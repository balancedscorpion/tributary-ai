import {
  DEFAULT_DATABASE_QUERY_POLICY,
  DEFAULT_SETTINGS,
  MAX_DATABASE_QUERY_ROW_LIMIT,
} from '../constants';
import {
  BEDROCK_COMPACTION_DEFAULT_INSTRUCTION,
  BEDROCK_COMPACTION_LEGACY_INSTRUCTION,
  BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS,
  OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS,
} from '../lib/bedrockCompaction';
import {
  deriveProviderApiSurface,
  migrateLegacyCapabilityControls,
  preserveCapabilityControls,
  supportsProviderHostedTools,
} from '../lib/capability/providerConfig';
import { normalizeScanBudget } from '../lib/scanBudget';
import { canonicalProviderWebSearch } from '../lib/providerWebSearch';
import type {
  AppSettings,
  CompatibleProfile,
  ConnectionCatalogId,
  ConnectionsSettings,
  GitHubSettings,
  Header,
  LocalComputerSettings,
  LocalComputerAccessMode,
  ProviderCodeExecutionConfig,
  ProviderCodeExecutionSkill,
  ProviderConfig,
  ProviderWebSearchConfig,
  SlackSettings,
  ArxivSettings,
  ImageGenerationSettings,
  SharePointSettings,
  ThreadSearchSettings,
} from '../types';

const isRecord = (value: unknown): value is Record<string, unknown> =>
  Boolean(value) && typeof value === 'object' && !Array.isArray(value);

const normalizeDatabaseQueryPolicy = (
  value: unknown,
  legacyRowLimit: number,
): NonNullable<AppSettings['databaseQueryPolicy']> => {
  const raw = isRecord(value) ? value : {};
  const positiveInt = (key: keyof typeof DEFAULT_DATABASE_QUERY_POLICY): number => {
    const parsed = Number(raw[key]);
    return Number.isFinite(parsed) && parsed > 0
      ? Math.trunc(parsed)
      : DEFAULT_DATABASE_QUERY_POLICY[key] as number;
  };

  const maxExecutingGlobal = positiveInt('maxExecutingGlobal');
  const maxExecutingPerThread = Math.min(maxExecutingGlobal, positiveInt('maxExecutingPerThread'));
  const maxExecutingPerConnection = Math.min(maxExecutingPerThread, positiveInt('maxExecutingPerConnection'));
  const maxPollIntervalMilliseconds = positiveInt('maxPollIntervalMilliseconds');
  const monitorErrorBackoffMilliseconds = Math.max(
    maxPollIntervalMilliseconds,
    positiveInt('monitorErrorBackoffMilliseconds'),
  );
  const softExecutionTimeoutMilliseconds = positiveInt('softExecutionTimeoutMilliseconds');
  const hardExecutionTimeoutMilliseconds = Math.max(
    softExecutionTimeoutMilliseconds,
    positiveInt('hardExecutionTimeoutMilliseconds'),
  );
  const configuredRows = isRecord(value)
    ? positiveInt('maxResultRows')
    : legacyRowLimit;

  return {
    maxExecutingGlobal,
    maxExecutingPerThread,
    maxExecutingPerConnection,
    inlineWaitMilliseconds: positiveInt('inlineWaitMilliseconds'),
    maxPollIntervalMilliseconds,
    monitorErrorBackoffMilliseconds,
    softQueueTimeoutMilliseconds: positiveInt('softQueueTimeoutMilliseconds'),
    softExecutionTimeoutMilliseconds,
    hardExecutionTimeoutMilliseconds,
    maxResultRows: Math.max(1, Math.min(MAX_DATABASE_QUERY_ROW_LIMIT, configuredRows)),
    maxMaterializedBytes: positiveInt('maxMaterializedBytes'),
    bigQueryMaximumBytesBilled: positiveInt('bigQueryMaximumBytesBilled'),
    maxAutoContinuations: positiveInt('maxAutoContinuations'),
  };
};

const inferCompatibleApiType = (url: string): 'chat_completions' | 'responses' => {
  const lower = url.toLowerCase();
  return lower.includes('/responses') ? 'responses' : 'chat_completions';
};

const normalizeCompatibleProfileBaseUrl = (url: string): string => {
  const trimmed = url.trim();
  if (!trimmed) return trimmed;

  // Drop query/fragment.
  const withoutQuery = trimmed.split('#')[0].split('?')[0];

  // If the user stored the full endpoint, back into base URL.
  const endpoints = ['/chat/completions', '/responses', '/v1/chat/completions', '/v1/responses'];
  for (const endpoint of endpoints) {
    const idx = withoutQuery.toLowerCase().indexOf(endpoint);
    if (idx !== -1) return withoutQuery.slice(0, idx);
  }

  return withoutQuery.replace(/\/+$/, '');
};

const normalizeProviderBaseUrl = (providerId: 'openai' | 'anthropic' | 'gemini', raw: string): string => {
  const trimmed = (raw || '').trim();
  if (!trimmed) return trimmed;

  // Migrate old Vite proxy defaults to upstream absolute URLs (Go server proxies requests).
  const mapping: Record<string, string> = {
    '/proxy/openai/v1': 'https://api.openai.com/v1',
    '/api/proxy/openai/v1': 'https://api.openai.com/v1',
    '/proxy/anthropic': 'https://api.anthropic.com',
    '/api/proxy/anthropic': 'https://api.anthropic.com',
    '/proxy/gemini': 'https://generativelanguage.googleapis.com',
    '/api/proxy/gemini': 'https://generativelanguage.googleapis.com',
  };

  const mapped = mapping[trimmed];
  if (mapped) return mapped;

  // Some users may have stored full endpoints; keep those, but normalize trailing slashes.
  try {
    const u = new URL(trimmed);
    u.pathname = u.pathname.replace(/\/+$/, '');
    return u.toString();
  } catch {
    // If it's still relative and unrecognized, fall back to the upstream default for safety.
    if (trimmed.startsWith('/')) {
      if (providerId === 'openai') return 'https://api.openai.com/v1';
      if (providerId === 'anthropic') return 'https://api.anthropic.com';
      return 'https://generativelanguage.googleapis.com';
    }
    return trimmed.replace(/\/+$/, '');
  }
};

const normalizeBedrockModelId = (raw: unknown): string => {
  const model = typeof raw === 'string' ? raw.trim() : '';
  // This was the Bedrock connection editor's old default, but it is a direct
  // Anthropic API model name rather than a Bedrock Runtime model ID.
  if (model === 'claude-opus-4-6') return 'anthropic.claude-opus-4-6-v1';
  return model;
};

const extractBearerKeyFromHeaders = (headers: Header[] | undefined): string | null => {
  if (!headers) return null;
  for (const h of headers) {
    if (!h.key) continue;
    if (h.key.toLowerCase() !== 'authorization') continue;
    const value = (h.value || '').trim();
    if (!value.toLowerCase().startsWith('bearer ')) continue;
    const key = value.slice('bearer '.length).trim();
    return key || null;
  }
  return null;
};

const headersArrayToExtraHeaders = (headers: Header[] | undefined): Header[] => {
  if (!headers) return [];
  return headers
    .filter((h) => Boolean(h.key?.trim()))
    .filter((h) => {
      const k = h.key.trim().toLowerCase();
      return k !== 'content-type' && k !== 'authorization' && k !== 'x-api-key' && k !== 'x-goog-api-key';
    })
    .map((h) => ({ id: h.id, key: h.key, value: h.value, isSecret: h.isSecret }));
};

const normalizeCompactionInstruction = (value: unknown): string => {
  const trimmed = typeof value === 'string' ? value.trim() : '';
  if (!trimmed) return BEDROCK_COMPACTION_DEFAULT_INSTRUCTION;
  if (trimmed === BEDROCK_COMPACTION_LEGACY_INSTRUCTION) {
    return BEDROCK_COMPACTION_DEFAULT_INSTRUCTION;
  }
  return trimmed;
};

const normalizeStringArray = (value: unknown): string[] | null => {
  if (!Array.isArray(value)) return null;
  const out = value
    .map((item) => (typeof item === 'string' ? item.trim() : ''))
    .filter(Boolean);
  return out.length > 0 ? out : null;
};

const normalizeCodeExecutionSkills = (value: unknown): ProviderCodeExecutionSkill[] | null => {
  if (!Array.isArray(value)) return null;
  const out = value
    .map((item) => {
      if (!isRecord(item)) return null;
      const type = typeof item.type === 'string' ? item.type.trim() : '';
      const skillId = typeof item.skillId === 'string' ? item.skillId.trim() : '';
      const version = typeof item.version === 'string' ? item.version.trim() : '';
      if (!type || !skillId) return null;
      return {
        type,
        skillId,
        ...(version ? { version } : {}),
      };
    })
    .filter(Boolean) as ProviderCodeExecutionSkill[];
  return out.length > 0 ? out : null;
};

const normalizeWebSearchConfig = (value: unknown): ProviderWebSearchConfig | null => {
  if (!isRecord(value)) return null;
  const enabled = typeof value.enabled === 'boolean' ? value.enabled : undefined;
  const toolType = typeof value.toolType === 'string' && value.toolType.trim() ? value.toolType.trim() : null;
  const maxUses = Number.isFinite(Number(value.maxUses)) ? Math.max(1, Math.round(Number(value.maxUses))) : null;
  const searchContextSize = typeof value.searchContextSize === 'string' && value.searchContextSize.trim()
    ? value.searchContextSize.trim()
    : null;
  const allowedDomains = normalizeStringArray(value.allowedDomains);
  const blockedDomains = normalizeStringArray(value.blockedDomains);
  const allowedCallers = normalizeStringArray(value.allowedCallers);
  if (
    enabled === undefined
    && !toolType
    && maxUses == null
    && !searchContextSize
    && !allowedDomains
    && !blockedDomains
    && !allowedCallers
  ) {
    return null;
  }
  return {
    ...(enabled !== undefined ? { enabled } : {}),
    ...(toolType ? { toolType } : {}),
    ...(maxUses != null ? { maxUses } : {}),
    ...(searchContextSize ? { searchContextSize } : {}),
    ...(allowedDomains ? { allowedDomains } : {}),
    ...(blockedDomains ? { blockedDomains } : {}),
    ...(allowedCallers ? { allowedCallers } : {}),
  };
};

