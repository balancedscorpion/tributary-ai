package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/optionc"
	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/providers"
	searchsvc "ai-analyst-local/internal/chat/search"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

func TestCancelRunReportsDurableCancellationFailure(t *testing.T) {
	t.Parallel()
	harness := newDataSourceHTTPHarness(t, false, false)
	t.Cleanup(harness.cleanup)
	req, err := http.NewRequest(http.MethodPost, harness.baseURL+"/api/runs/missing-run/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		t.Fatalf("cancel status=%d body=%s, want 404", resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

func TestRunEventsTailsDurableCrossManagerExecutionWithoutSyntheticFailure(t *testing.T) {
	t.Parallel()
	harness := newDataSourceHTTPHarness(t, false, false)
	t.Cleanup(harness.cleanup)
	ctx := context.Background()
	run, err := harness.store.CreateRun(ctx, harness.thread.ID, "compatible", "test-model", nil, "system", 4)
	if err != nil {
		t.Fatal(err)
	}
	longHorizon, ok := harness.store.(store.LongHorizonRepository)
	if !ok {
		t.Fatal("test store does not implement long-horizon repository")
	}
	now := time.Now().UTC()
	live, claimed, err := longHorizon.ClaimRunWake(ctx, run.ID, run.Revision, "manager-a-live", now, now.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim remote live run: run=%#v claimed=%v err=%v", live, claimed, err)
	}

	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, harness.baseURL+"/api/runs/"+run.ID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	bodyDone := make(chan []byte, 1)
	go func() {
		body, _ := io.ReadAll(resp.Body)
		bodyDone <- body
	}()
	select {
	case body := <-bodyDone:
		t.Fatalf("nonterminal cross-manager stream closed early: %s", body)
	case <-time.After(400 * time.Millisecond):
	}

	for _, event := range []store.RunEvent{
		{RunID: run.ID, ThreadID: harness.thread.ID, Type: "assistant_delta", Data: json.RawMessage(`{"delta":"remote-answer"}`)},
		{RunID: run.ID, ThreadID: harness.thread.ID, Type: "tool_result", Data: json.RawMessage(`{"tool":"check_query_status","ok":true}`)},
	} {
		if _, err := harness.store.AppendRunEvent(ctx, event); err != nil {
			t.Fatalf("append remote event: %v", err)
		}
	}
	completed, changed, err := longHorizon.TransitionRunCAS(ctx, store.RunStateMutation{
		RunID: live.ID, ExpectedRevision: live.Revision, ExpectedStatuses: []string{store.RunStatusRunning},
		ExpectedResumeLeaseHolder: "manager-a-live", Status: store.RunStatusCompleted,
		ClearWakeAt: true, ClearResumeLease: true,
	})
	if err != nil || !changed || completed.Status != store.RunStatusCompleted {
		t.Fatalf("complete remote run: run=%#v changed=%v err=%v", completed, changed, err)
	}
	select {
	case body := <-bodyDone:
		text := string(body)
		for _, want := range []string{"remote-answer", "check_query_status", optionc.EventRunTerminal, `"status":"completed"`, "event: final"} {
			if !strings.Contains(text, want) {
				t.Fatalf("durable cross-manager stream missing %q: %s", want, text)
			}
		}
		if strings.Contains(text, "RUN_INTERRUPTED") {
			t.Fatalf("durable cross-manager stream synthesized failure: %s", text)
		}
	case <-requestCtx.Done():
		t.Fatalf("durable cross-manager stream did not finish: %v", requestCtx.Err())
	}
}

func TestRunEventsReportsDurableCrossManagerWaitingUserWithoutTerminalFailure(t *testing.T) {
	t.Parallel()
	harness := newDataSourceHTTPHarness(t, false, false)
	t.Cleanup(harness.cleanup)
	ctx := context.Background()
	run, err := harness.store.CreateRun(ctx, harness.thread.ID, "compatible", "test-model", nil, "system", 4)
	if err != nil {
		t.Fatal(err)
	}
	longHorizon, ok := harness.store.(store.LongHorizonRepository)
	if !ok {
		t.Fatal("test store does not implement long-horizon repository")
	}
	now := time.Now().UTC()
	_, claimed, err := longHorizon.ClaimRunWake(ctx, run.ID, run.Revision, "manager-a-paused", now, now.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("claim remote run: claimed=%v err=%v", claimed, err)
	}
	if err := longHorizon.ReleaseRunWake(ctx, run.ID, "manager-a-paused", store.RunStatusWaitingUser, nil); err != nil {
		t.Fatalf("park remote run for review: %v", err)
	}

	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, harness.baseURL+"/api/runs/"+run.ID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"event: run_waiting_user", run.ID, "durable query work was not abandoned"} {
		if !strings.Contains(text, want) {
			t.Fatalf("durable waiting-user stream missing %q: %s", want, text)
		}
	}
	for _, forbidden := range []string{"RUN_INTERRUPTED", "event: run_terminal", "event: final"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("durable waiting-user stream contained %q: %s", forbidden, text)
		}
	}
}

