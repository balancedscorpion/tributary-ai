# Async Query Execution Plan

## Problem

Queries block the entire agent loop for up to 60-70 seconds. The user sees a frozen tool call with no progress feedback, no ability to cancel individual queries, and no way to steer or interact mid-query. If a query exceeds the timeout, it fails even though the warehouse job is still running server-side.

## Design Principles

1. **Fast queries stay fast.** If the query completes inside an adaptive short wait window (e.g. 300ms then 800ms), return results inline. The common case should feel unchanged.
2. **Queries survive run lifecycle changes.** A warehouse job outlives run cancellation/steer and can be resumed from a later run in the same thread using the same query handle (thread+opaque owner token).
3. **Query ownership is resilient but strict.** Async jobs use thread-bound ownership with a high-entropy handle token; run IDs are retained for audit/readability only, not hard auth.
4. **Reliable progress and cancellation.** Registry status is source-of-truth with atomic transitions, lock-safe snapshots, bounded polling fanout, and persisted terminal metadata where persistence is available.
5. **Agent reliability under long waits.** The agent still does useful work during waiting but does not rely on perfect polling cadence for user-visible progress.
6. **Users can cancel individual queries.** A cancel button on each running query cancels warehouse execution without canceling the active run.

---

## Architecture Overview

```
                    +----------------------+
                    |   Job Registry       |
                    |   in-memory + optional persistent metadata   |
                    |                      |
                    |  job_id -> {         |
                    |    provider,         |
                    |    owner, status,    |
                    |    result, metadata, |
                    |    cancel_func,      |
                    |    poller_slot       |
                    |  }                   |
                    +------+---------------+
                           |
          +----------------+----------------+
          |                |                |
    +-----v-----+   +-----v-----+   +------v------+
    |  Submit    |   |  Poll     |   |  Cancel     |
    |  (tool)   |   |  (tool)   |   |  (UI + tool)|
    +-----+-----+   +-----+-----+   +------+------+
          |                |                |
    +-----v----------------v----------------v------+
    |              Warehouse APIs                   |
    |  BigQuery REST  /  Databricks SQL Statement   |
    +----------------------------------------------+
```

---

## Detailed Design

### 1. Server: Job Registry

**New file:** `server/internal/chat/jobs/registry.go`

The registry tracks query jobs independently of runs with two storage modes:

- **In-memory fast path** for active runtime state.
- **Optional persisted query metadata** (terminal state + owner mapping) for restart resilience.
Running jobs remain visible until terminal state; running jobs are never evicted due to age alone.

```go
type JobStatus string

const (
    JobStatusSubmitted  JobStatus = "submitted"
    JobStatusRunning    JobStatus = "running"
    JobStatusCompleted  JobStatus = "completed"
    JobStatusFailed     JobStatus = "failed"
    JobStatusCancelled  JobStatus = "cancelled"
)

type QueryResult struct {
    Rows     []map[string]any
    Schema   []map[string]any
    Metadata map[string]any // duration_ms, bytes_processed, job_id, etc.
}

type Job struct {
    ID           string
    Provider     string    // "bigquery" | "databricks"
    SQL          string
    Status       JobStatus
    SubmittedAt  time.Time
    LastAccessAt time.Time
    CompletedAt  *time.Time
    Result       *QueryResult
    Error        string
    Metadata     map[string]any
    Owner        JobOwner
    PollMeta     map[string]any // provider-native poll reference for rehydrate (job_id/location/statement_id, etc.)

    cancel context.CancelFunc // cancels the background poll goroutine
    mu     sync.RWMutex
}

type JobOwner struct {
    ThreadID  string
    RunID     string // optional, for diagnostics only
    UserToken string
    Handle    string // owner token from tool/result path
}

type PollFunc func(ctx context.Context) (done bool, result *QueryResult, err error)

type Registry struct {
    mu   sync.RWMutex
    jobs map[string]*Job

    // Optional persistent metadata (terminal state + owner mapping) for restart recovery.
    // If unset, terminal jobs are retained in-memory only.
    store RegistryStore
}

func (j *Job) tryTransition(from, to JobStatus) bool // true when transition is accepted
```

