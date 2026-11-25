package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/jsondb"
)

func moveJSONRunToRunning(t *testing.T, store *JSONStore, run Run) Run {
	t.Helper()
	running, changed, err := store.TransitionRunCAS(context.Background(), RunStateMutation{
		RunID: run.ID, ExpectedRevision: run.Revision, ExpectedStatuses: []string{RunStatusQueued}, Status: RunStatusRunning,
	})
	if err != nil || !changed {
		t.Fatalf("move run to running: changed=%v run=%#v err=%v", changed, running, err)
	}
	return running
}

func testSuspendMutation(run Run, thread Thread, id string, wakeAt time.Time) SuspendRunMutation {
	return SuspendRunMutation{
		Checkpoint: ContinuationCheckpoint{
			ID: id, RunID: run.ID, ThreadID: thread.ID, ToolBatchSequence: 3,
			HistoryJSON:           json.RawMessage(`[{"role":"assistant","content":"query still running"}]`),
			ProviderStateJSON:     json.RawMessage(`{"provider":"anthropic","cursor":"turn-3"}`),
			ExecutionSnapshotJSON: json.RawMessage(`{"system_prompt":"system","dispatch_ordinal":3,"tools":["query_status"]}`),
			DependencyIDs:         []string{"job-old"}, CreatedAt: wakeAt.Add(-time.Minute),
		},
		ExpectedRevision: run.Revision, ExpectedStatuses: []string{run.Status},
		ContinuationCount: run.ContinuationCount, MaxAutoContinuations: 3, WakeAt: wakeAt,
	}
}

func TestJSONSuspendRunIsAtomicIdempotentAndSnapshotRoundTrips(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, thread, run := seedLongHorizonJSONRun(t, t.TempDir())
	run = moveJSONRunToRunning(t, store, run)
	wakeAt := time.Now().UTC().Add(-time.Second)
	mutation := testSuspendMutation(run, thread, "checkpoint-fixed-id", wakeAt)

	checkpoint, waiting, changed, err := store.SuspendRun(ctx, mutation)
	if err != nil || !changed {
		t.Fatalf("SuspendRun changed=%v checkpoint=%#v run=%#v err=%v", changed, checkpoint, waiting, err)
	}
	if waiting.Status != RunStatusWaitingExternal || waiting.Revision != run.Revision+1 ||
		waiting.LastCheckpointSequence != checkpoint.Sequence || waiting.ResumeLeaseHolder != "" {
		t.Fatalf("suspended run=%#v checkpoint=%#v", waiting, checkpoint)
	}
	if !jsonSemanticallyEqual(checkpoint.ExecutionSnapshotJSON, mutation.Checkpoint.ExecutionSnapshotJSON) {
		t.Fatalf("execution snapshot=%s want=%s", checkpoint.ExecutionSnapshotJSON, mutation.Checkpoint.ExecutionSnapshotJSON)
	}

	replayed, replayRun, changed, err := store.SuspendRun(ctx, mutation)
	if err != nil || changed || replayed.ID != checkpoint.ID || replayed.Sequence != checkpoint.Sequence || replayRun.Revision != waiting.Revision {
		t.Fatalf("idempotent SuspendRun changed=%v checkpoint=%#v run=%#v err=%v", changed, replayed, replayRun, err)
	}
	conflicting := mutation
	conflicting.Checkpoint.HistoryJSON = json.RawMessage(`[{"role":"assistant","content":"different"}]`)
	if _, _, _, err := store.SuspendRun(ctx, conflicting); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting fixed checkpoint replay err=%v, want ErrConflict", err)
	}
	latest, err := store.LatestContinuationCheckpoint(ctx, run.ID)
	if err != nil || latest == nil || !jsonSemanticallyEqual(latest.ExecutionSnapshotJSON, mutation.Checkpoint.ExecutionSnapshotJSON) {
		t.Fatalf("latest checkpoint=%#v err=%v", latest, err)
	}
}

func TestJSONInitialExecutionLeaseIsRecoverableWithoutIncrementingContinuations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, _, run := seedLongHorizonJSONRun(t, t.TempDir())
	startedAt := time.Now().UTC().Add(-2 * time.Minute)
	expiresAt := startedAt.Add(time.Minute)

	running, claimed, err := repository.ClaimRunWake(ctx, run.ID, run.Revision, "initial-worker", startedAt, expiresAt)
	if err != nil || !claimed || running.Status != RunStatusRunning || running.ContinuationCount != 0 ||
		running.ResumeLeaseHolder != "initial-worker" {
		t.Fatalf("initial execution claim=%v run=%#v err=%v", claimed, running, err)
	}
	stillLeased, err := repository.ListRecoverableRuns(ctx, startedAt.Add(30*time.Second), 10)
	if err != nil || len(stillLeased) != 0 {
		t.Fatalf("live initial execution was recoverable=%#v err=%v", stillLeased, err)
	}
	recoverable, err := repository.ListRecoverableRuns(ctx, time.Now().UTC(), 10)
	if err != nil || len(recoverable) != 1 || recoverable[0].ID != run.ID || recoverable[0].Status != RunStatusRunning {
		t.Fatalf("expired initial execution recoverable=%#v err=%v", recoverable, err)
	}

	reclaimedAt := time.Now().UTC()
	reclaimed, claimed, err := repository.ClaimRunWake(
		ctx, run.ID, running.Revision, "recovery-worker", reclaimedAt, reclaimedAt.Add(time.Minute),
	)
	if err != nil || !claimed || reclaimed.Status != RunStatusRunning || reclaimed.ContinuationCount != 0 ||
		reclaimed.ResumeLeaseHolder != "recovery-worker" {
		t.Fatalf("expired initial reclaim=%v run=%#v err=%v", claimed, reclaimed, err)
	}
}

