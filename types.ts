export {
  Role,
  type ChatMessage,
  type ToolCall,
  type ToolDefinition,
  type ProviderId,
  type ProviderSelection,
  type ProviderRuntimeSettings,
  type CompatibleProfile,
  type HeaderKV as Header,
  type OpenAIConfig,
  type AnthropicConfig,
  type GeminiConfig,
  type BedrockConfig,
  type ProviderWebSearchConfig,
  type ProviderCodeExecutionConfig,
  type ProviderCodeExecutionSkill,
} from "./providers/core/types";

export type {
  ApiSurface,
  AttachmentPolicy,
  CapabilityControlSetting,
  CapabilityReport,
  FeatureKey,
  ThreadTarget,
} from "./lib/capability/types";

import type {
  ProviderRuntimeSettings,
  ToolDefinition,
  ChatMessage as _ChatMessage,
  ProviderId,
  Role,
  ProviderWebSearchConfig,
  ProviderCodeExecutionConfig,
} from "./providers/core/types";
import type {
  AttachmentPolicy,
  CapabilityControlSetting,
  FeatureKey,
} from "./lib/capability/types";

export interface ToolContext {
  projectId?: string;
  accessToken?: string;
  // Map of relative path -> file content
  fileSystem?: Record<string, string>;
}

export interface ExecutableTool {
  definition: ToolDefinition;
  execute: (args: any, context: ToolContext) => Promise<string>;
}

export interface DatabaseQueryPolicy {
  maxExecutingGlobal: number;
  maxExecutingPerThread: number;
  maxExecutingPerConnection: number;
  inlineWaitMilliseconds: number;
  maxPollIntervalMilliseconds: number;
  monitorErrorBackoffMilliseconds: number;
  softQueueTimeoutMilliseconds: number;
  softExecutionTimeoutMilliseconds: number;
  hardExecutionTimeoutMilliseconds: number;
  maxResultRows: number;
  maxMaterializedBytes: number;
  bigQueryMaximumBytesBilled: number;
  maxAutoContinuations: number;
}

export interface AppSettings extends ProviderRuntimeSettings {
  featureFlags?: FeatureFlags;
  dataSources?: DataSourcesConfig;
  connections?: ConnectionsSettings;
  threadSearch?: ThreadSearchSettings;
  localComputer?: LocalComputerSettings;
  atlassian?: AtlassianSettings;
  browserControl?: BrowserControlSettings;
  sharepoint?: SharePointSettings;
  github?: GitHubSettings;
  slack?: SlackSettings;
  arxiv?: ArxivSettings;
  imageGeneration?: ImageGenerationSettings;
  cloudCredentials?: CloudCredentials;
  providerConfigs?: ProviderConfig[];
  providerTemplates?: ProviderTemplate[];
  activeProviderConfigId?: string | null;
  systemPrompt: string;
  googleProjectId?: string;
  googleAccessToken?: string;
  /**
   * Optional custom path to an ADC credentials JSON file (inside the runtime environment).
   * When set, the Go backend will prefer this over auto-detected ADC locations.
   */
  googleCredentialsPath?: string;
  /** Databricks workspace host URL (e.g. https://1234567890.7.gcp.databricks.com) */
  databricksHost?: string;
  /** Databricks SQL Warehouse ID */
  databricksWarehouseId?: string;
  /** Optional OAuth client ID for PKCE flow (defaults to databricks-cli) */
  databricksOAuthClientId?: string;
  /** Optional GitHub OAuth App client ID for local device login */
  githubOAuthClientId?: string;
  /** Optional Slack OAuth App client ID for local PKCE login */
  slackOAuthClientId?: string;
  /** Trino coordinator host URL (e.g. https://trino.example.com) */
  trinoHost?: string;
  /** Trino catalog name */
  trinoCatalog?: string;
  /** Trino schema name */
  trinoSchema?: string;
  /** Trino user name */
  trinoUser?: string;
  /** Optional Trino source header value */
  trinoSource?: string;
  /** Snowflake account identifier or host (e.g. xy12345.us-east-1) */
  snowflakeAccountIdentifier?: string;
  /** Snowflake warehouse */
  snowflakeWarehouse?: string;
  /** Snowflake database */
  snowflakeDatabase?: string;
  /** Snowflake schema */
  snowflakeSchema?: string;
  /** Optional Snowflake role */
  snowflakeRole?: string;
  /** Snowflake user */
  snowflakeUser?: string;
  /**
   * Safety limit for how many agent/tool loop iterations are allowed per user message.
   * Higher values allow deeper tool-driven tasks, but can increase cost/time.
   */
  maxToolIterations?: number;
  /**
   * Maximum rows returned per page for remote DB query tools.
   * Default is 2500 rows.
   */
  databaseQueryRowLimit?: number;
  /** Durable limits and continuation policy for long-running database work. */
  databaseQueryPolicy?: DatabaseQueryPolicy;
  /**
   * When enabled, the app may log non-sensitive debug info.
   * Never log raw API keys.
   */
  debug?: boolean;
}

export type ConnectionCatalogId =
  | "thread-search"
  | "image-generation"
  | "web-search"
  | "python-sandbox"
  | "local-computer"
  | "browser"
  | "github"
  | "slack"
  | "atlassian"
  | "sharepoint"
  | "bigquery"
  | "databricks"
  | "trino"
  | "snowflake"
  | "arxiv";

export interface ConnectionsSettings {
  visibleIds?: ConnectionCatalogId[];
}

