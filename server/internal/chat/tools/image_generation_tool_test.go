package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/store"
	chattypes "ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/chat/usage"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

func TestGenerateImageDryRunDoesNotCallProvider(t *testing.T) {
	settings, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := settings.Get()
	enabled := true
	providerID := "image-provider"
	baseURL := "https://images.example.test/v1"
	app.ImageGeneration.Enabled = &enabled
	app.ImageGeneration.ProviderID = &providerID
	app.ProviderConfigs = []config.ProviderConfig{{ID: providerID, DisplayName: "Images", ProviderID: "compatible", BaseURL: &baseURL}}
	if err := settings.Save(app); err != nil {
		t.Fatal(err)
	}
	secretStore, err := secrets.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := secretStore.Init("test passphrase"); err != nil {
		t.Fatal(err)
	}
	if err := secretStore.Unlock("test passphrase"); err != nil {
		t.Fatal(err)
	}
	if err := secretStore.SetCompatibleAPIKey(providerID, "test-key"); err != nil {
		t.Fatal(err)
	}

	tool := newGenerateImageTool(settings, secretStore)
	out, err := tool.Execute(context.Background(), ExecContext{RunID: "tool_test"}, json.RawMessage(`{"prompt":"A blue circle","dry_run":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !out.OK {
		t.Fatalf("dry run output = %#v", out)
	}
	if out.ResultMetadata["dry_run"] != true {
		t.Fatalf("result metadata = %#v", out.ResultMetadata)
	}
}

func TestGenerateImageRequiresExplicitProvider(t *testing.T) {
	settings, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := settings.Get()
	enabled := true
	app.ImageGeneration.Enabled = &enabled
	app.ImageGeneration.ProviderID = nil
	if err := settings.Save(app); err != nil {
		t.Fatal(err)
	}
	tool := newGenerateImageTool(settings, &secrets.Store{})
	out, err := tool.Execute(context.Background(), ExecContext{RunID: "tool_test"}, json.RawMessage(`{"prompt":"A blue circle","dry_run":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.OK || out.ResultMetadata["error_category"] != "not_configured" {
		t.Fatalf("output = %#v", out)
	}
}

func TestGenerateImageRecordsExactUsageBeforeReturningArtifact(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/images/generations" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "image-request-1")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"b64_json": testPNGBase64(t)}},
			"usage": map[string]any{
				"input_tokens": 43, "output_tokens": 229, "total_tokens": 272,
				"input_tokens_details":  map[string]any{"text_tokens": 43, "image_tokens": 0},
				"output_tokens_details": map[string]any{"text_tokens": 0, "image_tokens": 229},
			},
		})
	}))
	defer server.Close()

	tool, repository, providerID := configuredImageTool(t, server.URL+"/v1")
	ctx := usage.WithAttribution(context.Background(), usage.Attribution{RunID: "run-image", ThreadID: "thread-image", Operation: usage.OperationChat})
	out, err := tool.Execute(ctx, ExecContext{RunID: "run-image", ThreadID: "thread-image", Usage: repository}, json.RawMessage(`{"prompt":"A blue circle","quality":"low"}`))
	if err != nil || !out.OK || out.Artifact == nil {
		t.Fatalf("Execute() output=%#v err=%v", out, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("provider requests = %d, want 1", requests.Load())
	}
	rows, err := repository.List(context.Background(), usage.Query{Operation: usage.OperationImageGeneration})
	if err != nil || len(rows) != 1 {
		t.Fatalf("usage rows=%#v err=%v", rows, err)
	}
	row := rows[0]
	if row.Status != usage.StatusCompleted || row.ProviderID != "compatible" || stringValueForTest(row.ConnectionID) != providerID || row.DisplayModel() != "gpt-image-2" {
		t.Fatalf("usage identity = %#v", row)
	}
	if stringValueForTest(row.PlannedAPISurface) != "images" || stringValueForTest(row.EffectiveAPISurface) != "images" || row.UpstreamRequestCount != 1 {
		t.Fatalf("usage dispatch = %#v", row)
	}
	if int64ValueForTest(row.InputTokens) != 43 || int64ValueForTest(row.OutputTokens) != 229 || int64ValueForTest(row.ProviderTotalTokens) != 272 {
		t.Fatalf("usage counters = %#v", row)
	}
	if row.SanitizedDetails["input_text_tokens"] != 43 || row.SanitizedDetails["output_image_tokens"] != 229 || row.SanitizedDetails["generated_images"] != 1 {
		t.Fatalf("usage details = %#v", row.SanitizedDetails)
	}
	if got := out.ResultMetadata["usage_invocation_id"]; got != row.ID {
		t.Fatalf("result usage invocation id = %#v, want %q", got, row.ID)
	}
	if out.ResultMetadata["provider_id"] != "compatible" || out.ResultMetadata["connection_id"] != providerID {
		t.Fatalf("result provider identity = %#v", out.ResultMetadata)
	}
	totals, err := repository.SumByRun(context.Background(), "run-image")
	if err != nil || totals.InvocationCount != 1 || totals.InputTokens != 43 || totals.OutputTokens != 229 || totals.TotalTokens != 272 {
		t.Fatalf("run totals=%#v err=%v", totals, err)
	}
}

