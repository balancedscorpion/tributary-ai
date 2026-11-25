package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

type JobStatus string

const (
	JobStatusUnknown   JobStatus = "unknown"
	JobStatusSubmitted JobStatus = "submitted"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCancelled JobStatus = "cancelled"
)

var (
	ErrNotFound          = errors.New("job not found")
	ErrNotCompleted      = errors.New("job not completed")
	ErrOwnershipMismatch = errors.New("job ownership mismatch")
	ErrConcurrencyLimit  = errors.New("job concurrency limit exceeded")
)

const (
	defaultMaxJobsPerThread = 5
	defaultMaxJobsGlobal    = 10
	cancelGracePeriod       = 2 * time.Second
)

type QueryResult struct {
	Rows     []map[string]any `json:"rows,omitempty"`
	Schema   []map[string]any `json:"schema,omitempty"`
	Metadata map[string]any   `json:"metadata,omitempty"`
}

type JobOwner struct {
	ThreadID  string `json:"thread_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	UserToken string `json:"user_token,omitempty"`
	Handle    string `json:"handle,omitempty"`
}

type PollFunc func(ctx context.Context) (done bool, result *QueryResult, err error)
type CancelFunc func(ctx context.Context) error

type Job struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	SQL         string `json:"sql,omitempty"`
	SQLPreview  string `json:"sql_preview,omitempty"`
	Status      JobStatus
	SubmittedAt time.Time `json:"submitted_at"`
	// RemoteSubmittedAt is stamped durably with SubmitDispatched immediately
	// before the provider boundary. Admission and profiling time must not burn
	// the warehouse execution budget. Legacy records fall back to SubmittedAt.
	RemoteSubmittedAt *time.Time `json:"remote_submitted_at,omitempty"`
	// NextPollAt is a durable provider backpressure deadline. It is seeded
	// from authoritative Retry-After hints so failover cannot immediately
	// hammer an endpoint that asked this job to wait.
	NextPollAt     *time.Time  `json:"next_poll_at,omitempty"`
	NextPollAction RetryAction `json:"next_poll_action,omitempty"`
	LastAccess     time.Time   `json:"last_access_at"`
	CompletedAt    *time.Time  `json:"completed_at,omitempty"`
	Result         *QueryResult
	Error          string         `json:"error,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Owner          JobOwner       `json:"owner"`

	// Managed lifecycle fields (jobs launched via Launch; zero-valued on
	// legacy Register jobs).
	ConnectionID        string               `json:"connection_id,omitempty"`
	Handle              NativeHandle         `json:"handle,omitempty"`
	IdempotencyKey      string               `json:"idempotency_key,omitempty"`
	ReplacesJobID       string               `json:"replaces_job_id,omitempty"`
	KeepOnRunCompletion bool                 `json:"keep_on_run_completion,omitempty"`
	Budgets             Budgets              `json:"budgets,omitempty"`
	MonitorHealth       MonitorHealth        `json:"monitor_health,omitempty"`
	PolicyHealth        PolicyHealth         `json:"policy_health,omitempty"`
	Materialization     MaterializationState `json:"materialization_state,omitempty"`
	StartedAt           *time.Time           `json:"started_at,omitempty"`
	CancelReason        CancelReason         `json:"cancel_reason,omitempty"`
	CancelOutcome       CancelOutcome        `json:"cancel_outcome,omitempty"`
	CancelStartedAt     *time.Time           `json:"cancel_started_at,omitempty"`
	WakeEvents          []WakeEvent          `json:"wake_events,omitempty"`
	ArtifactID          string               `json:"artifact_id,omitempty"`
	DrainSeq            int64                `json:"drain_seq,omitempty"`
	// Drain*Committed is the cumulative durable accounting for every
	// successfully committed continuous-drain observation. Unlike the
	// in-memory assembler, these counters survive lease handoff/restart and
	// keep result ceilings and terminal summaries global to the query.
	DrainRowsCommitted  int64           `json:"drain_rows_committed,omitempty"`
	DrainBytesCommitted int64           `json:"drain_bytes_committed,omitempty"`
	DrainTruncated      bool            `json:"drain_truncated,omitempty"`
	Validation          ValidationState `json:"validation_state,omitempty"`
	ValidationDetail    string          `json:"validation_detail,omitempty"`
	Profile             *QueryProfile   `json:"profile,omitempty"`
	ProfileOutcome      ProfileOutcome  `json:"profile_outcome,omitempty"`
	ProfileHandle       NativeHandle    `json:"profile_handle,omitempty"`
	ProfilePhase        string          `json:"profile_phase,omitempty"`
	ProfileDispatched   bool            `json:"profile_dispatched,omitempty"`

	// persistMu serializes ordinary whole-record writes for this process.
	// job.mu protects the in-memory snapshot, but is deliberately released
	// during store I/O; without this second lock a monitor transition and a
	// concurrent RequestCancel can race their CAS writes and make the winner
	// look like an external stale-holder conflict.
	persistMu sync.Mutex
	mu        sync.RWMutex
	pollCtx   context.Context
	pollStop  context.CancelFunc
	pollFn    PollFunc
	cancelFn  CancelFunc

	// SubmitDispatched persists the "remote submit was started" marker
	// (see JobRecord.SubmitDispatched).
	SubmitDispatched bool `json:"submit_dispatched,omitempty"`

	// Managed-lifecycle runtime state (guarded by mu unless noted).
	managed          bool
	adapter          ProviderAdapter
	assembler        *resultAssembler   // owned by launch until monitor starts, then by the monitor goroutine
	backlog          *drainBacklog      // uncommitted drain observation awaiting durable commit
	reservedViaStore bool               // admission granted by a CapacityReserver
	capacityReleased bool               // ReleaseQueryCapacity already issued
	driving          bool               // a local monitor/submission-watch goroutine owns this job
	profileCancel    context.CancelFunc // cancels an in-flight local profile
	cancelWanted     bool
	hardExpired      bool // hard-budget cancel: terminal state becomes expired
	emittedWakes     map[WakeKind]bool
	lastSignals      map[ProgressSignal]int64
	lastProgressAt   time.Time
	noProgressObs    int // comparable observations with no advancement
	monitorErrs      int
	monitorErrAt     time.Time     // start of current error streak
	storeRevision    int64         // CAS revision when RevisionedStore is used
	cancellingAt     time.Time     // when cancelling was entered
	wakeSignal       chan struct{} // buffered(1) nudge for the monitor loop
	// terminalPersistPending keeps an in-memory terminal verdict from being
	// reaped while its durable lifecycle write is being repaired. The repair
	// loop never performs provider I/O; it only converges the already-observed
	// terminal state under CAS (and a monitor lease when the store has one).
	terminalPersistPending   bool
	terminalRepairing        bool
	terminalPersistAbandoned bool
}

