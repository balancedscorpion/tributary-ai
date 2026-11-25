package atlassian

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
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
	JiraBaseURL           string   `json:"jiraBaseUrl"`
	ConfluenceBaseURL     string   `json:"confluenceBaseUrl"`
	DefaultJiraProjectKey string   `json:"defaultJiraProjectKey,omitempty"`
	AllowedProjects       []string `json:"allowedProjects,omitempty"`
	AllowedAuthDomains    []string `json:"allowedAuthDomains,omitempty"`
	BrowserProvider       string   `json:"browserProvider,omitempty"`
	BrowserChannel        string   `json:"browserChannel,omitempty"`
	BrowserExecutablePath string   `json:"browserExecutablePath,omitempty"`
	BrowserProfileDir     string   `json:"browserProfileDir"`
	BrowserHeadless       bool     `json:"browserHeadless"`
}

type PendingApproval struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Detail    string         `json:"detail"`
	ToolName  string         `json:"toolName"`
	Product   string         `json:"product"`
	Action    string         `json:"action"`
	Target    map[string]any `json:"target,omitempty"`
	Content   map[string]any `json:"content,omitempty"`
	CreatedAt string         `json:"createdAt"`
}

type approvalAction struct {
	PendingApproval
	BridgeTool string
	Args       map[string]any
}

type Manager struct {
	cfgDir   string
	settings *config.Store
	client   *http.Client

	mu      sync.Mutex
	cmd     *exec.Cmd
	baseURL string

	approvals map[string]approvalAction
}

func NewManager(cfgDir string, settings *config.Store) *Manager {
	return &Manager{
		cfgDir:   strings.TrimSpace(cfgDir),
		settings: settings,
		client: &http.Client{
			Timeout: 90 * time.Second,
		},
		approvals: map[string]approvalAction{},
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
		m.persistAtlassianFailure(err.Error())
		return result, err
	}
	m.persistAtlassianProductHealth(result, "Atlassian connection checked.")
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
		m.persistAtlassianFailure(err.Error())
		return result, err
	}
	m.persistAtlassianProductHealth(result, "Atlassian sign-in completed.")
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
			return map[string]any{"ok": false}, fmt.Errorf("Atlassian browser profile is outside the app config directory and must be cleared manually: %s", profileDir)
		}
		if err := os.RemoveAll(profileDir); err != nil {
			return map[string]any{"ok": false}, fmt.Errorf("clear Atlassian browser profile: %w", err)
		}
	}

	m.persistAtlassianSignedOut("Signed out of Atlassian.")
	products := map[string]any{}
	if strings.TrimSpace(cfg.JiraBaseURL) != "" {
		products["jira"] = map[string]any{"product": "jira", "connected": false, "state": "disconnected", "url": cfg.JiraBaseURL}
	}
	if strings.TrimSpace(cfg.ConfluenceBaseURL) != "" {
		products["confluence"] = map[string]any{"product": "confluence", "connected": false, "state": "disconnected", "url": cfg.ConfluenceBaseURL}
	}
	return map[string]any{
		"ok":           true,
		"disconnected": true,
		"products":     products,
		"message":      "Signed out of Atlassian.",
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

func (m *Manager) QueueApproval(input PendingApproval, bridgeTool string, args map[string]any) PendingApproval {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = fmt.Sprintf("atl-%d", time.Now().UnixNano())
	}
	createdAt := strings.TrimSpace(input.CreatedAt)
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339)
	}
	input.ID = id
	input.CreatedAt = createdAt
	m.approvals[id] = approvalAction{
		PendingApproval: input,
		BridgeTool:      strings.TrimSpace(bridgeTool),
		Args:            cloneMap(args),
	}
	return input
}

func (m *Manager) ListApprovals() []PendingApproval {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PendingApproval, 0, len(m.approvals))
	for _, approval := range m.approvals {
		out = append(out, approval.PendingApproval)
	}
	return out
}

func (m *Manager) Approve(ctx context.Context, id string) (map[string]any, error) {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	action, ok := m.approvals[id]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown approval id: %s", id)
	}
	result, err := m.executeApprovedBridgeOperation(ctx, action.BridgeTool, action.Args)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.approvals, id)
	m.mu.Unlock()
	return map[string]any{
		"approval": action.PendingApproval,
		"result":   result,
	}, nil
}

func (m *Manager) RunApprovedOperation(ctx context.Context, bridgeTool string, args map[string]any) (map[string]any, error) {
	return m.executeApprovedBridgeOperation(ctx, bridgeTool, args)
}

