package jobs

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestScanApprovalAuthorizationBytes(t *testing.T) {
	tests := []struct {
		name      string
		estimated int64
		ceiling   int64
		want      int64
	}{
		{name: "five percent rounded up", estimated: 250, want: 263},
		{name: "exact five percent", estimated: 20, want: 21},
		{
			name:      "reported production drift has ample allowance",
			estimated: 213_534_213_421,
			want:      224_210_924_093,
		},
		{name: "hard ceiling clamps allowance", estimated: 490, ceiling: 500, want: 500},
		{name: "overflow saturates", estimated: math.MaxInt64 - 1, want: math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scanApprovalAuthorizationBytes(tt.estimated, tt.ceiling); got != tt.want {
				t.Fatalf("scanApprovalAuthorizationBytes(%d, %d)=%d want %d", tt.estimated, tt.ceiling, got, tt.want)
			}
		})
	}
}

// profilingAdapter adds ObservableQueryProfiler + ProfileCanceller to the
// fake adapter, with hooks for gating, publications, and verdicts.
type profilingAdapter struct {
	*fakeAdapter
	pmu            sync.Mutex
	profileCalls   int
	concurrent     atomic.Int32
	maxConcurrent  atomic.Int32
	gate           chan struct{} // when non-nil, profile blocks here (or ctx)
	publishHandle  *NativeHandle
	publishPhase   string
	profileResult  QueryProfile
	profileErr     error
	cancelCalls    atomic.Int32
	onProfileStart func() // runs inside Profile before gating
}

func (a *profilingAdapter) ProfileWithObserver(ctx context.Context, _ SubmitRequest, obs ProfileObserver) (QueryProfile, error) {
	a.pmu.Lock()
	a.profileCalls++
	gate := a.gate
	ph := a.publishHandle
	phase := a.publishPhase
	res := a.profileResult
	perr := a.profileErr
	start := a.onProfileStart
	a.pmu.Unlock()

	cur := a.concurrent.Add(1)
	for {
		max := a.maxConcurrent.Load()
		if cur <= max || a.maxConcurrent.CompareAndSwap(max, cur) {
			break
		}
	}
	defer a.concurrent.Add(-1)

	// Contract: a failed durable publication means abort + clean up.
	if ph != nil {
		if err := obs.PublishProfileHandle(*ph); err != nil {
			return QueryProfile{}, fmt.Errorf("aborting profile after failed handle publication: %w", err)
		}
	}
	if phase != "" {
		if err := obs.PublishProfilePhase(phase); err != nil {
			return QueryProfile{}, fmt.Errorf("aborting profile after failed phase publication: %w", err)
		}
	}
	if start != nil {
		start()
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return QueryProfile{}, ctx.Err()
		}
	}
	if perr != nil {
		return QueryProfile{}, perr
	}
	return res, nil
}

func (a *profilingAdapter) CancelProfile(context.Context, NativeHandle) error {
	a.cancelCalls.Add(1)
	return nil
}

func (a *profilingAdapter) profileCount() int {
	a.pmu.Lock()
	defer a.pmu.Unlock()
	return a.profileCalls
}

func newProfilingAdapter(valid bool) *profilingAdapter {
	bytes := int64(50)
	return &profilingAdapter{
		fakeAdapter: &fakeAdapter{
			name: "fake",
			caps: ProviderCapabilities{CancelConfirmable: true, NativePagination: true},
			pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
				return StatusSnapshot{State: ProviderStateRunning}, nil
			},
			fetchFn: func(int, FetchCursor) (ResultPage, error) { return ResultPage{}, nil },
		},
		profileResult: QueryProfile{Valid: valid, EstimatedBytes: &bytes, EstimateIsExact: true},
	}
}

// --- req 1+2+5+7: ordering, durability, persistence of the profile ---

func TestProfileRunsAfterDurableReservationAndBeforeSubmit(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := newProfilingAdapter(true)
	var statusDuringProfile JobStatus
	var order []string
	var omu sync.Mutex
	adapter.onProfileStart = func() {
		// The durable profiling reservation must already exist when the
		// provider profile call begins.
		recs, _ := store.ListQueryJobsByThread(context.Background(), "thread-1")
		if len(recs) == 1 {
			statusDuringProfile = recs[0].Status
		}
		omu.Lock()
		order = append(order, "profile")
		omu.Unlock()
	}
	adapter.submitFn = func(int, SubmitRequest) (SubmitResult, error) {
		omu.Lock()
		order = append(order, "submit")
		omu.Unlock()
		return SubmitResult{Handle: NativeHandle{Provider: "fake", ID: "n1"}}, nil
	}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if statusDuringProfile != JobStatusProfiling {
		t.Fatalf("profile ran without a durable profiling reservation; status=%s", statusDuringProfile)
	}
	omu.Lock()
	got := strings.Join(order, ",")
	omu.Unlock()
	if got != "profile,submit" {
		t.Fatalf("order=%q want profile,submit", got)
	}
	// Accepted profile persisted before submission.
	rec, _, _ := store.GetQueryJob(context.Background(), job.ID)
	if rec.Profile == nil || rec.ProfileOutcome != ProfileOutcomeAccepted || !rec.Profile.Valid {
		t.Fatalf("accepted profile not durable: %+v / %s", rec.Profile, rec.ProfileOutcome)
	}
	_ = r.RequestCancel(job.ID, testOwner(), CancelReasonNoLongerNeeded)
}

