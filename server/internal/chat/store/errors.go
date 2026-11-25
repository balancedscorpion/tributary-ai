package store

import "errors"

var (
	// ErrNotFound indicates the requested entity does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict indicates a constraint violation (e.g. duplicate, concurrent run).
	ErrConflict = errors.New("conflict")
	// ErrInvalid indicates the request was malformed.
	ErrInvalid = errors.New("invalid")
	// ErrQuarantinedQueryJobIdentity marks a legacy query row whose primary or
	// lineage identity cannot be used safely. Broad recovery scans skip these
	// rows so one corrupt record cannot block monitoring of healthy jobs.
	ErrQuarantinedQueryJobIdentity = errors.New("query job identity quarantined")
	// ErrPlanStale indicates an expired, missing, already-divergent, or otherwise
	// invalid preflight plan. HTTP callers should re-preflight on this error.
	ErrPlanStale = errors.New("plan stale")
)
