# Streaming Flicker Root Cause Analysis

## Symptom
When assistant text is streamed token-by-token, the active assistant bubble visibly flickers.

## Root cause
The currently streaming assistant message is repeatedly **unmounted/remounted** during each text delta.

### Why this happens
1. `useChat` appends text on every `text_delta` event by replacing the last assistant message with a new object containing updated `content`.
2. `MessageList` computes row keys from message identity.
3. If a message does not yet have `id`/`seq`, identity falls back to a hash derived from message content.
4. Because streaming changes `content` every delta, that hash changes every delta.
5. The rendered key changes each token update, so React treats the message as a new element and remounts it.
6. `AssistantMessage` root node includes `animate-fade-in`, so every remount replays the entrance animation and appears as flicker.

## Secondary contributors
- The list also rebuilds message references and stable identities on every token update, increasing render work.
- Auto-scroll effects re-run for every message update (expected), which can amplify perceived jitter under heavy streaming.

## Implementation plan

### Phase 1 (high impact, low risk)
1. Introduce a stable UI-only identity on message creation (for placeholders and locally-added messages), e.g. `_uiStableId`.
2. In `MessageList`, use identity priority:
   - `message.id`
   - `message.seq`
   - `_uiStableId`
   - content hash fallback (last resort only)
3. Ensure streaming updates preserve `_uiStableId` when replacing/merging messages.

Expected result: assistant message key remains stable throughout streaming; no remount per token.

### Phase 2 (reduce render churn)
1. Throttle/batch streaming text updates to animation frames (`requestAnimationFrame`) or small intervals (20–50ms).
2. Keep intermediate deltas in refs and commit state in batches.
3. Apply same batching approach to `thinking_delta`.

Expected result: lower React commit frequency and smoother updates on slower devices.

### Phase 3 (UX hardening)
1. Gate `animate-fade-in` so it applies only on first mount (or disable during active streaming).
2. Add a regression test strategy:
   - Unit/integration test for identity stability across multiple deltas.
   - Optional visual e2e assertion that assistant bubble element node persists while content grows.
3. Profile with React DevTools/Performance panel before/after to confirm reduced mounts and commit count.

## Rollout and validation
1. Implement Phase 1 and verify no regressions in message reference pinning and tool-call rendering.
2. Add Phase 2 batching if needed after measuring CPU/paint during long responses.
3. Capture before/after perf traces on a long streamed response and document metrics (commits, scripting time, dropped frames).
