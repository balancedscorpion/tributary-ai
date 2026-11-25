package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/jobs"
)

const (
	RunStatusQueued          = "queued"
	RunStatusRunning         = "running"
	RunStatusWaitingExternal = "waiting_external"
	RunStatusResumePending   = "resume_pending"
	RunStatusResuming        = "resuming"
	RunStatusWaitingUser     = "waiting_user"
	RunStatusCompleted       = "completed"
	RunStatusFailed          = "failed"
	RunStatusCancelled       = "cancelled"

	// legacyRunningRecoveryAge is a conservative compatibility fence for runs
	// created before initial model executions acquired durable leases. A
	// running legacy row is recoverable only after this much durable inactivity
	// and only when required query work is confirmed by the manager.
	legacyRunningRecoveryAge = 15 * time.Minute
	// queuedRunRecoveryAge protects the short atomic-finalization/message/claim
	// handoff while still making a process death before initial lease acquisition
	// recoverable without a new client replay.
	queuedRunRecoveryAge = 2 * time.Minute
)

func queryResultChunksSemanticallyEqual(left, right QueryResultChunk) bool {
	left, leftErr := normalizeQueryResultChunk(left)
	right, rightErr := normalizeQueryResultChunk(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	left.CreatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && jsonSemanticallyEqual(leftJSON, rightJSON)
}

func nativeHandlesSemanticallyEqual(left, right jobs.NativeHandle) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && jsonSemanticallyEqual(leftJSON, rightJSON)
}

func continuationCheckpointsSemanticallyEqual(left, right ContinuationCheckpoint) bool {
	left = cloneContinuationCheckpoint(left)
	right = cloneContinuationCheckpoint(right)
	left.CreatedAt = time.Time{}
	right.CreatedAt = time.Time{}
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && jsonSemanticallyEqual(leftJSON, rightJSON)
}

// LongHorizonRepository is deliberately adjunct to Store. Callers must obtain
// it from the concrete base chat store; wrappers such as search.IndexedStore
// are not expected to forward this lifecycle contract.
type LongHorizonRepository interface {
	jobs.JobStore
	jobs.RevisionedStore
	jobs.LeaseStore
	jobs.RevisionedWakeClaimStore
	jobs.ValidationStore
	jobs.FencedProfileStore
	jobs.CapacityReserver
	jobs.DrainCommitter
	jobs.FencedDrainCommitter
	jobs.QueryObligationBinder
	GetQueryJobByIdempotency(ctx context.Context, provider, connectionKey, key string) (jobs.JobRecord, bool, error)
	RunContinuationRepository
	QueryObservationRepository
}

func queryProfileTransition(status jobs.JobStatus) (terminal bool, valid bool) {
	switch status {
	case jobs.JobStatusReserved:
		return false, true
	case jobs.JobStatusFailed, jobs.JobStatusCancelled, jobs.JobStatusExpired, jobs.JobStatusOrphaned:
		return true, true
	default:
		return false, false
	}
}

func validateDrainCommitAccounting(commit jobs.DrainCommit, current jobs.JobRecord) error {
	rowsAdded := int64(len(commit.Page.Rows))
	if commit.Page.Bytes < 0 || current.DrainRowsCommitted < 0 || current.DrainBytesCommitted < 0 ||
		commit.DrainRowsCommitted < 0 || commit.DrainBytesCommitted < 0 {
		return errors.New("drain accounting cannot be negative")
	}
	if commit.Record.DrainRowsCommitted != commit.DrainRowsCommitted ||
		commit.Record.DrainBytesCommitted != commit.DrainBytesCommitted ||
		commit.Record.DrainTruncated != commit.DrainTruncated {
		return errors.New("drain commit record does not match cumulative accounting")
	}
	if current.DrainRowsCommitted > math.MaxInt64-rowsAdded || current.DrainBytesCommitted > math.MaxInt64-commit.Page.Bytes {
		return errors.New("drain accounting overflow")
	}
	wantRows := current.DrainRowsCommitted + rowsAdded
	wantBytes := current.DrainBytesCommitted + commit.Page.Bytes
	wantTruncated := current.DrainTruncated || commit.Page.Truncated
	if commit.DrainRowsCommitted != wantRows || commit.DrainBytesCommitted != wantBytes || commit.DrainTruncated != wantTruncated {
		return ErrConflict
	}
	return nil
}

