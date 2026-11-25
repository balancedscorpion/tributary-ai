# External API Build-Out Plan

## Purpose

This document specifies how to expose the current chat-first AI analyst app as a stable external API with the same application behavior and outputs as the UI:

- agent runs
- streamed assistant output
- tool calls and tool progress
- query jobs
- generated artifacts
- uploaded files and image attachments
- thread/message history

The goal is not to create a separate agent stack. The API should reuse the same backend orchestration path that powers the UI so API and UI behavior stay aligned.

Primary implementation rule for this repository: prefer local settings, secrets, and existing config over new environment variables unless there is no reasonable alternative.

## Implementation Status

Implemented in this build:

- `server/internal/chat/publicapi` mounted at `/api/external/v1`
- API key auth with scopes, revocation, last-used tracking, and optional thread/project allowlists
- run creation, run status, run cancellation, SSE events, final run results, messages, attachments, artifacts, artifact rows, query status/cancel, and API status
- public DTO/redaction layer for runs, messages, artifacts, tool results, query owner tokens, hidden context, and provider payloads
- manager-assigned SSE sequence numbers with in-memory `after` resume semantics
- unit/handler tests covering auth, normalization, DTOs, routing, resource scoping, streaming resume, result assembly, artifacts, attachments, query errors, and cancellation errors

Not implemented yet:

- durable event replay after in-memory run state expires
- OpenAPI 3.1 schema and generated SDK fixtures
- UI for creating/revoking public API keys

## Current State

The production chat flow already has the necessary primitives:

1. The React UI posts a user turn through `POST /api/threads/{threadId}/runs`.
2. `server/internal/chat/httpapi/handler.go` parses UI-friendly run payloads and calls `optionc.Manager.StartRun`.
3. `optionc.Manager` owns run lifecycle, active-run enforcement, persistence, context planning, tool execution, SSE broadcast, artifact persistence, and final status updates.
4. `engine.RunAgentLoop` executes provider streaming and tool loops through callbacks supplied by the manager.
5. The UI streams `/api/runs/{runId}/events`, fetches artifacts from `/api/artifacts/{artifactId}`, and polls query jobs through `/api/queries/{jobId}/status`.

Key files:

- `server/internal/chat/httpapi/handler.go`
- `server/internal/chat/optionc/manager.go`
- `server/internal/chat/engine/engine.go`
- `server/internal/chat/store/store.go`
- `server/internal/server/handler.go`
- `hooks/useChat.ts`
- `types.ts`

There is also a cleaner `server/internal/chat/api` package with thread/message/run abstractions and tests, but it is not the production path wired by `server/cmd/ai-analyst/main.go`. Treat it as design reference, not as the implementation target, unless the production manager is deliberately adapted to it.

## Design Principles

1. Reuse `optionc.Manager`.
   - Do not call `engine.RunAgentLoop` directly from the external API.
   - Direct engine usage skips persistence, active-run conflicts, context planning, tool filtering, artifact storage, and post-run jobs.

2. Add a versioned public contract.
   - Existing UI endpoints can remain permissive and UI-shaped.
   - External endpoints should be typed, documented, stable, and versioned.

3. Expose outputs as structured objects, not UI reconstruction work.
   - Tool result envelopes should be first-class JSON.
   - Artifacts should be first-class retrievable resources.
   - UI-only flags should not leak into public contracts.

4. Keep streaming and final-result modes.
   - Streaming is required for parity with the chat UI.
   - A final-result endpoint is required for API consumers that do not want to manage SSE.

5. Add authorization before broad exposure.
   - Current protection is local activation/origin gating.
   - Public API clients need explicit API keys or tokens with scopes, stored in local settings/secrets rather than introduced as env vars by default.

6. Make replay semantics explicit.
   - V1 assigns manager-level monotonic sequence numbers and supports `after` for events retained in the in-memory run buffer.
   - Final result retrieval is deterministic through persisted messages/artifacts.
   - Durable event replay after manager state expires remains a migration item.

## Proposed Namespace

Use a separate versioned namespace:

```text
/api/external/v1
```

This avoids breaking the current UI contract and gives the app room to evolve the public API independently.

### Status

```http
GET /api/external/v1/status
Authorization: Bearer aia_...
```

