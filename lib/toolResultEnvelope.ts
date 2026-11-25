import type { AsyncQueryHandle, ToolResultEnvelope } from '@/types';
import { isQueryProvider } from '@/lib/queryProviderRegistry';
import { isActiveQueryStatus } from '@/lib/queryProgress';

function isPlainObject(value: any): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

export function tryParseEnvelope(content: string | null): ToolResultEnvelope | null {
  if (!content) return null;
  try {
    const obj = JSON.parse(content);
    if (!isPlainObject(obj)) return null;
    if (typeof obj.artifact_id !== 'string') return null;
    return obj as ToolResultEnvelope;
  } catch {
    return null;
  }
}

export function isAsyncQueryHandle(result: any): result is AsyncQueryHandle {
  const provider = isPlainObject(result) ? (result as any).provider : undefined;
  return (
    isPlainObject(result)
    && typeof result.job_id === 'string'
    && typeof result.status === 'string'
    && isActiveQueryStatus(result.status)
    && (provider === undefined || isQueryProvider(provider))
  );
}
