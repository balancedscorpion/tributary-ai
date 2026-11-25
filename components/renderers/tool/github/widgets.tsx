import React, { useState } from 'react';
import {
  Activity,
  CheckCircle2,
  ChevronRight,
  Globe,
  Lock,
  XCircle,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import {
  ActorInline,
  Avatar,
  AvatarStack,
  Chip,
  ExternalLinkIcon,
  MetaGrid,
  RelativeTime,
  SectionHeading,
  StatePill,
  StatusDot,
  ZoneLabel,
  classifyState,
  humanize,
  type StateKind,
} from './atoms';

// ---------- helpers ----------

function isObject(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function unwrapResult(raw: unknown): Record<string, any> {
  // Some envelopes nest the payload under `result` (e.g. gh command + JSON
  // strings); normalise to a plain object so widgets see the same shape.
  if (!isObject(raw)) return {};
  if ('result' in raw && isObject(raw.result)) return raw.result;
  if ('result' in raw && typeof raw.result === 'string') {
    try {
      const parsed = JSON.parse(raw.result);
      if (isObject(parsed)) return parsed;
    } catch {
      // fall through
    }
  }
  return raw;
}

function asArray<T = any>(value: unknown): T[] {
  return Array.isArray(value) ? (value as T[]) : [];
}

function pickString(...values: unknown[]): string | null {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) return value;
  }
  return null;
}

function normalizeTimestamp(value: unknown): string | null {
  if (typeof value === 'string' && value.trim()) return value;
  if (typeof value === 'number' && Number.isFinite(value)) {
    return new Date(value).toISOString();
  }
  return null;
}

function pickBool(...values: unknown[]): boolean | null {
  for (const value of values) {
    if (typeof value === 'boolean') return value;
  }
  return null;
}

function hasAnyKey(value: Record<string, any>, keys: string[]): boolean {
  return keys.some((key) => Object.prototype.hasOwnProperty.call(value, key));
}

