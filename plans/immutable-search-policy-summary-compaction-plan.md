# Immutable Search Policy + Summary Compaction Plan

## Problem

Anthropic near-cap fallback currently mutates runtime behavior from analysis to search mode (`searchMode = true`), which strips analysis tools and causes failures like:

- `tool is not allowed in search mode: read_file`
- `tool is not allowed in search mode: run_bigquery_query`

This violates user intent (analysis run) and reduces capabilities exactly when context pressure is highest.

## Goals

1. Keep run policy immutable: analysis runs must stay analysis runs.
2. Avoid context-cap failures through compaction, not policy mutation.
3. Preserve analysis tool capabilities under pressure.
4. Add reliable failure handling for compaction/summarization.
5. Keep behavior observable and testable.

## Non-Goals

1. Redesign of provider adapters.
2. New storage systems for memory.
3. Changing search-mode tool restrictions for true search runs.

## Design Overview

### 1) Immutable Search Policy

- Compute a strict policy flag from persisted run purpose:
  - `strictSearchMode := (run.Purpose == "search")`
- Use `strictSearchMode` for search-only gating and restrictions.
- Do not toggle search mode as a side effect of context overflow recovery.

Expected result:
- Analysis runs never become search runs mid-loop.
- Search-mode restrictions still apply to runs explicitly created with `purpose=search`.

### 2) Summary-Based Mid-Run Compaction

When request tokens exceed budget, replace older history with a compact synthetic summary message, then keep the most recent working set.

Compaction policy:

1. Soft threshold (default 88% of active budget):
   - Trigger summary compaction of dropped prefix.
2. Hard threshold (default 97% of active budget):
   - Trigger aggressive trim after summary compaction if still over budget.

Summary generation strategy:

1. Try LLM summarization with a short timeout (e.g. 2-4s).
2. On timeout/error/empty summary, fallback to deterministic summary (existing string-based approach).
3. If still over budget, fallback to `trimHistoryToTokenBudget`.

Safety:

1. Compaction must never fail the run by itself.
2. Compaction failure should degrade gracefully to deterministic summary + trim.
3. Avoid repeated work by reusing recent prefix summary within a run when possible.

Failure-mode contract (explicit):

1. LLM summary attempt is best-effort only.
2. On timeout/error/empty output, immediately degrade to `summarizeAnthropicToolHistory` style summary.
3. If deterministic summary still does not get under budget, apply aggressive trim.
4. If aggressive trim still cannot satisfy hard cap, fail with context overflow (never mutate run policy).

### 3) Near-Cap Fallback Behavior

Keep near-cap fallback, but scope it to token recovery only:

1. Mark `HardCapped/OverflowCompacted`.
2. Rebuild prompt/tool schemas.
3. Compact history.
4. Retry dispatch.

Do not mutate search policy or tool-family policy in this path.

Decision on `applyNearCapacityFallback`:

1. Keep the function, but refactor its responsibility to "near-cap compaction recovery."
2. Remove search-mode mutation (`searchMode = true`) entirely.
3. Keep aggressive trim behavior, but run it only after summary compaction attempts.
4. Prefer a cap-aware aggressive target over hardcoded `hardInputCap/2`:
   - First aggressive pass: trim to near hard cap after tool overhead.
   - Optional second aggressive pass: tighter budget if still above hard cap.
5. If both passes fail, return `false` and let existing overflow failure handling terminate the run cleanly.

### 4) Capability Preservation

Maintain analysis tool families in analysis runs:

1. File tools (`read_file`, etc.)
2. Datasource tools (`run_bigquery_query`, etc.)
3. Existing compacted tool schema behavior for Anthropic to reduce token overhead

Search-only allowlist remains unchanged for true search runs.

Additional constraint for overflow recovery:

1. Do not switch to search-only tool definitions for analysis runs.
2. If token pressure remains high, reduce history first, then schema payload compactness, not capability class.

### 5) Persistent Thread Summary Alignment

Thread-level summary jobs remain complementary to run-time compaction:

1. Runtime compaction controls immediate token pressure.
2. Background thread summaries improve cross-run memory quality.

