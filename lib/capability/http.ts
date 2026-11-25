import type { CapabilityApiErrorBody } from './types';

export class CapabilityApiError extends Error {
  readonly status: number;
  readonly code: string | null;
  readonly body: unknown;

  constructor(message: string, status: number, code: string | null, body: unknown) {
    super(message);
    this.name = 'CapabilityApiError';
    this.status = status;
    this.code = code;
    this.body = body;
  }
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

async function readJsonSafe(response: Response): Promise<unknown> {
  const text = await response.text().catch(() => '');
  if (!text.trim()) return null;
  try {
    return JSON.parse(text);
  } catch {
    return { message: text, error: text } satisfies CapabilityApiErrorBody;
  }
}

function stringField(value: unknown): string | null {
  return typeof value === 'string' && value.trim() ? value.trim() : null;
}

function errorDetails(body: unknown, fallback: string): { message: string; code: string | null } {
  if (!isRecord(body)) return { message: fallback, code: null };

  const nested = isRecord(body.error) ? body.error : null;
  const message = stringField(nested?.message)
    || stringField(body.message)
    || stringField(typeof body.error === 'string' ? body.error : null)
    || fallback;
  const code = stringField(nested?.code)
    || stringField(nested?.type)
    || stringField(body.code)
    || (typeof body.error === 'string' && !body.error.includes(' ') ? body.error : null);
  return { message, code };
}

export async function requestCapabilityJson<T>(url: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  if (!headers.has('Accept')) headers.set('Accept', 'application/json');
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
  let response: Response;
  try {
    response = await fetch(url, {
      ...init,
      headers,
    });
  } catch (error: any) {
    if (error?.name === 'AbortError') throw error;
    throw new CapabilityApiError(
      error?.message || 'Could not reach the local server.',
      0,
      'network_error',
      null,
    );
  }

  const body = await readJsonSafe(response);
  if (!response.ok) {
    const details = errorDetails(body, `HTTP ${response.status}`);
    throw new CapabilityApiError(details.message, response.status, details.code, body);
  }
  return body as T;
}

export function unwrapObjectPayload(raw: unknown, keys: readonly string[]): Record<string, unknown> {
  if (!isRecord(raw)) throw new Error('The server returned an invalid JSON response.');
  for (const key of keys) {
    if (isRecord(raw[key])) return raw[key] as Record<string, unknown>;
  }
  return raw;
}