function splitTokens(value: unknown): string[] {
  if (Array.isArray(value)) {
    return value
      .filter((item): item is string => typeof item === 'string' && item.trim().length > 0)
      .map((item) => item.trim());
  }
  if (typeof value !== 'string') return [];
  return value
    .split(/[,\s]+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function safeHttpUrl(value: unknown): string | null {
  const url = pickString(value);
  if (!url || !/^https?:\/\//.test(url)) return null;
  return url.replace(/\.git$/, '');
}

function shortSha(value: unknown): string {
  if (typeof value !== 'string' || !value) return '';
  return value.slice(0, 7);
}

function durationLabel(startISO: unknown, endISO: unknown): string | null {
  if (typeof startISO !== 'string' || typeof endISO !== 'string') return null;
  const s = new Date(startISO).getTime();
  const e = new Date(endISO).getTime();
  if (!Number.isFinite(s) || !Number.isFinite(e) || e < s) return null;
  const ms = e - s;
  if (ms < 1000) return `${ms}ms`;
  const sec = Math.round(ms / 1000);
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  const rem = sec % 60;
  return `${min}m ${rem}s`;
}

function pickLogin(actor: unknown): { login: string | null; avatar: string | null } {
  if (!isObject(actor)) return { login: null, avatar: null };
  const login =
    (typeof actor.login === 'string' && actor.login) ||
    (typeof actor.name === 'string' && actor.name) ||
    null;
  const avatar =
    (typeof actor.avatar_url === 'string' && actor.avatar_url) || null;
  return { login, avatar };
}

interface WidgetProps {
  raw: unknown;
  meta: Record<string, any>;
}

// ---------- status ----------

export function StatusWidget({ raw, meta }: WidgetProps) {
  const r = unwrapResult(raw);
  const status = r.status || r;
  const overall = isObject(status) ? status : {};
  const components = asArray(
    (isObject(r.status) && r.status.components) || r.components,
  );
  const isBackendStatus = hasAnyKey(overall, [
    'configured',
    'client_id_configured',
    'client_id',
    'secrets_initialized',
    'secrets_locked',
    'connected',
    'login',
    'scope',
    'read_tools_enabled',
    'allowed_repositories',
    'lastCheckedAt',
    'lastCheckOk',
    'lastCheckMessage',
  ]);
  const isStatuspage =
    typeof overall.indicator === 'string' || components.length > 0;
  if (isBackendStatus || !isStatuspage) {
    return <ConnectionStatusWidget raw={raw} meta={meta} />;
  }
  const indicator: string =
    (typeof overall.indicator === 'string' && overall.indicator) ||
    (typeof overall.status === 'string' && overall.status) ||
    'none';
  const description: string =
    (typeof overall.description === 'string' && overall.description) ||
    'Operational';
  const updated = overall.updated_at || overall.last_updated || overall.time;
  const indicatorKind: StateKind =
    indicator === 'none' || indicator === 'operational'
      ? 'success'
      : indicator === 'minor'
        ? 'pending'
        : indicator === 'major' || indicator === 'critical'
          ? 'failed'
          : 'unknown';

  return (
    <div className="flex flex-col gap-4">
      <ZoneLabel>Response</ZoneLabel>
      <div
        role="status"
        aria-live="polite"
        className={cn(
          'flex items-center gap-3 rounded-md border border-border bg-muted/40 p-3 border-l-2',
          indicatorKind === 'success' && 'border-l-emerald-500/60',
          indicatorKind === 'pending' && 'border-l-amber-500/60',
          indicatorKind === 'failed' && 'border-l-destructive/60',
          indicatorKind === 'unknown' && 'border-l-muted-foreground/40',
        )}
      >
        <StatusDot kind={indicatorKind} />
        <div className="min-w-0 flex-1">
          <div className="text-[13px] font-medium text-foreground">
            {description}
          </div>
          {updated && (
            <div className="mt-0.5 text-[11px] text-muted-foreground">
              <RelativeTime value={updated} prefix="Updated" />
            </div>
          )}
        </div>
        <Chip mono>{indicator}</Chip>
      </div>

      {components.length > 0 && (
        <div>
          <SectionHeading>Components</SectionHeading>
          <ul className="flex flex-col divide-y divide-border overflow-hidden rounded-md border border-border">
            {components.map((c: any, i: number) => {
              const cName = String(c?.name || `component-${i}`);
              const cStatus = classifyState(c?.status);
              // Keep the statuspage vocabulary on the pill (e.g. "Operational",
              // "Degraded Performance") rather than falling back to the generic
              // "Success" / "In Progress" derived from the StateKind.
              const cLabel = c?.status ? humanize(String(c.status)) : undefined;
              const desc = typeof c?.description === 'string' ? c.description : null;
              return (
                <li
                  key={`${cName}-${i}`}
                  className="flex items-center gap-2.5 px-3 py-2 text-[13px]"
                >
                  <StatusDot kind={cStatus} />
                  <span className="min-w-0 flex-1 truncate text-foreground">
                    {cName}
                  </span>
                  {desc && (
                    <span className="hidden text-[11px] text-muted-foreground sm:inline">
                      {desc}
                    </span>
                  )}
                  <StatePill kind={cStatus} label={cLabel} />
                </li>
              );
            })}
          </ul>
        </div>
      )}
    </div>
  );
}

// ---------- connection_status ----------

export function ConnectionStatusWidget({ raw }: WidgetProps) {
  const r = unwrapResult(raw);
  const outerStatus = isObject(r.status) ? r.status : {};
  const nestedStatus = isObject(outerStatus.status) ? outerStatus.status : {};
  const status = { ...r, ...outerStatus, ...nestedStatus };
  const account = isObject(status.account)
    ? status.account
    : isObject(status.user)
      ? status.user
      : {};
  const login = pickString(status.login, account.login, status.username, account.name);
  const hasPositiveCheck =
    status.connected === true ||
    status.lastCheckOk === true ||
    ((status.ok === true || outerStatus.ok === true) && Boolean(login));
  const hasNegativeCheck =
    status.connected === false ||
    status.lastCheckOk === false ||
    status.ok === false ||
    outerStatus.ok === false;
  const stateRaw =
    (typeof status.state === 'string' && status.state) ||
    (typeof status.connection === 'string' && status.connection) ||
    (hasPositiveCheck
      ? 'connected'
      : hasNegativeCheck
        ? 'not_connected'
        : 'unknown');
  const stateKind: StateKind =
    stateRaw === 'connected'
      ? 'success'
      : stateRaw === 'not_connected'
        ? 'failed'
        : stateRaw === 'expired' || stateRaw === 'reauth_required'
          ? 'pending'
          : 'unknown';
  const stateLabel =
    stateRaw === 'connected'
      ? 'Connected'
      : stateRaw === 'not_connected'
        ? 'Not connected'
        : stateRaw === 'expired'
          ? 'Token expired'
          : stateRaw === 'reauth_required'
            ? 'Reauthorization required'
            : 'Unknown';

  const name = pickString(status.name, account.name);
  const profileUrl = pickString(status.html_url, account.html_url);
  const message = pickString(status.lastCheckMessage, status.message, outerStatus.message);
  const inferredAuthMethod =
    message?.toLowerCase().includes('cli') || status.client_id_configured === false
      ? 'GitHub CLI'
      : null;
  const authMethod = pickString(status.auth_method, status.authMethod, inferredAuthMethod);
  const lastRefreshed = normalizeTimestamp(
    status.last_refreshed_at || status.last_refresh || status.lastCheckedAt
  );
  const expiresAt = normalizeTimestamp(status.token_expires_at || status.expires_at);
  const readToolsState =
    typeof status.read_tools_enabled === 'boolean'
      ? status.read_tools_enabled
      : typeof status.readToolsEnabled === 'boolean'
        ? status.readToolsEnabled
        : null;
  const configuredState = pickBool(status.configured);
  const allowedRepositories = asArray<string>(status.allowed_repositories);
  const secretsState =
    typeof status.secrets_initialized === 'boolean' ||
    typeof status.secrets_locked === 'boolean'
      ? `${status.secrets_initialized === false ? 'not initialized' : 'initialized'} · ${
          status.secrets_locked === true ? 'locked' : 'unlocked'
        }`
      : null;
  const clientState =
    typeof status.client_id_configured === 'boolean'
      ? status.client_id_configured
        ? pickString(status.client_id) || 'configured'
        : 'not configured'
      : null;
  const scopeTokens = splitTokens(status.scope);
  const scopes = Array.from(
    new Set([...asArray<string>(status.scopes), ...scopeTokens]),
  );
  const missing = asArray<string>(status.missing_scopes);

  return (
    <div className="flex flex-col gap-4">
      <ZoneLabel>Response</ZoneLabel>
      <div className="flex items-center gap-3 rounded-md border border-border bg-muted/40 p-3">
        <Avatar url={account.avatar_url} login={login || '?'} size={28} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5 text-[13px] font-medium text-foreground">
            {login || 'Not signed in'}
            {profileUrl && (
              <ExternalLinkIcon href={profileUrl} label="Open profile on github.com" />
            )}
          </div>
          {name && (
            <div className="mt-0.5 text-[11px] text-muted-foreground">
              {name}
            </div>
          )}
          {!name && message && (
            <div className="mt-0.5 truncate text-[11px] text-muted-foreground">
              {message}
            </div>
          )}
        </div>
        <StatePill kind={stateKind} label={stateLabel} />
      </div>

      <MetaGrid
        rows={[
          configuredState !== null
            ? { label: 'Configured', value: configuredState ? 'Yes' : 'No' }
            : null,
          authMethod ? { label: 'Auth method', value: <Chip mono>{authMethod}</Chip> } : null,
          lastRefreshed
            ? { label: 'Last checked', value: <RelativeTime value={lastRefreshed} /> }
            : null,
          expiresAt
            ? { label: 'Token expires', value: <RelativeTime value={expiresAt} /> }
            : null,
          readToolsState !== null
            ? { label: 'Read tools', value: readToolsState ? 'Enabled' : 'Disabled' }
            : null,
          secretsState ? { label: 'Secrets', value: secretsState } : null,
          clientState ? { label: 'OAuth client', value: clientState } : null,
          message && name ? { label: 'Last check', value: message } : null,
        ]}
      />

      {allowedRepositories.length > 0 && (
        <div>
          <SectionHeading>Allowed repositories</SectionHeading>
          <div className="flex flex-wrap gap-1.5">
            {allowedRepositories.map((repoName) => (
              <Chip key={repoName} mono>
                {repoName}
              </Chip>
            ))}
          </div>
        </div>
      )}

      {(scopes.length > 0 || missing.length > 0) && (
        <div>
          <SectionHeading>Scopes</SectionHeading>
          <div className="flex flex-wrap gap-1.5">
            {scopes.map((s) => (
              <Chip key={s} mono>
                {s}
              </Chip>
            ))}
            {missing.map((s) => (
              <Chip
                key={`missing-${s}`}
                mono
                outline
                className="border-amber-500/40 text-amber-600"
              >
                {s} <span className="ml-1 text-[10px] uppercase">required</span>
              </Chip>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

// ---------- repository_list ----------

function firstGitHubRemote(repo: unknown): Record<string, any> {
  if (!isObject(repo)) return {};
  const remotes = asArray<any>(repo.github_remotes).filter(isObject);
  return remotes[0] || {};
}

function repositoryOwner(repo: unknown): string | null {
  if (!isObject(repo)) return null;
  const owner = repo.owner;
  const ownerLabel = pickString(
    typeof owner === 'string' ? owner : null,
    isObject(owner) ? owner.login : null,
    isObject(owner) ? owner.name : null,
    repo.owner_login,
  );
  if (ownerLabel) return ownerLabel;
  const fullName = pickString(repo.full_name, repo.nameWithOwner, repo.repository);
  return fullName?.includes('/') ? fullName.split('/')[0] : null;
}

function repositoryLocalPath(repo: unknown): string | null {
  if (!isObject(repo)) return null;
  return pickString(repo.repo_path, repo.local_path, repo.path);
}

function repositoryRemoteName(repo: unknown): string | null {
  if (!isObject(repo)) return null;
  const firstRemote = firstGitHubRemote(repo);
  return pickString(repo.remote_name, firstRemote.name, firstRemote.remote_name);
}

function repositoryRemoteUrl(repo: unknown): string | null {
  if (!isObject(repo)) return null;
  const firstRemote = firstGitHubRemote(repo);
  return pickString(
    repo.remote_url,
    firstRemote.url,
    firstRemote.remote_url,
    repo.html_url,
    repo.url,
  );
}

export function RepositoryListWidget({ raw }: WidgetProps) {
  const r = unwrapResult(raw);
  const repos = asArray<any>(r.repositories || r.repos || raw);
  const projectId = pickString(r.project_id);
  if (!repos.length) {
    return (
      <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[13px] text-muted-foreground">
        No repositories returned.
      </div>
    );
  }

  // Normalise GitHub-API rows (visibility/updated_at/html_url) and backend
  // ProjectRepository rows (owner/repo/repo_path/remote_*/resolvable —
  // server/internal/githubcli/types.go) into one record. Columns render only
  // for fields the payload actually carries: no Visibility/Updated columns
  // full of dashes for CLI payloads.
  const rows = repos.map((repo: any) => {
    const name = repoFromPath(repo) || 'unknown';
    const owner = repositoryOwner(repo);
    const localPath = repositoryLocalPath(repo);
    const remote = repositoryRemoteUrl(repo);
    const remoteName = repositoryRemoteName(repo);
    const isPrivate =
      typeof repo?.visibility === 'string'
        ? repo.visibility.toLowerCase() === 'private'
        : typeof repo?.isPrivate === 'boolean'
          ? repo.isPrivate
          : typeof repo?.private === 'boolean'
            ? repo.private
            : null;
    const updated =
      repo?.updated_at || repo?.updatedAt || repo?.pushed_at || repo?.pushedAt || null;
    const githubRemotes = asArray<any>(repo?.github_remotes);
    const extraRemotes = githubRemotes.length > 1 ? githubRemotes.length - 1 : 0;
    const resolvable = typeof repo?.resolvable === 'boolean' ? repo.resolvable : null;
    const url = safeHttpUrl(repo?.html_url) || safeHttpUrl(repo?.url) || safeHttpUrl(remote);
    return {
      name,
      owner,
      localPath,
      remote,
      remoteName,
      isPrivate,
      updated,
      url,
      extraRemotes,
      resolvable,
    };
  });
  const hasOwner = rows.some((row) => row.owner !== null);
  const hasLocalPath = rows.some((row) => row.localPath !== null);
  const hasVisibility = rows.some((row) => row.isPrivate !== null);
  const hasUpdated = rows.some((row) => row.updated !== null);
  const hasRemote = rows.some((row) => row.remote !== null || row.remoteName !== null);
  const hasResolvable = rows.some((row) => row.resolvable !== null);

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center justify-between">
        <ZoneLabel>
          Response · {repos.length} {repos.length === 1 ? 'repository' : 'repositories'}
        </ZoneLabel>
        {projectId && (
          <span className="font-mono text-[11px] text-muted-foreground/60">{projectId}</span>
        )}
      </div>
      <div className="overflow-x-auto rounded-md border border-border">
        <table className="min-w-full border-collapse text-[13px]">
          <thead>
            <tr className="border-b border-border bg-muted/40 text-left text-[11px] uppercase tracking-wide text-muted-foreground/60">
              <th className="px-3 py-2 font-medium">Repository</th>
              {hasOwner && <th className="px-3 py-2 font-medium">Owner</th>}
              {hasLocalPath && <th className="px-3 py-2 font-medium">Local path</th>}
              {hasRemote && <th className="px-3 py-2 font-medium">Remote</th>}
              {hasResolvable && <th className="px-3 py-2 font-medium">Resolvable</th>}
              {hasVisibility && <th className="px-3 py-2 font-medium">Visibility</th>}
              {hasUpdated && <th className="px-3 py-2 font-medium">Updated</th>}
            </tr>
          </thead>
          <tbody>
            {rows.map((row, i) => (
              <tr
                key={`${row.name}-${i}`}
                className="border-b border-border last:border-b-0 hover:bg-muted/30"
              >
                <td className="px-3 py-2 font-mono text-foreground [overflow-wrap:anywhere]">
                  <span className="inline-flex items-center gap-1.5">
                    {row.name}
                    <ExternalLinkIcon href={row.url} label={`Open ${row.name} on github.com`} />
                  </span>
                </td>
                {hasOwner && (
                  <td className="px-3 py-2 font-mono text-muted-foreground [overflow-wrap:anywhere]">
                    {row.owner || <span className="text-muted-foreground/60">—</span>}
                  </td>
                )}
                {hasLocalPath && (
                  <td className="px-3 py-2 font-mono text-muted-foreground [overflow-wrap:anywhere]">
                    {row.localPath || <span className="text-muted-foreground/60">—</span>}
                  </td>
                )}
                {hasRemote && (
                  <td className="px-3 py-2 text-muted-foreground [overflow-wrap:anywhere]">
                    {row.remote ? (
                      <span className="inline-flex flex-wrap items-center gap-1.5 font-mono">
                        {row.remoteName && <Chip mono>{row.remoteName}</Chip>}
                        <span className="break-all">{row.remote}</span>
                        {row.extraRemotes > 0 && (
                          <span className="text-muted-foreground/60"> +{row.extraRemotes}</span>
                        )}
                        <ExternalLinkIcon
                          href={safeHttpUrl(row.remote)}
                          label={`Open ${row.name} remote`}
                        />
                      </span>
                    ) : (
                      <span className="text-muted-foreground/60">—</span>
                    )}
                  </td>
                )}
                {hasResolvable && (
                  <td className="px-3 py-2">
                    {row.resolvable === null ? (
                      <span className="text-muted-foreground/60">—</span>
                    ) : (
                      <StatePill
                        kind={row.resolvable ? 'success' : 'failed'}
                        label={row.resolvable ? 'Yes' : 'No'}
                      />
                    )}
                  </td>
                )}
                {hasVisibility && (
                  <td className="px-3 py-2 text-muted-foreground">
                    {row.isPrivate === null ? (
                      <span className="text-muted-foreground/60">—</span>
                    ) : row.isPrivate ? (
                      <span className="inline-flex items-center gap-1">
                        <Lock size={12} aria-hidden /> Private
                      </span>
                    ) : (
                      <span className="inline-flex items-center gap-1">
                        <Globe size={12} aria-hidden /> Public
                      </span>
                    )}
                  </td>
                )}
                {hasUpdated && (
                  <td className="px-3 py-2 text-muted-foreground [overflow-wrap:anywhere]">
                    {row.updated ? (
                      <RelativeTime value={row.updated} />
                    ) : (
                      <span className="text-muted-foreground/60">—</span>
                    )}
                  </td>
                )}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ---------- pull_request_list ----------

function repoFromPath(value: unknown): string | null {
  if (typeof value === 'string') return value || null;
  // Backend payloads pass repository as a ProjectRepository object
  // (server/internal/githubcli/types.go) — derive a stable display label
  // instead of dropping it.
  if (isObject(value)) {
    const fullName = pickString(value.full_name, value.nameWithOwner, value.name);
    if (fullName) return fullName;
    const owner = repositoryOwner(value);
    const repo = pickString(value.repo);
    if (owner && repo) return `${owner}/${repo}`;
    const repoPath = pickString(value.repo_path);
    if (repoPath) return repoPath.split('/').filter(Boolean).pop() || repoPath;
  }
  return null;
}

function prState(item: any): StateKind {
  if (item?.is_draft) return 'draft';
  if (typeof item?.merged_at === 'string' && item.merged_at) return 'merged';
  return classifyState(item?.state);
}

export function PullRequestListWidget({ raw, meta }: WidgetProps) {
  const r = unwrapResult(raw);
  const list = asArray<any>(r.pull_requests || r.pullRequests || r.items);
  const repo = repoFromPath(r.repository || meta?.tool_args?.repository);
  if (!list.length) {
    return (
      <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[13px] text-muted-foreground">
        No pull requests match.
      </div>
    );
  }
  const counts = list.reduce(
    (acc: Record<string, number>, item: any) => {
      const k = prState(item);
      acc[k] = (acc[k] || 0) + 1;
      return acc;
    },
    {} as Record<string, number>,
  );

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <ZoneLabel>
          Response · {list.length} {list.length === 1 ? 'pull request' : 'pull requests'}
        </ZoneLabel>
        <div className="flex flex-wrap items-center gap-1">
          {(['open', 'draft', 'merged', 'closed'] as StateKind[]).map((k) =>
            counts[k] ? <StatePill key={k} kind={k} label={`${counts[k]} ${k}`} /> : null,
          )}
        </div>
      </div>
      <ul className="flex flex-col overflow-hidden rounded-md border border-border">
        {list.map((pr: any, i: number) => (
          <PullRequestRow key={pr?.number ?? i} pr={pr} repo={repo} />
        ))}
      </ul>
    </div>
  );
}

function PullRequestRow({ pr, repo }: { pr: any; repo: string | null }) {
  const state = prState(pr);
  const number = pr?.number;
  const title = pr?.title || `Pull request #${number}`;
  const { login, avatar } = pickLogin(pr?.author);
  const head = typeof pr?.head_ref_name === 'string' ? pr.head_ref_name : null;
  const base = typeof pr?.base_ref_name === 'string' ? pr.base_ref_name : null;
  const updated = pr?.updated_at || pr?.created_at;
  const url = typeof pr?.url === 'string' ? pr.url : null;
  const labels = asArray<any>(pr?.labels);
  const reviewDecision: string | null =
    typeof pr?.review_decision === 'string' ? pr.review_decision : null;
  const reviewKind: StateKind | null =
    reviewDecision === 'APPROVED'
      ? 'success'
      : reviewDecision === 'CHANGES_REQUESTED'
        ? 'failed'
        : reviewDecision === 'REVIEW_REQUIRED'
          ? 'pending'
          : null;

  return (
    <li className="flex flex-col gap-1.5 border-b border-border px-3 py-2.5 last:border-b-0 hover:bg-muted/30">
      <div className="flex flex-wrap items-center gap-2">
        <StatePill kind={state} />
        {number != null && (
          <span className="font-mono text-[12px] text-muted-foreground">
            {`#${number}`}
          </span>
        )}
        <span className="min-w-0 flex-1 text-[13px] text-foreground [overflow-wrap:anywhere]">
          {title}
        </span>
        <ExternalLinkIcon href={url} label={`Open PR on github.com`} />
      </div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
        {repo && <span className="font-mono">{repo}</span>}
        {login && (
          <>
            <span className="text-muted-foreground/40">·</span>
            <ActorInline login={login} avatarUrl={avatar} />
          </>
        )}
        {head && base && (
          <>
            <span className="text-muted-foreground/40">·</span>
            <span className="inline-flex items-center gap-1">
              <Chip mono>{head}</Chip>
              <ChevronRight size={10} aria-hidden />
              <Chip mono>{base}</Chip>
            </span>
          </>
        )}
        {updated && (
          <>
            <span className="text-muted-foreground/40">·</span>
            <RelativeTime value={updated} />
          </>
        )}
        {reviewKind && (
          <>
            <span className="text-muted-foreground/40">·</span>
            <span className="inline-flex items-center gap-1">
              <StatusDot kind={reviewKind} />
              {reviewDecision?.toLowerCase().replace(/_/g, ' ')}
            </span>
          </>
        )}
        {labels.slice(0, 3).map((l: any, i: number) => (
          <Chip key={i}>{String(l?.name || l)}</Chip>
        ))}
        {labels.length > 3 && (
          <span className="text-muted-foreground/60">+{labels.length - 3}</span>
        )}
      </div>
    </li>
  );
}

// ---------- pull_request (detail) ----------

export function PullRequestWidget({ raw, meta }: WidgetProps) {
  const r = unwrapResult(raw);
  const pr = isObject(r.pull_request) ? r.pull_request : isObject(r) ? r : {};
  const repo = repoFromPath(r.repository || meta?.tool_args?.repository);
  const state = prState(pr);
  const { login, avatar } = pickLogin(pr.author);
  const number = pr.number;
  const title = pr.title || (number != null ? `Pull request #${number}` : 'Pull request');
  const url = typeof pr.url === 'string' ? pr.url : null;
  const head = typeof pr.head_ref_name === 'string' ? pr.head_ref_name : null;
  const base = typeof pr.base_ref_name === 'string' ? pr.base_ref_name : null;
  const labels = asArray<any>(pr.labels);
  const assignees = asArray<any>(pr.assignees).map(pickLogin).filter((a) => a.login);
  const reviewers = asArray<any>(pr.reviewers || pr.requested_reviewers).map(pickLogin).filter((a) => a.login);
  const body = typeof pr.body === 'string' ? pr.body : '';
  const mergeable = pr.mergeable_state || (pr.mergeable === true ? 'mergeable' : pr.mergeable === false ? 'conflicting' : null);

  return (
    <div className="flex flex-col gap-4">
      <ZoneLabel>Response</ZoneLabel>
      <div>
        <div className="flex flex-wrap items-center gap-2">
          <StatePill kind={state} />
          {number != null && (
            <span className="font-mono text-[12px] text-muted-foreground">
              {`#${number}`}
            </span>
          )}
          <ExternalLinkIcon href={url} label="Open PR on github.com" />
        </div>
        <h2 className="mt-1.5 text-[15px] font-semibold text-foreground [overflow-wrap:anywhere]">
          {title}
        </h2>
        <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
          {login && <ActorInline login={login} avatarUrl={avatar} />}
          {pr.created_at && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <RelativeTime value={pr.created_at} prefix="opened" />
            </>
          )}
          {pr.updated_at && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <RelativeTime value={pr.updated_at} prefix="updated" />
            </>
          )}
        </div>
      </div>

      <MetaGrid
        rows={[
          repo ? { label: 'Repository', value: repo, mono: true } : null,
          head && base
            ? {
                label: 'Branches',
                value: (
                  <span className="inline-flex flex-wrap items-center gap-1">
                    <Chip mono>{head}</Chip>
                    <ChevronRight size={10} aria-hidden />
                    <Chip mono>{base}</Chip>
                  </span>
                ),
              }
            : null,
          mergeable
            ? {
                label: 'Mergeable',
                value: (
                  <StatePill
                    kind={classifyState(mergeable)}
                    label={mergeable.toString().replace(/_/g, ' ')}
                  />
                ),
              }
            : null,
          assignees.length
            ? {
                label: 'Assignees',
                value: (
                  <AvatarStack
                    items={assignees.map((a) => ({
                      login: a.login!,
                      avatar_url: a.avatar,
                    }))}
                  />
                ),
              }
            : null,
          reviewers.length
            ? {
                label: 'Reviewers',
                value: (
                  <AvatarStack
                    items={reviewers.map((a) => ({
                      login: a.login!,
                      avatar_url: a.avatar,
                    }))}
                  />
                ),
              }
            : null,
          labels.length
            ? {
                label: 'Labels',
                value: (
                  <span className="inline-flex flex-wrap gap-1">
                    {labels.map((l: any, i: number) => (
                      <Chip key={i}>{String(l?.name || l)}</Chip>
                    ))}
                  </span>
                ),
              }
            : null,
        ]}
      />

      {body.trim() && (
        <div>
          <SectionHeading>Description</SectionHeading>
          <CollapsibleBody body={body} />
        </div>
      )}
    </div>
  );
}

function CollapsibleBody({ body }: { body: string }) {
  const [expanded, setExpanded] = useState(false);
  const long = body.length > 600;
  const text = expanded || !long ? body : `${body.slice(0, 600)}…`;
  return (
    <div className="flex flex-col gap-2">
      <pre className="font-sans whitespace-pre-wrap [overflow-wrap:anywhere] rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[13px] text-foreground">
        {text}
      </pre>
      {long && (
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          className="self-start rounded text-[11px] text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
        >
          {expanded ? 'Show less' : 'Show more'}
        </button>
      )}
    </div>
  );
}

// ---------- issue_list ----------

function issueState(item: any): StateKind {
  if (item?.state_reason === 'not_planned') return 'not_planned';
  if (item?.state_reason === 'completed') return 'completed';
  return classifyState(item?.state);
}

export function IssueListWidget({ raw, meta }: WidgetProps) {
  const r = unwrapResult(raw);
  // github_list_issues nests the backend IssueList object under `issues`
  // (server/internal/chat/tools/github_tools.go wraps githubcli.IssueList:
  // {repository, issues, total_count, limit, truncated}); unwrap it before
  // falling back to flat API-shaped arrays.
  const listPayload = isObject(r.issues) ? r.issues : r;
  const list = asArray<any>(listPayload.issues || listPayload.items || r.items);
  const repo =
    repoFromPath(listPayload.repository) ||
    repoFromPath(r.repository) ||
    repoFromPath(meta?.tool_args?.repository);
  const totalCount =
    typeof listPayload.total_count === 'number' ? listPayload.total_count : null;
  const limit = typeof listPayload.limit === 'number' ? listPayload.limit : null;
  const truncated = listPayload.truncated === true;
  if (!list.length) {
    return (
      <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[13px] text-muted-foreground">
        No issues match.
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <ZoneLabel>
          Response · {list.length} {list.length === 1 ? 'issue' : 'issues'}
          {totalCount != null && totalCount > list.length ? ` of ${totalCount}` : ''}
        </ZoneLabel>
        {truncated && (
          <StatePill
            kind="pending"
            label={limit != null ? `truncated at ${limit}` : 'truncated'}
          />
        )}
      </div>
      <ul className="flex flex-col overflow-hidden rounded-md border border-border">
        {list.map((issue: any, i: number) => {
          const state = issueState(issue);
          const number = issue?.number;
          const title = issue?.title || `Issue #${number}`;
          const { login, avatar } = pickLogin(issue?.author);
          const closed = normalizeTimestamp(issue?.closed_at);
          const updated = issue?.updated_at || issue?.created_at;
          const url = typeof issue?.url === 'string' ? issue.url : null;
          const labels = asArray<any>(issue?.labels);
          const assignees = asArray<any>(issue?.assignees)
            .map(pickLogin)
            .filter((a) => a.login);
          const comments =
            typeof issue?.comments_count === 'number'
              ? issue.comments_count
              : Array.isArray(issue?.comments)
                ? issue.comments.length
                : null;
          return (
            <li
              key={number ?? i}
              className="flex flex-col gap-1.5 border-b border-border px-3 py-2.5 last:border-b-0 hover:bg-muted/30"
            >
              <div className="flex flex-wrap items-center gap-2">
                <StatePill kind={state} />
                {number != null && (
                  <span className="font-mono text-[12px] text-muted-foreground">
                    {`#${number}`}
                  </span>
                )}
                <span className="min-w-0 flex-1 text-[13px] text-foreground [overflow-wrap:anywhere]">
                  {title}
                </span>
                <ExternalLinkIcon href={url} label="Open issue on github.com" />
              </div>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
                {repo && <span className="font-mono">{repo}</span>}
                {login && (
                  <>
                    <span className="text-muted-foreground/40">·</span>
                    <ActorInline login={login} avatarUrl={avatar} />
                  </>
                )}
                {closed ? (
                  <>
                    <span className="text-muted-foreground/40">·</span>
                    <RelativeTime value={closed} prefix="closed" />
                  </>
                ) : (
                  updated && (
                    <>
                      <span className="text-muted-foreground/40">·</span>
                      <RelativeTime value={updated} />
                    </>
                  )
                )}
                {typeof comments === 'number' && (
                  <>
                    <span className="text-muted-foreground/40">·</span>
                    <span>{comments} comments</span>
                  </>
                )}
                {assignees.length > 0 && (
                  <>
                    <span className="text-muted-foreground/40">·</span>
                    <AvatarStack
                      items={assignees.map((a) => ({
                        login: a.login!,
                        avatar_url: a.avatar,
                      }))}
                    />
                  </>
                )}
                {labels.slice(0, 3).map((l: any, j: number) => (
                  <Chip key={j}>{String(l?.name || l)}</Chip>
                ))}
                {labels.length > 3 && (
                  <span className="text-muted-foreground/60">+{labels.length - 3}</span>
                )}
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

// ---------- issue (detail) ----------

export function IssueWidget({ raw, meta }: WidgetProps) {
  const r = unwrapResult(raw);
  // github_get_issue nests the backend IssueResult ({repository, issue})
  // under `issue` (server/internal/chat/tools/github_tools.go); peel both
  // levels so flat API payloads and the wrapped backend shape render alike.
  const container = isObject(r.issue) ? r.issue : isObject(r) ? r : {};
  const issue = isObject(container.issue) ? container.issue : container;
  const repo =
    repoFromPath(container.repository) ||
    repoFromPath(r.repository) ||
    repoFromPath(meta?.tool_args?.repository);
  const state = issueState(issue);
  const { login, avatar } = pickLogin(issue.author);
  const number = issue.number;
  const title = issue.title || (number != null ? `Issue #${number}` : 'Issue');
  const url = typeof issue.url === 'string' ? issue.url : null;
  const labels = asArray<any>(issue.labels);
  const assignees = asArray<any>(issue.assignees).map(pickLogin).filter((a) => a.login);
  const closedAt = normalizeTimestamp(issue.closed_at);
  const body = typeof issue.body === 'string' ? issue.body : '';

  return (
    <div className="flex flex-col gap-4">
      <ZoneLabel>Response</ZoneLabel>
      <div>
        <div className="flex flex-wrap items-center gap-2">
          <StatePill kind={state} />
          {number != null && (
            <span className="font-mono text-[12px] text-muted-foreground">
              {`#${number}`}
            </span>
          )}
          <ExternalLinkIcon href={url} label="Open issue on github.com" />
        </div>
        <h2 className="mt-1.5 text-[15px] font-semibold text-foreground [overflow-wrap:anywhere]">
          {title}
        </h2>
        <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
          {login && <ActorInline login={login} avatarUrl={avatar} />}
          {issue.created_at && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <RelativeTime value={issue.created_at} prefix="opened" />
            </>
          )}
          {issue.updated_at && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <RelativeTime value={issue.updated_at} prefix="updated" />
            </>
          )}
          {closedAt && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <RelativeTime value={closedAt} prefix="closed" />
            </>
          )}
        </div>
      </div>

      <MetaGrid
        rows={[
          repo ? { label: 'Repository', value: repo, mono: true } : null,
          assignees.length
            ? {
                label: 'Assignees',
                value: (
                  <AvatarStack
                    items={assignees.map((a) => ({
                      login: a.login!,
                      avatar_url: a.avatar,
                    }))}
                  />
                ),
              }
            : null,
          labels.length
            ? {
                label: 'Labels',
                value: (
                  <span className="inline-flex flex-wrap gap-1">
                    {labels.map((l: any, i: number) => (
                      <Chip key={i}>{String(l?.name || l)}</Chip>
                    ))}
                  </span>
                ),
              }
            : null,
          typeof issue.comments_count === 'number'
            ? { label: 'Comments', value: String(issue.comments_count) }
            : Array.isArray(issue.comments)
              ? { label: 'Comments', value: String(issue.comments.length) }
              : null,
        ]}
      />

      {body.trim() && (
        <div>
          <SectionHeading>Description</SectionHeading>
          <CollapsibleBody body={body} />
        </div>
      )}
    </div>
  );
}

// ---------- workflow_run_list ----------

function runStateKind(item: any): StateKind {
  const status = String(item?.status || '').toLowerCase();
  const conclusion = String(item?.conclusion || '').toLowerCase();
  if (status === 'in_progress' || status === 'queued' || status === 'requested' || status === 'waiting' || status === 'pending')
    return 'in_progress';
  if (conclusion) return classifyState(conclusion);
  return classifyState(status);
}

export function WorkflowRunListWidget({ raw, meta }: WidgetProps) {
  const r = unwrapResult(raw);
  const list = asArray<any>(r.workflow_runs || r.runs);
  const repo = repoFromPath(r.repository || meta?.tool_args?.repository);
  if (!list.length) {
    return (
      <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[13px] text-muted-foreground">
        No workflow runs.
      </div>
    );
  }
  return (
    <div className="flex flex-col gap-2">
      <ZoneLabel>
        Response · {list.length} {list.length === 1 ? 'run' : 'runs'}
      </ZoneLabel>
      <ul className="flex flex-col overflow-hidden rounded-md border border-border">
        {list.map((run: any, i: number) => {
          const kind = runStateKind(run);
          const number = run?.number;
          const workflow = run?.workflow_name || run?.name || 'workflow';
          const display = run?.display_title || run?.name;
          const branch = typeof run?.head_branch === 'string' ? run.head_branch : null;
          const event = typeof run?.event === 'string' ? run.event : null;
          const sha = shortSha(run?.head_sha);
          const url = typeof run?.url === 'string' ? run.url : null;
          const actor = pickLogin(run?.actor || run?.triggering_actor);
          const started = run?.started_at || run?.created_at;
          const dur = durationLabel(run?.started_at, run?.updated_at);
          return (
            <li
              key={run?.database_id ?? number ?? i}
              className="flex flex-col gap-1.5 border-b border-border px-3 py-2.5 last:border-b-0 hover:bg-muted/30"
            >
              <div className="flex flex-wrap items-center gap-2">
                <StatusDot kind={kind} pulse={kind === 'in_progress'} />
                <span className="min-w-0 flex-1 text-[13px] text-foreground [overflow-wrap:anywhere]">
                  {workflow}
                  {display && display !== workflow && (
                    <span className="text-muted-foreground"> · {display}</span>
                  )}
                </span>
                {number != null && (
                  <span className="font-mono text-[11px] text-muted-foreground">
                    {`#${number}`}
                  </span>
                )}
                <ExternalLinkIcon href={url} label="Open run on github.com" />
              </div>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
                {repo && <span className="font-mono">{repo}</span>}
                {branch && (
                  <>
                    <span className="text-muted-foreground/40">·</span>
                    <Chip mono>{branch}</Chip>
                  </>
                )}
                {event && (
                  <>
                    <span className="text-muted-foreground/40">·</span>
                    <Chip>{event}</Chip>
                  </>
                )}
                {actor.login && (
                  <>
                    <span className="text-muted-foreground/40">·</span>
                    <ActorInline login={actor.login} avatarUrl={actor.avatar} />
                  </>
                )}
                {sha && (
                  <>
                    <span className="text-muted-foreground/40">·</span>
                    <span className="font-mono">{sha}</span>
                  </>
                )}
                {started && (
                  <>
                    <span className="text-muted-foreground/40">·</span>
                    <RelativeTime value={started} prefix="started" />
                  </>
                )}
                {dur && (
                  <>
                    <span className="text-muted-foreground/40">·</span>
                    <span>{dur}</span>
                  </>
                )}
                <StatePill kind={kind} />
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

// ---------- workflow_run_jobs ----------

export function WorkflowRunJobsWidget({ raw, meta }: WidgetProps) {
  const r = unwrapResult(raw);
  const jobs = asArray<any>(r.jobs);
  const repo = repoFromPath(r.repository || meta?.tool_args?.repository);
  const runId = r.run_id || meta?.tool_args?.run_id;

  return (
    <div className="flex flex-col gap-4">
      <ZoneLabel>Response</ZoneLabel>
      <MetaGrid
        rows={[
          repo ? { label: 'Repository', value: repo, mono: true } : null,
          runId
            ? { label: 'Run', value: <span className="font-mono">{String(runId)}</span> }
            : null,
          {
            label: 'Jobs',
            value: `${jobs.length} ${jobs.length === 1 ? 'job' : 'jobs'}`,
          },
        ]}
      />
      {jobs.length === 0 ? (
        <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[13px] text-muted-foreground">
          No jobs reported.
        </div>
      ) : (
        <ul className="flex flex-col gap-2">
          {jobs.map((job: any, i: number) => (
            <JobItem key={job?.database_id ?? job?.id ?? i} job={job} />
          ))}
        </ul>
      )}
    </div>
  );
}

function JobItem({ job }: { job: any }) {
  const [open, setOpen] = useState(false);
  const kind = runStateKind(job);
  const name = job?.name || 'job';
  const dur = durationLabel(job?.started_at, job?.completed_at);
  const url = typeof job?.url === 'string' ? job.url : null;
  const steps = asArray<any>(job?.steps);
  const failed = kind === 'failed';
  return (
    <li
      className={cn(
        'overflow-hidden rounded-md border border-border bg-muted/30',
        failed && 'border-l-2 border-l-destructive/60',
      )}
    >
      <button
        type="button"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        className="flex w-full items-center gap-2 px-3 py-2 text-left text-[13px] hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-inset"
      >
        <ChevronRight
          size={12}
          aria-hidden
          className={cn('text-muted-foreground transition-transform', open && 'rotate-90')}
        />
        <StatusDot kind={kind} pulse={kind === 'in_progress'} />
        <span className="min-w-0 flex-1 truncate text-foreground">{name}</span>
        {dur && (
          <span className="font-mono text-[11px] text-muted-foreground">{dur}</span>
        )}
        <StatePill kind={kind} />
        <ExternalLinkIcon href={url} label={`Open job ${name} on github.com`} />
      </button>
      {open && steps.length > 0 && (
        <div className="border-t border-border bg-background/60 px-3 py-2">
          <table className="w-full table-fixed border-collapse text-[12px]">
            <thead>
              <tr className="text-left text-[10px] uppercase tracking-wide text-muted-foreground/60">
                <th className="w-8 py-1 font-medium">#</th>
                <th className="w-1/2 py-1 font-medium">Name</th>
                <th className="w-1/4 py-1 font-medium">Conclusion</th>
                <th className="w-1/4 py-1 font-medium">Duration</th>
              </tr>
            </thead>
            <tbody>
              {steps.map((s: any, i: number) => {
                const sk = runStateKind(s);
                const sd = durationLabel(s?.started_at, s?.completed_at);
                return (
                  <tr key={i} className="border-t border-border/60">
                    <td className="py-1 font-mono text-muted-foreground">
                      {s?.number ?? i + 1}
                    </td>
                    <td className="py-1 text-foreground [overflow-wrap:anywhere]">
                      {s?.name || `step ${i + 1}`}
                    </td>
                    <td className="py-1">
                      <span className="inline-flex items-center gap-1">
                        <StatusDot kind={sk} />
                        {String(s?.conclusion || s?.status || '—')}
                      </span>
                    </td>
                    <td className="py-1 font-mono text-muted-foreground">{sd || '—'}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </li>
  );
}

// ---------- approval ----------

export function ApprovalWidget({ raw }: WidgetProps) {
  const r = unwrapResult(raw);
  const approval = isObject(r.approval) ? r.approval : isObject(r) ? r : {};
  const state = String(approval.state || 'pending').toLowerCase();
  const kind: StateKind =
    state === 'approved'
      ? 'success'
      : state === 'rejected'
        ? 'failed'
        : state === 'expired'
          ? 'failed'
          : 'pending';
  const title = approval.title || approval.action || 'Pending approval';
  const detail = approval.detail || approval.summary || null;
  const created = approval.created_at;
  const decidedAt = approval.decided_at;
  const target = isObject(approval.target) ? approval.target : null;
  const content = isObject(approval.content) ? approval.content : null;
  const preview =
    (typeof approval.preview === 'string' && approval.preview) ||
    (content && typeof content.preview === 'string' ? content.preview : null) ||
    (content && typeof content.command === 'string' ? `$ ${content.command}` : null);

  return (
    <div className="flex flex-col gap-4">
      <ZoneLabel>Response</ZoneLabel>
      <div
        className={cn(
          'rounded-md border border-border bg-muted/40 p-3 border-l-2',
          kind === 'pending' && 'border-l-amber-500/60',
          kind === 'success' && 'border-l-emerald-500/60',
          kind === 'failed' && 'border-l-destructive/60',
        )}
      >
        <div className="flex flex-wrap items-center gap-2">
          <Activity
            size={14}
            className={cn(
              'shrink-0',
              kind === 'pending' && 'text-amber-600',
              kind === 'success' && 'text-emerald-600',
              kind === 'failed' && 'text-destructive',
            )}
            aria-hidden
          />
          <h3 className="min-w-0 flex-1 text-[13px] font-medium text-foreground [overflow-wrap:anywhere]">
            {title}
          </h3>
          <StatePill kind={kind} label={state.replace(/_/g, ' ')} />
        </div>
        {detail && (
          <p className="mt-1.5 text-[13px] text-foreground whitespace-pre-wrap [overflow-wrap:anywhere]">
            {detail}
          </p>
        )}
      </div>

      {(target || approval.action) && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
          {target && (
            <div className="rounded-md border border-border bg-muted/40 p-3">
              <SectionHeading>Target</SectionHeading>
              <MetaGrid
                rows={Object.entries(target).map(([k, v]) => ({
                  label: k,
                  value: String(v ?? ''),
                  mono: /id|ref|sha|path|repository|branch/.test(k),
                }))}
              />
            </div>
          )}
          {approval.action && (
            <div className="rounded-md border border-border bg-muted/40 p-3">
              <SectionHeading>Action</SectionHeading>
              <div className="font-mono text-[12px] text-foreground [overflow-wrap:anywhere]">
                {String(approval.action)}
              </div>
              {approval.tool_name && (
                <div className="mt-1 text-[11px] text-muted-foreground">
                  via <span className="font-mono">{String(approval.tool_name)}</span>
                </div>
              )}
            </div>
          )}
        </div>
      )}

      {preview && (
        <div>
          <SectionHeading>Preview</SectionHeading>
          <pre className="font-mono whitespace-pre-wrap [overflow-wrap:anywhere] rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[12px] text-foreground">
            {preview}
          </pre>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
        {created && (
          <span>
            <RelativeTime value={created} prefix="requested" />
          </span>
        )}
        {decidedAt && (
          <>
            <span className="text-muted-foreground/40">·</span>
            <span>
              <RelativeTime value={decidedAt} prefix="decided" />
            </span>
          </>
        )}
        {approval.id && (
          <>
            <span className="text-muted-foreground/40">·</span>
            <span className="font-mono">approval {String(approval.id)}</span>
          </>
        )}
      </div>
    </div>
  );
}

// ---------- gh_command ----------

export function GhCommandWidget({ raw }: WidgetProps) {
  const r = isObject(raw) ? raw : {};
  const result = isObject(r.result) ? r.result : r;
  const stdout =
    (typeof result.stdout === 'string' && result.stdout) ||
    (typeof r.stdout === 'string' && r.stdout) ||
    '';
  const stderr =
    (typeof result.stderr === 'string' && result.stderr) ||
    (typeof r.stderr === 'string' && r.stderr) ||
    '';
  const exitCode =
    typeof result.exit_code === 'number'
      ? result.exit_code
      : typeof r.exit_code === 'number'
        ? r.exit_code
        : null;
  const timedOut = result.timed_out === true || r.timed_out === true;
  const stdoutTruncated = result.stdout_truncated === true;
  const stderrTruncated = result.stderr_truncated === true;
  const exitKind: StateKind = exitCode === 0 ? 'success' : 'failed';

  return (
    <div className="flex flex-col gap-4">
      <ZoneLabel>Response</ZoneLabel>
      {stdout.trim() ? (
        <div>
          <SectionHeading>stdout</SectionHeading>
          <pre className="font-mono whitespace-pre-wrap [overflow-wrap:anywhere] rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[12px] text-foreground">
            {stdout}
            {stdoutTruncated && '\n…(truncated)'}
          </pre>
        </div>
      ) : (
        <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[13px] text-muted-foreground">
          No stdout.
        </div>
      )}
      {stderr.trim() && (
        <div>
          <SectionHeading>stderr</SectionHeading>
          <pre className="font-mono whitespace-pre-wrap [overflow-wrap:anywhere] rounded-md border border-border border-l-2 border-l-destructive/60 bg-muted/40 px-3 py-2.5 text-[12px] text-foreground">
            {stderr}
            {stderrTruncated && '\n…(truncated)'}
          </pre>
        </div>
      )}
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-[11px] text-muted-foreground">
        {exitCode != null && (
          <StatePill kind={exitKind} label={`exit ${exitCode}`} />
        )}
        {timedOut && <StatePill kind="failed" label="timed out" />}
      </div>
    </div>
  );
}

// ---------- repository_write ----------

export function RepositoryWriteWidget({ raw }: WidgetProps) {
  const r = unwrapResult(raw);
  const rows = [
    ['Repository', pickString(r.repository)],
    ['Project', pickString(r.project_id)],
    ['Path', pickString(r.added_path)],
    ['URL', pickString(r.url)],
  ].filter((row): row is [string, string] => Boolean(row[1]));
  const repositories = asArray<Record<string, any>>(r.repositories);

  return (
    <div className="flex flex-col gap-4">
      <ZoneLabel>Response</ZoneLabel>
      <div className="rounded-md border border-border bg-muted/40 p-3 border-l-2 border-l-emerald-500/60">
        <div className="flex flex-wrap items-center gap-2">
          <CheckCircle2 size={14} className="shrink-0 text-emerald-600" aria-hidden />
          <h3 className="min-w-0 flex-1 text-[13px] font-medium text-foreground [overflow-wrap:anywhere]">
            GitHub repository updated
          </h3>
          <StatePill kind="success" label="completed" />
        </div>
        {rows.length > 0 && (
          <div className="mt-3">
            <MetaGrid rows={rows.map(([label, value]) => ({ label, value, mono: true }))} />
          </div>
        )}
      </div>

      {repositories.length > 0 && (
        <div>
          <SectionHeading>Repositories</SectionHeading>
          <div className="space-y-2">
            {repositories.map((repo, index) => {
              const path = pickString(repo.path, repo.repo_path) || `Repository ${index + 1}`;
              const status = pickString(repo.status) || 'updated';
              const detail = pickString(repo.detail);
              return (
                <div key={`${path}-${index}`} className="rounded-md border border-border bg-muted/40 px-3 py-2.5">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="min-w-0 flex-1 font-mono text-[12px] text-foreground [overflow-wrap:anywhere]">
                      {path}
                    </span>
                    <Chip>{status}</Chip>
                  </div>
                  {detail && (
                    <p className="mt-1.5 text-[12px] text-muted-foreground whitespace-pre-wrap [overflow-wrap:anywhere]">
                      {detail}
                    </p>
                  )}
                </div>
              );
            })}
          </div>
        </div>
      )}
    </div>
  );
}

// ---------- generic fallback ----------

const GENERIC_PREFERRED = [
  'id',
  'number',
  'name',
  'full_name',
  'login',
  'state',
  'html_url',
  'created_at',
  'updated_at',
];

export function GenericGitHubWidget({ raw }: WidgetProps) {
  const r = unwrapResult(raw);
  const entries = Object.entries(r).filter(([k, v]) => {
    if (k.startsWith('_')) return false;
    return v != null && (typeof v !== 'object' || Array.isArray(v));
  });
  const preferred = GENERIC_PREFERRED.filter((k) => k in r).map((k) => [k, r[k]] as const);
  const rest = entries
    .filter(([k]) => !GENERIC_PREFERRED.includes(k))
    .slice(0, Math.max(0, 6 - preferred.length));
  const rows = [...preferred, ...rest];

  return (
    <div className="flex flex-col gap-4">
      <ZoneLabel>Response</ZoneLabel>
      {rows.length > 0 ? (
        <MetaGrid
          rows={rows.map(([k, v]) => ({
            label: k,
            value: formatGenericValue(k, v),
            mono: /id|sha|ref|number|html_url/.test(k),
          }))}
        />
      ) : (
        <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[13px] text-muted-foreground">
          No top-level fields to summarise. Expand Details for the full payload.
        </div>
      )}
    </div>
  );
}

function formatGenericValue(key: string, value: any): React.ReactNode {
  if (Array.isArray(value)) return `${value.length} items`;
  if (key.endsWith('_at')) return <RelativeTime value={value} />;
  if (key === 'state') return <StatePill kind={classifyState(value)} />;
  if (key === 'html_url' && typeof value === 'string') {
    return (
      <a
        href={value}
        target="_blank"
        rel="noopener noreferrer"
        className="text-foreground underline decoration-muted-foreground/40 underline-offset-2 hover:text-foreground"
      >
        {value}
      </a>
    );
  }
  return String(value);
}

// ---------- dispatch ----------

export type WidgetKey =
  | 'status'
  | 'connection_status'
  | 'repository_list'
  | 'pull_request_list'
  | 'pull_request'
  | 'issue_list'
  | 'issue'
  | 'workflow_run_list'
  | 'workflow_run_jobs'
  | 'approval'
  | 'gh_command'
  | 'repository_write'
  | 'generic';

const TOOL_NAME_TO_WIDGET: Array<[RegExp, WidgetKey]> = [
  [/^github_status$/, 'status'],
  [/^github_check_connection$/, 'connection_status'],
  [/^github_list_repositories$/, 'repository_list'],
  [/^github_list_pull_requests$/, 'pull_request_list'],
  [/^github_get_pull_request$/, 'pull_request'],
  [/^github_list_issues$/, 'issue_list'],
  [/^github_get_issue$/, 'issue'],
  [/^github_list_workflow_runs$/, 'workflow_run_list'],
  [/^github_get_workflow_run_jobs$/, 'workflow_run_jobs'],
  [/^github_run_gh_command$/, 'gh_command'],
  [/^github_(clone_repository|pull_repository|refresh_repository)$/, 'approval'],
];

export function resolveWidgetKey(
  meta: Record<string, any>,
): WidgetKey {
  const widget =
    (typeof meta?.widget === 'string' && meta.widget) ||
    (isObject(meta?.result_metadata) && typeof meta.result_metadata.widget === 'string'
      ? meta.result_metadata.widget
      : null);
  if (widget) {
    switch (widget) {
      case 'status':
      case 'connection_status':
      case 'repository_list':
      case 'pull_request_list':
      case 'pull_request':
      case 'issue_list':
      case 'issue':
      case 'workflow_run_list':
      case 'workflow_run_jobs':
      case 'approval':
      case 'gh_command':
      case 'repository_write':
        return widget;
      default:
        break;
    }
  }
  const toolName = String(meta?.tool_name || '');
  for (const [re, key] of TOOL_NAME_TO_WIDGET) {
    if (re.test(toolName)) return key;
  }
  return 'generic';
}

export function WidgetForKey({
  widgetKey,
  raw,
  meta,
}: {
  widgetKey: WidgetKey;
  raw: unknown;
  meta: Record<string, any>;
}) {
  switch (widgetKey) {
    case 'status':
      return <StatusWidget raw={raw} meta={meta} />;
    case 'connection_status':
      return <ConnectionStatusWidget raw={raw} meta={meta} />;
    case 'repository_list':
      return <RepositoryListWidget raw={raw} meta={meta} />;
    case 'pull_request_list':
      return <PullRequestListWidget raw={raw} meta={meta} />;
    case 'pull_request':
      return <PullRequestWidget raw={raw} meta={meta} />;
    case 'issue_list':
      return <IssueListWidget raw={raw} meta={meta} />;
    case 'issue':
      return <IssueWidget raw={raw} meta={meta} />;
    case 'workflow_run_list':
      return <WorkflowRunListWidget raw={raw} meta={meta} />;
    case 'workflow_run_jobs':
      return <WorkflowRunJobsWidget raw={raw} meta={meta} />;
    case 'approval':
      return <ApprovalWidget raw={raw} meta={meta} />;
    case 'gh_command':
      return <GhCommandWidget raw={raw} meta={meta} />;
    case 'repository_write':
      return <RepositoryWriteWidget raw={raw} meta={meta} />;
    default:
      return <GenericGitHubWidget raw={raw} meta={meta} />;
  }
}

// Static import keeps icons tree-shakeable; CheckCircle2/XCircle reserved
// for future use (success/failure indicators on detail headers).
export const _ICONS = { CheckCircle2, XCircle };
