import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ChevronRight,
  FileText,
  FolderOpen,
  FolderPlus,
  History,
  Lock,
  Play,
  RefreshCw,
  RotateCcw,
  Search,
  Trash2,
  TriangleAlert,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { cn } from '@/lib/utils';
import { resolveRenderer } from '@/components/renderers';
import RawViewer from '@/components/renderers/shared/RawViewer';
import PendingApprovalsQueue from '@/components/approvals/PendingApprovalsQueue';
import {
  createPermissionRule,
  deletePermissionRule,
  isPermissionsUnavailable,
  listPermissionAudit,
  listPermissionRules,
  testLocalPermissionGrant,
  updatePermissionRule,
} from '@/services/permissionsApi';
import {
  isLocalPickerCanceled,
  pickLocalDirectory,
  pickLocalFile,
} from '@/services/localFilePicker';
import { fetchToolInventory } from '@/services/toolInventoryApi';
import type {
  PermissionAuditRecordUI,
  PermissionCategory,
  PermissionEffect,
  PermissionRuleUI,
  PermissionSubject,
} from '@/types/permissions';
import {
  CATEGORY_META,
  displayPath,
  relativeTime,
  subjectPresentation,
} from '@/lib/approvalPresentation';
import { isThreadScope } from '@/types/permissions';
import type { AppSettings, ConnectionCatalogId } from '@/types';
import {
  CONNECTION_CATALOG_IDS,
  CONNECTION_META,
  deriveVisibleConnections,
  type ConnectionAuditInfo,
  type ConnectionMeta,
} from '@/lib/connectionsCatalog';
import {
  connectionIdForConnector,
  connectionIdForRule,
  getToolsForConnectionFromInventory,
  isAppToolName,
  resolveStandaloneToolPermission,
  resolveToolPermission,
  type ConnectionToolInfo,
  type EffectiveToolPermission,
  type PermissionToolInventory,
} from '@/lib/toolInventory';
import {
  permissionSubjectForToolRule,
  supportsConnectionWidePermissionRule,
} from '@/lib/permissionRuleSubjects';
import { buildToolTestArtifact } from '@/lib/toolResultArtifact';
import {
  isToolTestMissingInput,
  isToolTestPermissionError,
  runToolTest,
  type ToolTestRequest,
  type ToolTestResult,
} from '@/services/toolTestApi';

const EFFECT_LABELS: Record<PermissionEffect, string> = {
  allow: 'Always allow',
  ask: 'Ask first',
  deny: 'Off',
};

/** Segment order: escalating permissiveness, matching how users reason. */
const EFFECT_SEGMENTS: Array<{ value: PermissionEffect; label: string; aria: string }> = [
  { value: 'deny', label: 'Off', aria: 'Off — blocked automatically' },
  { value: 'ask', label: 'Ask', aria: 'Ask first' },
  { value: 'allow', label: 'Always', aria: 'Always allow' },
];

/** Built-in fallback when no custom rule covers a tool. */
function defaultEffectFor(category: PermissionCategory): PermissionEffect {
  return category === 'read' ? 'allow' : 'ask';
}

const EFFECT_HELP: Record<PermissionEffect, string> = {
  allow: 'Runs without asking you.',
  ask: 'You’re asked in the chat each time.',
  deny: 'Blocked — the assistant is told no automatically.',
};

function testIdPart(value: string): string {
  return String(value || 'item')
    .replace(/[^a-zA-Z0-9_-]+/g, '-')
    .replace(/^-+|-+$/g, '') || 'item';
}

// ---------------------------------------------------------------------------
// Shared design vocabulary: risk chips, origin badges, effect summaries
// ---------------------------------------------------------------------------

/** Risk tint per category — colors always ride with the text label. */
const RISK_CHIP_CLASS: Record<PermissionCategory, string> = {
  read: 'border-border bg-muted/60 text-muted-foreground',
  write:
    'border-amber-600/40 bg-amber-500/10 text-amber-700 dark:border-amber-400/40 dark:text-amber-400',
  execute:
    'border-rose-600/40 bg-rose-500/10 text-rose-700 dark:border-rose-400/40 dark:text-rose-400',
};

const RISK_MEANING: Record<PermissionCategory, string> = {
  read: 'looks at data',
  write: 'changes or sends data',
  execute: 'runs commands',
};

/** Where a tool set comes from and whether it is usable right now. */
type OriginKind = 'connected' | 'needs-setup' | 'not-added' | 'builtin' | 'local' | 'provider';

const ORIGIN_BADGE: Record<
  OriginKind,
  { label: string; className: string; locked?: boolean; description: string }
> = {
  connected: {
    label: 'Connected',
    className:
      'border-emerald-600/40 bg-emerald-500/10 text-emerald-700 dark:border-emerald-400/40 dark:text-emerald-400',
    description: 'You’ve connected this source — its tools are available.',
  },
  'needs-setup': {
    label: 'Needs setup',
    className:
      'border-amber-500/40 bg-amber-500/10 text-amber-700 dark:border-amber-400/40 dark:text-amber-400',
    description: 'Added, but not usable until setup finishes.',
  },
  'not-added': {
    label: 'Not added',
    className: 'border-border bg-muted/40 text-muted-foreground',
    locked: true,
    description: 'Not in your connections — tools stay locked until you add it.',
  },
  builtin: {
    label: 'Built-in',
    className: 'border-border bg-muted/40 text-muted-foreground',
    description: 'Part of the app — always available, nothing leaves this app.',
  },
  provider: {
    label: 'Provider',
    className:
      'border-violet-600/40 bg-violet-500/10 text-violet-700 dark:border-violet-400/40 dark:text-violet-400',
    description: 'Runs inside the selected model provider.',
  },
  local: {
    label: 'Local',
    className:
      'border-sky-600/40 bg-sky-500/10 text-sky-700 dark:border-sky-400/40 dark:text-sky-400',
    description: 'Works with files on this computer.',
  },
};

/** Aggregate behavior of a set of tools, shown next to scope-level controls. */
interface EffectSummary {
  kind: 'off' | 'ask' | 'allow' | 'mixed' | 'none';
  counts: Record<PermissionEffect, number>;
  total: number;
}

function summarizeEffects(effects: PermissionEffect[]): EffectSummary {
  const counts: Record<PermissionEffect, number> = { allow: 0, ask: 0, deny: 0 };
  for (const effect of effects) counts[effect] += 1;
  const total = effects.length;
  let kind: EffectSummary['kind'] = 'mixed';
  if (total === 0) kind = 'none';
  else if (counts.deny === total) kind = 'off';
  else if (counts.ask === total) kind = 'ask';
  else if (counts.allow === total) kind = 'allow';
  return { kind, counts, total };
}

/** "2 off · 8 ask · 3 always allowed" — for mixed-state detail text. */
function summaryBreakdown(summary: EffectSummary): string {
  const parts: string[] = [];
  if (summary.counts.deny > 0) parts.push(`${summary.counts.deny} off`);
  if (summary.counts.ask > 0) parts.push(`${summary.counts.ask} ask`);
  if (summary.counts.allow > 0) parts.push(`${summary.counts.allow} always allowed`);
  return parts.join(' · ');
}

/** Effective state each tool currently resolves to, precomputed by the page. */
interface ResolvedTool {
  tool: ConnectionToolInfo;
  effective: EffectiveToolPermission;
}

/** Per-scope tool view after search/filters: full inventory + visible subset. */
interface ScopeView {
  resolved: ResolvedTool[];
  visible: ResolvedTool[];
}

/** Built-in defaults, rendered as informational rows so the page teaches the model. */
const DEFAULT_ROWS: Array<{ category: PermissionCategory; label: string; effect: string }> = [
  { category: 'read', label: 'Connected sources you’ve enabled', effect: 'Allowed without asking' },
  { category: 'read', label: 'Files and folders on this computer', effect: 'Asks first, per location' },
  { category: 'write', label: 'Everything that changes or sends data', effect: 'Asks first' },
  { category: 'execute', label: 'All commands and repository operations', effect: 'Asks first' },
];

type LoadState =
  | { phase: 'loading' }
  | { phase: 'ready' }
  | { phase: 'unsupported' }
  | { phase: 'error'; message: string };

/** Per-scope state of a bulk action ("all", "conn:github", "app:attachments"…). */
interface BulkStatus {
  phase: 'idle' | 'running' | 'done' | 'error';
  /** Which action runs, so only its trigger shows busy text. */
  op?: 'set' | 'clear';
  message?: string;
}

interface BulkTarget {
  tool: ConnectionToolInfo;
  connectionId: ConnectionCatalogId | null;
  durableToolSet?: boolean;
}

function toolScopedRuleToolName(rule: PermissionRuleUI): string | undefined {
  return (rule.subject.kind === 'tool' || rule.subject.kind === 'provider_hosted_tool')
    ? rule.subject.tool
    : undefined;
}

function toolSetRuleMatchesScopeTool(
  rule: PermissionRuleUI,
  tool: ConnectionToolInfo,
  connectionId: ConnectionCatalogId | null,
): boolean {
  if (rule.subject.kind !== 'tool_set' || rule.category !== tool.category) return false;
  const ruleToolSetId = String(rule.subject.tool_set_id || '').trim().toLowerCase();
  if (!ruleToolSetId || ruleToolSetId !== String(tool.toolSetId || '').trim().toLowerCase()) {
    return false;
  }
  const rawRuleConnector = String(rule.subject.connector || '').trim().toLowerCase();
  if (!rawRuleConnector) return true;
  const catalogConnection = connectionIdForConnector(rawRuleConnector);
  if (connectionId) {
    return catalogConnection === connectionId || rawRuleConnector === connectionId;
  }
  const toolConnector = String(tool.connectorId || '').trim().toLowerCase();
  if (toolConnector && rawRuleConnector === toolConnector) return true;
  if (catalogConnection) {
    const toolConnection = connectionIdForConnector(tool.connectorId || tool.toolSetId);
    return toolConnection === catalogConnection;
  }
  return false;
}

/**
 * Rules "Clear custom permissions" would delete for a scope: global tool rules
 * on the scope's tools plus connection-wide rules for the scope's connections.
 * Repository grants and thread-scoped rules are deliberately left alone.
 */
function clearableRulesFor(
  rules: PermissionRuleUI[],
  toolNames: ReadonlySet<string>,
  connectionIds: readonly ConnectionCatalogId[],
  scopeTools: readonly ConnectionToolInfo[] = [],
): PermissionRuleUI[] {
  return rules.filter((rule) => {
    if (isThreadScope(rule.scope)) return false;
    const toolName = toolScopedRuleToolName(rule);
    if (toolName) {
      return toolNames.has(toolName);
    }
    if (rule.subject.kind === 'tool_set' && rule.subject.tool_set_id) {
      return scopeTools.some((tool) => toolSetRuleMatchesScopeTool(rule, tool, null));
    }
    if (rule.subject.kind === 'connector') {
      const id = connectionIdForConnector(rule.subject.connector);
      return id !== null && connectionIds.includes(id);
    }
    return false;
  });
}

/** Run workers a few at a time so bulk edits don't flood the backend. */
async function runWithPool<T>(
  items: T[],
  worker: (item: T) => Promise<void>,
  limit = 4,
): Promise<void> {
  let next = 0;
  await Promise.all(
    Array.from({ length: Math.min(limit, items.length) }, async () => {
      while (next < items.length) {
        const item = items[next];
        next += 1;
        await worker(item);
      }
    }),
  );
}

interface PermissionsTabProps {
  /** Live settings from the dialog — decides which connections (and tools) show. */
  settings?: AppSettings | null;
  /** Current chat context used to prefill tool tests that need a project/thread. */
  toolTestContext?: ToolTestDefaultContext;
  /** Jump to Settings → Connections (used by empty/needs-setup states). */
  onOpenConnections?: () => void;
}

/**
 * Settings → Permissions. A single scroll page with a real heading hierarchy
 * (screen readers navigate by heading): Pending, Connections & tools, Folders,
 * Other custom permissions, Defaults, Audit. Changes apply immediately — the
 * dialog footer uses the shared settings Save/Cancel actions.
 */
