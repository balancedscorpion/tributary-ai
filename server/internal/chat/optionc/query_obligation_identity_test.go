package optionc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/permissions"
)

type failOnceListMessagesStore struct {
	store.Store
	remainingFailures int
}

type failDependencyUpsertRepository struct {
	store.LongHorizonRepository
	remainingFailures int
}

func (r *failDependencyUpsertRepository) UpsertRunQueryDependency(ctx context.Context, dependency store.RunQueryDependency) (store.RunQueryDependency, error) {
	if r.remainingFailures > 0 {
		r.remainingFailures--
		return store.RunQueryDependency{}, errors.New("transient dependency upsert failure")
	}
	return r.LongHorizonRepository.UpsertRunQueryDependency(ctx, dependency)
}

func (s *failOnceListMessagesStore) ListMessages(ctx context.Context, params store.ListMessagesParams) ([]store.Message, error) {
	if s.remainingFailures > 0 {
		s.remainingFailures--
		return nil, errors.New("transient message read failure")
	}
	return s.Store.ListMessages(ctx, params)
}

// Status payloads and result-artifact metadata may only surface an obligation
// handle whose (run, root) identity is durable-truthful: managed roots with an
// intact run scope. Deleted-run tombstones (blank RunID) and generic
// non-managed registry jobs must never mint one — the frontend polls with this
// key and the model retries against it.
func TestQueryJobPayloadNeverMintsUnverifiedObligation(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	baseJob := func(managed bool, runID, replaces string) *jobs.Job {
		job := &jobs.Job{
			ID: "qj-identity", Provider: "bigquery", Status: jobs.JobStatusRunning,
			Owner:       jobs.JobOwner{ThreadID: "thread-1", RunID: runID, Handle: "qown-identity"},
			SubmittedAt: now, LastAccess: now,
			ReplacesJobID: replaces,
		}
		if managed {
			job.Metadata = map[string]any{jobs.QueryObligationManagedMetadataKey: true}
		}
		return job
	}

	// Managed root with intact run scope: the one truthful self-root mint.
	scoped := baseJob(true, "run-1", "")
	wantObligation, err := store.QueryObligationIDForRoot("run-1", "qj-identity")
	if err != nil {
		t.Fatalf("derive obligation: %v", err)
	}
	payload := queryJobPayloadFromRegistry(scoped)
	if payload["query_obligation_id"] != wantObligation {
		t.Fatalf("scoped managed payload obligation=%v, want %q", payload["query_obligation_id"], wantObligation)
	}

	// Deleted-run tombstone: blank RunID must yield NO handle, never a
	// caller-run fabrication.
	tombstoned := baseJob(true, "", "")
	if payload := queryJobPayloadFromRegistry(tombstoned); payload["query_obligation_id"] != nil {
		t.Fatalf("tombstoned managed payload minted obligation %v", payload["query_obligation_id"])
	}

	// Generic non-managed registry job: not part of the obligation protocol.
	unmanaged := baseJob(false, "run-1", "")
	if payload := queryJobPayloadFromRegistry(unmanaged); payload["query_obligation_id"] != nil {
		t.Fatalf("unmanaged payload minted obligation %v", payload["query_obligation_id"])
	}

	// Managed mid-chain job without recorded metadata: self-root would be the
	// WRONG identity (root differs), so nothing is minted.
	midChain := baseJob(true, "run-1", "qj-predecessor")
	if payload := queryJobPayloadFromRegistry(midChain); payload["query_obligation_id"] != nil {
		t.Fatalf("mid-chain payload minted obligation %v", payload["query_obligation_id"])
	}

	// Valid metadata handle passes through untouched for all shapes.
	withMetadata := baseJob(true, "", "qj-predecessor")
	withMetadata.Metadata["query_obligation_id"] = wantObligation
	withMetadata.Metadata["nested"] = map[string]any{"query_obligation_id": wantObligation}
	if payload := queryJobPayloadFromRegistry(withMetadata); payload["query_obligation_id"] != wantObligation {
		t.Fatalf("metadata obligation not passed through: %v", payload["query_obligation_id"])
	}

	// The materialized result artifact is deterministic per job and served
	// across runs, so it must never advertise ANY run-scoped obligation
	// handle — not even a valid one recorded in the job's own metadata.
	result := &jobs.QueryResult{
		Rows: []map[string]any{{"n": 1}},
		Metadata: map[string]any{
			"query_obligation_id": wantObligation,
			"nested":              map[string]any{"query_obligation_id": wantObligation},
		},
	}
	for name, job := range map[string]*jobs.Job{
		"scoped":        scoped,
		"tombstoned":    tombstoned,
		"unmanaged":     unmanaged,
		"with-metadata": withMetadata,
	} {
		artifactMetadata := completedQueryResultArtifactMetadata(job, result, "qown-identity", "artifact-1")
		raw, marshalErr := json.Marshal(artifactMetadata)
		if marshalErr != nil || strings.Contains(string(raw), "query_obligation_id") || strings.Contains(string(raw), wantObligation) {
			t.Fatalf("artifact metadata for %s job advertised run-scoped obligation: err=%v payload=%s", name, marshalErr, raw)
		}
	}
}

func TestAsyncQueryModelSurfacesHideLegacyTargetsAndSharedArtifactObligations(t *testing.T) {
	t.Parallel()
	canonical := "qob-0123456789abcdef0123456789abcdef"
	legacy := "qj-private-replacement-target"
	call := types.ToolCall{
		Name: "run_databricks_query",
		Args: map[string]any{
			"sql":                 "select 1",
			"replaces_job_id":     legacy,
			"query_obligation_id": "qob-ffffffffffffffffffffffffffffffff",
			"options": map[string]any{
				"replacement_target_job_id": "qj-nested-private-target",
			},
		},
		ArgsText: `{"sql":"select 1","replaces_job_id":"` + legacy + `"}`,
	}
	out := tools.Output{
		OK:           false,
		ResultFormat: tools.ResultFormatJSON,
		Result:       map[string]any{"query_obligation_id": canonical},
		QueryMetadata: map[string]any{
			"query_obligation_id": canonical,
		},
	}
	sanitized := sanitizeAsyncQueryToolCallForModel(call.Name, call, out)
	if sanitized.Args["query_obligation_id"] != canonical || sanitized.Args["sql"] != "select 1" {
		t.Fatalf("sanitized args=%#v, want sql plus canonical obligation", sanitized.Args)
	}
	sanitizedRaw, sanitizedErr := json.Marshal(sanitized.Args)
	if _, exists := sanitized.Args["replaces_job_id"]; exists || sanitizedErr != nil || strings.Contains(sanitized.ArgsText, legacy) || strings.Contains(string(sanitizedRaw), "qj-nested-private-target") || strings.Contains(string(sanitizedRaw), "replacement_target_job_id") {
		t.Fatalf("model-facing call leaked legacy target: args=%#v args_text=%s", sanitized.Args, sanitized.ArgsText)
	}
	diagnosticOut := enrichToolErrorOutput(call.Name, sanitized, tools.Output{
		OK: false, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{"error": "retry failed"},
	})
	diagnosticRaw, diagnosticErr := json.Marshal(diagnosticOut.Result)
	if diagnosticErr != nil || strings.Contains(string(diagnosticRaw), legacy) || strings.Contains(string(diagnosticRaw), "qob-ffffffffffffffffffffffffffffffff") {
		t.Fatalf("error diagnostic leaked a caller target: err=%v result=%s", diagnosticErr, diagnosticRaw)
	}
	placeholderOut := tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result:       map[string]any{"status": "running", "job_id": "qj-placeholder"},
		QueryMetadata: map[string]any{
			"status":              "running",
			"job_id":              "qj-placeholder",
			"query_obligation_id": canonical,
			"nested":              map[string]any{"query_obligation_id": canonical},
		},
	}
	_, _, _, _, placeholderMetadata := buildArtifact("run_databricks_query", placeholderOut, `{"status":"running"}`)
	placeholderRaw, placeholderErr := json.Marshal(placeholderMetadata)
	if placeholderErr != nil || strings.Contains(string(placeholderRaw), canonical) || strings.Contains(string(placeholderRaw), "query_obligation_id") {
		t.Fatalf("shared placeholder artifact leaked run-scoped obligation: err=%v metadata=%s", placeholderErr, placeholderRaw)
	}
	statusOut := tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"status": "completed", "job_id": "qj-status", "query_obligation_id": canonical,
		},
		QueryMetadata: map[string]any{
			"status": "completed", "job_id": "qj-status", "run_id": "run-old",
			"query_obligation_id": canonical,
		},
	}
	_, statusTitle, _, statusContent, statusMetadata := buildArtifact("check_query_status", statusOut, `{"status":"completed"}`)
	statusRaw, statusErr := json.Marshal(statusMetadata)
	if statusTitle != "Query Results Ready" || statusContent != "" || statusMetadata["placeholder"] != true {
		t.Fatalf("status lifecycle artifact must remain an upgradeable placeholder: title=%q content=%q metadata=%#v", statusTitle, statusContent, statusMetadata)
	}
	if statusErr != nil || strings.Contains(string(statusRaw), canonical) || strings.Contains(string(statusRaw), "query_obligation_id") || strings.Contains(string(statusRaw), "run-old") {
		t.Fatalf("shared status artifact leaked run-scoped identity: err=%v metadata=%s", statusErr, statusRaw)
	}

	sharedOut := tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result:       map[string]any{"rows": []any{map[string]any{"n": 1}}},
		QueryMetadata: map[string]any{
			"query_obligation_id": canonical,
			"nested":              map[string]any{"query_obligation_id": canonical},
		},
		ResultMetadata: map[string]any{
			"nested":                    map[string]any{"query_obligation_id": canonical},
			"layers":                    []any{[]any{map[string]any{"query_obligation_id": canonical}}},
			"note":                      "retry " + canonical,
			canonical + "-metadata-key": "must be redacted",
		},
	}
	_, _, _, _, metadata := buildArtifact("get_query_results", sharedOut, `{"rows":[{"n":1}]}`)
	raw, err := json.Marshal(metadata)
	if err != nil || strings.Contains(string(raw), canonical) || strings.Contains(string(raw), "query_obligation_id") {
		t.Fatalf("shared result artifact leaked run-scoped obligation: err=%v metadata=%s", err, raw)
	}
}

func TestQueryRetryTargetParsingPreservesExplicitInvalidPresence(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		call              types.ToolCall
		obligationPresent bool
		obligationInvalid bool
		legacyPresent     bool
		legacyInvalid     bool
		unsupported       bool
	}{
		{name: "targetless", call: types.ToolCall{Args: map[string]any{"sql": "select 1"}}},
		{name: "args text null", call: types.ToolCall{ArgsText: `{"query_obligation_id":null}`}, obligationPresent: true, obligationInvalid: true},
		{name: "args blank", call: types.ToolCall{Args: map[string]any{"query_obligation_id": "  "}}, obligationPresent: true, obligationInvalid: true},
		{name: "legacy non-string", call: types.ToolCall{Args: map[string]any{"replaces_job_id": 42}}, legacyPresent: true, legacyInvalid: true},
		{name: "unsupported top-level alias", call: types.ToolCall{Args: map[string]any{"replacement_job_id": "qj-old"}}, unsupported: true},
		{name: "illegal concrete job identity", call: types.ToolCall{Args: map[string]any{"job_id": "qj-old"}}, unsupported: true},
		{name: "normalized lookalike is not authoritative", call: types.ToolCall{Args: map[string]any{"query-obligation-id": "qob-0123456789abcdef0123456789abcdef"}}, unsupported: true},
		{name: "nested retry identity", call: types.ToolCall{Args: map[string]any{"options": map[string]any{"query_obligation_id": "qob-0123456789abcdef0123456789abcdef"}}}, unsupported: true},
		{name: "deep array retry identity", call: types.ToolCall{Args: map[string]any{"options": []any{[]any{map[string]any{"query_obligation_id": "qob-0123456789abcdef0123456789abcdef"}}}}}, unsupported: true},
		{name: "truncated raw retry identity", call: types.ToolCall{ArgsText: `{"sql":"select 1","query_obligation_id":"qob-0123456789abcdef0123456789abcdef"`}, unsupported: true},
		{name: "args text valid", call: types.ToolCall{ArgsText: `{"query_obligation_id":"qob-0123456789abcdef0123456789abcdef"}`}, obligationPresent: true},
		{
			name: "conflicting decoded and raw",
			call: types.ToolCall{
				Args:     map[string]any{"replaces_job_id": "qj-one"},
				ArgsText: `{"replaces_job_id":"qj-two"}`,
			},
			legacyPresent: true,
			legacyInvalid: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := queryRetryTargetFromCallArgs(tc.call)
			if request.Obligation.Present != tc.obligationPresent || request.Obligation.Invalid != tc.obligationInvalid ||
				request.LegacyJob.Present != tc.legacyPresent || request.LegacyJob.Invalid != tc.legacyInvalid ||
				request.UnsupportedIdentityPresent != tc.unsupported {
				t.Fatalf("request=%#v, want obligation(present=%v invalid=%v) legacy(present=%v invalid=%v) unsupported=%v", request, tc.obligationPresent, tc.obligationInvalid, tc.legacyPresent, tc.legacyInvalid, tc.unsupported)
			}
		})
	}
}

