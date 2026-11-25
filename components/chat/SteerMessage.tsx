import React from 'react';
import { Sparkles, Code2, Table as TableIcon } from 'lucide-react';
import type { Artifact, ChatFileAttachment, MessageBlockReference, ThreadReference } from '@/types';
import MessageReferenceButton from './MessageReferenceButton';
import ReferencedMessageChips from './ReferencedMessageChips';
import ReferencedThreadChips from './ReferencedThreadChips';
import FileAttachmentCard from './FileAttachmentCard';
import { isRawDownloadArtifact } from '@/lib/artifactDownload';
import {
  fileArtifactCardPresentation,
  fileAttachmentSnapshotArtifact,
  splitAttachmentArtifacts,
} from '@/lib/attachmentPresentation';

interface SteerMessageProps {
  content: string | null;
  referencedArtifacts?: Artifact[];
  fileAttachments?: ChatFileAttachment[];
  referencedMessages?: MessageBlockReference[];
  referencedThreads?: ThreadReference[];
  onOpenArtifact?: (artifact: Artifact) => void;
  messageReference?: MessageBlockReference;
  isMessageReferenced?: boolean;
  pinnedMessageReferenceIdSet?: Set<string>;
  onToggleMessageReference?: (reference: MessageBlockReference) => void;
  pinnedThreadReferenceIdSet?: Set<string>;
  onToggleThreadReference?: (reference: ThreadReference) => void;
}

