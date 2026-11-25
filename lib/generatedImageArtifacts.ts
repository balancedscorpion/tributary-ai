import type { Artifact, ToolProgressEvent, ToolResultEnvelope } from "@/types";

/**
 * Shared classification + metadata normalization for generated-image artifacts
 * (the local `generate_image` tool). Every surface that treats generated
 * images specially — transcript cards, artifact inspector, Files provenance,
 * audit previews, downloads — must classify through these helpers rather than
 * re-reading raw metadata, because the backend contract may evolve between
 * snake_case and camelCase and between `generated_image` and
 * `image_generation` source markers.
 */

export const IMAGE_GENERATION_TOOL_NAME = "generate_image";

/** Source markers accepted as "this image artifact was generated". */
const GENERATED_IMAGE_SOURCES = new Set([
  "generated_image",
  "image_generation",
]);

export type GeneratedImageErrorCategory =
  | "auth"
  | "rate_limited"
  | "moderation_blocked"
  | "invalid_input"
  | "provider_unavailable"
  | "cancelled"
  | "unknown";

export interface GeneratedImageMetadata {
  prompt: string;
  revisedPrompt: string;
  provider: string;
  model: string;
  width: number | null;
  height: number | null;
  mimeType: string;
  /** Short format token derived from MIME ("png", "jpeg", "webp"). */
  format: string;
  sizeBytes: number | null;
  quality: string;
  requestId: string;
  runId: string;
  toolCallId: string;
  latencyMs: number | null;
  createdAt: string;
  parentArtifactId: string;
  sourceArtifactIds: string[];
}

function metadataOf(artifact: Artifact | null | undefined): Record<string, any> {
  const metadata = artifact?.metadata;
  return metadata && typeof metadata === "object" ? metadata : {};
}

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  return "";
}

function firstNumber(...values: unknown[]): number | null {
  for (const value of values) {
    if (typeof value === "number" && Number.isFinite(value)) return value;
    if (typeof value === "string" && value.trim()) {
      const parsed = Number(value);
      if (Number.isFinite(parsed)) return parsed;
    }
  }
  return null;
}

function stringArray(...values: unknown[]): string[] {
  for (const value of values) {
    if (Array.isArray(value)) {
      const items = value
        .filter((item): item is string => typeof item === "string")
        .map((item) => item.trim())
        .filter(Boolean);
      if (items.length > 0) return items;
    }
  }
  return [];
}

function normalizeToken(value: unknown): string {
  return String(value ?? "").trim().toLowerCase();
}

export function isImageGenerationToolName(name: unknown): boolean {
  return normalizeToken(name) === IMAGE_GENERATION_TOOL_NAME;
}

/**
 * True when an artifact is a generated image: `type:"image"` plus an explicit
 * provenance marker (`source` in the accepted set, a `generated` flag, or the
 * generating tool name). A plain image attachment never matches.
 *
 * Deliberately NOT a type predicate: `artifact is Artifact` would narrow the
 * negative branch of an already-`Artifact` input to `never`.
 */
export function isGeneratedImageArtifact(
  artifact: Artifact | null | undefined,
): boolean {
  if (!artifact || artifact.type !== "image") return false;
  const metadata = metadataOf(artifact);
  if (GENERATED_IMAGE_SOURCES.has(normalizeToken(metadata.source))) return true;
  if (metadata.generated === true) return true;
  return isImageGenerationToolName(metadata.tool_name ?? metadata.toolName);
}

function normalizeMimeType(value: unknown): string {
  return String(value ?? "")
    .split(";")[0]
    .trim()
    .toLowerCase();
}

function mimeFromDataUri(content: unknown): string {
  if (typeof content !== "string") return "";
  const match = content.match(/^data:([^;,]+)[;,]/i);
  return normalizeMimeType(match?.[1]);
}

/** "image/jpeg" → "jpeg"; tolerates bare tokens like "png". */
export function imageFormatFromMime(mimeType: string): string {
  const normalized = normalizeMimeType(mimeType);
  if (!normalized) return "";
  const subtype = normalized.includes("/")
    ? normalized.split("/")[1] || ""
    : normalized;
  if (subtype === "jpg") return "jpeg";
  if (subtype === "svg+xml") return "svg";
  return subtype;
}