Reference implementation is in `server/internal/chat/optionc/worker.go`.

## Implementation Steps

### Phase 1: Immutable Policy Refactor

1. Update `server/internal/chat/optionc/manager.go`:
   - Introduce `strictSearchMode` based on run purpose.
   - Replace gating calls to use `strictSearchMode`.
2. Remove near-cap mutation to search mode:
   - Delete `searchMode = true` behavior in `applyNearCapacityFallback`.

Acceptance:

1. Analysis run under overflow never emits `tool is not allowed in search mode` for analysis tools solely due to fallback.
2. Explicit search run still enforces search-only tool allowlist.

### Phase 2: Mid-Run Summary Compaction

1. Extend `midRunCompact` in `manager.go`:
   - Add soft/hard threshold checks.
   - Build summary for dropped history prefix.
   - Keep recent working set + synthetic summary message.
2. Add summary helper functions:
   - LLM summary with timeout.
   - Deterministic fallback summary.
   - Optional within-run prefix-summary memoization to avoid repeated summarization of identical dropped prefixes.
3. Integrate fallback chain:
   - LLM summary -> deterministic summary -> aggressive trim.

Acceptance:

1. Near-cap runs recover without mode mutation.
2. Compaction remains functional if LLM summary fails.
3. Final request estimate drops below active cap in typical overflow cases.

### Phase 2.5: `attemptCapRecovery` / no-planner path hardening

1. Update the `attemptCapRecovery` branch where `contextPlanner == nil` to rely on refactored near-cap compaction fallback (without search-mode mutation).
2. Ensure the fallback sequence is:
   - summary compaction,
   - deterministic fallback summary,
   - aggressive trim pass(es),
   - hard-cap failure if still too large.
3. Recompute request estimate after each stage and short-circuit on success.

Acceptance:

1. `contextPlanner == nil` runs can still recover from moderate overflow without mode switching.
2. Extremely large requests fail explicitly as context overflow instead of silently degrading capability policy.

### Phase 3: Hardening + Telemetry

1. Log compaction decisions:
   - threshold crossed
   - strategy used (llm, fallback, trim)
   - estimated token before/after
2. Persist context-plan indicators already used by run metadata.

Acceptance:

1. Logs clearly explain why compaction happened.
2. Run metadata reflects compaction state consistently.

## Test Plan

Update tests in:

- `server/internal/chat/optionc/manager_test.go`
- `server/internal/chat/optionc/manager_regression_test.go`
- `server/internal/chat/optionc/manager_query_test.go` (audit + targeted updates only if assumptions break)

Add/adjust cases:

1. Analysis run + near-cap fallback:
   - does not become search mode.
   - does not block `read_file`/`run_bigquery_query` due to search-mode policy.
2. Search run:
   - still blocks non-search tools.
3. Compaction with summarizer failure:
   - deterministic fallback is used.
   - run continues.
4. Hard threshold overflow:
   - aggressive trim applied.
   - dispatch still attempted.
5. `contextPlanner == nil` recovery path:
   - does not mutate search policy.
   - uses compaction fallback chain.
   - still fails cleanly when irreducible.

Test-impact note:

1. Existing tests that implicitly relied on search-mode mutation must be updated or replaced.
2. Query-focused tests (`manager_query_test.go`) are expected to be mostly unaffected, but should be audited for shared helper assumptions about fallback behavior.

## Risks and Mitigations

1. Risk: LLM summary call adds latency.
   - Mitigation: strict timeout + fallback path.
2. Risk: repeated compaction churn.
   - Mitigation: hysteresis thresholds + prefix summary reuse.
3. Risk: still exceeding budget with large tool schemas.
   - Mitigation: retain Anthropic schema compaction and staged aggressive trim fallback.
4. Risk: no-planner path becomes less recoverable after removing search-mode switch.
   - Mitigation: explicit staged fallback in `attemptCapRecovery` + clear terminal overflow handling.

## Rollout Notes

1. Land immutable policy change first (low risk, high impact).
2. Land summary compaction with fallback next.
3. Enable threshold tuning via constants in code (not env vars).
4. Verify with Anthropic near-cap scenarios before wider rollout.
