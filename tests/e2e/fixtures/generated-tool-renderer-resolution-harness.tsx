import React from 'react';
import { createRoot } from 'react-dom/client';
import ArtifactPanel from '@/components/ArtifactPanel';
import ToolResultBlock from '@/components/chat/ToolResultBlock';
import { TooltipProvider } from '@/components/ui/tooltip';
import type { AppSettings, Artifact } from '@/types';
import {
  GENERATED_STATIC_PROVIDER_HOSTED_TOOL_CAPABILITIES,
  GENERATED_STATIC_TOOL_METADATA,
} from '@/lib/generatedToolMetadata';

type GeneratedToolMetadata = (typeof GENERATED_STATIC_TOOL_METADATA)[number];
type GeneratedProviderHostedCapability = (typeof GENERATED_STATIC_PROVIDER_HOSTED_TOOL_CAPABILITIES)[number];

type RendererCase = {
  id: string;
  toolName: string;
  expectedHeader: string;
  artifact: Artifact;
};

type FullRendererCase = {
  id: string;
  toolName: string;
  sentinel: string;
  artifact: Artifact;
};

type ProviderHostedRendererCase = {
  id: string;
  toolName: string;
  expectedHeader: string;
  sentinel: string;
  artifact: Artifact;
};

type ToolResultRouteCase = FullRendererCase | ProviderHostedRendererCase;

type BoundaryState = {
  error: Error | null;
};

class RendererErrorBoundary extends React.Component<React.PropsWithChildren<{ id: string }>, BoundaryState> {
  state: BoundaryState = { error: null };

  static getDerivedStateFromError(error: Error): BoundaryState {
    return { error };
  }

  render() {
    if (this.state.error) {
      return (
        <div data-renderer-error={this.props.id}>
          {this.state.error.message || String(this.state.error)}
        </div>
      );
    }
    return this.props.children;
  }
}

function metadataFor(name: string): GeneratedToolMetadata {
  const meta = GENERATED_STATIC_TOOL_METADATA.find((tool) => tool.name === name);
  if (!meta) throw new Error(`Generated metadata missing ${name}`);
  return meta;
}

function baseMetadata(meta: GeneratedToolMetadata, extra: Record<string, unknown> = {}): Record<string, unknown> {
  const connectorId = 'connector_id' in meta ? meta.connector_id : undefined;
  return {
    ok: true,
    tool_name: meta.name,
    tool_args: {},
    tool_presentation: meta.presentation,
    result_metadata: {
      tool_name: meta.name,
      tool_presentation: meta.presentation,
      integration: connectorId || meta.presentation.family,
    },
    raw_result: {},
    ...extra,
  };
}

function artifactFor(meta: GeneratedToolMetadata, artifact: Omit<Artifact, 'metadata'> & { metadata?: Record<string, unknown> }): Artifact {
  return {
    ...artifact,
    metadata: baseMetadata(meta, artifact.metadata || {}),
  };
}

function safeId(value: string): string {
  return value.replace(/[^a-zA-Z0-9_-]+/g, '-').replace(/^-+|-+$/g, '') || 'tool';
}

function isSchemaCatalogTool(toolName: string): boolean {
  return (
    toolName === 'get_bq_table_schema' ||
    toolName === 'describe_databricks_table' ||
    toolName === 'describe_trino_table' ||
    toolName === 'describe_snowflake_table'
  );
}

