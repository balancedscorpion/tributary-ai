export type SnowflakeAuthMode = 'oauth_token' | 'keypair_jwt' | 'none';

export type SnowflakeStatus = {
  authMode: SnowflakeAuthMode;
  accountConfigured: boolean;
  warehouseConfigured: boolean;
  databaseConfigured: boolean;
  schemaConfigured: boolean;
  roleConfigured: boolean;
  userConfigured: boolean;
  accountIdentifier: string | null;
  warehouse: string | null;
  database: string | null;
  schema: string | null;
  role: string | null;
  user: string | null;
  raw: any;
};

export type SnowflakeTestResult = {
  ok: boolean;
  message: string | null;
  queryId: string | null;
  statementHandle: string | null;
  raw: any;
};

export type SnowflakePreflightResult = {
  ok: boolean;
  readOnly: boolean;
  message: string | null;
  warning: string | null;
  raw: any;
};

export type SnowflakeQueryResult = {
  ok: boolean;
  status: string | null;
  jobId: string | null;
  queryId: string | null;
  rows: Array<Record<string, unknown>> | null;
  queryMetadata: Record<string, unknown> | null;
  message: string | null;
  raw: any;
};

export type SnowflakeMetadataListResult = {
  items: string[];
  raw: any;
};

export type SnowflakeTableColumn = {
  name: string;
  type?: string | null;
  mode?: string | null;
  nullable?: boolean | null;
  defaultValue?: string | null;
  comment?: string | null;
  raw?: any;
};

export type SnowflakeTableSchemaResult = {
  columns: SnowflakeTableColumn[];
  raw: any;
};

export class SnowflakeApiError extends Error {
  status: number;
  payload: any;

