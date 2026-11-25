# Snowflake Ticket Verification and Validity Matrix

This matrix defines validity checks, verification checks, and required evidence per Snowflake ticket.

## Evidence Conventions

- `Unit`: targeted unit tests pass.
- `Integration`: endpoint/tool behavior tests pass.
- `Manual`: reproducible manual flow notes.
- `Trace`: logs, request/response captures, screenshots.

---

## Release A

## SNOW-001 Contract Freeze

Validity checks:
- Route contract includes `preflight` and `query` as separate endpoints.
- SQL API lifecycle covers submit, poll, partition fetch, and cancel.
- Read-only policy explicitly references `sqlutil.IsReadOnlySQL()` for user SQL.
- Async status/state contract matches existing runtime (`running`, `completed`, `failed`, `cancelled`).
- Session-context precedence is deterministic across overrides/settings/state.
- Restart/orphan behavior is explicit (recovery or explicit non-goal).

Verification checks:
- Contract doc lists all route names and payload shapes.
- Contract doc path is canonical (`docs/Snowflake.md`).
- Contract explicitly rejects `PUT` and `GET` commands.
- Contract includes fixed preflight response schema for HTTP `200` warning/pass and HTTP `400` blocking failures.
- Async payload contract includes stable running and terminal fields used by query + jobs + prompts.
- Submit retry rules define required request-id/idempotency key behavior.
- Non-goals are explicit and enforceable.

Exit criteria:
- `Manual`: architecture review sign-off.
- `Trace`: approved contract section in `docs/Snowflake.md`.

---

## SNOW-002 Registry Dependency Gate

Validity checks:
- Exactly one path is chosen for Release A (registry or fallback).

Verification checks:
- Plan declares whether `REG-003` is present.
- If absent, `SNOW-FB-001` is mandatory.

Exit criteria:
- `Manual`: dependency decision accepted by maintainers.
- `Trace`: dependency chain update in release plan.

---

## SNOW-003 Settings, Defaults, Feature Flags, Datasource State Mapping

Validity checks:
- Field names match backend/frontend.
- `cloudCredentialsSnowflake` naming is consistent.
- Snowflake datasource state uses existing generic `DatasourceConnectionState` fields for v1.

Verification checks:
- Settings serialize/deserialize all Snowflake fields.
- `constants.ts` includes Snowflake defaults.
- `settingsMigration.ts` includes `snowflake` in `ensureDataSources()`.
- Run payload parsing includes Snowflake datasource override (`server/internal/chat/httpapi/handler.go`) and does not drop it.
- Build defaults:
- Tributary has AWS/Trino/Snowflake off.
- All-providers has AWS/Trino/Snowflake on.
- Build-policy enforcement:
- release artifacts do not allow runtime env-based override for `cloudCredentialsAws`, `cloudCredentialsTrino`, `cloudCredentialsSnowflake`.
- any local-dev override path is explicitly scoped/documented as non-release behavior.

Exit criteria:
- `Unit`: config/settings tests pass.
- `Unit`: frontend type/build checks pass.
- `Trace`: sample settings payload from both build profiles.

---

## SNOW-004 Secrets Store Schema

Validity checks:
- Secrets model is mode-scoped and minimal.
- No secret value leaks to plain settings.

Verification checks:
- `SnowflakeSecrets` includes mode, OAuth token, private key PEM, passphrase.
- Set/get/clear methods enforce mode validity.
- Configured status booleans include `snowflake`, `snowflakeOAuthToken`, `snowflakeKeyPair`.

Exit criteria:
- `Unit`: `secrets/store_snowflake_test.go` covers set/get/clear transitions and mode changes.
- `Trace`: encrypted round-trip with representative PEM payload.

---

## SNOW-005 Secrets API Wiring

Validity checks:
- API validation is strict and explicit.

Verification checks:
- `/api/secrets/set` supports Snowflake with `oauth_token` and `keypair_jwt`.
- `/api/secrets/clear` clears Snowflake state.
- Invalid payloads return HTTP 400 with actionable errors.

Exit criteria:
- `Integration`: endpoint tests for success/failure matrix.
- `Trace`: request/response examples.

---

## SNOW-006 Datasource Filter Migration (Map-Based) + Snowflake Gate

Validity checks:
- Variadic helper path is removed from active call flow.
- Snowflake feature flag gate is server-enforced.
- Snowflake server-side include rules require validated connection state.