const defaultWebSearchConfigForProvider = (
  providerId: ProviderConfig['providerId'] | 'openai' | 'anthropic' | 'bedrock' | string
): ProviderWebSearchConfig | null => {
  if (providerId === 'openai') {
    return canonicalProviderWebSearch(providerId, true);
  }
  if (providerId === 'anthropic') {
    return canonicalProviderWebSearch(providerId, true);
  }
  return null;
};

const resolveWebSearchConfig = (
  providerId: ProviderConfig['providerId'] | 'openai' | 'anthropic' | 'bedrock' | string,
  value: unknown
): ProviderWebSearchConfig | null => {
  const normalized = normalizeWebSearchConfig(value);
  if (normalized) return normalized;
  return defaultWebSearchConfigForProvider(providerId);
};

const normalizeCodeExecutionConfig = (value: unknown): ProviderCodeExecutionConfig | null => {
  if (!isRecord(value)) return null;
  const enabled = typeof value.enabled === 'boolean' ? value.enabled : undefined;
  const toolType = typeof value.toolType === 'string' && value.toolType.trim() ? value.toolType.trim() : null;
  const containerMode = value.containerMode === 'auto' || value.containerMode === 'existing'
    ? value.containerMode
    : null;
  const containerId = typeof value.containerId === 'string' && value.containerId.trim() ? value.containerId.trim() : null;
  const memoryLimit = typeof value.memoryLimit === 'string' && value.memoryLimit.trim() ? value.memoryLimit.trim() : null;
  const skills = normalizeCodeExecutionSkills(value.skills);
  if (
    enabled === undefined
    && !toolType
    && !containerMode
    && !containerId
    && !memoryLimit
    && !skills
  ) {
    return null;
  }
  return {
    ...(enabled !== undefined ? { enabled } : {}),
    ...(toolType ? { toolType } : {}),
    ...(containerMode ? { containerMode } : {}),
    ...(containerId ? { containerId } : {}),
    ...(memoryLimit ? { memoryLimit } : {}),
    ...(skills ? { skills } : {}),
  };
};



const defaultFeatureFlags = () => ({
  activation: true,
  providers: DEFAULT_SETTINGS.featureFlags?.providers || {
    openai: false,
    anthropic: false,
    gemini: false,
    bedrock: true,
    compatible: true,
  },
  providerTemplates: true,
  cloudCredentialsAws: DEFAULT_SETTINGS.featureFlags?.cloudCredentialsAws ?? false,
  cloudCredentialsTrino: DEFAULT_SETTINGS.featureFlags?.cloudCredentialsTrino ?? false,
  cloudCredentialsSnowflake: DEFAULT_SETTINGS.featureFlags?.cloudCredentialsSnowflake ?? false,
  chartDimensionFilters: DEFAULT_SETTINGS.featureFlags?.chartDimensionFilters ?? false,
  chartEngines: {
    echarts: DEFAULT_SETTINGS.featureFlags?.chartEngines?.echarts ?? true,
    highcharts: DEFAULT_SETTINGS.featureFlags?.chartEngines?.highcharts ?? false,
  },
  capabilityUx: {
    report: DEFAULT_SETTINGS.featureFlags?.capabilityUx?.report ?? false,
    editorV2: DEFAULT_SETTINGS.featureFlags?.capabilityUx?.editorV2 ?? false,
    controls: DEFAULT_SETTINGS.featureFlags?.capabilityUx?.controls ?? false,
    preflight: DEFAULT_SETTINGS.featureFlags?.capabilityUx?.preflight ?? false,
    composerTarget: DEFAULT_SETTINGS.featureFlags?.capabilityUx?.composerTarget ?? false,
  },
});

const ensureDataSourceState = (raw: any) => {
  if (!raw || typeof raw !== 'object') return undefined;

  const enabledRaw = (raw as any).enabled;
  const enabled = typeof enabledRaw === 'boolean' ? enabledRaw : false;

  const lastCheckedAt = typeof (raw as any).lastCheckedAt === 'number'
    ? (raw as any).lastCheckedAt
    : typeof (raw as any).lastCheckedAt === 'string' && Number.isFinite(Number((raw as any).lastCheckedAt))
      ? Number((raw as any).lastCheckedAt)
      : undefined;

  const lastCheckOkRaw = (raw as any).lastCheckOk;
  const lastCheckOk = typeof lastCheckOkRaw === 'boolean' ? lastCheckOkRaw : undefined;

  const lastCheckMessage = typeof (raw as any).lastCheckMessage === 'string'
    ? (raw as any).lastCheckMessage
    : undefined;

  const lastProjectId = typeof (raw as any).lastProjectId === 'string'
    ? (raw as any).lastProjectId
    : undefined;
  const lastWarehouseId = typeof (raw as any).lastWarehouseId === 'string'
    ? (raw as any).lastWarehouseId
    : undefined;
  const lastHost = typeof (raw as any).lastHost === 'string'
    ? (raw as any).lastHost
    : undefined;

  return {
    enabled,
    ...(lastCheckedAt !== undefined ? { lastCheckedAt } : {}),
    ...(lastCheckOk !== undefined ? { lastCheckOk } : {}),
    ...(lastCheckMessage !== undefined ? { lastCheckMessage } : {}),
    ...optionalLastVerifiedFingerprint(raw),
    ...(lastProjectId !== undefined ? { lastProjectId } : {}),
    ...(lastWarehouseId !== undefined ? { lastWarehouseId } : {}),
    ...(lastHost !== undefined ? { lastHost } : {}),
    ...normalizeScanBudget((raw as any).automaticScanBytes, (raw as any).approvedScanBytes),
  };
};

const optionalLastVerifiedFingerprint = (raw: any) => {
  const value = raw?.lastVerifiedFingerprint;
  return typeof value === 'string' || value === null
    ? { lastVerifiedFingerprint: value }
    : {};
};

const ensureDataSources = (raw: any) => ({
  bigquery: ensureDataSourceState((raw as any)?.bigquery),
  databricks: ensureDataSourceState((raw as any)?.databricks),
  trino: ensureDataSourceState((raw as any)?.trino),
  snowflake: ensureDataSourceState((raw as any)?.snowflake) || { enabled: false },
});

const ensureAtlassianProductState = (raw: any) => ({
  enabled: typeof raw?.enabled === 'boolean' ? raw.enabled : false,
  showBrowser: typeof raw?.showBrowser === 'boolean' ? raw.showBrowser : false,
  ...(typeof raw?.lastCheckedAt === 'number' ? { lastCheckedAt: raw.lastCheckedAt } : {}),
  ...(typeof raw?.lastCheckOk === 'boolean' ? { lastCheckOk: raw.lastCheckOk } : {}),
  ...(typeof raw?.lastCheckMessage === 'string' ? { lastCheckMessage: raw.lastCheckMessage } : {}),
});

const stringArray = (value: any): string[] => (
  Array.isArray(value)
    ? value.map((item) => String(item || '').trim()).filter(Boolean)
    : []
);

const THREAD_SEARCH_CONNECTION_ID: ConnectionCatalogId = 'thread-search';

const CONNECTION_CATALOG_IDS = new Set<ConnectionCatalogId>([
  'thread-search',
  'image-generation',
  'web-search',
  'python-sandbox',
  'local-computer',
  'browser',
  'github',
  'slack',
  'atlassian',
  'sharepoint',
  'bigquery',
  'databricks',
  'trino',
  'snowflake',
  'arxiv',
]);

const ensureConnectionsSettings = (raw: any): ConnectionsSettings => {
  const visibleIds = stringArray(raw?.visibleIds)
    .map((item) => item.toLowerCase())
    .filter((item, index, items) => CONNECTION_CATALOG_IDS.has(item as ConnectionCatalogId) && items.indexOf(item) === index) as ConnectionCatalogId[];
  return { visibleIds };
};

const ensureDefaultThreadSearchConnection = (
  connections: ConnectionsSettings,
  threadSearch: ThreadSearchSettings,
): ConnectionsSettings => {
  if (threadSearch.enabled !== true) return connections;
  const visibleIds = Array.isArray(connections.visibleIds) ? connections.visibleIds : [];
  if (visibleIds.includes(THREAD_SEARCH_CONNECTION_ID)) return connections;
  return { ...connections, visibleIds: [THREAD_SEARCH_CONNECTION_ID, ...visibleIds] };
};

const ensureVisibleConnectionIds = (
  connections: ConnectionsSettings,
  ids: ConnectionCatalogId[],
): ConnectionsSettings => {
  const visibleIds = Array.isArray(connections.visibleIds) ? connections.visibleIds : [];
  const next = [...visibleIds];
  for (const id of ids) {
    if (!next.includes(id)) next.push(id);
  }
  return next.length === visibleIds.length ? connections : { ...connections, visibleIds: next };
};

