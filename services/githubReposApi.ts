// API client for app-managed GitHub repository clones.
//
// The backend is moving GitHub clones out of project storage into a dedicated
// repo store with browse endpoints:
//   GET  /api/github/repositories
//   POST /api/github/repositories/clone
//   GET  /api/github/repositories/{id}/tree?ref=&path=
//   GET  /api/github/repositories/{id}/raw?ref=&path=
//
// If those routes are missing, this client reports capability via
// `GithubReposApiUnavailableError` (the services/permissionsApi.ts pattern).
// GitHub repository sources must not silently degrade to project snapshots:
// copied imports are a separate, explicit "Import a copy" flow.

export class GithubReposApiUnavailableError extends Error {
  constructor(message = 'The GitHub repository service is not available in this build.') {
    super(message);
    this.name = 'GithubReposApiUnavailableError';
  }
}

export function isGithubReposUnavailable(error: unknown): boolean {
  return error instanceof GithubReposApiUnavailableError;
}

export interface GitHubRepoUI {
  id: string;
  name: string;
  fullName?: string;
  remoteUrl?: string;
  defaultRef?: string;
  refs?: string[];
  lastFetchedAt?: number;
  byteSize?: number;
  fileCount?: number;
  shallow?: boolean;
}

export type GitHubTreeEntryType = 'dir' | 'file';

export interface GitHubTreeEntryUI {
  name: string;
  path: string;
  type: GitHubTreeEntryType;
  byteSize?: number;
}

function isRecord(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function stringValue(...values: unknown[]): string | undefined {
  for (const value of values) {
    if (typeof value !== 'string') continue;
    const trimmed = value.trim();
    if (trimmed) return trimmed;
  }
  return undefined;
}

function numberValue(...values: unknown[]): number | undefined {
  for (const value of values) {
    if (typeof value === 'number' && Number.isFinite(value)) return value;
  }
  return undefined;
}

function timestampValue(...values: unknown[]): number | undefined {
  for (const value of values) {
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value === 'string' && value.trim()) {
      const ts = Date.parse(value);
      if (Number.isFinite(ts)) return ts;
    }
  }
  return undefined;
}

async function requestJSON(path: string, init?: RequestInit): Promise<Record<string, any>> {
  let res: Response;
  try {
    res = await fetch(path, {
      ...init,
      headers: { Accept: 'application/json', ...(init?.headers || {}) },
    });
  } catch (e: any) {
    throw new Error(e?.message || 'Network error');
  }
  if (res.status === 404 || res.status === 501) {
    throw new GithubReposApiUnavailableError();
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error((data && (data.error || data.message)) || `HTTP ${res.status}`);
  }
  return data && typeof data === 'object' ? data : {};
}

export function normalizeGitHubRepo(raw: unknown): GitHubRepoUI | null {
  if (!isRecord(raw)) return null;
  const id = stringValue(raw.id, raw.source_id, raw.sourceId, raw.repo_id, raw.repoId);
  if (!id) return null;
  const fullName = stringValue(raw.full_name, raw.fullName, raw.repository, raw.repo);
  const name = stringValue(raw.name, raw.title) || fullName || id;
  const refs = Array.isArray(raw.refs)
    ? raw.refs.map((ref: unknown) => stringValue(ref)).filter(Boolean) as string[]
    : undefined;
  return {
    id,
    name,
    fullName,
    remoteUrl: stringValue(raw.remote_url, raw.remoteUrl, raw.url, raw.clone_url, raw.cloneUrl),
    defaultRef: stringValue(raw.default_ref, raw.defaultRef, raw.default_branch, raw.defaultBranch, raw.ref),
    refs: refs && refs.length > 0 ? refs : undefined,
    lastFetchedAt: timestampValue(raw.last_fetched_at, raw.lastFetchedAt, raw.fetched_at, raw.updated_at),
    byteSize: numberValue(raw.byte_size, raw.byteSize, raw.size_bytes, raw.sizeBytes),
    fileCount: numberValue(raw.file_count, raw.fileCount),
    shallow: typeof raw.shallow === 'boolean' ? raw.shallow : undefined,
  };
}