function fixtureResultFor(meta: GeneratedToolMetadata, sentinel: string): Record<string, unknown> {
  const name = meta.name;

  if (name === 'list_files') {
    return { files: [`${sentinel}.txt`], has_more: false };
  }
  if (name === 'search_files') {
    return {
      matches: [{ path: `${sentinel}.txt`, line: 1, content: sentinel }],
      total_matches: 1,
      has_more: false,
    };
  }
  if (name === 'search_threads' || name === 'search_docs') {
    return {
      results: [{ doc_id: `doc-${sentinel}`, title: sentinel, snippet: sentinel, score: 0.91 }],
      count: 1,
      file_search: { available: true, matches: [] },
    };
  }
  if (name === 'describe_dataset') {
    return {
      path: `${sentinel}.csv`,
      row_count: 1,
      column_count: 1,
      columns: [{ name: sentinel, type: 'string', nulls: 0, cardinality: 1 }],
    };
  }
  if (name === 'aggregate_csv') {
    return { results: [{ group: sentinel, count: 1 }], row_count_total: 1 };
  }
  if (name === 'rlm_exec') {
    return { status: 'completed', final_answer: sentinel, stdout: sentinel, operations: 1 };
  }
  if (name === 'github_run_gh_command') {
    return { command: 'gh status', stdout: sentinel, stderr: '', exit_code: 0 };
  }
  if (isSchemaCatalogTool(name)) {
    return { schema: [{ name: sentinel, type: 'STRING' }] };
  }
  if (name.startsWith('list_') && (name.includes('_tables') || name.includes('_schemas') || name.includes('_catalogs') || name.includes('_databases') || name.includes('_datasets'))) {
    const key = name.includes('_tables')
      ? 'tables'
      : name.includes('_schemas')
        ? 'schemas'
        : name.includes('_databases')
          ? 'databases'
          : name.includes('_datasets')
            ? 'datasets'
            : 'catalogs';
    return { [key]: [{ name: sentinel }] };
  }
  if (meta.presentation.native_viewer === 'query') {
    return {
      status: 'completed',
      job_id: `job-${sentinel}`,
      sql: `select '${sentinel}' as renderer_sentinel`,
      rows: [{ renderer_sentinel: sentinel }],
      schema: [{ name: 'renderer_sentinel', type: 'STRING' }],
    };
  }
  if (meta.presentation.native_viewer === 'file') {
    return { file_name: `${sentinel}.txt`, content: sentinel, size: sentinel.length };
  }
  if (meta.presentation.native_viewer === 'code') {
    return { content: sentinel, language: 'text', size: sentinel.length };
  }
  return {
    items: [{ name: sentinel, value: 1 }],
    sentinel,
    count: 1,
  };
}

function artifactTypeFor(meta: GeneratedToolMetadata): Artifact['type'] {
  switch (String(meta.presentation.native_viewer)) {
    case 'file':
      return 'file';
    case 'code':
    case 'interactive_html':
      return 'code';
    case 'image':
      return 'image';
    case 'data':
    case 'chart':
    case 'query':
    case 'connector_inspector':
    default:
      return 'data';
  }
}

function artifactForGeneratedTool(meta: GeneratedToolMetadata): FullRendererCase {
  const id = `all-${safeId(meta.name)}`;
  const sentinel = `renderer-all-${safeId(meta.name)}-sentinel`;
  const result = fixtureResultFor(meta, sentinel);
  const artifactType = artifactTypeFor(meta);
  const nativeViewer = String(meta.presentation.native_viewer);
  const content =
    nativeViewer === 'code'
      ? sentinel
      : nativeViewer === 'interactive_html'
        ? `<!doctype html><html><body>${sentinel}</body></html>`
        : nativeViewer === 'file'
          ? sentinel
          : nativeViewer === 'data' || nativeViewer === 'chart'
            ? JSON.stringify([{ renderer_sentinel: sentinel }])
          : artifactType === 'image'
            ? 'data:image/png;base64,iVBORw0KGgo='
            : JSON.stringify(result);
  const queryProvider =
    meta.tool_set_id === 'bigquery' ||
    meta.tool_set_id === 'databricks' ||
    meta.tool_set_id === 'trino' ||
    meta.tool_set_id === 'snowflake'
      ? meta.tool_set_id
      : nativeViewer === 'query'
        ? 'bigquery'
        : undefined;
  const connectorId = 'connector_id' in meta ? meta.connector_id : undefined;

  const metadata: Record<string, unknown> = {
    raw_result: result,
    result_metadata: {
      tool_name: meta.name,
      tool_set_id: meta.tool_set_id,
      category: meta.category,
      origin: meta.origin,
      integration: connectorId || meta.tool_set_id,
      kind: meta.presentation.result_kind,
      renderer_strategy: meta.presentation.renderer_strategy,
      renderer_key: meta.presentation.renderer_key,
      native_viewer: meta.presentation.native_viewer,
      tool_presentation: meta.presentation,
    },
  };

  if (queryProvider) {
    metadata.provider = queryProvider;
    metadata.query_metadata = {
      provider: queryProvider,
      query_provider: queryProvider,
      sql: `select '${sentinel}' as renderer_sentinel`,
      job_id: `job-${sentinel}`,
      status: 'completed',
      schema: [{ name: 'renderer_sentinel', type: 'STRING' }],
    };
  }
  if (nativeViewer === 'interactive_html') {
    metadata.render_mode = 'sandboxed_html';
    metadata.sandbox = 'allow-scripts';
    metadata.network = 'blocked';
    metadata.artifact_kind = 'interactive_html';
  }
  if (artifactType === 'file') {
    metadata.detectedMimeType = 'text/plain';
    metadata.file_name = `${sentinel}.txt`;
    metadata.sizeBytes = sentinel.length;
  }

  return {
    id,
    toolName: meta.name,
    sentinel,
    artifact: artifactFor(meta, {
      id: `fixture-${id}`,
      type: artifactType,
      title: `Renderer ${meta.name}`,
      language: artifactType === 'code' ? 'text' : undefined,
      content,
      metadata,
    }),
  };
}