func TestHandleCreateRunDataSourceToggleControlsToolExposure(t *testing.T) {
	cases := []struct {
		name                    string
		baseBigQueryEnabled     bool
		overrideBigQueryEnabled bool
		useMessagePayload       bool
		expectBQToolInRequest   bool
		expectToolResultMessage bool
	}{
		{
			name:                    "top-level override can enable bigquery tool",
			baseBigQueryEnabled:     false,
			overrideBigQueryEnabled: true,
			useMessagePayload:       false,
			expectBQToolInRequest:   true,
			expectToolResultMessage: true,
		},
		{
			name:                    "top-level override can disable pre-enabled bigquery tool",
			baseBigQueryEnabled:     true,
			overrideBigQueryEnabled: false,
			useMessagePayload:       false,
			expectBQToolInRequest:   false,
			expectToolResultMessage: false,
		},
		{
			name:                    "message payload override can enable bigquery tool",
			baseBigQueryEnabled:     false,
			overrideBigQueryEnabled: true,
			useMessagePayload:       true,
			expectBQToolInRequest:   true,
			expectToolResultMessage: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()
			harness := newDataSourceHTTPHarness(t, tc.baseBigQueryEnabled, false)
			t.Cleanup(harness.cleanup)

			runID := createRunWithDataSource(t, harness.baseURL, harness.thread.ID, tc.useMessagePayload, "bigquery", tc.overrideBigQueryEnabled)
			run, err := waitForRunTerminalState(context.Background(), harness.store, runID)
			if err != nil {
				t.Fatalf("waitForRunTerminalState: %v", err)
			}
			wantStatus := store.RunStatusCompleted
			if tc.expectToolResultMessage {
				// A failed query attempt is durable unfinished work. The run must
				// park for an alternative plan instead of accepting the provider's
				// later generic final answer and abandoning the analytics task.
				wantStatus = store.RunStatusWaitingUser
			}
			if run.Status != wantStatus {
				t.Fatalf("run.Status = %q, want %s", run.Status, wantStatus)
			}
			waitForRunStreamClosed(t, harness.manager, runID)

			if got := harness.spy.bqToolOnFirstRequest(); got != tc.expectBQToolInRequest {
				t.Fatalf("first provider request includes run_bigquery_query = %v, want %v", got, tc.expectBQToolInRequest)
			}

			messages, err := harness.store.ListMessages(context.Background(), store.ListMessagesParams{
				ThreadID: harness.thread.ID,
				Limit:    20,
			})
			if err != nil {
				t.Fatalf("ListMessages: %v", err)
			}

			toolMessages := filterMessagesByRole(messages, types.RoleTool)
			if tc.expectToolResultMessage {
				if len(toolMessages) != 1 {
					t.Fatalf("len(toolMessages)=%d, want 1", len(toolMessages))
				}
				env, ok := types.ParseToolResultEnvelope(strings.TrimSpace(*toolMessages[0].Content))
				if !ok {
					t.Fatalf("tool message has invalid tool envelope: %#v", toolMessages[0])
				}
				if env.ToolName != "run_bigquery_query" {
					t.Fatalf("tool message tool_name=%q, want run_bigquery_query", env.ToolName)
				}
				if env.OK {
					t.Fatalf("tool execution should not succeed in this e2e harness, got ok=true")
				}
				result, ok := env.Result.(map[string]any)
				if !ok {
					t.Fatalf("tool result shape=%T", env.Result)
				}
				errMsg, ok := result["error"].(string)
				if !ok {
					t.Fatalf("tool result missing error, got %#v", result)
				}
				if !strings.Contains(strings.ToLower(errMsg), "configured") {
					t.Fatalf("tool result error=%q, want BigQuery tool to be attempted (expected BigQuery not configured)", errMsg)
				}
			} else {
				if len(toolMessages) != 0 {
					t.Fatalf("len(toolMessages)=%d, want 0", len(toolMessages))
				}
			}
		})
	}
}