func TestJSONQueuedRunUsesRecoveryGraceBeforeInitialClaim(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, _, run := seedLongHorizonJSONRun(t, t.TempDir())
	fresh, err := repository.ListRecoverableRuns(ctx, run.UpdatedAt.Add(queuedRunRecoveryAge-time.Second), 10)
	if err != nil || len(fresh) != 0 {
		t.Fatalf("fresh queued run recoverable=%#v err=%v", fresh, err)
	}
	staleAt := run.UpdatedAt.Add(queuedRunRecoveryAge + time.Second)
	stale, err := repository.ListRecoverableRuns(ctx, staleAt, 10)
	if err != nil || len(stale) != 1 || stale[0].ID != run.ID || stale[0].Status != RunStatusQueued {
		t.Fatalf("stale queued run recoverable=%#v err=%v", stale, err)
	}
	claimed, changed, err := repository.ClaimRunWake(ctx, run.ID, run.Revision, "queued-recovery", staleAt, staleAt.Add(time.Minute))
	if err != nil || !changed || claimed.Status != RunStatusRunning || claimed.ResumeLeaseHolder != "queued-recovery" {
		t.Fatalf("stale queued claim changed=%v run=%#v err=%v", changed, claimed, err)
	}
}

func TestJSONLegacyRunningExecutionUsesConservativeStalenessFence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, _, run := seedLongHorizonJSONRun(t, t.TempDir())
	run = moveJSONRunToRunning(t, repository, run)

	tooEarly := run.UpdatedAt.Add(legacyRunningRecoveryAge - time.Second)
	recoverable, err := repository.ListRecoverableRuns(ctx, tooEarly, 10)
	if err != nil || len(recoverable) != 0 {
		t.Fatalf("recent lease-less legacy run was recoverable=%#v err=%v", recoverable, err)
	}
	staleAt := run.UpdatedAt.Add(legacyRunningRecoveryAge + time.Second)
	recoverable, err = repository.ListRecoverableRuns(ctx, staleAt, 10)
	if err != nil || len(recoverable) != 1 || recoverable[0].ID != run.ID {
		t.Fatalf("stale lease-less legacy run recoverable=%#v err=%v", recoverable, err)
	}
	reclaimed, claimed, err := repository.ClaimRunWake(
		ctx, run.ID, run.Revision, "legacy-recovery", staleAt, staleAt.Add(time.Minute),
	)
	if err != nil || !claimed || reclaimed.Status != RunStatusRunning || reclaimed.ContinuationCount != 0 ||
		reclaimed.ResumeLeaseHolder != "legacy-recovery" {
		t.Fatalf("legacy reclaim=%v run=%#v err=%v", claimed, reclaimed, err)
	}
}

func TestSuspendRunRequiresCallerStableCheckpointID(t *testing.T) {
	mutation := SuspendRunMutation{
		Checkpoint: ContinuationCheckpoint{
			RunID: "run-1", ThreadID: "thread-1", HistoryJSON: json.RawMessage(`[]`),
		},
		ExpectedRevision: 1, ExpectedStatuses: []string{RunStatusRunning},
		MaxAutoContinuations: 8, WakeAt: time.Now().UTC().Add(time.Minute),
	}
	if _, err := normalizeSuspendRunMutation(mutation); err == nil || !strings.Contains(err.Error(), "stable checkpoint id") {
		t.Fatalf("normalizeSuspendRunMutation err=%v, want stable checkpoint id error", err)
	}
}

