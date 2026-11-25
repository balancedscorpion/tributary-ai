package jobs

import (
	"context"
	"errors"
	"time"
)

// ProgressSignal names a telemetry signal a provider genuinely reports.
// Stall detection only trusts signals declared here.
type ProgressSignal string

const (
	SignalBytesProcessed ProgressSignal = "bytes_processed"
	SignalRowsProcessed  ProgressSignal = "rows_processed"
	SignalSplits         ProgressSignal = "splits"
	SignalNativePercent  ProgressSignal = "native_percent"
	SignalStageInfo      ProgressSignal = "stage_info"
)

// ProviderCapabilities declares what a provider adapter can honestly do.
// The registry never assumes a capability that is not declared.
type ProviderCapabilities struct {
	// IdempotentSubmit: Submit with the same IdempotencyKey is safe to
	// retry without double-executing (BigQuery requestId/jobId, Snowflake
	// requestId+retry). When false the registry never re-submits.
	IdempotentSubmit bool
	// DetachedRepoll: the native handle can be polled/fetched from a fresh
	// process (BigQuery, Databricks, Snowflake). Enables restart adoption.
	DetachedRepoll bool
	// RequiresContinuousDrain: results must be consumed as a live stream
	// and unfetched cursors expire server-side (Trino). The monitor treats
	// polling as a keepalive and materializes chunks incrementally.
	RequiresContinuousDrain bool
	// NativePagination: Fetch supports provider-native continuation.
	NativePagination bool
	// QueuedStateVisible: Poll can distinguish queued from executing.
	QueuedStateVisible bool
	// CancelConfirmable: ConfirmCancel can verify terminal state.
	CancelConfirmable bool
	// MaxSafePollInterval bounds monitor cadence when the protocol makes
	// polling load-bearing (Trino client timeout). Zero means unbounded.
	MaxSafePollInterval time.Duration
	// ProfileBound, when > 0, requests a SHORTER total profile deadline
	// than the registry's ProfilePolicy (e.g. Trino's three-statement
	// profile). It can only tighten the bound, never extend it, and no
	// profile ever inherits the query's hard execution budget.
	ProfileBound time.Duration
	// ProgressSignals the provider actually reports.
	ProgressSignals []ProgressSignal
}

func (c ProviderCapabilities) hasSignal(s ProgressSignal) bool {
	for _, sig := range c.ProgressSignals {
		if sig == s {
			return true
		}
	}
	return false
}

// NativeHandle is the serializable provider-side identity of a query.
// It is first-class and persisted; together with locally stored
// connection settings it must be sufficient for Poll/Fetch/Cancel/Adopt
// on providers with DetachedRepoll.
type NativeHandle struct {
	Provider string            `json:"provider"`
	ID       string            `json:"id"`                 // job/statement/query id
	Location string            `json:"location,omitempty"` // BQ location, host, account
	Extra    map[string]string `json:"extra,omitempty"`    // nextUri, project_id, ...
}

func (h NativeHandle) IsZero() bool { return h.ID == "" }

// ProviderState is the normalized execution state reported by a provider.
type ProviderState string

const (
	ProviderStateUnknown   ProviderState = "unknown"
	ProviderStatePending   ProviderState = "pending"
	ProviderStateQueued    ProviderState = "queued"
	ProviderStateRunning   ProviderState = "running"
	ProviderStateSucceeded ProviderState = "succeeded"
	ProviderStateFailed    ProviderState = "failed"
	ProviderStateCancelled ProviderState = "cancelled"
)

