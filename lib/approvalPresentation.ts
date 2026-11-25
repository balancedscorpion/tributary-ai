// Presentation layer for approvals & permissions.
//
// Decision rule: the user must never need an internal tool name to decide.
// Every approval renders as a verb phrase + destination ("Create a Jira issue
// in PROJ"). Raw tool names / operations / ids stay behind the "Technical
// details" disclosure in ApprovalCard.

import type { LucideIcon } from 'lucide-react';
import {
  Database,
  Eye,
  PenLine,
  TerminalSquare,
  FolderSearch,
  GitBranch,
  MessageSquare,
  Paperclip,
  FileText,
  Link2,
  Search,
  ShieldQuestion,
} from 'lucide-react';
import { formatScanBytes } from '@/lib/scanBudget';
import type {
  ApprovalRequestUI,
  ApprovalScope,
  ApprovalStatus,
  PermissionCategory,
  PermissionSubject,
} from '@/types/permissions';

// ---------------------------------------------------------------------------
// Categories
// ---------------------------------------------------------------------------

export interface CategoryMeta {
  /** Settings section heading (matches the Read/Write/Execute model). */
  label: string;
  /** Chip label in approval cards — "Run" instead of the jargon "Execute". */
  chipLabel: string;
  icon: LucideIcon;
  /** Generic one-line meaning; prefer categoryMeaning() for subject-aware copy. */
  meaning: string;
  /** Longer description used as a Settings section intro. */
  sectionIntro: string;
}

export const CATEGORY_META: Record<PermissionCategory, CategoryMeta> = {
  read: {
    label: 'Read',
    chipLabel: 'Read',
    icon: Eye,
    meaning: 'Looks at data without changing anything',
    sectionIntro: 'What the assistant is allowed to look at.',
  },
  write: {
    label: 'Write',
    chipLabel: 'Write',
    icon: PenLine,
    meaning: 'Changes or sends data outside this app',
    sectionIntro: 'What the assistant is allowed to change or send.',
  },
  execute: {
    label: 'Execute',
    chipLabel: 'Run',
    icon: TerminalSquare,
    meaning: 'Runs a command or repository operation',
    sectionIntro: 'Commands and repository operations the assistant can run.',
  },
};

/**
 * Subject-aware category one-liner ("Adds or changes data in Jira" beats
 * "Changes data outside the app"). Falls back to the generic meaning.
 */
export function categoryMeaning(
  category: PermissionCategory,
  subject?: PermissionSubject,
): string {
  const conn = connectorLabel(subject?.connector);
  switch (category) {
    case 'read':
      if (subject?.kind === 'local_location') {
        if (isComputerWideLocalSubject(subject)) {
          return 'Searches files on this computer — nothing is changed';
        }
        return localSubjectKind(subject) === 'file'
          ? 'Reads this file — nothing is changed'
          : 'Reads files in this folder — nothing is changed';
      }
      return conn ? `Reads from ${conn} — nothing is changed` : CATEGORY_META.read.meaning;
    case 'write':
      return conn ? `Adds or changes data in ${conn}` : CATEGORY_META.write.meaning;
    case 'execute':
      if (subject?.kind === 'repository') {
        return 'Runs a repository operation on this computer';
      }
      return conn
        ? `Runs a ${conn} operation on this computer`
        : CATEGORY_META.execute.meaning;
    default:
      return '';
  }
}

// ---------------------------------------------------------------------------
// Scopes
// ---------------------------------------------------------------------------

export interface ScopeMeta {
  /** Button / menu label. May be refined per subject via scopeLabel(). */
  label: string;
  /** One-line consequence shown in the scope menu. */
  consequence: string;
}

export const SCOPE_META: Record<ApprovalScope, ScopeMeta> = {
  once: {
    label: 'Allow once',
    consequence: 'Just this time — asks again next time',
  },
  thread: {
    label: 'Allow for this chat',
    consequence: 'Won’t ask again in this conversation',
  },
  always: {
    label: 'Always allow',
    consequence: 'Won’t ask again — change anytime in Settings → Permissions',
  },
};

export function scopeLabel(scope: ApprovalScope, subject?: PermissionSubject): string {
  if (scope === 'always' && subject?.kind === 'local_location') {
    if (isComputerWideLocalSubject(subject)) return 'Always allow this computer';
    return localSubjectKind(subject) === 'file' ? 'Always allow this file' : 'Always allow this folder';
  }
  if (scope === 'always' && subject?.kind === 'repository') {
    return 'Always allow for this repository';
  }
  if (scope === 'always' && subject?.remote_id) {
    return 'Always allow this target';
  }
  if (scope === 'always' && subject?.kind === 'tool_set') {
    return 'Always allow this tool set';
  }
  return SCOPE_META[scope].label;
}

