# Release A: Snowflake V1 Delivery Tickets

## Goal

Ship Snowflake as a new datasource with a Cloud Credentials card, read-only SQL API query execution, metadata tools, async jobs, baseline launch observability, and full backend/frontend provider wiring, while keeping feature-flag behavior consistent across build profiles.

## Preconditions

1. `REG-003` is merged (query-provider registry available), OR fallback ticket `SNOW-FB-001` is completed first.
2. Build-profile feature-flag defaults are defined for Tributary and all-providers builds.
3. Snowflake v1 scope is fixed to SQL API read-only workloads (no staged file transfer, no Snowpipe).

## Ticket Order

| Ticket | Title | Depends On |
|---|---|---|
| SNOW-001 | Contract Freeze | None |
| SNOW-002 | Registry Dependency Gate | SNOW-001 |
| SNOW-FB-001 (Conditional) | Fallback Path if Registry Work Is Not Merged | SNOW-002 |
| SNOW-003 | Settings, Defaults, Feature Flags, Datasource State Mapping | SNOW-001 |
| SNOW-004 | Secrets Store Schema | SNOW-001 |
| SNOW-005 | Secrets API Wiring | SNOW-004 |
| SNOW-006 | Datasource Filter Migration (Map-Based) + Snowflake Gate | SNOW-003 |
| SNOW-007 | Snowflake Handler Skeleton (`status`, `test`) | SNOW-003, SNOW-005 |
| SNOW-008 | Auth Engine (OAuth Token + Key-Pair JWT) | SNOW-007 |
| SNOW-009 | Query + Preflight Endpoints (`/query`, `/preflight`) | SNOW-008 |
| SNOW-010 | Metadata Endpoints (`databases`, `schemas`, `tables`, `table-schema`) | SNOW-009 |
| SNOW-011 | Server Route Wiring + Activation Gate | SNOW-007, SNOW-010 |
| SNOW-012 | Tools + `DatasourceCapability` + Query Provider Registry Wiring | SNOW-002, SNOW-009, SNOW-010 |
| SNOW-013 | Async Jobs Integration + `qj-sf-...` Prefix | SNOW-012 |
| SNOW-014 | Manager + Prompts + Runtime Wiring | SNOW-011, SNOW-013 |
| SNOW-015 | Frontend Types, Migration, Service, Secrets Parsing, Chat/Query UI | SNOW-003, SNOW-005, SNOW-012, SNOW-014 |
| SNOW-016 | Snowflake Cloud Credentials Card + UI Feature Flag Gate | SNOW-015 |
| SNOW-017 | Tests, Verification, and Release Readiness | SNOW-016 |

---

## SNOW-001

### Title

Contract Freeze

### Objective

Finalize Snowflake v1 API and behavior contracts before implementation starts.

### Scope

- In scope: Snowflake SQL API route contract, auth modes, async lifecycle, read-only boundaries.
- In scope: explicit non-goals for v1.
- Out of scope: code changes outside planning docs.

### Dependencies

- None.

### Files

- `docs/Snowflake.md` (canonical contract/spec location)

### Implementation Notes

1. Route parity must match existing datasource handlers.
2. Snowflake top-level metadata object is `database`, not `catalog`.
3. Contract must include both direct HTTP query endpoint and tool-driven query path.

### Acceptance Criteria

- [ ] Routes are frozen and documented:
- [ ] `GET /api/snowflake/status`
- [ ] `POST /api/snowflake/test`
- [ ] `POST /api/snowflake/preflight`
- [ ] `POST /api/snowflake/query`
- [ ] `GET /api/snowflake/databases`
- [ ] `GET /api/snowflake/schemas`
- [ ] `GET /api/snowflake/tables`
- [ ] `GET /api/snowflake/table-schema`
- [ ] SQL API lifecycle is explicit: submit statement, poll handle, fetch partitions, cancel handle.
- [ ] Preflight response schema is frozen and testable:
- [ ] HTTP `200` for pass/warning outcomes with payload fields: `ok`, `severity`, `readOnly`, `explainAttempted`, `explainSucceeded`, `warnings`.
- [ ] HTTP `400` for blocking failures with payload fields: `ok=false`, `severity=error`, `errorCode`, `message`.
- [ ] Async query contract is frozen across query endpoint and jobs lifecycle:
- [ ] running response includes stable job fields (`status`, `job_id`, `owner_token`).
- [ ] poll responses use stable states (`running`, `completed`, `failed`, `cancelled`) and stable payload shape.
- [ ] cancel semantics and idempotency are specified against Snowflake statement handles.
- [ ] Submit retry policy is frozen with idempotency requirements:
- [ ] SQL submit retries require a stable request-id/idempotency key reused across attempts.
- [ ] submit retries without idempotency key are disallowed.
- [ ] Session-context precedence is frozen for all routes/tools:
- [ ] per-request/per-run override > persisted provider settings > datasource connection state.
- [ ] datasource connection state is observational and must not override explicit settings/overrides at execution time.
- [ ] Auth modes are exactly `oauth_token` and `keypair_jwt`.
- [ ] Read-only policy explicitly states `sqlutil.IsReadOnlySQL()` is required for user SQL.
- [ ] `PUT` and `GET` SQL commands are explicitly rejected in v1.
- [ ] Async restart/orphan behavior is explicit for v1:
- [ ] durable async recovery across process restarts is out of scope.
- [ ] polling unknown `job_id` after restart returns explicit not-found semantics with operator guidance.
- [ ] v1 non-goals include staged file commands, multi-statement by default, and Snowpipe.

