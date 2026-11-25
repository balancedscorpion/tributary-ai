import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import type {
  AppSettings,
  Artifact,
  ChartBudgetReport,
  ChatFileAttachment,
  ChatImageAttachment,
  CompactionStats,
  ThreadReference,
  ToolProgressEvent,
  ToolResultEnvelope,
  UIMessage,
} from "@/types";
import {
  Role,
  SANDBOXED_HTML_NETWORK_POLICY,
  SANDBOXED_HTML_RENDER_MODE,
  SANDBOXED_HTML_SANDBOX_POLICY,
} from "@/types";
import { buildArtifactFromEnvelope } from "@/lib/toolResultArtifact";
import { getSecretsStatus } from "@/services/secretsApi";
import { publishApprovalEvent } from "@/hooks/useApprovals";
import { parseSSE } from "@/providers/stream/sseParser";
import { parseRunSseEvent } from "@/providers/stream/runEventParser";
import type { ToolCall } from "@/providers/core/types";
import { getActiveProviderConfig } from "@/lib/providerConfigRuntime";
import {
  getQueryProviderForToolName,
  isQueryRunToolName,
  isQueryToolName,
  normalizeQueryProvider,
} from "@/lib/queryProviderRegistry";
import {
  isHiddenQueryToolCallName,
  shouldSuppressQueryToolResult,
} from "@/lib/queryToolVisibility";
import { isActiveQueryStatus } from "@/lib/queryProgress";
import { normalizeManagedQueryJobId } from "@/lib/managedQueryJobId";
import {
  createStreamingMarkdownSegmenter,
  type StreamingMarkdownSegmenter,
} from "@/lib/streamingMarkdownSegmenter";
import {
  CapabilityApiError,
  getRunCapabilityPlan,
  preflightThreadRun,
  updateThreadTarget,
} from "@/services/capabilityApi";
import type {
  PlanChoiceSelection,
  PreflightAttachmentRequirement,
  PreflightRequirements,
  PreflightResult,
  ThreadTarget,
} from "@/lib/capability/types";
import type { UICapabilityPlanItem, UICapabilityPlanSummary } from "@/types";
import { getCapabilityUxFlags } from "@/components/settings/providers/capabilityUxFlags";
import {
  resolveAuthTargetConfig,
  resolveCompatibleAuthProfile,
} from "@/lib/capability/providerConfig";
import { targetIdentityLabel } from "@/providers/catalog/targetIdentity";
import { toast } from "sonner";

export { buildArtifactFromEnvelope };

interface UseChatOptions {
  settings: AppSettings;
  projectId?: string | null;
  /**
   * A preflight fix removed an attachment from the pending submission; the
   * composer owner must drop it from its pending state too.
   */
  onPreflightAttachmentRemoved?: (
    threadId: string,
    attachmentId: string,
  ) => void;
}

type ThreadSummary = {
  id: string;
  title: string;
  summary?: string;
  status?: string;
  artifactCount?: number;
  createdAt?: number;
  updatedAt?: number;
  /** Server-persisted per-thread capability target (plan §5.6). */
  lastTarget?: ThreadTarget;
};

/** State handed to the consolidated preflight sheet (plan §4.4). */
export type PreflightGateState = {
  threadId: string;
  result: PreflightResult;
};

export type PreflightGateDecision =
  | { action: "cancel" }
  | { action: "reflight" }
  | { action: "remove_attachment"; attachmentId: string }
  | { action: "send"; selections: Record<string, PlanChoiceSelection> };

type PlanSendPayload = {
  planId: string;
  target: ThreadTarget;
  requirements: PreflightRequirements;
  choiceSelections: Record<string, PlanChoiceSelection>;
};

type RunInfo = {
  id: string;
  threadId: string;
  status: string;
  recoveredFromConflict?: boolean;
};

export type RunStreamDiagnosticKind =
  "heartbeat_without_progress" | "preview_only_artifact";

export type RunStreamDiagnostic = {
  kind: RunStreamDiagnosticKind;
  message: string;
  since?: number;
  durationMs?: number;
  artifactId?: string;
  fullArtifactId?: string;
};

export type RunStreamStatus = {
  runId: string;
  threadId: string;
  startedAt: number;
  connectedAt?: number;
  lastEventAt?: number;
  lastEventName?: string;
  lastHeartbeatAt?: number;
  lastMeaningfulEventAt?: number;
  lastMeaningfulEventName?: string;
  heartbeatCount: number;
  meaningfulEventCount: number;
  phase?: string;
  label?: string;
  message?: string;
  progressPercent?: number;
  previewOnlyArtifactId?: string;
  fullArtifactId?: string;
  diagnostic?: RunStreamDiagnostic;
};

type PreflightAuthResult = {
  ok: boolean;
  message?: string;
};

type UIImageAttachment = NonNullable<UIMessage["imageAttachments"]>[number];
type UIFileAttachment = NonNullable<UIMessage["fileAttachments"]>[number];

const ACTIVE_THREAD_STORAGE_KEY = "ai_analyst_active_thread_id_v1";
const HEARTBEAT_WITHOUT_PROGRESS_MS = 30_000;

function safeJsonStringify(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    try {
      return JSON.stringify({ error: "Failed to serialize value" }, null, 2);
    } catch {
      return '{"error":"Failed to serialize value"}';
    }
  }
}

function tryParseJson(text: string): { ok: true; value: any } | { ok: false } {
  try {
    return { ok: true, value: JSON.parse(text) };
  } catch {
    return { ok: false };
  }
}

function isPlainObject(value: any): value is Record<string, any> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function normalizeTokenCount(value: unknown): number | undefined {
  if (typeof value === "number") {
    if (!Number.isFinite(value) || value < 0) return undefined;
    return Math.round(value);
  }
  if (typeof value === "string" && value.trim()) {
    const parsed = Number(value.trim());
    if (!Number.isFinite(parsed) || parsed < 0) return undefined;
    return Math.round(parsed);
  }
  return undefined;
}

function firstTokenCount(
  source: Record<string, any> | null | undefined,
  ...keys: string[]
): number | undefined {
  if (!source) return undefined;
  for (const key of keys) {
    if (!Object.prototype.hasOwnProperty.call(source, key)) continue;
    const value = normalizeTokenCount(source[key]);
    if (value !== undefined) return value;
  }
  return undefined;
}

function normalizeBoolean(value: unknown): boolean | undefined {
  if (typeof value === "boolean") return value;
  if (typeof value === "number") {
    if (value === 1) return true;
    if (value === 0) return false;
  }
  if (typeof value === "string") {
    const text = value.trim().toLowerCase();
    if (text === "true" || text === "yes" || text === "1") return true;
    if (text === "false" || text === "no" || text === "0") return false;
  }
  return undefined;
}

function hasMeaningfulCompactionStats(
  stats: CompactionStats | null,
): stats is CompactionStats {
  return Boolean(
    stats &&
    (stats.tokensBefore !== undefined ||
      stats.tokensAfter !== undefined ||
      stats.tokenBudget !== undefined ||
      stats.compacted !== undefined ||
      Boolean(stats.phase) ||
      Boolean(stats.status)),
  );
}

function normalizeCompactionStatsPayload(payload: any): CompactionStats | null {
  const root =
    isPlainObject(payload) && isPlainObject((payload as any).run)
      ? ((payload as any).run as Record<string, any>)
      : isPlainObject(payload)
        ? (payload as Record<string, any>)
        : null;
  if (!root) return null;

  const contextPlan = isPlainObject(root.context_plan)
    ? (root.context_plan as Record<string, any>)
    : null;
  const phase = firstCompactString(root, "phase", "run_phase", "backend_phase");
  const status = firstCompactString(root, "status", "state");
  const tokensBefore =
    firstTokenCount(
      root,
      "tokens_before",
      "tokensBefore",
      "context_tokens_before",
      "contextTokensBefore",
    ) ?? firstTokenCount(contextPlan, "tokens_before", "tokensBefore");
  const tokensAfter =
    firstTokenCount(
      root,
      "tokens_after",
      "tokensAfter",
      "context_tokens_after",
      "contextTokensAfter",
    ) ?? firstTokenCount(contextPlan, "tokens_after", "tokensAfter");
  const tokenBudget =
    firstTokenCount(
      root,
      "token_budget",
      "tokenBudget",
      "hard_token_budget",
      "hardTokenBudget",
    ) ??
    firstTokenCount(
      contextPlan,
      "token_budget",
      "tokenBudget",
      "hard_token_budget",
      "hardTokenBudget",
    );
  const compacted =
    normalizeBoolean(root.compacted) ??
    normalizeBoolean(root.context_overflow_compacted) ??
    normalizeBoolean(root.contextOverflowCompacted) ??
    normalizeBoolean(contextPlan?.overflow_compacted) ??
    normalizeBoolean(contextPlan?.overflowCompacted);

  const stats: CompactionStats = {};
  if (tokensBefore !== undefined) stats.tokensBefore = tokensBefore;
  if (tokensAfter !== undefined) stats.tokensAfter = tokensAfter;
  if (tokenBudget !== undefined) stats.tokenBudget = tokenBudget;
  if (compacted !== undefined) stats.compacted = compacted;
  if (phase) stats.phase = phase;
  if (status) stats.status = status;
  return hasMeaningfulCompactionStats(stats) ? stats : null;
}

function mergeCompactionStats(
  current: CompactionStats | undefined,
  incoming: CompactionStats | null,
): CompactionStats | undefined {
  if (!incoming) return current;
  const merged: CompactionStats = { ...(current || {}) };
  if (incoming.tokensBefore !== undefined)
    merged.tokensBefore = incoming.tokensBefore;
  if (incoming.tokensAfter !== undefined)
    merged.tokensAfter = incoming.tokensAfter;
  if (incoming.tokenBudget !== undefined)
    merged.tokenBudget = incoming.tokenBudget;
  if (incoming.compacted !== undefined) merged.compacted = incoming.compacted;
  if (incoming.phase) merged.phase = incoming.phase;
  if (incoming.status) merged.status = incoming.status;
  return hasMeaningfulCompactionStats(merged) ? merged : undefined;
}

function compactRunStatusText(value: unknown, maxLength = 160): string {
  if (typeof value !== "string") return "";
  const text = value.replace(/\s+/g, " ").trim();
  if (!text) return "";
  return text.length > maxLength
    ? `${text.slice(0, Math.max(0, maxLength - 3))}...`
    : text;
}

function humanizeRunStatusIdentifier(value: string): string {
  const text = compactRunStatusText(value.replace(/[_-]+/g, " "));
  if (!text) return "";
  return text.charAt(0).toUpperCase() + text.slice(1);
}

const CONTEXT_PROGRESS_EVENT_NAMES = new Set([
  "local_progress",
  "context_progress",
]);

const TERMINAL_FAILURE_STATUSES = new Set(["failed", "error"]);

const NEUTRAL_CONTEXT_PROGRESS_LABELS: Record<
  string,
  Record<string, string>
> = {
  context_planning: {
    default: "Preparing context",
    completed: "Context ready",
    failed: "Context preparation failed",
  },
  context_compacting: {
    default: "Compacting context",
    completed: "Context compacted",
    failed: "Context compaction failed",
  },
  context_checkpoint_written: {
    default: "Context checkpoint saved",
    started: "Saving context checkpoint",
    completed: "Context checkpoint saved",
    failed: "Context checkpoint failed",
  },
  context_retrying_with_checkpoint: {
    default: "Continuing with compacted context",
    completed: "Continuing with compacted context",
  },
  tool_result_compacting: {
    default: "Compacting tool results",
    completed: "Tool results compacted",
    failed: "Tool result compaction failed",
  },
};

function neutralContextProgressLabel(phase: string, status: string): string {
  const labels = NEUTRAL_CONTEXT_PROGRESS_LABELS[phase];
  if (!labels) return "";
  return labels[status] || labels.default || "";
}

function firstCompactString(
  source: Record<string, any>,
  ...keys: string[]
): string {
  for (const key of keys) {
    const value = compactRunStatusText(source[key]);
    if (value) return value;
  }
  return "";
}

function normalizeRunProgressPercent(
  source: Record<string, any>,
): number | undefined {
  const candidates: Array<{ key: string; value: unknown }> = [
    { key: "percent", value: source.percent },
    { key: "progress_percent", value: source.progress_percent },
    { key: "progress_pct", value: source.progress_pct },
    { key: "progressPercent", value: source.progressPercent },
    { key: "progress", value: source.progress },
  ];

  if (isPlainObject(source.progress)) {
    candidates.unshift(
      { key: "progress.percent", value: (source.progress as any).percent },
      {
        key: "progress.progress_percent",
        value: (source.progress as any).progress_percent,
      },
      { key: "progress.progress", value: (source.progress as any).progress },
    );
  }

  for (const candidate of candidates) {
    const raw = Number(candidate.value);
    if (!Number.isFinite(raw)) continue;
    const ratioStyleKey =
      candidate.key.endsWith("progress") ||
      candidate.key.includes("percent") ||
      candidate.key.includes("pct");
    const normalized = ratioStyleKey && raw >= 0 && raw <= 1 ? raw * 100 : raw;
    return Math.max(0, Math.min(100, Math.round(normalized)));
  }

  return undefined;
}

function normalizeRunTerminalStatus(payload: any): string {
  if (!isPlainObject(payload)) return "";
  const raw = firstCompactString(
    payload,
    "status",
    "state",
    "terminal_status",
    "terminalStatus",
  ).toLowerCase();
  if (!raw) return "";
  if (raw === "succeeded" || raw === "success" || raw === "complete") {
    return "completed";
  }
  if (raw === "canceled") return "cancelled";
  if (raw === "errored") return "error";
  return raw;
}

function isExplicitTerminalPayload(payload: any): boolean {
  if (!isPlainObject(payload)) return false;
  if (
    payload.terminal === true ||
    payload.is_terminal === true ||
    payload.isTerminal === true
  ) {
    return true;
  }
  const status = normalizeRunTerminalStatus(payload);
  return (
    status === "completed" ||
    status === "cancelled" ||
    TERMINAL_FAILURE_STATUSES.has(status)
  );
}

function isContextLikePayload(payload: any): boolean {
  const values: string[] = [];
  if (typeof payload === "string") values.push(payload);
  if (isPlainObject(payload)) {
    for (const key of [
      "code",
      "kind",
      "phase",
      "run_phase",
      "backend_phase",
      "message",
      "error",
      "detail",
      "description",
    ]) {
      const value = compactRunStatusText(payload[key], 260);
      if (value) values.push(value);
    }
  }
  return values.some((value) => {
    const text = value.toLowerCase();
    return (
      text.includes("context") ||
      text.includes("token budget") ||
      text.includes("prompt too large") ||
      text.includes("context_overflow") ||
      text.includes("context window")
    );
  });
}

export function isUserFacingRunErrorEvent(
  eventName: string,
  payload: any,
): boolean {
  const name = String(eventName || "").trim();
  if (name === "run_terminal") {
    return TERMINAL_FAILURE_STATUSES.has(normalizeRunTerminalStatus(payload));
  }
  if (name !== "error") return false;
  if (isContextLikePayload(payload) && !isExplicitTerminalPayload(payload)) {
    return false;
  }
  return true;
}

function extractRunEventErrorMessage(
  payload: any,
  fallback = "Unknown error",
): string {
  if (isPlainObject(payload)) {
    const text = firstCompactString(
      payload,
      "message",
      "error",
      "detail",
      "description",
      "code",
    );
    if (text) return text;
  }
  if (typeof payload === "string" && payload.trim()) return payload.trim();
  return fallback;
}

export function deriveRunStatusFields(
  eventName: string,
  payload: any,
): Pick<RunStreamStatus, "phase" | "label" | "message" | "progressPercent"> {
  const name = String(eventName || "").trim();
  const payloadObj = isPlainObject(payload) ? payload : null;
  const nestedProgress = isPlainObject(payloadObj?.progress)
    ? (payloadObj?.progress as Record<string, any>)
    : null;

  const explicitPhase = payloadObj
    ? firstCompactString(
        payloadObj,
        "run_phase",
        "backend_phase",
        "phase",
        "stage",
      )
    : "";
  const explicitStatus = payloadObj
    ? firstCompactString(payloadObj, "status", "state")
    : "";
  const explicitLabel = payloadObj
    ? firstCompactString(payloadObj, "label", "title", "step")
    : "";
  const explicitMessage = payloadObj
    ? firstCompactString(payloadObj, "message", "detail", "description")
    : "";
  const nestedLabel = nestedProgress
    ? firstCompactString(nestedProgress, "label", "message", "detail")
    : "";
  const progressPercent = payloadObj
    ? normalizeRunProgressPercent(payloadObj)
    : undefined;
  const statusKey = explicitStatus.toLowerCase();
  const neutralContextProgressPhase =
    CONTEXT_PROGRESS_EVENT_NAMES.has(name) && explicitPhase
      ? neutralContextProgressLabel(explicitPhase, statusKey)
      : "";

  if (neutralContextProgressPhase) {
    return {
      phase: explicitPhase,
      label: neutralContextProgressPhase,
      progressPercent,
    };
  }

  if (name === "run_diagnostic") {
    const code = payloadObj
      ? firstCompactString(payloadObj, "code", "kind")
      : "";
    const visibility = payloadObj
      ? firstCompactString(payloadObj, "visibility").toLowerCase()
      : "";
    const contextDiagnostic = isContextLikePayload(payload);
    return {
      phase: code || "run_diagnostic",
      label: contextDiagnostic
        ? "Context adjusted"
        : explicitLabel || "Run diagnostic",
      message:
        visibility === "debug" ? undefined : explicitMessage || undefined,
      progressPercent,
    };
  }

  if (name === "run_terminal") {
    const status = normalizeRunTerminalStatus(payloadObj);
    if (TERMINAL_FAILURE_STATUSES.has(status)) {
      return {
        phase: "failed",
        label: "Run failed",
        message: explicitMessage || undefined,
        progressPercent,
      };
    }
    if (status === "cancelled") {
      return {
        phase: "cancelled",
        label: "Run cancelled",
        message: explicitMessage || undefined,
        progressPercent,
      };
    }
    if (status === "completed") {
      return { phase: "complete", label: "Run complete", progressPercent: 100 };
    }
    return {
      phase: "terminal",
      label: "Run finished",
      message: explicitMessage || undefined,
      progressPercent,
    };
  }

  if (name === "error" && !isUserFacingRunErrorEvent(name, payload)) {
    return {
      phase: isContextLikePayload(payload)
        ? "context_progress"
        : "run_diagnostic",
      label: isContextLikePayload(payload)
        ? "Context adjusted"
        : "Run diagnostic",
      progressPercent,
    };
  }

  if (name === "tool_progress" && explicitPhase === "approval_required") {
    return {
      phase: "approval_required",
      label: "Waiting for your approval",
      message: explicitMessage || undefined,
      progressPercent,
    };
  }

  if (name === "run_waiting_external") {
    return {
      phase: "waiting_external",
      label: "Waiting for database query",
      message:
        explicitMessage ||
        "The query is still running. It will continue without occupying a model turn.",
    };
  }

  if (name === "run_resuming") {
    return {
      phase: "resuming",
      label: "Query update received",
      message: "Resuming the task from its saved checkpoint.",
    };
  }

  if (name === "run_waiting_user") {
    return {
      phase: "waiting_user",
      label: "Task paused for review",
      message:
        explicitMessage ||
        "Automatic continuations are paused. Required query work remains durable and was not abandoned or cancelled merely for taking time.",
    };
  }

  if (
    explicitPhase ||
    explicitStatus ||
    explicitLabel ||
    explicitMessage ||
    progressPercent !== undefined
  ) {
    const phase = explicitPhase || explicitStatus || name;
    return {
      phase,
      label:
        explicitLabel ||
        nestedLabel ||
        (phase ? humanizeRunStatusIdentifier(phase) : undefined),
      message: explicitMessage || undefined,
      progressPercent,
    };
  }

  switch (name) {
    case "assistant_start":
      return { phase: "assistant", label: "Assistant started" };
    case "thinking_start":
      return { phase: "thinking", label: "Thinking" };
    case "thinking_end":
      return { phase: "thinking_complete", label: "Thinking complete" };
    case "compaction_start":
      return { phase: "compacting", label: "Compacting context" };
    case "compaction_end":
      return { phase: "compaction_complete", label: "Context compacted" };
    case "tool_calls":
      return { phase: "tool_calls", label: "Starting tools" };
    case "tool_progress": {
      const toolName = payloadObj
        ? firstCompactString(payloadObj, "tool_name", "name")
        : "";
      return {
        phase: "tool_progress",
        label: toolName
          ? `Running ${humanizeRunStatusIdentifier(toolName)}`
          : "Running tool",
      };
    }
    case "tool_result":
      return { phase: "tool_result", label: "Tool result received" };
    case "artifact":
      return { phase: "artifact", label: "Artifact received" };
    case "assistant_final":
    case "final":
      return { phase: "finalizing", label: "Finalizing response" };
    case "run_end":
    case "end":
    case "done":
      return { phase: "complete", label: "Run complete", progressPercent: 100 };
    case "error":
      return { phase: "error", label: "Run error" };
    default:
      if (name.endsWith("_delta")) return {};
      return name
        ? { phase: name, label: humanizeRunStatusIdentifier(name) }
        : {};
  }
}

function previewOnlyDiagnosticFromEnvelope(
  envelope: ToolResultEnvelope | null | undefined,
): RunStreamDiagnostic | null {
  if (!envelope) return null;
  const result = envelope.result;
  const resultMetadata = isPlainObject(envelope.result_metadata)
    ? envelope.result_metadata
    : null;
  const previewOnly = Boolean(
    (envelope as any).preview_only ||
    (isPlainObject(result) && (result as any).preview_only === true) ||
    resultMetadata?.preview_only === true,
  );
  if (!previewOnly) return null;
  const artifactId = String(envelope.artifact_id || "").trim();
  const fullArtifactId = String(
    (envelope as any).full_artifact_id ||
      (isPlainObject(result) ? (result as any).full_artifact_id : "") ||
      (resultMetadata ? (resultMetadata as any).full_artifact_id : "") ||
      "",
  ).trim();
  return {
    kind: "preview_only_artifact",
    message: fullArtifactId
      ? "Preview artifact received; full data is stored separately."
      : "Preview artifact received.",
    ...(artifactId ? { artifactId } : {}),
    ...(fullArtifactId ? { fullArtifactId } : {}),
  };
}

function previewOnlyDiagnosticFromArtifactPayload(
  payload: any,
): RunStreamDiagnostic | null {
  if (!isPlainObject(payload)) return null;
  const metadata = isPlainObject(payload.metadata) ? payload.metadata : {};
  const previewOnly =
    payload.preview_only === true ||
    payload.partial === true ||
    metadata.preview_only === true ||
    metadata.partial === true;
  if (!previewOnly) return null;
  const artifactId = String(payload.id || "").trim();
  const fullArtifactId = String(
    payload.full_artifact_id || metadata.full_artifact_id || "",
  ).trim();
  return {
    kind: "preview_only_artifact",
    message: fullArtifactId
      ? "Preview artifact received; full data is stored separately."
      : "Preview artifact received.",
    ...(artifactId ? { artifactId } : {}),
    ...(fullArtifactId ? { fullArtifactId } : {}),
  };
}

