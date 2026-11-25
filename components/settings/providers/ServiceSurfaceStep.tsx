import React from 'react';
import { Check, Network } from 'lucide-react';
import type { ProviderDef } from '@/providers/catalog/providerDefs';
import { PROVIDER_SURFACES } from '@/providers/catalog/providerSurfaces';
import type { ProviderCardType, ProviderConfig } from '@/types';

type ApiType = NonNullable<ProviderConfig['apiType']>;

interface ServiceSurfaceStepProps {
  providers: ProviderDef[];
  selectedProviderId: ProviderCardType | null;
  selectedApiType: ApiType | null;
  onSelect: (providerId: ProviderCardType, apiType: ApiType | null) => void;
}

/**
 * Surface-first service picker (plan §4.2), driven by the canonical surface
 * catalog in providers/catalog/providerSurfaces.ts. Family groups carry the
 * family glyph tile; each surface row uses a generic protocol glyph and a
 * precise text label — no repeated third-party branding (D-G9). Unavailable
 * surfaces (e.g. Vertex AI) render disabled with their catalog reason.
 */
const ServiceSurfaceStep: React.FC<ServiceSurfaceStepProps> = ({
  providers, selectedProviderId, selectedApiType, onSelect,
}) => {
  return (
    <div data-service-surface-step className="space-y-3">
      {providers.map((p) => {
        const surfaces = PROVIDER_SURFACES.filter((s) => s.providerId === p.id);
        if (surfaces.length === 0) return null;
        const multiSurface = surfaces.filter((s) => s.available).length > 1;
        return (
          <div key={p.id} className="rounded-lg border border-border bg-card/30 p-2.5">
            <div className="flex items-center gap-2 mb-1.5">
              <div
                className="w-6 h-6 rounded-md border flex items-center justify-center text-[11px] font-bold shrink-0"
                aria-hidden
                style={{ color: p.color, backgroundColor: `${p.color}14`, borderColor: `${p.color}30` }}
              >
                {p.icon}
              </div>
              <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
                {surfaces[0].familyLabel}
              </span>
            </div>

            <div className="space-y-1" role="radiogroup" aria-label={`${surfaces[0].familyLabel} API surfaces`}>
              {surfaces.map((s) => {
                if (!s.available) {
                  return (
                    <div
                      key={s.id}
                      aria-disabled="true"
                      className="flex items-center gap-2 rounded-md border border-dashed border-border/70 px-2.5 py-2 text-left opacity-60"
                    >
                      <Network size={13} className="shrink-0 text-muted-foreground" aria-hidden />
                      <span className="text-xs text-muted-foreground">
                        {s.label}{s.unavailableReason ? ` — ${s.unavailableReason.toLowerCase()}` : ''}
                      </span>
                    </div>
                  );
                }
                // Single-surface families don't persist an apiType discriminator
                // (matches the legacy editor's field model for those providers).
                const optionApiType: ApiType | null = multiSurface ? s.legacyApiType : null;
                const selected = selectedProviderId === p.id
                  && (multiSurface ? selectedApiType === s.legacyApiType : true);
                return (
                  <button
                    key={s.id}
                    type="button"
                    role="radio"
                    aria-checked={selected}
                    data-surface-option={`${p.id}:${multiSurface ? s.legacyApiType : 'default'}`}
                    className="w-full flex items-center gap-2 rounded-md border bg-card/50 px-2.5 py-2 min-h-[44px] text-left transition-colors hover:bg-card/80 focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
                    style={{
                      borderColor: selected ? p.color : 'hsl(var(--border))',
                      backgroundColor: selected ? `${p.color}08` : undefined,
                    }}
                    onClick={() => onSelect(p.id, optionApiType)}
                  >
                    <Network size={13} className="shrink-0 text-muted-foreground" aria-hidden />
                    <span className="min-w-0 flex-1">
                      <span className="block text-xs font-medium">{s.label}</span>
                      <span className="block text-[10px] text-muted-foreground leading-snug">{s.description}</span>
                    </span>
                    {selected && (
                      <span
                        className="w-4 h-4 rounded-full flex items-center justify-center text-white shrink-0"
                        style={{ backgroundColor: p.color }}
                        aria-hidden
                      >
                        <Check size={10} />
                      </span>
                    )}
                  </button>
                );
              })}
            </div>
          </div>
        );
      })}
    </div>
  );
};

export default ServiceSurfaceStep;