const ensureEnabledConnectionsVisible = (
  connections: ConnectionsSettings,
  settings: AppSettings,
): ConnectionsSettings => {
  const ids: ConnectionCatalogId[] = [];
  const hostedEnabled = (
    config: ProviderConfig,
    kind: 'webSearch' | 'codeExecution',
  ): boolean => {
    if (!supportsProviderHostedTools(config)) return false;
    const controlKey = kind === 'webSearch' ? 'hosted_web_search' : 'hosted_code_execution';
    const control = config.capabilityControls?.[controlKey];
    if (control != null) return control === 'on';
    return config[kind]?.enabled === true;
  };
  if (settings.threadSearch?.enabled === true) ids.push('thread-search');
  if (settings.imageGeneration?.enabled === true) ids.push('image-generation');
  if ((settings.providerConfigs || []).some((config) => hostedEnabled(config, 'webSearch'))) ids.push('web-search');
  if ((settings.providerConfigs || []).some((config) => hostedEnabled(config, 'codeExecution'))) ids.push('python-sandbox');
  if (settings.localComputer?.enabled === true) ids.push('local-computer');
  if (settings.browserControl?.enabled === true) ids.push('browser');
  if (settings.github?.enabled === true) ids.push('github');
  if (settings.slack?.enabled === true) ids.push('slack');
  if (settings.atlassian?.jira?.enabled === true || settings.atlassian?.confluence?.enabled === true) ids.push('atlassian');
  if (settings.sharepoint?.enabled === true) ids.push('sharepoint');
  if (settings.dataSources?.bigquery?.enabled === true) ids.push('bigquery');
  if (settings.dataSources?.databricks?.enabled === true) ids.push('databricks');
  if (settings.dataSources?.trino?.enabled === true) ids.push('trino');
  if (settings.dataSources?.snowflake?.enabled === true) ids.push('snowflake');
  if (settings.arxiv?.enabled === true) ids.push('arxiv');
  return ensureVisibleConnectionIds(connections, ids);
};

const ensureThreadSearchSettings = (raw: any): ThreadSearchSettings => ({
  enabled: typeof raw?.enabled === 'boolean'
    ? raw.enabled
    : DEFAULT_SETTINGS.threadSearch?.enabled ?? true,
});

const normalizeLocalComputerAccessMode = (value: any): LocalComputerAccessMode => (
  String(value || '').trim() === 'selected-folders'
    ? 'selected-folders'
    : 'whole-computer'
);

const ensureLocalComputerSettings = (raw: any): LocalComputerSettings => {
  const base = DEFAULT_SETTINGS.localComputer || {};
  return {
    enabled: typeof raw?.enabled === 'boolean' ? raw.enabled : base.enabled ?? false,
    accessMode: normalizeLocalComputerAccessMode(raw?.accessMode ?? base.accessMode),
    selectedFolders: stringArray(raw?.selectedFolders),
    ...(typeof raw?.lastCheckedAt === 'number' ? { lastCheckedAt: raw.lastCheckedAt } : {}),
    ...(typeof raw?.lastCheckOk === 'boolean' ? { lastCheckOk: raw.lastCheckOk } : {}),
    ...(typeof raw?.lastCheckMessage === 'string' ? { lastCheckMessage: raw.lastCheckMessage } : {}),
  };
};

const normalizeAtlassianUrl = (value: any, confluence = false): string => {
  const raw = typeof value === 'string' ? value.trim().replace(/\/+$/, '') : '';
  if (!raw) return '';
  try {
    const url = new URL(raw.includes('://') ? raw : `https://${raw}`);
    if (!['http:', 'https:'].includes(url.protocol)) return raw;
    if (confluence && !url.pathname.replace(/\/+$/, '') && url.hostname.toLowerCase().endsWith('.atlassian.net')) {
      url.pathname = '/wiki';
    }
    url.search = '';
    url.hash = '';
    return url.toString().replace(/\/+$/, '');
  } catch {
    return raw;
  }
};

const ensureAtlassianSettings = (raw: any) => {
  const base = DEFAULT_SETTINGS.atlassian || {};
  const jiraBaseUrl = normalizeAtlassianUrl(raw?.jiraBaseUrl) || base.jiraBaseUrl || '';
  const confluenceBaseUrl = normalizeAtlassianUrl(raw?.confluenceBaseUrl, true)
    || (jiraBaseUrl ? normalizeAtlassianUrl(`${jiraBaseUrl}/wiki`, true) : base.confluenceBaseUrl || '');
  return {
    jiraBaseUrl,
    confluenceBaseUrl,
    defaultJiraProjectKey: typeof raw?.defaultJiraProjectKey === 'string'
      ? raw.defaultJiraProjectKey.trim().toUpperCase()
      : base.defaultJiraProjectKey || '',
    allowedProjects: stringArray(raw?.allowedProjects).map((item) => item.toUpperCase()),
    allowedAuthDomains: stringArray(raw?.allowedAuthDomains).length
      ? stringArray(raw?.allowedAuthDomains).map((item) => item.toLowerCase())
      : base.allowedAuthDomains || [],
    browserProvider: raw?.browserProvider === 'bundled' ? 'bundled' : 'managed',
    browserChannel: typeof raw?.browserChannel === 'string' && raw.browserChannel.trim().toLowerCase() !== 'auto'
      ? raw.browserChannel.trim()
      : base.browserChannel || '',
    browserExecutablePath: typeof raw?.browserExecutablePath === 'string' ? raw.browserExecutablePath.trim() : base.browserExecutablePath || '',
    browserProfileDir: typeof raw?.browserProfileDir === 'string' ? raw.browserProfileDir.trim() : base.browserProfileDir || '',
    ...optionalLastVerifiedFingerprint(raw),
    jira: ensureAtlassianProductState(raw?.jira),
    confluence: ensureAtlassianProductState(raw?.confluence),
  };
};

const ensureBrowserControlSettings = (raw: any) => {
  const base = DEFAULT_SETTINGS.browserControl || {};
  return {
    enabled: typeof raw?.enabled === 'boolean' ? raw.enabled : base.enabled ?? false,
    showBrowser: typeof raw?.showBrowser === 'boolean' ? raw.showBrowser : base.showBrowser ?? false,
    browserProfileDir: typeof raw?.browserProfileDir === 'string'
      ? raw.browserProfileDir.trim()
      : base.browserProfileDir || '',
  };
};

const normalizeSharePointUrl = (value: any): string => {
  const raw = typeof value === 'string' ? value.trim().replace(/\/+$/, '') : '';
  if (!raw) return '';
  try {
    const url = new URL(raw.includes('://') ? raw : `https://${raw}`);
    if (!['http:', 'https:'].includes(url.protocol)) return raw;
    url.search = '';
    url.hash = '';
    return url.toString().replace(/\/+$/, '');
  } catch {
    return raw;
  }
};

const ensureSharePointSettings = (raw: any): SharePointSettings => {
  const base = DEFAULT_SETTINGS.sharepoint || {};
  const siteUrl = normalizeSharePointUrl(raw?.siteUrl ?? raw?.baseUrl ?? raw?.url) || base.siteUrl || '';
  const authMode = String(raw?.authMode ?? base.authMode ?? 'browser').trim().toLowerCase() === 'graph' ? 'graph' : 'browser';
  return {
    siteUrl,
    enabled: typeof raw?.enabled === 'boolean' ? raw.enabled : base.enabled ?? false,
    showBrowser: typeof raw?.showBrowser === 'boolean' ? raw.showBrowser : base.showBrowser ?? false,
    browserProfileDir: typeof raw?.browserProfileDir === 'string'
      ? raw.browserProfileDir.trim()
      : base.browserProfileDir || '',
    authMode,
    ...(typeof raw?.lastCheckedAt === 'number' ? { lastCheckedAt: raw.lastCheckedAt } : {}),
    ...(typeof raw?.lastCheckOk === 'boolean' ? { lastCheckOk: raw.lastCheckOk } : {}),
    ...(typeof raw?.lastCheckMessage === 'string' ? { lastCheckMessage: raw.lastCheckMessage } : {}),
    ...optionalLastVerifiedFingerprint(raw),
  };
};

const normalizeGitHubRepositoryList = (value: any): string[] => {
  const rawItems = Array.isArray(value)
    ? value
    : typeof value === 'string'
      ? value.split(/[\n,]+/)
      : [];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const rawItem of rawItems) {
    const item = String(rawItem || '')
      .trim()
      .replace(/^ssh:\/\/git@github\.com\//i, '')
      .replace(/^https?:\/\/github\.com\//i, '')
      .replace(/^git@github\.com:/i, '')
      .replace(/\.git$/i, '');
    if (!item || !item.includes('/')) continue;
    const parts = item.split('/').filter(Boolean);
    if (parts.length < 2) continue;
    const repo = `${parts[0]}/${parts[1]}`;
    const key = repo.toLowerCase();
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(repo);
  }
  return out;
};

const ensureGitHubSettings = (raw: any): GitHubSettings => {
  const base = DEFAULT_SETTINGS.github || {};
  const allowedRepositories = normalizeGitHubRepositoryList(
    raw?.allowedRepositories
      ?? raw?.allowedRepos
      ?? raw?.repositories
      ?? raw?.repositoryAllowlist
  );
  const enabled = typeof raw?.enabled === 'boolean'
    ? raw.enabled
    : typeof raw?.readTools === 'boolean'
      ? raw.readTools
      : base.enabled ?? false;
  const readTools = typeof raw?.readTools === 'boolean'
    ? raw.readTools
    : enabled;
  return {
    enabled,
    readTools,
    allowedRepositories,
    ...(typeof raw?.lastCheckedAt === 'number' ? { lastCheckedAt: raw.lastCheckedAt } : {}),
    ...(typeof raw?.lastCheckOk === 'boolean' ? { lastCheckOk: raw.lastCheckOk } : {}),
    ...(typeof raw?.lastCheckMessage === 'string' ? { lastCheckMessage: raw.lastCheckMessage } : {}),
    ...optionalLastVerifiedFingerprint(raw),
  };
};

const normalizeSlackIdList = (value: any): string[] => {
  const rawItems = Array.isArray(value)
    ? value
    : typeof value === 'string'
      ? value.split(/[\n,]+/)
      : [];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const rawItem of rawItems) {
    const item = String(rawItem || '').trim().toUpperCase();
    if (!item || seen.has(item)) continue;
    seen.add(item);
    out.push(item);
  }
  return out;
};

