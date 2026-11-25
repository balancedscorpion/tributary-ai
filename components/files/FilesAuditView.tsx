import React from 'react';
import {
  ChevronRight,
  Cloud,
  CloudDownload,
  Download,
  ExternalLink,
  Files,
  FileText,
  Loader2,
  Plus,
  RefreshCw,
  Search,
  SearchX,
  Trash2,
  X,
} from 'lucide-react';
import { toast } from 'sonner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible';
import FileProvenanceDetails from '@/components/files/FileProvenanceDetails';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import type { Artifact } from '@/types';
import { fileArtifactCardPresentation } from '@/lib/attachmentPresentation';
import { downloadArtifactRawBytes, getArtifactDownloadFileName } from '@/lib/artifactDownload';
import {
  fileProvenanceFromRecord,
  fileSourceBadgeClass,
  fileSourceDotClass,
  fileSourceKey,
  isHttpUrl,
  type FileArtifactProvenance,
} from '@/lib/fileProvenance';
import { fileKindGlyph } from '@/lib/fileKindGlyph';
import {
  clearRemoteFileDownloadRequestsFor,
  getRemoteFileDownloadRequest,
  remoteFileDownloadRequestsSnapshot,
  subscribeRemoteFileDownloadRequests,
  subscribeRemoteFileDownloadRunCompleted,
  type RemoteFileDownloadRequest,
} from '@/lib/remoteFileDownloadRequests';
import { formatAbsoluteDateTime, formatBytes, formatRelativeTime } from '@/lib/formatters';
import { cn } from '@/lib/utils';
import {
  deleteFileAuditArtifact,
  fileAuditCountSummary,
  isFileAuditUnavailable,
  listFileAuditRecords,
  type FileAuditCountSummary,
  type FileAuditScopeMetadata,
  loadFileAuditArtifact,
  type FileAuditRecord,
} from '@/services/fileAuditApi';

export type FilesAuditScope = 'thread' | 'all';
export type FilesAuditLayout = 'settings' | 'page';

interface FilesAuditViewProps {
  currentThreadId?: string | null;
  currentThreadTitle?: string | null;
  onOpenArtifact: (artifact: Artifact, provenance?: FileArtifactProvenance) => void;
  onOpenOriginThread?: (threadId: string, toolCallId?: string) => void;
  /**
   * Ask the agent to download/cache a remote-only reference (e.g. by
   * inserting a composer prompt). The action is hidden when absent.
   */
  onRequestRemoteDownload?: (record: FileAuditRecord) => void;
  onCloseAfterOpen?: () => void;
  initialScope?: FilesAuditScope;
  lockScope?: FilesAuditScope;
  layout?: FilesAuditLayout;
  title?: string;
  description?: string;
  pageSize?: number;
}

function recordSearchText(record: FileAuditRecord): string {
  return [
    record.title,
    record.sourceLabel,
    record.sourceToolName,
    record.toolName,
    record.threadTitle,
    record.fileKind,
    record.mimeType,
    record.contentPath,
    record.managedLocalPath,
    record.remoteUrl,
    record.remotePath,
    record.remoteRef,
    record.previewText,
    record.contentNote,
  ].filter(Boolean).join('\n').toLowerCase();
}

function fileKindIcon(record: FileAuditRecord) {
  return fileKindGlyph(record.fileKind, record.mimeType, record.title);
}

function recordPreviewText(record: FileAuditRecord): string {
  if (record.cached !== false && record.artifact) {
    const presentation = fileArtifactCardPresentation(record.artifact);
    const hasLivePreview = Boolean(
      presentation.thumbnailUrl ||
        presentation.thumbnailFetchUrl ||
        presentation.thumbnailArtifactId ||
        presentation.pdfPreviewUrl ||
        presentation.htmlPreviewUrl ||
        presentation.htmlPreviewContent ||
        presentation.textPreviewFetchUrl,
    );
    if (hasLivePreview) return '';
    return presentation.previewText || presentation.contentNote || '';
  }
  return record.previewText || record.contentNote || '';
}

const MetaDot: React.FC = () => (
  <span aria-hidden="true" className="shrink-0 select-none text-muted-foreground/50">·</span>
);

// Coverage disclosure for the zero-match state: what the active narrowing
// actually looked at. Server-side search spans the whole audit, but the
// client-side query fallback and the source/status filters only ever see
// loaded records — the copy must not imply more than that.
function filteredEmptyCoverage(
  loaded: number,
  summary: FileAuditCountSummary,
  mode: 'search-matches' | 'client-search' | 'filters',
): string {
  const available = summary.available !== null
    ? `${summary.available}${summary.availableIsLowerBound ? '+' : ''}`
    : null;
  if (mode === 'search-matches') {
    const noun = loaded === 1 ? 'match' : 'matches';
    return available
      ? `Source and status filters only cover the ${loaded} loaded ${noun} — ${available} match the search.`
      : `Source and status filters only cover the ${loaded} loaded ${noun} — more matches exist.`;
  }
  const noun = loaded === 1 ? 'file' : 'files';
  const subject = mode === 'client-search' ? 'Search only covers' : 'Filters only cover';
  return available
    ? `${subject} the ${loaded} loaded ${noun} — ${available} exist.`
    : `${subject} the ${loaded} loaded ${noun} — more exist.`;
}

const DEFAULT_FILE_AUDIT_PAGE_SIZE = 150;

function fileAuditRecordKey(record: FileAuditRecord): string {
  return record.artifactId || record.id;
}

function appendUniqueFileAuditRecords(prev: FileAuditRecord[], next: FileAuditRecord[]): FileAuditRecord[] {
  if (next.length === 0) return prev;
  const seen = new Set(prev.map(fileAuditRecordKey));
  const out = [...prev];
  for (const record of next) {
    const key = fileAuditRecordKey(record);
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(record);
  }
  return out;
}