func TestCrossRunHistoryRedactsStaleQueryRetryIdentity(t *testing.T) {
	t.Parallel()
	priorRunID := "run-prior"
	currentRunID := "run-current"
	obligationID := "qob-0123456789abcdef0123456789abcdef"
	legacyJobID := "qj-prior-private"
	priorToolName := "run_databricks_query"
	priorToolCallID := "call-prior"
	priorToolContent := `{"tool_name":"run_databricks_query","run_id":"run-prior","result":{"query_obligation_id":"` + obligationID + `","instructions":"retry query_obligation_id=` + obligationID + `","error":"replacement target [` + legacyJobID + `] is not owned by the current run","rows":[{"replacement_job_id":"business-123","run_id":"fiscal-run"}]},"query_metadata":{"replaces_job_id":"` + legacyJobID + `"}}`
	currentToolContent := `{"tool_name":"run_databricks_query","result":{"query_obligation_id":"` + obligationID + `"}}`
	currentProviderPayload := json.RawMessage(`{"signed_block":"current-run-native","query_obligation_id":"` + obligationID + `"}`)
	messages := []store.Message{
		{
			ID: "assistant-prior", RunID: &priorRunID, Role: types.RoleAssistant,
			Content: func() *string { value := "Use " + obligationID; return &value }(),
			ToolCalls: []types.ToolCall{{
				ID: priorToolCallID, Name: priorToolName,
				Args: map[string]any{
					"sql": "select 1", "query_obligation_id": obligationID,
					"replaces_job_id": legacyJobID, "job_id": legacyJobID,
				},
				ArgsText: `{"sql":"select 1","query_obligation_id":"` + obligationID + `","job_id":"` + legacyJobID + `"}`,
			}},
			ProviderPayload: json.RawMessage(`{"run_id":"run-prior","query_obligation_id":"` + obligationID + `"}`),
		},
		{
			ID: "tool-prior", RunID: &priorRunID, Role: types.RoleTool,
			Content: &priorToolContent, ToolCallID: &priorToolCallID, Name: &priorToolName,
		},
		{
			ID: "tool-current", RunID: &currentRunID, Role: types.RoleTool,
			Content: &currentToolContent, ToolCallID: &priorToolCallID, Name: &priorToolName,
			ProviderPayload: currentProviderPayload,
		},
	}
	sanitized := sanitizeStoredQueryRetryHistoryForRun(messages, currentRunID)
	priorRaw, err := json.Marshal(sanitized[:2])
	if err != nil || strings.Contains(string(priorRaw), obligationID) || strings.Contains(string(priorRaw), legacyJobID) {
		t.Fatalf("prior-run history leaked retry identity: err=%v history=%s", err, priorRaw)
	}
	if !strings.Contains(string(priorRaw), "select 1") {
		t.Fatalf("history sanitizer discarded portable SQL context: %s", priorRaw)
	}
	if !strings.Contains(string(priorRaw), "business-123") || !strings.Contains(string(priorRaw), "fiscal-run") {
		t.Fatalf("history sanitizer stripped lifecycle dataset columns: %s", priorRaw)
	}
	if len(sanitized[0].ProviderPayload) != 0 {
		t.Fatalf("stale opaque provider payload was rewritten instead of dropped: %s", sanitized[0].ProviderPayload)
	}
	currentRaw, err := json.Marshal(sanitized[2])
	if err != nil || !strings.Contains(string(currentRaw), obligationID) {
		t.Fatalf("current-run history lost its verified obligation: err=%v history=%s", err, currentRaw)
	}
	if string(sanitized[2].ProviderPayload) != string(currentProviderPayload) {
		t.Fatalf("current-run provider payload changed: got=%s want=%s", sanitized[2].ProviderPayload, currentProviderPayload)
	}

	checkpoint := &managerContextCheckpoint{
		RunID: &priorRunID,
		ReplacementHistory: []types.ChatMessage{{
			Role: types.RoleTool, Content: &priorToolContent, Name: priorToolName,
			ProviderPayload: json.RawMessage(`{"signed_block":"stale-native","query_obligation_id":"` + obligationID + `"}`),
		}},
		Summary: "Continue " + obligationID + " after replacement target [" + legacyJobID + "] is not owned by the current run",
		Plan:    RunContextPlan{SummarizedBuckets: []ContextSummaryBucket{{Summary: "Retry " + obligationID + " with replacement_job_id=" + legacyJobID}}},
	}
	cleanCheckpoint := sanitizeQueryRetryCheckpointForRun(checkpoint, currentRunID)
	checkpointRaw, err := json.Marshal(cleanCheckpoint)
	if err != nil || strings.Contains(string(checkpointRaw), obligationID) || strings.Contains(string(checkpointRaw), legacyJobID) {
		t.Fatalf("prior-run checkpoint leaked retry identity: err=%v checkpoint=%s", err, checkpointRaw)
	}
	if len(cleanCheckpoint.ReplacementHistory[0].ProviderPayload) != 0 {
		t.Fatalf("stale checkpoint retained opaque provider payload: %s", cleanCheckpoint.ReplacementHistory[0].ProviderPayload)
	}
	originalRaw, _ := json.Marshal(checkpoint)
	if !strings.Contains(string(originalRaw), obligationID) {
		t.Fatal("checkpoint sanitizer mutated the stored source checkpoint")
	}

	businessCall := sanitizeStaleQueryRetryToolCall(types.ToolCall{
		Name: "display_table",
		Args: map[string]any{"replacement_job_id": "business-123", "run_id": "fiscal-run", "note": obligationID},
	})
	if businessCall.Args["replacement_job_id"] != "business-123" || businessCall.Args["run_id"] != "fiscal-run" {
		t.Fatalf("non-query tool args were structurally stripped: %#v", businessCall.Args)
	}
	businessRaw, marshalErr := json.Marshal(businessCall.Args)
	if marshalErr != nil || strings.Contains(string(businessRaw), obligationID) {
		t.Fatalf("non-query tool args retained stale qob: err=%v args=%#v", marshalErr, businessCall.Args)
	}
	businessContent := `{"tool_name":"display_table","result":{"rows":[{"replacement_job_id":"business-123","target_job_id":"sales-target","run_id":"fiscal-run","note":"` + obligationID + `"}]}}`
	sanitizedBusinessContent := sanitizeStaleQueryRetryContent(&businessContent, true, "display_table")
	if sanitizedBusinessContent == nil || !strings.Contains(*sanitizedBusinessContent, "business-123") ||
		!strings.Contains(*sanitizedBusinessContent, "sales-target") || !strings.Contains(*sanitizedBusinessContent, "fiscal-run") ||
		strings.Contains(*sanitizedBusinessContent, obligationID) {
		t.Fatalf("non-query business payload was corrupted or leaked qob: %#v", sanitizedBusinessContent)
	}
}

func TestThreadSummaryDoesNotInjectStaleQueryRetryAuthority(t *testing.T) {
	obligationID := "qob-0123456789abcdef0123456789abcdef"
	summaryValue := "Continue the analysis with " + obligationID + ", replacement_job_id=qj-prior-private, after replacement target [qj-prior-private] is not owned by the current run. Keep replacement_job_id=business-123 as a dataset value."
	prompt := appendSanitizedThreadSummary("base prompt", &summaryValue)
	if strings.Contains(prompt, obligationID) || strings.Contains(prompt, "qj-prior-private") {
		t.Fatalf("thread summary injected stale retry authority: %q", prompt)
	}
	if !strings.Contains(prompt, "Continue the analysis") || !strings.Contains(prompt, "Thread summary:") {
		t.Fatalf("thread summary sanitizer discarded portable context: %q", prompt)
	}
	if !strings.Contains(prompt, "replacement_job_id=business-123") {
		t.Fatalf("thread summary sanitizer stripped a legitimate business field: %q", prompt)
	}
	if !strings.Contains(summaryValue, obligationID) || !strings.Contains(summaryValue, "qj-prior-private") {
		t.Fatal("thread summary sanitizer mutated stored source text")
	}
}

func TestStartRunSanitizesPinnedHiddenQueryAuthority(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	manager, repository := newStartRunManagerForSSE(t, t.TempDir(), srv.URL)
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	obligationID := "qob-0123456789abcdef0123456789abcdef"
	visible := "Visible user text keeps " + obligationID + " and replacement_job_id=qj-visible because visible content is not hidden authority."
	hidden := strings.Join([]string{
		"Pinned prior query block: " + obligationID,
		"replacement_job_id=qj-prior-private",
		"retry_target=nil",
		"target_job_id=00000-00000",
		"replacement target [qj-prior-private] is not owned by the current run",
		"Dataset column replacement_job_id=business-123 must remain.",
	}, "\n")
	started, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID, Content: visible, HiddenContext: hidden,
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if run, waitErr := waitForRunTerminalState(ctx, repository, started.Run.ID); waitErr != nil || run.Status != store.RunStatusCompleted {
		t.Fatalf("wait for run: status=%q err=%v", run.Status, waitErr)
	}
	messages, err := repository.ListMessages(ctx, store.ListMessagesParams{
		ThreadID: thread.ID, Limit: 100, IncludeHidden: true, OrderBy: "seq_asc",
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	var persisted *store.Message
	for index := range messages {
		message := &messages[index]
		if message.RunID != nil && *message.RunID == started.Run.ID && message.Role == types.RoleUser {
			persisted = message
			break
		}
	}
	if persisted == nil || persisted.Content == nil || *persisted.Content != visible {
		t.Fatalf("visible user content was changed: %#v", persisted)
	}
	if persisted.HiddenContext == nil {
		t.Fatal("sanitized hidden context was not persisted")
	}
	clean := *persisted.HiddenContext
	for _, forbidden := range []string{obligationID, "qj-prior-private", "retry_target=nil", "target_job_id=00000-00000"} {
		if strings.Contains(clean, forbidden) {
			t.Fatalf("current-run hidden context retained stale query authority %q: %s", forbidden, clean)
		}
	}
	if !strings.Contains(clean, "replacement_job_id=business-123") {
		t.Fatalf("hidden-context scrub stripped legitimate business prose: %s", clean)
	}
}

func TestManagedQueryRunOutputRepairsMissingZeroAndNonexistentIdentities(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	manager := &Manager{store: repository, longHorizon: repository}
	cases := []struct {
		name      string
		claimedID any
	}{
		{name: "nil", claimedID: nil},
		{name: "all-zero native sentinel", claimedID: "00000-00000"},
		{name: "all-zero managed sentinel", claimedID: "qj-0000-0000"},
		{name: "well-formed nonexistent managed id", claimedID: "qj-dbx-missing123"},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run, createErr := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
			if createErr != nil {
				t.Fatalf("CreateRun: %v", createErr)
			}
			result := map[string]any{"message": "query started", "job_id": tc.claimedID}
			out := tools.Output{OK: true, ResultFormat: tools.ResultFormatJSON, Result: result, QueryMetadata: map[string]any{"job_id": tc.claimedID}}
			call := types.ToolCall{
				ID: "call-invalid-identity-" + string(rune('a'+index)), Name: "run_databricks_query",
				Args: map[string]any{"sql": "select 1"}, ArgsText: `{"sql":"select 1"}`,
			}
			manager.processAsyncQueryRunOutput(ctx, thread, run, call, call.Name, "", &out)
			if out.OK {
				t.Fatalf("invalid identity output remained successful: %#v", out)
			}
			syntheticID := extractAsyncQueryJobID(out)
			if !validManagedQueryJobID(syntheticID) || syntheticID == strings.TrimSpace(stringMapValue(result, "job_id")) {
				t.Fatalf("recovery identity=%q output=%#v", syntheticID, out)
			}
			raw, marshalErr := json.Marshal(out)
			if marshalErr != nil {
				t.Fatalf("marshal output: %v", marshalErr)
			}
			if claimed, ok := tc.claimedID.(string); ok && claimed != "" && strings.Contains(string(raw), claimed) {
				t.Fatalf("output retained unusable identity %q: %s", claimed, raw)
			}
			obligationID := firstStringMapValue(out.QueryMetadata, "query_obligation_id")
			if err := store.ValidateQueryObligationID(obligationID); err != nil {
				t.Fatalf("synthetic obligation=%q: %v", obligationID, err)
			}
			dependencies, listErr := repository.ListRunQueryDependencies(ctx, run.ID)
			if listErr != nil || len(dependencies) != 1 || dependencies[0].JobID != syntheticID || dependencies[0].ObligationID != obligationID {
				t.Fatalf("recovery dependencies=%#v err=%v", dependencies, listErr)
			}
			restarted := &Manager{store: repository, longHorizon: repository}
			refs := restarted.queryDependenciesForRun(thread.ID, run.ID)
			if len(refs) != 1 || refs[0].ID != syntheticID {
				t.Fatalf("restart lost recovery obligation: %#v", refs)
			}
		})
	}

	maskedRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun masked valid id: %v", err)
	}
	maskedJobID := "qj-dbx-valid-after-zero"
	now := time.Now().UTC()
	maskedRecord := jobs.JobRecord{
		ID: maskedJobID, Provider: "databricks", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: maskedRun.ID, Handle: "qown-masked-valid"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
		Metadata: map[string]any{jobs.QueryObligationManagedMetadataKey: true},
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, maskedRecord, 0); err != nil {
		t.Fatalf("persist masked valid job: %v", err)
	}
	maskedOut := tools.Output{
		OK: true, ResultFormat: tools.ResultFormatJSON,
		Result:        map[string]any{"job_id": maskedJobID, "status": "running", "owner_token": "wrong-output-token", "query_obligation_id": "qob-ffffffffffffffffffffffffffffffff"},
		QueryMetadata: map[string]any{"job_id": "00000-00000", "status": "running", "owner_token": nil},
	}
	maskedCall := types.ToolCall{ID: "call-masked-valid", Name: "run_databricks_query", Args: map[string]any{"sql": "select 1"}}
	manager.processAsyncQueryRunOutput(ctx, thread, maskedRun, maskedCall, maskedCall.Name, "", &maskedOut)
	if !maskedOut.OK || extractAsyncQueryJobID(maskedOut) != maskedJobID || firstStringMapValue(maskedOut.QueryMetadata, "job_id") != maskedJobID {
		t.Fatalf("zero alias masked a valid durable job identity: %#v", maskedOut)
	}
	if firstStringMapValue(maskedOut.QueryMetadata, "owner_token") != maskedRecord.Owner.Handle ||
		firstStringMapValue(anyMap(maskedOut.Result), "owner_token") != maskedRecord.Owner.Handle ||
		firstStringMapValue(maskedOut.QueryMetadata, "run_id") != maskedRun.ID ||
		store.ValidateQueryObligationID(firstStringMapValue(maskedOut.QueryMetadata, "query_obligation_id")) != nil {
		t.Fatalf("valid run output did not receive authoritative owner/run/qob scope: %#v", maskedOut)
	}
	if dependencies, listErr := repository.ListRunQueryDependencies(ctx, maskedRun.ID); listErr != nil || len(dependencies) != 1 || dependencies[0].JobID != maskedJobID {
		t.Fatalf("valid identity behind zero alias was not registered: %#v err=%v", dependencies, listErr)
	}

	// A successful legacy synchronous result is the sole no-job compatibility
	// path: rows are already materialized, so no polling obligation is needed.
	syncRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun sync: %v", err)
	}
	nativeBigQueryJobID := "bquxjob_native_123"
	syncOut := tools.Output{
		OK: true, ResultFormat: tools.ResultFormatJSON,
		Result:        map[string]any{"rows": []any{map[string]any{"n": 1}}},
		QueryMetadata: map[string]any{"job_id": nativeBigQueryJobID},
	}
	syncCall := types.ToolCall{ID: "call-sync", Name: "run_bigquery_query", Args: map[string]any{"sql": "select 1"}}
	manager.processAsyncQueryRunOutput(ctx, thread, syncRun, syncCall, syncCall.Name, "", &syncOut)
	if !syncOut.OK {
		t.Fatalf("materialized synchronous compatibility output was rejected: %#v", syncOut)
	}
	if extractAsyncQueryJobID(syncOut) != "" || firstStringMapValue(syncOut.QueryMetadata, "provider_job_id") != nativeBigQueryJobID {
		t.Fatalf("native synchronous identity was not relocated: %#v", syncOut.QueryMetadata)
	}
	if dependencies, listErr := repository.ListRunQueryDependencies(ctx, syncRun.ID); listErr != nil || len(dependencies) != 0 {
		t.Fatalf("materialized synchronous output created a dependency: %#v err=%v", dependencies, listErr)
	}
}

