import React from 'react';
import { ExternalLink } from 'lucide-react';
import ConnectionLogo from '@/components/chat/ConnectionLogo';
import type { ConnectionMeta } from '@/lib/connectionsCatalog';
import { cn } from '@/lib/utils';
import {
  CopyIcon,
  StatusPill,
  formatClock,
  formatDurationMs,
  shortId,
  type InspectorStatusKind,
} from '../query/atoms';

/**
 * InspectorShell — the shared three-zone inspector skeleton.
 *
 * The header band (logo tile · tool name · StatusPill · meta line) was, until
 * this module existed, hand-rolled identically in the Query, GitHub and Error
 * inspectors. New connector inspectors compose this instead so the whole family
 * stays one product. The only state-coloured element is the StatusPill — every
 * other token holds neutral colour.
 *
 * Layout:
 *   Row A: [logo tile] [tool name] [StatusPill] [optional badge]
 *   Row B: provider · duration · clock · shortId(callId)⧉ · [Open in <provider>]
 *   Body:  children (Request / Response / Metadata zones)
 */

export interface InspectorShellProps {
  /** Connector brand mark; when absent the fallback icon is shown. */
  connectorMeta?: ConnectionMeta | null;
  fallbackIcon?: React.ComponentType<{ size?: number; className?: string }>;
  toolName: string;
  status: InspectorStatusKind;
  /** Provider/product label for the meta line (e.g. "Slack", "Jira"). */
  providerLabel?: string | null;
  durationMs?: number | null;
  endedAtMs?: number | null;
  toolCallId?: string | null;
  /** "Open in <provider>" deep link, when the tool result carries one. */
  consoleUrl?: string | null;
  /** Extra header token, e.g. a write/approval badge. */
  badge?: React.ReactNode;
  className?: string;
  children: React.ReactNode;
}

export function InspectorShell({
  connectorMeta,
  fallbackIcon: FallbackIcon,
  toolName,
  status,
  providerLabel,
  durationMs,
  endedAtMs,
  toolCallId,
  consoleUrl,
  badge,
  className,
  children,
}: InspectorShellProps) {
  const duration = formatDurationMs(durationMs);
  const clock = formatClock(endedAtMs);
  const label = providerLabel || 'Tool';

  return (
    <div className={cn('flex h-full flex-col overflow-auto text-foreground', className)}>
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
            ) : FallbackIcon ? (
              <FallbackIcon size={14} className="text-muted-foreground" />
            ) : null}
          </span>
          <span className="min-w-0 truncate font-mono text-[15px] text-foreground">
            {toolName}
          </span>
          <StatusPill status={status} subject={label} />
          {badge}
        </div>
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
          <span>{label}</span>
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
          {toolCallId && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <span className="inline-flex items-center gap-1">
                <span className="font-mono">{shortId(toolCallId)}</span>
                <CopyIcon value={toolCallId} label="Copy tool call id" />
              </span>
            </>
          )}
          {consoleUrl && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <a
                href={consoleUrl}
                target="_blank"
                rel="noopener noreferrer"
                aria-label={`Open in ${label}`}
                className="inline-flex items-center gap-1 rounded text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
              >
                Open in {label}
                <ExternalLink size={11} aria-hidden />
              </a>
            </>
          )}
        </div>
      </div>
      {children}
    </div>
  );
}

/**
 * InspectorZone — a labelled body section. Uppercase micro-label over content,
 * matching the "Why / Try this" treatment in ErrorDiagnosticBody so the whole
 * family shares one vocabulary.
 */
export function InspectorZone({
  label,
  children,
  className,
  flush = false,
}: {
  label: string;
  children: React.ReactNode;
  className?: string;
  /**
   * Let the body run edge-to-edge (result lists with their own row gutters);
   * the micro-label keeps the family's px-4 alignment either way.
   */
  flush?: boolean;
}) {
  return (
    <section
      className={cn(
        'border-b border-border last:border-b-0',
        flush ? 'pt-4' : 'px-4 py-4',
        className,
      )}
    >
      <div
        className={cn(
          'mb-2 text-[11px] uppercase tracking-wide text-muted-foreground/60',
          flush && 'px-4',
        )}
      >
        {label}
      </div>
      {children}
    </section>
  );
}
