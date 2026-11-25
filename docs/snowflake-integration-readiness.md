# Snowflake Integration Readiness

This document validates that adding Snowflake after the registry refactor is a provider-entry-first change.

## Backend Extension Points

1. Add Snowflake datasource capability entry in `server/internal/chat/tools/types.go`:
   - datasource name
   - run-query tool name
   - metadata tool names
   - async capability flag
2. Implement Snowflake tools in `server/internal/chat/tools/` and register them in `server/internal/chat/tools/registry.go`.
3. Add Snowflake datasource enablement wiring through existing datasource maps (`server/internal/chat/optionc/datasource_filter.go` and manager call sites), without new provider-specific booleans.
4. Add optional Snowflake HTTP handler and route mounting in `server/internal/server/handler.go`.

## Frontend Extension Points

1. Add a provider entry in `lib/queryProviderRegistry.ts` for labels/icons/tool membership/link behavior.
2. Ensure UI consumers (`hooks/useChat.ts`, query cards/blocks, tray items, catalog renderer) rely on registry metadata, not provider-specific conditionals.
3. Add Snowflake settings UI and API client module following the Trino/Databricks section pattern.

## Readiness Check

- Provider/tool filtering is registry-driven.
- Async query lifecycle tools remain provider-agnostic.
- Prompt tool guidance is generated from registry-backed capability lists.

Given the above, Snowflake integration does not require new provider-specific branching in orchestration hot paths beyond registry entries and Snowflake-specific handler/tool implementations.
