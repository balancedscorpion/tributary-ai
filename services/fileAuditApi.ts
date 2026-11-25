import type { Artifact } from '@/types';

export interface FileAuditRecord {
  id: string;
  artifactId: string;
  threadId: string;
  threadTitle?: string;
  title: string;
  source?: string;
  sourceLabel: string;
  sourceToolName?: string;
  toolName?: string;
  toolCallId?: string;
  fileKind?: string;
  mimeType?: string;
  byteSize?: number;
  contentStorage?: string;
  contentPath?: string;
  managedLocalPath?: string;
  previewText?: string;
  contentNote?: string;
  previewAvailable?: boolean;
  previewTruncated?: boolean;
  downloadable?: boolean;
  removable?: boolean;
  /**
   * Remote-reference request contract from the backend: whether the record
   * can be opened at all, whether the UI may offer an ask-agent download,
   * which connector tool that request should use, and the exact composer
   * prompt to insert. Absent on cached artifacts and legacy records.
   */
  openable?: boolean;
  canRequestDownload?: boolean;
  requestToolName?: string;
  requestToolArgs?: Record<string, unknown>;
  requestDownloadPrompt?: string;
  /**
   * False when the backend record is a remote-only reference: the file was
   * seen through a connector but its bytes are not cached locally, so the
   * native viewer, raw download, and preview are unavailable. Legacy records
   * without a cache marker are treated as cached.
   */
  cached: boolean;
  cacheState?: string;
  remoteUrl?: string;
  /** Connector-native path (e.g. SharePoint server-relative path) when the remote source is path-addressed. */
  remotePath?: string;
  remoteRef?: string;
  createdAt?: number;
  product?: string;
  family?: string;
  integration?: string;
  metadata?: Record<string, any>;
  artifact?: Artifact;
}

export interface FileAuditListOptions {
  threadId?: string | null;
  limit?: number;
  offset?: number;
  /**
   * Server-side search, sent as `q`: the backend filters titles, provenance,
   * paths, and previews across the whole audit before pagination.
   */
  query?: string | null;
  /**
   * Server-side source filter, sent as `source`: the record's source label
   * (the same value `fileSourceKey` derives — source_label falling back to
   * source), matched case-insensitively by the backend before pagination.
   */
  source?: string | null;
  /** Server-side status filter, sent as `remote_only=1`: only remote-only (uncached) references. */
  remoteOnly?: boolean;
  signal?: AbortSignal;
}

export interface FileAuditScopeMetadata {
  threadId?: string;
  limit: number;
  offset: number;
  nextOffset?: number;
  returned: number;
  hasMore: boolean;
  truncated: boolean;
  cachedFileLimitHit: boolean;
  remoteScanLimit?: number;
  remoteScanLimitHit: boolean;
  /**
   * Echo of the search query the backend actually applied. Absent when no
   * query was applied or on legacy backends that ignore `q` — the UI uses
   * this to detect whether the loaded records already reflect the search.
   */
  query?: string;
  /**
   * Echoes of the applied source/status filters, with the same role as the
   * query echo: when present and matching what the UI sent, the loaded
   * records already reflect the filter server-side (applied before
   * pagination); when absent, the UI falls back to client-side filtering
   * over loaded records.
   */
  source?: string;
  remoteOnly?: boolean;
  /**
   * Optional facet counts over the current result universe: per-source
   * buckets (query + status applied, source dimension excluded) and the
   * cached/remote-only status buckets (query + source applied, status
   * excluded). The backend sends source buckets as a `source_counts` list of
   * {key, label, count} and status buckets as `status_counts`
   * {total, cached, remote_only}; both normalize here. Absent on backends
   * that do not compute them — the UI then falls back to counting loaded
   * records where that is still honest.
   */
  sourceCounts?: Record<string, number>;
  remoteOnlyCount?: number;
  cachedCount?: number;
  /**
   * Records the backend enumerated in its scan window before pagination
   * slicing. Exact when no scan limit was hit; otherwise a lower bound on
   * how many files actually exist. Absent on legacy backends.
   */
  candidateCount?: number;
}

export interface FileAuditCountSummary {
  /** Whether the backend reports records beyond what has been loaded. */
  hasMore: boolean;
  /** Known available record count, or null when unknown or not larger than the loaded count. */
  available: number | null;
  /** True when `available` is bounded by a scan limit, so the real total may be higher. */
  availableIsLowerBound: boolean;
}

