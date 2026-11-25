export type NormalizedAdcStatus = {
  detected: boolean | null;
  credentialsPath: string | null;
  credentialsPathConfigured: string | null;
  credentialsPathResolved: string | null;
  credentialsSource: string | null;
  credentialsPresent: boolean | null;
  credentialsReadable: boolean | null;
  credentialsType: string | null;
  customPathConfigured: boolean | null;
  detectedProject: string | null;
  detectedProjectSource: string | null;
  quotaProjectId: string | null;
  credentialsProjectId: string | null;
  error: string | null;
  raw: any;
};

export type BigQueryTestResult = {
  ok: boolean;
  projectId: string | null;
  datasetsVisible: number | null;
  raw: any;
};

export class BigQueryApiError extends Error {
  status: number;
  payload: any;

  constructor(message: string, status: number, payload: any) {
    super(message);
    this.name = 'BigQueryApiError';
    this.status = status;
    this.payload = payload;
  }
}

const RETRY_DELAYS_MS = [200, 500];

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

function pickBool(raw: any, keys: string[]): boolean | null {
  if (!isRecord(raw)) return null;
  for (const k of keys) {
    const b = coerceBool((raw as any)[k]);
    if (b !== null) return b;
  }
  return null;
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
    if (typeof v === 'number' && Number.isFinite(v)) return v;
    if (typeof v === 'string' && v.trim() && Number.isFinite(Number(v))) return Number(v);
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
      (isRecord((data as any).error) && typeof (data as any).error.message === 'string' && (data as any).error.message) ||
      '';
    if (msg.trim()) return msg.trim();
  }
  return `HTTP ${status}`;
}

function isRetryableError(err: unknown): boolean {
  if (err instanceof BigQueryApiError) {
    if (err.status === 408 || err.status === 429) return true;
    if (err.status >= 500 && err.status <= 599) return true;
    return false;
  }

  const msg = String((err as any)?.message || err || '').toLowerCase();
  if (!msg) return false;
  return (
    msg.includes('failed to fetch')
    || msg.includes('networkerror')
    || msg.includes('fetch failed')
    || msg.includes('connection')
  );
}

function normalizeUnknownError(err: unknown): BigQueryApiError {
  if (err instanceof BigQueryApiError) return err;
  const raw = String((err as any)?.message || err || '').trim();
  const msg = raw.toLowerCase() === 'failed to fetch'
    ? 'Failed to fetch from the local server. Check that the AI Analyst server is running and reachable.'
    : (raw || 'Unexpected BigQuery API error');
  return new BigQueryApiError(msg, 0, null);
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function fetchJsonOnce(url: string, init?: RequestInit): Promise<any> {
  const res = await fetch(url, {
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init?.headers || {}),
    },
  });
  const data = await readJsonSafe(res);
  if (!res.ok) {
    throw new BigQueryApiError(extractErrorMessage(data, res.status), res.status, data);
  }
  return data;
}

async function fetchJson(url: string, init?: RequestInit): Promise<any> {
  let lastErr: unknown = null;
  for (let attempt = 0; attempt <= RETRY_DELAYS_MS.length; attempt += 1) {
    try {
      return await fetchJsonOnce(url, init);
    } catch (err: unknown) {
      lastErr = err;
      if (!isRetryableError(err) || attempt >= RETRY_DELAYS_MS.length) {
        throw normalizeUnknownError(err);
      }
      await sleep(RETRY_DELAYS_MS[attempt]);
    }
  }
  throw normalizeUnknownError(lastErr);
}

export function normalizeAdcStatus(raw: any): NormalizedAdcStatus {
  const unwrapped = isRecord(raw) ? ((raw as any).adc ?? raw) : raw;
  const detected = pickBool(unwrapped, ['detected']);
  const credentialsPathResolved = pickString(unwrapped, [
    'credentials_path_resolved',
    'credentialsPathResolved',
    'credentials_path',
    'credentialsPath',
    'path',
  ]);
  const credentialsPathConfigured = pickString(unwrapped, [
    'credentials_path_configured',
    'credentialsPathConfigured',
    'configured_path',
    'configuredPath',
  ]);
  const credentialsPath = credentialsPathResolved;
  const credentialsSource = pickString(unwrapped, ['credentials_source', 'credentialsSource', 'source']);
  const credentialsType = pickString(unwrapped, ['credentials_type', 'credentialsType', 'type']);
  const credentialsPresent = pickBool(unwrapped, ['credentials_present', 'credentialsPresent', 'present']);
  const credentialsReadable = pickBool(unwrapped, ['credentials_readable', 'credentialsReadable', 'readable']);
  const customPathConfigured = pickBool(unwrapped, ['custom_path_configured', 'customPathConfigured']);
  const detectedProject = pickString(unwrapped, [
    'detected_project_id',
    'detectedProjectId',
    'detected_project',
    'detectedProject',
    'project_id',
    'projectId',
  ]);
  const detectedProjectSource = pickString(unwrapped, [
    'detected_project_source',
    'detectedProjectSource',
    'project_source',
    'projectSource',
  ]);
  const quotaProjectId = pickString(unwrapped, ['quota_project_id', 'quotaProjectId']);
  const credentialsProjectId = pickString(unwrapped, ['credentials_project_id', 'credentialsProjectId']);
  const error =
    (isRecord(unwrapped) && typeof (unwrapped as any).error === 'string' && (unwrapped as any).error.trim())
      ? (unwrapped as any).error.trim()
      : null;
  return {
    detected,
    credentialsPath,
    credentialsPathConfigured,
    credentialsPathResolved,
    credentialsSource,
    credentialsPresent,
    credentialsReadable,
    credentialsType,
    customPathConfigured,
    detectedProject,
    detectedProjectSource,
    quotaProjectId,
    credentialsProjectId,
    error,
    raw,
  };
}

