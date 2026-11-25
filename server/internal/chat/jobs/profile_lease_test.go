package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

// denyLeaseStore simulates a lease held elsewhere (or acquisition errors).
type denyLeaseStore struct {
	*MemoryJobStore
	acquireErr error
}

func (s *denyLeaseStore) AcquireMonitorLease(context.Context, string, string, time.Duration) (bool, error) {
	if s.acquireErr != nil {
		return false, s.acquireErr
	}
	return false, nil
}

// fenceFailStore fails fenced HANDLE publications with a non-lease error.
type fenceFailStore struct {
	*MemoryJobStore
}

func (s *fenceFailStore) ApplyProfileMutation(ctx context.Context, jobID, holder string, expectedRevision int64, mut ProfileMutation) (ProfileMutationResult, error) {
	if mut.Handle != nil {
		return ProfileMutationResult{}, errors.New("simulated durable write failure")
	}
	return s.MemoryJobStore.ApplyProfileMutation(ctx, jobID, holder, expectedRevision, mut)
}

// leaseOnlyStore has leases but NO fenced profile mutations (hides the
// method by shadowing it with an unexported field of a different type).
type leaseOnlyStore struct {
	inner *MemoryJobStore
}

func (s *leaseOnlyStore) UpsertQueryJob(ctx context.Context, rec JobRecord) error {
	return s.inner.UpsertQueryJob(ctx, rec)
}
func (s *leaseOnlyStore) GetQueryJob(ctx context.Context, id string) (JobRecord, bool, error) {
	return s.inner.GetQueryJob(ctx, id)
}
func (s *leaseOnlyStore) ListActiveQueryJobs(ctx context.Context) ([]JobRecord, error) {
	return s.inner.ListActiveQueryJobs(ctx)
}
func (s *leaseOnlyStore) ListQueryJobsByThread(ctx context.Context, threadID string) ([]JobRecord, error) {
	return s.inner.ListQueryJobsByThread(ctx, threadID)
}
func (s *leaseOnlyStore) DeleteQueryJob(ctx context.Context, id string) error {
	return s.inner.DeleteQueryJob(ctx, id)
}
func (s *leaseOnlyStore) AcquireMonitorLease(ctx context.Context, jobID, holder string, ttl time.Duration) (bool, error) {
	return s.inner.AcquireMonitorLease(ctx, jobID, holder, ttl)
}
func (s *leaseOnlyStore) RenewMonitorLease(ctx context.Context, jobID, holder string, ttl time.Duration) (bool, error) {
	return s.inner.RenewMonitorLease(ctx, jobID, holder, ttl)
}
func (s *leaseOnlyStore) ReleaseMonitorLease(ctx context.Context, jobID, holder string) error {
	return s.inner.ReleaseMonitorLease(ctx, jobID, holder)
}

// --- req 1: unconfirmed lease => zero profile, zero submit ---

func TestProfileLeaseUnconfirmedNeverProfiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"held elsewhere", nil},
		{"acquire error", errors.New("lease store down")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &denyLeaseStore{MemoryJobStore: NewMemoryJobStore(), acquireErr: tc.err}
			adapter := newProfilingAdapter(true)
			r := newTestRegistry(t, RegistryOptions{Store: store})
			r.RegisterAdapter(adapter)
			job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
			if !errors.Is(err, ErrProfilePending) {
				t.Fatalf("err=%v want ErrProfilePending", err)
			}
			if adapter.profileCount() != 0 || adapter.submitCount() != 0 {
				t.Fatalf("unconfirmed lease must mean zero profile/submit: %d/%d", adapter.profileCount(), adapter.submitCount())
			}
			// The durable profiling job remains for the real holder or
			// recovery to drive.
			if job == nil {
				t.Fatalf("caller must receive the durable profiling job")
			}
			rec, ok, _ := store.GetQueryJob(context.Background(), job.ID)
			if !ok || rec.Status != JobStatusProfiling || rec.ProfileDispatched {
				t.Fatalf("record must remain profiling+undispatched: %+v", rec)
			}
		})
	}
}

// --- fence gate: leases without fenced mutations fail closed pre-Profile ---

