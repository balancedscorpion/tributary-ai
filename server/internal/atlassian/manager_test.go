package atlassian

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/config"
)

func TestManagerBridgeScriptPathMaterializesEmbeddedFallback(t *testing.T) {
	originalFinder := findSourceBridgeScriptPath
	findSourceBridgeScriptPath = func() (string, bool) { return "", false }
	t.Cleanup(func() { findSourceBridgeScriptPath = originalFinder })

	cfgDir := t.TempDir()
	manager := NewManager(cfgDir, nil)
	script, err := manager.bridgeScriptPath()
	if err != nil {
		t.Fatalf("bridgeScriptPath returned error: %v", err)
	}

	want := filepath.Join(cfgDir, "atlassian", "bridge.mjs")
	if script != want {
		t.Fatalf("bridgeScriptPath = %q, want %q", script, want)
	}
	assertEmbeddedBridgeScript(t, script)
}

func TestManagerBridgeScriptPathPrefersSourceCheckout(t *testing.T) {
	originalFinder := findSourceBridgeScriptPath
	sourceScript := filepath.Join(t.TempDir(), "bridge.mjs")
	findSourceBridgeScriptPath = func() (string, bool) { return sourceScript, true }
	t.Cleanup(func() { findSourceBridgeScriptPath = originalFinder })

	manager := NewManager(t.TempDir(), nil)
	script, err := manager.bridgeScriptPath()
	if err != nil {
		t.Fatalf("bridgeScriptPath returned error: %v", err)
	}
	if script != sourceScript {
		t.Fatalf("bridgeScriptPath = %q, want source script %q", script, sourceScript)
	}
}

