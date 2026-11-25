import React from 'react';
import { Avatar, AvatarFallback } from '@/components/ui/avatar';
import type { UIMessage, Artifact, EnsureArtifactLoaded, MessageBlockReference, ThreadReference, ToolProgressEvent, ToolResultEnvelope } from '@/types';
import ThinkingTrace from './ThinkingTrace';
import CompactionBlock from './CompactionBlock';
import FileAttachmentCard from './FileAttachmentCard';
import MessageReferenceButton from './MessageReferenceButton';
import MarkdownRenderer from './MarkdownRenderer';
import CollapsedToolsSummary from './CollapsedToolsSummary';
import ReferencedMessageChips from './ReferencedMessageChips';
import ReferencedThreadChips from './ReferencedThreadChips';
import { canDownloadArtifact, downloadArtifact } from '@/lib/artifactDownload';
import { fileArtifactCardPresentation } from '@/lib/attachmentPresentation';
import { projectGeneratedFileArtifactsFromMarkdown } from '@/lib/generatedFileArtifacts';

interface AssistantMessageProps {
  message: UIMessage;
  isLastMessage?: boolean;
  isThreadRunActive?: boolean;
  onOpenArtifact: (a: Artifact) => void;
  onPinArtifact?: (a: Artifact) => void;
  pinnedArtifactIds?: string[];
  messageReference?: MessageBlockReference;
  referencedMessages?: MessageBlockReference[];
  referencedThreads?: ThreadReference[];
  isMessageReferenced?: boolean;
  pinnedMessageReferenceIds?: string[];
  onToggleMessageReference?: (reference: MessageBlockReference) => void;
  pinnedThreadReferenceIds?: string[];
  onToggleThreadReference?: (reference: ThreadReference) => void;
  toolProgressByKey?: Record<string, ToolProgressEvent>;
  sqlToolResultsByScopedKey?: Map<string, {
    envelope: ToolResultEnvelope;
    artifact?: Artifact;
    resultMessageId?: string;
    resultMessageSeq?: number;
    resultMessageIdentityKey?: string;
  }>;
  messageIdentityKey?: string;
  artifactsById?: Record<string, Artifact>;
  ensureArtifactLoaded?: EnsureArtifactLoaded;
  toolResultsByCallId?: Map<string, { envelope?: ToolResultEnvelope; artifact?: Artifact; rawContent?: string }>;
  jiraBaseUrl?: string;
}