func TestProfileFenceUnsupportedFailsClosed(t *testing.T) {
	store := &leaseOnlyStore{inner: NewMemoryJobStore()}
	adapter := newProfilingAdapter(true)
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	_, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if !errors.Is(err, ErrProfileFenceUnsupported) {
		t.Fatalf("err=%v want ErrProfileFenceUnsupported", err)
	}
	if adapter.profileCount() != 0 || adapter.submitCount() != 0 {
		t.Fatalf("fence-unsupported must fail closed before Profile: %d/%d", adapter.profileCount(), adapter.submitCount())
	}
	recs, _ := store.ListQueryJobsByThread(context.Background(), "thread-1")
	if len(recs) != 0 {
		t.Fatalf("fail-closed gate must precede any reservation: %+v", recs)
	}
}

// --- req 2: heartbeat lets a profile outlive the lease TTL ---

func TestProfileHeartbeatOutlivesLeaseTTL(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := newProfilingAdapter(true)
	adapter.gate = make(chan struct{})
	r := newTestRegistry(t, RegistryOptions{Store: store, LeaseTTL: 40 * time.Millisecond, InstanceID: "proc-A"})
	r.RegisterAdapter(adapter)
	done := make(chan error, 1)
	var jobID string
	go func() {
		job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
		if job != nil {
			jobID = job.ID
		}
		done <- err
	}()
	waitFor(t, 2*time.Second, func() bool { return adapter.profileCount() == 1 }, "profiling")
	// Profile runs well past the TTL; the heartbeat must keep the lease:
	// an intruder cannot take it mid-profile.
	time.Sleep(150 * time.Millisecond)
	recs, _ := store.ListQueryJobsByThread(context.Background(), "thread-1")
	if len(recs) != 1 {
		t.Fatalf("setup: %+v", recs)
	}
	if got, _ := store.AcquireMonitorLease(context.Background(), recs[0].ID, "intruder", time.Minute); got {
		t.Fatalf("lease expired mid-profile; the profile heartbeat must renew independently")
	}
	close(adapter.gate)
	if err := <-done; err != nil {
		t.Fatalf("launch: %v", err)
	}
	_ = r.RequestCancel(jobID, testOwner(), CancelReasonNoLongerNeeded)
}

// --- req 2+3: forced takeover => stale holder writes nothing ---

func TestProfileLeaseTakeoverRejectsStaleVerdict(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := newProfilingAdapter(true)
	adapter.gate = make(chan struct{}) // blocks until ctx cancellation
	r := newTestRegistry(t, RegistryOptions{Store: store, LeaseTTL: 40 * time.Millisecond, InstanceID: "proc-A"})
	r.RegisterAdapter(adapter)
	done := make(chan error, 1)
	go func() {
		_, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
		done <- err
	}()
	waitFor(t, 2*time.Second, func() bool { return adapter.profileCount() == 1 }, "profiling")
	recs, _ := store.ListQueryJobsByThread(context.Background(), "thread-1")
	jobID := recs[0].ID
	revBefore := recs[0].Revision

	// Forced takeover: steal the lease.
	_ = store.ReleaseMonitorLease(context.Background(), jobID, "proc-A")
	if got, _ := store.AcquireMonitorLease(context.Background(), jobID, "intruder", time.Minute); !got {
		t.Fatalf("intruder acquire failed")
	}
	err := <-done
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale holder must yield with ErrLeaseLost, got %v", err)
	}
	if adapter.submitCount() != 0 {
		t.Fatalf("stale holder must never submit")
	}
	// Zero verdict writes from the stale holder: the durable record is
	// still profiling and untouched past the dispatch marker.
	rec, _, _ := store.GetQueryJob(context.Background(), jobID)
	if rec.Status != JobStatusProfiling || rec.ProfileOutcome != ProfileOutcomeNone {
		t.Fatalf("stale holder wrote a verdict: %s/%s", rec.Status, rec.ProfileOutcome)
	}
	if rec.Revision != revBefore {
		t.Fatalf("stale holder mutated the record after takeover: rev %d -> %d", revBefore, rec.Revision)
	}
	// The fence itself rejects a direct stale write.
	out := ProfileOutcomeErrored
	if _, fErr := store.ApplyProfileMutation(context.Background(), jobID, "proc-A", rec.Revision, ProfileMutation{Outcome: &out}); !errors.Is(fErr, ErrLeaseLost) {
		t.Fatalf("fence must reject the stale holder: %v", fErr)
	}
}

