package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-analyst-local/internal/activation"
	chatstore "ai-analyst-local/internal/chat/store"
	chattools "ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/databricks"
	"ai-analyst-local/internal/permissions"
	"ai-analyst-local/internal/proxy"
	"ai-analyst-local/internal/secrets"
)

func TestProviderConnectionTestUsesDraftConfig(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	if err := opts.Secrets.Init("test-passphrase"); err != nil {
		t.Fatalf("secrets init: %v", err)
	}
	if err := opts.Secrets.Unlock("test-passphrase"); err != nil {
		t.Fatalf("secrets unlock: %v", err)
	}
	if err := opts.Secrets.SetCompatibleAPIKey("cfg-compatible-test", "compatible-key"); err != nil {
		t.Fatalf("SetCompatibleAPIKey: %v", err)
	}

	var gotPath string
	var gotAuth string
	var gotContent string
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		if messages, ok := body["messages"].([]any); ok && len(messages) > 0 {
			if first, ok := messages[0].(map[string]any); ok {
				gotContent, _ = first["content"].(string)
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-test\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer providerServer.Close()

	h := NewHandler(opts)
	rec := doJSON(t, h, http.MethodPost, "/api/providers/test", map[string]any{
		"endpoint": providerServer.URL + "/chat/completions",
		"config": map[string]any{
			"id":          "cfg-compatible-test",
			"displayName": "Compatible Test",
			"providerId":  "compatible",
			"model":       "test-model",
			"apiType":     "chat_completions",
			"baseUrl":     providerServer.URL,
			"authType":    "api_key",
			"authHeader":  "Authorization",
			"authScheme":  "Bearer",
			"modelInBody": true,
			"modelInPath": false,
		},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	if payload["ok"] != true {
		t.Fatalf("ok = %#v, want true (payload=%#v)", payload["ok"], payload)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("provider path = %q, want /chat/completions", gotPath)
	}
	if gotAuth != "Bearer compatible-key" {
		t.Fatalf("authorization = %q, want Bearer compatible-key", gotAuth)
	}
	if gotContent != "hi" {
		t.Fatalf("message content = %q, want hi", gotContent)
	}
}

func TestSettingsAcceptConnectionVerifiedFingerprints(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	h := NewHandler(opts)

	raw, err := json.Marshal(opts.Settings.Get())
	if err != nil {
		t.Fatalf("marshal defaults: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("unmarshal defaults: %v", err)
	}

	body["github"] = map[string]any{
		"enabled":                 true,
		"readTools":               true,
		"lastVerifiedFingerprint": " github/v1:clientId=abc ",
	}
	body["slack"] = map[string]any{
		"enabled":                 true,
		"readTools":               true,
		"lastVerifiedFingerprint": "slack/v1:clientId=abc",
	}
	body["sharepoint"] = map[string]any{
		"siteUrl":                 "https://example.sharepoint.com",
		"enabled":                 true,
		"lastVerifiedFingerprint": "sharepoint/v1:siteUrl=x",
	}
	body["localComputer"] = map[string]any{
		"enabled":          true,
		"accessMode":       "selected-folders",
		"selectedFolders":  []string{" /Users/example/Documents ", "/Users/example/Documents", " ", "/tmp"},
		"lastCheckMessage": " ready ",
	}
	body["atlassian"] = map[string]any{
		"jiraBaseUrl":             "https://example.atlassian.net",
		"confluenceBaseUrl":       "https://example.atlassian.net/wiki",
		"lastVerifiedFingerprint": "atlassian/v1:jiraBaseUrl=x",
	}
	body["dataSources"] = map[string]any{
		"bigquery": map[string]any{
			"enabled":                 true,
			"lastVerifiedFingerprint": "bigquery/v1:projectId=proj",
		},
		"databricks": map[string]any{
			"enabled":                 true,
			"lastVerifiedFingerprint": "databricks/v1:warehouseId=wh",
		},
		"trino": map[string]any{
			"enabled":                 true,
			"lastVerifiedFingerprint": "trino/v1:host=trino",
		},
		"snowflake": map[string]any{
			"enabled":                 true,
			"lastVerifiedFingerprint": "snowflake/v1:account=acct",
		},
	}

	rec := doJSON(t, h, http.MethodPut, "/api/settings", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("PUT /api/settings Cache-Control = %q, want no-store", got)
	}

	got := opts.Settings.Get()
	if got.GitHub == nil || got.GitHub.LastVerifiedFingerprint == nil || *got.GitHub.LastVerifiedFingerprint != "github/v1:clientId=abc" {
		t.Fatalf("GitHub.LastVerifiedFingerprint = %#v, want trimmed fingerprint", got.GitHub)
	}
	if got.Slack == nil || got.Slack.LastVerifiedFingerprint == nil || *got.Slack.LastVerifiedFingerprint != "slack/v1:clientId=abc" {
		t.Fatalf("Slack.LastVerifiedFingerprint = %#v, want fingerprint", got.Slack)
	}
	if got.SharePoint == nil || got.SharePoint.LastVerifiedFingerprint == nil || *got.SharePoint.LastVerifiedFingerprint != "sharepoint/v1:siteUrl=x" {
		t.Fatalf("SharePoint.LastVerifiedFingerprint = %#v, want fingerprint", got.SharePoint)
	}
	if got.LocalComputer == nil || got.LocalComputer.Enabled == nil || !*got.LocalComputer.Enabled {
		t.Fatalf("LocalComputer.Enabled = %#v, want true", got.LocalComputer)
	}
	if got.LocalComputer.AccessMode == nil || *got.LocalComputer.AccessMode != config.LocalComputerAccessModeSelectedFolders {
		t.Fatalf("LocalComputer.AccessMode = %#v, want selected-folders", got.LocalComputer.AccessMode)
	}
	wantFolders := []string{"/Users/example/Documents", "/tmp"}
	if len(got.LocalComputer.SelectedFolders) != len(wantFolders) {
		t.Fatalf("LocalComputer.SelectedFolders = %#v, want %#v", got.LocalComputer.SelectedFolders, wantFolders)
	}
	for i := range wantFolders {
		if got.LocalComputer.SelectedFolders[i] != wantFolders[i] {
			t.Fatalf("LocalComputer.SelectedFolders = %#v, want %#v", got.LocalComputer.SelectedFolders, wantFolders)
		}
	}
	if got.LocalComputer.LastCheckMessage == nil || *got.LocalComputer.LastCheckMessage != "ready" {
		t.Fatalf("LocalComputer.LastCheckMessage = %#v, want ready", got.LocalComputer.LastCheckMessage)
	}
	if got.Atlassian == nil || got.Atlassian.LastVerifiedFingerprint == nil || *got.Atlassian.LastVerifiedFingerprint != "atlassian/v1:jiraBaseUrl=x" {
		t.Fatalf("Atlassian.LastVerifiedFingerprint = %#v, want fingerprint", got.Atlassian)
	}
	if got.DataSources == nil || got.DataSources.Databricks == nil || got.DataSources.Databricks.LastVerifiedFingerprint == nil || *got.DataSources.Databricks.LastVerifiedFingerprint != "databricks/v1:warehouseId=wh" {
		t.Fatalf("Databricks.LastVerifiedFingerprint = %#v, want fingerprint", got.DataSources)
	}
}

func TestSettingsGETCannotBeReplayedFromAStaleDesktopCache(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	h := NewHandler(opts)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("GET /api/settings Cache-Control = %q, want no-store", got)
	}
}

func TestLocalLocationPermissionTestRouteValidatesPickedFolder(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	cfgDir := t.TempDir()
	st, err := chatstore.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	opts.Permissions = permissionSvc
	root := t.TempDir()

	h := NewHandler(opts)
	rec := doJSON(t, h, http.MethodPost, "/api/permissions/local-locations/test", map[string]any{
		"subject": map[string]any{
			"kind":       "local_location",
			"local_root": root,
		},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	if payload["ok"] != true {
		t.Fatalf("ok = %#v, want true (payload=%#v)", payload["ok"], payload)
	}
	result, ok := payload["result"].(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want object", payload["result"])
	}
	if result["readable"] != true || result["kind"] != "folder" {
		t.Fatalf("result = %#v, want readable folder", result)
	}
	subject, ok := result["subject"].(map[string]any)
	if !ok || subject["kind"] != "local_location" || subject["local_root"] == "" {
		t.Fatalf("subject = %#v, want canonical local location", result["subject"])
	}
}

func TestProviderConnectionTestReturnsProviderError(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	if err := opts.Secrets.Init("test-passphrase"); err != nil {
		t.Fatalf("secrets init: %v", err)
	}
	if err := opts.Secrets.Unlock("test-passphrase"); err != nil {
		t.Fatalf("secrets unlock: %v", err)
	}
	if err := opts.Secrets.SetCompatibleAPIKey("cfg-compatible-test", "compatible-key"); err != nil {
		t.Fatalf("SetCompatibleAPIKey: %v", err)
	}

	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad model", http.StatusNotFound)
	}))
	defer providerServer.Close()

	h := NewHandler(opts)
	rec := doJSON(t, h, http.MethodPost, "/api/providers/test", map[string]any{
		"config": map[string]any{
			"id":          "cfg-compatible-test",
			"displayName": "Compatible Test",
			"providerId":  "compatible",
			"model":       "missing-model",
			"apiType":     "chat_completions",
			"baseUrl":     providerServer.URL,
			"authType":    "api_key",
			"authHeader":  "Authorization",
			"authScheme":  "Bearer",
			"modelInBody": true,
			"modelInPath": false,
		},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	if payload["ok"] != false {
		t.Fatalf("ok = %#v, want false (payload=%#v)", payload["ok"], payload)
	}
	if msg, _ := payload["message"].(string); !strings.Contains(strings.ToLower(msg), "bad model") && !strings.Contains(strings.ToLower(msg), "404") {
		t.Fatalf("message = %q, want provider error", msg)
	}
}

func TestProviderStreamErrorMessageFormatting(t *testing.T) {
	tests := []struct {
		name string
		data any
		want string
	}{
		{
			name: "nested JSON string",
			data: `{"error":{"type":"invalid_request_error","code":"DeploymentNotFound","message":"The API deployment for this resource does not exist."}}`,
			want: "DeploymentNotFound: The API deployment for this resource does not exist.",
		},
		{
			name: "nested map",
			data: map[string]any{"error": map[string]any{
				"type":    "invalid_request_error",
				"code":    "DeploymentNotFound",
				"message": "The API deployment for this resource does not exist.",
			}},
			want: "DeploymentNotFound: The API deployment for this resource does not exist.",
		},
		{
			name: "top level error and message",
			data: map[string]any{"error": "invalid_request", "message": "Bad request."},
			want: "invalid_request: Bad request.",
		},
		{
			name: "plain text",
			data: "bad model",
			want: "bad model",
		},
		{
			name: "malformed JSON passthrough",
			data: `{"error":`,
			want: `{"error":`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := providerStreamErrorMessage(tt.data); got != tt.want {
				t.Fatalf("providerStreamErrorMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProviderConnectionTestFormatsProviderJSONError(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	if err := opts.Secrets.Init("test-passphrase"); err != nil {
		t.Fatalf("secrets init: %v", err)
	}
	if err := opts.Secrets.Unlock("test-passphrase"); err != nil {
		t.Fatalf("secrets unlock: %v", err)
	}
	if err := opts.Secrets.SetCompatibleAPIKey("cfg-compatible-test", "compatible-key"); err != nil {
		t.Fatalf("SetCompatibleAPIKey: %v", err)
	}

	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"DeploymentNotFound","message":"The API deployment for this resource does not exist. If you created the deployment within the last 5 minutes, please wait a moment and try again."}}`))
	}))
	defer providerServer.Close()

	h := NewHandler(opts)
	rec := doJSON(t, h, http.MethodPost, "/api/providers/test", map[string]any{
		"config": map[string]any{
			"id":          "cfg-compatible-test",
			"displayName": "Compatible Test",
			"providerId":  "compatible",
			"model":       "missing-model",
			"apiType":     "chat_completions",
			"baseUrl":     providerServer.URL,
			"authType":    "api_key",
			"authHeader":  "Authorization",
			"authScheme":  "Bearer",
			"modelInBody": true,
			"modelInPath": false,
		},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	if payload["ok"] != false {
		t.Fatalf("ok = %#v, want false (payload=%#v)", payload["ok"], payload)
	}
	msg, _ := payload["message"].(string)
	if !strings.Contains(msg, "DeploymentNotFound: The API deployment for this resource does not exist.") {
		t.Fatalf("message = %q, want formatted provider JSON error", msg)
	}
	if strings.Contains(msg, `{"error"`) {
		t.Fatalf("message = %q, should not include raw provider JSON", msg)
	}
}

func TestSnowflakeRouteMatchesEffectiveFeatureFlag(t *testing.T) {
	opts, hitCounter := newTestHandlerOpts(t)
	opts.Snowflake = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hitCounter = *hitCounter + 1
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	settings := opts.Settings.Get()
	if settings.FeatureFlags == nil {
		t.Fatalf("feature flags missing in test settings")
	}
	flagEnabled := settings.FeatureFlags.CloudCredentialsSnowflake

	h := NewHandler(opts)
	req := httptest.NewRequest(http.MethodGet, "/api/snowflake/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if flagEnabled {
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 when cloudCredentialsSnowflake=true", rec.Code)
		}
		if *hitCounter != 1 {
			t.Fatalf("snowflake handler should be invoked when feature flag enabled")
		}
		return
	}

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 when cloudCredentialsSnowflake=false", rec.Code)
	}
	if *hitCounter != 0 {
		t.Fatalf("snowflake handler was invoked with feature flag disabled")
	}
}

func TestQueriesRouteIsForwardedToChatHandler(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	var hitPath string
	opts.Chat = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	h := NewHandler(opts)
	req := httptest.NewRequest(http.MethodGet, "/api/queries/job-123/status?thread_id=thread-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if hitPath != "/api/queries/job-123/status" {
		t.Fatalf("chat handler path = %q, want %q", hitPath, "/api/queries/job-123/status")
	}
}

func TestFilesRouteIsForwardedToChatHandler(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	var hitPath string
	opts.Chat = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	h := NewHandler(opts)
	req := httptest.NewRequest(http.MethodGet, "/api/files?scope=all", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if hitPath != "/api/files" {
		t.Fatalf("chat handler path = %q, want %q", hitPath, "/api/files")
	}
}

func TestToolTestRouteIsForwardedToChatHandler(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	var hitPath string
	opts.Chat = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	h := NewHandler(opts)
	req := httptest.NewRequest(http.MethodPost, "/api/tools/list_files/test", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if hitPath != "/api/tools/list_files/test" {
		t.Fatalf("chat handler path = %q, want %q", hitPath, "/api/tools/list_files/test")
	}
}

func TestToolTestBodyRouteIsForwardedToChatHandler(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	var hitPath string
	opts.Chat = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	h := NewHandler(opts)
	req := httptest.NewRequest(http.MethodPost, "/api/tools/test", bytes.NewReader([]byte(`{"tool_name":"openai-responses/web_search"}`)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if hitPath != "/api/tools/test" {
		t.Fatalf("chat handler path = %q, want %q", hitPath, "/api/tools/test")
	}
}

func TestToolInventoryRouteReturnsCanonicalMetadata(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	h := NewHandler(opts)

	req := httptest.NewRequest(http.MethodGet, "/api/tools/inventory", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var payload struct {
		Tools               []chattools.ToolMetadata                 `json:"tools"`
		ProviderHostedTools []chattools.ProviderHostedToolCapability `json:"provider_hosted_tools"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v payload=%q", err, rec.Body.String())
	}

	want := chattools.AllToolMetadata()
	if len(payload.Tools) != len(want) {
		t.Fatalf("tools count = %d, want %d", len(payload.Tools), len(want))
	}
	if len(payload.ProviderHostedTools) != len(chattools.ProviderHostedToolCapabilities()) {
		t.Fatalf("provider_hosted_tools count = %d, want %d", len(payload.ProviderHostedTools), len(chattools.ProviderHostedToolCapabilities()))
	}
	wantByName := map[string]chattools.ToolMetadata{}
	for _, meta := range want {
		wantByName[meta.Name] = meta
	}

	seenOrigin := map[chattools.ToolOrigin]bool{}
	seenCategory := map[chattools.ToolPermissionCategory]bool{}
	for _, meta := range payload.Tools {
		if strings.TrimSpace(meta.Name) == "" {
			t.Fatal("tool inventory includes empty name")
		}
		if strings.TrimSpace(meta.Label) == "" {
			t.Fatalf("%s label is empty", meta.Name)
		}
		if strings.TrimSpace(meta.Description) == "" {
			t.Fatalf("%s description is empty", meta.Name)
		}
		if strings.TrimSpace(meta.ToolSetID) == "" || strings.TrimSpace(meta.ToolSetName) == "" {
			t.Fatalf("%s tool set metadata is incomplete: %#v", meta.Name, meta)
		}
		if meta.Test.Mode == "" {
			t.Fatalf("%s test mode is empty", meta.Name)
		}

		canonical, ok := wantByName[meta.Name]
		if !ok {
			t.Fatalf("endpoint returned non-canonical tool metadata for %q", meta.Name)
		}
		if !sameCanonicalJSON(canonical, meta) {
			t.Fatalf("%s metadata = %#v, want canonical %#v", meta.Name, meta, canonical)
		}

		seenOrigin[meta.Origin] = true
		seenCategory[meta.Category] = true
	}

	for _, origin := range []chattools.ToolOrigin{
		chattools.ToolOriginBuiltin,
		chattools.ToolOriginConnected,
		chattools.ToolOriginLocal,
		chattools.ToolOriginAttached,
	} {
		if !seenOrigin[origin] {
			t.Fatalf("tool inventory missing origin %q", origin)
		}
	}
	for _, category := range []chattools.ToolPermissionCategory{
		chattools.ToolCategoryWrite,
		chattools.ToolCategoryExecute,
	} {
		if !seenCategory[category] {
			t.Fatalf("tool inventory missing category %q", category)
		}
	}
}

func sameCanonicalJSON(a any, b any) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(left, right)
}

func TestToolInventoryRouteJSONContract(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	h := NewHandler(opts)

	req := httptest.NewRequest(http.MethodGet, "/api/tools/inventory", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v payload=%q", err, rec.Body.String())
	}
	rawTools, ok := payload["tools"].([]any)
	if !ok || len(rawTools) == 0 {
		t.Fatalf("tools = %#v, want non-empty array", payload["tools"])
	}
	rawHostedTools, ok := payload["provider_hosted_tools"].([]any)
	if !ok || len(rawHostedTools) == 0 {
		t.Fatalf("provider_hosted_tools = %#v, want non-empty array", payload["provider_hosted_tools"])
	}

	var cloneTool map[string]any
	var listRepositoriesTool map[string]any
	for _, raw := range rawTools {
		tool, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("tool has type %T, want object", raw)
		}
		if tool["Name"] != nil || tool["ToolSetID"] != nil || tool["ConnectorID"] != nil {
			t.Fatalf("tool uses Go field names instead of JSON contract: %#v", tool)
		}
		if tool["name"] == chattools.GitHubToolCloneRepository {
			cloneTool = tool
		}
		if tool["name"] == chattools.GitHubToolListRepositories {
			listRepositoriesTool = tool
		}
	}
	if cloneTool == nil {
		t.Fatalf("inventory did not include %s", chattools.GitHubToolCloneRepository)
	}
	if listRepositoriesTool == nil {
		t.Fatalf("inventory did not include %s", chattools.GitHubToolListRepositories)
	}
	var openAIHostedSearch map[string]any
	for _, raw := range rawHostedTools {
		hosted, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("provider hosted tool has type %T, want object", raw)
		}
		if hosted["ID"] != nil || hosted["AdapterFamily"] != nil || hosted["ProviderToolNames"] != nil {
			t.Fatalf("provider hosted tool uses Go field names instead of JSON contract: %#v", hosted)
		}
		if hosted["id"] == "openai-responses/web_search" {
			openAIHostedSearch = hosted
		}
	}
	if openAIHostedSearch == nil {
		t.Fatalf("inventory did not include provider-hosted openai-responses/web_search")
	}
	for _, key := range []string{"id", "name", "label", "description", "category", "adapter_family", "settings_key", "provider_tool_names", "provider_executed"} {
		if _, ok := openAIHostedSearch[key]; !ok {
			t.Fatalf("provider hosted web search missing %s: %#v", key, openAIHostedSearch)
		}
	}
	if openAIHostedSearch["name"] != "web_search" || openAIHostedSearch["adapter_family"] != "openai-responses" || openAIHostedSearch["settings_key"] != "webSearch" {
		t.Fatalf("provider hosted web search contract = %#v", openAIHostedSearch)
	}

	for _, key := range []string{"name", "label", "description", "category", "origin", "tool_set_id", "tool_set_name", "connector_id", "presentation", "test"} {
		if _, ok := cloneTool[key]; !ok {
			t.Fatalf("%s missing from %s contract: %#v", key, chattools.GitHubToolCloneRepository, cloneTool)
		}
	}
	if cloneTool["category"] != string(chattools.ToolCategoryExecute) {
		t.Fatalf("category = %#v, want execute", cloneTool["category"])
	}
	if cloneTool["origin"] != string(chattools.ToolOriginConnected) {
		t.Fatalf("origin = %#v, want connected", cloneTool["origin"])
	}
	if cloneTool["tool_set_id"] != "github" || cloneTool["connector_id"] != "github" {
		t.Fatalf("github provenance missing: %#v", cloneTool)
	}
	presentation, ok := cloneTool["presentation"].(map[string]any)
	if !ok {
		t.Fatalf("presentation has type %T, want object", cloneTool["presentation"])
	}
	for _, key := range []string{"family", "result_kind", "renderer_strategy", "renderer_key", "native_viewer", "file_capability", "source_refs", "native_links"} {
		if _, ok := presentation[key]; !ok {
			t.Fatalf("presentation missing %s: %#v", key, presentation)
		}
	}
	if presentation["family"] != "github" ||
		presentation["result_kind"] != "repository" ||
		presentation["renderer_strategy"] != "family" ||
		presentation["renderer_key"] != "github" ||
		presentation["native_viewer"] != "connector_inspector" ||
		presentation["file_capability"] != "repository_clone" {
		t.Fatalf("clone presentation = %#v, want GitHub repository clone contract", presentation)
	}
	if !jsonArrayContainsString(presentation["source_refs"], "remote_id") || !jsonArrayContainsString(presentation["source_refs"], "local_path") {
		t.Fatalf("clone source_refs = %#v, want remote_id and local_path", presentation["source_refs"])
	}
	if !jsonArrayContainsString(presentation["native_links"], "source_url") {
		t.Fatalf("clone native_links = %#v, want source_url", presentation["native_links"])
	}
	testSpec, ok := cloneTool["test"].(map[string]any)
	if !ok {
		t.Fatalf("test has type %T, want object", cloneTool["test"])
	}
	if testSpec["supported"] != true || testSpec["mode"] != string(chattools.ToolTestModeDryRun) || testSpec["requires_approval"] != true {
		t.Fatalf("test spec = %#v, want supported dry_run requiring approval", testSpec)
	}
	if testSpec["input_required"] != true {
		t.Fatalf("input_required = %#v, want true", testSpec["input_required"])
	}
	if !jsonArrayContainsString(testSpec["required_arguments"], "repository") {
		t.Fatalf("required_arguments = %#v, want repository", testSpec["required_arguments"])
	}
	if !jsonArrayContainsString(testSpec["required_context"], "connection:github") || jsonArrayContainsString(testSpec["required_context"], "project") {
		t.Fatalf("required_context = %#v, want connection:github without project for clone", testSpec["required_context"])
	}
	if strings.TrimSpace(fmt.Sprint(testSpec["input_hint"])) == "" {
		t.Fatalf("input_hint missing: %#v", testSpec)
	}

	listTestSpec, ok := listRepositoriesTool["test"].(map[string]any)
	if !ok {
		t.Fatalf("list repository test has type %T, want object", listRepositoriesTool["test"])
	}
	if !jsonArrayContainsString(listTestSpec["required_context"], "connection:github") || jsonArrayContainsString(listTestSpec["required_context"], "project") {
		t.Fatalf("list repository required_context = %#v, want connection:github without project", listTestSpec["required_context"])
	}
}

func TestToolInventoryRouteIsReadOnly(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	h := NewHandler(opts)

	req := httptest.NewRequest(http.MethodPost, "/api/tools/inventory", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", rec.Code)
	}
}

func TestExternalAPIRouteIsForwardedToPublicAPIHandler(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	var publicPath string
	var chatHit bool
	opts.Chat = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chatHit = true
		writeJSON(w, http.StatusTeapot, map[string]any{"ok": false})
	})
	opts.PublicAPI = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicPath = r.URL.Path
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
	})

	h := NewHandler(opts)
	req := httptest.NewRequest(http.MethodPost, "/api/external/v1/runs", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body=%s)", rec.Code, rec.Body.String())
	}
	if chatHit {
		t.Fatalf("chat handler should not receive public API route")
	}
	if publicPath != "/api/external/v1/runs" {
		t.Fatalf("public API handler path = %q, want %q", publicPath, "/api/external/v1/runs")
	}
}