Verification checks:
- Manager callsites use map-based helpers.
- Run-level datasource override path maps Snowflake through enablement/filtering checks.
- `datasourceEnabled()` has explicit Snowflake gate with flag-off disable behavior.
- Snowflake datasource inclusion requires `enabled=true` and `lastCheckOk=true` (matching Databricks/Trino behavior).
- Existing provider behavior is unchanged.

Exit criteria:
- `Unit`: datasource-filter tests include Snowflake gate.
- `Unit`: manager tool-filter regression tests pass.

---

## SNOW-007 Snowflake Handler Skeleton (`status`, `test`)

Validity checks:
- Account identifier normalization is deterministic.
- Status/test contracts are stable.

Verification checks:
- `GET /api/snowflake/status` returns expected fields.
- `POST /api/snowflake/test` executes `SELECT 1` path.
- Datasource state persistence uses mapped generic fields.
- Account identifier normalization matrix verified:
- org/account form
- account locator + region/cloud suffix
- full host input
- privatelink-style host input
- malformed host/scheme/path rejection

Exit criteria:
- `Unit`: handler tests for status/test paths.
- `Integration`: local mock/stub smoke for connection test.

---

## SNOW-008 Auth Engine (OAuth Token + Key-Pair JWT)

Validity checks:
- JWT signing matches Snowflake claim/fingerprint rules.
- Cache/refresh behavior prevents expiry race.

Verification checks:
- OAuth header generation tested.
- Keypair path covers:
- PKCS#1 and PKCS#8 parsing.
- encrypted PEM passphrase handling.
- fingerprint computation.
- claim correctness (`iss`, `sub`, `iat`, `exp`).
- RS256 signature validity.
- refresh-before-expiry behavior.

Exit criteria:
- `Unit`: auth test matrix passes.
- `Trace`: decoded, redacted JWT sample with expected claims.

---

## SNOW-009 Query + Preflight Endpoints (`/query`, `/preflight`)

Validity checks:
- Preflight and query exist as distinct endpoints.
- User SQL always passes through shared `sqlutil.IsReadOnlySQL()`.
- SQL API partition model is handled without data loss.
- Submit retry behavior is idempotent and safe against duplicate statement creation.
- Preflight behavior avoids false negatives for read-only queries with restricted `EXPLAIN` permissions.

Verification checks:
- Preflight:
- `POST /api/snowflake/preflight` validates read-only SQL and runs `EXPLAIN`.
- `EXPLAIN` permission/dialect failures return non-blocking warning-grade preflight result when read-only validation passes.
- Preflight response schema is fixed and tested:
- HTTP `200` payload includes `ok`, `severity`, `readOnly`, `explainAttempted`, `explainSucceeded`, `warnings`.
- HTTP `400` payload includes `ok=false`, `severity=error`, `errorCode`, `message`.
- Query:
- `POST /api/snowflake/query` submit/poll/fetch flow succeeds.
- Query endpoint response contract is stable for async bridge:
- running payload carries stable async fields (`status`, `job_id`, `owner_token`).
- terminal payloads are contract-consistent with runtime states (`completed`, `failed`, `cancelled`).
- Partition handling:
- inline partition handled.
- additional partitions fetched in order.
- global row limit enforced across partitions.
- Edge behavior:
- empty result set.
- partial-result then failure.
- partition expiry (`404/410`).
- bounded transient retries only.
- timeout and response size cap enforced.
- submit retries require stable request-id/idempotency key reuse and do not submit without request-id.

Exit criteria:
- `Unit`: handler tests for all edge cases.
- `Integration`: mocked SQL API lifecycle suite.

---

## SNOW-010 Metadata Endpoints

Validity checks:
- Endpoint naming is Snowflake-native (`databases`).
- Metadata SQL is handler-owned and safe.

Verification checks:
- `databases`, `schemas`, `tables`, `table-schema` endpoints work.
- Parameter validation errors are explicit.
- Payloads satisfy frontend metadata renderer contracts.

Exit criteria:
- `Unit`: per-endpoint metadata tests.
- `Integration`: metadata render contract tests.

---

## SNOW-011 Server Route Wiring + Activation Gate

Validity checks:
- Snowflake routing follows existing activation and settings-gate patterns.

