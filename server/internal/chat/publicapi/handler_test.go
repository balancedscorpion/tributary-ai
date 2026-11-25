package publicapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/optionc"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

func newTestSettings(t *testing.T, keys []config.ExternalAPIKey) *config.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	next := st.Get()
	next.ExternalAPI = &config.ExternalAPISettings{APIKeys: keys}
	if err := st.Save(next); err != nil {
		t.Fatalf("Save settings: %v", err)
	}
	return st
}

func TestAuthenticateAPIKeyScopes(t *testing.T) {
	token := "aia_test_token_123"
	now := time.Now().Add(-time.Hour).UnixMilli()
	settings := newTestSettings(t, []config.ExternalAPIKey{
		{
			ID:        "key_1",
			Name:      "test",
			Prefix:    tokenPrefix(token),
			Hash:      hashAPIKey(token),
			Scopes:    []string{ScopeRunsCreate, ScopeRunsRead},
			CreatedAt: now,
		},
	})

	auth, err := authenticateAPIKey(settings, "Bearer "+token, ScopeRunsCreate)
	if err != nil {
		t.Fatalf("authenticateAPIKey returned error: %v", err)
	}
	if auth.KeyID != "key_1" {
		t.Fatalf("KeyID=%q, want key_1", auth.KeyID)
	}
	if auth, err := authenticateAPIKey(settings, "bearer "+token, ScopeRunsRead); err != nil {
		t.Fatalf("authenticateAPIKey should accept case-insensitive bearer scheme: %v", err)
	} else if auth.KeyID != "key_1" {
		t.Fatalf("KeyID=%q, want key_1", auth.KeyID)
	}
	if updated := settings.Get().ExternalAPI.APIKeys[0].LastUsedAt; updated == nil || *updated <= 0 {
		t.Fatalf("LastUsedAt not updated: %#v", updated)
	}

	if _, err := authenticateAPIKey(settings, "Bearer "+token, ScopeArtifactsRead); err == nil {
		t.Fatalf("expected missing scope error")
	} else if apiErr, ok := err.(*APIError); !ok || apiErr.Status != http.StatusForbidden {
		t.Fatalf("missing scope error=%T %[1]v, want 403 APIError", err)
	}

	if _, err := authenticateAPIKey(settings, "", ScopeRunsRead); err == nil {
		t.Fatalf("expected missing auth error")
	} else if apiErr, ok := err.(*APIError); !ok || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("missing auth error=%T %[1]v, want 401 APIError", err)
	}
}

func TestAuthenticateAPIKeyRejectsRevokedKey(t *testing.T) {
	token := "aia_revoked_token_123"
	revokedAt := time.Now().UnixMilli()
	settings := newTestSettings(t, []config.ExternalAPIKey{
		{
			ID:        "key_revoked",
			Prefix:    tokenPrefix(token),
			Hash:      hashAPIKey(token),
			Scopes:    []string{ScopeRunsRead},
			CreatedAt: time.Now().Add(-time.Hour).UnixMilli(),
			RevokedAt: &revokedAt,
		},
	})

	if _, err := authenticateAPIKey(settings, "Bearer "+token, ScopeRunsRead); err == nil {
		t.Fatalf("expected revoked key error")
	} else if apiErr, ok := err.(*APIError); !ok || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("revoked key error=%T %[1]v, want 401 APIError", err)
	}
}

func TestNewRequiresCfgDir(t *testing.T) {
	settings := newTestSettings(t, []config.ExternalAPIKey{})
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatalf("expected panic for missing CfgDir")
		}
	}()
	_ = New(Deps{Store: st, Manager: &fakeManager{}, Settings: settings})
}