func TestJSONPreparedSuspendJournalRollsForwardAcrossStoreInstances(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	store, thread, run := seedLongHorizonJSONRun(t, dir)
	run = moveJSONRunToRunning(t, store, run)
	wakeAt := time.Now().UTC().Add(-time.Second)
	mutation, err := normalizeSuspendRunMutation(testSuspendMutation(run, thread, "checkpoint-prepared-crash", wakeAt))
	if err != nil {
		t.Fatalf("normalize suspend: %v", err)
	}
	before, _, err := store.readRunWithPath(run.ID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	checkpoint := mutation.Checkpoint
	checkpoint.Sequence = 1
	after := before
	after.Status = RunStatusWaitingExternal
	after.Revision++
	after.ContinuationCount = mutation.ContinuationCount
	after.MaxAutoContinuations = mutation.MaxAutoContinuations
	after.LastCheckpointSequence = checkpoint.Sequence
	after.WakeAt = &wakeAt
	after.ResumeLeaseHolder = ""
	after.ResumeLeaseExpiresAt = nil
	after.UpdatedAt = time.Now().UTC()
	transaction := jsonRunSuspendTransactionRecord{
		Kind: runSuspendTransactionRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ID: "run_suspend_prepared_crash", BeforeRun: before, AfterRun: after,
		Checkpoint: jsonContinuationCheckpointRecord{
			Kind: continuationCheckpointRecordKind, SchemaVersion: longHorizonSchemaVersion,
			ContinuationCheckpoint: checkpoint,
		},
		PreparedAt: time.Now().UTC(),
	}
	journalPath, err := queryTransactionPath(dir, transaction.ID)
	if err != nil {
		t.Fatalf("journal path: %v", err)
	}
	if err := jsondb.WriteJSONFile(journalPath, transaction); err != nil {
		t.Fatalf("write prepared journal: %v", err)
	}
	checkpointPath, err := continuationCheckpointPath(dir, run.ID, checkpoint.Sequence)
	if err != nil {
		t.Fatalf("checkpoint path: %v", err)
	}
	if err := jsondb.WriteJSONFile(checkpointPath, transaction.Checkpoint); err != nil {
		t.Fatalf("simulate checkpoint-first crash: %v", err)
	}

	reopened, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen with prepared suspend journal: %v", err)
	}
	recoverable, err := reopened.ListRecoverableRuns(ctx, time.Now().UTC(), 10)
	if err != nil || len(recoverable) != 1 || recoverable[0].ID != run.ID || recoverable[0].Status != RunStatusWaitingExternal {
		t.Fatalf("recoverable after journal replay=%#v err=%v", recoverable, err)
	}
	got, err := reopened.GetRun(ctx, run.ID)
	if err != nil || got.LastCheckpointSequence != checkpoint.Sequence || got.Revision != after.Revision {
		t.Fatalf("recovered run=%#v err=%v", got, err)
	}
	latest, err := reopened.LatestContinuationCheckpoint(ctx, run.ID)
	if err != nil || latest == nil || !jsonSemanticallyEqual(latest.ExecutionSnapshotJSON, checkpoint.ExecutionSnapshotJSON) {
		t.Fatalf("recovered checkpoint=%#v err=%v", latest, err)
	}
	if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("prepared journal still exists: %v", err)
	}
}

