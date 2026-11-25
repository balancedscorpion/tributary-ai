import React, { useMemo } from 'react';
import { AlertCircle, AlertTriangle } from 'lucide-react';
import { cn } from '@/lib/utils';
import ConnectionLogo from '@/components/chat/ConnectionLogo';
import type { ConnectionCatalogId } from '@/types';
import { CONNECTION_META, getConnectionForToolName } from '@/lib/connectionsCatalog';
import { registerCustomResolver } from '../registry';
import type { RendererProps } from '../types';
import {
  ErrorDiagnosticBody,
  extractToolErrorDiagnostic,
} from './errorDiagnostic';

function connIdFromIntegration(integration: string | null): ConnectionCatalogId | null {
  const s = String(integration || '').trim().toLowerCase();
  if (!s) return null;
  if (s === 'jira' || s === 'confluence' || s === 'atlassian') return 'atlassian';
  if (s === 'browser_control' || s === 'browser') return 'browser';
  if (s in CONNECTION_META) return s as ConnectionCatalogId;
  return null;
}

// ---------- helpers ----------

function isObject(v: unknown): v is Record<string, any> {
  return v !== null && typeof v === 'object' && !Array.isArray(v);
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
 * Synthesise a `tool_name(args)` line so the "Failed request" zone always has
 * something to show, even when the tool didn't include `failed_request` in its
 * diagnostic metadata. Keeps the inspector inspectable, not blank.
 */
function defaultFailedRequest(meta: Record<string, any>): string | null {
  const toolName = typeof meta.tool_name === 'string' ? meta.tool_name.trim() : '';
  if (!toolName) return null;
  const args = isObject(meta.tool_args) ? meta.tool_args : null;
  if (!args || !Object.keys(args).length) return `${toolName}()`;
  const parts = Object.entries(args).map(([k, v]) => {
    try {
      return `${k}: ${JSON.stringify(v)}`;
    } catch {
      return `${k}: <unserialisable>`;
    }
  });
  return `${toolName}({ ${parts.join(', ')} })`;
}

// ---------- component ----------

const ErrorRenderer: React.FC<RendererProps> = ({ artifact, className }) => {
  const meta = (artifact.metadata || {}) as Record<string, any>;
  const toolName = String(meta.tool_name || artifact.title || 'tool');
  const callId = String(meta.tool_call_id || '');
  const integration =
    (isObject(meta.result_metadata) && typeof meta.result_metadata.integration === 'string'
      ? meta.result_metadata.integration
      : null) ||
    (typeof meta.integration === 'string' ? meta.integration : null);
  // Brand the error header with the connector logo when we can resolve one, so
  // a failed Slack/Jira/Browser call still reads as that product (the StatusPill
  // already carries the error state). Falls back to the generic warning mark.
  const connId = getConnectionForToolName(toolName) || connIdFromIntegration(integration);
  const connectorMeta = connId ? CONNECTION_META[connId] : null;

  const duration = formatDuration(
    typeof meta.duration_ms === 'number' ? meta.duration_ms : undefined,
  );
  const clock = formatClock(
    typeof meta.ended_at_ms === 'number' ? meta.ended_at_ms : undefined,
  );

  const diagnostic = useMemo(() => extractToolErrorDiagnostic(artifact), [artifact]);

  const rawJson = useMemo(() => {
    const raw = meta.raw_result;
    try {
      return JSON.stringify(raw, null, 2);
    } catch {
      return String(raw);
    }
  }, [meta.raw_result]);

  const failedRequestFallback = useMemo(() => defaultFailedRequest(meta), [meta]);

  return (
    <div
      className={cn(
        'flex h-full flex-col overflow-auto text-foreground',
        className,
      )}
    >
      {/* Header band — same skeleton as the GitHub inspector, generic chip. */}
      <div className="flex flex-col gap-2 border-b border-border px-4 py-3">
        <div className="flex flex-wrap items-center gap-2">
          <span className="flex h-[30px] w-[30px] shrink-0 items-center justify-center overflow-hidden rounded-md border border-border bg-muted/50">
            {connectorMeta ? (
              <ConnectionLogo
                meta={connectorMeta}
                className={cn(
                  'h-4 w-4 object-contain',
                  connectorMeta.mono && '[filter:brightness(0)_invert(1)]',
                )}
              />
            ) : (
              <AlertTriangle size={14} className="text-destructive" aria-hidden />
            )}
          </span>
          <span className="min-w-0 truncate font-mono text-[15px] text-foreground">
            {toolName}
          </span>
          <span className="inline-flex items-center gap-1 rounded-md border border-destructive/30 bg-destructive/10 px-2 py-0.5 text-[11px] font-medium text-destructive">
            <AlertTriangle size={11} aria-hidden /> Error
          </span>
        </div>
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
          <span>{integration ? capitalise(integration) : 'Tool'}</span>
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
              <span className="font-mono">{shortId(callId)}</span>
            </>
          )}
        </div>
      </div>

      {/* Body — same diagnostic vocabulary as the GitHub error branch. */}
      <ErrorDiagnosticBody
        diagnostic={diagnostic}
        rawJson={rawJson}
        failedRequestFallback={failedRequestFallback}
        toolArgs={isObject(meta.tool_args) ? meta.tool_args : null}
      />
    </div>
  );
};

function capitalise(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}

registerCustomResolver((artifact) => {
  if (artifact.metadata?.ok === false && !isGitHubArtifact(artifact)) {
    return {
      component: ErrorRenderer,
      label: 'Error',
      icon: AlertCircle,
    };
  }
  return null;
});

/**
 * GitHub tool errors render through the connector-specific inspector skeleton
 * (GitHubToolRenderer), so the generic error fallback must not claim them —
 * regardless of renderer registration order.
 */
function isGitHubArtifact(artifact: RendererProps['artifact']): boolean {
  const toolName = String(artifact.metadata?.tool_name || '');
  const integration = String(artifact.metadata?.integration || '');
  return integration === 'github' || /^github_/.test(toolName);
}

export default ErrorRenderer;
