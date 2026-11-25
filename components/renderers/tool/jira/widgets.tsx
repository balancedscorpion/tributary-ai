import React, { useEffect, useRef, useState } from 'react';
import {
  ArrowRight,
  Check,
  Copy,
  FileText,
  Image as ImageIcon,
  Paperclip,
  Search,
  ShieldAlert,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import MarkdownRenderer from '@/components/chat/MarkdownRenderer';
import {
  Chip,
  ExternalLinkIcon,
  MetaGrid,
  RelativeTime,
  SectionHeading,
  StatePill,
  StatusDot,
  ZoneLabel,
  humanize,
  type StateKind,
} from '../github/atoms';
import { ValueBlock } from '../inspector/values';
import {
  initials,
  issueTypeMeta,
  normalizeJiraIssue,
  priorityMeta,
  statusTone,
  type JiraIssueData,
} from './IssueCard';
import JiraIssueInspectorResponse, {
  ChangelogList,
  normalizeChangelog,
} from './JiraIssueResponse';
import {
  formatBytes,
  jiraWriteHeadline,
  resolveJiraWidgetKey,
  urlPath,
  type JiraWidgetKey,
} from './summary';

// Widget-key resolution and the write headline live in the pure module
// (./summary) so the collapsed-card summary path shares them without pulling
// React; re-export here so widget consumers keep one import surface.
export { resolveJiraWidgetKey } from './summary';
export type { JiraWidgetKey } from './summary';

/**
 * Jira widget family — the rendered Response-zone views for every Jira tool
 * payload the Atlassian bridge ships (server/internal/atlassian/bridge.mjs +
 * server/internal/chat/tools/atlassian_tools.go). Mirrors the GitHub family
 * (tool/github/widgets.tsx): a WidgetKey union, a resolver from
 * result_metadata.widget / tool_name, and one dispatch component shared by the
 * inspector response-widget registry AND the chat inline preview registry, so
 * the rendered view is the default everywhere and raw JSON stays one tab /
 * disclosure away.
 */

// ---------- helpers ----------

function isObject(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function asArray<T = any>(value: unknown): T[] {
  return Array.isArray(value) ? (value as T[]) : [];
}

function str(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function num(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null;
}

function safeUrl(value: unknown): string | null {
  const url = str(value);
  return /^https?:\/\//i.test(url) ? url : null;
}

function personName(value: unknown): string {
  if (typeof value === 'string') return value.trim();
  if (isObject(value)) {
    return str(value.displayName) || str(value.name) || str(value.emailAddress);
  }
  return '';
}

function connectionStateKind(product: Record<string, any>): StateKind {
  const state = str(product.state).toLowerCase();
  if (state === 'connected' || product.connected === true) return 'success';
  if (state === 'expired' || state === 'reauth_required' || state === 'pending') return 'pending';
  if (state === 'not_connected' || state === 'error' || product.connected === false) return 'failed';
  return 'unknown';
}

function httpStatusKind(status: unknown): StateKind {
  const s = num(status);
  if (s === null) return 'unknown';
  if (s >= 200 && s < 300) return 'success';
  if (s >= 400) return 'failed';
  return 'unknown';
}

// ---------- shared Jira atoms ----------

/** Initials avatar — Jira users have no avatar URLs in the bridge summaries. */
function PersonAvatar({ name, size = 24 }: { name: string; size?: 20 | 24 }) {
  const px = `${size}px`;
  return (
    <span
      style={{ width: px, height: px }}
      className="inline-flex shrink-0 items-center justify-center rounded-full bg-blue-500/10 text-[10px] font-medium text-blue-700 dark:text-blue-400"
      aria-hidden
    >
      {initials(name || '?')}
    </span>
  );
}

function IssueKeyLink({ issueKey, url }: { issueKey: string; url?: string | null }) {
  if (!issueKey) return null;
  const href = safeUrl(url);
  if (!href) {
    return <span className="font-mono text-[12px] text-muted-foreground">{issueKey}</span>;
  }
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      className="rounded font-mono text-[12px] text-muted-foreground hover:text-foreground hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      {issueKey}
    </a>
  );
}

/** One-line issue identity strip (key · summary · status) — the compact
 *  header for list/history/attachment widgets where the full JiraIssueCard
 *  would drown the actual payload. */
function IssueStrip({ issue }: { issue: JiraIssueData }) {
  const type = issueTypeMeta(issue.issueType);
  return (
    <div className="flex min-w-0 items-center gap-2">
      <span
        className={cn(
          'flex h-[22px] w-[22px] shrink-0 items-center justify-center rounded-md',
          type.classes,
        )}
        title={issue.issueType || 'Issue'}
      >
        <type.Icon size={13} aria-hidden />
        <span className="sr-only">{issue.issueType || 'Issue'}</span>
      </span>
      <IssueKeyLink issueKey={issue.key} url={issue.url} />
      <span className="min-w-0 flex-1 truncate text-[13px] text-foreground">{issue.summary}</span>
      {issue.status && (
        <span
          className={cn(
            'shrink-0 rounded-full px-2 py-[2px] text-[11px] font-medium',
            statusTone(issue.status),
          )}
        >
          {issue.status}
        </span>
      )}
    </div>
  );
}

/** Two-line dense issue row shared by issue_list and find. */
function IssueRow({ issue }: { issue: Record<string, any> }) {
  const norm = normalizeJiraIssue(issue);
  if (!norm) return null;
  const type = issueTypeMeta(norm.issueType);
  const priority = priorityMeta(norm.priority);
  const labels = norm.labels.slice(0, 3);
  const extraLabels = norm.labels.length - labels.length;
  return (
    <li className="flex flex-col gap-1 border-b border-border px-3 py-2 last:border-b-0 hover:bg-muted/30">
      <div className="flex min-w-0 items-center gap-2">
        <span
          className={cn(
            'flex h-[18px] w-[18px] shrink-0 items-center justify-center rounded',
            type.classes,
          )}
          title={norm.issueType || 'Issue'}
        >
          <type.Icon size={11} aria-hidden />
          <span className="sr-only">{norm.issueType || 'Issue'}</span>
        </span>
        <IssueKeyLink issueKey={norm.key} url={norm.url} />
        <span className="min-w-0 flex-1 truncate text-[13px] text-foreground">{norm.summary}</span>
        {norm.status && (
          <span
            className={cn(
              'shrink-0 rounded-full px-2 py-[2px] text-[11px] font-medium',
              statusTone(norm.status),
            )}
          >
            {norm.status}
          </span>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 pl-[26px] text-[11px] text-muted-foreground">
        {priority && (
          <span className="inline-flex items-center gap-0.5">
            <priority.Icon size={11} aria-hidden />
            {norm.priority}
          </span>
        )}
        <span>{norm.assignee || 'Unassigned'}</span>
        {norm.updated && (
          <>
            <span className="text-muted-foreground/40">·</span>
            <RelativeTime value={norm.updated} prefix="updated" />
          </>
        )}
        {labels.map((label) => (
          <Chip key={label}>{label}</Chip>
        ))}
        {extraLabels > 0 && <span className="text-muted-foreground/60">+{extraLabels}</span>}
      </div>
    </li>
  );
}

function IssueRows({ issues }: { issues: Array<Record<string, any>> }) {
  return (
    <ul className="flex flex-col overflow-hidden rounded-md border border-border">
      {issues.map((issue, i) => (
        <IssueRow key={str(issue.key) || str(issue.id) || i} issue={issue} />
      ))}
    </ul>
  );
}

function EmptyNote({ children }: { children: React.ReactNode }) {
  return (
    <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[13px] text-muted-foreground">
      {children}
    </div>
  );
}

function CollapsibleText({ text, threshold = 400 }: { text: string; threshold?: number }) {
  const [expanded, setExpanded] = useState(false);
  const regionId = React.useId();
  const long = text.length > threshold;
  const shown = expanded || !long ? text : `${text.slice(0, threshold).trimEnd()}…`;
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <p
        id={regionId}
        className="whitespace-pre-wrap text-[13px] leading-relaxed text-foreground [overflow-wrap:anywhere]"
      >
        {shown}
      </p>
      {long && (
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          aria-expanded={expanded}
          aria-controls={regionId}
          className="self-start rounded text-[11px] text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
        >
          {expanded ? 'Show less' : 'Show more'}
        </button>
      )}
    </div>
  );
}

/** Account IDs are THE payload of jira_find_users (needed for mentions) —
 *  keep them whole and one click away from the clipboard. */
function CopyableId({ value }: { value: string }) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );
  return (
    <button
      type="button"
      onClick={() => {
        navigator.clipboard
          ?.writeText(value)
          .then(() => {
            setCopied(true);
            if (timer.current) clearTimeout(timer.current);
            timer.current = setTimeout(() => setCopied(false), 1500);
          })
          .catch(() => {
            // Clipboard unavailable (permissions / insecure context) — the ID
            // stays visible and selectable, so failing quietly is fine.
          });
      }}
      title={copied ? 'Copied' : `Copy ${value}`}
      aria-label={copied ? 'Copied' : `Copy account ID ${value}`}
      className="inline-flex max-w-[180px] items-center gap-1 rounded px-1 py-0.5 font-mono text-[11px] text-muted-foreground/70 hover:bg-muted/60 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <span className="min-w-0 truncate">{value}</span>
      {copied ? (
        <Check size={11} aria-hidden className="shrink-0 text-emerald-500" />
      ) : (
        <Copy size={11} aria-hidden className="shrink-0" />
      )}
      <span aria-live="polite" className="sr-only">
        {copied ? 'Copied' : ''}
      </span>
    </button>
  );
}

function CopyableCodeBlock({
  label,
  value,
  compact = false,
}: {
  label: string;
  value: string;
  compact?: boolean;
}) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );
  if (!value) return null;
  return (
    <div className="min-w-0 overflow-hidden rounded-md border border-border bg-background/70">
      <div className="flex min-w-0 items-center gap-2 border-b border-border bg-muted/30 px-2.5 py-1.5">
        <span className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground/70">
          {label}
        </span>
        <span className="flex-1" />
        <button
          type="button"
          onClick={() => {
            navigator.clipboard
              ?.writeText(value)
              .then(() => {
                setCopied(true);
                if (timer.current) clearTimeout(timer.current);
                timer.current = setTimeout(() => setCopied(false), 1500);
              })
              .catch(() => undefined);
          }}
          className="inline-flex h-6 items-center gap-1 rounded px-1.5 text-[11px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          aria-label={copied ? `${label} copied` : `Copy ${label}`}
          title={copied ? 'Copied' : `Copy ${label}`}
        >
          {copied ? <Check size={12} aria-hidden /> : <Copy size={12} aria-hidden />}
          {copied ? 'Copied' : 'Copy'}
        </button>
      </div>
      <pre
        className={cn(
          'chat-text-wrap whitespace-pre-wrap break-words px-2.5 py-2 font-mono text-[12px] leading-relaxed text-foreground',
          compact && 'max-h-24 overflow-y-auto text-[11px] leading-snug',
        )}
      >
        <code>{value}</code>
      </pre>
    </div>
  );
}

