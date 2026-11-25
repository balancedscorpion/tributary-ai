import { fileArtifactMimeType } from "@/lib/fileProvenance";
import { isGeneratedImageArtifact } from "@/lib/generatedImageArtifacts";
import type { Artifact } from "@/types";

const EXTENSION_BY_LANGUAGE: Record<string, string> = {
  bash: "sh",
  css: "css",
  csv: "csv",
  go: "go",
  htm: "html",
  html: "html",
  javascript: "js",
  jpg: "jpg",
  jpeg: "jpg",
  json: "json",
  markdown: "md",
  md: "md",
  png: "png",
  python: "py",
  sh: "sh",
  sql: "sql",
  svg: "svg",
  text: "txt",
  ts: "ts",
  tsx: "tsx",
  txt: "txt",
  typescript: "ts",
  xml: "xml",
  yaml: "yaml",
  yml: "yml",
};

const EXTENSION_BY_MIME: Record<string, string> = {
  "application/json": "json",
  "application/javascript": "js",
  "application/pdf": "pdf",
  "image/gif": "gif",
  "image/jpeg": "jpg",
  "image/png": "png",
  "image/svg+xml": "svg",
  "image/webp": "webp",
  "text/css": "css",
  "text/csv": "csv",
  "text/html": "html",
  "text/javascript": "js",
  "text/markdown": "md",
  "text/plain": "txt",
  "text/tab-separated-values": "tsv",
  "text/xml": "xml",
};

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === "string" && value.trim()) return value.trim();
  }
  return "";
}

function normalizeMimeType(value: unknown): string {
  return String(value || "")
    .split(";")[0]
    .trim()
    .toLowerCase();
}

function inferMimeTypeFromDataUri(content: string): string {
  const match = content.match(/^data:([^;,]+)[;,]/i);
  return normalizeMimeType(match?.[1]);
}

function getArtifactMimeType(artifact: Artifact): string {
  // File artifacts resolve MIME through the same alias chain as FileRenderer
  // and provenance: top-level MIME keys first, then connector-native keys
  // nested in result_metadata (Slack `mimetype`, ...), so download type and
  // extension inference match what the viewer opens. Scoped to file-typed
  // artifacts because the shared helper also sniffs JSON `content`, which
  // must not re-type data/code artifacts whose payload carries MIME-like fields.
  if (artifact.type === "file") {
    const shared = normalizeMimeType(fileArtifactMimeType(artifact));
    if (shared) return shared;
  }
  const metadata = artifact.metadata || {};
  const explicit = normalizeMimeType(
    firstString(
      metadata.mimeType,
      metadata.mime_type,
      metadata.detectedMimeType,
      metadata.detected_mime_type,
    ),
  );
  if (explicit) return explicit;

  const content = String(artifact.content || "");
  const dataUriMime = inferMimeTypeFromDataUri(content);
  if (dataUriMime) return dataUriMime;

  const language = String(artifact.language || "").trim().toLowerCase();
  if (language === "html" || language === "htm") return "text/html";
  if (language === "json") return "application/json";
  if (language === "csv") return "text/csv";
  if (language === "svg") return "image/svg+xml";
  if (artifact.type === "data") return "application/json";
  // A generated/attached image with no MIME signal is overwhelmingly PNG;
  // application/octet-stream would strip the extension off the download.
  if (artifact.type === "image") return "image/png";
  return "text/plain";
}

function hasFileExtension(fileName: string): boolean {
  return /\.[a-z0-9]{1,12}$/i.test(fileName);
}

function extensionForArtifact(artifact: Artifact, mimeType: string): string {
  const language = String(artifact.language || "").trim().toLowerCase();
  if (language && EXTENSION_BY_LANGUAGE[language]) {
    return EXTENSION_BY_LANGUAGE[language];
  }
  if (mimeType && EXTENSION_BY_MIME[mimeType]) {
    return EXTENSION_BY_MIME[mimeType];
  }
  if (artifact.type === "data") return "json";
  if (artifact.type === "code") return "txt";
  return "";
}

