import React from 'react';
import { Database, ExternalLink } from 'lucide-react';
import ConnectionLogo from '@/components/chat/ConnectionLogo';
import { cn } from '@/lib/utils';
import {
  CopyIcon,
  StatusPill,
  formatClock,
  formatDurationMs,
  shortId,
  statusKindFor,
} from './atoms';
import type { QueryInspectorState } from './useQueryInspectorState';

interface QueryInspectorHeaderProps {
  state: QueryInspectorState;
  className?: string;
}

/**
 * QueryInspectorHeader — top band of the inspector. Layout:
 *   Row A: [logo tile] [tool name] [status pill]
 *   Row B: [provider · duration · clock · jobId(copy) · region · warehouse]
 *           [Open in <provider> link]
 *
 * Identical visual chrome across running / completed / error / cancelled. The
 * only state-coloured element is the StatusPill — every other token holds
 * neutral colour so the inspector reads as one family.
 */
export function QueryInspectorHeader({ state, className }: QueryInspectorHeaderProps) {
  const {
    providerLabel,
    connectorMeta,
    toolName,
    toolCallId,
    status,
    stats,
    consoleUrl,
    endedAtMs,
  } = state;

  const duration = formatDurationMs(stats.durationMs);
  const clock = formatClock(endedAtMs);
  const pillStatus = statusKindFor(status);
  const integrationLabel = providerLabel || 'Query';

  return (
    <div className={cn('flex flex-col gap-2 border-b border-border px-4 py-3', className)}>
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
            <Database size={14} className="text-muted-foreground" aria-hidden />
          )}
        </span>
        <span className="min-w-0 truncate font-mono text-[15px] text-foreground">
          {toolName}
        </span>
        <StatusPill status={pillStatus} />
      </div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
        <span>{integrationLabel}</span>
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
              aria-label={`Open in ${integrationLabel}`}
              className="inline-flex items-center gap-1 rounded text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
            >
              Open in {integrationLabel}
              <ExternalLink size={11} aria-hidden />
            </a>
          </>
        )}
      </div>
    </div>
  );
}
