import { useEffect, useMemo, useState } from 'react';
import { getQueryPollDelay, isTerminalQueryStatus, mergeQueryStatusSnapshot } from '@/lib/queryProgress';
import { normalizeManagedQueryJobId } from '@/lib/managedQueryJobId';
import type { QueryJobStatusResponse } from '@/types';

export type SharedQueryStatusSnapshot = Partial<QueryJobStatusResponse> & {
  client_poll_error?: string;
  client_poll_failures?: number;
};

export interface SharedQueryStatusTarget {
  jobId?: string;
  threadId?: string;
  runId?: string;
  ownerToken?: string;
  queryObligationId?: string;
  startedAtMs?: number;
  enabled?: boolean;
}

interface NormalizedTarget {
  key: string;
  jobId: string;
  threadId?: string;
  runId?: string;
  ownerToken?: string;
  queryObligationId?: string;
  startedAtMs: number;
}

interface QueryStatusEntry {
  key: string;
  jobId: string;
  threadId?: string;
  runId?: string;
  ownerToken?: string;
  queryObligationId?: string;
  startedAtMs: number;
  snapshot: SharedQueryStatusSnapshot;
  listeners: Set<(snapshot: SharedQueryStatusSnapshot) => void>;
  timer: ReturnType<typeof setTimeout> | null;
  inFlight: boolean;
  consecutiveFailures: number;
}

const entryByKey = new Map<string, QueryStatusEntry>();

const STATUS_REQUEST_TIMEOUT_MS = 15_000;
const MAX_FAILURE_BACKOFF_MS = 60_000;

