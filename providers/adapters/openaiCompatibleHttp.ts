import type {
  ChatFileAttachment,
  ChatMessage,
  ChatRequest,
  CompatibleProfile,
  Provider,
  ProviderId,
  ResponseMeta,
  StreamEvent,
  ToolCall,
  ToolDefinition,
} from '../core/types';
import { Role } from '../core/types';
import { ToolNameMapper } from '../core/toolNameMapper';
import { normalizeToolParameters } from '../core/toolSchema';
import { safeTruncate, uuid } from '../core/utils';
import { proxyFetch } from '../core/proxyFetch';
import { parseSSE } from '../stream/sseParser';

type ChatCompletionsChunk = {
  id?: string;
  choices?: Array<{
    delta?: {
      content?: string;
      tool_calls?: Array<{
        index?: number;
        id?: string;
        function?: { name?: string; arguments?: string };
      }>;
    };
    finish_reason?: string | null;
  }>;
  usage?: {
    prompt_tokens?: number;
    completion_tokens?: number;
    total_tokens?: number;
  };
  error?: unknown;
};

type ResponsesSseEventPayload = {
  type?: string;
  delta?: unknown;
  output_index?: number;
  item?: unknown;
  response?: unknown;
  error?: unknown;
  id?: string;
};

function coerceInt(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value)) return Math.max(0, Math.trunc(value));
  if (typeof value === 'string' && /^\d+$/.test(value)) return Math.max(0, Number(value));
  return null;
}

function normalizeBaseUrl(raw: string): string {
  const trimmed = raw.trim().replace(/\/+$/, '');
  return trimmed;
}

function getDefaultUrlBase(): string {
  // Allow relative base URLs like "/proxy/openai/v1" in browser environments.
  const origin =
    (typeof globalThis !== 'undefined' &&
      (globalThis as any).location &&
      typeof (globalThis as any).location.origin === 'string' &&
      (globalThis as any).location.origin) ||
    'http://localhost';
  return origin;
}

function applyQueryParams(url: string, params?: Record<string, string> | null): string {
  if (!params || Object.keys(params).length === 0) return url;
  const parsed = new URL(url, getDefaultUrlBase());
  for (const [k, v] of Object.entries(params)) parsed.searchParams.set(k, v);
  return parsed.toString();
}

function buildEndpointUrl(profile: CompatibleProfile, model: string): string {
  const baseUrl = applyQueryParams(normalizeBaseUrl(profile.baseUrl), profile.queryParams);
  const parsed = new URL(baseUrl, getDefaultUrlBase());
  let path = parsed.pathname.replace(/\/+$/, '');

  const apiType = profile.apiType;
  const endpointSegment = apiType === 'responses' ? '/responses' : '/chat/completions';

  // If the base URL already includes an endpoint, keep it.
  if (!path.includes(endpointSegment)) {
    const modelInPath = Boolean(profile.modelInPath);
    if (modelInPath) path = `${path}/${encodeURIComponent(model)}`;
    path = `${path}${endpointSegment}`;
  }

  const suffix = (profile.endpointSuffix ?? '').trim();
  if (suffix) {
    path = suffix.startsWith('/') ? `${path}${suffix}` : `${path}/${suffix}`;
  }

  parsed.pathname = path;
  return parsed.toString();
}

function headersFromProfile(profile: CompatibleProfile): Record<string, string> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };

  for (const kv of profile.extraHeaders || []) {
    const key = kv.key?.trim();
    if (!key) continue;
    if (kv.isSecret) continue; // injected server-side
    headers[key] = kv.value ?? '';
  }

  return headers;
}

type OpenAIFileInputMode = NonNullable<CompatibleProfile['fileInputMode']>;

type OpenAIInputFilePayload = {
  filename: string;
  fileData: string;
};

type OpenAIInputImagePayload = {
  dataUri: string;
};

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return '';
}

function messageFileAttachments(msg: ChatMessage): ChatFileAttachment[] {
  const camel = (msg as any).fileAttachments;
  if (Array.isArray(camel)) return camel.filter((item) => item && typeof item === 'object') as ChatFileAttachment[];
  const snake = (msg as any).file_attachments;
  if (Array.isArray(snake)) return snake.filter((item) => item && typeof item === 'object') as ChatFileAttachment[];
  return [];
}

function fileAttachmentName(file: ChatFileAttachment): string {
  return firstString(file.fileName, file.file_name);
}

function fileAttachmentKind(file: ChatFileAttachment): string {
  return firstString(file.fileKind, file.file_kind).toLowerCase();
}

