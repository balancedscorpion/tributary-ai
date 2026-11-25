import type { AppSettings, ConnectionCatalogId, DataSourcesConfig } from '@/types';
import { canonicalProviderWebSearch } from '@/lib/providerWebSearch';
import { supportsProviderHostedTools } from '@/lib/capability/providerConfig';

/**
 * Scoped connection settings.
 *
 * Every connection in the catalogue owns a well-defined slice of AppSettings.
 * Connection actions (connect, test, sign out, enable) must persist only that
 * slice so a draft edited on another Settings tab — or on another connection —
 * is never committed as a side effect. These helpers are pure functions over
 * settings so both the console and tests can rely on them.
 */

type DataSourceKey = 'bigquery' | 'databricks' | 'trino' | 'snowflake';
export type HostedToolConnectionKind = 'web-search' | 'python-sandbox';

const DATA_SOURCE_IDS: DataSourceKey[] = ['bigquery', 'databricks', 'trino', 'snowflake'];

function isDataSourceId(id: ConnectionCatalogId): id is DataSourceKey {
  return (DATA_SOURCE_IDS as ConnectionCatalogId[]).includes(id);
}

/**
 * Removing a hosted-tool connection must neutralize every stored provider
 * copy, including stale values left on a surface that no longer supports the
 * tool. Otherwise a later migration/surface change can resurrect the removed
 * connection from one of those mirrors.
 */
export function disableHostedToolConnectionAcrossProviders(
  settings: AppSettings,
  kind: HostedToolConnectionKind,
): AppSettings {
  const controlKey = kind === 'web-search' ? 'hosted_web_search' : 'hosted_code_execution';
  return {
    ...settings,
    providerConfigs: (settings.providerConfigs || []).map((config) => {
      const capabilityControls = {
        ...(config.capabilityControls || {}),
        [controlKey]: 'off',
      };
      if (kind === 'web-search') {
        return {
          ...config,
          capabilityControls,
          webSearch: supportsProviderHostedTools(config)
            ? canonicalProviderWebSearch(config.providerId, false)
            : null,
        };
      }
      return {
        ...config,
        capabilityControls,
        codeExecution: config.codeExecution ? { ...config.codeExecution, enabled: false } : null,
      };
    }),
  };
}

/** Top-level AppSettings keys owned by each connection (besides dataSources.<id>). */
const CONNECTION_SETTINGS_KEYS: Record<ConnectionCatalogId, (keyof AppSettings)[]> = {
  'thread-search': ['threadSearch'],
  'image-generation': ['imageGeneration'],
  'web-search': [],
  'python-sandbox': [],
  'local-computer': ['localComputer'],
  browser: ['browserControl'],
  github: ['github', 'githubOAuthClientId'],
  slack: ['slack', 'slackOAuthClientId'],
  atlassian: ['atlassian'],
  sharepoint: ['sharepoint'],
  arxiv: ['arxiv'],
  bigquery: ['googleProjectId', 'googleCredentialsPath'],
  databricks: ['databricksHost', 'databricksWarehouseId', 'databricksOAuthClientId'],
  trino: ['trinoHost', 'trinoCatalog', 'trinoSchema', 'trinoUser', 'trinoSource'],
  snowflake: [
    'snowflakeAccountIdentifier',
    'snowflakeWarehouse',
    'snowflakeDatabase',
    'snowflakeSchema',
    'snowflakeRole',
    'snowflakeUser',
  ],
};