const ensureSlackSettings = (raw: any): SlackSettings => {
  const base = DEFAULT_SETTINGS.slack || {};
  const enabled = typeof raw?.enabled === 'boolean'
    ? raw.enabled
    : typeof raw?.readTools === 'boolean'
      ? raw.readTools
      : base.enabled ?? false;
  const readTools = typeof raw?.readTools === 'boolean'
    ? raw.readTools
    : enabled;
  const writeTools = typeof raw?.writeTools === 'boolean'
    ? raw.writeTools
    : base.writeTools ?? false;
  const rawWriteIdentity = String(raw?.writeIdentity ?? raw?.sendAs ?? base.writeIdentity ?? 'user').trim().toLowerCase();
  const writeIdentity = rawWriteIdentity === 'bot' ? 'bot' : 'user';
  return {
    enabled,
    readTools,
    writeTools,
    writeIdentity,
    allowedTeamIds: normalizeSlackIdList(raw?.allowedTeamIds ?? raw?.allowedTeams ?? raw?.teamAllowlist),
    allowedChannelIds: normalizeSlackIdList(raw?.allowedChannelIds ?? raw?.allowedChannels ?? raw?.channelAllowlist),
    ...(typeof raw?.lastCheckedAt === 'number' ? { lastCheckedAt: raw.lastCheckedAt } : {}),
    ...(typeof raw?.lastCheckOk === 'boolean' ? { lastCheckOk: raw.lastCheckOk } : {}),
    ...(typeof raw?.lastCheckMessage === 'string' ? { lastCheckMessage: raw.lastCheckMessage } : {}),
    ...optionalLastVerifiedFingerprint(raw),
  };
};

const ensureArxivSettings = (raw: any): ArxivSettings => {
  const base = DEFAULT_SETTINGS.arxiv || {};
  const enabled = typeof raw?.enabled === 'boolean' ? raw.enabled : base.enabled ?? false;
  const contactEmail = typeof raw?.contactEmail === 'string' ? raw.contactEmail.trim() : '';
  return {
    enabled,
    ...(contactEmail ? { contactEmail } : {}),
    ...(typeof raw?.lastCheckedAt === 'number' ? { lastCheckedAt: raw.lastCheckedAt } : {}),
    ...(typeof raw?.lastCheckOk === 'boolean' ? { lastCheckOk: raw.lastCheckOk } : {}),
    ...(typeof raw?.lastCheckMessage === 'string' ? { lastCheckMessage: raw.lastCheckMessage } : {}),
  };
};

const clampMaxImagesPerRun = (raw: any): number => {
  const value = typeof raw === 'number' && Number.isFinite(raw) ? Math.round(raw) : 1;
  return Math.min(4, Math.max(1, value));
};

const ensureImageGenerationSettings = (raw: any): ImageGenerationSettings => {
  const base = DEFAULT_SETTINGS.imageGeneration || {};
  const baseDefaults = base.defaults || {};
  const enabled = typeof raw?.enabled === 'boolean' ? raw.enabled : base.enabled ?? false;
  // Any non-empty string is a valid reference; it may dangle if the referenced
  // provider was later deleted, which the settings UI resolves and warns about.
  const providerId = typeof raw?.providerId === 'string' && raw.providerId.trim()
    ? raw.providerId.trim()
    : null;
  const model = typeof raw?.model === 'string' && raw.model.trim()
    ? raw.model.trim()
    : (base.model || 'gpt-image-2');
  const rawDefaults = raw?.defaults && typeof raw.defaults === 'object' ? raw.defaults : {};
  // Keep unknown-but-valid strings so future model sizes/qualities/formats survive.
  const size = typeof rawDefaults.size === 'string' && rawDefaults.size.trim()
    ? rawDefaults.size.trim()
    : (baseDefaults.size || 'auto');
  const quality = typeof rawDefaults.quality === 'string' && rawDefaults.quality.trim()
    ? rawDefaults.quality.trim()
    : (baseDefaults.quality || 'auto');
  const format = typeof rawDefaults.format === 'string' && rawDefaults.format.trim()
    ? rawDefaults.format.trim()
    : (baseDefaults.format || 'png');
  const maxImagesPerRun = clampMaxImagesPerRun(
    raw?.maxImagesPerRun ?? base.maxImagesPerRun,
  );
  return {
    enabled,
    providerId,
    model,
    defaults: { quality, size, format },
    maxImagesPerRun,
  };
};

const stripGitHubClientSecrets = (settings: Record<string, any>) => {
  delete settings.githubAccessToken;
  delete settings.githubOAuthToken;
  delete settings.githubOAuthAccessToken;
  delete settings.github_oauth_token;
  delete settings.github_oauth_access_token;
  delete settings.githubToken;
  delete settings.github_token;
  delete settings.githubPersonalAccessToken;
  delete settings.github_personal_access_token;
  delete settings.githubPAT;
  delete settings.gitHubAccessToken;
  if (settings.github && typeof settings.github === 'object') {
    delete settings.github.accessToken;
    delete settings.github.access_token;
    delete settings.github.oauthToken;
    delete settings.github.oauth_token;
    delete settings.github.oauthAccessToken;
    delete settings.github.oauth_access_token;
    delete settings.github.token;
    delete settings.github.personalAccessToken;
    delete settings.github.personal_access_token;
    delete settings.github.pat;
    delete settings.github.refreshToken;
    delete settings.github.refresh_token;
  }
};

const stripSlackClientSecrets = (settings: Record<string, any>) => {
  delete settings.slackAccessToken;
  delete settings.slack_access_token;
  delete settings.slackOAuthToken;
  delete settings.slack_oauth_token;
  delete settings.slackRefreshToken;
  delete settings.slack_refresh_token;
  delete settings.slackToken;
  delete settings.slack_token;
  delete settings.slackBotToken;
  delete settings.slack_bot_token;
  delete settings.slackUserToken;
  delete settings.slack_user_token;
  delete settings.slackWebhookUrl;
  delete settings.slack_webhook_url;
  if (settings.slack && typeof settings.slack === 'object') {
    delete settings.slack.accessToken;
    delete settings.slack.access_token;
    delete settings.slack.oauthToken;
    delete settings.slack.oauth_token;
    delete settings.slack.refreshToken;
    delete settings.slack.refresh_token;
    delete settings.slack.token;
    delete settings.slack.botToken;
    delete settings.slack.bot_token;
    delete settings.slack.userToken;
    delete settings.slack.user_token;
    delete settings.slack.webhookUrl;
    delete settings.slack.webhook_url;
    delete settings.slack.clientSecret;
    delete settings.slack.client_secret;
  }
};

const stripSharePointClientSecrets = (settings: Record<string, any>) => {
  delete settings.sharepointAccessToken;
  delete settings.sharepoint_access_token;
  delete settings.sharepointRefreshToken;
  delete settings.sharepoint_refresh_token;
  delete settings.sharepointToken;
  delete settings.sharepoint_token;
  delete settings.sharepointCookie;
  delete settings.sharepoint_cookie;
  if (settings.sharepoint && typeof settings.sharepoint === 'object') {
    delete settings.sharepoint.accessToken;
    delete settings.sharepoint.access_token;
    delete settings.sharepoint.refreshToken;
    delete settings.sharepoint.refresh_token;
    delete settings.sharepoint.token;
    delete settings.sharepoint.cookie;
    delete settings.sharepoint.cookies;
    delete settings.sharepoint.authorization;
    delete settings.sharepoint.Authorization;
    delete settings.sharepoint.FedAuth;
    delete settings.sharepoint.rtFa;
    delete settings.sharepoint.SPA_RT;
  }
};

const ensureFeatureFlags = (raw: any) => {
  const base = defaultFeatureFlags();
  return {
    ...(isRecord(raw) ? raw : {}),
    activation: raw?.activation ?? base.activation,
    providers: {
      ...(isRecord(raw?.providers) ? raw.providers : {}),
      openai: raw?.providers?.openai ?? base.providers.openai,
      anthropic: raw?.providers?.anthropic ?? base.providers.anthropic,
      gemini: raw?.providers?.gemini ?? base.providers.gemini,
      bedrock: raw?.providers?.bedrock ?? base.providers.bedrock,
      compatible: raw?.providers?.compatible ?? base.providers.compatible,
    },
    providerTemplates: raw?.providerTemplates ?? base.providerTemplates,
    cloudCredentialsAws: base.cloudCredentialsAws,
    cloudCredentialsTrino: base.cloudCredentialsTrino,
    cloudCredentialsSnowflake: base.cloudCredentialsSnowflake,
    chartDimensionFilters: raw?.chartDimensionFilters ?? base.chartDimensionFilters,
    chartEngines: {
      ...(isRecord(raw?.chartEngines) ? raw.chartEngines : {}),
      echarts: raw?.chartEngines?.echarts ?? base.chartEngines.echarts,
      highcharts: raw?.chartEngines?.highcharts ?? base.chartEngines.highcharts,
    },
    capabilityUx: {
      ...(isRecord(raw?.capabilityUx) ? raw.capabilityUx : {}),
      report: raw?.capabilityUx?.report ?? base.capabilityUx.report,
      editorV2: raw?.capabilityUx?.editorV2 ?? base.capabilityUx.editorV2,
      controls: raw?.capabilityUx?.controls ?? base.capabilityUx.controls,
      preflight: raw?.capabilityUx?.preflight ?? base.capabilityUx.preflight,
      composerTarget: raw?.capabilityUx?.composerTarget ?? base.capabilityUx.composerTarget,
    },
  };
};

