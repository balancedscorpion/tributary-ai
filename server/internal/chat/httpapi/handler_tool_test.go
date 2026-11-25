package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/arxiv"
	"ai-analyst-local/internal/atlassian"
	"ai-analyst-local/internal/browsercontrol"
	"ai-analyst-local/internal/chat/optionc"
	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/providers"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/permissions"
	"ai-analyst-local/internal/secrets"
	"ai-analyst-local/internal/sharepoint"
	"ai-analyst-local/internal/slack"
)

func TestHandleTestToolExecutesRegisteredToolTest(t *testing.T) {
	ctx := context.Background()
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
	projectID := "project-tool-http-test"
	if err := ps.PutFile(projectID, "README.md", []byte("hello")); err != nil {
		t.Fatalf("PutFile: %v", err)
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
		nil,
	)
	handler := New(Deps{
		Store:    st,
		Manager:  manager,
		Projects: ps,
		CfgDir:   cfgDir,
	})

	body, err := json.Marshal(map[string]any{"project_id": projectID})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/tools/list_files/test", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if payload["ok"] != true {
		t.Fatalf("ok=%#v want true payload=%#v", payload["ok"], payload)
	}
	testResult, ok := payload["test"].(map[string]any)
	if !ok {
		t.Fatalf("test=%T want object", payload["test"])
	}
	if testResult["tool_name"] != "list_files" || testResult["mode"] != string(tools.ToolTestModeSampleCall) {
		t.Fatalf("test metadata=%#v", testResult)
	}
	presentation, ok := testResult["presentation"].(map[string]any)
	if !ok {
		t.Fatalf("presentation=%T want object", testResult["presentation"])
	}
	if presentation["family"] != "local-files" ||
		presentation["result_kind"] != "repository" ||
		presentation["renderer_strategy"] != "family" ||
		presentation["renderer_key"] != "local-files" ||
		presentation["native_viewer"] != "connector_inspector" ||
		presentation["file_capability"] != "local_repository" {
		t.Fatalf("presentation=%#v, want list_files local repository presentation", presentation)
	}
	output, ok := testResult["output"].(map[string]any)
	if !ok || output["ok"] != true {
		t.Fatalf("output=%#v want ok output", testResult["output"])
	}
	resultMetadata, ok := output["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("output.result_metadata=%T want object", output["result_metadata"])
	}
	if resultMetadata["integration"] != "local-files" ||
		resultMetadata["renderer_strategy"] != "family" ||
		resultMetadata["renderer_key"] != "local-files" {
		t.Fatalf("output.result_metadata=%#v, want canonical local-files metadata", resultMetadata)
	}
	if _, ok := resultMetadata["tool_presentation"]; !ok {
		t.Fatalf("output.result_metadata.tool_presentation missing: %#v", resultMetadata)
	}
}

func TestHandleTestToolIncludesNativeArtifactPayload(t *testing.T) {
	ctx := context.Background()
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
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
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
		nil,
	)
	handler := New(Deps{
		Store:    st,
		Manager:  manager,
		Projects: ps,
		CfgDir:   cfgDir,
	})

	body, err := json.Marshal(map[string]any{"thread_id": thread.ID})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/tools/export_thread/test", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	testResult, ok := payload["test"].(map[string]any)
	if !ok {
		t.Fatalf("test=%T want object", payload["test"])
	}
	output, ok := testResult["output"].(map[string]any)
	if !ok {
		t.Fatalf("output=%T want object", testResult["output"])
	}
	artifact, ok := output["artifact"].(map[string]any)
	if !ok {
		t.Fatalf("output.artifact=%T want object; output=%#v", output["artifact"], output)
	}
	if artifact["type"] != "file" {
		t.Fatalf("artifact.type=%#v want file", artifact["type"])
	}
	if content := strings.TrimSpace(fmt.Sprint(artifact["content"])); !strings.Contains(content, `"bundle_type"`) {
		t.Fatalf("artifact.content missing bundle payload: %.120q", content)
	}
	metadata, ok := artifact["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("artifact.metadata=%T want object", artifact["metadata"])
	}
	if metadata["tool_test"] != true ||
		metadata["tool_name"] != "export_thread" ||
		metadata["export_kind"] != "thread_bundle" {
		t.Fatalf("artifact metadata=%#v, want native export_thread test metadata", metadata)
	}
	if _, ok := metadata["tool_presentation"]; !ok {
		t.Fatalf("artifact metadata missing tool_presentation: %#v", metadata)
	}
	resultMetadata, ok := metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("artifact metadata.result_metadata=%T want object", metadata["result_metadata"])
	}
	if resultMetadata["native_viewer"] != "file" ||
		resultMetadata["renderer_strategy"] != "artifact_type" ||
		resultMetadata["renderer_key"] != "file" {
		t.Fatalf("result_metadata=%#v, want file native viewer metadata", resultMetadata)
	}
}

