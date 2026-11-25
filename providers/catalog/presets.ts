import type { CompatibleApiType, CompatibleProfile, ProviderAuth } from '../core/types';

export interface CompatiblePreset {
  id: string;
  label: string;
  description: string;
  baseUrl: string;
  apiType: CompatibleApiType;
  auth: ProviderAuth;
}

export const COMPATIBLE_PRESETS: CompatiblePreset[] = [
  {
    id: 'groq',
    label: 'Groq',
    description: 'Ultra-fast OpenAI-compatible API.',
    baseUrl: 'https://api.groq.com/openai/v1',
    apiType: 'chat_completions',
    auth: { type: 'api_key', header: 'Authorization', scheme: 'Bearer' },
  },
  {
    id: 'together',
    label: 'Together AI',
    description: 'Open models via an OpenAI-compatible endpoint.',
    baseUrl: 'https://api.together.xyz/v1',
    apiType: 'chat_completions',
    auth: { type: 'api_key', header: 'Authorization', scheme: 'Bearer' },
  },
  {
    id: 'fireworks',
    label: 'Fireworks AI',
    description: 'OpenAI-compatible inference for open models.',
    baseUrl: 'https://api.fireworks.ai/inference/v1',
    apiType: 'chat_completions',
    auth: { type: 'api_key', header: 'Authorization', scheme: 'Bearer' },
  },
  {
    id: 'openrouter',
    label: 'OpenRouter',
    description: 'Unified OpenAI-compatible access to many vendors/models.',
    baseUrl: 'https://openrouter.ai/api/v1',
    apiType: 'chat_completions',
    auth: { type: 'api_key', header: 'Authorization', scheme: 'Bearer' },
  },
];

export function presetToProfile(preset: CompatiblePreset, opts?: { id?: string; displayName?: string }): CompatibleProfile {
  const id = (opts?.id || preset.id).trim();
  return {
    id,
    displayName: opts?.displayName || preset.label,
    apiType: preset.apiType,
    baseUrl: preset.baseUrl,
    auth: preset.auth,
    apiKey: '',
    extraHeaders: [],
    queryParams: null,
    payloadOverrides: null,
    timeoutMs: null,
    reasoningEffort: null,
    modelInPath: false,
    modelInBody: true,
    endpointSuffix: null,
  };
}