function fileAttachmentInlineBase64(file: ChatFileAttachment): string {
  return firstString(file.inlineDataBase64, file.inline_data_base64);
}

function fileAttachmentMimeCandidates(file: ChatFileAttachment): string[] {
  const values = [
    file.mimeType,
    file.mime_type,
    file.normalizedMimeType,
    file.normalized_mime_type,
    file.detectedMimeType,
    file.detected_mime_type,
    file.declaredMimeType,
    file.declared_mime_type,
    file.extensionMimeType,
    file.extension_mime_type,
    file.sniffedMimeType,
    file.sniffed_mime_type,
  ];
  const out: string[] = [];
  const seen = new Set<string>();
  for (const raw of values) {
    const value = typeof raw === 'string' ? raw.trim().toLowerCase() : '';
    if (!value || seen.has(value)) continue;
    seen.add(value);
    out.push(value);
  }
  return out;
}

function fileExtension(fileName: string): string {
  const trimmed = fileName.trim().toLowerCase();
  const index = trimmed.lastIndexOf('.');
  if (index < 0 || index === trimmed.length - 1) return '';
  return trimmed.slice(index + 1);
}

function fileAttachmentIsPDF(file: ChatFileAttachment): boolean {
  if (fileExtension(fileAttachmentName(file)) === 'pdf') return true;
  return fileAttachmentMimeCandidates(file).includes('application/pdf');
}

function fileAttachmentImageMediaType(file: ChatFileAttachment): string {
  for (const mimeType of fileAttachmentMimeCandidates(file)) {
    if (mimeType === 'image/png') return 'image/png';
    if (mimeType === 'image/jpeg' || mimeType === 'image/jpg') return 'image/jpeg';
    if (mimeType === 'image/gif') return 'image/gif';
    if (mimeType === 'image/webp') return 'image/webp';
  }
  switch (fileExtension(fileAttachmentName(file))) {
    case 'png':
      return 'image/png';
    case 'jpg':
    case 'jpeg':
      return 'image/jpeg';
    case 'gif':
      return 'image/gif';
    case 'webp':
      return 'image/webp';
    default:
      return '';
  }
}

function capabilityEnabled(file: ChatFileAttachment, key: string): boolean {
  const capabilities = file.capabilities;
  if (Array.isArray(capabilities)) return capabilities.includes(key);
  if (capabilities && typeof capabilities === 'object') return capabilities[key] === true;
  return false;
}

function normalizeOpenAIFileInputMode(mode?: OpenAIFileInputMode | null): OpenAIFileInputMode {
  if (mode === 'sidecar_only' || mode === 'native_pdf_only' || mode === 'native_supported') return mode;
  return 'native_supported';
}

function openAIFileInputEligible(file: ChatFileAttachment, mode?: OpenAIFileInputMode | null): boolean {
  const normalized = normalizeOpenAIFileInputMode(mode);
  if (normalized === 'sidecar_only') return false;
  if (fileAttachmentImageMediaType(file)) return false;
  if (fileAttachmentIsPDF(file)) return true;
  if (normalized === 'native_pdf_only') return false;
  if (normalized === 'native_supported') return true;
  if (capabilityEnabled(file, 'canUseNativeDocument')) return true;
  const kind = fileAttachmentKind(file);
  if (['text', 'json', 'html', 'csv', 'document', 'spreadsheet', 'presentation'].includes(kind)) return true;
  const ext = fileExtension(fileAttachmentName(file));
  if (['txt', 'md', 'markdown', 'csv', 'tsv', 'json', 'jsonl', 'html', 'htm', 'xml', 'rtf', 'doc', 'docx', 'ppt', 'pptx', 'xls', 'xlsx'].includes(ext)) {
    return true;
  }
  return fileAttachmentMimeCandidates(file).some((mime) => (
    mime.startsWith('text/')
    || mime === 'application/json'
    || mime === 'application/xml'
    || mime === 'application/rtf'
    || mime.includes('wordprocessingml')
    || mime.includes('presentationml')
    || mime.includes('spreadsheetml')
  ));
}

function fallbackMimeForFile(file: ChatFileAttachment): string {
  const nonGeneric = fileAttachmentMimeCandidates(file).find((mime) => mime !== 'application/octet-stream');
  if (nonGeneric) return nonGeneric;
  switch (fileExtension(fileAttachmentName(file))) {
    case 'pdf':
      return 'application/pdf';
    case 'txt':
      return 'text/plain';
    case 'md':
    case 'markdown':
      return 'text/markdown';
    case 'csv':
      return 'text/csv';
    case 'tsv':
      return 'text/tab-separated-values';
    case 'json':
      return 'application/json';
    case 'html':
    case 'htm':
      return 'text/html';
    default:
      return 'application/octet-stream';
  }
}

