import React from 'react';
import { toast } from 'sonner';
import type {
  AppSettings,
  ArxivSettings,
  AtlassianSettings,
  BrowserControlSettings,
  ConnectionCatalogId,
  DataSourceState,
  GitHubSettings,
  ImageGenerationDefaults,
  ImageGenerationSettings,
  LocalComputerAccessMode,
  LocalComputerSettings,
  ProviderCodeExecutionConfig,
  ProviderCodeExecutionSkill,
  ProviderConfig,
  SnowflakeAuthMode,
  SharePointSettings,
  ThreadSearchSettings,
  TrinoAuthMode,
} from '@/types';
import {
  getBigQueryAdcStatus,
  testBigQueryConnection,
  startAdcReauth,
  getAdcReauthStatus,
  cancelAdcReauth,
  isAdcReauthRequiredMessage,
  ADC_REAUTH_FALLBACK_COMMAND,
  type NormalizedAdcStatus,
  type NormalizedAdcReauth,
} from '@/services/bigqueryApi';
import {
  getDatabricksStatus,
  testDatabricksConnection,
  startDatabricksOAuth,
  completeDatabricksOAuth,
  getDatabricksOAuthResult,
  type DatabricksStatus,
} from '@/services/databricksApi';
import {
  getSecretsStatus,
  setSnowflakeKeyPair,
  setSnowflakeOAuthToken,
  clearSnowflakeSecrets,
} from '@/services/secretsApi';
import {
  getTrinoStatus,
  testTrinoConnection,
  type TrinoStatus,
} from '@/services/trinoApi';
import {
  getSnowflakeStatus,
  testSnowflakeConnection,
  type SnowflakeStatus,
} from '@/services/snowflakeApi';
import { openExternalUrl } from '@/services/openExternal';
import { getArxivStatus, type ArxivStatus } from '@/services/arxivApi';
import { verifyConnectionCredentials, type ConnectionCheckOutcome } from '@/services/connectionHealthApi';
import { getCredentialConnections } from '@/lib/connectionHealth';
import { isPermissionsUnavailable } from '@/services/permissionsApi';
import {
  dedupeLocalFolders,
  defaultLocalComputer,
  getLocalComputer,
  isLocalFilesystemRoot,
  listLocalComputerGrantRules,
  localComputerWholeRoots,
  normalizeLocalComputerMode,
  normalizeLocalPathKey,
  deleteLocalComputerGrantsForRoots as deleteLocalComputerGrantRulesForRoots,
  syncLocalComputerGrants as syncLocalComputerGrantRules,
} from '@/lib/localComputerGrants';
import {
  isLocalPickerCanceled,
  pickLocalDirectory,
} from '@/services/localFilePicker';
import { displayPath } from '@/lib/approvalPresentation';
import type { PermissionRuleUI } from '@/types/permissions';
import {
  configFingerprint,
  connectionConfigDirty,
  disableHostedToolConnectionAcrossProviders,
  fingerprintMatchesConfig,
  mergeConnectionPatch,
} from '@/lib/connectionConfig';
import { supportsProviderHostedTools } from '@/lib/capability/providerConfig';
import { applyProviderConfigToLegacySettings } from '@/lib/providerConfigRuntime';
import { canonicalProviderWebSearch } from '@/lib/providerWebSearch';
import { withConnectionEnabled } from '@/lib/connectionEnablement';
import {
  bytesToGibInput,
  effectiveAutomaticScanBytes,
  formatScanBytes,
  normalizeScanBudget,
  parseGibInput,
} from '@/lib/scanBudget';
import ArxivPapersPanel from './arxiv/ArxivPapersPanel';
import './connectionsConsole.css';

interface McpIntegrationsTabProps {
  localSettings: AppSettings;
  setLocalSettings: React.Dispatch<React.SetStateAction<AppSettings>>;
  savedSettings: AppSettings;
  onSave: (settings: AppSettings) => void | Promise<void>;
  /**
   * Serialized settings mutation (see useSettings.applySettingsMutation).
   * When provided, scoped connection persists rebase their slice onto the
   * LATEST saved settings at write time instead of a render-time snapshot, so
   * rapid toggles here and in the chat drawer compose instead of clobbering.
   */
  onApplySettingsMutation?: (mutator: (latest: AppSettings) => AppSettings) => Promise<AppSettings>;
  /** Attach a file to the chat composer through the standard upload path. */
  onAttachFile?: (file: File) => void | Promise<void>;
  /** Append text to the chat composer draft (used for paper excerpts). */
  onInsertDraftText?: (text: string) => void;
  /** Close the surrounding settings dialog (after sending content to chat). */
  onCloseDialog?: () => void;
  /**
   * Connection whose setup window should open as soon as the console mounts -
   * the deep-link target for reconnect toasts and inline chat re-auth actions.
   */
  focusConnectionId?: ConnectionCatalogId | null;
}

type Stage = 'setup' | 'connect' | 'enable' | 'active' | 'reconnect';

type AtlassianStatus = {
  running?: boolean;
  mode?: string;
  browser?: Record<string, any>;
  products?: Record<string, any>;
  autoLogin?: Record<string, any>;
  message?: string;
  error?: string;
};

type SharePointStatus = {
  running?: boolean;
  mode?: string;
  browser?: Record<string, any>;
  site?: Record<string, any>;
  autoLogin?: Record<string, any>;
  message?: string;
  error?: string;
};

type GitHubProfile = {
  github_login?: string;
  github_error?: string;
};

type GitHubAuthStatus = {
  configured?: boolean;
  client_id_configured?: boolean;
  connected?: boolean;
  login?: string;
  scope?: string;
  profile?: GitHubProfile;
  secrets_locked?: boolean;
  message?: string;
  error?: string;
  lastCheckedAt?: number;
  lastCheckOk?: boolean;
  lastCheckMessage?: string;
};

type GitHubDeviceFlow = {
  state: string;
  user_code: string;
  verification_uri: string;
  verification_uri_complete?: string;
  expires_in?: number;
  interval?: number;
};

type GitHubPrereqStep = {
  key: string;
  title: string;
  instruction: string;
  command?: string;
};

type GitHubPrerequisites = {
  git?: { installed?: boolean; version?: string };
  gh?: { installed?: boolean; version?: string };
  gh_authenticated?: boolean;
  gh_login?: string;
  client_id_configured?: boolean;
  ready?: boolean;
  missing?: GitHubPrereqStep[];
};

type SlackSettings = {
  enabled?: boolean;
  readTools?: boolean;
  writeTools?: boolean;
  writeIdentity?: 'user' | 'bot';
  allowedTeamIds?: string[];
  allowedChannelIds?: string[];
  lastCheckedAt?: number;
  lastCheckOk?: boolean;
  lastCheckMessage?: string | null;
  lastVerifiedFingerprint?: string | null;
};

/** How the setup window was opened; drives its eyebrow, footer and aria-label. */
type SheetIntent = 'setup' | 'edit' | 'reconnect';

type SlackSettingsHost = AppSettings & {
  slack?: SlackSettings;
  slackOAuthClientId?: string;
};

type SlackAuthStatus = {
  configured?: boolean;
  client_id_configured?: boolean;
  client_secret_set?: boolean;
  pkce_redirect_uri?: string;
  connected?: boolean;
  user_connected?: boolean;
  bot_connected?: boolean;
  write_identity?: 'user' | 'bot';
  team_id?: string;
  team_name?: string;
  authed_user_id?: string;
  bot_user_id?: string;
  user_id?: string;
  message?: string;
  error?: string;
  team?: Record<string, any>;
  workspace?: Record<string, any>;
  [key: string]: any;
};

type SlackOAuthStart = {
  authorize_url?: string;
  state?: string;
  redirect_uri?: string;
  expires_in_seconds?: number;
};

const defaultAtlassian = (): AtlassianSettings => ({
  jiraBaseUrl: '',
  confluenceBaseUrl: '',
  defaultJiraProjectKey: '',
  allowedProjects: [],
  allowedAuthDomains: ['id.atlassian.com', 'auth.atlassian.com', 'login.microsoftonline.com'],
  browserProvider: 'bundled',
  browserChannel: '',
  browserExecutablePath: '',
  browserProfileDir: '',
  jira: { enabled: false, showBrowser: false },
  confluence: { enabled: false, showBrowser: false },
});

const getAtlassian = (settings: AppSettings): AtlassianSettings => ({
  ...defaultAtlassian(),
  ...(settings.atlassian || {}),
  browserProvider: 'bundled',
  browserChannel: '',
  browserExecutablePath: '',
  jira: { ...defaultAtlassian().jira, ...(settings.atlassian?.jira || {}) },
  confluence: { ...defaultAtlassian().confluence, ...(settings.atlassian?.confluence || {}) },
});

const defaultBrowserControl = (): BrowserControlSettings => ({
  enabled: false,
  showBrowser: false,
  browserProfileDir: '',
});

const getBrowserControl = (settings: AppSettings): BrowserControlSettings => ({
  ...defaultBrowserControl(),
  ...(settings.browserControl || {}),
});

const defaultSharePoint = (): SharePointSettings => ({
  siteUrl: '',
  enabled: false,
  showBrowser: false,
  browserProfileDir: '',
  authMode: 'browser',
});

const getSharePoint = (settings: AppSettings): SharePointSettings => ({
  ...defaultSharePoint(),
  ...(settings.sharepoint || {}),
  authMode: settings.sharepoint?.authMode === 'graph' ? 'graph' : 'browser',
});

const defaultThreadSearch = (): ThreadSearchSettings => ({
  enabled: true,
});

const getThreadSearch = (settings: AppSettings): ThreadSearchSettings => ({
  ...defaultThreadSearch(),
  ...(settings.threadSearch || {}),
});

const defaultGithub = (): GitHubSettings => ({
  enabled: false,
  readTools: false,
  allowedRepositories: [],
});

const getGithub = (settings: AppSettings): GitHubSettings => ({
  ...defaultGithub(),
  ...(settings.github || {}),
  allowedRepositories: Array.isArray(settings.github?.allowedRepositories)
    ? settings.github.allowedRepositories
    : [],
});

const defaultSlack = (): SlackSettings => ({
  enabled: false,
  readTools: false,
  writeTools: false,
  writeIdentity: 'user',
  allowedTeamIds: [],
  allowedChannelIds: [],
});

const getSlack = (settings: AppSettings): SlackSettings => {
  const slack = (settings as SlackSettingsHost).slack || {};
  return {
    ...defaultSlack(),
    ...slack,
    writeIdentity: slack.writeIdentity === 'bot' ? 'bot' : 'user',
    allowedTeamIds: Array.isArray(slack.allowedTeamIds) ? slack.allowedTeamIds : [],
    allowedChannelIds: Array.isArray(slack.allowedChannelIds) ? slack.allowedChannelIds : [],
  };
};

const getSlackOAuthClientId = (settings: AppSettings): string => (
  typeof (settings as SlackSettingsHost).slackOAuthClientId === 'string'
    ? (settings as SlackSettingsHost).slackOAuthClientId || ''
    : ''
);

const defaultImageGeneration = (): ImageGenerationSettings => ({
  enabled: false,
  providerId: null,
  model: 'gpt-image-2',
  defaults: { quality: 'auto', size: 'auto', format: 'png' },
  maxImagesPerRun: 1,
});

const getImageGeneration = (settings: AppSettings): ImageGenerationSettings => ({
  ...defaultImageGeneration(),
  ...(settings.imageGeneration || {}),
  defaults: {
    ...defaultImageGeneration().defaults,
    ...(settings.imageGeneration?.defaults || {}),
  },
});

const supportsImageGeneration = (config: ProviderConfig): boolean => (
  config.providerId === 'openai' || config.providerId === 'compatible'
);

const providerKindLabel = (config: ProviderConfig): string => {
  if (config.providerId === 'openai') return 'OpenAI';
  if (config.providerId === 'anthropic') return 'Anthropic';
  return 'Responses-compatible';
};

const defaultCodeExecution = (config: ProviderConfig): ProviderCodeExecutionConfig => ({
  enabled: false,
  toolType: config.providerId === 'anthropic' ? 'code_execution_20260120' : 'code_interpreter',
  containerMode: config.providerId === 'anthropic' ? null : 'auto',
  containerId: null,
  memoryLimit: null,
  skills: null,
});

const listText = (items?: string[] | null): string => (items || []).join(', ');

function parsePythonSkills(raw: string): ProviderCodeExecutionSkill[] | null {
  const text = raw.trim();
  if (!text) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    throw new Error('Skills must be valid JSON.');
  }
  if (!Array.isArray(parsed)) throw new Error('Skills must be a JSON array.');
  const skills = parsed.map((item) => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) {
      throw new Error('Each skill needs a type and skillId.');
    }
    const value = item as Record<string, unknown>;
    const type = String(value.type || '').trim();
    const skillId = String(value.skillId || '').trim();
    const version = String(value.version || '').trim();
    if (!type || !skillId) throw new Error('Each skill needs a type and skillId.');
    return { type, skillId, ...(version ? { version } : {}) };
  });
  return skills.length > 0 ? skills : null;
}

type HostedConnectionKind = 'web-search' | 'python-sandbox';

const hostedControlKey = (kind: HostedConnectionKind): 'hosted_web_search' | 'hosted_code_execution' => (
  kind === 'web-search' ? 'hosted_web_search' : 'hosted_code_execution'
);

const hostedConfigKey = (kind: HostedConnectionKind): 'webSearch' | 'codeExecution' => (
  kind === 'web-search' ? 'webSearch' : 'codeExecution'
);

const hostedConnectionEnabled = (config: ProviderConfig, kind: HostedConnectionKind): boolean => {
  const control = config.capabilityControls?.[hostedControlKey(kind)];
  if (control === 'on') return true;
  if (control === 'off') return false;
  return config[hostedConfigKey(kind)]?.enabled === true;
};

const syncConnectionRuntimeMirrors = (
  id: ConnectionCatalogId,
  settings: AppSettings,
): AppSettings => (
  id === 'web-search' || id === 'python-sandbox'
    ? applyProviderConfigToLegacySettings(settings)
    : settings
);

async function readJsonSafe(res: Response): Promise<any> {
  const text = await res.text().catch(() => '');
  if (!text.trim()) return null;
  try {
    return JSON.parse(text);
  } catch {
    return { error: text };
  }
}

async function apiJson(url: string, init?: RequestInit): Promise<any> {
  const res = await fetch(url, {
    ...init,
    headers: {
      Accept: 'application/json',
      ...(init?.headers || {}),
    },
  });
  const data = await readJsonSafe(res);
  if (!res.ok) {
    throw new Error(data?.error || data?.message || `HTTP ${res.status}`);
  }
  return data;
}

function splitList(value: string): string[] {
  return value.split(',').map((item) => item.trim()).filter(Boolean);
}

function normalizeGitHubRepositoryList(value: string): string[] {
  const seen = new Set<string>();
  const repositories: string[] = [];
  value
    .split(/[\n,]+/)
    .map((item) => item
      .trim()
      .replace(/^ssh:\/\/git@github\.com\//i, '')
      .replace(/^https?:\/\/github\.com\//i, '')
      .replace(/^git@github\.com:/i, '')
      .replace(/\.git$/i, ''))
    .filter(Boolean)
    .forEach((item) => {
      const parts = item.split('/').filter(Boolean);
      if (parts.length < 2) return;
      const repo = `${parts[0]}/${parts[1]}`;
      const key = repo.toLowerCase();
      if (seen.has(key)) return;
      seen.add(key);
      repositories.push(repo);
    });
  return repositories;
}

function splitGitHubRepositoryInput(value: string): string[] {
  return value.split(/[\n,]+/).map((item) => item.trim()).filter(Boolean);
}

function splitSlackIdInput(value: string): string[] {
  return value.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean);
}

function normalizeSlackIdList(value: string): string[] {
  const seen = new Set<string>();
  const ids: string[] = [];
  splitSlackIdInput(value).forEach((item) => {
    const key = item.toLowerCase();
    if (seen.has(key)) return;
    seen.add(key);
    ids.push(item);
  });
  return ids;
}

function unwrapGitHubStatus(data: any): GitHubAuthStatus {
  return (data?.status || data || {}) as GitHubAuthStatus;
}

function unwrapSlackStatus(data: any): SlackAuthStatus {
  let raw = data;
  if (data?.status && typeof data.status === 'object') {
    raw = data.status;
  } else if (data?.slack_status && typeof data.slack_status === 'object') {
    raw = data.slack_status;
  } else if (data?.slack && typeof data.slack === 'object') {
    raw = data.slack;
  }
  if (!raw || typeof raw !== 'object') return {};
  return raw as SlackAuthStatus;
}

function normalizeAtlassianUrl(value: string, confluence = false): string {
  const raw = value.trim().replace(/\/+$/, '');
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
}

function normalizeSharePointUrl(value: string): string {
  const raw = value.trim().replace(/\/+$/, '');
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
}

function derivedConfluenceUrl(jiraBaseUrl: string): string {
  return jiraBaseUrl ? normalizeAtlassianUrl(jiraBaseUrl, true) : '';
}

function shouldUpdateDerivedConfluenceUrl(currentConfluenceBaseUrl: string | undefined, previousJiraBaseUrl: string | undefined): boolean {
  if (!currentConfluenceBaseUrl) return true;
  return normalizeAtlassianUrl(currentConfluenceBaseUrl, true) === derivedConfluenceUrl(previousJiraBaseUrl || '');
}

function productIsConnected(product: any): boolean {
  return String(product?.state || '').toLowerCase() === 'connected' || product?.connected === true;
}

/**
 * Fold strict check outcomes into the local settings copy so rows update
 * immediately. Callers persist the touched connection slices afterward so the
 * verified config fingerprints survive dialog close and app reload.
 */
function applyCheckOutcomes(prev: AppSettings, outcomes: ConnectionCheckOutcome[]): AppSettings {
  let next = prev;
  const now = Date.now();
  for (const outcome of outcomes) {
    if (outcome.indeterminate) continue;
    // A successful strict check proves the *current* config, so record its
    // fingerprint; a failure invalidates whatever fingerprint was stored.
    const fingerprint = outcome.ok ? configFingerprint(next, outcome.id) : null;
    const health = {
      lastCheckedAt: now,
      lastCheckOk: outcome.ok,
      lastCheckMessage: outcome.ok ? null : (outcome.message || null),
      lastVerifiedFingerprint: fingerprint,
    };
    switch (outcome.id) {
      case 'github':
        next = { ...next, github: { ...next.github, ...health } };
        break;
      case 'slack':
        next = { ...next, slack: { ...next.slack, ...health } };
        break;
      case 'sharepoint':
        next = { ...next, sharepoint: { ...next.sharepoint, ...health } };
        break;
      case 'atlassian': {
        const products = outcome.detail?.products || {};
        const at = { ...(next.atlassian || {}), lastVerifiedFingerprint: fingerprint };
        if (products.jira || products.confluence) {
          if (products.jira) {
            at.jira = { ...(at.jira || {}), lastCheckedAt: now, lastCheckOk: productIsConnected(products.jira), lastCheckMessage: health.lastCheckMessage };
          }
          if (products.confluence) {
            at.confluence = { ...(at.confluence || {}), lastCheckedAt: now, lastCheckOk: productIsConnected(products.confluence), lastCheckMessage: health.lastCheckMessage };
          }
        } else {
          at.jira = { ...(at.jira || {}), lastCheckedAt: now, lastCheckOk: outcome.ok, lastCheckMessage: health.lastCheckMessage };
          at.confluence = { ...(at.confluence || {}), lastCheckedAt: now, lastCheckOk: outcome.ok, lastCheckMessage: health.lastCheckMessage };
        }
        next = { ...next, atlassian: at };
        break;
      }
      case 'bigquery':
      case 'databricks':
      case 'trino':
      case 'snowflake':
        next = {
          ...next,
          dataSources: {
            ...next.dataSources,
            [outcome.id]: { ...next.dataSources?.[outcome.id], ...health },
          },
        };
        break;
      default:
        break;
    }
  }
  return next;
}

function compactText(...values: any[]): string {
  for (const value of values) {
    const text = String(value || '').trim();
    if (text) return text;
  }
  return '';
}

function hostLabel(rawUrl?: string | null): string {
  const raw = String(rawUrl || '').trim();
  if (!raw) return '';
  try {
    return new URL(raw.includes('://') ? raw : `https://${raw}`).hostname;
  } catch {
    return raw.replace(/^https?:\/\//, '').replace(/\/.*$/, '');
  }
}

function relativeTime(ts?: number | null): string {
  if (!ts) return '';
  const diff = Date.now() - ts;
  if (diff < 45_000) return 'just now';
  const minutes = Math.round(diff / 60_000);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  return `${days}d ago`;
}

function isSlackOAuthComplete(data: any): boolean {
  const raw = unwrapSlackStatus(data);
  const marker = String(data?.state || data?.status || raw.state || raw.status || '').toLowerCase();
  return data?.ok === true || data?.complete === true || marker === 'success' || marker === 'complete' || marker === 'connected' || raw.connected === true;
}

function slackOAuthError(data: any): string | null {
  const raw = unwrapSlackStatus(data);
  const marker = String(data?.state || data?.status || raw.state || raw.status || '').toLowerCase();
  if (typeof data?.error === 'string' && data.error) return data.error;
  if (typeof raw.error === 'string' && raw.error) return raw.error;
  if (marker === 'error' || marker === 'failed' || marker === 'expired') {
    return raw.message || data?.message || 'Slack authorization failed.';
  }
  return null;
}

function isGcpDatabricksHost(host: string): boolean {
  if (!host.trim()) return true;
  try {
    const u = new URL(host.startsWith('http') ? host : `https://${host}`);
    return u.hostname.endsWith('.gcp.databricks.com');
  } catch {
    return false;
  }
}

function extractAuthorizationCode(inputRaw: string, expectedState: string): string {
  const input = inputRaw.trim();
  if (!input) throw new Error('Paste the redirect URL or authorization code.');
  const validateState = (stateValue: string | null) => {
    const state = (stateValue || '').trim();
    if (!state || !expectedState) return;
    if (state !== expectedState) {
      throw new Error('OAuth state mismatch. Start a new authorization attempt and retry.');
    }
  };
  const parseParams = (params: URLSearchParams): string => {
    validateState(params.get('state'));
    const code = (params.get('code') || '').trim();
    if (code) return code;
    const err = (params.get('error_description') || params.get('error') || '').trim();
    if (err) throw new Error(`Databricks authorization returned an error: ${err}`);
    throw new Error('No authorization code found. Paste the full redirect URL or just the code value.');
  };
  if (/^https?:\/\//i.test(input)) {
    let parsed: URL;
    try {
      parsed = new URL(input);
    } catch {
      throw new Error('Invalid redirect URL. Paste the full URL from the browser address bar.');
    }
    return parseParams(parsed.searchParams);
  }
  if (input.includes('code=') || input.includes('state=')) {
    const query = input.includes('?') ? input.slice(input.indexOf('?') + 1) : input;
    return parseParams(new URLSearchParams(query));
  }
  return input;
}

// --- Brand logos (real full-colour marks served from public/brand-logos) -----

const LOGOS: Record<ConnectionCatalogId, string> = {
  'thread-search': '/brand-logos/search-threads.svg',
  'image-generation': '/brand-logos/image-generation.svg',
  'web-search': '/brand-logos/web-search.svg',
  'python-sandbox': '/brand-logos/python-sandbox.svg',
  'local-computer': '/brand-logos/local-computer.svg',
  browser: '/brand-logos/chrome.svg',
  github: '/brand-logos/github.svg',
  slack: '/brand-logos/slack.svg',
  atlassian: '/brand-logos/atlassian.svg',
  sharepoint: '/brand-logos/sharepoint.svg',
  bigquery: '/brand-logos/bigquery.svg',
  databricks: '/brand-logos/databricks.svg',
  trino: '/brand-logos/trino.svg',
  snowflake: '/brand-logos/snowflake.svg',
  arxiv: '/brand-logos/arxiv.svg',
};

// Marks that are inherently monochrome/dark and need to render light on the dark tile.
const MONO_LOGOS = new Set<ConnectionCatalogId>(['github']);

// --- Utility icons ----------------------------------------------------------

// All inline icons are decorative; the adjacent text or aria-label carries meaning.
const iconA11y = { 'aria-hidden': true, focusable: 'false' } as const;

const IconPlus = () => (
  <svg {...iconA11y} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round">
    <path d="M12 5v14M5 12h14" />
  </svg>
);
const IconArrow = () => (
  <svg {...iconA11y} className="cx-arr" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2.2} strokeLinecap="round" strokeLinejoin="round">
    <path d="M5 12h14M13 6l6 6-6 6" />
  </svg>
);
const IconCheck = () => (
  <svg {...iconA11y} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2.2} strokeLinecap="round" strokeLinejoin="round">
    <path d="M5 12l5 5L20 7" />
  </svg>
);
const IconChevron = () => (
  <svg {...iconA11y} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round">
    <path d="M6 9l6 6 6-6" />
  </svg>
);
const IconGear = () => (
  <svg {...iconA11y} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.8}>
    <circle cx="12" cy="12" r="3" />
    <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-2.7.62 1.65 1.65 0 0 0-1.18 1.51V22a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 20.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4 15.4a1.65 1.65 0 0 0-1.51-1.18H2a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 3.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.6a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09A1.65 1.65 0 0 0 15 4.6a1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9c.2.42.65.69 1.11.69H22a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1.31z" />
  </svg>
);
const IconSignOut = () => (
  <svg {...iconA11y} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.8} strokeLinecap="round" strokeLinejoin="round">
    <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4M16 17l5-5-5-5M21 12H9" />
  </svg>
);
const IconExternal = () => (
  <svg {...iconA11y} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.8} strokeLinecap="round" strokeLinejoin="round">
    <path d="M14 3h7v7M21 3l-9 9M19 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h5" />
  </svg>
);
const IconClose = () => (
  <svg {...iconA11y} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round">
    <path d="M6 6l12 12M18 6L6 18" />
  </svg>
);
const IconInfo = () => (
  <svg {...iconA11y} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.8} strokeLinecap="round" strokeLinejoin="round">
    <circle cx="12" cy="12" r="9" />
    <path d="M12 16v-4M12 8h.01" />
  </svg>
);
const IconRefresh = () => (
  <svg {...iconA11y} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round">
    <path d="M21 12a9 9 0 1 1-2.64-6.36M21 3v6h-6" />
  </svg>
);

const Tile = ({ id }: { id: ConnectionCatalogId }) => (
  <div className={`cx-ic${MONO_LOGOS.has(id) ? ' cx-mono' : ''}`} aria-hidden="true">
    <img className="cx-logo" src={LOGOS[id]} alt="" draggable={false} />
  </div>
);

const Toggle = ({ checked, onChange, label, disabled, inputId }: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: string;
  disabled?: boolean;
  /** Lets an external visible label target this switch via htmlFor. */
  inputId?: string;
}) => (
  <label className="cx-switch">
    <input
      id={inputId}
      type="checkbox"
      checked={checked}
      disabled={disabled}
      aria-label={label}
      onChange={(e) => onChange(e.target.checked)}
    />
    <span className="cx-track" />
  </label>
);

// --- Console field primitives -----------------------------------------------

// Wires the Field label/help to whatever CxInput renders inside it, without
// changing any call sites: Field generates the ids, CxInput picks them up.
const FieldContext = React.createContext<{ inputId: string; helpId?: string } | null>(null);

const Field = ({ label, children, help }: { label: React.ReactNode; children: React.ReactNode; help?: React.ReactNode }) => {
  const inputId = React.useId();
  const helpId = React.useId();
  return (
    <div>
      <label className="cx-field-label" htmlFor={inputId}>{label}</label>
      <FieldContext.Provider value={{ inputId, helpId: help ? helpId : undefined }}>
        {children}
      </FieldContext.Provider>
      {help ? <p className="cx-help" id={helpId}>{help}</p> : null}
    </div>
  );
};

const ReqLabel = ({ children, optional }: { children: React.ReactNode; optional?: boolean }) => (
  <>
    {children}
    {optional
      ? <span className="cx-field-opt"> · optional</span>
      : <><span className="cx-req" aria-hidden="true">*</span><span className="cx-sr-only"> (required)</span></>}
  </>
);

const CxInput = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement> & { mono?: boolean }>(
  ({ mono, className, id, 'aria-describedby': ariaDescribedBy, ...rest }, ref) => {
    const field = React.useContext(FieldContext);
    return (
      <input
        ref={ref}
        id={id ?? field?.inputId}
        aria-describedby={ariaDescribedBy ?? field?.helpId}
        className={`cx-input${mono ? ' cx-mono' : ''}${className ? ` ${className}` : ''}`}
        {...rest}
      />
    );
  },
);
CxInput.displayName = 'CxInput';

const CxSelect = React.forwardRef<HTMLSelectElement, React.SelectHTMLAttributes<HTMLSelectElement>>(
  ({ className, id, 'aria-describedby': ariaDescribedBy, ...rest }, ref) => {
    const field = React.useContext(FieldContext);
    return (
      <select
        ref={ref}
        id={id ?? field?.inputId}
        aria-describedby={ariaDescribedBy ?? field?.helpId}
        className={`cx-input${className ? ` ${className}` : ''}`}
        {...rest}
      />
    );
  },
);
CxSelect.displayName = 'CxSelect';

const CxTextarea = React.forwardRef<HTMLTextAreaElement, React.TextareaHTMLAttributes<HTMLTextAreaElement>>(
  ({ className, id, 'aria-describedby': ariaDescribedBy, ...rest }, ref) => {
    const field = React.useContext(FieldContext);
    return (
      <textarea
        ref={ref}
        id={id ?? field?.inputId}
        aria-describedby={ariaDescribedBy ?? field?.helpId}
        className={`cx-input cx-textarea${className ? ` ${className}` : ''}`}
        {...rest}
      />
    );
  },
);
CxTextarea.displayName = 'CxTextarea';

function fieldNameSlug(label: string): string {
  return label.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '');
}

const SecretField = ({ label, placeholder, isSet, busy, onSet, onClear, help }: {
  label: string;
  placeholder?: string;
  isSet: boolean;
  busy?: boolean;
  onSet: (value: string) => void | Promise<void>;
  onClear: () => void | Promise<void>;
  help?: React.ReactNode;
}) => {
  const [value, setValue] = React.useState('');
  const [reveal, setReveal] = React.useState(false);
  return (
    <Field
      label={`${label}${isSet ? ' · stored' : ''}`}
      help={help}
    >
      <div className="cx-field-row">
        <CxInput
          type={reveal ? 'text' : 'password'}
          mono
          name={fieldNameSlug(label)}
          autoComplete="new-password"
          spellCheck={false}
          placeholder={isSet ? 'Stored in encrypted local secrets' : (placeholder || '')}
          value={value}
          onChange={(e) => setValue(e.target.value)}
        />
        <button
          type="button"
          className="cx-secret-reveal"
          aria-pressed={reveal}
          aria-label={`${reveal ? 'Hide' : 'Show'} ${label}`}
          disabled={!value}
          onClick={() => setReveal((r) => !r)}
        >
          {reveal ? 'Hide' : 'Show'}
        </button>
        <button
          type="button"
          className="cx-btn-save"
          disabled={busy || !value.trim()}
          onClick={async () => { await onSet(value.trim()); setValue(''); setReveal(false); }}
        >
          {busy ? <span className="cx-spin cx-spin-ghost" /> : <IconCheck />}Save
        </button>
        <button type="button" className="cx-btn-save" disabled={busy || !isSet} onClick={() => onClear()}>
          Clear
        </button>
      </div>
    </Field>
  );
};

// --- Surface B: Advanced disclosure (collapsed by default) ------------------
// Houses optional auth configuration (e.g. a custom OAuth client ID). It never
// gates setup and is rendered only inside the setup window — never in a drawer.

const Disclosure = ({ label, children, defaultOpen }: {
  label?: string;
  children: React.ReactNode;
  defaultOpen?: boolean;
}) => {
  const [open, setOpen] = React.useState(Boolean(defaultOpen));
  const bodyId = React.useId();
  return (
    <div className="cx-disc">
      <button type="button" className="cx-disc-btn" aria-expanded={open} aria-controls={bodyId} onClick={() => setOpen((o) => !o)}>
        <IconChevron />{label || 'Advanced'}
      </button>
      {open ? <div className="cx-disc-body" id={bodyId}>{children}</div> : null}
    </div>
  );
};

// --- Surface C: per-tool enablement + scope refinement ----------------------

const ToolRow = ({ name, desc, checked, disabled, onChange, label }: {
  name: string;
  desc: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (v: boolean) => void;
  label: string;
}) => (
  <div className="cx-tool">
    <div>
      <div className="cx-tname">{name}</div>
      <div className="cx-tdesc">{desc}</div>
    </div>
    <Toggle checked={checked} disabled={disabled} label={label} onChange={onChange} />
  </div>
);