### Test Plan

1. Design review with backend + frontend owners.
2. Validate route list against existing provider conventions.

### Risks

- Contract drift during implementation.
- Ambiguous read-only policy if utility usage is not explicit.

### Rollout Notes

- Contract changes after ticket closure require explicit amendment in this file.

---

## SNOW-002

### Title

Registry Dependency Gate

### Objective

Resolve whether Snowflake ships on the query-provider registry path or fallback mechanical path.

### Scope

- In scope: explicit dependency decision and fallback ticket definition.
- Out of scope: implementation of registry itself.

### Dependencies

- `SNOW-001`

### Files

- `docs/Snowflake.md`
- `plans/snowflake-integration-tickets/release-a-snowflake-v1.md`

### Implementation Notes

1. Pick one execution path for Release A.
2. Avoid dual-path ambiguity inside any single ticket.

### Acceptance Criteria

- [ ] Plan states whether `REG-003` is available before implementation starts.
- [ ] If `REG-003` is unavailable, `SNOW-FB-001` is required and linked.
- [ ] If `REG-003` is available, no fallback-only tasks remain in critical path.

### Test Plan

1. Architecture sign-off on chosen dependency path.

### Risks

- Hidden dependency on registry work causing schedule slips.

### Rollout Notes

- Dependency decision is frozen for Release A once coding begins.

---

## SNOW-003

### Title

Settings, Defaults, Feature Flags, Datasource State Mapping

### Objective

Add Snowflake configuration fields and feature flags while preserving existing datasource-state schema patterns.

### Scope

- In scope: backend and frontend settings types, defaults, migration, build-profile feature flags.
- In scope: datasource-state mapping using existing generic fields.
- Out of scope: Snowflake secrets.

### Dependencies

- `SNOW-001`

### Files

- `server/internal/config/settings.go`
- `server/internal/config/settings_test.go`
- `server/internal/chat/httpapi/handler.go`
- `types.ts`
- `constants.ts`
- `services/settingsMigration.ts`

### Implementation Notes

1. Keep `DatasourceConnectionState` pattern stable for v1.
2. Reuse generic fields:
- `LastHost` stores Snowflake account identifier.
- `LastWarehouseID` stores Snowflake warehouse.
- `LastProjectID` stores Snowflake database.
3. Keep schema/role in provider settings (`snowflakeSchema`, `snowflakeRole`) rather than adding provider-specific state fields.

### Acceptance Criteria

- [ ] Backend and frontend settings include:
- [ ] `snowflakeAccountIdentifier`
- [ ] `snowflakeWarehouse`
- [ ] `snowflakeDatabase`
- [ ] `snowflakeSchema`
- [ ] `snowflakeRole`
- [ ] `snowflakeUser`
- [ ] Backend and frontend feature flags include `cloudCredentialsSnowflake`.
- [ ] `constants.ts` includes Snowflake default values for all new settings.
- [ ] `dataSources.snowflake` exists with default `enabled=false`.
- [ ] `services/settingsMigration.ts` keeps Snowflake datasource state in `ensureDataSources()`.
- [ ] Run-level datasource overrides include `snowflake` in run payload parsing and do not drop the provider.
- [ ] Build defaults enforce policy:
- [ ] Tributary: AWS false, Trino false, Snowflake false.
- [ ] All-providers: AWS true, Trino true, Snowflake true.
- [ ] Build-enforced flag policy is explicit:
- [ ] release artifacts must not allow runtime env-based override of `cloudCredentialsAws`, `cloudCredentialsTrino`, `cloudCredentialsSnowflake`.
- [ ] local development override support, if any, must be explicitly non-release scoped and documented.
- [ ] `DatasourceConnectionState` is not extended with Snowflake-only fields for v1.

### Test Plan

1. Backend settings serialization unit tests.
2. Frontend build/type checks for settings and defaults.
3. Migration tests for existing stored settings payloads.

### Risks

- Breaking shared datasource-state compatibility if new provider-specific fields are added.
- Feature flag mismatch across build profiles.

### Rollout Notes