export interface ThreadSearchSettings {
  enabled?: boolean;
}

export type LocalComputerAccessMode = "whole-computer" | "selected-folders";

export interface LocalComputerSettings {
  enabled?: boolean;
  accessMode?: LocalComputerAccessMode;
  selectedFolders?: string[];
  lastCheckedAt?: number;
  lastCheckOk?: boolean;
  lastCheckMessage?: string | null;
}

export interface DataSourceState {
  enabled?: boolean;
  lastCheckedAt?: number;
  lastCheckOk?: boolean;
  lastCheckMessage?: string | null;
  /**
   * Fingerprint of the setup-relevant config that was in effect when the last
   * successful check ran (see lib/connectionConfig.ts). Missing means the
   * connection predates fingerprinting and stays trusted (grandfathered).
   */
  lastVerifiedFingerprint?: string | null;
  lastProjectId?: string | null;
  lastWarehouseId?: string | null;
  lastHost?: string | null;
  /**
   * BigQuery only: estimated scans at or below this many bytes run without
   * asking. Missing = the 100 GiB default (lib/scanBudget).
   */
  automaticScanBytes?: number;
  /**
   * BigQuery only: hard scan ceiling in bytes. Estimates above the automatic
   * limit pause for the user's one-time approval; estimates above this
   * ceiling are blocked without asking. Missing = no ceiling (every
   * oversized estimate still asks first). Persisted values always satisfy
   * approvedScanBytes >= automaticScanBytes.
   */
  approvedScanBytes?: number;
}

export interface DataSourcesConfig {
  bigquery?: DataSourceState;
  databricks?: DataSourceState;
  trino?: DataSourceState;
  snowflake?: DataSourceState;
}

export interface AtlassianProductSettings {
  enabled?: boolean;
  showBrowser?: boolean;
  lastCheckedAt?: number;
  lastCheckOk?: boolean;
  lastCheckMessage?: string | null;
}

export interface AtlassianSettings {
  jiraBaseUrl?: string;
  confluenceBaseUrl?: string;
  defaultJiraProjectKey?: string;
  allowedProjects?: string[];
  allowedAuthDomains?: string[];
  browserProvider?: "bundled" | string;
  browserChannel?: string;
  browserExecutablePath?: string;
  browserProfileDir?: string;
  /** See DataSourceState.lastVerifiedFingerprint. Missing = grandfathered valid. */
  lastVerifiedFingerprint?: string | null;
  jira?: AtlassianProductSettings;
  confluence?: AtlassianProductSettings;
}

export interface BrowserControlSettings {
  enabled?: boolean;
  showBrowser?: boolean;
  browserProfileDir?: string;
}

export interface SharePointSettings {
  siteUrl?: string;
  enabled?: boolean;
  showBrowser?: boolean;
  browserProfileDir?: string;
  authMode?: "browser" | "graph";
  lastCheckedAt?: number;
  lastCheckOk?: boolean;
  lastCheckMessage?: string | null;
  /** See DataSourceState.lastVerifiedFingerprint. Missing = grandfathered valid. */
  lastVerifiedFingerprint?: string | null;
}

export interface GitHubSettings {
  enabled?: boolean;
  readTools?: boolean;
  allowedRepositories?: string[];
  lastCheckedAt?: number;
  lastCheckOk?: boolean;
  lastCheckMessage?: string | null;
  /** See DataSourceState.lastVerifiedFingerprint. Missing = grandfathered valid. */
  lastVerifiedFingerprint?: string | null;
}

export interface SlackSettings {
  enabled?: boolean;
  readTools?: boolean;
  writeTools?: boolean;
  writeIdentity?: "user" | "bot";
  allowedTeamIds?: string[];
  allowedChannelIds?: string[];
  lastCheckedAt?: number;
  lastCheckOk?: boolean;
  lastCheckMessage?: string | null;
  /** See DataSourceState.lastVerifiedFingerprint. Missing = grandfathered valid. */
  lastVerifiedFingerprint?: string | null;
}

export interface ArxivSettings {
  enabled?: boolean;
  /** Optional courtesy contact email included in the arXiv API User-Agent. */
  contactEmail?: string;
  lastCheckedAt?: number;
  lastCheckOk?: boolean;
  lastCheckMessage?: string | null;
}

export type ImageGenerationQuality = "auto" | "low" | "medium" | "high";
export type ImageGenerationFormat = "png" | "jpeg" | "webp";

export interface ImageGenerationDefaults {
  /**
   * "auto" or an explicit "WIDTHxHEIGHT". Unknown-but-valid strings are
   * tolerated so future model sizes survive a round-trip.
   */
  size?: string;
  /** One of ImageGenerationQuality; unknown-but-valid strings tolerated. */
  quality?: ImageGenerationQuality | string;
  /** One of ImageGenerationFormat; unknown-but-valid strings tolerated. */
  format?: ImageGenerationFormat | string;
}

export interface ImageGenerationSettings {
  enabled?: boolean;
  /**
   * References an existing OpenAI-family ProviderConfig by id and reuses its
   * secret. May dangle if that config was deleted; the UI surfaces that state.
   */
  providerId?: string | null;
  /** Explicit image model, stored rather than hidden in code (default "gpt-image-2"). */
  model?: string;
  defaults?: ImageGenerationDefaults;
  /** Maximum images produced per run; clamped to an integer in [1, 4]. */
  maxImagesPerRun?: number;
}

export type ProviderCardType = ProviderId | "bedrock";