Verification checks:
- Handler opts include Snowflake handler.
- `/api/snowflake/*` is mounted behind activation gate.
- Feature flag off route behavior is enforced at request-time server gating.
- Feature flag off returns HTTP `404` for all `/api/snowflake/*` endpoints.

Exit criteria:
- `Unit`: server routing tests with feature flag matrix.
- `Trace`: route availability traces for flag on/off.

---

## SNOW-012 Tools + `DatasourceCapability` + Query Provider Registry

Validity checks:
- Snowflake tool names are canonical.
- Backend capability registration is explicit in `tools/types.go`.
- Frontend provider label mapping reflects Snowflake terminology (`databases`).

Verification checks:
- Snowflake tools are registered and discoverable.
- `DatasourceCapability` maps Snowflake `RunQueryTool`, `MetadataTools`, `AsyncQuery`.
- Query provider registry maps Snowflake tool names and display labels correctly.
- Tool outputs emit `query_metadata.provider="snowflake"`.

Exit criteria:
- `Unit`: tool/capability registry tests pass.
- `Integration`: Snowflake tool events render with correct provider labels.

---

## SNOW-013 Async Jobs Integration + `qj-sf-...` Prefix

Validity checks:
- Job identity and lifecycle are handle-safe and cancel-safe.
- Runtime status names match existing async contract (`running`, `completed`, `failed`, `cancelled`).
- Restart behavior is explicit for v1 in-memory registry.

Verification checks:
- `newJobID()` returns `qj-sf-...` for Snowflake.
- Async submit/poll/get/cancel flows work.
- Owner-token authorization remains enforced.
- Restart/orphan behavior is validated:
- unknown `job_id` after restart returns explicit not-found semantics.
- operator guidance for orphan handling is documented and linked.

Exit criteria:
- `Unit`: jobs registry prefix/lifecycle tests pass.
- `Integration`: async Snowflake query lifecycle smoke.

---

## SNOW-014 Manager + Prompts + Runtime Wiring

Validity checks:
- Manager includes Snowflake deterministically when enabled.
- Prompt families and async guidance include Snowflake.

Verification checks:
- `datasourcePromptFamilies()` has explicit Snowflake case.
- Prompt text includes `run_snowflake_query` async handling.
- Prompt text/status handling aligns with runtime async states (`running`, `completed`, `failed`, `cancelled`).
- System prompt has generic datasource guidance covering Snowflake.

Exit criteria:
- `Unit`: manager/prompt tests pass.
- `Trace`: prompt snapshot with Snowflake enabled.

---

## SNOW-015 Frontend Types, Migration, Service, Secrets Parsing, Chat/Query UI

Validity checks:
- Provider unions include Snowflake everywhere required.
- Frontend secrets status parsing includes Snowflake booleans.
- Chat/query UI provider displays are complete for Snowflake.

Verification checks:
- `types.ts` unions updated.
- `settingsMigration.ts` preserves Snowflake datasource state.
- `services/secretsApi.ts` parses `snowflake`, `snowflakeOAuthToken`, `snowflakeKeyPair`.
- UI files updated:
- `components/chat/RunningQueryCard.tsx`
- `components/chat/ToolCallBlock.tsx`
- `components/chat/ToolResultBlock.tsx`
- `components/layout/QueryTrayItem.tsx`
- `components/renderers/tool/CatalogBrowserRenderer.tsx`
- `lib/queryProviderRegistry.ts`

Exit criteria:
- `Unit`: frontend parsing/mapping tests pass.
- `Integration`: provider-aware UI rendering tests pass.
- `Manual`: Snowflake chat/query rendering smoke verified.

---

## SNOW-016 Snowflake Cloud Credentials Card + UI Feature Flag Gate

Validity checks:
- Card behavior follows Cloud Credentials conventions.
- Feature flag gate prevents Tributary exposure.

Verification checks:
- Card renders only when `cloudCredentialsSnowflake` is true.
- Auth mode switching works for OAuth token and keypair JWT.
- Test connection uses Snowflake test endpoint.
- Enable toggle behavior matches validation gate.

Exit criteria:
- `Integration`: settings card tests pass.
- `Manual`: build-profile visibility check completed.

---

## SNOW-017 Tests, Verification, and Release Readiness

Validity checks:
- Test targets reference real files.
- Verification evidence exists for every critical path.

