import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('MessageList scopes unified SQL result absorption using run/message-aware keys', () => {
  const source = sourceAt('components/chat/MessageList.tsx');

  assert.ok(
    source.includes('sqlToolResultsByScopedKey'),
    'MessageList should hold SQL tool results in a scoped-key map'
  );
  assert.ok(
    source.includes('getQueryCardStateKey({'),
    'MessageList should derive scoped query card state keys'
  );
  assert.ok(
    source.includes('getEnvelopeResultRunId(envelope)'),
    'MessageList should recover run ids from envelope result metadata'
  );
  assert.ok(
    source.includes("if (msg.name === 'get_query_results')"),
    'MessageList should retain explicit get_query_results dedupe branch'
  );
  assert.ok(
    source.includes('if (!artifactId || !ownedIds.has(artifactId)) continue;'),
    'MessageList should suppress duplicate get_query_results rows only when artifact ownership is absorbed'
  );
});

test('CollapsedToolsSummary resolves SQL unified card results using scoped keys', () => {
  const source = sourceAt('components/chat/CollapsedToolsSummary.tsx');

  assert.ok(
    source.includes('const scopedToolKey = getQueryCardStateKey({'),
    'CollapsedToolsSummary should look up SQL result state via scoped tool keys'
  );
  assert.ok(
    source.includes('sqlToolResultsByScopedKey?.get(scopedToolKey)'),
    'CollapsedToolsSummary should read SQL result state from the scoped map'
  );
  assert.ok(
    source.includes('const isExec =')
      && source.includes('isLastMessage === true && hasStartedAt && !resultInfo'),
    'CollapsedToolsSummary should keep SQL cards executing only until a scoped result is absorbed'
  );
});

test('QueryExecutionCard is a compact opener that delegates rendering to the side inspector', () => {
  const cardSource = sourceAt('components/chat/QueryExecutionCard.tsx');
  const rendererSource = sourceAt('components/renderers/tool/query/QueryToolRenderer.tsx');

  assert.ok(
    !cardSource.includes('<QueryInspector'),
    'QueryExecutionCard must not render the full QueryInspector inline in chat'
  );
  assert.ok(
    cardSource.includes('useQueryInspectorState({'),
    'QueryExecutionCard should derive all state through the shared inspector hook'
  );
  assert.ok(
    cardSource.includes('onOpenArtifact(openTarget)'),
    'QueryExecutionCard click should open the artifact side panel'
  );
  assert.ok(
    cardSource.includes('buildArtifactFromEnvelope(toolResultEnvelope, toolCall.name, null)'),
    'QueryExecutionCard should derive an openable artifact from the envelope when the map lags'
  );
  assert.ok(
    cardSource.includes('pending-query:'),
    'QueryExecutionCard should open a derived submitting placeholder before any envelope exists'
  );
  assert.ok(
    rendererSource.includes('<QueryInspector'),
    'QueryToolRenderer (artifact panel) should remain the surface that renders the full inspector'
  );
});

test('QueryExecutionCard reports the remote query lifecycle to its tool group', () => {
  const cardSource = sourceAt('components/chat/QueryExecutionCard.tsx');
  const groupSource = sourceAt('components/chat/CollapsedToolsSummary.tsx');

  assert.ok(
    cardSource.includes('onStatusChange(statusStateKey, status);'),
    'QueryExecutionCard should report live polling status to the parent tool group'
  );
  assert.ok(
    cardSource.includes('data-query-status={status}'),
    'QueryExecutionCard should expose its resolved lifecycle for UI regressions'
  );
  assert.ok(
    groupSource.includes('resolveQueryToolLifecycleStatus({'),
    'CollapsedToolsSummary should classify SQL results by query lifecycle'
  );
  assert.ok(
    groupSource.includes('return isActiveQueryStatus(queryStatus);'),
    'active remote queries should keep the tool group running after launch'
  );
  assert.ok(
    groupSource.includes('} else if (doneCount === totalCount) {'),
    'the collapsed group should claim all tools succeeded only when every tool is terminal'
  );
});

test('App upgrades the open panel snapshot when query artifacts materialise', () => {
  const source = sourceAt('App.tsx');

  assert.ok(
    source.includes('a && a !== activeArtifact && !isStale(a)'),
    'App should swap a stale placeholder snapshot for the materialised artifact with the same id'
  );
  assert.ok(
    source.includes("if (activeArtifact.metadata?.pre_submit !== true) return;"),
    'App should only widen matching to tool_call_id for pre-submit query placeholders'
  );
  assert.ok(
    source.includes("String(a.metadata?.tool_call_id || '').trim() === callId"),
    'App should upgrade pre-submit placeholders by tool_call_id once a real artifact exists'
  );
});

