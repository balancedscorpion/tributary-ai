package usage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDailyUsageHTTPFlattensBucketsAndUsesRequestedTimezone(t *testing.T) {
	repository, err := NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	started := time.Date(2026, 7, 10, 23, 25, 0, 0, time.UTC)
	ended := time.Date(2026, 7, 10, 23, 30, 0, 0, time.UTC) // 11 July in Europe/London.
	measured, err := repository.Start(context.Background(), Invocation{
		ProviderID: "anthropic", ProfileID: stringPointer("legacy-profile"), RequestedModel: "claude-test", Operation: OperationChat, StartedAt: started,
	})
	if err != nil {
		t.Fatalf("Start measured: %v", err)
	}
	input, output, total := int64(10), int64(4), int64(14)
	cached := int64(2)
	if _, err := repository.Finalize(context.Background(), measured.ID, Finalization{
		Status: StatusCompleted, EndedAt: ended, InputTokens: &input, OutputTokens: &output, TotalTokens: &total, CachedInputTokens: &cached,
		UsageQuality: QualityProviderReported, UpstreamRequestCount: 1,
	}); err != nil {
		t.Fatalf("Finalize measured: %v", err)
	}
	missing, err := repository.Start(context.Background(), Invocation{
		ProviderID: "anthropic", ProfileID: stringPointer("legacy-profile"), RequestedModel: "claude-test", Operation: OperationChat, StartedAt: started.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Start missing: %v", err)
	}
	if _, err := repository.Finalize(context.Background(), missing.ID, Finalization{
		Status: StatusFailed, EndedAt: ended.Add(time.Minute), UsageQuality: QualityMissing, UpstreamRequestCount: 1,
	}); err != nil {
		t.Fatalf("Finalize missing: %v", err)
	}

	handler := NewHTTPHandler(NewService(repository))
	request := httptest.NewRequest(http.MethodGet, "/api/usage/tokens/daily?from=2026-07-11&to=2026-07-11&timezone=Europe%2FLondon", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q", got)
	}
	var payload struct {
		SchemaVersion  int             `json:"schema_version"`
		ReportComplete bool            `json:"report_complete"`
		Health         ReportHealth    `json:"health"`
		Retention      RetentionPolicy `json:"retention"`
		Range          struct {
			From, To, TimeZone string
		} `json:"range"`
		Totals  map[string]int64 `json:"totals"`
		Buckets []struct {
			Date                 string    `json:"date"`
			ProviderID           string    `json:"provider_id"`
			ConnectionID         *string   `json:"connection_id"`
			ProfileID            string    `json:"profile_id"`
			Operation            Operation `json:"operation"`
			UpstreamRequestCount int64     `json:"upstream_request_count"`
			PlannedAPISurface    *string   `json:"planned_api_surface"`
			EffectiveAPISurface  *string   `json:"effective_api_surface"`
			Model                string    `json:"model"`
			Input                int64     `json:"input_tokens"`
			ProviderTotal        *int64    `json:"provider_total_tokens"`
			DerivedTotal         *int64    `json:"derived_total_tokens"`
			Total                int64     `json:"total_tokens"`
			Calls                int64     `json:"invocation_count"`
			Measured             int64     `json:"measured_invocation_count"`
			Missing              int64     `json:"missing_usage_count"`
			Failed               int64     `json:"failed_count"`
		} `json:"buckets"`
		TrackingStartedAt *string `json:"tracking_started_at"`
		GeneratedAt       string  `json:"generated_at"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.Range.From != "2026-07-11" || payload.Range.To != "2026-07-11" || payload.Range.TimeZone != "Europe/London" {
		t.Fatalf("range=%#v", payload.Range)
	}
	if payload.SchemaVersion != ReportSchemaVersion || !payload.ReportComplete || payload.Health.Status != HealthHealthy {
		t.Fatalf("schema/completeness/health = %d/%v/%#v", payload.SchemaVersion, payload.ReportComplete, payload.Health)
	}
	if payload.Health.Message != nil || payload.Retention.Mode != "indefinite" || payload.Retention.DeleteWithThread {
		t.Fatalf("health/retention = %#v/%#v", payload.Health, payload.Retention)
	}
	if payload.Totals["input_tokens"] != 10 || payload.Totals["total_tokens"] != 14 || payload.Totals["invocation_count"] != 2 ||
		payload.Totals["measured_invocation_count"] != 1 || payload.Totals["input_measured_invocation_count"] != 1 ||
		payload.Totals["output_measured_invocation_count"] != 1 || payload.Totals["total_measured_invocation_count"] != 1 ||
		payload.Totals["cached_input_measured_invocation_count"] != 1 ||
		payload.Totals["missing_usage_count"] != 1 || payload.Totals["failed_count"] != 1 {
		t.Fatalf("totals=%#v", payload.Totals)
	}
	if len(payload.Buckets) != 1 {
		t.Fatalf("buckets=%d, want 1: %s", len(payload.Buckets), response.Body.String())
	}
	bucket := payload.Buckets[0]
	if bucket.Date != "2026-07-11" || bucket.ProviderID != "anthropic" || bucket.ProfileID != "legacy-profile" || bucket.Model != "claude-test" ||
		bucket.ConnectionID != nil || bucket.Operation != OperationChat || bucket.UpstreamRequestCount != 2 ||
		bucket.PlannedAPISurface != nil || bucket.EffectiveAPISurface != nil || bucket.ProviderTotal == nil || *bucket.ProviderTotal != 14 ||
		bucket.DerivedTotal != nil || bucket.Input != 10 || bucket.Total != 14 || bucket.Calls != 2 || bucket.Measured != 1 || bucket.Missing != 1 || bucket.Failed != 1 {
		t.Fatalf("bucket=%#v", bucket)
	}
	if payload.TrackingStartedAt == nil || payload.GeneratedAt == "" {
		t.Fatalf("tracking/generated=%#v/%q", payload.TrackingStartedAt, payload.GeneratedAt)
	}
}

func TestUsageHealthRemainsReadableWhenPrimaryFails(t *testing.T) {
	primary, err := NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	recorder, err := NewRecorder(t.TempDir(), &faultRepository{Repository: primary, healthErr: errors.New("primary down")})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	handler := NewHTTPHandler(NewService(recorder))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/usage/health", nil))
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status/cache=%d/%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	var health ReportHealth
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if health.Status != HealthDegraded || health.Message == nil {
		t.Fatalf("health=%#v", health)
	}
}

func TestDailyUsageHTTPValidatesDatesAndTimezone(t *testing.T) {
	repository, err := NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	handler := NewHTTPHandler(NewService(repository))
	tests := []string{
		"/api/usage/tokens/daily?from=bad&to=2026-07-11&timezone=UTC",
		"/api/usage/tokens/daily?from=2026-07-12&to=2026-07-11&timezone=UTC",
		"/api/usage/tokens/daily?from=2026-07-11&to=2026-07-11&timezone=Not%2FAZone",
		"/api/usage/tokens/daily?from=2024-01-01&to=2026-07-11&timezone=UTC",
	}
	for _, target := range tests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s status=%d body=%s", target, response.Code, response.Body.String())
		}
	}
}
