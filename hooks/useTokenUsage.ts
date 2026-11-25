import { useCallback, useEffect, useRef, useState } from 'react';
import {
  getDailyTokenUsage,
  TokenUsageApiError,
  type TokenUsageQuery,
  type TokenUsageReport,
} from '@/services/tokenUsageApi';

export type TokenUsageStatus = 'idle' | 'loading' | 'success' | 'error' | 'unavailable';

export interface TokenUsageState {
  status: TokenUsageStatus;
  data: TokenUsageReport | null;
  error: string | null;
  isRefreshing: boolean;
  refresh: () => void;
}

/**
 * Loads usage only while its read-only Settings tab is visible. Every query
 * change and unmount aborts the previous request so stale date ranges can
 * never replace the current report.
 */
export function useTokenUsage(active: boolean, query: TokenUsageQuery): TokenUsageState {
  const [status, setStatus] = useState<TokenUsageStatus>('idle');
  const [data, setData] = useState<TokenUsageReport | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [refreshKey, setRefreshKey] = useState(0);
  const requestSequence = useRef(0);
  const dataQueryKey = useRef<string | null>(null);

  const refresh = useCallback(() => {
    if (active) setRefreshKey((value) => value + 1);
  }, [active]);

  useEffect(() => {
    if (!active) {
      requestSequence.current += 1;
      setIsRefreshing(false);
      return;
    }

    const sequence = ++requestSequence.current;
    const controller = new AbortController();
    const queryKey = `${query.from}\u0000${query.to}\u0000${query.timezone}`;
    const refreshingCurrentQuery = data !== null && dataQueryKey.current === queryKey;
    if (!refreshingCurrentQuery) setData(null);
    setStatus(refreshingCurrentQuery ? 'success' : 'loading');
    setIsRefreshing(refreshingCurrentQuery);
    setError(null);

    void getDailyTokenUsage(query, controller.signal).then((report) => {
      if (controller.signal.aborted || sequence !== requestSequence.current) return;
      dataQueryKey.current = queryKey;
      setData(report);
      setStatus('success');
      setIsRefreshing(false);
    }).catch((cause: unknown) => {
      if (controller.signal.aborted || sequence !== requestSequence.current) return;
      if (cause instanceof DOMException && cause.name === 'AbortError') return;
      const unavailable = cause instanceof TokenUsageApiError && cause.unavailable;
      dataQueryKey.current = null;
      setData(null);
      setStatus(unavailable ? 'unavailable' : 'error');
      setError(cause instanceof Error ? cause.message : 'Could not load token usage.');
      setIsRefreshing(false);
    });

    return () => controller.abort();
    // `data` is deliberately excluded: retaining it distinguishes refreshes
    // from first load without causing a completed request to fetch again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, query.from, query.to, query.timezone, refreshKey]);

  return {
    status,
    data,
    error,
    isRefreshing,
    refresh,
  };
}
