import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type {
  Artifact,
  AsyncQueryHandle,
  QueryJobStatus,
  QueryJobStatusResponse,
  QueryMetadata,
  QueryProvider,
  ToolCall,
  ToolProgressEvent,
  ToolResultEnvelope,
} from '@/types';
import type { SchemaField } from './Explorer';
import { useSharedQueryStatus } from '@/hooks/useSharedQueryStatus';
import {
  getQueryProviderBrand,
  getQueryProviderForToolName,
  getQueryProviderLabel,
  normalizeQueryProvider,
  resolveQueryNativeJobLink,
  type QueryProviderBrandConfig,
  type QueryProviderJobLink,
} from '@/lib/queryProviderRegistry';
import {
  getDeterministicQueryToolProgressPercent,
  isActiveQueryStatus,
  isTerminalQueryStatus,
  normalizeQueryJobStatus,
  resolveQueryDisplayProgressPercent,
  resolveQueryProgressPercent,
} from '@/lib/queryProgress';
import { CONNECTION_META, type ConnectionMeta } from '@/lib/connectionsCatalog';
import { normalizeManagedQueryJobId } from '@/lib/managedQueryJobId';
import {
  hasMaterializedQueryResults,
  resolveQueryToolLifecycleStatus,
} from '@/lib/queryToolLifecycle';

/**
 * useQueryInspectorState — single source of truth for the unified query
 * inspector. Absorbs the logic that previously lived in three places:
 *   - QueryToolRenderer (artifact-panel completed/error)
 *   - QueryExecutionCard (chat-inline executing/polling/completed/failed)
 *   - RunningQueryCard (deprecated standalone running card)
 *
 * Inputs reflect the union of what each call site has:
 *   - artifact-panel: just an Artifact (already materialised by useChat)
 *   - chat-inline: ToolCall + optional ToolResultEnvelope + optional Artifact
 *     + optional SSE ToolProgressEvent + isExecuting flag
 *
 * Outputs a single denormalised state object the zones can render from.
 */

export type QueryInspectorPhase = 'executing' | 'polling' | 'completed' | 'failed' | 'cancelled';

export interface QueryInspectorInput {
  /** Always present in artifact-panel mode; optional in chat-inline mode. */
  artifact?: Artifact;
  /** Set in chat-inline mode (we know the tool call). */
  toolCall?: ToolCall;
  /** Result envelope (async handle or completed/error). */
  toolResultEnvelope?: ToolResultEnvelope;
  /** Active tool execution progress before any envelope is in flight. */
  sseProgress?: ToolProgressEvent;
  /** Whether the run loop currently considers this tool call mid-execution. */
  isExecuting?: boolean;
  /** Raw tool_result payload for the artifact-panel inspector. */
  toolResult?: unknown;
}

export interface QueryInspectorStats {
  rowsReturned: number | null;
  totalRowsKnown: number | null;
  bytesProcessed: string | null;
  cacheHit: boolean;
  durationMs: number | null;
  jobId: string;
  jobLocation: string;
  warehouseId: string;
  warehouseName: string;
  projectId: string;
  accountIdentifier: string;
  host: string;
  monitorHealth: string;
  policyHealth: string;
  materializationState: string;
  validationState: string;
  cancelOutcome: string;
  queuedElapsedMs: number | null;
  executionElapsedMs: number | null;
  telemetryAgeMs: number | null;
  resultTruncated: boolean;
  profileOutcome: string;
  profilePhase: string;
  profileEstimatedBytes: string | null;
  profileEstimateIsExact: boolean;
  remoteSubmitted: boolean | null;
}

export interface QueryInspectorRunning {
  elapsedMs: number;
  progressPercent: number | null;
  cancelUrl: string | null;
  canCancel: boolean;
  cancelPending: boolean;
  cancelLocked: boolean;
  onCancel: () => Promise<void>;
}

export interface QueryInspectorErrorContext {
  errorLine: number | null;
  errorColumn: number | null;
  errorMessage: string | null;
}

