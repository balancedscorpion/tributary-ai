// Static fallback inventory of every chat tool the backend can register.
// Runtime metadata from GET /api/tools/inventory is canonical; this file keeps
// older/dev surfaces usable when that endpoint is unavailable.

import type { ConnectionCatalogId } from '@/types';
import {
  isThreadScope,
  type PermissionCategory,
  type PermissionEffect,
  type PermissionRuleUI,
} from '@/types/permissions';
import { CONNECTION_META, getConnectionForToolName } from '@/lib/connectionsCatalog';
import {
  GENERATED_STATIC_PROVIDER_HOSTED_TOOL_CAPABILITIES,
  GENERATED_STATIC_TOOL_METADATA,
} from '@/lib/generatedToolMetadata';
import { supportsConnectionWidePermissionRule } from '@/lib/permissionRuleSubjects';

export interface ConnectionToolInfo {
  /** Internal tool name as registered by the backend. */
  name: string;
  /** Human verb phrase — users never need the internal name to decide. */
  label: string;
  /** Backend-provided detail text, when runtime inventory is available. */
  description?: string;
  category: PermissionCategory;
  origin?: ToolInventoryOrigin;
  toolSetId?: string;
  toolSetName?: string;
  connectorId?: string;
  permissionSubjectKind?: string;
  permissionOperation?: string;
  presentation?: ToolPresentationSpec;
  test?: ToolInventoryTestSpec;
}

const read = (name: string, label: string): ConnectionToolInfo => ({ name, label, category: 'read' });
const write = (name: string, label: string): ConnectionToolInfo => ({ name, label, category: 'write' });
const execute = (name: string, label: string): ConnectionToolInfo => ({ name, label, category: 'execute' });

export const CONNECTION_TOOLS: Record<ConnectionCatalogId, ConnectionToolInfo[]> = {
  'thread-search': [
    read('search_threads', 'Search past conversations'),
    read('search_docs', 'Search thread documents'),
    read('get_doc', 'Read a thread document'),
    read('get_artifact', 'Read a generated artifact'),
    read('list_messages', 'List messages in a thread'),
    execute('rlm_exec', 'Run a retrieval script over threads'),
  ],
  'image-generation': [
    execute('generate_image', 'Generate an image'),
  ],
  'web-search': [],
  'python-sandbox': [],
  'local-computer': [
  ],
  bigquery: [
    read('list_bq_datasets', 'List datasets'),
    read('list_bq_tables', 'List tables'),
    read('get_bq_table_schema', 'Read a table schema'),
    read('run_bigquery_query', 'Run a SQL query'),
  ],
  databricks: [
    read('list_databricks_catalogs', 'List catalogs'),
    read('list_databricks_schemas', 'List schemas'),
    read('list_databricks_tables', 'List tables'),
    read('describe_databricks_table', 'Read a table schema'),
    read('run_databricks_query', 'Run a SQL query'),
  ],
  trino: [
    read('list_trino_catalogs', 'List catalogs'),
    read('list_trino_schemas', 'List schemas'),
    read('list_trino_tables', 'List tables'),
    read('describe_trino_table', 'Read a table schema'),
    read('run_trino_query', 'Run a SQL query'),
  ],
  snowflake: [
    read('list_snowflake_databases', 'List databases'),
    read('list_snowflake_schemas', 'List schemas'),
    read('list_snowflake_tables', 'List tables'),
    read('describe_snowflake_table', 'Read a table schema'),
    read('run_snowflake_query', 'Run a SQL query'),
  ],
  github: [
    read('github_status', 'Check connection status'),
    read('github_check_connection', 'Verify the GitHub sign-in'),
    read('github_list_repositories', 'List repositories'),
    read('github_list_pull_requests', 'List pull requests'),
    read('github_get_pull_request', 'Read a pull request'),
    read('github_list_issues', 'List issues'),
    read('github_get_issue', 'Read an issue'),
    read('github_list_workflow_runs', 'List workflow runs'),
    read('github_get_workflow_run_jobs', 'Read workflow run jobs'),
    execute('github_clone_repository', 'Clone a repository'),
    execute('github_pull_repository', 'Pull repository changes'),
    execute('github_refresh_repository', 'Refresh a cloned repository'),
    execute('github_run_gh_command', 'Run a gh command'),
  ],
  slack: [
    read('slack_status', 'Check connection status'),
    read('slack_check_connection', 'Verify the Slack sign-in'),
    read('slack_list_conversations', 'List channels'),
    read('slack_get_conversation', 'Read channel details'),
    read('slack_get_conversation_members', 'List channel members'),
    read('slack_get_messages', 'Read channel messages'),
    read('slack_get_thread_replies', 'Read thread replies'),
    read('slack_get_user', 'Look up a user'),
    read('slack_search_messages', 'Search messages'),
    read('slack_download_file', 'Download a shared file'),
    write('slack_post_message', 'Send a message'),
    write('slack_upload_file', 'Upload a file'),
  ],
  atlassian: [
    read('atlassian_status', 'Check connection status'),
    read('atlassian_check_connection', 'Verify the Atlassian sign-in'),
    read('jira_search_issues', 'Search Jira issues'),
    read('jira_find', 'Find Jira issues'),
    read('jira_get_issue', 'Read a Jira issue'),
    read('jira_get_issue_history', 'Read issue history'),
    read('jira_get_issue_comments', 'Read issue comments'),
    read('jira_get_issue_attachments', 'List issue attachments'),
    read('jira_read_attachment', 'Read an issue attachment'),
    read('jira_get_issue_link_types', 'List issue link types'),
    read('jira_find_users', 'Find Jira users'),
    read('jira_rest_get', 'Read from the Jira API'),
    write('jira_create_issue', 'Create a Jira issue'),
    write('jira_update_issue', 'Update a Jira issue'),
    write('jira_add_comment', 'Post a Jira comment'),
    write('jira_reply_to_comment', 'Reply to a Jira comment'),
    write('jira_add_related_tickets', 'Link Jira issues'),
    write('jira_upload_attachment', 'Upload a file to Jira'),
    read('confluence_search', 'Search Confluence'),
    read('confluence_open_page', 'Read a Confluence page'),
    read('confluence_open_space', 'Browse a Confluence space'),
    read('confluence_rest_get', 'Read from the Confluence API'),
  ],
  sharepoint: [
    read('sharepoint_status', 'Check connection status'),
    read('sharepoint_check_connection', 'Verify the SharePoint sign-in'),
    read('sharepoint_search', 'Search files'),
    read('sharepoint_get_metadata', 'Read file details'),
    read('sharepoint_read', 'Read a document'),
    read('sharepoint_summarize', 'Summarize a document'),
    read('sharepoint_download', 'Download a document'),
  ],
  browser: [
    read('cua_browser_status', 'Check browser status'),
    read('cua_browser_snapshot', 'Read the current page'),
    read('cua_browser_logs', 'Read console logs'),
    execute('cua_browser_navigate', 'Open a web page'),
    execute('cua_browser_click', 'Click on the page'),
    execute('cua_browser_type', 'Type into the page'),
    execute('cua_browser_press', 'Press a key'),
    execute('cua_browser_back', 'Go back a page'),
    execute('cua_browser_wait', 'Wait for the page'),
    execute('cua_browser_clear_logs', 'Clear console logs'),
  ],
  arxiv: [
    read('arxiv_status', 'Check connection status'),
    read('arxiv_check_connection', 'Verify the arXiv connection'),
    read('arxiv_search', 'Search papers'),
    read('arxiv_screening_report', 'Build a screening report'),
    read('arxiv_recommendations', 'Get paper recommendations'),
    read('arxiv_list_papers', 'List downloaded papers'),
    read('arxiv_get_paper', 'Read paper details'),
    read('arxiv_get_paper_text', 'Read paper text'),
    read('arxiv_read_paper', 'Read paper pages'),
    read('arxiv_download_paper', 'Download a paper'),
  ],
};

export interface AppToolGroup {
  id: string;
  title: string;
  /** One-line "why this exists" shown under the group title. */
  description: string;
  /**
   * Where the group's tools operate: 'local' touches files on this computer
   * (attachments, clones, uploaded CSVs); 'builtin' stays inside the app;
   * 'connected' is a runtime tool set whose connector is not in the static
   * catalogue yet.
   * Drives the origin badge on the Permissions page.
   */
  origin: 'local' | 'builtin' | 'connected' | 'provider';
  tools: ConnectionToolInfo[];
}

export type ToolInventoryOrigin = 'builtin' | 'connected' | 'local' | 'attached' | 'provider';

export interface ToolInventoryTestSpec {
  supported: boolean;
  mode: string;
  requires_approval: boolean;
  input_required: boolean;
  required_arguments?: string[];
  required_argument_groups?: string[][];
  required_context?: string[];
  sample_arguments?: Record<string, unknown>;
  input_hint?: string;
  summary?: string;
}

