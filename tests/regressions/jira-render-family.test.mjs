import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

// Every live Jira result_metadata.widget the Atlassian bridge / Go tool layer
// can emit (server/internal/atlassian/bridge.mjs +
// server/internal/chat/tools/atlassian_tools.go).
const JIRA_WIDGET_KEYS = [
  'status',
  'connection',
  'issue',
  'history',
  'comment_list',
  'attachment_list',
  'attachment',
  'issue_link_types',
  'user_list',
  'issue_list',
  'find',
  'rest',
  'approval',
  'write_result',
];

const JIRA_READ_TOOLS = [
  'atlassian_status',
  'atlassian_check_connection',
  'jira_get_issue',
  'jira_get_issue_history',
  'jira_get_issue_comments',
  'jira_get_issue_attachments',
  'jira_read_attachment',
  'jira_get_issue_link_types',
  'jira_find_users',
  'jira_search_issues',
  'jira_find',
  'jira_rest_get',
];

const JIRA_WRITE_TOOLS = [
  'add_comment',
  'reply_to_comment',
  'add_related_tickets',
  'upload_attachment',
  'update_issue',
  'create_issue',
];

test('the Jira widget family covers every live widget key and tool name', () => {
  const widgets = sourceAt('components/renderers/tool/jira/widgets.tsx');
  const summary = sourceAt('components/renderers/tool/jira/summary.ts');

  for (const key of JIRA_WIDGET_KEYS) {
    assert.ok(
      summary.includes(`'${key}'`),
      `summary.ts must know the '${key}' widget key`
    );
    assert.ok(
      widgets.includes(`case '${key}':`),
      `JiraWidgetForKey must dispatch a rendered view for '${key}'`
    );
  }

  for (const tool of JIRA_READ_TOOLS) {
    assert.ok(
      summary.includes(`/^${tool}$/`),
      `resolveJiraWidgetKey must map ${tool} by tool name (widget hints can be absent on old envelopes)`
    );
  }
  assert.ok(
    summary.includes(
      '/^jira_(add_comment|reply_to_comment|add_related_tickets|upload_attachment|update_issue|create_issue)_execute$/'
    ),
    'every *_execute write tool must resolve to the write_result widget'
  );
  assert.ok(
    summary.includes(
      '/^jira_(add_comment|reply_to_comment|add_related_tickets|upload_attachment|update_issue|create_issue)$/'
    ),
    'every write tool must resolve to the approval widget pre-execution'
  );
  assert.ok(
    widgets.includes("export { resolveJiraWidgetKey } from './summary';"),
    'widgets.tsx must re-export the shared resolver so widget consumers keep one import surface'
  );
});

test('Jira widgets render content, not raw JSON, in the rendered zone', () => {
  const widgets = sourceAt('components/renderers/tool/jira/widgets.tsx');

  // Approval bodies are markdown-sourced and must render as markdown.
  assert.ok(
    widgets.includes('<MarkdownRenderer content={markdownBody} />'),
    'approval comment/description bodies must render through MarkdownRenderer'
  );
  assert.ok(
    widgets.includes('markdown → Jira ADF on submit'),
    'approvals must disclose the markdown→ADF conversion'
  );
  // Binary/native-image attachment outcomes are normal, never error-toned.
  assert.ok(
    widgets.includes('Binary / native-image outcomes are normal results, never error-toned.'),
    'attachment notes must stay neutral-toned'
  );
  // Per-link failures in write results keep the loud red error text rule.
  assert.ok(
    widgets.includes('text-red-400'),
    'partial write failures must surface red-400 error text'
  );
  // accountIds are the payload of user search — keep a copy affordance.
  assert.ok(
    widgets.includes('CopyableId'),
    'user rows must keep account IDs copyable'
  );
  assert.ok(
    widgets.includes('CopyableCodeBlock') && widgets.includes('Jira JQL search'),
    'jira_search_issues must render JQL as a first-class code/query block'
  );
  assert.ok(
    widgets.includes('GET {path}') && widgets.includes("'/rest/api/3/search/jql'"),
    'jira_search_issues must surface the Jira REST endpoint/path in the rendered response'
  );
  assert.ok(
    widgets.includes('JiraSearchIssueRows') && widgets.includes('commentsCount'),
    'jira_search_issues must render rich issue search rows, not only generic JSON/list rows'
  );
  // The dedicated issue card stays the single source of truth for `issue`.
  assert.ok(
    widgets.includes('<JiraIssueInspectorResponse result={raw} meta={meta} />'),
    'the issue widget must reuse the dedicated jira_get_issue response (card + changelog)'
  );
});