function providerHostedPresentation() {
  return {
    family: 'provider-hosted',
    result_kind: 'connector',
    renderer_strategy: 'family',
    renderer_key: 'provider-hosted',
    native_viewer: 'connector_inspector',
  };
}

function providerHostedHeader(capability: GeneratedProviderHostedCapability): string {
  if (capability.adapter_family === 'anthropic-messages') return 'Anthropic';
  if (capability.adapter_family === 'openai-responses') return 'OpenAI Responses';
  return 'Tool';
}

function artifactForProviderHostedCapability(capability: GeneratedProviderHostedCapability): ProviderHostedRendererCase {
  const id = `hosted-${safeId(capability.id)}`;
  const sentinel = `renderer-${safeId(capability.id)}-sentinel`;
  const presentation = providerHostedPresentation();
  const isSearch = capability.settings_key === 'webSearch';
  const result = isSearch
    ? {
        query: sentinel,
        results: [
          {
            title: `${sentinel} result`,
            url: 'https://example.com/provider-hosted',
            snippet: sentinel,
          },
        ],
      }
    : {
        status: 'completed',
        stdout: sentinel,
        files: [{ name: `${sentinel}.txt`, content: sentinel }],
      };
  return {
    id,
    toolName: capability.id,
    expectedHeader: providerHostedHeader(capability),
    sentinel,
    artifact: {
      id: `fixture-${id}`,
      type: 'data',
      title: `Renderer ${capability.id}`,
      content: JSON.stringify(result),
      metadata: {
        ok: true,
        tool_name: capability.name,
        tool_args: isSearch ? { query: sentinel } : { code: `print(${JSON.stringify(sentinel)})` },
        tool_presentation: presentation,
        raw_result: result,
        result_metadata: {
          provider_tool: true,
          provider_hosted_tool_id: capability.id,
          adapter_family: capability.adapter_family,
          category: capability.category,
          settings_key: capability.settings_key,
          tool_presentation: presentation,
        },
      },
    },
  };
}

function rendererCases(): RendererCase[] {
  const displayTable = metadataFor('display_table');
  const chart = metadataFor('build_chart_artifact');
  const code = metadataFor('git_show_file_at_ref');
  const file = metadataFor('read_attachment');
  const interactive = metadataFor('create_interactive_artifact');
  const query = metadataFor('run_bigquery_query');
  const catalog = metadataFor('get_bq_table_schema');
  const connector = metadataFor('slack_get_messages');

  return [
    {
      id: 'data',
      toolName: displayTable.name,
      expectedHeader: 'Interactive Data',
      artifact: artifactFor(displayTable, {
        id: 'fixture-display-table',
        type: 'data',
        title: 'Renderer Matrix Data',
        content: JSON.stringify([{ region: 'EMEA', revenue: 12 }]),
      }),
    },
    {
      id: 'chart',
      toolName: chart.name,
      expectedHeader: 'Interactive Data',
      artifact: artifactFor(chart, {
        id: 'fixture-chart',
        type: 'data',
        title: 'Renderer Matrix Chart',
        content: JSON.stringify([{ region: 'EMEA', revenue: 12 }]),
        metadata: {
          plot_spec: { engine: 'echarts', type: 'bar', x: 'region', y: 'revenue' },
          chart_engine: 'echarts',
        },
      }),
    },
    {
      id: 'code',
      toolName: code.name,
      expectedHeader: 'Source Code',
      artifact: artifactFor(code, {
        id: 'fixture-code',
        type: 'code',
        title: 'Renderer Matrix Code',
        language: 'text',
        content: 'renderer-code-sentinel',
      }),
    },
    {
      id: 'file',
      toolName: file.name,
      expectedHeader: 'File',
      artifact: artifactFor(file, {
        id: 'fixture-file',
        type: 'file',
        title: 'Renderer Matrix File',
        content: 'renderer-file-sentinel',
        metadata: {
          detectedMimeType: 'text/plain',
          file_name: 'renderer-file.txt',
          sizeBytes: 22,
        },
      }),
    },
    {
      id: 'interactive',
      toolName: interactive.name,
      expectedHeader: 'Interactive HTML',
      artifact: artifactFor(interactive, {
        id: 'fixture-interactive',
        type: 'code',
        title: 'Renderer Matrix Interactive',
        language: 'html',
        content: '<!doctype html><html><body>renderer-interactive-sentinel</body></html>',
        metadata: {
          render_mode: 'sandboxed_html',
          sandbox: 'allow-scripts',
          network: 'blocked',
          artifact_kind: 'interactive_html',
        },
      }),
    },
    {
      id: 'query',
      toolName: query.name,
      expectedHeader: 'BigQuery',
      artifact: artifactFor(query, {
        id: 'fixture-query',
        type: 'data',
        title: 'Renderer Matrix Query',
        content: JSON.stringify([{ value: 1 }]),
        metadata: {
          provider: 'bigquery',
          query_metadata: {
            provider: 'bigquery',
            query_provider: 'bigquery',
            sql: 'select 1 as renderer_query_sentinel',
            job_id: 'bq_renderer_matrix',
            status: 'completed',
            schema: [{ name: 'value', type: 'INTEGER' }],
          },
        },
      }),
    },
    {
      id: 'catalog',
      toolName: catalog.name,
      expectedHeader: 'Catalog',
      artifact: artifactFor(catalog, {
        id: 'fixture-catalog',
        type: 'data',
        title: 'Renderer Matrix Catalog',
        content: JSON.stringify({
          schema: [{ name: 'catalog_schema_sentinel', type: 'STRING' }],
        }),
      }),
    },
    {
      id: 'connector',
      toolName: connector.name,
      expectedHeader: 'Slack',
      artifact: artifactFor(connector, {
        id: 'fixture-connector',
        type: 'data',
        title: 'Renderer Matrix Connector',
        content: JSON.stringify({
          channel_id: 'C123',
          messages: [{ text: 'renderer-slack-sentinel' }],
        }),
        metadata: {
          raw_result: {
            channel_id: 'C123',
            messages: [{ text: 'renderer-slack-sentinel' }],
          },
        },
      }),
    },
  ];
}