Response:

```json
{
  "object": "api.status",
  "ok": true,
  "version": "v1"
}
```

## Authentication And Access Control

### API Keys

Add an API-key model stored in the existing local settings/secrets system:

```json
{
  "id": "key_...",
  "name": "Reporting job",
  "prefix": "aia_...",
  "hash": "sha256-hex-of-high-entropy-token",
  "scopes": [
    "runs:create",
    "runs:read",
    "runs:cancel",
    "threads:read",
    "threads:write",
    "artifacts:read",
    "attachments:write",
    "queries:read",
    "queries:cancel"
  ],
  "allowedThreadIds": [],
  "allowedProjectIds": [],
  "createdAt": 1778937600000,
  "lastUsedAt": 1778937600000,
  "revokedAt": null
}
```

API clients send:

```http
Authorization: Bearer aia_...
```

Implementation notes:

- Bearer schemes are accepted case-insensitively.
- API keys are treated as high-entropy bearer tokens; the stored hash is lowercase SHA-256 hex.
- `prefix` stores the first 12 token characters for lookup/display and is not a secret.
- Empty `allowedThreadIds` and `allowedProjectIds` means workspace-wide access for the granted scopes.
- Non-empty allowlists restrict the key to matching threads and/or threads in matching projects. A resource-scoped key can create a new thread only when `project_id` is present and allowed.

### Scope Checks

Suggested endpoint scopes:

| Endpoint | Required scope |
| --- | --- |
| `GET /status` | any valid API key |
| `POST /runs` | `runs:create` |
| `GET /runs/{runId}` | `runs:read` |
| `GET /runs/{runId}/events` | `runs:read` |
| `GET /runs/{runId}/result` | `runs:read` |
| `POST /runs/{runId}/cancel` | `runs:cancel` |
| `GET /threads/{threadId}/messages` | `threads:read` |
| `POST /threads/{threadId}/attachments` | `attachments:write` |
| `GET /artifacts/{artifactId}` | `artifacts:read` |
| `GET /artifacts/{artifactId}/rows` | `artifacts:read` |
| `GET /queries/{jobId}/status` | `queries:read` |
| `POST /queries/{jobId}/cancel` | `queries:cancel` |

### Redaction Rules

Public API responses must not expose:

- provider API keys or secret headers
- raw provider request payloads containing credentials
- query `owner_token` unless specifically needed by the endpoint and caller scope
- local absolute file paths
- hidden context unless a debug/admin scope is added
- raw provider error bodies if they may contain request headers or secrets

## Resource Model

### Thread

```json
{
  "object": "thread",
  "id": "uuid",
  "title": "Revenue analysis",
  "summary": "Optional summary",
  "status": "active",
  "project_id": "uuid",
  "artifact_count": 3,
  "created_at": 1778937600000,
  "updated_at": 1778937600000,
  "last_message_at": 1778937600000,
  "last_run_id": "uuid"
}
```

This maps to `store.Thread` and the existing `threadToDTO` helper.

### Run

```json
{
  "object": "run",
  "id": "uuid",
  "thread_id": "uuid",
  "status": "queued|running|completed|failed|cancelled",
  "purpose": "analysis|search",
  "provider_id": "openai|anthropic|gemini|compatible|bedrock",
  "model": "string",
  "profile_id": "string",
  "max_iterations": 200,
  "created_at": 1778937600000,
  "started_at": 1778937600000,
  "ended_at": 1778937600000,
  "usage": {
    "request_id": "string",
    "input_tokens": 0,
    "output_tokens": 0,
    "total_tokens": 0
  },
  "error": {
    "code": "FAILED",
    "message": "string"
  },
  "context": {
    "tokens_before": 0,
    "tokens_after": 0,
    "summary_buckets": 0,
    "summary_rounds": 0,
    "artifact_refs_loaded": 0,
    "need_retrieval": false,
    "need_retrieval_tooling": false,
    "hard_capped": false,
    "overflow_compacted": false,
    "plan": {}
  }
}
```

This maps to `store.Run` and the existing `runToDTO` helper, but should be normalized into nested `usage`, `error`, and `context` objects for the external contract.

### Message

