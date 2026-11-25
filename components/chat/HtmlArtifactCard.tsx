import React from "react";
import {
  ChevronDown,
  Clock,
  Code2,
  Database,
  Download,
  FileCode2,
  Maximize2,
  MessageSquareDashed,
  MessageSquarePlus,
  MousePointerClick,
  ShieldCheck,
} from "lucide-react";
import { cn } from "@/lib/utils";
import { formatBytes, formatRelativeTime } from "@/lib/formatters";
import { hasNonCollapsedSelectionWithin } from "@/lib/textSelection";
import type { Artifact } from "@/types";

const SandboxedHtmlRenderer = React.lazy(
  () => import("@/components/renderers/type/SandboxedHtmlRenderer"),
);

// P4 "split summary + thumbnail" treatment for generated sandboxed HTML
// artifacts (create_interactive_artifact / create_interactive_data_artifact).
// Compact: metadata summary beside a scaled live thumbnail; click opens the
// full artifact. Expanded: large preview + section chips, with an explicit
// Interact toggle — the iframe stays inert (no pointer/focus/scroll capture)
// until the user opts in.

const COMPACT_CARD_HEIGHT = 152;
const COMPACT_THUMB_HEIGHT = 150;
const STACKED_THUMB_HEIGHT = 128;
const EXPANDED_THUMB_HEIGHT = 380;
const STACK_BREAKPOINT = 440;
const DEFAULT_DESIGN_WIDTH = 1180;

const ACTION_BTN =
  "inline-flex shrink-0 items-center gap-1.5 rounded-md border border-border bg-background px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1 disabled:cursor-not-allowed disabled:opacity-50";

const PRIMARY_BTN =
  "inline-flex shrink-0 items-center gap-1.5 rounded-md border border-primary/40 bg-primary/10 px-2.5 py-1.5 text-xs font-semibold text-primary transition-colors hover:bg-primary/20 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1";

const GHOST_ICON_BTN =
  "inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted-foreground/40 transition-colors hover:bg-muted hover:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

// Transparent full-surface click target layered above the inert preview. The
// hover pill lives inside it so the open affordance never shifts layout.
const CLICK_CATCH =
  "group/open absolute inset-0 z-10 cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset";

interface HtmlArtifactCardProps {
  artifact: Artifact;
  title: string;
  /** Render the live scaled thumbnail (callers budget how many cards get one). */
  showThumbnail?: boolean;
  onOpenArtifact: (artifact: Artifact) => void;
  onDownload?: () => void;
  onPin?: (artifact: Artifact) => void;
  isPinned?: boolean;
  testId?: string;
}

const MetaChip: React.FC<{
  icon?: React.ComponentType<{ size?: number; className?: string }>;
  className?: string;
  title?: string;
  children: React.ReactNode;
}> = ({ icon: Icon, className, title, children }) => (
  <span
    title={title}
    className={cn(
      "inline-flex min-w-0 items-center gap-1 rounded-md border border-border/50 bg-muted/30 px-1.5 py-0.5 text-[11px] font-medium text-muted-foreground",
      className,
    )}
  >
    {Icon && <Icon size={11} className="shrink-0" />}
    <span className="truncate">{children}</span>
  </span>
);

function useElementWidth<T extends HTMLElement>() {
  const ref = React.useRef<T | null>(null);
  const [width, setWidth] = React.useState(0);
  React.useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const update = () => setWidth(el.clientWidth);
    update();
    const observer = new ResizeObserver(update);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);
  return [ref, width] as const;
}

/**
 * Live render of the artifact, transform-scaled into a reserved box. Inert by
 * default (pointer-events off, removed from the a11y/tab tree) so it can never
 * trap scroll or focus; `interactive` hands the surface to the iframe.
 */
