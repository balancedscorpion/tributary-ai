package jobs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMonitorErrorsNeverTerminalizeExecution(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{DetachedRepoll: true, NativePagination: true},
		pollFn: func(call int, _ NativeHandle) (StatusSnapshot, error) {
			if failing.Load() {
				return StatusSnapshot{}, errors.New("transient network blip")
			}
			return StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
		fetchFn: func(int, FetchCursor) (ResultPage, error) { return ResultPage{}, nil },
	}
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		snap, ok := r.Snapshot(job.ID, testOwner())
		return ok && snap.MonitorHealth == MonitorHealthDegraded
	}, "monitor health degraded")

	snap, _ := r.Snapshot(job.ID, testOwner())
	if isTerminalStatus(snap.Status) {
		t.Fatalf("monitor errors terminalized execution: %s (invariant I3)", snap.Status)
	}

	failing.Store(false)
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "recovery to completion")
	snap, _ = r.Snapshot(job.ID, testOwner())
	if snap.MonitorHealth != MonitorHealthOK {
		t.Fatalf("monitor health=%s want ok after recovery", snap.MonitorHealth)
	}
}

func TestHardBudgetAutoCancelTerminalizesAsExpired(t *testing.T) {
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{CancelConfirmable: true},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner:    testOwner(),
		Provider: "fake",
		SQL:      "select 1",
		Budgets:  Budgets{HardExecution: 30 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusExpired
	}, "hard budget expiry")
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.CancelReason != CancelReasonBudget {
		t.Fatalf("cancel reason=%s want budget", snap.CancelReason)
	}
	if snap.CancelOutcome != CancelOutcomeConfirmed {
		t.Fatalf("cancel outcome=%s want confirmed (budget cancels still confirm)", snap.CancelOutcome)
	}
}

func TestSuspectedStallIsWakeOnlyNeverAutoCancel(t *testing.T) {
	splits := int64(7) // never advances
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{
			ProgressSignals:    []ProgressSignal{SignalSplits},
			QueuedStateVisible: true,
		},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning, SplitsDone: &splits}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{
		Policy: &PolicyConfig{StallWindow: 5 * time.Millisecond, StallWindows: 2},
	})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		snap, ok := r.Snapshot(job.ID, testOwner())
		return ok && snap.PolicyHealth == PolicyHealthSuspectedStalled
	}, "suspected stall detected")

	snap, _ := r.Snapshot(job.ID, testOwner())
	if isTerminalStatus(snap.Status) {
		t.Fatalf("suspected stall must never auto-cancel (default off); status=%s", snap.Status)
	}
	stallWakes := 0
	for _, ev := range snap.WakeEvents {
		if ev.Kind == WakeSuspectedStalled {
			stallWakes++
		}
	}
	if stallWakes != 1 {
		t.Fatalf("stall wake events=%d want exactly 1 (deduped)", stallWakes)
	}
	if adapter.cancelCalls != 0 {
		t.Fatalf("no provider cancel may be issued on suspected stall")
	}
}

func TestSoftBudgetsEmitWakeSignalsOnly(t *testing.T) {
	adapter := &fakeAdapter{
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner:    testOwner(),
		Provider: "fake",
		SQL:      "select 1",
		Budgets:  Budgets{SoftExecution: 10 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		snap, ok := r.Snapshot(job.ID, testOwner())
		return ok && snap.PolicyHealth == PolicyHealthSoftExecution
	}, "soft execution breach")
	snap, _ := r.Snapshot(job.ID, testOwner())
	if isTerminalStatus(snap.Status) {
		t.Fatalf("soft budget must not terminalize; status=%s", snap.Status)
	}
}

