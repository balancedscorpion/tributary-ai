import type { QueryJobStatus, QueryJobStatusResponse, ToolProgressEvent } from '@/types';

const POLL_INTERVAL_SHORT_MS = 1500;
const POLL_INTERVAL_MEDIUM_MS = 2500;
const POLL_INTERVAL_LONG_MS = 5000;
const SHORT_WINDOW_MS = 10_000;
const MEDIUM_WINDOW_MS = 60_000;
const ACTIVE_PROGRESS_CAP = 99;

type QueryStatusSnapshot = Partial<QueryJobStatusResponse>;

type ToolProgressEventWithRows = Partial<ToolProgressEvent> & {
  rows_processed?: unknown;
  rows_returned?: unknown;
  row_count?: unknown;
  bytes_processed?: unknown;
};

export interface QueryProgressDisplayInput {
  status: QueryJobStatus;
  startedAtMs: number;
  nowMs?: number;
  completedAtMs?: number;
  durationMs?: number;
  liveStatus?: {
    status?: QueryJobStatus;
    elapsed_ms?: unknown;
    progress_percent?: unknown;
    progress_pct?: unknown;
  };
  metadataProgressPercent?: unknown;
  metadataProgressPct?: unknown;
  previousProgressPercent?: unknown;
}

export interface QueryProgressDisplayState {
  status: QueryJobStatus;
  isRunning: boolean;
  isComplete: boolean;
  elapsedMs: number;
  frozenDurationMs: number;
  progressPercent: number | null;
  showProgress: boolean;
}

export function normalizeQueryProgressPercent(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value)) {
    return Math.max(0, Math.min(100, Math.round(value)));
  }
  if (typeof value === 'string') {
    const parsed = Number(value.trim());
    if (!Number.isFinite(parsed)) return null;
    return Math.max(0, Math.min(100, Math.round(parsed)));
  }
  return null;
}

export function resolveQueryProgressPercent(progressPercent: unknown, progressPct: unknown): number | null {
  const fromPercent = normalizeQueryProgressPercent(progressPercent);
  if (fromPercent !== null) return fromPercent;

  let pctValue: number | null = null;
  if (typeof progressPct === 'number' && Number.isFinite(progressPct)) {
    pctValue = progressPct;
  } else if (typeof progressPct === 'string') {
    const parsed = Number(progressPct.trim());
    if (Number.isFinite(parsed)) pctValue = parsed;
  }

  if (pctValue === null) return null;
  if (pctValue > 0 && pctValue < 1) {
    return normalizeQueryProgressPercent(pctValue * 100);
  }
  return normalizeQueryProgressPercent(pctValue);
}

export function shouldKeepHigherProgress(status: unknown): boolean {
  return isActiveQueryStatus(status);
}

export function normalizeQueryJobStatus(status: unknown): QueryJobStatus | null {
  if (
    status === 'reserved'
    || status === 'profiling'
    || status === 'submission_unknown'
    || status === 'submitted'
    || status === 'running'
    || status === 'cancel_requested'
    || status === 'cancelling'
    || status === 'completed'
    || status === 'failed'
    || status === 'cancelled'
    || status === 'expired'
    || status === 'orphaned'
    || status === 'unknown'
  ) {
    return status;
  }
  return null;
}

export function isActiveQueryStatus(status: unknown): boolean {
  return status === 'reserved'
    || status === 'profiling'
    || status === 'submission_unknown'
    || status === 'submitted'
    || status === 'running'
    || status === 'cancel_requested'
    || status === 'cancelling'
    || status === 'unknown';
}

export function isTerminalQueryStatus(status: unknown): boolean {
  return status === 'completed'
    || status === 'failed'
    || status === 'cancelled'
    || status === 'expired'
    || status === 'orphaned';
}

function normalizeNonNegativeNumber(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value) && value >= 0) return value;
  if (typeof value === 'string' && value.trim()) {
    const parsed = Number(value.trim());
    if (Number.isFinite(parsed) && parsed >= 0) return parsed;
  }
  return null;
}

function normalizeStartedAtMs(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null;
}

function localElapsedMs(startedAtMs: unknown, nowMs = Date.now()): number | null {
  const started = normalizeStartedAtMs(startedAtMs);
  if (started === null) return null;
  return Math.max(0, nowMs - started);
}

export function resolveMonotonicQueryElapsedMs(input: {
  nextElapsedMs?: unknown;
  previousElapsedMs?: unknown;
  startedAtMs?: unknown;
  nowMs?: number;
}): number {
  const candidates = [
    normalizeNonNegativeNumber(input.nextElapsedMs),
    normalizeNonNegativeNumber(input.previousElapsedMs),
    localElapsedMs(input.startedAtMs, input.nowMs),
  ].filter((value): value is number => value !== null);
  if (candidates.length === 0) return 0;
  return Math.max(...candidates);
}

