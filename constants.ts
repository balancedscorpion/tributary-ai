import { AppSettings } from "./types";
import {
  BEDROCK_COMPACTION_DEFAULT_INSTRUCTION,
  BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS,
  OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS,
} from "./lib/bedrockCompaction";
import { CAPABILITY_UX_DEFAULTS } from "./featureFlags";
import { DEFAULT_BIGQUERY_AUTOMATIC_SCAN_BYTES } from "./lib/scanBudget";

const showAllProviders = () => {
  const raw = (import.meta as { env?: Record<string, string | undefined> }).env
    ?.VITE_SHOW_ALL_PROVIDERS;
  if (typeof raw !== "string") return false;
  const normalized = raw.trim().toLowerCase();
  return (
    normalized === "1" ||
    normalized === "true" ||
    normalized === "on" ||
    normalized === "yes"
  );
};
const showExpandedCloudCredentials = () => showAllProviders();

export const DEFAULT_DATABASE_QUERY_ROW_LIMIT = 2500;
export const MAX_DATABASE_QUERY_ROW_LIMIT = 2500;
export const DEFAULT_DATABASE_QUERY_POLICY = {
  maxExecutingGlobal: 8,
  maxExecutingPerThread: 3,
  maxExecutingPerConnection: 2,
  inlineWaitMilliseconds: 90_000,
  maxPollIntervalMilliseconds: 30_000,
  monitorErrorBackoffMilliseconds: 120_000,
  softQueueTimeoutMilliseconds: 600_000,
  softExecutionTimeoutMilliseconds: 1_800_000,
  hardExecutionTimeoutMilliseconds: 21_600_000,
  maxResultRows: DEFAULT_DATABASE_QUERY_ROW_LIMIT,
  maxMaterializedBytes: 32 * 1024 * 1024,
  bigQueryMaximumBytesBilled: DEFAULT_BIGQUERY_AUTOMATIC_SCAN_BYTES,
  maxAutoContinuations: 8,
} as const;

