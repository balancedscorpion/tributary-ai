import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

// Regression: async query placeholders whose completion was only observed via
// shared polling (missed SSE envelope, reload mid-query) left the explorer
// blank forever. App must hydrate the persisted result artifact through the
// existing ensureArtifactLoaded path when polling reports 'completed'.
test('completed query placeholders hydrate into result artifacts', () => {
  const hookSource = sourceAt('hooks/useQueryCompletionHydration.ts');
  const targetsSource = sourceAt('lib/asyncQueryPollTargets.ts');
  const appSource = sourceAt('App.tsx');

  assert.ok(
    appSource.includes('useQueryCompletionHydration({ artifactsById, ensureArtifactLoaded })'),
    'App should wire query completion hydration to the chat artifact map'
  );
  assert.ok(
    hookSource.includes('useSharedQueryStatuses'),
    'hydration must reuse the shared polling status path, not its own poll loop'
  );
  assert.ok(
    hookSource.includes('collectAsyncQueryPollTargets'),
    'hydration should derive its job set from the shared poll-target helper'
  );
  assert.ok(
    targetsSource.includes("meta.async_query !== true || meta.placeholder !== true"),
    'poll targets should only cover running async-query placeholder artifacts'
  );
  assert.ok(
    hookSource.includes("status !== 'completed'"),
    'hydration should trigger on the completed shared status'
  );
  assert.ok(
    hookSource.includes('!snapshot?.error'),
    'hydration should also retry artifact loading when status polling gives up'
  );
  assert.ok(
    hookSource.includes('MAX_HYDRATION_ATTEMPTS'),
    'hydration retries must be bounded (the result artifact can lag job completion)'
  );
});

// Regression: ensureArtifactLoaded force-stamped placeholder:false onto
// whatever the server returned. Fetching the persisted async placeholder
// mid-run then froze the inspector on an empty "completed" artifact and
// stopped completion hydration from retrying.
test('ensureArtifactLoaded preserves server-side placeholder state', () => {
  const chatSource = sourceAt('hooks/useChat.ts');
  assert.ok(
    chatSource.includes("loadedArtifact.metadata?.placeholder === true"),
    'loaded artifacts must be checked for their own placeholder flag'
  );
  assert.ok(
    chatSource.includes('placeholder: loadedIsPlaceholder'),
    'merge must keep placeholder:true when the server record is still a placeholder'
  );
  assert.ok(
    chatSource.includes('delete mergedMetadata.async_query'),
    'hydrated results must not retain the async_query running flag from the placeholder'
  );
});

// Regression: the hydrated full artifact can inherit the placeholder's nested
// query_metadata.status="running" during metadata merge. The artifact is real,
// but the inspector reads that stale nested status and pins the panel on
// polling unless completion wins before running hints.
test('hydrated query artifacts clear stale running metadata status', () => {
  const chatSource = sourceAt('hooks/useChat.ts');
  const stateSource = sourceAt('components/renderers/tool/query/useQueryInspectorState.ts');
  assert.ok(
    chatSource.includes('queryMetadata.status = "completed"'),
    'ensureArtifactLoaded should mark hydrated result query_metadata as completed'
  );
  assert.ok(
    stateSource.includes("if (hasCompletedResultsArtifact) return 'completed';"),
    'metadata status hints should prefer completed result artifacts over stale running metadata'
  );
  assert.ok(
    stateSource.includes("if (hasCompletedResultsArtifact || pollingStatus === 'completed') return 'completed';"),
    'artifact-only phase should complete before falling back to old running/submitted hints'
  );
});

// Regression: server-hydrated artifacts expose tool identity as top-level
// `tool_name`/`tool_call_id` fields, while the query renderer reads
// metadata. Without normalizing that shape the completed result could look
// like an anonymous data artifact and the original async handle kept the
// card/panel in polling state.
test('client normalizes hydrated artifact top-level identity into metadata', () => {
  const chatSource = sourceAt('hooks/useChat.ts');
  const stateSource = sourceAt('components/renderers/tool/query/useQueryInspectorState.ts');
  assert.ok(
    chatSource.includes('function normalizeArtifactForClient'),
    'useChat should normalize server artifact DTOs before storing them'
  );
  assert.ok(
    chatSource.includes('"tool_name"') && chatSource.includes('"tool_call_id"'),
    'normalization should preserve top-level tool identity in artifact metadata'
  );
  assert.ok(
    stateSource.includes('rawToolName') && stateSource.includes('meta.generated_by'),
    'query inspector should accept top-level tool_name and generated_by as completion hints'
  );
});

// Regression: a preview-only tool-result envelope can arrive after the full
// artifact has already hydrated (especially when polling materializes first).
// That preview must not clobber the full rows held in client state.
test('preview envelopes cannot overwrite hydrated full query artifacts', () => {
  const chatSource = sourceAt('hooks/useChat.ts');
  assert.ok(
    chatSource.includes('function artifactIsPreviewLike'),
    'useChat should classify placeholder/partial/preview artifacts centrally'
  );
  assert.ok(
    chatSource.includes('!artifactIsPreviewLike(existing)') &&
      chatSource.includes('artifactIsPreviewLike(artifact)'),
    'upsertArtifact should preserve an existing full artifact when a later preview arrives'
  );
  assert.ok(
    chatSource.includes('partial: false') && chatSource.includes('preview_only: false'),
    'the preserved full artifact should not keep preview flags from the later envelope'
  );
});