Verification checks:
- Backend tests updated:
- `optionc/datasource_filter_test.go`
- `optionc/manager_test.go`
- `optionc/manager_query_test.go`
- `chat/httpapi/handler_e2e_test.go` (Snowflake run override end-to-end behavior)
- `jobs/registry_test.go`
- `internal/server/handler_test.go` (route mount and flag-off unavailability checks)
- `config/settings_test.go`
- `secrets/store_snowflake_test.go` (encryption/mode transitions/clear behavior)
- `snowflake/auth_test.go`
- `snowflake/handler_test.go`
- `tools/snowflake_tools_test.go`
- `services/secretsApi.test.ts` (Snowflake secrets status parsing contract)
- Frontend tests/build checks pass.
- Manual smoke flow covers status/test/preflight/query/metadata/async.
- Both build profiles validated for feature-flag behavior.
- Backend/frontend secrets status contract verified end-to-end for Snowflake booleans.
- Release-A observability gate verified:
- structured logs for submit/poll/partition/cancel include required keys: `provider`, `snowflake_query_handle`, `job_id`, `request_id`, `event`, `status`.
- metrics with fixed names exist: `snowflake_submit_latency_ms`, `snowflake_poll_attempt_total`, `snowflake_terminal_total`, `snowflake_cancel_attempt_total`.
- runbook exists at `docs/runbooks/snowflake-v1-ops.md` and documents v1 restart/orphan handling.

Exit criteria:
- `Unit`: targeted backend/frontend suites pass.
- `Integration`: Snowflake end-to-end flow passes.
- `Manual`: signed release checklist with evidence links.

---

## Release B Checks

## SNOW-B01 OAuth Refresh Flow

Validity checks:
- Refresh flow uses Snowflake-specific token exchange assumptions, not generic defaults.

Verification checks:
- Refresh endpoint/params documented and tested.
- Refresh token storage and rotation logic validated.

Exit criteria:
- `Unit`: refresh path tests pass.
- `Trace`: redacted refresh exchange logs.

---

## SNOW-B02 Workload Identity Federation Path

Validity checks:
- Chosen WIF flow is explicitly compatible with Snowflake SQL API path.
- WIF remains opt-in and does not alter v1 default auth flows.

Verification checks:
- Provider-specific setup and trust assumptions are documented.
- Feature flag gating for WIF is implemented.
- End-to-end test for one WIF path passes.

Exit criteria:
- `Unit`: auth selection and failure-path tests pass.
- `Integration`: one WIF path validated in staging.
- `Trace`: setup and runbook documentation approved.

---

## SNOW-B03 Guarded Multi-Statement Support

Validity checks:
- Multi-statement execution is opt-in and read-only constrained.
- `MULTI_STATEMENT_COUNT` contract enforcement is deterministic.

Verification checks:
- Request validation rejects missing/invalid multi-statement count.
- Unsupported variable-binding combinations are rejected.
- Default single-statement behavior remains unchanged.

Exit criteria:
- `Unit`: validation tests for allowed/blocked combinations pass.
- `Integration`: multi-statement opt-in path and failure paths pass.
- `Trace`: rollback path to single-statement behavior documented.

---

## SNOW-B04 Deep Observability + SLO Instrumentation

Validity checks:
- Telemetry design extends (not replaces) Release-A baseline telemetry.
- Telemetry design covers submit/poll/partition/cancel lifecycle with stable identifiers.
- Metric design avoids unbounded label cardinality.

Verification checks:
- Structured logs include query handle and async job correlation fields.
- Metrics for latency/retry/cancel/auth outcomes are emitted and queryable.
- SLOs and alerts are documented and validated in staging.

Exit criteria:
- `Integration`: staging telemetry checks pass.
- `Trace`: dashboard and alert definitions linked.
- `Manual`: on-call runbook updated with Snowflake failure triage.

---

## SNOW-B05 Durable Async Recovery + Orphan Reconciliation

Validity checks:
- Recovery design preserves job ownership and avoids duplicate lifecycle execution.
- Persistence schema supports Snowflake async resumption with backward compatibility.

Verification checks:
- Persisted Snowflake jobs survive process restart.
- Recovered jobs resume polling or transition through explicit reconciliation contract.
- Unrecoverable jobs expose actionable terminal errors.
- Owner-token authorization works for recovered jobs.