- If state schema extension becomes necessary later, ship as a separate migration ticket.

---

## SNOW-004

### Title

Secrets Store Schema

### Objective

Add encrypted storage model for Snowflake OAuth tokens and key-pair JWT materials.

### Scope

- In scope: secret struct, set/get/clear methods, configured status booleans.
- Out of scope: HTTP API wiring.

### Dependencies

- `SNOW-001`

### Files

- `server/internal/secrets/store.go`

### Implementation Notes

1. Keep sensitive data only in encrypted secrets store.
2. Mode-specific validation must prevent mixed credential shapes.
3. Preserve status shape so frontend parsing can map provider-specific booleans.

### Acceptance Criteria

- [ ] `SnowflakeSecrets` is added with:
- [ ] `Mode` (`oauth_token` | `keypair_jwt`)
- [ ] `OAuthToken`
- [ ] `PrivateKeyPEM`
- [ ] `PrivateKeyPassphrase`
- [ ] Methods exist:
- [ ] `GetSnowflake()`
- [ ] `SetSnowflakeOAuthToken(token string)`
- [ ] `SetSnowflakeKeyPair(privateKeyPEM, passphrase string)`
- [ ] `ClearSnowflake()`
- [ ] Configured status contains:
- [ ] `snowflake`
- [ ] `snowflakeOAuthToken`
- [ ] `snowflakeKeyPair`
- [ ] PEM-sized payloads round-trip through encrypted storage.

### Test Plan

1. Store round-trip tests for each auth mode.
2. Negative tests for invalid mode transitions.

### Risks

- Invalid status flags can break frontend auth-state rendering.

### Rollout Notes

- No migration needed for empty secrets state; existing providers remain unchanged.

---

## SNOW-005

### Title

Secrets API Wiring

### Objective

Expose Snowflake secrets set/clear through existing secrets endpoints.

### Scope

- In scope: `/api/secrets/set`, `/api/secrets/clear`, payload validation.
- Out of scope: frontend parsing and UI.

### Dependencies

- `SNOW-004`

### Files

- `server/internal/server/handler.go`

### Implementation Notes

1. Match Databricks/Trino API behavior and error patterns.
2. Keep auth-mode parsing strict to avoid ambiguous writes.

### Acceptance Criteria

- [ ] `/api/secrets/set` accepts `providerId="snowflake"`.
- [ ] `authType="oauth_token"` requires token and persists OAuth mode.
- [ ] `authType="keypair_jwt"` requires PEM (+ optional passphrase) and persists keypair mode.
- [ ] `/api/secrets/clear` accepts `providerId="snowflake"` and clears all Snowflake secret material.
- [ ] Invalid payload combinations return `400` with clear error text.

### Test Plan

1. Endpoint tests for success and failure payloads.

### Risks

- Invalid clear/set behavior can leave stale auth mode in status.

### Rollout Notes

- API remains backward compatible for all existing providers.

---

## SNOW-006

### Title

Datasource Filter Migration (Map-Based) + Snowflake Gate

### Objective

Remove fragile variadic datasource filtering and add Snowflake server-side feature-flag enforcement.

### Scope

- In scope: datasource filtering helper migration and manager callsites.
- In scope: Snowflake feature-flag gate in datasource enablement.
- Out of scope: tool implementation.

### Dependencies

- `SNOW-003`

### Files

- `server/internal/chat/optionc/datasource_filter.go`
- `server/internal/chat/optionc/manager.go`
- `server/internal/chat/httpapi/handler.go`
- `server/internal/chat/optionc/datasource_filter_test.go`

### Implementation Notes

1. Replace use of variadic signature:
- `isDatasourceToolAllowed(toolName, isBQEnabled, isDBEnabled, isTrinoEnabled ...bool)`
2. Switch manager callsites to map-based helpers:
- `isDatasourceToolAllowedByMap(...)`
- `filterToolsByDatasourceByMap(...)`
3. Add explicit Snowflake gate inside `datasourceEnabled()`.

### Acceptance Criteria

- [ ] Active manager/tool filtering callpaths use map-based datasource enablement.
- [ ] Variadic helper is no longer required for new providers.
- [ ] Run request datasource-override parsing path supports `snowflake` and maps through enablement filtering.
- [ ] `datasourceEnabled()` includes Snowflake guard:
- [ ] If `settings.FeatureFlags.cloudCredentialsSnowflake` is false, Snowflake is disabled.
- [ ] Snowflake server-side validation gating matches Databricks/Trino:
- [ ] datasource is included only when `enabled=true` and `lastCheckOk=true`.
- [ ] Existing BigQuery, Databricks, and Trino behavior is unchanged.

### Test Plan

1. Datasource-filter unit tests for map-based filtering.
2. Regression tests for existing provider gating.

### Risks

- Breaking tool filtering in manager hot path.

### Rollout Notes