function fallbackFileName(mimeType: string): string {
  switch (mimeType) {
    case 'application/pdf':
      return 'attachment.pdf';
    case 'text/plain':
      return 'attachment.txt';
    case 'text/markdown':
      return 'attachment.md';
    case 'text/csv':
      return 'attachment.csv';
    case 'text/tab-separated-values':
      return 'attachment.tsv';
    case 'application/json':
      return 'attachment.json';
    default:
      return 'attachment';
  }
}

function fileDataURI(rawData: string, mimeType: string): string {
  const data = rawData.trim();
  if (!data || data.toLowerCase().startsWith('data:')) return data;
  return `data:${mimeType || 'application/octet-stream'};base64,${data}`;
}

function openAIInputFilePayloads(attachments: ChatFileAttachment[], mode?: OpenAIFileInputMode | null): OpenAIInputFilePayload[] {
  if (normalizeOpenAIFileInputMode(mode) === 'sidecar_only') return [];
  const out: OpenAIInputFilePayload[] = [];
  for (const file of attachments) {
    const rawData = fileAttachmentInlineBase64(file);
    if (fileAttachmentImageMediaType(file)) continue;
    if (!rawData || !openAIFileInputEligible(file, mode)) continue;
    const mimeType = fallbackMimeForFile(file);
    const filename = fileAttachmentName(file) || fallbackFileName(mimeType);
    out.push({ filename, fileData: fileDataURI(rawData, mimeType) });
  }
  return out;
}

function openAIInputImagePayloads(attachments: ChatFileAttachment[], mode?: OpenAIFileInputMode | null): OpenAIInputImagePayload[] {
  if (normalizeOpenAIFileInputMode(mode) === 'sidecar_only') return [];
  const out: OpenAIInputImagePayload[] = [];
  for (const file of attachments) {
    const rawData = fileAttachmentInlineBase64(file);
    const mimeType = fileAttachmentImageMediaType(file);
    if (!rawData || !mimeType) continue;
    out.push({ dataUri: fileDataURI(rawData, mimeType) });
  }
  return out;
}

function responsesInputImageContent(attachments: ChatFileAttachment[], mode?: OpenAIFileInputMode | null): Array<Record<string, any>> {
  return openAIInputImagePayloads(attachments, mode).map((image) => ({
    type: 'input_image',
    image_url: image.dataUri,
  }));
}

function responsesInputFileContent(attachments: ChatFileAttachment[], mode?: OpenAIFileInputMode | null): Array<Record<string, any>> {
  return openAIInputFilePayloads(attachments, mode).map((file) => ({
    type: 'input_file',
    filename: file.filename,
    file_data: file.fileData,
  }));
}

function chatCompletionsImageContent(attachments: ChatFileAttachment[], mode?: OpenAIFileInputMode | null): Array<Record<string, any>> {
  return openAIInputImagePayloads(attachments, mode).map((image) => ({
    type: 'image_url',
    image_url: { url: image.dataUri },
  }));
}

function chatCompletionsFileContent(attachments: ChatFileAttachment[], mode?: OpenAIFileInputMode | null): Array<Record<string, any>> {
  return openAIInputFilePayloads(attachments, mode).map((file) => ({
    type: 'file',
    file: {
      filename: file.filename,
      file_data: file.fileData,
    },
  }));
}

function toolAttachmentLabel(msg: ChatMessage): string {
  const toolName = typeof msg.name === 'string' && msg.name.trim() ? msg.name.trim() : 'tool';
  return `Attachment returned by ${toolName}.`;
}