export interface FeatureFlags {
  activation: boolean;
  providers: {
    openai: boolean;
    anthropic: boolean;
    gemini: boolean;
    bedrock: boolean;
    compatible: boolean;
  };
  providerTemplates: boolean;
  cloudCredentialsAws: boolean;
  cloudCredentialsTrino: boolean;
  cloudCredentialsSnowflake: boolean;
  chartDimensionFilters: boolean;
  chartEngines: {
    echarts: boolean;
    highcharts: boolean;
  };
  capabilityUx: {
    report: boolean;
    editorV2: boolean;
    controls: boolean;
    preflight: boolean;
    composerTarget: boolean;
  };
}

export type ConnectionTestStatus = "success" | "failure";
export type TrinoAuthMode = "basic" | "jwt" | "none";
export type SnowflakeAuthMode = "oauth_token" | "keypair_jwt" | "none";

export interface AwsCloudCredential {
  id: string;
  displayName: string;
  region: string;
  authMode: "access_key" | "profile" | "role";
  accessKeyId?: string | null;
  secretAccessKeySet?: boolean;
  sessionTokenSet?: boolean;
  profileName?: string | null;
  roleArn?: string | null;
  lastTestedAt?: number | null;
  lastTestStatus?: ConnectionTestStatus | null;
  lastTestMessage?: string | null;
}

export interface GcpCloudCredential {
  id: string;
  displayName: string;
  projectId?: string | null;
  adcMode: "auto" | "path";
  credentialsPath?: string | null;
  lastTestedAt?: number | null;
  lastTestStatus?: ConnectionTestStatus | null;
  lastTestMessage?: string | null;
}

export interface CloudCredentials {
  aws: AwsCloudCredential[];
  gcp: GcpCloudCredential[];
}

export interface ProviderConfig {
  id: string;
  displayName: string;
  providerId: ProviderCardType;
  model: string;
  apiType?: "chat_completions" | "responses" | "messages";
  /** Canonical runtime surface. Legacy configs derive this during migration. */
  apiSurface?: string;
  baseUrl?: string | null;
  authType?: "api_key" | "cloud_credentials" | "none";
  authHeader?: string | null;
  authScheme?: string | null;
  cloudCredentialRefs?: {
    awsCredentialId?: string;
    gcpCredentialId?: string;
  };
  region?: string | null;
  anthropicVersion?: string | null;
  compactionEnabled?: boolean;
  compactionInstruction?: string | null;
  compactionTriggerTokens?: number | null;
  endpointSuffix?: string | null;
  timeoutMs?: number | null;
  reasoningEffort?: string | null;
  modelInPath?: boolean;
  modelInBody?: boolean;
  fileInputMode?:
    "sidecar_only" | "native_pdf_only" | "native_supported" | null;
  includeStreamUsage?: boolean | null;
  usageSemantics?: "generic" | "openai" | null;
  extraHeaders?: Array<{
    id: string;
    key: string;
    value: string;
    isSecret?: boolean;
  }>;
  queryParams?: Record<string, string> | null;
  payloadOverrides?: Record<string, unknown> | null;
  webSearch?: ProviderWebSearchConfig | null;
  codeExecution?: ProviderCodeExecutionConfig | null;
  /** Server-registry feature preferences. Keys are intentionally open-ended. */
  capabilityControls?: Partial<Record<FeatureKey, CapabilityControlSetting>>;
  /** Explicit, surface-scoped attachment handling consent. */
  attachmentPolicies?: AttachmentPolicy[];
  /** Backend-computed target/config fingerprint used to bind evidence. */
  configFingerprint?: string;
  isDefault?: boolean;
  createdAt?: number;
  updatedAt?: number;
  lastTestedAt?: number | null;
  lastTestStatus?: ConnectionTestStatus | null;
  lastTestMessage?: string | null;
  lastTestEndpoint?: string | null;
}

export interface ProviderTemplate {
  id: string;
  name: string;
  provider: Omit<ProviderConfig, "id" | "displayName">;
}

export interface SavedConfig {
  id: string;
  name: string;
  settings: AppSettings;
  updatedAt: number;
}

export interface ChatImageAttachment {
  artifactId?: string;
  fileName: string;
  mimeType: string;
  sizeBytes: number;
  dataUri: string;
  width?: number;
  height?: number;
  source?: "paste" | "drop" | "upload";
}

export type EvaluatedFileCapabilities =
  string[] | Record<string, boolean | string | number | null | undefined>;

export interface EvaluatedFileMetadata {
  fileKind?: string;
  normalizedMimeType?: string;
  declaredMimeType?: string;
  extensionMimeType?: string;
  sniffedMimeType?: string;
  sha256?: string;
  providerFileId?: string;
  providerFileType?: string;
  capabilities?: EvaluatedFileCapabilities;
}

export type ChatFileAttachmentStatus = "uploading" | "uploaded" | "error";

