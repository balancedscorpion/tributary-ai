package jobs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrRevisionConflict is returned by RevisionedStore implementations when
// the expected revision does not match the stored one (another writer won).
var ErrRevisionConflict = errors.New("query job revision conflict")

// ErrLeaseLost is returned by fenced mutations when the claimed holder no
// longer holds an unexpired lease (per STORE time). Revision CAS alone
// cannot detect lease takeover — takeover does not bump the record
// revision — so profile mutations fence on the holder explicitly.
var ErrLeaseLost = errors.New("monitor lease no longer held by this process")

// QueryObligationManagedMetadataKey marks launches whose durable job must be
// attached to a run-scoped query obligation before any provider work. It is a
// persisted marker so recovery can make the same decision after a restart.
const QueryObligationManagedMetadataKey = "_query_obligation_managed"

// RequiresQueryObligationBinding reports whether a durable record belongs to
// the run-query obligation protocol. Generic Registry clients deliberately do
// not participate: query jobs may be thread-scoped and outlive a run.
func RequiresQueryObligationBinding(record JobRecord) bool {
	managed, _ := record.Metadata[QueryObligationManagedMetadataKey].(bool)
	return managed
}

// JobRecord is the fully serializable durable form of a job. The store
// implementation (MySQL/JSON) lives outside this package; this package
// defines the contract and writes through best-effort on every transition.
//
// Records are keyed on ThreadID (not RunID): jobs outlive runs.
type JobRecord struct {
	ID           string `json:"id"`
	Provider     string `json:"provider"`
	ConnectionID string `json:"connection_id,omitempty"`
	SQL          string `json:"sql,omitempty"`
	SQLPreview   string `json:"sql_preview,omitempty"`

	Status               JobStatus            `json:"status"`
	MonitorHealth        MonitorHealth        `json:"monitor_health,omitempty"`
	PolicyHealth         PolicyHealth         `json:"policy_health,omitempty"`
	MaterializationState MaterializationState `json:"materialization_state,omitempty"`

	Owner               JobOwner     `json:"owner"`
	Handle              NativeHandle `json:"handle,omitempty"`
	IdempotencyKey      string       `json:"idempotency_key,omitempty"`
	ReplacesJobID       string       `json:"replaces_job_id,omitempty"`
	KeepOnRunCompletion bool         `json:"keep_on_run_completion,omitempty"`
	// SubmitDispatched marks that the remote submit call was started for
	// this reservation. A crash between dispatch and the first durable
	// post-submit commit leaves a reserved+dispatched record, which
	// rehydration must treat as ambiguous (possibly consumed initial
	// page), never as a safe expiry.
	SubmitDispatched bool `json:"submit_dispatched,omitempty"`

	Budgets Budgets `json:"budgets,omitempty"`

	SubmittedAt time.Time `json:"submitted_at"`
	// RemoteSubmittedAt is the actual provider-dispatch timestamp, committed
	// atomically with SubmitDispatched. Nil denotes a legacy record and falls
	// back to SubmittedAt for budget/reconciliation calculations.
	RemoteSubmittedAt *time.Time  `json:"remote_submitted_at,omitempty"`
	NextPollAt        *time.Time  `json:"next_poll_at,omitempty"`
	NextPollAction    RetryAction `json:"next_poll_action,omitempty"`
	StartedAt         *time.Time  `json:"started_at,omitempty"` // first observed running
	CompletedAt       *time.Time  `json:"completed_at,omitempty"`
	LastAccess        time.Time   `json:"last_access_at"`

	CancelReason    CancelReason  `json:"cancel_reason,omitempty"`
	CancelOutcome   CancelOutcome `json:"cancel_outcome,omitempty"`
	CancelStartedAt *time.Time    `json:"cancel_started_at,omitempty"`
	Error           string        `json:"error,omitempty"`

	ArtifactID string         `json:"artifact_id,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
	WakeEvents []WakeEvent    `json:"wake_events,omitempty"`

	// Result validation (orthogonal axis; see ValidationState).
	ValidationState  ValidationState `json:"validation_state,omitempty"`
	ValidationDetail string          `json:"validation_detail,omitempty"`

	// Pre-submit profiling lifecycle (see ProfileOutcome / ProfileObserver).
	Profile        *QueryProfile  `json:"profile,omitempty"`
	ProfileOutcome ProfileOutcome `json:"profile_outcome,omitempty"`
	ProfileHandle  NativeHandle   `json:"profile_handle,omitempty"`
	ProfilePhase   string         `json:"profile_phase,omitempty"`
	// ProfileDispatched marks that the remote PROFILE call was started —
	// strictly persisted immediately before the profiler's remote-call
	// boundary, and fully distinct from SubmitDispatched (which stays
	// false until the accepted profile is durable and the actual query
	// Submit is imminent). Rehydration branches on it: false = provably
	// no remote profile work; true = ambiguous/needs cleanup.
	ProfileDispatched bool `json:"profile_dispatched,omitempty"`

	// Revision is the optimistic-concurrency counter maintained by
	// RevisionedStore implementations (0 = never persisted).
	Revision int64 `json:"revision,omitempty"`
	// DrainSeq is the last durably committed drain observation for
	// RequiresContinuousDrain providers. Sinks dedupe replays by it.
	DrainSeq            int64 `json:"drain_seq,omitempty"`
	DrainRowsCommitted  int64 `json:"drain_rows_committed,omitempty"`
	DrainBytesCommitted int64 `json:"drain_bytes_committed,omitempty"`
	DrainTruncated      bool  `json:"drain_truncated,omitempty"`
}

// JobStore is the persistence contract implemented by chat/store (MySQL and
// JSON). Implementations must make UpsertQueryJob atomic per record.
//
// Stores SHOULD additionally implement the optional interfaces below.
// When they do, the registry upgrades its behavior automatically:
//   - RevisionedStore: every write becomes a CAS keyed on JobRecord.Revision.
//   - LeaseStore: monitors run under a lease so two processes never drive
//     the same job.
//   - WakeClaimStore: wake-event claims become atomic across processes,
//     making auto-continuation exactly-once.
//
// MemoryJobStore implements all three and is the reference semantics.
type JobStore interface {
	UpsertQueryJob(ctx context.Context, rec JobRecord) error
	GetQueryJob(ctx context.Context, id string) (JobRecord, bool, error)
	ListActiveQueryJobs(ctx context.Context) ([]JobRecord, error)
	ListQueryJobsByThread(ctx context.Context, threadID string) ([]JobRecord, error)
	DeleteQueryJob(ctx context.Context, id string) error
}

// QueryObligationBinder durably attaches a reserved query job to the
// run-scoped obligation that owns it. Implementations must be idempotent and
// must atomically advance a replacement obligation from the predecessor to
// this concrete durable successor. Registry calls the binder after the
// durable idempotency winner is known and before Profile or Submit, so a
// binding failure is guaranteed to cause zero provider work.
//
// The interface intentionally accepts only jobID. The implementation re-reads
// the authoritative durable JobRecord (owner, run and ReplacesJobID) instead
// of trusting caller-supplied lineage.
type QueryObligationBinder interface {
	BindQueryObligation(ctx context.Context, jobID string) error
}

// RevisionedStore adds compare-and-swap semantics. UpsertQueryJobCAS writes
// rec only when the stored revision equals expectedRevision (0 = record
// must not exist yet) and returns the new revision; otherwise it returns
// ErrRevisionConflict and writes nothing.
type RevisionedStore interface {
	UpsertQueryJobCAS(ctx context.Context, rec JobRecord, expectedRevision int64) (int64, error)
}

// LeaseStore provides per-job monitor leases. Acquire returns false when a
// different live holder owns the lease. Renew returns false when the
// holder no longer owns it (expired and taken, or released).
type LeaseStore interface {
	AcquireMonitorLease(ctx context.Context, jobID, holder string, ttl time.Duration) (bool, error)
	RenewMonitorLease(ctx context.Context, jobID, holder string, ttl time.Duration) (bool, error)
	ReleaseMonitorLease(ctx context.Context, jobID, holder string) error
}

// WakeClaimStore claims all currently-unclaimed wake events for a job
// atomically. Two concurrent claimants must partition events: an event is
// returned to exactly one of them.
//
// REVISION-DOMAIN CAUTION: a claim mutates the job record and typically
// bumps its lifecycle revision. With this legacy signature the registry
// cannot see the new revision, so it self-heals by re-reading the record
// after every claim. Implement RevisionedWakeClaimStore instead — the
// registry prefers it and skips the extra read.
type WakeClaimStore interface {
	ClaimWakeEvents(ctx context.Context, jobID, claimant string) (claimed []WakeEvent, err error)
}

// RevisionedWakeClaimStore is the preferred claim contract: the claim
// returns the record's new lifecycle revision so sidecar writes can never
// invisibly invalidate the registry's lifecycle/drain CAS state (the next
// DrainCommit would otherwise conflict). Stores implement EITHER this or
// the legacy WakeClaimStore (same method name, different signature).
type RevisionedWakeClaimStore interface {
	ClaimWakeEvents(ctx context.Context, jobID, claimant string) (claimed []WakeEvent, newRevision int64, err error)
}

// ValidationStore atomically updates the validation axis of a job record
// without disturbing lifecycle fields. The update is CAS-guarded on
// expectedRevision (ErrRevisionConflict on mismatch) and returns the new
// lifecycle revision, so validation writes can neither race each other nor
// invisibly invalidate lifecycle/drain CAS.
type ValidationStore interface {
	UpdateQueryJobValidation(ctx context.Context, jobID string, state ValidationState, detail string, expectedRevision int64) (newRevision int64, err error)
}

// WakeSpec is an append-only wake request inside a fenced mutation. Wakes
// are deduped per job by WakeKind (stable per-job idempotency): a kind
// already present in the record is never appended again, so replays and
// ambiguous-commit retries cannot duplicate wakes.
type WakeSpec struct {
	Kind   WakeKind
	Detail string
}

// ProfileMutation is the narrow patch a profile-phase write may apply.
// Nil fields are untouched; there is deliberately no whole-record form so
// a stale holder can never overwrite unrelated lifecycle state.
//
// This is the ONE atomic durable boundary for every transition out of
// profiling: terminal side effects (CancelOutcome, store-stamped
// CompletedAt, wakes, capacity release) commit in the SAME store
// transaction as the status change — never as follow-up writes.
type ProfileMutation struct {
	Dispatched   *bool
	Handle       *NativeHandle
	Phase        *string
	Profile      *QueryProfile
	Outcome      *ProfileOutcome
	Error        *string
	TransitionTo *JobStatus // must be a legal transition from profiling

	// Terminal side effects (honored when TransitionTo is terminal):
	CancelReason  *CancelReason
	CancelOutcome *CancelOutcome
	Wakes         []WakeSpec
}

// ProfileMutationResult reports what the store committed, so local state
// mirrors the durable record exactly without any follow-up read or write.
type ProfileMutationResult struct {
	NewRevision int64
	// At is the store/DB timestamp of the mutation (LastAccess).
	At time.Time
	// CompletedAt is stamped BY THE STORE on terminal transitions.
	CompletedAt *time.Time
	// AppendedWakes are the wake events actually appended (post-dedup),
	// carrying the store timestamp.
	AppendedWakes []WakeEvent
	// CapacityReleased is true when the terminal transition atomically
	// released the job's combined capacity reservation. The registry never
	// depends on a later ReleaseQueryCapacity call for correctness.
	CapacityReleased bool
}

// FencedProfileStore applies profile mutations under the triple fence
// required for crash/lease safety, atomically in ONE store transaction:
//  1. the record's lifecycle revision equals expectedRevision
//     (ErrRevisionConflict otherwise),
//  2. the record's status is JobStatusProfiling
//     (ErrRevisionConflict otherwise),
//  3. holder currently holds an UNEXPIRED monitor lease for the job,
//     judged by STORE/DB time (ErrLeaseLost otherwise).
//
// On success the patch is applied — profile fields, an optional legal
// status transition, CompletedAt/LastAccess stamped by store time,
// deduped wake appends, and revision increment — and, for TERMINAL
// transitions, any store-side combined capacity reservation is released
// in the same transaction. A profiling->reserved transition releases only
// the nested profiling count (implicitly, via the status change) while
// preserving the ordinary execution slot.
//
// JSON store: one atomicfile record write carrying all of the above (the
// record IS the transaction). MySQL: one UPDATE guarded by
// revision+status+lease-holder+lease_expires_at > NOW() applying every
// column, with wake dedup by kind in the same statement/transaction.
//
// A durable store that implements LeaseStore MUST implement this too;
// the registry fails closed before invoking any profiler otherwise.
// MemoryJobStore is the reference semantics.
type FencedProfileStore interface {
	ApplyProfileMutation(ctx context.Context, jobID, holder string, expectedRevision int64, mut ProfileMutation) (ProfileMutationResult, error)
}

// IdempotencyLookupStore resolves the durable job previously created under
// the same (provider, canonical connection key, idempotency key), enabling
// replay-safe Launch recovery. OPTIONAL: stores that cannot index the key
// simply do not implement it and Launch behaves as before.
//
// Determinism contract: when duplicates exist (a reservation race that the
// store's uniqueness constraint could not prevent), every call must return
// the SAME stable winner (e.g. earliest SubmittedAt, then lowest ID) so
// racing processes converge on one job.
type IdempotencyLookupStore interface {
	GetQueryJobByIdempotency(ctx context.Context, provider, connectionKey, key string) (JobRecord, bool, error)
}

// CapacityLimits are the configured cross-process admission limits.
// Per-connection counting keys on ConnectionID with a fallback to the
// provider name when empty (the same rule as connectionKey in this
// package) so a limit binds per warehouse even without an explicit id.
//
// The MaxProfiling* fields are NESTED limits over records currently in
// JobStatusProfiling: a profiling job counts toward BOTH the overall
// limits (it holds its execution slot end to end — no release/reacquire
// between profile acceptance and submit) AND the profiling limits, so
// profiles can never occupy every execution slot. Stores enforcing
// ReserveQueryCapacity must apply the nested limits whenever
// rec.Status == JobStatusProfiling.
type CapacityLimits struct {
	MaxGlobal        int
	MaxPerThread     int
	MaxPerConnection int

	MaxProfilingGlobal        int // default 4
	MaxProfilingPerThread     int // default 1
	MaxProfilingPerConnection int // default 1
}

// CapacityReserver is the transactional cross-process admission seam.
// ReserveQueryCapacity must, in ONE transaction:
//  1. FIRST resolve idempotency: when a DIFFERENT-ID record already
//     exists for rec's canonical (provider, connection key,
//     IdempotencyKey), return ErrRevisionConflict — BEFORE any capacity
//     rejection, so a replay recovers the original job immediately even
//     when capacity is completely full;
//  2. count active (non-terminal) query jobs globally / for
//     rec.Owner.ThreadID / for the record's connection key — applying
//     the nested MaxProfiling* limits when rec.Status==profiling — and
//     iff all limits admit it, persist rec as the reserved record and
//     return granted with its new revision.
//
// When granted, the write IS the reserve-before-submit persist — the
// registry does not double-write it via CAS. ReleaseQueryCapacity is
// called once when the job reaches a terminal state (after the terminal
// record persisted) and must be idempotent; stores that derive counts
// from record status may implement it as a no-op.
type CapacityReserver interface {
	ReserveQueryCapacity(ctx context.Context, rec JobRecord, limits CapacityLimits) (granted bool, newRevision int64, err error)
	ReleaseQueryCapacity(ctx context.Context, jobID string) error
}

// MemoryJobStore is an in-memory JobStore for tests and store-less
// deployments. It intentionally mirrors the atomic-per-record semantics of
// the JSON store and implements RevisionedStore, LeaseStore, and
// WakeClaimStore as the reference semantics for real stores.
type MemoryJobStore struct {
	mu     sync.RWMutex
	recs   map[string]JobRecord
	nowFn  func() time.Time
	leases map[string]memoryLease
}

type memoryLease struct {
	holder  string
	expires time.Time
}

func NewMemoryJobStore() *MemoryJobStore {
	return &MemoryJobStore{recs: map[string]JobRecord{}, leases: map[string]memoryLease{}, nowFn: time.Now}
}

func (s *MemoryJobStore) UpsertQueryJob(_ context.Context, rec JobRecord) error {
	if err := ValidateDurableQueryJobIdentity(rec); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := cloneRecord(rec)
	if prev, ok := s.recs[rec.ID]; ok && stored.Revision <= prev.Revision {
		stored.Revision = prev.Revision + 1
	} else if stored.Revision == 0 {
		stored.Revision = 1
	}
	s.recs[rec.ID] = stored
	return nil
}

func (s *MemoryJobStore) UpsertQueryJobCAS(_ context.Context, rec JobRecord, expectedRevision int64) (int64, error) {
	if err := ValidateDurableQueryJobIdentity(rec); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, exists := s.recs[rec.ID]
	switch {
	case !exists && expectedRevision != 0:
		return 0, ErrRevisionConflict
	case exists && prev.Revision != expectedRevision:
		return 0, ErrRevisionConflict
	}
	stored := cloneRecord(rec)
	stored.Revision = expectedRevision + 1
	s.recs[rec.ID] = stored
	return stored.Revision, nil
}

func (s *MemoryJobStore) AcquireMonitorLease(_ context.Context, jobID, holder string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFn()
	lease, ok := s.leases[jobID]
	if ok && lease.holder != holder && lease.expires.After(now) {
		return false, nil
	}
	s.leases[jobID] = memoryLease{holder: holder, expires: now.Add(ttl)}
	return true, nil
}

func (s *MemoryJobStore) RenewMonitorLease(_ context.Context, jobID, holder string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowFn()
	lease, ok := s.leases[jobID]
	if !ok || lease.holder != holder || !lease.expires.After(now) {
		return false, nil
	}
	s.leases[jobID] = memoryLease{holder: holder, expires: now.Add(ttl)}
	return true, nil
}

func (s *MemoryJobStore) ReleaseMonitorLease(_ context.Context, jobID, holder string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if lease, ok := s.leases[jobID]; ok && lease.holder == holder {
		delete(s.leases, jobID)
	}
	return nil
}

func (s *MemoryJobStore) ClaimWakeEvents(_ context.Context, jobID, _ string) ([]WakeEvent, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.recs[jobID]
	if !ok {
		return nil, 0, nil
	}
	claimed := make([]WakeEvent, 0, len(rec.WakeEvents))
	for i := range rec.WakeEvents {
		if !rec.WakeEvents[i].Claimed {
			rec.WakeEvents[i].Claimed = true
			claimed = append(claimed, rec.WakeEvents[i])
		}
	}
	if len(claimed) > 0 {
		rec.Revision++
		s.recs[jobID] = rec
	}
	return claimed, rec.Revision, nil
}

func (s *MemoryJobStore) UpdateQueryJobValidation(_ context.Context, jobID string, state ValidationState, detail string, expectedRevision int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.recs[jobID]
	if !ok {
		return 0, errors.New("query job not found")
	}
	if rec.Revision != expectedRevision {
		return 0, ErrRevisionConflict
	}
	rec.ValidationState = state
	rec.ValidationDetail = detail
	rec.Revision++
	s.recs[jobID] = rec
	return rec.Revision, nil
}

func (s *MemoryJobStore) ReserveQueryCapacity(_ context.Context, rec JobRecord, limits CapacityLimits) (bool, int64, error) {
	if err := ValidateDurableQueryJobIdentity(rec); err != nil {
		return false, 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	connKey := connectionKey(rec.Provider, rec.ConnectionID)
	// Idempotency resolution FIRST (frozen ordering): a different-ID
	// canonical winner short-circuits before any capacity verdict.
	if key := strings.TrimSpace(rec.IdempotencyKey); key != "" {
		for _, existing := range s.recs {
			if existing.ID == rec.ID || existing.IdempotencyKey != key {
				continue
			}
			if strings.TrimSpace(strings.ToLower(existing.Provider)) != strings.TrimSpace(strings.ToLower(rec.Provider)) {
				continue
			}
			if connectionKey(existing.Provider, existing.ConnectionID) != connKey {
				continue
			}
			return false, 0, ErrRevisionConflict
		}
	}
	global, thread, conn := 0, 0, 0
	profGlobal, profThread, profConn := 0, 0, 0
	for _, existing := range s.recs {
		if existing.ID == rec.ID || !isActiveStatus(existing.Status) {
			continue
		}
		global++
		if existing.Owner.ThreadID == rec.Owner.ThreadID {
			thread++
		}
		sameConn := connectionKey(existing.Provider, existing.ConnectionID) == connKey
		if sameConn {
			conn++
		}
		if existing.Status == JobStatusProfiling {
			profGlobal++
			if existing.Owner.ThreadID == rec.Owner.ThreadID {
				profThread++
			}
			if sameConn {
				profConn++
			}
		}
	}
	if (limits.MaxGlobal > 0 && global >= limits.MaxGlobal) ||
		(limits.MaxPerThread > 0 && thread >= limits.MaxPerThread) ||
		(limits.MaxPerConnection > 0 && conn >= limits.MaxPerConnection) {
		return false, 0, nil
	}
	if rec.Status == JobStatusProfiling {
		if (limits.MaxProfilingGlobal > 0 && profGlobal >= limits.MaxProfilingGlobal) ||
			(limits.MaxProfilingPerThread > 0 && profThread >= limits.MaxProfilingPerThread) ||
			(limits.MaxProfilingPerConnection > 0 && profConn >= limits.MaxProfilingPerConnection) {
			return false, 0, nil
		}
	}
	stored := cloneRecord(rec)
	if prev, ok := s.recs[rec.ID]; ok {
		stored.Revision = prev.Revision + 1
	} else {
		stored.Revision = 1
	}
	s.recs[rec.ID] = stored
	return true, stored.Revision, nil
}

// ReleaseQueryCapacity is a no-op: this store derives capacity counts from
// record status, so persisting the terminal record already released it.
func (s *MemoryJobStore) ReleaseQueryCapacity(context.Context, string) error { return nil }

func (s *MemoryJobStore) ApplyProfileMutation(_ context.Context, jobID, holder string, expectedRevision int64, mut ProfileMutation) (ProfileMutationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.recs[jobID]
	if !ok || rec.Revision != expectedRevision || rec.Status != JobStatusProfiling {
		return ProfileMutationResult{}, ErrRevisionConflict
	}
	lease, held := s.leases[jobID]
	if !held || lease.holder != holder || !lease.expires.After(s.nowFn()) {
		return ProfileMutationResult{}, ErrLeaseLost
	}
	if mut.Dispatched != nil {
		rec.ProfileDispatched = *mut.Dispatched
	}
	if mut.Handle != nil {
		rec.ProfileHandle = cloneHandle(*mut.Handle)
	}
	if mut.Phase != nil {
		rec.ProfilePhase = *mut.Phase
	}
	if mut.Profile != nil {
		rec.Profile = cloneQueryProfile(mut.Profile)
	}
	if mut.Outcome != nil {
		rec.ProfileOutcome = *mut.Outcome
	}
	if mut.Error != nil {
		rec.Error = *mut.Error
	}
	now := s.nowFn()
	result := ProfileMutationResult{At: now}
	terminal := false
	if mut.TransitionTo != nil {
		if !canTransition(JobStatusProfiling, *mut.TransitionTo) {
			return ProfileMutationResult{}, ErrRevisionConflict
		}
		rec.Status = *mut.TransitionTo
		terminal = isTerminalStatus(rec.Status)
	}
	if terminal {
		// Store-stamped terminal metadata, in the SAME transaction.
		t := now
		rec.CompletedAt = &t
		result.CompletedAt = &t
		if mut.CancelReason != nil {
			rec.CancelReason = *mut.CancelReason
		}
		if mut.CancelOutcome != nil {
			rec.CancelOutcome = *mut.CancelOutcome
		}
		// Capacity: this store derives counts from record status, so the
		// terminal write itself IS the combined-slot release.
		result.CapacityReleased = true
	}
	// Append-only, kind-deduped wakes (replay-safe).
	existing := map[WakeKind]bool{}
	for _, ev := range rec.WakeEvents {
		existing[ev.Kind] = true
	}
	for _, spec := range mut.Wakes {
		if existing[spec.Kind] {
			continue
		}
		existing[spec.Kind] = true
		ev := WakeEvent{At: now, Kind: spec.Kind, Detail: spec.Detail}
		rec.WakeEvents = append(rec.WakeEvents, ev)
		result.AppendedWakes = append(result.AppendedWakes, ev)
	}
	rec.LastAccess = now
	rec.Revision++
	s.recs[jobID] = rec
	result.NewRevision = rec.Revision
	return result, nil
}

func (s *MemoryJobStore) GetQueryJobByIdempotency(_ context.Context, provider, connKey, key string) (JobRecord, bool, error) {
	provider = strings.TrimSpace(strings.ToLower(provider))
	connKey = strings.TrimSpace(strings.ToLower(connKey))
	key = strings.TrimSpace(key)
	if key == "" {
		return JobRecord{}, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var winner *JobRecord
	for id := range s.recs {
		rec := s.recs[id]
		if rec.IdempotencyKey != key {
			continue
		}
		if strings.TrimSpace(strings.ToLower(rec.Provider)) != provider {
			continue
		}
		if connectionKey(rec.Provider, rec.ConnectionID) != connKey {
			continue
		}
		// Deterministic winner: earliest SubmittedAt, then lowest ID.
		if winner == nil ||
			rec.SubmittedAt.Before(winner.SubmittedAt) ||
			(rec.SubmittedAt.Equal(winner.SubmittedAt) && rec.ID < winner.ID) {
			cloned := cloneRecord(rec)
			winner = &cloned
		}
	}
	if winner == nil {
		return JobRecord{}, false, nil
	}
	return *winner, true, nil
}

func (s *MemoryJobStore) GetQueryJob(_ context.Context, id string) (JobRecord, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.recs[id]
	if !ok {
		return JobRecord{}, false, nil
	}
	return cloneRecord(rec), true, nil
}

func (s *MemoryJobStore) ListActiveQueryJobs(_ context.Context) ([]JobRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]JobRecord, 0, len(s.recs))
	for _, rec := range s.recs {
		if isActiveStatus(rec.Status) {
			out = append(out, cloneRecord(rec))
		}
	}
	return out, nil
}

func (s *MemoryJobStore) ListQueryJobsByThread(_ context.Context, threadID string) ([]JobRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]JobRecord, 0, 8)
	for _, rec := range s.recs {
		if rec.Owner.ThreadID == threadID {
			out = append(out, cloneRecord(rec))
		}
	}
	return out, nil
}

func (s *MemoryJobStore) DeleteQueryJob(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.recs, id)
	return nil
}

func cloneQueryProfile(p *QueryProfile) *QueryProfile {
	if p == nil {
		return nil
	}
	out := *p
	if p.Warnings != nil {
		out.Warnings = append([]string(nil), p.Warnings...)
	}
	out.ProviderDetails = cloneMap(p.ProviderDetails)
	if p.EstimatedBytes != nil {
		v := *p.EstimatedBytes
		out.EstimatedBytes = &v
	}
	if p.EstimatedRows != nil {
		v := *p.EstimatedRows
		out.EstimatedRows = &v
	}
	return &out
}

func cloneRecord(rec JobRecord) JobRecord {
	out := rec
	out.Metadata = cloneMap(rec.Metadata)
	out.Profile = cloneQueryProfile(rec.Profile)
	out.ProfileHandle = cloneHandle(rec.ProfileHandle)
	if rec.Handle.Extra != nil {
		extra := make(map[string]string, len(rec.Handle.Extra))
		for k, v := range rec.Handle.Extra {
			extra[k] = v
		}
		out.Handle.Extra = extra
	}
	if rec.WakeEvents != nil {
		out.WakeEvents = append([]WakeEvent(nil), rec.WakeEvents...)
	}
	if rec.StartedAt != nil {
		t := *rec.StartedAt
		out.StartedAt = &t
	}
	if rec.CompletedAt != nil {
		t := *rec.CompletedAt
		out.CompletedAt = &t
	}
	return out
}
