package jobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestCrossProcessBarrierWakesWithoutLocalTransition is the two-registry
// regression for the barrier P0: registry A owns and drives the job to a
// terminal state (persisting the terminal record and its wake); registry B
// — sharing only the durable store, holding no lease and performing no
// local transition — must wake from WaitForJobRefs via bounded durable
// refresh and atomically claim the wake.
func TestCrossProcessBarrierWakesWithoutLocalTransition(t *testing.T) {
	store := NewMemoryJobStore()
	owner := testOwner()

	var finish atomic.Bool
	adapterA := &fakeAdapter{
		name: "fake",
		caps: ProviderCapabilities{DetachedRepoll: true, NativePagination: true},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			if finish.Load() {
				return StatusSnapshot{State: ProviderStateSucceeded}, nil
			}
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
		fetchFn: func(int, FetchCursor) (ResultPage, error) {
			return ResultPage{Rows: []map[string]any{{"n": 42}}}, nil
		},
	}
	rA := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "proc-A"})
	rA.RegisterAdapter(adapterA)

	// Registry B: separate process simulation — own adapter instance, no
	// launch, no rehydrate, no lease. Loader-capable materializer so the
	// externally completed result is servable after the wake.
	adapterB := &fakeAdapter{name: "fake", caps: ProviderCapabilities{DetachedRepoll: true, NativePagination: true}}
	matB := &loaderMaterializer{loadResult: &QueryResult{Rows: []map[string]any{{"n": 42}}}}
	rB := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "proc-B", Materializer: matB})
	rB.RegisterAdapter(adapterB)

	job, err := rA.Launch(context.Background(), LaunchRequest{Owner: owner, Provider: "fake", SQL: "select 42"})
	if err != nil {
		t.Fatalf("launch on A: %v", err)
	}

	// B starts waiting while the job is still running. Its only knowledge
	// comes from the shared store (lazy load); wrong owners stay excluded.
	if _, ok := rB.Snapshot(job.ID, JobOwner{ThreadID: "thread-1", Handle: "tok_wrong"}); ok {
		t.Fatalf("cross-process visibility must not weaken owner scope")
	}
	type waitOut struct {
		res *WaitResult
		err error
	}
	got := make(chan waitOut, 1)
	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		res, err := rB.WaitForJobRefs(waitCtx, []JobRef{{ID: job.ID, Owner: owner}}, WaitOptions{Claim: true})
		got <- waitOut{res, err}
	}()

	// Give B time to enter its blocked state, then let A terminalize.
	time.Sleep(50 * time.Millisecond)
	select {
	case out := <-got:
		t.Fatalf("B returned before any terminal state existed: %+v / %v", out.res, out.err)
	default:
	}
	finish.Store(true)

	out := <-got
	if out.err != nil {
		t.Fatalf("B wait: %v", out.err)
	}
	if out.res.Job == nil || out.res.Job.ID != job.ID || out.res.Job.Status != JobStatusCompleted {
		t.Fatalf("B observed %+v want completed job via durable refresh", out.res.Job)
	}
	if out.res.Event == nil || out.res.Event.Kind != WakeTerminal {
		t.Fatalf("B must claim the durable terminal wake, got %+v", out.res.Event)
	}

	// Exactly-once across processes: A (or anyone) claiming afterwards
	// gets nothing.
	claimedAgain, err := rA.ClaimWakeEvents(job.ID, owner)
	if err != nil || len(claimedAgain) != 0 {
		t.Fatalf("wake claimed twice across processes: %v / %+v", err, claimedAgain)
	}

	// B never drove the job: no monitor, no submit, no SQL rerun.
	if adapterB.submitCount() != 0 {
		t.Fatalf("B must never submit")
	}
	adapterB.mu.Lock()
	bPolls := adapterB.pollCalls
	adapterB.mu.Unlock()
	if bPolls != 0 {
		t.Fatalf("B must never poll/drive; polls=%d", bPolls)
	}
	snapB, ok := rB.Snapshot(job.ID, owner)
	if !ok || snapB.Status != JobStatusCompleted {
		t.Fatalf("B snapshot after wake: ok=%v %+v", ok, snapB)
	}
	if res, err := rB.Results(job.ID, owner); err != nil || res == nil || len(res.Rows) != 1 {
		t.Fatalf("B must serve the externally materialized result: %+v / %v", res, err)
	}

	// B's local revision stayed coherent with the store: a sidecar write
	// from B (validation) succeeds without a revision conflict.
	if err := rB.SetValidation(job.ID, owner, ValidationValidated, "verified on B"); err != nil {
		t.Fatalf("post-wake validation on B hit a revision problem: %v", err)
	}
}

// TestCrossProcessBarrierSeesExternalNonTerminalWake covers the softer
// case: A emits a non-terminal wake (suspected stall) and keeps running;
// B's waiter must still observe and claim it through durable refresh.
func TestCrossProcessBarrierSeesExternalNonTerminalWake(t *testing.T) {
	store := NewMemoryJobStore()
	owner := testOwner()
	splits := int64(3) // frozen → suspected stall on A
	adapterA := &fakeAdapter{
		name: "fake",
		caps: ProviderCapabilities{ProgressSignals: []ProgressSignal{SignalSplits}},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning, SplitsDone: &splits}, nil
		},
	}
	rA := newTestRegistry(t, RegistryOptions{
		Store:      store,
		InstanceID: "proc-A",
		Policy:     &PolicyConfig{StallWindow: 5 * time.Millisecond, StallWindows: 2},
	})
	rA.RegisterAdapter(adapterA)
	rB := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "proc-B"})
	rB.RegisterAdapter(&fakeAdapter{name: "fake"})

	job, err := rA.Launch(context.Background(), LaunchRequest{Owner: owner, Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch on A: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := rB.WaitForJobRefs(waitCtx, []JobRef{{ID: job.ID, Owner: owner}}, WaitOptions{Claim: true})
	if err != nil {
		t.Fatalf("B wait: %v", err)
	}
	if res.Event == nil || res.Event.Kind != WakeSuspectedStalled {
		t.Fatalf("B must observe A's stall wake, got %+v", res.Event)
	}
	if isTerminalStatus(res.Job.Status) {
		t.Fatalf("job must still be running; B woke on a non-terminal event")
	}
	_ = rA.RequestCancel(job.ID, owner, CancelReasonNoLongerNeeded)
}
