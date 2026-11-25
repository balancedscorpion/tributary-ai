import React from 'react';

/**
 * Disclosure state machine for a tool-activity group.
 *
 * Contract (product requirements):
 * - While any tool is running (or an integration write-approval is pending)
 *   the container is PINNED open — no collapse affordance.
 * - When the run completes with zero failures, the container lingers open for
 *   1.5s, then auto-collapses to the minimized pill. With the thread-level
 *   "collapse all tool activity" preference on, it collapses immediately, and
 *   the moment the preference is switched on every open group collapses too
 *   (except running/pinned groups and groups awaiting an approval).
 * - Failures never auto-collapse: the container stays open until the user
 *   minimizes it themselves (the pill then carries the red dot + fail count).
 * - Completed turns mount minimized by default; a user's manual toggle is
 *   remembered for the session (module-level map keyed by message identity,
 *   mirroring IntermediateThinkingBlock's step-state pattern).
 */

const AUTO_COLLAPSE_DELAY_MS = 1500;
/** Re-check cadence while auto-collapse is deferred by hover/focus. */
const DEFER_RETRY_MS = 600;

const manualOpenByKey = new Map<string, boolean>();

interface ToolActivityDisclosureOptions {
  /** Stable per-message key so manual open/close survives remounts. */
  stateKey?: string;
  /** True while any tool call in the group lacks a result. */
  isRunning: boolean;
  /** True when any completed tool call in the group errored. */
  hasFailure: boolean;
  /** True while an integration write approval awaits user action. */
  hasPendingApproval: boolean;
  /** Thread-level "collapse all tool activity" preference. */
  collapseAll: boolean;
  /** Return true to postpone an imminent auto-collapse (pointer over the
      container, focus within it). Re-checked every DEFER_RETRY_MS. */
  shouldDeferCollapse?: () => boolean;
}

export interface ToolActivityDisclosure {
  /** Final resolved disclosure for this render. */
  isOpen: boolean;
  /** True while the container must stay open (running / pending approval). */
  isPinned: boolean;
  /** Manual toggle — cancels any pending auto-collapse. No-op while pinned. */
  setOpen: (next: boolean) => void;
}

export function useToolActivityDisclosure({
  stateKey,
  isRunning,
  hasFailure,
  hasPendingApproval,
  collapseAll,
  shouldDeferCollapse,
}: ToolActivityDisclosureOptions): ToolActivityDisclosure {
  const normalizedKey = typeof stateKey === 'string' && stateKey.trim() ? stateKey.trim() : undefined;

  const [manualOpen, setManualOpenState] = React.useState<boolean | undefined>(() =>
    normalizedKey ? manualOpenByKey.get(normalizedKey) : undefined,
  );
  // Holds the container open from live completion until auto-collapse fires
  // (or indefinitely on failure).
  const [liveHold, setLiveHold] = React.useState(false);

  const prevRunningRef = React.useRef(isRunning);
  const timerRef = React.useRef<ReturnType<typeof setTimeout> | null>(null);
  const hasFailureRef = React.useRef(hasFailure);
  hasFailureRef.current = hasFailure;
  const shouldDeferRef = React.useRef(shouldDeferCollapse);
  shouldDeferRef.current = shouldDeferCollapse;

  const clearTimer = React.useCallback(() => {
    if (timerRef.current !== null) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  // Re-sync manual state if this hook instance is reused for a different message.
  React.useEffect(() => {
    setManualOpenState(normalizedKey ? manualOpenByKey.get(normalizedKey) : undefined);
  }, [normalizedKey]);

  React.useEffect(() => {
    const wasRunning = prevRunningRef.current;
    prevRunningRef.current = isRunning;

    if (isRunning) {
      // New (or resumed) activity: drop any pending collapse so the eventual
      // completion gets a fresh linger window.
      clearTimer();
      setLiveHold(true);
      return;
    }

    if (!wasRunning) return;

    // Completion observed live in this mount.
    if (hasFailure) {
      setLiveHold(true);
      return;
    }
    clearTimer();
    const fire = () => {
      timerRef.current = null;
      // A failure envelope can land a beat after the last result flips
      // isRunning off; never collapse a failed group.
      if (hasFailureRef.current) return;
      // Collapsing under the user's pointer or focus is hostile — wait until
      // they leave, re-checking on a short cadence.
      if (shouldDeferRef.current?.()) {
        timerRef.current = setTimeout(fire, DEFER_RETRY_MS);
        return;
      }
      setLiveHold(false);
    };
    // With the thread "collapse all" preference on, skip the linger but keep
    // fire()'s late-failure and hover/focus re-checks.
    timerRef.current = setTimeout(fire, collapseAll ? 0 : AUTO_COLLAPSE_DELAY_MS);
  }, [isRunning, hasFailure, collapseAll, clearTimer]);

  // Turning the thread-level preference ON acts as "collapse everything now":
  // drop this group's manual-open memory and live hold. Pinned (running)
  // groups stay open via isPinned and collapse on completion; pending-approval
  // groups stay open through the hasPendingApproval fallback, because a
  // collapsed pill carries no approval cue and the run is blocked on it.
  const prevCollapseAllRef = React.useRef(collapseAll);
  React.useEffect(() => {
    const was = prevCollapseAllRef.current;
    prevCollapseAllRef.current = collapseAll;
    if (was || !collapseAll) return;
    clearTimer();
    if (normalizedKey) manualOpenByKey.delete(normalizedKey);
    setManualOpenState(undefined);
    setLiveHold(false);
  }, [collapseAll, normalizedKey, clearTimer]);

  React.useEffect(() => clearTimer, [clearTimer]);

  // Only live execution hard-pins the container. A pending approval defaults
  // the group open (it needs user action) but stays manually collapsible so
  // historical turns with approval records don't lock open forever.
  const isPinned = isRunning;

  const setOpen = React.useCallback(
    (next: boolean) => {
      if (isRunning) return;
      clearTimer();
      if (normalizedKey) manualOpenByKey.set(normalizedKey, next);
      setManualOpenState(next);
      if (!next) setLiveHold(false);
    },
    [isRunning, normalizedKey, clearTimer],
  );

  const isOpen =
    isPinned ||
    (manualOpen !== undefined ? manualOpen : liveHold || hasPendingApproval);

  return { isOpen, isPinned, setOpen };
}
