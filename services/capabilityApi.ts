import type { ProviderConfig } from '@/types';
import { isRecord, requestCapabilityJson, unwrapObjectPayload } from '@/lib/capability/http';
import type {
  CapabilityReport,
  PreflightRequest,
  PreflightResult,
  RunPlanRecord,
  ThreadTarget,
} from '@/lib/capability/types';

function asCapabilityReport(raw: unknown): CapabilityReport {
  const value = unwrapObjectPayload(raw, ['report', 'data']);
  if (!isRecord(value.target) || !isRecord(value.features)) {
    throw new Error('The server returned an invalid capability report.');
  }
  return value as unknown as CapabilityReport;
}

export async function getConnectionCapabilityReport(
  connectionId: string,
  model?: string | null,
  signal?: AbortSignal,
): Promise<CapabilityReport> {
  const id = connectionId.trim();
  if (!id) throw new Error('Connection id is required.');
  const params = new URLSearchParams();
  if (model?.trim()) params.set('model', model.trim());
  const query = params.size > 0 ? `?${params.toString()}` : '';
  const raw = await requestCapabilityJson<unknown>(
    `/api/providers/connections/${encodeURIComponent(id)}/capabilities${query}`,
    { method: 'GET', signal },
  );
  return asCapabilityReport(raw);
}

export async function resolveCapabilityReport(
  config: ProviderConfig,
  model?: string | null,
  signal?: AbortSignal,
): Promise<CapabilityReport> {
  const raw = await requestCapabilityJson<unknown>('/api/providers/capabilities/resolve', {
    method: 'POST',
    body: JSON.stringify({ config, model: model?.trim() || config.model || '' }),
    signal,
  });
  return asCapabilityReport(raw);
}

export async function preflightThreadRun(
  threadId: string,
  request: PreflightRequest,
  signal?: AbortSignal,
): Promise<PreflightResult> {
  const id = threadId.trim();
  if (!id) throw new Error('Thread id is required.');
  const raw = await requestCapabilityJson<unknown>(
    `/api/threads/${encodeURIComponent(id)}/runs/preflight`,
    { method: 'POST', body: JSON.stringify(request), signal },
  );
  const value = unwrapObjectPayload(raw, ['preflight', 'data']);
  if (typeof value.planId !== 'string' || typeof value.status !== 'string') {
    throw new Error('The server returned an invalid preflight result.');
  }
  return value as unknown as PreflightResult;
}

export async function getRunCapabilityPlan(
  runId: string,
  signal?: AbortSignal,
): Promise<RunPlanRecord> {
  const id = runId.trim();
  if (!id) throw new Error('Run id is required.');
  const raw = await requestCapabilityJson<unknown>(
    `/api/runs/${encodeURIComponent(id)}/plan`,
    { method: 'GET', signal },
  );
  // The persisted DTO itself carries a `plan` field (the CapabilityReport), so
  // only unwrap a `{plan: …}` envelope when the top level is not already the DTO.
  const value = isRecord(raw) && typeof raw.planId === 'string'
    ? raw
    : unwrapObjectPayload(raw, ['plan', 'data']);
  if (typeof value.planId !== 'string') throw new Error('The server returned an invalid run plan.');
  return value as unknown as RunPlanRecord;
}

export async function updateThreadTarget(
  threadId: string,
  target: ThreadTarget,
  signal?: AbortSignal,
): Promise<void> {
  const id = threadId.trim();
  if (!id) throw new Error('Thread id is required.');
  await requestCapabilityJson<unknown>(`/api/threads/${encodeURIComponent(id)}/target`, {
    method: 'PUT',
    body: JSON.stringify(target),
    signal,
  });
}

export { CapabilityApiError } from '@/lib/capability/http';
