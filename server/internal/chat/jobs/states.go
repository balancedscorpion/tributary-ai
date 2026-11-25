package jobs

import "time"

// Additional execution states for the managed (long-horizon) lifecycle.
// The legacy constants in registry.go (unknown/submitted/running/completed/
// failed/cancelled) are unchanged; managed jobs may pass through these
// additional states. Execution state is forward-only and validated by
// canTransition.
const (
	JobStatusReserved          JobStatus = "reserved"
	JobStatusProfiling         JobStatus = "profiling"
	JobStatusSubmissionUnknown JobStatus = "submission_unknown"
	JobStatusCancelRequested   JobStatus = "cancel_requested"
	JobStatusCancelling        JobStatus = "cancelling"
	JobStatusExpired           JobStatus = "expired"
	JobStatusOrphaned          JobStatus = "orphaned"
)

// ProfileOutcome is the typed verdict of the pre-submit profiling phase.
// It is orthogonal to JobStatus: a profile-rejected job terminalizes as
// failed (or orphaned when interrupted) with the outcome naming why, so
// tools can format the rewrite/approval/alternative-plan response.
type ProfileOutcome string

const (
	ProfileOutcomeNone           ProfileOutcome = ""
	ProfileOutcomeAccepted       ProfileOutcome = "accepted"
	ProfileOutcomeInvalid        ProfileOutcome = "invalid"         // provider rejected the SQL
	ProfileOutcomeOverBudget     ProfileOutcome = "over_budget"     // estimate exceeds MaxScanBytes
	ProfileOutcomeApprovalDenied ProfileOutcome = "approval_denied" // in-policy scan was not approved
	ProfileOutcomeTimedOut       ProfileOutcome = "timed_out"       // profile deadline elapsed
	ProfileOutcomeErrored        ProfileOutcome = "errored"         // provider/transport failure
	ProfileOutcomeIndeterminate  ProfileOutcome = "indeterminate"   // interrupted (crash/restart)
)

// MonitorHealth reflects our ability to observe the remote job. It is
// orthogonal to execution state: monitor failures never terminalize a job.
type MonitorHealth string

const (
	MonitorHealthOK       MonitorHealth = "ok"
	MonitorHealthDegraded MonitorHealth = "degraded"
	MonitorHealthLost     MonitorHealth = "lost"
)

// PolicyHealth reflects budget/stall evaluation. Soft states are wake
// signals only; hard_exceeded is the only state that triggers automatic
// cancellation (terminalizing as expired).
type PolicyHealth string

const (
	PolicyHealthOK               PolicyHealth = "ok"
	PolicyHealthSoftQueue        PolicyHealth = "soft_queue_exceeded"
	PolicyHealthSoftExecution    PolicyHealth = "soft_execution_exceeded"
	PolicyHealthSuspectedStalled PolicyHealth = "suspected_stalled"
	PolicyHealthHardExceeded     PolicyHealth = "hard_exceeded"
)

// MaterializationState tracks durable result storage, independent of
// execution state (a job can be completed with materialization still
// partial, or truncated by limits).
type MaterializationState string

const (
	MaterializationNone      MaterializationState = "none"
	MaterializationPartial   MaterializationState = "partial"
	MaterializationComplete  MaterializationState = "complete"
	MaterializationTruncated MaterializationState = "truncated"
	MaterializationExpired   MaterializationState = "expired"
)

// CancelOutcome records how a cancellation request resolved.
type CancelOutcome string

const (
	CancelOutcomeNone                   CancelOutcome = ""
	CancelOutcomeConfirmed              CancelOutcome = "confirmed"
	CancelOutcomeUnconfirmed            CancelOutcome = "unconfirmed"
	CancelOutcomeSupersededByCompletion CancelOutcome = "superseded_by_completion"
	CancelOutcomeSupersededByFailure    CancelOutcome = "superseded_by_failure"
	CancelOutcomeBeforeSubmit           CancelOutcome = "before_submit"
)

// CancelReason is required for every managed cancellation.
type CancelReason string

const (
	CancelReasonUserRequest    CancelReason = "user_request"
	CancelReasonSuperseded     CancelReason = "superseded"
	CancelReasonBudget         CancelReason = "budget"
	CancelReasonWrongQuery     CancelReason = "wrong_query"
	CancelReasonNoLongerNeeded CancelReason = "no_longer_needed"
	CancelReasonLegacy         CancelReason = "legacy"
	// CancelReasonDurability: the job's durable anchor could not be
	// established (initial drain commit failed), so the query is cancelled
	// through the confirmed lifecycle rather than abandoned or lied about.
	CancelReasonDurability CancelReason = "durability_failure"
)

// ValidationState is the orchestration-facing result-validation axis. It
// is fully orthogonal to execution state and never set automatically —
// retrieving results does NOT validate them; only an explicit
// SetValidation call moves this state. Orchestration uses it to enforce
// "required query results must be validated before assistant final".
type ValidationState string