export interface ToolPresentationSpec {
  family: string;
  result_kind: string;
  renderer_strategy: string;
  renderer_key: string;
  native_viewer: string;
  artifact_types?: string[];
  file_capability?: string;
  file_kinds?: string[];
  preview_modes?: string[];
  source_refs?: string[];
  produces_file_artifact?: boolean;
  promotes_native_input?: boolean;
  native_links?: string[];
}

export interface RuntimeToolMetadata {
  name: string;
  label: string;
  description: string;
  category: PermissionCategory;
  origin: ToolInventoryOrigin;
  tool_set_id: string;
  tool_set_name: string;
  connector_id?: string;
  presentation: ToolPresentationSpec;
  test: ToolInventoryTestSpec;
}

export interface ProviderHostedToolCapability {
  id: string;
  name: string;
  label: string;
  description: string;
  category: PermissionCategory;
  origin?: 'provider';
  tool_set_id?: string;
  tool_set_name?: string;
  adapter_family: string;
  settings_key: string;
  permission_subject_kind?: string;
  provider_tool_names?: string[];
  provider_executed: boolean;
  presentation?: ToolPresentationSpec;
  test?: ToolInventoryTestSpec;
}

export interface PermissionToolInventory {
  connectionTools: Record<ConnectionCatalogId, ConnectionToolInfo[]>;
  appToolGroups: AppToolGroup[];
  source: 'static' | 'runtime';
}

/**
 * Tools the backend registers unconditionally (registry.go "File tools",
 * "CSV analysis tools", "Query lifecycle tools" and "UI/data tools") — they
 * exist whether or not any connection is configured. Git tools operate on
 * local clones and are registered outside the GitHub gate, so they live here.
 */
export const APP_TOOL_GROUPS: AppToolGroup[] = [
  {
    id: 'local-files',
    origin: 'local',
    title: 'Local files & repositories',
    description: 'Imported snapshot copies, Local Computer references, and app-managed local repository clones.',
    tools: [
      read('list_files', 'List imported snapshot files'),
      read('read_file', 'Read an imported snapshot file'),
      read('search_files', 'Search imported snapshot files'),
      read('file_search', 'Search files on this device'),
      read('local_list_folder', 'List a folder on this device'),
      read('local_read_file', 'Read a file on this device'),
      read('local_open_file', 'Open a file on this device'),
      read('local_read_pdf_page', 'Read one PDF page on this device'),
      read('local_list_grant_folder', 'List a local file grant'),
      execute('local_search_command', 'Run a local search command'),
      read('local_search_in_grant', 'Search a local file grant'),
      read('local_read_grant_file', 'Read from a local file grant'),
      read('list_git_repositories', 'List cloned repositories'),
      read('git_log', 'Read commit history in a clone'),
      read('git_show_commit', 'Read a commit'),
      read('git_show_file_at_ref', 'Read a file at a branch or tag'),
      read('git_grep', 'Search files in a clone'),
      read('git_search_history', 'Search commit history'),
      read('git_list_refs', 'List branches and tags'),
    ],
  },
  {
    id: 'attachments',
    origin: 'local',
    title: 'Attachments',
    description: 'Files you upload into a conversation.',
    tools: [
      read('inspect_attachment', 'Inspect an attached file'),
      read('read_attachment', 'Read an attached file'),
      read('search_attachment', 'Search an attached file'),
    ],
  },
  {
    id: 'artifacts',
    origin: 'builtin',
    title: 'Artifacts & charts',
    description: 'Creating and reading in-app outputs — nothing leaves this app.',
    tools: [
      read('build_chart_artifact', 'Create a chart'),
      read('create_interactive_artifact', 'Create an interactive artifact'),
      read('create_interactive_data_artifact', 'Create an interactive data artifact'),
      read('display_table', 'Display a table'),
      read('get_echarts_capabilities', 'Read chart capabilities (ECharts)'),
      read('get_highcharts_capabilities', 'Read chart capabilities (Highcharts)'),
      read('get_artifact_chunk', 'Read part of a large artifact'),
      read('search_artifact_content', 'Search artifact content'),
      read('get_json_artifact', 'Read a JSON artifact'),
      read('export_thread', 'Export this conversation'),
    ],
  },
  {
    id: 'data-analysis',
    origin: 'local',
    title: 'Data analysis',
    description: 'Uploaded CSVs and dataset artifacts, analyzed locally.',
    tools: [
      read('describe_dataset', 'Describe an uploaded dataset'),
      read('sample_rows', 'Sample dataset rows'),
      read('filter_csv', 'Filter a CSV'),
      read('aggregate_csv', 'Aggregate a CSV'),
      read('inspect_dataset_artifact', 'Inspect a dataset artifact'),
      read('get_dataset_rows', 'Read dataset rows'),
      read('aggregate_dataset', 'Aggregate a dataset'),
      read('search_dataset_rows', 'Search dataset rows'),
    ],
  },
  {
    id: 'query-lifecycle',
    origin: 'builtin',
    title: 'Query lifecycle',
    description: 'Managing queries already started on a connected warehouse.',
    tools: [
      read('check_query_status', 'Check a running query'),
      read('get_query_results', 'Fetch query results'),
      read('validate_query_result', 'Validate query results'),
      execute('cancel_query', 'Cancel a running query'),
    ],
  },
];

const PROVIDER_HOSTED_TOOL_SET_ID = 'provider-hosted';
const PROVIDER_HOSTED_TOOL_SET_NAME = 'Model provider capabilities';
const LOCAL_COMPUTER_TOOL_SET_ID = 'local-computer';
const LOCAL_COMPUTER_TOOL_SET_NAME = 'Local Computer';
const LOCAL_COMPUTER_TOOL_ORDER = [
  'file_search',
  'local_list_folder',
  'local_read_file',
  'local_open_file',
  'local_read_pdf_page',
  'local_list_grant_folder',
  'local_search_command',
  'local_search_in_grant',
  'local_read_grant_file',
];
const LOCAL_COMPUTER_TOOL_NAMES = new Set(LOCAL_COMPUTER_TOOL_ORDER);

const STATIC_APP_GROUP_BY_ID = new Map(APP_TOOL_GROUPS.map((group) => [group.id, group]));
const STATIC_CONNECTION_TOOL_SET_NAMES: Partial<Record<ConnectionCatalogId, string>> = {
  'thread-search': 'Search past conversations',
  'local-computer': LOCAL_COMPUTER_TOOL_SET_NAME,
  browser: 'In-app browser',
};
const STATIC_CONNECTION_ORDER = new Map<ConnectionCatalogId, Map<string, number>>();
for (const id of Object.keys(CONNECTION_TOOLS) as ConnectionCatalogId[]) {
  STATIC_CONNECTION_ORDER.set(
    id,
    new Map(CONNECTION_TOOLS[id].map((tool, index) => [tool.name, index])),
  );
}
STATIC_CONNECTION_ORDER.set(
  'local-computer',
  new Map(LOCAL_COMPUTER_TOOL_ORDER.map((toolName, index) => [toolName, index])),
);
const STATIC_APP_TOOL_ORDER = new Map<string, Map<string, number>>();
for (const group of APP_TOOL_GROUPS) {
  STATIC_APP_TOOL_ORDER.set(
    group.id,
    new Map(group.tools.map((tool, index) => [tool.name, index])),
  );
}

const STATIC_CONNECTION_CHECK_TOOLS = new Set([
  'github_status',
  'github_check_connection',
  'slack_status',
  'slack_check_connection',
  'atlassian_status',
  'atlassian_check_connection',
  'sharepoint_status',
  'sharepoint_check_connection',
  'cua_browser_status',
  'arxiv_status',
  'arxiv_check_connection',
]);

const STATIC_UNSUPPORTED_TEST_TOOLS = new Set<string>([]);

const STATIC_GITHUB_PROJECT_CONTEXT_TOOLS = new Set([
  'github_list_pull_requests',
  'github_get_pull_request',
  'github_list_issues',
  'github_get_issue',
  'github_list_workflow_runs',
  'github_get_workflow_run_jobs',
]);

const STATIC_DATA_ANALYSIS_PROJECT_CONTEXT_TOOLS = new Set([
  'describe_dataset',
  'sample_rows',
  'filter_csv',
  'aggregate_csv',
]);

const STATIC_ARTIFACT_CONTEXT_TOOLS = new Set([
  'get_artifact_chunk',
  'search_artifact_content',
  'get_json_artifact',
  'build_chart_artifact',
  'create_interactive_data_artifact',
]);