func TestDesktopTokenGuard(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := desktopTokenGuard(next, "token-123")

	t.Run("requires token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
		payload := decodeMap(t, rec.Body.Bytes())
		if payload["code"] != "DESKTOP_TOKEN_REQUIRED" {
			t.Fatalf("code = %#v, want DESKTOP_TOKEN_REQUIRED", payload["code"])
		}
	})

	t.Run("allows matching token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Header.Set(desktopTokenHeader, "token-123")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", rec.Code)
		}
	})

	t.Run("allows external oauth callback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/databricks/oauth/callback?state=s&code=c", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", rec.Code)
		}
	})

	t.Run("allows databricks root completion", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/databricks/oauth/complete", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", rec.Code)
		}
	})
}

func TestDatabricksRootOAuthRedirectBypassesDesktopUI(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	opts.DesktopToken = "token-123"
	opts.Databricks = databricks.NewHandler(opts.Settings, opts.Secrets)

	var staticHit bool
	opts.Static = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		staticHit = true
		w.WriteHeader(http.StatusTeapot)
	})

	h := NewHandler(opts)
	req := httptest.NewRequest(http.MethodGet, "/?state=missing-state&code=oauth-code", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if staticHit {
		t.Fatalf("static UI should not be served for Databricks root OAuth redirects")
	}
	body := rec.Body.String()
	if strings.Contains(body, "desktop authorization token required") {
		t.Fatalf("root OAuth callback should not render desktop token error: %s", body)
	}
	if !strings.Contains(body, "Databricks Authorization Failed") {
		t.Fatalf("body should be Databricks callback page, got: %s", body)
	}
}