  constructor(message: string, status: number, payload: any) {
    super(message);
    this.name = 'SnowflakeApiError';
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

function pickBool(raw: any, keys: string[], fallback = false): boolean {
  if (!isRecord(raw)) return fallback;
  for (const key of keys) {
    const value = (raw as any)[key];
    if (typeof value === 'boolean') return value;
    if (typeof value === 'number' && Number.isFinite(value)) return value !== 0;
    if (typeof value === 'string') {
      const normalized = value.trim().toLowerCase();
      if (normalized === 'true' || normalized === '1' || normalized === 'yes' || normalized === 'y') return true;
      if (normalized === 'false' || normalized === '0' || normalized === 'no' || normalized === 'n') return false;
    }
  }
  return fallback;
}

function normalizeAuthMode(raw: any): SnowflakeAuthMode {
  const value = pickString(raw, ['auth_mode', 'authMode', 'mode']);
  if (value === 'oauth_token' || value === 'keypair_jwt' || value === 'none') return value;
  return 'none';
}

function trimString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function toQueryString(params: Record<string, string>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    const trimmed = value.trim();
    if (!trimmed) continue;
    search.set(key, trimmed);
  }
  const encoded = search.toString();
  return encoded ? `?${encoded}` : '';
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
  if (isRecord(data)) {
    const message =
      (typeof (data as any).message === 'string' && (data as any).message)
      || (typeof (data as any).error === 'string' && (data as any).error)
      || '';
    if (message.trim()) return message.trim();
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
    throw new SnowflakeApiError(extractErrorMessage(data, res.status), res.status, data);
  }
  return data;
}

function normalizeListItems(raw: any): string[] {
  const candidates: unknown[] = [];
  if (Array.isArray(raw)) candidates.push(raw);
  if (isRecord(raw)) {
    candidates.push(
      (raw as any).items,
      (raw as any).databases,
      (raw as any).schemas,
      (raw as any).tables,
      (raw as any).rows,
      (raw as any).data,
    );
  }

  for (const candidate of candidates) {
    if (!Array.isArray(candidate)) continue;
    const out: string[] = [];
    for (const value of candidate) {
      if (typeof value === 'string' && value.trim()) {
        out.push(value.trim());
        continue;
      }
      if (!isRecord(value)) continue;
      const name = pickString(value, [
        'name',
        'database',
        'database_name',
        'schema',
        'schema_name',
        'table',
        'table_name',
        'id',
      ]);
      if (name) out.push(name);
    }
    if (out.length > 0) return out;
  }

  return [];
}

function normalizeSchemaColumns(raw: any): SnowflakeTableColumn[] {
  const source =
    Array.isArray(raw) ? raw :
    isRecord(raw) && Array.isArray((raw as any).schema) ? (raw as any).schema :
    isRecord(raw) && Array.isArray((raw as any).columns) ? (raw as any).columns :
    isRecord(raw) && Array.isArray((raw as any).fields) ? (raw as any).fields :
    isRecord(raw) && Array.isArray((raw as any).table_schema) ? (raw as any).table_schema :
    null;
  if (!Array.isArray(source)) return [];

  const out: SnowflakeTableColumn[] = [];
  for (const col of source) {
    if (!isRecord(col)) continue;
    const name = pickString(col, ['name', 'column_name']);
    if (!name) continue;
    out.push({
      name,
      type: pickString(col, ['type', 'data_type']),
      mode: pickString(col, ['mode']),
      nullable: Object.prototype.hasOwnProperty.call(col, 'nullable')
        ? pickBool(col, ['nullable'], false)
        : null,
      defaultValue: pickString(col, ['default', 'default_value']),
      comment: pickString(col, ['comment', 'description']),
      raw: col,
    });
  }
  return out;
}

export function normalizeSnowflakeStatus(raw: any): SnowflakeStatus {
  const unwrapped = isRecord(raw) ? raw : {};
  return {
    authMode: normalizeAuthMode(unwrapped),
    accountConfigured: pickBool(unwrapped, ['account_configured', 'accountConfigured']),
    warehouseConfigured: pickBool(unwrapped, ['warehouse_configured', 'warehouseConfigured']),
    databaseConfigured: pickBool(unwrapped, ['database_configured', 'databaseConfigured']),
    schemaConfigured: pickBool(unwrapped, ['schema_configured', 'schemaConfigured']),
    roleConfigured: pickBool(unwrapped, ['role_configured', 'roleConfigured']),
    userConfigured: pickBool(unwrapped, ['user_configured', 'userConfigured']),
    accountIdentifier: pickString(unwrapped, ['account_identifier', 'accountIdentifier', 'account']),
    warehouse: pickString(unwrapped, ['warehouse']),
    database: pickString(unwrapped, ['database']),
    schema: pickString(unwrapped, ['schema']),
    role: pickString(unwrapped, ['role']),
    user: pickString(unwrapped, ['user', 'username']),
    raw,
  };
}

export function normalizeSnowflakeTestResult(raw: any): SnowflakeTestResult {
  const unwrapped = isRecord(raw) ? raw : {};
  return {
    ok: pickBool(unwrapped, ['ok', 'success']),
    message: pickString(unwrapped, ['message', 'error']),
    queryId: pickString(unwrapped, ['query_id', 'queryId', 'statement_id', 'statementId']),
    statementHandle: pickString(unwrapped, ['statement_handle', 'statementHandle', 'query_id', 'queryId']),
    raw,
  };
}

export function normalizeSnowflakePreflightResult(raw: any): SnowflakePreflightResult {
  const unwrapped = isRecord(raw) ? raw : {};
  return {
    ok: pickBool(unwrapped, ['ok', 'allowed', 'success']),
    readOnly: pickBool(unwrapped, ['read_only', 'readOnly', 'readonly']),
    message: pickString(unwrapped, ['message', 'error']),
    warning: pickString(unwrapped, ['warning', 'warn']),
    raw,
  };
}

export function normalizeSnowflakeQueryResult(raw: any): SnowflakeQueryResult {
  const unwrapped = isRecord(raw) ? raw : {};
  const rowsCandidate =
    Array.isArray((unwrapped as any).rows) ? (unwrapped as any).rows :
    Array.isArray((unwrapped as any).data) ? (unwrapped as any).data :
    Array.isArray((unwrapped as any).results) ? (unwrapped as any).results :
    null;
  const queryMetadata = isRecord((unwrapped as any).query_metadata)
    ? ((unwrapped as any).query_metadata as Record<string, unknown>)
    : isRecord((unwrapped as any).queryMetadata)
      ? ((unwrapped as any).queryMetadata as Record<string, unknown>)
      : null;
  return {
    ok: pickBool(unwrapped, ['ok', 'success'], true),
    status: pickString(unwrapped, ['status']),
    jobId: pickString(unwrapped, ['job_id', 'jobId']),
    queryId: pickString(unwrapped, ['query_id', 'queryId', 'statement_handle', 'statementHandle']),
    rows: rowsCandidate as Array<Record<string, unknown>> | null,
    queryMetadata,
    message: pickString(unwrapped, ['message', 'error']),
    raw,
  };
}

export function normalizeSnowflakeMetadataListResult(raw: any): SnowflakeMetadataListResult {
  return {
    items: normalizeListItems(raw),
    raw,
  };
}

export function normalizeSnowflakeTableSchemaResult(raw: any): SnowflakeTableSchemaResult {
  return {
    columns: normalizeSchemaColumns(raw),
    raw,
  };
}

export async function getSnowflakeStatus(): Promise<SnowflakeStatus> {
  const raw = await fetchJson('/api/snowflake/status', { method: 'GET' });
  return normalizeSnowflakeStatus(raw);
}

export async function testSnowflakeConnection(input?: {
  accountIdentifier?: string;
  warehouse?: string;
  database?: string;
  schema?: string;
  role?: string;
  user?: string;
  authMode?: SnowflakeAuthMode;
}): Promise<SnowflakeTestResult> {
  const body = {
    account_identifier: trimString(input?.accountIdentifier),
    warehouse: trimString(input?.warehouse),
    database: trimString(input?.database),
    schema: trimString(input?.schema),
    role: trimString(input?.role),
    user: trimString(input?.user),
    auth_mode: trimString(input?.authMode),
  };
  const raw = await fetchJson('/api/snowflake/test', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  return normalizeSnowflakeTestResult(raw);
}

export async function preflightSnowflakeQuery(input: {
  sql: string;
  accountIdentifier?: string;
  warehouse?: string;
  database?: string;
  schema?: string;
  role?: string;
  user?: string;
}): Promise<SnowflakePreflightResult> {
  const raw = await fetchJson('/api/snowflake/preflight', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      sql: trimString(input.sql),
      account_identifier: trimString(input.accountIdentifier),
      warehouse: trimString(input.warehouse),
      database: trimString(input.database),
      schema: trimString(input.schema),
      role: trimString(input.role),
      user: trimString(input.user),
    }),
  });
  return normalizeSnowflakePreflightResult(raw);
}