const STATIC_REQUIRED_TEST_ARGUMENTS: Partial<Record<string, string[]>> = {
  'read_file': ['path'],
  'search_files': ['query'],
  'file_search': ['query'],
  'local_read_file': ['path'],
  'local_open_file': ['path'],
  'local_read_pdf_page': ['path', 'page_number'],
  'local_list_grant_folder': ['grant_id'],
  'local_search_command': ['grant_id', 'program', 'query'],
  'local_search_in_grant': ['grant_id', 'query'],
  'local_read_grant_file': ['grant_id', 'file_ref'],
  'git_search_history': ['query'],
  'git_grep': ['query'],
  'git_show_file_at_ref': ['path'],
  'inspect_attachment': ['artifact_id'],
  'read_attachment': ['artifact_id'],
  'search_attachment': ['artifact_id', 'query'],
  'get_artifact_chunk': ['artifact_id'],
  'search_artifact_content': ['artifact_id', 'query'],
  'get_json_artifact': ['artifact_id'],
  'generate_image': ['prompt'],
  'build_chart_artifact': ['source', 'chart'],
  'create_interactive_artifact': ['title', 'html'],
  'create_interactive_data_artifact': ['title', 'source', 'html'],
  'describe_dataset': ['path'],
  'sample_rows': ['path'],
  'filter_csv': ['path', 'column', 'operator'],
  'aggregate_csv': ['path', 'metrics'],
  'inspect_dataset_artifact': ['artifact_id'],
  'get_dataset_rows': ['artifact_id'],
  'aggregate_dataset': ['artifact_id'],
  'search_dataset_rows': ['artifact_id', 'query'],
  'search_threads': ['query'],
  'search_docs': ['q'],
  'get_doc': ['doc_id'],
  'get_artifact': ['artifact_id'],
  'list_messages': ['thread_id'],
  'rlm_exec': ['code'],
  'run_bigquery_query': ['sql'],
  'list_bq_tables': ['dataset_id'],
  'get_bq_table_schema': ['dataset_id', 'table_id'],
  'run_databricks_query': ['sql'],
  'list_databricks_schemas': ['catalog_name'],
  'list_databricks_tables': ['catalog_name', 'schema_name'],
  'describe_databricks_table': ['full_name'],
  'run_trino_query': ['sql'],
  'list_trino_schemas': ['catalog_name'],
  'list_trino_tables': ['catalog_name', 'schema_name'],
  'describe_trino_table': ['full_name'],
  'run_snowflake_query': ['sql'],
  'list_snowflake_schemas': ['database_name'],
  'list_snowflake_tables': ['database_name', 'schema_name'],
  'describe_snowflake_table': ['full_name'],
  'cua_browser_navigate': ['url'],
  'cua_browser_type': ['value'],
  'cua_browser_press': ['key'],
  'github_get_pull_request': ['number'],
  'github_get_issue': ['number'],
  'github_get_workflow_run_jobs': ['run_id'],
  'github_clone_repository': ['repository'],
  'github_run_gh_command': ['args'],
  'slack_get_conversation': ['channel_id'],
  'slack_get_conversation_members': ['channel_id'],
  'slack_get_messages': ['channel_id'],
  'slack_get_thread_replies': ['channel_id', 'thread_ts'],
  'slack_get_user': ['user_id'],
  'slack_search_messages': ['query'],
  'slack_download_file': ['file_id'],
  'slack_post_message': ['channel_id', 'text'],
  'slack_upload_file': ['channel_id'],
  'jira_get_issue': ['issueKey'],
  'jira_get_issue_history': ['issueKey'],
  'jira_get_issue_comments': ['issueKey'],
  'jira_get_issue_attachments': ['issueKey'],
  'jira_read_attachment': ['issueKey', 'attachmentId'],
  'jira_find_users': ['query'],
  'jira_search_issues': ['jql'],
  'jira_rest_get': ['path'],
  'jira_add_comment': ['issueKey', 'comment'],
  'jira_reply_to_comment': ['issueKey', 'commentId', 'comment'],
  'jira_add_related_tickets': ['issueKey', 'relatedIssueKeys'],
  'jira_upload_attachment': ['issueKey'],
  'jira_update_issue': ['issueKey'],
  'jira_create_issue': ['projectKey', 'summary'],
  'confluence_open_space': ['spaceKey'],
  'confluence_search': ['query'],
  'confluence_rest_get': ['path'],
  'sharepoint_search': ['query'],
  'arxiv_download_paper': ['arxiv_id'],
  'arxiv_get_paper': ['paper'],
  'arxiv_get_paper_text': ['paper'],
  'arxiv_read_paper': ['paper'],
};

const STATIC_REQUIRED_TEST_ARGUMENT_GROUPS: Partial<Record<string, string[][]>> = {
  'jira_update_issue': [
    ['summary'],
    ['description'],
    ['clearDescription'],
    ['issueType'],
    ['assigneeAccountId'],
    ['priorityName'],
    ['labels'],
    ['components'],
    ['fields'],
  ],
  'sharepoint_get_metadata': [['item_url'], ['server_relative_url']],
  'sharepoint_read': [['item_url'], ['server_relative_url']],
  'sharepoint_summarize': [['item_url'], ['server_relative_url']],
  'sharepoint_download': [['item_url'], ['server_relative_url']],
  'cua_browser_click': [['index'], ['selector'], ['text']],
  'cua_browser_type': [['index'], ['selector'], ['text']],
  'display_table': [['data_json'], ['source']],
  'jira_find': [['query'], ['projectOrBoard']],
  'confluence_open_page': [['url'], ['pageId'], ['spaceKey', 'title']],
  'arxiv_search': [['query'], ['categories'], ['authors'], ['ids']],
  'slack_upload_file': [
    ['fileName', 'contentText'],
    ['fileName', 'contentBase64'],
    ['artifact_id'],
    ['path'],
    ['local_path'],
  ],
  'jira_upload_attachment': [
    ['fileName', 'contentText'],
    ['fileName', 'contentBase64'],
    ['artifact_id'],
    ['path'],
    ['local_path'],
  ],
};

export const STATIC_TOOL_INVENTORY: PermissionToolInventory = buildGeneratedStaticToolInventory();

function appToolNamesFor(inventory: PermissionToolInventory = STATIC_TOOL_INVENTORY): Set<string> {
  return new Set(inventory.appToolGroups.flatMap((group) => group.tools.map((tool) => tool.name)));
}

export function isAppToolName(
  toolName?: string,
  inventory: PermissionToolInventory = STATIC_TOOL_INVENTORY,
): boolean {
  return appToolNamesFor(inventory).has(String(toolName || '').trim());
}

/** Reverse index: internal tool name → owning connection. */
const TOOL_TO_CONNECTION = new Map<string, ConnectionCatalogId>();
for (const id of Object.keys(CONNECTION_TOOLS) as ConnectionCatalogId[]) {
  for (const tool of CONNECTION_TOOLS[id]) TOOL_TO_CONNECTION.set(tool.name, id);
}

export function getToolsForConnection(id: ConnectionCatalogId): ConnectionToolInfo[] {
  return STATIC_TOOL_INVENTORY.connectionTools[id] || [];
}

export function getToolsForConnectionFromInventory(
  id: ConnectionCatalogId,
  inventory: PermissionToolInventory = STATIC_TOOL_INVENTORY,
): ConnectionToolInfo[] {
  return inventory.connectionTools[id] || [];
}

/**
 * Connector strings seen in permission subjects (e.g. "jira") don't always
 * match a catalogue id; fold the known aliases before comparing.
 */
export function connectionIdForConnector(connector?: string): ConnectionCatalogId | null {
  const key = String(connector || '').trim().toLowerCase();
  if (!key) return null;
  if (key === 'jira' || key === 'confluence') return 'atlassian';
  return CONNECTION_META[key as ConnectionCatalogId] ? (key as ConnectionCatalogId) : null;
}

/** Which connection a saved rule belongs to, or null (e.g. local folders). */
export function connectionIdForRule(
  rule: PermissionRuleUI,
  inventory: PermissionToolInventory = STATIC_TOOL_INVENTORY,
): ConnectionCatalogId | null {
  const subject = rule.subject;
  switch (subject.kind) {
    case 'local_location':
      return null;
    case 'repository':
      return 'github';
    case 'connector':
      return connectionIdForConnector(subject.connector);
    case 'tool_set':
      return (
        connectionIdForConnector(subject.connector) ||
        connectionIdForConnector(subject.tool_set_id) ||
        null
      );
    case 'tool':
    default: {
      if (subject.tool) {
        if (isAppToolName(subject.tool, inventory)) return null;
        const owned = connectionForToolFromInventory(subject.tool, inventory);
        if (owned) return owned;
        const matched = getConnectionForToolName(subject.tool);
        if (matched) return matched;
      }
      return connectionIdForConnector(subject.connector);
    }
  }
}

export function normalizeRuntimeToolInventory(
  rawTools: unknown,
  rawProviderHostedTools?: unknown,
): PermissionToolInventory {
  return tryNormalizeRuntimeToolInventory(rawTools, rawProviderHostedTools) || STATIC_TOOL_INVENTORY;
}

export function tryNormalizeRuntimeToolInventory(
  rawTools: unknown,
  rawProviderHostedTools?: unknown,
): PermissionToolInventory | null {
  return buildInventoryFromRuntimeTools(rawTools, 'runtime', rawProviderHostedTools);
}

