import type { Artifact, ToolCall, ToolResultEnvelope } from '@/types';
import { isQueryRunToolName } from '@/lib/queryProviderRegistry';
import { getQueryCardStateKey } from '@/lib/queryCardStateKey';

export interface ThinkingStepSqlResultState {
  envelope: ToolResultEnvelope;
  artifact?: Artifact;
  resultMessageId?: string;
  resultMessageSeq?: number;
  resultMessageIdentityKey?: string;
}

export interface ThinkingStepToolResultState {
  envelope?: ToolResultEnvelope;
  artifact?: Artifact;
  rawContent?: string;
}

interface DeriveThinkingStepIsActiveInput {
  isStreaming: boolean;
  isLatestAssistant: boolean;
  isLatestVisibleStep: boolean;
  toolCalls: ToolCall[];
  messageIdentityKey: string;
  sqlToolResultsByScopedKey?: Map<string, ThinkingStepSqlResultState>;
  toolResultsByCallId?: Map<string, ThinkingStepToolResultState>;
}

export function deriveThinkingStepIsActive(input: DeriveThinkingStepIsActiveInput): boolean {
  if (!input.isLatestAssistant) return false;
  if (!input.isLatestVisibleStep) return false;
  if (input.isStreaming) return true;
  if (!Array.isArray(input.toolCalls) || input.toolCalls.length === 0) return false;

  for (const toolCall of input.toolCalls) {
    const hasStarted = typeof (toolCall as any)._uiStartedAt === 'number';
    if (!hasStarted) continue;

    if (isQueryRunToolName(toolCall.name)) {
      const scopedKey = getQueryCardStateKey({
        runId: (toolCall as any)._uiRunId,
        messageIdentityKey: input.messageIdentityKey,
        toolCallId: toolCall.id,
      });
      if (!(scopedKey ? input.sqlToolResultsByScopedKey?.has(scopedKey) : false)) return true;
      continue;
    }

    if (!(toolCall.id ? input.toolResultsByCallId?.has(toolCall.id) : false)) return true;
  }

  return false;
}
