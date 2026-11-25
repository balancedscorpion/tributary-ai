# Release A: Trino V1 Delivery Tickets

## Goal

Ship Trino as a third datasource using the existing BigQuery/Databricks architecture, without bundling the broad provider-registry refactor.

## Ticket Order

| Ticket | Title | Depends On |
|---|---|---|
| TRINO-001 | Contract Freeze | None |
| TRINO-002 | Settings, Defaults, and Migration | TRINO-001 |
| TRINO-003 | Secrets Store Support | TRINO-001 |
| TRINO-004 | Secrets API Wiring | TRINO-003 |
| TRINO-005 | Shared Read-Only SQL Utility | TRINO-001 |
| TRINO-006 | Trino Handler Skeleton | TRINO-002, TRINO-004 |
| TRINO-007 | Trino Query Protocol Engine | TRINO-006 |
| TRINO-008 | Trino Metadata Endpoints | TRINO-007 |
| TRINO-009 | Server Route Wiring Only | TRINO-006 |
| TRINO-010 | Go Trino Tools | TRINO-007, TRINO-008 |
| TRINO-011 | Async Jobs Integration | TRINO-010 |
| TRINO-012 | Manager/ExecContext/Prompt/Main Wiring | TRINO-009, TRINO-011 |
| TRINO-013 | Frontend Mechanical Provider Expansion | TRINO-012 |
| TRINO-014 | Trino Settings UI + Service | TRINO-006, TRINO-013 |
| TRINO-015 | Tests + QA | TRINO-014 |

## TRINO-001: Contract Freeze

## Objective

Lock the Trino API, settings, and auth behavior contract before implementation.

## Files

- `docs/trino-integration-plan.md` (new)

## Acceptance Criteria

- [ ] Defines settings fields: `trinoHost`, `trinoCatalog`, `trinoSchema`, `trinoUser`, `trinoSource`.
- [ ] Defines Trino secret payloads for `/api/secrets/set` and `/api/secrets/clear`.
- [ ] Defines Trino routes: `status`, `test`, `preflight`, `query`, `catalogs`, `schemas`, `tables`, `table-schema`.
- [ ] Defines auth/user semantics:
- [ ] `basic` mode uses secrets username as `X-Trino-User`.
- [ ] `jwt` and `none` modes use settings `trinoUser`.
- [ ] Defines transport policy:
- [ ] `basic` and `jwt` require HTTPS.
- [ ] `none` allows HTTP for local/dev.

## TRINO-002: Settings, Defaults, and Migration

## Objective

Add Trino config fields and ensure migration preserves Trino datasource state.

## Files

- `server/internal/config/settings.go`
- `constants.ts`
- `services/settingsMigration.ts`
- `types.ts`

## Acceptance Criteria

- [ ] Adds settings fields: `trinoHost`, `trinoCatalog`, `trinoSchema`, `trinoUser`, `trinoSource`.
- [ ] Adds `dataSources.trino` with default `enabled=false`.
- [ ] Sets default `trinoSource` to `ai-analyst`.
- [ ] `services/settingsMigration.ts` updates `ensureDataSources()` to include:
- [ ] `trino: ensureDataSourceState((raw as any)?.trino)`.
- [ ] Existing settings load without dropping prior datasource state.

## TRINO-003: Secrets Store Support

## Objective

Store Trino auth material in encrypted secrets storage.

## Files

- `server/internal/secrets/store.go`

## Acceptance Criteria

- [ ] Adds `TrinoSecrets` with `Mode`, `Username`, `Password`, `JWTToken`.
- [ ] Adds methods: `GetTrino`, `SetTrinoBasicAuth`, `SetTrinoJWT`, `SetTrinoNone`, `ClearTrino`.
- [ ] Adds `ConfiguredStatus` flags: `Trino`, `TrinoBasic`, `TrinoJWT`.
- [ ] No Trino secret material is persisted to plain settings.

## TRINO-004: Secrets API Wiring

## Objective

Expose Trino auth set/clear operations through the existing secrets HTTP API.

## Files

- `server/internal/server/handler.go`

## Acceptance Criteria

