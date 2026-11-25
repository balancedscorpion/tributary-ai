import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

function generatedToolMetadata() {
  const source = sourceAt('lib/generatedToolMetadata.ts');
  const start = source.indexOf('export const GENERATED_STATIC_TOOL_METADATA = ');
  assert.notEqual(start, -1, 'generated metadata export must exist');
  const jsonStart = source.indexOf('[', start);
  const jsonEnd = source.indexOf('\n] as const;', jsonStart);
  assert.ok(jsonStart > start && jsonEnd > jsonStart, 'generated metadata must be emitted as a JSON array');
  return JSON.parse(source.slice(jsonStart, jsonEnd + 2));
}

function sorted(values) {
  return [...values].sort((a, b) => a.localeCompare(b));
}

test('generated tool metadata only uses explicitly supported renderer combinations', () => {
  const tools = generatedToolMetadata();
  const comboNames = new Map();
  for (const meta of tools) {
    const presentation = meta.presentation || {};
    const key = `${presentation.native_viewer}/${presentation.renderer_strategy}`;
    if (!comboNames.has(key)) comboNames.set(key, []);
    comboNames.get(key).push(meta.name);
  }

  assert.deepEqual(sorted(comboNames.keys()), [
    'chart/artifact_type',
    'code/artifact_type',
    'connector_inspector/family',
    'connector_inspector/tool',
    'data/artifact_type',
    'file/artifact_type',
    'image/artifact_type',
    'interactive_html/artifact_type',
    'query/artifact_type',
  ]);

  const requiredExamples = {
    'chart/artifact_type': ['build_chart_artifact'],
    'code/artifact_type': [
      'arxiv_get_paper_text',
      'arxiv_read_paper',
      'git_show_file_at_ref',
      'github_run_gh_command',
      'read_file',
      'rlm_exec',
    ],
    'connector_inspector/family': [
      'cua_browser_click',
      'github_clone_repository',
      'jira_upload_attachment',
      'sharepoint_search',
      'slack_upload_file',
    ],
    'connector_inspector/tool': [
      'get_bq_table_schema',
      'list_databricks_tables',
      'list_snowflake_databases',
      'list_trino_catalogs',
    ],
    'data/artifact_type': ['describe_dataset', 'display_table', 'get_dataset_rows'],
    'file/artifact_type': [
      'arxiv_download_paper',
      'export_thread',
      'local_open_file',
      'read_attachment',
      'sharepoint_read',
      'slack_download_file',
    ],
    'image/artifact_type': ['generate_image'],
    'interactive_html/artifact_type': ['create_interactive_artifact'],
    'query/artifact_type': [
      'get_query_results',
      'run_bigquery_query',
      'run_databricks_query',
      'run_snowflake_query',
      'run_trino_query',
    ],
  };

  for (const [combo, names] of Object.entries(requiredExamples)) {
    const generatedNames = new Set(comboNames.get(combo) || []);
    for (const name of names) {
      assert.ok(generatedNames.has(name), `${name} must remain covered by ${combo}`);
    }
  }
});

test('local_open_file metadata and promotion path produce native file artifacts', () => {
  const tools = generatedToolMetadata();
  const meta = tools.find((tool) => tool.name === 'local_open_file');
  assert.ok(meta, 'local_open_file must be present in generated tool metadata');
  assert.equal(meta.category, 'read');
  assert.equal(meta.origin, 'local');
  assert.equal(meta.tool_set_id, 'local-files');
  assert.equal(meta.presentation?.native_viewer, 'file');
  assert.equal(meta.presentation?.renderer_strategy, 'artifact_type');
  assert.equal(meta.presentation?.renderer_key, 'file');
  assert.deepEqual(meta.presentation?.artifact_types, ['file']);
  assert.equal(meta.presentation?.file_capability, 'local_file_read');
  assert.equal(meta.presentation?.produces_file_artifact, true);
  assert.equal(meta.presentation?.promotes_native_input, true);
  assert.ok(meta.presentation?.preview_modes?.includes('pdf_canvas'), 'PDF canvas preview mode must be advertised');
  assert.ok(meta.presentation?.source_refs?.includes('artifact_id'), 'artifact id source ref must be advertised');
  assert.ok(meta.presentation?.source_refs?.includes('path'), 'local path source ref must be advertised');

  const artifact = sourceAt('lib/toolResultArtifact.ts');
  assert.ok(
    artifact.includes('nativeFileArtifactFromPromotedResult(') &&
      artifact.includes('resultMetadata.file_artifact_id') &&
      artifact.includes('model_file_input: true') &&
      artifact.includes('native_file_input: true') &&
      artifact.includes('externalToolFile: true'),
    'tool result artifact builder must promote local_open_file-style native file results to file artifacts'
  );
});

