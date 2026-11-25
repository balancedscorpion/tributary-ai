import type { AppSettings, ConnectionCatalogId } from '@/types';
import { supportsProviderHostedTools } from '@/lib/capability/providerConfig';

/**
 * Credential freshness policy for the connection catalogue.
 *
 * Connections authenticate against external services whose tokens routinely
 * expire (OAuth refresh rotation, daily ADC logins, session cookies). Every
 * `lastCheckedAt/lastCheckOk/lastCheckMessage` live in the shared settings
 * store, so freshness is a pure function of settings - no extra bookkeeping
 * store.
 *
 * Policy: a connection's credentials are trusted for 24 hours after the last
 * successful strict check. At app launch (and once an hour while the app stays
 * open) anything past that window is strictly re-verified against the real
 * provider. Verification never runs on a shorter cadence - the point is to
 * catch the daily token reset without background polling.
 */
export const CONNECTION_CHECK_MAX_AGE_MS = 24 * 60 * 60 * 1000;

/**
 * How often the app re-evaluates staleness while it stays open. The interval
 * itself is pure local math over settings - network checks only fire for
 * connections that crossed the 24h window since the last evaluation.
 */
export const CONNECTION_HEALTH_REEVAL_INTERVAL_MS = 60 * 60 * 1000;

/**
 * Grace delay after launch before the first sweep, so credential checks never
 * compete with initial thread/settings loading.
 */
export const CONNECTION_HEALTH_LAUNCH_DELAY_MS = 2500;

/** Connections that hold external credentials and support a strict backend check. */
export const CREDENTIAL_CONNECTION_IDS: ConnectionCatalogId[] = [
  'github',
  'slack',
  'atlassian',
  'sharepoint',
  'bigquery',
  'databricks',
  'trino',
  'snowflake',
];

export interface ConnectionHealthState {
  id: ConnectionCatalogId;
  lastCheckedAt: number | null;
  lastCheckOk: boolean | null;
  lastCheckMessage: string | null;
}

function numOrNull(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : null;
}

function boolOrNull(value: unknown): boolean | null {
  return typeof value === 'boolean' ? value : null;
}

function strOrNull(value: unknown): string | null {
  const text = typeof value === 'string' ? value.trim() : '';
  return text ? text : null;
}

function hasText(value: unknown): boolean {
  return typeof value === 'string' && value.trim().length > 0;
}

/**
 * A connection participates in credential sweeps only once the user has
 * actually set it up: it is in the visible catalogue AND is either enabled for
 * model access or has been verified at least once. Unconfigured rows stay in
 * their `setup`/`connect` stage and are never probed.
 */
function deriveHealthState(id: ConnectionCatalogId, settings: AppSettings): ConnectionHealthState | null {
  switch (id) {
    case 'github': {
      const gh = settings.github;
      if (!(gh?.enabled === true || numOrNull(gh?.lastCheckedAt))) return null;
      return {
        id,
        lastCheckedAt: numOrNull(gh?.lastCheckedAt),
        lastCheckOk: boolOrNull(gh?.lastCheckOk),
        lastCheckMessage: strOrNull(gh?.lastCheckMessage),
      };
    }
    case 'slack': {
      const sl = settings.slack;
      if (!(sl?.enabled === true || numOrNull(sl?.lastCheckedAt))) return null;
      return {
        id,
        lastCheckedAt: numOrNull(sl?.lastCheckedAt),
        lastCheckOk: boolOrNull(sl?.lastCheckOk),
        lastCheckMessage: strOrNull(sl?.lastCheckMessage),
      };
    }
    case 'atlassian': {
      const at = settings.atlassian;
      if (!hasText(at?.jiraBaseUrl) && !hasText(at?.confluenceBaseUrl)) return null;
      const jira = at?.jira;
      const confluence = at?.confluence;
      const checkedAt = Math.max(numOrNull(jira?.lastCheckedAt) || 0, numOrNull(confluence?.lastCheckedAt) || 0) || null;
      const inUse = jira?.enabled === true || confluence?.enabled === true || checkedAt !== null;
      if (!inUse) return null;
      const oks = [boolOrNull(jira?.lastCheckOk), boolOrNull(confluence?.lastCheckOk)].filter((v): v is boolean => v !== null);
      return {
        id,
        lastCheckedAt: checkedAt,
        lastCheckOk: oks.length === 0 ? null : oks.every(Boolean),
        lastCheckMessage: strOrNull(jira?.lastCheckMessage) || strOrNull(confluence?.lastCheckMessage),
      };
    }
    case 'sharepoint': {
      const sp = settings.sharepoint;
      if (!hasText(sp?.siteUrl)) return null;
      if (!(sp?.enabled === true || numOrNull(sp?.lastCheckedAt))) return null;
      return {
        id,
        lastCheckedAt: numOrNull(sp?.lastCheckedAt),
        lastCheckOk: boolOrNull(sp?.lastCheckOk),
        lastCheckMessage: strOrNull(sp?.lastCheckMessage),
      };
    }
    case 'bigquery':
    case 'databricks':
    case 'trino':
    case 'snowflake': {
      const ds = settings.dataSources?.[id];
      const configured = id === 'bigquery'
        ? hasText(settings.googleProjectId) || hasText(ds?.lastProjectId)
        : id === 'databricks'
          ? (hasText(settings.databricksHost) || hasText(ds?.lastHost)) && (hasText(settings.databricksWarehouseId) || hasText(ds?.lastWarehouseId))
          : id === 'trino'
            ? hasText(settings.trinoHost) || hasText(ds?.lastHost)
            : hasText(settings.snowflakeAccountIdentifier) || hasText(ds?.lastHost);
      if (!configured) return null;
      if (!(ds?.enabled === true || numOrNull(ds?.lastCheckedAt))) return null;
      return {
        id,
        lastCheckedAt: numOrNull(ds?.lastCheckedAt),
        lastCheckOk: boolOrNull(ds?.lastCheckOk),
        lastCheckMessage: strOrNull(ds?.lastCheckMessage),
      };
    }
    default:
      return null;
  }
}