const PermissionsTab: React.FC<PermissionsTabProps> = ({
  settings,
  toolTestContext,
  onOpenConnections,
}) => {
  const [rules, setRules] = useState<PermissionRuleUI[]>([]);
  const [load, setLoad] = useState<LoadState>({ phase: 'loading' });
  const [announcement, setAnnouncement] = useState('');
  const [rowError, setRowError] = useState<Record<string, string>>({});
  const [auditRecords, setAuditRecords] = useState<PermissionAuditRecordUI[]>([]);
  const [auditLoad, setAuditLoad] = useState<LoadState>({ phase: 'loading' });
  const [recentlySavedTools, setRecentlySavedTools] = useState<Set<string>>(() => new Set());
  const [toolInventory, setToolInventory] = useState<PermissionToolInventory | null>(null);
  const [inventoryLoad, setInventoryLoad] = useState<'loading' | 'runtime' | 'fallback' | 'error'>('loading');
  const [inventoryError, setInventoryError] = useState('');
  const savedTimersRef = useRef<Map<string, number>>(new Map());

  const announce = useCallback((message: string) => {
    setAnnouncement('');
    requestAnimationFrame(() => setAnnouncement(message));
  }, []);

  const markToolSaved = useCallback((toolName?: string) => {
    if (!toolName) return;
    const existing = savedTimersRef.current.get(toolName);
    if (existing) window.clearTimeout(existing);
    setRecentlySavedTools((prev) => new Set(prev).add(toolName));
    const timer = window.setTimeout(() => {
      savedTimersRef.current.delete(toolName);
      setRecentlySavedTools((prev) => {
        const next = new Set(prev);
        next.delete(toolName);
        return next;
      });
    }, 2400);
    savedTimersRef.current.set(toolName, timer);
  }, []);

  useEffect(() => {
    return () => {
      savedTimersRef.current.forEach((timer) => window.clearTimeout(timer));
      savedTimersRef.current.clear();
    };
  }, []);

  const fetchRules = useCallback(async () => {
    setLoad({ phase: 'loading' });
    setAuditLoad({ phase: 'loading' });
    try {
      const fetched = await listPermissionRules();
      setRules(fetched);
      setLoad({ phase: 'ready' });
      try {
        const audit = await listPermissionAudit();
        setAuditRecords(audit);
        setAuditLoad({ phase: 'ready' });
      } catch (e: any) {
        setAuditRecords([]);
        setAuditLoad({
          phase: isPermissionsUnavailable(e) ? 'unsupported' : 'error',
          message: e?.message || 'Couldn’t load permission activity.',
        });
      }
    } catch (e: any) {
      if (isPermissionsUnavailable(e)) {
        setRules([]);
        setLoad({ phase: 'unsupported' });
        setAuditLoad({ phase: 'unsupported' });
      } else {
        setLoad({
          phase: 'error',
          message: `Couldn’t load permissions${e?.message ? ` (${e.message})` : ''}. `,
        });
        setAuditLoad({
          phase: 'error',
          message: `Couldn’t load permission activity${e?.message ? ` (${e.message})` : ''}.`,
        });
      }
    }
  }, []);

  useEffect(() => {
    void fetchRules();
  }, [fetchRules]);

  useEffect(() => {
    let cancelled = false;
    setInventoryLoad('loading');
    setInventoryError('');
    fetchToolInventory()
      .then((inventory) => {
        if (!cancelled) {
          setToolInventory(inventory);
          setInventoryLoad(inventory.source === 'runtime' ? 'runtime' : 'fallback');
        }
      })
      .catch((e: any) => {
        if (!cancelled) {
          setToolInventory(null);
          setInventoryError(e?.message || 'Couldn’t load the tool inventory.');
          setInventoryLoad('error');
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const handleEffectChange = useCallback(
    async (rule: PermissionRuleUI, effect: PermissionEffect) => {
      const previous = rules;
      setRules((prev) => prev.map((r) => (r.id === rule.id ? { ...r, effect } : r)));
      setRowError((prev) => ({ ...prev, [rule.id]: '' }));
      try {
        await updatePermissionRule(rule.id, { effect });
        const toolName = toolScopedRuleToolName(rule);
        if (toolName) markToolSaved(toolName);
        announce(
          `${subjectPresentation(rule.subject).label}: ${EFFECT_LABELS[effect]}`,
        );
      } catch (e: any) {
        setRules(previous);
        setRowError((prev) => ({
          ...prev,
          [rule.id]: e?.message || 'Couldn’t change this permission.',
        }));
      }
    },
    [rules, announce, markToolSaved],
  );

  const handleRevoke = useCallback(
    async (rule: PermissionRuleUI) => {
      const previous = rules;
      setRules((prev) => prev.filter((r) => r.id !== rule.id));
      const scopedToolName = toolScopedRuleToolName(rule);
      if (scopedToolName) {
        const timer = savedTimersRef.current.get(scopedToolName);
        if (timer) {
          window.clearTimeout(timer);
          savedTimersRef.current.delete(scopedToolName);
        }
        setRecentlySavedTools((prev) => {
          const next = new Set(prev);
          next.delete(scopedToolName);
          return next;
        });
      }
      try {
        await deletePermissionRule(rule.id);
        announce(`Removed: ${subjectPresentation(rule.subject).label}`);
      } catch (e: any) {
        setRules(previous);
        setRowError((prev) => ({
          ...prev,
          [rule.id]: e?.message || 'Couldn’t remove this permission.',
        }));
      }
    },
    [rules, announce],
  );

  const [busyTools, setBusyTools] = useState<Set<string>>(() => new Set());

  /**
   * Direct edit on a tool that has no tool-scoped rule yet: pin the chosen
   * behavior as a custom global rule. (Editing an existing rule goes through
   * handleEffectChange instead.)
   */
  const handleCreateToolRule = useCallback(
    async (
      tool: ConnectionToolInfo,
      connectionId: ConnectionCatalogId | null,
      effect: PermissionEffect,
    ) => {
      const errKey = `tool:${tool.name}`;
      setBusyTools((prev) => new Set(prev).add(tool.name));
      setRowError((prev) => ({ ...prev, [errKey]: '' }));
      try {
        const rule = await createPermissionRule({
          category: tool.category,
          effect,
          scope: 'global',
          subject: permissionSubjectForToolRule(tool, connectionId),
        });
        if (rule) {
          setRules((prev) => [...prev, rule]);
          const toolName = toolScopedRuleToolName(rule);
          if (toolName) markToolSaved(toolName);
          announce(`${tool.label}: ${EFFECT_LABELS[effect]}`);
        }
      } catch (e: any) {
        setRowError((prev) => ({
          ...prev,
          [errKey]: e?.message || 'Couldn’t change this permission.',
        }));
      } finally {
        setBusyTools((prev) => {
          const next = new Set(prev);
          next.delete(tool.name);
          return next;
        });
      }
    },
    [announce, markToolSaved],
  );

  // Bulk actions — one scope at a time; success messages auto-clear, errors
  // persist until the next attempt so failures can't be missed.
  const [bulkStatus, setBulkStatus] = useState<Record<string, BulkStatus>>({});
  const [bulkRunningScope, setBulkRunningScope] = useState<string | null>(null);
  const bulkDoneTimersRef = useRef<Map<string, number>>(new Map());

  useEffect(() => {
    return () => {
      bulkDoneTimersRef.current.forEach((timer) => window.clearTimeout(timer));
      bulkDoneTimersRef.current.clear();
    };
  }, []);

  const finishBulk = useCallback(
    (scopeId: string, phase: 'done' | 'error', message: string) => {
      const existing = bulkDoneTimersRef.current.get(scopeId);
      if (existing) {
        window.clearTimeout(existing);
        bulkDoneTimersRef.current.delete(scopeId);
      }
      setBulkStatus((prev) => ({ ...prev, [scopeId]: { phase, message } }));
      if (phase === 'done') {
        const timer = window.setTimeout(() => {
          bulkDoneTimersRef.current.delete(scopeId);
          setBulkStatus((prev) => ({ ...prev, [scopeId]: { phase: 'idle' } }));
        }, 8000);
        bulkDoneTimersRef.current.set(scopeId, timer);
      }
    },
    [],
  );

  /**
   * Set every tool in scope to one behavior (Off / Ask / Always allow) — the
   * group- and global-level counterpart of the per-row segmented control.
   *
   * Semantics, chosen so bulk changes stay predictable and auditable:
   *  - connection-wide rules covering the scope are patched to the target
   *    effect (instead of being shadowed by dozens of per-tool pins);
   *  - existing tool rules are updated in place;
   *  - tools running on a built-in default get a pinned global rule;
   *  - tools already behaving as requested are skipped, so no needless
   *    "Custom" pins appear;
   *  - thread-scoped rules are never touched — they belong to a single chat.
   * When the page is filtered, callers pass `connectionIds: []` so only the
   * visible tools get explicit rules and no connection-wide rule is widened.
   */
  const handleBulkSetEffect = useCallback(
    async (
      scopeId: string,
      scopeLabel: string,
      targets: BulkTarget[],
      connectionIds: ConnectionCatalogId[],
      effect: PermissionEffect,
    ) => {
      if (bulkRunningScope) return;
      const connectionRuleIds = new Set(
        targets
          .filter(
            (target) =>
              target.connectionId &&
              connectionIds.includes(target.connectionId) &&
              supportsConnectionWidePermissionRule(target.tool),
          )
          .map((target) => target.connectionId as ConnectionCatalogId),
      );
      const connectorOps = rules.filter((rule) => {
        if (isThreadScope(rule.scope) || rule.subject.kind !== 'connector') return false;
        if (rule.effect === effect) return false;
        const id = connectionIdForConnector(rule.subject.connector);
        return id !== null && connectionRuleIds.has(id);
      });
      const toolSetOps = rules.filter((rule) => {
        if (isThreadScope(rule.scope) || rule.subject.kind !== 'tool_set') return false;
        if (rule.effect === effect) return false;
        return targets.some(
          (target) =>
            target.durableToolSet &&
            toolSetRuleMatchesScopeTool(rule, target.tool, target.connectionId),
        );
      });
      const patchedConnectorIds = new Set(connectorOps.map((rule) => rule.id));
      const patchedToolSetIds = new Set(toolSetOps.map((rule) => rule.id));
      const updates: Array<{ tool: ConnectionToolInfo; rule: PermissionRuleUI }> = [];
      const creates: BulkTarget[] = [];
      const toolSetCreates = new Map<
        string,
        {
          toolSetId: string;
          connectorId?: string;
          category: PermissionCategory;
          toolNames: string[];
        }
      >();
      // Where connection-wide rules are in play, pin a whole category with ONE
      // connector rule instead of N per-tool pins — one line in the audit and
      // one thing to reset, instead of a flood of Custom rows.
      const connectorCreates = new Map<
        string,
        { connectionId: ConnectionCatalogId; category: PermissionCategory; toolNames: string[] }
      >();
      let already = 0;
      for (const target of targets) {
        const effective = target.connectionId
          ? resolveToolPermission(target.tool, target.connectionId, rules)
          : resolveStandaloneToolPermission(target.tool, rules);
        if (effective.effect === effect) {
          already += 1;
        } else if (effective.source === 'tool-rule' && effective.rule) {
          updates.push({ tool: target.tool, rule: effective.rule });
        } else if (
          effective.source === 'connector-rule' &&
          effective.rule &&
          patchedConnectorIds.has(effective.rule.id)
        ) {
          // Covered by patching the connection-wide rule above.
        } else if (
          effective.source === 'tool-set-rule' &&
          effective.rule &&
          patchedToolSetIds.has(effective.rule.id)
        ) {
          // Covered by patching the tool-set rule above.
        } else if (
          target.connectionId &&
          connectionIds.includes(target.connectionId) &&
          supportsConnectionWidePermissionRule(target.tool)
        ) {
          const key = `${target.connectionId}:${target.tool.category}`;
          const entry = connectorCreates.get(key) ?? {
            connectionId: target.connectionId,
            category: target.tool.category,
            toolNames: [],
          };
          entry.toolNames.push(target.tool.name);
          connectorCreates.set(key, entry);
        } else if (target.durableToolSet && target.tool.toolSetId) {
          const connectorId = target.tool.connectorId || undefined;
          const key = `${target.tool.toolSetId}:${connectorId || ''}:${target.tool.category}`;
          const entry = toolSetCreates.get(key) ?? {
            toolSetId: target.tool.toolSetId,
            connectorId,
            category: target.tool.category,
            toolNames: [],
          };
          entry.toolNames.push(target.tool.name);
          toolSetCreates.set(key, entry);
        } else {
          creates.push(target);
        }
      }
      const effectLabel = EFFECT_LABELS[effect];
      const ops: Array<{ toolNames?: string[]; run: () => Promise<void> }> = [
        ...connectorOps.map((rule) => ({
          run: async () => {
            const updated = await updatePermissionRule(rule.id, { effect });
            setRules((prev) =>
              prev.map((r) => (r.id === rule.id ? updated ?? { ...r, effect } : r)),
            );
          },
        })),
        ...toolSetOps.map((rule) => ({
          run: async () => {
            const updated = await updatePermissionRule(rule.id, { effect });
            setRules((prev) =>
              prev.map((r) => (r.id === rule.id ? updated ?? { ...r, effect } : r)),
            );
          },
        })),
        ...updates.map(({ tool, rule }) => ({
          toolNames: [tool.name],
          run: async () => {
            const updated = await updatePermissionRule(rule.id, { effect });
            setRules((prev) =>
              prev.map((r) => (r.id === rule.id ? updated ?? { ...r, effect } : r)),
            );
            markToolSaved(tool.name);
          },
        })),
        ...Array.from(connectorCreates.values()).map((entry) => ({
          toolNames: entry.toolNames,
          run: async () => {
            const created = await createPermissionRule({
              category: entry.category,
              effect,
              scope: 'global',
              subject: { kind: 'connector', connector: entry.connectionId },
            });
            if (created) setRules((prev) => [...prev, created]);
            entry.toolNames.forEach((name) => markToolSaved(name));
          },
        })),
        ...Array.from(toolSetCreates.values()).map((entry) => ({
          toolNames: entry.toolNames,
          run: async () => {
            const created = await createPermissionRule({
              category: entry.category,
              effect,
              scope: 'global',
              subject: {
                kind: 'tool_set',
                tool_set_id: entry.toolSetId,
                ...(entry.connectorId ? { connector: entry.connectorId } : {}),
              },
            });
            if (created) setRules((prev) => [...prev, created]);
            entry.toolNames.forEach((name) => markToolSaved(name));
          },
        })),
        ...creates.map((target) => ({
          toolNames: [target.tool.name],
          run: async () => {
            const created = await createPermissionRule({
              category: target.tool.category,
              effect,
              scope: 'global',
              subject: permissionSubjectForToolRule(target.tool, target.connectionId),
            });
            if (created) setRules((prev) => [...prev, created]);
            markToolSaved(target.tool.name);
          },
        })),
      ];
      if (ops.length === 0) {
        finishBulk(scopeId, 'done', `Every tool here is already set to ${effectLabel}.`);
        announce(`${scopeLabel}: every tool is already set to ${effectLabel}.`);
        return;
      }
      setBulkRunningScope(scopeId);
      setBulkStatus((prev) => ({ ...prev, [scopeId]: { phase: 'running', op: 'set' } }));
      setBusyTools((prev) => {
        const next = new Set(prev);
        ops.forEach((op) => op.toolNames?.forEach((name) => next.add(name)));
        return next;
      });
      announce(`Setting ${targets.length} ${scopeLabel} tools to ${effectLabel}.`);
      let changed = 0;
      let failed = 0;
      await runWithPool(ops, async (op) => {
        try {
          await op.run();
          changed += 1;
        } catch {
          failed += 1;
        } finally {
          if (op.toolNames) {
            const names = op.toolNames;
            setBusyTools((prev) => {
              const next = new Set(prev);
              names.forEach((name) => next.delete(name));
              return next;
            });
          }
        }
      });
      setBulkRunningScope(null);
      if (failed > 0) {
        finishBulk(
          scopeId,
          'error',
          `${failed} of ${ops.length} changes failed — not every tool is set to ${effectLabel}. Try again.`,
        );
        announce(
          `Done with errors. ${changed} set to ${effectLabel}, ${already} already were, ${failed} couldn’t be changed.`,
        );
      } else {
        finishBulk(scopeId, 'done', `All ${targets.length} tools are now set to ${effectLabel}.`);
        announce(`Done. ${changed} set to ${effectLabel}, ${already} already were.`);
      }
    },
    [rules, bulkRunningScope, announce, finishBulk, markToolSaved],
  );

  /** Delete every custom rule in scope so the group falls back to defaults. */
  const handleBulkClear = useCallback(
    async (
      scopeId: string,
      scopeLabel: string,
      toolNames: string[],
      connectionIds: ConnectionCatalogId[],
      scopeTools: ConnectionToolInfo[] = [],
    ) => {
      if (bulkRunningScope) return;
      const targets = clearableRulesFor(rules, new Set(toolNames), connectionIds, scopeTools);
      if (targets.length === 0) {
        finishBulk(scopeId, 'done', 'No custom permissions here.');
        return;
      }
      setBulkRunningScope(scopeId);
      setBulkStatus((prev) => ({ ...prev, [scopeId]: { phase: 'running', op: 'clear' } }));
      announce(
        `Removing ${targets.length} custom ${targets.length === 1 ? 'permission' : 'permissions'} for ${scopeLabel}.`,
      );
      let removed = 0;
      let failed = 0;
      await runWithPool(targets, async (rule) => {
        try {
          await deletePermissionRule(rule.id);
          removed += 1;
          setRules((prev) => prev.filter((r) => r.id !== rule.id));
          const toolName = toolScopedRuleToolName(rule);
          if (toolName) {
            const timer = savedTimersRef.current.get(toolName);
            if (timer) {
              window.clearTimeout(timer);
              savedTimersRef.current.delete(toolName);
            }
            setRecentlySavedTools((prev) => {
              const next = new Set(prev);
              next.delete(toolName);
              return next;
            });
          }
        } catch {
          failed += 1;
        }
      });
      setBulkRunningScope(null);
      if (failed > 0) {
        finishBulk(
          scopeId,
          'error',
          `Removed ${removed} of ${targets.length} — ${failed} couldn’t be removed. Try again.`,
        );
        announce(
          `Done with errors. Removed ${removed} custom permissions, ${failed} couldn’t be removed.`,
        );
      } else {
        finishBulk(
          scopeId,
          'done',
          `Removed ${removed} custom ${removed === 1 ? 'permission' : 'permissions'}.`,
        );
        announce(`Done. Removed ${removed} custom permissions for ${scopeLabel}.`);
      }
    },
    [rules, bulkRunningScope, announce, finishBulk],
  );

  const handleAddFolder = useCallback(
    async () => {
      const picked = await pickLocalDirectory();
      const rule = await createPermissionRule({
        category: 'read',
        effect: 'allow',
        scope: 'global',
        subject: { kind: 'local_location', local_root: picked.path, local_kind: 'folder' },
      });
      if (rule) {
        setRules((prev) => [...prev, rule]);
        announce(`Added ${displayPath(picked.path)}`);
      }
    },
    [announce],
  );

  const handleAddFile = useCallback(
    async () => {
      const picked = await pickLocalFile();
      const rule = await createPermissionRule({
        category: 'read',
        effect: 'allow',
        scope: 'global',
        subject: { kind: 'local_location', local_root: picked.path, local_kind: 'file' },
      });
      if (rule) {
        setRules((prev) => [...prev, rule]);
        announce(`Added ${displayPath(picked.path)}`);
      }
    },
    [announce],
  );

  const handleChangeLocalLocation = useCallback(
    async (rule: PermissionRuleUI) => {
      const kind = localGrantKind(rule.subject);
      const picked = kind === 'file' ? await pickLocalFile() : await pickLocalDirectory();
      const subject: PermissionSubject = {
        ...rule.subject,
        kind: 'local_location',
        local_root: picked.path,
        local_kind: kind,
      };
      const previous = rules;
      setRules((prev) => prev.map((r) => (r.id === rule.id ? { ...r, subject } : r)));
      setRowError((prev) => ({ ...prev, [rule.id]: '' }));
      try {
        const updated = await updatePermissionRule(rule.id, { subject });
        if (updated) setRules((prev) => prev.map((r) => (r.id === rule.id ? updated : r)));
        announce(`Changed ${kind} to ${displayPath(picked.path)}`);
      } catch (e: any) {
        setRules(previous);
        setRowError((prev) => ({
          ...prev,
          [rule.id]: e?.message || `Couldn’t change this ${kind}.`,
        }));
      }
    },
    [announce, rules],
  );

  const connections = useMemo(() => deriveVisibleConnections(settings), [settings]);
  const appToolGroups = toolInventory?.appToolGroups ?? [];
  const runtimeConnectedToolGroups = useMemo(
    () => appToolGroups.filter((group) => group.origin === 'connected'),
    [appToolGroups],
  );
  const appOwnedToolGroups = useMemo(
    () => appToolGroups.filter((group) => group.origin !== 'connected'),
    [appToolGroups],
  );

  const getConnectionTools = useCallback(
    (id: ConnectionCatalogId) =>
      toolInventory ? getToolsForConnectionFromInventory(id, toolInventory) : [],
    [toolInventory],
  );

  const connectionToolCount = useMemo(
    () => connections.reduce((sum, c) => sum + getConnectionTools(c.id).length, 0),
    [connections, getConnectionTools],
  );

  const notAddedConnectionIds = useMemo(() => {
    const visible = new Set(connections.map((c) => c.id));
    return CONNECTION_CATALOG_IDS.filter(
      (id) => !visible.has(id) && getConnectionTools(id).length > 0,
    );
  }, [connections, getConnectionTools]);

  // Search & filters over the tool inventory. While a filter is active,
  // groups are forced open, non-matching rows and empty groups hide, and
  // scope-level controls apply to the matching tools only.
  const [query, setQuery] = useState('');
  const [effectFilter, setEffectFilter] = useState<'any' | PermissionEffect>('any');
  const [riskFilter, setRiskFilter] = useState<'any' | PermissionCategory>('any');
  const [customOnly, setCustomOnly] = useState(false);
  // Controlled so active filters can force sections open without a Radix
  // controlled/uncontrolled flip.
  const [connectionsOpen, setConnectionsOpen] = useState(true);
  const [notAddedOpen, setNotAddedOpen] = useState(false);
  const normalizedQuery = query.trim().toLowerCase();
  const filterActive =
    normalizedQuery !== '' || effectFilter !== 'any' || riskFilter !== 'any' || customOnly;
  const clearFilters = useCallback(() => {
    setQuery('');
    setEffectFilter('any');
    setRiskFilter('any');
    setCustomOnly(false);
  }, []);

  const matchesFilter = useCallback(
    (tool: ConnectionToolInfo, effective: EffectiveToolPermission, groupLabel: string) => {
      if (effectFilter !== 'any' && effective.effect !== effectFilter) return false;
      if (riskFilter !== 'any' && tool.category !== riskFilter) return false;
      if (customOnly && effective.source === 'default') return false;
      if (normalizedQuery) {
        const test = tool.test;
        const presentation = tool.presentation;
        const haystack = [
          tool.label,
          tool.name,
          tool.description,
          tool.toolSetName,
          tool.toolSetId,
          groupLabel,
          presentation?.family,
          presentation?.result_kind,
          presentation?.renderer_strategy,
          presentation?.renderer_key,
          presentation?.native_viewer,
          presentation?.file_capability,
          ...(presentation?.artifact_types || []),
          ...(presentation?.file_kinds || []),
          ...(presentation?.preview_modes || []),
          ...(presentation?.source_refs || []),
          ...(presentation?.native_links || []),
          presentation?.produces_file_artifact ? 'produces file artifact' : '',
          presentation?.promotes_native_input ? 'promotes native input' : '',
          test?.mode,
          test?.input_hint,
          ...(test?.required_arguments || []),
          ...(test?.required_context || []),
        ]
          .filter(Boolean)
          .join(' ')
          .toLowerCase();
        if (!haystack.includes(normalizedQuery)) return false;
      }
      return true;
    },
    [effectFilter, riskFilter, customOnly, normalizedQuery],
  );

  // Every scope's tools resolved once per render pass, keyed by bulk scope id
  // ("conn:github", "app:attachments", "notadded:slack"). The full inventory is
  // always resolved — including not-added connections, whose dormant rules
  // define behavior before the connection becomes available.
  const scopeViews = useMemo(() => {
    const views = new Map<string, ScopeView>();
    const add = (
      scopeId: string,
      groupLabel: string,
      tools: ConnectionToolInfo[],
      connectionId: ConnectionCatalogId | null,
    ) => {
      const resolved = tools.map((tool) => ({
        tool,
        effective: connectionId
          ? resolveToolPermission(tool, connectionId, rules)
          : resolveStandaloneToolPermission(tool, rules),
      }));
      const visible = filterActive
        ? resolved.filter((r) => matchesFilter(r.tool, r.effective, groupLabel))
        : resolved;
      views.set(scopeId, { resolved, visible });
    };
    for (const info of connections) {
      add(`conn:${info.id}`, info.meta.label, getConnectionTools(info.id), info.id);
    }
    for (const id of notAddedConnectionIds) {
      add(`notadded:${id}`, CONNECTION_META[id].label, getConnectionTools(id), id);
    }
    for (const group of appToolGroups) {
      add(`app:${group.id}`, group.title, group.tools, null);
    }
    return views;
  }, [
    rules,
    connections,
    notAddedConnectionIds,
    appToolGroups,
    getConnectionTools,
    filterActive,
    matchesFilter,
  ]);

  // Announce filter results through the one shared live region, debounced so
  // typing in the search box doesn't produce a message per keystroke.
  const matchCountForAnnounce = filterActive
    ? Array.from(scopeViews.values()).reduce((sum, v) => sum + v.visible.length, 0)
    : -1;
  useEffect(() => {
    if (!filterActive) return;
    const timer = window.setTimeout(() => {
      announce(
        matchCountForAnnounce === 0
          ? 'No tools match your filters.'
          : `${matchCountForAnnounce} ${matchCountForAnnounce === 1 ? 'tool' : 'tools'} shown.`,
      );
    }, 400);
    return () => window.clearTimeout(timer);
  }, [filterActive, matchCountForAnnounce, announce]);

  const connectionIdForScope = (scopeId: string): ConnectionCatalogId | null => {
    if (scopeId.startsWith('conn:')) return scopeId.slice(5) as ConnectionCatalogId;
    if (scopeId.startsWith('notadded:')) return scopeId.slice(9) as ConnectionCatalogId;
    return null;
  };

  // Global scope = every tool the app knows, narrowed by active filters.
  const globalView = useMemo(() => {
    const targets: Array<BulkTarget & { effective: EffectiveToolPermission }> = [];
    let total = 0;
    scopeViews.forEach((view, scopeId) => {
      total += view.resolved.length;
      const connectionId = connectionIdForScope(scopeId);
      for (const r of view.visible) {
        targets.push({ tool: r.tool, connectionId, effective: r.effective });
      }
    });
    return { targets, total };
  }, [scopeViews]);

  const globalConnectionIds = useMemo<ConnectionCatalogId[]>(
    () => (filterActive ? [] : [...CONNECTION_CATALOG_IDS]),
    [filterActive],
  );
  const globalSummary = useMemo(
    () => summarizeEffects(globalView.targets.map((t) => t.effective.effect)),
    [globalView],
  );
  const globalCustomCount = useMemo(
    () => globalView.targets.filter((t) => t.effective.source !== 'default').length,
    [globalView],
  );
  const globalRiskyCount = useMemo(
    () => globalView.targets.filter((t) => t.tool.category !== 'read').length,
    [globalView],
  );
  const globalClearableCount = useMemo(
    () =>
      clearableRulesFor(
        rules,
        new Set(globalView.targets.map((t) => t.tool.name)),
        globalConnectionIds,
        globalView.targets.map((t) => t.tool),
      ).length,
    [rules, globalView, globalConnectionIds],
  );

  const {
    folderRules,
    threadRules,
    rulesByConnection,
    rulesByToolSet,
    otherRules,
  } = useMemo(() => {
    const folder: PermissionRuleUI[] = [];
    const thread: PermissionRuleUI[] = [];
    const byConnection = new Map<ConnectionCatalogId, PermissionRuleUI[]>();
    const byToolSet = new Map<string, PermissionRuleUI[]>();
    const other: PermissionRuleUI[] = [];
    const appToolSetIds = new Set((toolInventory?.appToolGroups ?? []).map((group) => group.id));
    for (const rule of rules) {
      if (isThreadScope(rule.scope)) {
        thread.push(rule);
        continue;
      }
      if (rule.subject.kind === 'local_location') {
        folder.push(rule);
        continue;
      }
      // App-tool rules surface inline on their tool rows in the App tools
      // groups, so they never land in "Other custom permissions".
      const scopedToolName = toolScopedRuleToolName(rule);
      if (toolInventory && scopedToolName && isAppToolName(scopedToolName, toolInventory)) {
        continue;
      }
      if (
        rule.subject.kind === 'tool_set' &&
        rule.subject.tool_set_id &&
        appToolSetIds.has(rule.subject.tool_set_id)
      ) {
        const bucket = byToolSet.get(rule.subject.tool_set_id);
        if (bucket) bucket.push(rule);
        else byToolSet.set(rule.subject.tool_set_id, [rule]);
        continue;
      }
      const connectionId = toolInventory ? connectionIdForRule(rule, toolInventory) : null;
      if (connectionId) {
        const bucket = byConnection.get(connectionId);
        if (bucket) bucket.push(rule);
        else byConnection.set(connectionId, [rule]);
      } else {
        other.push(rule);
      }
    }
    return {
      folderRules: folder,
      threadRules: thread,
      rulesByConnection: byConnection,
      rulesByToolSet: byToolSet,
      otherRules: other,
    };
  }, [rules, toolInventory]);

  const localFileTools = useMemo(
    () =>
      (toolInventory?.appToolGroups ?? [])
        .filter((group) => group.origin === 'local' || group.id === 'local-files')
        .flatMap((group) => group.tools)
        .filter((tool) => tool.category === 'read'),
    [toolInventory],
  );

  const rulesAvailable = load.phase === 'ready';

  return (
    <div className="space-y-8 pb-4">
      <div role="status" aria-live="polite" className="sr-only">
        {announcement}
      </div>

      <p className="text-[13px] text-muted-foreground max-w-prose">
        Control what the assistant can do on its own. Anything without a custom
        permission follows the defaults at the bottom of the page; changes
        apply immediately.
      </p>

      {/* Pending */}
      <section aria-labelledby="permissions-pending-heading">
        <h2
          id="permissions-pending-heading"
          className="mb-2 text-[15px] font-semibold text-foreground"
        >
          Pending
        </h2>
        <p className="mb-3 text-[12.5px] text-muted-foreground">
          Requests waiting for your decision, across all conversations.
        </p>
        <PendingApprovalsQueue />
      </section>

      {/* Tools */}
      <section aria-labelledby="permissions-tools-heading">
        <h2
          id="permissions-tools-heading"
          className="mb-1 text-[15px] font-semibold text-foreground"
        >
          Tools
        </h2>
        <p className="mb-3 text-[12.5px] text-muted-foreground">
          Everything the assistant can do, grouped by tool set. Expand a row
          for details on what it does and where its behavior comes from.
        </p>

        {load.phase === 'unsupported' && (
          <p className="mb-3 text-[12.5px] text-muted-foreground">
            The permissions service isn’t available in this build yet, so
            behaviors shown here are the built-in defaults. Custom permissions
            appear once the service is available.
          </p>
        )}
        {load.phase === 'error' && (
          <div className="mb-3 flex flex-wrap items-center gap-2 text-[12.5px] text-muted-foreground">
            <span>{load.message}Behaviors shown are the built-in defaults.</span>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-7 text-[12px]"
              onClick={() => void fetchRules()}
            >
              <RefreshCw size={12} className="mr-1.5" aria-hidden />
              Try again
            </Button>
          </div>
        )}
        {inventoryLoad === 'fallback' && (
          <div className="mb-3 flex items-start gap-2 rounded-md border border-amber-300/70 bg-amber-50 px-3 py-2 text-[12.5px] text-amber-900 dark:border-amber-700/70 dark:bg-amber-950/40 dark:text-amber-100">
            <TriangleAlert size={14} className="mt-0.5 shrink-0" aria-hidden />
            <p>
              Runtime tool inventory could not be loaded, so this page is using
              the built-in fallback. New dynamic tools or connector provenance
              may be missing until inventory is available.
            </p>
          </div>
        )}
        {inventoryLoad === 'error' && (
          <div className="mb-3 flex flex-wrap items-center gap-2 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-[12.5px] text-destructive">
            <TriangleAlert size={14} className="shrink-0" aria-hidden />
            <span>{inventoryError || 'Couldn’t load the tool inventory.'}</span>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="h-7 text-[12px]"
              onClick={() => {
                setInventoryLoad('loading');
                setInventoryError('');
                void fetchToolInventory()
                  .then((inventory) => {
                    setToolInventory(inventory);
                    setInventoryLoad(inventory.source === 'runtime' ? 'runtime' : 'fallback');
                  })
                  .catch((e: any) => {
                    setToolInventory(null);
                    setInventoryError(e?.message || 'Couldn’t load the tool inventory.');
                    setInventoryLoad('error');
                  });
              }}
            >
              <RefreshCw size={12} className="mr-1.5" aria-hidden />
              Try again
            </Button>
          </div>
        )}

        {load.phase === 'loading' || inventoryLoad === 'loading' ? (
          <div className="space-y-2" aria-hidden>
            <Skeleton className="h-24 w-full rounded-md" />
            <Skeleton className="h-24 w-full rounded-md" />
          </div>
        ) : inventoryLoad === 'error' || !toolInventory ? (
          <div className="rounded-md border border-border bg-muted/30 px-3 py-4 text-[13px] text-muted-foreground">
            Tool permissions can’t be shown until the tool inventory is
            available.
          </div>
        ) : (
          <div className="space-y-5">
            <ToolFilterToolbar
              query={query}
              onQueryChange={setQuery}
              effectFilter={effectFilter}
              onEffectFilterChange={setEffectFilter}
              riskFilter={riskFilter}
              onRiskFilterChange={setRiskFilter}
              customOnly={customOnly}
              onCustomOnlyChange={setCustomOnly}
              filterActive={filterActive}
              matchCount={globalView.targets.length}
              totalCount={globalView.total}
              onClear={clearFilters}
            />

            <GlobalScopeCard
              filterActive={filterActive}
              matchCount={globalView.targets.length}
              totalCount={globalView.total}
              summary={globalSummary}
              customCount={globalCustomCount}
              riskyCount={globalRiskyCount}
              clearableCount={globalClearableCount}
              rulesAvailable={rulesAvailable}
              runningScope={bulkRunningScope}
              status={bulkStatus['all']}
              onSetEffect={(effect) =>
                handleBulkSetEffect(
                  'all',
                  filterActive ? 'matching' : 'inventory',
                  globalView.targets,
                  globalConnectionIds,
                  effect,
                )
              }
              onClear={() =>
                handleBulkClear(
                  'all',
                  filterActive ? 'matching tools' : 'inventory',
                  globalView.targets.map((t) => t.tool.name),
                  globalConnectionIds,
                  globalView.targets.map((t) => t.tool),
                )
              }
            />

            {filterActive && globalView.targets.length === 0 ? (
              <div className="rounded-md border border-dashed border-border px-3 py-6 text-center text-[12.5px] text-muted-foreground">
                No tools match these filters.{' '}
                <button
                  type="button"
                  className="rounded-sm text-foreground underline underline-offset-2 hover:no-underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  onClick={clearFilters}
                >
                  Clear filters
                </button>
              </div>
            ) : (
              <div className="space-y-6">
                {/* Your connections — minimizable drawer, a filtered lens */}
                {(!filterActive ||
                  connections.some(
                    (c) => (scopeViews.get(`conn:${c.id}`)?.visible.length ?? 0) > 0,
                  )) && (
                  <div>
                    <Collapsible
                      open={filterActive ? true : connectionsOpen}
                      onOpenChange={setConnectionsOpen}
                    >
                      <h3>
                        <CollapsibleTrigger asChild>
                          <button
                            type="button"
                            className="group/coll flex w-full items-center gap-2 rounded-md border border-border bg-muted/30 px-3 py-2 text-left hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            <CollapseChevron size={13} />
                            <span className="min-w-0 flex-1 truncate text-[13px] font-medium text-foreground">
                              Your connections
                            </span>
                            <span className="shrink-0 text-[11px] text-muted-foreground">
                              {connections.length}{' '}
                              {connections.length === 1 ? 'connection' : 'connections'} ·{' '}
                              {connectionToolCount} {connectionToolCount === 1 ? 'tool' : 'tools'}
                            </span>
                          </button>
                        </CollapsibleTrigger>
                      </h3>
                      <CollapsibleContent>
                        {connections.length === 0 ? (
                          <div className="mt-2 flex flex-wrap items-center gap-2 rounded-md border border-dashed border-border px-3 py-3 text-[12.5px] text-muted-foreground">
                            <span>
                              No connections added yet. Tools appear here once you
                              add a connection.
                            </span>
                            {onOpenConnections && (
                              <Button
                                type="button"
                                variant="outline"
                                size="sm"
                                className="h-7 text-[12px]"
                                onClick={onOpenConnections}
                              >
                                Open Connections
                                <ChevronRight size={12} className="ml-1" aria-hidden />
                              </Button>
                            )}
                          </div>
                        ) : (
                          <div className="mt-2 space-y-3">
                            {connections.map((info) => (
                              <ToolGroupCard
                                key={info.id}
                                scopeId={`conn:${info.id}`}
                                title={info.meta.label}
                                subtitle={info.identity || undefined}
                                logoMeta={info.meta}
                                origin={info.readiness === 'ready' ? 'connected' : 'needs-setup'}
                                setupHint={info.setupHint}
                                view={scopeViews.get(`conn:${info.id}`)!}
                                connectionId={info.id}
                                connectionRules={rulesByConnection.get(info.id) || []}
                                filterActive={filterActive}
                                allRules={rules}
                                rulesAvailable={rulesAvailable}
                                rowError={rowError}
                                busyTools={busyTools}
                                recentlySavedTools={recentlySavedTools}
                                settings={settings}
                                toolTestContext={toolTestContext}
                                bulkRunningScope={bulkRunningScope}
                                bulkStatus={bulkStatus}
                                onBulkSetEffect={handleBulkSetEffect}
                                onBulkClear={handleBulkClear}
                                onEffectChange={handleEffectChange}
                                onRevoke={handleRevoke}
                                onCreateToolRule={handleCreateToolRule}
                                onOpenConnections={onOpenConnections}
                              />
                            ))}
                          </div>
                        )}
                      </CollapsibleContent>
                    </Collapsible>
                  </div>
                )}

                {/* Runtime-connected tool sets not represented in the static connection catalogue */}
                {(!filterActive ||
                  runtimeConnectedToolGroups.some(
                    (g) => (scopeViews.get(`app:${g.id}`)?.visible.length ?? 0) > 0,
                  )) &&
                  runtimeConnectedToolGroups.length > 0 && (
                  <div>
                    <h3 className="mb-1 text-[13px] font-medium text-foreground">
                      Connected tool sets
                    </h3>
                    <p className="mb-2 text-[11.5px] text-muted-foreground">
                      Runtime connections that are available but not part of the static
                      Connections catalogue.
                    </p>
                    <div className="space-y-3">
                      {runtimeConnectedToolGroups.map((group) => (
                        <ToolGroupCard
                          key={group.id}
                          scopeId={`app:${group.id}`}
                          title={group.title}
                          subtitle={group.description}
                          origin="connected"
                          view={scopeViews.get(`app:${group.id}`)!}
                          connectionId={null}
                          connectionRules={rulesByToolSet.get(group.id) || []}
                          filterActive={filterActive}
                          allRules={rules}
                          rulesAvailable={rulesAvailable}
                          rowError={rowError}
                          busyTools={busyTools}
                          recentlySavedTools={recentlySavedTools}
                          settings={settings}
                          toolTestContext={toolTestContext}
                          bulkRunningScope={bulkRunningScope}
                          bulkStatus={bulkStatus}
                          onBulkSetEffect={handleBulkSetEffect}
                          onBulkClear={handleBulkClear}
                          onEffectChange={handleEffectChange}
                          onRevoke={handleRevoke}
                          onCreateToolRule={handleCreateToolRule}
                        />
                      ))}
                    </div>
                  </div>
                )}

                {/* App and provider tools — not managed from the Connections list */}
                {appOwnedToolGroups.length > 0 &&
                  (!filterActive ||
                    appOwnedToolGroups.some(
                      (g) => (scopeViews.get(`app:${g.id}`)?.visible.length ?? 0) > 0,
                    )) && (
                  <div>
                    <h3 className="mb-1 text-[13px] font-medium text-foreground">
                      App and provider tools
                    </h3>
                    <p className="mb-2 text-[11.5px] text-muted-foreground">
                      Built-in app tools and provider-native capabilities that are not managed as user connections.
                    </p>
                    <div className="space-y-3">
                      {appOwnedToolGroups.map((group) => (
                        <ToolGroupCard
                          key={group.id}
                          scopeId={`app:${group.id}`}
                          title={group.title}
                          subtitle={group.description}
                          origin={
                            group.origin === 'local'
                              ? 'local'
                              : group.origin === 'provider'
                                ? 'provider'
                                : 'builtin'
                          }
                          view={scopeViews.get(`app:${group.id}`)!}
                          connectionId={null}
                          connectionRules={rulesByToolSet.get(group.id) || []}
                          filterActive={filterActive}
                          allRules={rules}
                          rulesAvailable={rulesAvailable}
                          rowError={rowError}
                          busyTools={busyTools}
                          recentlySavedTools={recentlySavedTools}
                          settings={settings}
                          toolTestContext={toolTestContext}
                          bulkRunningScope={bulkRunningScope}
                          bulkStatus={bulkStatus}
                          onBulkSetEffect={handleBulkSetEffect}
                          onBulkClear={handleBulkClear}
                          onEffectChange={handleEffectChange}
                          onRevoke={handleRevoke}
                          onCreateToolRule={handleCreateToolRule}
                        />
                      ))}
                    </div>
                  </div>
                )}

                {/* Connections not currently added — kept so the inventory is complete */}
                {notAddedConnectionIds.length > 0 &&
                  (!filterActive ||
                    notAddedConnectionIds.some(
                      (id) => (scopeViews.get(`notadded:${id}`)?.visible.length ?? 0) > 0,
                    )) && (
                    <Collapsible
                      open={filterActive ? true : notAddedOpen}
                      onOpenChange={setNotAddedOpen}
                    >
                      <h3>
                        <CollapsibleTrigger asChild>
                          <button
                            type="button"
                            className="group/coll inline-flex min-h-6 items-center gap-1 rounded-sm text-[12px] text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                          >
                            <CollapseChevron size={12} />
                            {`Not in your connections — ${notAddedConnectionIds.length} ${notAddedConnectionIds.length === 1 ? 'connection' : 'connections'}`}
                          </button>
                        </CollapsibleTrigger>
                      </h3>
                      <CollapsibleContent>
                        <p className="mt-1.5 text-[11.5px] text-muted-foreground">
                          Their tools stay locked until you add the connection in
                          Settings → Connections.
                        </p>
                        <div className="mt-2 space-y-3">
                          {notAddedConnectionIds.map((id) => (
                            <ToolGroupCard
                              key={id}
                              scopeId={`notadded:${id}`}
                              title={CONNECTION_META[id].label}
                              logoMeta={CONNECTION_META[id]}
                              origin="not-added"
                              view={scopeViews.get(`notadded:${id}`)!}
                              connectionId={id}
                              connectionRules={rulesByConnection.get(id) || []}
                              filterActive={filterActive}
                              allRules={rules}
                              rulesAvailable={rulesAvailable}
                              rowError={rowError}
                              busyTools={busyTools}
                              recentlySavedTools={recentlySavedTools}
                              settings={settings}
                              toolTestContext={toolTestContext}
                              bulkRunningScope={bulkRunningScope}
                              bulkStatus={bulkStatus}
                              onBulkSetEffect={handleBulkSetEffect}
                              onBulkClear={handleBulkClear}
                              onEffectChange={handleEffectChange}
                              onRevoke={handleRevoke}
                              onCreateToolRule={handleCreateToolRule}
                              onOpenConnections={onOpenConnections}
                            />
                          ))}
                        </div>
                      </CollapsibleContent>
                    </Collapsible>
                  )}
              </div>
            )}
          </div>
        )}
      </section>

      {/* Read > Local files */}
      {load.phase === 'ready' && (
        <section aria-labelledby="permissions-folders-heading">
          <h2
            id="permissions-folders-heading"
            className="mb-1 text-[15px] font-semibold text-foreground"
          >
            Read &gt; Local files
          </h2>
          <p className="mb-3 text-[12.5px] text-muted-foreground">
            Local file access is managed through Connections &gt; Local Computer. The assistant
            can only search and read inside that configured scope.
          </p>
          <LocalFilesToolsSummary tools={localFileTools} rules={rules} />
          <div className="mb-2 flex flex-wrap items-center justify-between gap-2 rounded-md border border-border bg-muted/25 px-3 py-2">
            <p className="text-[12.5px] text-muted-foreground">
              Use Local Computer to choose Whole computer or selected folders. Local file
              requests no longer create chat approval cards.
            </p>
            {onOpenConnections && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="h-7 text-[12px]"
                onClick={onOpenConnections}
              >
                Manage Local Computer
              </Button>
            )}
          </div>
        </section>
      )}

      {/* Other custom permissions (not tied to a visible connection) */}
      {load.phase === 'ready' && otherRules.length > 0 && (
        <section aria-labelledby="permissions-other-heading">
          <h2
            id="permissions-other-heading"
            className="mb-1 text-[15px] font-semibold text-foreground"
          >
            Other custom permissions
          </h2>
          <p className="mb-3 text-[12.5px] text-muted-foreground">
            Custom rules for tools or connections this app no longer knows about. Safe
            to remove — the assistant simply asks again if one is ever needed.
          </p>
          <ul className="space-y-1.5">
            {otherRules.map((rule) => (
              <PermissionRuleRow
                key={rule.id}
                rule={rule}
                error={rowError[rule.id]}
                onEffectChange={(effect) => handleEffectChange(rule, effect)}
                onRevoke={() => handleRevoke(rule)}
              />
            ))}
          </ul>
        </section>
      )}

      {/* This chat only */}
      {load.phase === 'ready' && threadRules.length > 0 && (
        <section aria-label="Permissions for single chats">
          <Collapsible>
            <CollapsibleTrigger asChild>
              <button
                type="button"
                className="group/coll inline-flex min-h-6 items-center gap-1 text-[12px] text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-sm"
              >
                <CollapseChevron size={12} />
                {`This chat only — ${threadRules.length} ${threadRules.length === 1 ? 'permission' : 'permissions'}`}
              </button>
            </CollapsibleTrigger>
            <CollapsibleContent>
              <ul className="mt-2 space-y-1.5">
                {threadRules.map((rule) => (
                  <FolderRuleRow
                    key={rule.id}
                    rule={rule}
                    error={rowError[rule.id]}
                    onEffectChange={(effect) => handleEffectChange(rule, effect)}
                    onChangeLocation={() => handleChangeLocalLocation(rule)}
                    onRevoke={() => handleRevoke(rule)}
                    scopeNote="This chat only"
                  />
                ))}
              </ul>
            </CollapsibleContent>
          </Collapsible>
        </section>
      )}

      {/* Defaults — informational, full-contrast (not disabled styling) */}
      <section aria-labelledby="permissions-defaults-heading">
        <h2
          id="permissions-defaults-heading"
          className="mb-1 text-[15px] font-semibold text-foreground"
        >
          Defaults
        </h2>
        <p className="mb-3 text-[12.5px] text-muted-foreground">
          What happens when nothing above says otherwise.
        </p>
        <ul className="space-y-1">
          {DEFAULT_ROWS.map((row) => {
            const meta = CATEGORY_META[row.category];
            const Icon = meta.icon;
            return (
              <li
                key={`${row.category}-${row.label}`}
                className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 rounded-md border border-dashed border-border px-3 py-2 text-[12.5px]"
              >
                <span className="inline-flex w-[72px] shrink-0 items-center gap-1.5 text-muted-foreground">
                  <Icon size={12} aria-hidden />
                  {meta.label}
                </span>
                <span className="min-w-0 flex-1 text-foreground">{row.label}</span>
                <span className="text-muted-foreground">{row.effect}</span>
              </li>
            );
          })}
        </ul>
      </section>

      <AuditSection
        records={auditRecords}
        load={auditLoad}
        onRetry={fetchRules}
      />
    </div>
  );
};

// ---------------------------------------------------------------------------
// Connections & tools
// ---------------------------------------------------------------------------

/** Bulk plumbing shared by every tool group. */
interface BulkActionProps {
  bulkRunningScope: string | null;
  bulkStatus: Record<string, BulkStatus>;
  onBulkSetEffect: (
    scopeId: string,
    scopeLabel: string,
    targets: BulkTarget[],
    connectionIds: ConnectionCatalogId[],
    effect: PermissionEffect,
  ) => void;
  onBulkClear: (
    scopeId: string,
    scopeLabel: string,
    toolNames: string[],
    connectionIds: ConnectionCatalogId[],
    scopeTools?: ConnectionToolInfo[],
  ) => void;
}

/** The segmented value a scope summary maps to ('mixed' selects nothing). */
function summaryValue(summary: EffectSummary): PermissionEffect | 'mixed' {
  switch (summary.kind) {
    case 'off':
      return 'deny';
    case 'ask':
      return 'ask';
    case 'allow':
      return 'allow';
    default:
      return 'mixed';
  }
}

/** Collapse chevron: points right when closed, rotates open with its OWN
 * trigger's state (scoped via the group/coll class on the trigger button, so
 * an open ancestor collapsible can't rotate a collapsed child's chevron). */
const CollapseChevron: React.FC<{ size?: number }> = ({ size = 13 }) => (
  <ChevronRight
    size={size}
    aria-hidden
    className="shrink-0 text-muted-foreground transition-transform motion-reduce:transition-none group-data-[state=open]/coll:rotate-90"
  />
);

/** Risk chip: category label that always carries its tint (never color-only). */
const RiskChip: React.FC<{ category: PermissionCategory }> = ({ category }) => {
  const meta = CATEGORY_META[category];
  const Icon = meta.icon;
  return (
    <span
      className={cn(
        'inline-flex w-[52px] shrink-0 items-center justify-center gap-1 rounded border px-1 py-px text-[9.5px] font-semibold uppercase tracking-wide',
        RISK_CHIP_CLASS[category],
      )}
      title={`${meta.chipLabel} — ${RISK_MEANING[category]}`}
    >
      <Icon size={10} aria-hidden />
      {meta.chipLabel}
    </span>
  );
};

const RiskLegend: React.FC = () => (
  <span className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
    {(['read', 'write', 'execute'] as PermissionCategory[]).map((category) => (
      <span key={category} className="inline-flex items-center gap-1.5">
        <RiskChip category={category} />
        {RISK_MEANING[category]}
      </span>
    ))}
  </span>
);

/** Origin/status of a tool set: Connected, Needs setup, Not added, Built-in, Local. */
const OriginBadge: React.FC<{ kind: OriginKind }> = ({ kind }) => {
  const meta = ORIGIN_BADGE[kind];
  return (
    <span
      className={cn(
        'inline-flex shrink-0 items-center gap-1 rounded-full border px-2 py-0.5 text-[10.5px] font-medium',
        meta.className,
      )}
      title={meta.description}
    >
      {meta.locked && <Lock size={10} aria-hidden />}
      {meta.label}
    </span>
  );
};

const SUMMARY_BADGE_META: Record<
  Exclude<EffectSummary['kind'], 'none'>,
  { label: string; className: string }
> = {
  off: {
    label: 'All off',
    className: 'border-destructive/40 bg-destructive/10 text-destructive',
  },
  ask: { label: 'All ask', className: 'border-border bg-muted/60 text-foreground' },
  allow: {
    label: 'All always',
    className:
      'border-emerald-600/40 bg-emerald-500/10 text-emerald-700 dark:border-emerald-400/40 dark:text-emerald-400',
  },
  mixed: { label: 'Mixed', className: 'border-border bg-muted/60 text-muted-foreground' },
};

/** Aggregate state next to a scope control ("All ask", "Mixed", …). */
const ScopeSummaryBadge: React.FC<{ summary: EffectSummary; id?: string }> = ({
  summary,
  id,
}) => {
  if (summary.kind === 'none') return null;
  const meta = SUMMARY_BADGE_META[summary.kind];
  const detail = summaryBreakdown(summary);
  return (
    <span
      id={id}
      className={cn(
        'inline-flex shrink-0 items-center rounded-full border px-2 py-0.5 text-[10.5px] font-medium',
        meta.className,
      )}
      title={detail}
    >
      {meta.label}
      {detail && <span className="sr-only"> — {detail}</span>}
    </span>
  );
};

/**
 * Selected segment: one strong neutral fill for every effect. Most rows sit
 * at their default, so semantic fills would flood the list with color and
 * drown the two signals that matter — risk chips and Custom pills. The
 * summary badges and caution markers carry the semantic tints instead.
 */
const SELECTED_SEGMENT_CLASS = 'bg-foreground text-background';

/**
 * The one permission control used at every scope: Off / Ask / Always allow.
 * At group/global scope `value` may be 'mixed', which selects nothing — mixed
 * is a summary, never a fourth state to pick.
 */
const EffectSegments: React.FC<{
  value: PermissionEffect | 'mixed';
  /** Accessible name for the whole control, naming the blast radius. */
  label: string;
  /**
   * Appended to each button's aria-label — a screen-reader user landing on a
   * button hears the button, not the group, so the blast radius must ride on
   * the button itself (" for all 13 GitHub tools").
   */
  segmentSuffix?: string;
  /** id of the scope-summary text, referenced from every button. */
  describedBy?: string;
  size?: 'sm' | 'md';
  busy?: boolean;
  disabled?: boolean;
  /** Why the control is inert, surfaced as a tooltip and to AT. */
  title?: string;
  allowButtonRef?: React.Ref<HTMLButtonElement>;
  testIdPrefix?: string;
  onChange: (effect: PermissionEffect) => void;
}> = ({
  value,
  label,
  segmentSuffix,
  describedBy,
  size = 'sm',
  busy,
  disabled,
  title,
  allowButtonRef,
  testIdPrefix,
  onChange,
}) => {
  const inert = busy || disabled;
  const reasonId = React.useId();
  const describedByIds =
    [describedBy, title ? reasonId : undefined].filter(Boolean).join(' ') || undefined;
  return (
    <div
      role="group"
      aria-label={label}
      className={cn(
        'flex shrink-0 items-center rounded-md border border-border bg-muted/40 p-0.5',
        inert && 'opacity-60',
      )}
    >
      {title && (
        <span id={reasonId} className="sr-only">
          {title}
        </span>
      )}
      {EFFECT_SEGMENTS.map((seg) => {
        const selected = seg.value === value;
        return (
          <button
            key={seg.value}
            ref={seg.value === 'allow' ? allowButtonRef : undefined}
            type="button"
            data-testid={testIdPrefix ? `${testIdPrefix}-${seg.value}` : undefined}
            aria-pressed={selected}
            aria-label={
              seg.aria +
              (segmentSuffix ?? '') +
              (value === 'mixed' ? ' (currently mixed)' : '')
            }
            aria-describedby={describedByIds}
            aria-disabled={inert || undefined}
            aria-busy={busy || undefined}
            title={title ?? seg.aria}
            className={cn(
              'rounded-[4px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
              size === 'md'
                ? 'px-2.5 py-1 text-[11.5px] leading-4'
                : 'px-1.5 py-0.5 text-[10.5px] leading-4',
              selected
                ? cn('font-semibold shadow-sm', SELECTED_SEGMENT_CLASS)
                : 'text-muted-foreground hover:text-foreground',
            )}
            onClick={() => {
              if (!inert && !selected) onChange(seg.value);
            }}
          >
            {seg.label}
          </button>
        );
      })}
    </div>
  );
};

/** Search + filters over the whole inventory, with the risk legend beneath. */
const ToolFilterToolbar: React.FC<{
  query: string;
  onQueryChange: (value: string) => void;
  effectFilter: 'any' | PermissionEffect;
  onEffectFilterChange: (value: 'any' | PermissionEffect) => void;
  riskFilter: 'any' | PermissionCategory;
  onRiskFilterChange: (value: 'any' | PermissionCategory) => void;
  customOnly: boolean;
  onCustomOnlyChange: (value: boolean) => void;
  filterActive: boolean;
  matchCount: number;
  totalCount: number;
  onClear: () => void;
}> = ({
  query,
  onQueryChange,
  effectFilter,
  onEffectFilterChange,
  riskFilter,
  onRiskFilterChange,
  customOnly,
  onCustomOnlyChange,
  filterActive,
  matchCount,
  totalCount,
  onClear,
}) => (
  <div>
    <div className="flex flex-wrap items-center gap-2">
      <div className="relative min-w-[160px] max-w-xs flex-1">
        <Search
          size={13}
          aria-hidden
          className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground"
        />
        <Input
          type="search"
          value={query}
          placeholder="Search tools"
          aria-label="Search tools by name or tool set"
          className="h-8 pl-8 text-[12.5px]"
          onChange={(e) => onQueryChange(e.target.value)}
        />
      </div>
      <Select
        value={effectFilter}
        onValueChange={(v) => onEffectFilterChange(v as 'any' | PermissionEffect)}
      >
        <SelectTrigger className="h-8 w-[128px] text-[12px]" aria-label="Filter by behavior">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="any" className="text-[12px]">Any behavior</SelectItem>
          <SelectItem value="deny" className="text-[12px]">Off</SelectItem>
          <SelectItem value="ask" className="text-[12px]">Ask</SelectItem>
          <SelectItem value="allow" className="text-[12px]">Always allow</SelectItem>
        </SelectContent>
      </Select>
      <Select
        value={riskFilter}
        onValueChange={(v) => onRiskFilterChange(v as 'any' | PermissionCategory)}
      >
        <SelectTrigger className="h-8 w-[104px] text-[12px]" aria-label="Filter by risk">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value="any" className="text-[12px]">Any risk</SelectItem>
          <SelectItem value="read" className="text-[12px]">Read</SelectItem>
          <SelectItem value="write" className="text-[12px]">Write</SelectItem>
          <SelectItem value="execute" className="text-[12px]">Run</SelectItem>
        </SelectContent>
      </Select>
      <button
        type="button"
        aria-pressed={customOnly}
        className={cn(
          'inline-flex h-8 items-center rounded-md border px-2.5 text-[12px] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
          customOnly
            ? 'border-primary/50 bg-primary/10 font-medium text-primary'
            : 'border-border text-muted-foreground hover:text-foreground',
        )}
        title="Only tools whose behavior you’ve customized"
        onClick={() => onCustomOnlyChange(!customOnly)}
      >
        Customized
      </button>
    </div>
    <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1">
      <RiskLegend />
      {filterActive && (
        <span className="inline-flex items-center gap-2 text-[11px] text-muted-foreground">
          <span>
            Showing {matchCount} of {totalCount} {totalCount === 1 ? 'tool' : 'tools'}
          </span>
          <button
            type="button"
            className="rounded-sm text-foreground underline underline-offset-2 hover:no-underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
            onClick={onClear}
          >
            Clear filters
          </button>
        </span>
      )}
    </div>
  </div>
);

/** Aggregate bulk-run feedback for a scope; per-row errors stay on their rows. */
const ScopeStatusLine: React.FC<{ status?: BulkStatus; className?: string }> = ({
  status,
  className,
}) => {
  if (!status || status.phase === 'idle') return null;
  if (status.phase === 'running') {
    return (
      <div role="status" className={cn('text-[11.5px] text-muted-foreground', className)}>
        Applying…
      </div>
    );
  }
  if (!status.message) return null;
  return (
    <div
      role="status"
      className={cn(
        'text-[11.5px]',
        status.phase === 'error' ? 'font-medium text-destructive' : 'text-muted-foreground',
        className,
      )}
    >
      {status.message}
    </div>
  );
};

/**
 * Inline confirm before a bulk change that deserves a beat: Always-allow over
 * write/execute tools (amber caution), or any bulk while filters narrow the
 * scope (so a filter can never silently rescope a bulk action). Focus lands
 * on the confirm button; Escape or Cancel returns to the control.
 */
const BulkConfirmBar: React.FC<{
  effect: PermissionEffect;
  /** Full noun phrase, e.g. "all 13 GitHub tools". */
  scopePhrase: string;
  count: number;
  riskyCount: number;
  filtered: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}> = ({ effect, scopePhrase, count, riskyCount, filtered, onConfirm, onCancel }) => {
  const confirmRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    confirmRef.current?.focus();
  }, []);
  const caution = effect === 'allow' && riskyCount > 0;
  const plural = count === 1 ? 'tool' : 'tools';
  const confirmLabel =
    effect === 'allow'
      ? `Always allow ${count} ${plural}`
      : effect === 'deny'
        ? `Turn off ${count} ${plural}`
        : `Ask for ${count} ${plural}`;
  return (
    <div
      role="group"
      aria-label="Confirm bulk permission change"
      className={cn(
        'flex flex-wrap items-center gap-2 border-t px-3 py-2',
        caution ? 'border-amber-500/30 bg-amber-500/10' : 'border-border bg-muted/40',
      )}
      onKeyDown={(e) => {
        if (e.key === 'Escape') {
          e.stopPropagation();
          onCancel();
        }
      }}
    >
      {caution && (
        <TriangleAlert
          size={13}
          className="shrink-0 text-amber-700 dark:text-amber-400"
          aria-hidden
        />
      )}
      <span className="min-w-0 flex-1 basis-56 text-[11.5px] text-foreground">
        {effect === 'allow' ? (
          <>
            Always allow {scopePhrase}?{' '}
            {riskyCount > 0
              ? `${riskyCount} can run commands or change data — they’ll run without asking you.`
              : 'They’ll run without asking you.'}
          </>
        ) : (
          <>
            Set {scopePhrase} to {EFFECT_LABELS[effect]}?
          </>
        )}
        {filtered && ' Filters are active — only the matching tools change.'}
      </span>
      <span className="flex shrink-0 items-center gap-2">
        <Button
          ref={confirmRef}
          type="button"
          variant="outline"
          size="sm"
          className="h-6 px-2 text-[11px]"
          aria-label={
            confirmLabel +
            (caution ? ', including tools that write or run commands' : '') +
            (filtered ? ' (matching tools only)' : '')
          }
          onClick={onConfirm}
        >
          {confirmLabel}
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-6 px-2 text-[11px]"
          onClick={onCancel}
        >
          Cancel
        </Button>
      </span>
    </div>
  );
};

/**
 * Subordinate reset: deletes the scope's custom rules so tools fall back to
 * defaults (with inline confirm — it removes grants made from chat approvals).
 * Repository grants and thread-scoped rules are deliberately left alone.
 */
const ResetCustomLink: React.FC<{
  scopeLabel: string;
  clearableCount: number;
  inert: boolean;
  inertReason?: string;
  busy?: boolean;
  onClear: () => void;
}> = ({ scopeLabel, clearableCount, inert, inertReason, busy, onClear }) => {
  const [confirming, setConfirming] = useState(false);
  const linkRef = useRef<HTMLButtonElement>(null);
  const confirmRef = useRef<HTMLButtonElement>(null);

  useEffect(() => {
    if (confirming) confirmRef.current?.focus();
  }, [confirming]);

  const closeConfirm = () => {
    setConfirming(false);
    requestAnimationFrame(() => linkRef.current?.focus());
  };

  if (clearableCount === 0 && !busy) return null;

  if (confirming) {
    return (
      <span
        className="inline-flex flex-wrap items-center gap-2"
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            e.stopPropagation();
            closeConfirm();
          }
        }}
      >
        <span className="text-[11px] text-foreground">
          Reset {clearableCount} custom{' '}
          {clearableCount === 1 ? 'permission' : 'permissions'} to defaults?
          This includes grants made from chat approvals.
        </span>
        <Button
          ref={confirmRef}
          type="button"
          variant="outline"
          size="sm"
          className="h-6 px-2 text-[11px] text-destructive hover:text-destructive"
          aria-label={`Reset custom permissions for ${scopeLabel}`}
          onClick={() => {
            setConfirming(false);
            onClear();
          }}
        >
          Reset
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-6 px-2 text-[11px]"
          onClick={closeConfirm}
        >
          Cancel
        </Button>
      </span>
    );
  }

  return (
    <button
      ref={linkRef}
      type="button"
      className={cn(
        'inline-flex min-h-6 items-center rounded-sm text-[11px] text-muted-foreground underline underline-offset-2 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
        inert ? 'opacity-60' : 'hover:text-foreground hover:no-underline',
      )}
      aria-disabled={inert || undefined}
      aria-busy={busy || undefined}
      title={inertReason}
      onClick={() => {
        if (!inert) setConfirming(true);
      }}
    >
      {busy
        ? 'Resetting…'
        : `Reset ${clearableCount} custom ${clearableCount === 1 ? 'permission' : 'permissions'} — ${scopeLabel}`}
    </button>
  );
};