func TestValidateExternalURL(t *testing.T) {
	t.Run("allows https", func(t *testing.T) {
		got, err := validateExternalURL(" https://slack.com/oauth/v2/authorize?client_id=abc ")
		if err != nil {
			t.Fatalf("validateExternalURL returned error: %v", err)
		}
		if got != "https://slack.com/oauth/v2/authorize?client_id=abc" {
			t.Fatalf("url = %q, want normalized Slack URL", got)
		}
	})

	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: "url is required"},
		{name: "relative", raw: "/oauth/start", want: "url must be absolute"},
		{name: "http", raw: "http://localhost:8787/callback", want: "only https urls can be opened externally"},
		{name: "credentials", raw: "https://user:pass@example.com", want: "url credentials are not allowed"},
		{name: "control", raw: "https://example.com/\nnext", want: "url contains invalid control characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validateExternalURL(tc.raw)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestSecretsSnowflakeSetAndClearValidation(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	if err := opts.Secrets.Init("passphrase"); err != nil {
		t.Fatalf("Init secrets: %v", err)
	}
	if err := opts.Secrets.Unlock("passphrase"); err != nil {
		t.Fatalf("Unlock secrets: %v", err)
	}
	h := NewHandler(opts)

	t.Run("set requires authType", func(t *testing.T) {
		body := map[string]any{
			"providerId":       "snowflake",
			"oauthAccessToken": "token",
		}
		res := doJSON(t, h, http.MethodPost, "/api/secrets/set", body)
		assertHTTPError(t, res, http.StatusBadRequest, "snowflake authType is required (oauth_token or keypair_jwt)")
	})

	t.Run("oauth_token requires token", func(t *testing.T) {
		body := map[string]any{
			"providerId": "snowflake",
			"authType":   "oauth_token",
		}
		res := doJSON(t, h, http.MethodPost, "/api/secrets/set", body)
		assertHTTPError(t, res, http.StatusBadRequest, "oauthAccessToken is required when authType=oauth_token")
	})

	t.Run("keypair_jwt requires privateKeyPem", func(t *testing.T) {
		body := map[string]any{
			"providerId": "snowflake",
			"authType":   "keypair_jwt",
		}
		res := doJSON(t, h, http.MethodPost, "/api/secrets/set", body)
		assertHTTPError(t, res, http.StatusBadRequest, "privateKeyPem is required when authType=keypair_jwt")
	})

	t.Run("oauth_token rejects keypair fields", func(t *testing.T) {
		body := map[string]any{
			"providerId":       "snowflake",
			"authType":         "oauth_token",
			"oauthAccessToken": "token",
			"privateKeyPem":    "pem",
		}
		res := doJSON(t, h, http.MethodPost, "/api/secrets/set", body)
		assertHTTPError(t, res, http.StatusBadRequest, "privateKeyPem/privateKeyPassphrase are not allowed when authType=oauth_token")
	})

	t.Run("keypair_jwt rejects oauth token", func(t *testing.T) {
		body := map[string]any{
			"providerId":       "snowflake",
			"authType":         "keypair_jwt",
			"privateKeyPem":    "-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----",
			"oauthAccessToken": "token",
		}
		res := doJSON(t, h, http.MethodPost, "/api/secrets/set", body)
		assertHTTPError(t, res, http.StatusBadRequest, "oauthAccessToken is not allowed when authType=keypair_jwt")
	})

	t.Run("set oauth_token updates configured booleans", func(t *testing.T) {
		body := map[string]any{
			"providerId":       "snowflake",
			"authType":         "oauth_token",
			"oauthAccessToken": "token-123",
		}
		res := doJSON(t, h, http.MethodPost, "/api/secrets/set", body)
		if status := res.Code; status != http.StatusOK {
			t.Fatalf("POST /api/secrets/set status = %d, want 200", status)
		}

		statusRes := doJSON(t, h, http.MethodGet, "/api/secrets/status", nil)
		if statusRes.Code != http.StatusOK {
			t.Fatalf("GET /api/secrets/status status = %d, want 200", statusRes.Code)
		}
		payload := decodeMap(t, statusRes.Body.Bytes())
		configured := payload["configured"].(map[string]any)
		if configured["snowflake"] != true {
			t.Fatalf("configured.snowflake = %#v, want true", configured["snowflake"])
		}
		if configured["snowflakeOAuthToken"] != true {
			t.Fatalf("configured.snowflakeOAuthToken = %#v, want true", configured["snowflakeOAuthToken"])
		}
		if configured["snowflakeKeyPair"] != false {
			t.Fatalf("configured.snowflakeKeyPair = %#v, want false", configured["snowflakeKeyPair"])
		}
	})

	t.Run("clear rejects unsupported authType", func(t *testing.T) {
		body := map[string]any{
			"providerId": "snowflake",
			"authType":   "oauth_token",
		}
		res := doJSON(t, h, http.MethodPost, "/api/secrets/clear", body)
		assertHTTPError(t, res, http.StatusBadRequest, "snowflake authType must be all when clearing")
	})

	t.Run("clear all removes snowflake config", func(t *testing.T) {
		body := map[string]any{
			"providerId": "snowflake",
			"authType":   "all",
		}
		res := doJSON(t, h, http.MethodPost, "/api/secrets/clear", body)
		if status := res.Code; status != http.StatusOK {
			t.Fatalf("POST /api/secrets/clear status = %d, want 200", status)
		}

		statusRes := doJSON(t, h, http.MethodGet, "/api/secrets/status", nil)
		if statusRes.Code != http.StatusOK {
			t.Fatalf("GET /api/secrets/status status = %d, want 200", statusRes.Code)
		}
		payload := decodeMap(t, statusRes.Body.Bytes())
		configured := payload["configured"].(map[string]any)
		if configured["snowflake"] != false {
			t.Fatalf("configured.snowflake = %#v, want false", configured["snowflake"])
		}
		if configured["snowflakeOAuthToken"] != false {
			t.Fatalf("configured.snowflakeOAuthToken = %#v, want false", configured["snowflakeOAuthToken"])
		}
		if configured["snowflakeKeyPair"] != false {
			t.Fatalf("configured.snowflakeKeyPair = %#v, want false", configured["snowflakeKeyPair"])
		}
	})
}