export interface ChatFileAttachment {
  artifactId: string;
  fileName: string;
  /**
   * UI upload lifecycle. Absent means "uploaded" (persisted attachments and
   * older payloads never carry these fields).
   */
  status?: ChatFileAttachmentStatus;
  /** Stable composer key before an artifactId exists; also the retry handle. */
  localId?: string;
  /** Upload progress in [0, 1] while status is "uploading". */
  progress?: number;
  errorMessage?: string;
  /** Object URL while pending, or a trusted non-API preview URL. */
  thumbnailUrl?: string;
  /** Same-origin API preview URL fetched through authenticated fetch before rendering. */
  thumbnailFetchUrl?: string;
  /** Raw artifact id used to fetch image previews through authenticated fetch. */
  thumbnailArtifactId?: string;
  /** Same-origin raw PDF URL used for inline PDF previews. */
  pdfPreviewUrl?: string;
  /** Same-origin raw HTML URL used for sandboxed HTML previews. */
  htmlPreviewUrl?: string;
  /** Inline HTML source used for sandboxed generated/file-like HTML previews. */
  htmlPreviewContent?: string;
  /** Same-origin extracted text preview URL, commonly a sheet preview sidecar. */
  textPreviewFetchUrl?: string;
  mimeType?: string;
  detectedMimeType?: string;
  fileKind?: string;
  normalizedMimeType?: string;
  declaredMimeType?: string;
  extensionMimeType?: string;
  sniffedMimeType?: string;
  sha256?: string;
  providerFileId?: string;
  providerFileType?: string;
  capabilities?: EvaluatedFileCapabilities;
  sizeBytes?: number;
  isText?: boolean;
  previewAvailable?: boolean;
  previewTruncated?: boolean;
  previewText?: string;
  contentNote?: string;
  language?: string;
  source?:
    | "chat_upload"
    | "external_tool"
    | "external_api"
    | "upload"
    | "paste"
    | "drop";
  /**
   * Send-path fidelity marker from an accepted capability-plan choice
   * (▽ Reduced fidelity). Describes how the file was SENT for one run —
   * the stored file itself is unchanged. UI-only.
   */
  sendFidelity?: "reduced";
}

export type MessageBlockReferenceKind =
  "user_message" | "assistant_message" | "tool_call" | "tool_result";

export interface MessageBlockReference {
  /**
   * Stable message-block reference id used in UI and run payloads.
   */
  id: string;
  /**
   * Deterministic UI key for rendering and chip identity.
   */
  key: string;
  kind: MessageBlockReferenceKind;
  /**
   * Optional persisted backend message id when available.
   */
  messageId?: string;
  /**
   * Optional persisted per-thread sequence number when available.
   */
  messageSeq?: number;
  role: Role;
  /**
   * Human-readable title/label shown in reference chips.
   */
  title: string;
  label?: string;
  /**
   * Compact one-line preview.
   */
  preview: string;
  /**
   * Snapshot of referenced block content used for hidden context.
   */
  contentSnapshot: string;
}

export interface ThreadReference {
  id: string;
  title: string;
  summary?: string;
  updatedAt?: number;
  status?: string;
}

export interface CompactionStats {
  tokensBefore?: number;
  tokensAfter?: number;
  tokenBudget?: number;
  compacted?: boolean;
  phase?: string;
  status?: string;
}

/** One line of a run's capability plan summary (UI runtime state; plan §4.6). */
export interface UICapabilityPlanItem {
  kind: "blocker" | "choice" | "notice" | "update";
  code?: string;
  text: string;
  attachmentId?: string | null;
}

/**
 * UI-side summary of a run's preflight/finalized capability plan. Enough to
 * render the thread-differential RunPlanRow live; the finalized plan is
 * fetched on demand (GET /api/runs/{runId}/plan) when the row is expanded.
 */
export interface UICapabilityPlanSummary {
  planId?: string;
  runId?: string;
  /** "family · surface · model" identity of the target the run used. */
  targetLabel?: string;
  targetChanged?: boolean;
  items: UICapabilityPlanItem[];
}

export interface UIMessage extends _ChatMessage {
  id?: string;
  seq?: number;
  /** Persisted run ownership, used to restore run-plan rows in transcript order. */
  run_id?: string;
  /**
   * UI-only stable id used for React keying before persisted message ids/sequences exist.
   */
  _uiStableId?: string;
  /**
   * UI-only flag for in-flight assistant streaming.
   */
  _uiStreaming?: boolean;
  /**
   * UI-only rendering hint. Defaults to "normal".
   * - "steer": a steering message intended to modify an in-flight assistant turn.
   * - "capability_plan": a first-class run-plan row (never assistant prose);
   *   role is System so legacy transcript paths skip it. Rendered from
   *   `capabilityPlan`; UI-only, never persisted.
   */
  uiKind?: "normal" | "steer" | "capability_plan";
  /** Present when uiKind === "capability_plan". */
  capabilityPlan?: UICapabilityPlanSummary;
  thinking?: string;
  thinkingComplete?: boolean;
  /**
   * Context compaction state. Set when the Anthropic compact beta triggers mid-stream.
   * These are UI-only and should never be persisted to backend messages.
   */
  compactionSummary?: string;
  compactionInProgress?: boolean;
  compactionComplete?: boolean;
  compactionStats?: CompactionStats;
  /**
   * Streaming markdown segmentation snapshots used for stable-prefix rendering.
   * These are UI-only and should never be persisted to backend messages.
   */
  streamStablePrefix?: string;
  streamUnstableSuffix?: string;
  streamInCodeBlock?: boolean;
  streamParserDegraded?: boolean;
  /**
   * Artifact UUIDs referenced by the user when sending this message.
   * Used for UI chips; not sent to providers.
   */
  referencedArtifactIds?: string[];
  referenced_artifact_ids?: string[];
  /**
   * Message block ids referenced by the user when sending this message.
   * Used for UI chips/context stitching; not sent to providers directly.
   */
  referencedMessageIds?: string[];
  referenced_message_ids?: string[];
  /**
   * Other threads selected as read-only context for this message.
   * The backend expands these into retrieval-grounded hidden context.
   */
  referencedThreadIds?: string[];
  referenced_thread_ids?: string[];
  referencedThreads?: ThreadReference[];
  referenced_threads?: ThreadReference[];
  /**
   * Extra context appended to the user message for the model/tool loop,
   * but intentionally hidden from the UI message bubble.
   */
  hiddenContext?: string;
  /**
   * Optional inline image attachments for the current user turn.
   * Used by the backend to build provider-specific multimodal content blocks.
   */
  imageAttachments?: ChatImageAttachment[];
  /**
   * Thread-scoped uploaded files for the current user turn.
   * These are persisted as backend artifacts and inspected via attachment tools.
   */
  fileAttachments?: ChatFileAttachment[];
}