/**
 * Global scope: one control for every tool the app knows (or, while filters
 * are active, for the matching tools) — labelled so the blast radius is
 * unmistakable before anything is clicked.
 */
const GlobalScopeCard: React.FC<{
  filterActive: boolean;
  matchCount: number;
  totalCount: number;
  summary: EffectSummary;
  customCount: number;
  riskyCount: number;
  clearableCount: number;
  rulesAvailable: boolean;
  runningScope: string | null;
  status?: BulkStatus;
  onSetEffect: (effect: PermissionEffect) => void;
  onClear: () => void;
}> = ({
  filterActive,
  matchCount,
  totalCount,
  summary,
  customCount,
  riskyCount,
  clearableCount,
  rulesAvailable,
  runningScope,
  status,
  onSetEffect,
  onClear,
}) => {
  const [pendingEffect, setPendingEffect] = useState<PermissionEffect | null>(null);
  const allowSegRef = useRef<HTMLButtonElement>(null);

  const runningHere = runningScope === 'all';
  const inert = !rulesAvailable || runningScope !== null;
  const inertReason = !rulesAvailable
    ? 'Custom permissions can’t be loaded right now.'
    : runningScope !== null && !runningHere
      ? 'Another bulk change is in progress.'
      : undefined;
  const scopePhrase = filterActive
    ? `the ${matchCount} matching ${matchCount === 1 ? 'tool' : 'tools'}`
    : `all ${totalCount} tools`;

  const requestEffect = (effect: PermissionEffect) => {
    if ((effect === 'allow' && riskyCount > 0) || filterActive) setPendingEffect(effect);
    else onSetEffect(effect);
  };

  return (
    <div className="rounded-md border border-border bg-muted/25" data-testid="permissions-global-scope">
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2 px-3 py-2.5">
        <div className="min-w-0 flex-1 basis-52">
          <h3 className="text-[13px] font-medium text-foreground">
            {filterActive ? 'Matching tools' : 'Every tool'}
          </h3>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            {filterActive
              ? `One setting for the ${matchCount} matching ${matchCount === 1 ? 'tool' : 'tools'} across every tool set.`
              : `One setting for all ${totalCount} tools — every connection and app tool set, including ones not added yet.`}
            {customCount > 0 &&
              ` ${customCount} ${customCount === 1 ? 'is' : 'are'} customized.`}
          </p>
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1">
          <div className="flex flex-wrap items-center justify-end gap-2">
            <ScopeSummaryBadge summary={summary} id="permissions-global-summary" />
            <EffectSegments
              size="md"
              value={summaryValue(summary)}
              label={`Permission for ${scopePhrase} — every tool set`}
              segmentSuffix={
                filterActive ? ` for ${scopePhrase}` : ' for every tool in the app'
              }
              describedBy={summary.kind === 'none' ? undefined : 'permissions-global-summary'}
              disabled={inert}
              busy={runningHere && status?.op === 'set'}
              title={inertReason}
              allowButtonRef={allowSegRef}
              testIdPrefix="permissions-global-effect"
              onChange={requestEffect}
            />
          </div>
          <ResetCustomLink
            scopeLabel={filterActive ? 'matching tools only' : 'every tool set'}
            clearableCount={clearableCount}
            inert={inert}
            inertReason={inertReason}
            busy={runningHere && status?.op === 'clear'}
            onClear={onClear}
          />
        </div>
      </div>
      {pendingEffect !== null && (
        <BulkConfirmBar
          effect={pendingEffect}
          scopePhrase={scopePhrase}
          count={matchCount}
          riskyCount={riskyCount}
          filtered={filterActive}
          onConfirm={() => {
            const chosen = pendingEffect;
            setPendingEffect(null);
            onSetEffect(chosen);
            requestAnimationFrame(() => allowSegRef.current?.focus());
          }}
          onCancel={() => {
            setPendingEffect(null);
            requestAnimationFrame(() => allowSegRef.current?.focus());
          }}
        />
      )}
      <ScopeStatusLine status={status} className="border-t border-border px-3 py-1.5" />
    </div>
  );
};