/** The settings slice a connection owns, extracted as a partial patch. */
export function getConnectionPatch(settings: AppSettings, id: ConnectionCatalogId): Partial<AppSettings> {
  const patch: Partial<AppSettings> = {};
  for (const key of CONNECTION_SETTINGS_KEYS[id] || []) {
    (patch as Record<string, unknown>)[key] = (settings as unknown as Record<string, unknown>)[key];
  }
  if (isDataSourceId(id)) {
    patch.dataSources = { [id]: settings.dataSources?.[id] } as DataSourcesConfig;
  }
  if (id === 'web-search') {
    patch.providerConfigs = (settings.providerConfigs || []).map((config) => ({
      id: config.id,
      webSearch: config.webSearch ? { ...config.webSearch } : null,
      capabilityControls: {
        hosted_web_search: config.capabilityControls?.hosted_web_search,
      },
    })) as unknown as AppSettings['providerConfigs'];
  }
  if (id === 'python-sandbox') {
    patch.providerConfigs = (settings.providerConfigs || []).map((config) => ({
      id: config.id,
      codeExecution: config.codeExecution ? {
        ...config.codeExecution,
        ...(config.codeExecution.skills
          ? { skills: config.codeExecution.skills.map((skill) => ({ ...skill })) }
          : {}),
      } : null,
      capabilityControls: {
        hosted_code_execution: config.capabilityControls?.hosted_code_execution,
      },
    })) as unknown as AppSettings['providerConfigs'];
  }
  return patch;
}

/**
 * `base` with only `id`'s settings slice replaced from `from`. Everything the
 * connection does not own — other connections, provider drafts, the system
 * prompt — stays exactly as it is in `base`.
 */
export function mergeConnectionPatch(base: AppSettings, id: ConnectionCatalogId, from: AppSettings): AppSettings {
  const next: AppSettings = { ...base };
  for (const key of CONNECTION_SETTINGS_KEYS[id] || []) {
    (next as unknown as Record<string, unknown>)[key] = (from as unknown as Record<string, unknown>)[key];
  }
  if (isDataSourceId(id)) {
    next.dataSources = { ...base.dataSources, [id]: from.dataSources?.[id] };
  }
  if (id === 'web-search' || id === 'python-sandbox') {
    const sourceByID = new Map((from.providerConfigs || []).map((config) => [config.id, config]));
    next.providerConfigs = (base.providerConfigs || []).map((config) => {
      const source = sourceByID.get(config.id);
      if (!source) return config;
      if (id === 'web-search') {
        return {
          ...config,
          // The connection owns only the enabled state. Re-derive the actual
          // provider-native schema here so a stale draft or older renderer can
          // never merge legacy tool fields back into current settings.
          webSearch: supportsProviderHostedTools(config)
            ? canonicalProviderWebSearch(config.providerId, source.webSearch?.enabled === true)
            : null,
          capabilityControls: {
            ...(config.capabilityControls || {}),
            hosted_web_search: source.capabilityControls?.hosted_web_search,
          },
        };
      }
      return {
        ...config,
        codeExecution: source.codeExecution ? {
          ...source.codeExecution,
          ...(source.codeExecution.skills
            ? { skills: source.codeExecution.skills.map((skill) => ({ ...skill })) }
            : {}),
        } : null,
        capabilityControls: {
          ...(config.capabilityControls || {}),
          hosted_code_execution: source.capabilityControls?.hosted_code_execution,
        },
      };
    });
  }
  return next;
}

// --- Config fingerprints ------------------------------------------------------

function fpValue(value: unknown): string {
  if (typeof value === 'string') return value.trim();
  if (typeof value === 'boolean') return value ? '1' : '0';
  if (value === null || value === undefined) return '';
  return String(value);
}

/**
 * Setup-relevant fields per connection: the inputs whose change invalidates a
 * previous successful verification (hosts, projects, schemas, users, auth
 * modes, OAuth client IDs). Live health bookkeeping (lastCheckedAt/lastCheckOk/
 * messages) and pure tool-scope narrowing never participate.
 */
