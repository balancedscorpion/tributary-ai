import React, { useMemo, useState } from 'react';
import { TooltipProvider } from '@/components/ui/tooltip';
import { Button } from '@/components/ui/button';
import ApprovalCard from '@/components/approvals/ApprovalCard';
import PendingApprovalsStrip from '@/components/approvals/PendingApprovalsStrip';
import PermissionsTab from '@/components/settings/PermissionsTab';
import { publishApprovalEvent } from '@/hooks/useApprovals';
import {
  staticProviderHostedToolCapabilitiesForFixture,
  staticToolMetadataForFixture,
} from '@/lib/toolInventory';
import type { ApprovalRequestUI } from '@/types/permissions';
import type { AppSettings } from '@/types';

// Enough settings shape for the Permissions tab's connection/tool inventory:
// GitHub, BigQuery, Local Computer and provider-hosted tool connections render
// as ready; Slack + arXiv render as needs-setup (locked).
const FIXTURE_SETTINGS = {
  connections: {
    visibleIds: [
      'github',
      'slack',
      'bigquery',
      'local-computer',
      'arxiv',
      'web-search',
      'python-sandbox',
    ],
  },
  providerConfigs: [
    {
      id: 'fixture-openai',
      displayName: 'Fixture OpenAI',
      providerId: 'openai',
      apiType: 'responses',
      model: 'gpt-5',
      capabilityControls: {
        hosted_web_search: 'on',
        hosted_code_execution: 'on',
      },
      webSearch: { enabled: true },
      codeExecution: { enabled: true },
    },
  ],
  github: { enabled: true },
  slack: { enabled: false },
  dataSources: { bigquery: { enabled: true } },
  localComputer: { enabled: true, accessMode: 'whole-computer', selectedFolders: [] },
  arxiv: { enabled: false },
} as unknown as AppSettings;

// Visual QA fixture for the persistent-approvals UI: /dev/approvals
// Every card state renders without the backend; decisions resolve locally
// (fake onDecide) so focus management and busy/error states are testable.

const now = new Date();
const minutesAgo = (m: number) => new Date(now.getTime() - m * 60_000).toISOString();

function approval(partial: Partial<ApprovalRequestUI> & { id: string }): ApprovalRequestUI {
  return {
    threadId: 'thread-fixture',
    runId: 'run-fixture',
    tool: 'jira_create_issue',
    category: 'write',
    subject: { kind: 'tool', tool: 'jira_create_issue', connector: 'jira' },
    summary: '',
    preview: {},
    options: ['once', 'thread', 'always'],
    status: 'pending',
    createdAt: minutesAgo(1),
    ...partial,
  } as ApprovalRequestUI;
}

