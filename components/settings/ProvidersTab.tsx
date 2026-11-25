import React, { useState, useEffect, useCallback, useMemo, useRef } from 'react';
import {
  Plus,
  Check,
  ArrowRight,
  ArrowLeft,
  Pencil,
  Trash2,
  Globe,
  Shield,
  KeyRound,
  Zap,
  Settings2,
  Eye,
  EyeOff,
  RefreshCw,
  ChevronRight,
  Cloud,
  BookmarkPlus,
  Star,
  X,
} from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Textarea } from '@/components/ui/textarea';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { Switch } from '@/components/ui/switch';
import type {
  AppSettings,
  ProviderCardType,
  ProviderConfig,
  ProviderTemplate,
} from '@/types';
import { PROVIDER_DEFS, getProviderDef } from '@/providers/catalog/providerDefs';
import type { AuthMethod, ConfigField, ProviderDef } from '@/providers/catalog/providerDefs';
import SecretField from './SecretField';
import { getSecretsStatus } from '@/services/secretsApi';
import { buildProviderEndpoint, getActiveProviderConfig, applyProviderConfigToLegacySettings } from '@/lib/providerConfigRuntime';
import { canonicalProviderWebSearch } from '@/lib/providerWebSearch';
import { testProviderConnection } from '@/services/providerTestApi';
import {
  BEDROCK_COMPACTION_DEFAULT_INSTRUCTION,
  BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS,
  OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS,
} from '@/lib/bedrockCompaction';
import {
  buildCompatibleEndpointUrl,
  getCompatibleEndpointSegment,
  splitCompatibleEndpointFromUrl,
} from '@/lib/compatibleEndpointUrl';
import { getCapabilityUxFlags } from './providers/capabilityUxFlags';
import { useCapabilityReportSeam } from './providers/capabilityContract';
import CapabilityMatrix from './providers/CapabilityMatrix';
import CapabilityCheckPanel from './providers/CapabilityCheckPanel';
import ConnectionCard from './providers/ConnectionCard';
import ConnectionDetail from './providers/ConnectionDetail';
import ServiceSurfaceStep from './providers/ServiceSurfaceStep';
import ModelDiscoveryList from './providers/ModelDiscoveryList';
import { describeSurface } from './providers/surfaceIdentity';
import { applyNewConnectionCapabilityDefaults, deriveProviderApiSurface } from '@/lib/capability/providerConfig';
import { isConnectionEnabledForRuns, withConnectionEnabled } from '@/lib/connectionEnablement';

interface ProvidersTabProps {
  localSettings: AppSettings;
  setLocalSettings: React.Dispatch<React.SetStateAction<AppSettings>>;
  onSave: (settings: AppSettings) => void | Promise<void>;
  onWizardOpenChange?: (open: boolean) => void;
  /** Open this connection's detail view on mount (deep-link from the preflight sheet). */
  focusConnectionId?: string | null;
}

type TestState = 'idle' | 'testing' | 'success' | 'failure';

type TestResult = {
  ok: boolean;
  message: string;
  endpoint: string;
  timestamp: number;
};

const redactKey = (k: string) => {
  if (!k || k.length < 8) return '••••••••••';
  return k.slice(0, Math.min(7, k.length - 3)) + '••••••' + k.slice(-3);
};

const formatTimestamp = (ts: number | null | undefined): string => {
  if (!ts) return 'Never';
  try {
    return new Date(ts).toLocaleString();
  } catch {
    return 'Never';
  }
};

const defaultApiTypeForProvider = (
  providerId: ProviderCardType
): NonNullable<ProviderConfig['apiType']> => {
  if (providerId === 'openai') return 'responses';
  if (providerId === 'bedrock') return 'messages';
  return 'chat_completions';
};

const GROUP_META: Record<string, { label: string; icon: React.ReactNode }> = {
  identity: { label: 'Identity', icon: <Pencil size={12} /> },
  config: { label: 'Configuration', icon: <Zap size={12} /> },
  auth_detail: { label: 'Auth Details', icon: <KeyRound size={12} /> },
  placement: { label: 'Model Placement', icon: <Settings2 size={12} /> },
};

const isConnectionManagedHostedToolField = (key: string): boolean => (
  key.startsWith('webSearch') || key.startsWith('codeExecution')
);

function parseOptionalJsonObject(raw: unknown, label: string): Record<string, unknown> | null {
  const txt = String(raw ?? '').trim();
  if (!txt) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(txt);
  } catch {
    throw new Error(`${label} must be valid JSON.`);
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error(`${label} must be a JSON object.`);
  }
  return parsed as Record<string, unknown>;
}

function parseOptionalCodeExecutionSkills(raw: unknown): Array<{ type: string; skillId: string; version?: string | null }> | null {
  const txt = String(raw ?? '').trim();
  if (!txt) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(txt);
  } catch {
    throw new Error('Code Sandbox Skills must be valid JSON.');
  }
  if (!Array.isArray(parsed)) {
    throw new Error('Code Sandbox Skills must be a JSON array.');
  }
  const out = parsed
    .map((item) => {
      if (!item || typeof item !== 'object' || Array.isArray(item)) return null;
      const type = String((item as Record<string, unknown>).type || '').trim();
      const skillId = String((item as Record<string, unknown>).skillId || '').trim();
      const version = String((item as Record<string, unknown>).version || '').trim();
      if (!type || !skillId) return null;
      return {
        type,
        skillId,
        ...(version ? { version } : {}),
      };
    })
    .filter(Boolean) as Array<{ type: string; skillId: string; version?: string | null }>;
  return out.length > 0 ? out : null;
}

/**
 * Identity/lifecycle fields the wizard always rebuilds (or the server owns).
 * Everything else in a seed config — capabilityControls, attachmentPolicies,
 * apiSurface, and unknown forward-compatible fields — must survive an edit.
 * configFingerprint is deliberately dropped so the server recomputes it.
 */
const CONFIG_REBUILD_STRIP_KEYS: readonly string[] = [
  'id',
  'createdAt',
  'updatedAt',
  'isDefault',
  'lastTestedAt',
  'lastTestStatus',
  'lastTestMessage',
  'lastTestEndpoint',
  'configFingerprint',
];

function stripVolatileConfigFields(
  source: Record<string, any>,
  extraPattern?: RegExp,
): Record<string, any> {
  const out: Record<string, any> = {};
  for (const [key, value] of Object.entries(source)) {
    if (CONFIG_REBUILD_STRIP_KEYS.includes(key)) continue;
    if (extraPattern?.test(key)) continue;
    out[key] = value;
  }
  return out;
}

/** Index (into the full auth list) of the single offered method, else null. */
function soleOfferedAuthIndex(def: ProviderDef, keepLegacyId: string | null): number | null {
  const offered = def.authMethods.filter((m) => !m.legacy || m.id === keepLegacyId);
  return offered.length === 1 ? def.authMethods.indexOf(offered[0]) : null;
}

function jsonText(value: unknown): string {
  if (!value || typeof value !== 'object') return '';
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return '';
  }
}

function stableRecordEntries(value: Record<string, any>, ignoredKeys: string[] = []): Array<[string, any]> {
  const ignored = new Set(ignoredKeys);
  return Object.keys(value)
    .filter((key) => !ignored.has(key))
    .sort()
    .map((key) => [key, value[key]]);
}

function parseOptionalStringMapLoose(raw: unknown): Record<string, string> | null {
  const txt = String(raw ?? '').trim();
  if (!txt) return null;
  try {
    const parsed = JSON.parse(txt);
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null;

    const out: Record<string, string> = {};
    for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
      if (!key.trim()) continue;
      out[key] = String(value ?? '');
    }
    return Object.keys(out).length > 0 ? out : null;
  } catch {
    return null;
  }
}

function normalizeCompatibleEndpointDraft(
  rawBaseUrl: unknown,
  rawApiType: ProviderConfig['apiType'],
  rawEndpointSuffix: unknown
): {
  baseUrl: string;
  apiType: 'chat_completions' | 'responses';
  endpointSuffix: string;
} {
  const fallbackApiType = rawApiType === 'responses' ? 'responses' : 'chat_completions';
  const baseUrl = String(rawBaseUrl ?? '');
  const storedEndpointSuffix = String(rawEndpointSuffix ?? '').trim();
  const parsed = splitCompatibleEndpointFromUrl(baseUrl);

  return {
    baseUrl: parsed.includesEndpoint ? parsed.baseUrl : baseUrl,
    apiType: parsed.apiType || fallbackApiType,
    endpointSuffix: storedEndpointSuffix || parsed.endpointSuffix || '',
  };
}

