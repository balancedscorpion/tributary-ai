import React, { useEffect, useMemo, useRef, useState } from "react";
import {
  Check,
  Copy,
  Download,
  ExternalLink,
  ImageIcon,
  ImageOff,
  Info,
  Loader2,
  MessageSquareDashed,
  MessageSquarePlus,
  Pencil,
  RefreshCw,
  Settings,
  ShieldAlert,
} from "lucide-react";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { formatElapsedColon, StatusPill } from "@/components/renderers/tool/query/atoms";
import { useChatActions } from "./ChatActionsContext";
import {
  artifactRawUrl,
  downloadArtifact,
  downloadArtifactRawBytes,
  getArtifactDownloadFileName,
  isRawDownloadArtifact,
} from "@/lib/artifactDownload";
import {
  classifyGeneratedImageError,
  generatedImageAltText,
  generatedImageArtifactIds,
  generatedImageAspectRatio,
  generatedImageStage,
  generatedImageStageLabel,
  getGeneratedImageMetadata,
  type GeneratedImageErrorCategory,
} from "@/lib/generatedImageArtifacts";
import { cn } from "@/lib/utils";
import type {
  Artifact,
  ChatImageAttachment,
  EnsureArtifactLoaded,
  ToolCall,
  ToolProgressEvent,
  ToolResultEnvelope,
} from "@/types";

/**
 * Purpose-built transcript card for the local `generate_image` tool. Rendered
 * from CollapsedToolsSummary's per-tool dispatch (which serves both live paths:
 * the collapsed summary and its IntermediateThinkingBlock mirror). All
 * generated-image classification flows through lib/generatedImageArtifacts so
 * the card never re-reads raw metadata; if a helper is missing the fix belongs
 * there, not here.
 *
 * The card deliberately avoids a flashy "AI image generator" look — it matches
 * the muted card language of its neighbours (QueryExecutionCard, the generic
 * tool row) so a generated image reads as one more tool result, not an ad.
 */

export interface GeneratedImageCardProps {
  toolCall: ToolCall;
  envelope?: ToolResultEnvelope;
  /** Primary result artifact when the run message already carried it. */
  resultArtifact?: Artifact;
  artifactsById?: Record<string, Artifact>;
  ensureArtifactLoaded?: EnsureArtifactLoaded;
  /** Live tool_progress event for this call (only while running). */
  progressEvent?: ToolProgressEvent;
  isRunning?: boolean;
  onOpenArtifact: (a: Artifact) => void;
  onPinArtifact?: (a: Artifact) => void;
  pinnedArtifactIds?: string[];
  compact?: boolean;
}

type CardState =
  | { kind: "queued" }
  | { kind: "generating" }
  | { kind: "completed" }
  | { kind: "moderation_blocked" }
  | { kind: "cancelled" }
  | { kind: "interrupted" }
  | { kind: "failed"; category: GeneratedImageErrorCategory };

const DATA_STATE: Record<CardState["kind"], string> = {
  queued: "queued",
  generating: "generating",
  completed: "completed",
  moderation_blocked: "moderation_blocked",
  cancelled: "cancelled",
  interrupted: "interrupted",
  failed: "failed",
};

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  return "";
}

/** The frame that reserves stable, cap-280px space across every state so the
 *  card never shifts layout when pixels arrive. */
const IMAGE_FRAME_CLASS =
  "relative mx-auto w-full max-h-[280px] overflow-hidden rounded-md bg-muted/40";

function ImageFrame({
  ratio,
  children,
}: {
  ratio: number;
  children: React.ReactNode;
}) {
  const safeRatio = ratio > 0 ? ratio : 1;
  return (
    <div
      className={IMAGE_FRAME_CLASS}
      style={{
        aspectRatio: String(safeRatio),
        maxWidth: `${Math.round(280 * safeRatio)}px`,
      }}
    >
      {children}
    </div>
  );
}

/** Coarse request description from tool args (no settings/metadata yet while
 *  generating): e.g. "high quality · landscape · PNG". */
function describeRequest(args: Record<string, unknown> | null | undefined): string {
  const record = (args && typeof args === "object" ? args : {}) as Record<string, any>;
  const quality = firstString(record.quality);
  const size = firstString(record.size);
  const aspect = firstString(record.aspect_ratio, record.aspectRatio);
  const format = firstString(record.format);
  const parts: string[] = [];
  if (quality && quality !== "auto") parts.push(`${quality} quality`);
  const shape = size && size !== "auto" ? size : aspect && aspect !== "auto" ? aspect : "";
  if (shape) parts.push(shape);
  if (format) parts.push(format.toUpperCase());
  return parts.join(" · ");
}

