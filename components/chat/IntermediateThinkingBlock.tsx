import React from "react";
import { Brain, ChevronRight, Clock, Loader2 } from "lucide-react";
import { Collapsible, CollapsibleContent } from "@/components/ui/collapsible";
import type {
  UIMessage,
  Artifact,
  ChatFileAttachment,
  MessageBlockReference,
  ThreadReference,
  ToolProgressEvent,
  ToolResultEnvelope,
  ToolCall,
  EnsureArtifactLoaded,
} from "@/types";
import { isSandboxedHtmlArtifact } from "@/types";
import { isQueryRunToolName } from "@/lib/queryProviderRegistry";
import { getQueryCardStateKey } from "@/lib/queryCardStateKey";
import { isVisibleThinkingStepMessage } from "@/lib/intermediateThinking";
import { deriveThinkingStepIsActive } from "@/lib/thinkingStepState";
import {
  buildMessageTextReference,
  normalizeMessageId,
  normalizeMessageSeq,
} from "@/lib/messageReferences";
import CollapsedToolsSummary from "./CollapsedToolsSummary";
import ArtifactCard from "./ArtifactCard";
import FileAttachmentCard from "./FileAttachmentCard";
import HtmlArtifactCard from "./HtmlArtifactCard";
import MarkdownRenderer from "./MarkdownRenderer";
import MessageReferenceButton from "./MessageReferenceButton";
import ReferencedMessageChips from "./ReferencedMessageChips";
import ReferencedThreadChips from "./ReferencedThreadChips";
import {
  canDownloadArtifact,
  downloadArtifact,
  isRawDownloadArtifact,
} from "@/lib/artifactDownload";
import {
  fileAttachmentSnapshotArtifact,
  fileArtifactCardPresentation,
} from "@/lib/attachmentPresentation";

const INLINE_HTML_PREVIEW_LIMIT = 2;

// -----------------------------------------------------------------------
// Types
// -----------------------------------------------------------------------

export interface ThinkingStep {
  message: UIMessage;
  messageIdentityKey: string;
  referencedMessages?: MessageBlockReference[];
  referencedThreads?: ThreadReference[];
  isLastMessage?: boolean;
}

interface IntermediateThinkingBlockProps {
  steps: ThinkingStep[];
  isThreadRunActive?: boolean;
  jiraBaseUrl?: string;
  onOpenArtifact: (a: Artifact) => void;
  onPinArtifact?: (a: Artifact) => void;
  pinnedArtifactIds?: string[];
  onToggleMessageReference?: (ref: MessageBlockReference) => void;
  pinnedMessageReferenceIds?: string[];
  onToggleThreadReference?: (ref: ThreadReference) => void;
  pinnedThreadReferenceIds?: string[];
  toolProgressByKey?: Record<string, ToolProgressEvent>;
  sqlToolResultsByScopedKey?: Map<
    string,
    {
      envelope: ToolResultEnvelope;
      artifact?: Artifact;
      resultMessageId?: string;
      resultMessageSeq?: number;
      resultMessageIdentityKey?: string;
    }
  >;
  artifactsById?: Record<string, Artifact>;
  ensureArtifactLoaded?: EnsureArtifactLoaded;
  toolResultsByCallId?: Map<
    string,
    { envelope?: ToolResultEnvelope; artifact?: Artifact; rawContent?: string; fileAttachments?: ChatFileAttachment[] }
  >;
}

// -----------------------------------------------------------------------
// Step title derivation
// -----------------------------------------------------------------------

function extractStepTitle(text: string): string | null {
  if (!text) return null;
  let cleaned = text
    .replace(
      /^(I'd be happy to|I'll|I will|I can|Let me|Sure[!,.]?\s*|Certainly[!,.]?\s*|Of course[!,.]?\s*|Great[!,.]?\s*|Now,?\s*)/i,
      "",
    )
    .trim();
  if (!cleaned) return null;
  const match = cleaned.match(/^[^.!?\n]+[.!?]?/);
  let sentence = match ? match[0].trim() : cleaned;
  sentence = sentence.replace(/\.$/, "");
  if (sentence.length > 65) sentence = sentence.slice(0, 62).trimEnd() + "…";
  return sentence.charAt(0).toUpperCase() + sentence.slice(1);
}

function deriveFromTools(toolCalls: ToolCall[]): string {
  const names = new Set(toolCalls.map((tc) => tc.name));
  if (names.has("build_chart_artifact")) return "Building chart";
  if (names.has("display_table")) return "Preparing visualization";
  if (names.has("read_file")) return "Reading data";
  if (names.has("list_files") || names.has("search_files"))
    return "Exploring files";
  if ([...names].some((n) => isQueryRunToolName(n))) return "Running query";
  if (
    names.has("get_echarts_capabilities") ||
    names.has("get_highcharts_capabilities")
  )
    return "Checking chart capabilities";
  if (names.has("get_artifact")) return "Retrieving artifact";
  return "Working";
}

/** Combined step text exactly as StepRow displays it (stream prefix/suffix,
    final content, or thinking trace). */
function getStepDisplayText(message: UIMessage): string {
  const stablePrefix =
    typeof (message as any).streamStablePrefix === "string"
      ? (message as any).streamStablePrefix
      : "";
  const unstableSuffix =
    typeof (message as any).streamUnstableSuffix === "string"
      ? (message as any).streamUnstableSuffix
      : "";
  const rawContent =
    typeof message.content === "string" ? message.content.trim() : "";
  const thinkingText =
    typeof (message as any).thinking === "string"
      ? (message as any).thinking.trim()
      : "";
  return stablePrefix || unstableSuffix
    ? `${stablePrefix}${unstableSuffix}`.trim()
    : rawContent || thinkingText;
}