function buildInventoryFromRuntimeTools(
  rawTools: unknown,
  source: PermissionToolInventory['source'],
  rawProviderHostedTools?: unknown,
): PermissionToolInventory | null {
  const tools = Array.isArray(rawTools) ? rawTools : [];
  const connectionTools = emptyConnectionTools();
  const appGroups = new Map<string, AppToolGroup>();
  const seen = new Set<string>();

  for (const raw of tools) {
    const meta = normalizeRuntimeTool(raw);
    if (!meta || seen.has(meta.name)) continue;
    seen.add(meta.name);

    const tool: ConnectionToolInfo = {
      name: meta.name,
      label: meta.label,
      description: meta.description,
      category: meta.category,
      origin: meta.origin,
      toolSetId: meta.tool_set_id,
      toolSetName: meta.tool_set_name,
      connectorId: meta.connector_id,
      presentation: meta.presentation,
      test: meta.test,
    };

    if (LOCAL_COMPUTER_TOOL_NAMES.has(meta.name)) {
      connectionTools['local-computer'].push({
        ...tool,
        origin: 'local',
        toolSetId: LOCAL_COMPUTER_TOOL_SET_ID,
        toolSetName: LOCAL_COMPUTER_TOOL_SET_NAME,
        connectorId: 'local-computer',
      });
      continue;
    }

    const toolConnectionId = getConnectionForToolName(meta.name);
    if (toolConnectionId === 'image-generation') {
      connectionTools[toolConnectionId].push({
        ...tool,
        connectorId: toolConnectionId,
        toolSetId: toolConnectionId,
        toolSetName: CONNECTION_META[toolConnectionId].label,
      });
      continue;
    }

    if (meta.origin === 'connected') {
      const connectionId =
        connectionIdForConnector(meta.connector_id) ||
        connectionIdForConnector(meta.tool_set_id) ||
        getConnectionForToolName(meta.name);
      if (connectionId) {
        connectionTools[connectionId].push(tool);
        continue;
      }
    }

    const groupId = meta.tool_set_id || fallbackToolSetId(meta.origin);
    const staticGroup = STATIC_APP_GROUP_BY_ID.get(groupId);
    const existing = appGroups.get(groupId);
    if (existing) {
      existing.tools.push(tool);
    } else {
      appGroups.set(groupId, {
        id: groupId,
        origin:
          meta.origin === 'connected'
            ? 'connected'
            : meta.origin === 'builtin'
              ? 'builtin'
              : 'local',
        title: meta.tool_set_name || staticGroup?.title || titleFromId(groupId),
        description: staticGroup?.description || descriptionForRuntimeGroup(meta),
        tools: [tool],
      });
    }
  }

  if (seen.size === 0) return null;

  for (const id of Object.keys(connectionTools) as ConnectionCatalogId[]) {
    connectionTools[id] = sortToolsByStaticOrder(
      connectionTools[id],
      STATIC_CONNECTION_ORDER.get(id),
    );
  }

  return withProviderHostedCapabilities({
    connectionTools,
    appToolGroups: sortAppGroups(Array.from(appGroups.values())),
    source,
  }, rawProviderHostedTools);
}