// --- req 5: invalid / over-budget / timeout / errored never submit ---

func TestProfileRejectionsNeverSubmit(t *testing.T) {
	over := int64(1000)
	cases := []struct {
		name    string
		mutate  func(*profilingAdapter, *RegistryOptions, *LaunchRequest)
		outcome ProfileOutcome
	}{
		{"invalid", func(a *profilingAdapter, _ *RegistryOptions, _ *LaunchRequest) {
			a.profileResult = QueryProfile{Valid: false, Error: "syntax error near FROM"}
		}, ProfileOutcomeInvalid},
		{"over_budget", func(a *profilingAdapter, _ *RegistryOptions, q *LaunchRequest) {
			a.profileResult = QueryProfile{Valid: true, EstimatedBytes: &over}
			q.Budgets.MaxScanBytes = 100
		}, ProfileOutcomeOverBudget},
		{"timed_out", func(a *profilingAdapter, o *RegistryOptions, _ *LaunchRequest) {
			a.gate = make(chan struct{}) // never opens
			o.Profile = &ProfilePolicy{Timeout: 30 * time.Millisecond}
		}, ProfileOutcomeTimedOut},
		{"errored", func(a *profilingAdapter, _ *RegistryOptions, _ *LaunchRequest) {
			a.profileErr = errors.New("warehouse unreachable")
		}, ProfileOutcomeErrored},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewMemoryJobStore()
			adapter := newProfilingAdapter(true)
			h := NativeHandle{Provider: "fake", ID: "prof-1"}
			adapter.publishHandle = &h
			opts := RegistryOptions{Store: store}
			req := LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"}
			tc.mutate(adapter, &opts, &req)
			if opts.Scheduler == nil {
				opts.Scheduler = fastScheduler()
			}
			r := NewRegistry(opts)
			r.RegisterAdapter(adapter)

			job, err := r.Launch(context.Background(), req)
			if !errors.Is(err, ErrProfileRejected) {
				t.Fatalf("err=%v want ErrProfileRejected", err)
			}
			if !strings.Contains(err.Error(), string(tc.outcome)) {
				t.Fatalf("error must name the outcome %q: %v", tc.outcome, err)
			}
			if adapter.submitCount() != 0 {
				t.Fatalf("Submit must never run after a rejected profile; submits=%d", adapter.submitCount())
			}
			if job.Status != JobStatusFailed || job.ProfileOutcome != tc.outcome {
				t.Fatalf("job=%s/%s want failed/%s", job.Status, job.ProfileOutcome, tc.outcome)
			}
			hasRecovery := false
			for _, ev := range job.WakeEvents {
				if ev.Kind == WakeRecoveryRequired {
					hasRecovery = true
				}
			}
			if !hasRecovery {
				t.Fatalf("rejected profile must emit the recovery/alternative-plan wake")
			}
			// Best-effort cancel of the published profile handle on
			// timeout/error paths.
			if tc.name == "timed_out" && adapter.cancelCalls.Load() == 0 {
				t.Fatalf("timed-out profile must request best-effort profile cancellation")
			}
			// Capacity released: the next launch admits immediately.
			ok := newProfilingAdapter(true)
			r.RegisterAdapter(ok)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			next, nErr := r.Launch(ctx, LaunchRequest{Owner: JobOwner{ThreadID: "thread-1", Handle: "tok_next"}, Provider: "fake", SQL: "select 2"})
			if nErr != nil {
				t.Fatalf("capacity leaked after profile rejection: %v", nErr)
			}
			_ = r.RequestCancel(next.ID, JobOwner{ThreadID: "thread-1", Handle: "tok_next"}, CancelReasonNoLongerNeeded)
		})
	}
}