**Owner token generation:** Use `crypto/rand` 128-bit tokens (hex-encoded, e.g. `tok_...`), stored in `Job.Owner.Handle` and persisted in metadata. This is intentionally simple and persistence-friendly; no JWT/signature assumptions are made.

**Key behaviours:**

| Method | Description |
|--------|-------------|
| `Register(owner JobOwner, provider, sql, sqlPreview string, poll PollFunc) (*Job, error)` | Creates a job with ownership metadata and a cancellation-safe background poller using adaptive provider-specific intervals. Enforces per-thread and global concurrency limits. |
| `Status(id string, owner JobOwner) (*Job, bool)` | Returns current job state only when owner `ThreadID + Handle` matches. |
| `Snapshot(id string, owner JobOwner) (*Job, bool)` | Returns a lock-safe immutable snapshot for read-only call paths. |
| `Results(id string, owner JobOwner) (*QueryResult, error)` | Returns results for completed jobs if ownership is valid. |
| `Cancel(id string, owner JobOwner) error` | Marks terminal cancelled state only if not already terminal and sends best-effort warehouse cancel API call. Idempotent for repeated requests. |
| `AttachOwner(id string, owner JobOwner) error` | Adds a compatible read owner for resumed runs only when `ThreadID` and `Handle` match exactly. |
| background: `reap()` | Removes only terminal jobs after retention window. Running jobs are never evicted by age alone. Persisted terminal records can be restored on restart. |

**Concurrency:** All mutable fields are accessed under the job's `mu` mutex; read APIs take snapshots while holding `RLock`. State transitions are forward-only (`submitted -> running -> terminal`) and guarded under `mu` (`Status` is the single source of truth; no separate atomic `state` field).

**Lock ordering:** Always acquire `Registry.mu` before `Job.mu`. Methods that hold a `Job.mu` must not take `Registry.mu` until `Job.mu` is released.

**Concurrency limits:** Start with defaults (`maxJobsPerThread = 5`, `maxJobsGlobal = 10`). `Register` fails with `ErrConcurrencyLimit` when either limit is exceeded.

**Polling fanout scaling:** The registry uses a bounded poller worker pool. Jobs are scheduled with adaptive poll intervals, and each poller performs at most one provider poll per due job at any time.

**Cancel propagation to warehouse:**

The `PollFunc` receives a context. When `Cancel()` is called, it cancels that context. The poll function's in-flight HTTP request to the warehouse is aborted. Additionally, Cancel sends an explicit cancel API call:
- BigQuery: `DELETE /bigquery/v2/projects/{projectId}/jobs/{jobId}/cancel`
- Databricks: `POST /api/2.0/sql/statements/{statementId}/cancel`

This is a best-effort fire-and-forget. Cancel is attempted for a short grace period, then the registry keeps terminal `cancelled` state regardless of any delayed warehouse response.

**Restart behavior:** On startup, restore persisted metadata and resume tracking.
- Terminal records are loaded for TTL reuse so status/cancel remain queryable after restart.
- Non-terminal records are restored with ownership plus `PollMeta`, then reattached to provider-specific pollers.
- Poller reconstruction requires provider type, provider-native poll reference, and active provider credentials.
- Jobs that cannot be rehydrated stay `unknown` and return explicit resume guidance on status calls.

---

### 2. Server: Handler Changes

**Modified files:** `server/internal/bigquery/handler.go`, `server/internal/databricks/handler.go`

No new HTTP endpoints on the handlers themselves. Instead, the existing `runQuery` / `queryHandler` functions are refactored to expose a lower-level API that the tools can use:

#### BigQuery Handler

```go
// SubmitQuery sends the query and returns immediately with a job reference.
// If the query completes within initialWait, returns results directly.
// owner is persisted in the job registry for safe read/cancel validation.
// Uses two-step adaptive poll behavior: wait 300ms, then an additional 800ms.
func (h *Handler) SubmitQuery(
    ctx context.Context,
    projectID, sql string,
    initialWait time.Duration,
    owner jobs.JobOwner,
) (*SubmitResult, error)

type SubmitResult struct {
    // Set if query completed within initialWait
    Completed bool
    Rows      []map[string]any
    Schema    []map[string]any
    Metadata  map[string]any

    // Set if query is still running
    JobID       string
    JobLocation string
    ProjectID   string
    OwnerToken  string
}

// PollJob checks if a BigQuery job has completed. Returns rows if done.
func (h *Handler) PollJob(ctx context.Context, projectID, jobID, location string) (done bool, result *QueryResult, err error)

// CancelJob sends a cancel request to BigQuery.
func (h *Handler) CancelJob(ctx context.Context, projectID, jobID, location string) error
```