function sanitizeMessagesForChatCompletions(
  request: ChatRequest,
  nameMapper: ToolNameMapper,
  fileInputMode?: OpenAIFileInputMode | null,
): Array<Record<string, any>> {
  const validToolCallIds = collectValidToolCallIds(request.messages);
  const out: Array<Record<string, any>> = [];
  if (request.systemPrompt.trim()) out.push({ role: 'system', content: request.systemPrompt.trim() });

  for (const msg of request.messages) {
    if (msg.role === Role.System) continue;
    if (msg.role === Role.User) {
      const text = safeTruncate(msg.content ?? '');
      const images = chatCompletionsImageContent(messageFileAttachments(msg), fileInputMode);
      const files = chatCompletionsFileContent(messageFileAttachments(msg), fileInputMode);
      if (images.length === 0 && files.length === 0) {
        out.push({ role: 'user', content: text });
        continue;
      }
      const content: Array<Record<string, any>> = [];
      if (text.trim()) content.push({ type: 'text', text });
      content.push(...images);
      content.push(...files);
      out.push({ role: 'user', content: content.length > 0 ? content : [{ type: 'text', text: '' }] });
      continue;
    }
    if (msg.role === Role.Assistant) {
      const validToolCalls = filterToolCallsByValidIds(msg.tool_calls, validToolCallIds);
      const content = msg.content && msg.content.trim() ? safeTruncate(msg.content) : '';
      if (!content && validToolCalls.length === 0) continue;
      const payload: Record<string, any> = {
        role: 'assistant',
        // Some upstreams reject `content: null`.
        content,
      };
      if (validToolCalls.length > 0) {
        payload.tool_calls = validToolCalls.map((call) => {
          const argsText =
            (typeof call.argsText === 'string' && call.argsText.trim()) ? call.argsText : JSON.stringify(call.args ?? {});
          const safeArgsText = argsText.trim() ? argsText : '{}';
          return {
            id: call.id || uuid('call'),
            type: 'function',
            function: {
              name: nameMapper.providerName(call.name),
              arguments: safeArgsText,
            },
          };
        });
      }
      out.push(payload);
      continue;
    }
    if (msg.role === Role.Tool) {
      if (!msg.tool_call_id || !validToolCallIds.has(msg.tool_call_id.trim())) continue;
      out.push({
        role: 'tool',
        tool_call_id: msg.tool_call_id,
        content: safeTruncate(msg.content ?? ''),
      });
      const attachmentContent = [
        { type: 'text', text: toolAttachmentLabel(msg) },
        ...chatCompletionsImageContent(messageFileAttachments(msg), fileInputMode),
        ...chatCompletionsFileContent(messageFileAttachments(msg), fileInputMode),
      ];
      if (attachmentContent.length > 1) {
        out.push({ role: 'user', content: attachmentContent });
      }
    }
  }

  return out;
}

function toolsToChatCompletionsFormat(
  tools: ToolDefinition[],
  nameMapper: ToolNameMapper
): ToolDefinition[] {
  return tools.map((tool) => ({
    type: 'function',
    function: {
      name: nameMapper.providerName(tool.function.name),
      description: tool.function.description,
      parameters: normalizeToolParameters(tool.function.parameters),
    },
  }));
}

function toolsToResponsesFormat(
  tools: ToolDefinition[],
  nameMapper: ToolNameMapper,
  webSearch?: CompatibleProfile['webSearch'],
  codeExecution?: CompatibleProfile['codeExecution'],
): Array<Record<string, any>> {
  const out: Array<Record<string, any>> = tools.map((tool) => ({
    type: 'function',
    name: nameMapper.providerName(tool.function.name),
    description: tool.function.description,
    parameters: normalizeToolParameters(tool.function.parameters),
  }));

  if (webSearch?.enabled) {
    const toolType = (webSearch.toolType || '').trim() || 'web_search';
    const tool: Record<string, any> = { type: toolType };
    const searchContextSize = (webSearch.searchContextSize || '').trim();
    if (searchContextSize) tool.search_context_size = searchContextSize;
    if ((webSearch.allowedDomains && webSearch.allowedDomains.length > 0) || (webSearch.blockedDomains && webSearch.blockedDomains.length > 0)) {
      const filters: Record<string, any> = {};
      if (webSearch.allowedDomains && webSearch.allowedDomains.length > 0) {
        filters.allowed_domains = [...webSearch.allowedDomains];
      }
      if (webSearch.blockedDomains && webSearch.blockedDomains.length > 0) {
        filters.blocked_domains = [...webSearch.blockedDomains];
      }
      if (Object.keys(filters).length > 0) tool.filters = filters;
    }
    out.push(tool);
  }

  if (codeExecution?.enabled) {
    const toolType = (codeExecution.toolType || '').trim() || 'code_interpreter';
    const tool: Record<string, any> = { type: toolType };
    const containerId = (codeExecution.containerId || '').trim();
    if (containerId) {
      tool.container = containerId;
    } else {
      const containerMode = codeExecution.containerMode === 'existing' ? 'existing' : 'auto';
      const container: Record<string, any> = { type: containerMode };
      const memoryLimit = (codeExecution.memoryLimit || '').trim();
      if (memoryLimit) container.memory_limit = memoryLimit;
      tool.container = container;
    }
    out.push(tool);
  }

  return out;
}