func TestHandleTestToolRejectsUnknownTool(t *testing.T) {
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
		nil,
	)
	handler := New(Deps{
		Store:    st,
		Manager:  manager,
		Projects: ps,
		CfgDir:   cfgDir,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/tools/not_a_tool/test", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleTestToolRecognizesProviderHostedCapability(t *testing.T) {
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
		nil,
	)
	handler := New(Deps{
		Store:    st,
		Manager:  manager,
		Projects: ps,
		CfgDir:   cfgDir,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/tools/test", bytes.NewReader([]byte(`{"tool_name":"openai-responses/web_search"}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	testResult, ok := payload["test"].(map[string]any)
	if !ok {
		t.Fatalf("test=%T want object payload=%#v", payload["test"], payload)
	}
	if testResult["tool_name"] != "openai-responses/web_search" ||
		testResult["mode"] != string(tools.ToolTestModeConnectionCheck) ||
		testResult["origin"] != "provider" ||
		testResult["tool_set_id"] != "provider-hosted" ||
		testResult["connector_id"] != "openai-responses" {
		t.Fatalf("provider-hosted test metadata=%#v", testResult)
	}
	output, ok := testResult["output"].(map[string]any)
	if !ok || output["ok"] != true {
		t.Fatalf("output=%#v want ok output", testResult["output"])
	}
	result, ok := output["result"].(map[string]any)
	if !ok || result["provider_hosted_tool_id"] != "openai-responses/web_search" || result["not_executed"] != true {
		t.Fatalf("result=%#v want non-executing provider-hosted result", output["result"])
	}
	resultMetadata, ok := output["result_metadata"].(map[string]any)
	if !ok ||
		resultMetadata["provider_hosted_tool_id"] != "openai-responses/web_search" ||
		resultMetadata["permission_subject_kind"] != "provider_hosted_tool" {
		t.Fatalf("result_metadata=%#v want provider-hosted metadata", output["result_metadata"])
	}
}

func TestHandleTestToolReturnsMissingRequiredArguments(t *testing.T) {
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
		nil,
	)
	handler := New(Deps{
		Store:    st,
		Manager:  manager,
		Projects: ps,
		CfgDir:   cfgDir,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/tools/read_file/test", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if payload["tool_name"] != "read_file" {
		t.Fatalf("tool_name=%#v", payload["tool_name"])
	}
	if !jsonStringArrayContains(payload["missing_arguments"], "path") {
		t.Fatalf("missing_arguments=%#v want path", payload["missing_arguments"])
	}
	if payload["input_hint"] == "" {
		t.Fatalf("input_hint missing from payload: %#v", payload)
	}
}

func TestHandleTestToolReturnsMissingAlternativeArgumentGroups(t *testing.T) {
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
	manager := optionc.NewManager(
		st,
		cfg,
		providers.NewFactory(cfg, sec),
		tools.NewRegistry(&tools.RegistryOptions{SharePoint: sharepoint.NewManager(cfgDir, nil)}),
		ps,
		nil,
		nil,
		nil,
		cfgDir,
		nil,
	)
	handler := New(Deps{
		Store:    st,
		Manager:  manager,
		Projects: ps,
		CfgDir:   cfgDir,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/tools/sharepoint_read/test", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if payload["tool_name"] != tools.SharePointToolRead {
		t.Fatalf("tool_name=%#v", payload["tool_name"])
	}
	if !jsonStringArrayContains(payload["missing_arguments"], "item_url") ||
		!jsonStringArrayContains(payload["missing_arguments"], "server_relative_url") {
		t.Fatalf("missing_arguments=%#v want SharePoint alternatives", payload["missing_arguments"])
	}
	if !jsonStringMatrixContainsGroup(payload["required_argument_groups"], "item_url") ||
		!jsonStringMatrixContainsGroup(payload["required_argument_groups"], "server_relative_url") {
		t.Fatalf("required_argument_groups=%#v want SharePoint alternatives", payload["required_argument_groups"])
	}
	if payload["input_hint"] == "" {
		t.Fatalf("input_hint missing from payload: %#v", payload)
	}
}

func TestHandleTestToolReturnsMissingRequiredContext(t *testing.T) {
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
		nil,
	)
	handler := New(Deps{
		Store:    st,
		Manager:  manager,
		Projects: ps,
		CfgDir:   cfgDir,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/tools/list_files/test", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if payload["tool_name"] != "list_files" {
		t.Fatalf("tool_name=%#v", payload["tool_name"])
	}
	if !jsonStringArrayContains(payload["missing_context"], "project") {
		t.Fatalf("missing_context=%#v want project", payload["missing_context"])
	}
	if !jsonStringArrayContains(payload["required_context"], "project") {
		t.Fatalf("required_context=%#v want project", payload["required_context"])
	}
}

func TestHandleTestToolReturnsMissingAutomaticConnectionContext(t *testing.T) {
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
		nil,
	)
	handler := New(Deps{
		Store:    st,
		Manager:  manager,
		Projects: ps,
		CfgDir:   cfgDir,
	})

	body := []byte(`{"args":{"repository":"owner/repo"}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tools/"+tools.GitHubToolCloneRepository+"/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if payload["tool_name"] != tools.GitHubToolCloneRepository {
		t.Fatalf("tool_name=%#v", payload["tool_name"])
	}
	if !jsonStringArrayContains(payload["missing_context"], "connection:github") {
		t.Fatalf("missing_context=%#v want connection:github", payload["missing_context"])
	}
	if !jsonStringArrayContains(payload["required_context"], "connection:github") {
		t.Fatalf("required_context=%#v want connection:github", payload["required_context"])
	}
}

func TestHandleTestToolReturnsPermissionDecision(t *testing.T) {
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
	projectID := "project-tool-http-permission"
	if err := ps.PutFile(projectID, "README.md", []byte("hello")); err != nil {
		t.Fatalf("PutFile: %v", err)
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
		Store:    st,
		Manager:  manager,
		Projects: ps,
		CfgDir:   cfgDir,
	})

	body, err := json.Marshal(map[string]any{
		"project_id": projectID,
		"args":       map[string]any{"path": "README.md"},
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/tools/read_file/test", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if payload["tool_name"] != "read_file" || payload["decision"] != permissions.DecisionAsk {
		t.Fatalf("permission payload=%#v", payload)
	}
	subject, ok := payload["subject"].(map[string]any)
	if !ok || subject["kind"] != "local_location" {
		t.Fatalf("subject=%#v want local_location", payload["subject"])
	}
}

func TestHandleTestToolLocalPermissionStatesReturnPreciseSubject(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name         string
		ruleEffect   string
		wantStatus   int
		wantDecision string
	}{
		{name: "ask_default", wantStatus: http.StatusConflict, wantDecision: permissions.DecisionAsk},
		{name: "deny_rule", ruleEffect: permissions.EffectDeny, wantStatus: http.StatusForbidden, wantDecision: permissions.DecisionDeny},
		{name: "allow_rule", ruleEffect: permissions.EffectAllow, wantStatus: http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			projectID := "project-tool-http-local-" + tc.name
			if err := ps.PutFile(projectID, "README.md", []byte("hello")); err != nil {
				t.Fatalf("PutFile: %v", err)
			}
			projectRoot, err := ps.FilesDir(projectID)
			if err != nil {
				t.Fatalf("FilesDir: %v", err)
			}
			targetFile := filepath.Join(projectRoot, "README.md")
			if tc.ruleEffect != "" {
				if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
					Category: permissions.CategoryRead,
					Effect:   tc.ruleEffect,
					Scope:    "global",
					Subject: store.PermissionSubject{
						Kind:      "local_location",
						Tool:      "read_file",
						LocalRoot: targetFile,
					},
					CreatedVia: "test",
				}); err != nil {
					t.Fatalf("CreateRule: %v", err)
				}
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
				Store:    st,
				Manager:  manager,
				Projects: ps,
				CfgDir:   cfgDir,
			})

			body, err := json.Marshal(map[string]any{
				"project_id": projectID,
				"args":       map[string]any{"path": "README.md"},
			})
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/tools/read_file/test", bytes.NewReader(body)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			if tc.wantStatus == http.StatusOK {
				if payload["ok"] != true {
					t.Fatalf("ok=%#v want true payload=%#v", payload["ok"], payload)
				}
				testResult, ok := payload["test"].(map[string]any)
				if !ok {
					t.Fatalf("test=%T want object", payload["test"])
				}
				output, ok := testResult["output"].(map[string]any)
				if !ok || output["ok"] != true {
					t.Fatalf("output=%#v want successful read_file output", testResult["output"])
				}
				return
			}
			if payload["tool_name"] != "read_file" || payload["decision"] != tc.wantDecision || payload["category"] != permissions.CategoryRead {
				t.Fatalf("permission payload=%#v", payload)
			}
			subject, ok := payload["subject"].(map[string]any)
			if !ok {
				t.Fatalf("subject=%T want object", payload["subject"])
			}
			if subject["kind"] != "local_location" || subject["tool"] != "read_file" {
				t.Fatalf("subject=%#v want read_file local_location", subject)
			}
			if got := filepath.Clean(fmt.Sprint(subject["local_root"])); got != filepath.Clean(targetFile) {
				t.Fatalf("subject.local_root=%q want %q; subject=%#v", got, targetFile, subject)
			}
			if !jsonStringArrayContains(subject["excludes"], ".env") || !jsonStringArrayContains(subject["excludes"], "**/.ssh/**") {
				t.Fatalf("subject.excludes=%#v want sensitive local exclusion patterns", subject["excludes"])
			}
		})
	}
}

func TestHandleTestToolReturnsRemotePermissionSubjects(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name          string
		toolName      string
		args          map[string]any
		registry      string
		denyRule      *permissions.PermissionRule
		wantStatus    int
		wantDecision  string
		wantCategory  string
		wantKind      string
		wantConnector string
		wantToolSetID string
		wantOperation string
		wantRemote    string
		wantRemoteID  string
	}{
		{
			name:          "slack_write_channel",
			toolName:      tools.SlackToolPostMessage,
			args:          map[string]any{"channel_id": "C123", "text": "hello"},
			registry:      "slack",
			wantStatus:    http.StatusConflict,
			wantDecision:  permissions.DecisionAsk,
			wantCategory:  permissions.CategoryWrite,
			wantKind:      "tool",
			wantConnector: "slack",
			wantToolSetID: "slack",
			wantOperation: "slack_post_message_execute",
			wantRemote:    "slack_channel",
			wantRemoteID:  "C123",
		},
		{
			name:          "jira_write_issue",
			toolName:      "jira_update_issue",
			args:          map[string]any{"issueKey": "proj-1", "summary": "Updated summary"},
			registry:      "atlassian",
			wantStatus:    http.StatusConflict,
			wantDecision:  permissions.DecisionAsk,
			wantCategory:  permissions.CategoryWrite,
			wantKind:      "tool",
			wantConnector: "atlassian",
			wantToolSetID: "atlassian",
			wantOperation: "jira_update_issue_execute",
			wantRemote:    "jira_issue",
			wantRemoteID:  "PROJ-1",
		},
		{
			name:          "github_clone_repository",
			toolName:      tools.GitHubToolCloneRepository,
			args:          map[string]any{"repository": "Owner/Repo"},
			registry:      "github",
			wantStatus:    http.StatusConflict,
			wantDecision:  permissions.DecisionAsk,
			wantCategory:  permissions.CategoryExecute,
			wantKind:      "repository",
			wantConnector: "github",
			wantToolSetID: "github",
			wantOperation: "clone_repository",
			wantRemote:    "github_repository",
			wantRemoteID:  "owner/repo",
		},
		{
			name:     "sharepoint_read_file_denied",
			toolName: tools.SharePointToolRead,
			args:     map[string]any{"server_relative_url": "/sites/team/Shared Documents/report.docx"},
			registry: "sharepoint",
			denyRule: &permissions.PermissionRule{
				Category: permissions.CategoryRead,
				Effect:   permissions.EffectDeny,
				Scope:    "global",
				Subject: store.PermissionSubject{
					Kind:       "tool",
					Tool:       tools.SharePointToolRead,
					Connector:  "sharepoint",
					ToolSetID:  "sharepoint",
					Operation:  tools.SharePointToolRead,
					RemoteKind: "sharepoint_file",
					RemoteID:   "/sites/team/Shared Documents/report.docx",
				},
				CreatedVia: "test",
			},
			wantStatus:    http.StatusForbidden,
			wantDecision:  permissions.DecisionDeny,
			wantCategory:  permissions.CategoryRead,
			wantKind:      "tool",
			wantConnector: "sharepoint",
			wantToolSetID: "sharepoint",
			wantOperation: tools.SharePointToolRead,
			wantRemote:    "sharepoint_file",
			wantRemoteID:  "/sites/team/Shared Documents/report.docx",
		},
		{
			name:     "arxiv_download_paper_denied",
			toolName: tools.ArxivToolDownloadPaper,
			args:     map[string]any{"arxiv_id": "2401.00001"},
			registry: "arxiv",
			denyRule: &permissions.PermissionRule{
				Category: permissions.CategoryRead,
				Effect:   permissions.EffectDeny,
				Scope:    "global",
				Subject: store.PermissionSubject{
					Kind:       "tool",
					Tool:       tools.ArxivToolDownloadPaper,
					Connector:  "arxiv",
					ToolSetID:  "arxiv",
					Operation:  tools.ArxivToolDownloadPaper,
					RemoteKind: "arxiv_paper",
					RemoteID:   "2401.00001",
				},
				CreatedVia: "test",
			},
			wantStatus:    http.StatusForbidden,
			wantDecision:  permissions.DecisionDeny,
			wantCategory:  permissions.CategoryRead,
			wantKind:      "tool",
			wantConnector: "arxiv",
			wantToolSetID: "arxiv",
			wantOperation: tools.ArxivToolDownloadPaper,
			wantRemote:    "arxiv_paper",
			wantRemoteID:  "2401.00001",
		},
		{
			name:          "browser_navigate_origin",
			toolName:      tools.BrowserControlToolNavigate,
			args:          map[string]any{"url": "https://Example.com/path"},
			registry:      "browser",
			wantStatus:    http.StatusConflict,
			wantDecision:  permissions.DecisionAsk,
			wantCategory:  permissions.CategoryExecute,
			wantKind:      "tool",
			wantConnector: "browser",
			wantToolSetID: "browser",
			wantOperation: "navigate",
			wantRemote:    "browser_origin",
			wantRemoteID:  "https://example.com",
		},
		{
			name:     "slack_read_file_denied",
			toolName: tools.SlackToolDownloadFile,
			args:     map[string]any{"file_id": "FABC", "channel_id": "C123"},
			registry: "slack",
			denyRule: &permissions.PermissionRule{
				Category: permissions.CategoryRead,
				Effect:   permissions.EffectDeny,
				Scope:    "global",
				Subject: store.PermissionSubject{
					Kind:       "tool",
					Tool:       tools.SlackToolDownloadFile,
					Connector:  "slack",
					Operation:  tools.SlackToolDownloadFile,
					RemoteKind: "slack_file",
					RemoteID:   "FABC",
				},
				CreatedVia: "test",
			},
			wantStatus:    http.StatusForbidden,
			wantDecision:  permissions.DecisionDeny,
			wantCategory:  permissions.CategoryRead,
			wantKind:      "tool",
			wantConnector: "slack",
			wantToolSetID: "slack",
			wantOperation: tools.SlackToolDownloadFile,
			wantRemote:    "slack_file",
			wantRemoteID:  "FABC",
		},
		{
			name:          "cancel_query_execute",
			toolName:      "cancel_query",
			args:          map[string]any{"job_id": "query-job-123", "provider": "bigquery"},
			wantStatus:    http.StatusConflict,
			wantDecision:  permissions.DecisionAsk,
			wantCategory:  permissions.CategoryExecute,
			wantKind:      "tool",
			wantConnector: "bigquery",
			wantToolSetID: "query-lifecycle",
			wantOperation: "cancel_query",
			wantRemote:    "query_job",
			wantRemoteID:  "query-job-123",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
			if tc.denyRule != nil {
				if _, err := permissionSvc.CreateRule(ctx, *tc.denyRule); err != nil {
					t.Fatalf("CreateRule: %v", err)
				}
			}
			registryOpts := &tools.RegistryOptions{}
			switch tc.registry {
			case "slack":
				registryOpts.Slack = slack.NewManager(cfg, sec)
			case "atlassian":
				registryOpts.Atlassian = atlassian.NewManager(cfgDir, cfg)
			case "github":
				registryOpts.GitHub = &struct{}{}
			case "sharepoint":
				registryOpts.SharePoint = sharepoint.NewManager(cfgDir, nil)
			case "arxiv":
				registryOpts.Arxiv = arxiv.NewManager(arxiv.Options{CfgDir: cfgDir})
			case "browser":
				registryOpts.Browser = browsercontrol.NewManager(cfgDir, nil)
			}
			manager := optionc.NewManager(
				st,
				cfg,
				providers.NewFactory(cfg, sec),
				tools.NewRegistry(registryOpts),
				ps,
				nil,
				nil,
				nil,
				cfgDir,
				&optionc.Options{Permissions: permissionSvc},
			)
			handler := New(Deps{
				Store:    st,
				Manager:  manager,
				Projects: ps,
				CfgDir:   cfgDir,
			})

			body, err := json.Marshal(map[string]any{"args": tc.args})
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/tools/"+tc.toolName+"/test", bytes.NewReader(body)).WithContext(ctx)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d want %d body=%s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}
			if payload["tool_name"] != tc.toolName || payload["decision"] != tc.wantDecision || payload["category"] != tc.wantCategory {
				t.Fatalf("permission payload=%#v", payload)
			}
			subject, ok := payload["subject"].(map[string]any)
			if !ok {
				t.Fatalf("subject=%T want object", payload["subject"])
			}
			assertSubjectField := func(key string, want string) {
				t.Helper()
				if want == "" {
					return
				}
				if got := fmt.Sprint(subject[key]); got != want {
					t.Fatalf("subject[%s]=%q want %q; subject=%#v", key, got, want, subject)
				}
			}
			assertSubjectField("kind", tc.wantKind)
			assertSubjectField("connector", tc.wantConnector)
			assertSubjectField("tool_set_id", tc.wantToolSetID)
			assertSubjectField("operation", tc.wantOperation)
			assertSubjectField("remote_kind", tc.wantRemote)
			assertSubjectField("remote_id", tc.wantRemoteID)
		})
	}
}

func TestHandleTestToolAlwaysAllowExecutesDryRun(t *testing.T) {
	ctx := context.Background()
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
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryExecute,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:       "repository",
			Tool:       tools.GitHubToolCloneRepository,
			Connector:  "github",
			ToolSetID:  "github",
			Repository: "owner/repo",
			Operation:  "clone_repository",
			RemoteKind: "github_repository",
			RemoteID:   "owner/repo",
		},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	manager := optionc.NewManager(
		st,
		cfg,
		providers.NewFactory(cfg, sec),
		tools.NewRegistry(&tools.RegistryOptions{GitHub: &struct{}{}}),
		ps,
		nil,
		nil,
		nil,
		cfgDir,
		&optionc.Options{Permissions: permissionSvc},
	)
	handler := New(Deps{
		Store:    st,
		Manager:  manager,
		Projects: ps,
		CfgDir:   cfgDir,
	})

	body, err := json.Marshal(map[string]any{
		"args": map[string]any{"repository": "Owner/Repo"},
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/tools/"+tools.GitHubToolCloneRepository+"/test", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if payload["ok"] != true {
		t.Fatalf("ok=%#v want true payload=%#v", payload["ok"], payload)
	}
	testResult, ok := payload["test"].(map[string]any)
	if !ok {
		t.Fatalf("test=%T want object", payload["test"])
	}
	if testResult["tool_name"] != tools.GitHubToolCloneRepository ||
		testResult["mode"] != string(tools.ToolTestModeDryRun) ||
		testResult["requires_approval"] != true {
		t.Fatalf("test metadata=%#v", testResult)
	}
	output, ok := testResult["output"].(map[string]any)
	if !ok || output["ok"] != true {
		t.Fatalf("output=%#v want successful dry-run output", testResult["output"])
	}
	result, ok := output["result"].(map[string]any)
	if !ok {
		t.Fatalf("output.result=%T want object", output["result"])
	}
	if result["dry_run"] != true || result["operation"] != "github_clone_repository_execute" {
		t.Fatalf("dry-run result=%#v, want clone dry-run operation", result)
	}
}

func jsonStringArrayContains(raw any, want string) bool {
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

func jsonStringMatrixContainsGroup(raw any, want string) bool {
	groups, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, groupRaw := range groups {
		if jsonStringArrayContains(groupRaw, want) {
			return true
		}
	}
	return false
}
