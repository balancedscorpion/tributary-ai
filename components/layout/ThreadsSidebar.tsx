import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { Loader2, Menu, Pin, Plus, Search, Upload } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuTrigger } from '@/components/ui/dropdown-menu';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import { formatRelativeTime } from '@/lib/formatters';
import { cn } from '@/lib/utils';

export type SidebarThread = {
  id: string;
  title: string;
  preview?: string;
  updatedAt?: number;
  artifactCount?: number;
};

type Props = {
  pinnedThreads: SidebarThread[];
  pinnedThreadIds: string[];
  threads: SidebarThread[]; // unpinned threads in display order
  activeThreadId: string | null;
  runningThreadIdSet: Set<string>;

  onCreateThread: () => void;
  onImportThread: () => void;
  onSelectThread: (threadId: string) => void;
  onRequestDeleteThread: (threadId: string, title: string) => void;
  onExportThread: (threadId: string, title: string) => void;
  onPinThread: (threadId: string) => void;
  onUnpinThread: (threadId: string) => void;
  onReorderPinned: (nextPinnedIds: string[]) => void;

  onSearchClick: () => void;
};

const SIDEBAR_COLLAPSED_KEY = 'ai_analyst_threads_sidebar_collapsed_v1';

function safeParseJSON(value: string | null): any | null {
  if (!value) return null;
  try {
    return JSON.parse(value);
  } catch {
    return null;
  }
}

function getMonogram(title: string): string {
  const raw = String(title || '').trim();
  if (!raw) return '…';
  const cleaned = raw.replace(/[^a-zA-Z0-9]+/g, ' ').trim();
  const parts = cleaned.split(/\s+/).filter(Boolean);
  const a = parts[0] || raw;
  const b = parts[1] || '';
  const first = a.slice(0, 1);
  const second = b ? b.slice(0, 1) : a.slice(1, 2);
  return (first + second).toUpperCase();
}

function timeGroupLabel(ms?: number): string {
  if (!ms || !Number.isFinite(ms)) return 'Older';
  const now = new Date();
  const d = new Date(ms);
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const startOfYesterday = new Date(startOfToday);
  startOfYesterday.setDate(startOfYesterday.getDate() - 1);
  const startOfLastWeek = new Date(startOfToday);
  startOfLastWeek.setDate(startOfLastWeek.getDate() - 7);
  if (d >= startOfToday) return 'Today';
  if (d >= startOfYesterday) return 'Yesterday';
  if (d >= startOfLastWeek) return 'Last Week';
  return 'Older';
}