export function staticToolMetadataForFixture(): RuntimeToolMetadata[] {
  const out: RuntimeToolMetadata[] = [];
  for (const id of Object.keys(STATIC_TOOL_INVENTORY.connectionTools) as ConnectionCatalogId[]) {
    for (const tool of STATIC_TOOL_INVENTORY.connectionTools[id]) {
      out.push(
        toolInfoToRuntimeMetadata(
          tool,
          tool.origin || 'connected',
          tool.toolSetId || id,
          tool.toolSetName || CONNECTION_META[id].label,
          tool.connectorId || id,
        ),
      );
    }
  }
  for (const group of STATIC_TOOL_INVENTORY.appToolGroups) {
    if (group.id === PROVIDER_HOSTED_TOOL_SET_ID) continue;
    for (const tool of group.tools) {
      out.push(
        toolInfoToRuntimeMetadata(
          tool,
          tool.origin || (group.id === 'attachments' ? 'attached' : group.origin),
          tool.toolSetId || group.id,
          tool.toolSetName || group.title,
        ),
      );
    }
  }
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

export function staticProviderHostedToolCapabilitiesForFixture(): ProviderHostedToolCapability[] {
  return GENERATED_STATIC_PROVIDER_HOSTED_TOOL_CAPABILITIES.map((capability) => ({
    ...capability,
    provider_tool_names: [...capability.provider_tool_names],
  }));
}

function buildStaticToolInventory(): PermissionToolInventory {
  const connectionTools = emptyConnectionTools();
  for (const id of Object.keys(CONNECTION_TOOLS) as ConnectionCatalogId[]) {
    connectionTools[id] = CONNECTION_TOOLS[id].map((tool) =>
      withStaticToolMetadata(
        tool,
        'connected',
        id,
        STATIC_CONNECTION_TOOL_SET_NAMES[id] || CONNECTION_META[id].label,
        id,
      ),
    );
  }

  const appToolGroups = APP_TOOL_GROUPS.map((group) => {
    const origin: ToolInventoryOrigin = group.id === 'attachments' ? 'attached' : group.origin;
    return {
      ...group,
      tools: group.tools.map((tool) => withStaticToolMetadata(tool, origin, group.id, group.title)),
    };
  });

  return {
    connectionTools,
    appToolGroups,
    source: 'static',
  };
}

function buildGeneratedStaticToolInventory(): PermissionToolInventory {
  const inventory = buildInventoryFromRuntimeTools(
    GENERATED_STATIC_TOOL_METADATA,
    'static',
    GENERATED_STATIC_PROVIDER_HOSTED_TOOL_CAPABILITIES,
  );
  if (!inventory) {
    throw new Error('Generated tool metadata is empty or invalid');
  }
  return inventory;
}

function withProviderHostedCapabilities(
  inventory: PermissionToolInventory,
  rawCapabilities: unknown,
): PermissionToolInventory {
  let tools = normalizeProviderHostedCapabilities(
    rawCapabilities === undefined
      ? GENERATED_STATIC_PROVIDER_HOSTED_TOOL_CAPABILITIES
      : rawCapabilities,
  );
  if (tools.length === 0) return inventory;

  const connectionTools = { ...inventory.connectionTools };
  const remaining: ConnectionToolInfo[] = [];
  for (const tool of tools) {
    const connectionId = tool.permissionOperation === 'webSearch'
      ? 'web-search'
      : tool.permissionOperation === 'codeExecution'
        ? 'python-sandbox'
        : null;
    if (connectionId) connectionTools[connectionId] = [...connectionTools[connectionId], tool];
    else remaining.push(tool);
  }

  const appToolGroups = inventory.appToolGroups.filter((group) => group.id !== PROVIDER_HOSTED_TOOL_SET_ID);
  if (remaining.length > 0) {
    appToolGroups.push({
      id: PROVIDER_HOSTED_TOOL_SET_ID,
      origin: 'provider',
      title: PROVIDER_HOSTED_TOOL_SET_NAME,
      description: 'Provider-native tools executed inside the selected model provider.',
      tools: remaining,
    });
  }

  return {
    ...inventory,
    connectionTools,
    appToolGroups: sortAppGroups(appToolGroups),
  };
}

function normalizeProviderHostedCapabilities(rawCapabilities: unknown): ConnectionToolInfo[] {
  const raw = Array.isArray(rawCapabilities) ? rawCapabilities : [];
  const out: ConnectionToolInfo[] = [];
  const seen = new Set<string>();
  for (const item of raw) {
    const cap = normalizeProviderHostedCapability(item);
    if (!cap || seen.has(cap.id)) continue;
    seen.add(cap.id);
    out.push(providerHostedCapabilityToToolInfo(cap));
  }
  return out.sort((a, b) => a.name.localeCompare(b.name));
}

function normalizeProviderHostedCapability(raw: unknown): ProviderHostedToolCapability | null {
  if (!raw || typeof raw !== 'object') return null;
  const value = raw as Record<string, unknown>;
  const id = cleanString(value.id);
  const name = cleanString(value.name);
  const category = normalizeCategory(value.category);
  const adapterFamily = cleanString(value.adapter_family);
  const settingsKey = cleanString(value.settings_key);
  if (!id || !name || !category || !adapterFamily || !settingsKey) return null;
  return {
    id,
    name,
    label: cleanString(value.label) || titleFromId(id),
    description: cleanString(value.description),
    category,
    origin: cleanString(value.origin) === 'provider' ? 'provider' : undefined,
    tool_set_id: cleanString(value.tool_set_id) || undefined,
    tool_set_name: cleanString(value.tool_set_name) || undefined,
    adapter_family: adapterFamily,
    settings_key: settingsKey,
    permission_subject_kind: cleanString(value.permission_subject_kind) || undefined,
    provider_tool_names: cleanStringArray(value.provider_tool_names),
    provider_executed: value.provider_executed === true,
    presentation: normalizePresentationSpec(
      value.presentation,
      id,
      category,
      'provider',
      cleanString(value.tool_set_id) || PROVIDER_HOSTED_TOOL_SET_ID,
      adapterFamily,
    ),
    test: normalizeTestSpec(value.test),
  };
}

function providerHostedCapabilityToToolInfo(cap: ProviderHostedToolCapability): ConnectionToolInfo {
  return {
    name: cap.id,
    label: cap.label,
    description: cap.description || cap.name,
    category: cap.category,
    origin: cap.origin || 'provider',
    toolSetId: cap.tool_set_id || PROVIDER_HOSTED_TOOL_SET_ID,
    toolSetName: cap.tool_set_name || PROVIDER_HOSTED_TOOL_SET_NAME,
    connectorId: cap.adapter_family,
    permissionSubjectKind: cap.permission_subject_kind || 'provider_hosted_tool',
    permissionOperation: cap.settings_key,
    presentation: cap.presentation,
    test: cap.test,
  };
}

function withStaticToolMetadata(
  tool: ConnectionToolInfo,
  origin: ToolInventoryOrigin,
  toolSetId: string,
  toolSetName: string,
  connectorId = '',
): ConnectionToolInfo {
  return {
    ...tool,
    description: tool.description || tool.label,
    origin,
    toolSetId,
    toolSetName,
    connectorId: connectorId || undefined,
    presentation: tool.presentation || presentationForTool(tool.name, tool.category, origin, toolSetId, connectorId),
    test: staticToolTestSpecFor(tool, origin, toolSetId, connectorId),
  };
}

function emptyConnectionTools(): Record<ConnectionCatalogId, ConnectionToolInfo[]> {
  const out = {} as Record<ConnectionCatalogId, ConnectionToolInfo[]>;
  for (const id of Object.keys(CONNECTION_META) as ConnectionCatalogId[]) out[id] = [];
  return out;
}

function normalizeRuntimeTool(raw: unknown): RuntimeToolMetadata | null {
  if (!raw || typeof raw !== 'object') return null;
  const value = raw as Record<string, unknown>;
  const name = cleanString(value.name);
  if (!name) return null;
  const category = normalizeCategory(value.category);
  if (!category) return null;
  const origin = normalizeOrigin(value.origin);
  if (!origin) return null;
  return {
    name,
    label: cleanString(value.label) || titleFromId(name),
    description: cleanString(value.description),
    category,
    origin,
    tool_set_id: cleanString(value.tool_set_id),
    tool_set_name: cleanString(value.tool_set_name),
    connector_id: cleanString(value.connector_id) || undefined,
    presentation: normalizePresentationSpec(value.presentation, name, category, origin, cleanString(value.tool_set_id), cleanString(value.connector_id)),
    test: normalizeTestSpec(value.test),
  };
}

function normalizePresentationSpec(
  raw: unknown,
  name: string,
  category: PermissionCategory,
  origin: ToolInventoryOrigin,
  toolSetId: string,
  connectorId = '',
): ToolPresentationSpec {
  const hasRawPresentation = raw !== null && typeof raw === 'object' && Object.keys(raw as Record<string, unknown>).length > 0;
  const value = hasRawPresentation ? (raw as Record<string, unknown>) : {};
  const fallback = presentationForTool(name, category, origin, toolSetId, connectorId);
  const artifactTypes = cleanStringArray(value.artifact_types);
  const fileKinds = cleanStringArray(value.file_kinds);
  const previewModes = cleanStringArray(value.preview_modes);
  const sourceRefs = cleanStringArray(value.source_refs);
  const nativeLinks = cleanStringArray(value.native_links);
  return {
    family: cleanString(value.family) || fallback.family,
    result_kind: cleanString(value.result_kind) || fallback.result_kind,
    renderer_strategy: cleanString(value.renderer_strategy) || fallback.renderer_strategy,
    renderer_key: cleanString(value.renderer_key) || fallback.renderer_key,
    native_viewer: cleanString(value.native_viewer) || fallback.native_viewer,
    artifact_types: artifactTypes.length > 0 ? artifactTypes : hasRawPresentation ? undefined : fallback.artifact_types,
    file_capability: cleanString(value.file_capability) || fallback.file_capability || undefined,
    file_kinds: fileKinds.length > 0 ? fileKinds : hasRawPresentation ? undefined : fallback.file_kinds,
    preview_modes: previewModes.length > 0 ? previewModes : hasRawPresentation ? undefined : fallback.preview_modes,
    source_refs: sourceRefs.length > 0 ? sourceRefs : hasRawPresentation ? undefined : fallback.source_refs,
    produces_file_artifact:
      typeof value.produces_file_artifact === 'boolean'
        ? value.produces_file_artifact
        : hasRawPresentation ? undefined : fallback.produces_file_artifact,
    promotes_native_input:
      typeof value.promotes_native_input === 'boolean'
        ? value.promotes_native_input
        : hasRawPresentation ? undefined : fallback.promotes_native_input,
    native_links: nativeLinks.length > 0 ? nativeLinks : hasRawPresentation ? undefined : fallback.native_links,
  };
}

function normalizeCategory(raw: unknown): PermissionCategory | null {
  return raw === 'read' || raw === 'write' || raw === 'execute' ? raw : null;
}

function normalizeOrigin(raw: unknown): ToolInventoryOrigin | null {
  return raw === 'builtin' || raw === 'connected' || raw === 'local' || raw === 'attached' || raw === 'provider'
    ? raw
    : null;
}

function normalizeTestSpec(raw: unknown): ToolInventoryTestSpec {
  const value = raw && typeof raw === 'object' ? (raw as Record<string, unknown>) : {};
  const requiredArguments = cleanStringArray(value.required_arguments);
  const requiredArgumentGroups = cleanStringMatrix(value.required_argument_groups);
  const requiredContext = cleanStringArray(value.required_context);
  const sampleArguments = cleanRecord(value.sample_arguments);
  const explicitInputRequired = value.input_required === true;
  return {
    supported: value.supported === true,
    mode: cleanString(value.mode) || 'none',
    requires_approval: value.requires_approval === true,
    input_required: explicitInputRequired || requiredArguments.length > 0 || requiredArgumentGroups.length > 0,
    required_arguments: requiredArguments.length > 0 ? requiredArguments : undefined,
    required_argument_groups:
      requiredArgumentGroups.length > 0 ? requiredArgumentGroups : undefined,
    required_context: requiredContext.length > 0 ? requiredContext : undefined,
    sample_arguments: Object.keys(sampleArguments).length > 0 ? sampleArguments : undefined,
    input_hint: cleanString(value.input_hint) || undefined,
    summary: cleanString(value.summary) || undefined,
  };
}

function cleanRecord(raw: unknown): Record<string, unknown> {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return {};
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(raw as Record<string, unknown>)) {
    const cleanKey = cleanString(key);
    if (cleanKey) out[cleanKey] = value;
  }
  return out;
}

function cleanString(raw: unknown): string {
  return String(raw || '').trim();
}

function cleanStringArray(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  const out: string[] = [];
  const seen = new Set<string>();
  for (const entry of raw) {
    const value = cleanString(entry);
    if (!value || seen.has(value)) continue;
    seen.add(value);
    out.push(value);
  }
  return out;
}

function cleanStringMatrix(raw: unknown): string[][] {
  if (!Array.isArray(raw)) return [];
  const out: string[][] = [];
  for (const group of raw) {
    const values = cleanStringArray(group);
    if (values.length > 0) out.push(values);
  }
  return out;
}

function fallbackToolSetId(origin: ToolInventoryOrigin): string {
  if (origin === 'attached') return 'attachments';
  if (origin === 'local') return 'local-tools';
  return 'app-tools';
}

function descriptionForRuntimeGroup(meta: RuntimeToolMetadata): string {
  if (meta.origin === 'connected') {
    return 'Tools provided by a runtime connection.';
  }
  if (meta.origin === 'local' || meta.origin === 'attached') {
    return 'Tools that work with local or attached files.';
  }
  if (meta.origin === 'provider') {
    return 'Provider-native capabilities executed by the selected model provider.';
  }
  if (meta.origin === 'builtin') return 'Tools built into the app.';
  return 'Tools provided by this tool set.';
}

function titleFromId(id: string): string {
  return id
    .split(/[_\-\s]+/)
    .filter(Boolean)
    .map((part) => part.slice(0, 1).toUpperCase() + part.slice(1))
    .join(' ');
}

function sortToolsByStaticOrder(
  tools: ConnectionToolInfo[],
  order?: Map<string, number>,
): ConnectionToolInfo[] {
  return tools
    .map((tool, index) => ({ tool, index }))
    .sort((a, b) => {
      const ai = order?.get(a.tool.name);
      const bi = order?.get(b.tool.name);
      if (ai !== undefined && bi !== undefined) return ai - bi;
      if (ai !== undefined) return -1;
      if (bi !== undefined) return 1;
      return a.index - b.index;
    })
    .map((entry) => entry.tool);
}