export async function runSnowflakeQuery(input: {
  sql: string;
  limit?: number;
  pageToken?: string;
  accountIdentifier?: string;
  warehouse?: string;
  database?: string;
  schema?: string;
  role?: string;
  user?: string;
}): Promise<SnowflakeQueryResult> {
  const raw = await fetchJson('/api/snowflake/query', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      sql: trimString(input.sql),
      ...(Number.isFinite(input.limit) ? { limit: input.limit } : {}),
      ...(trimString(input.pageToken) ? { page_token: trimString(input.pageToken) } : {}),
      account_identifier: trimString(input.accountIdentifier),
      warehouse: trimString(input.warehouse),
      database: trimString(input.database),
      schema: trimString(input.schema),
      role: trimString(input.role),
      user: trimString(input.user),
    }),
  });
  return normalizeSnowflakeQueryResult(raw);
}

export async function listSnowflakeDatabases(input?: {
  accountIdentifier?: string;
  role?: string;
  user?: string;
}): Promise<SnowflakeMetadataListResult> {
  const qs = toQueryString({
    account_identifier: trimString(input?.accountIdentifier),
    role: trimString(input?.role),
    user: trimString(input?.user),
  });
  const raw = await fetchJson(`/api/snowflake/databases${qs}`, { method: 'GET' });
  return normalizeSnowflakeMetadataListResult(raw);
}

export async function listSnowflakeSchemas(input?: {
  database?: string;
  accountIdentifier?: string;
}): Promise<SnowflakeMetadataListResult> {
  const qs = toQueryString({
    database: trimString(input?.database),
    account_identifier: trimString(input?.accountIdentifier),
  });
  const raw = await fetchJson(`/api/snowflake/schemas${qs}`, { method: 'GET' });
  return normalizeSnowflakeMetadataListResult(raw);
}

export async function listSnowflakeTables(input?: {
  database?: string;
  schema?: string;
  accountIdentifier?: string;
}): Promise<SnowflakeMetadataListResult> {
  const qs = toQueryString({
    database: trimString(input?.database),
    schema: trimString(input?.schema),
    account_identifier: trimString(input?.accountIdentifier),
  });
  const raw = await fetchJson(`/api/snowflake/tables${qs}`, { method: 'GET' });
  return normalizeSnowflakeMetadataListResult(raw);
}

export async function describeSnowflakeTable(input: {
  database: string;
  schema: string;
  table: string;
  accountIdentifier?: string;
}): Promise<SnowflakeTableSchemaResult> {
  const qs = toQueryString({
    database: trimString(input.database),
    schema: trimString(input.schema),
    table: trimString(input.table),
    account_identifier: trimString(input.accountIdentifier),
  });
  const raw = await fetchJson(`/api/snowflake/table-schema${qs}`, { method: 'GET' });
  return normalizeSnowflakeTableSchemaResult(raw);
}
