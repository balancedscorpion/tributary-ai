import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('tool activity disclosure pins live groups open and auto-collapses successes only', () => {
  const hook = sourceAt('components/chat/toolActivity/useToolActivityDisclosure.ts');

  assert.ok(
    hook.includes('const AUTO_COLLAPSE_DELAY_MS = 1500;'),
    'auto-collapse must linger 1.5s after completion'
  );
  assert.ok(
    hook.includes('if (hasFailureRef.current) return;'),
    'failed groups must never auto-collapse'
  );
  assert.ok(
    hook.includes('const isPinned = isRunning;'),
    'only live execution hard-pins the container open'
  );
  assert.ok(
    hook.includes('if (isRunning) return;'),
    'manual toggling must be a no-op while pinned'
  );
  assert.ok(
    hook.includes('collapseAll ? 0 : AUTO_COLLAPSE_DELAY_MS'),
    'collapse-all preference must reuse fire() so late failures still block collapse'
  );
});

test('aborted runs unpin the live container instead of sticking on Working', () => {
  const source = sourceAt('components/chat/CollapsedToolsSummary.tsx');
  assert.ok(
    source.includes('if (isThreadRunActive === false) return false;'),
    'a stopped/aborted thread run must not leave tool groups pinned open'
  );
  assert.ok(
    source.includes('focusRestoreArmedRef'),
    'focus restore on pill/expanded swap must be armed by explicit toggles only'
  );

  const messageList = sourceAt('components/chat/MessageList.tsx');
  assert.ok(
    messageList.includes('isThreadRunActive={isLoading}'),
    'MessageList must forward live-run state into assistant tool groups'
  );
});

test('tool failure counts use the canonical envelope outcome', () => {
  const outcome = sourceAt('components/chat/toolActivity/toolResultOutcome.ts');
  assert.ok(
    outcome.includes('return envelope?.ok === false;'),
    'the shared outcome classifier must treat only an explicit ok=false envelope as a failed tool'
  );
  assert.ok(
    !outcome.includes('result.error'),
    'nested payload errors must not override a successful tool envelope'
  );

  const summary = sourceAt('components/chat/CollapsedToolsSummary.tsx');
  const resultBlock = sourceAt('components/chat/ToolResultBlock.tsx');
  assert.ok(
    summary.includes('isToolResultFailure(env)') &&
      summary.includes('isToolResultFailure(resultInfo?.envelope)'),
    'group totals and expanded rows must use the shared classifier'
  );
  assert.ok(
    resultBlock.includes('const isError = isToolResultFailure(envelope);'),
    'the generic detail renderer must use the same classifier as the group summary'
  );
});

test('tool cards open the audit on click; inline preview is an explicit secondary toggle', () => {
  const source = sourceAt('components/chat/CollapsedToolsSummary.tsx');

  assert.ok(
    source.includes('onClick={bodyOpensAudit ? handleOpenAudit : () => togglePreview(previewKey)}'),
    'card body click must open the full audit (preview fallback only while no result exists)'
  );
  assert.ok(
    source.includes('aria-label="Toggle inline preview"'),
    'the inline preview must stay available behind an explicit chevron toggle'
  );
  assert.ok(
    ['group', 'relative', 'transition-colors', 'focus-within:ring-2', 'focus-within:ring-ring'].every((token) =>
      source.includes(token)
    ),
    'the whole card surface must highlight on hover/focus, not just inner rows'
  );
  assert.ok(
    source.includes('useToolActivityDisclosure({'),
    'group disclosure must run through the shared state machine'
  );
  assert.ok(
    source.includes("text-red-400") && source.includes('failed'),
    'minimized pill must surface failed counts'
  );
  assert.ok(
    source.includes('Working') && source.includes('of ${totalCount} done'),
    'live header must read Working with progress counts'
  );
  assert.ok(
    source.includes('`Ran ${totalCount} ${toolNoun}`'),
    'completed header/pill must read Ran N tools'
  );
});

