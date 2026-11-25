// Shared logic for Local Computer read-only reference grants.
//
// The Settings > Local Computer connection and the Sources dialog both manage
// the same underlying state: `read`/`allow`/`global` permission rules with a
// `local_location` folder subject, mirrored into `settings.localComputer`.
// Keeping the path normalization, rule matching, and settings accessors in one
// module is what keeps those two surfaces from drifting apart.

import type {
  AppSettings,
  LocalComputerAccessMode,
  LocalComputerSettings,
} from '@/types';
import type { PermissionRuleUI } from '@/types/permissions';
import {
  createPermissionRule,
  deletePermissionRule,
  listPermissionRules,
} from '@/services/permissionsApi';

export const defaultLocalComputer = (): LocalComputerSettings => ({
  enabled: false,
  accessMode: 'whole-computer',
  selectedFolders: [],
});

export const LOCAL_COMPUTER_SEARCH_COMMAND_TOOL = 'local_search_command';

interface LocalComputerRootsResponse {
  ok?: boolean;
  os?: string;
  primary_root?: string;
  roots?: unknown;
}

export const normalizeLocalComputerMode = (value: unknown): LocalComputerAccessMode => (
  String(value || '').trim() === 'selected-folders' ? 'selected-folders' : 'whole-computer'
);

export const getLocalComputer = (settings: AppSettings): LocalComputerSettings => {
  const raw = settings.localComputer || {};
  return {
    ...defaultLocalComputer(),
    ...raw,
    accessMode: normalizeLocalComputerMode(raw.accessMode),
    selectedFolders: Array.isArray(raw.selectedFolders) ? raw.selectedFolders.filter(Boolean) : [],
  };
};

export function localComputerWholeRoot(): string {
  if (typeof navigator !== 'undefined') {
    const nav = navigator as Navigator & { userAgentData?: { platform?: string } };
    const platform = [nav.userAgentData?.platform, nav.platform, nav.userAgent].filter(Boolean).join(' ');
    if (/win/i.test(platform)) return 'C:\\';
  }
  return '/';
}

export async function localComputerWholeRoots(): Promise<string[]> {
  try {
    const response = await fetch('/api/local-files/roots', { headers: { Accept: 'application/json' } });
    if (response.ok) {
      const payload = await response.json() as LocalComputerRootsResponse;
      const roots = Array.isArray(payload.roots)
        ? dedupeLocalFolders(payload.roots.map((value) => String(value || '').trim()).filter(Boolean))
        : [];
      if (roots.length > 0) return roots;
      const primary = String(payload.primary_root || '').trim();
      if (primary) return [primary];
    }
  } catch {
    // Older backends and browser-only fixtures use the platform fallback.
  }
  return [localComputerWholeRoot()];
}

export function normalizeLocalPathKey(value: string): string {
  let path = String(value || '').trim();
  if (!path) return '';
  path = path.replace(/\\/g, '/').replace(/\/+$/g, '');
  if (path === '') return '/';
  if (/^[a-z]:$/i.test(path)) return `${path.toUpperCase()}/`;
  if (/^[a-z]:\/$/i.test(path)) return path.toUpperCase();
  if (/^[a-z]:\//i.test(path)) return `${path[0].toUpperCase()}${path.slice(1).toLowerCase()}`;
  if (path.startsWith('//')) return path.toLowerCase();
  return path || '/';
}

export function dedupeLocalFolders(paths: string[]): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const value of paths) {
    const path = String(value || '').trim();
    const key = normalizeLocalPathKey(path);
    if (!path || !key || seen.has(key)) continue;
    seen.add(key);
    out.push(path);
  }
  return out;
}

export function isLocalComputerGrantRule(rule: PermissionRuleUI): boolean {
  if (rule.category !== 'read' || rule.effect !== 'allow' || rule.scope !== 'global') return false;
  if (rule.subject.kind !== 'local_location') return false;
  if (String(rule.createdVia || '').startsWith('approval')) return false;
  const localKind = String(rule.subject.local_kind || 'folder').trim().toLowerCase();
  return localKind !== 'file' && Boolean(String(rule.subject.local_root || '').trim());
}

export function localComputerRuleMatchesRoot(rule: PermissionRuleUI, root: string): boolean {
  return isLocalComputerGrantRule(rule)
    && normalizeLocalPathKey(rule.subject.local_root || '') === normalizeLocalPathKey(root);
}

export function isWholeComputerRoot(root: string): boolean {
  return normalizeLocalPathKey(root) === normalizeLocalPathKey(localComputerWholeRoot());
}

