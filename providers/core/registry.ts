import type {
  CompatibleProfile,
  Provider,
  ProviderCodeExecutionConfig,
  ProviderRuntimeSettings,
  ProviderWebSearchConfig,
} from './types';
import { OpenAICompatibleHttpProvider } from '../adapters/openaiCompatibleHttp';
import { AnthropicHttpProvider } from '../adapters/anthropicHttp';
import { GeminiHttpProvider } from '../adapters/geminiHttp';

type ActiveConfigLike = {
  id: string;
  providerId: 'openai' | 'anthropic' | 'gemini' | 'compatible' | 'bedrock';
  model: string;
  apiType?: 'chat_completions' | 'responses' | 'messages';
  baseUrl?: string | null;
  authType?: 'none' | 'api_key' | 'cloud_credentials';
  authHeader?: string | null;
  authScheme?: string | null;
  compactionEnabled?: boolean;
  compactionInstruction?: string | null;
  compactionTriggerTokens?: number | null;
  endpointSuffix?: string | null;
  fileInputMode?: CompatibleProfile['fileInputMode'];
  timeoutMs?: number | null;
  reasoningEffort?: string | null;
  modelInPath?: boolean;
  modelInBody?: boolean;
  extraHeaders?: Array<{ id: string; key: string; value: string; isSecret?: boolean }>;
  queryParams?: Record<string, string> | null;
  payloadOverrides?: Record<string, unknown> | null;
  webSearch?: ProviderWebSearchConfig | null;
  codeExecution?: ProviderCodeExecutionConfig | null;
};

function getActiveConfig(settings: ProviderRuntimeSettings): ActiveConfigLike | null {
  const anySettings = settings as any;
  const configs = Array.isArray(anySettings.providerConfigs)
    ? (anySettings.providerConfigs as ActiveConfigLike[])
    : [];
  if (configs.length === 0) return null;
  const desired = typeof anySettings.activeProviderConfigId === 'string'
    ? anySettings.activeProviderConfigId
    : '';
  if (desired) {
    const found = configs.find((cfg) => cfg.id === desired);
    if (found) return found;
  }
  return configs[0] || null;
}

function configToCompatibleProfile(config: ActiveConfigLike): CompatibleProfile {
  const apiType = config.apiType === 'responses' ? 'responses' : 'chat_completions';
  return {
    id: config.id,
    displayName: config.id,
    apiType,
    baseUrl: (config.baseUrl || 'https://api.openai.com/v1').trim() || 'https://api.openai.com/v1',
    auth: {
      type: config.authType === 'none' ? 'none' : 'api_key',
      header: (config.authHeader || 'Authorization').trim() || 'Authorization',
      scheme: (config.authScheme || 'Bearer').trim() || 'Bearer',
    },
    apiKey: null,
    extraHeaders: (config.extraHeaders || []).map((h) => ({
      id: h.id,
      key: h.key,
      value: h.isSecret ? '' : (h.value || ''),
      isSecret: Boolean(h.isSecret),
    })),
    queryParams: config.queryParams || null,
    payloadOverrides: config.payloadOverrides || null,
    timeoutMs: typeof config.timeoutMs === 'number' ? config.timeoutMs : null,
    reasoningEffort: config.reasoningEffort || null,
    modelInPath: Boolean(config.modelInPath),
    modelInBody: config.modelInBody !== false,
    endpointSuffix: config.endpointSuffix || null,
    fileInputMode: normalizeCompatibleFileInputMode(config.fileInputMode, apiType),
    webSearch: config.webSearch || null,
    codeExecution: config.codeExecution || null,
  };
}

function normalizeCompatibleFileInputMode(
  raw: CompatibleProfile['fileInputMode'] | undefined,
  _apiType: CompatibleProfile['apiType'],
): NonNullable<CompatibleProfile['fileInputMode']> {
  if (raw === 'sidecar_only' || raw === 'native_pdf_only' || raw === 'native_supported') return raw;
  return 'native_supported';
}

