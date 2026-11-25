# Release B: Snowflake Post-V1 Enhancements

## Goal

Expand Snowflake integration beyond v1 baseline after production stabilization.

## Ticket Order

| Ticket | Title | Depends On |
|---|---|---|
| SNOW-B01 | OAuth Refresh Flow (Snowflake-Specific Token Exchange) | SNOW-017 |
| SNOW-B02 | Workload Identity Federation Path | SNOW-017 |
| SNOW-B03 | Guarded Multi-Statement Support | SNOW-B01 |
| SNOW-B04 | Deep Observability + SLO Instrumentation | SNOW-017 |
| SNOW-B05 | Durable Async Recovery + Orphan Reconciliation | SNOW-017 |

---

## SNOW-B01

### Title

OAuth Refresh Flow (Snowflake-Specific Token Exchange)

### Objective

Support long-lived OAuth sessions by adding refresh-token handling compatible with Snowflake OAuth token exchange semantics.

### Scope

- In scope: refresh-token storage, refresh scheduling, retry/failure behavior.
- In scope: Snowflake-specific token endpoint expectations and parameter contract.
- Out of scope: browser auth UX.

### Dependencies

- `SNOW-017`

### Files

- `server/internal/snowflake/auth.go`
- `server/internal/secrets/store.go`
- `server/internal/snowflake/auth_test.go`

### Implementation Notes

1. Do not assume generic OAuth2 provider defaults; Snowflake token exchange contract must be documented and implemented explicitly.
2. Keep refreshed access tokens in-memory cache and encrypted store boundary as needed.

### Acceptance Criteria

- [ ] Secure refresh-token storage model added for Snowflake OAuth.
- [ ] Refresh flow uses documented Snowflake token exchange endpoint/parameters.
- [ ] Refresh-on-expiry with safety margin is implemented.
- [ ] Refresh failures produce explicit retry/fail behavior.
- [ ] Refreshed tokens are never persisted in plain settings.

### Test Plan

1. Unit tests for refresh token parsing, expiry handling, and retry behavior.
2. Integration tests for Snowflake token exchange success and failure paths.
3. Manual staging run validating refresh under long-lived sessions.

### Risks

- Provider-specific OAuth exchange mismatches can silently fail refresh.

### Rollout Notes

- Ship behind a dedicated opt-in flag until production confidence is established.

---

## SNOW-B02

### Title

Workload Identity Federation Path

### Objective

Enable secretless Snowflake authentication where supported.

### Scope

- In scope: Snowflake-compatible workload identity authentication path and runtime wiring.
- In scope: provider-specific setup documentation and feature-flag gating.
- Out of scope: replacing v1 OAuth/keypair defaults.

### Dependencies

- `SNOW-017`

### Files

- `server/internal/snowflake/auth.go`
- `server/internal/config/settings.go`
- `server/internal/config/settings_test.go`
- `server/internal/snowflake/auth_test.go`
- `docs/Snowflake.md`

### Implementation Notes

1. Validate SQL API compatibility for the selected WIF flow before implementation.
2. Keep WIF optional behind a dedicated flag until proven in production.
3. Document cloud-identity provider prerequisites and trust-chain assumptions.

### Acceptance Criteria

- [ ] Implementation path is selected and documented.
- [ ] Cloud-specific setup expectations are documented.
- [ ] Feature flag keeps WIF opt-in.
- [ ] End-to-end tests exist for at least one WIF path.

### Test Plan

1. Unit tests for auth path selection and failure behavior.
2. Integration tests for one WIF provider flow.
3. Manual validation with a real workload identity principal.

### Risks

- Runtime auth failure if workload identity tokens are issued with unexpected claims.
- Operational complexity from cloud-specific trust policy configuration.

### Rollout Notes

- Roll out to internal environments first with explicit feature flag enablement.

---

## SNOW-B03

### Title

Guarded Multi-Statement Support

### Objective

Add explicit opt-in multi-statement SQL API support.

### Scope

- In scope: request-level opt-in controls, statement count validation, read-only safeguards.
- In scope: SQL API compatibility guardrails for unsupported combinations.
- Out of scope: enabling multi-statement by default.

### Dependencies

- `SNOW-B01`

### Files

