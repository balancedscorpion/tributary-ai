// Shared types for the persistent approvals / permissions system.
//
// Wire shapes mirror the backend models in
// docs/persistent-approvals-implementation-plan.md (snake_case JSON).
// UI code should consume the normalized types produced by
// `normalizeApprovalRequest` / `normalizePermissionRule` so it is tolerant of
// partial payloads while the backend lands incrementally.

export type PermissionCategory = 'read' | 'write' | 'execute';

export type PermissionEffect = 'allow' | 'ask' | 'deny';

export type ApprovalStatus =
  | 'pending'
  | 'approved'
  | 'rejected'
  | 'expired'
  | 'canceled';

export type ApprovalScope = 'once' | 'thread' | 'always';

export type PermissionSubjectKind =
  | 'tool'
  | 'tool_set'
  | 'provider_hosted_tool'
  | 'connector'
  | 'local_location'
  | 'remote_resource'
  | 'repository';

export interface PermissionSubject {
  kind: PermissionSubjectKind | string;
  tool?: string;
  tool_set_id?: string;
  connector?: string;
  local_root?: string;
  local_kind?: 'folder' | 'file' | string;
  repository?: string;
  remote_kind?: string;
  remote_id?: string;
  operation?: string;
  excludes?: string[];
}

export interface ApprovalDecision {
  action: 'approve' | 'reject';
  scope?: ApprovalScope;
  subject?: PermissionSubject;
  decided_at?: string;
}

/** Normalized approval request as consumed by UI components. */
export interface ApprovalRequestUI {
  id: string;
  threadId: string;
  runId: string;
  toolCallId?: string;
  tool: string;
  category: PermissionCategory;
  operation?: string;
  subject: PermissionSubject;
  summary: string;
  preview: Record<string, any>;
  /** Scopes the user may choose from. Always non-empty; defaults to ['once']. */
  options: ApprovalScope[];
  status: ApprovalStatus;
  decision?: ApprovalDecision;
  createdAt?: string;
  expiresAt?: string;
  /**
   * Which legacy integration namespace can decide this approval when the
   * unified /api/approvals routes are unavailable (github | atlassian | slack).
   */
  legacyIntegration?: string;
}

/** Normalized durable permission rule as consumed by Settings. */
export interface PermissionRuleUI {
  id: string;
  category: PermissionCategory;
  effect: PermissionEffect;
  /** 'global' or 'thread:<threadId>' */
  scope: string;
  subject: PermissionSubject;
  createdVia?: string;
  createdAt?: string;
  lastUsedAt?: string;
  expiresAt?: string;
}

export interface PermissionAuditRecordUI {
  id: string;
  type: string;
  ruleId?: string;
  approvalId?: string;
  threadId?: string;
  runId?: string;
  category?: PermissionCategory;
  effect?: PermissionEffect;
  scope?: string;
  subject?: PermissionSubject;
  createdAt?: string;
}

const APPROVAL_STATUSES: ReadonlySet<string> = new Set([
  'pending',
  'approved',
  'rejected',
  'expired',
  'canceled',
]);

const CATEGORIES: ReadonlySet<string> = new Set(['read', 'write', 'execute']);

const SCOPES: ReadonlySet<string> = new Set(['once', 'thread', 'always']);

function str(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

function obj(value: unknown): Record<string, any> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? (value as Record<string, any>)
    : {};
}

function pick(source: Record<string, any>, ...keys: string[]): unknown {
  for (const key of keys) {
    if (source[key] !== undefined && source[key] !== null) return source[key];
  }
  return undefined;
}

export function normalizePermissionSubject(raw: unknown): PermissionSubject {
  const s = obj(raw);
  const subject: PermissionSubject = {
    kind: str(pick(s, 'kind', 'subject_kind', 'subjectKind', 'type')) || 'tool',
  };
  const tool = str(pick(s, 'tool', 'tool_name', 'toolName'));
  if (tool) subject.tool = tool;
  const connector = str(pick(s, 'connector', 'integration'));
  if (connector) subject.connector = connector;
  const toolSetId = str(pick(s, 'tool_set_id', 'toolSetId', 'tool_set', 'toolSet'));
  if (toolSetId) subject.tool_set_id = toolSetId;
  const localRoot = str(pick(s, 'local_root', 'localRoot', 'root'));
  if (localRoot) subject.local_root = localRoot;
  const localKind = str(pick(s, 'local_kind', 'localKind', 'kind_of_local_root', 'localRootKind')).toLowerCase();
  if (localKind === 'folder' || localKind === 'file') subject.local_kind = localKind;
  const repository = str(pick(s, 'repository', 'repo'));
  if (repository) subject.repository = repository;
  const remoteKind = str(pick(s, 'remote_kind', 'remoteKind', 'resource_kind', 'resourceKind'));
  if (remoteKind) subject.remote_kind = remoteKind;
  const remoteId = str(pick(s, 'remote_id', 'remoteId', 'resource_id', 'resourceId'));
  if (remoteId) subject.remote_id = remoteId;
  const operation = str(pick(s, 'operation'));
  if (operation) subject.operation = operation;
  const excludes = pick(s, 'excludes');
  if (Array.isArray(excludes)) {
    subject.excludes = excludes.map((e) => str(e)).filter(Boolean);
  }
  return subject;
}

export function normalizeApprovalStatus(value: unknown): ApprovalStatus {
  const status = str(value).toLowerCase();
  if (status === 'cancelled') return 'canceled';
  return APPROVAL_STATUSES.has(status) ? (status as ApprovalStatus) : 'pending';
}