interface ToolGroupCardProps extends BulkActionProps {
  /** Bulk scope id, e.g. "conn:github", "app:attachments", "notadded:slack". */
  scopeId: string;
  title: string;
  /** Identity line (connections) or description (app groups). */
  subtitle?: string;
  /** Brand tile for connection groups; app groups render without one. */
  logoMeta?: ConnectionMeta;
  origin: OriginKind;
  setupHint?: string;
  view: ScopeView;
  connectionId: ConnectionCatalogId | null;
  /** Global rules already attributed to this connection. */
  connectionRules: PermissionRuleUI[];
  filterActive: boolean;
  /** All rules — clearable/effective resolution needs connector-wide rules. */
  allRules: PermissionRuleUI[];
  rulesAvailable: boolean;
  rowError: Record<string, string>;
  busyTools: Set<string>;
  recentlySavedTools: Set<string>;
  settings?: AppSettings | null;
  toolTestContext?: ToolTestDefaultContext;
  onEffectChange: (rule: PermissionRuleUI, effect: PermissionEffect) => void;
  onRevoke: (rule: PermissionRuleUI) => void;
  onCreateToolRule: (
    tool: ConnectionToolInfo,
    connectionId: ConnectionCatalogId | null,
    effect: PermissionEffect,
  ) => void;
  onOpenConnections?: () => void;
}