test('QueryExecutionCard adopts the open-on-click + explicit-preview contract', () => {
  const source = sourceAt('components/chat/QueryExecutionCard.tsx');

  assert.ok(
    source.includes("aria-label={canOpen ? 'Open full audit' : undefined}"),
    'query card body must open the audit overlay, and must not advertise the action without a handler'
  );
  assert.ok(
    source.includes('aria-label="Toggle SQL preview"') &&
      source.includes('setIsPreviewOpen((prev) => !prev)'),
    'the SQL preview must live behind an explicit chevron toggle'
  );
  assert.ok(
    source.includes('focus-within:ring-2 focus-within:ring-ring'),
    'the whole query card must highlight on hover/focus'
  );
  assert.ok(
    source.includes('hasNonCollapsedSelectionWithin'),
    'text selection on the card must not trigger the open action'
  );
});

test('inspector opens as a bounded modal overlay in the app and the fixture', () => {
  const overlay = sourceAt('components/InspectorOverlay.tsx');
  assert.ok(
    overlay.includes('role="dialog"') &&
      overlay.includes('aria-modal="true"') &&
      overlay.includes('fixed inset-0') &&
      overlay.includes('data-testid="inspector-backdrop"') &&
      overlay.includes('max-w-screen-2xl'),
    'InspectorOverlay must be a bounded modal dialog over a backdrop'
  );
  assert.ok(
    overlay.includes("e.key === 'Escape'"),
    'Escape must close the overlay'
  );

  const app = sourceAt('App.tsx');
  assert.ok(app.includes('<InspectorOverlay'), 'App must host the inspector in the overlay');
  assert.ok(
    !app.includes('slide-in-from-left'),
    'the old side-panel hosting must not return in App'
  );

  const fixture = sourceAt('components/dev/AuditTrailFixture.tsx');
  assert.ok(fixture.includes('<InspectorOverlay'), 'the fixture must host the inspector in the overlay');
  assert.ok(
    !fixture.includes('<aside'),
    'the fixture must not keep a persistent aside inspector'
  );
});

test('tool-only thinking steps render bare tool cards, not the Working/Done wrapper', () => {
  const block = sourceAt('components/chat/IntermediateThinkingBlock.tsx');
  assert.ok(
    block.includes('function isToolOnlyStep('),
    'the block must detect tool-only steps'
  );
  assert.ok(
    !block.includes('_uiStreaming === true) return false'),
    'live (streaming) tool-only steps must render bare cards too — the Working/Done wrapper must never appear around running tool activity'
  );
  assert.ok(
    block.includes('segment.kind === "tools"'),
    'tool-only steps must render as direct tool-activity segments'
  );
  assert.ok(
    block.includes('toolSegmentIdentityKeys.has(step.messageIdentityKey)'),
    'block-level chart/html collectors must skip tool segments to avoid double rendering'
  );
});

test('mixed text and tool thinking steps split tools out of the dropdown wrapper', () => {
  const block = sourceAt('components/chat/IntermediateThinkingBlock.tsx');
  assert.ok(
    block.includes('function stripToolCallsFromStep('),
    'mixed text+tool steps need a text-only clone so StepRow cannot re-render tool cards inside its disclosure'
  );
  assert.ok(
    block.includes('if (!toolOnly) {') &&
      block.includes('pushStackSegment(stripToolCallsFromStep(step), visibleIndex);'),
    'mixed steps must keep their text in the thinking stack without retaining tool_calls'
  );
  assert.ok(
    block.includes('pushToolSegment(step, visibleIndex, stamped);'),
    'all tool calls from mixed steps must be promoted to the bare tool-card segment'
  );
  assert.ok(
    block.includes('Tool calls render the new disclosure cards directly') &&
      block.includes('even when the same') &&
      block.includes('stream step also carries explanatory text'),
    'the source comment should document the exact regression this guards against'
  );
});

test('failures stay loud when collapsed: exact counts and red card accents', () => {
  const summary = sourceAt('components/chat/CollapsedToolsSummary.tsx');
  assert.ok(
    summary.includes('`${failedCount} of ${totalCount} failed`'),
    'failure pill copy must carry the exact denominator, e.g. "1 of 2 failed"'
  );
  assert.ok(
    !summary.includes('`${failedCount} failed`'),
    'denominator-less "<n> failed" copy must not return anywhere in the group UI'
  );
  assert.ok(
    summary.includes('ring-destructive/40') && summary.includes('bg-destructive/5'),
    'failed tool cards must carry the red accent on the card surface itself'
  );

  const queryCard = sourceAt('components/chat/QueryExecutionCard.tsx');
  assert.ok(
    queryCard.includes('border-destructive/40') && queryCard.includes('bg-destructive/5'),
    'failed query cards must share the same red card accent as activity cards'
  );
});