test('all generated datasource catalog tools are registered through the catalog renderer path', () => {
  const tools = generatedToolMetadata();
  const catalogTools = tools
    .filter((meta) => meta.presentation?.renderer_strategy === 'tool')
    .map((meta) => meta.name);

  assert.deepEqual(sorted(catalogTools), [
    'describe_databricks_table',
    'describe_snowflake_table',
    'describe_trino_table',
    'get_bq_table_schema',
    'list_bq_datasets',
    'list_bq_tables',
    'list_databricks_catalogs',
    'list_databricks_schemas',
    'list_databricks_tables',
    'list_snowflake_databases',
    'list_snowflake_schemas',
    'list_snowflake_tables',
    'list_trino_catalogs',
    'list_trino_schemas',
    'list_trino_tables',
  ]);

  const queryRegistry = sourceAt('lib/queryProviderRegistry.ts');
  const catalogRenderer = sourceAt('components/renderers/tool/CatalogBrowserRenderer.tsx');
  assert.ok(
    queryRegistry.includes('const CATALOG_TOOL_NAMES = new Set<string>()') &&
      queryRegistry.includes('CATALOG_TOOL_NAMES.add(toolName)') &&
      queryRegistry.includes('export function getAllCatalogToolNames()'),
    'query provider registry must expose generated catalog tool names'
  );
  assert.ok(
    catalogRenderer.includes('const CATALOG_TOOLS = getAllCatalogToolNames()') &&
      catalogRenderer.includes('registerToolRenderer(name'),
    'CatalogBrowserRenderer must register every generated datasource catalog tool'
  );
});

test('tool presentation metadata drives generic inspector eligibility without stealing native viewers', () => {
  const inspector = sourceAt('components/renderers/tool/inspector/ToolInspector.tsx');

  assert.ok(
    inspector.includes('function resolveToolPresentation'),
    'ToolInspector must resolve top-level and result_metadata tool_presentation'
  );
  assert.ok(
    inspector.includes('const connectorPresentation =') &&
      inspector.includes('if (presentation && !connectorPresentation) return null;'),
    'generic inspector must claim connector/family presentation metadata without taking file/data/query/chart native viewers'
  );
  assert.ok(
    inspector.includes('connIdFromIntegration(resultMeta.integration)') &&
      inspector.includes('connIdFromIntegration(presentation?.family)'),
    'connector identity must be derivable from result_metadata and presentation family'
  );
});

test('tool result artifact builder preserves presentation metadata and treats declared empty data as data', () => {
  const artifact = sourceAt('lib/toolResultArtifact.ts');

  assert.ok(
    artifact.includes('buildToolTestOutputArtifact(result)') &&
      artifact.includes('output.artifact') &&
      artifact.includes('normalizeArtifactType(rawArtifact.type)'),
    'settings-page tool tests must preserve backend output.artifact as a native-renderable artifact'
  );
  assert.ok(
    artifact.includes('tool_presentation: result.presentation as any'),
    'tool-test presentation must be present before artifact shaping runs'
  );
  assert.ok(
    artifact.includes('normalizeToolPresentation('),
    'buildArtifactFromEnvelope must normalize top-level and nested tool_presentation'
  );
  assert.ok(
    artifact.includes('tool_presentation: toolPresentation'),
    'artifact metadata must preserve canonical tool_presentation for reload and inspectors'
  );
  assert.ok(
    artifact.includes('const toolArgs =') &&
      artifact.includes('tool_args: toolArgs'),
    'live tool-result artifacts must preserve tool_args before reload'
  );
  assert.ok(
    artifact.includes("presentationNativeViewer === 'data' || presentationResultKind === 'data'"),
    'declared data tools must render empty array results as data artifacts'
  );
  assert.ok(
    artifact.includes('isQueryTool || isDeclaredData'),
    'query tools and declared data tools should share empty-array dataset handling'
  );
  assert.ok(
    artifact.includes("presentationNativeViewer === 'code'") &&
      artifact.includes('codeArtifactFromNativeViewerResult('),
    'declared code-native tools must shape structured text results as code artifacts without exact-name special cases'
  );
  assert.ok(
    artifact.includes('nativeFileArtifactFromPromotedResult(') &&
      artifact.includes('native_file_input') &&
      artifact.includes('attachment_artifact_id') &&
      artifact.includes("type: 'file'"),
    'promoted native-file tool results must shape to file artifacts for settings tool tests'
  );
  assert.ok(
    artifact.includes('text_window') &&
      artifact.includes('paper_pages') &&
      artifact.includes('joinCommandOutput'),
    'code-native shaping must cover paper windows/pages and command stdout/stderr'
  );
});