- [ ] `/api/secrets/set` accepts Trino modes `basic`, `jwt`, and `none`.
- [ ] `/api/secrets/clear` accepts Trino authType values `basic`, `jwt`, and `all`.
- [ ] Invalid combinations return explicit HTTP 400 responses.

## TRINO-005: Shared Read-Only SQL Utility

## Objective

Remove duplicated read-only SQL logic and reuse a single implementation.

## Files

- `server/internal/sqlutil/readonly.go` (new)
- `server/internal/sqlutil/readonly_test.go` (new)
- `server/internal/bigquery/readonly.go` (delete)
- `server/internal/bigquery/readonly_test.go` (delete)
- `server/internal/databricks/readonly.go` (delete)
- `server/internal/databricks/readonly_test.go` (delete)
- `server/internal/bigquery/handler.go`
- `server/internal/databricks/handler.go`

## Acceptance Criteria

- [ ] BigQuery and Databricks read-only checks import from `server/internal/sqlutil/readonly.go`.
- [ ] Duplicate local `readonly.go` files are removed from BigQuery and Databricks packages.
- [ ] Consolidated tests exist under `server/internal/sqlutil/readonly_test.go`.
- [ ] Existing BigQuery and Databricks tests pass after migration to the shared import.

## TRINO-006: Trino Handler Skeleton

## Objective

Create Trino backend handler foundation with status/test and auth-aware host normalization.

## Files

- `server/internal/trino/handler.go` (new)
- `server/internal/trino/auth.go` (new)

## Acceptance Criteria

- [ ] `GET /api/trino/status` implemented.
- [ ] `POST /api/trino/test` implemented using lightweight query (`SELECT 1`).
- [ ] Host normalization enforces HTTPS for `basic`/`jwt` and allows HTTP only for `none`.
- [ ] Default headers include `X-Trino-Source` (`ai-analyst`) and `X-Trino-Time-Zone` (`UTC`).
- [ ] Test results persist datasource status fields in settings.

## TRINO-007: Trino Query Protocol Engine

## Objective

Implement production-safe Trino protocol execution via `POST /v1/statement` and `nextUri` chain.

## Files

- `server/internal/trino/handler.go`

## Acceptance Criteria

- [ ] Query flow follows mutable `nextUri` chain until terminal state.
- [ ] Stable `queryId` is captured when present.
- [ ] Cancellation supports:
- [ ] `DELETE` on current `nextUri`.
- [ ] fallback cancel via `DELETE /v1/query/{queryId}` when available.
- [ ] Retries only on transient `429/502/503/504` and honors `Retry-After`.
- [ ] Handles empty-result responses with no `nextUri` without hanging.
- [ ] Handles partial-results-then-error by returning clean failure (no loop/hang).
- [ ] Applies explicit timeout policy for each upstream request in the `nextUri` loop.
- [ ] Applies response size cap using `io.LimitReader(..., 8<<20)`.

## TRINO-008: Trino Metadata Endpoints

## Objective

Implement metadata routes for catalogs/schemas/tables/schema inspection.

## Files

- `server/internal/trino/handler.go`

## Acceptance Criteria

- [ ] Implements `/api/trino/catalogs`, `/api/trino/schemas`, `/api/trino/tables`, `/api/trino/table-schema`.
- [ ] Uses safe handler-owned metadata queries.
- [ ] Explicitly addresses read-only guard interaction for `SHOW`/`DESCRIBE` metadata queries.
- [ ] Returns normalized JSON payloads suitable for existing catalog UI flows.

## TRINO-009: Server Route Wiring Only

## Objective

Mount Trino routes in the server router without touching run-manager wiring.

## Files

- `server/internal/server/handler.go`

## Acceptance Criteria

- [ ] `HandlerOpts` includes `Trino *trino.Handler`.
- [ ] `/api/trino/*` routes are mounted behind existing activation/settings gate.
- [ ] No `server/cmd/ai-analyst/main.go` edits in this ticket.

## TRINO-010: Go Trino Tools

## Objective

Add Trino tools to the Go chat tool runtime.

## Files

- `server/internal/chat/tools/trino_tools.go` (new)
- `server/internal/chat/tools/registry.go`

## Acceptance Criteria