- `server/internal/snowflake/handler.go`
- `server/internal/sqlutil/readonly.go`
- `server/internal/snowflake/handler_test.go`
- `docs/Snowflake.md`

### Implementation Notes

1. Multi-statement support must remain opt-in and read-only scoped.
2. Enforce `MULTI_STATEMENT_COUNT` contract on requests and responses.
3. Preserve current single-statement path behavior for default clients.

### Acceptance Criteria

- [ ] Multi-statement requires explicit opt-in.
- [ ] `MULTI_STATEMENT_COUNT` consistency checks enforced.
- [ ] Unsupported variable-binding combinations remain blocked.
- [ ] Read-only safety boundaries preserved.

### Test Plan

1. Unit tests for opt-in and validation failures.
2. Integration tests for accepted and rejected multi-statement requests.
3. Manual verification of rollback to single-statement defaults.

### Risks

- Incorrect statement-count enforcement can cause partial execution ambiguity.
- Expanded SQL surface can weaken read-only protections if not guarded.

### Rollout Notes

- Default remains single-statement; feature requires explicit enablement.

---

## SNOW-B04

### Title

Deep Observability + SLO Instrumentation

### Objective

Extend Release-A baseline telemetry into production-grade observability and formal SLO instrumentation.

### Scope

- In scope: structured logging, metrics emission, SLO definitions, alert thresholds.
- In scope: Snowflake query lifecycle coverage from submit through cancel/timeout.
- Out of scope: unrelated platform-wide observability refactors.

### Dependencies

- `SNOW-017`

### Files

- `server/internal/snowflake/handler.go`
- `server/internal/chat/jobs/registry.go`
- `server/internal/server/handler.go`
- `docs/Snowflake.md`

### Implementation Notes

1. Ensure logs carry correlation keys for query handle and async job ID.
2. Keep metric cardinality bounded; avoid per-query label explosions.
3. SLOs should cover latency and reliability of submit/poll/fetch/cancel flows.

### Acceptance Criteria

- [ ] Structured logs cover submit, poll, partition fetch, and cancel.
- [ ] Metrics include latency, retries, cancel success, and auth refresh outcomes.
- [ ] SLOs and alert thresholds are documented.

### Test Plan

1. Unit tests for instrumentation hooks where practical.
2. Integration verification of emitted metrics/log fields in a staging run.
3. Manual validation that alerts trigger at configured thresholds.

### Risks

- Missing correlation fields reduce debuggability for production incidents.
- High-cardinality metrics can increase cost and reduce observability performance.

### Rollout Notes

- Ship dashboards and alert rules in staging before production enablement.

---

## SNOW-B05

### Title

Durable Async Recovery + Orphan Reconciliation

### Objective

Eliminate v1 restart/orphan limitations by adding durable async job persistence and restart-safe recovery/reconciliation behavior.

### Scope

- In scope: durable job state persistence, restart recovery, orphan reconciliation workflow.
- In scope: explicit ownership and cancellation behavior for recovered jobs.
- Out of scope: changes to Snowflake SQL semantics.

### Dependencies

- `SNOW-017`

### Files

- `server/internal/chat/jobs/registry.go`
- `server/internal/chat/store/store.go`
- `server/internal/chat/store/mysql.go`
- `server/internal/chat/store/json.go`
- `server/internal/chat/jobs/registry_test.go`
- `docs/Snowflake.md`

### Implementation Notes

1. Persist Snowflake async jobs with enough state to resume polling after process restart.
2. Recovered jobs must preserve owner-token authorization semantics.
3. Define orphan reconciliation behavior for jobs that cannot be resumed cleanly.

### Acceptance Criteria

- [ ] Async job state for Snowflake persists across process restarts.
- [ ] On restart, recoverable jobs resume poll lifecycle safely.
- [ ] Unrecoverable jobs transition via explicit reconciliation contract with actionable error details.
- [ ] Owner-token authorization remains enforced for recovered jobs.

### Test Plan

1. Unit tests for persisted job serialization/deserialization and recovery behavior.
2. Integration tests simulating process restart mid-query.
3. Manual validation of reconciliation and cancel behavior for orphaned jobs.

### Risks

- Recovery bugs can duplicate polling or lose terminal state transitions.
- Persistence schema changes can affect existing async providers.

### Rollout Notes

- Roll out behind a recovery feature flag and enable after staging burn-in.
