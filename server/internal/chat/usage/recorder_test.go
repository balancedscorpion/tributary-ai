package usage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type faultRepository struct {
	Repository
	startErr    error
	snapshotErr error
	finalizeErr error
	healthErr   error
}

func (r *faultRepository) Start(ctx context.Context, inv Invocation) (Invocation, error) {
	if r.startErr != nil {
		return Invocation{}, r.startErr
	}
	return r.Repository.Start(ctx, inv)
}

func (r *faultRepository) Snapshot(ctx context.Context, id string, snapshot UsageSnapshot) (Invocation, error) {
	if r.snapshotErr != nil {
		return Invocation{}, r.snapshotErr
	}
	return r.Repository.Snapshot(ctx, id, snapshot)
}

func (r *faultRepository) Finalize(ctx context.Context, id string, final Finalization) (Invocation, error) {
	if r.finalizeErr != nil {
		return Invocation{}, r.finalizeErr
	}
	return r.Repository.Finalize(ctx, id, final)
}

func (r *faultRepository) Health(ctx context.Context, now time.Time) (ReportHealth, error) {
	if r.healthErr != nil {
		return ReportHealth{}, r.healthErr
	}
	return r.Repository.Health(ctx, now)
}

func newRecorderTestPrimary(t *testing.T) *JSONRepository {
	t.Helper()
	repo, err := NewJSONRepository(filepath.Join(t.TempDir(), "primary"))
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	return repo
}

func recorderTestInvocation(id string) Invocation {
	return Invocation{ID: id, ProviderID: "openai", ConnectionID: optionalString("connection-a"), PlannedAPISurface: optionalString("responses"), RequestedModel: "gpt-test"}
}