// StatusSnapshot is a single observation of the remote job. Telemetry is
// pass-through: fields are set only when the provider reports them.
// NativeProgressPercent in particular must never be synthesized.
type StatusSnapshot struct {
	State    ProviderState `json:"state"`
	RawState string        `json:"raw_state,omitempty"`
	Error    string        `json:"error,omitempty"`

	BytesProcessed        *int64   `json:"bytes_processed,omitempty"`
	RowsProcessed         *int64   `json:"rows_processed,omitempty"`
	SplitsDone            *int64   `json:"splits_done,omitempty"`
	SplitsTotal           *int64   `json:"splits_total,omitempty"`
	NativeProgressPercent *float64 `json:"native_progress_percent,omitempty"`

	// Handle updates (e.g. Trino's advancing nextUri) observed during poll.
	UpdatedHandle *NativeHandle `json:"updated_handle,omitempty"`
	// Pages drained during this poll for RequiresContinuousDrain providers.
	DrainedPages []ResultPage `json:"-"`

	Telemetry map[string]any `json:"telemetry,omitempty"`

	// RetryAfter is an optional provider-directed minimum delay before the
	// next observation. It is transport pacing, not execution state: callers
	// combine it with their exponential backoff and the provider's maximum
	// safe cursor interval. Providers should populate it from authoritative
	// Retry-After / backpressure signals only.
	RetryAfter time.Duration `json:"retry_after,omitempty"`
}

// RetryAction identifies the provider operation whose durable backpressure
// deadline must be honored after restart. Initial cancellation may bypass a
// stale ordinary-poll deadline, but must not bypass a 429 from Cancel or
// ConfirmCancel itself.
type RetryAction string

const (
	RetryActionPoll          RetryAction = "poll"
	RetryActionCancel        RetryAction = "cancel"
	RetryActionConfirmCancel RetryAction = "confirm_cancel"
	RetryActionReconcile     RetryAction = "reconcile"
)

func (s StatusSnapshot) terminal() bool {
	switch s.State {
	case ProviderStateSucceeded, ProviderStateFailed, ProviderStateCancelled:
		return true
	default:
		return false
	}
}

// SubmitRequest carries everything an adapter needs to submit. Budgets map
// onto provider-side caps (maximumBytesBilled, byte_limit, statement
// timeout, session max execution time) where supported.
type SubmitRequest struct {
	SQL            string
	ConnectionID   string
	IdempotencyKey string
	Budgets        Budgets
	Session        map[string]string
	Metadata       map[string]any
}

// FetchCursor is a provider-native (or spill-backed) continuation. It must
// never cause SQL re-execution.
type FetchCursor struct {
	PageToken string
	MaxRows   int
	MaxBytes  int64
}

// ResultPage is one page/chunk of results.
type ResultPage struct {
	Rows          []map[string]any
	Schema        []map[string]any
	NextPageToken string
	Truncated     bool
	Bytes         int64
	Metadata      map[string]any
	// CursorAfter is the native cursor positioned AFTER this page was
	// consumed (e.g. Trino's next nextUri). RequiresContinuousDrain
	// adapters must set it on every drained page so each observation can
	// commit chunk + cursor atomically.
	CursorAfter *NativeHandle
}

// AdoptOutcome reports what restart adoption found.
type AdoptOutcome string

const (
	AdoptOutcomeAdopted  AdoptOutcome = "adopted"  // handle live; monitoring resumed
	AdoptOutcomeTerminal AdoptOutcome = "terminal" // finished while we were away
	AdoptOutcomeOrphaned AdoptOutcome = "orphaned" // handle dead/expired/unreachable
)

// SubmitResult is what a submit call actually produced. Some providers'
// submit responses are already the FIRST OBSERVATION of the query: Trino's
// initial POST page can carry rows, columns, warnings, protocol/session
// state, stats, and the first nextUri; Databricks with a wait_timeout can
// return an already-terminal statement. Initial carries that observation
// so the launch path can commit it atomically (bounded page + post-page
// cursor + DrainSeq + record revision) BEFORE monitoring starts — putting
// pages into NativeHandle.Extra or replaying them from Poll would break
// the atomic-observation contract (lost rows or duplicate replay).
//
// Providers whose submit returns only an identifier leave Initial nil.
type SubmitResult struct {
	Handle NativeHandle
	// Initial, when set, is processed exactly like a Poll observation:
	// State may be non-terminal or terminal; DrainedPages (each with
	// CursorAfter) carry any rows returned inline; UpdatedHandle carries
	// advanced protocol/cursor state beyond Handle. For a NON-DRAIN terminal
	// observation, DrainedPages are result pages already consumed by that
	// observation. The registry appends them exactly once; when the final page
	// carries NextPageToken it continues Fetch from that token rather than
	// restarting at the first provider page. A page with no continuation is a
	// complete inline result and causes no provider Fetch call.
	Initial *StatusSnapshot
}

