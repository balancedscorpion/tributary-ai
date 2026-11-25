import type {
  ChatFileAttachment,
  ChatMessage,
  ChatRequest,
  Provider,
  ProviderCodeExecutionConfig,
  ProviderId,
  ProviderWebSearchConfig,
  ResponseMeta,
  StreamEvent,
  ToolCall,
} from '../core/types';
import { Role } from '../core/types';
import { ToolNameMapper } from '../core/toolNameMapper';
import { normalizeToolParameters } from '../core/toolSchema';
import { safeTruncate, uuid } from '../core/utils';
import { proxyFetch } from '../core/proxyFetch';
import { parseSSE } from '../stream/sseParser';
import {
  ANTHROPIC_COMPACTION_DEFAULT_INSTRUCTION,
  ANTHROPIC_COMPACTION_DEFAULT_TRIGGER_TOKENS,
} from '@/lib/bedrockCompaction';

const COMPACTION_BETA = 'compact-2026-01-12';
const COMPACTION_EDIT_TYPE = 'compact_20260112';
const SKILLS_BETA = 'skills-2025-10-02';

type AnthropicStreamEnvelope = {
  type?: string;
  message?: any;
  index?: number;
  content_block?: any;
  delta?: any;
  usage?: any;
  error?: any;
};

function coerceInt(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value)) return Math.max(0, Math.trunc(value));
  if (typeof value === 'string' && /^\d+$/.test(value)) return Math.max(0, Number(value));
  return null;
}

function codeExecutionBeta(toolType?: string | null): string | null {
  const raw = (toolType || '').trim();
  if (!raw.startsWith('code_execution_')) return null;
  const suffix = raw.slice('code_execution_'.length);
  if (suffix.length === 8) return `code-execution-${suffix.slice(0, 4)}-${suffix.slice(4, 6)}-${suffix.slice(6)}`;
  return raw.replaceAll('_', '-');
}

function buildHeaders(
  anthropicVersion: string,
  compactionEnabled: boolean,
  codeExecution?: ProviderCodeExecutionConfig | null,
): Record<string, string> {
  const headers: Record<string, string> = {
    'Content-Type': 'application/json',
    'anthropic-version': anthropicVersion,
  };
  const betas: string[] = [];
  if (compactionEnabled) betas.push(COMPACTION_BETA);
  if (codeExecution?.enabled) {
    const beta = codeExecutionBeta(codeExecution.toolType);
    if (beta) betas.push(beta);
    if (Array.isArray(codeExecution.skills) && codeExecution.skills.length > 0) {
      betas.push(SKILLS_BETA);
    }
  }
  if (betas.length > 0) {
    headers['anthropic-beta'] = betas.join(',');
  }
  return headers;
}

function normalizeBaseUrl(raw: string): string {
  return raw.trim().replace(/\/+$/, '');
}

function toolsToAnthropic(
  tools: any[],
  nameMapper: ToolNameMapper,
  webSearch?: ProviderWebSearchConfig | null,
  codeExecution?: ProviderCodeExecutionConfig | null,
): any[] {
  const out: Array<Record<string, any>> = tools.map((tool) => ({
    name: nameMapper.providerName(tool.function.name),
    description: tool.function.description || '',
    input_schema: normalizeToolParameters(tool.function.parameters),
  }));
  if (webSearch?.enabled) {
    const tool: Record<string, any> = {
      type: (webSearch.toolType || '').trim() || 'web_search_20260209',
      name: 'web_search',
    };
    if (typeof webSearch.maxUses === 'number' && Number.isFinite(webSearch.maxUses) && webSearch.maxUses > 0) {
      tool.max_uses = Math.max(1, Math.trunc(webSearch.maxUses));
    }
    if (Array.isArray(webSearch.allowedDomains) && webSearch.allowedDomains.length > 0) {
      tool.allowed_domains = [...webSearch.allowedDomains];
    }
    if (Array.isArray(webSearch.blockedDomains) && webSearch.blockedDomains.length > 0) {
      tool.blocked_domains = [...webSearch.blockedDomains];
    }
    if (Array.isArray(webSearch.allowedCallers) && webSearch.allowedCallers.length > 0) {
      tool.allowed_callers = [...webSearch.allowedCallers];
    } else if (tool.type !== 'web_search_20250305' && !codeExecution?.enabled) {
      tool.allowed_callers = ['direct'];
    }
    out.push(tool);
  }
  if (codeExecution?.enabled) {
    out.push({
      type: (codeExecution.toolType || '').trim() || 'code_execution_20260120',
      name: 'code_execution',
    });
  }
  return out;
}

