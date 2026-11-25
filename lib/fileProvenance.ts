import type { FileAuditRecord } from '@/services/fileAuditApi';
import type { Artifact } from '@/types';
import {
  IMAGE_GENERATION_TOOL_NAME,
  getGeneratedImageMetadata,
  isGeneratedImageArtifact,
} from '@/lib/generatedImageArtifacts';

export interface FileArtifactProvenance {
  artifactId: string;
  title: string;
  sourceLabel: string;
  source?: string;
  sourceToolName?: string;
  toolName?: string;
  toolCallId?: string;
  threadId?: string;
  threadTitle?: string;
  fileKind?: string;
  mimeType?: string;
  byteSize?: number;
  contentStorage?: string;
  contentPath?: string;
  managedLocalPath?: string;
  downloadable?: boolean;
  /** False only for remote-only references with no cached bytes. */
  cached?: boolean;
  /** Backend gate for whether the audit/history row can be removed. */
  removable?: boolean;
  /** Backend gate for the ask-agent download; absent on cached/legacy records. */
  canRequestDownload?: boolean;
  remoteUrl?: string;
  /** Connector-native path (e.g. SharePoint server-relative path) when the remote source is path-addressed. */
  remotePath?: string;
  remoteRef?: string;
  toolArgs?: Record<string, unknown>;
  createdAt?: number;
}

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value !== 'string') continue;
    const text = value.trim();
    if (text) return text;
  }
  return '';
}

function firstDisplayString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === 'string') {
      const text = value.trim();
      if (text) return text;
      continue;
    }
    if (typeof value === 'number' && Number.isFinite(value)) return String(value);
  }
  return '';
}

function firstNumber(...values: unknown[]): number | undefined {
  for (const value of values) {
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value !== 'string') continue;
    const trimmed = value.trim();
    if (!trimmed) continue;
    const parsed = Number(trimmed);
    if (Number.isFinite(parsed)) return parsed;
  }
  return undefined;
}

function firstBoolean(...values: unknown[]): boolean | undefined {
  for (const value of values) {
    if (typeof value === 'boolean') return value;
    if (typeof value !== 'string') continue;
    const normalized = value.trim().toLowerCase();
    if (normalized === 'true') return true;
    if (normalized === 'false') return false;
  }
  return undefined;
}

function firstTimestamp(...values: unknown[]): number | undefined {
  for (const value of values) {
    if (typeof value === 'number' && Number.isFinite(value)) {
      return value > 0 && value < 10_000_000_000 ? value * 1000 : value;
    }
    if (typeof value !== 'string') continue;
    const trimmed = value.trim();
    if (!trimmed) continue;
    const numeric = Number(trimmed);
    if (Number.isFinite(numeric)) return numeric > 0 && numeric < 10_000_000_000 ? numeric * 1000 : numeric;
    const parsed = Date.parse(trimmed);
    if (Number.isFinite(parsed)) return parsed;
  }
  return undefined;
}

function isPlainRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function field(record: Record<string, unknown> | undefined, ...keys: string[]): unknown {
  if (!record) return undefined;
  for (const key of keys) {
    if (!Object.prototype.hasOwnProperty.call(record, key)) continue;
    const value = record[key];
    if (value === undefined || value === null) continue;
    if (typeof value === 'string' && !value.trim()) continue;
    return value;
  }
  return undefined;
}

function firstRecord(...values: unknown[]): Record<string, unknown> | undefined {
  for (const value of values) {
    if (isPlainRecord(value)) return value;
  }
  return undefined;
}

function contentRecord(content: string | undefined): Record<string, unknown> | undefined {
  const trimmed = String(content || '').trim();
  if (!trimmed || !trimmed.startsWith('{')) return undefined;
  try {
    const parsed = JSON.parse(trimmed);
    return isPlainRecord(parsed) ? parsed : undefined;
  } catch {
    return undefined;
  }
}

export function fileSourceKey(record: Pick<FileAuditRecord, 'sourceLabel' | 'source'>): string {
  return firstString(record.sourceLabel, record.source) || 'File artifact';
}

