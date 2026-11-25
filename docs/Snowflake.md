# Snowflake V1 Contract (Release A)

This document is the canonical Snowflake v1 integration contract for this repository.

## Release-A decision and scope

- Dependency path: `REG-003` is available. Release A uses the query-provider registry path.
- Supported auth modes: `oauth_token`, `keypair_jwt`.
- Transport: Snowflake SQL API over HTTPS.
- In scope:
  - Cloud Credentials card and secrets wiring.
  - Read-only SQL execution.
  - Metadata browsing endpoints.
  - Async query jobs through the existing jobs registry.
- Out of scope:
  - Snowpipe.
  - Staged file operations.
  - Multi-statement-by-default behavior.
  - Durable async recovery across process restarts.

## Session context precedence

For all Snowflake routes and tool execution, configuration precedence is:

1. Per-request and per-run overrides.
2. Persisted provider settings.
3. Datasource connection state.

Datasource connection state is observational only and must not override explicit request/run settings.

## Read-only boundary

- User SQL must pass `sqlutil.IsReadOnlySQL()`.
- `PUT` and `GET` SQL commands are explicitly rejected in v1.
- Metadata queries are handler-owned constants and are not user-supplied SQL.

## Route contract

All routes are mounted under `/api/snowflake/*` and are feature-gated by `cloudCredentialsSnowflake`.

| Method | Path | Purpose |
|---|---|---|
| `GET` | `/api/snowflake/status` | Snowflake config/auth diagnostics |
| `POST` | `/api/snowflake/test` | Connectivity smoke (`SELECT 1`) |
| `POST` | `/api/snowflake/preflight` | Read-only + `EXPLAIN` precheck |
| `POST` | `/api/snowflake/query` | Read-only query execution |
| `GET` | `/api/snowflake/databases` | List databases |
| `GET` | `/api/snowflake/schemas` | List schemas |
| `GET` | `/api/snowflake/tables` | List tables |
| `GET` | `/api/snowflake/table-schema` | Describe table |

## Preflight contract

### HTTP 200 (pass or warning)

```json
{
  "ok": true,
  "severity": "ok|warning",
  "readOnly": true,
  "explainAttempted": true,
  "explainSucceeded": true,
  "warnings": []
}
```

`warning` is used when read-only validation passes but `EXPLAIN` is not executable (for example, permission or dialect constraints).

### HTTP 400 (blocking)

```json
{
  "ok": false,
  "severity": "error",
  "errorCode": "read_only_required|unsupported_command|invalid_sql|auth_error|...",
  "message": "human readable reason"
}
```

## Query contract

### Query lifecycle

The Snowflake execution lifecycle is:

1. Submit statement.
2. Poll statement handle until terminal.
3. Read inline partition (if present).
4. Fetch additional partitions in order.
5. Cancel on context cancellation or timeout.

### Submit retry and idempotency

- Submit retries require a stable request id.
- The same request id is reused across submit attempts.
- Retry submission without an idempotency key is disallowed.

### Direct query success shape

```json
{
  "rows": [{"col": "value"}],
  "query_metadata": {
    "provider": "snowflake",
    "duration_ms": 12,
    "snowflake_query_handle": "<statement-handle>",
    "job_id": "<statement-handle>",
    "request_id": "<request-id>",
    "status": "completed",
    "schema": [{"name": "col", "type": "TEXT"}]
  }
}
```

### Direct query failure shape

```json
{
  "ok": false,
  "error": {"message": "..."},
  "partial_rows": [{"col": "value"}],
  "query_metadata": {
    "provider": "snowflake",
    "snowflake_query_handle": "<statement-handle>",
    "job_id": "<statement-handle>",
    "request_id": "<request-id>",
    "schema": [{"name": "col", "type": "TEXT"}]
  }
}
```

### Async job contract (tool + jobs lifecycle)

`run_snowflake_query` can return a running response with stable fields:

```json
{
  "status": "running",
  "job_id": "qj-sf-...",
  "owner_token": "<opaque-owner-token>"
}
```

Job status and result polling use stable terminal states:

- `running`
- `completed`
- `failed`
- `cancelled`

Cancel semantics are idempotent. Repeated cancels of an already terminal job are treated as successful no-op behavior.

## Metadata endpoint contract

- Snowflake-native naming is used: `databases` (not `catalogs`).
- Required query params are validated with explicit 400 responses.
- Response payloads are shaped for the catalog browser renderer.

## Restart and orphan semantics (v1)

- Async job registry is in-memory in v1.
- Durable async recovery across process restarts is a v1 non-goal.
- Unknown `job_id` after restart returns not-found semantics (`404`/`not found`).
- Operator action: rerun the query from the client and verify Snowflake statement history if needed.

## Observability launch gate

Structured logs for submit/poll/partition/cancel include:

- `provider`
- `snowflake_query_handle`
- `job_id`
- `request_id`
- `event`
- `status`

Baseline metrics emitted:

- `snowflake_submit_latency_ms`
- `snowflake_poll_attempt_total`
- `snowflake_terminal_total`
- `snowflake_cancel_attempt_total`

## Compatibility notes

Any contract change after Release A requires an explicit amendment in this file and corresponding updates to tests and `plans/snowflake-integration-tickets/verification-matrix.md`.
