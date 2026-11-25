import { DEFAULT_SETTINGS } from '@/constants';
import {
  BEDROCK_COMPACTION_DEFAULT_INSTRUCTION,
  BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS,
  OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS,
} from '@/lib/bedrockCompaction';
import { buildCompatibleEndpointUrl } from '@/lib/compatibleEndpointUrl';
import type {
  AppSettings,
  CompatibleProfile,
  ProviderCardType,
  ProviderConfig,
  ProviderCodeExecutionConfig,
  ProviderWebSearchConfig,
} from '@/types';

function cloneDefaultCompatibleProfiles(): CompatibleProfile[] {
  return (DEFAULT_SETTINGS.compatibleProfiles || []).map((p) => ({
    ...p,
    extraHeaders: (p.extraHeaders || []).map((h) => ({ ...h })),
    webSearch: cloneWebSearchConfig(p.webSearch),
    codeExecution: cloneCodeExecutionConfig(p.codeExecution),
    models: p.models ? p.models.map((m) => ({ ...m })) : undefined,
  }));
}

function nonEmpty(value: unknown): string | null {
  if (typeof value !== 'string') return null;
  const trimmed = value.trim();
  return trimmed || null;
}

function cloneWebSearchConfig(value: ProviderWebSearchConfig | null | undefined): ProviderWebSearchConfig | null {
  if (!value) return null;
  return {
    enabled: value.enabled ?? null,
    toolType: value.toolType ?? null,
    maxUses: value.maxUses ?? null,
    searchContextSize: value.searchContextSize ?? null,
    allowedDomains: value.allowedDomains ? [...value.allowedDomains] : null,
    blockedDomains: value.blockedDomains ? [...value.blockedDomains] : null,
    allowedCallers: value.allowedCallers ? [...value.allowedCallers] : null,
  };
}

function cloneCodeExecutionConfig(value: ProviderCodeExecutionConfig | null | undefined): ProviderCodeExecutionConfig | null {
  if (!value) return null;
  return {
    enabled: value.enabled ?? null,
    toolType: value.toolType ?? null,
    containerMode: value.containerMode ?? null,
    containerId: value.containerId ?? null,
    memoryLimit: value.memoryLimit ?? null,
    skills: value.skills
      ? value.skills.map((skill) => ({
        type: skill.type,
        skillId: skill.skillId,
        version: skill.version ?? null,
      }))
      : null,
  };
}

function defaultBaseUrl(providerId: ProviderCardType): string {
  if (providerId === 'openai') return DEFAULT_SETTINGS.openai?.baseUrl || 'https://api.openai.com/v1';
  if (providerId === 'anthropic') return DEFAULT_SETTINGS.anthropic?.baseUrl || 'https://api.anthropic.com';
  if (providerId === 'gemini') return DEFAULT_SETTINGS.gemini?.baseUrl || 'https://generativelanguage.googleapis.com';
  if (providerId === 'bedrock') return '';
  return 'https://api.openai.com/v1';
}

function isProviderEnabled(settings: AppSettings, providerId: ProviderCardType): boolean {
  const flags = settings.featureFlags?.providers;
  if (!flags) return true;
  return (flags as Record<string, boolean>)[providerId] !== false;
}

export function getProviderConfigs(settings: AppSettings): ProviderConfig[] {
  if (!Array.isArray(settings.providerConfigs)) return [];
  return settings.providerConfigs.filter((cfg): cfg is ProviderConfig => {
    return Boolean(cfg && typeof cfg.id === 'string' && typeof cfg.providerId === 'string');
  });
}

export function getActiveProviderConfig(settings: AppSettings): ProviderConfig | null {
  const configs = getProviderConfigs(settings);
  if (configs.length === 0) return null;
  const enabled = configs.filter((cfg) => isProviderEnabled(settings, cfg.providerId));
  const desired = nonEmpty(settings.activeProviderConfigId);
  if (desired) {
    const found = enabled.find((cfg) => cfg.id === desired) || configs.find((cfg) => cfg.id === desired);
    if (found) return found;
  }
  return enabled[0] || configs[0] || null;
}

function configToCompatibleProfile(config: ProviderConfig): CompatibleProfile {
  const authType = config.authType === 'none' ? 'none' : 'api_key';
  const authHeader = nonEmpty(config.authHeader) || 'Authorization';
  const authScheme = nonEmpty(config.authScheme) || 'Bearer';
  const baseUrl = nonEmpty(config.baseUrl) || defaultBaseUrl('compatible');

  return {
    id: config.id,
    displayName: nonEmpty(config.displayName) || 'Compatible Provider',
    apiType: config.apiType === 'responses' ? 'responses' : 'chat_completions',
    baseUrl,
    auth: {
      type: authType,
      header: authHeader,
      scheme: authScheme,
    },
    apiKey: null,
    extraHeaders: (config.extraHeaders || []).map((h) => {
      const isSecret = Boolean(h.isSecret);
      return {
        id: h.id,
        key: h.key,
        value: isSecret ? '' : (h.value || ''),
        isSecret,
      };
    }),
    queryParams: config.queryParams || null,
    payloadOverrides: config.payloadOverrides || null,
    timeoutMs: typeof config.timeoutMs === 'number' ? config.timeoutMs : null,
    reasoningEffort: nonEmpty(config.reasoningEffort),
    modelInPath: Boolean(config.modelInPath),
    modelInBody: config.modelInBody !== false,
    endpointSuffix: nonEmpty(config.endpointSuffix),
    webSearch: cloneWebSearchConfig(config.webSearch),
    codeExecution: cloneCodeExecutionConfig(config.codeExecution),
  };
}