function errorMessage(
  envelope: ToolResultEnvelope | undefined,
  fallback: string,
): string {
  if (!envelope) return fallback;
  const result = (envelope.result && typeof envelope.result === "object"
    ? envelope.result
    : {}) as Record<string, any>;
  const meta = (envelope.result_metadata && typeof envelope.result_metadata === "object"
    ? envelope.result_metadata
    : {}) as Record<string, any>;
  return (
    firstString(
      result.message,
      result.error,
      meta.message,
      meta.error,
      typeof envelope.result === "string" ? envelope.result : "",
    ) || fallback
  );
}

function useElapsedMs(active: boolean, startedAt?: number): number {
  const [now, setNow] = useState(() => Date.now());
  const mountRef = useRef(Date.now());
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, [active]);
  const start =
    typeof startedAt === "number" && Number.isFinite(startedAt)
      ? startedAt
      : mountRef.current;
  return Math.max(0, now - start);
}

/** A muted icon button with a tooltip — the shared shape for every card action
 *  (matches ArtifactCard's inline icon convention; no dropdown menus). */
function IconAction({
  label,
  onClick,
  children,
  active,
  disabled,
}: {
  label: string;
  onClick: () => void;
  children: React.ReactNode;
  active?: boolean;
  disabled?: boolean;
}) {
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <button
          type="button"
          aria-label={label}
          onClick={onClick}
          disabled={disabled}
          className={cn(
            "inline-flex h-8 w-8 items-center justify-center rounded-md transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-40",
            active
              ? "text-sky-400 hover:bg-muted"
              : "text-muted-foreground hover:bg-muted hover:text-foreground",
          )}
        >
          {children}
        </button>
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  );
}

function imageSrcFor(artifact: Artifact | undefined, id: string): string {
  const content = typeof artifact?.content === "string" ? artifact.content : "";
  if (content.startsWith("data:")) return content;
  return id ? artifactRawUrl(id) : "";
}

function blobAsDataUri(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () =>
      typeof reader.result === "string"
        ? resolve(reader.result)
        : reject(new Error("Image could not be encoded"));
    reader.onerror = () => reject(reader.error || new Error("Image could not be read"));
    reader.readAsDataURL(blob);
  });
}

function DetailRow({ label, value }: { label: string; value: string }) {
  if (!value) return null;
  return (
    <div className="flex gap-2">
      <dt className="w-24 shrink-0 text-muted-foreground">{label}</dt>
      <dd className="min-w-0 flex-1 break-words text-foreground/90">{value}</dd>
    </div>
  );
}

function DetailsPopover({ artifact }: { artifact: Artifact }) {
  const meta = getGeneratedImageMetadata(artifact);
  const size =
    meta.width && meta.height ? `${meta.width}×${meta.height}` : "";
  const duration =
    meta.latencyMs !== null ? `${(meta.latencyMs / 1000).toFixed(1)}s` : "";
  return (
    <Popover>
      <PopoverTrigger asChild>
        <button
          type="button"
          aria-label="Image details"
          className="inline-flex h-8 w-8 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          <Info size={15} aria-hidden />
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-80 max-w-[90vw] text-[11px]">
        <dl className="space-y-1.5">
          <DetailRow label="Prompt" value={meta.prompt} />
          <DetailRow label="Revised prompt" value={meta.revisedPrompt} />
          <DetailRow label="Model" value={meta.model} />
          <DetailRow label="Provider" value={meta.provider} />
          <DetailRow label="Quality" value={meta.quality} />
          <DetailRow label="Size" value={size} />
          <DetailRow label="Format" value={meta.format ? meta.format.toUpperCase() : ""} />
          <DetailRow label="Request ID" value={meta.requestId} />
          <DetailRow label="Duration" value={duration} />
        </dl>
      </PopoverContent>
    </Popover>
  );
}