test('the inspector response registry covers the whole Jira family with rendered defaults', () => {
  const responses = sourceAt('components/renderers/tool/jira/JiraResponses.tsx');
  assert.ok(
    responses.includes('registerInspectorResponseWidget({'),
    'JiraResponses must register an inspector response widget'
  );
  assert.ok(
    responses.includes("/^(jira|atlassian)_/.test(toolName) || product === 'jira' || product === 'atlassian'"),
    'the matcher must claim jira_*/atlassian_* tools and jira/atlassian products'
  );
  assert.ok(
    responses.includes("key !== 'generic' && key !== 'issue'"),
    'unknown payloads must keep the structured fallback; issue stays with JiraIssueResponse'
  );

  const manifest = sourceAt('components/renderers/index.ts');
  const issueImport = manifest.indexOf("import './tool/jira/JiraIssueResponse';");
  const familyImport = manifest.indexOf("import './tool/jira/JiraResponses';");
  assert.ok(issueImport !== -1, 'manifest must import the dedicated issue response');
  assert.ok(familyImport !== -1, 'manifest must import the Jira family responses');
  assert.ok(
    issueImport < familyImport,
    'the dedicated issue widget must register before the family catch-all'
  );

  // Rendered widget stays the default; Raw stays one disclosure away.
  const inspector = sourceAt('components/renderers/tool/inspector/ToolInspector.tsx');
  const widgetRender = inspector.indexOf('<ResponseWidget result={result} meta={meta} />');
  const rawDisclosure = inspector.indexOf('<Disclosure label="Raw">');
  assert.ok(widgetRender !== -1 && rawDisclosure !== -1, 'inspector must render widget + Raw');
  assert.ok(
    widgetRender < rawDisclosure,
    'the rendered response must come before the Raw fallback'
  );
});

test('inline previews default to the rendered Jira widget for all Jira tools', () => {
  const registry = sourceAt('components/chat/toolActivity/inlinePreviewRegistry.tsx');

  assert.ok(
    registry.includes("/^(jira|atlassian)_/.test(String(toolCall.name || ''))"),
    'the Jira family preview must claim every jira_*/atlassian_* tool'
  );
  assert.ok(
    registry.includes('JiraWidgetForKey') && registry.includes('resolveJiraWidgetKey'),
    'Jira previews must render through the shared widget family'
  );
  assert.ok(
    registry.includes('inline: true'),
    'inline Jira previews must mark card-scale rendering so jira_search_issues can use its compact preview'
  );
  assert.ok(
    registry.includes("widgetKey === 'generic' || widgetKey === 'approval'"),
    'approval flows stay with the chat approval card; unknown payloads fall back generically'
  );

  // The family preview must register before the generic search preview so
  // jira_search_issues / jira_find render real widgets, not link lists.
  const familyRegistration = registry.indexOf('component: JiraWidgetInlinePreview');
  const searchRegistration = registry.indexOf('component: SearchInlinePreview');
  assert.ok(familyRegistration !== -1 && searchRegistration !== -1);
  assert.ok(
    familyRegistration < searchRegistration,
    'the Jira widget preview must win over the generic search preview'
  );

  // Rendered stays the default tab with JSON one explicit tab away.
  assert.ok(
    registry.includes("useState<'rendered' | 'json'>('rendered')"),
    'rendered must remain the default inline preview tab'
  );
});