const FIXTURES: Array<{ label: string; approval: ApprovalRequestUI; failDecide?: boolean }> = [
  {
    label: 'Pending write — Jira issue, full preview, all scopes',
    approval: approval({
      id: 'apr-jira-create',
      preview: {
        issue_key: 'PROJ-142',
        summary: 'Compaction stalls when tool results exceed budget',
        description:
          'Streaming runs stall when **tool-result compaction** exceeds the evidence budget.\n\n- Repro: run `aggregate_csv` on a 2 GB file\n- Expected: bounded summary',
      },
    }),
  },
  {
    label: 'Pending read — local folder access (picker chooses location)',
    approval: approval({
      id: 'apr-local-docs',
      tool: 'file_search',
      category: 'read',
      subject: {
        kind: 'local_location',
        local_kind: 'folder',
        excludes: ['.env*', '*.pem', 'id_*'],
      },
      preview: {
        query: 'Q3 forecast',
        location_hint: 'Documents',
        file_types: ['presentation', 'spreadsheet'],
      },
      options: ['once', 'thread', 'always'],
    }),
  },
  {
    label: 'Pending execute — GitHub clone, single scope (plain button)',
    approval: approval({
      id: 'apr-gh-clone',
      tool: 'github_clone_repository',
      category: 'execute',
      subject: { kind: 'repository', repository: 'acme/site', connector: 'github' },
      preview: { command: 'gh repo clone acme/site -- --depth 1' },
      options: ['once'],
    }),
  },
  {
    label: 'Pending execute — BigQuery scan budget (one-time cost approval)',
    approval: approval({
      id: 'apr-bq-scan-budget',
      tool: 'run_bigquery_query',
      category: 'execute',
      operation: 'query_scan_budget',
      subject: {
        kind: 'tool',
        tool: 'run_bigquery_query',
        connector: 'bigquery',
        operation: 'query_scan_budget',
      },
      preview: {
        estimated_bytes: 142_318_642_339, // ~132.5 GiB
        automatic_scan_bytes: 107_374_182_400, // 100 GiB
        authorized_scan_bytes: 149_434_574_456, // estimate + 5%
        approved_scan_bytes: 536_870_912_000, // 500 GiB
        project_id: 'acme-analytics',
        sql_preview:
          'SELECT user_id, COUNT(*) AS events\nFROM `acme-analytics.events.pageviews`\nWHERE _PARTITIONTIME >= "2026-01-01"\nGROUP BY user_id',
        summary: 'Estimated 132.5 GiB scan is over your 100 GiB automatic limit.',
      },
      options: ['once'],
    }),
  },
  {
    label: 'Pending execute — BigQuery scan budget (no ceiling configured)',
    approval: approval({
      id: 'apr-bq-scan-budget-no-ceiling',
      tool: 'run_bigquery_query',
      category: 'execute',
      operation: 'query_scan_budget',
      subject: {
        kind: 'tool',
        tool: 'run_bigquery_query',
        connector: 'bigquery',
        operation: 'query_scan_budget',
      },
      preview: {
        estimated_bytes: 142_318_642_339, // ~132.5 GiB
        automatic_scan_bytes: 107_374_182_400, // 100 GiB — no approved ceiling set
        authorized_scan_bytes: 149_434_574_456, // estimate + 5%
        project_id: 'acme-analytics',
        sql_preview:
          'SELECT user_id, COUNT(*) AS events\nFROM `acme-analytics.events.pageviews`\nWHERE _PARTITIONTIME >= "2026-01-01"\nGROUP BY user_id',
        summary: 'Estimated 132.5 GiB scan is over your 100 GiB automatic limit.',
      },
      options: ['once'],
    }),
  },
  {
    label: 'Pending — decision request fails (retry state)',
    approval: approval({
      id: 'apr-fails',
      tool: 'slack_post_message',
      subject: { kind: 'tool', tool: 'slack_post_message', connector: 'slack' },
      preview: { channel_name: 'general', text: 'Deploy finished ✅' },
    }),
    failDecide: true,
  },
  {
    label: 'Approved (always) — transcript, collapsed',
    approval: approval({
      id: 'apr-approved',
      status: 'approved',
      decision: { action: 'approve', scope: 'always', decided_at: minutesAgo(4) },
      preview: { issue_key: 'PROJ-99', summary: 'Add retry to uploader' },
    }),
  },
  {
    label: 'Rejected — transcript, collapsed (neutral, not red)',
    approval: approval({
      id: 'apr-rejected',
      status: 'rejected',
      decision: { action: 'reject', decided_at: minutesAgo(12) },
    }),
  },
  {
    label: 'Expired — transcript',
    approval: approval({ id: 'apr-expired', status: 'expired' }),
  },
  {
    label: 'Canceled — transcript',
    approval: approval({ id: 'apr-canceled', status: 'canceled' }),
  },
];

let fixtureFetchMockInstalled = false;
const fixturePermissionRequests: Array<Record<string, any>> = [];
const fixtureToolTestRequests: Array<Record<string, any>> = [];
const fixtureApprovalDecisions: Array<Record<string, any>> = [];

