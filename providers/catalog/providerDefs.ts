import type { ProviderCardType } from '@/types';
import {
  BEDROCK_COMPACTION_DEFAULT_INSTRUCTION,
  BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS,
  OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS,
} from '@/lib/bedrockCompaction';

export interface AuthField {
  key: string;
  label: string;
  placeholder?: string;
  secret?: boolean;
  optional?: boolean;
}

export interface AuthMethod {
  id: string;
  label: string;
  desc: string;
  fields: AuthField[];
  usesCloudCred?: boolean;
  /**
   * Not offered for new configurations. Existing configs that already use the
   * method stay editable and get a legacy warning instead of a silent reset.
   */
  legacy?: boolean;
}

export interface ConfigField {
  key: string;
  label: string;
  placeholder?: string;
  def?: string | number | boolean;
  type: 'text' | 'number' | 'select' | 'toggle' | 'json' | 'textarea';
  options?: string[];
  group: 'identity' | 'config' | 'auth_detail' | 'placement' | 'advanced';
}

export interface ProviderDef {
  id: ProviderCardType;
  name: string;
  desc: string;
  icon: string;
  color: string;
  cloudCred: 'aws' | 'gcp' | null;
  authMethods: AuthMethod[];
  config: ConfigField[];
}

export const PROVIDER_DEFS: Record<ProviderCardType, ProviderDef> = {
  anthropic: {
    id: 'anthropic',
    name: 'Anthropic',
    desc: 'Direct API access',
    icon: 'A',
    color: '#D97757',
    cloudCred: null,
    authMethods: [
      {
        id: 'api_key',
        label: 'API Key',
        desc: 'Anthropic API key',
        fields: [{ key: 'apiKey', label: 'API Key', placeholder: 'sk-ant-...', secret: true }],
      },
    ],
    config: [
      { key: 'model', label: 'Model', placeholder: 'claude-opus-4-6', def: 'claude-opus-4-6', type: 'text', group: 'config' },
      { key: 'baseUrl', label: 'Base URL (optional)', placeholder: 'https://api.anthropic.com/', def: 'https://api.anthropic.com/', type: 'text', group: 'config' },
      { key: 'anthropicVersion', label: 'Anthropic Version', placeholder: '2023-06-01', def: '2023-06-01', type: 'text', group: 'advanced' },
      { key: 'compactionEnabled', label: 'Compaction', def: true, type: 'toggle', group: 'config' },
      { key: 'compactionInstruction', label: 'Compaction Instruction', def: BEDROCK_COMPACTION_DEFAULT_INSTRUCTION, type: 'textarea', group: 'config' },
      { key: 'compactionTriggerTokens', label: 'Compaction Token Limit', def: BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS, type: 'number', group: 'config' },
      { key: 'codeExecutionEnabled', label: 'Code Sandbox', def: false, type: 'toggle', group: 'config' },
      { key: 'codeExecutionSkills', label: 'Code Sandbox Skills (JSON)', placeholder: '[{\"type\":\"anthropic\",\"skillId\":\"pdf\",\"version\":\"latest\"}]', def: '', type: 'json', group: 'config' },
      { key: 'codeExecutionToolType', label: 'Code Sandbox Tool Type', placeholder: 'code_execution_20260120', def: 'code_execution_20260120', type: 'text', group: 'advanced' },
    ],
  },
  bedrock: {
    id: 'bedrock',
    name: 'AWS Bedrock',
    desc: 'Anthropic via AWS',
    icon: '▲',
    color: '#FF9900',
    cloudCred: 'aws',
    authMethods: [
      {
        id: 'aws_credentials',
        label: 'AWS Cloud Credentials',
        desc: 'Use saved AWS credentials from Cloud Credentials tab',
        fields: [],
        usesCloudCred: true,
      },
      {
        id: 'bedrock_api_key',
        label: 'Bedrock API Key',
        desc: 'Single API key for Bedrock access',
        fields: [{ key: 'apiKey', label: 'Bedrock API Key', placeholder: 'Enter Bedrock API key', secret: true }],
      },
    ],
    config: [
      { key: 'model', label: 'Model', placeholder: 'anthropic.claude-opus-4-6-v1', def: 'anthropic.claude-opus-4-6-v1', type: 'text', group: 'config' },
      { key: 'region', label: 'Region', placeholder: 'us-east-1', def: 'us-east-1', type: 'text', group: 'config' },
      { key: 'baseUrl', label: 'Base URL (optional)', placeholder: 'https://bedrock-runtime.us-east-1.amazonaws.com', def: '', type: 'text', group: 'config' },
      { key: 'anthropicVersion', label: 'Anthropic Version', placeholder: 'bedrock-2023-05-31', def: 'bedrock-2023-05-31', type: 'text', group: 'config' },
      { key: 'apiType', label: 'API Type', def: 'messages', type: 'select', options: ['messages', 'responses', 'chat_completions'], group: 'config' },
      { key: 'compactionEnabled', label: 'Compaction', def: true, type: 'toggle', group: 'config' },
      { key: 'compactionInstruction', label: 'Compaction Instruction', def: BEDROCK_COMPACTION_DEFAULT_INSTRUCTION, type: 'textarea', group: 'config' },
      { key: 'compactionTriggerTokens', label: 'Compaction Token Limit', def: BEDROCK_COMPACTION_DEFAULT_TRIGGER_TOKENS, type: 'number', group: 'config' },
      { key: 'authHeader', label: 'Auth Header', placeholder: 'Authorization', def: 'Authorization', type: 'text', group: 'auth_detail' },
      { key: 'authScheme', label: 'Auth Scheme', placeholder: 'Bearer', def: 'Bearer', type: 'text', group: 'auth_detail' },
      { key: 'modelInBody', label: 'Include model in request body', def: true, type: 'toggle', group: 'placement' },
      { key: 'modelInPath', label: 'Include model in URL path', def: false, type: 'toggle', group: 'placement' },
      { key: 'endpointSuffix', label: 'Endpoint Suffix', placeholder: '/model', def: '', type: 'text', group: 'advanced' },
      { key: 'reasoningEffort', label: 'Reasoning Effort', def: 'Default', type: 'select', options: ['Default', 'low', 'medium', 'high'], group: 'advanced' },
      { key: 'timeout', label: 'Timeout (ms)', placeholder: 'No timeout', def: '', type: 'number', group: 'advanced' },
      { key: 'queryParams', label: 'Query Params (JSON)', placeholder: '{"key":"value"}', def: '', type: 'json', group: 'advanced' },
      { key: 'payloadOverrides', label: 'Payload Overrides (JSON)', placeholder: '{"temperature":0.2}', def: '', type: 'json', group: 'advanced' },
    ],
  },
  openai: {
    id: 'openai',
    name: 'OpenAI',
    desc: 'OpenAI Responses API',
    icon: '◎',
    color: '#10A37F',
    cloudCred: null,
    authMethods: [
      {
        id: 'api_key',
        label: 'API Key',
        desc: 'Your OpenAI API key',
        fields: [{ key: 'apiKey', label: 'API Key', placeholder: 'sk-...', secret: true }],
      },
    ],
    config: [
      { key: 'model', label: 'Model', placeholder: 'gpt-4o', def: 'gpt-4o', type: 'text', group: 'config' },
      { key: 'baseUrl', label: 'Base URL (optional)', placeholder: 'https://api.openai.com/v1', def: '', type: 'text', group: 'config' },
      { key: 'compactionEnabled', label: 'Compaction', def: true, type: 'toggle', group: 'config' },
      { key: 'compactionTriggerTokens', label: 'Compaction Token Limit', def: OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS, type: 'number', group: 'config' },
      { key: 'codeExecutionEnabled', label: 'Code Sandbox', def: false, type: 'toggle', group: 'config' },
      { key: 'codeExecutionMemoryLimit', label: 'Code Sandbox Memory Limit', placeholder: '4g', def: '', type: 'text', group: 'advanced' },
      { key: 'codeExecutionContainerMode', label: 'Code Sandbox Container Mode', def: 'auto', type: 'select', options: ['auto', 'existing'], group: 'advanced' },
      { key: 'codeExecutionContainerId', label: 'Code Sandbox Container ID', placeholder: 'cntr_abc123', def: '', type: 'text', group: 'advanced' },
    ],
  },
  gemini: {
    id: 'gemini',
    name: 'Gemini',
    desc: 'Google Gemini API',
    icon: '✦',
    color: '#4285F4',
    cloudCred: 'gcp',
    authMethods: [
      {
        id: 'gcp_adc',
        label: 'GCP Application Default Credentials',
        desc: 'Legacy authentication. Existing connections keep working; new Gemini connections use an API key.',
        fields: [],
        usesCloudCred: true,
        legacy: true,
      },
      {
        id: 'api_key',
        label: 'API Key',
        desc: 'Direct Gemini API key',
        fields: [{ key: 'apiKey', label: 'Gemini API Key', placeholder: 'AI...', secret: true }],
      },
    ],
    config: [
      // No baked-in model default: pick from discovery or enter an id explicitly.
      { key: 'model', label: 'Model', placeholder: 'Pick a discovered model or enter a model id', def: '', type: 'text', group: 'config' },
      { key: 'baseUrl', label: 'Base URL (optional)', placeholder: 'https://generativelanguage.googleapis.com', def: '', type: 'text', group: 'config' },
    ],
  },
  compatible: {
    id: 'compatible',
    name: 'OpenAI Compatible',
    desc: 'Any compatible endpoint',
    icon: '⟡',
    color: '#8B8B8B',
    cloudCred: null,
    authMethods: [
      {
        id: 'api_key',
        label: 'API Key',
        desc: 'Standard API key auth',
        fields: [{ key: 'apiKey', label: 'API Key', placeholder: 'Enter API key', secret: true }],
      },
      {
        id: 'none',
        label: 'No Authentication',
        desc: 'For VPC or pre-authed endpoints',
        fields: [],
      },
    ],
    config: [
      { key: 'displayName', label: 'Display Name', placeholder: 'Custom Provider', def: 'Custom Provider', type: 'text', group: 'identity' },
      { key: 'apiType', label: 'API Type', def: 'chat_completions', type: 'select', options: ['chat_completions', 'responses'], group: 'config' },
      { key: 'model', label: 'Model', placeholder: 'Enter model name', def: '', type: 'text', group: 'config' },
      { key: 'baseUrl', label: 'Base URL', placeholder: 'https://your-endpoint.com/v1', def: '', type: 'text', group: 'config' },
      { key: 'compactionEnabled', label: 'Responses Compaction', def: false, type: 'toggle', group: 'config' },
      { key: 'compactionTriggerTokens', label: 'Compaction Token Limit', def: OPENAI_COMPACTION_DEFAULT_TRIGGER_TOKENS, type: 'number', group: 'config' },
      { key: 'authHeader', label: 'Auth Header', placeholder: 'Authorization', def: 'Authorization', type: 'text', group: 'auth_detail' },
      { key: 'authScheme', label: 'Auth Scheme', placeholder: 'Bearer', def: 'Bearer', type: 'text', group: 'auth_detail' },
      { key: 'modelInBody', label: 'Include model in request body', def: true, type: 'toggle', group: 'placement' },
      { key: 'modelInPath', label: 'Include model in URL path', def: false, type: 'toggle', group: 'placement' },
      { key: 'endpointSuffix', label: 'Endpoint Suffix', placeholder: '/beta', def: '', type: 'text', group: 'advanced' },
      { key: 'timeout', label: 'Timeout (ms)', placeholder: 'No timeout', def: '', type: 'number', group: 'advanced' },
      { key: 'reasoningEffort', label: 'Reasoning Effort', def: 'Default', type: 'select', options: ['Default', 'low', 'medium', 'high'], group: 'advanced' },
      { key: 'queryParams', label: 'Query Params (JSON)', placeholder: '{"stream":"false"}', def: '', type: 'json', group: 'advanced' },
      { key: 'payloadOverrides', label: 'Payload Overrides (JSON)', placeholder: '{"temperature":0.2}', def: '', type: 'json', group: 'advanced' },
      { key: 'codeExecutionEnabled', label: 'Code Sandbox', def: false, type: 'toggle', group: 'config' },
      { key: 'codeExecutionToolType', label: 'Code Sandbox Tool Type', placeholder: 'code_interpreter', def: 'code_interpreter', type: 'text', group: 'advanced' },
      { key: 'codeExecutionMemoryLimit', label: 'Code Sandbox Memory Limit', placeholder: '4g', def: '', type: 'text', group: 'advanced' },
      { key: 'codeExecutionContainerMode', label: 'Code Sandbox Container Mode', def: 'auto', type: 'select', options: ['auto', 'existing'], group: 'advanced' },
      { key: 'codeExecutionContainerId', label: 'Code Sandbox Container ID', placeholder: 'cntr_abc123', def: '', type: 'text', group: 'advanced' },
    ],
  },
};

export function getProviderDef(id: ProviderCardType): ProviderDef {
  return PROVIDER_DEFS[id];
}

/**
 * True when a stored config authenticates via a method that is no longer
 * offered for new configurations (currently Gemini via GCP ADC). Such configs
 * remain visible and editable, but surfaces should show a legacy warning.
 */
export function isLegacyAuthConfig(
  providerId: ProviderCardType,
  authType?: string | null,
): boolean {
  if (authType !== 'cloud_credentials') return false;
  const def = PROVIDER_DEFS[providerId];
  return Boolean(def?.authMethods.some((method) => method.usesCloudCred && method.legacy));
}
