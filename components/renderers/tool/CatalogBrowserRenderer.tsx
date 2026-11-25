import React, { useMemo } from 'react';
import { Database, Table as TableIcon } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import ItemList from '../shared/ItemList';
import type { ItemListItem } from '../shared/ItemList';
import SchemaViewer from '@/components/SchemaViewer';
import { registerToolRenderer } from '../registry';
import type { RendererProps } from '../types';
import {
  getAllCatalogToolNames,
  getAllSchemaToolNames,
  getQueryMetadataToolDescriptor,
} from '@/lib/queryProviderRegistry';

const SCHEMA_TOOLS = new Set(getAllSchemaToolNames());

function extractItems(raw: unknown): string[] | null {
  if (Array.isArray(raw)) {
    return raw.map((item) => {
      if (typeof item === 'string') return item;
      if (item && typeof item === 'object') {
        return (item as Record<string, unknown>).name
          ?? (item as Record<string, unknown>).id
          ?? (item as Record<string, unknown>).table_name
          ?? (item as Record<string, unknown>).schema_name
          ?? JSON.stringify(item);
      }
      return String(item);
    }) as string[];
  }
  if (raw && typeof raw === 'object' && !Array.isArray(raw)) {
    // Look for first array-valued key
    for (const val of Object.values(raw as Record<string, unknown>)) {
      if (Array.isArray(val)) return extractItems(val);
    }
  }
  return null;
}

function extractSchema(raw: unknown): Array<{ name: string; type?: string; mode?: string; description?: string }> | null {
  if (!raw || typeof raw !== 'object') return null;
  const obj = raw as Record<string, unknown>;
  for (const key of ['schema', 'columns', 'fields']) {
    if (Array.isArray(obj[key])) {
      const arr = obj[key] as Array<Record<string, unknown>>;
      if (arr.length > 0 && typeof arr[0] === 'object' && arr[0].name) {
        return arr as Array<{ name: string; type?: string; mode?: string; description?: string }>;
      }
    }
  }
  // If the raw result itself is an array of column objects
  if (Array.isArray(raw)) {
    const arr = raw as Array<Record<string, unknown>>;
    if (arr.length > 0 && arr[0].name && (arr[0].type || arr[0].data_type)) {
      return arr.map((c) => ({
        name: String(c.name),
        type: String(c.type ?? c.data_type ?? ''),
        mode: c.mode ? String(c.mode) : undefined,
        description: c.description ? String(c.description) : undefined,
      }));
    }
  }
  return null;
}

const CatalogBrowserRenderer: React.FC<RendererProps> = ({ artifact }) => {
  const toolName = (artifact.metadata?.tool_name as string) || '';
  const descriptor = useMemo(() => getQueryMetadataToolDescriptor(toolName), [toolName]);
  const isSchemaView = descriptor?.kind === 'table-schema' || SCHEMA_TOOLS.has(toolName);
  const raw = artifact.metadata?.raw_result;

  const schema = useMemo(() => {
    if (!isSchemaView) return null;
    const s = extractSchema(raw);
    if (s) return s;
    // Fallback: try parsing artifact.content
    try {
      return extractSchema(JSON.parse(artifact.content));
    } catch {
      return null;
    }
  }, [raw, artifact.content, isSchemaView]);

  const items = useMemo<ItemListItem[] | null>(() => {
    if (isSchemaView && schema) return null;
    const parsed = extractItems(raw) ?? (() => {
      try { return extractItems(JSON.parse(artifact.content)); } catch { return null; }
    })();
    if (!parsed) return null;
    const isTable = descriptor?.kind === 'tables' || descriptor?.kind === 'table-schema' || toolName.includes('table');
    return parsed.map((label) => ({
      icon: isTable ? <TableIcon size={12} /> : <Database size={12} />,
      label: String(label),
    }));
  }, [artifact.content, descriptor?.kind, isSchemaView, raw, schema, toolName]);

  const itemCount = schema?.length ?? items?.length ?? 0;
  const itemTerm = descriptor?.term || { singular: 'Item', plural: 'Items' };
  const countLabel = itemCount === 1 ? itemTerm.singular : itemTerm.plural;
  const fallbackTitle = itemCount === 1 ? itemTerm.singular : itemTerm.plural;

  return (
    <div className="h-full flex flex-col bg-background rounded-lg border border-border overflow-hidden">
      {/* Header */}
      <div className="flex items-center gap-2 px-3 py-1.5 bg-muted/50 border-b border-border min-w-0">
        <Database size={14} className="text-muted-foreground shrink-0" />
        <span className="text-xs font-semibold text-foreground truncate">
          {artifact.title || (descriptor ? fallbackTitle : 'Catalog')}
        </span>
        {itemCount > 0 && (
          <Badge variant="secondary" className="text-[10px] shrink-0">
            {itemCount} {countLabel}
          </Badge>
        )}
      </div>

      {/* Body */}
      <div className="flex-1 overflow-auto">
        {isSchemaView && schema ? (
          <SchemaViewer schema={schema} />
        ) : items ? (
          <ItemList items={items} searchable={items.length > 10} />
        ) : (
          <pre className="p-4 text-xs font-mono text-foreground whitespace-pre-wrap">
            {artifact.content}
          </pre>
        )}
      </div>
    </div>
  );
};

const CATALOG_TOOLS = getAllCatalogToolNames();

for (const name of CATALOG_TOOLS) {
  registerToolRenderer(name, {
    component: CatalogBrowserRenderer,
    label: 'Catalog',
    icon: Database,
  });
}

export default CatalogBrowserRenderer;