func TestGenerateImageRetriesRateLimitWithinOneUsageInvocation(t *testing.T) {
	var requests atomic.Int64
	var idempotencyKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		if attempt == 1 {
			idempotencyKey = r.Header.Get("Idempotency-Key")
		} else if got := r.Header.Get("Idempotency-Key"); got == "" || got != idempotencyKey {
			t.Errorf("idempotency key = %q, want stable %q", got, idempotencyKey)
		}
		if attempt == 1 {
			http.Error(w, `{"error":{"message":"busy"}}`, http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data":  []map[string]any{{"b64_json": testPNGBase64(t)}},
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 20, "total_tokens": 30},
		})
	}))
	defer server.Close()

	tool, repository, _ := configuredImageTool(t, server.URL+"/v1")
	out, err := tool.Execute(context.Background(), ExecContext{RunID: "run-retry", ThreadID: "thread-retry", Usage: repository}, json.RawMessage(`{"prompt":"Retry image"}`))
	if err != nil || !out.OK {
		t.Fatalf("Execute() output=%#v err=%v", out, err)
	}
	if idempotencyKey == "" {
		t.Fatal("missing idempotency key")
	}
	rows, err := repository.List(context.Background(), usage.Query{Operation: usage.OperationImageGeneration})
	if err != nil || len(rows) != 1 || rows[0].UpstreamRequestCount != 2 || int64ValueForTest(rows[0].ProviderTotalTokens) != 30 {
		t.Fatalf("retry usage rows=%#v err=%v", rows, err)
	}
}