function messagesToResponsesInputItems(
  request: ChatRequest,
  nameMapper: ToolNameMapper,
  fileInputMode?: OpenAIFileInputMode | null,
): Array<Record<string, any>> {
  const validToolCallIds = collectValidToolCallIds(request.messages);
  const items: Array<Record<string, any>> = [];
  for (const msg of request.messages) {
    if (msg.role === Role.System) continue;

    if (msg.role === Role.User || msg.role === Role.Assistant) {
      if (msg.role === Role.User) {
        const text = msg.content ? safeTruncate(msg.content) : '';
        const images = responsesInputImageContent(messageFileAttachments(msg), fileInputMode);
        const files = responsesInputFileContent(messageFileAttachments(msg), fileInputMode);
        if (images.length > 0 || files.length > 0) {
          const content: Array<Record<string, any>> = [];
          if (text.trim()) content.push({ type: 'input_text', text });
          content.push(...images);
          content.push(...files);
          items.push({ role: 'user', content });
        } else if (text) {
          items.push({ role: 'user', content: text });
        }
      } else if (msg.content) {
        items.push({ role: 'assistant', content: safeTruncate(msg.content) });
      }
      if (msg.role === Role.Assistant && msg.tool_calls) {
        for (const call of filterToolCallsByValidIds(msg.tool_calls, validToolCallIds)) {
          const argsText =
            (typeof call.argsText === 'string' && call.argsText.trim()) ? call.argsText : JSON.stringify(call.args ?? {});
          items.push({
            type: 'function_call',
            call_id: call.id,
            name: nameMapper.providerName(call.name),
            arguments: argsText,
          });
        }
      }
      continue;
    }

    if (msg.role === Role.Tool) {
      if (!msg.tool_call_id || !validToolCallIds.has(msg.tool_call_id.trim())) continue;
      items.push({
        type: 'function_call_output',
        call_id: msg.tool_call_id,
        output: safeTruncate(msg.content ?? ''),
      });
      const attachmentContent = [
        { type: 'input_text', text: toolAttachmentLabel(msg) },
        ...responsesInputImageContent(messageFileAttachments(msg), fileInputMode),
        ...responsesInputFileContent(messageFileAttachments(msg), fileInputMode),
      ];
      if (attachmentContent.length > 1) {
        items.push({ role: 'user', content: attachmentContent });
      }
    }
  }
  return items;
}

function collectValidToolCallIds(messages: ChatMessage[]): Set<string> {
  const valid = new Set<string>();

  for (let i = 0; i < messages.length; i += 1) {
    const msg = messages[i];
    if (msg.role !== Role.Assistant || !Array.isArray(msg.tool_calls) || msg.tool_calls.length === 0) continue;

    const expected = new Set<string>();
    for (const call of msg.tool_calls) {
      if ((call as any).provider_executed === true) continue;
      const id = typeof call.id === 'string' ? call.id.trim() : '';
      if (id) expected.add(id);
    }
    if (expected.size === 0) continue;

    for (let j = i + 1; j < messages.length; j += 1) {
      const next = messages[j];
      if (next.role !== Role.Tool) break;
      const id = typeof next.tool_call_id === 'string' ? next.tool_call_id.trim() : '';
      if (id && expected.has(id)) valid.add(id);
    }
  }

  return valid;
}

function filterToolCallsByValidIds(toolCalls: ToolCall[] | undefined, validToolCallIds: Set<string>): ToolCall[] {
  if (!Array.isArray(toolCalls) || toolCalls.length === 0 || validToolCallIds.size === 0) return [];
  return toolCalls.filter((call) => (call as any).provider_executed !== true && validToolCallIds.has((call.id || '').trim()));
}

function toolCallsFromChatCompletions(
  toolCallsByIndex: Map<number, { id: string; name: string; arguments: string }>,
  nameMapper: ToolNameMapper
): ToolCall[] {
  const toolCalls: ToolCall[] = [];
  const indices = Array.from(toolCallsByIndex.keys()).sort((a, b) => a - b);
  for (const idx of indices) {
    const raw = toolCallsByIndex.get(idx);
    if (!raw) continue;
    const providerName = raw.name || '';
    const canonical = nameMapper.canonicalName(providerName);
    const argsText = raw.arguments || '';
    let args: Record<string, unknown> = {};
    try {
      const parsed = JSON.parse(argsText || '{}');
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) args = parsed;
    } catch {
      args = {};
    }
    toolCalls.push({
      id: raw.id || uuid('call'),
      name: canonical,
      args,
      argsText,
    });
  }
  return toolCalls;
}