func TestProfileScanBudgetUsesOnlyTheAuthorizedProviderCap(t *testing.T) {
	t.Run("automatic", func(t *testing.T) {
		adapter := newProfilingAdapter(true)
		estimated := int64(80)
		adapter.profileResult.EstimatedBytes = &estimated
		var submitted SubmitRequest
		adapter.submitFn = func(_ int, req SubmitRequest) (SubmitResult, error) {
			submitted = req
			return SubmitResult{Handle: NativeHandle{Provider: "fake", ID: "automatic"}}, nil
		}
		approvalCalls := 0
		r := newTestRegistry(t, RegistryOptions{})
		r.RegisterAdapter(adapter)
		job, err := r.Launch(context.Background(), LaunchRequest{
			Owner:    testOwner(),
			Provider: "fake",
			SQL:      "select automatic",
			Budgets: Budgets{
				AutomaticScanBytes: 100,
				MaxScanBytes:       500,
			},
			ScanApproval: func(context.Context, ScanApprovalRequest) (ScanApprovalDecision, error) {
				approvalCalls++
				return ScanApprovalDecision{Approved: true}, nil
			},
		})
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		if approvalCalls != 0 {
			t.Fatalf("automatic query requested approval %d times", approvalCalls)
		}
		if submitted.Budgets.MaxScanBytes != 100 {
			t.Fatalf("provider cap=%d want automatic cap 100", submitted.Budgets.MaxScanBytes)
		}
		if got := job.Profile.ProviderDetails["authorized_scan_bytes"]; got != int64(100) {
			t.Fatalf("authorized profile detail=%v want 100", got)
		}
		_ = r.RequestCancel(job.ID, testOwner(), CancelReasonNoLongerNeeded)
	})

	t.Run("approved", func(t *testing.T) {
		adapter := newProfilingAdapter(true)
		estimated := int64(250)
		adapter.profileResult.EstimatedBytes = &estimated
		var submitted SubmitRequest
		adapter.submitFn = func(_ int, req SubmitRequest) (SubmitResult, error) {
			submitted = req
			return SubmitResult{Handle: NativeHandle{Provider: "fake", ID: "approved"}}, nil
		}
		approvalCalls := 0
		r := newTestRegistry(t, RegistryOptions{})
		r.RegisterAdapter(adapter)
		job, err := r.Launch(context.Background(), LaunchRequest{
			Owner:        testOwner(),
			Provider:     "fake",
			ConnectionID: "project",
			SQL:          "select approved",
			SQLPreview:   "select approved",
			Budgets: Budgets{
				AutomaticScanBytes: 100,
				MaxScanBytes:       500,
			},
			ScanApproval: func(_ context.Context, req ScanApprovalRequest) (ScanApprovalDecision, error) {
				approvalCalls++
				if req.JobID == "" || req.ConnectionID != "project" || req.EstimatedBytes != 250 ||
					req.AutomaticScanBytes != 100 || req.ApprovedScanBytes != 500 ||
					req.AuthorizedScanBytes != 263 {
					t.Fatalf("approval request=%+v", req)
				}
				return ScanApprovalDecision{Approved: true, ApprovalID: "apr_test"}, nil
			},
		})
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		if approvalCalls != 1 {
			t.Fatalf("approval calls=%d want 1", approvalCalls)
		}
		if submitted.Budgets.MaxScanBytes != 263 {
			t.Fatalf("provider cap=%d want approved estimate plus headroom 263", submitted.Budgets.MaxScanBytes)
		}
		if got := job.Profile.ProviderDetails["scan_budget_approval_id"]; got != "apr_test" {
			t.Fatalf("approval id=%v want apr_test", got)
		}
		_ = r.RequestCancel(job.ID, testOwner(), CancelReasonNoLongerNeeded)
	})

	t.Run("approval wait is not profile RPC time", func(t *testing.T) {
		adapter := newProfilingAdapter(true)
		estimated := int64(250)
		adapter.profileResult.EstimatedBytes = &estimated
		r := newTestRegistry(t, RegistryOptions{
			Profile: &ProfilePolicy{Timeout: 20 * time.Millisecond},
		})
		r.RegisterAdapter(adapter)
		started := time.Now()
		job, err := r.Launch(context.Background(), LaunchRequest{
			Owner:    testOwner(),
			Provider: "fake",
			SQL:      "select patient_approval",
			Budgets: Budgets{
				AutomaticScanBytes: 100,
				MaxScanBytes:       500,
			},
			ScanApproval: func(context.Context, ScanApprovalRequest) (ScanApprovalDecision, error) {
				time.Sleep(60 * time.Millisecond)
				return ScanApprovalDecision{Approved: true, ApprovalID: "apr_patient"}, nil
			},
		})
		if err != nil {
			t.Fatalf("launch: %v", err)
		}
		if elapsed := time.Since(started); elapsed < 50*time.Millisecond {
			t.Fatalf("approval did not wait independently: %s", elapsed)
		}
		if job.ProfileOutcome != ProfileOutcomeAccepted || adapter.submitCount() != 1 {
			t.Fatalf("job=%s/%s submits=%d", job.Status, job.ProfileOutcome, adapter.submitCount())
		}
		_ = r.RequestCancel(job.ID, testOwner(), CancelReasonNoLongerNeeded)
	})
}

