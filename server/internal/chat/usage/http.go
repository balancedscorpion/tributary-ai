package usage

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

func NewHTTPHandler(service *Service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/usage/tokens/daily", func(w http.ResponseWriter, r *http.Request) { handleDailyUsage(w, r, service) })
	mux.HandleFunc("GET /api/usage/health", func(w http.ResponseWriter, r *http.Request) { handleUsageHealth(w, r, service) })
	return mux
}

func handleUsageHealth(w http.ResponseWriter, r *http.Request, service *Service) {
	if service == nil || service.repository == nil {
		writeUsageJSON(w, http.StatusOK, ReportHealth{Status: HealthUnavailable, Message: messagePointer("usage accounting is unavailable")})
		return
	}
	health, err := service.repository.Health(r.Context(), time.Now().UTC())
	if err != nil {
		health = ReportHealth{Status: HealthUnavailable, Message: messagePointer("usage accounting health is unavailable")}
	}
	writeUsageJSON(w, http.StatusOK, health)
}

func handleDailyUsage(w http.ResponseWriter, r *http.Request, service *Service) {
	if service == nil || service.repository == nil {
		writeUsageJSON(w, http.StatusServiceUnavailable, map[string]any{
			"schema_version": ReportSchemaVersion, "report_complete": false,
			"health":    ReportHealth{Status: HealthUnavailable, Message: messagePointer("usage reporting is unavailable")},
			"retention": IndefiniteRetention,
		})
		return
	}
	timeZone := strings.TrimSpace(r.URL.Query().Get("timezone"))
	if timeZone == "" {
		timeZone = "UTC"
	}
	location, err := loadLocation(timeZone)
	if err != nil {
		writeUsageJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	now := time.Now().In(location)
	defaultTo := dateOnly(now)
	defaultFrom := defaultTo.AddDate(0, 0, -29)
	from, fromText, err := parseUsageDate(r.URL.Query().Get("from"), defaultFrom, location)
	if err != nil {
		writeUsageJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid from date; expected YYYY-MM-DD"})
		return
	}
	through, throughText, err := parseUsageDate(r.URL.Query().Get("to"), defaultTo, location)
	if err != nil {
		writeUsageJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid to date; expected YYYY-MM-DD"})
		return
	}
	if through.Before(from) {
		writeUsageJSON(w, http.StatusBadRequest, map[string]any{"error": "to date must be on or after from date"})
		return
	}
	toExclusive := through.AddDate(0, 0, 1)
	if days := reportCalendarDays(from, toExclusive); days <= 0 || days > maxDailyReportDays {
		writeUsageJSON(w, http.StatusBadRequest, map[string]any{"error": "usage date range must contain between 1 and 366 days"})
		return
	}
	connectionID := firstNonEmpty(r.URL.Query().Get("connection_id"), r.URL.Query().Get("provider_config_id"))
	report, err := service.Daily(r.Context(), DailyQuery{
		From: from.UTC(), To: toExclusive.UTC(), TimeZone: location.String(),
		ProviderID: strings.TrimSpace(r.URL.Query().Get("provider_id")), ConnectionID: connectionID,
		ProfileID: strings.TrimSpace(r.URL.Query().Get("profile_id")), Model: strings.TrimSpace(r.URL.Query().Get("model")),
		Operation:           Operation(strings.TrimSpace(r.URL.Query().Get("operation"))),
		PlannedAPISurface:   strings.TrimSpace(r.URL.Query().Get("planned_api_surface")),
		EffectiveAPISurface: strings.TrimSpace(r.URL.Query().Get("effective_api_surface")),
	})
	if err != nil {
		if report.Health.Status == HealthUnavailable {
			writeUsageJSON(w, http.StatusServiceUnavailable, map[string]any{"schema_version": ReportSchemaVersion, "report_complete": false, "health": report.Health, "retention": IndefiniteRetention})
			return
		}
		writeUsageJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to load token usage"})
		return
	}
	buckets := make([]map[string]any, 0, len(report.Days))
	for _, day := range report.Days {
		bucket := totalsDTO(day.Totals)
		bucket["date"] = day.Date
		bucket["provider_id"] = day.ProviderID
		bucket["connection_id"] = nullableUsageString(day.ConnectionID)
		bucket["profile_id"] = nullableUsageString(day.ProfileID)
		bucket["plan_id"] = nullableUsageString(day.PlanID)
		bucket["config_fingerprint"] = nullableUsageString(day.ConfigFingerprint)
		bucket["operation"] = day.Operation
		bucket["upstream_request_count"] = day.UpstreamRequestCount
		bucket["planned_api_surface"] = nullableUsageString(day.PlannedAPISurface)
		bucket["effective_api_surface"] = nullableUsageString(day.EffectiveAPISurface)
		bucket["model"] = day.Model
		buckets = append(buckets, bucket)
	}
	response := map[string]any{
		"schema_version": report.SchemaVersion, "report_complete": report.ReportComplete, "health": report.Health, "retention": report.Retention,
		"range":  map[string]any{"from": fromText, "to": throughText, "timezone": location.String()},
		"totals": totalsDTO(report.Totals), "buckets": buckets, "generated_at": report.GeneratedAt.Format(time.RFC3339Nano),
		"tracking_started_at": nil,
	}
	if report.TrackingStartedAt != nil {
		response["tracking_started_at"] = report.TrackingStartedAt.UTC().Format(time.RFC3339Nano)
	}
	writeUsageJSON(w, http.StatusOK, response)
}

func totalsDTO(totals TokenTotals) map[string]any {
	return map[string]any{
		"input_tokens":                      nullableTotal(totals.InputTokens, totals.CallsWithInputTokens),
		"output_tokens":                     nullableTotal(totals.OutputTokens, totals.CallsWithOutputTokens),
		"provider_total_tokens":             nullableTotal(totals.ProviderTotalTokens, totals.CallsWithProviderTotalTokens),
		"derived_total_tokens":              nullableTotal(totals.DerivedTotalTokens, totals.CallsWithDerivedTotalTokens),
		"total_tokens":                      nullableTotal(totals.TotalTokens, totals.CallsWithTotalTokens),
		"cached_input_tokens":               nullableTotal(totals.CachedInputTokens, totals.CallsWithCachedInput),
		"cache_write_tokens":                nullableTotal(totals.CacheWriteTokens, totals.CallsWithCacheWrite),
		"reasoning_tokens":                  nullableTotal(totals.ReasoningTokens, totals.CallsWithReasoning),
		"lower_bound_input_tokens":          nullableTotal(totals.LowerBoundInputTokens, totals.CallsWithLowerBoundInput),
		"lower_bound_output_tokens":         nullableTotal(totals.LowerBoundOutputTokens, totals.CallsWithLowerBoundOutput),
		"lower_bound_provider_total_tokens": nullableTotal(totals.LowerBoundProviderTotalTokens, totals.CallsWithLowerBoundProviderTotal),
		"lower_bound_derived_total_tokens":  nullableTotal(totals.LowerBoundDerivedTotalTokens, totals.CallsWithLowerBoundDerivedTotal),
		"lower_bound_total_tokens":          nullableTotal(totals.LowerBoundTotalTokens, totals.CallsWithLowerBoundTotal),
		"lower_bound_cached_input_tokens":   nullableTotal(totals.LowerBoundCachedInputTokens, totals.CallsWithLowerBoundCachedInput),
		"lower_bound_cache_write_tokens":    nullableTotal(totals.LowerBoundCacheWriteTokens, totals.CallsWithLowerBoundCacheWrite),
		"lower_bound_reasoning_tokens":      nullableTotal(totals.LowerBoundReasoningTokens, totals.CallsWithLowerBoundReasoning),
		"invocation_count":                  totals.InvocationCount,
		"upstream_request_count":            totals.UpstreamRequestCount,
		"measured_invocation_count":         totals.CallsWithUsage,
		"input_measured_invocation_count":   totals.CallsWithInputTokens, "output_measured_invocation_count": totals.CallsWithOutputTokens,
		"provider_total_measured_invocation_count":    totals.CallsWithProviderTotalTokens,
		"derived_total_measured_invocation_count":     totals.CallsWithDerivedTotalTokens,
		"total_measured_invocation_count":             totals.CallsWithTotalTokens,
		"cached_input_measured_invocation_count":      totals.CallsWithCachedInput,
		"cache_write_measured_invocation_count":       totals.CallsWithCacheWrite,
		"reasoning_measured_invocation_count":         totals.CallsWithReasoning,
		"lower_bound_invocation_count":                totals.LowerBoundCalls,
		"lower_bound_input_invocation_count":          totals.CallsWithLowerBoundInput,
		"lower_bound_output_invocation_count":         totals.CallsWithLowerBoundOutput,
		"lower_bound_provider_total_invocation_count": totals.CallsWithLowerBoundProviderTotal,
		"lower_bound_derived_total_invocation_count":  totals.CallsWithLowerBoundDerivedTotal,
		"lower_bound_total_invocation_count":          totals.CallsWithLowerBoundTotal,
		"lower_bound_cached_input_invocation_count":   totals.CallsWithLowerBoundCachedInput,
		"lower_bound_cache_write_invocation_count":    totals.CallsWithLowerBoundCacheWrite,
		"lower_bound_reasoning_invocation_count":      totals.CallsWithLowerBoundReasoning,
		"missing_usage_count":                         totals.MissingUsageCalls,
		"provider_reported_count":                     totals.ProviderReportedCalls, "derived_usage_count": totals.DerivedCalls,
		"legacy_partial_count": totals.LegacyPartialCalls, "failed_count": totals.FailedCalls,
		"cancelled_count": totals.CancelledCalls, "interrupted_count": totals.InterruptedCalls,
	}
}

func nullableTotal(value, coverage int64) any {
	if coverage == 0 {
		return nil
	}
	return value
}
func parseUsageDate(raw string, fallback time.Time, location *time.Location) (time.Time, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		date := dateOnly(fallback.In(location))
		return date, date.Format("2006-01-02"), nil
	}
	parsed, err := time.ParseInLocation("2006-01-02", raw, location)
	if err != nil {
		return time.Time{}, "", err
	}
	return parsed, raw, nil
}

func dateOnly(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, value.Location())
}
func nullableUsageString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}
func writeUsageJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
