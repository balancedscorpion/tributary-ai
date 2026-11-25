export enum Role {
  System = 'system',
  User = 'user',
  Assistant = 'assistant',
  Tool = 'tool',
}

export interface ToolCall {
  id: string;
  name: string; // canonical tool name
  args: Record<string, unknown>;
  /**
   * Optional raw JSON string for arguments (useful while streaming partial deltas).
   * When present, `args` should represent the best-effort parsed form.
   */
  argsText?: string;
  provider_executed?: boolean;
}

export interface ChatMessage {
  role: Role;
  content: string | null;
  tool_calls?: ToolCall[];
  tool_call_id?: string; // for tool result messages
  name?: string; // tool name for tool result messages
  fileAttachments?: ChatFileAttachment[];
  file_attachments?: ChatFileAttachment[];
}

export type EvaluatedFileCapabilities =
  | string[]
  | Record<string, boolean | string | number | null | undefined>;

export interface ChatFileAttachment {
  artifactId?: string;
  artifact_id?: string;
  fileName?: string;
  file_name?: string;
  mimeType?: string;
  mime_type?: string;
  detectedMimeType?: string;
  detected_mime_type?: string;
  normalizedMimeType?: string;
  normalized_mime_type?: string;
  declaredMimeType?: string;
  declared_mime_type?: string;
  extensionMimeType?: string;
  extension_mime_type?: string;
  sniffedMimeType?: string;
  sniffed_mime_type?: string;
  fileKind?: string;
  file_kind?: string;
  sha256?: string;
  inlineDataBase64?: string;
  inline_data_base64?: string;
  providerFileId?: string;
  provider_file_id?: string;
  providerFileType?: string;
  provider_file_type?: string;
  previewText?: string;
  preview_text?: string;
  contentNote?: string;
  content_note?: string;
  sizeBytes?: number;
  size_bytes?: number;
  isText?: boolean;
  is_text?: boolean;
  previewAvailable?: boolean;
  preview_available?: boolean;
  previewTruncated?: boolean;
  preview_truncated?: boolean;
  source?: string;
  capabilities?: EvaluatedFileCapabilities;
}

export interface ToolDefinition {
  type: 'function';
  function: {
    name: string;
    description?: string;
    parameters: Record<string, any>;
  };
}

export type ProviderId = 'openai' | 'anthropic' | 'gemini' | 'compatible' | 'bedrock';

export type CompatibleApiType = 'chat_completions' | 'responses';

export interface ProviderSelection {
  providerId: ProviderId;
  model: string;
  /**
   * Only used for `providerId: "compatible"`.
   */
  profileId?: string | null;
}

export interface ProviderAuth {
  type: 'none' | 'api_key';
  header: string; // e.g. Authorization, x-api-key
  scheme?: string | null; // e.g. Bearer, null for raw
}

export interface HeaderKV {
  id: string;
  key: string;
  value: string;
  isSecret?: boolean;
}

export interface CompatibleProfile {
  id: string;
  displayName: string;
  apiType: CompatibleApiType;
  baseUrl: string; // typically ends with /v1

  auth: ProviderAuth;
  apiKey?: string | null;
  extraHeaders: HeaderKV[];

  queryParams?: Record<string, string> | null;
  payloadOverrides?: Record<string, unknown> | null;
  timeoutMs?: number | null;

  /**
   * OpenAI-compatible "reasoning effort" control.
   * For chat_completions this is sent as `reasoning_effort`.
   * For responses this is sent as `reasoning: { effort }`.
   */
  reasoningEffort?: string | null;

  modelInPath?: boolean | null;
  modelInBody?: boolean | null;
  endpointSuffix?: string | null;
  fileInputMode?: "sidecar_only" | "native_pdf_only" | "native_supported" | null;
  includeStreamUsage?: boolean | null;
  usageSemantics?: "generic" | "openai" | null;
  webSearch?: ProviderWebSearchConfig | null;
  codeExecution?: ProviderCodeExecutionConfig | null;

  models?: Array<{
    id: string;
    label?: string | null;
    description?: string | null;
    recommended?: boolean | null;
  }>;
}

export interface ProviderWebSearchConfig {
  enabled?: boolean | null;
  toolType?: string | null;
  maxUses?: number | null;
  searchContextSize?: string | null;
  allowedDomains?: string[] | null;
  blockedDomains?: string[] | null;
  allowedCallers?: string[] | null;
}

export interface ProviderCodeExecutionSkill {
  type: string;
  skillId: string;
  version?: string | null;
}

export interface ProviderCodeExecutionConfig {
  enabled?: boolean | null;
  toolType?: string | null;
  containerMode?: 'auto' | 'existing' | null;
  containerId?: string | null;
  memoryLimit?: string | null;
  skills?: ProviderCodeExecutionSkill[] | null;
}

