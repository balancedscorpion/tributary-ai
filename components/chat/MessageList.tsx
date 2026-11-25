import React from 'react';
import { ScrollArea } from '@/components/ui/scroll-area';
import type { UIMessage, Artifact, ChatFileAttachment, EnsureArtifactLoaded, MessageBlockReference, ThreadReference, ToolProgressEvent, ToolResultEnvelope } from '@/types';
import { Role } from '@/types';
import UserMessage from './UserMessage';
import AssistantMessage from './AssistantMessage';
import RunPlanRow from './RunPlanRow';
import IntermediateThinkingBlock, { type ThinkingStep } from './IntermediateThinkingBlock';
import SteerMessage from './SteerMessage';
import EmptyState from './EmptyState';
import { hasNonCollapsedSelectionWithin } from '@/lib/textSelection';
import { isQueryRunToolName } from '@/lib/queryProviderRegistry';
import { tryParseEnvelope } from '@/lib/toolResultEnvelope';
import { deriveIntermediateFlags } from '@/lib/intermediateThinking';
import {
  buildReferenceForMessage,
  buildStableMessageIdentities,
  getMessageBaseIdentity,
  type MessageIdentity,
} from '@/lib/messageReferences';
import {
  getEnvelopeResultRunId,
  getQueryCardStateKey,
  getRunScopedQueryCardKey,
  normalizeQueryCardToken,
} from '@/lib/queryCardStateKey';

function normalizeStringArray(...values: unknown[]): string[] {
  const out: string[] = [];
  const seen = new Set<string>();

  for (const value of values) {
    if (!Array.isArray(value)) continue;
    for (const item of value) {
      if (typeof item !== 'string') continue;
      const trimmed = item.trim();
      if (!trimmed || seen.has(trimmed)) continue;
      seen.add(trimmed);
      out.push(trimmed);
    }
  }

  return out;
}

function normalizeThreadReference(raw: unknown): ThreadReference | null {
  if (!raw || typeof raw !== 'object') return null;
  const id = String((raw as any).id || '').trim();
  if (!id) return null;
  const title = String((raw as any).title || '').trim() || 'Untitled thread';
  const summary = typeof (raw as any).summary === 'string' && (raw as any).summary.trim()
    ? (raw as any).summary.trim()
    : undefined;
  const updatedAtRaw = Number((raw as any).updatedAt ?? (raw as any).updated_at ?? NaN);
  const status = typeof (raw as any).status === 'string' && (raw as any).status.trim()
    ? (raw as any).status.trim()
    : undefined;
  return {
    id,
    title,
    summary,
    updatedAt: Number.isFinite(updatedAtRaw) ? updatedAtRaw : undefined,
    status,
  };
}

interface SqlAssistantToolCallEntry {
  messageIndex: number;
  callId: string;
  scopedKey: string;
}

interface SqlToolResultState {
  envelope: ToolResultEnvelope;
  artifact?: Artifact;
  resultMessageId?: string;
  resultMessageSeq?: number;
  resultMessageIdentityKey?: string;
}

function getMessageRenderKey(
  message: UIMessage,
  identity: MessageIdentity,
  messageReference: MessageBlockReference | null,
): string {
  if (messageReference?.id) return messageReference.id;
  return `message:${identity.key}:row:${message.role}:${message.uiKind || 'normal'}`;
}

interface MessageListProps {
  messages: UIMessage[];
  artifactsById: Record<string, Artifact>;
  ensureArtifactLoaded?: EnsureArtifactLoaded;
  threadReferencesById?: Record<string, ThreadReference>;
  jiraBaseUrl?: string;
  isLoading?: boolean;
  bottomRef: React.RefObject<HTMLDivElement | null>;
  scrollViewportRef?: React.MutableRefObject<HTMLDivElement | null>;
  onOpenArtifact: (artifact: Artifact) => void;
  onPinArtifact?: (artifact: Artifact) => void;
  pinnedArtifactIds?: string[];
  onToggleMessageReference?: (reference: MessageBlockReference) => void;
  pinnedMessageReferenceIds?: string[];
  onToggleThreadReference?: (reference: ThreadReference) => void;
  pinnedThreadReferenceIds?: string[];
  toolProgressByKey?: Record<string, ToolProgressEvent>;
}

