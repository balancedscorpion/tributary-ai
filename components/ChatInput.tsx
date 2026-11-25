import React, { useRef, useEffect, useCallback, useState } from 'react';
import { Loader2, Send, StopCircle, Table as TableIcon, Code2, ImagePlus, ImageIcon, Link2, MessageSquarePlus, Paperclip, X } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Textarea } from '@/components/ui/textarea';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { toast } from 'sonner';
import type { Artifact, ChatFileAttachment, ChatImageAttachment, MessageBlockReference, ThreadReference } from '@/types';
import FileAttachmentCard from '@/components/chat/FileAttachmentCard';

export type ImageAttachment = ChatImageAttachment;
export type FileAttachment = ChatFileAttachment;

interface ChatInputProps {
  // Return true when the message was accepted for sending. Return false to keep the draft
  // (e.g. when we need to open Settings to collect missing credentials).
  onSend: (message: string) => boolean | Promise<boolean>;
  onSteer: (message: string) => boolean | Promise<boolean>;
  threadId?: string | null;
  draftInsertion?: { id: number; text: string } | null;
  onStop: () => void;
  isLoading: boolean;
  isCompacting?: boolean;
  disabled: boolean;
  pinnedArtifacts?: Artifact[];
  onRemovePin?: (artifactId: string) => void;
  pinnedMessageReferences?: MessageBlockReference[];
  onRemovePinnedMessageReference?: (referenceId: string) => void;
  pinnedThreadReferences?: ThreadReference[];
  onRemovePinnedThreadReference?: (threadId: string) => void;
  onOpenThreadReferencePicker?: () => void;
  onAttachImage?: (image: ImageAttachment) => void;
  pendingImages?: ImageAttachment[];
  onRemoveImage?: (index: number) => void;
  onAttachFile?: (file: File, source: 'upload' | 'drop' | 'paste') => void | Promise<void>;
  pendingFiles?: FileAttachment[];
  onRemoveFile?: (index: number) => void;
  onRetryFile?: (localId: string) => void;
  /** Compact active-target pill slot (plan §4.5); rendered in the footer row. */
  targetControl?: React.ReactNode;
  /** Thin non-blocking row above the composer (preflight notices, plan §4.4). */
  aboveComposer?: React.ReactNode;
}

const MAX_IMAGE_SIZE_BYTES = 10 * 1024 * 1024; // 10 MB
const MAX_FILE_SIZE_BYTES = 128 * 1024 * 1024; // 128 MB
const ACCEPTED_IMAGE_TYPES = 'image/png,image/jpeg,image/gif,image/webp';
const ACCEPTED_MIME_TYPES = new Set([
  'image/png', 'image/jpeg', 'image/gif', 'image/webp',
]);

function readFileAsDataUri(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result as string);
    reader.onerror = () => reject(new Error('Failed to read file'));
    reader.readAsDataURL(file);
  });
}

function getImageDimensions(dataUri: string): Promise<{ width: number; height: number } | null> {
  return new Promise((resolve) => {
    const img = new Image();
    img.onload = () => resolve({ width: img.naturalWidth, height: img.naturalHeight });
    img.onerror = () => resolve(null);
    img.src = dataUri;
  });
}