/**
 * One tool set (a connection or an app group): a collapsed row that already
 * answers "what is this, is it usable, what do its tools do, what did I
 * change" — plus the same Off/Ask/Always control the tool rows use, scoped to
 * this set only.
 */
const ToolGroupCard: React.FC<ToolGroupCardProps> = ({
  scopeId,
  title,
  subtitle,
  logoMeta,
  origin,
  setupHint,
  view,
  connectionId,
  connectionRules,
  filterActive,
  allRules,
  rulesAvailable,
  rowError,
  busyTools,
  recentlySavedTools,
  settings,
  toolTestContext,
  bulkRunningScope,
  bulkStatus,
  onBulkSetEffect,
  onBulkClear,
  onEffectChange,
  onRevoke,
  onCreateToolRule,
  onOpenConnections,
}) => {
  const [open, setOpen] = useState(false);
  const [pendingEffect, setPendingEffect] = useState<PermissionEffect | null>(null);
  const allowSegRef = useRef<HTMLButtonElement>(null);

  const { resolved, visible } = view;
  if (filterActive && visible.length === 0) return null;

  const locked = origin === 'needs-setup' || origin === 'not-added';
  const headingId = `permissions-group-${scopeId.replace(/[^a-zA-Z0-9-]/g, '-')}`;

  const connectorRules = connectionRules.filter((r) => r.subject.kind === 'connector');
  const toolSetRules = connectionRules.filter((r) => r.subject.kind === 'tool_set');
  // Custom rules for tools the static inventory doesn't know (renamed backend
  // tools, repository-scoped grants). Shown as extra rows so nothing is lost.
  const knownToolNames = new Set(resolved.map((r) => r.tool.name));
  const extraRules = connectionRules.filter(
    (r) =>
      r.subject.kind !== 'connector' &&
      r.subject.kind !== 'tool_set' &&
      !(toolScopedRuleToolName(r) && knownToolNames.has(toolScopedRuleToolName(r)!)),
  );

  const summary = summarizeEffects(visible.map((r) => r.effective.effect));
  const customToolCount = visible.filter((r) => r.effective.source !== 'default').length;
  const customCount = filterActive
    ? customToolCount
    : customToolCount + extraRules.length;
  const riskCounts: Record<PermissionCategory, number> = { read: 0, write: 0, execute: 0 };
  visible.forEach((r) => {
    riskCounts[r.tool.category] += 1;
  });
  const riskyCount = riskCounts.write + riskCounts.execute;
  const alwaysOnRiskyCount = visible.filter(
    (r) => r.effective.effect === 'allow' && r.tool.category !== 'read',
  ).length;

  const bulkConnectionIds: ConnectionCatalogId[] =
    filterActive || !connectionId ? [] : [connectionId];
  const clearableCount = clearableRulesFor(
    allRules,
    new Set(visible.map((r) => r.tool.name)),
    bulkConnectionIds,
    visible.map((r) => r.tool),
  ).length;

  const runningHere = bulkRunningScope === scopeId;
  const status = bulkStatus[scopeId];
  const inert = !rulesAvailable || bulkRunningScope !== null;
  const inertReason = !rulesAvailable
    ? 'Custom permissions can’t be loaded right now.'
    : bulkRunningScope !== null && !runningHere
      ? 'Another bulk change is in progress.'
      : undefined;
  const scopePhrase = filterActive
    ? `the ${visible.length} matching ${title} ${visible.length === 1 ? 'tool' : 'tools'}`
    : `all ${visible.length} ${title} ${visible.length === 1 ? 'tool' : 'tools'}`;

  const runBulk = (effect: PermissionEffect) =>
    onBulkSetEffect(
      scopeId,
      title,
      visible.map((r) => ({
        tool: r.tool,
        connectionId,
        durableToolSet: !filterActive && !connectionId,
      })),
      bulkConnectionIds,
      effect,
    );
  const requestEffect = (effect: PermissionEffect) => {
    if ((effect === 'allow' && riskyCount > 0) || filterActive) setPendingEffect(effect);
    else runBulk(effect);
  };

  const isOpen = filterActive ? true : open;

  return (
    <div
      className="rounded-md border border-border"
      role="group"
      aria-labelledby={headingId}
      data-testid={`permissions-tool-group-${testIdPart(scopeId)}`}
    >
      <Collapsible open={isOpen} onOpenChange={setOpen}>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-3 py-2">
          <h4 id={headingId} className="m-0 min-w-0 flex-1 basis-56">
            <CollapsibleTrigger asChild>
              <button
                type="button"
                data-testid={`permissions-tool-group-toggle-${testIdPart(scopeId)}`}
                aria-label={`${title}, ${ORIGIN_BADGE[origin].label}, ${resolved.length} ${resolved.length === 1 ? 'tool' : 'tools'}`}
                className="group/coll flex w-full items-center gap-2.5 rounded-md py-0.5 text-left hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                <CollapseChevron size={13} />
                {logoMeta && <ConnectionLogo meta={logoMeta} />}
                <span className="min-w-0 flex-1">
                  <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
                    <span className="truncate text-[13px] font-medium leading-tight text-foreground">
                      {title}
                    </span>
                    <OriginBadge kind={origin} />
                  </span>
                  <span
                    aria-hidden
                    className="mt-0.5 block text-[11px] font-normal leading-snug text-muted-foreground"
                  >
                    {subtitle && <span>{subtitle} · </span>}
                    {filterActive
                      ? `${visible.length} of ${resolved.length} tools`
                      : `${resolved.length} ${resolved.length === 1 ? 'tool' : 'tools'}`}
                    {riskCounts.write > 0 && (
                      <span className="text-amber-700 dark:text-amber-400">
                        {' '}· {riskCounts.write} write
                      </span>
                    )}
                    {riskCounts.execute > 0 && (
                      <span className="text-rose-700 dark:text-rose-400">
                        {' '}· {riskCounts.execute} run
                      </span>
                    )}
                    {customCount > 0 && (
                      <span className="font-medium text-primary"> · {customCount} custom</span>
                    )}
                    {alwaysOnRiskyCount > 0 && (
                      <span
                        className="font-medium text-amber-700 dark:text-amber-400"
                        title="Always-allowed — runs without asking, and can change data or run commands"
                      >
                        {' '}·{' '}
                        <TriangleAlert size={10} aria-hidden className="mb-px inline" />{' '}
                        {alwaysOnRiskyCount} always-on
                      </span>
                    )}
                    {summary.kind === 'mixed' && <span> · {summaryBreakdown(summary)}</span>}
                  </span>
                </span>
              </button>
            </CollapsibleTrigger>
          </h4>
          <div className="flex shrink-0 flex-col items-end gap-0.5 py-0.5">
            <div className="flex flex-wrap items-center justify-end gap-2">
              <ScopeSummaryBadge summary={summary} id={`${headingId}-summary`} />
              <EffectSegments
                size="md"
                value={summaryValue(summary)}
                label={`Permission for ${scopePhrase} — ${title} only`}
                segmentSuffix={` for ${scopePhrase}`}
                describedBy={summary.kind === 'none' ? undefined : `${headingId}-summary`}
                disabled={inert}
                busy={runningHere && status?.op === 'set'}
                title={inertReason}
                allowButtonRef={allowSegRef}
                testIdPrefix={`permissions-tool-group-effect-${testIdPart(scopeId)}`}
                onChange={requestEffect}
              />
            </div>
            <span className="text-[10.5px] text-muted-foreground">
              {filterActive
                ? `${title} only — ${visible.length} matching ${visible.length === 1 ? 'tool' : 'tools'}`
                : `${title} only — all ${visible.length} tools`}
            </span>
          </div>
        </div>

        {pendingEffect !== null && (
          <BulkConfirmBar
            effect={pendingEffect}
            scopePhrase={scopePhrase}
            count={visible.length}
            riskyCount={riskyCount}
            filtered={filterActive}
            onConfirm={() => {
              const chosen = pendingEffect;
              setPendingEffect(null);
              runBulk(chosen);
              requestAnimationFrame(() => allowSegRef.current?.focus());
            }}
            onCancel={() => {
              setPendingEffect(null);
              requestAnimationFrame(() => allowSegRef.current?.focus());
            }}
          />
        )}
        <ScopeStatusLine status={status} className="border-t border-border px-3 py-1.5" />

        <CollapsibleContent>
          <div className="border-t border-border">
            {locked && (
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1 border-b border-border px-3 py-1.5 text-[11.5px] text-muted-foreground">
                <span>
                  {origin === 'not-added'
                    ? 'Not in your connections — its tools stay locked until you add it. Permissions you set now apply once it’s added.'
                    : setupHint || 'Finish setting up this connection to use its tools.'}
                </span>
                {onOpenConnections && (
                  <button
                    type="button"
                    className="inline-flex items-center gap-0.5 rounded-sm text-[11.5px] text-foreground underline underline-offset-2 hover:no-underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    onClick={onOpenConnections}
                  >
                    {origin === 'not-added' ? 'Add in Connections' : 'Open Connections'}
                    <ChevronRight size={11} aria-hidden />
                  </button>
                )}
              </div>
            )}
            <ul className="divide-y divide-border/60">
              {connectorRules.map((rule) => (
                <ConnectorRuleRow
                  key={rule.id}
                  rule={rule}
                  connectionLabel={title}
                  error={rowError[rule.id]}
                  onEffectChange={(effect) => onEffectChange(rule, effect)}
                  onRevoke={() => onRevoke(rule)}
                />
              ))}
              {toolSetRules.map((rule) => (
                <ToolSetRuleRow
                  key={rule.id}
                  rule={rule}
                  toolSetLabel={title}
                  error={rowError[rule.id]}
                  onEffectChange={(effect) => onEffectChange(rule, effect)}
                  onRevoke={() => onRevoke(rule)}
                />
              ))}
              {visible.map(({ tool, effective }) => (
                <ToolRow
                  key={tool.name}
                  tool={tool}
                  contextLabel={connectionId ? 'Connection' : 'Source'}
                  contextName={origin === 'not-added' ? `${title} (not added)` : title}
                  lockedNote={
                    locked
                      ? origin === 'not-added'
                        ? 'Locked until added'
                        : 'Locked until set up'
                      : undefined
                  }
                  effective={effective}
                  rulesAvailable={rulesAvailable}
                  rowError={rowError}
                  busy={busyTools.has(tool.name)}
                  recentlySaved={recentlySavedTools.has(tool.name)}
                  settings={settings}
                  toolTestContext={toolTestContext}
                  onEffectChange={onEffectChange}
                  onRevoke={onRevoke}
                  onCreateRule={(t, effect) => onCreateToolRule(t, connectionId, effect)}
                />
              ))}
              {!filterActive &&
                extraRules.map((rule) => (
                  <CustomRuleToolRow
                    key={rule.id}
                    rule={rule}
                    connectionLabel={title}
                    error={rowError[rule.id]}
                    onEffectChange={(effect) => onEffectChange(rule, effect)}
                    onRevoke={() => onRevoke(rule)}
                  />
                ))}
              {visible.length === 0 &&
                connectorRules.length === 0 &&
                toolSetRules.length === 0 &&
                extraRules.length === 0 && (
                  <li className="px-3 py-2 text-[12px] text-muted-foreground">
                    No tools known for this connection.
                  </li>
                )}
            </ul>
            {clearableCount > 0 && (
              <div className="border-t border-border px-3 py-1.5">
                <ResetCustomLink
                  scopeLabel={`${title} only`}
                  clearableCount={clearableCount}
                  inert={inert}
                  inertReason={inertReason}
                  busy={runningHere && status?.op === 'clear'}
                  onClear={() =>
                    onBulkClear(
                      scopeId,
                      title,
                      visible.map((r) => r.tool.name),
                      bulkConnectionIds,
                      visible.map((r) => r.tool),
                    )
                  }
                />
              </div>
            )}
          </div>
        </CollapsibleContent>
      </Collapsible>
    </div>
  );
};

/** Brand tile matching the Connections console (dark tile, mono marks tinted light). */
const ConnectionLogo: React.FC<{ meta: ConnectionMeta }> = ({ meta }) => {
  const [failed, setFailed] = useState(false);
  const Icon = meta.icon;
  return (
    <span
      className="flex h-7 w-7 shrink-0 items-center justify-center rounded-md border border-border bg-[#1a1a1f] text-[#f4f2ee]"
      aria-hidden
    >
      {failed ? (
        <Icon size={15} />
      ) : (
        <img
          src={meta.logo}
          alt=""
          draggable={false}
          className={cn('h-4 w-4 object-contain', meta.mono && 'brightness-0 invert')}
          onError={() => setFailed(true)}
        />
      )}
    </span>
  );
};

