import React, { useMemo, useState } from 'react';
import {
  AlertTriangle,
  Check,
  ChevronRight,
  Copy,
  Github,
} from 'lucide-react';
import ConnectionLogo from '@/components/chat/ConnectionLogo';
import { CONNECTION_META } from '@/lib/connectionsCatalog';
import { cn } from '@/lib/utils';
import { registerCustomResolver } from '../registry';
import type { RendererProps } from '../types';
import { resolveWidgetKey, WidgetForKey } from './github/widgets';
import {
  ErrorDiagnosticBody,
  extractToolErrorDiagnostic,
  type ConnectorClassifier,
} from './errorDiagnostic';

// ---------- helpers ----------

function isObject(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function formatDuration(ms: number | undefined): string | null {
  if (typeof ms !== 'number' || !Number.isFinite(ms) || ms < 0) return null;
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

function formatClock(ms: number | undefined): string | null {
  if (typeof ms !== 'number' || !Number.isFinite(ms)) return null;
  const d = new Date(ms);
  if (Number.isNaN(d.getTime())) return null;
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

function shortId(id: string): string {
  if (!id) return '';
  if (id.length <= 14) return id;
  return `${id.slice(0, 8)}…${id.slice(-4)}`;
}

/**
 * GitHub connector classifier — last-resort fallback for legacy error strings
 * (raw `error: "404 …"` payloads). Tools that include `result_metadata.diagnostic`
 * in their result envelope take precedence; see ./errorDiagnostic.tsx.
 */
const classifyError: ConnectorClassifier = (message: string) => {
  const m = message.toLowerCase();
  if (/\b401\b|unauthorized|bad credentials/.test(m)) {
    return {
      kind: 'auth',
      title: 'Authentication required',
      code: '401',
      why: 'Your GitHub connection is not authorized for this request.',
      tryThis: 'Reconnect GitHub in Settings → Connections, then try again.',
    };
  }
  if (/\b403\b|rate limit/.test(m)) {
    return {
      kind: 'ratelimit',
      title: 'Rate limited',
      code: '403',
      why: 'GitHub temporarily blocked further requests from this token.',
      tryThis: 'Wait a moment and try the request again.',
    };
  }
  if (/\b404\b|not found/.test(m)) {
    return {
      kind: 'notfound',
      title: 'Not found',
      code: '404',
      why: "GitHub couldn't find that resource, or your token can't see it.",
      tryThis: 'Verify the name and confirm your GitHub connection has access.',
    };
  }
  return {};
};

/** Build a readable terminal command line for the request hero. */
function buildCommandLine(
  toolName: string,
  raw: unknown,
  toolArgs: Record<string, any> | undefined,
): string {
  if (isObject(raw) && typeof raw.command === 'string' && raw.command.trim()) {
    return `$ ${raw.command.trim()}`;
  }
  if (toolArgs && typeof toolArgs.command === 'string' && toolArgs.command.trim()) {
    const c = toolArgs.command.trim();
    return `$ ${c.startsWith('gh ') ? c : `gh ${c}`}`;
  }
  if (toolArgs && Array.isArray(toolArgs.args)) {
    return `$ gh ${toolArgs.args.join(' ')}`;
  }
  if (/list_repositories/.test(toolName)) {
    const owner =
      (toolArgs && (toolArgs.owner || toolArgs.org || toolArgs.user)) || '';
    return `$ gh repo list${owner ? ` ${owner}` : ''}`;
  }
  if (/pull_request/.test(toolName)) {
    const repo = (toolArgs && (toolArgs.repo || toolArgs.repository)) || '';
    return `$ gh pr list${repo ? ` --repo ${repo}` : ''}`;
  }
  // Fall back to the operation name.
  return `$ ${toolName.replace(/^github_/, '') || 'github operation'}`;
}

// ---------- small UI pieces ----------

function CopyIcon({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  const onClick = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // ignore
    }
  };
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      className="inline-flex items-center justify-center rounded p-0.5 text-muted-foreground/60 hover:text-foreground transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
    >
      {copied ? (
        <Check size={12} className="text-emerald-600" aria-hidden />
      ) : (
        <Copy size={12} aria-hidden />
      )}
    </button>
  );
}

function StatusPill({ status }: { status: 'success' | 'error' | 'running' }) {
  if (status === 'error') {
    return (
      <span className="inline-flex items-center gap-1 rounded-md border border-destructive/30 bg-destructive/10 px-2 py-0.5 text-[11px] font-medium text-destructive">
        <AlertTriangle size={11} aria-hidden /> Error
      </span>
    );
  }
  if (status === 'running') {
    return (
      <span className="inline-flex items-center gap-1 rounded-md border border-blue-500/25 bg-blue-500/10 px-2 py-0.5 text-[11px] font-medium text-blue-600">
        Running
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1 rounded-md border border-emerald-500/25 bg-emerald-500/10 px-2 py-0.5 text-[11px] font-medium text-emerald-600">
      <Check size={11} aria-hidden /> Success
    </span>
  );
}

function Disclosure({
  label,
  children,
}: {
  label: string;
  children: React.ReactNode;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        className="flex items-center gap-1 rounded text-xs text-muted-foreground hover:text-foreground transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
      >
        <ChevronRight
          size={12}
          aria-hidden
          className={cn('transition-transform', open && 'rotate-90')}
        />
        {label}
      </button>
      {open && <div className="mt-2">{children}</div>}
    </div>
  );
}

const wrapPre =
  'font-mono text-[12px] whitespace-pre-wrap [overflow-wrap:anywhere] bg-muted/40 border border-border rounded-md px-3 py-2.5 text-foreground';

// ---------- main component ----------

const GitHubToolRenderer: React.FC<RendererProps> = ({
  artifact,
  toolResult,
  className,
}) => {
  const meta = artifact.metadata || {};
  const toolName = String(meta.tool_name || artifact.title || 'github');
  const toolArgs = isObject(meta.tool_args) ? meta.tool_args : undefined;
  const callId = String(meta.tool_call_id || '');
  const raw = (toolResult ?? meta.raw_result) as unknown;

  const ok = meta.ok;
  const hasResult = raw !== undefined && raw !== null;
  const resultHasError = isObject(raw) && typeof raw.error === 'string' && !!raw.error.trim();
  const legacyResultFailure = typeof ok !== 'boolean' && resultHasError;
  const status: 'success' | 'error' | 'running' =
    ok === false || legacyResultFailure
      ? 'error'
      : ok === undefined && !hasResult
        ? 'running'
        : 'success';

  const [view, setView] = useState<'rendered' | 'raw'>('rendered');

  const widgetKey = useMemo(() => resolveWidgetKey(meta), [meta]);

  const commandLine = useMemo(
    () => buildCommandLine(toolName, raw, toolArgs),
    [toolName, raw, toolArgs],
  );

  const rawJson = useMemo(() => {
    try {
      return JSON.stringify(raw, null, 2);
    } catch {
      return String(raw);
    }
  }, [raw]);

  const duration = formatDuration(
    typeof meta.duration_ms === 'number' ? meta.duration_ms : undefined,
  );
  const clock = formatClock(
    typeof meta.ended_at_ms === 'number' ? meta.ended_at_ms : undefined,
  );

  const ghMeta = CONNECTION_META.github;
  const resultMeta = isObject(meta.result_metadata) ? meta.result_metadata : {};

  // Diagnostic comes from the tool/backend via result_metadata.diagnostic (or
  // direct fields). The GitHub `classifyError` is supplied as a string-based
  // fallback for legacy error envelopes that only carry a plain `error` string.
  const diagnostic = useMemo(
    () => extractToolErrorDiagnostic(artifact, { classifier: classifyError }),
    [artifact],
  );

  const metaChips = useMemo(() => {
    const chips: Array<[string, string]> = [];
    const integration = resultMeta.integration ?? meta.integration;
    const product = resultMeta.product ?? meta.product;
    const widget = resultMeta.widget ?? meta.widget;
    if (integration) chips.push(['integration', String(integration)]);
    if (product) chips.push(['product', String(product)]);
    if (widget) chips.push(['widget', String(widget)]);
    return chips;
  }, [resultMeta, meta]);

  // ---------- render ----------

  return (
    <div className={cn('flex h-full flex-col overflow-auto text-foreground', className)}>
      {/* Header band */}
      <div className="flex flex-col gap-2 border-b border-border px-4 py-3">
        <div className="flex flex-wrap items-center gap-2">
          <span className="flex h-[30px] w-[30px] shrink-0 items-center justify-center overflow-hidden rounded-md border border-border bg-muted/50">
            <ConnectionLogo
              meta={ghMeta}
              className={`h-4 w-4 object-contain${
                ghMeta.mono ? ' [filter:brightness(0)_invert(1)]' : ''
              }`}
            />
          </span>
          <span className="min-w-0 truncate font-mono text-[15px] text-foreground">
            {toolName}
          </span>
          <StatusPill status={status} />
        </div>
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
          <span>GitHub</span>
          {duration && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <span>{duration}</span>
            </>
          )}
          {clock && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <span>{clock}</span>
            </>
          )}
          {callId && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <span className="inline-flex items-center gap-1">
                <span className="font-mono">{shortId(callId)}</span>
                <CopyIcon value={callId} label="Copy tool call id" />
              </span>
            </>
          )}
        </div>
      </div>

      {/* Body */}
      {status === 'error' ? (
        <ErrorDiagnosticBody
          diagnostic={diagnostic}
          rawJson={rawJson}
          failedRequestFallback={commandLine}
          toolArgs={toolArgs ?? null}
        />
      ) : (
        <div className="flex flex-col gap-4 px-4 py-4">
          {/* Request zone */}
          <div>
            <div className="mb-1.5 text-xs text-muted-foreground/60">Request</div>
            <div className="font-mono text-[13px] whitespace-pre-wrap [overflow-wrap:anywhere] rounded-md border border-border bg-muted/40 px-3 py-2.5">
              {commandLine}
            </div>
            {toolArgs && (
              <div className="mt-2">
                <Disclosure label="raw args">
                  <pre className={wrapPre}>{JSON.stringify(toolArgs, null, 2)}</pre>
                </Disclosure>
              </div>
            )}
          </div>

          {/* Response zone — widget-aware rendering with a raw/rendered toggle. */}
          <div>
            <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
              <div className="inline-flex items-center rounded-md border border-border p-0.5">
                <button
                  type="button"
                  aria-pressed={view === 'rendered'}
                  onClick={() => setView('rendered')}
                  className={cn(
                    'rounded px-2 py-0.5 text-[11px] font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1',
                    view === 'rendered'
                      ? 'bg-muted text-foreground'
                      : 'text-muted-foreground hover:text-foreground',
                  )}
                >
                  Rendered
                </button>
                <button
                  type="button"
                  aria-pressed={view === 'raw'}
                  onClick={() => setView('raw')}
                  className={cn(
                    'rounded px-2 py-0.5 text-[11px] font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1',
                    view === 'raw'
                      ? 'bg-muted text-foreground'
                      : 'text-muted-foreground hover:text-foreground',
                  )}
                >
                  JSON
                </button>
              </div>
            </div>

            {view === 'raw' ? (
              <pre className={wrapPre}>{rawJson}</pre>
            ) : (
              <WidgetForKey widgetKey={widgetKey} raw={raw} meta={meta} />
            )}
          </div>

          {/* Metadata disclosure */}
          {metaChips.length > 0 && (
            <div className="mt-4 border-t border-border pt-3">
              <Disclosure label="Metadata">
                <div className="flex flex-wrap gap-1.5">
                  {metaChips.map(([k, v]) => (
                    <span
                      key={k}
                      className="inline-flex items-center gap-1 rounded-md border border-border bg-muted/40 px-2 py-0.5 font-mono text-[11px] text-muted-foreground"
                    >
                      <span className="text-muted-foreground/60">{k}</span>
                      <span className="text-foreground">{v}</span>
                    </span>
                  ))}
                </div>
              </Disclosure>
            </div>
          )}
        </div>
      )}
    </div>
  );
};

registerCustomResolver((artifact) => {
  const tn = String(artifact.metadata?.tool_name || '');
  const integ = String(artifact.metadata?.integration || '');
  if (integ === 'github' || /^github_/.test(tn)) {
    return {
      component: GitHubToolRenderer,
      label: 'GitHub',
      icon: Github,
    };
  }
  return null;
});

export default GitHubToolRenderer;
