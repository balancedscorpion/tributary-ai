import type { ProviderCardType, ProviderConfig } from '@/types';
import { deriveProviderSurface } from '@/providers/catalog/providerSurfaces';

/**
 * Identity labels for the exact API surface of a connection (plan §2, §4.1).
 *
 * Canonical surface truth lives in providers/catalog/providerSurfaces.ts; this
 * module is a thin view helper over it. It describes WHAT a connection is
 * (family · surface) — capability/support facts come exclusively from the
 * backend capability report (plan P1). Keep this file free of support logic.
 */

export interface SurfaceIdentity {
  /** Provider family display name, e.g. "Amazon Bedrock". */
  familyLabel: string;
  /** Exact API-surface label, e.g. "Claude · Messages". */
  surfaceLabel: string;
  /** Short one-line positioning shown on editor surface cards. */
  surfaceHint: string;
}

type ApiType = NonNullable<ProviderConfig['apiType']> | null | undefined;

export function describeSurface(providerId: ProviderCardType, apiType: ApiType): SurfaceIdentity {
  const surface = deriveProviderSurface({ providerId, apiType: apiType ?? undefined });
  return {
    familyLabel: surface.familyLabel,
    surfaceLabel: surface.label,
    surfaceHint: surface.description,
  };
}

/** "Anthropic · Claude API · Messages · claude-opus-4-6" identity line pieces. */
export function connectionIdentityLine(cfg: Pick<ProviderConfig, 'providerId' | 'apiType' | 'model'>): string[] {
  const id = describeSurface(cfg.providerId, cfg.apiType);
  const parts = [id.familyLabel, id.surfaceLabel];
  if (cfg.model?.trim()) parts.push(cfg.model.trim());
  return parts;
}
