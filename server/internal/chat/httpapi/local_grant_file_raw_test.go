package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/optionc"
	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/providers"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/permissions"
	"ai-analyst-local/internal/secrets"
)

func TestLocalGrantFileRawStreamsAllowedFolderFile(t *testing.T) {
	handler, permissionSvc := newLocalGrantFileRawTestHandler(t)
	root := t.TempDir()
	writeLocalGrantTestFile(t, filepath.Join(root, "notes.txt"), "quarterly planning notes")
	rule := createLocalGrantRule(t, permissionSvc, store.PermissionSubject{
		Kind:      "local_location",
		LocalRoot: root,
		LocalKind: "folder",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/local-files/grants/"+rule.ID+"/file?ref=notes.txt", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "quarterly planning notes" {
		t.Fatalf("body=%q", got)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("Content-Type=%q, want text/plain", rec.Header().Get("Content-Type"))
	}
}

func TestLocalGrantFileRawBlocksSensitivePathUnderFolderGrant(t *testing.T) {
	handler, permissionSvc := newLocalGrantFileRawTestHandler(t)
	root := t.TempDir()
	writeLocalGrantTestFile(t, filepath.Join(root, ".env"), "TOKEN=secret")
	rule := createLocalGrantRule(t, permissionSvc, store.PermissionSubject{
		Kind:      "local_location",
		LocalRoot: root,
		LocalKind: "folder",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/local-files/grants/"+rule.ID+"/file?ref=.env", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403 body=%s", rec.Code, rec.Body.String())
	}
}

func TestLocalGrantFileRawRejectsSymlinkEscape(t *testing.T) {
	handler, permissionSvc := newLocalGrantFileRawTestHandler(t)
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.txt")
	writeLocalGrantTestFile(t, target, "outside")
	if err := os.Symlink(target, filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	rule := createLocalGrantRule(t, permissionSvc, store.PermissionSubject{
		Kind:      "local_location",
		LocalRoot: root,
		LocalKind: "folder",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/local-files/grants/"+rule.ID+"/file?ref=link.txt", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestLocalGrantFileRawExactFileGrantCannotReadSibling(t *testing.T) {
	handler, permissionSvc := newLocalGrantFileRawTestHandler(t)
	root := t.TempDir()
	allowed := filepath.Join(root, "allowed.txt")
	sibling := filepath.Join(root, "sibling.txt")
	writeLocalGrantTestFile(t, allowed, "allowed")
	writeLocalGrantTestFile(t, sibling, "sibling")
	rule := createLocalGrantRule(t, permissionSvc, store.PermissionSubject{
		Kind:      "local_location",
		LocalRoot: allowed,
		LocalKind: "file",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/local-files/grants/"+rule.ID+"/file?ref="+sibling, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/local-files/grants/"+rule.ID+"/file", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "allowed" {
		t.Fatalf("exact file status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestLocalGrantFileRawThreadScopedGrantStreamsByGrantID(t *testing.T) {
	handler, permissionSvc := newLocalGrantFileRawTestHandler(t)
	root := t.TempDir()
	writeLocalGrantTestFile(t, filepath.Join(root, "notes.txt"), "thread notes")
	rule := createLocalGrantRuleWithScope(t, permissionSvc, "thread:thread-1", store.PermissionSubject{
		Kind:      "local_location",
		LocalRoot: root,
		LocalKind: "folder",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/local-files/grants/"+rule.ID+"/file?ref=notes.txt", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unscoped thread grant status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/local-files/grants/"+rule.ID+"/file?ref=notes.txt&thread_id=thread-1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "thread notes" {
		t.Fatalf("matching thread status=%d body=%q", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/local-files/grants/"+rule.ID+"/file?ref=notes.txt&thread_id=thread-2", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("mismatched thread status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
}

func TestLocalSearchJobsHTTPContracts(t *testing.T) {
	handler, _ := newLocalGrantFileRawTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/local-files/search-jobs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unscoped list status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/local-files/search-jobs?thread_id=thread-1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scoped list status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"jobs":[]`) {
		t.Fatalf("scoped list body=%s, want empty jobs array", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/local-files/search-jobs/local_search_missing", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unscoped missing job status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/local-files/search-jobs/local_search_missing?thread_id=thread-1", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing job status=%d want 404 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("missing job Content-Type=%q, want JSON", rec.Header().Get("Content-Type"))
	}
}

func TestLocalComputerRootsHTTPContract(t *testing.T) {
	handler, _ := newLocalGrantFileRawTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/local-files/roots", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		OK          bool     `json:"ok"`
		OS          string   `json:"os"`
		PrimaryRoot string   `json:"primary_root"`
		Roots       []string `json:"roots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode roots response: %v", err)
	}
	if !payload.OK || payload.OS == "" || len(payload.Roots) == 0 || payload.PrimaryRoot != payload.Roots[0] {
		t.Fatalf("roots payload=%#v, want non-empty platform roots with primary first", payload)
	}
	for _, root := range payload.Roots {
		if !filepath.IsAbs(root) || filepath.Dir(root) != root {
			t.Fatalf("root=%q is not an absolute filesystem root", root)
		}
	}
}

func TestLocalComputerWholeSourceIsUnavailableInSelectedFolderMode(t *testing.T) {
	cfg, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := cfg.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:         &enabled,
		AccessMode:      &mode,
		SelectedFolders: []string{t.TempDir()},
	}
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("Save settings: %v", err)
	}
	handler := &Handler{settings: cfg}
	if root, ok := handler.localComputerSourceRoot("local-whole-computer"); ok {
		t.Fatalf("stale whole-computer source resolved to %q in selected-folder mode", root)
	}
}

func newLocalGrantFileRawTestHandler(t *testing.T) (http.Handler, *permissions.Service) {
	t.Helper()
	cfgDir := t.TempDir()
	cfg, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	sec, err := secrets.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	manager := optionc.NewManager(
		st,
		cfg,
		providers.NewFactory(cfg, sec),
		tools.NewRegistry(nil),
		ps,
		nil,
		nil,
		nil,
		cfgDir,
		&optionc.Options{Permissions: permissionSvc},
	)
	handler := New(Deps{
		Store:       st,
		Manager:     manager,
		Projects:    ps,
		Settings:    cfg,
		Permissions: permissionSvc,
		CfgDir:      cfgDir,
	})
	return handler, permissionSvc
}

func createLocalGrantRule(t *testing.T, svc *permissions.Service, subject store.PermissionSubject) permissions.PermissionRule {
	t.Helper()
	return createLocalGrantRuleWithScope(t, svc, "global", subject)
}

func createLocalGrantRuleWithScope(t *testing.T, svc *permissions.Service, scope string, subject store.PermissionSubject) permissions.PermissionRule {
	t.Helper()
	rule, err := svc.CreateRule(context.Background(), permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectAllow,
		Scope:    scope,
		Subject:  subject,
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	return rule
}

func writeLocalGrantTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
