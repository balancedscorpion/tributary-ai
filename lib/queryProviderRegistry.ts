import type { LucideIcon } from 'lucide-react';
import { Database, Zap, Triangle, Snowflake } from 'lucide-react';
import type { QueryProvider } from '@/types';

type NullableString = string | null | undefined;

/* ------------------------------------------------------------------ */
/*  Provider brand system                                              */
/* ------------------------------------------------------------------ */

export interface QueryProviderBrandConfig {
  icon: LucideIcon;
  label: string;
  accentBorder: string;
  badgeBg: string;
  badgeText: string;
  badgeBorder: string;
  progressFill: string;
  progressTrack: string;
  dotColor: string;
  glowShadow: string;
  textActive: string;
}

const PROVIDER_BRAND: Record<QueryProvider, QueryProviderBrandConfig> = {
  bigquery: {
    icon: Database,
    label: 'BigQuery',
    accentBorder: 'border-l-blue-400',
    badgeBg: 'bg-blue-500/10',
    badgeText: 'text-blue-400',
    badgeBorder: 'border-blue-500/20',
    progressFill: 'bg-blue-400',
    progressTrack: 'bg-blue-500/15',
    dotColor: 'bg-blue-400',
    glowShadow: 'shadow-[0_0_8px_-2px_rgba(96,165,250,0.4)]',
    textActive: 'text-blue-400',
  },
  databricks: {
    icon: Zap,
    label: 'Databricks',
    accentBorder: 'border-l-red-400',
    badgeBg: 'bg-red-500/10',
    badgeText: 'text-red-400',
    badgeBorder: 'border-red-500/20',
    progressFill: 'bg-red-400',
    progressTrack: 'bg-red-500/15',
    dotColor: 'bg-red-400',
    glowShadow: 'shadow-[0_0_8px_-2px_rgba(248,113,113,0.4)]',
    textActive: 'text-red-400',
  },
  trino: {
    icon: Triangle,
    label: 'Trino',
    accentBorder: 'border-l-purple-400',
    badgeBg: 'bg-purple-500/10',
    badgeText: 'text-purple-400',
    badgeBorder: 'border-purple-500/20',
    progressFill: 'bg-purple-400',
    progressTrack: 'bg-purple-500/15',
    dotColor: 'bg-purple-400',
    glowShadow: 'shadow-[0_0_8px_-2px_rgba(192,132,252,0.4)]',
    textActive: 'text-purple-400',
  },
  snowflake: {
    icon: Snowflake,
    label: 'Snowflake',
    accentBorder: 'border-l-cyan-400',
    badgeBg: 'bg-cyan-500/10',
    badgeText: 'text-cyan-400',
    badgeBorder: 'border-cyan-500/20',
    progressFill: 'bg-cyan-400',
    progressTrack: 'bg-cyan-500/15',
    dotColor: 'bg-cyan-400',
    glowShadow: 'shadow-[0_0_8px_-2px_rgba(34,211,238,0.4)]',
    textActive: 'text-cyan-400',
  },
};

export function getQueryProviderBrand(provider: unknown): QueryProviderBrandConfig {
  const normalized = isQueryProvider(provider) ? provider : 'bigquery';
  return PROVIDER_BRAND[normalized];
}

export interface QueryProviderJobLink {
  id: string;
  url: string;
}

export interface QueryProviderJobLinkInput {
  providerJobId?: NullableString;
  providerJobUrl?: NullableString;
  warehouseJobId?: NullableString;
  statementId?: NullableString;
  queryId?: NullableString;
  projectId?: NullableString;
  accountIdentifier?: NullableString;
  location?: NullableString;
  host?: NullableString;
  url?: NullableString;
  jobId?: NullableString;
}

export type QueryMetadataToolKind = 'databases' | 'schemas' | 'tables' | 'table-schema';

export interface QueryMetadataTerm {
  singular: string;
  plural: string;
}

export interface QueryProviderMetadataTerminology {
  databases: QueryMetadataTerm;
  schemas: QueryMetadataTerm;
  tables: QueryMetadataTerm;
  tableSchema: QueryMetadataTerm;
}