```json
{
  "object": "message",
  "id": "uuid",
  "thread_id": "uuid",
  "run_id": "uuid",
  "seq": 12,
  "role": "user|assistant|tool",
  "content": "string or null",
  "tool_calls": [
    {
      "id": "call_...",
      "name": "display_table",
      "args": {},
      "provider_executed": false
    }
  ],
  "tool_call_id": "call_...",
  "name": "display_table",
  "referenced_artifact_ids": [],
  "referenced_message_ids": [],
  "referenced_thread_ids": [],
  "created_at": 1778937600000,
  "updated_at": 1778937600000
}
```

Do not expose these UI-only fields:

- `_uiStableId`
- `_uiStreaming`
- `_uiRunId`
- `_uiStartedAt`
- streaming markdown segmentation fields
- client-only optimistic placeholders

### Tool Result

Today the UI parses a JSON string from `message.content`. The external API should provide a typed field:

```json
{
  "object": "tool_result",
  "artifact_id": "uuid",
  "tool_name": "display_table",
  "tool_call_id": "call_...",
  "ok": true,
  "result_format": "json",
  "result": {},
  "query_metadata": {},
  "result_metadata": {},
  "plot_spec": {},
  "chart_budget": {},
  "chart_engine": "highcharts",
  "chart_spec_version": "highcharts-plot-spec.v2",
  "chart_module": "core",
  "chart_modules": [],
  "preview_only": false,
  "full_artifact_id": "uuid",
  "row_count_total": 1000,
  "downsample_ratio": 0.5,
  "display": {
    "suppress": false
  }
}
```

Migration rule:

- Keep persisting the current JSON envelope for model replay.
- Add response mapping that parses the envelope into `tool_result` for external responses.
- Later, optionally store parsed envelope columns or JSON fields in the store for faster querying.

### Artifact

```json
{
  "object": "artifact",
  "id": "uuid",
  "thread_id": "uuid",
  "message_id": "uuid",
  "type": "data|code|image|file",
  "title": "Query results",
  "language": "json",
  "content": "string",
  "metadata": {},
  "created_at": 1778937600000
}
```

Large artifact behavior:

- `GET /artifacts/{artifactId}` returns metadata and content when content is within response limits.
- Use `GET /artifacts/{artifactId}/rows` for large tabular data.
- Use a download endpoint later if binary file responses are needed.

## Endpoints

### Create Run

```http
POST /api/external/v1/runs
Content-Type: application/json
Authorization: Bearer aia_...
```

```json
{
  "thread_id": "optional-existing-thread-id",
  "project_id": "optional-project-id",
  "input": {
    "content": "Analyze sales by region and produce a chart.",
    "hidden_context": "optional hidden instruction",
    "referenced_artifact_ids": [],
    "referenced_message_ids": [],
    "referenced_thread_ids": [],
    "image_attachments": [],
    "file_attachments": []
  },
  "purpose": "analysis",
  "search_depth": "shallow",
  "max_iterations": 200,
  "data_sources": {
    "bigquery": { "enabled": true },
    "databricks": { "enabled": false },
    "trino": { "enabled": false },
    "snowflake": { "enabled": false }
  },
  "runtime_context": {
    "now_iso": "2026-05-16T12:00:00Z",
    "timezone": "Europe/London",
    "utc_offset_minutes": 60
  },
  "stream": false
}
```

Response:

```json
{
  "object": "run.create_response",
  "thread": {
    "object": "thread",
    "id": "uuid"
  },
  "run": {
    "object": "run",
    "id": "uuid",
    "thread_id": "uuid",
    "status": "queued"
  },
  "events_url": "/api/external/v1/runs/uuid/events",
  "result_url": "/api/external/v1/runs/uuid/result"
}
```

Behavior:

- If `thread_id` is omitted, create a new thread.
- If `thread_id` is present, validate it exists and belongs to the caller scope.
- Reuse `optionc.StartRunRequest`.
- Return `409` when the thread already has an active run.
- Do not require the caller to separately append a user message; `StartRun` already persists it.

### Stream Run Events

```http
GET /api/external/v1/runs/{runId}/events?after=0
Accept: text/event-stream
Authorization: Bearer aia_...
```

SSE frame:

```text
event: text_delta
data: {"object":"run.event","run_id":"uuid","thread_id":"uuid","sequence":12,"type":"text_delta","created_at":1778937600000,"data":{"content":"Hello"}}
```

`sequence` is assigned by `optionc.Manager.broadcast` and is stable for the lifetime of the retained in-memory run state. `after=N` skips retained events with `sequence <= N` before forwarding live events. It is not durable after manager state expires; durable replay is Phase 7.

External event names:

| Event | Data |
| --- | --- |
| `run_started` | `{ "run": Run }` |
| `assistant_started` | `{}` |
| `text_delta` | `{ "content": "string" }` |
| `thinking_start` | `{ "trace_id": "string" }` |
| `thinking_delta` | `{ "content": "string" }` |
| `thinking_end` | `{ "summary": "string" }` |
| `compaction_start` | `{}` |
| `compaction_delta` | `{ "content": "string" }` |
| `compaction_end` | `{}` |
| `tool_calls` | `{ "tool_calls": [ToolCall] }` |
| `tool_progress` | `ToolProgressEvent` |
| `tool_result` | `{ "message": Message, "tool_result": ToolResult }` |
| `artifact_created` | `{ "artifact": Artifact }`; reserved for internal artifact events |
| `assistant_completed` | `{ "message": Message }` |
| `run_completed` | `{ "run": Run }` |
| `run_failed` | `{ "run": Run, "error": Error }` |
| `run_cancelled` | `{ "run": Run }` |
| `error` | `{ "error": Error }` |

Internal-to-external mapping:

| Internal manager event | External event |
| --- | --- |
| `run_started` | `run_started` |
| `assistant_start` | `assistant_started` |
| `text_delta` | `text_delta` |
| `thinking_start` | `thinking_start` |
| `thinking_delta` | `thinking_delta` |
| `thinking_end` | `thinking_end` |
| `compaction_start` | `compaction_start` |
| `compaction_delta` | `compaction_delta` |
| `compaction_end` | `compaction_end` |
| `tool_calls` | `tool_calls` |
| `tool_progress` | `tool_progress` |
| `tool_result` | `tool_result` |
| `assistant_final` | `assistant_completed` |
| `final` after completed status | `run_completed` |
| `final` after cancelled status | `run_cancelled` |
| `final` after failed status | `run_failed` |
| `error` | `error` |

### Get Run

```http
GET /api/external/v1/runs/{runId}
```

Returns the normalized `Run`.

### Get Final Run Result

```http
GET /api/external/v1/runs/{runId}/result?include_artifacts=true&include_tool_results=true
```

Response:

```json
{
  "object": "run.result",
  "run": {},
  "thread": {},
  "messages": [],
  "assistant_messages": [],
  "tool_results": [],
  "artifacts": [],
  "answer": "Final assistant text assembled from assistant messages for this run.",
  "usage": {},
  "error": null
}
```

Behavior:

- If the run is non-terminal, return `409` with current run status unless `wait=true` is provided.
- With `wait=true`, hold the request up to a bounded timeout, for example `timeout_ms <= 30000`.
- Assemble results by `run_id`, not by entire thread.

### Cancel Run

```http
POST /api/external/v1/runs/{runId}/cancel
```

Returns the current normalized run. Uses `optionc.Manager.Cancel`.

### List Messages

```http
GET /api/external/v1/threads/{threadId}/messages?limit=100&after_seq=12&include_tool_results=true
```

Response:

```json
{
  "object": "list",
  "data": [],
  "has_more": false
}
```

Use `store.ListMessagesParams` rather than the UI route's hardcoded `Limit: 1000`.

### Upload Attachment

```http
POST /api/external/v1/threads/{threadId}/attachments
Content-Type: text/csv
X-File-Name: sales.csv
```

The request body is the raw file bytes, matching the existing UI upload implementation. The filename is required through one of:

- query parameter `filename`
- query parameter `name`
- header `X-File-Name`

Response:

```json
{
  "object": "attachment.create_response",
  "artifact": {},
  "attachment": {
    "artifactId": "uuid",
    "fileName": "sales.csv",
    "mimeType": "text/csv",
    "sizeBytes": 12345,
    "isText": true,
    "previewAvailable": true,
    "previewTruncated": false
  }
}
```