func (m *Manager) executeApprovedBridgeOperation(ctx context.Context, bridgeTool string, args map[string]any) (map[string]any, error) {
	result, err := m.CallTool(ctx, strings.TrimSpace(bridgeTool), cloneMap(args))
	if err != nil {
		return nil, err
	}
	if err := approvalExecutionError(result); err != nil {
		return nil, err
	}
	return result, nil
}

func (m *Manager) Reject(id string) (PendingApproval, error) {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	action, ok := m.approvals[id]
	if !ok {
		return PendingApproval{}, fmt.Errorf("unknown approval id: %s", id)
	}
	delete(m.approvals, id)
	return action.PendingApproval, nil
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
		return fmt.Errorf("prepare bundled Atlassian runtime: %w", err)
	}
	node, err := bridgeNodeExecutable()
	if err != nil {
		return err
	}
	stateDir := filepath.Join(m.cfgDir, "atlassian")
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
		return fmt.Errorf("start Atlassian browser bridge: %w", err)
	}
	readyCh := make(chan string, 1)
	errCh := make(chan error, 1)
	go scanBridgeStdout(stdout, readyCh, errCh)
	go scanBridgeStderr(stderr, stderrCapture)
	go func() {
		if err := cmd.Wait(); err != nil {
			log.Printf("atlassian bridge exited: %v", err)
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
		return errors.New("timed out waiting for Atlassian browser bridge")
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
	atl := settings.Atlassian
	if atl == nil {
		atl = config.DefaultSettings().Atlassian
	}
	jiraBase := ""
	confluenceBase := ""
	defaultProject := ""
	browserProvider := "bundled"
	browserChannel := ""
	browserExecutablePath := ""
	profileDir := filepath.Join(m.cfgDir, "atlassian-browser-profile")
	if atl != nil {
		jiraBase = stringPtr(atl.JiraBaseURL)
		confluenceBase = stringPtr(atl.ConfluenceBaseURL)
		defaultProject = stringPtr(atl.DefaultJiraProjectKey)
		if rawProfile := stringPtr(atl.BrowserProfileDir); strings.TrimSpace(rawProfile) != "" {
			if filepath.IsAbs(rawProfile) {
				profileDir = rawProfile
			} else {
				profileDir = filepath.Join(m.cfgDir, rawProfile)
			}
		}
	}
	if confluenceBase == "" && jiraBase != "" {
		confluenceBase = strings.TrimRight(jiraBase, "/") + "/wiki"
	}
	showBrowser := false
	if atl != nil {
		showBrowser = productShowBrowser(atl.Jira) || productShowBrowser(atl.Confluence)
	}
	allowedAuthDomains := []string{"id.atlassian.com", "auth.atlassian.com", "login.microsoftonline.com"}
	if atl != nil && len(atl.AllowedAuthDomains) > 0 {
		allowedAuthDomains = append([]string(nil), atl.AllowedAuthDomains...)
	}
	allowedProjects := []string(nil)
	if atl != nil && len(atl.AllowedProjects) > 0 {
		allowedProjects = append([]string(nil), atl.AllowedProjects...)
	}
	return BridgeConfig{
		JiraBaseURL:           jiraBase,
		ConfluenceBaseURL:     confluenceBase,
		DefaultJiraProjectKey: defaultProject,
		AllowedProjects:       allowedProjects,
		AllowedAuthDomains:    allowedAuthDomains,
		BrowserProvider:       browserProvider,
		BrowserChannel:        browserChannel,
		BrowserExecutablePath: browserExecutablePath,
		BrowserProfileDir:     profileDir,
		BrowserHeadless:       !showBrowser,
	}
}

func (m *Manager) getJSON(ctx context.Context, path string) (map[string]any, error) {
	m.mu.Lock()
	base := m.baseURL
	m.mu.Unlock()
	if base == "" {
		return nil, errors.New("Atlassian bridge is not running")
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
		return nil, errors.New("Atlassian bridge is not running")
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
			msg = fmt.Sprintf("Atlassian bridge HTTP %d", resp.StatusCode)
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
		return "", errors.New("Atlassian config directory is not configured")
	}
	return materializeEmbeddedBridgeScript(filepath.Join(m.cfgDir, "atlassian", "bridge.mjs"))
}

var findSourceBridgeScriptPath = sourceBridgeScriptPath

func sourceBridgeScriptPath() (string, bool) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", false
	}
	candidates := []string{
		filepath.Join(filepath.Dir(file), "bridge.mjs"),
		filepath.Join(repoRootFromScript(file), "server", "internal", "atlassian", "bridge.mjs"),
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
		return "", errors.New("embedded Atlassian bridge script path is empty")
	}
	if len(embeddedBridgeScript) == 0 {
		return "", errors.New("embedded Atlassian bridge script is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("prepare Atlassian bridge directory: %w", err)
	}
	if current, err := os.ReadFile(path); err == nil {
		if bytes.Equal(current, embeddedBridgeScript) {
			if err := os.Chmod(path, 0o600); err != nil {
				return "", fmt.Errorf("secure embedded Atlassian bridge script: %w", err)
			}
			return path, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read Atlassian bridge script: %w", err)
	}
	if err := os.WriteFile(path, embeddedBridgeScript, 0o600); err != nil {
		return "", fmt.Errorf("write embedded Atlassian bridge script: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("secure embedded Atlassian bridge script: %w", err)
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
	candidates := append(bundledNodeExecutables(), systemNodeExecutableCandidates()...)
	failures := make([]string, 0, len(candidates))
	for _, node := range dedupeNodeExecutableCandidates(candidates) {
		if err := probeNodeExecutable(node); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", node, err))
			continue
		}
		return node, nil
	}

	message := "node is required for Atlassian browser automation; download the full release bundle or install Node.js"
	if len(failures) > 0 {
		message += "; unusable Node candidates: " + strings.Join(failures, " | ")
	}
	return "", errors.New(message)
}

func systemNodeExecutableCandidates() []string {
	candidates := make([]string, 0, 4)
	if node, err := findNodeInPath("node"); err == nil {
		candidates = append(candidates, node)
	}

	names := []string{"node"}
	if runtime.GOOS == "windows" {
		names = []string{"node.exe", "node"}
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		for _, name := range names {
			candidate := filepath.Join(dir, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				candidates = append(candidates, candidate)
			}
		}
	}
	return dedupeNodeExecutableCandidates(candidates)
}

func probeNodeExecutable(node string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, node, "--version").CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errors.New("timed out while checking node --version")
	}
	detail := strings.TrimSpace(string(output))
	if err != nil {
		if detail == "" {
			return err
		}
		return fmt.Errorf("%w (%s)", err, compactNodeProbeOutput(detail))
	}
	if !strings.HasPrefix(detail, "v") {
		return fmt.Errorf("unexpected node --version output %q", compactNodeProbeOutput(detail))
	}
	return nil
}

