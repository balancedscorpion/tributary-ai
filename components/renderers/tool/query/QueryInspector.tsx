import React from 'react';
import { Loader2 } from 'lucide-react';
import { cn } from '@/lib/utils';
import type { Artifact } from '@/types';
import { QueryInspectorHeader } from './QueryInspectorHeader';
import { QueryRequestZone } from './QueryRequestZone';
import { RunningResponse } from './RunningResponse';
import { CompletedResponse } from './CompletedResponse';
import { ErrorResponse } from './ErrorResponse';
import { CancelledResponse } from './CancelledResponse';
import { useQueryInspectorState } from './useQueryInspectorState';

interface QueryInspectorProps {
  /** Materialised artifact — placeholder while an async job runs, the
      loaded results artifact once get_query_results lands. */
  artifact?: Artifact;
  /** Raw tool_result payload, when the artifact panel has one. */
  toolResult?: unknown;
  className?: string;
}

/**
 * QueryInspector — the artifact-panel detail surface for every query-tool
 * artifact. Three-zone structure: header / query / response; the response
 * zone is the only state-aware zone.
 *
 * Chat does not render this inline any more — the compact
 * QueryExecutionCard summarises the query in the stream and opens this
 * panel on click. Running/polling placeholder artifacts render the
 * RunningResponse ("results will appear when the job completes"); when the
 * placeholder is replaced by the loaded artifact the same panel shows the
 * explorer. State derivation is shared with the chat card through
 * useQueryInspectorState.
 *
 * The collapsed "Raw envelope" disclosure under the completed response is
 * panel-only by design — engineers occasionally need the exact envelope.
 */
export function QueryInspector({ artifact, toolResult, className }: QueryInspectorProps) {
  const state = useQueryInspectorState({ artifact, toolResult });

  const { phase, sql, description, errorContext, resolvedArtifact } = state;

  // Error mode: keep the response zone simple; the Query zone above already
  // highlights the offending line.
  const errorLine = phase === 'failed' ? errorContext.errorLine ?? null : null;

  return (
    <div className={cn('flex h-full flex-col overflow-auto text-foreground', className)}>
      <QueryInspectorHeader state={state} />
      <div
        className={cn(
          'flex flex-col gap-4 px-4 py-4',
          // Tight stacking for error mode so the diagnostic doesn't read as
          // a separate report.
          phase === 'failed' && 'gap-3',
        )}
      >
        <QueryRequestZone
          description={description}
          sql={sql}
          highlightLine={errorLine}
        />

        {phase === 'executing' || phase === 'polling' ? (
          <RunningResponse state={state} />
        ) : phase === 'cancelled' ? (
          <CancelledResponse state={state} />
        ) : phase === 'failed' ? (
          resolvedArtifact || artifact ? (
            <ErrorResponse state={state} artifact={(resolvedArtifact || artifact) as Artifact} />
          ) : (
            <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-[12px] text-destructive">
              {errorContext.errorMessage || 'Query execution failed.'}
            </div>
          )
        ) : resolvedArtifact ? (
          <CompletedResponse state={state} artifact={resolvedArtifact} showRawEnvelope />
        ) : (
          // Completed but the result artifact hasn't hydrated yet (placeholder
          // swap pending) — useQueryCompletionHydration is fetching it; show a
          // loading state instead of a dead-end card.
          <div
            role="status"
            aria-live="polite"
            className="flex items-center gap-2 rounded-md border border-border bg-muted/30 px-3 py-2 text-[12px] text-muted-foreground"
          >
            <Loader2 size={13} aria-hidden className="animate-spin" />
            Query completed. Loading results…
          </div>
        )}
      </div>
    </div>
  );
}

export default QueryInspector;
