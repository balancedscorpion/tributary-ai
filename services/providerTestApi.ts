import type { ProviderConfig } from '@/types';

export type ProviderConnectionTestApiResult = {
  ok: boolean;
  message: string;
  endpoint?: string | null;
  providerId?: string | null;
  model?: string | null;
  raw?: any;
};

async function readJsonSafe(res: Response): Promise<any> {
  const text = await res.text().catch(() => '');
  if (!text.trim()) return null;
  try {
    return JSON.parse(text);
  } catch {
    return { message: text, error: text };
  }
}

function recordFromUnknown(value: unknown): Record<string, unknown> | null {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  return value as Record<string, unknown>;
}

function firstString(obj: Record<string, unknown>, keys: string[]): string {
  for (const key of keys) {
    const value = obj[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return '';
}

function formatProviderError(code: string, message: string): string {
  const cleanCode = code.trim();
  const cleanMessage = message.trim();
  if (!cleanMessage) return cleanCode;
  if (!cleanCode || cleanMessage.toLowerCase().includes(cleanCode.toLowerCase())) return cleanMessage;
  return `${cleanCode}: ${cleanMessage}`;
}

function providerErrorObjectMessage(obj: Record<string, unknown>): string {
  const errorObj = recordFromUnknown(obj.error);
  if (errorObj) {
    const nestedMessage = formatProviderError(
      firstString(errorObj, ['code', 'type']),
      firstString(errorObj, ['message', 'error_description']),
    );
    if (nestedMessage) return nestedMessage;
  }

  if (typeof obj.error === 'string' && obj.error.trim()) {
    const errorText = obj.error.trim();
    const topLevelMessage = firstString(obj, ['message', 'error_description']);
    if (topLevelMessage) return formatProviderError(errorText, topLevelMessage);
    return parseProviderErrorJson(errorText) || errorText;
  }

  return formatProviderError(
    firstString(obj, ['code', 'type']),
    firstString(obj, ['message', 'error_description', 'error']),
  );
}

function parseProviderErrorJson(raw: string): string {
  const trimmed = raw.trim();
  if (!trimmed.startsWith('{')) return '';

  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    return '';
  }

  const obj = recordFromUnknown(parsed);
  return obj ? providerErrorObjectMessage(obj) : '';
}

function cleanProviderErrorMessage(message: string): string {
  const trimmed = message.trim();
  if (!trimmed) return '';
  const parsed = parseProviderErrorJson(trimmed);
  if (parsed) return parsed;

  const jsonStart = trimmed.indexOf('{');
  if (jsonStart > 0) {
    const parsedInner = parseProviderErrorJson(trimmed.slice(jsonStart));
    if (parsedInner) return parsedInner;
  }

  return trimmed;
}

function normalizeProviderTestResult(raw: any, fallbackEndpoint: string): ProviderConnectionTestApiResult {
  const obj = raw && typeof raw === 'object' ? raw : {};
  const structuredMessage = providerErrorObjectMessage(obj);
  const hasStructuredError = Boolean(recordFromUnknown(obj.error));
  let rawMessage = hasStructuredError && structuredMessage ? structuredMessage : '';
  if (!rawMessage && typeof obj.message === 'string' && obj.message.trim()) {
    rawMessage = obj.message.trim();
  }
  if (!rawMessage && typeof obj.error === 'string' && obj.error.trim()) {
    rawMessage = obj.error.trim();
  }
  if (!rawMessage) {
    rawMessage = structuredMessage || 'Provider connection test failed.';
  }
  const msg = cleanProviderErrorMessage(rawMessage) || 'Provider connection test failed.';

  return {
    ok: obj.ok === true,
    message: msg,
    endpoint: typeof obj.endpoint === 'string' && obj.endpoint.trim() ? obj.endpoint.trim() : fallbackEndpoint,
    providerId: typeof obj.providerId === 'string' ? obj.providerId : null,
    model: typeof obj.model === 'string' ? obj.model : null,
    raw,
  };
}

export async function testProviderConnection(
  config: ProviderConfig,
  endpoint: string,
  signal?: AbortSignal,
): Promise<ProviderConnectionTestApiResult> {
  let res: Response;
  try {
    res = await fetch('/api/providers/test', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
      body: JSON.stringify({ config, endpoint }),
      signal,
    });
  } catch (e: any) {
    if (e?.name === 'AbortError') throw e;
    return {
      ok: false,
      message: e?.message || 'Could not reach the local server.',
      endpoint,
      raw: null,
    };
  }

  const raw = await readJsonSafe(res);
  const normalized = normalizeProviderTestResult(raw, endpoint);
  if (!res.ok && normalized.ok) {
    return {
      ...normalized,
      ok: false,
      message: normalized.message || `Provider connection test failed (HTTP ${res.status}).`,
    };
  }
  return normalized;
}