export function fileSourceBadgeClass(sourceLabel: string): string {
  const label = sourceLabel.toLowerCase();
  if (label.includes('chat')) return 'border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300';
  if (label.includes('sharepoint')) return 'border-blue-500/30 bg-blue-500/10 text-blue-700 dark:text-blue-300';
  if (label.includes('jira') || label.includes('atlassian')) return 'border-indigo-500/30 bg-indigo-500/10 text-indigo-700 dark:text-indigo-300';
  if (label.includes('slack')) return 'border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300';
  if (label.includes('github')) return 'border-neutral-500/30 bg-neutral-500/10 text-neutral-700 dark:text-neutral-300';
  if (label.includes('arxiv')) return 'border-rose-500/30 bg-rose-500/10 text-rose-700 dark:text-rose-300';
  // Generated images share the app's purple image affordance (ArtifactCard,
  // ChatInput). No connector source uses purple, so there is no collision.
  if (label.includes('generated')) return 'border-purple-500/30 bg-purple-500/10 text-purple-700 dark:text-purple-300';
  return 'border-border bg-muted/40 text-muted-foreground';
}

export function fileSourceDotClass(sourceLabel: string): string {
  const label = sourceLabel.toLowerCase();
  if (label.includes('chat')) return 'bg-sky-500';
  if (label.includes('sharepoint')) return 'bg-blue-500';
  if (label.includes('jira') || label.includes('atlassian')) return 'bg-indigo-500';
  if (label.includes('slack')) return 'bg-emerald-500';
  if (label.includes('github')) return 'bg-neutral-500';
  if (label.includes('arxiv')) return 'bg-rose-500';
  if (label.includes('generated')) return 'bg-purple-500';
  return 'bg-muted-foreground';
}

function toolArgsFromMetadata(metadata: Record<string, any> | undefined): Record<string, unknown> | undefined {
  const raw = metadata?.tool_args ?? metadata?.toolArgs;
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return undefined;
  return raw as Record<string, unknown>;
}

function toolArgsFromRecords(...records: Array<Record<string, unknown> | undefined>): Record<string, unknown> | undefined {
  for (const record of records) {
    const raw = field(record, 'tool_args', 'toolArgs', 'request_tool_args', 'requestToolArgs');
    if (isPlainRecord(raw)) return raw;
  }
  return undefined;
}

function sourceLabelFromTokens(...tokens: unknown[]): string {
  const joined = tokens
    .map((token) => firstDisplayString(token))
    .filter(Boolean)
    .join(' ')
    .toLowerCase();
  if (!joined) return '';
  // Generated image markers win first: `generate_image` embeds "generat" and
  // must never be mistaken for another source by a later substring match.
  if (joined.includes('generated') || joined.includes('generated_image') || joined.includes('image_generation')) return 'Generated';
  if (joined.includes('chat') || joined.includes('upload') || joined.includes('thread_file')) return 'Chat upload';
  if (joined.includes('sharepoint')) return 'SharePoint';
  if (joined.includes('jira')) return 'Jira';
  if (joined.includes('confluence')) return 'Confluence';
  if (joined.includes('atlassian')) return 'Atlassian';
  if (joined.includes('slack')) return 'Slack';
  if (joined.includes('github')) return 'GitHub';
  if (joined.includes('arxiv')) return 'arXiv';
  return '';
}

const FILE_MIME_TYPE_KEYS = [
  'detectedMimeType', 'detected_mime_type',
  'normalizedMimeType', 'normalized_mime_type',
  'mimeType', 'mime_type',
  'mimetype', 'mime',
  'contentType', 'content_type',
];

// `kind` last: tool envelopes use it for the generic result kind ('file'),
// so connector-specific keys like Slack's `filetype` must win over it.
const FILE_KIND_KEYS = [
  'fileKind', 'file_kind',
  'fileType', 'file_type', 'filetype',
  'kind',
];

function artifactMetadataRecords(artifact: Artifact | null | undefined): Array<Record<string, unknown> | undefined> {
  if (!artifact) return [];
  const metadata = isPlainRecord(artifact.metadata) ? artifact.metadata : {};
  return [
    metadata,
    firstRecord(field(metadata, 'result_metadata', 'resultMetadata')),
    contentRecord(artifact.content),
  ];
}

/**
 * MIME type for a file artifact across backend-normalized top-level keys and
 * connector-native shapes nested in result_metadata / JSON content (Slack
 * `mimetype`, HTTP-ish `content_type`, ...). Shared by the native file
 * renderer and provenance so both route a direct-open artifact the same way.
 */
