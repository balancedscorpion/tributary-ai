# Snowflake Integration Plan

## Purpose

Define a production-ready Snowflake v1 integration plan using the same execution and review quality as the Trino plan, while incorporating lessons learned from Trino delivery.

## Architecture Decision (V1)

Use Snowflake SQL API over HTTPS (`/api/v2/statements`) from Go (`net/http`) instead of adding a native Snowflake DB driver for v1.

## Why this is the right v1 choice

1. Aligns with existing HTTP-first datasource handlers (Databricks/Trino patterns).
2. Integrates cleanly with existing async submit/poll/cancel query jobs.
3. Avoids new driver lifecycle/pooling complexity and dependency risk.
4. Supports the documented SQL API auth methods required for service use cases.

## V1 Auth Scope

- `oauth_token`
- `keypair_jwt`

## V1 Non-Goals

- Username/password auth
- Browser/SAML auth flows (`externalbrowser`)
- Workload identity federation (WIF)
- PUT/GET stage operations
- Multi-statement SQL execution

## Hard Preconditions

1. Query-provider registry path must be available (`REG-003` landed), or fallback bridge ticket `SNOW-FB-001` must be applied first.
2. Snowflake feature flag policy must be build-enforced:
- Tributary build: `cloudCredentialsAws=false`, `cloudCredentialsTrino=false`, `cloudCredentialsSnowflake=false`
- All-providers build: these flags true
 - Release artifacts must not allow runtime env overrides of these three flags.
 - Any local dev env override behavior, if retained, must be explicitly documented as non-release only.
3. Async query contract must be frozen before implementation starts (running payload fields, terminal states, and cancel semantics) so `/api/snowflake/query` and jobs lifecycle remain aligned.

## Deliverables

- Release A ticketed implementation plan: `release-a-snowflake-v1.md`
- Release B follow-up enhancements: `release-b-snowflake-post-v1.md`
- Shared ticket template: `TICKET_TEMPLATE.md`
- Conditional fallback delivery path (if registry prerequisite is unavailable): `SNOW-FB-001` in Release A ticket order.

## Release-A launch gates

1. Async contract parity with current runtime (`running`, `completed`, `failed`, `cancelled`) across query endpoint, jobs registry, and prompt guidance.
2. Submit retry idempotency contract (stable request-id required for retries).
3. Baseline observability in Release A:
- structured logs for submit/poll/partition/cancel with keys: `provider`, `snowflake_query_handle`, `job_id`, `request_id`, `event`, `status`.
- baseline metrics: `snowflake_submit_latency_ms`, `snowflake_poll_attempt_total`, `snowflake_terminal_total`, `snowflake_cancel_attempt_total`.
4. Explicit v1 restart/orphan behavior documentation and runbook coverage for in-memory job registry limitations.
 - Runbook target: `docs/runbooks/snowflake-v1-ops.md`.
5. Session-context precedence is deterministic:
- per-request/per-run override > provider settings > datasource connection state.

## Primary Snowflake constraints reflected in this plan

1. SQL API is submit/poll/cancel handle-based.
2. SQL API auth is OAuth bearer or key-pair JWT.
3. SQL API results can be partitioned and must be fetched/assembled correctly.
4. SQL API does not support `PUT`/`GET`.
5. Session context (`warehouse`, `database`, `schema`, `role`) is required for predictable behavior.