func drainReplayAccountingMatches(commit jobs.DrainCommit, current jobs.JobRecord) bool {
	return current.DrainRowsCommitted == commit.DrainRowsCommitted &&
		current.DrainBytesCommitted == commit.DrainBytesCommitted &&
		current.DrainTruncated == commit.DrainTruncated
}

func isTerminalQueryExecutionState(status jobs.JobStatus) bool {
	switch status {
	case jobs.JobStatusCompleted, jobs.JobStatusFailed, jobs.JobStatusCancelled,
		jobs.JobStatusExpired, jobs.JobStatusOrphaned:
		return true
	default:
		return false
	}
}

type RunContinuationRepository interface {
	AppendContinuationCheckpoint(ctx context.Context, checkpoint ContinuationCheckpoint) (ContinuationCheckpoint, error)
	SuspendRun(ctx context.Context, mutation SuspendRunMutation) (ContinuationCheckpoint, Run, bool, error)
	LatestContinuationCheckpoint(ctx context.Context, runID string) (*ContinuationCheckpoint, error)
	ListRecoverableRuns(ctx context.Context, now time.Time, limit int) ([]Run, error)
	TransitionRunCAS(ctx context.Context, mutation RunStateMutation) (Run, bool, error)
	ClaimRunWake(ctx context.Context, runID string, expectedRevision int64, claimant string, now time.Time, leaseUntil time.Time) (Run, bool, error)
	UpdateClaimedRunMetadata(ctx context.Context, runID, claimant string, fields map[string]any) error
	RenewRunWakeLease(ctx context.Context, runID, claimant string, ttl time.Duration) (bool, error)
	ReleaseRunWake(ctx context.Context, runID string, claimant string, nextStatus string, wakeAt *time.Time) error
	UpsertRunQueryDependency(ctx context.Context, dependency RunQueryDependency) (RunQueryDependency, error)
	ReplaceRunQueryDependency(ctx context.Context, runID, replacedJobID string, replacement RunQueryDependency) (RunQueryDependency, error)
	ListRunQueryDependencies(ctx context.Context, runID string) ([]RunQueryDependency, error)
	DetachRunQueryDependencies(ctx context.Context, runID string, now time.Time) error
}

type QueryObservationRepository interface {
	AppendQueryJobEvent(ctx context.Context, event QueryJobEvent) (QueryJobEvent, error)
	ListQueryJobEvents(ctx context.Context, jobID string, afterSequence int64, limit int) ([]QueryJobEvent, error)
	AppendQueryResultChunk(ctx context.Context, chunk QueryResultChunk) error
	ListQueryResultChunks(ctx context.Context, jobID string, afterSequence int64, limit int) ([]QueryResultChunk, error)
}

type ContinuationCheckpoint struct {
	ID                    string          `json:"id"`
	RunID                 string          `json:"run_id"`
	ThreadID              string          `json:"thread_id"`
	Sequence              int64           `json:"sequence"`
	ToolBatchSequence     int64           `json:"tool_batch_sequence"`
	HistoryJSON           json.RawMessage `json:"history_json"`
	ProviderStateJSON     json.RawMessage `json:"provider_state_json,omitempty"`
	ExecutionSnapshotJSON json.RawMessage `json:"execution_snapshot_json,omitempty"`
	DependencyIDs         []string        `json:"dependency_ids"`
	CreatedAt             time.Time       `json:"created_at"`
}

type RunStateMutation struct {
	RunID                     string
	ExpectedRevision          int64
	ExpectedStatuses          []string
	Status                    string
	ContinuationCount         *int
	MaxAutoContinuations      *int
	LastCheckpointSequence    *int64
	WakeAt                    *time.Time
	ClearWakeAt               bool
	ClearResumeLease          bool
	ExpectedResumeLeaseHolder string
}

// SuspendRunMutation is committed as one durable unit: the immutable model
// checkpoint and the run's transition to waiting_external become visible
// together after recovery. ExpectedStatuses is normally running or resuming;
// callers may also include resume_pending when completing a recovered handoff.
type SuspendRunMutation struct {
	Checkpoint                ContinuationCheckpoint
	ExpectedRevision          int64
	ExpectedStatuses          []string
	ExpectedResumeLeaseHolder string
	ContinuationCount         int
	MaxAutoContinuations      int
	WakeAt                    time.Time
}