export interface BigQueryRow {
  [key: string]: string | number | boolean | null;
}

// --- ARTIFACT TYPES ---

export type ArtifactType = "data" | "code" | "image" | "file";

export interface Artifact {
  id: string;
  type: ArtifactType;
  title: string;
  content: string; // JSON string for data, raw string for code
  language?: string; // For code
  metadata?: Record<string, any>; // Extra info like row count, dataset ID, plot_spec
}

export const SANDBOXED_HTML_RENDER_MODE = "sandboxed_html" as const;
export const SANDBOXED_HTML_SANDBOX_POLICY = "allow-scripts" as const;
export const SANDBOXED_HTML_NETWORK_POLICY = "blocked" as const;

export const SANDBOXED_HTML_TOOL_NAMES = [
  "create_interactive_artifact",
  "create_interactive_data_artifact",
] as const;

export type SandboxedHtmlToolName = (typeof SANDBOXED_HTML_TOOL_NAMES)[number];

export interface SandboxedHtmlArtifactMetadata extends Record<string, any> {
  render_mode: typeof SANDBOXED_HTML_RENDER_MODE;
  sandbox: typeof SANDBOXED_HTML_SANDBOX_POLICY;
  network: typeof SANDBOXED_HTML_NETWORK_POLICY;
  tool_name?: SandboxedHtmlToolName | string;
  artifact_kind?: "interactive_html" | "interactive_data_html" | string;
  data_source_artifact_id?: string;
  data_source_title?: string;
  data_source_columns?: string[];
  data_source_row_count?: number;
  data_access?: {
    mode?: "parent_bridge" | string;
    max_page_size?: number;
    [key: string]: unknown;
  };
  byte_size?: number;
  inline_preview?: boolean;
  preview_height?: number;
  full_artifact_id?: string;
  placeholder?: boolean;
  partial?: boolean;
  preview_only?: boolean;
}

export type SandboxedHtmlArtifact = Artifact & {
  metadata: SandboxedHtmlArtifactMetadata;
};

export type EnsureArtifactLoaded = (
  artifactId: string,
) => Promise<Artifact | null>;

function normalizeMetadataToken(value: unknown): string {
  return String(value || "")
    .trim()
    .toLowerCase();
}

export function isSandboxedHtmlToolName(
  value: unknown,
): value is SandboxedHtmlToolName {
  const token = normalizeMetadataToken(value);
  return SANDBOXED_HTML_TOOL_NAMES.some((name) => name === token);
}

export function getSandboxedHtmlArtifactMetadata(
  artifact: Artifact | null | undefined,
): SandboxedHtmlArtifactMetadata | null {
  if (!artifact?.metadata || typeof artifact.metadata !== "object") {
    return null;
  }
  const metadata = artifact.metadata;
  if (
    normalizeMetadataToken(metadata.render_mode) !== SANDBOXED_HTML_RENDER_MODE
  ) {
    return null;
  }
  if (
    normalizeMetadataToken(metadata.sandbox) !== SANDBOXED_HTML_SANDBOX_POLICY
  ) {
    return null;
  }
  if (
    normalizeMetadataToken(metadata.network) !== SANDBOXED_HTML_NETWORK_POLICY
  ) {
    return null;
  }
  return metadata as SandboxedHtmlArtifactMetadata;
}

export function isSandboxedHtmlArtifact(
  artifact: Artifact | null | undefined,
  fallbackToolName?: string,
): artifact is SandboxedHtmlArtifact {
  if (!artifact) return false;
  if (artifact.type !== "code" && artifact.type !== "file") return false;
  const language = normalizeMetadataToken(artifact.language);
  if (language && language !== "html" && language !== "htm") return false;
  const metadata = getSandboxedHtmlArtifactMetadata(artifact);
  if (!metadata) return false;
  if (metadata.inline_preview === false) return false;
  const resultMetadata =
    metadata.result_metadata && typeof metadata.result_metadata === "object"
      ? (metadata.result_metadata as Record<string, unknown>)
      : null;
  const artifactKind = normalizeMetadataToken(metadata.artifact_kind);
  return (
    metadata.inline_preview === true ||
    artifactKind === "interactive_html" ||
    artifactKind === "interactive_data_html" ||
    isSandboxedHtmlToolName(fallbackToolName) ||
    isSandboxedHtmlToolName(metadata.tool_name) ||
    isSandboxedHtmlToolName(resultMetadata?.tool_name)
  );
}

export type HighchartsModule = "core" | "stock" | "maps" | "gantt";
export type HighchartsStage1Module = "core" | "stock";
export type HighchartsPlotSpecSchemaVersion =
  "highcharts-plot-spec.v1" | "highcharts-plot-spec.v2";
