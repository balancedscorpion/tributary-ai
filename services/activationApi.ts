export type NormalizedActivationStatus = {
  activated: boolean | null;
  deviceId: string | null;
  deviceIdSource: string | null;
  expiresAt: string | null;
  raw: any;
};

export class ActivationApiError extends Error {
  status: number;
  code: string | null;
  payload: any;

  constructor(message: string, status: number, code: string | null, payload: any) {
    super(message);
    this.name = 'ActivationApiError';
    this.status = status;
    this.code = code;
    this.payload = payload;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function coerceBool(value: unknown): boolean | null {
  if (typeof value === 'boolean') return value;
  if (typeof value === 'number' && Number.isFinite(value)) return value !== 0;
  if (typeof value === 'string') {
    const v = value.trim().toLowerCase();
    if (v === 'true' || v === '1' || v === 'yes' || v === 'y') return true;
    if (v === 'false' || v === '0' || v === 'no' || v === 'n') return false;
  }
  return null;
}

function pickString(raw: any, keys: string[]): string | null {
  if (!isRecord(raw)) return null;
  for (const key of keys) {
    const value = (raw as any)[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return null;
}

function pickBool(raw: any, keys: string[]): boolean | null {
  if (!isRecord(raw)) return null;
  for (const key of keys) {
    const value = (raw as any)[key];
    const next = coerceBool(value);
    if (next !== null) return next;
  }
  return null;
}

async function readJsonSafe(res: Response): Promise<any> {
  const text = await res.text().catch(() => '');
  if (!text.trim()) return null;
  try {
    return JSON.parse(text);
  } catch {
    return { error: text };
  }
}

async function fetchJson(url: string, init?: RequestInit): Promise<any> {
  const res = await fetch(url, {
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init?.headers || {}),
    },
  });
  const data = await readJsonSafe(res);
  if (!res.ok) {
    const code =
      (data && typeof data === 'object' && typeof (data as any).code === 'string' && (data as any).code) ||
      null;
    const msg =
      (data && typeof data === 'object' && typeof (data as any).error === 'string' && (data as any).error) ||
      (data && typeof data === 'object' && typeof (data as any).message === 'string' && (data as any).message) ||
      `HTTP ${res.status}`;
    throw new ActivationApiError(msg, res.status, code, data);
  }
  return data;
}

function unwrapPayload(raw: any): any {
  if (raw && typeof raw === 'object') {
    const maybeStatus = (raw as any).status;
    if (maybeStatus && typeof maybeStatus === 'object') return maybeStatus;
    const maybeData = (raw as any).data;
    if (maybeData && typeof maybeData === 'object') return maybeData;
  }
  return raw;
}

export function normalizeActivationStatus(raw: any): NormalizedActivationStatus {
  const activated = pickBool(raw, ['activated', 'isActivated', 'active', 'valid']);
  const deviceId = pickString(raw, ['device_id', 'deviceId', 'deviceID', 'fingerprint', 'machine_id', 'machineId', 'id']);
  const deviceIdSource = pickString(raw, ['device_id_source', 'deviceIdSource', 'deviceIDSource', 'source']);
  const expiresAt = pickString(raw, ['expires_at', 'expiresAt', 'expiry', 'expiryDate']);
  return { activated, deviceId, deviceIdSource, expiresAt, raw };
}

export async function getActivationStatus(): Promise<NormalizedActivationStatus> {
  const raw = unwrapPayload(await fetchJson('/api/activation/status', { method: 'GET' }));
  return normalizeActivationStatus(raw);
}

export async function installActivationKey(key: string): Promise<void> {
  await fetchJson('/api/activation/install', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token: key }),
  });
}

export async function clearActivation(): Promise<void> {
  try {
    await fetchJson('/api/activation/clear', { method: 'POST' });
    return;
  } catch (e: any) {
    const msg = String(e?.message || e);
    if (!msg.toLowerCase().includes('405')) throw e;
  }

  await fetchJson('/api/activation/clear', { method: 'DELETE' });
}