func TestHandleCreateRunSpreadsheetAttachmentSendsVisibleSheetContextAndNativeFile(t *testing.T) {
	t.Parallel()

	cfgDir := t.TempDir()
	var providerBody string
	var providerReq map[string]any
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/responses") {
			http.NotFound(w, r)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read provider request: %v", err)
		}
		providerBody = string(raw)
		if err := json.Unmarshal(raw, &providerReq); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		hasCapTable := strings.Contains(providerBody, "Founder Name (1)") &&
			strings.Contains(providerBody, "1234567") &&
			strings.Contains(providerBody, "0.46212865607403242")
		answer := "missing cap table"
		if hasCapTable {
			answer = "Founder Name (1) holds 1,234,567 shares."
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = fmt.Fprint(w, "event: response.completed\n")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"rid-file-e2e\",\"output_text\":%q}}\n\n", answer)
	}))
	defer providerServer.Close()

	cfg, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	settings := cfg.Get()
	profileID := "local-noauth"
	settings.Provider = config.ProviderSelection{
		ProviderID: "compatible",
		Model:      "test-model",
		ProfileID:  &profileID,
	}
	settings.ProviderConfigs = []config.ProviderConfig{{
		ID:            profileID,
		DisplayName:   "Local no-auth provider",
		ProviderID:    "compatible",
		Model:         "test-model",
		ApiType:       strPtr("responses"),
		BaseURL:       strPtr(providerServer.URL + "/v1"),
		AuthType:      strPtr("none"),
		ModelInBody:   boolPtr(true),
		ModelInPath:   boolPtr(false),
		FileInputMode: strPtr("native_supported"),
	}}
	settings.ActiveProviderConfigID = &profileID
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("cfg.Save: %v", err)
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
	search, err := searchsvc.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("search.NewService: %v", err)
	}
	systemThreads := searchsvc.NewSystemThreadRegistry(cfgDir)
	manager := optionc.NewManager(
		st,
		cfg,
		providers.NewFactory(cfg, sec),
		tools.NewRegistry(&tools.RegistryOptions{Store: st, Search: search, CfgDir: cfgDir}),
		ps,
		nil,
		nil,
		nil,
		cfgDir,
		nil,
	)
	handler := New(Deps{
		Store:         st,
		Manager:       manager,
		Projects:      ps,
		Search:        search,
		SystemThreads: systemThreads,
		CfgDir:        cfgDir,
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	thread, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	artifactID := types.NewUUID()
	rawRel := filepath.ToSlash(filepath.Join("thread_uploads", thread.ID, artifactID+".xlsx"))
	rootRel := filepath.ToSlash(filepath.Join("thread_uploads", thread.ID, artifactID+".extract"))
	writeTestFile(t, cfgDir, rawRel, "fake workbook bytes")
	writeTestFile(t, cfgDir, filepath.ToSlash(filepath.Join(rootRel, "index", "workbook.md")), "Workbook index\n\n## Lists\nPreview: `worksheets/sheet1/preview.md`\n\n## Cap Table\nPreview: `worksheets/sheet3/preview.md`")
	writeTestFile(t, cfgDir, filepath.ToSlash(filepath.Join(rootRel, "workbook.json")), `{"sheets":[{"name":"Lists","state":"hidden","previewPath":"worksheets/sheet1/preview.md"},{"name":"Cap Table","state":"visible","previewPath":"worksheets/sheet3/preview.md"}]}`)
	writeTestFile(t, cfgDir, filepath.ToSlash(filepath.Join(rootRel, "worksheets", "sheet1", "preview.md")), "# Lists\n\nLISTS_SENTINEL currency codes only")
	writeTestFile(t, cfgDir, filepath.ToSlash(filepath.Join(rootRel, "worksheets", "sheet3", "preview.md")), "# Cap Table\n\n| Row | B | C | D | E | F | G |\n| --- | --- | --- | --- | --- | --- | --- |\n| 18 | Shareholder | Ordinary Shares | Other Share class | Total | % Non-Diluted Ownership | % Diluted Ownership |\n| 19 | Founder Name (1) | 1234567 | 0 | 1234567 | 0.46212865607403242 | 0.45884227869262506 |\n\nPrice per share: 1.672481")

	toolName := types.ThreadFileAttachmentToolName
	okVal := true
	if _, err := st.InsertArtifact(context.Background(), store.Artifact{
		ID:          artifactID,
		ThreadID:    thread.ID,
		Type:        "file",
		Title:       "model.xlsx",
		ToolName:    &toolName,
		OK:          &okVal,
		Storage:     "file",
		ContentPath: &rawRel,
		ByteSize:    int64(len("fake workbook bytes")),
		Metadata: map[string]any{
			"source":             types.ChatUploadAttachmentSource,
			"mimeType":           "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"normalizedMimeType": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"fileKind":           "spreadsheet",
			"isText":             false,
			"previewAvailable":   true,
			"previewText":        "Binary preview omitted.",
			"capabilities": map[string]any{
				"canUseNativeDocument":     true,
				"canExtractStructuredData": true,
				"canPreviewText":           true,
			},
			"extraction": map[string]any{
				"kind":         "spreadsheet",
				"rootPath":     rootRel,
				"markdownPath": filepath.ToSlash(filepath.Join(rootRel, "index", "workbook.md")),
				"workbookPath": filepath.ToSlash(filepath.Join(rootRel, "workbook.json")),
				"sheetOutputs": []any{
					filepath.ToSlash(filepath.Join(rootRel, "worksheets", "sheet1", "preview.md")),
					filepath.ToSlash(filepath.Join(rootRel, "worksheets", "sheet1", "cells-0001.jsonl")),
					filepath.ToSlash(filepath.Join(rootRel, "worksheets", "sheet3", "preview.md")),
				},
			},
		},
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	runID := createRunWithBody(t, server.URL, thread.ID, map[string]any{
		"content": "How many shares does Founder Name (1) hold?",
		"file_attachments": []any{
			map[string]any{"artifact_id": artifactID},
		},
		"referenced_artifact_ids": []any{artifactID},
		"search_depth":            "shallow",
	})
	run, err := waitForRunTerminalState(context.Background(), st, runID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status = %q, want completed; error=%v", run.Status, run.ErrorMessage)
	}
	waitForRunStreamClosed(t, manager, runID)

	if !strings.Contains(providerBody, "Founder Name (1)") || !strings.Contains(providerBody, "1234567") || !strings.Contains(providerBody, "0.46212865607403242") {
		t.Fatalf("provider request missing visible Cap Table preview: %s", providerBody)
	}
	if strings.Index(providerBody, "Founder Name (1)") > strings.Index(providerBody, "LISTS_SENTINEL") {
		t.Fatalf("visible sheet preview should precede hidden Lists preview: %s", providerBody)
	}
	if !strings.Contains(providerBody, `"type":"input_file"`) {
		t.Fatalf("provider request missing native input_file block: %s", providerBody)
	}
	toolNames := responseToolNames(providerReq)
	for _, blocked := range []string{"list_files", "read_file", "search_files", "get_artifact", "read_attachment"} {
		if containsString(toolNames, blocked) {
			t.Fatalf("provider request exposed %s despite chat file attachment/no project; tools=%v", blocked, toolNames)
		}
	}

	messages, err := st.ListMessages(context.Background(), store.ListMessagesParams{ThreadID: thread.ID, Limit: 20})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	assistants := filterMessagesByRole(messages, types.RoleAssistant)
	if len(assistants) == 0 || assistants[len(assistants)-1].Content == nil || !strings.Contains(*assistants[len(assistants)-1].Content, "1,234,567") {
		t.Fatalf("assistant final missing expected answer; assistant messages=%#v", assistants)
	}
}

func TestHandleCreateRunConflictIncludesActiveRunID(t *testing.T) {
	t.Parallel()

	cfgDir := t.TempDir()
	cfg, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	cfgSettings := cfg.Get()
	cfgSettings.Provider.ProviderID = "compatible"
	cfgSettings.Provider.Model = "test-model"
	profileID := "default"
	cfgSettings.Provider.ProfileID = &profileID

	releaseProvider := make(chan struct{})
	var releaseProviderOnce sync.Once
	defer releaseProviderOnce.Do(func() { close(releaseProvider) })
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		select {
		case <-releaseProvider:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"id":"req-1","choices":[{"delta":{"content":"done","finish_reason":"stop"}}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer providerServer.Close()

	cfgSettings.CompatibleProfiles = append([]config.CompatibleProfile(nil), cfgSettings.CompatibleProfiles...)
	for i := range cfgSettings.CompatibleProfiles {
		cfgSettings.CompatibleProfiles[i].BaseURL = providerServer.URL
	}
	if err := cfg.Save(cfgSettings); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}

	sec, err := secrets.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	if err := sec.Init("test-pass"); err != nil && !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("secrets.Init: %v", err)
	}
	if err := sec.Unlock("test-pass"); err != nil {
		t.Fatalf("secrets.Unlock: %v", err)
	}
	if err := sec.SetCompatibleAPIKey(profileID, "test-key"); err != nil {
		t.Fatalf("sec.SetCompatibleAPIKey: %v", err)
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
	server := httptest.NewServer(handler)
	defer server.Close()

	thread, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	firstRunID := createRunWithBody(t, server.URL, thread.ID, map[string]any{
		"content": "first",
	})

	reqBody, err := json.Marshal(map[string]any{"content": "second"})
	if err != nil {
		t.Fatalf("json.Marshal second request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, server.URL+"/api/threads/"+thread.ID+"/runs", bytes.NewReader(reqBody))
	if err != nil {
		t.Fatalf("http.NewRequest second request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST second run request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		t.Fatalf("second create run status=%d body=%s, want 409", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode conflict response: %v", err)
	}
	if got := strings.TrimSpace(fmt.Sprintf("%v", payload["active_run_id"])); got != firstRunID {
		t.Fatalf("active_run_id=%q, want %q", got, firstRunID)
	}

	releaseProviderOnce.Do(func() { close(releaseProvider) })
	if _, err := waitForRunTerminalState(context.Background(), st, firstRunID); err != nil {
		t.Fatalf("waitForRunTerminalState first run: %v", err)
	}
	waitForRunStreamClosed(t, manager, firstRunID)
}

func TestHandleCreateRunDatabricksToggleControlsToolExposure(t *testing.T) {
	cases := []struct {
		name                    string
		baseBigQueryEnabled     bool
		baseDatabricksEnabled   bool
		overrideDatabricks      bool
		useMessagePayload       bool
		expectDBToolInRequest   bool
		expectToolResultMessage bool
	}{
		{
			name:                    "top-level override can enable databricks tool",
			baseBigQueryEnabled:     false,
			baseDatabricksEnabled:   false,
			overrideDatabricks:      true,
			useMessagePayload:       false,
			expectDBToolInRequest:   true,
			expectToolResultMessage: true,
		},
		{
			name:                    "top-level override can disable pre-enabled databricks tool",
			baseBigQueryEnabled:     false,
			baseDatabricksEnabled:   true,
			overrideDatabricks:      false,
			useMessagePayload:       false,
			expectDBToolInRequest:   false,
			expectToolResultMessage: false,
		},
		{
			name:                    "message payload override can enable databricks tool",
			baseBigQueryEnabled:     false,
			baseDatabricksEnabled:   false,
			overrideDatabricks:      true,
			useMessagePayload:       true,
			expectDBToolInRequest:   true,
			expectToolResultMessage: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Helper()
			harness := newDataSourceHTTPHarness(t, tc.baseBigQueryEnabled, tc.baseDatabricksEnabled)
			t.Cleanup(harness.cleanup)

			runID := createRunWithDataSource(t, harness.baseURL, harness.thread.ID, tc.useMessagePayload, "databricks", tc.overrideDatabricks)
			run, err := waitForRunTerminalState(context.Background(), harness.store, runID)
			if err != nil {
				t.Fatalf("waitForRunTerminalState: %v", err)
			}
			wantStatus := store.RunStatusCompleted
			if tc.expectToolResultMessage {
				wantStatus = store.RunStatusWaitingUser
			}
			if run.Status != wantStatus {
				t.Fatalf("run.Status = %q, want %s", run.Status, wantStatus)
			}
			waitForRunStreamClosed(t, harness.manager, runID)

			if got := harness.spy.databricksToolOnFirstRequest(); got != tc.expectDBToolInRequest {
				t.Fatalf("first provider request includes run_databricks_query = %v, want %v", got, tc.expectDBToolInRequest)
			}

			messages, err := harness.store.ListMessages(context.Background(), store.ListMessagesParams{
				ThreadID: harness.thread.ID,
				Limit:    20,
			})
			if err != nil {
				t.Fatalf("ListMessages: %v", err)
			}

			toolMessages := filterMessagesByRole(messages, types.RoleTool)
			if tc.expectToolResultMessage {
				if len(toolMessages) != 1 {
					t.Fatalf("len(toolMessages)=%d, want 1", len(toolMessages))
				}
				env, ok := types.ParseToolResultEnvelope(strings.TrimSpace(*toolMessages[0].Content))
				if !ok {
					t.Fatalf("tool message has invalid tool envelope: %#v", toolMessages[0])
				}
				if env.ToolName != "run_databricks_query" {
					t.Fatalf("tool message tool_name=%q, want run_databricks_query", env.ToolName)
				}
				if env.OK {
					t.Fatalf("tool execution should not succeed in this e2e harness, got ok=true")
				}
				result, ok := env.Result.(map[string]any)
				if !ok {
					t.Fatalf("tool result shape=%T", env.Result)
				}
				errMsg, ok := result["error"].(string)
				if !ok {
					t.Fatalf("tool result missing error, got %#v", result)
				}
				if !strings.Contains(strings.ToLower(errMsg), "databricks") {
					t.Fatalf("tool result error=%q, want Databricks tool attempt failure", errMsg)
				}
			} else {
				if len(toolMessages) != 0 {
					t.Fatalf("len(toolMessages)=%d, want 0", len(toolMessages))
				}
			}
		})
	}
}

func TestHandleCreateRunPersistsReferencedMessageIDs(t *testing.T) {
	harness := newDataSourceHTTPHarness(t, false, false)
	t.Cleanup(harness.cleanup)

	referenced := make([]string, 0, 5)
	for i := 1; i <= 5; i++ {
		content := fmt.Sprintf("context-%d", i)
		msg, err := harness.store.AppendMessage(context.Background(), harness.thread.ID, nil, store.Message{
			Role:    types.RoleUser,
			Content: &content,
		})
		if err != nil {
			t.Fatalf("AppendMessage(%d): %v", i, err)
		}
		referenced = append(referenced, msg.ID)
	}

	runID := createRunWithBody(t, harness.baseURL, harness.thread.ID, map[string]any{
		"content":              "run an analytics check",
		"referencedMessageIds": []string{referenced[0], referenced[1], referenced[0]},
		"referenced_message_ids": []string{
			referenced[2],
		},
		"message": map[string]any{
			"referencedMessageIds": []string{referenced[1], referenced[3]},
			"referenced_message_ids": []string{
				referenced[4], referenced[3],
			},
		},
	})
	if strings.TrimSpace(runID) == "" {
		t.Fatalf("expected run id from create response")
	}

	run, err := waitForRunTerminalState(context.Background(), harness.store, runID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status=%q, want completed", run.Status)
	}
	waitForRunStreamClosed(t, harness.manager, runID)

	messages, err := harness.store.ListMessages(context.Background(), store.ListMessagesParams{
		ThreadID: harness.thread.ID,
		Limit:    20,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var userMsg *store.Message
	for i := range messages {
		m := messages[i]
		if m.Role != types.RoleUser || m.RunID == nil || strings.TrimSpace(*m.RunID) != runID {
			continue
		}
		userMsg = &m
		break
	}
	if userMsg == nil {
		t.Fatalf("expected user message for run %s", runID)
	}
	got := strings.Join(userMsg.ReferencedMessageIDs, ",")
	want := strings.Join(referenced, ",")
	if got != want {
		t.Fatalf("referenced_message_ids=%q, want %q", got, want)
	}
}

func TestHandleCreateRunBuildsReferencedThreadContext(t *testing.T) {
	harness := newDataSourceHTTPHarness(t, false, false)
	t.Cleanup(harness.cleanup)

	ctx := context.Background()
	refA, err := harness.store.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread refA: %v", err)
	}
	refATitle := "Warehouse anomaly investigation"
	refASummary := "Investigated the warehouse anomaly and traced it to a stale bronze ingestion job."
	if err := harness.store.UpdateThreadTitleSummary(ctx, refA.ID, &refATitle, &refASummary); err != nil {
		t.Fatalf("UpdateThreadTitleSummary refA: %v", err)
	}
	if err := harness.search.SyncThread(ctx, refA.ID); err != nil {
		t.Fatalf("SyncThread refA: %v", err)
	}

	refB, err := harness.store.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread refB: %v", err)
	}
	refBTitle := "Dashboard lag follow-up"
	refBSummary := "Documented dashboard lag symptoms and linked them to late warehouse anomaly data loads."
	if err := harness.store.UpdateThreadTitleSummary(ctx, refB.ID, &refBTitle, &refBSummary); err != nil {
		t.Fatalf("UpdateThreadTitleSummary refB: %v", err)
	}
	if err := harness.search.SyncThread(ctx, refB.ID); err != nil {
		t.Fatalf("SyncThread refB: %v", err)
	}

	runID := createRunWithBody(t, harness.baseURL, harness.thread.ID, map[string]any{
		"content":             "Use the other threads to explain the warehouse anomaly.",
		"referencedThreadIds": []string{refA.ID, refB.ID, refA.ID},
		"referenced_thread_ids": []string{
			harness.thread.ID,
		},
	})
	if strings.TrimSpace(runID) == "" {
		t.Fatalf("expected run id from create response")
	}

	run, err := waitForRunTerminalState(ctx, harness.store, runID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status=%q, want completed", run.Status)
	}
	waitForRunStreamClosed(t, harness.manager, runID)

	messages, err := harness.store.ListMessages(ctx, store.ListMessagesParams{
		ThreadID: harness.thread.ID,
		Limit:    20,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var userMsg *store.Message
	for i := range messages {
		m := messages[i]
		if m.Role != types.RoleUser || m.RunID == nil || strings.TrimSpace(*m.RunID) != runID {
			continue
		}
		userMsg = &m
		break
	}
	if userMsg == nil {
		t.Fatalf("expected user message for run %s", runID)
	}
	if got := strings.Join(userMsg.ReferencedThreadIDs, ","); got != strings.Join([]string{refA.ID, refB.ID}, ",") {
		t.Fatalf("referenced_thread_ids=%q, want %q", got, strings.Join([]string{refA.ID, refB.ID}, ","))
	}
	if userMsg.HiddenContext == nil {
		t.Fatalf("HiddenContext=nil, want referenced thread context")
	}
	hidden := strings.TrimSpace(*userMsg.HiddenContext)
	if !strings.Contains(hidden, refATitle) || !strings.Contains(hidden, refBTitle) {
		t.Fatalf("HiddenContext=%q, want referenced thread titles", hidden)
	}
	if !strings.Contains(hidden, "doc_id=thread:"+refA.ID) {
		t.Fatalf("HiddenContext=%q, want thread doc provenance for %s", hidden, refA.ID)
	}
}

func TestMessageToUIIncludesIDSeqAndReferences(t *testing.T) {
	content := "hello"
	runID := "run-1"
	msg := store.Message{
		ID:                   "msg-1",
		Seq:                  7,
		RunID:                &runID,
		Role:                 types.RoleAssistant,
		Content:              &content,
		ReferencedMessageIDs: []string{"msg-2", "msg-3"},
		ReferencedThreadIDs:  []string{"thread-2", "thread-3"},
		ReferencedArtifactIDs: []string{
			"artifact-1",
		},
	}

	out := messageToUI(msg, map[string]store.Thread{
		"thread-2": {ID: "thread-2", Title: "Thread two"},
		"thread-3": {ID: "thread-3", Title: "Thread three"},
	})
	if got, ok := out["id"].(string); !ok || got != "msg-1" {
		t.Fatalf("id=%#v, want msg-1", out["id"])
	}
	if got, ok := out["seq"].(int64); !ok || got != 7 {
		t.Fatalf("seq=%#v, want 7", out["seq"])
	}
	if got, ok := out["run_id"].(string); !ok || got != "run-1" {
		t.Fatalf("run_id=%#v, want run-1", out["run_id"])
	}
	if got, ok := out["referenced_message_ids"].([]string); !ok || strings.Join(got, ",") != "msg-2,msg-3" {
		t.Fatalf("referenced_message_ids=%#v, want [msg-2 msg-3]", out["referenced_message_ids"])
	}
	if got, ok := out["referenced_thread_ids"].([]string); !ok || strings.Join(got, ",") != "thread-2,thread-3" {
		t.Fatalf("referenced_thread_ids=%#v, want [thread-2 thread-3]", out["referenced_thread_ids"])
	}
	if got, ok := out["referenced_threads"].([]map[string]any); !ok || len(got) != 2 {
		t.Fatalf("referenced_threads=%#v, want 2 thread DTOs", out["referenced_threads"])
	}
	if got, ok := out["referenced_artifact_ids"].([]string); !ok || strings.Join(got, ",") != "artifact-1" {
		t.Fatalf("referenced_artifact_ids=%#v, want [artifact-1]", out["referenced_artifact_ids"])
	}
}

type dataSourceHarness struct {
	baseURL string
	store   store.Store
	thread  store.Thread
	search  *searchsvc.Service
	manager *optionc.Manager
	spy     *mockProviderSpy
	server  *httptest.Server
	cleanup func()
}

func newDataSourceHTTPHarness(t *testing.T, baseBigQueryEnabled bool, baseDatabricksEnabled bool) dataSourceHarness {
	t.Helper()

	cfgDir := t.TempDir()
	cfg, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	cfgSettings := cfg.Get()
	cfgSettings.Provider.ProviderID = "compatible"
	cfgSettings.Provider.Model = "test-model"
	profileID := "default"
	cfgSettings.Provider.ProfileID = &profileID
	cfgSettings.MaxToolIterations = intPtr(5)
	cfgSettings.DataSources = &config.DatasourceConnections{
		BigQuery: &config.DatasourceConnectionState{
			Enabled: boolPtr(baseBigQueryEnabled),
		},
		Databricks: &config.DatasourceConnectionState{
			Enabled:     boolPtr(baseDatabricksEnabled),
			LastCheckOk: boolPtr(true),
		},
	}

	spy := &mockProviderSpy{}
	providerServer := httptest.NewServer(mockCompatibleProviderHandler(spy))
	cfgSettings.CompatibleProfiles = append([]config.CompatibleProfile(nil), cfgSettings.CompatibleProfiles...)
	for i := range cfgSettings.CompatibleProfiles {
		cfgSettings.CompatibleProfiles[i].BaseURL = providerServer.URL
	}
	if err := cfg.Save(cfgSettings); err != nil {
		t.Fatalf("cfg.Save: %v", err)
	}

	sec, err := secrets.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	if err := sec.Init("test-pass"); err != nil && !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("secrets.Init: %v", err)
	}
	if err := sec.Unlock("test-pass"); err != nil {
		t.Fatalf("secrets.Unlock: %v", err)
	}
	if err := sec.SetCompatibleAPIKey(profileID, "test-key"); err != nil {
		t.Fatalf("secrets.SetCompatibleAPIKey: %v", err)
	}

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	search, err := searchsvc.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("search.NewService: %v", err)
	}
	systemThreads := searchsvc.NewSystemThreadRegistry(cfgDir)
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
		Store:         st,
		Manager:       manager,
		Projects:      ps,
		Search:        search,
		SystemThreads: systemThreads,
		CfgDir:        cfgDir,
	})
	server := httptest.NewServer(handler)

	thread, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		server.Close()
		t.Fatalf("CreateThread: %v", err)
	}

	return dataSourceHarness{
		baseURL: server.URL,
		store:   st,
		thread:  thread,
		search:  search,
		manager: manager,
		spy:     spy,
		server:  server,
		cleanup: func() {
			server.Close()
			providerServer.Close()
		},
	}
}

func createRunWithDataSource(t *testing.T, baseURL, threadID string, useMessagePayload bool, datasource string, enabled bool) string {
	t.Helper()

	dataSources := map[string]any{
		datasource: map[string]any{
			"enabled": enabled,
		},
	}
	body := map[string]any{
		"content": "run an analytics check",
	}
	if useMessagePayload {
		delete(body, "content")
		body["message"] = map[string]any{
			"content":      "run an analytics check",
			"dataSources":  dataSources,
			"searchDepth":  "shallow",
			"search_depth": "shallow",
		}
	} else {
		body["dataSources"] = dataSources
	}

	return createRunWithBody(t, baseURL, threadID, body)
}

func createRunWithBody(t *testing.T, baseURL, threadID string, body map[string]any) string {
	t.Helper()

	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/threads/"+threadID+"/runs", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST run request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		t.Fatalf("create run status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var parsed struct {
		RunID string `json:"runId"`
		Run   struct {
			ID string `json:"id"`
		} `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if parsed.RunID == "" {
		parsed.RunID = strings.TrimSpace(parsed.Run.ID)
	}
	if parsed.RunID == "" {
		t.Fatalf("missing runId in create response")
	}
	return parsed.RunID
}

func writeTestFile(t *testing.T, cfgDir string, rel string, content string) {
	t.Helper()
	full := filepath.Join(cfgDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", full, err)
	}
}

func responseToolNames(req map[string]any) []string {
	rawTools, _ := req["tools"].([]any)
	out := make([]string, 0, len(rawTools))
	for _, rawTool := range rawTools {
		tool, _ := rawTool.(map[string]any)
		if name, _ := tool["name"].(string); strings.TrimSpace(name) != "" {
			out = append(out, strings.TrimSpace(name))
			continue
		}
		if fn, _ := tool["function"].(map[string]any); fn != nil {
			if name, _ := fn["name"].(string); strings.TrimSpace(name) != "" {
				out = append(out, strings.TrimSpace(name))
			}
		}
	}
	return out
}

func containsString(values []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}

func filterMessagesByRole(msgs []store.Message, role types.Role) []store.Message {
	out := make([]store.Message, 0)
	for _, m := range msgs {
		if m.Role == role {
			out = append(out, m)
		}
	}
	return out
}

type mockProviderSpy struct {
	mu                 sync.Mutex
	firstCallHasBQTool bool
	firstCallHasDBTool bool
	callCount          int
}

func (s *mockProviderSpy) bqToolOnFirstRequest() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstCallHasBQTool
}

func (s *mockProviderSpy) databricksToolOnFirstRequest() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstCallHasDBTool
}

func mockCompatibleProviderHandler(spy *mockProviderSpy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Tools []map[string]any `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			tErr := errors.New("invalid request body")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": tErr.Error()})
			return
		}
		defer r.Body.Close()

		hasBQ := false
		hasDB := false
		for _, item := range req.Tools {
			fn, ok := item["function"].(map[string]any)
			if !ok {
				continue
			}
			name, ok := fn["name"].(string)
			if !ok {
				continue
			}
			switch strings.TrimSpace(name) {
			case "run_bigquery_query":
				hasBQ = true
			case "run_databricks_query":
				hasDB = true
			}
		}

		spy.mu.Lock()
		spy.callCount++
		callID := spy.callCount
		if callID == 1 {
			spy.firstCallHasBQTool = hasBQ
		}
		if callID == 1 {
			spy.firstCallHasDBTool = hasDB
		}
		spy.mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		f, _ := w.(http.Flusher)

		if callID == 1 && hasBQ {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`data: {"id":"req-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"tc-bq-1","function":{"name":"run_bigquery_query","arguments":"{\"sql\":\"SELECT 1\"}"}}]}}]}` + "\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if f != nil {
				f.Flush()
			}
			return
		}
		if callID == 1 && hasDB {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`data: {"id":"req-1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"tc-dbr-1","function":{"name":"run_databricks_query","arguments":"{\"sql\":\"SELECT 1\"}"}}]}}]}` + "\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if f != nil {
				f.Flush()
			}
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`data: {"id":"req-1","choices":[{"delta":{"content":"analysis complete","finish_reason":"stop"}}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if f != nil {
			f.Flush()
		}
	})
}

func waitForRunTerminalState(ctx context.Context, st store.Store, runID string) (store.Run, error) {
	deadline := time.Now().Add(8 * time.Second)
	for {
		run, err := st.GetRun(ctx, runID)
		if err != nil {
			return run, err
		}
		if run.Status == store.RunStatusCompleted || run.Status == store.RunStatusFailed ||
			run.Status == store.RunStatusCancelled || run.Status == store.RunStatusWaitingUser {
			return run, nil
		}
		if time.Now().After(deadline) {
			return run, context.DeadlineExceeded
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForRunStreamClosed(t *testing.T, manager *optionc.Manager, runID string) {
	t.Helper()
	ch, cancel, err := manager.Subscribe(runID)
	if errors.Is(err, store.ErrNotFound) {
		// Completed and parked cross-process runs are served from the durable
		// HTTP tail, so no local stream is expected after the durable state
		// assertion made by each caller above.
		return
	}
	if err != nil {
		t.Fatalf("Subscribe(%s): %v", runID, err)
	}
	defer cancel()

	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline.C:
			t.Fatalf("run stream %s did not close", runID)
		}
	}
}

func boolPtr(v bool) *bool {
	return &v
}

func intPtr(v int) *int {
	return &v
}

func strPtr(v string) *string {
	return &v
}