// --- req 4: failed observer persistence aborts before Submit ---

func TestObserverPersistFailureAbortsBeforeSubmit(t *testing.T) {
	store := &fenceFailStore{MemoryJobStore: NewMemoryJobStore()}
	adapter := newProfilingAdapter(true)
	h := NativeHandle{Provider: "fake", ID: "prof-x"}
	adapter.publishHandle = &h // publication will fail; provider aborts
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if !errors.Is(err, ErrProfileRejected) {
		t.Fatalf("err=%v want ErrProfileRejected (provider aborted after failed publication)", err)
	}
	if adapter.submitCount() != 0 {
		t.Fatalf("failed durable publication must never reach Submit")
	}
	if job.Status != JobStatusFailed || job.ProfileOutcome != ProfileOutcomeErrored {
		t.Fatalf("job=%s/%s want failed/errored", job.Status, job.ProfileOutcome)
	}
	// The local job never believed the unpersisted handle existed.
	if !job.ProfileHandle.IsZero() {
		t.Fatalf("local state must not advance on failed publication: %+v", job.ProfileHandle)
	}
}

// --- req 6: ProfileDispatched crash branches ---

func TestRehydrateProfilingDispatchBranches(t *testing.T) {
	owner := testOwner()
	now := time.Now()
	seed := func(store *MemoryJobStore, id string, dispatched bool, handle NativeHandle) {
		_ = store.UpsertQueryJob(context.Background(), JobRecord{
			ID: id, Provider: "fake", Status: JobStatusProfiling,
			Owner: owner, SQL: "select 1",
			ProfileDispatched: dispatched, ProfileHandle: handle,
			SubmittedAt: now.Add(-time.Minute), LastAccess: now,
		})
	}

	// (a) Never dispatched: provably no remote profile work → expired,
	// visible terminal wake, zero cancel calls, zero reprofile.
	storeA := NewMemoryJobStore()
	seed(storeA, "qj-fake-pnd", false, NativeHandle{})
	adapterA := newProfilingAdapter(true)
	rA := newTestRegistry(t, RegistryOptions{Store: storeA})
	rA.RegisterAdapter(adapterA)
	statsA, err := rA.Rehydrate(context.Background())
	if err != nil || statsA.Expired != 1 {
		t.Fatalf("(a) stats=%+v err=%v want expired=1", statsA, err)
	}
	snapA, _ := rA.Snapshot("qj-fake-pnd", owner)
	if snapA.Status != JobStatusExpired || snapA.ProfileOutcome != ProfileOutcomeNone {
		t.Fatalf("(a) %s/%s want expired/none", snapA.Status, snapA.ProfileOutcome)
	}
	hasTerminalWake := false
	for _, ev := range snapA.WakeEvents {
		if ev.Kind == WakeTerminal {
			hasTerminalWake = true
		}
	}
	if !hasTerminalWake {
		t.Fatalf("(a) undispatched expiry must carry a visible terminal wake")
	}
	if adapterA.cancelCalls.Load() != 0 || adapterA.profileCount() != 0 {
		t.Fatalf("(a) cancel/profile calls: %d/%d want 0/0", adapterA.cancelCalls.Load(), adapterA.profileCount())
	}

	// (b) Dispatched + handle: cancel once, orphaned+indeterminate.
	storeB := NewMemoryJobStore()
	seed(storeB, "qj-fake-pdh", true, NativeHandle{Provider: "fake", ID: "prof-b"})
	adapterB := newProfilingAdapter(true)
	rB := newTestRegistry(t, RegistryOptions{Store: storeB})
	rB.RegisterAdapter(adapterB)
	statsB, err := rB.Rehydrate(context.Background())
	if err != nil || statsB.Orphaned != 1 {
		t.Fatalf("(b) stats=%+v err=%v want orphaned=1", statsB, err)
	}
	snapB, _ := rB.Snapshot("qj-fake-pdh", owner)
	if snapB.Status != JobStatusOrphaned || snapB.ProfileOutcome != ProfileOutcomeIndeterminate {
		t.Fatalf("(b) %s/%s want orphaned/indeterminate", snapB.Status, snapB.ProfileOutcome)
	}
	if adapterB.cancelCalls.Load() != 1 || adapterB.profileCount() != 0 {
		t.Fatalf("(b) cancel/profile calls: %d/%d want 1/0", adapterB.cancelCalls.Load(), adapterB.profileCount())
	}

	// (c) Dispatched + NO handle: ambiguous remote profile submit —
	// orphaned+indeterminate, no cancel possible, never reprofile.
	storeC := NewMemoryJobStore()
	seed(storeC, "qj-fake-pdn", true, NativeHandle{})
	adapterC := newProfilingAdapter(true)
	rC := newTestRegistry(t, RegistryOptions{Store: storeC})
	rC.RegisterAdapter(adapterC)
	statsC, err := rC.Rehydrate(context.Background())
	if err != nil || statsC.Orphaned != 1 {
		t.Fatalf("(c) stats=%+v err=%v want orphaned=1", statsC, err)
	}
	snapC, _ := rC.Snapshot("qj-fake-pdn", owner)
	if snapC.Status != JobStatusOrphaned || snapC.ProfileOutcome != ProfileOutcomeIndeterminate {
		t.Fatalf("(c) %s/%s want orphaned/indeterminate", snapC.Status, snapC.ProfileOutcome)
	}
	hasRecovery := false
	for _, ev := range snapC.WakeEvents {
		if ev.Kind == WakeRecoveryRequired {
			hasRecovery = true
		}
	}
	if !hasRecovery {
		t.Fatalf("(c) ambiguous profile must demand recovery")
	}
	if adapterC.cancelCalls.Load() != 0 || adapterC.profileCount() != 0 {
		t.Fatalf("(c) cancel/profile calls: %d/%d want 0/0", adapterC.cancelCalls.Load(), adapterC.profileCount())
	}

	// A lease held elsewhere defers with zero mutation.
	storeD := NewMemoryJobStore()
	seed(storeD, "qj-fake-pdl", true, NativeHandle{Provider: "fake", ID: "prof-d"})
	if got, _ := storeD.AcquireMonitorLease(context.Background(), "qj-fake-pdl", "other", time.Minute); !got {
		t.Fatalf("setup lease")
	}
	rD := newTestRegistry(t, RegistryOptions{Store: storeD, InstanceID: "proc-D"})
	rD.RegisterAdapter(newProfilingAdapter(true))
	statsD, err := rD.Rehydrate(context.Background())
	if err != nil || statsD.Deferred != 1 {
		t.Fatalf("(d) stats=%+v err=%v want deferred=1", statsD, err)
	}
	recD, _, _ := storeD.GetQueryJob(context.Background(), "qj-fake-pdl")
	if recD.Status != JobStatusProfiling {
		t.Fatalf("(d) deferral must not mutate: %s", recD.Status)
	}
}