function toolCallsFromResponsesAccumulator(
  byIndex: Map<number, { id: string; name: string; arguments: string }>,
  nameMapper: ToolNameMapper
): ToolCall[] {
  const indices = Array.from(byIndex.keys()).sort((a, b) => a - b);
  const out: ToolCall[] = [];
  for (const idx of indices) {
    const raw = byIndex.get(idx);
    if (!raw) continue;
    const providerName = raw.name || '';
    const canonical = nameMapper.canonicalName(providerName);
    const argsText = raw.arguments || '';
    let args: Record<string, unknown> = {};
    try {
      const parsed = JSON.parse(argsText || '{}');
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) args = parsed;
    } catch {
      args = {};
    }
    out.push({
      id: raw.id || uuid('call'),
      name: canonical,
      args,
      argsText,
    });
  }
  return out;
}

function toErrorEvent(
  providerId: ProviderId,
  model: string,
  message: string,
  opts?: { code?: string; retryable?: boolean; meta?: Partial<ResponseMeta> }
): StreamEvent {
  const code = opts?.code ?? 'PROVIDER_ERROR';
  const retryable = opts?.retryable ?? true;
  const meta = opts?.meta;

  return {
    event: 'error',
    data: {
      code,
      message,
      retryable,
      meta: {
        ...(meta ?? {}),
        providerId,
        model,
      },
    },
  };
}

export class OpenAICompatibleHttpProvider implements Provider {
  public readonly id: ProviderId;
  public readonly model: string;
  private readonly profile: CompatibleProfile;

  constructor(opts: { providerId: ProviderId; model: string; profile: CompatibleProfile }) {
    this.id = opts.providerId;
    this.model = opts.model;
    this.profile = opts.profile;
  }

