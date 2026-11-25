/**
 * summary.ts — the pure (React-free) half of the Jira widget family.
 *
 * Owns widget-key resolution from result_metadata/tool names, the write-result
 * headline shared by the inspector widget and the collapsed activity card, and
 * the one-line scan summaries for every Jira/Atlassian bridge payload
 * (server/internal/atlassian/bridge.mjs shapes, tool names from
 * server/internal/chat/tools/atlassian_tools.go). The card path
 * (components/chat/toolActivity/resultSummary.ts) imports from here so a
 * successful Jira call can never fall through to the generic list scan and
 * summarize itself as "0 errors".
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

function count(n: number, noun: string, plural = `${noun}s`): string {
  return `${n} ${n === 1 ? noun : plural}`;
}

export function formatBytes(value: unknown): string | null {
  const n = num(value);
  if (n === null || n < 0) return null;
  if (n < 1024) return `${n} B`;
  const kb = n / 1024;
  if (kb < 1024) return `${kb.toFixed(kb < 10 ? 1 : 0)} KB`;
  const mb = kb / 1024;
  if (mb < 1024) return `${mb.toFixed(mb < 10 ? 1 : 0)} MB`;
  return `${(mb / 1024).toFixed(1)} GB`;
}

/** Strip the site origin so REST paths stay scannable. */
export function urlPath(value: unknown): string {
  const url = str(value);
  if (!url) return '';
  try {
    const u = new URL(url);
    return `${u.pathname}${u.search}`;
  } catch {
    return url;
  }
}

// ---------- widget-key resolution ----------

export type JiraWidgetKey =
  | 'status'
  | 'connection'
  | 'issue'
  | 'history'
  | 'comment_list'
  | 'attachment_list'
  | 'attachment'
  | 'issue_link_types'
  | 'user_list'
  | 'issue_list'
  | 'find'
  | 'rest'
  | 'approval'
  | 'write_result'
  | 'generic';

const KNOWN_WIDGETS: ReadonlySet<string> = new Set([
  'status',
  'connection',
  'issue',
  'history',
  'comment_list',
  'attachment_list',
  'attachment',
  'issue_link_types',
  'user_list',
  'issue_list',
  'find',
  'rest',
  'approval',
  'write_result',
]);

const TOOL_NAME_TO_WIDGET: Array<[RegExp, JiraWidgetKey]> = [
  [/^atlassian_status$/, 'status'],
  [/^atlassian_check_connection$/, 'connection'],
  [/^jira_get_issue$/, 'issue'],
  [/^jira_get_issue_history$/, 'history'],
  [/^jira_get_issue_comments$/, 'comment_list'],
  [/^jira_get_issue_attachments$/, 'attachment_list'],
  [/^jira_read_attachment$/, 'attachment'],
  [/^jira_get_issue_link_types$/, 'issue_link_types'],
  [/^jira_find_users$/, 'user_list'],
  [/^jira_search_issues$/, 'issue_list'],
  [/^jira_find$/, 'find'],
  [/^jira_rest_get$/, 'rest'],
  [
    /^jira_(add_comment|reply_to_comment|add_related_tickets|upload_attachment|update_issue|create_issue)_execute$/,
    'write_result',
  ],
  [
    /^jira_(add_comment|reply_to_comment|add_related_tickets|upload_attachment|update_issue|create_issue)$/,
    'approval',
  ],
];

export function resolveJiraWidgetKey(meta: Record<string, any>): JiraWidgetKey {
  const widget =
    (typeof meta?.widget === 'string' && meta.widget) ||
    (isObject(meta?.result_metadata) && typeof meta.result_metadata.widget === 'string'
      ? meta.result_metadata.widget
      : null);
  if (widget && KNOWN_WIDGETS.has(widget)) return widget as JiraWidgetKey;
  const toolName = String(meta?.tool_name || '');
  for (const [re, key] of TOOL_NAME_TO_WIDGET) {
    if (re.test(toolName)) return key;
  }
  return 'generic';
}

/** Jira/Atlassian payloads only — widget strings like `status`/`issue_list`
 *  exist in other connector families (GitHub), so the key alone never claims. */
export function isJiraFamilyMeta(meta: Record<string, any>): boolean {
  const resultMeta = isObject(meta?.result_metadata) ? meta.result_metadata : {};
  const product = String(resultMeta.product ?? meta?.product ?? '').toLowerCase();
  const toolName = String(meta?.tool_name || '');
  return /^(jira|atlassian)_/.test(toolName) || product === 'jira' || product === 'atlassian';
}