func TestJSONLiveLeasedIdentityDefersCheckpointReconciliationWithSuspendJournal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	store, thread, run := seedLongHorizonJSONRun(t, dir)
	run = moveJSONRunToRunning(t, store, run)
	now := nowUTC()
	oldJobID := "00000-00000"
	corrupt := managedSentinelTestJob(oldJobID, "")
	corrupt.Owner.ThreadID = thread.ID
	corrupt.Owner.RunID = run.ID
	corrupt.SubmittedAt = now
	corrupt.LastAccess = now
	jobPath, err := jsondb.QueryJobPath(dir, oldJobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsondb.WriteJSONFile(jobPath, jsonQueryJobRecord{
		Kind: queryJobRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ConnectionKey: queryConnectionKey(corrupt), Job: corrupt,
		MonitorLease: &jsonMonitorLease{Holder: "live-monitor", ExpiresAt: now.Add(time.Hour)}, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	wakeAt := now.Add(time.Minute)
	mutation, err := normalizeSuspendRunMutation(testSuspendMutation(run, thread, "checkpoint-live-identity", wakeAt))
	if err != nil {
		t.Fatalf("normalize suspend: %v", err)
	}
	before, _, err := store.readRunWithPath(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := mutation.Checkpoint
	checkpoint.Sequence = 1
	checkpoint.DependencyIDs = []string{oldJobID}
	after := before
	after.Status = RunStatusWaitingExternal
	after.Revision++
	after.ContinuationCount = mutation.ContinuationCount
	after.MaxAutoContinuations = mutation.MaxAutoContinuations
	after.LastCheckpointSequence = checkpoint.Sequence
	after.WakeAt = &wakeAt
	after.ResumeLeaseHolder = ""
	after.ResumeLeaseExpiresAt = nil
	after.UpdatedAt = now
	transaction := jsonRunSuspendTransactionRecord{
		Kind: runSuspendTransactionRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ID: "run_suspend_live_identity", BeforeRun: before, AfterRun: after,
		Checkpoint: jsonContinuationCheckpointRecord{
			Kind: continuationCheckpointRecordKind, SchemaVersion: longHorizonSchemaVersion,
			ContinuationCheckpoint: checkpoint,
		},
		PreparedAt: now,
	}
	journalPath, err := queryTransactionPath(dir, transaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsondb.WriteJSONFile(journalPath, transaction); err != nil {
		t.Fatal(err)
	}
	checkpointPath, err := continuationCheckpointPath(dir, run.ID, checkpoint.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsondb.WriteJSONFile(checkpointPath, transaction.Checkpoint); err != nil {
		t.Fatal(err)
	}

	deferred, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen while identity owner lease is live: %v", err)
	}
	deferredRun, err := deferred.GetRun(ctx, run.ID)
	if err != nil || deferredRun.Status != RunStatusRunning || deferredRun.Revision != before.Revision ||
		deferredRun.LastCheckpointSequence != before.LastCheckpointSequence {
		t.Fatalf("deferred run=%#v err=%v, want untouched before-image", deferredRun, err)
	}
	if _, err := os.Stat(journalPath); err != nil {
		t.Fatalf("deferred suspend journal missing: %v", err)
	}

	var expired jsonQueryJobRecord
	if err := readJSONLoose(jobPath, &expired); err != nil {
		t.Fatal(err)
	}
	expired.MonitorLease.ExpiresAt = now.Add(-time.Minute)
	if err := jsondb.WriteJSONFile(jobPath, expired); err != nil {
		t.Fatal(err)
	}
	// Model the exact startup clock-boundary: the first recovery pass deferred
	// while the lease was live, then it expired before checkpoint reconciliation.
	// Reconciliation must recover again under its own lock before touching the
	// run pointer.
	if err := deferred.reconcileContinuationCheckpoints(); err != nil {
		t.Fatalf("reconcile after identity owner lease expiry: %v", err)
	}
	recovered := deferred
	recoveredRun, err := recovered.GetRun(ctx, run.ID)
	if err != nil || recoveredRun.Status != RunStatusWaitingExternal || recoveredRun.Revision != after.Revision ||
		recoveredRun.LastCheckpointSequence != checkpoint.Sequence {
		t.Fatalf("recovered run=%#v err=%v", recoveredRun, err)
	}
	latest, err := recovered.LatestContinuationCheckpoint(ctx, run.ID)
	repairedJobID := repairedManagedQueryJobID(corrupt)
	if err != nil || latest == nil || len(latest.DependencyIDs) != 1 || latest.DependencyIDs[0] != repairedJobID {
		t.Fatalf("recovered checkpoint=%#v err=%v, want dependency %s", latest, err, repairedJobID)
	}
	if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered suspend journal still exists: %v", err)
	}
}

func TestJSONSuspendAndLeaseFencingAreCrossProcessSafe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	first, thread, run := seedLongHorizonJSONRun(t, dir)
	run = moveJSONRunToRunning(t, first, run)
	second, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("NewJSON second: %v", err)
	}
	wakeAt := time.Now().UTC().Add(-time.Second)
	mutations := []SuspendRunMutation{
		testSuspendMutation(run, thread, "checkpoint-racer-a", wakeAt),
		testSuspendMutation(run, thread, "checkpoint-racer-b", wakeAt),
	}
	stores := []*JSONStore{first, second}
	start := make(chan struct{})
	var wg sync.WaitGroup
	var changed [2]bool
	var errs [2]error
	for i := range stores {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, changed[i], errs[i] = stores[i].SuspendRun(ctx, mutations[i])
		}(i)
	}
	close(start)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || changed[0] == changed[1] {
		t.Fatalf("cross-store suspend changed=%v errors=%v", changed, errs)
	}
	waiting, err := first.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	now := time.Now().UTC()
	resuming, claimed, err := second.ClaimRunWake(ctx, run.ID, waiting.Revision, "claim-token", now, now.Add(time.Minute))
	if err != nil || !claimed {
		t.Fatalf("ClaimRunWake claimed=%v run=%#v err=%v", claimed, resuming, err)
	}
	beforeRevision := resuming.Revision
	beforeExpiry := *resuming.ResumeLeaseExpiresAt
	if renewed, err := first.RenewRunWakeLease(ctx, run.ID, "wrong-token", time.Minute); err != nil || renewed {
		t.Fatalf("wrong owner renewed=%v err=%v", renewed, err)
	}
	if renewed, err := first.RenewRunWakeLease(ctx, run.ID, "claim-token", 2*time.Minute); err != nil || !renewed {
		t.Fatalf("owner renewal renewed=%v err=%v", renewed, err)
	}
	afterRenewal, err := second.GetRun(ctx, run.ID)
	if err != nil || afterRenewal.Revision != beforeRevision || afterRenewal.ResumeLeaseExpiresAt == nil ||
		!afterRenewal.ResumeLeaseExpiresAt.After(beforeExpiry) {
		t.Fatalf("renewed run=%#v err=%v", afterRenewal, err)
	}
	if _, changed, err := first.TransitionRunCAS(ctx, RunStateMutation{
		RunID: run.ID, ExpectedRevision: beforeRevision, ExpectedStatuses: []string{RunStatusResuming},
		ExpectedResumeLeaseHolder: "stale-token", Status: RunStatusWaitingUser, ClearResumeLease: true,
	}); err != nil || changed {
		t.Fatalf("stale fenced transition changed=%v err=%v", changed, err)
	}

	record, path, err := first.readRunWithPath(run.ID)
	if err != nil {
		t.Fatalf("read run for expiry: %v", err)
	}
	expiredAt := time.Now().UTC().Add(-time.Minute)
	record.ResumeLeaseExpiresAt = &expiredAt
	if err := jsondb.WriteJSONFile(path, record); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	if renewed, err := second.RenewRunWakeLease(ctx, run.ID, "claim-token", time.Minute); err != nil || renewed {
		t.Fatalf("expired lease renewed=%v err=%v", renewed, err)
	}
	expiredMutation := testSuspendMutation(afterRenewal, thread, "checkpoint-expired-worker", time.Now().UTC().Add(time.Second))
	expiredMutation.ExpectedResumeLeaseHolder = "claim-token"
	if _, _, changed, err := second.SuspendRun(ctx, expiredMutation); err != nil || changed {
		t.Fatalf("expired worker suspend changed=%v err=%v", changed, err)
	}
	requeueAt := time.Now().UTC().Add(time.Second)
	if err := second.ReleaseRunWake(ctx, run.ID, "claim-token", RunStatusResumePending, &requeueAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired former holder release err=%v, want ErrConflict", err)
	}
	expiredRun, err := first.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun expired: %v", err)
	}
	reclaimNow := time.Now().UTC()
	reclaimed, ok, err := first.ClaimRunWake(ctx, run.ID, expiredRun.Revision, "new-claim-token", reclaimNow, reclaimNow.Add(time.Minute))
	if err != nil || !ok || reclaimed.ResumeLeaseHolder != "new-claim-token" ||
		reclaimed.ContinuationCount != expiredRun.ContinuationCount {
		t.Fatalf("reclaim after expiry ok=%v run=%#v err=%v", ok, reclaimed, err)
	}
	if err := second.ReleaseRunWake(ctx, run.ID, "claim-token", RunStatusResumePending, &requeueAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("former holder revoked newer claim err=%v, want ErrConflict", err)
	}
}