type RegistryOptions struct {
	// Legacy Register() limits (hard-fail, legacy jobs only).
	MaxJobsPerThread int
	MaxJobsGlobal    int

	// Managed-lifecycle options. All optional: the zero value preserves
	// legacy behavior exactly.
	Store        JobStore         // durable write-through + rehydration source
	Admission    *AdmissionConfig // managed-job admission (queue, not fail)
	Policy       *PolicyConfig    // budgets / stall evaluation
	Profile      *ProfilePolicy   // pre-submit profiling bounds
	Scheduler    *SchedulerConfig // monitor cadence + error backoff
	Materializer Materializer     // durable-result seam
	RetentionTTL time.Duration    // in-memory retention of terminal jobs; default 2h
	LeaseTTL     time.Duration    // monitor lease TTL when the store supports leases; default 60s
	InstanceID   string           // this process's lease/claim identity; default random
	Logf         func(string, ...any)
	Now          func() time.Time // test seam
}

type Registry struct {
	mu               sync.RWMutex
	jobs             map[string]*Job
	maxJobsPerThread int
	maxJobsGlobal    int

	store        JobStore
	adapters     map[string]ProviderAdapter
	admission    *admissionState
	policy       PolicyConfig
	profile      ProfilePolicy
	sched        SchedulerConfig
	materializer Materializer
	retention    time.Duration
	leaseTTL     time.Duration
	instanceID   string
	logf         func(string, ...any)
	now          func() time.Time

	notifyMu sync.Mutex
	notifyCh chan struct{}

	// Rehydrate is serialized per registry. Same-instance lease acquisition is
	// reentrant by design, so two concurrent scans otherwise could build two
	// Job objects and both issue Adopt before either inserts its placeholder.
	rehydrateMu sync.Mutex
}