function formatCount(value: number | null): string {
  return value === null ? '—' : value.toLocaleString();
}

/** Pathname only; the JQL query string is rendered separately below. */
function jiraSearchPath(url: string): string {
  if (url) {
    try {
      return new URL(url).pathname;
    } catch {
      // not an absolute URL — fall through to the known endpoint
    }
  }
  return '/rest/api/3/search/jql';
}

function StatTile({
  label,
  value,
  detail,
}: {
  label: string;
  value: React.ReactNode;
  detail?: React.ReactNode;
}) {
  return (
    <div className="min-w-0 rounded-md border border-border bg-muted/25 px-3 py-2">
      <div className="text-[11px] uppercase tracking-wide text-muted-foreground/60">{label}</div>
      <div className="mt-0.5 truncate text-sm font-semibold text-foreground">{value}</div>
      {detail && <div className="mt-0.5 truncate text-[11px] text-muted-foreground">{detail}</div>}
    </div>
  );
}

export interface JiraWidgetProps {
  raw: unknown;
  meta: Record<string, any>;
}

// ---------- status / connection ----------

/** Shared product rows (Jira / Confluence sessions) for status + connection. */
function ProductRows({ products }: { products: Record<string, any> }) {
  const rows = Object.entries(products).filter(([, v]) => isObject(v)) as Array<
    [string, Record<string, any>]
  >;
  if (!rows.length) return null;
  return (
    <ul className="flex flex-col divide-y divide-border overflow-hidden rounded-md border border-border">
      {rows.map(([key, product]) => {
        const kind = connectionStateKind(product);
        const label = str(product.state) ? humanize(str(product.state)) : undefined;
        const email = str(product.profile?.emailAddress);
        const error = str(product.error);
        return (
          <li key={key} className="flex flex-col gap-0.5 px-3 py-2">
            <div className="flex min-w-0 items-center gap-2 text-[13px]">
              <StatusDot kind={kind} />
              <span className="font-medium text-foreground">{humanize(str(product.product) || key)}</span>
              {str(product.title) && (
                <span className="min-w-0 flex-1 truncate text-muted-foreground">
                  {str(product.title)}
                </span>
              )}
              {!str(product.title) && <span className="flex-1" />}
              <StatePill kind={kind} label={label} />
              <ExternalLinkIcon href={safeUrl(product.url)} label={`Open ${key} site`} />
            </div>
            <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 pl-[14px] text-[11px] text-muted-foreground">
              {email && <span>{email}</span>}
              {email && str(product.checkedAt) && <span className="text-muted-foreground/40">·</span>}
              {str(product.checkedAt) && <RelativeTime value={product.checkedAt} prefix="checked" />}
            </div>
            {error && <p className="pl-[14px] text-[11px] text-red-400">{error}</p>}
          </li>
        );
      })}
    </ul>
  );
}

