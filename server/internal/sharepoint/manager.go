package sharepoint

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/config"
)

type BridgeConfig struct {
	SiteURL           string `json:"siteUrl"`
	BrowserProfileDir string `json:"browserProfileDir"`
	BrowserHeadless   bool   `json:"browserHeadless"`
	AuthMode          string `json:"authMode"`
}

type Manager struct {
	cfgDir   string
	settings *config.Store
	client   *http.Client

	mu      sync.Mutex
	cmd     *exec.Cmd
	baseURL string
}

func NewManager(cfgDir string, settings *config.Store) *Manager {
	return &Manager{
		cfgDir:   strings.TrimSpace(cfgDir),
		settings: settings,
		client: &http.Client{
			Timeout: 90 * time.Second,
		},
	}
}

func (m *Manager) Status(ctx context.Context) (map[string]any, error) {
	if err := m.ensureBridge(ctx); err != nil {
		return nil, err
	}
	if err := m.syncConfig(ctx); err != nil {
		return nil, err
	}
	return m.getJSON(ctx, "/status")
}

func (m *Manager) CheckConnection(ctx context.Context) (map[string]any, error) {
	if err := m.ensureBridge(ctx); err != nil {
		return nil, err
	}
	if err := m.syncConfig(ctx); err != nil {
		return nil, err
	}
	result, err := m.postJSON(ctx, "/check", map[string]any{})
	if err != nil {
		m.persistFailure(err.Error())
		return result, err
	}
	m.persistHealth(result, "SharePoint connection checked.")
	return result, nil
}

func (m *Manager) StartLogin(ctx context.Context) (map[string]any, error) {
	if err := m.ensureBridge(ctx); err != nil {
		return nil, err
	}
	if err := m.syncConfig(ctx); err != nil {
		return nil, err
	}
	return m.postJSON(ctx, "/login/start", map[string]any{})
}

func (m *Manager) FinishLogin(ctx context.Context) (map[string]any, error) {
	if err := m.ensureBridge(ctx); err != nil {
		return nil, err
	}
	if err := m.syncConfig(ctx); err != nil {
		return nil, err
	}
	result, err := m.postJSON(ctx, "/login/finish", map[string]any{})
	if err != nil {
		m.persistFailure(err.Error())
		return result, err
	}
	m.persistHealth(result, "SharePoint sign-in completed.")
	return result, nil
}

func (m *Manager) Disconnect(ctx context.Context) (map[string]any, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
	}
	cfg := m.bridgeConfig()
	profileDir := strings.TrimSpace(cfg.BrowserProfileDir)

	m.mu.Lock()
	cmd := m.cmd
	m.cmd = nil
	m.baseURL = ""
	m.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}

	if profileDir != "" {
		if !pathWithinRoot(profileDir, m.cfgDir) {
			return map[string]any{"ok": false}, fmt.Errorf("SharePoint browser profile is outside the app config directory and must be cleared manually: %s", profileDir)
		}
		if err := os.RemoveAll(profileDir); err != nil {
			return map[string]any{"ok": false}, fmt.Errorf("clear SharePoint browser profile: %w", err)
		}
	}

	m.persistSignedOut("Signed out of SharePoint.")
	return map[string]any{
		"ok":           true,
		"disconnected": true,
		"site":         map[string]any{"connected": false, "state": "disconnected", "url": cfg.SiteURL},
		"message":      "Signed out of SharePoint.",
	}, nil
}

func (m *Manager) CallTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	if err := m.ensureBridge(ctx); err != nil {
		return nil, err
	}
	if err := m.syncConfig(ctx); err != nil {
		return nil, err
	}
	return m.postJSON(ctx, "/tool/"+strings.TrimSpace(name), args)
}

func (m *Manager) ensureBridge(ctx context.Context) error {
	m.mu.Lock()
	if m.baseURL != "" && m.cmd != nil && m.cmd.Process != nil {
		base := m.baseURL
		m.mu.Unlock()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
		resp, err := m.client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
		}
		m.mu.Lock()
		m.baseURL = ""
		m.cmd = nil
	}
	m.mu.Unlock()

	return m.startBridge(ctx)
}