- [ ] Adds tools:
- [ ] `run_trino_query`
- [ ] `list_trino_catalogs`
- [ ] `list_trino_schemas`
- [ ] `list_trino_tables`
- [ ] `describe_trino_table`
- [ ] `run_trino_query` is preflight-first and row-limit aware.
- [ ] Tool outputs include `query_metadata.provider = "trino"` where applicable.

## TRINO-011: Async Jobs Integration

## Objective

Enable Trino long-running query lifecycle in shared async jobs system.

## Files

- `server/internal/chat/tools/trino_tools.go`
- `server/internal/chat/jobs/registry.go`
- `server/internal/chat/optionc/manager.go`

## Acceptance Criteria

- [ ] Trino async poll logic stores mutable `nextUri` and updates it between polls.
- [ ] Trino `queryId` is retained for status/cancel metadata.
- [ ] Job registry supports Trino job prefix (`tri` or agreed prefix).
- [ ] `check_query_status`, `get_query_results`, and `cancel_query` work for Trino jobs.

## TRINO-012: Manager, Prompt, ExecContext, and Main Wiring

## Objective

Mechanically thread Trino as a third datasource/provider through existing run path.

## Files

- `server/internal/chat/optionc/datasource_filter.go`
- `server/internal/chat/optionc/manager.go`
- `server/internal/chat/optionc/prompts.go`
- `server/internal/chat/tools/types.go`
- `server/internal/chat/httpapi/handler.go`
- `server/cmd/ai-analyst/main.go`

## Acceptance Criteria

- [ ] Datasource filter includes Trino tool map and enablement checks.
- [ ] Manager includes Trino enable boolean flow and tool definition inclusion.
- [ ] Prompt text includes Trino async guidance where query-tool names are listed.
- [ ] `ExecContext` includes `Trino *trino.Handler`.
- [ ] Run request datasource overrides accept and preserve `dataSources.trino.enabled`.
- [ ] `main.go` creates Trino handler and wires it to both server handler and run manager.

## TRINO-013: Frontend Mechanical Provider Expansion

## Objective

Extend existing hardcoded provider branches to support Trino without refactor.

## Files

- `types.ts`
- `hooks/useChat.ts`
- `components/chat/RunningQueryCard.tsx`
- `components/layout/QueryTrayItem.tsx`
- `components/chat/ToolCallBlock.tsx`
- `components/chat/ToolResultBlock.tsx`
- `components/renderers/tool/CatalogBrowserRenderer.tsx`

## Acceptance Criteria

- [ ] Provider unions include `trino` where query provider is typed.
- [ ] Existing BigQuery/Databricks behavior remains unchanged.
- [ ] Trino query states/results render correctly in running cards, tray, and tool result blocks.

## TRINO-014: Trino Settings UI and Service

## Objective

Add Trino settings card and frontend API client for status/test flows.

## Files

- `services/trinoApi.ts` (new)
- `components/settings/TrinoSection.tsx` (new)
- `components/settings/CloudCredentialsTab.tsx`

## Acceptance Criteria

- [ ] Card includes host/catalog/schema/user/source inputs.
- [ ] Card supports auth mode `basic|jwt|none`.
- [ ] Card uses secure secret set/clear flows for password/JWT.
- [ ] Test-before-enable gate mirrors Databricks behavior.
- [ ] `trinoApi` status/test methods are active once backend routes exist.

## TRINO-015: Test Coverage and QA

## Objective

Cover Trino integration with focused tests and a release QA checklist.

## Files

- `server/internal/trino/handler_test.go` (new)
- `server/internal/trino/auth_test.go` (new)
- `server/internal/chat/optionc/datasource_filter_test.go`
- `server/internal/chat/optionc/manager_test.go`
- `server/internal/chat/optionc/manager_query_test.go`
- `server/internal/chat/jobs/registry_test.go`
- `server/internal/config/settings_test.go`
- `server/internal/chat/tools/trino_tools_test.go` (new)

## Acceptance Criteria

- [ ] Tests cover Trino secrets contract and auth-aware host normalization.
- [ ] Tests cover Trino async `nextUri` polling, empty result, partial-then-fail, and cancel behavior.
- [ ] Tests cover datasource gating and manager tool inclusion for Trino.
- [ ] Tests cover Trino job id prefix behavior.
- [ ] Manual QA checklist exists for setup, test, enable, query, async status, and cancel.