export type HighchartsExtensionModule =
  | "highcharts-more"
  | "drilldown"
  | "heatmap"
  | "tilemap"
  | "treemap"
  | "sunburst"
  | "sankey"
  | "organization"
  | "dependency-wheel"
  | "networkgraph"
  | "wordcloud"
  | "venn"
  | "timeline"
  | "xrange"
  | "streamgraph"
  | "item-series"
  | "pareto"
  | "dumbbell"
  | "lollipop"
  | "funnel"
  | "histogram-bellcurve"
  | "geoheatmap"
  | "flowmap"
  | "tiledwebmap";

export interface HighchartsPlotSpecV1 {
  schema_version: "highcharts-plot-spec.v1";
  module: HighchartsStage1Module;
  options: Record<string, any>;
}

export interface HighchartsPlotSpecV2 {
  schema_version: "highcharts-plot-spec.v2";
  module: HighchartsModule;
  modules?: HighchartsExtensionModule[];
  options: Record<string, any>;
  extensions?: {
    drilldown_async?: {
      loader_id: string;
      series_by_id: Record<string, Record<string, any>>;
      max_depth?: number;
    };
  };
}

export type HighchartsPlotSpec = HighchartsPlotSpecV1 | HighchartsPlotSpecV2;

export type EchartsModule = "core" | "dataviz" | "graph" | "all";
export type EchartsPlotSpecSchemaVersion = "echarts-plot-spec.v1";

export interface EchartsPlotSpecV1 {
  schema_version: "echarts-plot-spec.v1";
  module: EchartsModule;
  modules?: string[];
  options: Record<string, any>;
  extensions?: {
    drilldown_async?: {
      loader_id: string;
      series_by_id: Record<string, Record<string, any>>;
      max_depth?: number;
    };
  };
}

export type EchartsPlotSpec = EchartsPlotSpecV1;
export type ChartEngine = "highcharts" | "echarts";
export type AnyChartPlotSpec =
  HighchartsPlotSpec | EchartsPlotSpec | Record<string, any>;
export interface ChartBudgetReport {
  schema_version?: "chart-budget.v1" | string;
  chart_kind?: string;
  chart_engine?: string;
  row_count_total?: number;
  points_requested?: number;
  points_returned?: number;
  series_count?: number;
  plot_spec_bytes?: number;
  rows_preview_bytes?: number;
  result_payload_bytes?: number;
  sse_event_bytes?: number;
  budget_actions?: string[];
  failure_stage?: string;
  failure_code?: string;
  failure_message?: string;
}

// --- QUERY METADATA ---
export type QueryProvider = "bigquery" | "databricks" | "trino" | "snowflake";

export type QueryProfileOutcome =
  | "accepted"
  | "invalid"
  | "over_budget"
  | "timed_out"
  | "errored"
  | "indeterminate";

export interface QueryNativeHandle {
  provider?: string;
  id: string;
  location?: string;
  extra?: Record<string, string>;
}

export interface QueryProfile {
  valid: boolean;
  error?: string;
  estimated_bytes?: number;
  estimated_rows?: number;
  estimate_is_exact?: boolean;
  plan_summary?: string;
  warnings?: string[];
  provider_details?: Record<string, unknown>;
}

export interface QueryMetadata {
  provider: QueryProvider;
  duration_ms: number;
  status?: QueryJobStatus;
  description?: string;
  sql?: string;
  sql_preview?: string;
  total_bytes_processed?: number | string;
  job_id?: string;
  warehouse_job_id?: string;
  provider_job_id?: string;
  provider_job_url?: string;
  job_location?: string;
  location?: string;
  project_id?: string;
  account_identifier?: string;
  statement_id?: string;
  query_id?: string;
  warehouse_id?: string;
  warehouse_name?: string;
  database_name?: string;
  host?: string;
  url?: string;
  catalog_name?: string;
  schema_name?: string;
  role_name?: string;
  user_name?: string;
  source_name?: string;
  progress_percent?: number;
  progress_pct?: number;
  monitor_health?: QueryMonitorHealth;
  policy_health?: QueryPolicyHealth;
  materialization_state?: QueryMaterializationState;
  validation_state?: QueryValidationState;
  validation_detail?: string;
  execution_status?: QueryJobStatus;
  remote_submitted?: boolean;
  submit_dispatched?: boolean;
  provider_submission_confirmed?: boolean;
  submission_uncertain?: boolean;
  profile_outcome?: QueryProfileOutcome;
  query_profile?: QueryProfile;
  profile?: QueryProfile;
  profile_handle?: QueryNativeHandle;
  profile_phase?: string;
  profile_dispatched?: boolean;
  profile_pending?: boolean;
  rewrite_required?: boolean;
  replacement_required?: boolean;
  approval_required?: boolean;
  /** Opaque run-scoped retry handle (qob- plus 32 lowercase hex characters). */
  query_obligation_id?: string;
  cancel_reason?: string;
  cancel_outcome?: string;
  queued_elapsed_ms?: number;
  execution_elapsed_ms?: number;
  telemetry_age_ms?: number;
  bytes_processed?: number;
  result_truncated?: boolean;
  owner_token?: string;
  thread_id?: string;
  run_id?: string;
  cache_hit?: boolean;
  schema?: Array<{
    name: string;
    type?: string;
    mode?: string;
    description?: string;
  }>;
  requested_limit?: number;
  effective_limit?: number;
  configured_limit?: number;
  limit_capped?: boolean;
  rows_returned?: number;
  rows_processed?: number;
  row_count?: number;
  page_offset?: number;
  page_size?: number;
  offset?: number;
  has_more?: boolean;
  next_page_token?: string;
}

