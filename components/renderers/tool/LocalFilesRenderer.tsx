import React, { useMemo, useState } from 'react';
import {
  ChevronDown,
  ChevronRight,
  Eye,
  Folder,
  FolderSearch,
  Loader2,
  Search,
} from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { CONNECTION_META } from '@/lib/connectionsCatalog';
import { displayPath } from '@/lib/approvalPresentation';
import { fileKindGlyph } from '@/lib/fileKindGlyph';
import { formatBytes } from '@/lib/formatters';
import {
  fetchLocalGrantFilePreview,
  isLocalFilesApiUnavailable,
  type LocalGrantFilePreview,
} from '@/services/localFilesApi';
import { registerToolRenderer } from '../registry';
import type { RendererProps } from '../types';
import { Disclosure } from './query/atoms';
import { InspectorShell, InspectorZone } from './inspector/InspectorShell';
import { isPlainObject, safeJson } from './inspector/values';

/**
 * LocalFilesRenderer — first-class results for the local-machine file tools
 * (`file_search`, `local_list_folder`, and their grant-scoped agent twins).
 *
 * These tools read files that stay on the user's computer, so the renderer's
 * job is provenance and honesty: where the assistant looked, that access was
 * read-only, and which files matched — with the same file-kind glyphs the
 * Files audit rows and artifact viewer use. Without this registration the
 * results fall through to the generic ToolInspector's key-value dump.
 *
 * Preview uses the grant-scoped stream endpoint when the build has it and
 * says so plainly when it doesn't — files are never copied into the app to
 * make the preview work.
 */

interface LocalFileHit {
  fileRef?: string;
  path: string;
  displayPath: string;
  mimeType?: string;
  fileKind?: string;
  matchTypes: string[];
  lineMatches: { line: number; content: string }[];
  sizeBytes?: number;
  modifiedAt?: string;
  entryKind?: string;
  grantId?: string;
}

interface ParsedLocalResult {
  mode: 'search' | 'folder';
  localRoot: string;
  localKind: string;
  subPath?: string;
  query?: string;
  grantId?: string;
  files: LocalFileHit[];
  folders: LocalFileHit[];
  matchesCount?: number;
  limit?: number;
  truncated: boolean;
  raw: unknown;
}

function parseLineMatch(raw: string): { line: number; content: string } {
  const prefixed = raw.trim().match(/^Line\s+(\d+):\s?(.*)$/i);
  if (prefixed) return { line: parseInt(prefixed[1], 10), content: prefixed[2] };
  return { line: 0, content: raw };
}

function normalizeHit(raw: unknown, grantId?: string): LocalFileHit | null {
  if (!isPlainObject(raw)) return null;
  const path = String(raw.path ?? raw.file_ref ?? raw.display_path ?? '').trim();
  if (!path) return null;
  const matchTypes = Array.isArray(raw.match_types)
    ? raw.match_types.filter((t: unknown): t is string => typeof t === 'string')
    : [];
  const lineMatches = Array.isArray(raw.line_matches)
    ? raw.line_matches
        .filter((l: unknown): l is string => typeof l === 'string')
        .map(parseLineMatch)
    : [];
  return {
    fileRef: typeof raw.file_ref === 'string' ? raw.file_ref : undefined,
    path,
    displayPath: String(raw.display_path || path),
    mimeType: typeof raw.mime_type === 'string' ? raw.mime_type : undefined,
    fileKind: typeof raw.file_kind === 'string' ? raw.file_kind : undefined,
    matchTypes,
    lineMatches,
    sizeBytes: typeof raw.size_bytes === 'number' ? raw.size_bytes : undefined,
    modifiedAt: typeof raw.modified_at === 'string' ? raw.modified_at : undefined,
    entryKind: typeof raw.entry_kind === 'string' ? raw.entry_kind : undefined,
    grantId: typeof raw.grant_id === 'string' ? raw.grant_id : grantId,
  };
}

