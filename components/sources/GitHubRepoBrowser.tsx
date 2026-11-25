import React from 'react';
import { ArrowLeft, ChevronRight, ExternalLink, FolderClosed, Loader2, RefreshCw } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import type { Artifact } from '@/types';
import { fileKindGlyph } from '@/lib/fileKindGlyph';
import { formatAbsoluteDateTime, formatBytes, formatRelativeTime } from '@/lib/formatters';
import { cn } from '@/lib/utils';
import {
  fetchGitHubRepoFile,
  getGitHubRepoTree,
  gitHubRepoRawUrl,
  isGithubReposUnavailable,
  refreshGitHubRepository,
  type GitHubRepoUI,
  type GitHubTreeEntryUI,
} from '@/services/githubReposApi';

/**
 * One phrasing for what an app-managed clone is, shared by the source row and
 * the browser header so adjacent screens never explain the same fact twice.
 */
export const LOCAL_CLONE_CAPTION = 'Local clone · reads without GitHub';

interface GitHubRepoBrowserProps {
  repo: GitHubRepoUI;
  /** Return to the sources list. The caller restores focus to the repo row. */
  onBack: () => void;
  /** Open a repo file in the app's native viewer (the caller closes the dialog). */
  onOpenFile: (artifact: Artifact) => void;
  /** Single polite live-region owned by the dialog. */
  announce: (message: string) => void;
  /** Drop an "ask the assistant to search this repo" draft into the composer. */
  onAskAgent?: (prompt: string) => void;
}

interface DirState {
  status: 'loading' | 'loaded' | 'error';
  entries: GitHubTreeEntryUI[];
  error?: string;
  open: boolean;
}

const IMAGE_EXTENSIONS = ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg'];

function fileExtension(name: string): string {
  const clean = String(name || '').toLowerCase();
  const dot = clean.lastIndexOf('.');
  if (dot <= 0 || dot === clean.length - 1) return '';
  return clean.slice(dot + 1);
}

/**
 * Builds the artifact a repo file renders through. Text files carry their
 * content inline (FileRenderer's inline text path); images point the image
 * viewer at the raw endpoint. Binary files never reach here.
 */
function repoFileArtifact(
  repo: GitHubRepoUI,
  entry: GitHubTreeEntryUI,
  file: { text?: string; mimeType?: string; byteSize?: number },
): Artifact {
  const shared = {
    source: 'github_repository',
    repository: repo.fullName || repo.name,
    repo_path: entry.path,
    mimeType: file.mimeType,
    mime_type: file.mimeType,
    sizeBytes: file.byteSize,
    size_bytes: file.byteSize,
  };
  const ext = fileExtension(entry.name);
  if (IMAGE_EXTENSIONS.includes(ext) || (file.mimeType || '').startsWith('image/')) {
    return {
      id: `github:${repo.id}:${entry.path}`,
      type: 'image',
      title: entry.name,
      content: gitHubRepoRawUrl(repo.id, { ref: repo.defaultRef, path: entry.path }),
      metadata: shared,
    };
  }
  return {
    id: `github:${repo.id}:${entry.path}`,
    type: 'file',
    title: entry.name,
    content: file.text || '',
    metadata: { ...shared, isText: true, is_text: true },
  };
}

const TreeSkeleton: React.FC = () => (
  <div aria-hidden="true" className="space-y-2 px-3 py-3">
    {[0, 1, 2, 3, 4].map((row) => (
      <div key={row} className="h-4 animate-pulse rounded bg-muted/60" style={{ width: `${72 - row * 9}%` }} />
    ))}
  </div>
);