// A *narrowing* control: off means all accessible resources, on reveals the
// input that limits access. Never a blank field implying it is required. The
// open state is local so the input appears the moment the user engages it,
// even before any value exists; turning it off clears the underlying list.
const ScopeControl = ({ label, defaultOn, value, placeholder, disabled, onChange, onBlur, onClear }: {
  label: string;
  defaultOn: boolean;
  value: string;
  placeholder: string;
  disabled?: boolean;
  onChange: (v: string) => void;
  onBlur?: (v: string) => void;
  onClear: () => void;
}) => {
  const [on, setOn] = React.useState(defaultOn);
  const descId = React.useId();
  return (
    <>
      <div className="cx-tool">
        <div>
          <div className="cx-tname">{label}</div>
          <div className="cx-tdesc" id={descId}>{on ? 'On — only the resources below' : 'Off — all accessible resources'}</div>
        </div>
        <Toggle checked={on} disabled={disabled} label={label} onChange={(v) => { setOn(v); if (!v) onClear(); }} />
      </div>
      {on ? (
        <CxInput
          mono
          className="cx-scope-inp"
          name={fieldNameSlug(label)}
          aria-label={label}
          aria-describedby={descId}
          placeholder={placeholder}
          value={value}
          disabled={disabled}
          onChange={(e) => onChange(e.target.value)}
          onBlur={onBlur ? (e) => onBlur(e.target.value) : undefined}
        />
      ) : null}
    </>
  );
};

// --- Setup window (focused setup surface for credential-heavy connections) ---