/**
 * Normalizes generated-image metadata across snake_case/camelCase spellings.
 * Safe on any artifact; absent fields come back as ""/null/[].
 */
export function getGeneratedImageMetadata(
  artifact: Artifact | null | undefined,
): GeneratedImageMetadata {
  const metadata = metadataOf(artifact);
  const mimeType =
    normalizeMimeType(
      firstString(
        metadata.mimeType,
        metadata.mime_type,
        metadata.detectedMimeType,
        metadata.detected_mime_type,
      ),
    ) || mimeFromDataUri(artifact?.content);
  return {
    prompt: firstString(metadata.prompt, metadata.original_prompt, metadata.originalPrompt),
    revisedPrompt: firstString(metadata.revisedPrompt, metadata.revised_prompt),
    provider: firstString(metadata.provider, metadata.image_provider, metadata.imageProvider),
    model: firstString(metadata.model, metadata.image_model, metadata.imageModel),
    width: firstNumber(metadata.width, metadata.image_width, metadata.imageWidth),
    height: firstNumber(metadata.height, metadata.image_height, metadata.imageHeight),
    mimeType,
    format: imageFormatFromMime(
      mimeType || firstString(metadata.format, metadata.output_format, metadata.outputFormat),
    ),
    sizeBytes: firstNumber(metadata.sizeBytes, metadata.size_bytes, metadata.byte_size),
    quality: normalizeToken(firstString(metadata.quality, metadata.image_quality, metadata.imageQuality)),
    requestId: firstString(metadata.requestId, metadata.request_id),
    runId: firstString(metadata.runId, metadata.run_id),
    toolCallId: firstString(metadata.toolCallId, metadata.tool_call_id),
    latencyMs: firstNumber(metadata.latencyMs, metadata.latency_ms),
    createdAt: firstString(metadata.createdAt, metadata.created_at, metadata.timestamp),
    parentArtifactId: firstString(metadata.parentArtifactId, metadata.parent_artifact_id),
    sourceArtifactIds: stringArray(
      metadata.sourceArtifactIds,
      metadata.source_artifact_ids,
    ),
  };
}

/**
 * Prompt-derived alt text for generated images. Falls back to the artifact
 * title so an <img> never ships an empty alt.
 */
export function generatedImageAltText(artifact: Artifact | null | undefined): string {
  const { prompt, revisedPrompt } = getGeneratedImageMetadata(artifact);
  const source = prompt || revisedPrompt;
  if (source) {
    const trimmed = source.length > 240 ? `${source.slice(0, 237)}…` : source;
    return `Generated image: ${trimmed}`;
  }
  const title = String(artifact?.title || "").trim();
  return title ? `Generated image: ${title}` : "Generated image";
}

/**
 * Ordered artifact IDs of a generate_image result envelope: `artifact_ids`
 * (gallery, when present) with `artifact_id` kept first for compatibility.
 */
export function generatedImageArtifactIds(
  envelope: Partial<ToolResultEnvelope> | Record<string, any> | null | undefined,
): string[] {
  if (!envelope || typeof envelope !== "object") return [];
  const record = envelope as Record<string, any>;
  const ordered = stringArray(record.artifact_ids, record.artifactIds);
  const primary = firstString(record.artifact_id, record.artifactId);
  if (!primary) return ordered;
  return [primary, ...ordered.filter((id) => id !== primary)];
}

const ERROR_CATEGORY_ALIASES: Record<string, GeneratedImageErrorCategory> = {
  auth: "auth",
  auth_error: "auth",
  unauthorized: "auth",
  target_invalid: "auth",
  invalid_target: "auth",
  rate_limited: "rate_limited",
  rate_limit: "rate_limited",
  quota_exceeded: "rate_limited",
  quota: "rate_limited",
  moderation_blocked: "moderation_blocked",
  moderation: "moderation_blocked",
  content_policy_violation: "moderation_blocked",
  safety_blocked: "moderation_blocked",
  invalid_input: "invalid_input",
  invalid_request: "invalid_input",
  provider_unavailable: "provider_unavailable",
  provider_error: "provider_unavailable",
  timeout: "provider_unavailable",
  cancelled: "cancelled",
  canceled: "cancelled",
};

