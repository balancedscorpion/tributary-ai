import { useRef } from 'react';
import type { Artifact, QueryMetadata, TrackedQuery } from '@/types';
import { getQueryProviderForToolName, normalizeQueryProvider } from '@/lib/queryProviderRegistry';
import { getSharedQueryStatusKey, type SharedQueryStatusSnapshot } from '@/hooks/useSharedQueryStatus';
import { isActiveQueryStatus } from '@/lib/queryProgress';
import { normalizeManagedQueryJobId } from '@/lib/managedQueryJobId';

const MAX_RECENT = 10;
const MAX_AGE_MS = 30 * 60 * 1000; // 30 minutes

function asString(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined;
  const trimmed = value.trim();
  return trimmed || undefined;
}

function asNumber(value: unknown): number | undefined {
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  if (typeof value === 'string' && value.trim()) {
    const parsed = Number(value);
    if (Number.isFinite(parsed)) return parsed;
  }
  return undefined;
}

function firstString(...values: unknown[]): string | undefined {
  for (const value of values) {
    const parsed = asString(value);
    if (parsed) return parsed;
  }
  return undefined;
}

function normalizeThreadId(value: string | null | undefined): string | null {
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  return trimmed || null;
}

function normalizeQueryObligationId(value: unknown): string | undefined {
  const normalized = asString(value);
  if (!normalized || !/^qob-[0-9a-f]{32}$/.test(normalized)) return undefined;
  return normalized === `qob-${'0'.repeat(32)}` ? undefined : normalized;
}

function matchesThreadScope(
  queryThreadId: string | undefined,
  activeThreadId: string | null,
  hasScope: boolean,
): boolean {
  if (!hasScope) return true;
  if (!activeThreadId) return false;
  return normalizeThreadId(queryThreadId) === activeThreadId;
}

function parseQueryJobStatus(value: unknown): TrackedQuery['status'] | undefined {
  if (typeof value !== 'string') return undefined;
  const normalized = value.trim().toLowerCase();
  if (!normalized) return undefined;
  if (normalized === 'reserved') return 'reserved';
  if (normalized === 'profiling') return 'profiling';
  if (normalized === 'submission_unknown') return 'submission_unknown';
  if (normalized === 'running') return 'running';
  if (normalized === 'submitted' || normalized === 'queued' || normalized === 'pending') return 'submitted';
  if (normalized === 'cancel_requested') return 'cancel_requested';
  if (normalized === 'cancelling') return 'cancelling';
  if (
    normalized === 'completed'
    || normalized === 'complete'
    || normalized === 'done'
    || normalized === 'succeeded'
    || normalized === 'success'
    || normalized === 'finished'
  ) return 'completed';
  if (normalized === 'failed' || normalized === 'error' || normalized === 'errored') return 'failed';
  if (normalized === 'cancelled' || normalized === 'canceled' || normalized === 'cancel') return 'cancelled';
  if (normalized === 'expired') return 'expired';
  if (normalized === 'orphaned' || normalized === 'lost') return 'orphaned';
  if (normalized === 'unknown') return 'unknown';
  return undefined;
}

function isTerminalStatus(status: TrackedQuery['status']): boolean {
  return status === 'completed'
    || status === 'failed'
    || status === 'cancelled'
    || status === 'expired'
    || status === 'orphaned';
}

/**
 * Derives running and recently-completed queries from the global artifacts map.
 *
 * Running queries are artifacts with `metadata.async_query === true && metadata.placeholder === true`.
 * When a query completes, its placeholder is replaced with real data (`placeholder: false`).
 * This hook tracks that transition and keeps a short history of recent completions.
 *
 * `liveStatusByKey` (snapshots from useSharedQueryStatuses, keyed by
 * getSharedQueryStatusKey) lets live polling participate in the running →
 * terminal transition. Without it the monitor only reacts to artifact
 * metadata, so a job whose completion was observed by polling but whose
 * placeholder had not hydrated yet kept counting as "running" in the task
 * bar header.
 */