function useFileAudit(
  scope: FilesAuditScope,
  threadId: string | null,
  pageSize: number,
  query: string,
  source: string,
  remoteOnly: boolean,
) {
  const [records, setRecords] = React.useState<FileAuditRecord[]>([]);
  const [scopeMeta, setScopeMeta] = React.useState<FileAuditScopeMetadata | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [loadingMore, setLoadingMore] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const [unavailable, setUnavailable] = React.useState(false);
  const [refreshToken, setRefreshToken] = React.useState(0);
  const loadMoreAbortRef = React.useRef<AbortController | null>(null);

  React.useEffect(() => {
    if (scope === 'thread' && !threadId) {
      setRecords([]);
      setScopeMeta(null);
      setLoading(false);
      setLoadingMore(false);
      setError(null);
      setUnavailable(false);
      return;
    }
    const controller = new AbortController();
    loadMoreAbortRef.current?.abort();
    loadMoreAbortRef.current = null;
    setLoading(true);
    setLoadingMore(false);
    setError(null);
    setUnavailable(false);
    void listFileAuditRecords({
      threadId: scope === 'thread' ? threadId : null,
      limit: pageSize,
      offset: 0,
      query,
      source,
      remoteOnly,
      signal: controller.signal,
    }).then((next) => {
      setRecords(next.records);
      setScopeMeta(next.scope);
    }).catch((err: any) => {
      if (err?.name === 'AbortError') return;
      // A missing backend route is a capability notice, not a failure.
      if (isFileAuditUnavailable(err)) {
        setRecords([]);
        setScopeMeta(null);
        setUnavailable(true);
        return;
      }
      setError(err?.message || 'Failed to load files.');
    }).finally(() => {
      if (!controller.signal.aborted) setLoading(false);
    });
    return () => controller.abort();
    // A query/filter change restarts from offset 0, so pagination resets too.
  }, [scope, threadId, pageSize, query, source, remoteOnly, refreshToken]);

  React.useEffect(() => () => {
    loadMoreAbortRef.current?.abort();
  }, []);

  const loadMore = React.useCallback(() => {
    const nextOffset = scopeMeta?.nextOffset;
    if (!nextOffset || nextOffset <= 0 || loading || loadingMore) return;
    loadMoreAbortRef.current?.abort();
    const controller = new AbortController();
    loadMoreAbortRef.current = controller;
    setLoadingMore(true);
    setError(null);
    void listFileAuditRecords({
      threadId: scope === 'thread' ? threadId : null,
      limit: pageSize,
      offset: nextOffset,
      query,
      source,
      remoteOnly,
      signal: controller.signal,
    }).then((next) => {
      setRecords((prev) => appendUniqueFileAuditRecords(prev, next.records));
      setScopeMeta(next.scope);
    }).catch((err: any) => {
      if (err?.name !== 'AbortError') {
        setError(err?.message || 'Failed to load more files.');
      }
    }).finally(() => {
      if (!controller.signal.aborted) setLoadingMore(false);
      if (loadMoreAbortRef.current === controller) loadMoreAbortRef.current = null;
    });
  }, [loading, loadingMore, pageSize, query, remoteOnly, scope, scopeMeta?.nextOffset, source, threadId]);

  return {
    records,
    scopeMeta,
    loading,
    loadingMore,
    error,
    unavailable,
    refresh: () => setRefreshToken((value) => value + 1),
    loadMore,
    removeRecord: (artifactId: string) => {
      setRecords((prev) => prev.filter((record) => record.artifactId !== artifactId));
      setScopeMeta((prev) => {
        if (!prev?.nextOffset || prev.nextOffset <= 0) return prev;
        return { ...prev, nextOffset: Math.max(prev.nextOffset - 1, 0) };
      });
    },
  };
}

const FileAuditScopeStatus: React.FC<{
  meta: FileAuditScopeMetadata | null;
  visibleCount: number;
  totalLoaded: number;
  canLoadMore: boolean;
  loading: boolean;
  /** True when the loaded records are a server-side search/filter result set, so the counts describe matches rather than the whole audit. */
  narrowed: boolean;
  onLoadMore: () => void;
}> = ({ meta, visibleCount, totalLoaded, canLoadMore, loading, narrowed, onLoadMore }) => {
  const summary = fileAuditCountSummary(meta, totalLoaded);
  if (!summary.hasMore) return null;
  // "of 7" when the scan enumerated everything, "of 7+" when a scan limit
  // means the real total may be higher. Legacy backends without a candidate
  // count keep the count-free copy.
  const availableLabel = summary.available !== null
    ? `${summary.available}${summary.availableIsLowerBound ? '+' : ''}`
    : null;
  const unit = narrowed ? 'matches' : 'files';
  const loadedUnit = narrowed && totalLoaded === 1 ? 'match' : unit;
  const message = canLoadMore
    ? availableLabel
      ? `Showing ${totalLoaded} of ${availableLabel} ${unit}.`
      : `Showing ${totalLoaded} ${loadedUnit} so far. More are available.`
    : availableLabel
      ? `Showing ${totalLoaded} of ${availableLabel} ${unit}. More exist, but this view has reached the current limit.`
      : `Showing ${totalLoaded} ${loadedUnit}. More exist, but this view has reached the current limit.`;
  const filteredNote = visibleCount !== totalLoaded
    ? ` ${visibleCount} ${visibleCount === 1 ? 'file matches' : 'files match'} the current filters.`
    : '';
  return (
    <div
      className="flex flex-col gap-2 border-b border-border bg-muted/30 px-3 py-2 text-xs text-muted-foreground sm:flex-row sm:items-center sm:justify-between"
      data-file-audit-scope-status
    >
      <div className="min-w-0">
        <span className="font-medium text-foreground">{message}</span>
        {filteredNote && <span>{filteredNote}</span>}
      </div>
      {canLoadMore ? (
        // Not disabled while loading: disabling the focused button would blur
        // keyboard focus to <body> on every page. Re-clicks are no-ops (the
        // hook guards on loadingMore) and aria-busy reports the wait.
        <Button
          type="button"
          size="sm"
          variant="outline"
          className="h-8 shrink-0 gap-1.5 px-2.5 text-xs"
          onClick={onLoadMore}
          aria-busy={loading}
          aria-label={narrowed ? 'Load more matches' : 'Load more files'}
        >
          {loading ? (
            <Loader2 size={13} className="motion-safe:animate-spin" aria-hidden="true" />
          ) : (
            <Plus size={13} aria-hidden="true" />
          )}
          {/* The unit must agree with the "Showing N of M matches" copy
              beside it: paging a narrowed set loads matches, not the audit. */}
          {narrowed ? 'Load more matches' : 'Load more files'}
        </Button>
      ) : (
        <Badge variant="outline" className="w-fit shrink-0 border-border bg-muted/40 text-muted-foreground">
          Limit reached
        </Badge>
      )}
    </div>
  );
};

const SourceFilterChip: React.FC<{
  label: string;
  /**
   * Undefined hides the number entirely: without server facet counts, a
   * server-side source/status filter narrows the loaded universe so no
   * honest per-bucket count exists — a zero would read as "no such files".
   */
  count?: number;
  active: boolean;
  dotClass?: string;
  onClick: () => void;
}> = ({ label, count, active, dotClass, onClick }) => (
  <button
    type="button"
    aria-pressed={active}
    onClick={onClick}
    data-source-filter-chip={label}
    className={cn(
      'inline-flex h-7 min-w-0 max-w-full items-center gap-1.5 whitespace-nowrap rounded-full border px-2.5 text-xs',
      'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
      active
        ? 'border-primary/50 bg-primary/10 font-medium text-foreground'
        : 'border-border bg-background text-muted-foreground hover:bg-muted/40 hover:text-foreground',
    )}
  >
    {dotClass && (
      <span
        aria-hidden="true"
        className={cn('h-1.5 w-1.5 shrink-0 rounded-full', dotClass, count === 0 && 'opacity-50')}
      />
    )}
    <span className="min-w-0 truncate">{label}</span>
    {count !== undefined && (
      <>
        <span aria-hidden="true" className="text-[11px] tabular-nums text-muted-foreground">{count}</span>
        <span className="sr-only">({count} {count === 1 ? 'file' : 'files'})</span>
      </>
    )}
  </button>
);