func TestNormalizeCreateRunRequest(t *testing.T) {
	body := createRunRequest{
		ThreadID:  "thread_123",
		ProjectID: "project_123",
		Input: createRunInput{
			Content:               "  Analyze this  ",
			HiddenContext:         "hidden",
			ReferencedArtifactIDs: []string{"art_1", "art_1", "art_2"},
			ReferencedMessageIDs:  []string{"msg_1"},
			ReferencedThreadIDs:   []string{"thread_ref"},
			ImageAttachments: []types.ImageAttachment{
				{FileName: "plot.png", MimeType: "image/png", DataURI: "data:image/png;base64,AAAA"},
			},
			FileAttachments: []types.FileAttachment{
				{ArtifactID: "file_art", FileName: "input.csv", MimeType: "text/csv"},
			},
		},
		Purpose:       "analysis",
		SearchDepth:   "",
		MaxIterations: intPtr(42),
		DataSources: map[string]any{
			"bigquery": map[string]any{"enabled": true},
		},
		RuntimeContext: runtimeContextRequest{
			NowISO:           "2026-05-16T12:00:00Z",
			Timezone:         "Europe/London",
			UTCOffsetMinutes: intPtr(60),
		},
	}

	req, err := normalizeCreateRunRequest(body)
	if err != nil {
		t.Fatalf("normalizeCreateRunRequest: %v", err)
	}
	if req.ThreadID != "thread_123" {
		t.Fatalf("ThreadID=%q", req.ThreadID)
	}
	if req.Content != "Analyze this" {
		t.Fatalf("Content=%q", req.Content)
	}
	if req.HiddenContext != "hidden" {
		t.Fatalf("HiddenContext=%q", req.HiddenContext)
	}
	if req.ProjectIDOverride == nil || *req.ProjectIDOverride != "project_123" {
		t.Fatalf("ProjectIDOverride=%v", req.ProjectIDOverride)
	}
	if req.SearchDepth != "shallow" {
		t.Fatalf("SearchDepth=%q, want shallow", req.SearchDepth)
	}
	if req.MaxIterationsOverride == nil || *req.MaxIterationsOverride != 42 {
		t.Fatalf("MaxIterationsOverride=%v", req.MaxIterationsOverride)
	}
	if got := strings.Join(req.ReferencedArtifactIDs, ","); got != "art_1,art_2" {
		t.Fatalf("ReferencedArtifactIDs=%q", got)
	}
	if req.DataSourceOverrides["bigquery"] == nil {
		t.Fatalf("DataSourceOverrides missing bigquery")
	}
	if req.DataSources == nil || req.DataSources.BigQuery == nil || req.DataSources.BigQuery.Enabled == nil || !*req.DataSources.BigQuery.Enabled {
		t.Fatalf("DataSources.BigQuery.Enabled not true: %#v", req.DataSources)
	}
	if req.ClientNowISO != "2026-05-16T12:00:00Z" || req.ClientTimezone != "Europe/London" || req.ClientUTCOffsetMinutes == nil || *req.ClientUTCOffsetMinutes != 60 {
		t.Fatalf("runtime context not normalized: %#v", req)
	}
}

func TestNormalizeCreateRunRequestValidation(t *testing.T) {
	if _, err := normalizeCreateRunRequest(createRunRequest{}); err == nil {
		t.Fatalf("expected missing content error")
	}

	_, err := normalizeCreateRunRequest(createRunRequest{
		Input: createRunInput{
			Content:             "hi",
			ReferencedThreadIDs: []string{"1", "2", "3", "4", "5", "6"},
		},
	})
	if err == nil {
		t.Fatalf("expected referenced thread limit error")
	}

	_, err = normalizeCreateRunRequest(createRunRequest{
		Input:   createRunInput{Content: "hi"},
		Purpose: "other",
	})
	if err == nil {
		t.Fatalf("expected invalid purpose error")
	}

	_, err = normalizeCreateRunRequest(createRunRequest{
		Input:  createRunInput{Content: "hi"},
		Stream: true,
	})
	if err == nil {
		t.Fatalf("expected unsupported stream error")
	}

	for name, image := range map[string]types.ImageAttachment{
		"missing data URI": {FileName: "plot.png", MimeType: "image/png"},
		"non-image data URI": {
			FileName: "note.txt",
			MimeType: "text/plain",
			DataURI:  "data:text/plain;base64,AAAA",
		},
		"non-image mime type": {
			FileName: "plot.png",
			MimeType: "text/plain",
			DataURI:  "data:image/png;base64,AAAA",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeCreateRunRequest(createRunRequest{
				Input: createRunInput{ImageAttachments: []types.ImageAttachment{image}},
			})
			if err == nil {
				t.Fatalf("expected invalid image attachment error")
			}
		})
	}
}