// In-memory permission rules so the Permissions tab is fully interactive in
// the fixture — create/update/delete land here, never on a real backend.
// Seeded to demo every provenance state: custom Off, custom Always-allow on a
// write tool (caution state), a connection-wide rule, a folder grant, a
// thread-scoped rule, and a rule for a tool the inventory doesn't know.
let fixtureRuleSeq = 0;
const fixtureRules: Array<Record<string, any>> = [
  {
    id: 'fxr-gh-cmd-off',
    category: 'execute',
    effect: 'deny',
    scope: 'global',
    subject: { kind: 'tool', tool: 'github_run_gh_command', connector: 'github' },
    created_via: 'settings',
    created_at: minutesAgoIso(60 * 24 * 3),
  },
  {
    id: 'fxr-slack-post-allow',
    category: 'write',
    effect: 'allow',
    scope: 'global',
    subject: { kind: 'tool', tool: 'slack_post_message', connector: 'slack' },
    created_via: 'approval',
    created_at: minutesAgoIso(60 * 5),
    last_used_at: minutesAgoIso(42),
  },
  {
    id: 'fxr-bq-read-ask',
    category: 'read',
    effect: 'ask',
    scope: 'global',
    subject: { kind: 'connector', connector: 'bigquery' },
    created_via: 'settings',
    created_at: minutesAgoIso(60 * 30),
  },
  {
    id: 'fxr-folder-docs',
    category: 'read',
    effect: 'allow',
    scope: 'global',
    subject: { kind: 'local_location', local_root: '/Users/alex/Documents', local_kind: 'folder', excludes: ['.env*', '*.pem', 'id_*'] },
    created_via: 'approval',
    created_at: minutesAgoIso(60 * 50),
    last_used_at: minutesAgoIso(60 * 2),
  },
  {
    id: 'fxr-thread-jira',
    category: 'write',
    effect: 'allow',
    scope: 'thread:thread-fixture',
    subject: { kind: 'tool', tool: 'jira_add_comment', connector: 'jira' },
    created_via: 'approval',
    created_at: minutesAgoIso(15),
  },
  {
    id: 'fxr-unknown-tool',
    category: 'write',
    effect: 'ask',
    scope: 'global',
    subject: { kind: 'tool', tool: 'legacy_export_report' },
    created_via: 'settings',
    created_at: minutesAgoIso(60 * 24 * 20),
  },
];

const fixtureToolInventory = staticToolMetadataForFixture().map((tool) =>
  tool.name === 'slack_post_message'
    ? { ...tool, label: 'Send a fixture Slack message' }
    : tool,
);
const fixtureProviderHostedTools = staticProviderHostedToolCapabilitiesForFixture();

function exposeFixtureState(): void {
  if (typeof window === 'undefined') return;
  (window as any).__approvalsFixtureRules = fixtureRules;
  (window as any).__approvalsFixturePermissionRequests = fixturePermissionRequests;
  (window as any).__approvalsFixtureToolTestRequests = fixtureToolTestRequests;
  (window as any).__approvalsFixtureDecisions = fixtureApprovalDecisions;
}