function sortAppGroups(groups: AppToolGroup[]): AppToolGroup[] {
  const staticGroupOrder = new Map(APP_TOOL_GROUPS.map((group, index) => [group.id, index]));
  return groups
    .map((group, index) => ({
      group: {
        ...group,
        tools: sortToolsByStaticOrder(group.tools, STATIC_APP_TOOL_ORDER.get(group.id)),
      },
      index,
    }))
    .sort((a, b) => {
      const ai = staticGroupOrder.get(a.group.id);
      const bi = staticGroupOrder.get(b.group.id);
      if (ai !== undefined && bi !== undefined) return ai - bi;
      if (ai !== undefined) return -1;
      if (bi !== undefined) return 1;
      return a.index - b.index;
    })
    .map((entry) => entry.group);
}

function connectionForToolFromInventory(
  toolName: string,
  inventory: PermissionToolInventory,
): ConnectionCatalogId | null {
  const name = String(toolName || '').trim();
  if (!name) return null;
  for (const id of Object.keys(inventory.connectionTools) as ConnectionCatalogId[]) {
    if (inventory.connectionTools[id].some((tool) => tool.name === name)) return id;
  }
  return TOOL_TO_CONNECTION.get(name) || null;
}

function toolInfoToRuntimeMetadata(
  tool: ConnectionToolInfo,
  origin: ToolInventoryOrigin,
  toolSetId: string,
  toolSetName: string,
  connectorId = '',
): RuntimeToolMetadata {
  return {
    name: tool.name,
    label: tool.label,
    description: tool.description || tool.label,
    category: tool.category,
    origin,
    tool_set_id: toolSetId,
    tool_set_name: toolSetName,
    connector_id: connectorId || undefined,
    presentation: tool.presentation || presentationForTool(tool.name, tool.category, origin, toolSetId, connectorId),
    test: tool.test || staticToolTestSpecFor(tool, origin, toolSetId, connectorId),
  };
}

function staticToolTestSpecFor(
  tool: ConnectionToolInfo,
  origin: ToolInventoryOrigin,
  toolSetId: string,
  connectorId = '',
): ToolInventoryTestSpec {
  if (STATIC_UNSUPPORTED_TEST_TOOLS.has(tool.name)) {
    return {
      supported: false,
      mode: 'none',
      requires_approval: false,
      input_required: false,
    };
  }

  const mode = STATIC_CONNECTION_CHECK_TOOLS.has(tool.name)
    ? 'connection_check'
    : tool.category === 'read'
      ? 'sample_call'
      : 'dry_run';
  const requiredArguments = [...(STATIC_REQUIRED_TEST_ARGUMENTS[tool.name] || [])];
  const requiredArgumentGroups = (STATIC_REQUIRED_TEST_ARGUMENT_GROUPS[tool.name] || []).map(
    (group) => [...group],
  );
  const requiredContext = staticRequiredToolTestContext(
    tool.name,
    origin,
    toolSetId,
    connectorId,
    mode,
  );
  const inputHint =
    requiredArguments.length > 0 || requiredArgumentGroups.length > 0 || requiredContext.length > 0
      ? staticToolTestInputHint(
          tool.name,
          requiredArguments,
          requiredArgumentGroups,
          requiredContext,
        )
      : undefined;
  return {
    supported: true,
    mode,
    requires_approval: tool.category !== 'read',
    input_required: requiredArguments.length > 0 || requiredArgumentGroups.length > 0,
    required_arguments: requiredArguments.length > 0 ? requiredArguments : undefined,
    required_argument_groups:
      requiredArgumentGroups.length > 0 ? requiredArgumentGroups : undefined,
    required_context: requiredContext.length > 0 ? requiredContext : undefined,
    input_hint: inputHint,
    summary: 'Runs a bounded test through the normal tool execution and permission path.',
  };
}

function presentationForTool(
  name: string,
  category: PermissionCategory,
  origin: ToolInventoryOrigin,
  toolSetId: string,
  connectorId = '',
): ToolPresentationSpec {
  let family = toolSetId || origin;
  let resultKind = 'generic';
  let nativeViewer = 'connector_inspector';
  let fileCapability = '';

  switch (toolSetId) {
    case 'local-computer':
    case 'local-files':
      resultKind = 'repository';
      fileCapability = 'local_repository';
      if (name === 'read_file' || name === 'local_read_file' || name === 'local_read_grant_file' || name === 'git_show_file_at_ref') {
        resultKind = 'file';
        nativeViewer = 'code';
        fileCapability = 'local_file_read';
      } else if (name === 'local_open_file') {
        resultKind = 'file';
        nativeViewer = 'file';
        fileCapability = 'local_file_read';
      } else if (name === 'local_list_folder' || name === 'local_list_grant_folder') {
        resultKind = 'folder';
        fileCapability = 'local_folder_list';
      } else if (name === 'search_files' || name === 'file_search' || name === 'local_search_command' || name === 'local_search_in_grant' || name === 'git_grep') {
        resultKind = 'search';
        fileCapability = 'local_file_search';
      }
      break;
    case 'attachments':
      resultKind = 'metadata';
      nativeViewer = 'connector_inspector';
      fileCapability = 'attachment_inspect';
      if (name === 'read_attachment') {
        resultKind = 'file';
        nativeViewer = 'file';
        fileCapability = 'attachment_read';
      } else if (name === 'search_attachment') {
        resultKind = 'search';
        fileCapability = 'attachment_search';
      }
      break;
    case 'artifacts':
      resultKind = 'artifact';
      fileCapability = 'artifact_read';
      if (name === 'build_chart_artifact') {
        resultKind = 'chart';
        nativeViewer = 'chart';
        fileCapability = 'artifact_create';
      } else if (name === 'display_table') {
        resultKind = 'data';
        nativeViewer = 'data';
        fileCapability = 'artifact_create';
      } else if (name === 'create_interactive_artifact' || name === 'create_interactive_data_artifact') {
        resultKind = 'interactive';
        nativeViewer = 'interactive_html';
        fileCapability = 'artifact_create';
      } else if (name === 'export_thread') {
        resultKind = 'file';
        nativeViewer = 'file';
        fileCapability = 'artifact_create';
      } else if (name === 'get_echarts_capabilities' || name === 'get_highcharts_capabilities') {
        resultKind = 'status';
        fileCapability = '';
      }
      break;
    case 'image-generation':
      resultKind = 'image';
      nativeViewer = 'image';
      fileCapability = 'artifact_create';
      break;
    case 'data-analysis':
      resultKind = 'data';
      nativeViewer = 'data';
      fileCapability = ['describe_dataset', 'sample_rows', 'filter_csv', 'aggregate_csv'].includes(name)
        ? 'local_file_read'
        : 'dataset_artifact_read';
      break;
    case 'query-lifecycle':
      resultKind = 'query';
      nativeViewer = 'query';
      break;
    case 'thread-search':
      resultKind = 'search';
      if (name === 'get_artifact') {
        resultKind = 'artifact';
        fileCapability = 'artifact_read';
      } else if (name === 'rlm_exec') {
        resultKind = 'code';
        nativeViewer = 'code';
      }
      break;
    case 'bigquery':
    case 'databricks':
    case 'trino':
    case 'snowflake':
      resultKind = 'query';
      nativeViewer = 'query';
      if (!presentationIsQueryRunTool(name)) {
        resultKind = presentationDatasourceCatalogKind(name);
        nativeViewer = 'connector_inspector';
      }
      break;
    case 'github':
      resultKind = 'connector';
      if (name === 'github_clone_repository') {
        resultKind = 'repository';
        fileCapability = 'repository_clone';
      } else if (name === 'github_pull_repository' || name === 'github_refresh_repository') {
        resultKind = 'repository';
        fileCapability = 'local_repository';
      } else if (name === 'github_run_gh_command') {
        resultKind = 'command';
        nativeViewer = 'code';
      }
      break;
    case 'slack':
      resultKind = 'connector';
      if (name === 'slack_download_file') {
        resultKind = 'file';
        nativeViewer = 'file';
        fileCapability = 'remote_file_download';
      } else if (name === 'slack_upload_file') {
        resultKind = 'file';
        fileCapability = 'remote_file_upload';
      }
      break;
    case 'atlassian':
      resultKind = 'connector';
      if (name.startsWith('jira_')) family = 'jira';
      if (name.startsWith('confluence_')) family = 'confluence';
      if (name === 'jira_read_attachment') {
        resultKind = 'file';
        nativeViewer = 'file';
        fileCapability = 'remote_file_download';
      } else if (name === 'jira_upload_attachment') {
        resultKind = 'file';
        fileCapability = 'remote_file_upload';
      }
      break;
    case 'sharepoint':
      resultKind = 'connector';
      if (name === 'sharepoint_read' || name === 'sharepoint_summarize' || name === 'sharepoint_download') {
        resultKind = 'file';
        nativeViewer = 'file';
        fileCapability = 'remote_file_download';
      }
      break;
    case 'browser':
      resultKind = 'browser';
      if (category === 'execute') fileCapability = 'browser_control';
      break;
    case 'arxiv':
      resultKind = 'paper';
      if (name === 'arxiv_download_paper') {
        resultKind = 'file';
        nativeViewer = 'file';
        fileCapability = 'remote_file_download';
      } else if (name === 'arxiv_get_paper_text' || name === 'arxiv_read_paper') {
        resultKind = 'file';
        nativeViewer = 'code';
        fileCapability = 'remote_file_read';
      } else if (
        name === 'arxiv_search' ||
        name === 'arxiv_screening_report' ||
        name === 'arxiv_recommendations' ||
        name === 'arxiv_list_papers'
      ) {
        resultKind = 'data';
      }
      break;
    default:
      if (origin === 'local' || origin === 'attached') {
        resultKind = 'file';
        nativeViewer = 'file';
        fileCapability = 'local_file_read';
      }
      break;
  }

  const [rendererStrategy, rendererKey] = presentationRenderer(name, family, resultKind, nativeViewer);
  return {
    family,
    result_kind: resultKind,
    renderer_strategy: rendererStrategy,
    renderer_key: rendererKey,
    native_viewer: nativeViewer,
    artifact_types: presentationArtifactTypes(resultKind, nativeViewer),
    file_capability: fileCapability || undefined,
    file_kinds: presentationFileKinds(fileCapability, nativeViewer),
    preview_modes: presentationPreviewModes(fileCapability, nativeViewer),
    source_refs: presentationSourceRefs(name, toolSetId, resultKind, fileCapability),
    produces_file_artifact: presentationProducesFileArtifact(name, fileCapability) || undefined,
    promotes_native_input: presentationPromotesNativeInput(fileCapability) || undefined,
    native_links: presentationNativeLinks(name, family, toolSetId, resultKind),
  };
}

