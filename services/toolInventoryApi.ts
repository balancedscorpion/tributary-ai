import {
  STATIC_TOOL_INVENTORY,
  tryNormalizeRuntimeToolInventory,
  type PermissionToolInventory,
} from '@/lib/toolInventory';

export async function fetchToolInventory(): Promise<PermissionToolInventory> {
  let res: Response;
  try {
    res = await fetch('/api/tools/inventory', {
      headers: { Accept: 'application/json' },
    });
  } catch {
    return STATIC_TOOL_INVENTORY;
  }

  if (!res.ok) {
    return STATIC_TOOL_INVENTORY;
  }

  const data = await res.json().catch(() => null);
  if (!data || typeof data !== 'object' || !Array.isArray((data as any).tools)) {
    throw new Error('Tool inventory response was malformed.');
  }
  const hasProviderHostedTools = Object.prototype.hasOwnProperty.call(
    data,
    'provider_hosted_tools',
  );
  if (
    hasProviderHostedTools &&
    !Array.isArray((data as any).provider_hosted_tools)
  ) {
    throw new Error('Provider-hosted tool inventory response was malformed.');
  }

  const inventory = tryNormalizeRuntimeToolInventory(data?.tools, data?.provider_hosted_tools);
  if (!inventory) {
    throw new Error('Tool inventory response did not contain any valid tools.');
  }
  if (
    hasProviderHostedTools &&
    (data as any).provider_hosted_tools.length > 0 &&
    !(
      inventory.connectionTools['web-search'].some(
        (tool) => tool.permissionSubjectKind === 'provider_hosted_tool',
      ) ||
      inventory.connectionTools['python-sandbox'].some(
        (tool) => tool.permissionSubjectKind === 'provider_hosted_tool',
      ) ||
      inventory.appToolGroups.some(
        (group) => group.id === 'provider-hosted' && group.tools.length > 0,
      )
    )
  ) {
    throw new Error('Provider-hosted tool inventory response did not contain valid tools.');
  }
  return inventory;
}