export async function getBigQueryAdcStatus(): Promise<NormalizedAdcStatus> {
  const raw = await fetchJson('/api/bigquery/status', { method: 'GET' });
  return normalizeAdcStatus(raw);
}

export function normalizeBigQueryTestResult(raw: any): BigQueryTestResult {
  const ok = coerceBool(isRecord(raw) ? (raw as any).ok : null) === true;
  const projectId = pickString(raw, ['project_id', 'projectId']);
  const datasetsVisible = pickNumber(raw, ['datasets_visible', 'datasetsVisible']);
  return {
    ok,
    projectId,
    datasetsVisible,
    raw,
  };
}

export async function testBigQueryConnection(projectId?: string): Promise<BigQueryTestResult> {
  const raw = await fetchJson('/api/bigquery/test', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ project_id: typeof projectId === 'string' ? projectId : '' }),
  });
  return normalizeBigQueryTestResult(raw);
}

// --- ADC reauthentication (gcloud auth application-default login) -----------

export type AdcReauthState =
  | 'idle'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'cancelled'
  | 'gcloud_missing';

export type NormalizedAdcReauth = {
  state: AdcReauthState;
  running: boolean;
  startedAt: number | null;
  finishedAt: number | null;
  message: string | null;
  error: string | null;
  command: string[] | null;
  fallbackCommand: string | null;
  gcloudPath: string | null;
  raw: any;
};

export const ADC_REAUTH_FALLBACK_COMMAND = 'gcloud auth application-default login';

const ADC_REAUTH_STATES: ReadonlySet<string> = new Set([
  'idle',
  'running',
  'succeeded',
  'failed',
  'cancelled',
  'gcloud_missing',
]);

function pickStringArray(raw: any, keys: string[]): string[] | null {
  if (!isRecord(raw)) return null;
  for (const k of keys) {
    const v = (raw as any)[k];
    if (Array.isArray(v)) {
      const parts = v.filter((x): x is string => typeof x === 'string' && x.trim().length > 0).map((x) => x.trim());
      if (parts.length) return parts;
    }
  }
  return null;
}

export function normalizeAdcReauth(raw: any): NormalizedAdcReauth {
  const stateRaw = (pickString(raw, ['state']) || '').toLowerCase();
  const state = (ADC_REAUTH_STATES.has(stateRaw) ? stateRaw : 'idle') as AdcReauthState;
  const running = pickBool(raw, ['running']) === true || state === 'running';
  return {
    state,
    running,
    startedAt: pickNumber(raw, ['started_at', 'startedAt']),
    finishedAt: pickNumber(raw, ['finished_at', 'finishedAt']),
    message: pickString(raw, ['message']),
    error: pickString(raw, ['error']),
    command: pickStringArray(raw, ['command']),
    fallbackCommand: pickString(raw, ['fallback_command', 'fallbackCommand']),
    gcloudPath: pickString(raw, ['gcloud_path', 'gcloudPath']),
    raw,
  };
}

export async function startAdcReauth(): Promise<NormalizedAdcReauth> {
  const raw = await fetchJson('/api/bigquery/auth/application-default/start', { method: 'POST' });
  return normalizeAdcReauth(raw);
}

export async function getAdcReauthStatus(): Promise<NormalizedAdcReauth> {
  const raw = await fetchJson('/api/bigquery/auth/application-default/status', { method: 'GET' });
  return normalizeAdcReauth(raw);
}

export async function cancelAdcReauth(): Promise<NormalizedAdcReauth> {
  const raw = await fetchJson('/api/bigquery/auth/application-default/cancel', { method: 'POST' });
  return normalizeAdcReauth(raw);
}

// Detects the backend signal that the saved Google login has expired and the
// user must re-run `gcloud auth application-default login` before BigQuery works.
export function isAdcReauthRequiredMessage(message: unknown): boolean {
  if (typeof message !== 'string') return false;
  const m = message.toLowerCase();
  if (!m.trim()) return false;
  if (m.includes('application-default login')) return true;
  if (m.includes('adc token unavailable')) return true;
  if ((m.includes('expired') || m.includes('reauth')) && (m.includes('login') || m.includes('credential') || m.includes('token'))) {
    return true;
  }
  return false;
}