// Regression: a missing approved ceiling (MaxScanBytes == 0) means "no
// ceiling — ask first", never "ceiling equals the automatic limit — reject as
// over_budget". This is the default BigQuery configuration: only the 100 GiB
// automatic limit is in force, so an oversized estimate must surface an
// approval and, once approved, submit that query with bounded estimate
// headroom.
func TestProfileScanApprovalWithoutConfiguredCeiling(t *testing.T) {
	adapter := newProfilingAdapter(true)
	estimated := int64(250)
	adapter.profileResult.EstimatedBytes = &estimated
	var submitted SubmitRequest
	adapter.submitFn = func(_ int, req SubmitRequest) (SubmitResult, error) {
		submitted = req
		return SubmitResult{Handle: NativeHandle{Provider: "fake", ID: "no-ceiling"}}, nil
	}
	approvalCalls := 0
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner:    testOwner(),
		Provider: "fake",
		SQL:      "select no_ceiling",
		Budgets:  Budgets{AutomaticScanBytes: 100},
		ScanApproval: func(_ context.Context, req ScanApprovalRequest) (ScanApprovalDecision, error) {
			approvalCalls++
			if req.EstimatedBytes != 250 || req.AutomaticScanBytes != 100 ||
				req.ApprovedScanBytes != 0 || req.AuthorizedScanBytes != 263 {
				t.Fatalf("approval request=%+v", req)
			}
			return ScanApprovalDecision{Approved: true, ApprovalID: "apr_no_ceiling"}, nil
		},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if approvalCalls != 1 {
		t.Fatalf("approval calls=%d want 1", approvalCalls)
	}
	if job.ProfileOutcome != ProfileOutcomeAccepted || adapter.submitCount() != 1 {
		t.Fatalf("job=%s/%s submits=%d", job.Status, job.ProfileOutcome, adapter.submitCount())
	}
	if submitted.Budgets.MaxScanBytes != 263 {
		t.Fatalf("provider cap=%d want approved estimate plus headroom 263", submitted.Budgets.MaxScanBytes)
	}
	if got := job.Profile.ProviderDetails["scan_budget_approval_id"]; got != "apr_no_ceiling" {
		t.Fatalf("approval id=%v want apr_no_ceiling", got)
	}
	_ = r.RequestCancel(job.ID, testOwner(), CancelReasonNoLongerNeeded)
}

func TestProfileScanApprovalHeadroomRespectsConfiguredCeiling(t *testing.T) {
	adapter := newProfilingAdapter(true)
	estimated := int64(490)
	adapter.profileResult.EstimatedBytes = &estimated
	var submitted SubmitRequest
	adapter.submitFn = func(_ int, req SubmitRequest) (SubmitResult, error) {
		submitted = req
		return SubmitResult{Handle: NativeHandle{Provider: "fake", ID: "ceiling-clamped"}}, nil
	}
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner:    testOwner(),
		Provider: "fake",
		SQL:      "select ceiling_clamped",
		Budgets:  Budgets{AutomaticScanBytes: 100, MaxScanBytes: 500},
		ScanApproval: func(_ context.Context, req ScanApprovalRequest) (ScanApprovalDecision, error) {
			if req.AuthorizedScanBytes != 500 {
				t.Fatalf("authorized cap=%d want hard ceiling 500", req.AuthorizedScanBytes)
			}
			return ScanApprovalDecision{Approved: true, ApprovalID: "apr_ceiling_clamped"}, nil
		},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if submitted.Budgets.MaxScanBytes != 500 {
		t.Fatalf("provider cap=%d want hard ceiling 500", submitted.Budgets.MaxScanBytes)
	}
	if got := job.Profile.ProviderDetails["scan_budget_headroom_bytes"]; got != int64(10) {
		t.Fatalf("headroom=%v want 10", got)
	}
	_ = r.RequestCancel(job.ID, testOwner(), CancelReasonNoLongerNeeded)
}