const ScaledThumbnail: React.FC<{
  artifact: Artifact;
  height: number;
  interactive?: boolean;
}> = ({ artifact, height, interactive = false }) => {
  const [ref, width] = useElementWidth<HTMLDivElement>();
  const rawDesignWidth = Number(artifact.metadata?.design_width);
  const designWidth =
    Number.isFinite(rawDesignWidth) && rawDesignWidth >= 320
      ? rawDesignWidth
      : DEFAULT_DESIGN_WIDTH;
  const scale = width > 0 ? width / designWidth : 0;
  return (
    <div
      ref={ref}
      className="relative overflow-hidden bg-white"
      style={{ height }}
      aria-hidden={interactive ? undefined : true}
      inert={interactive ? undefined : true}
      data-testid="inline-html-preview"
    >
      {scale > 0 ? (
        <div
          className={interactive ? undefined : "pointer-events-none select-none"}
          style={{
            width: designWidth,
            height: Math.ceil(height / scale),
            transform: `scale(${scale})`,
            transformOrigin: "top left",
          }}
        >
          <React.Suspense
            fallback={<div className="h-full w-full animate-pulse bg-muted/20" />}
          >
            <SandboxedHtmlRenderer
              artifact={artifact}
              className="h-full min-h-0 rounded-none border-0"
            />
          </React.Suspense>
        </div>
      ) : (
        <div className="h-full w-full animate-pulse bg-muted/20" />
      )}
    </div>
  );
};

function getKindLabel(artifact: Artifact): string {
  return String(artifact.metadata?.artifact_kind || "").trim() ===
    "interactive_data_html"
    ? "Interactive data HTML"
    : "Interactive HTML";
}

function getToolName(artifact: Artifact): string {
  const toolName = String(artifact.metadata?.tool_name || "").trim();
  if (toolName) return toolName;
  return String(artifact.metadata?.artifact_kind || "").trim() ===
    "interactive_data_html"
    ? "create_interactive_data_artifact"
    : "create_interactive_artifact";
}

function getSizeLabel(artifact: Artifact): string {
  const bytes = Number(
    artifact.metadata?.byte_size ??
      artifact.metadata?.sizeBytes ??
      artifact.metadata?.size_bytes ??
      String(artifact.content || "").length,
  );
  return Number.isFinite(bytes) && bytes > 0 ? formatBytes(bytes) : "";
}

function getSections(artifact: Artifact): string[] {
  const raw = artifact.metadata?.sections;
  if (!Array.isArray(raw)) return [];
  return raw
    .filter((s): s is string => typeof s === "string" && s.trim().length > 0)
    .slice(0, 8);
}

function getDataLine(artifact: Artifact): string {
  const metadata = artifact.metadata || {};
  const parts: string[] = [];
  const rowCount = Number(metadata.data_source_row_count);
  if (Number.isFinite(rowCount) && rowCount > 0) {
    parts.push(`${rowCount.toLocaleString()} rows`);
  }
  const cols = Array.isArray(metadata.data_source_columns)
    ? metadata.data_source_columns.length
    : 0;
  if (cols > 0) parts.push(`${cols} cols`);
  const sourceTitle = String(metadata.data_source_title || "").trim();
  if (sourceTitle) parts.push(sourceTitle);
  if (parts.length > 0) return parts.join(" · ");

  const fallback: string[] = [];
  const sections = getSections(artifact);
  if (sections.length > 0) {
    fallback.push(
      `${sections.length} ${sections.length === 1 ? "section" : "sections"}`,
    );
  }
  const size = getSizeLabel(artifact);
  if (size) fallback.push(size);
  fallback.push("self-contained HTML");
  return fallback.join(" · ");
}