const SideSheet: React.FC<{
  open: boolean;
  /** A close *request* — the parent may intercept it with an unsaved-changes guard. */
  onClose: () => void;
  id: ConnectionCatalogId;
  name: string;
  eyebrow?: string;
  /** Intent-derived accessible name, e.g. "Edit connection — Databricks". */
  ariaLabel?: string;
  /** Rendered between body and footer (unsaved-changes guard). */
  notice?: React.ReactNode;
  children: React.ReactNode;
  footer: React.ReactNode;
}> = ({ open, onClose, id, name, eyebrow, ariaLabel, notice, children, footer }) => {
  const [render, setRender] = React.useState(open);
  const [shown, setShown] = React.useState(false);
  const panelRef = React.useRef<HTMLDivElement>(null);

  React.useEffect(() => {
    if (open) {
      setRender(true);
      const r = window.requestAnimationFrame(() => setShown(true));
      return () => window.cancelAnimationFrame(r);
    }
    setShown(false);
    const t = window.setTimeout(() => setRender(false), 280);
    return () => window.clearTimeout(t);
  }, [open]);

  React.useEffect(() => {
    if (!render || !open) return undefined;
    const previouslyFocused = document.activeElement as HTMLElement | null;
    const focusTimer = window.setTimeout(() => {
      // Land on the first real input/action in the body — never the close X.
      const panel = panelRef.current;
      const target = panel?.querySelector<HTMLElement>(
        '.cx-sheet-body input:not([disabled]), .cx-sheet-body select:not([disabled]), .cx-sheet-body textarea:not([disabled]), .cx-sheet-body button:not([disabled]), .cx-sheet-body [tabindex]:not([tabindex="-1"])',
      )
        || panel?.querySelector<HTMLElement>('.cx-sheet-foot button:not([disabled])')
        || panel?.querySelector<HTMLElement>('input:not([disabled]), button:not([disabled]), [tabindex]:not([tabindex="-1"])');
      target?.focus();
    }, 90);
    // Capture phase so Escape closes the setup window without bubbling out to the
    // surrounding settings dialog (which would otherwise close entirely).
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if (e.isComposing || e.keyCode === 229) return;
        e.preventDefault();
        e.stopPropagation();
        onClose();
        return;
      }
      if (e.key !== 'Tab') return;
      const panel = panelRef.current;
      if (!panel) return;
      const items = panel.querySelectorAll<HTMLElement>(
        'input:not([disabled]), button:not([disabled]), a[href], [tabindex]:not([tabindex="-1"])',
      );
      if (items.length === 0) return;
      const first = items[0];
      const last = items[items.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    window.addEventListener('keydown', onKey, true);
    return () => {
      window.clearTimeout(focusTimer);
      window.removeEventListener('keydown', onKey, true);
      // The trigger may have unmounted while the sheet was open (stage moved,
      // row re-rendered) — fall back to the console root instead of dropping
      // focus on <body>.
      if (previouslyFocused && document.contains(previouslyFocused)) {
        previouslyFocused.focus?.();
      } else {
        document.getElementById('cx-console-root')?.focus?.();
      }
    };
  }, [render, open, onClose]);

  if (!render) return null;
  return (
    <div
      className={`cx-sheet-backdrop${shown ? ' cx-shown' : ''}`}
      onMouseDown={(e) => { if (e.target === e.currentTarget) onClose(); }}
    >
      <div
        ref={panelRef}
        className={`cx-sheet${shown ? ' cx-shown' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-label={ariaLabel || `${eyebrow || 'Set up connection'} — ${name}`}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="cx-sheet-head">
          <Tile id={id} />
          <div className="cx-sheet-titles">
            <div className="cx-eyebrow">{eyebrow || 'Set up connection'}</div>
            <div className="cx-sheet-title">{name}</div>
          </div>
          <button type="button" className="cx-sheet-x" aria-label="Close setup window" onClick={onClose}>
            <IconClose />
          </button>
        </div>
        <div className="cx-sheet-body">{children}</div>
        {notice}
        <div className="cx-sheet-foot">{footer}</div>
      </div>
    </div>
  );
};

const SheetFooter = ({ label, onClick, disabled, busy, busyLabel, reason, reasonError }: {
  label: string;
  onClick: () => void | Promise<void>;
  disabled?: boolean;
  busy?: boolean;
  busyLabel?: string;
  reason?: string;
  reasonError?: boolean;
}) => (
  <>
    {reason ? <p className={`cx-sheet-reason${reasonError ? ' cx-sheet-reason-err' : ''}`}>{reason}</p> : null}
    <button type="button" className="cx-sheet-primary" disabled={disabled || busy} onClick={() => void onClick()}>
      {busy ? <><span className="cx-spin" />{busyLabel || 'Connecting…'}</> : <>{label}<IconArrow /></>}
    </button>
  </>
);

// Removal is destructive-ish (drops the row and disables model access), so it
// always takes a two-step inline confirm — no modal, no accidental single click.
const TwoStepRemove = ({ name, onRemove }: { name: string; onRemove: () => void }) => {
  const [confirming, setConfirming] = React.useState(false);
  React.useEffect(() => {
    if (!confirming) return undefined;
    const t = window.setTimeout(() => setConfirming(false), 8000);
    return () => window.clearTimeout(t);
  }, [confirming]);
  if (!confirming) {
    return (
      <button
        type="button"
        className="cx-link cx-link-warn"
        aria-label={`Remove ${name} from connections`}
        onClick={() => setConfirming(true)}
      >
        <IconClose />Remove from connections
      </button>
    );
  }
  return (
    <span className="cx-remove-confirm" role="alert">
      <span>Remove {name}?</span>
      <button type="button" className="cx-link cx-link-warn" onClick={onRemove}>Yes, remove</button>
      <button type="button" className="cx-link" onClick={() => setConfirming(false)}>Keep</button>
    </span>
  );
};

// Every connection sheet ends in the same danger zone: sign out (when a
// server-side credential/session exists) and a two-step remove. It renders
// regardless of health so a broken connection can always be abandoned.
const DangerZone = ({ name, signOut, onRemove }: {
  name: string;
  signOut?: { label: string; onClick: () => void; disabled?: boolean } | null;
  onRemove: () => void;
}) => (
  <div className="cx-danger">
    <div className="cx-danger-title">Danger zone</div>
    <div className="cx-danger-row">
      {signOut ? (
        <button type="button" className="cx-link cx-link-warn" disabled={signOut.disabled} onClick={signOut.onClick}>
          <IconSignOut />{signOut.label}
        </button>
      ) : null}
      <TwoStepRemove name={name} onRemove={onRemove} />
    </div>
  </div>
);

// --- Connection row ---------------------------------------------------------

interface RowProps {
  id: ConnectionCatalogId;
  name: string;
  index: number;
  stage: Stage;
  eyebrow: string;
  tone: 'default' | 'live' | 'warn';
  /** When the connection's credentials were last strictly verified. */
  checkedAt?: number;
  instruction: React.ReactNode;
  connecting?: boolean;
  connectingLabel?: string;
  primaryLabel?: string;
  primaryIcon?: 'gear' | 'arrow' | null;
  onPrimary?: () => void;
  primaryDisabled?: boolean;
  primaryOpensDrawer?: boolean;
  secondary?: { label: string; onClick: () => void; disabled?: boolean } | null;
  showToggle?: boolean;
  toggleOn?: boolean;
  onToggle?: (v: boolean) => void;
  toggleLabel?: string;
  toggleDisabled?: boolean;
  hasDetails?: boolean;
  detailsNode?: React.ReactNode;
  feedback?: React.ReactNode;
  feedbackError?: boolean;
  disconnect?: { label: string; onClick: () => void; disabled?: boolean } | null;
  /** Opens the setup window with intent 'edit' — configuration is never trapped. */
  onEdit?: (() => void) | null;
  onRemove: () => void;
  // When set, the primary action and manage chevron open a focused setup window
  // instead of expanding an inline drawer (used for credential-heavy setups).
  usesSheet?: boolean;
  onOpenSheet?: () => void;
}

const ConnectionRow: React.FC<RowProps> = ({
  id, name, index, stage, eyebrow, tone, checkedAt, instruction,
  connecting, connectingLabel, primaryLabel, primaryIcon, onPrimary, primaryDisabled, primaryOpensDrawer,
  secondary, showToggle, toggleOn, onToggle, toggleLabel, toggleDisabled,
  hasDetails, detailsNode, feedback, feedbackError, disconnect, onEdit, onRemove,
  usesSheet, onOpenSheet,
}) => {
  const [drawerOpen, setDrawerOpen] = React.useState(false);
  const drawerId = `cx-drawer-${id}`;

  // The drawer is deliberately sticky across stage changes: turning off the
  // last tool (active → enable) or a health flip must not slam it shut while
  // the user is working inside it.

  // The drawer is Surface C only — opened by the chevron. Setup and sign-in
  // always happen in the window (Surface B), never inline.
  const drawerNode = detailsNode;
  const primaryOpensSheet = Boolean(usesSheet) && (stage === 'setup' || stage === 'connect' || stage === 'reconnect');
  const primaryShouldOpenDrawer = !primaryOpensSheet && primaryOpensDrawer;

  const renderPrimary = () => {
    if (connecting) {
      return (
        <button type="button" className="cx-btn-primary" disabled>
          <span className="cx-spin" />{connectingLabel || 'Connecting…'}
        </button>
      );
    }
    if (stage === 'enable') {
      const enableInputId = `cx-enable-${id}`;
      return (
        <div className="cx-enable-wrap">
          <label className="cx-enable-lbl" htmlFor={enableInputId}>Enable</label>
          <Toggle inputId={enableInputId} checked={false} disabled={toggleDisabled} label={toggleLabel || `Enable ${name}`} onChange={(v) => onToggle?.(v)} />
        </div>
      );
    }
    if (stage === 'active') {
      return <Toggle checked={Boolean(toggleOn)} disabled={toggleDisabled} label={toggleLabel || `Disable ${name}`} onChange={(v) => onToggle?.(v)} />;
    }
    // setup / connect / reconnect
    return (
      <>
        {secondary ? (
          <button type="button" className="cx-btn-secondary" disabled={secondary.disabled} onClick={secondary.onClick}>
            {secondary.label}
          </button>
        ) : null}
        <button
          type="button"
          className="cx-btn-primary"
          disabled={primaryDisabled}
          aria-haspopup={primaryOpensSheet ? 'dialog' : undefined}
          aria-expanded={primaryShouldOpenDrawer ? drawerOpen : undefined}
          aria-controls={primaryShouldOpenDrawer ? drawerId : undefined}
          onClick={() => {
            if (primaryOpensSheet) {
              onOpenSheet?.();
              return;
            }
            if (primaryShouldOpenDrawer) {
              setDrawerOpen((o) => !o);
            }
            onPrimary?.();
          }}
        >
          {primaryIcon === 'gear' ? <IconGear /> : null}
          {primaryLabel}
          {primaryIcon === 'arrow' ? <IconArrow /> : null}
        </button>
      </>
    );
  };

  // Surface C (management drawer) is reachable from every post-setup state —
  // connect, enable, active, reconnect — and stays reachable while a
  // connection attempt is running. Users must always be able to edit, sign
  // out of, or remove a connection; only brand-new 'setup' rows (whose primary
  // action already opens the setup window with everything in it) skip it.
  const showChevron = Boolean(hasDetails && stage !== 'setup');

  return (
    <article className="cx-conn" style={{ animationDelay: `${index * 75}ms` }}>
      <div className="cx-head-row">
        <Tile id={id} />
        <div className="cx-body">
          <div className={`cx-eyebrow${tone === 'live' ? ' cx-live' : ''}${tone === 'warn' ? ' cx-warn' : ''}`}>
            {tone === 'live' ? <span className="cx-livedot" /> : null}{eyebrow}
            {checkedAt && (stage === 'active' || stage === 'enable' || stage === 'reconnect') ? (
              <span className="cx-checked">· checked {relativeTime(checkedAt)}</span>
            ) : null}
          </div>
          <div className="cx-cname">{name}</div>
          <div className="cx-instruct">{instruction}</div>
        </div>
        <div className="cx-action">
          {/* No remount key here: swapping the node on stage change used to
              drop keyboard focus mid-interaction. */}
          <div className="cx-action-primary cx-swap">
            {renderPrimary()}
          </div>
          <div className="cx-action-manage">
            {showChevron ? (
              <button
                type="button"
                className={`cx-iconbtn${drawerOpen ? ' cx-open' : ''}`}
                aria-label={`Manage ${name}`}
                aria-expanded={drawerOpen}
                aria-controls={drawerId}
                onClick={() => setDrawerOpen((o) => !o)}
              >
                <IconChevron />
              </button>
            ) : null}
          </div>
        </div>
      </div>

      {feedback ? (
        <div className={`cx-feedback${feedbackError ? ' cx-feedback-err' : ''}`} role="status">{feedback}</div>
      ) : null}

      <div
        className={`cx-drawer${drawerOpen ? ' cx-open' : ''}`}
        id={drawerId}
        role={drawerOpen ? 'region' : undefined}
        aria-label={drawerOpen ? `${name} details` : undefined}
      >
        <div>
          {drawerOpen ? (
            <div className="cx-drawer-pad">
              {drawerNode}
              <div className="cx-drawer-manage">
                {onEdit ? (
                  <button type="button" className="cx-link" onClick={onEdit}>
                    <IconGear />Edit connection
                  </button>
                ) : null}
                {disconnect ? (
                  <button type="button" className="cx-link cx-link-warn" disabled={disconnect.disabled} onClick={disconnect.onClick}>
                    <IconSignOut />{disconnect.label}
                  </button>
                ) : null}
                <TwoStepRemove name={name} onRemove={onRemove} />
              </div>
            </div>
          ) : null}
        </div>
      </div>
    </article>
  );
};

// --- Catalogue picker -------------------------------------------------------

const CATALOG: { id: ConnectionCatalogId; name: string; desc: string }[] = [
  { id: 'thread-search', name: 'Search Threads', desc: 'Search local conversation history' },
  { id: 'image-generation', name: 'Image Generation', desc: 'Create image files with a dedicated image model' },
  { id: 'web-search', name: 'Web Search', desc: 'Provider-hosted search with cited sources' },
  { id: 'python-sandbox', name: 'Python Sandbox', desc: 'Run Python in a provider-managed coding sandbox' },
  { id: 'local-computer', name: 'Local Computer', desc: 'Read-only search and file access on this device' },
  { id: 'browser', name: 'Browser', desc: 'Local Playwright browser automation' },
  { id: 'github', name: 'GitHub', desc: 'Repositories, issues and pull requests' },
  { id: 'slack', name: 'Slack', desc: 'Read and post workspace messages' },
  { id: 'atlassian', name: 'Atlassian', desc: 'Jira & Confluence' },
  { id: 'sharepoint', name: 'SharePoint', desc: 'Search and read SharePoint files' },
  { id: 'bigquery', name: 'BigQuery', desc: 'Google Cloud BigQuery datasets' },
  { id: 'databricks', name: 'Databricks', desc: 'Databricks SQL warehouse' },
  { id: 'trino', name: 'Trino', desc: 'Trino coordinator SQL queries' },
  { id: 'snowflake', name: 'Snowflake', desc: 'Snowflake data warehouse' },
  { id: 'arxiv', name: 'arXiv', desc: 'Research papers from arXiv.org' },
];

const CataloguePicker = ({ visible, onAdd, onClose }: {
  visible: Set<ConnectionCatalogId>;
  onAdd: (id: ConnectionCatalogId) => void;
  onClose: () => void;
}) => {
  const closeRef = React.useRef<HTMLButtonElement>(null);
  const panelRef = React.useRef<HTMLDivElement>(null);
  React.useEffect(() => {
    const previouslyFocused = document.activeElement as HTMLElement | null;
    closeRef.current?.focus();
    // Capture phase + stopPropagation so Escape closes only the catalogue —
    // never the surrounding Settings dialog listening on the same document.
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        if (e.isComposing || e.keyCode === 229) return;
        e.preventDefault();
        e.stopPropagation();
        onClose();
        return;
      }
      if (e.key !== 'Tab') return;
      const panel = panelRef.current;
      if (!panel) return;
      const focusable = panel.querySelectorAll<HTMLElement>(
        'button:not([disabled]), [href], input:not([disabled]), [tabindex]:not([tabindex="-1"])',
      );
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    window.addEventListener('keydown', onKey, true);
    return () => {
      window.removeEventListener('keydown', onKey, true);
      previouslyFocused?.focus?.();
    };
  }, [onClose]);

  return (
    <div className="cx-cat-backdrop" onClick={onClose}>
      <div
        ref={panelRef}
        className="cx-cat"
        role="dialog"
        aria-modal="true"
        aria-label="Add a connection"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="cx-cat-head">
          <span className="cx-cat-title">Add a connection</span>
          <button ref={closeRef} type="button" className="cx-iconbtn" aria-label="Close catalogue" onClick={onClose}>
            <IconClose />
          </button>
        </div>
        <div className="cx-cat-list">
          {CATALOG.map((c) => {
            const added = visible.has(c.id);
            return (
              <button
                key={c.id}
                type="button"
                className="cx-cat-item"
                disabled={added}
                onClick={() => { if (!added) onAdd(c.id); }}
              >
                <Tile id={c.id} />
                <div className="cx-cat-meta">
                  <div className="cx-cat-name">{c.name}</div>
                  <div className="cx-cat-desc">{c.desc}</div>
                </div>
                {added ? <span className="cx-cat-added">Added</span> : <span className="cx-cat-add" aria-hidden="true"><IconPlus /></span>}
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
};

// --- DataSource helpers ------------------------------------------------------

type DataSourceKey = 'bigquery' | 'databricks' | 'trino' | 'snowflake';

const dataSourceState = (settings: AppSettings, key: DataSourceKey): DataSourceState => {
  const raw = settings.dataSources?.[key];
  return {
    enabled: Boolean(raw?.enabled),
    lastCheckedAt: raw?.lastCheckedAt,
    lastCheckOk: raw?.lastCheckOk,
    lastCheckMessage: raw?.lastCheckMessage,
    lastVerifiedFingerprint: raw?.lastVerifiedFingerprint,
    lastProjectId: raw?.lastProjectId,
    lastWarehouseId: raw?.lastWarehouseId,
    lastHost: raw?.lastHost,
    automaticScanBytes: raw?.automaticScanBytes,
    approvedScanBytes: raw?.approvedScanBytes,
  };
};

const McpIntegrationsTab: React.FC<McpIntegrationsTabProps> = ({
  localSettings,
  setLocalSettings,
  savedSettings,
  onSave,
  onApplySettingsMutation,
  onAttachFile,
  onInsertDraftText,
  onCloseDialog,
  focusConnectionId,
}) => {
  // Atlassian
  const [status, setStatus] = React.useState<AtlassianStatus | null>(null);
  const [busy, setBusy] = React.useState<string | null>(null);
  const atlassianPollTimeoutRef = React.useRef<number | null>(null);
  const atlassianPollGenRef = React.useRef(0);
  // SharePoint
  const [sharePointStatus, setSharePointStatus] = React.useState<SharePointStatus | null>(null);
  const [sharePointBusy, setSharePointBusy] = React.useState<string | null>(null);
  const sharePointPollTimeoutRef = React.useRef<number | null>(null);
  const sharePointPollGenRef = React.useRef(0);
  // GitHub
  const [githubStatus, setGithubStatus] = React.useState<GitHubAuthStatus | null>(null);
  const [githubBusy, setGithubBusy] = React.useState<string | null>(null);
  const [githubDeviceFlow, setGithubDeviceFlow] = React.useState<GitHubDeviceFlow | null>(null);
  const [githubAuthMessage, setGithubAuthMessage] = React.useState<string | null>(null);
  const [githubPrereq, setGithubPrereq] = React.useState<GitHubPrerequisites | null>(null);
  // Slack
  const [slackStatus, setSlackStatus] = React.useState<SlackAuthStatus | null>(null);
  const [slackBusy, setSlackBusy] = React.useState<string | null>(null);
  const [slackOAuthFlow, setSlackOAuthFlow] = React.useState<SlackOAuthStart | null>(null);
  const [slackOAuthMessage, setSlackOAuthMessage] = React.useState<string | null>(null);
  // BigQuery
  const [bqStatus, setBqStatus] = React.useState<NormalizedAdcStatus | null>(null);
  const [bqBusy, setBqBusy] = React.useState(false);
  const [bqFeedback, setBqFeedback] = React.useState<{ text: string; error: boolean } | null>(null);
  const bqProjectTouchedRef = React.useRef(false);
  // BigQuery scan-budget inputs: raw text while the user types (so "1." or a
  // momentarily-too-low ceiling doesn't snap); null = show the stored value.
  const [bqScanDrafts, setBqScanDrafts] = React.useState<{ automatic: string | null; approved: string | null }>({ automatic: null, approved: null });
  // BigQuery ADC reauthentication (gcloud auth application-default login)
  const [bqReauth, setBqReauth] = React.useState<NormalizedAdcReauth | null>(null);
  const [bqReauthPhase, setBqReauthPhase] = React.useState<string | null>(null);
  const [bqFallbackCopied, setBqFallbackCopied] = React.useState(false);
  const bqReauthPollRef = React.useRef<number | null>(null);
  const bqReauthGenRef = React.useRef(0);
  // Databricks
  const [dbStatus, setDbStatus] = React.useState<DatabricksStatus | null>(null);
  const [dbBusy, setDbBusy] = React.useState(false);
  const [dbFeedback, setDbFeedback] = React.useState<{ text: string; error: boolean } | null>(null);
  const [dbRefreshSet, setDbRefreshSet] = React.useState(false);
  const [dbPatSet, setDbPatSet] = React.useState(false);
  const [dbAuthMode, setDbAuthMode] = React.useState<'refresh_token' | 'pat'>('refresh_token');
  const [pendingOAuthState, setPendingOAuthState] = React.useState('');
  const [pendingOAuthUrl, setPendingOAuthUrl] = React.useState('');
  const [manualOAuthInput, setManualOAuthInput] = React.useState('');
  const oauthCodeInFlightRef = React.useRef(false);
  const lastAutoCodeKeyRef = React.useRef('');
  // Trino
  const [trinoStatus, setTrinoStatus] = React.useState<TrinoStatus | null>(null);
  const [trinoBusy, setTrinoBusy] = React.useState(false);
  const [trinoFeedback, setTrinoFeedback] = React.useState<{ text: string; error: boolean } | null>(null);
  const [trinoAuthMode, setTrinoAuthMode] = React.useState<TrinoAuthMode>('none');
  const [trinoBasicSet, setTrinoBasicSet] = React.useState(false);
  const [trinoJwtSet, setTrinoJwtSet] = React.useState(false);
  // Snowflake
  const [snowflakeStatus, setSnowflakeStatus] = React.useState<SnowflakeStatus | null>(null);
  const [snowflakeBusy, setSnowflakeBusy] = React.useState(false);
  const [snowflakeFeedback, setSnowflakeFeedback] = React.useState<{ text: string; error: boolean } | null>(null);
  const [snowflakeAuthMode, setSnowflakeAuthMode] = React.useState<SnowflakeAuthMode>('oauth_token');
  const [snowflakeOAuthSet, setSnowflakeOAuthSet] = React.useState(false);
  const [snowflakeKeyPairSet, setSnowflakeKeyPairSet] = React.useState(false);
  const [snowflakeKeyPassphrase, setSnowflakeKeyPassphrase] = React.useState('');

  const [catalogueOpen, setCatalogueOpen] = React.useState(false);
  const [verifyAllBusy, setVerifyAllBusy] = React.useState(false);
  // Notice shown when a deep-link targets a connection not in the catalogue.
  const [focusNotice, setFocusNotice] = React.useState<string | null>(null);
  // Local Computer: read-only filesystem grants managed by this connection.
  const [localComputerBusy, setLocalComputerBusy] = React.useState<string | null>(null);
  const [localComputerFeedback, setLocalComputerFeedback] = React.useState<{ text: string; error?: boolean } | null>(null);
  const [pythonProviderTargetId, setPythonProviderTargetId] = React.useState('');
  const [pythonSkillsDraft, setPythonSkillsDraft] = React.useState('');
  // Skills JSON is intentionally kept as raw text until Save so invalid JSON
  // can remain editable. Track that raw draft separately from AppSettings so
  // close/discard guards still see changes that cannot yet be parsed.
  const pythonSkillsDraftRef = React.useRef('');
  const pythonSkillsDraftBaselineRef = React.useRef('');
  const [hostedToolFeedback, setHostedToolFeedback] = React.useState<{ kind: HostedConnectionKind; text: string } | null>(null);

  // --- Setup window (sheet) state --------------------------------------------
  // The sheet carries an explicit intent so its eyebrow, footer and aria-label
  // say what the user is actually doing: first-time setup, editing existing
  // configuration, or re-authenticating broken credentials.
  const [sheet, setSheet] = React.useState<{ id: ConnectionCatalogId; intent: SheetIntent } | null>(null);
  const [sheetGuard, setSheetGuard] = React.useState(false);
  const sheetOpenId = sheet?.id ?? null;
  const sheetRef = React.useRef(sheet);
  sheetRef.current = sheet;
  // Connection slice at sheet-open (refreshed on every scoped persist): the
  // restore point for "Discard changes" and the baseline for dirty checks.
  const sheetSnapshotRef = React.useRef<AppSettings | null>(null);
  // Fresh settings for async callbacks (OAuth polls, timers) without dep churn.
  const localSettingsRef = React.useRef(localSettings);
  localSettingsRef.current = localSettings;
  const savedSettingsRef = React.useRef(savedSettings);
  savedSettingsRef.current = savedSettings;

  const openSheet = React.useCallback((id: ConnectionCatalogId, intent: SheetIntent) => {
    sheetSnapshotRef.current = localSettingsRef.current;
    if (id === 'python-sandbox') pythonSkillsDraftBaselineRef.current = pythonSkillsDraftRef.current;
    setSheetGuard(false);
    setSheet({ id, intent });
  }, []);

  const closeSheetNow = React.useCallback(() => {
    setSheetGuard(false);
    sheetSnapshotRef.current = null;
    pythonSkillsDraftBaselineRef.current = pythonSkillsDraftRef.current;
    setSheet(null);
  }, []);

  /** Close after a successful connect/test — only if that connection's sheet is open. */
  const closeSheetFor = React.useCallback((id: ConnectionCatalogId) => {
    if (sheetRef.current?.id !== id) return;
    closeSheetNow();
  }, [closeSheetNow]);

  // X / Escape / backdrop route here: a dirty sheet is never silently
  // committed or silently dropped — the guard asks the user first.
  const requestCloseSheet = React.useCallback(() => {
    const current = sheetRef.current;
    if (!current) return;
    const snapshot = sheetSnapshotRef.current;
    const hasRawPythonSkillsDraft = current.id === 'python-sandbox'
      && pythonSkillsDraftRef.current !== pythonSkillsDraftBaselineRef.current;
    if (hasRawPythonSkillsDraft || (snapshot && connectionConfigDirty(snapshot, localSettingsRef.current, current.id))) {
      setSheetGuard(true);
      return;
    }
    closeSheetNow();
  }, [closeSheetNow]);

  /** Discard restores only this connection's slice — other tabs' drafts stay. */
  const discardSheetChanges = React.useCallback(() => {
    const current = sheetRef.current;
    const snapshot = sheetSnapshotRef.current;
    if (current && snapshot) {
      setLocalSettings((prev) => mergeConnectionPatch(prev, current.id, snapshot));
      if (current.id === 'python-sandbox') {
        const target = (snapshot.providerConfigs || []).find((config) => config.id === pythonProviderTargetId);
        const skills = target?.codeExecution?.skills || [];
        const restoredDraft = skills.length > 0 ? JSON.stringify(skills, null, 2) : '';
        pythonSkillsDraftRef.current = restoredDraft;
        pythonSkillsDraftBaselineRef.current = restoredDraft;
        setPythonSkillsDraft(restoredDraft);
      }
    }
    closeSheetNow();
  }, [closeSheetNow, pythonProviderTargetId, setLocalSettings]);

  const githubPollTimeoutRef = React.useRef<number | null>(null);
  const slackPollTimeoutRef = React.useRef<number | null>(null);

  const atlassian = getAtlassian(localSettings);
  const threadSearch = getThreadSearch(localSettings);
  const localComputer = getLocalComputer(localSettings);
  const browserControl = getBrowserControl(localSettings);
  const sharepoint = getSharePoint(localSettings);
  const github = getGithub(localSettings);
  const slack = getSlack(localSettings);
  const slackOAuthClientId = getSlackOAuthClientId(localSettings);
  const imageGeneration = getImageGeneration(localSettings);
  const providerConfigs = localSettings.providerConfigs || [];
  const imageProviderConfigs = providerConfigs.filter(supportsImageGeneration);
  const hostedProviderConfigs = providerConfigs.filter(supportsProviderHostedTools);
  const imageProviderTarget = imageProviderConfigs.find((config) => config.id === imageGeneration.providerId) || null;
  const pythonProviderTarget = hostedProviderConfigs.find((config) => config.id === pythonProviderTargetId)
    || hostedProviderConfigs.find((config) => hostedConnectionEnabled(config, 'python-sandbox'))
    || hostedProviderConfigs.find((config) => config.id === localSettings.activeProviderConfigId)
    || hostedProviderConfigs[0]
    || null;
  const visibleIds: ConnectionCatalogId[] = Array.isArray(localSettings.connections?.visibleIds)
    ? (localSettings.connections!.visibleIds as ConnectionCatalogId[])
    : [];
  const visibleSet = new Set<ConnectionCatalogId>(visibleIds);

  React.useEffect(() => {
    if (!pythonProviderTarget) return;
    if (pythonProviderTargetId !== pythonProviderTarget.id) {
      setPythonProviderTargetId(pythonProviderTarget.id);
    }
  }, [pythonProviderTarget?.id, pythonProviderTargetId]);

  React.useEffect(() => {
    const skills = pythonProviderTarget?.codeExecution?.skills || [];
    const nextDraft = skills.length > 0 ? JSON.stringify(skills, null, 2) : '';
    pythonSkillsDraftRef.current = nextDraft;
    pythonSkillsDraftBaselineRef.current = nextDraft;
    setPythonSkillsDraft(nextDraft);
  }, [pythonProviderTarget?.id]);

  const updateAtlassian = React.useCallback((patch: Partial<AtlassianSettings>) => {
    setLocalSettings((prev) => ({
      ...prev,
      atlassian: {
        ...getAtlassian(prev),
        ...patch,
        jira: { ...getAtlassian(prev).jira, ...(patch.jira || {}) },
        confluence: { ...getAtlassian(prev).confluence, ...(patch.confluence || {}) },
      },
    }));
  }, [setLocalSettings]);

  const updateBrowserControl = React.useCallback((patch: Partial<BrowserControlSettings>) => {
    setLocalSettings((prev) => ({ ...prev, browserControl: { ...getBrowserControl(prev), ...patch } }));
  }, [setLocalSettings]);

  const updateLocalComputer = React.useCallback((patch: Partial<LocalComputerSettings>) => {
    setLocalSettings((prev) => ({
      ...prev,
      localComputer: {
        ...getLocalComputer(prev),
        ...patch,
        selectedFolders: patch.selectedFolders
          ? dedupeLocalFolders(patch.selectedFolders)
          : getLocalComputer(prev).selectedFolders,
        accessMode: normalizeLocalComputerMode(patch.accessMode ?? getLocalComputer(prev).accessMode),
      },
    }));
  }, [setLocalSettings]);

  const updateSharePoint = React.useCallback((patch: Partial<SharePointSettings>) => {
    setLocalSettings((prev) => ({ ...prev, sharepoint: { ...getSharePoint(prev), ...patch } }));
  }, [setLocalSettings]);

  const updateGithub = React.useCallback((patch: Partial<GitHubSettings>) => {
    setLocalSettings((prev) => ({ ...prev, github: { ...getGithub(prev), ...patch } }));
  }, [setLocalSettings]);

  const updateGitHubOAuthClientId = React.useCallback((clientId: string) => {
    setLocalSettings((prev) => ({ ...prev, githubOAuthClientId: clientId }));
  }, [setLocalSettings]);

  const updateSlack = React.useCallback((patch: Partial<SlackSettings>) => {
    setLocalSettings((prev) => ({ ...prev, slack: { ...getSlack(prev), ...patch } } as AppSettings));
  }, [setLocalSettings]);

  const updateSlackOAuthClientId = React.useCallback((clientId: string) => {
    setLocalSettings((prev) => ({ ...prev, slackOAuthClientId: clientId } as AppSettings));
  }, [setLocalSettings]);

  const updateImageGeneration = React.useCallback((patch: Partial<ImageGenerationSettings>) => {
    setLocalSettings((prev) => ({
      ...prev,
      imageGeneration: { ...getImageGeneration(prev), ...patch },
    }));
  }, [setLocalSettings]);

  const updateImageGenerationDefaults = React.useCallback((patch: Partial<ImageGenerationDefaults>) => {
    setLocalSettings((prev) => {
      const current = getImageGeneration(prev);
      return {
        ...prev,
        imageGeneration: {
          ...current,
          defaults: { ...(current.defaults || {}), ...patch },
        },
      };
    });
  }, [setLocalSettings]);

  const updateProviderConfigLocal = React.useCallback((
    targetId: string,
    mutate: (config: ProviderConfig) => ProviderConfig,
  ) => {
    setLocalSettings((prev) => ({
      ...prev,
      providerConfigs: (prev.providerConfigs || []).map((config) => (
        config.id === targetId ? mutate(config) : config
      )),
    }));
  }, [setLocalSettings]);

  const withHostedToolEnabled = React.useCallback((
    settings: AppSettings,
    targetId: string,
    kind: HostedConnectionKind,
    enabled: boolean,
  ): AppSettings => ({
    ...settings,
    providerConfigs: (settings.providerConfigs || []).map((config) => {
      const isTarget = kind === 'web-search'
        ? supportsProviderHostedTools(config)
        : config.id === targetId;
      if (!isTarget) return config;
      const capabilityControls = {
        ...(config.capabilityControls || {}),
        [hostedControlKey(kind)]: enabled ? 'on' : 'off',
      };
      if (kind === 'web-search') {
        return {
          ...config,
          capabilityControls,
          webSearch: canonicalProviderWebSearch(config.providerId, enabled),
        };
      }
      return {
        ...config,
        capabilityControls,
        codeExecution: { ...defaultCodeExecution(config), ...(config.codeExecution || {}), enabled },
      };
    }),
  }), []);

  const withAllHostedToolsDisabled = React.useCallback((
    settings: AppSettings,
    kind: HostedConnectionKind,
  ): AppSettings => disableHostedToolConnectionAcrossProviders(settings, kind), []);

  const updateHostedToolEnabled = React.useCallback((
    kind: HostedConnectionKind,
    targetId: string,
    enabled: boolean,
  ) => {
    setLocalSettings((prev) => withHostedToolEnabled(prev, targetId, kind, enabled));
  }, [setLocalSettings, withHostedToolEnabled]);

  // --- Scoped persistence -----------------------------------------------------
  // Connection actions persist ONLY that connection's settings slice, merged
  // onto the last saved settings. Drafts from other Settings tabs (or other
  // connections) are never committed as a side effect of connecting, toggling,
  // signing out of, or removing a connection.

  /**
   * Persist connection `id`'s slice taken from `nextLocal`. Preferred path is
   * the serialized settings mutation: the slice is rebased onto the LATEST
   * saved settings when its turn in the write queue comes up, so a
   * concurrent write from another surface (the chat drawer pill, another
   * connection's flow) is never clobbered by a stale base. The legacy path
   * (no mutation prop) merges onto the last settings this console saw.
   */
  const commitConnectionSlice = React.useCallback(async (
    id: ConnectionCatalogId,
    nextLocal: AppSettings,
    attachConnections?: AppSettings['connections'],
  ): Promise<AppSettings> => {
    if (onApplySettingsMutation) {
      const persisted = await onApplySettingsMutation((latest) => {
        const merged = syncConnectionRuntimeMirrors(id, mergeConnectionPatch(latest, id, nextLocal));
        return attachConnections ? { ...merged, connections: attachConnections } : merged;
      });
      savedSettingsRef.current = persisted;
      return persisted;
    }
    const merged = syncConnectionRuntimeMirrors(
      id,
      mergeConnectionPatch(savedSettingsRef.current, id, nextLocal),
    );
    const persisted = attachConnections ? { ...merged, connections: attachConnections } : merged;
    await Promise.resolve(onSave(persisted));
    savedSettingsRef.current = persisted;
    return persisted;
  }, [onApplySettingsMutation, onSave]);

  /** Persist `id`'s current local slice (call before backend flows that read saved settings). */
  const persistConnectionSlice = React.useCallback(async (id: ConnectionCatalogId) => {
    const nextLocal = localSettingsRef.current;
    await commitConnectionSlice(id, nextLocal);
    // What was just persisted is no longer an unsaved draft: rebase the
    // sheet's discard/dirty baseline onto it.
    if (sheetRef.current?.id === id) sheetSnapshotRef.current = nextLocal;
  }, [commitConnectionSlice]);

  /** Apply a local mutation and persist that connection's slice immediately. */
  const persistConnectionUpdate = React.useCallback(async (id: ConnectionCatalogId, mutate: (prev: AppSettings) => AppSettings) => {
    const nextLocal = mutate(localSettingsRef.current);
    setLocalSettings(nextLocal);
    localSettingsRef.current = nextLocal;
    await commitConnectionSlice(id, nextLocal);
    if (sheetRef.current?.id === id) sheetSnapshotRef.current = nextLocal;
  }, [commitConnectionSlice, setLocalSettings]);

  const persistThreadSearch = React.useCallback((patch: Partial<ThreadSearchSettings>) => persistConnectionUpdate(
    'thread-search',
    (prev) => ({ ...prev, threadSearch: { ...getThreadSearch(prev), ...patch } }),
  ), [persistConnectionUpdate]);

  const persistImageGeneration = React.useCallback((patch: Partial<ImageGenerationSettings>) => persistConnectionUpdate(
    'image-generation',
    (prev) => ({
      ...prev,
      imageGeneration: { ...getImageGeneration(prev), ...patch },
    }),
  ), [persistConnectionUpdate]);

  const persistHostedToolEnabled = React.useCallback((
    kind: HostedConnectionKind,
    targetId: string,
    enabled: boolean,
  ) => persistConnectionUpdate(
    kind,
    (prev) => withHostedToolEnabled(prev, targetId, kind, enabled),
  ), [persistConnectionUpdate, withHostedToolEnabled]);

  const persistAllHostedToolsDisabled = React.useCallback((kind: HostedConnectionKind) => persistConnectionUpdate(
    kind,
    (prev) => withAllHostedToolsDisabled(prev, kind),
  ), [persistConnectionUpdate, withAllHostedToolsDisabled]);

  const persistLocalComputer = React.useCallback((patch: Partial<LocalComputerSettings>) => persistConnectionUpdate(
    'local-computer',
    (prev) => ({
      ...prev,
      localComputer: {
        ...getLocalComputer(prev),
        ...patch,
        selectedFolders: patch.selectedFolders
          ? dedupeLocalFolders(patch.selectedFolders)
          : getLocalComputer(prev).selectedFolders,
        accessMode: normalizeLocalComputerMode(patch.accessMode ?? getLocalComputer(prev).accessMode),
      },
    }),
  ), [persistConnectionUpdate]);

  const persistBrowserControl = React.useCallback((patch: Partial<BrowserControlSettings>) => persistConnectionUpdate(
    'browser',
    (prev) => ({ ...prev, browserControl: { ...getBrowserControl(prev), ...patch } }),
  ), [persistConnectionUpdate]);

  const persistGithub = React.useCallback((patch: Partial<GitHubSettings>) => persistConnectionUpdate(
    'github',
    (prev) => ({ ...prev, github: { ...getGithub(prev), ...patch } }),
  ), [persistConnectionUpdate]);

  const persistSlack = React.useCallback((patch: Partial<SlackSettings>) => persistConnectionUpdate(
    'slack',
    (prev) => ({ ...prev, slack: { ...getSlack(prev), ...patch } } as AppSettings),
  ), [persistConnectionUpdate]);

  const persistAtlassian = React.useCallback((patch: Partial<AtlassianSettings>) => persistConnectionUpdate(
    'atlassian',
    (prev) => ({
      ...prev,
      atlassian: {
        ...getAtlassian(prev),
        ...patch,
        jira: { ...getAtlassian(prev).jira, ...(patch.jira || {}) },
        confluence: { ...getAtlassian(prev).confluence, ...(patch.confluence || {}) },
      },
    }),
  ), [persistConnectionUpdate]);

  const persistSharePoint = React.useCallback((patch: Partial<SharePointSettings>) => persistConnectionUpdate(
    'sharepoint',
    (prev) => ({ ...prev, sharepoint: { ...getSharePoint(prev), ...patch } }),
  ), [persistConnectionUpdate]);

  const handleSaveImageGeneration = React.useCallback(async () => {
    const current = getImageGeneration(localSettingsRef.current);
    const target = (localSettingsRef.current.providerConfigs || []).find(
      (config) => config.id === current.providerId && supportsImageGeneration(config),
    );
    if (!target || !String(current.model || '').trim()) {
      toast.error('Choose an image provider and model first.');
      return;
    }
    const enable = sheetRef.current?.intent !== 'edit' ? true : current.enabled === true;
    await persistImageGeneration({ enabled: enable });
    toast.success(enable ? 'Image Generation is ready.' : 'Image Generation settings saved.');
    closeSheetFor('image-generation');
  }, [closeSheetFor, persistImageGeneration]);

  const handleSaveHostedConnection = React.useCallback(async (
    kind: HostedConnectionKind,
    targetId: string,
  ) => {
    if (!targetId) {
      toast.error('Add a supported provider connection first.');
      return;
    }
    let parsedSkills: ProviderCodeExecutionSkill[] | null = null;
    if (kind === 'python-sandbox') {
      try {
        parsedSkills = parsePythonSkills(pythonSkillsDraft);
      } catch (error: any) {
        const message = error?.message || 'Skills must be valid JSON.';
        setHostedToolFeedback({ kind, text: message });
        toast.error(message);
        return;
      }
    }
    await persistConnectionUpdate(kind, (prev) => {
      let next = prev;
      if (kind === 'python-sandbox') {
        next = {
          ...next,
          providerConfigs: (next.providerConfigs || []).map((config) => (
            config.id === targetId
              ? {
                ...config,
                codeExecution: {
                  ...defaultCodeExecution(config),
                  ...(config.codeExecution || {}),
                  skills: parsedSkills,
                },
              }
              : config
          )),
        };
      }
      const hasEnabledTarget = (next.providerConfigs || []).some(
        (config) => supportsProviderHostedTools(config) && hostedConnectionEnabled(config, kind),
      );
      if (sheetRef.current?.intent !== 'edit' && !hasEnabledTarget) {
        next = withHostedToolEnabled(next, targetId, kind, true);
      }
      return next;
    });
    setHostedToolFeedback(null);
    toast.success(kind === 'web-search' ? 'Web Search connection saved.' : 'Python Sandbox connection saved.');
    closeSheetFor(kind);
  }, [closeSheetFor, persistConnectionUpdate, pythonSkillsDraft, withHostedToolEnabled]);

  const syncLocalComputerGrants = React.useCallback(async (
    mode: LocalComputerAccessMode,
    selectedFolders: string[],
  ) => {
    await syncLocalComputerGrantRules(mode, selectedFolders);
  }, []);

  const revokeLocalComputerConfiguredRules = React.useCallback(async (
    config: LocalComputerSettings = getLocalComputer(localSettingsRef.current),
  ) => {
    const rules = await listLocalComputerGrantRules();
    const wholeRoots = await localComputerWholeRoots();
    await deleteLocalComputerGrantRulesForRoots(rules, [
      ...wholeRoots,
      ...rules
        .map((rule) => String(rule.subject.local_root || '').trim())
        .filter(isLocalFilesystemRoot),
      ...dedupeLocalFolders(config.selectedFolders || []),
    ]);
  }, []);

  const handleLocalComputerPickFolder = React.useCallback(async () => {
    setLocalComputerBusy('pick');
    setLocalComputerFeedback(null);
    try {
      const picked = await pickLocalDirectory();
      updateLocalComputer({
        accessMode: 'selected-folders',
        selectedFolders: dedupeLocalFolders([
          ...(getLocalComputer(localSettingsRef.current).selectedFolders || []),
          picked.path,
        ]),
      });
    } catch (e: any) {
      if (!isLocalPickerCanceled(e)) {
        const detail = e?.message || 'Could not open the folder picker.';
        setLocalComputerFeedback({ text: detail, error: true });
        toast.error(detail);
      }
    } finally {
      setLocalComputerBusy(null);
    }
  }, [updateLocalComputer]);

  const handleLocalComputerSaveAccess = React.useCallback(async () => {
    const config = getLocalComputer(localSettingsRef.current);
    const mode = normalizeLocalComputerMode(config.accessMode);
    const selectedFolders = dedupeLocalFolders(config.selectedFolders || []);
    if (mode === 'selected-folders' && selectedFolders.length === 0) {
      setLocalComputerFeedback({ text: 'Choose at least one folder, or switch to Whole computer.', error: true });
      return;
    }
    setLocalComputerBusy('save');
    setLocalComputerFeedback(null);
    try {
      await syncLocalComputerGrants(mode, selectedFolders);
      await persistLocalComputer({
        enabled: true,
        accessMode: mode,
        selectedFolders: mode === 'selected-folders' ? selectedFolders : [],
        lastCheckedAt: Date.now(),
        lastCheckOk: true,
        lastCheckMessage: null,
      });
      setLocalComputerFeedback({ text: 'Read-only local access saved.' });
      toast.success(mode === 'whole-computer'
        ? 'Local Computer can read and search this computer.'
        : 'Local Computer folder access saved.');
      closeSheetFor('local-computer');
    } catch (e: any) {
      const detail = isPermissionsUnavailable(e)
        ? 'The permissions service is not available in this build.'
        : e?.message || 'Could not save local computer access.';
      setLocalComputerFeedback({ text: detail, error: true });
      toast.error(detail);
    } finally {
      setLocalComputerBusy(null);
    }
  }, [closeSheetFor, persistLocalComputer, syncLocalComputerGrants]);

  const handleLocalComputerDisable = React.useCallback(async () => {
    if (localComputerBusy) return;
    const config = getLocalComputer(localSettingsRef.current);
    setLocalComputerBusy('disable');
    setLocalComputerFeedback(null);
    try {
      await revokeLocalComputerConfiguredRules(config);
      await persistLocalComputer({
        enabled: false,
        lastCheckedAt: Date.now(),
        lastCheckOk: false,
        lastCheckMessage: 'Disabled',
      });
      toast.success('Local Computer access disabled.');
    } catch (e: any) {
      const detail = isPermissionsUnavailable(e)
        ? 'The permissions service is not available in this build.'
        : e?.message || 'Could not disable local computer access.';
      setLocalComputerFeedback({ text: detail, error: true });
      toast.error(detail);
    } finally {
      setLocalComputerBusy(null);
    }
  }, [localComputerBusy, persistLocalComputer, revokeLocalComputerConfiguredRules]);

  const addConnection = React.useCallback((id: ConnectionCatalogId) => {
    const prev = localSettingsRef.current;
    const current = Array.isArray(prev.connections?.visibleIds) ? prev.connections!.visibleIds as ConnectionCatalogId[] : [];
    if (current.includes(id)) return;
    const next: AppSettings = {
      ...prev,
      connections: { ...(prev.connections || {}), visibleIds: [...current, id] },
    };
    setLocalSettings(next);
    localSettingsRef.current = next;
    void commitConnectionSlice(id, next, next.connections);
  }, [commitConnectionSlice, setLocalSettings]);

  const removeConnection = React.useCallback((id: ConnectionCatalogId) => {
    // Removing a connection detaches its capability from the running model: the
    // automatic enablement is switched off so re-adding it later requires the
    // user to explicitly enable it again. Authentication/config is preserved.
    const prev = localSettingsRef.current;
    const current = Array.isArray(prev.connections?.visibleIds) ? prev.connections!.visibleIds as ConnectionCatalogId[] : [];
    const base: AppSettings = {
      ...prev,
      connections: { ...(prev.connections || {}), visibleIds: current.filter((x) => x !== id) },
    };
    switch (id) {
      case 'thread-search':
        base.threadSearch = { ...getThreadSearch(prev), enabled: false };
        break;
      case 'image-generation':
        base.imageGeneration = { ...getImageGeneration(prev), enabled: false };
        break;
      case 'web-search':
        base.providerConfigs = withAllHostedToolsDisabled(base, 'web-search').providerConfigs;
        break;
      case 'python-sandbox':
        base.providerConfigs = withAllHostedToolsDisabled(base, 'python-sandbox').providerConfigs;
        break;
      case 'local-computer':
        base.localComputer = { ...getLocalComputer(prev), enabled: false, lastCheckOk: false, lastCheckMessage: 'Removed' };
        void revokeLocalComputerConfiguredRules(getLocalComputer(prev)).catch((e: any) => {
          toast.error(e?.message || 'Could not revoke Local Computer grants.');
        });
        break;
      case 'browser':
        base.browserControl = { ...getBrowserControl(prev), enabled: false };
        break;
      case 'github':
        base.github = { ...getGithub(prev), enabled: false, readTools: false };
        break;
      case 'slack':
        base.slack = { ...getSlack(prev), enabled: false, readTools: false, writeTools: false } as AppSettings['slack'];
        break;
      case 'atlassian': {
        const atl = getAtlassian(prev);
        base.atlassian = { ...atl, jira: { ...atl.jira, enabled: false }, confluence: { ...atl.confluence, enabled: false } };
        break;
      }
      case 'sharepoint':
        base.sharepoint = { ...getSharePoint(prev), enabled: false };
        break;
      case 'bigquery':
      case 'databricks':
      case 'trino':
      case 'snowflake':
        base.dataSources = {
          ...prev.dataSources,
          [id]: { ...dataSourceState(prev, id), enabled: false },
        };
        break;
      case 'arxiv':
        base.arxiv = { ...(prev.arxiv || {}), enabled: false };
        break;
      default:
        break;
    }
    setLocalSettings(base);
    localSettingsRef.current = base;
    // Persist only the catalogue list + this connection's slice; unrelated
    // drafts in localSettings stay local.
    void commitConnectionSlice(id, base, base.connections);
  }, [commitConnectionSlice, revokeLocalComputerConfiguredRules, setLocalSettings, withAllHostedToolsDisabled]);

  const clearGithubPoll = React.useCallback(() => {
    if (githubPollTimeoutRef.current !== null) {
      window.clearTimeout(githubPollTimeoutRef.current);
      githubPollTimeoutRef.current = null;
    }
  }, []);

  const clearSlackPoll = React.useCallback(() => {
    if (slackPollTimeoutRef.current !== null) {
      window.clearTimeout(slackPollTimeoutRef.current);
      slackPollTimeoutRef.current = null;
    }
  }, []);

  const clearAtlassianPoll = React.useCallback(() => {
    atlassianPollGenRef.current += 1;
    if (atlassianPollTimeoutRef.current !== null) {
      window.clearTimeout(atlassianPollTimeoutRef.current);
      atlassianPollTimeoutRef.current = null;
    }
  }, []);

  const clearSharePointPoll = React.useCallback(() => {
    sharePointPollGenRef.current += 1;
    if (sharePointPollTimeoutRef.current !== null) {
      window.clearTimeout(sharePointPollTimeoutRef.current);
      sharePointPollTimeoutRef.current = null;
    }
  }, []);

  const clearAdcReauthPoll = React.useCallback(() => {
    // Bump the generation so any in-flight poll response is ignored, then drop
    // the pending timer. Both are needed: the timer stops future ticks, the
    // generation guard stops a slow response already awaiting a fetch.
    bqReauthGenRef.current += 1;
    if (bqReauthPollRef.current !== null) {
      window.clearTimeout(bqReauthPollRef.current);
      bqReauthPollRef.current = null;
    }
  }, []);

  const refreshStatus = React.useCallback(async () => {
    try {
      const data = await apiJson('/api/integrations/atlassian/status');
      setStatus(data);
    } catch (e: any) {
      setStatus({ error: e?.message || 'Failed to load Atlassian status' });
    }
  }, []);

  const refreshSharePointStatus = React.useCallback(async () => {
    try {
      const data = await apiJson('/api/integrations/sharepoint/status');
      setSharePointStatus(data);
    } catch (e: any) {
      setSharePointStatus({ error: e?.message || 'Failed to load SharePoint status' });
    }
  }, []);

  const refreshGitHubStatus = React.useCallback(async () => {
    try {
      const data = await apiJson('/api/integrations/github/status');
      setGithubStatus(unwrapGitHubStatus(data));
    } catch (e: any) {
      setGithubStatus({ error: e?.message || 'Failed to load GitHub status' });
    }
  }, []);

  const refreshGitHubPrereq = React.useCallback(async () => {
    try {
      const data = await apiJson('/api/integrations/github/auth/prerequisites');
      setGithubPrereq((data?.prerequisites || data || {}) as GitHubPrerequisites);
    } catch {
      setGithubPrereq(null);
    }
  }, []);

  const refreshSlackStatus = React.useCallback(async () => {
    try {
      const data = await apiJson('/api/integrations/slack/status');
      setSlackStatus(unwrapSlackStatus(data));
    } catch (e: any) {
      setSlackStatus({ error: e?.message || 'Failed to load Slack status' });
    }
  }, []);

  const refreshBigQuery = React.useCallback(async (): Promise<NormalizedAdcStatus | null> => {
    try {
      const data = await getBigQueryAdcStatus();
      setBqStatus(data);
      return data;
    } catch {
      setBqStatus(null);
      return null;
    }
  }, []);

  const refreshDatabricks = React.useCallback(async () => {
    try {
      const st = await getDatabricksStatus();
      setDbStatus(st);
      if (st.authMode === 'pat') setDbAuthMode('pat');
      else if (st.authMode === 'refresh_token') setDbAuthMode('refresh_token');
    } catch {
      setDbStatus(null);
    }
    try {
      const secrets = await getSecretsStatus();
      setDbRefreshSet(Boolean(secrets.present.databricks_refresh_token));
      setDbPatSet(Boolean(secrets.present.databricks_pat));
    } catch {
      /* secrets store may be locked */
    }
  }, []);

  const refreshTrino = React.useCallback(async () => {
    let backendAuthMode: TrinoAuthMode | null = null;
    try {
      const st = await getTrinoStatus();
      setTrinoStatus(st);
      backendAuthMode = st.authMode;
      setTrinoAuthMode(st.authMode);
    } catch {
      setTrinoStatus(null);
    }
    try {
      const secrets = await getSecretsStatus();
      const hasBasic = Boolean(
        secrets.present.trino_basic_password
        || secrets.present.trino_password
        || secrets.present.trino_basic
        || secrets.present['provider:trino:basic'],
      );
      const hasJwt = Boolean(
        secrets.present.trino_jwt
        || secrets.present.trino_jwt_token
        || secrets.present['provider:trino:jwt'],
      );
      const hasGeneric = Boolean(secrets.present.trino_credentials || secrets.present['provider:trino']);
      setTrinoBasicSet(hasBasic || (hasGeneric && backendAuthMode === 'basic'));
      setTrinoJwtSet(hasJwt || (hasGeneric && backendAuthMode === 'jwt'));
    } catch {
      /* secrets store may be locked */
    }
  }, []);

  const refreshSnowflake = React.useCallback(async () => {
    let backendAuthMode: SnowflakeAuthMode | null = null;
    try {
      const st = await getSnowflakeStatus();
      setSnowflakeStatus(st);
      backendAuthMode = st.authMode;
      if (st.authMode !== 'none') setSnowflakeAuthMode(st.authMode);
    } catch {
      setSnowflakeStatus(null);
    }
    try {
      const secrets = await getSecretsStatus();
      const hasOAuth = Boolean(
        secrets.present.snowflake_oauth_token
        || secrets.present.snowflakeOAuthToken
        || secrets.present['provider:snowflake:oauth_token'],
      );
      const hasKeyPair = Boolean(
        secrets.present.snowflake_keypair
        || secrets.present.snowflake_key_pair
        || secrets.present.snowflakeKeyPair
        || secrets.present['provider:snowflake:keypair_jwt'],
      );
      const hasGeneric = Boolean(secrets.present.snowflake_credentials || secrets.present['provider:snowflake']);
      setSnowflakeOAuthSet(hasOAuth || (hasGeneric && backendAuthMode === 'oauth_token'));
      setSnowflakeKeyPairSet(hasKeyPair || (hasGeneric && backendAuthMode === 'keypair_jwt'));
    } catch {
      /* secrets store may be locked */
    }
  }, []);

  React.useEffect(() => { void refreshStatus(); }, [refreshStatus]);
  React.useEffect(() => { void refreshSharePointStatus(); }, [refreshSharePointStatus]);
  React.useEffect(() => { void refreshGitHubStatus(); }, [refreshGitHubStatus]);
  React.useEffect(() => { void refreshGitHubPrereq(); }, [refreshGitHubPrereq]);
  React.useEffect(() => { void refreshSlackStatus(); }, [refreshSlackStatus]);
  React.useEffect(() => { void refreshBigQuery(); }, [refreshBigQuery]);
  React.useEffect(() => { void refreshDatabricks(); }, [refreshDatabricks]);
  React.useEffect(() => { void refreshTrino(); }, [refreshTrino]);
  React.useEffect(() => { void refreshSnowflake(); }, [refreshSnowflake]);
  React.useEffect(() => clearGithubPoll, [clearGithubPoll]);
  React.useEffect(() => clearSlackPoll, [clearSlackPoll]);
  React.useEffect(() => clearAtlassianPoll, [clearAtlassianPoll]);
  React.useEffect(() => clearSharePointPoll, [clearSharePointPoll]);
  React.useEffect(() => clearAdcReauthPoll, [clearAdcReauthPoll]);

  const persistDataSource = React.useCallback(async (key: DataSourceKey, patch: Partial<DataSourceState>) => {
    const prev = localSettingsRef.current;
    const merged: DataSourceState = { ...dataSourceState(prev, key), ...patch };
    const next: AppSettings = {
      ...prev,
      dataSources: { ...prev.dataSources, [key]: merged },
    };
    // Every check verdict re-anchors the fingerprint: success proves the
    // current config, failure invalidates whatever was proven before. Editing
    // host/warehouse/schema/user afterwards then reads as "unverified changes"
    // instead of staying green.
    if (typeof patch.lastCheckOk === 'boolean') {
      merged.lastVerifiedFingerprint = patch.lastCheckOk ? configFingerprint(next, key) : null;
    }
    setLocalSettings(next);
    localSettingsRef.current = next;
    await commitConnectionSlice(key, next);
    if (sheetRef.current?.id === key) sheetSnapshotRef.current = next;
  }, [commitConnectionSlice, setLocalSettings]);

  // --- arXiv ------------------------------------------------------------------
  const [arxivStatus, setArxivStatus] = React.useState<ArxivStatus | null>(null);
  const arxivVisible = visibleSet.has('arxiv');

  React.useEffect(() => {
    if (!arxivVisible) return undefined;
    let cancelled = false;
    (async () => {
      try {
        const st = await getArxivStatus();
        if (!cancelled) setArxivStatus(st);
      } catch {
        // Backend without arXiv support (or not running) — row still renders
        // from local settings; the papers panel surfaces its own errors.
      }
    })();
    return () => { cancelled = true; };
  }, [arxivVisible]);

  const updateArxiv = React.useCallback((patch: Partial<ArxivSettings>) => {
    setLocalSettings((prev) => ({ ...prev, arxiv: { ...(prev.arxiv || {}), ...patch } }));
  }, [setLocalSettings]);

  const persistArxiv = React.useCallback((patch: Partial<ArxivSettings>) => persistConnectionUpdate(
    'arxiv',
    (prev) => ({ ...prev, arxiv: { ...(prev.arxiv || {}), ...patch } }),
  ), [persistConnectionUpdate]);

  const applyAtlassianHealth = React.useCallback((data: any, fallbackMessage: string) => {
    const products = data?.products || {};
    const checkedAt = Date.now();
    const patch: Partial<AtlassianSettings> = {};
    if (products.jira) {
      patch.jira = {
        lastCheckedAt: checkedAt,
        lastCheckOk: productIsConnected(products.jira),
        lastCheckMessage: compactText(products.jira.message, products.jira.error, fallbackMessage),
      };
    }
    if (products.confluence) {
      patch.confluence = {
        lastCheckedAt: checkedAt,
        lastCheckOk: productIsConnected(products.confluence),
        lastCheckMessage: compactText(products.confluence.message, products.confluence.error, fallbackMessage),
      };
    }
    if (patch.jira || patch.confluence) {
      const anyOk = (products.jira && productIsConnected(products.jira))
        || (products.confluence && productIsConnected(products.confluence));
      patch.lastVerifiedFingerprint = anyOk ? configFingerprint(localSettingsRef.current, 'atlassian') : null;
      void persistAtlassian(patch);
    }
  }, [persistAtlassian]);

  const pollAtlassianLogin = React.useCallback((generation: number, deadline: number) => {
    const schedule = (delayMs: number) => {
      if (generation !== atlassianPollGenRef.current) return;
      atlassianPollTimeoutRef.current = window.setTimeout(tick, delayMs);
    };
    const stop = () => {
      clearAtlassianPoll();
      setBusy(null);
    };
    async function tick() {
      if (generation !== atlassianPollGenRef.current) return;
      let data: any = null;
      try {
        data = await apiJson('/api/integrations/atlassian/status');
        if (generation !== atlassianPollGenRef.current) return;
        setStatus(data);
      } catch (e: any) {
        if (Date.now() >= deadline) {
          stop();
          toast.error(e?.message || 'Atlassian sign-in status check timed out.');
          return;
        }
        schedule(1500);
        return;
      }

      const autoState = String(data?.autoLogin?.state || '').toLowerCase();
      const mode = String(data?.mode || '').toLowerCase();
      if (autoState === 'hidden' || mode === 'hidden') {
        clearAtlassianPoll();
        setBusy('finish');
        try {
          const checked = await apiJson('/api/integrations/atlassian/check', { method: 'POST' });
          setStatus(checked);
          applyAtlassianHealth(checked, 'Atlassian sign-in completed.');
          toast.success('Atlassian sign-in complete');
          // The action the user initiated in the setup window succeeded —
          // close it explicitly (never from derived stage drift).
          closeSheetFor('atlassian');
        } catch (e: any) {
          const detail = e?.message || 'Atlassian sign-in completed, but the connection check failed.';
          setStatus((prev) => ({ ...(prev || {}), error: detail }));
          toast.error(detail);
        } finally {
          setBusy(null);
        }
        return;
      }

      if (autoState === 'error' || autoState === 'expired') {
        stop();
        toast.error(compactText(data?.autoLogin?.message, data?.error, 'Atlassian sign-in did not complete.'));
        return;
      }

      if (Date.now() >= deadline) {
        stop();
        toast.error('Timed out waiting for Atlassian sign-in to complete.');
        return;
      }

      schedule(1500);
    }
    schedule(1000);
  }, [applyAtlassianHealth, clearAtlassianPoll, closeSheetFor]);

  // Set when the user cancels an in-flight sign-in so the status-driven
  // auto-resume effect below does not immediately restart the poll.
  const atlassianCancelledRef = React.useRef(false);

  const handleAtlassianLogin = React.useCallback(async () => {
    clearAtlassianPoll();
    atlassianCancelledRef.current = false;
    const generation = atlassianPollGenRef.current;
    setBusy('login');
    try {
      await persistConnectionSlice('atlassian');
      const data = await apiJson('/api/integrations/atlassian/login/start', { method: 'POST' });
      setStatus(data);
      toast.success('Login browser opened');
      pollAtlassianLogin(generation, Date.now() + 15 * 60 * 1000);
    } catch (e: any) {
      clearAtlassianPoll();
      setBusy(null);
      toast.error(e?.message || 'Atlassian sign-in failed to start');
    }
  }, [clearAtlassianPoll, persistConnectionSlice, pollAtlassianLogin]);

  const handleAtlassianFinish = React.useCallback(async () => {
    clearAtlassianPoll();
    setBusy('finish');
    try {
      await persistConnectionSlice('atlassian');
      const data = await apiJson('/api/integrations/atlassian/login/finish', { method: 'POST' });
      setStatus(data);
      applyAtlassianHealth(data, 'Atlassian sign-in completed.');
      toast.success('Atlassian sign-in complete');
      closeSheetFor('atlassian');
    } catch (e: any) {
      toast.error(e?.message || 'Atlassian sign-in finalization failed');
    } finally {
      setBusy(null);
    }
  }, [applyAtlassianHealth, clearAtlassianPoll, closeSheetFor, persistConnectionSlice]);

  /** Inline cancel for the Atlassian sign-in poll: bump generation, clear busy. */
  const handleAtlassianCancelAuth = React.useCallback(() => {
    atlassianCancelledRef.current = true;
    clearAtlassianPoll();
    setBusy(null);
  }, [clearAtlassianPoll]);

  React.useEffect(() => {
    const autoState = String(status?.autoLogin?.state || '').toLowerCase();
    const mode = String(status?.mode || '').toLowerCase();
    const shouldPoll = mode === 'login' || autoState === 'watching' || autoState === 'switching';
    if (!shouldPoll || atlassianPollTimeoutRef.current !== null) return;
    if (atlassianCancelledRef.current) return;
    setBusy((prev) => prev || 'login');
    pollAtlassianLogin(atlassianPollGenRef.current, Date.now() + 15 * 60 * 1000);
  }, [pollAtlassianLogin, status?.autoLogin?.state, status?.mode]);

  const handleAtlassianDisconnect = React.useCallback(async () => {
    setBusy('disconnect');
    try {
      const data = await apiJson('/api/integrations/atlassian/disconnect', { method: 'POST' });
      setStatus(data);
      await persistAtlassian({
        lastVerifiedFingerprint: null,
        jira: { enabled: false, lastCheckedAt: Date.now(), lastCheckOk: false, lastCheckMessage: 'Signed out' },
        confluence: { enabled: false, lastCheckedAt: Date.now(), lastCheckOk: false, lastCheckMessage: 'Signed out' },
      });
      toast.success('Atlassian signed out');
    } catch (e: any) {
      const detail = e?.message || 'Atlassian sign-out failed';
      setStatus((prev) => ({ ...(prev || {}), error: detail }));
      toast.error(detail);
    } finally {
      setBusy(null);
    }
  }, [persistAtlassian]);

  /** Edit-intent save: persist the Atlassian slice, then strictly verify it. */
  const handleAtlassianSaveEdit = React.useCallback(async () => {
    setBusy('verify');
    try {
      await persistConnectionSlice('atlassian');
      const outcome = await verifyConnectionCredentials('atlassian', localSettingsRef.current);
      if (outcome.indeterminate) {
        toast.error(outcome.message || 'Saved, but the connection check could not run.');
        return;
      }
      setStatus(outcome.detail || null);
      applyAtlassianHealth(outcome.detail, outcome.ok ? 'Atlassian settings verified.' : (outcome.message || 'Atlassian check failed.'));
      if (outcome.ok) {
        toast.success('Atlassian settings saved and verified');
        closeSheetFor('atlassian');
      } else {
        toast.error(outcome.message || 'Saved, but the Atlassian check failed. Sign in again to verify.');
      }
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save Atlassian settings');
    } finally {
      setBusy(null);
    }
  }, [applyAtlassianHealth, closeSheetFor, persistConnectionSlice]);

  const applySharePointHealth = React.useCallback((data: any, fallbackMessage: string) => {
    const site = data?.site || {};
    const ok = productIsConnected(site);
    void persistSharePoint({
      lastCheckedAt: Date.now(),
      lastCheckOk: ok,
      lastCheckMessage: compactText(site.message, site.error, fallbackMessage),
      lastVerifiedFingerprint: ok ? configFingerprint(localSettingsRef.current, 'sharepoint') : null,
    });
  }, [persistSharePoint]);

  const pollSharePointLogin = React.useCallback((generation: number, deadline: number) => {
    const schedule = (delayMs: number) => {
      if (generation !== sharePointPollGenRef.current) return;
      sharePointPollTimeoutRef.current = window.setTimeout(tick, delayMs);
    };
    const stop = () => {
      clearSharePointPoll();
      setSharePointBusy(null);
    };
    async function tick() {
      if (generation !== sharePointPollGenRef.current) return;
      let data: any = null;
      try {
        data = await apiJson('/api/integrations/sharepoint/status');
        if (generation !== sharePointPollGenRef.current) return;
        setSharePointStatus(data);
      } catch (e: any) {
        if (Date.now() >= deadline) {
          stop();
          toast.error(e?.message || 'SharePoint sign-in status check timed out.');
          return;
        }
        schedule(1500);
        return;
      }

      const autoState = String(data?.autoLogin?.state || '').toLowerCase();
      const mode = String(data?.mode || '').toLowerCase();
      if (autoState === 'hidden' || mode === 'hidden') {
        clearSharePointPoll();
        setSharePointBusy('finish');
        try {
          const checked = await apiJson('/api/integrations/sharepoint/check', { method: 'POST' });
          setSharePointStatus(checked);
          applySharePointHealth(checked, 'SharePoint sign-in completed.');
          toast.success('SharePoint sign-in complete');
          closeSheetFor('sharepoint');
        } catch (e: any) {
          const detail = e?.message || 'SharePoint sign-in completed, but the connection check failed.';
          setSharePointStatus((prev) => ({ ...(prev || {}), error: detail }));
          toast.error(detail);
        } finally {
          setSharePointBusy(null);
        }
        return;
      }

      if (autoState === 'error' || autoState === 'expired') {
        stop();
        toast.error(compactText(data?.autoLogin?.message, data?.error, 'SharePoint sign-in did not complete.'));
        return;
      }

      if (Date.now() >= deadline) {
        stop();
        toast.error('Timed out waiting for SharePoint sign-in to complete.');
        return;
      }

      schedule(1500);
    }
    schedule(1000);
  }, [applySharePointHealth, clearSharePointPoll, closeSheetFor]);

  const sharePointCancelledRef = React.useRef(false);

  const handleSharePointLogin = React.useCallback(async () => {
    clearSharePointPoll();
    sharePointCancelledRef.current = false;
    const generation = sharePointPollGenRef.current;
    setSharePointBusy('login');
    try {
      await persistConnectionSlice('sharepoint');
      const data = await apiJson('/api/integrations/sharepoint/login/start', { method: 'POST' });
      setSharePointStatus(data);
      toast.success('SharePoint login browser opened');
      pollSharePointLogin(generation, Date.now() + 15 * 60 * 1000);
    } catch (e: any) {
      clearSharePointPoll();
      setSharePointBusy(null);
      toast.error(e?.message || 'SharePoint sign-in failed to start');
    }
  }, [clearSharePointPoll, persistConnectionSlice, pollSharePointLogin]);

  const handleSharePointFinish = React.useCallback(async () => {
    clearSharePointPoll();
    setSharePointBusy('finish');
    try {
      await persistConnectionSlice('sharepoint');
      const data = await apiJson('/api/integrations/sharepoint/login/finish', { method: 'POST' });
      setSharePointStatus(data);
      applySharePointHealth(data, 'SharePoint sign-in completed.');
      toast.success('SharePoint sign-in complete');
      closeSheetFor('sharepoint');
    } catch (e: any) {
      toast.error(e?.message || 'SharePoint sign-in finalization failed');
    } finally {
      setSharePointBusy(null);
    }
  }, [applySharePointHealth, clearSharePointPoll, closeSheetFor, persistConnectionSlice]);

  /** Inline cancel for the SharePoint sign-in poll: bump generation, clear busy. */
  const handleSharePointCancelAuth = React.useCallback(() => {
    sharePointCancelledRef.current = true;
    clearSharePointPoll();
    setSharePointBusy(null);
  }, [clearSharePointPoll]);

  React.useEffect(() => {
    const autoState = String(sharePointStatus?.autoLogin?.state || '').toLowerCase();
    const mode = String(sharePointStatus?.mode || '').toLowerCase();
    const shouldPoll = mode === 'login' || autoState === 'watching' || autoState === 'switching';
    if (!shouldPoll || sharePointPollTimeoutRef.current !== null) return;
    if (sharePointCancelledRef.current) return;
    setSharePointBusy((prev) => prev || 'login');
    pollSharePointLogin(sharePointPollGenRef.current, Date.now() + 15 * 60 * 1000);
  }, [pollSharePointLogin, sharePointStatus?.autoLogin?.state, sharePointStatus?.mode]);

  const handleSharePointDisconnect = React.useCallback(async () => {
    setSharePointBusy('disconnect');
    try {
      const data = await apiJson('/api/integrations/sharepoint/disconnect', { method: 'POST' });
      setSharePointStatus(data);
      await persistSharePoint({
        enabled: false,
        lastCheckedAt: Date.now(),
        lastCheckOk: false,
        lastCheckMessage: 'Signed out',
        lastVerifiedFingerprint: null,
      });
      toast.success('SharePoint signed out');
    } catch (e: any) {
      const detail = e?.message || 'SharePoint sign-out failed';
      setSharePointStatus((prev) => ({ ...(prev || {}), error: detail }));
      toast.error(detail);
    } finally {
      setSharePointBusy(null);
    }
  }, [persistSharePoint]);

  /** Edit-intent save: persist the SharePoint slice, then strictly verify it. */
  const handleSharePointSaveEdit = React.useCallback(async () => {
    setSharePointBusy('verify');
    try {
      await persistConnectionSlice('sharepoint');
      const outcome = await verifyConnectionCredentials('sharepoint', localSettingsRef.current);
      if (outcome.indeterminate) {
        toast.error(outcome.message || 'Saved, but the connection check could not run.');
        return;
      }
      setSharePointStatus(outcome.detail || null);
      applySharePointHealth(outcome.detail, outcome.ok ? 'SharePoint settings verified.' : (outcome.message || 'SharePoint check failed.'));
      if (outcome.ok) {
        toast.success('SharePoint settings saved and verified');
        closeSheetFor('sharepoint');
      } else {
        toast.error(outcome.message || 'Saved, but the SharePoint check failed. Sign in again to verify.');
      }
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save SharePoint settings');
    } finally {
      setSharePointBusy(null);
    }
  }, [applySharePointHealth, closeSheetFor, persistConnectionSlice]);

  const syncGitHubOAuthClient = React.useCallback(async () => {
    const data = await apiJson('/api/integrations/github/auth/client', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ client_id: (localSettings.githubOAuthClientId || '').trim() }),
    });
    const nextStatus = unwrapGitHubStatus(data);
    if (Object.keys(nextStatus).length > 0) setGithubStatus(nextStatus);
  }, [localSettings.githubOAuthClientId]);

  const pollGitHubAuth = React.useCallback((state: string, delaySeconds: number) => {
    clearGithubPoll();
    const delay = Math.max(1, delaySeconds || 5) * 1000;
    githubPollTimeoutRef.current = window.setTimeout(async () => {
      try {
        const data = await apiJson('/api/integrations/github/auth/poll', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ state }),
        });
        if (data?.state === 'complete') {
          clearGithubPoll();
          const nextStatus = unwrapGitHubStatus(data);
          setGithubStatus(nextStatus);
          void persistGithub({
            lastCheckedAt: Date.now(),
            lastCheckOk: true,
            lastCheckMessage: 'Connected',
            lastVerifiedFingerprint: configFingerprint(localSettingsRef.current, 'github'),
          });
          setGithubDeviceFlow(null);
          setGithubAuthMessage(nextStatus.login ? `Connected as ${nextStatus.login}.` : 'GitHub connected.');
          setGithubBusy(null);
          toast.success(nextStatus.login ? `Connected GitHub as ${nextStatus.login}.` : 'GitHub connected.');
          closeSheetFor('github');
          return;
        }
        setGithubAuthMessage('Waiting for GitHub authorization…');
        pollGitHubAuth(state, Number(data?.interval || delaySeconds || 5));
      } catch (e: any) {
        clearGithubPoll();
        const detail = e?.message || 'GitHub login failed';
        setGithubStatus((prev) => ({ ...(prev || {}), error: detail }));
        setGithubDeviceFlow(null);
        setGithubAuthMessage(detail);
        setGithubBusy(null);
        toast.error(detail);
      }
    }, delay);
  }, [clearGithubPoll, closeSheetFor, persistGithub]);

  const handleGitHubConnect = React.useCallback(async () => {
    setGithubBusy('github-connect');
    setGithubAuthMessage('Starting GitHub login…');
    setGithubDeviceFlow(null);
    clearGithubPoll();
    try {
      await persistConnectionSlice('github');
      await syncGitHubOAuthClient();
      const data = await apiJson('/api/integrations/github/auth/start', { method: 'POST' });
      if (data?.state === 'complete') {
        const nextStatus = unwrapGitHubStatus(data);
        setGithubStatus(nextStatus);
        void persistGithub({
          lastCheckedAt: Date.now(),
          lastCheckOk: true,
          lastCheckMessage: 'Connected',
          lastVerifiedFingerprint: configFingerprint(localSettingsRef.current, 'github'),
        });
        setGithubAuthMessage(nextStatus.login ? `Signed in as ${nextStatus.login}.` : 'Signed in to GitHub.');
        setGithubBusy(null);
        toast.success(nextStatus.login ? `Signed in to GitHub as ${nextStatus.login}.` : 'Signed in to GitHub.');
        closeSheetFor('github');
        return;
      }
      const flow = data as GitHubDeviceFlow;
      if (!flow.state || !flow.user_code) throw new Error('GitHub did not return a sign-in session.');
      setGithubDeviceFlow(flow);
      setGithubAuthMessage('Waiting for GitHub authorization…');
      const authUrl = flow.verification_uri_complete || flow.verification_uri;
      if (authUrl) {
        const opened = await openExternalUrl(authUrl);
        if (!opened) setGithubAuthMessage('Open the GitHub authorization link below to continue.');
      }
      pollGitHubAuth(flow.state, Number(flow.interval || 5));
    } catch (e: any) {
      const detail = e?.message || 'GitHub login failed';
      setGithubStatus((prev) => ({ ...(prev || {}), error: detail }));
      setGithubDeviceFlow(null);
      setGithubAuthMessage(detail);
      setGithubBusy(null);
      void refreshGitHubPrereq();
      toast.error(detail);
    }
  }, [clearGithubPoll, closeSheetFor, persistConnectionSlice, persistGithub, pollGitHubAuth, syncGitHubOAuthClient, refreshGitHubPrereq]);

  /** Cancel the GitHub device flow: stop polling, drop the pending code, clear busy. */
  const handleGitHubCancelAuth = React.useCallback(() => {
    clearGithubPoll();
    setGithubDeviceFlow(null);
    setGithubBusy(null);
    setGithubAuthMessage('GitHub sign-in cancelled.');
  }, [clearGithubPoll]);

  const handleGitHubDisconnect = React.useCallback(async () => {
    setGithubBusy('github-disconnect');
    setGithubAuthMessage(null);
    setGithubDeviceFlow(null);
    clearGithubPoll();
    try {
      const data = await apiJson('/api/integrations/github/auth/disconnect', { method: 'POST' });
      setGithubStatus(unwrapGitHubStatus(data));
      await persistGithub({
        enabled: false,
        readTools: false,
        lastCheckedAt: Date.now(),
        lastCheckOk: false,
        lastCheckMessage: 'Signed out',
        lastVerifiedFingerprint: null,
      });
      toast.success('GitHub signed out');
    } catch (e: any) {
      const detail = e?.message || 'GitHub sign-out failed';
      setGithubStatus((prev) => ({ ...(prev || {}), error: detail }));
      toast.error(detail);
    } finally {
      setGithubBusy(null);
    }
  }, [clearGithubPoll, persistGithub]);

  /** Edit-intent save: persist the GitHub slice + sync the OAuth client id. */
  const handleGitHubSaveEdit = React.useCallback(async () => {
    setGithubBusy('github-save');
    try {
      await persistConnectionSlice('github');
      await syncGitHubOAuthClient();
      toast.success('GitHub connection settings saved');
      closeSheetFor('github');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save GitHub settings');
    } finally {
      setGithubBusy(null);
    }
  }, [closeSheetFor, persistConnectionSlice, syncGitHubOAuthClient]);

  const pollSlackOAuth = React.useCallback((state: string, expiresAt: number, delaySeconds: number) => {
    clearSlackPoll();
    const delay = Math.max(1, delaySeconds || 2) * 1000;
    slackPollTimeoutRef.current = window.setTimeout(async () => {
      if (Date.now() > expiresAt) {
        clearSlackPoll();
        setSlackOAuthFlow(null);
        setSlackOAuthMessage('Timed out waiting for Slack authorization.');
        setSlackBusy(null);
        toast.error('Timed out waiting for Slack authorization.');
        return;
      }
      try {
        const data = await apiJson(`/api/integrations/slack/oauth/result?state=${encodeURIComponent(state)}`);
        const authError = slackOAuthError(data);
        if (authError) throw new Error(authError);
        if (isSlackOAuthComplete(data)) {
          clearSlackPoll();
          const nextStatus = unwrapSlackStatus(data);
          if (Object.keys(nextStatus).length > 0) setSlackStatus(nextStatus);
          void persistSlack({
            lastCheckedAt: Date.now(),
            lastCheckOk: true,
            lastCheckMessage: 'Connected',
            lastVerifiedFingerprint: configFingerprint(localSettingsRef.current, 'slack'),
          });
          setSlackOAuthFlow(null);
          setSlackOAuthMessage('Slack connected.');
          setSlackBusy(null);
          await refreshSlackStatus();
          toast.success('Slack connected');
          closeSheetFor('slack');
          return;
        }
        setSlackOAuthMessage('Waiting for Slack authorization…');
        pollSlackOAuth(state, expiresAt, Number(data?.interval || data?.interval_seconds || data?.poll_interval_seconds || delaySeconds || 2));
      } catch (e: any) {
        clearSlackPoll();
        const detail = e?.message || 'Slack authorization failed';
        setSlackStatus((prev) => ({ ...(prev || {}), error: detail }));
        setSlackOAuthFlow(null);
        setSlackOAuthMessage(detail);
        setSlackBusy(null);
        toast.error(detail);
      }
    }, delay);
  }, [clearSlackPoll, closeSheetFor, persistSlack, refreshSlackStatus]);

  const handleSlackSaveClientSecret = React.useCallback(async (clientSecret: string) => {
    const secret = clientSecret.trim();
    if (!secret) return;
    setSlackBusy('slack-client-secret');
    setSlackOAuthMessage(null);
    try {
      await apiJson('/api/secrets/set', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ providerId: 'slack', authType: 'client_secret', clientSecret: secret }),
      });
      await refreshSlackStatus();
      toast.success('Slack Client Secret saved');
    } catch (e: any) {
      const detail = e?.message || 'Failed to save Slack Client Secret';
      setSlackStatus((prev) => ({ ...(prev || {}), error: detail }));
      toast.error(detail);
    } finally {
      setSlackBusy(null);
    }
  }, [refreshSlackStatus]);

  const handleSlackClearClientSecret = React.useCallback(async () => {
    setSlackBusy('slack-client-secret');
    setSlackOAuthMessage(null);
    try {
      await apiJson('/api/secrets/clear', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ providerId: 'slack', authType: 'client_secret' }),
      });
      await refreshSlackStatus();
      toast.success('Slack Client Secret cleared');
    } catch (e: any) {
      const detail = e?.message || 'Failed to clear Slack Client Secret';
      setSlackStatus((prev) => ({ ...(prev || {}), error: detail }));
      toast.error(detail);
    } finally {
      setSlackBusy(null);
    }
  }, [refreshSlackStatus]);

  const handleSlackConnect = React.useCallback(async () => {
    setSlackBusy('slack-connect');
    setSlackOAuthMessage('Starting Slack authorization…');
    setSlackOAuthFlow(null);
    clearSlackPoll();
    try {
      await persistConnectionSlice('slack');
      const data = await apiJson('/api/integrations/slack/oauth/start', { method: 'POST' });
      const flow = data as SlackOAuthStart;
      if (!flow.authorize_url || !flow.state) throw new Error('Slack did not return an authorization session.');
      setSlackOAuthFlow(flow);
      setSlackOAuthMessage('Waiting for Slack authorization…');
      const opened = await openExternalUrl(flow.authorize_url);
      if (!opened) setSlackOAuthMessage('Open the Slack authorization link below to continue.');
      const expiresInSeconds = Number(flow.expires_in_seconds || 300);
      const expiresAt = Date.now() + Math.max(30, expiresInSeconds) * 1000;
      pollSlackOAuth(flow.state, expiresAt, 2);
    } catch (e: any) {
      const detail = e?.message || 'Slack authorization failed';
      setSlackStatus((prev) => ({ ...(prev || {}), error: detail }));
      setSlackOAuthFlow(null);
      setSlackOAuthMessage(detail);
      setSlackBusy(null);
      toast.error(detail);
    }
  }, [clearSlackPoll, persistConnectionSlice, pollSlackOAuth]);

  /** Cancel the Slack OAuth wait: stop polling, drop the flow, clear busy. */
  const handleSlackCancelAuth = React.useCallback(() => {
    clearSlackPoll();
    setSlackOAuthFlow(null);
    setSlackBusy(null);
    setSlackOAuthMessage('Slack authorization cancelled.');
  }, [clearSlackPoll]);

  const handleSlackDisconnect = React.useCallback(async () => {
    setSlackBusy('slack-disconnect');
    setSlackOAuthMessage(null);
    setSlackOAuthFlow(null);
    clearSlackPoll();
    try {
      const data = await apiJson('/api/integrations/slack/disconnect', { method: 'POST' });
      setSlackStatus(unwrapSlackStatus(data));
      await persistSlack({
        enabled: false,
        readTools: false,
        writeTools: false,
        lastCheckedAt: Date.now(),
        lastCheckOk: false,
        lastCheckMessage: 'Signed out',
        lastVerifiedFingerprint: null,
      });
      toast.success('Slack signed out');
    } catch (e: any) {
      const detail = e?.message || 'Slack sign-out failed';
      setSlackStatus((prev) => ({ ...(prev || {}), error: detail }));
      toast.error(detail);
    } finally {
      setSlackBusy(null);
    }
  }, [clearSlackPoll, persistSlack]);

  /** Edit-intent save: persist the Slack slice (client id + tool prefs). */
  const handleSlackSaveEdit = React.useCallback(async () => {
    setSlackBusy('slack-save');
    try {
      await persistConnectionSlice('slack');
      toast.success('Slack connection settings saved');
      closeSheetFor('slack');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save Slack settings');
    } finally {
      setSlackBusy(null);
    }
  }, [closeSheetFor, persistConnectionSlice]);

  // BigQuery -----------------------------------------------------------------
  const bqProjectId = (localSettings.googleProjectId || '').trim();
  const bqDetectedProject = (bqStatus?.detectedProject || '').trim();
  const bqEffectiveProjectId = bqProjectId || (!bqProjectTouchedRef.current ? bqDetectedProject : '');
  const bqDetectedProjectSource = (bqStatus?.detectedProjectSource || '').trim();
  const bqDetectedProjectIsQuota = bqDetectedProjectSource === 'adc.quota_project_id';
  const bqEffectiveProjectIsDetected = Boolean(bqDetectedProject && bqEffectiveProjectId === bqDetectedProject);
  const bqProjectSuggested = Boolean(bqDetectedProject && bqProjectId === bqDetectedProject && !bqProjectTouchedRef.current);
  const bqState = dataSourceState(localSettings, 'bigquery');
  const bqFingerprintOk = fingerprintMatchesConfig(bqState.lastVerifiedFingerprint, localSettings, 'bigquery');
  const bqValidated = bqState.lastCheckOk === true
    && Boolean(bqEffectiveProjectId)
    && (bqState.lastProjectId || '').trim() === bqEffectiveProjectId
    && bqFingerprintOk;
  // Passed its last check, but setup-relevant config changed since — the green
  // state is stale and the user must re-verify to apply the new config.
  const bqConfigChanged = bqState.lastCheckOk === true
    && Boolean(bqEffectiveProjectId)
    && (bqState.lastProjectId || '').trim() === bqEffectiveProjectId
    && !bqFingerprintOk;
  const bqReauthRunning = Boolean(bqReauth && (bqReauth.running || bqReauth.state === 'running'));
  const bqReauthFailed = Boolean(bqReauth && (bqReauth.state === 'gcloud_missing' || bqReauth.state === 'failed'));
  const bqVerifying = bqBusy || Boolean(bqReauthPhase);
  // A failed test whose message points at expired Google credentials means the
  // saved ADC login needs refreshing — the only real fix is re-running the
  // gcloud sign-in, so we surface a dedicated amber "Sign in again" affordance.
  const bqNeedsReauth = Boolean(
    bqStatus?.detected
    && !bqValidated
    && (
      bqReauthRunning
      || bqReauthFailed
      || isAdcReauthRequiredMessage(bqState.lastCheckMessage)
      || isAdcReauthRequiredMessage(bqFeedback?.text)
    ),
  );
  const bqFallbackCommand = (bqReauth?.fallbackCommand || ADC_REAUTH_FALLBACK_COMMAND).trim();
  const bqCustomPathConfigured = Boolean((localSettings.googleCredentialsPath || '').trim());

  // --- BigQuery scan budget (Advanced) --------------------------------------
  // Drafts hold raw text while typing; settings always hold a normalized pair
  // (approved ceiling >= effective automatic limit) so Save can never persist
  // an invalid policy. Blur reformats both fields from the stored values.
  const bqAutomaticGibText = bqScanDrafts.automatic ?? bytesToGibInput(bqState.automaticScanBytes);
  const bqApprovedGibText = bqScanDrafts.approved ?? bytesToGibInput(bqState.approvedScanBytes);
  const bqEffectiveAutomaticBytes = effectiveAutomaticScanBytes(bqState);
  const bqScanInputInvalid =
    (bqScanDrafts.automatic !== null && parseGibInput(bqScanDrafts.automatic) === null)
    || (bqScanDrafts.approved !== null && parseGibInput(bqScanDrafts.approved) === null);
  const bqApprovedDraftBytes = bqScanDrafts.approved !== null ? parseGibInput(bqScanDrafts.approved) : undefined;
  const bqApprovedBelowAutomatic =
    typeof bqApprovedDraftBytes === 'number' && bqApprovedDraftBytes < bqEffectiveAutomaticBytes;

  const applyBigQueryScanBudget = React.useCallback((field: 'automatic' | 'approved', bytes: number | undefined) => {
    setLocalSettings((prev) => {
      const state = dataSourceState(prev, 'bigquery');
      const automatic = field === 'automatic' ? bytes : state.automaticScanBytes;
      const approved = field === 'approved' ? bytes : state.approvedScanBytes;
      const { automaticScanBytes: _prevAutomatic, approvedScanBytes: _prevApproved, ...rest } = state;
      return {
        ...prev,
        dataSources: {
          ...prev.dataSources,
          bigquery: { ...rest, ...normalizeScanBudget(automatic, approved) },
        },
      };
    });
  }, [setLocalSettings]);

  // Opening/closing any sheet resets in-progress scan-budget text (discard
  // restores the slice; the drafts must not survive to contradict it).
  React.useEffect(() => {
    setBqScanDrafts({ automatic: null, approved: null });
  }, [sheetOpenId]);

  React.useEffect(() => {
    if (!bqDetectedProject || bqProjectId || bqProjectTouchedRef.current) return;
    setLocalSettings((prev) => {
      if ((prev.googleProjectId || '').trim()) return prev;
      return { ...prev, googleProjectId: bqDetectedProject };
    });
  }, [bqDetectedProject, bqProjectId, setLocalSettings]);

  const saveBigQueryConnectionState = React.useCallback(async (projectId: string, patch: Partial<DataSourceState>) => {
    const cleanProjectId = projectId.trim();
    const prev = localSettingsRef.current;
    const merged: DataSourceState = { ...dataSourceState(prev, 'bigquery'), ...patch };
    const next: AppSettings = {
      ...prev,
      googleProjectId: cleanProjectId,
      dataSources: { ...prev.dataSources, bigquery: merged },
    };
    if (typeof patch.lastCheckOk === 'boolean') {
      merged.lastVerifiedFingerprint = patch.lastCheckOk ? configFingerprint(next, 'bigquery') : null;
    }
    setLocalSettings(next);
    localSettingsRef.current = next;
    await commitConnectionSlice('bigquery', next);
    if (sheetRef.current?.id === 'bigquery') sheetSnapshotRef.current = next;
  }, [commitConnectionSlice, setLocalSettings]);

  const handleBigQueryConnect = React.useCallback(async () => {
    setBqBusy(true);
    setBqFeedback(null);
    try {
      await persistConnectionSlice('bigquery');
      const refreshedStatus = await refreshBigQuery();
      const refreshedDetectedProject = (refreshedStatus?.detectedProject || '').trim();
      const projectToTest = bqProjectId || (!bqProjectTouchedRef.current ? refreshedDetectedProject : '') || bqEffectiveProjectId;
      if (!projectToTest) {
        throw new Error('Add a Google Cloud project ID to continue.');
      }
      const result = await testBigQueryConnection(projectToTest);
      const verifiedProject = result.projectId || projectToTest;
      await saveBigQueryConnectionState(verifiedProject, {
        lastCheckedAt: Date.now(),
        lastCheckOk: result.ok,
        lastProjectId: verifiedProject || null,
        lastCheckMessage: result.ok ? 'BigQuery connection verified.' : 'BigQuery connection failed.',
      });
      if (result.ok) {
        setBqFeedback(null);
        toast.success(`Connected to ${verifiedProject}`);
        closeSheetFor('bigquery');
      } else {
        setBqFeedback({ text: 'Could not reach BigQuery with the current ADC credentials.', error: true });
        toast.error('BigQuery connection failed.');
      }
    } catch (e: any) {
      await persistDataSource('bigquery', {
        lastCheckedAt: Date.now(),
        lastCheckOk: false,
        lastCheckMessage: e?.message || 'BigQuery connection failed.',
      });
      setBqFeedback({ text: e?.message || 'BigQuery connection failed.', error: true });
      toast.error(e?.message || 'BigQuery connection failed.');
    } finally {
      setBqBusy(false);
    }
  }, [bqEffectiveProjectId, bqProjectId, closeSheetFor, persistConnectionSlice, persistDataSource, refreshBigQuery, saveBigQueryConnectionState]);

  const handleBigQueryToggle = React.useCallback(async (enabled: boolean) => {
    if (bqBusy || bqReauthRunning || bqReauthPhase) return;
    if (enabled && !bqValidated) {
      toast.error('Verify the BigQuery connection before enabling model access.');
      return;
    }
    await persistDataSource('bigquery', { enabled });
  }, [bqBusy, bqReauthRunning, bqReauthPhase, bqValidated, persistDataSource]);

  // Polls the backend reauth snapshot on a chained timer. `generation` guards
  // against a slow response landing after the user cancelled or a new run began.
  const pollAdcReauth = React.useCallback((generation: number, expiresAt: number) => {
    bqReauthPollRef.current = window.setTimeout(async () => {
      if (generation !== bqReauthGenRef.current) return;
      if (Date.now() > expiresAt) {
        clearAdcReauthPoll();
        try { await cancelAdcReauth(); } catch { /* best effort */ }
        setBqReauth((prev) => (prev ? { ...prev, state: 'failed', running: false } : prev));
        setBqReauthPhase(null);
        setBqBusy(false);
        setBqFeedback({ text: 'Timed out waiting for Google sign-in. Try again, or run the command below in a terminal.', error: true });
        toast.error('Timed out waiting for Google sign-in.');
        return;
      }
      let snap: NormalizedAdcReauth;
      try {
        snap = await getAdcReauthStatus();
      } catch {
        if (generation !== bqReauthGenRef.current) return;
        pollAdcReauth(generation, expiresAt);
        return;
      }
      if (generation !== bqReauthGenRef.current) return;
      setBqReauth(snap);
      if (snap.running || snap.state === 'running') {
        setBqReauthPhase('Waiting for Google sign-in in your browser…');
        pollAdcReauth(generation, expiresAt);
        return;
      }
      clearAdcReauthPoll();
      if (snap.state === 'succeeded') {
        setBqReauthPhase('Verifying BigQuery connection…');
        toast.success('Signed in to Google');
        try {
          await handleBigQueryConnect();
        } finally {
          setBqReauthPhase(null);
          setBqReauth(null);
        }
        return;
      }
      if (snap.state === 'cancelled') {
        setBqReauthPhase(null);
        setBqBusy(false);
        setBqReauth(null);
        setBqFeedback({ text: 'Google sign-in cancelled.', error: false });
        return;
      }
      // failed | gcloud_missing — keep the snapshot so the fallback command shows.
      setBqReauthPhase(null);
      setBqBusy(false);
      const detail = snap.message || snap.error || 'Google sign-in did not complete.';
      setBqFeedback({ text: detail, error: true });
      toast.error(detail);
    }, 1500);
  }, [clearAdcReauthPoll, handleBigQueryConnect]);

  const handleAdcReauthStart = React.useCallback(async () => {
    clearAdcReauthPoll();
    const generation = bqReauthGenRef.current;
    setBqFallbackCopied(false);
    setBqFeedback(null);
    setBqReauthPhase('Opening Google sign-in…');
    setBqBusy(true);
    try {
      const snap = await startAdcReauth();
      if (generation !== bqReauthGenRef.current) return;
      setBqReauth(snap);
      if (snap.running || snap.state === 'running') {
        setBqReauthPhase('Waiting for Google sign-in in your browser…');
        const expiresAt = Date.now() + 5 * 60 * 1000;
        pollAdcReauth(generation, expiresAt);
        return;
      }
      clearAdcReauthPoll();
      if (snap.state === 'succeeded') {
        setBqReauthPhase('Verifying BigQuery connection…');
        try {
          await handleBigQueryConnect();
        } finally {
          setBqReauthPhase(null);
          setBqReauth(null);
        }
        return;
      }
      // gcloud_missing | failed | idle — surface the fallback command and stop.
      setBqReauthPhase(null);
      setBqBusy(false);
      const detail = snap.message || snap.error || 'Could not start Google sign-in.';
      setBqFeedback({ text: detail, error: true });
      if (snap.state !== 'idle') toast.error(detail);
    } catch (e: any) {
      if (generation !== bqReauthGenRef.current) return;
      clearAdcReauthPoll();
      setBqReauth(null);
      setBqReauthPhase(null);
      setBqBusy(false);
      const detail = e?.message || 'Could not start Google sign-in.';
      setBqFeedback({ text: detail, error: true });
      toast.error(detail);
    }
  }, [clearAdcReauthPoll, handleBigQueryConnect, pollAdcReauth]);

  const handleAdcReauthCancel = React.useCallback(async () => {
    clearAdcReauthPoll();
    setBqReauthPhase(null);
    setBqBusy(false);
    setBqReauth(null);
    setBqFeedback({ text: 'Google sign-in cancelled.', error: false });
    try { await cancelAdcReauth(); } catch { /* best effort */ }
  }, [clearAdcReauthPoll]);

  const handleCopyFallbackCommand = React.useCallback(async (command: string) => {
    try {
      if (navigator?.clipboard?.writeText) {
        await navigator.clipboard.writeText(command);
        setBqFallbackCopied(true);
        window.setTimeout(() => setBqFallbackCopied(false), 2000);
      }
    } catch {
      /* clipboard unavailable — the command stays selectable for manual copy */
    }
  }, []);

  // Databricks ---------------------------------------------------------------
  const dbHost = (localSettings.databricksHost || '').trim();
  const dbWarehouse = (localSettings.databricksWarehouseId || '').trim();
  const dbOAuthClientId = (localSettings.databricksOAuthClientId || '').trim();
  const dbHostIsGcp = isGcpDatabricksHost(dbHost);
  const dbState = dataSourceState(localSettings, 'databricks');
  const dbHasCredential = dbRefreshSet || dbPatSet;
  const dbFingerprintOk = fingerprintMatchesConfig(dbState.lastVerifiedFingerprint, localSettings, 'databricks');
  const dbValidated = dbState.lastCheckOk === true && dbFingerprintOk;
  const dbConfigChanged = dbState.lastCheckOk === true && !dbFingerprintOk;

  const postSecret = React.useCallback(async (path: string, body: any) => {
    const res = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      throw new Error(text || `HTTP ${res.status}`);
    }
  }, []);

  const handleDatabricksTest = React.useCallback(async () => {
    setDbBusy(true);
    setDbFeedback(null);
    try {
      await persistConnectionSlice('databricks');
      const result = await testDatabricksConnection({ host: dbHost, warehouseId: dbWarehouse });
      await persistDataSource('databricks', {
        lastCheckedAt: Date.now(),
        lastCheckOk: result.ok,
        lastHost: dbHost || null,
        lastWarehouseId: dbWarehouse || null,
        lastCheckMessage: result.message || (result.ok ? 'Databricks connection verified.' : 'Databricks connection failed.'),
      });
      if (result.ok) {
        setDbFeedback(null);
        toast.success(result.message || 'Databricks connection verified.');
        closeSheetFor('databricks');
      } else {
        setDbFeedback({ text: result.message || 'Databricks connection failed.', error: true });
        toast.error(result.message || 'Databricks connection failed.');
      }
      return result.ok;
    } catch (e: any) {
      await persistDataSource('databricks', {
        lastCheckedAt: Date.now(),
        lastCheckOk: false,
        lastCheckMessage: e?.message || 'Databricks connection failed.',
      });
      setDbFeedback({ text: e?.message || 'Databricks connection failed.', error: true });
      toast.error(e?.message || 'Databricks connection failed.');
      return false;
    } finally {
      setDbBusy(false);
    }
  }, [closeSheetFor, dbHost, dbWarehouse, persistConnectionSlice, persistDataSource]);

  const submitOAuthCode = React.useCallback(async (state: string, code: string, source: 'manual' | 'auto') => {
    const stateTrimmed = state.trim();
    const codeTrimmed = code.trim();
    if (!stateTrimmed || !codeTrimmed || oauthCodeInFlightRef.current) return false;
    oauthCodeInFlightRef.current = true;
    try {
      const result = await completeDatabricksOAuth({ state: stateTrimmed, code: codeTrimmed });
      if (!result.ok && result.status !== 'success') {
        throw new Error(result.error || result.message || 'Databricks OAuth authorization failed.');
      }
      setManualOAuthInput('');
      setPendingOAuthUrl('');
      if (source === 'manual') toast.success('Authorization code accepted.');
      return true;
    } catch (e: any) {
      toast.error(e?.message || 'Failed to complete OAuth with authorization code');
      return false;
    } finally {
      oauthCodeInFlightRef.current = false;
    }
  }, []);

  const handleDatabricksOAuth = React.useCallback(async () => {
    if (!dbHost) { toast.error('Databricks host is required before starting OAuth.'); return; }
    if (!dbHostIsGcp) { toast.error('Databricks host must be a GCP workspace URL (*.gcp.databricks.com).'); return; }
    setDbBusy(true);
    setDbFeedback({ text: 'Opening Databricks authorization…', error: false });
    setPendingOAuthState('');
    setPendingOAuthUrl('');
    setManualOAuthInput('');
    lastAutoCodeKeyRef.current = '';
    oauthCodeInFlightRef.current = false;
    try {
      await persistConnectionSlice('databricks');
      const started = await startDatabricksOAuth({ host: dbHost, clientId: dbOAuthClientId || undefined });
      setPendingOAuthState(started.state);
      setPendingOAuthUrl(started.authorizeUrl);
      const opened = await openExternalUrl(started.authorizeUrl);
      setDbFeedback({
        text: opened
          ? 'Complete Databricks authorization in your browser.'
          : 'Open the Databricks authorization link below to continue.',
        error: false,
      });
      const timeoutMs = Math.max(started.expiresInSeconds * 1000, 2 * 60 * 1000);
      const deadline = Date.now() + timeoutMs;
      while (Date.now() < deadline) {
        await new Promise((resolve) => window.setTimeout(resolve, 1250));
        let result: Awaited<ReturnType<typeof getDatabricksOAuthResult>> | null = null;
        try {
          result = await getDatabricksOAuthResult(started.state);
        } catch (pollErr: any) {
          if (Number(pollErr?.status ?? NaN) === 404) continue;
          throw pollErr;
        }
        if (!result || result.status === 'pending') continue;
        if (result.ok || result.status === 'success') {
          setDbAuthMode('refresh_token');
          setDbRefreshSet(true);
          setDbPatSet(false);
          setPendingOAuthState('');
          setPendingOAuthUrl('');
          await refreshDatabricks();
          toast.success(result.message || 'Databricks refresh token generated.');
          setDbFeedback({ text: 'Credential generated. Verifying connection…', error: false });
          await handleDatabricksTest();
          return;
        }
        throw new Error(result.error || result.message || 'Databricks OAuth authorization failed.');
      }
      throw new Error('Timed out waiting for Databricks OAuth authorization to complete.');
    } catch (e: any) {
      setDbFeedback({ text: e?.message || 'Failed to generate Databricks credential.', error: true });
      toast.error(e?.message || 'Failed to generate Databricks credential.');
    } finally {
      setDbBusy(false);
    }
  }, [dbHost, dbHostIsGcp, dbOAuthClientId, persistConnectionSlice, refreshDatabricks, handleDatabricksTest]);

  const handleDatabricksConnect = React.useCallback(() => {
    if (dbAuthMode === 'refresh_token') {
      void handleDatabricksOAuth();
    } else {
      void handleDatabricksTest();
    }
  }, [dbAuthMode, handleDatabricksOAuth, handleDatabricksTest]);

  const handleSetDatabricksSecret = React.useCallback(async (authType: 'refresh_token' | 'pat', value: string) => {
    try {
      await postSecret('/api/secrets/set', authType === 'refresh_token'
        ? { providerId: 'databricks', authType: 'refresh_token', refreshToken: value, ...(dbOAuthClientId ? { clientId: dbOAuthClientId } : {}) }
        : { providerId: 'databricks', authType: 'pat', apiKey: value });
      if (authType === 'refresh_token') setDbRefreshSet(true); else setDbPatSet(true);
      toast.success(`Databricks ${authType === 'refresh_token' ? 'refresh token' : 'PAT'} saved.`);
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save credential');
    }
  }, [dbOAuthClientId, postSecret]);

  const handleClearDatabricksSecret = React.useCallback(async (authType: 'refresh_token' | 'pat') => {
    try {
      await postSecret('/api/secrets/clear', { providerId: 'databricks', authType });
      if (authType === 'refresh_token') setDbRefreshSet(false); else setDbPatSet(false);
      toast.success(`Databricks ${authType === 'refresh_token' ? 'refresh token' : 'PAT'} cleared.`);
    } catch (e: any) {
      toast.error(e?.message || 'Failed to clear credential');
    }
  }, [postSecret]);

  const handleDatabricksToggle = React.useCallback(async (enabled: boolean) => {
    if (enabled && !dbValidated) {
      toast.error('Verify the Databricks connection before enabling SQL tools.');
      return;
    }
    await persistDataSource('databricks', { enabled });
  }, [dbValidated, persistDataSource]);

  // Trino --------------------------------------------------------------------
  const trinoHost = (localSettings.trinoHost || '').trim();
  const trinoCatalog = (localSettings.trinoCatalog || '').trim();
  const trinoSchema = (localSettings.trinoSchema || '').trim();
  const trinoUser = (localSettings.trinoUser || '').trim();
  const trinoSource = (localSettings.trinoSource || '').trim();
  const trinoState = dataSourceState(localSettings, 'trino');
  const trinoFingerprintOk = fingerprintMatchesConfig(trinoState.lastVerifiedFingerprint, localSettings, 'trino');
  const trinoValidated = trinoState.lastCheckOk === true && trinoFingerprintOk;
  const trinoConfigChanged = trinoState.lastCheckOk === true && !trinoFingerprintOk;

  const handleTrinoConnect = React.useCallback(async () => {
    setTrinoBusy(true);
    setTrinoFeedback(null);
    try {
      if (trinoAuthMode === 'none') {
        await postSecret('/api/secrets/set', { providerId: 'trino', authType: 'none' });
        setTrinoBasicSet(false);
        setTrinoJwtSet(false);
      }
      await persistConnectionSlice('trino');
      const result = await testTrinoConnection({ host: trinoHost, catalog: trinoCatalog, schema: trinoSchema, source: trinoSource });
      await persistDataSource('trino', {
        lastCheckedAt: Date.now(),
        lastCheckOk: result.ok,
        lastHost: trinoHost || null,
        lastCheckMessage: result.message || (result.ok ? 'Trino connection verified.' : 'Trino connection failed.'),
      });
      if (result.ok) {
        setTrinoFeedback(null);
        toast.success(result.message || 'Trino connection verified.');
        closeSheetFor('trino');
      } else {
        setTrinoFeedback({ text: result.message || 'Trino connection failed.', error: true });
        toast.error(result.message || 'Trino connection failed.');
      }
    } catch (e: any) {
      await persistDataSource('trino', {
        lastCheckedAt: Date.now(),
        lastCheckOk: false,
        lastCheckMessage: e?.message || 'Trino connection failed.',
      });
      setTrinoFeedback({ text: e?.message || 'Trino connection failed.', error: true });
      toast.error(e?.message || 'Trino connection failed.');
    } finally {
      setTrinoBusy(false);
    }
  }, [closeSheetFor, persistConnectionSlice, persistDataSource, postSecret, trinoAuthMode, trinoCatalog, trinoHost, trinoSchema, trinoSource]);

  const handleSetTrinoBasic = React.useCallback(async (value: string) => {
    if (!trinoUser) { toast.error('Enter the Trino user before saving a Basic password.'); return; }
    try {
      await postSecret('/api/secrets/set', { providerId: 'trino', authType: 'basic', username: trinoUser, password: value });
      setTrinoBasicSet(true);
      toast.success('Trino basic password saved.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save Trino basic password');
    }
  }, [postSecret, trinoUser]);

  const handleSetTrinoJwt = React.useCallback(async (value: string) => {
    try {
      await postSecret('/api/secrets/set', { providerId: 'trino', authType: 'jwt', jwt: value, token: value });
      setTrinoJwtSet(true);
      toast.success('Trino JWT token saved.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save Trino JWT token');
    }
  }, [postSecret]);

  const handleClearTrinoSecret = React.useCallback(async (authType: 'basic' | 'jwt') => {
    try {
      await postSecret('/api/secrets/clear', { providerId: 'trino', authType });
      if (authType === 'basic') setTrinoBasicSet(false); else setTrinoJwtSet(false);
      toast.success(`Trino ${authType === 'basic' ? 'basic password' : 'JWT token'} cleared.`);
    } catch (e: any) {
      toast.error(e?.message || 'Failed to clear Trino credential');
    }
  }, [postSecret]);

  const handleTrinoToggle = React.useCallback(async (enabled: boolean) => {
    if (enabled && !trinoValidated) {
      toast.error('Verify the Trino connection before enabling SQL tools.');
      return;
    }
    await persistDataSource('trino', { enabled });
  }, [persistDataSource, trinoValidated]);

  // Snowflake ----------------------------------------------------------------
  const snowflakeAccount = (localSettings.snowflakeAccountIdentifier || '').trim();
  const snowflakeWarehouse = (localSettings.snowflakeWarehouse || '').trim();
  const snowflakeDatabase = (localSettings.snowflakeDatabase || '').trim();
  const snowflakeSchema = (localSettings.snowflakeSchema || '').trim();
  const snowflakeRole = (localSettings.snowflakeRole || '').trim();
  const snowflakeUser = (localSettings.snowflakeUser || '').trim();
  const snowflakeState = dataSourceState(localSettings, 'snowflake');
  const snowflakeFingerprintOk = fingerprintMatchesConfig(snowflakeState.lastVerifiedFingerprint, localSettings, 'snowflake');
  const snowflakeValidated = snowflakeState.lastCheckOk === true && snowflakeFingerprintOk;
  const snowflakeConfigChanged = snowflakeState.lastCheckOk === true && !snowflakeFingerprintOk;

  const handleSnowflakeConnect = React.useCallback(async () => {
    setSnowflakeBusy(true);
    setSnowflakeFeedback(null);
    try {
      await persistConnectionSlice('snowflake');
      const result = await testSnowflakeConnection({
        accountIdentifier: snowflakeAccount,
        warehouse: snowflakeWarehouse,
        database: snowflakeDatabase,
        schema: snowflakeSchema,
        role: snowflakeRole,
        user: snowflakeUser,
        authMode: snowflakeAuthMode,
      });
      await persistDataSource('snowflake', {
        lastCheckedAt: Date.now(),
        lastCheckOk: result.ok,
        lastHost: snowflakeAccount || null,
        lastWarehouseId: snowflakeWarehouse || null,
        lastProjectId: snowflakeDatabase || null,
        lastCheckMessage: result.message || (result.ok ? 'Snowflake connection verified.' : 'Snowflake connection failed.'),
      });
      if (result.ok) {
        setSnowflakeFeedback(null);
        toast.success(result.message || 'Snowflake connection verified.');
        closeSheetFor('snowflake');
      } else {
        setSnowflakeFeedback({ text: result.message || 'Snowflake connection failed.', error: true });
        toast.error(result.message || 'Snowflake connection failed.');
      }
    } catch (e: any) {
      await persistDataSource('snowflake', {
        lastCheckedAt: Date.now(),
        lastCheckOk: false,
        lastCheckMessage: e?.message || 'Snowflake connection failed.',
      });
      setSnowflakeFeedback({ text: e?.message || 'Snowflake connection failed.', error: true });
      toast.error(e?.message || 'Snowflake connection failed.');
    } finally {
      setSnowflakeBusy(false);
    }
  }, [closeSheetFor, persistConnectionSlice, persistDataSource, snowflakeAccount, snowflakeAuthMode, snowflakeDatabase, snowflakeRole, snowflakeSchema, snowflakeUser, snowflakeWarehouse]);

  const handleSetSnowflakeOAuth = React.useCallback(async (value: string) => {
    try {
      await setSnowflakeOAuthToken(value);
      setSnowflakeOAuthSet(true);
      toast.success('Snowflake OAuth token saved.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save Snowflake OAuth token');
    }
  }, []);

  const handleSetSnowflakeKeyPair = React.useCallback(async (value: string) => {
    try {
      await setSnowflakeKeyPair(value, snowflakeKeyPassphrase);
      setSnowflakeKeyPairSet(true);
      // The passphrase is only needed to store the key; never keep it in
      // component state (or the DOM) once the secret is saved.
      setSnowflakeKeyPassphrase('');
      toast.success('Snowflake keypair secret saved.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save Snowflake keypair secret');
    }
  }, [snowflakeKeyPassphrase]);

  const handleClearSnowflakeSecret = React.useCallback(async () => {
    try {
      await clearSnowflakeSecrets();
      setSnowflakeOAuthSet(false);
      setSnowflakeKeyPairSet(false);
      setSnowflakeKeyPassphrase('');
      toast.success('Snowflake credentials cleared.');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to clear Snowflake credentials');
    }
  }, []);

  const handleSnowflakeToggle = React.useCallback(async (enabled: boolean) => {
    if (enabled && !snowflakeValidated) {
      toast.error('Verify the Snowflake connection before enabling SQL tools.');
      return;
    }
    await persistDataSource('snowflake', { enabled });
  }, [persistDataSource, snowflakeValidated]);

  React.useEffect(() => {
    const onMessage = (event: MessageEvent) => {
      if (!pendingOAuthState.trim()) return;
      // The OAuth callback page posting the code is served either by this app
      // or by the local bridge on loopback; reject codes from anywhere else.
      // The state check below still applies on top of this.
      try {
        const origin = new URL(event.origin);
        const isLoopback = origin.hostname === 'localhost' || origin.hostname === '127.0.0.1' || origin.hostname === '[::1]' || origin.hostname === '::1';
        if (event.origin !== window.location.origin && !isLoopback) return;
      } catch {
        return;
      }
      const data = event.data;
      if (!data || typeof data !== 'object') return;
      const payload = data as Record<string, unknown>;
      if (String(payload.type || '').trim() !== 'databricks-oauth-code') return;
      const msgState = String(payload.state || '').trim();
      if (!msgState || msgState !== pendingOAuthState.trim()) return;
      const msgError = String(payload.error || '').trim();
      if (msgError) { toast.error(`Databricks authorization failed: ${msgError}`); return; }
      const msgCode = String(payload.code || '').trim();
      if (!msgCode) return;
      const key = `${msgState}|${msgCode}`;
      if (lastAutoCodeKeyRef.current === key) return;
      lastAutoCodeKeyRef.current = key;
      setManualOAuthInput(msgCode);
      void submitOAuthCode(msgState, msgCode, 'auto');
    };
    window.addEventListener('message', onMessage);
    return () => window.removeEventListener('message', onMessage);
  }, [pendingOAuthState, submitOAuthCode]);

  // --- Derived per-connection context ---------------------------------------
  const githubAccount = githubStatus?.login || githubStatus?.profile?.github_login || '';
  const githubConnected = Boolean(githubStatus?.connected);
  const githubModelAccess = github.enabled === true && github.readTools === true;
  const githubAuthenticating = githubBusy === 'github-connect' || Boolean(githubDeviceFlow);
  const githubError = githubStatus?.error || githubStatus?.profile?.github_error;
  const githubLastCheckOk = typeof githubStatus?.lastCheckOk === 'boolean' ? githubStatus.lastCheckOk : github.lastCheckOk;
  const githubLastCheckedAt = Number(githubStatus?.lastCheckedAt || github.lastCheckedAt || 0) || undefined;
  const githubNeedsReconnect = githubConnected && githubLastCheckOk === false;
  const githubConfigChanged = githubConnected && !fingerprintMatchesConfig(github.lastVerifiedFingerprint, localSettings, 'github');

  const slackTeamName = slackStatus?.team_name || slackStatus?.team?.name || slackStatus?.workspace?.name;
  const slackWorkspace = String(slackTeamName || slackStatus?.team_id || '').trim();
  const slackClientConfigured = Boolean(slackStatus?.client_id_configured) || Boolean(slackOAuthClientId.trim());
  const slackConnected = Boolean(slackStatus?.connected);
  const slackModelAccess = slack.enabled === true && (slack.readTools === true || slack.writeTools === true);
  const slackAuthenticating = slackBusy === 'slack-connect' || Boolean(slackOAuthFlow);
  const slackLastCheckOk = typeof slackStatus?.lastCheckOk === 'boolean' ? slackStatus.lastCheckOk : slack.lastCheckOk;
  const slackLastCheckedAt = Number(slackStatus?.lastCheckedAt || slack.lastCheckedAt || 0) || undefined;
  const slackNeedsReconnect = slackConnected && slackLastCheckOk === false;
  const slackConfigChanged = slackConnected && !fingerprintMatchesConfig(slack.lastVerifiedFingerprint, localSettings, 'slack');

  const jiraProduct = status?.products?.jira;
  const confluenceProduct = status?.products?.confluence;
  const atlassianToolsEnabled = atlassian.jira?.enabled === true || atlassian.confluence?.enabled === true;
  const atlassianAutoState = String(status?.autoLogin?.state || '').toLowerCase();
  const atlassianConnected = productIsConnected(jiraProduct) || productIsConnected(confluenceProduct) || atlassianAutoState === 'hidden';
  const atlassianAuthenticating = busy === 'login' || busy === 'finish' || status?.mode === 'login' || ['watching', 'switching'].includes(atlassianAutoState);
  const atlassianHasJiraUrl = Boolean(hostLabel(atlassian.jiraBaseUrl));
  const atlassianProfile = jiraProduct?.profile || confluenceProduct?.profile;
  const atlassianSite = compactText(hostLabel(jiraProduct?.url), hostLabel(atlassian.jiraBaseUrl), atlassianConnected ? 'Atlassian Cloud' : 'Atlassian');
  const atlassianCredential = compactText(atlassianProfile?.emailAddress, atlassianProfile?.displayName, atlassianProfile?.accountId, atlassianConnected ? 'Browser SSO profile' : 'Browser SSO');
  const atlassianLastCheckedAt = Math.max(Number(atlassian.jira?.lastCheckedAt || 0), Number(atlassian.confluence?.lastCheckedAt || 0)) || undefined;
  const atlassianNeedsReconnect = atlassianConnected && (atlassian.jira?.lastCheckOk === false || atlassian.confluence?.lastCheckOk === false);
  const atlassianConfigChanged = atlassianConnected && !fingerprintMatchesConfig(atlassian.lastVerifiedFingerprint, localSettings, 'atlassian');

  const sharePointSiteStatus = sharePointStatus?.site;
  const sharePointAutoState = String(sharePointStatus?.autoLogin?.state || '').toLowerCase();
  const sharePointConnected = productIsConnected(sharePointSiteStatus) || sharePointAutoState === 'hidden';
  const sharePointAuthenticating = sharePointBusy === 'login' || sharePointBusy === 'finish' || sharePointStatus?.mode === 'login' || ['watching', 'switching'].includes(sharePointAutoState);
  const sharePointHasUrl = Boolean(hostLabel(sharepoint.siteUrl));
  const sharePointToolsEnabled = sharepoint.enabled === true;
  const sharePointSite = compactText(hostLabel(sharePointSiteStatus?.url), hostLabel(sharepoint.siteUrl), sharePointConnected ? 'SharePoint' : 'SharePoint');
  const sharePointCredential = sharePointConnected ? 'Browser SSO profile' : 'Browser SSO';
  const sharePointLastCheckedAt = Number(sharepoint.lastCheckedAt || 0) || undefined;
  const sharePointNeedsReconnect = sharePointConnected && sharepoint.lastCheckOk === false;
  const sharePointConfigChanged = sharePointConnected && !fingerprintMatchesConfig(sharepoint.lastVerifiedFingerprint, localSettings, 'sharepoint');

  // Surface C confirmation block — read-only Account / Credential (+ Reconnect
  // link that re-opens Surface B) / Status (auth health with a green dot). This
  // is the only place auth health is surfaced inside the drawer.
  const confirmBlock = (
    id: ConnectionCatalogId,
    account: React.ReactNode,
    credentialLabel: React.ReactNode,
    statusText: React.ReactNode,
    options?: { accountLabel?: string; reconnect?: boolean; checkedAt?: number | null },
  ) => {
    const checked = options?.checkedAt ? relativeTime(options.checkedAt) : '';
    return (
      <>
        <div className="cx-meta-row">
          <span className="cx-k">{options?.accountLabel || 'Account'}</span>
          <span className="cx-v" title={typeof account === 'string' ? account : undefined}>{account}</span>
        </div>
        <div className="cx-meta-row">
          <span className="cx-k">Credential</span>
          <span className="cx-v">
            <span className="cx-v-text" title={typeof credentialLabel === 'string' ? credentialLabel : undefined}>{credentialLabel}</span>
            {options?.reconnect === false ? null : (
              <button type="button" className="cx-reconnect" onClick={() => openSheet(id, 'reconnect')}>Reconnect</button>
            )}
          </span>
        </div>
        <div className="cx-meta-row">
          <span className="cx-k">Status</span>
          <span className="cx-v"><span className="cx-gd" />{statusText}{checked ? ` · checked ${checked}` : ''}</span>
        </div>
      </>
    );
  };

    // For window-driven connections, the body/footer rendered inside the setup window.
  interface View extends Omit<RowProps, 'index' | 'onRemove'> {
    sheetEyebrow?: string;
    sheetBody?: React.ReactNode;
    sheetFooter?: React.ReactNode;
  }

  // Default eyebrow per intent; connections override for flow-specific wording
  // ("Sign in", "Sign in again", "Paper library").
  const intentEyebrow = (intent: SheetIntent | null): string => (
    intent === 'edit' ? 'Edit connection' : intent === 'reconnect' ? 'Reconnect' : 'Set up connection'
  );

  /**
   * `sheetIntent` is non-null only for the connection whose setup window is
   * open; it selects the window's eyebrow, footer label and primary action.
   */
  const deriveView = (id: ConnectionCatalogId, sheetIntent: SheetIntent | null = null): View => {
    if (id === 'thread-search') {
      const on = threadSearch.enabled === true;
      const stage: Stage = on ? 'active' : 'enable';
      return {
        id, name: 'Search Threads', stage, tone: on ? 'live' : 'default',
        eyebrow: on ? 'Active' : 'Ready',
        instruction: on
          ? <>Local conversation search is available to this chat when armed.</>
          : <>Local app history, no account needed — <b>enable</b> to let the AI search prior threads.</>,
        toggleOn: on,
        toggleLabel: on ? 'Disable Search Threads for the AI' : 'Enable Search Threads for the AI',
        onToggle: (v) => { void persistThreadSearch({ enabled: v }); },
        hasDetails: true,
        detailsNode: (
          <>
            {confirmBlock(
              'thread-search',
              'Local app history',
              'No credential required',
              on ? 'Ready' : 'Disabled',
              { accountLabel: 'Scope', reconnect: false },
            )}
            <div className="cx-sec">Tools</div>
            <div className="cx-tools">
              <ToolRow
                name="Search and retrieve threads"
                desc="Search prior conversations, fetch indexed documents and list thread messages."
                checked={on}
                disabled={!on}
                label="Search Threads tools"
                onChange={(v) => { void persistThreadSearch({ enabled: v }); }}
              />
            </div>
          </>
        ),
      };
    }

    if (id === 'image-generation') {
      const configured = Boolean(imageProviderTarget && String(imageGeneration.model || '').trim());
      const on = imageGeneration.enabled === true && configured;
      const stage: Stage = on ? 'active' : configured ? 'enable' : 'setup';
      const intent = sheetIntent ?? (stage === 'setup' ? 'setup' : 'edit');
      const defaults = imageGeneration.defaults || {};
      const quality = defaults.quality || 'auto';
      const size = defaults.size || 'auto';
      const format = defaults.format || 'png';
      return {
        id,
        name: 'Image Generation',
        stage,
        tone: on ? 'live' : 'default',
        eyebrow: on ? 'Active' : stage === 'setup' ? 'Setup required' : 'Ready',
        instruction: on
          ? <>Creating image files with <b>{imageGeneration.model || 'gpt-image-2'}</b> via {imageProviderTarget?.displayName}.</>
          : stage === 'setup'
            ? <>Choose an OpenAI-family provider and image model to create images in chat.</>
            : <>{imageProviderTarget?.displayName} is configured — <b>enable</b> to let the AI generate images.</>,
        primaryLabel: stage === 'setup' ? 'Set up' : undefined,
        primaryIcon: stage === 'setup' ? 'gear' : null,
        toggleOn: on,
        toggleLabel: on ? 'Disable Image Generation for the AI' : 'Enable Image Generation for the AI',
        onToggle: (enabled) => { void persistImageGeneration({ enabled }); },
        hasDetails: true,
        onEdit: () => openSheet('image-generation', 'edit'),
        usesSheet: true,
        sheetEyebrow: intent === 'edit' ? 'Edit connection' : 'Set up connection',
        sheetBody: (
          <>
            <div className="cx-callout">
              <IconInfo />
              <p>
                Image Generation exposes <code>generate_image</code> to chats and saves every result as
                an image file. It reuses one existing OpenAI or compatible provider&apos;s encrypted credential.
              </p>
            </div>
            <Field label={<ReqLabel>Provider connection</ReqLabel>} help="The provider credential and base URL used for image requests.">
              <CxSelect
                value={imageProviderTarget?.id || ''}
                disabled={imageProviderConfigs.length === 0}
                onChange={(event) => updateImageGeneration({ providerId: event.target.value || null })}
              >
                <option value="">Select a provider</option>
                {imageProviderConfigs.map((config) => (
                  <option key={config.id} value={config.id}>{config.displayName} · {providerKindLabel(config)}</option>
                ))}
              </CxSelect>
            </Field>
            {imageProviderConfigs.length === 0 ? (
              <div className="cx-feedback cx-feedback-err" role="status">
                Add an OpenAI or compatible provider on the Providers tab first.
              </div>
            ) : null}
            <Field label={<ReqLabel>Image model</ReqLabel>} help="Defaults to GPT-Image-2; custom compatible model identifiers are preserved.">
              <CxInput
                mono
                value={imageGeneration.model || ''}
                placeholder="gpt-image-2"
                onChange={(event) => updateImageGeneration({ model: event.target.value })}
              />
            </Field>
            <div className="cx-sec">Generation defaults</div>
            <Field label="Quality">
              <CxSelect value={quality} onChange={(event) => updateImageGenerationDefaults({ quality: event.target.value })}>
                {['auto', 'low', 'medium', 'high'].map((value) => <option key={value} value={value}>{value}</option>)}
                {!['auto', 'low', 'medium', 'high'].includes(quality) ? <option value={quality}>{quality}</option> : null}
              </CxSelect>
            </Field>
            <Field label="Size" help="Use auto or a model-supported size such as 1024x1024.">
              <CxInput mono value={size} onChange={(event) => updateImageGenerationDefaults({ size: event.target.value })} />
            </Field>
            <Field label="Format">
              <CxSelect value={format} onChange={(event) => updateImageGenerationDefaults({ format: event.target.value })}>
                {['png', 'jpeg', 'webp'].map((value) => <option key={value} value={value}>{value}</option>)}
                {!['png', 'jpeg', 'webp'].includes(format) ? <option value={format}>{format}</option> : null}
              </CxSelect>
            </Field>
            <Field label="Max images per run" help="One image per call today; values up to four are reserved for variant workflows.">
              <CxInput
                type="number"
                min={1}
                max={4}
                value={imageGeneration.maxImagesPerRun || 1}
                onChange={(event) => updateImageGeneration({
                  maxImagesPerRun: Math.min(4, Math.max(1, Math.round(Number(event.target.value) || 1))),
                })}
              />
            </Field>
            <DangerZone name="Image Generation" onRemove={() => removeConnection('image-generation')} />
          </>
        ),
        sheetFooter: (
          <SheetFooter
            label={intent === 'edit' ? 'Save changes' : 'Save & enable'}
            disabled={!configured}
            reason={!imageProviderTarget
              ? 'Select an OpenAI or compatible provider to continue.'
              : !String(imageGeneration.model || '').trim()
                ? 'Enter an image model to continue.'
                : 'Generated images are billed by the selected image provider.'}
            reasonError={!configured}
            onClick={handleSaveImageGeneration}
          />
        ),
        detailsNode: (
          <>
            {confirmBlock(
              'image-generation',
              imageProviderTarget?.displayName || 'No provider selected',
              imageProviderTarget ? `${providerKindLabel(imageProviderTarget)} provider credential` : 'Not configured',
              on ? 'Ready' : configured ? 'Disabled' : 'Needs setup',
              { accountLabel: 'Target', reconnect: false },
            )}
            <div className="cx-sec">Tool the model can use</div>
            <div className={`cx-tools${configured ? '' : ' cx-dim'}`}>
              <ToolRow
                name="Generate image"
                desc={`${imageGeneration.model || 'gpt-image-2'} · ${quality} quality · ${format.toUpperCase()} · ${size}`}
                checked={on}
                disabled={!configured}
                label="Generate image"
                onChange={(enabled) => { void persistImageGeneration({ enabled }); }}
              />
            </div>
          </>
        ),
      };
    }

    if (id === 'web-search') {
      const enabledTargets = hostedProviderConfigs.filter((config) => hostedConnectionEnabled(config, 'web-search'));
      const on = enabledTargets.length > 0;
      const configured = hostedProviderConfigs.length > 0;
      const stage: Stage = on ? 'active' : 'enable';
      return {
        id,
        name: 'Web Search',
        stage,
        tone: on ? 'live' : 'default',
        eyebrow: on ? 'Active' : configured ? 'Ready' : 'Provider required',
        instruction: on
          ? <>Web search is attached to the <b>selected chat model</b>.</>
          : configured
            ? <>Provider-hosted search is available — <b>enable</b> it for the selected chat model.</>
            : <>Add OpenAI, Anthropic, or a Responses-compatible provider first.</>,
        toggleOn: on,
        toggleLabel: on ? 'Disable Web Search for the AI' : 'Enable Web Search for the AI',
        toggleDisabled: !configured,
        onToggle: (enabled) => {
          if (enabled) void persistHostedToolEnabled('web-search', '', true);
          if (!enabled) void persistAllHostedToolsDisabled('web-search');
        },
        hasDetails: false,
        usesSheet: false,
      };
    }

    if (id === 'python-sandbox') {
      const enabledTargets = hostedProviderConfigs.filter((config) => hostedConnectionEnabled(config, 'python-sandbox'));
      const on = enabledTargets.length > 0;
      const configured = hostedProviderConfigs.length > 0;
      const stage: Stage = on ? 'active' : configured ? 'enable' : 'setup';
      const intent = sheetIntent ?? (stage === 'setup' ? 'setup' : 'edit');
      const target = pythonProviderTarget;
      const codeExecution = target ? { ...defaultCodeExecution(target), ...(target.codeExecution || {}) } : null;
      const updateTarget = (patch: Partial<ProviderCodeExecutionConfig>) => {
        if (!target) return;
        updateProviderConfigLocal(target.id, (config) => ({
          ...config,
          codeExecution: { ...defaultCodeExecution(config), ...(config.codeExecution || {}), ...patch },
        }));
      };
      return {
        id,
        name: 'Python Sandbox',
        stage,
        tone: on ? 'live' : 'default',
        eyebrow: on ? 'Active' : stage === 'setup' ? 'Provider required' : 'Ready',
        instruction: on
          ? <>Running Python in provider-managed sandboxes through <b>{enabledTargets.map((config) => config.displayName).join(', ')}</b>.</>
          : configured
            ? <>A hosted coding runtime is available — <b>enable</b> it for {target?.displayName}.</>
            : <>Add OpenAI, Anthropic, or a Responses-compatible provider first.</>,
        primaryLabel: stage === 'setup' ? 'Set up' : undefined,
        primaryIcon: stage === 'setup' ? 'gear' : null,
        toggleOn: on,
        toggleLabel: on ? 'Disable Python Sandbox for the AI' : 'Enable Python Sandbox for the AI',
        toggleDisabled: !target,
        onToggle: (enabled) => {
          if (enabled && target) void persistHostedToolEnabled('python-sandbox', target.id, true);
          if (!enabled) void persistAllHostedToolsDisabled('python-sandbox');
        },
        hasDetails: true,
        onEdit: () => openSheet('python-sandbox', 'edit'),
        usesSheet: true,
        sheetEyebrow: intent === 'edit' ? 'Edit connection' : 'Set up connection',
        sheetBody: (
          <>
            <div className="cx-callout">
              <IconInfo />
              <p>
                Python runs inside a provider-managed sandbox, not on this computer. Files supplied to
                the sandbox may be uploaded to the selected provider under that provider&apos;s retention policy.
              </p>
            </div>
            <Field label={<ReqLabel>Provider connection</ReqLabel>} help="Only provider surfaces with hosted code-execution support appear here.">
              <CxSelect
                value={target?.id || ''}
                disabled={!configured}
                onChange={(event) => {
                  setPythonProviderTargetId(event.target.value);
                  setHostedToolFeedback(null);
                }}
              >
                <option value="">Select a provider</option>
                {hostedProviderConfigs.map((config) => (
                  <option key={config.id} value={config.id}>{config.displayName} · {providerKindLabel(config)}</option>
                ))}
              </CxSelect>
            </Field>
            {!configured ? (
              <div className="cx-feedback cx-feedback-err" role="status">Add a supported provider on the Providers tab first.</div>
            ) : null}
            {target && codeExecution ? (
              <>
                <ToolRow
                  name={`Enable for ${target.displayName}`}
                  desc="Makes the hosted Python runtime available whenever this provider is selected for a chat."
                  checked={hostedConnectionEnabled(target, 'python-sandbox')}
                  label={`Enable Python Sandbox for ${target.displayName}`}
                  onChange={(enabled) => updateHostedToolEnabled('python-sandbox', target.id, enabled)}
                />
                {target.providerId !== 'anthropic' ? (
                  <>
                    <Field label="Container mode" help="Auto creates a provider-managed container; Existing reuses a container ID.">
                      <CxSelect value={codeExecution.containerMode || 'auto'} onChange={(event) => updateTarget({ containerMode: event.target.value as 'auto' | 'existing' })}>
                        <option value="auto">auto</option>
                        <option value="existing">existing</option>
                      </CxSelect>
                    </Field>
                    {codeExecution.containerMode === 'existing' ? (
                      <Field label={<ReqLabel>Container ID</ReqLabel>}>
                        <CxInput mono value={codeExecution.containerId || ''} placeholder="cntr_abc123" onChange={(event) => updateTarget({ containerId: event.target.value || null })} />
                      </Field>
                    ) : null}
                    <Field label="Memory limit" help="Optional provider-specific limit, for example 4g.">
                      <CxInput mono value={codeExecution.memoryLimit || ''} placeholder="4g" onChange={(event) => updateTarget({ memoryLimit: event.target.value || null })} />
                    </Field>
                  </>
                ) : null}
                <Disclosure label="Advanced">
                  <Field label="Provider tool type" help="Leave the provider-specific default unless your endpoint requires another identifier.">
                    <CxInput mono value={codeExecution.toolType || ''} placeholder="code_interpreter" onChange={(event) => updateTarget({ toolType: event.target.value || null })} />
                  </Field>
                  <Field label="Skills JSON" help={'Optional provider skills: [{"type":"anthropic","skillId":"pdf","version":"latest"}]'}>
                    <CxTextarea
                      rows={5}
                      value={pythonSkillsDraft}
                      placeholder='[{"type":"anthropic","skillId":"pdf","version":"latest"}]'
                      onChange={(event) => {
                        const nextDraft = event.target.value;
                        pythonSkillsDraftRef.current = nextDraft;
                        setPythonSkillsDraft(nextDraft);
                        setHostedToolFeedback(null);
                      }}
                    />
                  </Field>
                </Disclosure>
              </>
            ) : null}
            {hostedToolFeedback?.kind === 'python-sandbox' ? (
              <div className="cx-feedback cx-feedback-err" role="status">{hostedToolFeedback.text}</div>
            ) : null}
            <DangerZone name="Python Sandbox" onRemove={() => removeConnection('python-sandbox')} />
          </>
        ),
        sheetFooter: (
          <SheetFooter
            label={intent === 'edit' ? 'Save changes' : 'Save & enable'}
            disabled={!target}
            reason={target ? 'Code executes in the provider-managed sandbox; it does not run on this computer.' : 'Add and select a supported provider to continue.'}
            reasonError={!target}
            onClick={() => handleSaveHostedConnection('python-sandbox', target?.id || '')}
          />
        ),
        detailsNode: (
          <>
            {confirmBlock(
              'python-sandbox',
              on ? `${enabledTargets.length} enabled provider${enabledTargets.length === 1 ? '' : 's'}` : 'No enabled providers',
              'Provider-managed sandbox',
              on ? 'Ready' : configured ? 'Disabled' : 'Needs a provider',
              { accountLabel: 'Targets', reconnect: false },
            )}
            <div className="cx-sec">Provider connections</div>
            <div className="cx-tools">
              {hostedProviderConfigs.map((config) => (
                <ToolRow
                  key={config.id}
                  name={config.displayName}
                  desc={`${providerKindLabel(config)} · ${config.model || 'model not set'}`}
                  checked={hostedConnectionEnabled(config, 'python-sandbox')}
                  label={`Python Sandbox for ${config.displayName}`}
                  onChange={(enabled) => { void persistHostedToolEnabled('python-sandbox', config.id, enabled); }}
                />
              ))}
              {hostedProviderConfigs.length === 0 ? <div className="cx-tdesc">No supported providers configured.</div> : null}
            </div>
          </>
        ),
      };
    }

    if (id === 'local-computer') {
      const on = localComputer.enabled === true;
      const mode = normalizeLocalComputerMode(localComputer.accessMode);
      const selectedFolders = dedupeLocalFolders(localComputer.selectedFolders || []);
      const selectedCount = selectedFolders.length;
      const scopeLabel = mode === 'selected-folders'
        ? selectedCount > 0
          ? `${selectedCount} selected ${selectedCount === 1 ? 'folder' : 'folders'}`
          : 'Selected folders'
        : 'Whole computer';
      const stage: Stage = on ? 'active' : 'setup';
      const intent = sheetIntent ?? (stage === 'setup' ? 'setup' : 'edit');
      const setupReason = mode === 'selected-folders' && selectedCount === 0
        ? 'Choose at least one folder, or switch to Whole computer.'
        : '';
      return {
        id, name: 'Local Computer', stage, tone: on ? 'live' : 'default',
        checkedAt: localComputer.lastCheckedAt,
        eyebrow: on ? 'Active · read-only' : 'Setup required',
        instruction: on
          ? <>Searching and reading files on this device within <b>{scopeLabel}</b>. Day-to-day folder references are managed from <b>Sources</b> in the app header; this connection sets the overall access mode.</>
          : <>Choose read-only local access once, then the AI can search and read without repeated file prompts.</>,
        primaryLabel: stage === 'setup' ? 'Set up' : undefined,
        primaryIcon: stage === 'setup' ? 'gear' : null,
        toggleOn: on,
        toggleLabel: on ? 'Disable Local Computer for the AI' : 'Enable Local Computer for the AI',
        toggleDisabled: Boolean(localComputerBusy),
        onToggle: (v) => {
          if (v) void handleLocalComputerSaveAccess();
          else void handleLocalComputerDisable();
        },
        hasDetails: true,
        feedback: localComputerFeedback?.text,
        feedbackError: localComputerFeedback?.error,
        onEdit: () => openSheet('local-computer', 'edit'),
        usesSheet: true,
        sheetEyebrow: intent === 'edit' ? 'Edit local access' : 'Set up local access',
        sheetBody: (
          <>
            <div className="cx-callout">
              <IconInfo />
              <p>
                Local Computer grants read-only search and file preview access on this device. Searches
                can use a bounded ripgrep/fd runtime inside the selected scope; arbitrary shell commands
                are not available. It does not allow writing, deleting or modifying local files. Folders
                referenced from the Sources dialog appear here automatically.
              </p>
            </div>
            <div>
              <span className="cx-field-label" id="cx-local-computer-scope-label">Access scope</span>
              <div className="cx-seg" role="radiogroup" aria-labelledby="cx-local-computer-scope-label">
                <button
                  type="button"
                  role="radio"
                  aria-checked={mode === 'whole-computer'}
                  className={`cx-seg-btn${mode === 'whole-computer' ? ' cx-on' : ''}`}
                  onClick={() => updateLocalComputer({ accessMode: 'whole-computer' })}
                >
                  Whole computer<span className="cx-seg-desc">Search and read anywhere the desktop app can access.</span>
                </button>
                <button
                  type="button"
                  role="radio"
                  aria-checked={mode === 'selected-folders'}
                  className={`cx-seg-btn${mode === 'selected-folders' ? ' cx-on' : ''}`}
                  onClick={() => updateLocalComputer({ accessMode: 'selected-folders' })}
                >
                  Selected folders<span className="cx-seg-desc">Limit access to folders you choose with the system picker.</span>
                </button>
              </div>
            </div>
            {mode === 'selected-folders' ? (
              <div className="cx-local-folders">
                <div className="cx-local-folders-head">
                  <div>
                    <div className="cx-tname">Allowed folders</div>
                    <div className="cx-tdesc">Use the folder picker. Paths cannot be typed manually here.</div>
                  </div>
                  <button
                    type="button"
                    className="cx-btn-secondary"
                    disabled={localComputerBusy === 'pick'}
                    onClick={() => void handleLocalComputerPickFolder()}
                  >
                    {localComputerBusy === 'pick' ? <span className="cx-spin cx-spin-ghost" /> : <IconPlus />}Choose folder
                  </button>
                </div>
                {selectedCount === 0 ? (
                  <div className="cx-local-folder-empty">No folders selected.</div>
                ) : (
                  <div className="cx-local-folder-list">
                    {selectedFolders.map((folder) => (
                      <div className="cx-local-folder" key={normalizeLocalPathKey(folder)}>
                        <span title={folder}>{displayPath(folder)}</span>
                        <button
                          type="button"
                          className="cx-local-folder-remove"
                          aria-label={`Remove ${displayPath(folder)}`}
                          onClick={() => updateLocalComputer({
                            selectedFolders: selectedFolders.filter((item) => (
                              normalizeLocalPathKey(item) !== normalizeLocalPathKey(folder)
                            )),
                          })}
                        >
                          <IconClose />
                        </button>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            ) : (
              <div className="cx-local-scope-note">
                <span>Read-only grant</span>
                <strong>Whole computer</strong>
              </div>
            )}
            {localComputerFeedback ? (
              <div className={`cx-feedback${localComputerFeedback.error ? ' cx-feedback-err' : ''}`} role="status">
                {localComputerFeedback.text}
              </div>
            ) : null}
            <DangerZone name="Local Computer" onRemove={() => { removeConnection('local-computer'); }} />
          </>
        ),
        sheetFooter: (
          <SheetFooter
            label={intent === 'edit' ? 'Save access' : 'Allow read-only access'}
            busy={localComputerBusy === 'save'}
            busyLabel="Saving…"
            disabled={Boolean(setupReason) || localComputerBusy === 'disable' || localComputerBusy === 'pick'}
            reason={setupReason || 'Creates read-only search/read grants. Write and delete remain unavailable.'}
            reasonError={Boolean(setupReason)}
            onClick={handleLocalComputerSaveAccess}
          />
        ),
        detailsNode: (
          <>
            {confirmBlock(
              'local-computer',
              scopeLabel,
              'Read-only local grant',
              on ? 'Ready' : 'Not configured',
              { accountLabel: 'Scope', reconnect: false, checkedAt: localComputer.lastCheckedAt },
            )}
            <div className="cx-sec">Tools the model can use</div>
            <div className={`cx-tools${on ? '' : ' cx-dim'}`}>
              <ToolRow
                name="Search files"
                desc="Search filenames and contents with bounded ripgrep/fd semantics inside the allowed scope."
                checked={on}
                disabled={!on}
                label="Local Computer search"
                onChange={(v) => { if (!v) void handleLocalComputerDisable(); }}
              />
              <ToolRow
                name="Read and preview files"
                desc="Open local files in the app viewer when they are inside the allowed scope."
                checked={on}
                disabled={!on}
                label="Local Computer read files"
                onChange={(v) => { if (!v) void handleLocalComputerDisable(); }}
              />
              <div className="cx-tool">
                <div>
                  <div className="cx-tname">Write or delete files</div>
                  <div className="cx-tdesc">Not available through this connection.</div>
                </div>
                <span className="cx-readonly-pill">Off</span>
              </div>
            </div>
          </>
        ),
      };
    }

    if (id === 'arxiv') {
      const ax = localSettings.arxiv || {};
      const on = ax.enabled === true;
      const stage: Stage = on ? 'active' : 'enable';
      const paperCount = arxivStatus?.paperCount ?? 0;
      const storageBytes = arxivStatus?.storageBytes ?? 0;
      const storageLabel = storageBytes > 0
        ? storageBytes < 1024 * 1024
          ? `${Math.max(1, Math.round(storageBytes / 1024))} KB`
          : `${(storageBytes / (1024 * 1024)).toFixed(1)} MB`
        : '';
      const checkedAt = arxivStatus?.lastCheckedAt ?? ax.lastCheckedAt ?? null;
      return {
        id, name: 'arXiv', stage, tone: on ? 'live' : 'default',
        eyebrow: on ? 'Active' : 'Ready',
        instruction: on
          ? <>Research papers from arxiv.org{paperCount ? <> · <b>{paperCount}</b> in library</> : null} — manage via the chevron.</>
          : <>Public arxiv.org API, no account needed — <b>enable</b> to search and download papers.</>,
        toggleOn: on,
        toggleLabel: on ? 'Disable arXiv for the AI' : 'Enable arXiv for the AI',
        onToggle: (v) => { void persistArxiv({ enabled: v }); },
        hasDetails: true,
        detailsNode: (
          <>
            {confirmBlock(
              'arxiv',
              'arxiv.org · public API',
              'No credential required',
              arxivStatus?.lastCheckOk === false ? 'Last check failed' : 'Ready',
              { accountLabel: 'Source', reconnect: false, checkedAt },
            )}
            <div className="cx-sec">Paper library</div>
            <div className="cx-tools">
              <div className="cx-tool">
                <div>
                  <div className="cx-tname">{paperCount} paper{paperCount === 1 ? '' : 's'} stored locally{storageLabel ? ` · ${storageLabel}` : ''}</div>
                  <div className="cx-tdesc">Search arXiv, download PDFs, preview and highlight, attach to chat. Paper content is treated as untrusted external data.</div>
                </div>
                <button type="button" className="cx-btn-secondary" onClick={() => openSheet('arxiv', 'edit')}>
                  Open papers
                </button>
              </div>
            </div>
            <Disclosure label="Advanced">
              <Field
                label={<ReqLabel optional>Contact email</ReqLabel>}
                help="Included in the arXiv API User-Agent as a courtesy contact. Saved automatically."
              >
                <CxInput
                  mono
                  placeholder="you@example.com"
                  value={ax.contactEmail || ''}
                  onChange={(e) => updateArxiv({ contactEmail: e.target.value })}
                  onBlur={(e) => { void persistArxiv({ contactEmail: e.target.value }); }}
                />
              </Field>
            </Disclosure>
          </>
        ),
        sheetEyebrow: 'Paper library',
        sheetBody: (
          <ArxivPapersPanel
            enabled={on}
            onAttachFile={onAttachFile}
            onInsertDraftText={onInsertDraftText}
            onCloseDialog={onCloseDialog}
          />
        ),
        sheetFooter: (
          <SheetFooter label="Done" onClick={requestCloseSheet} />
        ),
      };
    }

    if (id === 'browser') {
      const on = browserControl.enabled === true;
      const stage: Stage = on ? 'active' : 'enable';
      return {
        id, name: 'Browser', stage, tone: on ? 'live' : 'default',
        eyebrow: on ? 'Active' : 'Ready',
        instruction: on ? <>Browsing and acting on pages for you.</> : <>Runs locally on this device — <b>enable</b> to let the AI use it.</>,
        toggleOn: on, toggleLabel: on ? 'Disable Browser for the AI' : 'Enable Browser for the AI',
        onToggle: (v) => { void persistBrowserControl({ enabled: v }); },
        hasDetails: true,
        detailsNode: (
          <>
            {confirmBlock('browser', 'This device', 'Local Playwright profile', 'Ready', { reconnect: false })}
            <div className="cx-sec">Local browser settings</div>
            <div className="cx-tools">
              <ToolRow
                name="Show browser during automation"
                desc="Applies after the local profile restarts."
                checked={browserControl.showBrowser === true}
                label="Show browser during automation"
                onChange={(v) => { void persistBrowserControl({ showBrowser: v }); }}
              />
            </div>
            <Disclosure label="Advanced">
              <Field label="Browser profile directory" help="Leave blank to use the app config directory.">
                <CxInput
                  mono
                  placeholder="Default: app config directory"
                  value={browserControl.browserProfileDir || ''}
                  onChange={(e) => updateBrowserControl({ browserProfileDir: e.target.value })}
                  onBlur={(e) => { void persistBrowserControl({ browserProfileDir: e.target.value }); }}
                />
              </Field>
            </Disclosure>
          </>
        ),
      };
    }

    if (id === 'github') {
      const stage: Stage = !githubConnected
        ? 'connect'
        : (githubNeedsReconnect || githubConfigChanged)
          ? 'reconnect'
          : githubModelAccess ? 'active' : 'enable';
      const unverifiedOnly = githubConfigChanged && !githubNeedsReconnect;
      // Surface a concise prerequisite hint while signing in (git/gh installed
      // and authenticated). The full checklist resolves one step at a time.
      const prereqStep = (stage === 'connect' && githubPrereq && !githubPrereq.ready)
        ? (githubPrereq.missing?.[0] || null)
        : null;
      const prereqNode = prereqStep
        ? <>{prereqStep.instruction}{prereqStep.command ? <> <code>{prereqStep.command}</code></> : null} · <button type="button" className="cx-relink" onClick={() => void refreshGitHubPrereq()}>Re-check</button></>
        : null;
      const prereqGuidance = { error: Boolean(prereqStep), node: prereqNode };
      const cancelNode = githubAuthenticating
        ? <> · <button type="button" className="cx-relink" onClick={handleGitHubCancelAuth}>Cancel</button></>
        : null;
      const feedbackNode = githubError
        ? githubError
        : githubAuthMessage
          ? (githubDeviceFlow
            ? <>{githubAuthMessage} Code <code>{githubDeviceFlow.user_code}</code> · <a href={githubDeviceFlow.verification_uri_complete || githubDeviceFlow.verification_uri} target="_blank" rel="noreferrer">open GitHub</a>{cancelNode}</>
            : <>{githubAuthMessage}{cancelNode}</>)
          : prereqGuidance.node;
      const intent = sheetIntent ?? (stage === 'reconnect' ? 'reconnect' : 'setup');
      return {
        id, name: 'GitHub', stage, tone: stage === 'active' ? 'live' : stage === 'reconnect' ? 'warn' : 'default',
        checkedAt: githubLastCheckedAt,
        eyebrow: stage === 'connect'
          ? 'Not connected'
          : stage === 'reconnect'
            ? (unverifiedOnly ? 'Unverified changes' : 'Reconnect required')
            : stage === 'active' ? 'Active' : 'Connected · off',
        instruction: stage === 'connect'
          ? <>github.com — <b>sign in</b> to link your account.</>
          : stage === 'reconnect'
            ? (unverifiedOnly
              ? <>Setup changed since the last sign-in — <b>reconnect</b> to verify and apply it.</>
              : <>{githubAccount ? `@${githubAccount}` : 'github.com'} — <b>reconnect</b> to refresh access.</>)
            : stage === 'enable'
              ? <>{githubAccount ? `@${githubAccount}` : 'github.com'} — <b>enable</b> to let the AI use it.</>
              : <>Reading repositories, issues and pull requests.</>,
        connecting: githubAuthenticating, connectingLabel: 'Connecting…',
        primaryLabel: stage === 'reconnect' ? 'Reconnect' : stage === 'connect' ? 'Connect' : undefined,
        primaryIcon: stage === 'connect' || stage === 'reconnect' ? 'arrow' : null,
        toggleOn: githubModelAccess, toggleLabel: githubModelAccess ? 'Disable GitHub for the AI' : 'Enable GitHub for the AI',
        onToggle: (v) => {
          void persistConnectionUpdate('github', (prev) => withConnectionEnabled(prev, 'github', v));
        },
        hasDetails: true,
        feedback: feedbackNode, feedbackError: Boolean(githubError) || prereqGuidance.error,
        disconnect: githubConnected
          ? { label: 'Sign out of GitHub', onClick: handleGitHubDisconnect, disabled: githubBusy === 'github-disconnect' }
          : null,
        onEdit: () => openSheet('github', 'edit'),
        usesSheet: true,
        sheetEyebrow: intent === 'edit' ? 'Edit connection' : intent === 'reconnect' ? 'Sign in again' : 'Sign in',
        sheetBody: (
          <>
            <div className="cx-callout">
              <IconInfo />
              <p>
                Authorize GitHub to read repositories, issues and pull requests. Save &amp; connect opens
                GitHub in your browser to approve a device code.{' '}
                <a href="https://docs.github.com/en/apps/oauth-apps" target="_blank" rel="noreferrer">Setup guide <IconExternal /></a>
              </p>
            </div>
            {githubDeviceFlow ? (
              <div className="cx-feedback" role="status">
                Enter code <code>{githubDeviceFlow.user_code}</code> at{' '}
                <a href={githubDeviceFlow.verification_uri_complete || githubDeviceFlow.verification_uri} target="_blank" rel="noreferrer">github.com/login/device</a>
                {' '}· <button type="button" className="cx-relink" onClick={handleGitHubCancelAuth}>Cancel</button>
              </div>
            ) : null}
            {!githubDeviceFlow && feedbackNode ? (
              <div className={`cx-feedback${Boolean(githubError) || prereqGuidance.error ? ' cx-feedback-err' : ''}`} role="status">{feedbackNode}</div>
            ) : null}
            <Disclosure label="Advanced">
              <Field label="Custom OAuth client ID" help="Leave blank to use the bundled GitHub sign-in.">
                <CxInput mono placeholder="Bundled client when empty" value={localSettings.githubOAuthClientId || ''} onChange={(e) => updateGitHubOAuthClientId(e.target.value)} />
              </Field>
            </Disclosure>
            <DangerZone
              name="GitHub"
              signOut={githubConnected
                ? { label: 'Sign out of GitHub', onClick: handleGitHubDisconnect, disabled: githubBusy === 'github-disconnect' }
                : null}
              onRemove={() => { removeConnection('github'); }}
            />
          </>
        ),
        sheetFooter: intent === 'edit'
          ? (
            <SheetFooter
              label="Save changes"
              busy={githubBusy === 'github-save'}
              busyLabel="Saving…"
              reason="Changing the OAuth client requires signing in again before GitHub shows as verified."
              onClick={handleGitHubSaveEdit}
            />
          )
          : (
            <SheetFooter
              label={intent === 'reconnect' ? 'Reconnect' : 'Save & connect'}
              busy={githubAuthenticating}
              busyLabel="Connecting…"
              onClick={handleGitHubConnect}
            />
          ),
        detailsNode: (
          <>
            {confirmBlock(
              'github',
              githubAccount ? `@${githubAccount}` : (githubConnected ? 'Connected' : 'Not signed in'),
              `GitHub OAuth${githubStatus?.scope ? ` · ${githubStatus.scope}` : ''}`,
              !githubConnected
                ? 'Not signed in'
                : githubNeedsReconnect
                  ? (githubStatus?.lastCheckMessage || github.lastCheckMessage || 'Needs reconnection')
                  : githubConfigChanged
                    ? 'Unverified changes — reconnect to apply'
                    : 'Token valid',
              { checkedAt: githubLastCheckedAt },
            )}
            <div className="cx-sec">Tools the model can use</div>
            <div className={`cx-tools${githubModelAccess ? '' : ' cx-dim'}`}>
              <ScopeControl
                label="Limit to specific repositories"
                defaultOn={(github.allowedRepositories || []).length > 0}
                value={(github.allowedRepositories || []).join(', ')}
                placeholder="owner/repo, org/another-repo"
                disabled={!githubModelAccess}
                onChange={(v) => updateGithub({ allowedRepositories: splitGitHubRepositoryInput(v) })}
                onBlur={(v) => { void persistGithub({ allowedRepositories: normalizeGitHubRepositoryList(v) }); }}
                onClear={() => { void persistGithub({ allowedRepositories: [] }); }}
              />
            </div>
          </>
        ),
      };
    }

    if (id === 'slack') {
      const stage: Stage = !slackClientConfigured
        ? 'setup'
        : !slackConnected
          ? 'connect'
          : (slackNeedsReconnect || slackConfigChanged)
            ? 'reconnect'
            : slackModelAccess ? 'active' : 'enable';
      const unverifiedOnly = slackConfigChanged && !slackNeedsReconnect;
      const cancelNode = slackAuthenticating
        ? <> · <button type="button" className="cx-relink" onClick={handleSlackCancelAuth}>Cancel</button></>
        : null;
      const feedbackNode = slackStatus?.error
        ? slackStatus.error
        : slackOAuthMessage
          ? (slackOAuthFlow?.authorize_url
            ? <>{slackOAuthMessage} <a href={slackOAuthFlow.authorize_url} target="_blank" rel="noreferrer">open Slack authorization</a>{cancelNode}</>
            : <>{slackOAuthMessage}{cancelNode}</>)
          : null;
      const intent = sheetIntent ?? (stage === 'reconnect' ? 'reconnect' : 'setup');
      return {
        id, name: 'Slack', stage, tone: stage === 'active' ? 'live' : stage === 'reconnect' ? 'warn' : 'default',
        checkedAt: slackLastCheckedAt,
        eyebrow: stage === 'setup'
          ? 'Setup required'
          : stage === 'connect'
            ? 'Not connected'
            : stage === 'reconnect'
              ? (unverifiedOnly ? 'Unverified changes' : 'Reconnect required')
              : stage === 'active' ? 'Active' : 'Connected · off',
        instruction: stage === 'setup'
          ? <>Add your <b>Slack OAuth client ID</b> to continue.</>
          : stage === 'connect'
            ? <>{slackWorkspace || 'Slack'} — <b>sign in</b> to link your workspace.</>
            : stage === 'reconnect'
              ? (unverifiedOnly
                ? <>Setup changed since the last sign-in — <b>reconnect</b> to verify and apply it.</>
                : <>{slackWorkspace || 'Slack workspace'} — <b>reconnect</b> to refresh access.</>)
              : stage === 'enable'
                ? <>{slackWorkspace || 'Slack workspace'} — <b>enable</b> to let the AI use it.</>
                : <>Reading and posting messages in {slackWorkspace || 'your workspace'}.</>,
        connecting: slackAuthenticating, connectingLabel: 'Connecting…',
        primaryLabel: stage === 'setup' ? 'Set up' : stage === 'reconnect' ? 'Reconnect' : stage === 'connect' ? 'Connect' : undefined,
        primaryIcon: stage === 'setup' ? 'gear' : stage === 'connect' || stage === 'reconnect' ? 'arrow' : null,
        toggleOn: slackModelAccess, toggleLabel: slackModelAccess ? 'Disable Slack for the AI' : 'Enable Slack for the AI',
        onToggle: (v) => {
          void persistConnectionUpdate('slack', (prev) => withConnectionEnabled(prev, 'slack', v));
        },
        hasDetails: true,
        feedback: feedbackNode, feedbackError: Boolean(slackStatus?.error),
        disconnect: slackConnected
          ? { label: 'Sign out of Slack', onClick: handleSlackDisconnect, disabled: slackBusy === 'slack-disconnect' }
          : null,
        onEdit: () => openSheet('slack', 'edit'),
        usesSheet: true,
        sheetEyebrow: intent === 'edit' ? 'Edit connection' : intent === 'reconnect' ? 'Sign in again' : 'Set up connection',
        sheetBody: (
          <>
            <div className="cx-callout">
              <IconInfo />
              <p>
                Authorize a Slack workspace over OAuth (PKCE). Paste your app&apos;s public Client ID;
                Save &amp; connect opens Slack to approve.{' '}
                <a href="https://api.slack.com/apps" target="_blank" rel="noreferrer">Slack app setup <IconExternal /></a>
              </p>
            </div>
            <Field label={<ReqLabel>Slack OAuth client ID</ReqLabel>} help="From your Slack app's Basic Information. Enable PKCE so this public ID is the only credential needed.">
              <CxInput mono placeholder="1234567890.1234567890" value={slackOAuthClientId} onChange={(e) => updateSlackOAuthClientId(e.target.value)} />
            </Field>
            {slackStatus?.error
              ? <div className="cx-feedback cx-feedback-err" role="status">{slackStatus.error}</div>
              : slackOAuthMessage
                ? <div className="cx-feedback" role="status">{slackOAuthMessage}{slackOAuthFlow?.authorize_url ? <> · <a href={slackOAuthFlow.authorize_url} target="_blank" rel="noreferrer">open Slack authorization</a></> : null}{cancelNode}</div>
                : null}
            <Disclosure label="Advanced">
              <SecretField label="OAuth client secret" placeholder="Only for non-PKCE Slack apps" isSet={Boolean(slackStatus?.client_secret_set)} busy={slackBusy === 'slack-client-secret'} onSet={handleSlackSaveClientSecret} onClear={handleSlackClearClientSecret} help="Only needed when your Slack app does not use PKCE. Stored encrypted; never shown in chat." />
            </Disclosure>
            <DangerZone
              name="Slack"
              signOut={slackConnected
                ? { label: 'Sign out of Slack', onClick: handleSlackDisconnect, disabled: slackBusy === 'slack-disconnect' }
                : null}
              onRemove={() => { removeConnection('slack'); }}
            />
          </>
        ),
        sheetFooter: (() => {
          if (intent === 'edit') {
            return (
              <SheetFooter
                label="Save changes"
                busy={slackBusy === 'slack-save'}
                busyLabel="Saving…"
                reason="Changing the OAuth client requires signing in again before Slack shows as verified."
                onClick={handleSlackSaveEdit}
              />
            );
          }
          const reason = !slackOAuthClientId.trim() ? 'Add your Slack OAuth client ID to continue.' : '';
          return (
            <SheetFooter
              label={intent === 'reconnect' ? 'Reconnect' : 'Save & connect'}
              busy={slackAuthenticating}
              busyLabel="Connecting…"
              disabled={Boolean(reason)}
              reason={reason || undefined}
              onClick={handleSlackConnect}
            />
          );
        })(),
        detailsNode: (
          <>
            {confirmBlock(
              'slack',
              slackWorkspace || (slackConnected ? 'Connected' : 'Not signed in'),
              `Slack OAuth${slackStatus?.authed_user_id ? ` · user ${slackStatus.authed_user_id}` : ''}`,
              !slackConnected
                ? 'Not signed in'
                : slackNeedsReconnect
                  ? (slackStatus?.lastCheckMessage || slack.lastCheckMessage || 'Needs reconnection')
                  : slackConfigChanged
                    ? 'Unverified changes — reconnect to apply'
                    : 'Token valid',
              { accountLabel: 'Workspace', checkedAt: slackLastCheckedAt },
            )}
            <div className="cx-sec">Tools the model can use</div>
            <div className={`cx-tools${slackModelAccess ? '' : ' cx-dim'}`}>
              <ToolRow name="Read messages" desc="Read channels, messages and threads." checked={slack.readTools === true} disabled={!slackModelAccess} label="Slack read messages" onChange={(v) => { void persistSlack({ readTools: v }); }} />
              <ToolRow name="Write messages" desc="Approval-gated posting and uploads." checked={slack.writeTools === true} disabled={!slackModelAccess} label="Slack write messages" onChange={(v) => { void persistSlack({ writeTools: v }); }} />
              <ScopeControl
                label="Limit to specific channels"
                defaultOn={(slack.allowedChannelIds || []).length > 0}
                value={(slack.allowedChannelIds || []).join(', ')}
                placeholder="C0123456789, C0456789012"
                disabled={!slackModelAccess}
                onChange={(v) => updateSlack({ allowedChannelIds: splitSlackIdInput(v) })}
                onBlur={(v) => { void persistSlack({ allowedChannelIds: normalizeSlackIdList(v) }); }}
                onClear={() => { void persistSlack({ allowedChannelIds: [] }); }}
              />
              <ScopeControl
                label="Limit to specific teams"
                defaultOn={(slack.allowedTeamIds || []).length > 0}
                value={(slack.allowedTeamIds || []).join(', ')}
                placeholder="T0123456789"
                disabled={!slackModelAccess}
                onChange={(v) => updateSlack({ allowedTeamIds: splitSlackIdInput(v) })}
                onBlur={(v) => { void persistSlack({ allowedTeamIds: normalizeSlackIdList(v) }); }}
                onClear={() => { void persistSlack({ allowedTeamIds: [] }); }}
              />
            </div>
          </>
        ),
      };
    }

    if (id === 'atlassian') {
      const stage: Stage = !atlassianHasJiraUrl
        ? 'setup'
        : !atlassianConnected
          ? 'connect'
          : (atlassianNeedsReconnect || atlassianConfigChanged)
            ? 'reconnect'
            : atlassianToolsEnabled ? 'active' : 'enable';
      const unverifiedOnly = atlassianConfigChanged && !atlassianNeedsReconnect;
      const cancelNode = atlassianAuthenticating
        ? <> · <button type="button" className="cx-relink" onClick={handleAtlassianCancelAuth}>Cancel</button></>
        : null;
      const feedbackNode = status?.error
        ? String(status.error)
        : atlassianAuthenticating
          ? <>{compactText(status?.autoLogin?.message, 'Waiting for Atlassian authentication…')}{cancelNode}</>
          : null;
      const intent = sheetIntent ?? (stage === 'reconnect' ? 'reconnect' : 'setup');
      return {
        id, name: 'Atlassian', stage, tone: stage === 'active' ? 'live' : stage === 'reconnect' ? 'warn' : 'default',
        checkedAt: atlassianLastCheckedAt,
        eyebrow: stage === 'setup'
          ? 'Setup required'
          : stage === 'connect'
            ? 'Not connected'
            : stage === 'reconnect'
              ? (unverifiedOnly ? 'Unverified changes' : 'Reconnect required')
              : stage === 'active' ? 'Active' : 'Connected · off',
        instruction: stage === 'setup'
          ? <>Add your <b>Jira site URL</b> to continue.</>
          : stage === 'connect'
            ? <>{atlassianSite} — <b>sign in</b> to link your account.</>
            : stage === 'reconnect'
              ? (unverifiedOnly
                ? <>Setup changed since the last sign-in — <b>reconnect</b> to verify and apply it.</>
                : <>{atlassianSite} — <b>reconnect</b> to refresh access.</>)
              : stage === 'enable'
                ? <>{atlassianSite} — <b>enable</b> to let the AI use it.</>
                : <>Searching Jira issues and Confluence pages.</>,
        connecting: atlassianAuthenticating, connectingLabel: 'Connecting…',
        primaryLabel: stage === 'setup' ? 'Set up' : stage === 'reconnect' ? 'Reconnect' : stage === 'connect' ? 'Connect' : undefined,
        primaryIcon: stage === 'setup' ? 'gear' : stage === 'connect' || stage === 'reconnect' ? 'arrow' : null,
        toggleOn: atlassianToolsEnabled, toggleLabel: atlassianToolsEnabled ? 'Disable Atlassian for the AI' : 'Enable Atlassian for the AI',
        onToggle: (v) => { void persistAtlassian({ jira: { enabled: v }, confluence: { enabled: v } }); },
        hasDetails: true,
        feedback: feedbackNode, feedbackError: Boolean(status?.error),
        disconnect: atlassianConnected
          ? { label: 'Sign out of Atlassian', onClick: handleAtlassianDisconnect, disabled: busy === 'disconnect' }
          : null,
        onEdit: () => openSheet('atlassian', 'edit'),
        usesSheet: true,
        sheetEyebrow: intent === 'edit' ? 'Edit connection' : intent === 'reconnect' ? 'Sign in again' : 'Set up connection',
        sheetBody: (
          <>
            <div className="cx-callout">
              <IconInfo />
              <p>
                Save &amp; connect opens the bundled browser for single sign-on, then Finish login once
                you have authenticated.{' '}
                <a href="https://support.atlassian.com" target="_blank" rel="noreferrer">Setup guide <IconExternal /></a>
              </p>
            </div>
            <Field label={<ReqLabel>Jira site URL</ReqLabel>} help="Your Atlassian Cloud site, e.g. https://your-company.atlassian.net.">
              <CxInput
                mono
                placeholder="https://your-company.atlassian.net"
                value={atlassian.jiraBaseUrl || ''}
                onChange={(e) => updateAtlassian({ jiraBaseUrl: e.target.value })}
                onBlur={(e) => {
                  const jiraBaseUrl = normalizeAtlassianUrl(e.target.value);
                  const updateConfluence = shouldUpdateDerivedConfluenceUrl(atlassian.confluenceBaseUrl, atlassian.jiraBaseUrl);
                  updateAtlassian({ jiraBaseUrl, confluenceBaseUrl: updateConfluence ? derivedConfluenceUrl(jiraBaseUrl) : normalizeAtlassianUrl(atlassian.confluenceBaseUrl || '', true) });
                }}
              />
            </Field>
            {status?.error
              ? <div className="cx-feedback cx-feedback-err" role="status">{String(status.error)}</div>
              : feedbackNode
                ? <div className="cx-feedback" role="status">{feedbackNode}</div>
                : null}
            <Disclosure label="Advanced">
              <Field label="Confluence base URL" help="Defaults from the Jira site; override only if different.">
                <CxInput mono placeholder="https://your-company.atlassian.net/wiki" value={atlassian.confluenceBaseUrl || ''}
                  onChange={(e) => updateAtlassian({ confluenceBaseUrl: e.target.value })}
                  onBlur={(e) => updateAtlassian({ confluenceBaseUrl: normalizeAtlassianUrl(e.target.value, true) })} />
              </Field>
              <Field label="Default Jira project">
                <CxInput mono placeholder="TEAM" value={atlassian.defaultJiraProjectKey || ''} onChange={(e) => updateAtlassian({ defaultJiraProjectKey: e.target.value.toUpperCase() })} />
              </Field>
            </Disclosure>
            <DangerZone
              name="Atlassian"
              signOut={atlassianConnected
                ? { label: 'Sign out of Atlassian', onClick: handleAtlassianDisconnect, disabled: busy === 'disconnect' }
                : null}
              onRemove={() => { removeConnection('atlassian'); }}
            />
          </>
        ),
        sheetFooter: (() => {
          const reason = !atlassianHasJiraUrl ? 'Add your Jira site URL to continue.' : '';
          if (intent === 'edit') {
            return (
              <SheetFooter
                label="Save & verify"
                busy={busy === 'verify'}
                busyLabel="Verifying…"
                disabled={Boolean(reason)}
                reason={reason || 'Saves these settings and re-checks the connection against them.'}
                reasonError={Boolean(reason)}
                onClick={handleAtlassianSaveEdit}
              />
            );
          }
          return (
            <>
              <SheetFooter
                label={intent === 'reconnect' ? 'Reconnect' : 'Save & connect'}
                busy={atlassianAuthenticating}
                busyLabel="Waiting for sign-in…"
                disabled={Boolean(reason)}
                reason={reason || 'Opens the bundled browser for single sign-on. The app will finish automatically once sign-in is detected.'}
                reasonError={Boolean(reason)}
                onClick={() => { void handleAtlassianLogin(); }}
              />
              {atlassianAuthenticating ? (
                <button
                  type="button"
                  className="cx-btn-ghost-sm"
                  onClick={handleAtlassianCancelAuth}
                >
                  Cancel sign-in
                </button>
              ) : null}
              <button
                type="button"
                className="cx-btn-ghost-sm"
                disabled={busy === 'finish' || !atlassianHasJiraUrl}
                onClick={() => { void handleAtlassianFinish(); }}
              >
                {busy === 'finish' ? <span className="cx-spin cx-spin-ghost" /> : <IconCheck />}Finish login
              </button>
            </>
          );
        })(),
        detailsNode: (
          <>
            {confirmBlock(
              'atlassian',
              atlassianSite,
              atlassianCredential,
              !atlassianConnected
                ? 'Not signed in'
                : atlassianNeedsReconnect
                  ? (atlassian.jira?.lastCheckMessage || atlassian.confluence?.lastCheckMessage || 'Needs reconnection')
                  : atlassianConfigChanged
                    ? 'Unverified changes — reconnect to apply'
                    : 'Signed in',
              { accountLabel: 'Site', checkedAt: atlassianLastCheckedAt },
            )}
            <div className="cx-sec">Tools the model can use</div>
            <div className={`cx-tools${atlassianToolsEnabled ? '' : ' cx-dim'}`}>
              <ToolRow name="Jira" desc="Search and read issues." checked={atlassian.jira?.enabled === true} disabled={!atlassianToolsEnabled} label="Atlassian Jira" onChange={(v) => { void persistAtlassian({ jira: { enabled: v } }); }} />
              <ToolRow name="Confluence" desc="Search and read pages." checked={atlassian.confluence?.enabled === true} disabled={!atlassianToolsEnabled} label="Atlassian Confluence" onChange={(v) => { void persistAtlassian({ confluence: { enabled: v } }); }} />
              <ScopeControl
                label="Limit to specific Jira projects"
                defaultOn={(atlassian.allowedProjects || []).length > 0}
                value={(atlassian.allowedProjects || []).join(', ')}
                placeholder="TEAM, PLAT"
                disabled={!atlassianToolsEnabled}
                onChange={(v) => updateAtlassian({ allowedProjects: splitList(v).map((x) => x.toUpperCase()) })}
                onBlur={(v) => { void persistAtlassian({ allowedProjects: splitList(v).map((x) => x.toUpperCase()) }); }}
                onClear={() => { void persistAtlassian({ allowedProjects: [] }); }}
              />
            </div>
          </>
        ),
      };
    }

    if (id === 'sharepoint') {
      const stage: Stage = !sharePointHasUrl
        ? 'setup'
        : !sharePointConnected
          ? 'connect'
          : (sharePointNeedsReconnect || sharePointConfigChanged)
            ? 'reconnect'
            : sharePointToolsEnabled ? 'active' : 'enable';
      const unverifiedOnly = sharePointConfigChanged && !sharePointNeedsReconnect;
      const cancelNode = sharePointAuthenticating
        ? <> · <button type="button" className="cx-relink" onClick={handleSharePointCancelAuth}>Cancel</button></>
        : null;
      const feedbackNode = sharePointStatus?.error
        ? String(sharePointStatus.error)
        : sharePointAuthenticating
          ? <>{compactText(sharePointStatus?.autoLogin?.message, 'Waiting for SharePoint authentication…')}{cancelNode}</>
          : null;
      const intent = sheetIntent ?? (stage === 'reconnect' ? 'reconnect' : 'setup');
      return {
        id, name: 'SharePoint', stage, tone: stage === 'active' ? 'live' : stage === 'reconnect' ? 'warn' : 'default',
        checkedAt: sharePointLastCheckedAt,
        eyebrow: stage === 'setup'
          ? 'Setup required'
          : stage === 'connect'
            ? 'Not connected'
            : stage === 'reconnect'
              ? (unverifiedOnly ? 'Unverified changes' : 'Reconnect required')
              : stage === 'active' ? 'Active' : 'Connected · off',
        instruction: stage === 'setup'
          ? <>Add your <b>SharePoint URL</b> to continue.</>
          : stage === 'connect'
            ? <>{sharePointSite} — <b>sign in</b> to link your account.</>
            : stage === 'reconnect'
              ? (unverifiedOnly
                ? <>Setup changed since the last sign-in — <b>reconnect</b> to verify and apply it.</>
                : <>{sharePointSite} — <b>reconnect</b> to refresh access.</>)
              : stage === 'enable'
                ? <>{sharePointSite} — <b>enable</b> to let the AI use it.</>
                : <>Searching and reading SharePoint documents.</>,
        connecting: sharePointAuthenticating, connectingLabel: 'Connecting…',
        primaryLabel: stage === 'setup' ? 'Set up' : stage === 'reconnect' ? 'Reconnect' : stage === 'connect' ? 'Connect' : undefined,
        primaryIcon: stage === 'setup' ? 'gear' : stage === 'connect' || stage === 'reconnect' ? 'arrow' : null,
        toggleOn: sharePointToolsEnabled, toggleLabel: sharePointToolsEnabled ? 'Disable SharePoint for the AI' : 'Enable SharePoint for the AI',
        onToggle: (v) => { void persistSharePoint({ enabled: v }); },
        hasDetails: true,
        feedback: feedbackNode, feedbackError: Boolean(sharePointStatus?.error),
        disconnect: sharePointConnected
          ? { label: 'Sign out of SharePoint', onClick: handleSharePointDisconnect, disabled: sharePointBusy === 'disconnect' }
          : null,
        onEdit: () => openSheet('sharepoint', 'edit'),
        usesSheet: true,
        sheetEyebrow: intent === 'edit' ? 'Edit connection' : intent === 'reconnect' ? 'Sign in again' : 'Set up connection',
        sheetBody: (
          <>
            <div className="cx-callout">
              <IconInfo />
              <p>
                Save &amp; connect opens the bundled browser for SharePoint single sign-on. The app stores
                only a local browser capability; tokens and cookies are never shown in chat.
              </p>
            </div>
            <Field label={<ReqLabel>SharePoint URL</ReqLabel>} help="Use your tenant or site URL, e.g. https://your-company.sharepoint.com or https://your-company.sharepoint.com/sites/team.">
              <CxInput
                mono
                placeholder="https://your-company.sharepoint.com"
                value={sharepoint.siteUrl || ''}
                onChange={(e) => updateSharePoint({ siteUrl: e.target.value })}
                onBlur={(e) => updateSharePoint({ siteUrl: normalizeSharePointUrl(e.target.value) })}
              />
            </Field>
            {sharePointStatus?.error
              ? <div className="cx-feedback cx-feedback-err" role="status">{String(sharePointStatus.error)}</div>
              : feedbackNode
                ? <div className="cx-feedback" role="status">{feedbackNode}</div>
                : null}
            <Disclosure label="Advanced">
              <div className="cx-tool">
                <div>
                  <div className="cx-tname">Show browser during sign-in checks</div>
                  <div className="cx-tdesc">Applies after the SharePoint browser profile restarts.</div>
                </div>
                <Toggle checked={sharepoint.showBrowser === true} label="Show SharePoint browser" onChange={(v) => updateSharePoint({ showBrowser: v })} />
              </div>
              <Field label="Browser profile directory" help="Leave blank to use the app config directory.">
                <CxInput mono placeholder="Default: app config directory" value={sharepoint.browserProfileDir || ''} onChange={(e) => updateSharePoint({ browserProfileDir: e.target.value })} />
              </Field>
            </Disclosure>
            <DangerZone
              name="SharePoint"
              signOut={sharePointConnected
                ? { label: 'Sign out of SharePoint', onClick: handleSharePointDisconnect, disabled: sharePointBusy === 'disconnect' }
                : null}
              onRemove={() => { removeConnection('sharepoint'); }}
            />
          </>
        ),
        sheetFooter: (() => {
          const reason = !sharePointHasUrl ? 'Add your SharePoint URL to continue.' : '';
          if (intent === 'edit') {
            return (
              <SheetFooter
                label="Save & verify"
                busy={sharePointBusy === 'verify'}
                busyLabel="Verifying…"
                disabled={Boolean(reason)}
                reason={reason || 'Saves these settings and re-checks the connection against them.'}
                reasonError={Boolean(reason)}
                onClick={handleSharePointSaveEdit}
              />
            );
          }
          return (
            <>
              <SheetFooter
                label={intent === 'reconnect' ? 'Reconnect' : 'Save & connect'}
                busy={sharePointAuthenticating}
                busyLabel="Waiting for sign-in…"
                disabled={Boolean(reason)}
                reason={reason || 'Opens the bundled browser for single sign-on. The app will finish automatically once sign-in is detected.'}
                reasonError={Boolean(reason)}
                onClick={() => { void handleSharePointLogin(); }}
              />
              {sharePointAuthenticating ? (
                <button
                  type="button"
                  className="cx-btn-ghost-sm"
                  onClick={handleSharePointCancelAuth}
                >
                  Cancel sign-in
                </button>
              ) : null}
              <button
                type="button"
                className="cx-btn-ghost-sm"
                disabled={sharePointBusy === 'finish' || !sharePointHasUrl}
                onClick={() => { void handleSharePointFinish(); }}
              >
                {sharePointBusy === 'finish' ? <span className="cx-spin cx-spin-ghost" /> : <IconCheck />}Finish login
              </button>
            </>
          );
        })(),
        detailsNode: (
          <>
            {confirmBlock(
              'sharepoint',
              sharePointSite,
              sharePointCredential,
              !sharePointConnected
                ? 'Not signed in'
                : sharePointNeedsReconnect
                  ? (sharepoint.lastCheckMessage || 'Needs reconnection')
                  : sharePointConfigChanged
                    ? 'Unverified changes — reconnect to apply'
                    : 'Signed in',
              { accountLabel: 'Site', checkedAt: sharePointLastCheckedAt },
            )}
            <div className="cx-sec">Tools the model can use</div>
            <div className={`cx-tools${sharePointToolsEnabled ? '' : ' cx-dim'}`}>
              <ToolRow
                name="Search files"
                desc="Find accessible SharePoint and OneDrive documents by text query."
                checked={sharePointToolsEnabled}
                disabled={!sharePointToolsEnabled}
                label="SharePoint search files"
                onChange={(v) => { void persistSharePoint({ enabled: v }); }}
              />
              <ToolRow
                name="Read and download files"
                desc="Download files into the app parser and native model file-input path."
                checked={sharePointToolsEnabled}
                disabled={!sharePointToolsEnabled}
                label="SharePoint read files"
                onChange={(v) => { void persistSharePoint({ enabled: v }); }}
              />
            </div>
          </>
        ),
      };
    }

    if (id === 'bigquery') {
      const needsReauth = bqNeedsReauth && Boolean(bqEffectiveProjectId);
      const stage: Stage = !bqEffectiveProjectId
        ? 'setup'
        : needsReauth
          ? 'reconnect'
          : bqValidated
            ? (bqState.enabled ? 'active' : 'enable')
            : (bqState.enabled ? 'reconnect' : 'connect');
      const adcLabel = bqStatus?.detected ? `ADC${bqStatus.credentialsType ? ` · ${bqStatus.credentialsType}` : ''}` : 'ADC not detected';
      const projectIntro = bqEffectiveProjectIsDetected
        ? `Detected ${adcLabel} · ${bqEffectiveProjectId}`
        : `${bqEffectiveProjectId} via ${adcLabel}`;
      const projectHelp = bqProjectSuggested
        ? (bqDetectedProjectIsQuota
          ? 'Suggested from your ADC quota project. Edit if your BigQuery data lives in another project.'
          : bqDetectedProjectSource === 'adc.project_id'
            ? 'From your service account credentials. Edit to override.'
            : 'Suggested from your active Google Cloud SDK project. Edit to override.')
        : 'The project BigQuery queries run against.';
      // While signing in or auto-verifying, the card primary collapses to a
      // spinner; Cancel stays reachable inline so the user is never trapped.
      const cardFeedback: React.ReactNode = bqReauthRunning
        ? (
          <>
            {bqReauthPhase || 'Waiting for Google sign-in in your browser…'}{' '}
            <button type="button" className="cx-relink" onClick={() => void handleAdcReauthCancel()}>Cancel</button>
          </>
        )
        : bqReauthPhase
          ? bqReauthPhase
          : bqFeedback?.text;
      const cardFeedbackError = bqReauthRunning || bqReauthPhase ? false : bqFeedback?.error;
      const reauthBlock = bqReauthRunning ? (
        <div className="cx-reauth cx-reauth-running" role="status" aria-live="polite">
          <span className="cx-spin cx-spin-ghost" />
          <div className="cx-reauth-text">
            <strong>{bqReauthPhase || 'Waiting for Google sign-in in your browser…'}</strong>
            <p>Finish the Google sign-in that opened in your browser. This window updates on its own.</p>
          </div>
          <button type="button" className="cx-btn-ghost-sm" onClick={() => void handleAdcReauthCancel()}>Cancel</button>
        </div>
      ) : needsReauth ? (
        <div className="cx-callout cx-callout-warn" role="status" aria-live="polite">
          <IconInfo />
          <div className="cx-reauth-fallback">
            <p>{bqReauthFailed && bqReauth?.message ? bqReauth.message : 'Your saved Google sign-in has expired. Sign in again to keep using BigQuery.'}</p>
            <p className="cx-help">Prefer a terminal? Run this on macOS or Windows, then we&apos;ll re-verify:</p>
            <div className="cx-cmd">
              <code>{bqFallbackCommand}</code>
              <button
                type="button"
                className="cx-secret-reveal"
                aria-label="Copy gcloud sign-in command"
                onClick={() => void handleCopyFallbackCommand(bqFallbackCommand)}
              >
                {bqFallbackCopied ? 'Copied' : 'Copy'}
              </button>
            </div>
            {bqReauth?.state === 'gcloud_missing' ? (
              <p className="cx-help">Google Cloud CLI wasn&apos;t found — install it from <a href="https://cloud.google.com/sdk/docs/install" target="_blank" rel="noreferrer">cloud.google.com/sdk <IconExternal /></a>, then try again.</p>
            ) : null}
          </div>
        </div>
      ) : null;
      const intent = sheetIntent ?? (stage === 'reconnect' ? 'reconnect' : stage === 'setup' || stage === 'connect' ? 'setup' : 'edit');
      return {
        id, name: 'BigQuery', stage, tone: stage === 'active' ? 'live' : stage === 'reconnect' ? 'warn' : 'default',
        checkedAt: bqState.lastCheckedAt,
        eyebrow: stage === 'setup'
          ? 'Setup required'
          : stage === 'connect'
            ? (bqConfigChanged ? 'Unverified changes' : 'Not connected')
            : stage === 'active'
              ? 'Active'
              : stage === 'reconnect'
                ? (bqConfigChanged && !needsReauth ? 'Unverified changes' : 'Reconnect required')
                : 'Verified · off',
        instruction: stage === 'setup'
          ? <>Add or detect your <b>Google Cloud project ID</b> to continue.</>
          : stage === 'connect'
            ? (bqConfigChanged
              ? <>Setup changed since the last check — <b>verify</b> to apply it.</>
              : <>{projectIntro} — <b>verify</b> to link BigQuery.</>)
            : stage === 'reconnect'
              ? (needsReauth
                ? <>Sign-in expired — <b>reconnect</b> to keep using BigQuery.</>
                : bqConfigChanged
                  ? <>Setup changed since the last check — <b>verify</b> to apply it.</>
                  : <>Credentials stopped working — <b>reconnect</b> to restore access.</>)
              : stage === 'enable'
                ? <>{bqEffectiveProjectId} — <b>enable</b> to let the AI query it.</>
                : <>Querying datasets in {bqEffectiveProjectId}.</>,
        connecting: bqVerifying, connectingLabel: bqReauthRunning ? 'Signing in…' : 'Verifying…',
        primaryLabel: stage === 'setup' ? 'Set up' : stage === 'connect' ? 'Connect' : stage === 'reconnect' ? (needsReauth ? 'Sign in again' : 'Reconnect') : undefined,
        primaryIcon: stage === 'setup' ? 'gear' : stage === 'connect' ? 'arrow' : null,
        toggleOn: bqState.enabled === true, toggleLabel: bqState.enabled ? 'Disable BigQuery for the AI' : 'Enable BigQuery for the AI',
        onToggle: handleBigQueryToggle,
        toggleDisabled: bqVerifying,
        hasDetails: true,
        feedback: cardFeedback, feedbackError: cardFeedbackError,
        onEdit: () => openSheet('bigquery', 'edit'),
        usesSheet: true,
        sheetEyebrow: needsReauth
          ? 'Reconnect Google sign-in'
          : intent === 'edit' ? 'Edit connection' : intent === 'reconnect' ? 'Reconnect' : 'Set up connection',
        sheetBody: (
          <>
            <div className="cx-callout">
              <IconInfo />
              <p>
                BigQuery runs through the local server using Application Default Credentials. Save &amp;
                connect verifies access to your project.{' '}
                <a href="https://cloud.google.com/docs/authentication/application-default-credentials" target="_blank" rel="noreferrer">Setup guide <IconExternal /></a>
              </p>
            </div>
            {reauthBlock}
            <Field label={<ReqLabel>Google Cloud project ID</ReqLabel>} help={projectHelp}>
              <CxInput
                mono
                placeholder={bqDetectedProject || 'my-gcp-project-id'}
                value={localSettings.googleProjectId || ''}
                onChange={(e) => {
                  bqProjectTouchedRef.current = true;
                  setLocalSettings((prev) => ({ ...prev, googleProjectId: e.target.value }));
                }}
              />
              {bqDetectedProject ? (
                <div className="cx-detected-project">
                  <span>{bqProjectSuggested ? 'Suggested from ADC' : 'Detected ADC'}</span>
                  <code>{bqDetectedProject}</code>
                  {!bqProjectSuggested && bqProjectId !== bqDetectedProject ? (
                    <button
                      type="button"
                      onClick={() => {
                        bqProjectTouchedRef.current = true;
                        setLocalSettings((prev) => ({ ...prev, googleProjectId: bqDetectedProject }));
                      }}
                    >
                      Use
                    </button>
                  ) : null}
                </div>
              ) : null}
            </Field>
            <div className="cx-test">
              <button type="button" className="cx-btn-ghost-sm" disabled={bqVerifying || !bqEffectiveProjectId} onClick={() => void handleBigQueryConnect()}>
                {bqVerifying ? <span className="cx-spin cx-spin-ghost" /> : <IconCheck />}Test connection
              </button>
              {bqValidated && !bqFeedback ? <span className="cx-test-ok"><span className="cx-dot" />Connection verified</span> : null}
            </div>
            {bqFeedback && !bqReauthRunning && !bqReauthPhase ? <div className={`cx-feedback${bqFeedback.error ? ' cx-feedback-err' : ''}`} role="status">{bqFeedback.text}</div> : null}
            <Disclosure label="Advanced">
              <Field label="Custom credentials JSON path" help="Leave blank to auto-detect ADC.">
                <CxInput mono placeholder="~/.config/gcloud/application_default_credentials.json" value={localSettings.googleCredentialsPath || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, googleCredentialsPath: e.target.value }))} />
              </Field>
              {bqCustomPathConfigured ? (
                <div className="cx-feedback cx-feedback-err" role="status">
                  Google sign-in updates the <b>default</b> ADC file. With a custom path set here, BigQuery keeps reading the custom file — update or clear this path to pick up the new sign-in.
                </div>
              ) : null}
              <p className="cx-help">
                BigQuery bills by data scanned. These two limits decide when a query runs on its
                own, when it asks you first, and when it&apos;s blocked.
              </p>
              <Field
                label="Automatic scan limit (GiB)"
                help="Queries with an estimated scan up to this size run without asking. Leave blank for the default 100 GiB."
              >
                <CxInput
                  inputMode="decimal"
                  placeholder="100"
                  value={bqAutomaticGibText}
                  onChange={(e) => {
                    const text = e.target.value;
                    setBqScanDrafts((d) => ({ ...d, automatic: text }));
                    const parsed = parseGibInput(text);
                    if (parsed !== null) applyBigQueryScanBudget('automatic', parsed);
                  }}
                  onBlur={() => setBqScanDrafts({ automatic: null, approved: null })}
                />
              </Field>
              <Field
                label="One-time approval ceiling (GiB)"
                help="Estimates over the automatic limit pause and ask you each time. Set a ceiling to block anything larger than it without asking. Leave blank for no ceiling — oversized scans always ask first."
              >
                <CxInput
                  inputMode="decimal"
                  placeholder="No ceiling"
                  value={bqApprovedGibText}
                  onChange={(e) => {
                    const text = e.target.value;
                    setBqScanDrafts((d) => ({ ...d, approved: text }));
                    const parsed = parseGibInput(text);
                    if (parsed !== null) applyBigQueryScanBudget('approved', parsed);
                  }}
                  onBlur={() => setBqScanDrafts({ automatic: null, approved: null })}
                />
              </Field>
              {bqScanInputInvalid ? (
                <div className="cx-feedback cx-feedback-err" role="status">
                  Scan sizes must be numbers in GiB — like 250 or 1.5.
                </div>
              ) : bqApprovedBelowAutomatic ? (
                <div className="cx-feedback" role="status">
                  The approval ceiling can&apos;t be below the automatic limit — it will be saved
                  as {formatScanBytes(bqEffectiveAutomaticBytes)}.
                </div>
              ) : null}
            </Disclosure>
            <DangerZone name="BigQuery" onRemove={() => { removeConnection('bigquery'); }} />
          </>
        ),
        sheetFooter: (() => {
          const reason = !bqEffectiveProjectId ? 'Add your Google Cloud project ID to continue.' : '';
          if (needsReauth) {
            return (
              <SheetFooter
                label="Sign in again"
                busy={bqVerifying}
                busyLabel={bqReauthRunning ? 'Signing in…' : 'Verifying…'}
                disabled={Boolean(reason)}
                reason={reason || undefined}
                onClick={handleAdcReauthStart}
              />
            );
          }
          return (
            <SheetFooter
              label={intent === 'edit' ? 'Save & verify' : intent === 'reconnect' ? 'Reconnect' : 'Save & connect'}
              busy={bqVerifying}
              busyLabel="Verifying…"
              disabled={Boolean(reason)}
              reason={reason || undefined}
              onClick={handleBigQueryConnect}
            />
          );
        })(),
        detailsNode: (
          <>
            {confirmBlock(
              'bigquery',
              bqEffectiveProjectId || '—',
              `${adcLabel}${bqStatus?.credentialsSource ? ` · ${bqStatus.credentialsSource}` : ''}`,
              bqValidated
                ? 'Connection verified'
                : bqConfigChanged
                  ? 'Unverified changes — verify to apply'
                  : (bqState.lastCheckMessage || 'Not verified'),
              { accountLabel: 'Project', checkedAt: bqState.lastCheckedAt },
            )}
          </>
        ),
      };
    }

    if (id === 'databricks') {
    const dbStage: Stage = (!dbHost || !dbWarehouse)
      ? 'setup'
      : (!dbHasCredential || !dbValidated)
        ? (dbState.enabled ? 'reconnect' : 'connect')
        : (dbState.enabled ? 'active' : 'enable');
    const dbWorkspaceLabel = hostLabel(dbHost) || (dbStatus?.host ? hostLabel(dbStatus.host) : '') || 'Databricks';
    const dbCredentialLabel = dbRefreshSet ? 'OAuth refresh token' : dbPatSet ? 'Personal access token' : 'No credential';
    const dbSignOut = dbHasCredential
      ? {
        label: 'Sign out of Databricks',
        onClick: () => {
          if (dbRefreshSet) void handleClearDatabricksSecret('refresh_token');
          if (dbPatSet) void handleClearDatabricksSecret('pat');
          void persistDataSource('databricks', { enabled: false });
        },
        disabled: dbBusy,
      }
      : null;
    const intent = sheetIntent ?? (dbStage === 'reconnect' ? 'reconnect' : dbStage === 'setup' || dbStage === 'connect' ? 'setup' : 'edit');
    return {
      id, name: 'Databricks', stage: dbStage, tone: dbStage === 'active' ? 'live' : dbStage === 'reconnect' ? 'warn' : 'default',
      checkedAt: dbState.lastCheckedAt,
      eyebrow: dbStage === 'setup'
        ? 'Setup required'
        : dbStage === 'connect'
          ? (dbConfigChanged ? 'Unverified changes' : 'Not connected')
          : dbStage === 'active'
            ? 'Active'
            : dbStage === 'reconnect'
              ? (dbConfigChanged ? 'Unverified changes' : 'Reconnect required')
              : 'Verified · off',
      instruction: dbStage === 'setup'
        ? <>Add your <b>workspace host and warehouse</b> to continue.</>
        : dbStage === 'connect'
          ? (dbConfigChanged
            ? <>Setup changed since the last check — <b>verify</b> to apply it.</>
            : <>{dbWorkspaceLabel} — <b>{dbAuthMode === 'refresh_token' && !dbRefreshSet ? 'authorize' : 'verify'}</b> to link your warehouse.</>)
          : dbStage === 'reconnect'
            ? (dbConfigChanged
              ? <>Setup changed since the last check — <b>verify</b> to apply it.</>
              : <>Credential expired — <b>reconnect</b> to restore access.</>)
            : dbStage === 'enable'
              ? <>{dbWorkspaceLabel} · {dbCredentialLabel} — <b>enable</b> to let the AI query it.</>
              : <>Running SQL on {dbWorkspaceLabel}.</>,
      connecting: dbBusy, connectingLabel: pendingOAuthState ? 'Authorizing…' : 'Verifying…',
      primaryLabel: dbStage === 'setup' ? 'Set up' : dbStage === 'connect' ? 'Connect' : dbStage === 'reconnect' ? 'Reconnect' : undefined,
      primaryIcon: dbStage === 'setup' ? 'gear' : dbStage === 'connect' ? 'arrow' : null,
      onPrimary: (dbStage === 'connect' || dbStage === 'reconnect') ? handleDatabricksConnect : undefined,
      toggleOn: dbState.enabled === true, toggleLabel: dbState.enabled ? 'Disable Databricks for the AI' : 'Enable Databricks for the AI',
      onToggle: handleDatabricksToggle,
      hasDetails: true,
      feedback: dbFeedback?.text, feedbackError: dbFeedback?.error,
      disconnect: dbSignOut,
      onEdit: () => openSheet('databricks', 'edit'),
      detailsNode: (
        <>
          {confirmBlock(
            'databricks',
            dbWorkspaceLabel,
            dbCredentialLabel,
            dbValidated
              ? 'Connection verified'
              : dbConfigChanged
                ? 'Unverified changes — verify to apply'
                : (dbState.lastCheckMessage || 'Not verified'),
            { accountLabel: 'Workspace', checkedAt: dbState.lastCheckedAt },
          )}
        </>
      ),
      usesSheet: true,
      sheetEyebrow: intent === 'edit' ? 'Edit connection' : intent === 'reconnect' ? 'Reconnect' : 'Set up connection',
      sheetBody: (
        <>
          <div className="cx-callout">
            <IconInfo />
            <p>
              For a GCP Databricks workspace (<code>*.gcp.databricks.com</code>). Save &amp; connect opens
              OAuth to generate a refresh token, then verifies the warehouse.{' '}
              <a href="https://docs.databricks.com" target="_blank" rel="noreferrer">Setup guide <IconExternal /></a>
            </p>
          </div>
          <Field
            label={<ReqLabel>Workspace host URL</ReqLabel>}
            help={<>Found in your Databricks workspace URL, including the <code>https://</code> prefix.</>}
          >
            <CxInput mono placeholder="https://1234567890.7.gcp.databricks.com" value={localSettings.databricksHost || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, databricksHost: e.target.value }))} />
          </Field>
          <Field
            label={<ReqLabel>SQL warehouse ID</ReqLabel>}
            help="SQL Warehouses → your warehouse → Connection details → ID."
          >
            <CxInput mono placeholder="Enter your SQL warehouse ID" value={localSettings.databricksWarehouseId || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, databricksWarehouseId: e.target.value }))} />
          </Field>
          <div>
            <span className="cx-field-label" id="cx-db-authmode-label">Credential mode</span>
            <div className="cx-seg" role="radiogroup" aria-labelledby="cx-db-authmode-label">
              <button type="button" role="radio" aria-checked={dbAuthMode === 'refresh_token'} className={`cx-seg-btn${dbAuthMode === 'refresh_token' ? ' cx-on' : ''}`} onClick={() => setDbAuthMode('refresh_token')}>
                OAuth refresh token<span className="cx-seg-desc">Auto-renews. Recommended.</span>
              </button>
              <button type="button" role="radio" aria-checked={dbAuthMode === 'pat'} className={`cx-seg-btn${dbAuthMode === 'pat' ? ' cx-on' : ''}`} onClick={() => setDbAuthMode('pat')}>
                Personal access token<span className="cx-seg-desc">Static token, manual entry.</span>
              </button>
            </div>
          </div>
          {dbAuthMode === 'refresh_token'
            ? <SecretField label="Refresh token" placeholder="doau2c95f1…" isSet={dbRefreshSet} onSet={(v) => handleSetDatabricksSecret('refresh_token', v)} onClear={() => handleClearDatabricksSecret('refresh_token')} help="Optional — Save & connect can generate this via OAuth. Stored encrypted; never shown in chat." />
            : <SecretField label="Personal access token" placeholder="dapi…" isSet={dbPatSet} onSet={(v) => handleSetDatabricksSecret('pat', v)} onClear={() => handleClearDatabricksSecret('pat')} help="Stored encrypted; never shown in chat." />}
          {pendingOAuthState ? (
            <Field
              label="Authorization code"
              help={(
                <>
                  Complete authorization in your browser. If it does not open automatically
                  {pendingOAuthUrl ? (
                    <>
                      , <a href={pendingOAuthUrl} target="_blank" rel="noreferrer">open Databricks authorization</a>
                    </>
                  ) : null}
                  . If the redirect fails, paste the full redirect URL or code here.
                </>
              )}
            >
              <div className="cx-field-row">
                <CxInput mono placeholder="http://localhost:.../?code=…&state=…" value={manualOAuthInput} onChange={(e) => setManualOAuthInput(e.target.value)} />
                <button type="button" className="cx-btn-save" disabled={!manualOAuthInput.trim()} onClick={() => {
                  try {
                    const code = extractAuthorizationCode(manualOAuthInput, pendingOAuthState);
                    void submitOAuthCode(pendingOAuthState, code, 'manual');
                  } catch (err: any) {
                    toast.error(err?.message || 'Invalid authorization code.');
                  }
                }}>Submit</button>
              </div>
            </Field>
          ) : null}
          <div className="cx-test">
            <button type="button" className="cx-btn-ghost-sm" disabled={dbBusy || !dbHost || !dbWarehouse} onClick={() => void handleDatabricksTest()}>
              {dbBusy ? <span className="cx-spin cx-spin-ghost" /> : <IconCheck />}Test connection
            </button>
            {dbValidated && !dbFeedback ? <span className="cx-test-ok"><span className="cx-dot" />Connection verified</span> : null}
          </div>
          {dbFeedback ? <div className={`cx-feedback${dbFeedback.error ? ' cx-feedback-err' : ''}`} role="status">{dbFeedback.text}</div> : null}
          <Disclosure label="Advanced">
            <Field label="Custom OAuth client ID" help="Leave blank to use the bundled databricks-cli client.">
              <CxInput mono placeholder="databricks-cli" value={localSettings.databricksOAuthClientId || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, databricksOAuthClientId: e.target.value }))} />
            </Field>
          </Disclosure>
          <DangerZone name="Databricks" signOut={dbSignOut} onRemove={() => { removeConnection('databricks'); }} />
        </>
      ),
      sheetFooter: (() => {
        const dbReason = (!dbHost)
          ? 'Enter your workspace host URL to continue.'
          : (!dbHostIsGcp)
            ? 'Host must be a GCP workspace URL (ending in .gcp.databricks.com).'
            : (!dbWarehouse)
              ? 'Enter your SQL warehouse ID to continue.'
              : (dbAuthMode === 'pat' && !dbPatSet)
                ? 'Save a personal access token below, or switch to OAuth refresh token.'
                : '';
        return (
          <SheetFooter
            label={intent === 'edit' ? 'Save & verify' : intent === 'reconnect' ? 'Reconnect' : 'Save & connect'}
            busy={dbBusy}
            busyLabel={pendingOAuthState ? 'Authorizing…' : 'Verifying…'}
            disabled={Boolean(dbReason)}
            reason={dbReason || (dbAuthMode === 'refresh_token' && !dbRefreshSet ? 'Opens Databricks OAuth to generate a refresh token, then verifies.' : undefined)}
            onClick={handleDatabricksConnect}
          />
        );
      })(),
    };
    }

    if (id === 'trino') {
      const ready = Boolean(trinoHost && trinoCatalog && trinoSchema && trinoUser);
      const activeCredential = trinoAuthMode === 'none' ? true : trinoAuthMode === 'basic' ? trinoBasicSet : trinoJwtSet;
      const stage: Stage = !ready
        ? 'setup'
        : trinoValidated
          ? (trinoState.enabled ? 'active' : 'enable')
          : (trinoState.enabled ? 'reconnect' : 'connect');
      const trinoLabel = hostLabel(trinoHost) || 'Trino';
      const trinoSignOut = (trinoBasicSet || trinoJwtSet)
        ? {
          label: 'Sign out of Trino',
          onClick: () => {
            if (trinoAuthMode === 'basic') void handleClearTrinoSecret('basic');
            else if (trinoAuthMode === 'jwt') void handleClearTrinoSecret('jwt');
            void persistDataSource('trino', { enabled: false });
          },
          disabled: trinoBusy,
        }
        : null;
      const intent = sheetIntent ?? (stage === 'reconnect' ? 'reconnect' : stage === 'setup' || stage === 'connect' ? 'setup' : 'edit');
      return {
        id, name: 'Trino', stage, tone: stage === 'active' ? 'live' : stage === 'reconnect' ? 'warn' : 'default',
        checkedAt: trinoState.lastCheckedAt,
        eyebrow: stage === 'setup'
          ? 'Setup required'
          : stage === 'connect'
            ? (trinoConfigChanged ? 'Unverified changes' : 'Not connected')
            : stage === 'active'
              ? 'Active'
              : stage === 'reconnect'
                ? (trinoConfigChanged ? 'Unverified changes' : 'Reconnect required')
                : 'Verified · off',
        instruction: stage === 'setup'
          ? <>Add your <b>host, catalog, schema and user</b> to continue.</>
          : stage === 'connect'
            ? (trinoConfigChanged
              ? <>Setup changed since the last check — <b>verify</b> to apply it.</>
              : <>{trinoLabel} · {trinoCatalog || 'catalog'} — <b>verify</b> to link Trino.</>)
            : stage === 'reconnect'
              ? (trinoConfigChanged
                ? <>Setup changed since the last check — <b>verify</b> to apply it.</>
                : <>Connection stopped working — <b>reconnect</b> to restore access.</>)
              : stage === 'enable'
                ? <>{trinoLabel} — <b>enable</b> to let the AI query it.</>
                : <>Querying {trinoCatalog || 'catalogs'} on {trinoLabel}.</>,
        connecting: trinoBusy, connectingLabel: 'Verifying…',
        primaryLabel: stage === 'setup' ? 'Set up' : stage === 'connect' ? 'Connect' : stage === 'reconnect' ? 'Reconnect' : undefined,
        primaryIcon: stage === 'setup' ? 'gear' : stage === 'connect' ? 'arrow' : null,
        onPrimary: (stage === 'connect' || stage === 'reconnect') ? handleTrinoConnect : undefined,
        toggleOn: trinoState.enabled === true, toggleLabel: trinoState.enabled ? 'Disable Trino for the AI' : 'Enable Trino for the AI',
        onToggle: handleTrinoToggle,
        hasDetails: true,
        feedback: trinoFeedback?.text, feedbackError: trinoFeedback?.error,
        disconnect: trinoSignOut,
        onEdit: () => openSheet('trino', 'edit'),
        detailsNode: (
          <>
            {confirmBlock(
              'trino',
              `${trinoLabel}${trinoCatalog ? ` · ${trinoCatalog}` : ''}`,
              trinoAuthMode === 'basic' ? 'Basic auth' : trinoAuthMode === 'jwt' ? 'JWT bearer' : 'No authentication',
              trinoValidated
                ? 'Connection verified'
                : trinoConfigChanged
                  ? 'Unverified changes — verify to apply'
                  : (trinoState.lastCheckMessage || 'Not verified'),
              { accountLabel: 'Coordinator', checkedAt: trinoState.lastCheckedAt },
            )}
          </>
        ),
        usesSheet: true,
        sheetEyebrow: intent === 'edit' ? 'Edit connection' : intent === 'reconnect' ? 'Reconnect' : 'Set up connection',
        sheetBody: (
          <>
            <div className="cx-callout">
              <IconInfo />
              <p>
                Connects to a Trino coordinator over HTTP(S). Pick an authentication mode below, then
                Save &amp; connect to verify.{' '}
                <a href="https://trino.io/docs/current/" target="_blank" rel="noreferrer">Trino docs <IconExternal /></a>
              </p>
            </div>
            <Field label={<ReqLabel>Coordinator host URL</ReqLabel>} help="Include the https:// prefix and port if non-standard.">
              <CxInput mono placeholder="https://trino.example.com" value={localSettings.trinoHost || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, trinoHost: e.target.value }))} />
            </Field>
            <div className="cx-field-row">
              <Field label={<ReqLabel>Catalog</ReqLabel>}><CxInput mono placeholder="hive" value={localSettings.trinoCatalog || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, trinoCatalog: e.target.value }))} /></Field>
              <Field label={<ReqLabel>Schema</ReqLabel>}><CxInput mono placeholder="default" value={localSettings.trinoSchema || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, trinoSchema: e.target.value }))} /></Field>
            </div>
            <div className="cx-field-row">
              <Field label={<ReqLabel>User</ReqLabel>}><CxInput mono placeholder="analyst" value={localSettings.trinoUser || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, trinoUser: e.target.value }))} /></Field>
              <Field label={<ReqLabel optional>Source</ReqLabel>}><CxInput mono placeholder="ai-analyst" value={localSettings.trinoSource || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, trinoSource: e.target.value }))} /></Field>
            </div>
            <div>
              <span className="cx-field-label" id="cx-trino-auth-label">Authentication</span>
              <div className="cx-seg" role="radiogroup" aria-labelledby="cx-trino-auth-label">
                <button type="button" role="radio" aria-checked={trinoAuthMode === 'none'} className={`cx-seg-btn${trinoAuthMode === 'none' ? ' cx-on' : ''}`} onClick={() => setTrinoAuthMode('none')}>None<span className="cx-seg-desc">No auth headers.</span></button>
                <button type="button" role="radio" aria-checked={trinoAuthMode === 'basic'} className={`cx-seg-btn${trinoAuthMode === 'basic' ? ' cx-on' : ''}`} onClick={() => setTrinoAuthMode('basic')}>Basic<span className="cx-seg-desc">User & password.</span></button>
                <button type="button" role="radio" aria-checked={trinoAuthMode === 'jwt'} className={`cx-seg-btn${trinoAuthMode === 'jwt' ? ' cx-on' : ''}`} onClick={() => setTrinoAuthMode('jwt')}>JWT<span className="cx-seg-desc">Bearer token.</span></button>
              </div>
            </div>
            {trinoAuthMode === 'basic'
              ? <SecretField label="Basic password" placeholder="Trino password" isSet={trinoBasicSet} onSet={handleSetTrinoBasic} onClear={() => handleClearTrinoSecret('basic')} help="Saved against the user above. Stored encrypted; never shown in chat." />
              : trinoAuthMode === 'jwt'
                ? <SecretField label="JWT token" placeholder="eyJ…" isSet={trinoJwtSet} onSet={handleSetTrinoJwt} onClear={() => handleClearTrinoSecret('jwt')} help="Bearer token. Stored encrypted; never shown in chat." />
                : null}
            {trinoFeedback ? <div className={`cx-feedback${trinoFeedback.error ? ' cx-feedback-err' : ''}`} role="status">{trinoFeedback.text}</div> : null}
            <DangerZone name="Trino" signOut={trinoSignOut} onRemove={() => { removeConnection('trino'); }} />
          </>
        ),
        sheetFooter: (() => {
          const reason = !trinoHost
            ? 'Enter the coordinator host URL to continue.'
            : !trinoCatalog || !trinoSchema
              ? 'Enter the catalog and schema to continue.'
              : !trinoUser
                ? 'Enter the Trino user to continue.'
                : !activeCredential
                  ? (trinoAuthMode === 'basic' ? 'Save a basic password below, or switch auth mode.' : 'Save a JWT token below, or switch auth mode.')
                  : '';
          return (
            <SheetFooter
              label={intent === 'edit' ? 'Save & verify' : intent === 'reconnect' ? 'Reconnect' : 'Save & connect'}
              busy={trinoBusy}
              busyLabel="Verifying…"
              disabled={Boolean(reason)}
              reason={reason || undefined}
              onClick={handleTrinoConnect}
            />
          );
        })(),
      };
    }

    // snowflake
    const sfReady = Boolean(snowflakeAccount && snowflakeWarehouse && snowflakeDatabase && snowflakeSchema && snowflakeUser);
    const sfCredential = snowflakeAuthMode === 'oauth_token' ? snowflakeOAuthSet : snowflakeKeyPairSet;
    const sfStage: Stage = !sfReady
      ? 'setup'
      : snowflakeValidated
        ? (snowflakeState.enabled ? 'active' : 'enable')
        : (snowflakeState.enabled ? 'reconnect' : 'connect');
    const sfNeedsCredential = sfReady && !sfCredential;
    const sfCredentialLabel = snowflakeAuthMode === 'oauth_token' ? 'OAuth token' : 'Keypair JWT';
    const sfSignOut = sfCredential
      ? {
        label: 'Sign out of Snowflake',
        onClick: () => { void handleClearSnowflakeSecret(); void persistDataSource('snowflake', { enabled: false }); },
        disabled: snowflakeBusy,
      }
      : null;
    const sfIntent = sheetIntent ?? (sfStage === 'reconnect' ? 'reconnect' : sfStage === 'setup' || sfStage === 'connect' ? 'setup' : 'edit');
    return {
      id, name: 'Snowflake', stage: sfStage, tone: sfStage === 'active' ? 'live' : sfStage === 'reconnect' ? 'warn' : 'default',
      checkedAt: snowflakeState.lastCheckedAt,
      eyebrow: sfStage === 'setup'
        ? 'Setup required'
        : sfStage === 'connect'
          ? (snowflakeConfigChanged ? 'Unverified changes' : 'Not connected')
          : sfStage === 'active'
            ? 'Active'
            : sfStage === 'reconnect'
              ? (snowflakeConfigChanged ? 'Unverified changes' : 'Reconnect required')
              : 'Verified · off',
      instruction: sfStage === 'setup'
        ? <>Add your <b>account, warehouse, database, schema and user</b> to continue.</>
        : sfStage === 'connect' && sfNeedsCredential
          ? <>Add your <b>{sfCredentialLabel}</b> to continue.</>
          : sfStage === 'connect'
            ? (snowflakeConfigChanged
              ? <>Setup changed since the last check — <b>verify</b> to apply it.</>
              : <>{snowflakeAccount || 'Snowflake'} — <b>verify</b> to link your warehouse.</>)
          : sfStage === 'reconnect'
            ? (snowflakeConfigChanged
              ? <>Setup changed since the last check — <b>verify</b> to apply it.</>
              : <>Connection stopped working — <b>reconnect</b> to restore access.</>)
            : sfStage === 'enable'
              ? <>{snowflakeAccount} — <b>enable</b> to let the AI query it.</>
              : <>Querying {snowflakeDatabase || 'databases'} on {snowflakeAccount || 'Snowflake'}.</>,
      connecting: snowflakeBusy, connectingLabel: 'Verifying…',
      primaryLabel: sfStage === 'setup' ? 'Set up' : sfStage === 'connect' ? 'Connect' : sfStage === 'reconnect' ? 'Reconnect' : undefined,
      primaryIcon: sfStage === 'setup' ? 'gear' : sfStage === 'connect' ? 'arrow' : null,
      onPrimary: (sfStage === 'connect' || sfStage === 'reconnect') ? handleSnowflakeConnect : undefined,
      toggleOn: snowflakeState.enabled === true, toggleLabel: snowflakeState.enabled ? 'Disable Snowflake for the AI' : 'Enable Snowflake for the AI',
      onToggle: handleSnowflakeToggle,
      hasDetails: true,
      feedback: snowflakeFeedback?.text, feedbackError: snowflakeFeedback?.error,
      disconnect: sfSignOut,
      onEdit: () => openSheet('snowflake', 'edit'),
      detailsNode: (
        <>
          {confirmBlock(
            'snowflake',
            `${snowflakeAccount || 'Snowflake'}${snowflakeDatabase ? ` · ${snowflakeDatabase}` : ''}`,
            snowflakeAuthMode === 'oauth_token' ? 'OAuth token' : 'Keypair JWT',
            snowflakeValidated
              ? 'Connection verified'
              : snowflakeConfigChanged
                ? 'Unverified changes — verify to apply'
                : (snowflakeState.lastCheckMessage || 'Not verified'),
            { accountLabel: 'Account', checkedAt: snowflakeState.lastCheckedAt },
          )}
        </>
      ),
      usesSheet: true,
      sheetEyebrow: sfIntent === 'edit' ? 'Edit connection' : sfIntent === 'reconnect' ? 'Reconnect' : 'Set up connection',
      sheetBody: (
        <>
          <div className="cx-callout">
            <IconInfo />
            <p>
              Connects to a Snowflake account over the SQL API. Fill the warehouse details, choose an
              authentication mode, then Save &amp; connect to verify.{' '}
              <a href="https://docs.snowflake.com" target="_blank" rel="noreferrer">Snowflake docs <IconExternal /></a>
            </p>
          </div>
          <div className="cx-field-row">
            <Field label={<ReqLabel>Account</ReqLabel>} help="e.g. orgname-account or locator.region."><CxInput mono placeholder="xy12345.us-east-1" value={localSettings.snowflakeAccountIdentifier || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, snowflakeAccountIdentifier: e.target.value }))} /></Field>
            <Field label={<ReqLabel>Warehouse</ReqLabel>}><CxInput mono placeholder="ANALYTICS_WH" value={localSettings.snowflakeWarehouse || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, snowflakeWarehouse: e.target.value }))} /></Field>
          </div>
          <div className="cx-field-row">
            <Field label={<ReqLabel>Database</ReqLabel>}><CxInput mono placeholder="ANALYTICS" value={localSettings.snowflakeDatabase || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, snowflakeDatabase: e.target.value }))} /></Field>
            <Field label={<ReqLabel>Schema</ReqLabel>}><CxInput mono placeholder="PUBLIC" value={localSettings.snowflakeSchema || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, snowflakeSchema: e.target.value }))} /></Field>
          </div>
          <div className="cx-field-row">
            <Field label={<ReqLabel optional>Role</ReqLabel>}><CxInput mono placeholder="ANALYST_ROLE" value={localSettings.snowflakeRole || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, snowflakeRole: e.target.value }))} /></Field>
            <Field label={<ReqLabel>User</ReqLabel>}><CxInput mono placeholder="analyst_user" value={localSettings.snowflakeUser || ''} onChange={(e) => setLocalSettings((prev) => ({ ...prev, snowflakeUser: e.target.value }))} /></Field>
          </div>
          <div>
            <span className="cx-field-label" id="cx-sf-auth-label">Authentication</span>
            <div className="cx-seg" role="radiogroup" aria-labelledby="cx-sf-auth-label">
              <button type="button" role="radio" aria-checked={snowflakeAuthMode === 'oauth_token'} className={`cx-seg-btn${snowflakeAuthMode === 'oauth_token' ? ' cx-on' : ''}`} onClick={() => setSnowflakeAuthMode('oauth_token')}>OAuth token<span className="cx-seg-desc">Snowflake access token.</span></button>
              <button type="button" role="radio" aria-checked={snowflakeAuthMode === 'keypair_jwt'} className={`cx-seg-btn${snowflakeAuthMode === 'keypair_jwt' ? ' cx-on' : ''}`} onClick={() => setSnowflakeAuthMode('keypair_jwt')}>Keypair JWT<span className="cx-seg-desc">Private key signing.</span></button>
            </div>
          </div>
          {snowflakeAuthMode === 'oauth_token'
            ? <SecretField label="OAuth token" placeholder="eyJ…" isSet={snowflakeOAuthSet} onSet={handleSetSnowflakeOAuth} onClear={handleClearSnowflakeSecret} help="Stored encrypted; never shown in chat." />
            : (
              <>
                <SecretField label="Private key PEM" placeholder="-----BEGIN PRIVATE KEY-----" isSet={snowflakeKeyPairSet} onSet={handleSetSnowflakeKeyPair} onClear={handleClearSnowflakeSecret} help="PKCS#8 private key. Stored encrypted; never shown in chat." />
                <Field label={<ReqLabel optional>Key passphrase</ReqLabel>} help="Used once when saving the key, then cleared from this form.">
                  <CxInput type="password" mono name="snowflake-key-passphrase" autoComplete="new-password" placeholder="Key passphrase" value={snowflakeKeyPassphrase} onChange={(e) => setSnowflakeKeyPassphrase(e.target.value)} />
                </Field>
              </>
            )}
          {snowflakeFeedback ? <div className={`cx-feedback${snowflakeFeedback.error ? ' cx-feedback-err' : ''}`} role="status">{snowflakeFeedback.text}</div> : null}
          <DangerZone name="Snowflake" signOut={sfSignOut} onRemove={() => { removeConnection('snowflake'); }} />
        </>
      ),
      sheetFooter: (() => {
        const reason = !sfReady
          ? 'Fill account, warehouse, database, schema and user to continue.'
          : !sfCredential
            ? (snowflakeAuthMode === 'oauth_token' ? 'Save an OAuth token below, or switch to keypair JWT.' : 'Save a private key PEM below, or switch to OAuth token.')
            : '';
        return (
          <SheetFooter
            label={sfIntent === 'edit' ? 'Save & verify' : sfIntent === 'reconnect' ? 'Reconnect' : 'Save & connect'}
            busy={snowflakeBusy}
            busyLabel="Verifying…"
            disabled={Boolean(reason)}
            reason={reason || undefined}
            onClick={handleSnowflakeConnect}
          />
        );
      })(),
    };
  };

  // Strictly re-verify every configured credential connection on demand. Each
  // check endpoint validates against the real provider and persists health
  // server-side; the local copy is patched so rows update without a reload.
  const verifiableCount = getCredentialConnections(localSettings).length;
  const handleVerifyAll = React.useCallback(async () => {
    if (verifyAllBusy) return;
    setVerifyAllBusy(true);
    try {
      const states = getCredentialConnections(localSettingsRef.current);
      if (states.length === 0) return;
      const outcomes: ConnectionCheckOutcome[] = [];
      for (const state of states) {
        outcomes.push(await verifyConnectionCredentials(state.id, localSettingsRef.current));
      }
      const nextSettings = applyCheckOutcomes(localSettingsRef.current, outcomes);
      setLocalSettings(nextSettings);
      localSettingsRef.current = nextSettings;
      for (const outcome of outcomes) {
        if (!outcome.indeterminate) await persistConnectionSlice(outcome.id);
      }
      await Promise.all([
        refreshStatus(),
        refreshSharePointStatus(),
        refreshGitHubStatus(),
        refreshSlackStatus(),
        refreshBigQuery(),
        refreshDatabricks(),
        refreshTrino(),
        refreshSnowflake(),
      ]);
      const failed = outcomes.filter((o) => !o.ok && !o.indeterminate);
      const skipped = outcomes.filter((o) => o.indeterminate);
      if (failed.length > 0) {
        toast.warning(`${failed.length} connection${failed.length === 1 ? ' needs' : 's need'} re-authentication.`);
      } else if (skipped.length === outcomes.length) {
        toast.error('Connection checks could not run.');
      } else {
        toast.success('All connections verified.');
      }
    } finally {
      setVerifyAllBusy(false);
    }
  }, [
    verifyAllBusy, setLocalSettings, persistConnectionSlice,
    refreshStatus, refreshSharePointStatus, refreshGitHubStatus, refreshSlackStatus,
    refreshBigQuery, refreshDatabricks, refreshTrino, refreshSnowflake,
  ]);

  const views = visibleIds.map((vid) => deriveView(vid, sheet?.id === vid ? sheet.intent : null));
  const activeCount = views.filter((v) => v.stage === 'active').length;
  const openView = sheetOpenId ? views.find((v) => v.id === sheetOpenId) || null : null;
  // Retain the last opened view so the sheet can animate out after close.
  const lastOpenViewRef = React.useRef<View | null>(null);
  if (openView) lastOpenViewRef.current = openView;
  const sheetView = openView ?? lastOpenViewRef.current;

  // Note: there is deliberately no stage-drift auto-close here. The sheet
  // closes only when (a) the user closes it, (b) the action they started in
  // it succeeds (closeSheetFor in the success callbacks), or (c) the
  // connection is removed. Derived stage changes never slam it shut.

  // If a connection is removed while its sheet is open, close the sheet.
  React.useEffect(() => {
    if (sheet && !visibleSet.has(sheet.id)) closeSheetNow();
  }, [sheet, visibleSet, closeSheetNow]);

  // Deep-link landing: open the focused connection's setup window once, so a
  // "Reconnect" action from chat or a toast drops the user directly into the
  // re-auth flow instead of the full console list. Targets that are not in
  // the catalogue get an inline notice instead of a silent no-op.
  const focusHandledRef = React.useRef<ConnectionCatalogId | null>(null);
  React.useEffect(() => {
    if (!focusConnectionId || focusHandledRef.current === focusConnectionId) return;
    focusHandledRef.current = focusConnectionId;
    if (!visibleSet.has(focusConnectionId)) {
      const label = CATALOG.find((c) => c.id === focusConnectionId)?.name || focusConnectionId;
      setFocusNotice(`${label} isn't in your connections yet — use “Add connection” to set it up.`);
      return;
    }
    const view = views.find((v) => v.id === focusConnectionId);
    if (!view || !view.sheetBody) return;
    openSheet(
      focusConnectionId,
      view.stage === 'reconnect' ? 'reconnect' : (view.stage === 'setup' || view.stage === 'connect') ? 'setup' : 'edit',
    );
  });

  return (
    <div className="cx-console" id="cx-console-root" tabIndex={-1}>
      {catalogueOpen ? (
        <CataloguePicker
          visible={visibleSet}
          onAdd={(id) => {
            addConnection(id);
            setCatalogueOpen(false);
            if (id === 'local-computer' || id === 'image-generation' || id === 'python-sandbox') {
              openSheet(id, 'setup');
            }
          }}
          onClose={() => setCatalogueOpen(false)}
        />
      ) : null}

      <div className="cx-kicker">Settings</div>
      <header className="cx-header">
        <div>
          <h1 className="cx-title">Connections</h1>
          <p className="cx-sub">Link your tools, then choose what the AI can read and act on.</p>
        </div>
        <div className="cx-head-right">
          <span className="cx-counter"><b>{activeCount}</b> of {visibleIds.length} active</span>
          {verifiableCount > 0 ? (
            <button
              type="button"
              className="cx-btn-ghost"
              disabled={verifyAllBusy}
              aria-label="Re-verify all connection credentials"
              onClick={() => void handleVerifyAll()}
            >
              {verifyAllBusy ? <><span className="cx-spin" />Verifying…</> : <><IconRefresh />Verify all</>}
            </button>
          ) : null}
          <button type="button" className="cx-btn-ghost" onClick={() => setCatalogueOpen(true)}><IconPlus />Add connection</button>
        </div>
      </header>

      {focusNotice ? (
        <div className="cx-feedback cx-console-notice" role="status">
          <span>{focusNotice}</span>
          <button type="button" className="cx-relink" onClick={() => setFocusNotice(null)}>Dismiss</button>
        </div>
      ) : null}

      {visibleIds.length === 0 ? (
        <div className="cx-empty">
          <div className="cx-empty-title">No connections yet</div>
          <div className="cx-empty-sub">Add a connection to let the AI read and act on your tools and data.</div>
          <button type="button" className="cx-btn-ghost" onClick={() => setCatalogueOpen(true)}><IconPlus />Add your first connection</button>
        </div>
      ) : (
        <div className="cx-list">
          {views.map((v, i) => {
            const { sheetEyebrow: _se, sheetBody: _sb, sheetFooter: _sf, ...rowProps } = v;
            return (
              <ConnectionRow
                key={v.id}
                index={i}
                onRemove={() => removeConnection(v.id)}
                onOpenSheet={() => {
                  // Row primary: first-time setup for setup/connect stages,
                  // re-auth for broken ones.
                  openSheet(v.id, v.stage === 'reconnect' ? 'reconnect' : 'setup');
                }}
                {...rowProps}
              />
            );
          })}
        </div>
      )}

      {sheetView ? (
        <SideSheet
          open={Boolean(sheet)}
          onClose={requestCloseSheet}
          id={sheetView.id}
          name={sheetView.name}
          eyebrow={sheetView.sheetEyebrow}
          ariaLabel={`${sheetView.sheetEyebrow || intentEyebrow(sheet?.intent ?? null)} — ${sheetView.name}`}
          notice={sheetGuard ? (
            <div className="cx-sheet-guard" role="alert">
              <p>You have unsaved changes to this connection.</p>
              <div className="cx-sheet-guard-actions">
                <button type="button" className="cx-btn-ghost-sm" onClick={() => setSheetGuard(false)}>Keep editing</button>
                <button type="button" className="cx-btn-ghost-sm cx-guard-discard" onClick={discardSheetChanges}>Discard changes</button>
              </div>
            </div>
          ) : null}
          footer={sheetView.sheetFooter}
        >
          {sheetView.sheetBody}
        </SideSheet>
      ) : null}
    </div>
  );
};

export default McpIntegrationsTab;