function normalizeTreeEntry(raw: unknown): GitHubTreeEntryUI | null {
  if (!isRecord(raw)) return null;
  const path = stringValue(raw.path, raw.rel_path, raw.relPath, raw.name);
  if (!path) return null;
  const name = stringValue(raw.name) || path.split('/').filter(Boolean).pop() || path;
  const rawType = stringValue(raw.type, raw.kind) || '';
  const isDir = rawType === 'dir' || rawType === 'directory' || rawType === 'tree' || raw.is_dir === true || raw.isDir === true;
  return {
    name,
    path,
    type: isDir ? 'dir' : 'file',
    byteSize: numberValue(raw.byte_size, raw.byteSize, raw.size, raw.size_bytes),
  };
}

const REPO_BASE_PATHS = ['/api/github/repositories', '/api/github/repos'];

// The mount point is stable per build; remember the first one that answers so
// later tree/raw calls do not re-probe on every navigation.
let resolvedBasePath: string | null = null;

async function requestRepoJSON(subPath: string, init?: RequestInit): Promise<Record<string, any>> {
  const bases = resolvedBasePath ? [resolvedBasePath] : REPO_BASE_PATHS;
  let lastUnavailable: GithubReposApiUnavailableError | null = null;
  for (const base of bases) {
    try {
      const data = await requestJSON(`${base}${subPath}`, init);
      resolvedBasePath = base;
      return data;
    } catch (e) {
      if (e instanceof GithubReposApiUnavailableError) {
        lastUnavailable = e;
        continue;
      }
      resolvedBasePath = base;
      throw e;
    }
  }
  resolvedBasePath = null;
  throw lastUnavailable || new GithubReposApiUnavailableError();
}

export async function listGitHubRepositories(): Promise<GitHubRepoUI[]> {
  const data = await requestRepoJSON('');
  const raw = Array.isArray(data.repositories)
    ? data.repositories
    : Array.isArray(data.repos)
      ? data.repos
      : Array.isArray(data)
        ? data
        : [];
  return raw.map(normalizeGitHubRepo).filter((repo): repo is GitHubRepoUI => repo !== null);
}

export interface GitHubTreeResult {
  entries: GitHubTreeEntryUI[];
  ref?: string;
  truncated?: boolean;
}

export async function getGitHubRepoTree(
  repoId: string,
  options: { ref?: string; path?: string; signal?: AbortSignal } = {},
): Promise<GitHubTreeResult> {
  const params = new URLSearchParams();
  if (options.ref) params.set('ref', options.ref);
  if (options.path) params.set('path', options.path);
  const qs = params.toString();
  const data = await requestRepoJSON(
    `/${encodeURIComponent(repoId)}/tree${qs ? `?${qs}` : ''}`,
    { signal: options.signal },
  );
  const tree = isRecord(data.tree) ? data.tree : {};
  const raw = Array.isArray(data.entries)
    ? data.entries
    : Array.isArray(tree.entries)
      ? tree.entries
      : Array.isArray(data.tree)
        ? data.tree
        : [];
  const entries = raw
    .map(normalizeTreeEntry)
    .filter((entry): entry is GitHubTreeEntryUI => entry !== null)
    .sort((a, b) => (a.type === b.type ? a.name.localeCompare(b.name) : a.type === 'dir' ? -1 : 1));
  return {
    entries,
    ref: stringValue(data.ref, tree.ref),
    truncated: data.truncated === true || tree.truncated === true,
  };
}

export function gitHubRepoRawUrl(repoId: string, options: { ref?: string; path: string }): string {
  const base = resolvedBasePath || REPO_BASE_PATHS[0];
  const params = new URLSearchParams({ path: options.path, format: 'raw' });
  if (options.ref) params.set('ref', options.ref);
  return `${base}/${encodeURIComponent(repoId)}/raw?${params.toString()}`;
}

export interface GitHubRepoFile {
  /** Raw bytes as text when the payload is text-like; undefined for binary. */
  text?: string;
  mimeType?: string;
  byteSize?: number;
  /** True when the body was not returned (binary or too large) and only the raw URL is usable. */
  binary: boolean;
}