func TestRestartRecoveryRejectsIdentitylessAndBogusSuccessfulQueryOutputs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	appendToolResult := func(runID, callID, content string) {
		t.Helper()
		name := "run_databricks_query"
		if _, appendErr := repository.AppendMessage(ctx, thread.ID, &runID, store.Message{
			Role: types.RoleTool, Content: &content, Name: &name, ToolCallID: &callID,
		}); appendErr != nil {
			t.Fatalf("AppendMessage: %v", appendErr)
		}
	}

	identitylessRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun identityless: %v", err)
	}
	appendToolResult(identitylessRun.ID, "call-identityless", `{"tool_name":"run_databricks_query","ok":true,"result":{"message":"query started"}}`)
	identitylessRestart := &Manager{store: repository, longHorizon: repository}
	refs := identitylessRestart.queryDependenciesForRun(thread.ID, identitylessRun.ID)
	if len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") {
		t.Fatalf("restart accepted OK=true without job identity or rows: %#v", refs)
	}

	materializedRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun materialized: %v", err)
	}
	appendToolResult(materializedRun.ID, "call-materialized", `{"tool_name":"run_databricks_query","ok":true,"result":{"rows":[{"n":1}]}}`)
	if refs := (&Manager{store: repository, longHorizon: repository}).queryDependenciesForRun(thread.ID, materializedRun.ID); len(refs) != 0 {
		t.Fatalf("restart rejected materialized synchronous compatibility output: %#v", refs)
	}

	bogusRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun bogus: %v", err)
	}
	bogusID := "00000-00000"
	appendToolResult(bogusRun.ID, "call-bogus", `{"tool_name":"run_databricks_query","ok":true,"result":{"job_id":"`+bogusID+`","status":"running"}}`)
	bogusRestart := &Manager{store: repository, longHorizon: repository}
	refs = bogusRestart.queryDependenciesForRun(thread.ID, bogusRun.ID)
	if len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") || refs[0].ID == bogusID {
		t.Fatalf("restart turned bogus identity into a permanent job fault: %#v", refs)
	}

	now := time.Now().UTC()
	healingJobID := "qj-dbx-healing123"
	healing := jobs.JobRecord{
		ID: healingJobID, Provider: "databricks", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: bogusRun.ID, Handle: "qown-healing"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, healing, 0); err != nil {
		t.Fatalf("persist healing job: %v", err)
	}
	healingObligationID, err := store.QueryObligationIDForRoot(bogusRun.ID, healingJobID)
	if err != nil {
		t.Fatalf("derive healing obligation: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: bogusRun.ID, JobID: healingJobID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationUnvalidated, ObligationID: healingObligationID,
	}); err != nil {
		t.Fatalf("persist healing dependency: %v", err)
	}
	appendToolResult(bogusRun.ID, "call-healing", `{"tool_name":"run_databricks_query","ok":true,"result":{"job_id":"`+healingJobID+`","query_obligation_id":"`+healingObligationID+`","status":"running"}}`)
	healedRestart := &Manager{store: repository, longHorizon: repository}
	refs = healedRestart.queryDependenciesForRun(thread.ID, bogusRun.ID)
	seenHealingJob := false
	seenOriginalFault := false
	for _, ref := range refs {
		seenHealingJob = seenHealingJob || ref.ID == healingJobID
		seenOriginalFault = seenOriginalFault || strings.HasPrefix(ref.ID, "dependency-fault-")
	}
	if len(refs) != 2 || !seenHealingJob || !seenOriginalFault {
		t.Fatalf("unrelated durable action erased bogus-identity protocol fault: %#v", refs)
	}
	completedAt := time.Now().UTC()
	healing.Status = jobs.JobStatusCompleted
	healing.CompletedAt = &completedAt
	healing.ValidationState = jobs.ValidationValidated
	storedHealing, found, err := repository.GetQueryJob(ctx, healingJobID)
	if err != nil || !found {
		t.Fatalf("reload unrelated job: found=%v err=%v", found, err)
	}
	healing.Revision = storedHealing.Revision
	if _, err := repository.UpsertQueryJobCAS(ctx, healing, healing.Revision); err != nil {
		t.Fatalf("validate unrelated job: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: bogusRun.ID, JobID: healingJobID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationValidated, ObligationID: healingObligationID,
	}); err != nil {
		t.Fatalf("validate unrelated dependency: %v", err)
	}
	refs = (&Manager{store: repository, longHorizon: repository}).queryDependenciesForRun(thread.ID, bogusRun.ID)
	if len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") {
		t.Fatalf("validated unrelated query erased the original protocol fault: %#v", refs)
	}
}

func TestPersistedQueryToolCallIdentityCollisionsFailClosed(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name              string
		olderColumnID     string
		olderEnvelopeID   string
		olderSQL          string
		newerColumnID     string
		newerEnvelopeID   string
		newerSQL          string
		wantProtocolFault int
	}{
		{
			name:          "reused message id with different args",
			olderColumnID: "call-shared", olderEnvelopeID: "call-shared", olderSQL: "select 'first'",
			newerColumnID: "call-shared", newerEnvelopeID: "call-shared", newerSQL: "select 'second'",
			wantProtocolFault: 1,
		},
		{
			name:          "message and envelope ids disagree",
			olderColumnID: "call-original", olderEnvelopeID: "call-original", olderSQL: "select 1",
			newerColumnID: "call-new", newerEnvelopeID: "call-original", newerSQL: "select 1",
			wantProtocolFault: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repository, _ := store.NewJSON(t.TempDir())
			thread, _ := repository.CreateThread(ctx, nil)
			run, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
			now := time.Now().UTC()
			job := jobs.JobRecord{
				ID: "qj-dbx-call-collision", Provider: "databricks", Status: jobs.JobStatusRunning,
				Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "tok_tttttttttttttttttttttttttttttttt"},
				SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
			}
			if _, err := repository.UpsertQueryJobCAS(ctx, job, 0); err != nil {
				t.Fatal(err)
			}
			obligationID, _ := store.QueryObligationIDForRoot(run.ID, job.ID)
			if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
				RunID: run.ID, JobID: job.ID, RequiredForCompletion: true,
				ValidationState: store.QueryValidationUnvalidated, ObligationID: obligationID,
			}); err != nil {
				t.Fatal(err)
			}
			name := "run_databricks_query"
			older := `{"tool_name":"run_databricks_query","tool_call_id":"` + tc.olderEnvelopeID + `","tool_args":{"sql":"` + tc.olderSQL + `"},"ok":true,"result":{"message":"submission started"}}`
			if _, err := repository.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
				Role: types.RoleTool, Content: &older, Name: &name, ToolCallID: &tc.olderColumnID,
			}); err != nil {
				t.Fatal(err)
			}
			newer := `{"tool_name":"run_databricks_query","tool_call_id":"` + tc.newerEnvelopeID + `","tool_args":{"sql":"` + tc.newerSQL + `"},"ok":true,"result":{"job_id":"` + job.ID + `","query_obligation_id":"` + obligationID + `","status":"running"}}`
			if _, err := repository.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
				Role: types.RoleTool, Content: &newer, Name: &name, ToolCallID: &tc.newerColumnID,
			}); err != nil {
				t.Fatal(err)
			}

			refs := (&Manager{store: repository, longHorizon: repository}).queryDependenciesForRun(thread.ID, run.ID)
			faults, activeJob := 0, false
			for _, ref := range refs {
				if strings.HasPrefix(ref.ID, "dependency-fault-") {
					faults++
				}
				activeJob = activeJob || ref.ID == job.ID
			}
			if faults != tc.wantProtocolFault || !activeJob || len(refs) != tc.wantProtocolFault+1 {
				t.Fatalf("call identity collision did not fail closed: %#v", refs)
			}
		})
	}
}

func TestStatusObservationAdoptsOlderRunningQueryForCurrentRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfgDir := t.TempDir()
	repository, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	priorRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun prior: %v", err)
	}
	currentRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun current: %v", err)
	}
	now := time.Now().UTC()
	jobID := "qj-dbx-prior-running"
	ownerToken := "tok_0123456789abcdef0123456789abcdef"
	record := jobs.JobRecord{
		ID: jobID, Provider: "databricks", Status: jobs.JobStatusRunning,
		Handle:      jobs.NativeHandle{Provider: "databricks", ID: "statement-prior-running"},
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: priorRun.ID, Handle: ownerToken},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
		Metadata: map[string]any{jobs.QueryObligationManagedMetadataKey: true},
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
		t.Fatalf("persist prior query: %v", err)
	}
	registry := jobs.NewRegistry(jobs.RegistryOptions{Store: repository})
	manager := &Manager{
		store: repository, tools: tools.NewRegistry(nil), queryJobs: registry,
		longHorizon: repository, cfgDir: cfgDir,
	}
	call := types.ToolCall{
		ID: "call-observe-prior", Name: "check_query_status",
		ArgsText: `{"job_id":"` + jobID + `","owner_token":"` + ownerToken + `"}`,
	}
	message, err := manager.execTool(
		ctx, thread, currentRun, testProjectID(""), call, false, "shallow", 0, nil,
		map[string]bool{"databricks": true}, defaultChartEngineEnablement(),
		false, false, true, true, false,
		atlassianEnablement{},
		false,
		false,
		slackToolAccess{},
	)
	if err != nil {
		t.Fatalf("execTool status observation: %v", err)
	}
	if message.Content == nil {
		t.Fatal("status observation did not persist a tool result")
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(*message.Content), &envelope); err != nil {
		t.Fatalf("decode status envelope: %v\n%s", err, *message.Content)
	}
	result := anyMap(envelope["result"])
	queryMetadata := anyMap(envelope["query_metadata"])
	obligationID := firstStringMapValue(result, "query_obligation_id")
	if obligationID == "" {
		obligationID = firstStringMapValue(queryMetadata, "query_obligation_id")
	}
	if err := store.ValidateQueryObligationID(obligationID); err != nil {
		t.Fatalf("status observation obligation=%q: %v; envelope=%s", obligationID, err, *message.Content)
	}
	if firstStringMapValue(result, "run_id") != currentRun.ID || firstStringMapValue(result, "thread_id") != thread.ID {
		t.Fatalf("status observation did not expose current observer scope: %s", *message.Content)
	}
	if strings.Contains(*message.Content, `"run_id":"`+priorRun.ID+`"`) {
		t.Fatalf("status observation paired current qob with prior owner run: %s", *message.Content)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, currentRun.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].JobID != jobID || dependencies[0].ObligationID != obligationID || !dependencies[0].RequiredForCompletion || dependencies[0].Detached {
		t.Fatalf("current run did not adopt observed query: %#v err=%v", dependencies, err)
	}
	if refs := manager.queryDependenciesForRun(thread.ID, currentRun.ID); len(refs) != 1 || refs[0].ID != jobID {
		t.Fatalf("observed running query is not a completion barrier: %#v", refs)
	}
	restarted := &Manager{store: repository, longHorizon: repository}
	if refs := restarted.queryDependenciesForRun(thread.ID, currentRun.ID); len(refs) != 1 || refs[0].ID != jobID {
		t.Fatalf("restart forgot adopted running query: %#v", refs)
	}
}

