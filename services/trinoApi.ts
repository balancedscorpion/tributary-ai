export type TrinoAuthMode = 'basic' | 'jwt' | 'none';

export type TrinoStatus = {
  authMode: TrinoAuthMode;
  hostConfigured: boolean;
  catalogConfigured: boolean;
  schemaConfigured: boolean;
  userConfigured: boolean;
  sourceConfigured: boolean;
  host: string | null;
  catalog: string | null;
  schema: string | null;
  user: string | null;
  source: string | null;
  raw: any;
};

export type TrinoTestResult = {
  ok: boolean;
  message: string | null;
  queryId: string | null;
  serverVersion: string | null;
  raw: any;
};

export class TrinoApiError extends Error {
  status: number;
  payload: any;

  constructor(message: string, status: number, payload: any) {
    super(message);
    this.name = 'TrinoApiError';
    this.status = status;
    this.payload = payload;
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value);
}

function pickString(raw: any, keys: string[]): string | null {
  if (!isRecord(raw)) return null;
  for (const key of keys) {
    const value = (raw as any)[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return null;
}

function pickBool(raw: any, keys: string[]): boolean {
  if (!isRecord(raw)) return false;
  for (const key of keys) {
    const value = (raw as any)[key];
    if (typeof value === 'boolean') return value;
    if (typeof value === 'number' && Number.isFinite(value)) return value !== 0;
    if (typeof value === 'string') {
      const normalized = value.trim().toLowerCase();
      if (normalized === 'true' || normalized === '1' || normalized === 'yes') return true;
      if (normalized === 'false' || normalized === '0' || normalized === 'no') return false;
    }
  }
  return false;
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
    throw new TrinoApiError(extractErrorMessage(data, res.status), res.status, data);
  }
  return data;
}

export function normalizeTrinoStatus(raw: any): TrinoStatus {
  const unwrapped = isRecord(raw) ? raw : {};
  const authModeRaw = pickString(unwrapped, ['auth_mode', 'authMode']) || 'none';
  const authMode: TrinoAuthMode =
    authModeRaw === 'basic' || authModeRaw === 'jwt' || authModeRaw === 'none'
      ? authModeRaw
      : 'none';
  return {
    authMode,
    hostConfigured: pickBool(unwrapped, ['host_configured', 'hostConfigured']),
    catalogConfigured: pickBool(unwrapped, ['catalog_configured', 'catalogConfigured']),
    schemaConfigured: pickBool(unwrapped, ['schema_configured', 'schemaConfigured']),
    userConfigured: pickBool(unwrapped, ['user_configured', 'userConfigured']),
    sourceConfigured: pickBool(unwrapped, ['source_configured', 'sourceConfigured']),
    host: pickString(unwrapped, ['host']),
    catalog: pickString(unwrapped, ['catalog']),
    schema: pickString(unwrapped, ['schema']),
    user: pickString(unwrapped, ['user']),
    source: pickString(unwrapped, ['source']),
    raw,
  };
}

export async function getTrinoStatus(): Promise<TrinoStatus> {
  const raw = await fetchJson('/api/trino/status', { method: 'GET' });
  return normalizeTrinoStatus(raw);
}

export function normalizeTrinoTestResult(raw: any): TrinoTestResult {
  const unwrapped = isRecord(raw) ? raw : {};
  return {
    ok: pickBool(unwrapped, ['ok']),
    message: pickString(unwrapped, ['message']),
    queryId: pickString(unwrapped, ['query_id', 'queryId']),
    serverVersion: pickString(unwrapped, ['server_version', 'serverVersion']),
    raw,
  };
}

export async function testTrinoConnection(input?: {
  host?: string;
  catalog?: string;
  schema?: string;
  user?: string;
  source?: string;
  authMode?: TrinoAuthMode;
}): Promise<TrinoTestResult> {
  const host = typeof input?.host === 'string' ? input.host.trim() : '';
  const catalog = typeof input?.catalog === 'string' ? input.catalog.trim() : '';
  const schema = typeof input?.schema === 'string' ? input.schema.trim() : '';
  const source = typeof input?.source === 'string' ? input.source.trim() : '';

  const raw = await fetchJson('/api/trino/test', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      ...(host ? { host } : {}),
      ...(catalog ? { catalog } : {}),
      ...(schema ? { schema } : {}),
      ...(source ? { source } : {}),
    }),
  });
  return normalizeTrinoTestResult(raw);
}