function normalizeToken(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function normalizeQueryObligationID(value: unknown): string | undefined {
  const normalized = normalizeToken(value);
  if (!/^qob-[0-9a-f]{32}$/.test(normalized)) return undefined;
  if (normalized === `qob-${'0'.repeat(32)}`) return undefined;
  return normalized;
}

function normalizeStartedAt(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : Date.now();
}

export function getSharedQueryStatusKey(
  jobId: unknown,
  threadId: unknown,
  runId?: unknown,
  queryObligationId?: unknown,
): string | null {
  const normalizedJobID = normalizeManagedQueryJobId(jobId);
  if (!normalizedJobID) return null;
  const normalizedThreadID = normalizeToken(threadId);
  const normalizedRunID = normalizeToken(runId);
  const normalizedQueryObligationID = normalizedRunID
    ? normalizeQueryObligationID(queryObligationId) || ''
    : '';
  return JSON.stringify([
    normalizedThreadID,
    normalizedRunID,
    normalizedQueryObligationID,
    normalizedJobID,
  ]);
}

function normalizeTarget(input: SharedQueryStatusTarget | null | undefined): NormalizedTarget | null {
  if (!input || input.enabled === false) return null;
  const normalizedJobID = normalizeManagedQueryJobId(input.jobId);
  if (!normalizedJobID) return null;
  const normalizedThreadID = normalizeToken(input.threadId);
  const normalizedRunID = normalizeToken(input.runId);
  const normalizedOwnerToken = normalizeToken(input.ownerToken);
  // Legacy artifacts can have a qob but no run id. A qob is meaningful only
  // inside its requesting run, so poll those artifacts through the unscoped
  // compatibility API instead of sending an unverifiable identity.
  const normalizedQueryObligationID = normalizedRunID
    ? normalizeQueryObligationID(input.queryObligationId)
    : undefined;
  const key = getSharedQueryStatusKey(
    normalizedJobID,
    normalizedThreadID,
    normalizedRunID,
    normalizedQueryObligationID,
  );
  if (!key) return null;
  return {
    key,
    jobId: normalizedJobID,
    threadId: normalizedThreadID || undefined,
    runId: normalizedRunID || undefined,
    ownerToken: normalizedOwnerToken || undefined,
    queryObligationId: normalizedQueryObligationID,
    startedAtMs: normalizeStartedAt(input.startedAtMs),
  };
}

function notifyEntry(entry: QueryStatusEntry) {
  for (const listener of entry.listeners) {
    listener(entry.snapshot);
  }
}

function pickElapsedMs(snapshot: SharedQueryStatusSnapshot, startedAtMs: number): number {
  const raw = snapshot.elapsed_ms;
  if (typeof raw === 'number' && Number.isFinite(raw) && raw >= 0) return raw;
  return Math.max(0, Date.now() - startedAtMs);
}

function scheduleEntryPoll(entry: QueryStatusEntry, delayMs: number) {
  if (entry.timer) {
    clearTimeout(entry.timer);
    entry.timer = null;
  }
  if (entry.listeners.size === 0) return;
  entry.timer = setTimeout(() => {
    void pollEntry(entry.key);
  }, Math.max(1000, delayMs));
}

async function pollEntry(key: string) {
  const entry = entryByKey.get(key);
  if (!entry || entry.inFlight || entry.listeners.size === 0) return;
  if (isTerminalQueryStatus(entry.snapshot.status)) return;

  entry.inFlight = true;
  try {
    const qs = new URLSearchParams();
    if (entry.threadId) qs.set('thread_id', entry.threadId);
    if (entry.runId) qs.set('run_id', entry.runId);
    if (entry.queryObligationId) qs.set('query_obligation_id', entry.queryObligationId);
    const suffix = qs.toString();
    const statusURL = `/api/queries/${encodeURIComponent(entry.jobId)}/status${suffix ? `?${suffix}` : ''}`;
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (entry.ownerToken) headers['x-query-owner-token'] = entry.ownerToken;

    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), STATUS_REQUEST_TIMEOUT_MS);
    let response: Response;
    try {
      response = await fetch(statusURL, { method: 'GET', headers, signal: controller.signal });
    } finally {
      clearTimeout(timeout);
    }
    if (!response.ok) {
      recordEntryPollFailure(entry, `Query status request failed (${response.status}).`);
      return;
    }
    const payload = await response.json() as QueryJobStatusResponse;
    const payloadJobID = normalizeManagedQueryJobId(payload.job_id);
    if (payloadJobID !== entry.jobId) {
      recordEntryPollFailure(entry, 'Query status returned a different job identity.');
      return;
    }
    const payloadRunID = normalizeToken(payload.run_id);
    const rawPayloadObligationID = normalizeToken(payload.query_obligation_id);
    const payloadObligationID = normalizeQueryObligationID(rawPayloadObligationID);
    if (rawPayloadObligationID && !payloadObligationID) {
      recordEntryPollFailure(entry, 'Query status returned an invalid query obligation identity.');
      return;
    }
    if (entry.runId) {
      if (payloadRunID !== entry.runId) {
        recordEntryPollFailure(entry, 'Query status returned a different run identity.');
        return;
      }
      if (!payloadObligationID) {
        recordEntryPollFailure(entry, 'Query status did not return a verified query obligation identity.');
        return;
      }
    } else if (payloadRunID || payloadObligationID) {
      recordEntryPollFailure(entry, 'Query status returned an unscoped query obligation identity.');
      return;
    }
    if (
      entry.queryObligationId
      && entry.queryObligationId !== payloadObligationID
    ) {
      recordEntryPollFailure(entry, 'Query status returned a different query obligation identity.');
      return;
    }
    if (!entry.queryObligationId && payloadObligationID) {
      entry.queryObligationId = payloadObligationID;
    }
    entry.consecutiveFailures = 0;

    const nextSnapshot = {
      ...mergeQueryStatusSnapshot(entry.snapshot, payload, entry.startedAtMs),
      client_poll_error: undefined,
      client_poll_failures: 0,
    };

    const changed =
      entry.snapshot.status !== nextSnapshot.status
      || entry.snapshot.progress_percent !== nextSnapshot.progress_percent
      || entry.snapshot.progress_pct !== nextSnapshot.progress_pct
      || entry.snapshot.elapsed_ms !== nextSnapshot.elapsed_ms
      || entry.snapshot.error !== nextSnapshot.error
      || entry.snapshot.run_id !== nextSnapshot.run_id
      || entry.snapshot.query_obligation_id !== nextSnapshot.query_obligation_id
      || entry.snapshot.provider_job_id !== nextSnapshot.provider_job_id
      || entry.snapshot.provider_job_url !== nextSnapshot.provider_job_url
      || entry.snapshot.statement_id !== nextSnapshot.statement_id
      || entry.snapshot.query_id !== nextSnapshot.query_id
      || entry.snapshot.project_id !== nextSnapshot.project_id
      || entry.snapshot.account_identifier !== nextSnapshot.account_identifier
      || entry.snapshot.job_location !== nextSnapshot.job_location
      || entry.snapshot.location !== nextSnapshot.location
      || entry.snapshot.host !== nextSnapshot.host
      || entry.snapshot.url !== nextSnapshot.url
      || entry.snapshot.warehouse_job_id !== nextSnapshot.warehouse_job_id
      || entry.snapshot.monitor_health !== nextSnapshot.monitor_health
      || entry.snapshot.policy_health !== nextSnapshot.policy_health
      || entry.snapshot.materialization_state !== nextSnapshot.materialization_state
      || entry.snapshot.validation_state !== nextSnapshot.validation_state
      || entry.snapshot.remote_submitted !== nextSnapshot.remote_submitted
      || entry.snapshot.profile_outcome !== nextSnapshot.profile_outcome
      || entry.snapshot.query_profile !== nextSnapshot.query_profile
      || entry.snapshot.profile !== nextSnapshot.profile
      || entry.snapshot.profile_handle !== nextSnapshot.profile_handle
      || entry.snapshot.profile_phase !== nextSnapshot.profile_phase
      || entry.snapshot.profile_dispatched !== nextSnapshot.profile_dispatched
      || entry.snapshot.cancel_outcome !== nextSnapshot.cancel_outcome
      || entry.snapshot.queued_elapsed_ms !== nextSnapshot.queued_elapsed_ms
      || entry.snapshot.execution_elapsed_ms !== nextSnapshot.execution_elapsed_ms
      || entry.snapshot.telemetry_age_ms !== nextSnapshot.telemetry_age_ms
      || entry.snapshot.bytes_processed !== nextSnapshot.bytes_processed
      || entry.snapshot.bytes_billed !== nextSnapshot.bytes_billed
      || entry.snapshot.rows_processed !== nextSnapshot.rows_processed
      || entry.snapshot.result_rows !== nextSnapshot.result_rows
      || entry.snapshot.result_bytes !== nextSnapshot.result_bytes
      || entry.snapshot.result_truncated !== nextSnapshot.result_truncated
      || entry.snapshot.client_poll_error !== nextSnapshot.client_poll_error
      || entry.snapshot.client_poll_failures !== nextSnapshot.client_poll_failures;

    if (changed) {
      entry.snapshot = nextSnapshot;
      notifyEntry(entry);
    } else {
      entry.snapshot = nextSnapshot;
    }

    if (isTerminalQueryStatus(nextSnapshot.status)) {
      if (entry.timer) {
        clearTimeout(entry.timer);
        entry.timer = null;
      }
      return;
    }

    scheduleEntryPoll(entry, getQueryPollDelay(pickElapsedMs(entry.snapshot, entry.startedAtMs)));
  } catch {
    recordEntryPollFailure(entry, 'Query status request failed.');
  } finally {
    entry.inFlight = false;
  }
}