const FileRow: React.FC<{
  record: FileAuditRecord;
  /** Session-scoped "the user already drafted a download request" memory. */
  downloadRequest?: RemoteFileDownloadRequest | null;
  onOpen: (record: FileAuditRecord) => void;
  onOpenOriginThread?: (threadId: string, toolCallId?: string) => void;
  onDownload: (record: FileAuditRecord) => void;
  onRequestRemoteDownload?: (record: FileAuditRecord) => void;
  onDelete: (record: FileAuditRecord) => void;
  onCopied?: (fieldLabel: string) => void;
}> = ({ record, downloadRequest, onOpen, onOpenOriginThread, onDownload, onRequestRemoteDownload, onDelete, onCopied }) => {
  const [detailsOpen, setDetailsOpen] = React.useState(false);
  const preview = recordPreviewText(record);
  const sourceLabel = fileSourceKey(record);
  const canOpenOrigin = Boolean(onOpenOriginThread && record.threadId);
  const remoteOnly = record.cached === false;
  // Backend gate for the ask-agent CTA; records without the flag (legacy)
  // stay eligible so the action does not vanish on old data.
  const canRequestDownload = remoteOnly && record.canRequestDownload !== false;
  const canRemove = record.removable !== false;
  // A remote-only file can't open in the native viewer, so its web URL is the
  // only view path — surface it as a row action. Connector paths and bare IDs
  // stay in the details rows (nothing for a browser to open).
  const remoteOpenUrl = remoteOnly && isHttpUrl(record.remoteUrl) ? record.remoteUrl : undefined;
  const removeAriaLabel = remoteOnly
    ? `Remove remote reference for ${record.title}`
    : `Remove ${record.title}`;
  const TitleIcon = fileKindIcon(record);
  // The requested state only ever annotates a requestable remote row: cached
  // rows render nothing (the request is moot) and blocked rows keep their
  // "can't download" wording.
  const requested = canRequestDownload ? downloadRequest || null : null;
  return (
    <div
      role="listitem"
      className="grid grid-cols-[minmax(0,1fr)_auto] gap-3 border-b border-border/70 px-3 py-3 last:border-b-0 hover:bg-muted/30"
      data-file-audit-row
      data-file-status={remoteOnly ? 'remote' : 'cached'}
      data-file-request-state={requested ? 'requested' : undefined}
    >
      <div className="min-w-0">
        <div className="flex min-w-0 items-center gap-2">
          {remoteOnly ? (
            // No cached bytes: the title is not an open affordance. Actions
            // stay explicit — request download, details, origin chat, remove.
            // The kind glyph stays (muted, not primary) so remote rows still
            // scan by file type; the amber badge carries the remote signal.
            <>
              <TitleIcon size={16} className="shrink-0 text-muted-foreground" aria-hidden="true" />
              <span
                className="min-w-0 truncate text-left text-sm font-medium text-foreground"
                title={record.title}
                data-file-title-text
              >
                {record.title}
              </span>
            </>
          ) : (
            // Icon and title form one open target so the row reads as "click
            // to view the file artifact", not just an underlined string.
            <button
              type="button"
              className="group flex min-w-0 items-center gap-2 rounded-sm text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
              onClick={() => onOpen(record)}
              aria-label={`Open ${record.title}`}
              title={`Open ${record.title} in the file viewer`}
              data-file-title-button
            >
              <TitleIcon size={16} className="shrink-0 text-primary" aria-hidden="true" />
              <span className="min-w-0 truncate text-sm font-medium text-foreground underline-offset-4 group-hover:underline">
                {record.title}
              </span>
            </button>
          )}
          {remoteOnly && (
            <Badge variant="outline" className="shrink-0 border-amber-500/30 bg-amber-500/10 text-[10px] text-amber-700 dark:text-amber-300">
              Remote only
            </Badge>
          )}
          {record.previewTruncated && (
            // Neutral on purpose: amber is reserved for the remote-only axis,
            // and a clipped preview is a display note, not an access state.
            <Badge variant="outline" className="shrink-0 border-border bg-muted/40 text-[10px] text-muted-foreground">
              Preview clipped
            </Badge>
          )}
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-x-1.5 gap-y-1 text-[11px] text-muted-foreground">
          <Badge variant="outline" className={`h-5 px-1.5 text-[10px] ${fileSourceBadgeClass(sourceLabel)}`}>
            {sourceLabel}
          </Badge>
          {record.threadTitle && (
            canOpenOrigin ? (
              <button
                type="button"
                className="max-w-full truncate rounded-sm underline-offset-4 hover:text-foreground hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
                onClick={() => onOpenOriginThread?.(record.threadId, record.toolCallId)}
                aria-label={`Open origin chat for ${record.title}`}
                title={record.threadTitle}
              >
                Chat: {record.threadTitle}
              </button>
            ) : (
              <span className="truncate" title={record.threadTitle}>Chat: {record.threadTitle}</span>
            )
          )}
          {/* Raw MIME stays out of the row (still searchable, shown in
              details); the human file kind plus aligned size/time is the
              scannable fact set. */}
          {record.fileKind && (
            <>
              <MetaDot />
              <span className="shrink-0">{record.fileKind}</span>
            </>
          )}
          {(record.byteSize !== undefined || record.createdAt) && (
            // Trailing cluster so size/time hug the row's right edge and the
            // eye can run down the list comparing them, instead of finding
            // them at a different offset behind every title.
            <span className="ml-auto flex shrink-0 items-center gap-x-1.5 pl-2 tabular-nums">
              {record.byteSize !== undefined && (
                <span className="shrink-0">{formatBytes(record.byteSize)}</span>
              )}
              {record.byteSize !== undefined && record.createdAt && <MetaDot />}
              {record.createdAt && (
                <span
                  className="shrink-0"
                  title={formatAbsoluteDateTime(record.createdAt)}
                  data-file-meta-time
                >
                  {formatRelativeTime(record.createdAt)}
                </span>
              )}
            </span>
          )}
        </div>
        {remoteOnly && (
          // Downloadability must be stated, not implied by a missing button:
          // a blocked reference is otherwise indistinguishable from broken UI.
          // Once a request is drafted, the note is the single carrier of that
          // memory ("you already asked") — the button label stays put, and no
          // badge is added (amber stays reserved for the remote axis). The
          // copy claims only what the app knows: a draft was created; it may
          // since have been edited, cleared, or sent.
          <p className="mt-1 text-xs leading-5 text-muted-foreground" data-remote-status-note>
            {requested ? (
              <>
                You drafted a download request
                {requested.threadTitle ? (
                  <> in <span className="font-medium text-foreground">{requested.threadTitle}</span></>
                ) : null}{' '}
                <span title={formatAbsoluteDateTime(requested.requestedAt)}>
                  {formatRelativeTime(requested.requestedAt)}
                </span>
                {requested.threadTitle
                  ? ' — send it there to download the file.'
                  : ' — send it from the composer to download the file.'}
              </>
            ) : canRequestDownload
              ? 'No local copy yet — the agent can download and cache it on request.'
              : 'No local copy — this connector can’t download the file automatically.'}
          </p>
        )}
        {preview && (
          <p className="mt-1 line-clamp-2 text-xs leading-5 text-muted-foreground">
            {preview}
          </p>
        )}
        <Collapsible open={detailsOpen} onOpenChange={setDetailsOpen}>
          <CollapsibleTrigger asChild>
            <button
              type="button"
              className="mt-1 flex h-6 items-center gap-1 rounded-sm text-[11px] text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
              aria-label={detailsOpen ? `Hide details for ${record.title}` : `Show details for ${record.title}`}
            >
              <ChevronRight
                size={12}
                aria-hidden="true"
                className={cn('motion-safe:transition-transform', detailsOpen && 'rotate-90')}
              />
              Details
            </button>
          </CollapsibleTrigger>
          <CollapsibleContent>
            <FileProvenanceDetails
              provenance={fileProvenanceFromRecord(record)}
              onCopied={onCopied}
              className="mt-2 border-t border-border/50 pl-6 pt-2"
            />
          </CollapsibleContent>
        </Collapsible>
      </div>
      <div className="flex shrink-0 items-start gap-1">
        {canRequestDownload && onRequestRemoteDownload && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                type="button"
                size="sm"
                variant="outline"
                className="h-8 gap-1.5 px-2.5 text-xs"
                onClick={() => onRequestRemoteDownload(record)}
                aria-label={`Ask agent to download ${record.title}`}
                data-file-request-download
              >
                <CloudDownload size={14} aria-hidden="true" />
                Ask agent to download
              </Button>
            </TooltipTrigger>
            <TooltipContent>Adds an editable download prompt to the composer — nothing is sent until you send it</TooltipContent>
          </Tooltip>
        )}
        {remoteOpenUrl && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                asChild
                size="icon"
                variant="ghost"
                className="h-8 w-8 text-muted-foreground hover:text-foreground"
              >
                <a
                  href={remoteOpenUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  aria-label={`Open ${record.title} at its remote source (opens in a new tab)`}
                  data-file-open-remote
                >
                  <ExternalLink size={15} aria-hidden="true" />
                </a>
              </Button>
            </TooltipTrigger>
            <TooltipContent>Open the remote source in your browser</TooltipContent>
          </Tooltip>
        )}
        {!remoteOnly && record.downloadable && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                type="button"
                size="icon"
                variant="ghost"
                className="h-8 w-8 text-muted-foreground hover:text-foreground"
                onClick={() => onDownload(record)}
                aria-label={`Download ${record.title}`}
              >
                <Download size={15} aria-hidden="true" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Download original</TooltipContent>
          </Tooltip>
        )}
        {canRemove && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                type="button"
                size="icon"
                variant="ghost"
                className="h-8 w-8 text-muted-foreground hover:text-destructive"
                onClick={() => onDelete(record)}
                aria-label={removeAriaLabel}
              >
                <Trash2 size={15} aria-hidden="true" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>
              {remoteOnly
                ? 'Remove from Files and file history only. Remote source and chat/tool messages remain.'
                : 'Remove from Files and delete the app-managed copy.'}
            </TooltipContent>
          </Tooltip>
        )}
      </div>
    </div>
  );
};