func TestDrainProviderCommitsAtomicallyAndRedirectsFetch(t *testing.T) {
	mat := &fakeMaterializer{}
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{
			RequiresContinuousDrain: true,
			MaxSafePollInterval:     time.Second,
		},
		pollFn: func(call int, h NativeHandle) (StatusSnapshot, error) {
			nextURI := "page-" + string(rune('0'+call))
			next := NativeHandle{Provider: "fake", ID: h.ID, Extra: map[string]string{"next_uri": nextURI}}
			if call < 3 {
				return StatusSnapshot{
					State:         ProviderStateRunning,
					UpdatedHandle: &next,
					DrainedPages: []ResultPage{{
						Rows:        []map[string]any{{"n": call}},
						CursorAfter: &next,
					}},
				}, nil
			}
			return StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Materializer: mat, Store: NewMemoryJobStore()})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner:    testOwner(),
		Provider: "fake",
		SQL:      "select 1",
		Budgets:  Budgets{MaxResultRows: 1}, // force explicit truncation
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "drain job completed")

	commits := mat.drainCommits()
	// Commit 0 is the launch-time initial anchor (empty page here: this
	// fake's submit response carries no Initial); commits 1..2 are the
	// polled observations. Every durable drain record has DrainSeq >= 1.
	if len(commits) != 4 {
		t.Fatalf("drain commits=%d want 4 (initial anchor + two row observations + atomic terminal observation)", len(commits))
	}
	// Seq strictly increasing from 1: the sink's crash-replay dedupe key.
	for i, c := range commits {
		if c.Seq != int64(i+1) {
			t.Fatalf("commit %d seq=%d want %d", i, c.Seq, i+1)
		}
		if c.Record.ID != job.ID || c.Record.DrainSeq != c.Seq {
			t.Fatalf("commit %d record out of sync: %+v", i, c.Record)
		}
	}
	for i, c := range commits[1:] {
		if c.Handle.Extra["next_uri"] == "" {
			t.Fatalf("observation commit %d missing post-page cursor; chunk+cursor must commit together", i+1)
		}
	}
	// Bounded durably: row ceiling 1 → anchor empty, first observation 1
	// row, second 0 rows with explicit truncation (P1-6: never the
	// unbounded original page).
	if len(commits[0].Page.Rows) != 0 {
		t.Fatalf("anchor commit rows=%d want 0", len(commits[0].Page.Rows))
	}
	if len(commits[1].Page.Rows) != 1 {
		t.Fatalf("commit 1 rows=%d want 1", len(commits[1].Page.Rows))
	}
	if len(commits[2].Page.Rows) != 0 || !commits[2].Page.Truncated {
		t.Fatalf("commit 2 must be empty+truncated, got rows=%d truncated=%v", len(commits[2].Page.Rows), commits[2].Page.Truncated)
	}
	if !mat.finalized() {
		t.Fatalf("materializer Finalize must run exactly once at terminal")
	}
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.Materialization != MaterializationTruncated {
		t.Fatalf("materialization=%s want truncated", snap.Materialization)
	}
	if snap.DrainSeq != 4 {
		t.Fatalf("job DrainSeq=%d want 4", snap.DrainSeq)
	}
	if _, err := r.FetchPage(context.Background(), job.ID, testOwner(), FetchCursor{}); !errors.Is(err, ErrUseMaterializedResults) {
		t.Fatalf("err=%v want ErrUseMaterializedResults for drain providers", err)
	}
	// The advancing cursor must be durable (Trino restart-adoption).
	rec, ok, _ := r.store.GetQueryJob(context.Background(), job.ID)
	if !ok || rec.Handle.Extra["next_uri"] == "" {
		t.Fatalf("advancing cursor was not persisted; rec=%+v", rec.Handle)
	}
}

func TestDrainProviderRefusedWithoutCommitter(t *testing.T) {
	adapter := &fakeAdapter{caps: ProviderCapabilities{RequiresContinuousDrain: true}}
	r := newTestRegistry(t, RegistryOptions{}) // no materializer at all
	r.RegisterAdapter(adapter)
	_, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if !errors.Is(err, ErrDrainCommitUnsupported) {
		t.Fatalf("err=%v want ErrDrainCommitUnsupported (no best-effort drain)", err)
	}
	if adapter.submitCount() != 0 {
		t.Fatalf("refusal must happen before submit")
	}
}

type fakeMaterializer struct {
	mu        sync.Mutex
	chunks    []ResultPage
	commits   []DrainCommit
	done      bool
	commitRev int64
	commitErr error
}

func (m *fakeMaterializer) AppendChunk(_ context.Context, _ string, _ JobOwner, page ResultPage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.chunks = append(m.chunks, page)
	return nil
}

func (m *fakeMaterializer) Finalize(_ context.Context, _ string, _ JobOwner, _ *QueryResult, _ MaterializationSummary) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.done = true
	return nil
}