export interface QueryInspectorState {
  // Identity / branding
  phase: QueryInspectorPhase;
  status: QueryJobStatus;
  /** Stable identity for the query obligation across replacement attempts. */
  queryObligationId: string;
  provider: QueryProvider;
  providerLabel: string;
  brand: QueryProviderBrandConfig;
  connectorMeta: ConnectionMeta | null;
  toolName: string;
  toolCallId: string;
  description: string;
  // Query
  sql: string;
  // Results / response zone
  schema: SchemaField[] | undefined;
  stats: QueryInspectorStats;
  consoleUrl: string;
  consoleLink: QueryProviderJobLink | null;
  // Running-state details
  running: QueryInspectorRunning;
  // Error-state details
  errorContext: QueryInspectorErrorContext;
  // Raw envelope JSON (for artifact-panel disclosure only)
  rawJson: string;
  // Derived helpers
  endedAtMs: number | null;
  startedAtMs: number;
  // Routed-through artifact (the canonical one to feed Explorer with)
  resolvedArtifact: Artifact | null;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function normalizeString(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const next = value.trim();
  return next ? next : null;
}

function normalizeQueryObligationID(value: unknown): string | null {
  const normalized = normalizeString(value);
  if (!normalized || !/^qob-[0-9a-f]{32}$/.test(normalized)) return null;
  if (normalized === `qob-${'0'.repeat(32)}`) return null;
  return normalized;
}

function normalizeStatusValue(value: unknown): QueryJobStatus | null {
  return normalizeQueryJobStatus(value);
}

function pickAsyncHandle(envelope: ToolResultEnvelope | undefined): AsyncQueryHandle | null {
  if (!envelope) return null;
  const result = envelope.result;
  if (
    isObject(result) &&
    typeof (result as Record<string, unknown>).job_id === 'string' &&
    typeof (result as Record<string, unknown>).status === 'string' &&
    isActiveQueryStatus((result as Record<string, unknown>).status)
  ) {
    return result as unknown as AsyncQueryHandle;
  }
  return null;
}

function formatBytes(value: unknown): string | null {
  if (value === undefined || value === null) return null;
  const n = typeof value === 'number' ? value : Number(value);
  if (!Number.isFinite(n) || n < 0) return null;
  if (n < 1024) return `${n.toFixed(0)} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`;
  if (n < 1024 ** 4) return `${(n / 1024 ** 3).toFixed(2)} GB`;
  return `${(n / 1024 ** 4).toFixed(2)} TB`;
}

function pickFirstString(values: Array<unknown>): string {
  for (const v of values) {
    if (typeof v === 'string') {
      const trimmed = v.trim();
      if (trimmed) return trimmed;
    }
  }
  return '';
}

function pickFirstNumber(values: Array<unknown>): number | null {
  for (const v of values) {
    if (typeof v === 'number' && Number.isFinite(v) && v >= 0) return v;
  }
  return null;
}

function pickProvider(
  envelope: ToolResultEnvelope | undefined,
  asyncHandle: AsyncQueryHandle | null,
  sseProgress: ToolProgressEvent | undefined,
  artifact: Artifact | undefined,
  toolCall: ToolCall | undefined,
): QueryProvider {
  const qm = envelope?.query_metadata || (artifact?.metadata as Record<string, unknown> | undefined)?.query_metadata;
  const fromMetadata = isObject(qm) ? qm.provider : undefined;
  const fromArtifactMeta = (artifact?.metadata as Record<string, unknown> | undefined)?.provider;
  const fromTool = toolCall?.name ? getQueryProviderForToolName(toolCall.name) : null;
  const fromArtifactToolName = (artifact?.metadata as Record<string, unknown> | undefined)?.tool_name;
  const fromArtifactToolNameStr =
    typeof fromArtifactToolName === 'string' ? getQueryProviderForToolName(fromArtifactToolName) : null;
  return normalizeQueryProvider(
    asyncHandle?.provider ||
      sseProgress?.provider ||
      fromMetadata ||
      fromArtifactMeta ||
      fromTool ||
      fromArtifactToolNameStr ||
      undefined,
    'bigquery',
  );
}

function extractErrorLocation(
  qm: Partial<QueryMetadata> | undefined,
  resultMeta: Record<string, unknown> | undefined,
  raw: unknown,
): { line: number | null; column: number | null } {
  const sources: Array<Record<string, unknown>> = [];
  if (isObject(qm)) sources.push(qm as Record<string, unknown>);
  if (isObject(resultMeta)) sources.push(resultMeta);
  if (isObject(raw)) sources.push(raw as Record<string, unknown>);
  let line: number | null = null;
  let column: number | null = null;
  for (const s of sources) {
    if (line == null) {
      const v =
        (typeof s.error_line === 'number' && s.error_line) ||
        (typeof s.line === 'number' && s.line) ||
        (typeof s.lineNumber === 'number' && s.lineNumber) ||
        null;
      if (typeof v === 'number') line = v;
    }
    if (column == null) {
      const v =
        (typeof s.error_column === 'number' && s.error_column) ||
        (typeof s.column === 'number' && s.column) ||
        (typeof s.columnNumber === 'number' && s.columnNumber) ||
        null;
      if (typeof v === 'number') column = v;
    }
  }
  if (line == null) {
    const errString =
      (isObject(raw) && typeof raw.error === 'string' && raw.error) ||
      (isObject(qm) && typeof (qm as Record<string, unknown>).error === 'string'
        ? ((qm as Record<string, unknown>).error as string)
        : '') ||
      '';
    const m = /\bline\s+(\d+)/i.exec(errString);
    if (m) line = Number(m[1]);
    const m2 = /\bcolumn\s+(\d+)/i.exec(errString);
    if (m2) column = Number(m2[1]);
  }
  return { line, column };
}

export function useQueryInspectorState(input: QueryInspectorInput): QueryInspectorState {
  const { artifact, toolCall, toolResultEnvelope, sseProgress, isExecuting, toolResult } = input;

  /* ---------- Source-of-truth resolution ----------
     We can be called with any subset of (toolCall, envelope, artifact, sseProgress).
     We normalise all sources into a single denormalised view.                       */

  const asyncHandle = useMemo(() => pickAsyncHandle(toolResultEnvelope), [toolResultEnvelope]);

  const provider = useMemo(
    () => pickProvider(toolResultEnvelope, asyncHandle, sseProgress, artifact, toolCall),
    [toolResultEnvelope, asyncHandle, sseProgress, artifact, toolCall],
  );
  const providerLabel = useMemo(() => getQueryProviderLabel(provider), [provider]);
  const brand = useMemo(() => getQueryProviderBrand(provider), [provider]);
  const connectorMeta = useMemo<ConnectionMeta | null>(
    () => CONNECTION_META[provider] || null,
    [provider],
  );

  /* ---------- Tool identity ---------- */

  const toolName = useMemo(() => {
    if (toolCall?.name) return toolCall.name;
    const meta = (artifact?.metadata as Record<string, unknown> | undefined) || {};
    return (typeof meta.tool_name === 'string' && meta.tool_name) || artifact?.title || 'query';
  }, [toolCall, artifact]);

  const toolCallId = useMemo(() => {
    if (toolCall?.id) return toolCall.id;
    const meta = (artifact?.metadata as Record<string, unknown> | undefined) || {};
    return typeof meta.tool_call_id === 'string' ? meta.tool_call_id : '';
  }, [toolCall, artifact]);

  /* ---------- Description / SQL ---------- */

  // Fallback for providers where args parsing can fail (or lag during
  // streaming) but argsText still carries valid JSON. Parsed once and shared
  // by both the compact chat card and the side-panel inspector.
  const argsTextFallback = useMemo<{ sql: string | null; description: string | null }>(() => {
    const empty = { sql: null, description: null };
    const rawArgsText = typeof toolCall?.argsText === 'string' ? toolCall.argsText.trim() : '';
    if (!rawArgsText) return empty;
    try {
      const parsed = JSON.parse(rawArgsText);
      if (!isObject(parsed)) return empty;
      return {
        sql: normalizeString(parsed.sql),
        description: normalizeString(parsed.description),
      };
    } catch {
      return empty;
    }
  }, [toolCall?.argsText]);

  const description = useMemo(() => {
    const meta = (artifact?.metadata as Record<string, unknown> | undefined) || {};
    const toolArgs = isObject(meta.tool_args) ? (meta.tool_args as Record<string, unknown>) : null;
    const qm = isObject(meta.query_metadata) ? (meta.query_metadata as Record<string, unknown>) : null;
    return pickFirstString([
      toolCall?.args?.description,
      argsTextFallback.description,
      toolResultEnvelope?.query_metadata?.description,
      meta.description,
      toolArgs?.description,
      qm?.description,
      asyncHandle?.message,
    ]);
  }, [artifact, toolCall, toolResultEnvelope, asyncHandle, argsTextFallback]);

  const sql = useMemo(() => {
    const meta = (artifact?.metadata as Record<string, unknown> | undefined) || {};
    const toolArgs = isObject(meta.tool_args) ? (meta.tool_args as Record<string, unknown>) : null;
    const qm = isObject(meta.query_metadata) ? (meta.query_metadata as Record<string, unknown>) : null;
    const raw = toolResult ?? meta.raw_result;
    const rawSql = isObject(raw) && typeof raw.sql === 'string' ? raw.sql : '';
    return pickFirstString([
      toolCall?.args?.sql,
      argsTextFallback.sql,
      toolResultEnvelope?.query_metadata?.sql,
      toolArgs?.sql,
      meta.sql,
      qm?.sql,
      rawSql,
      asyncHandle?.sql_preview,
      toolResultEnvelope?.query_metadata?.sql_preview,
      qm?.sql_preview,
    ]);
  }, [artifact, toolCall, toolResultEnvelope, asyncHandle, toolResult, argsTextFallback]);

  /* ---------- Phase / status state machine ----------
     - executing: tool call in flight, no envelope yet (chat-inline only)
     - polling: async handle returned, still running on server
     - completed: ok envelope (or completed artifact)
     - failed: ok=false envelope (or hasErrorResult)
     - cancelled: user requested cancel locally or upstream reported cancelled
                                                                            */

  const hasCompletedResultsArtifact = useMemo(() => {
    return hasMaterializedQueryResults({ artifact });
  }, [artifact]);

  const renderableArtifact = useMemo(
    () => {
      const meta = (artifact?.metadata as Record<string, unknown> | undefined) || {};
      return artifact && meta.placeholder !== true ? artifact : null;
    },
    [artifact],
  );

  const sourceLifecycleStatus = useMemo<QueryJobStatus>(() => {
    return resolveQueryToolLifecycleStatus({
      envelope: toolResultEnvelope,
      artifact,
      liveStatus: normalizeStatusValue(asyncHandle?.status),
    });
  }, [asyncHandle, artifact, toolResultEnvelope]);

  const derivedInitialStatus = useMemo<QueryJobStatus>(() => {
    return sourceLifecycleStatus;
  }, [sourceLifecycleStatus]);

  const [pollingStatus, setPollingStatus] = useState<QueryJobStatus>(derivedInitialStatus);
  const [pollingError, setPollingError] = useState<string | null>(null);
  const [cancelPending, setCancelPending] = useState(false);

  // Seed status payload (provider job ids, region, host, etc.) from the
  // current source-of-truth metadata. Recomputed whenever the envelope or
  // artifact changes so a fresh query gets fresh seed values; the identity
  // reset effect below feeds this back into state when the inspector slot
  // is reused for a different query.
  //
  // EVERY field is explicitly written (even when absent in the new qm) so
  // a swap from BigQuery → Databricks doesn't carry stale BQ identifiers
  // like project_id / job_location into the next provider's panel.
  const seedQueryObligationId = useMemo(() => {
    const envelopeResult = isObject(toolResultEnvelope?.result)
      ? toolResultEnvelope.result as Record<string, unknown>
      : null;
    const meta = (artifact?.metadata as Record<string, unknown> | undefined) || {};
    const artifactQM = isObject(meta.query_metadata)
      ? meta.query_metadata as Record<string, unknown>
      : null;
    const candidates = [
      asyncHandle?.query_obligation_id,
      sseProgress?.query_obligation_id,
      toolResultEnvelope?.query_metadata?.query_obligation_id,
      envelopeResult?.query_obligation_id,
      meta.query_obligation_id,
      artifactQM?.query_obligation_id,
    ];
    for (const candidate of candidates) {
      const normalized = normalizeQueryObligationID(candidate);
      if (normalized) return normalized;
    }
    return '';
  }, [asyncHandle, sseProgress, toolResultEnvelope, artifact]);

  const seedStatusPayload = useMemo<Partial<QueryJobStatusResponse>>(() => {
    const qmFromEnvelope = toolResultEnvelope?.query_metadata;
    const meta = (artifact?.metadata as Record<string, unknown> | undefined) || {};
    const qmFromArtifact = isObject(meta.query_metadata)
      ? (meta.query_metadata as Record<string, unknown>)
      : null;
    const qm = (qmFromEnvelope || qmFromArtifact || {}) as Record<string, unknown>;
    return {
      query_obligation_id: seedQueryObligationId || undefined,
      provider_job_id: normalizeString(qm.provider_job_id) || undefined,
      provider_job_url: normalizeString(qm.provider_job_url) || undefined,
      warehouse_job_id: normalizeString(qm.warehouse_job_id) || undefined,
      statement_id: normalizeString(qm.statement_id) || undefined,
      query_id: normalizeString(qm.query_id) || undefined,
      project_id: normalizeString(qm.project_id) || undefined,
      account_identifier: normalizeString(qm.account_identifier) || undefined,
      job_location: normalizeString(qm.job_location) || normalizeString(qm.location) || undefined,
      host: normalizeString(qm.host) || undefined,
      url: normalizeString(qm.url) || undefined,
      progress_percent:
        resolveQueryProgressPercent(qm.progress_percent, qm.progress_pct) ?? undefined,
      monitor_health: normalizeString(qm.monitor_health) as QueryJobStatusResponse['monitor_health'],
      policy_health: normalizeString(qm.policy_health) as QueryJobStatusResponse['policy_health'],
      materialization_state: normalizeString(qm.materialization_state) as QueryJobStatusResponse['materialization_state'],
      validation_state: normalizeString(qm.validation_state) as QueryJobStatusResponse['validation_state'],
      remote_submitted: typeof qm.remote_submitted === 'boolean' ? qm.remote_submitted : undefined,
      profile_outcome: normalizeString(qm.profile_outcome) as QueryJobStatusResponse['profile_outcome'],
      query_profile: isObject(qm.query_profile)
		? qm.query_profile as unknown as QueryJobStatusResponse['query_profile']
        : undefined,
      profile: isObject(qm.profile)
		? qm.profile as unknown as QueryJobStatusResponse['profile']
        : undefined,
      profile_handle: isObject(qm.profile_handle)
		? qm.profile_handle as unknown as QueryJobStatusResponse['profile_handle']
        : undefined,
      profile_phase: normalizeString(qm.profile_phase) || undefined,
      profile_dispatched: typeof qm.profile_dispatched === 'boolean' ? qm.profile_dispatched : undefined,
      cancel_outcome: normalizeString(qm.cancel_outcome) || undefined,
      queued_elapsed_ms: pickFirstNumber([qm.queued_elapsed_ms]) ?? undefined,
      execution_elapsed_ms: pickFirstNumber([qm.execution_elapsed_ms]) ?? undefined,
      telemetry_age_ms: pickFirstNumber([qm.telemetry_age_ms]) ?? undefined,
      bytes_processed: pickFirstNumber([qm.bytes_processed, qm.total_bytes_processed]) ?? undefined,
      result_truncated: qm.result_truncated === true,
    } as Partial<QueryJobStatusResponse>;
  }, [toolResultEnvelope, artifact, seedQueryObligationId]);

  const [statusPayload, setStatusPayload] = useState<Partial<QueryJobStatusResponse>>(
    () => seedStatusPayload,
  );
  const queryObligationId =
    normalizeQueryObligationID(statusPayload.query_obligation_id)
    || seedQueryObligationId;

  useEffect(() => {
    if (!seedQueryObligationId) return;
    setStatusPayload((prev) => {
      const current = normalizeQueryObligationID(prev.query_obligation_id);
      if (current) return prev;
      return { ...prev, query_obligation_id: seedQueryObligationId };
    });
  }, [seedQueryObligationId]);

  // Accept terminal upgrades from parent (placeholder → completed artifact swap).
  useEffect(() => {
    if (sourceLifecycleStatus === 'completed') {
      setPollingStatus('completed');
      setCancelPending(false);
    }
  }, [sourceLifecycleStatus]);

  // The artifact metadata's `ok` boolean is authoritative for error detection
  // when there is no live envelope (artifact-panel mode).
  const artifactMetaOk = ((artifact?.metadata as Record<string, unknown> | undefined) || {}).ok;
  const artifactRaw = ((artifact?.metadata as Record<string, unknown> | undefined) || {}).raw_result;
  const rawResult = toolResult ?? artifactRaw;
  const rawHasError = isObject(rawResult) && typeof rawResult.error === 'string' && !!rawResult.error.trim();
  const artifactPlaceholder = ((artifact?.metadata as Record<string, unknown> | undefined) || {}).placeholder === true ||
    ((artifact?.metadata as Record<string, unknown> | undefined) || {}).async_query === true;

  // Query lifecycle is independent from the invocation envelope's `ok` bit:
  // ok=true can mean only that an asynchronous query was accepted.
  const metadataStatusHint = sourceLifecycleStatus;

  const phase = useMemo<QueryInspectorPhase>(() => {
    if (isExecuting && !toolResultEnvelope && !artifact) return 'executing';
    if (asyncHandle) {
      if (metadataStatusHint === 'cancelled') return 'cancelled';
      if (
        metadataStatusHint === 'failed'
        || metadataStatusHint === 'expired'
        || metadataStatusHint === 'orphaned'
      ) return 'failed';
      if (hasCompletedResultsArtifact || pollingStatus === 'completed') return 'completed';
      if (pollingStatus === 'failed' || pollingStatus === 'expired' || pollingStatus === 'orphaned') return 'failed';
      if (pollingStatus === 'cancelled') return 'cancelled';
      return 'polling';
    }
    // Envelope path
    if (toolResultEnvelope) {
      if (metadataStatusHint === 'cancelled') return 'cancelled';
      if (metadataStatusHint === 'failed' || metadataStatusHint === 'expired' || metadataStatusHint === 'orphaned') return 'failed';
      if (metadataStatusHint === 'completed') return 'completed';
      return 'polling';
    }
    // Artifact-only path (artifact panel)
    if (artifact) {
      if (metadataStatusHint === 'cancelled') return 'cancelled';
      if (metadataStatusHint === 'failed' || metadataStatusHint === 'expired' || metadataStatusHint === 'orphaned') return 'failed';
      if (artifactMetaOk === false || (artifactMetaOk === undefined && rawHasError)) return 'failed';
      if (hasCompletedResultsArtifact || pollingStatus === 'completed') return 'completed';
      if (artifactPlaceholder) return 'polling';
      if (metadataStatusHint === 'completed') return 'completed';
      return 'polling';
    }
    return 'executing';
  }, [
    isExecuting,
    toolResultEnvelope,
    artifact,
    artifactMetaOk,
    artifactPlaceholder,
    asyncHandle,
    pollingStatus,
    hasCompletedResultsArtifact,
    rawHasError,
    metadataStatusHint,
  ]);

  const status = useMemo<QueryJobStatus>(() => {
    if (phase === 'executing') return 'running';
    if (phase === 'polling') return pollingStatus;
    if (phase === 'completed') return 'completed';
    if (phase === 'failed') {
      if (pollingStatus === 'expired' || pollingStatus === 'orphaned') return pollingStatus;
      if (metadataStatusHint === 'expired' || metadataStatusHint === 'orphaned') return metadataStatusHint;
      return 'failed';
    }
    if (phase === 'cancelled') return 'cancelled';
    return 'running';
  }, [phase, pollingStatus, metadataStatusHint]);

  const isTerminal = phase === 'completed' || phase === 'failed' || phase === 'cancelled';

  /* ---------- Elapsed timer ----------
     Single startedAt ref drives both executing-phase and polling-phase
     elapsed display. Recomputes monotonically — never goes backwards.        */

  const seedStartedAt = useMemo(() => {
    const fromToolCall = (toolCall as unknown as { _uiStartedAt?: number })?._uiStartedAt;
    const fromArtifact = (artifact?.metadata as Record<string, unknown> | undefined)?.started_at_ms;
    const fromAsync = asyncHandle?._uiStartedAt;
    const fromArtifactUi = (artifact?.metadata as Record<string, unknown> | undefined)?._uiStartedAt;
    return (
      (typeof fromToolCall === 'number' && fromToolCall > 0 ? fromToolCall : null) ||
      (typeof fromArtifact === 'number' && fromArtifact > 0 ? fromArtifact : null) ||
      (typeof fromAsync === 'number' && fromAsync > 0 ? fromAsync : null) ||
      (typeof fromArtifactUi === 'number' && fromArtifactUi > 0 ? fromArtifactUi : null) ||
      Date.now()
    );
  }, [toolCall, artifact, asyncHandle]);

  const startedAtRef = useRef(seedStartedAt);
  const [elapsed, setElapsed] = useState(() => Math.max(Date.now() - startedAtRef.current, 0));

  // Last progress percent shown for the active (executing/polling) phases.
  // Feeds resolveQueryDisplayProgressPercent as previousProgressPercent so the
  // bar never moves backwards when the source switches (SSE estimate → first
  // server-reported percent, or a provider briefly reporting a lower value).
  const lastProgressRef = useRef<number | null>(null);

  /* ---------- SSE-driven progress (executing phase) ---------- */

  const [sseProgressWidth, setSseProgressWidth] = useState<number | null>(null);

  // Identity reset — when the inspector slot is reused for a different
  // query (artifact panel swapping artifacts, chat row swapping tool
  // calls, async handle changing job id), every piece of state that was
  // tied to the previous query must be reseeded from the new source of
  // truth. Without this:
  //   - statusPayload would keep the previous provider's job_id, region,
  //     project, statement_id, etc., and the stats row would render a
  //     BigQuery `region US` chip on a Databricks panel.
  //   - pollingStatus / cancelPending would carry over, so a panel opened
  //     just after a cancellation request would render stale state.
  //   - elapsed / sseProgressWidth would inherit the previous query's
  //     clock and bar fill.
  //
  // The previousIdentityRef gate skips the first mount: useState
  // initializers already populated everything correctly for that case.
  const identityKey = `${artifact?.id ?? ''}::${toolCall?.id ?? ''}::${asyncHandle?.job_id ?? ''}`;
  const previousIdentityRef = useRef<string | null>(null);
  useEffect(() => {
    if (previousIdentityRef.current === identityKey) return;
    const isFirstMount = previousIdentityRef.current === null;
    previousIdentityRef.current = identityKey;
    if (isFirstMount) return;

    startedAtRef.current = seedStartedAt;
    setElapsed(Math.max(Date.now() - seedStartedAt, 0));
    setPollingStatus(derivedInitialStatus);
    setPollingError(null);
    setCancelPending(false);
    setSseProgressWidth(null);
    lastProgressRef.current = null;
    setStatusPayload(seedStatusPayload);
  }, [identityKey, seedStartedAt, derivedInitialStatus, seedStatusPayload]);

  useEffect(() => {
    if (isTerminal) return;
    setElapsed((prev) => Math.max(prev, Date.now() - startedAtRef.current, 0));
    const interval = setInterval(() => {
      setElapsed((prev) => Math.max(prev, Date.now() - startedAtRef.current, 0));
    }, 1000);
    return () => clearInterval(interval);
  }, [isTerminal]);

  useEffect(() => {
    if (phase !== 'executing') return;
    const next = getDeterministicQueryToolProgressPercent(sseProgress);
    if (next === null) return;
    setSseProgressWidth((prev) => {
      if (prev === null || next === 100) return next;
      return Math.max(prev, next);
    });
  }, [
    phase,
    sseProgress?.provider,
    sseProgress?.phase,
    sseProgress?.state,
    sseProgress?.bytes_processed,
    sseProgress?.rows_processed,
    sseProgress?.rows_returned,
    sseProgress?.row_count,
  ]);

  /* ---------- Polling state (polling phase) ---------- */

  const jobId = useMemo<string | undefined>(() => {
    const fromHandle = normalizeManagedQueryJobId(asyncHandle?.job_id);
    if (fromHandle) return fromHandle;
    const fromProgress = normalizeManagedQueryJobId(sseProgress?.job_id);
    if (fromProgress) return fromProgress;
    const meta = (artifact?.metadata as Record<string, unknown> | undefined) || {};
    const fromMetadata = normalizeManagedQueryJobId(meta.job_id);
    if (fromMetadata) return fromMetadata;
    const qm = isObject(meta.query_metadata) ? (meta.query_metadata as Record<string, unknown>) : null;
    return normalizeManagedQueryJobId(qm?.job_id);
  }, [asyncHandle, sseProgress, artifact]);

  const ownerToken = useMemo<string | undefined>(() => {
    if (asyncHandle?.owner_token) return asyncHandle.owner_token;
    const fromEnv = toolResultEnvelope?.query_metadata?.owner_token;
    if (typeof fromEnv === 'string' && fromEnv) return fromEnv;
    const meta = (artifact?.metadata as Record<string, unknown> | undefined) || {};
    if (typeof meta.owner_token === 'string' && meta.owner_token) return meta.owner_token;
    return undefined;
  }, [asyncHandle, toolResultEnvelope, artifact]);

  const threadId = useMemo<string | undefined>(() => {
    if (asyncHandle?.thread_id) return asyncHandle.thread_id;
    const fromEnv = toolResultEnvelope?.query_metadata?.thread_id;
    if (typeof fromEnv === 'string' && fromEnv) return fromEnv;
    const meta = (artifact?.metadata as Record<string, unknown> | undefined) || {};
    if (typeof meta.thread_id === 'string' && meta.thread_id) return meta.thread_id;
    return undefined;
  }, [asyncHandle, toolResultEnvelope, artifact]);

  const runId = useMemo<string | undefined>(() => {
    if (asyncHandle?.run_id) return asyncHandle.run_id;
    const fromProgress = normalizeString(
      (sseProgress as unknown as Record<string, unknown> | undefined)?.run_id,
    );
    if (fromProgress) return fromProgress;
    const fromEnv = toolResultEnvelope?.query_metadata?.run_id;
    if (typeof fromEnv === 'string' && fromEnv) return fromEnv;
    const meta = (artifact?.metadata as Record<string, unknown> | undefined) || {};
    if (typeof meta.run_id === 'string' && meta.run_id) return meta.run_id;
    const qm = isObject(meta.query_metadata) ? meta.query_metadata as Record<string, unknown> : null;
    return normalizeString(qm?.run_id) || undefined;
  }, [asyncHandle, sseProgress, toolResultEnvelope, artifact]);

  const requestHeaders = useMemo(() => {
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (ownerToken) headers['x-query-owner-token'] = ownerToken;
    return headers;
  }, [ownerToken]);

  // Polling-target subscription. We only enable it when actively running
  // and a real jobId exists. The hook handles refcount + adaptive backoff.
  const sharedStatus = useSharedQueryStatus({
    enabled: phase === 'polling' && Boolean(jobId),
    jobId,
    threadId,
    runId,
    ownerToken,
    queryObligationId,
    startedAtMs: startedAtRef.current,
  });

  useEffect(() => {
    if (!sharedStatus) return;
    const nextStatus = normalizeStatusValue(sharedStatus.status);
    if (nextStatus) {
      setPollingStatus(nextStatus);
      if (isTerminalQueryStatus(nextStatus)) {
        setCancelPending(false);
      }
    }
    if (
      typeof sharedStatus.elapsed_ms === 'number' &&
      Number.isFinite(sharedStatus.elapsed_ms) &&
      sharedStatus.elapsed_ms >= 0
    ) {
      setElapsed((prev) => Math.max(prev, sharedStatus.elapsed_ms as number));
    }
    const sharedError =
      (typeof sharedStatus.error === 'string' && sharedStatus.error.trim())
      || (typeof sharedStatus.client_poll_error === 'string' && sharedStatus.client_poll_error.trim())
      || null;
    setPollingError(sharedError);
    const nextProgress = resolveQueryProgressPercent(
      sharedStatus.progress_percent,
      (sharedStatus as Record<string, unknown>).progress_pct,
    );
    setStatusPayload((prev) => ({
      ...prev,
      query_obligation_id:
        normalizeQueryObligationID(sharedStatus.query_obligation_id)
        || prev.query_obligation_id,
      provider_job_id: normalizeString(sharedStatus.provider_job_id) || prev.provider_job_id,
      provider_job_url: normalizeString(sharedStatus.provider_job_url) || prev.provider_job_url,
      warehouse_job_id: normalizeString(sharedStatus.warehouse_job_id) || prev.warehouse_job_id,
      statement_id: normalizeString(sharedStatus.statement_id) || prev.statement_id,
      query_id: normalizeString(sharedStatus.query_id) || prev.query_id,
      project_id: normalizeString(sharedStatus.project_id) || prev.project_id,
      account_identifier: normalizeString(sharedStatus.account_identifier) || prev.account_identifier,
      job_location:
        normalizeString(sharedStatus.job_location) ||
        normalizeString(sharedStatus.location) ||
        prev.job_location,
      host: normalizeString(sharedStatus.host) || prev.host,
      url: normalizeString(sharedStatus.url) || prev.url,
      progress_percent: nextProgress ?? prev.progress_percent,
      monitor_health: sharedStatus.monitor_health ?? prev.monitor_health,
      policy_health: sharedStatus.policy_health ?? prev.policy_health,
      materialization_state: sharedStatus.materialization_state ?? prev.materialization_state,
      validation_state: sharedStatus.validation_state ?? prev.validation_state,
      remote_submitted: sharedStatus.remote_submitted ?? prev.remote_submitted,
      profile_outcome: sharedStatus.profile_outcome ?? prev.profile_outcome,
      query_profile: sharedStatus.query_profile ?? prev.query_profile,
      profile: sharedStatus.profile ?? prev.profile,
      profile_handle: sharedStatus.profile_handle ?? prev.profile_handle,
      profile_phase: sharedStatus.profile_phase ?? prev.profile_phase,
      profile_dispatched: sharedStatus.profile_dispatched ?? prev.profile_dispatched,
      cancel_outcome: sharedStatus.cancel_outcome ?? prev.cancel_outcome,
      queued_elapsed_ms: sharedStatus.queued_elapsed_ms ?? prev.queued_elapsed_ms,
      execution_elapsed_ms: sharedStatus.execution_elapsed_ms ?? prev.execution_elapsed_ms,
      telemetry_age_ms: sharedStatus.telemetry_age_ms ?? prev.telemetry_age_ms,
      bytes_processed: sharedStatus.bytes_processed ?? prev.bytes_processed,
      result_truncated: sharedStatus.result_truncated ?? prev.result_truncated,
    }));
    // identityKey is a dependency on purpose: the identity-reset effect above
    // reseeds pollingStatus from derivedInitialStatus, which can resurrect a
    // stale 'running' after the shared store already observed a terminal
    // status (the snapshot object itself doesn't change, so [sharedStatus]
    // alone would never re-apply it and the card would stay running forever).
  }, [sharedStatus, identityKey]);

  /* ---------- Cancel handler ----------
     Query cancellation is deliberately separate from stopping the model
     run. The UI remains active until the provider confirms a terminal state. */

  const cancelUrl = useMemo<string | null>(() => {
    if (!jobId) return null;
    const qs = new URLSearchParams();
    if (threadId) qs.set('thread_id', threadId);
    const suffix = qs.toString();
    return `/api/queries/${encodeURIComponent(jobId)}/cancel${suffix ? `?${suffix}` : ''}`;
  }, [jobId, threadId]);

  const cancelRequestUrl = cancelUrl;
  const canCancel =
    !isTerminal && (phase === 'executing' || phase === 'polling') && Boolean(cancelRequestUrl);

  const onCancel = useCallback(async () => {
    if (!cancelRequestUrl || isTerminal || cancelPending) return;
    setCancelPending(true);
    setPollingError(null);
    try {
      const response = await fetch(cancelRequestUrl, { method: 'POST', headers: requestHeaders });
      if (!response.ok) {
        throw new Error(`Query cancellation request failed (${response.status}).`);
      }
    } catch (error) {
      setCancelPending(false);
      setPollingError(error instanceof Error ? error.message : 'Query cancellation request failed.');
    }
  }, [cancelPending, cancelRequestUrl, isTerminal, requestHeaders]);

  /* ---------- Progress display value ---------- */

  const progressPercent = useMemo<number | null>(() => {
    if (phase === 'completed') return 100;
    if (phase === 'failed' || phase === 'cancelled') return null;
    if (phase === 'executing') {
      if (sseProgressWidth === null) return lastProgressRef.current;
      return Math.max(sseProgressWidth, lastProgressRef.current ?? 0);
    }
    // polling
    const fromServer = resolveQueryProgressPercent(
      statusPayload.progress_percent,
      (statusPayload as Record<string, unknown>).progress_pct,
    );
    return (
      resolveQueryDisplayProgressPercent({
        status: pollingStatus,
        elapsedMs: elapsed,
        progressPercent: fromServer,
        previousProgressPercent: lastProgressRef.current,
      }) ?? lastProgressRef.current
    );
  }, [phase, sseProgressWidth, statusPayload, pollingStatus, elapsed]);

  // Remember the displayed value only while the query is active so the next
  // active-phase render can clamp against it; terminal phases reset it so a
  // reused inspector slot starts fresh.
  useEffect(() => {
    if (phase === 'executing' || phase === 'polling') {
      lastProgressRef.current = progressPercent;
    } else {
      lastProgressRef.current = null;
    }
  }, [phase, progressPercent]);

  /* ---------- Native-link resolution ----------
     Stays inside the provider registry so any provider (current or future)
     gets a console link as long as it surfaces enough identifiers. Trino
     deliberately needs an explicit URL because there's no canonical UI.      */

  const qm = useMemo(() => {
    const fromEnvelope = toolResultEnvelope?.query_metadata;
    const meta = (artifact?.metadata as Record<string, unknown> | undefined) || {};
    const fromArtifact = isObject(meta.query_metadata) ? (meta.query_metadata as Record<string, unknown>) : null;
    return (fromEnvelope || (fromArtifact as unknown as QueryMetadata) || {}) as Partial<QueryMetadata>;
  }, [toolResultEnvelope, artifact]);

  const consoleLink = useMemo<QueryProviderJobLink | null>(() => {
    return resolveQueryNativeJobLink(provider, {
      providerJobId: normalizeString(statusPayload.provider_job_id) || normalizeString(qm.provider_job_id),
      providerJobUrl:
        normalizeString(statusPayload.provider_job_url) || normalizeString(qm.provider_job_url),
      warehouseJobId:
        normalizeString(statusPayload.warehouse_job_id) || normalizeString(qm.warehouse_job_id),
      statementId: normalizeString(statusPayload.statement_id) || normalizeString(qm.statement_id),
      queryId: normalizeString(statusPayload.query_id) || normalizeString(qm.query_id),
      projectId: normalizeString(statusPayload.project_id) || normalizeString(qm.project_id),
      accountIdentifier:
        normalizeString(statusPayload.account_identifier) ||
        normalizeString(qm.account_identifier),
      location:
        normalizeString(statusPayload.job_location) ||
        normalizeString((statusPayload as Record<string, unknown>).location) ||
        normalizeString(qm.job_location) ||
        normalizeString(qm.location),
      host: normalizeString(statusPayload.host) || normalizeString(qm.host),
      url: normalizeString(statusPayload.url) || normalizeString(qm.url),
      jobId: jobId || undefined,
    });
  }, [provider, statusPayload, qm, jobId]);

  /* ---------- Stats ---------- */

  const stats = useMemo<QueryInspectorStats>(() => {
    const meta = (artifact?.metadata as Record<string, unknown> | undefined) || {};
    const resultMeta = isObject(meta.result_metadata) ? (meta.result_metadata as Record<string, unknown>) : null;
    const rowsReturned = pickFirstNumber([
      qm.rows_returned,
      qm.row_count,
      meta.row_count,
    ]);
    const totalRowsKnown = pickFirstNumber([
      meta.row_count_total,
      (qm as Record<string, unknown>).row_count_total,
      resultMeta?.row_count_total,
    ]) ?? rowsReturned;
    const bytesProcessed = formatBytes(
      statusPayload.bytes_processed ?? qm.total_bytes_processed,
    );
    const cacheHit = qm.cache_hit === true;
    const durationCandidate = pickFirstNumber([
      meta.duration_ms,
      qm.duration_ms,
    ]);
    const jobIdValue = pickFirstString([
      statusPayload.provider_job_id,
      statusPayload.warehouse_job_id,
      statusPayload.statement_id,
      statusPayload.query_id,
      qm.provider_job_id,
      qm.warehouse_job_id,
      qm.statement_id,
      qm.query_id,
      normalizeManagedQueryJobId(qm.job_id),
      jobId,
    ]);
    const jobLocation = pickFirstString([
      statusPayload.job_location,
      (statusPayload as Record<string, unknown>).location,
      qm.job_location,
      qm.location,
    ]);
    const warehouseId = pickFirstString([qm.warehouse_id, qm.warehouse_job_id]);
    const warehouseName = pickFirstString([qm.warehouse_name]);
    const projectId = pickFirstString([statusPayload.project_id, qm.project_id]);
    const accountIdentifier = pickFirstString([
      statusPayload.account_identifier,
      qm.account_identifier,
    ]);
    const host = pickFirstString([statusPayload.host, qm.host]);
    const profile = (isObject(statusPayload.query_profile) && statusPayload.query_profile)
      || (isObject(statusPayload.profile) && statusPayload.profile)
      || (isObject(qm.query_profile) && qm.query_profile)
      || (isObject(qm.profile) && qm.profile)
      || null;
    return {
      rowsReturned,
      totalRowsKnown,
      bytesProcessed,
      cacheHit,
      durationMs: durationCandidate,
      jobId: jobIdValue,
      jobLocation,
      warehouseId,
      warehouseName,
      projectId,
      accountIdentifier,
      host,
      monitorHealth: pickFirstString([statusPayload.monitor_health, qm.monitor_health]),
      policyHealth: pickFirstString([statusPayload.policy_health, qm.policy_health]),
      materializationState: pickFirstString([
        statusPayload.materialization_state,
        qm.materialization_state,
      ]),
      validationState: pickFirstString([statusPayload.validation_state, qm.validation_state]),
      cancelOutcome: pickFirstString([statusPayload.cancel_outcome, qm.cancel_outcome]),
      queuedElapsedMs: pickFirstNumber([statusPayload.queued_elapsed_ms, qm.queued_elapsed_ms]),
      executionElapsedMs: pickFirstNumber([
        statusPayload.execution_elapsed_ms,
        qm.execution_elapsed_ms,
      ]),
      telemetryAgeMs: pickFirstNumber([statusPayload.telemetry_age_ms, qm.telemetry_age_ms]),
      resultTruncated:
        statusPayload.result_truncated === true || qm.result_truncated === true,
      profileOutcome: pickFirstString([statusPayload.profile_outcome, qm.profile_outcome]),
      profilePhase: pickFirstString([statusPayload.profile_phase, qm.profile_phase]),
      profileEstimatedBytes: formatBytes(profile?.estimated_bytes),
      profileEstimateIsExact: profile?.estimate_is_exact === true,
      remoteSubmitted:
        typeof statusPayload.remote_submitted === 'boolean'
          ? statusPayload.remote_submitted
          : typeof qm.remote_submitted === 'boolean'
            ? qm.remote_submitted
            : null,
    };
  }, [artifact, qm, statusPayload, jobId]);

  /* ---------- Schema / raw envelope JSON ---------- */

  const schema = useMemo<SchemaField[] | undefined>(() => {
    if (Array.isArray(qm.schema)) return qm.schema as SchemaField[];
    return undefined;
  }, [qm]);

  const rawJson = useMemo(() => {
    const source = toolResultEnvelope ?? ((artifact?.metadata as Record<string, unknown> | undefined) || {}).raw_result ?? rawResult;
    try {
      return JSON.stringify(source, null, 2);
    } catch {
      return String(source);
    }
  }, [toolResultEnvelope, artifact, rawResult]);

  /* ---------- Error context ---------- */

  const errorContext = useMemo<QueryInspectorErrorContext>(() => {
    const meta = (artifact?.metadata as Record<string, unknown> | undefined) || {};
    const resultMeta = isObject(meta.result_metadata) ? (meta.result_metadata as Record<string, unknown>) : undefined;
    const { line, column } = extractErrorLocation(qm, resultMeta, rawResult);
    const errorMessage =
      (isObject(rawResult) && typeof rawResult.error === 'string' && rawResult.error.trim()) ||
      pollingError ||
      null;
    return { errorLine: line, errorColumn: column, errorMessage };
  }, [artifact, qm, rawResult, pollingError]);

  /* ---------- Console URL string for header rendering ---------- */

  const consoleUrl = consoleLink?.url || '';

  return {
    phase,
    status,
    queryObligationId,
    provider,
    providerLabel,
    brand,
    connectorMeta,
    toolName,
    toolCallId,
    description,
    sql,
    schema,
    stats,
    consoleUrl,
    consoleLink,
    running: {
      elapsedMs: elapsed,
      progressPercent,
      cancelUrl: cancelRequestUrl,
      canCancel,
      cancelPending,
      cancelLocked: cancelPending,
      onCancel,
    },
    errorContext,
    rawJson,
    endedAtMs: pickFirstNumber([
      ((artifact?.metadata as Record<string, unknown> | undefined) || {}).ended_at_ms,
      (rawResult as Record<string, unknown> | undefined)?._uiEndedAt,
    ]),
    startedAtMs: startedAtRef.current,
    resolvedArtifact: renderableArtifact,
  };
}
