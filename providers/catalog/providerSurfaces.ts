import type { ProviderCardType, ProviderConfig } from '@/types';

/**
 * A provider family is branding. A surface is the concrete wire protocol the
 * application will dispatch. Capability resolution is always keyed by the
 * latter; UI code must not infer support from the family alone.
 */
export type ProviderSurfaceId =
  | 'anthropic_messages'
  | 'openai_responses'
  | 'gemini_developer'
  | 'vertex_generate_content'
  | 'bedrock_claude_messages'
  | 'bedrock_chat_completions'
  | 'bedrock_responses'
  | 'compatible_chat_completions'
  | 'compatible_responses';

export type ProviderApiSurface =
  | 'responses'
  | 'messages'
  | 'generate_content'
  | 'invoke_stream'
  | 'converse_stream'
  | 'chat_completions';

export interface ProviderSurfaceDefinition {
  id: ProviderSurfaceId;
  providerId: ProviderCardType;
  familyLabel: string;
  label: string;
  description: string;
  apiSurface: ProviderApiSurface;
  /** Existing persisted discriminator retained during the additive migration. */
  legacyApiType: NonNullable<ProviderConfig['apiType']>;
  endpointFamily: string;
  available: boolean;
  unavailableReason?: string;
}

export const PROVIDER_SURFACES: readonly ProviderSurfaceDefinition[] = [
  {
    id: 'anthropic_messages',
    providerId: 'anthropic',
    familyLabel: 'Anthropic',
    label: 'Claude API · Messages',
    description: 'Direct Claude API with native Messages features.',
    apiSurface: 'messages',
    legacyApiType: 'messages',
    endpointFamily: 'anthropic-api',
    available: true,
  },
  {
    id: 'openai_responses',
    providerId: 'openai',
    familyLabel: 'OpenAI',
    label: 'Responses API',
    description: 'OpenAI Responses with native state and hosted tools.',
    apiSurface: 'responses',
    legacyApiType: 'responses',
    endpointFamily: 'openai-api',
    available: true,
  },
  {
    id: 'bedrock_claude_messages',
    providerId: 'bedrock',
    familyLabel: 'Amazon Bedrock',
    label: 'Claude · Messages',
    description: 'Bedrock Runtime Invoke; documents require an explicit Converse plan.',
    apiSurface: 'invoke_stream',
    legacyApiType: 'messages',
    endpointFamily: 'bedrock-runtime',
    available: true,
  },
  {
    id: 'bedrock_chat_completions',
    providerId: 'bedrock',
    familyLabel: 'Amazon Bedrock',
    label: 'Chat Completions',
    description: 'OpenAI-compatible Bedrock Runtime surface for compatible models.',
    apiSurface: 'chat_completions',
    legacyApiType: 'chat_completions',
    endpointFamily: 'bedrock-runtime',
    available: true,
  },
  {
    id: 'bedrock_responses',
    providerId: 'bedrock',
    familyLabel: 'Amazon Bedrock',
    label: 'Responses (Mantle)',
    description: 'OpenAI-compatible Responses on the Bedrock Mantle endpoint.',
    apiSurface: 'responses',
    legacyApiType: 'responses',
    endpointFamily: 'bedrock-mantle',
    available: true,
  },
  {
    id: 'gemini_developer',
    providerId: 'gemini',
    familyLabel: 'Google',
    label: 'Gemini Developer API',
    description: 'Google AI Gemini generateContent API with API-key authentication.',
    apiSurface: 'generate_content',
    legacyApiType: 'chat_completions',
    endpointFamily: 'gemini-developer',
    available: true,
  },
  {
    id: 'vertex_generate_content',
    providerId: 'gemini',
    familyLabel: 'Google',
    label: 'Vertex AI',
    description: 'Google Cloud Vertex generateContent surface.',
    apiSurface: 'generate_content',
    legacyApiType: 'chat_completions',
    endpointFamily: 'vertex-ai',
    available: false,
    unavailableReason: 'Not yet supported',
  },
  {
    id: 'compatible_chat_completions',
    providerId: 'compatible',
    familyLabel: 'OpenAI-compatible',
    label: 'Chat Completions',
    description: 'Custom endpoint implementing the OpenAI Chat Completions protocol.',
    apiSurface: 'chat_completions',
    legacyApiType: 'chat_completions',
    endpointFamily: 'compatible-custom',
    available: true,
  },
  {
    id: 'compatible_responses',
    providerId: 'compatible',
    familyLabel: 'OpenAI-compatible',
    label: 'Responses',
    description: 'Custom endpoint implementing the OpenAI Responses protocol.',
    apiSurface: 'responses',
    legacyApiType: 'responses',
    endpointFamily: 'compatible-custom',
    available: true,
  },
] as const;

export function getProviderSurface(id: ProviderSurfaceId): ProviderSurfaceDefinition {
  const surface = PROVIDER_SURFACES.find((candidate) => candidate.id === id);
  if (!surface) throw new Error(`Unknown provider surface: ${id}`);
  return surface;
}

export function deriveProviderSurface(config: Pick<ProviderConfig, 'providerId' | 'apiType'>): ProviderSurfaceDefinition {
  const providerId = config.providerId;
  const apiType = config.apiType;
  const match = PROVIDER_SURFACES.find((surface) => {
    if (!surface.available || surface.providerId !== providerId) return false;
    if (providerId === 'bedrock' || providerId === 'compatible') {
      return surface.legacyApiType === (apiType || (providerId === 'bedrock' ? 'messages' : 'chat_completions'));
    }
    return true;
  });
  if (!match) {
    return getProviderSurface('compatible_chat_completions');
  }
  return match;
}