function presentationRenderer(
  name: string,
  family: string,
  resultKind: string,
  nativeViewer: string,
): [string, string] {
  if (presentationUsesToolRenderer(name)) {
    return ['tool', name.trim()];
  }
  if (nativeViewer && nativeViewer !== 'connector_inspector') {
    return ['artifact_type', nativeViewer];
  }
  if (family && resultKind !== 'generic') {
    return ['family', family];
  }
  return ['generic', 'connector_inspector'];
}

function presentationArtifactTypes(resultKind: string, nativeViewer: string): string[] | undefined {
  if (nativeViewer === 'image') return ['image', 'file'];
  if (nativeViewer === 'file') return ['file'];
  if (nativeViewer === 'code' || nativeViewer === 'interactive_html') return ['code'];
  if (nativeViewer === 'chart' || nativeViewer === 'data' || nativeViewer === 'query') return ['data'];
  if (resultKind === 'artifact') return ['data', 'code', 'image', 'file'];
  if (resultKind === 'catalog' || resultKind === 'table_schema') return ['data'];
  if (resultKind === 'file') return ['file'];
  return undefined;
}

function presentationFileKinds(fileCapability: string, nativeViewer: string): string[] | undefined {
  if (nativeViewer !== 'file' && !presentationHasFileSource(fileCapability)) return undefined;
  return ['pdf', 'image', 'text', 'html', 'document', 'spreadsheet', 'presentation', 'archive'];
}

function presentationPreviewModes(fileCapability: string, nativeViewer: string): string[] | undefined {
  if (nativeViewer !== 'file' && !presentationHasFileSource(fileCapability)) return undefined;
  return [
    'raw_download',
    'text_preview',
    'pdf_canvas',
    'image_raw',
    'html_sandbox',
    'spreadsheet_workbook',
    'presentation_slides',
    'download_only',
  ];
}

function presentationHasFileSource(fileCapability: string): boolean {
  return [
    'local_file_read',
    'local_file_search',
    'local_folder_list',
    'attachment_read',
    'dataset_artifact_read',
    'remote_file_download',
    'remote_file_read',
    'remote_file_upload',
    'artifact_read',
  ].includes(fileCapability);
}

function presentationSourceRefs(
  name: string,
  toolSetId: string,
  resultKind: string,
  fileCapability: string,
): string[] | undefined {
  if (name === 'display_table' || name === 'build_chart_artifact') {
    return ['source.artifact_id', 'source.path', 'source.job_id', 'owner_token'];
  }
  if (name === 'create_interactive_data_artifact') {
    return ['source.artifact_id'];
  }
  if (name === 'local_open_file') {
    return ['artifact_id', 'path', 'local_path'];
  }
  const catalogRefs = presentationDatasourceCatalogSourceRefs(name);
  if (catalogRefs) return catalogRefs;
  if (name === 'slack_download_file') {
    return ['file_id', 'channel_id', 'remote_id', 'artifact_id'];
  }
  if (name === 'jira_read_attachment') {
    return ['issueKey', 'attachmentId', 'remote_id', 'artifact_id'];
  }
  if (
    name === 'sharepoint_read' ||
    name === 'sharepoint_summarize' ||
    name === 'sharepoint_download'
  ) {
    return ['item_url', 'server_relative_url', 'site_url', 'remote_id', 'artifact_id'];
  }
  if (name === 'arxiv_download_paper') {
    return ['arxiv_id', 'remote_id', 'artifact_id'];
  }
  if (name === 'arxiv_get_paper_text' || name === 'arxiv_read_paper') {
    return ['paper', 'arxiv_id', 'remote_id', 'artifact_id'];
  }
  if (fileCapability === 'remote_file_upload') {
    return ['artifact_id', 'path', 'local_path', 'fileName', 'contentText', 'contentBase64'];
  }
  if (
    fileCapability === 'local_file_read' ||
    fileCapability === 'local_file_search' ||
    fileCapability === 'local_folder_list' ||
    fileCapability === 'local_repository'
  ) {
    return ['path', 'local_path'];
  }
  if (fileCapability === 'repository_clone') return ['remote_id', 'local_path'];
  if (
    fileCapability === 'attachment_read' ||
    fileCapability === 'attachment_inspect' ||
    fileCapability === 'attachment_search' ||
    fileCapability === 'artifact_read' ||
    fileCapability === 'dataset_artifact_read'
  ) {
    return ['artifact_id'];
  }
  if (fileCapability === 'remote_file_download' || fileCapability === 'remote_file_read') {
    return ['remote_id', 'artifact_id'];
  }
  if (resultKind === 'query' && (toolSetId === 'query-lifecycle' || name.startsWith('run_'))) {
    return ['remote_id'];
  }
  return undefined;
}

function presentationIsQueryRunTool(name: string): boolean {
  return [
    'run_bigquery_query',
    'run_databricks_query',
    'run_trino_query',
    'run_snowflake_query',
  ].includes(String(name || '').trim());
}

function presentationUsesToolRenderer(name: string): boolean {
  return [
    'list_bq_datasets',
    'list_bq_tables',
    'get_bq_table_schema',
    'list_databricks_catalogs',
    'list_databricks_schemas',
    'list_databricks_tables',
    'describe_databricks_table',
    'list_trino_catalogs',
    'list_trino_schemas',
    'list_trino_tables',
    'describe_trino_table',
    'list_snowflake_databases',
    'list_snowflake_schemas',
    'list_snowflake_tables',
    'describe_snowflake_table',
  ].includes(String(name || '').trim());
}

function presentationDatasourceCatalogKind(name: string): string {
  return [
    'get_bq_table_schema',
    'describe_databricks_table',
    'describe_trino_table',
    'describe_snowflake_table',
  ].includes(String(name || '').trim())
    ? 'table_schema'
    : 'catalog';
}

function presentationDatasourceCatalogSourceRefs(name: string): string[] | undefined {
  switch (String(name || '').trim()) {
    case 'list_bq_datasets':
      return ['project_id'];
    case 'list_bq_tables':
      return ['project_id', 'dataset_id'];
    case 'get_bq_table_schema':
      return ['project_id', 'dataset_id', 'table_id'];
    case 'list_databricks_catalogs':
    case 'list_trino_catalogs':
    case 'list_snowflake_databases':
      return ['connection'];
    case 'list_databricks_schemas':
    case 'list_trino_schemas':
      return ['catalog_name'];
    case 'list_databricks_tables':
    case 'list_trino_tables':
      return ['catalog_name', 'schema_name'];
    case 'describe_databricks_table':
    case 'describe_trino_table':
    case 'describe_snowflake_table':
      return ['full_name'];
    case 'list_snowflake_schemas':
      return ['database_name'];
    case 'list_snowflake_tables':
      return ['database_name', 'schema_name'];
    default:
      return undefined;
  }
}

function presentationProducesFileArtifact(name: string, fileCapability: string): boolean {
  return name === 'export_thread' || name === 'local_open_file' || name === 'generate_image' || fileCapability === 'remote_file_download';
}

