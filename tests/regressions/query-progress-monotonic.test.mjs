import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('query progress policy is centralized and monotonic', () => {
  const progressSource = sourceAt('lib/queryProgress.ts');
  const sharedStatusSource = sourceAt('hooks/useSharedQueryStatus.ts');

  assert.ok(
    progressSource.includes('resolveMonotonicQueryElapsedMs'),
    'query progress should clamp elapsed time in the shared progress helper'
  );
  assert.ok(
    progressSource.includes('resolveQueryDisplayProgressPercent'),
    'query progress should resolve display percent in one shared helper'
  );
  assert.ok(
    progressSource.includes('mergeQueryStatusSnapshot'),
    'shared polling snapshots should be merged through the monotonic progress helper'
  );
  assert.ok(
    sharedStatusSource.includes('mergeQueryStatusSnapshot(entry.snapshot, payload, entry.startedAtMs)'),
    'polling should use the shared monotonic snapshot merge'
  );
  assert.ok(
    sharedStatusSource.includes('isTerminalQueryStatus(nextSnapshot.status)'),
    'polling should stop once a terminal query status is observed'
  );
});

test('query surfaces reuse the shared progress policy', () => {
  const taskBarSource = sourceAt('components/layout/TaskBar.tsx');
  const traySource = sourceAt('components/layout/QueryTray.tsx');
  const trayItemSource = sourceAt('components/layout/QueryTrayItem.tsx');
  const stateHookSource = sourceAt('components/renderers/tool/query/useQueryInspectorState.ts');
  const executionCardSource = sourceAt('components/chat/QueryExecutionCard.tsx');
  const runningCardSource = sourceAt('components/chat/RunningQueryCard.tsx');

  assert.ok(
    taskBarSource.includes('deriveQueryProgressDisplay({'),
    'TaskBar should derive query progress through the shared helper'
  );
  assert.ok(
    traySource.includes('useSharedQueryStatuses(pollTargets)'),
    'QueryTray should subscribe to the same shared status snapshots as TaskBar'
  );
  assert.ok(
    trayItemSource.includes('deriveQueryProgressDisplay({'),
    'QueryTrayItem should derive query progress through the shared helper'
  );
  assert.ok(
    stateHookSource.includes('resolveQueryDisplayProgressPercent({'),
    'useQueryInspectorState should derive polling percent through the shared helper'
  );
  assert.ok(
    stateHookSource.includes('getDeterministicQueryToolProgressPercent(sseProgress)'),
    'useQueryInspectorState should derive executing percent through the shared helper'
  );
  assert.ok(
    executionCardSource.includes('running.progressPercent')
      && !executionCardSource.includes('resolveQueryDisplayProgressPercent('),
    'QueryExecutionCard should consume shared progress state instead of computing its own'
  );
  assert.ok(
    runningCardSource.includes('resolveQueryDisplayProgressPercent({'),
    'RunningQueryCard should derive polling percent through the shared helper'
  );
});

// Regression: the inspector's polling percent was computed without
// previousProgressPercent, so the bar jumped backwards when the source
// switched (SSE estimate → first server-reported percent, or a provider
// briefly reporting a lower value) and reset between phases.
test('inspector progress is monotonic across phases and completes at 100', () => {
  const stateHookSource = sourceAt('components/renderers/tool/query/useQueryInspectorState.ts');

  assert.ok(
    stateHookSource.includes('previousProgressPercent: lastProgressRef.current'),
    'polling percent must clamp against the last displayed value'
  );
  assert.ok(
    stateHookSource.includes("if (phase === 'completed') return 100;"),
    'completed phase must snap progress to exactly 100'
  );
  assert.ok(
    stateHookSource.includes("if (phase === 'failed' || phase === 'cancelled') return null;"),
    'failed/cancelled phases must settle the bar instead of leaving a stale percent'
  );
  assert.ok(
    stateHookSource.includes('lastProgressRef.current = null;'),
    'the monotonic clamp must reset when the inspector identity changes or the query ends'
  );
});