const defaultRetentionTTL = 2 * time.Hour

func NewRegistry(opts RegistryOptions) *Registry {
	maxPerThread := opts.MaxJobsPerThread
	if maxPerThread <= 0 {
		maxPerThread = defaultMaxJobsPerThread
	}
	maxGlobal := opts.MaxJobsGlobal
	if maxGlobal <= 0 {
		maxGlobal = defaultMaxJobsGlobal
	}
	admissionCfg := AdmissionConfig{}
	if opts.Admission != nil {
		admissionCfg = *opts.Admission
	}
	policyCfg := PolicyConfig{}
	if opts.Policy != nil {
		policyCfg = *opts.Policy
	}
	profileCfg := ProfilePolicy{}
	if opts.Profile != nil {
		profileCfg = *opts.Profile
	}
	schedCfg := SchedulerConfig{}
	if opts.Scheduler != nil {
		schedCfg = *opts.Scheduler
	}
	retention := opts.RetentionTTL
	if retention <= 0 {
		retention = defaultRetentionTTL
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	leaseTTL := opts.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = 60 * time.Second
	}
	instanceID := strings.TrimSpace(opts.InstanceID)
	if instanceID == "" {
		if tok, err := GenerateOwnerToken(); err == nil {
			instanceID = "reg_" + strings.TrimPrefix(tok, ownerTokenPrefix)
		} else {
			instanceID = "reg_fallback"
		}
	}
	return &Registry{
		jobs:             map[string]*Job{},
		maxJobsPerThread: maxPerThread,
		maxJobsGlobal:    maxGlobal,
		store:            opts.Store,
		adapters:         map[string]ProviderAdapter{},
		admission:        newAdmissionState(admissionCfg),
		policy:           policyCfg.withDefaults(),
		profile:          profileCfg.withDefaults(),
		sched:            schedCfg.withDefaults(),
		materializer:     opts.Materializer,
		retention:        retention,
		leaseTTL:         leaseTTL,
		instanceID:       instanceID,
		logf:             opts.Logf,
		now:              nowFn,
		notifyCh:         make(chan struct{}),
	}
}

// SetArtifactID durably associates the job with its materialized artifact
// (owner-checked). The tools slice calls this once the deterministic
// artifact id is minted.
func (r *Registry) SetArtifactID(id string, owner JobOwner, artifactID string) error {
	if r == nil {
		return ErrNotFound
	}
	job := r.lookupJob(id, normalizeOwner(owner))
	if job == nil {
		return ErrNotFound
	}
	job.mu.Lock()
	if !ownerMatches(job.Owner, normalizeOwner(owner)) {
		job.mu.Unlock()
		return ErrOwnershipMismatch
	}
	if job.Status == JobStatusProfiling {
		// Profiling is a fenced write domain. An ordinary artifact upsert would
		// bump the lifecycle revision behind the active profile holder and make
		// its next fenced mutation fail. The caller can retry after profiling.
		job.mu.Unlock()
		return ErrProfilePending
	}
	job.ArtifactID = strings.TrimSpace(artifactID)
	job.LastAccess = r.nowFn()
	job.mu.Unlock()
	r.afterTransition(job)
	return nil
}