function ThreadsSidebar({
  pinnedThreads,
  pinnedThreadIds,
  threads,
  activeThreadId,
  runningThreadIdSet,
  onCreateThread,
  onImportThread,
  onSelectThread,
  onRequestDeleteThread,
  onExportThread,
  onPinThread,
  onUnpinThread,
  onReorderPinned,
  onSearchClick,
}: Props) {
  const [collapsed, setCollapsed] = useState<boolean>(() => {
    const v = safeParseJSON((() => { try { return localStorage.getItem(SIDEBAR_COLLAPSED_KEY); } catch { return null; } })());
    return v === true;
  });

  useEffect(() => {
    try {
      localStorage.setItem(SIDEBAR_COLLAPSED_KEY, JSON.stringify(collapsed));
    } catch {
      // ignore
    }
  }, [collapsed]);

  const toggleCollapsed = useCallback(() => setCollapsed((v) => !v), []);

  // Drag & drop for pinned ordering.
  const [draggingId, setDraggingId] = useState<string | null>(null);
  const [dragOverId, setDragOverId] = useState<string | null>(null);

  const onPinnedDragStart = useCallback((id: string) => (e: React.DragEvent) => {
    setDraggingId(id);
    setDragOverId(null);
    try {
      e.dataTransfer.effectAllowed = 'move';
      e.dataTransfer.setData('text/plain', id);
    } catch {
      // ignore
    }
  }, []);

  const onPinnedDragOver = useCallback((id: string) => (e: React.DragEvent) => {
    if (!draggingId || draggingId === id) return;
    e.preventDefault();
    setDragOverId(id);
  }, [draggingId]);

  const onPinnedDrop = useCallback((id: string) => (e: React.DragEvent) => {
    if (!draggingId) return;
    e.preventDefault();
    const from = draggingId;
    const to = id;
    if (from === to) return;

    const cur = pinnedThreadIds.slice();
    const fromIdx = cur.indexOf(from);
    const toIdx = cur.indexOf(to);
    if (fromIdx < 0 || toIdx < 0) return;

    cur.splice(fromIdx, 1);
    // Insert at hovered index in the post-removal list.
    // This enables natural movement both upward and downward.
    const insertAt = Math.max(0, Math.min(toIdx, cur.length));
    cur.splice(insertAt, 0, from);
    onReorderPinned(cur);
  }, [draggingId, onReorderPinned, pinnedThreadIds]);

  const onPinnedListDragOver = useCallback((e: React.DragEvent) => {
    if (!draggingId) return;
    e.preventDefault();
    setDragOverId(null);
  }, [draggingId]);

  const onPinnedListDrop = useCallback((e: React.DragEvent) => {
    if (!draggingId) return;
    e.preventDefault();
    const cur = pinnedThreadIds.slice();
    const fromIdx = cur.indexOf(draggingId);
    if (fromIdx < 0) return;
    cur.splice(fromIdx, 1);
    cur.push(draggingId);
    onReorderPinned(cur);
  }, [draggingId, onReorderPinned, pinnedThreadIds]);

  const onPinnedDragEnd = useCallback(() => {
    setDraggingId(null);
    setDragOverId(null);
  }, []);

  const sidebarWidth = collapsed ? 52 : 300;

  const renderThreadRow = useCallback((t: SidebarThread, opts: { pinned: boolean }) => {
    const active = t.id === activeThreadId;
    const runningHere = runningThreadIdSet.has(t.id);
    const updated = formatRelativeTime(t.updatedAt);
    const artifacts = Number.isFinite(t.artifactCount as number) ? Number(t.artifactCount) : 0;
    const group = timeGroupLabel(t.updatedAt);
    const meta = [updated ? `Updated ${updated}` : '', `${artifacts} artifacts`].filter(Boolean).join(' · ');

    const baseClass = cn(
      'group/thread relative w-full flex items-center gap-2 rounded-lg border border-transparent transition-colors',
      'hover:bg-muted/40 hover:border-border/60',
      active ? 'bg-muted/60 border-border/80 text-foreground' : 'text-muted-foreground',
      collapsed ? 'px-1 py-1 justify-center' : 'px-2 py-2',
      collapsed ? '' : 'focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40',
      (!collapsed && opts.pinned) ? 'cursor-grab active:cursor-grabbing' : '',
    );

    const inner = (
      <div
        role="button"
        tabIndex={0}
        onClick={() => onSelectThread(t.id)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            onSelectThread(t.id);
          }
        }}
        className={baseClass}
        title={!collapsed ? t.title : undefined}
        draggable={!collapsed && opts.pinned}
        onDragStart={!collapsed && opts.pinned ? onPinnedDragStart(t.id) : undefined}
        onDragOver={!collapsed && opts.pinned ? onPinnedDragOver(t.id) : undefined}
        onDrop={!collapsed && opts.pinned ? onPinnedDrop(t.id) : undefined}
        onDragEnd={!collapsed && opts.pinned ? onPinnedDragEnd : undefined}
        data-time-group={group}
      >
        {active && (
          <div className="absolute left-0 top-1.5 bottom-1.5 w-[3px] rounded-r bg-primary/90" />
        )}

        {collapsed ? (
          <div className={cn(
            'relative h-9 w-9 rounded-lg border border-border/70 bg-background/20',
            'flex items-center justify-center text-[11px] font-semibold tracking-wide text-foreground',
            active ? 'bg-primary/10 border-primary/30' : '',
          )}>
            {getMonogram(t.title)}
            {runningHere && (
              <Loader2 size={14} className="absolute -right-1 -bottom-1 animate-spin text-primary bg-background rounded-full p-[1px]" />
            )}
          </div>
        ) : (
          <>
            <div className="flex-1 min-w-0">
              <div className={cn("text-[13px] font-medium truncate leading-snug", active ? "text-primary" : "text-foreground")}>
                {t.title || 'Untitled'}
              </div>
              <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground truncate leading-snug mt-0.5">
                {updated && <span>{updated}</span>}
                {artifacts > 0 && <span className="bg-muted/40 rounded px-1.5 py-0.5 text-[10px] font-mono">{artifacts}</span>}
              </div>
            </div>

            {opts.pinned && <Pin size={12} className="text-muted-foreground/50 shrink-0" />}
            <div className="flex items-center gap-1">
              {runningHere && <Loader2 size={14} className="animate-spin text-primary shrink-0" />}
              <DropdownMenu>
                <DropdownMenuTrigger asChild>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 rounded-md opacity-0 group-hover/thread:opacity-100 group-focus-within/thread:opacity-100 focus:opacity-100 text-muted-foreground hover:text-foreground hover:bg-muted/40"
                    onClick={(e) => e.stopPropagation()}
                    aria-label="Thread actions"
                  >
                    <Menu size={16} />
                  </Button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" side="right" sideOffset={6}>
                  {opts.pinned ? (
                    <DropdownMenuItem onSelect={() => onUnpinThread(t.id)}>
                      Unpin
                    </DropdownMenuItem>
                  ) : (
                    <DropdownMenuItem onSelect={() => onPinThread(t.id)}>
                      Pin
                    </DropdownMenuItem>
                  )}
                  <DropdownMenuItem onSelect={() => onExportThread(t.id, t.title || 'Thread')}>
                    Export
                  </DropdownMenuItem>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem
                    className="text-destructive focus:text-destructive"
                    onSelect={() => onRequestDeleteThread(t.id, t.title || 'Thread')}
                  >
                    Delete
                  </DropdownMenuItem>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
          </>
        )}
      </div>
    );

    if (!collapsed) {
      return inner;
    }
    return (
      <Tooltip key={t.id}>
        <TooltipTrigger asChild>
          {inner}
        </TooltipTrigger>
        <TooltipContent side="right" className="max-w-[260px]">
          <div className="font-medium">{t.title || 'Untitled'}</div>
          <div className="text-xs text-muted-foreground mt-0.5">{meta}</div>
        </TooltipContent>
      </Tooltip>
    );
  }, [
    activeThreadId,
    collapsed,
    onPinThread,
    onPinnedDragEnd,
    onPinnedDragOver,
    onPinnedDragStart,
    onPinnedDrop,
    onExportThread,
    onRequestDeleteThread,
    onSelectThread,
    onUnpinThread,
    runningThreadIdSet,
  ]);

  const unpinnedWithGroupLabels = useMemo(() => {
    const out: Array<{ kind: 'label'; label: string } | { kind: 'thread'; thread: SidebarThread }> = [];
    let lastLabel = '';
    for (const t of threads) {
      const lbl = timeGroupLabel(t.updatedAt);
      if (lbl !== lastLabel) {
        out.push({ kind: 'label', label: lbl });
        lastLabel = lbl;
      }
      out.push({ kind: 'thread', thread: t });
    }
    return out;
  }, [threads]);

  const pinnedOverId = dragOverId && pinnedThreadIds.includes(dragOverId) ? dragOverId : null;

  return (
    <aside
      className={cn(
        'hidden md:flex flex-col h-full shrink-0 bg-card/40 backdrop-blur border-r border-border overflow-hidden relative',
        'transition-[width] duration-200 ease-in-out',
      )}
      style={{ width: sidebarWidth }}
    >
      {/* Header */}
      <div className="px-3 py-3 border-b border-border flex items-center gap-2 min-h-[56px]">
        <div
          className={cn(
            'h-6 w-6 rounded-md bg-primary/90 text-primary-foreground flex items-center justify-center shrink-0',
            'cursor-pointer select-none',
          )}
          onClick={toggleCollapsed}
          title={collapsed ? 'Expand threads sidebar' : 'Collapse threads sidebar'}
        >
          <div className="rotate-90 border-l-[5px] border-l-primary-foreground border-y-[3.5px] border-y-transparent" />
        </div>
        <div className={cn(
          'text-sm font-semibold tracking-tight whitespace-nowrap overflow-hidden transition-opacity',
          collapsed ? 'opacity-0 pointer-events-none' : 'opacity-100',
        )}>
          Threads
        </div>
      </div>

      {/* Search */}
      <div className={cn('px-3 py-2', collapsed ? 'px-2' : 'px-3')}>
        <button
          type="button"
          onClick={onSearchClick}
          className={cn(
            'w-full flex items-center gap-2 rounded-lg border border-border/60 bg-background/20 hover:bg-background/30',
            'px-2.5 py-2 transition-colors text-left',
            'focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40',
          )}
        >
          <Search size={14} className="text-muted-foreground shrink-0" />
          {!collapsed && (
            <>
              <input
                readOnly
                tabIndex={-1}
                className="flex-1 min-w-0 bg-transparent outline-none border-0 text-[13px] text-foreground placeholder:text-muted-foreground/70"
                placeholder="Search threads…"
              />
              <span className="text-[10px] font-mono text-muted-foreground/70 border border-border/60 bg-background/30 rounded px-1.5 py-0.5 shrink-0">
                ⌘K
              </span>
            </>
          )}
        </button>
      </div>

      {/* New thread */}
      <div className={cn('px-3 pb-2', collapsed ? 'px-2' : 'px-3')}>
        <button
          type="button"
          onClick={onCreateThread}
          className={cn(
            'w-full flex items-center gap-2 rounded-lg border',
            'border-primary/20 bg-primary/10 text-primary hover:bg-primary/15',
            'px-2.5 py-2 transition-colors',
            'focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40',
            collapsed ? 'justify-center' : '',
          )}
        >
          <Plus size={16} className="shrink-0" />
          {!collapsed && <span className="text-[13px] font-medium">New thread</span>}
        </button>
      </div>

      <div className={cn('px-3 pb-3', collapsed ? 'px-2' : 'px-3')}>
        <button
          type="button"
          onClick={onImportThread}
          className={cn(
            'w-full flex items-center gap-2 rounded-lg border',
            'border-border/60 bg-background/20 text-foreground hover:bg-background/30',
            'px-2.5 py-2 transition-colors',
            'focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40',
            collapsed ? 'justify-center' : '',
          )}
        >
          <Upload size={16} className="shrink-0" />
          {!collapsed && <span className="text-[13px] font-medium">Import thread</span>}
        </button>
      </div>

      {/* Thread list */}
      <div className="flex-1 overflow-y-auto overflow-x-hidden px-2 pb-3">
        {/* Pinned */}
        {pinnedThreads.length > 0 && (
          <div className="pt-2" onDragOver={onPinnedListDragOver} onDrop={onPinnedListDrop}>
            <div className={cn(
              'px-2 pb-1 text-[10px] font-semibold uppercase tracking-widest text-muted-foreground/70',
              collapsed ? 'opacity-0 h-2 pb-0' : 'opacity-100',
            )}>
              Pinned
            </div>
            <div className="space-y-1">
              {pinnedThreads.map((t) => (
                <div
                  key={t.id}
                  className={cn(
                    pinnedOverId === t.id ? 'ring-1 ring-primary/40 rounded-lg' : '',
                    draggingId === t.id ? 'opacity-60' : '',
                  )}
                >
                  {renderThreadRow(t, { pinned: true })}
                </div>
              ))}
            </div>
          </div>
        )}

        {/* Unpinned with time group labels */}
        <div className={cn(pinnedThreads.length > 0 ? 'pt-3' : 'pt-2')}>
          <div className="space-y-1">
            {unpinnedWithGroupLabels.map((item, idx) => {
              if (item.kind === 'label') {
                return (
                  <div
                    key={`lbl-${idx}-${item.label}`}
                    className={cn(
                      'px-2 pt-3 pb-1 text-[10px] font-semibold uppercase tracking-widest text-muted-foreground/70',
                      collapsed ? 'opacity-0 h-2 pt-1 pb-0' : 'opacity-100',
                    )}
                  >
                    {item.label}
                  </div>
                );
              }
              return (
                <div key={item.thread.id}>
                  {renderThreadRow(item.thread, { pinned: false })}
                </div>
              );
            })}

            {threads.length === 0 && pinnedThreads.length === 0 && (
              <div className={cn('px-2 py-2 text-sm text-muted-foreground', collapsed ? 'opacity-0' : 'opacity-100')}>
                No threads yet.
              </div>
            )}
          </div>
        </div>
      </div>
    </aside>
  );
}

export default React.memo(ThreadsSidebar);