test('tool activity preferences persist per thread and previews fall back generically', () => {
  const prefs = sourceAt('lib/toolActivityPrefs.ts');
  assert.ok(
    prefs.includes("'ai_analyst_tool_activity_collapse_all_v1'"),
    'collapse-all preference must persist under a stable storage key'
  );
  assert.ok(
    prefs.includes('useSyncExternalStore'),
    'preference reads must subscribe so every group updates on toggle'
  );

  const registry = sourceAt('components/chat/toolActivity/inlinePreviewRegistry.tsx');
  assert.ok(
    registry.includes('return GenericInlinePreview;'),
    'inline preview registry must fall back to the generic renderer'
  );
});

test('GitHub connection widget renders nested CLI-auth payloads as signed in', () => {
  const widget = sourceAt('components/renderers/tool/github/widgets.tsx');
  assert.ok(
    widget.includes('const nestedStatus = isObject(outerStatus.status) ? outerStatus.status : {};'),
    'connection status must unwrap real github_check_connection payloads nested under status.status'
  );
  assert.ok(
    widget.includes('pickString(status.login, account.login, status.username, account.name)'),
    'CLI-auth payloads with a top-level login must not render as "Not signed in"'
  );
  assert.ok(
    widget.includes('status.lastCheckOk === true') &&
      widget.includes('Boolean(login)') &&
      widget.includes('status.read_tools_enabled'),
    'connection status must use last-check/read-tool fields supplied by the backend without treating ok:true alone as signed-in'
  );

  const fixture = sourceAt('components/dev/AuditTrailFixture.tsx');
  assert.ok(
    fixture.includes('client_id_configured: false') &&
      fixture.includes('GitHub CLI authentication is usable.'),
    'the dev fixture must cover the real CLI-auth payload shape'
  );
});

test('collapse-all is a single thread-level control that collapses open groups', () => {
  const toggle = sourceAt('components/chat/toolActivity/CollapseAllToggle.tsx');
  assert.ok(
    toggle.includes("'Collapse all tool activity'") &&
      toggle.includes('aria-pressed={collapseAll}'),
    'thread-level toggle must carry the exact product label and pressed semantics'
  );

  const app = sourceAt('App.tsx');
  assert.ok(
    app.includes('<CollapseAllToggle'),
    'the toggle must render in thread-level chrome (App chat panel header)'
  );

  const summary = sourceAt('components/chat/CollapsedToolsSummary.tsx');
  assert.ok(
    !summary.includes('ListCollapse'),
    'the per-group collapse-all icon must not return — the control is thread-level'
  );

  const hook = sourceAt('components/chat/toolActivity/useToolActivityDisclosure.ts');
  assert.ok(
    hook.includes('manualOpenByKey.delete(normalizedKey)'),
    'enabling collapse-all must drop per-group manual-open state so open groups collapse now'
  );
});

test('contiguous tool-only steps merge into one growing group with per-call identity', () => {
  const block = sourceAt('components/chat/IntermediateThinkingBlock.tsx');
  assert.ok(
    block.includes('last.kind === "tools"') && block.includes('last.toolCalls.push(...stamped)'),
    'contiguous tool-only steps must merge into one group ("Ran 1 tool" grows to "Ran 2 tools")'
  );
  assert.ok(
    block.includes('_uiSourceMessageIdentityKey'),
    'merged tool calls must carry their source message identity'
  );

  const summary = sourceAt('components/chat/CollapsedToolsSummary.tsx');
  assert.ok(
    summary.includes('getCallMessageIdentityKey'),
    'SQL scoped-key lookups must resolve via each call source identity, not only the group key'
  );
});

test('expanded tool activity sheds redundant minimize/maximize affordances', () => {
  const summary = sourceAt('components/chat/CollapsedToolsSummary.tsx');
  assert.ok(
    !summary.includes('aria-label="Minimize tool activity"'),
    'the top-right minimize icon button must not return — the header arrow minimises'
  );
  assert.ok(
    !summary.includes('Maximize2'),
    'the per-card maximize icon must not return on tool cards'
  );
  assert.ok(
    summary.includes('motion-safe:slide-in-from-top-1'),
    'pill-to-expanded must use the eased animate-in transition'
  );
  assert.ok(
    summary.includes('flex w-full items-center gap-2 rounded-lg border border-border bg-muted/20'),
    'the minimized pill must span the same width as the expanded container (no w-fit jump)'
  );

  const queryCard = sourceAt('components/chat/QueryExecutionCard.tsx');
  assert.ok(
    !queryCard.includes('Maximize2'),
    'the maximize icon must not return on query cards'
  );
});

