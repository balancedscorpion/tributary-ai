export type NormalizedSecretsStatus = {
  initialized: boolean | null;
  locked: boolean | null;
  /**
   * Best-effort map of secret identifiers -> present.
   * Backend implementations vary; callers should tolerate missing keys.
   */
  present: Record<string, boolean>;
  raw: any;
};

export class SecretsApiError extends Error {
  status: number;
  code: string | null;
  payload: any;

  constructor(message: string, status: number, code: string | null, payload: any) {
    super(message);
    this.name = 'SecretsApiError';
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

function trimString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function pickBool(raw: any, keys: string[]): boolean | null {
  if (!isRecord(raw)) return null;
  for (const k of keys) {
    const v = (raw as any)[k];
    const b = coerceBool(v);
    if (b !== null) return b;
  }
  return null;
}

function extractPresentMap(raw: any): Record<string, boolean> {
  if (!isRecord(raw)) return {};

  // Our Go backend shape: { initialized, locked, configured: { openai, anthropic, gemini, bedrock, compatible: { [id]: { apiKey } } } }
  const configured = (raw as any).configured;
  if (isRecord(configured)) {
    const out: Record<string, boolean> = {};
    if (typeof (configured as any).openai === 'boolean') {
      out.openai_api_key = Boolean((configured as any).openai);
      out['provider:openai'] = Boolean((configured as any).openai);
    }
    if (typeof (configured as any).anthropic === 'boolean') {
      out.anthropic_api_key = Boolean((configured as any).anthropic);
      out['provider:anthropic'] = Boolean((configured as any).anthropic);
    }
    if (typeof (configured as any).gemini === 'boolean') {
      out.gemini_api_key = Boolean((configured as any).gemini);
      out['provider:gemini'] = Boolean((configured as any).gemini);
    }
    if (typeof (configured as any).bedrock === 'boolean') {
      out.bedrock_credentials = Boolean((configured as any).bedrock);
      out['provider:bedrock'] = Boolean((configured as any).bedrock);
    }
    if (typeof (configured as any).bedrockApiKey === 'boolean') {
      out.bedrock_api_key = Boolean((configured as any).bedrockApiKey);
    }
    if (typeof (configured as any).bedrockAwsCredentials === 'boolean') {
      out.bedrock_aws_credentials = Boolean((configured as any).bedrockAwsCredentials);
    }
    if (!Object.prototype.hasOwnProperty.call(out, 'provider:bedrock')) {
      const api = out.bedrock_api_key === true;
      const aws = out.bedrock_aws_credentials === true;
      if (api || aws) {
        out.bedrock_credentials = true;
        out['provider:bedrock'] = true;
      }
    }
    if (typeof (configured as any).databricks === 'boolean') {
      out.databricks_credentials = Boolean((configured as any).databricks);
      out['provider:databricks'] = Boolean((configured as any).databricks);
    }
    if (typeof (configured as any).databricksRefreshToken === 'boolean') {
      out.databricks_refresh_token = Boolean((configured as any).databricksRefreshToken);
    }
    if (typeof (configured as any).databricksPat === 'boolean') {
      out.databricks_pat = Boolean((configured as any).databricksPat);
    }
    if (typeof (configured as any).trino === 'boolean') {
      out.trino_credentials = Boolean((configured as any).trino);
      out['provider:trino'] = Boolean((configured as any).trino);
    }
    const trinoBasic =
      typeof (configured as any).trinoBasicPassword === 'boolean'
        ? Boolean((configured as any).trinoBasicPassword)
        : typeof (configured as any).trinoBasicAuth === 'boolean'
          ? Boolean((configured as any).trinoBasicAuth)
          : typeof (configured as any).trinoBasic === 'boolean'
            ? Boolean((configured as any).trinoBasic)
            : null;
    if (trinoBasic !== null) {
      out.trino_basic_password = trinoBasic;
      out['provider:trino:basic'] = trinoBasic;
    }
    if (typeof (configured as any).trinoJwt === 'boolean') {
      out.trino_jwt = Boolean((configured as any).trinoJwt);
      out['provider:trino:jwt'] = Boolean((configured as any).trinoJwt);
    }
    if (!Object.prototype.hasOwnProperty.call(out, 'provider:trino')) {
      if (out.trino_basic_password === true || out.trino_jwt === true) {
        out.trino_credentials = true;
        out['provider:trino'] = true;
      }
    }
    if (typeof (configured as any).snowflake === 'boolean') {
      out.snowflake_credentials = Boolean((configured as any).snowflake);
      out['provider:snowflake'] = Boolean((configured as any).snowflake);
    }
    if (typeof (configured as any).snowflakeOAuthToken === 'boolean') {
      out.snowflake_oauth_token = Boolean((configured as any).snowflakeOAuthToken);
      out['provider:snowflake:oauth_token'] = Boolean((configured as any).snowflakeOAuthToken);
    }
    if (typeof (configured as any).snowflakeKeyPair === 'boolean') {
      out.snowflake_keypair = Boolean((configured as any).snowflakeKeyPair);
      out['provider:snowflake:keypair_jwt'] = Boolean((configured as any).snowflakeKeyPair);
    }
    if (!Object.prototype.hasOwnProperty.call(out, 'provider:snowflake')) {
      if (out.snowflake_oauth_token === true || out.snowflake_keypair === true) {
        out.snowflake_credentials = true;
        out['provider:snowflake'] = true;
      }
    }
    if (typeof (configured as any).github === 'boolean') {
      out.github_oauth_token = Boolean((configured as any).github);
      out.github = Boolean((configured as any).github);
    }
    if (typeof (configured as any).slack === 'boolean') {
      out.slack_oauth_token = Boolean((configured as any).slack);
      out.slack = Boolean((configured as any).slack);
    }
    if (typeof (configured as any).slackClientSecret === 'boolean') {
      out.slack_client_secret = Boolean((configured as any).slackClientSecret);
    }
    const compat = (configured as any).compatible;
    if (isRecord(compat)) {
      for (const [pid, v] of Object.entries(compat)) {
        if (!isRecord(v)) continue;
        const hasKey = coerceBool((v as any).apiKey);
        if (hasKey !== null) {
          out[`compatible:${pid}:api_key`] = hasKey;
          out[`provider:compatible:${pid}`] = hasKey;
        }
      }
    }
    if (Object.keys(out).length > 0) return out;
  }

  const candidates: unknown[] = [
    (raw as any).present,
    (raw as any).secrets,
    (raw as any).keys_present,
    (raw as any).keyPresent,
  ];

  for (const c of candidates) {
    if (!isRecord(c)) continue;
    const out: Record<string, boolean> = {};
    for (const [k, v] of Object.entries(c)) {
      const b = coerceBool(v);
      if (b === null) continue;
      out[k] = b;
    }
    if (Object.keys(out).length > 0) return out;
  }

  const keysArr =
    Array.isArray((raw as any).keys) ? (raw as any).keys :
    Array.isArray((raw as any).presentKeys) ? (raw as any).presentKeys :
    null;
  if (Array.isArray(keysArr)) {
    const out: Record<string, boolean> = {};
    for (const k of keysArr) {
      if (typeof k === 'string' && k.trim()) out[k.trim()] = true;
    }
    if (Object.keys(out).length > 0) return out;
  }

  // Provider-shaped payloads (best effort).
  const providers = (raw as any).providers;
  if (isRecord(providers)) {
    const out: Record<string, boolean> = {};
    for (const [providerId, p] of Object.entries(providers)) {
      if (!isRecord(p)) continue;
      const hasKey = pickBool(p, ['hasKey', 'keyPresent', 'present', 'configured']);
      if (hasKey === null) continue;
      out[`provider:${providerId}`] = hasKey;
    }
    if (Object.keys(out).length > 0) return out;
  }

  return {};
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
      (data && typeof data === 'object'
        && (data as any).status
        && typeof (data as any).status === 'object'
        && typeof (data as any).status.error_code === 'string'
        && (data as any).status.error_code) ||
      null;
    const msg =
      (data && typeof data === 'object' && typeof (data as any).message === 'string' && (data as any).message) ||
      (data && typeof data === 'object' && typeof (data as any).error === 'string' && (data as any).error) ||
      `HTTP ${res.status}`;
    throw new SecretsApiError(msg, res.status, code, data);
  }
  return data;
}

export function normalizeSecretsStatus(raw: any): NormalizedSecretsStatus {
  const initialized = pickBool(raw, ['initialized', 'isInitialized', 'secretsInitialized', 'ready']);
  const locked = pickBool(raw, ['locked', 'isLocked', 'sealed']);
  const present = extractPresentMap(raw);
  return { initialized, locked, present, raw };
}

export async function getSecretsStatus(): Promise<NormalizedSecretsStatus> {
  const raw = await fetchJson('/api/secrets/status', { method: 'GET' });
  const unwrapped =
    raw && typeof raw === 'object'
      ? ((raw as any).status ?? (raw as any).data ?? raw)
      : raw;
  return normalizeSecretsStatus(unwrapped);
}

export async function initSecrets(password: string): Promise<void> {
  await fetchJson('/api/secrets/init', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ passphrase: password }),
  });
}