interface ToolRowProps {
  tool: ConnectionToolInfo;
  contextLabel: string;
  contextName: string;
  lockedNote?: string;
  effective: EffectiveToolPermission;
  rulesAvailable: boolean;
  rowError: Record<string, string>;
  busy?: boolean;
  recentlySaved?: boolean;
  settings?: AppSettings | null;
  toolTestContext?: ToolTestDefaultContext;
  onEffectChange: (rule: PermissionRuleUI, effect: PermissionEffect) => void;
  onRevoke: (rule: PermissionRuleUI) => void;
  onCreateRule: (tool: ConnectionToolInfo, effect: PermissionEffect) => void;
}

/** One tool: compact summary row that expands into the full detail view. */
const ToolRow: React.FC<ToolRowProps> = ({
  tool,
  contextLabel,
  contextName,
  lockedNote,
  effective,
  rulesAvailable,
  rowError,
  busy,
  recentlySaved,
  settings,
  toolTestContext,
  onEffectChange,
  onRevoke,
  onCreateRule,
}) => {
  const [testPhase, setTestPhase] = useState<'idle' | 'running' | 'ok' | 'error'>('idle');
  const [testMessage, setTestMessage] = useState('');
  const [testResult, setTestResult] = useState<ToolTestResult | null>(null);
  const [testErrorDetails, setTestErrorDetails] = useState<Record<string, unknown> | null>(null);
  const [testArgValues, setTestArgValues] = useState<Record<string, string>>({});
  const [testContextValues, setTestContextValues] = useState<Record<string, string>>({});
  const [selectedTestGroupIndex, setSelectedTestGroupIndex] = useState(0);
  const [localRootPickerBusy, setLocalRootPickerBusy] = useState(false);
  const previousDefaultArgValuesRef = useRef<Record<string, string>>({});
  const previousDefaultContextValuesRef = useRef<Record<string, string>>({});
  const defaultProjectId = String(toolTestContext?.projectId || '').trim();
  const defaultThreadId = String(toolTestContext?.threadId || '').trim();
  const meta = CATEGORY_META[tool.category];
  const { effect, source, rule } = effective;
  const custom = source !== 'default';
  // Always allow on a tool that changes data or runs commands deserves a
  // standing caution — it acts without asking from now on.
  const risky = effect === 'allow' && tool.category !== 'read';
  const createError = rowError[`tool:${tool.name}`];
  const ruleError = rule ? rowError[rule.id] : undefined;
  const testReadiness = toolTestReadiness(tool, toolTestContext);
  const testBusy = testPhase === 'running';
  const locked = Boolean(lockedNote);
  const localFileToolNeedsGrant = tool.name === 'file_search' || tool.name === 'local_read_file' || tool.name === 'local_open_file';
  const localRootPickerKind: 'folder' | 'file' = tool.name === 'local_read_file' || tool.name === 'local_open_file' ? 'file' : 'folder';
  const missingLocalGrant = localFileToolNeedsGrant && !String(testArgValues.local_root || '').trim();
  const missingTestArgs = testReadiness.requiredArguments.filter(
    (name) => !String(testArgValues[name] || '').trim(),
  );
  const selectedTestGroup =
    testReadiness.requiredArgumentGroups[
      Math.min(selectedTestGroupIndex, Math.max(testReadiness.requiredArgumentGroups.length - 1, 0))
    ] || [];
  const missingSelectedGroupArgs = selectedTestGroup.filter(
    (name) => !String(testArgValues[name] || '').trim(),
  );
  const testArgGroupSatisfied =
    testReadiness.requiredArgumentGroups.length === 0 ||
    missingSelectedGroupArgs.length === 0;
  const missingTestContexts = testReadiness.contextInputs.filter(
    (input) => !String(testContextValues[input.key] || '').trim(),
  );
  const missingUnstructuredTestInput = testReadiness.unstructuredInputRequired;
  const missingGroupRequirement = testArgGroupSatisfied
    ? undefined
    : toolTestGroupRequirementLabel(testReadiness.requiredArgumentGroups);
  const canRunTest =
    !locked &&
    testReadiness.supported &&
    missingTestArgs.length === 0 &&
    !missingLocalGrant &&
    testArgGroupSatisfied &&
    missingTestContexts.length === 0 &&
    !missingUnstructuredTestInput;
  const testDisabledReason = !testReadiness.supported
    ? testReadiness.disabledReason
    : locked
      ? lockedNote
    : !canRunTest
      ? `Provide ${[
          ...missingTestArgs.map((name) => `arg:${name}`),
          ...(missingLocalGrant ? [localRootPickerKind] : []),
          ...(missingSelectedGroupArgs.length > 0
            ? missingSelectedGroupArgs.map((name) => `mode:${name}`)
            : missingGroupRequirement
              ? [`one of:${missingGroupRequirement}`]
              : []),
          ...missingTestContexts.map((input) => input.label),
          ...(missingUnstructuredTestInput ? ['test input'] : []),
        ].join(', ')}.`
      : undefined;
  const testButtonLabel = testBusy
    ? 'Testing'
    : locked
      ? 'Locked'
      : !testReadiness.supported
        ? 'Unavailable'
        : canRunTest
          ? 'Test'
          : 'Needs input';

  useEffect(() => {
    const defaults = toolTestDefaultArgumentValues(testReadiness, toolTestContext);
    const previous = previousDefaultArgValuesRef.current;
    previousDefaultArgValuesRef.current = defaults;
    setTestArgValues((current) => {
      let changed = false;
      const next = { ...current };
      for (const [key, value] of Object.entries(defaults)) {
        const currentValue = String(current[key] || '').trim();
        if (!currentValue || currentValue === previous[key]) {
          next[key] = value;
          changed = changed || current[key] !== value;
        }
      }
      return changed ? next : current;
    });
  }, [defaultThreadId, testReadiness, toolTestContext]);

  useEffect(() => {
    const defaults = {
      ...(defaultProjectId ? { projectId: defaultProjectId } : {}),
      ...(defaultThreadId ? { threadId: defaultThreadId } : {}),
    };
    const previous = previousDefaultContextValuesRef.current;
    previousDefaultContextValuesRef.current = defaults;
    setTestContextValues((current) => {
      let changed = false;
      const next = { ...current };
      for (const [key, value] of Object.entries(defaults)) {
        const currentValue = String(current[key] || '').trim();
        if (!currentValue || currentValue === previous[key]) {
          next[key] = value;
          changed = changed || current[key] !== value;
        }
      }
      return changed ? next : current;
    });
  }, [defaultProjectId, defaultThreadId]);

  const setEffect = (value: PermissionEffect) => {
    if (value === effect) return;
    if (rule && source === 'tool-rule') onEffectChange(rule, value);
    else onCreateRule(tool, value);
  };

  const clearTestFeedback = () => {
    if (testPhase === 'running') return;
    setTestPhase('idle');
    setTestMessage('');
    setTestResult(null);
    setTestErrorDetails(null);
  };

  const runReadinessTest = async () => {
    if (!canRunTest || testBusy) return;
    setTestPhase('running');
    setTestMessage('');
    setTestResult(null);
    setTestErrorDetails(null);
    try {
      const input: ToolTestRequest = {
        args: buildToolTestArgs(
          testReadiness.requiredArguments,
          selectedTestGroup,
          testArgValues,
        ),
      };
      if (localFileToolNeedsGrant && String(testArgValues.local_root || '').trim()) {
        input.args.local_root = String(testArgValues.local_root || '').trim();
        input.args.local_kind = String(testArgValues.local_kind || localRootPickerKind).trim();
      }
      const projectId = String(testContextValues.projectId || '').trim();
      const threadId = String(testContextValues.threadId || '').trim();
      if (projectId) input.projectId = projectId;
      if (threadId) input.threadId = threadId;
      const result = await runToolTest(tool.name, input);
      const ok = result.output?.ok === true;
      setTestPhase(ok ? 'ok' : 'error');
      setTestMessage(ok ? 'Test passed.' : 'Test ran but returned an error.');
      setTestResult(result);
    } catch (error) {
      setTestPhase('error');
      setTestResult(null);
      if (isToolTestMissingInput(error)) {
        setTestMessage(error.inputHint || error.message);
        setTestErrorDetails({
          type: 'missing_input',
          missingArguments: error.missingArguments,
          missingContext: error.missingContext,
          requiredArguments: error.requiredArguments,
          requiredArgumentGroups: error.requiredArgumentGroups,
          requiredContext: error.requiredContext,
        });
      } else if (isToolTestPermissionError(error)) {
        setTestMessage(toolTestPermissionMessage(error.decision, tool.label));
        setTestErrorDetails({
          type: 'permission',
          decision: error.decision,
          category: error.category,
          subject: error.subject,
          reason: error.reason,
        });
      } else {
        setTestMessage(error instanceof Error ? error.message : 'Tool test failed.');
        setTestErrorDetails({
          type: 'error',
          message: error instanceof Error ? error.message : 'Tool test failed.',
        });
      }
    }
  };

  return (
    <li data-testid={`permissions-tool-row-${testIdPart(tool.name)}`}>
      <Collapsible>
        <div className="flex items-center gap-2.5 px-3 py-1.5 hover:bg-muted/30">
          <CollapsibleTrigger asChild>
            <button
              type="button"
              data-testid={`permissions-tool-toggle-${testIdPart(tool.name)}`}
              className="group/coll flex min-w-0 flex-1 items-center gap-2.5 rounded-sm py-0.5 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              aria-label={`Details for ${tool.label}`}
            >
              <CollapseChevron size={12} />
              <span
                className="min-w-0 flex-1 truncate text-[12.5px] text-foreground"
                title={tool.label}
              >
                {tool.label}
              </span>
              <RiskChip category={tool.category} />
            </button>
          </CollapsibleTrigger>
          <EffectSegments
            value={effect}
            label={`Permission for ${tool.label} — this tool only`}
            busy={busy}
            disabled={!rulesAvailable}
            testIdPrefix={`permissions-tool-effect-${testIdPart(tool.name)}`}
            onChange={setEffect}
          />
          <span className="flex w-[64px] shrink-0 items-center justify-end gap-1 text-[10.5px]">
            {risky && (
              <span
                title={`Caution: always allowed even though it ${RISK_MEANING[tool.category]}`}
              >
                <TriangleAlert
                  size={12}
                  className="shrink-0 text-amber-700 dark:text-amber-400"
                  aria-hidden
                />
                <span className="sr-only">
                  Caution: always allowed even though it {RISK_MEANING[tool.category]}.
                </span>
              </span>
            )}
            {busy ? (
              <span className="text-muted-foreground">Saving…</span>
            ) : locked ? (
              <span
                className="inline-flex items-center gap-1 text-muted-foreground"
                title={lockedNote}
              >
                <Lock size={11} aria-hidden />
                Locked
              </span>
            ) : recentlySaved ? (
              <span className="font-medium text-primary">Saved</span>
            ) : custom ? (
              <span
                className="rounded-full border border-primary/40 bg-primary/10 px-1.5 py-px text-[10px] font-medium text-primary"
                title="You customized this — expand the row to reset it"
              >
                Custom
              </span>
            ) : null}
          </span>
        </div>
        {createError && (
          <p role="alert" className="px-3 pb-1.5 text-[12px] text-destructive">
            {createError}
          </p>
        )}
        <CollapsibleContent>
          <div className="border-t border-border/60 bg-muted/20 px-3 py-2.5">
            <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 text-[11.5px]">
              <dt className="text-muted-foreground">Tool</dt>
              <dd className="font-mono text-foreground">{tool.name}</dd>
              {tool.description && (
                <>
                  <dt className="text-muted-foreground">Description</dt>
                  <dd className="text-foreground">{tool.description}</dd>
                </>
              )}
              <dt className="text-muted-foreground">{contextLabel}</dt>
              <dd className="text-foreground">{contextName}</dd>
              {lockedNote && (
                <>
                  <dt className="text-muted-foreground">Availability</dt>
                  <dd className="text-foreground">{lockedNote}</dd>
                </>
              )}
              <dt className="text-muted-foreground">Category</dt>
              <dd className="text-foreground">
                {meta.label} — {meta.meaning.toLowerCase()}
              </dd>
              {rule?.subject.operation && (
                <>
                  <dt className="text-muted-foreground">Operation</dt>
                  <dd className="font-mono text-foreground">{rule.subject.operation}</dd>
                </>
              )}
              <dt className="text-muted-foreground">Behavior</dt>
              <dd className="text-foreground">
                {EFFECT_LABELS[effect]} — {EFFECT_HELP[effect].toLowerCase()}
              </dd>
              <dt className="text-muted-foreground">Source</dt>
              <dd className="text-foreground">{sourceDescription(effective, tool, contextName)}</dd>
              <dt className="text-muted-foreground">Tool test</dt>
              <dd className="space-y-1 text-foreground">
                <div className="flex flex-wrap items-center gap-2">
                  <span>
                    {lockedNote
                      ? `${testReadiness.summary} ${lockedNote}.`
                      : testReadiness.summary}
                  </span>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    className="h-7 px-2 text-[11px]"
                    disabled={!canRunTest || testBusy}
                    title={testDisabledReason}
                    data-testid={`permissions-tool-test-${testIdPart(tool.name)}`}
                    onClick={runReadinessTest}
                  >
                    {testBusy ? (
                      <RefreshCw size={11} className="mr-1 animate-spin" aria-hidden />
                    ) : (
                      <Play size={11} className="mr-1" aria-hidden />
                    )}
                    {testButtonLabel}
                  </Button>
                </div>
                {testReadiness.requirements.length > 0 && (
                  <div className="flex flex-wrap gap-1">
                    {testReadiness.requirements.map((item) => (
                      <span
                        key={item}
                        className="rounded-full border border-border bg-background px-1.5 py-px font-mono text-[10.5px] text-muted-foreground"
                      >
                        {item}
                      </span>
                    ))}
                  </div>
                )}
                {testReadiness.inputHint && (
                  <div className="text-[11px] text-muted-foreground">
                    {testReadiness.inputHint}
                  </div>
                )}
                {testReadiness.supported &&
                  (localFileToolNeedsGrant ||
                    testReadiness.requiredArguments.length > 0 ||
                    testReadiness.requiredArgumentGroups.length > 0 ||
                    testReadiness.contextInputs.length > 0) && (
                    <div className="mt-2 grid gap-2 rounded-md border border-border bg-background/75 p-2">
                      {localFileToolNeedsGrant && (
                        <div className="grid gap-1">
                          <Label
                            htmlFor={`tool-test-${tool.name}-folder`.replace(/[^a-zA-Z0-9_-]/g, '-')}
                            className="text-[10.5px] text-muted-foreground"
                          >
                            {localRootPickerKind === 'file' ? 'File' : 'Folder'}
                          </Label>
                          <LocalRootTestPicker
                            id={`tool-test-${tool.name}-folder`.replace(/[^a-zA-Z0-9_-]/g, '-')}
                            toolName={tool.name}
                            kind={localRootPickerKind}
                            value={testArgValues.local_root || ''}
                            busy={localRootPickerBusy}
                            onBusyChange={setLocalRootPickerBusy}
                            onChange={(value) => {
                              setTestArgValues((current) => ({
                                ...current,
                                local_root: value,
                                local_kind: localRootPickerKind,
                                ...(localRootPickerKind === 'file' && !String(current.path || '').trim()
                                  ? { path: localFileName(value) }
                                  : {}),
                              }));
                              clearTestFeedback();
                            }}
                          />
                        </div>
                      )}
                      {testReadiness.requiredArguments.map((name) => {
                        const id = `tool-test-${tool.name}-${name}`.replace(/[^a-zA-Z0-9_-]/g, '-');
                        return (
                          <div key={name} className="grid gap-1">
                            <Label
                              htmlFor={id}
                              className="font-mono text-[10.5px] text-muted-foreground"
                            >
                              arg:{name}
                            </Label>
                            {name === 'local_root' ? (
                              <LocalRootTestPicker
                                id={id}
                                toolName={tool.name}
                                kind={localRootPickerKind}
                                value={testArgValues[name] || ''}
                                busy={localRootPickerBusy}
                                onBusyChange={setLocalRootPickerBusy}
                                onChange={(value) => {
                                  setTestArgValues((current) => ({
                                    ...current,
                                    [name]: value,
                                    local_kind: localRootPickerKind,
                                  }));
                                  clearTestFeedback();
                                }}
                              />
                            ) : (
                              <Input
                                id={id}
                                data-testid={`permissions-tool-test-input-${testIdPart(tool.name)}-${testIdPart(name)}`}
                                value={testArgValues[name] || ''}
                                placeholder={toolTestArgumentPlaceholder(name, toolTestContext, testReadiness.sampleArguments)}
                                className="h-8 font-mono text-[12px]"
                                onChange={(event) => {
                                  const value = event.target.value;
                                  setTestArgValues((current) => ({ ...current, [name]: value }));
                                  clearTestFeedback();
                                }}
                              />
                            )}
                            {toolTestArgumentContextDescription(name, toolTestContext) && (
                              <div className="text-[10.5px] text-muted-foreground">
                                {toolTestArgumentContextDescription(name, toolTestContext)}
                              </div>
                            )}
                          </div>
                        );
                      })}
                      {testReadiness.requiredArgumentGroups.length > 0 && (
                        <div className="grid gap-2 rounded border border-border/70 bg-muted/25 p-2">
                          <div className="grid gap-1">
                            <Label
                              htmlFor={`tool-test-${tool.name}-input-mode`.replace(/[^a-zA-Z0-9_-]/g, '-')}
                              className="text-[10.5px] text-muted-foreground"
                            >
                              Input mode
                            </Label>
                            <Select
                              value={String(
                                Math.min(
                                  selectedTestGroupIndex,
                                  Math.max(testReadiness.requiredArgumentGroups.length - 1, 0),
                                ),
                              )}
                              onValueChange={(value) => {
                                const next = Number.parseInt(value, 10);
                                setSelectedTestGroupIndex(Number.isFinite(next) ? next : 0);
                                clearTestFeedback();
                              }}
                            >
                              <SelectTrigger
                                id={`tool-test-${tool.name}-input-mode`.replace(/[^a-zA-Z0-9_-]/g, '-')}
                                className="h-8 text-[12px]"
                              >
                                <SelectValue />
                              </SelectTrigger>
                              <SelectContent>
                                {testReadiness.requiredArgumentGroups.map((group, index) => (
                                  <SelectItem key={`${tool.name}-mode-${index}`} value={String(index)}>
                                    {toolTestGroupOptionLabel(group)}
                                  </SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                          </div>
                          <div className="grid gap-2 sm:grid-cols-2">
                            {selectedTestGroup.map((name) => {
                              const id = `tool-test-${tool.name}-mode-${name}`.replace(/[^a-zA-Z0-9_-]/g, '-');
                              return (
                                <div key={`selected-group-${name}`} className="grid gap-1">
                                  <Label
                                    htmlFor={id}
                                    className="font-mono text-[10.5px] text-muted-foreground"
                                  >
                                    mode:{name}
                                  </Label>
                                  {name === 'local_root' ? (
                                    <LocalRootTestPicker
                                      id={id}
                                      toolName={tool.name}
                                      kind={localRootPickerKind}
                                      value={testArgValues[name] || ''}
                                      busy={localRootPickerBusy}
                                      onBusyChange={setLocalRootPickerBusy}
                                      onChange={(value) => {
                                        setTestArgValues((current) => ({
                                          ...current,
                                          [name]: value,
                                          local_kind: localRootPickerKind,
                                        }));
                                        clearTestFeedback();
                                      }}
                                    />
                                  ) : (
                                    <Input
                                      id={id}
                                      data-testid={`permissions-tool-test-input-${testIdPart(tool.name)}-${testIdPart(name)}`}
                                      value={testArgValues[name] || ''}
                                      placeholder={toolTestArgumentPlaceholder(name, toolTestContext, testReadiness.sampleArguments)}
                                      className="h-8 font-mono text-[12px]"
                                      onChange={(event) => {
                                        const value = event.target.value;
                                        setTestArgValues((current) => ({ ...current, [name]: value }));
                                        clearTestFeedback();
                                      }}
                                    />
                                  )}
                                  {toolTestArgumentContextDescription(name, toolTestContext) && (
                                    <div className="text-[10.5px] text-muted-foreground">
                                      {toolTestArgumentContextDescription(name, toolTestContext)}
                                    </div>
                                  )}
                                </div>
                              );
                            })}
                          </div>
                        </div>
                      )}
                      {testReadiness.contextInputs.map((input) => {
                        const id = `tool-test-${tool.name}-${input.key}`.replace(/[^a-zA-Z0-9_-]/g, '-');
                        return (
                          <div key={input.key} className="grid gap-1">
                            <Label
                              htmlFor={id}
                              className="text-[10.5px] text-muted-foreground"
                            >
                              {input.label}
                            </Label>
                            <Input
                              id={id}
                              data-testid={`permissions-tool-test-context-${testIdPart(tool.name)}-${testIdPart(input.key)}`}
                              value={testContextValues[input.key] || ''}
                              placeholder={input.placeholder}
                              className="h-8 font-mono text-[12px]"
                              onChange={(event) => {
                                const value = event.target.value;
                                setTestContextValues((current) => ({ ...current, [input.key]: value }));
                                clearTestFeedback();
                              }}
                            />
                            {input.description && (
                              <div className="text-[10.5px] text-muted-foreground">
                                {input.description}
                              </div>
                            )}
                          </div>
                        );
                      })}
                    </div>
                  )}
                {testMessage && (
                  <div
                    role={testPhase === 'error' ? 'alert' : 'status'}
                    data-testid={`permissions-tool-test-message-${testIdPart(tool.name)}`}
                    className={cn(
                      'text-[11px]',
                      testPhase === 'error' ? 'text-destructive' : 'text-muted-foreground',
                    )}
                  >
                    {testMessage}
                  </div>
                )}
                {(testResult || testErrorDetails) && (
                  <ToolTestDetails
                    result={testResult}
                    errorDetails={testErrorDetails}
                    settings={settings}
                  />
                )}
              </dd>
            </dl>
            {risky && (
              <p className="mt-2 flex items-start gap-1.5 text-[11px] text-amber-700 dark:text-amber-400">
                <TriangleAlert size={12} className="mt-px shrink-0" aria-hidden />
                Caution: this tool {RISK_MEANING[tool.category]} and is set to
                run without asking you.
              </p>
            )}
            {rule && source === 'tool-rule' && (
              <div className="mt-2.5 flex flex-wrap items-center gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-8 text-[12px]"
                  aria-label={`Reset ${tool.label} to its default`}
                  onClick={() => onRevoke(rule)}
                >
                  <RotateCcw size={12} className="mr-1.5" aria-hidden />
                  Reset to default
                </Button>
                <span className="text-[11px] text-muted-foreground">
                  Default: {EFFECT_LABELS[defaultEffectFor(tool.category)]}
                </span>
              </div>
            )}
            {!rulesAvailable && (
              <p className="mt-2 text-[11px] text-muted-foreground">
                Custom permissions can’t be loaded right now, so this shows the
                built-in default.
              </p>
            )}
            {ruleError && (
              <p role="alert" className="mt-1.5 text-[12px] text-destructive">
                {ruleError}
              </p>
            )}
          </div>
        </CollapsibleContent>
      </Collapsible>
    </li>
  );
};

const ToolTestDetails: React.FC<{
  result: ToolTestResult | null;
  errorDetails: Record<string, unknown> | null;
  settings?: AppSettings | null;
}> = ({ result, errorDetails, settings }) => {
  if (!result && !errorDetails) return null;
  return (
    <div className="mt-2 grid gap-2 rounded-md border border-border bg-background/80 p-2 text-[11px]">
      {result && (
        <>
          <ToolTestJSONBlock label="Normalized args" value={result.args} />
          {Array.isArray(result.progress) && result.progress.length > 0 && (
            <ToolTestJSONBlock label="Progress events" value={result.progress} />
          )}
          <ToolTestRenderedOutput result={result} settings={settings} />
        </>
      )}
      {errorDetails && <ToolTestJSONBlock label="Failure details" value={errorDetails} />}
    </div>
  );
};

const LocalRootTestPicker: React.FC<{
  id: string;
  toolName: string;
  kind: 'folder' | 'file';
  value: string;
  busy: boolean;
  onBusyChange: (busy: boolean) => void;
  onChange: (value: string) => void;
}> = ({ id, toolName, kind, value, busy, onBusyChange, onChange }) => {
  const [error, setError] = useState('');
  const choose = async () => {
    if (busy) return;
    onBusyChange(true);
    setError('');
    try {
      const picked = kind === 'file' ? await pickLocalFile() : await pickLocalDirectory();
      onChange(picked.path);
    } catch (e: any) {
      if (!isLocalPickerCanceled(e)) {
        setError(e?.message || `Couldn’t choose a ${kind}.`);
      }
    } finally {
      onBusyChange(false);
    }
  };
  return (
    <div className="grid gap-1">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          id={id}
          type="button"
          variant="outline"
          size="sm"
          className="h-8 px-2 text-[11.5px]"
          aria-busy={busy || undefined}
          data-testid={`permissions-tool-test-choose-${kind}-${testIdPart(toolName)}`}
          onClick={() => void choose()}
        >
          {busy ? (
            <RefreshCw size={11} className="mr-1 animate-spin" aria-hidden />
          ) : kind === 'file' ? (
            <FileText size={11} className="mr-1" aria-hidden />
          ) : (
            <FolderOpen size={11} className="mr-1" aria-hidden />
          )}
          {kind === 'file' ? 'Choose file' : 'Choose folder'}
        </Button>
        {value && (
          <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-foreground" title={value}>
            {displayPath(value)}
          </span>
        )}
      </div>
      {error && <div className="text-[10.5px] text-destructive">{error}</div>}
    </div>
  );
};

function localFileName(path: string): string {
  const value = String(path || '').trim();
  if (!value) return '';
  const parts = value.split(/[\\/]/).filter(Boolean);
  return parts[parts.length - 1] || value;
}

const ToolTestRenderedOutput: React.FC<{
  result: ToolTestResult;
  settings?: AppSettings | null;
}> = ({ result, settings }) => {
  const artifact = useMemo(() => buildToolTestArtifact(result), [result]);
  const rendererEntry = useMemo(() => {
    if (!artifact) return null;
    return resolveRenderer(artifact);
  }, [artifact]);
  const RenderedContent = rendererEntry?.component;

  if (!artifact) {
    return <ToolTestJSONBlock label="Output" value={result.output} />;
  }

  return (
    <div className="grid gap-1">
      <div className="flex items-center justify-between gap-2">
        <div className="font-medium text-muted-foreground">Output</div>
        <div className="truncate text-[10.5px] text-muted-foreground">
          {rendererEntry?.label || artifact.title}
        </div>
      </div>
      <div className="h-80 min-h-64 overflow-hidden rounded-md border border-border/70 bg-background">
        {RenderedContent ? (
          <RenderedContent
            artifact={artifact}
            toolResult={artifact.metadata?.raw_result}
            settings={settings || undefined}
            className="h-full"
          />
        ) : (
          <RawViewer
            content={artifact.content}
            language={artifact.language}
            className="h-full rounded-none border-0"
          />
        )}
      </div>
      <details className="rounded border border-border/70 bg-muted/20 px-2 py-1">
        <summary className="cursor-pointer text-[10.5px] font-medium text-muted-foreground">
          Raw output
        </summary>
        <div className="mt-2">
          <ToolTestJSONBlock label="Output JSON" value={result.output} />
        </div>
      </details>
    </div>
  );
};

const ToolTestJSONBlock: React.FC<{ label: string; value: unknown }> = ({ label, value }) => (
  <div className="grid gap-1">
    <div className="font-medium text-muted-foreground">{label}</div>
    <pre className="max-h-36 overflow-auto rounded border border-border/70 bg-muted/35 p-2 font-mono text-[10.5px] leading-relaxed text-foreground">
      {formatToolTestJSON(value)}
    </pre>
  </div>
);

function formatToolTestJSON(value: unknown): string {
  try {
    return JSON.stringify(value ?? null, null, 2);
  } catch {
    return String(value);
  }
}

function sourceDescription(
  effective: EffectiveToolPermission,
  tool: ConnectionToolInfo,
  contextName: string,
): string {
  const { source, rule } = effective;
  if (source === 'default' || !rule) {
    return `Built-in default for ${CATEGORY_META[tool.category].label.toLowerCase()} tools`;
  }
  if (source === 'connector-rule') {
    return `Connection-wide custom permission for ${contextName} — edit it on the row above`;
  }
  if (source === 'tool-set-rule') {
    return `Tool-set custom permission for ${contextName} — edit or reset it on the group row`;
  }
  const parts = ['Custom permission'];
  if (rule.createdVia === 'approval' || rule.createdVia?.startsWith('approval:')) {
    parts.push('granted from a chat approval');
  }
  if (rule.createdAt) parts.push(`added ${relativeTime(rule.createdAt)}`);
  if (rule.lastUsedAt) parts.push(`last used ${relativeTime(rule.lastUsedAt)}`);
  return parts.join(' · ');
}

const TEST_MODE_LABELS: Record<string, string> = {
  connection_check: 'Connection check',
  sample_call: 'Sample call',
  dry_run: 'Dry run',
  none: 'No safe test',
};

type ToolTestContextKey = 'projectId' | 'threadId';

interface ToolTestDefaultContext {
  projectId?: string | null;
  projectName?: string | null;
  threadId?: string | null;
  threadTitle?: string | null;
}

interface ToolTestContextInput {
  key: ToolTestContextKey;
  label: string;
  placeholder: string;
  context: string;
  description?: string;
}

function toolTestReadiness(tool: ConnectionToolInfo, defaultContext?: ToolTestDefaultContext): {
  summary: string;
  requirements: string[];
  requiredArguments: string[];
  requiredArgumentGroups: string[][];
  sampleArguments: Record<string, unknown>;
  contextInputs: ToolTestContextInput[];
  supported: boolean;
  unstructuredInputRequired: boolean;
  inputHint?: string;
  disabledReason?: string;
} {
  const test = tool.test;
  if (!test || !test.supported || test.mode === 'none') {
    const summary = test?.summary?.trim() || (test ? 'No safe test declared.' : 'Runtime test metadata unavailable.');
    return {
      summary,
      requirements: [],
      requiredArguments: [],
      requiredArgumentGroups: [],
      sampleArguments: {},
      contextInputs: [],
      supported: false,
      unstructuredInputRequired: false,
      disabledReason: summary,
    };
  }

  const label = TEST_MODE_LABELS[test.mode] || titleCaseFromId(test.mode);
  const argumentRequirements = test.required_arguments || [];
  const argumentGroupRequirements = test.required_argument_groups || [];
  const sampleArguments = test.sample_arguments || {};
  const contextRequirements = test.required_context || [];
  const contextInputs = toolTestContextInputs(tool, defaultContext);
  const argumentGroupLabel = toolTestGroupRequirementLabel(argumentGroupRequirements);
  const unstructuredInputRequired =
    test.input_required === true &&
    argumentRequirements.length === 0 &&
    argumentGroupRequirements.length === 0 &&
    contextInputs.length === 0;
  const requirements = [
    ...argumentRequirements.map((value) => `arg:${value}`),
    ...(argumentGroupLabel ? [`one-of:${argumentGroupLabel}`] : []),
    ...contextRequirements.map((value) => `context:${value}`),
    ...(unstructuredInputRequired ? ['input'] : []),
  ];
  const inputCount =
    argumentRequirements.length +
    (argumentGroupLabel ? 1 : 0) +
    contextInputs.length +
    (unstructuredInputRequired ? 1 : 0);
  const approval = test.requires_approval ? ' · approval required' : '';
  if (inputCount === 0) {
    return {
      summary: `${label}${approval}.`,
      requirements,
      requiredArguments: argumentRequirements,
      requiredArgumentGroups: argumentGroupRequirements,
      sampleArguments,
      contextInputs,
      supported: true,
      unstructuredInputRequired: false,
      inputHint: test.input_hint,
    };
  }
  return {
    summary: `${label}${approval} · requires ${inputCount} ${inputCount === 1 ? 'input' : 'inputs'}.`,
    requirements,
    requiredArguments: argumentRequirements,
    requiredArgumentGroups: argumentGroupRequirements,
    sampleArguments,
    contextInputs,
    supported: true,
    unstructuredInputRequired,
    disabledReason: test.input_hint || `Requires ${requirements.join(', ')}.`,
  };
}

function toolTestContextInputs(
  tool: ConnectionToolInfo,
  defaultContext?: ToolTestDefaultContext,
): ToolTestContextInput[] {
  const out: ToolTestContextInput[] = [];
  const add = (input: ToolTestContextInput) => {
    if (out.some((existing) => existing.key === input.key)) return;
    out.push(input);
  };
  const projectId = String(defaultContext?.projectId || '').trim();
  const threadId = String(defaultContext?.threadId || '').trim();
  for (const context of tool.test?.required_context || []) {
    const value = String(context || '').trim();
    if (!value || toolTestContextIsAutomatic(tool, value)) continue;
    switch (value) {
      case 'project':
        add({
          key: 'projectId',
          label: 'Project ID',
          placeholder: projectId ? 'Current project ID' : 'project id',
          context: value,
          description: toolTestCurrentContextDescription(
            'current project',
            defaultContext?.projectName,
            projectId,
          ),
        });
        break;
      case 'thread':
        add({
          key: 'threadId',
          label: 'Thread ID',
          placeholder: threadId ? 'Current thread ID' : 'thread id',
          context: value,
          description: toolTestCurrentContextDescription(
            'current thread',
            defaultContext?.threadTitle,
            threadId,
          ),
        });
        break;
      default:
        break;
    }
  }
  return out;
}

function toolTestContextIsAutomatic(tool: ConnectionToolInfo, context: string): boolean {
  const value = String(context || '').trim();
  if (!value) return true;
  if (value.startsWith('connection:')) {
    const connector = String(tool.connectorId || '').trim();
    return connector === '' || value === `connection:${connector}`;
  }
  return ['artifact', 'query_job', 'search_index', 'browser'].includes(value);
}

function toolTestCurrentContextDescription(
  prefix: string,
  label?: string | null,
  id?: string | null,
): string | undefined {
  const cleanId = String(id || '').trim();
  if (!cleanId) return undefined;
  const cleanLabel = String(label || '').trim();
  return cleanLabel ? `Using ${prefix}: ${cleanLabel}` : `Using ${prefix}`;
}

function toolTestDefaultArgumentValues(
  readiness: ReturnType<typeof toolTestReadiness>,
  defaultContext?: ToolTestDefaultContext,
): Record<string, string> {
  const defaults: Record<string, string> = {};
  const threadId = String(defaultContext?.threadId || '').trim();
  if (!threadId) return defaults;
  const argumentNames = new Set<string>([
    ...readiness.requiredArguments,
    ...readiness.requiredArgumentGroups.flat(),
  ]);
  if (argumentNames.has('thread_id')) defaults.thread_id = threadId;
  return defaults;
}

function toolTestArgumentContextDescription(
  name: string,
  defaultContext?: ToolTestDefaultContext,
): string | undefined {
  if (String(name || '').trim() !== 'thread_id') return undefined;
  return toolTestCurrentContextDescription(
    'current thread',
    defaultContext?.threadTitle,
    defaultContext?.threadId,
  );
}

function toolTestPermissionMessage(decision: string, toolLabel: string): string {
  const label = String(toolLabel || 'This tool').trim();
  switch (String(decision || '').trim().toLowerCase()) {
    case 'deny':
      return `${label} is set to Off. Change this tool, or its tool set, to Ask or Always allow before testing.`;
    case 'ask':
      return `${label} is set to Ask. Run it from a chat approval flow, or set it to Always allow before testing from Settings.`;
    default:
      return `${label} is blocked by its current permission. Check the tool or tool-set permission before testing.`;
  }
}

function buildToolTestArgs(
  requiredArguments: string[],
  selectedArgumentGroup: string[],
  values: Record<string, string>,
): Record<string, unknown> {
  const args: Record<string, unknown> = {};
  for (const name of requiredArguments) {
    args[name] = parseToolTestArgumentValue(name, values[name] || '');
  }
  for (const raw of selectedArgumentGroup) {
    const name = String(raw || '').trim();
    if (!name) continue;
    const value = values[name] || '';
    if (String(value).trim()) {
      args[name] = parseToolTestArgumentValue(name, value);
    }
  }
  return args;
}

function toolTestGroupRequirementLabel(groups: string[][]): string | undefined {
  const labels = groups
    .map((group) =>
      group
        .map((raw) => String(raw || '').trim())
        .filter(Boolean)
        .join(' + '),
    )
    .filter(Boolean);
  return labels.length > 0 ? labels.join(' OR ') : undefined;
}

function toolTestGroupOptionLabel(group: string[]): string {
  return group
    .map((raw) => String(raw || '').trim())
    .filter(Boolean)
    .join(' + ') || 'Input mode';
}

function parseToolTestArgumentValue(name: string, raw: string): unknown {
  const value = raw.trim();
  if (value === '') return '';
  if (
    value === 'true' ||
    value === 'false' ||
    value === 'null' ||
    /^-?\d+(?:\.\d+)?$/.test(value) ||
    /^[\[{"]/.test(value)
  ) {
    try {
      return JSON.parse(value);
    } catch (error) {
      throw new Error(`Invalid JSON for ${name}. Use a plain string or valid JSON.`);
    }
  }
  return value;
}

function toolTestArgumentPlaceholder(
  name: string,
  defaultContext?: ToolTestDefaultContext,
  sampleArguments?: Record<string, unknown>,
): string {
  const sample = toolTestSampleArgumentPlaceholder(name, sampleArguments);
  if (sample) return sample;
  switch (name) {
    case 'artifact_id':
      return 'artifact_123';
    case 'channel_id':
      return 'C1234567890';
    case 'contentBase64':
      return 'aGVsbG8=';
    case 'contentText':
      return 'Hello from a dry-run tool test';
    case 'data_json':
      return '[{"name":"A","value":10}]';
    case 'fileName':
      return 'report.txt';
    case 'issueKey':
      return 'TEAM-123';
    case 'local_path':
      return 'downloads/report.txt';
    case 'path':
      return 'docs/report.txt';
    case 'thread_id':
      return String(defaultContext?.threadId || '').trim() ? 'Current thread ID' : 'thread id';
    case 'projectOrBoard':
      return 'TEAM';
    case 'relatedIssueKeys':
    case 'metrics':
    case 'args':
      return '["value"]';
    case 'source':
    case 'chart':
    case 'fields':
      return '{"key":"value"}';
    default:
      return name;
  }
}

function toolTestSampleArgumentPlaceholder(
  name: string,
  sampleArguments?: Record<string, unknown>,
): string | undefined {
  if (!sampleArguments || !Object.prototype.hasOwnProperty.call(sampleArguments, name)) {
    return undefined;
  }
  const value = sampleArguments[name];
  if (value === undefined || value === null) return undefined;
  if (typeof value === 'string') return value;
  if (typeof value === 'number' || typeof value === 'boolean') return String(value);
  try {
    return JSON.stringify(value);
  } catch {
    return undefined;
  }
}

function titleCaseFromId(value: string): string {
  return String(value || '')
    .split(/[_\-\s]+/)
    .filter(Boolean)
    .map((part) => part.slice(0, 1).toUpperCase() + part.slice(1))
    .join(' ') || 'Tool test';
}

/** Connection-wide custom rule ("All GitHub write tools") with inline controls. */
const ConnectorRuleRow: React.FC<{
  rule: PermissionRuleUI;
  connectionLabel: string;
  error?: string;
  onEffectChange: (effect: PermissionEffect) => void;
  onRevoke: () => void;
}> = ({ rule, connectionLabel, error, onEffectChange, onRevoke }) => {
  const meta = CATEGORY_META[rule.category];
  const CategoryIcon = meta.icon;
  return (
    <li className="bg-muted/25 px-3 py-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <CategoryIcon size={13} className="shrink-0 text-muted-foreground" aria-hidden />
        <div className="min-w-0 flex-1">
          <div className="text-[12.5px] text-foreground">
            All {connectionLabel} {meta.label.toLowerCase()} tools
          </div>
          <div className="text-[11px] text-muted-foreground">
            Connection-wide · applies to every matching tool below
          </div>
        </div>
        <EffectSegments
          value={rule.effect}
          label={`Permission for all ${connectionLabel} ${meta.label.toLowerCase()} tools — connection-wide`}
          onChange={onEffectChange}
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-8 text-[12px]"
          aria-label={`Remove connection-wide permission for ${connectionLabel}`}
          onClick={onRevoke}
        >
          <Trash2 size={12} className="mr-1.5" aria-hidden />
          Remove
        </Button>
      </div>
      {error && (
        <p role="alert" className="mt-1.5 text-[12px] text-destructive">
          {error}
        </p>
      )}
    </li>
  );
};

/** Tool-set-wide custom rule ("All Artifacts & charts read tools"). */
const ToolSetRuleRow: React.FC<{
  rule: PermissionRuleUI;
  toolSetLabel: string;
  error?: string;
  onEffectChange: (effect: PermissionEffect) => void;
  onRevoke: () => void;
}> = ({ rule, toolSetLabel, error, onEffectChange, onRevoke }) => {
  const meta = CATEGORY_META[rule.category];
  const CategoryIcon = meta.icon;
  return (
    <li className="bg-muted/25 px-3 py-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <CategoryIcon size={13} className="shrink-0 text-muted-foreground" aria-hidden />
        <div className="min-w-0 flex-1">
          <div className="text-[12.5px] text-foreground">
            All {toolSetLabel} {meta.label.toLowerCase()} tools
          </div>
          <div className="text-[11px] text-muted-foreground">
            Tool-set rule · applies to current and future tools in this set
          </div>
        </div>
        <EffectSegments
          value={rule.effect}
          label={`Permission for all ${toolSetLabel} ${meta.label.toLowerCase()} tools — tool-set-wide`}
          onChange={onEffectChange}
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-8 text-[12px]"
          aria-label={`Remove tool-set permission for ${toolSetLabel}`}
          onClick={onRevoke}
        >
          <Trash2 size={12} className="mr-1.5" aria-hidden />
          Remove
        </Button>
      </div>
      {error && (
        <p role="alert" className="mt-1.5 text-[12px] text-destructive">
          {error}
        </p>
      )}
    </li>
  );
};

/** Custom rule attributed to this connection but not in the static inventory. */
const CustomRuleToolRow: React.FC<{
  rule: PermissionRuleUI;
  connectionLabel: string;
  error?: string;
  onEffectChange: (effect: PermissionEffect) => void;
  onRevoke: () => void;
}> = ({ rule, error, onEffectChange, onRevoke }) => {
  const subject = subjectPresentation(rule.subject);
  const SubjectIcon = subject.icon;
  return (
    <li className="px-3 py-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <SubjectIcon size={13} className="shrink-0 text-muted-foreground" aria-hidden />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[12.5px] text-foreground" title={subject.label}>
            {subject.label}
          </div>
          <div className="text-[11px] text-muted-foreground">
            {[
              'Custom permission',
              subject.detail,
              rule.lastUsedAt ? `Last used ${relativeTime(rule.lastUsedAt)}` : null,
            ]
              .filter(Boolean)
              .join(' · ')}
          </div>
        </div>
        <EffectSegments
          value={rule.effect}
          label={`Permission for ${subject.label} — this tool only`}
          onChange={onEffectChange}
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-8 text-[12px]"
          aria-label={`Remove custom permission: ${subject.label}`}
          onClick={onRevoke}
        >
          <Trash2 size={12} className="mr-1.5" aria-hidden />
          Remove
        </Button>
      </div>
      {error && (
        <p role="alert" className="mt-1.5 text-[12px] text-destructive">
          {error}
        </p>
      )}
    </li>
  );
};

// ---------------------------------------------------------------------------
// Audit
// ---------------------------------------------------------------------------

const AuditSection: React.FC<{
  records: PermissionAuditRecordUI[];
  load: LoadState;
  onRetry: () => void;
}> = ({ records, load, onRetry }) => {
  return (
    <section aria-labelledby="permissions-audit-heading">
      <h2
        id="permissions-audit-heading"
        className="mb-2 flex items-center gap-2 text-[15px] font-semibold text-foreground"
      >
        <History size={14} aria-hidden />
        Audit
      </h2>
      {load.phase === 'loading' ? (
        <div className="space-y-2" aria-hidden>
          <Skeleton className="h-10 w-full rounded-md" />
          <Skeleton className="h-10 w-full rounded-md" />
        </div>
      ) : load.phase === 'unsupported' ? (
        <p className="text-[12.5px] text-muted-foreground">
          Permission activity appears here once the approvals service is available.
        </p>
      ) : load.phase === 'error' ? (
        <div className="flex flex-wrap items-center gap-2 text-[12.5px] text-muted-foreground">
          <span>{load.message}</span>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="h-7 text-[12px]"
            onClick={() => void onRetry()}
          >
            <RefreshCw size={12} className="mr-1.5" aria-hidden />
            Try again
          </Button>
        </div>
      ) : records.length === 0 ? (
        <p className="text-[12.5px] text-muted-foreground">
          No permission activity yet.
        </p>
      ) : (
        <ul className="space-y-1.5">
          {records.slice(0, 8).map((record) => (
            <AuditRow key={record.id} record={record} />
          ))}
        </ul>
      )}
    </section>
  );
};

const AuditRow: React.FC<{ record: PermissionAuditRecordUI }> = ({ record }) => {
  const subject = record.subject ? subjectPresentation(record.subject) : null;
  const SubjectIcon = subject?.icon || History;
  const label = auditLabel(record);
  const detail = [
    subject?.label,
    record.scope,
    record.createdAt ? relativeTime(record.createdAt) : null,
  ]
    .filter(Boolean)
    .join(' · ');
  return (
    <li className="rounded-md border border-border bg-muted/30 px-3 py-2">
      <div className="flex items-start gap-2">
        <SubjectIcon size={14} className="mt-0.5 shrink-0 text-muted-foreground" aria-hidden />
        <div className="min-w-0">
          <div className="text-[12.5px] text-foreground">{label}</div>
          {detail && (
            <div className="truncate text-[11.5px] text-muted-foreground" title={detail}>
              {detail}
            </div>
          )}
        </div>
      </div>
    </li>
  );
};

function auditLabel(record: PermissionAuditRecordUI): string {
  switch (record.type) {
    case 'approval_requested':
      return 'Approval requested';
    case 'approval_approved':
      return 'Approval granted';
    case 'approval_rejected':
      return 'Approval rejected';
    case 'rule_created':
      return 'Permission added';
    case 'rule_updated':
      return 'Permission changed';
    case 'rule_deleted':
      return 'Permission removed';
    default:
      return 'Permission activity';
  }
}

// ---------------------------------------------------------------------------
// Rows
// ---------------------------------------------------------------------------

const LocalFilesToolsSummary: React.FC<{
  tools: ConnectionToolInfo[];
  rules: PermissionRuleUI[];
}> = ({ tools, rules }) => {
  if (tools.length === 0) {
    return (
      <div className="mb-3 rounded-md border border-border bg-muted/25 px-3 py-2 text-[12.5px] text-muted-foreground">
        Local file tools appear here once the tool inventory is available.
      </div>
    );
  }
  return (
    <div className="mb-3 rounded-md border border-border bg-muted/25 px-3 py-2">
      <div className="text-[12.5px] font-medium text-foreground">Tools allowed by Local Computer</div>
      <div className="mt-1 flex flex-wrap gap-1.5">
        {tools.map((tool) => {
          const effective = resolveStandaloneToolPermission(tool, rules);
          return (
            <span
              key={tool.name}
              className="inline-flex items-center gap-1 rounded-full border border-border bg-background px-2 py-0.5 text-[11px] text-muted-foreground"
              title={`${tool.label}: ${EFFECT_LABELS[effective.effect]}`}
            >
              <RiskChip category={tool.category} />
              <span className="text-foreground">{tool.label}</span>
              <span>{EFFECT_SEGMENTS.find((s) => s.value === effective.effect)?.label}</span>
            </span>
          );
        })}
      </div>
    </div>
  );
};

const PermissionRuleRow: React.FC<{
  rule: PermissionRuleUI;
  error?: string;
  onEffectChange: (effect: PermissionEffect) => void;
  onRevoke?: () => void;
}> = ({ rule, error, onEffectChange, onRevoke }) => {
  const subject = subjectPresentation(rule.subject);
  const SubjectIcon = subject.icon;
  const helpId = `rule-help-${rule.id}`;
  return (
    <li className="rounded-md border border-border bg-muted/30 px-3 py-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <SubjectIcon size={14} className="shrink-0 text-muted-foreground" aria-hidden />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] text-foreground" title={subject.label}>
            {subject.label}
          </div>
          <div className="text-[11.5px] text-muted-foreground">
            {[
              subject.detail,
              CATEGORY_META[rule.category].label,
              rule.lastUsedAt ? `Last used ${relativeTime(rule.lastUsedAt)}` : null,
              rule.createdVia?.startsWith('approval:') || rule.createdVia === 'approval'
                ? 'Granted from a chat approval'
                : null,
            ]
              .filter(Boolean)
              .join(' · ')}
          </div>
        </div>
        <div className="flex flex-col items-end gap-0.5">
          <div className="flex items-center gap-2">
            <EffectSegments
              value={rule.effect}
              label={`Permission for ${subject.label}`}
              onChange={onEffectChange}
            />
            {onRevoke && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="h-8 text-[12px]"
                aria-label={`Remove custom permission: ${subject.label}`}
                onClick={onRevoke}
              >
                <Trash2 size={12} className="mr-1.5" aria-hidden />
                Remove
              </Button>
            )}
          </div>
          <span id={helpId} className="text-[11px] text-muted-foreground">
            {EFFECT_HELP[rule.effect]}
          </span>
        </div>
      </div>
      {error && (
        <p role="alert" className="mt-1.5 text-[12px] text-destructive">
          {error}
        </p>
      )}
    </li>
  );
};

const FolderRuleRow: React.FC<{
  rule: PermissionRuleUI;
  error?: string;
  onEffectChange: (effect: PermissionEffect) => void;
  onChangeLocation: () => Promise<void>;
  onRevoke: () => void;
  scopeNote?: string;
}> = ({ rule, error, onEffectChange, onChangeLocation, onRevoke, scopeNote }) => {
  const subject = subjectPresentation(rule.subject);
  const kind = localGrantKind(rule.subject);
  const SubjectIcon = kind === 'file' ? FileText : subject.icon;
  const [changing, setChanging] = useState(false);
  const [testing, setTesting] = useState(false);
  const [testMessage, setTestMessage] = useState('');
  const excludes = rule.subject.excludes || [];
  const runTest = async () => {
    if (testing) return;
    setTesting(true);
    setTestMessage('');
    try {
      await testLocalPermissionGrant(rule.subject);
      setTestMessage('Test passed.');
    } catch (e: any) {
      setTestMessage(e?.message || 'Test failed.');
    } finally {
      setTesting(false);
    }
  };
  return (
    <li className="rounded-md border border-border bg-muted/30 px-3 py-2">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
        <SubjectIcon size={14} className="shrink-0 text-muted-foreground" aria-hidden />
        <div className="min-w-0 flex-1">
          <div
            className="truncate font-mono text-[12.5px] text-foreground"
            title={subject.label}
          >
            {subject.label}
          </div>
          <div className="text-[11.5px] text-muted-foreground">
            {[
              subject.detail,
              rule.scope === 'global' ? 'Scope: Always' : rule.scope,
              scopeNote,
              kind === 'folder'
                ? excludes.length > 0
                  ? `${excludes.length} excludes`
                  : 'Default excludes'
                : 'Selected file only',
              String(rule.createdVia || '').startsWith('approval')
                ? 'Granted from a chat approval'
                : 'Added by you',
              rule.lastUsedAt ? `Last used ${relativeTime(rule.lastUsedAt)}` : 'Not used yet',
            ]
              .filter(Boolean)
              .join(' · ')}
          </div>
        </div>
        <EffectSegments
          value={rule.effect}
          label={`Permission for ${subject.label}`}
          onChange={onEffectChange}
        />
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-8 text-[12px]"
          aria-busy={testing || undefined}
          onClick={() => void runTest()}
        >
          {testing ? (
            <RefreshCw size={12} className="mr-1.5 animate-spin" aria-hidden />
          ) : (
            <Play size={12} className="mr-1.5" aria-hidden />
          )}
          Test
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-8 text-[12px]"
          aria-busy={changing || undefined}
          onClick={async () => {
            if (changing) return;
            setChanging(true);
            try {
              await onChangeLocation();
            } catch (e: any) {
              if (!isLocalPickerCanceled(e)) {
                setTestMessage(e?.message || `Couldn’t choose a different ${kind}.`);
              }
            } finally {
              setChanging(false);
            }
          }}
        >
          {kind === 'file' ? (
            <FileText size={12} className="mr-1.5" aria-hidden />
          ) : (
            <FolderOpen size={12} className="mr-1.5" aria-hidden />
          )}
          Change
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="h-8 text-[12px]"
          aria-label={`Revoke access: ${subject.label}`}
          onClick={onRevoke}
        >
          <Trash2 size={12} className="mr-1.5" aria-hidden />
          Revoke
        </Button>
      </div>
      {error && (
        <p role="alert" className="mt-1.5 text-[12px] text-destructive">
          {error}
        </p>
      )}
      {testMessage && (
        <p
          role={testMessage === 'Test passed.' ? 'status' : 'alert'}
          className={cn(
            'mt-1.5 text-[12px]',
            testMessage === 'Test passed.' ? 'text-muted-foreground' : 'text-destructive',
          )}
        >
          {testMessage}
        </p>
      )}
      {kind === 'folder' && excludes.length > 0 && (
        <details className="mt-1.5 text-[11.5px] text-muted-foreground">
          <summary className="cursor-pointer">Excluded paths</summary>
          <ul className="mt-1 list-disc pl-5 font-mono">
            {excludes.map((pattern) => (
              <li key={pattern}>{pattern}</li>
            ))}
          </ul>
        </details>
      )}
    </li>
  );
};

function localGrantKind(subject: PermissionSubject): 'folder' | 'file' {
  return subject.local_kind === 'file' ? 'file' : 'folder';
}

// ---------------------------------------------------------------------------
// Add local location
// ---------------------------------------------------------------------------

const AddLocalLocationPicker: React.FC<{
  onAddFolder: () => Promise<void>;
  onAddFile: () => Promise<void>;
}> = ({ onAddFolder, onAddFile }) => {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const buttonRef = useRef<HTMLButtonElement>(null);
  const errorId = 'add-local-location-error';

  const submit = async (kind: 'folder' | 'file') => {
    setBusy(true);
    setError('');
    try {
      await (kind === 'file' ? onAddFile() : onAddFolder());
      buttonRef.current?.focus();
    } catch (e: any) {
      if (!isLocalPickerCanceled(e)) {
        setError(e?.message || `Couldn’t add this ${kind}.`);
      }
      buttonRef.current?.focus();
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="max-w-md">
      <div className="text-[12px] text-muted-foreground">
        Add a file or folder the assistant may read
      </div>
      <div className="mt-1 flex flex-wrap gap-2">
        <Button
          ref={buttonRef}
          type="button"
          variant="outline"
          className="h-8 shrink-0 text-[12.5px]"
          aria-busy={busy || undefined}
          aria-disabled={busy || undefined}
          aria-describedby={error ? errorId : undefined}
          onClick={() => {
            if (!busy) void submit('folder');
          }}
        >
          {busy ? (
            <RefreshCw size={13} className="mr-1.5 animate-spin" aria-hidden />
          ) : (
            <FolderPlus size={13} className="mr-1.5" aria-hidden />
          )}
          Choose folder
        </Button>
        <Button
          type="button"
          variant="outline"
          className="h-8 shrink-0 text-[12.5px]"
          aria-busy={busy || undefined}
          aria-disabled={busy || undefined}
          aria-describedby={error ? errorId : undefined}
          onClick={() => {
            if (!busy) void submit('file');
          }}
        >
          {busy ? (
            <RefreshCw size={13} className="mr-1.5 animate-spin" aria-hidden />
          ) : (
            <FileText size={13} className="mr-1.5" aria-hidden />
          )}
          Choose file
        </Button>
      </div>
      {error && (
        <p id={errorId} role="alert" className="mt-1 text-[12px] text-destructive">
          {error}
        </p>
      )}
      <p className="mt-1 text-[11.5px] text-muted-foreground">
        Read-only. Folder grants exclude hidden credentials by default; file grants are exact-file only.
      </p>
    </div>
  );
};

export default PermissionsTab;
