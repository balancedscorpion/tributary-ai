import type {
  ChatMessage,
  ChatRequest,
  ProviderRuntimeSettings,
  StreamEvent,
  ToolCall,
  ToolDefinition,
} from './types';
import { Role } from './types';
import { createProvider } from './registry';

function safeJson(value: unknown): string {
  try {
    return JSON.stringify(value);
  } catch {
    return '"[unserializable]"';
  }
}

function summarizeToolCalls(calls: ToolCall[]): string {
  if (!calls || calls.length === 0) return '';
  const parts = calls.map((c) => `${c.name}(${safeJson(c.args)})`);
  const joined = parts.join(', ');
  // Keep error messages compact.
  return joined.length > 600 ? `${joined.slice(0, 600)}…` : joined;
}

export interface EngineCallbacks {
  onAssistantStart?: () => void;
  onTextDelta?: (delta: string) => void;
  onThinkingStart?: (traceId: string) => void;
  onThinkingDelta?: (delta: string) => void;
  onThinkingEnd?: (summary?: string) => void;
  onAssistantFinal?: (message: ChatMessage, meta?: Record<string, unknown>) => void;
  onToolResult?: (message: ChatMessage) => void;
  onError?: (message: string) => void;
}

export interface RunAgentLoopParams {
  providerSettings: ProviderRuntimeSettings;
  systemPrompt: string;
  model: string;
  history: ChatMessage[];
  tools: ToolDefinition[];
  executeTool: (call: ToolCall) => Promise<ChatMessage>;
  callbacks?: EngineCallbacks;
  signal?: AbortSignal;
  maxIterations?: number;
}

export async function runAgentLoop(params: RunAgentLoopParams): Promise<ChatMessage[]> {
  const {
    providerSettings,
    systemPrompt,
    model,
    tools,
    executeTool,
    callbacks,
    signal,
    maxIterations = 200,
  } = params;

  let history = params.history.slice();
  let lastToolCallsSummary: string | null = null;

  for (let iteration = 0; iteration < maxIterations; iteration += 1) {
    if (signal?.aborted) return history;

    let provider;
    try {
      provider = createProvider({
        ...providerSettings,
        provider: { ...providerSettings.provider, model },
      });
    } catch (err: any) {
      const message = `Error: ${String(err?.message || err)}`;
      callbacks?.onAssistantStart?.();
      const assistantMsg: ChatMessage = { role: Role.Assistant, content: message };
      history = history.concat([assistantMsg]);
      callbacks?.onAssistantFinal?.(assistantMsg);
      callbacks?.onError?.(message);
      return history;
    }

    const request: ChatRequest = {
      model,
      systemPrompt,
      messages: history,
      tools,
    };

    callbacks?.onAssistantStart?.();

    let accumulatedText = '';
    let accumulatedToolCalls: ToolCall[] = [];
    let terminal: Extract<StreamEvent, { event: 'tool_calls' | 'final' | 'error' }> | null = null;
    let lastMeta: Record<string, unknown> | undefined;

    try {
      for await (const ev of provider.streamChat(request, { signal })) {
        if (signal?.aborted) {
          // Preserve any partial assistant output already streamed into the UI.
          if (accumulatedText.length > 0) {
            const assistantMsg: ChatMessage = { role: Role.Assistant, content: accumulatedText };
            history = history.concat([assistantMsg]);
            callbacks?.onAssistantFinal?.(assistantMsg, lastMeta);
          }
          return history;
        }

        if (ev.event === 'thinking_start') {
          callbacks?.onThinkingStart?.(ev.data.traceId);
          continue;
        }
        if (ev.event === 'thinking_delta') {
          callbacks?.onThinkingDelta?.(ev.data.content);
          continue;
        }
        if (ev.event === 'thinking_end') {
          callbacks?.onThinkingEnd?.(ev.data.summary);
          continue;
        }
        if (ev.event === 'text_delta') {
          accumulatedText += ev.data.content;
          callbacks?.onTextDelta?.(ev.data.content);
          continue;
        }
        if (ev.event === 'tool_calls') {
          accumulatedToolCalls = ev.data.toolCalls;
          lastToolCallsSummary = summarizeToolCalls(accumulatedToolCalls);
          terminal = ev;
          lastMeta = ev.data.meta as any;
          break;
        }
        if (ev.event === 'final') {
          terminal = ev;
          lastMeta = ev.data.meta as any;
          break;
        }
        if (ev.event === 'error') {
          terminal = ev;
          lastMeta = ev.data.meta as any;
          break;
        }
      }
    } catch (err: any) {
      const message = `Error: ${String(err?.message || err)}`;
      const assistantMsg: ChatMessage = { role: Role.Assistant, content: message };
      history = history.concat([assistantMsg]);
      callbacks?.onAssistantFinal?.(assistantMsg);
      callbacks?.onError?.(message);
      return history;
    }

    if (!terminal) {
      const message = 'Error: Provider stream ended unexpectedly.';
      const assistantMsg: ChatMessage = { role: Role.Assistant, content: message };
      history = history.concat([assistantMsg]);
      callbacks?.onAssistantFinal?.(assistantMsg);
      callbacks?.onError?.(message);
      return history;
    }

    if (terminal.event === 'error') {
      const message = `Error: ${terminal.data.message}`;
      const assistantMsg: ChatMessage = { role: Role.Assistant, content: message };
      history = history.concat([assistantMsg]);
      callbacks?.onAssistantFinal?.(assistantMsg, lastMeta);
      callbacks?.onError?.(message);
      return history;
    }

    if (terminal.event === 'final') {
      const finalToolCalls = terminal.data.toolCalls || [];
      const finalToolResults = terminal.data.toolResults || [];
      const assistantMsg: ChatMessage = {
        role: Role.Assistant,
        content: terminal.data.answer || accumulatedText || null,
        ...(finalToolCalls.length > 0 ? { tool_calls: finalToolCalls } : {}),
      };
      history = history.concat([assistantMsg]);
      callbacks?.onAssistantFinal?.(assistantMsg, lastMeta);
      for (const toolMsg of finalToolResults) {
        history = history.concat([toolMsg]);
        callbacks?.onToolResult?.(toolMsg);
      }
      return history;
    }

    // tool_calls
    const assistantMsg: ChatMessage = {
      role: Role.Assistant,
      content: accumulatedText || null,
      tool_calls: accumulatedToolCalls,
    };
    history = history.concat([assistantMsg]);
    callbacks?.onAssistantFinal?.(assistantMsg, lastMeta);

    for (const call of accumulatedToolCalls) {
      if (signal?.aborted) return history;
      const toolMsg = await executeTool(call);
      history = history.concat([toolMsg]);
      callbacks?.onToolResult?.(toolMsg);
    }
  }

  const message = `Error: Stopped after ${maxIterations} tool iterations (possible loop).`;
  const detail = lastToolCallsSummary ? ` Last tool calls: ${lastToolCallsSummary}` : '';
  const assistantMsg: ChatMessage = { role: Role.Assistant, content: message };
  assistantMsg.content = `${message}${detail}`;
  history = history.concat([assistantMsg]);
  callbacks?.onAssistantFinal?.(assistantMsg);
  callbacks?.onError?.(message);
  return history;
}