// A run-query call that failed BEFORE any durable work (missing provider
// config, invalid preflight, etc.) but explicitly targeted an existing
// obligation of this run must preserve that obligation: no synthetic root is
// created and only the ledger-verified handle is echoed. Explicit unverifiable
// caller values fail closed without creating a second task and are never
// echoed; only targetless first-attempt failures receive a synthetic fail-safe.
func TestFailedRetryPreservesExplicitObligationWithoutSyntheticRoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now().UTC()
	root := jobs.JobRecord{
		ID: "qj-preserve-root", Provider: "databricks", Status: jobs.JobStatusFailed,
		Owner:           jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-preserve"},
		ValidationState: jobs.ValidationRejected,
		SubmittedAt:     now, LastAccess: now, CompletedAt: &now,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, root, 0); err != nil {
		t.Fatalf("persist root: %v", err)
	}
	obligationID, obligationErr := store.QueryObligationIDForRoot(run.ID, root.ID)
	if obligationErr != nil {
		t.Fatalf("derive obligation: %v", obligationErr)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: run.ID, JobID: root.ID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationRejected, ObligationID: obligationID,
	}); err != nil {
		t.Fatalf("seed dependency: %v", err)
	}
	manager := &Manager{longHorizon: repository}

	// Config/preflight failure with a verified obligation target in the args.
	configFailure := tools.Output{OK: false, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{
		"error": "Databricks is not configured.",
	}}
	call := types.ToolCall{ID: "call-preserve", Name: "run_databricks_query", Args: map[string]any{
		"sql": "select 1", "query_obligation_id": obligationID,
	}}
	verified, preserved := manager.preservedExplicitQueryRetryTarget(ctx, run.ID, call, &configFailure)
	if !preserved || verified != obligationID {
		t.Fatalf("preserved=%v verified=%q, want the existing obligation", preserved, verified)
	}
	result := configFailure.Result.(map[string]any)
	if result["query_obligation_id"] != obligationID || result["replacement_wait_required"] != true {
		t.Fatalf("enriched failure=%#v, want verified obligation echo", result)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].JobID != root.ID {
		t.Fatalf("ledger changed: %#v err=%v", dependencies, err)
	}

	// The legacy replaces_job_id spelling preserves the same obligation.
	legacyFailure := tools.Output{OK: false, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{
		"error": "Missing sql",
	}}
	legacyCall := types.ToolCall{ID: "call-preserve-legacy", Name: "run_databricks_query", Args: map[string]any{
		"replaces_job_id": root.ID,
	}}
	verified, preserved = manager.preservedExplicitQueryRetryTarget(ctx, run.ID, legacyCall, &legacyFailure)
	if !preserved || verified != obligationID {
		t.Fatalf("legacy preserved=%v verified=%q", preserved, verified)
	}

	// Both spellings may coexist during migration, but each is verified and
	// they must independently resolve to the same active chain.
	pairedFailure := tools.Output{OK: false, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{"error": "Missing sql"}}
	pairedCall := types.ToolCall{ID: "call-preserve-paired", Name: "run_databricks_query", Args: map[string]any{
		"query_obligation_id": obligationID,
		"replaces_job_id":     root.ID,
	}}
	if got := manager.classifyExplicitQueryRetryTarget(ctx, run.ID, pairedCall, &pairedFailure); got.State != explicitQueryRetryTargetVerified || got.ObligationID != obligationID {
		t.Fatalf("paired target classification=%#v, want verified %q", got, obligationID)
	}

	// A malformed public handle cannot silently downgrade to a valid legacy
	// job id. Presence of both fields means both must verify.
	malformedPairedFailure := tools.Output{OK: false, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{"error": "Missing sql"}}
	malformedPairedCall := types.ToolCall{ID: "call-malformed-paired", Name: "run_databricks_query", Args: map[string]any{
		"query_obligation_id": "qob-not-valid",
		"replaces_job_id":     root.ID,
	}}
	if got := manager.classifyExplicitQueryRetryTarget(ctx, run.ID, malformedPairedCall, &malformedPairedFailure); got.State != explicitQueryRetryTargetUnverified {
		t.Fatalf("malformed paired target classification=%#v, want explicit-unverified", got)
	}
	if raw, marshalErr := json.Marshal(malformedPairedFailure); marshalErr != nil || strings.Contains(string(raw), root.ID) || strings.Contains(string(raw), "qob-not-valid") {
		t.Fatalf("malformed paired output leaked a caller target: err=%v payload=%s", marshalErr, raw)
	}

	second := root
	second.ID = "qj-preserve-second-root"
	second.Owner.Handle = "qown-preserve-second"
	if _, err := repository.UpsertQueryJobCAS(ctx, second, 0); err != nil {
		t.Fatalf("persist second root: %v", err)
	}
	secondObligationID, secondObligationErr := store.QueryObligationIDForRoot(run.ID, second.ID)
	if secondObligationErr != nil {
		t.Fatalf("derive second obligation: %v", secondObligationErr)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: run.ID, JobID: second.ID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationRejected, ObligationID: secondObligationID,
	}); err != nil {
		t.Fatalf("seed second dependency: %v", err)
	}
	mismatchedFailure := tools.Output{OK: false, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{"error": "Missing sql"}}
	mismatchedCall := types.ToolCall{ID: "call-mismatched-paired", Name: "run_databricks_query", Args: map[string]any{
		"query_obligation_id": obligationID,
		"replaces_job_id":     second.ID,
	}}
	if got := manager.classifyExplicitQueryRetryTarget(ctx, run.ID, mismatchedCall, &mismatchedFailure); got.State != explicitQueryRetryTargetUnverified {
		t.Fatalf("mismatched paired target classification=%#v, want explicit-unverified", got)
	}
	if raw, marshalErr := json.Marshal(mismatchedFailure); marshalErr != nil || strings.Contains(string(raw), obligationID) || strings.Contains(string(raw), second.ID) {
		t.Fatalf("mismatched paired output leaked a caller target: err=%v payload=%s", marshalErr, raw)
	}

	// An unverifiable caller value is an explicit retry request, not a new first
	// attempt. It is sanitized and must not split the task with a synthetic root.
	bogus := "qob-ffffffffffffffffffffffffffffffff"
	bogusFailure := tools.Output{OK: false, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{
		"error": "Databricks is not configured.",
	}}
	bogusCall := types.ToolCall{ID: "call-bogus", Name: "run_databricks_query", Args: map[string]any{
		"sql": "select 1", "query_obligation_id": bogus,
	}}
	disposition := manager.classifyExplicitQueryRetryTarget(ctx, run.ID, bogusCall, &bogusFailure)
	if disposition.State != explicitQueryRetryTargetUnverified {
		t.Fatalf("unverifiable target state=%v, want explicit-unverified", disposition.State)
	}
	if raw, marshalErr := json.Marshal(bogusFailure); marshalErr != nil || strings.Contains(string(raw), bogus) {
		t.Fatalf("fail-closed output echoed unverified caller obligation: err=%v payload=%s", marshalErr, raw)
	}
	dependencies, err = repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 2 {
		t.Fatalf("explicit invalid retry changed the ledger: %#v err=%v", dependencies, err)
	}

	unsupportedFailure := tools.Output{OK: false, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{"error": "Invalid arguments."}}
	unsupportedCall := types.ToolCall{ID: "call-unsupported", Name: "run_databricks_query", Args: map[string]any{
		"sql": "select 1", "replacement_job_id": root.ID,
	}}
	if got := manager.classifyExplicitQueryRetryTarget(ctx, run.ID, unsupportedCall, &unsupportedFailure); got.State != explicitQueryRetryTargetUnverified {
		t.Fatalf("unsupported-only identity classification=%#v, want explicit-unverified", got)
	}
	if raw, marshalErr := json.Marshal(unsupportedFailure); marshalErr != nil || strings.Contains(string(raw), root.ID) {
		t.Fatalf("unsupported-only output leaked caller identity: err=%v payload=%s", marshalErr, raw)
	}

	// A genuinely targetless first-attempt failure still creates one durable,
	// deterministic recovery root so the model cannot abandon the task.
	targetlessFailure := tools.Output{OK: false, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{
		"error": "Databricks is not configured.",
	}}
	targetlessCall := types.ToolCall{ID: "call-targetless", Name: "run_databricks_query", Args: map[string]any{
		"sql": "select 2",
	}}
	if got := manager.classifyExplicitQueryRetryTarget(ctx, run.ID, targetlessCall, &targetlessFailure); got.State != explicitQueryRetryTargetNone {
		t.Fatalf("targetless classification=%v, want none", got.State)
	}
	syntheticID, syntheticErr := manager.persistFailedQueryAttemptObligation(ctx, thread, run, targetlessCall, targetlessCall.Name, &targetlessFailure)
	if syntheticErr != nil || syntheticID == "" {
		t.Fatalf("targetless synthetic obligation=%q err=%v", syntheticID, syntheticErr)
	}
	dependencies, err = repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 3 {
		t.Fatalf("targetless synthetic dependency not recorded exactly once: %#v err=%v", dependencies, err)
	}
}

func TestExplicitRetryTargetCanonicalizesSoleActiveObligationAndRetiresCleanly(t *testing.T) {
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID: "qj-canonical-active", Provider: "databricks", Status: jobs.JobStatusFailed,
		Owner:           jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-canonical-active"},
		ValidationState: jobs.ValidationRejected, SubmittedAt: now, LastAccess: now, CompletedAt: &now,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
		t.Fatalf("persist query: %v", err)
	}
	obligationID, err := store.QueryObligationIDForRoot(run.ID, record.ID)
	if err != nil {
		t.Fatalf("derive obligation: %v", err)
	}
	dependency, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: run.ID, JobID: record.ID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationRejected, ObligationID: obligationID,
	})
	if err != nil {
		t.Fatalf("persist dependency: %v", err)
	}
	manager := &Manager{longHorizon: repository}

	// A nil/stale single handle is recoverable because the ledger has exactly
	// one unfinished obligation. Dispatch rewrites it to the authoritative qob.
	call := types.ToolCall{ID: "call-canonical-active", Name: "run_databricks_query", ArgsText: `{"sql":"select 1","query_obligation_id":null}`}
	out := tools.JSONError("preflight placeholder")
	disposition := manager.classifyExplicitQueryRetryTarget(ctx, run.ID, call, &out)
	if disposition.State != explicitQueryRetryTargetVerified || disposition.ObligationID != obligationID {
		t.Fatalf("classification=%#v, want canonical active obligation %q", disposition, obligationID)
	}
	canonicalCall, canonicalRaw, err := canonicalizeRunQueryRetryCall(call, disposition.ObligationID)
	if err != nil || canonicalCall.Args["query_obligation_id"] != obligationID || canonicalCall.Args["sql"] != "select 1" ||
		!strings.Contains(string(canonicalRaw), obligationID) {
		t.Fatalf("canonical call=%#v raw=%s err=%v", canonicalCall, canonicalRaw, err)
	}

	// Once validation settles the only obligation, the same stale handle is
	// removed and the query can proceed as a new first attempt instead of
	// looping on an impossible active-target verification.
	dependency.ValidationState = store.QueryValidationValidated
	dependency.UpdatedAt = time.Now().UTC()
	if _, err := repository.UpsertRunQueryDependency(ctx, dependency); err != nil {
		t.Fatalf("settle dependency: %v", err)
	}
	retiredCall := types.ToolCall{
		ID:       "call-canonical-retired",
		Name:     "run_databricks_query",
		ArgsText: `{"sql":"select 1","query_obligation_id":"` + obligationID + `"}`,
	}
	settledOut := tools.JSONError("preflight placeholder")
	settled := manager.classifyExplicitQueryRetryTarget(ctx, run.ID, retiredCall, &settledOut)
	if settled.State != explicitQueryRetryTargetFresh {
		t.Fatalf("settled classification=%#v, want fresh launch", settled)
	}
	freshCall, freshRaw, err := canonicalizeRunQueryRetryCall(retiredCall, "")
	if err != nil || freshCall.Args["sql"] != "select 1" || strings.Contains(string(freshRaw), "query_obligation_id") {
		t.Fatalf("fresh call=%#v raw=%s err=%v", freshCall, freshRaw, err)
	}
}

func TestExecToolRepairsNilRetryTargetFromSoleActiveObligation(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	repository, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, _ := repository.CreateThread(ctx, nil)
	run, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID: "qj-exec-canonical-active", Provider: "databricks", Status: jobs.JobStatusFailed,
		Owner:           jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-exec-canonical-active"},
		ValidationState: jobs.ValidationRejected, SubmittedAt: now, LastAccess: now, CompletedAt: &now,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
		t.Fatalf("persist query: %v", err)
	}
	obligationID, _ := store.QueryObligationIDForRoot(run.ID, record.ID)
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: run.ID, JobID: record.ID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationRejected, ObligationID: obligationID,
	}); err != nil {
		t.Fatalf("persist dependency: %v", err)
	}
	manager := &Manager{store: repository, tools: tools.NewRegistry(nil), longHorizon: repository, cfgDir: cfgDir}
	message, err := manager.execTool(
		ctx, thread, run, testProjectID(""),
		types.ToolCall{ID: "call-repair-nil-target", Name: "run_databricks_query", ArgsText: `{"sql":"select 1","query_obligation_id":null}`},
		false, "shallow", 0, nil, map[string]bool{"databricks": true}, defaultChartEngineEnablement(),
		false, false, true, true, false,
		atlassianEnablement{},
		false,
		false,
		slackToolAccess{},
	)
	if err != nil || message.Content == nil {
		t.Fatalf("execTool message=%#v err=%v", message, err)
	}
	content := *message.Content
	if strings.Contains(strings.ToLower(content), "could not be verified as an active obligation") ||
		!strings.Contains(content, obligationID) || !strings.Contains(strings.ToLower(content), "not configured") {
		t.Fatalf("nil target was not repaired to the sole active obligation: %s", content)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].ObligationID != obligationID || dependencies[0].Detached {
		t.Fatalf("repair changed obligation ledger: deps=%#v err=%v", dependencies, err)
	}
}