func TestJSONClaimRunWakeLegacyContinuationLimitFallsBackToEight(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, _, run := seedLongHorizonJSONRun(t, t.TempDir())
	run = moveJSONRunToRunning(t, store, run)
	zero, three := 0, 3
	wakeAt := time.Now().UTC().Add(-time.Second)
	waiting, changed, err := store.TransitionRunCAS(ctx, RunStateMutation{
		RunID: run.ID, ExpectedRevision: run.Revision, ExpectedStatuses: []string{RunStatusRunning},
		Status: RunStatusWaitingExternal, ContinuationCount: &three, MaxAutoContinuations: &zero, WakeAt: &wakeAt,
	})
	if err != nil || !changed {
		t.Fatalf("prepare legacy run changed=%v run=%#v err=%v", changed, waiting, err)
	}
	now := time.Now().UTC()
	claimed, ok, err := store.ClaimRunWake(ctx, run.ID, waiting.Revision, "legacy-token", now, now.Add(time.Minute))
	if err != nil || !ok || claimed.Status != RunStatusResuming || claimed.ContinuationCount != 4 {
		t.Fatalf("legacy fallback claim ok=%v run=%#v err=%v", ok, claimed, err)
	}
}

func persistJSONJobStatus(t *testing.T, store *JSONStore, record jobs.JobRecord, status jobs.JobStatus) jobs.JobRecord {
	t.Helper()
	granted, revision, err := store.ReserveQueryCapacity(context.Background(), record, jobs.CapacityLimits{
		MaxGlobal: 100, MaxPerThread: 100, MaxPerConnection: 100,
	})
	if err != nil || !granted {
		t.Fatalf("ReserveQueryCapacity %s granted=%v revision=%d err=%v", record.ID, granted, revision, err)
	}
	record.Revision = revision
	if status != record.Status {
		record.Status = status
		revision, err = store.UpsertQueryJobCAS(context.Background(), record, revision)
		if err != nil {
			t.Fatalf("UpsertQueryJobCAS %s: %v", record.ID, err)
		}
		record.Revision = revision
	}
	return record
}