function orderedContentBlocks(blocks: Map<number, Record<string, any>>): Array<Record<string, any>> {
  return Array.from(blocks.entries())
    .sort((a, b) => a[0] - b[0])
    .map(([, block]) => ({ ...block }));
}

function textFromContentBlocks(blocks: Array<Record<string, any>>): string {
  return blocks
    .map((block) => (typeof block?.text === 'string' ? block.text : ''))
    .filter(Boolean)
    .join('');
}

function toolCallsFromAnthropicBlocks(
  blocks: Array<Record<string, any>>,
  nameMapper: ToolNameMapper
): ToolCall[] {
  const out: ToolCall[] = [];
  for (const block of blocks) {
    if (block?.type !== 'tool_use') continue;
    const args = block?.input && typeof block.input === 'object' && !Array.isArray(block.input)
      ? block.input as Record<string, unknown>
      : {};
    const argsText = JSON.stringify(args);
    out.push({
      id: String(block.id || uuid('call')),
      name: nameMapper.canonicalName(String(block.name || '')),
      args,
      argsText,
    });
  }
  return out;
}

function toolCallsFromOpenAIMessage(raw: any, nameMapper: ToolNameMapper): ToolCall[] {
  if (!raw || typeof raw !== 'object' || !Array.isArray(raw.tool_calls)) return [];
  const out: ToolCall[] = [];
  for (const item of raw.tool_calls) {
    if (!item || typeof item !== 'object') continue;
    const fn = item.function && typeof item.function === 'object' ? item.function : null;
    const argsText = typeof fn?.arguments === 'string' && fn.arguments.trim() ? fn.arguments : '{}';
    let args: Record<string, unknown> = {};
    try {
      const parsed = JSON.parse(argsText);
      if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) args = parsed;
    } catch {
      args = {};
    }
    out.push({
      id: String(item.id || uuid('call')),
      name: nameMapper.canonicalName(String(fn?.name || '')),
      args,
      argsText,
    });
  }
  return out;
}

function collectValidToolUseIds(messages: ChatRequest['messages']): Set<string> {
  const valid = new Set<string>();

  for (let i = 0; i < messages.length; i += 1) {
    const msg = messages[i];
    if (msg.role !== Role.Assistant || !Array.isArray(msg.tool_calls) || msg.tool_calls.length === 0) continue;

    const expected = new Set<string>();
    for (const call of msg.tool_calls) {
      if ((call as any).provider_executed === true) continue;
      const id = String(call?.id || '').trim();
      if (id) expected.add(id);
    }
    if (expected.size === 0) continue;

    for (let j = i + 1; j < messages.length; j += 1) {
      const next = messages[j];
      if (next.role !== Role.Tool) break;
      const id = String(next.tool_call_id || '').trim();
      if (id && expected.has(id)) valid.add(id);
    }
  }

  return valid;
}

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