test('renderer registry falls back to presentation native viewer before artifact type', () => {
  const registry = sourceAt('components/renderers/registry.ts');

  assert.ok(
    registry.includes('function resolveToolPresentation') &&
      registry.includes('metadata.result_metadata.tool_presentation'),
    'renderer registry must resolve top-level and nested tool_presentation metadata'
  );
  assert.ok(
    registry.includes('function presentationTypeKey') &&
      registry.includes("case 'query':") &&
      registry.includes("case 'chart':") &&
      registry.includes("return 'data'"),
    'presentation fallback must map query/chart native viewers to the data renderer'
  );
  assert.ok(
    registry.indexOf('presentationTypeKey(resolveToolPresentation(artifact))') <
      registry.indexOf('Type fallback'),
    'presentation dispatch must run before plain artifact.type fallback'
  );
});

test('query lifecycle tools route to the native query renderer', () => {
  const queryRegistry = sourceAt('lib/queryProviderRegistry.ts');
  const queryRenderer = sourceAt('components/renderers/tool/query/QueryToolRenderer.tsx');

  assert.ok(
    queryRegistry.includes("['check_query_status', 'get_query_results', 'cancel_query']"),
    'query lifecycle tools must be registered as query-native tools'
  );
  assert.ok(
    queryRenderer.includes('isQueryToolName(meta.tool_name)') &&
      queryRenderer.includes('qm?.query_provider') &&
      queryRenderer.includes('rm?.query_provider'),
    'QueryToolRenderer must claim lifecycle tools and resolve provider from query_provider metadata'
  );
});

test('chat tool result routing lets native-viewer artifacts beat connector widgets', () => {
  const block = sourceAt('components/chat/ToolResultBlock.tsx');

  assert.ok(
    block.includes('function shouldUseConnectorWidget') &&
      block.includes('hasNativeViewerArtifact(envelope, artifact)') &&
      block.includes("integration !== 'atlassian'"),
    'ToolResultBlock must restrict branded connector widgets and let native-viewer artifacts render normally'
  );
  assert.ok(
    block.indexOf('const artifact = artifactsById[envelope.artifact_id]') <
      block.indexOf('shouldUseConnectorWidget(envelope, toolName, artifact)'),
    'ToolResultBlock must look up artifacts before deciding whether a connector widget should claim the result'
  );
  assert.ok(
    !block.includes("envelope.result_metadata?.integration === 'github'") &&
      !block.includes("envelope.result_metadata?.integration === 'slack'"),
    'Slack and GitHub envelopes must not be routed into the Atlassian widget fallback'
  );
});

test('search_files renderer accepts both array and path-map backend result shapes', () => {
  const renderer = sourceAt('components/renderers/tool/SearchFilesRenderer.tsx');

  assert.ok(
    renderer.includes('MatchLine[] | Record<string, string[]>'),
    'search_files renderer must accept legacy path -> lines search results'
  );
  assert.ok(
    renderer.includes('Object.entries(raw.matches)') &&
      renderer.includes('parseSearchLine'),
    'path-map search results must be grouped through a line parser'
  );
  assert.ok(
    renderer.includes('m.content ?? m.snippet'),
    'array-shaped search results must accept either content or snippet fields'
  );
});