  async *streamChat(request: ChatRequest, opts?: { signal?: AbortSignal }): AsyncIterable<StreamEvent> {
    const profile = this.profile;
    const model = request.model || this.model;
    const nameMapper = new ToolNameMapper();

    const metaBase: ResponseMeta = {
      providerId: this.id,
      model,
      profileId: profile.id,
    };

    const url = buildEndpointUrl(profile, model);
    const headers = headersFromProfile(profile);

    const timeoutMs = profile.timeoutMs ?? null;
    const controller = new AbortController();
    const outerSignal = opts?.signal;
    const onAbort = () => controller.abort();
    outerSignal?.addEventListener('abort', onAbort, { once: true });
    const timeoutId = timeoutMs ? window.setTimeout(() => controller.abort(), timeoutMs) : null;

    try {
      if (profile.apiType === 'responses') {
        const tools = toolsToResponsesFormat(request.tools, nameMapper, profile.webSearch, profile.codeExecution);
        const inputItems = messagesToResponsesInputItems(request, nameMapper, profile.fileInputMode);
        const body: Record<string, any> = {
          stream: true,
          input: inputItems.length > 0 ? inputItems : '',
        };
        if (profile.modelInBody !== false) body.model = model;
        if (request.systemPrompt.trim()) body.instructions = request.systemPrompt.trim();
        {
          const effort = typeof profile.reasoningEffort === 'string' ? profile.reasoningEffort.trim() : '';
          if (effort) body.reasoning = { effort };
        }
        if (tools.length > 0) {
          body.tools = tools;
          body.tool_choice = 'auto';
        }
        if (profile.payloadOverrides) Object.assign(body, profile.payloadOverrides);

        const response = await proxyFetch(url, {
          method: 'POST',
          headers,
          body: JSON.stringify(body),
          signal: controller.signal,
        }, { providerId: this.id, profileId: profile.id });

        if (!response.ok) {
          const text = await response.text().catch(() => '');
          yield toErrorEvent(this.id, model, text || `HTTP ${response.status}`, {
            retryable: response.status >= 500 || response.status === 429 || response.status === 408,
            code: 'HTTP_ERROR',
            meta: metaBase,
          });
          return;
        }
        if (!response.body) {
          yield toErrorEvent(this.id, model, 'Missing response body', { meta: metaBase });
          return;
        }

        let accumulatedText = '';
        const toolCallsByIndex = new Map<number, { id: string; name: string; arguments: string }>();
        let requestId: string | null = null;
        let inputTokens: number | null = null;
        let outputTokens: number | null = null;
        let totalTokens: number | null = null;
        let finalSent = false;

        for await (const msg of parseSSE(response.body, { signal: controller.signal })) {
          const dataStr = msg.data?.trim();
          if (!dataStr) continue;
          if (dataStr === '[DONE]') break;

          let payload: ResponsesSseEventPayload;
          try {
            payload = JSON.parse(dataStr) as ResponsesSseEventPayload;
          } catch {
            continue;
          }

          const currentType = msg.event || payload.type || '';

          if (!requestId && typeof payload.id === 'string') requestId = payload.id;

          if (currentType === 'response.output_text.delta' || currentType === 'response.text.delta') {
            const delta = typeof payload.delta === 'string' ? payload.delta : null;
            if (delta) {
              accumulatedText += delta;
              yield { event: 'text_delta', data: { content: delta } };
            }
            continue;
          }

          if (currentType === 'response.output_item.added') {
            const item = payload.item as any;
            if (item && typeof item === 'object' && item.type === 'function_call') {
              const index = typeof payload.output_index === 'number' ? payload.output_index : 0;
              toolCallsByIndex.set(index, {
                id: String(item.call_id || ''),
                name: String(item.name || ''),
                arguments: '',
              });
            }
            continue;
          }

          if (currentType === 'response.function_call_arguments.delta') {
            const index = typeof payload.output_index === 'number' ? payload.output_index : 0;
            const entry = toolCallsByIndex.get(index);
            const delta = typeof payload.delta === 'string' ? payload.delta : null;
            if (entry && delta) entry.arguments += delta;
            continue;
          }

          if (currentType === 'response.completed') {
            const responsePayload = payload.response as any;
            if (responsePayload && typeof responsePayload === 'object') {
              requestId = typeof responsePayload.id === 'string' ? responsePayload.id : requestId;
              const usage = responsePayload.usage;
              if (usage && typeof usage === 'object') {
                inputTokens = coerceInt(usage.input_tokens);
                outputTokens = coerceInt(usage.output_tokens);
                totalTokens = coerceInt(usage.total_tokens);
              }
              const outputText = responsePayload.output_text;
              if (typeof outputText === 'string' && outputText) accumulatedText = outputText;

              // Some upstreams include tool calls only in the final response payload.
              if (toolCallsByIndex.size === 0 && Array.isArray(responsePayload.output)) {
                responsePayload.output.forEach((it: any, idx: number) => {
                  if (!it || typeof it !== 'object') return;
                  if (it.type !== 'function_call') return;
                  toolCallsByIndex.set(idx, {
                    id: String(it.call_id || ''),
                    name: String(it.name || ''),
                    arguments: String(it.arguments || ''),
                  });
                });
              }
            }

            const meta: ResponseMeta = {
              ...metaBase,
              requestId,
              inputTokens,
              outputTokens,
              totalTokens,
            };

            if (toolCallsByIndex.size > 0) {
              const toolCalls = toolCallsFromResponsesAccumulator(toolCallsByIndex, nameMapper);
              yield { event: 'tool_calls', data: { toolCalls, meta } };
            } else {
              yield { event: 'final', data: { answer: accumulatedText || '(No response)', meta } };
            }
            finalSent = true;
            break;
          }

          if (currentType === 'response.error' || currentType === 'response.failed' || currentType === 'response.incomplete') {
            const err = payload.error as any;
            const msgText =
              (err && typeof err === 'object' && (err.message || err.error?.message)) ? String(err.message || err.error?.message) : 'Responses API error';
            yield toErrorEvent(this.id, model, msgText, { meta: metaBase });
            return;
          }
        }

        if (!finalSent) {
          yield { event: 'final', data: { answer: accumulatedText || '(No response)', meta: metaBase } };
        }

        return;
      }

      // chat_completions
      const openaiTools = toolsToChatCompletionsFormat(request.tools, nameMapper);
      const messages = sanitizeMessagesForChatCompletions(request, nameMapper, profile.fileInputMode);

      const body: Record<string, any> = { stream: true, messages };
      if (profile.modelInBody !== false) body.model = model;
      {
        const effort = typeof profile.reasoningEffort === 'string' ? profile.reasoningEffort.trim() : '';
        if (effort) body.reasoning_effort = effort;
      }
      if (openaiTools.length > 0) {
        body.tools = openaiTools;
        body.tool_choice = 'auto';
      }
      if (profile.payloadOverrides) Object.assign(body, profile.payloadOverrides);

      const response = await proxyFetch(url, {
        method: 'POST',
        headers,
        body: JSON.stringify(body),
        signal: controller.signal,
      }, { providerId: this.id, profileId: profile.id });

      if (!response.ok) {
        const text = await response.text().catch(() => '');
        yield toErrorEvent(this.id, model, text || `HTTP ${response.status}`, {
          retryable: response.status >= 500 || response.status === 429 || response.status === 408,
          code: 'HTTP_ERROR',
          meta: metaBase,
        });
        return;
      }
      if (!response.body) {
        yield toErrorEvent(this.id, model, 'Missing response body', { meta: metaBase });
        return;
      }

      let accumulatedText = '';
      const toolCallsByIndex = new Map<number, { id: string; name: string; arguments: string }>();
      let requestId: string | null = response.headers.get('x-request-id') || response.headers.get('request-id') || null;
      let inputTokens: number | null = null;
      let outputTokens: number | null = null;
      let totalTokens: number | null = null;

      for await (const msg of parseSSE(response.body, { signal: controller.signal })) {
        const dataStr = msg.data?.trim();
        if (!dataStr) continue;
        if (dataStr === '[DONE]') break;

        let json: ChatCompletionsChunk;
        try {
          json = JSON.parse(dataStr) as ChatCompletionsChunk;
        } catch {
          continue;
        }

        if (!requestId && typeof json.id === 'string') requestId = json.id;

        const choice = Array.isArray(json.choices) && json.choices.length > 0 ? json.choices[0] : null;
        const delta = choice?.delta || null;

        if (delta?.content) {
          accumulatedText += delta.content;
          yield { event: 'text_delta', data: { content: delta.content } };
        }

        if (Array.isArray(delta?.tool_calls)) {
          delta!.tool_calls!.forEach((tc, offset) => {
            const index = typeof tc.index === 'number' ? tc.index : offset;
            const entry = toolCallsByIndex.get(index) ?? { id: '', name: '', arguments: '' };
            if (tc.id) entry.id = tc.id;
            if (tc.function?.name) entry.name = tc.function.name;
            if (tc.function?.arguments) entry.arguments += tc.function.arguments;
            toolCallsByIndex.set(index, entry);
          });
        }

        if (json.usage && typeof json.usage === 'object') {
          inputTokens = coerceInt((json.usage as any).prompt_tokens);
          outputTokens = coerceInt((json.usage as any).completion_tokens);
          totalTokens = coerceInt((json.usage as any).total_tokens);
        }
      }

      const meta: ResponseMeta = {
        ...metaBase,
        requestId,
        inputTokens,
        outputTokens,
        totalTokens,
      };

      if (toolCallsByIndex.size > 0) {
        const toolCalls = toolCallsFromChatCompletions(toolCallsByIndex, nameMapper);
        yield { event: 'tool_calls', data: { toolCalls, meta } };
        return;
      }

      yield { event: 'final', data: { answer: accumulatedText || '(No response)', meta } };
    } catch (err: any) {
      if (controller.signal.aborted) {
        yield toErrorEvent(this.id, model, 'Request aborted', { retryable: false, code: 'ABORTED', meta: metaBase });
        return;
      }
      yield toErrorEvent(this.id, model, String(err?.message || err), { meta: metaBase });
    } finally {
      outerSignal?.removeEventListener('abort', onAbort);
      if (timeoutId) window.clearTimeout(timeoutId);
    }
  }
}

