import React, { useState } from 'react';
import {
  Bookmark,
  Bug,
  CheckSquare,
  ChevronDown,
  ChevronUp,
  ChevronsDown,
  ChevronsUp,
  Clock,
  CornerDownRight,
  Equal,
  ExternalLink,
  MessageCircle,
  Zap,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { AdfContent, adfToText, isAdfDoc, type AdfNode } from './adf';

/**
 * Jira issue card — the dedicated renderer for jira_get_issue results.
 *
 * Normalizes both payload generations:
 *  - live bridge shape (server/internal/atlassian/bridge.mjs): the summarized
 *    issue nested under `result.issue`, users as {displayName}, ADF
 *    `description` + plain `descriptionText`;
 *  - legacy flat shape: issue fields at the result top level, users as plain
 *    strings, `comments` as a bare count.
 */

export interface JiraIssueData {
  key: string;
  url: string | null;
  summary: string;
  status: string;
  issueType: string;
  priority: string;
  assignee: string | null;
  reporter: string | null;
  labels: string[];
  components: string[];
  commentsCount: number | null;
  created: string | null;
  updated: string | null;
  description: AdfNode | null;
  descriptionText: string;
}

function isObject(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function str(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function namedString(value: unknown): string {
  if (typeof value === 'string') return value.trim();
  if (isObject(value)) return str(value.name) || str(value.displayName);
  return '';
}

function personName(value: unknown): string | null {
  if (typeof value === 'string') return value.trim() || null;
  if (isObject(value)) {
    return str(value.displayName) || str(value.name) || str(value.emailAddress) || null;
  }
  return null;
}

function stringList(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.map(namedString).filter(Boolean);
}

function safeUrl(value: unknown): string | null {
  const url = str(value);
  return /^https?:\/\//i.test(url) ? url : null;
}

export function normalizeJiraIssue(raw: unknown): JiraIssueData | null {
  if (!isObject(raw)) return null;
  const key = str(raw.key);
  const summary = str(raw.summary);
  if (!key && !summary) return null;
  const commentsCount =
    typeof raw.commentsCount === 'number'
      ? raw.commentsCount
      : typeof raw.comments === 'number'
        ? raw.comments
        : Array.isArray(raw.comments)
          ? raw.comments.length
          : null;
  return {
    key,
    url: safeUrl(raw.url),
    summary,
    status: namedString(raw.status),
    issueType: namedString(raw.issueType) || namedString(raw.issuetype),
    priority: namedString(raw.priority),
    assignee: personName(raw.assignee),
    reporter: personName(raw.reporter),
    labels: stringList(raw.labels),
    components: stringList(raw.components),
    commentsCount,
    created: str(raw.created) || null,
    updated: str(raw.updated) || null,
    description: isAdfDoc(raw.description) ? raw.description : null,
    descriptionText:
      str(raw.descriptionText) ||
      (typeof raw.description === 'string' ? raw.description.trim() : ''),
  };
}

export function extractJiraIssueFromResult(result: unknown): JiraIssueData | null {
  if (!isObject(result)) return null;
  // Live bridge shape nests the summarized issue under `issue`; some older
  // envelopes nested it one level deeper; legacy fixtures were flat.
  return (
    normalizeJiraIssue(result.issue) ||
    normalizeJiraIssue(isObject(result.issues) ? result.issues.issue : null) ||
    normalizeJiraIssue(result)
  );
}

// ---------- visual vocabulary ----------

export function issueTypeMeta(issueType: string): { Icon: typeof Bug; classes: string } {
  const t = issueType.toLowerCase();
  if (t.includes('bug')) return { Icon: Bug, classes: 'bg-destructive/10 text-destructive' };
  if (t.includes('story'))
    return { Icon: Bookmark, classes: 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-400' };
  if (t.includes('epic'))
    return { Icon: Zap, classes: 'bg-purple-500/10 text-purple-700 dark:text-purple-400' };
  if (t.includes('sub'))
    return { Icon: CornerDownRight, classes: 'bg-sky-500/10 text-sky-700 dark:text-sky-400' };
  return { Icon: CheckSquare, classes: 'bg-sky-500/10 text-sky-700 dark:text-sky-400' };
}

export function statusTone(status: string): string {
  const s = status.toLowerCase();
  if (/done|closed|resolved|complete|shipped/.test(s)) {
    return 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-400';
  }
  if (/progress|review|qa|test|develop/.test(s)) {
    return 'bg-blue-500/10 text-blue-700 dark:text-blue-400';
  }
  if (/block|imped/.test(s)) return 'bg-destructive/10 text-destructive';
  return 'bg-muted text-muted-foreground';
}

export function priorityMeta(priority: string): { Icon: typeof ChevronUp; classes: string } | null {
  const p = priority.toLowerCase();
  if (!p) return null;
  if (/highest|blocker|critical|urgent/.test(p)) {
    return { Icon: ChevronsUp, classes: 'bg-destructive/10 text-destructive' };
  }
  if (/high|major/.test(p)) {
    return { Icon: ChevronUp, classes: 'bg-amber-500/10 text-amber-700 dark:text-amber-400' };
  }
  if (/lowest|trivial/.test(p)) {
    return { Icon: ChevronsDown, classes: 'bg-sky-500/10 text-sky-700 dark:text-sky-400' };
  }
  if (/low|minor/.test(p)) {
    return { Icon: ChevronDown, classes: 'bg-sky-500/10 text-sky-700 dark:text-sky-400' };
  }
  return { Icon: Equal, classes: 'bg-muted text-muted-foreground' };
}

export function initials(name: string): string {
  const parts = name.split(/\s+/).filter(Boolean);
  if (!parts.length) return '?';
  return parts
    .slice(0, 2)
    .map((part) => part[0]!.toUpperCase())
    .join('');
}

function timeAgo(iso: string): string | null {
  const t = new Date(iso).getTime();
  if (!Number.isFinite(t)) return null;
  const seconds = Math.max(0, Math.round((Date.now() - t) / 1000));
  if (seconds < 60) return 'just now';
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days}d ago`;
  const months = Math.floor(days / 30);
  if (months < 12) return `${months}mo ago`;
  return `${Math.floor(months / 12)}y ago`;
}

function absoluteTime(iso: string): string {
  const t = new Date(iso);
  return Number.isFinite(t.getTime()) ? t.toLocaleString() : iso;
}

const DESCRIPTION_COLLAPSE_THRESHOLD = 700;

function IssueDescription({ issue }: { issue: JiraIssueData }) {
  const plain = issue.description ? adfToText(issue.description) : issue.descriptionText;
  const long = plain.length > DESCRIPTION_COLLAPSE_THRESHOLD;
  const [expanded, setExpanded] = useState(false);
  const regionId = React.useId();
  if (!issue.description && !issue.descriptionText.trim()) return null;
  // Collapsed long descriptions render as truncated PLAIN text, not a clipped
  // live ADF tree — otherwise links below the fold stay keyboard-focusable
  // with invisible focus (and aria-expanded would lie about hidden content).
  const collapsed = long && !expanded;
  return (
    <div className="mb-4">
      <div id={regionId}>
        {collapsed ? (
          <p className="whitespace-pre-wrap text-[13px] leading-relaxed text-foreground [overflow-wrap:anywhere]">
            {`${plain.slice(0, DESCRIPTION_COLLAPSE_THRESHOLD).trimEnd()}…`}
          </p>
        ) : issue.description ? (
          <AdfContent doc={issue.description} />
        ) : (
          <p className="whitespace-pre-wrap text-[13px] leading-relaxed text-foreground [overflow-wrap:anywhere]">
            {issue.descriptionText}
          </p>
        )}
      </div>
      {long && (
        <button
          type="button"
          onClick={() => setExpanded((v) => !v)}
          aria-expanded={expanded}
          aria-controls={regionId}
          className="mt-1 rounded text-[11px] text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
        >
          {expanded ? 'Show less' : 'Show full description'}
        </button>
      )}
    </div>
  );
}

export function JiraIssueCard({
  issue,
  className,
  bordered = false,
}: {
  issue: JiraIssueData;
  className?: string;
  bordered?: boolean;
}) {
  const type = issueTypeMeta(issue.issueType);
  const priority = priorityMeta(issue.priority);
  const updatedAgo = issue.updated ? timeAgo(issue.updated) : null;
  const hasPillRow = Boolean(priority) || issue.labels.length > 0 || issue.components.length > 0;

  return (
    <div
      className={cn(
        'min-w-0 text-left',
        bordered && 'rounded-lg border border-border bg-background p-4',
        className,
      )}
    >
      <div className="mb-3 flex items-center gap-2.5">
        <span
          className={cn(
            'flex h-[26px] w-[26px] shrink-0 items-center justify-center rounded-md',
            type.classes,
          )}
          title={issue.issueType || 'Issue'}
        >
          <type.Icon size={15} aria-hidden />
          <span className="sr-only">{issue.issueType || 'Issue'}</span>
        </span>
        {issue.key &&
          (issue.url ? (
            <a
              href={issue.url}
              target="_blank"
              rel="noreferrer"
              className="min-w-0 truncate font-mono text-[13px] text-muted-foreground hover:text-foreground hover:underline"
            >
              {issue.key}
            </a>
          ) : (
            <span className="min-w-0 truncate font-mono text-[13px] text-muted-foreground">
              {issue.key}
            </span>
          ))}
        <span className="flex-1" />
        {issue.status && (
          <span
            className={cn(
              'min-w-0 max-w-[55%] truncate rounded-full px-2.5 py-[3px] text-[12px] font-medium',
              statusTone(issue.status),
            )}
          >
            {issue.status}
          </span>
        )}
      </div>

      <h3 className="mb-3.5 text-[15px] font-semibold leading-[1.45] text-foreground [overflow-wrap:anywhere]">
        {issue.summary || 'Untitled issue'}
      </h3>

      {hasPillRow && (
        <div className="mb-4 flex flex-wrap items-center gap-2">
          {priority && (
            <span
              className={cn(
                'inline-flex items-center gap-1 rounded-full py-[3px] pl-1.5 pr-2.5 text-[12px] font-medium',
                priority.classes,
              )}
            >
              <priority.Icon size={13} aria-hidden />
              {issue.priority}
              <span className="sr-only">priority</span>
            </span>
          )}
          {[...issue.labels, ...issue.components].map((label, i) => (
            <span
              key={`${label}-${i}`}
              className="rounded-full border border-border bg-muted/40 px-2.5 py-[3px] text-[12px] text-muted-foreground"
              title={i >= issue.labels.length ? `Component: ${label}` : `Label: ${label}`}
            >
              <span className="sr-only">{i >= issue.labels.length ? 'component: ' : 'label: '}</span>
              {label}
            </span>
          ))}
        </div>
      )}

      <IssueDescription issue={issue} />

      <div className="flex items-center gap-3.5 border-t border-border pt-3 text-[13px] text-muted-foreground">
        <span
          className="flex min-w-0 flex-1 items-center gap-2"
          title={[
            issue.assignee ? `Assignee: ${issue.assignee}` : 'Unassigned',
            issue.reporter ? `Reporter: ${issue.reporter}` : null,
          ]
            .filter(Boolean)
            .join(' · ')}
        >
          <span
            aria-hidden
            className={cn(
              'inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-[11px] font-medium',
              issue.assignee
                ? 'bg-blue-500/10 text-blue-700 dark:text-blue-400'
                : 'bg-muted text-muted-foreground',
            )}
          >
            {issue.assignee ? initials(issue.assignee) : '—'}
          </span>
          <span className="sr-only">Assignee:</span>
          <span className="min-w-0 truncate text-foreground">
            {issue.assignee || 'Unassigned'}
          </span>
          {issue.reporter && (
            <span className="min-w-0 truncate text-muted-foreground">
              · by {issue.reporter}
            </span>
          )}
        </span>
        {typeof issue.commentsCount === 'number' && (
          <span
            className="inline-flex items-center gap-1 whitespace-nowrap"
            title={`${issue.commentsCount} ${issue.commentsCount === 1 ? 'comment' : 'comments'}`}
          >
            <MessageCircle size={14} aria-hidden />
            {issue.commentsCount}
            <span className="sr-only">{issue.commentsCount === 1 ? 'comment' : 'comments'}</span>
          </span>
        )}
        {issue.updated && updatedAgo && (
          <span
            className="inline-flex items-center gap-1 whitespace-nowrap"
            title={`Updated ${absoluteTime(issue.updated)}`}
          >
            <Clock size={14} aria-hidden />
            <time dateTime={issue.updated}>{updatedAgo}</time>
            <span className="sr-only">updated</span>
          </span>
        )}
        {issue.url && (
          <a
            href={issue.url}
            target="_blank"
            rel="noreferrer"
            aria-label="Open in Jira"
            title="Open in Jira"
            className="inline-flex items-center rounded text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <ExternalLink size={15} aria-hidden />
          </a>
        )}
      </div>
    </div>
  );
}