function normalizeCompatibleProfile(profile: CompatibleProfile): CompatibleProfile {
  return {
    ...profile,
    fileInputMode: normalizeCompatibleFileInputMode(profile.fileInputMode, profile.apiType),
  };
}

function getCompatibleProfile(settings: ProviderRuntimeSettings): CompatibleProfile | null {
  const active = getActiveConfig(settings);
  if (active && active.providerId === 'compatible') {
    return configToCompatibleProfile(active);
  }
  const profiles = settings.compatibleProfiles || [];
  if (profiles.length === 0) return null;
  const desired = settings.provider.profileId;
  if (desired) {
    const found = profiles.find((p) => p.id === desired);
    if (found) return normalizeCompatibleProfile(found);
  }
  return profiles[0] ? normalizeCompatibleProfile(profiles[0]) : null;
}

export function createProvider(settings: ProviderRuntimeSettings): Provider {
  const active = getActiveConfig(settings);
  const selection = active
    ? {
      providerId: active.providerId,
      model: active.model,
      profileId: active.providerId === 'compatible' ? active.id : null,
    }
    : settings.provider;
  const providerId = selection.providerId;
  const model = selection.model;

  if (!model || !model.trim()) {
    throw new Error('Model is required.');
  }

  if (providerId === 'compatible') {
    const profile = getCompatibleProfile(settings);
    if (!profile) {
      throw new Error('No compatible provider profiles are configured.');
    }
    return new OpenAICompatibleHttpProvider({
      providerId: 'compatible',
      model,
      profile,
    });
  }

  if (providerId === 'openai') {
    const cfg = settings.openai;
    const baseUrl = (active?.baseUrl || cfg?.baseUrl || 'https://api.openai.com/v1').trim() || 'https://api.openai.com/v1';

    const profile: CompatibleProfile = {
      id: 'openai',
      displayName: 'OpenAI',
      apiType: 'responses',
      baseUrl,
      auth: { type: 'none', header: 'Authorization', scheme: 'Bearer' },
      apiKey: null,
      extraHeaders: [],
      queryParams: null,
      payloadOverrides: null,
      timeoutMs: null,
      reasoningEffort: null,
      modelInPath: false,
      modelInBody: true,
      endpointSuffix: null,
      fileInputMode: 'native_supported',
      webSearch: active?.webSearch ?? cfg?.webSearch ?? null,
      codeExecution: active?.codeExecution ?? cfg?.codeExecution ?? null,
    };

    return new OpenAICompatibleHttpProvider({
      providerId: 'openai',
      model,
      profile,
    });
  }

  if (providerId === 'anthropic') {
    const cfg = settings.anthropic;
    const baseUrl = (active?.baseUrl ?? cfg?.baseUrl ?? null);
    return new AnthropicHttpProvider({
      model,
      baseUrl,
      anthropicVersion: (active as any)?.anthropicVersion ?? cfg?.anthropicVersion ?? null,
      compactionEnabled: (active as any)?.compactionEnabled ?? cfg?.compactionEnabled ?? true,
      compactionInstruction: (active as any)?.compactionInstruction ?? cfg?.compactionInstruction ?? null,
      compactionTriggerTokens: (active as any)?.compactionTriggerTokens ?? cfg?.compactionTriggerTokens ?? null,
      webSearch: active?.webSearch ?? cfg?.webSearch ?? null,
      codeExecution: active?.codeExecution ?? cfg?.codeExecution ?? null,
    });
  }

  if (providerId === 'gemini') {
    const cfg = settings.gemini;
    const baseUrl = (active?.baseUrl ?? cfg?.baseUrl ?? null);
    return new GeminiHttpProvider({
      model,
      baseUrl,
    });
  }

  if (providerId === 'bedrock') {
    throw new Error('Bedrock provider is available through the local backend run API in this build.');
  }

  // Exhaustiveness guard
  throw new Error(`Unknown provider: ${providerId}`);
}