function parseLocalResult(artifact: RendererProps['artifact']): ParsedLocalResult | null {
  const meta = (artifact.metadata || {}) as Record<string, any>;
  let result: unknown = meta.raw_result;
  if (result === undefined && typeof artifact.content === 'string' && artifact.content.trim()) {
    try {
      result = JSON.parse(artifact.content);
    } catch {
      result = undefined;
    }
  }
  if (!isPlainObject(result)) return null;
  const grantId =
    (typeof result.grant_id === 'string' && result.grant_id) ||
    (typeof meta.result_metadata?.grant_id === 'string' && meta.result_metadata.grant_id) ||
    undefined;
  const isSearch = typeof result.query === 'string' && result.query.trim() !== '';
  const files = Array.isArray(result.files)
    ? result.files.map((f: unknown) => normalizeHit(f, grantId)).filter(Boolean) as LocalFileHit[]
    : [];
  const folders = Array.isArray(result.folders)
    ? result.folders.map((f: unknown) => normalizeHit(f, grantId)).filter(Boolean) as LocalFileHit[]
    : [];
  return {
    mode: isSearch ? 'search' : 'folder',
    localRoot: String(result.local_root || ''),
    localKind: String(result.local_kind || 'folder'),
    subPath: typeof result.path === 'string' && result.path.trim() ? result.path : undefined,
    query: isSearch ? String(result.query) : undefined,
    grantId,
    files,
    folders,
    matchesCount: typeof result.matches_count === 'number' ? result.matches_count : undefined,
    limit: typeof result.limit === 'number' ? result.limit : undefined,
    truncated: result.truncated === true,
    raw: result,
  };
}

type PreviewState =
  | { phase: 'loading' }
  | { phase: 'ready'; preview: LocalGrantFilePreview }
  | { phase: 'unavailable' }
  | { phase: 'error'; message: string };

const InlinePreview: React.FC<{ grantId: string; fileRef: string; displayPath: string }> = ({
  grantId,
  fileRef,
  displayPath: hitPath,
}) => {
  const [state, setState] = useState<PreviewState>({ phase: 'loading' });

  React.useEffect(() => {
    let cancelled = false;
    const controller = new AbortController();
    fetchLocalGrantFilePreview(grantId, fileRef, { signal: controller.signal })
      .then((preview) => {
        if (!cancelled) setState({ phase: 'ready', preview });
      })
      .catch((e) => {
        if (cancelled) return;
        if (isLocalFilesApiUnavailable(e)) setState({ phase: 'unavailable' });
        else setState({ phase: 'error', message: e?.message || 'Preview failed' });
      });
    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [grantId, fileRef]);

  if (state.phase === 'loading') {
    return (
      <div className="flex items-center gap-2 px-4 py-2 text-xs text-muted-foreground" role="status">
        <Loader2 size={12} className="animate-spin" aria-hidden />
        Loading preview…
      </div>
    );
  }
  if (state.phase === 'unavailable') {
    return (
      <p className="px-4 py-2 text-xs text-muted-foreground" role="status">
        Inline preview isn't available here yet. Ask the assistant to read this file — it stays
        on your computer either way.
      </p>
    );
  }
  if (state.phase === 'error') {
    return (
      <p className="px-4 py-2 text-xs text-destructive" role="status">
        Couldn't load the preview: {state.message}
      </p>
    );
  }
  const { preview } = state;
  if (preview.binary || preview.text === undefined) {
    return (
      <p className="px-4 py-2 text-xs text-muted-foreground" role="status">
        {preview.reason === 'too-large'
          ? `This file is too large to preview inline${
              preview.byteSize !== undefined ? ` (${formatBytes(preview.byteSize)})` : ''
            }. Ask the assistant to read or summarize it.`
          : 'No inline preview for this file type. Ask the assistant to read or summarize it.'}
      </p>
    );
  }
  return (
    <>
      <span className="sr-only" role="status">
        Preview loaded
      </span>
      <pre
        tabIndex={0}
        role="region"
        aria-label={`Preview of ${hitPath}`}
        className="max-h-64 overflow-auto border-t border-border/30 bg-muted/20 px-4 py-2 font-mono text-[11px] text-foreground whitespace-pre-wrap [overflow-wrap:anywhere] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        {preview.text}
      </pre>
    </>
  );
};

const FileHitRow: React.FC<{ hit: LocalFileHit; mode: 'search' | 'folder' }> = ({ hit, mode }) => {
  const [linesOpen, setLinesOpen] = useState(mode === 'search' && hit.lineMatches.length > 0);
  const [previewOpen, setPreviewOpen] = useState(false);
  const regionId = React.useId();
  const Glyph = fileKindGlyph(hit.fileKind, hit.mimeType, hit.displayPath);
  const canExpandLines = hit.lineMatches.length > 0;
  const canPreview = Boolean(hit.grantId && hit.fileRef);

  return (
    <li className="border-b border-border/50 last:border-b-0">
      <div className="flex w-full items-center gap-2 px-4 py-1.5">
        {canExpandLines ? (
          <button
            type="button"
            onClick={() => setLinesOpen((v) => !v)}
            aria-expanded={linesOpen}
            aria-controls={`${regionId}-lines`}
            aria-label={`Matching lines in ${hit.displayPath}`}
            className="flex min-w-0 flex-1 items-center gap-2 rounded text-left transition-colors hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {linesOpen ? (
              <ChevronDown size={12} className="shrink-0 text-muted-foreground" aria-hidden />
            ) : (
              <ChevronRight size={12} className="shrink-0 text-muted-foreground" aria-hidden />
            )}
            <Glyph size={14} className="shrink-0 text-muted-foreground" aria-hidden />
            <span className="truncate font-mono text-xs text-foreground">{hit.displayPath}</span>
          </button>
        ) : (
          <span className="flex min-w-0 flex-1 items-center gap-2">
            <span className="w-3 shrink-0" aria-hidden />
            <Glyph size={14} className="shrink-0 text-muted-foreground" aria-hidden />
            <span className="truncate font-mono text-xs text-foreground">{hit.displayPath}</span>
          </span>
        )}
        <span className="ml-auto flex shrink-0 items-center gap-1.5">
          {mode === 'search' && hit.matchTypes.includes('name') && (
            <Badge variant="outline" className="px-1.5 py-0 text-[10px]">
              name
            </Badge>
          )}
          {hit.lineMatches.length > 0 && (
            <Badge variant="secondary" className="px-1.5 py-0 text-[10px]">
              {hit.lineMatches.length} {hit.lineMatches.length === 1 ? 'line' : 'lines'}
            </Badge>
          )}
          {mode === 'folder' && hit.sizeBytes !== undefined && (
            <span className="text-[10px] text-muted-foreground">{formatBytes(hit.sizeBytes)}</span>
          )}
          {canPreview && (
            <button
              type="button"
              onClick={() => setPreviewOpen((v) => !v)}
              aria-expanded={previewOpen}
              aria-controls={`${regionId}-preview`}
              aria-label={`Preview ${hit.displayPath}`}
              className="inline-flex items-center gap-1 rounded-md border border-border px-1.5 py-0.5 text-[10px] text-muted-foreground transition-colors hover:bg-muted/40 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              <Eye size={11} aria-hidden />
              Preview
            </button>
          )}
        </span>
      </div>
      {linesOpen && hit.lineMatches.length > 0 && (
        <ul id={`${regionId}-lines`} className="border-t border-border/30 bg-muted/20">
          {hit.lineMatches.map((line, i) => (
            <li key={i} className="flex px-4 py-0.5 font-mono text-xs hover:bg-muted/30">
              <span className="w-12 shrink-0 select-none pr-3 text-right text-muted-foreground/70">
                <span className="sr-only">Line </span>
                {line.line > 0 ? line.line : '-'}
              </span>
              <span className="whitespace-pre-wrap break-all text-foreground">{line.content}</span>
            </li>
          ))}
        </ul>
      )}
      {previewOpen && hit.grantId && hit.fileRef && (
        <div id={`${regionId}-preview`}>
          <InlinePreview grantId={hit.grantId} fileRef={hit.fileRef} displayPath={hit.displayPath} />
        </div>
      )}
    </li>
  );
};