test('attachment search and inspect presentation do not claim the native file viewer', () => {
  const generated = sourceAt('lib/generatedToolMetadata.ts');

  const inspectStart = generated.indexOf('"name": "inspect_attachment"');
  const readStart = generated.indexOf('"name": "read_attachment"');
  const searchStart = generated.indexOf('"name": "search_attachment"');
  assert.ok(inspectStart > -1 && readStart > inspectStart, 'inspect_attachment metadata must precede read_attachment metadata');
  assert.ok(searchStart > readStart, 'search_attachment metadata must be generated');

  const inspectBlock = generated.slice(inspectStart, readStart);
  const searchBlock = generated.slice(searchStart, generated.indexOf('"test":', searchStart));

  assert.ok(inspectBlock.includes('"native_viewer": "connector_inspector"'), 'inspect_attachment must render through the inspector');
  assert.ok(inspectBlock.includes('"file_capability": "attachment_inspect"'), 'inspect_attachment must not reuse attachment_read capability');
  assert.ok(searchBlock.includes('"result_kind": "search"'), 'search_attachment must declare search result kind');
  assert.ok(searchBlock.includes('"native_viewer": "connector_inspector"'), 'search_attachment must render through the inspector');
  assert.ok(searchBlock.includes('"file_capability": "attachment_search"'), 'search_attachment must not claim the native file viewer');
});

test('frontend fallback attachment presentation matches backend source refs', () => {
  const inventory = sourceAt('lib/toolInventory.ts');
  const attachmentCase = inventory.slice(
    inventory.indexOf("case 'attachments':"),
    inventory.indexOf("case 'artifacts':"),
  );
  assert.ok(
    attachmentCase.includes("fileCapability = 'attachment_inspect'") &&
      attachmentCase.includes("name === 'read_attachment'") &&
      attachmentCase.includes("fileCapability = 'attachment_read'") &&
      attachmentCase.includes("name === 'search_attachment'") &&
      attachmentCase.includes("fileCapability = 'attachment_search'"),
    'fallback attachment tools must distinguish inspect/read/search capabilities'
  );
  assert.ok(
    inventory.includes("'attachment_inspect'") &&
      inventory.includes("'attachment_search'") &&
      inventory.includes("fileCapability === 'attachment_inspect'") &&
      inventory.includes("fileCapability === 'attachment_search'"),
    'fallback attachment inspect/search capabilities must expose artifact_id source refs'
  );
});

test('artifact reload normalization preserves migrated tool and renderer metadata', () => {
  const hook = sourceAt('hooks/useChat.ts');
  const normalizeStart = hook.indexOf('function normalizeArtifactForClient');
  assert.notEqual(normalizeStart, -1, 'normalizeArtifactForClient must exist');
  const normalizeSource = hook.slice(normalizeStart, normalizeStart + 1400);

  for (const key of [
    'tool_args',
    'tool_presentation',
    'result_metadata',
    'query_metadata',
    'plot_spec',
    'chart_budget',
    'chart_engine',
    'preview_only',
    'full_artifact_id',
    'row_count_total',
    'raw_result',
  ]) {
    assert.ok(
      normalizeSource.includes(`"${key}"`),
      `normalizeArtifactForClient must preserve top-level ${key}`
    );
  }
});

test('data viewer treats an empty array as valid zero-row data', () => {
  const dataViz = sourceAt('components/DataVisualization.tsx');

  assert.ok(
    dataViz.includes('if (!Array.isArray(data)) return null;') &&
      dataViz.includes('if (data.length === 0) return [];'),
    'DataVisualization must distinguish invalid JSON from valid empty datasets'
  );
  assert.ok(
    dataViz.includes('if (!rawData && !isPartialDataset)'),
    'the invalid-data branch should only run for null parse failures, not []'
  );
});