const GitHubRepoBrowser: React.FC<GitHubRepoBrowserProps> = ({
  repo,
  onBack,
  onOpenFile,
  announce,
  onAskAgent,
}) => {
  const backButtonRef = React.useRef<HTMLButtonElement>(null);
  const [dirs, setDirs] = React.useState<Map<string, DirState>>(new Map());
  const [browseUnavailable, setBrowseUnavailable] = React.useState(false);
  const [openingPath, setOpeningPath] = React.useState<string | null>(null);
  const [refreshing, setRefreshing] = React.useState(false);
  const [repoMeta, setRepoMeta] = React.useState<GitHubRepoUI>(repo);
  const treeAbortRef = React.useRef<AbortController | null>(null);

  // The drill-in swaps content inside the same DialogContent, so radix does
  // not move focus for us: land on Back so Esc/Shift-Tab keep working.
  React.useEffect(() => {
    backButtonRef.current?.focus();
    announce(`Browsing ${repo.fullName || repo.name}`);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  React.useEffect(() => () => treeAbortRef.current?.abort(), []);

  const loadDir = React.useCallback(async (path: string) => {
    setDirs((prev) => {
      const next = new Map(prev);
      next.set(path, { status: 'loading', entries: [], open: true });
      return next;
    });
    const dirLabel = path ? path.split('/').filter(Boolean).pop() || path : (repo.fullName || repo.name);
    // The root load follows the "Browsing {repo}" announcement in the same
    // live region; announcing it too would clobber that before it is voiced.
    if (path) announce(`Loading ${dirLabel}`);
    const controller = new AbortController();
    treeAbortRef.current = controller;
    try {
      const result = await getGitHubRepoTree(repo.id, {
        ref: repo.defaultRef,
        path: path || undefined,
        signal: controller.signal,
      });
      setDirs((prev) => {
        const next = new Map(prev);
        next.set(path, { status: 'loaded', entries: result.entries, open: true });
        return next;
      });
      announce(result.entries.length === 0 ? 'Empty folder' : `${result.entries.length} items`);
    } catch (e: any) {
      if (e?.name === 'AbortError') return;
      if (isGithubReposUnavailable(e)) {
        setBrowseUnavailable(true);
        return;
      }
      setDirs((prev) => {
        const next = new Map(prev);
        next.set(path, { status: 'error', entries: [], open: true, error: e?.message || 'Could not load this folder.' });
        return next;
      });
      announce(`Couldn't load ${dirLabel}`);
    }
  }, [announce, repo.defaultRef, repo.fullName, repo.id, repo.name]);

  React.useEffect(() => {
    void loadDir('');
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const toggleDir = React.useCallback((path: string) => {
    const state = dirs.get(path);
    if (!state) {
      void loadDir(path);
      return;
    }
    if (state.status === 'error') {
      void loadDir(path);
      return;
    }
    setDirs((prev) => {
      const next = new Map(prev);
      next.set(path, { ...state, open: !state.open });
      return next;
    });
  }, [dirs, loadDir]);

  const openFile = React.useCallback(async (entry: GitHubTreeEntryUI) => {
    if (openingPath) return;
    setOpeningPath(entry.path);
    try {
      const file = await fetchGitHubRepoFile(repo.id, { ref: repo.defaultRef, path: entry.path });
      if (file.binary && !(file.mimeType || '').startsWith('image/')) {
        // No inline viewer for this payload: the browser's own handling of
        // the raw endpoint (PDF viewer, download) is the honest fallback.
        window.open(gitHubRepoRawUrl(repo.id, { ref: repo.defaultRef, path: entry.path }), '_blank', 'noopener');
        announce(`Opened ${entry.name} in a new tab`);
        return;
      }
      onOpenFile(repoFileArtifact(repo, entry, file));
    } catch (e: any) {
      toast.error(e?.message || `Couldn't open ${entry.name}.`);
    } finally {
      setOpeningPath(null);
    }
  }, [announce, onOpenFile, openingPath, repo]);

  const handleRefresh = React.useCallback(async () => {
    if (refreshing) return;
    setRefreshing(true);
    try {
      const updated = await refreshGitHubRepository(repo.id);
      if (updated) setRepoMeta(updated);
      setDirs(new Map());
      setBrowseUnavailable(false);
      await loadDir('');
      toast.success(`Refreshed ${repo.fullName || repo.name}.`);
    } catch (e: any) {
      toast.error(e?.message || 'Could not refresh the repository.');
    } finally {
      setRefreshing(false);
    }
  }, [loadDir, refreshing, repo.fullName, repo.id, repo.name]);

  const renderEntries = (path: string, level: number): React.ReactNode => {
    const state = dirs.get(path);
    if (!state) return null;
    if (state.status === 'loading') {
      return level === 0 ? <TreeSkeleton /> : (
        <div aria-hidden="true" className="py-1 pl-4">
          <div className="h-4 w-2/5 animate-pulse rounded bg-muted/60" />
        </div>
      );
    }
    if (state.status === 'error') {
      return (
        <div className="flex items-center gap-2 px-3 py-2 text-xs text-destructive">
          <span className="min-w-0">{state.error}</span>
          <Button type="button" size="sm" variant="outline" className="h-7 px-2 text-xs" onClick={() => void loadDir(path)}>
            Retry
          </Button>
        </div>
      );
    }
    if (state.entries.length === 0) {
      return <div className="px-3 py-2 text-xs text-muted-foreground">Empty folder</div>;
    }
    return (
      <ul className={cn('min-w-0', level === 0 ? 'py-1' : '')}>
        {state.entries.map((entry) => {
          if (entry.type === 'dir') {
            const childState = dirs.get(entry.path);
            const open = childState?.open === true;
            const busy = childState?.status === 'loading';
            return (
              <li key={entry.path} className="min-w-0">
                <button
                  type="button"
                  aria-expanded={open}
                  aria-busy={busy || undefined}
                  aria-label={`${entry.name}, folder`}
                  className="flex h-8 w-full min-w-0 items-center gap-1.5 rounded-sm px-2 text-left text-sm hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  style={{ paddingLeft: `${8 + level * 16}px` }}
                  onClick={() => toggleDir(entry.path)}
                >
                  <ChevronRight
                    size={14}
                    aria-hidden="true"
                    className={cn('shrink-0 text-muted-foreground motion-safe:transition-transform', open && 'rotate-90')}
                  />
                  {busy
                    ? <Loader2 size={15} aria-hidden="true" className="shrink-0 text-muted-foreground motion-safe:animate-spin" />
                    : <FolderClosed size={15} aria-hidden="true" className="shrink-0 text-muted-foreground" />}
                  <span className="min-w-0 truncate">{entry.name}</span>
                </button>
                {open && renderEntries(entry.path, level + 1)}
              </li>
            );
          }
          const FileGlyph = fileKindGlyph(undefined, undefined, entry.name);
          const busy = openingPath === entry.path;
          return (
            <li key={entry.path} className="min-w-0">
              <button
                type="button"
                aria-label={`${entry.name}, file`}
                aria-busy={busy || undefined}
                className="flex h-8 w-full min-w-0 items-center gap-1.5 rounded-sm px-2 text-left text-sm hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                style={{ paddingLeft: `${8 + level * 16 + 20}px` }}
                onClick={() => void openFile(entry)}
              >
                {busy
                  ? <Loader2 size={15} aria-hidden="true" className="shrink-0 text-muted-foreground motion-safe:animate-spin" />
                  : <FileGlyph size={15} aria-hidden="true" className="shrink-0 text-muted-foreground" />}
                <span className="min-w-0 flex-1 truncate">{entry.name}</span>
                {entry.byteSize !== undefined && (
                  <span className="shrink-0 pl-2 text-[11px] tabular-nums text-muted-foreground">{formatBytes(entry.byteSize)}</span>
                )}
              </button>
            </li>
          );
        })}
      </ul>
    );
  };

  const meta = repoMeta;
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-3" data-github-repo-browser>
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 items-start gap-2">
          <Button
            ref={backButtonRef}
            type="button"
            variant="ghost"
            size="icon"
            className="mt-0.5 h-8 w-8 shrink-0 text-muted-foreground hover:text-foreground"
            onClick={onBack}
            aria-label="Back to the sources list"
          >
            <ArrowLeft size={16} aria-hidden="true" />
          </Button>
          <div className="min-w-0">
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <img src="/brand-logos/github.svg" alt="" aria-hidden="true" className="h-4 w-4 shrink-0 dark:invert" />
              <h3 className="min-w-0 truncate text-sm font-semibold">{meta.fullName || meta.name}</h3>
              {meta.defaultRef && (
                <Badge variant="secondary" className="h-5 px-1.5 font-mono text-[10px]">{meta.defaultRef}</Badge>
              )}
            </div>
            <div className="mt-0.5 flex flex-wrap items-center gap-x-1.5 text-[11px] text-muted-foreground">
              <span>{LOCAL_CLONE_CAPTION}</span>
              {meta.lastFetchedAt !== undefined && (
                <>
                  <span aria-hidden="true" className="select-none text-muted-foreground/50">·</span>
                  <span title={formatAbsoluteDateTime(meta.lastFetchedAt)}>
                    updated {formatRelativeTime(meta.lastFetchedAt)}
                  </span>
                </>
              )}
              {meta.byteSize !== undefined && (
                <>
                  <span aria-hidden="true" className="select-none text-muted-foreground/50">·</span>
                  <span className="tabular-nums">{formatBytes(meta.byteSize)}</span>
                </>
              )}
            </div>
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-1">
          {meta.remoteUrl && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button asChild size="icon" variant="ghost" className="h-8 w-8 text-muted-foreground hover:text-foreground">
                  <a
                    href={meta.remoteUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    aria-label={`View ${meta.fullName || meta.name} on GitHub (opens in a new tab)`}
                  >
                    <ExternalLink size={15} aria-hidden="true" />
                  </a>
                </Button>
              </TooltipTrigger>
              <TooltipContent>Open the repository on GitHub</TooltipContent>
            </Tooltip>
          )}
          <Tooltip>
            <TooltipTrigger asChild>
              <Button
                type="button"
                size="icon"
                variant="ghost"
                className="h-8 w-8 text-muted-foreground hover:text-foreground"
                onClick={() => void handleRefresh()}
                aria-busy={refreshing}
                aria-label={`Refresh ${meta.fullName || meta.name} from its remote`}
              >
                <RefreshCw size={15} aria-hidden="true" className={refreshing ? 'motion-safe:animate-spin' : ''} />
              </Button>
            </TooltipTrigger>
            <TooltipContent>Pull the latest changes</TooltipContent>
          </Tooltip>
        </div>
      </div>

      {browseUnavailable ? (
        <div className="flex flex-col items-center justify-center gap-2 rounded-md border border-border bg-muted/20 px-4 py-8 text-center">
          <p className="text-sm font-medium text-foreground">File browsing needs the updated backend</p>
          <p className="max-w-md text-xs leading-5 text-muted-foreground">
            This build can&apos;t list the repository&apos;s files yet. The assistant can still search and read it for you.
          </p>
          {onAskAgent && (
            <Button
              type="button"
              size="sm"
              variant="outline"
              className="mt-1"
              onClick={() => onAskAgent(
                `Search the ${meta.fullName || meta.name} repository for `,
              )}
            >
              Ask the assistant to search this repository
            </Button>
          )}
        </div>
      ) : (
        <div
          className="min-h-0 flex-1 overflow-auto rounded-md border border-border bg-background"
          data-github-repo-tree
        >
          {renderEntries('', 0)}
        </div>
      )}
    </div>
  );
};

export default GitHubRepoBrowser;