function fileAttachmentPreviewText(file: ChatFileAttachment): string {
  return firstString(file.previewText, file.preview_text);
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

function fileAttachmentMime(file: ChatFileAttachment): string {
  return fileAttachmentMimeCandidates(file)[0] || '';
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

function fileAttachmentIsTextLike(file: ChatFileAttachment): boolean {
  if (file.isText === true || file.is_text === true) return true;
  const mime = fileAttachmentMime(file);
  if (mime.startsWith('text/')) return true;
  return [
    'application/json',
    'application/xml',
    'application/javascript',
    'application/x-ndjson',
    'application/yaml',
    'application/x-yaml',
    'application/toml',
  ].includes(mime);
}

function capabilityEnabled(file: ChatFileAttachment, key: string): boolean {
  const capabilities = file.capabilities;
  if (Array.isArray(capabilities)) return capabilities.includes(key);
  if (capabilities && typeof capabilities === 'object') return capabilities[key] === true;
  return false;
}

function anthropicNativeDocumentCandidate(file: ChatFileAttachment): boolean {
  if (capabilityEnabled(file, 'canUseNativeDocument')) return true;
  if (fileAttachmentIsPDF(file) || fileAttachmentIsTextLike(file)) return true;
  if (fileAttachmentPreviewText(file)) return true;
  const kind = fileAttachmentKind(file);
  if (['pdf', 'text', 'csv', 'json', 'html', 'document', 'spreadsheet', 'presentation'].includes(kind)) return true;
  return ['txt', 'md', 'markdown', 'csv', 'tsv', 'json', 'jsonl', 'html', 'htm', 'xml', 'rtf', 'doc', 'docx', 'ppt', 'pptx', 'xls', 'xlsx'].includes(fileExtension(fileAttachmentName(file)));
}

function stripDataUrlPrefix(rawData: string): string {
  const data = rawData.trim();
  const match = /^data:[^,]*;base64,(.*)$/i.exec(data);
  return match ? match[1].trim() : data;
}

function decodeBase64Text(rawData: string): string {
  const data = stripDataUrlPrefix(rawData);
  if (!data) return '';
  try {
    if (typeof globalThis.atob === 'function') {
      return safeTruncate(globalThis.atob(data));
    }
  } catch {
    return '';
  }
  return '';
}

function anthropicAttachmentText(file: ChatFileAttachment): string {
  const rawData = fileAttachmentInlineBase64(file);
  if (rawData && fileAttachmentIsTextLike(file)) {
    const decoded = decodeBase64Text(rawData).trim();
    if (decoded) return decoded;
  }
  const preview = fileAttachmentPreviewText(file);
  return preview ? safeTruncate(preview) : '';
}

function anthropicDocumentBlockForAttachment(file: ChatFileAttachment): Record<string, any> | null {
  if (!anthropicNativeDocumentCandidate(file)) return null;

  const title = fileAttachmentName(file);
  const rawData = fileAttachmentInlineBase64(file);
  if (rawData && fileAttachmentIsPDF(file)) {
    const block: Record<string, any> = {
      type: 'document',
      source: {
        type: 'base64',
        media_type: 'application/pdf',
        data: stripDataUrlPrefix(rawData),
      },
    };
    if (title) block.title = title;
    return block;
  }

  const text = anthropicAttachmentText(file);
  if (!text) return null;
  const block: Record<string, any> = {
    type: 'document',
    source: {
      type: 'content',
      content: [{ type: 'text', text }],
    },
  };
  if (title) block.title = title;
  return block;
}

function userContentToAnthropic(msg: ChatMessage): string | Array<Record<string, any>> {
  const text = safeTruncate(msg.content ?? '');
  const blocks = messageFileAttachments(msg)
    .map(anthropicDocumentBlockForAttachment)
    .filter((block): block is Record<string, any> => Boolean(block));
  if (text.trim()) blocks.push({ type: 'text', text });
  if (blocks.length === 0) return text;
  if (blocks.length === 1 && blocks[0].type === 'text') return blocks[0].text;
  return blocks;
}

function toolAttachmentLabel(msg: ChatMessage): string {
  const toolName = typeof msg.name === 'string' && msg.name.trim() ? msg.name.trim() : 'tool';
  return `Attachment returned by ${toolName}.`;
}

function toolAttachmentContentToAnthropic(msg: ChatMessage): Array<Record<string, any>> {
  const blocks = messageFileAttachments(msg)
    .map(anthropicDocumentBlockForAttachment)
    .filter((block): block is Record<string, any> => Boolean(block));
  if (blocks.length === 0) return [];
  return [{ type: 'text', text: toolAttachmentLabel(msg) }, ...blocks];
}

function messagesToAnthropic(
  request: ChatRequest,
  nameMapper: ToolNameMapper
): Array<{ role: 'user' | 'assistant'; content: any }> {
  const out: Array<{ role: 'user' | 'assistant'; content: any }> = [];
  const validToolUseIds = collectValidToolUseIds(request.messages);

  const flushToolResults = (toolBlocks: any[]) => {
    if (toolBlocks.length === 0) return;
    out.push({ role: 'user', content: toolBlocks.slice() });
    toolBlocks.length = 0;
  };

  const pendingToolResults: any[] = [];

  for (const msg of request.messages) {
    if (msg.role === Role.System) continue;

    if (msg.role === Role.Tool) {
      const toolUseId = String(msg.tool_call_id || '').trim();
      if (!toolUseId || !validToolUseIds.has(toolUseId)) continue;
      const content = msg.content ?? '';
      pendingToolResults.push({
        type: 'tool_result',
        tool_use_id: toolUseId,
        content: safeTruncate(String(content)),
      });
      pendingToolResults.push(...toolAttachmentContentToAnthropic(msg));
      continue;
    }

    // Ensure tool results are delivered before the next non-tool message.
    flushToolResults(pendingToolResults);

    if (msg.role === Role.User) {
      out.push({ role: 'user', content: userContentToAnthropic(msg) });
      continue;
    }

    if (msg.role === Role.Assistant) {
      const blocks: any[] = [];
      if (msg.content && msg.content.trim()) {
        blocks.push({ type: 'text', text: safeTruncate(msg.content) });
      }
      for (const call of msg.tool_calls || []) {
        if ((call as any).provider_executed === true) continue;
        const toolUseId = String(call?.id || '').trim();
        if (!toolUseId || !validToolUseIds.has(toolUseId)) continue;
        blocks.push({
          type: 'tool_use',
          id: toolUseId,
          name: nameMapper.providerName(call.name),
          input: call.args || {},
        });
      }
      // Skip assistant turns that become empty after filtering invalid tool uses.
      // Sending an empty text block is rejected by Anthropic.
      if (blocks.length === 0) continue;
      out.push({ role: 'assistant', content: blocks });
    }
  }

  flushToolResults(pendingToolResults);
  return out;
}

export class AnthropicHttpProvider implements Provider {
  public readonly id: ProviderId = 'anthropic';
  public readonly model: string;
  private readonly baseUrl: string;
  private readonly anthropicVersion: string;
  private readonly compactionEnabled: boolean;
  private readonly compactionInstruction: string;
  private readonly compactionTriggerTokens: number;
  private readonly webSearch: ProviderWebSearchConfig | null;
  private readonly codeExecution: ProviderCodeExecutionConfig | null;

  constructor(opts: {
    model: string;
    baseUrl?: string | null;
    anthropicVersion?: string | null;
    compactionEnabled?: boolean | null;
    compactionInstruction?: string | null;
    compactionTriggerTokens?: number | null;
    webSearch?: ProviderWebSearchConfig | null;
    codeExecution?: ProviderCodeExecutionConfig | null;
  }) {
    this.model = opts.model;
    this.baseUrl = normalizeBaseUrl(opts.baseUrl || 'https://api.anthropic.com');
    this.anthropicVersion = (opts.anthropicVersion || '2023-06-01').trim() || '2023-06-01';
    this.compactionEnabled = opts.compactionEnabled !== false;
    this.compactionInstruction = (opts.compactionInstruction || '').trim() || ANTHROPIC_COMPACTION_DEFAULT_INSTRUCTION;
    const triggerTokens = Number(opts.compactionTriggerTokens);
    this.compactionTriggerTokens = Number.isFinite(triggerTokens) && triggerTokens >= 50000
      ? Math.round(triggerTokens)
      : ANTHROPIC_COMPACTION_DEFAULT_TRIGGER_TOKENS;
    this.webSearch = opts.webSearch || null;
    this.codeExecution = opts.codeExecution || null;
  }

  async *streamChat(request: ChatRequest, opts?: { signal?: AbortSignal }): AsyncIterable<StreamEvent> {
    const nameMapper = new ToolNameMapper();
    const headers = buildHeaders(this.anthropicVersion, this.compactionEnabled, this.codeExecution);
    const url = `${this.baseUrl}/v1/messages`;

    // Seed tool name mapping from definitions first for stable, deterministic provider tool names.
    const tools = toolsToAnthropic(request.tools as any[], nameMapper, this.webSearch, this.codeExecution);
    const messages = messagesToAnthropic(request, nameMapper);

    const body: Record<string, any> = {
      model: request.model || this.model,
      system: request.systemPrompt.trim() || undefined,
      messages,
      stream: true,
      // Conservative default; can be made configurable later.
      max_tokens: 8192,
    };
    if (this.compactionEnabled) {
      body.context_management = {
        edits: [
          {
            type: COMPACTION_EDIT_TYPE,
            instructions: this.compactionInstruction,
            trigger: {
              type: 'input_tokens',
              value: this.compactionTriggerTokens,
            },
          },
        ],
      };
    }
    if (tools.length > 0) body.tools = tools;
    if (this.codeExecution?.enabled) {
      const containerId = (this.codeExecution.containerId || '').trim();
      if (containerId) {
        body.container = containerId;
      } else if (Array.isArray(this.codeExecution.skills) && this.codeExecution.skills.length > 0) {
        body.container = {
          skills: this.codeExecution.skills
            .filter((skill) => skill && typeof skill.type === 'string' && typeof skill.skillId === 'string')
            .map((skill) => ({
              type: skill.type,
              skill_id: skill.skillId,
              ...(skill.version ? { version: skill.version } : {}),
            })),
        };
      }
    }

    const controller = new AbortController();
    const outerSignal = opts?.signal;
    const onAbort = () => controller.abort();
    outerSignal?.addEventListener('abort', onAbort, { once: true });

    const metaBase: ResponseMeta = {
      providerId: 'anthropic',
      model: request.model || this.model,
    };

    try {
      const response = await proxyFetch(url, {
        method: 'POST',
        headers,
        body: JSON.stringify(body),
        signal: controller.signal,
      }, { providerId: 'anthropic' });

      if (!response.ok) {
        const text = await response.text().catch(() => '');
        yield {
          event: 'error',
          data: {
            code: 'HTTP_ERROR',
            message: text || `HTTP ${response.status}`,
            retryable: response.status >= 500 || response.status === 429 || response.status === 408,
            meta: metaBase,
          },
        };
        return;
      }

      if (!response.body) {
        yield {
          event: 'error',
          data: { code: 'PROVIDER_ERROR', message: 'Missing response body', retryable: true, meta: metaBase },
        };
        return;
      }

      const contentType = (response.headers.get('content-type') || '').toLowerCase();
      if (!contentType.includes('text/event-stream')) {
        const rawText = await response.text().catch(() => '');
        let payload: any = null;
        try {
          payload = JSON.parse(rawText);
        } catch {
          payload = null;
        }
        if (!payload || typeof payload !== 'object') {
          yield { event: 'final', data: { answer: rawText || '(No response)', meta: metaBase } };
          return;
        }

        const usage = payload?.usage;
        const meta: ResponseMeta = {
          ...metaBase,
          requestId: typeof payload?.id === 'string' ? payload.id : null,
          inputTokens: usage && typeof usage === 'object' ? coerceInt(usage.input_tokens) : null,
          outputTokens: usage && typeof usage === 'object' ? coerceInt(usage.output_tokens) : null,
          totalTokens: usage && typeof usage === 'object' ? coerceInt(usage.total_tokens) : null,
        };

        const directBlocks = Array.isArray(payload.content)
          ? payload.content.filter((item: any) => item && typeof item === 'object').map((item: any) => ({ ...item }))
          : [];
        const directToolCalls = toolCallsFromAnthropicBlocks(directBlocks, nameMapper);
        if (directToolCalls.length > 0 || payload?.stop_reason === 'tool_use') {
          yield { event: 'tool_calls', data: { toolCalls: directToolCalls, meta } };
          return;
        }
        const directAnswer = textFromContentBlocks(directBlocks);
        if (directAnswer) {
          yield { event: 'final', data: { answer: directAnswer, meta } };
          return;
        }

        const choice = Array.isArray(payload.choices) && payload.choices.length > 0 ? payload.choices[0] : null;
        const message = choice && typeof choice === 'object' && choice.message && typeof choice.message === 'object'
          ? choice.message
          : null;
        const openAIToolCalls = toolCallsFromOpenAIMessage(message, nameMapper);
        if (openAIToolCalls.length > 0 || choice?.finish_reason === 'tool_use') {
          yield { event: 'tool_calls', data: { toolCalls: openAIToolCalls, meta } };
          return;
        }
        if (Array.isArray(message?.content)) {
          const blocks = message.content.filter((item: any) => item && typeof item === 'object').map((item: any) => ({ ...item }));
          const answer = textFromContentBlocks(blocks);
          yield { event: 'final', data: { answer: answer || '(No response)', meta } };
          return;
        }
        if (typeof message?.content === 'string') {
          yield { event: 'final', data: { answer: message.content || '(No response)', meta } };
          return;
        }
        yield { event: 'final', data: { answer: rawText || '(No response)', meta } };
        return;
      }

      let accumulatedText = '';
      const toolCalls: ToolCall[] = [];
      const toolUseInputsByIndex = new Map<number, { id: string; name: string; inputJson: string }>();
      const contentBlocksByIndex = new Map<number, Record<string, any>>();
      let compactionBlockIndex: number | null = null;

      let requestId: string | null = null;
      let inputTokens: number | null = null;
      let outputTokens: number | null = null;
      let totalTokens: number | null = null;

      const traceId = uuid('trace');
      let thinkingActive = false;

      let stopReason: string | null = null;

      for await (const msg of parseSSE(response.body, { signal: controller.signal })) {
        const raw = msg.data?.trim();
        if (!raw) continue;

        let env: AnthropicStreamEnvelope;
        try {
          env = JSON.parse(raw) as AnthropicStreamEnvelope;
        } catch {
          continue;
        }

        const type = msg.event || env.type || '';

        if (type === 'message_start') {
          const message = env.message;
          if (message && typeof message === 'object' && typeof message.id === 'string') {
            requestId = message.id;
          }
          const usage = message?.usage;
          if (usage && typeof usage === 'object') {
            inputTokens = coerceInt(usage.input_tokens);
            outputTokens = coerceInt(usage.output_tokens);
            totalTokens = coerceInt(usage.total_tokens);
          }
          continue;
        }

        if (type === 'content_block_start') {
          const index = typeof env.index === 'number' ? env.index : null;
          const block = env.content_block;
          if (index == null || !block || typeof block !== 'object') continue;
          const copied = { ...block };
          contentBlocksByIndex.set(index, copied);
          if (block.type === 'tool_use' || block.type === 'server_tool_use') {
            toolUseInputsByIndex.set(index, {
              id: String(block.id || uuid('call')),
              name: String(block.name || ''),
              inputJson: '',
            });
          } else if (block.type === 'compaction') {
            compactionBlockIndex = index;
            yield { event: 'compaction_start', data: {} } as StreamEvent;
          }
          continue;
        }

        if (type === 'content_block_delta') {
          const index = typeof env.index === 'number' ? env.index : null;
          const delta = env.delta;
          if (!delta || typeof delta !== 'object') continue;

          if (delta.type === 'compaction_delta') {
            const content = typeof delta.content === 'string' ? delta.content : '';
            if (content) {
              yield { event: 'compaction_delta', data: { content } } as StreamEvent;
            }
            continue;
          }

          if (delta.type === 'text_delta') {
            const text = typeof delta.text === 'string' ? delta.text : '';
            if (text) {
              const existing = contentBlocksByIndex.get(index ?? -1) || { type: 'text', text: '' };
              existing.type = 'text';
              existing.text = `${typeof existing.text === 'string' ? existing.text : ''}${text}`;
              if (index != null) contentBlocksByIndex.set(index, existing);
              accumulatedText += text;
              yield { event: 'text_delta', data: { content: text } };
            }
            continue;
          }

          if (delta.type === 'thinking_delta') {
            const thinking = typeof delta.thinking === 'string' ? delta.thinking : '';
            if (!thinking) continue;
            if (!thinkingActive) {
              thinkingActive = true;
              yield { event: 'thinking_start', data: { traceId } };
            }
            yield { event: 'thinking_delta', data: { content: thinking } };
            continue;
          }

          if (delta.type === 'input_json_delta') {
            if (index == null) continue;
            const entry = toolUseInputsByIndex.get(index);
            const partial = typeof delta.partial_json === 'string' ? delta.partial_json : '';
            if (entry && partial) entry.inputJson += partial;
            continue;
          }
          continue;
        }

        if (type === 'content_block_stop') {
          const index = typeof env.index === 'number' ? env.index : null;
          if (index == null) continue;
          if (compactionBlockIndex !== null && index === compactionBlockIndex) {
            yield { event: 'compaction_end', data: {} } as StreamEvent;
            compactionBlockIndex = null;
            continue;
          }
          const entry = toolUseInputsByIndex.get(index);
          if (!entry) continue;
          const block = contentBlocksByIndex.get(index) || {};
          if (entry.id) block.id = entry.id;
          if (entry.name) block.name = entry.name;

          const providerName = entry.name;
          const canonicalName = nameMapper.canonicalName(providerName);
          const argsText = entry.inputJson || '{}';
          let args: Record<string, unknown> = {};
          try {
            const parsed = JSON.parse(argsText || '{}');
            if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) args = parsed;
          } catch {
            args = {};
          }
          if (block.type === 'tool_use' && Object.keys(args).length > 0) block.input = args;
          if (block.type === 'tool_use') {
            toolCalls.push({
              id: entry.id || uuid('call'),
              name: canonicalName,
              args,
              argsText,
            });
          }
          continue;
        }

        if (type === 'message_delta') {
          const delta = env.delta;
          if (delta && typeof delta === 'object') {
            if (typeof delta.stop_reason === 'string') stopReason = delta.stop_reason;
          }
          const usage = env.usage;
          if (usage && typeof usage === 'object') {
            // Anthropic emits incremental output token usage in deltas.
            outputTokens = coerceInt(usage.output_tokens) ?? outputTokens;
            inputTokens = coerceInt(usage.input_tokens) ?? inputTokens;
          }
          continue;
        }

        if (type === 'message_stop') {
          break;
        }

        if (type === 'error') {
          const err = env.error;
          const msgText =
            (err && typeof err === 'object' && (err.message || err.error?.message)) ? String(err.message || err.error?.message) : 'Anthropic stream error';
          yield { event: 'error', data: { code: 'PROVIDER_ERROR', message: msgText, retryable: true, meta: metaBase } };
          return;
        }
      }

      if (thinkingActive) {
        yield { event: 'thinking_end', data: {} };
      }

      const meta: ResponseMeta = {
        providerId: 'anthropic',
        model: request.model || this.model,
        requestId,
        inputTokens,
        outputTokens,
        totalTokens,
      };

      if (toolCalls.length > 0 || stopReason === 'tool_use') {
        yield { event: 'tool_calls', data: { toolCalls, meta } };
        return;
      }

      const finalBlocks = orderedContentBlocks(contentBlocksByIndex);
      const finalAnswer = accumulatedText || textFromContentBlocks(finalBlocks) || '(No response)';
      yield { event: 'final', data: { answer: finalAnswer, meta } };
    } catch (err: any) {
      if (controller.signal.aborted) {
        yield { event: 'error', data: { code: 'ABORTED', message: 'Request aborted', retryable: false, meta: metaBase } };
        return;
      }
      yield {
        event: 'error',
        data: { code: 'PROVIDER_ERROR', message: String(err?.message || err), retryable: true, meta: metaBase },
      };
    } finally {
      outerSignal?.removeEventListener('abort', onAbort);
    }
  }
}