/** Tool-only steps render their tool-activity cards directly in the flow —
    never inside the StepRow "Working/Done" wrapper, INCLUDING while the
    tools are still streaming/running: the card group's own pinned live
    header is the progress UI. Trade-off (accepted): in the rare case a
    still-streaming step grows text after its tool calls, the segment
    reflows once from bare cards into the text stack — preferable to ever
    showing the old wrapper around live tool activity. */
function isToolOnlyStep(message: UIMessage): boolean {
  if (!message.tool_calls || message.tool_calls.length === 0) return false;
  return !getStepDisplayText(message);
}

function stripToolCallsFromStep(step: ThinkingStep): ThinkingStep {
  return {
    ...step,
    message: {
      ...step.message,
      tool_calls: [],
    },
  };
}

const thinkingStepOpenStateByKey = new Map<string, boolean>();

function normalizeThinkingStepStateKey(value: string): string {
  return value.trim();
}

function getInitialThinkingStepOpenState(stateKey: string): boolean {
  if (!stateKey) return true;
  const existing = thinkingStepOpenStateByKey.get(stateKey);
  if (typeof existing === "boolean") return existing;
  thinkingStepOpenStateByKey.set(stateKey, true);
  return true;
}

// -----------------------------------------------------------------------
// Single step row
// -----------------------------------------------------------------------

interface StepRowProps {
  step: ThinkingStep;
  isLatestVisibleStep: boolean;
  isThreadRunActive?: boolean;
  jiraBaseUrl?: string;
  sqlToolResultsByScopedKey?: IntermediateThinkingBlockProps["sqlToolResultsByScopedKey"];
  toolResultsByCallId?: IntermediateThinkingBlockProps["toolResultsByCallId"];
  toolProgressByKey?: IntermediateThinkingBlockProps["toolProgressByKey"];
  artifactsById?: Record<string, Artifact>;
  ensureArtifactLoaded?: EnsureArtifactLoaded;
  onOpenArtifact: (a: Artifact) => void;
  onPinArtifact?: (a: Artifact) => void;
  pinnedArtifactIds?: string[];
  onToggleMessageReference?: (ref: MessageBlockReference) => void;
  pinnedMessageReferenceIdSet?: Set<string>;
  onToggleThreadReference?: (ref: ThreadReference) => void;
  pinnedThreadReferenceIdSet?: Set<string>;
}