/**
 * Interprets scope metadata against the loaded record count so the UI can
 * distinguish "loaded" from "available" without overpromising: `available`
 * is only reported when the backend enumerated more records than are loaded,
 * and it is flagged as a lower bound whenever a scan limit truncated the
 * enumeration.
 */
export function fileAuditCountSummary(
  scope: FileAuditScopeMetadata | null | undefined,
  loadedCount: number,
): FileAuditCountSummary {
  if (!scope) return { hasMore: false, available: null, availableIsLowerBound: false };
  const hasMore = Boolean(scope.hasMore || scope.truncated || (scope.nextOffset && scope.nextOffset > 0));
  if (!hasMore) return { hasMore: false, available: null, availableIsLowerBound: false };
  const bounded = Boolean(scope.cachedFileLimitHit || scope.remoteScanLimitHit);
  const candidate = scope.candidateCount;
  if (typeof candidate !== 'number' || candidate <= loadedCount) {
    return { hasMore: true, available: null, availableIsLowerBound: bounded };
  }
  return { hasMore: true, available: candidate, availableIsLowerBound: bounded };
}

export interface FileAuditListResult {
  records: FileAuditRecord[];
  scope: FileAuditScopeMetadata;
}

function isRecord(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function stringValue(...values: unknown[]): string | undefined {
  for (const value of values) {
    if (typeof value !== 'string') continue;
    const trimmed = value.trim();
    if (trimmed) return trimmed;
  }
  return undefined;
}

function numberValue(...values: unknown[]): number | undefined {
  for (const value of values) {
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value === 'string' && value.trim()) {
      const parsed = Number(value.trim());
      if (Number.isFinite(parsed)) return parsed;
    }
  }
  return undefined;
}

function boolValue(...values: unknown[]): boolean | undefined {
  for (const value of values) {
    if (typeof value === 'boolean') return value;
  }
  return undefined;
}

function recordValue(...values: unknown[]): Record<string, unknown> | undefined {
  for (const value of values) {
    if (isRecord(value)) return value;
  }
  return undefined;
}

// Source facet buckets arrive as a list of {key, label, count} from the
// backend; a plain {label: count} map is also accepted defensively. Entries
// without a usable label or a non-negative numeric count are dropped.
function sourceCountsValue(...values: unknown[]): Record<string, number> | undefined {
  for (const value of values) {
    if (Array.isArray(value)) {
      const out: Record<string, number> = {};
      for (const entry of value) {
        if (!isRecord(entry)) continue;
        const label = stringValue(entry.label, entry.key, entry.source);
        const count = numberValue(entry.count);
        if (label && count !== undefined && count >= 0) {
          out[label] = (out[label] || 0) + count;
        }
      }
      return out;
    }
    if (!isRecord(value)) continue;
    const out: Record<string, number> = {};
    for (const [key, raw] of Object.entries(value)) {
      const count = numberValue(raw);
      if (typeof key === 'string' && key.trim() && count !== undefined && count >= 0) {
        out[key.trim()] = count;
      }
    }
    return out;
  }
  return undefined;
}

function timestampValue(value: unknown): number | undefined {
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  if (typeof value === 'string' && value.trim()) {
    const ts = Date.parse(value);
    if (Number.isFinite(ts)) return ts;
  }
  return undefined;
}

function artifactFromRaw(value: unknown): Artifact | undefined {
  if (!isRecord(value)) return undefined;
  const id = stringValue(value.id, value.artifact_id);
  if (!id) return undefined;
  const type = stringValue(value.type) || 'file';
  return {
    id,
    type: type as Artifact['type'],
    title: stringValue(value.title) || 'File',
    content: typeof value.content === 'string' ? value.content : '',
    language: stringValue(value.language),
    metadata: isRecord(value.metadata) ? value.metadata : {},
  };
}