/**
 * All configured credential-bearing connections in the user's catalogue.
 * Local, credential-free connections (thread-search, browser, arxiv) are never
 * included - they have nothing to expire.
 */
export function getCredentialConnections(settings: AppSettings | null | undefined): ConnectionHealthState[] {
  if (!settings) return [];
  const visible = Array.isArray(settings.connections?.visibleIds) ? settings.connections!.visibleIds! : [];
  const out: ConnectionHealthState[] = [];
  for (const id of CREDENTIAL_CONNECTION_IDS) {
    if (!visible.includes(id)) continue;
    const state = deriveHealthState(id, settings);
    if (state) out.push(state);
  }
  return out;
}

/**
 * Is the connection currently granted to the model (its enable toggle on)?
 * Launch/cadence sweeps still record health for disabled connections, but they
 * must not toast about them: a credential the model cannot use anyway is not
 * actionable noise-worthy news. Tool-failure paths are unaffected — a tool
 * only runs through an enabled connection.
 */
export function isConnectionEnabledForModel(
  settings: AppSettings | null | undefined,
  id: ConnectionCatalogId,
): boolean {
  if (!settings) return false;
  switch (id) {
    case 'github':
      return settings.github?.enabled === true;
    case 'slack':
      return settings.slack?.enabled === true;
    case 'atlassian':
      return settings.atlassian?.jira?.enabled === true || settings.atlassian?.confluence?.enabled === true;
    case 'sharepoint':
      return settings.sharepoint?.enabled === true;
    case 'bigquery':
    case 'databricks':
    case 'trino':
    case 'snowflake':
      return settings.dataSources?.[id]?.enabled === true;
    case 'thread-search':
      return settings.threadSearch?.enabled === true;
    case 'image-generation':
      return settings.imageGeneration?.enabled === true;
    case 'web-search':
      return (settings.providerConfigs || []).some((config) => {
        if (!supportsProviderHostedTools(config)) return false;
        const control = config.capabilityControls?.hosted_web_search;
        return control != null ? control === 'on' : config.webSearch?.enabled === true;
      });
    case 'python-sandbox':
      return (settings.providerConfigs || []).some((config) => {
        if (!supportsProviderHostedTools(config)) return false;
        const control = config.capabilityControls?.hosted_code_execution;
        return control != null ? control === 'on' : config.codeExecution?.enabled === true;
      });
    case 'browser':
      return settings.browserControl?.enabled === true;
    case 'arxiv':
      return settings.arxiv?.enabled === true;
    default:
      return false;
  }
}

/** Never verified, verified too long ago, or verified "in the future" (clock reset). */
export function isCheckDue(
  state: Pick<ConnectionHealthState, 'lastCheckedAt'>,
  now: number,
  maxAgeMs: number = CONNECTION_CHECK_MAX_AGE_MS,
): boolean {
  const last = state.lastCheckedAt;
  if (!last) return true;
  if (last > now + 60_000) return true;
  return now - last >= maxAgeMs;
}

/** The connections a launch/cadence sweep should strictly re-verify right now. */
export function getDueConnections(
  settings: AppSettings | null | undefined,
  now: number,
  maxAgeMs: number = CONNECTION_CHECK_MAX_AGE_MS,
): ConnectionHealthState[] {
  return getCredentialConnections(settings).filter((state) => isCheckDue(state, now, maxAgeMs));
}
