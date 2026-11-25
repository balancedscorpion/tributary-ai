package jobs

import (
	"context"
	"testing"
	"time"
)

func drainHandle(id, uri string) NativeHandle {
	return NativeHandle{Provider: "fake", ID: id, Extra: map[string]string{"next_uri": uri}}
}

// TestDrainInitialPageCommittedBeforeMonitor proves: initial rows are not
// lost, the initial page + post-page cursor commit atomically (seq 1)
// before any poll, bounds apply to the initial page, and the monitor's
// first poll starts from the post-initial cursor.
func TestDrainInitialPageCommittedBeforeMonitor(t *testing.T) {
	mat := &fakeMaterializer{}
	h0 := drainHandle("q1", "uri-0")
	h1 := drainHandle("q1", "uri-1")
	h2 := drainHandle("q1", "uri-2")
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{RequiresContinuousDrain: true, MaxSafePollInterval: time.Second},
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{
				Handle: h0,
				Initial: &StatusSnapshot{
					State:         ProviderStateRunning,
					UpdatedHandle: &h1,
					DrainedPages: []ResultPage{{
						Rows:        []map[string]any{{"n": 0}, {"n": 100}}, // 2 rows; ceiling clips to 1
						CursorAfter: &h1,
					}},
				},
			}, nil
		},
		pollFn: func(call int, h NativeHandle) (StatusSnapshot, error) {
			if call == 1 {
				return StatusSnapshot{
					State:         ProviderStateRunning,
					UpdatedHandle: &h2,
					DrainedPages:  []ResultPage{{Rows: []map[string]any{{"n": 1}}, CursorAfter: &h2}},
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
		Budgets:  Budgets{MaxResultRows: 1},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}

	// The initial observation must already be durable when Launch returns
	// — before any monitor poll could have run.
	initialCommits := mat.drainCommits()
	if len(initialCommits) != 1 {
		t.Fatalf("commits at launch-return=%d want 1 (initial page committed before monitoring)", len(initialCommits))
	}
	first := initialCommits[0]
	if first.Seq != 1 || len(first.Page.Rows) != 1 || !first.Page.Truncated {
		t.Fatalf("initial commit must be seq=1 with the BOUNDED page (1 row, truncated): %+v", first)
	}
	if first.Handle.Extra["next_uri"] != "uri-1" {
		t.Fatalf("initial commit cursor=%q want uri-1 (post-page cursor commits with the page)", first.Handle.Extra["next_uri"])
	}
	if first.Record.DrainSeq != 1 || first.Record.Status != JobStatusSubmitted {
		t.Fatalf("initial commit record out of sync: %+v", first.Record)
	}

	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "completed")

	// The monitor's first poll must start from the post-initial cursor —
	// never from the consumed submit cursor (exactly-once advancement).
	polled, ok := adapter.firstPolledHandle()
	if !ok || polled.Extra["next_uri"] != "uri-1" {
		t.Fatalf("first poll handle=%v want uri-1", polled)
	}
	commits := mat.drainCommits()
	if len(commits) != 3 {
		t.Fatalf("total commits=%d want 3 (initial + row observation + atomic terminal observation)", len(commits))
	}
	if commits[1].Seq != 2 || commits[1].Handle.Extra["next_uri"] != "uri-2" {
		t.Fatalf("second commit must advance seq/cursor exactly once: %+v", commits[1])
	}
	// The initial row survived into the in-memory result (bounded).
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.Result == nil || len(snap.Result.Rows) != 1 || snap.Result.Rows[0]["n"] != 0 {
		t.Fatalf("initial row lost from result: %+v", snap.Result)
	}
	if snap.Materialization != MaterializationTruncated {
		t.Fatalf("materialization=%s want truncated (bounds applied to initial page)", snap.Materialization)
	}
}