function normalizeFileAuditScope(raw: unknown, returned: number, requestedLimit: number, requestedOffset: number): FileAuditScopeMetadata {
  const scope = isRecord(raw) ? raw : {};
  const statusCounts = recordValue(scope.status_counts, scope.statusCounts);
  const limit = numberValue(scope.limit, requestedLimit) || requestedLimit;
  const offset = numberValue(scope.offset, requestedOffset) || requestedOffset;
  const nextOffset = numberValue(scope.next_offset, scope.nextOffset);
  const normalizedReturned = numberValue(scope.returned, scope.count, returned) || returned;
  const hasMore = boolValue(scope.has_more, scope.hasMore) || false;
  const truncated = boolValue(scope.truncated) || hasMore;
  return {
    threadId: stringValue(scope.thread_id, scope.threadId),
    limit,
    offset,
    nextOffset,
    returned: normalizedReturned,
    hasMore,
    truncated,
    cachedFileLimitHit: boolValue(scope.cached_file_limit_hit, scope.cachedFileLimitHit) || false,
    remoteScanLimit: numberValue(scope.remote_scan_limit, scope.remoteScanLimit),
    remoteScanLimitHit: boolValue(scope.remote_scan_limit_hit, scope.remoteScanLimitHit) || false,
    query: stringValue(scope.query, scope.q),
    source: stringValue(scope.source, scope.source_filter, scope.sourceFilter),
    remoteOnly: boolValue(scope.remote_only, scope.remoteOnly),
    sourceCounts: sourceCountsValue(scope.source_counts, scope.sourceCounts),
    remoteOnlyCount: numberValue(
      statusCounts?.remote_only,
      statusCounts?.remoteOnly,
      scope.remote_only_count,
      scope.remoteOnlyCount,
    ),
    cachedCount: numberValue(statusCounts?.cached, scope.cached_count, scope.cachedCount),
    candidateCount: numberValue(scope.candidate_count, scope.candidateCount),
  };
}

export function normalizeFileAuditRecord(raw: unknown): FileAuditRecord | null {
  if (!isRecord(raw)) return null;
  const id = stringValue(raw.id, raw.artifact_id);
  if (!id) return null;
  const artifact = artifactFromRaw(raw.artifact);
  // Cache marker precedence: explicit boolean, then availability/cache-state
  // strings ("cached" vs "remote_only"/"remote"), then a remote_only flag.
  // Legacy records carry none of these and default to cached.
  const cachedFlag = boolValue(raw.cached);
  const cacheState = stringValue(raw.availability, raw.cache_state, raw.cacheState);
  const remoteOnly = boolValue(raw.remote_only, raw.remoteOnly);
  const cached = cachedFlag !== undefined
    ? cachedFlag
    : cacheState !== undefined
      ? cacheState === 'cached'
      : remoteOnly === undefined ? true : !remoteOnly;
  return {
    id,
    artifactId: stringValue(raw.artifact_id, raw.artifactId, raw.id) || id,
    threadId: stringValue(raw.thread_id, raw.threadId) || '',
    threadTitle: stringValue(raw.thread_title, raw.threadTitle),
    title: stringValue(raw.title, artifact?.title) || 'File',
    source: stringValue(raw.source),
    sourceLabel: stringValue(raw.source_label, raw.sourceLabel) || 'File artifact',
    sourceToolName: stringValue(raw.source_tool_name, raw.sourceToolName),
    toolName: stringValue(raw.tool_name, raw.toolName),
    toolCallId: stringValue(raw.tool_call_id, raw.toolCallId),
    fileKind: stringValue(raw.file_kind, raw.fileKind),
    mimeType: stringValue(raw.mime_type, raw.mimeType),
    byteSize: numberValue(raw.byte_size, raw.byteSize),
    contentStorage: stringValue(raw.content_storage, raw.contentStorage),
    contentPath: stringValue(raw.content_path, raw.contentPath),
    managedLocalPath: stringValue(raw.managed_local_path, raw.managedLocalPath),
    previewText: stringValue(raw.preview_text, raw.previewText),
    contentNote: stringValue(raw.content_note, raw.contentNote),
    previewAvailable: boolValue(raw.preview_available, raw.previewAvailable),
    previewTruncated: boolValue(raw.preview_truncated, raw.previewTruncated),
    downloadable: boolValue(raw.downloadable),
    removable: boolValue(raw.removable),
    openable: boolValue(raw.openable),
    canRequestDownload: boolValue(raw.can_request_download, raw.canRequestDownload),
    requestToolName: stringValue(raw.request_tool_name, raw.requestToolName),
    requestToolArgs: recordValue(raw.request_tool_args, raw.requestToolArgs),
    requestDownloadPrompt: stringValue(raw.request_download_prompt, raw.requestDownloadPrompt),
    cached,
    cacheState,
    remoteUrl: stringValue(raw.remote_url, raw.remoteUrl, raw.source_url, raw.external_url, raw.web_url),
    remotePath: stringValue(raw.remote_path, raw.remotePath),
    remoteRef: stringValue(raw.remote_ref, raw.remoteRef, raw.remote_id, raw.external_id),
    createdAt: timestampValue(raw.created_at ?? raw.createdAt),
    product: stringValue(raw.product),
    family: stringValue(raw.family),
    integration: stringValue(raw.integration),
    metadata: isRecord(raw.metadata) ? raw.metadata : undefined,
    artifact,
  };
}