function everyGeneratedToolRendererCase(): FullRendererCase[] {
  return GENERATED_STATIC_TOOL_METADATA.map(artifactForGeneratedTool);
}

function providerHostedRendererCases(): ProviderHostedRendererCase[] {
  return GENERATED_STATIC_PROVIDER_HOSTED_TOOL_CAPABILITIES.map(artifactForProviderHostedCapability);
}

function toolResultEnvelopeForCase(tc: ToolResultRouteCase): string {
  const metadata = (tc.artifact.metadata || {}) as Record<string, any>;
  const resultMetadata = (metadata.result_metadata || {}) as Record<string, any>;
  const result = metadata.raw_result ?? {};
  const envelope: Record<string, unknown> = {
    artifact_id: tc.artifact.id,
    tool_name: tc.toolName,
    tool_call_id: `call-${tc.id}`,
    ok: true,
    result_format: 'json',
    result,
    result_metadata: resultMetadata,
    tool_args: metadata.tool_args || {},
  };
  for (const key of ['query_metadata', 'plot_spec', 'chart_engine']) {
    if (metadata[key] !== undefined) envelope[key] = metadata[key];
  }
  return JSON.stringify(envelope);
}

const settings = {
  systemPrompt: '',
  featureFlags: {},
} as AppSettings;

export async function mountGeneratedToolRendererResolutionHarness(): Promise<Array<{ id: string; toolName: string; expectedHeader: string }>> {
  const rootNode = document.createElement('div');
  rootNode.id = 'generated-tool-renderer-resolution-root';
  document.body.appendChild(rootNode);
  const cases = rendererCases();
  const root = createRoot(rootNode);
  root.render(
    <TooltipProvider>
      <div className="space-y-4">
        {cases.map((tc) => (
          <section
            key={tc.id}
            data-testid={`renderer-case-${tc.id}`}
            data-tool-name={tc.toolName}
            data-expected-header={tc.expectedHeader}
            style={{ height: 360, border: '1px solid #ddd', margin: 12 }}
          >
            <RendererErrorBoundary id={tc.id}>
              <ArtifactPanel
                artifact={tc.artifact}
                settings={settings}
                onClose={() => undefined}
              />
            </RendererErrorBoundary>
          </section>
        ))}
      </div>
    </TooltipProvider>,
  );
  await new Promise((resolve) => window.setTimeout(resolve, 50));
  return cases.map((tc) => ({
    id: tc.id,
    toolName: tc.toolName,
    expectedHeader: tc.expectedHeader,
  }));
}

