import { isRecord, requestCapabilityJson, unwrapObjectPayload } from '@/lib/capability/http';
import type {
  ProviderCheckProbeResult,
  ProviderCheckRequest,
  ProviderCheckResult,
} from '@/lib/capability/types';

function legacyProbeResult(value: Record<string, unknown>): ProviderCheckProbeResult {
  const ok = value.ok === true;
  return {
    probe: 'transport',
    verdict: ok ? 'verified' : 'failed',
    reason: ok ? null : 'transport_failed',
    message: typeof value.message === 'string'
      ? value.message
      : ok ? 'Connection succeeded.' : 'Connection failed.',
    requestId: null,
    durationMs: 0,
  };
}

function asCheckResult(raw: unknown): ProviderCheckResult {
  const value = unwrapObjectPayload(raw, ['result', 'data']);
  if (Array.isArray(value.results)) {
    if (value.results.some((result) => !isRecord(result) || typeof result.probe !== 'string')) {
      throw new Error('The server returned invalid capability check entries.');
    }
    return value as unknown as ProviderCheckResult;
  }

  // The endpoint's existing quick-check shape remains valid during rollout.
  if (typeof value.ok === 'boolean') {
    return {
      ...value,
      ok: value.ok,
      fingerprint: typeof value.fingerprint === 'string' ? value.fingerprint : 'legacy',
      results: [legacyProbeResult(value)],
    } as ProviderCheckResult;
  }
  throw new Error('The server returned an invalid provider check result.');
}

export async function runProviderCheck(
  request: ProviderCheckRequest,
  signal?: AbortSignal,
): Promise<ProviderCheckResult> {
  if (request.tier === 'capability' && (!request.probes || request.probes.length === 0)) {
    throw new Error('At least one capability probe is required.');
  }
  if (!request.config && !request.connectionId) {
    throw new Error('A provider config or connection id is required.');
  }

  const raw = await requestCapabilityJson<unknown>('/api/providers/test', {
    method: 'POST',
    body: JSON.stringify(request),
    signal,
  });
  return asCheckResult(raw);
}