function resolveCompatibleProfiles(settings: AppSettings, configs: ProviderConfig[]): CompatibleProfile[] {
  const fromConfigs = configs
    .filter((cfg) => cfg.providerId === 'compatible')
    .map(configToCompatibleProfile);

  if (fromConfigs.length > 0) return fromConfigs;
  if (Array.isArray(settings.compatibleProfiles) && settings.compatibleProfiles.length > 0) {
    return settings.compatibleProfiles;
  }
  return cloneDefaultCompatibleProfiles();
}

export function buildProviderEndpoint(providerId: ProviderCardType, config?: ProviderConfig | null): string {
  const base = nonEmpty(config?.baseUrl) || defaultBaseUrl(providerId);
  if (providerId === 'anthropic') return `${base.replace(/\/+$/, '')}/v1/messages`;
  if (providerId === 'openai') return `${base.replace(/\/+$/, '')}/responses`;
  if (providerId === 'gemini') return `${base.replace(/\/+$/, '')}/v1beta/models`;
  if (providerId === 'compatible') {
    return buildCompatibleEndpointUrl({
      baseUrl: base,
      apiType: config?.apiType === 'responses' ? 'responses' : 'chat_completions',
      model: nonEmpty(config?.model) || '',
      modelInPath: Boolean(config?.modelInPath),
      endpointSuffix: nonEmpty(config?.endpointSuffix),
      queryParams: config?.queryParams || null,
    });
  }
  if (providerId === 'bedrock') {
    const region = nonEmpty(config?.region) || DEFAULT_SETTINGS.bedrock?.region || 'us-east-1';
    const apiType = nonEmpty(config?.apiType) || 'messages';
    if (apiType === 'messages') {
      const runtimeBase = (base || `https://bedrock-runtime.${region}.amazonaws.com`).replace(/\/+$/, '');
      const model = nonEmpty(config?.model);
      return model
        ? `${runtimeBase}/model/${encodeURIComponent(model)}/invoke-with-response-stream`
        : runtimeBase;
    }
    const compatibleBase = base || (
      apiType === 'responses'
        ? `https://bedrock-mantle.${region}.api.aws/v1`
        : `https://bedrock-runtime.${region}.amazonaws.com/v1`
    );
    return buildCompatibleEndpointUrl({
      baseUrl: compatibleBase,
      apiType: apiType === 'responses' ? 'responses' : 'chat_completions',
      model: nonEmpty(config?.model) || '',
      modelInPath: Boolean(config?.modelInPath),
      endpointSuffix: nonEmpty(config?.endpointSuffix),
      queryParams: config?.queryParams || null,
    });
  }
  return base;
}

export function getProviderBadge(settings: AppSettings): { providerId: ProviderCardType; model: string; displayName: string } {
  const active = getActiveProviderConfig(settings);
  if (active) {
    const model = nonEmpty(active.model) || nonEmpty(settings.provider.model);
    return {
      providerId: active.providerId,
      model: model || 'not configured',
      displayName: nonEmpty(active.displayName) || active.providerId,
    };
  }
  return {
    providerId: settings.provider.providerId as ProviderCardType,
    model: nonEmpty(settings.provider.model) || 'not configured',
    displayName: settings.provider.providerId,
  };
}

