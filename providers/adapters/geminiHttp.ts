import type {
  ChatRequest,
  Provider,
  ProviderId,
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

function normalizeBaseUrl(raw: string): string {
  return raw.trim().replace(/\/+$/, '');
}

function coerceInt(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value)) return Math.max(0, Math.trunc(value));
  if (typeof value === 'string' && /^\d+$/.test(value)) return Math.max(0, Number(value));
  return null;
}

function toolsToGeminiFunctionDeclarations(tools: any[], nameMapper: ToolNameMapper): any[] {
  return tools.map((tool) => ({
    name: nameMapper.providerName(tool.function.name),
    description: tool.function.description || '',
    parameters: normalizeToolParameters(tool.function.parameters),
  }));
}

function parseToolResponse(content: string | null): Record<string, unknown> {
  const raw = (content ?? '').trim();
  if (!raw) return {};
  try {
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) return parsed as Record<string, unknown>;
    return { result: parsed as any };
  } catch {
    return { result: raw };
  }
}

function messagesToGeminiContents(request: ChatRequest, nameMapper: ToolNameMapper): any[] {
  const contents: any[] = [];

  for (const msg of request.messages) {
    if (msg.role === Role.System) continue;

    if (msg.role === Role.User) {
      const text = safeTruncate(msg.content ?? '');
      if (!text) continue;
      contents.push({ role: 'user', parts: [{ text }] });
      continue;
    }

    if (msg.role === Role.Assistant) {
      const parts: any[] = [];
      const text = msg.content ? safeTruncate(msg.content) : '';
      if (text) parts.push({ text });
      for (const call of msg.tool_calls || []) {
        parts.push({
          functionCall: {
            name: nameMapper.providerName(call.name),
            args: call.args || {},
          },
        });
      }
      if (parts.length > 0) contents.push({ role: 'model', parts });
      continue;
    }

    if (msg.role === Role.Tool) {
      const toolName = msg.name;
      if (!toolName) continue;
      contents.push({
        role: 'tool',
        parts: [
          {
            functionResponse: {
              name: nameMapper.providerName(toolName),
              response: parseToolResponse(msg.content),
            },
          },
        ],
      });
    }
  }

  return contents;
}

export class GeminiHttpProvider implements Provider {
  public readonly id: ProviderId = 'gemini';
  public readonly model: string;
  private readonly baseUrl: string;

  constructor(opts: { model: string; baseUrl?: string | null }) {
    this.model = opts.model;
    this.baseUrl = normalizeBaseUrl(opts.baseUrl || 'https://generativelanguage.googleapis.com');
  }

  async *streamChat(request: ChatRequest, opts?: { signal?: AbortSignal }): AsyncIterable<StreamEvent> {
    const nameMapper = new ToolNameMapper();

    const model = request.model || this.model;
    const url = `${this.baseUrl}/v1beta/models/${encodeURIComponent(model)}:streamGenerateContent?alt=sse`;

    const metaBase: ResponseMeta = { providerId: 'gemini', model };

    const tools = request.tools && request.tools.length > 0 ? toolsToGeminiFunctionDeclarations(request.tools as any[], nameMapper) : [];
    const contents = messagesToGeminiContents(request, nameMapper);

    const body: Record<string, any> = {
      contents,
    };

    const systemPrompt = request.systemPrompt.trim();
    if (systemPrompt) {
      body.systemInstruction = { parts: [{ text: systemPrompt }] };
    }

    if (tools.length > 0) {
      body.tools = [{ functionDeclarations: tools }];
      body.toolConfig = { functionCallingConfig: { mode: 'AUTO' } };
    }

    const controller = new AbortController();
    const outerSignal = opts?.signal;
    const onAbort = () => controller.abort();
    outerSignal?.addEventListener('abort', onAbort, { once: true });

    try {
      const headers: Record<string, string> = { 'Content-Type': 'application/json' };

      const response = await proxyFetch(url, {
        method: 'POST',
        headers,
        body: JSON.stringify(body),
        signal: controller.signal,
      }, { providerId: 'gemini' });

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
        yield { event: 'error', data: { code: 'PROVIDER_ERROR', message: 'Missing response body', retryable: true, meta: metaBase } };
        return;
      }

      let accumulatedText = '';
      const toolCalls: ToolCall[] = [];
      let inputTokens: number | null = null;
      let outputTokens: number | null = null;
      let totalTokens: number | null = null;

      for await (const msg of parseSSE(response.body, { signal: controller.signal })) {
        const raw = msg.data?.trim();
        if (!raw) continue;
        if (raw === '[DONE]') break;

        let payload: any;
        try {
          payload = JSON.parse(raw);
        } catch {
          continue;
        }

        const usage = payload.usageMetadata;
        if (usage && typeof usage === 'object') {
          inputTokens = coerceInt(usage.promptTokenCount) ?? inputTokens;
          outputTokens = coerceInt(usage.candidatesTokenCount) ?? outputTokens;
          totalTokens = coerceInt(usage.totalTokenCount) ?? totalTokens;
        }

        const candidates = payload.candidates;
        if (!Array.isArray(candidates) || candidates.length === 0) continue;
        const candidate = candidates[0];
        const content = candidate?.content;
        const parts = content?.parts;
        if (!Array.isArray(parts)) continue;

        for (const part of parts) {
          if (!part || typeof part !== 'object') continue;
          if (typeof part.text === 'string' && part.text) {
            accumulatedText += part.text;
            yield { event: 'text_delta', data: { content: part.text } };
            continue;
          }
          const fc = part.functionCall;
          if (fc && typeof fc === 'object' && typeof fc.name === 'string' && fc.name) {
            const providerName = fc.name;
            const canonicalName = nameMapper.canonicalName(providerName);
            let args: Record<string, unknown> = {};
            if (fc.args && typeof fc.args === 'object') {
              args = fc.args as Record<string, unknown>;
            } else if (typeof fc.args === 'string') {
              try {
                const parsed = JSON.parse(fc.args);
                if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) args = parsed;
              } catch {
                args = {};
              }
            }
            toolCalls.push({
              id: uuid('call'),
              name: canonicalName,
              args,
            });
          }
        }
      }

      const meta: ResponseMeta = {
        providerId: 'gemini',
        model,
        inputTokens,
        outputTokens,
        totalTokens,
      };

      if (toolCalls.length > 0) {
        yield { event: 'tool_calls', data: { toolCalls, meta } };
        return;
      }

      yield { event: 'final', data: { answer: accumulatedText || '(No response)', meta } };
    } catch (err: any) {
      if (controller.signal.aborted) {
        yield { event: 'error', data: { code: 'ABORTED', message: 'Request aborted', retryable: false, meta: metaBase } };
        return;
      }
      yield { event: 'error', data: { code: 'PROVIDER_ERROR', message: String(err?.message || err), retryable: true, meta: metaBase } };
    } finally {
      outerSignal?.removeEventListener('abort', onAbort);
    }
  }
}