The existing `runQuery` function is refactored to call `SubmitQuery` internally (preserving the current HTTP endpoint behaviour).

#### Databricks Handler

```go
func (h *Handler) SubmitQuery(
    ctx context.Context,
    host, warehouseID, sql, catalog, schema string,
    initialWait time.Duration,
    owner jobs.JobOwner,
) (*SubmitResult, error)

func (h *Handler) PollStatement(ctx context.Context, host, statementID string) (done bool, result *QueryResult, err error)

func (h *Handler) CancelStatement(ctx context.Context, host, statementID string) error
```

Same pattern. The existing `queryHandler` calls `SubmitQuery` internally.

---

### 3. Server: Tool Changes

#### Modified: `run_bigquery_query` / `run_databricks_query`

**Files:** `server/internal/chat/tools/bigquery_tools.go`, `server/internal/chat/tools/databricks_tools.go`

The execute function changes from:

```
1. Preflight (if first page)
2. Call handler's HTTP query endpoint
3. Wait for full response
4. Wrap in Output
```

To:

```
1. Preflight (if first page) -- unchanged, still synchronous
2. Call handler.SubmitQuery(ctx, ..., initialWait: adaptive (300ms then 800ms))
3a. If completed -> wrap rows in Output as today (fast path, no change)
3b. If still running -> after both waits (~1.1s total), register job in registry, return handle (`owner_token`, `thread_id`, no run-bound lockout)
```

The existing Databricks poller cadence remains unchanged; this adaptive behavior is layered on top of current statement submission/polling internals.

**Fast-path return** (query completed within the adaptive quick path window):
```json
{
  "ok": true,
  "result_format": "json",
  "result": [{"col1": "val1"}, "..."],
  "query_metadata": { "duration_ms": 1200 },
  "artifact_id": "art-abc"
}
```
Identical to today. No UX change.

**Async return** (query still running):
```json
{
  "ok": true,
  "result_format": "json",
  "result": {
    "status": "running",
    "job_id": "qj-bq-abc123",
    "provider": "bigquery",
    "sql_preview": "SELECT region, SUM(revenue)...",
    "owner_token": "tok_abc123",
    "message": "Query submitted to BigQuery. Use check_query_status to monitor progress."
  },
  "query_metadata": { "job_id": "qj-bq-abc123", "provider": "bigquery", "owner_token": "tok_abc123", "thread_id": "thread-1" },
  "artifact_id": "art-thread1-qj-bq-abc123"
}
```

The `artifact_id` is deterministic from `{thread_id, job_id}` so that `get_query_results` can later update the same artifact from a resumed run.
The deterministic async `artifact_id` path is explicitly scoped to this async placeholder/update flow and must remain disjoint from synchronous `persistToolResult` UUID artifacts to avoid collisions.

#### New: `check_query_status`

**Provider-agnostic.** One tool handles both BigQuery and Databricks -- the registry knows which provider.

```
Name: check_query_status
Description: Check the status of a previously submitted query.
             Returns the current status and elapsed time.
             Call this periodically while a query is running.

Parameters:
  job_id (string, required): The job ID returned by run_bigquery_query
                             or run_databricks_query.
  owner_token (string, required): Opaque owner token from async return metadata.

  Returns:
    { "status": "running"|"completed"|"failed"|"cancelled"|"unknown",
      "owner_token": "tok_abc123",
      "elapsed_ms": 5200,
      "message": "Query is executing (5.2s elapsed)" }

This tool should prefer registry-local state reads over direct warehouse status calls, and is intended to be a cheap probe between useful model work.
```

