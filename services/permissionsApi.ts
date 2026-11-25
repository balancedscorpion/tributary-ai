// API client for the unified permissions/approvals service.
//
// The unified routes (`/api/approvals`, `/api/permissions/rules`) are being
// added by the backend permissions work. Until they exist everywhere, this
// client:
//   - reports capability via `unavailable` errors the UI can render gracefully;
//   - falls back to the legacy per-connector approval routes
//     (`/api/integrations/{github|atlassian|slack}/approvals/...`) for
//     decisions on approvals that carry a legacy integration hint.
//
// All requests go through window.fetch, which the desktop-token shim
// (services/apiClient.ts) authenticates for same-origin /api/ calls.

import {
  normalizeApprovalRequest,
  normalizePermissionAuditRecord,
  normalizePermissionRule,
  type ApprovalRequestUI,
  type ApprovalScope,
  type PermissionAuditRecordUI,
  type PermissionCategory,
  type PermissionEffect,
  type PermissionRuleUI,
  type PermissionSubject,
} from '@/types/permissions';

export class PermissionsApiUnavailableError extends Error {
  constructor(message = 'Permissions service is not available in this build.') {
    super(message);
    this.name = 'PermissionsApiUnavailableError';
  }
}

/**
 * The approval was already decided, expired, or canceled elsewhere. Callers
 * must refetch state instead of retrying the decision — re-POSTing would
 * double-decide.
 */
export class ApprovalConflictError extends Error {
  constructor(message = 'This request was already decided.') {
    super(message);
    this.name = 'ApprovalConflictError';
  }
}

/** True when the error means "backend route not implemented", not a real failure. */
export function isPermissionsUnavailable(error: unknown): boolean {
  return error instanceof PermissionsApiUnavailableError;
}

export function isApprovalConflict(error: unknown): boolean {
  return error instanceof ApprovalConflictError;
}

async function requestJSON(
  path: string,
  init?: RequestInit,
): Promise<Record<string, any>> {
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
    throw new PermissionsApiUnavailableError();
  }
  const data = await res.json().catch(() => ({}));
  if (res.status === 409 || res.status === 410) {
    throw new ApprovalConflictError(
      (data && (data.error || data.message)) || undefined,
    );
  }
  if (!res.ok) {
    throw new Error(
      (data && (data.error || data.message)) || `HTTP ${res.status}`,
    );
  }
  return data && typeof data === 'object' ? data : {};
}

// ---------------------------------------------------------------------------
// Approvals
// ---------------------------------------------------------------------------

export interface ListApprovalsFilter {
  threadId?: string;
  status?: string;
}

export async function listApprovals(
  filter: ListApprovalsFilter = {},
): Promise<ApprovalRequestUI[]> {
  const params = new URLSearchParams();
  if (filter.threadId) params.set('thread_id', filter.threadId);
  if (filter.status) params.set('status', filter.status);
  const qs = params.toString();
  const data = await requestJSON(`/api/approvals${qs ? `?${qs}` : ''}`);
  const raw = Array.isArray(data.approvals)
    ? data.approvals
    : Array.isArray(data)
      ? data
      : [];
  return raw
    .map((a: unknown) => normalizeApprovalRequest(a))
    .filter((a): a is ApprovalRequestUI => a !== null);
}

export interface DecideApprovalInput {
  approvalId: string;
  action: 'approve' | 'reject';
  scope?: ApprovalScope;
  /**
   * Optional user-selected subject override. Used by picker-backed local file
   * approvals: the model requests access, but the user chooses the file or folder.
   */
  subject?: PermissionSubject;
  /** Legacy fallback namespace (github | atlassian | slack), when known. */
  legacyIntegration?: string;
}

export interface DecideApprovalResult {
  approval: ApprovalRequestUI | null;
  /** True when the decision went through a legacy per-connector route. */
  viaLegacyRoute: boolean;
}

/**
 * Decide an approval through the unified route, falling back to the legacy
 * per-connector route when the unified one is not deployed yet.
 */
export async function decideApproval(
  input: DecideApprovalInput,
): Promise<DecideApprovalResult> {
  const { approvalId, action, scope, subject, legacyIntegration } = input;
  try {
    const body =
      action === 'approve'
        ? JSON.stringify({
            scope: scope || 'once',
            ...(subject ? { subject } : {}),
          })
        : undefined;
    const data = await requestJSON(
      `/api/approvals/${encodeURIComponent(approvalId)}/${action}`,
      {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body,
      },
    );
    return {
      approval: normalizeApprovalRequest(data.approval ?? data),
      viaLegacyRoute: false,
    };
  } catch (e) {
    if (!isPermissionsUnavailable(e) || !legacyIntegration) throw e;
  }
  // Legacy route: no scope concept — decisions are always "once".
  await requestJSON(
    `/api/integrations/${encodeURIComponent(legacyIntegration)}/approvals/${encodeURIComponent(approvalId)}/${action}`,
    { method: 'POST' },
  );
  return { approval: null, viaLegacyRoute: true };
}

// ---------------------------------------------------------------------------
// Permission rules
// ---------------------------------------------------------------------------

export interface ListRulesFilter {
  category?: PermissionCategory;
  scope?: string;
}

export async function listPermissionRules(
  filter: ListRulesFilter = {},
): Promise<PermissionRuleUI[]> {
  const params = new URLSearchParams();
  if (filter.category) params.set('category', filter.category);
  if (filter.scope) params.set('scope', filter.scope);
  const qs = params.toString();
  const data = await requestJSON(`/api/permissions/rules${qs ? `?${qs}` : ''}`);
  const raw = Array.isArray(data.rules)
    ? data.rules
    : Array.isArray(data)
      ? data
      : [];
  return raw
    .map((r: unknown) => normalizePermissionRule(r))
    .filter((r): r is PermissionRuleUI => r !== null);
}

export interface CreateRuleInput {
  category: PermissionCategory;
  effect: PermissionEffect;
  scope: string;
  subject: PermissionSubject;
}

export async function createPermissionRule(
  input: CreateRuleInput,
): Promise<PermissionRuleUI | null> {
  const data = await requestJSON('/api/permissions/rules', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  return normalizePermissionRule(data.rule ?? data);
}

export async function updatePermissionRule(
  id: string,
  patch: Partial<Pick<CreateRuleInput, 'effect' | 'scope' | 'subject'>>,
): Promise<PermissionRuleUI | null> {
  const data = await requestJSON(
    `/api/permissions/rules/${encodeURIComponent(id)}`,
    {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(patch),
    },
  );
  return normalizePermissionRule(data.rule ?? data);
}

export async function deletePermissionRule(id: string): Promise<void> {
  await requestJSON(`/api/permissions/rules/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  });
}

export async function listPermissionAudit(): Promise<PermissionAuditRecordUI[]> {
  const data = await requestJSON('/api/permissions/audit');
  const raw = Array.isArray(data.records)
    ? data.records
    : Array.isArray(data)
      ? data
      : [];
  return raw
    .map((r: unknown) => normalizePermissionAuditRecord(r))
    .filter((r): r is PermissionAuditRecordUI => r !== null);
}

export async function testLocalPermissionGrant(subject: PermissionSubject): Promise<void> {
  await requestJSON('/api/permissions/local-locations/test', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ subject }),
  });
}