### Get Artifact

```http
GET /api/external/v1/artifacts/{artifactId}
```

Returns normalized `Artifact`.

### Get Artifact Rows

```http
GET /api/external/v1/artifacts/{artifactId}/rows?offset=0&limit=200&columns=region,total
```

Response:

```json
{
  "object": "artifact.rows",
  "artifact_id": "uuid",
  "rows": [],
  "offset": 0,
  "next_offset": 200,
  "limit": 200,
  "rows_returned": 200,
  "row_count_total": 1000,
  "has_more": true
}
```

### Query Job Status

```http
GET /api/external/v1/queries/{jobId}/status?thread_id=uuid
X-Query-Owner-Token: owner-token-from-tool-result
```

Use the current query-job registry path. Direct query status/cancel endpoints require `X-Query-Owner-Token`; that token is emitted only through the owning run's tool result. Public query responses redact `owner_token` / `ownerToken`. Manager query errors map to public status codes:

- query registry unavailable: `503`
- wrong owner token or thread scope: `403`
- missing owner token: `401`

## Streaming Versus Blocking Run Creation

Two options are useful:

1. `POST /runs` returns immediately with `run.status`.
2. `POST /runs?stream=true` returns SSE directly.

The first option is easier to implement and should be v1. The second can be added later by creating the run, then internally attaching to the same event stream in the request handler.

Current v1 rejects `stream=true` on `POST /runs`; clients should call the returned `events_url`.

## Internal API Reuse

### Extract Shared Request Parsing

Current `handleCreateRun` accepts many shapes through `map[string]any`:

- top-level `content`
- nested `message.content`
- camelCase and snake_case references
- image/file attachments
- datasource overrides
- runtime context

For the external API:

1. Create typed DTOs for the public request.
2. Convert typed DTOs into `optionc.StartRunRequest`.
3. Leave current UI parsing in place, or refactor both UI and external handlers to a shared internal normalized request struct.

Suggested internal struct:

```go
type NormalizedRunRequest struct {
    ThreadID string
    ProjectID *string
    Content string
    HiddenContext string
    ReferencedMessageIDs []string
    ReferencedThreadIDs []string
    ReferencedArtifactIDs []string
    ImageAttachments []types.ImageAttachment
    FileAttachments []types.FileAttachment
    Purpose string
    SearchDepth string
    MaxIterations *int
    DataSources *config.DatasourceConnections
    DataSourceOverrides map[string]any
    RuntimeContext RuntimeContext
}
```

### Response Mappers

Create public mappers instead of reusing UI DTOs directly:

- `publicThreadDTO(store.Thread)`
- `publicRunDTO(store.Run)`
- `publicMessageDTO(store.Message)`
- `publicArtifactDTO(store.Artifact, content string)`
- `publicToolResultDTO(message store.Message)`
- `publicSSEEvent(internal optionc.SSEEvent, run store.Run)`

This avoids leaking UI-only fields and lets the external contract normalize naming.

## Persistence Changes

### Required For V1

No schema change is required for the first API version. Result assembly queries existing thread messages, filters by `run_id`, parses tool-result envelopes, and includes artifacts referenced by run messages or tool results.

Required helper additions:

- derive run messages from `ListMessages` by thread + `run_id`
- derive artifacts from run messages and referenced artifact IDs
- parse tool-result envelopes into structured response fields

If this becomes a performance bottleneck, add store-level methods:

```go
ListMessagesByRun(ctx context.Context, runID string, limit int) ([]store.Message, error)
ListArtifactsByRun(ctx context.Context, runID string, limit int) ([]store.Artifact, error)
```

### Recommended For Durable Event Replay

Add persisted run events for the production `optionc.Manager` path, similar to the separate `chat/runs.Manager` package:

```go
type RunEvent struct {
    RunID string
    ThreadID string
    Seq int64
    Type string
    CreatedAt time.Time
    Data json.RawMessage
}
```

Store methods:

```go
AppendRunEvent(ctx context.Context, event RunEvent) error
ListRunEvents(ctx context.Context, runID string, afterSeq int64, limit int) ([]RunEvent, error)
```