function markRunStreamOpened(
  current: RunStreamStatus,
  now: number,
): RunStreamStatus {
  return {
    ...current,
    connectedAt: current.connectedAt ?? now,
    lastEventAt: now,
    lastEventName: "stream_open",
    phase: "stream_open",
    label: "Connected to run stream",
  };
}

function markRunStreamHeartbeat(
  current: RunStreamStatus,
  now: number,
): RunStreamStatus {
  const lastMeaningful =
    current.lastMeaningfulEventAt ?? current.connectedAt ?? current.startedAt;
  const durationMs = Math.max(0, now - lastMeaningful);
  const stale = durationMs >= HEARTBEAT_WITHOUT_PROGRESS_MS;
  return {
    ...current,
    lastEventAt: now,
    lastEventName: "heartbeat",
    lastHeartbeatAt: now,
    heartbeatCount: current.heartbeatCount + 1,
    diagnostic: stale
      ? {
          kind: "heartbeat_without_progress",
          message:
            "Backend heartbeats are still arriving, but no progress event has arrived recently.",
          since: lastMeaningful,
          durationMs,
        }
      : current.diagnostic,
  };
}

function markRunStreamMeaningfulEvent(
  current: RunStreamStatus,
  eventName: string,
  payload: any,
  now: number,
): RunStreamStatus {
  const fields = deriveRunStatusFields(eventName, payload);
  const terminal =
    eventName === "done" ||
    eventName === "run_end" ||
    eventName === "end" ||
    eventName === "final" ||
    eventName === "assistant_final" ||
    eventName === "run_terminal" ||
    eventName === "error";
  const currentDiagnostic =
    current.diagnostic?.kind === "heartbeat_without_progress"
      ? undefined
      : current.diagnostic;
  return {
    ...current,
    ...fields,
    lastEventAt: now,
    lastEventName: eventName,
    lastMeaningfulEventAt: now,
    lastMeaningfulEventName: eventName,
    meaningfulEventCount: current.meaningfulEventCount + 1,
    diagnostic: terminal ? undefined : currentDiagnostic,
  };
}

function unwrapSettingsStylePayload(raw: any): any {
  if (raw && typeof raw === "object") {
    const maybeData = (raw as any).data;
    if (maybeData && typeof maybeData === "object") return maybeData;
    const maybeThread = (raw as any).thread;
    const maybeMessages = (raw as any).messages;
    if (
      maybeThread &&
      typeof maybeThread === "object" &&
      Array.isArray(maybeMessages)
    )
      return raw;
  }
  return raw;
}

async function readJsonSafe(res: Response): Promise<any> {
  const text = await res.text().catch(() => "");
  if (!text.trim()) return null;
  const parsed = tryParseJson(text);
  return parsed.ok ? parsed.value : { error: text };
}

async function fetchJson(
  url: string,
  init?: RequestInit,
): Promise<{ ok: true; data: any } | { ok: false; status: number; data: any }> {
  const res = await fetch(url, {
    ...init,
    headers: {
      Accept: "application/json",
      ...(init?.headers || {}),
    },
  });
  const data = unwrapSettingsStylePayload(await readJsonSafe(res));
  if (!res.ok) return { ok: false, status: res.status, data };
  return { ok: true, data };
}

function parseDownloadFileName(
  header: string | null,
  fallback: string,
): string {
  const raw = String(header || "").trim();
  if (!raw) return fallback;
  const utf8Match = raw.match(/filename\*\s*=\s*UTF-8''([^;]+)/i);
  if (utf8Match?.[1]) {
    try {
      return decodeURIComponent(utf8Match[1]).trim() || fallback;
    } catch {
      // ignore decode failures
    }
  }
  const basicMatch =
    raw.match(/filename\s*=\s*"([^"]+)"/i) ||
    raw.match(/filename\s*=\s*([^;]+)/i);
  if (!basicMatch?.[1]) return fallback;
  return basicMatch[1].trim() || fallback;
}

function triggerDownload(blob: Blob, fileName: string) {
  const safeName = String(fileName || "").trim() || "download";
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = safeName;
  document.body.appendChild(link);
  link.click();
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}

function normalizeThread(raw: any): ThreadSummary | null {
  if (!raw || typeof raw !== "object") return null;
  const id = String(
    (raw as any).id ?? (raw as any).thread_id ?? (raw as any).threadId ?? "",
  ).trim();
  if (!id) return null;
  const titleRaw =
    (raw as any).title ?? (raw as any).name ?? (raw as any).label ?? null;
  const title = String(titleRaw ?? "").trim() || "Untitled";
  const summaryRaw =
    (raw as any).summary ??
    (raw as any).preview ??
    (raw as any).description ??
    null;
  const summary =
    typeof summaryRaw === "string" && summaryRaw.trim()
      ? summaryRaw.trim()
      : undefined;
  const statusRaw = (raw as any).status ?? null;
  const status =
    typeof statusRaw === "string" && statusRaw.trim()
      ? statusRaw.trim()
      : undefined;
  const artifactCountRaw =
    (raw as any).artifact_count ?? (raw as any).artifactCount ?? null;
  const artifactCount = Number(artifactCountRaw ?? NaN);
  const createdAt = Number(
    (raw as any).created_at ?? (raw as any).createdAt ?? NaN,
  );
  const updatedAt = Number(
    (raw as any).updated_at ??
      (raw as any).updatedAt ??
      (raw as any).last_activity_at ??
      NaN,
  );
  const lastTargetRaw =
    (raw as any).last_target ?? (raw as any).lastTarget ?? null;
  const lastTargetConnectionId = String(
    lastTargetRaw?.connection_id ?? lastTargetRaw?.connectionId ?? "",
  ).trim();
  const lastTarget = lastTargetConnectionId
    ? {
        connectionId: lastTargetConnectionId,
        model: String(lastTargetRaw?.model ?? "").trim(),
      }
    : undefined;
  return {
    id,
    title,
    summary,
    status,
    artifactCount: Number.isFinite(artifactCount) ? artifactCount : undefined,
    createdAt: Number.isFinite(createdAt) ? createdAt : undefined,
    updatedAt: Number.isFinite(updatedAt) ? updatedAt : undefined,
    lastTarget,
  };
}

/**
 * Exact requirements for the preflight resolver (plan §4.4): attachment ids with
 * a normalized/detected MIME fallback chain and byte sizes, plus the input
 * feature keys implied by the payload. Hosted capabilities are never inferred
 * from prompt text; the backend adds core requirements itself.
 */
function buildPreflightRequirements(
  images: Array<Record<string, any>>,
  files: Array<Record<string, any>>,
  imageIdOf: (img: any) => string,
  fileIdOf: (file: any) => string,
): PreflightRequirements {
  const attachments: PreflightAttachmentRequirement[] = [];
  images.forEach((img, i) => {
    attachments.push({
      id: imageIdOf(img) || String(img.localId || "") || `image-${i + 1}`,
      mime: String(img.mimeType || "image/*"),
      sizeBytes: Number(img.sizeBytes) || 0,
    });
  });
  const fileMime = (file: Record<string, any>): string =>
    String(
      file.detectedMimeType ||
        file.normalizedMimeType ||
        file.mimeType ||
        "application/octet-stream",
    );
  files.forEach((file, i) => {
    attachments.push({
      id: fileIdOf(file) || String(file.localId || "") || `file-${i + 1}`,
      mime: fileMime(file),
      sizeBytes: Number(file.sizeBytes) || 0,
    });
  });
  const features: string[] = [];
  const hasImages =
    images.length > 0 || files.some((f) => fileMime(f).startsWith("image/"));
  const hasDocuments = files.some((f) => !fileMime(f).startsWith("image/"));
  if (hasImages) features.push("images");
  if (hasDocuments) features.push("documents");
  return { attachments, features };
}

function isPreflightServiceMissing(error: unknown): boolean {
  return (
    error instanceof CapabilityApiError &&
    (error.status === 404 || error.status === 405 || error.status === 501)
  );
}

function isPlanStaleResponse(status: number, data: unknown): boolean {
  if (status !== 409) return false;
  const text = JSON.stringify(data ?? "").toLowerCase();
  return text.includes("plan_stale");
}

function normalizeThreadReference(raw: any): ThreadReference | null {
  if (!raw || typeof raw !== "object") return null;
  const normalized = normalizeThread(raw);
  if (!normalized) return null;
  return {
    id: normalized.id,
    title: normalized.title,
    summary: normalized.summary,
    updatedAt: normalized.updatedAt,
    status: normalized.status,
  };
}

function normalizeRunInfo(
  raw: any,
  fallbackThreadId?: string,
  extra?: Partial<RunInfo>,
): RunInfo | null {
  const source =
    isPlainObject(raw) && isPlainObject((raw as any).run)
      ? (raw as any).run
      : raw;
  if (!source || typeof source !== "object") return null;
  const id = String(
    (source as any).id ??
      (raw as any)?.runId ??
      (raw as any)?.run_id ??
      (raw as any)?.id ??
      "",
  ).trim();
  if (!id) return null;
  const threadId = String(
    (source as any).thread_id ??
      (source as any).threadId ??
      fallbackThreadId ??
      "",
  ).trim();
  if (!threadId) return null;
  const status =
    String((source as any).status ?? (raw as any)?.status ?? "queued")
      .trim()
      .toLowerCase() || "queued";
  return {
    id,
    threadId,
    status,
    ...(extra || {}),
  };
}

function extractActiveRunId(raw: any): string {
  return String(
    (raw as any)?.active_run_id ??
      (raw as any)?.activeRunId ??
      (raw as any)?.run_id ??
      "",
  ).trim();
}

function isTerminalRunStatus(status: string): boolean {
  const normalized = String(status || "")
    .trim()
    .toLowerCase();
  return (
    normalized === "completed" ||
    normalized === "failed" ||
    normalized === "cancelled"
  );
}

function normalizeThreadsList(raw: any): ThreadSummary[] {
  const data = unwrapSettingsStylePayload(raw);
  const arr = Array.isArray(data)
    ? data
    : Array.isArray((data as any)?.threads)
      ? (data as any).threads
      : Array.isArray((data as any)?.items)
        ? (data as any).items
        : [];
  return arr.map(normalizeThread).filter(Boolean) as ThreadSummary[];
}

function unwrapSingleArrayField(value: any): any {
  if (!isPlainObject(value)) return value;

  // BigQuery tool results commonly wrap rows under a "rows" key, sometimes alongside other fields (e.g. notes).
  // Prefer unwrapping "rows" when present to keep the UI dataset renderer working.
  const maybeRows = (value as any).rows;
  if (Array.isArray(maybeRows)) return maybeRows;

  const keys = Object.keys(value);
  if (keys.length !== 1) return value;
  const inner = (value as any)[keys[0]];
  return Array.isArray(inner) ? inner : value;
}

function isArrayOfObjects(value: any): value is Array<Record<string, any>> {
  return Array.isArray(value) && value.length > 0 && isPlainObject(value[0]);
}

function isArrayOfObjectsOrEmpty(
  value: any,
): value is Array<Record<string, any>> {
  return (
    Array.isArray(value) && (value.length === 0 || isPlainObject(value[0]))
  );
}

function shouldSuppressToolResultUI(
  toolName: string,
  resultFormat: ToolResultEnvelope["result_format"],
  result: any,
  envelope?: ToolResultEnvelope | null,
): boolean {
  if (
    shouldSuppressQueryToolResult({
      toolName,
      resultFormat,
      result,
      envelopeUiSuppress: envelope?.ui_suppress === true,
    })
  )
    return true;

  const durableJobId = normalizeManagedQueryJobId(isPlainObject(result) ? result.job_id : undefined)
    || normalizeManagedQueryJobId(envelope?.query_metadata?.job_id)
    || "";

  // Legacy preflight failures without a durable identity remain internal noise.
  // Profile-rejected durable jobs are visible so the inspector can render their
  // typed diagnostic, profile telemetry, and replacement plan.
  return (
    isQueryRunToolName(toolName) &&
    resultFormat === "json" &&
    isPlainObject(result) &&
    (result as any).preflight_failed === true && durableJobId === ""
  );
}

function getToolCallId(value: any): string {
  return typeof value?.id === "string" ? value.id.trim() : "";
}

function makeToolProgressKey(runId: string, toolCallId: string): string {
  const run = String(runId || "").trim();
  const call = String(toolCallId || "").trim();
  if (!run || !call) return "";
  return `${run}:${call}`;
}

function makeToolMessageDedupKey(
  runId: string,
  message: UIMessage,
  envelope: ToolResultEnvelope | null,
): string {
  const run = String(runId || "").trim();
  if (!run) return "";

  const messageId = typeof message.id === "string" ? message.id.trim() : "";
  if (messageId) return `${run}:message:${messageId}`;

  const seq = Number((message as any).seq);
  if (Number.isFinite(seq) && seq > 0) return `${run}:seq:${seq.toString(10)}`;

  const callId = String(
    envelope?.tool_call_id || (message as any).tool_call_id || "",
  ).trim();
  const artifactId = String(envelope?.artifact_id || "").trim();
  const toolName = String(envelope?.tool_name || message.name || "").trim();

  if (callId && artifactId) return `${run}:tool:${callId}:${artifactId}`;
  if (callId && toolName) return `${run}:tool:${callId}:${toolName}`;
  if (artifactId) return `${run}:artifact:${artifactId}`;
  return "";
}

function filterVisibleToolCalls(
  toolCalls: ToolCall[] | undefined,
  suppressedCallIds?: Set<string>,
): ToolCall[] {
  if (!Array.isArray(toolCalls)) return [];
  return toolCalls.filter((toolCall) => {
    const toolName = String((toolCall as any).name || "").trim();
    if (isHiddenQueryToolCallName(toolName)) return false;
    const callId = getToolCallId(toolCall);
    if (callId && suppressedCallIds?.has(callId)) return false;
    return true;
  });
}

function hasVisibleAssistantNarrative(message: UIMessage): boolean {
  if (typeof message.content === "string" && message.content.trim().length > 0)
    return true;

  const stablePrefix =
    typeof (message as any).streamStablePrefix === "string"
      ? (message as any).streamStablePrefix.trim()
      : "";
  if (stablePrefix) return true;

  const unstableSuffix =
    typeof (message as any).streamUnstableSuffix === "string"
      ? (message as any).streamUnstableSuffix.trim()
      : "";
  if (unstableSuffix) return true;

  const thinking =
    typeof (message as any).thinking === "string"
      ? (message as any).thinking.trim()
      : "";
  if (thinking) return true;

  const compactionSummary =
    typeof (message as any).compactionSummary === "string"
      ? (message as any).compactionSummary.trim()
      : "";
  if (compactionSummary) return true;
  if (hasMeaningfulCompactionStats((message as any).compactionStats || null))
    return true;

  return (
    (message as any).compactionInProgress === true ||
    (message as any).compactionComplete === true
  );
}

function sanitizeAssistantMessageForUI(
  message: UIMessage,
  suppressedCallIds?: Set<string>,
): UIMessage | null {
  if (message.role !== Role.Assistant) return message;

  const toolCalls = Array.isArray(message.tool_calls) ? message.tool_calls : [];
  const visibleToolCalls = filterVisibleToolCalls(toolCalls, suppressedCallIds);
  const hasVisibleToolCalls = visibleToolCalls.length > 0;
  const hasNarrative = hasVisibleAssistantNarrative(message);

  if (!hasNarrative && !hasVisibleToolCalls) {
    return null;
  }

  if (toolCalls.length === visibleToolCalls.length) {
    return message;
  }

  const updated: UIMessage = { ...message };
  if (visibleToolCalls.length > 0) {
    (updated as any).tool_calls = visibleToolCalls;
  } else {
    delete (updated as any).tool_calls;
  }
  return updated;
}

function collectSuppressedToolCallIds(messages: UIMessage[]): Set<string> {
  const suppressed = new Set<string>();
  for (const msg of messages) {
    if (msg.role !== Role.Tool) continue;
    const env = tryParseToolResultEnvelope(msg.content || null);
    if (!env) continue;
    const toolName = (env.tool_name || msg.name || "").trim();
    if (
      !shouldSuppressToolResultUI(toolName, env.result_format, env.result, env)
    )
      continue;
    const callId = String(
      env.tool_call_id || (msg as any).tool_call_id || "",
    ).trim();
    if (callId) suppressed.add(callId);
  }
  return suppressed;
}

function sanitizeMessagesForToolUI(messages: UIMessage[]): UIMessage[] {
  if (!Array.isArray(messages) || messages.length === 0) return [];
  const suppressedCallIds = collectSuppressedToolCallIds(messages);
  const next: UIMessage[] = [];

  for (const msg of messages) {
    if (msg.role === Role.Tool) {
      const env = tryParseToolResultEnvelope(msg.content || null);
      if (env) {
        const toolName = (env.tool_name || msg.name || "").trim();
        if (
          shouldSuppressToolResultUI(
            toolName,
            env.result_format,
            env.result,
            env,
          )
        ) {
          continue;
        }
      } else if (msg.name === "check_query_status") {
        continue;
      }
      next.push(msg);
      continue;
    }

    if (msg.role === Role.Assistant) {
      const sanitizedAssistant = sanitizeAssistantMessageForUI(
        msg,
        suppressedCallIds,
      );
      if (sanitizedAssistant) {
        next.push(sanitizedAssistant);
      }
      continue;
    }

    next.push(msg);
  }

  return next;
}

function tryParseToolResultEnvelope(
  text: string | null,
): ToolResultEnvelope | null {
  if (!text) return null;
  const parsed = tryParseJson(text);
  if (!parsed.ok) return null;
  if (!isPlainObject(parsed.value)) return null;
  const v = parsed.value as any;
  if (typeof v.artifact_id !== "string") return null;
  if (typeof v.ok !== "boolean") return null;
  if (v.result_format !== "json" && v.result_format !== "text") return null;
  return v as ToolResultEnvelope;
}

function parseReadFileResult(
  text: string,
): { path: string; content: string; language: string } | null {
  const match = text.match(/^File: (.+)\n\n/);
  if (!match) return null;
  const path = match[1].trim();
  const content = text.replace(/^File: .+\n\n/, "");
  const ext = path.match(/\.([a-zA-Z0-9]+)$/)?.[1];
  return { path, content, language: ext || "text" };
}

function artifactIsPreviewLike(artifact: Artifact | null | undefined): boolean {
  const meta = artifact?.metadata || {};
  return (
    meta.placeholder === true ||
    meta.partial === true ||
    meta.preview_only === true
  );
}

function normalizeArtifactForClient(artifact: Artifact): Artifact {
  const raw = artifact as Artifact & Record<string, any>;
  const metadata: Record<string, any> = { ...(artifact.metadata || {}) };
  for (const key of [
    "tool_name",
    "tool_call_id",
    "tool_args",
    "tool_presentation",
    "result_metadata",
    "query_metadata",
    "result_format",
    "raw_result",
    "ok",
    "byte_size",
    "inline_preview",
    "plot_spec",
    "chart_budget",
    "chart_engine",
    "chart_spec_version",
    "chart_module",
    "chart_modules",
    "preview_only",
    "full_artifact_id",
    "row_count_total",
    "downsample_ratio",
    "placeholder",
    "partial",
    "render_mode",
    "artifact_kind",
    "mimeType",
    "mime_type",
    "source_url",
    "source_id",
  ]) {
    if (metadata[key] === undefined && raw[key] !== undefined) {
      metadata[key] = raw[key];
    }
  }
  return { ...artifact, metadata };
}

function makePlaceholderArtifact(id: string, title?: string): Artifact {
  return {
    id,
    type: "code",
    title: title || "Artifact",
    content: "",
    language: "text",
    metadata: { placeholder: true },
  };
}

function parseImageDataUri(value: unknown): string | null {
  if (typeof value !== "string") return null;
  const trimmed = value.trim();
  if (!trimmed) return null;
  if (!/^data:image\//i.test(trimmed)) return null;
  return trimmed;
}

function extractImageDataUriFromToolResult(
  resultFormat: ToolResultEnvelope["result_format"],
  result: any,
  fallbackRawContent: string | null,
): string | null {
  if (resultFormat === "text") {
    return parseImageDataUri(
      typeof result === "string" ? result : fallbackRawContent,
    );
  }
  if (!isPlainObject(result)) return null;

  const direct = parseImageDataUri(
    (result as any).dataUri ??
      (result as any).data_uri ??
      (result as any).image ??
      (result as any).imageDataUri ??
      (result as any).image_data_uri ??
      (result as any).url,
  );
  if (direct) return direct;

  const imageURL = (result as any).image_url;
  if (isPlainObject(imageURL)) {
    return parseImageDataUri((imageURL as any).url);
  }

  return null;
}

function inferImageMimeTypeFromDataUri(dataUri: string): string | undefined {
  const raw = dataUri.trim();
  const comma = raw.indexOf(",");
  if (comma <= 5) return undefined;
  const meta = raw.slice(5, comma);
  const semi = meta.indexOf(";");
  const mime = (semi >= 0 ? meta.slice(0, semi) : meta).trim().toLowerCase();
  if (!mime.startsWith("image/")) return undefined;
  return mime;
}

function getImageAttachmentArtifactId(attachment: UIImageAttachment): string {
  const raw =
    (attachment as any)?.artifactId ?? (attachment as any)?.artifact_id;
  if (typeof raw !== "string") return "";
  return raw.trim();
}

function getFileAttachmentArtifactId(attachment: UIFileAttachment): string {
  const raw =
    (attachment as any)?.artifactId ?? (attachment as any)?.artifact_id;
  if (typeof raw !== "string") return "";
  return raw.trim();
}

function buildImageArtifactFromAttachment(
  attachment: UIImageAttachment,
): Artifact | null {
  const artifactId = getImageAttachmentArtifactId(attachment);
  const dataUri = parseImageDataUri(
    (attachment as any)?.dataUri ?? (attachment as any)?.data_uri,
  );
  if (!artifactId || !dataUri) return null;
  const fileName =
    String(
      (attachment as any)?.fileName ?? (attachment as any)?.file_name ?? "",
    ).trim() || "Image attachment";
  const mimeTypeRaw = String(
    (attachment as any)?.mimeType ?? (attachment as any)?.mime_type ?? "",
  )
    .trim()
    .toLowerCase();
  const mimeType = mimeTypeRaw.startsWith("image/")
    ? mimeTypeRaw
    : inferImageMimeTypeFromDataUri(dataUri);
  const sizeBytes = Number(
    (attachment as any)?.sizeBytes ?? (attachment as any)?.size_bytes ?? NaN,
  );
  const width = Number((attachment as any)?.width ?? NaN);
  const height = Number((attachment as any)?.height ?? NaN);
  const sourceRaw = String((attachment as any)?.source ?? "")
    .trim()
    .toLowerCase();
  const source =
    sourceRaw === "paste" || sourceRaw === "drop" || sourceRaw === "upload"
      ? sourceRaw
      : undefined;

  return {
    id: artifactId,
    type: "image",
    title: fileName,
    content: dataUri,
    metadata: {
      ...(mimeType ? { mimeType } : {}),
      ...(Number.isFinite(sizeBytes) && sizeBytes > 0 ? { sizeBytes } : {}),
      ...(Number.isFinite(width) && width > 0 ? { width } : {}),
      ...(Number.isFinite(height) && height > 0 ? { height } : {}),
      ...(source ? { source } : {}),
      placeholder: false,
    },
  };
}

function extractErrorMessage(raw: any, status: number): string {
  if (typeof raw === "string" && raw.trim()) return raw.trim();
  if (raw && typeof raw === "object") {
    const msg =
      (raw as any).message ?? (raw as any).error ?? (raw as any).detail ?? null;
    if (typeof msg === "string" && msg.trim()) return msg.trim();
  }
  return `HTTP ${status}`;
}

function extractHint(raw: any): string {
  if (!raw || typeof raw !== "object") return "";
  const hint = (raw as any).hint;
  return typeof hint === "string" ? hint.trim() : "";
}

function normalizeThrownErrorMessage(err: any, fallback: string): string {
  const msg = String(err?.message || err || "").trim();
  if (!msg) return fallback;
  if (msg.toLowerCase() === "failed to fetch") {
    return "Failed to fetch from the local server. Check that the app server is running and reachable.";
  }
  return msg;
}

function normalizeStringArray(...values: any[]): string[] | undefined {
  const out: string[] = [];
  const seen = new Set<string>();

  for (const value of values) {
    if (!Array.isArray(value)) continue;
    for (const item of value) {
      if (typeof item !== "string") continue;
      const trimmed = item.trim();
      if (!trimmed || seen.has(trimmed)) continue;
      seen.add(trimmed);
      out.push(trimmed);
    }
  }

  return out.length > 0 ? out : undefined;
}

function addStringArtifactIds(
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
    for (const item of value) addStringArtifactIds(out, seen, item);
  }
}

