import React, { useState, useEffect, useRef, useCallback, useMemo } from 'react';
import { TooltipProvider } from '@/components/ui/tooltip';
import { Toaster, toast } from 'sonner';
import { ArrowLeft, FileText, Plus, Loader2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import type {
  Artifact,
  ChatFileAttachment,
  ConnectionCatalogId,
  EvaluatedFileCapabilities,
  MessageBlockReference,
  ThreadReference,
  TrackedQuery,
} from '@/types';
import { useSettings } from '@/hooks/useSettings';
import { useConnectionHealth } from '@/hooks/useConnectionHealth';
import { useChat, type RunStreamStatus } from '@/hooks/useChat';
import { useQueryCompletionHydration } from '@/hooks/useQueryCompletionHydration';
import { withConnectionEnabled } from '@/lib/connectionEnablement';
import { buildArtifactReferenceContext } from '@/lib/artifactPreview';
import { fileArtifactCardPresentation, fileArtifactRenderedPreviewUrl } from '@/lib/attachmentPresentation';
import { buildRemoteFileDownloadPrompt, type FileArtifactProvenance } from '@/lib/fileProvenance';
import {
  markRemoteFileDownloadRequested,
  notifyRemoteFileDownloadRunCompleted,
} from '@/lib/remoteFileDownloadRequests';
import type { FileAuditRecord } from '@/services/fileAuditApi';
import { requestToolCallReveal } from '@/lib/toolCallReveal';
import { ToolActivityThreadContext } from '@/lib/toolActivityPrefs';
import { buildMessageReferenceContext } from '@/lib/messageReferencePreview';
import { getDesktopToken } from './services/apiClient';
import Header from '@/components/layout/Header';
import TaskBar from '@/components/layout/TaskBar';
import ThreadsSidebar from '@/components/layout/ThreadsSidebar';
import MessageList from '@/components/chat/MessageList';
import CollapseAllToggle from '@/components/chat/toolActivity/CollapseAllToggle';
import ChatInput, { type ImageAttachment } from '@/components/ChatInput';
import TargetPill from '@/components/chat/TargetPill';
import PreflightSheet from '@/components/chat/PreflightSheet';
import PreflightNoticeRow from '@/components/chat/PreflightNoticeRow';
import { getCapabilityUxFlags } from '@/components/settings/providers/capabilityUxFlags';
import SettingsDialog from '@/components/settings/SettingsDialog';
import FilesAuditView from '@/components/files/FilesAuditView';
import PendingApprovalsStrip from '@/components/approvals/PendingApprovalsStrip';
import ArtifactPanel from '@/components/ArtifactPanel';
import InspectorOverlay from '@/components/InspectorOverlay';
import ThreadReferencePickerDialog from '@/components/chat/ThreadReferencePickerDialog';
import { ChatActionsContext, type ChatActions } from '@/components/chat/ChatActionsContext';
import {
  applyUiTextZoom,
  getUiTextZoomShortcutDirection,
  persistUiTextZoom,
  readStoredUiTextZoom,
  stepUiTextZoom,
} from '@/lib/uiTextZoom';

const PINNED_THREADS_STORAGE_KEY = 'ai_analyst_pinned_thread_ids_v1';

type AppView = 'chat' | 'files';

type ThreadComposerState = {
  pinnedArtifactIds: string[];
  pinnedMessageReferences: MessageBlockReference[];
  pinnedThreadReferences: ThreadReference[];
  pendingImages: Array<ImageAttachment & { artifactId: string }>;
  pendingFiles: ChatFileAttachment[];
  uiArtifactsById: Record<string, Artifact>;
};

type ProjectSummary = {
  id: string;
  name: string;
  fileCount: number;
  source?: string;
  gitUrl?: string;
  localPath?: string;
  rootRelPath?: string;
};

function formatRunStatusDuration(ms: number): string {
  const seconds = Math.max(0, Math.round(ms / 1000));
  if (seconds < 60) return `${seconds}s`;
  const minutes = Math.floor(seconds / 60);
  const remainder = seconds % 60;
  if (minutes < 60) return remainder ? `${minutes}m ${remainder}s` : `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  const minuteRemainder = minutes % 60;
  return minuteRemainder ? `${hours}h ${minuteRemainder}m` : `${hours}h`;
}

function formatRunStatusSince(ts?: number): string {
  if (!ts || !Number.isFinite(ts)) return 'never';
  return `${formatRunStatusDuration(Date.now() - ts)} ago`;
}

function getRunStreamStatusLabel(status: RunStreamStatus | null): string {
  if (!status) return 'Running';
  if (status.diagnostic?.kind === 'heartbeat_without_progress') {
    return `No progress for ${formatRunStatusDuration(status.diagnostic.durationMs || 0)}`;
  }
  if (status.diagnostic?.kind === 'preview_only_artifact') {
    return 'Preview artifact ready';
  }
  const label = status.message || status.label || status.phase || 'Running';
  return typeof status.progressPercent === 'number'
    ? `${label} ${status.progressPercent}%`
    : label;
}

function getRunStreamStatusTitle(status: RunStreamStatus | null): string {
  if (!status) return 'Run is active';
  const parts = [
    status.phase ? `Phase: ${status.phase}` : '',
    status.label ? `Status: ${status.label}` : '',
    status.message ? `Detail: ${status.message}` : '',
    typeof status.progressPercent === 'number' ? `Progress: ${status.progressPercent}%` : '',
    `Last heartbeat: ${formatRunStatusSince(status.lastHeartbeatAt)}`,
    `Last progress: ${formatRunStatusSince(status.lastMeaningfulEventAt)}`,
    status.lastMeaningfulEventName ? `Last event: ${status.lastMeaningfulEventName}` : '',
    status.diagnostic?.message ? `Diagnostic: ${status.diagnostic.message}` : '',
  ].filter(Boolean);
  return parts.join('\n');
}

function createEmptyComposerState(): ThreadComposerState {
  return {
    pinnedArtifactIds: [],
    pinnedMessageReferences: [],
    pinnedThreadReferences: [],
    pendingImages: [],
    pendingFiles: [],
    uiArtifactsById: {},
  };
}

async function readJsonSafe(res: Response): Promise<any> {
  const text = await res.text().catch(() => '');
  if (!text.trim()) return null;
  try {
    return JSON.parse(text);
  } catch {
    return { error: text };
  }
}

function isRecord(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function firstStringValue(...values: unknown[]): string | undefined {
  for (const value of values) {
    if (typeof value !== 'string') continue;
    const text = value.trim();
    if (text) return text;
  }
  return undefined;
}

function firstNumberValue(...values: unknown[]): number | undefined {
  for (const value of values) {
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value === 'string' && value.trim()) {
      const parsed = Number(value.trim());
      if (Number.isFinite(parsed)) return parsed;
    }
  }
  return undefined;
}

function firstBoolValue(...values: unknown[]): boolean | undefined {
  for (const value of values) {
    if (typeof value === 'boolean') return value;
  }
  return undefined;
}

function normalizeFileCapabilities(value: unknown): EvaluatedFileCapabilities | undefined {
  if (Array.isArray(value)) {
    const items = value.map((item) => String(item || '').trim()).filter(Boolean);
    return items.length > 0 ? items : undefined;
  }
  if (!isRecord(value)) return undefined;
  return value as EvaluatedFileCapabilities;
}

function initialAppView(): AppView {
  try {
    return window.location.pathname === '/files' ? 'files' : 'chat';
  } catch {
    return 'chat';
  }
}

const App: React.FC = () => {
  const [isSettingsOpen, setIsSettingsOpen] = useState(false);
  const [activeView, setActiveView] = useState<AppView>(() => initialAppView());
  // Where the settings dialog should land when opened via a deep link
  // (reconnect toast, inline chat re-auth action). Null -> default tab.
  const [settingsFocus, setSettingsFocus] = useState<{
    tab: 'providers' | 'mcps' | 'permissions' | 'files';
    connectionId: ConnectionCatalogId | null;
    providerConfigId?: string | null;
  } | null>(null);
  const [activeArtifact, setActiveArtifact] = useState<Artifact | null>(null);
  const [activeArtifactProvenance, setActiveArtifactProvenance] = useState<FileArtifactProvenance | null>(null);
  const [isPanelOpen, setIsPanelOpen] = useState(false);
  const [activeProject, setActiveProject] = useState<ProjectSummary | null>(null);
  const [composerByThreadId, setComposerByThreadId] = useState<Record<string, ThreadComposerState>>({});
  const [deleteCandidate, setDeleteCandidate] = useState<{ id: string; title: string } | null>(null);
  const [pinnedThreadIds, setPinnedThreadIds] = useState<string[]>([]);
  const [isSearchOpen, setIsSearchOpen] = useState(false);
  const [isThreadReferencePickerOpen, setIsThreadReferencePickerOpen] = useState(false);
  const [draftInsertionRequest, setDraftInsertionRequest] = useState<{ id: number; text: string } | null>(null);
  const [uiTextZoom, setUiTextZoom] = useState(() => readStoredUiTextZoom());

  const messagesEndRef = useRef<HTMLDivElement>(null);
  const messagesScrollViewportRef = useRef<HTMLDivElement>(null);
  const importThreadInputRef = useRef<HTMLInputElement>(null);
  const filesHeadingRef = useRef<HTMLHeadingElement>(null);
  const draftInsertionCounterRef = useRef(0);

  const { settings, settingsLoaded, saveSettings, applySettingsMutation, reloadSettings, profiles, loadProfiles, saveProfile, deleteProfile } = useSettings();

  const handleSaveSettings = useCallback(async (newSettings: typeof settings) => {
    try {
      await saveSettings(newSettings);
      toast.success('Settings saved');
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save settings');
      throw e;
    }
  }, [saveSettings]);

  // Shared enabled-for-run connection state: the drawer pill and the
  // Connections settings tab both persist per-connection enabled flags through
  // the same serialized settings mutation, so either surface's toggle is
  // immediately canonical for the other and for the next run.
  const handleSetConnectionEnabled = useCallback(async (id: ConnectionCatalogId, enabled: boolean) => {
    try {
      await applySettingsMutation((latest) => withConnectionEnabled(latest, id, enabled));
    } catch (e: any) {
      toast.error(e?.message || 'Failed to update connection.');
    }
  }, [applySettingsMutation]);

  // Deep-link into Settings -> Connections (reconnect toasts, inline chat
  // re-auth banners), optionally landing on one connection's setup window.
  const handleOpenConnections = useCallback((connectionId?: ConnectionCatalogId) => {
    setSettingsFocus({ tab: 'mcps', connectionId: connectionId ?? null });
    setIsSettingsOpen(true);
  }, []);

  // Deep-link into Settings -> Permissions (pending-approvals overflow in the
  // chat strip, "manage in Settings" affordances on approval cards).
  const handleOpenPermissions = useCallback(() => {
    setSettingsFocus({ tab: 'permissions', connectionId: null });
    setIsSettingsOpen(true);
  }, []);

  // Deep-link into the Image Generation connection
  // (auth/target-invalid recovery on the generated-image card).
  const handleOpenImageSettings = useCallback(() => {
    handleOpenConnections('image-generation');
  }, [handleOpenConnections]);

  const navigateAppView = useCallback((view: AppView) => {
    const nextPath = view === 'files' ? '/files' : '/';
    try {
      if (window.location.pathname !== nextPath || window.location.search) {
        window.history.pushState({}, document.title, nextPath);
      }
    } catch {
      // ignore history API errors; still switch the visible view.
    }
    setActiveView(view);
  }, []);

  const handleOpenFiles = useCallback(() => {
    navigateAppView('files');
  }, [navigateAppView]);

  const handleBackToChatView = useCallback(() => {
    navigateAppView('chat');
  }, [navigateAppView]);

  useEffect(() => {
    const syncViewFromLocation = () => {
      setActiveView(initialAppView());
    };
    window.addEventListener('popstate', syncViewFromLocation);
    syncViewFromLocation();
    return () => window.removeEventListener('popstate', syncViewFromLocation);
  }, []);

  useEffect(() => {
    if (activeView !== 'files') return;
    const previousTitle = document.title;
    document.title = 'Files · AI Analyst';
    window.setTimeout(() => {
      filesHeadingRef.current?.focus({ preventScroll: true });
    }, 0);
    return () => {
      document.title = previousTitle;
    };
  }, [activeView]);

  // Verifies stored connection credentials at launch and once they age past
  // the 24h trust window; failures surface a reconnect toast and feed the
  // Connections console via persisted health fields.
  const { reportToolAuthFailure } = useConnectionHealth({
    settings,
    settingsLoaded,
    persistSettings: saveSettings,
    reloadSettings,
    onReconnect: handleOpenConnections,
  });

  const capabilityUx = React.useMemo(() => getCapabilityUxFlags(settings), [settings]);

  // A preflight fix removed an attachment from the pending submission; drop it
  // from the composer too so the visible state matches what will be sent.
  const handlePreflightAttachmentRemoved = useCallback((threadId: string, attachmentId: string) => {
    setComposerByThreadId((prev) => {
      const composer = prev[threadId];
      if (!composer) return prev;
      return {
        ...prev,
        [threadId]: {
          ...composer,
          pendingImages: composer.pendingImages.filter((img) => img.artifactId !== attachmentId),
          pendingFiles: composer.pendingFiles.filter((file) => file.artifactId !== attachmentId),
          pinnedArtifactIds: composer.pinnedArtifactIds.filter((id) => id !== attachmentId),
        },
      };
    });
  }, []);

  const {
    threads,
    activeThreadId,
    activeThreadTitle,
    selectThread,
    createThread,
    deleteThread,
    exportThread,
    importThread,
    isActiveThreadRunning,
    runningThreadIds,
    messages,
    artifactsById,
    ensureArtifactLoaded,
    sendMessage,
    sendSteer,
    stopGeneration,
    clearChat: clearMessages,
    toolProgressByKey,
    isCompacting,
    activeRunStreamStatus,
    preflightGate,
    resolvePreflightGate,
    preflightNotice,
    dismissPreflightNotice,
    resolveThreadTarget,
    switchThreadTarget,
    threadTargetOverrides,
    negotiatingByThreadId,
  } = useChat({
    settings,
    projectId: activeProject?.id ?? null,
    onPreflightAttachmentRemoved: handlePreflightAttachmentRemoved,
  });

  const capabilityProviderConnections = React.useMemo(() => {
    const flags = settings.featureFlags?.providers as Record<string, boolean> | undefined;
    return (settings.providerConfigs || []).filter((cfg) => {
      if (!cfg || typeof cfg.id !== 'string' || !cfg.id.trim()) return false;
      return !flags || flags[cfg.providerId] !== false;
    });
  }, [settings.featureFlags?.providers, settings.providerConfigs]);

  const activeThreadTarget = React.useMemo(
    () => (activeThreadId ? resolveThreadTarget(activeThreadId) : null),
    // threads/overrides feed resolveThreadTarget through refs; keep them as deps.
    [activeThreadId, resolveThreadTarget, threadTargetOverrides, threads],
  );

  // When shared polling sees an async query complete but the placeholder was
  // never replaced (missed SSE envelope, reload mid-query), hydrate the real
  // result artifact so the explorer and running cards resolve.
  useQueryCompletionHydration({ artifactsById, ensureArtifactLoaded });

  const settingsToolTestContext = React.useMemo(() => ({
    projectId: activeProject?.id ?? null,
    projectName: activeProject?.name ?? null,
    threadId: activeThreadId ?? null,
    threadTitle: activeThreadTitle ?? null,
  }), [activeProject?.id, activeProject?.name, activeThreadId, activeThreadTitle]);

  const runningThreadIdSet = React.useMemo(() => new Set(runningThreadIds), [runningThreadIds]);
  const activeRunStatusLabel = React.useMemo(
    () => getRunStreamStatusLabel(activeRunStreamStatus),
    [activeRunStreamStatus],
  );
  const activeRunStatusTitle = React.useMemo(
    () => getRunStreamStatusTitle(activeRunStreamStatus),
    [activeRunStreamStatus],
  );
  const activeRunStatusClass =
    activeRunStreamStatus?.diagnostic?.kind === 'heartbeat_without_progress'
      ? 'text-amber-500'
      : 'text-primary';

  const updateComposerState = useCallback((
    threadId: string | null | undefined,
    updater: (prev: ThreadComposerState) => ThreadComposerState,
  ) => {
    const id = String(threadId || '').trim();
    if (!id) return;
    setComposerByThreadId((prev) => {
      const current = prev[id] || createEmptyComposerState();
      const next = updater(current);
      if (next === current) return prev;
      return { ...prev, [id]: next };
    });
  }, []);

  const activeComposer = React.useMemo<ThreadComposerState>(() => {
    const id = String(activeThreadId || '').trim();
    if (!id) return createEmptyComposerState();
    return composerByThreadId[id] || createEmptyComposerState();
  }, [activeThreadId, composerByThreadId]);

  const allUiArtifactsById = React.useMemo<Record<string, Artifact>>(() => {
    const out: Record<string, Artifact> = {};
    for (const state of Object.values(composerByThreadId)) {
      const uiArtifacts = state?.uiArtifactsById || {};
      for (const [artifactId, artifact] of Object.entries(uiArtifacts)) {
        out[artifactId] = artifact;
      }
    }
    return out;
  }, [composerByThreadId]);

  // Load pinned threads from localStorage (UI-only).
  useEffect(() => {
    try {
      const raw = localStorage.getItem(PINNED_THREADS_STORAGE_KEY);
      if (!raw) return;
      const parsed = JSON.parse(raw);
      if (!Array.isArray(parsed)) return;
      const ids = parsed.map((v) => String(v || '').trim()).filter(Boolean);
      setPinnedThreadIds(ids);
    } catch {
      // ignore
    }
  }, []);

  // Persist pinned thread ids.
  useEffect(() => {
    try {
      localStorage.setItem(PINNED_THREADS_STORAGE_KEY, JSON.stringify(pinnedThreadIds));
    } catch {
      // ignore
    }
  }, [pinnedThreadIds]);

  // Prune pinned ids when threads list is loaded/changes.
  useEffect(() => {
    if (threads.length === 0) return;
    const existing = new Set(threads.map((t) => t.id));
    setPinnedThreadIds((prev) => prev.filter((id) => existing.has(id)));
  }, [threads]);

  // Prune per-thread composer state for deleted/missing threads.
  useEffect(() => {
    if (threads.length === 0) {
      setComposerByThreadId({});
      return;
    }
    const existing = new Set(threads.map((t) => t.id));
    setComposerByThreadId((prev) => {
      let changed = false;
      const next: Record<string, ThreadComposerState> = {};
      for (const [threadId, state] of Object.entries(prev)) {
        if (!existing.has(threadId)) {
          changed = true;
          continue;
        }
        next[threadId] = state;
      }
      return changed ? next : prev;
    });
  }, [threads]);

  useEffect(() => {
    applyUiTextZoom(uiTextZoom);
    persistUiTextZoom(uiTextZoom);
  }, [uiTextZoom]);

  // Cmd+K opens search. Cmd/Ctrl + / - adjust the application text scale.
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      const zoomDirection = getUiTextZoomShortcutDirection(e);
      if (zoomDirection !== 0) {
        e.preventDefault();
        setUiTextZoom((current) => stepUiTextZoom(current, zoomDirection));
        return;
      }
      const isK = e.key.toLowerCase() === 'k';
      if ((e.metaKey || e.ctrlKey) && isK) {
        e.preventDefault();
        setIsSearchOpen(true);
      }
      if (e.key === 'Escape') {
        setIsSearchOpen(false);
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, []);

  const scrollMessagesToBottom = useCallback((behavior: ScrollBehavior = 'smooth') => {
    const viewport = messagesScrollViewportRef.current;
    if (!viewport) return;
    viewport.scrollTo({ top: viewport.scrollHeight, behavior });
  }, []);

  // If this window is a Databricks OAuth redirect to localhost root, complete
  // the exchange server-side. Native/open-external flows do not have an opener.
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const state = (params.get('state') || '').trim();
    const code = (params.get('code') || '').trim();
    const oauthError = (params.get('error') || '').trim();
    const oauthErrorDescription = (params.get('error_description') || '').trim();
    const oauthErrorMessage = oauthErrorDescription || oauthError;
    if (!state || (!code && !oauthErrorMessage)) {
      return;
    }

    const payload: Record<string, string> = {
      type: 'databricks-oauth-code',
      state,
    };
    if (code) payload.code = code;
    if (oauthError) payload.error = oauthError;
    if (oauthErrorDescription) payload.error_description = oauthErrorDescription;

    const hasOpener = (() => {
      try {
        return Boolean(window.opener && !window.opener.closed);
      } catch {
        return false;
      }
    })();

    if (hasOpener) {
      try {
        window.opener.postMessage(payload, '*');
      } catch {
        // ignore cross-window messaging errors
      }
    } else {
      void (async () => {
        try {
          const res = await fetch('/api/databricks/oauth/complete', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
            body: JSON.stringify({
              state,
              ...(code ? { code } : {}),
              ...(oauthError ? { error: oauthError } : {}),
              ...(oauthErrorDescription ? { error_description: oauthErrorDescription } : {}),
            }),
          });
          const data = await res.json().catch(() => null);
          if (!res.ok || data?.ok === false || data?.status === 'error') {
            throw new Error(data?.error || data?.message || 'Databricks authorization failed.');
          }
          toast.success(data?.message || 'Databricks authorization complete.');
        } catch (err: any) {
          toast.error(err?.message || 'Databricks authorization failed.');
        }
      })();
    }

    try {
      const cleanPath = `${window.location.pathname}${window.location.hash}` || '/';
      window.history.replaceState({}, document.title, cleanPath);
    } catch {
      // ignore history API errors
    }

    if (hasOpener) {
      window.setTimeout(() => {
        try {
          window.close();
        } catch {
          // ignore
        }
      }, 75);
    }
  }, []);

  const handleProjectLoaded = useCallback((p: ProjectSummary | null) => {
    setActiveProject(p);
    try {
      if (p?.id) localStorage.setItem('ai_analyst_active_project_id_v1', p.id);
      else localStorage.removeItem('ai_analyst_active_project_id_v1');
    } catch {
      // ignore
    }
  }, []);

  // Restore last active project (best-effort).
  useEffect(() => {
    let cancelled = false;
    const init = async () => {
      const stored = (() => {
        try { return localStorage.getItem('ai_analyst_active_project_id_v1'); } catch { return null; }
      })();
      if (!stored) return;
      const res = await fetch(`/api/projects/${encodeURIComponent(stored)}`, { method: 'GET', headers: { Accept: 'application/json' } });
      if (!res.ok) return;
      const data = await res.json().catch(() => null);
      const proj = (data as any)?.project;
      if (!proj || typeof proj.id !== 'string') return;
      if (cancelled) return;
      setActiveProject({
        id: String(proj.id),
        name: String(proj.name ?? 'Project'),
        fileCount: Number(proj.file_count ?? 0) || 0,
        source: typeof proj.source === 'string' ? proj.source : undefined,
        gitUrl: typeof proj.git_url === 'string' ? proj.git_url : undefined,
        localPath: typeof proj.local_path === 'string' ? proj.local_path : undefined,
        rootRelPath: typeof proj.root_rel_path === 'string' ? proj.root_rel_path : undefined,
      });
    };
    void init();
    return () => { cancelled = true; };
  }, []);

  const handleOpenArtifact = useCallback((artifact: Artifact, provenance?: FileArtifactProvenance) => {
    setActiveArtifact(artifact);
    setActiveArtifactProvenance(provenance || null);
    setIsPanelOpen(true);
    const needsHydration = artifact?.metadata?.placeholder === true || artifact?.metadata?.partial === true || artifact?.metadata?.preview_only === true;
    if (needsHydration) {
      void ensureArtifactLoaded(artifact.id).then((full) => {
        if (!full) return;
        setActiveArtifact((prev) => {
          if (!prev) return prev;
          if (prev.id === full.id) return full;
          const prevFullID = String(prev.metadata?.full_artifact_id || '').trim();
          if (prevFullID && prevFullID === full.id) return full;
          return prev;
        });
      });
    }
  }, [ensureArtifactLoaded]);

  // The artifact panel holds a snapshot. Async queries open as placeholder
  // artifacts while running; when get_query_results replaces the stored
  // artifact under the same id, upgrade the open snapshot so the panel moves
  // from the running inspector to the loaded explorer without a re-click.
  // Pre-submit query placeholders (derived by QueryExecutionCard before any
  // envelope exists, under a synthetic `pending-query:` id) instead match by
  // tool_call_id, hopping to whichever real artifact materialises for that
  // call — placeholder allowed there, since the real-id placeholder can poll
  // and the id-based rule then finishes the upgrade.
  useEffect(() => {
    if (!activeArtifact) return;
    const isStale = (a: Artifact | null | undefined) =>
      !!a &&
      (a.metadata?.placeholder === true ||
        a.metadata?.partial === true ||
        a.metadata?.preview_only === true);
    if (!isStale(activeArtifact)) return;
    // Same precedence as mergedArtifactsById, but skip candidates that are
    // themselves still placeholders so we never swap one stub for another.
    const next = [allUiArtifactsById[activeArtifact.id], artifactsById[activeArtifact.id]].find(
      (a) => a && a !== activeArtifact && !isStale(a),
    );
    if (next) {
      setActiveArtifact(next);
      return;
    }
    if (activeArtifact.metadata?.pre_submit !== true) return;
    const callId = String(activeArtifact.metadata?.tool_call_id || '').trim();
    if (!callId) return;
    const byCallId = [...Object.values(allUiArtifactsById), ...Object.values(artifactsById)].filter(
      (a) =>
        a &&
        a.id !== activeArtifact.id &&
        a.metadata?.pre_submit !== true &&
        String(a.metadata?.tool_call_id || '').trim() === callId,
    );
    const upgraded = byCallId.find((a) => !isStale(a)) || byCallId[0];
    if (upgraded) setActiveArtifact(upgraded);
  }, [activeArtifact, artifactsById, allUiArtifactsById]);

  const registerArtifact = useCallback((threadId: string | null | undefined, artifact: Artifact) => {
    updateComposerState(threadId, (prev) => {
      if (prev.uiArtifactsById[artifact.id]) return prev;
      return {
        ...prev,
        uiArtifactsById: {
          ...prev.uiArtifactsById,
          [artifact.id]: artifact,
        },
      };
    });
  }, [updateComposerState]);

  const handlePinArtifact = useCallback((artifact: Artifact) => {
    const threadId = activeThreadId;
    registerArtifact(threadId, artifact);
    updateComposerState(threadId, (prev) => {
      const exists = prev.pinnedArtifactIds.includes(artifact.id);
      return {
        ...prev,
        pinnedArtifactIds: exists
          ? prev.pinnedArtifactIds.filter((id) => id !== artifact.id)
          : [...prev.pinnedArtifactIds, artifact.id],
      };
    });
  }, [activeThreadId, registerArtifact, updateComposerState]);

  const handleRemovePin = useCallback((artifactId: string) => {
    updateComposerState(activeThreadId, (prev) => ({
      ...prev,
      pinnedArtifactIds: prev.pinnedArtifactIds.filter((id) => id !== artifactId),
    }));
  }, [activeThreadId, updateComposerState]);

  const handleToggleMessageReference = useCallback((reference: MessageBlockReference) => {
    const id = String(reference?.id || '').trim();
    if (!id) return;

    const normalized: MessageBlockReference = {
      ...reference,
      id,
      key: String(reference.key || id).trim() || id,
      title: String(reference.title || reference.label || 'Message block').trim() || 'Message block',
      preview: String(reference.preview || '').trim(),
      contentSnapshot: String(reference.contentSnapshot || reference.preview || '').trim(),
    };

    updateComposerState(activeThreadId, (prev) => {
      const exists = prev.pinnedMessageReferences.some((item) => item.id === id);
      return {
        ...prev,
        pinnedMessageReferences: exists
          ? prev.pinnedMessageReferences.filter((item) => item.id !== id)
          : [...prev.pinnedMessageReferences, normalized],
      };
    });
  }, [activeThreadId, updateComposerState]);

  const handleRemovePinnedMessageReference = useCallback((referenceId: string) => {
    const id = String(referenceId || '').trim();
    if (!id) return;
    updateComposerState(activeThreadId, (prev) => ({
      ...prev,
      pinnedMessageReferences: prev.pinnedMessageReferences.filter((item) => item.id !== id),
    }));
  }, [activeThreadId, updateComposerState]);

  const handleAddPinnedThreadReference = useCallback((reference: ThreadReference) => {
    const id = String(reference?.id || '').trim();
    if (!id) return;

    const normalized: ThreadReference = {
      id,
      title: String(reference.title || 'Untitled thread').trim() || 'Untitled thread',
      summary: typeof reference.summary === 'string' && reference.summary.trim()
        ? reference.summary.trim()
        : undefined,
      updatedAt: typeof reference.updatedAt === 'number' && Number.isFinite(reference.updatedAt)
        ? reference.updatedAt
        : undefined,
      status: typeof reference.status === 'string' && reference.status.trim()
        ? reference.status.trim()
        : undefined,
    };

    updateComposerState(activeThreadId, (prev) => {
      const exists = prev.pinnedThreadReferences.some((item) => item.id === id);
      if (exists) return prev;
      if (prev.pinnedThreadReferences.length >= 5) {
        toast.error('You can reference up to 5 threads per message.');
        return prev;
      }
      return {
        ...prev,
        pinnedThreadReferences: [...prev.pinnedThreadReferences, normalized],
      };
    });
  }, [activeThreadId, updateComposerState]);

  const handleRemovePinnedThreadReference = useCallback((threadId: string) => {
    const id = String(threadId || '').trim();
    if (!id) return;
    updateComposerState(activeThreadId, (prev) => ({
      ...prev,
      pinnedThreadReferences: prev.pinnedThreadReferences.filter((item) => item.id !== id),
    }));
  }, [activeThreadId, updateComposerState]);

  const handleAttachImage = useCallback((image: ImageAttachment) => {
    const threadId = activeThreadId;
    const artifact: Artifact = {
      id: crypto.randomUUID(),
      type: 'image',
      title: image.fileName,
      content: image.dataUri,
      metadata: {
        mimeType: image.mimeType,
        sizeBytes: image.sizeBytes,
        width: image.width,
        height: image.height,
        source: image.source || (image.fileName.startsWith('Pasted') ? 'paste' : 'upload'),
      },
    };
    updateComposerState(threadId, (prev) => ({
      ...prev,
      uiArtifactsById: {
        ...prev.uiArtifactsById,
        [artifact.id]: artifact,
      },
      pendingImages: [...prev.pendingImages, { ...image, artifactId: artifact.id }],
    }));
  }, [activeThreadId, updateComposerState]);

  const handleRemoveImage = useCallback((index: number) => {
    updateComposerState(activeThreadId, (prev) => ({
      ...prev,
      pendingImages: prev.pendingImages.filter((_, i) => i !== index),
    }));
  }, [activeThreadId, updateComposerState]);

  const pendingUploadFilesRef = useRef<Record<string, { file: File; threadId: string; source: 'upload' | 'drop' | 'paste'; blobUrl?: string; xhr?: XMLHttpRequest }>>({});
  const uploadSeqRef = useRef(0);

  const patchPendingFile = useCallback((
    threadId: string,
    localId: string,
    patch: Partial<ChatFileAttachment> | ((prev: ChatFileAttachment) => ChatFileAttachment) | null,
  ) => {
    updateComposerState(threadId, (prev) => ({
      ...prev,
      pendingFiles: patch === null
        ? prev.pendingFiles.filter((item) => item.localId !== localId)
        : prev.pendingFiles.map((item) => {
            if (item.localId !== localId) return item;
            return typeof patch === 'function' ? patch(item) : { ...item, ...patch };
          }),
    }));
  }, [updateComposerState]);

  const releasePendingUpload = useCallback((localId: string | undefined) => {
    const key = String(localId || '').trim();
    if (!key) return;
    const entry = pendingUploadFilesRef.current[key];
    entry?.xhr?.abort();
    if (entry?.blobUrl) URL.revokeObjectURL(entry.blobUrl);
    delete pendingUploadFilesRef.current[key];
  }, []);

  // A file dropped outside the composer must never navigate the tab away from
  // the app; the composer's own drop handler still receives its events first.
  useEffect(() => {
    const preventFileDrop = (e: DragEvent) => {
      if (e.dataTransfer?.types?.includes('Files')) e.preventDefault();
    };
    window.addEventListener('dragover', preventFileDrop);
    window.addEventListener('drop', preventFileDrop);
    return () => {
      window.removeEventListener('dragover', preventFileDrop);
      window.removeEventListener('drop', preventFileDrop);
    };
  }, []);

  const performAttachmentUpload = useCallback(async (threadId: string, localId: string, file: File) => {
    let status = 0;
    let data: any = null;
    try {
      ({ status, data } = await new Promise<{ status: number; data: any }>((resolve, reject) => {
        const xhr = new XMLHttpRequest();
        const refEntry = pendingUploadFilesRef.current[localId];
        if (refEntry) refEntry.xhr = xhr;
        const qs = new URLSearchParams({ name: file.name || 'attachment' });
        xhr.open('POST', `/api/threads/${encodeURIComponent(threadId)}/attachments?${qs.toString()}`);
        xhr.setRequestHeader('Accept', 'application/json');
        xhr.setRequestHeader('Content-Type', 'application/octet-stream');
        const desktopToken = getDesktopToken();
        if (desktopToken) {
          xhr.setRequestHeader('X-AI-Desktop-Token', desktopToken);
        }
        xhr.responseType = 'json';
        xhr.upload.onprogress = (e) => {
          if (!e.lengthComputable || e.total <= 0) return;
          patchPendingFile(threadId, localId, { progress: Math.min(1, e.loaded / e.total) });
        };
        xhr.onload = () => resolve({ status: xhr.status, data: xhr.response });
        xhr.onerror = () => reject(new Error('Network error during upload.'));
        xhr.onabort = () => reject(new Error('Upload cancelled.'));
        xhr.send(file);
      }));
    } catch (error: any) {
      patchPendingFile(threadId, localId, {
        status: 'error',
        progress: undefined,
        errorMessage: error?.message || 'Failed to upload file.',
      });
      return;
    }

    if (status < 200 || status >= 300) {
      const message = typeof data?.error === 'string' ? data.error : `Upload failed (HTTP ${status})`;
      const hint = typeof data?.hint === 'string' ? data.hint : '';
      patchPendingFile(threadId, localId, {
        status: 'error',
        progress: undefined,
        errorMessage: hint ? `${message} — ${hint}` : message,
      });
      return;
    }

    const rawArtifact = data?.artifact;
    const rawAttachment = data?.attachment;
    if (!rawArtifact || typeof rawArtifact !== 'object' || typeof rawArtifact.id !== 'string' ||
        !rawAttachment || typeof rawAttachment !== 'object' || typeof rawAttachment.artifactId !== 'string') {
      patchPendingFile(threadId, localId, {
        status: 'error',
        progress: undefined,
        errorMessage: 'Backend did not return uploaded attachment metadata.',
      });
      return;
    }

    const artifact = rawArtifact as Artifact;
    const artifactMetadata = isRecord(artifact.metadata) ? artifact.metadata : {};
    const artifactPresentation = fileArtifactCardPresentation(artifact);
    const attachmentPreviewText = firstStringValue(rawAttachment.previewText, rawAttachment.preview_text);
    const attachmentContentNote = firstStringValue(rawAttachment.contentNote, rawAttachment.content_note);
    const previewText = attachmentPreviewText || artifactPresentation.previewText;
    const contentNote = attachmentContentNote || artifactPresentation.contentNote;
    const detectedMimeType = firstStringValue(
      rawAttachment.detectedMimeType,
      rawAttachment.detected_mime_type,
      artifactMetadata.detectedMimeType,
      artifactMetadata.detected_mime_type,
    );
    const normalizedMimeType = firstStringValue(
      rawAttachment.normalizedMimeType,
      rawAttachment.normalized_mime_type,
      artifactMetadata.normalizedMimeType,
      artifactMetadata.normalized_mime_type,
      rawAttachment.mimeType,
      rawAttachment.mime_type,
      artifactMetadata.mimeType,
      artifactMetadata.mime_type,
    );
    const capabilities = normalizeFileCapabilities(rawAttachment.capabilities ?? artifactMetadata.capabilities);
    const attachmentSource = firstStringValue(rawAttachment.source, artifactMetadata.source) as ChatFileAttachment['source'] | undefined;
    const source = pendingUploadFilesRef.current[localId]?.source || 'upload';
    const artifactId = String(rawAttachment.artifactId || '').trim();
    const isImageUpload = String(file.type || '').toLowerCase().startsWith('image/') ||
      String(detectedMimeType || normalizedMimeType || '').toLowerCase().startsWith('image/');
    const renderedPreviewUrl = fileArtifactRenderedPreviewUrl(artifact);
    const attachment: ChatFileAttachment = {
      artifactId,
      status: 'uploaded',
      localId,
      thumbnailArtifactId: isImageUpload && artifactId ? artifactId : undefined,
      thumbnailFetchUrl: isImageUpload ? undefined : renderedPreviewUrl,
      pdfPreviewUrl: artifactPresentation.pdfPreviewUrl,
      htmlPreviewUrl: artifactPresentation.htmlPreviewUrl,
      htmlPreviewContent: artifactPresentation.htmlPreviewContent,
      textPreviewFetchUrl: artifactPresentation.textPreviewFetchUrl,
      fileName: String(rawAttachment.fileName || rawAttachment.file_name || artifact.title || file.name || 'Attachment').trim(),
      mimeType: firstStringValue(rawAttachment.mimeType, rawAttachment.mime_type, artifactMetadata.mimeType, artifactMetadata.mime_type),
      detectedMimeType,
      fileKind: firstStringValue(rawAttachment.fileKind, rawAttachment.file_kind, artifactMetadata.fileKind, artifactMetadata.file_kind),
      normalizedMimeType,
      declaredMimeType: firstStringValue(rawAttachment.declaredMimeType, rawAttachment.declared_mime_type, artifactMetadata.declaredMimeType, artifactMetadata.declared_mime_type),
      extensionMimeType: firstStringValue(rawAttachment.extensionMimeType, rawAttachment.extension_mime_type, artifactMetadata.extensionMimeType, artifactMetadata.extension_mime_type),
      sniffedMimeType: firstStringValue(
        rawAttachment.sniffedMimeType,
        rawAttachment.sniffed_mime_type,
        artifactMetadata.sniffedMimeType,
        artifactMetadata.sniffed_mime_type,
        detectedMimeType,
      ),
      sha256: firstStringValue(rawAttachment.sha256, artifactMetadata.sha256),
      capabilities,
      sizeBytes: firstNumberValue(rawAttachment.sizeBytes, rawAttachment.size_bytes, artifactMetadata.sizeBytes, artifactMetadata.size_bytes) ?? artifactPresentation.sizeBytes,
      isText: firstBoolValue(rawAttachment.isText, rawAttachment.is_text, artifactMetadata.isText, artifactMetadata.is_text),
      previewAvailable: firstBoolValue(rawAttachment.previewAvailable, rawAttachment.preview_available, artifactMetadata.previewAvailable, artifactMetadata.preview_available) ?? artifactPresentation.previewAvailable,
      previewTruncated: firstBoolValue(rawAttachment.previewTruncated, rawAttachment.preview_truncated, artifactMetadata.previewTruncated, artifactMetadata.preview_truncated) ?? artifactPresentation.previewTruncated,
      previewText,
      contentNote,
      language: typeof artifact.language === 'string' ? artifact.language : undefined,
      source: attachmentSource || source,
    };

    registerArtifact(threadId, artifact);
    // Hydrate the chat-level artifact map too, so the transcript card is
    // interactive immediately after send (composer state is reset on send).
    void ensureArtifactLoaded(artifactId);
    updateComposerState(threadId, (prev) => {
      const duplicate = prev.pendingFiles.some(
        (item) => item.localId !== localId && item.artifactId === attachment.artifactId,
      );
      return {
        ...prev,
        pendingFiles: duplicate
          ? prev.pendingFiles.filter((item) => item.localId !== localId)
          : prev.pendingFiles.map((item) => (item.localId === localId ? attachment : item)),
      };
    });
    releasePendingUpload(localId);
  }, [ensureArtifactLoaded, patchPendingFile, registerArtifact, releasePendingUpload, updateComposerState]);

  const handleAttachFile = useCallback(async (file: File, source: 'upload' | 'drop' | 'paste' = 'upload') => {
    let threadId = String(activeThreadId || '').trim();
    if (!threadId) {
      const created = await createThread();
      threadId = String(created?.id || '').trim();
    }
    if (!threadId) {
      toast.error('No thread is available for file upload.');
      return;
    }

    uploadSeqRef.current += 1;
    const localId = `upload-${Date.now().toString(36)}-${uploadSeqRef.current.toString(36)}`;
    const isImage = String(file.type || '').toLowerCase().startsWith('image/');
    const blobUrl = isImage ? URL.createObjectURL(file) : undefined;
    pendingUploadFilesRef.current[localId] = { file, threadId, source, blobUrl };
    const placeholder: ChatFileAttachment = {
      artifactId: '',
      status: 'uploading',
      localId,
      progress: 0,
      fileName: String(file.name || 'Attachment').trim() || 'Attachment',
      mimeType: file.type || undefined,
      sizeBytes: Number.isFinite(file.size) ? file.size : undefined,
      thumbnailUrl: blobUrl,
      source,
    };
    updateComposerState(threadId, (prev) => ({
      ...prev,
      pendingFiles: [...prev.pendingFiles, placeholder],
    }));

    await performAttachmentUpload(threadId, localId, file);
  }, [activeThreadId, createThread, performAttachmentUpload, updateComposerState]);

  const handleRetryFile = useCallback((localId: string) => {
    const pending = pendingUploadFilesRef.current[String(localId || '').trim()];
    if (!pending) {
      toast.error('The original file is no longer available. Remove the card and attach it again.');
      return;
    }
    patchPendingFile(pending.threadId, localId, {
      status: 'uploading',
      progress: 0,
      errorMessage: undefined,
    });
    void performAttachmentUpload(pending.threadId, localId, pending.file);
  }, [patchPendingFile, performAttachmentUpload]);

  const handleRemoveFile = useCallback((index: number) => {
    updateComposerState(activeThreadId, (prev) => {
      const target = prev.pendingFiles[index];
      if (target?.localId) releasePendingUpload(target.localId);
      return {
        ...prev,
        pendingFiles: prev.pendingFiles.filter((_, i) => i !== index),
      };
    });
  }, [activeThreadId, releasePendingUpload, updateComposerState]);

  // Appends pre-framed text (e.g. arXiv paper excerpts) to the composer draft.
  const handleInsertDraftText = useCallback((text: string) => {
    const snippet = String(text || '').trim();
    if (!snippet) return;
    draftInsertionCounterRef.current += 1;
    setDraftInsertionRequest({ id: draftInsertionCounterRef.current, text: snippet });
  }, []);

  // Sources dialog deep link: the whole-computer access mode is managed by
  // the Local Computer connection in Settings, not by Sources rows.
  const handleManageLocalComputer = useCallback(() => {
    handleOpenConnections('local-computer');
  }, [handleOpenConnections]);

  // Sources fallbacks (e.g. a repo that can't be browsed in this build) hand
  // the task to the assistant: drop a draft in the composer, return to chat.
  const handleAskAgentPrompt = useCallback((prompt: string) => {
    handleInsertDraftText(prompt);
    handleBackToChatView();
    toast.success('Added a draft to the chat', {
      description: 'Finish the message and send it when ready.',
    });
  }, [handleBackToChatView, handleInsertDraftText]);

  // A remote-only file reference has no cached bytes to open; drop a
  // download request into the composer and return to the chat so the user
  // can review and send it.
  const handleRequestRemoteFileDownload = useCallback((record: FileAuditRecord) => {
    handleInsertDraftText(buildRemoteFileDownloadPrompt(record));
    // Remember the draft (session-scoped) so the files surface can show
    // "you already asked" when the user returns to this record's row.
    markRemoteFileDownloadRequested(record, {
      threadId: activeThreadId,
      threadTitle: activeThreadTitle,
    });
    handleBackToChatView();
    // The Files surface just disappeared and the draft sits silently in the
    // composer — confirm what happened and that nothing was sent yet.
    toast.success(`Added a download request for "${record.title}" to the chat`, {
      description: 'Review the message and send it to download the file.',
    });
  }, [activeThreadId, activeThreadTitle, handleBackToChatView, handleInsertDraftText]);

  // When a run finishes in a thread that has pending download-request drafts,
  // let a mounted files view offer a refresh (offer only — run completion is
  // a hint that requested files may be cached now, not proof).
  const prevRunningThreadIdSetRef = useRef<Set<string>>(new Set());
  useEffect(() => {
    const prev = prevRunningThreadIdSetRef.current;
    prevRunningThreadIdSetRef.current = runningThreadIdSet;
    for (const threadId of prev) {
      if (!runningThreadIdSet.has(threadId)) notifyRemoteFileDownloadRunCompleted(threadId);
    }
  }, [runningThreadIdSet]);

  // Chat composer actions for nested surfaces (settings sheets, inspector
  // renderers). Same handlers SettingsDialog receives; memoized so consumers
  // do not re-render on unrelated App state changes.
  const chatActions = useMemo<ChatActions>(() => ({
    attachFile: handleAttachFile,
    insertDraftText: handleInsertDraftText,
    openConnections: handleOpenConnections,
    reportConnectionAuthFailure: reportToolAuthFailure,
    attachImage: handleAttachImage,
    openImageSettings: handleOpenImageSettings,
  }), [handleAttachFile, handleInsertDraftText, handleOpenConnections, reportToolAuthFailure, handleAttachImage, handleOpenImageSettings]);

  const mergedArtifactsById = React.useMemo<Record<string, Artifact>>(() => {
    if (Object.keys(allUiArtifactsById).length === 0) return artifactsById;
    return { ...artifactsById, ...allUiArtifactsById };
  }, [allUiArtifactsById, artifactsById]);
  const pinnedArtifactIds = activeComposer.pinnedArtifactIds;
  const pinnedMessageReferences = activeComposer.pinnedMessageReferences;
  const pinnedThreadReferences = activeComposer.pinnedThreadReferences;
  const pendingImages = activeComposer.pendingImages;
  const pendingFiles = activeComposer.pendingFiles;
  const pinnedArtifacts = React.useMemo(
    () => pinnedArtifactIds
      .map(id => mergedArtifactsById[id])
      .filter(Boolean) as Artifact[],
    [mergedArtifactsById, pinnedArtifactIds],
  );
  const pinnedMessageReferenceIds = React.useMemo(
    () => pinnedMessageReferences.map((reference) => reference.id),
    [pinnedMessageReferences],
  );
  const pinnedThreadReferenceIds = React.useMemo(
    () => pinnedThreadReferences.map((reference) => reference.id),
    [pinnedThreadReferences],
  );

  const handleSendWithContext = useCallback(async (text: string) => {
    const targetThreadId = String(activeThreadId || '').trim() || undefined;
    const targetComposer = targetThreadId
      ? (composerByThreadId[targetThreadId] || createEmptyComposerState())
      : createEmptyComposerState();
    const {
      pinnedArtifactIds: targetPinnedArtifactIds,
      pinnedMessageReferences: targetPinnedMessageReferences,
      pinnedThreadReferences: targetPinnedThreadReferences,
      pendingImages: targetPendingImages,
      pendingFiles: targetPendingFiles,
      uiArtifactsById: targetUiArtifactsById,
    } = targetComposer;

    const merged = { ...artifactsById, ...targetUiArtifactsById };
    const pinnedReferenceIds = targetPinnedArtifactIds
      .map((id) => String(id || '').trim())
      .filter(Boolean);
    const pinnedArtifactReferences = pinnedReferenceIds.map((id) => {
      const artifact = merged[id];
      if (artifact) return artifact;
      return {
        id,
        type: 'file',
        title: 'Artifact',
        content: '',
        metadata: { placeholder: true },
      } as Artifact;
    });

    if (targetPendingFiles.some((file) => file.status === 'uploading')) {
      toast.info('Waiting for file uploads to finish before sending.');
      return false;
    }
    const failedFiles = targetPendingFiles.filter((file) => file.status === 'error');
    if (failedFiles.length > 0) {
      toast.warning(
        failedFiles.length === 1
          ? `"${failedFiles[0].fileName}" failed to upload and was not attached.`
          : `${failedFiles.length} files failed to upload and were not attached.`,
      );
    }
    const sendablePendingFiles = targetPendingFiles.filter(
      (file) => file.artifactId && (!file.status || file.status === 'uploaded'),
    );

    // Include pending image artifacts
    const imageArtifactIds = targetPendingImages.map((img) => img.artifactId);
    const imageArtifacts = imageArtifactIds
      .map((id) => merged[id])
      .filter(Boolean) as Artifact[];
    const fileArtifactIds = sendablePendingFiles.map((file) => file.artifactId);
    const fileArtifacts = fileArtifactIds
      .map((id) => merged[id])
      .filter(Boolean) as Artifact[];
    const pendingAttachmentArtifacts = [...imageArtifacts, ...fileArtifacts];

    const pinnedArtifactContext = pinnedArtifactReferences.length > 0
      ? buildArtifactReferenceContext(pinnedArtifactReferences)
      : '';
    const attachmentContext = '';
    const messageContext = targetPinnedMessageReferences.length > 0
      ? buildMessageReferenceContext(targetPinnedMessageReferences)
      : '';
    const hiddenContext = [pinnedArtifactContext, attachmentContext, messageContext].filter(Boolean).join('\n\n');
    const referencedArtifactIds = Array.from(new Set([
      ...pinnedReferenceIds,
      ...pendingAttachmentArtifacts.map((a) => a.id),
    ]));
    const targetReferencedMessageIds = targetPinnedMessageReferences.reduce<string[]>((out, reference) => {
      const id = String(reference?.messageId || '').trim();
      if (!id || out.includes(id)) return out;
      out.push(id);
      return out;
    }, []);

    const accepted = await sendMessage(text, {
      referencedArtifactIds,
      referencedMessageIds: targetReferencedMessageIds,
      referencedThreadIds: targetPinnedThreadReferences.map((reference) => reference.id),
      referencedThreads: targetPinnedThreadReferences.map((reference) => ({ ...reference })),
      hiddenContext: hiddenContext || undefined,
      imageAttachments: targetPendingImages.map((img) => ({ ...img })),
      fileAttachments: sendablePendingFiles.map((file) => ({ ...file })),
      threadId: targetThreadId,
    });
    if (accepted && targetThreadId) {
      targetPendingFiles.forEach((file) => releasePendingUpload(file.localId));
      setComposerByThreadId((prev) => ({
        ...prev,
        [targetThreadId]: createEmptyComposerState(),
      }));
    }
    return accepted;
  }, [
    activeThreadId,
    artifactsById,
    composerByThreadId,
    releasePendingUpload,
    sendMessage,
  ]);

  const handleSteerWithContext = useCallback(async (text: string) => {
    const targetThreadId = String(activeThreadId || '').trim() || undefined;
    const targetComposer = targetThreadId
      ? (composerByThreadId[targetThreadId] || createEmptyComposerState())
      : createEmptyComposerState();
    const {
      pendingImages: targetPendingImages,
      pendingFiles: targetPendingFiles,
      uiArtifactsById: targetUiArtifactsById,
    } = targetComposer;

    if (targetPendingFiles.some((file) => file.status === 'uploading')) {
      toast.info('Waiting for file uploads to finish before sending.');
      return false;
    }
    const sendablePendingFiles = targetPendingFiles.filter(
      (file) => file.artifactId && (!file.status || file.status === 'uploaded'),
    );

    // Resolve pending image artifacts for steer message
    const merged = { ...artifactsById, ...targetUiArtifactsById };
    const imageArtifactIds = targetPendingImages.map((img) => img.artifactId);
    const imageArtifacts = imageArtifactIds
      .map((id) => merged[id])
      .filter(Boolean) as Artifact[];
    const fileArtifactIds = sendablePendingFiles.map((file) => file.artifactId);
    const fileArtifacts = fileArtifactIds
      .map((id) => merged[id])
      .filter(Boolean) as Artifact[];

    const accepted = await sendSteer(text, {
      threadId: targetThreadId,
      referencedArtifactIds: [...imageArtifacts, ...fileArtifacts].map((a) => a.id),
      imageAttachments: targetPendingImages.map((img) => ({ ...img })),
      fileAttachments: sendablePendingFiles.map((file) => ({ ...file })),
    });
    if (accepted && targetThreadId) {
      targetPendingFiles.forEach((file) => releasePendingUpload(file.localId));
      updateComposerState(targetThreadId, (prev) => ({
        ...prev,
        pendingImages: [],
        pendingFiles: [],
      }));
    }
    return accepted;
  }, [activeThreadId, artifactsById, composerByThreadId, releasePendingUpload, sendSteer, updateComposerState]);

  const handleClearChat = useCallback(() => {
    clearMessages();
    setActiveArtifact(null);
    setActiveArtifactProvenance(null);
    setIsPanelOpen(false);
  }, [clearMessages]);
  const handleOpenPanel = useCallback(() => {
    setIsPanelOpen(true);
  }, []);
  const handleClosePanel = useCallback(() => {
    setIsPanelOpen(false);
  }, []);
  const handleOpenSettings = useCallback(() => {
    setSettingsFocus(null);
    setIsSettingsOpen(true);
  }, []);
  const handleCloseSettings = useCallback(() => {
    setIsSettingsOpen(false);
    setSettingsFocus(null);
  }, []);

  const handleCreateThread = useCallback(async () => {
    const t = await createThread();
    if (!t) return;
    setComposerByThreadId((prev) => (
      prev[t.id]
        ? prev
        : { ...prev, [t.id]: createEmptyComposerState() }
    ));
    setActiveArtifact(null);
    setActiveArtifactProvenance(null);
    setIsPanelOpen(false);
  }, [createThread]);
  const handleCreateThreadClick = useCallback(() => {
    void handleCreateThread();
  }, [handleCreateThread]);

  const handleSelectThread = useCallback((threadId: string) => {
    selectThread(threadId);
    setActiveArtifact(null);
    setActiveArtifactProvenance(null);
    setIsPanelOpen(false);
  }, [selectThread]);

  const handleOpenOriginThread = useCallback((threadId: string, toolCallId?: string) => {
    const id = String(threadId || '').trim();
    if (!id) return;
    handleSelectThread(id);
    navigateAppView('chat');
    const callId = String(toolCallId || '').trim();
    if (!callId) return;
    // The transcript group that owns this call claims the request once the
    // thread renders, expanding and flashing the exact tool event. If nothing
    // claims it (compacted or deleted history), tell the user the chat opened
    // but the event itself could not be located.
    requestToolCallReveal(callId, {
      onMiss: () => {
        toast.info('Opened the origin chat, but the exact file event is no longer in the transcript.');
      },
    });
  }, [handleSelectThread, navigateAppView]);

  const handleNavigateToQuery = useCallback((query: TrackedQuery) => {
    if (query.threadId && query.threadId !== activeThreadId) {
      handleSelectThread(query.threadId);
    }

    if (query.status === 'completed' && query.artifactId) {
      const artifact = mergedArtifactsById[query.artifactId];
      if (artifact) {
        handleOpenArtifact(artifact);
      }
    }
  }, [activeThreadId, handleOpenArtifact, handleSelectThread, mergedArtifactsById]);

  const handleCancelQuery = useCallback(async (jobId: string, ownerToken?: string, threadId?: string) => {
    const headers: Record<string, string> = { Accept: 'application/json' };
    const token = String(ownerToken || '').trim();
    if (token) headers['x-query-owner-token'] = token;
    const qs = new URLSearchParams();
    const tid = String(threadId || activeThreadId || '').trim();
    if (!tid) return;
    qs.set('thread_id', tid);
    const suffix = qs.toString();
    await fetch(`/api/queries/${encodeURIComponent(jobId)}/cancel${suffix ? `?${suffix}` : ''}`, {
      method: 'POST',
      headers,
    }).catch(() => {});
  }, [activeThreadId]);

  const handleOpenArtifactById = useCallback((artifactId: string) => {
    const artifact = mergedArtifactsById[artifactId];
    if (artifact) {
      handleOpenArtifact(artifact);
    }
  }, [handleOpenArtifact, mergedArtifactsById]);

  const handleDeleteThread = useCallback((threadId: string) => {
    setComposerByThreadId((prev) => {
      if (!Object.prototype.hasOwnProperty.call(prev, threadId)) return prev;
      const next = { ...prev };
      delete next[threadId];
      return next;
    });
    void deleteThread(threadId);
  }, [deleteThread]);

  const handlePinThread = useCallback((threadId: string) => {
    const id = String(threadId || '').trim();
    if (!id) return;
    setPinnedThreadIds((prev) => (prev.includes(id) ? prev : [id, ...prev]));
  }, []);

  const handleUnpinThread = useCallback((threadId: string) => {
    const id = String(threadId || '').trim();
    if (!id) return;
    setPinnedThreadIds((prev) => prev.filter((x) => x !== id));
  }, []);

  const handleReorderPinned = useCallback((nextPinnedIds: string[]) => {
    const next = Array.isArray(nextPinnedIds)
      ? nextPinnedIds.map((v) => String(v || '').trim()).filter(Boolean)
      : [];
    setPinnedThreadIds(next);
  }, []);

  const handleExportThread = useCallback(async (threadId: string, title: string) => {
    const exported = await exportThread(threadId);
    if (!exported) return;
    toast.success(`Exported ${title || 'thread'} as ${exported.fileName}`);
  }, [exportThread]);

  const handleImportThread = useCallback(async (file: File) => {
    const imported = await importThread(file);
    if (!imported) return;
    setActiveArtifact(null);
    setActiveArtifactProvenance(null);
    setIsPanelOpen(false);
    toast.success(`Imported ${imported.title}`);
  }, [importThread]);

  const handleImportThreadClick = useCallback(() => {
    importThreadInputRef.current?.click();
  }, []);
  const handleRequestDeleteThread = useCallback((id: string, title: string) => {
    setDeleteCandidate({ id, title });
  }, []);
  const handleExportThreadClick = useCallback((id: string, title: string) => {
    void handleExportThread(id, title);
  }, [handleExportThread]);
  const handleSearchClick = useCallback(() => {
    setIsSearchOpen(true);
  }, []);
  const handleOpenThreadReferencePicker = useCallback(() => {
    setIsThreadReferencePickerOpen(true);
  }, []);

  const pinnedThreadIdSet = React.useMemo(() => new Set(pinnedThreadIds), [pinnedThreadIds]);
  const threadById = React.useMemo(() => {
    const m = new Map<string, {
      id: string;
      title: string;
      summary?: string;
      updatedAt?: number;
      status?: string;
      artifactCount?: number;
    }>();
    for (const t of threads) m.set(t.id, t);
    return m;
  }, [threads]);
  const threadReferencesById = React.useMemo<Record<string, ThreadReference>>(() => {
    const out: Record<string, ThreadReference> = {};
    for (const thread of threads) {
      out[thread.id] = {
        id: thread.id,
        title: thread.title,
        summary: typeof thread.summary === 'string' && thread.summary.trim() ? thread.summary.trim() : undefined,
        updatedAt: thread.updatedAt,
        status: typeof thread.status === 'string' && thread.status.trim() ? thread.status.trim() : undefined,
      };
    }
    for (const state of Object.values(composerByThreadId)) {
      for (const reference of state.pinnedThreadReferences || []) {
        const id = String(reference?.id || '').trim();
        if (!id) continue;
        out[id] = {
          ...out[id],
          ...reference,
          id,
          title: String(reference.title || out[id]?.title || 'Untitled thread').trim() || 'Untitled thread',
        };
      }
    }
    return out;
  }, [composerByThreadId, threads]);
  const threadReferenceOptions = React.useMemo<ThreadReference[]>(() => (
    threads.map((thread) => ({
      id: thread.id,
      title: thread.title,
      summary: typeof thread.summary === 'string' && thread.summary.trim() ? thread.summary.trim() : undefined,
      updatedAt: thread.updatedAt,
      status: typeof thread.status === 'string' && thread.status.trim() ? thread.status.trim() : undefined,
    }))
  ), [threads]);
  const pinnedThreads = React.useMemo(() => pinnedThreadIds.map((id) => threadById.get(id)).filter(Boolean), [pinnedThreadIds, threadById]);
  const unpinnedThreads = React.useMemo(() => threads.filter((t) => !pinnedThreadIdSet.has(t.id)), [pinnedThreadIdSet, threads]);
  const orderedThreadsForMobile = React.useMemo(() => [...pinnedThreads, ...unpinnedThreads], [pinnedThreads, unpinnedThreads]);

  const chatPanel = (
    <ToolActivityThreadContext.Provider value={activeThreadId || null}>
    <div className="flex h-full min-h-0 min-w-0 max-w-full flex-col overflow-hidden">
      {/* Mobile thread selector */}
      <div className="md:hidden border-b border-border bg-card/60 backdrop-blur px-4 py-2 flex items-center gap-2">
        <select
          className="flex-1 bg-background/30 border border-border rounded-md px-2 py-1.5 text-sm min-w-0"
          value={activeThreadId || ''}
          onChange={(e) => handleSelectThread(e.target.value)}
        >
          {orderedThreadsForMobile.map((t) => (
            <option key={t.id} value={t.id}>{t.title}</option>
          ))}
        </select>
        <Button
          size="sm"
          variant="secondary"
          onClick={handleCreateThreadClick}
          className="gap-2"
        >
          <Plus size={16} />
          New
        </Button>
        <CollapseAllToggle showLabel={false} />
        {isActiveThreadRunning && (
          <div
            className={`flex shrink-0 items-center gap-2 text-xs ${activeRunStatusClass}`}
            title={activeRunStatusTitle}
          >
            <Loader2 size={18} className="shrink-0 animate-spin" />
            {/* Fixed-width slot: the label text must not shift as run phases
                change the string underneath it. */}
            <span className="hidden w-[150px] truncate text-left tabular-nums sm:inline">
              {activeRunStatusLabel}
            </span>
          </div>
        )}
      </div>

        <div className="flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden">
          {/* Active thread title (desktop) */}
          <div className="hidden md:flex items-center justify-between gap-2 px-4 py-2 border-b border-border bg-card/40">
            <div className="min-w-0">
            <div className="text-sm font-semibold truncate">{activeThreadTitle}</div>

          </div>
          <div className="flex shrink-0 items-center gap-2">
            {isActiveThreadRunning && (
              <div
                className={`flex shrink-0 items-center gap-2 text-xs ${activeRunStatusClass}`}
                title={activeRunStatusTitle}
              >
                <Loader2 size={14} className="shrink-0 animate-spin" />
                {/* Fixed-width slot: phase-to-phase label changes must not
                    jitter the text or push the collapse-all toggle around. */}
                <span className="w-[260px] truncate text-left tabular-nums">
                  {activeRunStatusLabel}
                </span>
              </div>
            )}
            <CollapseAllToggle />
          </div>
        </div>
        <TaskBar
          activeThreadId={activeThreadId}
          artifactsById={mergedArtifactsById}
          onNavigateToQuery={handleNavigateToQuery}
          onCancelQuery={handleCancelQuery}
          onOpenArtifact={handleOpenArtifactById}
        />
        <div className="min-h-0 min-w-0 flex-1 overflow-hidden">
          <MessageList
            messages={messages}
            artifactsById={mergedArtifactsById}
            ensureArtifactLoaded={ensureArtifactLoaded}
            threadReferencesById={threadReferencesById}
            jiraBaseUrl={settings.atlassian?.jiraBaseUrl}
            isLoading={isActiveThreadRunning}
            bottomRef={messagesEndRef}
            scrollViewportRef={messagesScrollViewportRef}
            onOpenArtifact={handleOpenArtifact}
            onPinArtifact={handlePinArtifact}
            pinnedArtifactIds={pinnedArtifactIds}
            onToggleMessageReference={handleToggleMessageReference}
            pinnedMessageReferenceIds={pinnedMessageReferenceIds}
            onToggleThreadReference={handleAddPinnedThreadReference}
            pinnedThreadReferenceIds={pinnedThreadReferenceIds}
            toolProgressByKey={toolProgressByKey}
          />
        </div>
      </div>
      <PendingApprovalsStrip
        threadId={activeThreadId}
        onOpenPermissions={handleOpenPermissions}
        onRevealLatestActivity={() => scrollMessagesToBottom()}
      />
      <div className="w-full bg-gradient-to-t from-background via-background/90 to-transparent pt-6 pb-4 px-4">
        <ChatInput
          onSend={handleSendWithContext}
          onSteer={handleSteerWithContext}
          threadId={activeThreadId}
          draftInsertion={draftInsertionRequest}
          onStop={stopGeneration}
          isLoading={isActiveThreadRunning}
          isCompacting={isCompacting}
          disabled={Boolean(activeThreadId && negotiatingByThreadId[activeThreadId])}
          pinnedArtifacts={pinnedArtifacts}
          onRemovePin={handleRemovePin}
          pinnedMessageReferences={pinnedMessageReferences}
          onRemovePinnedMessageReference={handleRemovePinnedMessageReference}
          pinnedThreadReferences={pinnedThreadReferences}
          onRemovePinnedThreadReference={handleRemovePinnedThreadReference}
          onOpenThreadReferencePicker={handleOpenThreadReferencePicker}
          onAttachFile={handleAttachFile}
          pendingFiles={pendingFiles}
          onRemoveFile={handleRemoveFile}
          onRetryFile={handleRetryFile}
          targetControl={
            capabilityUx.composerTarget && activeThreadId ? (
              <TargetPill
                connections={capabilityProviderConnections}
                target={activeThreadTarget}
                runActive={isActiveThreadRunning}
                negotiating={Boolean(negotiatingByThreadId[activeThreadId])}
                onSwitchTarget={(target) => switchThreadTarget(activeThreadId, target)}
                onManageConnections={handleOpenSettings}
              />
            ) : undefined
          }
          aboveComposer={
            capabilityUx.preflight && preflightNotice && preflightNotice.threadId === activeThreadId ? (
              <PreflightNoticeRow texts={preflightNotice.texts} onDismiss={dismissPreflightNotice} />
            ) : undefined
          }
        />
        </div>
      </div>
    </ToolActivityThreadContext.Provider>
  );

  const filesPage = (
    <div className="flex h-full min-h-0 flex-col bg-background">
      <div className="shrink-0 border-b border-border bg-card/40 px-4 py-3">
        <div className="mx-auto flex max-w-5xl items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="text-muted-foreground hover:text-foreground"
              onClick={handleBackToChatView}
              aria-label="Back to chat"
            >
              <ArrowLeft size={18} />
            </Button>
            <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg border border-primary/20 bg-primary/10 text-primary">
              <FileText size={18} aria-hidden="true" />
            </div>
            <div className="min-w-0">
              <h2 ref={filesHeadingRef} className="truncate text-sm font-semibold outline-none" tabIndex={-1}>
                Files
              </h2>
              <p className="truncate text-xs text-muted-foreground">
                All files the model has seen, with a chat filter for the active thread.
              </p>
            </div>
          </div>
          <Button type="button" variant="outline" size="sm" onClick={handleBackToChatView}>
            Back to chat
          </Button>
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-hidden p-4">
        <div className="mx-auto h-full min-h-0 max-w-5xl">
          <FilesAuditView
            currentThreadId={activeThreadId}
            currentThreadTitle={activeThreadTitle}
            onOpenArtifact={handleOpenArtifact}
            onOpenOriginThread={handleOpenOriginThread}
            onRequestRemoteDownload={handleRequestRemoteFileDownload}
            initialScope="all"
            layout="page"
            title="All files"
            description="Search files the model has inspected, downloaded, or received from connected tools."
          />
        </div>
      </div>
    </div>
  );

  return (
    <TooltipProvider delayDuration={300}>
      <ChatActionsContext.Provider value={chatActions}>
      <div
        className="flex h-[100dvh] max-h-[100dvh] w-full min-w-0 max-w-full flex-col overflow-hidden bg-background font-sans text-foreground selection:bg-primary/30"
        data-app-shell
        data-ui-text-zoom={uiTextZoom}
      >
        <Header
          settings={settings}
          isLoading={isActiveThreadRunning}
          isPanelOpen={isPanelOpen}
          activeArtifact={activeArtifact}
          onSetConnectionEnabled={handleSetConnectionEnabled}
          projectId={activeProject?.id ?? null}
          projectFileCount={activeProject?.fileCount ?? 0}
          projectName={activeProject?.name ?? null}
          onOpenPanel={handleOpenPanel}
          onClosePanel={handleClosePanel}
          onClearChat={handleClearChat}
          onOpenSettings={handleOpenSettings}
          onOpenFiles={handleOpenFiles}
          isFilesView={activeView === 'files'}
          onProjectLoaded={handleProjectLoaded}
          onSaveSettingsQuiet={saveSettings}
          onOpenArtifact={handleOpenArtifact}
          onManageLocalComputer={handleManageLocalComputer}
          onAskAgent={handleAskAgentPrompt}
        />
        <input
          ref={importThreadInputRef}
          type="file"
          accept=".json,.ai-thread.json,application/json,application/vnd.ai-analyst.thread+json"
          className="hidden"
          onChange={(e) => {
            const file = e.target.files?.[0];
            if (file) {
              void handleImportThread(file);
            }
            e.currentTarget.value = '';
          }}
        />

        <div className="relative flex min-h-0 min-w-0 max-w-full flex-1 overflow-hidden">
          {activeView === 'files' ? (
            <div className="min-w-0 flex-1">
              {filesPage}
            </div>
          ) : (
            <>
              <ThreadsSidebar
                pinnedThreads={pinnedThreads}
                pinnedThreadIds={pinnedThreadIds}
                threads={unpinnedThreads}
                activeThreadId={activeThreadId}
                runningThreadIdSet={runningThreadIdSet}
                onCreateThread={handleCreateThreadClick}
                onImportThread={handleImportThreadClick}
                onSelectThread={handleSelectThread}
                onRequestDeleteThread={handleRequestDeleteThread}
                onExportThread={handleExportThreadClick}
                onPinThread={handlePinThread}
                onUnpinThread={handleUnpinThread}
                onReorderPinned={handleReorderPinned}
                onSearchClick={handleSearchClick}
              />

              {/* Active thread */}
              <div className="flex-1 min-w-0">
                {chatPanel}
              </div>
            </>
          )}

          {isPanelOpen && (
            <InspectorOverlay onClose={handleClosePanel}>
              <ArtifactPanel
                artifact={activeArtifact}
                settings={settings}
                onClose={handleClosePanel}
                onPinArtifact={handlePinArtifact}
                isPinned={activeArtifact ? pinnedArtifactIds.includes(activeArtifact.id) : false}
                fileProvenance={activeArtifactProvenance}
                onOpenOriginThread={handleOpenOriginThread}
              />
            </InspectorOverlay>
          )}
        </div>

        <SettingsDialog
          isOpen={isSettingsOpen}
          onClose={handleCloseSettings}
          settings={settings}
          onSave={handleSaveSettings}
          onApplySettingsMutation={applySettingsMutation}
          profiles={profiles}
          onSaveProfile={saveProfile}
          onDeleteProfile={deleteProfile}
          onLoadProfiles={loadProfiles}
          onAttachFile={handleAttachFile}
          onInsertDraftText={handleInsertDraftText}
          initialTab={settingsFocus?.tab}
          focusConnectionId={settingsFocus?.connectionId ?? null}
          focusProviderConfigId={settingsFocus?.providerConfigId ?? null}
          toolTestContext={settingsToolTestContext}
          onOpenArtifact={handleOpenArtifact}
          onOpenFiles={handleOpenFiles}
          onOpenOriginThread={handleOpenOriginThread}
          onRequestRemoteFileDownload={handleRequestRemoteFileDownload}
        />

        <ThreadReferencePickerDialog
          open={isThreadReferencePickerOpen}
          onOpenChange={setIsThreadReferencePickerOpen}
          activeThreadId={activeThreadId}
          threads={threadReferenceOptions}
          selectedThreadIds={pinnedThreadReferenceIds}
          onSelectReference={handleAddPinnedThreadReference}
        />

        <PreflightSheet
          open={Boolean(preflightGate)}
          result={preflightGate?.result ?? null}
          connections={capabilityProviderConnections}
          currentTarget={preflightGate ? resolveThreadTarget(preflightGate.threadId) : null}
          onResolve={resolvePreflightGate}
          onOpenPermissions={() => {
            // No stacked dialogs: close the preflight sheet first (cancel keeps
            // the composer draft and attachments), then open Settings. Land on
            // the target connection's capability controls when that surface is
            // available; otherwise the generic Permissions tab.
            const targetConnectionId = preflightGate
              ? resolveThreadTarget(preflightGate.threadId)?.connectionId ?? null
              : null;
            resolvePreflightGate({ action: 'cancel' });
            if (capabilityUx.report && targetConnectionId) {
              setSettingsFocus({ tab: 'providers', connectionId: null, providerConfigId: targetConnectionId });
            } else {
              setSettingsFocus({ tab: 'permissions', connectionId: null });
            }
            setIsSettingsOpen(true);
          }}
          onSwitchTarget={
            preflightGate
              ? (target) =>
                switchThreadTarget(preflightGate.threadId, target, { viaPreflightGate: true })
              : undefined
          }
        />

        <Dialog open={!!deleteCandidate} onOpenChange={(open) => { if (!open) setDeleteCandidate(null); }}>
          <DialogContent className="max-w-[420px]">
            <DialogHeader>
              <DialogTitle>Delete thread?</DialogTitle>
              <DialogDescription>
                Delete &quot;{deleteCandidate?.title || 'Thread'}&quot;? This cannot be undone.
              </DialogDescription>
            </DialogHeader>
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setDeleteCandidate(null)}
              >
                Cancel
              </Button>
              <Button
                type="button"
                variant="destructive"
                onClick={() => {
                  const id = deleteCandidate?.id;
                  if (id) setPinnedThreadIds((prev) => prev.filter((x) => x !== id));
                  setDeleteCandidate(null);
                  if (id) handleDeleteThread(id);
                }}
              >
                Delete
              </Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>

        <Dialog open={isSearchOpen} onOpenChange={setIsSearchOpen}>
          <DialogContent className="max-w-[640px]">
            <DialogHeader>
              <DialogTitle>Search</DialogTitle>
              <DialogDescription>
                Thread search is coming next. For now, this is just the UI shell.
              </DialogDescription>
            </DialogHeader>
            <div className="mt-2">
              <input
                autoFocus
                placeholder="Search threads…"
                className="w-full rounded-lg border border-border bg-background/30 px-3 py-2 text-sm outline-none focus:ring-2 focus:ring-ring/40"
              />
              <div className="mt-2 text-xs text-muted-foreground">
                Tip: press <span className="font-mono">Esc</span> to close.
              </div>
            </div>
          </DialogContent>
        </Dialog>

        <Toaster theme="dark" position="top-center" richColors />
      </div>
      </ChatActionsContext.Provider>
    </TooltipProvider>
  );
};

export default App;