// --- req 7: idempotency replay wins BEFORE full-capacity rejection ---

func TestIdempotencyReplayWinsBeforeCapacityRejection(t *testing.T) {
	base := NewMemoryJobStore()
	owner := JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_orig"}
	// The competitor with the same key occupies the ONLY capacity slot.
	_ = base.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-fake-capwin", Provider: "fake", Status: JobStatusRunning,
		Owner: owner, Handle: NativeHandle{Provider: "fake", ID: "n-cw"},
		SQL: "select 1", IdempotencyKey: "call-cap",
		SubmittedAt: time.Now().Add(-time.Second), LastAccess: time.Now(),
		SubmitDispatched: true,
	})
	// First pre-reserve lookup misses (race window); the reserve
	// transaction must STILL resolve the winner before rejecting on
	// capacity — otherwise this launch queues forever against a full slot.
	store := &raceStore{MemoryJobStore: base}
	adapter := runningAdapter()
	r := newTestRegistry(t, RegistryOptions{
		Store: store,
		Admission: &AdmissionConfig{
			MaxGlobal: 1, MaxPerThread: 1, MaxPerConnection: 1,
			CrossProcessRetryInterval: 20 * time.Millisecond,
		},
	})
	r.RegisterAdapter(adapter)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := idemRequest("call-cap")
	req.Owner = JobOwner{ThreadID: "thread-1", RunID: "run-1"}
	job, err := r.Launch(ctx, req)
	if err != nil {
		t.Fatalf("replay under full capacity must recover, not queue: %v", err)
	}
	if job.ID != "qj-fake-capwin" || job.Owner.Handle != "tok_orig" {
		t.Fatalf("must recover the original job+owner: %s/%q", job.ID, job.Owner.Handle)
	}
	if adapter.submitCount() != 0 {
		t.Fatalf("recovery must not submit")
	}
}