func normalizeSuspendRunMutation(mutation SuspendRunMutation) (SuspendRunMutation, error) {
	if strings.TrimSpace(mutation.Checkpoint.ID) == "" {
		return SuspendRunMutation{}, errors.New("suspend run requires a stable checkpoint id")
	}
	checkpoint, err := normalizeContinuationCheckpoint(mutation.Checkpoint)
	if err != nil {
		return SuspendRunMutation{}, err
	}
	mutation.Checkpoint = checkpoint
	mutation.ExpectedResumeLeaseHolder = strings.TrimSpace(mutation.ExpectedResumeLeaseHolder)
	if mutation.ExpectedRevision < 0 || mutation.ContinuationCount < 0 || mutation.MaxAutoContinuations <= 0 {
		return SuspendRunMutation{}, errors.New("suspend run requires a valid revision and continuation counters")
	}
	if mutation.ContinuationCount > mutation.MaxAutoContinuations {
		return SuspendRunMutation{}, errors.New("continuation count exceeds the automatic continuation limit")
	}
	if mutation.WakeAt.IsZero() {
		return SuspendRunMutation{}, errors.New("suspend run wake deadline is required")
	}
	mutation.WakeAt = mutation.WakeAt.UTC()
	if len(mutation.ExpectedStatuses) == 0 {
		mutation.ExpectedStatuses = []string{RunStatusRunning, RunStatusResuming, RunStatusResumePending}
	}
	seen := make(map[string]bool, len(mutation.ExpectedStatuses))
	statuses := make([]string, 0, len(mutation.ExpectedStatuses))
	for _, status := range mutation.ExpectedStatuses {
		status = strings.TrimSpace(status)
		if !validateRunStatus(status) {
			return SuspendRunMutation{}, errors.New("suspend run has an invalid expected status")
		}
		if !seen[status] {
			seen[status] = true
			statuses = append(statuses, status)
		}
	}
	mutation.ExpectedStatuses = statuses
	return mutation, nil
}

func runMatchesSuspension(run Run, mutation SuspendRunMutation, checkpoint ContinuationCheckpoint) bool {
	if run.Status != RunStatusWaitingExternal || run.Revision != mutation.ExpectedRevision+1 ||
		run.ContinuationCount != mutation.ContinuationCount ||
		run.MaxAutoContinuations != mutation.MaxAutoContinuations ||
		run.LastCheckpointSequence != checkpoint.Sequence ||
		run.ResumeLeaseHolder != "" || run.ResumeLeaseExpiresAt != nil || run.WakeAt == nil {
		return false
	}
	return run.WakeAt.Equal(mutation.WakeAt)
}

func dependencyReplacementEligible(dependency RunQueryDependency, record jobs.JobRecord) bool {
	if dependency.ValidationState == QueryValidationRejected {
		return true
	}
	switch record.Status {
	case jobs.JobStatusFailed, jobs.JobStatusCancelled, jobs.JobStatusExpired, jobs.JobStatusOrphaned:
		return true
	default:
		return false
	}
}

func normalizeQueryIdempotencyIdentity(provider, connectionKey, key string) (string, string, string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	connectionKey = strings.ToLower(strings.TrimSpace(connectionKey))
	key = strings.TrimSpace(key)
	if provider == "" || key == "" {
		return "", "", "", errors.New("query provider and idempotency key are required")
	}
	if connectionKey == "" {
		connectionKey = provider
	}
	return provider, connectionKey, key, nil
}

