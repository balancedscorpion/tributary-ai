import React from 'react';
import { HardDrive, Zap } from 'lucide-react';
import { CopyIcon, Disclosure, Stat, formatDurationMs, shortId } from './atoms';
import { Explorer, type SchemaField } from './Explorer';
import { useQueryExplorer } from './useQueryExplorer';
import type { Artifact } from '@/types';
import type { QueryInspectorState } from './useQueryInspectorState';

interface CompletedResponseProps {
  state: QueryInspectorState;
  /**
   * Artifact to drive the Explorer. Required — the completed response
   * cannot render without a materialised artifact (rows + schema). Pass
   * `state.resolvedArtifact` and the parent should fall back to a thin
   * "Completed (no preview)" card when missing.
   */
  artifact: Artifact;
  /** Show the raw envelope disclosure (artifact-panel variant only). */
  showRawEnvelope?: boolean;
}

/**
 * CompletedResponse — wraps the existing Explorer with the inspector's
 * stats row above it. The Explorer is unchanged; this component only adds
 * the stats summary and the optional raw-envelope disclosure for the
 * artifact-panel variant.
 */
export function CompletedResponse({
  state,
  artifact,
  showRawEnvelope = false,
}: CompletedResponseProps) {
  const dataSource = useQueryExplorer(artifact);
  const { stats, providerLabel, rawJson, schema } = state;
  const duration = formatDurationMs(stats.durationMs);
  const explorerSchema: SchemaField[] | undefined = schema;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        {stats.rowsReturned != null && (
          <Stat
            label={`${stats.rowsReturned.toLocaleString()} ${stats.rowsReturned === 1 ? 'row' : 'rows'}`}
          />
        )}
        {stats.totalRowsKnown != null &&
          stats.rowsReturned != null &&
          stats.totalRowsKnown > stats.rowsReturned && (
            <Stat
              label={`of ${stats.totalRowsKnown.toLocaleString()} total`}
              mono
            />
          )}
        {stats.bytesProcessed && (
          <Stat
            icon={<HardDrive size={11} aria-hidden />}
            label={`${stats.bytesProcessed} processed`}
          />
        )}
        {stats.cacheHit && (
          <Stat
            icon={<Zap size={11} aria-hidden className="text-emerald-600" />}
            label="cache hit"
            highlight
          />
        )}
        {duration && <Stat label={duration} mono />}
        {stats.jobId && (
          <span className="inline-flex items-center gap-1 font-mono text-[11px] text-muted-foreground">
            {`job ${shortId(stats.jobId)}`}
            <CopyIcon value={stats.jobId} label="Copy job id" />
          </span>
        )}
        {stats.jobLocation && (
          <span className="font-mono text-[11px] text-muted-foreground">
            {`region ${stats.jobLocation}`}
          </span>
        )}
        {(stats.warehouseName || stats.warehouseId) && (
          <span className="font-mono text-[11px] text-muted-foreground">
            {`warehouse ${stats.warehouseName || shortId(stats.warehouseId)}`}
          </span>
        )}
        {stats.materializationState && stats.materializationState !== 'none' && (
          <Stat label={`results ${stats.materializationState}`} />
        )}
        {stats.validationState && <Stat label={`validation ${stats.validationState}`} />}
        {stats.resultTruncated && <Stat label="result capped" />}
      </div>

      <div>
        <div className="mb-2 text-xs text-muted-foreground/60">Results</div>
        <Explorer
          rows={dataSource.rows}
          schema={explorerSchema}
          totalRows={dataSource.totalRows}
          isLive={dataSource.isLive}
          hasMore={dataSource.hasMore}
          loading={dataSource.loading}
          error={dataSource.error}
          onLoadMore={dataSource.loadMore}
          onRefresh={dataSource.refresh}
          providerLabel={providerLabel}
        />
      </div>

      {showRawEnvelope && (
        <Disclosure label="Raw envelope">
          <pre className="font-mono text-[12px] whitespace-pre-wrap [overflow-wrap:anywhere] rounded-md border border-border bg-muted/40 px-3 py-2.5 text-foreground">
            {rawJson}
          </pre>
        </Disclosure>
      )}
    </div>
  );
}
