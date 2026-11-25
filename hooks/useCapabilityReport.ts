import { useCallback, useEffect, useMemo, useState } from 'react';
import type { ProviderConfig } from '@/types';
import type { CapabilityReport } from '@/lib/capability/types';
import {
  getConnectionCapabilityReport,
  resolveCapabilityReport,
} from '@/services/capabilityApi';

interface CacheEntry {
  report: CapabilityReport;
  fetchedAt: number;
}

const reportCache = new Map<string, CacheEntry>();
const DEFAULT_STALE_TIME_MS = 30_000;

function stableValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(stableValue);
  if (value && typeof value === 'object') {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>)
        .filter(([key]) => !['updatedAt', 'lastTestedAt', 'lastTestStatus', 'lastTestMessage'].includes(key))
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([key, child]) => [key, stableValue(child)]),
    );
  }
  return value;
}

function hash(value: string): string {
  let result = 2166136261;
  for (let index = 0; index < value.length; index += 1) {
    result ^= value.charCodeAt(index);
    result = Math.imul(result, 16777619);
  }
  return (result >>> 0).toString(36);
}

export interface UseCapabilityReportOptions {
  connectionId?: string | null;
  config?: ProviderConfig | null;
  model?: string | null;
  enabled?: boolean;
  staleTimeMs?: number;
}

export interface UseCapabilityReportResult {
  report: CapabilityReport | null;
  error: Error | null;
  isLoading: boolean;
  isRefreshing: boolean;
  isStale: boolean;
  refetch: () => void;
}

export function capabilityReportCacheKey(options: UseCapabilityReportOptions): string | null {
  const model = options.model?.trim() || options.config?.model?.trim() || '';
  const connectionId = options.connectionId?.trim();
  if (connectionId) return `connection:${connectionId}:model:${model}`;
  if (!options.config) return null;
  const serialized = JSON.stringify(stableValue({ ...options.config, model }));
  return `draft:${hash(serialized)}`;
}

export function invalidateCapabilityReportCache(predicate?: (key: string) => boolean): void {
  if (!predicate) {
    reportCache.clear();
    return;
  }
  for (const key of reportCache.keys()) {
    if (predicate(key)) reportCache.delete(key);
  }
}

export function invalidateConnectionCapabilityReport(connectionId: string): void {
  const prefix = `connection:${connectionId.trim()}:`;
  invalidateCapabilityReportCache((key) => key.startsWith(prefix));
}

export function useCapabilityReport(options: UseCapabilityReportOptions): UseCapabilityReportResult {
  const enabled = options.enabled !== false;
  const staleTimeMs = options.staleTimeMs ?? DEFAULT_STALE_TIME_MS;
  const cacheKey = useMemo(
    () => capabilityReportCacheKey(options),
    [options.connectionId, options.config, options.model],
  );
  const cached = cacheKey ? reportCache.get(cacheKey) : undefined;
  const [report, setReport] = useState<CapabilityReport | null>(cached?.report || null);
  const [fetchedAt, setFetchedAt] = useState(cached?.fetchedAt || 0);
  const [error, setError] = useState<Error | null>(null);
  const [loading, setLoading] = useState(false);
  const [refreshVersion, setRefreshVersion] = useState(0);
  const [, setStaleTick] = useState(0);

  const refetch = useCallback(() => setRefreshVersion((value) => value + 1), []);

  useEffect(() => {
    const current = cacheKey ? reportCache.get(cacheKey) : undefined;
    setReport(current?.report || null);
    setFetchedAt(current?.fetchedAt || 0);
    setError(null);
  }, [cacheKey]);

  useEffect(() => {
    if (!enabled || !cacheKey) {
      setLoading(false);
      return;
    }

    const current = reportCache.get(cacheKey);
    const fresh = refreshVersion === 0
      && current
      && Date.now() - current.fetchedAt <= staleTimeMs;
    if (fresh) {
      setReport(current.report);
      setFetchedAt(current.fetchedAt);
      setLoading(false);
      return;
    }

    const controller = new AbortController();
    setLoading(true);
    setError(null);
    const request = options.connectionId?.trim()
      ? getConnectionCapabilityReport(options.connectionId, options.model, controller.signal)
      : resolveCapabilityReport(options.config!, options.model, controller.signal);

    request.then((next) => {
      if (controller.signal.aborted) return;
      const now = Date.now();
      reportCache.set(cacheKey, { report: next, fetchedAt: now });
      setReport(next);
      setFetchedAt(now);
    }).catch((cause: unknown) => {
      if (controller.signal.aborted) return;
      setError(cause instanceof Error ? cause : new Error(String(cause)));
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false);
    });

    return () => controller.abort();
  }, [cacheKey, enabled, refreshVersion, staleTimeMs]);

  useEffect(() => {
    if (!fetchedAt || staleTimeMs <= 0) return;
    const delay = Math.max(0, fetchedAt + staleTimeMs - Date.now());
    const timer = window.setTimeout(() => setStaleTick((value) => value + 1), delay + 1);
    return () => window.clearTimeout(timer);
  }, [fetchedAt, staleTimeMs]);

  const isStale = Boolean(report) && (!fetchedAt || Date.now() - fetchedAt > staleTimeMs);
  return {
    report,
    error,
    isLoading: loading && !report,
    isRefreshing: loading && Boolean(report),
    isStale,
    refetch,
  };
}
