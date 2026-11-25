import React, { useCallback, useEffect, useRef, useState } from 'react';
import { ShieldQuestion } from 'lucide-react';
import { Button } from '@/components/ui/button';
import ApprovalCard from './ApprovalCard';
import { useApprovals } from '@/hooks/useApprovals';
import type { ApprovalRequestUI, ApprovalScope } from '@/types/permissions';
import { approvalPhrase, STATUS_META } from '@/lib/approvalPresentation';

const MAX_VISIBLE = 2;
export const COMPOSER_INPUT_ID = 'chat-composer-input';

interface PendingApprovalsStripProps {
  threadId: string | null;
  /** Open Settings → Permissions (used by the "+N more" overflow). */
  onOpenPermissions: () => void;
  /** Fallback reveal when the originating tool card isn't in the DOM yet. */
  onRevealLatestActivity?: () => void;
}

/**
 * Pending approvals for the active thread, rendered directly above the
 * composer (before it in DOM order, so Shift+Tab from the textarea reaches
 * the approval controls).
 *
 * Focus & announcement contract (accessibility review B1–B4/S2/S3):
 * - Cards appearing/changing via SSE never steal focus; the single polite
 *   live region announces instead.
 * - After the user's own decision, focus moves to the next pending card's
 *   first action; when the strip empties, to the composer textarea.
 * - If SSE removes the card that currently contains focus (decided on
 *   another surface, expired, canceled), the same fallback runs.
 */
const PendingApprovalsStrip: React.FC<PendingApprovalsStripProps> = ({
  threadId,
  onOpenPermissions,
  onRevealLatestActivity,
}) => {
  const { pending, approvalsById, decide, busyIds, refresh } = useApprovals(threadId);
  const [announcement, setAnnouncement] = useState('');
  const containerRef = useRef<HTMLDivElement>(null);
  const prevPendingRef = useRef<ApprovalRequestUI[]>([]);

  const announce = useCallback((message: string) => {
    // Clear-then-set so repeated identical messages still fire.
    setAnnouncement('');
    requestAnimationFrame(() => setAnnouncement(message));
  }, []);

  const focusFallback = useCallback((excludeId?: string) => {
    const container = containerRef.current;
    const nextCard = container?.querySelector<HTMLElement>(
      excludeId
        ? `[data-approval-anchor]:not([data-approval-anchor="${excludeId}"]) button`
        : '[data-approval-anchor] button',
    );
    if (nextCard) {
      nextCard.focus();
      return;
    }
    const composer = document.getElementById(COMPOSER_INPUT_ID);
    if (composer instanceof HTMLElement) composer.focus();
  }, []);

  // Announce arrivals; recover focus when SSE removes the focused card.
  useEffect(() => {
    const prev = prevPendingRef.current;
    prevPendingRef.current = pending;
    const prevIds = new Set(prev.map((a) => a.id));
    const currentIds = new Set(pending.map((a) => a.id));

    const arrived = pending.filter((a) => !prevIds.has(a.id));
    if (arrived.length === 1) {
      announce(`Approval needed: ${approvalPhrase(arrived[0]).title}`);
    } else if (arrived.length > 1) {
      announce(`${pending.length} approvals waiting`);
    }

    // A card the user was focused on vanished without their click
    // (decided elsewhere, expired, canceled): announce + move focus.
    const removed = prev.filter((a) => !currentIds.has(a.id));
    if (removed.length > 0) {
      const active = document.activeElement;
      const container = containerRef.current;
      if (container && active instanceof HTMLElement && container.contains(active)) {
        const stillHasCard = active.closest('[data-approval-anchor]');
        if (!stillHasCard || removed.some((a) => stillHasCard.getAttribute('data-approval-anchor') === a.id)) {
          const latest = removed
            .map((a) => approvalsById[a.id] || a)
            .find(Boolean);
          if (latest && latest.status !== 'pending') {
            announce(
              `${approvalPhrase(latest).title} — ${STATUS_META[latest.status].label.toLowerCase()}`,
            );
          }
          focusFallback();
        }
      }
    }
  }, [pending, approvalsById, announce, focusFallback]);

  const handleDecide = useCallback(
    async (
      approvalId: string,
      decision: { action: 'approve' | 'reject'; scope?: ApprovalScope },
    ) => {
      await decide(approvalId, decision);
      const approval = approvalsById[approvalId];
      const title = approval ? approvalPhrase(approval).title : 'Request';
      announce(
        decision.action === 'approve' ? `Allowed: ${title}` : `Not allowed: ${title}`,
      );
    },
    [decide, approvalsById, announce],
  );

  const handleDecidedByUser = useCallback(
    (approvalId: string) => {
      // The decided card unmounts from the strip on the next render; move
      // focus to a surviving element now.
      requestAnimationFrame(() => focusFallback(approvalId));
    },
    [focusFallback],
  );

  const handleShowInChat = useCallback(
    (approval: ApprovalRequestUI) => {
      const strip = containerRef.current;
      const anchors = Array.from(
        document.querySelectorAll<HTMLElement>(
          `[data-approval-anchor="${approval.id}"]`,
        ),
      ).filter((node) => !strip || !strip.contains(node));
      if (anchors.length > 0) {
        anchors[0].scrollIntoView({ block: 'center', behavior: prefersReducedMotion() ? 'auto' : 'smooth' });
        return;
      }
      onRevealLatestActivity?.();
    },
    [onRevealLatestActivity],
  );

  const visible = pending.slice(0, MAX_VISIBLE);
  const overflow = pending.length - visible.length;

  return (
    <>
      {/* Single polite announcer for the approvals flow — kept mounted even
          when the strip is empty so announcements never unmount mid-read. */}
      <div role="status" aria-live="polite" className="sr-only">
        {announcement}
      </div>
      {visible.length > 0 && (
        <section
          ref={containerRef}
          aria-label="Pending approvals"
          data-approvals-strip
          className="mx-auto w-full max-w-3xl px-4 pb-2 motion-safe:animate-in motion-safe:fade-in motion-safe:slide-in-from-bottom-1 motion-safe:duration-200"
        >
          <div className="flex items-center gap-1.5 pb-1.5 text-[11px] font-medium uppercase text-amber-500">
            <ShieldQuestion size={12} aria-hidden />
            {pending.length === 1
              ? 'Approval needed'
              : `${pending.length} approvals waiting`}
          </div>
          <div className="flex flex-col gap-2">
            {visible.map((approval) => (
              <ApprovalCard
                key={approval.id}
                approval={approval}
                variant="live"
                onDecide={handleDecide}
                busy={busyIds.has(approval.id)}
                onRefresh={refresh}
                onDecidedByUser={handleDecidedByUser}
                onShowInChat={handleShowInChat}
                className="bg-background/80 backdrop-blur-sm shadow-sm"
              />
            ))}
          </div>
          {overflow > 0 && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="mt-1.5 h-7 text-[12px] text-muted-foreground"
              onClick={onOpenPermissions}
            >
              +{overflow} more waiting — review in Settings
            </Button>
          )}
        </section>
      )}
    </>
  );
};

function prefersReducedMotion(): boolean {
  try {
    return window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  } catch {
    return false;
  }
}

export default PendingApprovalsStrip;
