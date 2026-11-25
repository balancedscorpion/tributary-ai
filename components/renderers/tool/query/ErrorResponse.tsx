import React, { useMemo } from 'react';
import { Hash } from 'lucide-react';
import {
  ErrorDiagnosticBody,
  extractToolErrorDiagnostic,
  type ConnectorClassifier,
} from '../errorDiagnostic';
import { CopyIcon, shortId } from './atoms';
import type { Artifact } from '@/types';
import type { QueryInspectorState } from './useQueryInspectorState';

const classifyQueryError: ConnectorClassifier = (message: string) => {
  const m = message.toLowerCase();
  if (/syntax error|parser error|unrecognized name/.test(m)) {
    return {
      kind: 'validation',
      title: 'SQL syntax error',
      why: 'The query was rejected before execution.',
      tryThis: 'Fix the offending token and re-run.',
    };
  }
  if (/permission denied|access denied|forbidden|not authorized/.test(m)) {
    return {
      kind: 'permission',
      title: 'Permission denied',
      why: 'Your credentials are not authorized for one of the referenced objects.',
      tryThis: 'Verify dataset / table access for the connector account.',
    };
  }
  if (/quota|rate limit|exceeded|too many requests/.test(m)) {
    return {
      kind: 'ratelimit',
      title: 'Quota / rate limited',
      why: 'The data source temporarily blocked further work.',
      tryThis: 'Wait briefly and try again.',
    };
  }
  if (/not found|does not exist/.test(m)) {
    return {
      kind: 'notfound',
      title: 'Not found',
      why: 'The referenced object does not exist or is not visible to your credentials.',
      tryThis: 'Verify the project / catalog / schema / table names.',
    };
  }
  return {};
};

interface ErrorResponseProps {
  state: QueryInspectorState;
  /** Artifact carrying the diagnostic metadata; required for the shared body. */
  artifact: Artifact;
}

/**
 * ErrorResponse — slots the existing shared diagnostic body into the
 * unified inspector's response zone. The Query/Request zone above already
 * shows the SQL with the offending line highlighted (state.errorContext);
 * this component only handles context chips + ErrorDiagnosticBody.
 */
export function ErrorResponse({ state, artifact }: ErrorResponseProps) {
  const diagnostic = useMemo(
    () => extractToolErrorDiagnostic(artifact, { classifier: classifyQueryError }),
    [artifact],
  );
  const { stats, sql, toolName, rawJson } = state;
  const meta = (artifact.metadata || {}) as Record<string, unknown>;
  const toolArgs = (meta.tool_args && typeof meta.tool_args === 'object' && !Array.isArray(meta.tool_args))
    ? (meta.tool_args as Record<string, unknown>)
    : null;

  return (
    <div className="flex flex-col gap-3">
      {(stats.jobId || stats.jobLocation || stats.warehouseId || stats.profileOutcome) && (
        <div className="flex flex-wrap items-center gap-1.5">
          {stats.jobId && (
            <span className="inline-flex items-center gap-1 rounded-md border border-border bg-muted/40 px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
              <Hash size={10} aria-hidden />
              {`job ${shortId(stats.jobId)}`}
              <CopyIcon value={stats.jobId} label="Copy job id" />
            </span>
          )}
          {stats.jobLocation && (
            <span className="inline-flex items-center gap-1 rounded-md border border-border bg-muted/40 px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
              {`region ${stats.jobLocation}`}
            </span>
          )}
          {(stats.warehouseName || stats.warehouseId) && (
            <span className="inline-flex items-center gap-1 rounded-md border border-border bg-muted/40 px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
              {`warehouse ${stats.warehouseName || shortId(stats.warehouseId)}`}
            </span>
          )}
          {stats.profileOutcome && (
            <span className="inline-flex items-center gap-1 rounded-md border border-border bg-muted/40 px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
              {`profile ${stats.profileOutcome}`}
            </span>
          )}
          {stats.profileEstimatedBytes && (
            <span className="inline-flex items-center gap-1 rounded-md border border-border bg-muted/40 px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
              {`${stats.profileEstimatedBytes} estimated${stats.profileEstimateIsExact ? ' (exact)' : ''}`}
            </span>
          )}
          {stats.remoteSubmitted === false && (
            <span className="inline-flex items-center gap-1 rounded-md border border-border bg-muted/40 px-2 py-0.5 text-[11px] text-muted-foreground">
              not submitted remotely
            </span>
          )}
        </div>
      )}

      <ErrorDiagnosticBody
        diagnostic={diagnostic}
        rawJson={rawJson}
        failedRequestFallback={sql ? null : `${toolName}({ ... })`}
        toolArgs={toolArgs}
      />
    </div>
  );
}