export function fileArtifactMimeType(artifact: Artifact | null | undefined): string {
  return firstString(
    ...artifactMetadataRecords(artifact).map((record) => field(record, ...FILE_MIME_TYPE_KEYS)),
  );
}

/**
 * File kind (pdf, image, spreadsheet, ...) with the same fallback chain as
 * fileArtifactMimeType. Generic 'file' placeholders (e.g. the envelope `kind`)
 * are skipped so they cannot mask a real kind in a lower-priority key.
 */
export function fileArtifactFileKind(artifact: Artifact | null | undefined): string {
  for (const record of artifactMetadataRecords(artifact)) {
    if (!record) continue;
    for (const key of FILE_KIND_KEYS) {
      if (!Object.prototype.hasOwnProperty.call(record, key)) continue;
      const value = record[key];
      if (typeof value !== 'string') continue;
      const text = value.trim();
      if (!text || text.toLowerCase() === 'file') continue;
      return text;
    }
  }
  return '';
}

function remoteOnlyFrom(...values: unknown[]): boolean {
  return values.some((value) => firstString(value).toLowerCase() === 'remote_only');
}

const REMOTE_URL_KEYS = [
  'remoteUrl', 'remote_url',
  'sourceUrl', 'source_url',
  'itemUrl', 'item_url',
  'webUrl', 'web_url',
  'url',
  'downloadUrl', 'download_url',
  'urlPrivateDownload', 'url_private_download',
  'urlPrivate', 'url_private',
  'permalink',
  'pdfUrl', 'pdf_url',
  'absUrl', 'abs_url',
];

const REMOTE_PATH_KEYS = [
  'remotePath', 'remote_path',
  'serverRelativeUrl', 'server_relative_url',
  'drivePath', 'drive_path',
  'path',
  'filePath', 'file_path',
];

const REMOTE_REF_KEYS = [
  'remoteRef', 'remote_ref',
  'remoteId', 'remote_id',
  'fileId', 'file_id',
  'attachmentId', 'attachment_id',
  'arxivId', 'arxiv_id',
  'itemId', 'item_id',
  'driveItemId', 'drive_item_id',
];

/**
 * Provenance for a generated `type:"image"` artifact. The Files surface and the
 * ArtifactPanel provenance block treat generated images as first-class files
 * (one canonical artifact, no paired file record), so this fills the same
 * FileArtifactProvenance shape from generated-image metadata — sourceLabel
 * "Generated", fileKind "image", downloadable/cached true — without a
 * FileAuditRecord. Fields the record-less path cannot honestly know (remote
 * locators, contentPath) are left empty.
 */
function generatedImageProvenance(
  artifact: Artifact,
  options: { downloadable?: boolean },
): FileArtifactProvenance {
  const image = getGeneratedImageMetadata(artifact);
  const metadata = isPlainRecord(artifact.metadata) ? artifact.metadata : {};
  const title = firstString(
    artifact.title,
    field(metadata, 'title', 'name', 'file_name', 'fileName'),
    image.prompt,
    artifact.id,
  );
  const toolName = firstString(
    field(metadata, 'tool_name', 'toolName'),
    IMAGE_GENERATION_TOOL_NAME,
  );
  return {
    artifactId: artifact.id,
    title,
    sourceLabel: 'Generated',
    source: firstString(field(metadata, 'source'), 'generated_image'),
    sourceToolName: toolName,
    toolName,
    toolCallId: image.toolCallId || undefined,
    fileKind: 'image',
    mimeType: image.mimeType || undefined,
    byteSize: image.sizeBytes ?? undefined,
    downloadable: options.downloadable !== undefined ? options.downloadable : true,
    cached: true,
    createdAt: firstTimestamp(image.createdAt) ?? undefined,
  };
}

