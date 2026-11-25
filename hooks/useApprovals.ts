// Approval request state shared between the chat SSE stream, the pending
// approvals strip, transcript cards, and Settings.
//
// The store is module-level (useSyncExternalStore) so hooks/useChat.ts can
// publish `approval` SSE events with one call and every subscribed surface —
// strip, transcript card, Settings queue — stays in sync, including across
// multiple mounted components.

import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import {
  normalizeApprovalRequest,
  type ApprovalRequestUI,
  type ApprovalScope,
  type PermissionSubject,
} from '@/types/permissions';
import {
  decideApproval as decideApprovalApi,
  isPermissionsUnavailable,
  listApprovals,
} from '@/services/permissionsApi';

// ---------------------------------------------------------------------------
// Module-level store
// ---------------------------------------------------------------------------

type ApprovalsMap = Record<string, ApprovalRequestUI>;

let approvalsById: ApprovalsMap = {};
const listeners = new Set<() => void>();

function emitChange(): void {
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function getSnapshot(): ApprovalsMap {
  return approvalsById;
}

function upsertApprovals(next: ApprovalRequestUI[]): void {
  if (next.length === 0) return;
  const merged: ApprovalsMap = { ...approvalsById };
  let changed = false;
  for (const approval of next) {
    const existing = merged[approval.id];
    // Never let a stale refetch downgrade a decided approval back to pending.
    if (existing && existing.status !== 'pending' && approval.status === 'pending') {
      continue;
    }
    merged[approval.id] = existing ? { ...existing, ...approval } : approval;
    changed = true;
  }
  if (changed) {
    approvalsById = merged;
    emitChange();
  }
}

function shouldSurfacePendingApproval(approval: ApprovalRequestUI): boolean {
  return approval.subject.kind !== 'local_location';
}

/**
 * Entry point for hooks/useChat.ts: publish the payload of an `approval` SSE
 * event (create or state change). Safe to call with anything — invalid
 * payloads are ignored.
 */
export function publishApprovalEvent(
  payload: unknown,
  context?: { threadId?: string; runId?: string; legacyIntegration?: string },
): void {
  const raw =
    payload && typeof payload === 'object' && 'approval' in (payload as any)
      ? (payload as any).approval
      : payload;
  const approval = normalizeApprovalRequest(raw, context);
  if (approval) upsertApprovals([approval]);
}

/**
 * Fetch approvals matching a filter into the shared store (Settings queue,
 * cross-thread views). Returns false when the unified API is not deployed.
 * Non-availability errors propagate so callers can render a retry state.
 */
export async function hydrateApprovals(
  filter: { threadId?: string; status?: string } = {},
): Promise<boolean> {
  try {
    const approvals = await listApprovals(filter);
    upsertApprovals(approvals);
    return true;
  } catch (e) {
    if (isPermissionsUnavailable(e)) return false;
    throw e;
  }
}

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

export interface DecideOptions {
  action: 'approve' | 'reject';
  scope?: ApprovalScope;
  subject?: PermissionSubject;
}

export interface UseApprovalsResult {
  /** All known approvals for the thread, pending first, newest first within groups. */
  approvals: ApprovalRequestUI[];
  /** Pending approvals for the thread (what the strip renders). */
  pending: ApprovalRequestUI[];
  /** Look up any approval by id (transcript cards from envelopes). */
  approvalsById: Record<string, ApprovalRequestUI>;
  /** Decide an approval. Resolves when the backend accepted the decision. */
  decide: (approvalId: string, options: DecideOptions) => Promise<void>;
  /** Approval ids with an in-flight decision request. */
  busyIds: ReadonlySet<string>;
  /** False when the unified approvals API is not deployed in this build. */
  supported: boolean;
  refresh: () => void;
}

export function useApprovals(threadId?: string | null): UseApprovalsResult {
  const byId = useSyncExternalStore(subscribe, getSnapshot, getSnapshot);
  const [supported, setSupported] = useState(true);
  const [busyIds, setBusyIds] = useState<ReadonlySet<string>>(new Set());
  const hydratedThreadRef = useRef<string | null>(null);

  const hydrate = useCallback((tid: string) => {
    let cancelled = false;
    (async () => {
      try {
        const approvals = await listApprovals({ threadId: tid });
        if (cancelled) return;
        setSupported(true);
        upsertApprovals(approvals);
      } catch (e) {
        if (cancelled) return;
        if (isPermissionsUnavailable(e)) setSupported(false);
        // Other failures stay silent here: SSE remains the live source, and
        // hydration is only a reload/backfill nicety.
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    const tid = (threadId || '').trim();
    if (!tid || hydratedThreadRef.current === tid) return;
    hydratedThreadRef.current = tid;
    return hydrate(tid);
  }, [threadId, hydrate]);

  const refresh = useCallback(() => {
    const tid = (threadId || '').trim();
    if (tid) hydrate(tid);
  }, [threadId, hydrate]);

  const decide = useCallback(
    async (approvalId: string, options: DecideOptions) => {
      const current = approvalsById[approvalId];
      const decidedAt = new Date().toISOString();
      const optimistic: ApprovalRequestUI = {
        ...(current || {
          id: approvalId,
          threadId: '',
          runId: '',
          tool: '',
          category: 'write',
          subject: { kind: 'tool' },
          summary: '',
          preview: {},
          options: ['once'],
          status: 'pending',
        }),
        status: options.action === 'approve' ? 'approved' : 'rejected',
        decision: {
          action: options.action,
          scope: options.scope || 'once',
          decided_at: decidedAt,
        },
        subject: options.subject || current?.subject || { kind: 'tool' },
      };
      setBusyIds((prev) => new Set(prev).add(approvalId));
      upsertApprovals([optimistic]);
      try {
        const result = await decideApprovalApi({
          approvalId,
          action: options.action,
          scope: options.scope,
          subject: options.subject,
          legacyIntegration: current?.legacyIntegration,
        });
        const decided: ApprovalRequestUI = result.approval
          ? result.approval
          : optimistic;
        upsertApprovals([decided]);
      } catch (e) {
        if (current) {
          approvalsById = { ...approvalsById, [approvalId]: current };
        } else {
          const next = { ...approvalsById };
          delete next[approvalId];
          approvalsById = next;
        }
        emitChange();
        throw e;
      } finally {
        setBusyIds((prev) => {
          const next = new Set(prev);
          next.delete(approvalId);
          return next;
        });
      }
    },
    [],
  );

  const { approvals, pending } = useMemo(() => {
    const tid = (threadId || '').trim();
    const all = Object.values(byId).filter(
      (a) => !tid || !a.threadId || a.threadId === tid,
    );
    all.sort((a, b) => {
      const aPending = a.status === 'pending' ? 0 : 1;
      const bPending = b.status === 'pending' ? 0 : 1;
      if (aPending !== bPending) return aPending - bPending;
      return (b.createdAt || '').localeCompare(a.createdAt || '');
    });
    return {
      approvals: all,
      pending: all.filter((a) => a.status === 'pending' && shouldSurfacePendingApproval(a)),
    };
  }, [byId, threadId]);

  return { approvals, pending, approvalsById: byId, decide, busyIds, supported, refresh };
}
