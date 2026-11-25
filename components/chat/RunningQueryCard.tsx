import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Loader2, CheckCircle2, XCircle, AlertTriangle, Square, HelpCircle, ExternalLink } from 'lucide-react';
import { Card } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { useSharedQueryStatus } from '@/hooks/useSharedQueryStatus';
import type { QueryJobStatus, QueryJobStatusResponse, QueryMetadata, QueryProvider } from '@/types';
import { formatElapsed } from '@/lib/formatters';
import {
  getQueryProviderBrand,
  getQueryProviderLabel,
  normalizeQueryProvider,
  resolveQueryNativeJobLink,
} from '@/lib/queryProviderRegistry';
import {
  resolveQueryDisplayProgressPercent,
  resolveQueryProgressPercent,
} from '@/lib/queryProgress';
import { normalizeManagedQueryJobId } from '@/lib/managedQueryJobId';

interface RunningQueryCardProps {
  jobId: string;
  provider?: QueryProvider;
  sqlPreview?: string;
  ownerToken?: string;
  threadId?: string;
  runId?: string;
  queryObligationId?: string;
  queryMetadata?: QueryMetadata;
  initialStatus?: QueryJobStatus;
  startedAtMs?: number;
}

const PROGRESS_VISIBLE_STATUSES: QueryJobStatus[] = ['reserved', 'profiling', 'submission_unknown', 'submitted', 'running', 'cancel_requested', 'cancelling', 'unknown'];

interface NativeJobLink {
  id: string;
  url: string;
}

function normalizeString(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const next = value.trim();
  return next ? next : null;
}

function normalizeQueryObligationId(value: unknown): string | undefined {
  const normalized = normalizeString(value);
  if (!normalized || !/^qob-[0-9a-f]{32}$/.test(normalized)) return undefined;
  return normalized === `qob-${'0'.repeat(32)}` ? undefined : normalized;
}

function normalizeStatus(status: unknown): QueryJobStatus | null {
  if (status === 'reserved' || status === 'profiling' || status === 'submission_unknown' || status === 'submitted' || status === 'running' || status === 'cancel_requested' || status === 'cancelling' || status === 'completed' || status === 'failed' || status === 'cancelled' || status === 'expired' || status === 'orphaned' || status === 'unknown') {
    return status;
  }
  return null;
}

function isTerminalStatus(status: QueryJobStatus): boolean {
  return status === 'completed' || status === 'failed' || status === 'cancelled' || status === 'expired' || status === 'orphaned';
}