const SteerMessage: React.FC<SteerMessageProps> = ({
  content,
  referencedArtifacts = [],
  fileAttachments = [],
  referencedMessages = [],
  referencedThreads = [],
  onOpenArtifact,
  messageReference,
  isMessageReferenced,
  pinnedMessageReferenceIdSet,
  onToggleMessageReference,
  pinnedThreadReferenceIdSet,
  onToggleThreadReference,
}) => {
  const { imageArtifacts, fileArtifacts, snapshotAttachments, otherArtifacts } =
    splitAttachmentArtifacts(referencedArtifacts, fileAttachments);

  return (
    <div className="flex w-full justify-start animate-fade-in min-w-0">
      <div className="flex w-full max-w-[90%] md:max-w-[85%] justify-end min-w-0">
        <div className="flex flex-col gap-1 min-w-0 items-end max-w-[80%]">
          <div
            className={`group/msg flex max-w-full min-w-0 items-start gap-2 overflow-hidden px-4 py-3 rounded-2xl rounded-br-sm shadow-sm bg-card border border-border border-l-4 border-l-primary/50 ${
              isMessageReferenced ? 'ring-1 ring-sky-400/40' : ''
            }`}
          >
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-1.5 mb-1 text-[10px] font-semibold tracking-wider uppercase text-primary">
                <Sparkles size={12} />
                Steer
              </div>
              {content && (
                <p className="chat-text-wrap text-sm leading-relaxed whitespace-pre-wrap text-card-foreground">
                  {content}
                </p>
              )}

              {/* Inline image thumbnails */}
              {imageArtifacts.length > 0 && (
                <div className={content ? 'mt-2' : ''}>
                  <div className="flex flex-wrap gap-2">
                    {imageArtifacts.map((artifact) => (
                      <button
                        key={artifact.id}
                        type="button"
                        onClick={() => onOpenArtifact?.(artifact)}
                        className="group/img max-w-full rounded-lg overflow-hidden border border-border/50
                                   hover:border-border/70 transition-all
                                   hover:ring-1 hover:ring-border/30"
                        title={artifact.title}
                      >
                        <img
                          src={artifact.content}
                          alt={artifact.title}
                          className="max-w-[240px] max-h-[160px] object-contain rounded-lg"
                        />
                      </button>
                    ))}
                  </div>
                </div>
              )}

              {(fileArtifacts.length > 0 || snapshotAttachments.length > 0) && (
                <div className={content || imageArtifacts.length > 0 ? 'mt-2' : ''}>
                  <div className="flex w-full max-w-[30rem] flex-col gap-2">
                    {fileArtifacts.map((artifact) => {
                      const presentation = fileArtifactCardPresentation(artifact);
                      return (
                        <FileAttachmentCard
                          key={artifact.id}
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
                          downloadArtifactId={isRawDownloadArtifact(artifact) ? artifact.id : undefined}
                          onClick={() => onOpenArtifact?.(artifact)}
                          className="max-w-full sm:max-w-[28rem]"
                        />
                      );
                    })}
                    {snapshotAttachments.map((attachment) => {
                      const artifact = fileAttachmentSnapshotArtifact(attachment);
                      return (
                        <FileAttachmentCard
                          key={attachment.artifactId}
                          title={attachment.fileName}
                          mimeType={attachment.detectedMimeType || attachment.mimeType}
                          fileKind={attachment.fileKind}
                          sizeBytes={attachment.sizeBytes}
                          previewText={attachment.previewText}
                          contentNote={attachment.contentNote}
                          previewAvailable={attachment.previewAvailable}
                          previewTruncated={attachment.previewTruncated}
                          metadata={attachment as Record<string, any>}
                          thumbnailUrl={attachment.thumbnailUrl}
                          thumbnailFetchUrl={attachment.thumbnailFetchUrl}
                          thumbnailArtifactId={attachment.thumbnailArtifactId}
                          pdfPreviewUrl={attachment.pdfPreviewUrl}
                          htmlPreviewUrl={attachment.htmlPreviewUrl}
                          htmlPreviewContent={attachment.htmlPreviewContent}
                          textPreviewFetchUrl={attachment.textPreviewFetchUrl}
                          downloadArtifactId={attachment.artifactId}
                          downloadFileName={attachment.fileName}
                          onClick={artifact ? () => onOpenArtifact?.(artifact) : undefined}
                          className="max-w-full sm:max-w-[28rem]"
                        />
                      );
                    })}
                  </div>
                </div>
              )}

              {otherArtifacts.length > 0 && (
                <div className={content || imageArtifacts.length > 0 || fileArtifacts.length > 0 || snapshotAttachments.length > 0 ? 'mt-2' : ''}>
                  <div className="flex flex-wrap gap-1.5">
                    {otherArtifacts.map((artifact) => (
                      <button
                        key={artifact.id}
                        type="button"
                        onClick={() => onOpenArtifact?.(artifact)}
                        className="group/chip flex max-w-full min-w-0 items-center gap-1.5 bg-background/30 border border-border/50
                                   rounded-lg px-2.5 py-1.5 text-xs
                                   hover:bg-background/40 hover:border-border/70 transition-colors"
                        title={artifact.title}
                      >
                        {artifact.type === 'data'
                          ? <TableIcon size={14} className="text-muted-foreground shrink-0 opacity-90" />
                          : <Code2 size={14} className="text-muted-foreground shrink-0 opacity-90" />}
                        <span className="min-w-0 truncate max-w-[200px] text-foreground">
                          {artifact.title || artifact.id}
                        </span>
                      </button>
                    ))}
                  </div>
                </div>
              )}

              <ReferencedMessageChips
                references={referencedMessages}
                pinnedMessageReferenceIdSet={pinnedMessageReferenceIdSet}
                onToggleMessageReference={onToggleMessageReference}
                className={
                  content || imageArtifacts.length > 0 || fileArtifacts.length > 0 || snapshotAttachments.length > 0 || otherArtifacts.length > 0
                    ? 'mt-2'
                    : undefined
                }
              />
              <ReferencedThreadChips
                references={referencedThreads}
                pinnedThreadReferenceIdSet={pinnedThreadReferenceIdSet}
                onToggleThreadReference={onToggleThreadReference}
                className={
                  referencedThreads.length > 0
                    ? content || imageArtifacts.length > 0 || fileArtifacts.length > 0 || snapshotAttachments.length > 0 || otherArtifacts.length > 0 || referencedMessages.length > 0
                      ? 'mt-2'
                      : undefined
                    : undefined
                }
              />
            </div>
            {messageReference && onToggleMessageReference && (
              <MessageReferenceButton
                isReferenced={isMessageReferenced}
                onToggle={() => onToggleMessageReference(messageReference)}
              />
            )}
          </div>
        </div>
      </div>
    </div>
  );
};

export default SteerMessage;
