import React, { useCallback, useEffect, useState } from 'react';
import { RefreshCw, ShieldCheck } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import ApprovalCard from './ApprovalCard';
import { hydrateApprovals, useApprovals } from '@/hooks/useApprovals';

type LoadState =
  | { phase: 'loading' }
  | { phase: 'ready' }
  | { phase: 'unsupported' }
  | { phase: 'error'; message: string };

/**
 * Pending approvals across every conversation — the durable queue surfaced in
 * Settings → Permissions. Decisions here behave exactly like the in-chat
 * strip (same store, same API).
 */
const PendingApprovalsQueue: React.FC = () => {
  const { pending, decide, busyIds, refresh } = useApprovals(null);
  const [load, setLoad] = useState<LoadState>({ phase: 'loading' });

  const fetchQueue = useCallback(async () => {
    setLoad({ phase: 'loading' });
    try {
      const supported = await hydrateApprovals({ status: 'pending' });
      setLoad(supported ? { phase: 'ready' } : { phase: 'unsupported' });
    } catch (e: any) {
      setLoad({
        phase: 'error',
        message: `Couldn’t load pending approvals${e?.message ? ` (${e.message})` : ''}.`,
      });
    }
  }, []);

  useEffect(() => {
    void fetchQueue();
  }, [fetchQueue]);

  if (load.phase === 'loading') {
    return (
      <div className="space-y-2" aria-hidden>
        <Skeleton className="h-20 w-full rounded-md" />
        <Skeleton className="h-20 w-full rounded-md" />
      </div>
    );
  }

  if (load.phase === 'unsupported') {
    return (
      <p className="text-[13px] text-muted-foreground">
        The approvals service isn’t available in this build yet. Requests will
        appear here once it is.
      </p>
    );
  }

  if (load.phase === 'error') {
    return (
      <div className="flex flex-wrap items-center gap-2 text-[13px] text-muted-foreground">
        <span>{load.message}</span>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-7 text-[12px]"
          onClick={() => void fetchQueue()}
        >
          <RefreshCw size={12} className="mr-1.5" aria-hidden />
          Try again
        </Button>
      </div>
    );
  }

  if (pending.length === 0) {
    return (
      <div className="flex items-center gap-2.5 rounded-md border border-border bg-muted/30 px-3 py-3">
        <ShieldCheck size={16} className="shrink-0 text-emerald-500" aria-hidden />
        <div className="text-[13px]">
          <p className="font-medium text-foreground">Nothing waiting for you</p>
          <p className="text-muted-foreground">
            When the assistant needs permission, the request appears here and in the chat.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-2">
      {pending.map((approval) => (
        <ApprovalCard
          key={approval.id}
          approval={approval}
          variant="live"
          onDecide={(id, decision) => decide(id, decision)}
          busy={busyIds.has(approval.id)}
          onRefresh={refresh}
        />
      ))}
    </div>
  );
};

export default PendingApprovalsQueue;
