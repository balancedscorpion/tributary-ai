import type { ProviderConfig } from '@/types';
import { deriveProviderSurface } from './providerSurfaces';

/**
 * "Family · Surface · model" identity line for a run target. Lives in the
 * catalog (not a component) so data hooks can format labels without importing
 * UI modules.
 */
export function targetIdentityLabel(cfg: ProviderConfig): string {
  const surface = deriveProviderSurface(cfg);
  const model = cfg.model?.trim();
  return [surface.familyLabel, surface.label, model].filter(Boolean).join(' · ');
}