function sanitizeFileName(fileName: string): string {
  const cleaned = fileName
    .replace(/[/\\?%*:|"<>]/g, "-")
    .replace(/[\u0000-\u001f\u007f]/g, "")
    .replace(/\s+/g, " ")
    .trim();
  return cleaned || "artifact";
}

export function getArtifactDownloadFileName(artifact: Artifact): string {
  const metadata = artifact.metadata || {};
  const mimeType = getArtifactMimeType(artifact);
  const rawName = firstString(
    metadata.download_name,
    metadata.downloadName,
    metadata.file_name,
    metadata.fileName,
    artifact.title,
    artifact.id,
  );
  let fileName = sanitizeFileName(rawName || "artifact");
  if (!hasFileExtension(fileName)) {
    const ext = extensionForArtifact(artifact, mimeType);
    if (ext) fileName = `${fileName}.${ext}`;
  }
  return fileName;
}

function decodeBase64(raw: string): ArrayBuffer {
  const decoded = atob(raw.replace(/\s+/g, ""));
  const buffer = new ArrayBuffer(decoded.length);
  const bytes = new Uint8Array(buffer);
  for (let i = 0; i < decoded.length; i += 1) {
    bytes[i] = decoded.charCodeAt(i);
  }
  return buffer;
}

function blobFromDataUri(dataUri: string): Blob | null {
  const match = dataUri.match(/^data:([^;,]+)?(;base64)?,(.*)$/is);
  if (!match) return null;
  const mimeType = normalizeMimeType(match[1]) || "application/octet-stream";
  const isBase64 = Boolean(match[2]);
  const payload = match[3] || "";
  if (isBase64) {
    return new Blob([decodeBase64(payload)], { type: mimeType });
  }
  try {
    return new Blob([decodeURIComponent(payload)], { type: mimeType });
  } catch {
    return new Blob([payload], { type: mimeType });
  }
}

export function isHtmlArtifact(artifact: Artifact | null | undefined): boolean {
  if (!artifact) return false;
  const language = String(artifact.language || "").trim().toLowerCase();
  const renderMode = String(artifact.metadata?.render_mode || "")
    .trim()
    .toLowerCase();
  const mimeType = getArtifactMimeType(artifact);
  const title = String(artifact.title || "").trim().toLowerCase();
  return (
    (artifact.type === "code" || artifact.type === "file") &&
    (renderMode === "sandboxed_html" ||
      language === "html" ||
      language === "htm" ||
      mimeType === "text/html" ||
      title.endsWith(".html") ||
      title.endsWith(".htm"))
  );
}

export function canDownloadArtifact(
  artifact: Artifact | null | undefined,
): artifact is Artifact {
  return (
    !!artifact &&
    artifact.metadata?.placeholder !== true &&
    typeof artifact.content === "string" &&
    artifact.content.length > 0
  );
}

function triggerBlobDownload(blob: Blob, fileName: string): void {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = fileName;
  document.body.appendChild(link);
  link.click();
  link.remove();
  window.setTimeout(() => URL.revokeObjectURL(url), 0);
}

export function downloadArtifact(artifact: Artifact): void {
  const mimeType = getArtifactMimeType(artifact);
  const content = String(artifact.content || "");
  const contentEncoding = String(artifact.metadata?.content_encoding || "")
    .trim()
    .toLowerCase();
  const blob =
    blobFromDataUri(content) ||
    (contentEncoding === "base64"
      ? new Blob([decodeBase64(content)], { type: mimeType })
      : new Blob([content], { type: mimeType }));

  triggerBlobDownload(blob, getArtifactDownloadFileName(artifact));
}

export function artifactRawUrl(artifactId: string): string {
  return `/api/artifacts/${encodeURIComponent(artifactId)}/raw`;
}

/**
 * First-party uploaded-file artifacts store preview text in `content`; the
 * original bytes live behind /api/artifacts/{id}/raw. Those must download via
 * the raw endpoint, never the preview. Mirrors the server-side
 * fileartifact.IsFileAttachmentArtifact gate (which also decides what /raw
 * serves), so file-typed artifacts that are NOT stored attachments — e.g.
 * export_thread bundles whose full payload lives in `content` — keep the
 * content download path.
 */
export function isRawDownloadArtifact(
  artifact: Artifact | null | undefined,
): artifact is Artifact {
  // Generated images are canonical `type:"image"` artifacts whose `content` is
  // a (possibly externalized) data URI; their original bytes must come from
  // /raw so the user gets the provider's encoding, not a browser re-encode.
  if (isGeneratedImageArtifact(artifact)) return true;
  if (!artifact || artifact.type !== "file") return false;
  const metadata = (artifact.metadata || {}) as Record<string, any>;
  if (metadata.placeholder === true) return false;
  const toolName = String(metadata.tool_name || metadata.toolName || "")
    .trim()
    .toLowerCase();
  if (toolName === "thread_file_attachment") return true;
  if (metadata.externalToolFile === true || metadata.external_tool_file === true) {
    return true;
  }
  const source = String(metadata.source || "").trim().toLowerCase();
  return source === "chat_upload" || source === "external_tool";
}

/**
 * Downloads the original bytes of a stored attachment artifact without
 * navigating: fetch same-origin → Blob → object-URL anchor click → revoke.
 * Throws on any failure so callers can surface it.
 */
export async function downloadArtifactRawBytes(
  artifactId: string,
  fileName: string,
): Promise<void> {
  const response = await fetch(artifactRawUrl(artifactId), {
    credentials: "same-origin",
  });
  if (!response.ok) {
    throw new Error(`Download failed (HTTP ${response.status})`);
  }
  const blob = await response.blob();
  triggerBlobDownload(blob, sanitizeFileName(fileName));
}