**UI visibility:** Tool calls to `check_query_status` are marked with `"ui_suppress": true` in the tool result (same mechanism as `preflight_failed`/`rewrite_required` today). The frontend suppresses rendering. The LLM narrates progress in its text output instead, which streams naturally.

#### New: `get_query_results`

```
Name: get_query_results
Description: Retrieve results for a completed query.
             Only call this after check_query_status returns "completed".

Parameters:
  job_id (string, required): The job ID.
  owner_token (string, required): Opaque owner token from async return metadata.
  limit (integer, optional): Max rows to return. Default 200, max 1000.
  page_token (string, optional): Pagination token for subsequent pages.

Returns:
  Standard ToolResultEnvelope with rows, schema, query_metadata, artifact_id.
  Same format as today's run_bigquery_query success response.
  The artifact_id matches the one from the original submit.
```

**Pagination:** Works identically to today.
- Prefer provider-native continuation tokens when available.
- `get_query_results` is provider-native-only pagination; if continuation is unavailable, return a clear guidance/error and do not wrap/rewrite SQL at result-time.
- The LLM can call `get_query_results` again with a `page_token`.

#### New: `cancel_query`

```
Name: cancel_query
Description: Cancel a running query. Use this when the user requests
             cancellation or when a query is no longer needed.

Parameters:
  job_id (string, required): The job ID to cancel.
  owner_token (string, required): Opaque owner token from async return metadata.

Returns:
  { "ok": true, "message": "Query cancelled." }
  or
  { "ok": false, "error": "Job not found or already completed." }
```

The LLM calls this when it discovers a query was cancelled by the user (via `check_query_status` returning `"cancelled"`), or proactively when it decides a query is no longer needed (e.g., after a steer changes the approach).

#### Registry in Tools

**Modified file:** `server/internal/chat/tools/types.go`

Add the registry to `ExecContext`:

```go
type ExecContext struct {
    // ... existing fields ...
    Jobs *jobs.Registry
}
```

The manager should call `Jobs.AttachOwner(job_id, owner)` when a later run or resume path
reads a job token from conversation history, so legacy/steered run context can
re-use an active job handle without relaxing authorization boundaries. `AttachOwner` requires exact `ThreadID + Handle` match and cannot fall back to thread-only auth.

**Modified file:** `server/internal/chat/tools/registry.go`

Register the three new tools:

```go
// Query lifecycle tools (provider-agnostic)
r.mustRegister(newCheckQueryStatusTool(), true, false, false)
r.mustRegister(newGetQueryResultsTool(), true, false, false)
r.mustRegister(newCancelQueryTool(), true, false, false)
```

These are always registered (not gated behind a provider flag) since they're provider-agnostic and only useful if a job_id exists.

---

### 4. Server: Cancel HTTP Endpoint

**Modified file:** `server/internal/chat/httpapi/handler.go`

Add REST endpoints for the frontend cancel button and status polling:

```
POST /api/queries/{jobId}/cancel
GET  /api/queries/{jobId}/status
```

These do NOT go through the LLM. They are direct frontend-to-backend calls.

**Progress channel trade-off:** Query progress is surfaced via REST polling rather than SSE. Run-scoped SSE streams are not durable beyond run lifetime, and query jobs must remain discoverable across runs; a thread-scoped SSE channel is explicitly deferred in this phase due additional stream lifecycle and replay complexity.

**Cancel handler:**
1. Validates owner context and reads owner token from `x-query-owner-token` or tool metadata
2. Looks up the job in the registry and enforces token auth (`ThreadID + Handle`)
3. Calls `registry.Cancel(jobId, owner)`
4. Returns `200 OK` with `{ "status": "cancelled" }` (or terminally-idempotent equivalent)
5. If record exists only in persisted terminal metadata, return a clear expired/owned terminal message rather than "not found".

**Status handler:**
1. Validates owner context and reads owner token from `x-query-owner-token` or tool metadata
2. Looks up the job in the registry and enforces token auth (`ThreadID + Handle`)
3. Returns `{ "job_id": "...", "status": "running", "elapsed_ms": 12400, "provider": "bigquery" }`
4. Returns `unknown` status when a job is rehydrated from stale state or is temporarily unavailable for in-process polling.