export function resolveQueryDisplayProgressPercent(input: {
  status: QueryJobStatus;
  elapsedMs: number;
  progressPercent?: unknown;
  progressPct?: unknown;
  fallbackProgressPercent?: unknown;
  fallbackProgressPct?: unknown;
  previousProgressPercent?: unknown;
}): number | null {
  if (input.status === 'completed') return 100;
  if (
    input.status === 'failed'
    || input.status === 'cancelled'
    || input.status === 'expired'
    || input.status === 'orphaned'
  ) return null;

  const providerProgress =
    resolveQueryProgressPercent(input.progressPercent, input.progressPct)
    ?? resolveQueryProgressPercent(input.fallbackProgressPercent, input.fallbackProgressPct);
  const previousProgress = normalizeQueryProgressPercent(input.previousProgressPercent);
  if (providerProgress === null) {
    return previousProgress === null ? null : Math.min(ACTIVE_PROGRESS_CAP, previousProgress);
  }
  const rawProgress = providerProgress;
  const cappedProgress = isActiveQueryStatus(input.status)
    ? Math.min(ACTIVE_PROGRESS_CAP, rawProgress)
    : rawProgress;

  if (
    previousProgress !== null
    && shouldKeepHigherProgress(input.status)
    && cappedProgress < previousProgress
  ) {
    return Math.min(ACTIVE_PROGRESS_CAP, previousProgress);
  }
  return cappedProgress;
}

export function deriveQueryProgressDisplay(input: QueryProgressDisplayInput): QueryProgressDisplayState {
  const nowMs = input.nowMs ?? Date.now();
  const status = normalizeQueryJobStatus(input.liveStatus?.status) ?? input.status;
  const isRunning = isActiveQueryStatus(status);
  const isComplete = status === 'completed';
  const liveElapsedMs = normalizeNonNegativeNumber(input.liveStatus?.elapsed_ms);
  const localElapsed = localElapsedMs(input.startedAtMs, nowMs) ?? 0;
  const elapsedMs = isRunning ? Math.max(liveElapsedMs ?? 0, localElapsed) : 0;
  const durationMs = normalizeNonNegativeNumber(input.durationMs);
  const completedAt = normalizeStartedAtMs(input.completedAtMs);
  const frozenDurationMs =
    liveElapsedMs
    ?? durationMs
    ?? (completedAt !== null ? Math.max(0, completedAt - input.startedAtMs) : localElapsed);
  const progressPercent = resolveQueryDisplayProgressPercent({
    status,
    elapsedMs: isRunning ? elapsedMs : frozenDurationMs,
    progressPercent: input.liveStatus?.progress_percent,
    progressPct: input.liveStatus?.progress_pct,
    fallbackProgressPercent: input.metadataProgressPercent,
    fallbackProgressPct: input.metadataProgressPct,
    previousProgressPercent: input.previousProgressPercent,
  });

  return {
    status,
    isRunning,
    isComplete,
    elapsedMs,
    frozenDurationMs,
    progressPercent,
    showProgress: (isRunning || isComplete) && progressPercent !== null,
  };
}

export function mergeQueryStatusSnapshot(
  previous: QueryStatusSnapshot,
  payload: QueryStatusSnapshot,
  startedAtMs: number,
  nowMs = Date.now(),
): QueryStatusSnapshot {
  const status =
    normalizeQueryJobStatus(payload.status)
    ?? normalizeQueryJobStatus(previous.status)
    ?? 'running';
  const elapsedMs = resolveMonotonicQueryElapsedMs({
    nextElapsedMs: payload.elapsed_ms,
    previousElapsedMs: previous.elapsed_ms,
    startedAtMs: isTerminalQueryStatus(status) ? undefined : startedAtMs,
    nowMs,
  });
  const previousProgress = resolveQueryProgressPercent(previous.progress_percent, previous.progress_pct);
  const progressPercent = resolveQueryDisplayProgressPercent({
    status,
    elapsedMs,
    progressPercent: payload.progress_percent,
    progressPct: payload.progress_pct,
    fallbackProgressPercent: previous.progress_percent,
    fallbackProgressPct: previous.progress_pct,
    previousProgressPercent: previousProgress,
  });

  return {
    ...previous,
    ...payload,
    status,
    elapsed_ms: elapsedMs,
    progress_percent: progressPercent ?? previous.progress_percent,
    progress_pct: progressPercent ?? previous.progress_pct,
  };
}

export function getDeterministicQueryToolProgressPercent(progress?: ToolProgressEventWithRows): number | null {
  if (!progress) return null;

  const explicitPercent = normalizeQueryProgressPercent(progress.percent);
  if (explicitPercent !== null) return explicitPercent;
  if (progress.phase === 'complete' || progress.state === 'SUCCEEDED' || progress.state === 'SUCCESS') return 100;
  return null;
}

export function getQueryPollDelay(elapsedMs: number): number {
  if (elapsedMs < SHORT_WINDOW_MS) return POLL_INTERVAL_SHORT_MS;
  if (elapsedMs < MEDIUM_WINDOW_MS) return POLL_INTERVAL_MEDIUM_MS;
  return POLL_INTERVAL_LONG_MS;
}