// A configured ceiling stays a hard cap: estimates above it are rejected as
// over_budget without ever asking the user.
func TestProfileScanCeilingBlocksAboveItWithoutAsking(t *testing.T) {
	adapter := newProfilingAdapter(true)
	estimated := int64(900)
	adapter.profileResult.EstimatedBytes = &estimated
	approvalCalls := 0
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner:    testOwner(),
		Provider: "fake",
		SQL:      "select blocked",
		Budgets:  Budgets{AutomaticScanBytes: 100, MaxScanBytes: 500},
		ScanApproval: func(context.Context, ScanApprovalRequest) (ScanApprovalDecision, error) {
			approvalCalls++
			return ScanApprovalDecision{Approved: true}, nil
		},
	})
	if !errors.Is(err, ErrProfileRejected) {
		t.Fatalf("err=%v want ErrProfileRejected", err)
	}
	if approvalCalls != 0 {
		t.Fatalf("over-ceiling estimate must not ask; approval calls=%d", approvalCalls)
	}
	if job.ProfileOutcome != ProfileOutcomeOverBudget || adapter.submitCount() != 0 {
		t.Fatalf("job=%s/%s submits=%d", job.Status, job.ProfileOutcome, adapter.submitCount())
	}
}

func TestProfileScanBudgetDenialNeverSubmits(t *testing.T) {
	adapter := newProfilingAdapter(true)
	estimated := int64(250)
	adapter.profileResult.EstimatedBytes = &estimated
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner:    testOwner(),
		Provider: "fake",
		SQL:      "select denied",
		Budgets: Budgets{
			AutomaticScanBytes: 100,
			MaxScanBytes:       500,
		},
		ScanApproval: func(context.Context, ScanApprovalRequest) (ScanApprovalDecision, error) {
			return ScanApprovalDecision{ApprovalID: "apr_denied"}, nil
		},
	})
	if !errors.Is(err, ErrProfileRejected) {
		t.Fatalf("err=%v want ErrProfileRejected", err)
	}
	if adapter.submitCount() != 0 {
		t.Fatalf("denied query submitted %d times", adapter.submitCount())
	}
	if job.Status != JobStatusFailed || job.ProfileOutcome != ProfileOutcomeApprovalDenied {
		t.Fatalf("job=%s/%s want failed/%s", job.Status, job.ProfileOutcome, ProfileOutcomeApprovalDenied)
	}
}

func TestCancelWhileAwaitingScanApprovalNeverSubmits(t *testing.T) {
	adapter := newProfilingAdapter(true)
	estimated := int64(250)
	adapter.profileResult.EstimatedBytes = &estimated
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	approvalStarted := make(chan string, 1)
	done := make(chan struct {
		job *Job
		err error
	}, 1)
	go func() {
		job, err := r.Launch(context.Background(), LaunchRequest{
			Owner:    testOwner(),
			Provider: "fake",
			SQL:      "select cancel_approval",
			Budgets: Budgets{
				AutomaticScanBytes: 100,
				MaxScanBytes:       500,
			},
			ScanApproval: func(ctx context.Context, req ScanApprovalRequest) (ScanApprovalDecision, error) {
				approvalStarted <- req.JobID
				<-ctx.Done()
				return ScanApprovalDecision{}, ctx.Err()
			},
		})
		done <- struct {
			job *Job
			err error
		}{job: job, err: err}
	}()
	var jobID string
	select {
	case jobID = <-approvalStarted:
	case <-time.After(time.Second):
		t.Fatal("approval callback did not start")
	}
	if err := r.RequestCancel(jobID, testOwner(), CancelReasonUserRequest); err != nil {
		t.Fatalf("RequestCancel: %v", err)
	}
	select {
	case result := <-done:
		if !errors.Is(result.err, context.Canceled) {
			t.Fatalf("err=%v want context cancellation", result.err)
		}
		if result.job.Status != JobStatusCancelled || result.job.CancelOutcome != CancelOutcomeBeforeSubmit {
			t.Fatalf("job=%s/%s want cancelled/before_submit", result.job.Status, result.job.CancelOutcome)
		}
	case <-time.After(time.Second):
		t.Fatal("approval wait did not cancel")
	}
	if adapter.submitCount() != 0 {
		t.Fatalf("cancelled approval submitted %d times", adapter.submitCount())
	}
}

// --- req 8: replay in profile states, profile count exactly 1 ---

