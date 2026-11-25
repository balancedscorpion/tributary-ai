import type { LucideIcon } from 'lucide-react';
import {
  Database,
  Zap,
  Triangle,
  Snowflake,
  Github,
  Slack,
  Globe2,
  Monitor,
  BookOpen,
  Search,
  Files,
  ImageIcon,
  Code2,
} from 'lucide-react';
import type { AppSettings, ConnectionCatalogId, ProviderConfig } from '@/types';
import { getQueryProviderForToolName } from '@/lib/queryProviderRegistry';
import { supportsProviderHostedTools } from '@/lib/capability/providerConfig';

/**
 * Single source of truth for the per-chat tool-audit surface. Mirrors the
 * connection catalogue used by the settings console (ConnectionCatalogId), but
 * exposes a normalized, settings-derived audit view that the chat header pill,
 * popover, and transcript record all consume.
 *
 * Readiness is intentionally derived from local settings only so the pill never
 * needs a network round-trip to render. Live status (e.g. GitHub login, Slack
 * workspace) is layered on top via useConnectionStatuses purely to enrich the
 * identity line — it is never required for correctness.
 */

export const CONNECTION_CATALOG_IDS: ConnectionCatalogId[] = [
  'thread-search',
  'image-generation',
  'web-search',
  'python-sandbox',
  'local-computer',
  'bigquery',
  'databricks',
  'trino',
  'snowflake',
  'github',
  'slack',
  'atlassian',
  'sharepoint',
  'browser',
  'arxiv',
];

export interface ConnectionMeta {
  id: ConnectionCatalogId;
  label: string;
  /** Real brand mark served from public/brand-logos. */
  logo: string;
  /** Dark/monochrome marks (e.g. GitHub) must be tinted light on the dark tile. */
  mono?: boolean;
  /** Fallback lucide icon if the logo asset fails to load. */
  icon: LucideIcon;
}

export const CONNECTION_META: Record<ConnectionCatalogId, ConnectionMeta> = {
  'thread-search': { id: 'thread-search', label: 'Search Threads', logo: '/brand-logos/search-threads.svg', icon: Search },
  'image-generation': { id: 'image-generation', label: 'Image Generation', logo: '/brand-logos/image-generation.svg', icon: ImageIcon },
  'web-search': { id: 'web-search', label: 'Web Search', logo: '/brand-logos/web-search.svg', icon: Globe2 },
  'python-sandbox': { id: 'python-sandbox', label: 'Python Sandbox', logo: '/brand-logos/python-sandbox.svg', icon: Code2 },
  'local-computer': { id: 'local-computer', label: 'Local Computer', logo: '/brand-logos/local-computer.svg', icon: Monitor },
  bigquery: { id: 'bigquery', label: 'BigQuery', logo: '/brand-logos/bigquery.svg', icon: Database },
  databricks: { id: 'databricks', label: 'Databricks', logo: '/brand-logos/databricks.svg', icon: Zap },
  trino: { id: 'trino', label: 'Trino', logo: '/brand-logos/trino.svg', icon: Triangle },
  snowflake: { id: 'snowflake', label: 'Snowflake', logo: '/brand-logos/snowflake.svg', icon: Snowflake },
  github: { id: 'github', label: 'GitHub', logo: '/brand-logos/github.svg', mono: true, icon: Github },
  slack: { id: 'slack', label: 'Slack', logo: '/brand-logos/slack.svg', icon: Slack },
  atlassian: { id: 'atlassian', label: 'Atlassian', logo: '/brand-logos/atlassian.svg', icon: Globe2 },
  sharepoint: { id: 'sharepoint', label: 'SharePoint', logo: '/brand-logos/sharepoint.svg', icon: Files },
  browser: { id: 'browser', label: 'Browser', logo: '/brand-logos/chrome.svg', icon: Monitor },
  arxiv: { id: 'arxiv', label: 'arXiv', logo: '/brand-logos/arxiv.svg', icon: BookOpen },
};

