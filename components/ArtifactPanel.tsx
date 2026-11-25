import React, { useState, useMemo } from "react";
import {
  X,
  ChevronDown,
  Code2,
  Database,
  Download,
  FileText,
  ImageIcon,
  Loader2,
  MessageSquarePlus,
  MessageSquareText,
} from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import { Separator } from "@/components/ui/separator";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import type { AppSettings, Artifact, QueryMetadata } from "@/types";
import {
  fileProvenanceFromArtifact,
  fileSourceBadgeClass,
  type FileArtifactProvenance,
} from "@/lib/fileProvenance";
import FileProvenanceDetails from "@/components/files/FileProvenanceDetails";
import { fileKindGlyph } from "@/lib/fileKindGlyph";
import { formatBytes } from "@/lib/formatters";
import DataVisualization from "./DataVisualization";
import SchemaViewer from "./SchemaViewer";
import QueryInfoPanel from "./QueryInfoPanel";
import { resolveRenderer } from "./renderers";
import RawViewer from "./renderers/shared/RawViewer";
import ImageRenderer from "./renderers/type/ImageRenderer";
import {
  getGeneratedImageMetadata,
  isGeneratedImageArtifact,
} from "@/lib/generatedImageArtifacts";
import {
  canDownloadArtifact,
  downloadArtifact,
  downloadArtifactRawBytes,
  getArtifactDownloadFileName,
  isRawDownloadArtifact,
} from "@/lib/artifactDownload";

interface ArtifactPanelProps {
  artifact: Artifact | null;
  settings: AppSettings;
  onClose: () => void;
  onPinArtifact?: (artifact: Artifact) => void;
  isPinned?: boolean;
  fileProvenance?: FileArtifactProvenance | null;
  onOpenOriginThread?: (threadId: string, toolCallId?: string) => void;
}

