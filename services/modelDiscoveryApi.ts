import type { ProviderConfig } from '@/types';
import { isRecord, requestCapabilityJson, unwrapObjectPayload } from '@/lib/capability/http';
import type { ModelDiscoveryResult } from '@/lib/capability/types';

function asDiscoveryResult(raw: unknown): ModelDiscoveryResult {
  const value = unwrapObjectPayload(raw, ['result', 'data']);
  if (!Array.isArray(value.models) || typeof value.source !== 'string') {
    throw new Error('The server returned an invalid model discovery result.');
  }
  if (value.models.some((model) => !isRecord(model) || typeof model.id !== 'string')) {
    throw new Error('The server returned invalid model discovery entries.');
  }
  return value as unknown as ModelDiscoveryResult;
}

export async function discoverConnectionModels(
  connectionId: string,
  signal?: AbortSignal,
): Promise<ModelDiscoveryResult> {
  const id = connectionId.trim();
  if (!id) throw new Error('Connection id is required.');
  const raw = await requestCapabilityJson<unknown>(
    `/api/providers/connections/${encodeURIComponent(id)}/models/discover`,
    { method: 'POST', body: '{}', signal },
  );
  return asDiscoveryResult(raw);
}

export async function discoverProviderModels(
  config: ProviderConfig,
  signal?: AbortSignal,
): Promise<ModelDiscoveryResult> {
  const raw = await requestCapabilityJson<unknown>('/api/providers/models/discover', {
    method: 'POST',
    body: JSON.stringify({ config }),
    signal,
  });
  return asDiscoveryResult(raw);
}
