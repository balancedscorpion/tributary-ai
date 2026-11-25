# Query Polling UI Requirements

## Problem Statement
During async query execution, the UI can render internal polling turns (`check_query_status`, non-terminal `get_query_results`) as visible assistant/tool activity. This creates empty assistant rows, duplicate polling checks, and stale `Working` indicators that do not reflect true step completion.

## Required Behavior
1. Assistant and tool output must complete sequentially in the UI.
2. Internal polling checks must not render as user-visible transcript steps.
3. Non-terminal `get_query_results` responses must not create standalone cards/messages.
4. Terminal `get_query_results` responses must resolve the original `run_*_query` card.
5. Persisted history should not retain poll-noise as visible UI messages.

## UI Contract
- Hide assistant tool-call shells for polling utilities (`check_query_status`, `get_query_results`).
- Suppress tool result rendering when response payload indicates poll-only/internal state (`ui_suppress`, `retryable`).
- Keep run-query cards as the single source of truth for query lifecycle display.

## Backend Contract
- Treat `ui_suppress`/`ui_hidden` markers as authoritative for UI-hidden persistence.
- Mark non-terminal `get_query_results` responses as UI-hidden.
- Preserve terminal `get_query_results` visibility so query cards can transition to completed.

## Result Mapping Contract
- `run_*_query` tool results establish ownership for query artifact IDs.
- `get_query_results` tool results with owned artifact IDs must be absorbed into the owning `run_*_query` card state.
- Poll-only checks must not create additional visible query cards.

## Root Cause Summary
- Suppression flags were emitted in payloads but not consistently consumed by backend/UI suppression paths.
- Assistant shell filtering hid `check_query_status` but not `get_query_results`.
- SQL card state mapping did not absorb `get_query_results` into run-query entries.

## Validation
- Frontend regressions for empty poll shells and unified query card behavior.
- Backend tests for suppression semantics on retryable `get_query_results`.
- Full app build verification.