test('Query inspector state does not surface placeholder artifacts as completed results', () => {
  const stateSource = sourceAt('components/renderers/tool/query/useQueryInspectorState.ts');
  const inspectorSource = sourceAt('components/renderers/tool/query/QueryInspector.tsx');

  assert.ok(
    stateSource.includes('const renderableArtifact = useMemo('),
    'useQueryInspectorState should derive a non-placeholder renderable artifact'
  );
  assert.ok(
    stateSource.includes('return artifact && meta.placeholder !== true ? artifact : null;'),
    'useQueryInspectorState should filter placeholder artifacts before result rendering'
  );
  assert.ok(
    inspectorSource.includes('<CompletedResponse state={state} artifact={resolvedArtifact} showRawEnvelope />'),
    'QueryInspector completed renderer should require the non-placeholder resolved artifact'
  );
});

test('QueryExecutionCard preserves tool-result snapshots for message references', () => {
  const source = sourceAt('components/chat/QueryExecutionCard.tsx');

  assert.ok(
    source.includes('const resultSnapshot = useMemo(() => {'),
    'QueryExecutionCard should compute a result snapshot when tool results are present'
  );
  assert.ok(
    source.includes("const kind: MessageBlockReference['kind'] = resultSnapshot ? 'tool_result' : 'tool_call';"),
    'QueryExecutionCard should expose tool_result references when result content exists'
  );
  assert.ok(
    source.includes('const contentSnapshot = resultSnapshot || argsSnapshot;'),
    'QueryExecutionCard references should use the result-aware snapshot payload'
  );
  assert.ok(
    source.includes("const idPrefix = resultSnapshot ? 'tool_result' : 'tool_call';"),
    'QueryExecutionCard should emit tool_result-scoped reference ids once results are absorbed'
  );
  assert.ok(
    source.includes('? resultSourceMessageId || callSourceMessageId'),
    'QueryExecutionCard should prefer absorbed tool-result message ids for reference parity'
  );
});

test('Query inspector state keeps cancel reachable in executing and polling phases', () => {
  const stateSource = sourceAt('components/renderers/tool/query/useQueryInspectorState.ts');
  const runningSource = sourceAt('components/renderers/tool/query/RunningResponse.tsx');

  assert.ok(
    stateSource.includes("(phase === 'executing' || phase === 'polling') && Boolean(cancelRequestUrl)"),
    'cancel should stay possible while executing or polling'
  );
  assert.ok(
    stateSource.includes('const cancelRequestUrl = cancelUrl || runCancelUrl;'),
    'cancel should fall back to run cancel when query cancel url is unavailable'
  );
  assert.ok(
    runningSource.includes('running.canCancel') && runningSource.includes('void running.onCancel();'),
    'RunningResponse should render the cancel affordance off the shared state'
  );
});

test('Query inspector state falls back to argsText for SQL and description derivation', () => {
  const source = sourceAt('components/renderers/tool/query/useQueryInspectorState.ts');

  assert.ok(
    source.includes("const rawArgsText = typeof toolCall?.argsText === 'string' ? toolCall.argsText.trim() : '';"),
    'useQueryInspectorState should inspect toolCall.argsText when parsed args are missing or incomplete'
  );
  assert.ok(
    source.includes('const parsed = JSON.parse(rawArgsText);'),
    'useQueryInspectorState should parse raw tool args text as fallback'
  );
  assert.ok(
    source.includes('argsTextFallback.description,'),
    'description derivation should consult the argsText fallback'
  );
  assert.ok(
    source.includes('argsTextFallback.sql,'),
    'sql derivation should consult the argsText fallback'
  );
});

test('Envelope-derived artifacts preserve tool_call_id for the inspector header', () => {
  const source = sourceAt('hooks/useChat.ts');

  const occurrences = source.split(
    '...(envelope.tool_call_id ? { tool_call_id: envelope.tool_call_id } : {}),'
  ).length - 1;
  assert.ok(
    occurrences >= 2,
    'buildArtifactFromEnvelope should stamp tool_call_id on async placeholders and completed artifacts'
  );
});

test('Async query prompt guidance asks for brief description arg', () => {
  const source = sourceAt('server/internal/chat/optionc/prompts.go');

  assert.ok(
    source.includes('include a brief `description` argument (one sentence, max 300 characters)'),
    'Prompt guidance should ask models to include a concise description argument'
  );
});

test('useChat stamps streamed tool results with run-scoped metadata for keying', () => {
  const source = sourceAt('hooks/useChat.ts');

  assert.ok(
    source.includes('nextResult._uiRunId = runId;'),
    'useChat should stamp tool result envelopes with _uiRunId for scoped matching'
  );
  assert.ok(
    /nextResult\._uiStartedAt\s*=\s*typeof startedAt === ['"]number['"] \? startedAt : Date\.now\(\);/.test(source),
    'useChat should set a deterministic startedAt fallback even if tool_result arrives before tool_calls'
  );
  assert.ok(
    source.includes('if (!progressKey.startsWith(runKeyPrefix)) continue;'),
    'useChat should clean up all residual run-scoped progress keys at stream teardown'
  );
});