// ---------------------------------------------------------------------------
// Statuses
// ---------------------------------------------------------------------------

export interface StatusMeta {
  /** Short label for pills/status lines. */
  label: string;
  /** Sentence used in the decided-state status line. */
  sentence: string;
  tone: 'pending' | 'positive' | 'neutral' | 'attention';
}

export const STATUS_META: Record<ApprovalStatus, StatusMeta> = {
  pending: {
    label: 'Waiting for you',
    sentence: 'Waiting for your decision',
    tone: 'pending',
  },
  approved: { label: 'Allowed', sentence: 'You allowed this', tone: 'positive' },
  rejected: {
    label: 'Not allowed',
    sentence: 'You didn’t allow this',
    tone: 'neutral',
  },
  expired: {
    label: 'Expired',
    sentence: 'This request expired before a decision',
    tone: 'neutral',
  },
  canceled: {
    label: 'Canceled',
    sentence: 'The run ended before a decision was needed',
    tone: 'neutral',
  },
};

export function decidedStatusSentence(approval: ApprovalRequestUI): string {
  const base = STATUS_META[approval.status].sentence;
  if (approval.status === 'approved' && approval.decision?.scope) {
    switch (approval.decision.scope) {
      case 'thread':
        return 'You allowed this for the rest of this chat';
      case 'always':
        return 'You allowed this from now on';
      default:
        return 'You allowed this once';
    }
  }
  return base;
}

// ---------------------------------------------------------------------------
// Connectors
// ---------------------------------------------------------------------------

const CONNECTOR_LABELS: Record<string, string> = {
  github: 'GitHub',
  atlassian: 'Jira',
  jira: 'Jira',
  confluence: 'Confluence',
  slack: 'Slack',
  sharepoint: 'SharePoint',
  arxiv: 'arXiv',
  bigquery: 'BigQuery',
  databricks: 'Databricks',
  trino: 'Trino',
  snowflake: 'Snowflake',
  browser: 'Browser',
  localfs: 'This computer',
};

export function connectorLabel(connector?: string): string {
  const key = (connector || '').trim().toLowerCase();
  if (!key) return '';
  return CONNECTOR_LABELS[key] || key.charAt(0).toUpperCase() + key.slice(1);
}

/**
 * Where the requested capability reaches: this computer, an attached upload,
 * or a connected account. The spec's provenance grammar ("Local", "Attached",
 * "Connected: SharePoint") must be explicit on approval cards so the user
 * never has to infer the blast radius from a tool name. Returns null when the
 * subject carries no origin signal worth a chip.
 */
export function subjectProvenance(subject?: PermissionSubject): string | null {
  if (!subject) return null;
  if (subject.kind === 'local_location') return 'This computer';
  if (subject.kind === 'repository') {
    // Repository operations run git against the app-managed local clone;
    // only clone/pull reach GitHub itself.
    return 'This computer';
  }
  const remoteKind = String(subject.remote_kind || '').trim().toLowerCase();
  if (remoteKind) {
    const service = remoteKind.split('_')[0];
    const label = connectorLabel(service);
    if (service === 'artifact' || service === 'attachment' || service === 'thread') {
      return 'Attached';
    }
    return label ? `Connected: ${label}` : 'Connected';
  }
  const connector = String(subject.connector || '').trim().toLowerCase();
  if (connector === 'localfs') return 'This computer';
  if (connector) return `Connected: ${connectorLabel(connector)}`;
  if (subject.kind === 'provider_hosted_tool') return 'Provider-hosted';
  return null;
}

// ---------------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------------

/** Substitute the home directory with ~ for display. Best-effort, display only. */
export function displayPath(path?: string): string {
  const p = (path || '').trim();
  if (!p) return '';
  if (isComputerWideLocalRoot(p)) return 'This computer';
  const match = p.match(/^\/(?:Users|home)\/[^/]+(\/|$)/);
  if (match) {
    const rest = p.slice(match[0].length - (match[1] === '/' ? 1 : 0));
    return `~${rest || ''}` || '~';
  }
  return p;
}

export function isComputerWideLocalRoot(path?: string): boolean {
  const p = (path || '').trim();
  return p === '/';
}