const FINGERPRINT_FIELDS: Record<ConnectionCatalogId, (s: AppSettings) => Array<[string, unknown]>> = {
  'thread-search': () => [],
  'image-generation': () => [],
  'web-search': () => [],
  'python-sandbox': () => [],
  'local-computer': (s) => [
    ['enabled', s.localComputer?.enabled],
    ['accessMode', s.localComputer?.accessMode],
    ['selectedFolders', (s.localComputer?.selectedFolders || []).join('|')],
  ],
  browser: (s) => [['profileDir', s.browserControl?.browserProfileDir]],
  arxiv: () => [],
  github: (s) => [['clientId', s.githubOAuthClientId]],
  slack: (s) => [['clientId', s.slackOAuthClientId]],
  atlassian: (s) => [
    ['jiraBaseUrl', s.atlassian?.jiraBaseUrl],
    ['confluenceBaseUrl', s.atlassian?.confluenceBaseUrl],
    ['browserProfileDir', s.atlassian?.browserProfileDir],
  ],
  sharepoint: (s) => [
    ['siteUrl', s.sharepoint?.siteUrl],
    // authMode defaults to 'browser' when unset; normalize so materializing the
    // default later does not read as a config change.
    ['authMode', s.sharepoint?.authMode === 'graph' ? 'graph' : 'browser'],
    ['browserProfileDir', s.sharepoint?.browserProfileDir],
  ],
  bigquery: (s) => [
    ['projectId', s.googleProjectId],
    ['credentialsPath', s.googleCredentialsPath],
  ],
  databricks: (s) => [
    ['host', s.databricksHost],
    ['warehouseId', s.databricksWarehouseId],
    ['clientId', s.databricksOAuthClientId],
  ],
  trino: (s) => [
    ['host', s.trinoHost],
    ['catalog', s.trinoCatalog],
    ['schema', s.trinoSchema],
    ['user', s.trinoUser],
    ['source', s.trinoSource],
  ],
  snowflake: (s) => [
    ['account', s.snowflakeAccountIdentifier],
    ['warehouse', s.snowflakeWarehouse],
    ['database', s.snowflakeDatabase],
    ['schema', s.snowflakeSchema],
    ['role', s.snowflakeRole],
    ['user', s.snowflakeUser],
  ],
};

/**
 * Deterministic fingerprint of a connection's setup-relevant configuration.
 * Stored as `lastVerifiedFingerprint` on successful verification; a mismatch
 * against the current settings means the config changed since it was last
 * proven to work, so the connection must not present itself as verified.
 */
export function configFingerprint(settings: AppSettings, id: ConnectionCatalogId): string {
  const fields = (FINGERPRINT_FIELDS[id] || (() => []))(settings);
  const body = fields.map(([key, value]) => `${key}=${encodeURIComponent(fpValue(value))}`).join('&');
  return `${id}/v1:${body}`;
}

/** Missing fingerprints are grandfathered: existing users stay verified until a config edit. */
export function fingerprintMatchesConfig(
  stored: string | null | undefined,
  settings: AppSettings,
  id: ConnectionCatalogId,
): boolean {
  if (typeof stored !== 'string' || !stored) return true;
  return stored === configFingerprint(settings, id);
}

// --- Dirty detection ----------------------------------------------------------

const HEALTH_KEYS = new Set(['lastCheckedAt', 'lastCheckOk', 'lastCheckMessage', 'lastVerifiedFingerprint']);

/**
 * Serialize a connection slice for comparison, dropping live health fields and
 * empty defaults. Skipping falsy leaves ('', false, [], null) keeps the getter
 * pattern of materializing default objects from reading as a user edit.
 */
function normalizeForCompare(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map((item) => normalizeForCompare(item));
  }
  if (value && typeof value === 'object') {
    const out: Record<string, unknown> = {};
    for (const key of Object.keys(value as Record<string, unknown>).sort()) {
      if (HEALTH_KEYS.has(key)) continue;
      const child = (value as Record<string, unknown>)[key];
      if (child === undefined || child === null || child === false) continue;
      if (typeof child === 'string' && child === '') continue;
      if (Array.isArray(child) && child.length === 0) continue;
      out[key] = normalizeForCompare(child);
    }
    return out;
  }
  return value;
}

/** Stable serialization of a connection's config slice (health fields excluded). */
export function connectionConfigSnapshot(settings: AppSettings, id: ConnectionCatalogId): string {
  return JSON.stringify(normalizeForCompare(getConnectionPatch(settings, id)));
}

/** Did the connection's config slice change between two settings states? */
export function connectionConfigDirty(a: AppSettings, b: AppSettings, id: ConnectionCatalogId): boolean {
  return connectionConfigSnapshot(a, id) !== connectionConfigSnapshot(b, id);
}