// --- fence unit semantics ---

func TestApplyProfileMutationFenceSemantics(t *testing.T) {
	store := NewMemoryJobStore()
	owner := testOwner()
	_ = store.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-fence", Provider: "fake", Status: JobStatusProfiling,
		Owner: owner, SubmittedAt: time.Now(), LastAccess: time.Now(),
	})
	rec, _, _ := store.GetQueryJob(context.Background(), "qj-fence")
	if got, _ := store.AcquireMonitorLease(context.Background(), "qj-fence", "holder-1", time.Minute); !got {
		t.Fatalf("lease setup")
	}
	phase := "explain"
	// Wrong holder → ErrLeaseLost.
	if _, err := store.ApplyProfileMutation(context.Background(), "qj-fence", "holder-2", rec.Revision, ProfileMutation{Phase: &phase}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("wrong holder: %v want ErrLeaseLost", err)
	}
	// Stale revision → ErrRevisionConflict.
	if _, err := store.ApplyProfileMutation(context.Background(), "qj-fence", "holder-1", rec.Revision+7, ProfileMutation{Phase: &phase}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale revision: %v want ErrRevisionConflict", err)
	}
	// Correct fence → applied, revision bumped.
	res, err := store.ApplyProfileMutation(context.Background(), "qj-fence", "holder-1", rec.Revision, ProfileMutation{Phase: &phase})
	if err != nil || res.NewRevision != rec.Revision+1 {
		t.Fatalf("fenced apply: rev=%d err=%v", res.NewRevision, err)
	}
	if res.CompletedAt != nil || res.CapacityReleased {
		t.Fatalf("non-terminal mutation must not stamp terminal effects: %+v", res)
	}
	// Illegal transition → ErrRevisionConflict, nothing applied.
	running := JobStatusRunning
	if _, err := store.ApplyProfileMutation(context.Background(), "qj-fence", "holder-1", res.NewRevision, ProfileMutation{TransitionTo: &running}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("illegal transition: %v want ErrRevisionConflict", err)
	}
	// Terminal transition: CompletedAt stamped by store time, wakes
	// deduped by kind, capacity released — all in one result.
	failed := JobStatusFailed
	res2, err := store.ApplyProfileMutation(context.Background(), "qj-fence", "holder-1", res.NewRevision, ProfileMutation{
		TransitionTo: &failed,
		Wakes:        []WakeSpec{{Kind: WakeTerminal, Detail: "d"}, {Kind: WakeTerminal, Detail: "dup"}},
	})
	if err != nil {
		t.Fatalf("legal terminal transition: %v", err)
	}
	if res2.CompletedAt == nil || !res2.CapacityReleased || len(res2.AppendedWakes) != 1 {
		t.Fatalf("terminal result missing atomic side effects: %+v", res2)
	}
	recAfter, _, _ := store.GetQueryJob(context.Background(), "qj-fence")
	if recAfter.CompletedAt == nil || len(recAfter.WakeEvents) != 1 {
		t.Fatalf("terminal record incomplete after single fenced write: %+v", recAfter)
	}
	// Status no longer profiling → later mutations fence out.
	if _, err := store.ApplyProfileMutation(context.Background(), "qj-fence", "holder-1", res2.NewRevision, ProfileMutation{Phase: &phase}); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("post-terminal mutation: %v want ErrRevisionConflict", err)
	}
}