function addAttachmentArtifactIdsFromRecord(
  out: string[],
  seen: Set<string>,
  record: Record<string, any>,
) {
  addStringArtifactIds(out, seen, record.attachment_artifact_id);
  addStringArtifactIds(out, seen, record.attachmentArtifactId);
  addStringArtifactIds(out, seen, record.attachment_artifact_ids);
  addStringArtifactIds(out, seen, record.attachmentArtifactIds);

  for (const listKey of ["attachments", "files", "downloads"]) {
    const list = record[listKey];
    if (!Array.isArray(list)) continue;
    for (const item of list) {
      if (!isPlainObject(item)) continue;
      addStringArtifactIds(out, seen, (item as any).artifact_id);
      addStringArtifactIds(out, seen, (item as any).artifactId);
      addStringArtifactIds(out, seen, (item as any).attachment_artifact_id);
      addStringArtifactIds(out, seen, (item as any).attachmentArtifactId);
    }
  }
}

function extractToolResultAttachmentArtifactIds(
  envelope: ToolResultEnvelope,
  message?: UIMessage,
): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  if (isPlainObject(envelope.result)) {
    addAttachmentArtifactIdsFromRecord(
      out,
      seen,
      envelope.result as Record<string, any>,
    );
  }
  if (isPlainObject(envelope.result_metadata)) {
    addAttachmentArtifactIdsFromRecord(
      out,
      seen,
      envelope.result_metadata as Record<string, any>,
    );
  }
  for (const attachment of message?.fileAttachments || []) {
    addStringArtifactIds(out, seen, attachment.artifactId);
  }
  addStringArtifactIds(
    out,
    seen,
    normalizeStringArray(
      (message as any)?.referencedArtifactIds,
      (message as any)?.referenced_artifact_ids,
    ),
  );
  const mainArtifactId = String(envelope.artifact_id || "").trim();
  return mainArtifactId ? out.filter((id) => id !== mainArtifactId) : out;
}

function normalizeChartBudget(...values: any[]): ChartBudgetReport | undefined {
  for (const value of values) {
    if (!isPlainObject(value)) continue;
    const out: Record<string, any> = {};
    const schemaVersion = String((value as any).schema_version || "").trim();
    if (schemaVersion) out.schema_version = schemaVersion;
    const chartKind = String((value as any).chart_kind || "").trim();
    if (chartKind) out.chart_kind = chartKind;
    const chartEngine = String((value as any).chart_engine || "").trim();
    if (chartEngine) out.chart_engine = chartEngine;
    const asFiniteNonNegativeInt = (raw: any): number | undefined => {
      const n = Number(raw);
      if (!Number.isFinite(n) || n < 0) return undefined;
      return Math.trunc(n);
    };
    const rowCountTotal = asFiniteNonNegativeInt(
      (value as any).row_count_total,
    );
    if (rowCountTotal !== undefined) out.row_count_total = rowCountTotal;
    const pointsRequested = asFiniteNonNegativeInt(
      (value as any).points_requested,
    );
    if (pointsRequested !== undefined) out.points_requested = pointsRequested;
    const pointsReturned = asFiniteNonNegativeInt(
      (value as any).points_returned,
    );
    if (pointsReturned !== undefined) out.points_returned = pointsReturned;
    const seriesCount = asFiniteNonNegativeInt((value as any).series_count);
    if (seriesCount !== undefined) out.series_count = seriesCount;
    const plotSpecBytes = asFiniteNonNegativeInt(
      (value as any).plot_spec_bytes,
    );
    if (plotSpecBytes !== undefined) out.plot_spec_bytes = plotSpecBytes;
    const rowsPreviewBytes = asFiniteNonNegativeInt(
      (value as any).rows_preview_bytes,
    );
    if (rowsPreviewBytes !== undefined)
      out.rows_preview_bytes = rowsPreviewBytes;
    const resultPayloadBytes = asFiniteNonNegativeInt(
      (value as any).result_payload_bytes,
    );
    if (resultPayloadBytes !== undefined)
      out.result_payload_bytes = resultPayloadBytes;
    const sseEventBytes = asFiniteNonNegativeInt(
      (value as any).sse_event_bytes,
    );
    if (sseEventBytes !== undefined) out.sse_event_bytes = sseEventBytes;
    const budgetActions = normalizeStringArray((value as any).budget_actions);
    if (budgetActions) out.budget_actions = budgetActions;
    const failureStage = String((value as any).failure_stage || "").trim();
    if (failureStage) out.failure_stage = failureStage;
    const failureCode = String((value as any).failure_code || "").trim();
    if (failureCode) out.failure_code = failureCode;
    const failureMessage = String((value as any).failure_message || "").trim();
    if (failureMessage) out.failure_message = failureMessage;
    if (Object.keys(out).length > 0) return out as ChartBudgetReport;
  }
  return undefined;
}

function normalizeFileAttachmentSnapshots(raw: unknown): ChatFileAttachment[] {
  if (!Array.isArray(raw)) return [];
  const out: ChatFileAttachment[] = [];
  for (const item of raw) {
    if (!item || typeof item !== "object") continue;
    const obj = item as Record<string, any>;
    const artifactId = String(obj.artifactId ?? obj.artifact_id ?? "").trim();
    if (!artifactId) continue;
    const str = (...keys: string[]): string | undefined => {
      for (const key of keys) {
        const v = obj[key];
        if (typeof v === "string" && v.trim()) return v.trim();
      }
      return undefined;
    };
    const bool = (...keys: string[]): boolean | undefined => {
      for (const key of keys) {
        if (typeof obj[key] === "boolean") return obj[key];
      }
      return undefined;
    };
    const num = (...keys: string[]): number | undefined => {
      for (const key of keys) {
        const v = Number(obj[key]);
        if (Number.isFinite(v) && v > 0) return v;
      }
      return undefined;
    };
    const fileKind = str("fileKind", "file_kind");
    const mimeType = str("mimeType", "mime_type");
    const detectedMimeType = str("detectedMimeType", "detected_mime_type");
    const isImage =
      String(fileKind || "").toLowerCase() === "image" ||
      String(detectedMimeType || mimeType || "")
        .toLowerCase()
        .startsWith("image/");
    out.push({
      artifactId,
      fileName: str("fileName", "file_name") || "Uploaded file",
      mimeType,
      detectedMimeType,
      fileKind,
      normalizedMimeType: str("normalizedMimeType", "normalized_mime_type"),
      sha256: str("sha256"),
      sizeBytes: num("sizeBytes", "size_bytes"),
      isText: bool("isText", "is_text"),
      previewAvailable: bool("previewAvailable", "preview_available"),
      previewTruncated: bool("previewTruncated", "preview_truncated"),
      previewText: str("previewText", "preview_text"),
      contentNote: str("contentNote", "content_note"),
      source: str("source") as ChatFileAttachment["source"],
      thumbnailArtifactId: isImage ? artifactId : undefined,
      thumbnailUrl: isImage ? str("thumbnailUrl", "thumbnail_url") : undefined,
    });
  }
  return out;
}

function normalizeMessage(raw: any): UIMessage | null {
  if (!raw || typeof raw !== "object" || typeof raw.role !== "string")
    return null;
  const next: UIMessage = { ...(raw as UIMessage) };

  const idRaw =
    (raw as any).id ??
    (raw as any).message_id ??
    (raw as any).messageId ??
    null;
  if (typeof idRaw === "string" && idRaw.trim()) {
    next.id = idRaw.trim();
  }

  const seqRaw =
    (raw as any).seq ??
    (raw as any).message_seq ??
    (raw as any).messageSeq ??
    null;
  const seq = Number(seqRaw);
  if (Number.isFinite(seq) && seq > 0) {
    next.seq = seq;
  }

  const referencedArtifactIds = normalizeStringArray(
    (raw as any).referencedArtifactIds,
    (raw as any).referenced_artifact_ids,
  );
  if (referencedArtifactIds) {
    next.referencedArtifactIds = referencedArtifactIds;
    next.referenced_artifact_ids = referencedArtifactIds;
  }

  const referencedMessageIds = normalizeStringArray(
    (raw as any).referencedMessageIds,
    (raw as any).referenced_message_ids,
  );
  if (referencedMessageIds) {
    next.referencedMessageIds = referencedMessageIds;
    next.referenced_message_ids = referencedMessageIds;
  }

  const referencedThreadIds = normalizeStringArray(
    (raw as any).referencedThreadIds,
    (raw as any).referenced_thread_ids,
  );
  if (referencedThreadIds) {
    next.referencedThreadIds = referencedThreadIds;
    next.referenced_thread_ids = referencedThreadIds;
  }

  const referencedThreadsRaw = Array.isArray((raw as any).referencedThreads)
    ? (raw as any).referencedThreads
    : Array.isArray((raw as any).referenced_threads)
      ? (raw as any).referenced_threads
      : [];
  const referencedThreads = referencedThreadsRaw
    .map(normalizeThreadReference)
    .filter(Boolean) as ThreadReference[];
  if (referencedThreads.length > 0) {
    next.referencedThreads = referencedThreads;
    next.referenced_threads = referencedThreads;
  }

  const hiddenContext =
    typeof (raw as any).hiddenContext === "string" &&
    (raw as any).hiddenContext.trim()
      ? (raw as any).hiddenContext.trim()
      : typeof (raw as any).hidden_context === "string" &&
          (raw as any).hidden_context.trim()
        ? (raw as any).hidden_context.trim()
        : "";
  if (hiddenContext) {
    next.hiddenContext = hiddenContext;
  }

  // Persisted messages carry attachment snapshots under snake_case keys.
  // Normalize them so transcript cards can render without waiting for
  // artifact hydration.
  const fileAttachments = normalizeFileAttachmentSnapshots(
    (raw as any).fileAttachments ?? (raw as any).file_attachments,
  );
  if (fileAttachments.length > 0) {
    next.fileAttachments = fileAttachments;
  }

  if (next.role === Role.Assistant && next._uiStreaming !== true) {
    next._uiStreaming = false;
  }

  return next;
}

function ensureUiStableIds(
  messages: UIMessage[],
  createUiStableId: () => string,
): UIMessage[] {
  let mutated = false;
  const next = messages.map((message) => {
    if (
      typeof message?._uiStableId === "string" &&
      message._uiStableId.trim()
    ) {
      return message;
    }
    mutated = true;
    const persistedId = String(message?.id || "").trim();
    return {
      ...message,
      _uiStableId: persistedId
        ? `persisted-message:${persistedId}`
        : createUiStableId(),
    };
  });
  return mutated ? next : messages;
}

function normalizeMessages(raw: any): UIMessage[] {
  const data = unwrapSettingsStylePayload(raw);
  const arr = Array.isArray((data as any)?.messages)
    ? (data as any).messages
    : Array.isArray(data)
      ? data
      : [];
  return arr
    .map((m: any) => normalizeMessage(m))
    .filter(Boolean) as UIMessage[];
}