export interface QueryMetadataToolDescriptor {
  provider: QueryProvider;
  kind: QueryMetadataToolKind;
  term: QueryMetadataTerm;
}

interface QueryProviderConfig {
  id: QueryProvider;
  label: string;
  runQueryTools: readonly string[];
  metadataTools: {
    databases: readonly string[];
    schemas: readonly string[];
    tables: readonly string[];
    tableSchema: readonly string[];
  };
  progressLinkLabel: string;
  terminology: QueryProviderMetadataTerminology;
  resolveNativeJobLink: (input: QueryProviderJobLinkInput) => QueryProviderJobLink | null;
}

const PROVIDER_IDS: QueryProvider[] = ['bigquery', 'databricks', 'trino', 'snowflake'];

function normalizeString(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const next = value.trim();
  return next ? next : null;
}

function toHttpsHost(host: string): string {
  const trimmed = host.trim().replace(/\/+$/, '');
  if (!trimmed) return '';
  if (trimmed.startsWith('https://')) return trimmed;
  if (trimmed.startsWith('http://')) return `https://${trimmed.slice('http://'.length)}`;
  return `https://${trimmed}`;
}

function buildBigQueryJobURL(projectID: string, location: string, warehouseJobID: string): string {
  const encodedProject = encodeURIComponent(projectID);
  const encodedJobRef = encodeURIComponent(`bq:${location}:${warehouseJobID}`);
  return `https://console.cloud.google.com/bigquery?project=${encodedProject}&j=${encodedJobRef}&page=queryresults`;
}

function buildDatabricksHistoryURL(host: string, statementID: string): string {
  const normalizedHost = toHttpsHost(host);
  return `${normalizedHost}/sql/history?filter=%7B%22statementId%22%3A%22${encodeURIComponent(statementID)}%22%7D`;
}