export async function mountEveryGeneratedToolRendererHarness(): Promise<Array<{ id: string; toolName: string; sentinel: string; nativeViewer: string }>> {
  const rootNode = document.createElement('div');
  rootNode.id = 'every-generated-tool-renderer-root';
  document.body.appendChild(rootNode);
  const cases = everyGeneratedToolRendererCase();
  const root = createRoot(rootNode);
  root.render(
    <TooltipProvider>
      <div className="space-y-3">
        {cases.map((tc) => (
          <section
            key={tc.id}
            data-testid={`renderer-all-case-${tc.id}`}
            data-tool-name={tc.toolName}
            data-sentinel={tc.sentinel}
            style={{ height: 300, border: '1px solid #ddd', margin: 8 }}
          >
            <RendererErrorBoundary id={tc.id}>
              <ArtifactPanel
                artifact={tc.artifact}
                settings={settings}
                onClose={() => undefined}
              />
            </RendererErrorBoundary>
          </section>
        ))}
      </div>
    </TooltipProvider>,
  );
  await new Promise((resolve) => window.setTimeout(resolve, 50));
  return cases.map((tc) => ({
    id: tc.id,
    toolName: tc.toolName,
    sentinel: tc.sentinel,
    nativeViewer: String(metadataFor(tc.toolName).presentation.native_viewer),
  }));
}

export async function mountProviderHostedRendererHarness(): Promise<Array<{ id: string; toolName: string; expectedHeader: string; sentinel: string }>> {
  const rootNode = document.createElement('div');
  rootNode.id = 'provider-hosted-renderer-root';
  document.body.appendChild(rootNode);
  const cases = providerHostedRendererCases();
  const root = createRoot(rootNode);
  root.render(
    <TooltipProvider>
      <div className="space-y-3">
        {cases.map((tc) => (
          <section
            key={tc.id}
            data-testid={`renderer-hosted-case-${tc.id}`}
            data-tool-name={tc.toolName}
            data-sentinel={tc.sentinel}
            style={{ height: 300, border: '1px solid #ddd', margin: 8 }}
          >
            <RendererErrorBoundary id={tc.id}>
              <ArtifactPanel
                artifact={tc.artifact}
                settings={settings}
                onClose={() => undefined}
              />
            </RendererErrorBoundary>
          </section>
        ))}
      </div>
    </TooltipProvider>,
  );
  await new Promise((resolve) => window.setTimeout(resolve, 50));
  return cases.map((tc) => ({
    id: tc.id,
    toolName: tc.toolName,
    expectedHeader: tc.expectedHeader,
    sentinel: tc.sentinel,
  }));
}

function expectedToolResultRouteText(tc: ToolResultRouteCase): string {
  if ('expectedHeader' in tc) return tc.artifact.title;
  const nativeViewer = String(metadataFor(tc.toolName).presentation.native_viewer);
  if (nativeViewer === 'connector_inspector') {
    if (tc.toolName.startsWith('confluence_')) return 'Confluence';
    if (tc.toolName.startsWith('jira_')) return 'Jira';
  }
  return tc.artifact.title;
}

export async function mountEveryGeneratedToolResultBlockHarness(): Promise<Array<{ id: string; toolName: string; expectedText: string; suppressed: boolean }>> {
  const rootNode = document.createElement('div');
  rootNode.id = 'every-generated-tool-result-block-root';
  document.body.appendChild(rootNode);
  const cases: ToolResultRouteCase[] = [
    ...everyGeneratedToolRendererCase(),
    ...providerHostedRendererCases(),
  ];
  const root = createRoot(rootNode);
  root.render(
    <TooltipProvider>
      <div className="space-y-3">
        {cases.map((tc) => (
          <section
            key={tc.id}
            data-testid={`tool-result-route-case-${tc.id}`}
            data-tool-name={tc.toolName}
            style={{ minHeight: 96, border: '1px solid #ddd', margin: 8 }}
          >
            <RendererErrorBoundary id={`tool-result-${tc.id}`}>
              <ToolResultBlock
                name={tc.toolName}
                content={toolResultEnvelopeForCase(tc)}
                artifactsById={{ [tc.artifact.id]: tc.artifact }}
                onOpenArtifact={() => undefined}
              />
            </RendererErrorBoundary>
          </section>
        ))}
      </div>
    </TooltipProvider>,
  );
  await new Promise((resolve) => window.setTimeout(resolve, 50));
  return cases.map((tc) => ({
    id: tc.id,
    toolName: tc.toolName,
    expectedText: expectedToolResultRouteText(tc),
    suppressed: tc.toolName === 'check_query_status',
  }));
}