async function readJson(res: Response): Promise<any> {
  const text = await res.text().catch(() => '');
  if (!text.trim()) return null;
  try {
    return JSON.parse(text);
  } catch {
    return { error: text };
  }
}

/**
 * The file audit routes are not deployed in this build. Distinct from a load
 * failure so the UI can render a calm capability notice instead of an error.
 */
export class FileAuditApiUnavailableError extends Error {
  constructor(message = 'The files service is not available in this build.') {
    super(message);
    this.name = 'FileAuditApiUnavailableError';
  }
}

export function isFileAuditUnavailable(error: unknown): boolean {
  return error instanceof FileAuditApiUnavailableError;
}

export async function listFileAuditRecords(options: FileAuditListOptions = {}): Promise<FileAuditListResult> {
  const limit = Math.min(Math.max(Math.trunc(options.limit || 100), 1), 500);
  const offset = Math.max(Math.trunc(options.offset || 0), 0);
  const threadId = stringValue(options.threadId || '');
  const query = stringValue(options.query || '');
  const source = stringValue(options.source || '');
  const params = new URLSearchParams({ limit: String(limit) });
  if (offset > 0) params.set('offset', String(offset));
  if (query) params.set('q', query);
  if (source) params.set('source', source);
  if (options.remoteOnly) params.set('remote_only', '1');
  const url = threadId
    ? `/api/threads/${encodeURIComponent(threadId)}/files?${params.toString()}`
    : `/api/files?${params.toString()}`;
  const res = await fetch(url, {
    method: 'GET',
    headers: { Accept: 'application/json' },
    signal: options.signal,
  });
  // 404/501 on the whole-audit route means the backend predates the files
  // service; a thread-scoped 404 can also mean the route is absent. Either
  // way the surface should degrade to a notice, never a dead red error.
  if (res.status === 404 || res.status === 501) {
    throw new FileAuditApiUnavailableError();
  }
  const data = await readJson(res);
  if (!res.ok) {
    throw new Error(data?.error || data?.message || `Failed to load files (HTTP ${res.status})`);
  }
  const files = Array.isArray(data?.files) ? data.files : [];
  const records = files.map(normalizeFileAuditRecord).filter(Boolean) as FileAuditRecord[];
  return {
    records,
    scope: normalizeFileAuditScope(data?.scope, records.length, limit, offset),
  };
}

export async function loadFileAuditArtifact(artifactId: string, signal?: AbortSignal): Promise<Artifact> {
  const id = stringValue(artifactId);
  if (!id) throw new Error('Missing artifact id.');
  const res = await fetch(`/api/artifacts/${encodeURIComponent(id)}`, {
    method: 'GET',
    headers: { Accept: 'application/json' },
    signal,
  });
  if (res.status === 404) {
    throw new Error('This file is no longer stored. It may have been removed.');
  }
  const data = await readJson(res);
  if (!res.ok) {
    throw new Error(data?.error || data?.message || `Failed to load file (HTTP ${res.status})`);
  }
  const artifact = artifactFromRaw(data?.artifact);
  if (!artifact) throw new Error('The server did not return a file artifact.');
  return artifact;
}

export async function deleteFileAuditArtifact(artifactId: string): Promise<void> {
  const id = stringValue(artifactId);
  if (!id) throw new Error('Missing artifact id.');
  const res = await fetch(`/api/artifacts/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
  });
  const data = await readJson(res);
  if (!res.ok) {
    throw new Error(data?.error || data?.message || `Failed to remove file (HTTP ${res.status})`);
  }
}