test('rich tool previews default to rendered widgets; run-status label cannot jitter', () => {
  const registry = sourceAt('components/chat/toolActivity/inlinePreviewRegistry.tsx');
  assert.ok(
    registry.includes('WidgetForKey') &&
      registry.includes("useState<'rendered' | 'json'>('rendered')"),
    'tools with inspector widgets must preview as Rendered by default with JSON one tab away'
  );

  const app = sourceAt('App.tsx');
  assert.ok(
    app.includes('w-[260px] truncate text-left tabular-nums'),
    'the run-status label must sit in a fixed-width slot so phase changes cannot move it'
  );
});

test('durations under a millisecond render as <1ms in both shared formatters', () => {
  const summary = sourceAt('components/chat/CollapsedToolsSummary.tsx');
  const atoms = sourceAt('components/renderers/tool/query/atoms.tsx');
  assert.ok(summary.includes('if (ms < 1) return "<1ms";'), 'group formatter handles <1ms');
  assert.ok(atoms.includes("if (ms < 1) return '<1ms';"), 'inspector formatter handles <1ms');
});

test('GitHub issue widgets unwrap backend IssueList/IssueResult envelopes', () => {
  const widget = sourceAt('components/renderers/tool/github/widgets.tsx');

  assert.ok(
    widget.includes('const listPayload = isObject(r.issues) ? r.issues : r;'),
    'issue list must unwrap the backend IssueList object nested under `issues` instead of treating it as an empty array'
  );
  assert.ok(
    widget.includes('repoFromPath(listPayload.repository)'),
    'issue list must derive the repository label from the nested ProjectRepository before falling back to args'
  );
  assert.ok(
    widget.includes("typeof listPayload.total_count === 'number'") &&
      widget.includes('listPayload.truncated === true') &&
      widget.includes('truncated at'),
    'issue list must surface total_count/limit/truncated when the backend reports them'
  );
  assert.ok(
    widget.includes('const issue = isObject(container.issue) ? container.issue : container;'),
    'issue detail must unwrap the backend IssueResult ({repository, issue}) nested under `issue` instead of rendering Unknown'
  );
  assert.ok(
    widget.includes('repoFromPath(container.repository)'),
    'issue detail must derive the repository label from the nested ProjectRepository before falling back to args'
  );

  const fixture = sourceAt('components/dev/AuditTrailFixture.tsx');
  assert.ok(
    fixture.includes('githubcli.IssueList') && fixture.includes('githubcli.IssueResult'),
    'the dev fixture must cover the backend-shaped nested issue envelopes'
  );
  assert.ok(
    fixture.includes('total_count: 2') && fixture.includes('truncated: true'),
    'the dev fixture issue list must exercise the live truncated-at-limit metadata path'
  );
});