- Keep change isolated and mechanical; avoid refactoring unrelated manager logic.

---

## SNOW-007

### Title

Snowflake Handler Skeleton (`status`, `test`)

### Objective

Create Snowflake handler foundation with status inspection and connectivity test endpoints.

### Scope

- In scope: handler package, route dispatch, status/test logic.
- Out of scope: query execution and metadata endpoints.

### Dependencies

- `SNOW-003`
- `SNOW-005`

### Files

- `server/internal/snowflake/handler.go` (new)

### Implementation Notes

1. Mirror existing handler structure from Trino/Databricks for maintainability.
2. Keep account-identifier normalization deterministic.
3. Normalize with a required case matrix covering common Snowflake account formats and host-style inputs.

### Acceptance Criteria

- [ ] `GET /api/snowflake/status` implemented with config + auth diagnostics.
- [ ] `POST /api/snowflake/test` implemented using `SELECT 1` through SQL API.
- [ ] Account identifier normalization to Snowflake SQL API host form is validated.
- [ ] Account identifier normalization matrix is implemented and tested:
- [ ] org/account format (for example `org-account`) is accepted and normalized.
- [ ] account locator with region/cloud suffix is accepted and normalized.
- [ ] full Snowflake host input is accepted/preserved.
- [ ] privatelink-style host input is accepted/preserved when valid.
- [ ] malformed hosts, unsupported schemes, and path-bearing URLs are rejected.
- [ ] Datasource connection status persists mapped generic fields from `SNOW-003`.

### Test Plan

1. Handler tests for status payload and validation errors.
2. Handler tests for connectivity test success/failure.
3. Account identifier normalization case-matrix tests.

### Risks

- Incorrect account normalization causes opaque connectivity failures.

### Rollout Notes

- Endpoint behavior should remain stable for UI card integration.

---

## SNOW-008

### Title

Auth Engine (OAuth Token + Key-Pair JWT)

### Objective

Implement Snowflake SQL API authentication for OAuth bearer tokens and server-signed key-pair JWTs.

### Scope

- In scope: auth header generation, JWT signing, cache/refresh behavior, auth validation.
- Out of scope: OAuth browser login/refresh flow.

### Dependencies

- `SNOW-007`

### Files

- `server/internal/snowflake/auth.go` (new)
- `server/internal/snowflake/auth_test.go` (new)

### Implementation Notes

1. Key-pair JWT implementation must include Snowflake-specific claim and fingerprint rules.
2. Normalize account/user casing as required by Snowflake JWT auth docs.
3. Cache signed JWTs with expiry margin to avoid edge-expiry race.

### Acceptance Criteria

- [ ] OAuth mode emits `Authorization: Bearer <token>`.
- [ ] Key-pair JWT mode:
- [ ] Supports PKCS#1 and PKCS#8 PEM parsing.
- [ ] Validates encrypted PEM with passphrase handling.
- [ ] Computes Snowflake-compatible public key fingerprint (`SHA256:...`).
- [ ] Builds claims `iss`, `sub`, `iat`, `exp` correctly.
- [ ] Enforces maximum token lifetime policy.
- [ ] Signs JWT using RS256.
- [ ] Caches JWT and refreshes before expiry.
- [ ] Explicit, actionable errors are returned for PEM/fingerprint/claim/signing failures.

### Test Plan

1. Unit tests for OAuth token flow.
2. Unit matrix for keypair success/failure paths.
3. Token cache timing tests.

### Risks

- Crypto implementation bugs can silently block all Snowflake traffic.

### Rollout Notes

- Keep key material redacted in logs and traces.

---

## SNOW-009

### Title

Query + Preflight Endpoints (`/query`, `/preflight`)

### Objective

Implement Snowflake read-only query execution and preflight validation endpoints, including partitioned SQL API result handling.

### Scope

- In scope: `POST /api/snowflake/preflight`, `POST /api/snowflake/query`, submit/poll/fetch/cancel logic.
- In scope: shared read-only guard and transport limits.
- Out of scope: metadata endpoint handlers.

### Dependencies

- `SNOW-008`

### Files

- `server/internal/snowflake/handler.go`
- `server/internal/sqlutil/readonly.go` (shared usage reference only; no duplication)

### Implementation Notes

1. Preflight validates user SQL with `sqlutil.IsReadOnlySQL()` and performs `EXPLAIN` as best-effort semantic validation.
2. Query endpoint validates SQL with `sqlutil.IsReadOnlySQL()` before submit.
3. Explicitly reject `PUT` and `GET` commands at validation boundary.
4. Handle partitioned results in order; enforce global row cap across partitions.
5. Apply request timeout, bounded transient retries, and response size cap (`io.LimitReader`).
6. Submit retries must be idempotent: generate and reuse a stable request-id across submit attempts, and persist it in async metadata for traceability.
7. Preflight warning/error schema must be stable and shared across backend/frontend tests.