// ProviderAdapter is implemented once per provider. All methods take the
// serializable NativeHandle so they work on adopted jobs after restart.
type ProviderAdapter interface {
	Provider() string
	Capabilities() ProviderCapabilities
	Submit(ctx context.Context, req SubmitRequest) (SubmitResult, error)
	Poll(ctx context.Context, h NativeHandle) (StatusSnapshot, error)
	Fetch(ctx context.Context, h NativeHandle, cursor FetchCursor) (ResultPage, error)
	Cancel(ctx context.Context, h NativeHandle) error
	// ConfirmCancel verifies the provider reached a terminal state after
	// Cancel. It returns the observed snapshot so completion can win.
	ConfirmCancel(ctx context.Context, h NativeHandle) (confirmed bool, snap StatusSnapshot, err error)
	// Adopt re-attaches to a persisted handle after restart.
	Adopt(ctx context.Context, h NativeHandle) (AdoptOutcome, StatusSnapshot, error)
}

// retryAfterError carries an authoritative provider retry delay alongside
// the original error. Keeping this typed avoids scraping error strings and
// lets the scheduler apply one pacing decision instead of multiplying native
// and registry sleeps.
type retryAfterError struct {
	err   error
	delay time.Duration
}

func (e *retryAfterError) Error() string { return e.err.Error() }
func (e *retryAfterError) Unwrap() error { return e.err }
func (e *retryAfterError) RetryAfter() time.Duration {
	if e == nil || e.delay < 0 {
		return 0
	}
	return e.delay
}

// WithRetryAfter annotates err with an authoritative provider pacing hint.
// Non-positive delays leave the error unchanged.
func WithRetryAfter(err error, delay time.Duration) error {
	if err == nil || delay <= 0 {
		return err
	}
	return &retryAfterError{err: err, delay: delay}
}

// RetryAfterFromError extracts the largest retry delay in an error chain.
func RetryAfterFromError(err error) time.Duration {
	var hinted interface{ RetryAfter() time.Duration }
	if errors.As(err, &hinted) {
		if delay := hinted.RetryAfter(); delay > 0 {
			return delay
		}
	}
	return 0
}

// permanentObservationLossError marks a Poll/Fetch failure that is
// DEFINITIVELY unrecoverable: the provider reports the observation cursor
// gone for good (Trino nextUri expired/404/410, results purged). Adapters
// wrap such errors with PermanentObservationLoss; the monitor then
// transitions the job to orphaned + recovery_required instead of retrying
// forever. Transient failures must never be wrapped.
type permanentObservationLossError struct{ err error }

func (e *permanentObservationLossError) Error() string { return e.err.Error() }
func (e *permanentObservationLossError) Unwrap() error { return e.err }

// PermanentObservationLoss asserts the observation can never be resumed.
func PermanentObservationLoss(err error) error {
	if err == nil {
		return nil
	}
	return &permanentObservationLossError{err: err}
}

// IsPermanentObservationLoss reports whether err carries the assertion.
func IsPermanentObservationLoss(err error) bool {
	var p *permanentObservationLossError
	return errors.As(err, &p)
}

// ReconcileResolution classifies what a SubmissionReconciler found.
type ReconcileResolution string