const isProviderEnabledByFlags = (settings: AppSettings, providerId: string): boolean => {
  const flags = settings.featureFlags?.providers;
  if (!flags) return true;
  return (flags as Record<string, boolean>)[providerId] !== false;
};

const alignActiveProviderWithFlags = (settings: AppSettings): void => {
  let configs = Array.isArray(settings.providerConfigs) ? settings.providerConfigs : [];
  if (configs.length === 0) return;

  let enabledConfigs = configs.filter((cfg) => isProviderEnabledByFlags(settings, cfg.providerId));
  if (enabledConfigs.length === 0) {
    const fallbackProviderId =
      settings.featureFlags?.providers?.compatible !== false
        ? 'compatible'
        : settings.featureFlags?.providers?.bedrock !== false
          ? 'bedrock'
          : null;
    if (fallbackProviderId) {
      const fallbackSeed: AppSettings = {
        ...settings,
        providerConfigs: [],
        provider: {
          providerId: fallbackProviderId,
          model: fallbackProviderId === 'compatible'
            ? DEFAULT_SETTINGS.provider.model
            : (settings.provider.model || DEFAULT_SETTINGS.provider.model),
          profileId: fallbackProviderId === 'compatible'
            ? settings.compatibleProfiles?.[0]?.id || settings.provider.profileId || DEFAULT_SETTINGS.provider.profileId
            : null,
        },
      };
      const generated = ensureProviderConfigs(fallbackSeed)
        .filter((cfg) => isProviderEnabledByFlags(settings, cfg.providerId));
      if (generated.length > 0) {
        const existingIds = new Set(configs.map((cfg) => cfg.id));
        const merged = [...configs];
        for (const cfg of generated) {
          if (existingIds.has(cfg.id)) continue;
          merged.push(cfg);
        }
        settings.providerConfigs = merged;
        configs = merged;
        enabledConfigs = merged.filter((cfg) => isProviderEnabledByFlags(settings, cfg.providerId));
      }
    }
  }

  const desired = typeof settings.activeProviderConfigId === 'string'
    ? settings.activeProviderConfigId.trim()
    : '';
  const nextActive =
    (desired && enabledConfigs.find((cfg) => cfg.id === desired))
    || enabledConfigs[0]
    || configs.find((cfg) => cfg.id === desired)
    || configs[0]
    || null;

  settings.activeProviderConfigId = nextActive?.id || null;
  if (!nextActive) return;

  settings.provider = {
    providerId: nextActive.providerId,
    model: nextActive.model || settings.provider.model,
    profileId: nextActive.providerId === 'compatible' ? nextActive.id : null,
  };
};

const ensureCloudCredentials = (raw: any) => ({
  aws: Array.isArray(raw?.aws) ? raw.aws : [],
  gcp: Array.isArray(raw?.gcp) ? raw.gcp : [],
});

const normalizeProviderFileInputMode = (
  raw: unknown,
  _apiType: ProviderConfig['apiType'],
): NonNullable<ProviderConfig['fileInputMode']> => {
  if (raw === 'sidecar_only' || raw === 'native_pdf_only' || raw === 'native_supported') return raw;
  return 'native_supported';
};

const normalizeProviderConfig = (raw: any, index: number): ProviderConfig | null => {
  if (!raw || typeof raw !== 'object') return null;
  const providerId = typeof raw.providerId === 'string' ? raw.providerId : '';
  if (!providerId) return null;
  const id = typeof raw.id === 'string' && raw.id.trim() ? raw.id.trim() : `cfg-${providerId}-${index + 1}`;
  const createdAt = Number(raw.createdAt);
  const updatedAt = Number(raw.updatedAt);
  const normalizedApiType = typeof raw.apiType === 'string' && raw.apiType.trim()
    ? raw.apiType.trim()
    : providerId === 'openai'
      ? 'responses'
      : providerId === 'bedrock'
        ? 'messages'
        : 'chat_completions';
  const normalizedFileInputMode = normalizeProviderFileInputMode(raw.fileInputMode, normalizedApiType as ProviderConfig['apiType']);
  const normalizedCompactionEnabled = providerId === 'openai'
    ? (typeof raw.compactionEnabled === 'boolean' ? raw.compactionEnabled : true)
    : providerId === 'anthropic'
      ? (typeof raw.compactionEnabled === 'boolean' ? raw.compactionEnabled : true)
      : providerId === 'bedrock'
        ? normalizedApiType === 'messages'
          ? (typeof raw.compactionEnabled === 'boolean' ? raw.compactionEnabled : true)
          : false
        : providerId === 'compatible'
          ? normalizedApiType === 'responses' && raw.compactionEnabled === true
          : undefined;
  const normalizedCompactionInstruction = providerId === 'anthropic'
    ? normalizeCompactionInstruction(raw.compactionInstruction)
    : providerId === 'bedrock'
      ? normalizedApiType === 'messages'
        ? normalizeCompactionInstruction(raw.compactionInstruction)
        : null
      : undefined;
  const normalizedCompactionTriggerTokens = providerId === 'openai'
    ? (Number.isFinite(Number(raw.compactionTriggerTokens)) && Number(raw.compactionTriggerTokens) >= 50000
      ? Math.round(Number(raw.compactionTriggerTokens))
      : OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS)
    : providerId === 'anthropic'
      ? (Number.isFinite(Number(raw.compactionTriggerTokens)) && Number(raw.compactionTriggerTokens) >= 50000
        ? Math.round(Number(raw.compactionTriggerTokens))
        : BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS)
      : providerId === 'bedrock'
        ? normalizedApiType === 'messages'
          ? (Number.isFinite(Number(raw.compactionTriggerTokens)) && Number(raw.compactionTriggerTokens) >= 50000
            ? Math.round(Number(raw.compactionTriggerTokens))
            : BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS)
          : null
        : providerId === 'compatible'
          ? normalizedApiType === 'responses' && raw.compactionEnabled === true
            ? (Number.isFinite(Number(raw.compactionTriggerTokens)) && Number(raw.compactionTriggerTokens) >= 50000
              ? Math.round(Number(raw.compactionTriggerTokens))
              : OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS)
            : null
          : undefined;
  const hasCapabilityControls = isRecord(raw.capabilityControls);
  const capabilityControls = hasCapabilityControls
    ? preserveCapabilityControls(raw.capabilityControls)
    : migrateLegacyCapabilityControls({
      providerId: providerId as ProviderConfig['providerId'],
      webSearch: raw.webSearch,
      codeExecution: raw.codeExecution,
      compactionEnabled: raw.compactionEnabled,
    });
  const apiSurface = deriveProviderApiSurface({
    providerId: providerId as ProviderConfig['providerId'],
    apiType: normalizedApiType as ProviderConfig['apiType'],
    apiSurface: typeof raw.apiSurface === 'string' ? raw.apiSurface : undefined,
  });
  return {
    ...(raw as ProviderConfig),
    id,
    providerId: providerId as ProviderConfig['providerId'],
    displayName: typeof raw.displayName === 'string' && raw.displayName.trim() ? raw.displayName : providerId.toUpperCase(),
    model: providerId === 'bedrock'
      ? normalizeBedrockModelId(raw.model)
      : typeof raw.model === 'string' ? raw.model : '',
    apiType: normalizedApiType as ProviderConfig['apiType'],
    apiSurface,
    authType: raw.authType === 'none' || raw.authType === 'cloud_credentials' ? raw.authType : 'api_key',
    compactionEnabled: normalizedCompactionEnabled,
    compactionInstruction: normalizedCompactionInstruction,
    compactionTriggerTokens: normalizedCompactionTriggerTokens,
    fileInputMode: providerId === 'compatible' ? normalizedFileInputMode : undefined,
    // Legacy blobs retain their effective OpenAI/Anthropic On default. New
    // editor blobs are identified by capabilityControls and never have a
    // hosted capability injected implicitly.
    webSearch: hasCapabilityControls
      ? normalizeWebSearchConfig(raw.webSearch)
      : resolveWebSearchConfig(providerId, raw.webSearch),
    codeExecution: normalizeCodeExecutionConfig(raw.codeExecution),
    capabilityControls,
    queryParams: raw.queryParams && typeof raw.queryParams === 'object' && !Array.isArray(raw.queryParams) ? raw.queryParams : null,
    payloadOverrides: raw.payloadOverrides && typeof raw.payloadOverrides === 'object' && !Array.isArray(raw.payloadOverrides) ? raw.payloadOverrides : null,
    createdAt: Number.isFinite(createdAt) ? createdAt : Date.now(),
    updatedAt: Number.isFinite(updatedAt) ? updatedAt : Date.now(),
    lastTestedAt: Number.isFinite(Number(raw.lastTestedAt)) ? Number(raw.lastTestedAt) : null,
    lastTestStatus: raw.lastTestStatus === 'success' || raw.lastTestStatus === 'failure' ? raw.lastTestStatus : null,
    lastTestMessage: typeof raw.lastTestMessage === 'string' ? raw.lastTestMessage : null,
    lastTestEndpoint: typeof raw.lastTestEndpoint === 'string' ? raw.lastTestEndpoint : null,
  };
};

const providerWebSearchEffectivelyEnabled = (config: ProviderConfig): boolean => {
  const control = config.capabilityControls?.hosted_web_search;
  if (control != null) return control === 'on';
  return config.webSearch?.enabled === true;
};

/**
 * Web Search is one app-level connection backed by the selected provider's
 * first-party hosted tool. Older app versions could persist it On for one
 * provider and Off for another, which made the catalogue look ready while an
 * Azure run silently omitted the tool. Collapse every supported provider onto
 * one explicit value during every settings load so stale disk state cannot
 * survive long enough to reach a run.
 */