export function AtlassianStatusWidget({ raw }: JiraWidgetProps) {
  const r = isObject(raw) ? raw : {};
  const running = r.running === true;
  const products = isObject(r.products) ? r.products : {};
  const autoLogin = isObject(r.autoLogin) ? r.autoLogin : {};
  const browser = isObject(r.browser) ? r.browser : {};
  const browserLabel = [str(browser.name), str(browser.provider)].filter(Boolean).join(' · ');
  const autoLoginState = str(autoLogin.state);
  const autoLoginBad = autoLoginState === 'error' || autoLoginState === 'expired';
  if (!running && !Object.keys(products).length) {
    return <EmptyNote>Bridge not running. No product sessions to report.</EmptyNote>;
  }
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-col gap-1 rounded-md border border-border bg-muted/40 p-3">
        <div className="flex flex-wrap items-center gap-2 text-[13px] font-medium text-foreground">
          <span
            className={cn(
              'inline-block h-1.5 w-1.5 shrink-0 rounded-full',
              running ? 'animate-pulse bg-blue-400' : 'bg-muted-foreground/50',
            )}
            aria-hidden
          />
          {running ? 'Bridge running' : 'Bridge stopped'}
          <span className="flex-1" />
          {browserLabel && <Chip mono>{browserLabel}</Chip>}
          {str(r.mode) && <Chip>{str(r.mode)}</Chip>}
        </div>
        {str(autoLogin.message) && (
          <p className={cn('text-[11px]', autoLoginBad ? 'text-red-400' : 'text-muted-foreground')}>
            {str(autoLogin.message)}
          </p>
        )}
      </div>
      <ProductRows products={products} />
    </div>
  );
}

export function AtlassianConnectionWidget({ raw }: JiraWidgetProps) {
  const r = isObject(raw) ? raw : {};
  const ok = r.ok === true;
  const products = isObject(r.products) ? r.products : {};
  if (r.checked !== true && !Object.keys(products).length) {
    return <EmptyNote>No connection check has run.</EmptyNote>;
  }
  return (
    <div className="flex flex-col gap-3">
      <div
        role="status"
        aria-live="polite"
        className={cn(
          'flex flex-col gap-0.5 rounded-md border border-border border-l-2 bg-muted/40 p-3',
          ok ? 'border-l-emerald-500/60' : 'border-l-red-500/60',
        )}
      >
        <div className="flex items-center gap-2 text-[13px] font-medium text-foreground">
          <StatusDot kind={ok ? 'success' : 'failed'} />
          {ok ? 'Connected' : 'Connection failed'}
        </div>
        {str(r.message) && (
          <p className={cn('text-[11px]', ok ? 'text-muted-foreground' : 'text-red-400')}>
            {str(r.message)}
          </p>
        )}
      </div>
      <ProductRows products={products} />
    </div>
  );
}

// ---------- issue ----------

export function IssueWidget({ raw, meta }: JiraWidgetProps) {
  // The dedicated jira_get_issue inspector response (issue card + recent
  // changes) is the single source of truth for the issue detail view.
  return <JiraIssueInspectorResponse result={raw} meta={meta} />;
}

// ---------- history ----------

interface SprintTransition {
  changedAt: string;
  from: string;
  to: string;
}

function normalizeTransitions(value: unknown): SprintTransition[] {
  return asArray(value)
    .filter(isObject)
    .map((t) => ({
      changedAt: str(t.changedAt),
      from: str(t.fromSprintName) || (t.fromSprintId != null ? `Sprint ${t.fromSprintId}` : ''),
      to: str(t.toSprintName) || (t.toSprintId != null ? `Sprint ${t.toSprintId}` : ''),
    }))
    .filter((t) => t.from || t.to)
    .map((t) => {
      const at = new Date(t.changedAt).getTime();
      // Undated transitions sort last so the comparator stays consistent.
      return { ...t, sortKey: Number.isFinite(at) ? at : -Infinity };
    })
    .sort((a, b) => b.sortKey - a.sortKey); // newest first
}

export function IssueHistoryWidget({ raw }: JiraWidgetProps) {
  const r = isObject(raw) ? raw : {};
  const issue = normalizeJiraIssue(r.issue);
  const transitions = normalizeTransitions(r.sprintTransitions);
  const current = isObject(r.currentSprint) ? str(r.currentSprint.name) : '';
  const previous = isObject(r.previousSprint) ? str(r.previousSprint.name) : '';
  const changelog = normalizeChangelog(r.changelog);
  return (
    <div className="flex flex-col gap-4">
      {issue && <IssueStrip issue={issue} />}
      <MetaGrid
        rows={[
          current ? { label: 'Current sprint', value: <Chip>{current}</Chip> } : null,
          previous ? { label: 'Previous sprint', value: <Chip>{previous}</Chip> } : null,
        ]}
      />
      {transitions.length > 0 ? (
        <div>
          <SectionHeading>Sprint transitions</SectionHeading>
          <ol className="ml-1.5 flex flex-col border-l border-border pl-4">
            {transitions.map((t, i) => (
              <li key={`${t.changedAt}-${i}`} className="relative pb-3 last:pb-0">
                <span
                  className={cn(
                    'absolute -left-[21px] top-1.5 h-2 w-2 rounded-full',
                    i === 0 ? 'bg-blue-400' : 'bg-muted-foreground/40',
                  )}
                  aria-hidden
                />
                <div className="flex flex-wrap items-center gap-1.5 text-[13px] text-foreground">
                  <span>{t.from || 'Backlog'}</span>
                  <ArrowRight size={11} aria-hidden className="shrink-0 text-muted-foreground/60" />
                  <span className="sr-only">to</span>
                  <span>{t.to || 'Backlog'}</span>
                </div>
                {t.changedAt && (
                  <RelativeTime value={t.changedAt} className="text-[11px] text-muted-foreground" />
                )}
              </li>
            ))}
          </ol>
        </div>
      ) : (
        <EmptyNote>
          No sprint transitions recorded{issue?.key ? ` for ${issue.key}` : ''}.
        </EmptyNote>
      )}
      {changelog.length > 0 && <ChangelogList entries={changelog} />}
    </div>
  );
}

// ---------- comment_list ----------