func TestPublicRunDTO(t *testing.T) {
	started := time.UnixMilli(1000).UTC()
	ended := time.UnixMilli(2000).UTC()
	reqID := "req_123"
	inTok := 10
	outTok := 20
	totalTok := 30
	code := "FAILED"
	msg := "boom"
	need := true
	hard := false
	plan := json.RawMessage(`{"plan_reason":"unit"}`)
	connectionID := "cfg-123"
	dto := publicRunDTO(store.Run{
		ID:                   "run_1",
		ThreadID:             "thread_1",
		Status:               "failed",
		Purpose:              "analysis",
		ProviderID:           "compatible",
		Model:                "model",
		ConnectionID:         connectionID,
		PlanID:               "plan-123",
		SystemPrompt:         "secret system prompt",
		MaxIterations:        12,
		RequestID:            &reqID,
		InputTokens:          &inTok,
		OutputTokens:         &outTok,
		TotalTokens:          &totalTok,
		ErrorCode:            &code,
		ErrorMessage:         &msg,
		ContextNeedRetrieval: &need,
		ContextHardCapped:    &hard,
		ContextPlanJSON:      plan,
		CreatedAt:            time.UnixMilli(500).UTC(),
		StartedAt:            &started,
		EndedAt:              &ended,
		UpdatedAt:            time.UnixMilli(2100).UTC(),
	})

	if dto["object"] != "run" {
		t.Fatalf("object=%v", dto["object"])
	}
	if _, ok := dto["system_prompt"]; ok {
		t.Fatalf("public run DTO leaked system_prompt")
	}
	if dto["connection_id"] != connectionID || dto["plan_id"] != "plan-123" {
		t.Fatalf("target identity=%#v/%#v", dto["connection_id"], dto["plan_id"])
	}
	usage := dto["usage"].(map[string]any)
	if usage["total_tokens"] != 30 {
		t.Fatalf("usage=%#v", usage)
	}
	errObj := dto["error"].(map[string]any)
	if errObj["code"] != "FAILED" || errObj["message"] != "boom" {
		t.Fatalf("error=%#v", errObj)
	}
	ctx := dto["context"].(map[string]any)
	if ctx["need_retrieval"] != true || ctx["hard_capped"] != false {
		t.Fatalf("context=%#v", ctx)
	}
}

func TestPublicMessageDTOParsesToolResult(t *testing.T) {
	content := `{"artifact_id":"art_1","tool_name":"display_table","tool_call_id":"call_1","ok":true,"result_format":"json","result":{"rows":[{"x":1}]},"ui_suppress":true}`
	hidden := "do not expose"
	payload := json.RawMessage(`{"raw":"provider"}`)
	msg := store.Message{
		ID:              "msg_1",
		ThreadID:        "thread_1",
		RunID:           stringPtr("run_1"),
		Seq:             7,
		Role:            types.RoleTool,
		Content:         &content,
		ProviderPayload: payload,
		HiddenContext:   &hidden,
		ToolCallID:      stringPtr("call_1"),
		Name:            stringPtr("display_table"),
		CreatedAt:       time.UnixMilli(100).UTC(),
		UpdatedAt:       time.UnixMilli(200).UTC(),
	}

	dto := publicMessageDTO(msg)
	if _, ok := dto["provider_payload"]; ok {
		t.Fatalf("public message DTO leaked provider_payload")
	}
	if _, ok := dto["hidden_context"]; ok {
		t.Fatalf("public message DTO leaked hidden_context")
	}
	tr, ok := dto["tool_result"].(map[string]any)
	if !ok {
		t.Fatalf("tool_result missing: %#v", dto)
	}
	if tr["artifact_id"] != "art_1" {
		t.Fatalf("tool_result=%#v", tr)
	}
	display := tr["display"].(map[string]any)
	if display["suppress"] != true {
		t.Fatalf("display suppress=%#v", display)
	}
}

func TestMapSSEEvent(t *testing.T) {
	run := store.Run{ID: "run_1", ThreadID: "thread_1", Status: "running"}
	ev, err := mapSSEEvent(run, optionc.SSEEvent{
		Event: "assistant_start",
		Data:  []byte(`{}`),
	}, 3)
	if err != nil {
		t.Fatalf("mapSSEEvent assistant_start: %v", err)
	}
	if ev.Type != EventAssistantStarted {
		t.Fatalf("Type=%q", ev.Type)
	}
	if ev.Sequence != 3 || ev.RunID != "run_1" || ev.ThreadID != "thread_1" {
		t.Fatalf("event identity=%#v", ev)
	}

	ev, err = mapSSEEvent(run, optionc.SSEEvent{
		Event: "tool_result",
		Data:  []byte(`{"message":{"role":"tool","content":"{\"artifact_id\":\"art_1\",\"ok\":true,\"result_format\":\"json\"}","tool_call_id":"call_1","name":"display_table"}}`),
	}, 4)
	if err != nil {
		t.Fatalf("mapSSEEvent tool_result: %v", err)
	}
	if ev.Type != EventToolResult {
		t.Fatalf("Type=%q", ev.Type)
	}
	msg, ok := ev.Data["message"].(map[string]any)
	if !ok || msg["tool_result"] == nil {
		t.Fatalf("tool result message not structured: %#v", ev.Data)
	}
}