function recordEntryPollFailure(entry: QueryStatusEntry, message: string) {
  entry.consecutiveFailures += 1;
  if (
    entry.snapshot.client_poll_error !== message
    || entry.snapshot.client_poll_failures !== entry.consecutiveFailures
  ) {
    entry.snapshot = {
      ...entry.snapshot,
      client_poll_error: message,
      client_poll_failures: entry.consecutiveFailures,
    };
    notifyEntry(entry);
  }
  const normalDelay = getQueryPollDelay(pickElapsedMs(entry.snapshot, entry.startedAtMs));
  const failureMultiplier = 2 ** Math.min(entry.consecutiveFailures - 1, 6);
  scheduleEntryPoll(entry, Math.min(MAX_FAILURE_BACKOFF_MS, normalDelay * failureMultiplier));
}

function subscribeToTarget(
  target: NormalizedTarget,
  listener: (snapshot: SharedQueryStatusSnapshot) => void,
): () => void {
  const existing = entryByKey.get(target.key);
  const entry: QueryStatusEntry = existing || {
    key: target.key,
    jobId: target.jobId,
    threadId: target.threadId,
    runId: target.runId,
    ownerToken: target.ownerToken,
    queryObligationId: target.queryObligationId,
    startedAtMs: target.startedAtMs,
    // A client-provided obligation is only a request constraint. Do not
    // expose it as observed status until the server verifies the requesting
    // run's active dependency and returns the same identity.
    snapshot: {},
    listeners: new Set(),
    timer: null,
    inFlight: false,
    consecutiveFailures: 0,
  };

  if (!existing) {
    entryByKey.set(target.key, entry);
  } else {
    // Keep the best available identity fields and earliest known start time.
    if (!entry.threadId && target.threadId) entry.threadId = target.threadId;
    if (!entry.runId && target.runId) entry.runId = target.runId;
    if (!entry.ownerToken && target.ownerToken) entry.ownerToken = target.ownerToken;
    if (!entry.queryObligationId && target.queryObligationId) {
      entry.queryObligationId = target.queryObligationId;
    }
    entry.startedAtMs = Math.min(entry.startedAtMs, target.startedAtMs);
  }

  entry.listeners.add(listener);
  listener(entry.snapshot);
  if (!entry.inFlight && !entry.timer && !isTerminalQueryStatus(entry.snapshot.status)) {
    void pollEntry(entry.key);
  }

  return () => {
    const current = entryByKey.get(target.key);
    if (!current) return;
    current.listeners.delete(listener);
    if (current.listeners.size > 0) return;
    if (current.timer) {
      clearTimeout(current.timer);
      current.timer = null;
    }
    entryByKey.delete(target.key);
  };
}