export function useChat({
  settings,
  projectId,
  onPreflightAttachmentRemoved,
}: UseChatOptions) {
  const [threads, setThreads] = useState<ThreadSummary[]>([]);
  const [activeThreadId, setActiveThreadId] = useState<string | null>(null);

  // ---- Capability preflight / per-thread target state (plan §4.4–§4.6) ----
  const [threadTargetOverrides, setThreadTargetOverrides] = useState<
    Record<string, ThreadTarget>
  >({});
  const threadTargetOverridesRef = useRef<Record<string, ThreadTarget>>({});
  useEffect(() => {
    threadTargetOverridesRef.current = threadTargetOverrides;
  }, [threadTargetOverrides]);
  const [preflightGate, setPreflightGate] = useState<PreflightGateState | null>(
    null,
  );
  const preflightGateResolverRef = useRef<
    ((decision: PreflightGateDecision) => void) | null
  >(null);
  const [preflightNotice, setPreflightNotice] = useState<{
    threadId: string;
    texts: string[];
  } | null>(null);
  /** planId → transcript row registry so plan_update events can extend rows. */
  const planRowsRef = useRef<
    Record<string, { threadId: string; stableId: string; runId?: string }>
  >({});
  /** Target used by each thread's previous run (thread-differential plan rows). */
  const lastRunTargetByThreadRef = useRef<Record<string, ThreadTarget>>({});
  /**
   * Per-thread send-negotiation lock (auth → preflight gate → run submit).
   * There is a single preflight gate resolver: a concurrent send would clobber
   * it (hanging the first send's promise), and a composer target switch mid-
   * check would race the authenticated target — both fail closed while a
   * negotiation is in flight.
   */
  const negotiationLocksRef = useRef<Record<string, true>>({});
  const [negotiatingByThreadId, setNegotiatingByThreadId] = useState<
    Record<string, boolean>
  >({});

  const [messagesByThreadId, setMessagesByThreadId] = useState<
    Record<string, UIMessage[]>
  >({});
  const [artifactsById, setArtifactsById] = useState<Record<string, Artifact>>(
    {},
  );
  const [runningByThreadId, setRunningByThreadId] = useState<
    Record<string, RunInfo>
  >({});
  const [runStreamStatusByThreadId, setRunStreamStatusByThreadId] = useState<
    Record<string, RunStreamStatus>
  >({});
  const [toolProgressByKey, setToolProgressByKey] = useState<
    Record<string, ToolProgressEvent>
  >({});

  const messagesRef = useRef<Record<string, UIMessage[]>>({});
  const runningByThreadRef = useRef<Record<string, RunInfo>>({});
  const abortByRunIdRef = useRef<Record<string, AbortController>>({});
  const waitAbortByRunIdRef = useRef<Record<string, AbortController>>({});
  const streamSegmentersByRunIdRef = useRef<
    Record<string, StreamingMarkdownSegmenter>
  >({});
  const threadsRef = useRef<ThreadSummary[]>([]);
  const uiMessageCounterRef = useRef(0);

  const createUiStableId = useCallback((): string => {
    uiMessageCounterRef.current += 1;
    return `ui-msg-${Date.now().toString(36)}-${uiMessageCounterRef.current.toString(36)}`;
  }, []);

  const isLoading = Object.keys(runningByThreadId).length > 0;
  const isActiveThreadRunning = !!(
    activeThreadId && runningByThreadId[activeThreadId]
  );

  const runningThreadIds = useMemo(
    () => Object.keys(runningByThreadId),
    [runningByThreadId],
  );

  const activeRunStreamStatus = activeThreadId
    ? runStreamStatusByThreadId[activeThreadId] || null
    : null;

  const setRunStreamStatusForThread = useCallback(
    (
      threadId: string,
      updater:
        | RunStreamStatus
        | null
        | ((prev: RunStreamStatus | null) => RunStreamStatus | null),
    ) => {
      const id = String(threadId || "").trim();
      if (!id) return;
      setRunStreamStatusByThreadId((prev) => {
        const current = prev[id] || null;
        const nextStatus =
          typeof updater === "function" ? updater(current) : updater;
        const next = { ...prev };
        if (nextStatus) next[id] = nextStatus;
        else delete next[id];
        return next;
      });
    },
    [],
  );

  const setRunForThread = useCallback(
    (threadId: string, run: RunInfo | null) => {
      const id = String(threadId || "").trim();
      if (!id) return;
      const next = { ...runningByThreadRef.current };
      if (run) next[id] = run;
      else delete next[id];
      runningByThreadRef.current = next;
      setRunningByThreadId(next);
    },
    [],
  );

  const setMessagesForThread = useCallback(
    (
      threadId: string,
      nextOrUpdater: UIMessage[] | ((prev: UIMessage[]) => UIMessage[]),
    ) => {
      setMessagesByThreadId((prev) => {
        const prevMsgs = prev[threadId] || [];
        const rawNextMsgs =
          typeof nextOrUpdater === "function"
            ? (nextOrUpdater as (p: UIMessage[]) => UIMessage[])(prevMsgs)
            : nextOrUpdater;
        const nextMsgs = ensureUiStableIds(rawNextMsgs, createUiStableId);
        messagesRef.current = { ...messagesRef.current, [threadId]: nextMsgs };
        return { ...prev, [threadId]: nextMsgs };
      });
    },
    [createUiStableId],
  );

  const upsertArtifact = useCallback((incoming: Artifact) => {
    const artifact = normalizeArtifactForClient(incoming);
    setArtifactsById((prev) => {
      const existing = prev[artifact.id];
      const incomingMetadata = artifact.metadata || {};
      const incomingSandboxedHtml =
        incomingMetadata.render_mode === SANDBOXED_HTML_RENDER_MODE &&
        incomingMetadata.sandbox === SANDBOXED_HTML_SANDBOX_POLICY &&
        incomingMetadata.network === SANDBOXED_HTML_NETWORK_POLICY;
      if (
        existing &&
        !artifactIsPreviewLike(existing) &&
        artifactIsPreviewLike(artifact)
      ) {
        return {
          ...prev,
          [artifact.id]: {
            ...existing,
            language: existing.language || artifact.language,
            metadata: {
              ...incomingMetadata,
              ...(existing.metadata || {}),
              placeholder: false,
              partial: false,
              preview_only: false,
            },
          },
        };
      }
      // Preserve newer/non-placeholder content when present.
      if (
        existing &&
        existing.metadata?.placeholder !== true &&
        artifact.metadata?.placeholder === true
      ) {
        if (incomingSandboxedHtml) {
          return {
            ...prev,
            [artifact.id]: {
              ...existing,
              language: existing.language || artifact.language,
              metadata: {
                ...incomingMetadata,
                ...(existing.metadata || {}),
                render_mode: SANDBOXED_HTML_RENDER_MODE,
                sandbox: SANDBOXED_HTML_SANDBOX_POLICY,
                network: SANDBOXED_HTML_NETWORK_POLICY,
                placeholder: false,
              },
            },
          };
        }
        return prev;
      }
      return { ...prev, [artifact.id]: artifact };
    });
  }, []);

  const lastToastRef = useRef<string>("");
  const toastOnce = useCallback((msg: string) => {
    const text = String(msg || "").trim();
    if (!text) return;
    if (lastToastRef.current === text) return;
    lastToastRef.current = text;
    toast.error(text);
  }, []);

  const appendAssistantError = useCallback(
    (threadId: string, message: string) => {
      const msg = String(message || "").trim();
      if (!msg) return;
      const errorMessage: UIMessage = {
        role: Role.Assistant,
        content: `Error: ${msg}`,
        _uiStreaming: false,
      };
      setMessagesForThread(threadId, (prev) => {
        const last = prev[prev.length - 1];
        const lastIsEmptyPlaceholder = Boolean(
          last &&
          last.role === Role.Assistant &&
          !String(last.content || "").trim() &&
          !Array.isArray(last.tool_calls) &&
          !String(last.thinking || "").trim(),
        );
        return lastIsEmptyPlaceholder
          ? prev.slice(0, -1).concat([errorMessage])
          : prev.concat([errorMessage]);
      });
    },
    [setMessagesForThread],
  );

  const bumpThreadToTop = useCallback((threadId: string) => {
    const id = String(threadId || "").trim();
    if (!id) return;
    setThreads((prev) => {
      const idx = prev.findIndex((t) => t.id === id);
      if (idx <= 0) return prev;
      const t = prev[idx];
      return [t, ...prev.slice(0, idx), ...prev.slice(idx + 1)];
    });
  }, []);

  const refreshThreads = useCallback(async (): Promise<ThreadSummary[]> => {
    let res: Awaited<ReturnType<typeof fetchJson>>;
    try {
      res = await fetchJson("/api/threads", { method: "GET" });
    } catch (e: any) {
      toastOnce(normalizeThrownErrorMessage(e, "Failed to refresh threads."));
      return [];
    }
    if (res.ok === false) {
      const msg = extractErrorMessage(res.data, res.status);
      const hint = extractHint(res.data);
      // Don't spam; this can run at startup and after runs.
      toastOnce(hint ? `${msg}\n\n${hint}` : msg);
      return [];
    }
    const incoming = normalizeThreadsList(res.data);
    setThreads((prev) => {
      // Initial load: pick a reasonable deterministic order (most-recent first).
      if (prev.length === 0) {
        const sorted = [...incoming].sort(
          (a, b) => (b.updatedAt ?? 0) - (a.updatedAt ?? 0),
        );
        threadsRef.current = sorted;
        return sorted;
      }

      // Preserve the existing order; only update fields and append new items.
      const byId = new Map(incoming.map((t) => [t.id, t]));
      const next: ThreadSummary[] = [];
      const seen = new Set<string>();

      for (const t of prev) {
        const upd = byId.get(t.id);
        if (!upd) continue; // server is authoritative (e.g., archived/deleted threads drop out)
        next.push(upd);
        seen.add(t.id);
      }
      for (const t of incoming) {
        if (seen.has(t.id)) continue;
        next.push(t);
      }

      threadsRef.current = next;
      return next;
    });
    return incoming;
  }, [toastOnce]);

  const loadThread = useCallback(
    async (threadId: string) => {
      let res: Awaited<ReturnType<typeof fetchJson>>;
      try {
        res = await fetchJson(`/api/threads/${encodeURIComponent(threadId)}`, {
          method: "GET",
        });
      } catch {
        return;
      }
      if (!res.ok) return;
      const data = res.data;
      const thread =
        normalizeThread(
          (data as any)?.thread ?? (data as any)?.data?.thread ?? data,
        ) || normalizeThread(data);
      if (thread) {
        setThreads((prev) => {
          const idx = prev.findIndex((t) => t.id === thread.id);
          if (idx === -1) {
            // If the thread is missing locally, append it without changing existing order.
            const next = prev.concat([thread]);
            threadsRef.current = next;
            return next;
          }
          const next = prev.slice();
          next[idx] = thread;
          threadsRef.current = next;
          return next;
        });
      }
      const msgs = normalizeMessages(data);
      // Keep the same stable ids in the async plan-hydration closure and the
      // rendered transcript so persisted rows can be inserted at their run's
      // user-message anchor even if a newer send starts while plans load.
      const visibleMsgs = ensureUiStableIds(
        sanitizeMessagesForToolUI(msgs),
        createUiStableId,
      );
      if (msgs.length > 0) {
        setMessagesForThread(threadId, visibleMsgs);
      } else if (!messagesRef.current[threadId]) {
        setMessagesForThread(threadId, []);
      }

      // Optional: backend may return artifacts inline.
      const artifactsArr = Array.isArray((data as any)?.artifacts)
        ? (data as any).artifacts
        : Array.isArray((data as any)?.data?.artifacts)
          ? (data as any).data.artifacts
          : null;
      if (Array.isArray(artifactsArr)) {
        for (const a of artifactsArr) {
          if (!a || typeof a !== "object") continue;
          if (typeof (a as any).id !== "string") continue;
          upsertArtifact(a as Artifact);
        }
      }

      // Seed placeholder artifacts from tool result envelopes so ToolResultBlock can open them.
      for (const m of visibleMsgs) {
        if (m.role !== Role.Tool) continue;
        const env = tryParseToolResultEnvelope(m.content || null);
        if (!env) continue;
        const toolName = (env.tool_name || m.name || "").trim();
        const suppressUi = shouldSuppressToolResultUI(
          toolName,
          env.result_format,
          env.result,
          env,
        );
        if (suppressUi) continue;
        // Build a concrete artifact immediately when possible (avoids empty placeholders on reload).
        applyToolResultMessageSideEffects(m);
        if (!artifactsById[env.artifact_id]) {
          upsertArtifact(
            makePlaceholderArtifact(
              env.artifact_id,
              `${toolName || "tool"} result`,
            ),
          );
        }
      }

      // Hydrate referenced artifacts used by user messages so inline chips/thumbnails render
      // even when artifacts were created in previous runs or sessions.
      const referencedArtifactIDs = new Set<string>();
      const fileAttachmentTitleById = new Map<string, string>();
      for (const m of visibleMsgs) {
        if (m.role !== Role.User) continue;
        const refs = normalizeStringArray(
          (m as any).referencedArtifactIds,
          (m as any).referenced_artifact_ids,
        );
        if (refs) for (const id of refs) referencedArtifactIDs.add(id);
        for (const attachment of m.fileAttachments || []) {
          if (attachment.artifactId) {
            fileAttachmentTitleById.set(
              attachment.artifactId,
              attachment.fileName,
            );
          }
        }
      }
      for (const artifactID of referencedArtifactIDs) {
        if (artifactsById[artifactID]) continue;
        const attachmentTitle = fileAttachmentTitleById.get(artifactID);
        upsertArtifact(
          attachmentTitle
            ? {
                id: artifactID,
                type: "file",
                title: attachmentTitle,
                content: "",
                metadata: { placeholder: true },
              }
            : makePlaceholderArtifact(artifactID, "Artifact"),
        );
        void fetchJson(`/api/artifacts/${encodeURIComponent(artifactID)}`, {
          method: "GET",
        })
          .then((artifactRes) => {
            if (artifactRes.ok === false) return;
            const rawArtifact =
              (artifactRes.data as any)?.artifact ??
              (artifactRes.data as any)?.data?.artifact ??
              artifactRes.data;
            if (!rawArtifact || typeof rawArtifact !== "object") return;
            if (typeof (rawArtifact as any).id !== "string") return;
            const artifact = rawArtifact as Artifact;
            upsertArtifact({
              ...artifact,
              metadata: {
                ...(artifact.metadata || {}),
                placeholder: false,
              },
            });
          })
          .catch(() => {});
      }

      // Rehydrate persisted run plans (plan §4.6). Rows are UI-only, while
      // messages carry run_id; fetching all distinct run plans restores the
      // complete audit trail after reload. last_run_id remains a compatibility
      // fallback for older backends whose message DTOs did not expose run_id.
      if (getCapabilityUxFlags(settings).preflight) {
        const hydrationTargets = new Map<
          string,
          { afterStableId: string | null; allowEmptyTranscript: boolean }
        >();
        for (const message of visibleMsgs) {
          const runId = String(message.run_id || "").trim();
          if (!runId) continue;
          const current = hydrationTargets.get(runId) || {
            afterStableId: null,
            allowEmptyTranscript: false,
          };
          if (message.role === Role.User && message._uiStableId) {
            current.afterStableId = message._uiStableId;
          }
          hydrationTargets.set(runId, current);
        }
        const lastRunId = String(
          (data as any)?.thread?.last_run_id ?? "",
        ).trim();
        if (lastRunId && !hydrationTargets.has(lastRunId)) {
          const lastUser = [...visibleMsgs]
            .reverse()
            .find((message) => message.role === Role.User);
          hydrationTargets.set(lastRunId, {
            afterStableId: lastUser?._uiStableId || null,
            allowEmptyTranscript: visibleMsgs.length === 0,
          });
        }

        const orderedTargets = [...hydrationTargets.entries()];
        if (orderedTargets.length > 0) {
          void Promise.all(
            orderedTargets.map(async ([runId, placement]) => {
              try {
                return {
                  runId,
                  placement,
                  record: await getRunCapabilityPlan(runId),
                };
              } catch {
                // Older/legacy runs legitimately have no persisted plan.
                return null;
              }
            }),
          ).then((loaded) => {
            let previousTarget: ThreadTarget | null = null;
            for (const entry of loaded) {
              if (!entry) continue;
              const { record, placement } = entry;
              const planId = String(record.planId || "").trim();
              if (!planId) continue;
              const target: ThreadTarget = {
                connectionId: String(record.target?.connectionId || "").trim(),
                model: String(record.target?.model || "").trim(),
              };
              const targetChanged = Boolean(
                previousTarget &&
                (previousTarget.connectionId !== target.connectionId ||
                  previousTarget.model !== target.model),
              );
              if (target.connectionId && target.model) {
                previousTarget = target;
              }
              const selectionByIssue = new Map(
                (record.choiceSelections || []).map((selection) => [
                  selection.issueId,
                  selection,
                ]),
              );
              const items: UICapabilityPlanItem[] = [
                ...(record.choices || [])
                  .filter((choice) => selectionByIssue.has(choice.issueId))
                  .map((choice) => {
                    const selection = selectionByIssue.get(choice.issueId);
                    const option = choice.options?.find(
                      (candidate) => candidate.id === selection?.optionId,
                    );
                    return {
                      kind: "choice" as const,
                      code: choice.code,
                      text: option
                        ? `${choice.text} → ${option.label}`
                        : choice.text,
                      attachmentId: choice.attachmentId,
                    };
                  }),
                ...(record.notices || []).map((notice) => ({
                  kind: "notice" as const,
                  code: notice.code,
                  text: notice.text,
                  attachmentId: notice.attachmentId,
                })),
              ];
              // Clean, same-target plans stay quiet after reload, matching
              // the live thread-differential rule.
              if (items.length === 0 && !targetChanged) continue;
              const cfgForTarget = (settings.providerConfigs || []).find(
                (config) => config.id === target.connectionId,
              );
              const targetLabel = cfgForTarget
                ? targetIdentityLabel({
                    ...cfgForTarget,
                    model: target.model || cfgForTarget.model,
                  })
                : undefined;
              const runId = record.runId || entry.runId;
              const stableId = createUiStableId();
              const row = {
                role: Role.System,
                content: "",
                uiKind: "capability_plan",
                capabilityPlan: {
                  planId,
                  runId,
                  targetLabel,
                  targetChanged,
                  items,
                },
                _uiStableId: stableId,
              } as UIMessage;
              setMessagesForThread(threadId, (prev) => {
                // Dedupe against live rows and concurrent hydrations.
                const existing = prev.find(
                  (message) => message.capabilityPlan?.planId === planId,
                );
                if (existing) {
                  if (existing._uiStableId) {
                    planRowsRef.current[planId] = {
                      threadId,
                      stableId: existing._uiStableId,
                      runId,
                    };
                  }
                  return prev;
                }
                const anchorIndex = placement.afterStableId
                  ? prev.findIndex(
                      (message) =>
                        message._uiStableId === placement.afterStableId,
                    )
                  : -1;
                if (anchorIndex >= 0) {
                  planRowsRef.current[planId] = {
                    threadId,
                    stableId,
                    runId,
                  };
                  return [
                    ...prev.slice(0, anchorIndex + 1),
                    row,
                    ...prev.slice(anchorIndex + 1),
                  ];
                }
                if (placement.allowEmptyTranscript && prev.length === 0) {
                  planRowsRef.current[planId] = {
                    threadId,
                    stableId,
                    runId,
                  };
                  return [row];
                }
                // The captured persisted anchor disappeared; never append an
                // old plan below a newer send.
                return prev;
              });
            }
            const latestTarget = [...loaded]
              .reverse()
              .find((entry) =>
                Boolean(
                  entry?.record.target?.connectionId &&
                  entry.record.target?.model,
                ),
              )?.record.target;
            if (latestTarget) {
              lastRunTargetByThreadRef.current[threadId] = {
                connectionId: latestTarget.connectionId,
                model: latestTarget.model,
              };
            }
          });
        }
      }
    },
    [
      artifactsById,
      createUiStableId,
      setMessagesForThread,
      settings,
      upsertArtifact,
    ],
  );

  const createThread = useCallback(
    async (opts?: { title?: string }) => {
      const body: any = {};
      if (opts?.title && opts.title.trim()) body.title = opts.title.trim();
      if (typeof projectId === "string" && projectId.trim())
        body.project_id = projectId.trim();
      let res: Awaited<ReturnType<typeof fetchJson>>;
      try {
        res = await fetchJson("/api/threads", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        });
      } catch (e: any) {
        toast.error(normalizeThrownErrorMessage(e, "Failed to create thread."));
        return null;
      }
      if (res.ok === false) {
        const msg = extractErrorMessage(res.data, res.status);
        const hint = extractHint(res.data);
        toast.error(hint ? `${msg}\n\n${hint}` : msg);
        return null;
      }
      const created =
        normalizeThread(
          (res.data as any)?.thread ??
            (res.data as any)?.data?.thread ??
            res.data,
        ) || normalizeThread(res.data);
      if (!created) return null;
      setThreads((prev) => [
        created,
        ...prev.filter((t) => t.id !== created.id),
      ]);
      threadsRef.current = [
        created,
        ...threadsRef.current.filter((t) => t.id !== created.id),
      ];
      setActiveThreadId(created.id);
      try {
        localStorage.setItem(ACTIVE_THREAD_STORAGE_KEY, created.id);
      } catch {
        // ignore
      }
      // Initialize empty local state immediately; fetch detail in background.
      setMessagesForThread(created.id, []);
      void loadThread(created.id);
      return created;
    },
    [loadThread, projectId, setMessagesForThread],
  );

  const deleteThread = useCallback(
    async (threadId: string) => {
      await fetchJson(`/api/threads/${encodeURIComponent(threadId)}`, {
        method: "DELETE",
      });
      setThreads((prev) => prev.filter((t) => t.id !== threadId));
      setMessagesByThreadId((prev) => {
        const next = { ...prev };
        delete next[threadId];
        messagesRef.current = next;
        return next;
      });
      if (activeThreadId === threadId) {
        const next = threads.find((t) => t.id !== threadId)?.id || null;
        setActiveThreadId(next);
        if (next) {
          try {
            localStorage.setItem(ACTIVE_THREAD_STORAGE_KEY, next);
          } catch {
            /* ignore */
          }
          void loadThread(next);
        } else {
          try {
            localStorage.removeItem(ACTIVE_THREAD_STORAGE_KEY);
          } catch {
            /* ignore */
          }
        }
      }
    },
    [activeThreadId, loadThread, threads],
  );

  const exportThread = useCallback(
    async (threadId: string): Promise<{ fileName: string } | null> => {
      const id = String(threadId || "").trim();
      if (!id) return null;
      let res: Response;
      try {
        res = await fetch(`/api/threads/${encodeURIComponent(id)}/export`, {
          method: "GET",
          headers: {
            Accept: "application/vnd.ai-analyst.thread+json, application/json",
          },
        });
      } catch (e: any) {
        toast.error(normalizeThrownErrorMessage(e, "Failed to export thread."));
        return null;
      }
      if (!res.ok) {
        const data = unwrapSettingsStylePayload(await readJsonSafe(res));
        const msg = extractErrorMessage(data, res.status);
        const hint = extractHint(data);
        toast.error(hint ? `${msg}\n\n${hint}` : msg);
        return null;
      }
      const blob = await res.blob();
      const fileName = parseDownloadFileName(
        res.headers.get("Content-Disposition"),
        "thread-export.ai-thread.json",
      );
      triggerDownload(blob, fileName);
      return { fileName };
    },
    [],
  );

  const importThread = useCallback(
    async (file: File): Promise<ThreadSummary | null> => {
      if (!(file instanceof File)) return null;
      let res: Response;
      try {
        res = await fetch("/api/threads/import", {
          method: "POST",
          headers: {
            Accept: "application/json",
            "Content-Type": file.type || "application/json",
          },
          body: file,
        });
      } catch (e: any) {
        toast.error(normalizeThrownErrorMessage(e, "Failed to import thread."));
        return null;
      }

      const data = unwrapSettingsStylePayload(await readJsonSafe(res));
      if (!res.ok) {
        const msg = extractErrorMessage(data, res.status);
        const hint = extractHint(data);
        toast.error(hint ? `${msg}\n\n${hint}` : msg);
        return null;
      }

      const imported =
        normalizeThread(
          (data as any)?.thread ?? (data as any)?.data?.thread ?? data,
        ) || normalizeThread(data);
      if (!imported) {
        toast.error("Backend did not return the imported thread.");
        return null;
      }

      const incoming = await refreshThreads();
      setThreads((prev) => {
        const next = prev.some((thread) => thread.id === imported.id)
          ? prev.map((thread) =>
              thread.id === imported.id ? imported : thread,
            )
          : [imported, ...prev];
        threadsRef.current = next;
        return next;
      });
      if (!incoming.some((thread) => thread.id === imported.id)) {
        threadsRef.current = [
          imported,
          ...threadsRef.current.filter((thread) => thread.id !== imported.id),
        ];
      }
      setActiveThreadId(imported.id);
      try {
        localStorage.setItem(ACTIVE_THREAD_STORAGE_KEY, imported.id);
      } catch {
        // ignore
      }
      await loadThread(imported.id);
      return imported;
    },
    [loadThread, refreshThreads],
  );

  const ensureActiveThread = useCallback(async (): Promise<string | null> => {
    if (activeThreadId) return activeThreadId;
    const list = await refreshThreads();
    const stored = (() => {
      try {
        return localStorage.getItem(ACTIVE_THREAD_STORAGE_KEY);
      } catch {
        return null;
      }
    })();
    const first = list[0]?.id || null;
    const chosen = stored && list.some((t) => t.id === stored) ? stored : first;
    if (chosen) {
      setActiveThreadId(chosen);
      try {
        localStorage.setItem(ACTIVE_THREAD_STORAGE_KEY, chosen);
      } catch {
        /* ignore */
      }
      return chosen;
    }
    const created = await createThread();
    return created?.id || null;
  }, [activeThreadId, createThread, refreshThreads]);

  const selectThread = useCallback(
    async (threadId: string) => {
      const id = String(threadId || "").trim();
      if (!id) return;
      setActiveThreadId(id);
      try {
        localStorage.setItem(ACTIVE_THREAD_STORAGE_KEY, id);
      } catch {
        /* ignore */
      }
      if (!messagesRef.current[id]) {
        void loadThread(id);
      }
    },
    [loadThread],
  );

  const ensureArtifactLoaded = useCallback(
    async (artifactId: string): Promise<Artifact | null> => {
      const id = String(artifactId || "").trim();
      if (!id) return null;
      const existing = artifactsById[id];
      const existingPartial =
        existing?.metadata?.partial === true ||
        existing?.metadata?.preview_only === true;
      const targetId =
        String(existing?.metadata?.full_artifact_id || id).trim() || id;
      if (
        existing &&
        existing.metadata?.placeholder !== true &&
        !existingPartial
      )
        return existing;
      let res: Awaited<ReturnType<typeof fetchJson>>;
      try {
        res = await fetchJson(
          `/api/artifacts/${encodeURIComponent(targetId)}`,
          { method: "GET" },
        );
      } catch {
        return existing || null;
      }
      if (!res.ok) return existing || null;
      const raw =
        (res.data as any)?.artifact ??
        (res.data as any)?.data?.artifact ??
        res.data;
      if (!raw || typeof raw !== "object") return existing || null;
      if (typeof (raw as any).id !== "string") return existing || null;
      const loadedArtifact = normalizeArtifactForClient(raw as Artifact);
      const loadedId = String(loadedArtifact.id || "").trim();
      const shouldAliasToRequestedId = Boolean(
        existing && loadedId && loadedId !== id,
      );
      // A fetched async-query record can itself still be the running
      // placeholder (the server upgrades it in place once get_query_results
      // lands). Forcing placeholder:false here would freeze the inspector on
      // an empty "completed" artifact and stop completion hydration from
      // retrying, so only clear the flag when the server record is real.
      const loadedIsPlaceholder = loadedArtifact.metadata?.placeholder === true;
      const mergedMetadata: Record<string, unknown> = {
        ...(existing?.metadata || {}),
        ...(loadedArtifact.metadata || {}),
        ...(loadedId && loadedId !== id ? { full_artifact_id: loadedId } : {}),
        placeholder: loadedIsPlaceholder,
        partial: false,
        preview_only: false,
      };
      // Once the real results artifact is loaded, the running-query flag from
      // the placeholder must not leak through the merge — the inspector treats
      // async_query===true as "still polling" and would pin the panel on the
      // running view even with rows present.
      const loadedIsCompleteResult =
        !loadedIsPlaceholder &&
        loadedArtifact.metadata?.async_query !== true &&
        loadedArtifact.metadata?.ok !== false;
      if (loadedIsCompleteResult) {
        delete mergedMetadata.async_query;
        if (isPlainObject(mergedMetadata.query_metadata)) {
          const queryMetadata = { ...mergedMetadata.query_metadata };
          const status = String(queryMetadata.status || "")
            .trim()
            .toLowerCase();
          if (
            status === "" ||
            status === "pending" ||
            status === "queued" ||
            isActiveQueryStatus(status)
          ) {
            queryMetadata.status = "completed";
          }
          mergedMetadata.query_metadata = queryMetadata;
        }
      }
      const merged: Artifact = {
        ...loadedArtifact,
        id: shouldAliasToRequestedId ? id : loadedArtifact.id,
        metadata: mergedMetadata,
      };
      upsertArtifact(merged);
      if (shouldAliasToRequestedId) {
        upsertArtifact({
          ...loadedArtifact,
          metadata: mergedMetadata,
        });
      }
      return merged;
    },
    [artifactsById, upsertArtifact],
  );

  const pruneTrailingEmptyAssistant = useCallback(
    (threadId: string) => {
      setMessagesForThread(threadId, (prev) => {
        const last = prev[prev.length - 1];
        if (!last || last.role !== Role.Assistant) return prev;
        if (sanitizeAssistantMessageForUI(last)) return prev;
        return prev.slice(0, -1);
      });
    },
    [setMessagesForThread],
  );

  const rollbackOptimisticUserTurn = useCallback(
    (threadId: string, uiStableId?: string) => {
      pruneTrailingEmptyAssistant(threadId);
      const stableId = String(uiStableId || "").trim();
      if (!stableId) return;
      setMessagesForThread(threadId, (prev) => {
        const idx = prev.findIndex((msg) => msg._uiStableId === stableId);
        if (idx === -1) return prev;
        return prev.slice(0, idx).concat(prev.slice(idx + 1));
      });
    },
    [pruneTrailingEmptyAssistant, setMessagesForThread],
  );

  const appendAssistantPlaceholder = useCallback(
    (threadId: string) => {
      setMessagesForThread(threadId, (prev) => {
        const last = prev[prev.length - 1];
        if (
          last &&
          last.role === Role.Assistant &&
          !last.content &&
          !(last as any).tool_calls
        )
          return prev;
        return prev.concat([
          { role: Role.Assistant, content: "", _uiStreaming: true },
        ]);
      });
    },
    [setMessagesForThread],
  );

  const applyToolResultMessageSideEffects = useCallback(
    (message: UIMessage) => {
      if (message.role !== Role.Tool) return;
      const env = tryParseToolResultEnvelope(message.content || null);
      if (!env) return;
      const toolName = (env.tool_name || message.name || "").trim();
      const suppressUi = shouldSuppressToolResultUI(
        toolName,
        env.result_format,
        env.result,
        env,
      );
      if (suppressUi) return;

      // Create/overwrite the referenced artifact from the envelope when possible.
      const artifact = buildArtifactFromEnvelope(
        env,
        toolName,
        message.content,
      );
      upsertArtifact(artifact);
      for (const attachmentArtifactId of extractToolResultAttachmentArtifactIds(
        env,
        message,
      )) {
        void ensureArtifactLoaded(attachmentArtifactId);
      }
    },
    [ensureArtifactLoaded, upsertArtifact],
  );

  const finalizeLatestAssistantStepIfIdle = useCallback(
    (threadId: string) => {
      setMessagesForThread(threadId, (prev) => {
        const last = prev[prev.length - 1];
        if (!last || last.role !== Role.Assistant) return prev;

        const visibleToolCalls = filterVisibleToolCalls(
          Array.isArray((last as any).tool_calls)
            ? ((last as any).tool_calls as ToolCall[])
            : undefined,
        );
        if (visibleToolCalls.length > 0) return prev;

        const hasThinkingText =
          typeof (last as any).thinking === "string" &&
          (last as any).thinking.trim().length > 0;
        const needsFinalize =
          last._uiStreaming === true ||
          (last as any).compactionInProgress === true ||
          (hasThinkingText && (last as any).thinkingComplete !== true);
        if (!needsFinalize) return prev;

        const updated: UIMessage = {
          ...last,
          _uiStreaming: false,
        };
        if (
          typeof (last as any).thinking === "string" &&
          (last as any).thinking.trim().length > 0
        ) {
          updated.thinkingComplete = true;
        }
        if ((last as any).compactionInProgress === true) {
          updated.compactionInProgress = false;
          updated.compactionComplete = true;
        }

        const sanitized = sanitizeAssistantMessageForUI(updated);
        if (!sanitized) return prev.slice(0, -1);
        return prev.slice(0, -1).concat([sanitized]);
      });
    },
    [setMessagesForThread],
  );

  const removeToolCallFromAssistantMessages = useCallback(
    (threadId: string, toolCallId: string) => {
      const id = String(toolCallId || "").trim();
      if (!id) return;
      setMessagesForThread(threadId, (prev) =>
        prev.flatMap((msg) => {
          if (msg.role !== Role.Assistant) return [msg];

          const toolCalls = Array.isArray((msg as any).tool_calls)
            ? ((msg as any).tool_calls as ToolCall[])
            : null;
          if (!toolCalls || toolCalls.length === 0) return [msg];

          const remainingCalls = toolCalls.filter(
            (toolCall) => getToolCallId(toolCall) !== id,
          );
          if (remainingCalls.length === toolCalls.length) return [msg];

          const updated: UIMessage = { ...msg };
          if (remainingCalls.length > 0) {
            (updated as any).tool_calls = remainingCalls;
          } else {
            delete (updated as any).tool_calls;
          }

          const sanitized = sanitizeAssistantMessageForUI(updated);
          return sanitized ? [sanitized] : [];
        }),
      );
    },
    [setMessagesForThread],
  );

  const getToolCallUiStartedAt = useCallback(
    (threadId: string, toolCallId: string): number | undefined => {
      const id = String(toolCallId || "").trim();
      if (!id) return undefined;
      const threadMessages = messagesRef.current[threadId] || [];
      for (let i = threadMessages.length - 1; i >= 0; i -= 1) {
        const msg = threadMessages[i];
        if (msg.role !== Role.Assistant) continue;
        const toolCalls = Array.isArray((msg as any).tool_calls)
          ? ((msg as any).tool_calls as ToolCall[])
          : null;
        if (!toolCalls) continue;
        const match = toolCalls.find(
          (toolCall) => getToolCallId(toolCall) === id,
        );
        if (!match) continue;
        const startedAt = (match as any)._uiStartedAt;
        if (
          typeof startedAt === "number" &&
          Number.isFinite(startedAt) &&
          startedAt > 0
        ) {
          return startedAt;
        }
      }
      return undefined;
    },
    [],
  );

  const cancelThreadRun = useCallback(
    (threadId: string) => {
      const id = String(threadId || "").trim();
      if (!id) return;
      const run = runningByThreadRef.current[id];
      if (!run) return;

      try {
        abortByRunIdRef.current[run.id]?.abort();
      } catch {
        // ignore
      }
      try {
        waitAbortByRunIdRef.current[run.id]?.abort();
        delete waitAbortByRunIdRef.current[run.id];
      } catch {
        // ignore
      }

      // Best-effort cancel on the backend.
      void fetchJson(`/api/runs/${encodeURIComponent(run.id)}/cancel`, {
        method: "POST",
      });

      pruneTrailingEmptyAssistant(id);
    },
    [pruneTrailingEmptyAssistant],
  );

  const recoverActiveRun = useCallback(
    async (threadId: string, runId: string): Promise<RunInfo | null> => {
      const activeRunId = String(runId || "").trim();
      if (!activeRunId) return null;
      let res: Awaited<ReturnType<typeof fetchJson>>;
      try {
        res = await fetchJson(`/api/runs/${encodeURIComponent(activeRunId)}`, {
          method: "GET",
        });
      } catch {
        return null;
      }
      if (res.ok === false) return null;
      const recovered = normalizeRunInfo(res.data, threadId, {
        recoveredFromConflict: true,
      });
      if (!recovered) return null;
      if (isTerminalRunStatus(recovered.status)) return null;
      return recovered;
    },
    [],
  );

  // ---- Capability plan transcript rows (plan §4.6) ----

  const appendCapabilityPlanRow = useCallback(
    (
      threadId: string,
      summary: UICapabilityPlanSummary,
      options?: { afterStableId?: string },
    ): string => {
      const stableId = createUiStableId();
      const row = {
        role: Role.System,
        content: "",
        uiKind: "capability_plan",
        capabilityPlan: summary,
        _uiStableId: stableId,
      } as UIMessage;
      setMessagesForThread(threadId, (prev) => {
        const afterStableId = String(options?.afterStableId || "").trim();
        if (afterStableId) {
          const anchorIndex = prev.findIndex(
            (message) => message._uiStableId === afterStableId,
          );
          if (anchorIndex >= 0) {
            return [
              ...prev.slice(0, anchorIndex + 1),
              row,
              ...prev.slice(anchorIndex + 1),
            ];
          }
        }
        return prev.concat([row]);
      });
      if (summary.planId) {
        planRowsRef.current[summary.planId] = {
          threadId,
          stableId,
          runId: summary.runId,
        };
      }
      return stableId;
    },
    [createUiStableId, setMessagesForThread],
  );

  const attachRunIdToPlanRow = useCallback(
    (planId: string, runId: string) => {
      const registered = planRowsRef.current[planId];
      if (!registered) return;
      registered.runId = runId;
      setMessagesForThread(registered.threadId, (prev) =>
        prev.map((msg) =>
          msg._uiStableId === registered.stableId && msg.capabilityPlan
            ? { ...msg, capabilityPlan: { ...msg.capabilityPlan, runId } }
            : msg,
        ),
      );
    },
    [setMessagesForThread],
  );

  const markAttachmentReducedFidelity = useCallback(
    (threadId: string, attachmentId: string) => {
      setMessagesForThread(threadId, (prev) =>
        prev.map((msg) => {
          if (!Array.isArray(msg.fileAttachments)) return msg;
          if (!msg.fileAttachments.some((f) => f.artifactId === attachmentId))
            return msg;
          return {
            ...msg,
            fileAttachments: msg.fileAttachments.map((f) =>
              f.artifactId === attachmentId
                ? { ...f, sendFidelity: "reduced" as const }
                : f,
            ),
          };
        }),
      );
    },
    [setMessagesForThread],
  );

  const appendPlanUpdateItem = useCallback(
    (
      threadId: string,
      planId: string,
      runId: string,
      item: UICapabilityPlanItem,
    ) => {
      const registered = planId ? planRowsRef.current[planId] : null;
      if (!registered) {
        appendCapabilityPlanRow(threadId, {
          planId: planId || undefined,
          runId,
          items: [item],
        });
        return;
      }
      setMessagesForThread(threadId, (prev) =>
        prev.map((msg) =>
          msg._uiStableId === registered.stableId && msg.capabilityPlan
            ? {
                ...msg,
                capabilityPlan: {
                  ...msg.capabilityPlan,
                  runId: msg.capabilityPlan.runId || runId,
                  items: [...msg.capabilityPlan.items, item],
                },
              }
            : msg,
        ),
      );
    },
    [appendCapabilityPlanRow, setMessagesForThread],
  );

  const streamRunEvents = useCallback(
    async (run: RunInfo) => {
      const threadId = run.threadId;
      const runId = run.id;
      const activeToolProgressKeys = new Set<string>();
      const seenToolMessageKeys = new Set<string>();
      let pendingTextDelta = "";
      let pendingThinkingDelta = "";
      let flushScheduled = false;

      // Seed with already-rendered tool messages from this run so SSE replays do not duplicate cards.
      const existingMessages = messagesRef.current[threadId] || [];
      for (const existing of existingMessages) {
        if (existing.role !== Role.Tool) continue;
        if (String((existing as any)._uiRunId || "").trim() !== runId) continue;
        const existingEnv = tryParseToolResultEnvelope(
          existing.content || null,
        );
        const existingKey = makeToolMessageDedupKey(
          runId,
          existing,
          existingEnv,
        );
        if (existingKey) seenToolMessageKeys.add(existingKey);
      }

      const getRunSegmenter = (): StreamingMarkdownSegmenter => {
        let segmenter = streamSegmentersByRunIdRef.current[runId];
        if (!segmenter) {
          segmenter = createStreamingMarkdownSegmenter();
          streamSegmentersByRunIdRef.current[runId] = segmenter;
        }
        return segmenter;
      };

      const flushPendingAssistantDeltas = () => {
        if (!pendingTextDelta && !pendingThinkingDelta) return;
        const textDelta = pendingTextDelta;
        const thinkingDelta = pendingThinkingDelta;
        pendingTextDelta = "";
        pendingThinkingDelta = "";
        const snapshot = textDelta ? getRunSegmenter().push(textDelta) : null;
        setMessagesForThread(threadId, (prev) => {
          const last = prev[prev.length - 1];
          if (!last || last.role !== Role.Assistant) return prev;
          const nextMessage: UIMessage = { ...last };
          if (snapshot) {
            nextMessage.content = snapshot.fullText;
            nextMessage.streamStablePrefix = snapshot.stablePrefix;
            nextMessage.streamUnstableSuffix = snapshot.unstableSuffix;
            nextMessage.streamInCodeBlock = snapshot.inCodeBlock;
            nextMessage.streamParserDegraded = snapshot.degraded;
          }
          if (thinkingDelta) {
            nextMessage.thinking =
              ((last as any).thinking || "") + thinkingDelta;
          }
          return prev.slice(0, -1).concat([nextMessage]);
        });
      };

      const scheduleAssistantDeltaFlush = () => {
        if (flushScheduled) return;
        flushScheduled = true;
        requestAnimationFrame(() => {
          flushScheduled = false;
          flushPendingAssistantDeltas();
        });
      };

      const upsertProviderToolProgress = (rawProgress: Record<string, any>) => {
        const callId = String(
          rawProgress.tool_call_id ||
            rawProgress.id ||
            rawProgress.toolCallId ||
            "",
        ).trim();
        const toolName = String(
          rawProgress.tool_name ||
            rawProgress.name ||
            rawProgress.toolName ||
            "",
        ).trim();
        if (!callId || !toolName) return;

        const query = firstCompactString(
          rawProgress,
          "query",
          "search_query",
          "searchQuery",
        );
        const rawArgs = isPlainObject(rawProgress.args)
          ? { ...(rawProgress.args as Record<string, unknown>) }
          : {};
        if (query && typeof rawArgs.query !== "string") rawArgs.query = query;

        let argsText = "{}";
        try {
          argsText = JSON.stringify(rawArgs);
        } catch {
          argsText = "{}";
        }

        const provider = String(rawProgress.provider || "provider-hosted");
        const progress = {
          ...rawProgress,
          tool_call_id: callId,
          tool_name: toolName,
          provider,
          integration: rawProgress.integration || rawProgress.adapter_family,
          run_id: runId,
          thread_id: threadId,
        } as ToolProgressEvent;

        const progressKey = makeToolProgressKey(runId, callId);
        if (progressKey) {
          activeToolProgressKeys.add(progressKey);
          setToolProgressByKey((prev) => ({
            ...prev,
            [progressKey]: progress,
          }));
        }

        const existingStartedAt = getToolCallUiStartedAt(threadId, callId);
        const startedAt =
          typeof existingStartedAt === "number" &&
          Number.isFinite(existingStartedAt)
            ? existingStartedAt
            : Date.now();
        const incomingToolCall = {
          id: callId,
          name: toolName,
          args: rawArgs,
          argsText,
          provider_executed: true,
          _uiStartedAt: startedAt,
          _uiRunId: runId,
        } as ToolCall;

        const mergeToolCalls = (existingCalls: ToolCall[]): ToolCall[] => {
          const index = existingCalls.findIndex(
            (toolCall) => getToolCallId(toolCall) === callId,
          );
          if (index < 0) return existingCalls.concat([incomingToolCall]);
          const existing = existingCalls[index] as any;
          const mergedArgs = {
            ...(isPlainObject(existing.args) ? existing.args : {}),
            ...rawArgs,
          };
          let mergedArgsText = argsText;
          try {
            mergedArgsText = JSON.stringify(mergedArgs);
          } catch {
            mergedArgsText = existing.argsText || argsText;
          }
          const merged = {
            ...existing,
            ...incomingToolCall,
            args: mergedArgs,
            argsText: mergedArgsText,
            _uiStartedAt:
              typeof existing._uiStartedAt === "number" &&
              Number.isFinite(existing._uiStartedAt)
                ? existing._uiStartedAt
                : startedAt,
          } as ToolCall;
          return existingCalls
            .slice(0, index)
            .concat([merged], existingCalls.slice(index + 1));
        };

        setMessagesForThread(threadId, (prev) => {
          const last = prev[prev.length - 1];
          if (last && last.role === Role.Assistant) {
            const existingCalls = Array.isArray((last as any).tool_calls)
              ? ((last as any).tool_calls as ToolCall[])
              : [];
            const merged = sanitizeAssistantMessageForUI({
              ...last,
              tool_calls: mergeToolCalls(existingCalls),
              _uiStreaming: true,
            });
            if (!merged) return prev;
            return prev.slice(0, -1).concat([merged]);
          }

          const nextAssistant = sanitizeAssistantMessageForUI({
            role: Role.Assistant,
            content: null,
            tool_calls: [incomingToolCall],
            _uiStreaming: true,
          });
          if (!nextAssistant) return prev;
          return prev.concat([nextAssistant]);
        });
      };

      // Deduplicate: if we're already streaming this run, don't start a second loop.
      if (abortByRunIdRef.current[runId]) return;

      // If a newer run has already replaced this one for the thread, ignore it.
      const current = runningByThreadRef.current[threadId];
      if (current && current.id !== runId) return;

      const streamStartedAt = Date.now();
      const updateRunStreamStatus = (
        updater: (current: RunStreamStatus) => RunStreamStatus,
      ) => {
        setRunStreamStatusForThread(threadId, (prev) => {
          const base: RunStreamStatus =
            prev && prev.runId === runId
              ? prev
              : {
                  runId,
                  threadId,
                  startedAt: streamStartedAt,
                  heartbeatCount: 0,
                  meaningfulEventCount: 0,
                  phase: run.status === "queued" ? "queued" : "connecting",
                  label: "Connecting to run stream",
                };
          return updater(base);
        });
      };
      const markMeaningfulStreamEvent = (name: string, payload: any) => {
        const now = Date.now();
        updateRunStreamStatus((current) =>
          markRunStreamMeaningfulEvent(current, name, payload, now),
        );
      };
      const markPreviewOnlyArtifactDiagnostic = (
        diagnostic: RunStreamDiagnostic | null,
      ) => {
        if (!diagnostic) return;
        updateRunStreamStatus((current) => ({
          ...current,
          phase: "preview_artifact",
          label: "Preview artifact ready",
          message: diagnostic.message,
          previewOnlyArtifactId: diagnostic.artifactId,
          fullArtifactId: diagnostic.fullArtifactId,
          diagnostic,
        }));
      };
      const applyCompactionStatsToLatestAssistant = (
        stats: CompactionStats | null,
        options?: {
          createIfMissing?: boolean;
          inProgress?: boolean;
          complete?: boolean;
          resetSummary?: boolean;
        },
      ) => {
        const hasStats = hasMeaningfulCompactionStats(stats);
        if (!hasStats && !options?.createIfMissing) return;
        setMessagesForThread(threadId, (prev) => {
          const last = prev[prev.length - 1];
          const canUpdateExisting =
            last?.role === Role.Assistant &&
            ((last as any).compactionInProgress === true ||
              (last as any).compactionComplete === true ||
              options?.createIfMissing === true);
          if (!canUpdateExisting) {
            if (!options?.createIfMissing) return prev;
            const nextAssistant: UIMessage = {
              role: Role.Assistant,
              content: "",
              _uiStreaming: options.inProgress !== false,
              compactionInProgress: options.inProgress ?? false,
              compactionComplete: options.complete ?? false,
              compactionSummary: "",
              compactionStats: mergeCompactionStats(undefined, stats),
            };
            return prev.concat([nextAssistant]);
          }

          const nextMessage: UIMessage = { ...last };
          if (options?.resetSummary) {
            nextMessage.compactionSummary = "";
          }
          if (options?.inProgress !== undefined) {
            nextMessage.compactionInProgress = options.inProgress;
          }
          if (options?.complete !== undefined) {
            nextMessage.compactionComplete = options.complete;
          }
          if (hasStats) {
            nextMessage.compactionStats = mergeCompactionStats(
              nextMessage.compactionStats,
              stats,
            );
          }
          return prev.slice(0, -1).concat([nextMessage]);
        });
      };
      const hydrateRunCompactionStats = async () => {
        let res: Awaited<ReturnType<typeof fetchJson>>;
        try {
          res = await fetchJson(`/api/runs/${encodeURIComponent(runId)}`, {
            method: "GET",
          });
        } catch {
          return;
        }
        if (res.ok === false) return;
        const stats = normalizeCompactionStatsPayload(res.data);
        if (!stats) return;
        applyCompactionStatsToLatestAssistant(stats, {
          createIfMissing: stats.compacted === true,
          inProgress: false,
          complete: stats.compacted === true ? true : undefined,
        });
      };
      setRunStreamStatusForThread(threadId, {
        runId,
        threadId,
        startedAt: streamStartedAt,
        heartbeatCount: 0,
        meaningfulEventCount: 0,
        phase: run.status === "queued" ? "queued" : "connecting",
        label: "Connecting to run stream",
      });

      const controller = new AbortController();
      abortByRunIdRef.current[runId] = controller;
      const currentRunForDisplay = runningByThreadRef.current[threadId];
      if (
        currentRunForDisplay &&
        currentRunForDisplay.id === runId &&
        currentRunForDisplay.status === "queued"
      ) {
        setRunForThread(threadId, {
          ...currentRunForDisplay,
          status: "running",
        });
      }
      let streamEndedWaitingUser = false;

      try {
        const res = await fetch(
          `/api/runs/${encodeURIComponent(runId)}/events`,
          {
            method: "GET",
            headers: {
              Accept: "text/event-stream",
              "Cache-Control": "no-cache",
            },
            signal: controller.signal,
          },
        );

        if (!res.ok) {
          const data = await readJsonSafe(res);
          const msg = extractErrorMessage(data, res.status);
          // Avoid appending noise if this run was superseded.
          if (runningByThreadRef.current[threadId]?.id === runId) {
            appendAssistantError(threadId, msg);
          }
          return;
        }

        if (!res.body) {
          if (runningByThreadRef.current[threadId]?.id === runId) {
            appendAssistantError(
              threadId,
              "Empty response body (SSE not supported?)",
            );
          }
          return;
        }

        updateRunStreamStatus((current) =>
          markRunStreamOpened(current, Date.now()),
        );

        for await (const sseMsg of parseSSE(res.body, {
          signal: controller.signal,
          includeComments: true,
        })) {
          if (controller.signal.aborted) break;
          if (runningByThreadRef.current[threadId]?.id !== runId) break;
          if (typeof sseMsg.comment === "string") {
            updateRunStreamStatus((current) =>
              markRunStreamHeartbeat(current, Date.now()),
            );
            continue;
          }
          const parsed = parseRunSseEvent(sseMsg);
          if (!parsed) continue;

          const name = parsed.name;
          const payload = parsed.payload;
          markMeaningfulStreamEvent(name, payload);

          if (name !== "text_delta" && name !== "thinking_delta") {
            flushPendingAssistantDeltas();
          }

          if (name === "done" || name === "run_end" || name === "end") {
            break;
          }

          if (name === "run_waiting_external") {
            const currentRun = runningByThreadRef.current[threadId];
            if (currentRun?.id === runId) {
              setRunForThread(threadId, {
                ...currentRun,
                status: "waiting_external",
              });
            }
            finalizeLatestAssistantStepIfIdle(threadId);
            continue;
          }

          if (name === "run_resuming") {
            const currentRun = runningByThreadRef.current[threadId];
            if (currentRun?.id === runId) {
              setRunForThread(threadId, {
                ...currentRun,
                status: "resuming",
              });
            }
            continue;
          }

          if (name === "run_waiting_user") {
            streamEndedWaitingUser = true;
            const currentRun = runningByThreadRef.current[threadId];
            if (currentRun?.id === runId) {
              setRunForThread(threadId, {
                ...currentRun,
                status: "waiting_user",
              });
            }
            finalizeLatestAssistantStepIfIdle(threadId);
            break;
          }

          if (name === "run_terminal") {
            const terminalStatus = normalizeRunTerminalStatus(payload);
            if (TERMINAL_FAILURE_STATUSES.has(terminalStatus)) {
              appendAssistantError(
                threadId,
                extractRunEventErrorMessage(
                  payload,
                  "The run could not be completed.",
                ),
              );
              break;
            }
            if (terminalStatus === "cancelled") {
              break;
            }
            if (terminalStatus === "completed") {
              finalizeLatestAssistantStepIfIdle(threadId);
              void hydrateRunCompactionStats();
            }
            continue;
          }

          if (name === "local_progress" || name === "context_progress") {
            const stats = normalizeCompactionStatsPayload(payload);
            const phase = String(stats?.phase || "").trim();
            if (phase === "context_compacting") {
              const status = String(stats?.status || "")
                .trim()
                .toLowerCase();
              applyCompactionStatsToLatestAssistant(stats, {
                createIfMissing: true,
                inProgress: status !== "completed" && status !== "failed",
                complete: status === "completed" || status === "failed",
              });
              continue;
            }
          }

          if (name === "plan_update") {
            // First-class plan row (plan §4.6) — never assistant prose.
            const change =
              isPlainObject(payload) && isPlainObject((payload as any).change)
                ? (payload as any).change
                : null;
            const planId = String(
              (payload as any)?.planId ?? (payload as any)?.plan_id ?? "",
            ).trim();
            if (change) {
              const kind = String(change.kind || "").trim();
              const attachmentId =
                String(
                  change.attachmentId ?? change.attachment_id ?? "",
                ).trim() || null;
              appendPlanUpdateItem(threadId, planId, runId, {
                kind: "update",
                code: kind || undefined,
                text: String(change.text || "").trim(),
                attachmentId,
              });
              if (kind === "fidelity_reduction" && attachmentId) {
                markAttachmentReducedFidelity(threadId, attachmentId);
              }
            }
            continue;
          }

          if (name === "assistant_start") {
            const running = runningByThreadRef.current[threadId];
            if (
              running &&
              running.id === runId &&
              running.status === "queued"
            ) {
              setRunForThread(threadId, { ...running, status: "running" });
            }
            streamSegmentersByRunIdRef.current[runId] =
              createStreamingMarkdownSegmenter();
            appendAssistantPlaceholder(threadId);
            continue;
          }

          if (name === "thinking_start") {
            setMessagesForThread(threadId, (prev) => {
              const last = prev[prev.length - 1];
              if (!last || last.role !== Role.Assistant) return prev;
              return prev.slice(0, -1).concat([
                {
                  ...last,
                  thinking: "",
                  thinkingComplete: false,
                  _uiStreaming: true,
                },
              ]);
            });
            continue;
          }

          if (name === "thinking_delta") {
            const delta = isPlainObject(payload)
              ? String((payload as any).content ?? "")
              : String(payload ?? "");
            if (!delta) continue;
            pendingThinkingDelta += delta;
            scheduleAssistantDeltaFlush();
            continue;
          }

          if (name === "thinking_end") {
            setMessagesForThread(threadId, (prev) => {
              const last = prev[prev.length - 1];
              if (!last || last.role !== Role.Assistant) return prev;
              return prev
                .slice(0, -1)
                .concat([
                  { ...last, thinkingComplete: true, _uiStreaming: true },
                ]);
            });
            continue;
          }

          if (name === "compaction_start") {
            applyCompactionStatsToLatestAssistant(
              normalizeCompactionStatsPayload(payload),
              {
                createIfMissing: true,
                inProgress: true,
                complete: false,
                resetSummary: true,
              },
            );
            continue;
          }

          if (name === "compaction_delta") {
            const content = isPlainObject(payload)
              ? String((payload as any).content ?? "")
              : String(payload ?? "");
            if (!content) continue;
            setMessagesForThread(threadId, (prev) => {
              const last = prev[prev.length - 1];
              if (!last || last.role !== Role.Assistant) return prev;
              const existingSummary =
                typeof last.compactionSummary === "string"
                  ? last.compactionSummary
                  : "";
              return prev.slice(0, -1).concat([
                {
                  ...last,
                  compactionSummary: existingSummary + content,
                },
              ]);
            });
            continue;
          }

          if (name === "compaction_end") {
            applyCompactionStatsToLatestAssistant(
              normalizeCompactionStatsPayload(payload),
              {
                createIfMissing: true,
                inProgress: false,
                complete: true,
              },
            );
            continue;
          }

          if (name === "text_delta") {
            const delta = isPlainObject(payload)
              ? String((payload as any).content ?? "")
              : String(payload ?? "");
            if (!delta) continue;
            pendingTextDelta += delta;
            scheduleAssistantDeltaFlush();
            continue;
          }

          if (name === "tool_calls") {
            const toolCalls: ToolCall[] =
              isPlainObject(payload) &&
              Array.isArray((payload as any).toolCalls)
                ? (payload as any).toolCalls
                : isPlainObject(payload) &&
                    Array.isArray((payload as any).tool_calls)
                  ? (payload as any).tool_calls
                  : Array.isArray(payload)
                    ? (payload as any)
                    : [];

            const visibleToolCalls = filterVisibleToolCalls(toolCalls);
            // Stamp UI start time for elapsed timer display.
            const now = Date.now();
            for (const tc of visibleToolCalls) {
              (tc as any)._uiStartedAt = now;
              (tc as any)._uiRunId = runId;
              const callId =
                typeof (tc as any).id === "string" ? (tc as any).id.trim() : "";
              const progressKey = makeToolProgressKey(runId, callId);
              if (progressKey) activeToolProgressKeys.add(progressKey);
            }

            setMessagesForThread(threadId, (prev) => {
              const last = prev[prev.length - 1];
              if (last && last.role === Role.Assistant) {
                if (visibleToolCalls.length === 0) {
                  const sanitizedLast = sanitizeAssistantMessageForUI(last);
                  if (!sanitizedLast) return prev.slice(0, -1);
                  return sanitizedLast === last
                    ? prev
                    : prev.slice(0, -1).concat([sanitizedLast]);
                }
                const merged = sanitizeAssistantMessageForUI({
                  ...last,
                  tool_calls: visibleToolCalls,
                  _uiStreaming: true,
                });
                if (!merged) return prev.slice(0, -1);
                return prev.slice(0, -1).concat([merged]);
              }
              if (visibleToolCalls.length === 0) return prev;
              const nextAssistant = sanitizeAssistantMessageForUI({
                role: Role.Assistant,
                content: null,
                tool_calls: visibleToolCalls,
                _uiStreaming: true,
              });
              if (!nextAssistant) return prev;
              return prev.concat([nextAssistant]);
            });
            continue;
          }

          if (name === "approval") {
            // Persistent approvals: create + every state change stream (and
            // replay) through this event. The shared store fans updates out to
            // the pending strip, transcript cards, and Settings queue.
            publishApprovalEvent(payload, { threadId, runId });
            continue;
          }

          if (name === "tool_progress") {
            if (
              isPlainObject(payload) &&
              typeof (payload as any).tool_call_id === "string"
            ) {
              const rawProgress = payload as Record<string, any>;
              const toolName =
                typeof rawProgress.tool_name === "string"
                  ? rawProgress.tool_name
                  : "";
              const progressProvider = isQueryToolName(toolName)
                ? normalizeQueryProvider(
                    rawProgress.provider ??
                      (toolName
                        ? getQueryProviderForToolName(toolName)
                        : null) ??
                      undefined,
                    "bigquery",
                  )
                : String(rawProgress.provider || "tool");
              const progress = {
                ...rawProgress,
                provider: progressProvider,
                run_id: runId,
                thread_id: threadId,
              } as ToolProgressEvent;
              if (isPlainObject(rawProgress.approval)) {
                const legacyIntegration = firstCompactString(
                  rawProgress,
                  "legacy_integration",
                  "integration",
                  "provider",
                  "product",
                );
                publishApprovalEvent(rawProgress, {
                  threadId,
                  runId,
                  legacyIntegration:
                    legacyIntegration === "github" ||
                    legacyIntegration === "atlassian" ||
                    legacyIntegration === "slack"
                      ? legacyIntegration
                      : undefined,
                });
              }
              const callId = progress.tool_call_id.trim();
              const progressKey = makeToolProgressKey(runId, callId);
              if (progressKey) {
                activeToolProgressKeys.add(progressKey);
                setToolProgressByKey((prev) => ({
                  ...prev,
                  [progressKey]: progress,
                }));
              }
            }
            continue;
          }

          if (name === "provider_tool_progress") {
            if (isPlainObject(payload)) {
              upsertProviderToolProgress(payload as Record<string, any>);
            }
            continue;
          }

          if (name === "tool_result") {
            const rawMsg: any =
              isPlainObject(payload) && isPlainObject((payload as any).message)
                ? (payload as any).message
                : isPlainObject(payload) &&
                    typeof (payload as any).role === "string"
                  ? payload
                  : null;
            let msg: UIMessage | null = normalizeMessage(rawMsg);
            if (msg) {
              if (String((msg as any)._uiRunId || "").trim() !== runId) {
                msg = { ...msg, _uiRunId: runId } as UIMessage;
              }
              let env = tryParseToolResultEnvelope(msg.content || null);
              const callId = String(
                env?.tool_call_id || (msg as any).tool_call_id || "",
              ).trim();
              const toolName = (env?.tool_name || msg.name || "").trim();
              markPreviewOnlyArtifactDiagnostic(
                previewOnlyDiagnosticFromEnvelope(env),
              );

              if (env && isPlainObject(env.result)) {
                const nextResult = { ...(env.result as any) };
                let mutated = false;

                const hasRunId =
                  typeof nextResult._uiRunId === "string" &&
                  nextResult._uiRunId.trim().length > 0;
                if (!hasRunId) {
                  nextResult._uiRunId = runId;
                  mutated = true;
                }

                if (callId) {
                  const hasStartedAt =
                    typeof nextResult._uiStartedAt === "number" &&
                    Number.isFinite(nextResult._uiStartedAt);
                  if (!hasStartedAt) {
                    const startedAt = getToolCallUiStartedAt(threadId, callId);
                    nextResult._uiStartedAt =
                      typeof startedAt === "number" ? startedAt : Date.now();
                    mutated = true;
                  }
                }

                const hasEndedAt =
                  typeof nextResult._uiEndedAt === "number" &&
                  Number.isFinite(nextResult._uiEndedAt);
                if (!hasEndedAt) {
                  nextResult._uiEndedAt = Date.now();
                  mutated = true;
                }

                if (mutated) {
                  env = { ...env, result: nextResult };
                  msg = { ...msg, content: JSON.stringify(env) };
                }
              }

              const suppressUi = env
                ? shouldSuppressToolResultUI(
                    toolName,
                    env.result_format,
                    env.result,
                    env,
                  )
                : false;
              if (!suppressUi) {
                const dedupeKey = makeToolMessageDedupKey(runId, msg, env);
                const isDuplicate = dedupeKey
                  ? seenToolMessageKeys.has(dedupeKey)
                  : false;
                if (!isDuplicate) {
                  if (dedupeKey) seenToolMessageKeys.add(dedupeKey);
                  setMessagesForThread(threadId, (prev) => prev.concat([msg]));
                  applyToolResultMessageSideEffects(msg);
                  const progressKey = makeToolProgressKey(runId, callId);
                  if (progressKey) activeToolProgressKeys.add(progressKey);
                }
              } else {
                if (callId)
                  removeToolCallFromAssistantMessages(threadId, callId);
                finalizeLatestAssistantStepIfIdle(threadId);
              }
              // Clean up progress state for the completed tool call.
              const progressKey = makeToolProgressKey(runId, callId);
              if (progressKey) {
                setToolProgressByKey((prev) => {
                  if (!prev[progressKey]) return prev;
                  const next = { ...prev };
                  delete next[progressKey];
                  return next;
                });
              }
            }
            continue;
          }

          if (name === "artifact") {
            if (
              payload &&
              typeof payload === "object" &&
              typeof (payload as any).id === "string"
            ) {
              markPreviewOnlyArtifactDiagnostic(
                previewOnlyDiagnosticFromArtifactPayload(payload),
              );
              upsertArtifact({
                ...(payload as Artifact),
                metadata: {
                  ...((payload as any).metadata || {}),
                  placeholder: false,
                },
              });
            }
            continue;
          }

          if (name === "message") {
            let msg = normalizeMessage(payload);
            if (msg) {
              if (msg.role === Role.Tool) {
                if (String((msg as any)._uiRunId || "").trim() !== runId) {
                  msg = { ...msg, _uiRunId: runId } as UIMessage;
                }
                let env = tryParseToolResultEnvelope(msg.content || null);
                const callId = String(
                  env?.tool_call_id || (msg as any).tool_call_id || "",
                ).trim();
                const toolName = (env?.tool_name || msg.name || "").trim();
                markPreviewOnlyArtifactDiagnostic(
                  previewOnlyDiagnosticFromEnvelope(env),
                );

                if (env && isPlainObject(env.result)) {
                  const nextResult = { ...(env.result as any) };
                  let mutated = false;

                  const hasRunId =
                    typeof nextResult._uiRunId === "string" &&
                    nextResult._uiRunId.trim().length > 0;
                  if (!hasRunId) {
                    nextResult._uiRunId = runId;
                    mutated = true;
                  }

                  if (callId) {
                    const hasStartedAt =
                      typeof nextResult._uiStartedAt === "number" &&
                      Number.isFinite(nextResult._uiStartedAt);
                    if (!hasStartedAt) {
                      const startedAt = getToolCallUiStartedAt(
                        threadId,
                        callId,
                      );
                      nextResult._uiStartedAt =
                        typeof startedAt === "number" ? startedAt : Date.now();
                      mutated = true;
                    }
                  }

                  const hasEndedAt =
                    typeof nextResult._uiEndedAt === "number" &&
                    Number.isFinite(nextResult._uiEndedAt);
                  if (!hasEndedAt) {
                    nextResult._uiEndedAt = Date.now();
                    mutated = true;
                  }

                  if (mutated) {
                    env = { ...env, result: nextResult };
                    msg = { ...msg, content: JSON.stringify(env) };
                  }
                }

                const suppressUi = env
                  ? shouldSuppressToolResultUI(
                      toolName,
                      env.result_format,
                      env.result,
                      env,
                    )
                  : false;
                if (suppressUi) {
                  if (callId)
                    removeToolCallFromAssistantMessages(threadId, callId);
                  finalizeLatestAssistantStepIfIdle(threadId);
                  continue;
                }
                const dedupeKey = makeToolMessageDedupKey(runId, msg, env);
                if (dedupeKey) {
                  if (seenToolMessageKeys.has(dedupeKey)) continue;
                  seenToolMessageKeys.add(dedupeKey);
                }
                const progressKey = makeToolProgressKey(runId, callId);
                if (progressKey) activeToolProgressKeys.add(progressKey);
              }

              if (msg.role === Role.Assistant) {
                const sanitizedAssistant = sanitizeAssistantMessageForUI(msg);
                if (!sanitizedAssistant) {
                  pruneTrailingEmptyAssistant(threadId);
                  continue;
                }
                msg = sanitizedAssistant;
              }

              setMessagesForThread(threadId, (prev) => prev.concat([msg]));
              applyToolResultMessageSideEffects(msg);
            }
            continue;
          }

          if (name === "assistant_final" || name === "final") {
            const rawFinalMessage =
              isPlainObject(payload) && isPlainObject((payload as any).message)
                ? normalizeMessage((payload as any).message)
                : null;
            const finalMessage =
              rawFinalMessage?.role === Role.Assistant
                ? sanitizeAssistantMessageForUI(rawFinalMessage)
                : rawFinalMessage;

            const finalText =
              isPlainObject(payload) &&
              typeof (payload as any).answer === "string"
                ? (payload as any).answer
                : isPlainObject(payload) &&
                    isPlainObject((payload as any).message) &&
                    typeof (payload as any).message.content === "string"
                  ? (payload as any).message.content
                  : isPlainObject(payload) &&
                      typeof (payload as any).content === "string"
                    ? (payload as any).content
                    : typeof payload === "string"
                      ? payload
                      : "";
            const hasFinalText = String(finalText || "").trim().length > 0;

            if (!hasFinalText && !finalMessage) {
              // Some providers stream all visible text via text_delta and send
              // assistant_final with content=null. In that case we still need to
              // clear the streaming flag on the last assistant row.
              finalizeLatestAssistantStepIfIdle(threadId);
              pruneTrailingEmptyAssistant(threadId);
              if (name === "final") break;
              continue;
            }

            const resolveFinalAssistantContent = (
              fallback: UIMessage["content"],
            ): string => {
              if (hasFinalText) return finalText;
              const messageContent = finalMessage?.content;
              if (typeof messageContent === "string" && messageContent.trim())
                return messageContent;
              return typeof fallback === "string" ? fallback : "";
            };

            const clearAssistantStreamingFields = (
              message: UIMessage,
            ): UIMessage => {
              const cleared: UIMessage = { ...message, _uiStreaming: false };
              delete (cleared as any).streamStablePrefix;
              delete (cleared as any).streamUnstableSuffix;
              delete (cleared as any).streamInCodeBlock;
              delete (cleared as any).streamParserDegraded;
              delete (cleared as any).compactionInProgress;
              const thinkingText =
                typeof (cleared as any).thinking === "string"
                  ? (cleared as any).thinking.trim()
                  : "";
              if (thinkingText.length > 0) {
                (cleared as any).thinkingComplete = true;
              } else {
                delete (cleared as any).thinkingComplete;
              }
              return cleared;
            };

            const buildStandaloneFinalAssistant = (
              fallback: UIMessage["content"],
            ): UIMessage => {
              const standalone: UIMessage = clearAssistantStreamingFields({
                ...(finalMessage || {}),
                role: Role.Assistant,
                content: resolveFinalAssistantContent(fallback),
                _uiStreaming: false,
              });
              delete (standalone as any).tool_calls;
              delete (standalone as any).thinking;
              delete (standalone as any).thinkingComplete;
              delete (standalone as any).compactionSummary;
              delete (standalone as any).compactionComplete;
              delete (standalone as any).compactionStats;
              return standalone;
            };
            const shouldSplitOnThisEvent = name === "final";

            setMessagesForThread(threadId, (prev) => {
              const last = prev[prev.length - 1];
              if (last && last.role === Role.Assistant) {
                const resolvedFinalContent = resolveFinalAssistantContent(
                  last.content,
                );
                const normalizedResolvedFinalContent =
                  resolvedFinalContent.trim();
                const hasFinalNarrative =
                  normalizedResolvedFinalContent.length > 0;
                const finalMessageNarrative =
                  typeof finalMessage?.content === "string"
                    ? finalMessage.content.trim()
                    : "";
                const hasExplicitFinalNarrative =
                  hasFinalText || finalMessageNarrative.length > 0;
                const lastVisibleToolCalls =
                  filterVisibleToolCalls(
                    Array.isArray((last as any).tool_calls)
                      ? (last as any).tool_calls
                      : [],
                  ).length > 0;
                const lastHasThinking =
                  typeof (last as any).thinking === "string" &&
                  (last as any).thinking.trim().length > 0;
                const lastHasCompactionNarrative =
                  typeof (last as any).compactionSummary === "string" &&
                  (last as any).compactionSummary.trim().length > 0;
                const lastHasCompactionState =
                  (last as any).compactionInProgress === true ||
                  (last as any).compactionComplete === true;
                const incomingVisibleToolCalls =
                  filterVisibleToolCalls(
                    Array.isArray((finalMessage as any)?.tool_calls)
                      ? (finalMessage as any).tool_calls
                      : [],
                  ).length > 0;

                const shouldSplitStandaloneFinal =
                  shouldSplitOnThisEvent &&
                  hasFinalNarrative &&
                  (lastVisibleToolCalls ||
                    lastHasThinking ||
                    lastHasCompactionNarrative ||
                    lastHasCompactionState ||
                    incomingVisibleToolCalls);

                if (shouldSplitStandaloneFinal) {
                  const finalizedLastCandidate =
                    clearAssistantStreamingFields(last);
                  const lastNarrative =
                    typeof finalizedLastCandidate.content === "string"
                      ? finalizedLastCandidate.content.trim()
                      : "";
                  const shouldStripLastNarrative =
                    hasFinalNarrative &&
                    (!hasExplicitFinalNarrative ||
                      lastNarrative === normalizedResolvedFinalContent);
                  if (shouldStripLastNarrative) {
                    finalizedLastCandidate.content = null;
                  }
                  const finalizedLast = sanitizeAssistantMessageForUI(
                    finalizedLastCandidate,
                  );
                  const baseMessages = finalizedLast
                    ? prev.slice(0, -1).concat([finalizedLast])
                    : prev.slice(0, -1);
                  const finalAssistant = sanitizeAssistantMessageForUI(
                    buildStandaloneFinalAssistant(last.content),
                  );
                  if (!finalAssistant) return baseMessages;
                  return baseMessages.concat([finalAssistant]);
                }

                const mergedAssistant = clearAssistantStreamingFields({
                  ...last,
                  ...(finalMessage || {}),
                  role: Role.Assistant,
                  content: resolveFinalAssistantContent(last.content),
                  _uiStreaming: false,
                });
                const sanitizedMerged =
                  sanitizeAssistantMessageForUI(mergedAssistant);
                if (!sanitizedMerged) return prev.slice(0, -1);
                return prev.slice(0, -1).concat([sanitizedMerged]);
              }

              const finalAssistant = sanitizeAssistantMessageForUI(
                buildStandaloneFinalAssistant(null),
              );
              if (!finalAssistant) return prev;
              return prev.concat([finalAssistant]);
            });
            // final is terminal for most backends.
            if (name === "final") break;
            continue;
          }

          if (name === "error") {
            if (!isUserFacingRunErrorEvent(name, payload)) {
              continue;
            }
            appendAssistantError(
              threadId,
              extractRunEventErrorMessage(payload, "Unknown error"),
            );
            break;
          }
        }
      } catch (e: any) {
        const msg = normalizeThrownErrorMessage(
          e,
          "Failed while streaming response.",
        );
        // Abort is normal (stop/steer).
        if (controller.signal.aborted) {
          pruneTrailingEmptyAssistant(threadId);
          return;
        }
        if (runningByThreadRef.current[threadId]?.id === runId) {
          appendAssistantError(threadId, msg);
        }
      } finally {
        flushPendingAssistantDeltas();
        delete streamSegmentersByRunIdRef.current[runId];
        setToolProgressByKey((prev) => {
          const runKeyPrefix = `${runId}:`;
          const next = { ...prev };
          let changed = false;

          for (const progressKey of Object.keys(next)) {
            if (!progressKey.startsWith(runKeyPrefix)) continue;
            delete next[progressKey];
            changed = true;
          }

          if (!changed && activeToolProgressKeys.size > 0) {
            for (const progressKey of activeToolProgressKeys) {
              if (!Object.prototype.hasOwnProperty.call(next, progressKey))
                continue;
              delete next[progressKey];
              changed = true;
            }
          }

          return changed ? next : prev;
        });
        delete abortByRunIdRef.current[runId];
        delete waitAbortByRunIdRef.current[runId];
        if (runningByThreadRef.current[threadId]?.id === runId) {
          setRunForThread(threadId, null);
        }
        if (!streamEndedWaitingUser) {
          setRunStreamStatusForThread(threadId, (prev) =>
            prev?.runId === runId ? null : prev,
          );
        }
        pruneTrailingEmptyAssistant(threadId);
        // Refresh thread list (picks up model-generated title/summary updates).
        void refreshThreads();
      }
    },
    [
      appendAssistantError,
      appendAssistantPlaceholder,
      appendPlanUpdateItem,
      applyToolResultMessageSideEffects,
      getToolCallUiStartedAt,
      markAttachmentReducedFidelity,
      pruneTrailingEmptyAssistant,
      refreshThreads,
      finalizeLatestAssistantStepIfIdle,
      removeToolCallFromAssistantMessages,
      setMessagesForThread,
      setRunForThread,
      setRunStreamStatusForThread,
      upsertArtifact,
    ],
  );

  const activeProviderConfig = useMemo(
    () => getActiveProviderConfig(settings),
    [settings],
  );
  const activeCompatibleProfileId = useMemo(() => {
    if (activeProviderConfig?.providerId === "compatible")
      return activeProviderConfig.id;
    const desired = settings.provider.profileId;
    if (desired && settings.compatibleProfiles.some((p) => p.id === desired))
      return desired;
    return settings.compatibleProfiles[0]?.id || null;
  }, [
    activeProviderConfig,
    settings.provider.profileId,
    settings.compatibleProfiles,
  ]);

  /**
   * The exact connection credentials are validated against (plan §4.4): the
   * thread's selected target when one exists, never silently the global
   * default. An explicit target whose connection is missing FAILS CLOSED
   * (cfg null); only `target: null` falls back to the active config.
   */
  const resolveAuthSubject = useCallback(
    (target?: ThreadTarget | null) => {
      const cfg = resolveAuthTargetConfig(
        target,
        settings.providerConfigs,
        activeProviderConfig,
      );
      const providerId = cfg?.providerId || settings.provider.providerId;
      const compatibleProfileId =
        cfg?.providerId === "compatible" ? cfg.id : activeCompatibleProfileId;
      const model = (target?.model || cfg?.model || "").trim();
      return { cfg, providerId, compatibleProfileId, model };
    },
    [activeCompatibleProfileId, activeProviderConfig, settings],
  );

  type AuthSubject = ReturnType<typeof resolveAuthSubject>;

  const legacyNeedsKeyFromSettings = useCallback(
    (subject: AuthSubject): boolean => {
      const { cfg, providerId, compatibleProfileId } = subject;
      const authType = cfg?.authType || "api_key";
      if (authType === "none") return false;
      return (
        (providerId === "openai" &&
          authType === "api_key" &&
          !settings.openai?.apiKey?.trim()) ||
        (providerId === "anthropic" &&
          authType === "api_key" &&
          !settings.anthropic?.apiKey?.trim()) ||
        (providerId === "gemini" &&
          authType === "api_key" &&
          !settings.gemini?.apiKey?.trim()) ||
        (providerId === "bedrock" &&
          (() => {
            if (authType === "cloud_credentials") {
              const hasRef = Boolean(cfg?.cloudCredentialRefs?.awsCredentialId);
              if (!hasRef) return true;
            }
            // Best-effort fallback only when running without the local secrets API.
            const apiKey = settings.bedrock?.apiKey?.trim() || "";
            if (apiKey) return false;
            const access = settings.bedrock?.accessKeyId?.trim() || "";
            const secret = settings.bedrock?.secretAccessKey?.trim() || "";
            if (!access && !secret) return false;
            return !(access && secret);
          })()) ||
        (providerId === "compatible" &&
          (() => {
            if (authType !== "api_key") return false;
            const profile = resolveCompatibleAuthProfile(
              cfg,
              compatibleProfileId,
              settings.compatibleProfiles,
            );
            if (!profile) return true;
            if (profile.auth.type !== "api_key") return false;
            return !profile.apiKey?.trim();
          })())
      );
    },
    [settings],
  );

  const providerAuthTarget = useCallback(
    (subject: AuthSubject): string => {
      const { cfg, providerId, compatibleProfileId } = subject;
      if (cfg?.displayName?.trim()) return cfg.displayName.trim();
      if (providerId === "compatible") {
        const profile =
          settings.compatibleProfiles.find(
            (p) => p.id === compatibleProfileId,
          ) || settings.compatibleProfiles[0];
        return profile?.displayName || "the selected compatible profile";
      }
      if (providerId === "openai") return "OpenAI";
      if (providerId === "anthropic") return "Anthropic";
      if (providerId === "gemini") return "Gemini";
      if (providerId === "bedrock") return "AWS Bedrock";
      return "the selected provider";
    },
    [settings.compatibleProfiles],
  );

  const preflightAuth = useCallback(
    async (target?: ThreadTarget | null): Promise<PreflightAuthResult> => {
      const subject = resolveAuthSubject(target);
      const cfg = subject.cfg;
      if (!cfg) {
        return {
          ok: false,
          message: target
            ? "The selected model's connection no longer exists. Pick a model in the composer or add one in Settings."
            : "No provider configuration found. Add a provider in Settings before sending messages.",
        };
      }

      if (!subject.model) {
        return {
          ok: false,
          message:
            "Configured provider is missing a model. Update the selected provider before sending messages.",
        };
      }

      const providerId = subject.providerId;
      const authType = cfg.authType || "api_key";
      if (authType === "none") return { ok: true };

      // Prefer server-side secrets (Go server). If unavailable, fall back to legacy localStorage keys.
      let status = null as null | Awaited<ReturnType<typeof getSecretsStatus>>;
      let statusError: any = null;
      try {
        status = await getSecretsStatus();
      } catch (e: any) {
        statusError = e;
        status = null;
      }

      if (!status && statusError) {
        const msg = String(statusError?.message || "").toLowerCase();
        const failedFetch =
          msg === "failed to fetch" ||
          msg.includes("networkerror") ||
          msg.includes("fetch failed");
        if (failedFetch) {
          return {
            ok: false,
            message:
              "Failed to reach the local server for credentials check. Verify the app backend is running.",
          };
        }
      }

      if (status) {
        if (status.initialized === false || status.locked === true) {
          return {
            ok: false,
            message:
              "Secrets store is locked or not initialized. Open Settings and unlock Security to continue.",
          };
        }

        const presentOf = (candidates: string[]): boolean | null => {
          for (const k of candidates) {
            if (Object.prototype.hasOwnProperty.call(status.present, k))
              return Boolean(status.present[k]);
          }
          return null;
        };

        if (providerId === "openai" && authType === "api_key") {
          const present = presentOf(["openai_api_key", "provider:openai"]);
          if (present === false) {
            return {
              ok: false,
              message: `Missing API key for ${providerAuthTarget(subject)}.`,
            };
          }
        } else if (providerId === "anthropic" && authType === "api_key") {
          const present = presentOf([
            "anthropic_api_key",
            "provider:anthropic",
          ]);
          if (present === false) {
            return {
              ok: false,
              message: `Missing API key for ${providerAuthTarget(subject)}.`,
            };
          }
        } else if (providerId === "gemini" && authType === "api_key") {
          const present = presentOf(["gemini_api_key", "provider:gemini"]);
          if (present === false) {
            return {
              ok: false,
              message: `Missing API key for ${providerAuthTarget(subject)}.`,
            };
          }
        } else if (providerId === "bedrock") {
          if (authType === "cloud_credentials") {
            const hasRef = Boolean(cfg.cloudCredentialRefs?.awsCredentialId);
            if (!hasRef) {
              return {
                ok: false,
                message: `Select AWS cloud credentials for ${providerAuthTarget(subject)}.`,
              };
            }
            const present = presentOf([
              "bedrock_aws_credentials",
              "bedrock_credentials",
              "provider:bedrock",
            ]);
            if (present === false) {
              return {
                ok: false,
                message: `Missing credentials for ${providerAuthTarget(subject)}.`,
              };
            }
            return { ok: true };
          }

          let sawBedrockKey = false;
          let present = false;
          const keys =
            authType === "api_key"
              ? ["bedrock_api_key", "provider:bedrock"]
              : [
                  "bedrock_api_key",
                  "bedrock_aws_credentials",
                  "bedrock_credentials",
                  "provider:bedrock",
                ];
          for (const key of keys) {
            if (!Object.prototype.hasOwnProperty.call(status.present, key))
              continue;
            sawBedrockKey = true;
            if (Boolean(status.present[key])) {
              present = true;
              break;
            }
          }
          if (!sawBedrockKey) {
            const legacyPresent = presentOf([
              "bedrock_credentials",
              "provider:bedrock",
            ]);
            if (legacyPresent === false) {
              return {
                ok: false,
                message: `Missing credentials for ${providerAuthTarget(subject)}.`,
              };
            }
          } else if (!present) {
            return {
              ok: false,
              message: `Missing credentials for ${providerAuthTarget(subject)}.`,
            };
          }
        } else if (providerId === "compatible") {
          if (authType !== "api_key") return { ok: true };
          const profileId = subject.compatibleProfileId;
          const profile = resolveCompatibleAuthProfile(
            cfg,
            profileId,
            settings.compatibleProfiles,
          );
          if (profile && profile.auth.type === "api_key") {
            const pid = profile.id;
            const present = presentOf([
              `compatible:${pid}:api_key`,
              `provider:compatible:${pid}`,
            ]);
            // Dynamic compatible secrets are omitted entirely until created.
            // For an exact saved connection, absence is therefore a known
            // missing credential, not permission to borrow/fall back.
            if (present !== true) {
              return {
                ok: false,
                message: `Missing API key for ${providerAuthTarget(subject)}.`,
              };
            }
          } else if (!profile && profileId) {
            // New-style compatible connection without a legacy profile entry:
            // its secret is still keyed by the connection id.
            const present = presentOf([
              `compatible:${profileId}:api_key`,
              `provider:compatible:${profileId}`,
            ]);
            if (present !== true) {
              return {
                ok: false,
                message: `Missing API key for ${providerAuthTarget(subject)}.`,
              };
            }
          }
        }
        return { ok: true };
      }

      if (legacyNeedsKeyFromSettings(subject)) {
        return {
          ok: false,
          message: `Missing API key for ${providerAuthTarget(subject)}.`,
        };
      }
      return { ok: true };
    },
    [
      legacyNeedsKeyFromSettings,
      providerAuthTarget,
      resolveAuthSubject,
      settings.compatibleProfiles,
    ],
  );

  /**
   * Authenticates one exact target per send attempt. `checked` is the send's
   * dedup set (a switched target re-authenticates; an unchanged one doesn't).
   * Returns the failure message, or null when credentials check out.
   */
  const authenticateTargetOnce = useCallback(
    async (
      target: ThreadTarget | null,
      checked: Set<string>,
    ): Promise<string | null> => {
      const key = target
        ? `${target.connectionId}::${target.model}`
        : "::active";
      if (checked.has(key)) return null;
      const auth = await preflightAuth(target);
      if (!auth.ok) {
        return (
          auth.message || "Authentication is required before sending messages."
        );
      }
      checked.add(key);
      return null;
    },
    [preflightAuth],
  );

  // ---- Capability target resolution + preflight gate (plan §4.4/§4.5) ----

  const resolveThreadTarget = useCallback(
    (threadId: string): ThreadTarget | null => {
      const configs = (settings.providerConfigs || []).filter(
        (c) => c && typeof c.id === "string" && c.id.trim(),
      );
      if (configs.length === 0) return null;
      // An explicit selection — this session's override or the thread's
      // persisted lastTarget — is preserved even when its connection has been
      // deleted: auth/preflight must reject the stale target explicitly
      // rather than silently dispatching a different connection.
      const explicit =
        threadTargetOverridesRef.current[threadId] ||
        threadsRef.current.find((t) => t.id === threadId)?.lastTarget ||
        null;
      if (explicit?.connectionId) {
        const cfg = configs.find((c) => c.id === explicit.connectionId);
        return {
          connectionId: explicit.connectionId,
          model: String(explicit.model || cfg?.model || "").trim(),
        };
      }
      const active = getActiveProviderConfig(settings);
      if (active) {
        return {
          connectionId: active.id,
          model: String(active.model || "").trim(),
        };
      }
      return {
        connectionId: configs[0].id,
        model: String(configs[0].model || "").trim(),
      };
    },
    [settings],
  );

  const explicitThreadTarget = useCallback(
    (threadId: string): ThreadTarget | null =>
      threadTargetOverridesRef.current[threadId] ||
      threadsRef.current.find((thread) => thread.id === threadId)?.lastTarget ||
      null,
    [],
  );

  const targetNeedsPlanTransport = useCallback(
    (target: ThreadTarget | null): boolean => {
      if (!target) return false;
      if (!activeProviderConfig) return true;
      return (
        target.connectionId !== activeProviderConfig.id ||
        target.model.trim() !== String(activeProviderConfig.model || "").trim()
      );
    },
    [activeProviderConfig],
  );

  const targetLabelFor = useCallback(
    (target: ThreadTarget | null): string | undefined => {
      if (!target) return undefined;
      const cfg = (settings.providerConfigs || []).find(
        (c) => c.id === target.connectionId,
      );
      if (!cfg) return undefined;
      return targetIdentityLabel({ ...cfg, model: target.model || cfg.model });
    },
    [settings.providerConfigs],
  );

  // Persist a composer target switch. Local state updates only after the PUT
  // succeeds; the global default connection is never mutated here.
  const switchThreadTarget = useCallback(
    async (
      threadId: string,
      target: ThreadTarget,
      opts?: { viaPreflightGate?: boolean },
    ): Promise<boolean> => {
      if (runningByThreadRef.current[threadId]) {
        toast.error(
          "Wait for the current response to finish before switching models.",
        );
        return false;
      }
      // The sheet's own switch-model fix runs INSIDE the negotiation (and is
      // re-authenticated/re-preflighted by the loop); any other switch while a
      // send is being checked would race the validated target — fail closed.
      if (!opts?.viaPreflightGate && negotiationLocksRef.current[threadId]) {
        toast.error(
          "Hold on — the current message is still being checked before sending.",
        );
        return false;
      }
      try {
        await updateThreadTarget(threadId, target);
      } catch (e: any) {
        toast.error(
          e?.message
            ? `Couldn’t switch the model: ${e.message}`
            : "Couldn’t switch the model. The previous model stays selected.",
        );
        return false;
      }
      // Sync the ref eagerly: the sheet resolves a reflight in the same
      // microtask chain, and the next negotiation iteration must already see
      // the switched target (the state->ref effect flushes later).
      threadTargetOverridesRef.current = {
        ...threadTargetOverridesRef.current,
        [threadId]: target,
      };
      setThreadTargetOverrides((prev) => ({ ...prev, [threadId]: target }));
      return true;
    },
    [],
  );

  const openPreflightGate = useCallback(
    (state: PreflightGateState): Promise<PreflightGateDecision> =>
      new Promise((resolve) => {
        preflightGateResolverRef.current = resolve;
        setPreflightGate(state);
      }),
    [],
  );

  const resolvePreflightGate = useCallback(
    (decision: PreflightGateDecision) => {
      const resolver = preflightGateResolverRef.current;
      preflightGateResolverRef.current = null;
      setPreflightGate(null);
      resolver?.(decision);
    },
    [],
  );

  /**
   * Serializes a thread's full send span (auth → preflight gate → run submit).
   * A second entry while locked fails closed: the caller keeps its draft and
   * nothing is checked, preflighted, or submitted.
   */
  const withNegotiationLock = useCallback(
    async (threadId: string, fn: () => Promise<boolean>): Promise<boolean> => {
      if (negotiationLocksRef.current[threadId]) return false;
      negotiationLocksRef.current[threadId] = true;
      setNegotiatingByThreadId((prev) => ({ ...prev, [threadId]: true }));
      try {
        return await fn();
      } finally {
        delete negotiationLocksRef.current[threadId];
        setNegotiatingByThreadId((prev) => {
          if (!prev[threadId]) return prev;
          const next = { ...prev };
          delete next[threadId];
          return next;
        });
      }
    },
    [],
  );

  /**
   * Shared preflight negotiation for every send path (plan §4.4). Resolves the
   * thread target, calls the resolver, and — for choices/blockers — runs the
   * consolidated gate until the user sends, fixes, or cancels. Cancel returns
   * `{ok:false}` and persists nothing.
   */
  const negotiateSendPlan = useCallback(
    async (
      threadId: string,
      images: UIImageAttachment[],
      files: UIFileAttachment[],
      authedTargets: Set<string>,
    ): Promise<
      | {
          ok: true;
          plan: PlanSendPayload | null;
          items: UICapabilityPlanItem[];
          images: UIImageAttachment[];
          files: UIFileAttachment[];
          reducedAttachmentIds: Set<string>;
          removedAttachmentIds: Set<string>;
        }
      | { ok: false; authMessage?: string }
    > => {
      const removedAttachmentIds = new Set<string>();
      const reducedAttachmentIds = new Set<string>();
      let currentImages = images;
      let currentFiles = files;
      const capabilityFlags = getCapabilityUxFlags(settings);
      if (!capabilityFlags.preflight || !resolveThreadTarget(threadId)) {
        // With plan transport disabled, the backend dispatches the global
        // active connection. Authenticate that actual legacy target rather
        // than a persisted per-thread target that cannot be submitted.
        const authMessage = await authenticateTargetOnce(null, authedTargets);
        if (authMessage) return { ok: false, authMessage };
        return {
          ok: true,
          plan: null,
          items: [],
          images,
          files,
          reducedAttachmentIds,
          removedAttachmentIds,
        };
      }

      setPreflightNotice(null);
      for (;;) {
        // Re-resolve every iteration: a reflight after the sheet switched the
        // thread target (PUT /target) must preflight — and later submit — the
        // NEW connection/model, never the one captured before the gate opened.
        const target = resolveThreadTarget(threadId);
        if (!target) return { ok: false };
        // Authenticate the exact target BEFORE it is preflighted or submitted;
        // a switch to an unauthenticated connection stops here, draft kept.
        const authMessage = await authenticateTargetOnce(target, authedTargets);
        if (authMessage) return { ok: false, authMessage };
        const requirements = buildPreflightRequirements(
          currentImages as Array<Record<string, any>>,
          currentFiles as Array<Record<string, any>>,
          (img) => getImageAttachmentArtifactId(img as UIImageAttachment),
          (file) => getFileAttachmentArtifactId(file as UIFileAttachment),
        );
        let result: PreflightResult;
        try {
          result = await preflightThreadRun(threadId, { target, requirements });
        } catch (e: any) {
          if (isPreflightServiceMissing(e)) {
            const explicitTarget = explicitThreadTarget(threadId);
            if (targetNeedsPlanTransport(explicitTarget)) {
              // This thread explicitly targets a connection/model different
              // from the legacy global dispatch target, but the backend has no
              // plan transport to carry it. Never silently send to another
              // model, including after a reload of the persisted target.
              toast.error(
                "This backend can’t run per-chat model switches. The message wasn’t sent — your draft is kept.",
              );
              return { ok: false };
            }
            // Backend without preflight: legacy send path, no plan.
            return {
              ok: true,
              plan: null,
              items: [],
              images: currentImages,
              files: currentFiles,
              reducedAttachmentIds,
              removedAttachmentIds,
            };
          }
          toast.error(
            e?.message
              ? `Couldn’t check this message before sending: ${e.message}`
              : "Couldn’t check this message before sending. Your draft is kept.",
          );
          return { ok: false };
        }

        if (result.status === "clean" || result.status === "notices") {
          if (result.status === "notices") {
            // Informational only: the send proceeds without another click.
            setPreflightNotice({
              threadId,
              texts: result.notices.map((n) => n.text),
            });
          }
          return {
            ok: true,
            plan: {
              planId: result.planId,
              target,
              requirements,
              choiceSelections: {},
            },
            items: result.notices.map((n) => ({
              kind: "notice" as const,
              code: n.code,
              text: n.text,
              attachmentId: n.attachmentId,
            })),
            images: currentImages,
            files: currentFiles,
            reducedAttachmentIds,
            removedAttachmentIds,
          };
        }

        const decision = await openPreflightGate({ threadId, result });
        if (decision.action === "cancel") return { ok: false };
        if (decision.action === "reflight") continue;
        if (decision.action === "remove_attachment") {
          removedAttachmentIds.add(decision.attachmentId);
          currentImages = currentImages.filter(
            (img) =>
              getImageAttachmentArtifactId(img as UIImageAttachment) !==
              decision.attachmentId,
          );
          currentFiles = currentFiles.filter(
            (file) =>
              getFileAttachmentArtifactId(file as UIFileAttachment) !==
              decision.attachmentId,
          );
          onPreflightAttachmentRemoved?.(threadId, decision.attachmentId);
          continue;
        }
        // decision.action === "send"
        return {
          ok: true,
          plan: {
            planId: result.planId,
            target,
            requirements,
            choiceSelections: decision.selections,
          },
          items: [
            ...result.choices.map((choice) => {
              const selection = decision.selections[choice.issueId];
              const option = choice.options.find(
                (o) => o.id === selection?.optionId,
              );
              if (choice.code === "fidelity_reduction" && choice.attachmentId) {
                reducedAttachmentIds.add(choice.attachmentId);
              }
              return {
                kind: "choice" as const,
                code: choice.code,
                text: option ? `${choice.text} → ${option.label}` : choice.text,
                attachmentId: choice.attachmentId,
              };
            }),
            ...result.notices.map((n) => ({
              kind: "notice" as const,
              code: n.code,
              text: n.text,
              attachmentId: n.attachmentId,
            })),
          ],
          images: currentImages,
          files: currentFiles,
          reducedAttachmentIds,
          removedAttachmentIds,
        };
      }
    },
    [
      authenticateTargetOnce,
      explicitThreadTarget,
      onPreflightAttachmentRemoved,
      openPreflightGate,
      resolveThreadTarget,
      settings,
      targetNeedsPlanTransport,
    ],
  );

  const startRun = useCallback(
    async (
      threadId: string,
      opts: {
        userMessage: UIMessage;
        projectId?: string | null;
        plan?: PlanSendPayload | null;
      },
    ): Promise<RunInfo | "plan_stale" | null> => {
      const runtimeNow = new Date();
      const resolvedTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
      const runtimeContext = {
        nowIso: runtimeNow.toISOString(),
        timezone:
          typeof resolvedTimezone === "string" && resolvedTimezone.trim()
            ? resolvedTimezone.trim()
            : undefined,
        utcOffsetMinutes: -runtimeNow.getTimezoneOffset(),
      };

      let res: Awaited<ReturnType<typeof fetchJson>>;
      try {
        res = await fetchJson(
          `/api/threads/${encodeURIComponent(threadId)}/runs`,
          {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({
              // Shape is intentionally flexible; backend will solidify contract.
              // Connection enablement (activeConnectionIds/dataSources) is
              // deliberately NOT sent: the backend derives the run's tool
              // scope from the server-persisted settings — the canonical
              // enabled-for-run state — so a stale or pre-hydration client can
              // never narrow or widen a run. The backend still accepts those
              // fields from older clients.
              message: opts.userMessage,
              runtimeContext,
              project_id:
                typeof opts.projectId === "string" && opts.projectId.trim()
                  ? opts.projectId.trim()
                  : undefined,
              // Capability plan (additive; plan §5.4). The UI must faithfully
              // submit the chosen plan — the runtime refuses undeclared changes.
              ...(opts.plan
                ? {
                    planId: opts.plan.planId,
                    target: opts.plan.target,
                    choiceSelections: opts.plan.choiceSelections,
                    requirements: opts.plan.requirements,
                  }
                : {}),
            }),
          },
        );
      } catch (e: any) {
        appendAssistantError(
          threadId,
          normalizeThrownErrorMessage(e, "Failed to start run."),
        );
        return null;
      }
      if (res.ok === false) {
        // plan_stale: the caller re-preflights once (plan §4.4 step 9);
        // no assistant error is appended until that recovery finishes.
        if (opts.plan && isPlanStaleResponse(res.status, res.data)) {
          return "plan_stale";
        }
        if (res.status === 409) {
          const activeRunId = extractActiveRunId(res.data);
          if (activeRunId) {
            const recovered = await recoverActiveRun(threadId, activeRunId);
            if (recovered) {
              toastOnce(
                "Reattached to the active run. Your new message was not sent.",
              );
              return recovered;
            }
          }
        }
        const msg = extractErrorMessage(res.data, res.status);
        appendAssistantError(threadId, msg);
        return null;
      }
      const run = normalizeRunInfo(res.data, threadId);
      if (!run) {
        appendAssistantError(threadId, "Backend did not return a run id.");
        return null;
      }
      return run;
    },
    [appendAssistantError, recoverActiveRun, toastOnce],
  );

  const waitForRunStartThenStream = useCallback(
    async (run: RunInfo) => {
      const runId = run.id;
      const threadId = run.threadId;
      const waitController = new AbortController();
      waitAbortByRunIdRef.current[runId] = waitController;
      const startMs = Date.now();
      const maxQueuedWaitMs = 30_000;
      try {
        let attempts = 0;
        while (!waitController.signal.aborted) {
          attempts += 1;
          if (runningByThreadRef.current[threadId]?.id !== runId) return;
          let res: Awaited<ReturnType<typeof fetchJson>>;
          try {
            res = await fetchJson(`/api/runs/${encodeURIComponent(runId)}`, {
              method: "GET",
            });
          } catch {
            if (attempts >= 5) {
              appendAssistantError(
                threadId,
                "Failed to monitor queued run state. Please try again.",
              );
              setRunForThread(threadId, null);
              return;
            }
            await new Promise((r) => setTimeout(r, 400));
            continue;
          }

          if (res.ok === false) {
            appendAssistantError(
              threadId,
              extractErrorMessage(res.data, res.status),
            );
            setRunForThread(threadId, null);
            return;
          }

          const status = String((res.data as any)?.run?.status ?? "")
            .trim()
            .toLowerCase();
          if (status === "queued") {
            if (Date.now() - startMs > maxQueuedWaitMs) {
              const current = runningByThreadRef.current[threadId];
              if (
                current &&
                current.id === runId &&
                current.status === "queued"
              ) {
                setRunForThread(threadId, { ...current, status: "running" });
              }
              return;
            }
            await new Promise((r) => setTimeout(r, 400));
            continue;
          }
          break;
        }

        if (waitController.signal.aborted) return;
        if (runningByThreadRef.current[threadId]?.id !== runId) return;
        void streamRunEvents(run);
      } finally {
        delete waitAbortByRunIdRef.current[runId];
      }
    },
    [appendAssistantError, setRunForThread, streamRunEvents],
  );

  const sendMessage = useCallback(
    async (
      content: string,
      opts?: {
        referencedArtifactIds?: string[];
        referencedMessageIds?: string[];
        referencedThreadIds?: string[];
        referencedThreads?: ThreadReference[];
        hiddenContext?: string;
        imageAttachments?: UIMessage["imageAttachments"];
        fileAttachments?: UIMessage["fileAttachments"];
        threadId?: string;
      },
    ): Promise<boolean> => {
      const trimmed = String(content || "").trim();
      const imageAttachments = Array.isArray(opts?.imageAttachments)
        ? opts.imageAttachments.filter((img) => Boolean(img?.dataUri))
        : [];
      const fileAttachments = Array.isArray(opts?.fileAttachments)
        ? opts.fileAttachments.filter((file) =>
            Boolean(getFileAttachmentArtifactId(file as UIFileAttachment)),
          )
        : [];
      const imageAttachmentArtifactIDs = imageAttachments
        .map((attachment) =>
          getImageAttachmentArtifactId(attachment as UIImageAttachment),
        )
        .filter(Boolean);
      const fileAttachmentArtifactIDs = fileAttachments
        .map((attachment) =>
          getFileAttachmentArtifactId(attachment as UIFileAttachment),
        )
        .filter(Boolean);
      if (
        !trimmed &&
        imageAttachments.length === 0 &&
        fileAttachments.length === 0
      )
        return false;

      const requestedThreadId = String(opts?.threadId || "").trim();
      const threadId = requestedThreadId || (await ensureActiveThread());
      if (!threadId) {
        toast.error(
          "No thread is available. Backend may be unavailable or not configured.",
        );
        return false;
      }

      if (runningByThreadRef.current[threadId]) return false;

      // ---- Capability preflight protocol (plan §4.4) --------------------
      // Runs BEFORE the optimistic append and before the composer clears its
      // draft (ChatInput only clears when this resolves true). Cancel keeps
      // draft and attachments; fixes re-preflight the same pending payload.
      // Credential checks live INSIDE the negotiation: every exact target the
      // loop resolves (including sheet switches) authenticates before it is
      // preflighted or submitted, deduped per send attempt by this set.
      const authedTargets = new Set<string>();
      const negotiated = await negotiateSendPlan(
        threadId,
        imageAttachments,
        fileAttachments,
        authedTargets,
      );
      if (negotiated.ok === false) {
        if (negotiated.authMessage) {
          appendAssistantError(threadId, negotiated.authMessage);
        }
        return false;
      }
      let currentImages = negotiated.images;
      let currentFiles = negotiated.files;
      const removedAttachmentIds = negotiated.removedAttachmentIds;
      let reducedAttachmentIds = negotiated.reducedAttachmentIds;
      let planPayload = negotiated.plan;
      let planSummaryItems = negotiated.items;

      const buildReferencedArtifactIds = (
        images: UIImageAttachment[],
        files: UIFileAttachment[],
      ) => {
        const imageArtifactIds = images
          .map((attachment) =>
            getImageAttachmentArtifactId(attachment as UIImageAttachment),
          )
          .filter(Boolean);
        const fileArtifactIds = files
          .map((attachment) =>
            getFileAttachmentArtifactId(attachment as UIFileAttachment),
          )
          .filter(Boolean);
        // normalizeStringArray yields undefined (not []) when empty — keep
        // that shape so plain-text sends omit the field entirely.
        return normalizeStringArray(
          opts?.referencedArtifactIds,
          imageArtifactIds,
          fileArtifactIds,
        )?.filter((id) => !removedAttachmentIds.has(id));
      };
      const buildSendFiles = (
        files: UIFileAttachment[],
        reducedIds: Set<string>,
      ) =>
        files.map((file) =>
          file.artifactId && reducedIds.has(file.artifactId)
            ? { ...file, sendFidelity: "reduced" as const }
            : file,
        );
      let referencedArtifactIds = buildReferencedArtifactIds(
        currentImages,
        currentFiles,
      );
      const referencedMessageIds = normalizeStringArray(
        opts?.referencedMessageIds,
      );
      const referencedThreadIds = normalizeStringArray(
        opts?.referencedThreadIds,
      );
      const referencedThreads = Array.isArray(opts?.referencedThreads)
        ? (opts.referencedThreads
            .map(normalizeThreadReference)
            .filter(Boolean) as ThreadReference[])
        : [];
      for (const attachment of currentImages) {
        const artifact = buildImageArtifactFromAttachment(
          attachment as UIImageAttachment,
        );
        if (!artifact) continue;
        upsertArtifact(artifact);
      }
      let sendFiles = buildSendFiles(currentFiles, reducedAttachmentIds);
      let userMessage: UIMessage = {
        role: Role.User,
        content: trimmed,
        _uiStableId: createUiStableId(),
        referencedArtifactIds,
        referencedMessageIds,
        referencedThreadIds,
        referencedThreads:
          referencedThreads.length > 0 ? referencedThreads : undefined,
        hiddenContext:
          typeof opts?.hiddenContext === "string"
            ? opts.hiddenContext
            : undefined,
        imageAttachments: currentImages.length > 0 ? currentImages : undefined,
        fileAttachments: sendFiles.length > 0 ? sendFiles : undefined,
      };

      setMessagesForThread(threadId, (prev) => prev.concat([userMessage]));
      // Reorder only on send (not on selection): active thread rises to top when prompted.
      bumpThreadToTop(threadId);

      // Thread-differential run-plan row (plan §4.6): only when the plan has
      // any non-clean content or the target changed since this thread's last run.
      const previousRunTarget = lastRunTargetByThreadRef.current[threadId];
      let planRowStableId: string | null = null;
      let planRowPlanId: string | null = null;
      const syncPlanRow = (
        plan: PlanSendPayload,
        items: UICapabilityPlanItem[],
      ) => {
        const targetChanged = Boolean(
          previousRunTarget &&
          (previousRunTarget.connectionId !== plan.target.connectionId ||
            previousRunTarget.model !== plan.target.model),
        );
        lastRunTargetByThreadRef.current[threadId] = plan.target;
        const shouldRender = items.length > 0 || targetChanged;
        if (!shouldRender) {
          if (planRowStableId) {
            const stableId = planRowStableId;
            setMessagesForThread(threadId, (prev) =>
              prev.filter((message) => message._uiStableId !== stableId),
            );
          }
          if (planRowPlanId) delete planRowsRef.current[planRowPlanId];
          planRowStableId = null;
          planRowPlanId = null;
          return;
        }

        const summary: UICapabilityPlanSummary = {
          planId: plan.planId,
          targetLabel: targetLabelFor(plan.target),
          targetChanged,
          items,
        };
        if (!planRowStableId) {
          planRowStableId = appendCapabilityPlanRow(threadId, summary, {
            afterStableId: userMessage._uiStableId,
          });
        } else {
          const stableId = planRowStableId;
          setMessagesForThread(threadId, (prev) =>
            prev.map((message) =>
              message._uiStableId === stableId
                ? { ...message, capabilityPlan: summary }
                : message,
            ),
          );
        }
        if (planRowPlanId && planRowPlanId !== plan.planId) {
          delete planRowsRef.current[planRowPlanId];
        }
        planRowsRef.current[plan.planId] = {
          threadId,
          stableId: planRowStableId,
        };
        planRowPlanId = plan.planId;
      };
      if (planPayload) {
        syncPlanRow(planPayload, planSummaryItems);
      }

      const rollbackSend = () => {
        rollbackOptimisticUserTurn(threadId, userMessage._uiStableId);
        pruneTrailingEmptyAssistant(threadId);
        if (planRowStableId) {
          const stableId = planRowStableId;
          setMessagesForThread(threadId, (prev) =>
            prev.filter((m) => m._uiStableId !== stableId),
          );
        }
        if (planRowPlanId) delete planRowsRef.current[planRowPlanId];
      };

      // Placeholder bubble so the UI shows streaming immediately (backend may also send assistant_start).
      appendAssistantPlaceholder(threadId);

      let run = await startRun(threadId, {
        userMessage,
        projectId,
        plan: planPayload,
      });

      if (run === "plan_stale" && planPayload) {
        // Run the shared negotiation once more against the same pending
        // attachments. This preserves exact-target auth, fixes/removals,
        // choice scopes, notices, and the final audit row instead of merely
        // swapping the stale plan id.
        const recovered = await negotiateSendPlan(
          threadId,
          currentImages,
          currentFiles,
          authedTargets,
        );
        if (recovered.ok === false) {
          if (recovered.authMessage) toast.error(recovered.authMessage);
          rollbackSend();
          return false;
        }
        if (!recovered.plan) {
          toast.error(
            "The backend can no longer validate this send plan. The message wasn’t sent — your draft is kept.",
          );
          rollbackSend();
          return false;
        }

        currentImages = recovered.images;
        currentFiles = recovered.files;
        for (const id of recovered.removedAttachmentIds) {
          removedAttachmentIds.add(id);
        }
        reducedAttachmentIds = recovered.reducedAttachmentIds;
        referencedArtifactIds = buildReferencedArtifactIds(
          currentImages,
          currentFiles,
        );
        sendFiles = buildSendFiles(currentFiles, reducedAttachmentIds);
        userMessage = {
          ...userMessage,
          referencedArtifactIds,
          imageAttachments:
            currentImages.length > 0 ? currentImages : undefined,
          fileAttachments: sendFiles.length > 0 ? sendFiles : undefined,
        };
        setMessagesForThread(threadId, (prev) =>
          prev.map((message) =>
            message._uiStableId === userMessage._uiStableId
              ? userMessage
              : message,
          ),
        );
        planPayload = recovered.plan;
        planSummaryItems = recovered.items;
        syncPlanRow(planPayload, planSummaryItems);
        run = await startRun(threadId, {
          userMessage,
          projectId,
          plan: planPayload,
        });
        if (run === "plan_stale") {
          appendAssistantError(
            threadId,
            "The send plan changed again while sending. Please try again.",
          );
          run = null;
        }
      }

      const startedRun: RunInfo | null = run === "plan_stale" ? null : run;
      if (!startedRun) {
        pruneTrailingEmptyAssistant(threadId);
        return true;
      }
      if (startedRun.recoveredFromConflict) {
        rollbackOptimisticUserTurn(threadId, userMessage._uiStableId);
      }
      if (planPayload && planRowStableId) {
        attachRunIdToPlanRow(planPayload.planId, startedRun.id);
      }

      setRunForThread(threadId, startedRun);
      void streamRunEvents(startedRun);
      if (startedRun.status === "queued") {
        void waitForRunStartThenStream(startedRun);
      }
      return startedRun.recoveredFromConflict !== true;
    },
    [
      appendAssistantPlaceholder,
      appendCapabilityPlanRow,
      attachRunIdToPlanRow,
      bumpThreadToTop,
      createUiStableId,
      ensureActiveThread,
      authenticateTargetOnce,
      negotiateSendPlan,
      openPreflightGate,
      resolveThreadTarget,
      pruneTrailingEmptyAssistant,
      appendAssistantError,
      rollbackOptimisticUserTurn,
      setRunForThread,
      startRun,
      targetLabelFor,
      waitForRunStartThenStream,
      streamRunEvents,
      setMessagesForThread,
      upsertArtifact,
      projectId,
    ],
  );

  const stopGeneration = useCallback(() => {
    if (!activeThreadId) return;
    cancelThreadRun(activeThreadId);
  }, [activeThreadId, cancelThreadRun]);

  const sendSteer = useCallback(
    async (
      text: string,
      opts?: {
        threadId?: string;
        referencedArtifactIds?: string[];
        imageAttachments?: ChatImageAttachment[];
        fileAttachments?: ChatFileAttachment[];
      },
    ): Promise<boolean> => {
      const trimmed = String(text || "").trim();
      const imageAttachments = Array.isArray(opts?.imageAttachments)
        ? opts.imageAttachments.filter((img) => Boolean(img?.dataUri))
        : [];
      const fileAttachments = Array.isArray(opts?.fileAttachments)
        ? opts.fileAttachments.filter((file) =>
            Boolean(getFileAttachmentArtifactId(file as UIFileAttachment)),
          )
        : [];
      const imageAttachmentArtifactIDs = imageAttachments
        .map((attachment) =>
          getImageAttachmentArtifactId(attachment as UIImageAttachment),
        )
        .filter(Boolean);
      const fileAttachmentArtifactIDs = fileAttachments
        .map((attachment) =>
          getFileAttachmentArtifactId(attachment as UIFileAttachment),
        )
        .filter(Boolean);
      const referencedArtifactIds = normalizeStringArray(
        opts?.referencedArtifactIds,
        imageAttachmentArtifactIDs,
        fileAttachmentArtifactIDs,
      );
      const hasImages = imageAttachments.length > 0;
      const hasFiles = fileAttachments.length > 0;
      if (!trimmed && !hasImages && !hasFiles) return false;

      const requestedThreadId = String(opts?.threadId || "").trim();
      const threadId = requestedThreadId || (await ensureActiveThread());
      if (!threadId) return false;
      const existingRun = runningByThreadRef.current[threadId];

      // If nothing is running for this thread, treat steer as a normal message.
      if (!existingRun)
        return sendMessage(trimmed, {
          threadId,
          referencedArtifactIds,
          imageAttachments,
          fileAttachments,
        });

      // Steer restarts use the same preflight protocol as normal sends
      // (plan §4.4 step 10), INCLUDING exact-target credential checks inside
      // the negotiation. Negotiate BEFORE cancelling: a cancelled sheet — or a
      // failed credential check — leaves the in-flight run untouched.
      const authedTargets = new Set<string>();
      const negotiated = await negotiateSendPlan(
        threadId,
        imageAttachments,
        fileAttachments,
        authedTargets,
      );
      if (negotiated.ok === false) {
        if (negotiated.authMessage) {
          // Toast, not a transcript error: an assistant error bubble would
          // collide with the still-streaming run this steer leaves running.
          toast.error(negotiated.authMessage);
        }
        return false;
      }
      const steerImages = negotiated.images;
      const steerFiles = negotiated.files.map((file) =>
        file.artifactId && negotiated.reducedAttachmentIds.has(file.artifactId)
          ? { ...file, sendFidelity: "reduced" as const }
          : file,
      );
      const steerReferencedArtifactIds = referencedArtifactIds?.filter(
        (id) => !negotiated.removedAttachmentIds.has(id),
      );

      // Cancel and restart as a new message with a steer hint.
      cancelThreadRun(threadId);

      const steerMsg: UIMessage = {
        role: Role.User,
        content: trimmed,
        _uiStableId: createUiStableId(),
        uiKind: "steer",
        referencedArtifactIds: steerReferencedArtifactIds,
        imageAttachments: steerImages.length > 0 ? steerImages : undefined,
        fileAttachments: steerFiles.length > 0 ? steerFiles : undefined,
        hiddenContext: [
          "Steering message for the currently in-progress response:",
          "- Treat this as a modifier, not a new standalone task.",
          "- Continue until the original request is satisfied under this updated guidance.",
          "- Do not repeat text already produced unless revising; if revising, say what changed.",
        ].join("\n"),
      };
      setMessagesForThread(threadId, (prev) => prev.concat([steerMsg]));
      bumpThreadToTop(threadId);

      // Wait for the previous SSE loop to unwind so we don't race the per-thread run state.
      const startedAt = Date.now();
      while (
        runningByThreadRef.current[threadId] &&
        Date.now() - startedAt < 4000
      ) {
        await new Promise((r) => setTimeout(r, 50));
      }
      if (runningByThreadRef.current[threadId]) {
        setMessagesForThread(threadId, (prev) =>
          prev.concat([
            {
              role: Role.Assistant,
              content:
                "Error: Could not cancel the current run yet. Try again.",
            },
          ]),
        );
        return true;
      }

      // Thread-differential plan row for the restarted run (plan §4.6).
      let steerPlan = negotiated.plan;
      let steerPlanRowStableId: string | null = null;
      if (steerPlan) {
        const prevTarget = lastRunTargetByThreadRef.current[threadId];
        const targetChanged = Boolean(
          prevTarget &&
          (prevTarget.connectionId !== steerPlan.target.connectionId ||
            prevTarget.model !== steerPlan.target.model),
        );
        lastRunTargetByThreadRef.current[threadId] = steerPlan.target;
        if (negotiated.items.length > 0 || targetChanged) {
          steerPlanRowStableId = appendCapabilityPlanRow(threadId, {
            planId: steerPlan.planId,
            targetLabel: targetLabelFor(steerPlan.target),
            targetChanged,
            items: negotiated.items,
          });
        }
      }

      appendAssistantPlaceholder(threadId);
      let steerRun = await startRun(threadId, {
        userMessage: steerMsg,
        projectId,
        plan: steerPlan,
      });
      if (steerRun === "plan_stale" && steerPlan) {
        // One automatic recovery attempt; steering is time-sensitive, so a
        // still-contested plan surfaces as an error rather than a second sheet.
        // Re-resolve and authenticate the exact recovery target just like a
        // normal send: the thread target may have changed after the original
        // plan was negotiated, and a stale plan must never authorize a silent
        // dispatch through another connection.
        try {
          const recoveryTarget =
            resolveThreadTarget(threadId) || steerPlan.target;
          const authMessage = await authenticateTargetOnce(
            recoveryTarget,
            authedTargets,
          );
          if (authMessage) {
            appendAssistantError(threadId, authMessage);
            steerRun = null;
            throw new Error("steer recovery target authentication failed");
          }
          const result = await preflightThreadRun(threadId, {
            target: recoveryTarget,
            requirements: steerPlan.requirements,
          });
          if (result.status === "clean" || result.status === "notices") {
            steerPlan = {
              ...steerPlan,
              planId: result.planId,
              target: recoveryTarget,
              choiceSelections: {},
            };
            steerRun = await startRun(threadId, {
              userMessage: steerMsg,
              projectId,
              plan: steerPlan,
            });
          }
        } catch {
          // fall through to the stale handling below
        }
      }
      const nextRun: RunInfo | null =
        steerRun === "plan_stale" ? null : steerRun;
      if (steerRun === "plan_stale") {
        appendAssistantError(
          threadId,
          "The send plan changed while restarting. Please send the steer again.",
        );
      }
      if (!nextRun) {
        pruneTrailingEmptyAssistant(threadId);
        if (steerPlanRowStableId) {
          setMessagesForThread(threadId, (prev) =>
            prev.filter((m) => m._uiStableId !== steerPlanRowStableId),
          );
        }
        return true;
      }
      if (nextRun.recoveredFromConflict) {
        rollbackOptimisticUserTurn(threadId, steerMsg._uiStableId);
      }
      if (steerPlan && steerPlanRowStableId) {
        attachRunIdToPlanRow(steerPlan.planId, nextRun.id);
      }
      setRunForThread(threadId, nextRun);
      void streamRunEvents(nextRun);
      if (nextRun.status === "queued") {
        void waitForRunStartThenStream(nextRun);
      }
      return nextRun.recoveredFromConflict !== true;
    },
    [
      appendAssistantError,
      appendAssistantPlaceholder,
      appendCapabilityPlanRow,
      authenticateTargetOnce,
      attachRunIdToPlanRow,
      bumpThreadToTop,
      cancelThreadRun,
      createUiStableId,
      ensureActiveThread,
      negotiateSendPlan,
      pruneTrailingEmptyAssistant,
      projectId,
      resolveThreadTarget,
      rollbackOptimisticUserTurn,
      sendMessage,
      setMessagesForThread,
      setRunForThread,
      startRun,
      streamRunEvents,
      targetLabelFor,
      waitForRunStartThenStream,
    ],
  );

  const clearChat = useCallback(() => {
    // In a threads-based UI, "clear" means "start fresh": create and select a new thread.
    void createThread();
  }, [createThread]);

  // Initial bootstrap: load threads and restore last active thread.
  useEffect(() => {
    let cancelled = false;
    const init = async () => {
      const list = await refreshThreads();
      if (cancelled) return;

      const stored = (() => {
        try {
          return localStorage.getItem(ACTIVE_THREAD_STORAGE_KEY);
        } catch {
          return null;
        }
      })();
      const chosen =
        stored && list.some((t) => t.id === stored)
          ? stored
          : (list[0]?.id ?? null);

      if (chosen) {
        setActiveThreadId(chosen);
        if (!messagesRef.current[chosen]) void loadThread(chosen);
        return;
      }

      const created = await createThread();
      if (cancelled) return;
      if (created) {
        setActiveThreadId(created.id);
      }
    };
    init();
    return () => {
      cancelled = true;
    };
  }, [createThread, loadThread, refreshThreads]);

  // Keep the active thread hydrated when switching.
  useEffect(() => {
    if (!activeThreadId) return;
    if (messagesRef.current[activeThreadId]) return;
    void loadThread(activeThreadId);
  }, [activeThreadId, loadThread]);

  // Public send surface: every entry point holds the per-thread negotiation
  // lock for its full auth → preflight → submit span, so a concurrent send
  // can never clobber the preflight gate resolver or race the checked target.
  const sendMessageLocked = useCallback(
    async (
      content: string,
      opts?: Parameters<typeof sendMessage>[1],
    ): Promise<boolean> => {
      const requestedThreadId = String(opts?.threadId || "").trim();
      const threadId = requestedThreadId || (await ensureActiveThread());
      if (!threadId) {
        toast.error(
          "No thread is available. Backend may be unavailable or not configured.",
        );
        return false;
      }
      return withNegotiationLock(threadId, () =>
        sendMessage(content, { ...(opts || {}), threadId }),
      );
    },
    [ensureActiveThread, sendMessage, withNegotiationLock],
  );

  const sendSteerLocked = useCallback(
    async (
      text: string,
      opts?: Parameters<typeof sendSteer>[1],
    ): Promise<boolean> => {
      const requestedThreadId = String(opts?.threadId || "").trim();
      const threadId = requestedThreadId || (await ensureActiveThread());
      if (!threadId) return false;
      return withNegotiationLock(threadId, () =>
        sendSteer(text, { ...(opts || {}), threadId }),
      );
    },
    [ensureActiveThread, sendSteer, withNegotiationLock],
  );

  const activeMessages = useMemo(() => {
    if (!activeThreadId) return [];
    return messagesByThreadId[activeThreadId] || [];
  }, [activeThreadId, messagesByThreadId]);

  const activeThreadTitle = useMemo(() => {
    if (!activeThreadId) return "Untitled";
    const t = threads.find((x) => x.id === activeThreadId);
    return t?.title || "Untitled";
  }, [activeThreadId, threads]);

  const isCompacting = useMemo(() => {
    return activeMessages.some(
      (message) =>
        message.role === Role.Assistant && message.compactionInProgress,
    );
  }, [activeMessages]);

  return {
    // Threads
    threads,
    activeThreadId,
    activeThreadTitle,
    selectThread,
    createThread,
    deleteThread,
    exportThread,
    importThread,
    refreshThreads,
    // Chat view (active thread)
    messages: activeMessages,
    messagesByThreadId,
    artifactsById,
    ensureArtifactLoaded,
    // Run state
    isLoading,
    isActiveThreadRunning,
    runningThreadIds,
    activeRunStreamStatus,
    runStreamStatusByThreadId,
    isCompacting,
    // Actions
    sendMessage: sendMessageLocked,
    sendSteer: sendSteerLocked,
    stopGeneration,
    clearChat,
    // Tool progress
    toolProgressByKey,
    // Capability preflight / per-thread target (plan §4.4–§4.6)
    preflightGate,
    resolvePreflightGate,
    preflightNotice,
    dismissPreflightNotice: () => setPreflightNotice(null),
    resolveThreadTarget,
    switchThreadTarget,
    threadTargetOverrides,
    negotiatingByThreadId,
  };
}