const LocalFilesRenderer: React.FC<RendererProps> = ({ artifact }) => {
  const meta = (artifact.metadata || {}) as Record<string, any>;
  const parsed = useMemo(() => parseLocalResult(artifact), [artifact]);
  const toolName = String(meta.tool_name || artifact.title || 'file_search');
  const connector = CONNECTION_META['local-computer'] ?? null;
  const rawJson = useMemo(() => safeJson(parsed?.raw ?? artifact.content), [parsed, artifact.content]);

  if (!parsed) {
    return (
      <InspectorShell
        connectorMeta={connector}
        fallbackIcon={FolderSearch}
        toolName={toolName}
        status="success"
        providerLabel="Local Computer"
        durationMs={typeof meta.duration_ms === 'number' ? meta.duration_ms : null}
        endedAtMs={typeof meta.ended_at_ms === 'number' ? meta.ended_at_ms : null}
        toolCallId={typeof meta.tool_call_id === 'string' ? meta.tool_call_id : null}
      >
        <InspectorZone label="Response">
          <pre
            tabIndex={0}
            role="region"
            aria-label="Tool response"
            className="max-h-[420px] overflow-auto rounded-md border border-border bg-muted/30 p-3 font-mono text-[11px] whitespace-pre-wrap [overflow-wrap:anywhere] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {typeof artifact.content === 'string' ? artifact.content : ''}
          </pre>
        </InspectorZone>
      </InspectorShell>
    );
  }

  const location = displayPath(parsed.localRoot) || 'This computer';
  const scopeLine =
    parsed.localKind === 'file'
      ? 'Read-only · only this file'
      : 'Read-only · files stay on your computer';
  const fileCount = parsed.files.length;
  const summary =
    parsed.mode === 'search'
      ? `${parsed.matchesCount ?? fileCount} ${
          (parsed.matchesCount ?? fileCount) === 1 ? 'match' : 'matches'
        } across ${fileCount} ${fileCount === 1 ? 'file' : 'files'}`
      : `${parsed.folders.length} ${parsed.folders.length === 1 ? 'folder' : 'folders'} · ${fileCount} ${
          fileCount === 1 ? 'file' : 'files'
        }`;

  return (
    <InspectorShell
      connectorMeta={connector}
      fallbackIcon={FolderSearch}
      toolName={toolName}
      status="success"
      providerLabel="Local Computer"
      durationMs={typeof meta.duration_ms === 'number' ? meta.duration_ms : null}
      endedAtMs={typeof meta.ended_at_ms === 'number' ? meta.ended_at_ms : null}
      toolCallId={typeof meta.tool_call_id === 'string' ? meta.tool_call_id : null}
      badge={
        <span className="inline-flex items-center rounded-md border border-border bg-muted/40 px-2 py-0.5 text-[11px] font-medium text-muted-foreground">
          Read-only
        </span>
      }
    >
      <InspectorZone label={parsed.mode === 'search' ? 'Searched' : 'Listed'}>
        <div className="flex flex-col gap-1.5 text-xs">
          {parsed.query && (
            <div className="flex items-center gap-2">
              <Search size={13} className="shrink-0 text-muted-foreground" aria-hidden />
              <span className="font-medium text-foreground">“{parsed.query}”</span>
            </div>
          )}
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-muted-foreground">
            <span className="font-mono">
              {location}
              {parsed.subPath ? `/${parsed.subPath}` : ''}
            </span>
            <span className="text-muted-foreground/40">·</span>
            <span>{scopeLine}</span>
            {parsed.grantId && (
              <>
                <span className="text-muted-foreground/40">·</span>
                <span>Granted by you</span>
              </>
            )}
          </div>
        </div>
      </InspectorZone>

      <InspectorZone label={parsed.mode === 'search' ? 'Matches' : 'Contents'} flush>
        <div className="flex items-center gap-2 border-b border-border bg-muted/50 px-4 py-1.5">
          <span className="text-xs text-muted-foreground">{summary}</span>
          {parsed.truncated && (
            <Badge variant="outline" className="ml-auto shrink-0 text-[10px]">
              {parsed.limit ? `Showing first ${parsed.limit}` : 'More results — narrow your search'}
            </Badge>
          )}
        </div>
        {(fileCount > 0 || parsed.folders.length > 0) && (
          <ul>
            {parsed.mode === 'folder' &&
              parsed.folders.map((folder) => (
                <li
                  key={`dir:${folder.path}`}
                  className="flex items-center gap-2 border-b border-border/50 px-4 py-1.5 last:border-b-0"
                >
                  <span className="w-3 shrink-0" aria-hidden />
                  <Folder size={14} className="shrink-0 text-muted-foreground" aria-hidden />
                  <span className="truncate font-mono text-xs text-foreground">{folder.displayPath}</span>
                </li>
              ))}
            {parsed.files.map((hit) => (
              <FileHitRow key={hit.path} hit={hit} mode={parsed.mode} />
            ))}
          </ul>
        )}
        {fileCount === 0 && parsed.folders.length === 0 && (
          <p className="px-4 py-4 text-center text-xs text-muted-foreground">
            {parsed.mode === 'search' ? 'No files matched.' : 'This folder is empty.'}
          </p>
        )}
      </InspectorZone>

      <InspectorZone label="Details">
        <div className="flex flex-col gap-3">
          {parsed.grantId && (
            <div className="flex flex-wrap gap-1.5">
              <span className="inline-flex items-center gap-1 rounded-md border border-border bg-muted/40 px-2 py-0.5 text-[11px]">
                <span className="text-muted-foreground/60">grant</span>
                <span className="font-mono text-foreground">{parsed.grantId}</span>
              </span>
            </div>
          )}
          <Disclosure label="Raw">
            <pre
              tabIndex={0}
              role="region"
              aria-label="Raw result"
              className="mt-1 max-h-[420px] overflow-auto rounded-md border border-border bg-muted/30 p-3 font-mono text-[11px] text-foreground whitespace-pre-wrap [overflow-wrap:anywhere] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {rawJson}
            </pre>
          </Disclosure>
        </div>
      </InspectorZone>
    </InspectorShell>
  );
};

const entry = {
  component: LocalFilesRenderer,
  label: 'Local Files',
  icon: FolderSearch,
  ownsChrome: true,
};

registerToolRenderer('file_search', entry);
registerToolRenderer('local_list_folder', entry);
registerToolRenderer('local_search_in_grant', entry);
registerToolRenderer('local_list_grant_folder', entry);

export default LocalFilesRenderer;