func TestExecToolRejectsUnsupportedRetryIdentityWithoutCreatingWork(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	repository, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{
		store:       repository,
		tools:       tools.NewRegistry(nil),
		longHorizon: repository,
		cfgDir:      cfgDir,
	}
	message, err := manager.execTool(
		ctx,
		thread,
		run,
		testProjectID(""),
		types.ToolCall{
			ID:       "call-unsupported-dispatch",
			Name:     "run_databricks_query",
			ArgsText: `{"sql":"select 1","replacement_job_id":"qj-stale-private"}`,
		},
		false,
		"shallow",
		0,
		nil,
		map[string]bool{"databricks": true},
		defaultChartEngineEnablement(),
		false,
		false,
		true,
		true,
		false,
		atlassianEnablement{},
		false,
		false,
		slackToolAccess{},
	)
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	if message.Content == nil || strings.Contains(*message.Content, "qj-stale-private") {
		t.Fatalf("unverified retry identity leaked into dispatch result: %#v", message.Content)
	}
	jobsForThread, err := repository.ListQueryJobsByThread(ctx, thread.ID)
	if err != nil || len(jobsForThread) != 0 {
		t.Fatalf("unsupported retry created durable query work: jobs=%#v err=%v", jobsForThread, err)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 0 {
		t.Fatalf("unsupported retry created a synthetic dependency: deps=%#v err=%v", dependencies, err)
	}
	if refs := manager.queryDependenciesForRun(thread.ID, run.ID); len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") {
		t.Fatalf("unsupported retry did not leave a fail-closed protocol barrier: %#v", refs)
	}
	restarted := &Manager{store: repository, longHorizon: repository}
	if refs := restarted.queryDependenciesForRun(thread.ID, run.ID); len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") {
		t.Fatalf("restart forgot persisted unverified retry barrier: %#v", refs)
	}
	healingJobID := "qj-healing-action"
	healingOwnerToken := "qown-healing-action"
	healingNow := time.Now().UTC()
	healingRecord := jobs.JobRecord{
		ID: healingJobID, Provider: "databricks", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: healingOwnerToken},
		SubmittedAt: healingNow, LastAccess: healingNow, ValidationState: jobs.ValidationUnvalidated,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, healingRecord, 0); err != nil {
		t.Fatalf("persist healing query action: %v", err)
	}
	healingObligationID, err := store.QueryObligationIDForRoot(run.ID, healingJobID)
	if err != nil {
		t.Fatalf("derive healing obligation: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: run.ID, JobID: healingJobID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationUnvalidated, ObligationID: healingObligationID,
	}); err != nil {
		t.Fatalf("persist healing query dependency: %v", err)
	}
	healingContent := `{"tool_name":"run_databricks_query","ok":true,"result":{"job_id":"` + healingJobID + `","query_obligation_id":"` + healingObligationID + `","status":"running"}}`
	healingName := "run_databricks_query"
	healingCallID := "call-healing-action"
	if _, err := repository.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
		Role: types.RoleTool, Content: &healingContent, Name: &healingName, ToolCallID: &healingCallID,
	}); err != nil {
		t.Fatalf("append healing query action: %v", err)
	}
	flakyStore := &failOnceListMessagesStore{Store: repository, remainingFailures: 1}
	healedRestart := &Manager{store: flakyStore, longHorizon: repository}
	if refs := healedRestart.queryDependenciesForRun(thread.ID, run.ID); len(refs) != 3 {
		t.Fatalf("transient recovery read did not fail closed alongside durable job: %#v", refs)
	}
	if refs := healedRestart.queryDependenciesForRun(thread.ID, run.ID); len(refs) != 2 {
		t.Fatalf("successful recovery retry erased an unrelated protocol fault: %#v", refs)
	}
	healingCompletedAt := time.Now().UTC()
	storedHealing, found, err := repository.GetQueryJob(ctx, healingJobID)
	if err != nil || !found {
		t.Fatalf("reload healing query: found=%v err=%v", found, err)
	}
	healingRecord.Revision = storedHealing.Revision
	healingRecord.Status = jobs.JobStatusCompleted
	healingRecord.CompletedAt = &healingCompletedAt
	healingRecord.ValidationState = jobs.ValidationValidated
	if _, err := repository.UpsertQueryJobCAS(ctx, healingRecord, healingRecord.Revision); err != nil {
		t.Fatalf("validate healing query job: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: run.ID, JobID: healingJobID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationValidated, ObligationID: healingObligationID,
	}); err != nil {
		t.Fatalf("validate healing dependency: %v", err)
	}
	verifiedFailureContent := `{"tool_name":"run_databricks_query","ok":false,"result":{"query_obligation_id":"` + healingObligationID + `","replacement_preflight_failed":true,"replacement_wait_required":true}}`
	verifiedFailureCallID := "call-verified-after-validation"
	if _, err := repository.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
		Role: types.RoleTool, Content: &verifiedFailureContent, Name: &healingName, ToolCallID: &verifiedFailureCallID,
	}); err != nil {
		t.Fatalf("append qob-only verified failure: %v", err)
	}
	validatedRestart := &Manager{store: repository, longHorizon: repository}
	if refs := validatedRestart.queryDependenciesForRun(thread.ID, run.ID); len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") {
		t.Fatalf("validated unrelated qob erased the unsupported retry fault: %#v", refs)
	}
}

func TestObservedQueryAdoptionFailureRecoversAfterRestart(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	repository, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	priorRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	currentRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID: "qj-dbx-restart-adopt", Provider: "databricks", Status: jobs.JobStatusRunning,
		Handle:      jobs.NativeHandle{Provider: "databricks", ID: "statement-restart-adopt"},
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: priorRun.ID, Handle: "tok_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
		Metadata: map[string]any{jobs.QueryObligationManagedMetadataKey: true},
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
		t.Fatalf("persist query: %v", err)
	}
	failing := &failDependencyUpsertRepository{LongHorizonRepository: repository, remainingFailures: 3}
	manager := &Manager{
		store: repository, tools: tools.NewRegistry(nil), queryJobs: jobs.NewRegistry(jobs.RegistryOptions{Store: repository}),
		longHorizon: failing, cfgDir: cfgDir,
	}
	call := types.ToolCall{ID: "call-restart-adopt", Name: "check_query_status", ArgsText: `{"job_id":"` + record.ID + `","owner_token":"` + record.Owner.Handle + `"}`}
	message, err := manager.execTool(
		ctx, thread, currentRun, testProjectID(""), call, false, "shallow", 0, nil,
		map[string]bool{"databricks": true}, defaultChartEngineEnablement(), false, false, true, true, false,
		atlassianEnablement{},
		false,
		false,
		slackToolAccess{},
	)
	if err != nil || message.Content == nil {
		t.Fatalf("execTool: message=%#v err=%v", message, err)
	}
	if !strings.Contains(*message.Content, "query_observation_adoption_pending") || strings.Contains(*message.Content, "transient dependency upsert failure") {
		t.Fatalf("adoption failure marker missing or leaked internal store error: %s", *message.Content)
	}
	if dependencies, listErr := repository.ListRunQueryDependencies(ctx, currentRun.ID); listErr != nil || len(dependencies) != 0 {
		t.Fatalf("live failure unexpectedly committed dependency: %#v err=%v", dependencies, listErr)
	}
	if refs := manager.queryDependencyFaultRefs(thread.ID, currentRun.ID); len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") {
		t.Fatalf("live adoption failure did not establish a protocol barrier: %#v", refs)
	}
	if refs := manager.queryDependenciesForRun(thread.ID, currentRun.ID); len(refs) != 1 || refs[0].ID != record.ID {
		t.Fatalf("live barrier retry did not self-heal after the store recovered: %#v", refs)
	}

	restarted := &Manager{store: repository, longHorizon: repository}
	refs := restarted.queryDependenciesForRun(thread.ID, currentRun.ID)
	if len(refs) != 1 || refs[0].ID != record.ID {
		t.Fatalf("restart did not reconstruct/adopt observed query: %#v", refs)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, currentRun.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].JobID != record.ID || store.ValidateQueryObligationID(dependencies[0].ObligationID) != nil {
		t.Fatalf("recovered observation dependency=%#v err=%v", dependencies, err)
	}
}

func TestRestartRecoveryAdoptsEveryAuthenticatedQueryObservation(t *testing.T) {
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, _ := repository.CreateThread(ctx, nil)
	currentRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	now := time.Now().UTC()
	jobIDs := []string{"qj-dbx-observed-one", "qj-dbx-observed-two"}
	for index, jobID := range jobIDs {
		ownerRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
		token := "tok_" + strings.Repeat(string(rune('a'+index)), 32)
		record := jobs.JobRecord{
			ID: jobID, Provider: "databricks", Status: jobs.JobStatusRunning,
			Handle:      jobs.NativeHandle{Provider: "databricks", ID: "statement-" + jobID},
			Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: ownerRun.ID, Handle: token},
			SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
		}
		if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
			t.Fatalf("persist %s: %v", jobID, err)
		}
		content := `{"tool_name":"check_query_status","tool_args":{"job_id":"` + jobID + `","owner_token":"` + token + `"},"ok":true,"result":{"job_id":"` + jobID + `","owner_token":"` + token + `","status":"running"},"result_metadata":{"found":true,"job_id":"` + jobID + `"}}`
		name := "check_query_status"
		callID := "call-" + jobID
		if _, err := repository.AppendMessage(ctx, thread.ID, &currentRun.ID, store.Message{Role: types.RoleTool, Content: &content, Name: &name, ToolCallID: &callID}); err != nil {
			t.Fatalf("append observation: %v", err)
		}
	}
	restarted := &Manager{store: repository, longHorizon: repository}
	refs := restarted.queryDependenciesForRun(thread.ID, currentRun.ID)
	if len(refs) != len(jobIDs) {
		t.Fatalf("restart adopted only a subset of observations: %#v", refs)
	}
	for _, jobID := range jobIDs {
		found := false
		for _, ref := range refs {
			found = found || ref.ID == jobID
		}
		if !found {
			t.Fatalf("missing observed dependency %s in %#v", jobID, refs)
		}
	}
}

func TestQueryBarrierRepairsBareAttemptAndStaleValidationRows(t *testing.T) {
	t.Run("bare failed-attempt root", func(t *testing.T) {
		ctx := context.Background()
		repository, _ := store.NewJSON(t.TempDir())
		thread, _ := repository.CreateThread(ctx, nil)
		run, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
		failing := &failDependencyUpsertRepository{LongHorizonRepository: repository, remainingFailures: 1}
		manager := &Manager{store: repository, longHorizon: failing}
		call := types.ToolCall{ID: "call-bare-attempt", Name: "run_databricks_query", Args: map[string]any{"sql": "select 1"}, ArgsText: `{"sql":"select 1"}`}
		out := tools.Output{OK: true, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{"message": "started"}}
		manager.processAsyncQueryRunOutput(ctx, thread, run, call, call.Name, "", &out)
		jobID := extractAsyncQueryJobID(out)
		obligationID := firstStringMapValue(out.QueryMetadata, "query_obligation_id")
		if !validManagedQueryJobID(jobID) || store.ValidateQueryObligationID(obligationID) != nil {
			t.Fatalf("failed dependency insert did not expose deterministic repair identity: %#v", out)
		}
		manager.persistToolResult(ctx, thread, run, call, out, RunContextPlan{})
		if dependencies, _ := repository.ListRunQueryDependencies(ctx, run.ID); len(dependencies) != 0 {
			t.Fatalf("dependency failure unexpectedly persisted row: %#v", dependencies)
		}
		refs := manager.queryDependenciesForRun(thread.ID, run.ID)
		if len(refs) != 1 || refs[0].ID != jobID {
			t.Fatalf("barrier scan did not auto-bind bare root: %#v", refs)
		}
		dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
		if err != nil || len(dependencies) != 1 || dependencies[0].ObligationID != obligationID {
			t.Fatalf("repaired bare-root dependency=%#v err=%v", dependencies, err)
		}
	})

	t.Run("validated job with stale dependency", func(t *testing.T) {
		ctx := context.Background()
		repository, _ := store.NewJSON(t.TempDir())
		thread, _ := repository.CreateThread(ctx, nil)
		run, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
		now := time.Now().UTC()
		jobID := "qj-dbx-validated-stale-row"
		record := jobs.JobRecord{
			ID: jobID, Provider: "databricks", Status: jobs.JobStatusCompleted,
			Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "tok_cccccccccccccccccccccccccccccccc"},
			SubmittedAt: now, LastAccess: now, CompletedAt: &now, ValidationState: jobs.ValidationValidated,
		}
		if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
			t.Fatalf("persist validated job: %v", err)
		}
		obligationID, _ := store.QueryObligationIDForRoot(run.ID, jobID)
		if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
			RunID: run.ID, JobID: jobID, RequiredForCompletion: true,
			ValidationState: store.QueryValidationUnvalidated, ObligationID: obligationID,
		}); err != nil {
			t.Fatalf("persist stale dependency: %v", err)
		}
		failing := &failDependencyUpsertRepository{LongHorizonRepository: repository, remainingFailures: 1}
		manager := &Manager{store: repository, longHorizon: failing}
		if refs := manager.queryDependenciesForRun(thread.ID, run.ID); len(refs) != 1 || refs[0].ID != jobID {
			t.Fatalf("transient validation-row repair did not fail closed: %#v", refs)
		}
		if refs := manager.queryDependenciesForRun(thread.ID, run.ID); len(refs) != 0 {
			t.Fatalf("validation-row repair did not heal barrier: %#v", refs)
		}
		dependencies, _ := repository.ListRunQueryDependencies(ctx, run.ID)
		if len(dependencies) != 1 || dependencies[0].ValidationState != store.QueryValidationValidated {
			t.Fatalf("dependency validation was not reconciled: %#v", dependencies)
		}
	})

	t.Run("rejected job with stale validated dependency", func(t *testing.T) {
		ctx := context.Background()
		repository, _ := store.NewJSON(t.TempDir())
		thread, _ := repository.CreateThread(ctx, nil)
		run, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
		now := time.Now().UTC()
		jobID := "qj-dbx-rejected-stale-validated-row"
		record := jobs.JobRecord{
			ID: jobID, Provider: "databricks", Status: jobs.JobStatusCompleted,
			Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "tok_rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr"},
			SubmittedAt: now, LastAccess: now, CompletedAt: &now, ValidationState: jobs.ValidationRejected,
		}
		if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
			t.Fatalf("persist rejected job: %v", err)
		}
		obligationID, _ := store.QueryObligationIDForRoot(run.ID, jobID)
		if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
			RunID: run.ID, JobID: jobID, RequiredForCompletion: true,
			ValidationState: store.QueryValidationValidated, ObligationID: obligationID,
		}); err != nil {
			t.Fatalf("persist stale validated dependency: %v", err)
		}
		failing := &failDependencyUpsertRepository{LongHorizonRepository: repository, remainingFailures: 1}
		manager := &Manager{store: repository, longHorizon: failing}
		if refs := manager.queryDependenciesForRun(thread.ID, run.ID); len(refs) == 0 {
			t.Fatal("rejection downgrade write failure exposed an empty completion barrier")
		}
		persisted, _ := repository.ListRunQueryDependencies(ctx, run.ID)
		if len(persisted) != 1 || persisted[0].ValidationState != store.QueryValidationValidated {
			t.Fatalf("injected failure unexpectedly changed durable dependency: %#v", persisted)
		}
		refs := manager.queryDependenciesForRun(thread.ID, run.ID)
		if len(refs) != 1 || refs[0].ID != jobID || refs[0].ValidationState != store.QueryValidationRejected {
			t.Fatalf("authoritative rejection did not repair/block dependency: %#v", refs)
		}
		persisted, _ = repository.ListRunQueryDependencies(ctx, run.ID)
		if len(persisted) != 1 || persisted[0].ValidationState != store.QueryValidationRejected {
			t.Fatalf("rejected dependency state was not durably reconciled: %#v", persisted)
		}
	})
}

