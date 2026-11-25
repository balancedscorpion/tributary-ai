package usage

import (
	"context"
	"testing"
	"time"
)

func TestServiceDailyGroupsByLocalEndedDateAndModel(t *testing.T) {
	repo, err := NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	// Europe/London is UTC+1 in July. These first two calls end on the same
	// local date even though their UTC dates differ.
	createFinalizedJSONInvocation(t, repo, Invocation{
		ID: "daily-1", ProviderID: "openai", ProviderConfigID: stringPointer("profile-a"), APIType: stringPointer("responses"),
		RequestedModel: "gpt-5", StartedAt: time.Date(2026, 7, 10, 22, 58, 0, 0, time.UTC),
	}, Finalization{Status: StatusCompleted, EndedAt: time.Date(2026, 7, 10, 23, 2, 0, 0, time.UTC), InputTokens: int64Pointer(100), OutputTokens: int64Pointer(20), TotalTokens: int64Pointer(120), UsageQuality: QualityProviderReported})
	createFinalizedJSONInvocation(t, repo, Invocation{
		ID: "daily-2", ProviderID: "openai", ProviderConfigID: stringPointer("profile-a"), APIType: stringPointer("responses"),
		RequestedModel: "gpt-5", StartedAt: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
	}, Finalization{Status: StatusFailed, EndedAt: time.Date(2026, 7, 11, 0, 5, 0, 0, time.UTC), UsageQuality: QualityMissing})
	resolved := "claude-sonnet-4-20260601"
	createFinalizedJSONInvocation(t, repo, Invocation{
		ID: "daily-3", ProviderID: "anthropic", RequestedModel: "claude-sonnet", StartedAt: time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC),
	}, Finalization{Status: StatusCompleted, EndedAt: time.Date(2026, 7, 11, 10, 1, 0, 0, time.UTC), ResolvedModel: &resolved, TotalTokens: int64Pointer(75), UsageQuality: QualityDerived})

	service := NewService(repo)
	generatedAt := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return generatedAt }
	report, err := service.Daily(context.Background(), DailyQuery{
		From:     time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC),
		To:       time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC),
		TimeZone: "Europe/London",
	})
	if err != nil {
		t.Fatalf("Daily: %v", err)
	}
	if report.TimeZone != "Europe/London" || !report.GeneratedAt.Equal(generatedAt) {
		t.Fatalf("report metadata = %#v", report)
	}
	if report.SchemaVersion != ReportSchemaVersion || !report.ReportComplete || report.Health.Status != HealthHealthy || report.Retention != IndefiniteRetention {
		t.Fatalf("report contract = %#v", report)
	}
	if len(report.Days) != 2 {
		t.Fatalf("len(report.Days) = %d, want 2: %#v", len(report.Days), report.Days)
	}
	if report.Days[0].Date != "2026-07-11" || report.Days[0].ProviderID != "anthropic" || report.Days[0].Model != resolved {
		t.Fatalf("first sorted bucket = %#v", report.Days[0])
	}
	if report.Days[1].Date != "2026-07-11" || report.Days[1].ProviderID != "openai" || report.Days[1].Totals.InvocationCount != 2 {
		t.Fatalf("openai bucket = %#v", report.Days[1])
	}
	if report.Days[1].ConnectionID != "profile-a" || report.Days[1].Operation != OperationUnknown || report.Days[1].PlannedAPISurface != "responses" {
		t.Fatalf("openai bucket identity = %#v", report.Days[1])
	}
	if report.Totals.InvocationCount != 3 || report.Totals.TotalTokens != 195 || report.Totals.MissingUsageCalls != 1 || report.Totals.FailedCalls != 1 {
		t.Fatalf("report totals = %#v", report.Totals)
	}
	if report.TrackingStartedAt == nil || !report.TrackingStartedAt.Equal(time.Date(2026, 7, 10, 22, 58, 0, 0, time.UTC)) {
		t.Fatalf("TrackingStartedAt = %v", report.TrackingStartedAt)
	}
}

func TestServiceDailyMarksStaleLedgerIncomplete(t *testing.T) {
	repo, err := NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	started := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return started }
	if _, err := repo.Start(context.Background(), Invocation{ID: "stale-running", ProviderID: "openai", RequestedModel: "gpt-5"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	service := NewService(repo)
	service.now = func() time.Time { return started.Add(StaleInvocationAge + time.Minute) }
	report, err := service.Daily(context.Background(), DailyQuery{From: started.Add(-time.Hour), To: started.Add(time.Hour), TimeZone: "UTC"})
	if err != nil {
		t.Fatalf("Daily: %v", err)
	}
	if report.ReportComplete || report.Health.Status != HealthDegraded || report.Health.StaleInvocationCount != 1 || report.Health.RunningInvocationCount != 1 {
		t.Fatalf("stale health = %#v", report)
	}
}

func TestServiceDailyRejectsInvalidTimeZone(t *testing.T) {
	repo, err := NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	_, err = NewService(repo).Daily(context.Background(), DailyQuery{
		From:     time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
		To:       time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC),
		TimeZone: "Not/A-Timezone",
	})
	if err == nil {
		t.Fatal("Daily accepted an invalid timezone")
	}
}

func TestServiceReconcileRunningMarksRestartInterruptions(t *testing.T) {
	repo, err := NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	startedAt := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	created, err := repo.Start(context.Background(), Invocation{
		ID:             "restart-interrupted",
		ProviderID:     "openai",
		RequestedModel: "gpt-5",
		StartedAt:      startedAt,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	createFinalizedJSONInvocation(t, repo, Invocation{
		ID:             "already-completed",
		ProviderID:     "anthropic",
		RequestedModel: "claude-sonnet",
		StartedAt:      startedAt,
	}, Finalization{
		Status:       StatusCompleted,
		EndedAt:      startedAt.Add(time.Minute),
		UsageQuality: QualityMissing,
	})

	interruptedAt := startedAt.Add(5 * time.Minute)
	count, err := NewService(repo).ReconcileRunning(context.Background(), interruptedAt)
	if err != nil {
		t.Fatalf("ReconcileRunning: %v", err)
	}
	if count != 1 {
		t.Fatalf("reconciled count = %d, want 1", count)
	}
	got, err := repo.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusInterrupted || got.EndedAt == nil || !got.EndedAt.Equal(interruptedAt) {
		t.Fatalf("reconciled invocation = %#v", got)
	}
	if got.ErrorCode == nil || *got.ErrorCode != "PROCESS_RESTART" || got.UsageQuality != QualityMissing {
		t.Fatalf("reconciled metadata = %#v", got)
	}

	count, err = NewService(repo).ReconcileRunning(context.Background(), interruptedAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("second ReconcileRunning: %v", err)
	}
	if count != 0 {
		t.Fatalf("second reconciled count = %d, want 0", count)
	}
}