export function CommentListWidget({ raw }: JiraWidgetProps) {
  const r = isObject(raw) ? raw : {};
  const comments = asArray(r.comments).filter(isObject);
  const issueKey = str(r.issueKey);
  const total = num(r.total);
  const orderBy = str(r.orderBy);
  if (!comments.length) {
    return <EmptyNote>No comments{issueKey ? ` on ${issueKey}` : ''} yet.</EmptyNote>;
  }
  const countLabel =
    total !== null && total > comments.length
      ? `${comments.length} of ${total} comments`
      : `${comments.length} ${comments.length === 1 ? 'comment' : 'comments'}`;
  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <ZoneLabel>
          {countLabel}
          {issueKey ? ` · ${issueKey}` : ''}
        </ZoneLabel>
        {orderBy && <Chip mono>{orderBy}</Chip>}
      </div>
      <ul className="flex flex-col divide-y divide-border overflow-hidden rounded-md border border-border">
        {comments.map((comment, i) => {
          const author = personName(comment.author);
          const email = isObject(comment.author) ? str(comment.author.emailAddress) : '';
          const created = str(comment.created);
          const updated = str(comment.updated);
          const edited = Boolean(created && updated && updated !== created);
          const body = str(comment.bodyText) || str(comment.renderedText);
          const parentId = str(comment.parentCommentId);
          const parentUrl = safeUrl(comment.parentUrl);
          return (
            <li key={str(comment.id) || i} className="flex gap-2.5 px-3 py-2.5">
              <PersonAvatar name={author || '?'} />
              <div className="flex min-w-0 flex-1 flex-col gap-1">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5">
                  <span
                    className="text-[13px] font-medium text-foreground"
                    title={email || undefined}
                  >
                    {author || 'Unknown author'}
                  </span>
                  {created && (
                    <RelativeTime value={created} className="text-[11px] text-muted-foreground" />
                  )}
                  {edited && <span className="text-[11px] text-muted-foreground/60">· edited</span>}
                  <ExternalLinkIcon href={safeUrl(comment.url)} label="Open comment in Jira" />
                </div>
                {parentId &&
                  (parentUrl ? (
                    <a
                      href={parentUrl}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="self-start rounded-md border border-border bg-transparent px-1.5 py-0.5 text-[11px] leading-none text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      <span aria-hidden>↩ </span>reply to #{parentId}
                    </a>
                  ) : (
                    <Chip outline className="self-start">
                      <span aria-hidden>↩ </span>reply to #{parentId}
                    </Chip>
                  ))}
                {body ? (
                  <CollapsibleText text={body} />
                ) : (
                  <p className="text-[12px] text-muted-foreground/60">No comment text available.</p>
                )}
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

// ---------- attachment_list ----------

function mimeShort(mimeType: string): string {
  const part = mimeType.split('/').pop() || mimeType;
  return part.length > 12 ? mimeType.split('/')[0] || part : part;
}

function AttachmentFileCell({ attachment }: { attachment: Record<string, any> }) {
  const fileName = str(attachment.fileName) || 'attachment';
  const thumb = safeUrl(attachment.thumbnailUrl);
  const isImage = str(attachment.mimeType).startsWith('image/');
  return (
    <span className="inline-flex min-w-0 items-center gap-1.5">
      {thumb ? (
        <img
          src={thumb}
          alt=""
          loading="lazy"
          referrerPolicy="no-referrer"
          className="h-4 w-4 shrink-0 rounded-sm border border-border object-cover"
        />
      ) : isImage ? (
        <ImageIcon size={13} aria-hidden className="shrink-0 text-muted-foreground/60" />
      ) : (
        <Paperclip size={13} aria-hidden className="shrink-0 text-muted-foreground/60" />
      )}
      <span className="min-w-0 truncate">{fileName}</span>
      <ExternalLinkIcon href={safeUrl(attachment.contentUrl)} label={`Open ${fileName}`} />
    </span>
  );
}

export function AttachmentListWidget({ raw }: JiraWidgetProps) {
  const r = isObject(raw) ? raw : {};
  const attachments = asArray(r.attachments).filter(isObject);
  const issue = normalizeJiraIssue(r.issue);
  const issueKey = issue?.key || str(r.issueKey);
  if (!attachments.length) {
    return <EmptyNote>No attachments{issueKey ? ` on ${issueKey}` : ''}.</EmptyNote>;
  }
  return (
    <div className="flex flex-col gap-2">
      {issue && <IssueStrip issue={issue} />}
      <ZoneLabel>
        {attachments.length} {attachments.length === 1 ? 'attachment' : 'attachments'}
      </ZoneLabel>
      <div className="overflow-x-auto rounded-md border border-border">
        <table className="min-w-full border-collapse text-[13px]">
          <thead>
            <tr className="border-b border-border bg-muted/40 text-left text-[11px] uppercase tracking-wide text-muted-foreground/60">
              <th scope="col" className="px-3 py-2 font-medium">File</th>
              <th scope="col" className="px-3 py-2 font-medium">Type</th>
              <th scope="col" className="px-3 py-2 text-right font-medium">Size</th>
              <th scope="col" className="px-3 py-2 font-medium">Author</th>
              <th scope="col" className="px-3 py-2 font-medium">Added</th>
            </tr>
          </thead>
          <tbody>
            {attachments.map((attachment, i) => (
              <tr
                key={str(attachment.id) || i}
                className="border-b border-border last:border-b-0 hover:bg-muted/30"
              >
                <td className="max-w-[260px] px-3 py-2 font-mono text-[12px] text-foreground">
                  <AttachmentFileCell attachment={attachment} />
                </td>
                <td className="px-3 py-2">
                  {str(attachment.mimeType) ? (
                    <Chip mono>{mimeShort(str(attachment.mimeType))}</Chip>
                  ) : (
                    <span className="text-muted-foreground/60">—</span>
                  )}
                </td>
                <td className="px-3 py-2 text-right font-mono text-[12px] tabular-nums text-muted-foreground">
                  {formatBytes(attachment.sizeBytes) || '—'}
                </td>
                <td className="px-3 py-2 text-muted-foreground">
                  {personName(attachment.author) || '—'}
                </td>
                <td className="px-3 py-2 text-muted-foreground">
                  {str(attachment.created) ? <RelativeTime value={attachment.created} /> : '—'}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ---------- attachment (read) ----------

export function AttachmentWidget({ raw }: JiraWidgetProps) {
  const r = isObject(raw) ? raw : {};
  const summary = isObject(r.attachment) ? r.attachment : {};
  const fileName = str(r.fileName) || str(summary.fileName) || 'attachment';
  const mimeType = str(r.mimeType) || str(summary.mimeType);
  const sizeBytes = num(r.sizeBytes) ?? num(summary.sizeBytes);
  const bytesReturned = num(r.bytesReturned);
  const truncated = r.truncated === true;
  const content = str(r.content);
  const note = str(r.note);
  const contentBase64 = str(r.contentBase64);
  const isImage = mimeType.startsWith('image/');
  const download = isObject(r.download) ? r.download : {};
  const localPath =
    str(r.localPath) || str(r.local_path) || str(download.local_path) || str(download.localPath);
  const sha = str(r.sha256) || str(download.sha256);
  const issueKey = str(r.issueKey);

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted/40 p-3">
        <span className="flex h-[26px] w-[26px] shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground">
          {isImage ? <ImageIcon size={14} aria-hidden /> : <FileText size={14} aria-hidden />}
        </span>
        <span className="min-w-0 flex-1 truncate font-mono text-[13px] text-foreground" title={fileName}>
          {fileName}
        </span>
        {mimeType && <Chip mono>{mimeType}</Chip>}
        {formatBytes(sizeBytes) && (
          <span className="text-[11px] text-muted-foreground">{formatBytes(sizeBytes)}</span>
        )}
        {truncated && (
          <StatePill
            kind="pending"
            label={
              bytesReturned !== null && sizeBytes !== null
                ? `truncated · ${formatBytes(bytesReturned)} of ${formatBytes(sizeBytes)}`
                : 'truncated'
            }
          />
        )}
        <ExternalLinkIcon href={safeUrl(summary.contentUrl) || safeUrl(r.url)} label={`Open ${fileName} in Jira`} />
      </div>

      {content ? (
        <pre
          tabIndex={0}
          role="region"
          aria-label={`${fileName} content`}
          className="max-h-[320px] overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted/40 px-3 py-2.5 font-mono text-[12px] text-foreground [overflow-wrap:anywhere] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {content}
        </pre>
      ) : isImage && contentBase64 ? (
        <img
          src={`data:${mimeType};base64,${contentBase64}`}
          alt={fileName}
          className="max-h-[320px] w-auto max-w-full self-start rounded-md border border-border"
        />
      ) : note ? (
        // Binary / native-image outcomes are normal results, never error-toned.
        <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[13px] text-muted-foreground">
          {note}
        </div>
      ) : r.binary === true ? (
        <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[13px] text-muted-foreground">
          Binary attachment — content not shown. See the download details below.
        </div>
      ) : (
        <EmptyNote>Attachment returned no readable content.</EmptyNote>
      )}

      <MetaGrid
        rows={[
          issueKey ? { label: 'Issue', value: issueKey, mono: true } : null,
          localPath ? { label: 'Saved to', value: localPath, mono: true } : null,
          sha
            ? {
                label: 'SHA-256',
                value: (
                  <span className="font-mono text-[12px]" title={sha}>
                    {sha.slice(0, 12)}…
                  </span>
                ),
              }
            : null,
          str(r.downloadMethod)
            ? { label: 'Method', value: <Chip mono>{str(r.downloadMethod)}</Chip> }
            : null,
        ]}
      />
    </div>
  );
}

// ---------- issue_link_types ----------

export function IssueLinkTypesWidget({ raw }: JiraWidgetProps) {
  const r = isObject(raw) ? raw : {};
  const types = asArray(r.issueLinkTypes)
    .filter(isObject)
    .slice()
    .sort((a, b) => str(a.name).localeCompare(str(b.name)));
  if (!types.length) {
    return <EmptyNote>No issue link types defined.</EmptyNote>;
  }
  return (
    <div className="flex flex-col gap-2">
      <ZoneLabel>
        {types.length} link {types.length === 1 ? 'type' : 'types'}
      </ZoneLabel>
      <div className="overflow-x-auto rounded-md border border-border">
        <table className="min-w-full border-collapse text-[13px]">
          <thead>
            <tr className="border-b border-border bg-muted/40 text-left text-[11px] uppercase tracking-wide text-muted-foreground/60">
              <th scope="col" className="px-3 py-2 font-medium">Name</th>
              <th scope="col" className="px-3 py-2 font-medium">Outward</th>
              <th scope="col" className="px-3 py-2 font-medium">Inward</th>
            </tr>
          </thead>
          <tbody>
            {types.map((t, i) => (
              <tr
                key={str(t.id) || i}
                className="border-b border-border last:border-b-0 hover:bg-muted/30"
              >
                <td className="px-3 py-2 font-medium text-foreground">{str(t.name) || '—'}</td>
                <td className="px-3 py-2 text-muted-foreground">{str(t.outward) || '—'}</td>
                <td className="px-3 py-2 text-muted-foreground">{str(t.inward) || '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

// ---------- user_list ----------

export function UserListWidget({ raw }: JiraWidgetProps) {
  const r = isObject(raw) ? raw : {};
  const users = asArray(r.users).filter(isObject);
  const query = str(r.query);
  if (!users.length) {
    return <EmptyNote>No users match{query ? ` "${query}"` : ' the query'}.</EmptyNote>;
  }
  return (
    <div className="flex flex-col gap-2">
      <ZoneLabel>
        {users.length} {users.length === 1 ? 'user' : 'users'}
        {query ? ` for "${query}"` : ''}
      </ZoneLabel>
      <ul className="flex flex-col divide-y divide-border overflow-hidden rounded-md border border-border">
        {users.map((user, i) => {
          const name = str(user.displayName) || str(user.emailAddress) || 'Unknown user';
          const accountType = str(user.accountType);
          return (
            <li key={str(user.accountId) || i} className="flex items-center gap-2.5 px-3 py-2">
              <PersonAvatar name={name} />
              <div className="flex min-w-0 flex-1 flex-col">
                <span className="flex min-w-0 items-center gap-1.5 text-[13px] font-medium text-foreground">
                  <span className="min-w-0 truncate">{name}</span>
                  {user.active === false && <StatePill kind="closed" label="inactive" />}
                </span>
                {str(user.emailAddress) && (
                  <span className="truncate text-[11px] text-muted-foreground">
                    {str(user.emailAddress)}
                  </span>
                )}
              </div>
              {accountType && accountType !== 'atlassian' && (
                <Chip outline>{accountType}</Chip>
              )}
              {str(user.accountId) && <CopyableId value={str(user.accountId)} />}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

// ---------- issue_list ----------

function JiraSearchIssueRow({ issue }: { issue: Record<string, any> }) {
  const norm = normalizeJiraIssue(issue);
  if (!norm) return null;
  const type = issueTypeMeta(norm.issueType);
  const priority = priorityMeta(norm.priority);
  const parent = normalizeJiraIssue(issue.parent);
  const components = norm.components.slice(0, 2);
  const labels = norm.labels.slice(0, 3);
  const hiddenTags = Math.max(
    0,
    norm.components.length - components.length + norm.labels.length - labels.length,
  );
  return (
    <li className="grid gap-2 border-b border-border px-3 py-2.5 last:border-b-0 hover:bg-muted/25 sm:grid-cols-[minmax(0,1fr)_auto]">
      <div className="min-w-0">
        <div className="flex min-w-0 items-center gap-2">
          <span
            className={cn(
              'flex h-[22px] w-[22px] shrink-0 items-center justify-center rounded-md',
              type.classes,
            )}
            title={norm.issueType || 'Issue'}
          >
            <type.Icon size={13} aria-hidden />
            <span className="sr-only">{norm.issueType || 'Issue'}</span>
          </span>
          <IssueKeyLink issueKey={norm.key} url={norm.url} />
          <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-foreground">
            {norm.summary}
          </span>
        </div>
        <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 pl-[30px] text-[11px] text-muted-foreground">
          {norm.issueType && <span>{norm.issueType}</span>}
          {priority && (
            <>
              {norm.issueType && <span className="text-muted-foreground/40">·</span>}
              <span className="inline-flex items-center gap-0.5">
                <priority.Icon size={11} aria-hidden />
                {norm.priority}
              </span>
            </>
          )}
          <span className="text-muted-foreground/40">·</span>
          <span>{norm.assignee || 'Unassigned'}</span>
          {norm.updated && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <RelativeTime value={norm.updated} prefix="updated" />
            </>
          )}
          {parent?.key && (
            <>
              <span className="text-muted-foreground/40">·</span>
              <span>
                parent <span className="font-mono">{parent.key}</span>
              </span>
            </>
          )}
        </div>
        {(components.length > 0 || labels.length > 0 || hiddenTags > 0) && (
          <div className="mt-1.5 flex flex-wrap items-center gap-1 pl-[30px]">
            {components.map((component) => (
              <Chip key={`component-${component}`}>{component}</Chip>
            ))}
            {labels.map((label) => (
              <Chip key={`label-${label}`} outline>{label}</Chip>
            ))}
            {hiddenTags > 0 && (
              <span className="text-[11px] text-muted-foreground/60">+{hiddenTags}</span>
            )}
          </div>
        )}
      </div>
      <div className="flex items-start justify-between gap-2 sm:min-w-[96px] sm:justify-end">
        {norm.status && (
          <span
            className={cn(
              'rounded-full px-2 py-[2px] text-[11px] font-medium',
              statusTone(norm.status),
            )}
          >
            {norm.status}
          </span>
        )}
        {norm.commentsCount !== null && (
          <span className="rounded-md bg-muted px-1.5 py-0.5 text-[11px] text-muted-foreground">
            {norm.commentsCount} {norm.commentsCount === 1 ? 'comment' : 'comments'}
          </span>
        )}
      </div>
    </li>
  );
}

function JiraSearchIssueRows({ issues }: { issues: Array<Record<string, any>> }) {
  return (
    <ul className="flex flex-col overflow-hidden rounded-md border border-border">
      {issues.map((issue, i) => (
        <JiraSearchIssueRow key={str(issue.key) || str(issue.id) || i} issue={issue} />
      ))}
    </ul>
  );
}

export function IssueListWidget({ raw, meta }: JiraWidgetProps) {
  const r = isObject(raw) ? raw : {};
  const issues = asArray(r.issues).filter(isObject);
  const jql = str(r.jql);
  const total = num(r.total);
  const startAt = num(r.startAt) ?? 0;
  const maxResults = num(r.maxResults);
  const status = num(r.status);
  const url = str(r.url);
  const path = jiraSearchPath(url);
  const errors = asArray(r.errors).map((err) => str(err)).filter(Boolean);
  const inline = meta.inline === true;
  const returned = issues.length;
  const countLabel =
    total !== null && total > returned
      ? `${returned.toLocaleString()} of ${total.toLocaleString()} issues`
      : `${returned.toLocaleString()} ${returned === 1 ? 'issue' : 'issues'}`;
  const pageStart = returned > 0 ? startAt + 1 : 0;
  const pageEnd = returned > 0 ? startAt + returned : 0;
  const statusKind = httpStatusKind(status);
  const pageLabel =
    returned > 0
      ? `${pageStart.toLocaleString()}-${pageEnd.toLocaleString()}`
      : startAt > 0
        ? `from ${startAt.toLocaleString()}`
        : '—';

  if (inline) {
    const previewIssues = issues.slice(0, 3);
    return (
      <div className="min-w-0 space-y-2">
        <div className="flex min-w-0 flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
          <StatePill kind={statusKind} label={status !== null ? `HTTP ${status}` : 'Jira search'} />
          <span>{countLabel}</span>
          {maxResults !== null && <span>· page size {maxResults}</span>}
        </div>
        <CopyableCodeBlock label="JQL" value={jql} compact />
        {errors.length > 0 && (
          <div className="rounded-md border border-destructive/20 bg-destructive/5 px-2 py-1.5 text-[11px] text-red-400">
            {errors.join(' · ')}
          </div>
        )}
        {previewIssues.length > 0 ? (
          <IssueRows issues={previewIssues} />
        ) : (
          <EmptyNote>No issues match this JQL.</EmptyNote>
        )}
        {returned > previewIssues.length && (
          <p className="text-[10px] text-muted-foreground/60">
            {returned - previewIssues.length} more in this page — open full audit for the complete rendered response.
          </p>
        )}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="rounded-md border border-border bg-muted/20 p-3">
        <div className="flex flex-wrap items-center gap-2">
          <span className="flex h-8 w-8 items-center justify-center rounded-md bg-blue-500/10 text-blue-700 dark:text-blue-400">
            <Search size={15} aria-hidden />
          </span>
          <div className="min-w-0 flex-1">
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <span className="text-sm font-semibold text-foreground">Jira JQL search</span>
              <StatePill kind={statusKind} label={status !== null ? `HTTP ${status}` : 'Jira'} />
              <ExternalLinkIcon href={safeUrl(url)} label="Open Jira search REST response" />
            </div>
            <p className="mt-0.5 font-mono text-[11px] text-muted-foreground [overflow-wrap:anywhere]">
              GET {path}
            </p>
          </div>
        </div>
        <div className="mt-3 grid grid-cols-3 gap-2">
          <StatTile label="Returned" value={formatCount(returned)} />
          <StatTile label="Total" value={formatCount(total)} />
          <StatTile
            label="Window"
            value={pageLabel}
            detail={maxResults !== null ? `page size ${maxResults}` : undefined}
          />
        </div>
      </div>

      <div>
        <ZoneLabel>Query</ZoneLabel>
        <CopyableCodeBlock label="JQL" value={jql} />
      </div>

      {errors.length > 0 && (
        <div className="rounded-md border border-destructive/25 bg-destructive/5 p-3">
          <ZoneLabel>Response errors</ZoneLabel>
          <ul className="space-y-1 text-[13px] text-red-400">
            {errors.map((error, i) => (
              <li key={`${error}-${i}`} className="chat-text-wrap">{error}</li>
            ))}
          </ul>
        </div>
      )}

      <div>
        <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
          <ZoneLabel>{countLabel}</ZoneLabel>
          {/* Which slice of the total is the Window tile's job; only restate
              it here when the page is offset and the count alone is ambiguous. */}
          {returned > 0 && startAt > 0 && (
            <span className="text-[11px] text-muted-foreground/70">
              showing {pageStart.toLocaleString()}-{pageEnd.toLocaleString()}
            </span>
          )}
        </div>
        {issues.length > 0 ? (
          <JiraSearchIssueRows issues={issues} />
        ) : (
          <EmptyNote>No issues match this JQL.</EmptyNote>
        )}
      </div>

      <MetaGrid
        rows={[
          {
            label: 'REST URL',
            value: safeUrl(url) ? (
              <a
                href={url}
                target="_blank"
                rel="noopener noreferrer"
                title={url}
                className="block max-w-full truncate hover:underline"
              >
                {url}
              </a>
            ) : (
              '—'
            ),
            mono: true,
          },
          {
            label: 'Issue keys',
            value: asArray(r.issueKeys).map((key) => str(key)).filter(Boolean).join(', ') || '—',
            mono: true,
          },
        ]}
      />
    </div>
  );
}

// ---------- find ----------

export function FindWidget({ raw }: JiraWidgetProps) {
  const r = isObject(raw) ? raw : {};
  const query = str(r.query);
  const scope = str(r.type);
  const projects = asArray(isObject(r.projects) ? r.projects.candidates : null).filter(isObject);
  const boards = asArray(isObject(r.boards) ? r.boards.candidates : null).filter(isObject);
  const issuesPayload = isObject(r.issues) ? r.issues : {};
  const issues = asArray(issuesPayload.issues).filter(isObject);
  const issuesJql = str(issuesPayload.jql);
  const empty = !projects.length && !boards.length && !issues.length;
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <ZoneLabel>{query ? `Results for "${query}"` : 'Find results'}</ZoneLabel>
        {scope && scope !== 'all' && <Chip outline>{scope}</Chip>}
      </div>
      {empty && (
        <EmptyNote>
          Nothing found{query ? ` for "${query}"` : ''} across projects, boards, or issues.
        </EmptyNote>
      )}
      {projects.length > 0 && (
        <div>
          <SectionHeading>Projects · {projects.length}</SectionHeading>
          <ul className="flex flex-col divide-y divide-border overflow-hidden rounded-md border border-border">
            {projects.map((project, i) => (
              <li key={str(project.id) || i} className="flex items-center gap-2 px-3 py-2 text-[13px]">
                <span className="font-mono text-[12px] text-muted-foreground">{str(project.key)}</span>
                <span className="min-w-0 flex-1 truncate text-foreground">{str(project.name)}</span>
                {str(project.projectTypeKey) && <Chip>{str(project.projectTypeKey)}</Chip>}
              </li>
            ))}
          </ul>
        </div>
      )}
      {boards.length > 0 && (
        <div>
          <SectionHeading>Boards · {boards.length}</SectionHeading>
          <ul className="flex flex-col divide-y divide-border overflow-hidden rounded-md border border-border">
            {boards.map((board, i) => (
              <li key={str(board.id) || i} className="flex items-center gap-2 px-3 py-2 text-[13px]">
                <span className="min-w-0 flex-1 truncate text-foreground">{str(board.name)}</span>
                {str(board.type) && <Chip>{str(board.type)}</Chip>}
                {(str(board.projectKey) || str(board.location?.projectKey)) && (
                  <span className="font-mono text-[12px] text-muted-foreground">
                    {str(board.projectKey) || str(board.location?.projectKey)}
                  </span>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
      {issues.length > 0 && (
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <SectionHeading className="mb-0">Issues · {issues.length}</SectionHeading>
            {issuesJql && (
              <span
                className="mb-2 inline-flex max-w-[320px] truncate font-mono text-[11px] text-muted-foreground/70"
                title={issuesJql}
              >
                {issuesJql}
              </span>
            )}
          </div>
          <div className="mt-2">
            <IssueRows issues={issues} />
          </div>
        </div>
      )}
    </div>
  );
}

// ---------- rest ----------

export function RestWidget({ raw }: JiraWidgetProps) {
  const r = isObject(raw) ? raw : {};
  const status = num(r.status);
  const kind = httpStatusKind(r.status);
  const path = urlPath(r.url);
  const hasJson = r.json !== undefined && r.json !== null;
  const text = str(r.text);
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Chip mono>GET</Chip>
        {path && (
          <span className="min-w-0 flex-1 font-mono text-[12px] text-foreground [overflow-wrap:anywhere]">
            {path}
          </span>
        )}
        {status !== null && <StatePill kind={kind} label={String(status)} />}
      </div>
      {hasJson ? (
        <ValueBlock value={r.json} />
      ) : text ? (
        <pre
          tabIndex={0}
          role="region"
          aria-label="Response body"
          className="max-h-[320px] overflow-auto whitespace-pre-wrap rounded-md border border-border bg-muted/40 px-3 py-2.5 font-mono text-[12px] text-foreground [overflow-wrap:anywhere] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {text}
        </pre>
      ) : (
        <EmptyNote>Empty response body.</EmptyNote>
      )}
    </div>
  );
}

// ---------- approval ----------

function MentionChips({ mentions }: { mentions: unknown }) {
  const list = asArray(mentions).filter(isObject);
  if (!list.length) return null;
  return (
    <div className="flex flex-wrap gap-1.5">
      {list.map((mention, i) => (
        <Chip key={str(mention.accountId) || i}>
          @{str(mention.displayName) || str(mention.accountId)}
        </Chip>
      ))}
    </div>
  );
}

export function JiraApprovalWidget({ raw }: JiraWidgetProps) {
  const r = isObject(raw) ? raw : {};
  const approval = isObject(r.approval) ? r.approval : r;
  const content = isObject(approval.content) ? approval.content : {};
  const target = isObject(approval.target) ? approval.target : {};
  const action = str(approval.action);
  const title = str(approval.title) || 'Pending Jira change';
  const detail = str(approval.detail);
  const targetKey = str(target.issue_key) || str(target.project_key);
  const commentId = str(target.comment_id) || str(content.reply_to_comment_id);
  const summary = str(content.summary);
  const markdownBody = str(content.comment) || str(content.description);
  const relationship = str(content.relationship);
  const relatedKeys = asArray<string>(content.related_issue_keys).map(str).filter(Boolean);
  const fieldNames = asArray<string>(content.field_names).map(str).filter(Boolean);
  const labels = asArray<string>(content.labels).map(str).filter(Boolean);
  const fileName = str(content.file_name);
  const filePreview = str(content.preview);
  const isMarkdownSource = str(content.source_format) === 'markdown';
  const hasContent =
    Boolean(summary || markdownBody || relatedKeys.length || fieldNames.length || fileName || filePreview);

  return (
    <div className="flex flex-col gap-4">
      <div className="rounded-md border border-border border-l-2 border-l-amber-500/60 bg-muted/40 p-3">
        <div className="flex flex-wrap items-center gap-2">
          <ShieldAlert size={14} aria-hidden className="shrink-0 text-amber-600" />
          <h3 className="min-w-0 flex-1 text-[13px] font-medium text-foreground [overflow-wrap:anywhere]">
            {title}
          </h3>
          <StatePill kind="pending" label="awaiting approval" />
        </div>
        {detail && (
          <p className="mt-1.5 whitespace-pre-wrap text-[12px] text-muted-foreground [overflow-wrap:anywhere]">
            {detail}
          </p>
        )}
      </div>

      <MetaGrid
        rows={[
          action ? { label: 'Action', value: <Chip mono>{action}</Chip> } : null,
          targetKey ? { label: 'Target', value: targetKey, mono: true } : null,
          commentId ? { label: 'In reply to', value: `comment #${commentId}`, mono: true } : null,
          relationship ? { label: 'Relationship', value: <Chip mono>{relationship}</Chip> } : null,
        ]}
      />

      <div>
        <SectionHeading>Content</SectionHeading>
        <div className="flex flex-col gap-2 rounded-md border border-border bg-muted/40 px-3 py-2.5">
          <MentionChips mentions={content.mentions} />
          {fieldNames.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {fieldNames.map((field) => (
                <Chip key={field} mono>
                  {field}
                </Chip>
              ))}
            </div>
          )}
          {summary && <div className="text-[13px] font-medium text-foreground">{summary}</div>}
          {/* Approval bodies are authored in Markdown and converted to ADF on
              submit — render what reviewers/readers will actually see. */}
          {markdownBody && <MarkdownRenderer content={markdownBody} />}
          {relatedKeys.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {relatedKeys.map((key) => (
                <Chip key={key} mono>
                  {key}
                </Chip>
              ))}
            </div>
          )}
          {labels.length > 0 && (
            <div className="text-[11px] text-muted-foreground">Labels: {labels.join(', ')}</div>
          )}
          {str(content.priority_name) && (
            <div className="text-[11px] text-muted-foreground">
              Priority: {str(content.priority_name)}
            </div>
          )}
          {str(content.assignee_account_id) && (
            <div className="text-[11px] text-muted-foreground">
              Assignee: <span className="font-mono">{str(content.assignee_account_id)}</span>
            </div>
          )}
          {fileName && (
            <div className="flex flex-wrap items-center gap-2 text-[13px] text-foreground">
              <Paperclip size={13} aria-hidden className="text-muted-foreground/60" />
              <span className="font-mono text-[12px]">{fileName}</span>
              {str(content.mime_type) && <Chip mono>{str(content.mime_type)}</Chip>}
              {formatBytes(content.size_bytes) && (
                <span className="text-[11px] text-muted-foreground">
                  {formatBytes(content.size_bytes)}
                </span>
              )}
            </div>
          )}
          {filePreview && (
            <pre
              tabIndex={0}
              role="region"
              aria-label="File preview"
              className="max-h-[200px] overflow-auto whitespace-pre-wrap rounded border border-border bg-background/60 px-2 py-1.5 font-mono text-[11px] text-muted-foreground [overflow-wrap:anywhere] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            >
              {filePreview}
            </pre>
          )}
          {!hasContent && (
            <span className="text-[12px] text-muted-foreground/60">No content preview provided.</span>
          )}
        </div>
        {isMarkdownSource && (
          <p className="mt-1 text-[11px] text-muted-foreground/60">markdown → Jira ADF on submit</p>
        )}
      </div>
    </div>
  );
}

// ---------- write_result ----------

export function WriteResultWidget({ raw, meta }: JiraWidgetProps) {
  const r = isObject(raw) ? raw : {};
  const toolName = String(meta?.tool_name || '');
  // Headline/variant copy is shared with the collapsed-card summary
  // (./summary jiraWriteHeadline) so the two surfaces can never drift.
  const { variant, partial, headline, headlineUrl } = jiraWriteHeadline(r, toolName);
  const links = asArray(r.links).filter(isObject);

  const mentions = asArray(r.mentions).filter(isObject);
  const updatedFields = asArray<string>(r.updatedFields).map(str).filter(Boolean);
  const labels = asArray<string>(r.labels).map(str).filter(Boolean);
  const components = asArray<string>(r.components).map(str).filter(Boolean);
  const attachments = asArray(r.attachments).filter(isObject);

  return (
    <div className="flex flex-col gap-3">
      <div
        role="status"
        aria-live="polite"
        className={cn(
          'flex flex-wrap items-center gap-2 rounded-md border border-border border-l-2 bg-muted/40 p-3',
          partial ? 'border-l-border' : 'border-l-emerald-500/60',
        )}
      >
        {/* A partial failure is a completed outcome, not a pending one —
            amber stays reserved for approvals. */}
        <StatusDot kind={partial ? 'failed' : 'success'} />
        <span className="min-w-0 flex-1 text-[13px] font-medium text-foreground [overflow-wrap:anywhere]">
          {headline}
        </span>
        <ExternalLinkIcon href={headlineUrl} label="Open in Jira" />
      </div>

      {variant === 'comment' && (
        <>
          <MetaGrid
            rows={[
              str(r.commentId) ? { label: 'Comment', value: `#${str(r.commentId)}`, mono: true } : null,
              str(r.replyToCommentId)
                ? { label: 'In reply to', value: `#${str(r.replyToCommentId)}`, mono: true }
                : null,
              mentions.length
                ? { label: 'Mentions', value: <MentionChips mentions={mentions} /> }
                : null,
            ]}
          />
          {str(r.comment) && (
            <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5">
              <MarkdownRenderer content={str(r.comment)} />
            </div>
          )}
        </>
      )}

      {variant === 'related' && links.length > 0 && (
        <ul className="flex flex-col divide-y divide-border overflow-hidden rounded-md border border-border">
          {links.map((link, i) => {
            const ok = link.ok !== false;
            const errors = asArray<string>(link.errors).map(str).filter(Boolean);
            return (
              <li key={str(link.relatedIssueKey) || i} className="flex flex-col gap-0.5 px-3 py-2">
                <div className="flex items-center gap-2 text-[13px]">
                  <span className="font-mono text-[12px] text-foreground">
                    {str(link.relatedIssueKey) || '—'}
                  </span>
                  {str(link.relationship) && <Chip mono>{str(link.relationship)}</Chip>}
                  <span className="flex-1" />
                  <StatePill kind={ok ? 'success' : 'failed'} label={ok ? 'linked' : 'failed'} />
                </div>
                {!ok && errors.length > 0 && (
                  <p className="text-[11px] text-red-400">{errors.join(' · ')}</p>
                )}
              </li>
            );
          })}
        </ul>
      )}

      {variant === 'upload' && attachments.length > 0 && (
        <ul className="flex flex-col divide-y divide-border overflow-hidden rounded-md border border-border">
          {attachments.map((attachment, i) => (
            <li key={str(attachment.id) || i} className="flex items-center gap-2 px-3 py-2 text-[13px]">
              <AttachmentFileCell attachment={attachment} />
              <span className="flex-1" />
              {formatBytes(attachment.sizeBytes) && (
                <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
                  {formatBytes(attachment.sizeBytes)}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}

      {variant === 'update' && (
        <MetaGrid
          rows={[
            updatedFields.length
              ? {
                  label: 'Updated fields',
                  value: (
                    <span className="inline-flex flex-wrap gap-1">
                      {updatedFields.map((field) => (
                        <Chip key={field} mono>
                          {field}
                        </Chip>
                      ))}
                    </span>
                  ),
                }
              : null,
            str(r.summary) ? { label: 'Summary', value: str(r.summary) } : null,
            str(r.priorityName) ? { label: 'Priority', value: str(r.priorityName) } : null,
            labels.length
              ? {
                  label: 'Labels',
                  value: (
                    <span className="inline-flex flex-wrap gap-1">
                      {labels.map((label) => (
                        <Chip key={label}>{label}</Chip>
                      ))}
                    </span>
                  ),
                }
              : null,
          ]}
        />
      )}

      {variant === 'create' && (
        <MetaGrid
          rows={[
            str(r.projectKey) ? { label: 'Project', value: str(r.projectKey), mono: true } : null,
            str(r.issueType) ? { label: 'Type', value: <Chip>{str(r.issueType)}</Chip> } : null,
            str(r.summary) ? { label: 'Summary', value: str(r.summary) } : null,
            components.length
              ? {
                  label: 'Components',
                  value: (
                    <span className="inline-flex flex-wrap gap-1">
                      {components.map((component) => (
                        <Chip key={component}>{component}</Chip>
                      ))}
                    </span>
                  ),
                }
              : null,
          ]}
        />
      )}

      {variant === 'unknown' && <ValueBlock value={r} />}
    </div>
  );
}

// ---------- dispatch ----------

export function JiraWidgetForKey({
  widgetKey,
  raw,
  meta,
}: {
  widgetKey: JiraWidgetKey;
  raw: unknown;
  meta: Record<string, any>;
}) {
  switch (widgetKey) {
    case 'status':
      return <AtlassianStatusWidget raw={raw} meta={meta} />;
    case 'connection':
      return <AtlassianConnectionWidget raw={raw} meta={meta} />;
    case 'issue':
      return <IssueWidget raw={raw} meta={meta} />;
    case 'history':
      return <IssueHistoryWidget raw={raw} meta={meta} />;
    case 'comment_list':
      return <CommentListWidget raw={raw} meta={meta} />;
    case 'attachment_list':
      return <AttachmentListWidget raw={raw} meta={meta} />;
    case 'attachment':
      return <AttachmentWidget raw={raw} meta={meta} />;
    case 'issue_link_types':
      return <IssueLinkTypesWidget raw={raw} meta={meta} />;
    case 'user_list':
      return <UserListWidget raw={raw} meta={meta} />;
    case 'issue_list':
      return <IssueListWidget raw={raw} meta={meta} />;
    case 'find':
      return <FindWidget raw={raw} meta={meta} />;
    case 'rest':
      return <RestWidget raw={raw} meta={meta} />;
    case 'approval':
      return <JiraApprovalWidget raw={raw} meta={meta} />;
    case 'write_result':
      return <WriteResultWidget raw={raw} meta={meta} />;
    default:
      return <ValueBlock value={raw} />;
  }
}