Migration path:

1. Add DB/JSON storage support.
2. Keep the existing manager-assigned event sequence and in-memory subscriber path for live delivery.
3. On every `broadcast`, also append the normalized event and sequence to persistent storage.
4. Make public `/events?after=` first replay persisted events, then attach to live subscription without duplicating live events.
5. Optionally migrate UI `/api/runs/{runId}/events` to use the same replay path later.

## Migration Plan

### Phase 0: Documentation And Contract Approval

Deliverables:

- This document.
- Example request/response fixtures.
- Decision on auth model and scopes.
- Decision on whether public event replay must be durable in v1 or can wait for v1.1.

Exit criteria:

- Endpoint list and object model are accepted.
- Redaction policy is accepted.
- Backward compatibility requirements for existing UI endpoints are explicit.

### Phase 1: Public API Skeleton

Deliverables:

- New package, for example `server/internal/chat/publicapi`.
- New handler mounted under `/api/external/v1`.
- Auth middleware for public API keys.
- `GET /api/external/v1/status` for API clients.
- Typed error response helper.

Exit criteria:

- Public route mounting does not affect existing `/api/threads`, `/api/runs`, or frontend static serving.
- Unauthenticated requests fail with `401`.
- Missing scopes fail with `403`.

### Phase 2: Run Creation And Status

Deliverables:

- `POST /runs`
- `GET /runs/{runId}`
- `POST /runs/{runId}/cancel`
- DTO conversion into `optionc.StartRunRequest`
- Conflict handling for active runs.

Exit criteria:

- API-created runs appear in the existing UI thread list.
- UI-created runs can be read through public `GET /runs/{runId}` when authorized.
- Cancelling through public API updates UI state.

### Phase 3: Streaming Event Mapping

Deliverables:

- `GET /runs/{runId}/events`
- Internal-to-external event mapper.
- Event envelope with `run_id`, `thread_id`, `sequence`, `type`, `created_at`, and `data`.
- SSE parser compatibility fixtures.

Exit criteria:

- Public stream reproduces assistant text, tool calls, tool progress, tool results, and terminal state.
- Event names and payloads match docs.
- Existing UI stream remains unchanged.

### Phase 4: Final Result Assembly

Deliverables:

- `GET /runs/{runId}/result`
- Run-scoped messages.
- Parsed tool results.
- Run-scoped artifacts.
- Optional `wait=true&timeout_ms=...`.

Exit criteria:

- A client can create a run, wait for completion, and retrieve all outputs without SSE.
- Tool result envelopes are returned as structured JSON.
- Artifacts referenced by tool results are included when requested.

### Phase 5: Attachments And Artifacts

Deliverables:

- External attachment upload route.
- External artifact get route.
- External artifact rows route.
- Response-size and row-limit documentation.

Exit criteria:

- Uploaded files can be referenced in a later public run.
- Image attachments follow existing size/count limits.
- Large tabular artifacts are retrievable by rows.

### Phase 6: Query Jobs

Deliverables:

- External query status and cancel endpoints.
- Redacted query metadata.
- Owner-token handling policy.

Exit criteria:

- Async query jobs launched by agent tools can be tracked externally.
- Cancelling an individual query does not require cancelling the run.
- Unauthorized access to another thread's query job fails.

### Phase 7: Durable Event Replay

Deliverables:

- Persisted run-event store support.
- Replay before live subscribe.
- `after` cursor support.
- Retention policy.

Exit criteria:

- Clients can disconnect and resume event streaming.
- Terminal events are still available after manager in-memory state expires.
- Replay order is deterministic.

### Phase 8: OpenAPI And SDK Fixtures

Deliverables:

- OpenAPI 3.1 spec.
- Example curl snippets.
- JSON fixtures for create-run, event stream, final result, artifact, and error responses.
- Optional lightweight TypeScript client.

Exit criteria:

- Contract tests validate fixtures against the OpenAPI schema.
- Docs are enough for an external client to integrate without reading frontend code.

## Backward Compatibility

Do not break:

- `POST /api/threads/{threadId}/runs`
- `GET /api/runs/{runId}`
- `GET /api/runs/{runId}/events`
- `GET /api/threads/{threadId}`
- `GET /api/artifacts/{artifactId}`
- `GET /api/artifacts/{artifactId}/rows`
- `POST /api/threads/{threadId}/attachments`
- query status/cancel endpoints

