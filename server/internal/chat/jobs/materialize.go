package jobs

import (
	"context"
	"encoding/json"
)

// MaterializationSummary describes what was durably stored for a job.
type MaterializationSummary struct {
	Rows      int            `json:"rows"`
	Bytes     int64          `json:"bytes"`
	Truncated bool           `json:"truncated"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Materializer is the durable-result seam. The artifact-backed
// implementation lives in the tools/orchestration slice; the registry only
// guarantees the calling contract:
//   - AppendChunk is called in stream order for post-completion fetches
//     on non-drain providers. Pages are already bounded by the job's
//     row/byte budgets; clipped pages carry Truncated=true.
//   - Finalize is called exactly once when the job reaches a terminal
//     state, with whatever result was assembled (may be nil on
//     failure/cancel) and an honest summary (truncated is set whenever
//     limits clipped anything).
//
// Errors are recorded on the job's materialization state; they never
// change execution state.
type Materializer interface {
	AppendChunk(ctx context.Context, jobID string, owner JobOwner, page ResultPage) error
	Finalize(ctx context.Context, jobID string, owner JobOwner, result *QueryResult, summary MaterializationSummary) error
}

// DrainCommit is one atomic durable observation for a continuous-drain
// provider: the bounded result chunk, the cursor positioned after it, and
// the full job record at that revision, all committed together. Seq is
// strictly increasing per job; on crash-replay the sink MUST dedupe by it
// (a commit with Seq <= the last committed Seq is a replay and must be a
// no-op). This is what makes Trino restart-safe without row duplication.
type DrainCommit struct {
	JobID               string
	Owner               JobOwner
	Seq                 int64
	Page                ResultPage // bounded by budgets; may be empty with Truncated set
	Handle              NativeHandle
	DrainRowsCommitted  int64 // cumulative values after this observation
	DrainBytesCommitted int64
	DrainTruncated      bool
	Record              JobRecord
}

// DrainCommitter is the atomicity capability REQUIRED to run managed jobs
// on RequiresContinuousDrain providers. A Materializer that does not also
// implement DrainCommitter cannot make chunk+cursor+record durable as one
// unit, so the registry refuses (never best-efforts) drain launches and
// rehydrations without it: Launch fails with ErrDrainCommitUnsupported and
// Rehydrate orphans the job with recovery_required.
//
// CommitDrainObservation returns the job record's new store revision so
// the registry's CAS bookkeeping stays coherent with the atomic write.
type DrainCommitter interface {
	CommitDrainObservation(ctx context.Context, commit DrainCommit) (newRevision int64, err error)
}

// FencedDrainCommitter is the cross-process-safe form required when monitor
// leases exist. The atomic commit must additionally verify that holder owns
// the exact unexpired monitor lease using store time. A stale holder returns
// ErrLeaseLost and writes neither chunk, cursor, nor record.
type FencedDrainCommitter interface {
	CommitDrainObservationFenced(ctx context.Context, commit DrainCommit, holder string) (newRevision int64, err error)
}

// resultAssembler accumulates pages up to row/byte ceilings, tracking
// truncation explicitly (never silently dropping).
type resultAssembler struct {
	maxRows            int
	maxBytes           int64
	rows               []map[string]any // rows held by this process only
	schema             []map[string]any
	committedRows      int64
	committedBytes     int64
	committedTruncated bool
	metadata           map[string]any
}

func newResultAssembler(b Budgets) *resultAssembler {
	return &resultAssembler{maxRows: b.MaxResultRows, maxBytes: b.MaxResultBytes}
}

// newResultAssemblerFromCommitted resumes global result accounting without
// loading all prior rows into memory. The durable materializer remains the
// row source; these counters keep clipping and terminal summaries cumulative.
func newResultAssemblerFromCommitted(b Budgets, rows, bytes int64, truncated bool) *resultAssembler {
	if rows < 0 {
		rows = 0
	}
	if bytes < 0 {
		bytes = 0
	}
	return &resultAssembler{
		maxRows: b.MaxResultRows, maxBytes: b.MaxResultBytes,
		committedRows: rows, committedBytes: bytes, committedTruncated: truncated,
	}
}

func (a *resultAssembler) matchesCommitted(rows, bytes int64, truncated bool) bool {
	return a != nil && a.committedRows == rows && a.committedBytes == bytes &&
		a.committedTruncated == truncated
}

// plan computes — WITHOUT mutating the assembler — the BOUNDED portion of
// a page that the row/byte ceilings admit (this, never the original page,
// is what may be durably appended) plus whether more data fits. Clipping
// is explicit: the bounded page has Truncated=true whenever anything was
// dropped. plan/commit are split so a durable commit can be attempted
// first and the assembler advanced only after it succeeds (atomic
// observation invariant: nothing observable moves before durability).
func (a *resultAssembler) plan(page ResultPage) (ResultPage, bool) {
	bounded := ResultPage{
		Schema:        page.Schema,
		NextPageToken: page.NextPageToken,
		Truncated:     page.Truncated,
		Metadata:      page.Metadata,
		CursorAfter:   page.CursorAfter,
	}
	rows := a.committedRows
	bytes := a.committedBytes
	// Adapter-side clipping is authoritative. Once a source page says it was
	// truncated, asking for its continuation can move the native cursor past
	// rows the adapter intentionally withheld. Keep the accepted rows, but do
	// not fetch/drain another page from this result sequence.
	more := !page.Truncated
	for _, row := range page.Rows {
		if a.maxRows > 0 && rows >= int64(a.maxRows) {
			bounded.Truncated = true
			more = false
			break
		}
		size := page.Bytes
		if size <= 0 {
			size = approxRowBytes(row)
		} else {
			// Page-level byte counts: amortize across rows.
			size = size / int64(max(1, len(page.Rows)))
		}
		if a.maxBytes > 0 && bytes+size > a.maxBytes {
			bounded.Truncated = true
			more = false
			break
		}
		bounded.Rows = append(bounded.Rows, row)
		bounded.Bytes += size
		rows++
		bytes += size
	}
	return bounded, more
}

// commit applies a previously planned bounded page. Must be called only
// after the corresponding durable commit succeeded.
func (a *resultAssembler) commit(source, bounded ResultPage) {
	if len(a.schema) == 0 && len(source.Schema) > 0 {
		a.schema = source.Schema
	}
	if source.Metadata != nil {
		if a.metadata == nil {
			a.metadata = map[string]any{}
		}
		for k, v := range source.Metadata {
			a.metadata[k] = v
		}
	}
	if bounded.Truncated || source.Truncated {
		a.committedTruncated = true
	}
	a.rows = append(a.rows, bounded.Rows...)
	a.committedRows += int64(len(bounded.Rows))
	a.committedBytes += bounded.Bytes
}

// accept is plan+commit in one step, for paths where the durable append is
// best-effort rather than the atomicity anchor (post-completion fetch).
func (a *resultAssembler) accept(page ResultPage) (ResultPage, bool) {
	bounded, more := a.plan(page)
	a.commit(page, bounded)
	return bounded, more
}

// add retains the boolean-only shape for callers that do not need the
// bounded page.
func (a *resultAssembler) add(page ResultPage) bool {
	_, more := a.accept(page)
	return more
}

func (a *resultAssembler) result() *QueryResult {
	// A result may be built once for the in-memory Job and again for the
	// durable Materializer. Never mutate or return the assembler's shared
	// metadata map: Snapshot can concurrently clone the first published
	// result while finalization constructs the second.
	meta := cloneMap(a.metadata)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["materialized_rows"] = a.committedRows
	meta["materialized_bytes"] = a.committedBytes
	if int64(len(a.rows)) != a.committedRows {
		meta["rows_loaded_in_memory"] = len(a.rows)
	}
	if a.committedTruncated {
		meta["truncated"] = true
	}
	return &QueryResult{Rows: a.rows, Schema: a.schema, Metadata: meta}
}

func (a *resultAssembler) summary() MaterializationSummary {
	return MaterializationSummary{Rows: safeInt(a.committedRows), Bytes: a.committedBytes, Truncated: a.committedTruncated}
}

func (a *resultAssembler) state() MaterializationState {
	if a.committedRows == 0 && a.committedBytes == 0 && !a.committedTruncated {
		return MaterializationNone
	}
	if a.committedTruncated {
		return MaterializationTruncated
	}
	return MaterializationComplete
}

func safeInt(v int64) int {
	if v <= 0 {
		return 0
	}
	maxInt := int64(^uint(0) >> 1)
	if v > maxInt {
		return int(maxInt)
	}
	return int(v)
}

func approxRowBytes(row map[string]any) int64 {
	b, err := json.Marshal(row)
	if err != nil {
		return 64
	}
	return int64(len(b))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
