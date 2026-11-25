import React from "react";
import {
  CheckCircle2,
  ChevronDown,
  ChevronUp,
  Clock,
  Database,
  FileCode2,
  LayoutDashboard,
  ListTree,
  Lock,
  Maximize2,
  Monitor,
  MousePointerClick,
  PanelRightClose,
  PanelRightOpen,
  Scan,
  ShieldCheck,
  Smartphone,
  Tablet,
  Wrench,
  X,
} from "lucide-react";
import InspectorOverlay from "@/components/InspectorOverlay";
import SandboxedHtmlRenderer from "@/components/renderers/type/SandboxedHtmlRenderer";
import { cn } from "@/lib/utils";
import {
  DASHBOARD_SAMPLES,
  type DashboardSample,
} from "./dashboardProposalFixtures";

// /dev/audit/dashboard — design proposals for the generated-HTML artifact
// journey: compact inline receipt -> expanded inline preview -> full
// dashboard. Each proposal is fully clickable against real sandboxed
// artifacts; nothing here is production behavior.

// ---------------------------------------------------------------------------
// Formatting helpers
// ---------------------------------------------------------------------------

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function formatAgo(ms: number): string {
  const minutes = Math.max(0, Math.round((Date.now() - ms) / 60_000));
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  return `${hours}h ago`;
}

function formatClock(ms: number): string {
  return new Date(ms).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
  });
}

function shortToolName(toolName: string): string {
  return toolName === "create_interactive_data_artifact"
    ? "interactive data artifact"
    : "interactive artifact";
}

function kindLabel(sample: DashboardSample): string {
  return sample.artifact.metadata.artifact_kind === "interactive_data_html"
    ? "Interactive data HTML"
    : "Interactive HTML";
}

// ---------------------------------------------------------------------------
// Shared primitives
// ---------------------------------------------------------------------------

const ACTION_BTN =
  "inline-flex shrink-0 items-center gap-1.5 rounded-md border border-border bg-background px-2.5 py-1.5 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1";

const PRIMARY_BTN =
  "inline-flex shrink-0 items-center gap-1.5 rounded-md border border-primary/40 bg-primary/10 px-2.5 py-1.5 text-xs font-semibold text-primary transition-colors hover:bg-primary/20 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1";

const ICON_BTN =
  "inline-flex shrink-0 items-center justify-center rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1";

// Transparent full-surface click target layered above an inert preview. The
// hover pill is rendered inside so the open affordance never shifts layout.
const CLICK_CATCH =
  "group absolute inset-0 z-10 cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset";

const MetaChip: React.FC<{
  icon?: React.ComponentType<{ size?: number; className?: string }>;
  className?: string;
  children: React.ReactNode;
}> = ({ icon: Icon, className, children }) => (
  <span
    className={cn(
      "inline-flex min-w-0 items-center gap-1 rounded-md border border-border/50 bg-muted/30 px-1.5 py-0.5 text-[11px] font-medium text-muted-foreground",
      className,
    )}
  >
    {Icon && <Icon size={11} className="shrink-0" />}
    <span className="truncate">{children}</span>
  </span>
);

