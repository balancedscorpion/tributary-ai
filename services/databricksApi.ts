export type DatabricksAuthMode = 'refresh_token' | 'pat' | 'none';

export type DatabricksStatus = {
  authMode: DatabricksAuthMode;
  hostConfigured: boolean;
  warehouseConfigured: boolean;
  host: string | null;
  warehouseId: string | null;
  hostValid: boolean;
  hostValidationMessage: string | null;
  raw: any;
};

export type DatabricksTestResult = {
  ok: boolean;
  warehouseName: string | null;
  warehouseState: string | null;
  message: string | null;
  raw: any;
};

export type DatabricksOAuthStartResult = {
  authorizeUrl: string;
  state: string;
  expiresInSeconds: number;
  clientId: string | null;
  redirectUri: string | null;
  host: string | null;
  raw: any;
};

export type DatabricksOAuthPollResult = {
  status: 'pending' | 'success' | 'error';
  ok: boolean;
  message: string | null;
  error: string | null;
  raw: any;
};

export class DatabricksApiError extends Error {
  status: number;
  payload: any;

  constructor(message: string, status: number, payload: any) {
    super(message);
    this.name = 'DatabricksApiError';
    this.status = status;
    this.payload = payload;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function pickString(raw: any, keys: string[]): string | null {
  if (!isRecord(raw)) return null;
  for (const k of keys) {
    const v = (raw as any)[k];
    if (typeof v === 'string' && v.trim()) return v.trim();
  }
  return null;
}

function pickNumber(raw: any, keys: string[]): number | null {
  if (!isRecord(raw)) return null;
  for (const k of keys) {
    const v = (raw as any)[k];
    const n = typeof v === 'number' ? v : Number(v);
    if (Number.isFinite(n)) return n;
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

function extractErrorMessage(data: any, status: number): string {
  if (data && typeof data === 'object') {
    const msg =
      (typeof (data as any).message === 'string' && (data as any).message) ||
      (typeof (data as any).error === 'string' && (data as any).error) ||
      '';
    if (msg.trim()) return msg.trim();
  }
  return `HTTP ${status}`;
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
    throw new DatabricksApiError(extractErrorMessage(data, res.status), res.status, data);
  }
  return data;
}

export function normalizeDatabricksStatus(raw: any): DatabricksStatus {
  const unwrapped = isRecord(raw) ? raw : {};
  const authMode: DatabricksAuthMode =
    pickString(unwrapped, ['auth_mode', 'authMode']) as DatabricksAuthMode || 'none';
  const hostConfigured = Boolean(
    (unwrapped as any).host_configured ?? (unwrapped as any).hostConfigured,
  );
  const warehouseConfigured = Boolean(
    (unwrapped as any).warehouse_configured ?? (unwrapped as any).warehouseConfigured,
  );
  const hostValid = Boolean(
    (unwrapped as any).host_valid ?? (unwrapped as any).hostValid,
  );
  const host = pickString(unwrapped, ['host']);
  const warehouseId = pickString(unwrapped, ['warehouse_id', 'warehouseId']);
  const hostValidationMessage = pickString(unwrapped, [
    'host_validation_message',
    'hostValidationMessage',
  ]);
  return {
    authMode,
    hostConfigured,
    warehouseConfigured,
    host,
    warehouseId,
    hostValid,
    hostValidationMessage,
    raw,
  };
}

export async function getDatabricksStatus(): Promise<DatabricksStatus> {
  const raw = await fetchJson('/api/databricks/status', { method: 'GET' });
  return normalizeDatabricksStatus(raw);
}

export function normalizeDatabricksTestResult(raw: any): DatabricksTestResult {
  const unwrapped = isRecord(raw) ? raw : {};
  const ok = Boolean((unwrapped as any).ok);
  const warehouseName = pickString(unwrapped, ['warehouse_name', 'warehouseName']);
  const warehouseState = pickString(unwrapped, ['warehouse_state', 'warehouseState']);
  const message = pickString(unwrapped, ['message']);
  return { ok, warehouseName, warehouseState, message, raw };
}

export async function testDatabricksConnection(input?: {
  host?: string;
  warehouseId?: string;
}): Promise<DatabricksTestResult> {
  const host = typeof input?.host === 'string' ? input.host.trim() : '';
  const warehouseId = typeof input?.warehouseId === 'string' ? input.warehouseId.trim() : '';
  const raw = await fetchJson('/api/databricks/test', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      ...(host ? { host } : {}),
      ...(warehouseId ? { warehouse_id: warehouseId } : {}),
    }),
  });
  return normalizeDatabricksTestResult(raw);
}