func TestHandlerRunEventsAfterSkipsReplayedBacklog(t *testing.T) {
	cfgDir := t.TempDir()
	token := "aia_stream"
	settings := newTestSettings(t, []config.ExternalAPIKey{
		{ID: "key_stream", Prefix: tokenPrefix(token), Hash: hashAPIKey(token), Scopes: []string{ScopeRunsRead}, CreatedAt: time.Now().UnixMilli()},
	})
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(context.Background(), thread.ID, "compatible", "test-model", nil, "system", 20)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &fakeManager{events: []optionc.SSEEvent{
		{Seq: 1, Event: "text_delta", Data: []byte(`{"content":"one"}`)},
		{Seq: 2, Event: "text_delta", Data: []byte(`{"content":"two"}`)},
		{Seq: 3, Event: "text_delta", Data: []byte(`{"content":"three"}`)},
	}}
	h := New(Deps{Store: st, Manager: manager, Settings: settings, CfgDir: cfgDir})
	req := httptest.NewRequest(http.MethodGet, "/api/external/v1/runs/"+run.ID+"/events?after=2", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `"content":"one"`) || strings.Contains(body, `"content":"two"`) {
		t.Fatalf("replayed events were not skipped: %s", body)
	}
	if !strings.Contains(body, `"sequence":3`) || !strings.Contains(body, `"content":"three"`) {
		t.Fatalf("expected sequence 3 event, got: %s", body)
	}
}