func (m *fakeMaterializer) CommitDrainObservation(_ context.Context, commit DrainCommit) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.commitErr != nil {
		return 0, m.commitErr
	}
	m.commits = append(m.commits, commit)
	m.commitRev++
	return m.commitRev, nil
}

func (m *fakeMaterializer) CommitDrainObservationFenced(ctx context.Context, commit DrainCommit, _ string) (int64, error) {
	return m.CommitDrainObservation(ctx, commit)
}

func (m *fakeMaterializer) drainCommits() []DrainCommit {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]DrainCommit(nil), m.commits...)
}

func (m *fakeMaterializer) finalized() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.done
}

func TestRunCompletionSweepSkipsManagedJobs(t *testing.T) {
	adapter := &fakeAdapter{
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	managed, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	legacy, err := r.Register(
		JobOwner{ThreadID: "thread-1", Handle: "tok_legacy"},
		"bigquery", "select 2", "select 2", nil,
		func(ctx context.Context) (bool, *QueryResult, error) {
			<-ctx.Done()
			return false, nil, ctx.Err()
		},
		nil,
	)
	if err != nil {
		t.Fatalf("register legacy: %v", err)
	}

	cancelled := r.CancelActiveByThread("thread-1")
	if len(cancelled) != 1 || cancelled[0] != legacy.ID {
		t.Fatalf("sweep cancelled=%v want only the legacy job %s (invariant I5)", cancelled, legacy.ID)
	}
	if got := snapshotStatus(r, managed.ID, testOwner()); isTerminalStatus(got) || got == JobStatusCancelRequested {
		t.Fatalf("managed job status=%s; the run-completion sweep must not touch it", got)
	}
}

func TestPolicyEvaluationPure(t *testing.T) {
	cfg := PolicyConfig{}.withDefaults()
	base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	started := base.Add(time.Minute)

	// Within budgets.
	v := evaluatePolicy(cfg, policyInput{
		budgets:     cfg.DefaultBudgets,
		submittedAt: base, startedAt: &started,
		providerState: ProviderStateRunning,
	}, base.Add(2*time.Minute))
	if v.health != PolicyHealthOK {
		t.Fatalf("health=%s want ok", v.health)
	}

	// Soft queue: never started.
	v = evaluatePolicy(cfg, policyInput{
		budgets:     cfg.DefaultBudgets,
		submittedAt: base, queued: true, providerState: ProviderStateQueued,
	}, base.Add(11*time.Minute))
	if v.health != PolicyHealthSoftQueue || v.wake != WakeSoftQueue {
		t.Fatalf("verdict=%+v want soft queue", v)
	}

	// Soft execution.
	v = evaluatePolicy(cfg, policyInput{
		budgets:     cfg.DefaultBudgets,
		submittedAt: base, startedAt: &started, providerState: ProviderStateRunning,
	}, started.Add(31*time.Minute))
	if v.health != PolicyHealthSoftExecution {
		t.Fatalf("health=%s want soft execution", v.health)
	}

	// Hard beats everything and requests cancel.
	v = evaluatePolicy(cfg, policyInput{
		budgets:     cfg.DefaultBudgets,
		submittedAt: base, startedAt: &started, providerState: ProviderStateRunning,
	}, base.Add(7*time.Hour))
	if v.health != PolicyHealthHardExceeded || !v.hardCancel {
		t.Fatalf("verdict=%+v want hard exceeded + cancel", v)
	}

	// Stall requires declared signals; wall-clock alone is never progress.
	v = evaluatePolicy(cfg, policyInput{
		budgets:     cfg.DefaultBudgets,
		submittedAt: base, startedAt: &started,
		hasSignals: false, lastProgress: base,
		providerState: ProviderStateRunning,
	}, started.Add(20*time.Minute))
	if v.health == PolicyHealthSuspectedStalled {
		t.Fatalf("stall must not fire without provider progress signals")
	}

	if progressAdvanced(map[ProgressSignal]int64{SignalSplits: 5}, map[ProgressSignal]int64{SignalSplits: 5}) {
		t.Fatalf("equal counters are not progress")
	}
	if !progressAdvanced(map[ProgressSignal]int64{SignalSplits: 5}, map[ProgressSignal]int64{SignalSplits: 6}) {
		t.Fatalf("advancing counters are progress")
	}
}