func TestProfileReplayProfilesExactlyOnce(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := newProfilingAdapter(true)
	r := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "proc-A"})
	r.RegisterAdapter(adapter)
	req := LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1", IdempotencyKey: "call-p"}
	job1, err := r.Launch(context.Background(), req)
	if err != nil {
		t.Fatalf("launch 1: %v", err)
	}
	job2, err := r.Launch(context.Background(), req)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if job2.ID != job1.ID || adapter.profileCount() != 1 || adapter.submitCount() != 1 {
		t.Fatalf("same-process replay: id %s/%s profiles=%d submits=%d want same/1/1",
			job1.ID, job2.ID, adapter.profileCount(), adapter.submitCount())
	}

	// Fresh process: neither profiles nor submits.
	adapterB := newProfilingAdapter(true)
	rB := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "proc-B"})
	rB.RegisterAdapter(adapterB)
	reqB := req
	reqB.Owner = JobOwner{ThreadID: "thread-1", RunID: "run-1"} // no token
	job3, err := rB.Launch(context.Background(), reqB)
	if err != nil {
		t.Fatalf("fresh-process replay: %v", err)
	}
	if job3.ID != job1.ID || job3.Owner.Handle != job1.Owner.Handle {
		t.Fatalf("fresh-process replay must return original job+owner")
	}
	if adapterB.profileCount() != 0 || adapterB.submitCount() != 0 {
		t.Fatalf("fresh-process replay profiled/submitted: %d/%d", adapterB.profileCount(), adapterB.submitCount())
	}

	// Replay of a profile-FAILED job returns the typed rejection, still
	// without re-profiling.
	storeF := NewMemoryJobStore()
	adapterF := newProfilingAdapter(true)
	adapterF.profileResult = QueryProfile{Valid: false, Error: "bad sql"}
	rF := newTestRegistry(t, RegistryOptions{Store: storeF})
	rF.RegisterAdapter(adapterF)
	reqF := LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select bad", IdempotencyKey: "call-pf"}
	if _, err := rF.Launch(context.Background(), reqF); !errors.Is(err, ErrProfileRejected) {
		t.Fatalf("setup: %v", err)
	}
	calls := adapterF.profileCount()
	jobF, err := rF.Launch(context.Background(), reqF)
	if !errors.Is(err, ErrProfileRejected) {
		t.Fatalf("failed-profile replay err=%v want typed ErrProfileRejected", err)
	}
	if jobF.ProfileOutcome != ProfileOutcomeInvalid || adapterF.profileCount() != calls {
		t.Fatalf("failed-profile replay must not re-profile: outcome=%s calls=%d", jobF.ProfileOutcome, adapterF.profileCount())
	}
	_ = r.RequestCancel(job1.ID, testOwner(), CancelReasonNoLongerNeeded)
}

// --- req 6: observer durability + crash rehydrate cancels the handle ---

func TestProfileObserverHandleDurableAndRehydrateCancels(t *testing.T) {
	// (a) The published handle is durable WHILE profiling is in flight.
	store := NewMemoryJobStore()
	adapter := newProfilingAdapter(true)
	h := NativeHandle{Provider: "fake", ID: "prof-live", Extra: map[string]string{"cursor": "c1"}}
	adapter.publishHandle = &h
	adapter.publishPhase = "explain_io"
	adapter.gate = make(chan struct{})
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	done := make(chan error, 1)
	go func() {
		_, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
		done <- err
	}()
	waitFor(t, 2*time.Second, func() bool {
		recs, _ := store.ListQueryJobsByThread(context.Background(), "thread-1")
		return len(recs) == 1 && recs[0].Status == JobStatusProfiling &&
			recs[0].ProfileHandle.ID == "prof-live" && recs[0].ProfilePhase == "explain_io"
	}, "profile handle+phase durable mid-profile")
	close(adapter.gate)
	if err := <-done; err != nil {
		t.Fatalf("launch after gate: %v", err)
	}

	// (b) Crash simulation: a persisted profiling record rehydrates as
	// indeterminate, the handle is cancelled, Profile is never re-run.
	store2 := NewMemoryJobStore()
	owner := testOwner()
	_ = store2.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-fake-pcrash", Provider: "fake", Status: JobStatusProfiling,
		Owner: owner, SQL: "select 1",
		ProfileDispatched: true,
		ProfileHandle:     NativeHandle{Provider: "fake", ID: "prof-dead"},
		ProfilePhase:      "explain_io",
		SubmittedAt:       time.Now().Add(-time.Minute), LastAccess: time.Now(),
	})
	adapter2 := newProfilingAdapter(true)
	r2 := newTestRegistry(t, RegistryOptions{Store: store2, InstanceID: "proc-2"})
	r2.RegisterAdapter(adapter2)
	stats, err := r2.Rehydrate(context.Background())
	if err != nil || stats.Orphaned != 1 {
		t.Fatalf("rehydrate: stats=%+v err=%v want orphaned=1", stats, err)
	}
	snap, ok := r2.Snapshot("qj-fake-pcrash", owner)
	if !ok || snap.Status != JobStatusOrphaned || snap.ProfileOutcome != ProfileOutcomeIndeterminate {
		t.Fatalf("interrupted profile: %+v want orphaned/indeterminate", snap)
	}
	hasRecovery := false
	for _, ev := range snap.WakeEvents {
		if ev.Kind == WakeRecoveryRequired {
			hasRecovery = true
		}
	}
	if !hasRecovery {
		t.Fatalf("interrupted profile must emit recovery_required")
	}
	if adapter2.cancelCalls.Load() != 1 {
		t.Fatalf("persisted profile handle must be cancelled once; calls=%d", adapter2.cancelCalls.Load())
	}
	if adapter2.profileCount() != 0 {
		t.Fatalf("rehydrate must NEVER re-run Profile; calls=%d", adapter2.profileCount())
	}
}