test('Jira issue renderer unwraps live payloads and renders ADF descriptions', () => {
  const card = sourceAt('components/renderers/tool/jira/IssueCard.tsx');
  assert.ok(
    card.includes('normalizeJiraIssue(result.issue)'),
    'the issue card must unwrap the live bridge shape nested under `result.issue`'
  );
  assert.ok(
    card.includes('normalizeJiraIssue(result)'),
    'legacy flat issue results must keep rendering (old threads/fixtures)'
  );
  assert.ok(
    card.includes("typeof raw.commentsCount === 'number'") &&
      card.includes('namedString(raw.issueType) || namedString(raw.issuetype)'),
    'normalization must accept both live summarized fields and legacy flat aliases'
  );
  assert.ok(
    card.includes('isAdfDoc(raw.description)') && card.includes('<AdfContent doc={issue.description} />'),
    'ADF descriptions must render through the ADF renderer, not as raw JSON or flattened text'
  );
  assert.ok(
    card.includes('issue.descriptionText') && card.includes('whitespace-pre-wrap'),
    'plain-text description previews must remain the fallback when no ADF doc is present'
  );

  const adf = sourceAt('components/renderers/tool/jira/adf.tsx');
  for (const nodeType of ["'bulletList'", "'orderedList'", "'codeBlock'", "'panel'", "'mention'", "'status'", "'taskItem'", "'table'"]) {
    assert.ok(adf.includes(`case ${nodeType}:`), `ADF renderer must handle ${nodeType} nodes`);
  }
  assert.ok(
    adf.includes('// Unknown node: render children if any, else fall back to its text.'),
    'unknown ADF nodes must degrade to children/plain text, never raw JSON'
  );
  assert.ok(
    adf.includes("/^https?:\\/\\//i.test(url)"),
    'ADF links must be restricted to http(s) URLs'
  );

  const chat = sourceAt('components/chat/AtlassianToolResult.tsx');
  assert.ok(
    chat.includes('extractJiraIssueFromResult(result)') && chat.includes('<JiraIssueCard issue={single} />'),
    'the chat Jira widget must render single issues through the dedicated card'
  );

  const registry = sourceAt('components/chat/toolActivity/inlinePreviewRegistry.tsx');
  assert.ok(
    registry.includes("String(toolCall.name || '') === 'jira_get_issue'") &&
      registry.includes('JiraIssueCard'),
    'jira_get_issue must register a rich inline preview rendering the issue card'
  );

  const inspector = sourceAt('components/renderers/tool/inspector/ToolInspector.tsx');
  assert.ok(
    inspector.includes('resolveInspectorResponseWidget(meta, result)') &&
      inspector.includes('<ResponseWidget result={result} meta={meta} />'),
    'the generic inspector must consult the response-widget registry for its Response zone'
  );
  assert.ok(
    inspector.includes('!ResponseWidget && primary.rest'),
    'when a response widget claims the zone, the auto-derived rest grid must not re-dump payload fields'
  );
  assert.ok(
    inspector.includes('<Disclosure label="Raw">'),
    'the Raw disclosure must stay available in the inspector regardless of the rendered view'
  );

  const inspectorResponse = sourceAt('components/renderers/tool/jira/JiraIssueResponse.tsx');
  assert.ok(
    inspectorResponse.includes('registerInspectorResponseWidget({') &&
      inspectorResponse.includes("String(meta.tool_name || '') === 'jira_get_issue'") &&
      inspectorResponse.includes('extractJiraIssueFromResult(result) !== null'),
    'jira_get_issue must register an inspector response widget that only claims extractable issues'
  );
  assert.ok(
    inspectorResponse.includes('<JiraIssueCard issue={issue} bordered />'),
    'the audit overlay must render the same first-class Jira issue card (ADF description included)'
  );

  const rendererManifest = sourceAt('components/renderers/index.ts');
  assert.ok(
    rendererManifest.includes("import './tool/jira/JiraIssueResponse';"),
    'the Jira response widget registration must be imported in the renderer manifest'
  );

  const bridge = sourceAt('server/internal/atlassian/bridge.mjs');
  assert.ok(
    bridge.includes('summary,description,status,issuetype,priority,') &&
      bridge.includes(',comment,customfield_10020'),
    'jira_get_issue default fields must request description, priority, and comment counts'
  );
  assert.ok(
    bridge.includes('description: safeAdfDocument(fields.description)') &&
      bridge.includes('descriptionText: truncateText(adfToPlainText(asObject(fields.description)), 2000)'),
    'summarizeJiraIssue must emit a size-capped ADF description plus a plain-text preview'
  );
  assert.ok(
    bridge.includes('priority: asObject(fields.priority).name') &&
      bridge.includes('commentsCount: Number.isFinite(commentTotal)'),
    'summarizeJiraIssue must emit priority and commentsCount for the card'
  );

  const fixture = sourceAt('components/dev/AuditTrailFixture.tsx');
  assert.ok(
    fixture.includes("result_metadata: { integration: 'atlassian', product: 'jira', widget: 'issue' }"),
    'the dev fixture must carry the live atlassian result_metadata for jira_get_issue'
  );
  assert.ok(
    fixture.includes("panelType: 'warning'") && fixture.includes('descriptionText:'),
    'the dev fixture issue must exercise ADF description rendering (marks, lists, panel) plus the text preview'
  );
});