function presentationPromotesNativeInput(fileCapability: string): boolean {
  return [
    'local_file_read',
    'attachment_read',
    'dataset_artifact_read',
    'remote_file_download',
    'remote_file_read',
  ].includes(fileCapability);
}

function presentationNativeLinks(
  name: string,
  family: string,
  toolSetId: string,
  resultKind: string,
): string[] | undefined {
  const links: string[] = [];
  const add = (value: string) => {
    if (!value || links.includes(value)) return;
    links.push(value);
  };
  if (resultKind === 'query' && (toolSetId === 'query-lifecycle' || name.startsWith('run_'))) {
    add('warehouse_job');
  }
  if (toolSetId === 'browser') add('browser_url');
  if (['github', 'jira', 'confluence', 'sharepoint', 'slack', 'arxiv'].includes(family)) {
    add('source_url');
  }
  return links.length > 0 ? links : undefined;
}

function staticRequiredToolTestContext(
  name: string,
  origin: ToolInventoryOrigin,
  toolSetId: string,
  connectorId: string,
  mode: string,
): string[] {
  const context: string[] = [];
  const add = (value: string) => {
    const cleaned = value.trim();
    if (!cleaned || context.includes(cleaned)) return;
    context.push(cleaned);
  };

  if (connectorId) add(`connection:${connectorId}`);
  if (name === 'local_search_command') add('connection:local-computer');
  if (origin === 'attached') add('thread');
  if (
    origin === 'local' &&
    (toolSetId === 'local-files' ||
      (toolSetId === 'data-analysis' && STATIC_DATA_ANALYSIS_PROJECT_CONTEXT_TOOLS.has(name)))
  ) {
    add('project');
  }

  switch (toolSetId) {
    case 'attachments':
      add('thread');
      break;
    case 'artifacts':
      if (name === 'export_thread') add('thread');
      if (STATIC_ARTIFACT_CONTEXT_TOOLS.has(name)) {
        add('artifact');
      }
      break;
    case 'thread-search':
      add('search_index');
      break;
    case 'github':
      if (STATIC_GITHUB_PROJECT_CONTEXT_TOOLS.has(name)) add('project');
      break;
    case 'browser':
      add('browser');
      break;
    default:
      break;
  }

  if (mode === 'connection_check' && context.length === 0 && connectorId) {
    add(`connection:${connectorId}`);
  }
  return context;
}

function staticToolTestInputHint(
  name: string,
  requiredArguments: string[],
  requiredArgumentGroups: string[][],
  requiredContext: string[],
): string {
  const parts: string[] = [];
  if (requiredContext.length > 0) parts.push(`requires ${requiredContext.join(', ')}`);
  if (requiredArguments.length > 0) parts.push(`arguments: ${requiredArguments.join(', ')}`);
  if (requiredArgumentGroups.length > 0) {
    parts.push(
      `one of: ${requiredArgumentGroups.map((group) => group.join(' + ')).join(' OR ')}`,
    );
  }
  return `Tool test for ${name} ${parts.join('; ')}.`;
}

export type EffectiveSource = 'tool-rule' | 'tool-set-rule' | 'connector-rule' | 'default';

export interface EffectiveToolPermission {
  effect: PermissionEffect;
  source: EffectiveSource;
  /** The saved rule producing the effect, when source is not 'default'. */
  rule?: PermissionRuleUI;
}

/**
 * Effective behavior for one tool: a tool-scoped rule wins, then a tool-set
 * rule, then a connection-wide rule of the same category, then the built-in default
 * (connected-source reads run without asking; writes and executes ask).
 * Thread-scoped rules never contribute — they belong to a single chat.
 */
export function resolveToolPermission(
  tool: ConnectionToolInfo,
  connectionId: ConnectionCatalogId,
  rules: PermissionRuleUI[],
): EffectiveToolPermission {
  let toolSetRule: PermissionRuleUI | undefined;
  let connectorRule: PermissionRuleUI | undefined;
  for (const rule of rules) {
    if (isThreadScope(rule.scope)) continue;
    if (toolScopedRuleMatchesTool(rule, tool)) {
      return { effect: rule.effect, source: 'tool-rule', rule };
    }
    if (
      !toolSetRule &&
      rule.subject.kind === 'tool_set' &&
      rule.category === tool.category &&
      toolSetRuleMatchesTool(rule.subject, tool, connectionId)
    ) {
      toolSetRule = rule;
    }
    if (
      supportsConnectionWidePermissionRule(tool) &&
      !connectorRule &&
      rule.subject.kind === 'connector' &&
      rule.category === tool.category &&
      connectionIdForConnector(rule.subject.connector) === connectionId
    ) {
      connectorRule = rule;
    }
  }
  if (toolSetRule) {
    return { effect: toolSetRule.effect, source: 'tool-set-rule', rule: toolSetRule };
  }
  if (connectorRule) {
    return { effect: connectorRule.effect, source: 'connector-rule', rule: connectorRule };
  }
  return { effect: tool.category === 'read' ? 'allow' : 'ask', source: 'default' };
}

export function resolveStandaloneToolPermission(
  tool: ConnectionToolInfo,
  rules: PermissionRuleUI[],
): EffectiveToolPermission {
  let toolSetRule: PermissionRuleUI | undefined;
  let connectorRule: PermissionRuleUI | undefined;
  for (const rule of rules) {
    if (isThreadScope(rule.scope)) continue;
    if (toolScopedRuleMatchesTool(rule, tool)) {
      return { effect: rule.effect, source: 'tool-rule', rule };
    }
    if (
      !toolSetRule &&
      rule.subject.kind === 'tool_set' &&
      rule.category === tool.category &&
      toolSetRuleMatchesTool(rule.subject, tool, null)
    ) {
      toolSetRule = rule;
    }
    if (
      !connectorRule &&
      rule.subject.kind === 'connector' &&
      rule.category === tool.category &&
      connectorRuleMatchesStandaloneTool(rule.subject.connector, tool)
    ) {
      connectorRule = rule;
    }
  }
  if (toolSetRule) {
    return { effect: toolSetRule.effect, source: 'tool-set-rule', rule: toolSetRule };
  }
  if (connectorRule) {
    return { effect: connectorRule.effect, source: 'connector-rule', rule: connectorRule };
  }
  return { effect: tool.category === 'read' ? 'allow' : 'ask', source: 'default' };
}

function toolScopedRuleMatchesTool(rule: PermissionRuleUI, tool: ConnectionToolInfo): boolean {
  const expectedKind = String(tool.permissionSubjectKind || 'tool').trim() || 'tool';
  if (rule.subject.kind !== expectedKind || rule.subject.tool !== tool.name) {
    return false;
  }
  const ruleConnector = String(rule.subject.connector || '').trim().toLowerCase();
  const toolConnector = String(tool.connectorId || '').trim().toLowerCase();
  if (ruleConnector || toolConnector) {
    if (!ruleConnector || !toolConnector) return false;
    if (ruleConnector !== toolConnector) {
      const ruleConnection = connectionIdForConnector(ruleConnector);
      const toolConnection = connectionIdForConnector(toolConnector);
      if (!ruleConnection || !toolConnection || ruleConnection !== toolConnection) {
        return false;
      }
    }
  }
  const ruleToolSetId = String(rule.subject.tool_set_id || '').trim().toLowerCase();
  if (ruleToolSetId && ruleToolSetId !== String(tool.toolSetId || '').trim().toLowerCase()) {
    return false;
  }
  return true;
}

function toolSetRuleMatchesTool(
  subject: PermissionRuleUI['subject'],
  tool: ConnectionToolInfo,
  connectionId: ConnectionCatalogId | null,
): boolean {
  const ruleToolSetId = String(subject.tool_set_id || '').trim().toLowerCase();
  if (!ruleToolSetId || ruleToolSetId !== String(tool.toolSetId || '').trim().toLowerCase()) {
    return false;
  }
  const rawRuleConnector = String(subject.connector || '').trim().toLowerCase();
  const ruleConnection = connectionIdForConnector(rawRuleConnector);
  if (connectionId) {
    if (!rawRuleConnector) return true;
    return ruleConnection === connectionId || rawRuleConnector === connectionId;
  }
  if (rawRuleConnector) {
    const toolConnector = String(tool.connectorId || '').trim().toLowerCase();
    if (toolConnector && rawRuleConnector === toolConnector) return true;
    if (ruleConnection) {
      const toolConnection = connectionIdForConnector(tool.connectorId || tool.toolSetId);
      return toolConnection === ruleConnection;
    }
    return false;
  }
  return true;
}

function connectorRuleMatchesStandaloneTool(
  connector: unknown,
  tool: ConnectionToolInfo,
): boolean {
  const ruleConnector = String(connector || '').trim().toLowerCase();
  if (!ruleConnector) return false;
  const candidates = [tool.connectorId, tool.toolSetId]
    .map((value) => String(value || '').trim().toLowerCase())
    .filter(Boolean);
  return candidates.includes(ruleConnector);
}
