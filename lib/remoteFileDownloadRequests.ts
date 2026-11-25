/**
 * Session-scoped ledger of "ask agent to download" draft requests.
 *
 * Clicking the ask-agent action on a remote-only file only drafts a composer
 * prompt — nothing downloads until the user sends it, and the draft is free
 * text the app must not try to parse back. This module remembers that the
 * draft was created so the files surface can say "you already asked" when the
 * user returns, without ever claiming the request was sent or fulfilled.
 *
 * Entries are keyed by the audit record and also carry the record's remote
 * locators: a fulfilled request produces a NEW cached record under a
 * different id, so "this file is now cached" and "the user deleted the cached
 * copy" can only be tied back to a pending entry by locator. Both events
 * clear the entry — otherwise deleting the cached copy resurfaces the remote
 * reference with a stale "you drafted a request" note for a request that
 * already succeeded.
 *
 * A separate channel reports "a run finished in a thread that has pending
 * requests" so a mounted files view can offer a refresh. It deliberately does
 * not claim the run downloaded anything — run completion is a hint, not
 * proof, which is why consumers surface it as an offer instead of reloading.
 */

export interface RemoteFileDownloadRecordLike {
  id?: string;
  artifactId?: string;
  title?: string;
  cached?: boolean;
  remoteUrl?: string;
  remotePath?: string;
  remoteRef?: string;
}

export interface RemoteFileDownloadRequest {
  recordKey: string;
  title: string;
  threadId: string | null;
  threadTitle: string | null;
  requestedAt: number;
  remoteUrl: string;
  remotePath: string;
  remoteRef: string;
}

const requests = new Map<string, RemoteFileDownloadRequest>();
let version = 0;
const requestSubscribers = new Set<() => void>();
const runCompletedSubscribers = new Set<(threadId: string) => void>();

function trimmed(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function recordKeyOf(record: RemoteFileDownloadRecordLike): string {
  return trimmed(record.artifactId) || trimmed(record.id);
}

function bump(): void {
  version += 1;
  for (const notify of [...requestSubscribers]) notify();
}

export function markRemoteFileDownloadRequested(
  record: RemoteFileDownloadRecordLike,
  thread: { threadId?: string | null; threadTitle?: string | null },
): void {
  const recordKey = recordKeyOf(record);
  if (!recordKey) return;
  // Re-asking overwrites the entry so requestedAt reflects the latest draft.
  requests.set(recordKey, {
    recordKey,
    title: trimmed(record.title),
    threadId: trimmed(thread.threadId) || null,
    threadTitle: trimmed(thread.threadTitle) || null,
    requestedAt: Date.now(),
    remoteUrl: trimmed(record.remoteUrl),
    remotePath: trimmed(record.remotePath),
    remoteRef: trimmed(record.remoteRef),
  });
  bump();
}

export function getRemoteFileDownloadRequest(
  record: RemoteFileDownloadRecordLike,
): RemoteFileDownloadRequest | null {
  const recordKey = recordKeyOf(record);
  return (recordKey && requests.get(recordKey)) || null;
}

/**
 * Clear any entry tied to this record — by key, or by remote locator when the
 * record is a differently-keyed cached copy of the requested file.
 */
export function clearRemoteFileDownloadRequestsFor(record: RemoteFileDownloadRecordLike): void {
  const recordKey = recordKeyOf(record);
  const remoteUrl = trimmed(record.remoteUrl);
  const remotePath = trimmed(record.remotePath);
  const remoteRef = trimmed(record.remoteRef);
  let removed = false;
  for (const [key, entry] of requests) {
    const matches = key === recordKey
      || (remoteUrl !== '' && entry.remoteUrl === remoteUrl)
      || (remotePath !== '' && entry.remotePath === remotePath)
      || (remoteRef !== '' && entry.remoteRef === remoteRef);
    if (!matches) continue;
    requests.delete(key);
    removed = true;
  }
  if (removed) bump();
}

export function threadHasRemoteFileDownloadRequests(threadId: string | null | undefined): boolean {
  const id = trimmed(threadId);
  if (!id) return false;
  for (const entry of requests.values()) {
    if (entry.threadId === id) return true;
  }
  return false;
}

/** Monotonic change counter — useSyncExternalStore snapshot. */
export function remoteFileDownloadRequestsSnapshot(): number {
  return version;
}

export function subscribeRemoteFileDownloadRequests(listener: () => void): () => void {
  requestSubscribers.add(listener);
  return () => {
    requestSubscribers.delete(listener);
  };
}

/**
 * Report a finished run. Only threads with pending entries notify: consumers
 * use this to hint "requested files may be cached now", which is meaningless
 * for runs unrelated to any pending request.
 */
export function notifyRemoteFileDownloadRunCompleted(threadId: string | null | undefined): void {
  const id = trimmed(threadId);
  if (!id || !threadHasRemoteFileDownloadRequests(id)) return;
  for (const notify of [...runCompletedSubscribers]) notify(id);
}

export function subscribeRemoteFileDownloadRunCompleted(
  listener: (threadId: string) => void,
): () => void {
  runCompletedSubscribers.add(listener);
  return () => {
    runCompletedSubscribers.delete(listener);
  };
}

export function resetRemoteFileDownloadRequestsForTests(): void {
  requests.clear();
  version += 1;
  requestSubscribers.clear();
  runCompletedSubscribers.clear();
}