/** One completed image with an inline <img> and an onError fallback tile. */
function CompletedImage({
  artifact,
  id,
  className,
}: {
  artifact: Artifact | undefined;
  id: string;
  className?: string;
}) {
  const [broken, setBroken] = useState(false);
  const src = imageSrcFor(artifact, id);
  useEffect(() => {
    setBroken(false);
  }, [src]);
  if (broken || !src) {
    return (
      <div
        data-testid="generated-image-broken-preview"
        className="flex h-full w-full flex-col items-center justify-center gap-1 text-muted-foreground"
      >
        <ImageOff size={22} aria-hidden />
        <span className="text-[11px]">Preview unavailable</span>
      </div>
    );
  }
  return (
    <img
      src={src}
      alt={generatedImageAltText(artifact)}
      onError={() => setBroken(true)}
      className={cn("h-full w-full object-contain", className)}
    />
  );
}

export interface GeneratedImageGalleryProps {
  toolCall: ToolCall;
  artifacts: Array<{ id: string; artifact: Artifact | undefined }>;
  onOpenArtifact: (a: Artifact) => void;
  onPinArtifact?: (a: Artifact) => void;
  pinnedArtifactIds?: string[];
  onDownload: (a: Artifact) => void;
}

/** Stable 2-column grid of independent image artifacts. Each tile is its own
 *  canonical artifact; per-tile actions live in an always-present toolbar so
 *  they stay keyboard reachable (no hover-only affordances). */
export function GeneratedImageGallery({
  toolCall,
  artifacts,
  onOpenArtifact,
  onPinArtifact,
  pinnedArtifactIds,
  onDownload,
}: GeneratedImageGalleryProps) {
  const ratio = generatedImageAspectRatio(toolCall.args as Record<string, unknown>);
  return (
    <div
      data-testid="generated-image-gallery"
      className="grid grid-cols-2 gap-2"
    >
      {artifacts.map(({ id, artifact }, index) => {
        const pinned = !!(artifact && pinnedArtifactIds?.includes(artifact.id));
        return (
          <div
            key={id || index}
            data-testid="generated-image-tile"
            className="flex flex-col overflow-hidden rounded-md border border-border/40 bg-background/40"
          >
            <button
              type="button"
              aria-label="Open image"
              onClick={() => artifact && onOpenArtifact(artifact)}
              disabled={!artifact}
              className="group relative block focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <ImageFrame ratio={ratio}>
                <CompletedImage artifact={artifact} id={id} />
              </ImageFrame>
              <span className="pointer-events-none absolute right-1.5 top-1.5 rounded bg-black/50 p-1 text-white opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100">
                <ExternalLink size={13} aria-hidden />
              </span>
            </button>
            <div className="flex items-center gap-0.5 border-t border-border/40 px-1 py-0.5">
              {onPinArtifact && (
                <IconAction
                  label="Use in chat"
                  active={pinned}
                  disabled={!artifact}
                  onClick={() => artifact && onPinArtifact(artifact)}
                >
                  {pinned ? (
                    <MessageSquareDashed size={15} aria-hidden />
                  ) : (
                    <MessageSquarePlus size={15} aria-hidden />
                  )}
                </IconAction>
              )}
              <IconAction
                label="Download image"
                disabled={!artifact}
                onClick={() => artifact && onDownload(artifact)}
              >
                <Download size={15} aria-hidden />
              </IconAction>
            </div>
          </div>
        );
      })}
    </div>
  );
}

function CardShell({
  state,
  errorCategory,
  children,
}: {
  state: CardState["kind"];
  errorCategory?: GeneratedImageErrorCategory;
  children: React.ReactNode;
}) {
  return (
    <div
      data-testid="generated-image-card"
      data-state={DATA_STATE[state]}
      data-error-category={state === "failed" ? errorCategory : undefined}
      className="my-2 overflow-hidden rounded-md border border-border/40 bg-muted/30 p-3"
    >
      {children}
    </div>
  );
}