Exit criteria:
- `Unit`: persistence/recovery test matrix passes.
- `Integration`: restart simulation with mid-flight Snowflake jobs passes.
- `Trace`: orphan reconciliation runbook and operator flow approved.

---

## Release A Evidence Snapshot (2026-02-16)

| Ticket | Status | Evidence |
|---|---|---|
| SNOW-001 | Complete | Contract frozen in `docs/Snowflake.md` with route list, preflight schema, async states, idempotency, precedence, non-goals, restart/orphan semantics. |
| SNOW-002 | Complete | Registry path selected (`REG-003`) and documented in `docs/Snowflake.md`. |
| SNOW-003 | Complete | Settings + feature flags + datasource state wiring in `server/internal/config/settings.go`, `types.ts`, `constants.ts`, `services/settingsMigration.ts`; tests in `server/internal/config/settings_test.go`. |
| SNOW-004 | Complete | Snowflake secrets schema and methods in `server/internal/secrets/store.go`; coverage in `server/internal/secrets/store_snowflake_test.go`. |
| SNOW-005 | Complete | Secrets set/clear wiring for `providerId=snowflake` in `server/internal/server/handler.go`; coverage in `server/internal/server/handler_test.go`. |
| SNOW-006 | Complete | Map-based datasource filtering and Snowflake gate in `server/internal/chat/optionc/datasource_filter.go`; coverage in `server/internal/chat/optionc/datasource_filter_test.go`. |
| SNOW-007 | Complete | Status/test handler endpoints + account normalization in `server/internal/snowflake/handler.go`; coverage in `server/internal/snowflake/handler_test.go`. |
| SNOW-008 | Complete | OAuth + keypair JWT auth engine in `server/internal/snowflake/auth.go`; coverage in `server/internal/snowflake/auth_test.go`. |
| SNOW-009 | Complete | Query + preflight implementation in `server/internal/snowflake/handler.go`; coverage in `server/internal/snowflake/handler_test.go`. |
| SNOW-010 | Complete | Metadata endpoints in `server/internal/snowflake/handler.go`; coverage in `server/internal/snowflake/handler_test.go`. |
| SNOW-011 | Complete | Route wiring and request-time feature gate in `server/internal/server/handler.go` + `server/cmd/ai-analyst/main.go`; coverage in `server/internal/server/handler_test.go`. |
| SNOW-012 | Complete | Snowflake tools/capability/registry mapping in `server/internal/chat/tools/snowflake_tools.go`, `server/internal/chat/tools/types.go`, `lib/queryProviderRegistry.ts`; coverage in `server/internal/chat/tools/snowflake_tools_test.go` and `tests/e2e/snowflake-frontend.spec.ts`. |
| SNOW-013 | Complete | Async jobs integration and `qj-sf-` prefix in `server/internal/chat/jobs/registry.go`; coverage in `server/internal/chat/jobs/registry_test.go` and `server/internal/chat/tools/snowflake_tools_test.go`. |
| SNOW-014 | Complete | Manager and prompt wiring in `server/internal/chat/optionc/manager.go` and `server/internal/chat/optionc/prompts.go`; coverage in `server/internal/chat/optionc/manager_test.go`. |
| SNOW-015 | Complete | Frontend provider wiring + migration + services + UI in `types.ts`, `services/settingsMigration.ts`, `services/secretsApi.ts`, `services/snowflakeApi.ts`, `hooks/useChat.ts`, `components/chat/*`, `components/layout/QueryTrayItem.tsx`, `components/renderers/tool/CatalogBrowserRenderer.tsx`. |
| SNOW-016 | Complete | Snowflake Cloud Credentials card + flag gate in `components/settings/SnowflakeSection.tsx` and `components/settings/CloudCredentialsTab.tsx`; payload contract coverage in `tests/e2e/snowflake-frontend.spec.ts`. |
| SNOW-017 | Complete | Runbook in `docs/runbooks/snowflake-v1-ops.md`; verification evidence recorded here; full test/build verification commands listed below. |

### Verification commands executed

- Backend full suite:
  - `cd server && go test ./...`
- Backend build-profile checks:
  - `cd server && go test -tags allproviders ./internal/config`
  - `cd server && go test -tags allproviders ./internal/server`
- Frontend build:
  - `npm run build`
- Frontend Snowflake e2e:
  - `npm run test:e2e -- tests/e2e/snowflake-frontend.spec.ts`