const (
	ValidationUnvalidated ValidationState = "unvalidated"
	ValidationValidating  ValidationState = "validating"
	ValidationValidated   ValidationState = "validated"
	ValidationRejected    ValidationState = "rejected"
)

func validValidationState(s ValidationState) bool {
	switch s {
	case ValidationUnvalidated, ValidationValidating, ValidationValidated, ValidationRejected:
		return true
	default:
		return false
	}
}

func isTerminalStatus(status JobStatus) bool {
	switch status {
	case JobStatusCompleted, JobStatusFailed, JobStatusCancelled, JobStatusExpired, JobStatusOrphaned:
		return true
	default:
		return false
	}
}

// isActiveStatus reports whether a job counts toward admission slots.
func isActiveStatus(status JobStatus) bool {
	switch status {
	case JobStatusReserved, JobStatusProfiling, JobStatusSubmitted, JobStatusSubmissionUnknown,
		JobStatusRunning, JobStatusCancelRequested, JobStatusCancelling:
		return true
	default:
		return false
	}
}

var allowedTransitions = map[JobStatus]map[JobStatus]bool{
	JobStatusReserved: {
		JobStatusProfiling:         true, // capacity held; pre-submit profile begins
		JobStatusSubmitted:         true,
		JobStatusSubmissionUnknown: true,
		JobStatusFailed:            true,
		JobStatusCancelled:         true, // cancelled before submit
		JobStatusExpired:           true, // reservation TTL (never dispatched)
		JobStatusOrphaned:          true, // dispatched but unreconcilable (e.g. drain initial page at risk)
	},
	JobStatusProfiling: {
		JobStatusReserved:  true, // profile accepted: SAME slot continues to submit
		JobStatusFailed:    true, // invalid / over_budget / timed_out / errored
		JobStatusCancelled: true, // cancelled before submit
		JobStatusExpired:   true,
		JobStatusOrphaned:  true, // interrupted profile (restart): indeterminate
	},
	JobStatusSubmissionUnknown: {
		JobStatusSubmitted: true, // reconciled: it did land
		JobStatusRunning:   true, // reconciled directly to running
		JobStatusCompleted: true, // reconciled terminal
		JobStatusFailed:    true,
		JobStatusCancelled: true,
		JobStatusOrphaned:  true, // could not reconcile
	},
	JobStatusSubmitted: {
		JobStatusRunning:         true,
		JobStatusCompleted:       true,
		JobStatusFailed:          true,
		JobStatusCancelRequested: true,
		JobStatusCancelled:       true, // legacy direct cancel
		JobStatusExpired:         true,
		JobStatusOrphaned:        true,
	},
	JobStatusRunning: {
		JobStatusCompleted:       true,
		JobStatusFailed:          true,
		JobStatusCancelRequested: true,
		JobStatusCancelled:       true, // legacy direct cancel
		JobStatusExpired:         true,
		JobStatusOrphaned:        true,
	},
	JobStatusCancelRequested: {
		JobStatusCancelling: true,
		JobStatusCancelled:  true,
		JobStatusCompleted:  true, // completion wins
		JobStatusFailed:     true, // failure wins
		JobStatusExpired:    true, // budget cancel confirmed
		JobStatusOrphaned:   true,
	},
	JobStatusCancelling: {
		JobStatusCancelled: true,
		JobStatusCompleted: true, // completion wins
		JobStatusFailed:    true,
		JobStatusExpired:   true,
		JobStatusOrphaned:  true,
	},
	// Legacy entry state: Register() creates jobs directly in submitted.
	// unknown is not a real lifecycle state; nothing transitions from it.
}

func canTransition(from, to JobStatus) bool {
	if from == to {
		return false
	}
	if isTerminalStatus(from) {
		return false
	}
	next, ok := allowedTransitions[from]
	if !ok {
		return false
	}
	return next[to]
}

// WakeKind classifies persisted wake events consumed (exactly once) by the
// continuation barrier.
type WakeKind string

const (
	WakeTerminal          WakeKind = "terminal"
	WakeSoftQueue         WakeKind = "soft_queue_exceeded"
	WakeSoftExecution     WakeKind = "soft_execution_exceeded"
	WakeSuspectedStalled  WakeKind = "suspected_stalled"
	WakeSubmissionUnknown WakeKind = "submission_unknown"
	WakeRecoveryRequired  WakeKind = "recovery_required"
	WakeMonitorLost       WakeKind = "monitor_lost"
	// WakePathology: the job is in a confirmed-degenerate condition that
	// stays non-terminal on principle (e.g. cancellation accepted but
	// unconfirmable, durable materialization commits failing) and needs
	// model/user attention.
	WakePathology WakeKind = "pathology"
)

// WakeEvent is a persisted, claimable signal that the model/task layer
// should look at a job. Events are claimed exactly once via
// Registry.ClaimWakeEvents.
type WakeEvent struct {
	At      time.Time `json:"at"`
	Kind    WakeKind  `json:"kind"`
	Detail  string    `json:"detail,omitempty"`
	Claimed bool      `json:"claimed"`
}
