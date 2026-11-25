// Unified source layer for the Sources dialog.
//
// Target backend: `GET /api/sources?scope=all` returning one row per source
// with an explicit kind. Until that exists, `listSources` composes the same
// picture from what the current backend does expose:
//   - Local Computer read grants (permission rules; references, read in place)
//   - App-managed GitHub clones (repo store, when the backend has it)
//   - Project sources (imported copies stored under the active project,
//     including legacy git clones)
//
// Every row carries `semantics` so the UI can state reference-vs-copy
// truthfully, and `capabilities` so actions never appear for rows that can't
// honor them.

import type { PermissionRuleUI } from '@/types/permissions';
import { deletePermissionRule } from '@/services/permissionsApi';
import {
  isWholeComputerRoot,
  listLocalComputerGrantRules,
} from '@/lib/localComputerGrants';
import {
  isGithubReposUnavailable,
  listGitHubRepositories,
  type GitHubRepoUI,
} from '@/services/githubReposApi';

export type SourceKind =
  | 'local_reference'
  | 'github_clone'
  | 'project_snapshot'
  | 'connector_file'
  | 'thread_upload';

export type SourceSemantics = 'reference' | 'copy';

export interface SourceCapabilities {
  browse?: boolean;
  refresh?: boolean;
  stopReference?: boolean;
  deleteCopy?: boolean;
  hide?: boolean;
}

export interface SourceUI {
  /** Stable list key, namespaced by origin (e.g. `grant:<ruleId>`). */
  id: string;
  kind: SourceKind;
  semantics: SourceSemantics;
  title: string;
  /** Local root, project source path, or other origin detail for captions. */
  path?: string;
  remoteUrl?: string;
  ref?: string;
  fileCount?: number;
  byteSize?: number;
  hidden?: boolean;
  refreshedAt?: number;
  wholeComputer?: boolean;
  capabilities: SourceCapabilities;
  /** Present when the row is backed by a permission rule (legacy composition). */
  grantRuleId?: string;
  /** Present when the row is a project source (legacy composition). */
  projectId?: string;
  projectSourceName?: string;
  /** Present when the row is an app-managed repo-store clone. */
  repo?: GitHubRepoUI;
}

export interface ListSourcesOptions {
  /** Active project, so legacy project sources can be included. */
  projectId?: string | null;
  signal?: AbortSignal;
}