### Acceptance Criteria

- [ ] `POST /api/snowflake/preflight` exists and returns read-only validation output.
- [ ] Preflight fallback behavior is deterministic:
- [ ] hard-fail on read-only violations.
- [ ] if `EXPLAIN` fails due to permission/dialect limitations but read-only validation passes, return a warning-grade preflight result (non-blocking for query endpoint).
- [ ] Preflight response schema is explicit:
- [ ] HTTP `200` + `{ ok: true, severity: \"ok\"|\"warning\", readOnly: true, explainAttempted: boolean, explainSucceeded: boolean, warnings: string[] }`.
- [ ] HTTP `400` + `{ ok: false, severity: \"error\", errorCode: string, message: string }` for blocking failures.
- [ ] `POST /api/snowflake/query` exists and executes read-only SQL.
- [ ] User-supplied SQL paths call shared `sqlutil.IsReadOnlySQL()`.
- [ ] Query lifecycle:
1. submit statement
2. poll handle until terminal state
3. collect inline result partition
4. fetch additional partitions in order
- [ ] Handles edge cases:
- [ ] empty results (no extra poll/fetch loop)
- [ ] partial results followed by upstream failure (clean failure, no hang)
- [ ] partition fetch expiry (`404`/`410`) with clear error
- [ ] Enforces `databaseQueryRowLimit` across all partitions.
- [ ] Uses bounded timeout/retry strategy (transient only).
- [ ] Submit retry logic is idempotent and safe:
- [ ] retries reuse the same request-id/idempotency key.
- [ ] no retry path submits without request-id.
- [ ] Uses response body size cap for upstream reads.

### Test Plan

1. Unit tests for preflight success/rejection cases.
2. Unit tests for query success, empty, partial-failure, and partition-expiry paths.
3. Integration tests with mocked SQL API for submit/poll/fetch/cancel.
4. Retry-idempotency tests proving no duplicate submit ownership under transient retry conditions.

### Risks

- Incorrect partition assembly causes row loss or ordering bugs.
- Missing read-only guard exposes mutating SQL.

### Rollout Notes

- Keep preflight and query response contracts aligned with existing datasource APIs.

---

## SNOW-010

### Title

Metadata Endpoints (`databases`, `schemas`, `tables`, `table-schema`)

### Objective

Provide Snowflake metadata browsing endpoints using safe handler-owned queries.

### Scope

- In scope: metadata endpoint handlers and query param validation.
- In scope: Snowflake identifier normalization.
- Out of scope: user-supplied ad-hoc metadata SQL.

### Dependencies

- `SNOW-009`

### Files

- `server/internal/snowflake/handler.go`

### Implementation Notes

1. Snowflake uses `databases` naming; do not reuse `catalog` labels.
2. Metadata SQL is handler-owned. Do not interpolate unchecked user SQL.
3. If metadata SQL uses `SHOW`/`DESCRIBE`, keep it outside user read-only guard path because queries are internal constants.

### Acceptance Criteria

- [ ] Endpoints implemented:
- [ ] `GET /api/snowflake/databases`
- [ ] `GET /api/snowflake/schemas`
- [ ] `GET /api/snowflake/tables`
- [ ] `GET /api/snowflake/table-schema`
- [ ] Required params validated with explicit errors.
- [ ] Identifier casing/quoting is deterministic.
- [ ] Response shape supports frontend catalog browser contracts.

### Test Plan

1. Unit tests per metadata endpoint.
2. Integration tests with representative metadata payloads.

### Risks

- Unquoted identifiers can fail on mixed-case object names.

### Rollout Notes

- Endpoint names remain Snowflake-native (`databases`) across backend and UI.

---

## SNOW-011

### Title

Server Route Wiring + Activation Gate

### Objective

Wire Snowflake handler into top-level server routing with activation and feature-flag gating.

### Scope

- In scope: handler options, route mount, activation middleware integration.
- Out of scope: tool manager integration.

### Dependencies

- `SNOW-007`
- `SNOW-010`

### Files

- `server/internal/server/handler.go`
- `server/cmd/ai-analyst/main.go`

### Implementation Notes

1. Follow Databricks/Trino mount pattern.
2. Gate route availability by activation + settings feature flag.
3. Route-gating is request-time in server handling (not handler-construction omission), because handler construction/mount remains part of normal bootstrap.

### Acceptance Criteria

- [ ] `HandlerOpts` includes `Snowflake *snowflake.Handler`.
- [ ] `main.go` constructs Snowflake handler and passes into server handler options.
- [ ] `/api/snowflake/*` is mounted behind existing activation/settings gate.
- [ ] Snowflake feature-flag route guard is explicit in request-time server path.
- [ ] If `cloudCredentialsSnowflake=false`, all `/api/snowflake/*` requests return HTTP `404` even when handler is mounted.