export type ConnectionReadiness = 'ready' | 'needs-setup';

export interface ConnectionAuditInfo {
  id: ConnectionCatalogId;
  meta: ConnectionMeta;
  /**
   * 'ready'  — authenticated/verified and globally enabled, so it can be armed
   *            for a thread.
   * 'needs-setup' — visible in the catalogue but not yet usable (needs auth or
   *            configuration to complete).
   */
  readiness: ConnectionReadiness;
  /** Identity/workspace/project/account line, settings-derived. */
  identity: string;
  /** Short list of enabled tool capability labels. */
  capabilities: string[];
  /** One-line hint shown when readiness is 'needs-setup'. */
  setupHint?: string;
}

function hostLabel(raw: unknown): string {
  const value = String(raw || '').trim();
  if (!value) return '';
  try {
    const url = new URL(value.includes('://') ? value : `https://${value}`);
    return url.host || value;
  } catch {
    return value.replace(/^https?:\/\//, '').replace(/\/+$/, '');
  }
}

function compact(...parts: Array<string | undefined | null>): string {
  return parts.map((p) => String(p || '').trim()).filter(Boolean).join(' · ');
}

function enabledHostedTargets(
  settings: AppSettings,
  key: 'webSearch' | 'codeExecution',
): ProviderConfig[] {
  const controlKey = key === 'webSearch' ? 'hosted_web_search' : 'hosted_code_execution';
  return (settings.providerConfigs || []).filter(
    (config) => {
      if (!supportsProviderHostedTools(config)) return false;
      const control = config.capabilityControls?.[controlKey];
      if (control != null) return control === 'on';
      return config[key]?.enabled === true;
    },
  );
}

/** Is the connection currently listed in the user's connection catalogue? */
export function getVisibleConnectionIds(settings: AppSettings | null | undefined): ConnectionCatalogId[] {
  const raw = settings?.connections?.visibleIds;
  if (!Array.isArray(raw)) return [];
  const seen = new Set<ConnectionCatalogId>();
  const out: ConnectionCatalogId[] = [];
  for (const id of raw) {
    if (CONNECTION_META[id as ConnectionCatalogId] && !seen.has(id as ConnectionCatalogId)) {
      seen.add(id as ConnectionCatalogId);
      out.push(id as ConnectionCatalogId);
    }
  }
  return out;
}

function deriveOne(id: ConnectionCatalogId, settings: AppSettings): ConnectionAuditInfo {
  const meta = CONNECTION_META[id];
  let readiness: ConnectionReadiness = 'needs-setup';
  let identity = '';
  let capabilities: string[] = [];
  let setupHint: string | undefined;

  switch (id) {
    case 'thread-search': {
      const ts = settings.threadSearch;
      readiness = ts?.enabled === true ? 'ready' : 'needs-setup';
      identity = 'Local app history';
      capabilities = ['Search threads', 'Read messages'];
      setupHint = 'Enable Search Threads to retrieve prior chat context.';
      break;
    }
    case 'image-generation': {
      const image = settings.imageGeneration;
      const target = (settings.providerConfigs || []).find((config) => config.id === image?.providerId);
      const targetReady = Boolean(target && (target.providerId === 'openai' || target.providerId === 'compatible'));
      readiness = image?.enabled === true && targetReady ? 'ready' : 'needs-setup';
      identity = target?.displayName || 'OpenAI-family provider';
      capabilities = ['Generate images', image?.model || 'gpt-image-2'];
      setupHint = targetReady
        ? 'Enable image generation for the selected provider.'
        : 'Select an OpenAI or compatible provider for image generation.';
      break;
    }
    case 'web-search': {
      const targets = enabledHostedTargets(settings, 'webSearch');
      readiness = targets.length > 0 ? 'ready' : 'needs-setup';
      identity = targets.length > 0
        ? targets.map((target) => target.displayName).join(', ')
        : 'Provider-hosted search';
      capabilities = ['Search the web', 'Return cited sources'];
      setupHint = 'Enable web search for at least one supported provider connection.';
      break;
    }
    case 'python-sandbox': {
      const targets = enabledHostedTargets(settings, 'codeExecution');
      readiness = targets.length > 0 ? 'ready' : 'needs-setup';
      identity = targets.length > 0
        ? targets.map((target) => target.displayName).join(', ')
        : 'Provider-managed runtime';
      capabilities = ['Run Python', 'Analyze files'];
      setupHint = 'Enable the Python sandbox for at least one supported provider connection.';
      break;
    }
    case 'local-computer': {
      const lc = settings.localComputer;
      readiness = lc?.enabled === true ? 'ready' : 'needs-setup';
      const selectedCount = Array.isArray(lc?.selectedFolders) ? lc.selectedFolders.length : 0;
      identity = lc?.accessMode === 'selected-folders'
        ? selectedCount > 0
          ? `${selectedCount} selected ${selectedCount === 1 ? 'folder' : 'folders'}`
          : 'Selected folders'
        : 'Whole computer';
      capabilities = ['Search files', 'Read files'];
      setupHint = 'Set up read-only local computer access.';
      break;
    }
    case 'bigquery': {
      const state = settings.dataSources?.bigquery;
      readiness = state?.enabled === true ? 'ready' : 'needs-setup';
      const project = String(settings.googleProjectId || state?.lastProjectId || '').trim();
      identity = compact(project || 'Google Cloud', 'authorized_user');
      capabilities = ['List datasets', 'Run query'];
      setupHint = 'Connect Google Cloud to query BigQuery.';
      break;
    }
    case 'databricks': {
      const state = settings.dataSources?.databricks;
      readiness = state?.enabled === true ? 'ready' : 'needs-setup';
      identity = compact(hostLabel(settings.databricksHost || state?.lastHost), settings.databricksWarehouseId || state?.lastWarehouseId || undefined) || 'Databricks';
      capabilities = ['Browse catalog', 'Run SQL'];
      setupHint = 'Add your workspace host and warehouse to continue.';
      break;
    }
    case 'trino': {
      const state = settings.dataSources?.trino;
      readiness = state?.enabled === true ? 'ready' : 'needs-setup';
      const scope = compact(settings.trinoCatalog, settings.trinoSchema);
      identity = compact(hostLabel(settings.trinoHost || state?.lastHost) || 'Trino', scope || undefined, settings.trinoUser || undefined);
      capabilities = ['Browse catalog', 'Run SQL'];
      setupHint = 'Add your host, catalog, schema and user to continue.';
      break;
    }
    case 'snowflake': {
      const state = settings.dataSources?.snowflake;
      readiness = state?.enabled === true ? 'ready' : 'needs-setup';
      identity = compact(settings.snowflakeAccountIdentifier || state?.lastHost || 'Snowflake', settings.snowflakeDatabase || undefined, settings.snowflakeUser || undefined);
      capabilities = ['Browse catalog', 'Run SQL'];
      setupHint = 'Add your account, warehouse, database, schema and user.';
      break;
    }
    case 'github': {
      const gh = settings.github;
      readiness = gh?.enabled === true ? 'ready' : 'needs-setup';
      identity = 'GitHub account';
      capabilities = ['Read repos', 'Issues & PRs'];
      setupHint = 'Sign in to GitHub to enable repository tools.';
      break;
    }
    case 'slack': {
      const sl = settings.slack;
      readiness = sl?.enabled === true ? 'ready' : 'needs-setup';
      identity = 'Slack workspace';
      capabilities = sl?.writeTools ? ['Read messages', 'Post messages'] : ['Read messages'];
      setupHint = 'Sign in to Slack to enable workspace tools.';
      break;
    }
    case 'atlassian': {
      const at = settings.atlassian;
      const jira = at?.jira?.enabled === true;
      const confluence = at?.confluence?.enabled === true;
      readiness = jira || confluence ? 'ready' : 'needs-setup';
      identity = hostLabel(at?.jiraBaseUrl || at?.confluenceBaseUrl) || 'Atlassian';
      capabilities = [jira ? 'Jira' : '', confluence ? 'Confluence' : ''].filter(Boolean);
      if (capabilities.length === 0) capabilities = ['Jira', 'Confluence'];
      setupHint = 'Connect Jira or Confluence to continue.';
      break;
    }
    case 'sharepoint': {
      const sp = settings.sharepoint;
      readiness = sp?.enabled === true ? 'ready' : 'needs-setup';
      identity = hostLabel(sp?.siteUrl) || 'SharePoint';
      capabilities = ['Search files', 'Read documents'];
      setupHint = 'Connect SharePoint to search and read documents.';
      break;
    }
    case 'browser': {
      const bc = settings.browserControl;
      readiness = bc?.enabled === true ? 'ready' : 'needs-setup';
      identity = 'Local · this device';
      capabilities = ['Navigate', 'Read page'];
      setupHint = 'Enable browser control to let the AI navigate the web.';
      break;
    }
    case 'arxiv': {
      const ax = settings.arxiv;
      readiness = ax?.enabled === true ? 'ready' : 'needs-setup';
      identity = 'arxiv.org · public API';
      capabilities = ['Search papers', 'Download PDFs'];
      setupHint = 'Enable arXiv to search and download research papers.';
      break;
    }
  }

  return { id, meta, readiness, identity, capabilities, setupHint };
}

/** Audit view for every connection the user has added to their catalogue. */
export function deriveVisibleConnections(settings: AppSettings | null | undefined): ConnectionAuditInfo[] {
  if (!settings) return [];
  return getVisibleConnectionIds(settings).map((id) => deriveOne(id, settings));
}

/** The connection ids that are ready to be armed (verified + globally enabled). */
export function getReadyConnectionIds(settings: AppSettings | null | undefined): ConnectionCatalogId[] {
  return deriveVisibleConnections(settings)
    .filter((c) => c.readiness === 'ready')
    .map((c) => c.id);
}

const TOOL_PREFIX_MATCHERS: Array<{ id: ConnectionCatalogId; test: (name: string) => boolean }> = [
  {
    id: 'image-generation',
    test: (n) => n === 'generate_image',
  },
  {
    id: 'web-search',
    test: (n) => /(^|\/)(web_search|web-search)(?:$|\/)/.test(n),
  },
  {
    id: 'python-sandbox',
    test: (n) => /(^|\/)(code_interpreter|code_execution|python)(?:$|\/)/.test(n),
  },
  {
    id: 'local-computer',
    test: (n) => /^(file_search|local_list_folder|local_read_file|local_open_file|local_read_pdf_page|local_list_grant_folder|local_search_in_grant|local_read_grant_file)$/.test(n),
  },
  {
    id: 'thread-search',
    test: (n) => /^(search_threads|search_docs|get_doc|get_artifact|list_messages|rlm_exec)$/.test(n),
  },
  { id: 'github', test: (n) => /^github_/.test(n) },
  { id: 'slack', test: (n) => /^slack_/.test(n) },
  { id: 'arxiv', test: (n) => /^arxiv_/.test(n) },
  { id: 'atlassian', test: (n) => /^(atlassian_|jira_|confluence_)/.test(n) },
  { id: 'sharepoint', test: (n) => /^sharepoint_/.test(n) },
  {
    id: 'browser',
    test: (n) =>
      n.startsWith('cua_browser') ||
      n.includes('browser') ||
      n.includes('playwright') ||
      n.includes('computer'),
  },
];

/** Map a tool name to the connection it was executed through, if any. */
export function getConnectionForToolName(toolName: string): ConnectionCatalogId | null {
  const name = String(toolName || '').trim();
  if (!name) return null;
  const provider = getQueryProviderForToolName(name);
  if (provider) return provider as ConnectionCatalogId;
  for (const matcher of TOOL_PREFIX_MATCHERS) {
    if (matcher.test(name)) return matcher.id;
  }
  return null;
}
