import React from 'react';
import { Database } from 'lucide-react';
import { CONNECTION_META } from '@/lib/connectionsCatalog';
import {
  getQueryProviderForToolName,
  isQueryRunToolName,
  isQueryToolName,
} from '@/lib/queryProviderRegistry';
import { registerCustomResolver } from '../../registry';
import type { RendererProps } from '../../types';
import { QueryInspector } from './QueryInspector';

/**
 * QueryToolRenderer — artifact-panel adapter for the unified QueryInspector.
 * Owns the inspector resolver claim ("does this artifact look like a query
 * tool's output?") and forwards to <QueryInspector>.
 *
 * The actual zone-by-zone rendering, state derivation, and provider
 * branding live inside QueryInspector + useQueryInspectorState; the state
 * derivation is shared with the compact chat card (QueryExecutionCard).
 */

type ProviderKey = 'bigquery' | 'databricks' | 'trino' | 'snowflake';

const SUPPORTED_PROVIDERS: ProviderKey[] = ['bigquery', 'databricks', 'trino', 'snowflake'];

function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function pickProvider(meta: Record<string, unknown>): ProviderKey | null {
  const qm = isObject(meta.query_metadata) ? (meta.query_metadata as Record<string, unknown>) : null;
  const rm = isObject(meta.result_metadata) ? (meta.result_metadata as Record<string, unknown>) : null;
  const candidates = [
    qm?.query_provider,
    qm?.provider,
    meta.query_provider,
    meta.provider,
    meta.integration,
    rm?.query_provider,
    rm?.provider,
    rm?.integration,
  ];
  for (const c of candidates) {
    const v = typeof c === 'string' ? c.toLowerCase() : null;
    if (v && SUPPORTED_PROVIDERS.includes(v as ProviderKey)) return v as ProviderKey;
  }
  const toolName = typeof meta.tool_name === 'string' ? meta.tool_name : '';
  const fromRegistry = getQueryProviderForToolName(toolName);
  if (fromRegistry && SUPPORTED_PROVIDERS.includes(fromRegistry as ProviderKey)) {
    return fromRegistry as ProviderKey;
  }
  return null;
}

const QueryToolRenderer: React.FC<RendererProps> = ({ artifact, toolResult, className }) => (
  <QueryInspector artifact={artifact} toolResult={toolResult} className={className} />
);

function isQueryArtifact(artifact: RendererProps['artifact']): boolean {
  const meta = (artifact.metadata || {}) as Record<string, unknown>;
  if (isObject(meta.query_metadata)) return true;
  const provider = String(meta.provider || '').toLowerCase();
  if (SUPPORTED_PROVIDERS.includes(provider as ProviderKey)) return true;
  const integration = String(meta.integration || '').toLowerCase();
  if (SUPPORTED_PROVIDERS.includes(integration as ProviderKey)) return true;
  const rm = isObject(meta.result_metadata) ? (meta.result_metadata as Record<string, unknown>) : null;
  if (rm) {
    const rint = String(rm.integration || '').toLowerCase();
    if (SUPPORTED_PROVIDERS.includes(rint as ProviderKey)) return true;
  }
  // Belt-and-braces: claim artifacts produced by known query and query
  // lifecycle tools, even if metadata is sparse. Uses the shared registry so
  // this stays aligned with the rest of the app's query-tool detection.
  if (typeof meta.tool_name === 'string' && (isQueryToolName(meta.tool_name) || isQueryRunToolName(meta.tool_name))) {
    return true;
  }
  return false;
}

registerCustomResolver((artifact) => {
  if (!isQueryArtifact(artifact)) return null;
  const provider = pickProvider((artifact.metadata || {}) as Record<string, unknown>);
  const meta = provider ? CONNECTION_META[provider] : null;
  return {
    component: QueryToolRenderer,
    label: meta?.label || 'Query',
    icon: meta?.icon || Database,
    ownsChrome: true,
  };
});

export default QueryToolRenderer;
