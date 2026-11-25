import React, { useMemo } from 'react';
import { ShieldAlert, Wrench } from 'lucide-react';
import type { ConnectionCatalogId } from '@/types';
import {
  CONNECTION_META,
  getConnectionForToolName,
} from '@/lib/connectionsCatalog';
import { registerCustomResolver } from '../../registry';
import type { RendererProps } from '../../types';
import { Disclosure } from '../query/atoms';
import { InspectorShell, InspectorZone } from './InspectorShell';
import { resolveInspectorResponseWidget } from './responseWidgets';
import { KeyValueGrid, ValueBlock, isPlainObject, safeJson } from './values';

/**
 * ToolInspector — the connector-aware generic inspector.
 *
 * It claims the "click-to-inspect" tool envelopes that have no dedicated
 * renderer (Slack, Atlassian/Jira/Confluence, Browser/CUA, web_search, and any
 * future tool) and renders them in the shared three-zone shell instead of
 * dumping raw JSON. Query and GitHub keep their own first-class inspectors;
 * errors fall to ErrorRenderer (which shows the same connector logo). This is
 * the success-state generalist that keeps the inspector family complete.
 */

const PRIORITY_KEYS = [
  'messages',
  'replies',
  'conversations',
  'channels',
  'members',
  'users',
  'issues',
  'pull_requests',
  'workflow_runs',
  'jobs',
  'repositories',
  'results',
  'matches',
  'comments',
  'attachments',
  'files',
  'logs',
  'elements',
  'rows',
  'data',
  'items',
];

const URL_KEYS = ['url', 'html_url', 'permalink', 'web_url', 'self', 'link'];

function connIdFromIntegration(integration: unknown): ConnectionCatalogId | null {
  const s = String(integration || '').trim().toLowerCase();
  if (!s) return null;
  if (s === 'jira' || s === 'confluence' || s === 'atlassian') return 'atlassian';
  if (s === 'browser_control' || s === 'browser') return 'browser';
  if (s in CONNECTION_META) return s as ConnectionCatalogId;
  return null;
}

function providerHostedLabelFromAdapter(adapterFamily: unknown, hostedToolId?: unknown): string | null {
  const adapter = String(adapterFamily || '').trim().toLowerCase();
  const id = String(hostedToolId || '').trim().toLowerCase();
  const source = adapter || id;
  if (!source) return null;
  if (source.startsWith('anthropic-messages') || source.startsWith('anthropic/')) {
    return 'Anthropic';
  }
  if (source.startsWith('openai-responses') || source.startsWith('openai/')) {
    return 'OpenAI Responses';
  }
  return null;
}

function providerHostedLabel(meta: Record<string, any>): string | null {
  const resultMeta = isPlainObject(meta.result_metadata) ? meta.result_metadata : {};
  return providerHostedLabelFromAdapter(
    resultMeta.adapter_family || meta.adapter_family,
    resultMeta.provider_hosted_tool_id || meta.provider_hosted_tool_id,
  );
}

function resolveConnector(meta: Record<string, any>) {
  const toolName = String(meta.tool_name || '');
  const resultMeta = isPlainObject(meta.result_metadata) ? meta.result_metadata : {};
  const presentation = resolveToolPresentation(meta);
  const id =
    getConnectionForToolName(toolName) ||
    connIdFromIntegration(meta.integration) ||
    connIdFromIntegration(resultMeta.integration) ||
    connIdFromIntegration(resultMeta.product) ||
    connIdFromIntegration(presentation?.family);
  return id ? CONNECTION_META[id] : null;
}

function resolveToolPresentation(meta: Record<string, any>): Record<string, any> | null {
  if (isPlainObject(meta.tool_presentation)) return meta.tool_presentation;
  if (
    isPlainObject(meta.result_metadata) &&
    isPlainObject(meta.result_metadata.tool_presentation)
  ) {
    return meta.result_metadata.tool_presentation;
  }
  return null;
}

function titleCase(key: string): string {
  return key
    .replace(/[_-]+/g, ' ')
    .replace(/\b\w/g, (c) => c.toUpperCase())
    .trim();
}

/** Drop UI-internal timing stamps (e.g. `_uiStartedAt`) so they never leak
 *  into the rendered Request/Response/Metadata zones. */
function stripUiKeys(v: unknown): unknown {
  if (!isPlainObject(v)) return v;
  const out: Record<string, any> = {};
  for (const [k, val] of Object.entries(v)) {
    if (!k.startsWith('_ui')) out[k] = val;
  }
  return out;
}

interface PickedPrimary {
  label: string;
  featured: unknown;
  /** Remaining top-level fields (paging, counts) shown under Metadata. */
  rest: Record<string, any> | null;
  isApproval: boolean;
}

