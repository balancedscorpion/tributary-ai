/**
 * Thin adapter between services/modelDiscoveryApi and the ModelDiscoveryList
 * visual component. 'service_missing' (endpoint absent / server unreachable) is
 * an expected condition: the editor renders plain manual model entry, silently.
 */
import { useCallback, useEffect, useRef, useState } from 'react';
import { discoverProviderModels } from '@/services/modelDiscoveryApi';
import { CapabilityApiError } from '@/lib/capability/http';
import type { ModelDiscoveryResult } from '@/lib/capability/types';
import type { ProviderConfig } from '@/types';

export type { DiscoveredModel, ModelDiscoveryResult, ModelLifecycle } from '@/lib/capability/types';

export type ModelDiscoveryStatus = 'idle' | 'loading' | 'ready' | 'service_missing' | 'error';

export interface ModelDiscoveryState {
  status: ModelDiscoveryStatus;
  result: ModelDiscoveryResult | null;
  errorMessage: string | null;
  refresh: () => void;
}

function isServiceMissing(error: unknown): boolean {
  return error instanceof CapabilityApiError
    && (error.status === 404 || error.status === 405 || error.status === 501 || error.status === 0);
}

export function useModelDiscoverySeam(config: ProviderConfig | null, enabled: boolean): ModelDiscoveryState {
  const [status, setStatus] = useState<ModelDiscoveryStatus>('idle');
  const [result, setResult] = useState<ModelDiscoveryResult | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [nonce, setNonce] = useState(0);
  const abortRef = useRef<AbortController | null>(null);

  const identity = config
    ? JSON.stringify({
      providerId: config.providerId,
      apiType: config.apiType || null,
      baseUrl: config.baseUrl || null,
      region: config.region || null,
    })
    : null;

  useEffect(() => {
    abortRef.current?.abort();
    abortRef.current = null;
    if (!enabled || !config || !identity) {
      setStatus('idle');
      setResult(null);
      setErrorMessage(null);
      return;
    }

    const controller = new AbortController();
    abortRef.current = controller;
    setStatus('loading');

    void (async () => {
      try {
        const next = await discoverProviderModels(config, controller.signal);
        if (controller.signal.aborted) return;
        setResult(next);
        setStatus('ready');
      } catch (e: any) {
        if (e?.name === 'AbortError' || controller.signal.aborted) return;
        if (isServiceMissing(e)) {
          setStatus('service_missing');
          setResult(null);
          return;
        }
        setStatus('error');
        setErrorMessage(e?.message || 'Model discovery failed.');
      }
    })();

    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [identity, enabled, nonce]);

  const refresh = useCallback(() => setNonce((n) => n + 1), []);

  return { status, result, errorMessage, refresh };
}