const HtmlArtifactCard: React.FC<HtmlArtifactCardProps> = ({
  artifact,
  title,
  showThumbnail = true,
  onOpenArtifact,
  onDownload,
  onPin,
  isPinned,
  testId,
}) => {
  const [expanded, setExpanded] = React.useState(false);
  const [interact, setInteract] = React.useState(false);
  const [cardRef, cardWidth] = useElementWidth<HTMLDivElement>();

  const hasContent =
    artifact.metadata?.placeholder !== true &&
    String(artifact.content || "").length > 0;
  const stacked = cardWidth > 0 && cardWidth < STACK_BREAKPOINT;
  const sideBySide = !expanded && !stacked;
  const thumbHeight = expanded
    ? EXPANDED_THUMB_HEIGHT
    : sideBySide
      ? COMPACT_THUMB_HEIGHT
      : STACKED_THUMB_HEIGHT;
  const interactive = expanded && interact && hasContent;

  const kindLabel = getKindLabel(artifact);
  const toolName = getToolName(artifact);
  const freshness = formatRelativeTime(
    Number(artifact.metadata?.generated_at_ms) || undefined,
  );
  const sizeLabel = getSizeLabel(artifact);
  const sections = getSections(artifact);
  const dataLine = getDataLine(artifact);

  const openArtifact = () => onOpenArtifact(artifact);
  const collapse = () => {
    setExpanded(false);
    setInteract(false);
  };

  const pinButton = onPin && (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        onPin(artifact);
      }}
      aria-label={isPinned ? "Unpin artifact" : "Pin artifact"}
      className={cn(
        GHOST_ICON_BTN,
        isPinned && "text-sky-400 hover:text-sky-400",
      )}
    >
      {isPinned ? (
        <MessageSquareDashed size={14} />
      ) : (
        <MessageSquarePlus size={14} />
      )}
    </button>
  );

  const downloadButton = onDownload && (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        onDownload();
      }}
      aria-label="Download artifact"
      className={GHOST_ICON_BTN}
    >
      <Download size={14} />
    </button>
  );

  const openButton = (
    <button
      type="button"
      onClick={(e) => {
        e.stopPropagation();
        openArtifact();
      }}
      className={PRIMARY_BTN}
    >
      <Maximize2 size={13} />
      Open
    </button>
  );

  const thumbnail = !showThumbnail ? (
    <div
      className="flex flex-col items-center justify-center gap-1.5 bg-muted/20 text-xs text-muted-foreground"
      style={{ height: thumbHeight }}
    >
      <Code2 size={16} className="text-muted-foreground/60" />
      Open to preview
    </div>
  ) : !hasContent ? (
    <div
      className="flex animate-pulse items-center justify-center bg-muted/20 text-xs text-muted-foreground"
      data-testid="inline-html-preview-loading"
      style={{ height: thumbHeight }}
    >
      Loading HTML preview
    </div>
  ) : (
    <ScaledThumbnail
      artifact={artifact}
      height={thumbHeight}
      interactive={interactive}
    />
  );

  return (
    <div
      ref={cardRef}
      data-testid={testId}
      onClick={(e) => {
        if (interactive) return;
        if (hasNonCollapsedSelectionWithin(e.currentTarget as HTMLElement)) {
          return;
        }
        openArtifact();
      }}
      className={cn(
        "my-2 min-w-0 max-w-full overflow-hidden rounded-lg border bg-card/50 transition-colors",
        !interactive && "cursor-pointer hover:bg-card",
        isPinned
          ? "border-sky-400/50 hover:border-sky-400/70 shadow-[0_0_12px_-4px_hsl(200_80%_60%/0.2)]"
          : "border-border/40 hover:border-border/70",
      )}
    >
      <div
        className={cn(
          "grid",
          sideBySide
            ? "grid-cols-[minmax(0,1fr)_230px]"
            : "grid-cols-[minmax(0,1fr)]",
        )}
        style={sideBySide ? { height: COMPACT_CARD_HEIGHT } : undefined}
      >
        {/* Thumbnail zone — kept mounted across compact/expanded so the
            sandbox doesn't reload (and lose document state) on toggle. */}
        <div
          key="thumb"
          className={cn(
            "relative min-w-0",
            sideBySide
              ? "order-2 border-l border-border/40"
              : "order-1 border-b border-border/40",
          )}
        >
          {thumbnail}
          {!interactive && (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation();
                openArtifact();
              }}
              className={CLICK_CATCH}
              aria-label={`Open ${title}`}
              data-testid="html-artifact-thumbnail-open"
            >
              <span className="pointer-events-none absolute inset-0 flex items-center justify-center">
                <span
                  className={cn(
                    "flex items-center gap-1.5 rounded-full border border-border bg-background/90 text-foreground opacity-0 shadow-lg backdrop-blur group-hover/open:opacity-100 group-focus-visible/open:opacity-100 motion-safe:transition-opacity motion-safe:duration-150",
                    expanded
                      ? "px-3 py-1.5 text-xs font-semibold"
                      : "p-2",
                  )}
                >
                  <Maximize2 size={13} />
                  {expanded ? "Open artifact" : null}
                </span>
              </span>
            </button>
          )}
        </div>

        {/* Summary zone (compact) / controls strip (expanded) */}
        <div
          key="summary"
          className={cn("min-w-0", sideBySide ? "order-1" : "order-2")}
        >
          {!expanded ? (
            <div
              className={cn(
                "flex h-full min-w-0 flex-col p-3",
                sideBySide ? "justify-between" : "gap-2.5",
              )}
            >
              <div className="min-w-0">
                <div className="flex min-w-0 items-start justify-between gap-2">
                  <h4 className="min-w-0 truncate text-sm font-medium text-card-foreground">
                    {title}
                  </h4>
                  <span className="flex shrink-0 items-center">
                    {downloadButton}
                    {pinButton}
                  </span>
                </div>
                <div className="mt-1.5 flex flex-nowrap items-center gap-1.5 overflow-hidden">
                  <MetaChip icon={Code2} title={toolName}>
                    {kindLabel}
                  </MetaChip>
                  {freshness && <MetaChip icon={Clock}>{freshness}</MetaChip>}
                  <MetaChip
                    icon={ShieldCheck}
                    className="border-emerald-500/25 bg-emerald-500/10 text-emerald-400"
                  >
                    Sandboxed
                  </MetaChip>
                </div>
              </div>
              <p className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
                <Database size={12} className="shrink-0" />
                <span className="truncate">{dataLine}</span>
              </p>
              <div className="flex flex-nowrap items-center gap-2 overflow-hidden">
                <button
                  type="button"
                  aria-expanded={false}
                  disabled={!hasContent || !showThumbnail}
                  onClick={(e) => {
                    e.stopPropagation();
                    setExpanded(true);
                  }}
                  className={ACTION_BTN}
                  data-testid="inline-html-expand-toggle"
                >
                  Expand
                  <ChevronDown size={13} />
                </button>
                {openButton}
              </div>
            </div>
          ) : (
            <div className="flex flex-wrap items-center gap-1.5 px-3 py-2.5">
              <MetaChip icon={Code2} title={toolName}>
                {kindLabel}
              </MetaChip>
              {sizeLabel && <MetaChip icon={FileCode2}>{sizeLabel}</MetaChip>}
              {sections.map((section) => (
                <MetaChip key={section}>{section}</MetaChip>
              ))}
              <span className="ml-auto flex flex-nowrap items-center gap-1.5">
                {downloadButton}
                {pinButton}
                <button
                  type="button"
                  aria-pressed={interact}
                  onClick={(e) => {
                    e.stopPropagation();
                    setInteract((v) => !v);
                  }}
                  className={cn(
                    ACTION_BTN,
                    interact && "border-primary/40 bg-primary/10 text-primary",
                  )}
                  data-testid="inline-html-interact-toggle"
                >
                  <MousePointerClick size={13} />
                  Interact
                </button>
                <button
                  type="button"
                  aria-expanded
                  onClick={(e) => {
                    e.stopPropagation();
                    collapse();
                  }}
                  className={ACTION_BTN}
                  data-testid="inline-html-expand-toggle"
                >
                  Collapse
                  <ChevronDown size={13} className="rotate-180" />
                </button>
                {openButton}
              </span>
            </div>
          )}
        </div>
      </div>
    </div>
  );
};

export default HtmlArtifactCard;