const SafetyChip: React.FC<{ short?: boolean }> = ({ short }) => (
  <MetaChip
    icon={ShieldCheck}
    className="border-emerald-500/25 bg-emerald-500/10 text-emerald-400"
  >
    {short ? "Sandboxed" : "Sandboxed · No network"}
  </MetaChip>
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
 * A live render of the artifact, transform-scaled down into a reserved box —
 * the "minimap" treatment. Inert by default (pointer-events off, removed from
 * the a11y/tab tree via inert) so it can never trap scroll or focus; the
 * surrounding control owns the interaction.
 */
const ScaledThumbnail: React.FC<{
  sample: DashboardSample;
  height: number;
  interactive?: boolean;
  className?: string;
  testId?: string;
}> = ({ sample, height, interactive = false, className, testId }) => {
  const [ref, width] = useElementWidth<HTMLDivElement>();
  const scale = width > 0 ? width / sample.designWidth : 0;
  return (
    <div
      ref={ref}
      className={cn("relative overflow-hidden bg-white", className)}
      style={{ height }}
      aria-hidden={interactive ? undefined : true}
      inert={interactive ? undefined : true}
      data-testid={testId}
    >
      {scale > 0 ? (
        <div
          className={interactive ? undefined : "pointer-events-none select-none"}
          style={{
            width: sample.designWidth,
            height: Math.ceil(height / scale),
            transform: `scale(${scale})`,
            transformOrigin: "top left",
          }}
        >
          <SandboxedHtmlRenderer
            artifact={sample.artifact}
            className="h-full min-h-0 rounded-none border-0"
          />
        </div>
      ) : (
        <div className="h-full w-full animate-pulse bg-muted/20" />
      )}
    </div>
  );
};

const PEEK_INNER_HEIGHT = 760;

/**
 * Top-anchored 1:1 peek at the document, clipped at the bottom. When
 * interactive, the renderer is sized to the visible box so the document
 * scrolls inside it; when inert, the document is clipped and untouchable.
 */
const ClippedPeek: React.FC<{
  sample: DashboardSample;
  height: number;
  interactive?: boolean;
  className?: string;
  testId?: string;
}> = ({ sample, height, interactive = false, className, testId }) => (
  <div
    className={cn("relative overflow-hidden bg-white", className)}
    style={{ height }}
    aria-hidden={interactive ? undefined : true}
    inert={interactive ? undefined : true}
    data-testid={testId}
  >
    <div
      className={interactive ? undefined : "pointer-events-none select-none"}
      style={{ height: interactive ? height : PEEK_INNER_HEIGHT }}
    >
      <SandboxedHtmlRenderer
        artifact={sample.artifact}
        className="h-full min-h-0 rounded-none border-0"
      />
    </div>
  </div>
);

const InteractToggle: React.FC<{
  on: boolean;
  onToggle: () => void;
}> = ({ on, onToggle }) => (
  <button
    type="button"
    aria-pressed={on}
    onClick={onToggle}
    className={cn(ACTION_BTN, on && "border-primary/40 bg-primary/10 text-primary")}
    data-testid="inline-html-interact-toggle"
  >
    <MousePointerClick size={13} />
    Interact
  </button>
);

// ---------------------------------------------------------------------------
// Full-dashboard overlays (one flavor per proposal)
// ---------------------------------------------------------------------------

const OverlayHeader: React.FC<{
  sample: DashboardSample;
  onClose: () => void;
  extras?: React.ReactNode;
}> = ({ sample, onClose, extras }) => (
  <header className="flex h-12 shrink-0 items-center gap-3 border-b border-border bg-card/60 px-4">
    <span className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md bg-artifact-code/10 text-artifact-code">
      <LayoutDashboard size={15} />
    </span>
    <h2 className="min-w-0 truncate text-sm font-semibold">
      {sample.artifact.title}
    </h2>
    <div className="hidden min-w-0 items-center gap-1.5 lg:flex">
      <MetaChip icon={Clock}>
        {formatClock(sample.generatedAtMs)} · {formatAgo(sample.generatedAtMs)}
      </MetaChip>
      <MetaChip icon={FileCode2}>
        {formatBytes(Number(sample.artifact.metadata.byte_size))}
      </MetaChip>
      <MetaChip icon={Wrench}>{shortToolName(sample.toolName)}</MetaChip>
      <SafetyChip />
    </div>
    <div className="ml-auto flex shrink-0 items-center gap-1.5">
      {extras}
      <button
        type="button"
        onClick={onClose}
        className={ICON_BTN}
        aria-label="Close dashboard"
        data-testid="inline-html-overlay-close"
      >
        <X size={16} />
      </button>
    </div>
  </header>
);

/** P1 full mode — viewport width presets for auditing responsiveness. */
const ViewportOverlay: React.FC<{
  sample: DashboardSample;
  onClose: () => void;
}> = ({ sample, onClose }) => {
  const [preset, setPreset] = React.useState<
    "fit" | "desktop" | "tablet" | "mobile"
  >("fit");
  const presets = [
    { key: "fit", label: "Fit window", Icon: Scan, width: 0 },
    { key: "desktop", label: "Desktop · 1280px", Icon: Monitor, width: 1280 },
    { key: "tablet", label: "Tablet · 834px", Icon: Tablet, width: 834 },
    { key: "mobile", label: "Mobile · 390px", Icon: Smartphone, width: 390 },
  ] as const;
  const active = presets.find((p) => p.key === preset) ?? presets[0];
  return (
    <InspectorOverlay onClose={onClose}>
      <div
        className="flex h-full min-h-0 flex-col"
        data-testid="inline-html-overlay"
      >
        <OverlayHeader
          sample={sample}
          onClose={onClose}
          extras={
            <span className="flex items-center gap-0.5 rounded-md border border-border bg-background p-0.5">
              {presets.map(({ key, label, Icon }) => (
                <button
                  key={key}
                  type="button"
                  aria-pressed={preset === key}
                  aria-label={label}
                  title={label}
                  onClick={() => setPreset(key)}
                  className={cn(
                    "rounded p-1.5 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    preset === key
                      ? "bg-primary/15 text-primary"
                      : "text-muted-foreground hover:bg-muted hover:text-foreground",
                  )}
                >
                  <Icon size={14} />
                </button>
              ))}
            </span>
          }
        />
        <div className="min-h-0 flex-1 overflow-auto bg-muted/20">
          {preset === "fit" ? (
            <SandboxedHtmlRenderer
              artifact={sample.artifact}
              className="h-full min-h-0 rounded-none border-0"
            />
          ) : (
            <div className="flex h-full justify-center px-6 py-5">
              <div
                className="h-full shrink-0 overflow-hidden rounded-lg border border-border shadow-2xl shadow-black/40"
                style={{ width: active.width }}
              >
                <SandboxedHtmlRenderer
                  artifact={sample.artifact}
                  className="h-full min-h-0 rounded-none border-0"
                />
              </div>
            </div>
          )}
        </div>
      </div>
    </InspectorOverlay>
  );
};

/** P2 full mode — immersive: the chrome bar can collapse out of the way. */
const ImmersiveOverlay: React.FC<{
  sample: DashboardSample;
  onClose: () => void;
}> = ({ sample, onClose }) => {
  const [chromeHidden, setChromeHidden] = React.useState(false);
  return (
    <InspectorOverlay onClose={onClose}>
      <div
        className="flex h-full min-h-0 flex-col"
        data-testid="inline-html-overlay"
      >
        {!chromeHidden && (
          <OverlayHeader
            sample={sample}
            onClose={onClose}
            extras={
              <button
                type="button"
                onClick={() => setChromeHidden(true)}
                className={ICON_BTN}
                aria-label="Hide toolbar"
                data-testid="immersive-hide-toolbar"
              >
                <ChevronUp size={16} />
              </button>
            }
          />
        )}
        <div className="relative min-h-0 flex-1">
          <SandboxedHtmlRenderer
            artifact={sample.artifact}
            className="h-full min-h-0 rounded-none border-0"
          />
          {chromeHidden && (
            <div className="absolute right-3 top-3 flex items-center gap-1.5">
              <button
                type="button"
                onClick={() => setChromeHidden(false)}
                className="rounded-full border border-border bg-background/85 p-2 text-muted-foreground shadow-lg backdrop-blur transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                aria-label="Show toolbar"
                data-testid="immersive-show-toolbar"
              >
                <ChevronDown size={14} />
              </button>
              <button
                type="button"
                onClick={onClose}
                className="rounded-full border border-border bg-background/85 p-2 text-muted-foreground shadow-lg backdrop-blur transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                aria-label="Close dashboard"
              >
                <X size={14} />
              </button>
            </div>
          )}
        </div>
      </div>
    </InspectorOverlay>
  );
};

const RailRow: React.FC<{ label: string; children: React.ReactNode }> = ({
  label,
  children,
}) => (
  <div className="flex items-baseline justify-between gap-3 py-1">
    <dt className="shrink-0 text-xs text-muted-foreground">{label}</dt>
    <dd className="min-w-0 truncate text-right text-xs font-medium text-foreground">
      {children}
    </dd>
  </div>
);

const RailHeading: React.FC<{
  icon: React.ComponentType<{ size?: number; className?: string }>;
  children: React.ReactNode;
}> = ({ icon: Icon, children }) => (
  <p className="mb-1.5 mt-5 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground first:mt-0">
    <Icon size={12} />
    {children}
  </p>
);

/** P3 full mode — the receipt promoted to a persistent metadata rail. */
const RailOverlay: React.FC<{
  sample: DashboardSample;
  onClose: () => void;
}> = ({ sample, onClose }) => {
  const [railOpen, setRailOpen] = React.useState(true);
  return (
    <InspectorOverlay onClose={onClose}>
      <div
        className="flex h-full min-h-0 flex-col"
        data-testid="inline-html-overlay"
      >
        <OverlayHeader
          sample={sample}
          onClose={onClose}
          extras={
            <button
              type="button"
              aria-pressed={railOpen}
              onClick={() => setRailOpen((v) => !v)}
              className={ICON_BTN}
              aria-label={railOpen ? "Hide details panel" : "Show details panel"}
              data-testid="rail-toggle"
            >
              {railOpen ? (
                <PanelRightClose size={16} />
              ) : (
                <PanelRightOpen size={16} />
              )}
            </button>
          }
        />
        <div className="flex min-h-0 flex-1">
          <div className="min-w-0 flex-1">
            <SandboxedHtmlRenderer
              artifact={sample.artifact}
              className="h-full min-h-0 rounded-none border-0"
            />
          </div>
          {railOpen && (
            <aside
              className="w-[300px] shrink-0 overflow-y-auto border-l border-border bg-card/40 px-4 py-4"
              data-testid="inline-html-overlay-rail"
            >
              <RailHeading icon={LayoutDashboard}>Artifact</RailHeading>
              <dl>
                <RailRow label="Generated">
                  {formatClock(sample.generatedAtMs)} ·{" "}
                  {formatAgo(sample.generatedAtMs)}
                </RailRow>
                <RailRow label="Source tool">
                  {shortToolName(sample.toolName)}
                </RailRow>
                <RailRow label="Build time">
                  {(sample.durationMs / 1000).toFixed(1)}s
                </RailRow>
                <RailRow label="Size">
                  {formatBytes(Number(sample.artifact.metadata.byte_size))}
                </RailRow>
                <RailRow label="Kind">{kindLabel(sample)}</RailRow>
                <RailRow label="Status">
                  <span className="text-emerald-400">Rendered</span>
                </RailRow>
              </dl>

              {sample.dataSource && (
                <>
                  <RailHeading icon={Database}>Data source</RailHeading>
                  <dl>
                    <RailRow label="Dataset">{sample.dataSource.title}</RailRow>
                    <RailRow label="Rows">
                      {sample.dataSource.rowCount.toLocaleString()}
                    </RailRow>
                    <RailRow label="Columns">
                      {sample.dataSource.columnCount}
                    </RailRow>
                  </dl>
                </>
              )}

              <RailHeading icon={ShieldCheck}>Safety</RailHeading>
              <ul className="space-y-1.5 text-xs text-muted-foreground">
                <li className="flex items-center gap-2">
                  <ShieldCheck size={13} className="shrink-0 text-emerald-400" />
                  Sandboxed iframe (allow-scripts only)
                </li>
                <li className="flex items-center gap-2">
                  <Lock size={13} className="shrink-0 text-emerald-400" />
                  Network blocked · strict CSP enforced
                </li>
              </ul>

              <RailHeading icon={ListTree}>Outline</RailHeading>
              <ol className="space-y-1">
                {sample.sections.map((section, i) => (
                  <li
                    key={section}
                    className="flex items-baseline gap-2 text-xs text-foreground/90"
                  >
                    <span className="w-4 shrink-0 text-right font-mono text-[10px] text-muted-foreground">
                      {i + 1}
                    </span>
                    {section}
                  </li>
                ))}
              </ol>
            </aside>
          )}
        </div>
      </div>
    </InspectorOverlay>
  );
};

/** P4 full mode — a headline stat strip docked under the header. */
const KpiStripOverlay: React.FC<{
  sample: DashboardSample;
  onClose: () => void;
}> = ({ sample, onClose }) => {
  const cells: Array<{ label: string; value: React.ReactNode }> = [
    {
      label: "Generated",
      value: `${formatClock(sample.generatedAtMs)} · ${formatAgo(sample.generatedAtMs)}`,
    },
    { label: "Size", value: formatBytes(Number(sample.artifact.metadata.byte_size)) },
    { label: "Sections", value: sample.sections.length },
    ...(sample.dataSource
      ? [
          {
            label: "Source rows",
            value: `${sample.dataSource.rowCount.toLocaleString()} × ${sample.dataSource.columnCount} cols`,
          },
        ]
      : []),
    { label: "Tool", value: shortToolName(sample.toolName) },
    {
      label: "Status",
      value: <span className="text-emerald-400">Rendered</span>,
    },
  ];
  return (
    <InspectorOverlay onClose={onClose}>
      <div
        className="flex h-full min-h-0 flex-col"
        data-testid="inline-html-overlay"
      >
        <OverlayHeader sample={sample} onClose={onClose} />
        <div
          className="grid shrink-0 grid-cols-3 gap-px border-b border-border bg-border/40 md:grid-cols-6"
          data-testid="overlay-kpi-strip"
        >
          {cells.map((cell) => (
            <div key={cell.label} className="bg-card/70 px-4 py-2.5">
              <div className="text-[10px] font-semibold uppercase tracking-wide text-muted-foreground">
                {cell.label}
              </div>
              <div className="mt-0.5 truncate text-xs font-medium text-foreground">
                {cell.value}
              </div>
            </div>
          ))}
        </div>
        <div className="min-h-0 flex-1">
          <SandboxedHtmlRenderer
            artifact={sample.artifact}
            className="h-full min-h-0 rounded-none border-0"
          />
        </div>
      </div>
    </InspectorOverlay>
  );
};

// ---------------------------------------------------------------------------
// P1 — Minimap: scaled live thumbnail
// ---------------------------------------------------------------------------

const MinimapDemo: React.FC<{ sample: DashboardSample }> = ({ sample }) => {
  const [expanded, setExpanded] = React.useState(false);
  const [interact, setInteract] = React.useState(false);
  const [open, setOpen] = React.useState(false);
  const live = expanded && interact;
  const height = expanded ? 440 : 208;
  const toggleExpanded = () =>
    setExpanded((prev) => {
      if (prev) setInteract(false);
      return !prev;
    });

  return (
    <>
      <div
        className="overflow-hidden rounded-lg border border-border/60 bg-card/50"
        data-testid="proposal-minimap-card"
      >
        <div className="relative border-b border-border/40">
          <ScaledThumbnail
            sample={sample}
            height={height}
            interactive={live}
            className="motion-safe:transition-[height] motion-safe:duration-200 motion-safe:ease-out"
          />
          {!live && (
            <button
              type="button"
              onClick={() => setOpen(true)}
              className={CLICK_CATCH}
              aria-label={`Open ${sample.artifact.title} dashboard`}
              data-testid="proposal-minimap-open"
            >
              <span className="pointer-events-none absolute inset-0 flex items-center justify-center">
                <span className="flex items-center gap-1.5 rounded-full border border-border bg-background/90 px-3 py-1.5 text-xs font-semibold text-foreground opacity-0 shadow-lg backdrop-blur group-hover:opacity-100 group-focus-visible:opacity-100 motion-safe:transition-opacity motion-safe:duration-150">
                  <Maximize2 size={13} />
                  Open dashboard
                </span>
              </span>
            </button>
          )}
          <span className="pointer-events-none absolute right-2 top-2 z-20">
            {live ? (
              <MetaChip className="border-primary/40 bg-primary/15 text-primary backdrop-blur">
                Live — interactions on
              </MetaChip>
            ) : (
              <MetaChip className="border-border/70 bg-background/85 backdrop-blur">
                Preview
              </MetaChip>
            )}
          </span>
        </div>
        <div className="flex items-center gap-3 px-3 py-2.5">
          <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-artifact-code/10 text-artifact-code">
            <LayoutDashboard size={17} />
          </span>
          <div className="min-w-0 flex-1">
            <h3 className="truncate text-sm font-medium text-card-foreground">
              {sample.artifact.title}
            </h3>
            <div className="mt-1 flex flex-wrap items-center gap-1.5">
              <MetaChip>{kindLabel(sample)}</MetaChip>
              <MetaChip icon={FileCode2}>
                {formatBytes(Number(sample.artifact.metadata.byte_size))}
              </MetaChip>
              <MetaChip icon={Clock}>{formatAgo(sample.generatedAtMs)}</MetaChip>
              <SafetyChip short />
            </div>
          </div>
          {expanded && (
            <InteractToggle on={interact} onToggle={() => setInteract((v) => !v)} />
          )}
          <button
            type="button"
            aria-expanded={expanded}
            onClick={toggleExpanded}
            className={ACTION_BTN}
            data-testid="inline-html-expand-toggle"
          >
            {expanded ? "Collapse" : "Expand"}
            <ChevronDown
              size={13}
              className={cn(
                "motion-safe:transition-transform motion-safe:duration-200",
                expanded && "rotate-180",
              )}
            />
          </button>
          <button
            type="button"
            onClick={() => setOpen(true)}
            className={PRIMARY_BTN}
          >
            <Maximize2 size={13} />
            Open
          </button>
        </div>
      </div>
      {open && <ViewportOverlay sample={sample} onClose={() => setOpen(false)} />}
    </>
  );
};

// ---------------------------------------------------------------------------
// P2 — Mini window: chrome bar + top-of-document peek with bottom scrim
// ---------------------------------------------------------------------------

const WindowPeekDemo: React.FC<{ sample: DashboardSample }> = ({ sample }) => {
  const [expanded, setExpanded] = React.useState(false);
  const [interact, setInteract] = React.useState(false);
  const [open, setOpen] = React.useState(false);
  const live = expanded && interact;
  const height = expanded ? 432 : 192;
  const scrimHeight = expanded ? 28 : 72;
  const toggleExpanded = () =>
    setExpanded((prev) => {
      if (prev) setInteract(false);
      return !prev;
    });

  return (
    <>
      <div
        className="overflow-hidden rounded-lg border border-border/60 bg-card/50"
        data-testid="proposal-window-card"
      >
        <div className="flex h-9 items-center gap-2 border-b border-border/60 bg-muted/30 px-3">
          <FileCode2 size={13} className="shrink-0 text-artifact-code" />
          <span className="min-w-0 truncate text-xs font-medium text-card-foreground">
            {sample.artifact.title}
          </span>
          <MetaChip icon={Clock} className="hidden sm:inline-flex">
            {formatAgo(sample.generatedAtMs)}
          </MetaChip>
          <SafetyChip short />
          <span className="ml-auto flex items-center gap-1">
            {expanded && (
              <InteractToggle on={interact} onToggle={() => setInteract((v) => !v)} />
            )}
            <button
              type="button"
              aria-expanded={expanded}
              onClick={toggleExpanded}
              className={ICON_BTN}
              aria-label={expanded ? "Collapse preview" : "Expand preview"}
              data-testid="inline-html-expand-toggle"
            >
              <ChevronDown
                size={14}
                className={cn(
                  "motion-safe:transition-transform motion-safe:duration-200",
                  expanded && "rotate-180",
                )}
              />
            </button>
            <button
              type="button"
              onClick={() => setOpen(true)}
              className={ICON_BTN}
              aria-label={`Open ${sample.artifact.title} dashboard`}
            >
              <Maximize2 size={14} />
            </button>
          </span>
        </div>

        <div className="relative">
          <ClippedPeek
            sample={sample}
            height={height}
            interactive={live}
            className="motion-safe:transition-[height] motion-safe:duration-200 motion-safe:ease-out"
          />
          {/* Bottom scrim: the "there's more below" signal. */}
          <div
            className="pointer-events-none absolute inset-x-0 bottom-0 z-10 bg-gradient-to-b from-transparent to-card"
            style={{ height: scrimHeight }}
          />
          {!live && (
            <button
              type="button"
              onClick={() => setOpen(true)}
              className={CLICK_CATCH}
              aria-label={`Open ${sample.artifact.title} dashboard`}
              data-testid="proposal-window-open"
            >
              {!expanded && (
                <span className="pointer-events-none absolute inset-x-0 bottom-0 z-20 flex items-end justify-center pb-2.5">
                  <span className="flex items-center gap-1.5 rounded-full border border-border bg-background/90 px-3 py-1 text-xs font-semibold text-foreground shadow-md backdrop-blur transition-colors group-hover:border-primary/50 group-hover:text-primary group-focus-visible:border-primary/50 group-focus-visible:text-primary">
                    Open full dashboard
                    <Maximize2 size={12} />
                  </span>
                </span>
              )}
            </button>
          )}
        </div>
      </div>
      {open && <ImmersiveOverlay sample={sample} onClose={() => setOpen(false)} />}
    </>
  );
};

// ---------------------------------------------------------------------------
// P3 — Native receipt: no iframe in compact, lazy minimap on expand
// ---------------------------------------------------------------------------

const ReceiptDemo: React.FC<{ sample: DashboardSample }> = ({ sample }) => {
  const [expanded, setExpanded] = React.useState(false);
  const [open, setOpen] = React.useState(false);

  return (
    <>
      <div
        className="rounded-lg border border-border/60 bg-card/50"
        data-testid="proposal-receipt-card"
      >
        <div className="flex items-center gap-3 p-3">
          <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-lg bg-artifact-code/10 text-artifact-code">
            <LayoutDashboard size={18} />
          </span>
          <div className="min-w-0 flex-1">
            <h3 className="truncate text-sm font-medium text-card-foreground">
              {sample.artifact.title}
            </h3>
            <div className="mt-1 flex flex-wrap items-center gap-1.5">
              <MetaChip>{kindLabel(sample)}</MetaChip>
              <MetaChip icon={Clock}>{formatAgo(sample.generatedAtMs)}</MetaChip>
              <MetaChip icon={FileCode2}>
                {formatBytes(Number(sample.artifact.metadata.byte_size))}
              </MetaChip>
              {sample.dataSource && (
                <MetaChip icon={Database}>
                  {sample.dataSource.rowCount.toLocaleString()} rows ·{" "}
                  {sample.dataSource.columnCount} cols
                </MetaChip>
              )}
              <SafetyChip short />
            </div>
          </div>
          <button
            type="button"
            aria-expanded={expanded}
            onClick={() => setExpanded((v) => !v)}
            className={ACTION_BTN}
            data-testid="inline-html-expand-toggle"
          >
            Preview
            <ChevronDown
              size={13}
              className={cn(
                "motion-safe:transition-transform motion-safe:duration-200",
                expanded && "rotate-180",
              )}
            />
          </button>
          <button
            type="button"
            onClick={() => setOpen(true)}
            className={PRIMARY_BTN}
          >
            <Maximize2 size={13} />
            Open dashboard
          </button>
        </div>

        {expanded && (
          <div className="border-t border-border/50 p-3 motion-safe:animate-in motion-safe:fade-in motion-safe:duration-200">
            <div className="grid gap-3 md:grid-cols-[200px_minmax(0,1fr)]">
              <div className="min-w-0">
                <p className="mb-2 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
                  <ListTree size={12} />
                  Contents
                </p>
                <ol className="space-y-1.5">
                  {sample.sections.map((section, i) => (
                    <li
                      key={section}
                      className="flex items-baseline gap-2 text-xs text-foreground/90"
                    >
                      <span className="w-4 shrink-0 text-right font-mono text-[10px] text-muted-foreground">
                        {i + 1}
                      </span>
                      {section}
                    </li>
                  ))}
                </ol>
                {sample.dataSource && (
                  <p className="mt-3 flex items-center gap-1.5 text-[11px] text-muted-foreground">
                    <Database size={11} className="shrink-0" />
                    <span className="truncate">{sample.dataSource.title}</span>
                  </p>
                )}
              </div>
              <div className="relative overflow-hidden rounded-md border border-border/60">
                {/* Lazy: the sandbox only boots once the user asks to preview. */}
                <ScaledThumbnail sample={sample} height={336} />
                <button
                  type="button"
                  onClick={() => setOpen(true)}
                  className={CLICK_CATCH}
                  aria-label={`Open ${sample.artifact.title} dashboard`}
                  data-testid="proposal-receipt-open"
                >
                  <span className="pointer-events-none absolute inset-0 flex items-center justify-center">
                    <span className="flex items-center gap-1.5 rounded-full border border-border bg-background/90 px-3 py-1.5 text-xs font-semibold text-foreground opacity-0 shadow-lg backdrop-blur group-hover:opacity-100 group-focus-visible:opacity-100 motion-safe:transition-opacity motion-safe:duration-150">
                      <Maximize2 size={13} />
                      Open dashboard
                    </span>
                  </span>
                </button>
              </div>
            </div>
          </div>
        )}
      </div>
      {open && <RailOverlay sample={sample} onClose={() => setOpen(false)} />}
    </>
  );
};

// ---------------------------------------------------------------------------
// P4 — Split: native summary + thumbnail side by side
// ---------------------------------------------------------------------------

const SplitDemo: React.FC<{ sample: DashboardSample }> = ({ sample }) => {
  const [expanded, setExpanded] = React.useState(false);
  const [interact, setInteract] = React.useState(false);
  const [open, setOpen] = React.useState(false);
  const collapse = () => {
    setExpanded(false);
    setInteract(false);
  };
  const dataLine = sample.dataSource
    ? `${sample.dataSource.rowCount.toLocaleString()} rows · ${sample.dataSource.columnCount} cols · ${sample.dataSource.title}`
    : `${sample.sections.length} sections · self-contained HTML`;

  return (
    <>
      <div
        className="overflow-hidden rounded-lg border border-border/60 bg-card/50"
        data-testid="proposal-split-card"
      >
        {!expanded ? (
          <div className="grid h-[152px] grid-cols-[minmax(0,1fr)_230px]">
            <div className="flex min-w-0 flex-col justify-between p-3">
              <div className="min-w-0">
                <h3 className="truncate text-sm font-medium text-card-foreground">
                  {sample.artifact.title}
                </h3>
                <div className="mt-1.5 flex flex-wrap items-center gap-1.5">
                  <MetaChip>{kindLabel(sample)}</MetaChip>
                  <MetaChip icon={Clock}>{formatAgo(sample.generatedAtMs)}</MetaChip>
                  <SafetyChip short />
                </div>
              </div>
              <p className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
                <Database size={12} className="shrink-0" />
                <span className="truncate">{dataLine}</span>
              </p>
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  aria-expanded={false}
                  onClick={() => setExpanded(true)}
                  className={ACTION_BTN}
                  data-testid="inline-html-expand-toggle"
                >
                  Expand
                  <ChevronDown size={13} />
                </button>
                <button
                  type="button"
                  onClick={() => setOpen(true)}
                  className={PRIMARY_BTN}
                >
                  <Maximize2 size={13} />
                  Open
                </button>
              </div>
            </div>
            <div className="relative border-l border-border/40">
              <ScaledThumbnail sample={sample} height={150} />
              <button
                type="button"
                onClick={() => setOpen(true)}
                className={CLICK_CATCH}
                aria-label={`Open ${sample.artifact.title} dashboard`}
                data-testid="proposal-split-open"
              >
                <span className="pointer-events-none absolute inset-0 flex items-center justify-center">
                  <span className="flex items-center justify-center rounded-full border border-border bg-background/90 p-2 text-foreground opacity-0 shadow-lg backdrop-blur group-hover:opacity-100 group-focus-visible:opacity-100 motion-safe:transition-opacity motion-safe:duration-150">
                    <Maximize2 size={13} />
                  </span>
                </span>
              </button>
            </div>
          </div>
        ) : (
          <>
            <div className="relative border-b border-border/40">
              <ScaledThumbnail
                sample={sample}
                height={380}
                interactive={interact}
              />
              {!interact && (
                <button
                  type="button"
                  onClick={() => setOpen(true)}
                  className={CLICK_CATCH}
                  aria-label={`Open ${sample.artifact.title} dashboard`}
                  data-testid="proposal-split-open"
                >
                  <span className="pointer-events-none absolute inset-0 flex items-center justify-center">
                    <span className="flex items-center gap-1.5 rounded-full border border-border bg-background/90 px-3 py-1.5 text-xs font-semibold text-foreground opacity-0 shadow-lg backdrop-blur group-hover:opacity-100 group-focus-visible:opacity-100 motion-safe:transition-opacity motion-safe:duration-150">
                      <Maximize2 size={13} />
                      Open dashboard
                    </span>
                  </span>
                </button>
              )}
            </div>
            <div className="flex flex-wrap items-center gap-1.5 px-3 py-2.5">
              {sample.sections.map((section) => (
                <MetaChip key={section}>{section}</MetaChip>
              ))}
              <span className="ml-auto flex items-center gap-2">
                <InteractToggle
                  on={interact}
                  onToggle={() => setInteract((v) => !v)}
                />
                <button
                  type="button"
                  aria-expanded
                  onClick={collapse}
                  className={ACTION_BTN}
                  data-testid="inline-html-expand-toggle"
                >
                  Collapse
                  <ChevronDown size={13} className="rotate-180" />
                </button>
                <button
                  type="button"
                  onClick={() => setOpen(true)}
                  className={PRIMARY_BTN}
                >
                  <Maximize2 size={13} />
                  Open
                </button>
              </span>
            </div>
          </>
        )}
      </div>
      {open && <KpiStripOverlay sample={sample} onClose={() => setOpen(false)} />}
    </>
  );
};

// ---------------------------------------------------------------------------
// Page scaffolding
// ---------------------------------------------------------------------------

/** Simulated chat context so each card is judged where it actually lives. */
const ChatSimulation: React.FC<{
  sample: DashboardSample;
  children: React.ReactNode;
}> = ({ sample, children }) => (
  <div className="rounded-lg border border-border bg-card/30 p-4">
    <div className="mb-3 flex items-center gap-2 text-xs text-muted-foreground">
      <CheckCircle2 size={13} className="shrink-0 text-emerald-500" />
      <span className="font-mono">{sample.toolName}</span>
      <span aria-hidden="true">·</span>
      <span>completed in {(sample.durationMs / 1000).toFixed(1)}s</span>
    </div>
    <p className="mb-3 text-sm text-foreground/90">
      Here is the generated dashboard — open it for the full view:
    </p>
    {children}
  </div>
);

interface ProposalDef {
  id: string;
  name: string;
  tagline: string;
  fullModeFlavor: string;
  notes: string[];
  defaultSampleKey: string;
  Demo: React.FC<{ sample: DashboardSample }>;
}

const PROPOSALS: ProposalDef[] = [
  {
    id: "minimap",
    name: "Minimap",
    tagline: "A scaled-down live render — obviously a picture of the result.",
    fullModeFlavor: "Full mode: viewport width presets (fit / desktop / tablet / mobile).",
    notes: [
      "The real document is rendered at design width and transform-scaled into a fixed box, so it reads as a thumbnail, never as the dashboard itself.",
      "Inert by default (no pointer events, removed from the tab order); the entire surface is one click target that opens the full view.",
      "Expand grows the card downward to a legible scale and unlocks an explicit Interact toggle for in-thread poking.",
      "Full mode adds viewport presets so generated HTML can be audited at phone / tablet / desktop widths.",
    ],
    defaultSampleKey: "revenue-pulse",
    Demo: MinimapDemo,
  },
  {
    id: "window",
    name: "Mini window + scrim peek",
    tagline: "Window chrome on top, a clipped peek at the document below.",
    fullModeFlavor: "Full mode: immersive — the toolbar collapses out of the way.",
    notes: [
      "The chrome bar carries the title, freshness and sandbox state, and windows read as openable — the metaphor does the affordance work.",
      "A top-anchored 1:1 peek fades into a bottom scrim: the clearest possible 'this is a peek, there is more' signal.",
      "The 'Open full dashboard' pill is always visible (not hover-gated), centered in the scrim.",
      "Full mode is immersive: the header collapses to floating controls so the dashboard gets the whole window.",
    ],
    defaultSampleKey: "ingestion-ops",
    Demo: WindowPeekDemo,
  },
  {
    id: "receipt",
    name: "Native receipt",
    tagline: "No iframe in the thread at all — a pure metadata receipt.",
    fullModeFlavor: "Full mode: persistent metadata rail beside the dashboard.",
    notes: [
      "Compact is a fixed-height native row: zero sandbox cost, zero layout shift, zero ambiguity about what is preview vs artifact.",
      "Chips surface what matters for repeated agent sessions: kind, freshness, size, bound data, sandbox state.",
      "Preview is opt-in: the minimap + section outline boot lazily only when asked, so long threads stay cheap.",
      "Full mode promotes the receipt into a collapsible details rail: provenance, data source, safety posture and outline next to the live render.",
    ],
    defaultSampleKey: "retention-cohorts",
    Demo: ReceiptDemo,
  },
  {
    id: "split",
    name: "Split summary + thumbnail",
    tagline: "What's inside on the left, what it looks like on the right.",
    fullModeFlavor: "Full mode: headline KPI strip docked under the header.",
    notes: [
      "The asymmetric split itself reads as a receipt: structured summary plus a small visual proof, not the artifact.",
      "Leads with the data lineage (rows × cols × dataset) — the natural fit for create_interactive_data_artifact results.",
      "Expanding inverts the split: a wide thumbnail on top, section pills and controls beneath.",
      "Full mode docks a stat strip (generated, size, sections, source rows, status) above the dashboard so the numbers travel with it.",
    ],
    defaultSampleKey: "revenue-pulse",
    Demo: SplitDemo,
  },
];

const ProposalSection: React.FC<{ proposal: ProposalDef; index: number }> = ({
  proposal,
  index,
}) => {
  const [sampleKey, setSampleKey] = React.useState(proposal.defaultSampleKey);
  const sample =
    DASHBOARD_SAMPLES.find((s) => s.key === sampleKey) ?? DASHBOARD_SAMPLES[0];
  const { Demo } = proposal;
  return (
    <section id={`proposal-${proposal.id}`} className="scroll-mt-8">
      <div className="mb-1 flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        <span className="rounded bg-primary/15 px-1.5 py-0.5 text-xs font-bold text-primary">
          P{index + 1}
        </span>
        <h2 className="text-base font-semibold">{proposal.name}</h2>
        <p className="text-sm text-muted-foreground">{proposal.tagline}</p>
      </div>
      <p className="mb-3 text-xs text-muted-foreground">
        {proposal.fullModeFlavor}
      </p>
      <div className="mb-3 flex flex-wrap items-center gap-1.5">
        <span className="mr-1 text-xs text-muted-foreground">Sample:</span>
        {DASHBOARD_SAMPLES.map((s) => (
          <button
            key={s.key}
            type="button"
            aria-pressed={s.key === sampleKey}
            onClick={() => setSampleKey(s.key)}
            className={cn(
              "rounded-md border px-2.5 py-1 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1",
              s.key === sampleKey
                ? "border-foreground/40 bg-muted text-foreground"
                : "border-border bg-background text-muted-foreground hover:bg-muted hover:text-foreground",
            )}
            data-sample-label={s.label}
          >
            {s.label}
          </button>
        ))}
      </div>
      <ChatSimulation sample={sample}>
        {/* Key on the sample so switching resets expand/interact state. */}
        <Demo key={sample.key} sample={sample} />
      </ChatSimulation>
      <div className="mt-3 rounded-md border border-border/50 bg-card/20 px-4 py-3">
        <p className="mb-1.5 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">
          Design notes
        </p>
        <ul className="grid gap-x-8 gap-y-1 text-xs leading-relaxed text-muted-foreground md:grid-cols-2">
          {proposal.notes.map((note) => (
            <li key={note} className="flex gap-2">
              <span aria-hidden="true" className="text-primary">
                ·
              </span>
              {note}
            </li>
          ))}
        </ul>
      </div>
    </section>
  );
};

export default function DashboardArtifactProposals() {
  return (
    <div className="min-h-screen bg-background text-foreground">
      <main className="mx-auto max-w-4xl px-6 py-10">
        <header className="mb-8">
          <p className="text-xs uppercase tracking-wide text-muted-foreground">
            Local UI fixture ·{" "}
            <a
              href="/dev/audit-trail"
              className="underline decoration-border underline-offset-2 hover:text-foreground"
            >
              audit trail
            </a>
          </p>
          <h1 className="mt-1 text-xl font-semibold">
            Generated dashboard previews — design proposals
          </h1>
          <p className="mt-2 max-w-2xl text-sm text-muted-foreground">
            Four treatments for HTML artifacts produced by{" "}
            <code className="rounded bg-muted/50 px-1 py-0.5 text-xs">
              create_interactive_artifact
            </code>{" "}
            /{" "}
            <code className="rounded bg-muted/50 px-1 py-0.5 text-xs">
              create_interactive_data_artifact
            </code>
            . Each demo is live against real sandboxed artifacts and walks the
            same journey:
          </p>
          <ol className="mt-3 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
            <li className="flex items-center gap-1.5 rounded-md border border-border/60 bg-card/40 px-2 py-1">
              <span className="font-semibold text-foreground">1</span> Compact
              receipt in the thread
            </li>
            <li aria-hidden="true">→</li>
            <li className="flex items-center gap-1.5 rounded-md border border-border/60 bg-card/40 px-2 py-1">
              <span className="font-semibold text-foreground">2</span> Expanded
              inline (Expand / Preview)
            </li>
            <li aria-hidden="true">→</li>
            <li className="flex items-center gap-1.5 rounded-md border border-border/60 bg-card/40 px-2 py-1">
              <span className="font-semibold text-foreground">3</span> Full
              dashboard (click anywhere on the preview, Esc to close)
            </li>
          </ol>
        </header>

        <div className="space-y-10">
          {PROPOSALS.map((proposal, i) => (
            <ProposalSection key={proposal.id} proposal={proposal} index={i} />
          ))}
        </div>

        <footer className="mt-10 border-t border-border/60 pt-4 text-xs text-muted-foreground">
          Previews are inert until you opt in: thumbnails carry no pointer
          events and are removed from the tab order, so the thread never traps
          scroll or focus. All heights are reserved up front — expanding only
          grows cards downward.
        </footer>
      </main>
    </div>
  );
}