const ProvidersTab: React.FC<ProvidersTabProps> = ({ localSettings, setLocalSettings, onSave, onWizardOpenChange, focusConnectionId }) => {
  const [secrets, setSecrets] = useState<{ present: Record<string, boolean> } | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);

  const [wizardOpen, setWizardOpen] = useState(false);
  const [editId, setEditId] = useState<string | null>(null);
  const [draftId, setDraftId] = useState<string | null>(null);
  const [step, setStep] = useState(0);
  const [selProv, setSelProv] = useState<ProviderCardType | null>(null);
  const [selAuth, setSelAuth] = useState<number | null>(null);
  const [selCC, setSelCC] = useState<string | null>(null);
  const [fields, setFields] = useState<Record<string, any>>({});
  const [secretsSet, setSecretsSet] = useState<Record<string, boolean>>({});
  const [showAdv, setShowAdv] = useState(false);
  const [extraHeaders, setExtraHeaders] = useState<Array<{ id: string; key: string; value: string; isSecret: boolean }>>([]);
  const [inlineCC, setInlineCC] = useState(false);
  const [ccFields, setCcFields] = useState<Record<string, any>>({});
  const [ccSecrets, setCcSecrets] = useState<Record<string, boolean>>({});

  const [testState, setTestState] = useState<TestState>('idle');
  const [testResult, setTestResult] = useState<TestResult | null>(null);
  const [testedInputKey, setTestedInputKey] = useState<string | null>(null);
  const [savingProvider, setSavingProvider] = useState(false);
  // Capability-UX (plan §5.7 flag): connection-detail navigation + inline save confirm.
  const [detailId, setDetailId] = useState<string | null>(null);
  const [pendingSaveConfirm, setPendingSaveConfirm] = useState<string | null>(null);
  // Inline template-name capture (quality bar: no window.prompt).
  const [templateDraft, setTemplateDraft] = useState<{ cfg: ProviderConfig; name: string } | null>(null);
  // Legacy auth methods are hidden for new configs; this keeps the one an
  // existing config already uses selectable while editing it.
  const [legacyAuthKeepId, setLegacyAuthKeepId] = useState<string | null>(null);
  // Seed for non-destructive rebuilds: the config being edited/duplicated or
  // the template being instantiated. buildDraftConfig preserves its
  // capability controls, policies, and unknown fields.
  const draftSeedRef = useRef<Partial<ProviderConfig> | null>(null);
  const testAbortRef = useRef<AbortController | null>(null);
  const latestTestInputKeyRef = useRef('');
  const previousTestInputKeyRef = useRef<string | null>(null);
  const baselineTestInputKeyRef = useRef<string | null>(null);

  const configs = localSettings.providerConfigs || [];
  const templates = localSettings.providerTemplates || [];
  const capabilityFlags = getCapabilityUxFlags(localSettings);
  // Report surfaces (chips/matrix/detail) and the v2 editor gate separately.
  const capabilityUxOn = capabilityFlags.report;
  const editorV2On = capabilityFlags.editorV2;

  const flags = localSettings.featureFlags?.providers;
  const templatesEnabled = localSettings.featureFlags?.providerTemplates !== false;

  const isProviderEnabled = useCallback((providerId: ProviderCardType): boolean => {
    if (!flags) return true;
    return (flags as Record<string, boolean>)[providerId] !== false;
  }, [flags]);

  const availableProviders = useMemo(() => {
    return Object.values(PROVIDER_DEFS).filter((p) => isProviderEnabled(p.id));
  }, [isProviderEnabled]);

  const visibleConfigs = useMemo(() => {
    return configs.filter((cfg) => isProviderEnabled(cfg.providerId));
  }, [configs, isProviderEnabled]);

  const visibleTemplates = useMemo(() => {
    return templates.filter((tpl) => {
      const provider = (tpl.provider as ProviderConfig | undefined)?.providerId;
      return typeof provider === 'string' ? isProviderEnabled(provider as ProviderCardType) : false;
    });
  }, [isProviderEnabled, templates]);

  const activeConfig = useMemo(() => getActiveProviderConfig(localSettings), [localSettings]);

  // Deep-link (e.g. preflight sheet → Permissions): land directly on the
  // relevant connection's capability controls when the detail surface exists.
  useEffect(() => {
    if (!focusConnectionId || !capabilityUxOn) return;
    if ((localSettings.providerConfigs || []).some((c) => c.id === focusConnectionId)) {
      setDetailId(focusConnectionId);
    }
    // Only on mount / focus change: user navigation must not be re-hijacked.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusConnectionId, capabilityUxOn]);

  const prov = selProv ? getProviderDef(selProv) : null;
  const auth = selAuth !== null && prov ? prov.authMethods[selAuth] : null;
  // Legacy auth methods (e.g. Gemini GCP ADC) are only offered to configs that
  // already use them; new configurations never see them.
  const isAuthMethodOffered = useCallback(
    (method: AuthMethod): boolean => !method.legacy || legacyAuthKeepId === method.id,
    [legacyAuthKeepId],
  );
  const offeredAuthMethods = useMemo(
    () => (prov ? prov.authMethods.filter(isAuthMethodOffered) : []),
    [isAuthMethodOffered, prov],
  );
  const multi = offeredAuthMethods.length > 1;

  useEffect(() => {
    onWizardOpenChange?.(wizardOpen);
    return () => onWizardOpenChange?.(false);
  }, [onWizardOpenChange, wizardOpen]);

  const matchingCloudCreds = useMemo(() => {
    if (!prov?.cloudCred) return [];
    if (prov.cloudCred === 'aws') return localSettings.cloudCredentials?.aws || [];
    if (prov.cloudCred === 'gcp') return localSettings.cloudCredentials?.gcp || [];
    return [];
  }, [prov, localSettings.cloudCredentials]);

  const compatibleSecretProfileId = useMemo(() => {
    if (selProv !== 'compatible') return null;
    if (draftId?.trim()) return draftId.trim();
    if (editId?.trim()) return editId.trim();
    return null;
  }, [draftId, editId, selProv]);

  const testInputKey = useMemo(() => JSON.stringify({
    draftId,
    editId,
    providerId: selProv,
    authIndex: selAuth,
    cloudCredentialId: selCC,
    fields: stableRecordEntries(fields, ['displayName']),
    extraHeaders: extraHeaders.map((h) => ({
      id: h.id,
      key: h.key,
      isSecret: h.isSecret,
      value: h.isSecret ? '' : h.value,
    })),
    inlineCC,
    ccFields: stableRecordEntries(ccFields),
    ccSecrets: stableRecordEntries(ccSecrets),
    secretsSet: stableRecordEntries(secretsSet),
  }), [ccFields, ccSecrets, draftId, editId, extraHeaders, fields, inlineCC, secretsSet, selAuth, selCC, selProv]);

  useEffect(() => {
    latestTestInputKeyRef.current = testInputKey;
  }, [testInputKey]);

  useEffect(() => {
    if (!wizardOpen) {
      previousTestInputKeyRef.current = null;
      baselineTestInputKeyRef.current = null;
      return;
    }
    if (previousTestInputKeyRef.current === null) {
      previousTestInputKeyRef.current = testInputKey;
      baselineTestInputKeyRef.current = testInputKey;
      return;
    }
    if (previousTestInputKeyRef.current === testInputKey) return;

    previousTestInputKeyRef.current = testInputKey;
    testAbortRef.current?.abort();
    testAbortRef.current = null;
    setTestedInputKey(null);
    setPendingSaveConfirm(null);
    if (testState !== 'idle') {
      setTestState('idle');
      setTestResult(null);
    }
  }, [testInputKey, testState, wizardOpen]);

  const refreshSecrets = useCallback(async () => {
    try {
      const status = await getSecretsStatus();
      setSecrets(status);
    } catch {
      setSecrets(null);
    }
  }, []);

  useEffect(() => { void refreshSecrets(); }, [refreshSecrets]);

  const isSecretSet = useCallback((key: string) => Boolean(secrets?.present?.[key]), [secrets?.present]);

  const resetWizard = useCallback(() => {
    setWizardOpen(false);
    setEditId(null);
    setDraftId(null);
    setStep(0);
    setSelProv(null);
    setSelAuth(null);
    setSelCC(null);
    setFields({});
    setSecretsSet({});
    setShowAdv(false);
    setExtraHeaders([]);
    setInlineCC(false);
    setCcFields({});
    setCcSecrets({});
    setTestState('idle');
    setTestResult(null);
    setTestedInputKey(null);
    testAbortRef.current?.abort();
    testAbortRef.current = null;
    previousTestInputKeyRef.current = null;
    baselineTestInputKeyRef.current = null;
    setSavingProvider(false);
    setPendingSaveConfirm(null);
    setTemplateDraft(null);
    setLegacyAuthKeepId(null);
    draftSeedRef.current = null;
  }, []);

  const postJson = useCallback(async (path: string, body: any) => {
    const res = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      const text = await res.text().catch(() => '');
      throw new Error(text || `HTTP ${res.status}`);
    }
  }, []);

  const findAuthIndex = useCallback((def: ReturnType<typeof getProviderDef>, cfg: ProviderConfig): number => {
    return def.authMethods.findIndex((a) => {
      if (cfg.authType === 'cloud_credentials') return Boolean(a.usesCloudCred);
      if (cfg.authType === 'none') return a.id === 'none';
      if (cfg.providerId === 'bedrock') return a.id === 'bedrock_api_key' || a.id === 'api_key';
      return a.id === 'api_key';
    });
  }, []);

  const hydrateFromConfig = useCallback((cfg: ProviderConfig, mode: 'edit' | 'duplicate') => {
    const def = getProviderDef(cfg.providerId);
    const nextDraftId = mode === 'edit' ? cfg.id : crypto.randomUUID();
    const resolvedApiType = cfg.apiType || defaultApiTypeForProvider(cfg.providerId);
    const compatibleEndpointDraft = cfg.providerId === 'compatible'
      ? normalizeCompatibleEndpointDraft(cfg.baseUrl || '', resolvedApiType, cfg.endpointSuffix || '')
      : null;
    const resolvedCompactionEnabled = cfg.providerId === 'openai'
      ? (cfg.compactionEnabled ?? true)
      : cfg.providerId === 'anthropic'
        ? (cfg.compactionEnabled ?? true)
        : cfg.providerId === 'bedrock'
          ? (cfg.compactionEnabled ?? (resolvedApiType === 'messages'))
          : (cfg.compactionEnabled ?? false);
    const resolvedCompactionInstruction = cfg.compactionInstruction || BEDROCK_COMPACTION_DEFAULT_INSTRUCTION;
    const resolvedCompactionTriggerTokens = cfg.compactionTriggerTokens
      ?? (cfg.providerId === 'openai' || cfg.providerId === 'compatible'
        ? OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS
        : BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS);

    setWizardOpen(true);
    setEditId(mode === 'edit' ? cfg.id : null);
    setDraftId(nextDraftId);
    setSelProv(cfg.providerId);
    draftSeedRef.current = cfg;

    const authIdx = findAuthIndex(def, cfg);
    const resolvedMethod = authIdx >= 0 ? def.authMethods[authIdx] : null;
    // A config already on a legacy method keeps it selectable (with a warning);
    // everything else never sees legacy options.
    const keepLegacyId = resolvedMethod?.legacy ? resolvedMethod.id : null;
    setLegacyAuthKeepId(keepLegacyId);
    if (resolvedMethod && (!resolvedMethod.legacy || keepLegacyId)) {
      setSelAuth(authIdx);
    } else {
      setSelAuth(soleOfferedAuthIndex(def, keepLegacyId));
    }

    const nextFields: Record<string, any> = {
      model: cfg.model || '',
      displayName: cfg.displayName || def.name,
      baseUrl: compatibleEndpointDraft ? compatibleEndpointDraft.baseUrl : (cfg.baseUrl || ''),
      apiType: compatibleEndpointDraft ? compatibleEndpointDraft.apiType : resolvedApiType,
      authHeader: cfg.authHeader || '',
      authScheme: cfg.authScheme || '',
      region: cfg.region || '',
      anthropicVersion: cfg.anthropicVersion || '',
      compactionEnabled: resolvedCompactionEnabled,
      compactionInstruction: resolvedCompactionInstruction,
      compactionTriggerTokens: resolvedCompactionTriggerTokens,
      endpointSuffix: compatibleEndpointDraft ? compatibleEndpointDraft.endpointSuffix : (cfg.endpointSuffix || ''),
      timeout: cfg.timeoutMs ? String(cfg.timeoutMs) : '',
      reasoningEffort: cfg.reasoningEffort || 'Default',
      modelInBody: cfg.modelInBody ?? true,
      modelInPath: cfg.modelInPath ?? false,
      queryParams: jsonText(cfg.queryParams),
      payloadOverrides: jsonText(cfg.payloadOverrides),
      webSearchEnabled: cfg.webSearch?.enabled ?? false,
      codeExecutionEnabled: cfg.codeExecution?.enabled ?? false,
      codeExecutionSkills: jsonText(cfg.codeExecution?.skills),
      codeExecutionToolType: cfg.codeExecution?.toolType || '',
      codeExecutionMemoryLimit: cfg.codeExecution?.memoryLimit || '',
      codeExecutionContainerMode: cfg.codeExecution?.containerMode || 'auto',
      codeExecutionContainerId: cfg.codeExecution?.containerId || '',
    };
    setFields(nextFields);

    setExtraHeaders((cfg.extraHeaders || []).map((h) => ({
      id: h.id,
      key: h.key,
      value: mode === 'edit' ? h.value : '',
      isSecret: Boolean(h.isSecret),
    })));

    const cloudId = cfg.cloudCredentialRefs?.awsCredentialId || cfg.cloudCredentialRefs?.gcpCredentialId || null;
    setSelCC(cloudId);

    setStep(0);
    setTestState('idle');
    setTestResult(null);
    setTestedInputKey(null);
  }, [findAuthIndex]);

  const hydrateFromTemplate = useCallback((tpl: ProviderTemplate) => {
    const cfg = tpl.provider as ProviderConfig;
    const def = getProviderDef(cfg.providerId);
    const authIdx = findAuthIndex(def, cfg);
    const resolvedApiType = cfg.apiType || defaultApiTypeForProvider(cfg.providerId);
    const compatibleEndpointDraft = cfg.providerId === 'compatible'
      ? normalizeCompatibleEndpointDraft(cfg.baseUrl || '', resolvedApiType, cfg.endpointSuffix || '')
      : null;
    const resolvedCompactionEnabled = cfg.providerId === 'openai'
      ? (cfg.compactionEnabled ?? true)
      : cfg.providerId === 'anthropic'
        ? (cfg.compactionEnabled ?? true)
        : cfg.providerId === 'bedrock'
          ? (cfg.compactionEnabled ?? (resolvedApiType === 'messages'))
          : (cfg.compactionEnabled ?? false);
    const resolvedCompactionInstruction = cfg.compactionInstruction || BEDROCK_COMPACTION_DEFAULT_INSTRUCTION;
    const resolvedCompactionTriggerTokens = cfg.compactionTriggerTokens
      ?? (cfg.providerId === 'openai' || cfg.providerId === 'compatible'
        ? OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS
        : BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS);

    setWizardOpen(true);
    setEditId(null);
    setDraftId(crypto.randomUUID());
    setSelProv(cfg.providerId);
    draftSeedRef.current = tpl.provider as Partial<ProviderConfig>;
    setLegacyAuthKeepId(null);
    // A template is a NEW configuration: legacy auth methods are never offered,
    // even if the template was saved from a legacy config.
    const resolvedMethod = authIdx >= 0 ? def.authMethods[authIdx] : null;
    if (resolvedMethod && !resolvedMethod.legacy) {
      setSelAuth(authIdx);
      setSelCC(cfg.cloudCredentialRefs?.awsCredentialId || cfg.cloudCredentialRefs?.gcpCredentialId || null);
    } else {
      setSelAuth(soleOfferedAuthIndex(def, null));
      setSelCC(null);
    }

    setFields({
      model: cfg.model || '',
      displayName: cfg.displayName || def.name,
      baseUrl: compatibleEndpointDraft ? compatibleEndpointDraft.baseUrl : (cfg.baseUrl || ''),
      apiType: compatibleEndpointDraft ? compatibleEndpointDraft.apiType : resolvedApiType,
      authHeader: cfg.authHeader || '',
      authScheme: cfg.authScheme || '',
      region: cfg.region || '',
      anthropicVersion: cfg.anthropicVersion || '',
      compactionEnabled: resolvedCompactionEnabled,
      compactionInstruction: resolvedCompactionInstruction,
      compactionTriggerTokens: resolvedCompactionTriggerTokens,
      endpointSuffix: compatibleEndpointDraft ? compatibleEndpointDraft.endpointSuffix : (cfg.endpointSuffix || ''),
      timeout: cfg.timeoutMs ? String(cfg.timeoutMs) : '',
      reasoningEffort: cfg.reasoningEffort || 'Default',
      modelInBody: cfg.modelInBody ?? true,
      modelInPath: cfg.modelInPath ?? false,
      queryParams: jsonText(cfg.queryParams),
      payloadOverrides: jsonText(cfg.payloadOverrides),
      webSearchEnabled: cfg.webSearch?.enabled ?? false,
      codeExecutionEnabled: cfg.codeExecution?.enabled ?? false,
      codeExecutionSkills: jsonText(cfg.codeExecution?.skills),
      codeExecutionToolType: cfg.codeExecution?.toolType || '',
      codeExecutionMemoryLimit: cfg.codeExecution?.memoryLimit || '',
      codeExecutionContainerMode: cfg.codeExecution?.containerMode || 'auto',
      codeExecutionContainerId: cfg.codeExecution?.containerId || '',
    });

    setExtraHeaders((cfg.extraHeaders || []).map((h) => ({
      id: h.id,
      key: h.key,
      value: h.isSecret ? '' : (h.value || ''),
      isSecret: Boolean(h.isSecret),
    })));

    setStep(2);
    setTestState('idle');
    setTestResult(null);
    setTestedInputKey(null);
  }, [findAuthIndex]);

  const openCreate = useCallback(() => {
    resetWizard();
    setWizardOpen(true);
    setDraftId(crypto.randomUUID());
  }, [resetWizard]);

  const openEdit = useCallback((cfg: ProviderConfig) => {
    resetWizard();
    hydrateFromConfig(cfg, 'edit');
  }, [hydrateFromConfig, resetWizard]);

  const openFromTemplate = useCallback((tpl: ProviderTemplate) => {
    resetWizard();
    hydrateFromTemplate(tpl);
  }, [hydrateFromTemplate, resetWizard]);

  const setActiveConfig = useCallback((id: string) => {
    setLocalSettings((prev) => ({
      ...prev,
      activeProviderConfigId: id,
      providerConfigs: (prev.providerConfigs || []).map((cfg) => ({
        ...cfg,
        isDefault: cfg.id === id,
      })),
    }));
  }, [setLocalSettings]);

  useEffect(() => {
    if (!prov) return;
    setFields((prev) => {
      const defaults: Record<string, any> = {};
      prov.config.forEach((f) => {
        if (f.def !== undefined && f.def !== '') defaults[f.key] = f.def;
      });
      return { ...defaults, ...prev };
    });
  }, [prov]);

  const isFieldSecretSet = useCallback((fieldKey: string): boolean => {
    if (secretsSet[fieldKey]) return true;
    if (!selProv) return false;
    if (selProv === 'openai') return isSecretSet('openai_api_key');
    if (selProv === 'anthropic') return isSecretSet('anthropic_api_key');
    if (selProv === 'gemini') return isSecretSet('gemini_api_key');
    if (selProv === 'bedrock') {
      if (auth?.id === 'bedrock_api_key') return isSecretSet('bedrock_api_key');
      return isSecretSet('bedrock_aws_credentials') || isSecretSet('provider:bedrock');
    }
    if (selProv === 'compatible' && compatibleSecretProfileId) {
      return isSecretSet(`compatible:${compatibleSecretProfileId}:api_key`);
    }
    return false;
  }, [auth?.id, compatibleSecretProfileId, isSecretSet, secretsSet, selProv]);

  const handleSetSecret = useCallback(async (fieldKey: string, value: string) => {
    if (!selProv) return;
    try {
      if (selProv === 'bedrock' && auth?.id === 'bedrock_api_key') {
        await postJson('/api/secrets/set', { providerId: 'bedrock', authType: 'api_key', apiKey: value });
      } else if (selProv === 'compatible') {
        if (!compatibleSecretProfileId) {
          toast.error('Cannot set key yet. Re-open the wizard and try again.');
          return;
        }
        await postJson('/api/secrets/set', { providerId: 'compatible', profileId: compatibleSecretProfileId, apiKey: value });
      } else {
        await postJson('/api/secrets/set', { providerId: selProv, apiKey: value });
      }
      setSecretsSet((prev) => ({ ...prev, [fieldKey]: true }));
      toast.success('API key saved securely');
      await refreshSecrets();
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save API key');
    }
  }, [auth?.id, compatibleSecretProfileId, postJson, refreshSecrets, selProv]);

  const handleClearSecret = useCallback(async (fieldKey: string) => {
    if (!selProv) return;
    try {
      if (selProv === 'bedrock' && auth?.id === 'bedrock_api_key') {
        await postJson('/api/secrets/clear', { providerId: 'bedrock', authType: 'api_key' });
      } else if (selProv === 'compatible') {
        if (!compatibleSecretProfileId) return;
        await postJson('/api/secrets/clear', { providerId: 'compatible', profileId: compatibleSecretProfileId });
      } else {
        await postJson('/api/secrets/clear', { providerId: selProv });
      }
      setSecretsSet((prev) => ({ ...prev, [fieldKey]: false }));
      toast.success('API key cleared');
      await refreshSecrets();
    } catch (e: any) {
      toast.error(e?.message || 'Failed to clear API key');
    }
  }, [auth?.id, compatibleSecretProfileId, postJson, refreshSecrets, selProv]);

  const handleSetSecretHeader = useCallback(async (headerKey: string, headerValue: string) => {
    if (!selProv) return;
    try {
      if (selProv === 'compatible') {
        if (!compatibleSecretProfileId) {
          toast.error('Cannot set secret header yet. Re-open the wizard and try again.');
          return;
        }
        await postJson('/api/secrets/set', { providerId: 'compatible', profileId: compatibleSecretProfileId, headerKey, headerValue });
      } else {
        await postJson('/api/secrets/set', { providerId: selProv, headerKey, headerValue });
      }
      toast.success('Secret header saved');
      await refreshSecrets();
    } catch (e: any) {
      toast.error(e?.message || 'Failed to save secret header');
    }
  }, [compatibleSecretProfileId, postJson, refreshSecrets, selProv]);

  const handleClearSecretHeader = useCallback(async (headerKey: string) => {
    if (!selProv) return;
    try {
      if (selProv === 'compatible') {
        if (!compatibleSecretProfileId) return;
        await postJson('/api/secrets/clear', { providerId: 'compatible', profileId: compatibleSecretProfileId, headerKey });
      } else {
        await postJson('/api/secrets/clear', { providerId: selProv, headerKey });
      }
      toast.success('Secret header cleared');
      await refreshSecrets();
    } catch (e: any) {
      toast.error(e?.message || 'Failed to clear secret header');
    }
  }, [compatibleSecretProfileId, postJson, refreshSecrets, selProv]);

  const canProceed = useCallback((): boolean => {
    if (!auth) return false;
    for (const f of auth.fields) {
      if (f.secret && !isFieldSecretSet(f.key)) return false;
      if (!f.secret && !f.optional && !String(fields[f.key] || '').trim()) return false;
    }
    if (!String(fields.model || '').trim()) return false;
    if (selProv === 'compatible' && !String(fields.baseUrl || '').trim()) return false;
    if (auth.usesCloudCred && !selCC) return false;
    return true;
  }, [auth, fields, isFieldSecretSet, selCC, selProv]);

  const buildDraftConfig = useCallback((): ProviderConfig | null => {
    if (!prov || !auth) return null;

    const model = String(fields.model || '').trim();
    if (!model) {
      toast.error('Model is required.');
      return null;
    }

    if (selProv === 'compatible' && !String(fields.baseUrl || '').trim()) {
      toast.error('Base URL is required.');
      return null;
    }

    if (auth.usesCloudCred && !selCC) {
      toast.error('Select cloud credentials before testing or saving.');
      return null;
    }

    let queryParams: Record<string, string> | null = null;
    let payloadOverrides: Record<string, unknown> | null = null;
    let codeExecutionSkills: Array<{ type: string; skillId: string; version?: string | null }> | null = null;
    try {
      const qp = parseOptionalJsonObject(fields.queryParams, 'Query Params');
      if (qp) {
        queryParams = {};
        for (const [k, v] of Object.entries(qp)) {
          if (!k.trim()) continue;
          queryParams[k] = String(v ?? '');
        }
      }
      payloadOverrides = parseOptionalJsonObject(fields.payloadOverrides, 'Payload Overrides');
      codeExecutionSkills = parseOptionalCodeExecutionSkills(fields.codeExecutionSkills);
    } catch (e: any) {
      toast.error(e?.message || 'Invalid JSON in advanced options.');
      return null;
    }

    const timeoutRaw = String(fields.timeout || '').trim();
    let timeoutMs: number | null = null;
    if (timeoutRaw) {
      const parsed = Number(timeoutRaw);
      if (!Number.isFinite(parsed) || parsed <= 0) {
        toast.error('Timeout must be a positive number.');
        return null;
      }
      timeoutMs = Math.round(parsed);
    }

    const existing = editId
      ? (localSettings.providerConfigs || []).find((cfg) => cfg.id === editId)
      : null;
    const now = Date.now();
    const id = editId || draftId || crypto.randomUUID();
    const freshTestResult = testedInputKey === testInputKey ? testResult : null;
    const canCarryExistingTest = Boolean(existing && baselineTestInputKeyRef.current === testInputKey);

    const cloudCredRefs: ProviderConfig['cloudCredentialRefs'] = {};
    if (auth.usesCloudCred && selCC) {
      if (prov.cloudCred === 'aws') cloudCredRefs.awsCredentialId = selCC;
      if (prov.cloudCred === 'gcp') cloudCredRefs.gcpCredentialId = selCC;
    }

    const apiType = String(fields.apiType || '').trim() as ProviderConfig['apiType'];
    const rawCompactionTriggerTokens = String(fields.compactionTriggerTokens ?? '').trim();
    const parsedCompactionTriggerTokens = rawCompactionTriggerTokens
      ? Number(rawCompactionTriggerTokens)
      : Number.NaN;
    const resolvedCompactionTriggerTokens = Number.isFinite(parsedCompactionTriggerTokens)
      ? Math.round(parsedCompactionTriggerTokens)
      : (selProv === 'openai' || selProv === 'compatible'
        ? OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS
        : BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS);
    if (resolvedCompactionTriggerTokens < 50000) {
      toast.error('Compaction Token Limit must be at least 50000.');
      return null;
    }
    const bedrockCompactionEnabled = apiType === 'messages'
      ? fields.compactionEnabled !== false
      : false;
    const bedrockCompactionInstruction = apiType === 'messages'
      ? (String(fields.compactionInstruction || '').trim() || BEDROCK_COMPACTION_DEFAULT_INSTRUCTION)
      : null;
    const bedrockCompactionTriggerTokens = apiType === 'messages'
      ? resolvedCompactionTriggerTokens
      : null;
    const anthropicCompactionEnabled = fields.compactionEnabled !== false;
    const anthropicCompactionInstruction = String(fields.compactionInstruction || '').trim() || BEDROCK_COMPACTION_DEFAULT_INSTRUCTION;
    const anthropicCompactionTriggerTokens = resolvedCompactionTriggerTokens;
    const openAICompactionEnabled = fields.compactionEnabled !== false;
    const openAICompactionTriggerTokens = resolvedCompactionTriggerTokens;
    const compatibleCompactionEnabled = apiType === 'responses' && fields.compactionEnabled === true;
    const compatibleCompactionTriggerTokens = compatibleCompactionEnabled
      ? resolvedCompactionTriggerTokens
      : null;
    const webSearchEnabled = fields.webSearchEnabled === true;
    const providerCanConfigureHostedTools =
      selProv === 'openai' ||
      selProv === 'anthropic' ||
      (selProv === 'compatible' && apiType === 'responses');
    const webSearch = providerCanConfigureHostedTools
      ? canonicalProviderWebSearch(selProv, webSearchEnabled)
      : null;
    const codeExecutionEnabled = fields.codeExecutionEnabled === true;
    const codeExecutionContainerMode = String(fields.codeExecutionContainerMode || '').trim();
    const normalizedCodeExecutionContainerMode =
      (codeExecutionContainerMode === 'auto' || codeExecutionContainerMode === 'existing')
        ? codeExecutionContainerMode
        : null;
    const codeExecution = providerCanConfigureHostedTools && codeExecutionEnabled ? {
      enabled: true,
      toolType: String(fields.codeExecutionToolType || '').trim() || null,
      memoryLimit: String(fields.codeExecutionMemoryLimit || '').trim() || null,
      containerMode: normalizedCodeExecutionContainerMode as 'auto' | 'existing' | null,
      containerId: String(fields.codeExecutionContainerId || '').trim() || null,
      skills: codeExecutionSkills,
    } : null;
    if (selProv === 'compatible' && apiType !== 'responses' && (webSearchEnabled || codeExecutionEnabled)) {
      toast.error('OpenAI-compatible web search and code sandbox require the Responses API.');
      return null;
    }

    // Non-destructive rebuild: seed with the config being edited (or the
    // duplicate/template source) so capabilityControls, attachmentPolicies,
    // apiSurface, and unknown forward-compatible fields survive; every edited
    // field below overwrites the seed. configFingerprint is stripped so the
    // server recomputes it for the changed config.
    const seedSource = existing || draftSeedRef.current;
    const seed = seedSource
      ? (stripVolatileConfigFields(seedSource) as Partial<ProviderConfig>)
      : {};

    const cfg: ProviderConfig = {
      ...seed,
      id,
      displayName: String(fields.displayName || prov.name).trim() || prov.name,
      providerId: selProv!,
      model,
      apiType,
      baseUrl: String(fields.baseUrl || '').trim() || null,
      authType: auth.usesCloudCred ? 'cloud_credentials' : auth.id === 'none' ? 'none' : 'api_key',
      authHeader: String(fields.authHeader || '').trim() || null,
      authScheme: String(fields.authScheme || '').trim() || null,
      cloudCredentialRefs: Object.keys(cloudCredRefs).length > 0 ? cloudCredRefs : undefined,
      region: String(fields.region || '').trim() || null,
      anthropicVersion: String(fields.anthropicVersion || '').trim() || null,
      compactionEnabled: selProv === 'bedrock'
        ? bedrockCompactionEnabled
        : selProv === 'anthropic'
          ? anthropicCompactionEnabled
          : selProv === 'openai'
            ? openAICompactionEnabled
            : selProv === 'compatible'
              ? compatibleCompactionEnabled
              : undefined,
      compactionInstruction: selProv === 'bedrock'
        ? bedrockCompactionInstruction
        : selProv === 'anthropic'
          ? anthropicCompactionInstruction
          : undefined,
      compactionTriggerTokens: selProv === 'bedrock'
        ? bedrockCompactionTriggerTokens
        : selProv === 'anthropic'
          ? anthropicCompactionTriggerTokens
          : selProv === 'openai'
            ? openAICompactionTriggerTokens
            : selProv === 'compatible'
              ? compatibleCompactionTriggerTokens
              : undefined,
      endpointSuffix: String(fields.endpointSuffix || '').trim() || null,
      timeoutMs,
      reasoningEffort: fields.reasoningEffort && fields.reasoningEffort !== 'Default' ? String(fields.reasoningEffort) : null,
      modelInPath: Boolean(fields.modelInPath ?? false),
      modelInBody: fields.modelInBody !== false,
      extraHeaders: extraHeaders
        .filter((h) => h.key.trim())
        .map((h) => ({
          id: h.id,
          key: h.key,
          value: h.isSecret ? '' : (h.value || ''),
          isSecret: h.isSecret,
      })),
      queryParams,
      payloadOverrides,
      webSearch,
      codeExecution,
      isDefault: true,
      createdAt: existing?.createdAt || now,
      updatedAt: now,
      lastTestedAt: freshTestResult?.timestamp || (canCarryExistingTest ? existing?.lastTestedAt : null) || null,
      lastTestStatus: freshTestResult ? (freshTestResult.ok ? 'success' : 'failure') : (canCarryExistingTest ? existing?.lastTestStatus : null) || null,
      lastTestMessage: freshTestResult?.message || (canCarryExistingTest ? existing?.lastTestMessage : null) || null,
      lastTestEndpoint: freshTestResult?.endpoint || (canCarryExistingTest ? existing?.lastTestEndpoint : null) || null,
    };

    // The canonical surface follows the edited apiType; an unchanged surface
    // keeps the seed's value verbatim. Clear the stale explicit value before
    // deriving — deriveProviderApiSurface prefers an explicit apiSurface.
    const seedApiType = (seedSource as Partial<ProviderConfig> | null)?.apiType;
    if (seed.apiSurface !== undefined && seedApiType && seedApiType !== apiType) {
      cfg.apiSurface = deriveProviderApiSurface({ ...cfg, apiSurface: undefined });
    }

    return cfg;
  }, [auth, draftId, editId, extraHeaders, fields, localSettings.providerConfigs, prov, selCC, selProv, testInputKey, testResult, testedInputKey]);

  const runProviderConnectionTest = useCallback(async (cfg: ProviderConfig, signal?: AbortSignal): Promise<TestResult> => {
    const endpoint = buildProviderEndpoint(cfg.providerId, cfg);
    const timestamp = Date.now();

    if (!cfg.model?.trim()) {
      return { ok: false, message: 'Model is required.', endpoint, timestamp };
    }

    try {
      const result = await testProviderConnection(cfg, endpoint, signal);
      return {
        ok: result.ok,
        message: result.message,
        endpoint: result.endpoint || endpoint,
        timestamp,
      };
    } catch (e: any) {
      if (e?.name === 'AbortError') throw e;
      return {
        ok: false,
        message: e?.message || 'Connection test failed.',
        endpoint,
        timestamp,
      };
    }
  }, []);

  const handleTest = useCallback(async () => {
    const cfg = buildDraftConfig();
    if (!cfg) return;

    testAbortRef.current?.abort();
    const controller = new AbortController();
    const inputKey = latestTestInputKeyRef.current;
    testAbortRef.current = controller;
    setTestState('testing');
    setTestResult(null);
    setTestedInputKey(null);

    let result: TestResult;
    try {
      result = await runProviderConnectionTest(cfg, controller.signal);
    } catch (e: any) {
      if (e?.name === 'AbortError') return;
      result = {
        ok: false,
        message: e?.message || 'Connection test failed.',
        endpoint: buildProviderEndpoint(cfg.providerId, cfg),
        timestamp: Date.now(),
      };
    } finally {
      if (testAbortRef.current === controller) testAbortRef.current = null;
    }

    if (controller.signal.aborted || latestTestInputKeyRef.current !== inputKey) return;
    setTestResult(result);
    setTestedInputKey(inputKey);
    setTestState(result.ok ? 'success' : 'failure');
  }, [buildDraftConfig, runProviderConnectionTest]);

  const withSavedProviderConfig = useCallback((base: AppSettings, cfg: ProviderConfig): AppSettings => {
    const existing = base.providerConfigs || [];
    const idx = existing.findIndex((c) => c.id === cfg.id);
    const nextConfigs = [...existing];
    if (idx >= 0) nextConfigs[idx] = cfg;
    else nextConfigs.push(cfg);

    return {
      ...base,
      providerConfigs: nextConfigs.map((entry) => ({
        ...entry,
        isDefault: entry.id === cfg.id,
      })),
      activeProviderConfigId: cfg.id,
    };
  }, []);

  const commitSave = useCallback(async (cfg: ProviderConfig) => {
    // Create-only capability defaults (consent features Off; never reapplied on
    // edit). They apply to every NEW config whenever the capability editor or
    // controls flow is active.
    const capabilityFlowActive = editorV2On || capabilityFlags.controls;
    const finalCfg = capabilityFlowActive && !editId ? applyNewConnectionCapabilityDefaults(cfg) : cfg;
    const inheritWebSearch = !editId && isConnectionEnabledForRuns(localSettings, 'web-search');
    let nextSettings = withSavedProviderConfig(localSettings, finalCfg);
    if (inheritWebSearch) {
      nextSettings = withConnectionEnabled(nextSettings, 'web-search', true);
    }
    setSavingProvider(true);
    try {
      await Promise.resolve(onSave(applyProviderConfigToLegacySettings(nextSettings)));
      setLocalSettings(nextSettings);
      resetWizard();
    } finally {
      setSavingProvider(false);
    }
  }, [capabilityFlags.controls, editId, editorV2On, localSettings, onSave, resetWizard, setLocalSettings, withSavedProviderConfig]);

  // Persist a single connection edit (capability controls, attachment policies)
  // without changing which connection is the default.
  const handleUpdateConfig = useCallback(async (next: ProviderConfig) => {
    const existing = localSettings.providerConfigs || [];
    const nextSettings: AppSettings = {
      ...localSettings,
      providerConfigs: existing.map((c) => (c.id === next.id ? next : c)),
    };
    await Promise.resolve(onSave(applyProviderConfigToLegacySettings(nextSettings)));
    setLocalSettings(nextSettings);
  }, [localSettings, onSave, setLocalSettings]);

  const handleSave = useCallback(async () => {
    const cfg = buildDraftConfig();
    if (!cfg) return;

    if (testState === 'testing' || savingProvider) {
      toast.error('Wait for the current connection test to finish before saving.');
      return;
    }

    const freshTestState: TestState = testedInputKey === testInputKey ? testState : 'idle';
    if (freshTestState !== 'success') {
      const message = freshTestState === 'failure'
        ? 'The last connection test failed. Save this provider anyway?'
        : 'This provider has not passed a connection test. Save it anyway?';
      if (capabilityUxOn || editorV2On) {
        // Inline confirm panel (quality bar: no window.confirm in touched flows).
        setPendingSaveConfirm(message);
        return;
      }
      if (!window.confirm(message)) return;
    }

    await commitSave(cfg);
  }, [buildDraftConfig, capabilityUxOn, commitSave, editorV2On, savingProvider, testInputKey, testState, testedInputKey]);

  const handleConfirmSaveAnyway = useCallback(async () => {
    setPendingSaveConfirm(null);
    const cfg = buildDraftConfig();
    if (!cfg) return;
    if (testState === 'testing' || savingProvider) return;
    await commitSave(cfg);
  }, [buildDraftConfig, commitSave, savingProvider, testState]);

  // Step 1 of the inline template flow: capture the draft and ask for a name
  // (no window.prompt — the name field renders inline with confirm/cancel).
  const handleSaveCurrentAsTemplate = useCallback(() => {
    if (!templatesEnabled) return;
    const cfg = buildDraftConfig();
    if (!cfg) return;
    setTemplateDraft({ cfg, name: `${cfg.displayName || cfg.providerId} template` });
  }, [buildDraftConfig, templatesEnabled]);

  const handleConfirmSaveTemplate = useCallback(() => {
    if (!templateDraft) return;
    const name = templateDraft.name.trim();
    if (!name) {
      toast.error('Template name is required.');
      return;
    }

    // Templates carry configuration intent only: identity, lifecycle,
    // test-result, and server-owned evidence/fingerprint fields are stripped;
    // capabilityControls and attachmentPolicies are preserved.
    const provider = stripVolatileConfigFields(
      templateDraft.cfg,
      /fingerprint|evidence/i,
    ) as ProviderTemplate['provider'];

    const tpl: ProviderTemplate = {
      id: crypto.randomUUID(),
      name,
      provider,
    };

    setLocalSettings((prev) => ({
      ...prev,
      providerTemplates: [...(prev.providerTemplates || []), tpl],
    }));

    setTemplateDraft(null);
    toast.success('Template saved.');
  }, [setLocalSettings, templateDraft]);

  const handleDelete = useCallback((id: string) => {
    setLocalSettings((prev) => {
      const remaining = (prev.providerConfigs || []).filter((c) => c.id !== id);
      const nextActive = prev.activeProviderConfigId === id
        ? (remaining[0]?.id || null)
        : prev.activeProviderConfigId;
      return {
        ...prev,
        providerConfigs: remaining,
        activeProviderConfigId: nextActive,
      };
    });
    setDeletingId(null);
  }, [setLocalSettings]);

  const handleDeleteTemplate = useCallback((id: string) => {
    setLocalSettings((prev) => ({
      ...prev,
      providerTemplates: (prev.providerTemplates || []).filter((tpl) => tpl.id !== id),
    }));
  }, [setLocalSettings]);

  const handleSaveInlineCC = useCallback(async () => {
    if (!prov || !prov.cloudCred) return;

    const id = crypto.randomUUID();
    const displayName = String(ccFields.name || (prov.cloudCred === 'aws' ? 'AWS Credentials' : 'GCP Credentials')).trim();

    if (prov.cloudCred === 'aws') {
      if (!ccSecrets.ak || !ccSecrets.sk || !ccFields.akVal || !ccFields.skVal) {
        toast.error('Access key ID and secret access key are required.');
        return;
      }
      try {
        await postJson('/api/secrets/set', {
          providerId: 'bedrock',
          authType: 'aws_credentials',
          accessKeyId: String(ccFields.akVal || ''),
          secretAccessKey: String(ccFields.skVal || ''),
          sessionToken: String(ccFields.stVal || ''),
        });
      } catch (e: any) {
        toast.error(e?.message || 'Failed to store AWS credentials');
        return;
      }

      const newCred = {
        id,
        displayName: displayName || 'AWS Credentials',
        region: String(ccFields.region || 'us-east-1'),
        authMode: 'access_key' as const,
        accessKeyId: String(ccFields.ak || ''),
        secretAccessKeySet: true,
        sessionTokenSet: Boolean(ccSecrets.st),
      };

      setLocalSettings((prev) => ({
        ...prev,
        cloudCredentials: {
          aws: [...(prev.cloudCredentials?.aws || []), newCred],
          gcp: prev.cloudCredentials?.gcp || [],
        },
      }));
      setSelCC(id);
    }

    if (prov.cloudCred === 'gcp') {
      const newCred = {
        id,
        displayName: displayName || 'GCP Credentials',
        projectId: String(ccFields.projectId || '').trim() || null,
        adcMode: 'auto' as const,
        credentialsPath: null,
      };

      setLocalSettings((prev) => ({
        ...prev,
        cloudCredentials: {
          aws: prev.cloudCredentials?.aws || [],
          gcp: [...(prev.cloudCredentials?.gcp || []), newCred],
        },
      }));
      setSelCC(id);
    }

    setInlineCC(false);
    setCcFields({});
    setCcSecrets({});
    toast.success('Cloud credential saved.');
  }, [ccFields, ccSecrets, postJson, prov, setLocalSettings]);

  const stepLabels = multi ? ['Provider', 'Auth', 'Configure', 'Test'] : ['Provider', 'Configure', 'Test'];
  const stepMap = multi ? [0, 1, 2, 3] : [0, 2, 3];

  const configGroups = useMemo(() => {
    if (!prov) return [];
    return [
      {
        key: 'identity',
        fields: prov.config.filter((f) => f.group === 'identity'),
      },
      {
        key: 'config',
        fields: prov.config.filter((f) => {
          if (f.group !== 'config') return false;
          // Provider-hosted tools are configured as first-class Connections.
          // Keep their draft hydration/build logic intact so editing transport
          // settings cannot erase the connection-owned values.
          if (isConnectionManagedHostedToolField(f.key)) return false;
          // Surface-first IA: with the v2 editor, the API surface is chosen on the
          // Service step, so the raw apiType select is not repeated here.
          if (editorV2On && f.key === 'apiType') return false;
          if (prov.id === 'anthropic') {
            if (f.key === 'compactionEnabled') return true;
            if (f.key === 'compactionInstruction') return fields.compactionEnabled !== false;
            if (f.key === 'compactionTriggerTokens') return fields.compactionEnabled !== false;
            return true;
          }
          if (prov.id === 'openai') {
            if (f.key === 'compactionEnabled') return true;
            if (f.key === 'compactionTriggerTokens') return fields.compactionEnabled !== false;
            return true;
          }
          if (prov.id === 'compatible') {
            const apiType = String(fields.apiType || '').trim();
            if (f.key === 'compactionEnabled') return apiType === 'responses';
            if (f.key === 'compactionTriggerTokens') return apiType === 'responses' && fields.compactionEnabled === true;
            return true;
          }
          if (prov.id === 'bedrock') {
            const apiType = String(fields.apiType || '').trim();
            if (f.key === 'compactionEnabled') return apiType === 'messages';
            if (f.key === 'compactionInstruction') return apiType === 'messages' && fields.compactionEnabled !== false;
            if (f.key === 'compactionTriggerTokens') return apiType === 'messages' && fields.compactionEnabled !== false;
            return true;
          }
          return true;
        }),
      },
      { key: 'auth_detail', fields: prov.config.filter((f) => f.group === 'auth_detail'), hide: auth?.id === 'none' },
      { key: 'placement', fields: prov.config.filter((f) => f.group === 'placement') },
    ].filter((g) => g.fields.length > 0 && !g.hide);
  }, [prov, auth, editorV2On, fields.apiType, fields.compactionEnabled]);

  const advFields = useMemo(() => {
    if (!prov) return [];
    return prov.config.filter((f) => {
      if (f.group !== 'advanced') return false;
      if (isConnectionManagedHostedToolField(f.key)) return false;
      if (prov.id === 'compatible' && f.key === 'endpointSuffix') return false;
      return true;
    });
  }, [prov]);

  const compatibleEndpointPreview = useMemo(() => {
    if (selProv !== 'compatible') return null;

    const apiType = fields.apiType === 'responses' ? 'responses' : 'chat_completions';
    const baseUrl = String(fields.baseUrl || '');
    const queryParams = parseOptionalStringMapLoose(fields.queryParams);

    return {
      apiType,
      endpointSegment: getCompatibleEndpointSegment(apiType),
      baseDisplay: baseUrl.trim().replace(/\/+$/, ''),
      endpointPreview: buildCompatibleEndpointUrl({
        baseUrl,
        apiType,
        model: String(fields.model || '').trim(),
        modelInPath: Boolean(fields.modelInPath),
        endpointSuffix: String(fields.endpointSuffix || ''),
        queryParams,
      }),
      hasQueryParams: Boolean(queryParams && Object.keys(queryParams).length > 0),
    };
  }, [fields.apiType, fields.baseUrl, fields.endpointSuffix, fields.model, fields.modelInPath, fields.queryParams, selProv]);

  const previewEndpoint = useMemo(() => {
    if (!prov) return '';

    return buildProviderEndpoint(prov.id, {
      id: draftId || editId || 'preview',
      displayName: String(fields.displayName || prov.name).trim() || prov.name,
      providerId: prov.id,
      model: String(fields.model || '').trim(),
      apiType: (String(fields.apiType || defaultApiTypeForProvider(prov.id)).trim() as ProviderConfig['apiType']) || undefined,
      baseUrl: String(fields.baseUrl || '').trim() || null,
      region: String(fields.region || '').trim() || null,
      endpointSuffix: String(fields.endpointSuffix || '').trim() || null,
      modelInPath: Boolean(fields.modelInPath),
      queryParams: parseOptionalStringMapLoose(fields.queryParams),
    });
  }, [draftId, editId, fields.apiType, fields.baseUrl, fields.displayName, fields.endpointSuffix, fields.model, fields.modelInPath, fields.queryParams, fields.region, prov]);

  // Side-effect-free draft config for capability/discovery previews (never toasts,
  // unlike buildDraftConfig). Support facts come from the backend report only.
  const draftPreviewConfig = useMemo((): ProviderConfig | null => {
    if ((!capabilityUxOn && !editorV2On) || !selProv) return null;
    const apiTypeRaw = String(fields.apiType || '').trim();
    return {
      id: draftId || editId || 'draft-preview',
      displayName: String(fields.displayName || prov?.name || selProv).trim() || selProv,
      providerId: selProv,
      model: String(fields.model || '').trim(),
      apiType: (apiTypeRaw || undefined) as ProviderConfig['apiType'],
      baseUrl: String(fields.baseUrl || '').trim() || null,
      region: String(fields.region || '').trim() || null,
      compactionEnabled: fields.compactionEnabled !== false,
      webSearch: canonicalProviderWebSearch(selProv, fields.webSearchEnabled === true),
      codeExecution: fields.codeExecutionEnabled === true
        ? { enabled: true, toolType: null, memoryLimit: null, containerMode: null, containerId: null, skills: null }
        : null,
      queryParams: parseOptionalStringMapLoose(fields.queryParams),
      payloadOverrides: parseOptionalStringMapLoose(fields.payloadOverrides),
    };
  }, [capabilityUxOn, editorV2On, draftId, editId, fields.apiType, fields.baseUrl, fields.codeExecutionEnabled, fields.compactionEnabled, fields.displayName, fields.model, fields.payloadOverrides, fields.queryParams, fields.region, fields.webSearchEnabled, prov, selProv]);

  const draftCapability = useCapabilityReportSeam(
    draftPreviewConfig,
    capabilityUxOn && wizardOpen && step === 3
  );

  const renderFieldControl = useCallback((f: ConfigField) => {
    if (f.type === 'toggle') {
      return (
        <label className="flex items-center gap-2.5 cursor-pointer">
          <Switch
            checked={fields[f.key] ?? f.def ?? false}
            onCheckedChange={(checked) => setFields((p) => ({ ...p, [f.key]: checked }))}
          />
          <span className="text-xs">{f.label}</span>
        </label>
      );
    }

    return (
      <>
        <Label className="text-xs">{f.label}</Label>
        {selProv === 'compatible' && f.key === 'baseUrl' ? (
          <Input
            type="text"
            placeholder={f.placeholder}
            value={fields[f.key] ?? ''}
            onChange={(e) => {
              const nextValue = e.target.value;
              const parsed = splitCompatibleEndpointFromUrl(nextValue);
              if (parsed.includesEndpoint) {
                setFields((p) => ({
                  ...p,
                  baseUrl: parsed.baseUrl || nextValue,
                  apiType: parsed.apiType || (p.apiType === 'responses' ? 'responses' : 'chat_completions'),
                  endpointSuffix: parsed.endpointSuffix || '',
                }));
                return;
              }
              setFields((p) => ({ ...p, [f.key]: nextValue }));
            }}
            className="font-mono"
          />
        ) : f.type === 'select' ? (
          <Select
            value={String(fields[f.key] ?? f.def ?? '')}
            onValueChange={(v) => setFields((p) => ({ ...p, [f.key]: v }))}
          >
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              {(f.options || []).map((o) => (
                <SelectItem key={o} value={o}>{o}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        ) : f.type === 'json' ? (
          <Textarea
            placeholder={f.placeholder}
            value={fields[f.key] ?? ''}
            onChange={(e) => setFields((p) => ({ ...p, [f.key]: e.target.value }))}
            className="font-mono text-xs min-h-[88px]"
          />
        ) : f.type === 'textarea' ? (
          <Textarea
            placeholder={f.placeholder}
            value={fields[f.key] ?? ''}
            onChange={(e) => setFields((p) => ({ ...p, [f.key]: e.target.value }))}
            className="text-xs min-h-[92px]"
          />
        ) : (
          <Input
            type={f.type === 'number' ? 'number' : 'text'}
            placeholder={f.placeholder}
            value={fields[f.key] ?? ''}
            onChange={(e) => setFields((p) => ({ ...p, [f.key]: e.target.value }))}
            className={f.type !== 'number' ? 'font-mono' : ''}
          />
        )}
      </>
    );
  }, [fields, selProv]);

  if (!wizardOpen) {
    const detailCfg = capabilityUxOn && detailId
      ? visibleConfigs.find((c) => c.id === detailId) || null
      : null;
    if (detailCfg) {
      return (
        <ConnectionDetail
          cfg={detailCfg}
          isActive={activeConfig?.id === detailCfg.id}
          flags={capabilityFlags}
          onBack={() => setDetailId(null)}
          onUse={() => setActiveConfig(detailCfg.id)}
          onEdit={() => openEdit(detailCfg)}
          onUpdateConfig={capabilityFlags.controls ? handleUpdateConfig : undefined}
        />
      );
    }

    return (
      <div className="space-y-4">
        {templatesEnabled && visibleTemplates.length > 0 && (
          <div className="rounded-lg border border-border bg-card/30 p-3 space-y-2">
            <div className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
              <BookmarkPlus size={12} /> Provider Templates
            </div>
            {visibleTemplates.map((tpl) => {
              const provider = tpl.provider as ProviderConfig;
              const def = getProviderDef(provider.providerId);
              return (
                <div key={tpl.id} className="flex items-center justify-between gap-2 p-2 rounded-md border border-border bg-card/50">
                  <div className="min-w-0">
                    <div className="text-xs font-medium truncate">{tpl.name}</div>
                    <div className="text-[11px] text-muted-foreground">
                      {def.name}
                      {provider.model ? <span className="font-mono"> · {provider.model}</span> : null}
                    </div>
                  </div>
                  <div className="flex items-center gap-1">
                    <Button variant="outline" size="sm" className="h-7 px-2 text-[11px]" onClick={() => openFromTemplate(tpl)}>
                      Use
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7 text-destructive"
                      onClick={() => handleDeleteTemplate(tpl.id)}
                    >
                      <Trash2 size={13} />
                    </Button>
                  </div>
                </div>
              );
            })}
          </div>
        )}

        {visibleConfigs.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-12 text-center">
            <div className="w-10 h-10 rounded-lg bg-muted border border-border flex items-center justify-center text-muted-foreground mb-3">
              <Globe size={18} />
            </div>
            <p className="text-sm font-medium text-muted-foreground mb-1">No providers configured yet</p>
            <p className="text-xs text-muted-foreground/70 mb-4">Add a provider to connect to an LLM API</p>
            <Button onClick={openCreate} className="gap-2">
              <Plus size={14} /> New Provider
            </Button>
          </div>
        ) : (
          <>
            <div className="space-y-2">
              {visibleConfigs.map((cfg) => {
                const def = getProviderDef(cfg.providerId);
                const isActive = activeConfig?.id === cfg.id;

                if (capabilityUxOn) {
                  return (
                    <ConnectionCard
                      key={cfg.id}
                      cfg={cfg}
                      isActive={isActive}
                      deleting={deletingId === cfg.id}
                      defaultForNewChatsWording={capabilityFlags.composerTarget}
                      onUse={() => setActiveConfig(cfg.id)}
                      onEdit={() => openEdit(cfg)}
                      onOpenDetail={() => setDetailId(cfg.id)}
                      onDeleteRequest={() => setDeletingId(cfg.id)}
                      onDeleteConfirm={() => handleDelete(cfg.id)}
                      onDeleteCancel={() => setDeletingId(null)}
                    />
                  );
                }

                return (
                  <div
                    key={cfg.id}
                    className="flex items-center justify-between p-3 rounded-lg border bg-card/50 hover:bg-card/80 transition-colors gap-2 flex-wrap"
                    style={{ borderColor: isActive ? `${def.color}50` : undefined }}
                  >
                    <div className="flex items-center gap-3 min-w-0 flex-1">
                      <div
                        className="w-8 h-8 rounded-md border flex items-center justify-center text-sm font-bold shrink-0"
                        style={{
                          color: def.color,
                          backgroundColor: `${def.color}14`,
                          borderColor: `${def.color}30`,
                        }}
                      >
                        {def.icon}
                      </div>
                      <div className="min-w-0">
                        <div className="text-sm font-medium truncate flex items-center gap-2">
                          {cfg.displayName || def.name}
                          {isActive && (
                            <span className="inline-flex items-center gap-1 text-[10px] font-semibold text-primary bg-primary/10 px-1.5 py-0.5 rounded">
                              <Star size={10} /> Active
                            </span>
                          )}
                        </div>
                        <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground flex-wrap">
                          {cfg.model && <span className="font-mono">{cfg.model}</span>}
                          {cfg.authType && (
                            <>
                              <span className="text-border">·</span>
                              <span>{cfg.authType === 'cloud_credentials' ? 'Cloud Creds' : cfg.authType === 'none' ? 'No Auth' : 'API Key'}</span>
                            </>
                          )}
                          {cfg.lastTestStatus && (
                            <>
                              <span className="text-border">·</span>
                              <span className={cfg.lastTestStatus === 'success' ? 'text-emerald-400' : 'text-destructive'}>
                                Last test: {cfg.lastTestStatus}
                              </span>
                            </>
                          )}
                        </div>
                        <div className="text-[10px] text-muted-foreground/80">{formatTimestamp(cfg.lastTestedAt)}</div>
                      </div>
                    </div>

                    <div className="flex items-center gap-1">
                      {!isActive && (
                        <Button variant="outline" size="sm" className="h-7 px-2 text-[11px]" onClick={() => setActiveConfig(cfg.id)}>
                          Use
                        </Button>
                      )}
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7 text-muted-foreground"
                        onClick={() => openEdit(cfg)}
                      >
                        <Pencil size={13} />
                      </Button>
                      {deletingId === cfg.id ? (
                        <div className="flex items-center gap-1">
                          <Button size="sm" variant="destructive" className="h-6 px-2 text-[11px]" onClick={() => handleDelete(cfg.id)}>
                            Delete
                          </Button>
                          <Button size="sm" variant="outline" className="h-6 px-2 text-[11px]" onClick={() => setDeletingId(null)}>
                            Cancel
                          </Button>
                        </div>
                      ) : (
                        <Button variant="ghost" size="icon" className="h-7 w-7 text-muted-foreground" onClick={() => setDeletingId(cfg.id)}>
                          <Trash2 size={13} />
                        </Button>
                      )}
                    </div>
                  </div>
                );
              })}
            </div>

            <button
              className="w-full flex items-center justify-center gap-2 py-2 px-4 rounded-lg border border-dashed border-border text-xs text-muted-foreground hover:text-foreground hover:border-foreground/20 transition-colors"
              onClick={openCreate}
            >
              <Plus size={14} /> Add Provider
            </button>
          </>
        )}

      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-center gap-0 pt-1 pb-2">
        {stepLabels.map((label, i) => {
          const myStep = stepMap[i];
          const active = step === myStep;
          const done = step > myStep;
          const provColor = prov?.color || '#D97757';
          return (
            <React.Fragment key={i}>
              <div className="flex items-center gap-1.5">
                <div
                  className="w-[22px] h-[22px] rounded-full border-2 flex items-center justify-center text-[11px] font-semibold shrink-0 transition-all"
                  style={{
                    borderColor: active ? provColor : done ? '#34D399' : 'hsl(var(--border))',
                    color: active ? provColor : done ? '#34D399' : 'hsl(var(--muted-foreground))',
                    backgroundColor: active ? `${provColor}12` : done ? '#34D39912' : 'transparent',
                  }}
                >
                  {done ? <Check size={12} /> : <span>{i + 1}</span>}
                </div>
                <span
                  className="text-[11px] font-medium whitespace-nowrap transition-colors"
                  style={{ color: active ? 'hsl(var(--foreground))' : done ? 'hsl(var(--muted-foreground))' : 'hsl(var(--muted-foreground)/50%)' }}
                >
                  {label}
                </span>
              </div>
              {i < stepLabels.length - 1 && (
                <div className="w-5 h-0.5 rounded-full mx-1 transition-colors" style={{ backgroundColor: done ? '#34D39940' : 'hsl(var(--border))' }} />
              )}
            </React.Fragment>
          );
        })}
      </div>

      {step === 0 && (
        <div className="space-y-4">
          <div>
            <h3 className="text-sm font-semibold mb-1">{editorV2On ? 'Choose a service' : 'Choose a provider'}</h3>
            <p className="text-xs text-muted-foreground">
              {editorV2On
                ? 'Pick the provider family and the exact API surface to connect to'
                : 'Select which LLM service to connect to'}
            </p>
          </div>
          {editorV2On && (
            <ServiceSurfaceStep
              providers={availableProviders}
              selectedProviderId={selProv}
              selectedApiType={(String(fields.apiType || '').trim() || null) as any}
              onSelect={(providerId, apiType) => {
                const def = getProviderDef(providerId);
                setSelProv(providerId);
                setSelAuth(soleOfferedAuthIndex(def, legacyAuthKeepId));
                setFields((p) => {
                  const next = { ...p };
                  if (apiType) next.apiType = apiType;
                  else delete next.apiType;
                  return next;
                });
                if (!draftId) setDraftId(crypto.randomUUID());
              }}
            />
          )}
          {!editorV2On && (
          <div className="grid grid-cols-2 sm:grid-cols-3 gap-2">
            {availableProviders.map((p) => {
              const selected = selProv === p.id;
              return (
                <button
                  key={p.id}
                  className="relative flex flex-col items-start p-3 rounded-lg border bg-card/50 text-left transition-all hover:bg-card/80"
                  style={{
                    borderColor: selected ? p.color : 'hsl(var(--border))',
                    boxShadow: selected ? `0 0 0 1px ${p.color}40` : undefined,
                    backgroundColor: selected ? `${p.color}08` : undefined,
                  }}
                  onClick={() => {
                    setSelProv(p.id);
                    setSelAuth(soleOfferedAuthIndex(p, legacyAuthKeepId));
                    if (!draftId) setDraftId(crypto.randomUUID());
                  }}
                >
                  <div className="w-7 h-7 rounded-md border flex items-center justify-center text-xs font-bold mb-2" style={{ color: p.color, backgroundColor: `${p.color}14`, borderColor: `${p.color}30` }}>
                    {p.icon}
                  </div>
                  <div className="text-xs font-medium mb-0.5">{p.name}</div>
                  <div className="text-[10px] text-muted-foreground leading-snug">{p.desc}</div>
                  {selected && (
                    <div className="absolute top-1.5 right-1.5 w-4 h-4 rounded-full flex items-center justify-center text-white" style={{ backgroundColor: p.color }}>
                      <Check size={10} />
                    </div>
                  )}
                </button>
              );
            })}
          </div>
          )}
          <div className="flex items-center justify-between pt-3 border-t border-border">
            <Button variant="outline" size="sm" onClick={resetWizard}>Cancel</Button>
            <Button size="sm" disabled={!selProv} onClick={() => setStep(multi ? 1 : 2)} className="gap-1">
              Continue <ArrowRight size={14} />
            </Button>
          </div>
        </div>
      )}

      {step === 1 && multi && prov && (
        <div className="space-y-4">
          <div>
            <h3 className="text-sm font-semibold mb-1">Choose authentication</h3>
            <p className="text-xs text-muted-foreground">How to authenticate with {prov.name}?</p>
          </div>
          <div className="space-y-2">
            {prov.authMethods.map((a, i) => {
              if (!isAuthMethodOffered(a)) return null;
              const selected = selAuth === i;
              const hasCloudCred = a.usesCloudCred && matchingCloudCreds.length > 0;
              return (
                <button
                  key={a.id}
                  className="w-full flex flex-col gap-2 p-3 rounded-lg border bg-card/50 text-left transition-all hover:bg-card/80"
                  style={{
                    borderColor: selected ? prov.color : 'hsl(var(--border))',
                    boxShadow: selected ? `0 0 0 1px ${prov.color}40` : undefined,
                    backgroundColor: selected ? `${prov.color}08` : undefined,
                  }}
                  onClick={() => setSelAuth(i)}
                >
                  <div className="flex items-start gap-3">
                    <div className="w-4 h-4 rounded-full border-2 flex items-center justify-center shrink-0 mt-0.5 transition-colors" style={{ borderColor: selected ? prov.color : 'hsl(var(--border))' }}>
                      {selected ? <div className="w-2 h-2 rounded-full" style={{ backgroundColor: prov.color }} /> : null}
                    </div>
                    <div className="flex-1">
                      <div className="flex items-center gap-2 flex-wrap">
                        <span className="text-xs font-medium">{a.label}</span>
                        {a.legacy && (
                          <span data-legacy-auth-badge className="text-[10px] text-amber-500 font-medium bg-amber-500/10 px-1.5 py-0.5 rounded">
                            Legacy
                          </span>
                        )}
                        {a.usesCloudCred && hasCloudCred && (
                          <span className="inline-flex items-center gap-1 text-[10px] text-emerald-400 font-medium bg-emerald-400/10 px-1.5 py-0.5 rounded">
                            <Check size={10} /> Available
                          </span>
                        )}
                        {a.usesCloudCred && !hasCloudCred && (
                          <span className="text-[10px] text-amber-500 font-medium bg-amber-500/10 px-1.5 py-0.5 rounded">
                            Not configured
                          </span>
                        )}
                      </div>
                      <div className="text-[11px] text-muted-foreground mt-0.5">{a.desc}</div>
                    </div>
                  </div>
                </button>
              );
            })}
          </div>
          <div className="flex items-center justify-between pt-3 border-t border-border">
            <Button variant="outline" size="sm" className="gap-1" onClick={() => setStep(0)}>
              <ArrowLeft size={14} /> Back
            </Button>
            <Button size="sm" disabled={selAuth === null} onClick={() => setStep(2)} className="gap-1">
              Continue <ArrowRight size={14} />
            </Button>
          </div>
        </div>
      )}

      {step === 2 && auth && prov && (
        <div className="space-y-4">
          <div>
            <h3 className="text-sm font-semibold mb-1">Configure {prov.name}</h3>
            <p className="text-xs text-muted-foreground flex items-center gap-1.5 flex-wrap">
              <span className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md border text-[11px] font-medium" style={{ borderColor: `${prov.color}40`, color: prov.color, backgroundColor: `${prov.color}10` }}>
                {auth.usesCloudCred ? <Cloud size={11} /> : <KeyRound size={11} />}
                {auth.label}
              </span>
              {editorV2On && (
                <span
                  data-surface-chip
                  className="inline-flex items-center gap-1.5 px-2 py-0.5 rounded-md border border-border text-[11px] font-medium text-muted-foreground"
                >
                  {describeSurface(prov.id, (String(fields.apiType || '').trim() || null) as ProviderConfig['apiType']).surfaceLabel}
                </span>
              )}
            </p>
          </div>

          {auth.legacy && (
            <div data-legacy-auth-warning role="note" className="rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 text-[11px] text-muted-foreground">
              <span className="font-medium text-amber-500">Legacy authentication.</span>{' '}
              This connection uses {auth.label}, which is no longer offered for new
              connections. It keeps working as configured; to modernize it, create a
              new connection with an API key.
            </div>
          )}

          {auth.usesCloudCred && (
            <div className="rounded-lg border border-border bg-card/30 p-3 space-y-2">
              <div className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
                <Cloud size={12} /> Cloud Credentials
              </div>
              {matchingCloudCreds.length > 0 ? (
                <>
                  <div className="space-y-1.5">
                    {matchingCloudCreds.map((cc: any) => (
                      <button
                        key={cc.id}
                        className="w-full flex items-center gap-3 p-2.5 rounded-md border bg-card/50 text-left transition-all"
                        style={{
                          borderColor: selCC === cc.id ? prov.color : 'hsl(var(--border))',
                          backgroundColor: selCC === cc.id ? `${prov.color}08` : undefined,
                        }}
                        onClick={() => setSelCC(cc.id)}
                      >
                        <div className="w-4 h-4 rounded-full border-2 flex items-center justify-center shrink-0" style={{ borderColor: selCC === cc.id ? prov.color : 'hsl(var(--border))' }}>
                          {selCC === cc.id ? <div className="w-1.5 h-1.5 rounded-full" style={{ backgroundColor: prov.color }} /> : null}
                        </div>
                        <div className="flex-1 min-w-0">
                          <div className="text-xs font-medium">{cc.displayName || cc.name}</div>
                          <div className="text-[11px] text-muted-foreground font-mono">
                            {cc.accessKeyId ? redactKey(cc.accessKeyId) : cc.projectId || '—'}
                            {cc.region ? ` · ${cc.region}` : ''}
                          </div>
                        </div>
                      </button>
                    ))}
                  </div>
                  <button className="w-full flex items-center justify-center gap-1.5 py-1.5 px-3 rounded-md border border-dashed border-border text-[11px] text-muted-foreground hover:text-foreground transition-colors" onClick={() => setInlineCC(true)}>
                    <Plus size={12} /> Add new {prov.cloudCred === 'aws' ? 'AWS' : 'GCP'} credentials
                  </button>
                </>
              ) : (
                <div className="text-center py-4 rounded-lg bg-muted/30">
                  <p className="text-xs text-muted-foreground mb-2">No {prov.cloudCred === 'aws' ? 'AWS' : 'GCP'} credentials configured.</p>
                  <Button size="sm" variant="outline" className="gap-1.5" onClick={() => setInlineCC(true)}>
                    <Plus size={12} /> Add Credentials
                  </Button>
                </div>
              )}

              {inlineCC && (
                <div className="mt-2 p-3 rounded-lg border border-border bg-muted/20 space-y-3">
                  <div className="flex items-center justify-between">
                    <span className="text-[11px] font-semibold" style={{ color: prov.color }}>
                      Quick Add: {prov.cloudCred === 'aws' ? 'AWS' : 'GCP'}
                    </span>
                    <Button variant="ghost" size="icon" className="h-6 w-6" onClick={() => setInlineCC(false)}>
                      <span className="text-muted-foreground text-xs">✕</span>
                    </Button>
                  </div>
                  <div className="space-y-1.5">
                    <Label className="text-xs">Name</Label>
                    <Input
                      placeholder={prov.cloudCred === 'aws' ? 'e.g. Production AWS' : 'e.g. My GCP Project'}
                      value={ccFields.name || ''}
                      onChange={(e) => setCcFields((p: any) => ({ ...p, name: e.target.value }))}
                    />
                  </div>

                  {prov.cloudCred === 'aws' ? (
                    <>
                      <SecretField
                        label="Access Key ID"
                        placeholder="AKIA..."
                        isSet={ccSecrets.ak || false}
                        onSet={(v) => {
                          setCcFields((p: any) => ({ ...p, ak: v, akVal: v }));
                          setCcSecrets((p) => ({ ...p, ak: true }));
                        }}
                        onClear={() => setCcSecrets((p) => ({ ...p, ak: false }))}
                      />
                      <SecretField
                        label="Secret Access Key"
                        placeholder="Enter secret"
                        isSet={ccSecrets.sk || false}
                        onSet={(v) => {
                          setCcFields((p: any) => ({ ...p, skVal: v }));
                          setCcSecrets((p) => ({ ...p, sk: true }));
                        }}
                        onClear={() => setCcSecrets((p) => ({ ...p, sk: false }))}
                      />
                      <SecretField
                        label="Session Token"
                        placeholder="Optional"
                        optional
                        isSet={ccSecrets.st || false}
                        onSet={(v) => {
                          setCcFields((p: any) => ({ ...p, stVal: v }));
                          setCcSecrets((p) => ({ ...p, st: true }));
                        }}
                        onClear={() => setCcSecrets((p) => ({ ...p, st: false }))}
                      />
                      <div className="space-y-1.5">
                        <Label className="text-xs">Region</Label>
                        <Input
                          placeholder="us-east-1"
                          value={ccFields.region || ''}
                          onChange={(e) => setCcFields((p: any) => ({ ...p, region: e.target.value }))}
                          className="font-mono"
                        />
                      </div>
                    </>
                  ) : (
                    <div className="space-y-1.5">
                      <Label className="text-xs">Project ID</Label>
                      <Input
                        placeholder="my-gcp-project-id"
                        value={ccFields.projectId || ''}
                        onChange={(e) => setCcFields((p: any) => ({ ...p, projectId: e.target.value }))}
                      />
                    </div>
                  )}

                  <Button size="sm" className="w-full gap-1.5" onClick={() => void handleSaveInlineCC()}>
                    <Check size={12} /> Save Credential
                  </Button>
                </div>
              )}
            </div>
          )}

          {auth.fields.length > 0 && (
            <div className="rounded-lg border border-border bg-card/30 p-3 space-y-3">
              <div className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
                <Shield size={12} /> Authentication
              </div>
              {auth.fields.map((f) => (
                f.secret ? (
                  <SecretField
                    key={f.key}
                    label={f.label}
                    placeholder={f.placeholder}
                    isSet={isFieldSecretSet(f.key)}
                    onSet={(v) => handleSetSecret(f.key, v)}
                    onClear={() => handleClearSecret(f.key)}
                    optional={f.optional}
                  />
                ) : (
                  <div key={f.key} className="space-y-1.5">
                    <Label className="text-xs">{f.label}</Label>
                    <Input
                      placeholder={f.placeholder}
                      value={fields[f.key] || ''}
                      onChange={(e) => setFields((p) => ({ ...p, [f.key]: e.target.value }))}
                    />
                  </div>
                )
              ))}
            </div>
          )}

          {configGroups.map((g) => {
            const meta = GROUP_META[g.key];
            if (!meta) return null;
            return (
              <div key={g.key} className="rounded-lg border border-border bg-card/30 p-3 space-y-3">
                <div className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
                  {meta.icon} {meta.label}
                </div>
                {g.key === 'config' && editorV2On && (
                  <ModelDiscoveryList
                    draftConfig={draftPreviewConfig}
                    enabled={editorV2On && step === 2}
                    selectedModel={String(fields.model || '')}
                    onSelectModel={(m) => setFields((p) => ({ ...p, model: m }))}
                  />
                )}
                {g.fields.map((f) => (
                  <div key={f.key} className="space-y-1.5">
                    {renderFieldControl(f)}
                  </div>
                ))}
                {g.key === 'config' && selProv === 'compatible' && compatibleEndpointPreview && (
                  <div className="rounded-lg border border-border bg-muted/20 p-3 space-y-3">
                    <div className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
                      <Globe size={12} /> Endpoint URL
                    </div>

                    <div className="space-y-1.5">
                      <Label className="text-xs">Composed Request Path</Label>
                      <div className="overflow-hidden rounded-md border border-border bg-background">
                        <div className="flex min-h-10 flex-col sm:flex-row sm:items-stretch">
                          <div
                            className="min-w-0 border-b border-border bg-muted/40 px-3 py-2 font-mono text-[11px] text-muted-foreground sm:max-w-[55%] sm:border-b-0 sm:border-r"
                            title={compatibleEndpointPreview.baseDisplay || 'Enter a base URL above'}
                          >
                            <div className="truncate">
                              {compatibleEndpointPreview.baseDisplay || 'Enter a base URL above'}
                            </div>
                          </div>
                          <div className="border-b border-border bg-card/60 px-3 py-2 font-mono text-[11px] text-foreground sm:border-b-0 sm:border-r">
                            {compatibleEndpointPreview.endpointSegment}
                          </div>
                          <input
                            type="text"
                            value={fields.endpointSuffix ?? ''}
                            onChange={(e) => setFields((p) => ({ ...p, endpointSuffix: e.target.value }))}
                            placeholder="/beta"
                            className="min-w-0 flex-1 bg-background px-3 py-2 font-mono text-[11px] outline-none placeholder:text-muted-foreground"
                          />
                        </div>
                      </div>
                      <p className="text-[11px] text-muted-foreground">
                        Paste either the base URL or the full endpoint URL. Use the suffix box to append anything extra after the standard endpoint.
                      </p>
                    </div>

                    <div className="rounded-md border border-border bg-background/70 px-3 py-2">
                      <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Final Request URL</div>
                      <div className="mt-1 font-mono text-[11px] break-all text-foreground/90">
                        {compatibleEndpointPreview.endpointPreview || 'Enter a base URL to preview the exact request URL.'}
                      </div>
                      {compatibleEndpointPreview.hasQueryParams && (
                        <div className="mt-1 text-[10px] text-muted-foreground">
                          Includes query params from Advanced Options.
                        </div>
                      )}
                    </div>
                  </div>
                )}
              </div>
            );
          })}

          {advFields.length > 0 && (
            <div>
              <button className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors" onClick={() => setShowAdv(!showAdv)}>
                <ChevronRight size={14} className="transition-transform" style={{ transform: showAdv ? 'rotate(90deg)' : 'rotate(0deg)' }} />
                Advanced Options
              </button>
              {showAdv && (
                <div className="mt-2 rounded-lg border border-border bg-card/30 p-3 space-y-3">
                  {advFields.map((f) => (
                    <div key={f.key} className="space-y-1.5">
                      {renderFieldControl(f)}
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}

          <div className="rounded-lg border border-border bg-card/30 p-3 space-y-2">
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
                <Globe size={12} /> Extra Headers
              </div>
              <Button variant="outline" size="sm" className="h-6 px-2 text-[10px] gap-1" onClick={() => setExtraHeaders((p) => [...p, { id: crypto.randomUUID(), key: '', value: '', isSecret: false }])}>
                <Plus size={10} /> Add
              </Button>
            </div>

            {extraHeaders.length === 0 ? (
              <p className="text-xs text-muted-foreground text-center py-2">No extra headers configured.</p>
            ) : (
              extraHeaders.map((h, i) => (
                <div key={h.id} className="flex items-center gap-1.5">
                  <Input
                    placeholder="Header name"
                    value={h.key}
                    onChange={(e) => {
                      const next = [...extraHeaders];
                      next[i] = { ...next[i], key: e.target.value };
                      setExtraHeaders(next);
                    }}
                    className="flex-1 font-mono text-xs"
                  />
                  <Input
                    type={h.isSecret ? 'password' : 'text'}
                    placeholder="Value"
                    value={h.value}
                    onChange={(e) => {
                      const next = [...extraHeaders];
                      next[i] = { ...next[i], value: e.target.value };
                      setExtraHeaders(next);
                    }}
                    className="flex-1 font-mono text-xs"
                  />
                  <Button
                    variant="ghost"
                    size="icon"
                    className="h-7 w-7 shrink-0 text-muted-foreground"
                    onClick={() => {
                      const next = [...extraHeaders];
                      next[i] = { ...next[i], isSecret: !next[i].isSecret };
                      setExtraHeaders(next);
                    }}
                  >
                    {h.isSecret ? <EyeOff size={13} /> : <Eye size={13} />}
                  </Button>

                  {h.isSecret && h.key.trim() && (
                    <>
                      <Button variant="outline" size="sm" className="h-7 px-2 text-[10px]" onClick={() => { if (h.value.trim()) void handleSetSecretHeader(h.key, h.value); }}>
                        Set
                      </Button>
                      <Button variant="outline" size="sm" className="h-7 px-2 text-[10px] text-destructive" onClick={() => void handleClearSecretHeader(h.key)}>
                        Clear
                      </Button>
                    </>
                  )}

                  <Button variant="ghost" size="icon" className="h-7 w-7 shrink-0 text-destructive" onClick={() => setExtraHeaders((p) => p.filter((x) => x.id !== h.id))}>
                    <Trash2 size={13} />
                  </Button>
                </div>
              ))
            )}
          </div>

          <div className="flex items-center justify-between pt-3 border-t border-border">
            <Button variant="outline" size="sm" className="gap-1" onClick={() => setStep(multi ? 1 : 0)}>
              <ArrowLeft size={14} /> Back
            </Button>
            <Button size="sm" disabled={!canProceed()} onClick={() => { setStep(3); setTestState('idle'); setTestResult(null); setTestedInputKey(null); setPendingSaveConfirm(null); }} className="gap-1">
              Continue <ArrowRight size={14} />
            </Button>
          </div>
        </div>
      )}

      {step === 3 && prov && auth && (
        <div className="space-y-4">
          <div>
            <h3 className="text-sm font-semibold mb-1">{editorV2On ? 'Quick check' : 'Test connection'}</h3>
            <p className="text-xs text-muted-foreground">
              {editorV2On
                ? 'Runs a minimal connectivity request. It proves transport only — not feature support.'
                : 'Sends a minimal "hi" request with this configuration.'}
            </p>
          </div>

          <div className="rounded-lg border border-border bg-card/30 p-3">
            {[
              ['Provider', (
                <span className="flex items-center gap-1.5">
                  <span style={{ color: prov.color }}>{prov.icon}</span> {prov.name}
                </span>
              )],
              ['Auth', auth.label],
              ...(auth.usesCloudCred && selCC
                ? [['Cloud Cred', (matchingCloudCreds as any[]).find((c: any) => c.id === selCC)?.displayName || '—']]
                : []),
              ...(fields.displayName && fields.displayName !== prov.name
                ? [['Name', fields.displayName]]
                : []),
              ['Model', <span className="font-mono text-xs">{fields.model || '—'}</span>],
              ...(fields.apiType ? [['API Type', <span className="font-mono text-xs">{fields.apiType}</span>]] : []),
            ].map(([label, value], i, arr) => (
              <div key={String(label)}>
                <div className="flex items-center justify-between py-1.5">
                  <span className="text-xs text-muted-foreground">{label}</span>
                  <span className="text-xs font-medium">{value}</span>
                </div>
                {i < arr.length - 1 && <div className="h-px bg-border" />}
              </div>
            ))}
          </div>

          <div className="rounded-lg border border-border bg-card/30 p-3 space-y-2">
            <div className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
              <Globe size={12} /> Endpoint URL
            </div>
            <div className="text-[11px] text-muted-foreground">
              This is the request URL that will be checked during the connection test.
            </div>
            <div className="rounded-md border border-border bg-background/70 px-3 py-2 font-mono text-[11px] break-all">
              {previewEndpoint || 'Enter a base URL to preview the request URL.'}
            </div>
          </div>

          <div className="flex flex-col items-stretch gap-2 py-3 min-h-[60px]">
            {testState === 'idle' && (
              <div className="flex justify-center">
                <Button variant="outline" size="sm" className="gap-2" style={{ borderColor: prov.color, color: prov.color }} onClick={() => void handleTest()}>
                  <Zap size={14} /> {editorV2On ? 'Run quick check' : 'Test Connection'}
                </Button>
              </div>
            )}
            {testState === 'testing' && (
              <div className="flex justify-center">
                <div className="flex items-center gap-3">
                  <div className="w-4 h-4 border-2 border-muted rounded-full animate-spin" style={{ borderTopColor: prov.color }} />
                  <span className="text-xs text-muted-foreground animate-pulse">Testing connection...</span>
                </div>
              </div>
            )}
            {testState === 'success' && testResult && (
              <div className="w-full flex items-start gap-3 p-3 rounded-lg border border-emerald-400/30 bg-emerald-400/5">
                <div className="w-7 h-7 flex items-center justify-center shrink-0">
                  <Check size={20} className="text-emerald-400" />
                </div>
                <div className="min-w-0">
                  <div className="text-sm font-medium text-emerald-400 mb-0.5">{editorV2On ? 'Quick check passed' : 'Connection checks passed'}</div>
                  <div className="text-[11px] text-muted-foreground whitespace-pre-line break-words">{testResult.message}</div>
                  <div className="text-[10px] text-muted-foreground font-mono break-all">{testResult.endpoint}</div>
                  <div className="text-[10px] text-muted-foreground">{formatTimestamp(testResult.timestamp)}</div>
                </div>
              </div>
            )}
            {testState === 'failure' && testResult && (
              <div className="w-full flex items-start gap-3 p-3 rounded-lg border border-destructive/30 bg-destructive/5">
                <div className="w-7 h-7 flex items-center justify-center shrink-0 text-destructive">
                  <X size={20} />
                </div>
                <div className="min-w-0">
                  <div className="text-sm font-medium text-destructive mb-0.5">{editorV2On ? 'Quick check failed' : 'Connection failed'}</div>
                  <div className="text-[11px] text-muted-foreground whitespace-pre-line break-words">{testResult.message}</div>
                  <div className="text-[10px] text-muted-foreground font-mono break-all">{testResult.endpoint}</div>
                  <div className="text-[10px] text-muted-foreground">{formatTimestamp(testResult.timestamp)}</div>
                </div>
              </div>
            )}
            {testState !== 'testing' && testState !== 'success' && (
              <div className="w-full text-[11px] text-muted-foreground">
                You can save without a passing test, but chats may fail until the provider responds correctly.
              </div>
            )}
          </div>

          {capabilityUxOn && (
            <div className="rounded-lg border border-border bg-card/30 p-3 space-y-2">
              <div className="flex items-center gap-1.5 text-[11px] font-semibold text-muted-foreground uppercase tracking-wider">
                <Shield size={12} /> Capabilities
              </div>
              <CapabilityMatrix
                status={draftCapability.status === 'idle' ? 'loading' : draftCapability.status}
                report={draftCapability.report}
                errorMessage={draftCapability.errorMessage}
                idPrefix="wizard-draft"
              />
            </div>
          )}

          {capabilityFlags.controls && draftPreviewConfig && (
            <CapabilityCheckPanel config={draftPreviewConfig} model={draftPreviewConfig.model} />
          )}

          {templateDraft && (
            <div
              data-template-name-panel
              role="group"
              aria-label="Save as template"
              className="rounded-lg border border-border bg-card/40 p-3 space-y-2"
            >
              <Label htmlFor="template-name-input" className="text-xs">Template name</Label>
              <Input
                id="template-name-input"
                autoFocus
                value={templateDraft.name}
                onChange={(e) => setTemplateDraft((prev) => (prev ? { ...prev, name: e.target.value } : prev))}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    handleConfirmSaveTemplate();
                  } else if (e.key === 'Escape') {
                    e.preventDefault();
                    setTemplateDraft(null);
                  }
                }}
              />
              <div className="flex items-center gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  className="h-7 px-2 text-[11px] gap-1"
                  disabled={!templateDraft.name.trim()}
                  onClick={handleConfirmSaveTemplate}
                >
                  <BookmarkPlus size={11} /> Save template
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 px-2 text-[11px]"
                  onClick={() => setTemplateDraft(null)}
                >
                  Cancel
                </Button>
              </div>
            </div>
          )}

          {capabilityUxOn && pendingSaveConfirm && (
            <div role="alert" data-save-confirm className="rounded-lg border border-amber-500/40 bg-amber-500/5 p-3 space-y-2">
              <p className="text-xs text-foreground/90">{pendingSaveConfirm}</p>
              <div className="flex items-center gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  className="h-7 px-2 text-[11px]"
                  disabled={savingProvider}
                  onClick={() => void handleConfirmSaveAnyway()}
                >
                  Save anyway
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  className="h-7 px-2 text-[11px]"
                  onClick={() => setPendingSaveConfirm(null)}
                >
                  Keep editing
                </Button>
              </div>
            </div>
          )}

          <div className="flex items-center justify-between pt-3 border-t border-border">
            <Button
              variant="outline"
              size="sm"
              className="gap-1"
              onClick={() => {
                testAbortRef.current?.abort();
                testAbortRef.current = null;
                setTestState('idle');
                setTestResult(null);
                setTestedInputKey(null);
                setPendingSaveConfirm(null);
                setStep(2);
              }}
            >
              <ArrowLeft size={14} /> Back
            </Button>
            <div className="flex items-center gap-2">
              {templatesEnabled && (
                <Button variant="outline" size="sm" className="gap-1" onClick={handleSaveCurrentAsTemplate}>
                  <BookmarkPlus size={12} /> Save Template
                </Button>
              )}
              {testState !== 'testing' && (
                <Button variant="outline" size="sm" className="gap-1" onClick={() => void handleTest()}>
                  <RefreshCw size={12} /> {editorV2On ? 'Re-run quick check' : 'Re-test'}
                </Button>
              )}
              <Button
                size="sm"
                disabled={testState === 'testing' || savingProvider}
                onClick={() => void handleSave()}
                className="gap-1 bg-green-800 hover:bg-green-700 text-emerald-300"
              >
                <Check size={14} /> {savingProvider ? 'Saving...' : 'Save Provider'}
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
};

export default ProvidersTab;