func (m *Manager) startBridge(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.baseURL != "" && m.cmd != nil && m.cmd.Process != nil {
		return nil
	}
	script, err := m.bridgeScriptPath()
	if err != nil {
		return err
	}
	runtimePaths := bridgeRuntimePathsForScript(script)
	if err := prepareBundledRuntimeForLaunch(runtimePaths.RuntimeRoot); err != nil {
		return fmt.Errorf("prepare bundled SharePoint runtime: %w", err)
	}
	node, err := bridgeNodeExecutable()
	if err != nil {
		return err
	}
	stateDir := filepath.Join(m.cfgDir, "sharepoint")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	args := []string{script, "--state-dir", stateDir, "--port", "0", "--node-module-root", runtimePaths.NodeModuleRoot}
	if runtimePaths.BrowserRoot != "" {
		args = append(args, "--browser-root", runtimePaths.BrowserRoot)
	}
	cmd := exec.Command(node, args...)
	cmd.Dir = runtimePaths.WorkingDirectory
	if runtimePaths.BrowserRoot != "" {
		cmd.Env = append(os.Environ(), "PLAYWRIGHT_BROWSERS_PATH="+runtimePaths.BrowserRoot)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	stderrCapture := &bridgeOutputCapture{}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start SharePoint browser bridge: %w", err)
	}
	readyCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go scanBridgeStdout(stdout, readyCh, errCh)
	go scanBridgeStderr(stderr, stderrCapture)
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("sharepoint bridge exited: %v", err)
		}
	}()

	select {
	case base := <-readyCh:
		m.cmd = cmd
		m.baseURL = strings.TrimRight(base, "/")
		return nil
	case err := <-errCh:
		_ = cmd.Process.Kill()
		time.Sleep(50 * time.Millisecond)
		return bridgeStartupError(err, stderrCapture.String())
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		return errors.New("timed out waiting for SharePoint browser bridge")
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return ctx.Err()
	}
}

func (m *Manager) syncConfig(ctx context.Context) error {
	cfg := m.bridgeConfig()
	_, err := m.postJSON(ctx, "/config", cfg)
	return err
}

func (m *Manager) bridgeConfig() BridgeConfig {
	settings := config.DefaultSettings()
	if m.settings != nil {
		settings = m.settings.Get()
	}
	sp := settings.SharePoint
	if sp == nil {
		sp = config.DefaultSettings().SharePoint
	}
	siteURL := ""
	profileDir := filepath.Join(m.cfgDir, "sharepoint-browser-profile")
	showBrowser := false
	authMode := "browser"
	if sp != nil {
		siteURL = stringPtr(sp.SiteURL)
		showBrowser = sp.ShowBrowser != nil && *sp.ShowBrowser
		if rawProfile := stringPtr(sp.BrowserProfileDir); strings.TrimSpace(rawProfile) != "" {
			if filepath.IsAbs(rawProfile) {
				profileDir = rawProfile
			} else {
				profileDir = filepath.Join(m.cfgDir, rawProfile)
			}
		}
		if strings.EqualFold(stringPtr(sp.AuthMode), "graph") {
			authMode = "graph"
		}
	}
	return BridgeConfig{
		SiteURL:           siteURL,
		BrowserProfileDir: profileDir,
		BrowserHeadless:   !showBrowser,
		AuthMode:          authMode,
	}
}