### Test Plan

1. Route-mount tests with feature flag on/off.
2. Activation gate regression tests.

### Risks

- Route exposed when flag is off.

### Rollout Notes

- Safe rollback: disable Snowflake via feature flag.

---

## SNOW-012

### Title

Tools + `DatasourceCapability` + Query Provider Registry Wiring

### Objective

Add Snowflake tool definitions and wire provider metadata in backend/frontend registries.

### Scope

- In scope: Snowflake tools, backend capability registration, frontend query provider mapping.
- Out of scope: manager async lifecycle state.

### Dependencies

- `SNOW-002`
- `SNOW-009`
- `SNOW-010`

### Files

- `server/internal/chat/tools/snowflake_tools.go` (new)
- `server/internal/chat/tools/registry.go`
- `server/internal/chat/tools/types.go`
- `lib/queryProviderRegistry.ts`

### Implementation Notes

1. Backend must register Snowflake in `DatasourceCapability` (`tools/types.go`) with:
- `RunQueryTool`
- `MetadataTools`
- `AsyncQuery`
2. Frontend registry labels must use Snowflake terms (`database`, not `catalog`).
3. If `REG-003` is unavailable, execute `SNOW-FB-001` first.

### Acceptance Criteria

- [ ] Tools added and registered:
- [ ] `run_snowflake_query`
- [ ] `list_snowflake_databases`
- [ ] `list_snowflake_schemas`
- [ ] `list_snowflake_tables`
- [ ] `describe_snowflake_table`
- [ ] Backend `DatasourceCapability` includes Snowflake mapping.
- [ ] Frontend query provider registry includes Snowflake tool and label mappings.
- [ ] Tool outputs include `query_metadata.provider="snowflake"`.

### Test Plan

1. Tool registry unit tests.
2. Capability lookup tests.
3. Frontend provider-registry mapping tests.

### Risks

- Missing capability entry breaks filtering and async routing.

### Rollout Notes

- Registry entry can be feature-flag gated without removing tool code.

---

## SNOW-013

### Title

Async Jobs Integration + `qj-sf-...` Prefix

### Objective

Integrate Snowflake tool execution with async query jobs and stable job identifier naming.

### Scope

- In scope: job registration, submit/poll/cancel/get handlers, ID prefix mapping.
- Out of scope: prompt/system guidance.

### Dependencies

- `SNOW-012`

### Files

- `server/internal/chat/jobs/registry.go`
- `server/internal/chat/jobs/registry_test.go`

### Implementation Notes

1. Snowflake async model is statement-handle-based, unlike Trino nextUri chain.
2. Job IDs must follow existing naming style.
3. Jobs lifecycle response contract must match the frozen async contract from `SNOW-001`.
4. V1 job registry is in-memory; restart behavior must be explicit, deterministic, and documented.

### Acceptance Criteria

- [ ] Snowflake async provider is registered in jobs registry.
- [ ] `newJobID()` maps Snowflake to `qj-sf-...`.
- [ ] Submit/poll/get/cancel functions work for Snowflake handles.
- [ ] Poll/get/cancel payload states and fields match the frozen async contract (`running`/`completed`/`failed`/`cancelled` with stable fields).
- [ ] Authorization/owner-token checks are unchanged.
- [ ] Restart/orphan behavior is explicitly defined and tested:
- [ ] after process restart, unknown `job_id` returns explicit not-found contract.
- [ ] durable in-memory recovery is explicitly marked v1 non-goal with operator guidance.

### Test Plan

1. Job ID prefix unit tests.
2. Async lifecycle tests for Snowflake provider.

### Risks

- Incorrect polling/cancel mapping can leak running statements.

### Rollout Notes

- Cancel behavior should be explicitly included in smoke tests.

---

## SNOW-014

### Title

Manager + Prompts + Runtime Wiring

### Objective

Integrate Snowflake provider in manager execution paths and prompt guidance.

### Scope

- In scope: manager wiring, provider inclusion checks, prompt family naming, async instructions.
- In scope: system prompt datasource guidance updates.
- Out of scope: UI rendering.

### Dependencies

- `SNOW-011`
- `SNOW-013`

### Files

- `server/internal/chat/optionc/manager.go`
- `server/internal/chat/optionc/prompts.go`
- `server/internal/chat/tools/types.go`
- `server/cmd/ai-analyst/main.go`

### Implementation Notes

1. `datasourcePromptFamilies()` should include explicit `snowflake` case.
2. Async prompt instructions must include `run_snowflake_query` behavior.
3. Async prompt/status handling must use existing runtime states (`running`, `completed`, `failed`, `cancelled`).
4. System prompt guidance should be generic across data providers, not duplicated by provider.

### Acceptance Criteria