/** Choose the most useful payload to feature in the Response zone. */
function pickPrimary(result: unknown): PickedPrimary {
  if (isPlainObject(result) && isPlainObject((result as any).approval)) {
    return { label: 'Pending action', featured: (result as any).approval, rest: null, isApproval: true };
  }
  if (typeof result === 'string' || Array.isArray(result) || !isPlainObject(result)) {
    return { label: 'Result', featured: result, rest: null, isApproval: false };
  }
  const obj = result as Record<string, any>;
  const entries = Object.entries(obj);
  // Only let a single array hijack the "featured" slot when it is a real
  // collection (array of objects). A bare scalar array like `labels` should not
  // outrank the object it belongs to — feature the whole object instead.
  const objectArrayKeys = entries.filter(
    ([, v]) => Array.isArray(v) && v.length > 0 && v.some((x) => isPlainObject(x)),
  );
  let featuredKey: string | null =
    PRIORITY_KEYS.find((k) => Array.isArray(obj[k]) && (obj[k] as any[]).length > 0) ||
    (objectArrayKeys.length === 1 ? objectArrayKeys[0][0] : null);
  if (featuredKey) {
    const rest = Object.fromEntries(entries.filter(([k]) => k !== featuredKey));
    return {
      label: titleCase(featuredKey),
      featured: obj[featuredKey],
      rest: Object.keys(rest).length ? rest : null,
      isApproval: false,
    };
  }
  return { label: 'Result', featured: obj, rest: null, isApproval: false };
}

function findConsoleUrl(result: unknown): string | null {
  if (!isPlainObject(result)) return null;
  for (const k of URL_KEYS) {
    const v = (result as any)[k];
    // REST endpoints (e.g. the Atlassian bridge's request URL) serve JSON,
    // not a console — never offer them as the header's "Open in …" link.
    if (typeof v === 'string' && /^https?:\/\//i.test(v) && !/\/rest\/api\//.test(v)) return v;
  }
  return null;
}

const ToolInspector: React.FC<RendererProps> = ({ artifact }) => {
  const meta = (artifact.metadata || {}) as Record<string, any>;
  const toolName = String(meta.tool_name || artifact.title || 'tool');
  const connector = useMemo(() => resolveConnector(meta), [artifact.metadata]);

  const result = useMemo(() => {
    let raw: unknown = undefined;
    if (meta.raw_result !== undefined) raw = meta.raw_result;
    else if (typeof artifact.content === 'string' && artifact.content.trim()) {
      try {
        raw = JSON.parse(artifact.content);
      } catch {
        raw = artifact.content;
      }
    }
    return stripUiKeys(raw);
  }, [meta.raw_result, artifact.content]);

  const primary = useMemo(() => pickPrimary(result), [result]);
  const toolArgs = isPlainObject(meta.tool_args) ? meta.tool_args : null;

  // Tools with a first-class rendered view (registered in responseWidgets)
  // claim the Response zone; the auto table / key-value fallback covers the
  // rest. Raw JSON stays available behind the Metadata zone's Raw disclosure.
  const ResponseWidget = useMemo(
    () => resolveInspectorResponseWidget(meta, result),
    [meta, result],
  );

  const resultMeta = isPlainObject(meta.result_metadata) ? meta.result_metadata : {};
  const integration =
    (typeof resultMeta.integration === 'string' && resultMeta.integration) ||
    (typeof meta.integration === 'string' && meta.integration) ||
    '';
  const product = typeof resultMeta.product === 'string' ? resultMeta.product : '';
  const widget = typeof resultMeta.widget === 'string' ? resultMeta.widget : '';
  const kind = typeof resultMeta.kind === 'string' ? resultMeta.kind : '';
  const readonly = typeof resultMeta.readonly === 'boolean' ? resultMeta.readonly : undefined;
  const isWrite = primary.isApproval || widget === 'approval' || readonly === false;
  const hostedLabel = providerHostedLabel(meta);
  const hostedToolId =
    typeof resultMeta.provider_hosted_tool_id === 'string'
      ? resultMeta.provider_hosted_tool_id
      : typeof meta.provider_hosted_tool_id === 'string'
        ? meta.provider_hosted_tool_id
        : '';
  const adapterFamily =
    typeof resultMeta.adapter_family === 'string'
      ? resultMeta.adapter_family
      : typeof meta.adapter_family === 'string'
        ? meta.adapter_family
        : '';

  const providerLabel =
    hostedLabel ||
    (product && titleCase(product)) ||
    connector?.label ||
    (integration && titleCase(integration)) ||
    'Tool';

  const consoleUrl = findConsoleUrl(result);

  const identityChips: Array<[string, string]> = [];
  if (hostedToolId) identityChips.push(['hosted tool', hostedToolId]);
  if (adapterFamily) identityChips.push(['adapter', adapterFamily]);
  if (integration) identityChips.push(['integration', integration]);
  if (product && product !== integration) identityChips.push(['product', product]);
  if (kind) identityChips.push(['kind', kind]);
  if (widget) identityChips.push(['widget', widget]);
  if (readonly !== undefined) identityChips.push(['access', readonly ? 'read-only' : 'write']);

  const rawJson = useMemo(() => safeJson(result), [result]);

  return (
    <InspectorShell
      connectorMeta={connector}
      fallbackIcon={Wrench}
      toolName={toolName}
      status="success"
      providerLabel={providerLabel}
      durationMs={typeof meta.duration_ms === 'number' ? meta.duration_ms : null}
      endedAtMs={typeof meta.ended_at_ms === 'number' ? meta.ended_at_ms : null}
      toolCallId={typeof meta.tool_call_id === 'string' ? meta.tool_call_id : null}
      consoleUrl={consoleUrl}
      badge={
        isWrite ? (
          <span className="inline-flex items-center gap-1 rounded-md border border-amber-500/30 bg-amber-500/10 px-2 py-0.5 text-[11px] font-medium text-amber-600">
            <ShieldAlert size={11} aria-hidden />
            {primary.isApproval || widget === 'approval' ? 'Approval required' : 'Write'}
          </span>
        ) : null
      }
    >
      {/* Request */}
      <InspectorZone label="Request">
        {toolArgs && Object.keys(toolArgs).length > 0 ? (
          <KeyValueGrid data={toolArgs} />
        ) : (
          <span className="text-[12px] text-muted-foreground/60">No arguments</span>
        )}
      </InspectorZone>

      {/* Response */}
      <InspectorZone label={primary.isApproval ? 'Pending action — awaiting approval' : 'Response'}>
        {ResponseWidget ? (
          <ResponseWidget result={result} meta={meta} />
        ) : (
          <ValueBlock value={primary.featured} />
        )}
      </InspectorZone>

      {/* Metadata */}
      <InspectorZone label="Metadata">
        <div className="flex flex-col gap-3">
          {identityChips.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {identityChips.map(([k, v]) => (
                <span
                  key={k}
                  className="inline-flex items-center gap-1 rounded-md border border-border bg-muted/40 px-2 py-0.5 text-[11px]"
                >
                  <span className="text-muted-foreground/60">{k}</span>
                  <span className="font-mono text-foreground">{v}</span>
                </span>
              ))}
            </div>
          )}
          {/* When a response widget owns the Response zone, the auto-derived
              "rest" grid would re-dump payload fields the widget already
              renders (e.g. the Jira issue object) — Raw covers them instead. */}
          {!ResponseWidget && primary.rest && (
            <div>
              <div className="mb-1.5 text-[11px] text-muted-foreground/60">Response details</div>
              <KeyValueGrid data={primary.rest} depth={1} />
            </div>
          )}
          <Disclosure label="Raw">
            <pre className="mt-1 max-h-[420px] overflow-auto rounded-md border border-border bg-muted/30 p-3 font-mono text-[11px] text-foreground [overflow-wrap:anywhere] whitespace-pre-wrap">
              {rawJson}
            </pre>
          </Disclosure>
        </div>
      </InspectorZone>
    </InspectorShell>
  );
};