const reconcileGlobalWebSearch = (configs: ProviderConfig[]): ProviderConfig[] => {
  const supported = configs.filter(supportsProviderHostedTools);
  const enabled = supported.some(providerWebSearchEffectivelyEnabled);
  return configs.map((config) => {
    if (!supportsProviderHostedTools(config)) {
      return {
        ...config,
        capabilityControls: config.capabilityControls
          ? { ...config.capabilityControls, hosted_web_search: 'off' }
          : config.capabilityControls,
        webSearch: null,
      };
    }
    return {
      ...config,
      capabilityControls: {
        ...(config.capabilityControls || {}),
        hosted_web_search: enabled ? 'on' : 'off',
      },
      webSearch: canonicalProviderWebSearch(config.providerId, enabled),
    };
  });
};

/**
 * Older clients also stored Web Search on provider-specific compatibility
 * mirrors. Keep every mirror canonical so opening and saving settings with an
 * old build cannot reintroduce an obsolete tool type into a newer build.
 */
const reconcileGlobalWebSearchMirrors = (settings: AppSettings): void => {
  const supported = (settings.providerConfigs || []).filter(supportsProviderHostedTools);
  const enabled = supported.some(providerWebSearchEffectivelyEnabled);
  const mirrorEnabled = (current: ProviderWebSearchConfig | null | undefined): boolean => (
    supported.length > 0 ? enabled : current?.enabled === true
  );

  if (settings.openai) {
    settings.openai = {
      ...settings.openai,
      webSearch: canonicalProviderWebSearch('openai', mirrorEnabled(settings.openai.webSearch)),
    };
  }
  if (settings.anthropic) {
    settings.anthropic = {
      ...settings.anthropic,
      webSearch: canonicalProviderWebSearch('anthropic', mirrorEnabled(settings.anthropic.webSearch)),
    };
  }
  settings.compatibleProfiles = (settings.compatibleProfiles || []).map((profile) => ({
    ...profile,
    webSearch: profile.apiType === 'responses'
      ? canonicalProviderWebSearch('compatible', mirrorEnabled(profile.webSearch))
      : null,
  }));
};

const ensureProviderConfigs = (merged: AppSettings): ProviderConfig[] => {
  if (Array.isArray((merged as any).providerConfigs) && (merged as any).providerConfigs.length > 0) {
    return (merged as any).providerConfigs
      .map((cfg: any, index: number) => normalizeProviderConfig(cfg, index))
      .filter(Boolean) as ProviderConfig[];
  }

  const providerId = merged.provider.providerId;
  const defaultModel = merged.provider.providerId === 'bedrock'
    ? normalizeBedrockModelId(merged.provider.model)
    : (merged.provider.model || '').trim();
  if (defaultModel === '') {
    return [];
  }

  if (providerId === 'compatible') {
    const profile = (merged.compatibleProfiles || []).find((p) => p.id === merged.provider.profileId) || merged.compatibleProfiles[0];
    if (!profile) return [];
    return [{
      id: profile.id || `cfg-compatible-default`,
      displayName: profile.displayName || 'Compatible Provider',
      providerId: 'compatible' as ProviderConfig['providerId'],
      model: defaultModel,
      apiType: profile.apiType,
      apiSurface: profile.apiType,
      baseUrl: profile.baseUrl,
      authType: profile.auth.type,
      authHeader: profile.auth.header,
      authScheme: profile.auth.scheme || null,
      endpointSuffix: profile.endpointSuffix || null,
      timeoutMs: profile.timeoutMs || null,
      reasoningEffort: profile.reasoningEffort || null,
      modelInPath: Boolean(profile.modelInPath),
      modelInBody: profile.modelInBody !== false,
      fileInputMode: normalizeProviderFileInputMode(profile.fileInputMode, profile.apiType),
	      extraHeaders: profile.extraHeaders || [],
      queryParams: profile.queryParams || null,
      payloadOverrides: profile.payloadOverrides || null,
      webSearch: normalizeWebSearchConfig(profile.webSearch),
      codeExecution: normalizeCodeExecutionConfig(profile.codeExecution),
      capabilityControls: migrateLegacyCapabilityControls({
        providerId: 'compatible',
        webSearch: profile.webSearch,
        codeExecution: profile.codeExecution,
      }),
      createdAt: Date.now(),
      updatedAt: Date.now(),
      isDefault: true,
    }];
  }

  const now = Date.now();
  return [{
    id: `cfg-${providerId}-default`,
    displayName: providerId.toUpperCase(),
    providerId: providerId as ProviderConfig['providerId'],
    model: defaultModel,
    apiType: providerId === 'openai'
      ? 'responses'
      : providerId === 'bedrock'
        ? 'messages'
        : 'chat_completions',
    apiSurface: deriveProviderApiSurface({
      providerId: providerId as ProviderConfig['providerId'],
      apiType: providerId === 'openai'
        ? 'responses'
        : providerId === 'bedrock'
          ? 'messages'
          : 'chat_completions',
    }),
    baseUrl: providerId === 'openai'
      ? merged.openai?.baseUrl
      : providerId === 'anthropic'
        ? merged.anthropic?.baseUrl
        : providerId === 'gemini'
          ? merged.gemini?.baseUrl
          : providerId === 'bedrock'
            ? merged.bedrock?.baseUrl
            : merged.gemini?.baseUrl,
    authType: 'api_key',
    authHeader: providerId === 'anthropic' ? 'x-api-key' : 'Authorization',
    authScheme: providerId === 'anthropic' ? null : 'Bearer',
    region: providerId === 'bedrock' ? merged.bedrock?.region || 'us-east-1' : null,
    anthropicVersion: providerId === 'anthropic'
      ? merged.anthropic?.anthropicVersion || '2023-06-01'
      : providerId === 'bedrock'
        ? merged.bedrock?.anthropicVersion || 'bedrock-2023-05-31'
        : null,
    compactionEnabled: providerId === 'openai'
      ? (merged.openai?.compactionEnabled ?? true)
      : providerId === 'anthropic'
        ? (merged.anthropic?.compactionEnabled ?? true)
        : providerId === 'bedrock'
          ? (merged.bedrock?.compactionEnabled ?? true)
          : undefined,
    compactionInstruction: providerId === 'anthropic'
      ? normalizeCompactionInstruction(merged.anthropic?.compactionInstruction)
      : providerId === 'bedrock'
        ? normalizeCompactionInstruction(merged.bedrock?.compactionInstruction)
        : undefined,
    compactionTriggerTokens: providerId === 'openai'
      ? (Number(merged.openai?.compactionTriggerTokens) >= 50000
        ? Math.round(Number(merged.openai?.compactionTriggerTokens))
        : OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS)
      : providerId === 'anthropic'
        ? (Number(merged.anthropic?.compactionTriggerTokens) >= 50000
          ? Math.round(Number(merged.anthropic?.compactionTriggerTokens))
          : BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS)
        : providerId === 'bedrock'
          ? (Number(merged.bedrock?.compactionTriggerTokens) >= 50000
            ? Math.round(Number(merged.bedrock?.compactionTriggerTokens))
            : BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS)
          : undefined,
    extraHeaders: providerId === 'bedrock' ? (merged.bedrock?.extraHeaders || []) : [],
    webSearch: providerId === 'openai'
      ? resolveWebSearchConfig('openai', merged.openai?.webSearch)
      : providerId === 'anthropic'
        ? resolveWebSearchConfig('anthropic', merged.anthropic?.webSearch)
        : null,
    codeExecution: providerId === 'openai'
      ? normalizeCodeExecutionConfig(merged.openai?.codeExecution)
      : providerId === 'anthropic'
        ? normalizeCodeExecutionConfig(merged.anthropic?.codeExecution)
        : null,
    capabilityControls: migrateLegacyCapabilityControls({
      providerId: providerId as ProviderConfig['providerId'],
      webSearch: providerId === 'openai'
        ? merged.openai?.webSearch
        : providerId === 'anthropic'
          ? merged.anthropic?.webSearch
          : null,
      codeExecution: providerId === 'openai'
        ? merged.openai?.codeExecution
        : providerId === 'anthropic'
          ? merged.anthropic?.codeExecution
          : null,
      compactionEnabled: providerId === 'openai'
        ? merged.openai?.compactionEnabled
        : providerId === 'anthropic'
          ? merged.anthropic?.compactionEnabled
          : providerId === 'bedrock'
            ? merged.bedrock?.compactionEnabled
            : undefined,
    }),
    createdAt: now,
    updatedAt: now,
    isDefault: true,
    modelInBody: true,
    modelInPath: false,
  }];
};

const buildLegacyCompatibleProfile = (raw: Record<string, unknown>): CompatibleProfile => {
  const url = typeof raw.url === 'string' ? raw.url : '';
  const apiType = inferCompatibleApiType(url);
  const baseUrl = normalizeCompatibleProfileBaseUrl(url) || 'https://api.openai.com/v1';

  const headers = Array.isArray(raw.headers) ? (raw.headers as Header[]) : undefined;
  const apiKey = extractBearerKeyFromHeaders(headers);

  return {
    id: 'legacy-default',
    displayName: 'Legacy Endpoint',
    apiType,
    baseUrl,
    auth: apiKey ? { type: 'api_key', header: 'Authorization', scheme: 'Bearer' } : { type: 'none', header: 'Authorization', scheme: 'Bearer' },
    apiKey: apiKey || null,
    extraHeaders: headersArrayToExtraHeaders(headers),
    queryParams: null,
    payloadOverrides: null,
    timeoutMs: null,
    reasoningEffort: null,
    modelInPath: false,
    modelInBody: true,
    endpointSuffix: null,
  };
};