func TestRecorderPrimaryStartFailureIsDurableAndReplayable(t *testing.T) {
	cfgDir := t.TempDir()
	primary := newRecorderTestPrimary(t)
	faults := &faultRepository{Repository: primary, startErr: errors.New("primary down")}
	recorder, err := NewRecorder(cfgDir, faults)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	started, err := recorder.Start(context.Background(), recorderTestInvocation("inv-start-replay"))
	if err != nil {
		t.Fatalf("Start should be journal-durable: %v", err)
	}
	health, err := recorder.Health(context.Background(), time.Now().UTC())
	if err != nil || health.Status != HealthDegraded || health.PendingInvocationCount != 1 || health.RunningInvocationCount != 1 {
		t.Fatalf("health=%#v err=%v", health, err)
	}

	faults.startErr = nil
	final := Finalization{Status: StatusCompleted, EndedAt: time.Now().UTC(), Sequence: 1, UsageQuality: QualityMissing, TokenSemanticsProfile: TokenSemanticsRawCountersV1}
	if _, err := recorder.Finalize(context.Background(), started.ID, final); err != nil {
		t.Fatalf("Finalize replay: %v", err)
	}
	stored, err := primary.Get(context.Background(), started.ID)
	if err != nil || stored.Status != StatusCompleted {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
	health, _ = recorder.Health(context.Background(), time.Now().UTC())
	if health.Status != HealthHealthy || health.PendingInvocationCount != 0 || health.TerminalPendingCount != 0 {
		t.Fatalf("journal backlog remained: %#v", health)
	}
}

func TestRecorderFinalizeFailureSurvivesRestartAndDegradesHealth(t *testing.T) {
	cfgDir := t.TempDir()
	primary := newRecorderTestPrimary(t)
	faults := &faultRepository{Repository: primary, finalizeErr: errors.New("primary finalize down")}
	recorder, err := NewRecorder(cfgDir, faults)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	invocation := recorderTestInvocation("inv-final-replay")
	invocation.RunID = optionalString("run-final-replay")
	started, err := recorder.Start(context.Background(), invocation)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	input, output, derived := int64(10), int64(4), int64(14)
	final := Finalization{
		Status: StatusCompleted, EndedAt: time.Now().UTC(), Sequence: 1,
		InputTokens: &input, OutputTokens: &output, DerivedTotalTokens: &derived,
		UsageQuality: QualityDerived, TokenSemanticsProfile: TokenSemanticsRawCountersV1,
	}
	if _, err := recorder.Finalize(context.Background(), started.ID, final); err != nil {
		t.Fatalf("durable Finalize: %v", err)
	}
	health, _ := recorder.Health(context.Background(), time.Now().UTC())
	if health.Status != HealthDegraded || health.PendingInvocationCount != 1 || health.TerminalPendingCount != 1 || health.OldestPendingAt == nil {
		t.Fatalf("terminal backlog health=%#v", health)
	}
	// Reporting reads the durable journal as well as primary storage, so a
	// transient primary outage never makes already-recorded spend disappear.
	visible, err := recorder.List(context.Background(), Query{Status: StatusCompleted, Limit: 10})
	if err != nil || len(visible) != 1 || visible[0].DerivedTotalTokens == nil || *visible[0].DerivedTotalTokens != derived {
		t.Fatalf("journal-backed List=%#v err=%v", visible, err)
	}
	rollup, err := recorder.SumByRun(context.Background(), "run-final-replay")
	if err != nil || rollup.TotalTokens != derived || rollup.InvocationCount != 1 {
		t.Fatalf("journal-backed SumByRun=%#v err=%v", rollup, err)
	}
	// Simulate the last primary retry failing immediately before shutdown. A
	// restart must ignore this old backoff and replay the richer terminal row
	// before any legacy running-row reconciliation can interrupt it.
	if err := recorder.journal.markRetry(started.ID, time.Now().UTC()); err != nil {
		t.Fatalf("markRetry terminal record: %v", err)
	}

	restarted, err := NewRecorder(cfgDir, faults)
	if err != nil {
		t.Fatalf("restart NewRecorder: %v", err)
	}
	restartHealth, _ := restarted.Health(context.Background(), time.Now().UTC())
	if restartHealth.Status != HealthDegraded || restartHealth.TerminalPendingCount != 1 {
		t.Fatalf("restart health=%#v", restartHealth)
	}
	faults.finalizeErr = nil
	pendingRecords, pendingErr := restarted.journal.pending(time.Now().UTC(), maxJournalReplayBatch)
	if pendingErr != nil || len(pendingRecords) != 1 {
		t.Fatalf("pending before replay=%d err=%v", len(pendingRecords), pendingErr)
	}
	if replayed, err := restarted.Reconcile(context.Background()); err != nil || replayed != 1 {
		t.Fatalf("Reconcile=%d err=%v", replayed, err)
	}
	stored, err := primary.Get(context.Background(), started.ID)
	if err != nil || stored.DerivedTotalTokens == nil || *stored.DerivedTotalTokens != derived {
		t.Fatalf("replayed stored=%#v err=%v", stored, err)
	}
	if reconciled, err := NewService(primary).ReconcileRunning(context.Background(), time.Now().UTC()); err != nil || reconciled != 0 {
		t.Fatalf("legacy ReconcileRunning=%d err=%v after terminal replay", reconciled, err)
	}
}

func TestRecorderQuarantinesCorruptRecordAcrossRestart(t *testing.T) {
	cfgDir := t.TempDir()
	primary := newRecorderTestPrimary(t)
	recorder, err := NewRecorder(cfgDir, primary)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	pending := filepath.Join(cfgDir, "chatdb", "usage-journal", "pending", "corrupt.json")
	if err := os.WriteFile(pending, []byte(`{"prompt":"must-not-survive"}`), 0o600); err != nil {
		t.Fatalf("write corrupt record: %v", err)
	}
	if _, err := recorder.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	health, _ := recorder.Health(context.Background(), time.Now().UTC())
	if health.Status != HealthDegraded || health.QuarantinedRecordCount != 1 || health.DroppedInvocationCount < 1 {
		t.Fatalf("quarantine health=%#v", health)
	}
	restarted, err := NewRecorder(cfgDir, primary)
	if err != nil {
		t.Fatalf("restart NewRecorder: %v", err)
	}
	restartHealth, _ := restarted.Health(context.Background(), time.Now().UTC())
	if restartHealth.QuarantinedRecordCount != 1 || restartHealth.Status != HealthDegraded {
		t.Fatalf("restart quarantine health=%#v", restartHealth)
	}
}

func TestRecorderPrimaryHealthFailureIsDegradedWhileJournalWritable(t *testing.T) {
	primary := newRecorderTestPrimary(t)
	recorder, err := NewRecorder(t.TempDir(), &faultRepository{Repository: primary, healthErr: errors.New("primary unavailable")})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	health, err := recorder.Health(context.Background(), time.Now().UTC())
	if err != nil || health.Status != HealthDegraded {
		t.Fatalf("health=%#v err=%v", health, err)
	}
}

func TestRecorderBeginFailsClosedWhenJournalIsUnavailable(t *testing.T) {
	cfgDir := t.TempDir()
	recorder, err := NewRecorder(cfgDir, newRecorderTestPrimary(t))
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	pending := filepath.Join(cfgDir, "chatdb", "usage-journal", "pending")
	if err := os.RemoveAll(pending); err != nil {
		t.Fatalf("remove pending: %v", err)
	}
	if err := os.WriteFile(pending, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("replace pending: %v", err)
	}
	if _, err := recorder.Start(context.Background(), recorderTestInvocation("inv-fail-closed")); !errors.Is(err, ErrAccountingUnavailable) {
		t.Fatalf("Start err=%v, want ErrAccountingUnavailable", err)
	}
}

func TestRecorderRestartInterruptsEveryRunningJournalRecordDespiteRetryDeferral(t *testing.T) {
	cfgDir := t.TempDir()
	primary := newRecorderTestPrimary(t)
	journal, err := NewJournal(cfgDir)
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	const recordCount = maxJournalReplayBatch + 5
	for i := 0; i < recordCount; i++ {
		id := fmt.Sprintf("restart-running-%03d", i)
		invocation := recorderTestInvocation(id)
		if _, err := journal.Begin(context.Background(), invocation); err != nil {
			t.Fatalf("Begin %s: %v", id, err)
		}
		if _, err := primary.Start(context.Background(), invocation); err != nil {
			t.Fatalf("primary Start %s: %v", id, err)
		}
	}
	richInput := int64(42)
	if _, err := journal.Snapshot(context.Background(), fmt.Sprintf("restart-running-%03d", recordCount-1), UsageSnapshot{
		Sequence: 1, ObservedAt: time.Now().UTC(), InputTokens: &richInput,
		UsageQuality: QualityDerived, TokenSemanticsProfile: TokenSemanticsRawCountersV1, UpstreamRequestCount: 1,
	}); err != nil {
		t.Fatalf("rich journal Snapshot: %v", err)
	}
	// Simulate a primary retry immediately before the process exits. Startup
	// recovery must not wait for this backoff deadline.
	if err := journal.markRetry("restart-running-000", time.Now().UTC()); err != nil {
		t.Fatalf("markRetry: %v", err)
	}

	recorder, err := NewRecorder(cfgDir, primary)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	records, err := recorder.journal.pendingRecords(time.Now().UTC(), -1, false)
	if err != nil {
		t.Fatalf("pendingRecords: %v", err)
	}
	if len(records) != recordCount {
		t.Fatalf("recovered records=%d, want %d", len(records), recordCount)
	}
	for _, record := range records {
		if record.Invocation.Status != StatusInterrupted || record.Invocation.ErrorCode == nil || *record.Invocation.ErrorCode != "PROCESS_RESTART" {
			t.Fatalf("record was not interrupted on restart: %#v", record.Invocation)
		}
	}
	if replayed, err := recorder.ReconcileAll(context.Background()); err != nil || replayed != recordCount {
		t.Fatalf("ReconcileAll=%d err=%v, want %d", replayed, err, recordCount)
	}
	lastID := fmt.Sprintf("restart-running-%03d", recordCount-1)
	stored, err := primary.Get(context.Background(), lastID)
	if err != nil || stored.Status != StatusInterrupted || stored.InputTokens == nil || *stored.InputTokens != richInput || stored.UpstreamRequestCount != 1 || !stored.LowerBoundMask.Has(CounterInput) {
		t.Fatalf("rich record was not preserved before legacy reconciliation: %#v err=%v", stored, err)
	}
	if reconciled, err := NewService(primary).ReconcileRunning(context.Background(), time.Now().UTC()); err != nil || reconciled != 0 {
		t.Fatalf("legacy ReconcileRunning=%d err=%v", reconciled, err)
	}
}

func TestRecorderTerminalConflictRequiresExactDurablePayload(t *testing.T) {
	cfgDir := t.TempDir()
	primary := newRecorderTestPrimary(t)
	journal, err := NewJournal(cfgDir)
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	startedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	invocation := recorderTestInvocation("terminal-payload-conflict")
	invocation.ProviderID = "anthropic"
	invocation.PlannedAPISurface = optionalString("messages")
	invocation.StartedAt = startedAt
	if _, err := primary.Start(context.Background(), invocation); err != nil {
		t.Fatalf("primary Start: %v", err)
	}
	if _, err := journal.Begin(context.Background(), invocation); err != nil {
		t.Fatalf("journal Begin: %v", err)
	}
	input, output, derived := int64(10), int64(2), int64(12)
	effective := "messages"
	endedAt := startedAt.Add(time.Second)
	primaryDetails := map[string]int64{"source": 1}
	if _, err := primary.Finalize(context.Background(), invocation.ID, Finalization{
		Sequence: 1, Status: StatusCompleted, EndedAt: endedAt, EffectiveAPISurface: &effective,
		InputTokens: &input, OutputTokens: &output, DerivedTotalTokens: &derived,
		UsageQuality: QualityDerived, TokenSemanticsProfile: TokenSemanticsRawCountersV1,
		UpstreamRequestCount: 1, SanitizedDetails: primaryDetails,
	}); err != nil {
		t.Fatalf("primary Finalize: %v", err)
	}
	desiredDetails := map[string]int64{"source": 2}
	if _, err := journal.Finalize(context.Background(), invocation.ID, Finalization{
		Sequence: 1, Status: StatusCompleted, EndedAt: endedAt, EffectiveAPISurface: &effective,
		InputTokens: &input, OutputTokens: &output, DerivedTotalTokens: &derived,
		UsageQuality: QualityDerived, TokenSemanticsProfile: TokenSemanticsAnthropicV1,
		UpstreamRequestCount: 1, SanitizedDetails: desiredDetails,
	}); err != nil {
		t.Fatalf("journal Finalize: %v", err)
	}

	recorder, err := NewRecorder(cfgDir, primary)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if replayed, err := recorder.Reconcile(context.Background()); replayed != 0 || !errors.Is(err, ErrReconciliationDeferred) {
		t.Fatalf("Reconcile=%d err=%v, want deferred conflict", replayed, err)
	}
	health, err := recorder.Health(context.Background(), time.Now().UTC())
	if err != nil || health.TerminalPendingCount != 1 {
		t.Fatalf("conflicting terminal journal truth was removed: health=%#v err=%v", health, err)
	}
	stored, err := primary.Get(context.Background(), invocation.ID)
	if err != nil || stored.TokenSemanticsProfile != TokenSemanticsRawCountersV1 || stored.SanitizedDetails["source"] != 1 {
		t.Fatalf("primary payload changed unexpectedly: %#v err=%v", stored, err)
	}
}

func TestRecorderTerminalConflictAcceptsDatabaseMillisecondTimePrecision(t *testing.T) {
	cfgDir := t.TempDir()
	primary := newRecorderTestPrimary(t)
	journal, err := NewJournal(cfgDir)
	if err != nil {
		t.Fatalf("NewJournal: %v", err)
	}
	startedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	invocation := recorderTestInvocation("terminal-time-precision")
	invocation.StartedAt = startedAt
	if _, err := primary.Start(context.Background(), invocation); err != nil {
		t.Fatalf("primary Start: %v", err)
	}
	if _, err := journal.Begin(context.Background(), invocation); err != nil {
		t.Fatalf("journal Begin: %v", err)
	}
	input, output, derived := int64(10), int64(2), int64(12)
	effective := "responses"
	endedAt := startedAt.Add(time.Second)
	final := Finalization{
		Sequence: 1, Status: StatusCompleted, EndedAt: endedAt, EffectiveAPISurface: &effective,
		InputTokens: &input, OutputTokens: &output, DerivedTotalTokens: &derived,
		UsageQuality: QualityDerived, TokenSemanticsProfile: TokenSemanticsOpenAIV1, UpstreamRequestCount: 1,
	}
	if _, err := primary.Finalize(context.Background(), invocation.ID, final); err != nil {
		t.Fatalf("primary Finalize: %v", err)
	}
	if _, err := journal.Finalize(context.Background(), invocation.ID, final); err != nil {
		t.Fatalf("journal Finalize: %v", err)
	}
	record, err := readJournalRecord(journal.path(invocation.ID))
	if err != nil {
		t.Fatalf("readJournalRecord: %v", err)
	}
	// Simulate a journal written by a pre-canonicalization build and a MySQL
	// terminal row rounded by DATETIME(3).
	record.Invocation.StartedAt = record.Invocation.StartedAt.Add(500 * time.Microsecond)
	record.Invocation.UpdatedAt = record.Invocation.UpdatedAt.Add(500 * time.Microsecond)
	if record.Invocation.EndedAt != nil {
		ended := record.Invocation.EndedAt.Add(500 * time.Microsecond)
		record.Invocation.EndedAt = &ended
	}
	if err := writeJournalJSON(journal.path(invocation.ID), record); err != nil {
		t.Fatalf("writeJournalJSON: %v", err)
	}

	recorder, err := NewRecorder(cfgDir, &faultRepository{Repository: primary, finalizeErr: ErrConflict})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	if replayed, err := recorder.Reconcile(context.Background()); err != nil || replayed != 1 {
		t.Fatalf("Reconcile=%d err=%v, want millisecond-equivalent replay", replayed, err)
	}
	health, err := recorder.Health(context.Background(), time.Now().UTC())
	if err != nil || health.PendingInvocationCount != 0 {
		t.Fatalf("millisecond-equivalent journal record remained: health=%#v err=%v", health, err)
	}
}