export const DEFAULT_SETTINGS: AppSettings = {
  provider: {
    providerId: "compatible",
    model: "",
    profileId: "custom",
  },
  featureFlags: {
    activation: true,
    providers: {
      openai: showAllProviders(),
      anthropic: showAllProviders(),
      gemini: showAllProviders(),
      bedrock: true,
      compatible: true,
    },
    providerTemplates: true,
    cloudCredentialsAws: showExpandedCloudCredentials(),
    cloudCredentialsTrino: showExpandedCloudCredentials(),
    cloudCredentialsSnowflake: showExpandedCloudCredentials(),
    chartDimensionFilters: false,
    chartEngines: {
      echarts: true,
      highcharts: false,
    },
    capabilityUx: { ...CAPABILITY_UX_DEFAULTS },
  },
  cloudCredentials: {
    aws: [],
    gcp: [],
  },
  providerConfigs: [],
  providerTemplates: [],
  activeProviderConfigId: null,
  compatibleProfiles: [
    {
      id: "custom",
      displayName: "Custom Provider",
      apiType: "chat_completions",
      baseUrl: "https://api.openai.com/v1",
      auth: { type: "api_key", header: "Authorization", scheme: "Bearer" },
      apiKey: "",
      extraHeaders: [],
      queryParams: null,
      payloadOverrides: null,
      timeoutMs: null,
      reasoningEffort: null,
      modelInPath: false,
      modelInBody: true,
      endpointSuffix: null,
    },
  ],
  openai: {
    apiKey: "",
    baseUrl: "https://api.openai.com/v1",
    compactionEnabled: true,
    compactionTriggerTokens: OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS,
    webSearch: {
      enabled: true,
    },
  },
  anthropic: {
    apiKey: "",
    baseUrl: "https://api.anthropic.com",
    anthropicVersion: "2023-06-01",
    compactionEnabled: true,
    compactionInstruction: BEDROCK_COMPACTION_DEFAULT_INSTRUCTION,
    compactionTriggerTokens: BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS,
    webSearch: {
      enabled: true,
      toolType: "web_search_20260209",
      maxUses: 5,
      allowedCallers: ["direct"],
    },
  },
  gemini: {
    apiKey: "",
    baseUrl: "https://generativelanguage.googleapis.com",
  },
  bedrock: {
    region: "us-east-1",
    baseUrl: null,
    anthropicVersion: "bedrock-2023-05-31",
    extraHeaders: [],
    compactionEnabled: true,
    compactionInstruction: BEDROCK_COMPACTION_DEFAULT_INSTRUCTION,
    compactionTriggerTokens: BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS,
    apiKey: "",
    accessKeyId: "",
    secretAccessKey: "",
    sessionToken: null,
    webSearch: null,
    codeExecution: null,
  },
  systemPrompt: `You are a helpful, witty, and precise AI assistant. 
  
If the user uploads a directory/files, you have access to tools to read them.
- Use 'list_files' to explore the directory structure first.
- Use 'search_files' to find specific code patterns or text.
- Use 'read_file' to examine specific file contents.
- Always check the file structure before assuming file paths exists.
- Batch tool calls when possible (e.g., request multiple read_file calls in one step), and avoid repeating the same tool call with identical arguments.

If the project includes GitHub repositories, you also have read-only git tools.
- Use 'list_git_repositories' before git history work.
- Use 'git_list_refs', 'git_log', 'git_search_history', 'git_grep', 'git_show_commit', and 'git_show_file_at_ref' to inspect branches, commits, diffs, and past file contents.
- These tools are read-only; do not claim to push, commit, or modify repositories through them.

When working with CSV files:
- Use 'describe_dataset' first to profile any CSV file — it returns column names, types, row count, statistics, and sample values without loading the full file.
- Use 'sample_rows' to inspect specific rows or get a random sample.
- Use 'filter_csv' to find rows matching specific conditions.
- Use 'aggregate_csv' to compute group-by aggregations (count, sum, avg, min, max, count_distinct).
- Avoid using 'read_file' on large CSV files; prefer the CSV analysis tools above.
- For small CSV files (under ~50 rows), 'read_file' is acceptable.

	When presenting data, use Markdown tables if the data is structured.

		For advanced charting in artifacts:
		- Call the available chart capability tool(s) before advanced chart generation (for example 'get_echarts_capabilities' or 'get_highcharts_capabilities') to fetch the current capability_version and supported chart contract.
		- Use 'display_table' with exactly one chart spec field per call: 'echarts_spec' or 'chart_spec'.
		- If a tool returns code 'DATA_TOO_LARGE_FOR_DISPLAY_TABLE', retry 'display_table' with source.artifact_id or source.path for advanced specs; use 'build_chart_artifact' for intent-based scatter/line/bar charts.
		- Never generate raw JavaScript callbacks in chart specs. Use callback IDs only.

For custom interactive visuals or small browser-based tools:
- Use 'create_interactive_artifact' to render self-contained HTML/CSS/JavaScript in a sandboxed iframe.
- When the visual depends on dataset rows, use 'create_interactive_data_artifact' with source.artifact_id instead of embedding rows in HTML or tool arguments. In the HTML, call window.sandboxData.getMetadata() and window.sandboxData.getRows({ offset, limit, columns }) for focused pages.
- Keep the artifact standalone; external network requests, external scripts, external stylesheets, cookies, and app API access are blocked.
- Prefer display_table/build_chart_artifact for ordinary data tables and charts; use create_interactive_artifact when bespoke interactivity is needed.

Tool results are returned as JSON envelopes. Read the actual tool output from the "result" field.
- artifact_id: stable UUID for this tool result (can be referenced later)
- ok: whether the tool succeeded
- result_format: "json" or "text"
- result: the tool output payload.

When data/query rows are compacted or too dense to read inline:
- Use inspect_dataset_artifact with artifact_id first to inspect schema, row counts, stats, and sample values.
- Use aggregate_dataset for totals, averages, distinct counts, and group-bys.
- Use get_dataset_rows or search_dataset_rows for focused pages only; keep pages to 50 rows or fewer.
- Do not request larger inline row payloads as a workaround for compaction.

When the user references artifacts (marked with "📎 Referenced Artifacts"), they are asking about those specific tool results. A content preview is included — for large datasets, only the first rows are shown. Treat tool-result envelopes as context previews, not full datasets. Use artifact_id plus paging/retrieval tools when you need more rows, and do not request inline full-data payloads to bypass compaction.

When the user references message blocks (marked with "Referenced Message Blocks"), treat those snapshots as focused context from earlier user/assistant/tool content. Use the block metadata (role, message id/seq, block key) to ground follow-up answers and avoid repeating unrelated history.`,
  maxToolIterations: 200,
  databaseQueryRowLimit: DEFAULT_DATABASE_QUERY_ROW_LIMIT,
  databaseQueryPolicy: { ...DEFAULT_DATABASE_QUERY_POLICY },
  googleProjectId: "",
  googleAccessToken: "",
  googleCredentialsPath: "",
  databricksHost: "",
  databricksWarehouseId: "",
  databricksOAuthClientId: "",
  githubOAuthClientId: "",
  slackOAuthClientId: "",
  trinoHost: "",
  trinoCatalog: "",
  trinoSchema: "",
  trinoUser: "",
  trinoSource: "ai-analyst",
  snowflakeAccountIdentifier: "",
  snowflakeWarehouse: "",
  snowflakeDatabase: "",
  snowflakeSchema: "",
  snowflakeRole: "",
  snowflakeUser: "",
  dataSources: {
    bigquery: {
      enabled: false,
    },
    databricks: {
      enabled: false,
    },
    trino: {
      enabled: false,
    },
    snowflake: {
      enabled: false,
    },
  },
  connections: {
    visibleIds: ["thread-search"],
  },
  threadSearch: {
    enabled: true,
  },
  localComputer: {
    enabled: false,
    accessMode: "whole-computer",
    selectedFolders: [],
  },
  atlassian: {
    jiraBaseUrl: "",
    confluenceBaseUrl: "",
    defaultJiraProjectKey: "",
    allowedProjects: [],
    allowedAuthDomains: ["id.atlassian.com", "auth.atlassian.com", "login.microsoftonline.com"],
    browserProvider: "bundled",
    browserChannel: "",
    browserExecutablePath: "",
    browserProfileDir: "",
    jira: {
      enabled: false,
      showBrowser: false,
    },
    confluence: {
      enabled: false,
      showBrowser: false,
    },
  },
  browserControl: {
    enabled: false,
    showBrowser: false,
    browserProfileDir: "",
  },
  sharepoint: {
    siteUrl: "",
    enabled: false,
    showBrowser: false,
    browserProfileDir: "",
    authMode: "browser",
  },
  github: {
    enabled: false,
    readTools: false,
    allowedRepositories: [],
  },
  slack: {
    enabled: false,
    readTools: false,
    writeTools: false,
    writeIdentity: "user",
    allowedTeamIds: [],
    allowedChannelIds: [],
  },
  arxiv: {
    enabled: false,
    contactEmail: "",
  },
  imageGeneration: {
    enabled: false,
    providerId: null,
    model: "gpt-image-2",
    defaults: {
      quality: "auto",
      size: "auto",
      format: "png",
    },
    maxImagesPerRun: 1,
  },
};

export const STORAGE_KEY = "chat_app_settings_v2";
