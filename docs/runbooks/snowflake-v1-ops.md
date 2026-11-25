# Snowflake V1 Ops Runbook

Operational runbook for Snowflake Release-A integration.

## Scope

Applies to Snowflake v1 paths:

- `/api/snowflake/status`
- `/api/snowflake/test`
- `/api/snowflake/preflight`
- `/api/snowflake/query`
- `/api/snowflake/databases`
- `/api/snowflake/schemas`
- `/api/snowflake/tables`
- `/api/snowflake/table-schema`
- Async query job lifecycle via `/api/queries/{jobId}/status` and `/api/queries/{jobId}/cancel`

## Preconditions checklist

1. Feature flag `cloudCredentialsSnowflake` is enabled for the build profile.
2. Secrets store is initialized and unlocked.
3. Snowflake secret mode is configured as exactly one of:
   - `oauth_token`
   - `keypair_jwt`
4. Snowflake account/warehouse/database settings are present and validated.

## Baseline diagnostics

1. Check server route availability:
   - `GET /api/snowflake/status`
2. Validate credentials + connectivity:
   - `POST /api/snowflake/test`
3. Validate SQL safety before execution:
   - `POST /api/snowflake/preflight`
4. Run query:
   - `POST /api/snowflake/query`
5. If async tool path is used:
   - poll `GET /api/queries/{jobId}/status`
   - cancel `POST /api/queries/{jobId}/cancel`

## Telemetry contract

### Required log keys

For submit/poll/partition/cancel events, logs must include:

- `provider`
- `snowflake_query_handle`
- `job_id`
- `request_id`
- `event`
- `status`

### Required metric names

- `snowflake_submit_latency_ms`
- `snowflake_poll_attempt_total`
- `snowflake_terminal_total`
- `snowflake_cancel_attempt_total`

## Failure triage

### 1) Auth failures

Symptoms:

- `401`/`403` from Snowflake routes.
- `status` route reports `auth_error`.

Actions:

1. Confirm secret mode (`oauth_token` vs `keypair_jwt`) matches configured material.
2. Re-set secrets using `/api/secrets/set` for `providerId=snowflake`.
3. Re-run `/api/snowflake/test`.

### 2) Preflight failures

Symptoms:

- HTTP `400` with `errorCode=read_only_required` or `unsupported_command`.

Actions:

1. Rewrite query into read-only SQL.
2. Remove blocked commands (`PUT`, `GET`).
3. Re-run preflight.

### 3) Query timeout/cancel

Symptoms:

- Query fails with timeout/cancel path.
- Terminal log shows `event=terminal status=cancelled`.

Actions:

1. Check submit latency and poll-attempt metrics.
2. Validate warehouse size and statement complexity.
3. Re-run query with reduced scope.

### 4) Partition expiry (`404`/`410`)

Symptoms:

- Query failure message indicates expired result partition.

Actions:

1. Re-run query to generate fresh result partitions.
2. Confirm no long delay between submit and partition fetch in logs.

### 5) Async not found after restart (v1 orphan case)

Symptoms:

- Async status/cancel endpoint returns `not found` for previous `job_id`.

Cause:

- v1 job registry is in-memory; restart drops active registry state.

Operator guidance:

1. Treat the previous `job_id` as orphaned local state.
2. Re-run the query from client/tooling.
3. Use Snowflake query history with prior `request_id`/statement handle for audit.
4. If statement is still running upstream, cancel it directly in Snowflake console/history.

## Cancel semantics

- Cancel requests are idempotent for job lifecycle.
- Repeated cancel for terminal jobs is no-op success.
- For running statements, cancel attempts increment `snowflake_cancel_attempt_total`.

## Build-profile behavior

- Tributary (`!allproviders`): Snowflake cloud credentials routes/UI are not exposed.
- All-providers (`allproviders`): Snowflake routes/UI are exposed.

## Escalation data to capture

For incident handoff, capture:

1. `request_id`
2. `snowflake_query_handle`
3. `job_id`
4. endpoint called and payload shape (redacted)
5. log lines for submit/poll/partition/cancel
6. metric snapshots for the four Snowflake baseline metrics