const StepRow: React.FC<StepRowProps> = ({
  step,
  isLatestVisibleStep,
  isThreadRunActive,
  jiraBaseUrl,
  sqlToolResultsByScopedKey,
  toolResultsByCallId,
  toolProgressByKey,
  artifactsById,
  ensureArtifactLoaded,
  onOpenArtifact,
  onPinArtifact,
  pinnedArtifactIds,
  onToggleMessageReference,
  pinnedMessageReferenceIdSet,
  onToggleThreadReference,
  pinnedThreadReferenceIdSet,
}) => {
  const {
    message,
    messageIdentityKey,
    referencedMessages = [],
    referencedThreads = [],
    isLastMessage,
  } = step;
  const thinkingStepStateKey = React.useMemo(() => {
    const normalizedIdentity = normalizeThinkingStepStateKey(messageIdentityKey);
    return normalizedIdentity ? `thinking-step:${normalizedIdentity}` : "";
  }, [messageIdentityKey]);
  const [isOpen, setIsOpen] = React.useState<boolean>(() =>
    getInitialThinkingStepOpenState(thinkingStepStateKey),
  );
  React.useEffect(() => {
    setIsOpen(getInitialThinkingStepOpenState(thinkingStepStateKey));
  }, [thinkingStepStateKey]);
  const setOpenState = React.useCallback(
    (nextOpen: boolean) => {
      setIsOpen(nextOpen);
      if (thinkingStepStateKey) {
        thinkingStepOpenStateByKey.set(thinkingStepStateKey, nextOpen);
      }
    },
    [thinkingStepStateKey],
  );

  const stablePrefix =
    typeof (message as any).streamStablePrefix === "string"
      ? (message as any).streamStablePrefix
      : "";
  const unstableSuffix =
    typeof (message as any).streamUnstableSuffix === "string"
      ? (message as any).streamUnstableSuffix
      : "";
  const rawContent =
    typeof message.content === "string" ? message.content.trim() : "";
  const thinkingText =
    typeof (message as any).thinking === "string"
      ? (message as any).thinking.trim()
      : "";
  const displayText =
    stablePrefix || unstableSuffix
      ? `${stablePrefix}${unstableSuffix}`.trim()
      : rawContent || thinkingText;
  const toolCalls = message.tool_calls || [];
  const toolCount = toolCalls.length;
  const messageReference = React.useMemo(
    () =>
      buildMessageTextReference(
        message,
        {
          messageId: normalizeMessageId(message.id),
          messageSeq: normalizeMessageSeq(message.seq),
          key: messageIdentityKey,
        },
      ),
    [message, messageIdentityKey],
  );
  const isMessageReferenced = messageReference
    ? pinnedMessageReferenceIdSet?.has(messageReference.id) || false
    : false;
  const isStreaming = message._uiStreaming === true;

  // Detect running tool
  const isActive = React.useMemo(() => {
    return deriveThinkingStepIsActive({
      isStreaming,
      isLatestAssistant: !!isLastMessage,
      isLatestVisibleStep,
      toolCalls,
      messageIdentityKey,
      sqlToolResultsByScopedKey,
      toolResultsByCallId,
    });
  }, [
    isStreaming,
    isLastMessage,
    isLatestVisibleStep,
    toolCalls,
    messageIdentityKey,
    sqlToolResultsByScopedKey,
    toolResultsByCallId,
  ]);

  const isAsyncQuery = React.useMemo(() => {
    if (toolCount === 0) return false;
    return toolCalls.some((tc) => isQueryRunToolName(tc.name));
  }, [toolCount, toolCalls]);

  // Step title
  const stepTitle = React.useMemo(() => {
    if (isStreaming && toolCount === 0) return null;
    const fromText = extractStepTitle(displayText);
    if (fromText) return fromText;
    if (toolCount > 0) return deriveFromTools(toolCalls);
    return null;
  }, [isStreaming, toolCount, displayText, toolCalls]);

  return (
    <Collapsible open={isOpen} onOpenChange={setOpenState}>
      {/* Collapsed header row */}
      <div
        className={`w-full flex items-center gap-2 px-3 py-2 text-xs transition-colors hover:bg-muted/20 ${
          isActive ? "thinking-shimmer" : ""
        } ${
          isAsyncQuery ? "bg-purple-500/5 border-l-2 border-purple-500/30" : ""
        } ${isMessageReferenced ? "border-l-2 border-l-sky-400/60 bg-sky-500/5" : ""}`}
      >
        <button
          type="button"
          onClick={() => setOpenState(!isOpen)}
          className="min-w-0 flex flex-1 items-center gap-2 text-left"
        >
          {isAsyncQuery ? (
            <Clock size={12} className={`text-purple-400/60 shrink-0`} />
          ) : (
            <ChevronRight
              size={12}
              className={`text-muted-foreground/40 transition-transform duration-200 shrink-0 ${isOpen ? "rotate-90" : ""}`}
            />
          )}
          <span className="text-muted-foreground/60 font-medium truncate shrink-0 max-w-[200px]">
            {stepTitle || (isStreaming ? "Thinking…" : "Thought")}
          </span>
          <div className="flex-1" />
          {/* Status labels sit in a fixed right-aligned slot (min-w +
              justify-end): the Working/Running → Done swap may only move the
              label's own left edge, never the row layout. Only the spinner /
              pulse dot animates — the text itself is never inside an
              animated transform. */}
          {isActive ? (
            isAsyncQuery ? (
              <span className="flex min-w-[60px] shrink-0 items-center justify-end gap-1.5 text-[10px] text-blue-400">
                <span className="inline-block w-1.5 h-1.5 rounded-full bg-blue-400 motion-safe:animate-pulse" />
                Running
              </span>
            ) : (
              <span className="flex min-w-[60px] shrink-0 items-center justify-end gap-1.5 text-[10px] text-amber-400/70">
                <Loader2 size={10} className="motion-safe:animate-spin" />
                Working
              </span>
            )
          ) : toolCount > 0 ? (
            <span className="flex min-w-[60px] shrink-0 items-center justify-end gap-1 text-[10px] text-green-500/60">
              <span className="inline-block w-1.5 h-1.5 rounded-full bg-green-500/60" />
              Done
            </span>
          ) : null}
        </button>
        {messageReference && onToggleMessageReference && (
          <MessageReferenceButton
            isReferenced={isMessageReferenced}
            onToggle={() => onToggleMessageReference(messageReference)}
          />
        )}
      </div>

      {/* Expanded detail */}
      <CollapsibleContent>
        <div className="px-3 pb-2 pt-0.5 border-t border-border/15">
          <ReferencedMessageChips
            references={referencedMessages}
            pinnedMessageReferenceIdSet={pinnedMessageReferenceIdSet}
            onToggleMessageReference={onToggleMessageReference}
            className={referencedMessages.length > 0 ? "pt-2 pb-1" : undefined}
          />
          <ReferencedThreadChips
            references={referencedThreads}
            pinnedThreadReferenceIdSet={pinnedThreadReferenceIdSet}
            onToggleThreadReference={onToggleThreadReference}
            className={referencedThreads.length > 0 ? "pt-2 pb-1" : undefined}
          />
          {displayText ? (
            <div className="min-w-0 max-w-full text-sm mb-1 [&_.chat-markdown_p]:text-white [&_.chat-markdown_li]:text-white [&_.chat-markdown_h1]:text-white [&_.chat-markdown_h2]:text-white [&_.chat-markdown_h3]:text-white [&_.chat-markdown_h4]:text-white [&_.chat-markdown_h5]:text-white [&_.chat-markdown_h6]:text-white">
              <MarkdownRenderer
                content={displayText}
                stablePrefix={stablePrefix || undefined}
                unstableSuffix={unstableSuffix || undefined}
                jiraBaseUrl={jiraBaseUrl}
              />
            </div>
          ) : isStreaming ? (
            <p className="text-xs text-white animate-pulse mb-1">
              &hellip;
            </p>
          ) : null}

          {toolCount > 0 && (
            <CollapsedToolsSummary
              toolCalls={toolCalls}
              isLastMessage={isLastMessage}
              isThreadRunActive={isThreadRunActive}
              compact
              messageIdentityKey={messageIdentityKey}
              toolProgressByKey={toolProgressByKey}
              sqlToolResultsByScopedKey={sqlToolResultsByScopedKey}
              artifactsById={artifactsById}
              ensureArtifactLoaded={ensureArtifactLoaded}
              onOpenArtifact={onOpenArtifact}
              onPinArtifact={onPinArtifact}
              pinnedArtifactIds={pinnedArtifactIds}
              parentMessageId={message.id}
              parentMessageSeq={message.seq}
              onToggleMessageReference={onToggleMessageReference}
              pinnedMessageReferenceIds={
                pinnedMessageReferenceIdSet
                  ? Array.from(pinnedMessageReferenceIdSet)
                  : undefined
              }
              toolResultsByCallId={toolResultsByCallId}
            />
          )}
        </div>
      </CollapsibleContent>
    </Collapsible>
  );
};