// TestDrainInitialTerminalCompletesWithoutPolling: a submit response that
// is already terminal finalizes with its inline rows and never polls.
func TestDrainInitialTerminalCompletesWithoutPolling(t *testing.T) {
	mat := &fakeMaterializer{}
	h1 := drainHandle("q2", "uri-final")
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{RequiresContinuousDrain: true},
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{
				Handle: drainHandle("q2", "uri-0"),
				Initial: &StatusSnapshot{
					State:         ProviderStateSucceeded,
					UpdatedHandle: &h1,
					DrainedPages:  []ResultPage{{Rows: []map[string]any{{"n": 7}}, CursorAfter: &h1}},
				},
			}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Materializer: mat, Store: NewMemoryJobStore()})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if job.Status != JobStatusCompleted {
		t.Fatalf("status=%s want completed synchronously", job.Status)
	}
	if adapter.pollCalls != 0 {
		t.Fatalf("terminal initial snapshot must not start polling; polls=%d", adapter.pollCalls)
	}
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.Result == nil || len(snap.Result.Rows) != 1 {
		t.Fatalf("inline terminal rows lost: %+v", snap.Result)
	}
	if !mat.finalized() {
		t.Fatalf("materializer must be finalized")
	}
	if len(mat.drainCommits()) != 1 {
		t.Fatalf("commits=%d want 1", len(mat.drainCommits()))
	}
}

// TestRehydrateRefusesDrainMissingInitialCommit: a dispatched/active drain
// record with DrainSeq==0 means the submit response's consumable page was
// acknowledged but never durably committed — resuming would lose rows, so
// rehydration must mark recovery_required. The DrainSeq>=1 control record
// adopts and resumes from the persisted cursor with the next Seq.
func TestRehydrateRefusesDrainMissingInitialCommit(t *testing.T) {
	store := NewMemoryJobStore()
	// Keep the fake committer's synthetic revision aligned with the seeded
	// in-memory store. Production drain committers update chunk+record in the
	// same transaction and return that record revision.
	mat := &fakeMaterializer{}
	owner := testOwner()
	now := time.Now()
	// Hazard: running drain record without the initial commit.
	_ = store.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-fake-nz", Provider: "fake", Status: JobStatusRunning,
		Owner: owner, Handle: drainHandle("qa", "uri-a0"),
		SubmittedAt: now.Add(-time.Minute), LastAccess: now,
		SubmitDispatched: true, DrainSeq: 0,
	})
	// Control: legitimate record with the initial commit durable.
	_ = store.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-fake-okz", Provider: "fake", Status: JobStatusRunning,
		Owner: owner, Handle: drainHandle("qb", "uri-b1"),
		SubmittedAt: now.Add(-time.Minute), LastAccess: now,
		SubmitDispatched: true, DrainSeq: 1,
	})
	h2 := drainHandle("qb", "uri-b2")
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{RequiresContinuousDrain: true},
		adoptFn: func(h NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
			return AdoptOutcomeAdopted, StatusSnapshot{State: ProviderStateRunning}, nil
		},
		pollFn: func(call int, h NativeHandle) (StatusSnapshot, error) {
			if call == 1 {
				return StatusSnapshot{
					State:         ProviderStateRunning,
					UpdatedHandle: &h2,
					DrainedPages:  []ResultPage{{Rows: []map[string]any{{"n": 2}}, CursorAfter: &h2}},
				}, nil
			}
			return StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: mat})
	r.RegisterAdapter(adapter)
	stats, err := r.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if stats.Orphaned != 1 || stats.Adopted != 1 {
		t.Fatalf("stats=%+v want orphaned=1 adopted=1", stats)
	}

	orph, _ := r.Snapshot("qj-fake-nz", owner)
	if orph == nil || orph.Status != JobStatusOrphaned {
		t.Fatalf("hazard record status=%v want orphaned", orph)
	}
	hasRecovery := false
	for _, ev := range orph.WakeEvents {
		if ev.Kind == WakeRecoveryRequired {
			hasRecovery = true
		}
	}
	if !hasRecovery {
		t.Fatalf("missing recovery_required wake on refused drain record")
	}

	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, "qj-fake-okz", owner) == JobStatusCompleted
	}, "control record adopted and completed")
	// Crash-replay dedupe: the resumed monitor's first poll uses the
	// persisted post-initial cursor, and its first commit continues the
	// durable sequence (seq 2), never repeating seq 1.
	polled, ok := adapter.firstPolledHandle()
	if !ok || polled.Extra["next_uri"] != "uri-b1" {
		t.Fatalf("resumed monitor polled %v want persisted cursor uri-b1", polled)
	}
	commits := mat.drainCommits()
	if len(commits) != 2 || commits[0].Seq != 2 || commits[1].Seq != 3 || commits[0].JobID != "qj-fake-okz" {
		t.Fatalf("resumed commits must continue at seq=2 and atomically terminalize at seq=3: %+v", commits)
	}
	if commits[0].Handle.Extra["next_uri"] != "uri-b2" {
		t.Fatalf("resumed commit cursor=%q want uri-b2", commits[0].Handle.Extra["next_uri"])
	}
}