export function useQueryMonitor(
  artifactsById: Record<string, Artifact>,
  activeThreadIdInput?: string | null,
  liveStatusByKey?: Record<string, SharedQueryStatusSnapshot>,
) {
  const hasScope = activeThreadIdInput !== undefined;
  const activeThreadId = normalizeThreadId(activeThreadIdInput);
  const trackerRef = useRef<Map<string, TrackedQuery>>(new Map());
  const tracker = trackerRef.current;
  const now = Date.now();
  for (const [artifactId, tracked] of tracker.entries()) {
    if (!normalizeManagedQueryJobId(tracked.jobId)) {
      tracker.delete(artifactId);
    }
  }
  if (hasScope && !activeThreadId) {
    return {
      runningQueries: [],
      recentQueries: [],
      totalRunning: 0,
    };
  }

  // 1. Scan artifacts for new running queries.
  for (const [artifactId, artifact] of Object.entries(artifactsById)) {
    const meta = artifact.metadata;
    if (meta?.async_query !== true || meta?.placeholder !== true) continue;
    if (tracker.has(artifactId)) continue;

    const queryMetadata = meta.query_metadata as QueryMetadata | undefined;
    const queryThreadId = firstString(meta.thread_id, queryMetadata?.thread_id);
    const queryRunId = firstString(meta.run_id, queryMetadata?.run_id);
    const queryObligationId = normalizeQueryObligationId(
      meta.query_obligation_id ?? queryMetadata?.query_obligation_id,
    );
    if (!matchesThreadScope(queryThreadId, activeThreadId, hasScope)) continue;

    // Deduplicate within the full run-scoped status identity. A concrete job
    // may be attached to multiple runs with a different obligation in each.
    const jobId = normalizeManagedQueryJobId(meta.job_id)
      || normalizeManagedQueryJobId(queryMetadata?.job_id);
    if (!jobId) continue;
    let alreadyTracked = false;
    for (const existing of tracker.values()) {
      if (
        existing.jobId === jobId
        && existing.runId === queryRunId
        && existing.queryObligationId === queryObligationId
        && matchesThreadScope(existing.threadId, activeThreadId, hasScope)
      ) {
        alreadyTracked = true;
        break;
      }
    }
    if (alreadyTracked) continue;
    const provider = normalizeQueryProvider(
      meta.provider
      ?? queryMetadata?.provider
      ?? (typeof meta.tool_name === 'string' ? getQueryProviderForToolName(meta.tool_name) : null)
      ?? undefined,
      'bigquery',
    );

    tracker.set(artifactId, {
      artifactId,
      jobId,
      provider,
      description: firstString(meta.description, queryMetadata?.description),
      sql: firstString(meta.sql, queryMetadata?.sql),
      sqlPreview: firstString(meta.sql_preview, queryMetadata?.sql_preview),
      ownerToken: asString(meta.owner_token ?? queryMetadata?.owner_token),
      threadId: queryThreadId,
      runId: queryRunId,
      queryObligationId,
      status: parseQueryJobStatus(meta.status ?? queryMetadata?.status) ?? 'running',
      startedAtMs: asNumber(meta._uiStartedAt) ?? now,
      queryMetadata,
    });
  }

  // 2. Detect transitions for tracked running queries.
  for (const [artifactId, tracked] of tracker.entries()) {
    if (!isActiveQueryStatus(tracked.status)) continue;

    const artifact = artifactsById[artifactId];
    if (!artifact) {
      const liveKey = getSharedQueryStatusKey(
        tracked.jobId,
        tracked.threadId ?? activeThreadId ?? '',
        tracked.runId,
        tracked.queryObligationId,
      );
      const live = liveKey && liveStatusByKey ? liveStatusByKey[liveKey] : undefined;
      const liveStatus = parseQueryJobStatus(live?.status);
      if (liveStatus && isTerminalStatus(liveStatus)) {
        tracked.status = liveStatus;
        tracked.completedAtMs = tracked.completedAtMs ?? now;
        tracked.durationMs = asNumber(live?.elapsed_ms)
          ?? tracked.durationMs
          ?? Math.max(0, tracked.completedAtMs - tracked.startedAtMs);
      } else {
        tracked.status = liveStatus ?? 'unknown';
      }
      continue;
    }

    const meta = artifact.metadata;
    const queryMetadata = meta?.query_metadata as QueryMetadata | undefined;
    tracked.queryMetadata = queryMetadata ?? tracked.queryMetadata;
    tracked.threadId = firstString(
      tracked.threadId,
      meta?.thread_id,
      queryMetadata?.thread_id,
    );
    tracked.runId = firstString(
      tracked.runId,
      meta?.run_id,
      queryMetadata?.run_id,
    );
    tracked.queryObligationId = tracked.queryObligationId ?? normalizeQueryObligationId(
      meta?.query_obligation_id ?? queryMetadata?.query_obligation_id,
    );
    tracked.description = firstString(
      tracked.description,
      meta?.description,
      queryMetadata?.description,
    );
    tracked.sql = firstString(
      tracked.sql,
      meta?.sql,
      queryMetadata?.sql,
    );
    tracked.sqlPreview = firstString(
      tracked.sqlPreview,
      meta?.sql_preview,
      queryMetadata?.sql_preview,
    );
    tracked.ownerToken = tracked.ownerToken ?? asString(meta?.owner_token ?? queryMetadata?.owner_token);

    const statusFromMetadata = parseQueryJobStatus(meta?.status ?? queryMetadata?.status);
    if (statusFromMetadata && isTerminalStatus(statusFromMetadata)) {
      tracked.status = statusFromMetadata;
      tracked.completedAtMs = tracked.completedAtMs ?? now;
      tracked.durationMs = asNumber(queryMetadata?.duration_ms)
        ?? tracked.durationMs
        ?? Math.max(0, tracked.completedAtMs - tracked.startedAtMs);
      tracked.rowsReturned = asNumber(
        queryMetadata?.rows_returned
        ?? queryMetadata?.row_count
        ?? meta?.rows_returned
        ?? meta?.row_count,
      );
      continue;
    }

    // Live polling observed a terminal status before the artifact hydrated
    // (placeholder swap pending). Trust it: artifact metadata stays
    // authoritative above, but a completed/failed/cancelled poll must clear
    // the running state even if hydration lags or fails.
    if (liveStatusByKey) {
      const liveKey = getSharedQueryStatusKey(
        tracked.jobId,
        tracked.threadId ?? activeThreadId ?? '',
        tracked.runId,
        tracked.queryObligationId,
      );
      const live = liveKey ? liveStatusByKey[liveKey] : undefined;
      const liveStatus = parseQueryJobStatus(live?.status);
      if (liveStatus && isTerminalStatus(liveStatus)) {
        tracked.status = liveStatus;
        tracked.completedAtMs = tracked.completedAtMs ?? now;
        tracked.durationMs = asNumber(live?.elapsed_ms)
          ?? tracked.durationMs
          ?? Math.max(0, tracked.completedAtMs - tracked.startedAtMs);
        continue;
      }
    }

    if (meta?.placeholder !== false) continue;

    tracked.status = meta.ok === false ? 'failed' : 'completed';
    tracked.completedAtMs = tracked.completedAtMs ?? now;
    tracked.durationMs = asNumber(queryMetadata?.duration_ms)
      ?? tracked.durationMs
      ?? Math.max(0, tracked.completedAtMs - tracked.startedAtMs);
    tracked.rowsReturned = asNumber(
      queryMetadata?.rows_returned
      ?? queryMetadata?.row_count
      ?? meta.rows_returned
      ?? meta.row_count,
    );
  }

  // 3. Prune completed entries older than 30 minutes.
  for (const [artifactId, tracked] of tracker.entries()) {
    if (!tracked.completedAtMs) continue;
    if (now - tracked.completedAtMs > MAX_AGE_MS) {
      tracker.delete(artifactId);
    }
  }

  // 4. Return sorted lists and enforce "recent" cap.
  const runningQueries: TrackedQuery[] = [];
  const recentAll: TrackedQuery[] = [];
  for (const tracked of tracker.values()) {
    if (!matchesThreadScope(tracked.threadId, activeThreadId, hasScope)) continue;
    if (isActiveQueryStatus(tracked.status)) {
      runningQueries.push(tracked);
    } else if (tracked.completedAtMs) {
      recentAll.push(tracked);
    }
  }

  runningQueries.sort((a, b) => a.startedAtMs - b.startedAtMs);
  recentAll.sort((a, b) => (b.completedAtMs ?? 0) - (a.completedAtMs ?? 0));

  const recentQueries = recentAll.slice(0, MAX_RECENT);
  if (recentAll.length > MAX_RECENT) {
    for (const stale of recentAll.slice(MAX_RECENT)) {
      tracker.delete(stale.artifactId);
    }
  }

  return {
    runningQueries,
    recentQueries,
    totalRunning: runningQueries.length,
  };
}