// freezeQueryJobIdentity validates the immutable coordinates of an existing
// durable query job and copies their authoritative stored representation onto
// the proposed lifecycle update. Whole-record lifecycle writes are convenient,
// but they must never be able to move a job to another run, rewrite retry
// lineage, or change the provider request protected by its idempotency key.
func freezeQueryJobIdentity(current jobs.JobRecord, proposed *jobs.JobRecord) error {
	if proposed == nil {
		return fmt.Errorf("nil proposed query job: %w", ErrConflict)
	}
	providerIdentity := func(value string) string {
		return strings.ToLower(strings.TrimSpace(value))
	}
	connectionIdentity := func(value string) string {
		return strings.TrimSpace(value)
	}
	field := ""
	switch {
	case current.ID != proposed.ID:
		field = "id"
	case providerIdentity(current.Provider) != providerIdentity(proposed.Provider):
		field = "provider"
	case connectionIdentity(current.ConnectionID) != connectionIdentity(proposed.ConnectionID):
		field = "connection_id"
	case current.SQL != proposed.SQL:
		field = "sql"
	case current.SQLPreview != proposed.SQLPreview:
		field = "sql_preview"
	case current.Owner.ThreadID != proposed.Owner.ThreadID:
		field = "owner.thread_id"
	// A blank durable run id is authoritative. MySQL intentionally clears it
	// via ON DELETE SET NULL because jobs outlive runs; an in-flight monitor may
	// still carry the deleted run id in memory. Preserve the durable blank so a
	// later lifecycle write neither reattaches the job nor trips the FK. A
	// never-persisted job may still receive its one-time AttachOwner run before
	// its initial insert.
	case strings.TrimSpace(current.Owner.RunID) != "" && current.Owner.RunID != proposed.Owner.RunID:
		field = "owner.run_id"
	case current.Owner.UserToken != proposed.Owner.UserToken:
		field = "owner.user_token"
	case current.Owner.Handle != proposed.Owner.Handle:
		field = "owner.handle"
	case current.IdempotencyKey != proposed.IdempotencyKey:
		field = "idempotency_key"
	case current.ReplacesJobID != proposed.ReplacesJobID:
		field = "replaces_job_id"
	}
	if field != "" {
		return fmt.Errorf("query job %s immutable %s does not match durable identity: %w", current.ID, field, ErrConflict)
	}

	// Preserve the exact durable representation even when a semantically
	// equivalent provider/connection spelling was supplied.
	proposed.ID = current.ID
	proposed.Provider = current.Provider
	proposed.ConnectionID = current.ConnectionID
	proposed.SQL = current.SQL
	proposed.SQLPreview = current.SQLPreview
	proposed.Owner = current.Owner
	proposed.IdempotencyKey = current.IdempotencyKey
	proposed.ReplacesJobID = current.ReplacesJobID
	// This recovery-safety marker is monotonic. A stale whole-record writer may
	// update ordinary metadata, but must not turn a managed query into a generic
	// job that recovery would resume without rebinding its run obligation.
	if jobs.RequiresQueryObligationBinding(current) {
		metadata := make(map[string]any, len(proposed.Metadata)+1)
		for key, value := range proposed.Metadata {
			metadata[key] = value
		}
		metadata[jobs.QueryObligationManagedMetadataKey] = true
		proposed.Metadata = metadata
	}
	return nil
}

