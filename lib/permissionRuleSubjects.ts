import type { ConnectionCatalogId } from '@/types';
import type { PermissionSubject } from '@/types/permissions';
import type { ConnectionToolInfo } from '@/lib/toolInventory';

export function permissionSubjectKindForTool(tool: ConnectionToolInfo): string {
  return String(tool.permissionSubjectKind || 'tool').trim() || 'tool';
}

/**
 * Connection-wide rules are valid for ordinary connector tools. Provider-
 * hosted capabilities live in virtual catalogue groups, but the backend
 * evaluates their exact provider_hosted_tool subject and adapter family.
 */
export function supportsConnectionWidePermissionRule(tool: ConnectionToolInfo): boolean {
  return permissionSubjectKindForTool(tool).toLowerCase() === 'tool';
}

export function permissionSubjectForToolRule(
  tool: ConnectionToolInfo,
  connectionId: ConnectionCatalogId | null,
): PermissionSubject {
  const kind = permissionSubjectKindForTool(tool);
  const subject: PermissionSubject = {
    kind,
    tool: tool.name,
  };
  // Web Search and Python Sandbox are virtual catalogue groups. Their hosted
  // tools must keep the adapter-family connector the backend evaluates (for
  // example openai-responses), not the virtual web-search/python-sandbox id.
  const connector = kind.toLowerCase() === 'provider_hosted_tool'
    ? tool.connectorId
    : connectionId || tool.connectorId;
  if (connector) subject.connector = connector;
  if (tool.toolSetId) subject.tool_set_id = tool.toolSetId;
  if (tool.permissionOperation) subject.operation = tool.permissionOperation;
  return subject;
}