// RegisterAdapter makes a provider adapter available to Launch, Rehydrate,
// and FetchPage. Later registrations for the same provider win.
func (r *Registry) RegisterAdapter(a ProviderAdapter) {
	if r == nil || a == nil {
		return
	}
	name := strings.TrimSpace(strings.ToLower(a.Provider()))
	if name == "" {
		return
	}
	r.mu.Lock()
	r.adapters[name] = a
	r.mu.Unlock()
}

func (r *Registry) adapterFor(provider string) ProviderAdapter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.adapters[strings.TrimSpace(strings.ToLower(provider))]
}

func (r *Registry) nowFn() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *Registry) logfSafe(format string, args ...any) {
	if r.logf != nil {
		r.logf(format, args...)
	}
}

func (r *Registry) Register(owner JobOwner, provider, sql, sqlPreview string, metadata map[string]any, poll PollFunc, cancel CancelFunc) (*Job, error) {
	if r == nil {
		return nil, errors.New("job registry is not configured")
	}
	owner = normalizeOwner(owner)
	if err := validateOwnerForRegister(owner); err != nil {
		return nil, err
	}
	if owner.Handle == "" {
		token, err := GenerateOwnerToken()
		if err != nil {
			return nil, err
		}
		owner.Handle = token
	}
	provider = strings.TrimSpace(strings.ToLower(provider))
	if provider == "" {
		provider = "unknown"
	}
	id, err := newJobID(provider)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	pollCtx, pollStop := context.WithCancel(context.Background())
	job := &Job{
		ID:          id,
		Provider:    provider,
		SQL:         strings.TrimSpace(sql),
		SQLPreview:  strings.TrimSpace(sqlPreview),
		Status:      JobStatusSubmitted,
		SubmittedAt: now,
		LastAccess:  now,
		Metadata:    cloneMap(metadata),
		Owner:       owner,
		pollCtx:     pollCtx,
		pollStop:    pollStop,
		pollFn:      poll,
		cancelFn:    cancel,
	}

	r.mu.Lock()
	threadCount, globalCount := r.activeCountsLocked(owner.ThreadID)
	if threadCount >= r.maxJobsPerThread || globalCount >= r.maxJobsGlobal {
		r.mu.Unlock()
		pollStop()
		return nil, ErrConcurrencyLimit
	}
	r.jobs[id] = job
	r.mu.Unlock()

	r.afterTransition(job)
	if poll != nil {
		go r.runPoller(job)
	}
	return cloneJob(job), nil
}

func (r *Registry) Status(id string, owner JobOwner) (*Job, bool) {
	return r.Snapshot(id, owner)
}