export function normalizeDatabricksOAuthStart(raw: any): DatabricksOAuthStartResult {
  const unwrapped = isRecord(raw) ? raw : {};
  const authorizeUrl = pickString(unwrapped, ['authorize_url', 'authorizeUrl']) || '';
  const state = pickString(unwrapped, ['state']) || '';
  const expiresInSeconds = pickNumber(unwrapped, ['expires_in_seconds', 'expiresInSeconds']) ?? 0;
  const clientId = pickString(unwrapped, ['client_id', 'clientId']);
  const redirectUri = pickString(unwrapped, ['redirect_uri', 'redirectUri']);
  const host = pickString(unwrapped, ['host']);
  return {
    authorizeUrl,
    state,
    expiresInSeconds,
    clientId,
    redirectUri,
    host,
    raw,
  };
}

export async function startDatabricksOAuth(input?: {
  host?: string;
  clientId?: string;
}): Promise<DatabricksOAuthStartResult> {
  const host = typeof input?.host === 'string' ? input.host.trim() : '';
  const clientId = typeof input?.clientId === 'string' ? input.clientId.trim() : '';
  const raw = await fetchJson('/api/databricks/oauth/start', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      ...(host ? { host } : {}),
      ...(clientId ? { clientId } : {}),
    }),
  });
  const normalized = normalizeDatabricksOAuthStart(raw);
  if (!normalized.authorizeUrl || !normalized.state) {
    throw new DatabricksApiError('Invalid OAuth start response', 502, raw);
  }
  return normalized;
}

export function normalizeDatabricksOAuthPoll(raw: any): DatabricksOAuthPollResult {
  const unwrapped = isRecord(raw) ? raw : {};
  const statusRaw = pickString(unwrapped, ['status']) || 'pending';
  const status: DatabricksOAuthPollResult['status'] =
    statusRaw === 'success' || statusRaw === 'error' ? statusRaw : 'pending';
  const ok = Boolean((unwrapped as any).ok);
  const message = pickString(unwrapped, ['message']);
  const error = pickString(unwrapped, ['error']);
  return { status, ok, message, error, raw };
}

export async function getDatabricksOAuthResult(state: string): Promise<DatabricksOAuthPollResult> {
  const stateTrimmed = (state || '').trim();
  if (!stateTrimmed) {
    throw new DatabricksApiError('OAuth state is required', 400, null);
  }
  const raw = await fetchJson(`/api/databricks/oauth/result?state=${encodeURIComponent(stateTrimmed)}`, {
    method: 'GET',
  });
  return normalizeDatabricksOAuthPoll(raw);
}

export async function completeDatabricksOAuth(input: {
  state: string;
  code: string;
}): Promise<DatabricksOAuthPollResult> {
  const state = typeof input?.state === 'string' ? input.state.trim() : '';
  const code = typeof input?.code === 'string' ? input.code.trim() : '';
  if (!state) {
    throw new DatabricksApiError('OAuth state is required', 400, null);
  }
  if (!code) {
    throw new DatabricksApiError('Authorization code is required', 400, null);
  }

  const raw = await fetchJson('/api/databricks/oauth/complete', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ state, code }),
  });
  return normalizeDatabricksOAuthPoll(raw);
}