/**
 * Classifies a failed generate_image result into a UI error category.
 * Reads the error code from result_metadata/result across spellings and
 * falls back to scanning the error text. `moderation_blocked` is terminal
 * (never offer Retry — offer "Edit prompt"); `cancelled` is quiet.
 */
export function classifyGeneratedImageError(
  envelope: Partial<ToolResultEnvelope> | Record<string, any> | null | undefined,
): GeneratedImageErrorCategory {
  if (!envelope || typeof envelope !== "object") return "unknown";
  const record = envelope as Record<string, any>;
  const resultMetadata =
    (record.result_metadata && typeof record.result_metadata === "object"
      ? record.result_metadata
      : record.resultMetadata) || {};
  const result =
    record.result && typeof record.result === "object" ? record.result : {};
  const code = normalizeToken(
    firstString(
      resultMetadata.error_category,
      resultMetadata.errorCategory,
      resultMetadata.error_code,
      resultMetadata.errorCode,
      result.error_category,
      result.errorCategory,
      result.error_code,
      result.errorCode,
      result.status === "cancelled" || result.status === "canceled"
        ? "cancelled"
        : "",
    ),
  );
  if (code && ERROR_CATEGORY_ALIASES[code]) return ERROR_CATEGORY_ALIASES[code];
  const text = normalizeToken(
    firstString(result.error, result.message, record.error, record.message),
  );
  if (!text) return "unknown";
  for (const [alias, category] of Object.entries(ERROR_CATEGORY_ALIASES)) {
    if (text.includes(alias.replace(/_/g, " ")) || text.includes(alias)) {
      return category;
    }
  }
  return "unknown";
}

export type GeneratedImageStage = "requesting" | "retrying" | "generating" | "saving";

const IMAGE_STAGE_LABELS: Record<GeneratedImageStage, string> = {
  requesting: "Sending prompt to image model",
  retrying: "Image service busy — retrying",
  generating: "Generating image",
  saving: "Saving image",
};

/**
 * Stage extracted from a tool_progress event for generate_image; the backend
 * may deliver it in `phase`, `stage`, or `label`. Null for unrelated events.
 */
export function generatedImageStage(
  event: Partial<ToolProgressEvent> | Record<string, any> | null | undefined,
): GeneratedImageStage | null {
  if (!event || typeof event !== "object") return null;
  const record = event as Record<string, any>;
  const candidates = [record.stage, record.phase, record.state, record.label];
  for (const candidate of candidates) {
    const token = normalizeToken(candidate);
    if (
      token === "requesting" ||
      token === "retrying" ||
      token === "generating" ||
      token === "saving"
    ) {
      return token;
    }
  }
  return null;
}

export function generatedImageStageLabel(stage: GeneratedImageStage): string {
  return IMAGE_STAGE_LABELS[stage];
}

const ASPECT_PRESETS: Record<string, number> = {
  square: 1,
  portrait: 2 / 3,
  landscape: 3 / 2,
  wide: 16 / 9,
};

/**
 * Aspect ratio (width/height) for a generate_image request, used to reserve
 * stable card space before pixels arrive. Reads an exact `size`
 * ("1536x1024") first, then an `aspect_ratio` preset; defaults to square.
 */
export function generatedImageAspectRatio(
  args: Record<string, unknown> | null | undefined,
): number {
  if (args && typeof args === "object") {
    const size = firstString((args as Record<string, any>).size);
    const match = size.match(/^(\d{2,5})\s*[xX×]\s*(\d{2,5})$/);
    if (match) {
      const width = Number(match[1]);
      const height = Number(match[2]);
      if (width > 0 && height > 0) return width / height;
    }
    const preset = normalizeToken((args as Record<string, any>).aspect_ratio ?? (args as Record<string, any>).aspectRatio);
    if (preset && ASPECT_PRESETS[preset]) return ASPECT_PRESETS[preset];
  }
  return 1;
}