func TestQueryObservationRequiresExactIdentityAndStampsDurableCapability(t *testing.T) {
	ctx := context.Background()
	repository, _ := store.NewJSON(t.TempDir())
	thread, _ := repository.CreateThread(ctx, nil)
	ownerRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	currentRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID: "qj-dbx-observation-auth", Provider: "databricks", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: ownerRun.ID, Handle: "tok_dddddddddddddddddddddddddddddddd"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
	}
	other := record
	other.ID = "qj-dbx-cross-wired"
	other.Owner.Handle = "tok_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
		t.Fatalf("persist record: %v", err)
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, other, 0); err != nil {
		t.Fatalf("persist other: %v", err)
	}
	manager := &Manager{store: repository, longHorizon: repository}
	call := types.ToolCall{ID: "call-observation-auth", Name: "check_query_status", Args: map[string]any{"job_id": record.ID, "owner_token": record.Owner.Handle}}
	out := tools.Output{
		OK: true, ResultFormat: tools.ResultFormatJSON,
		Result:         map[string]any{"job_id": record.ID, "owner_token": "wrong-output-token", "status": "running"},
		ResultMetadata: map[string]any{"job_id": record.ID, "owner_token": "wrong-output-token", "found": true},
	}
	manager.processAsyncQueryObservationOutput(ctx, thread, currentRun, call, &out)
	if firstStringMapValue(anyMap(out.Result), "owner_token") != record.Owner.Handle ||
		firstStringMapValue(out.QueryMetadata, "owner_token") != record.Owner.Handle ||
		store.ValidateQueryObligationID(firstStringMapValue(out.QueryMetadata, "query_obligation_id")) != nil {
		t.Fatalf("observation did not stamp durable capability/qob: %#v", out)
	}

	crossWired := tools.Output{
		OK: true, ResultFormat: tools.ResultFormatJSON,
		Result:         map[string]any{"job_id": other.ID, "owner_token": other.Owner.Handle, "status": "running"},
		ResultMetadata: map[string]any{"job_id": other.ID, "found": true},
	}
	manager.processAsyncQueryObservationOutput(ctx, thread, currentRun, call, &crossWired)
	raw, _ := json.Marshal(crossWired)
	if crossWired.OK || strings.Contains(string(raw), other.ID) || !strings.Contains(string(raw), "query_observation_identity_mismatch") {
		t.Fatalf("cross-wired response was not sanitized/fail-closed: %s", raw)
	}
	crossWiredMessage := manager.persistToolResult(ctx, thread, currentRun, call, crossWired, RunContextPlan{})
	if crossWiredMessage.Content == nil || !strings.Contains(*crossWiredMessage.Content, record.ID) ||
		!strings.Contains(*crossWiredMessage.Content, record.Owner.Handle) || strings.Contains(*crossWiredMessage.Content, other.ID) {
		t.Fatalf("persisted cross-wired observation lost the valid request capability or retained the wrong response: %#v", crossWiredMessage.Content)
	}
	restarted := &Manager{store: repository, longHorizon: repository}
	restartedRefs := restarted.queryDependenciesForRun(thread.ID, currentRun.ID)
	foundRequested, foundCrossWired := false, false
	for _, ref := range restartedRefs {
		foundRequested = foundRequested || ref.ID == record.ID
		foundCrossWired = foundCrossWired || ref.ID == other.ID
	}
	if !foundRequested || foundCrossWired {
		t.Fatalf("restart did not fail closed around the exact requested job: %#v", restartedRefs)
	}
	dependencies, _ := repository.ListRunQueryDependencies(ctx, currentRun.ID)
	if len(dependencies) != 1 || dependencies[0].JobID != record.ID {
		t.Fatalf("cross-wired response attached the wrong job: %#v", dependencies)
	}

	zeroRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	zeroCall := types.ToolCall{Name: "check_query_status", Args: map[string]any{"job_id": "00000-00000", "owner_token": record.Owner.Handle}}
	zeroOut := tools.Output{OK: true, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{"job_id": "00000-00000", "status": "unknown"}}
	manager.processAsyncQueryObservationOutput(ctx, thread, zeroRun, zeroCall, &zeroOut)
	zeroRaw, _ := json.Marshal(zeroOut)
	if zeroOut.OK || strings.Contains(string(zeroRaw), "00000-00000") {
		t.Fatalf("zero lifecycle identity was not sanitized: %s", zeroRaw)
	}
	message := manager.persistToolResult(ctx, thread, zeroRun, zeroCall, zeroOut, RunContextPlan{})
	if message.Content == nil || strings.Contains(*message.Content, "00000-00000") {
		t.Fatalf("persisted zero lifecycle authority was not stripped: %#v", message.Content)
	}
	if refs := manager.queryDependenciesForRun(thread.ID, zeroRun.ID); len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") {
		t.Fatalf("zero lifecycle identity did not create a live protocol barrier: %#v", refs)
	}
	zeroCancel := types.ToolCall{
		ID: "call-zero-cancel", Name: "cancel_query",
		Args: map[string]any{"job_id": "00000-00000", "owner_token": record.Owner.Handle, "reason": "wrong_query"},
	}
	zeroCancelOut := guardedQueryCancellationPreflightFailure(zeroCancel, errors.New("invalid cancellation identity"))
	zeroCancelMessage := manager.persistToolResult(ctx, thread, zeroRun, zeroCancel, zeroCancelOut, RunContextPlan{})
	if zeroCancelMessage.Content == nil || strings.Contains(*zeroCancelMessage.Content, "00000-00000") ||
		!strings.Contains(*zeroCancelMessage.Content, `"reason":"wrong_query"`) {
		t.Fatalf("persisted invalid cancel authority was not stripped while preserving reason: %#v", zeroCancelMessage.Content)
	}
	portableZeroCancel := sanitizeStaleQueryRetryToolCall(zeroCancel)
	portableRaw, _ := json.Marshal(portableZeroCancel.Args)
	if strings.Contains(string(portableRaw), "00000-00000") || strings.Contains(string(portableRaw), record.Owner.Handle) ||
		!strings.Contains(string(portableRaw), "wrong_query") {
		t.Fatalf("portable history retained invalid cancel authority or lost reason: %s", portableRaw)
	}
	validCancel := types.ToolCall{
		Name: "cancel_query",
		Args: map[string]any{"job_id": record.ID, "owner_token": record.Owner.Handle, "reason": "superseded"},
	}
	portableValid := sanitizeStaleQueryRetryToolCall(validCancel)
	if portableValid.Args["job_id"] != record.ID || portableValid.Args["owner_token"] != record.Owner.Handle {
		t.Fatalf("portable history stripped valid managed cancel capability: %#v", portableValid.Args)
	}
}

func TestDetachedPredecessorObservationCannotReactivateOrValidateSuccessor(t *testing.T) {
	ctx := context.Background()
	repository, _ := store.NewJSON(t.TempDir())
	thread, _ := repository.CreateThread(ctx, nil)
	run, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	now := time.Now().UTC()
	predecessor := jobs.JobRecord{
		ID: "qj-dbx-detached-predecessor", Provider: "databricks", Status: jobs.JobStatusFailed,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "tok_ffffffffffffffffffffffffffffffff"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationRejected,
		Metadata: map[string]any{jobs.QueryObligationManagedMetadataKey: true},
	}
	successor := jobs.JobRecord{
		ID: "qj-dbx-active-successor", Provider: "databricks", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "tok_gggggggggggggggggggggggggggggggg"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
		ReplacesJobID: predecessor.ID,
		Metadata:      map[string]any{jobs.QueryObligationManagedMetadataKey: true},
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, predecessor, 0); err != nil {
		t.Fatalf("persist predecessor: %v", err)
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, successor, 0); err != nil {
		t.Fatalf("persist successor: %v", err)
	}
	obligationID, err := store.QueryObligationIDForRoot(run.ID, predecessor.ID)
	if err != nil {
		t.Fatalf("derive obligation: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: run.ID, JobID: predecessor.ID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationRejected, ObligationID: obligationID,
	}); err != nil {
		t.Fatalf("persist predecessor dependency: %v", err)
	}
	if _, err := repository.ReplaceRunQueryDependency(ctx, run.ID, predecessor.ID, store.RunQueryDependency{
		RunID: run.ID, JobID: successor.ID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationUnvalidated, ObligationID: obligationID,
	}); err != nil {
		t.Fatalf("replace predecessor dependency: %v", err)
	}

	// Simulate a late terminal/validation observation arriving for the detached
	// predecessor after the successor became the active attempt.
	storedPredecessor, found, err := repository.GetQueryJob(ctx, predecessor.ID)
	if err != nil || !found {
		t.Fatalf("reload predecessor: found=%v err=%v", found, err)
	}
	storedPredecessor.Status = jobs.JobStatusCompleted
	storedPredecessor.CompletedAt = &now
	storedPredecessor.ValidationState = jobs.ValidationValidated
	if _, err := repository.UpsertQueryJobCAS(ctx, storedPredecessor, storedPredecessor.Revision); err != nil {
		t.Fatalf("persist late predecessor validation: %v", err)
	}

	manager := &Manager{store: repository, longHorizon: repository}
	call := types.ToolCall{
		ID: "call-detached-predecessor", Name: "check_query_status",
		Args: map[string]any{"job_id": predecessor.ID, "owner_token": predecessor.Owner.Handle},
	}
	out := tools.Output{
		OK: true, ResultFormat: tools.ResultFormatJSON,
		Result:         map[string]any{"job_id": predecessor.ID, "status": "completed"},
		ResultMetadata: map[string]any{"job_id": predecessor.ID, "found": true},
	}
	manager.processAsyncQueryObservationOutput(ctx, thread, run, call, &out)
	manager.persistToolResult(ctx, thread, run, call, out, RunContextPlan{})

	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 2 {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
	byJob := make(map[string]store.RunQueryDependency, len(dependencies))
	for _, dependency := range dependencies {
		byJob[dependency.JobID] = dependency
	}
	if !byJob[predecessor.ID].Detached || byJob[predecessor.ID].ReplacementJobID != successor.ID ||
		byJob[successor.ID].Detached || byJob[successor.ID].ValidationState != store.QueryValidationUnvalidated {
		t.Fatalf("late predecessor observation mutated active lineage: %#v", dependencies)
	}
	refs := (&Manager{store: repository, longHorizon: repository}).queryDependenciesForRun(thread.ID, run.ID)
	if len(refs) != 1 || refs[0].ID != successor.ID || refs[0].ValidationState == store.QueryValidationValidated {
		t.Fatalf("restart reactivated predecessor or validated successor from predecessor evidence: %#v", refs)
	}
}

func TestObservedSuccessorAdvancesCrossRunObligation(t *testing.T) {
	ctx := context.Background()
	repository, _ := store.NewJSON(t.TempDir())
	thread, _ := repository.CreateThread(ctx, nil)
	priorRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	currentRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	now := time.Now().UTC()
	predecessor := jobs.JobRecord{
		ID: "qj-dbx-cross-run-predecessor", Provider: "databricks", Status: jobs.JobStatusCancelled,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: priorRun.ID, Handle: "tok_oooooooooooooooooooooooooooooooo"},
		SubmittedAt: now, LastAccess: now, CompletedAt: &now, ValidationState: jobs.ValidationRejected,
		Metadata: map[string]any{jobs.QueryObligationManagedMetadataKey: true},
	}
	intermediate := jobs.JobRecord{
		ID: "qj-dbx-cross-run-intermediate", Provider: "databricks", Status: jobs.JobStatusCancelling,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: priorRun.ID, Handle: "tok_qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
		ReplacesJobID: predecessor.ID,
		Metadata:      map[string]any{jobs.QueryObligationManagedMetadataKey: true},
	}
	successor := jobs.JobRecord{
		ID: "qj-dbx-cross-run-successor", Provider: "databricks", Status: jobs.JobStatusCompleted,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: priorRun.ID, Handle: "tok_pppppppppppppppppppppppppppppppp"},
		SubmittedAt: now, LastAccess: now, CompletedAt: &now, ValidationState: jobs.ValidationValidated,
		ReplacesJobID: intermediate.ID,
		Metadata:      map[string]any{jobs.QueryObligationManagedMetadataKey: true},
	}
	for _, record := range []jobs.JobRecord{predecessor, intermediate, successor} {
		if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
			t.Fatalf("persist %s: %v", record.ID, err)
		}
	}
	manager := &Manager{store: repository, longHorizon: repository}
	cancelCall := types.ToolCall{
		ID: "call-cross-run-predecessor-cancel", Name: "cancel_query",
		Args: map[string]any{
			"job_id": predecessor.ID, "owner_token": predecessor.Owner.Handle, "reason": "superseded",
		},
	}
	guard, guarded, err := manager.preflightGuardedQueryCancellation(ctx, thread, currentRun, cancelCall)
	if err != nil || !guarded || !guard.NeedsBarrier || !guard.NeedsAlternate {
		t.Fatalf("predecessor guard=%#v guarded=%v err=%v", guard, guarded, err)
	}
	before, _ := repository.ListRunQueryDependencies(ctx, currentRun.ID)
	if len(before) != 1 || before[0].JobID != predecessor.ID || before[0].ValidationState != store.QueryValidationRejected {
		t.Fatalf("predecessor was not current-run active before successor observation: %#v", before)
	}
	// Persist a cross-wired predecessor observation first. Its exact call is a
	// protocol fault, but the later authenticated successor observation below
	// is allowed to supersede it because both jobs share the same durable qob.
	predecessorFault := `{"tool_name":"check_query_status","tool_args":{"job_id":"` + predecessor.ID + `","owner_token":"` + predecessor.Owner.Handle + `"},"ok":false,"result":{"job_id":"` + successor.ID + `","error":"cross-wired response"}}`
	predecessorFaultName := "check_query_status"
	predecessorFaultCallID := "call-cross-run-predecessor-status-fault"
	if _, err := repository.AppendMessage(ctx, thread.ID, &currentRun.ID, store.Message{
		Role: types.RoleTool, Content: &predecessorFault, Name: &predecessorFaultName, ToolCallID: &predecessorFaultCallID,
	}); err != nil {
		t.Fatalf("persist predecessor observation fault: %v", err)
	}

	statusCall := types.ToolCall{
		ID: "call-cross-run-successor-status", Name: "check_query_status",
		Args: map[string]any{"job_id": successor.ID, "owner_token": successor.Owner.Handle},
	}
	statusOut := tools.Output{
		OK: true, ResultFormat: tools.ResultFormatJSON,
		Result:         map[string]any{"job_id": successor.ID, "status": "completed"},
		ResultMetadata: map[string]any{"job_id": successor.ID, "found": true},
	}
	manager.processAsyncQueryObservationOutput(ctx, thread, currentRun, statusCall, &statusOut)
	message := manager.persistToolResult(ctx, thread, currentRun, statusCall, statusOut, RunContextPlan{})
	if message.Content == nil || !strings.Contains(*message.Content, successor.ID) {
		t.Fatalf("successor observation was not persisted: %#v", message.Content)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, currentRun.ID)
	if err != nil || len(dependencies) != 3 {
		t.Fatalf("advanced dependencies=%#v err=%v", dependencies, err)
	}
	byJob := make(map[string]store.RunQueryDependency, len(dependencies))
	for _, dependency := range dependencies {
		byJob[dependency.JobID] = dependency
	}
	if !byJob[predecessor.ID].Detached || byJob[predecessor.ID].ReplacementJobID != intermediate.ID ||
		!byJob[intermediate.ID].Detached || byJob[intermediate.ID].ReplacementJobID != successor.ID ||
		byJob[intermediate.ID].ValidationState != store.QueryValidationRejected ||
		byJob[successor.ID].Detached || byJob[successor.ID].ValidationState != store.QueryValidationValidated ||
		byJob[successor.ID].ObligationID != byJob[predecessor.ID].ObligationID {
		t.Fatalf("successor observation did not advance exact lineage: %#v", dependencies)
	}
	storedSuccessor, found, err := repository.GetQueryJob(ctx, successor.ID)
	if err != nil || !found || storedSuccessor.Owner.RunID != priorRun.ID {
		t.Fatalf("cross-run successor ownership mutated: %#v found=%v err=%v", storedSuccessor, found, err)
	}
	if refs := (&Manager{store: repository, longHorizon: repository}).queryDependenciesForRun(thread.ID, currentRun.ID); len(refs) != 0 {
		t.Fatalf("validated observed successor did not satisfy current-run obligation: %#v", refs)
	}
}