func TestHandlerCreateRun(t *testing.T) {
	cfgDir := t.TempDir()
	settings := newTestSettings(t, []config.ExternalAPIKey{
		{
			ID:        "key_1",
			Prefix:    tokenPrefix("aia_create_run"),
			Hash:      hashAPIKey("aia_create_run"),
			Scopes:    []string{ScopeRunsCreate},
			CreatedAt: time.Now().UnixMilli(),
		},
	})
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	manager := &fakeManager{}
	h := New(Deps{
		Store:    st,
		Manager:  manager,
		Settings: settings,
		CfgDir:   cfgDir,
	})

	body := `{"input":{"content":"hello"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/external/v1/runs", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer aia_create_run")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if manager.started.Content != "hello" {
		t.Fatalf("manager started content=%q", manager.started.Content)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response json: %v", err)
	}
	if resp["events_url"] == "" || resp["result_url"] == "" {
		t.Fatalf("missing URLs: %#v", resp)
	}
	if _, err := st.GetThread(context.Background(), manager.started.ThreadID); err != nil {
		t.Fatalf("created thread not persisted: %v", err)
	}
	if !strings.HasPrefix(filepath.ToSlash(resp["events_url"].(string)), "/api/external/v1/runs/") {
		t.Fatalf("events_url=%v", resp["events_url"])
	}
}

func TestHandlerStatusRequiresValidKey(t *testing.T) {
	cfgDir := t.TempDir()
	token := "aia_status"
	settings := newTestSettings(t, []config.ExternalAPIKey{
		{ID: "key_status", Prefix: tokenPrefix(token), Hash: hashAPIKey(token), CreatedAt: time.Now().UnixMilli()},
	})
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	h := New(Deps{Store: st, Manager: &fakeManager{}, Settings: settings, CfgDir: cfgDir})

	req := httptest.NewRequest(http.MethodGet, "/api/external/v1/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/external/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp["object"] != "api.status" || resp["ok"] != true {
		t.Fatalf("status response=%#v", resp)
	}
}

func TestHandlerRejectsResourceScopedKeyForOtherThread(t *testing.T) {
	cfgDir := t.TempDir()
	token := "aia_scoped"
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	allowed, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread allowed: %v", err)
	}
	other, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread other: %v", err)
	}
	settings := newTestSettings(t, []config.ExternalAPIKey{
		{
			ID:               "key_scoped",
			Prefix:           tokenPrefix(token),
			Hash:             hashAPIKey(token),
			Scopes:           []string{ScopeThreadsRead},
			AllowedThreadIDs: []string{allowed.ID},
			CreatedAt:        time.Now().UnixMilli(),
		},
	})
	h := New(Deps{Store: st, Manager: &fakeManager{}, Settings: settings, CfgDir: cfgDir})

	req := httptest.NewRequest(http.MethodGet, "/api/external/v1/threads/"+other.ID+"/messages", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("other thread status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/external/v1/threads/"+allowed.ID+"/messages", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("allowed thread status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlerCancelRunSurfacesManagerError(t *testing.T) {
	cfgDir := t.TempDir()
	token := "aia_cancel"
	settings := newTestSettings(t, []config.ExternalAPIKey{
		{ID: "key_cancel", Prefix: tokenPrefix(token), Hash: hashAPIKey(token), Scopes: []string{ScopeRunsCancel}, CreatedAt: time.Now().UnixMilli()},
	})
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(context.Background(), thread.ID, "compatible", "test-model", nil, "system", 20)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	h := New(Deps{Store: st, Manager: &fakeManager{cancelErr: store.ErrConflict}, Settings: settings, CfgDir: cfgDir})
	req := httptest.NewRequest(http.MethodPost, "/api/external/v1/runs/"+run.ID+"/cancel", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandlerRunResultIncludesToolResultAndArtifact(t *testing.T) {
	cfgDir := t.TempDir()
	token := "aia_read_result"
	settings := newTestSettings(t, []config.ExternalAPIKey{
		{
			ID:        "key_read",
			Prefix:    tokenPrefix(token),
			Hash:      hashAPIKey(token),
			Scopes:    []string{ScopeRunsRead},
			CreatedAt: time.Now().UnixMilli(),
		},
	})
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(context.Background(), thread.ID, "compatible", "test-model", nil, "system", 20)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.UpdateRunStatus(context.Background(), run.ID, "completed", map[string]any{"ended_at": time.Now().UTC()}); err != nil {
		t.Fatalf("UpdateRunStatus: %v", err)
	}
	run, _ = st.GetRun(context.Background(), run.ID)
	assistantContent := "Here is the answer."
	if _, err := st.AppendMessage(context.Background(), thread.ID, &run.ID, store.Message{
		ID:      "msg_assistant",
		Role:    types.RoleAssistant,
		Content: &assistantContent,
	}); err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	toolContent := `{"artifact_id":"art_result","tool_name":"display_table","tool_call_id":"call_1","ok":true,"result_format":"json","result":{"rows":[{"region":"A","total":10}]}}`
	toolMsg, err := st.AppendMessage(context.Background(), thread.ID, &run.ID, store.Message{
		ID:         "msg_tool",
		Role:       types.RoleTool,
		Content:    &toolContent,
		ToolCallID: stringPtr("call_1"),
		Name:       stringPtr("display_table"),
	})
	if err != nil {
		t.Fatalf("Append tool: %v", err)
	}
	artifactContent := `[{"region":"A","total":10}]`
	if _, err := st.InsertArtifact(context.Background(), store.Artifact{
		ID:          "art_result",
		ThreadID:    thread.ID,
		MessageID:   &toolMsg.ID,
		Type:        "data",
		Title:       "Result",
		Storage:     "mysql",
		ContentText: &artifactContent,
		ByteSize:    int64(len(artifactContent)),
		Metadata:    map[string]any{"row_count": 1},
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	h := New(Deps{Store: st, Manager: &fakeManager{}, Settings: settings, CfgDir: cfgDir})
	req := httptest.NewRequest(http.MethodGet, "/api/external/v1/runs/"+run.ID+"/result", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp["answer"] != assistantContent {
		t.Fatalf("answer=%#v", resp["answer"])
	}
	toolResults := resp["tool_results"].([]any)
	if len(toolResults) != 1 {
		t.Fatalf("tool_results=%#v", toolResults)
	}
	artifacts := resp["artifacts"].([]any)
	if len(artifacts) != 1 {
		t.Fatalf("artifacts=%#v", artifacts)
	}
	if resp["error"] != nil {
		t.Fatalf("unexpected top-level error: %#v", resp["error"])
	}
	req = httptest.NewRequest(http.MethodGet, "/api/external/v1/runs/"+run.ID+"/result?include_tool_results=false", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	resp = map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if toolResults, ok := resp["tool_results"].([]any); !ok || len(toolResults) != 0 {
		t.Fatalf("tool_results should be empty when excluded: %#v", resp["tool_results"])
	}
}

func TestHandlerRunResultBoundsLargeToolResultsAndArtifacts(t *testing.T) {
	cfgDir := t.TempDir()
	token := "aia_bounded_result"
	settings := newTestSettings(t, []config.ExternalAPIKey{
		{
			ID:        "key_bounded",
			Prefix:    tokenPrefix(token),
			Hash:      hashAPIKey(token),
			Scopes:    []string{ScopeRunsRead},
			CreatedAt: time.Now().UnixMilli(),
		},
	})
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(context.Background(), thread.ID, "compatible", "test-model", nil, "system", 20)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.UpdateRunStatus(context.Background(), run.ID, "completed", map[string]any{"ended_at": time.Now().UTC()}); err != nil {
		t.Fatalf("UpdateRunStatus: %v", err)
	}
	run, _ = st.GetRun(context.Background(), run.ID)

	large := strings.Repeat("x", maxRunResultToolResultBytes*2)
	toolEnvelope := map[string]any{
		"artifact_id":     "art_large",
		"tool_name":       "display_table",
		"tool_call_id":    "call_large",
		"ok":              true,
		"result_format":   "json",
		"query_metadata":  map[string]any{"row_count": 1},
		"result_metadata": map[string]any{"source": "test"},
		"result":          map[string]any{"rows": []map[string]any{{"payload": large}}},
	}
	toolBytes, err := json.Marshal(toolEnvelope)
	if err != nil {
		t.Fatalf("json.Marshal tool envelope: %v", err)
	}
	toolContent := string(toolBytes)
	toolMsg, err := st.AppendMessage(context.Background(), thread.ID, &run.ID, store.Message{
		ID:         "msg_large_tool",
		Role:       types.RoleTool,
		Content:    &toolContent,
		ToolCallID: stringPtr("call_large"),
		Name:       stringPtr("display_table"),
	})
	if err != nil {
		t.Fatalf("Append tool: %v", err)
	}

	artifactContent := strings.Repeat("y", maxRunResultArtifactPreviewBytes+2048)
	if _, err := st.InsertArtifact(context.Background(), store.Artifact{
		ID:          "art_large",
		ThreadID:    thread.ID,
		MessageID:   &toolMsg.ID,
		Type:        "data",
		Title:       "Large Result",
		Storage:     "mysql",
		ContentText: &artifactContent,
		ByteSize:    int64(len(artifactContent)),
		Metadata:    map[string]any{"row_count": 1},
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	h := New(Deps{Store: st, Manager: &fakeManager{}, Settings: settings, CfgDir: cfgDir})
	req := httptest.NewRequest(http.MethodGet, "/api/external/v1/runs/"+run.ID+"/result", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	toolResults := resp["tool_results"].([]any)
	if len(toolResults) != 1 {
		t.Fatalf("tool_results=%#v", toolResults)
	}
	toolResult := toolResults[0].(map[string]any)
	if _, ok := toolResult["result"]; ok {
		t.Fatalf("large tool result still includes full result")
	}
	if toolResult["result_truncated"] != true || toolResult["preview_only"] != true {
		t.Fatalf("tool result truncation metadata missing: %#v", toolResult)
	}
	if preview, ok := toolResult["result_preview"].(string); !ok || preview == "" || len(preview) > maxRunResultToolResultBytes {
		t.Fatalf("result_preview invalid: %#v", toolResult["result_preview"])
	}

	messages := resp["messages"].([]any)
	var toolMessage map[string]any
	for _, raw := range messages {
		msg := raw.(map[string]any)
		if msg["role"] == string(types.RoleTool) {
			toolMessage = msg
			break
		}
	}
	if toolMessage == nil {
		t.Fatalf("missing tool message in result: %#v", messages)
	}
	if toolMessage["content_truncated"] != true {
		t.Fatalf("tool message content was not marked truncated: %#v", toolMessage)
	}
	if content, ok := toolMessage["content"].(string); !ok || len(content) >= len(toolContent) {
		t.Fatalf("tool message content was not bounded")
	}

	artifacts := resp["artifacts"].([]any)
	if len(artifacts) != 1 {
		t.Fatalf("artifacts=%#v", artifacts)
	}
	artifact := artifacts[0].(map[string]any)
	if artifact["content_truncated"] != true || artifact["preview_only"] != true {
		t.Fatalf("artifact truncation metadata missing: %#v", artifact)
	}
	if content, ok := artifact["content"].(string); !ok || len(content) > maxRunResultArtifactPreviewBytes {
		t.Fatalf("artifact content preview invalid")
	}
	if int(artifact["byte_size"].(float64)) != len(artifactContent) {
		t.Fatalf("byte_size=%#v, want %d", artifact["byte_size"], len(artifactContent))
	}
	meta := artifact["metadata"].(map[string]any)
	if int(meta["row_count"].(float64)) != 1 {
		t.Fatalf("artifact metadata lost: %#v", meta)
	}
}

func TestHandlerRunResultFailedIncludesTopLevelError(t *testing.T) {
	cfgDir := t.TempDir()
	token := "aia_failed_result"
	settings := newTestSettings(t, []config.ExternalAPIKey{
		{ID: "key_failed", Prefix: tokenPrefix(token), Hash: hashAPIKey(token), Scopes: []string{ScopeRunsRead}, CreatedAt: time.Now().UnixMilli()},
	})
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(context.Background(), thread.ID, "compatible", "test-model", nil, "system", 20)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := st.UpdateRunStatus(context.Background(), run.ID, "failed", map[string]any{"error_code": "MODEL_ERROR", "error_message": "boom", "ended_at": time.Now().UTC()}); err != nil {
		t.Fatalf("UpdateRunStatus: %v", err)
	}
	h := New(Deps{Store: st, Manager: &fakeManager{}, Settings: settings, CfgDir: cfgDir})
	req := httptest.NewRequest(http.MethodGet, "/api/external/v1/runs/"+run.ID+"/result", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	errObj, ok := resp["error"].(map[string]any)
	if !ok || errObj["code"] != "MODEL_ERROR" || errObj["message"] != "boom" {
		t.Fatalf("error=%#v", resp["error"])
	}
}

func TestHandlerArtifactRows(t *testing.T) {
	cfgDir := t.TempDir()
	token := "aia_artifact_rows"
	settings := newTestSettings(t, []config.ExternalAPIKey{
		{ID: "key_art", Prefix: tokenPrefix(token), Hash: hashAPIKey(token), Scopes: []string{ScopeArtifactsRead}, CreatedAt: time.Now().UnixMilli()},
	})
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	content := `[{"region":"A","total":10},{"region":"B","total":20}]`
	if _, err := st.InsertArtifact(context.Background(), store.Artifact{
		ID:          "art_rows",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Rows",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	h := New(Deps{Store: st, Manager: &fakeManager{}, Settings: settings, CfgDir: cfgDir})
	req := httptest.NewRequest(http.MethodGet, "/api/external/v1/artifacts/art_rows/rows?offset=1&limit=1&columns=region", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if int(resp["row_count_total"].(float64)) != 2 || int(resp["rows_returned"].(float64)) != 1 {
		t.Fatalf("row metadata=%#v", resp)
	}
	rows := resp["rows"].([]any)
	row := rows[0].(map[string]any)
	if row["region"] != "B" {
		t.Fatalf("row=%#v", row)
	}
	if _, ok := row["total"]; ok {
		t.Fatalf("projected row still has total: %#v", row)
	}
}

func TestHandlerArtifactRowsHasMoreAfterByteClamp(t *testing.T) {
	cfgDir := t.TempDir()
	token := "aia_artifact_clamp"
	settings := newTestSettings(t, []config.ExternalAPIKey{
		{ID: "key_clamp", Prefix: tokenPrefix(token), Hash: hashAPIKey(token), Scopes: []string{ScopeArtifactsRead}, CreatedAt: time.Now().UnixMilli()},
	})
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	var b strings.Builder
	b.WriteString("[")
	large := strings.Repeat("x", 600000)
	for i := 0; i < 5; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"payload":"`)
		b.WriteString(large)
		b.WriteString(`"}`)
	}
	b.WriteString("]")
	content := b.String()
	if _, err := st.InsertArtifact(context.Background(), store.Artifact{
		ID:          "art_clamp",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Large Rows",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	h := New(Deps{Store: st, Manager: &fakeManager{}, Settings: settings, CfgDir: cfgDir})
	req := httptest.NewRequest(http.MethodGet, "/api/external/v1/artifacts/art_clamp/rows?limit=5", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp["has_more"] != true {
		t.Fatalf("has_more=%#v, rows_returned=%#v", resp["has_more"], resp["rows_returned"])
	}
	if int(resp["next_offset"].(float64)) <= 0 {
		t.Fatalf("next_offset missing/invalid: %#v", resp["next_offset"])
	}
}

func TestHandlerQueryStatusMapsErrorsAndRedactsOwnerToken(t *testing.T) {
	cfgDir := t.TempDir()
	token := "aia_query"
	settings := newTestSettings(t, []config.ExternalAPIKey{
		{ID: "key_query", Prefix: tokenPrefix(token), Hash: hashAPIKey(token), Scopes: []string{ScopeQueriesRead}, CreatedAt: time.Now().UnixMilli()},
	})
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	h := New(Deps{Store: st, Manager: &fakeManager{queryErr: optionc.ErrQueryJobUnauthorized}, Settings: settings, CfgDir: cfgDir})
	req := httptest.NewRequest(http.MethodGet, "/api/external/v1/queries/job_1/status?thread_id="+thread.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-query-owner-token", "owner")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	h = New(Deps{Store: st, Manager: &fakeManager{queryPayload: map[string]any{"job_id": "job_1", "status": "completed", "owner_token": "secret"}}, Settings: settings, CfgDir: cfgDir})
	req = httptest.NewRequest(http.MethodGet, "/api/external/v1/queries/job_1/status?thread_id="+thread.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("x-query-owner-token", "owner")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "owner_token") {
		t.Fatalf("owner token leaked: %s", rec.Body.String())
	}
}

func TestHandlerPostAttachment(t *testing.T) {
	cfgDir := t.TempDir()
	token := "aia_attachment"
	settings := newTestSettings(t, []config.ExternalAPIKey{
		{ID: "key_attach", Prefix: tokenPrefix(token), Hash: hashAPIKey(token), Scopes: []string{ScopeAttachmentsWrite}, CreatedAt: time.Now().UnixMilli()},
	})
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	h := New(Deps{Store: st, Manager: &fakeManager{}, Settings: settings, CfgDir: cfgDir})
	req := httptest.NewRequest(http.MethodPost, "/api/external/v1/threads/"+thread.ID+"/attachments?filename=data.csv", strings.NewReader("a,b\n1,2\n"))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/csv")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	attachment := resp["attachment"].(map[string]any)
	artifactID := strings.TrimSpace(attachment["artifactId"].(string))
	if artifactID == "" {
		t.Fatalf("missing artifactId: %#v", attachment)
	}
	if got, _ := attachment["fileKind"].(string); got != "csv" {
		t.Fatalf("fileKind=%q, want csv; attachment=%#v", got, attachment)
	}
	if got, _ := attachment["normalizedMimeType"].(string); got != "text/csv" {
		t.Fatalf("normalizedMimeType=%q, want text/csv; attachment=%#v", got, attachment)
	}
	if got, _ := attachment["sha256"].(string); strings.TrimSpace(got) == "" {
		t.Fatalf("sha256 missing: %#v", attachment)
	}
	if caps, _ := attachment["capabilities"].(map[string]any); caps == nil || caps["canPreviewText"] != true {
		t.Fatalf("capabilities=%#v, want canPreviewText", attachment["capabilities"])
	}
	if _, err := st.GetArtifact(context.Background(), artifactID); err != nil {
		t.Fatalf("uploaded artifact not persisted: %v", err)
	}
}

func intPtr(v int) *int { return &v }

func stringPtr(v string) *string { return &v }

type fakeManager struct {
	started      optionc.StartRunRequest
	events       []optionc.SSEEvent
	cancelErr    error
	queryErr     error
	queryPayload map[string]any
}

func (m *fakeManager) StartRun(ctx context.Context, req optionc.StartRunRequest) (optionc.StartRunResult, error) {
	m.started = req
	return optionc.StartRunResult{
		Run: store.Run{
			ID:            "run_test",
			ThreadID:      req.ThreadID,
			Status:        "queued",
			ProviderID:    "compatible",
			Model:         "test-model",
			MaxIterations: 200,
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		},
	}, nil
}

func (m *fakeManager) Subscribe(runID string) (<-chan optionc.SSEEvent, func(), error) {
	ch := make(chan optionc.SSEEvent, len(m.events))
	for _, ev := range m.events {
		ch <- ev
	}
	close(ch)
	return ch, func() {}, nil
}

func (m *fakeManager) Cancel(runID string) error { return m.cancelErr }

func (m *fakeManager) GetQueryJobStatus(ctx context.Context, jobID, threadID, ownerToken string) (map[string]any, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	if m.queryPayload != nil {
		return cloneTestMap(m.queryPayload), nil
	}
	return map[string]any{"job_id": jobID, "thread_id": threadID, "status": "completed"}, nil
}

func (m *fakeManager) CancelQueryJob(ctx context.Context, jobID, threadID, ownerToken string) (map[string]any, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	if m.queryPayload != nil {
		return cloneTestMap(m.queryPayload), nil
	}
	return map[string]any{"job_id": jobID, "thread_id": threadID, "status": "cancelled"}, nil
}

func cloneTestMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