/**
 * Claims the synthesised click-to-inspect envelope (identified by `tool_args`)
 * for any tool that lacks a dedicated renderer. Excludes:
 *   - errors (`ok === false`)        → ErrorRenderer (shared diagnostic skeleton)
 *   - charts (`plot_spec`)           → DataVisualization
 *   - SQL (`query_metadata`)         → QueryInspector
 *   - GitHub                         → claimed earlier by GitHubToolRenderer
 * Registered last so every more-specific resolver wins first.
 */
registerCustomResolver((artifact) => {
  const m = artifact.metadata as Record<string, any> | undefined;
  if (!m || typeof m !== 'object') return null;
  if (m.ok === false) return null;
  if (m.plot_spec) return null;
  if (m.query_metadata) return null;
  if (typeof m.tool_name !== 'string' || !m.tool_name) return null;
  const presentation = resolveToolPresentation(m);
  const nativeViewer =
    typeof presentation?.native_viewer === 'string'
      ? presentation.native_viewer
      : '';
  const rendererStrategy =
    typeof presentation?.renderer_strategy === 'string'
      ? presentation.renderer_strategy
      : '';
  const connectorPresentation =
    nativeViewer === 'connector_inspector' || rendererStrategy === 'family';
  if (presentation && !connectorPresentation) return null;
  if (!('tool_args' in m) && !connectorPresentation) {
    return null;
  }

  const resultMeta = isPlainObject(m.result_metadata) ? m.result_metadata : {};
  const id =
    getConnectionForToolName(m.tool_name) ||
    connIdFromIntegration(m.integration) ||
    connIdFromIntegration(resultMeta.integration) ||
    connIdFromIntegration(resultMeta.product) ||
    connIdFromIntegration(presentation?.family);
  const connectorMeta = id ? CONNECTION_META[id] : null;
  const hostedLabel = providerHostedLabel(m);
  return {
    component: ToolInspector,
    label: connectorMeta?.label || hostedLabel || 'Tool',
    icon: connectorMeta?.icon || Wrench,
    ownsChrome: true,
  };
});

export default ToolInspector;
