package browsercontrol

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
	BrowserProfileDir string `json:"browserProfileDir"`
	BrowserHeadless   bool   `json:"browserHeadless"`
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
		return fmt.Errorf("prepare bundled browser-control runtime: %w", err)
	}
	node, err := bridgeNodeExecutable()
	if err != nil {
		return err
	}
	stateDir := filepath.Join(m.cfgDir, "browser-control")
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
		return fmt.Errorf("start browser-control bridge: %w", err)
	}
	readyCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go scanBridgeStdout(stdout, readyCh, errCh)
	go scanBridgeStderr(stderr, stderrCapture)
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("browser-control bridge exited: %v", err)
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
		return errors.New("timed out waiting for browser-control bridge")
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
	browserControl := settings.BrowserControl
	if browserControl == nil {
		browserControl = config.DefaultSettings().BrowserControl
	}
	profileDir := filepath.Join(m.cfgDir, "browser-control-profile")
	showBrowser := false
	if browserControl != nil {
		showBrowser = browserControl.ShowBrowser != nil && *browserControl.ShowBrowser
		if rawProfile := stringPtr(browserControl.BrowserProfileDir); strings.TrimSpace(rawProfile) != "" {
			if filepath.IsAbs(rawProfile) {
				profileDir = rawProfile
			} else {
				profileDir = filepath.Join(m.cfgDir, rawProfile)
			}
		}
	}
	return BridgeConfig{
		BrowserProfileDir: profileDir,
		BrowserHeadless:   !showBrowser,
	}
}

func (m *Manager) getJSON(ctx context.Context, path string) (map[string]any, error) {
	m.mu.Lock()
	base := m.baseURL
	m.mu.Unlock()
	if base == "" {
		return nil, errors.New("browser-control bridge is not running")
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
		return nil, errors.New("browser-control bridge is not running")
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
			msg = fmt.Sprintf("browser-control bridge HTTP %d", resp.StatusCode)
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
		return "", errors.New("browser-control config directory is not configured")
	}
	return materializeEmbeddedBridgeScript(filepath.Join(m.cfgDir, "browser-control", "bridge.mjs"))
}

var findSourceBridgeScriptPath = sourceBridgeScriptPath

func sourceBridgeScriptPath() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	candidates := []string{
		filepath.Join(filepath.Dir(file), "bridge.mjs"),
		filepath.Join(repoRootFromScript(file), "server", "internal", "browsercontrol", "bridge.mjs"),
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
		return "", errors.New("embedded browser-control bridge script path is empty")
	}
	if len(embeddedBridgeScript) == 0 {
		return "", errors.New("embedded browser-control bridge script is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("prepare browser-control bridge directory: %w", err)
	}
	if current, err := os.ReadFile(path); err == nil {
		if bytes.Equal(current, embeddedBridgeScript) {
			if err := os.Chmod(path, 0o600); err != nil {
				return "", fmt.Errorf("secure embedded browser-control bridge script: %w", err)
			}
			return path, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read browser-control bridge script: %w", err)
	}
	if err := os.WriteFile(path, embeddedBridgeScript, 0o600); err != nil {
		return "", fmt.Errorf("write embedded browser-control bridge script: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("secure embedded browser-control bridge script: %w", err)
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
		return "", errors.New("node is required for browser-control automation; download the full release bundle or install Node.js")
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
		for _, name := range []string{"browser-control", "atlassian"} {
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
	for _, name := range []string{"browser-control", "atlassian"} {
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
				log.Printf("browser-control bridge: %s", line)
			}
			continue
		}
		log.Printf("browser-control bridge: %s", line)
	}
	if err := scanner.Err(); err != nil {
		errCh <- err
		return
	}
	if !reportedReady {
		errCh <- errors.New("browser-control bridge exited before reporting ready")
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
		if line == "" {
			continue
		}
		if capture != nil {
			capture.Append(line)
		}
		log.Printf("browser-control bridge stderr: %s", line)
	}
}

func stringPtr(ptr *string) string {
	if ptr == nil {
		return ""
	}
	return strings.TrimSpace(*ptr)
}