export interface OpenAIConfig {
  apiKey: string;
  baseUrl?: string | null; // default: https://api.openai.com/v1
  /**
   * Enables OpenAI Responses server-side compaction.
   * Default: true
   */
  compactionEnabled?: boolean | null;
  /**
   * Server-side compaction threshold in rendered input tokens.
   * Default: 200000
   */
  compactionTriggerTokens?: number | null;
  webSearch?: ProviderWebSearchConfig | null;
  codeExecution?: ProviderCodeExecutionConfig | null;
}

export interface AnthropicConfig {
  apiKey: string;
  baseUrl?: string | null; // default: https://api.anthropic.com
  anthropicVersion?: string | null; // default: 2023-06-01
  /**
   * Enables Anthropic compact context management beta mode.
   * Default: true
   */
  compactionEnabled?: boolean | null;
  /**
   * Instruction passed to compact context management edit.
   */
  compactionInstruction?: string | null;
  /**
   * Trigger threshold (input tokens) used by compact context management.
   * Default: 170000
   */
  compactionTriggerTokens?: number | null;
  webSearch?: ProviderWebSearchConfig | null;
  codeExecution?: ProviderCodeExecutionConfig | null;
}

export interface GeminiConfig {
  apiKey: string;
  baseUrl?: string | null; // default: https://generativelanguage.googleapis.com
}

export interface BedrockConfig {
  /**
   * AWS region used for Bedrock Runtime endpoint construction.
   * Example: "us-east-1".
   */
  region: string;
  /**
   * Optional endpoint override (for custom/VPC Bedrock runtime endpoints).
   * When empty, runtime uses: https://bedrock-runtime.{region}.amazonaws.com
   */
  baseUrl?: string | null;
  /**
   * Anthropic Bedrock Messages schema version.
   * Default: bedrock-2023-05-31
   */
  anthropicVersion?: string | null;
  /**
   * Optional Bedrock headers (guardrails, trace, latency tier, etc.).
   * Secret header values should be stored server-side.
   */
  extraHeaders: HeaderKV[];
  /**
   * Enables Anthropic compact context management mode on Bedrock.
   * Default: true
   */
  compactionEnabled?: boolean | null;
  /**
   * Instruction passed to compact context management edit.
   */
  compactionInstruction?: string | null;
  /**
   * Trigger threshold (input tokens) used by compact context management.
   * Default: 170000
   */
  compactionTriggerTokens?: number | null;
  /**
   * Legacy/fallback fields. Server-side encrypted secrets are preferred.
   */
  apiKey?: string | null;
  accessKeyId?: string | null;
  secretAccessKey?: string | null;
  sessionToken?: string | null;
  webSearch?: ProviderWebSearchConfig | null;
  codeExecution?: ProviderCodeExecutionConfig | null;
}

export interface ProviderRuntimeSettings {
  provider: ProviderSelection;
  openai?: OpenAIConfig;
  anthropic?: AnthropicConfig;
  gemini?: GeminiConfig;
  bedrock?: BedrockConfig;
  compatibleProfiles: CompatibleProfile[];
}

export interface ChatRequest {
  model: string;
  systemPrompt: string;
  messages: ChatMessage[];
  tools: ToolDefinition[];
  reasoning?: Record<string, unknown> | null;
  reasoningEnabled?: boolean | null;
  providerState?: Record<string, unknown> | null;
}

export interface ResponseMeta {
  providerId: ProviderId;
  model: string;
  requestId?: string | null;
  inputTokens?: number | null;
  outputTokens?: number | null;
  totalTokens?: number | null;
  profileId?: string | null;
}

export type StreamEvent =
  | { event: 'thinking_start'; data: { traceId: string } }
  | { event: 'thinking_delta'; data: { content: string } }
  | { event: 'thinking_end'; data: { summary?: string } }
  | { event: 'compaction_start'; data: Record<string, never> }
  | { event: 'compaction_delta'; data: { content: string } }
  | { event: 'compaction_end'; data: Record<string, never> }
  | { event: 'text_delta'; data: { content: string } }
  | { event: 'tool_calls'; data: { toolCalls: ToolCall[]; meta: ResponseMeta } }
  | { event: 'final'; data: { answer: string; meta: ResponseMeta; toolCalls?: ToolCall[]; toolResults?: ChatMessage[] } }
  | { event: 'error'; data: { code: string; message: string; retryable: boolean; meta?: ResponseMeta } };

export interface Provider {
  readonly id: ProviderId;
  readonly model: string;
  streamChat(request: ChatRequest, opts?: { signal?: AbortSignal }): AsyncIterable<StreamEvent>;
}