func TestPersistedFailedObservationRejectsCrossWiredJobIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, _ := store.NewJSON(t.TempDir())
	thread, _ := repository.CreateThread(ctx, nil)
	ownerRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	observerRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	now := time.Now().UTC()
	requested := jobs.JobRecord{
		ID: "qj-dbx-failed-observation-requested", Provider: "databricks", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: ownerRun.ID, Handle: "tok_rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
	}
	crossWired := jobs.JobRecord{
		ID: "qj-dbx-failed-observation-crosswired", Provider: "databricks", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: ownerRun.ID, Handle: "tok_ssssssssssssssssssssssssssssssss"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
	}
	for _, record := range []jobs.JobRecord{requested, crossWired} {
		if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
			t.Fatalf("persist %s: %v", record.ID, err)
		}
	}
	content := `{"tool_name":"check_query_status","tool_args":{"job_id":"` + requested.ID + `","owner_token":"` + requested.Owner.Handle + `"},"ok":false,"result":{"job_id":"` + crossWired.ID + `","error":"provider response failed"}}`
	name, callID := "check_query_status", "call-failed-crosswired-observation"
	if _, err := repository.AppendMessage(ctx, thread.ID, &observerRun.ID, store.Message{
		Role: types.RoleTool, Content: &content, Name: &name, ToolCallID: &callID,
	}); err != nil {
		t.Fatalf("append cross-wired failure: %v", err)
	}

	refs := (&Manager{store: repository, longHorizon: repository}).queryDependenciesForRun(thread.ID, observerRun.ID)
	if len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") {
		t.Fatalf("ok=false cross-wired lifecycle identity was accepted: %#v", refs)
	}
	if dependencies, err := repository.ListRunQueryDependencies(ctx, observerRun.ID); err != nil || len(dependencies) != 0 {
		t.Fatalf("cross-wired failure adopted the wrong query: %#v err=%v", dependencies, err)
	}
}

func TestNewerUnverifiedRunActionOutranksOlderValidObservationOnRestart(t *testing.T) {
	ctx := context.Background()
	repository, _ := store.NewJSON(t.TempDir())
	thread, _ := repository.CreateThread(ctx, nil)
	run, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID: "qj-dbx-older-valid-observation", Provider: "databricks", Status: jobs.JobStatusCompleted,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "tok_hhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhh"},
		SubmittedAt: now, LastAccess: now, CompletedAt: &now, ValidationState: jobs.ValidationValidated,
		Metadata: map[string]any{jobs.QueryObligationManagedMetadataKey: true},
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
		t.Fatalf("persist observed job: %v", err)
	}
	observation := `{"tool_name":"check_query_status","tool_args":{"job_id":"` + record.ID + `","owner_token":"` + record.Owner.Handle + `"},"ok":true,"result":{"job_id":"` + record.ID + `","status":"completed"},"result_metadata":{"found":true,"job_id":"` + record.ID + `"}}`
	observationName, observationCallID := "check_query_status", "call-older-valid-observation"
	if _, err := repository.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
		Role: types.RoleTool, Content: &observation, Name: &observationName, ToolCallID: &observationCallID,
	}); err != nil {
		t.Fatalf("append observation: %v", err)
	}
	newerFailure := `{"tool_name":"run_databricks_query","ok":true,"result":{"message":"query submission started"}}`
	runName, runCallID := "run_databricks_query", "call-newer-unverified-run"
	if _, err := repository.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
		Role: types.RoleTool, Content: &newerFailure, Name: &runName, ToolCallID: &runCallID,
	}); err != nil {
		t.Fatalf("append newer run action: %v", err)
	}

	refs := (&Manager{store: repository, longHorizon: repository}).queryDependenciesForRun(thread.ID, run.ID)
	if len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") {
		t.Fatalf("older valid observation erased newer unresolved run action: %#v", refs)
	}
}

func TestRunQueryPreflightLineageMismatchPreservesBothObligations(t *testing.T) {
	ctx := context.Background()
	repository, _ := store.NewJSON(t.TempDir())
	thread, _ := repository.CreateThread(ctx, nil)
	run, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	now := time.Now().UTC()
	expectedJob := jobs.JobRecord{
		ID: "qj-dbx-expected-lineage", Provider: "databricks", Status: jobs.JobStatusFailed,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "tok_iiiiiiiiiiiiiiiiiiiiiiiiiiiiiiii"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationRejected,
		Metadata: map[string]any{jobs.QueryObligationManagedMetadataKey: true},
	}
	returnedJob := jobs.JobRecord{
		ID: "qj-dbx-wrong-lineage", Provider: "databricks", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "tok_jjjjjjjjjjjjjjjjjjjjjjjjjjjjjjjj"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
		Metadata: map[string]any{jobs.QueryObligationManagedMetadataKey: true},
	}
	for _, record := range []jobs.JobRecord{expectedJob, returnedJob} {
		if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
			t.Fatalf("persist %s: %v", record.ID, err)
		}
	}
	expectedObligationID, err := store.QueryObligationIDForRoot(run.ID, expectedJob.ID)
	if err != nil {
		t.Fatalf("derive expected obligation: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: run.ID, JobID: expectedJob.ID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationRejected, ObligationID: expectedObligationID,
	}); err != nil {
		t.Fatalf("persist expected dependency: %v", err)
	}
	manager := &Manager{store: repository, longHorizon: repository}
	call := types.ToolCall{
		ID: "call-wrong-lineage", Name: "run_databricks_query",
		Args: map[string]any{"sql": "select 1", "query_obligation_id": expectedObligationID},
	}
	out := tools.Output{
		OK: true, ResultFormat: tools.ResultFormatJSON,
		Result:        map[string]any{"job_id": returnedJob.ID, "status": "running"},
		QueryMetadata: map[string]any{"job_id": returnedJob.ID},
	}
	manager.processAsyncQueryRunOutput(ctx, thread, run, call, call.Name, expectedObligationID, &out)
	raw, _ := json.Marshal(out)
	if out.OK || !strings.Contains(string(raw), "query_obligation_lineage_mismatch") {
		t.Fatalf("wrong-lineage result was not rejected: %s", raw)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 2 {
		t.Fatalf("wrong-lineage result abandoned a durable obligation: %#v err=%v", dependencies, err)
	}
	foundExpected, foundReturned := false, false
	for _, dependency := range dependencies {
		foundExpected = foundExpected || (dependency.JobID == expectedJob.ID && dependency.ObligationID == expectedObligationID && !dependency.Detached)
		foundReturned = foundReturned || (dependency.JobID == returnedJob.ID && dependency.ObligationID != expectedObligationID && !dependency.Detached)
	}
	if !foundExpected || !foundReturned {
		t.Fatalf("lineage mismatch did not preserve distinct active obligations: %#v", dependencies)
	}
	if refs := manager.queryDependencyFaultRefs(thread.ID, run.ID); len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") {
		t.Fatalf("lineage mismatch did not establish a protocol barrier: %#v", refs)
	}
}

func TestCrossRunWrongQueryCancellationRequiresDurableReplacement(t *testing.T) {
	ctx := context.Background()
	repository, _ := store.NewJSON(t.TempDir())
	thread, _ := repository.CreateThread(ctx, nil)
	priorRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	currentRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	registry := jobs.NewRegistry(jobs.RegistryOptions{Store: repository})
	owner := jobs.JobOwner{ThreadID: thread.ID, RunID: priorRun.ID, Handle: "tok_kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk"}
	job, err := registry.Register(
		owner, "databricks", "select incorrect", "select incorrect",
		map[string]any{jobs.QueryObligationManagedMetadataKey: true}, nil, func(context.Context) error { return nil },
	)
	if err != nil {
		t.Fatalf("register prior-run query: %v", err)
	}
	manager := &Manager{
		store: repository, tools: tools.NewRegistry(nil), queryJobs: registry,
		longHorizon: repository,
	}
	call := types.ToolCall{
		ID: "call-cross-run-wrong-cancel", Name: "cancel_query",
		ArgsText: `{"job_id":"` + job.ID + `","owner_token":"` + owner.Handle + `","reason":"wrong_query"}`,
	}
	message, err := manager.execTool(
		ctx, thread, currentRun, testProjectID(""), call, false, "shallow", 0, nil,
		map[string]bool{"databricks": true}, defaultChartEngineEnablement(), false, false, true, true, false,
		atlassianEnablement{},
		false,
		false,
		slackToolAccess{},
	)
	if err != nil || message.Content == nil {
		t.Fatalf("exec cancel: message=%#v err=%v", message, err)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, currentRun.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].JobID != job.ID ||
		dependencies[0].ValidationState != store.QueryValidationRejected || dependencies[0].Detached ||
		store.ValidateQueryObligationID(dependencies[0].ObligationID) != nil {
		t.Fatalf("cross-run cancellation dependency=%#v err=%v", dependencies, err)
	}
	if !strings.Contains(*message.Content, dependencies[0].ObligationID) ||
		!strings.Contains(*message.Content, `"run_id":"`+currentRun.ID+`"`) ||
		!strings.Contains(*message.Content, `"replacement_required":true`) {
		t.Fatalf("cancellation output did not carry current-run replacement obligation: %s", *message.Content)
	}
	status, ok := registry.Snapshot(job.ID, owner)
	if !ok || (status.Status != jobs.JobStatusCancelRequested && status.Status != jobs.JobStatusCancelling && status.Status != jobs.JobStatusCancelled) {
		t.Fatalf("guarded cancellation was not dispatched after durable adoption: %#v ok=%v", status, ok)
	}
	if refs := manager.queryDependenciesForRun(thread.ID, currentRun.ID); len(refs) != 1 || refs[0].ID != job.ID {
		t.Fatalf("cancelled wrong query no longer blocked on a replacement: %#v", refs)
	}
}