function normalizeSnowflakeHost(accountOrHost: string): string {
  const trimmed = accountOrHost.trim().replace(/^https?:\/\//, '').replace(/\/+$/, '');
  if (!trimmed) return '';
  const hostPart = trimmed.split('/')[0];
  if (!hostPart) return '';
  if (hostPart.endsWith('.snowflakecomputing.com')) return hostPart;
  return `${hostPart}.snowflakecomputing.com`;
}

function buildSnowflakeHistoryURL(accountOrHost: string, statementID: string): string {
  const host = normalizeSnowflakeHost(accountOrHost);
  const safeID = encodeURIComponent(statementID);
  return `https://${host}/console#/monitoring/queries/detail?queryId=${safeID}`;
}

const QUERY_PROVIDER_CONFIGS: Record<QueryProvider, QueryProviderConfig> = {
  bigquery: {
    id: 'bigquery',
    label: 'BigQuery',
    runQueryTools: ['run_bigquery_query'],
    metadataTools: {
      databases: ['list_bq_datasets'],
      schemas: [],
      tables: ['list_bq_tables'],
      tableSchema: ['get_bq_table_schema'],
    },
    progressLinkLabel: 'View in BigQuery Console',
    terminology: {
      databases: { singular: 'Dataset', plural: 'Datasets' },
      schemas: { singular: 'Schema', plural: 'Schemas' },
      tables: { singular: 'Table', plural: 'Tables' },
      tableSchema: { singular: 'Table Schema', plural: 'Table Schemas' },
    },
    resolveNativeJobLink: (input) => {
      const warehouseJobID =
        normalizeString(input.providerJobId)
        || normalizeString(input.warehouseJobId)
        || normalizeString(input.jobId);
      if (!warehouseJobID) return null;

      const directURL = normalizeString(input.providerJobUrl);
      if (directURL) return { id: warehouseJobID, url: directURL };

      const projectID = normalizeString(input.projectId);
      const location = normalizeString(input.location);
      if (!projectID || !location) return null;
      return { id: warehouseJobID, url: buildBigQueryJobURL(projectID, location, warehouseJobID) };
    },
  },
  databricks: {
    id: 'databricks',
    label: 'Databricks',
    runQueryTools: ['run_databricks_query'],
    metadataTools: {
      databases: ['list_databricks_catalogs'],
      schemas: ['list_databricks_schemas'],
      tables: ['list_databricks_tables'],
      tableSchema: ['describe_databricks_table'],
    },
    progressLinkLabel: 'View in Query History',
    terminology: {
      databases: { singular: 'Catalog', plural: 'Catalogs' },
      schemas: { singular: 'Schema', plural: 'Schemas' },
      tables: { singular: 'Table', plural: 'Tables' },
      tableSchema: { singular: 'Table Schema', plural: 'Table Schemas' },
    },
    resolveNativeJobLink: (input) => {
      const statementID =
        normalizeString(input.providerJobId)
        || normalizeString(input.statementId)
        || normalizeString(input.jobId);
      if (!statementID) return null;

      const directURL = normalizeString(input.providerJobUrl) || normalizeString(input.url);
      if (directURL) return { id: statementID, url: directURL };

      const host = normalizeString(input.host);
      if (!host) return null;
      return { id: statementID, url: buildDatabricksHistoryURL(host, statementID) };
    },
  },
  trino: {
    id: 'trino',
    label: 'Trino',
    runQueryTools: ['run_trino_query'],
    metadataTools: {
      databases: ['list_trino_catalogs'],
      schemas: ['list_trino_schemas'],
      tables: ['list_trino_tables'],
      tableSchema: ['describe_trino_table'],
    },
    progressLinkLabel: 'View in Trino UI',
    terminology: {
      databases: { singular: 'Catalog', plural: 'Catalogs' },
      schemas: { singular: 'Schema', plural: 'Schemas' },
      tables: { singular: 'Table', plural: 'Tables' },
      tableSchema: { singular: 'Table Schema', plural: 'Table Schemas' },
    },
    resolveNativeJobLink: (input) => {
      const queryID =
        normalizeString(input.providerJobId)
        || normalizeString(input.statementId)
        || normalizeString(input.jobId);
      const directURL = normalizeString(input.providerJobUrl) || normalizeString(input.url);
      if (!directURL) return null;
      return { id: queryID || 'query', url: directURL };
    },
  },
  snowflake: {
    id: 'snowflake',
    label: 'Snowflake',
    runQueryTools: ['run_snowflake_query'],
    metadataTools: {
      databases: ['list_snowflake_databases'],
      schemas: ['list_snowflake_schemas'],
      tables: ['list_snowflake_tables'],
      tableSchema: ['describe_snowflake_table'],
    },
    progressLinkLabel: 'View in Snowsight',
    terminology: {
      databases: { singular: 'Database', plural: 'Databases' },
      schemas: { singular: 'Schema', plural: 'Schemas' },
      tables: { singular: 'Table', plural: 'Tables' },
      tableSchema: { singular: 'Table Schema', plural: 'Table Schemas' },
    },
    resolveNativeJobLink: (input) => {
      const statementID =
        normalizeString(input.providerJobId)
        || normalizeString(input.queryId)
        || normalizeString(input.statementId)
        || normalizeString(input.jobId);
      if (!statementID) return null;

      const directURL = normalizeString(input.providerJobUrl) || normalizeString(input.url);
      if (directURL) return { id: statementID, url: directURL };

      const accountOrHost = normalizeString(input.accountIdentifier) || normalizeString(input.host);
      if (!accountOrHost) return null;
      return { id: statementID, url: buildSnowflakeHistoryURL(accountOrHost, statementID) };
    },
  },
};

const QUERY_RUN_TOOL_TO_PROVIDER = new Map<string, QueryProvider>();
const QUERY_TOOL_TO_PROVIDER = new Map<string, QueryProvider>();
const QUERY_RUN_TOOL_NAMES = new Set<string>();
const QUERY_TOOL_NAMES = new Set<string>(['check_query_status', 'get_query_results', 'cancel_query']);
const CATALOG_TOOL_NAMES = new Set<string>();
const SCHEMA_TOOL_NAMES = new Set<string>();
const METADATA_TOOL_KIND_BY_NAME = new Map<string, QueryMetadataToolKind>();

for (const provider of PROVIDER_IDS) {
  const cfg = QUERY_PROVIDER_CONFIGS[provider];
  for (const toolName of cfg.runQueryTools) {
    QUERY_RUN_TOOL_NAMES.add(toolName);
    QUERY_RUN_TOOL_TO_PROVIDER.set(toolName, provider);
    QUERY_TOOL_TO_PROVIDER.set(toolName, provider);
    QUERY_TOOL_NAMES.add(toolName);
  }

  const entries: Array<[QueryMetadataToolKind, readonly string[]]> = [
    ['databases', cfg.metadataTools.databases],
    ['schemas', cfg.metadataTools.schemas],
    ['tables', cfg.metadataTools.tables],
    ['table-schema', cfg.metadataTools.tableSchema],
  ];
  for (const [kind, toolNames] of entries) {
    for (const toolName of toolNames) {
      CATALOG_TOOL_NAMES.add(toolName);
      QUERY_TOOL_TO_PROVIDER.set(toolName, provider);
      METADATA_TOOL_KIND_BY_NAME.set(toolName, kind);
      if (kind === 'table-schema') SCHEMA_TOOL_NAMES.add(toolName);
    }
  }
}

export function isQueryProvider(value: unknown): value is QueryProvider {
  return value === 'bigquery' || value === 'databricks' || value === 'trino' || value === 'snowflake';
}

export function normalizeQueryProvider(value: unknown, fallback: QueryProvider = 'bigquery'): QueryProvider {
  return isQueryProvider(value) ? value : fallback;
}

export function getQueryProviderLabel(provider: unknown): string {
  const normalized = normalizeQueryProvider(provider, 'bigquery');
  return QUERY_PROVIDER_CONFIGS[normalized].label;
}

export function getQueryProviderProgressLinkLabel(provider: unknown): string {
  const normalized = normalizeQueryProvider(provider, 'bigquery');
  return QUERY_PROVIDER_CONFIGS[normalized].progressLinkLabel;
}

export function isQueryRunToolName(toolName: string): boolean {
  return QUERY_RUN_TOOL_NAMES.has(String(toolName || '').trim());
}

export function isQueryToolName(toolName: string): boolean {
  return QUERY_TOOL_NAMES.has(String(toolName || '').trim());
}

export function getQueryProviderForToolName(toolName: string): QueryProvider | null {
  return QUERY_TOOL_TO_PROVIDER.get(String(toolName || '').trim()) || null;
}

export function getAllCatalogToolNames(): string[] {
  return Array.from(CATALOG_TOOL_NAMES);
}

export function getAllSchemaToolNames(): string[] {
  return Array.from(SCHEMA_TOOL_NAMES);
}

export function getQueryProviderMetadataTerminology(provider: unknown): QueryProviderMetadataTerminology {
  const normalized = normalizeQueryProvider(provider, 'bigquery');
  return QUERY_PROVIDER_CONFIGS[normalized].terminology;
}

export function getQueryMetadataToolDescriptor(toolName: string): QueryMetadataToolDescriptor | null {
  const normalizedToolName = String(toolName || '').trim();
  if (!normalizedToolName) return null;
  const provider = QUERY_TOOL_TO_PROVIDER.get(normalizedToolName) || null;
  const kind = METADATA_TOOL_KIND_BY_NAME.get(normalizedToolName) || null;
  if (!provider || !kind) return null;

  const terminology = getQueryProviderMetadataTerminology(provider);
  const term =
    kind === 'databases' ? terminology.databases :
    kind === 'schemas' ? terminology.schemas :
    kind === 'tables' ? terminology.tables :
    terminology.tableSchema;

  return { provider, kind, term };
}

export function resolveQueryNativeJobLink(provider: unknown, input: QueryProviderJobLinkInput): QueryProviderJobLink | null {
  const normalized = normalizeQueryProvider(provider, 'bigquery');
  return QUERY_PROVIDER_CONFIGS[normalized].resolveNativeJobLink(input);
}