func TestGenerateImageDoesNotRetryAmbiguousServerFailureAndRecordsUsage(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("x-request-id", "failed-image-request")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "response lost after generation"},
			"usage": map[string]any{"input_tokens": 12, "output_tokens": 44, "total_tokens": 56},
		})
	}))
	defer server.Close()

	tool, repository, _ := configuredImageTool(t, server.URL+"/v1")
	out, err := tool.Execute(context.Background(), ExecContext{RunID: "run-ambiguous", ThreadID: "thread-ambiguous", Usage: repository}, json.RawMessage(`{"prompt":"Do not duplicate"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.OK || requests.Load() != 1 {
		t.Fatalf("output=%#v provider requests=%d", out, requests.Load())
	}
	rows, err := repository.List(context.Background(), usage.Query{Operation: usage.OperationImageGeneration})
	if err != nil || len(rows) != 1 {
		t.Fatalf("usage rows=%#v err=%v", rows, err)
	}
	row := rows[0]
	if row.Status != usage.StatusFailed || row.UpstreamRequestCount != 1 || int64ValueForTest(row.ProviderTotalTokens) != 56 {
		t.Fatalf("failed response usage = %#v", row)
	}
	if row.SanitizedDetails["generated_images"] != 0 || stringValueForTest(row.ProviderRequestID) != "failed-image-request" {
		t.Fatalf("failed response metadata = %#v", row)
	}
}

func TestImageUsageSnapshotSurvivesRestartBeforeFinalization(t *testing.T) {
	cfgDir := t.TempDir()
	primary, err := usage.NewJSONRepository(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	recorder, err := usage.NewRecorder(cfgDir, primary)
	if err != nil {
		t.Fatal(err)
	}
	provider := config.ProviderConfig{ID: "image-connection", ProviderID: "compatible"}
	tracker, err := beginImageUsage(context.Background(), ExecContext{
		RunID: "run-crash", ThreadID: "thread-crash", Usage: recorder,
	}, provider, "gpt-image-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := tracker.dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := tracker.observe(map[string]any{
		"input_tokens": 31, "output_tokens": 97, "total_tokens": 128,
		"input_tokens_details":  map[string]any{"text_tokens": 31},
		"output_tokens_details": map[string]any{"image_tokens": 97},
	}, "request-before-crash", true); err != nil {
		t.Fatal(err)
	}

	// Deliberately do not call finish: constructing a fresh recorder simulates
	// process restart and terminalizes the durable running invocation.
	recovered, err := usage.NewRecorder(cfgDir, primary)
	if err != nil {
		t.Fatal(err)
	}
	row, err := recovered.Get(context.Background(), tracker.invocationID())
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != usage.StatusInterrupted || int64ValueForTest(row.InputTokens) != 31 || int64ValueForTest(row.OutputTokens) != 97 || int64ValueForTest(row.ProviderTotalTokens) != 128 {
		t.Fatalf("recovered usage = %#v", row)
	}
	if row.UpstreamRequestCount != 1 || stringValueForTest(row.ProviderRequestID) != "request-before-crash" || row.SanitizedDetails["generated_images"] != 1 {
		t.Fatalf("recovered usage metadata = %#v", row)
	}
}

func TestGenerateImageFailsClosedWithoutUsageRepository(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer server.Close()
	tool, _, _ := configuredImageTool(t, server.URL+"/v1")
	out, err := tool.Execute(context.Background(), ExecContext{RunID: "run-untracked", ThreadID: "thread-untracked"}, json.RawMessage(`{"prompt":"Do not bill"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.OK || out.ResultMetadata["error_category"] != "accounting_unavailable" || requests.Load() != 0 {
		t.Fatalf("output=%#v requests=%d", out, requests.Load())
	}
}

func TestBackfillImageGenerationUsageIsIdempotent(t *testing.T) {
	cfgDir := t.TempDir()
	chatStore, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := usage.NewJSONRepository(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := chatStore.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	toolName := generateImageToolName
	content := "data:image/png;base64," + testPNGBase64(t)
	_, err = chatStore.InsertArtifact(context.Background(), store.Artifact{
		ID: chattypes.NewUUID(), ThreadID: thread.ID, Type: "image", Title: "Historical image",
		ToolName: &toolName, Storage: "inline", ContentText: &content,
		Metadata: map[string]any{
			"tool_name": generateImageToolName, "run_id": "run-backfill", "provider_id": "image-connection", "model": "gpt-image-2",
			"result_metadata": map[string]any{
				"provider_id": "image-connection", "model": "gpt-image-2", "request_id": "backfill-request",
				"usage": map[string]any{"input_tokens": 7, "output_tokens": 11, "total_tokens": 18},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	settings := config.AppSettings{ProviderConfigs: []config.ProviderConfig{{ID: "image-connection", ProviderID: "compatible"}}}
	imported, err := BackfillImageGenerationUsage(context.Background(), chatStore, repository, settings)
	if err != nil || imported != 1 {
		t.Fatalf("first backfill imported=%d err=%v", imported, err)
	}
	imported, err = BackfillImageGenerationUsage(context.Background(), chatStore, repository, settings)
	if err != nil || imported != 0 {
		t.Fatalf("second backfill imported=%d err=%v", imported, err)
	}
	rows, err := repository.List(context.Background(), usage.Query{Operation: usage.OperationImageGeneration})
	if err != nil || len(rows) != 1 || int64ValueForTest(rows[0].ProviderTotalTokens) != 18 || rows[0].SanitizedDetails["generated_images"] != 1 {
		t.Fatalf("backfilled rows=%#v err=%v", rows, err)
	}
}

func TestBackfillImageGenerationUsageContinuesPastMalformedArtifact(t *testing.T) {
	repository, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	toolName := generateImageToolName
	createdAt := time.Now().UTC()
	artifacts := []store.Artifact{
		{
			ID: chattypes.NewUUID(), ThreadID: chattypes.NewUUID(), Type: "image", ToolName: &toolName, CreatedAt: createdAt,
			Metadata: map[string]any{"tool_name": generateImageToolName, "model": "gpt-image-2", "result_metadata": map[string]any{
				"usage": map[string]any{"total_tokens": "not-a-number"},
			}},
		},
		{
			ID: chattypes.NewUUID(), ThreadID: chattypes.NewUUID(), Type: "image", ToolName: &toolName, CreatedAt: createdAt,
			Metadata: map[string]any{"tool_name": generateImageToolName, "model": "gpt-image-2", "result_metadata": map[string]any{
				"usage": map[string]any{"input_tokens": 5, "output_tokens": 8, "total_tokens": 13},
			}},
		},
	}
	imported, backfillErr := backfillImageArtifacts(context.Background(), artifacts, repository, map[string]string{})
	if imported != 2 || backfillErr != nil {
		t.Fatalf("backfill imported=%d err=%v", imported, backfillErr)
	}
	rows, err := repository.List(context.Background(), usage.Query{Operation: usage.OperationImageGeneration})
	if err != nil || len(rows) != 2 {
		t.Fatalf("backfilled rows=%#v err=%v", rows, err)
	}
	var foundExact, foundMissing bool
	for _, row := range rows {
		switch row.UsageQuality {
		case usage.QualityProviderReported:
			foundExact = int64ValueForTest(row.ProviderTotalTokens) == 13 && row.SanitizedDetails["generated_images"] == 1
		case usage.QualityMissing:
			foundMissing = row.ProviderTotalTokens == nil && row.SanitizedDetails["generated_images"] == 1 && row.SanitizedDetails["usage_payload_unrecognized"] == 1
		}
	}
	if !foundExact || !foundMissing {
		t.Fatalf("expected exact and transparently missing backfill rows, got %#v", rows)
	}
}

func configuredImageTool(t *testing.T, baseURL string) (Tool, usage.Repository, string) {
	t.Helper()
	settings, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := settings.Get()
	enabled := true
	providerID := "image-provider"
	model := "gpt-image-2"
	app.ImageGeneration.Enabled = &enabled
	app.ImageGeneration.ProviderID = &providerID
	app.ImageGeneration.Model = &model
	app.ProviderConfigs = []config.ProviderConfig{{ID: providerID, DisplayName: "Images", ProviderID: "compatible", BaseURL: &baseURL}}
	if err := settings.Save(app); err != nil {
		t.Fatal(err)
	}
	secretStore, err := secrets.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := secretStore.Init("test passphrase"); err != nil {
		t.Fatal(err)
	}
	if err := secretStore.Unlock("test passphrase"); err != nil {
		t.Fatal(err)
	}
	if err := secretStore.SetCompatibleAPIKey(providerID, "test-key"); err != nil {
		t.Fatal(err)
	}
	repository, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return newGenerateImageTool(settings, secretStore), repository, providerID
}

func testPNGBase64(t *testing.T) string {
	t.Helper()
	var encoded bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 40, G: 80, B: 220, A: 255})
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(encoded.Bytes())
}

func stringValueForTest(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func int64ValueForTest(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