// -----------------------------------------------------------------------
// Main component — grouped thinking stack
// -----------------------------------------------------------------------

// -----------------------------------------------------------------------
// Chart artifact helpers (mirrors CollapsedToolsSummary logic)
// -----------------------------------------------------------------------

function hasPlotSpec(tc: ToolCall): boolean {
  const args: any = tc.args || {};
  return !!(
    (args.echarts_spec &&
      typeof args.echarts_spec === "object" &&
      !Array.isArray(args.echarts_spec)) ||
    (args.chart_spec &&
      typeof args.chart_spec === "object" &&
      !Array.isArray(args.chart_spec))
  );
}

function buildDisplayTableArtifact(
  tc: ToolCall,
  resultInfo?: {
    envelope?: ToolResultEnvelope;
    artifact?: Artifact;
    rawContent?: string;
  },
): Artifact | null {
  const args: any = tc.args || {};
  const dataJson = args.data_json;
  const echartsSpec =
    args.echarts_spec &&
    typeof args.echarts_spec === "object" &&
    !Array.isArray(args.echarts_spec)
      ? args.echarts_spec
      : null;
  const highchartsSpec =
    args.chart_spec &&
    typeof args.chart_spec === "object" &&
    !Array.isArray(args.chart_spec)
      ? args.chart_spec
      : null;
  const plotSpec = echartsSpec || highchartsSpec;
  const chartEngine = echartsSpec
    ? "echarts"
    : highchartsSpec
      ? "highcharts"
      : null;
  const chartSpecVersion =
    typeof (args.echarts_spec_version || args.chart_spec_version) === "string"
      ? args.echarts_spec_version || args.chart_spec_version
      : null;
  const chartBudget =
    args.chart_budget &&
    typeof args.chart_budget === "object" &&
    !Array.isArray(args.chart_budget)
      ? args.chart_budget
      : null;

  if (!dataJson) {
    const resultArtifact = resultInfo?.artifact;
    if (!resultArtifact) return null;
    return {
      ...resultArtifact,
      title: args.title || resultArtifact.title || "Data Visualization",
      metadata: {
        ...(resultArtifact.metadata || {}),
        tool_name: tc.name,
        tool_call_id: tc.id,
        ...(plotSpec ? { plot_spec: plotSpec } : {}),
        ...(chartEngine ? { chart_engine: chartEngine } : {}),
        ...(chartSpecVersion ? { chart_spec_version: chartSpecVersion } : {}),
        ...(chartBudget ? { chart_budget: chartBudget } : {}),
      },
    };
  }

  return {
    id: tc.id,
    type: "data",
    title: args.title || "Data Visualization",
    content: dataJson,
    metadata: {
      tool_name: tc.name,
      tool_call_id: tc.id,
      ...(plotSpec ? { plot_spec: plotSpec } : {}),
      ...(chartEngine ? { chart_engine: chartEngine } : {}),
      ...(chartSpecVersion ? { chart_spec_version: chartSpecVersion } : {}),
      ...(chartBudget ? { chart_budget: chartBudget } : {}),
    },
  };
}

function getChartTypeFromSpec(spec: any): string | null {
  if (!spec || typeof spec !== "object") return null;
  const options = spec.options || spec;
  const series = options?.series;
  if (!Array.isArray(series) || series.length === 0) return null;
  return typeof series[0]?.type === "string" ? series[0].type : null;
}

function buildChartDetails(chartEngine: string | null, plotSpec: any): string {
  const chartType = getChartTypeFromSpec(plotSpec);
  const engineLabel =
    chartEngine === "echarts"
      ? "ECharts"
      : chartEngine === "highcharts"
        ? "Highcharts"
        : "Chart";
  return [engineLabel, chartType || "chart"].filter(Boolean).join(" · ");
}

interface ChartArtifactItem {
  key: string;
  artifact: Artifact;
  plotSpec: any;
  chartEngine: string | null;
  title: string;
}

interface HtmlArtifactItem {
  key: string;
  artifact: Artifact;
  title: string;
}

interface GeneratedFileArtifactItem {
  key: string;
  artifact: Artifact;
}

function artifactNeedsHydration(artifact: Artifact): boolean {
  return (
    artifact.metadata?.placeholder === true ||
    artifact.metadata?.partial === true ||
    artifact.metadata?.preview_only === true ||
    String(artifact.content || "").length === 0
  );
}