func compactNodeProbeOutput(output string) string {
	output = strings.Join(strings.Fields(output), " ")
	const maxLength = 240
	if len(output) <= maxLength {
		return output
	}
	return output[:maxLength] + "..."
}

func dedupeNodeExecutableCandidates(candidates []string) []string {
	out := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		key := candidate
		if absolute, err := filepath.Abs(candidate); err == nil {
			key = absolute
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, candidate)
	}
	return out
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
		candidate := filepath.Join(root, "atlassian")
		if hasPlaywrightPackageRoot(candidate) {
			return candidate, true
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
	if hasPlaywrightPackageRoot(filepath.Join(root, "atlassian")) {
		return true
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

func bundledNodeExecutables() []string {
	var candidates []string
	for _, root := range runtimeRootCandidates() {
		rootCandidates := []string{
			filepath.Join(root, "node", "bin", "node"),
			filepath.Join(root, "node", "node.exe"),
		}
		for _, candidate := range rootCandidates {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				candidates = append(candidates, candidate)
			}
		}
	}
	return dedupeNodeExecutableCandidates(candidates)
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
				log.Printf("atlassian bridge: %s", line)
			}
			continue
		}
		log.Printf("atlassian bridge: %s", line)
	}
	if err := scanner.Err(); err != nil {
		errCh <- err
		return
	}
	if !reportedReady {
		errCh <- errors.New("Atlassian bridge exited before reporting ready")
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
			log.Printf("atlassian bridge stderr: %s", line)
		}
	}
}

