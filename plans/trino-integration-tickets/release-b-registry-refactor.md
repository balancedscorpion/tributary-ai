# Release B: Query-Provider Registry Refactor Tickets

## Goal

Refactor provider-specific query behavior into registry-driven structures after Trino is stable, so Snowflake can be added with minimal branching changes.

## Ticket Order

| Ticket | Title | Depends On |
|---|---|---|
| REG-001 | Frontend Query Provider Registry | TRINO-015 |
| REG-002 | Backend Datasource Capability Registry | TRINO-015 |
| REG-003 | Prompt Integration with Registry | REG-001, REG-002 |
| REG-004 | Snowflake Readiness Validation | REG-003 |

## REG-001: Frontend Query Provider Registry

## Objective

Replace hardcoded provider conditionals in UI query paths with a single frontend registry.

## Files

- `lib/queryProviderRegistry.ts` (new)
- `hooks/useChat.ts`
- `components/chat/RunningQueryCard.tsx`
- `components/layout/QueryTrayItem.tsx`
- `components/chat/ToolCallBlock.tsx`
- `components/chat/ToolResultBlock.tsx`
- `components/renderers/tool/CatalogBrowserRenderer.tsx`

## Acceptance Criteria

- [ ] Registry defines label/icon/tool-name membership/link builder per provider.
- [ ] Query tool checks and provider label logic consume registry, not ad-hoc ternaries.
- [ ] BigQuery, Databricks, and Trino behavior is unchanged after migration.

## REG-002: Backend Datasource Capability Registry

## Objective

Replace multi-boolean provider flow in orchestration with a capability/registry pattern.

## Files

- `server/internal/chat/optionc/datasource_filter.go`
- `server/internal/chat/optionc/manager.go`
- `server/internal/chat/tools/types.go`

## Acceptance Criteria

- [ ] Provider enablement and tool membership are registry-driven.
- [ ] Manager no longer needs repeated provider-specific boolean plumbing across call chains.
- [ ] Tool filtering behavior remains unchanged for BigQuery, Databricks, and Trino.

## REG-003: Prompt Integration with Registry

## Objective

Generate query-tool guidance in prompts from centralized provider definitions.

## Files

- `server/internal/chat/optionc/prompts.go`

## Acceptance Criteria

- [ ] Prompt text references query tools via shared list/registry source.
- [ ] Adding a new query provider does not require manual hardcoded prompt edits in multiple places.

## REG-004: Snowflake Readiness Validation

## Objective

Validate that Snowflake can be added as a provider-entry-first change after refactor.

## Files

- `docs/snowflake-integration-readiness.md` (new)

## Acceptance Criteria

- [ ] Documents exact backend and frontend extension points for Snowflake.
- [ ] Confirms no additional provider-specific branching is required in hot paths beyond registry entries and handler/tool additions.