The UI can migrate later, but v1 should leave existing UI routes untouched.

## Required Tests

### Unit Tests

#### Request Normalization

Add tests for:

- minimal `POST /runs` body with no `thread_id`
- existing `thread_id`
- missing content and no attachments
- referenced artifact/message/thread IDs
- max 5 referenced threads
- image attachment guardrails
- file attachment references
- `purpose=analysis`
- `purpose=search`
- invalid purpose
- `search_depth` defaulting
- datasource enabled overrides
- runtime context parsing
- max iteration override

Expected files:

- `server/internal/chat/publicapi/handler_test.go`
- or `server/internal/chat/httpapi/external_request_test.go` if sharing parsing there

#### DTO Mapping

Add tests for:

- `store.Thread` to public thread
- `store.Run` to nested public usage/error/context
- `store.Message` to public message
- tool result envelope parsing from tool message content
- malformed tool result content
- artifact DTO with inline content
- artifact DTO with file-backed content
- redaction of hidden context and provider payload

#### Event Mapping

Add tests for all internal events:

- `run_started`
- `assistant_start`
- `text_delta`
- `thinking_start`
- `thinking_delta`
- `thinking_end`
- `compaction_start`
- `compaction_delta`
- `compaction_end`
- `tool_calls`
- `tool_progress`
- `tool_result`
- `assistant_final`
- `final`
- `error`

Assertions:

- external event name is stable
- payload uses snake_case or documented naming consistently
- no UI-only fields are emitted
- malformed payload produces `error` event, not panic
- `after` skips retained events with sequence less than or equal to the cursor

#### Auth Middleware

Add tests for:

- missing `Authorization`
- malformed bearer token
- unknown key
- revoked key
- valid key
- missing scope
- multiple scopes
- last-used timestamp update
- optional thread/project resource allowlists

#### Redaction

Add tests for:

- provider payload not included
- hidden context not included
- query owner token not included by default
- absolute file paths converted or omitted
- provider error with secret-looking content redacted

### Manager/Store Tests

Add tests for:

- run result assembly uses only messages from the requested `run_id`
- artifacts are included only when referenced by run messages or created by run tools
- active-run conflict returns `409` with active run ID when safe
- cancellation through public API calls `Manager.Cancel`
- completed run result includes usage tokens when persisted
- failed run result includes normalized error

If durable replay is implemented:

- `broadcast` persists events with increasing sequence
- replay returns events after `after` cursor
- replay then live subscribe does not duplicate events
- terminal run replay works after in-memory manager state expires
- JSON store and MySQL store both preserve event order

### HTTP Handler Tests

Add integration-style handler tests for:

- `GET /api/external/v1/status`
- `POST /api/external/v1/runs`
- `GET /api/external/v1/runs/{runId}`
- `GET /api/external/v1/runs/{runId}/events`
- `GET /api/external/v1/runs/{runId}/result`
- `POST /api/external/v1/runs/{runId}/cancel`
- `GET /api/external/v1/threads/{threadId}/messages`
- `POST /api/external/v1/threads/{threadId}/attachments`
- `GET /api/external/v1/artifacts/{artifactId}`
- `GET /api/external/v1/artifacts/{artifactId}/rows`
- query status and cancel endpoints

Use `httptest` with fake store/manager components where possible. Use the existing production handler tests as examples.

### Provider Adapter Regression Tests

The public API depends on normalized provider events. Keep or add regression tests for:

- OpenAI-compatible Responses API tool-call accumulation
- OpenAI-compatible Chat Completions tool-call accumulation
- Anthropic thinking events
- Anthropic compaction events
- Anthropic server-tool behavior not leaking as executable local tools
- Gemini generated tool call IDs
- provider errors normalized into public errors

### Frontend Regression Tests

The public API should not break existing UI behavior. Run existing frontend tests and add at least one regression proving current routes still work.

Relevant existing tests:

