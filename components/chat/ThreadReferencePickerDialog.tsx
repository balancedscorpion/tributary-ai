import React from 'react';
import { Link2, Loader2, Search } from 'lucide-react';
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Input } from '@/components/ui/input';
import type { ThreadReference } from '@/types';

interface ThreadReferencePickerDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  activeThreadId?: string | null;
  threads?: ThreadReference[];
  selectedThreadIds?: string[];
  onSelectReference: (reference: ThreadReference) => void;
}

function normalizeThreadReference(raw: any): ThreadReference | null {
  if (!raw || typeof raw !== 'object') return null;
  const id = String((raw as any).thread_id ?? (raw as any).id ?? '').trim();
  if (!id) return null;
  const title = String((raw as any).title ?? '').trim() || 'Untitled thread';
  const summary = typeof (raw as any).summary === 'string' && (raw as any).summary.trim()
    ? (raw as any).summary.trim()
    : undefined;
  const updatedAt = Number((raw as any).updated_at ?? (raw as any).updatedAt ?? NaN);
  const status = typeof (raw as any).status === 'string' && (raw as any).status.trim()
    ? (raw as any).status.trim()
    : undefined;
  return {
    id,
    title,
    summary,
    updatedAt: Number.isFinite(updatedAt) ? updatedAt : undefined,
    status,
  };
}

function formatUpdatedAt(value?: number): string | null {
  if (!value || !Number.isFinite(value)) return null;
  const formatter = new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  });
  return formatter.format(new Date(value));
}

const ThreadReferencePickerDialog: React.FC<ThreadReferencePickerDialogProps> = ({
  open,
  onOpenChange,
  activeThreadId,
  threads = [],
  selectedThreadIds = [],
  onSelectReference,
}) => {
  const [query, setQuery] = React.useState('');
  const [results, setResults] = React.useState<ThreadReference[]>([]);
  const [isLoading, setIsLoading] = React.useState(false);

  const selectedIdSet = React.useMemo(
    () => new Set(selectedThreadIds.map((id) => String(id || '').trim()).filter(Boolean)),
    [selectedThreadIds],
  );
  const localThreadsById = React.useMemo(() => {
    const out = new Map<string, ThreadReference>();
    for (const thread of threads) {
      const id = String(thread?.id || '').trim();
      if (!id) continue;
      out.set(id, {
        id,
        title: String(thread.title || 'Untitled thread').trim() || 'Untitled thread',
        summary: typeof thread.summary === 'string' && thread.summary.trim() ? thread.summary.trim() : undefined,
        updatedAt: typeof thread.updatedAt === 'number' && Number.isFinite(thread.updatedAt) ? thread.updatedAt : undefined,
        status: typeof thread.status === 'string' && thread.status.trim() ? thread.status.trim() : undefined,
      });
    }
    return out;
  }, [threads]);

  const filteredLocalThreads = React.useMemo(() => (
    threads
      .filter((thread) => {
        const id = String(thread?.id || '').trim();
        return id && id !== activeThreadId && !selectedIdSet.has(id);
      })
      .sort((a, b) => (b.updatedAt || 0) - (a.updatedAt || 0))
      .slice(0, 12)
  ), [activeThreadId, selectedIdSet, threads]);

  React.useEffect(() => {
    if (!open) {
      setQuery('');
      setResults([]);
      setIsLoading(false);
    }
  }, [open]);

  React.useEffect(() => {
    if (!open) return;

    const trimmedQuery = query.trim();
    if (!trimmedQuery) {
      setResults(filteredLocalThreads);
      setIsLoading(false);
      return;
    }

    const controller = new AbortController();
    const timeoutId = window.setTimeout(async () => {
      setIsLoading(true);
      try {
        const params = new URLSearchParams({
          q: trimmedQuery,
          limit: '12',
          doc_kind: 'thread',
        });
        const res = await fetch(`/api/search/suggest?${params.toString()}`, {
          method: 'GET',
          headers: { Accept: 'application/json' },
          signal: controller.signal,
        });
        const data = await res.json().catch(() => null);
        const suggestions = Array.isArray((data as any)?.suggestions)
          ? (data as any).suggestions
          : [];
        const next = suggestions
          .map(normalizeThreadReference)
          .filter(Boolean)
          .map((reference) => {
            const id = reference!.id;
            const local = localThreadsById.get(id);
            return {
              ...(local || {}),
              ...reference!,
            };
          })
          .filter((reference) => reference.id !== activeThreadId && !selectedIdSet.has(reference.id));
        setResults(next);
      } catch (error: any) {
        if (error?.name !== 'AbortError') {
          setResults([]);
        }
      } finally {
        if (!controller.signal.aborted) {
          setIsLoading(false);
        }
      }
    }, 180);

    return () => {
      controller.abort();
      window.clearTimeout(timeoutId);
    };
  }, [activeThreadId, filteredLocalThreads, localThreadsById, open, query, selectedIdSet]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[640px]">
        <DialogHeader>
          <DialogTitle>Reference Other Threads</DialogTitle>
          <DialogDescription>
            Add other threads as read-only context. The model will retrieve relevant evidence from them before it starts answering in the current thread.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          <div className="relative">
            <Search size={16} className="absolute left-3 top-1/2 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search thread titles and summaries"
              className="pl-9"
              autoFocus
            />
          </div>

          <div className="rounded-xl border border-border/60 bg-card/50">
            <div className="max-h-[360px] overflow-y-auto p-2">
              {isLoading ? (
                <div className="flex items-center justify-center gap-2 px-4 py-10 text-sm text-muted-foreground">
                  <Loader2 size={16} className="animate-spin" />
                  Searching threads…
                </div>
              ) : results.length === 0 ? (
                <div className="px-4 py-10 text-center text-sm text-muted-foreground">
                  No matching threads found.
                </div>
              ) : (
                <div className="space-y-1.5">
                  {results.map((reference) => {
                    const updatedLabel = formatUpdatedAt(reference.updatedAt);
                    return (
                      <button
                        key={reference.id}
                        type="button"
                        onClick={() => {
                          onSelectReference(reference);
                          setQuery('');
                        }}
                        className="flex w-full items-start gap-3 rounded-lg border border-transparent bg-background/20 px-3 py-3 text-left transition-colors hover:border-border/70 hover:bg-background/40"
                      >
                        <div className="mt-0.5 rounded-md border border-emerald-400/20 bg-emerald-400/10 p-1.5 text-emerald-400">
                          <Link2 size={14} />
                        </div>
                        <div className="min-w-0 flex-1">
                          <div className="truncate text-sm font-medium text-foreground">
                            {reference.title}
                          </div>
                          <div className="mt-1 text-xs text-muted-foreground">
                            {reference.summary || 'No saved summary yet.'}
                          </div>
                          {(updatedLabel || reference.status) && (
                            <div className="mt-1.5 text-[11px] text-muted-foreground/80">
                              {[reference.status, updatedLabel ? `Updated ${updatedLabel}` : ''].filter(Boolean).join(' · ')}
                            </div>
                          )}
                        </div>
                      </button>
                    );
                  })}
                </div>
              )}
            </div>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
};

export default ThreadReferencePickerDialog;
