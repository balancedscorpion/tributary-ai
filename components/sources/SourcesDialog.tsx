import React from 'react';
import {
  ExternalLink,
  Eye,
  EyeOff,
  FolderSearch,
  FolderTree,
  FolderUp,
  GitBranch,
  Loader2,
  Plus,
  RefreshCw,
  Settings2,
  Trash2,
  Unlink,
  Upload,
} from 'lucide-react';
import { toast } from 'sonner';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu';
import { Input } from '@/components/ui/input';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import type { AppSettings, Artifact } from '@/types';
import { displayPath } from '@/lib/approvalPresentation';
import {
  dedupeLocalFolders,
  ensureLocalComputerGrant,
  getLocalComputer,
  listLocalComputerGrantRules,
  normalizeLocalPathKey,
} from '@/lib/localComputerGrants';
import { formatAbsoluteDateTime, formatBytes, formatRelativeTime } from '@/lib/formatters';
import { cn } from '@/lib/utils';
import { isLocalPickerCanceled, pickLocalDirectory } from '@/services/localFilePicker';
import {
  cloneGitHubRepository,
  isGithubReposUnavailable,
  refreshGitHubRepository,
  removeGitHubRepository,
  type CloneRepositoryEvent,
} from '@/services/githubReposApi';
import {
  deleteImportedCopy,
  listSources,
  setImportedCopyHidden,
  stopReferencingLocalFolder,
  type ListSourcesResult,
  type SourceUI,
} from '@/services/sourcesApi';
import GitHubAuthSection from '@/components/sources/GitHubAuthSection';
import GitHubRepoBrowser, { LOCAL_CLONE_CAPTION } from '@/components/sources/GitHubRepoBrowser';

type ProjectSummary = {
  id: string;
  name: string;
  fileCount: number;
  source?: string;
  gitUrl?: string;
  localPath?: string;
  rootRelPath?: string;
};

interface SourcesControlProps {
  settings: AppSettings;
  /** Persist settings without UI side effects (grant/settings sync). */
  onSaveSettings: (settings: AppSettings) => Promise<void> | void;
  currentProjectId?: string | null;
  currentProjectName: string | null;
  currentFileCount: number;
  onProjectLoaded: (project: ProjectSummary | null) => void;
  /** Open a repo file in the app's native viewer. */
  onOpenArtifact: (artifact: Artifact) => void;
  /** Navigate to the Files surface (connector/uploaded file inventory). */
  onOpenFiles: () => void;
  /** Open Settings > Connections on the Local Computer entry. */
  onManageLocalComputer: () => void;
  /** Drop a draft prompt into the composer (repo-search fallback). */
  onAskAgent?: (prompt: string) => void;
}

function normalizeProject(project: any, fallback?: Partial<ProjectSummary>): ProjectSummary {
  const rootRelPath = typeof project?.root_rel_path === 'string' ? project.root_rel_path : fallback?.rootRelPath;
  const source = typeof project?.source === 'string'
    ? project.source
    : (typeof rootRelPath === 'string' && rootRelPath.startsWith('git:') ? 'git' : fallback?.source);
  return {
    id: String(project?.id || fallback?.id || '').trim(),
    name: String(project?.name || fallback?.name || 'Project').trim() || 'Project',
    fileCount: Number(project?.file_count ?? fallback?.fileCount ?? 0) || 0,
    source,
    gitUrl: typeof project?.git_url === 'string' ? project.git_url : fallback?.gitUrl,
    localPath: typeof project?.local_path === 'string' ? project.local_path : fallback?.localPath,
    rootRelPath,
  };
}

interface PendingOperation {
  kind: 'clone' | 'upload';
  title: string;
  detail?: string;
  percent?: number;
}

type ConfirmMode = 'stop-local' | 'stop-repo' | 'delete-copy';

interface ConfirmTarget {
  source: SourceUI;
  mode: ConfirmMode;
}

interface SourceGroup {
  key: 'local' | 'github' | 'copies';
  heading: string;
  items: SourceUI[];
}

/**
 * Individual files (connector downloads, chat uploads) are managed in the
 * Files surface, not here — the unified registry can return them, but this
 * dialog is about collections (folders, clones, imported directories). They
 * are surfaced through the footer count instead of being silently dropped.
 */
function isFileLevelSource(source: SourceUI): boolean {
  return source.kind === 'connector_file' || source.kind === 'thread_upload';
}

function groupSources(sources: SourceUI[]): SourceGroup[] {
  const collections = sources.filter((s) => !isFileLevelSource(s));
  return [
    {
      key: 'local' as const,
      heading: 'This computer',
      items: collections.filter((s) => s.kind === 'local_reference'),
    },
    {
      key: 'github' as const,
      heading: 'GitHub repositories',
      items: collections.filter((s) => s.kind === 'github_clone' && s.semantics === 'reference'),
    },
    {
      key: 'copies' as const,
      heading: 'Imported copies',
      items: collections.filter((s) => s.semantics === 'copy'),
    },
  ];
}