test('the dev audit fixture covers every Jira widget case', () => {
  const fixture = sourceAt('components/dev/AuditTrailFixture.tsx');

  const expectedFixtureTools = [
    "name: 'atlassian_status'",
    "name: 'atlassian_check_connection'",
    "name: 'jira_get_issue'",
    "name: 'jira_get_issue_history'",
    "name: 'jira_get_issue_comments'",
    "name: 'jira_get_issue_attachments'",
    "name: 'jira_read_attachment'",
    "name: 'jira_get_issue_link_types'",
    "name: 'jira_find_users'",
    "name: 'jira_search_issues'",
    "name: 'jira_find'",
    "name: 'jira_rest_get'",
    "name: 'jira_create_issue'",
    "name: 'jira_update_issue'",
    "name: 'jira_add_comment_execute'",
    "name: 'jira_create_issue_execute'",
    "name: 'jira_add_related_tickets_execute'",
  ];
  for (const expected of expectedFixtureTools) {
    assert.ok(fixture.includes(expected), `fixture must cover ${expected}`);
  }

  for (const widget of JIRA_WIDGET_KEYS) {
    assert.ok(
      fixture.includes(`widget: '${widget}'`),
      `fixture must carry result_metadata widget '${widget}'`
    );
  }

  // Edge states the fixtures must keep exercising.
  assert.ok(
    fixture.includes('parentCommentId') && fixture.includes('updateAuthor'),
    'comment fixtures must exercise replies and edited comments'
  );
  assert.ok(
    fixture.includes('native_image_input: true') && fixture.includes('truncated: true'),
    'attachment fixtures must exercise native-image and truncated-text states'
  );
  assert.ok(
    fixture.includes("ok: false,\n            status: 404,"),
    'the related-tickets fixture must keep a partially failed link'
  );
});

test('write tools resolve approval vs executed result distinctly', () => {
  const widgets = sourceAt('components/renderers/tool/jira/widgets.tsx');
  const summary = sourceAt('components/renderers/tool/jira/summary.ts');
  for (const tool of JIRA_WRITE_TOOLS) {
    assert.ok(
      summary.includes(tool),
      `write tool jira_${tool} must appear in the approval/write_result resolvers`
    );
  }
  assert.ok(
    widgets.includes("label=\"awaiting approval\""),
    'approvals must read awaiting approval (amber), not success'
  );
  assert.ok(
    summary.includes('of ${links.length} links created'),
    'partial related-link results must carry the exact denominator'
  );
  assert.ok(
    widgets.includes('jiraWriteHeadline(r, toolName)'),
    'the write-result widget must render the same shared headline as the collapsed card'
  );
});

test('collapsed Jira cards get scannable one-line summaries, never "0 errors"', () => {
  const summary = sourceAt('components/renderers/tool/jira/summary.ts');
  const resultSummary = sourceAt('components/chat/toolActivity/resultSummary.ts');

  // Every widget key has a dedicated summary branch.
  for (const key of JIRA_WIDGET_KEYS) {
    assert.ok(
      summary.includes(`case '${key}':`) || key === 'generic',
      `getJiraToolResultSummary must summarize the '${key}' widget`
    );
  }

  // The copy stays user-scannable, mirroring the rendered widgets.
  for (const fragment of [
    'sprint transition',
    "'comment'",
    "'attachment'",
    "'link type'",
    "'user'",
    "'issue'",
    'awaiting approval',
    '`GET ${path',
    'image input',
    'truncated',
    'Bridge running',
  ]) {
    assert.ok(
      summary.includes(fragment),
      `Jira summaries must include the ${fragment} copy`
    );
  }

  // Widget strings like 'status'/'issue_list' exist in other families —
  // the summary must gate on Jira/Atlassian identity, not the key alone.
  assert.ok(
    summary.includes('if (!isJiraFamilyMeta(meta)) return '),
    'getJiraToolResultSummary must only claim Jira/Atlassian envelopes'
  );

  // The card path consults the Jira helper BEFORE the generic list scan.
  const jiraCall = resultSummary.indexOf('getJiraToolResultSummary(');
  const listScan = resultSummary.indexOf('LIST_FIELDS_PRIORITY];');
  assert.ok(jiraCall !== -1, 'resultSummary must call the Jira family summary');
  assert.ok(
    jiraCall < listScan,
    'the Jira summary must run before generic list-field summarization'
  );
  assert.ok(
    resultSummary.includes("if (envelope.ok !== false) {"),
    'failed calls must keep the diagnostic headline, not a Jira success line'
  );

  // Generic hardening: auto-discovered diagnostics/empty arrays can never
  // become the summary ("0 errors" on a success is noise) — for any tool.
  assert.ok(
    resultSummary.includes('/^(errors|warnings)$/i.test(key)') &&
      resultSummary.includes('if (result[key].length === 0) continue;'),
    'generic list summarization must skip errors/warnings and empty auto-discovered arrays'
  );
});