func TestCrossRunCancellationPreflightFailureDoesNotDispatchAndRecovers(t *testing.T) {
	ctx := context.Background()
	repository, _ := store.NewJSON(t.TempDir())
	thread, _ := repository.CreateThread(ctx, nil)
	priorRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	currentRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	registry := jobs.NewRegistry(jobs.RegistryOptions{Store: repository})
	owner := jobs.JobOwner{ThreadID: thread.ID, RunID: priorRun.ID, Handle: "tok_llllllllllllllllllllllllllllllll"}
	job, err := registry.Register(
		owner, "databricks", "select incorrect", "select incorrect",
		map[string]any{jobs.QueryObligationManagedMetadataKey: true}, nil, func(context.Context) error { return nil },
	)
	if err != nil {
		t.Fatalf("register prior-run query: %v", err)
	}
	failing := &failDependencyUpsertRepository{LongHorizonRepository: repository, remainingFailures: 1}
	manager := &Manager{
		store: repository, tools: tools.NewRegistry(nil), queryJobs: registry,
		longHorizon: failing,
	}
	call := types.ToolCall{
		ID: "call-cross-run-cancel-store-failure", Name: "cancel_query",
		ArgsText: `{"job_id":"` + job.ID + `","owner_token":"` + owner.Handle + `","reason":"wrong_query"}`,
	}
	message, err := manager.execTool(
		ctx, thread, currentRun, testProjectID(""), call, false, "shallow", 0, nil,
		map[string]bool{"databricks": true}, defaultChartEngineEnablement(), false, false, true, true, false,
		atlassianEnablement{},
		false,
		false,
		slackToolAccess{},
	)
	if err != nil || message.Content == nil || !strings.Contains(*message.Content, "query_cancellation_preflight_failed") {
		t.Fatalf("cancel preflight failure: message=%#v err=%v", message, err)
	}
	status, ok := registry.Snapshot(job.ID, owner)
	if !ok || status.CancelReason != "" || status.Status != jobs.JobStatusSubmitted {
		t.Fatalf("cancellation dispatched before durable adoption: %#v ok=%v", status, ok)
	}
	if dependencies, _ := repository.ListRunQueryDependencies(ctx, currentRun.ID); len(dependencies) != 0 {
		t.Fatalf("failed preflight unexpectedly committed dependency: %#v", dependencies)
	}
	if refs := manager.queryDependencyFaultRefs(thread.ID, currentRun.ID); len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") {
		t.Fatalf("failed cancel preflight did not block completion: %#v", refs)
	}
	// A successful, unrelated lifecycle action may clear only its own fault;
	// it cannot erase the failed cancellation obligation before recovery.
	now := time.Now().UTC()
	unrelated := jobs.JobRecord{
		ID: "qj-dbx-unrelated-after-cancel-failure", Provider: "databricks", Status: jobs.JobStatusCompleted,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: currentRun.ID, Handle: "tok_nnnnnnnnnnnnnnnnnnnnnnnnnnnnnnnn"},
		SubmittedAt: now, LastAccess: now, CompletedAt: &now, ValidationState: jobs.ValidationValidated,
		Metadata: map[string]any{jobs.QueryObligationManagedMetadataKey: true},
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, unrelated, 0); err != nil {
		t.Fatalf("persist unrelated observation: %v", err)
	}
	unrelatedCall := types.ToolCall{
		ID: "call-unrelated-after-cancel-failure", Name: "check_query_status",
		Args: map[string]any{"job_id": unrelated.ID, "owner_token": unrelated.Owner.Handle},
	}
	unrelatedOut := tools.Output{
		OK: true, ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{"job_id": unrelated.ID, "status": "completed"},
	}
	manager.processAsyncQueryObservationOutput(ctx, thread, currentRun, unrelatedCall, &unrelatedOut)
	if refs := manager.queryDependencyFaultRefs(thread.ID, currentRun.ID); len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") {
		t.Fatalf("unrelated success erased failed cancellation barrier: %#v", refs)
	}

	// The persisted exact cancel action is restart/live-recovery evidence. Once
	// storage recovers, the barrier adopts and rejects the attempt without ever
	// replaying the provider cancellation side effect.
	refs := manager.queryDependenciesForRun(thread.ID, currentRun.ID)
	if len(refs) != 1 || refs[0].ID != job.ID {
		t.Fatalf("cancel preflight barrier did not self-heal: %#v", refs)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, currentRun.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].JobID != job.ID ||
		dependencies[0].ValidationState != store.QueryValidationRejected {
		t.Fatalf("recovered cancel dependency=%#v err=%v", dependencies, err)
	}
	status, _ = registry.Snapshot(job.ID, owner)
	if status.CancelReason != "" || status.Status != jobs.JobStatusSubmitted {
		t.Fatalf("recovery replayed cancellation side effect: %#v", status)
	}
	restartedRefs := (&Manager{store: repository, longHorizon: repository}).queryDependenciesForRun(thread.ID, currentRun.ID)
	if len(restartedRefs) != 1 || restartedRefs[0].ID != job.ID {
		t.Fatalf("restart lost recovered cancellation obligation: %#v", restartedRefs)
	}
}

func TestDeniedCrossRunCancellationStillAdoptsWaitObligation(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	repository, _ := store.NewJSON(cfgDir)
	thread, _ := repository.CreateThread(ctx, nil)
	priorRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	currentRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	registry := jobs.NewRegistry(jobs.RegistryOptions{Store: repository})
	owner := jobs.JobOwner{ThreadID: thread.ID, RunID: priorRun.ID, Handle: "tok_mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm"}
	job, err := registry.Register(
		owner, "databricks", "select incorrect", "select incorrect",
		map[string]any{jobs.QueryObligationManagedMetadataKey: true}, nil, func(context.Context) error { return nil },
	)
	if err != nil {
		t.Fatalf("register prior-run query: %v", err)
	}
	permissionService, err := permissions.NewService(cfgDir, repository)
	if err != nil {
		t.Fatalf("permissions service: %v", err)
	}
	if _, err := permissionService.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryExecute, Effect: permissions.EffectDeny, Scope: "global",
		Subject:    store.PermissionSubject{Kind: "tool", Tool: "cancel_query", Operation: "cancel_query"},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("deny cancel permission: %v", err)
	}
	manager := &Manager{
		store: repository, tools: tools.NewRegistry(nil), queryJobs: registry,
		longHorizon: repository, permissions: permissionService, cfgDir: cfgDir,
	}
	call := types.ToolCall{
		ID: "call-denied-cross-run-cancel", Name: "cancel_query",
		ArgsText: `{"job_id":"` + job.ID + `","owner_token":"` + owner.Handle + `","reason":"wrong_query"}`,
	}
	message, err := manager.execTool(
		ctx, thread, currentRun, testProjectID(""), call, false, "shallow", 0, nil,
		map[string]bool{"databricks": true}, defaultChartEngineEnablement(), false, false, true, true, false,
		atlassianEnablement{},
		false,
		false,
		slackToolAccess{},
	)
	if err != nil || message.Content == nil || !strings.Contains(*message.Content, "approval_denied") {
		t.Fatalf("denied cancel result: message=%#v err=%v", message, err)
	}
	status, ok := registry.Snapshot(job.ID, owner)
	if !ok || status.Status != jobs.JobStatusSubmitted || status.CancelReason != "" {
		t.Fatalf("denied cancellation reached provider lifecycle: %#v ok=%v", status, ok)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, currentRun.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].JobID != job.ID ||
		dependencies[0].ValidationState != store.QueryValidationUnvalidated || dependencies[0].Detached {
		t.Fatalf("denied cancellation wait dependency=%#v err=%v", dependencies, err)
	}
	if !strings.Contains(*message.Content, dependencies[0].ObligationID) ||
		!strings.Contains(*message.Content, "Cancellation was not approved and was not sent") ||
		strings.Contains(*message.Content, "This cancelled job") {
		t.Fatalf("denied cancellation did not explain durable wait obligation: %s", *message.Content)
	}
	refs := (&Manager{store: repository, longHorizon: repository}).queryDependenciesForRun(thread.ID, currentRun.ID)
	if len(refs) != 1 || refs[0].ID != job.ID {
		t.Fatalf("restart lost denied-cancel wait obligation: %#v", refs)
	}
	dependencies, _ = repository.ListRunQueryDependencies(ctx, currentRun.ID)
	if len(dependencies) != 1 || dependencies[0].ValidationState != store.QueryValidationUnvalidated {
		t.Fatalf("restart converted denied cancellation into semantic rejection: %#v", dependencies)
	}
	approvalError := `{"tool_name":"cancel_query","tool_args":{"job_id":"` + job.ID + `","owner_token":"` + owner.Handle + `","reason":"wrong_query"},"ok":false,"result":{"status":"approval_error","error":"approval service unavailable"}}`
	approvalErrorName, approvalErrorCallID := "cancel_query", "call-approval-error-cross-run-cancel"
	if _, err := repository.AppendMessage(ctx, thread.ID, &currentRun.ID, store.Message{
		Role: types.RoleTool, Content: &approvalError, Name: &approvalErrorName, ToolCallID: &approvalErrorCallID,
	}); err != nil {
		t.Fatalf("append approval error: %v", err)
	}
	if refs := (&Manager{store: repository, longHorizon: repository}).queryDependenciesForRun(thread.ID, currentRun.ID); len(refs) != 1 || refs[0].ID != job.ID {
		t.Fatalf("approval error lost wait obligation: %#v", refs)
	}
	dependencies, _ = repository.ListRunQueryDependencies(ctx, currentRun.ID)
	if len(dependencies) != 1 || dependencies[0].ValidationState != store.QueryValidationUnvalidated {
		t.Fatalf("approval error was replayed as an executed cancellation: %#v", dependencies)
	}
}

func TestCrossRunValidationAnchorFailureRecoversWithoutChangingVerdict(t *testing.T) {
	ctx := context.Background()
	repository, _ := store.NewJSON(t.TempDir())
	thread, _ := repository.CreateThread(ctx, nil)
	priorRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	currentRun, _ := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	registry := jobs.NewRegistry(jobs.RegistryOptions{Store: repository})
	owner := jobs.JobOwner{ThreadID: thread.ID, RunID: priorRun.ID, Handle: "tok_ssssssssssssssssssssssssssssssss"}
	job, err := registry.Register(
		owner, "databricks", "select completed", "select completed",
		map[string]any{jobs.QueryObligationManagedMetadataKey: true},
		func(context.Context) (bool, *jobs.QueryResult, error) {
			return true, &jobs.QueryResult{Rows: []map[string]any{{"n": 1}}}, nil
		}, nil,
	)
	if err != nil {
		t.Fatalf("register prior-run result: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, ok := registry.Snapshot(job.ID, owner)
		if ok && snapshot.Status == jobs.JobStatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("query did not complete for validation: %#v", snapshot)
		}
		time.Sleep(5 * time.Millisecond)
	}
	failing := &failDependencyUpsertRepository{LongHorizonRepository: repository, remainingFailures: 4}
	manager := &Manager{
		store: repository, tools: tools.NewRegistry(nil), queryJobs: registry,
		longHorizon: failing,
	}
	call := types.ToolCall{
		ID: "call-cross-run-validation-anchor-failure", Name: "validate_query_result",
		ArgsText: `{"job_id":"` + job.ID + `","owner_token":"` + owner.Handle + `","verdict":"rejected","detail":"Wrong business filter."}`,
	}
	message, err := manager.execTool(
		ctx, thread, currentRun, testProjectID(""), call, false, "shallow", 0, nil,
		map[string]bool{"databricks": true}, defaultChartEngineEnablement(), false, false, true, true, false,
		atlassianEnablement{},
		false,
		false,
		slackToolAccess{},
	)
	if err != nil || message.Content == nil || !strings.Contains(*message.Content, "query_observation_adoption_pending") {
		t.Fatalf("validation anchor failure: message=%#v err=%v", message, err)
	}
	record, found, err := repository.GetQueryJob(ctx, job.ID)
	if err != nil || !found || record.ValidationState != jobs.ValidationUnvalidated {
		t.Fatalf("validation verdict changed without anchor: record=%#v found=%v err=%v", record, found, err)
	}
	if dependencies, _ := repository.ListRunQueryDependencies(ctx, currentRun.ID); len(dependencies) != 0 {
		t.Fatalf("injected anchor failures unexpectedly committed dependency: %#v", dependencies)
	}
	if refs := manager.queryDependencyFaultRefs(thread.ID, currentRun.ID); len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") {
		t.Fatalf("validation anchor failure did not establish protocol barrier: %#v", refs)
	}
	refs := manager.queryDependenciesForRun(thread.ID, currentRun.ID)
	if len(refs) != 1 || refs[0].ID != job.ID {
		t.Fatalf("validation action did not self-heal into durable dependency: %#v", refs)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, currentRun.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].JobID != job.ID ||
		dependencies[0].ValidationState != store.QueryValidationUnvalidated {
		t.Fatalf("recovered validation dependency=%#v err=%v", dependencies, err)
	}
	if restarted := (&Manager{store: repository, longHorizon: repository}).queryDependenciesForRun(thread.ID, currentRun.ID); len(restarted) != 1 || restarted[0].ID != job.ID {
		t.Fatalf("restart lost recovered validation obligation: %#v", restarted)
	}
}