func TestMaterializeEmbeddedBridgeScriptRefreshesStaleFile(t *testing.T) {
	script := filepath.Join(t.TempDir(), "atlassian", "bridge.mjs")
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, []byte("stale bridge script"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := materializeEmbeddedBridgeScript(script)
	if err != nil {
		t.Fatalf("materializeEmbeddedBridgeScript returned error: %v", err)
	}
	if got != script {
		t.Fatalf("materializeEmbeddedBridgeScript = %q, want %q", got, script)
	}
	assertEmbeddedBridgeScript(t, script)
}

func TestScanBridgeStdoutReportsEarlyExitBeforeReady(t *testing.T) {
	readyCh := make(chan string, 1)
	errCh := make(chan error, 1)
	scanBridgeStdout(strings.NewReader(""), readyCh, errCh)

	select {
	case err := <-errCh:
		if err == nil || !strings.Contains(err.Error(), "before reporting ready") {
			t.Fatalf("unexpected error: %v", err)
		}
	default:
		t.Fatal("expected early-exit error")
	}
	select {
	case ready := <-readyCh:
		t.Fatalf("unexpected ready URL: %s", ready)
	default:
	}
}

func TestBridgeStartupErrorIncludesStderr(t *testing.T) {
	err := bridgeStartupError(errors.New("Atlassian bridge exited before reporting ready"), "Playwright package is required")
	if got := err.Error(); !strings.Contains(got, "before reporting ready") || !strings.Contains(got, "Playwright package is required") {
		t.Fatalf("bridgeStartupError = %q, want startup and stderr detail", got)
	}
}

func TestBridgeRuntimePrefersBundledNodePlaywrightAndBrowser(t *testing.T) {
	runtimeRoot := t.TempDir()
	nodePath := filepath.Join(runtimeRoot, "node", "bin", "node")
	playwrightPackage := filepath.Join(runtimeRoot, "atlassian", "node_modules", "playwright", "package.json")
	browserRoot := filepath.Join(runtimeRoot, "ms-playwright")
	if err := os.MkdirAll(filepath.Dir(nodePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nodePath, []byte("#!/bin/sh\nprintf 'v20.0.0\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(playwrightPackage), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(playwrightPackage, []byte(`{"name":"playwright"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(browserRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	originalRuntimeRoots := runtimeRootCandidates
	originalLookPath := findNodeInPath
	runtimeRootCandidates = func() []string { return []string{runtimeRoot} }
	findNodeInPath = func(string) (string, error) { return "", errors.New("system node should not be used") }
	t.Cleanup(func() {
		runtimeRootCandidates = originalRuntimeRoots
		findNodeInPath = originalLookPath
	})

	node, err := bridgeNodeExecutable()
	if err != nil {
		t.Fatalf("bridgeNodeExecutable returned error: %v", err)
	}
	if node != nodePath {
		t.Fatalf("bridgeNodeExecutable = %q, want bundled node %q", node, nodePath)
	}
	paths := bridgeRuntimePathsForScript(filepath.Join(t.TempDir(), "bridge.mjs"))
	if paths.NodeModuleRoot != filepath.Join(runtimeRoot, "atlassian") {
		t.Fatalf("NodeModuleRoot = %q, want bundled Atlassian runtime", paths.NodeModuleRoot)
	}
	if paths.BrowserRoot != browserRoot {
		t.Fatalf("BrowserRoot = %q, want bundled browser root %q", paths.BrowserRoot, browserRoot)
	}
	if paths.WorkingDirectory != paths.NodeModuleRoot {
		t.Fatalf("WorkingDirectory = %q, want node module root %q", paths.WorkingDirectory, paths.NodeModuleRoot)
	}
	if paths.RuntimeRoot != runtimeRoot {
		t.Fatalf("RuntimeRoot = %q, want bundled runtime root %q", paths.RuntimeRoot, runtimeRoot)
	}
}

func TestBridgeNodeExecutableSkipsBrokenPathCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script Node fixtures are Unix-only")
	}

	brokenDir := t.TempDir()
	healthyDir := t.TempDir()
	brokenNode := filepath.Join(brokenDir, "node")
	healthyNode := filepath.Join(healthyDir, "node")
	if err := os.WriteFile(brokenNode, []byte("#!/bin/sh\nprintf 'missing dynamic library\\n' >&2\nexit 127\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(healthyNode, []byte("#!/bin/sh\nprintf 'v22.0.0\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	originalRuntimeRoots := runtimeRootCandidates
	originalLookPath := findNodeInPath
	runtimeRootCandidates = func() []string { return nil }
	findNodeInPath = exec.LookPath
	t.Setenv("PATH", strings.Join([]string{brokenDir, healthyDir}, string(os.PathListSeparator)))
	t.Cleanup(func() {
		runtimeRootCandidates = originalRuntimeRoots
		findNodeInPath = originalLookPath
	})

	node, err := bridgeNodeExecutable()
	if err != nil {
		t.Fatalf("bridgeNodeExecutable returned error: %v", err)
	}
	if node != healthyNode {
		t.Fatalf("bridgeNodeExecutable = %q, want healthy fallback %q", node, healthyNode)
	}
}

func TestManagerBridgeConfigUsesBundledBrowserRuntime(t *testing.T) {
	cfgDir := t.TempDir()
	manager := NewManager(cfgDir, nil)
	cfg := manager.bridgeConfig()
	if cfg.BrowserProvider != "bundled" {
		t.Fatalf("default BrowserProvider = %q, want bundled", cfg.BrowserProvider)
	}

	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := store.Get()
	settings.Atlassian.JiraBaseURL = testStringPtr("https://example.atlassian.net")
	settings.Atlassian.BrowserProvider = testStringPtr("custom")
	settings.Atlassian.BrowserChannel = testStringPtr("custom-channel")
	settings.Atlassian.BrowserExecutablePath = testStringPtr("/custom/browser")
	settings.Atlassian.BrowserProfileDir = testStringPtr("atl-profile")
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}

	manager = NewManager(cfgDir, store)
	cfg = manager.bridgeConfig()
	if cfg.BrowserProvider != "bundled" {
		t.Fatalf("BrowserProvider = %q, want bundled", cfg.BrowserProvider)
	}
	if cfg.BrowserChannel != "" {
		t.Fatalf("BrowserChannel = %q, want empty", cfg.BrowserChannel)
	}
	if cfg.BrowserExecutablePath != "" {
		t.Fatalf("BrowserExecutablePath = %q, want empty", cfg.BrowserExecutablePath)
	}
	wantProfile := filepath.Join(cfgDir, "atl-profile")
	if cfg.BrowserProfileDir != wantProfile {
		t.Fatalf("BrowserProfileDir = %q, want %q", cfg.BrowserProfileDir, wantProfile)
	}
}

func TestApprovalExecutionErrorSurfacesBridgeFailures(t *testing.T) {
	err := approvalExecutionError(map[string]any{
		"ok":     false,
		"status": float64(401),
		"errors": []any{"Atlassian login is required.", "HTTP 401"},
	})
	if err == nil {
		t.Fatal("expected approval execution error")
	}
	got := err.Error()
	if !strings.Contains(got, "Atlassian login is required.") || !strings.Contains(got, "HTTP 401") {
		t.Fatalf("approvalExecutionError = %q, want login and status details", got)
	}
}

func TestApprovalExecutionErrorIgnoresSuccessfulBridgeResult(t *testing.T) {
	if err := approvalExecutionError(map[string]any{"ok": true}); err != nil {
		t.Fatalf("approvalExecutionError returned unexpected error: %v", err)
	}
	if err := approvalExecutionError(map[string]any{"result": "legacy bridge result"}); err != nil {
		t.Fatalf("approvalExecutionError returned unexpected legacy error: %v", err)
	}
}

func TestRunApprovedOperationAndApproveShareBridgeExecution(t *testing.T) {
	var mu sync.Mutex
	var toolBodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/tool/jira_add_comment_execute":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode bridge body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			toolBodies = append(toolBodies, body)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":        true,
				"issueKey":  body["issueKey"],
				"commentId": "10001",
				"result_metadata": map[string]any{
					"integration": "atlassian",
					"product":     "jira",
					"widget":      "write_result",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	manager := NewManager(t.TempDir(), nil)
	cmd := startSleepProcess(t)
	manager.mu.Lock()
	manager.cmd = cmd
	manager.baseURL = server.URL
	manager.mu.Unlock()

	args := map[string]any{"issueKey": "TEAM-123", "comment": "Approved comment"}
	direct, err := manager.RunApprovedOperation(context.Background(), "jira_add_comment_execute", args)
	if err != nil {
		t.Fatalf("RunApprovedOperation: %v", err)
	}
	if direct["commentId"] != "10001" {
		t.Fatalf("direct commentId=%#v, want 10001", direct["commentId"])
	}

	approval := manager.QueueApproval(PendingApproval{
		Title:    "Add Jira comment",
		ToolName: "jira_add_comment",
		Product:  "jira",
		Action:   "add_comment",
	}, "jira_add_comment_execute", args)
	approved, err := manager.Approve(context.Background(), approval.ID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	result := approved["result"].(map[string]any)
	if result["commentId"] != "10001" {
		t.Fatalf("approved commentId=%#v, want 10001", result["commentId"])
	}
	if approvals := manager.ListApprovals(); len(approvals) != 0 {
		t.Fatalf("approval was not cleared: %#v", approvals)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(toolBodies) != 2 {
		t.Fatalf("bridge tool call count=%d, want 2", len(toolBodies))
	}
	for i, body := range toolBodies {
		if body["issueKey"] != "TEAM-123" || body["comment"] != "Approved comment" {
			t.Fatalf("tool body %d = %#v, want shared approved args", i, body)
		}
	}
}

func startSleepProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skipf("sleep command unavailable for fake bridge process: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	return cmd
}

func TestEmbeddedBridgeAddsAtlassianCSRFBypassToJiraWrites(t *testing.T) {
	script := string(embeddedBridgeScript)
	for _, want := range []string{
		`return p.evaluate(async ({ targetUrl, requestMethod, requestBody, headers, timeoutMs }) =>`,
		`return p.evaluate(async ({ targetUrl, fileName, mimeType, contentBase64, headers, timeoutMs }) =>`,
		`credentials: "include"`,
		`function jiraRestMutationHeaders`,
		`"X-Atlassian-Token": "no-check"`,
		`"X-Requested-With": "XMLHttpRequest"`,
		`headers: jiraRestMutationHeaders({ "Content-Type": "application/json" })`,
		`headers: jiraRestMutationHeaders()`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("embedded Atlassian bridge missing %q", want)
		}
	}
}

func TestPersistAtlassianProductHealthAndSignOut(t *testing.T) {
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	settings := store.Get()
	on := true
	settings.Atlassian.JiraBaseURL = testStringPtr("https://example.atlassian.net")
	settings.Atlassian.ConfluenceBaseURL = testStringPtr("https://example.atlassian.net/wiki")
	settings.Atlassian.Jira.Enabled = &on
	settings.Atlassian.Confluence.Enabled = &on
	if err := store.Save(settings); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(t.TempDir(), store)
	manager.persistAtlassianProductHealth(map[string]any{
		"products": map[string]any{
			"jira":       map[string]any{"connected": true, "state": "connected"},
			"confluence": map[string]any{"connected": false, "state": "login_required", "error": "Atlassian login is required."},
		},
	}, "checked")

	got := store.Get()
	if got.Atlassian.Jira.LastCheckOk == nil || !*got.Atlassian.Jira.LastCheckOk || got.Atlassian.Jira.LastCheckedAt == nil {
		t.Fatalf("Jira health was not persisted: %#v", got.Atlassian.Jira)
	}
	if got.Atlassian.Confluence.LastCheckOk == nil || *got.Atlassian.Confluence.LastCheckOk || got.Atlassian.Confluence.LastCheckedAt == nil {
		t.Fatalf("Confluence failure health was not persisted: %#v", got.Atlassian.Confluence)
	}

	manager.persistAtlassianSignedOut("Signed out of Atlassian.")
	got = store.Get()
	if got.Atlassian.Jira.Enabled == nil || *got.Atlassian.Jira.Enabled || got.Atlassian.Confluence.Enabled == nil || *got.Atlassian.Confluence.Enabled {
		t.Fatalf("Atlassian tools were not disabled on sign-out: %#v", got.Atlassian)
	}
	if got.Atlassian.Jira.LastCheckOk == nil || *got.Atlassian.Jira.LastCheckOk || got.Atlassian.Confluence.LastCheckOk == nil || *got.Atlassian.Confluence.LastCheckOk {
		t.Fatalf("Atlassian signed-out health was not persisted: %#v", got.Atlassian)
	}
}

func testStringPtr(v string) *string {
	return &v
}

func TestBridgeScriptStartsWhenNodeAndPlaywrightAvailable(t *testing.T) {
	script, ok := sourceBridgeScriptPath()
	if !ok {
		t.Skip("source bridge script is not available")
	}
	node, repoRoot := requireNodeAndPlaywright(t, script)
	startNodeBridgeAndAssertHealth(t, node, script, repoRoot, repoRoot)
}

func TestMaterializedBridgeScriptStartsWhenNodeAndPlaywrightAvailable(t *testing.T) {
	sourceScript, ok := sourceBridgeScriptPath()
	if !ok {
		t.Skip("source bridge script is not available")
	}
	node, repoRoot := requireNodeAndPlaywright(t, sourceScript)
	script, err := materializeEmbeddedBridgeScript(filepath.Join(t.TempDir(), "atlassian", "bridge.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	startNodeBridgeAndAssertHealth(t, node, script, repoRoot, repoRoot)
}

func startNodeBridgeAndAssertHealth(t *testing.T, node string, script string, workDir string, nodeModuleRoot string) {
	t.Helper()
	cmd := exec.Command(node, script, "--state-dir", t.TempDir(), "--port", "0", "--node-module-root", nodeModuleRoot)
	cmd.Dir = workDir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderrBuf bytes.Buffer
	go func() {
		_, _ = io.Copy(&stderrBuf, stderr)
	}()

	if err := cmd.Start(); err != nil {
		t.Fatalf("start bridge: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	readyCh := make(chan bridgeReadyResult, 1)
	go readBridgeReadyLine(stdout, readyCh)

	var baseURL string
	select {
	case ready := <-readyCh:
		if ready.err != nil {
			t.Fatalf("bridge exited before ready event: %v; stderr: %s", ready.err, stderrBuf.String())
		}
		baseURL = ready.url
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for bridge ready event; stderr: %s", stderrBuf.String())
	}
	if strings.TrimSpace(baseURL) == "" {
		t.Fatalf("bridge reported empty URL; stderr: %s", stderrBuf.String())
	}

	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/health")
	if err != nil {
		t.Fatalf("bridge health request failed: %v; stderr: %s", err, stderrBuf.String())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bridge health status = %d, want 200", resp.StatusCode)
	}
}

func requireNodeAndPlaywright(t *testing.T, script string) (string, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping Node bridge smoke test in short mode")
	}
	node, err := bridgeNodeExecutable()
	if err != nil {
		t.Skipf("a usable Node runtime is not installed: %v", err)
	}
	repoRoot := repoRootFromScript(script)
	if _, err := os.Stat(filepath.Join(repoRoot, "node_modules", "playwright", "package.json")); err != nil {
		t.Skip("playwright package is not installed")
	}
	return node, repoRoot
}

func assertEmbeddedBridgeScript(t *testing.T, script string) {
	t.Helper()
	got, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("read materialized bridge script: %v", err)
	}
	if !bytes.Equal(got, embeddedBridgeScript) {
		t.Fatal("materialized bridge script does not match embedded bridge script")
	}
	if !bytes.Contains(got, []byte(`"playwright"`)) {
		t.Fatal("materialized bridge script does not load Playwright")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(script)
		if err != nil {
			t.Fatal(err)
		}
		if gotMode := info.Mode().Perm(); gotMode != 0o600 {
			t.Fatalf("bridge script permissions = %v, want 0600", gotMode)
		}
	}
}

type bridgeReadyResult struct {
	url string
	err error
}

func readBridgeReadyLine(stdout io.Reader, readyCh chan<- bridgeReadyResult) {
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg struct {
			Event string `json:"event"`
			URL   string `json:"url"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err == nil && msg.Event == "ready" {
			readyCh <- bridgeReadyResult{url: msg.URL}
			return
		}
	}
	if err := scanner.Err(); err != nil {
		readyCh <- bridgeReadyResult{err: err}
		return
	}
	readyCh <- bridgeReadyResult{err: errors.New("bridge exited before ready")}
}