function sourceCaption(source: SourceUI): string {
  if (source.kind === 'local_reference') {
    if (source.wholeComputer) return 'Read-only reference · everything except protected paths';
    return `Read-only reference · ${displayPath(source.path || '')}`;
  }
  if (source.semantics === 'reference') {
    const parts = [LOCAL_CLONE_CAPTION];
    if (source.ref) parts.push(source.ref);
    return parts.join(' · ');
  }
  const facts: string[] = ['Imported snapshot — won’t update'];
  if (source.fileCount !== undefined) facts.push(`${source.fileCount} files`);
  if (source.byteSize !== undefined && source.byteSize > 0) facts.push(formatBytes(source.byteSize));
  return facts.join(' · ');
}

function sourceGlyph(source: SourceUI): React.ReactNode {
  if (source.kind === 'local_reference') {
    return <img src="/brand-logos/local-computer.svg" alt="" aria-hidden="true" className="h-4 w-4" />;
  }
  if (source.kind === 'github_clone') {
    return <img src="/brand-logos/github.svg" alt="" aria-hidden="true" className="h-4 w-4 dark:invert" />;
  }
  return <FolderUp size={16} aria-hidden="true" className="text-muted-foreground" />;
}

const SourcesControl: React.FC<SourcesControlProps> = ({
  settings,
  onSaveSettings,
  currentProjectId,
  currentProjectName,
  currentFileCount,
  onProjectLoaded,
  onOpenArtifact,
  onOpenFiles,
  onManageLocalComputer,
  onAskAgent,
}) => {
  const [isOpen, setIsOpen] = React.useState(false);
  const [view, setView] = React.useState<{ name: 'list' } | { name: 'repo'; source: SourceUI }>({ name: 'list' });
  const [result, setResult] = React.useState<ListSourcesResult | null>(null);
  const [loading, setLoading] = React.useState(false);
  const [addRepoOpen, setAddRepoOpen] = React.useState(false);
  const [gitUrl, setGitUrl] = React.useState('');
  const [gitName, setGitName] = React.useState('');
  const [cloneError, setCloneError] = React.useState<{ kind: 'unavailable' | 'error'; message: string } | null>(null);
  const [pendingOp, setPendingOp] = React.useState<PendingOperation | null>(null);
  const [confirmTarget, setConfirmTarget] = React.useState<ConfirmTarget | null>(null);
  const [confirming, setConfirming] = React.useState(false);
  const [rowBusy, setRowBusy] = React.useState<string | null>(null);
  const [announcement, setAnnouncement] = React.useState('');

  const uploadInputRef = React.useRef<HTMLInputElement>(null);
  const addSourceTriggerRef = React.useRef<HTMLButtonElement>(null);
  const repoUrlInputRef = React.useRef<HTMLInputElement>(null);
  const cloneAlertRef = React.useRef<HTMLDivElement>(null);
  const listContainerRef = React.useRef<HTMLDivElement>(null);
  const announceTimerRef = React.useRef<number | null>(null);
  // Where focus should land after the confirm dialog closes: the position of
  // the removed row (stable-key capture happens before removal).
  const pendingConfirmFocusIndex = React.useRef<number | null>(null);
  // Which repo row Browse was pressed on, so Back can restore focus to it.
  const browsedFromSourceId = React.useRef<string | null>(null);
  const menuSelectionRef = React.useRef<'repo' | null>(null);

  const announce = React.useCallback((message: string) => {
    setAnnouncement(message);
    if (announceTimerRef.current !== null) window.clearTimeout(announceTimerRef.current);
    announceTimerRef.current = window.setTimeout(() => setAnnouncement(''), 2500);
  }, []);

  React.useEffect(() => () => {
    if (announceTimerRef.current !== null) window.clearTimeout(announceTimerRef.current);
  }, []);

  const loadSources = React.useCallback(async (projectIdOverride?: string | null) => {
    setLoading(true);
    try {
      const next = await listSources({
        projectId: projectIdOverride !== undefined ? projectIdOverride : currentProjectId,
      });
      setResult(next);
    } catch (e: any) {
      if (e?.name !== 'AbortError') {
        setResult({ sources: [], unified: false, errors: [{ section: 'local', message: e?.message || 'Failed to load sources.' }] });
      }
    } finally {
      setLoading(false);
    }
  }, [currentProjectId]);

  // Warm count for the header badge; errors stay silent out here.
  React.useEffect(() => {
    void loadSources().catch(() => {});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  React.useEffect(() => {
    if (!isOpen) return;
    setView({ name: 'list' });
    setAddRepoOpen(false);
    setCloneError(null);
    announce('Loading sources');
    void loadSources();
  }, [announce, isOpen, loadSources]);

  React.useEffect(() => {
    if (cloneError?.kind === 'unavailable') cloneAlertRef.current?.focus();
  }, [cloneError]);

  const sources = result?.sources || [];
  const groups = groupSources(sources);
  const fileLevelCount = sources.filter(isFileLevelSource).length;
  const sourceCount = sources.length - fileLevelCount;
  const isBlockingOp = pendingOp !== null;

  // ------------------------------------------------------------------
  // Add: folder on this computer (native picker -> grant + settings sync)
  // ------------------------------------------------------------------
  const handleAddFolder = React.useCallback(async () => {
    try {
      const picked = await pickLocalDirectory();
      setRowBusy('add-folder');
      const rules = await listLocalComputerGrantRules();
      await ensureLocalComputerGrant(rules, picked.path);
      const local = getLocalComputer(settings);
      const selectedFolders = dedupeLocalFolders([...(local.selectedFolders || []), picked.path]);
      await onSaveSettings({
        ...settings,
        localComputer: {
          ...local,
          enabled: true,
          // An existing whole-computer choice stays authoritative; otherwise
          // the picked folder implies selected-folders mode.
          accessMode: local.enabled && local.accessMode === 'whole-computer' ? 'whole-computer' : 'selected-folders',
          selectedFolders,
        },
      });
      announce(`Added ${picked.displayName || displayPath(picked.path)}`);
      toast.success(`Referencing ${picked.displayName || displayPath(picked.path)} (read-only).`);
      await loadSources();
    } catch (e: any) {
      if (!isLocalPickerCanceled(e)) {
        toast.error(e?.message || 'Could not add the folder.');
      }
    } finally {
      setRowBusy(null);
    }
  }, [announce, loadSources, onSaveSettings, settings]);

  // ------------------------------------------------------------------
  // Add: GitHub repository (app-managed local clone)
  // ------------------------------------------------------------------
  const handleClone = React.useCallback(async () => {
    const url = gitUrl.trim();
    if (!url) {
      announce('Enter a repository URL first');
      repoUrlInputRef.current?.focus();
      return;
    }
    setCloneError(null);
    const displayName = gitName.trim() || url.replace(/^https?:\/\/github\.com\//, '').replace(/\.git$/, '');
    setPendingOp({
      kind: 'clone',
      title: `Cloning ${displayName}`,
      detail: 'Creating a local clone the assistant can read',
    });
    const onEvent = (event: CloneRepositoryEvent) => {
      setPendingOp({
        kind: 'clone',
        title: event.stage || event.message || `Cloning ${displayName}`,
        detail: event.detail || 'Creating a local clone the assistant can read',
        percent: event.percent,
      });
    };
    try {
      const outcome = await cloneGitHubRepository({
        url,
        name: gitName.trim() || undefined,
        onEvent,
      });
      setGitUrl('');
      setGitName('');
      setAddRepoOpen(false);
      toast.success(`Cloned ${outcome.repository?.fullName || displayName} — pull anytime to update.`);
      announce('Repository added');
      await loadSources();
    } catch (e: any) {
      setCloneError(isGithubReposUnavailable(e)
        ? {
          kind: 'unavailable',
          message: 'This backend can’t clone repositories. Use “Import a copy (upload folder)” from Add source to store a folder snapshot instead — it won’t update.',
        }
        : { kind: 'error', message: e?.message || 'Failed to add the repository.' });
    } finally {
      setPendingOp(null);
    }
  }, [announce, gitName, gitUrl, loadSources]);

  // ------------------------------------------------------------------
  // Add: import a copy (browser directory upload)
  // ------------------------------------------------------------------
  const handleUploadDirectory = React.useCallback(async (event: React.ChangeEvent<HTMLInputElement>) => {
    const fileList = event.target.files;
    if (!fileList || fileList.length === 0) return;
    const UPLOAD_CONCURRENCY = 4;
    const PROGRESS_UPDATE_EVERY = 20;
    try {
      const files = Array.from(fileList);
      const filtered: File[] = [];
      for (const file of files) {
        const relativePath = String(file.webkitRelativePath || file.name || '').replaceAll('\\', '/');
        if (relativePath === '.git' || relativePath.startsWith('.git/') || relativePath.includes('/.git/')) continue;
        const isLikelyBinary = /\.(png|jpg|jpeg|gif|ico|pdf|zip|tar|gz|woff|woff2|eot|ttf|mp3|mp4)$/i.test(file.name);
        if (isLikelyBinary) continue;
        filtered.push(file);
      }
      if (filtered.length === 0) {
        toast.error('No importable files found in the selected directory.');
        return;
      }
      const rootName = filtered
        .map((file) => (file.webkitRelativePath || file.name || '').split('/')[0]?.trim())
        .find(Boolean) || currentProjectName || 'Project';

      setPendingOp({ kind: 'upload', title: `Importing ${rootName}`, detail: `0 of ${filtered.length} files` });

      let projectId = String(currentProjectId || '').trim();
      let projectName = String(currentProjectName || '').trim() || rootName;
      if (!projectId) {
        const createRes = await fetch('/api/projects', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
          body: JSON.stringify({ name: projectName }),
        });
        const created = await createRes.json().catch(() => ({}));
        if (!createRes.ok) {
          throw new Error(created?.error || `Import failed (HTTP ${createRes.status})`);
        }
        projectId = String(created?.project?.id || '').trim();
        if (!projectId) throw new Error('Backend did not return a project id.');
        projectName = String(created?.project?.name ?? projectName);
      }

      let idx = 0;
      let uploaded = 0;
      const uploadOne = async (file: File) => {
        const relativePath = String(file.webkitRelativePath || file.name || '')
          .replaceAll('\\', '/')
          .replace(/^\/+/, '')
          .trim();
        if (!relativePath) return;
        const res = await fetch(`/api/projects/${encodeURIComponent(projectId)}/files?path=${encodeURIComponent(relativePath)}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/octet-stream' },
          body: file,
        });
        if (!res.ok) {
          const data = await res.json().catch(() => ({}));
          throw new Error(data?.error || `Import failed (HTTP ${res.status})`);
        }
      };
      const workers = Array.from({ length: Math.min(UPLOAD_CONCURRENCY, filtered.length) }, async () => {
        while (true) {
          const i = idx;
          idx++;
          if (i >= filtered.length) break;
          await uploadOne(filtered[i]);
          uploaded++;
          if (uploaded === filtered.length || uploaded % PROGRESS_UPDATE_EVERY === 0) {
            setPendingOp({
              kind: 'upload',
              title: `Importing ${rootName}`,
              detail: `${uploaded} of ${filtered.length} files`,
              percent: Math.round((uploaded / filtered.length) * 100),
            });
          }
        }
      });
      await Promise.all(workers);

      const finRes = await fetch(`/api/projects/${encodeURIComponent(projectId)}/finalize`, {
        method: 'POST',
        headers: { Accept: 'application/json' },
      });
      const fin = await finRes.json().catch(() => ({}));
      if (!finRes.ok) {
        throw new Error(fin?.error || `Import failed (HTTP ${finRes.status})`);
      }
      const project = normalizeProject(fin?.project, { id: projectId, name: projectName, fileCount: filtered.length, source: 'upload' });
      onProjectLoaded(project);
      toast.success(`Imported a copy of ${rootName} (snapshot — won’t update).`);
      announce('Import complete');
      await loadSources(projectId);
    } catch (e: any) {
      toast.error(e?.message || 'Failed to import the directory.');
    } finally {
      setPendingOp(null);
      if (uploadInputRef.current) uploadInputRef.current.value = '';
    }
  }, [announce, currentProjectId, currentProjectName, loadSources, onProjectLoaded]);

  // ------------------------------------------------------------------
  // Row actions
  // ------------------------------------------------------------------
  const captureConfirmFocusIndex = React.useCallback((source: SourceUI) => {
    const rows = listContainerRef.current?.querySelectorAll<HTMLElement>('[data-source-row]');
    if (!rows) return;
    pendingConfirmFocusIndex.current = Array.from(rows).findIndex(
      (row) => row.getAttribute('data-source-row') === source.id,
    );
  }, []);

  const handleConfirm = React.useCallback(async () => {
    if (!confirmTarget || confirming) return;
    const { source, mode } = confirmTarget;
    setConfirming(true);
    try {
      captureConfirmFocusIndex(source);
      if (mode === 'stop-local') {
        await stopReferencingLocalFolder(source);
        const local = getLocalComputer(settings);
        const key = normalizeLocalPathKey(source.path || '');
        await onSaveSettings({
          ...settings,
          localComputer: {
            ...local,
            selectedFolders: (local.selectedFolders || []).filter(
              (folder) => normalizeLocalPathKey(folder) !== key,
            ),
          },
        });
        toast.success(`Stopped referencing ${source.title}. Your files are untouched.`);
      } else if (mode === 'stop-repo') {
        if (!source.repo) throw new Error('This repository is not managed by the app.');
        await removeGitHubRepository(source.repo.id);
        toast.success(`Stopped referencing ${source.title}.`);
      } else {
        const outcome = await deleteImportedCopy(source);
        if (outcome.project) {
          onProjectLoaded(normalizeProject(outcome.project, {
            id: source.projectId || undefined,
            name: currentProjectName || 'Project',
            fileCount: currentFileCount,
          }));
        }
        toast.success(`Deleted the imported copy of ${source.title}. Originals are unaffected.`);
      }
      announce(mode === 'delete-copy' ? 'Imported copy deleted' : 'Reference removed');
      setConfirmTarget(null);
      await loadSources();
    } catch (e: any) {
      toast.error(e?.message || 'The action failed.');
    } finally {
      setConfirming(false);
    }
  }, [announce, captureConfirmFocusIndex, confirmTarget, confirming, currentFileCount, currentProjectName, loadSources, onProjectLoaded, onSaveSettings, settings]);

  const handleConfirmDialogCloseFocus = React.useCallback((event: Event) => {
    const index = pendingConfirmFocusIndex.current;
    if (index === null) return;
    pendingConfirmFocusIndex.current = null;
    event.preventDefault();
    const rows = listContainerRef.current?.querySelectorAll<HTMLElement>('[data-source-row]');
    if (rows && rows.length > 0) {
      const row = rows[Math.min(Math.max(index, 0), rows.length - 1)];
      const target = row.querySelector<HTMLElement>('button, a[href]');
      if (target) {
        target.focus();
        return;
      }
    }
    addSourceTriggerRef.current?.focus();
  }, []);

  const handleToggleHidden = React.useCallback(async (source: SourceUI) => {
    setRowBusy(`${source.id}:hide`);
    try {
      const outcome = await setImportedCopyHidden(source, !source.hidden);
      if (outcome.project) {
        onProjectLoaded(normalizeProject(outcome.project, {
          id: source.projectId || undefined,
          name: currentProjectName || 'Project',
          fileCount: currentFileCount,
        }));
      }
      toast.success(source.hidden ? 'Copy visible to the assistant.' : 'Copy hidden from the assistant.');
      await loadSources();
    } catch (e: any) {
      toast.error(e?.message || 'Failed to update the copy.');
    } finally {
      setRowBusy(null);
    }
  }, [currentFileCount, currentProjectName, loadSources, onProjectLoaded]);

  const handleRefreshLegacyRepos = React.useCallback(async (source: SourceUI) => {
    const projectId = String(source.projectId || '').trim();
    if (!projectId) return;
    setRowBusy(`${source.id}:refresh`);
    try {
      const res = await fetch(`/api/projects/${encodeURIComponent(projectId)}/refresh`, {
        method: 'POST',
        headers: { Accept: 'application/json' },
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(data?.error || `Refresh failed (HTTP ${res.status})`);
      onProjectLoaded(normalizeProject(data?.project, {
        id: projectId,
        name: currentProjectName || 'Project',
        fileCount: currentFileCount,
      }));
      toast.success('Snapshot refreshed from its repository.');
      await loadSources();
    } catch (e: any) {
      toast.error(e?.message || 'Failed to refresh the snapshot.');
    } finally {
      setRowBusy(null);
    }
  }, [currentFileCount, currentProjectName, loadSources, onProjectLoaded]);

  const handlePullRepo = React.useCallback(async (source: SourceUI) => {
    if (!source.repo) return;
    setRowBusy(`${source.id}:refresh`);
    try {
      await refreshGitHubRepository(source.repo.id);
      toast.success(`Pulled the latest changes into the local clone of ${source.title}.`);
      await loadSources();
    } catch (e: any) {
      toast.error(e?.message || 'Failed to pull the latest changes.');
    } finally {
      setRowBusy(null);
    }
  }, [loadSources]);

  const handleBrowse = React.useCallback((source: SourceUI) => {
    browsedFromSourceId.current = source.id;
    setView({ name: 'repo', source });
  }, []);

  const handleBrowserBack = React.useCallback(() => {
    const fromId = browsedFromSourceId.current;
    browsedFromSourceId.current = null;
    setView({ name: 'list' });
    // The list re-renders on the next frame; restore focus to the Browse
    // control on the originating row (stable key), else the Add trigger.
    window.setTimeout(() => {
      const target = fromId
        ? listContainerRef.current?.querySelector<HTMLElement>(`[data-source-row="${CSS.escape(fromId)}"] [data-source-browse]`)
        : null;
      (target || addSourceTriggerRef.current)?.focus();
    }, 0);
  }, []);

  const handleOpenRepoFile = React.useCallback((artifact: Artifact) => {
    setIsOpen(false);
    onOpenArtifact(artifact);
  }, [onOpenArtifact]);

  const handleAskAgent = React.useCallback((prompt: string) => {
    if (!onAskAgent) return;
    setIsOpen(false);
    onAskAgent(prompt);
  }, [onAskAgent]);

  // ------------------------------------------------------------------
  // Rendering
  // ------------------------------------------------------------------
  const renderRow = (source: SourceUI) => {
    const caption = sourceCaption(source);
    const busyKey = (action: string) => rowBusy === `${source.id}:${action}`;
    return (
      <li
        key={source.id}
        className="flex items-center gap-3 px-3 py-2"
        data-source-row={source.id}
        data-source-kind={source.kind}
        data-source-semantics={source.semantics}
      >
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-muted/60">
          {sourceGlyph(source)}
        </div>
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-center gap-2">
            <span className="min-w-0 truncate text-sm font-medium" title={source.path || source.remoteUrl || source.title}>
              {source.title}
            </span>
            {source.hidden && (
              <Badge variant="secondary" className="shrink-0 text-[10px]">Hidden</Badge>
            )}
          </div>
          <div className="mt-0.5 flex min-w-0 flex-wrap items-center gap-x-1.5 text-[11px] text-muted-foreground">
            <span className="min-w-0 truncate">{caption}</span>
            {source.semantics === 'reference' && source.refreshedAt !== undefined && (
              <span className="shrink-0" title={formatAbsoluteDateTime(source.refreshedAt)}>
                · updated {formatRelativeTime(source.refreshedAt)}
              </span>
            )}
          </div>
        </div>
        <div className="flex shrink-0 items-center gap-0.5">
          {source.capabilities.browse && source.repo && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  className="h-8 w-8 text-muted-foreground hover:text-foreground"
                  onClick={() => handleBrowse(source)}
                  aria-label={`Browse the files in ${source.title}`}
                  data-source-browse
                >
                  <FolderTree size={15} aria-hidden="true" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Open the repository tree</TooltipContent>
            </Tooltip>
          )}
          {source.remoteUrl && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button asChild size="icon" variant="ghost" className="h-8 w-8 text-muted-foreground hover:text-foreground">
                  <a
                    href={source.remoteUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    aria-label={`View ${source.title} at its remote source (opens in a new tab)`}
                  >
                    <ExternalLink size={15} aria-hidden="true" />
                  </a>
                </Button>
              </TooltipTrigger>
              <TooltipContent>Open on GitHub</TooltipContent>
            </Tooltip>
          )}
          {source.capabilities.hide && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  className="h-8 w-8 text-muted-foreground hover:text-foreground"
                  onClick={() => void handleToggleHidden(source)}
                  aria-busy={busyKey('hide')}
                  aria-label={source.hidden ? `Show ${source.title} to the assistant` : `Hide ${source.title} from the assistant`}
                >
                  {busyKey('hide')
                    ? <Loader2 size={15} aria-hidden="true" className="motion-safe:animate-spin" />
                    : source.hidden ? <Eye size={15} aria-hidden="true" /> : <EyeOff size={15} aria-hidden="true" />}
                </Button>
              </TooltipTrigger>
              <TooltipContent>{source.hidden ? 'Include in what the assistant reads' : 'Exclude from what the assistant reads'}</TooltipContent>
            </Tooltip>
          )}
          {source.capabilities.refresh && source.semantics === 'copy' && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  className="h-8 w-8 text-muted-foreground hover:text-foreground"
                  onClick={() => void handleRefreshLegacyRepos(source)}
                  aria-busy={busyKey('refresh')}
                  aria-label={`Refresh the snapshot of ${source.title} from its repository`}
                >
                  <RefreshCw size={15} aria-hidden="true" className={busyKey('refresh') ? 'motion-safe:animate-spin' : ''} />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Pull the latest into this snapshot</TooltipContent>
            </Tooltip>
          )}
          {source.capabilities.refresh && source.semantics === 'reference' && source.repo && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  className="h-8 w-8 text-muted-foreground hover:text-foreground"
                  onClick={() => void handlePullRepo(source)}
                  aria-busy={busyKey('refresh')}
                  aria-label={`Pull the latest changes into the local clone of ${source.title}`}
                >
                  <RefreshCw size={15} aria-hidden="true" className={busyKey('refresh') ? 'motion-safe:animate-spin' : ''} />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Pull the latest into this clone</TooltipContent>
            </Tooltip>
          )}
          {source.wholeComputer && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  className="h-8 w-8 text-muted-foreground hover:text-foreground"
                  onClick={onManageLocalComputer}
                  aria-label="Manage whole-computer access in Settings"
                >
                  <Settings2 size={15} aria-hidden="true" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Change access mode in Settings</TooltipContent>
            </Tooltip>
          )}
          {source.capabilities.stopReference && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  className="h-8 w-8 text-muted-foreground hover:text-foreground"
                  onClick={() => setConfirmTarget({
                    source,
                    mode: source.kind === 'github_clone' ? 'stop-repo' : 'stop-local',
                  })}
                  aria-label={`Stop referencing ${source.title}`}
                >
                  <Unlink size={15} aria-hidden="true" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Stop referencing — your files stay put</TooltipContent>
            </Tooltip>
          )}
          {source.capabilities.deleteCopy && (
            <Tooltip>
              <TooltipTrigger asChild>
                <Button
                  type="button"
                  size="icon"
                  variant="ghost"
                  className="h-8 w-8 text-muted-foreground hover:text-destructive"
                  onClick={() => setConfirmTarget({ source, mode: 'delete-copy' })}
                  aria-label={`Delete the imported copy of ${source.title}`}
                >
                  <Trash2 size={15} aria-hidden="true" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>Delete the app&apos;s snapshot</TooltipContent>
            </Tooltip>
          )}
        </div>
      </li>
    );
  };

  const renderPendingRow = (op: PendingOperation) => (
    <li className="px-3 py-2" role="status" aria-busy="true" data-source-pending-row>
      <div className="flex items-center gap-3">
        <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-muted/60">
          <Loader2 size={16} aria-hidden="true" className="motion-safe:animate-spin text-primary" />
        </div>
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium">{op.title}</div>
          <div className="mt-0.5 truncate text-[11px] text-muted-foreground">
            {[op.detail, op.percent !== undefined ? `${Math.max(0, Math.min(100, Math.round(op.percent)))}%` : null]
              .filter(Boolean)
              .join(' · ')}
          </div>
        </div>
      </div>
      <div aria-hidden="true" className="mt-2 h-1.5 overflow-hidden rounded-full bg-muted">
        {op.percent !== undefined ? (
          <div
            className="h-full rounded-full bg-primary transition-all"
            style={{ width: `${Math.max(0, Math.min(100, op.percent))}%` }}
          />
        ) : (
          <div className="h-full w-1/3 rounded-full bg-primary motion-safe:animate-pulse" />
        )}
      </div>
    </li>
  );

  const sectionErrors = result?.errors || [];
  const hasAnySource = sources.length > 0;

  const renderListView = () => (
    <>
      <div className="flex items-center justify-between gap-3">
        <p className="min-w-0 text-sm text-muted-foreground">
          References update live; copies are snapshots.
        </p>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              ref={addSourceTriggerRef}
              type="button"
              size="sm"
              className="shrink-0 gap-1.5"
              aria-label="Add a source"
            >
              <Plus size={15} aria-hidden="true" />
              Add source
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            align="end"
            className="w-80"
            onCloseAutoFocus={(event) => {
              if (menuSelectionRef.current === 'repo') {
                menuSelectionRef.current = null;
                event.preventDefault();
                repoUrlInputRef.current?.focus();
              }
            }}
          >
            <DropdownMenuItem
              className="flex flex-col items-start gap-0.5 py-2"
              onSelect={() => { void handleAddFolder(); }}
            >
              <span className="flex items-center gap-2 text-sm font-medium">
                <img src="/brand-logos/local-computer.svg" alt="" aria-hidden="true" className="h-4 w-4" />
                Folder on this computer
              </span>
              <span className="pl-6 text-xs text-muted-foreground">
                Read-only reference — reads files in place via the system picker
              </span>
            </DropdownMenuItem>
            <DropdownMenuItem
              className="flex flex-col items-start gap-0.5 py-2"
              onSelect={() => {
                menuSelectionRef.current = 'repo';
                setCloneError(null);
                setAddRepoOpen(true);
              }}
            >
              <span className="flex items-center gap-2 text-sm font-medium">
                <img src="/brand-logos/github.svg" alt="" aria-hidden="true" className="h-4 w-4 dark:invert" />
                GitHub repository
              </span>
              <span className="pl-6 text-xs text-muted-foreground">
                A local clone the assistant reads — pull to update
              </span>
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              className="flex flex-col items-start gap-0.5 py-2"
              onSelect={() => uploadInputRef.current?.click()}
            >
              <span className="flex items-center gap-2 text-sm font-medium">
                <Upload size={15} aria-hidden="true" className="text-muted-foreground" />
                Import a copy (upload folder)
              </span>
              <span className="pl-6 text-xs text-muted-foreground">
                Imported snapshot stored by the app — won’t update
              </span>
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>

      {addRepoOpen && (
        <div className="grid gap-2 rounded-md border border-border px-3 py-3" data-add-repo-form>
          <div className="flex items-center gap-2 text-sm font-medium">
            <img src="/brand-logos/github.svg" alt="" aria-hidden="true" className="h-4 w-4 dark:invert" />
            Add a GitHub repository
          </div>
          {cloneError?.kind === 'unavailable' ? (
            <>
              <div
                ref={cloneAlertRef}
                role="alert"
                tabIndex={-1}
                data-clone-unavailable
                className="rounded-md border border-border bg-muted/30 px-3 py-2 text-xs leading-5 text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                {cloneError.message}
              </div>
              <div>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  aria-label="Close the add-repository form"
                  onClick={() => {
                    setAddRepoOpen(false);
                    setCloneError(null);
                    addSourceTriggerRef.current?.focus();
                  }}
                >
                  Close
                </Button>
              </div>
            </>
          ) : (
            <>
              <GitHubAuthSection />
              <Input
                ref={repoUrlInputRef}
                value={gitUrl}
                onChange={(e) => setGitUrl(e.target.value)}
                placeholder="owner/repo or https://github.com/org/repo"
                aria-label="Repository URL or owner/repo"
                autoComplete="off"
              />
              <Input
                value={gitName}
                onChange={(e) => setGitName(e.target.value)}
                placeholder="Display name (optional)"
                aria-label="Display name for the repository"
                autoComplete="off"
              />
              {cloneError?.kind === 'error' && (
                <div role="alert" className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
                  {cloneError.message}
                </div>
              )}
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  size="sm"
                  className="gap-1.5"
                  onClick={() => void handleClone()}
                  aria-busy={pendingOp?.kind === 'clone'}
                  aria-disabled={!gitUrl.trim() || undefined}
                >
                  {pendingOp?.kind === 'clone'
                    ? <Loader2 size={14} aria-hidden="true" className="motion-safe:animate-spin" />
                    : <GitBranch size={14} aria-hidden="true" />}
                  Add repository
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  onClick={() => {
                    setAddRepoOpen(false);
                    setCloneError(null);
                    addSourceTriggerRef.current?.focus();
                  }}
                >
                  Cancel
                </Button>
              </div>
            </>
          )}
        </div>
      )}

      {sectionErrors.length > 0 && (
        <div role="alert" className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
          <span className="min-w-0">
            {sectionErrors.map((error) => error.message).join(' ')}
          </span>
          <Button
            type="button"
            size="sm"
            variant="outline"
            className="h-7 shrink-0 px-2.5 text-xs"
            onClick={() => void loadSources()}
            aria-busy={loading}
          >
            Retry
          </Button>
        </div>
      )}

      <div ref={listContainerRef} className="min-h-0 flex-1 space-y-3 overflow-y-auto pr-0.5">
        {loading && !hasAnySource ? (
          <div aria-hidden="true" className="space-y-2 py-2">
            {[0, 1, 2].map((row) => (
              <div key={row} className="h-12 animate-pulse rounded-md bg-muted/50" />
            ))}
          </div>
        ) : !hasAnySource && !pendingOp ? (
          <p className="px-1 py-6 text-center text-sm text-muted-foreground">
            No sources yet — use <span className="font-medium text-foreground">Add source</span> to
            reference a folder, clone a repository, or import a copy.
          </p>
        ) : (
          groups.map((group) => {
            const showPending = pendingOp && (
              (pendingOp.kind === 'clone' && group.key === 'github') ||
              (pendingOp.kind === 'upload' && group.key === 'copies')
            );
            if (group.items.length === 0 && !showPending) return null;
            const headingId = `sources-group-${group.key}`;
            return (
              <section key={group.key} aria-labelledby={headingId}>
                <h3
                  id={headingId}
                  className="flex items-baseline gap-1.5 px-1 pb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground"
                >
                  {group.heading}
                  <span aria-hidden="true" className="font-normal tabular-nums">{group.items.length || ''}</span>
                  <span className="sr-only">
                    ({group.items.length} {group.items.length === 1 ? 'source' : 'sources'})
                  </span>
                </h3>
                <ul className="divide-y divide-border/70 rounded-md border border-border bg-background">
                  {group.items.map(renderRow)}
                  {showPending && pendingOp && renderPendingRow(pendingOp)}
                </ul>
              </section>
            );
          })
        )}
      </div>

      <div className="flex items-center justify-between gap-2 border-t border-border pt-3 text-xs text-muted-foreground">
        <span className="min-w-0">
          {fileLevelCount > 0
            ? `${fileLevelCount} individual ${fileLevelCount === 1 ? 'file' : 'files'} (from connectors and chat uploads) ${fileLevelCount === 1 ? 'is' : 'are'} in Files.`
            : 'Individual files from connectors and chat uploads live in Files.'}
        </span>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          className="h-7 shrink-0 px-2 text-xs"
          onClick={() => {
            setIsOpen(false);
            onOpenFiles();
          }}
        >
          Open Files
        </Button>
      </div>
    </>
  );

  return (
    <div className="flex items-center">
      <input
        type="file"
        ref={uploadInputRef}
        onChange={(e) => void handleUploadDirectory(e)}
        className="hidden"
        // @ts-ignore - webkitdirectory is non-standard but supported
        webkitdirectory=""
        directory=""
        multiple
      />
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="app-header-source-button gap-2"
            onClick={() => setIsOpen(true)}
            aria-label="Manage sources"
            aria-busy={isBlockingOp}
          >
            {isBlockingOp
              ? <Loader2 size={16} aria-hidden="true" className="motion-safe:animate-spin text-primary" />
              : <FolderSearch size={16} aria-hidden="true" className="text-primary" />}
            <span className="app-header-source-label">
              {sourceCount > 0 ? 'Sources' : 'Add sources'}
            </span>
            {sourceCount > 0 && (
              <>
                <Badge variant="secondary" aria-hidden="true" className="app-header-source-count h-5 px-1.5 text-[10px] tabular-nums">
                  {sourceCount}
                </Badge>
                <span className="sr-only">({sourceCount} configured)</span>
              </>
            )}
          </Button>
        </TooltipTrigger>
        <TooltipContent>Folders, repositories, and copies the assistant can read</TooltipContent>
      </Tooltip>

      <Dialog
        open={isOpen}
        onOpenChange={(open) => {
          // A streaming clone/import has no cancel path; keep the dialog up
          // so its progress row (role=status) explains the wait.
          if (!open && isBlockingOp) return;
          setIsOpen(open);
        }}
      >
        <DialogContent
          className={cn(
            'flex max-h-[85vh] flex-col overflow-hidden',
            view.name === 'repo' ? 'sm:max-w-3xl' : 'sm:max-w-2xl',
          )}
        >
          <div className="sr-only" role="status" aria-live="polite" aria-atomic="true">{announcement}</div>
          <DialogHeader className={view.name === 'repo' ? 'sr-only' : undefined}>
            <DialogTitle>Sources</DialogTitle>
            <DialogDescription>
              Folders, repositories, and copies the assistant can read.
            </DialogDescription>
          </DialogHeader>

          {view.name === 'repo' && view.source.repo ? (
            <GitHubRepoBrowser
              repo={view.source.repo}
              onBack={handleBrowserBack}
              onOpenFile={handleOpenRepoFile}
              announce={announce}
              onAskAgent={onAskAgent ? handleAskAgent : undefined}
            />
          ) : (
            renderListView()
          )}
        </DialogContent>
      </Dialog>

      <Dialog
        open={!!confirmTarget}
        onOpenChange={(open) => { if (!open && !confirming) setConfirmTarget(null); }}
      >
        <DialogContent className="max-w-[440px]" onCloseAutoFocus={handleConfirmDialogCloseFocus}>
          <DialogHeader>
            <DialogTitle>
              {confirmTarget?.mode === 'delete-copy'
                ? `Delete imported copy of ${confirmTarget.source.title}?`
                : `Stop referencing ${confirmTarget?.source.title}?`}
            </DialogTitle>
            <DialogDescription>
              {confirmTarget?.mode === 'delete-copy'
                ? 'This removes the snapshot stored by the app. Your original files are not affected.'
                : confirmTarget?.mode === 'stop-repo'
                  ? 'The app’s local clone is removed. The repository on GitHub is not affected.'
                  : 'Your files stay on your computer. The assistant loses read access to this folder.'}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => setConfirmTarget(null)} disabled={confirming}>
              Cancel
            </Button>
            <Button
              type="button"
              variant={confirmTarget?.mode === 'delete-copy' ? 'destructive' : 'default'}
              onClick={() => void handleConfirm()}
              disabled={confirming}
            >
              {confirming
                ? 'Working...'
                : confirmTarget?.mode === 'delete-copy' ? 'Delete copy' : 'Stop referencing'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
};

export default SourcesControl;