function isPlainRecord(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function addAttachmentArtifactIdValue(
  out: string[],
  seen: Set<string>,
  value: unknown,
) {
  if (typeof value === "string") {
    const trimmed = value.trim();
    if (trimmed && !seen.has(trimmed)) {
      seen.add(trimmed);
      out.push(trimmed);
    }
    return;
  }
  if (Array.isArray(value)) {
    for (const item of value) addAttachmentArtifactIdValue(out, seen, item);
  }
}

function addAttachmentArtifactIdsFromRecord(
  out: string[],
  seen: Set<string>,
  record: Record<string, any>,
) {
  addAttachmentArtifactIdValue(out, seen, record.attachment_artifact_id);
  addAttachmentArtifactIdValue(out, seen, record.attachmentArtifactId);
  addAttachmentArtifactIdValue(out, seen, record.attachment_artifact_ids);
  addAttachmentArtifactIdValue(out, seen, record.attachmentArtifactIds);

  for (const listKey of ["attachments", "files", "downloads"]) {
    const list = record[listKey];
    if (!Array.isArray(list)) continue;
    for (const item of list) {
      if (!isPlainRecord(item)) continue;
      addAttachmentArtifactIdValue(out, seen, item.artifact_id);
      addAttachmentArtifactIdValue(out, seen, item.artifactId);
      addAttachmentArtifactIdValue(out, seen, item.attachment_artifact_id);
      addAttachmentArtifactIdValue(out, seen, item.attachmentArtifactId);
    }
  }
}

function toolResultAttachmentArtifactIds(info?: {
  envelope?: ToolResultEnvelope;
  artifact?: Artifact;
  rawContent?: string;
}): string[] {
  if (!info?.envelope) return [];
  const out: string[] = [];
  const seen = new Set<string>();
  if (isPlainRecord(info.envelope.result)) {
    addAttachmentArtifactIdsFromRecord(out, seen, info.envelope.result);
  }
  if (isPlainRecord(info.envelope.result_metadata)) {
    addAttachmentArtifactIdsFromRecord(out, seen, info.envelope.result_metadata);
  }
  const mainArtifactId = String(info.envelope.artifact_id || "").trim();
  return mainArtifactId ? out.filter((id) => id !== mainArtifactId) : out;
}

// -----------------------------------------------------------------------
// Main component — grouped thinking stack
// -----------------------------------------------------------------------

const IntermediateThinkingBlock: React.FC<IntermediateThinkingBlockProps> = ({
  steps,
  isThreadRunActive,
  jiraBaseUrl,
  onOpenArtifact,
  onPinArtifact,
  pinnedArtifactIds,
  onToggleMessageReference,
  pinnedMessageReferenceIds,
  onToggleThreadReference,
  pinnedThreadReferenceIds,
  toolProgressByKey,
  sqlToolResultsByScopedKey,
  artifactsById,
  ensureArtifactLoaded,
  toolResultsByCallId,
}) => {
  // Filter out steps whose only tool calls are get_query_results (polling).
  // These are internal status checks, not meaningful thinking steps.
  // Also deduplicate text-only steps that repeat the previous step's thinking
  // text — these arise when the finalization split creates a standalone
  // narrative message whose content duplicates the preceding thinking trace.
  const visibleSteps = React.useMemo(() => {
    const filtered = steps.filter((step) =>
      isVisibleThinkingStepMessage(step.message),
    );
    return filtered.filter((step, i) => {
      if (i === 0) return true;
      const prev = filtered[i - 1];
      const prevThinking =
        typeof (prev.message as any).thinking === "string"
          ? (prev.message as any).thinking.trim()
          : "";
      if (!prevThinking) return true;
      // Keep steps that have tool calls — they represent real work.
      const tc = step.message.tool_calls;
      if (Array.isArray(tc) && tc.length > 0) return true;
      // Keep steps that have their own thinking — they are distinct turns.
      const ownThinking =
        typeof (step.message as any).thinking === "string"
          ? (step.message as any).thinking.trim()
          : "";
      if (ownThinking) return true;
      // Remove content-only steps whose text matches the previous thinking.
      const thisContent =
        typeof step.message.content === "string"
          ? step.message.content.trim()
          : "";
      if (!thisContent) return true;
      return thisContent !== prevThinking;
    });
  }, [steps]);
  const hasActiveStep = React.useMemo(() => {
    if (visibleSteps.length === 0) return false;
    const latestVisible = visibleSteps[visibleSteps.length - 1];
    return deriveThinkingStepIsActive({
      isStreaming: latestVisible.message._uiStreaming === true,
      isLatestAssistant: !!latestVisible.isLastMessage,
      isLatestVisibleStep: true,
      toolCalls: latestVisible.message.tool_calls || [],
      messageIdentityKey: latestVisible.messageIdentityKey,
      sqlToolResultsByScopedKey,
      toolResultsByCallId,
    });
  }, [visibleSteps, sqlToolResultsByScopedKey, toolResultsByCallId]);
  const pinnedMessageReferenceIdSet = React.useMemo(
    () =>
      new Set(
        (pinnedMessageReferenceIds || [])
          .map((id) => String(id || "").trim())
          .filter(Boolean),
      ),
    [pinnedMessageReferenceIds],
  );
  const pinnedThreadReferenceIdSet = React.useMemo(
    () =>
      new Set(
        (pinnedThreadReferenceIds || [])
          .map((id) => String(id || "").trim())
          .filter(Boolean),
      ),
    [pinnedThreadReferenceIds],
  );

  // Tool calls always escape the StepRow wrapper and render the full
  // tool-activity disclosure (pill / expanded cards / live pinned) directly
  // in the flow. Tool-only steps become bare card groups; mixed text+tool
  // steps split into a text-only StepRow followed by the bare card group.
  // CONTIGUOUS tool segments merge into ONE group ("Ran 1 tool" grows to
  // "Ran 2 tools" as the next call lands) — each merged call is stamped with
  // its source message identity so SQL result lookups and message references
  // keep resolving against the message that actually issued the call.
  type RenderSegment =
    | { kind: "stack"; steps: Array<{ step: ThinkingStep; visibleIndex: number }> }
    | {
        kind: "tools";
        steps: Array<{ step: ThinkingStep; visibleIndex: number }>;
        toolCalls: ToolCall[];
      };
  const renderSegments = React.useMemo(() => {
    const segments: RenderSegment[] = [];
    const pushStackSegment = (step: ThinkingStep, visibleIndex: number) => {
      const last = segments[segments.length - 1];
      if (last && last.kind === "stack") {
        last.steps.push({ step, visibleIndex });
      } else {
        segments.push({ kind: "stack", steps: [{ step, visibleIndex }] });
      }
    };
    const pushToolSegment = (
      step: ThinkingStep,
      visibleIndex: number,
      stamped: ToolCall[],
    ) => {
      const last = segments[segments.length - 1];
      if (last && last.kind === "tools") {
        last.steps.push({ step, visibleIndex });
        last.toolCalls.push(...stamped);
      } else {
        segments.push({
          kind: "tools",
          steps: [{ step, visibleIndex }],
          toolCalls: stamped,
        });
      }
    };

    visibleSteps.forEach((step, visibleIndex) => {
      const sourceToolCalls = step.message.tool_calls || [];
      if (sourceToolCalls.length > 0) {
        const toolOnly = isToolOnlyStep(step.message);
        if (!toolOnly) {
          pushStackSegment(stripToolCallsFromStep(step), visibleIndex);
        }
        const stamped = sourceToolCalls.map(
          (tc) =>
            ({
              ...tc,
              _uiSourceMessageIdentityKey: step.messageIdentityKey,
              _uiSourceMessageId: step.message.id,
              _uiSourceMessageSeq: step.message.seq,
            }) as ToolCall,
        );
        pushToolSegment(step, visibleIndex, stamped);
        return;
      }
      pushStackSegment(step, visibleIndex);
    });
    return segments;
  }, [visibleSteps]);

  // Steps rendered as direct tool segments draw their own chart/HTML cards
  // (full CollapsedToolsSummary), so the block-level collectors below must
  // skip them or every chart would render twice.
  const toolSegmentIdentityKeys = React.useMemo(() => {
    const keys = new Set<string>();
    for (const segment of renderSegments) {
      if (segment.kind !== "tools") continue;
      for (const { step } of segment.steps) keys.add(step.messageIdentityKey);
    }
    return keys;
  }, [renderSegments]);

  // Collect chart artifacts across ALL steps — these render below the stack
  const chartArtifacts = React.useMemo(() => {
    const items: ChartArtifactItem[] = [];
    const seen = new Set<string>();

    for (const step of steps) {
      if (toolSegmentIdentityKeys.has(step.messageIdentityKey)) continue;
      const toolCalls = step.message.tool_calls || [];
      for (const tc of toolCalls) {
        // display_table with chart spec in args
        if (tc.name === "display_table" && hasPlotSpec(tc)) {
          const resultInfo = tc.id ? toolResultsByCallId?.get(tc.id) : undefined;
          const artifact = buildDisplayTableArtifact(tc, resultInfo);
          if (!artifact || seen.has(tc.id)) continue;
          seen.add(tc.id);
          const args: any = tc.args || {};
          const plotSpec =
            args.echarts_spec ||
            args.chart_spec ||
            (artifact.metadata as any)?.plot_spec ||
            (resultInfo?.envelope as any)?.plot_spec ||
            null;
          if (!plotSpec) continue;
          const chartEngine = args.echarts_spec
            ? "echarts"
            : args.chart_spec
              ? "highcharts"
              : ((artifact.metadata as any)?.chart_engine as string | null) ||
                ((resultInfo?.envelope as any)?.chart_engine as string | null) ||
                null;
          items.push({
            key: `dt-${tc.id}`,
            artifact,
            plotSpec,
            chartEngine,
            title: args.title || artifact.title || "Chart",
          });
          continue;
        }

        // SQL query results with plot_spec
        if (isQueryRunToolName(tc.name)) {
          const scopedKey = getQueryCardStateKey({
            runId: (tc as any)._uiRunId,
            messageIdentityKey: step.messageIdentityKey,
            toolCallId: tc.id,
          });
          const resultInfo = scopedKey
            ? sqlToolResultsByScopedKey?.get(scopedKey)
            : undefined;
          if (!resultInfo) continue;
          const artifact =
            resultInfo.artifact ||
            (resultInfo.envelope?.artifact_id
              ? artifactsById?.[resultInfo.envelope.artifact_id]
              : undefined);
          if (!artifact || seen.has(artifact.id)) continue;
          const plotSpec =
            artifact.metadata?.plot_spec || resultInfo.envelope?.plot_spec;
          if (!plotSpec) continue;
          seen.add(artifact.id);
          const chartEngine =
            artifact.metadata?.chart_engine ||
            resultInfo.envelope?.chart_engine ||
            null;
          items.push({
            key: `sql-${tc.id}`,
            artifact,
            plotSpec,
            chartEngine,
            title: artifact.title || "Query Chart",
          });
          continue;
        }

        // General tool results (e.g. build_chart_artifact) with plot_spec
        if (tc.name !== "display_table") {
          const resultInfo = tc.id
            ? toolResultsByCallId?.get(tc.id)
            : undefined;
          if (!resultInfo) continue;
          const artifact =
            resultInfo.artifact ||
            (resultInfo.envelope?.artifact_id
              ? artifactsById?.[resultInfo.envelope.artifact_id]
              : undefined);
          if (!artifact || seen.has(artifact.id)) continue;
          const plotSpec =
            (artifact.metadata as any)?.plot_spec ||
            (resultInfo.envelope as any)?.plot_spec;
          if (!plotSpec) continue;
          seen.add(artifact.id);
          const chartEngine =
            (artifact.metadata as any)?.chart_engine ||
            (resultInfo.envelope as any)?.chart_engine ||
            null;
          items.push({
            key: `tool-${tc.id}`,
            artifact,
            plotSpec,
            chartEngine,
            title: artifact.title || tc.name,
          });
          continue;
        }
      }
    }
    return items;
  }, [steps, toolSegmentIdentityKeys, sqlToolResultsByScopedKey, artifactsById, toolResultsByCallId]);

  const htmlArtifacts = React.useMemo(() => {
    const items: HtmlArtifactItem[] = [];
    const seen = new Set<string>();

    for (const step of steps) {
      if (toolSegmentIdentityKeys.has(step.messageIdentityKey)) continue;
      const toolCalls = step.message.tool_calls || [];
      for (const tc of toolCalls) {
        if (isQueryRunToolName(tc.name) || tc.name === "display_table") {
          continue;
        }
        const resultInfo = tc.id ? toolResultsByCallId?.get(tc.id) : undefined;
        const artifact =
          resultInfo?.artifact ||
          (resultInfo?.envelope?.artifact_id
            ? artifactsById?.[resultInfo.envelope.artifact_id]
            : undefined);
        if (!artifact || !isSandboxedHtmlArtifact(artifact, tc.name)) continue;
        const key = artifact.id || tc.id;
        if (!key || seen.has(key)) continue;
        seen.add(key);
        items.push({
          key: `html-${key}`,
          artifact,
          title: artifact.title || "HTML Artifact",
        });
      }
    }

    return items;
  }, [steps, toolSegmentIdentityKeys, artifactsById, toolResultsByCallId]);

  const generatedFileArtifacts = React.useMemo(() => {
    const items: GeneratedFileArtifactItem[] = [];
    const seen = new Set<string>();
    const addArtifact = (artifact: Artifact | undefined, fallbackId?: string) => {
      if (!artifact || artifact.type !== "file") return;
      if (!isRawDownloadArtifact(artifact) && !canDownloadArtifact(artifact)) return;
      const key = artifact.id || fallbackId || "";
      if (!key || seen.has(key)) return;
      seen.add(key);
      items.push({ key: `file-${key}`, artifact });
    };
    const snapshotByArtifactId = (resultInfo?: {
      fileAttachments?: ChatFileAttachment[];
    }) => {
      const out = new Map<string, ChatFileAttachment>();
      for (const attachment of resultInfo?.fileAttachments || []) {
        const id = String(attachment.artifactId || "").trim();
        if (id) out.set(id, attachment);
      }
      return out;
    };
    const addSnapshot = (attachment: ChatFileAttachment | undefined) => {
      const artifact = attachment ? fileAttachmentSnapshotArtifact(attachment) : null;
      if (artifact) addArtifact(artifact, attachment?.artifactId);
    };

    for (const step of steps) {
      if (toolSegmentIdentityKeys.has(step.messageIdentityKey)) continue;
      const toolCalls = step.message.tool_calls || [];
      for (const tc of toolCalls) {
        const resultInfo = tc.id ? toolResultsByCallId?.get(tc.id) : undefined;
        const directArtifact =
          resultInfo?.artifact ||
          (resultInfo?.envelope?.artifact_id
            ? artifactsById?.[resultInfo.envelope.artifact_id]
            : undefined);
        addArtifact(directArtifact, resultInfo?.envelope?.artifact_id);

        const snapshots = snapshotByArtifactId(resultInfo);
        for (const artifactId of toolResultAttachmentArtifactIds(resultInfo)) {
          addArtifact(artifactsById?.[artifactId], artifactId);
          if (!artifactsById?.[artifactId]) addSnapshot(snapshots.get(artifactId));
        }
        for (const attachment of resultInfo?.fileAttachments || []) {
          if (!artifactsById?.[attachment.artifactId]) addSnapshot(attachment);
        }
      }
    }

    return items;
  }, [steps, toolSegmentIdentityKeys, artifactsById, toolResultsByCallId]);

  const previewHtmlArtifacts = React.useMemo(
    () => htmlArtifacts.slice(0, INLINE_HTML_PREVIEW_LIMIT),
    [htmlArtifacts],
  );

  const [hydratedHtmlArtifactsById, setHydratedHtmlArtifactsById] =
    React.useState<Record<string, Artifact>>({});

  React.useEffect(() => {
    if (!ensureArtifactLoaded) return;
    const targets = previewHtmlArtifacts
      .map((item) => item.artifact)
      .filter((artifact) => artifactNeedsHydration(artifact));
    if (targets.length === 0) return;

    let cancelled = false;
    for (const artifact of targets) {
      void ensureArtifactLoaded(artifact.id)
        .then((loaded) => {
          if (!loaded || cancelled) return;
          if (
            !isSandboxedHtmlArtifact(
              loaded,
              String(artifact.metadata?.tool_name || "").trim(),
            )
          ) {
            return;
          }
          setHydratedHtmlArtifactsById((prev) => ({
            ...prev,
            [artifact.id]: loaded,
          }));
        })
        .catch(() => {});
    }

    return () => {
      cancelled = true;
    };
  }, [ensureArtifactLoaded, previewHtmlArtifacts]);

  // Suppress empty thinking shells created by internal poll-only turns.
  if (
    visibleSteps.length === 0 &&
    chartArtifacts.length === 0 &&
    htmlArtifacts.length === 0 &&
    generatedFileArtifacts.length === 0
  ) {
    return null;
  }

  return (
    <div className="flex w-full justify-start min-w-0">
      <div className="flex gap-3 w-full max-w-[90%] md:max-w-[85%] flex-row min-w-0">
        <div className="h-8 w-8 mt-1 flex-shrink-0 flex items-center justify-center">
          <Brain
            size={16}
            className={`text-muted-foreground/30 ${hasActiveStep ? "animate-pulse" : ""}`}
          />
        </div>
        <div className="min-w-0 w-full max-w-full space-y-2">
          {/* Thinking stacks interleaved with bare tool-activity segments.
              Tool calls render the new disclosure cards directly — never
              inside the StepRow "Working/Done" wrapper, even when the same
              stream step also carries explanatory text. */}
          {renderSegments.map((segment, segIndex) =>
            segment.kind === "tools" ? (
              <CollapsedToolsSummary
                key={`tools-${segment.steps[0]?.step.message.id || segment.steps[0]?.visibleIndex}`}
                toolCalls={segment.toolCalls}
                isLastMessage={segment.steps.some(({ step }) => step.isLastMessage)}
                isThreadRunActive={isThreadRunActive}
                messageIdentityKey={segment.steps[0]?.step.messageIdentityKey}
                toolProgressByKey={toolProgressByKey}
                sqlToolResultsByScopedKey={sqlToolResultsByScopedKey}
                artifactsById={artifactsById}
                ensureArtifactLoaded={ensureArtifactLoaded}
                onOpenArtifact={onOpenArtifact}
                onPinArtifact={onPinArtifact}
                pinnedArtifactIds={pinnedArtifactIds}
                parentMessageId={segment.steps[0]?.step.message.id}
                parentMessageSeq={segment.steps[0]?.step.message.seq}
                onToggleMessageReference={onToggleMessageReference}
                pinnedMessageReferenceIds={pinnedMessageReferenceIds}
                toolResultsByCallId={toolResultsByCallId}
              />
            ) : (
              <div
                key={`stack-${segIndex}-${segment.steps[0]?.step.message.id || segment.steps[0]?.visibleIndex}`}
                className="min-w-0 max-w-full rounded-lg border border-border/30 divide-y divide-border/15 overflow-hidden transition-all duration-300 hover:border-border/50 hover:shadow-[0_0_15px_-5px_hsl(215_70%_82%/0.1)]"
              >
                {segment.steps.map(({ step, visibleIndex }) => (
                  <StepRow
                    key={step.message.id || visibleIndex}
                    step={step}
                    isLatestVisibleStep={visibleIndex === visibleSteps.length - 1}
                    isThreadRunActive={isThreadRunActive}
                    jiraBaseUrl={jiraBaseUrl}
                    sqlToolResultsByScopedKey={sqlToolResultsByScopedKey}
                    toolResultsByCallId={toolResultsByCallId}
                    toolProgressByKey={toolProgressByKey}
                    artifactsById={artifactsById}
                    ensureArtifactLoaded={ensureArtifactLoaded}
                    onOpenArtifact={onOpenArtifact}
                    onPinArtifact={onPinArtifact}
                    pinnedArtifactIds={pinnedArtifactIds}
                    onToggleMessageReference={onToggleMessageReference}
                    pinnedMessageReferenceIdSet={pinnedMessageReferenceIdSet}
                    onToggleThreadReference={onToggleThreadReference}
                    pinnedThreadReferenceIdSet={pinnedThreadReferenceIdSet}
                  />
                ))}
              </div>
            ),
          )}

          {/* Chart artifacts — promoted outside the stack, always visible */}
          {chartArtifacts.map(
            ({ key, artifact, plotSpec, chartEngine, title }) => (
              <ArtifactCard
                key={key}
                title={title}
                type="chart"
                details={buildChartDetails(chartEngine, plotSpec)}
                plotSpec={plotSpec}
                onClick={() => onOpenArtifact(artifact)}
                onPin={
                  onPinArtifact ? () => onPinArtifact(artifact) : undefined
                }
                isPinned={pinnedArtifactIds?.includes(artifact.id)}
              />
            ),
          )}

          {htmlArtifacts.map(({ key, artifact: sourceArtifact, title }, index) => {
            const artifact =
              hydratedHtmlArtifactsById[sourceArtifact.id] || sourceArtifact;
            return (
              <HtmlArtifactCard
                key={key}
                testId="sandboxed-html-artifact-card"
                artifact={artifact}
                title={title}
                showThumbnail={index < INLINE_HTML_PREVIEW_LIMIT}
                onOpenArtifact={onOpenArtifact}
                onPin={onPinArtifact}
                isPinned={pinnedArtifactIds?.includes(artifact.id)}
              />
            );
          })}

          {generatedFileArtifacts.map(({ key, artifact }) => {
            const presentation = fileArtifactCardPresentation(artifact);
            const rawDownload = isRawDownloadArtifact(artifact);
            const contentDownload = !rawDownload && canDownloadArtifact(artifact);
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
                downloadArtifactId={rawDownload ? artifact.id : undefined}
                downloadFileName={rawDownload ? artifact.title || artifact.id : undefined}
                onDownload={contentDownload ? () => downloadArtifact(artifact) : undefined}
                onClick={() => onOpenArtifact(artifact)}
                className="max-w-full sm:max-w-[28rem]"
              />
            );
          })}
        </div>
      </div>
    </div>
  );
};

export default IntermediateThinkingBlock;