const FilesAuditView: React.FC<FilesAuditViewProps> = ({
  currentThreadId,
  currentThreadTitle,
  onOpenArtifact,
  onOpenOriginThread,
  onRequestRemoteDownload,
  onCloseAfterOpen,
  initialScope,
  lockScope,
  layout = 'settings',
  title = 'Files',
  description = 'Review file artifacts the model can inspect, including chat uploads and connector downloads.',
  pageSize = DEFAULT_FILE_AUDIT_PAGE_SIZE,
}) => {
  const normalizedThreadId = String(currentThreadId || '').trim() || null;
  const defaultScope = initialScope || (normalizedThreadId ? 'thread' : 'all');
  const [scopeState, setScopeState] = React.useState<FilesAuditScope>(defaultScope);
  const [query, setQuery] = React.useState('');
  const [sourceFilter, setSourceFilter] = React.useState('all');
  const [remoteOnlyFilter, setRemoteOnlyFilter] = React.useState(false);
  const [deleteTarget, setDeleteTarget] = React.useState<FileAuditRecord | null>(null);
  const [deleting, setDeleting] = React.useState(false);
  const [copyAnnouncement, setCopyAnnouncement] = React.useState('');
  const copyAnnouncementTimer = React.useRef<number | null>(null);
  const listRef = React.useRef<HTMLDivElement>(null);
  const searchInputRef = React.useRef<HTMLInputElement>(null);
  // Fallback when zero-match pagination exhausts pages and removes its button.
  const clearFiltersRef = React.useRef<HTMLButtonElement>(null);
  const pendingDeleteFocusIndex = React.useRef<number | null>(null);
  // Loaded-record count captured when "Load more" is clicked, so focus can
  // move to the first newly appended row once it renders (the button itself
  // may unmount when the final page arrives).
  const pendingLoadMoreFocusCount = React.useRef<number | null>(null);
  const scope = lockScope || scopeState;
  const normalizedPageSize = Math.min(Math.max(Math.trunc(pageSize || DEFAULT_FILE_AUDIT_PAGE_SIZE), 1), 500);
  const normalizedQuery = query.trim();
  // The search box drives server-side queries: debounce to one request per
  // typing pause, but flush a cleared box immediately so the full list is
  // not held hostage to the debounce.
  const [serverQuery, setServerQuery] = React.useState('');
  React.useEffect(() => {
    if (!normalizedQuery) {
      setServerQuery('');
      return;
    }
    const timer = window.setTimeout(() => setServerQuery(normalizedQuery), 250);
    return () => window.clearTimeout(timer);
  }, [normalizedQuery]);
  // Source/status are discrete toggles (no debounce needed); passing them to
  // the hook makes them server-side filters that reset pagination on change.
  const requestedSource = sourceFilter !== 'all' ? sourceFilter : '';
  const audit = useFileAudit(scope, normalizedThreadId, normalizedPageSize, serverQuery, requestedSource, remoteOnlyFilter);
  const searchPending = normalizedQuery !== serverQuery;

  // Requested-download ledger: rows re-render when a draft request is added
  // or cleared anywhere in the app (the ask action usually navigates away,
  // so the state is mostly consumed on return to this surface).
  const downloadRequestsVersion = React.useSyncExternalStore(
    subscribeRemoteFileDownloadRequests,
    remoteFileDownloadRequestsSnapshot,
    remoteFileDownloadRequestsSnapshot,
  );
  const downloadRequestFor = React.useCallback(
    (record: FileAuditRecord) => getRemoteFileDownloadRequest(record),
    // The version is the lookup's real dependency: the store mutates in place.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [downloadRequestsVersion],
  );
  // A cached record fulfils any request matching its key or remote locators.
  // Clearing here (not just hiding on cached rows) matters for the resurface
  // path: delete the cached copy later and the remote reference returns —
  // without this, it would carry a stale "you drafted a request" note for a
  // request that already succeeded.
  React.useEffect(() => {
    for (const record of audit.records) {
      if (record.cached !== false) clearRemoteFileDownloadRequestsFor(record);
    }
  }, [audit.records]);
  // "A run finished in a thread with pending download requests" — offer a
  // refresh, never auto-refresh: run completion is a hint (the run may have
  // downloaded nothing), and reloading would reset pagination under the user.
  const [runCompletedHint, setRunCompletedHint] = React.useState(false);
  React.useEffect(() => subscribeRemoteFileDownloadRunCompleted(() => setRunCompletedHint(true)), []);
  React.useEffect(() => {
    // Any full reload supersedes the hint: the list now reflects the run.
    if (audit.loading) setRunCompletedHint(false);
  }, [audit.loading]);

  React.useEffect(() => () => {
    if (copyAnnouncementTimer.current !== null) window.clearTimeout(copyAnnouncementTimer.current);
  }, []);

  const announceCopy = React.useCallback((fieldLabel: string) => {
    // Separate live region from the result-count announcer so copy messages
    // never clobber (or get clobbered by) list updates.
    setCopyAnnouncement(`Copied ${fieldLabel.toLowerCase()}`);
    if (copyAnnouncementTimer.current !== null) window.clearTimeout(copyAnnouncementTimer.current);
    copyAnnouncementTimer.current = window.setTimeout(() => setCopyAnnouncement(''), 1200);
  }, []);

  React.useEffect(() => {
    if (!normalizedThreadId && scopeState === 'thread') setScopeState('all');
  }, [normalizedThreadId, scopeState]);

  // With server-side search/filters the loaded records are only the matches,
  // so chip stability needs a memory: remember every source seen in this
  // scope — from loaded records and from server facet counts — and keep its
  // chip while a query or filter narrows the list, instead of letting
  // non-matching chips vanish.
  const [seenSources, setSeenSources] = React.useState<string[]>([]);
  React.useEffect(() => {
    setSeenSources([]);
  }, [scope, normalizedThreadId]);
  const facetSourceCounts = audit.scopeMeta?.sourceCounts;
  React.useEffect(() => {
    setSeenSources((prev) => {
      const merged = new Set(prev);
      for (const record of audit.records) merged.add(fileSourceKey(record));
      for (const key of Object.keys(facetSourceCounts || {})) merged.add(key);
      if (merged.size === prev.length) return prev;
      return [...merged].sort((a, b) => a.localeCompare(b));
    });
  }, [audit.records, facetSourceCounts]);
  const sources = React.useMemo(() => {
    // Keep the active filter's chip around even if its records disappear
    // (refresh, deletes) so the user can still see and clear the filter.
    if (sourceFilter !== 'all' && !seenSources.includes(sourceFilter)) {
      return [...seenSources, sourceFilter].sort((a, b) => a.localeCompare(b));
    }
    return seenSources;
  }, [seenSources, sourceFilter]);
  // Client-side narrowing is only a fallback for legacy backends that ignore
  // the params: once the backend echoes an applied query/filter, the loaded
  // records already reflect it, and re-filtering here would drop matches from
  // fields only the server sees (e.g. nested tool metadata). While a changed
  // query/filter's fetch is in flight the fallback also narrows the stale
  // records, so the interim view never shows rows the new state excludes.
  const scopeMeta = audit.scopeMeta;
  const echoedQuery = scopeMeta?.query || '';
  const clientQueryNeedle = normalizedQuery && echoedQuery !== normalizedQuery
    ? normalizedQuery.toLowerCase()
    : '';
  const serverSearchActive = Boolean(normalizedQuery) && !clientQueryNeedle;
  const serverSourceApplied = Boolean(requestedSource)
    && (scopeMeta?.source || '').toLowerCase() === requestedSource.toLowerCase();
  const serverRemoteOnlyApplied = remoteOnlyFilter && scopeMeta?.remoteOnly === true;
  const clientSourceActive = Boolean(requestedSource) && !serverSourceApplied;
  const clientRemoteOnlyActive = remoteOnlyFilter && !serverRemoteOnlyApplied;
  // Any server-side narrowing means the loaded set is "matches", not the
  // audit inventory — counts and nouns must describe it that way.
  const serverNarrowed = serverSearchActive || serverSourceApplied || serverRemoteOnlyApplied;
  const searchFiltered = React.useMemo(() => {
    if (!clientQueryNeedle) return audit.records;
    return audit.records.filter((record) => recordSearchText(record).includes(clientQueryNeedle));
  }, [audit.records, clientQueryNeedle]);
  const filtered = React.useMemo(() => {
    let next = searchFiltered;
    if (clientSourceActive) next = next.filter((record) => fileSourceKey(record) === sourceFilter);
    if (clientRemoteOnlyActive) next = next.filter((record) => record.cached === false);
    return next;
  }, [clientRemoteOnlyActive, clientSourceActive, searchFiltered, sourceFilter]);
  // Chip counts are only claimed when they can be honest: server facets when
  // provided; otherwise loaded-record counts, but only while no server-side
  // source/status filter has narrowed the loaded universe (a per-bucket count
  // computed from someone else's bucket would read as "0 such files").
  const loadedCountsHonest = !serverSourceApplied && !serverRemoteOnlyApplied;
  const loadedSourceCounts = React.useMemo(() => {
    const counts = new Map<string, number>();
    for (const record of searchFiltered) {
      const key = fileSourceKey(record);
      counts.set(key, (counts.get(key) || 0) + 1);
    }
    return counts;
  }, [searchFiltered]);
  const sourceChipCount = React.useCallback((source: string): number | undefined => {
    if (facetSourceCounts) return facetSourceCounts[source] ?? 0;
    return loadedCountsHonest ? (loadedSourceCounts.get(source) || 0) : undefined;
  }, [facetSourceCounts, loadedCountsHonest, loadedSourceCounts]);
  const allChipCount = facetSourceCounts
    ? Object.values(facetSourceCounts).reduce((sum, count) => sum + count, 0)
    : loadedCountsHonest ? searchFiltered.length : undefined;
  const loadedRemoteOnlyCount = React.useMemo(
    () => searchFiltered.filter((record) => record.cached === false).length,
    [searchFiltered],
  );
  const remoteChipCount = scopeMeta?.remoteOnlyCount
    ?? (loadedCountsHonest ? loadedRemoteOnlyCount : undefined);
  // The status chip is a toggle, not a bucket list: once this scope has shown
  // remote-only files, keep the chip mounted even when the current source
  // filter's facet drops to 0 — unmounting it would make the filter
  // unreachable (and un-clearable) exactly when the user needs to see "0".
  const [seenRemoteOnly, setSeenRemoteOnly] = React.useState(false);
  React.useEffect(() => {
    setSeenRemoteOnly(false);
  }, [scope, normalizedThreadId]);
  React.useEffect(() => {
    if ((remoteChipCount ?? loadedRemoteOnlyCount) > 0) setSeenRemoteOnly(true);
  }, [loadedRemoteOnlyCount, remoteChipCount]);
  const showRemoteChip = seenRemoteOnly || remoteOnlyFilter
    || (remoteChipCount ?? loadedRemoteOnlyCount) > 0;
  const hasActiveFilters = Boolean(normalizedQuery) || sourceFilter !== 'all' || remoteOnlyFilter;
  const clearFilters = React.useCallback(() => {
    setQuery('');
    setSourceFilter('all');
    setRemoteOnlyFilter(false);
    // The Clear filters button lives in the zero-match empty state, which
    // unmounts on reset; hand focus to the always-mounted search box.
    searchInputRef.current?.focus();
  }, []);
  const canLoadMore = Boolean(audit.scopeMeta?.nextOffset && audit.scopeMeta.nextOffset > 0);
  const resultAnnouncement = React.useMemo(() => {
    if (audit.loading) return 'Loading files.';
    if (audit.unavailable) return 'Files is not available in this build.';
    if (audit.error) return 'File list failed to load.';
    const noun = serverNarrowed
      ? (filtered.length === 1 ? 'match' : 'matches')
      : (filtered.length === 1 ? 'file' : 'files');
    const suffix = remoteOnlyFilter ? ', remote only' : '';
    // Coverage caveats: server-applied search/filters span the whole audit,
    // but client-side fallbacks only ever see loaded records.
    const coverage = !canLoadMore
      ? ''
      : clientQueryNeedle
        ? ' Only loaded files are searched; more exist.'
        : (clientSourceActive || clientRemoteOnlyActive)
          ? serverSearchActive
            ? ' Filters only cover loaded search matches; more matches exist.'
            : ' Only loaded files are filtered; more exist.'
          : serverNarrowed
            ? ' More matches can be loaded.'
            : '';
    if (sourceFilter !== 'all') return `${filtered.length} ${noun} shown from ${sourceFilter}${suffix}.${coverage}`;
    return `${filtered.length} ${noun} shown${suffix}.${coverage}`;
  }, [audit.error, audit.loading, audit.unavailable, canLoadMore, clientQueryNeedle, clientRemoteOnlyActive, clientSourceActive, filtered.length, remoteOnlyFilter, serverNarrowed, serverSearchActive, sourceFilter]);
  // Trailing debounce so typing in the search box queues one polite
  // announcement per pause, not one per keystroke.
  const [announcedResult, setAnnouncedResult] = React.useState('');
  React.useEffect(() => {
    const timer = window.setTimeout(() => setAnnouncedResult(resultAnnouncement), 350);
    return () => window.clearTimeout(timer);
  }, [resultAnnouncement]);

  const handleLoadMore = React.useCallback(() => {
    pendingLoadMoreFocusCount.current = audit.records.length;
    audit.loadMore();
  }, [audit]);
  // A full reload (scope switch, refresh) invalidates the captured count.
  React.useEffect(() => {
    if (audit.loading) pendingLoadMoreFocusCount.current = null;
  }, [audit.loading]);
  React.useEffect(() => {
    const prevCount = pendingLoadMoreFocusCount.current;
    if (prevCount === null || audit.records.length <= prevCount) return;
    pendingLoadMoreFocusCount.current = null;
    // Focus the first appended row that survives the active filters; if the
    // filters hide them all, focus stays on the still-mounted button.
    const appendedKeys = new Set(audit.records.slice(prevCount).map(fileAuditRecordKey));
    const index = filtered.findIndex((record) => appendedKeys.has(fileAuditRecordKey(record)));
    if (index < 0) {
      // If the final page hides the Load more button, keep keyboard focus in
      // this surface instead of letting it fall back to <body>.
      const active = document.activeElement;
      if (active === document.body || active === null) {
        (clearFiltersRef.current || listRef.current || searchInputRef.current)?.focus();
      }
      return;
    }
    // Chained lookups preserve priority; selector lists follow DOM order.
    const row = listRef.current?.querySelectorAll<HTMLElement>('[data-file-audit-row]')?.[index];
    const target = row?.querySelector<HTMLElement>('[data-file-title-button]')
      ?? row?.querySelector<HTMLElement>('[data-file-request-download]')
      ?? row?.querySelector<HTMLElement>('button, a[href]');
    target?.focus();
  }, [audit.records, filtered]);

  const counts = React.useMemo(() => {
    const remoteOnly = audit.records.filter((record) => record.cached === false).length;
    return { total: audit.records.length, cached: audit.records.length - remoteOnly, remoteOnly };
  }, [audit.records]);
  // The stored-locally/remote-only header badges are inventory claims like
  // the total badge, so prefer the server's status facets (whole current
  // universe) over loaded-page counts when the backend provides them.
  const cachedBadgeCount = scopeMeta?.cachedCount ?? counts.cached;
  const remoteBadgeCount = scopeMeta?.remoteOnlyCount ?? counts.remoteOnly;
  // The headline badge is the audit's inventory claim, so it must not read as
  // a total while pagination is holding records back: "3 of 7 files" when the
  // backend enumerated everything, "3 of 7+ files" when a scan limit makes
  // that a lower bound, "3+ files" when only "more exist" is known. While a
  // server-side search or filter is applied the loaded set is matches, not
  // inventory, so the badge claims match counts instead.
  const countSummary = fileAuditCountSummary(audit.scopeMeta, counts.total);
  const badgeUnit = serverNarrowed ? 'matches' : 'files';
  const totalBadgeLabel = countSummary.available !== null
    ? `${counts.total} of ${countSummary.available}${countSummary.availableIsLowerBound ? '+' : ''} ${badgeUnit}`
    : countSummary.hasMore
      ? `${counts.total}+ ${badgeUnit}`
      : serverNarrowed
        ? `${counts.total} ${counts.total === 1 ? 'match' : 'matches'}`
        : `${counts.total} files`;
  // Zero loaded records under any server-applied narrowing is a whole-audit
  // verdict: the backend filtered before pagination and found nothing, and a
  // client-fallback filter can only subtract from that empty set. Without
  // server narrowing, zero loaded records means the audit itself is empty
  // (the genuine-empty state), and zero *visible* rows from client fallbacks
  // is merely a loaded-pages verdict — the states must not blur.
  const serverZeroVerdict = audit.records.length === 0 && serverNarrowed;
  const searchBounded = Boolean(audit.scopeMeta?.cachedFileLimitHit || audit.scopeMeta?.remoteScanLimitHit);

  const handleOpen = React.useCallback(async (record: FileAuditRecord) => {
    if (record.cached === false) return;
    try {
      const artifact = await loadFileAuditArtifact(record.artifactId);
      onOpenArtifact(artifact, fileProvenanceFromRecord(record));
      onCloseAfterOpen?.();
    } catch (err: any) {
      toast.error(err?.message || `Couldn't open ${record.title}.`);
    }
  }, [onCloseAfterOpen, onOpenArtifact]);

  const handleDownload = React.useCallback(async (record: FileAuditRecord) => {
    try {
      const fileName = record.artifact
        ? getArtifactDownloadFileName(record.artifact)
        : record.title;
      await downloadArtifactRawBytes(record.artifactId, fileName);
    } catch {
      toast.error(`Couldn't download ${record.title}.`);
    }
  }, []);

  const handleConfirmDelete = React.useCallback(async () => {
    if (!deleteTarget || deleting) return;
    setDeleting(true);
    try {
      await deleteFileAuditArtifact(deleteTarget.artifactId);
      // The dialog's default close focus target is the removed row's button,
      // which is about to unmount; remember where focus should land instead.
      pendingDeleteFocusIndex.current = filtered.findIndex((record) => record.artifactId === deleteTarget.artifactId);
      // Delete owns focus restoration; discard any stale load-more boundary.
      pendingLoadMoreFocusCount.current = null;
      audit.removeRecord(deleteTarget.artifactId);
      // Deleting a remote reference retires its own drafted request; deleting
      // a cached copy retires the (locator-matched) request it fulfilled, so
      // the resurfaced reference starts clean.
      clearRemoteFileDownloadRequestsFor(deleteTarget);
      toast.success(deleteTarget.cached === false
        ? `Removed remote reference for ${deleteTarget.title}`
        : `Removed ${deleteTarget.title}`);
      setDeleteTarget(null);
    } catch (err: any) {
      toast.error(err?.message || `Couldn't remove ${deleteTarget.title}.`);
    } finally {
      setDeleting(false);
    }
  }, [audit, deleteTarget, deleting, filtered]);

  const handleDeleteDialogCloseFocus = React.useCallback((event: Event) => {
    const index = pendingDeleteFocusIndex.current;
    if (index === null) return;
    pendingDeleteFocusIndex.current = null;
    event.preventDefault();
    // Remote-only rows have no title button, so use action priority explicitly.
    const rows = listRef.current?.querySelectorAll<HTMLElement>('[data-file-audit-row]');
    if (rows && rows.length > 0) {
      const row = rows[Math.min(Math.max(index, 0), rows.length - 1)];
      const target = row.querySelector<HTMLButtonElement>('[data-file-title-button]')
        ?? row.querySelector<HTMLButtonElement>('[data-file-request-download]')
        ?? row.querySelector<HTMLButtonElement>('button');
      if (target) {
        target.focus();
        return;
      }
    }
    // No rows survived the delete (the list container itself may already be
    // unmounted in favour of an empty state); the search box is the one
    // always-mounted, useful landing spot.
    if (listRef.current) {
      listRef.current.focus();
      return;
    }
    searchInputRef.current?.focus();
  }, []);

  const emptyTitle = scope === 'thread' ? 'No files in this chat' : 'No files seen yet';
  const emptyText = scope === 'thread'
    ? 'Uploaded files and connector-downloaded files touched in this chat will appear here.'
    : 'Files from chats and connected tools will appear here once the model can inspect or download them.';
  const pageLayout = layout === 'page';

  return (
    <div className={cn(pageLayout ? 'flex h-full min-h-0 flex-col gap-4' : 'space-y-4')} data-file-audit-view>
      <div className="sr-only" aria-live="polite">{announcedResult}</div>
      <div className="sr-only" aria-live="polite">{copyAnnouncement}</div>
      <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <FileText size={18} className="text-primary" aria-hidden="true" />
            {/* The title prop names the all-files scope; in thread scope the
                heading must agree with the active tab and scope banner rather
                than keep claiming "All files". */}
            <h3 className="text-base font-semibold">
              {!lockScope && scope === 'thread' ? 'This chat' : title}
            </h3>
          </div>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            {description}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant="outline" className="gap-1.5 border-border bg-muted/30" data-file-audit-total-badge>
            <Files size={12} aria-hidden="true" />
            {totalBadgeLabel}
          </Badge>
          {/* "Stored locally" is the user-facing opposite of "Remote only";
              "cached" stays an internal term. */}
          <Badge variant="outline" className="border-border bg-muted/30">
            {cachedBadgeCount} stored locally
          </Badge>
          {remoteBadgeCount > 0 && (
            <Badge variant="outline" className="gap-1.5 border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300">
              <Cloud size={12} aria-hidden="true" />
              {remoteBadgeCount} remote only
            </Badge>
          )}
        </div>
      </div>

      <div className="flex flex-col gap-3 lg:flex-row lg:items-center">
        {!lockScope && (
          <div className="flex items-center gap-2">
            <Tabs value={scope} onValueChange={(value) => setScopeState(value as FilesAuditScope)}>
              <TabsList className="h-9" aria-label="File scope">
                <TabsTrigger value="thread" disabled={!normalizedThreadId} className="text-xs">
                  This Chat
                </TabsTrigger>
                <TabsTrigger value="all" className="text-xs">
                  All Files
                </TabsTrigger>
              </TabsList>
            </Tabs>
            {/* Visible reason: a disabled tab drops out of the focus order,
                so a tooltip on it would never reach keyboard or SR users. */}
            {!normalizedThreadId && (
              <span className="text-xs text-muted-foreground" data-scope-hint>
                Open a chat to filter files to it.
              </span>
            )}
          </div>
        )}
        <div className="relative min-w-0 flex-1">
          <Search size={15} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground" aria-hidden="true" />
          <Input
            ref={searchInputRef}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search files, source, chat, file type, or preview"
            aria-label="Search files"
            className="h-9 pl-8 pr-8"
          />
          {query && (
            <button
              type="button"
              aria-label="Clear search"
              // Clearing unmounts this button; hand focus back to the input
              // so keyboard users are not dropped to <body> mid-task.
              onClick={() => {
                setQuery('');
                searchInputRef.current?.focus();
              }}
              className="absolute right-2 top-1/2 -translate-y-1/2 rounded-sm p-0.5 text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
            >
              <X size={14} aria-hidden="true" />
            </button>
          )}
        </div>
        <Tooltip>
          <TooltipTrigger asChild>
            {/* Not disabled while loading (same rule as Load more): disabling
                the focused button would blur keyboard focus to <body>. The
                click is a no-op mid-load and aria-busy reports the wait. */}
            <Button
              type="button"
              variant="outline"
              size="icon"
              className="h-9 w-9"
              onClick={() => { if (!audit.loading) audit.refresh(); }}
              aria-busy={audit.loading}
              aria-label="Refresh files"
            >
              {audit.loading
                ? <Loader2 size={15} className="motion-safe:animate-spin" aria-hidden="true" />
                : <RefreshCw size={15} aria-hidden="true" />}
            </Button>
          </TooltipTrigger>
          {/* Different text from the aria-label so aria-describedby does not
              echo the accessible name back to screen readers. */}
          <TooltipContent>Reload the file list</TooltipContent>
        </Tooltip>
      </div>

      {sources.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5" data-source-filter-row>
          <div
            role="group"
            aria-label="Filter files by source"
            className="flex flex-wrap items-center gap-1.5"
          >
            <SourceFilterChip
              label="All"
              count={allChipCount}
              active={sourceFilter === 'all'}
              onClick={() => setSourceFilter('all')}
            />
            {sources.map((source) => (
              <SourceFilterChip
                key={source}
                label={source}
                count={sourceChipCount(source)}
                active={sourceFilter === source}
                dotClass={fileSourceDotClass(source)}
                onClick={() => setSourceFilter((prev) => (prev === source ? 'all' : source))}
              />
            ))}
          </div>
          {/* Separate group: the status toggle is not a source bucket, so it
              must not inherit the "by source" group label. */}
          {showRemoteChip && (
            <div role="group" aria-label="Filter files by status" className="flex items-center gap-1.5">
              <span aria-hidden="true" className="mx-0.5 h-4 w-px shrink-0 bg-border" />
              <button
                type="button"
                aria-pressed={remoteOnlyFilter}
                onClick={() => setRemoteOnlyFilter((prev) => !prev)}
                data-status-filter-chip="remote-only"
                className={cn(
                  'inline-flex h-7 min-w-0 max-w-full items-center gap-1.5 whitespace-nowrap rounded-full border px-2.5 text-xs',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background',
                  remoteOnlyFilter
                    ? 'border-amber-500/50 bg-amber-500/10 font-medium text-foreground'
                    : 'border-border bg-background text-muted-foreground hover:bg-muted/40 hover:text-foreground',
                )}
              >
                <Cloud size={12} aria-hidden="true" className="shrink-0" />
                <span className="min-w-0 truncate">Remote only</span>
                {remoteChipCount !== undefined && (
                  <>
                    <span aria-hidden="true" className="text-[11px] tabular-nums text-muted-foreground">{remoteChipCount}</span>
                    <span className="sr-only">({remoteChipCount} {remoteChipCount === 1 ? 'file' : 'files'})</span>
                  </>
                )}
              </button>
            </div>
          )}
        </div>
      )}

      {scope === 'thread' && currentThreadTitle && (
        <div
          className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border bg-muted/20 px-3 py-2 text-xs text-muted-foreground"
          data-scope-banner
        >
          <span className="min-w-0">
            Showing files for <span className="font-medium text-foreground">{currentThreadTitle}</span>.
          </span>
          {!lockScope && (
            <button
              type="button"
              className="shrink-0 rounded-sm font-medium text-foreground underline-offset-4 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background"
              onClick={() => {
                setScopeState('all');
                // This banner unmounts with the scope switch; hand focus to
                // the always-mounted search box instead of dropping to body.
                searchInputRef.current?.focus();
              }}
            >
              View all files
            </button>
          )}
        </div>
      )}

      {runCompletedHint && (
        // role=status: the bar's appearance is worth one polite announcement,
        // and its wording promises only "may" — the run is a hint, not proof
        // the download happened.
        <div
          role="status"
          className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border bg-muted/20 px-3 py-2 text-xs"
          data-file-audit-run-hint
        >
          <span className="min-w-0 text-muted-foreground">
            A recent agent run finished — files you asked it to download may be ready.
          </span>
          <div className="flex shrink-0 items-center gap-1">
            <Button
              type="button"
              size="sm"
              variant="outline"
              className="h-7 px-2.5 text-xs"
              aria-label="Refresh files to check for requested downloads"
              onClick={() => {
                setRunCompletedHint(false);
                // This button unmounts with the hint; keep keyboard focus in
                // the surface (same rule as the scope banner's action).
                searchInputRef.current?.focus();
                if (!audit.loading) audit.refresh();
              }}
            >
              Refresh
            </Button>
            <Button
              type="button"
              size="icon"
              variant="ghost"
              className="h-7 w-7 text-muted-foreground hover:text-foreground"
              aria-label="Dismiss download notice"
              onClick={() => {
                setRunCompletedHint(false);
                searchInputRef.current?.focus();
              }}
            >
              <X size={13} aria-hidden="true" />
            </Button>
          </div>
        </div>
      )}

      <div className={cn('overflow-hidden rounded-md border border-border bg-background', pageLayout ? 'flex min-h-0 flex-1 flex-col' : '')}>
        <FileAuditScopeStatus
          meta={audit.scopeMeta}
          visibleCount={filtered.length}
          totalLoaded={audit.records.length}
          canLoadMore={canLoadMore}
          loading={audit.loadingMore}
          narrowed={serverNarrowed}
          onLoadMore={handleLoadMore}
        />
        {/* searchPending counts as loading: a not-yet-sent query must not
            flash a zero-match verdict computed from the previous results. */}
        {(audit.loading || searchPending) && filtered.length === 0 ? (
          <div className={cn('flex items-center justify-center text-sm text-muted-foreground', pageLayout ? 'h-full min-h-48' : 'h-48')}>
            <Loader2 size={16} className="mr-2 motion-safe:animate-spin" aria-hidden="true" />
            Loading files...
          </div>
        ) : audit.unavailable ? (
          // Calm capability notice, not an error: the backend simply predates
          // the files service, and nothing the user does here can fix that.
          <div
            className={cn('flex flex-col items-center justify-center px-4 text-center', pageLayout ? 'h-full min-h-48' : 'h-48')}
            data-file-audit-unavailable
          >
            <FileText size={24} className="mb-2 text-muted-foreground" aria-hidden="true" />
            <p className="text-sm font-medium text-foreground">Files isn&apos;t available in this build</p>
            <p className="mt-1 max-w-md text-xs leading-5 text-muted-foreground">
              The connected backend doesn&apos;t expose the files service yet. Chats and uploads keep working; update the backend to review files here.
            </p>
            <Button type="button" variant="outline" size="sm" className="mt-3" onClick={audit.refresh}>
              Check again
            </Button>
          </div>
        ) : audit.error ? (
          <div className={cn('flex flex-col items-center justify-center gap-3 px-4 text-center', pageLayout ? 'h-full min-h-48' : 'h-48')}>
            <p className="text-sm text-destructive">{audit.error}</p>
            <Button type="button" variant="outline" size="sm" onClick={audit.refresh}>Retry</Button>
          </div>
        ) : filtered.length === 0 && hasActiveFilters && (audit.records.length > 0 || serverZeroVerdict) ? (
          // Zero matches with active filters is not an empty audit: say which
          // situation this is and offer a one-click way back to the full list.
          <div
            className={cn('flex flex-col items-center justify-center px-4 text-center', pageLayout ? 'h-full min-h-48' : 'h-48')}
            data-file-audit-filtered-empty
          >
            <SearchX size={24} className="mb-2 text-muted-foreground" aria-hidden="true" />
            <p className="text-sm font-medium text-foreground">
              {serverZeroVerdict && normalizedQuery ? 'No files match your search' : 'No files match your filters'}
            </p>
            <p className="mt-1 max-w-md text-xs leading-5 text-muted-foreground">
              {serverZeroVerdict
                ? searchBounded
                  ? normalizedQuery
                    ? `Nothing matches “${normalizedQuery}” among the files this view can scan — a scan limit may hide older files.`
                    : 'Nothing matches the current filters among the files this view can scan — a scan limit may hide older files.'
                  : normalizedQuery
                    ? `Nothing matches “${normalizedQuery}” — every file in this view was searched, not just loaded pages.`
                    : 'Nothing matches the current filters — they covered every file in this view, not just loaded pages.'
                : serverSearchActive
                  ? audit.records.length === 1
                    ? 'The 1 loaded search match is hidden by the source or status filters.'
                    : `All ${audit.records.length} loaded search matches are hidden by the source or status filters.`
                  : audit.records.length === 1
                    ? 'The 1 loaded file is hidden by the current search or filters.'
                    : `All ${audit.records.length} loaded files are hidden by the current search or filters.`}
            </p>
            {!serverZeroVerdict && canLoadMore && (
              // Zero matches while pages remain unloaded is not exhaustive.
              <p className="mt-1 max-w-md text-xs leading-5 text-muted-foreground" data-file-audit-search-coverage>
                {filteredEmptyCoverage(
                  audit.records.length,
                  countSummary,
                  serverSearchActive ? 'search-matches' : clientQueryNeedle ? 'client-search' : 'filters',
                )}
              </p>
            )}
            <div className="mt-3 flex flex-wrap items-center justify-center gap-2">
              <Button
                ref={clearFiltersRef}
                type="button"
                variant="outline"
                size="sm"
                onClick={clearFilters}
              >
                Clear filters
              </Button>
              {!serverZeroVerdict && canLoadMore && (
                // Do not disable while focused; use aria-busy for progress.
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="gap-1.5"
                  onClick={handleLoadMore}
                  aria-busy={audit.loadingMore}
                  aria-label="Load more files to widen the search"
                  data-file-audit-filtered-empty-load-more
                >
                  {audit.loadingMore ? (
                    <Loader2 size={13} className="motion-safe:animate-spin" aria-hidden="true" />
                  ) : (
                    <Plus size={13} aria-hidden="true" />
                  )}
                  Load more files
                </Button>
              )}
            </div>
          </div>
        ) : filtered.length === 0 ? (
          <div className={cn('flex flex-col items-center justify-center px-4 text-center', pageLayout ? 'h-full min-h-48' : 'h-48')}>
            <FileText size={24} className="mb-2 text-muted-foreground" aria-hidden="true" />
            <p className="text-sm font-medium text-foreground">{emptyTitle}</p>
            <p className="mt-1 max-w-md text-xs leading-5 text-muted-foreground">{emptyText}</p>
          </div>
        ) : (
          <div
            ref={listRef}
            tabIndex={-1}
            className={cn(pageLayout ? 'min-h-0 flex-1 overflow-auto' : 'max-h-[52vh] overflow-auto', 'focus-visible:outline-none')}
            role="list"
            aria-label="Files"
          >
            {filtered.map((record) => (
              <FileRow
                key={record.artifactId}
                record={record}
                downloadRequest={downloadRequestFor(record)}
                onOpen={handleOpen}
                onOpenOriginThread={onOpenOriginThread}
                onDownload={handleDownload}
                onRequestRemoteDownload={onRequestRemoteDownload}
                onDelete={setDeleteTarget}
                onCopied={announceCopy}
              />
            ))}
          </div>
        )}
      </div>

      <Dialog open={!!deleteTarget} onOpenChange={(open) => { if (!open && !deleting) setDeleteTarget(null); }}>
        <DialogContent className="max-w-[420px]" onCloseAutoFocus={handleDeleteDialogCloseFocus}>
          <DialogHeader>
            {/* "Stored", not "cached": the header badge already committed to
                user-facing vocabulary and the title must not leak the
                internal term. */}
            <DialogTitle>{deleteTarget?.cached === false ? 'Remove remote reference?' : 'Remove stored file?'}</DialogTitle>
            <DialogDescription>
              {deleteTarget?.cached === false
                ? <>Remove &quot;{deleteTarget?.title || 'File'}&quot; from Files and file history only. No local copy exists, the remote source is untouched, and source chat/tool messages will remain.</>
                : <>Remove &quot;{deleteTarget?.title || 'File'}&quot; from Files and file history, and delete the app-managed copy. Source chat/tool messages will remain.</>}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setDeleteTarget(null)} disabled={deleting}>
              Cancel
            </Button>
            <Button type="button" variant="destructive" onClick={handleConfirmDelete} disabled={deleting}>
              {deleting ? 'Removing...' : deleteTarget?.cached === false ? 'Remove reference' : 'Remove file'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
};

export default FilesAuditView;
