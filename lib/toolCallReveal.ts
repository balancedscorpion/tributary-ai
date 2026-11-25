/**
 * Cross-surface "reveal this tool event" handshake.
 *
 * The files audit (and any provenance surface) knows a file's origin
 * tool_call_id but not where that call renders in the chat transcript. The
 * transcript groups (CollapsedToolsSummary) know their own calls but mount
 * asynchronously after a thread switch. This module bridges the two without
 * prop-drilling through MessageList:
 *
 * - The navigator calls requestToolCallReveal(id) right after switching to
 *   the origin thread. The request stays pending until a mounted group claims
 *   it or the timeout fires.
 * - Each tool-activity group checks the pending request on mount and on
 *   notification; the group containing the call claims it (first claim wins),
 *   expands itself, scrolls the card into view, and flashes it.
 * - If nothing claims the request (compacted history, deleted messages, slow
 *   thread load), onMiss fires so the navigator can tell the user the chat
 *   opened but the exact event could not be located.
 */

interface PendingToolCallReveal {
  toolCallId: string;
  timer: ReturnType<typeof setTimeout>;
  onMiss?: () => void;
}

const DEFAULT_REVEAL_TIMEOUT_MS = 8000;

let pending: PendingToolCallReveal | null = null;
const subscribers = new Set<() => void>();

function clearPending(): void {
  if (pending) clearTimeout(pending.timer);
  pending = null;
}

export function requestToolCallReveal(
  toolCallId: string,
  options?: { onMiss?: () => void; timeoutMs?: number },
): void {
  const id = String(toolCallId || '').trim();
  if (!id) return;
  // A newer navigation supersedes the previous request silently.
  clearPending();
  pending = {
    toolCallId: id,
    onMiss: options?.onMiss,
    timer: setTimeout(() => {
      const missed = pending;
      pending = null;
      missed?.onMiss?.();
    }, options?.timeoutMs ?? DEFAULT_REVEAL_TIMEOUT_MS),
  };
  for (const notify of [...subscribers]) notify();
}

/** The pending call id, if any - non-consuming (used to pre-open ancestors). */
export function peekToolCallReveal(): string | null {
  return pending?.toolCallId ?? null;
}

/** Consume the pending request. Returns true only for the first matching claim. */
export function claimToolCallReveal(toolCallId: string): boolean {
  if (!pending || pending.toolCallId !== toolCallId) return false;
  clearPending();
  return true;
}

/** Notifies when a new reveal is requested (for already-mounted groups). */
export function subscribeToolCallReveal(listener: () => void): () => void {
  subscribers.add(listener);
  return () => {
    subscribers.delete(listener);
  };
}

export function cancelToolCallReveal(): void {
  clearPending();
}