const ChatInput: React.FC<ChatInputProps> = ({
  onSend, onSteer, threadId, draftInsertion, onStop, isLoading, isCompacting = false, disabled,
  pinnedArtifacts = [], onRemovePin,
  pinnedMessageReferences = [], onRemovePinnedMessageReference,
  pinnedThreadReferences = [], onRemovePinnedThreadReference, onOpenThreadReferencePicker,
  onAttachImage, pendingImages = [], onRemoveImage,
  onAttachFile, pendingFiles = [], onRemoveFile, onRetryFile,
  targetControl, aboveComposer,
}) => {
  const [isDragging, setIsDragging] = useState(false);
  const [dragFileCount, setDragFileCount] = useState(0);
  const [value, setValue] = useState('');
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const imageInputRef = useRef<HTMLInputElement>(null);
  const attachmentInputRef = useRef<HTMLInputElement>(null);
  const dragCounterRef = useRef(0);
  const draftByThreadIdRef = useRef<Record<string, string>>({});
  const valueRef = useRef('');
  const activeDraftKeyRef = useRef(String(threadId || '').trim() || '__no-thread__');
  const lastDraftInsertionIdRef = useRef<number | null>(null);

  useEffect(() => {
    const textarea = textareaRef.current;
    if (!textarea) return;
    const frame = window.requestAnimationFrame(() => {
      textarea.style.height = 'auto';
      const nextHeight = `${Math.min(textarea.scrollHeight, 200)}px`;
      if (textarea.style.height !== nextHeight) {
        textarea.style.height = nextHeight;
      }
    });
    return () => window.cancelAnimationFrame(frame);
  }, [value]);

  const setDraftForActiveThread = useCallback((next: string) => {
    const draft = String(next || '');
    valueRef.current = draft;
    draftByThreadIdRef.current[activeDraftKeyRef.current] = draft;
    setValue(draft);
  }, []);

  useEffect(() => {
    const nextKey = String(threadId || '').trim() || '__no-thread__';
    if (activeDraftKeyRef.current === nextKey) return;

    draftByThreadIdRef.current[activeDraftKeyRef.current] = valueRef.current;
    activeDraftKeyRef.current = nextKey;
    const nextDraft = draftByThreadIdRef.current[nextKey] || '';
    valueRef.current = nextDraft;
    setValue(nextDraft);
  }, [threadId]);

  useEffect(() => {
    if (!draftInsertion) return;
    if (lastDraftInsertionIdRef.current === draftInsertion.id) return;
    lastDraftInsertionIdRef.current = draftInsertion.id;

    const snippet = String(draftInsertion.text || '').trim();
    if (!snippet) return;
    setDraftForActiveThread(
      valueRef.current.trim()
        ? `${valueRef.current.replace(/\s+$/, '')}\n\n${snippet}\n`
        : `${snippet}\n`,
    );
    textareaRef.current?.focus();
  }, [draftInsertion, setDraftForActiveThread]);

  const handleImageFile = useCallback(async (file: File, source: ImageAttachment['source']) => {
    if (!onAttachImage) return;
    if (!ACCEPTED_MIME_TYPES.has(file.type)) {
      toast.error(`Unsupported image type: ${file.type}`);
      return;
    }
    if (file.size > MAX_IMAGE_SIZE_BYTES) {
      toast.error(`Image too large (${(file.size / 1024 / 1024).toFixed(1)} MB). Max 10 MB.`);
      return;
    }
    try {
      const dataUri = await readFileAsDataUri(file);
      const dimensions = await getImageDimensions(dataUri);
      onAttachImage({
        fileName: file.name || 'Pasted image',
        mimeType: file.type,
        sizeBytes: file.size,
        dataUri,
        width: dimensions?.width,
        height: dimensions?.height,
        source,
      });
    } catch {
      toast.error('Failed to read image file');
    }
  }, [onAttachImage]);

  const handleFileUpload = useCallback(async (file: File, source: 'upload' | 'drop' | 'paste') => {
    // First-party upload path handles every file, images included. The inline
    // data-URI image path survives only for embedders that pass onAttachImage
    // without onAttachFile.
    if (file.type.startsWith('image/') && ACCEPTED_MIME_TYPES.has(file.type) && !onAttachFile && onAttachImage) {
      await handleImageFile(file, source);
      return;
    }
    if (!onAttachFile) return;
    if (file.size > MAX_FILE_SIZE_BYTES) {
      toast.error(`File too large (${(file.size / 1024 / 1024).toFixed(1)} MB). Max 128 MB.`);
      return;
    }
    try {
      await onAttachFile(file, source);
    } catch (error: any) {
      toast.error(error?.message || 'Failed to upload file');
    }
  }, [handleImageFile, onAttachFile, onAttachImage]);

  const handlePaste = useCallback((e: React.ClipboardEvent) => {
    if (!onAttachFile && !onAttachImage) return;
    const items = Array.from(e.clipboardData.items);
    const fileItems = items.filter((item) => {
      if (item.kind !== 'file') return false;
      if (item.type.startsWith('image/')) return ACCEPTED_MIME_TYPES.has(item.type);
      return Boolean(onAttachFile);
    });
    if (fileItems.length === 0) return;
    e.preventDefault();
    fileItems.forEach((item) => {
      const file = item.getAsFile();
      if (file) void handleFileUpload(file, 'paste');
    });
  }, [onAttachFile, onAttachImage, handleFileUpload]);

  const handleDragEnter = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    dragCounterRef.current += 1;
    if (e.dataTransfer.types.includes('Files')) {
      setIsDragging(true);
      const fileItems = Array.from(e.dataTransfer.items || []).filter((item) => item.kind === 'file');
      setDragFileCount(fileItems.length);
    }
  }, []);

  const handleDragOver = useCallback((e: React.DragEvent) => {
    e.preventDefault();
  }, []);

  const handleDragLeave = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    dragCounterRef.current -= 1;
    if (dragCounterRef.current <= 0) {
      dragCounterRef.current = 0;
      setIsDragging(false);
    }
  }, []);

  const handleDrop = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    dragCounterRef.current = 0;
    setIsDragging(false);
    const files = Array.from(e.dataTransfer.files || []);
    if (files.length === 0) return;
    files.forEach((f) => void handleFileUpload(f, 'drop'));
  }, [handleFileUpload]);

  const handleSendSubmit = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if ((!value.trim() && pendingImages.length === 0 && pendingFiles.length === 0) || disabled) return;
    const submitted = value;
    let accepted = false;
    try {
      accepted = await onSend(submitted);
    } catch (error) {
      console.error('Send failed; draft kept.', error);
      accepted = false;
    }
    if (!accepted) return;
    setDraftForActiveThread('');
  };

  const handleSteerSubmit = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if ((!value.trim() && pendingImages.length === 0 && pendingFiles.length === 0) || disabled) return;
    const submitted = value;
    let accepted = false;
    try {
      accepted = await onSteer(submitted);
    } catch (error) {
      console.error('Steer failed; draft kept.', error);
      accepted = false;
    }
    if (!accepted) return;
    setDraftForActiveThread('');
  };

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault();
      if (isLoading) {
        void handleSteerSubmit();
      } else {
        void handleSendSubmit();
      }
    }
  };

  const hasSendableContent = value.trim() || pendingImages.length > 0 || pendingFiles.length > 0;
  const hasUploadingFiles = pendingFiles.some((file) => file.status === 'uploading');

  // Single always-mounted live region for upload lifecycle announcements.
  // Per-card live regions flood screen readers with progress ticks and drop
  // the completion message when the status node unmounts.
  const [uploadAnnouncement, setUploadAnnouncement] = useState('');
  const [uploadErrorAnnouncement, setUploadErrorAnnouncement] = useState('');
  const prevFileStatusesRef = useRef<Record<string, string>>({});
  useEffect(() => {
    const prev = prevFileStatusesRef.current;
    const next: Record<string, string> = {};
    const announcements: string[] = [];
    const errorAnnouncements: string[] = [];
    for (const file of pendingFiles) {
      const key = file.localId || file.artifactId;
      if (!key) continue;
      const status = file.status || 'uploaded';
      next[key] = status;
      const prevStatus = prev[key];
      if (prevStatus === status) continue;
      if (!prevStatus && status === 'uploading') {
        announcements.push(`Uploading ${file.fileName}.`);
      } else if (prevStatus === 'uploading' && status === 'uploaded') {
        announcements.push(`${file.fileName} uploaded, ready to send.`);
      } else if (status === 'error') {
        errorAnnouncements.push(`${file.fileName} upload failed.`);
      }
    }
    prevFileStatusesRef.current = next;
    if (announcements.length > 0) setUploadAnnouncement(announcements.join(' '));
    if (errorAnnouncements.length > 0) setUploadErrorAnnouncement(errorAnnouncements.join(' '));
  }, [pendingFiles]);
  const hasAttachments =
    pinnedArtifacts.length > 0 ||
    pinnedMessageReferences.length > 0 ||
    pinnedThreadReferences.length > 0 ||
    pendingImages.length > 0 ||
    pendingFiles.length > 0;

  return (
    <div className="relative w-full max-w-4xl mx-auto p-4">
      {aboveComposer && <div className="mb-2 -mx-4">{aboveComposer}</div>}
      <div aria-live="polite" role="status" className="sr-only">
        {uploadAnnouncement}
      </div>
      {/* aria-live=assertive without role=alert: the error card itself carries
          role=alert; a second alert role here would double-announce. */}
      <div aria-live="assertive" className="sr-only">
        {uploadErrorAnnouncement}
      </div>
      <div
        className={`relative flex flex-col bg-card/80 backdrop-blur-sm border rounded-2xl p-2 shadow-xl ring-1 ring-white/5 focus-within:ring-ring/50 focus-within:border-ring/50 transition-all duration-200 ${
          isDragging
            ? 'border-primary ring-2 ring-primary/30 border-dashed'
            : 'border-border'
        }`}
        onDragEnter={handleDragEnter}
        onDragOver={handleDragOver}
        onDragLeave={handleDragLeave}
        onDrop={handleDrop}
      >
        {/* Drop zone overlay */}
        {isDragging && (
          <div aria-hidden="true" className="absolute inset-0 z-10 flex items-center justify-center rounded-2xl bg-primary/5 pointer-events-none">
            <div className="flex items-center gap-2 text-sm font-medium text-primary">
              <Paperclip size={18} />
              {dragFileCount > 1 ? `Drop ${dragFileCount} files to attach` : 'Drop to attach'}
            </div>
          </div>
        )}

        {isCompacting && (
          <div className="mx-2 mt-2 flex items-center gap-2 rounded-xl border border-primary/20 bg-primary/5 px-3 py-2 text-xs text-primary">
            <Loader2 size={14} className="animate-spin shrink-0" />
            <span className="font-medium">Compacting context</span>
            <span className="hidden sm:inline text-primary/70">Preserving the long conversation before continuing.</span>
          </div>
        )}

        {hasAttachments && (
          <div className="px-3 pt-2.5 pb-2 border-b border-border/30">
            <div className="flex flex-wrap gap-1.5">
              {pinnedArtifacts.map((artifact) => (
                <button
                  key={artifact.id}
                  onClick={() => onRemovePin?.(artifact.id)}
                  aria-label={`Remove pinned artifact ${artifact.title}`}
                  className="group/chip flex items-center gap-1.5 bg-muted/50 border border-border/50
                             rounded-lg px-2.5 py-1.5 text-xs animate-fade-in
                             hover:border-border transition-colors"
                >
                  {artifact.type === 'image'
                    ? <ImageIcon size={14} className="text-purple-400 shrink-0" />
                    : artifact.type === 'data'
                      ? <TableIcon size={14} className="text-accent shrink-0" />
                      : artifact.type === 'file'
                        ? <Paperclip size={14} className="text-primary shrink-0" />
                        : <Code2 size={14} className="text-primary shrink-0" />}
                  <span className="truncate max-w-[140px] text-muted-foreground">
                    {artifact.title}
                  </span>
                  <X size={12} className="text-muted-foreground/60 group-hover/chip:text-foreground
                                           shrink-0 ml-0.5 transition-colors" />
                </button>
              ))}

              {pinnedMessageReferences.map((reference) => {
                const preview = reference.preview || reference.contentSnapshot || '';
                const displayText = preview.length > 40 ? preview.slice(0, 40).trimEnd() + '…' : preview;
                return (
                  <button
                    key={reference.id}
                    onClick={() => onRemovePinnedMessageReference?.(reference.id)}
                    aria-label={`Remove message reference ${reference.label || reference.title}`}
                    className="group/chip flex items-center gap-1.5 bg-muted/50 border border-sky-400/20
                               rounded-lg px-2.5 py-1.5 text-xs animate-fade-in
                               hover:border-sky-400/40 transition-colors"
                    title={preview || reference.title}
                  >
                    <MessageSquarePlus size={12} className="text-sky-400 shrink-0" />
                    <span className="text-muted-foreground/60 shrink-0">Referencing:</span>
                    <span className="truncate max-w-[200px] text-muted-foreground">
                      &ldquo;{displayText || reference.label || reference.title}&rdquo;
                    </span>
                    <X size={12} className="text-muted-foreground/60 group-hover/chip:text-foreground
                                             shrink-0 ml-0.5 transition-colors" />
                  </button>
                );
              })}

              {pinnedThreadReferences.map((reference) => {
                const summary = typeof reference.summary === 'string' ? reference.summary.trim() : '';
                const displayText = summary.length > 48 ? summary.slice(0, 48).trimEnd() + '…' : summary;
                return (
                  <button
                    key={reference.id}
                    onClick={() => onRemovePinnedThreadReference?.(reference.id)}
                    aria-label={`Remove thread reference ${reference.title}`}
                    className="group/chip flex items-center gap-1.5 bg-muted/50 border border-emerald-400/20
                               rounded-lg px-2.5 py-1.5 text-xs animate-fade-in
                               hover:border-emerald-400/40 transition-colors"
                    title={summary || reference.title}
                  >
                    <Link2 size={12} className="text-emerald-400 shrink-0" />
                    <span className="text-muted-foreground/60 shrink-0">Thread:</span>
                    <span className="truncate max-w-[220px] text-muted-foreground">
                      {reference.title}
                      {displayText ? ` — ${displayText}` : ''}
                    </span>
                    <X size={12} className="text-muted-foreground/60 group-hover/chip:text-foreground
                                             shrink-0 ml-0.5 transition-colors" />
                  </button>
                );
              })}

              {pendingImages.map((img, i) => (
                <button
                  key={i}
                  onClick={() => onRemoveImage?.(i)}
                  aria-label={`Remove image ${img.fileName}`}
                  className="group/chip flex items-center gap-1.5 bg-muted/50 border border-border/50
                             rounded-lg px-2 py-1 text-xs animate-fade-in
                             hover:border-border transition-colors"
                >
                  <img
                    src={img.dataUri}
                    alt={img.fileName}
                    className="h-7 w-7 rounded object-cover shrink-0"
                  />
                  <span className="truncate max-w-[100px] text-muted-foreground">
                    {img.fileName}
                  </span>
                  <X size={12} className="text-muted-foreground/60 group-hover/chip:text-foreground
                                           shrink-0 ml-0.5 transition-colors" />
                </button>
              ))}
            </div>

            {pendingFiles.length > 0 && (
              <div className="mt-2 grid gap-2 sm:grid-cols-2">
                {pendingFiles.map((file, i) => (
                  <FileAttachmentCard
                    key={file.localId || `${file.artifactId}:${i}`}
                    title={file.fileName}
                    mimeType={file.detectedMimeType || file.mimeType}
                    fileKind={file.fileKind}
                    sizeBytes={file.sizeBytes}
                    previewText={file.previewText}
                    contentNote={file.contentNote}
                    previewAvailable={file.previewAvailable}
                    previewTruncated={file.previewTruncated}
                    metadata={file as Record<string, any>}
                    status={file.status}
                    progress={file.progress}
                    errorMessage={file.errorMessage}
                    thumbnailUrl={file.thumbnailUrl}
                    thumbnailFetchUrl={file.thumbnailFetchUrl}
                    thumbnailArtifactId={file.thumbnailArtifactId}
                    pdfPreviewUrl={file.pdfPreviewUrl}
                    htmlPreviewUrl={file.htmlPreviewUrl}
                    htmlPreviewContent={file.htmlPreviewContent}
                    textPreviewFetchUrl={file.textPreviewFetchUrl}
                    variant="compact"
                    downloadArtifactId={
                      file.status !== 'uploading' && file.status !== 'error' && file.artifactId
                        ? file.artifactId
                        : undefined
                    }
                    downloadFileName={file.fileName}
                    onRetry={
                      file.status === 'error' && file.localId && onRetryFile
                        ? () => onRetryFile(file.localId!)
                        : undefined
                    }
                    onRemove={() => {
                      onRemoveFile?.(i);
                      // The card unmounts; keep keyboard focus in the composer.
                      textareaRef.current?.focus();
                    }}
                  />
                ))}
              </div>
            )}
            {hasUploadingFiles && (
              <div className="mt-1.5 flex items-center gap-1.5 text-[11px] text-muted-foreground">
                <Loader2 size={11} className="animate-spin shrink-0" aria-hidden="true" />
                Waiting for {pendingFiles.filter((file) => file.status === 'uploading').length > 1 ? 'uploads' : 'upload'} to finish before sending…
              </div>
            )}
          </div>
        )}
        <div className="flex items-end gap-2">
          <Textarea
            ref={textareaRef}
            id="chat-composer-input"
            value={value}
            onChange={(e) => setDraftForActiveThread(e.target.value)}
            onKeyDown={handleKeyDown}
            onPaste={handlePaste}
            placeholder={isLoading ? 'Steer the response...' : 'Send a message...'}
            rows={1}
            className="w-full bg-transparent border-0 text-foreground placeholder-muted-foreground p-3 max-h-[200px] resize-none focus-visible:ring-0 focus-visible:ring-offset-0 rounded-xl"
            disabled={disabled && !isLoading}
          />
        <div className="pb-1 pr-1 flex items-center gap-1">
          {/* Attach image button — only for embedders without the general
              attach path. When onAttachFile exists, images go through the
              same "Attach file" upload, so a second button is just noise. */}
          {!onAttachFile && onAttachImage && (
            <>
              <input
                ref={imageInputRef}
                type="file"
                accept={ACCEPTED_IMAGE_TYPES}
                multiple
                className="hidden"
                onChange={(e) => {
                  Array.from(e.target.files || []).forEach((f) => void handleFileUpload(f, 'upload'));
                  e.target.value = '';
                }}
              />
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={() => imageInputRef.current?.click()}
                    aria-label="Attach image"
                    className="rounded-xl text-muted-foreground hover:text-foreground"
                  >
                    <ImagePlus size={20} />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Attach image</TooltipContent>
              </Tooltip>
            </>
          )}

          {onAttachFile && (
            <>
              <input
                ref={attachmentInputRef}
                type="file"
                multiple
                className="hidden"
                onChange={(e) => {
                  Array.from(e.target.files || []).forEach((f) => void handleFileUpload(f, 'upload'));
                  e.target.value = '';
                }}
              />
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={() => attachmentInputRef.current?.click()}
                    aria-label="Attach file"
                    className="rounded-xl text-muted-foreground hover:text-foreground"
                  >
                    <Paperclip size={20} />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Attach file</TooltipContent>
              </Tooltip>
            </>
          )}

          {onOpenThreadReferencePicker && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  onClick={onOpenThreadReferencePicker}
                  aria-label="Reference thread"
                  className="rounded-xl text-muted-foreground hover:text-foreground"
                >
                  <Link2 size={20} />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Reference thread</TooltipContent>
            </Tooltip>
          )}

          {isLoading ? (
            <>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    variant="ghost"
                    size="icon"
                    onClick={onStop}
                    aria-label="Stop generating"
                    className="rounded-xl text-destructive hover:bg-destructive/10 hover:text-destructive"
                  >
                    <StopCircle size={20} />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>Stop generating</TooltipContent>
              </Tooltip>
              <Tooltip>
                <TooltipTrigger asChild>
                  <Button
                    size="icon"
                    onClick={() => void handleSteerSubmit()}
                    disabled={(!value.trim() && pendingImages.length === 0 && pendingFiles.length === 0) || disabled || hasUploadingFiles}
                    aria-label={hasUploadingFiles ? 'Steer (waiting for uploads to finish)' : 'Steer'}
                    className="rounded-xl"
                  >
                    <Send size={20} className={value.trim() && !disabled ? 'translate-x-0.5' : ''} />
                  </Button>
                </TooltipTrigger>
                <TooltipContent>
                  {hasUploadingFiles
                    ? 'Waiting for uploads to finish…'
                    : <>Steer <kbd className="ml-1 text-[10px] bg-muted px-1 rounded">Enter</kbd></>}
                </TooltipContent>
              </Tooltip>
            </>
          ) : (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  size="icon"
                  onClick={() => void handleSendSubmit()}
                  disabled={!hasSendableContent || disabled || hasUploadingFiles}
                  aria-label={hasUploadingFiles ? 'Send message (waiting for uploads to finish)' : 'Send message'}
                  className="rounded-xl"
                >
                  <Send size={20} className={hasSendableContent && !disabled ? 'translate-x-0.5' : ''} />
                </Button>
              </TooltipTrigger>
              <TooltipContent>
                {hasUploadingFiles
                  ? 'Waiting for uploads to finish…'
                  : <>Send message <kbd className="ml-1 text-[10px] bg-muted px-1 rounded">Enter</kbd></>}
              </TooltipContent>
            </Tooltip>
          )}
        </div>
        </div>
        {targetControl && (
          <div className="flex items-center gap-2 px-2 pb-1 pt-0.5">
            {targetControl}
          </div>
        )}
      </div>
      <div className="text-center mt-2">
        <p className="text-xs text-muted-foreground/60">
          AI can make mistakes. Consider checking important information.
        </p>
      </div>
    </div>
  );
};

export default React.memo(ChatInput);