export function isComputerWideLocalSubject(subject?: PermissionSubject): boolean {
  return subject?.kind === 'local_location' && isComputerWideLocalRoot(subject.local_root);
}

// ---------------------------------------------------------------------------
// Tool phrases
// ---------------------------------------------------------------------------

export interface ApprovalPhrase {
  /** Verb phrase, e.g. "Create a Jira issue". Never an internal tool name. */
  title: string;
  /** Destination fragment, e.g. "in PROJ" / "in #general" / "~/Documents". */
  destination?: string;
  icon: LucideIcon;
}

interface PhraseRule {
  match: RegExp;
  build: (approval: ApprovalRequestUI) => ApprovalPhrase;
}

function firstString(source: Record<string, any>, ...keys: string[]): string {
  for (const key of keys) {
    const value = source?.[key];
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return '';
}

/**
 * BigQuery scan-budget approvals are cost authorization, not a reusable
 * permission: the query's estimated scan exceeds the user's automatic limit,
 * so this one execution needs an explicit OK. The card must never imply a
 * thread/always grant is on offer.
 */
export function isScanBudgetApproval(
  approval: Pick<ApprovalRequestUI, 'operation' | 'subject'>,
): boolean {
  return (
    approval.operation === 'query_scan_budget' ||
    approval.subject?.operation === 'query_scan_budget'
  );
}

/** Meaning line for scan-budget cards — cost framing instead of categoryMeaning(). */
export function scanBudgetMeaning(): string {
  return 'Costs money to run — approving runs this one query and bills your Google Cloud project for the scan. Nothing is allowed automatically next time.';
}

function jiraTarget(approval: ApprovalRequestUI): string {
  const p = approval.preview || {};
  const s: Record<string, any> = approval.subject || {};
  return (
    firstString(s, 'remote_id') ||
    firstString(p, 'issue_key', 'issueKey') ||
    firstString(s, 'repository') ||
    firstString(p, 'project_key', 'projectKey') ||
    ''
  );
}

const PHRASE_RULES: PhraseRule[] = [
  {
    match: /^jira_create_issue$/,
    build: (a) => ({
      title: 'Create a Jira issue',
      destination: jiraTarget(a) ? `in ${jiraTarget(a)}` : undefined,
      icon: FileText,
    }),
  },
  {
    match: /^jira_update_issue$/,
    build: (a) => ({
      title: 'Update a Jira issue',
      destination: jiraTarget(a) || undefined,
      icon: PenLine,
    }),
  },
  {
    match: /^jira_(add_comment|reply_to_comment)$/,
    build: (a) => ({
      title: a.tool === 'jira_reply_to_comment' ? 'Reply to a Jira comment' : 'Post a Jira comment',
      destination: jiraTarget(a) ? `on ${jiraTarget(a)}` : undefined,
      icon: MessageSquare,
    }),
  },
  {
    match: /^jira_add_related_tickets$/,
    build: (a) => ({
      title: 'Link Jira issues',
      destination: jiraTarget(a) ? `to ${jiraTarget(a)}` : undefined,
      icon: Link2,
    }),
  },
  {
    match: /^jira_upload_attachment$/,
    build: (a) => ({
      title: 'Upload a file to Jira',
      destination: jiraTarget(a) ? `on ${jiraTarget(a)}` : undefined,
      icon: Paperclip,
    }),
  },
  {
    match: /^slack_post_message$/,
    build: (a) => {
      const channel =
        firstString(a.preview, 'channel_name', 'channelName', 'channel_id', 'channelId') ||
        firstString((a.preview as any)?.args || {}, 'channel_name', 'channelName', 'channel_id', 'channelId') ||
        firstString(a.subject as any, 'remote_id');
      return {
        title: 'Send a Slack message',
        destination: channel ? `to ${channel.startsWith('#') ? channel : `#${channel}`}` : undefined,
        icon: MessageSquare,
      };
    },
  },
  {
    match: /^slack_upload_file$/,
    build: (a) => {
      const channel =
        firstString(a.preview, 'channel_name', 'channelName', 'channel_id', 'channelId') ||
        firstString((a.preview as any)?.args || {}, 'channel_name', 'channelName', 'channel_id', 'channelId') ||
        firstString(a.subject as any, 'remote_id');
      return {
        title: 'Upload a file to Slack',
        destination: channel ? `in ${channel.startsWith('#') ? channel : `#${channel}`}` : undefined,
        icon: Paperclip,
      };
    },
  },
  {
    match: /^github_clone_repository$/,
    build: (a) => ({
      title: 'Clone a GitHub repository',
      destination: a.subject.repository || firstString(a.preview, 'repository', 'repo') || undefined,
      icon: GitBranch,
    }),
  },
  {
    match: /^github_(pull|refresh)_repository$/,
    build: (a) => ({
      title: 'Pull the latest repository changes',
      destination: a.subject.repository || firstString(a.preview, 'repository', 'repo') || undefined,
      icon: GitBranch,
    }),
  },
  {
    match: /^github_run_gh_command$/,
    build: (a) => ({
      title: 'Run a GitHub command',
      destination: undefined,
      icon: TerminalSquare,
    }),
  },
  {
    match: /^run_bigquery_query$/,
    build: (a) => {
      const project = firstString(a.preview, 'project_id', 'projectId');
      if (isScanBudgetApproval(a)) {
        const estimate = formatScanBytes(a.preview?.estimated_bytes ?? a.preview?.estimatedBytes);
        return {
          title: estimate
            ? `Run a BigQuery query scanning about ${estimate}`
            : 'Run a BigQuery query over your automatic scan limit',
          destination: project ? `in ${project}` : undefined,
          icon: Database,
        };
      }
      return {
        title: 'Run a BigQuery query',
        destination: project ? `in ${project}` : undefined,
        icon: Database,
      };
    },
  },
  {
    match: /^(local_list_folder|local_list_grant_folder)$/,
    build: (a) => {
      const root = displayPath(a.subject.local_root);
      const path = firstString(a.preview, 'path', 'relative_path', 'relativePath');
      return {
        title: 'List files in a local folder',
        destination: root ? `in ${path ? `${root}/${path}` : root}` : path ? `in ${path}` : undefined,
        icon: FolderSearch,
      };
    },
  },
  {
    match: /^(file_search|local_search_in_grant|request_folder_access|local_find_files|local_search_file_content|local_read_file|local_open_file|local_read_grant_file)$/,
    build: (a) => {
      const root = displayPath(a.subject.local_root);
      const query = firstString(a.preview, 'query', 'name_query', 'search');
      return {
        title: query ? `Search your files for “${query}”` : 'Search files on this computer',
        destination: root ? `in ${root}` : undefined,
        icon: FolderSearch,
      };
    },
  },
];

/** Fallback: humanize a snake_case identifier without exposing it as-is. */
function humanizeToolName(tool: string, connector?: string): ApprovalPhrase {
  const cleaned = tool
    .replace(/^(jira|slack|github|sharepoint|confluence|atlassian|arxiv|local)_/, '')
    .replace(/_/g, ' ')
    .trim();
  const label = cleaned ? cleaned.charAt(0).toUpperCase() + cleaned.slice(1) : 'Use a tool';
  const conn = connectorLabel(connector);
  return {
    title: conn ? `${label} in ${conn}` : label,
    icon: ShieldQuestion,
  };
}

export function approvalPhrase(approval: ApprovalRequestUI): ApprovalPhrase {
  // Location subjects read as local access regardless of the requesting tool.
  if (approval.subject.kind === 'local_location' && approval.subject.local_root) {
    const root = displayPath(approval.subject.local_root);
    const query = firstString(approval.preview, 'query', 'name_query', 'search');
    const kind = localSubjectKind(approval.subject);
    if (isComputerWideLocalSubject(approval.subject)) {
      return {
        title: query ? `Search this computer for “${query}”` : 'Search this computer',
        icon: FolderSearch,
      };
    }
    return {
      title:
        kind === 'file'
          ? `Read ${root}`
          : query
            ? `Search ${root} for “${query}”`
            : `Read files in ${root}`,
      icon: FolderSearch,
    };
  }
  for (const rule of PHRASE_RULES) {
    if (rule.match.test(approval.tool)) return rule.build(approval);
  }
  if (approval.summary) {
    return { title: approval.summary, icon: categoryIcon(approval.category) };
  }
  return humanizeToolName(approval.tool, approval.subject.connector);
}

export function categoryIcon(category: PermissionCategory): LucideIcon {
  return CATEGORY_META[category].icon;
}

// ---------------------------------------------------------------------------
// Subjects (Settings rows)
// ---------------------------------------------------------------------------

export interface SubjectPresentation {
  label: string;
  detail?: string;
  icon: LucideIcon;
}

export function subjectPresentation(subject: PermissionSubject): SubjectPresentation {
  const remoteLabel = remoteSubjectLabel(subject);
  if (remoteLabel) {
    return {
      label: remoteLabel,
      detail: subject.operation ? humanizeToolName(subject.operation, subject.connector).title : undefined,
      icon: remoteSubjectIcon(subject),
    };
  }
  switch (subject.kind) {
    case 'local_location':
      return {
        label: displayPath(subject.local_root) || (localSubjectKind(subject) === 'file' ? 'A file on this computer' : 'A folder on this computer'),
        detail: localSubjectDetail(subject),
        icon: FolderSearch,
      };
    case 'repository':
      return {
        label: subject.repository || 'A repository',
        detail: subject.operation ? humanizeToolName(subject.operation).title : undefined,
        icon: GitBranch,
      };
    case 'connector':
      return {
        label: connectorLabel(subject.connector) || 'A connected source',
        icon: Search,
      };
    case 'tool_set':
      return {
        label: `${titleCaseFromID(subject.tool_set_id) || 'Tool set'} tools`,
        detail: subject.connector
          ? connectorLabel(subject.connector) || subject.connector
          : 'Tool-set permission',
        icon: ShieldQuestion,
      };
    case 'provider_hosted_tool':
      return {
        label: providerHostedToolLabel(subject.tool),
        detail: subject.connector
          ? `${titleCaseFromID(subject.connector)} provider capability`
          : 'Provider-hosted capability',
        icon: subject.operation === 'codeExecution' ? TerminalSquare : Search,
      };
    case 'tool':
    default: {
      if (subject.tool) {
        const phrase = humanizeToolName(subject.tool, subject.connector);
        return { label: phrase.title, icon: phrase.icon };
      }
      return {
        label: connectorLabel(subject.connector) || 'Tool',
        icon: ShieldQuestion,
      };
    }
  }
}

function providerHostedToolLabel(tool?: string): string {
  const raw = String(tool || '').trim();
  const [, name = raw] = raw.split('/');
  switch (name) {
    case 'web_search':
      return 'Hosted web search';
    case 'code_interpreter':
      return 'Hosted code interpreter';
    case 'code_execution':
      return 'Hosted code execution';
    default:
      return humanizeToolName(name || raw).title;
  }
}

function localSubjectDetail(subject: PermissionSubject): string {
  if (isComputerWideLocalSubject(subject)) {
    const excludeCount = subject.excludes?.length || 0;
    if (excludeCount <= 0) {
      return 'Includes readable folders on this computer';
    }
    return `Includes readable folders except ${excludeCount} protected ${excludeCount === 1 ? 'pattern' : 'patterns'}`;
  }
  if (localSubjectKind(subject) === 'file') {
    return 'Selected file only';
  }
  const excludeCount = subject.excludes?.length || 0;
  if (excludeCount <= 0) {
    return 'Includes subfolders';
  }
  return `Includes subfolders except ${excludeCount} protected ${excludeCount === 1 ? 'pattern' : 'patterns'}`;
}

function localSubjectKind(subject?: PermissionSubject): 'folder' | 'file' {
  return subject?.local_kind === 'file' ? 'file' : 'folder';
}

function remoteSubjectLabel(subject: PermissionSubject): string {
  const remoteKind = String(subject.remote_kind || '').trim().toLowerCase();
  const remoteID = String(subject.remote_id || '').trim();
  if (!remoteKind && !remoteID) return '';
  switch (remoteKind) {
    case 'slack_channel':
      return remoteID ? `Slack channel ${remoteID}` : 'Slack channel';
    case 'slack_thread':
      return remoteID ? `Slack thread ${remoteID}` : 'Slack thread';
    case 'slack_user':
      return remoteID ? `Slack user ${remoteID}` : 'Slack user';
    case 'slack_file':
      return remoteID ? `Slack file ${remoteID}` : 'Slack file';
    case 'github_repository':
      return remoteID ? `GitHub repository ${remoteID}` : 'GitHub repository';
    case 'github_pull_request':
      return remoteID ? `GitHub pull request ${remoteID}` : 'GitHub pull request';
    case 'github_issue':
      return remoteID ? `GitHub issue ${remoteID}` : 'GitHub issue';
    case 'github_workflow_run':
      return remoteID ? `GitHub workflow run ${remoteID}` : 'GitHub workflow run';
    case 'jira_issue':
      return remoteID ? `Jira issue ${remoteID}` : 'Jira issue';
    case 'jira_comment':
      return remoteID ? `Jira comment ${remoteID}` : 'Jira comment';
    case 'jira_project':
      return remoteID ? `Jira project ${remoteID}` : 'Jira project';
    case 'jira_attachment':
      return remoteID ? `Jira attachment ${remoteID}` : 'Jira attachment';
    case 'jira_rest_path':
      return remoteID ? `Jira REST path ${remoteID}` : 'Jira REST path';
    case 'confluence_page':
    case 'confluence_page_url':
      return remoteID ? `Confluence page ${remoteID}` : 'Confluence page';
    case 'confluence_space':
      return remoteID ? `Confluence space ${remoteID}` : 'Confluence space';
    case 'confluence_rest_path':
      return remoteID ? `Confluence REST path ${remoteID}` : 'Confluence REST path';
    case 'sharepoint_file':
      return remoteID ? `SharePoint file ${remoteID}` : 'SharePoint file';
    case 'arxiv_paper':
      return remoteID ? `arXiv paper ${remoteID}` : 'arXiv paper';
    case 'search_doc':
      return remoteID ? `Search document ${remoteID}` : 'Search document';
    case 'thread':
      return remoteID ? `Thread ${remoteID}` : 'Thread';
    case 'bigquery_project':
      return remoteID ? `BigQuery project ${remoteID}` : 'BigQuery project';
    case 'bigquery_dataset':
      return remoteID ? `BigQuery dataset ${remoteID}` : 'BigQuery dataset';
    case 'bigquery_table':
      return remoteID ? `BigQuery table ${remoteID}` : 'BigQuery table';
    case 'databricks_catalog':
      return remoteID ? `Databricks catalog ${remoteID}` : 'Databricks catalog';
    case 'databricks_schema':
      return remoteID ? `Databricks schema ${remoteID}` : 'Databricks schema';
    case 'databricks_table':
      return remoteID ? `Databricks table ${remoteID}` : 'Databricks table';
    case 'trino_catalog':
      return remoteID ? `Trino catalog ${remoteID}` : 'Trino catalog';
    case 'trino_schema':
      return remoteID ? `Trino schema ${remoteID}` : 'Trino schema';
    case 'trino_table':
      return remoteID ? `Trino table ${remoteID}` : 'Trino table';
    case 'snowflake_database':
      return remoteID ? `Snowflake database ${remoteID}` : 'Snowflake database';
    case 'snowflake_schema':
      return remoteID ? `Snowflake schema ${remoteID}` : 'Snowflake schema';
    case 'snowflake_table':
      return remoteID ? `Snowflake table ${remoteID}` : 'Snowflake table';
    case 'query_job': {
      const product = connectorLabel(subject.connector) || 'Query';
      return remoteID ? `${product} query ${remoteID}` : `${product} query`;
    }
    default:
      return [titleCaseFromID(remoteKind) || 'Remote target', remoteID].filter(Boolean).join(' ');
  }
}

function remoteSubjectIcon(subject: PermissionSubject): LucideIcon {
  const remoteKind = String(subject.remote_kind || '').trim().toLowerCase();
  if (remoteKind.startsWith('slack_')) return MessageSquare;
  if (remoteKind.startsWith('github_')) return GitBranch;
  if (remoteKind.startsWith('jira_')) return FileText;
  if (remoteKind.startsWith('confluence_')) return FileText;
  if (remoteKind === 'sharepoint_file' || remoteKind === 'arxiv_paper') return Paperclip;
  if (
    remoteKind === 'query_job' ||
    remoteKind.startsWith('bigquery_') ||
    remoteKind.startsWith('databricks_') ||
    remoteKind.startsWith('trino_') ||
    remoteKind.startsWith('snowflake_') ||
    remoteKind === 'search_doc' ||
    remoteKind === 'thread'
  ) {
    return Search;
  }
  return ShieldQuestion;
}

function titleCaseFromID(value?: string): string {
  return String(value || '')
    .split(/[_\-\s]+/)
    .filter(Boolean)
    .map((part) => part.slice(0, 1).toUpperCase() + part.slice(1))
    .join(' ');
}

// ---------------------------------------------------------------------------
// Relative time (compact, for status lines)
// ---------------------------------------------------------------------------

export function relativeTime(iso?: string): string {
  if (!iso) return '';
  const then = Date.parse(iso);
  if (!Number.isFinite(then)) return '';
  const deltaSec = Math.round((Date.now() - then) / 1000);
  if (deltaSec < 45) return 'just now';
  if (deltaSec < 90) return '1m ago';
  const minutes = Math.round(deltaSec / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  return `${days}d ago`;
}