// ---------- write-result headline (shared widget + card copy) ----------

export type JiraWriteVariant = 'comment' | 'related' | 'upload' | 'update' | 'create' | 'unknown';

export function jiraWriteVariant(r: Record<string, any>, toolName: string): JiraWriteVariant {
  if (/^jira_(add_comment|reply_to_comment)/.test(toolName)) return 'comment';
  if (/^jira_add_related_tickets/.test(toolName)) return 'related';
  if (/^jira_upload_attachment/.test(toolName)) return 'upload';
  if (/^jira_update_issue/.test(toolName)) return 'update';
  if (/^jira_create_issue/.test(toolName)) return 'create';
  if (str(r.commentId)) return 'comment';
  if (asArray(r.relatedIssueKeys).length || asArray(r.links).length) return 'related';
  if (str(r.fileName) && asArray(r.attachments).length) return 'upload';
  if (asArray(r.updatedFields).length) return 'update';
  if (str(r.projectKey) && str(r.issueKey)) return 'create';
  return 'unknown';
}

export interface JiraWriteHeadline {
  variant: JiraWriteVariant;
  /** True when some (not all) related links failed — a completed, mixed outcome. */
  partial: boolean;
  headline: string;
  headlineUrl: string | null;
}

export function jiraWriteHeadline(result: unknown, toolName: string): JiraWriteHeadline {
  const r = isObject(result) ? result : {};
  const variant = jiraWriteVariant(r, toolName);
  const issueKey = str(r.issueKey);
  const links = asArray(r.links).filter(isObject);
  const failedLinks = links.filter((l) => l.ok === false).length;
  const partial = variant === 'related' && failedLinks > 0;

  let headline = 'Change applied';
  let headlineUrl: string | null = null;
  if (variant === 'comment') {
    headline = str(r.replyToCommentId)
      ? `Reply added to ${issueKey || 'issue'}`
      : `Comment added to ${issueKey || 'issue'}`;
    headlineUrl = safeUrl(r.commentUrl);
  } else if (variant === 'related') {
    const okCount = links.length ? links.length - failedLinks : asArray(r.relatedIssueKeys).length;
    headline = partial
      ? `${okCount} of ${links.length} links created on ${issueKey || 'issue'}`
      : `${count(okCount, 'issue')} linked to ${issueKey || 'issue'}`;
  } else if (variant === 'upload') {
    headline = `${str(r.fileName) || 'Attachment'} attached to ${issueKey || 'issue'}`;
  } else if (variant === 'update') {
    headline = `${issueKey || 'Issue'} updated`;
    headlineUrl = safeUrl(r.issueUrl);
  } else if (variant === 'create') {
    headline = `${issueKey || 'Issue'} created`;
    headlineUrl = safeUrl(r.issueUrl);
  }
  return { variant, partial, headline, headlineUrl };
}

// ---------- one-line card summaries ----------

function issueKeyOf(r: Record<string, any>): string {
  return str(r.issueKey) || (isObject(r.issue) ? str(r.issue.key) : '');
}

function productStateLine(products: Record<string, any>): string {
  const parts = Object.entries(products)
    .filter(([, p]) => isObject(p))
    .map(([key, p]: [string, Record<string, any>]) => {
      const name = str(p.product) || key;
      const label = name.charAt(0).toUpperCase() + name.slice(1);
      const state =
        str(p.state).replace(/_/g, ' ') ||
        (p.connected === true ? 'connected' : p.connected === false ? 'not connected' : '');
      return state ? `${label} ${state}` : label;
    })
    .filter(Boolean);
  return parts.join(' · ');
}

/**
 * One-line, user-scannable summary for a completed Jira/Atlassian call —
 * the collapsed activity-card subtitle ("STREAM-4821 · 2 sprint transitions",
 * "2 attachments · STREAM-4821", "GET /rest/api/3/… · 200"). Returns '' when
 * the payload is not Jira-family or carries nothing presentable, so the
 * caller's generic fallbacks still apply.
 */