const RunningQueryCard: React.FC<RunningQueryCardProps> = ({
  jobId,
  provider,
  sqlPreview,
  ownerToken,
  threadId,
  runId,
  queryObligationId,
  queryMetadata,
  initialStatus,
  startedAtMs,
}) => {
  const managedJobId = normalizeManagedQueryJobId(jobId);
  const providerValue = normalizeQueryProvider(provider ?? queryMetadata?.provider, 'bigquery');
  const initial = normalizeStatus(initialStatus) || 'running';
  const [status, setStatus] = useState<QueryJobStatus>(initial);
  const [elapsed, setElapsed] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [cancelPending, setCancelPending] = useState(false);
  const [statusPayload, setStatusPayload] = useState<Partial<QueryJobStatusResponse>>({
    provider_job_id: normalizeString(queryMetadata?.provider_job_id) || undefined,
    provider_job_url: normalizeString(queryMetadata?.provider_job_url) || undefined,
    warehouse_job_id: normalizeString(queryMetadata?.warehouse_job_id) || undefined,
    statement_id: normalizeString(queryMetadata?.statement_id) || undefined,
    query_id: normalizeString(queryMetadata?.query_id) || undefined,
    project_id: normalizeString(queryMetadata?.project_id) || undefined,
    account_identifier: normalizeString(queryMetadata?.account_identifier) || undefined,
    job_location: normalizeString(queryMetadata?.job_location) || normalizeString(queryMetadata?.location) || undefined,
    host: normalizeString(queryMetadata?.host) || undefined,
    url: normalizeString(queryMetadata?.url) || undefined,
    progress_percent: resolveQueryProgressPercent(queryMetadata?.progress_percent, queryMetadata?.progress_pct) ?? undefined,
  });
  const startedAtRef = useRef(
    typeof startedAtMs === 'number' && Number.isFinite(startedAtMs) && startedAtMs > 0
      ? startedAtMs
      : Date.now()
  );
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const cancelledLocallyRef = useRef(false);
  const isTerminal = isTerminalStatus(status);

  // Accept terminal status upgrades from parent re-renders (e.g. when
  // get_query_results has already materialized the shared artifact).
  useEffect(() => {
    const next = normalizeStatus(initialStatus);
    if (!next || !isTerminalStatus(next)) return;
    setStatus(next);
    if (next !== 'cancelled') {
      cancelledLocallyRef.current = false;
      setCancelPending(false);
    }
  }, [initialStatus]);

  const requestHeaders = useMemo(() => {
    const headers: Record<string, string> = { Accept: 'application/json' };
    const token = String(ownerToken || '').trim();
    if (token) headers['x-query-owner-token'] = token;
    return headers;
  }, [ownerToken]);
  const cancelUrl = useMemo(() => {
    if (!managedJobId) return null;
    const qs = new URLSearchParams();
    const tid = String(threadId || '').trim();
    if (tid) qs.set('thread_id', tid);
    const suffix = qs.toString();
    return `/api/queries/${encodeURIComponent(managedJobId)}/cancel${suffix ? `?${suffix}` : ''}`;
  }, [managedJobId, threadId]);

  // Client-side elapsed timer (ticks every second).
  useEffect(() => {
    if (isTerminal || !managedJobId) return;
    setElapsed((prev) => Math.max(prev, Date.now() - startedAtRef.current, 0));
    timerRef.current = setInterval(() => {
      setElapsed((prev) => Math.max(prev, Date.now() - startedAtRef.current, 0));
    }, 1000);
    return () => {
      if (timerRef.current) clearInterval(timerRef.current);
    };
  }, [isTerminal, managedJobId]);

  const sharedStatus = useSharedQueryStatus({
    enabled: !isTerminal && Boolean(managedJobId),
    jobId: managedJobId,
    threadId,
    runId: normalizeString(runId) || normalizeString(queryMetadata?.run_id) || undefined,
    ownerToken,
    queryObligationId:
      normalizeQueryObligationId(queryObligationId)
      || normalizeQueryObligationId(queryMetadata?.query_obligation_id),
    startedAtMs: startedAtRef.current,
  });

  useEffect(() => {
    if (!sharedStatus) return;
    const nextStatus = normalizeStatus(sharedStatus.status);
    if (nextStatus && !(cancelledLocallyRef.current && nextStatus === 'running')) {
      setStatus(nextStatus);
      if (nextStatus !== 'cancelled') {
        cancelledLocallyRef.current = false;
        setCancelPending(false);
      }
    }
    if (
      typeof sharedStatus.elapsed_ms === 'number'
      && Number.isFinite(sharedStatus.elapsed_ms)
      && sharedStatus.elapsed_ms >= 0
    ) {
      setElapsed((prev) => Math.max(prev, sharedStatus.elapsed_ms as number));
    }
    if (typeof sharedStatus.error === 'string' && sharedStatus.error.trim()) {
      setError(sharedStatus.error);
    }
    const nextProgress = resolveQueryProgressPercent(
      sharedStatus.progress_percent,
      (sharedStatus as any).progress_pct,
    );
    setStatusPayload((prev) => ({
      ...prev,
      provider_job_id: normalizeString(sharedStatus.provider_job_id) || prev.provider_job_id,
      provider_job_url: normalizeString(sharedStatus.provider_job_url) || prev.provider_job_url,
      warehouse_job_id: normalizeString(sharedStatus.warehouse_job_id) || prev.warehouse_job_id,
      statement_id: normalizeString(sharedStatus.statement_id) || prev.statement_id,
      query_id: normalizeString(sharedStatus.query_id) || prev.query_id,
      project_id: normalizeString(sharedStatus.project_id) || prev.project_id,
      account_identifier: normalizeString(sharedStatus.account_identifier) || prev.account_identifier,
      job_location: normalizeString(sharedStatus.job_location) || normalizeString(sharedStatus.location) || prev.job_location,
      host: normalizeString(sharedStatus.host) || prev.host,
      url: normalizeString(sharedStatus.url) || prev.url,
      progress_percent: nextProgress ?? prev.progress_percent,
    }));
  }, [sharedStatus]);

  const handleCancel = useCallback(async (e: React.MouseEvent) => {
    e.stopPropagation();
    if (isTerminal || cancelledLocallyRef.current || !cancelUrl) return;
    cancelledLocallyRef.current = true;
    setCancelPending(true);

    // Optimistic UI update.
    setStatus('cancelled');
    setError(null);

    try {
      await fetch(cancelUrl, {
        method: 'POST',
        headers: requestHeaders,
      });
    } catch {
      // Best effort -- the UI already shows cancelled.
    }
  }, [cancelUrl, isTerminal, requestHeaders]);

  const providerLabel = getQueryProviderLabel(providerValue);
  const brand = getQueryProviderBrand(providerValue);
  const BrandIcon = brand.icon;
  const showProgress = PROGRESS_VISIBLE_STATUSES.includes(status);
  const progressPercent = useMemo(() => {
    return resolveQueryDisplayProgressPercent({
      status,
      elapsedMs: elapsed,
      progressPercent: statusPayload.progress_percent,
      progressPct: statusPayload.progress_pct,
    }) ?? 0;
  }, [elapsed, status, statusPayload.progress_percent, statusPayload.progress_pct]);
  const nativeLink = useMemo<NativeJobLink | null>(() => {
    return resolveQueryNativeJobLink(providerValue, {
      providerJobId: normalizeString(statusPayload.provider_job_id),
      providerJobUrl: normalizeString(statusPayload.provider_job_url),
      warehouseJobId: normalizeString(statusPayload.warehouse_job_id) || normalizeString(queryMetadata?.warehouse_job_id),
      statementId: normalizeString(statusPayload.statement_id) || normalizeString(queryMetadata?.statement_id),
      queryId: normalizeString(statusPayload.query_id) || normalizeString(queryMetadata?.query_id),
      projectId: normalizeString(statusPayload.project_id) || normalizeString(queryMetadata?.project_id),
      accountIdentifier: normalizeString(statusPayload.account_identifier) || normalizeString(queryMetadata?.account_identifier),
      location: normalizeString(statusPayload.job_location)
        || normalizeString(statusPayload.location)
        || normalizeString(queryMetadata?.job_location)
        || normalizeString(queryMetadata?.location),
      host: normalizeString(statusPayload.host) || normalizeString(queryMetadata?.host),
      url: normalizeString(statusPayload.url) || normalizeString(queryMetadata?.url),
      jobId: managedJobId,
    });
  }, [managedJobId, providerValue, queryMetadata, statusPayload]);

  if (!managedJobId) return null;

  const statusConfig = {
    reserved:  { icon: Loader2, label: 'Query queued...', color: 'text-amber-500', animate: true },
    profiling: { icon: Loader2, label: 'Profiling query before submission...', color: 'text-blue-400', animate: true },
    submission_unknown: { icon: AlertTriangle, label: 'Reconciling query submission...', color: 'text-amber-500', animate: false },
    submitted: { icon: Loader2, label: 'Query submitted...', color: 'text-amber-500', animate: true },
    running:   { icon: Loader2, label: 'Query running...', color: 'text-amber-500', animate: true },
    cancel_requested: { icon: Loader2, label: 'Cancellation requested...', color: 'text-amber-500', animate: true },
    cancelling: { icon: Loader2, label: 'Confirming cancellation...', color: 'text-amber-500', animate: true },
    completed: { icon: CheckCircle2, label: 'Query completed', color: 'text-accent', animate: false },
    failed:    { icon: AlertTriangle, label: 'Query failed', color: 'text-destructive', animate: false },
    cancelled: { icon: XCircle, label: 'Query cancelled', color: 'text-muted-foreground', animate: false },
    expired:   { icon: AlertTriangle, label: 'Query budget expired', color: 'text-destructive', animate: false },
    orphaned:  { icon: AlertTriangle, label: 'Query recovery needed', color: 'text-destructive', animate: false },
    unknown:   { icon: HelpCircle, label: 'Query status unknown', color: 'text-muted-foreground', animate: false },
  };

  const config = statusConfig[status] || statusConfig.running;
  const StatusIcon = config.icon;

  return (
    <Card className={`flex items-center gap-3 p-3 my-2 bg-card/50 border border-border overflow-hidden border-l-[3px] ${brand.accentBorder}`}>
      <div className={`p-2 rounded-lg ${brand.badgeBg} ${brand.badgeText}`}>
        <StatusIcon size={20} className={config.animate ? 'animate-spin' : ''} />
      </div>

      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className={`text-sm font-medium ${config.color}`}>
            {config.label}
          </span>
          <span className="text-xs font-mono text-muted-foreground">
            {formatElapsed(elapsed)}
          </span>
        </div>
        <div className="flex items-center gap-2 mt-0.5 flex-wrap">
          <Badge variant="outline" className={`text-[10px] px-1.5 py-0 gap-1 ${brand.badgeBorder} ${brand.badgeText}`}>
            <BrandIcon size={9} />
            {providerLabel}
          </Badge>
          {nativeLink && (
            <a
              href={nativeLink.url}
              target="_blank"
              rel="noreferrer"
              onClick={(e) => e.stopPropagation()}
              className="text-[11px] text-muted-foreground/70 hover:text-muted-foreground transition-colors font-mono inline-flex items-center gap-1 min-w-0"
              title={`Open ${providerLabel} job ${nativeLink.id}`}
              data-testid="running-query-native-link"
            >
              <span className="truncate max-w-[220px]">{nativeLink.id}</span>
              <ExternalLink size={10} />
            </a>
          )}
          {sqlPreview && (
            <span className="text-[11px] text-muted-foreground/60 font-mono truncate max-w-[300px]">
              {sqlPreview}
            </span>
          )}
        </div>
        {showProgress && (
          <div className="flex items-center gap-2 mt-1.5">
            <div className="h-1.5 flex-1 rounded-full bg-muted/50 overflow-hidden" data-testid="running-query-progress-track">
              <div
                className={`h-full rounded-full transition-[width] duration-500 ease-out ${status === 'unknown' ? 'bg-muted-foreground/70' : brand.progressFill}`}
                style={{ width: `${progressPercent}%` }}
                data-testid="running-query-progress-bar"
              />
            </div>
            <span className="text-[11px] font-mono text-muted-foreground tabular-nums min-w-[2.25rem] text-right">
              {progressPercent}%
            </span>
          </div>
        )}
        {status === 'cancelled' && cancelledLocallyRef.current && (
          <p className="text-xs text-muted-foreground mt-1 truncate">
            {cancelPending ? 'Cancellation requested by user' : 'Cancelled by user'}
          </p>
        )}
        {status === 'failed' && error && (
          <p className="text-xs text-destructive mt-1 truncate">{error}</p>
        )}
        {status === 'unknown' && (
          <p className="text-xs text-muted-foreground mt-1 truncate">
            Query is still tracked, but live status is temporarily unavailable
          </p>
        )}
      </div>

      {!isTerminal && cancelUrl && (
        <Tooltip>
          <TooltipTrigger asChild>
            <button
              type="button"
              onClick={handleCancel}
              className="shrink-0 p-1.5 rounded-md text-muted-foreground/60 hover:text-destructive hover:bg-destructive/10 transition-all"
            >
              <Square size={14} fill="currentColor" />
            </button>
          </TooltipTrigger>
          <TooltipContent>Cancel query</TooltipContent>
        </Tooltip>
      )}
    </Card>
  );
};

export default RunningQueryCard;
