import { Role, type UIMessage } from '@/types';

function hasQueryPollingOnlyToolCalls(toolCalls: unknown): boolean {
  if (!Array.isArray(toolCalls) || toolCalls.length === 0) return false;
  return toolCalls.every((toolCall) => String((toolCall as any)?.name || '').trim() === 'get_query_results');
}

export function isVisibleThinkingStepMessage(message: Pick<UIMessage, 'tool_calls'>): boolean {
  const toolCalls = Array.isArray(message.tool_calls) ? message.tool_calls : [];
  if (toolCalls.length === 0) return true;
  return !hasQueryPollingOnlyToolCalls(toolCalls);
}

function hasVisibleAssistantNarrative(message: UIMessage): boolean {
  const hasVisibleText = typeof message.content === 'string' && message.content.trim().length > 0;
  const hasThinkingText = typeof (message as any).thinking === 'string' && (message as any).thinking.trim().length > 0;
  const hasCompactionSummary = typeof (message as any).compactionSummary === 'string'
    && (message as any).compactionSummary.trim().length > 0;
  const hasCompactionState =
    (message as any).compactionInProgress === true ||
    (message as any).compactionComplete === true;
  return hasVisibleText || hasThinkingText || hasCompactionSummary || hasCompactionState;
}

export function deriveIntermediateFlags(messages: UIMessage[]): boolean[] {
  const flags = new Array(messages.length).fill(false);

  for (let i = 0; i < messages.length; i++) {
    const msg = messages[i];
    if (msg.role !== Role.Assistant) continue;

    const hasCompactionState =
      (msg as any).compactionInProgress === true ||
      (msg as any).compactionComplete === true;
    const hasCompactionSummary = typeof (msg as any).compactionSummary === 'string'
      && (msg as any).compactionSummary.trim().length > 0;
    // Keep compaction UI in the primary assistant renderer so CompactionBlock remains visible.
    if (hasCompactionState || hasCompactionSummary) {
      continue;
    }

    const toolCalls = Array.isArray(msg.tool_calls) ? msg.tool_calls : [];
    const hasVisibleToolCalls = toolCalls.length > 0 && !hasQueryPollingOnlyToolCalls(toolCalls);

    // Streaming assistant turns are always shown in the thinking stack.
    if (msg._uiStreaming === true) {
      flags[i] = true;
      continue;
    }

    // Tooling turns are shown unless they're polling-only checks.
    if (hasVisibleToolCalls) {
      if (!hasVisibleAssistantNarrative(msg)) {
        flags[i] = true;
        continue;
      }
    }

    // Do not surface empty poll/status turns as intermediate traces.
    if (!hasVisibleAssistantNarrative(msg)) {
      continue;
    }

    // Intermediate if another assistant message appears before the next user turn.
    for (let j = i + 1; j < messages.length; j++) {
      if (messages[j].role === Role.User) break;
      if (messages[j].role === Role.Assistant) {
        flags[i] = true;
        break;
      }
    }
  }

  return flags;
}