const (
	// ReconcileFound: the submission landed; Handle (and possibly a
	// terminal Snapshot) identify it.
	ReconcileFound ReconcileResolution = "found"
	// ReconcileNotSubmitted: the provider verifiably never received the
	// statement (safe hard failure).
	ReconcileNotSubmitted ReconcileResolution = "not_submitted"
	// ReconcileUnknown: still cannot tell; the registry keeps retrying
	// until the hard budget classifies the job orphaned.
	ReconcileUnknown ReconcileResolution = "unknown"
)

// ReconcileRequest carries the durable identity of an ambiguous submission.
// Adapters resolve it via provider-native lookup (BigQuery job id/labels,
// Snowflake requestId, Databricks statement listing, Trino client tags).
//
// Budgets are the job's EFFECTIVE budgets (post-defaulting — exactly what
// Submit received), so reconciliation can match provider-side settings
// derived from them (e.g. Snowflake's statement timeout from
// HardExecution) instead of assuming provider defaults.
type ReconcileRequest struct {
	JobID          string
	IdempotencyKey string
	SQL            string
	ConnectionID   string
	SubmittedAt    time.Time
	Budgets        Budgets
	Metadata       map[string]any
}

// ReconcileOutcome is a SubmissionReconciler verdict. Snapshot may be set
// alongside ReconcileFound when the statement already finished.
type ReconcileOutcome struct {
	Resolution ReconcileResolution
	Handle     NativeHandle
	Snapshot   *StatusSnapshot
	RetryAfter time.Duration
}

// SubmissionReconciler is an OPTIONAL adapter capability (checked by type
// assertion). When present, submission_unknown jobs get a durable
// reconciliation loop instead of waiting for a manual ReconcileSubmission.
// Implementations must never re-submit; they only look up.
type SubmissionReconciler interface {
	ReconcileSubmission(ctx context.Context, req ReconcileRequest) (ReconcileOutcome, error)
}

// QueryProfile is the uniform representation of provider dry-run/EXPLAIN
// output. Fields are set only when the provider genuinely reports them.
// EstimateIsExact must only be true when the value remains an exact execution
// bound; a BigQuery dry run is still an estimate because table state and the
// provider's execution-time estimate can change before submission.
type QueryProfile struct {
	Valid           bool           `json:"valid"`
	Error           string         `json:"error,omitempty"`
	EstimatedBytes  *int64         `json:"estimated_bytes,omitempty"`
	EstimatedRows   *int64         `json:"estimated_rows,omitempty"`
	EstimateIsExact bool           `json:"estimate_is_exact,omitempty"`
	PlanSummary     string         `json:"plan_summary,omitempty"`
	Warnings        []string       `json:"warnings,omitempty"`
	ProviderDetails map[string]any `json:"provider_details,omitempty"`
}

// ScanApprovalRequest is emitted after profiling identifies a query whose
// estimate is above the automatic scan limit but not over the hard ceiling
// (ApprovedScanBytes; 0 means no ceiling is configured and any estimate may
// be approved). The callback is deliberately runtime-only: the durable
// profile/job remains the source of truth, while the application's existing
// approval service owns the user decision.
type ScanApprovalRequest struct {
	JobID              string
	Provider           string
	ConnectionID       string
	SQLPreview         string
	EstimatedBytes     int64
	AutomaticScanBytes int64
	ApprovedScanBytes  int64
	// AuthorizedScanBytes is the proposed provider-side cap for this one
	// execution. It includes bounded estimate headroom and never exceeds a
	// positive ApprovedScanBytes hard ceiling.
	AuthorizedScanBytes int64
}

type ScanApprovalDecision struct {
	Approved   bool
	ApprovalID string
}

type ScanApprovalFunc func(context.Context, ScanApprovalRequest) (ScanApprovalDecision, error)