func newTestHandlerOpts(t *testing.T) (HandlerOpts, *int) {
	t.Helper()
	cfgDir := t.TempDir()

	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	secretsStore, err := secrets.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	activationStore, err := activation.NewStore(cfgDir, activation.Options{DevBypass: true})
	if err != nil {
		t.Fatalf("activation.NewStore: %v", err)
	}
	proxyHandler := proxy.NewHandler(settingsStore, secretsStore)

	hits := 0
	opts := HandlerOpts{
		Dev:        true,
		Settings:   settingsStore,
		Secrets:    secretsStore,
		Proxy:      proxyHandler,
		Activation: activationStore,
	}
	return opts, &hits
}

func doJSON(t *testing.T, h http.Handler, method string, path string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody []byte
	if body != nil {
		var err error
		reqBody, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("json.Marshal: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(reqBody))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeMap(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("json.Unmarshal: %v payload=%q", err, string(payload))
	}
	return out
}

func jsonArrayContainsString(raw any, want string) bool {
	values, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func assertHTTPError(t *testing.T, rec *httptest.ResponseRecorder, status int, errMsg string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body=%s)", rec.Code, status, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	if got := payload["error"]; got != errMsg {
		t.Fatalf("error = %#v, want %#v", got, errMsg)
	}
}