test('GitHub status and repository widgets render backend payload fields', () => {
  const widget = sourceAt('components/renderers/tool/github/widgets.tsx');

  assert.ok(
    widget.includes("'read_tools_enabled'") &&
      widget.includes("'allowed_repositories'") &&
      widget.includes("'lastCheckMessage'"),
    'github_status must identify backend StatusResult payloads by their real keys'
  );
  assert.ok(
    widget.includes("label: 'Configured'") &&
      widget.includes("label: 'Last checked'") &&
      widget.includes('splitTokens(status.scope)'),
    'backend status rendering must expose configured state, last-check telemetry, and scope strings'
  );
  assert.ok(
    widget.includes('function repositoryOwner(') &&
      widget.includes('function repositoryLocalPath(') &&
      widget.includes('function repositoryRemoteUrl('),
    'repository lists must normalize owner, repo_path, and remote_url instead of assuming API rows'
  );
  assert.ok(
    widget.includes('hasOwner') &&
      widget.includes('hasLocalPath') &&
      widget.includes('hasRemote') &&
      widget.includes('hasVisibility') &&
      widget.includes('hasUpdated'),
    'repository table columns must be driven by fields actually present in the payload'
  );
  assert.ok(
    widget.includes('const owner = repositoryOwner(value);') &&
      widget.includes('if (owner && repo) return `${owner}/${repo}`;'),
    'shared repository labels must support backend ProjectRepository objects'
  );

  const fixture = sourceAt('components/dev/AuditTrailFixture.tsx');
  assert.ok(
    fixture.includes('repo_path: "Demo-Benchmark"') &&
      fixture.includes('remote_url: "https://github.com/example-lab/demo-benchmark"') &&
      fixture.includes('resolvable: true') &&
      fixture.includes('allowed_repositories: ["example-lab/demo-benchmark"]'),
    'the dev fixture must cover the live ProjectRepository and StatusResult shapes'
  );
});

test('the thinking-header shimmer can never translate its text across the screen', () => {
  const css = sourceAt('globals.css');

  // @keyframes share one global namespace. tailwind.config.ts owns the name
  // `shimmer` for the TRANSLATING progress-bar sweep; when globals.css also
  // defined `shimmer` (background-position), whichever stylesheet/chunk
  // loaded last hijacked the other — translating the whole header row
  // (spinner + "Working" + title) across the screen.
  assert.ok(
    !/@keyframes\s+shimmer\b/.test(css),
    'globals.css must not (re)define the global `shimmer` keyframes — the name belongs to the tailwind translateX progress sweep'
  );
  assert.ok(
    css.includes('animation: thinking-shimmer-sweep') &&
      /@keyframes\s+thinking-shimmer-sweep\b/.test(css),
    'the thinking header must animate via its own uniquely named keyframes'
  );
  const sweepBlock = css.slice(css.indexOf('@keyframes thinking-shimmer-sweep'));
  const sweepBody = sweepBlock.slice(0, sweepBlock.indexOf('\n}') + 2);
  assert.ok(
    sweepBody.includes('background-position') &&
      !/transform|translate/i.test(sweepBody),
    'the header sweep may only move the background gradient — never a transform that would carry the text'
  );
  assert.ok(
    /@media \(prefers-reduced-motion: reduce\)[\s\S]*?\.thinking-shimmer[\s\S]*?animation: none/.test(css),
    'the header shimmer must respect prefers-reduced-motion'
  );

  const tailwind = sourceAt('tailwind.config.ts');
  assert.ok(
    /"shimmer":\s*\{[\s\S]*?translateX/.test(tailwind),
    'the tailwind `shimmer` keyframes (text-free progress-bar sweep) must keep translating'
  );

  // The status label sits in a fixed right-aligned slot: Working/Running →
  // Done may only move the label's own left edge, never reflow the row, and
  // only the spinner/dot animates — the text is never inside a transform.
  const block = sourceAt('components/chat/IntermediateThinkingBlock.tsx');
  for (const label of ['Working', 'Running', 'Done']) {
    assert.ok(
      block.includes(`min-w-[60px] shrink-0 items-center justify-end`),
      `the ${label} status label must live in a stable min-width, right-anchored slot`
    );
  }
  assert.ok(
    block.includes('motion-safe:animate-spin') &&
      !/<Loader2 size=\{10\} className="animate-spin"/.test(block),
    'the thinking-header spinner must use the motion-safe variant'
  );
});