// QueryProfiler is an OPTIONAL adapter capability (type assertion).
//
// BOUNDARY (v2 — supersedes the earlier note): Registry.Launch OWNS
// profiling. When an adapter implements QueryProfiler (or, preferably,
// ObservableQueryProfiler), Launch profiles AFTER the durable
// capacity-reserved profiling record exists and BEFORE any Submit, under
// an independent bounded deadline. Tools must NOT call Profile directly
// anymore: idempotency recovery, admission, durability, and cancel-on-
// interrupt only hold when the registry drives it. Tools format the
// rewrite/approval/alternative-plan response from the typed surface Launch
// returns: Job.Profile (the persisted QueryProfile), Job.ProfileOutcome
// (invalid/over_budget/timed_out/errored/indeterminate), and the
// ErrProfileRejected/ErrProfilePending error chain.
type QueryProfiler interface {
	Profile(ctx context.Context, req SubmitRequest) (QueryProfile, error)
}

// ProfileObserver lets a profiler publish its native profile telemetry the
// MOMENT it exists — statement handle, phase, advancing cursor (via the
// handle's Extra) — so an interrupted profile is durably identifiable and
// cancellable after a crash. Every publication is a holder+lease+revision-
// fenced durable write; a non-nil return means the publication did NOT
// persist (lease lost, revision conflict, store failure) and the provider
// MUST abort the profile and clean up — never continue past a failed
// durable handle publication.
type ProfileObserver interface {
	PublishProfileHandle(handle NativeHandle) error
	PublishProfilePhase(phase string) error
}

// ObservableQueryProfiler is the PREFERRED profiling capability: providers
// whose profiles run real statements (Databricks EXPLAIN, Snowflake
// EXPLAIN, Trino's multi-statement profile) implement this so the handle
// is durable before the statement completes. BigQuery's immediate dry-run
// has no handle and may implement plain QueryProfiler instead. An
// ambiguous profile submit must never be blindly replayed — publish the
// handle first, or return an error and let the outcome be errored.
type ObservableQueryProfiler interface {
	ProfileWithObserver(ctx context.Context, req SubmitRequest, obs ProfileObserver) (QueryProfile, error)
}

// ProfileCanceller cancels a published profile handle best-effort. Used on
// local timeout/cancel and on rehydration of an interrupted profiling
// record. OPTIONAL; providers without profile cancel semantics omit it.
type ProfileCanceller interface {
	CancelProfile(ctx context.Context, handle NativeHandle) error
}

// Budgets are per-job limits. Zero values inherit registry policy defaults.
type Budgets struct {
	SoftQueue          time.Duration `json:"soft_queue,omitempty"`
	SoftExecution      time.Duration `json:"soft_execution,omitempty"`
	HardExecution      time.Duration `json:"hard_execution,omitempty"`
	MaxResultRows      int           `json:"max_result_rows,omitempty"`
	MaxResultBytes     int64         `json:"max_result_bytes,omitempty"`
	AutomaticScanBytes int64         `json:"automatic_scan_bytes,omitempty"`
	MaxScanBytes       int64         `json:"max_scan_bytes,omitempty"` // approved ceiling, e.g. BQ maximumBytesBilled
}

func (b Budgets) withDefaults(d Budgets) Budgets {
	if b.SoftQueue <= 0 {
		b.SoftQueue = d.SoftQueue
	}
	if b.SoftExecution <= 0 {
		b.SoftExecution = d.SoftExecution
	}
	if b.HardExecution <= 0 {
		b.HardExecution = d.HardExecution
	}
	if b.MaxResultRows <= 0 {
		b.MaxResultRows = d.MaxResultRows
	}
	if b.MaxResultBytes <= 0 {
		b.MaxResultBytes = d.MaxResultBytes
	}
	if b.AutomaticScanBytes <= 0 {
		b.AutomaticScanBytes = d.AutomaticScanBytes
	}
	if b.MaxScanBytes <= 0 {
		b.MaxScanBytes = d.MaxScanBytes
	}
	if b.MaxScanBytes > 0 && (b.AutomaticScanBytes <= 0 || b.AutomaticScanBytes > b.MaxScanBytes) {
		b.AutomaticScanBytes = b.MaxScanBytes
	}
	return b
}