export function fileProvenanceFromArtifact(
  artifact: Artifact | null | undefined,
  options: { downloadable?: boolean } = {},
): FileArtifactProvenance | null {
  if (artifact && isGeneratedImageArtifact(artifact)) {
    return generatedImageProvenance(artifact, options);
  }
  if (!artifact || artifact.type !== 'file') return null;
  const metadata = isPlainRecord(artifact.metadata) ? artifact.metadata : {};
  const resultMetadata = firstRecord(field(metadata, 'result_metadata', 'resultMetadata'));
  const toolPresentation = firstRecord(field(metadata, 'tool_presentation', 'toolPresentation'));
  const content = contentRecord(artifact.content);
  const toolArgs = toolArgsFromRecords(metadata, resultMetadata, content);
  const title = firstString(
    artifact.title,
    field(metadata, 'title', 'name', 'file_name', 'fileName'),
    field(resultMetadata, 'title', 'name', 'file_name', 'fileName'),
    field(content, 'title', 'name', 'file_name', 'fileName'),
    artifact.id,
  );
  const sourceToolName = firstString(
    field(metadata, 'sourceToolName', 'source_tool_name'),
    field(resultMetadata, 'sourceToolName', 'source_tool_name'),
    field(content, 'sourceToolName', 'source_tool_name'),
    field(metadata, 'tool_name', 'toolName'),
    field(resultMetadata, 'tool_name', 'toolName'),
    field(content, 'tool_name', 'toolName'),
  );
  const toolName = firstString(
    field(metadata, 'tool_name', 'toolName'),
    field(resultMetadata, 'tool_name', 'toolName'),
    field(content, 'tool_name', 'toolName'),
    sourceToolName,
  );
  const explicitSourceLabel = firstString(
    field(metadata, 'sourceLabel', 'source_label'),
    field(resultMetadata, 'sourceLabel', 'source_label'),
    field(content, 'sourceLabel', 'source_label'),
  );
  const source = firstString(
    field(metadata, 'source'),
    field(resultMetadata, 'source'),
    field(content, 'source'),
  );
  const sourceLabel =
    explicitSourceLabel ||
    sourceLabelFromTokens(
      field(metadata, 'integration', 'product', 'family'),
      field(resultMetadata, 'integration', 'product', 'family'),
      field(toolPresentation, 'family'),
      source,
      sourceToolName,
      toolName,
    ) ||
    (source && !/^external[_\s-]?tool$/i.test(source) ? source : '') ||
    'File artifact';
  const fileKind = fileArtifactFileKind(artifact) || undefined;
  const mimeType = fileArtifactMimeType(artifact);
  const byteSize = firstNumber(
    field(metadata, 'sizeBytes', 'size_bytes', 'byteSize', 'byte_size', 'fileSize', 'file_size', 'bytes', 'size'),
    field(resultMetadata, 'sizeBytes', 'size_bytes', 'byteSize', 'byte_size', 'fileSize', 'file_size', 'bytes', 'size'),
    field(content, 'sizeBytes', 'size_bytes', 'byteSize', 'byte_size', 'fileSize', 'file_size', 'bytes', 'size'),
  );
  const managedLocalPath = firstString(
    field(metadata, 'managedLocalPath', 'managed_local_path', 'localPath', 'local_path'),
    field(resultMetadata, 'managedLocalPath', 'managed_local_path', 'localPath', 'local_path'),
    field(content, 'managedLocalPath', 'managed_local_path', 'localPath', 'local_path'),
  );
  const contentPath = firstString(
    field(metadata, 'contentPath', 'content_path', 'storagePath', 'storage_path'),
    field(resultMetadata, 'contentPath', 'content_path', 'storagePath', 'storage_path'),
    field(content, 'contentPath', 'content_path', 'storagePath', 'storage_path'),
    managedLocalPath,
  );
  const remoteUrl = firstString(
    field(metadata, ...REMOTE_URL_KEYS),
    field(resultMetadata, ...REMOTE_URL_KEYS),
    field(content, ...REMOTE_URL_KEYS),
    field(toolArgs, ...REMOTE_URL_KEYS),
  );
  const remotePath = firstString(
    field(metadata, ...REMOTE_PATH_KEYS),
    field(resultMetadata, ...REMOTE_PATH_KEYS),
    field(content, ...REMOTE_PATH_KEYS),
    field(toolArgs, ...REMOTE_PATH_KEYS),
  );
  const remoteRef = firstDisplayString(
    field(metadata, ...REMOTE_REF_KEYS),
    field(resultMetadata, ...REMOTE_REF_KEYS),
    field(content, ...REMOTE_REF_KEYS),
    field(toolArgs, ...REMOTE_REF_KEYS),
  );
  const downloadable = firstBoolean(
    field(metadata, 'downloadable', 'canDownload', 'can_download'),
    field(resultMetadata, 'downloadable', 'canDownload', 'can_download'),
    field(content, 'downloadable', 'canDownload', 'can_download'),
    options.downloadable,
  );
  const cached = remoteOnlyFrom(
    field(metadata, 'availability', 'record_kind', 'recordKind'),
    field(resultMetadata, 'availability', 'record_kind', 'recordKind'),
    field(content, 'availability', 'record_kind', 'recordKind'),
  )
    ? false
    : firstBoolean(
        field(metadata, 'cached', 'openable'),
        field(resultMetadata, 'cached', 'openable'),
        field(content, 'cached', 'openable'),
        true,
      );

  return {
    artifactId: artifact.id,
    title,
    sourceLabel,
    source,
    sourceToolName,
    toolName,
    toolCallId: firstString(
      field(metadata, 'toolCallId', 'tool_call_id'),
      field(resultMetadata, 'toolCallId', 'tool_call_id'),
      field(content, 'toolCallId', 'tool_call_id'),
    ),
    threadId: firstString(field(metadata, 'threadId', 'thread_id'), field(content, 'threadId', 'thread_id')),
    threadTitle: firstString(field(metadata, 'threadTitle', 'thread_title'), field(content, 'threadTitle', 'thread_title')),
    fileKind,
    mimeType,
    byteSize,
    contentStorage: firstString(
      field(metadata, 'contentStorage', 'content_storage'),
      field(resultMetadata, 'contentStorage', 'content_storage'),
      field(content, 'contentStorage', 'content_storage'),
    ),
    contentPath,
    managedLocalPath,
    downloadable,
    cached,
    removable: firstBoolean(field(metadata, 'removable'), field(content, 'removable')),
    canRequestDownload: firstBoolean(
      field(metadata, 'canRequestDownload', 'can_request_download'),
      field(resultMetadata, 'canRequestDownload', 'can_request_download'),
      field(content, 'canRequestDownload', 'can_request_download'),
    ),
    remoteUrl,
    remotePath,
    remoteRef,
    toolArgs,
    createdAt: firstTimestamp(
      field(metadata, 'createdAt', 'created_at', 'createdAtMs', 'created_at_ms'),
      field(resultMetadata, 'createdAt', 'created_at', 'createdAtMs', 'created_at_ms'),
      field(content, 'createdAt', 'created_at', 'createdAtMs', 'created_at_ms'),
    ),
  };
}