test('tool inventory falls back for unavailable runtime inventory without hiding permissions', () => {
  const service = sourceAt('services/toolInventoryApi.ts');
  const inventory = sourceAt('lib/toolInventory.ts');
  const generated = sourceAt('lib/generatedToolMetadata.ts');
  const fixture = sourceAt('components/dev/ApprovalsFixture.tsx');
  const permissions = sourceAt('types/permissions.ts');

  assert.ok(
    service.includes('if (!res.ok)') &&
      service.includes('return STATIC_TOOL_INVENTORY'),
    'inventory service must use generated static fallback for any unavailable runtime inventory response'
  );
  assert.ok(
    service.includes('tryNormalizeRuntimeToolInventory') &&
      service.includes('data?.provider_hosted_tools') &&
      service.includes("throw new Error('Tool inventory response was malformed.')") &&
      service.includes("throw new Error('Provider-hosted tool inventory response was malformed.')") &&
      service.includes("throw new Error('Tool inventory response did not contain any valid tools.')"),
    'malformed successful runtime inventory responses must fail visibly instead of falling back'
  );
  assert.ok(
    generated.includes('export const GENERATED_STATIC_PROVIDER_HOSTED_TOOL_CAPABILITIES') &&
      generated.includes('"origin": "provider"') &&
      generated.includes('"permission_subject_kind": "provider_hosted_tool"') &&
      inventory.includes('GENERATED_STATIC_PROVIDER_HOSTED_TOOL_CAPABILITIES') &&
      inventory.includes('rawCapabilities === undefined') &&
      inventory.includes('normalizeProviderHostedCapabilities(') &&
      inventory.includes("origin: cap.origin || 'provider'") &&
      inventory.includes("permissionSubjectKind: cap.permission_subject_kind || 'provider_hosted_tool'"),
    'runtime inventory normalization must preserve generated provider-hosted capabilities as provider-scoped permission rows'
  );
  assert.ok(
    !inventory.includes('tools.length === 0 && rawCapabilities !== undefined'),
    'present-but-empty provider-hosted runtime capabilities must not be replaced with generated data'
  );
  assert.ok(
    !inventory.includes('const STATIC_PROVIDER_HOSTED_TOOL_CAPABILITIES: ProviderHostedToolCapability[] = ['),
    'frontend inventory must not carry a hand-maintained provider-hosted capability catalog'
  );
  assert.ok(
    fixture.includes('staticProviderHostedToolCapabilitiesForFixture') &&
      fixture.includes('provider_hosted_tools: fixtureProviderHostedTools'),
    'approvals fixture must serve provider-hosted capabilities through the same API channel as production'
  );
  assert.ok(
    permissions.includes("| 'provider_hosted_tool'"),
    'provider-hosted permission subjects must be first-class in the shared subject-kind type'
  );
});

test('Bedrock does not expose unsupported provider-hosted tool settings', () => {
  const providerDefs = sourceAt('providers/catalog/providerDefs.ts');
  const constants = sourceAt('constants.ts');
  const migration = sourceAt('services/settingsMigration.ts');
  const providersTab = sourceAt('components/settings/ProvidersTab.tsx');

  const bedrockDefStart = providerDefs.indexOf("bedrock: {");
  const bedrockDefEnd = providerDefs.indexOf("\n  openai:", bedrockDefStart);
  assert.ok(bedrockDefStart >= 0 && bedrockDefEnd > bedrockDefStart, 'Bedrock provider definition block must exist');
  const bedrockDef = providerDefs.slice(bedrockDefStart, bedrockDefEnd);
  assert.ok(
    !bedrockDef.includes('webSearchEnabled') && !bedrockDef.includes('codeExecutionEnabled'),
    'Bedrock catalog must not render hosted web search or code sandbox controls'
  );
  assert.ok(
    bedrockDef.includes("placeholder: 'anthropic.claude-opus-4-6-v1'") &&
      bedrockDef.includes("def: 'anthropic.claude-opus-4-6-v1'"),
    'new Bedrock connections must use a Bedrock Runtime model ID rather than a direct Anthropic model name'
  );

  const bedrockDefaultStart = constants.indexOf('  bedrock: {');
  const bedrockDefaultEnd = constants.indexOf('\n  systemPrompt:', bedrockDefaultStart);
  assert.ok(bedrockDefaultStart >= 0 && bedrockDefaultEnd > bedrockDefaultStart, 'Bedrock default settings block must exist');
  const bedrockDefaults = constants.slice(bedrockDefaultStart, bedrockDefaultEnd);
  assert.ok(
    bedrockDefaults.includes('webSearch: null') &&
      bedrockDefaults.includes('codeExecution: null'),
    'Bedrock defaults must keep unsupported hosted-tool settings null'
  );

  assert.ok(
    migration.includes("if (providerId === 'anthropic')") &&
      migration.includes('merged.bedrock.webSearch = null') &&
      migration.includes('merged.bedrock.codeExecution = null'),
    'settings migration must clear stale Bedrock hosted-tool settings'
  );
  assert.ok(
    migration.includes("if (model === 'claude-opus-4-6') return 'anthropic.claude-opus-4-6-v1'"),
    'settings migration must repair the former invalid Bedrock model default'
  );
  assert.ok(
    providersTab.includes("selProv === 'anthropic' ||") &&
      providersTab.includes("(selProv === 'compatible' && apiType === 'responses')") &&
      !providersTab.includes("selProv === 'openai' || selProv === 'anthropic' || selProv === 'bedrock'"),
    'provider save path must not persist hosted-tool settings for Bedrock'
  );
});