export function normalizeCompatibleProfileBaseUrl(url: string): string {
  const trimmed = url.trim();
  if (!trimmed) return trimmed;
  const withoutQuery = trimmed.split('?')[0];
  const segments = ['/chat/completions', '/responses', '/v1/chat/completions', '/v1/responses'];
  for (const seg of segments) {
    const idx = withoutQuery.indexOf(seg);
    if (idx !== -1) return withoutQuery.slice(0, idx);
  }
  return withoutQuery.replace(/\/+$/, '');
}

export function inferCompatibleApiType(url: string): 'chat_completions' | 'responses' {
  const lower = url.toLowerCase();
  return lower.includes('/responses') ? 'responses' : 'chat_completions';
}

export function extractBearerKeyFromHeaders(headers: Array<{ key: string; value: string }>): string | null {
  for (const h of headers) {
    if (!h.key) continue;
    if (h.key.toLowerCase() !== 'authorization') continue;
    const v = (h.value || '').trim();
    if (!v.toLowerCase().startsWith('bearer ')) continue;
    const key = v.slice('bearer '.length).trim();
    return key || null;
  }
  return null;
}

export function headersArrayToExtraHeaders(headers: Array<{ id: string; key: string; value: string; isSecret?: boolean }>): Array<{
  id: string;
  key: string;
  value: string;
  isSecret?: boolean;
}> {
  // Filter out Content-Type and Authorization (handled separately).
  return headers
    .filter((h) => Boolean(h.key?.trim()))
    .filter((h) => {
      const k = h.key.trim().toLowerCase();
      return k !== 'content-type' && k !== 'authorization' && k !== 'x-api-key' && k !== 'x-goog-api-key';
    })
    .map((h) => ({ id: h.id, key: h.key, value: h.value, isSecret: h.isSecret }));
}