export function fileProvenanceFromRecord(record: FileAuditRecord): FileArtifactProvenance {
  return {
    artifactId: record.artifactId,
    title: record.title,
    sourceLabel: fileSourceKey(record),
    source: record.source,
    sourceToolName: record.sourceToolName,
    toolName: record.toolName,
    toolCallId: record.toolCallId,
    threadId: record.threadId,
    threadTitle: record.threadTitle,
    fileKind: record.fileKind,
    mimeType: record.mimeType,
    byteSize: record.byteSize,
    contentStorage: record.contentStorage,
    contentPath: record.contentPath,
    managedLocalPath: record.managedLocalPath,
    downloadable: record.downloadable,
    cached: record.cached,
    removable: record.removable,
    canRequestDownload: record.canRequestDownload,
    remoteUrl: record.remoteUrl,
    remotePath: record.remotePath,
    remoteRef: record.remoteRef,
    toolArgs: toolArgsFromMetadata(record.metadata),
    createdAt: record.createdAt,
  };
}

/**
 * Whether a remote locator is a web URL the browser can open directly, as
 * opposed to a connector-native path or bare ID that is copy-only.
 */
export function isHttpUrl(value: string | undefined): boolean {
  return /^https?:\/\//i.test(String(value || '').trim());
}

/**
 * Composer draft asking the agent to fetch a remote-only reference through
 * its originating connector. Inserted (not sent) so the user can edit it.
 */
export function buildRemoteFileDownloadPrompt(record: FileAuditRecord): string {
  // The backend's connector-specific instruction wins verbatim; the generic
  // rebuild below only covers legacy records that predate it.
  const backendPrompt = firstString(record.requestDownloadPrompt);
  if (backendPrompt) return backendPrompt;
  const title = firstString(record.title) || 'the file';
  const source = fileSourceKey(record);
  const tool = firstString(record.requestToolName, record.sourceToolName, record.toolName);
  const locator = firstString(record.remoteUrl, record.remotePath, record.remoteRef);
  const parts = [`Download and cache "${title}" from ${source} so it can be opened in Files.`];
  if (locator) parts.push(`Remote reference: ${locator}`);
  if (tool) parts.push(`It was surfaced by the ${tool} tool.`);
  return parts.join(' ');
}