func (m *Manager) getJSON(ctx context.Context, path string) (map[string]any, error) {
	m.mu.Lock()
	base := m.baseURL
	m.mu.Unlock()
	if base == "" {
		return nil, errors.New("SharePoint bridge is not running")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeBridgeResponse(resp)
}

func (m *Manager) postJSON(ctx context.Context, path string, body any) (map[string]any, error) {
	m.mu.Lock()
	base := m.baseURL
	m.mu.Unlock()
	if base == "" {
		return nil, errors.New("SharePoint bridge is not running")
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return decodeBridgeResponse(resp)
}

func decodeBridgeResponse(resp *http.Response) (map[string]any, error) {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	var out map[string]any
	if len(bytes.TrimSpace(b)) > 0 {
		_ = json.Unmarshal(b, &out)
	}
	if out == nil {
		out = map[string]any{}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(fmt.Sprint(out["error"]))
		if msg == "" {
			msg = strings.TrimSpace(string(b))
		}
		if msg == "" {
			msg = fmt.Sprintf("SharePoint bridge HTTP %d", resp.StatusCode)
		}
		return out, errors.New(msg)
	}
	return out, nil
}

func (m *Manager) bridgeScriptPath() (string, error) {
	if script, ok := findSourceBridgeScriptPath(); ok {
		return script, nil
	}
	if strings.TrimSpace(m.cfgDir) == "" {
		return "", errors.New("SharePoint config directory is not configured")
	}
	return materializeEmbeddedBridgeScript(filepath.Join(m.cfgDir, "sharepoint", "bridge.mjs"))
}

var findSourceBridgeScriptPath = sourceBridgeScriptPath

func sourceBridgeScriptPath() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	candidates := []string{
		filepath.Join(filepath.Dir(file), "bridge.mjs"),
		filepath.Join(repoRootFromScript(file), "server", "internal", "sharepoint", "bridge.mjs"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

func materializeEmbeddedBridgeScript(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("embedded SharePoint bridge script path is empty")
	}
	if len(embeddedBridgeScript) == 0 {
		return "", errors.New("embedded SharePoint bridge script is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("prepare SharePoint bridge directory: %w", err)
	}
	if current, err := os.ReadFile(path); err == nil {
		if bytes.Equal(current, embeddedBridgeScript) {
			if err := os.Chmod(path, 0o600); err != nil {
				return "", fmt.Errorf("secure embedded SharePoint bridge script: %w", err)
			}
			return path, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read SharePoint bridge script: %w", err)
	}
	if err := os.WriteFile(path, embeddedBridgeScript, 0o600); err != nil {
		return "", fmt.Errorf("write embedded SharePoint bridge script: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("secure embedded SharePoint bridge script: %w", err)
	}
	return path, nil
}

func repoRootFromScript(script string) string {
	dir := filepath.Dir(script)
	for {
		if _, err := os.Stat(filepath.Join(dir, "package.json")); err == nil {
			return dir
		}
		next := filepath.Dir(dir)
		if next == dir {
			return filepath.Dir(script)
		}
		dir = next
	}
}

type bridgeRuntimePaths struct {
	NodeModuleRoot   string
	BrowserRoot      string
	WorkingDirectory string
	RuntimeRoot      string
}

var (
	findNodeInPath        = exec.LookPath
	runtimeRootCandidates = defaultRuntimeRootCandidates
)

func bridgeNodeExecutable() (string, error) {
	if node, ok := bundledNodeExecutable(); ok {
		return node, nil
	}
	node, err := findNodeInPath("node")
	if err != nil {
		return "", errors.New("node is required for SharePoint browser automation; download the full release bundle or install Node.js")
	}
	return node, nil
}

func bridgeRuntimePathsForScript(script string) bridgeRuntimePaths {
	nodeRoot := bridgeNodeModuleRoot(script)
	browserRoot := bundledPlaywrightBrowserRoot()
	return bridgeRuntimePaths{
		NodeModuleRoot:   nodeRoot,
		BrowserRoot:      browserRoot,
		WorkingDirectory: nodeRoot,
		RuntimeRoot:      bundledRuntimeRoot(nodeRoot, browserRoot),
	}
}

func bridgeNodeModuleRoot(script string) string {
	if root, ok := bundledNodeModuleRoot(); ok {
		return root
	}
	root := repoRootFromScript(script)
	if hasPlaywrightPackageRoot(root) {
		return root
	}
	if cwd, err := os.Getwd(); err == nil && hasPlaywrightPackageRoot(cwd) {
		return cwd
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		if hasPlaywrightPackageRoot(exeDir) {
			return exeDir
		}
	}
	return filepath.Dir(script)
}

func bundledNodeModuleRoot() (string, bool) {
	for _, root := range runtimeRootCandidates() {
		for _, name := range []string{"sharepoint", "browser-control", "atlassian"} {
			candidate := filepath.Join(root, name)
			if hasPlaywrightPackageRoot(candidate) {
				return candidate, true
			}
		}
	}
	return "", false
}

func bundledPlaywrightBrowserRoot() string {
	for _, root := range runtimeRootCandidates() {
		candidate := filepath.Join(root, "ms-playwright")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

func bundledRuntimeRoot(paths ...string) string {
	for _, root := range runtimeRootCandidates() {
		if strings.TrimSpace(root) == "" {
			continue
		}
		if !runtimeRootHasBundledAssets(root) {
			continue
		}
		for _, path := range paths {
			if pathWithinRoot(path, root) {
				return root
			}
		}
	}
	return ""
}

func runtimeRootHasBundledAssets(root string) bool {
	if strings.TrimSpace(root) == "" {
		return false
	}
	for _, name := range []string{"sharepoint", "browser-control", "atlassian"} {
		if hasPlaywrightPackageRoot(filepath.Join(root, name)) {
			return true
		}
	}
	if info, err := os.Stat(filepath.Join(root, "ms-playwright")); err == nil && info.IsDir() {
		return true
	}
	for _, candidate := range []string{
		filepath.Join(root, "node", "bin", "node"),
		filepath.Join(root, "node", "node.exe"),
	} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func pathWithinRoot(path string, root string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}

func bundledNodeExecutable() (string, bool) {
	for _, root := range runtimeRootCandidates() {
		candidates := []string{
			filepath.Join(root, "node", "bin", "node"),
			filepath.Join(root, "node", "node.exe"),
		}
		for _, candidate := range candidates {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, true
			}
		}
	}
	return "", false
}

func defaultRuntimeRootCandidates() []string {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	exeDir := filepath.Dir(exe)
	return []string{
		filepath.Join(exeDir, "runtime"),
		filepath.Join(exeDir, "resources", "runtime"),
		filepath.Join(exeDir, "..", "runtime"),
		filepath.Join(exeDir, "..", "resources", "runtime"),
		filepath.Join(exeDir, "..", "Resources", "runtime"),
		filepath.Join(exeDir, "..", "Resources", "resources", "runtime"),
	}
}

func hasPlaywrightPackageRoot(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "playwright", "package.json")); err == nil {
		return true
	}
	return false
}

func scanBridgeStdout(r io.Reader, readyCh chan<- string, errCh chan<- error) {
	scanner := bufio.NewScanner(r)
	reportedReady := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg struct {
			Event string `json:"event"`
			URL   string `json:"url"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err == nil {
			switch msg.Event {
			case "ready":
				reportedReady = true
				readyCh <- msg.URL
			case "error":
				errCh <- errors.New(msg.Error)
			default:
				log.Printf("sharepoint bridge: %s", line)
			}
			continue
		}
		log.Printf("sharepoint bridge: %s", line)
	}
	if err := scanner.Err(); err != nil {
		errCh <- err
		return
	}
	if !reportedReady {
		errCh <- errors.New("SharePoint bridge exited before reporting ready")
	}
}

type bridgeOutputCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *bridgeOutputCapture) Append(line string) {
	if c == nil {
		return
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, line)
	if len(c.lines) > 12 {
		c.lines = c.lines[len(c.lines)-12:]
	}
}

func (c *bridgeOutputCapture) String() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

func bridgeStartupError(err error, stderr string) error {
	if strings.TrimSpace(stderr) == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr))
}

func scanBridgeStderr(r io.Reader, capture *bridgeOutputCapture) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			capture.Append(line)
			log.Printf("sharepoint bridge stderr: %s", line)
		}
	}
}

func stringPtr(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func (m *Manager) persistHealth(result map[string]any, fallbackMessage string) {
	if m == nil || m.settings == nil {
		return
	}
	site := mapFromAny(result["site"])
	if len(site) == 0 {
		return
	}
	next := m.settings.Get()
	ensureSharePointSettings(&next)
	checkedAt := time.Now().UnixMilli()
	setSharePointHealth(next.SharePoint, checkedAt, siteConnected(site), siteMessage(site, fallbackMessage))
	_ = m.settings.Save(next)
}

func (m *Manager) persistFailure(message string) {
	if m == nil || m.settings == nil {
		return
	}
	next := m.settings.Get()
	ensureSharePointSettings(&next)
	checkedAt := time.Now().UnixMilli()
	checkedMessage := strings.TrimSpace(message)
	if checkedMessage == "" {
		checkedMessage = "SharePoint connection check failed."
	}
	setSharePointHealth(next.SharePoint, checkedAt, false, checkedMessage)
	_ = m.settings.Save(next)
}

func (m *Manager) persistSignedOut(message string) {
	if m == nil || m.settings == nil {
		return
	}
	next := m.settings.Get()
	ensureSharePointSettings(&next)
	checkedAt := time.Now().UnixMilli()
	checkedMessage := strings.TrimSpace(message)
	if checkedMessage == "" {
		checkedMessage = "Signed out of SharePoint."
	}
	off := false
	next.SharePoint.Enabled = &off
	setSharePointHealth(next.SharePoint, checkedAt, false, checkedMessage)
	_ = m.settings.Save(next)
}

func ensureSharePointSettings(settings *config.AppSettings) {
	if settings == nil {
		return
	}
	if settings.SharePoint == nil {
		settings.SharePoint = config.DefaultSettings().SharePoint
	}
	if settings.SharePoint == nil {
		settings.SharePoint = &config.SharePointSettings{}
	}
}

func setSharePointHealth(settings *config.SharePointSettings, checkedAt int64, ok bool, message string) {
	if settings == nil {
		return
	}
	checkedMessage := strings.TrimSpace(message)
	if checkedMessage == "" {
		if ok {
			checkedMessage = "SharePoint connection verified."
		} else {
			checkedMessage = "SharePoint connection check failed."
		}
	}
	settings.LastCheckedAt = &checkedAt
	settings.LastCheckOk = &ok
	settings.LastCheckMessage = &checkedMessage
}

func siteConnected(site map[string]any) bool {
	if site == nil {
		return false
	}
	if connected, ok := site["connected"].(bool); ok {
		return connected
	}
	return strings.EqualFold(strings.TrimSpace(fmt.Sprint(site["state"])), "connected")
}

func siteMessage(site map[string]any, fallback string) string {
	if site == nil {
		return fallback
	}
	for _, key := range []string{"message", "error", "state"} {
		if msg := strings.TrimSpace(fmt.Sprint(site[key])); msg != "" && msg != "<nil>" {
			return msg
		}
	}
	return fallback
}

func mapFromAny(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return nil
}