- [ ] Manager includes Snowflake enablement checks and tool filtering.
- [ ] Prompt families include explicit Snowflake label handling.
- [ ] Async instruction text references `run_snowflake_query` for running jobs.
- [ ] Async instruction text and examples use runtime status values (`running`, `completed`, `failed`, `cancelled`).
- [ ] System prompt includes datasource-tool usage guidance compatible with Snowflake.
- [ ] Runtime wiring passes Snowflake handler/context into manager tool execution path.

### Test Plan

1. Manager inclusion/exclusion tests.
2. Prompt snapshot tests for enabled/disabled Snowflake.

### Risks

- Missing prompt updates can cause LLM misuse of async query workflow.

### Rollout Notes

- Prompt changes should be reviewed for regressions across existing providers.

---

## SNOW-015

### Title

Frontend Types, Migration, Service, Secrets Parsing, Chat/Query UI

### Objective

Update frontend provider models and rendering to fully support Snowflake query and metadata flows.

### Scope

- In scope: frontend provider unions, migrations, API service, secrets status parsing, provider-aware UI components.
- Out of scope: settings card UX.

### Dependencies

- `SNOW-003`
- `SNOW-005`
- `SNOW-012`
- `SNOW-014`

### Files

- `types.ts`
- `services/settingsMigration.ts`
- `services/secretsApi.ts`
- `services/snowflakeApi.ts` (new)
- `hooks/useChat.ts`
- `components/chat/RunningQueryCard.tsx`
- `components/chat/ToolCallBlock.tsx`
- `components/chat/ToolResultBlock.tsx`
- `components/layout/QueryTrayItem.tsx`
- `components/renderers/tool/CatalogBrowserRenderer.tsx`
- `lib/queryProviderRegistry.ts`

### Implementation Notes

1. Update provider unions everywhere they are currently `bigquery | databricks | trino`.
2. `services/secretsApi.ts` must parse backend booleans:
- `snowflake`
- `snowflakeOAuthToken`
- `snowflakeKeyPair`
3. Metadata UI labels must present Snowflake terms (`Databases`, `Schemas`, `Tables`).

### Acceptance Criteria

- [ ] Frontend provider unions include `snowflake` in all relevant interfaces.
- [ ] Settings migration preserves `dataSources.snowflake` state.
- [ ] Snowflake API service covers status/test/preflight/query + metadata endpoints.
- [ ] `services/secretsApi.ts` maps Snowflake status booleans correctly.
- [ ] Provider-aware chat/query UI files are updated:
- [ ] `RunningQueryCard.tsx`
- [ ] `ToolCallBlock.tsx`
- [ ] `ToolResultBlock.tsx`
- [ ] `QueryTrayItem.tsx`
- [ ] `CatalogBrowserRenderer.tsx`
- [ ] Query provider registry label mapping uses Snowflake-specific metadata terminology.

### Test Plan

1. Frontend unit tests for secrets status parsing and provider label mapping.
2. UI tests or storybook snapshots for Snowflake query states.
3. Manual chat flow with Snowflake tool call rendering.

### Risks

- Missing one union site can create runtime rendering gaps.

### Rollout Notes

- Keep provider label logic centralized in registry/mapping files.

---

## SNOW-016

### Title

Snowflake Cloud Credentials Card + UI Feature Flag Gate

### Objective

Implement Snowflake settings card using the existing Cloud Credentials pattern and feature-flag gating.

### Scope

- In scope: card component, tab integration, auth-mode form behavior, test-connection UX.
- In scope: feature flag gate so Tributary hides Snowflake card.
- Out of scope: backend query execution changes.

### Dependencies

- `SNOW-015`

### Files

- `components/settings/SnowflakeSection.tsx` (new)
- `components/settings/CloudCredentialsTab.tsx`
- `types.ts`
- `constants.ts`

### Implementation Notes

1. Follow Databricks/Trino card patterns for consistency.
2. Auth modes for UI are exactly `oauth_token` and `keypair_jwt`.

### Acceptance Criteria

- [ ] Snowflake card is rendered as a new Cloud Credentials provider card when feature flag is on.
- [ ] Card is hidden when `featureFlags.cloudCredentialsSnowflake` is false.
- [ ] Inputs include account, warehouse, database, schema, role, user.
- [ ] Auth selector supports OAuth token and key-pair JWT secret paths.
- [ ] Test connection uses `/api/snowflake/test` and displays status.
- [ ] Datasource enable toggle remains gated by successful connection validation.

### Test Plan

1. UI tests for feature-flag on/off card visibility.
2. UI tests for auth-mode switching and payload generation.
3. Manual settings smoke test in both build profiles.

### Risks

- Incorrect feature flag gate can leak unfinished provider in Tributary.

### Rollout Notes

- Safe disable path is feature flag off in build defaults.

---

## SNOW-017

### Title

Tests, Verification, and Release Readiness