// Regression: the backend kept the submit-time placeholder record forever —
// InsertArtifact conflicted when get_query_results landed under the same
// deterministic id and the manager only logged "reusing existing artifact",
// so /api/artifacts/:id could never serve the completed rows.
test('backend upgrades conflicting query placeholder artifacts', () => {
  const managerSource = sourceAt('server/internal/chat/optionc/manager.go');
  const storeSource = sourceAt('server/internal/chat/store/store.go');
  const lifecycleSource = sourceAt('server/internal/chat/tools/query_lifecycle.go');
  assert.ok(
    managerSource.includes('upgradeQueryPlaceholderArtifact'),
    'insert conflicts must route through the placeholder upgrade path'
  );
  assert.ok(
    storeSource.includes('ReplaceArtifact(ctx context.Context, a Artifact) (Artifact, error)'),
    'the store contract must expose in-place artifact replacement'
  );
  assert.ok(
    managerSource.includes('metadata["status"] = string(jobs.JobStatusCompleted)') &&
      lifecycleSource.includes('queryMetadata["status"] = string(jobs.JobStatusCompleted)'),
    'completed query result artifacts should be self-describing as completed'
  );
});

// Regression: TaskBar derived totalRunning purely from artifact metadata via
// useQueryMonitor; live polling status only styled the rows. A completed job
// whose placeholder had not hydrated kept the header saying "running".
test('task bar running count consumes live terminal status', () => {
  const taskBarSource = sourceAt('components/layout/TaskBar.tsx');
  const monitorSource = sourceAt('hooks/useQueryMonitor.ts');
  assert.ok(
    taskBarSource.includes('useQueryMonitor(\n    artifactsById,\n    activeThreadId,\n    liveStatusByKey,\n  )'),
    'TaskBar must feed live shared statuses into the query monitor'
  );
  assert.ok(
    taskBarSource.includes('collectAsyncQueryPollTargets(artifactsById, activeThreadId)'),
    'TaskBar must subscribe from the artifacts map so live status can precede monitor output'
  );
  assert.ok(
    monitorSource.includes('liveStatusByKey?: Record<string, SharedQueryStatusSnapshot>'),
    'useQueryMonitor must accept live status snapshots'
  );
  assert.ok(
    monitorSource.includes('isTerminalStatus(liveStatus)'),
    'useQueryMonitor must transition tracked queries when live status is terminal'
  );
});

// Regression: a job the server no longer knows about (e.g. restart) made the
// shared status poller retry non-2xx responses forever.
test('shared status polling stops after bounded consecutive failures', () => {
  const sharedSource = sourceAt('hooks/useSharedQueryStatus.ts');
  assert.ok(
    sharedSource.includes('MAX_CONSECUTIVE_POLL_FAILURES'),
    'poll failures must be capped'
  );
  assert.ok(
    sharedSource.includes('recordEntryPollFailure'),
    'failure accounting must run on both non-2xx responses and network errors'
  );
  assert.ok(
    sharedSource.includes('entry.consecutiveFailures = 0'),
    'a successful poll must reset the failure counter'
  );
});

// Regression: when the inspector reached phase completed before the result
// artifact hydrated, it rendered a dead-end "Query completed." card with no
// rows and no indication that data was on the way.
test('inspector shows a loading state while completed results hydrate', () => {
  const inspectorSource = sourceAt('components/renderers/tool/query/QueryInspector.tsx');
  assert.ok(
    inspectorSource.includes('Query completed. Loading results…'),
    'completed-without-artifact branch should communicate that results are loading'
  );
  assert.ok(
    !/^\s*Query completed\.\s*$/m.test(inspectorSource),
    'the bare dead-end "Query completed." card should be gone'
  );
});

// Regression: the identity-reset effect reseeds pollingStatus from
// derivedInitialStatus when the artifact/tool-call/job identity changes (e.g.
// the placeholder artifact materialises a beat after the async handle). The
// shared snapshot object itself does not change in that render, so an effect
// keyed only on [sharedStatus] never re-applied an already-terminal status
// and the card stayed "running" forever.
test('inspector re-applies shared status after identity resets', () => {
  const stateSource = sourceAt('components/renderers/tool/query/useQueryInspectorState.ts');
  assert.ok(
    stateSource.includes('}, [sharedStatus, identityKey]);'),
    'shared-status application must re-run when the inspector identity changes'
  );
});

// Regression: the explorer should be able to attach to the full row source
// even when the full artifact id only appears in query_metadata or retrieval
// hints, and row totals should come from the same places.
test('query explorer resolves full row source from metadata fallbacks', () => {
  const explorerSource = sourceAt('components/renderers/tool/query/useQueryExplorer.ts');
  assert.ok(
    explorerSource.includes('qm.full_artifact_id'),
    'useQueryExplorer should read full_artifact_id from query_metadata'
  );
  assert.ok(
    explorerSource.includes('retrieval.artifact_id'),
    'useQueryExplorer should fall back to retrieval.artifact_id for row paging'
  );
  assert.ok(
    explorerSource.includes('qm.row_count_total'),
    'useQueryExplorer should use query_metadata row_count_total for pagination totals'
  );
});