export function applyProviderConfigToLegacySettings(settings: AppSettings): AppSettings {
  const configs = getProviderConfigs(settings);
  const active = getActiveProviderConfig(settings);
  const compatibleProfiles = resolveCompatibleProfiles(settings, configs);

  const next: AppSettings = {
    ...settings,
    providerConfigs: configs.map((cfg) => ({
      ...cfg,
      isDefault: active ? cfg.id === active.id : Boolean(cfg.isDefault),
    })),
    compatibleProfiles,
  };

  if (!active) return next;

  const model = nonEmpty(active.model) || nonEmpty(settings.provider.model) || '';
  next.provider = {
    providerId: active.providerId,
    model,
    profileId: active.providerId === 'compatible' ? active.id : null,
  };
  next.activeProviderConfigId = active.id;

  if (active.providerId === 'openai') {
    const baseUrl = nonEmpty(active.baseUrl) || defaultBaseUrl('openai');
    const compactionEnabled = typeof active.compactionEnabled === 'boolean'
      ? active.compactionEnabled
      : (typeof next.openai?.compactionEnabled === 'boolean' ? next.openai.compactionEnabled : true);
    const compactionTriggerTokens = Number(active.compactionTriggerTokens)
      || Number(next.openai?.compactionTriggerTokens)
      || OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS;
    next.openai = {
      ...(next.openai || { apiKey: '', baseUrl }),
      apiKey: '',
      baseUrl,
      compactionEnabled,
      compactionTriggerTokens: compactionTriggerTokens >= 50000
        ? Math.round(compactionTriggerTokens)
        : OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS,
      webSearch: cloneWebSearchConfig(active.webSearch),
      codeExecution: cloneCodeExecutionConfig(active.codeExecution),
    };
  } else if (active.providerId === 'anthropic') {
    const baseUrl = nonEmpty(active.baseUrl) || defaultBaseUrl('anthropic');
    const anthropicVersion =
      nonEmpty(active.anthropicVersion)
      || nonEmpty(next.anthropic?.anthropicVersion)
      || DEFAULT_SETTINGS.anthropic?.anthropicVersion
      || '2023-06-01';
    const compactionEnabled = typeof active.compactionEnabled === 'boolean'
      ? active.compactionEnabled
      : (typeof next.anthropic?.compactionEnabled === 'boolean' ? next.anthropic.compactionEnabled : true);
    const compactionInstruction =
      nonEmpty(active.compactionInstruction)
      || nonEmpty(next.anthropic?.compactionInstruction)
      || BEDROCK_COMPACTION_DEFAULT_INSTRUCTION;
    const compactionTriggerTokens = Number(active.compactionTriggerTokens)
      || Number(next.anthropic?.compactionTriggerTokens)
      || BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS;
    next.anthropic = {
      ...(next.anthropic || { apiKey: '', baseUrl }),
      apiKey: '',
      baseUrl,
      anthropicVersion,
      compactionEnabled,
      compactionInstruction,
      compactionTriggerTokens: compactionTriggerTokens >= 50000
        ? Math.round(compactionTriggerTokens)
        : BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS,
      webSearch: cloneWebSearchConfig(active.webSearch),
      codeExecution: cloneCodeExecutionConfig(active.codeExecution),
    };
  } else if (active.providerId === 'gemini') {
    const baseUrl = nonEmpty(active.baseUrl) || defaultBaseUrl('gemini');
    next.gemini = {
      ...(next.gemini || { apiKey: '', baseUrl }),
      apiKey: '',
      baseUrl,
    };
  } else if (active.providerId === 'bedrock') {
    const region = nonEmpty(active.region) || nonEmpty(next.bedrock?.region) || DEFAULT_SETTINGS.bedrock?.region || 'us-east-1';
    const anthropicVersion =
      nonEmpty(active.anthropicVersion)
      || nonEmpty(next.bedrock?.anthropicVersion)
      || DEFAULT_SETTINGS.bedrock?.anthropicVersion
      || 'bedrock-2023-05-31';
    const apiType = nonEmpty(active.apiType) || 'messages';
    const compactionEnabled = apiType === 'messages'
      ? (typeof active.compactionEnabled === 'boolean'
        ? active.compactionEnabled
        : (typeof next.bedrock?.compactionEnabled === 'boolean' ? next.bedrock.compactionEnabled : true))
      : false;
    const compactionInstruction = apiType === 'messages'
      ? (
        nonEmpty(active.compactionInstruction)
        || nonEmpty(next.bedrock?.compactionInstruction)
        || BEDROCK_COMPACTION_DEFAULT_INSTRUCTION
      )
      : null;
    const compactionTriggerTokens = apiType === 'messages'
      ? (
        Number(active.compactionTriggerTokens)
        || Number(next.bedrock?.compactionTriggerTokens)
        || BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS
      )
      : null;
    const baseUrl = nonEmpty(active.baseUrl);
    next.bedrock = {
      ...(next.bedrock || { region, baseUrl, anthropicVersion, extraHeaders: [] }),
      region,
      baseUrl,
      anthropicVersion,
      compactionEnabled,
      compactionInstruction,
      compactionTriggerTokens: compactionTriggerTokens == null
        ? null
        : (compactionTriggerTokens >= 50000 ? Math.round(compactionTriggerTokens) : BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS),
      extraHeaders: (active.extraHeaders || []).map((h) => ({
        id: h.id,
        key: h.key,
        value: h.isSecret ? '' : (h.value || ''),
        isSecret: Boolean(h.isSecret),
      })),
      apiKey: '',
      accessKeyId: '',
      secretAccessKey: '',
      sessionToken: null,
      webSearch: cloneWebSearchConfig(active.webSearch),
      codeExecution: cloneCodeExecutionConfig(active.codeExecution),
    };
  }

  return next;
}