const ArtifactPanel: React.FC<ArtifactPanelProps> = ({
  artifact,
  settings,
  onClose,
  onPinArtifact,
  isPinned,
  fileProvenance,
  onOpenOriginThread,
}) => {
  const [rawMode, setRawMode] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [copyAnnouncement, setCopyAnnouncement] = useState("");
  const copyAnnouncementTimer = React.useRef<number | null>(null);

  React.useEffect(() => () => {
    if (copyAnnouncementTimer.current !== null) {
      window.clearTimeout(copyAnnouncementTimer.current);
    }
  }, []);

  const announceCopy = React.useCallback((fieldLabel: string) => {
    setCopyAnnouncement(`Copied ${fieldLabel.toLowerCase()}`);
    if (copyAnnouncementTimer.current !== null) {
      window.clearTimeout(copyAnnouncementTimer.current);
    }
    copyAnnouncementTimer.current = window.setTimeout(
      () => setCopyAnnouncement(""),
      1200,
    );
  }, []);

  const copyField = React.useCallback(
    async (value: string, label: string) => {
      try {
        await navigator.clipboard.writeText(value);
        announceCopy(label);
      } catch {
        // Clipboard denied — nothing to announce.
      }
    },
    [announceCopy],
  );

  const rendererEntry = useMemo(() => {
    if (!artifact) return null;
    return resolveRenderer(artifact);
  }, [artifact]);
  const derivedFileProvenance = useMemo(() => {
    if (fileProvenance) return fileProvenance;
    if (!artifact) return null;
    return fileProvenanceFromArtifact(artifact, {
      downloadable: isRawDownloadArtifact(artifact) || canDownloadArtifact(artifact),
    });
  }, [artifact, fileProvenance]);

  if (!artifact) return null;

  const isImage = artifact.type === "image";
  const isGeneratedImage = isImage && isGeneratedImageArtifact(artifact);
  // Normalized across snake_case/camelCase so a generated image's header line
  // and safe Raw view read the same fields regardless of backend spelling.
  const imageMeta = isImage ? getGeneratedImageMetadata(artifact) : null;
  // Raw mode for an image must NEVER dump the (multi-MB) data-URI `content`.
  // Show a compact metadata JSON view instead.
  const imageRawJson =
    isImage && imageMeta
      ? JSON.stringify(
          {
            id: artifact.id,
            title: artifact.title,
            type: artifact.type,
            mimeType: imageMeta.mimeType || undefined,
            contentLength:
              typeof artifact.content === "string" ? artifact.content.length : 0,
            ...(isGeneratedImage ? { generated: imageMeta } : {}),
          },
          null,
          2,
        )
      : "";
  const isFile = artifact.type === "file";
  const queryMetadata: QueryMetadata | undefined =
    artifact.metadata?.query_metadata;
  const hasQueryMetadata = !!queryMetadata;
  // Renderers that own their own chrome (e.g. Query Tool Inspector) skip
  // the panel's QueryMetadata tab wrapper — they render schema + query info
  // inline already.
  const rendererOwnsChrome = rendererEntry?.ownsChrome === true;
  const rowCount = artifact.metadata?.row_count;
  // Stored uploads keep only preview text in `content`; their download must
  // stream the original bytes from /raw instead of saving the preview.
  const rawDownload = isRawDownloadArtifact(artifact);
  const showRawToggle = !(rawDownload && isFile);
  const effectiveRawMode = showRawToggle && rawMode;
  const artifactCanDownload = rawDownload || canDownloadArtifact(artifact);
  const provenance = derivedFileProvenance;
  const originThreadId = String(provenance?.threadId || "").trim();
  const originToolCallId = String(provenance?.toolCallId || "").trim();
  const canOpenOriginThread = Boolean(originThreadId && onOpenOriginThread);

  const handleDownload = async () => {
    if (!rawDownload) {
      downloadArtifact(artifact);
      return;
    }
    if (downloading) return;
    setDownloading(true);
    try {
      await downloadArtifactRawBytes(
        artifact.id,
        getArtifactDownloadFileName(artifact),
      );
    } catch {
      // A generated image whose bytes live in the data-URI `content` can still
      // be saved if /raw is unavailable (e.g. not yet persisted). Fall back to
      // the content download instead of stranding the user on a toast.
      const content =
        typeof artifact.content === "string" ? artifact.content : "";
      if (isGeneratedImage && /^data:/i.test(content)) {
        try {
          downloadArtifact(artifact);
          return;
        } catch {
          // fall through to the error toast below
        }
      }
      toast.error(`Couldn't download ${artifact.title || "file"}.`);
    } finally {
      setDownloading(false);
    }
  };

  const RendererIcon = rendererEntry?.icon;
  const rendererLabel = rendererEntry?.label;
  const fallbackIcon = isImage
    ? ImageIcon
    : isFile
      ? FileText
    : artifact.type === "data"
      ? Database
      : Code2;
  const fallbackLabel = isImage
    ? "Image"
    : isFile
      ? "File"
    : artifact.type === "data"
      ? "Interactive Data"
      : "Source Code";

  // Files keep the same kind glyph the audit rows use, so the header
  // visually confirms what was clicked — the 'file' renderer's generic icon
  // would flatten spreadsheets/images/code into one doc glyph.
  const HeaderIcon = isFile
    ? fileKindGlyph(provenance?.fileKind, provenance?.mimeType, artifact.title)
    : RendererIcon || fallbackIcon;
  const headerLabel = rendererLabel || fallbackLabel;
  const hasPlotSpec = Boolean(artifact.metadata?.plot_spec);
  const iconBg = isImage
    ? "bg-purple-500/10 text-purple-400"
    : isFile
      ? "bg-primary/10 text-primary"
    : hasPlotSpec
      ? "bg-artifact-chart/10 text-artifact-chart"
      : artifact.type === "data"
        ? "bg-artifact-data/10 text-artifact-data"
        : "bg-artifact-code/10 text-artifact-code";

  const RenderedContent = rendererEntry?.component;

  const renderContent = () => {
    if (effectiveRawMode) {
      return (
        <RawViewer content={artifact.content} language={artifact.language} />
      );
    }
    if (RenderedContent) {
      return (
        <RenderedContent
          key={artifact.id}
          artifact={artifact}
          toolResult={artifact.metadata?.raw_result}
          settings={settings}
        />
      );
    }
    // Final fallback (no renderer matched)
    if (artifact.type === "data") {
      return (
        <DataVisualization
          key={artifact.id}
          dataString={artifact.content}
          title={artifact.title}
          className="h-full"
          plotSpec={artifact.metadata?.plot_spec}
          chartEngine={artifact.metadata?.chart_engine}
          artifactId={artifact.id}
          partial={
            artifact.metadata?.partial === true ||
            artifact.metadata?.preview_only === true
          }
          fullArtifactId={
            typeof artifact.metadata?.full_artifact_id === "string"
              ? artifact.metadata.full_artifact_id
              : undefined
          }
          rowCountTotal={
            typeof artifact.metadata?.row_count_total === "number"
              ? artifact.metadata.row_count_total
              : undefined
          }
          downsampleRatio={
            typeof artifact.metadata?.downsample_ratio === "number"
              ? artifact.metadata.downsample_ratio
              : undefined
          }
          chartBudget={artifact.metadata?.chart_budget}
          dimensionFiltersEnabled={
            settings?.featureFlags?.chartDimensionFilters === true
          }
        />
      );
    }
    return (
      <RawViewer content={artifact.content} language={artifact.language} />
    );
  };

  return (
    <div className="h-full w-full flex flex-col bg-background relative animate-fade-in">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-border bg-card/95 backdrop-blur">
        <div className="flex items-center gap-3 overflow-hidden">
          <div className={`p-2 rounded-lg ${iconBg}`}>
            <HeaderIcon size={18} />
          </div>
          <div className="flex flex-col overflow-hidden">
            <h2
              className="text-sm font-semibold text-foreground truncate"
              title={artifact.title}
            >
              {artifact.title}
            </h2>
            <p className="text-[10px] text-muted-foreground uppercase tracking-wider font-medium">
              {headerLabel}
            </p>
            {isImage && imageMeta && (
              <p className="text-[10px] text-muted-foreground">
                {imageMeta.width && imageMeta.height
                  ? `${imageMeta.width} x ${imageMeta.height} · `
                  : ""}
                {imageMeta.format ? imageMeta.format.toUpperCase() : ""}
                {imageMeta.sizeBytes
                  ? ` · ${formatBytes(imageMeta.sizeBytes)}`
                  : ""}
              </p>
            )}
            {isFile && artifact.metadata && (
              <p className="text-[10px] text-muted-foreground">
                {typeof artifact.metadata.detectedMimeType === "string"
                  ? artifact.metadata.detectedMimeType
                  : typeof artifact.metadata.mimeType === "string"
                    ? artifact.metadata.mimeType
                    : ""}
                {artifact.metadata.sizeBytes
                  ? ` · ${formatBytes(artifact.metadata.sizeBytes)}`
                  : ""}
                {artifact.metadata.previewTruncated === true
                  ? " · preview truncated"
                  : ""}
              </p>
            )}
            <p
              className="text-[10px] font-mono text-muted-foreground break-all select-text"
              title={artifact.id}
            >
              {artifact.id}
            </p>
            {provenance && (
              <div className="mt-1 flex flex-wrap items-center gap-1.5 text-[10px] text-muted-foreground">
                <Badge
                  variant="outline"
                  className={`h-5 px-1.5 text-[10px] ${fileSourceBadgeClass(provenance.sourceLabel)}`}
                >
                  {provenance.sourceLabel}
                </Badge>
                {provenance.threadTitle && (
                  <span className="max-w-[220px] truncate">
                    from {provenance.threadTitle}
                  </span>
                )}
                {provenance.sourceToolName && (
                  <span className="max-w-[220px] truncate font-mono">
                    {provenance.sourceToolName}
                  </span>
                )}
                <Popover>
                  <PopoverTrigger asChild>
                    <button
                      type="button"
                      className="flex h-6 items-center gap-0.5 rounded-sm text-[10px] text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
                      aria-label="File details"
                    >
                      Details
                      <ChevronDown size={11} aria-hidden="true" />
                    </button>
                  </PopoverTrigger>
                  <PopoverContent
                    align="end"
                    className="max-h-[60vh] w-[22rem] overflow-auto p-3"
                    // Escape should dismiss only the popover, not the whole
                    // inspector overlay listening on window keydown.
                    onEscapeKeyDown={(event) => event.stopPropagation()}
                  >
                    <FileProvenanceDetails
                      provenance={provenance}
                      variant="popover"
                      onCopied={announceCopy}
                    />
                  </PopoverContent>
                </Popover>
              </div>
            )}
            <div className="sr-only" aria-live="polite">
              {copyAnnouncement}
            </div>
          </div>
        </div>
        <div className="flex items-center gap-1.5">
          {canOpenOriginThread && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => onOpenOriginThread?.(originThreadId, originToolCallId || undefined)}
                  className="h-8 gap-1.5 border-border/50 text-xs text-muted-foreground transition-colors hover:border-primary/30 hover:text-primary"
                >
                  <MessageSquareText size={14} />
                  Origin chat
                </Button>
              </TooltipTrigger>
              <TooltipContent>Open the chat where this file was used</TooltipContent>
            </Tooltip>
          )}

          {showRawToggle && (
            <Tooltip>
              <TooltipTrigger asChild>
                {/* A toggle, not a mode readout: constant label + aria-pressed
                    (the source-filter chip pattern) so the accessible name
                    doesn't shift under state and SRs announce on/off. */}
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setRawMode((v) => !v)}
                  aria-pressed={rawMode}
                  className={`text-xs gap-1.5 h-8 border-border/50 transition-colors
                    ${
                      rawMode
                        ? "text-primary border-primary/30 bg-primary/5"
                        : "text-muted-foreground hover:text-primary hover:border-primary/30"
                    }`}
                >
                  <Code2 size={14} aria-hidden="true" />
                  Raw
                </Button>
              </TooltipTrigger>
              <TooltipContent>
                {rawMode ? "Showing raw content — press to show the rendered view" : "Show raw content"}
              </TooltipContent>
            </Tooltip>
          )}

          {onPinArtifact && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => onPinArtifact(artifact)}
                  className={`text-xs gap-1.5 h-8 border-border/50 transition-colors
                    ${
                      isPinned
                        ? "text-accent border-accent/30 bg-accent/5"
                        : "text-muted-foreground hover:text-accent hover:border-accent/30"
                    }`}
                >
                  <MessageSquarePlus size={14} />
                  {isPinned ? "Referenced" : "Reference"}
                </Button>
              </TooltipTrigger>
              <TooltipContent>
                {isPinned ? "Remove from chat input" : "Add to chat input"}
              </TooltipContent>
            </Tooltip>
          )}
          {artifactCanDownload && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={handleDownload}
                  aria-disabled={downloading || undefined}
                  aria-busy={downloading}
                  className="text-xs gap-1.5 h-8 border-border/50 text-muted-foreground hover:text-foreground hover:border-primary/30"
                >
                  {downloading ? (
                    <Loader2 size={14} className="motion-safe:animate-spin" aria-hidden="true" />
                  ) : (
                    <Download size={14} aria-hidden="true" />
                  )}
                  Download
                </Button>
              </TooltipTrigger>
              <TooltipContent>
                {rawDownload
                  ? "Download the original file"
                  : "Download this artifact"}
              </TooltipContent>
            </Tooltip>
          )}
          <Button
            variant="ghost"
            size="icon"
            onClick={onClose}
            aria-label="Close inspector"
            className="text-muted-foreground hover:text-foreground"
          >
            <X size={18} />
          </Button>
        </div>
      </div>

      <Separator />

      {/* Content */}
      {isImage ? (
        <div className="flex flex-1 flex-col overflow-hidden">
          {effectiveRawMode ? (
            <div className="flex-1 overflow-hidden p-4">
              <RawViewer content={imageRawJson} language="json" />
            </div>
          ) : (
            <>
              <div className="min-h-0 flex-1 overflow-hidden">
                <ImageRenderer artifact={artifact} settings={settings} />
              </div>
              {isGeneratedImage && imageMeta && (
                <div className="max-h-[45%] shrink-0 space-y-3 overflow-auto border-t border-border bg-card/40 p-4 text-xs">
                  {imageMeta.prompt && (
                    <div>
                      <div className="mb-1 flex items-center justify-between gap-2">
                        <span className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
                          Prompt
                        </span>
                        <button
                          type="button"
                          onClick={() => copyField(imageMeta.prompt, "prompt")}
                          className="rounded px-1.5 py-0.5 text-[10px] text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          Copy
                        </button>
                      </div>
                      <p className="whitespace-pre-wrap break-words text-foreground/90">
                        {imageMeta.prompt}
                      </p>
                    </div>
                  )}
                  {imageMeta.revisedPrompt &&
                    imageMeta.revisedPrompt !== imageMeta.prompt && (
                      <div>
                        <span className="mb-1 block text-[10px] font-medium uppercase tracking-wider text-muted-foreground">
                          Revised prompt
                        </span>
                        <p className="whitespace-pre-wrap break-words text-foreground/80">
                          {imageMeta.revisedPrompt}
                        </p>
                      </div>
                    )}
                  <dl className="grid grid-cols-[auto,1fr] gap-x-3 gap-y-1 text-[11px]">
                    {imageMeta.model && (
                      <>
                        <dt className="text-muted-foreground">Model</dt>
                        <dd className="break-words text-foreground/85">
                          {imageMeta.model}
                          {imageMeta.provider ? ` · ${imageMeta.provider}` : ""}
                        </dd>
                      </>
                    )}
                    {imageMeta.quality && (
                      <>
                        <dt className="text-muted-foreground">Quality</dt>
                        <dd className="text-foreground/85">{imageMeta.quality}</dd>
                      </>
                    )}
                    {imageMeta.width && imageMeta.height && (
                      <>
                        <dt className="text-muted-foreground">Size</dt>
                        <dd className="text-foreground/85">
                          {imageMeta.width} × {imageMeta.height}
                          {imageMeta.format ? ` · ${imageMeta.format.toUpperCase()}` : ""}
                          {imageMeta.sizeBytes ? ` · ${formatBytes(imageMeta.sizeBytes)}` : ""}
                        </dd>
                      </>
                    )}
                    {imageMeta.createdAt && (
                      <>
                        <dt className="text-muted-foreground">Created</dt>
                        <dd className="text-foreground/85">{imageMeta.createdAt}</dd>
                      </>
                    )}
                    {imageMeta.requestId && (
                      <>
                        <dt className="text-muted-foreground">Request ID</dt>
                        <dd className="break-all font-mono text-foreground/80">
                          {imageMeta.requestId}
                        </dd>
                      </>
                    )}
                    {imageMeta.runId && (
                      <>
                        <dt className="text-muted-foreground">Run ID</dt>
                        <dd className="break-all font-mono text-foreground/80">
                          {imageMeta.runId}
                        </dd>
                      </>
                    )}
                    {imageMeta.parentArtifactId && (
                      <>
                        <dt className="text-muted-foreground">Parent artifact</dt>
                        <dd className="break-all font-mono text-foreground/80">
                          {imageMeta.parentArtifactId}
                        </dd>
                      </>
                    )}
                    {imageMeta.sourceArtifactIds.length > 0 && (
                      <>
                        <dt className="text-muted-foreground">Source artifacts</dt>
                        <dd className="break-all font-mono text-foreground/80">
                          {imageMeta.sourceArtifactIds.join(", ")}
                        </dd>
                      </>
                    )}
                  </dl>
                </div>
              )}
            </>
          )}
        </div>
      ) : hasQueryMetadata && !rendererOwnsChrome ? (
        <Tabs
          defaultValue="data"
          className="flex-1 flex flex-col overflow-hidden"
        >
          <div className="px-4 pt-2">
            <TabsList className="h-8">
              <TabsTrigger value="data" className="text-xs px-3 py-1">
                Data & Charts
              </TabsTrigger>
              {queryMetadata!.schema && queryMetadata!.schema.length > 0 && (
                <TabsTrigger value="schema" className="text-xs px-3 py-1">
                  Schema
                </TabsTrigger>
              )}
              <TabsTrigger value="info" className="text-xs px-3 py-1">
                Query Info
              </TabsTrigger>
            </TabsList>
          </div>

          <TabsContent value="data" className="flex-1 overflow-hidden p-4 mt-0">
            {renderContent()}
          </TabsContent>

          {queryMetadata!.schema && queryMetadata!.schema.length > 0 && (
            <TabsContent
              value="schema"
              className="flex-1 overflow-hidden p-4 mt-0"
            >
              <div className="h-full bg-background rounded-lg border border-border overflow-hidden">
                <SchemaViewer schema={queryMetadata!.schema} />
              </div>
            </TabsContent>
          )}

          <TabsContent value="info" className="flex-1 overflow-hidden mt-0">
            <QueryInfoPanel metadata={queryMetadata!} rowCount={rowCount} />
          </TabsContent>
        </Tabs>
      ) : (
        <div className="flex-1 overflow-hidden p-4">{renderContent()}</div>
      )}
    </div>
  );
};

export default React.memo(ArtifactPanel);