export type ToolProgressPhase =
  | "profiling"
  | "submitted"
  | "polling"
  | "complete"
  | "browser_starting"
  | "browser_ready"
  | "browser_navigating"
  | "browser_searching"
  | "browser_reading"
  | "approval_required"
  | "loading_source_metadata"
  | "source_loaded"
  | "binding_data_source"
  | "validating_html"
  | "preparing_artifact"
  | "artifact_ready"
  | "validation_failed"
  | "arxiv_metadata_lookup"
  | "arxiv_pdf_downloading"
  | "arxiv_pdf_downloaded"
  | "arxiv_pdf_extracting"
  | "arxiv_pdf_page_reading"
  | "arxiv_text_window_reading"
  | "arxiv_paper_saved"
  | "arxiv_cache_hit"
  | "local_search_agent_started"
  | "local_search_agent_listing"
  | "local_search_agent_searching"
  | "local_search_agent_reading"
  | "local_search_agent_ranking"
  | "local_search_agent_completed"
  | "local_search_agent_failed"
  | "requesting"
  | "generating"
  | "retrying"
  | "saving"
  | "failed";

// --- TOOL PROGRESS (SSE) ---
export interface ToolProgressEvent {
  tool_call_id: string;
  tool_name: string;
  provider: QueryProvider | string;
  integration?: string;
  run_id?: string;
  thread_id?: string;
  // 'complete' is the terminal phase and should be emitted once a tool run finishes successfully.
  phase: ToolProgressPhase;
  label?: string;
  message?: string;
  percent?: number;
  /** Percent under its short key — the local search agent emits `pct`. */
  pct?: number;
  /** Local search agent grant/file context (job_id is shared with query jobs below). */
  grant_id?: string;
  file_ref?: string;
  status?: string;
  url?: string;
  state?: string;
  adapter_family?: string;
  query?: string;
  results_count?: number;
  result_preview?: Array<Record<string, unknown>>;
  provider_executed?: boolean;
  arxiv_id?: string;
  paper_key?: string;
  title?: string;
  page_current?: number;
  page_count?: number;
  pages_read?: number;
  /** Reserved: model-authored reading intent (LLM judgment). Not yet emitted
      by the backend; the card displays it only when present. */
  purpose?: string;
  /** Reserved: model-authored intent tag such as skim/targeted/verify/scan
      (LLM judgment). Not yet emitted by the backend. */
  purpose_tag?: string;
  /** Reserved: why the model opened the paper — explicit | screening | scan
      (LLM judgment). A scan motive switches the read card to the notes
      register (observed/noticed language, no findings layer). Not yet
      emitted by the backend; never inferred when absent. */
  motive?: string;
  /** Reserved: model-authored takeaway from a read (LLM judgment). Not yet
      emitted by the backend. */
  takeaway?: string;
  /** Reserved: one-line "what is this paper" gloss, emitted once at first
      read and reused on every paper-level view (LLM judgment). Not yet
      emitted by the backend. */
  paper_gloss?: string;
  /** Reserved: cache-hit marker for paper reads. Not yet emitted; the
      deterministic signal today is arxiv_download_paper's already_existed. */
  cached?: boolean;
  bytes_read?: number;
  bytes_total?: number;
  extract_state?: string;
  extract_quality?: string;
  source_kind?: string;
  pdf?: {
    state?: string;
    page_current?: number;
    page_count?: number;
    bytes_read?: number;
    bytes_total?: number;
  };
  html_bytes?: number;
  byte_size?: number;
  source_artifact_id?: string;
  column_count?: number;
  max_page_size?: number;
  rows_processed?: number;
  rows_returned?: number;
  row_count?: number;
  job_id?: string;
  /** Stable run-scoped identity shared by every attempt for one query obligation. */
  query_obligation_id?: string;
  owner_token?: string;
  remote_submitted?: boolean;
  submit_dispatched?: boolean;
  provider_submission_confirmed?: boolean;
  submission_uncertain?: boolean;
  profile_outcome?: QueryProfileOutcome;
  profile_phase?: string;
  profile_dispatched?: boolean;
  rewrite_required?: boolean;
  replacement_required?: boolean;
  approval_required?: boolean;
  location?: string;
  project_id?: string;
  account_identifier?: string;
  statement_id?: string;
  query_id?: string;
  warehouse_id?: string;
  warehouse_name?: string;
  database_name?: string;
  host?: string;
  catalog_name?: string;
  schema_name?: string;
  role_name?: string;
  user_name?: string;
  source_name?: string;
  bytes_processed?: string;
  timestamp: number;
}

// --- ASYNC QUERY JOB TYPES ---

export type QueryJobStatus =
  | "reserved"
  | "profiling"
  | "submission_unknown"
  | "submitted"
  | "running"
  | "cancel_requested"
  | "cancelling"
  | "completed"
  | "failed"
  | "cancelled"
  | "expired"
  | "orphaned"
  | "unknown";

export type QueryMonitorHealth = "ok" | "degraded" | "lost";
export type QueryPolicyHealth =
  | "ok"
  | "soft_queue_exceeded"
  | "soft_execution_exceeded"
  | "suspected_stalled"
  | "hard_exceeded";
