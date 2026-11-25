import type { AppSettings, ConnectionCatalogId } from '@/types';
import { testBigQueryConnection } from './bigqueryApi';
import { testDatabricksConnection } from './databricksApi';
import { testTrinoConnection } from './trinoApi';
import { testSnowflakeConnection } from './snowflakeApi';

/**
 * Strict credential verification against the existing per-connection backend
 * endpoints. Every endpoint used here validates the stored credentials against
 * the real provider (GitHub API, Slack auth.test, Atlassian/SharePoint bridge,
 * warehouse test queries). Callers fold the verdict back into settings so row
 * health and verified config fingerprints are durable.
 */
export interface ConnectionCheckOutcome {
  id: ConnectionCatalogId;
  ok: boolean;
  message: string;
  /**
   * True when the check itself could not run (our backend was unreachable).
   * The credentials were neither confirmed nor refuted - callers must not
   * mark the connection unhealthy or alert the user.
   */
  indeterminate?: boolean;
  /** Raw check payload for callers that need provider detail (e.g. Atlassian products). */
  detail?: any;
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

function messageFrom(data: any, fallback: string): string {
  if (data && typeof data === 'object') {
    for (const key of ['error', 'message', 'lastCheckMessage']) {
      const value = (data as any)[key] ?? (data as any).status?.[key];
      if (typeof value === 'string' && value.trim()) return value.trim();
    }
  }
  return fallback;
}

function isIndeterminateStatus(status: number): boolean {
  return status === 404 || status === 408 || status === 429 || status >= 500;
}

async function postCheck(url: string): Promise<{ ok: boolean; message: string; data: any; status: number; indeterminate: boolean }> {
  const res = await fetch(url, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: '{}',
  });
  const data = await readJsonSafe(res);
  const ok = res.ok && data?.ok !== false;
  const indeterminate = !ok && isIndeterminateStatus(res.status);
  return {
    ok,
    message: ok ? '' : messageFrom(data, `HTTP ${res.status}`),
    data,
    status: res.status,
    indeterminate,
  };
}

function productConnected(product: any): boolean {
  return String(product?.state || '').toLowerCase() === 'connected' || product?.connected === true;
}

function siteConnected(site: any): boolean {
  return site?.connected === true || String(site?.state || '').toLowerCase() === 'connected';
}

/**
 * Strictly verify one connection's stored credentials. Never throws: backend
 * unreachability is reported as `indeterminate` so sweeps can skip it instead
 * of flagging a healthy connection.
 */
export async function verifyConnectionCredentials(
  id: ConnectionCatalogId,
  settings?: AppSettings | null,
): Promise<ConnectionCheckOutcome> {
  try {
    switch (id) {
      case 'github': {
        const r = await postCheck('/api/integrations/github/check');
        if (r.indeterminate) return { id, ok: false, message: r.message, indeterminate: true, detail: r.data };
        return { id, ok: r.ok, message: r.message, detail: r.data };
      }
      case 'slack': {
        const r = await postCheck('/api/integrations/slack/check');
        if (r.indeterminate) return { id, ok: false, message: r.message, indeterminate: true, detail: r.data };
        return { id, ok: r.ok, message: r.message, detail: r.data };
      }
      case 'atlassian': {
        const r = await postCheck('/api/integrations/atlassian/check');
        if (r.indeterminate) return { id, ok: false, message: r.message, indeterminate: true, detail: r.data };
        const products = r.data?.products;
        const jira = products?.jira;
        const confluence = products?.confluence;
        const anyProduct = Boolean(jira || confluence);
        const requiredProducts = [
          settings?.atlassian?.jira?.enabled === true ? jira : null,
          settings?.atlassian?.confluence?.enabled === true ? confluence : null,
        ].filter(Boolean);
        const ok = r.ok && (
          requiredProducts.length > 0
            ? requiredProducts.every(productConnected)
            : (!anyProduct || [jira, confluence].filter(Boolean).some(productConnected))
        );
        return { id, ok, message: ok ? '' : (r.message || 'Atlassian session is no longer valid.'), detail: r.data };
      }
      case 'sharepoint': {
        const r = await postCheck('/api/integrations/sharepoint/check');
        if (r.indeterminate) return { id, ok: false, message: r.message, indeterminate: true, detail: r.data };
        const ok = r.ok && (!r.data?.site || siteConnected(r.data.site));
        return { id, ok, message: ok ? '' : (r.message || 'SharePoint session is no longer valid.'), detail: r.data };
      }
      case 'bigquery': {
        const projectId = (settings?.googleProjectId || settings?.dataSources?.bigquery?.lastProjectId || '').trim();
        const result = await testBigQueryConnection(projectId || undefined);
        return { id, ok: result.ok === true, message: result.ok ? '' : messageFrom(result.raw, 'BigQuery connection failed.'), detail: result };
      }
      case 'databricks': {
        const result = await testDatabricksConnection();
        return { id, ok: result.ok === true, message: result.ok ? '' : (result.message || 'Databricks connection failed.'), detail: result };
      }
      case 'trino': {
        const result = await testTrinoConnection();
        return { id, ok: result.ok === true, message: result.ok ? '' : (result.message || 'Trino connection failed.'), detail: result };
      }
      case 'snowflake': {
        const result = await testSnowflakeConnection();
        return { id, ok: result.ok === true, message: result.ok ? '' : (result.message || 'Snowflake connection failed.'), detail: result };
      }
      default:
        return { id, ok: true, message: '', indeterminate: true };
    }
  } catch (e: any) {
    // The data-source services throw typed errors carrying an HTTP status when
    // the backend ran the check and refused. 4xx configuration/auth failures are
    // real verdicts; missing routes, throttling, timeouts and 5xx backend errors
    // do not prove the saved credentials are bad.
    if (typeof e?.status === 'number') {
      if (isIndeterminateStatus(e.status)) {
        return { id, ok: false, message: e?.message || 'Connection check could not run.', indeterminate: true };
      }
      return { id, ok: false, message: e?.message || 'Connection check failed.' };
    }
    return { id, ok: false, message: e?.message || 'Connection check could not run.', indeterminate: true };
  }
}
