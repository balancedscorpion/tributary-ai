import type { PermissionCategory, PermissionSubject } from '@/types/permissions';
import type { ToolInventoryOrigin, ToolPresentationSpec } from '@/lib/toolInventory';

export interface ToolTestRequest {
  threadId?: string;
  projectId?: string;
  searchDepth?: string;
  args?: Record<string, unknown>;
}

export interface ToolTestResult {
  tool_name: string;
  mode: string;
  requires_approval: boolean;
  category: PermissionCategory;
  origin: ToolInventoryOrigin;
  tool_set_id: string;
  connector_id?: string;
  presentation: ToolPresentationSpec;
  args: Record<string, unknown>;
  output: {
    ok: boolean;
    result_format?: 'json' | 'text' | string;
    result?: unknown;
    query_metadata?: Record<string, unknown>;
    result_metadata?: Record<string, unknown>;
    artifact?: unknown;
  };
  progress?: Array<Record<string, unknown>>;
}

export class ToolTestMissingInputError extends Error {
  toolName: string;
  missingArguments: string[];
  missingContext: string[];
  requiredArguments: string[];
  requiredArgumentGroups: string[][];
  requiredContext: string[];
  inputHint?: string;

  constructor(message: string, payload: Record<string, unknown>) {
    super(message || 'Tool test requires more input.');
    this.name = 'ToolTestMissingInputError';
    this.toolName = cleanString(payload.tool_name);
    this.missingArguments = cleanStringArray(payload.missing_arguments);
    this.missingContext = cleanStringArray(payload.missing_context);
    this.requiredArguments = cleanStringArray(payload.required_arguments);
    this.requiredArgumentGroups = cleanStringMatrix(payload.required_argument_groups);
    this.requiredContext = cleanStringArray(payload.required_context);
    this.inputHint = cleanString(payload.input_hint) || undefined;
  }
}

export function isToolTestMissingInput(error: unknown): error is ToolTestMissingInputError {
  return error instanceof ToolTestMissingInputError;
}

export class ToolTestPermissionError extends Error {
  toolName: string;
  decision: 'ask' | 'deny' | string;
  category: PermissionCategory | string;
  subject: PermissionSubject;
  reason?: string;

  constructor(message: string, payload: Record<string, unknown>) {
    super(message || 'Tool test is blocked by permissions.');
    this.name = 'ToolTestPermissionError';
    this.toolName = cleanString(payload.tool_name);
    this.decision = cleanString(payload.decision) || 'ask';
    this.category = cleanString(payload.category) || 'read';
    this.subject = normalizePermissionSubject(payload.subject);
    this.reason = cleanString(payload.reason) || undefined;
  }
}

export function isToolTestPermissionError(error: unknown): error is ToolTestPermissionError {
  return error instanceof ToolTestPermissionError;
}

export async function runToolTest(
  toolName: string,
  input: ToolTestRequest = {},
): Promise<ToolTestResult> {
  const body: Record<string, unknown> = { tool_name: toolName };
  if (input.threadId) body.thread_id = input.threadId;
  if (input.projectId) body.project_id = input.projectId;
  if (input.searchDepth) body.search_depth = input.searchDepth;
  if (input.args) body.args = input.args;

  const res = await fetch('/api/tools/test', {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    const payload = data && typeof data === 'object' ? (data as Record<string, unknown>) : {};
    if (
      Array.isArray(payload.missing_arguments) ||
      Array.isArray(payload.missing_context) ||
      Array.isArray(payload.required_arguments) ||
      Array.isArray(payload.required_argument_groups)
    ) {
      throw new ToolTestMissingInputError(cleanString(payload.error), payload);
    }
    if (cleanString(payload.decision)) {
      throw new ToolTestPermissionError(cleanString(payload.error), payload);
    }
    throw new Error(cleanString(payload.error) || cleanString(payload.message) || `HTTP ${res.status}`);
  }
  const result = data?.test;
  if (!result || typeof result !== 'object') {
    throw new Error('Tool test response was missing test result.');
  }
  return result as ToolTestResult;
}

function cleanString(raw: unknown): string {
  return String(raw || '').trim();
}

function cleanStringArray(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  const out: string[] = [];
  const seen = new Set<string>();
  for (const item of raw) {
    const value = cleanString(item);
    if (!value || seen.has(value)) continue;
    seen.add(value);
    out.push(value);
  }
  return out;
}

function cleanStringMatrix(raw: unknown): string[][] {
  if (!Array.isArray(raw)) return [];
  const out: string[][] = [];
  for (const group of raw) {
    const values = cleanStringArray(group);
    if (values.length > 0) out.push(values);
  }
  return out;
}

function normalizePermissionSubject(raw: unknown): PermissionSubject {
  const value = raw && typeof raw === 'object' ? (raw as Record<string, unknown>) : {};
  const subject: PermissionSubject = {
    kind: cleanString(value.kind) || 'tool',
  };
  const tool = cleanString(value.tool);
  if (tool) subject.tool = tool;
  const connector = cleanString(value.connector);
  if (connector) subject.connector = connector;
  const toolSetId = cleanString(value.tool_set_id ?? value.toolSetId ?? value.tool_set);
  if (toolSetId) subject.tool_set_id = toolSetId;
  const localRoot = cleanString(value.local_root);
  if (localRoot) subject.local_root = localRoot;
  const localKind = cleanString(value.local_kind ?? value.localKind).toLowerCase();
  if (localKind === 'folder' || localKind === 'file') subject.local_kind = localKind;
  const repository = cleanString(value.repository);
  if (repository) subject.repository = repository;
  const remoteKind = cleanString(value.remote_kind ?? value.remoteKind ?? value.resource_kind ?? value.resourceKind);
  if (remoteKind) subject.remote_kind = remoteKind;
  const remoteId = cleanString(value.remote_id ?? value.remoteId ?? value.resource_id ?? value.resourceId);
  if (remoteId) subject.remote_id = remoteId;
  const operation = cleanString(value.operation);
  if (operation) subject.operation = operation;
  const excludes = cleanStringArray(value.excludes);
  if (excludes.length > 0) subject.excludes = excludes;
  return subject;
}