func (r *Registry) Snapshot(id string, owner JobOwner) (*Job, bool) {
	if r == nil {
		return nil, false
	}
	id = strings.TrimSpace(id)
	owner = normalizeOwner(owner)
	if id == "" || owner.ThreadID == "" || owner.Handle == "" {
		return nil, false
	}
	job := r.lookupJob(id, owner)
	if job == nil {
		return nil, false
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	if !ownerMatches(job.Owner, owner) {
		return nil, false
	}
	return cloneJobLocked(job), true
}

func (r *Registry) Results(id string, owner JobOwner) (*QueryResult, error) {
	if r == nil {
		return nil, ErrNotFound
	}
	id = strings.TrimSpace(id)
	owner = normalizeOwner(owner)
	if id == "" || owner.ThreadID == "" || owner.Handle == "" {
		return nil, ErrNotFound
	}
	job := r.lookupJob(id, owner)
	if job == nil {
		return nil, ErrNotFound
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if !ownerMatches(job.Owner, owner) {
		return nil, ErrOwnershipMismatch
	}
	job.LastAccess = time.Now()
	if job.Status != JobStatusCompleted {
		if job.Status == JobStatusFailed && strings.TrimSpace(job.Error) != "" {
			return nil, errors.New(strings.TrimSpace(job.Error))
		}
		return nil, ErrNotCompleted
	}
	return cloneResult(job.Result), nil
}

func (r *Registry) Cancel(id string, owner JobOwner) error {
	if r == nil {
		return ErrNotFound
	}
	id = strings.TrimSpace(id)
	owner = normalizeOwner(owner)
	if id == "" || owner.ThreadID == "" || owner.Handle == "" {
		return ErrNotFound
	}
	job := r.lookupJob(id, owner)
	if job == nil {
		return ErrNotFound
	}
	job.mu.Lock()

	if !ownerMatches(job.Owner, owner) {
		job.mu.Unlock()
		return ErrOwnershipMismatch
	}
	if job.managed {
		// Managed jobs follow the confirmed-cancellation lifecycle:
		// cancel_requested -> cancelling -> cancelled (provider-confirmed).
		job.mu.Unlock()
		return r.RequestCancel(id, owner, CancelReasonLegacy)
	}
	if isTerminal(job.Status) {
		job.LastAccess = time.Now()
		job.mu.Unlock()
		return nil
	}
	now := time.Now()
	job.Status = JobStatusCancelled
	job.CompletedAt = &now
	job.LastAccess = now
	if strings.TrimSpace(job.Error) == "" {
		job.Error = "query cancelled"
	}
	stop := job.pollStop
	cancelFn := job.cancelFn
	job.mu.Unlock()

	if stop != nil {
		stop()
	}
	if cancelFn != nil {
		cancelCtx, cancel := context.WithTimeout(context.Background(), cancelGracePeriod)
		_ = cancelFn(cancelCtx)
		cancel()
	}
	r.afterTransition(job)
	return nil
}

func (r *Registry) AttachOwner(id string, owner JobOwner) error {
	if r == nil {
		return ErrNotFound
	}
	id = strings.TrimSpace(id)
	owner = normalizeOwner(owner)
	if id == "" || owner.ThreadID == "" || owner.Handle == "" {
		return ErrNotFound
	}
	job := r.lookupJob(id, owner)
	if job == nil {
		return ErrNotFound
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	if !ownerMatches(job.Owner, owner) {
		return ErrOwnershipMismatch
	}
	if owner.RunID != "" && strings.TrimSpace(job.Owner.RunID) == "" {
		// Run ownership is identity, not caller-local observation context. A
		// same-thread caller with the owner capability may observe an older job
		// from a newer run, but must not re-home that job (and thereby change its
		// durable completion-obligation scope). Preserve the legacy ability to
		// attach a never-scoped job exactly once — but never for a managed
		// obligation job: managed launches are run-scoped at reservation time, so
		// a blank RunID here mirrors a deleted-run durable tombstone. Both stores
		// refuse to re-home that tombstone durably; filling it in memory would
		// mint a wrong-run obligation identity in status payloads.
		if managed, _ := job.Metadata[QueryObligationManagedMetadataKey].(bool); !managed {
			job.Owner.RunID = owner.RunID
		}
	}
	// UserToken is durable launch identity, not caller-local attachment
	// context. In particular, a blank durable token is still authoritative:
	// filling it in memory would make the next lifecycle write conflict with
	// the stored identity. AttachOwner therefore never mutates UserToken.
	job.LastAccess = time.Now()
	return nil
}

func (r *Registry) runPoller(job *Job) {
	if job == nil || job.pollFn == nil {
		return
	}
	wait := 300 * time.Millisecond
	for {
		select {
		case <-job.pollCtx.Done():
			return
		default:
		}

		done, result, err := job.pollFn(job.pollCtx)
		now := time.Now()

		job.mu.Lock()
		if isTerminal(job.Status) {
			job.mu.Unlock()
			return
		}
		if job.Status == JobStatusSubmitted {
			job.Status = JobStatusRunning
		}
		job.LastAccess = now
		if err != nil {
			job.Status = JobStatusFailed
			job.Error = strings.TrimSpace(err.Error())
			if job.Error == "" {
				job.Error = "query failed"
			}
			job.CompletedAt = &now
			job.mu.Unlock()
			r.afterTransition(job)
			return
		}
		if done {
			job.Result = cloneResult(result)
			job.Status = terminalStatusFromResult(result)
			if job.Status == JobStatusFailed && strings.TrimSpace(job.Error) == "" {
				job.Error = errorMessageFromResult(result)
			}
			job.CompletedAt = &now
			job.mu.Unlock()
			r.afterTransition(job)
			return
		}
		job.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-job.pollCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		wait = 800 * time.Millisecond
	}
}

// activeCountsLocked counts active LEGACY jobs only; managed jobs are
// governed by AdmissionConfig instead (see admission.go) so the two
// populations never starve each other during migration.
func (r *Registry) activeCountsLocked(threadID string) (int, int) {
	threadID = strings.TrimSpace(threadID)
	threadCount := 0
	globalCount := 0
	for _, job := range r.jobs {
		if job == nil {
			continue
		}
		job.mu.RLock()
		active := !isTerminal(job.Status) && !job.managed
		ownedByThread := strings.TrimSpace(job.Owner.ThreadID) == threadID
		job.mu.RUnlock()
		if !active {
			continue
		}
		globalCount++
		if ownedByThread {
			threadCount++
		}
	}
	return threadCount, globalCount
}

func (r *Registry) CancelActiveByThread(threadID string) []string {
	if r == nil {
		return nil
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil
	}

	type cancelTarget struct {
		id    string
		owner JobOwner
	}
	targets := make([]cancelTarget, 0, 8)

	r.mu.RLock()
	for _, job := range r.jobs {
		if job == nil {
			continue
		}
		job.mu.RLock()
		active := !isTerminal(job.Status)
		jobID := strings.TrimSpace(job.ID)
		ownerThreadID := strings.TrimSpace(job.Owner.ThreadID)
		ownerHandle := strings.TrimSpace(job.Owner.Handle)
		survivesSweep := job.managed && job.KeepOnRunCompletion
		job.mu.RUnlock()

		// Managed long-horizon jobs are decoupled from run lifetime:
		// run completion/stop must never cancel them (invariant I5).
		if survivesSweep {
			continue
		}
		if !active || jobID == "" || ownerThreadID != threadID || ownerHandle == "" {
			continue
		}
		targets = append(targets, cancelTarget{
			id: jobID,
			owner: JobOwner{
				ThreadID: threadID,
				Handle:   ownerHandle,
			},
		})
	}
	r.mu.RUnlock()

	if len(targets) == 0 {
		return nil
	}

	seen := map[string]struct{}{}
	cancelled := make([]string, 0, len(targets))
	for _, target := range targets {
		if _, ok := seen[target.id]; ok {
			continue
		}
		seen[target.id] = struct{}{}
		_ = r.Cancel(target.id, target.owner)
		snap, ok := r.Snapshot(target.id, target.owner)
		if ok && snap.Status == JobStatusCancelled {
			cancelled = append(cancelled, target.id)
		}
	}
	if len(cancelled) == 0 {
		return nil
	}
	sort.Strings(cancelled)
	return cancelled
}

func terminalStatusFromResult(result *QueryResult) JobStatus {
	if result == nil {
		return JobStatusCompleted
	}
	if result.Metadata != nil {
		if term := strings.TrimSpace(strings.ToLower(fmt.Sprint(result.Metadata["terminal_status"]))); term != "" {
			switch JobStatus(term) {
			case JobStatusCompleted, JobStatusFailed, JobStatusCancelled:
				return JobStatus(term)
			}
		}
		state := strings.TrimSpace(strings.ToLower(fmt.Sprint(result.Metadata["state"])))
		if strings.Contains(state, "cancel") {
			return JobStatusCancelled
		}
		if strings.Contains(state, "fail") || strings.Contains(state, "error") {
			return JobStatusFailed
		}
	}
	return JobStatusCompleted
}

func errorMessageFromResult(result *QueryResult) string {
	if result == nil || result.Metadata == nil {
		return "query failed"
	}
	for _, key := range []string{"error", "message"} {
		if value := strings.TrimSpace(fmt.Sprint(result.Metadata[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return "query failed"
}

func isTerminal(status JobStatus) bool {
	return isTerminalStatus(status)
}

func newJobID(provider string) (string, error) {
	prefix := "job"
	switch strings.TrimSpace(strings.ToLower(provider)) {
	case "bigquery":
		prefix = "bq"
	case "databricks":
		prefix = "dbx"
	case "trino":
		prefix = "tri"
	case "snowflake":
		prefix = "sf"
	}
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "qj-" + prefix + "-" + hex.EncodeToString(b), nil
}

func cloneJob(src *Job) *Job {
	if src == nil {
		return nil
	}
	src.mu.RLock()
	defer src.mu.RUnlock()
	return cloneJobLocked(src)
}

func cloneJobLocked(src *Job) *Job {
	if src == nil {
		return nil
	}
	out := &Job{
		ID:                src.ID,
		Provider:          src.Provider,
		SQL:               src.SQL,
		SQLPreview:        src.SQLPreview,
		Status:            src.Status,
		SubmittedAt:       src.SubmittedAt,
		RemoteSubmittedAt: cloneTimePtr(src.RemoteSubmittedAt),
		NextPollAt:        cloneTimePtr(src.NextPollAt),
		NextPollAction:    src.NextPollAction,
		LastAccess:        src.LastAccess,
		Result:            cloneResult(src.Result),
		Error:             src.Error,
		Metadata:          cloneMap(src.Metadata),
		Owner:             src.Owner,

		ConnectionID:        src.ConnectionID,
		Handle:              cloneHandle(src.Handle),
		IdempotencyKey:      src.IdempotencyKey,
		ReplacesJobID:       src.ReplacesJobID,
		KeepOnRunCompletion: src.KeepOnRunCompletion,
		Budgets:             src.Budgets,
		MonitorHealth:       src.MonitorHealth,
		PolicyHealth:        src.PolicyHealth,
		Materialization:     src.Materialization,
		CancelReason:        src.CancelReason,
		CancelOutcome:       src.CancelOutcome,
		CancelStartedAt:     cloneTimePtr(src.CancelStartedAt),
		ArtifactID:          src.ArtifactID,
		DrainSeq:            src.DrainSeq,
		DrainRowsCommitted:  src.DrainRowsCommitted,
		DrainBytesCommitted: src.DrainBytesCommitted,
		DrainTruncated:      src.DrainTruncated,
		SubmitDispatched:    src.SubmitDispatched,
		Validation:          src.Validation,
		ValidationDetail:    src.ValidationDetail,
		Profile:             cloneQueryProfile(src.Profile),
		ProfileOutcome:      src.ProfileOutcome,
		ProfileHandle:       cloneHandle(src.ProfileHandle),
		ProfilePhase:        src.ProfilePhase,
		ProfileDispatched:   src.ProfileDispatched,
	}
	if src.CompletedAt != nil {
		t := *src.CompletedAt
		out.CompletedAt = &t
	}
	if src.StartedAt != nil {
		t := *src.StartedAt
		out.StartedAt = &t
	}
	if len(src.WakeEvents) > 0 {
		out.WakeEvents = append([]WakeEvent(nil), src.WakeEvents...)
	}
	return out
}

func cloneTimePtr(src *time.Time) *time.Time {
	if src == nil {
		return nil
	}
	t := *src
	return &t
}

func cloneHandle(h NativeHandle) NativeHandle {
	out := h
	if h.Extra != nil {
		extra := make(map[string]string, len(h.Extra))
		for k, v := range h.Extra {
			extra[k] = v
		}
		out.Extra = extra
	}
	return out
}

func cloneResult(src *QueryResult) *QueryResult {
	if src == nil {
		return nil
	}
	out := &QueryResult{
		Schema:   make([]map[string]any, 0, len(src.Schema)),
		Rows:     make([]map[string]any, 0, len(src.Rows)),
		Metadata: cloneMap(src.Metadata),
	}
	for _, col := range src.Schema {
		out.Schema = append(out.Schema, cloneMap(col))
	}
	for _, row := range src.Rows {
		out.Rows = append(out.Rows, cloneMap(row))
	}
	return out
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}