const AssistantMessage: React.FC<AssistantMessageProps> = ({
  message,
  isLastMessage,
  isThreadRunActive,
  onOpenArtifact,
  onPinArtifact,
  pinnedArtifactIds,
  messageReference,
  referencedMessages = [],
  referencedThreads = [],
  isMessageReferenced,
  pinnedMessageReferenceIds,
  onToggleMessageReference,
  pinnedThreadReferenceIds,
  onToggleThreadReference,
  toolProgressByKey,
  sqlToolResultsByScopedKey,
  messageIdentityKey,
  artifactsById,
  ensureArtifactLoaded,
  toolResultsByCallId,
  jiraBaseUrl,
}) => {
  const thinkingTraceStateKey = React.useMemo(() => {
    if (typeof messageIdentityKey === 'string' && messageIdentityKey.trim()) {
      return `assistant-thinking:${messageIdentityKey.trim()}`;
    }
    if (typeof message.id === 'string' && message.id.trim()) {
      return `assistant-thinking:id:${message.id.trim()}`;
    }
    if (typeof message.seq === 'number' && Number.isFinite(message.seq) && message.seq > 0) {
      return `assistant-thinking:seq:${Math.trunc(message.seq)}`;
    }
    return undefined;
  }, [messageIdentityKey, message.id, message.seq]);

  const streamStablePrefix = typeof (message as any).streamStablePrefix === 'string'
    ? (message as any).streamStablePrefix
    : undefined;
  const streamUnstableSuffix = typeof (message as any).streamUnstableSuffix === 'string'
    ? (message as any).streamUnstableSuffix
    : undefined;
  const messageContent = typeof message.content === 'string' ? message.content : '';
  const hasStreamingSplit = Boolean(streamStablePrefix || streamUnstableSuffix);
  const generatedFileProjection = React.useMemo(() => {
    if (message._uiStreaming || hasStreamingSplit) {
      return { markdown: messageContent, items: [] };
    }
    const seed = String(message.id || message.seq || messageIdentityKey || 'assistant');
    return projectGeneratedFileArtifactsFromMarkdown(messageContent, seed);
  }, [hasStreamingSplit, message._uiStreaming, message.seq, message.id, messageContent, messageIdentityKey]);
  const hasGeneratedFileCards = generatedFileProjection.items.length > 0;
  const hasRenderableContent = generatedFileProjection.markdown.length > 0 || hasStreamingSplit;
  const hasCompactionBlock = Boolean(message.compactionInProgress || message.compactionComplete);
  const hasAuxiliaryBlock = Boolean(message.thinking || hasCompactionBlock);
  const pinnedMessageReferenceIdSet = React.useMemo(
    () =>
      new Set(
        (pinnedMessageReferenceIds || [])
          .map((id) => String(id || '').trim())
          .filter(Boolean),
      ),
    [pinnedMessageReferenceIds],
  );
  const pinnedThreadReferenceIdSet = React.useMemo(
    () =>
      new Set(
        (pinnedThreadReferenceIds || [])
          .map((id) => String(id || '').trim())
          .filter(Boolean),
      ),
    [pinnedThreadReferenceIds],
  );

  return (
    <div className="flex w-full justify-start min-w-0">
      <div className={`flex gap-3 w-full max-w-[90%] md:max-w-[85%] flex-row min-w-0 ${message._uiStreaming ? "" : "animate-fade-in"}`}>
        <Avatar className="h-8 w-8 mt-1 flex-shrink-0">
          <AvatarFallback className="bg-avatar-neutral text-white text-[10px] font-bold tracking-tight">AI</AvatarFallback>
        </Avatar>
        <div className="flex flex-col gap-1 min-w-0 w-full items-start">
          <div className="flex items-center gap-1.5 w-full">
            <span className="text-[11px] font-medium text-muted-foreground ml-0.5">AI Analyst</span>
            {messageReference && onToggleMessageReference && (
              <MessageReferenceButton
                isReferenced={isMessageReferenced}
                onToggle={() => onToggleMessageReference(messageReference)}
              />
            )}
          </div>
          <div className={`group/msg w-full max-w-full min-w-0 overflow-hidden text-foreground transition-all ${
            isMessageReferenced ? 'pl-3 border-l-2 border-l-sky-400/60' : ''
          }`}>
            <ReferencedMessageChips
              references={referencedMessages}
              pinnedMessageReferenceIdSet={pinnedMessageReferenceIdSet}
              onToggleMessageReference={onToggleMessageReference}
              className={referencedMessages.length > 0 ? 'mb-2' : undefined}
            />
            <ReferencedThreadChips
              references={referencedThreads}
              pinnedThreadReferenceIdSet={pinnedThreadReferenceIdSet}
              onToggleThreadReference={onToggleThreadReference}
              className={referencedThreads.length > 0 ? 'mb-2' : undefined}
            />
            {/* Thinking Trace */}
            {message.thinking && (
              <ThinkingTrace
                thinking={message.thinking}
                isComplete={message.thinkingComplete ?? false}
                stateKey={thinkingTraceStateKey}
              />
            )}

            {/* Compaction Block */}
            {hasCompactionBlock && (
              <CompactionBlock
                summary={message.compactionSummary ?? ''}
                isComplete={message.compactionComplete ?? false}
                stats={message.compactionStats}
                jiraBaseUrl={jiraBaseUrl}
              />
            )}

            {/* Markdown Content — rendered before tool calls for natural reading order */}
            {hasRenderableContent ? (
              <MarkdownRenderer
                content={generatedFileProjection.markdown}
                stablePrefix={streamStablePrefix}
                unstableSuffix={streamUnstableSuffix}
                jiraBaseUrl={jiraBaseUrl}
              />
            ) : (
              !hasAuxiliaryBlock && !message.tool_calls?.length && !hasGeneratedFileCards && <span className="animate-pulse text-muted-foreground">...</span>
            )}

            {hasGeneratedFileCards && (
              <div className="mt-2 space-y-2" data-testid="assistant-generated-file-cards">
                {generatedFileProjection.items.map(({ key, artifact }) => {
                  const presentation = fileArtifactCardPresentation(artifact);
                  return (
                    <FileAttachmentCard
                      key={key}
                      title={artifact.title || artifact.id}
                      mimeType={presentation.mimeType}
                      fileKind={presentation.fileKind}
                      sizeBytes={presentation.sizeBytes}
                      previewText={presentation.previewText}
                      contentNote={presentation.contentNote}
                      previewAvailable={presentation.previewAvailable}
                      previewTruncated={presentation.previewTruncated}
                      metadata={presentation.metadata}
                      thumbnailUrl={presentation.thumbnailUrl}
                      thumbnailFetchUrl={presentation.thumbnailFetchUrl}
                      thumbnailArtifactId={presentation.thumbnailArtifactId}
                      pdfPreviewUrl={presentation.pdfPreviewUrl}
                      htmlPreviewUrl={presentation.htmlPreviewUrl}
                      htmlPreviewContent={presentation.htmlPreviewContent}
                      textPreviewFetchUrl={presentation.textPreviewFetchUrl}
                      onDownload={canDownloadArtifact(artifact) ? () => downloadArtifact(artifact) : undefined}
                      onClick={() => onOpenArtifact(artifact)}
                      className="max-w-full sm:max-w-[28rem]"
                    />
                  );
                })}
              </div>
            )}

            {/* Tool Calls + chart previews */}
            {message.tool_calls && message.tool_calls.length > 0 && (
              <CollapsedToolsSummary
                toolCalls={message.tool_calls}
                isLastMessage={isLastMessage}
                isThreadRunActive={isThreadRunActive}
                toolProgressByKey={toolProgressByKey}
                sqlToolResultsByScopedKey={sqlToolResultsByScopedKey}
                messageIdentityKey={messageIdentityKey}
                artifactsById={artifactsById}
                ensureArtifactLoaded={ensureArtifactLoaded}
                onOpenArtifact={onOpenArtifact}
                onPinArtifact={onPinArtifact}
                pinnedArtifactIds={pinnedArtifactIds}
                parentMessageId={message.id}
                parentMessageSeq={message.seq}
                onToggleMessageReference={onToggleMessageReference}
                pinnedMessageReferenceIds={pinnedMessageReferenceIds}
                toolResultsByCallId={toolResultsByCallId}
              />
            )}
          </div>
        </div>
      </div>
    </div>
  );
};

export default AssistantMessage;