func stringPtr(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func productShowBrowser(product *config.AtlassianProductSettings) bool {
	return product != nil && product.ShowBrowser != nil && *product.ShowBrowser
}

func (m *Manager) persistAtlassianProductHealth(result map[string]any, fallbackMessage string) {
	if m == nil || m.settings == nil {
		return
	}
	products := mapFromAny(result["products"])
	if len(products) == 0 {
		return
	}
	next := m.settings.Get()
	ensureAtlassianSettings(&next)
	checkedAt := time.Now().UnixMilli()
	if product := mapFromAny(products["jira"]); len(product) > 0 {
		setAtlassianProductHealth(next.Atlassian.Jira, checkedAt, atlassianProductConnected(product), atlassianProductMessage(product, fallbackMessage))
	}
	if product := mapFromAny(products["confluence"]); len(product) > 0 {
		setAtlassianProductHealth(next.Atlassian.Confluence, checkedAt, atlassianProductConnected(product), atlassianProductMessage(product, fallbackMessage))
	}
	_ = m.settings.Save(next)
}

func (m *Manager) persistAtlassianFailure(message string) {
	if m == nil || m.settings == nil {
		return
	}
	next := m.settings.Get()
	ensureAtlassianSettings(&next)
	checkedAt := time.Now().UnixMilli()
	checkedMessage := strings.TrimSpace(message)
	if checkedMessage == "" {
		checkedMessage = "Atlassian connection check failed."
	}
	if stringPtr(next.Atlassian.JiraBaseURL) != "" {
		setAtlassianProductHealth(next.Atlassian.Jira, checkedAt, false, checkedMessage)
	}
	if stringPtr(next.Atlassian.ConfluenceBaseURL) != "" {
		setAtlassianProductHealth(next.Atlassian.Confluence, checkedAt, false, checkedMessage)
	}
	_ = m.settings.Save(next)
}

func (m *Manager) persistAtlassianSignedOut(message string) {
	if m == nil || m.settings == nil {
		return
	}
	next := m.settings.Get()
	ensureAtlassianSettings(&next)
	checkedAt := time.Now().UnixMilli()
	checkedMessage := strings.TrimSpace(message)
	if checkedMessage == "" {
		checkedMessage = "Signed out of Atlassian."
	}
	off := false
	next.Atlassian.Jira.Enabled = &off
	next.Atlassian.Confluence.Enabled = &off
	setAtlassianProductHealth(next.Atlassian.Jira, checkedAt, false, checkedMessage)
	setAtlassianProductHealth(next.Atlassian.Confluence, checkedAt, false, checkedMessage)
	_ = m.settings.Save(next)
}

func ensureAtlassianSettings(settings *config.AppSettings) {
	if settings == nil {
		return
	}
	if settings.Atlassian == nil {
		settings.Atlassian = config.DefaultSettings().Atlassian
	}
	if settings.Atlassian == nil {
		settings.Atlassian = &config.AtlassianSettings{}
	}
	if settings.Atlassian.Jira == nil {
		settings.Atlassian.Jira = &config.AtlassianProductSettings{}
	}
	if settings.Atlassian.Confluence == nil {
		settings.Atlassian.Confluence = &config.AtlassianProductSettings{}
	}
}

func setAtlassianProductHealth(product *config.AtlassianProductSettings, checkedAt int64, ok bool, message string) {
	if product == nil {
		return
	}
	checkedMessage := strings.TrimSpace(message)
	if checkedMessage == "" {
		if ok {
			checkedMessage = "Atlassian connection verified."
		} else {
			checkedMessage = "Atlassian connection check failed."
		}
	}
	product.LastCheckedAt = &checkedAt
	product.LastCheckOk = &ok
	product.LastCheckMessage = &checkedMessage
}

func atlassianProductConnected(product map[string]any) bool {
	if product == nil {
		return false
	}
	if connected, ok := product["connected"].(bool); ok {
		return connected
	}
	return strings.EqualFold(strings.TrimSpace(fmt.Sprint(product["state"])), "connected")
}

func atlassianProductMessage(product map[string]any, fallback string) string {
	if product == nil {
		return strings.TrimSpace(fallback)
	}
	for _, key := range []string{"message", "error", "state"} {
		if value := strings.TrimSpace(fmt.Sprint(product[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return strings.TrimSpace(fallback)
}

func approvalExecutionError(result map[string]any) error {
	if result == nil {
		return errors.New("Atlassian approval execution returned an empty result")
	}
	ok, hasOK := result["ok"].(bool)
	if !hasOK || ok {
		return nil
	}

	parts := make([]string, 0, 4)
	for _, errText := range stringListFromAny(result["errors"]) {
		if trimmed := strings.TrimSpace(errText); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if msg := strings.TrimSpace(fmt.Sprint(result["message"])); msg != "" && msg != "<nil>" {
		parts = append(parts, msg)
	}
	if status := strings.TrimSpace(fmt.Sprint(result["status"])); status != "" && status != "<nil>" && status != "0" {
		parts = append(parts, "HTTP "+status)
	}
	if len(parts) == 0 {
		parts = append(parts, "the Atlassian bridge reported ok=false")
	}
	return fmt.Errorf("Atlassian approval execution failed: %s", strings.Join(dedupeStrings(parts), "; "))
}

func mapFromAny(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	default:
		return nil
	}
}

func stringListFromAny(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" && text != "<nil>" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func LocalURL(host string, port int) string {
	if strings.TrimSpace(host) == "" {
		host = "127.0.0.1"
	}
	if port <= 0 {
		return ""
	}
	return "http://" + net.JoinHostPort(host, fmt.Sprintf("%d", port))
}
