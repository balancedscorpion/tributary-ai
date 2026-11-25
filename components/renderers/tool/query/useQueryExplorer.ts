import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { Artifact } from '@/types';

/**
 * useQueryExplorer — connector-agnostic data source for the Query Tool
 * Inspector's results explorer.
 *
 * Behaviour:
 *   - Local mode (default): the envelope already carries `content` as a JSON
 *     dataset. We surface those rows and report `hasMore: false`.
 *
 *   - Live mode: when the artifact carries a `full_artifact_id` we can fetch
 *     additional pages from the server's existing per-artifact rows endpoint —
 *     `/api/artifacts/:id/rows?offset=…&limit=…` — without re-running the
 *     query. `refresh()` re-attaches to the same cached job; we never re-run
 *     the query implicitly. (The user explicitly forbade silent re-runs since
 *     they can be expensive.)
 *
 *   - Search / sort pushdown is NOT implemented server-side today for the
 *     BigQuery and Databricks handlers we inspected, so the Explorer applies
 *     them in memory over loaded rows. The contract is here, so when the
 *     backend exposes server-side search / sort pushdown later, only this
 *     hook needs updating.
 */

export type Row = Record<string, unknown>;

export interface QueryDataSource {
  rows: Row[];
  /** Total rows known to exist server-side, if reported. */
  totalRows: number | null;
  /** Are there more pages available beyond `rows`? */
  hasMore: boolean;
  /**
   * The inspector is attached to a live query (provider + job_id present).
   * The "Live · <provider>" indicator + refresh affordance render based on this.
   */
  isLive: boolean;
  /** A server-side row store is available to fetch additional pages from. */
  canFetchMore: boolean;
  loading: boolean;
  error: Error | null;
  loadMore: () => Promise<void>;
  refresh: () => Promise<void>;
}

function parseInitialRows(content: string | undefined): Row[] {
  if (!content || !content.trim()) return [];
  try {
    const parsed = JSON.parse(content);
    if (Array.isArray(parsed)) return parsed as Row[];
    if (parsed && typeof parsed === 'object') {
      const candidates = ['rows', 'data', 'results', 'records'];
      for (const k of candidates) {
        if (Array.isArray((parsed as any)[k])) return (parsed as any)[k] as Row[];
      }
    }
    return [];
  } catch {
    return [];
  }
}

interface LiveResponse {
  rows?: unknown;
  // The backend's /api/artifacts/:id/rows endpoint reports the dataset total
  // as `row_count_total` and the size of the returned page as `rows_returned`.
  // Accept the legacy aliases too in case other endpoints emit them.
  row_count_total?: number;
  rows_returned?: number;
  total?: number;
  total_rows?: number;
  row_count?: number;
  has_more?: boolean;
  next_offset?: number;
}

function isObj(v: unknown): v is Record<string, unknown> {
  return v !== null && typeof v === 'object' && !Array.isArray(v);
}

async function fetchLivePage(
  artifactId: string,
  offset: number,
  limit: number,
  signal: AbortSignal,
): Promise<{ rows: Row[]; total: number | null; hasMore: boolean; nextOffset: number }> {
  const url = `/api/artifacts/${encodeURIComponent(artifactId)}/rows?offset=${offset}&limit=${limit}`;
  const res = await fetch(url, { signal, credentials: 'same-origin' });
  if (!res.ok) {
    throw new Error(`live fetch failed: ${res.status} ${res.statusText}`);
  }
  const body = (await res.json()) as LiveResponse;
  const rawRows = Array.isArray(body?.rows) ? body.rows : [];
  const rows = rawRows.filter(isObj) as Row[];
  const total =
    typeof body?.row_count_total === 'number'
      ? body.row_count_total
      : typeof body?.total === 'number'
        ? body.total
        : typeof body?.total_rows === 'number'
          ? body.total_rows
          : typeof body?.row_count === 'number'
            ? body.row_count
            : null;
  const hasMore =
    typeof body?.has_more === 'boolean'
      ? body.has_more
      : total != null
        ? offset + rows.length < total
        : rows.length === limit;
  const nextOffset =
    typeof body?.next_offset === 'number' && Number.isFinite(body.next_offset)
      ? body.next_offset
      : offset + rows.length;
  return { rows, total, hasMore, nextOffset };
}