// --- req 2+3: same slot end to end; nested + combined admission ---

func TestProfileHoldsSameSlotAndNestedLimits(t *testing.T) {
	store := NewMemoryJobStore()
	gateA := make(chan struct{})
	adapter := newProfilingAdapter(true)
	adapter.gate = gateA
	r := newTestRegistry(t, RegistryOptions{
		Store: store,
		Admission: &AdmissionConfig{
			MaxGlobal: 4, MaxPerThread: 4, MaxPerConnection: 4,
			MaxProfilingGlobal: 4, MaxProfilingPerThread: 1, MaxProfilingPerConnection: 4,
			CrossProcessRetryInterval: 20 * time.Millisecond,
		},
	})
	r.RegisterAdapter(adapter)

	aDone := make(chan error, 1)
	var jobAID atomic.Value
	go func() {
		job, err := r.Launch(context.Background(), LaunchRequest{Owner: JobOwner{ThreadID: "thread-1", Handle: "tok_a"}, Provider: "fake", SQL: "select A"})
		if job != nil {
			jobAID.Store(job.ID)
		}
		aDone <- err
	}()
	waitFor(t, 2*time.Second, func() bool { return adapter.profileCount() == 1 }, "A profiling")

	// B (same thread) must queue on the NESTED profiling limit even though
	// overall capacity is free.
	bDone := make(chan error, 1)
	go func() {
		_, err := r.Launch(context.Background(), LaunchRequest{Owner: JobOwner{ThreadID: "thread-1", Handle: "tok_b"}, Provider: "fake", SQL: "select B"})
		bDone <- err
	}()
	time.Sleep(60 * time.Millisecond)
	select {
	case err := <-bDone:
		t.Fatalf("B admitted while A held the thread's only profiling slot: %v", err)
	default:
	}
	if adapter.maxConcurrent.Load() > 1 {
		t.Fatalf("nested limit violated: %d concurrent profiles", adapter.maxConcurrent.Load())
	}

	// A's profile completes: A moves profiling -> reserved -> submitted on
	// the SAME overall slot (never released), freeing only the nested
	// profiling slot — B may now profile.
	close(gateA)
	if err := <-aDone; err != nil {
		t.Fatalf("A: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool { return adapter.profileCount() == 2 }, "B profiling after A's nested slot freed")
	if err := <-bDone; err != nil {
		t.Fatalf("B: %v", err)
	}
	if adapter.maxConcurrent.Load() > 1 {
		t.Fatalf("profiles overlapped: %d", adapter.maxConcurrent.Load())
	}
	// Combined accounting sanity: two active jobs total, within limits.
	recs, _ := store.ListQueryJobsByThread(context.Background(), "thread-1")
	active := 0
	for _, rec := range recs {
		if isActiveStatus(rec.Status) {
			active++
		}
	}
	if active != 2 {
		t.Fatalf("active=%d want 2 (profiling+executing shared the existing capacity)", active)
	}
	_ = r.RequestCancel(jobAID.Load().(string), JobOwner{ThreadID: "thread-1", Handle: "tok_a"}, CancelReasonNoLongerNeeded)
}

func TestProfileSlotNeverReleasedBetweenPhases(t *testing.T) {
	store := NewMemoryJobStore()
	gate := make(chan struct{})
	adapter := newProfilingAdapter(true)
	adapter.gate = gate
	r := newTestRegistry(t, RegistryOptions{
		Store: store,
		Admission: &AdmissionConfig{
			MaxGlobal: 1, MaxPerThread: 1, MaxPerConnection: 1,
			MaxProfilingGlobal: 1, MaxProfilingPerThread: 1, MaxProfilingPerConnection: 1,
			CrossProcessRetryInterval: 15 * time.Millisecond,
		},
	})
	r.RegisterAdapter(adapter)
	ownerA := JobOwner{ThreadID: "thread-1", Handle: "tok_a"}
	aDone := make(chan struct {
		job *Job
		err error
	}, 1)
	go func() {
		job, err := r.Launch(context.Background(), LaunchRequest{Owner: ownerA, Provider: "fake", SQL: "select A"})
		aDone <- struct {
			job *Job
			err error
		}{job, err}
	}()
	waitFor(t, 2*time.Second, func() bool { return adapter.profileCount() == 1 }, "A profiling")

	// Competitor over the single GLOBAL slot: if the slot were released
	// and reacquired between profile acceptance and submit, B would slip
	// in. B must stay queued until A is cancelled.
	bDone := make(chan error, 1)
	go func() {
		_, err := r.Launch(context.Background(), LaunchRequest{Owner: JobOwner{ThreadID: "thread-2", Handle: "tok_b"}, Provider: "fake", SQL: "select B"})
		bDone <- err
	}()
	time.Sleep(50 * time.Millisecond)
	close(gate) // A: profiling -> reserved -> submitted -> running
	a := <-aDone
	if a.err != nil {
		t.Fatalf("A: %v", a.err)
	}
	time.Sleep(80 * time.Millisecond) // several cross-process retry ticks
	select {
	case err := <-bDone:
		t.Fatalf("slot leaked between profile and submit: B admitted (%v) while A still active", err)
	default:
	}
	if err := r.RequestCancel(a.job.ID, ownerA, CancelReasonNoLongerNeeded); err != nil {
		t.Fatalf("cancel A: %v", err)
	}
	select {
	case err := <-bDone:
		if err != nil {
			t.Fatalf("B after release: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("B never admitted after A terminal")
	}
}

// --- cancel during profile ---

func TestCancelDuringProfileTerminalizesBeforeSubmit(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := newProfilingAdapter(true)
	h := NativeHandle{Provider: "fake", ID: "prof-c"}
	adapter.publishHandle = &h
	adapter.gate = make(chan struct{}) // never opened; cancel unblocks via ctx
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	done := make(chan struct {
		job *Job
		err error
	}, 1)
	go func() {
		job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1", IdempotencyKey: "call-c"})
		done <- struct {
			job *Job
			err error
		}{job, err}
	}()
	var jobID string
	waitFor(t, 2*time.Second, func() bool {
		recs, _ := store.ListQueryJobsByThread(context.Background(), "thread-1")
		if len(recs) == 1 && recs[0].Status == JobStatusProfiling {
			jobID = recs[0].ID
			return true
		}
		return false
	}, "profiling visible")
	if err := r.RequestCancel(jobID, testOwner(), CancelReasonUserRequest); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	out := <-done
	if out.err == nil || !errors.Is(out.err, context.Canceled) {
		t.Fatalf("err=%v want cancellation", out.err)
	}
	if out.job.Status != JobStatusCancelled || out.job.CancelOutcome != CancelOutcomeBeforeSubmit {
		t.Fatalf("job=%s/%s want cancelled/before_submit", out.job.Status, out.job.CancelOutcome)
	}
	if adapter.submitCount() != 0 {
		t.Fatalf("cancelled profile must never submit")
	}
	if adapter.cancelCalls.Load() == 0 {
		t.Fatalf("published profile handle must get a best-effort cancel")
	}
	// No capacity leak (fresh, ungated adapter for the probe launch).
	r.RegisterAdapter(newProfilingAdapter(true))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := r.Launch(ctx, LaunchRequest{Owner: JobOwner{ThreadID: "thread-1", Handle: "tok_n"}, Provider: "fake", SQL: "select 2"}); err != nil {
		t.Fatalf("capacity leaked after profile cancel: %v", err)
	}
}

// --- reservation-conflict recovery skips profiling entirely ---

func TestProfileReservationConflictRecoversWithoutProfiling(t *testing.T) {
	owner := JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_orig"}
	store := &conflictReserveStore{
		MemoryJobStore: NewMemoryJobStore(),
		guardKey:       "call-pc",
		competitor: JobRecord{
			ID: "qj-fake-pcwin", Provider: "fake", Status: JobStatusRunning,
			Owner: owner, Handle: NativeHandle{Provider: "fake", ID: "n-pc"},
			SQL: "select 1", IdempotencyKey: "call-pc",
			SubmittedAt: time.Now().Add(-time.Second), LastAccess: time.Now(),
			SubmitDispatched: true,
		},
	}
	adapter := newProfilingAdapter(true)
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	req := LaunchRequest{Owner: JobOwner{ThreadID: "thread-1", RunID: "run-1"}, Provider: "fake", SQL: "select 1", IdempotencyKey: "call-pc"}
	job, err := r.Launch(context.Background(), req)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if job.ID != "qj-fake-pcwin" {
		t.Fatalf("must recover winner, got %s", job.ID)
	}
	if adapter.profileCount() != 0 || adapter.submitCount() != 0 {
		t.Fatalf("conflict recovery must skip profiling AND submit: %d/%d", adapter.profileCount(), adapter.submitCount())
	}
}