const TEXT_MIME_HINTS = ['text/', 'application/json', 'application/xml', 'application/javascript', 'application/x-sh', 'application/x-yaml', 'application/toml', 'image/svg'];
const MAX_INLINE_PREVIEW_BYTES = 1_048_576; // 1 MiB guard for inline text previews.

export async function fetchGitHubRepoFile(
  repoId: string,
  options: { ref?: string; path: string; signal?: AbortSignal },
): Promise<GitHubRepoFile> {
  const url = gitHubRepoRawUrl(repoId, options);
  let res: Response;
  try {
    res = await fetch(url, { signal: options.signal });
  } catch (e: any) {
    throw new Error(e?.message || 'Network error');
  }
  if (res.status === 404 || res.status === 501) {
    // Once the mount point has been probed (listing/tree succeeded), a 404
    // here means the file itself is missing from the clone — not that the
    // backend lacks the repo store. Say so instead of degrading the whole UI.
    if (res.status === 404 && resolvedBasePath) {
      throw new Error('This file is not in the local clone. Pull the latest changes and try again.');
    }
    throw new GithubReposApiUnavailableError();
  }
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error((data && (data.error || data.message)) || `HTTP ${res.status}`);
  }
  const mimeType = (res.headers.get('Content-Type') || '').split(';')[0].trim() || undefined;
  const lengthHeader = Number(res.headers.get('Content-Length') || '');
  const byteSize = Number.isFinite(lengthHeader) && lengthHeader > 0 ? lengthHeader : undefined;
  const textLike = !mimeType || TEXT_MIME_HINTS.some((hint) => mimeType.startsWith(hint));
  if (!textLike || (byteSize !== undefined && byteSize > MAX_INLINE_PREVIEW_BYTES)) {
    // Leave the body unread: the caller renders a metadata card with the raw
    // URL (images use it as an <img> src; everything else offers download).
    return { mimeType, byteSize, binary: true };
  }
  const text = await res.text();
  return { text, mimeType, byteSize: byteSize ?? text.length, binary: false };
}

export interface CloneRepositoryEvent {
  type: 'status' | 'progress';
  message?: string;
  detail?: string;
  stage?: string;
  percent?: number;
  current?: number;
  total?: number;
  unit?: string;
}

export interface CloneRepositoryResult {
  /** Dedicated app-managed repository source clone. */
  via: 'repo-store';
  repository?: GitHubRepoUI;
  message?: string;
}

/**
 * Clone through the dedicated repo store. Imported project snapshots are an
 * explicit copy flow and are intentionally not used as a fallback here.
 */
export async function cloneGitHubRepository(
  input: { url: string; name?: string; onEvent?: (event: CloneRepositoryEvent) => void },
): Promise<CloneRepositoryResult> {
  input.onEvent?.({ type: 'status', message: 'Cloning repository as a local source...' });
  const data = await requestRepoJSON('/clone', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ repository: input.url, name: input.name || undefined }),
  });
  input.onEvent?.({ type: 'status', message: 'Repository source is ready.' });
  return {
    via: 'repo-store',
    repository: normalizeGitHubRepo(data.repository ?? data.repo ?? data) || undefined,
    message: stringValue(data.message),
  };
}

/** Refresh (fetch/pull) an app-managed clone. Repo-store only. */
export async function refreshGitHubRepository(repoId: string): Promise<GitHubRepoUI | null> {
  const data = await requestRepoJSON(`/${encodeURIComponent(repoId)}/refresh`, { method: 'POST' });
  return normalizeGitHubRepo(data.repository ?? data.repo ?? data);
}

/** Remove an app-managed clone from the repo store. Repo-store only. */
export async function removeGitHubRepository(repoId: string): Promise<void> {
  await requestRepoJSON(`/${encodeURIComponent(repoId)}`, { method: 'DELETE' });
}

/** Test hook: forget the resolved mount point between mocked scenarios. */
export function resetGitHubReposApiCache(): void {
  resolvedBasePath = null;
}