type RunQueryDependency struct {
	RunID                 string `json:"run_id"`
	JobID                 string `json:"job_id"`
	RequiredForCompletion bool   `json:"required_for_completion"`
	Detached              bool   `json:"detached"`
	ValidationState       string `json:"validation_state"`
	// ObligationID is the opaque, run-scoped stable identity of the retry
	// chain. Every attempt row in one chain carries the same id; the single
	// non-detached row is the obligation's active attempt. Legacy rows may have
	// it empty and are upgraded lazily on the next write.
	ObligationID     string    `json:"query_obligation_id,omitempty"`
	ReplacementJobID string    `json:"replacement_job_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// QueryObligationIDPrefix marks a model-facing stable retry handle. The
// opaque suffix is a run-scoped digest; it never embeds a provider or job id.
const QueryObligationIDPrefix = "qob-"

const queryObligationDigestHexLength = 32

// ValidateQueryObligationID strictly validates the public obligation
// namespace. Provider-native ids, app job ids, UUIDs, blank/null-derived
// values, uppercase variants and the all-zero sentinel are rejected.
func ValidateQueryObligationID(obligationID string) error {
	value := strings.TrimSpace(obligationID)
	if len(value) != len(QueryObligationIDPrefix)+queryObligationDigestHexLength ||
		!strings.HasPrefix(value, QueryObligationIDPrefix) {
		return errors.New("query_obligation_id must match qob- followed by 32 lowercase hexadecimal characters")
	}
	suffix := strings.TrimPrefix(value, QueryObligationIDPrefix)
	allZero := true
	for _, char := range suffix {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return errors.New("query_obligation_id must match qob- followed by 32 lowercase hexadecimal characters")
		}
		if char != '0' {
			allZero = false
		}
	}
	if allZero {
		return errors.New("query_obligation_id must not be the all-zero sentinel")
	}
	return nil
}

// QueryObligationIDForRoot derives an opaque, deterministic handle scoped to
// one run and one replacement-chain root.
func QueryObligationIDForRoot(runID, rootJobID string) (string, error) {
	runID = strings.TrimSpace(runID)
	rootJobID = strings.TrimSpace(rootJobID)
	if runID == "" || rootJobID == "" {
		return "", errors.New("query obligation derivation requires run and root job ids")
	}
	sum := sha256.Sum256([]byte("run-query-obligation/v1\x00" + runID + "\x00" + rootJobID))
	return QueryObligationIDPrefix + hex.EncodeToString(sum[:16]), nil
}

type queryJobReader interface {
	GetQueryJob(ctx context.Context, jobID string) (jobs.JobRecord, bool, error)
}

// DeriveQueryObligationID computes the deterministic, run-scoped obligation
// id for a query job by walking its replaces_job_id chain to the durable root.
// Missing ancestors, cycles, excessive depth, and read errors fail closed:
// treating a currently-missing id as the root would mint an identity that can
// change if that record later appears with its own predecessor.
func DeriveQueryObligationID(ctx context.Context, reader queryJobReader, runID, jobID string) (string, error) {
	runID = strings.TrimSpace(runID)
	id := strings.TrimSpace(jobID)
	if runID == "" || id == "" {
		return "", errors.New("query obligation derivation requires run and job ids")
	}
	if jobs.IsQueryJobIDSentinel(id) {
		return "", fmt.Errorf("query obligation cannot use sentinel job id %q: %w", id, ErrConflict)
	}
	if strings.HasPrefix(strings.ToLower(id), jobs.ManagedQueryJobIDPrefix) && !jobs.IsValidManagedQueryJobID(id) {
		return "", fmt.Errorf("query obligation cannot use malformed managed job id %q: %w", id, ErrConflict)
	}
	if reader == nil {
		return QueryObligationIDForRoot(runID, id)
	}
	seen := make(map[string]bool, 4)
	for hops := 0; hops < 64; hops++ {
		if seen[id] {
			return "", fmt.Errorf("query replacement lineage contains a cycle at %s: %w", id, ErrConflict)
		}
		seen[id] = true
		record, ok, err := reader.GetQueryJob(ctx, id)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("query replacement lineage is missing job %s: %w", id, ErrConflict)
		}
		parent := strings.TrimSpace(record.ReplacesJobID)
		if parent == "" || jobs.IsQueryJobIDSentinel(parent) {
			break
		}
		if strings.HasPrefix(strings.ToLower(parent), jobs.ManagedQueryJobIDPrefix) && !jobs.IsValidManagedQueryJobID(parent) {
			return "", fmt.Errorf("query replacement lineage contains malformed managed id %q: %w", parent, ErrConflict)
		}
		if seen[parent] {
			return "", fmt.Errorf("query replacement lineage contains a cycle at %s: %w", parent, ErrConflict)
		}
		id = parent
		if hops == 63 {
			return "", fmt.Errorf("query replacement lineage exceeds 64 attempts: %w", ErrConflict)
		}
	}
	return QueryObligationIDForRoot(runID, id)
}

func queryDependencyValidationState(record jobs.JobRecord) string {
	state := strings.TrimSpace(string(record.ValidationState))
	if state == "" {
		state = QueryValidationUnvalidated
	}
	if record.ProfileOutcome != jobs.ProfileOutcomeNone && record.ProfileOutcome != jobs.ProfileOutcomeAccepted {
		state = QueryValidationRejected
	}
	return state
}

type queryObligationBindingRepository interface {
	queryJobReader
	UpsertRunQueryDependency(context.Context, RunQueryDependency) (RunQueryDependency, error)
	ReplaceRunQueryDependency(context.Context, string, string, RunQueryDependency) (RunQueryDependency, error)
}

type queryObligationDependencyReader interface {
	ListRunQueryDependencies(context.Context, string) ([]RunQueryDependency, error)
}

func bindQueryObligation(ctx context.Context, repository queryObligationBindingRepository, jobID string) error {
	jobID = strings.TrimSpace(jobID)
	if repository == nil || jobID == "" {
		return errors.New("query obligation binding requires a durable job id")
	}
	record, found, err := repository.GetQueryJob(ctx, jobID)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	runID := strings.TrimSpace(record.Owner.RunID)
	if runID == "" {
		// Query jobs outlive runs. Once the owning run (and its dependency
		// ledger) is deleted, MySQL clears run_id and recovery must continue
		// monitoring the remote work rather than deferring it forever.
		return nil
	}
	replacedJobID := strings.TrimSpace(record.ReplacesJobID)
	// A dependency ledger may predate an internal identity repair. Its opaque
	// obligation id is the stable model-facing handle and must survive that
	// repair even though deriving from the repaired concrete job id would yield
	// a different digest. Prefer the exact durable row when one already exists.
	obligationID := ""
	if reader, ok := repository.(queryObligationDependencyReader); ok {
		dependencies, listErr := reader.ListRunQueryDependencies(ctx, runID)
		if listErr != nil {
			return listErr
		}
		for _, dependency := range dependencies {
			matchesCurrent := dependency.JobID == jobID
			matchesPredecessor := obligationID == "" && replacedJobID != "" && dependency.JobID == replacedJobID
			if (!matchesCurrent && !matchesPredecessor) || strings.TrimSpace(dependency.ObligationID) == "" {
				continue
			}
			if err := ValidateQueryObligationID(dependency.ObligationID); err != nil {
				return err
			}
			obligationID = dependency.ObligationID
			break
		}
	}
	if obligationID == "" {
		var err error
		obligationID, err = DeriveQueryObligationID(ctx, repository, runID, jobID)
		if err != nil {
			return err
		}
	}
	now := nowUTC()
	dependency := RunQueryDependency{
		RunID:                 runID,
		JobID:                 jobID,
		RequiredForCompletion: true,
		ValidationState:       queryDependencyValidationState(record),
		ObligationID:          obligationID,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	if replacedJobID == "" {
		_, err = repository.UpsertRunQueryDependency(ctx, dependency)
		return err
	}
	if replacedJobID == jobID {
		return fmt.Errorf("query job %s replaces itself: %w", jobID, ErrConflict)
	}
	bound, err := repository.ReplaceRunQueryDependency(ctx, runID, replacedJobID, dependency)
	if err != nil {
		return err
	}
	// A replay after profiling/validation may carry a more recent validation
	// axis than the original pre-profile bind. The hardened upsert updates
	// that active row without ever reactivating a detached attempt.
	if bound.ValidationState != dependency.ValidationState {
		bound.ValidationState = dependency.ValidationState
		bound.UpdatedAt = now
		_, err = repository.UpsertRunQueryDependency(ctx, bound)
	}
	return err
}

type QueryJobEvent struct {
	JobID          string          `json:"job_id"`
	Sequence       int64           `json:"sequence"`
	Type           string          `json:"type"`
	WakeKind       jobs.WakeKind   `json:"wake_kind,omitempty"`
	Detail         string          `json:"detail,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	ObservationKey string          `json:"observation_key"`
	CreatedAt      time.Time       `json:"created_at"`
	ClaimedBy      string          `json:"claimed_by,omitempty"`
	ClaimedAt      *time.Time      `json:"claimed_at,omitempty"`
}

type QueryResultChunk struct {
	JobID           string            `json:"job_id"`
	Sequence        int64             `json:"sequence"`
	Page            jobs.ResultPage   `json:"page"`
	CursorAfter     jobs.NativeHandle `json:"cursor_after,omitempty"`
	NativePageToken string            `json:"native_page_token,omitempty"`
	RowCount        int               `json:"row_count"`
	ByteCount       int64             `json:"byte_count"`
	Truncated       bool              `json:"truncated"`
	CreatedAt       time.Time         `json:"created_at"`
}

const (
	QueryValidationUnvalidated = string(jobs.ValidationUnvalidated)
	QueryValidationValidating  = string(jobs.ValidationValidating)
	QueryValidationValidated   = string(jobs.ValidationValidated)
	QueryValidationRejected    = string(jobs.ValidationRejected)
)