Wired directly in the HTTP router alongside the existing BigQuery/Databricks endpoints.

---

### 5. Frontend: Running Query Card

**Modified file:** `components/chat/ToolResultBlock.tsx`

When the ToolResultBlock receives an envelope whose `result` has `"status": "running"`, it renders a **RunningQueryCard** instead of an ArtifactCard.

#### RunningQueryCard states

```
+---------------------------------------------+
| ::  Query running...             [Cancel]    |
|    [##########------] 58%   12.4s elapsed    |
|    BigQuery . bquxjob_abc123                 |
+---------------------------------------------+

+---------------------------------------------+
| ok  Query completed              12.4s       |
|    BigQuery . bquxjob_abc123                 |
+---------------------------------------------+

+---------------------------------------------+
| x   Query cancelled              8.1s       |
|    Cancelled by user                         |
+---------------------------------------------+

+---------------------------------------------+
| !   Query failed                 3.2s       |
|    Error: Table not found                    |
+---------------------------------------------+
```

#### Implementation

The RunningQueryCard:
1. Extracts `job_id`, `provider`, `owner_token`, and `thread_id` from the tool result envelope
2. Starts a client-side elapsed timer (uses the tool call's `_uiStartedAt` timestamp, same pattern as `ToolCallBlock`)
3. Polls `GET /api/queries/{jobId}/status` with owner token:
   - 0-10s: 1.5s
   - 10-60s: 2.5s
   - >60s: 5s
4. Shows a cancel button that calls `POST /api/queries/{jobId}/cancel` with owner token
5. Renders a determinate progress bar + `%` label for `submitted` / `running` / `unknown`:
   - Uses `progress_percent` from status payload when available.
   - Falls back to elapsed-time estimation to keep UX moving during sparse updates.
6. Renders a provider-native job link in-card, preferring status payload fields `provider_job_id` + `provider_job_url`.
7. Falls back to provider metadata when those fields are absent:
   - Databricks: `url` + `statement_id` (or reconstruct URL from `host` + `statement_id`).
   - BigQuery: build console job URL from `project_id` + `job_location` + `warehouse_job_id`.
8. Transitions to a terminal state (completed/failed/cancelled) when the poll returns a terminal status

**Why poll from the frontend?** The LLM's `check_query_status` calls are hidden and intentionally lightweight; they may be spaced out while the model does other work. The frontend poll gives user-visible real-time feedback and transitions the card on terminal status as soon as backend state updates.

#### Cancel button behaviour

1. User clicks Cancel
2. Frontend sends `POST /api/queries/{jobId}/cancel`
3. Frontend optimistically sets the card to "Cancelled" state (doesn't wait for the LLM)
4. Timer stops, cancel button disappears
5. Backend state is terminal quickly; LLM can continue on next polling/tool step.

**No steer or run cancellation needed.** The cancel is scoped to a single query. The agent loop keeps running. The LLM finds out naturally.

---

### 6. Frontend: Artifact Linking

**Modified file:** `hooks/useChat.ts`

When `get_query_results` returns a tool result with the same `artifact_id` as a previous `run_bigquery_query` async result, the artifact updates in place.

The existing `applyToolResultMessageSideEffects` already handles artifact creation by `artifact_id`. If the same `artifact_id` appears again with actual data and same ownership metadata, the artifact's `content` is replaced (data overwrites the `{ status: "running" }` placeholder).

The RunningQueryCard (which rendered for the "running" status) detects via its frontend poll that the job is now completed and transitions its own UI to the terminal "Completed" state.

If the same `artifact_id` appears in a later run, the existing placeholder artifact is replaced in place.

The ArtifactCard for the data results appears at the `get_query_results` tool result position in the message stream, which is where the LLM calls it (later in the conversation).

**Net effect:** The user sees:
1. A RunningQueryCard in the message stream (at the point the query was submitted)
2. LLM text explaining what it's doing
3. An ArtifactCard with the actual results (at the point the LLM retrieved them)
4. The RunningQueryCard transitions to "Completed . 12.4s" (via frontend poll)

---

### 7. Prompt Engineering

**Modified file:** `server/internal/chat/optionc/prompts.go`

Add async query guidance to the system prompt (appended via `embedAnthropicToolingGuidance` or a new `embedAsyncQueryGuidance` function):

```
## Query execution

When you call run_bigquery_query or run_databricks_query, the query may return
results immediately (fast path) or return a job handle with status "running".

When a query is running asynchronously:
1. Note the job_id and owner_token from the response.
2. Do useful work between status checks: explore schemas, plan your analysis, explain
   your approach, or submit additional queries.
3. Call check_query_status(job_id, owner_token) at meaningful decision points (roughly every 2-3 tool calls) to monitor progress.
   Keep text blocks concise while it is running.
4. When status is "completed", call get_query_results(job_id, owner_token) to retrieve data.
5. When status is "cancelled", acknowledge the cancellation and ask if the user
   wants an alternative approach.
6. When status is "failed", report the error and offer to adjust the query.
7. If a query has been running for > 30 seconds without progress checks, include a concise user-visible update and continue tool-level polling.

You can run multiple queries concurrently by submitting them in sequence (each
submit returns quickly, but in this engine they currently execute sequentially because tool calls are handled serially). So each async submission can block up to ~1.1s while waiting before returning.

Do NOT call get_query_results before check_query_status confirms "completed".
```

---

### 8. Hidden Tool Call Rendering

**Modified file:** `hooks/useChat.ts` (in `applyToolResultMessageSideEffects` or the SSE event handler)

`check_query_status` tool results include `"ui_suppress": true` in the envelope. The frontend checks for this flag and skips rendering the ToolCallBlock and ToolResultBlock for these calls -- identical to how `preflight_failed` / `rewrite_required` results are suppressed today.
Only deduplicated status updates with changed terminal state should re-render the RunningQueryCard.

The LLM's text narration ("Query still running, about 8 seconds...") renders normally through `text_delta` events.

---

## User Experience Scenarios

### Scenario 1: Fast query (no change)

```
User: "How many users signed up last month?"

[ToolCallBlock] run_bigquery_query
  SELECT COUNT(*) FROM users WHERE signup_date >= '2026-01-01'

[ToolResultBlock] Success . 1 row . 1.2s . 48 MB scanned
Assistant: "You had 12,340 signups last month."

Identical to today. The short adaptive wait (up to ~1.1s) absorbs the query, and the result flows through the existing tool result path.

### Scenario 2: Slow query with productive wait

    User: "Show me daily revenue for the last 3 years by product category"

    [ToolCallBlock] run_bigquery_query
      SELECT date, category, SUM(revenue) ...

    [RunningQueryCard] Query running... 0s elapsed  [Cancel]
                       BigQuery / job bq-xyz

      (check_query_status calls are hidden from UI)

    Assistant: "I've submitted a revenue query that's scanning a large dataset.
               Let me check what product categories are available..."

    [ToolCallBlock] list_bq_tables (dataset="sales")
    [ToolResultBlock] 8 tables found

    Assistant: "There are 12 product categories. The query is still running
               (about 8 seconds)... I'll break down the results by category
               once the data comes back."

    [RunningQueryCard transitions to: Query completed  12.4s]

    [ToolCallBlock] get_query_results (job_id="bq-xyz")
    [ArtifactCard] Query Results / 4,380 rows / 12.4s / 2.1 GB scanned

    Assistant: "Here's the daily revenue breakdown. Q4 had a significant spike
               in the Electronics category..."

The user sees a conversational analyst, not a frozen spinner.

### Scenario 3: User cancels a slow query

    User: "Run this complex join across all historical tables"

    [ToolCallBlock] run_bigquery_query
      SELECT ... (complex multi-table join)

    [RunningQueryCard] Query running... 15.2s elapsed  [Cancel]

    User clicks [Cancel]

    [RunningQueryCard transitions to: Query cancelled  15.2s]
      (card updates immediately via optimistic UI)

      (next time LLM polls check_query_status, it sees "cancelled")

    Assistant: "I see you cancelled that query. Would you like me to try a
               simpler approach -- perhaps focusing on just the last quarter?"

The cancel is scoped to the query, not the run. The LLM keeps working.

### Scenario 4: User steers mid-query

    User: "What's our revenue by region?"

    [ToolCallBlock] run_bigquery_query
      SELECT region, SUM(revenue) FROM sales GROUP BY region

    [RunningQueryCard] Query running... 3s elapsed  [Cancel]

    Assistant: "Running your revenue by region query..."

    User steers: "actually break it down by month too"

      -- run cancelled, new run starts with full history + steer --
      -- LLM sees: original query submitted (job bq-123), steer message --

    Assistant: "Got it -- I'll add a monthly breakdown. Let me check on the
               first query and submit an updated one."

    [ToolCallBlock] run_bigquery_query
      SELECT region, DATE_TRUNC(date, MONTH), SUM(revenue) ...

    [RunningQueryCard] Query running... 0s elapsed  [Cancel]

    Assistant: "I've submitted the updated query with monthly granularity.
               The original query is still running too -- I'll use whichever
               finishes first if useful, or just the new one."

The steer cancels the run, not the warehouse jobs. The new run picks up existing job_ids from history.

### Scenario 5: Multiple parallel queries

Note: within current `RunAgentLoop`, these tool calls are still processed in sequence; each submission should still be non-blocking after the adaptive fast path, and then results are polled independently.

    User: "Compare Q3 and Q4 revenue, and show customer churn for each quarter"

    Assistant: "I'll need to run a few queries for this comparison."

    [ToolCallBlock] run_bigquery_query
      SELECT ... Q3 revenue ...
    [RunningQueryCard] Query running... 0s  [Cancel]  (job bq-q3)

    [ToolCallBlock] run_bigquery_query
      SELECT ... Q4 revenue ...
    [RunningQueryCard] Query running... 0s  [Cancel]  (job bq-q4)

    [ToolCallBlock] run_bigquery_query
      SELECT ... churn rates ...
    [RunningQueryCard] Query running... 0s  [Cancel]  (job bq-churn)

    Assistant: "Three queries submitted. Let me check on them..."

    [RunningQueryCard bq-q3 transitions to: Completed  3.2s]

    [ToolCallBlock] get_query_results (job_id="bq-q3")
    [ArtifactCard] Q3 Revenue / 90 rows

    Assistant: "Q3 revenue is in -- $4.2M total. Still waiting on Q4 and churn..."

    [RunningQueryCard bq-q4 transitions to: Completed  5.1s]
    [RunningQueryCard bq-churn transitions to: Completed  6.8s]

    [ToolCallBlock] get_query_results (job_id="bq-q4")
    [ArtifactCard] Q4 Revenue / 90 rows

    [ToolCallBlock] get_query_results (job_id="bq-churn")
    [ArtifactCard] Churn Rates / 180 rows

    Assistant: "All three datasets are in. Q4 revenue was $5.1M (up 21% from Q3).
               Churn decreased from 4.2% to 3.8%..."

Each query has its own running card with independent cancel buttons. The LLM retrieves results as they complete.

---

## Edge Cases

### Query completes while LLM is generating text
The registry marks the job as completed. The RunningQueryCard transitions via its own frontend poll. The LLM catches up on its next check_query_status call. No data is lost.

### Registry cleanup / TTL
Jobs are not removed while non-terminal. Terminal jobs are retained for a configurable window (default 2 hours). If the LLM calls `get_query_results` after expiry, it returns a terminal-expired error with a clear retry path.

### Server restart
Persisted terminal metadata allows status/cancel lookups to continue for TTL-bound jobs after restart.
Persisted non-terminal jobs should be restored via scan on startup and reattached to pollers using `provider + PollMeta + credentials`. If rehydration fails, jobs return `unknown` with explicit resume guidance to continue polling the same `owner_token`.

### Multiple runs polling same job
After a steer, the old run (being torn down) and the new run may reference the same job_id. Ownership is validated by `thread_id + owner_token` and can be shared across runs.

### Fast query with pagination
If the first page returns on the adaptive fast path, pagination works exactly as today -- the LLM calls run_bigquery_query again with a page_token. No async involved. If the initial query was async, the LLM calls get_query_results with a page_token instead.

### CSV tools
The `tools/csv_tools.*` path is explicitly out of scope for this phase. If file-based analyses introduce long-running behavior, they should be evaluated separately as an async-capable vertical slice before extending this registry.

---

## File Change Summary

| File | Change Type | Description |
|------|------------|-------------|
| `server/internal/chat/jobs/registry.go` | **New** | Job registry: lock-safe store, bounded poller worker pool, terminal-state persistence hooks |
| `server/internal/chat/jobs/store.go` | **New** | Optional registry persistence abstraction and adapters (memory / mysql/json fallback) |
| `server/internal/chat/jobs/ownership.go` | **New** | Owner token generation/verification and lightweight job-scoped auth helpers |
| `server/internal/chat/jobs/registry_test.go` | **New** | Unit tests for registry lifecycle, cancellation, expiry |
| `server/internal/bigquery/handler.go` | Modified | Extract SubmitQuery/PollJob/CancelJob methods from existing runQuery |
| `server/internal/databricks/handler.go` | Modified | Extract SubmitQuery/PollStatement/CancelStatement methods |
| `server/internal/chat/tools/bigquery_tools.go` | Modified | Use adaptive wait + owner-scoped `SubmitQuery`; register job on async |
| `server/internal/chat/tools/databricks_tools.go` | Modified | Same pattern as BigQuery |
| `server/internal/chat/tools/query_lifecycle.go` | **New** | check_query_status, get_query_results, cancel_query tool implementations |
| `server/internal/chat/tools/types.go` | Modified | Add Jobs field to ExecContext |
| `server/internal/chat/tools/registry.go` | Modified | Register 3 new tools |
| `server/internal/chat/httpapi/handler.go` | Modified | Add GET /api/queries/{id}/status and POST /api/queries/{id}/cancel |
| `server/internal/chat/optionc/prompts.go` | Modified | Add async query guidance to system prompt |
| `server/internal/chat/optionc/manager.go` | Modified | Pass job registry to ExecContext |
| `components/chat/ToolResultBlock.tsx` | Modified | Detect running status, render RunningQueryCard |
| `components/chat/RunningQueryCard.tsx` | **New** | Running/completed/cancelled/failed states, timer, cancel button, status polling |
| `hooks/useChat.ts` | Modified | Suppress check_query_status rendering; handle artifact_id updates |
| `types.ts` | Modified | Add QueryJobStatus type; extend ToolResultEnvelope with ui_suppress |

---

## Implementation Phases

### Phase 1: BigQuery end-to-end slice
- Create `jobs/ownership.go` for 128-bit token issuance/validation.
- Add `jobs/registry.go` + store adapters with lock-ordering and concurrency-limit enforcement.
- Refactor BigQuery handler: extract `SubmitQuery`, `PollJob`, `CancelJob`.
- Add async path in `run_bigquery_query` (adaptive wait + registry register).
- Add `/api/queries/{id}/status` and `/api/queries/{id}/cancel`.
- Add `RunningQueryCard` and artifact-updating placeholder flow.
- Add async guidance to prompt.
- Add BigQuery end-to-end tests for fast path, slow path, cancel, and steer.

### Phase 2: Databricks parity
- Refactor Databricks handler: extract `SubmitQuery`, `PollStatement`, `CancelStatement`.
- Add async path in `run_databricks_query`.
- Update shared lifecycle tools (`check_query_status`, `get_query_results`, `cancel_query`) to support Databricks poll paths and pagination.

### Phase 3: Registry hardening + lifecycle recovery
- Implement startup rehydration from persisted metadata, including poller reconstruction from provider type + poll reference + credentials.
- Finalize owner validation with strict `ThreadID + Handle` checks for `AttachOwner`, status, and cancel.
- Finalize concurrency behavior (per-thread/global caps, overflow error contract, backpressure expectations).
- Add race-condition tests for transitions, reap, and attach/cancel edge cases.

### Phase 4: Completion and edge-case pass
- Add explicit progress-channel decision rationale (REST polling vs SSE) and lock this contract in docs.
- Add CSV-tools out-of-scope validation and follow-up ticket.
- Add failure-mode cases: owner mismatch, unknown after restart, concurrency rejection messaging, token/credential drift.
- Cross-provider and persistence-backed end-to-end regression checks.