const GeneratedImageCard: React.FC<GeneratedImageCardProps> = ({
  toolCall,
  envelope,
  resultArtifact,
  artifactsById,
  ensureArtifactLoaded,
  progressEvent,
  isRunning,
  onOpenArtifact,
  onPinArtifact,
  pinnedArtifactIds,
  compact,
}) => {
  const chatActions = useChatActions();
  const args = (toolCall.args || {}) as Record<string, unknown>;
  const requestedRatio = generatedImageAspectRatio(args);
  const startedAt =
    typeof (toolCall as any)._uiStartedAt === "number"
      ? (toolCall as any)._uiStartedAt
      : typeof progressEvent?.timestamp === "number"
        ? progressEvent.timestamp
        : undefined;

  // ----- Derive the card state -----
  const artifactIds = useMemo(
    () => generatedImageArtifactIds(envelope),
    [envelope],
  );
  const resolved = useMemo(() => {
    const list = artifactIds.map((id) => {
      if (resultArtifact && resultArtifact.id === id) {
        return { id, artifact: resultArtifact };
      }
      return { id, artifact: artifactsById?.[id] };
    });
    if (list.length === 0 && resultArtifact) {
      return [{ id: resultArtifact.id, artifact: resultArtifact }];
    }
    return list;
  }, [artifactIds, artifactsById, resultArtifact]);

  const state: CardState = useMemo(() => {
    if (envelope) {
      if (envelope.ok && resolved.length > 0) return { kind: "completed" };
      const category = classifyGeneratedImageError(envelope);
      if (category === "cancelled") return { kind: "cancelled" };
      if (category === "moderation_blocked") return { kind: "moderation_blocked" };
      if (envelope.ok) return { kind: "completed" };
      return { kind: "failed", category };
    }
    // A missing result is not evidence of cancellation. Result delivery and
    // the run-terminal event can commit in adjacent React renders, and older
    // cancelled runs may also be missing their terminal tool envelope. Only
    // an explicit cancelled envelope is allowed to claim cancellation.
    if (!isRunning) return { kind: "interrupted" };
    // Once any tool progress has arrived the provider request is under way.
    // Treat requesting/retrying as generating too so the user never sees a
    // static "queued" card while a billable request is actively running.
    if (progressEvent) return { kind: "generating" };
    return { kind: "queued" };
  }, [envelope, resolved.length, progressEvent, isRunning]);

  // ----- Hydrate placeholder artifacts once we know they are needed -----
  const missingIds = useMemo(() => {
    if (state.kind !== "completed") return [] as string[];
    return resolved
      .filter(({ id, artifact }) => {
        if (!id) return false;
        if (!artifact) return true;
        if (artifact.metadata?.placeholder === true) return true;
        const content = typeof artifact.content === "string" ? artifact.content : "";
        return content.length === 0;
      })
      .map(({ id }) => id);
  }, [state.kind, resolved]);
  const missingKey = missingIds.join(",");
  useEffect(() => {
    if (!ensureArtifactLoaded || !missingKey) return;
    for (const id of missingKey.split(",")) {
      if (id) void ensureArtifactLoaded(id).catch(() => {});
    }
  }, [ensureArtifactLoaded, missingKey]);

  // ----- Screen-reader announcements at state boundaries only -----
  const [announcement, setAnnouncement] = useState("");
  const [actionAnnouncement, setActionAnnouncement] = useState("");
  const prevStateRef = useRef<CardState["kind"] | null>(null);
  useEffect(() => {
    const prev = prevStateRef.current;
    prevStateRef.current = state.kind;
    // Announce boundaries: a run that just started generating, or any
    // transition into a terminal state. Never announce an already-terminal
    // card on first mount (a reload) and never per progress tick.
    const isTransition = prev !== null && prev !== state.kind;
    const isFreshStart = prev === null && state.kind === "generating";
    if (!isTransition && !isFreshStart) return;
    if (state.kind === "generating") setAnnouncement("Generating image");
    else if (state.kind === "completed") setAnnouncement("Image generated");
    else if (state.kind === "cancelled") setAnnouncement("Image generation cancelled");
    else if (state.kind === "interrupted")
      setAnnouncement("Image generation status unavailable");
    else if (state.kind === "moderation_blocked")
      setAnnouncement("Image blocked by content policy");
    else if (state.kind === "failed") setAnnouncement("Image generation failed");
  }, [state.kind]);

  const elapsedMs = useElapsedMs(state.kind === "generating", startedAt);

  // ----- Shared action helpers -----
  const meta = getGeneratedImageMetadata(resolved[0]?.artifact);
  const primaryArtifact = resolved[0]?.artifact;
  const promptText =
    meta.prompt || meta.revisedPrompt || firstString(args.prompt);

  const handleDownload = async (artifact: Artifact) => {
    try {
      if (isRawDownloadArtifact(artifact)) {
        await downloadArtifactRawBytes(
          artifact.id,
          getArtifactDownloadFileName(artifact),
        );
      } else {
        downloadArtifact(artifact);
      }
    } catch {
      setActionAnnouncement("Download failed");
    }
  };

  const [copied, setCopied] = useState(false);
  const handleCopyPrompt = async () => {
    if (!promptText) return;
    try {
      await navigator.clipboard.writeText(promptText);
      setCopied(true);
      setActionAnnouncement("Prompt copied");
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setActionAnnouncement("Copy failed");
    }
  };

  const handleEdit = async () => {
    chatActions.insertDraftText?.("Edit this image: ");
    if (!chatActions.attachImage || !primaryArtifact) return;

    let dataUri =
      typeof primaryArtifact?.content === "string" ? primaryArtifact.content : "";
    let mimeType = meta.mimeType || "image/png";
    let sizeBytes = meta.sizeBytes ?? 0;

    // Persisted generated images may have their multi-MB content externalized.
    // Rehydrate the source from the authenticated raw endpoint so Edit always
    // creates a draft with the actual image attached, not just instruction text.
    if (!dataUri.startsWith("data:")) {
      try {
        const response = await fetch(artifactRawUrl(primaryArtifact.id), {
          credentials: "same-origin",
        });
        if (!response.ok) throw new Error(`HTTP ${response.status}`);
        const blob = await response.blob();
        dataUri = await blobAsDataUri(blob);
        mimeType = blob.type || mimeType;
        sizeBytes = blob.size;
      } catch {
        setActionAnnouncement("Source image could not be attached");
        return;
      }
    }

    if (dataUri.startsWith("data:")) {
      const attachment: ChatImageAttachment = {
        artifactId: primaryArtifact.id,
        fileName: getArtifactDownloadFileName(primaryArtifact),
        mimeType,
        sizeBytes,
        dataUri,
        width: meta.width ?? undefined,
        height: meta.height ?? undefined,
        source: "upload",
      };
      chatActions.attachImage(attachment);
      setActionAnnouncement("Source image attached for editing");
    }
  };

  const handleRegenerate = () => {
    const settings = describeRequest(args);
    const draft = promptText
      ? settings
        ? `${promptText}\n(${settings})`
        : promptText
      : "";
    if (draft) chatActions.insertDraftText?.(draft);
  };

  const liveRegions = (
    <>
      <span role="status" aria-live="polite" className="sr-only">
        {announcement}
      </span>
      <span role="status" aria-live="polite" className="sr-only">
        {actionAnnouncement}
      </span>
    </>
  );

  const promptSummary = promptText ? (
    <p className="mt-2 line-clamp-2 text-[11px] text-muted-foreground" title={promptText}>
      {promptText}
    </p>
  ) : null;

  // ----- Render per state -----
  if (state.kind === "queued") {
    return (
      <CardShell state="queued">
        <ImageFrame ratio={requestedRatio}>
          <div className="absolute inset-0 motion-safe:animate-pulse bg-muted/50" />
          <div className="absolute inset-0 flex flex-col items-center justify-center gap-1.5 text-muted-foreground">
            <ImageIcon size={22} aria-hidden />
            <span className="text-xs">Preparing image</span>
          </div>
        </ImageFrame>
        {promptSummary}
        {liveRegions}
      </CardShell>
    );
  }

  if (state.kind === "generating") {
    const stage = generatedImageStage(progressEvent);
    const stageLabel = stage ? generatedImageStageLabel(stage) : "Generating image";
    const requestLine = describeRequest(args);
    return (
      <CardShell state="generating">
        <ImageFrame ratio={requestedRatio}>
          <div className="absolute inset-0 overflow-hidden bg-muted/40">
            <div className="h-full w-1/3 bg-gradient-to-r from-transparent via-foreground/10 to-transparent motion-safe:animate-[shimmer_1.5s_ease-in-out_infinite]" />
          </div>
          <div className="absolute inset-0 flex flex-col items-center justify-center gap-1.5 text-muted-foreground">
            <Loader2 size={20} className="motion-safe:animate-spin" aria-hidden />
            <span className="text-xs font-medium text-foreground/80">{stageLabel}</span>
          </div>
        </ImageFrame>
        <div className="mt-2 flex items-center justify-between gap-2 text-[11px] text-muted-foreground">
          <span className="min-w-0 truncate">{requestLine || "Image generation"}</span>
          <span className="shrink-0 font-mono tabular-nums">
            {formatElapsedColon(elapsedMs)}
          </span>
        </div>
        {promptSummary}
        {liveRegions}
      </CardShell>
    );
  }

  if (state.kind === "cancelled") {
    return (
      <CardShell state="cancelled">
        <div className="flex items-center gap-2">
          <StatusPill status="cancelled" subject="Image generation" />
          <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
            Image generation cancelled
          </span>
          {promptText && (
            <IconAction label="Copy prompt" onClick={handleCopyPrompt}>
              {copied ? (
                <Check size={15} className="text-emerald-500" aria-hidden />
              ) : (
                <Copy size={15} aria-hidden />
              )}
            </IconAction>
          )}
        </div>
        {liveRegions}
      </CardShell>
    );
  }

  if (state.kind === "interrupted") {
    return (
      <CardShell state="interrupted">
        <div className="flex items-start gap-2">
          <ImageOff size={16} className="mt-0.5 shrink-0 text-muted-foreground" aria-hidden />
          <div className="min-w-0 flex-1">
            <p className="text-xs font-medium text-foreground">Generation status unavailable</p>
            <p className="mt-0.5 text-[11px] text-muted-foreground">
              The run ended before its final image result reached this chat.
            </p>
          </div>
        </div>
        {promptSummary}
        <div className="mt-2 flex items-center gap-1">
          <IconAction label="Copy prompt" disabled={!promptText} onClick={handleCopyPrompt}>
            {copied ? <Check size={15} className="text-emerald-500" aria-hidden /> : <Copy size={15} aria-hidden />}
          </IconAction>
          <IconAction label="Regenerate image" disabled={!promptText} onClick={handleRegenerate}>
            <RefreshCw size={15} aria-hidden />
          </IconAction>
        </div>
        {liveRegions}
      </CardShell>
    );
  }

  if (state.kind === "moderation_blocked") {
    return (
      <CardShell state="moderation_blocked">
        <div className="flex items-start gap-2">
          <ShieldAlert size={16} className="mt-0.5 shrink-0 text-amber-500" aria-hidden />
          <div className="min-w-0 flex-1">
            <p className="text-xs font-medium text-foreground">Blocked by content policy</p>
            <p className="mt-0.5 text-[11px] text-muted-foreground">
              This request could not be generated. Adjust the prompt and try a
              different description.
            </p>
          </div>
        </div>
        {promptSummary}
        <div className="mt-2 flex items-center gap-1">
          <button
            type="button"
            onClick={handleEdit}
            className="inline-flex items-center gap-1.5 rounded-md border border-border/60 bg-background/40 px-2.5 py-1 text-[11px] font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Pencil size={13} aria-hidden /> Edit prompt
          </button>
          {promptText && (
            <IconAction label="Copy prompt" onClick={handleCopyPrompt}>
              {copied ? (
                <Check size={15} className="text-emerald-500" aria-hidden />
              ) : (
                <Copy size={15} aria-hidden />
              )}
            </IconAction>
          )}
        </div>
        {liveRegions}
      </CardShell>
    );
  }

  if (state.kind === "failed") {
    const category = state.category;
    const isAuth = category === "auth";
    const isInvalid = category === "invalid_input";
    const title = isAuth
      ? "Image target needs attention"
      : category === "rate_limited"
        ? "Rate limited"
        : isInvalid
          ? "Invalid request"
          : "Image generation failed";
    const detail = errorMessage(
      envelope,
      isAuth
        ? "Check the image generation target in settings."
        : category === "rate_limited"
          ? "The image provider is rate limiting requests. Try again shortly."
          : "The image provider could not complete this request.",
    );
    return (
      <CardShell state="failed" errorCategory={category}>
        <p className="text-xs font-medium text-foreground">{title}</p>
        <p className="mt-0.5 text-[11px] text-muted-foreground">{detail}</p>
        {promptSummary}
        <div className="mt-2 flex flex-wrap items-center gap-1">
          {isAuth ? (
            chatActions.openImageSettings ? (
              <button
                type="button"
                onClick={() => chatActions.openImageSettings?.()}
                className="inline-flex items-center gap-1.5 rounded-md border border-border/60 bg-background/40 px-2.5 py-1 text-[11px] font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <Settings size={13} aria-hidden /> Open image settings
              </button>
            ) : null
          ) : isInvalid ? (
            <button
              type="button"
              onClick={handleEdit}
              className="inline-flex items-center gap-1.5 rounded-md border border-border/60 bg-background/40 px-2.5 py-1 text-[11px] font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <Pencil size={13} aria-hidden /> Edit prompt
            </button>
          ) : (
            <button
              type="button"
              onClick={handleRegenerate}
              disabled={!promptText}
              className="inline-flex items-center gap-1.5 rounded-md border border-border/60 bg-background/40 px-2.5 py-1 text-[11px] font-medium text-foreground transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-40"
            >
              <RefreshCw size={13} aria-hidden /> Regenerate
            </button>
          )}
          {promptText && (
            <IconAction label="Copy prompt" onClick={handleCopyPrompt}>
              {copied ? (
                <Check size={15} className="text-emerald-500" aria-hidden />
              ) : (
                <Copy size={15} aria-hidden />
              )}
            </IconAction>
          )}
        </div>
        {liveRegions}
      </CardShell>
    );
  }

  // ----- Completed (single or gallery) -----
  const isGallery = resolved.length > 1;
  const pinned = !!(primaryArtifact && pinnedArtifactIds?.includes(primaryArtifact.id));
  const metaParts: string[] = [];
  if (meta.width && meta.height) metaParts.push(`${meta.width}×${meta.height}`);
  if (meta.format) metaParts.push(meta.format.toUpperCase());
  if (meta.quality && meta.quality !== "auto") metaParts.push(meta.quality);
  if (meta.model) metaParts.push(meta.model);
  const provenance = meta.model
    ? meta.provider
      ? `Generated with ${meta.model} via ${meta.provider}`
      : `Generated with ${meta.model}`
    : "";

  return (
    <CardShell state="completed">
      {isGallery ? (
        <GeneratedImageGallery
          toolCall={toolCall}
          artifacts={resolved}
          onOpenArtifact={onOpenArtifact}
          onPinArtifact={onPinArtifact}
          pinnedArtifactIds={pinnedArtifactIds}
          onDownload={handleDownload}
        />
      ) : (
        <button
          type="button"
          aria-label="Open image"
          onClick={() => primaryArtifact && onOpenArtifact(primaryArtifact)}
          disabled={!primaryArtifact}
          className="group relative block w-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-md"
        >
          <ImageFrame ratio={meta.width && meta.height ? meta.width / meta.height : requestedRatio}>
            <CompletedImage artifact={primaryArtifact} id={resolved[0]?.id || ""} />
          </ImageFrame>
        </button>
      )}

      {!isGallery && metaParts.length > 0 && (
        <p className="mt-2 truncate text-[11px] text-muted-foreground">
          {metaParts.join(" · ")}
        </p>
      )}
      {!compact && provenance && (
        <p className="mt-0.5 truncate text-[11px] text-muted-foreground/70">{provenance}</p>
      )}

      <div className="mt-2 flex flex-wrap items-center gap-0.5">
        {!isGallery && (
          <IconAction
            label="Open image"
            disabled={!primaryArtifact}
            onClick={() => primaryArtifact && onOpenArtifact(primaryArtifact)}
          >
            <ExternalLink size={15} aria-hidden />
          </IconAction>
        )}
        {onPinArtifact && !isGallery && (
          <IconAction
            label="Use in chat"
            active={pinned}
            disabled={!primaryArtifact}
            onClick={() => primaryArtifact && onPinArtifact(primaryArtifact)}
          >
            {pinned ? (
              <MessageSquareDashed size={15} aria-hidden />
            ) : (
              <MessageSquarePlus size={15} aria-hidden />
            )}
          </IconAction>
        )}
        {!isGallery && (
          <IconAction
            label="Download image"
            disabled={!primaryArtifact}
            onClick={() => primaryArtifact && handleDownload(primaryArtifact)}
          >
            <Download size={15} aria-hidden />
          </IconAction>
        )}
        <IconAction label="Copy prompt" disabled={!promptText} onClick={handleCopyPrompt}>
          {copied ? (
            <Check size={15} className="text-emerald-500" aria-hidden />
          ) : (
            <Copy size={15} aria-hidden />
          )}
        </IconAction>
        <IconAction label="Edit prompt" onClick={handleEdit}>
          <Pencil size={15} aria-hidden />
        </IconAction>
        <IconAction label="Regenerate image" disabled={!promptText} onClick={handleRegenerate}>
          <RefreshCw size={15} aria-hidden />
        </IconAction>
        {primaryArtifact && <DetailsPopover artifact={primaryArtifact} />}
      </div>
      {liveRegions}
    </CardShell>
  );
};

export default GeneratedImageCard;