function minutesAgoIso(m: number): string {
  return new Date(Date.now() - m * 60_000).toISOString();
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

async function handlePermissionsRequest(
  path: string,
  init?: RequestInit,
): Promise<Response> {
  await new Promise((resolve) => setTimeout(resolve, 250));
  const method = (init?.method || 'GET').toUpperCase();
  if (path === '/api/permissions/audit') {
    return jsonResponse({ records: [] });
  }
  if (path === '/api/permissions/local-locations/test' && method === 'POST') {
    const body = JSON.parse(String(init?.body || '{}'));
    fixturePermissionRequests.push({ method, path, body });
    return jsonResponse({ ok: true });
  }
  if (path === '/api/permissions/rules' && method === 'GET') {
    return jsonResponse({ rules: fixtureRules });
  }
  if (path === '/api/permissions/rules' && method === 'POST') {
    const body = JSON.parse(String(init?.body || '{}'));
    fixturePermissionRequests.push({ method, path, body });
    const rule = {
      id: `fxr-new-${++fixtureRuleSeq}`,
      created_via: 'settings',
      created_at: new Date().toISOString(),
      ...body,
    };
    fixtureRules.push(rule);
    exposeFixtureState();
    return jsonResponse({ rule });
  }
  const ruleMatch = path.match(/^\/api\/permissions\/rules\/([^/]+)$/);
  if (ruleMatch) {
    const id = decodeURIComponent(ruleMatch[1]);
    const index = fixtureRules.findIndex((r) => r.id === id);
    if (index === -1) return jsonResponse({ error: 'Rule not found' }, 404);
    if (method === 'PATCH') {
      const body = JSON.parse(String(init?.body || '{}'));
      fixturePermissionRequests.push({ method, path, id, body });
      fixtureRules[index] = { ...fixtureRules[index], ...body };
      exposeFixtureState();
      return jsonResponse({ rule: fixtureRules[index] });
    }
    if (method === 'DELETE') {
      fixturePermissionRequests.push({ method, path, id });
      fixtureRules.splice(index, 1);
      exposeFixtureState();
      return jsonResponse({ ok: true });
    }
  }
  return jsonResponse({ error: 'Fixture API unavailable' }, 404);
}

function fieldValue(source: Record<string, any> | undefined, snake: string, camel: string): string {
  if (!source) return '';
  return String(source[snake] ?? source[camel] ?? '').trim();
}

function findFixtureTool(toolName: string): Record<string, any> | undefined {
  const localTool = fixtureToolInventory.find((tool: any) => tool.name === toolName) as Record<string, any> | undefined;
  if (localTool) return localTool;
  const hosted = fixtureProviderHostedTools.find((tool: any) => tool.id === toolName || tool.name === toolName) as Record<string, any> | undefined;
  if (!hosted) return undefined;
  return {
    ...hosted,
    name: hosted.id,
    connector_id: hosted.adapter_family,
    permission_operation: hosted.settings_key,
  };
}

function fixturePermissionSubjectForTool(tool: Record<string, any>): Record<string, any> {
  const connector = fieldValue(tool, 'connector_id', 'connectorId');
  const toolSetId = fieldValue(tool, 'tool_set_id', 'toolSetId');
  const subjectKind = fieldValue(tool, 'permission_subject_kind', 'permissionSubjectKind') || 'tool';
  const operation = fieldValue(tool, 'permission_operation', 'permissionOperation');
  const subject: Record<string, any> = {
    kind: subjectKind,
    tool: String(tool.name || tool.id || ''),
  };
  if (connector) subject.connector = connector;
  if (subjectKind !== 'tool' && toolSetId) subject.tool_set_id = toolSetId;
  if (operation) subject.operation = operation;
  return subject;
}

function fixtureRuleMatchesExactTool(rule: Record<string, any>, tool: Record<string, any>): boolean {
  const subject = rule.subject || {};
  const wanted = fixturePermissionSubjectForTool(tool);
  if (String(rule.category || '') !== String(tool.category || 'read')) return false;
  if (String(subject.kind || 'tool') !== String(wanted.kind || 'tool')) return false;
  if (String(subject.tool || '') !== String(wanted.tool || '')) return false;
  if (wanted.connector && String(subject.connector || '') !== String(wanted.connector)) return false;
  if (wanted.tool_set_id && String(subject.tool_set_id || '') !== String(wanted.tool_set_id)) return false;
  if (wanted.operation && String(subject.operation || '') !== String(wanted.operation)) return false;
  return true;
}

function fixtureRuleMatchesToolSet(rule: Record<string, any>, tool: Record<string, any>): boolean {
  const subject = rule.subject || {};
  const toolSetId = fieldValue(tool, 'tool_set_id', 'toolSetId');
  const connector = fieldValue(tool, 'connector_id', 'connectorId');
  if (String(rule.category || '') !== String(tool.category || 'read')) return false;
  if (String(subject.kind || '') !== 'tool_set') return false;
  if (!toolSetId || String(subject.tool_set_id || '') !== toolSetId) return false;
  return !subject.connector || String(subject.connector || '') === connector;
}

function fixtureRuleMatchesConnector(rule: Record<string, any>, tool: Record<string, any>): boolean {
  const subject = rule.subject || {};
  const connector = fieldValue(tool, 'connector_id', 'connectorId');
  if (String(rule.category || '') !== String(tool.category || 'read')) return false;
  if (String(subject.kind || '') !== 'connector') return false;
  return Boolean(connector) && String(subject.connector || '') === connector;
}

function fixturePermissionEffectForTool(tool: Record<string, any>): string {
  const scopedRules = fixtureRules.filter((rule) => !String(rule.scope || '').startsWith('thread:'));
  const exact = scopedRules.find((rule) => fixtureRuleMatchesExactTool(rule, tool));
  if (exact) return String(exact.effect || 'ask');
  const toolSet = scopedRules.find((rule) => fixtureRuleMatchesToolSet(rule, tool));
  if (toolSet) return String(toolSet.effect || 'ask');
  const connector = scopedRules.find((rule) => fixtureRuleMatchesConnector(rule, tool));
  if (connector) return String(connector.effect || 'ask');
  return String(tool.category || 'read') === 'read' ? 'allow' : 'ask';
}

async function handleToolTestRequest(
  path: string,
  init?: RequestInit,
): Promise<Response> {
  const method = (init?.method || 'GET').toUpperCase();
  const pathMatch = path.match(/^\/api\/tools\/([^/]+)\/test$/);
  const bodyEndpoint = path === '/api/tools/test';
  if ((!pathMatch && !bodyEndpoint) || method !== 'POST') {
    return jsonResponse({ error: 'Fixture tool-test API unavailable' }, 404);
  }
  const body = JSON.parse(String(init?.body || '{}'));
  const toolName = bodyEndpoint
    ? String(body.tool_name || '').trim()
    : decodeURIComponent(pathMatch?.[1] || '');
  if (!toolName) {
    return jsonResponse({ error: 'missing toolName' }, 400);
  }
  fixtureToolTestRequests.push({ method, path, toolName, body });
  exposeFixtureState();

  const tool = findFixtureTool(toolName);
  if (!tool) {
    return jsonResponse({ error: `Unknown fixture tool: ${toolName}` }, 404);
  }
  const effect = fixturePermissionEffectForTool(tool);
  if (effect !== 'allow') {
    return jsonResponse(
      {
        error: 'Tool test is blocked by fixture permissions.',
        tool_name: toolName,
        decision: effect === 'deny' ? 'deny' : 'ask',
        category: tool.category || 'read',
        subject: fixturePermissionSubjectForTool(tool),
        reason: 'fixture_permission',
      },
      effect === 'deny' ? 403 : 409,
    );
  }

  return jsonResponse({
    test: {
      tool_name: toolName,
      mode: fieldValue(tool.test, 'mode', 'mode') || 'sample_call',
      requires_approval: Boolean(tool.test?.requires_approval),
      category: tool.category || 'read',
      origin: fieldValue(tool, 'origin', 'origin') || 'builtin',
      tool_set_id: fieldValue(tool, 'tool_set_id', 'toolSetId'),
      connector_id: fieldValue(tool, 'connector_id', 'connectorId') || undefined,
      presentation: tool.presentation || {},
      args: body.args || {},
      output: {
        ok: true,
        result_format: 'json',
        result: {
          fixture: true,
          tool_name: toolName,
          args: body.args || {},
        },
        result_metadata: {
          tool_name: toolName,
          tool_set_id: fieldValue(tool, 'tool_set_id', 'toolSetId'),
        },
      },
      progress: [
        { phase: 'fixture', message: 'Fixture tool test completed.' },
      ],
    },
  });
}

function installFixtureFetchMock(): void {
  if (fixtureFetchMockInstalled || typeof window === 'undefined') return;
  fixtureFetchMockInstalled = true;
  window.localFilePermissions = {
    pickDirectory: async () => ({
      path: '/Users/alex/Documents',
      displayName: '~/Documents',
    }),
    pickFile: async () => ({
      path: '/Users/alex/Documents/Q3 forecast.xlsx',
      displayName: '~/Documents/Q3 forecast.xlsx',
    }),
  };
  exposeFixtureState();
  const originalFetch = window.fetch.bind(window);
  window.fetch = (input, init) => {
    const rawURL =
      typeof input === 'string'
        ? input
        : input instanceof Request
          ? input.url
          : String(input);
    const path = new URL(rawURL, window.location.href).pathname;
    if (path.startsWith('/api/permissions')) {
      return handlePermissionsRequest(path, init);
    }
    if (path === '/api/tools/test' || /^\/api\/tools\/[^/]+\/test$/.test(path)) {
      return handleToolTestRequest(path, init);
    }
    if (path === '/api/tools/inventory') {
      return Promise.resolve(
        jsonResponse({
          tools: fixtureToolInventory,
          provider_hosted_tools: fixtureProviderHostedTools,
        }),
      );
    }
    if (path.startsWith('/api/approvals')) {
      return Promise.resolve(
        new Response(
          JSON.stringify({ ok: false, error: 'Fixture API unavailable' }),
          { status: 404, headers: { 'Content-Type': 'application/json' } },
        ),
      );
    }
    return originalFetch(input, init);
  };
}

const ApprovalsFixture: React.FC = () => {
  installFixtureFetchMock();
  const [decisions, setDecisions] = useState<Record<string, string>>({});

  const fakeDecide = useMemo(
    () =>
      (fail?: boolean) =>
      async (id: string, decision: { action: string; scope?: string; subject?: Record<string, any> }) => {
        await new Promise((resolve) => setTimeout(resolve, 600));
        if (fail) throw new Error('Simulated network failure');
        fixtureApprovalDecisions.push({ id, decision });
        exposeFixtureState();
        setDecisions((prev) => ({
          ...prev,
          [id]: `${decision.action}${decision.scope ? `:${decision.scope}` : ''}`,
        }));
      },
    [],
  );

  return (
    <TooltipProvider delayDuration={300}>
      <div className="min-h-screen bg-background p-6 text-foreground">
        <div className="mx-auto max-w-2xl space-y-8 pb-10">
          <header>
            <h1 className="text-lg font-semibold">Approvals UI fixture</h1>
            <p className="text-[13px] text-muted-foreground">
              Cards decide locally (600ms fake latency). “Inject into strip”
              publishes pending approvals into the shared store so the strip
              above the fake composer becomes live.
            </p>
          </header>

          {FIXTURES.map(({ label, approval: a, failDecide }) => (
            <section key={a.id}>
              <h2 className="mb-2 text-[12px] font-medium uppercase text-muted-foreground">
                {label}
                {decisions[a.id] ? ` · decided (${decisions[a.id]})` : ''}
              </h2>
              <ApprovalCard
                approval={a}
                variant={a.status === 'pending' ? 'live' : 'transcript'}
                onDecide={fakeDecide(failDecide)}
              />
            </section>
          ))}

          <section>
            <h2 className="mb-2 text-[12px] font-medium uppercase text-muted-foreground">
              Settings → Permissions tab (interactive — in-memory mock rules)
            </h2>
            <div className="rounded-lg border border-border p-4">
              <PermissionsTab settings={FIXTURE_SETTINGS} />
            </div>
          </section>
        </div>

        {/* Fake composer + live strip */}
        <section className="mx-auto w-full max-w-2xl pb-10">
          <h2 className="mb-2 text-[12px] font-medium uppercase text-muted-foreground">
            Live strip above composer
          </h2>
          <div className="rounded-lg border border-border bg-background/70 py-3">
            <PendingApprovalsStrip
              threadId="thread-fixture"
              onOpenPermissions={() => {}}
            />
            <div className="flex w-full flex-col gap-2 px-4 sm:flex-row sm:items-center">
              <textarea
                id="chat-composer-input"
                rows={1}
                placeholder="Fake composer (focus target)"
                className="min-h-11 w-full resize-none rounded-xl border border-border bg-muted/40 p-3 text-sm"
              />
              <Button
                type="button"
                variant="outline"
                className="w-full sm:w-auto"
                onClick={() => {
                  const id = `apr-live-${Date.now()}`;
                  publishApprovalEvent(
                    {
                      id,
                      thread_id: 'thread-fixture',
                      run_id: 'run-fixture',
                      tool: 'jira_add_comment',
                      category: 'write',
                      subject: { kind: 'tool', tool: 'jira_add_comment', connector: 'jira' },
                      preview: { issue_key: 'PROJ-7', text: `Fixture comment ${id.slice(-4)}` },
                      options: ['once', 'thread'],
                      status: 'pending',
                      created_at: new Date().toISOString(),
                    },
                  );
                }}
              >
                Inject into strip
              </Button>
            </div>
          </div>
        </section>
      </div>
    </TooltipProvider>
  );
};

export default ApprovalsFixture;