export function isLocalFilesystemRoot(root: string): boolean {
  const key = normalizeLocalPathKey(root);
  return key === '/' || /^[A-Z]:\/$/.test(key);
}

export async function listLocalComputerGrantRules(): Promise<PermissionRuleUI[]> {
  const rules = await listPermissionRules({ category: 'read' });
  return rules.filter(isLocalComputerGrantRule);
}

export function isLocalComputerSearchCommandRule(rule: PermissionRuleUI): boolean {
  return rule.category === 'execute'
    && rule.scope === 'global'
    && rule.subject.kind === 'tool'
    && rule.subject.tool === LOCAL_COMPUTER_SEARCH_COMMAND_TOOL;
}

export function isLocalComputerSearchCommandPolicyRule(rule: PermissionRuleUI): boolean {
  if (rule.category !== 'execute' || rule.scope !== 'global') return false;
  if (isLocalComputerSearchCommandRule(rule)) return true;
  if (rule.subject.kind === 'tool_set') {
    return rule.subject.tool_set_id === 'local-computer'
      && (!rule.subject.connector || rule.subject.connector === 'local-computer');
  }
  return rule.subject.kind === 'connector' && rule.subject.connector === 'local-computer';
}

export async function listLocalComputerSearchCommandRules(): Promise<PermissionRuleUI[]> {
  const rules = await listPermissionRules({ category: 'execute' });
  return rules.filter(isLocalComputerSearchCommandRule);
}

/**
 * A newly attached Local Computer connection explicitly enables the bounded
 * search-command broker. Existing custom Ask/Off/Always choices are preserved.
 */
export async function ensureLocalComputerSearchCommandPermission(): Promise<void> {
  const rules = await listPermissionRules({ category: 'execute' });
  const existing = rules.filter(isLocalComputerSearchCommandPolicyRule);
  if (existing.length > 0) return;
  await createPermissionRule({
    category: 'execute',
    effect: 'allow',
    scope: 'global',
    subject: {
      kind: 'tool',
      tool: LOCAL_COMPUTER_SEARCH_COMMAND_TOOL,
      connector: 'local-computer',
      tool_set_id: 'local-computer',
    },
  });
}

export async function ensureLocalComputerGrant(
  rules: PermissionRuleUI[],
  root: string,
): Promise<void> {
  if (rules.some((rule) => localComputerRuleMatchesRoot(rule, root))) return;
  await createPermissionRule({
    category: 'read',
    effect: 'allow',
    scope: 'global',
    subject: {
      kind: 'local_location',
      local_root: root,
      local_kind: 'folder',
    },
  });
}

export async function deleteLocalComputerGrantsForRoots(
  rules: PermissionRuleUI[],
  roots: string[],
): Promise<void> {
  const rootKeys = new Set(roots.map(normalizeLocalPathKey).filter(Boolean));
  if (rootKeys.size === 0) return;
  for (const rule of rules) {
    const key = normalizeLocalPathKey(rule.subject.local_root || '');
    if (!rootKeys.has(key)) continue;
    await deletePermissionRule(rule.id);
  }
}

/**
 * Reconcile grant rules with the desired access mode: whole-computer keeps a
 * single root grant and drops folder grants; selected-folders keeps one grant
 * per folder and drops the root grant.
 */
export async function syncLocalComputerGrants(
  mode: LocalComputerAccessMode,
  selectedFolders: string[],
): Promise<void> {
  await ensureLocalComputerSearchCommandPermission();
  const rules = await listLocalComputerGrantRules();
  const wholeRoots = await localComputerWholeRoots();
  const knownWholeRoots = dedupeLocalFolders([
    ...wholeRoots,
    ...rules
      .map((rule) => String(rule.subject.local_root || '').trim())
      .filter(isLocalFilesystemRoot),
  ]);
  const wholeRootKeys = new Set(wholeRoots.map(normalizeLocalPathKey));
  if (mode === 'whole-computer') {
    for (const wholeRoot of wholeRoots) {
      await ensureLocalComputerGrant(rules, wholeRoot);
    }
    await deleteLocalComputerGrantsForRoots(
      rules,
      dedupeLocalFolders(selectedFolders).filter((folder) => (
        !wholeRootKeys.has(normalizeLocalPathKey(folder))
      )),
    );
    return;
  }

  const folders = dedupeLocalFolders(selectedFolders);
  for (const folder of folders) {
    await ensureLocalComputerGrant(rules, folder);
  }
  const selectedKeys = new Set(folders.map(normalizeLocalPathKey));
  await deleteLocalComputerGrantsForRoots(
    rules,
    knownWholeRoots.filter((wholeRoot) => !selectedKeys.has(normalizeLocalPathKey(wholeRoot))),
  );
}