test('permissions tool tests block unstructured required input instead of submitting empty args', () => {
  const tab = sourceAt('components/settings/PermissionsTab.tsx');

  assert.ok(
    tab.includes('unstructuredInputRequired') &&
      tab.includes('test.input_required === true') &&
      tab.includes('argumentRequirements.length === 0') &&
      tab.includes('argumentGroupRequirements.length === 0') &&
      tab.includes('contextInputs.length === 0'),
    'tool-test readiness must detect runtime tools that require input but expose no structured fields'
  );
  assert.ok(
    tab.includes('!missingUnstructuredTestInput') &&
      tab.includes("'test input'"),
    'the Test button gate must stay disabled until unstructured required input is made explicit'
  );
});

test('PDF attachment previews fetch raw bytes before rendering in an iframe', () => {
  const source = sourceAt('components/chat/FileAttachmentCard.tsx');
  const start = source.indexOf('const PdfPreviewBody');
  const end = source.indexOf('const HtmlPreviewBody', start);
  assert.ok(start >= 0 && end > start, 'PdfPreviewBody block must exist');
  const body = source.slice(start, end);

  assert.ok(
    body.includes('fetch(url, {') &&
      body.includes("credentials: 'same-origin'") &&
      body.includes('URL.createObjectURL'),
    'PDF previews must use authenticated fetch and render a blob URL'
  );
  assert.ok(
    !body.includes('`${url}#toolbar=0'),
    'PDF previews must not point iframes directly at raw API URLs because iframe requests cannot add the desktop token header'
  );
});

test('Files surfaces use file-history APIs and open through the native artifact viewer path', () => {
  const viewSource = sourceAt('components/files/FilesAuditView.tsx');
  assert.ok(
    viewSource.includes('listFileAuditRecords') &&
      viewSource.includes('loadFileAuditArtifact') &&
      viewSource.includes('downloadArtifactRawBytes') &&
      viewSource.includes('deleteFileAuditArtifact'),
    'FilesAuditView must use the file-history API wrapper for listing, opening, downloading, and removal'
  );
  assert.ok(
    viewSource.includes('onOpenArtifact(artifact, fileProvenanceFromRecord(record))') &&
      viewSource.includes('onOpenOriginThread') &&
      !viewSource.includes('resolveRenderer('),
    'FilesAuditView must hydrate artifacts with provenance and delegate viewing instead of duplicating renderer routing'
  );

  const settingsSource = sourceAt('components/settings/SettingsDialog.tsx');
  const tabSource = sourceAt('components/settings/FilesTab.tsx');
  assert.ok(
    settingsSource.includes('TabsTrigger value="files"') &&
      settingsSource.includes('<FilesTab'),
    'SettingsDialog must expose Files as a first-class tab'
  );
  assert.ok(
    tabSource.includes('FilesAuditView') &&
      tabSource.includes('Open full view') &&
      !tabSource.includes('listFileAuditRecords'),
    'Settings Files tab must be a wrapper around the shared FilesAuditView with an affordance for the full view'
  );

  const headerSource = sourceAt('components/layout/Header.tsx');
  assert.ok(
    headerSource.includes('aria-label="View files"') &&
      headerSource.includes('<Files size={20}'),
    'Header must provide a persistent Files entry point for the active chat'
  );

  const indexSource = sourceAt('index.tsx');
  const appSource = sourceAt('App.tsx');
  assert.ok(
    !indexSource.includes('FilesPage'),
    'The /files surface must stay inside App so it can reuse chat state and the existing inspector overlay'
  );
  assert.ok(
    appSource.includes("type AppView = 'chat' | 'files'") &&
      appSource.includes("window.location.pathname === '/files'") &&
      appSource.includes('<FilesAuditView') &&
      appSource.includes('<InspectorOverlay') &&
      appSource.includes('activeArtifactProvenance') &&
      appSource.includes('handleOpenOriginThread') &&
      !appSource.includes('FilesPage') &&
      !appSource.includes('resolveRenderer('),
    'App must expose a persistent in-app /files view that opens artifacts through the existing native viewer overlay with provenance'
  );
});
