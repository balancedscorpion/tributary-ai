import React from 'react';

/**
 * Thread-level "Collapse all tool activity" preference.
 *
 * Stored in localStorage as a map of threadId -> true so the choice survives
 * reloads and thread switches. Components read it through
 * useCollapseAllToolActivity(), which subscribes via useSyncExternalStore so
 * every tool-activity group in the thread updates the moment it is toggled
 * (including from another tab via the storage event).
 */

const STORAGE_KEY = 'ai_analyst_tool_activity_collapse_all_v1';

type CollapseAllStore = Record<string, true>;

function readStore(): CollapseAllStore {
  if (typeof window === 'undefined') return {};
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return {};
    const out: CollapseAllStore = {};
    for (const [key, value] of Object.entries(parsed)) {
      if (key && value === true) out[key] = true;
    }
    return out;
  } catch {
    return {};
  }
}

let store: CollapseAllStore = readStore();
const listeners = new Set<() => void>();

function emit(): void {
  for (const listener of listeners) listener();
}

function persist(): void {
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(store));
  } catch {
    // Persistence is best-effort; in-memory state still drives the session.
  }
}

if (typeof window !== 'undefined') {
  window.addEventListener('storage', (event) => {
    if (event.key !== null && event.key !== STORAGE_KEY) return;
    store = readStore();
    emit();
  });
}

export function subscribeToolActivityPrefs(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function isCollapseAllToolActivity(threadId: string | null | undefined): boolean {
  const id = String(threadId || '').trim();
  return !!id && store[id] === true;
}

export function setCollapseAllToolActivity(
  threadId: string | null | undefined,
  value: boolean,
): void {
  const id = String(threadId || '').trim();
  if (!id) return;
  if (value === (store[id] === true)) return;
  const next: CollapseAllStore = { ...store };
  if (value) next[id] = true;
  else delete next[id];
  store = next;
  persist();
  emit();
}

/** Active thread id for the chat tree; provided once in App so deeply nested
    tool-activity groups can read the thread-scoped preference without prop
    drilling through MessageList/AssistantMessage/IntermediateThinkingBlock. */
export const ToolActivityThreadContext = React.createContext<string | null>(null);

export function useCollapseAllToolActivity(): {
  threadId: string | null;
  collapseAll: boolean;
  setCollapseAll: (next: boolean) => void;
} {
  const threadId = React.useContext(ToolActivityThreadContext);
  const collapseAll = React.useSyncExternalStore(
    subscribeToolActivityPrefs,
    () => isCollapseAllToolActivity(threadId),
    () => false,
  );
  const setCollapseAll = React.useCallback(
    (next: boolean) => setCollapseAllToolActivity(threadId, next),
    [threadId],
  );
  return { threadId, collapseAll, setCollapseAll };
}