export function useQueryExplorer(
  artifact: Artifact,
  initialPageSize = 50,
): QueryDataSource {
  const meta = (artifact.metadata || {}) as Record<string, any>;
  const resultMeta = isObj(meta.result_metadata) ? (meta.result_metadata as Record<string, any>) : {};
  const resultCounts = isObj(resultMeta.result_counts) ? (resultMeta.result_counts as Record<string, any>) : {};
  const qm = isObj(meta.query_metadata) ? meta.query_metadata : {};
  const retrieval = isObj(meta.retrieval)
    ? meta.retrieval
    : isObj(resultMeta.retrieval)
      ? resultMeta.retrieval
      : {};
  const modelContextCompacted =
    meta.model_context_compacted === true ||
    resultMeta.model_context_compacted === true ||
    resultMeta.compacted === true;
  const fullArtifactId =
    (typeof meta.full_artifact_id === 'string' && meta.full_artifact_id.trim()) ||
    (typeof resultMeta.full_artifact_id === 'string' && resultMeta.full_artifact_id.trim()) ||
    (typeof qm.full_artifact_id === 'string' && qm.full_artifact_id.trim()) ||
    (typeof retrieval.artifact_id === 'string' && retrieval.artifact_id.trim()) ||
    (modelContextCompacted ? artifact.id : null);
  // A query is "live" when there's a real attached job — provider + a job
  // identifier from the backend. The Live indicator + refresh chrome render
  // off this. Whether we can ALSO fetch more pages on demand is a separate
  // capability (canFetchMore), which currently requires full_artifact_id.
  const jobIdentifier =
    (typeof qm.job_id === 'string' && qm.job_id) ||
    (typeof qm.statement_id === 'string' && qm.statement_id) ||
    (typeof qm.query_id === 'string' && qm.query_id) ||
    '';
  const provider =
    (typeof qm.provider === 'string' && qm.provider) ||
    (typeof meta.provider === 'string' && meta.provider) ||
    '';
  const isLive = !!(jobIdentifier && provider);
  const canFetchMore = !!fullArtifactId;

  const initialRows = useMemo(() => parseInitialRows(artifact.content), [artifact.content]);
  const reportedTotal =
    typeof meta.row_count_total === 'number'
      ? meta.row_count_total
      : typeof resultMeta.row_count_total === 'number'
        ? resultMeta.row_count_total
        : typeof resultCounts.rows === 'number'
          ? resultCounts.rows
          : typeof qm.row_count_total === 'number'
            ? qm.row_count_total
            : typeof qm.rows_returned === 'number'
              ? qm.rows_returned
              : typeof qm.row_count === 'number'
                ? qm.row_count
              : null;

  const [rows, setRows] = useState<Row[]>(initialRows);
  const [totalRows, setTotalRows] = useState<number | null>(reportedTotal);
  const [hasMore, setHasMore] = useState<boolean>(
    canFetchMore && (reportedTotal == null || reportedTotal > initialRows.length),
  );
  const [nextOffset, setNextOffset] = useState<number>(initialRows.length);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  const autoRefreshKeyRef = useRef<string | null>(null);

  // Re-seed when the artifact changes (e.g. user opens a different result).
  useEffect(() => {
    autoRefreshKeyRef.current = null;
    setRows(initialRows);
    setTotalRows(reportedTotal);
    setHasMore(canFetchMore && (reportedTotal == null || reportedTotal > initialRows.length));
    setNextOffset(initialRows.length);
    setError(null);
  }, [artifact.id, initialRows, reportedTotal, canFetchMore]);

  useEffect(
    () => () => {
      abortRef.current?.abort();
    },
    [],
  );

  const loadMore = useCallback(async () => {
    if (!fullArtifactId || loading || !hasMore) return;
    abortRef.current?.abort();
    const ctl = new AbortController();
    abortRef.current = ctl;
    setLoading(true);
    setError(null);
    try {
      const { rows: more, total, hasMore: more$, nextOffset: next } = await fetchLivePage(
        fullArtifactId,
        nextOffset,
        initialPageSize,
        ctl.signal,
      );
      setRows((prev) => prev.concat(more));
      if (total != null) setTotalRows(total);
      setHasMore(more$);
      setNextOffset(next);
    } catch (e) {
      if ((e as DOMException)?.name === 'AbortError') return;
      setError(e instanceof Error ? e : new Error(String(e)));
    } finally {
      if (abortRef.current === ctl) setLoading(false);
    }
  }, [fullArtifactId, hasMore, initialPageSize, loading, nextOffset]);

  const refresh = useCallback(async () => {
    if (!fullArtifactId) {
      // local mode — re-parse what we already have
      setRows(parseInitialRows(artifact.content));
      setError(null);
      return;
    }
    abortRef.current?.abort();
    const ctl = new AbortController();
    abortRef.current = ctl;
    setLoading(true);
    setError(null);
    try {
      const { rows: page, total, hasMore: more$, nextOffset: next } = await fetchLivePage(
        fullArtifactId,
        0,
        initialPageSize,
        ctl.signal,
      );
      setRows(page);
      if (total != null) setTotalRows(total);
      setHasMore(more$);
      setNextOffset(next);
    } catch (e) {
      if ((e as DOMException)?.name === 'AbortError') return;
      setError(e instanceof Error ? e : new Error(String(e)));
    } finally {
      if (abortRef.current === ctl) setLoading(false);
    }
  }, [artifact.content, fullArtifactId, initialPageSize]);

  useEffect(() => {
    if (!fullArtifactId) return;
    if (initialRows.length >= initialPageSize) return;
    if (reportedTotal != null && reportedTotal <= initialRows.length) return;
    const key = `${artifact.id}:${fullArtifactId}:${initialPageSize}:${initialRows.length}:${reportedTotal ?? 'unknown'}`;
    if (autoRefreshKeyRef.current === key) return;
    autoRefreshKeyRef.current = key;
    void refresh();
  }, [artifact.id, fullArtifactId, initialRows.length, initialPageSize, refresh, reportedTotal]);

  return { rows, totalRows, hasMore, isLive, canFetchMore, loading, error, loadMore, refresh };
}