export function getJiraToolResultSummary(
  meta: Record<string, any>,
  result: unknown,
): string {
  if (!isJiraFamilyMeta(meta)) return '';
  const key = resolveJiraWidgetKey(meta);
  const r = isObject(result) ? result : {};

  switch (key) {
    case 'status': {
      const products = isObject(r.products) ? r.products : {};
      const line = productStateLine(products);
      if (line) return line;
      if (r.running === true) return 'Bridge running';
      if (r.running === false) return 'Bridge stopped';
      return '';
    }
    case 'connection': {
      const message = str(r.message);
      if (message) return message;
      if (r.ok === true) return 'Connected';
      if (r.ok === false) return 'Connection failed';
      return productStateLine(isObject(r.products) ? r.products : {});
    }
    case 'issue': {
      const issue = isObject(r.issue) ? r.issue : r;
      const issueKey = str(issue.key);
      const summary = str(issue.summary);
      if (issueKey && summary) return `${issueKey} · ${summary}`;
      return issueKey || summary;
    }
    case 'history': {
      const issueKey = issueKeyOf(r);
      const transitions = asArray(r.sprintTransitions).length;
      const current = isObject(r.currentSprint) ? str(r.currentSprint.name) : '';
      const tail =
        transitions > 0
          ? count(transitions, 'sprint transition')
          : current
            ? `in ${current}`
            : 'no sprint transitions';
      return issueKey ? `${issueKey} · ${tail}` : tail;
    }
    case 'comment_list': {
      const comments = asArray(r.comments).length;
      const total = num(r.total);
      const issueKey = issueKeyOf(r);
      const base =
        total !== null && total > comments
          ? `${comments} of ${total} comments`
          : count(comments, 'comment');
      return issueKey ? `${base} · ${issueKey}` : base;
    }
    case 'attachment_list': {
      const attachments = asArray(r.attachments).length;
      const issueKey = issueKeyOf(r);
      const base = count(attachments, 'attachment');
      return issueKey ? `${base} · ${issueKey}` : base;
    }
    case 'attachment': {
      const summary = isObject(r.attachment) ? r.attachment : {};
      const fileName = str(r.fileName) || str(summary.fileName);
      if (!fileName) return '';
      const size = formatBytes(num(r.sizeBytes) ?? num(summary.sizeBytes));
      const state =
        r.native_image_input === true
          ? 'image input'
          : r.truncated === true
            ? 'truncated'
            : r.binary === true && !str(r.content)
              ? 'binary'
              : '';
      return [fileName, size, state].filter(Boolean).join(' · ');
    }
    case 'issue_link_types': {
      const types = asArray(r.issueLinkTypes).length;
      return count(types, 'link type');
    }
    case 'user_list': {
      const users = asArray(r.users).length;
      const query = str(r.query);
      const base = count(users, 'user');
      return query ? `${base} · ${query}` : base;
    }
    case 'issue_list': {
      const issues = asArray(r.issues).length;
      const total = num(r.total);
      const jql = str(r.jql);
      const base =
        total !== null && total > issues ? `${issues} of ${total} issues` : count(issues, 'issue');
      return jql ? `${base} · ${jql}` : base;
    }
    case 'find': {
      const query = str(r.query);
      const projects = asArray(isObject(r.projects) ? r.projects.candidates : null).length;
      const boards = asArray(isObject(r.boards) ? r.boards.candidates : null).length;
      const issues = asArray(isObject(r.issues) ? r.issues.issues : null).length;
      const parts = [
        projects ? count(projects, 'project') : '',
        boards ? count(boards, 'board') : '',
        issues ? count(issues, 'issue') : '',
      ].filter(Boolean);
      const base = parts.length ? parts.join(' · ') : 'No matches';
      return query ? `${base} · ${query}` : base;
    }
    case 'rest': {
      const path = urlPath(r.url);
      const status = num(r.status);
      if (!path && status === null) return '';
      return `GET ${path || '/'}${status !== null ? ` · ${status}` : ''}`;
    }
    case 'approval': {
      const approval = isObject(r.approval) ? r.approval : r;
      const title = str(approval.title);
      const target = isObject(approval.target) ? approval.target : {};
      const targetKey = str(target.issue_key) || str(target.project_key);
      const base = title || 'Pending Jira change';
      const withTarget = targetKey && !base.includes(targetKey) ? `${base} · ${targetKey}` : base;
      return `${withTarget} · awaiting approval`;
    }
    case 'write_result': {
      const toolName = String(meta?.tool_name || '');
      const { variant, headline } = jiraWriteHeadline(r, toolName);
      if (variant === 'update') {
        const fields = asArray<string>(r.updatedFields).map(str).filter(Boolean);
        return fields.length ? `${headline} · ${fields.join(', ')}` : headline;
      }
      if (variant === 'create') {
        const summary = str(r.summary);
        return summary ? `${headline} · ${summary}` : headline;
      }
      if (variant === 'unknown') return '';
      return headline;
    }
    default:
      return '';
  }
}
