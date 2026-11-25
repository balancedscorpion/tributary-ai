import type { CompatibleProfile, ProviderConfig } from '@/types';
import type { ApiSurface, CapabilityControlSetting, FeatureKey, ThreadTarget } from './types';

export const HOSTED_WEB_SEARCH_FEATURE = 'hosted_web_search';
export const HOSTED_CODE_EXECUTION_FEATURE = 'hosted_code_execution';
export const NATIVE_COMPACTION_FEATURE = 'native_compaction';

export type CapabilityControls = Partial<Record<FeatureKey, CapabilityControlSetting>>;

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

/**
 * The connection credentials are validated against. An EXPLICIT target must
 * authenticate as itself: when its connection no longer exists this returns
 * null (fail closed) — it never silently substitutes the global default.
 * The active-config fallback applies only when no target was provided
 * (legacy send paths with capability preflight disabled).
 */
export function resolveAuthTargetConfig(
  target: ThreadTarget | null | undefined,
  providerConfigs: ProviderConfig[] | undefined,
  activeConfig: ProviderConfig | null,
): ProviderConfig | null {
  if (target) {
    return (providerConfigs || []).find((c) => c && c.id === target.connectionId) || null;
  }
  return activeConfig;
}

/**
 * Resolves the legacy profile that owns a compatible connection's secret.
 * A saved ProviderConfig is keyed by its own id and must never borrow the
 * first unrelated legacy profile. The first-profile fallback exists only for
 * legacy callers that do not have a saved compatible connection.
 */
export function resolveCompatibleAuthProfile(
  config: ProviderConfig | null,
  profileId: string | null,
  profiles: CompatibleProfile[],
): CompatibleProfile | null {
  const exact = profiles.find((profile) => profile.id === profileId) || null;
  if (config?.providerId === 'compatible') return exact;
  return exact || profiles[0] || null;
}

export function deriveProviderApiSurface(config: Pick<ProviderConfig, 'providerId' | 'apiType' | 'apiSurface'>): ApiSurface {
  const explicit = typeof config.apiSurface === 'string' ? config.apiSurface.trim() : '';
  if (explicit) return explicit;

  switch (config.providerId) {
    case 'openai':
      return 'responses';
    case 'anthropic':
      return 'messages';
    case 'gemini':
      return 'generate_content';
    case 'bedrock':
      if (config.apiType === 'messages' || !config.apiType) return 'invoke_stream';
      return config.apiType;
    case 'compatible':
      return config.apiType || 'chat_completions';
    default:
      return config.apiType || String(config.providerId);
  }
}

/**
 * Provider-hosted web search and code execution are only available on the
 * native Responses/Messages surfaces. Keep this check shared by Settings
 * migration and the Connections UI so stale capability flags on another
 * surface cannot make an unusable connection appear configured.
 */
export function supportsProviderHostedTools(
  config: Pick<ProviderConfig, 'providerId' | 'apiType' | 'apiSurface'>,
): boolean {
  const surface = deriveProviderApiSurface(config);
  return (
    (config.providerId === 'openai' && surface === 'responses') ||
    (config.providerId === 'anthropic' && surface === 'messages') ||
    (config.providerId === 'compatible' && surface === 'responses')
  );
}

/** Defaults for a connection created by the new editor. Consent features are never Auto. */
export function createNewConnectionCapabilityControls(
  compactionEnabled?: boolean,
): CapabilityControls {
  return {
    [HOSTED_WEB_SEARCH_FEATURE]: 'off',
    [HOSTED_CODE_EXECUTION_FEATURE]: 'off',
    [NATIVE_COMPACTION_FEATURE]: typeof compactionEnabled === 'boolean'
      ? (compactionEnabled ? 'on' : 'off')
      : 'auto',
  };
}

export function applyNewConnectionCapabilityDefaults<T extends ProviderConfig>(config: T): T {
  return {
    ...config,
    apiSurface: deriveProviderApiSurface(config),
    capabilityControls: {
      ...createNewConnectionCapabilityControls(config.compactionEnabled),
      ...(preserveCapabilityControls(config.capabilityControls) || {}),
    },
  };
}

/**
 * Converts a pre-capability config without changing its effective behavior.
 * The caller must only use this when capabilityControls is absent.
 */
export function migrateLegacyCapabilityControls(config: {
  providerId: ProviderConfig['providerId'];
  webSearch?: unknown;
  codeExecution?: unknown;
  compactionEnabled?: unknown;
}): CapabilityControls {
  const web = isRecord(config.webSearch) && typeof config.webSearch.enabled === 'boolean'
    ? config.webSearch.enabled
    : config.providerId === 'openai' || config.providerId === 'anthropic';
  const code = isRecord(config.codeExecution) && typeof config.codeExecution.enabled === 'boolean'
    ? config.codeExecution.enabled
    : false;
  const compaction = typeof config.compactionEnabled === 'boolean'
    ? (config.compactionEnabled ? 'on' : 'off')
    : 'auto';

  return {
    [HOSTED_WEB_SEARCH_FEATURE]: web ? 'on' : 'off',
    [HOSTED_CODE_EXECUTION_FEATURE]: code ? 'on' : 'off',
    [NATIVE_COMPACTION_FEATURE]: compaction,
  };
}

/**
 * Retains every server/future feature key. Known settings are not reconstructed,
 * so a save/load cycle cannot erase registry additions a newer build wrote.
 */
export function preserveCapabilityControls(value: unknown): CapabilityControls | undefined {
  if (!isRecord(value)) return undefined;
  return { ...value } as CapabilityControls;
}