export function migrateSettings(raw: unknown): AppSettings {
  // Already-new settings.
  if (isRecord(raw) && isRecord(raw.provider) && Array.isArray(raw.compatibleProfiles)) {
    const rawHadThreadSearchSettings = isRecord((raw as any).threadSearch);
    // Merge with defaults to pick up newly added fields.
    const merged: AppSettings = {
      ...DEFAULT_SETTINGS,
      ...(raw as any),
    };
    stripGitHubClientSecrets(merged as Record<string, any>);
    stripSlackClientSecrets(merged as Record<string, any>);

    // Never keep API keys in browser settings (keys live in the local encrypted secrets store).
    if (merged.openai) merged.openai.apiKey = '';
    if (merged.anthropic) merged.anthropic.apiKey = '';
    if (merged.gemini) merged.gemini.apiKey = '';
    if (merged.openai) {
      merged.openai.webSearch = resolveWebSearchConfig('openai', merged.openai.webSearch);
      merged.openai.codeExecution = normalizeCodeExecutionConfig(merged.openai.codeExecution);
    }
    if (merged.anthropic) {
      merged.anthropic.webSearch = resolveWebSearchConfig('anthropic', merged.anthropic.webSearch);
      merged.anthropic.codeExecution = normalizeCodeExecutionConfig(merged.anthropic.codeExecution);
    }
    if (merged.bedrock) {
      merged.bedrock.webSearch = null;
      merged.bedrock.codeExecution = null;
    }
    if (typeof merged.googleAccessToken === 'string') merged.googleAccessToken = '';
    if (typeof merged.googleCredentialsPath !== 'string') merged.googleCredentialsPath = DEFAULT_SETTINGS.googleCredentialsPath;
    if (typeof (merged as any).databricksHost !== 'string') merged.databricksHost = DEFAULT_SETTINGS.databricksHost;
    if (typeof (merged as any).databricksWarehouseId !== 'string') merged.databricksWarehouseId = DEFAULT_SETTINGS.databricksWarehouseId;
    if (typeof (merged as any).databricksOAuthClientId !== 'string') merged.databricksOAuthClientId = DEFAULT_SETTINGS.databricksOAuthClientId;
    if (typeof (merged as any).githubOAuthClientId !== 'string') merged.githubOAuthClientId = DEFAULT_SETTINGS.githubOAuthClientId;
    if (typeof (merged as any).slackOAuthClientId !== 'string') merged.slackOAuthClientId = DEFAULT_SETTINGS.slackOAuthClientId;
    if (typeof (merged as any).trinoHost !== 'string') merged.trinoHost = DEFAULT_SETTINGS.trinoHost;
    if (typeof (merged as any).trinoCatalog !== 'string') merged.trinoCatalog = DEFAULT_SETTINGS.trinoCatalog;
    if (typeof (merged as any).trinoSchema !== 'string') merged.trinoSchema = DEFAULT_SETTINGS.trinoSchema;
    if (typeof (merged as any).trinoUser !== 'string') merged.trinoUser = DEFAULT_SETTINGS.trinoUser;
    if (typeof (merged as any).trinoSource !== 'string') merged.trinoSource = DEFAULT_SETTINGS.trinoSource;
    if (typeof (merged as any).snowflakeAccountIdentifier !== 'string') merged.snowflakeAccountIdentifier = DEFAULT_SETTINGS.snowflakeAccountIdentifier;
    if (typeof (merged as any).snowflakeWarehouse !== 'string') merged.snowflakeWarehouse = DEFAULT_SETTINGS.snowflakeWarehouse;
    if (typeof (merged as any).snowflakeDatabase !== 'string') merged.snowflakeDatabase = DEFAULT_SETTINGS.snowflakeDatabase;
    if (typeof (merged as any).snowflakeSchema !== 'string') merged.snowflakeSchema = DEFAULT_SETTINGS.snowflakeSchema;
    if (typeof (merged as any).snowflakeRole !== 'string') merged.snowflakeRole = DEFAULT_SETTINGS.snowflakeRole;
    if (typeof (merged as any).snowflakeUser !== 'string') merged.snowflakeUser = DEFAULT_SETTINGS.snowflakeUser;
    if (typeof (merged as any).databaseQueryRowLimit !== 'number' || !Number.isFinite((merged as any).databaseQueryRowLimit)) {
      merged.databaseQueryRowLimit = DEFAULT_SETTINGS.databaseQueryRowLimit;
    } else {
      const rawLimit = Math.trunc((merged as any).databaseQueryRowLimit);
      merged.databaseQueryRowLimit = Math.max(1, Math.min(MAX_DATABASE_QUERY_ROW_LIMIT, rawLimit));
    }
    merged.databaseQueryPolicy = normalizeDatabaseQueryPolicy(
      (raw as any).databaseQueryPolicy,
      merged.databaseQueryRowLimit ?? DEFAULT_DATABASE_QUERY_POLICY.maxResultRows,
    );

    // Treat null/empty base URLs as "use default" so we can evolve defaults safely.
    if (merged.openai) {
      const baseUrl = (merged.openai.baseUrl ?? '').trim();
      if (!baseUrl) merged.openai.baseUrl = DEFAULT_SETTINGS.openai?.baseUrl ?? null;
      else merged.openai.baseUrl = normalizeProviderBaseUrl('openai', baseUrl);
      merged.openai.compactionEnabled = typeof merged.openai.compactionEnabled === 'boolean'
        ? merged.openai.compactionEnabled
        : true;
      const compactionTriggerTokens = Number(merged.openai.compactionTriggerTokens);
      merged.openai.compactionTriggerTokens = Number.isFinite(compactionTriggerTokens) && compactionTriggerTokens >= 50000
        ? Math.round(compactionTriggerTokens)
        : OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS;
    }
    if (merged.anthropic) {
      const baseUrl = (merged.anthropic.baseUrl ?? '').trim();
      if (!baseUrl) merged.anthropic.baseUrl = DEFAULT_SETTINGS.anthropic?.baseUrl ?? null;
      else merged.anthropic.baseUrl = normalizeProviderBaseUrl('anthropic', baseUrl);
      merged.anthropic.compactionEnabled = typeof merged.anthropic.compactionEnabled === 'boolean'
        ? merged.anthropic.compactionEnabled
        : true;
      merged.anthropic.compactionInstruction = normalizeCompactionInstruction(merged.anthropic.compactionInstruction);
      const compactionTriggerTokens = Number(merged.anthropic.compactionTriggerTokens);
      merged.anthropic.compactionTriggerTokens = Number.isFinite(compactionTriggerTokens) && compactionTriggerTokens >= 50000
        ? Math.round(compactionTriggerTokens)
        : BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS;
    }
    if (merged.gemini) {
      const baseUrl = (merged.gemini.baseUrl ?? '').trim();
      if (!baseUrl) merged.gemini.baseUrl = DEFAULT_SETTINGS.gemini?.baseUrl ?? null;
      else merged.gemini.baseUrl = normalizeProviderBaseUrl('gemini', baseUrl);
    }
    if (merged.bedrock) {
      const region = (merged.bedrock.region ?? '').trim();
      merged.bedrock.region = region || DEFAULT_SETTINGS.bedrock?.region || 'us-east-1';

      const baseUrl = (merged.bedrock.baseUrl ?? '').trim();
      merged.bedrock.baseUrl = baseUrl ? baseUrl.replace(/\/+$/, '') : null;

      const anthropicVersion = (merged.bedrock.anthropicVersion ?? '').trim();
      merged.bedrock.anthropicVersion = anthropicVersion || DEFAULT_SETTINGS.bedrock?.anthropicVersion || 'bedrock-2023-05-31';
      merged.bedrock.compactionEnabled = typeof merged.bedrock.compactionEnabled === 'boolean'
        ? merged.bedrock.compactionEnabled
        : true;
      merged.bedrock.compactionInstruction = normalizeCompactionInstruction(merged.bedrock.compactionInstruction);
      const compactionTriggerTokens = Number(merged.bedrock.compactionTriggerTokens);
      merged.bedrock.compactionTriggerTokens = Number.isFinite(compactionTriggerTokens) && compactionTriggerTokens >= 50000
        ? Math.round(compactionTriggerTokens)
        : BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS;

      // Never keep AWS credentials in browser settings.
      merged.bedrock.apiKey = '';
      merged.bedrock.accessKeyId = '';
      merged.bedrock.secretAccessKey = '';
      merged.bedrock.sessionToken = null;

      merged.bedrock.extraHeaders = (merged.bedrock.extraHeaders || []).map((h) => h.isSecret ? { ...h, value: '' } : h);
    }

    // Normalize compatible profile base URLs.
    merged.compatibleProfiles = (merged.compatibleProfiles || []).map((p) => {
      const base = normalizeCompatibleProfileBaseUrl(p.baseUrl || '');
      // Migrate old local proxy base URLs to upstream OpenAI.
      const migrated = base === '/proxy/openai/v1' || base === '/api/proxy/openai/v1'
        ? 'https://api.openai.com/v1'
        : base;
      const extraHeaders = (p.extraHeaders || []).map((h) => h.isSecret ? { ...h, value: '' } : h);
      return {
        ...p,
        baseUrl: migrated,
        apiKey: '',
        extraHeaders,
        webSearch: normalizeWebSearchConfig(p.webSearch),
        codeExecution: normalizeCodeExecutionConfig(p.codeExecution),
      };
    });

    merged.featureFlags = ensureFeatureFlags((merged as any).featureFlags);
    merged.cloudCredentials = ensureCloudCredentials((merged as any).cloudCredentials);
    merged.dataSources = ensureDataSources((merged as any).dataSources);
    merged.connections = ensureConnectionsSettings((merged as any).connections);
    merged.threadSearch = ensureThreadSearchSettings((merged as any).threadSearch);
    merged.localComputer = ensureLocalComputerSettings((merged as any).localComputer);
    if (!rawHadThreadSearchSettings) {
      merged.connections = ensureDefaultThreadSearchConnection(merged.connections, merged.threadSearch);
    }
    merged.atlassian = ensureAtlassianSettings((merged as any).atlassian);
    merged.browserControl = ensureBrowserControlSettings((merged as any).browserControl);
    merged.sharepoint = ensureSharePointSettings((merged as any).sharepoint);
    merged.github = ensureGitHubSettings((merged as any).github);
    merged.slack = ensureSlackSettings((merged as any).slack);
    merged.arxiv = ensureArxivSettings((merged as any).arxiv);
    merged.imageGeneration = ensureImageGenerationSettings((merged as any).imageGeneration);
    merged.connections = ensureEnabledConnectionsVisible(merged.connections, merged);
    stripGitHubClientSecrets(merged as Record<string, any>);
    stripSlackClientSecrets(merged as Record<string, any>);
    stripSharePointClientSecrets(merged as Record<string, any>);
    delete (merged as any).trinoAuthMode;
    merged.providerConfigs = reconcileGlobalWebSearch(ensureProviderConfigs(merged));
    reconcileGlobalWebSearchMirrors(merged);
    merged.connections = ensureEnabledConnectionsVisible(merged.connections, merged);
    if (!Array.isArray((merged as any).providerTemplates)) merged.providerTemplates = [];
    if (typeof (merged as any).activeProviderConfigId !== 'string') {
      merged.activeProviderConfigId = merged.providerConfigs[0]?.id || null;
    } else if (!merged.providerConfigs.some((cfg) => cfg.id === (merged as any).activeProviderConfigId)) {
      merged.activeProviderConfigId = merged.providerConfigs[0]?.id || null;
    }
    alignActiveProviderWithFlags(merged);

    return merged;
  }

  // Legacy settings from earlier versions.
  if (isRecord(raw) && typeof raw.url === 'string') {
    const legacy = raw;
    // Legacy settings were OpenAI-compatible; keep the old default model as a safe fallback.
    const legacyModel = typeof legacy.model === 'string' ? legacy.model : 'gpt-4o-mini';

    const profile = buildLegacyCompatibleProfile(legacy);
    // Keys must be re-entered into the local encrypted secrets store.
    profile.apiKey = null;

    const next: AppSettings = {
      ...DEFAULT_SETTINGS,
      provider: {
        providerId: 'compatible',
        model: legacyModel,
        profileId: profile.id,
      },
      compatibleProfiles: [profile],
      systemPrompt: typeof legacy.systemPrompt === 'string' ? legacy.systemPrompt : DEFAULT_SETTINGS.systemPrompt,
      googleProjectId: typeof legacy.googleProjectId === 'string' ? legacy.googleProjectId : DEFAULT_SETTINGS.googleProjectId,
      googleAccessToken: typeof legacy.googleAccessToken === 'string' ? legacy.googleAccessToken : DEFAULT_SETTINGS.googleAccessToken,
      googleCredentialsPath: typeof (legacy as any).googleCredentialsPath === 'string'
        ? String((legacy as any).googleCredentialsPath)
        : DEFAULT_SETTINGS.googleCredentialsPath,
      databricksOAuthClientId: DEFAULT_SETTINGS.databricksOAuthClientId,
      githubOAuthClientId: DEFAULT_SETTINGS.githubOAuthClientId,
      slackOAuthClientId: DEFAULT_SETTINGS.slackOAuthClientId,
      trinoHost: typeof (legacy as any).trinoHost === 'string'
        ? String((legacy as any).trinoHost)
        : DEFAULT_SETTINGS.trinoHost,
      trinoCatalog: typeof (legacy as any).trinoCatalog === 'string'
        ? String((legacy as any).trinoCatalog)
        : DEFAULT_SETTINGS.trinoCatalog,
      trinoSchema: typeof (legacy as any).trinoSchema === 'string'
        ? String((legacy as any).trinoSchema)
        : DEFAULT_SETTINGS.trinoSchema,
      trinoUser: typeof (legacy as any).trinoUser === 'string'
        ? String((legacy as any).trinoUser)
        : DEFAULT_SETTINGS.trinoUser,
      trinoSource: typeof (legacy as any).trinoSource === 'string'
        ? String((legacy as any).trinoSource)
        : DEFAULT_SETTINGS.trinoSource,
      snowflakeAccountIdentifier: typeof (legacy as any).snowflakeAccountIdentifier === 'string'
        ? String((legacy as any).snowflakeAccountIdentifier)
        : DEFAULT_SETTINGS.snowflakeAccountIdentifier,
      snowflakeWarehouse: typeof (legacy as any).snowflakeWarehouse === 'string'
        ? String((legacy as any).snowflakeWarehouse)
        : DEFAULT_SETTINGS.snowflakeWarehouse,
      snowflakeDatabase: typeof (legacy as any).snowflakeDatabase === 'string'
        ? String((legacy as any).snowflakeDatabase)
        : DEFAULT_SETTINGS.snowflakeDatabase,
      snowflakeSchema: typeof (legacy as any).snowflakeSchema === 'string'
        ? String((legacy as any).snowflakeSchema)
        : DEFAULT_SETTINGS.snowflakeSchema,
      snowflakeRole: typeof (legacy as any).snowflakeRole === 'string'
        ? String((legacy as any).snowflakeRole)
        : DEFAULT_SETTINGS.snowflakeRole,
      snowflakeUser: typeof (legacy as any).snowflakeUser === 'string'
        ? String((legacy as any).snowflakeUser)
        : DEFAULT_SETTINGS.snowflakeUser,
      openai: { ...(DEFAULT_SETTINGS.openai || { apiKey: '', baseUrl: null }), apiKey: '' },
    };
    next.dataSources = ensureDataSources((next as any).dataSources);
    next.connections = ensureConnectionsSettings((next as any).connections);
    next.threadSearch = ensureThreadSearchSettings((next as any).threadSearch);
    next.localComputer = ensureLocalComputerSettings((next as any).localComputer);
    next.connections = ensureDefaultThreadSearchConnection(next.connections, next.threadSearch);
    next.atlassian = ensureAtlassianSettings((next as any).atlassian);
    next.browserControl = ensureBrowserControlSettings((next as any).browserControl);
    next.sharepoint = ensureSharePointSettings((next as any).sharepoint);
    next.github = ensureGitHubSettings((next as any).github);
    next.slack = ensureSlackSettings((next as any).slack);
    next.arxiv = ensureArxivSettings((next as any).arxiv);
    next.imageGeneration = ensureImageGenerationSettings((next as any).imageGeneration);
    next.connections = ensureEnabledConnectionsVisible(next.connections, next);
    stripGitHubClientSecrets(next as Record<string, any>);
    stripSlackClientSecrets(next as Record<string, any>);
    stripSharePointClientSecrets(next as Record<string, any>);

    next.featureFlags = defaultFeatureFlags();
    next.cloudCredentials = { aws: [], gcp: [] };
    next.providerConfigs = reconcileGlobalWebSearch(ensureProviderConfigs(next));
    reconcileGlobalWebSearchMirrors(next);
    next.connections = ensureEnabledConnectionsVisible(next.connections, next);
    next.providerTemplates = [];
    next.activeProviderConfigId = next.providerConfigs[0]?.id || null;
    alignActiveProviderWithFlags(next);
    return next;
  }

  // Unknown shape: fall back to defaults.
  const fallback = { ...DEFAULT_SETTINGS };
  (fallback as any).featureFlags = defaultFeatureFlags();
  (fallback as any).cloudCredentials = { aws: [], gcp: [] };
  (fallback as any).dataSources = ensureDataSources((fallback as any).dataSources);
  (fallback as any).connections = ensureConnectionsSettings((fallback as any).connections);
  (fallback as any).threadSearch = ensureThreadSearchSettings((fallback as any).threadSearch);
  (fallback as any).localComputer = ensureLocalComputerSettings((fallback as any).localComputer);
  (fallback as any).connections = ensureDefaultThreadSearchConnection((fallback as any).connections, (fallback as any).threadSearch);
  (fallback as any).atlassian = ensureAtlassianSettings((fallback as any).atlassian);
  (fallback as any).browserControl = ensureBrowserControlSettings((fallback as any).browserControl);
  (fallback as any).sharepoint = ensureSharePointSettings((fallback as any).sharepoint);
  (fallback as any).github = ensureGitHubSettings((fallback as any).github);
  (fallback as any).slack = ensureSlackSettings((fallback as any).slack);
  (fallback as any).arxiv = ensureArxivSettings((fallback as any).arxiv);
  (fallback as any).imageGeneration = ensureImageGenerationSettings((fallback as any).imageGeneration);
  (fallback as any).connections = ensureEnabledConnectionsVisible((fallback as any).connections, fallback as AppSettings);
  stripGitHubClientSecrets(fallback as Record<string, any>);
  stripSlackClientSecrets(fallback as Record<string, any>);
  stripSharePointClientSecrets(fallback as Record<string, any>);
  (fallback as any).providerConfigs = reconcileGlobalWebSearch(ensureProviderConfigs(fallback as any));
  reconcileGlobalWebSearchMirrors(fallback as AppSettings);
  (fallback as any).connections = ensureEnabledConnectionsVisible((fallback as any).connections, fallback as AppSettings);
  (fallback as any).providerTemplates = [];
  (fallback as any).activeProviderConfigId = (fallback as any).providerConfigs[0]?.id || null;
  alignActiveProviderWithFlags(fallback as AppSettings);
  return fallback;
}