### Objective

Execute comprehensive verification against ticket acceptance criteria and produce release evidence.

### Scope

- In scope: backend/frontend test updates, integration validation, manual smoke checklist.
- Out of scope: post-v1 enhancements.

### Dependencies

- `SNOW-016`

### Files

- `server/internal/chat/optionc/datasource_filter_test.go`
- `server/internal/chat/optionc/manager_test.go`
- `server/internal/chat/optionc/manager_query_test.go`
- `server/internal/chat/httpapi/handler_e2e_test.go`
- `server/internal/chat/jobs/registry_test.go`
- `server/internal/server/handler_test.go`
- `server/internal/config/settings_test.go`
- `server/internal/secrets/store_snowflake_test.go` (new)
- `server/internal/snowflake/auth_test.go`
- `server/internal/snowflake/handler_test.go`
- `server/internal/chat/tools/snowflake_tools_test.go` (new)
- `services/secretsApi.test.ts`
- `docs/runbooks/snowflake-v1-ops.md` (new)
- `plans/snowflake-integration-tickets/verification-matrix.md`

### Implementation Notes

1. Test files must match real repository paths; avoid placeholder filenames.
2. Include smoke flow evidence for status/test/preflight/query/metadata/tool/async paths.

### Acceptance Criteria

- [ ] All Snowflake-specific backend tests are present and passing.
- [ ] Frontend provider integration tests are passing.
- [ ] Route gating tests verify `/api/snowflake/*` is unavailable when `cloudCredentialsSnowflake=false`.
- [ ] HTTP e2e harness includes Snowflake run-level datasource override cases:
- [ ] run request with `snowflake` override is accepted and propagated to provider filtering.
- [ ] disabled/unvalidated Snowflake override is filtered with expected server behavior.
- [ ] Cross-layer secrets status contract is verified:
- [ ] backend configured booleans (`snowflake`, `snowflakeOAuthToken`, `snowflakeKeyPair`) are emitted correctly.
- [ ] frontend `services/secretsApi.ts` parses those booleans into expected UI state.
- [ ] Backend secrets-store tests verify Snowflake encryption/mode transitions/clear semantics.
- [ ] End-to-end smoke flow is passing:
- [ ] set secret
- [ ] status
- [ ] test connection
- [ ] preflight
- [ ] query
- [ ] metadata browse
- [ ] async query poll/cancel
- [ ] Tributary build hides Snowflake card/tools.
- [ ] All-providers build exposes Snowflake card/tools.
- [ ] Release-A baseline observability launch gate is met:
- [ ] structured logs exist for submit/poll/partition-fetch/cancel with required keys: `provider`, `snowflake_query_handle`, `job_id`, `request_id`, `event`, `status`.
- [ ] baseline metrics exist with fixed names: `snowflake_submit_latency_ms`, `snowflake_poll_attempt_total`, `snowflake_terminal_total`, `snowflake_cancel_attempt_total`.
- [ ] incident runbook exists at `docs/runbooks/snowflake-v1-ops.md` and includes restart/orphaned-job handling for v1 in-memory registry.
- [ ] Verification evidence is recorded in `verification-matrix.md`.

### Test Plan

1. Run targeted backend unit/integration suites, including `handler_e2e_test.go` Snowflake override coverage and `store_snowflake_test.go` secrets transitions.
2. Run frontend tests/build.
3. Perform manual smoke in both build profiles.

### Risks

- Shipping without cross-build verification can leak flags or break gating.

### Rollout Notes

- Release gate: no open `High` severity defects in Snowflake path.

---

## SNOW-FB-001

### Title

Fallback Path if Registry Work Is Not Merged

### Objective

Provide a mechanical Snowflake integration path when `REG-003` is unavailable.

### Scope

- In scope: additive boolean/switch expansion for Snowflake.
- Out of scope: registry refactor.

### Dependencies

- `SNOW-002`

### Files

- `server/internal/chat/optionc/manager.go`
- `server/internal/chat/optionc/datasource_filter.go`
- `types.ts`
- `hooks/useChat.ts`
- `components/chat/RunningQueryCard.tsx`
- `components/chat/ToolCallBlock.tsx`
- `components/chat/ToolResultBlock.tsx`
- `components/layout/QueryTrayItem.tsx`

### Implementation Notes

1. Keep changes mechanical and isolated.
2. Replace with registry path in post-v1 cleanup.

### Acceptance Criteria

- [ ] Snowflake ships without requiring `REG-003`.
- [ ] No broad refactor of manager hot paths is bundled into Release A.
- [ ] Follow-up task is created to remove fallback after registry lands.

### Test Plan

1. Re-run SNOW-017 matrix on fallback path.

### Risks

- Temporary duplication across provider checks.

### Rollout Notes

- This is a tactical bridge, not the long-term architecture.