export async function unlockSecrets(password: string): Promise<void> {
  await fetchJson('/api/secrets/unlock', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ passphrase: password }),
  });
}

export function buildSnowflakeOAuthTokenPayload(token: string): Record<string, string> {
  return {
    providerId: 'snowflake',
    authType: 'oauth_token',
    oauthAccessToken: trimString(token),
  };
}

export function buildSnowflakeKeyPairPayload(privateKeyPem: string, passphrase?: string): Record<string, string> {
  const payload: Record<string, string> = {
    providerId: 'snowflake',
    authType: 'keypair_jwt',
    privateKeyPem: trimString(privateKeyPem),
  };
  const normalizedPassphrase = trimString(passphrase);
  if (normalizedPassphrase) {
    payload.privateKeyPassphrase = normalizedPassphrase;
  }
  return payload;
}

export function buildSnowflakeClearPayload(): Record<string, string> {
  return {
    providerId: 'snowflake',
    authType: 'all',
  };
}

export async function setSnowflakeOAuthToken(token: string): Promise<void> {
  const payload = buildSnowflakeOAuthTokenPayload(token);
  if (!payload.oauthAccessToken) {
    throw new Error('OAuth token is required');
  }
  await fetchJson('/api/secrets/set', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
}

export async function setSnowflakeKeyPair(privateKeyPem: string, passphrase?: string): Promise<void> {
  const payload = buildSnowflakeKeyPairPayload(privateKeyPem, passphrase);
  if (!payload.privateKeyPem) {
    throw new Error('Private key PEM is required');
  }
  await fetchJson('/api/secrets/set', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
}

export async function clearSnowflakeSecrets(): Promise<void> {
  await fetchJson('/api/secrets/clear', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(buildSnowflakeClearPayload()),
  });
}