export interface ListSourcesResult {
  sources: SourceUI[];
  /** True when the unified `/api/sources` endpoint answered. */
  unified: boolean;
  /**
   * Sections that failed to load during legacy composition, so the UI can
   * report partial data instead of silently omitting a whole group.
   */
  errors: { section: 'local' | 'github' | 'project'; message: string }[];
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

function displayNameFromPath(path: string): string {
  const parts = path.replace(/\\/g, '/').split('/').filter(Boolean);
  return parts.length > 0 ? parts[parts.length - 1] : path;
}

const KNOWN_KINDS: SourceKind[] = [
  'local_reference',
  'github_clone',
  'project_snapshot',
  'connector_file',
  'thread_upload',
];

function normalizeUnifiedSource(raw: unknown): SourceUI | null {
  if (!isRecord(raw)) return null;
  const id = stringValue(raw.id, raw.source_id, raw.sourceId);
  const rawKind = stringValue(raw.kind) || '';
  const kind: SourceKind | undefined = rawKind === 'github_repository'
    ? 'github_clone'
    : KNOWN_KINDS.includes(rawKind as SourceKind)
      ? rawKind as SourceKind
      : undefined;
  if (!id || !kind) return null;
  const path = stringValue(raw.local_root, raw.localRoot, raw.local_path, raw.localPath, raw.path, raw.remote_path, raw.remotePath);
  const remoteUrl = stringValue(raw.remote_url, raw.remoteUrl, raw.source_url, raw.sourceUrl, raw.url);
  const ref = stringValue(raw.ref, raw.default_ref, raw.defaultRef, raw.branch);
  const fileCount = numberValue(raw.file_count, raw.fileCount);
  const byteSize = numberValue(raw.byte_size, raw.byteSize);
  const refreshedAt = timestampValue(raw.refreshed_at, raw.refreshedAt, raw.updated_at, raw.updatedAt);
  const title = stringValue(raw.title, raw.name)
    || (path ? displayNameFromPath(path) : undefined)
    || stringValue(raw.repository, raw.full_name, raw.fullName)
    || id;
  const semantics: SourceSemantics = kind === 'local_reference' || kind === 'github_clone'
    ? 'reference'
    : 'copy';
  const wholeComputer = kind === 'local_reference' && Boolean(path) && isWholeComputerRoot(path!);
  const caps = isRecord(raw.capabilities) ? raw.capabilities : {};
  const repo: GitHubRepoUI | undefined = kind === 'github_clone'
    ? {
      id,
      name: stringValue(raw.repo, raw.name) || title,
      fullName: stringValue(raw.full_name, raw.fullName, raw.repository) || title,
      remoteUrl,
      defaultRef: ref,
      fileCount,
      byteSize,
      lastFetchedAt: refreshedAt,
    }
    : undefined;
  return {
    id: `source:${id}`,
    kind,
    semantics,
    title,
    path,
    remoteUrl,
    ref,
    fileCount,
    byteSize,
    hidden: raw.visibility === 'hidden' || raw.hidden === true,
    refreshedAt,
    wholeComputer,
    grantRuleId: stringValue(raw.grant_id, raw.grantId, raw.grant_rule_id),
    repo,
    capabilities: {
      browse: caps.browse === true || kind === 'github_clone',
      refresh: caps.refresh === true || kind === 'github_clone',
      stopReference: caps.stop_reference === true || caps.stopReference === true
        || (semantics === 'reference' && !wholeComputer),
      deleteCopy: caps.delete_copy === true || caps.deleteCopy === true || semantics === 'copy',
      hide: caps.hide === true,
    },
  };
}

export function sourceFromGrantRule(rule: PermissionRuleUI): SourceUI {
  const root = String(rule.subject.local_root || '').trim();
  const wholeComputer = isWholeComputerRoot(root);
  return {
    id: `grant:${rule.id}`,
    kind: 'local_reference',
    semantics: 'reference',
    title: wholeComputer ? 'Whole computer' : displayNameFromPath(root),
    path: root,
    wholeComputer,
    grantRuleId: rule.id,
    capabilities: {
      // The whole-computer grant is managed by the Settings access-mode
      // switch; revoking it here would silently fight that control.
      stopReference: !wholeComputer,
    },
  };
}

export interface ProjectSourceSummaryUI {
  name: string;
  path: string;
  kind: string;
  hidden: boolean;
  fileCount: number;
  totalBytes: number;
}

export function normalizeProjectSource(raw: unknown): ProjectSourceSummaryUI | null {
  if (!isRecord(raw)) return null;
  const name = stringValue(raw.name, raw.path);
  if (!name) return null;
  return {
    name,
    path: stringValue(raw.path, raw.name) || name,
    kind: stringValue(raw.kind) || 'directory',
    hidden: raw.hidden === true,
    fileCount: numberValue(raw.file_count, raw.fileCount) || 0,
    totalBytes: numberValue(raw.total_bytes, raw.totalBytes) || 0,
  };
}

function sourceFromProjectSource(projectId: string, source: ProjectSourceSummaryUI): SourceUI {
  return {
    id: `project:${projectId}:${source.name}`,
    kind: 'project_snapshot',
    semantics: 'copy',
    title: source.name,
    path: source.path,
    fileCount: source.fileCount,
    byteSize: source.totalBytes,
    hidden: source.hidden,
    projectId,
    projectSourceName: source.name,
    capabilities: {
      deleteCopy: true,
      hide: true,
    },
  };
}

function sourceFromRepo(repo: GitHubRepoUI): SourceUI {
  return {
    id: `repo:${repo.id}`,
    kind: 'github_clone',
    semantics: 'reference',
    title: repo.fullName || repo.name,
    remoteUrl: repo.remoteUrl,
    ref: repo.defaultRef,
    byteSize: repo.byteSize,
    fileCount: repo.fileCount,
    refreshedAt: repo.lastFetchedAt,
    repo,
    capabilities: {
      browse: true,
      refresh: true,
      stopReference: true,
    },
  };
}

async function readJson(res: Response): Promise<any> {
  const text = await res.text().catch(() => '');
  if (!text.trim()) return null;
  try {
    return JSON.parse(text);
  } catch {
    return { error: text };
  }
}

export async function listProjectSources(
  projectId: string,
  signal?: AbortSignal,
): Promise<ProjectSourceSummaryUI[]> {
  const res = await fetch(`/api/projects/${encodeURIComponent(projectId)}/sources`, {
    method: 'GET',
    headers: { Accept: 'application/json' },
    signal,
  });
  const data = await readJson(res);
  if (!res.ok) {
    throw new Error(data?.error || data?.message || `Failed to load project sources (HTTP ${res.status})`);
  }
  const raw = Array.isArray(data?.sources) ? data.sources : [];
  return raw
    .map(normalizeProjectSource)
    .filter((source): source is ProjectSourceSummaryUI => source !== null);
}

export async function listSources(options: ListSourcesOptions = {}): Promise<ListSourcesResult> {
  // Preferred: the unified registry endpoint.
  try {
    const res = await fetch('/api/sources?scope=all', {
      method: 'GET',
      headers: { Accept: 'application/json' },
      signal: options.signal,
    });
    if (res.status !== 404 && res.status !== 501) {
      const data = await readJson(res);
      if (!res.ok) {
        throw new Error(data?.error || data?.message || `Failed to load sources (HTTP ${res.status})`);
      }
      const raw = Array.isArray(data?.sources) ? data.sources : [];
      const sources = raw
        .map(normalizeUnifiedSource)
        .filter((source): source is SourceUI => source !== null);
      return { sources, unified: true, errors: [] };
    }
  } catch (e: any) {
    if (e?.name === 'AbortError') throw e;
    // Network/parse failure on the unified route falls through to composition:
    // an aggregate view with a section error beats a dead dialog.
  }

  // Legacy composition. Sections load independently: one failing connector
  // must not blank the others.
  const errors: ListSourcesResult['errors'] = [];
  const sources: SourceUI[] = [];

  const [grantRules, repos, projectSources] = await Promise.all([
    listLocalComputerGrantRules().catch((e: any) => {
      if (e?.name === 'AbortError') throw e;
      // An absent permissions service means "no grants", not an error.
      if (e?.name !== 'PermissionsApiUnavailableError') {
        errors.push({ section: 'local', message: e?.message || 'Could not load local folder references.' });
      }
      return [] as PermissionRuleUI[];
    }),
    listGitHubRepositories().catch((e: any) => {
      if (e?.name === 'AbortError') throw e;
      if (!isGithubReposUnavailable(e)) {
        errors.push({ section: 'github', message: e?.message || 'Could not load GitHub repositories.' });
      }
      return [] as GitHubRepoUI[];
    }),
    options.projectId
      ? listProjectSources(options.projectId, options.signal).catch((e: any) => {
        if (e?.name === 'AbortError') throw e;
        errors.push({ section: 'project', message: e?.message || 'Could not load imported copies.' });
        return [] as ProjectSourceSummaryUI[];
      })
      : Promise.resolve([] as ProjectSourceSummaryUI[]),
  ]);

  for (const rule of grantRules) sources.push(sourceFromGrantRule(rule));
  for (const repo of repos) sources.push(sourceFromRepo(repo));
  for (const source of projectSources) {
    sources.push(sourceFromProjectSource(options.projectId!, source));
  }

  return { sources, unified: false, errors };
}

/**
 * Stop referencing a local folder: the grant rule is deleted; nothing on disk
 * is touched. Callers keep `settings.localComputer.selectedFolders` in sync.
 */
export async function stopReferencingLocalFolder(source: SourceUI): Promise<void> {
  if (!source.grantRuleId) throw new Error('This source is not backed by a folder reference.');
  await deletePermissionRule(source.grantRuleId);
}

export interface ProjectSourceMutationResult {
  sources: ProjectSourceSummaryUI[] | null;
  project: Record<string, any> | null;
}

async function projectSourceRequest(
  projectId: string,
  sourceName: string,
  init: RequestInit,
): Promise<ProjectSourceMutationResult> {
  const res = await fetch(
    `/api/projects/${encodeURIComponent(projectId)}/sources/${encodeURIComponent(sourceName)}`,
    { ...init, headers: { Accept: 'application/json', ...(init.headers || {}) } },
  );
  const data = await readJson(res);
  if (!res.ok) {
    throw new Error(data?.error || data?.message || `HTTP ${res.status}`);
  }
  const sources = Array.isArray(data?.sources)
    ? data.sources.map(normalizeProjectSource).filter(Boolean) as ProjectSourceSummaryUI[]
    : null;
  return { sources, project: isRecord(data?.project) ? data.project : null };
}

/** Delete an imported copy stored under the project. Originals are unaffected. */
export async function deleteImportedCopy(source: SourceUI): Promise<ProjectSourceMutationResult> {
  if (!source.projectId || !source.projectSourceName) {
    throw new Error('This source is not an imported copy.');
  }
  return projectSourceRequest(source.projectId, source.projectSourceName, { method: 'DELETE' });
}

export async function setImportedCopyHidden(
  source: SourceUI,
  hidden: boolean,
): Promise<ProjectSourceMutationResult> {
  if (!source.projectId || !source.projectSourceName) {
    throw new Error('This source is not an imported copy.');
  }
  return projectSourceRequest(source.projectId, source.projectSourceName, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ hidden }),
  });
}
