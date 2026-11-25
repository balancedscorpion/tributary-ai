import {
  getSharedQueryStatusKey,
  type SharedQueryStatusTarget,
} from '@/hooks/useSharedQueryStatus';
import type { Artifact } from '@/types';
import { normalizeManagedQueryJobId } from '@/lib/managedQueryJobId';

/**
 * Shared derivation of "which async query jobs should we watch" from the
 * artifacts map. Used by every surface that needs live job status (task bar
 * running count, completion hydration) so they all observe the exact same
 * job set through the refcounted shared polling store — no surface ever
 * spins up its own polling loop.
 */

export interface AsyncQueryPollTarget extends SharedQueryStatusTarget {
  artifactId: string;
  jobId: string;
  statusKey: string;
}

function asTrimmedString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function asQueryObligationID(value: unknown): string {
  const normalized = asTrimmedString(value);
  if (!/^qob-[0-9a-f]{32}$/.test(normalized)) return '';
  return normalized === `qob-${'0'.repeat(32)}` ? '' : normalized;
}

export function collectAsyncQueryPollTargets(
  artifactsById: Record<string, Artifact>,
  fallbackThreadId?: string | null,
): AsyncQueryPollTarget[] {
  const fallback = asTrimmedString(fallbackThreadId);
  const byKey = new Map<string, AsyncQueryPollTarget>();
  for (const [artifactId, artifact] of Object.entries(artifactsById)) {
    const meta = (artifact?.metadata || {}) as Record<string, unknown>;
    if (meta.async_query !== true || meta.placeholder !== true) continue;
    const qm = (meta.query_metadata || {}) as Record<string, unknown>;
    const jobId = normalizeManagedQueryJobId(meta.job_id) || normalizeManagedQueryJobId(qm.job_id);
    if (!jobId) continue;
    const threadId =
      asTrimmedString(meta.thread_id) || asTrimmedString(qm.thread_id) || fallback;
    const ownerToken =
      asTrimmedString(meta.owner_token) || asTrimmedString(qm.owner_token);
    const runId = asTrimmedString(meta.run_id) || asTrimmedString(qm.run_id);
    const queryObligationId =
      asQueryObligationID(meta.query_obligation_id) || asQueryObligationID(qm.query_obligation_id);
    const statusKey = getSharedQueryStatusKey(jobId, threadId, runId, queryObligationId);
    if (!statusKey || byKey.has(statusKey)) continue;
    const startedAt = meta._uiStartedAt;
    byKey.set(statusKey, {
      artifactId,
      jobId,
      statusKey,
      threadId: threadId || undefined,
      runId: runId || undefined,
      ownerToken: ownerToken || undefined,
      queryObligationId: queryObligationId || undefined,
      startedAtMs: typeof startedAt === 'number' ? startedAt : undefined,
    });
  }
  return Array.from(byKey.values());
}
