import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('TaskBar row click opens query details dialog with shared progress and timer derivation', () => {
  const source = sourceAt('components/layout/TaskBar.tsx');

  assert.ok(
    source.includes('query.description ?? query.queryMetadata?.description'),
    'TaskBar should prioritize natural-language query description when available'
  );
  assert.ok(
    source.includes('const [selectedQueryArtifactId, setSelectedQueryArtifactId] = useState<string | null>(null);')
      && source.includes('setSelectedQueryArtifactId(query.artifactId);'),
    'TaskBar should open a selected query details dialog when a row is clicked'
  );
  assert.ok(
    source.includes('const queryUiByArtifactId = useMemo(() => {')
      && source.includes('deriveQueryUiState(query, nowMs)')
      && source.includes('ui={queryUiByArtifactId.get(q.artifactId) ?? deriveQueryUiState(q, nowMs)}'),
    'TaskBar should derive query status/progress once and reuse it for row rendering'
  );
  assert.ok(
    source.includes('<QueryDetailDialog')
      && source.includes('ui={selectedQueryUi}')
      && source.includes('<CodeBlock language="sql">{sqlText}</CodeBlock>'),
    'TaskBar should render a query-detail dialog with SQL code block from the selected query'
  );
  assert.ok(
    source.includes('if (isComplete) {')
      && source.includes('progressPercent = 100;')
      && source.includes('showProgress: (isRunning || isComplete) && progressPercent != null'),
    'TaskBar should force 100% progress for completed queries and keep determinate progress visibility logic centralized'
  );
  assert.ok(
    source.includes('timerText: isRunning ? formatElapsed(elapsedMs) : formatDuration(frozenDurationMs)'),
    'TaskBar should freeze timer text after completion via shared derived timer state'
  );
});

test('useQueryMonitor hydrates description and sql fields from query metadata', () => {
  const source = sourceAt('hooks/useQueryMonitor.ts');

  assert.ok(
    source.includes('description: firstString(meta.description, queryMetadata?.description)'),
    'useQueryMonitor should read natural-language descriptions from artifact/query metadata'
  );
  assert.ok(
    source.includes('sql: firstString(meta.sql, queryMetadata?.sql)'),
    'useQueryMonitor should capture full SQL text from artifact/query metadata'
  );
  assert.ok(
    source.includes('sqlPreview: firstString(meta.sql_preview, queryMetadata?.sql_preview)'),
    'useQueryMonitor should capture SQL preview text from query metadata'
  );
  assert.ok(
    source.includes('activeThreadIdInput?: string | null') && source.includes('matchesThreadScope'),
    'useQueryMonitor should accept an active-thread scope and apply thread matching'
  );
  assert.ok(
    source.includes('if (!matchesThreadScope(tracked.threadId, activeThreadId, hasScope)) continue;'),
    'useQueryMonitor should only return tracked queries for the active thread'
  );
  assert.ok(
    source.includes('const statusFromMetadata = parseQueryJobStatus(meta?.status ?? queryMetadata?.status);')
    && source.includes('if (statusFromMetadata && isTerminalStatus(statusFromMetadata)) {'),
    'useQueryMonitor should honor terminal status metadata to stop running timers immediately'
  );
});

test('get_query_results preserves stored SQL metadata from async job registry', () => {
  const source = sourceAt('server/internal/chat/tools/query_lifecycle.go');

  assert.ok(
    source.includes('queryMetadata["sql"] = sql'),
    'get_query_results should return full SQL from the registered async job'
  );
  assert.ok(
    source.includes('queryMetadata["sql_preview"] = sqlPreview'),
    'get_query_results should return SQL preview from the registered async job'
  );
});

test('TaskBar is rendered in-thread and passes active thread scope to query monitor', () => {
  const appSource = sourceAt('App.tsx');
  const taskBarSource = sourceAt('components/layout/TaskBar.tsx');

  assert.ok(
    appSource.includes('activeThreadId={activeThreadId}') && appSource.includes('<MessageList'),
    'App should render TaskBar in the active thread panel with activeThreadId scope'
  );
  assert.ok(
    appSource.includes('onProjectLoaded={handleProjectLoaded}\n        />\n\n        <div className="flex-1 min-h-0 relative flex">'),
    'App should not render TaskBar as a global strip between Header and thread layout'
  );
  assert.ok(
    taskBarSource.includes('useQueryMonitor(artifactsById, activeThreadId)'),
    'TaskBar should scope query monitor state by active thread'
  );
  assert.ok(
    appSource.includes('const tid = String(threadId || activeThreadId || \'\').trim();') && appSource.includes('if (!tid) return;'),
    'Query cancel requests should always be thread-scoped before hitting backend query endpoints'
  );
});