function snapshotForTarget(target: NormalizedTarget | null): SharedQueryStatusSnapshot | undefined {
  if (!target) return undefined;
  return entryByKey.get(target.key)?.snapshot;
}

export function useSharedQueryStatus(
  input: SharedQueryStatusTarget | null | undefined,
): SharedQueryStatusSnapshot | undefined {
  const normalized = useMemo(
    () => normalizeTarget(input),
    [
      input?.jobId,
      input?.threadId,
      input?.runId,
      input?.ownerToken,
      input?.queryObligationId,
      input?.startedAtMs,
      input?.enabled,
    ],
  );
  const [snapshot, setSnapshot] = useState<SharedQueryStatusSnapshot | undefined>(
    () => snapshotForTarget(normalized),
  );

  useEffect(() => {
    if (!normalized) {
      setSnapshot(undefined);
      return;
    }
    setSnapshot(snapshotForTarget(normalized));
    return subscribeToTarget(normalized, setSnapshot);
  }, [
    normalized?.key,
    normalized?.jobId,
    normalized?.threadId,
    normalized?.runId,
    normalized?.ownerToken,
    normalized?.queryObligationId,
    normalized?.startedAtMs,
  ]);

  return snapshot;
}

export function useSharedQueryStatuses(
  targets: SharedQueryStatusTarget[],
): Record<string, SharedQueryStatusSnapshot> {
  const normalizedTargets = useMemo(() => {
    const byKey = new Map<string, NormalizedTarget>();
    for (const target of targets) {
      const normalized = normalizeTarget(target);
      if (!normalized) continue;
      if (byKey.has(normalized.key)) continue;
      byKey.set(normalized.key, normalized);
    }
    return Array.from(byKey.values());
  }, [targets]);

  const [snapshotByKey, setSnapshotByKey] = useState<Record<string, SharedQueryStatusSnapshot>>(() => {
    const next: Record<string, SharedQueryStatusSnapshot> = {};
    for (const target of normalizedTargets) {
      const snapshot = snapshotForTarget(target);
      if (snapshot) next[target.key] = snapshot;
    }
    return next;
  });
  const targetSignature = useMemo(
    () => normalizedTargets.map((target) => (
      `${target.key}:${target.ownerToken || ''}:${target.queryObligationId || ''}:${target.startedAtMs}`
    )).join('|'),
    [normalizedTargets],
  );

  useEffect(() => {
    const keep = new Set<string>(normalizedTargets.map((target) => target.key));
    setSnapshotByKey((prev) => {
      const keys = Object.keys(prev);
      if (keys.length === 0) return prev;
      let changed = false;
      const next: Record<string, SharedQueryStatusSnapshot> = {};
      for (const key of keys) {
        if (!keep.has(key)) {
          changed = true;
          continue;
        }
        next[key] = prev[key];
      }
      return changed ? next : prev;
    });

    const unsubscribers = normalizedTargets.map((target) => subscribeToTarget(target, (snapshot) => {
      setSnapshotByKey((prev) => {
        const existing = prev[target.key];
        if (existing && existing === snapshot) return prev;
        return { ...prev, [target.key]: snapshot };
      });
    }));

    return () => {
      for (const unsubscribe of unsubscribers) unsubscribe();
    };
  }, [targetSignature]);

  return snapshotByKey;
}
