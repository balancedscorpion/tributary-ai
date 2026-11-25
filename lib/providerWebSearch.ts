import type { ProviderCardType, ProviderWebSearchConfig } from '@/types';

/**
 * The app exposes Web Search as one connection, not as an editable raw tool
 * definition. Always derive the provider-native tool schema locally so stale
 * settings written by older app builds cannot change the request contract.
 */
export function canonicalProviderWebSearch(
  providerId: ProviderCardType | string,
  enabled: boolean,
): ProviderWebSearchConfig {
  if (providerId === 'anthropic') {
    return {
      enabled,
      toolType: 'web_search_20260209',
      maxUses: 5,
      allowedCallers: ['direct'],
    };
  }

  return {
    enabled,
    toolType: 'web_search',
  };
}