const MessageList: React.FC<MessageListProps> = ({
  messages,
  artifactsById,
  ensureArtifactLoaded,
  threadReferencesById,
  jiraBaseUrl,
  isLoading,
  bottomRef,
  scrollViewportRef,
  onOpenArtifact,
  onPinArtifact,
  pinnedArtifactIds,
  onToggleMessageReference,
  pinnedMessageReferenceIds,
  onToggleThreadReference,
  pinnedThreadReferenceIds,
  toolProgressByKey,
}) => {
  const [viewportEl, setViewportEl] = React.useState<HTMLDivElement | null>(null);
  const shouldAutoScrollRef = React.useRef(true);
  const setScrollViewport = React.useCallback((node: HTMLDivElement | null) => {
    setViewportEl(node);
    if (scrollViewportRef) scrollViewportRef.current = node;
  }, [scrollViewportRef]);
  const pinnedMessageReferenceIdSet = React.useMemo(
    () => new Set((pinnedMessageReferenceIds || []).map((id) => String(id || '').trim()).filter(Boolean)),
    [pinnedMessageReferenceIds],
  );
  const pinnedThreadReferenceIdSet = React.useMemo(
    () => new Set((pinnedThreadReferenceIds || []).map((id) => String(id || '').trim()).filter(Boolean)),
    [pinnedThreadReferenceIds],
  );
  const messageIdentities = React.useMemo(
    () => buildStableMessageIdentities(messages),
    [messages],
  );
  const messageReferences = React.useMemo(
    () => messages.map((message, index) => buildReferenceForMessage(message, messageIdentities[index] || getMessageBaseIdentity(message))),
    [messages, messageIdentities],
  );
  // Build a lookup from SQL run_*_query calls to their latest result envelope/artifact.
  // get_query_results messages are absorbed into the owning run_*_query card by artifact_id.
  const { sqlToolResultsByScopedKey } = React.useMemo(() => {
    const assistantByScopedKey = new Map<string, SqlAssistantToolCallEntry>();
    const assistantByCallId = new Map<string, SqlAssistantToolCallEntry[]>();

    for (let index = 0; index < messages.length; index += 1) {
      const msg = messages[index];
      if (msg.role !== Role.Assistant) continue;

      const identity = messageIdentities[index] || getMessageBaseIdentity(msg);
      const messageIdentityKey = identity.key;
      const toolCalls = Array.isArray(msg.tool_calls) ? msg.tool_calls : [];

      for (const toolCall of toolCalls) {
        if (!isQueryRunToolName(toolCall?.name || '')) continue;
        const callId = normalizeQueryCardToken(toolCall?.id);
        if (!callId) continue;

        const scopedKey = getQueryCardStateKey({
          runId: (toolCall as any)._uiRunId,
          messageIdentityKey,
          toolCallId: callId,
        });
        if (!scopedKey) continue;

        const entry: SqlAssistantToolCallEntry = {
          messageIndex: index,
          callId,
          scopedKey,
        };
        assistantByScopedKey.set(scopedKey, entry);

        const entries = assistantByCallId.get(callId) || [];
        entries.push(entry);
        assistantByCallId.set(callId, entries);
      }
    }

    const resultMap = new Map<string, SqlToolResultState>();
    const ownedIds = new Set<string>();
    const owningScopedKeyByArtifactId = new Map<string, string>();

    for (let index = 0; index < messages.length; index += 1) {
      const msg = messages[index];
      if (msg.role !== Role.Tool) continue;

      const envelope = tryParseEnvelope(msg.content);
      if (!envelope) continue;

      if (isQueryRunToolName(msg.name || '')) {
        const callId = normalizeQueryCardToken(envelope.tool_call_id || (msg as any).tool_call_id);
        if (!callId) continue;

        const runId = normalizeQueryCardToken((msg as any)._uiRunId) || getEnvelopeResultRunId(envelope);
        const runScopedKey = getRunScopedQueryCardKey(runId, callId);

        let matchedEntry = runScopedKey ? assistantByScopedKey.get(runScopedKey) : undefined;
        if (!matchedEntry) {
          const candidates = assistantByCallId.get(callId) || [];
          for (let i = candidates.length - 1; i >= 0; i -= 1) {
            const candidate = candidates[i];
            if (candidate.messageIndex <= index) {
              matchedEntry = candidate;
              break;
            }
          }
        }
        if (!matchedEntry) continue;

        const art = artifactsById[envelope.artifact_id];
        const identity = messageIdentities[index] || getMessageBaseIdentity(msg);
        resultMap.set(matchedEntry.scopedKey, {
          envelope,
          artifact: art,
          resultMessageId: identity.messageId,
          resultMessageSeq: identity.messageSeq,
          resultMessageIdentityKey: identity.key,
        });
        if (envelope.artifact_id) {
          ownedIds.add(envelope.artifact_id);
          owningScopedKeyByArtifactId.set(envelope.artifact_id, matchedEntry.scopedKey);
        }
        continue;
      }

      if (msg.name === 'get_query_results') {
        const artifactId = normalizeQueryCardToken(envelope.artifact_id);
        if (!artifactId || !ownedIds.has(artifactId)) continue;
        const owningScopedKey = owningScopedKeyByArtifactId.get(artifactId);
        if (!owningScopedKey) continue;

        const art = artifactsById[artifactId];
        const identity = messageIdentities[index] || getMessageBaseIdentity(msg);
        resultMap.set(owningScopedKey, {
          envelope,
          artifact: art,
          resultMessageId: identity.messageId,
          resultMessageSeq: identity.messageSeq,
          resultMessageIdentityKey: identity.key,
        });
      }
    }

    return {
      sqlToolResultsByScopedKey: resultMap,
    };
  }, [artifactsById, messageIdentities, messages]);

  // Build a lookup from tool_call_id → { envelope, artifact, fileAttachments } for ALL tool results.
  // This lets CollapsedToolsSummary make every tool row clickable.
  const toolResultsByCallId = React.useMemo(() => {
    const map = new Map<string, { envelope?: ToolResultEnvelope; artifact?: Artifact; rawContent?: string; fileAttachments?: ChatFileAttachment[] }>();
    for (const msg of messages) {
      if (msg.role !== Role.Tool) continue;
      const callId = ((msg as any).tool_call_id || '').trim();
      if (!callId) continue;
      const envelope = tryParseEnvelope(msg.content);
      const artifact = envelope?.artifact_id ? artifactsById[envelope.artifact_id] : undefined;
      map.set(callId, {
        envelope: envelope || undefined,
        artifact,
        rawContent: msg.content || undefined,
        fileAttachments: Array.isArray(msg.fileAttachments) ? msg.fileAttachments : undefined,
      });
    }
    return map;
  }, [messages, artifactsById]);

  const referencedMessageReferencesByIndex = React.useMemo(() => {
    const resolvedByIndex = new Map<number, MessageBlockReference[]>();
    const priorReferencesByMessageId = new Map<string, MessageBlockReference>();

    for (let index = 0; index < messages.length; index += 1) {
      const msg = messages[index];

      const referencedMessageIds = normalizeStringArray(
        msg.referencedMessageIds,
        msg.referenced_message_ids,
      );
      if (referencedMessageIds.length > 0) {
        const references: MessageBlockReference[] = [];
        const seenReferenceIds = new Set<string>();

        for (const referencedMessageId of referencedMessageIds) {
          const reference = priorReferencesByMessageId.get(referencedMessageId);
          if (!reference || seenReferenceIds.has(reference.id)) continue;
          seenReferenceIds.add(reference.id);
          references.push(reference);
        }

        if (references.length > 0) {
          resolvedByIndex.set(index, references);
        }
      }

      const messageId = messageIdentities[index]?.messageId;
      const messageReference = messageReferences[index];
      if (messageId && messageReference) {
        priorReferencesByMessageId.set(messageId, messageReference);
      }
    }

    return resolvedByIndex;
  }, [messages, messageIdentities, messageReferences]);

  const referencedThreadReferencesByIndex = React.useMemo(() => {
    const resolvedByIndex = new Map<number, ThreadReference[]>();

    for (let index = 0; index < messages.length; index += 1) {
      const msg = messages[index];
      const explicitReferences = [
        ...(Array.isArray(msg.referencedThreads) ? msg.referencedThreads : []),
        ...(Array.isArray(msg.referenced_threads) ? msg.referenced_threads : []),
      ]
        .map(normalizeThreadReference)
        .filter(Boolean) as ThreadReference[];
      const explicitById = new Map<string, ThreadReference>();
      for (const reference of explicitReferences) {
        explicitById.set(reference.id, reference);
      }

      const referencedThreadIds = normalizeStringArray(
        msg.referencedThreadIds,
        msg.referenced_thread_ids,
      );
      if (referencedThreadIds.length === 0 && explicitById.size === 0) {
        continue;
      }

      const references: ThreadReference[] = [];
      const seen = new Set<string>();
      const orderedIDs = referencedThreadIds.length > 0
        ? referencedThreadIds
        : Array.from(explicitById.keys());

      for (const referencedThreadId of orderedIDs) {
        if (seen.has(referencedThreadId)) continue;
        const explicit = explicitById.get(referencedThreadId);
        const fallback = normalizeThreadReference(threadReferencesById?.[referencedThreadId]);
        const reference = explicit || fallback || {
          id: referencedThreadId,
          title: 'Referenced thread',
        };
        seen.add(referencedThreadId);
        references.push(reference);
      }

      if (references.length > 0) {
        resolvedByIndex.set(index, references);
      }
    }

    return resolvedByIndex;
  }, [messages, threadReferencesById]);

  const updateAutoScroll = React.useCallback(() => {
    if (!viewportEl) return;
    const thresholdPx = 80;
    const distanceFromBottom = viewportEl.scrollHeight - viewportEl.scrollTop - viewportEl.clientHeight;
    shouldAutoScrollRef.current = distanceFromBottom <= thresholdPx;
  }, [viewportEl]);

  React.useEffect(() => {
    if (!viewportEl) return;
    updateAutoScroll();

    const onScroll = () => updateAutoScroll();
    viewportEl.addEventListener('scroll', onScroll, { passive: true });
    return () => viewportEl.removeEventListener('scroll', onScroll);
  }, [viewportEl, updateAutoScroll]);

  // Only auto-scroll if the user is already near the bottom AND they aren't actively selecting text.
  // This makes it possible to highlight/copy text even while the assistant is streaming updates.
  React.useEffect(() => {
    if (!viewportEl) return;
    if (!shouldAutoScrollRef.current) return;
    if (hasNonCollapsedSelectionWithin(viewportEl)) return;
    viewportEl.scrollTo({
      top: viewportEl.scrollHeight,
      behavior: isLoading ? 'auto' : 'smooth',
    });
  }, [messages, isLoading, viewportEl]);

  // -----------------------------------------------------------------------
  // Classify assistant messages: "intermediate" if a later assistant
  // message exists before the next user message (i.e. not the final reply).
  // Each intermediate message renders as its own compact thinking block;
  // only the final assistant message gets the full avatar + label treatment.
  // -----------------------------------------------------------------------
  const intermediateFlags = React.useMemo(() => deriveIntermediateFlags(messages), [messages]);
  const lastAssistantIndex = React.useMemo(() => {
    for (let idx = messages.length - 1; idx >= 0; idx -= 1) {
      if (messages[idx]?.role === Role.Assistant) return idx;
    }
    return -1;
  }, [messages]);

  return (
    <ScrollArea
      className="h-full w-full min-w-0 max-w-full"
      viewportRef={setScrollViewport}
      viewportClassName="overflow-x-hidden overscroll-contain"
    >
      <div className="p-4 overflow-x-hidden max-w-full min-w-0" style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}>
        {messages.length === 0 && <EmptyState />}

        {(() => {
          const elements: React.ReactNode[] = [];
          let i = 0;
          while (i < messages.length) {
            const msg = messages[i];

            // First-class run-plan rows (plan §4.6) — checked before the System
            // skip because they ride on System role so legacy paths ignore them.
            if (msg.uiKind === 'capability_plan' && msg.capabilityPlan) {
              elements.push(
                <RunPlanRow
                  key={msg._uiStableId || `capability-plan-${i}`}
                  plan={msg.capabilityPlan}
                />,
              );
              i++;
              continue;
            }

            if (msg.role === Role.System || msg.role === Role.Tool) {
              i++;
              continue;
            }

            const messageIdentity =
              messageIdentities[i] || getMessageBaseIdentity(msg);
            const messageReference = messageReferences[i];
            const referencedMessages =
              referencedMessageReferencesByIndex.get(i) || [];
            const referencedThreads =
              referencedThreadReferencesByIndex.get(i) || [];
            const renderKey = getMessageRenderKey(
              msg,
              messageIdentity,
              messageReference,
            );

            if (msg.role === Role.User) {
              if (msg.uiKind === 'steer') {
                const steerIds = normalizeStringArray(
                  msg.referencedArtifactIds,
                  msg.referenced_artifact_ids,
                );
                const steerArtifacts = steerIds
                  .map((id) => artifactsById[id])
                  .filter(Boolean) as Artifact[];
                elements.push(
                  <SteerMessage
                    key={renderKey}
                    content={msg.content}
                    referencedArtifacts={steerArtifacts}
                    fileAttachments={msg.fileAttachments}
                    referencedMessages={referencedMessages}
                    referencedThreads={referencedThreads}
                    onOpenArtifact={onOpenArtifact}
                    messageReference={messageReference || undefined}
                    isMessageReferenced={
                      messageReference
                        ? pinnedMessageReferenceIdSet.has(messageReference.id)
                        : false
                    }
                    pinnedMessageReferenceIdSet={pinnedMessageReferenceIdSet}
                    onToggleMessageReference={onToggleMessageReference}
                    pinnedThreadReferenceIdSet={pinnedThreadReferenceIdSet}
                    onToggleThreadReference={onToggleThreadReference}
                  />,
                );
              } else {
                const ids = normalizeStringArray(
                  msg.referencedArtifactIds,
                  msg.referenced_artifact_ids,
                );
                const referencedArtifacts = ids
                  .map((id) => artifactsById[id])
                  .filter(Boolean) as Artifact[];
                elements.push(
                  <UserMessage
                    key={renderKey}
                    content={msg.content}
                    referencedArtifacts={referencedArtifacts}
                    fileAttachments={msg.fileAttachments}
                    referencedMessages={referencedMessages}
                    referencedThreads={referencedThreads}
                    onOpenArtifact={onOpenArtifact}
                    messageReference={messageReference || undefined}
                    isMessageReferenced={
                      messageReference
                        ? pinnedMessageReferenceIdSet.has(messageReference.id)
                        : false
                    }
                    pinnedMessageReferenceIdSet={pinnedMessageReferenceIdSet}
                    onToggleMessageReference={onToggleMessageReference}
                    pinnedThreadReferenceIdSet={pinnedThreadReferenceIdSet}
                    onToggleThreadReference={onToggleThreadReference}
                  />,
                );
              }
              i++;
              continue;
            }

            if (msg.role === Role.Assistant && intermediateFlags[i]) {
              // Collect consecutive intermediate messages into a group
              const steps: ThinkingStep[] = [];
              const groupStartIndex = i;
              // Plan rows ride on System role but are first-class UI (§4.6):
              // when the skip below passes one, render it after the block
              // instead of silently swallowing it.
              const passedPlanRows: UIMessage[] = [];
              while (
                i < messages.length &&
                intermediateFlags[i] &&
                messages[i].role === Role.Assistant
              ) {
                const stepIdentity =
                  messageIdentities[i] || getMessageBaseIdentity(messages[i]);
                steps.push({
                  message: messages[i] as UIMessage,
                  messageIdentityKey: stepIdentity.key,
                    referencedMessages:
                      referencedMessageReferencesByIndex.get(i) || [],
                    referencedThreads:
                      referencedThreadReferencesByIndex.get(i) || [],
                    isLastMessage: i === lastAssistantIndex,
                  });
                i++;
                // Skip intervening System/Tool messages within the run
                while (
                  i < messages.length &&
                  (messages[i].role === Role.System ||
                    messages[i].role === Role.Tool)
                ) {
                  const skipped = messages[i];
                  if (skipped.uiKind === 'capability_plan' && skipped.capabilityPlan) {
                    passedPlanRows.push(skipped);
                  }
                  i++;
                }
              }
              elements.push(
                <IntermediateThinkingBlock
                  key={`thinking-stack-${groupStartIndex}`}
                  steps={steps}
                  isThreadRunActive={isLoading}
                  onOpenArtifact={onOpenArtifact}
                  onPinArtifact={onPinArtifact}
                  pinnedArtifactIds={pinnedArtifactIds}
                  onToggleMessageReference={onToggleMessageReference}
                  pinnedMessageReferenceIds={pinnedMessageReferenceIds}
                  onToggleThreadReference={onToggleThreadReference}
                  pinnedThreadReferenceIds={pinnedThreadReferenceIds}
                  toolProgressByKey={toolProgressByKey}
                  sqlToolResultsByScopedKey={sqlToolResultsByScopedKey}
                  artifactsById={artifactsById}
                  ensureArtifactLoaded={ensureArtifactLoaded}
                  toolResultsByCallId={toolResultsByCallId}
                  jiraBaseUrl={jiraBaseUrl}
                />,
              );
              for (const planMsg of passedPlanRows) {
                elements.push(
                  <RunPlanRow
                    key={planMsg._uiStableId || `capability-plan-${planMsg.capabilityPlan!.planId}`}
                    plan={planMsg.capabilityPlan!}
                  />,
                );
              }
              continue;
            }

            if (msg.role === Role.Assistant) {
              // Final assistant message → full rendering
              elements.push(
                <AssistantMessage
                  key={renderKey}
                  message={msg}
                  isLastMessage={i === lastAssistantIndex}
                  isThreadRunActive={isLoading}
                  onOpenArtifact={onOpenArtifact}
                  onPinArtifact={onPinArtifact}
                  pinnedArtifactIds={pinnedArtifactIds}
                  messageReference={messageReference || undefined}
                  referencedMessages={referencedMessages}
                  referencedThreads={referencedThreads}
                  isMessageReferenced={
                    messageReference
                      ? pinnedMessageReferenceIdSet.has(messageReference.id)
                      : false
                  }
                  pinnedMessageReferenceIds={pinnedMessageReferenceIds}
                  onToggleMessageReference={onToggleMessageReference}
                  pinnedThreadReferenceIds={pinnedThreadReferenceIds}
                  onToggleThreadReference={onToggleThreadReference}
                  toolProgressByKey={toolProgressByKey}
                  sqlToolResultsByScopedKey={sqlToolResultsByScopedKey}
                  messageIdentityKey={messageIdentity.key}
                  artifactsById={artifactsById}
                  ensureArtifactLoaded={ensureArtifactLoaded}
                  toolResultsByCallId={toolResultsByCallId}
                  jiraBaseUrl={jiraBaseUrl}
                />,
              );
              i++;
              continue;
            }

            i++;
          }
          return elements;
        })()}
        <div ref={bottomRef} />
      </div>
    </ScrollArea>
  );
};

export default React.memo(MessageList);