export function normalizeApprovalScopeOptions(value: unknown): ApprovalScope[] {
  const raw = Array.isArray(value) ? value : [];
  const options = raw
    .map((v) => str(v).toLowerCase())
    .filter((v): v is ApprovalScope => SCOPES.has(v));
  return options.length > 0 ? options : ['once'];
}

/**
 * Normalize an approval payload from any of its three transports: the
 * `approval` SSE event, the unified /api/approvals list, or a tool-result
 * envelope's `result.approval` object. Returns null when there is no id.
 */
export function normalizeApprovalRequest(
  raw: unknown,
  context?: { threadId?: string; runId?: string; legacyIntegration?: string },
): ApprovalRequestUI | null {
  const a = obj(raw);
  const id = str(pick(a, 'id', 'approval_id', 'approvalId'));
  if (!id) return null;
  const category = str(pick(a, 'category')).toLowerCase();
  const decisionRaw = obj(pick(a, 'decision'));
  const decisionAction = str(pick(decisionRaw, 'action')).toLowerCase();
  const decision: ApprovalDecision | undefined =
    decisionAction === 'approve' || decisionAction === 'reject'
      ? {
          action: decisionAction,
          scope: SCOPES.has(str(pick(decisionRaw, 'scope')).toLowerCase())
            ? (str(pick(decisionRaw, 'scope')).toLowerCase() as ApprovalScope)
            : undefined,
          subject:
            obj(pick(decisionRaw, 'subject')).kind || obj(pick(decisionRaw, 'subject')).local_root
              ? normalizePermissionSubject(pick(decisionRaw, 'subject'))
              : undefined,
          decided_at: str(pick(decisionRaw, 'decided_at', 'decidedAt')) || undefined,
        }
      : undefined;
  const subject =
    decision?.subject && normalizeApprovalStatus(pick(a, 'status', 'state')) === 'approved'
      ? decision.subject
      : normalizePermissionSubject(pick(a, 'subject', 'target'));
  return {
    id,
    threadId: str(pick(a, 'thread_id', 'threadId')) || str(context?.threadId) || '',
    runId: str(pick(a, 'run_id', 'runId')) || str(context?.runId) || '',
    toolCallId: str(pick(a, 'tool_call_id', 'toolCallId')) || undefined,
    tool: str(pick(a, 'tool', 'tool_name', 'toolName')),
    category: CATEGORIES.has(category) ? (category as PermissionCategory) : 'write',
    operation: str(pick(a, 'operation', 'action')) || undefined,
    subject,
    summary: str(pick(a, 'summary', 'title', 'detail')),
    preview: obj(pick(a, 'preview', 'content')),
    options: normalizeApprovalScopeOptions(pick(a, 'options', 'scopes')),
    status: normalizeApprovalStatus(pick(a, 'status', 'state')),
    decision,
    createdAt: str(pick(a, 'created_at', 'createdAt')) || undefined,
    expiresAt: str(pick(a, 'expires_at', 'expiresAt')) || undefined,
    legacyIntegration:
      str(pick(a, 'legacy_integration', 'integration')) ||
      str(context?.legacyIntegration) ||
      undefined,
  };
}

export function normalizePermissionRule(raw: unknown): PermissionRuleUI | null {
  const r = obj(raw);
  const id = str(pick(r, 'id'));
  if (!id) return null;
  const category = str(pick(r, 'category')).toLowerCase();
  const effect = str(pick(r, 'effect')).toLowerCase();
  return {
    id,
    category: CATEGORIES.has(category) ? (category as PermissionCategory) : 'read',
    effect: effect === 'allow' || effect === 'deny' ? (effect as PermissionEffect) : 'ask',
    scope: str(pick(r, 'scope')) || 'global',
    subject: normalizePermissionSubject(pick(r, 'subject')),
    createdVia: str(pick(r, 'created_via', 'createdVia')) || undefined,
    createdAt: str(pick(r, 'created_at', 'createdAt')) || undefined,
    lastUsedAt: str(pick(r, 'last_used_at', 'lastUsedAt')) || undefined,
    expiresAt: str(pick(r, 'expires_at', 'expiresAt')) || undefined,
  };
}

export function normalizePermissionAuditRecord(
  raw: unknown,
): PermissionAuditRecordUI | null {
  const r = obj(raw);
  const id = str(pick(r, 'id'));
  if (!id) return null;
  const category = str(pick(r, 'category')).toLowerCase();
  const effect = str(pick(r, 'effect')).toLowerCase();
  const rawSubject = pick(r, 'subject');
  const subjectObject = obj(rawSubject);
  return {
    id,
    type: str(pick(r, 'type')) || 'audit',
    ruleId: str(pick(r, 'rule_id', 'ruleId')) || undefined,
    approvalId: str(pick(r, 'approval_id', 'approvalId')) || undefined,
    threadId: str(pick(r, 'thread_id', 'threadId')) || undefined,
    runId: str(pick(r, 'run_id', 'runId')) || undefined,
    category: CATEGORIES.has(category) ? (category as PermissionCategory) : undefined,
    effect:
      effect === 'allow' || effect === 'ask' || effect === 'deny'
        ? (effect as PermissionEffect)
        : undefined,
    scope: str(pick(r, 'scope')) || undefined,
    subject: Object.keys(subjectObject).length
      ? normalizePermissionSubject(subjectObject)
      : undefined,
    createdAt: str(pick(r, 'created_at', 'createdAt')) || undefined,
  };
}

export function isApprovalPending(approval: ApprovalRequestUI | null | undefined): boolean {
  return approval?.status === 'pending';
}

export function threadScopeValue(threadId: string): string {
  return `thread:${threadId}`;
}

export function isThreadScope(scope: string): boolean {
  return scope.startsWith('thread:');
}