func TestJSONReplaceRunQueryDependencyDetachesOnlyEligibleNamedDependency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, thread, run := seedLongHorizonJSONRun(t, t.TempDir())
	now := time.Now().UTC()
	makeJob := func(id, replaces string, status jobs.JobStatus) jobs.JobRecord {
		record := testDurableJob(thread, run, now)
		record.ID, record.IdempotencyKey = id, "idem-"+id
		record.ReplacesJobID = replaces
		return persistJSONJobStatus(t, store, record, status)
	}
	oldA := makeJob("job-old-a", "", jobs.JobStatusFailed)
	oldB := makeJob("job-old-b", "", jobs.JobStatusFailed)
	active := makeJob("job-active", "", jobs.JobStatusReserved)
	completed := makeJob("job-completed-unvalidated", "", jobs.JobStatusCompleted)
	replacement := makeJob("job-replacement", oldA.ID, jobs.JobStatusReserved)
	alternative := makeJob("job-alternative", oldA.ID, jobs.JobStatusReserved)
	completedReplacement := makeJob("job-completed-replacement", completed.ID, jobs.JobStatusReserved)
	for _, job := range []jobs.JobRecord{oldA, oldB, active, completed} {
		if _, err := store.UpsertRunQueryDependency(ctx, RunQueryDependency{
			RunID: run.ID, JobID: job.ID, RequiredForCompletion: true, ValidationState: QueryValidationUnvalidated,
		}); err != nil {
			t.Fatalf("UpsertRunQueryDependency %s: %v", job.ID, err)
		}
	}
	request := RunQueryDependency{
		RunID: run.ID, JobID: replacement.ID, RequiredForCompletion: true, ValidationState: QueryValidationUnvalidated,
	}
	created, err := store.ReplaceRunQueryDependency(ctx, run.ID, oldA.ID, request)
	if err != nil || created.JobID != replacement.ID || created.Detached || !created.RequiredForCompletion {
		t.Fatalf("ReplaceRunQueryDependency=%#v err=%v", created, err)
	}
	dependencies, err := store.ListRunQueryDependencies(ctx, run.ID)
	if err != nil {
		t.Fatalf("ListRunQueryDependencies: %v", err)
	}
	byID := make(map[string]RunQueryDependency, len(dependencies))
	for _, dependency := range dependencies {
		byID[dependency.JobID] = dependency
	}
	if !byID[oldA.ID].Detached || byID[oldA.ID].ReplacementJobID != replacement.ID ||
		byID[oldB.ID].Detached || byID[active.ID].Detached || byID[replacement.ID].Detached {
		t.Fatalf("dependencies after replacement=%#v", dependencies)
	}
	if replay, err := store.ReplaceRunQueryDependency(ctx, run.ID, oldA.ID, request); err != nil || replay.JobID != replacement.ID {
		t.Fatalf("idempotent replacement=%#v err=%v", replay, err)
	}
	if _, err := store.ReplaceRunQueryDependency(ctx, run.ID, oldA.ID, RunQueryDependency{
		RunID: run.ID, JobID: alternative.ID, RequiredForCompletion: true,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting replacement err=%v, want ErrConflict", err)
	}
	if _, err := store.ReplaceRunQueryDependency(ctx, run.ID, active.ID, RunQueryDependency{
		RunID: run.ID, JobID: alternative.ID, RequiredForCompletion: true,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("active dependency replacement err=%v, want ErrConflict", err)
	}
	if _, err := store.ReplaceRunQueryDependency(ctx, run.ID, completed.ID, RunQueryDependency{
		RunID: run.ID, JobID: completedReplacement.ID, RequiredForCompletion: true,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("completed unvalidated dependency replacement err=%v, want ErrConflict", err)
	}
	sameThreadOldRun, err := store.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatalf("CreateRun same-thread old: %v", err)
	}
	currentRootRecord := testDurableJob(thread, run, now)
	currentRootRecord.ID, currentRootRecord.IdempotencyKey = "job-current-root-for-foreign-successor", "idem-job-current-root-for-foreign-successor"
	currentRoot := persistJSONJobStatus(t, store, currentRootRecord, jobs.JobStatusFailed)
	if _, err := store.UpsertRunQueryDependency(ctx, RunQueryDependency{
		RunID: run.ID, JobID: currentRoot.ID, RequiredForCompletion: true, ValidationState: QueryValidationRejected,
	}); err != nil {
		t.Fatalf("attach root for foreign successor: %v", err)
	}
	foreignSuccessorRecord := testDurableJob(thread, sameThreadOldRun, now)
	foreignSuccessorRecord.ID, foreignSuccessorRecord.IdempotencyKey = "job-foreign-successor", "idem-job-foreign-successor"
	foreignSuccessorRecord.ReplacesJobID = currentRoot.ID
	foreignSuccessor := persistJSONJobStatus(t, store, foreignSuccessorRecord, jobs.JobStatusReserved)
	if _, err := store.ReplaceRunQueryDependency(ctx, run.ID, currentRoot.ID, RunQueryDependency{
		RunID: run.ID, JobID: foreignSuccessor.ID, RequiredForCompletion: true, ValidationState: QueryValidationUnvalidated,
	}); err != nil {
		t.Fatalf("adopt same-thread foreign-run successor: %v", err)
	}
	foreignSuccessorAfter, ok, err := store.GetQueryJob(ctx, foreignSuccessor.ID)
	if err != nil || !ok || foreignSuccessorAfter.Owner.RunID != sameThreadOldRun.ID {
		t.Fatalf("foreign successor ownership changed: %#v ok=%v err=%v", foreignSuccessorAfter, ok, err)
	}
	foreignOldRecord := testDurableJob(thread, sameThreadOldRun, now)
	foreignOldRecord.ID, foreignOldRecord.IdempotencyKey = "job-foreign-old", "idem-job-foreign-old"
	foreignOld := persistJSONJobStatus(t, store, foreignOldRecord, jobs.JobStatusFailed)
	if _, err := store.UpsertRunQueryDependency(ctx, RunQueryDependency{
		RunID: run.ID, JobID: foreignOld.ID, RequiredForCompletion: true, ValidationState: QueryValidationRejected,
	}); err != nil {
		t.Fatalf("attach same-thread foreign job: %v", err)
	}
	foreignAlternativeRecord := testDurableJob(thread, run, now)
	foreignAlternativeRecord.ID, foreignAlternativeRecord.IdempotencyKey = "job-foreign-alternative", "idem-job-foreign-alternative"
	foreignAlternativeRecord.ReplacesJobID = foreignOld.ID
	foreignAlternative := persistJSONJobStatus(t, store, foreignAlternativeRecord, jobs.JobStatusReserved)
	if _, err := store.ReplaceRunQueryDependency(ctx, run.ID, foreignOld.ID, RunQueryDependency{
		RunID: run.ID, JobID: foreignAlternative.ID, RequiredForCompletion: true, ValidationState: QueryValidationUnvalidated,
	}); err != nil {
		t.Fatalf("replace attached same-thread foreign job: %v", err)
	}
	foreignOldAfter, ok, err := store.GetQueryJob(ctx, foreignOld.ID)
	if err != nil || !ok || foreignOldAfter.Owner.RunID != sameThreadOldRun.ID {
		t.Fatalf("foreign old job ownership changed: %#v ok=%v err=%v", foreignOldAfter, ok, err)
	}
	otherThread, err := store.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread other: %v", err)
	}
	otherRun, err := store.CreateRun(ctx, otherThread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatalf("CreateRun other: %v", err)
	}
	if _, err := store.ReplaceRunQueryDependency(ctx, otherRun.ID, oldB.ID, RunQueryDependency{
		RunID: otherRun.ID, JobID: alternative.ID, RequiredForCompletion: true,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("wrong-run replacement err=%v, want ErrNotFound", err)
	}
	replacement.Owner.ThreadID = otherThread.ID
	replacement.Owner.RunID = otherRun.ID
	store.longHorizonMu.Lock()
	replacementWrapper, corruptErr := store.readJSONQueryJobLocked(replacement.ID, false)
	if corruptErr == nil {
		replacementWrapper.Job.Owner = replacement.Owner
		corruptErr = store.writeJSONQueryJobLocked(replacementWrapper)
	}
	store.longHorizonMu.Unlock()
	if corruptErr != nil {
		t.Fatalf("corrupt replacement owner for replay fence: %v", corruptErr)
	}
	if _, err := store.ReplaceRunQueryDependency(ctx, run.ID, oldA.ID, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("idempotent replay with foreign replacement owner err=%v, want ErrConflict", err)
	}
}

func TestJSONPreparedDependencyReplacementRollsForwardWithoutDetachingPeers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	store, thread, run := seedLongHorizonJSONRun(t, dir)
	now := time.Now().UTC()
	old := testDurableJob(thread, run, now)
	old.ID, old.IdempotencyKey = "job-crash-old", "idem-crash-old"
	old = persistJSONJobStatus(t, store, old, jobs.JobStatusFailed)
	peerJob := testDurableJob(thread, run, now)
	peerJob.ID, peerJob.IdempotencyKey = "job-crash-peer", "idem-crash-peer"
	peerJob = persistJSONJobStatus(t, store, peerJob, jobs.JobStatusFailed)
	newJob := testDurableJob(thread, run, now)
	newJob.ID, newJob.IdempotencyKey = "job-crash-new", "idem-crash-new"
	newJob = persistJSONJobStatus(t, store, newJob, jobs.JobStatusReserved)
	for _, job := range []jobs.JobRecord{old, peerJob} {
		if _, err := store.UpsertRunQueryDependency(ctx, RunQueryDependency{
			RunID: run.ID, JobID: job.ID, RequiredForCompletion: true, ValidationState: QueryValidationUnvalidated,
		}); err != nil {
			t.Fatalf("upsert dependency %s: %v", job.ID, err)
		}
	}
	oldPath, _ := runQueryDependencyPath(dir, run.ID, old.ID)
	var beforeOld jsonRunQueryDependencyRecord
	if err := readJSONLoose(oldPath, &beforeOld); err != nil {
		t.Fatalf("read old dependency: %v", err)
	}
	afterOld := beforeOld
	afterOld.Detached = true
	afterOld.ReplacementJobID = newJob.ID
	afterOld.UpdatedAt = now.Add(time.Second)
	afterReplacement := jsonRunQueryDependencyRecord{
		Kind: runQueryDependencyRecordKind, SchemaVersion: longHorizonSchemaVersion,
		RunQueryDependency: RunQueryDependency{
			RunID: run.ID, JobID: newJob.ID, RequiredForCompletion: true,
			ValidationState: QueryValidationUnvalidated, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
		},
	}
	transaction := jsonRunDependencyReplacementTransactionRecord{
		Kind: runDependencyReplacementRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ID: "dependency_replace_prepared_crash", BeforeOld: beforeOld,
		AfterOld: afterOld, AfterReplacement: afterReplacement, PreparedAt: now.Add(time.Second),
	}
	journalPath, _ := queryTransactionPath(dir, transaction.ID)
	if err := jsondb.WriteJSONFile(journalPath, transaction); err != nil {
		t.Fatalf("write replacement journal: %v", err)
	}
	replacementPath, _ := runQueryDependencyPath(dir, run.ID, newJob.ID)
	if err := jsondb.WriteJSONFile(replacementPath, afterReplacement); err != nil {
		t.Fatalf("simulate replacement-first crash: %v", err)
	}

	reopened, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen with prepared replacement journal: %v", err)
	}
	dependencies, err := reopened.ListRunQueryDependencies(ctx, run.ID)
	if err != nil {
		t.Fatalf("recover dependency replacement: %v", err)
	}
	byID := make(map[string]RunQueryDependency, len(dependencies))
	for _, dependency := range dependencies {
		byID[dependency.JobID] = dependency
	}
	if !byID[old.ID].Detached || byID[old.ID].ReplacementJobID != newJob.ID || byID[peerJob.ID].Detached || byID[newJob.ID].Detached {
		t.Fatalf("recovered dependencies=%#v", dependencies)
	}
	if _, err := os.Stat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement journal still exists: %v", err)
	}
}