export type QueryMaterializationState =
  | "none"
  | "partial"
  | "complete"
  | "truncated"
  | "expired";
export type QueryValidationState =
  | "unvalidated"
  | "validating"
  | "validated"
  | "rejected"
  | "retrieved_unvalidated";

/** Returned by run_bigquery_query / run_databricks_query / run_trino_query / run_snowflake_query when the query takes >2s. */
export interface AsyncQueryHandle {
  status: QueryJobStatus;
  job_id: string;
  /** Opaque run-scoped retry handle (qob- plus 32 lowercase hex characters). */
  query_obligation_id?: string;
  provider: QueryProvider;
  sql_preview?: string;
  owner_token?: string;
  thread_id?: string;
  run_id?: string;
  _uiStartedAt?: number;
  message?: string;
  remote_submitted?: boolean;
  submit_dispatched?: boolean;
  provider_submission_confirmed?: boolean;
  submission_uncertain?: boolean;
  profile_outcome?: QueryProfileOutcome;
  query_profile?: QueryProfile;
  profile?: QueryProfile;
  profile_handle?: QueryNativeHandle;
  profile_phase?: string;
  profile_dispatched?: boolean;
}

/** Returned by check_query_status tool. */
export interface QueryStatusResult {
  status: QueryJobStatus;
  owner_token?: string;
  job_id?: string;
  /** Opaque run-scoped retry handle (qob- plus 32 lowercase hex characters). */
  query_obligation_id?: string;
  provider?: QueryProvider;
  elapsed_ms?: number;
  message?: string;
  remote_submitted?: boolean;
  submit_dispatched?: boolean;
  provider_submission_confirmed?: boolean;
  submission_uncertain?: boolean;
  profile_outcome?: QueryProfileOutcome;
  query_profile?: QueryProfile;
  profile?: QueryProfile;
  profile_handle?: QueryNativeHandle;
  profile_phase?: string;
  profile_dispatched?: boolean;
}

/** Returned by GET /api/queries/{jobId}/status (frontend polling). */
export interface QueryJobStatusResponse {
  job_id: string;
  /** Requesting run whose dependency ledger verified this status identity. */
  run_id?: string;
  status: QueryJobStatus;
  elapsed_ms: number;
  provider: QueryProvider;
  provider_job_id?: string;
  provider_job_url?: string;
  warehouse_job_id?: string;
  statement_id?: string;
  project_id?: string;
  warehouse_id?: string;
  account_identifier?: string;
  query_id?: string;
  job_location?: string;
  location?: string;
  host?: string;
  url?: string;
  progress_percent?: number;
  progress_pct?: number;
  monitor_health?: QueryMonitorHealth;
  policy_health?: QueryPolicyHealth;
  materialization_state?: QueryMaterializationState;
  validation_state?: QueryValidationState;
  validation_detail?: string;
  cancel_reason?: string;
  cancel_outcome?: string;
  queued_elapsed_ms?: number;
  execution_elapsed_ms?: number;
  telemetry_age_ms?: number;
  bytes_processed?: number;
  bytes_billed?: number;
  rows_processed?: number;
  slots_millis?: number;
  soft_queue_budget_ms?: number;
  soft_execution_budget_ms?: number;
  hard_execution_budget_ms?: number;
  max_scan_bytes?: number;
  result_rows?: number;
  result_bytes?: number;
  result_truncated?: boolean;
  /** Opaque run-scoped retry handle (qob- plus 32 lowercase hex characters). */
  query_obligation_id?: string;
  execution_status?: QueryJobStatus;
  remote_submitted?: boolean;
  submit_dispatched?: boolean;
  provider_submission_confirmed?: boolean;
  submission_uncertain?: boolean;
  profile_outcome?: QueryProfileOutcome;
  query_profile?: QueryProfile;
  profile?: QueryProfile;
  profile_handle?: QueryNativeHandle;
  profile_phase?: string;
  profile_dispatched?: boolean;
  error?: string;
}

/** Tracked query for the query-monitor tray. */
export interface TrackedQuery {
  artifactId: string;
  jobId: string;
  provider: QueryProvider;
  description?: string;
  sql?: string;
  sqlPreview?: string;
  ownerToken?: string;
  threadId?: string;
  runId?: string;
  queryObligationId?: string;
  status: QueryJobStatus;
  startedAtMs: number;
  completedAtMs?: number;
  durationMs?: number;
  rowsReturned?: number;
  queryMetadata?: QueryMetadata;
}

// --- TOOL RESULT ENVELOPE ---
// Tool result messages (role=tool) are wrapped so the model and UI can refer to a stable artifact UUID.
export interface ToolResultEnvelope {
  artifact_id: string;
  tool_name?: string;
  tool_call_id?: string;
  ok: boolean;
  result_format: "json" | "text";
  result: any;
  query_metadata?: QueryMetadata;
  tool_args?: Record<string, any>;
  result_metadata?: Record<string, any>;
  tool_presentation?: Record<string, any>;
  plot_spec?: AnyChartPlotSpec;
  chart_budget?: ChartBudgetReport;
  chart_engine?: ChartEngine | string;
  chart_spec_version?: string;
  chart_module?: HighchartsModule | string;
  chart_modules?: string[];
  preview_only?: boolean;
  full_artifact_id?: string;
  row_count_total?: number;
  downsample_ratio?: number;
  /** When true, the frontend should not render this tool call/result in the message stream. */
  ui_suppress?: boolean;
}