// TestRehydrateReservedDispatchedIsAmbiguous: a crash inside the submit
// window must never expire the reservation. Non-drain providers get a
// reconciliation watch; drain providers are refused outright (the initial
// consumable page may already be lost).
func TestRehydrateReservedDispatchedIsAmbiguous(t *testing.T) {
	owner := testOwner()
	now := time.Now()
	seed := func(store *MemoryJobStore, id string) {
		_ = store.UpsertQueryJob(context.Background(), JobRecord{
			ID: id, Provider: "fake", Status: JobStatusReserved,
			Owner: owner, SubmittedAt: now.Add(-time.Second), LastAccess: now,
			SubmitDispatched: true,
			Budgets:          Budgets{HardExecution: time.Hour},
		})
	}

	// Non-drain: becomes submission_unknown under the durable watch.
	store1 := NewMemoryJobStore()
	seed(store1, "qj-fake-amb1")
	r1 := newTestRegistry(t, RegistryOptions{Store: store1})
	r1.RegisterAdapter(&fakeAdapter{})
	stats1, err := r1.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate 1: %v", err)
	}
	if stats1.Reconciling != 1 || stats1.Expired != 0 {
		t.Fatalf("stats=%+v want reconciling=1, never expired", stats1)
	}
	if got := snapshotStatus(r1, "qj-fake-amb1", owner); got != JobStatusSubmissionUnknown {
		t.Fatalf("status=%s want submission_unknown", got)
	}

	// Drain: refused with recovery_required.
	store2 := NewMemoryJobStore()
	seed(store2, "qj-fake-amb2")
	r2 := newTestRegistry(t, RegistryOptions{Store: store2, Materializer: &fakeMaterializer{}})
	r2.RegisterAdapter(&fakeAdapter{caps: ProviderCapabilities{RequiresContinuousDrain: true}})
	stats2, err := r2.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate 2: %v", err)
	}
	if stats2.Orphaned != 1 {
		t.Fatalf("stats=%+v want orphaned=1", stats2)
	}
	if got := snapshotStatus(r2, "qj-fake-amb2", owner); got != JobStatusOrphaned {
		t.Fatalf("status=%s want orphaned", got)
	}

	// Undispatched reservations still expire safely.
	store3 := NewMemoryJobStore()
	_ = store3.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-fake-resv3", Provider: "fake", Status: JobStatusReserved,
		Owner: owner, SubmittedAt: now, LastAccess: now,
	})
	r3 := newTestRegistry(t, RegistryOptions{Store: store3})
	r3.RegisterAdapter(&fakeAdapter{})
	stats3, err := r3.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate 3: %v", err)
	}
	if stats3.Expired != 1 {
		t.Fatalf("stats=%+v want expired=1", stats3)
	}
}