func TestJSONQueryJobIdempotencyLookupRecoversOriginalAndScopesForeignKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, thread, run := seedLongHorizonJSONRun(t, t.TempDir())
	now := time.Now().UTC()
	original := testDurableJob(thread, run, now)
	original.ID = "job-idempotent-original"
	original.IdempotencyKey = "stable-tool-call"
	original = persistJSONJobStatus(t, store, original, jobs.JobStatusSubmitted)

	got, ok, err := store.GetQueryJobByIdempotency(ctx, " BIGQUERY ", " BQ-CONNECTION ", "stable-tool-call")
	if err != nil || !ok || got.ID != original.ID || got.Revision != original.Revision {
		t.Fatalf("idempotency lookup got=%#v ok=%v err=%v", got, ok, err)
	}
	if _, ok, err := store.GetQueryJobByIdempotency(ctx, "bigquery", "foreign-connection", "stable-tool-call"); err != nil || ok {
		t.Fatalf("foreign connection lookup ok=%v err=%v", ok, err)
	}
	foreign := testDurableJob(thread, run, now)
	foreign.ID = "job-idempotent-foreign-provider"
	foreign.Provider = "databricks"
	foreign.ConnectionID = "dbx-connection"
	foreign.IdempotencyKey = original.IdempotencyKey
	foreign = persistJSONJobStatus(t, store, foreign, jobs.JobStatusReserved)
	got, ok, err = store.GetQueryJobByIdempotency(ctx, "databricks", "dbx-connection", original.IdempotencyKey)
	if err != nil || !ok || got.ID != foreign.ID {
		t.Fatalf("foreign provider identity got=%#v ok=%v err=%v", got, ok, err)
	}
	duplicate := original
	duplicate.ID = "job-idempotent-duplicate"
	duplicate.Status = jobs.JobStatusReserved
	duplicate.Revision = 0
	if _, _, err := store.ReserveQueryCapacity(ctx, duplicate, jobs.CapacityLimits{
		MaxGlobal: 100, MaxPerThread: 100, MaxPerConnection: 100,
	}); !errors.Is(err, jobs.ErrRevisionConflict) {
		t.Fatalf("duplicate identity reservation err=%v, want revision conflict", err)
	}
	recovered, ok, err := store.GetQueryJobByIdempotency(ctx, original.Provider, original.ConnectionID, original.IdempotencyKey)
	if err != nil || !ok || recovered.ID != original.ID {
		t.Fatalf("recover original after duplicate got=%#v ok=%v err=%v", recovered, ok, err)
	}
	legacySubmittedAt := original.SubmittedAt.Add(-time.Minute)
	writeLegacyDuplicate := func(id string) {
		t.Helper()
		legacy := original
		legacy.ID = id
		legacy.SubmittedAt = legacySubmittedAt
		legacy.LastAccess = legacySubmittedAt
		legacy.Revision = 1
		legacy.WakeEvents = nil
		path, err := jsondb.QueryJobPath(store.cfgDir, id)
		if err != nil {
			t.Fatalf("legacy duplicate path: %v", err)
		}
		wrapper := jsonQueryJobRecord{
			Kind: queryJobRecordKind, SchemaVersion: longHorizonSchemaVersion,
			ConnectionKey: queryConnectionKey(legacy), Job: legacy,
			ValidationState: string(legacy.ValidationState), ValidationDetail: legacy.ValidationDetail, UpdatedAt: legacySubmittedAt,
		}
		if err := jsondb.WriteJSONFile(path, wrapper); err != nil {
			t.Fatalf("write legacy duplicate: %v", err)
		}
	}
	writeLegacyDuplicate("job-legacy-b")
	writeLegacyDuplicate("job-legacy-a")
	winner, ok, err := store.GetQueryJobByIdempotency(ctx, original.Provider, original.ConnectionID, original.IdempotencyKey)
	if err != nil || !ok || winner.ID != "job-legacy-a" {
		t.Fatalf("deterministic legacy winner=%#v ok=%v err=%v", winner, ok, err)
	}
}