- `tests/e2e/sse-event-parser.spec.ts`
- `tests/e2e/sse-parser-behavior.spec.ts`
- `tests/e2e/thread-isolation.spec.ts`
- `tests/regressions/assistant-final-streaming-finalization.test.mjs`
- `tests/regressions/tool-result-envelope-chart-engine.test.mjs`
- `tests/regressions/tool-result-envelope-plot-spec.test.mjs`

Add tests for:

- UI can still start and stream a run through existing endpoints.
- UI artifact panel still loads artifacts from existing artifact routes.
- UI query tray still polls existing query endpoints.

### End-To-End API Tests

Add an API-only e2e suite that runs against a local test server:

1. Create run with a fake provider that streams two text deltas and final output.
2. Stream events and assert ordered output.
3. Fetch final result and assert assembled answer.
4. Create a tool result with an artifact and assert result includes both.
5. Upload attachment and reference it in a run.
6. Cancel a long-running fake run.
7. Simulate async query job and poll status.
8. Validate auth failures.

These tests should avoid live provider calls.

### Contract Tests

Once OpenAPI is added:

- validate example request fixtures against request schemas
- validate handler response fixtures against response schemas
- validate error responses
- validate SSE event payload fixtures by event type

### Manual QA

Manual verification checklist:

1. Create API key in settings.
2. Create a run with `curl`.
3. Stream run events with `curl -N`.
4. Fetch final result after completion.
5. Fetch generated artifact.
6. Fetch artifact rows for data artifact.
7. Upload a file and use it in a later run.
8. Cancel a run.
9. Confirm existing UI still works.
10. Confirm unauthorized API requests fail.

## Error Model

Use a consistent error envelope:

```json
{
  "error": {
    "code": "invalid_request",
    "message": "content is required",
    "param": "input.content",
    "request_id": "optional"
  }
}
```

Recommended codes:

- `invalid_request`
- `authentication_required`
- `permission_denied`
- `not_found`
- `conflict`
- `run_active`
- `run_not_terminal`
- `payload_too_large`
- `rate_limited`
- `provider_error`
- `tool_error`
- `internal_error`

HTTP mapping:

| Code | HTTP |
| --- | --- |
| `invalid_request` | `400` |
| `authentication_required` | `401` |
| `permission_denied` | `403` |
| `not_found` | `404` |
| `conflict`, `run_active`, `run_not_terminal` | `409` |
| `payload_too_large` | `413` |
| `rate_limited` | `429` |
| `provider_error`, `tool_error` | `502` or `500` depending on source |
| `internal_error` | `500` |

## Versioning

Version the path and response objects:

- path: `/api/external/v1`
- object values: `run`, `message`, `artifact`, `run.event`, `run.result`
- optional schema field for complex objects: `schema_version: 1`

Breaking changes require `/v2`. Additive fields are allowed in v1.

## Decisions And Remaining Questions

Decided for this v1:

1. Durable final-result retrieval is required; durable event replay is deferred to Phase 7.
2. API-created threads are normal threads and are visible in the existing UI.
3. Query status/cancel requires both API-key authorization and the query owner token header; owner tokens are redacted from query responses.
4. `stream=true` on `POST /runs` is deferred. V1 returns URLs for SSE and final-result retrieval.
5. Public API clients use current app provider/model settings, with request-level controls limited to the documented run fields.

Remaining:

1. Add UI or local admin tooling for creating/revoking public API keys.
2. Add OpenAPI/SDK fixtures once the v1 surface settles.

## Recommended V1 Scope

Implement first:

- API key auth with local settings/secrets storage.
- `GET /status`
- `POST /runs`
- `GET /runs/{runId}`
- `GET /runs/{runId}/events`
- `GET /runs/{runId}/result`
- `POST /runs/{runId}/cancel`
- `GET /threads/{threadId}/messages`
- `GET /artifacts/{artifactId}`
- `GET /artifacts/{artifactId}/rows`
- `POST /threads/{threadId}/attachments`
- query status/cancel endpoints
- typed response mappers
- structured tool result mapping
- tests for auth, request normalization, event mapping, result assembly, artifacts, and UI backward compatibility

Defer:

- direct streaming from `POST /runs`
- SDKs
- admin API-key management endpoints
- provider/model per-run overrides
- durable event replay if final-result endpoint is sufficient for first integration
