import React, { useMemo } from 'react';
import { ChevronRight, Pencil, Star, Trash2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import type { ProviderConfig } from '@/types';
import { getProviderDef, isLegacyAuthConfig } from '@/providers/catalog/providerDefs';
import { describeSurface } from './surfaceIdentity';
import { useCapabilityReportSeam } from './capabilityContract';
import CapabilityChip, { capabilityViewState } from './CapabilityChip';

function healthLine(cfg: ProviderConfig): { text: string; tone: 'ok' | 'warn' | 'bad' | 'muted' } {
  if (cfg.lastTestStatus === 'failure') return { text: 'Last check failed', tone: 'bad' };
  if (!cfg.lastTestedAt) return { text: 'Never checked', tone: 'muted' };
  const ageMs = Date.now() - cfg.lastTestedAt;
  const days = Math.floor(ageMs / 86_400_000);
  const rel = days >= 1
    ? `${days}d ago`
    : ageMs >= 3_600_000
      ? `${Math.floor(ageMs / 3_600_000)}h ago`
      : `${Math.max(1, Math.floor(ageMs / 60_000))}m ago`;
  if (days > 30) return { text: `Checked ${rel}`, tone: 'warn' };
  return { text: `Checked ${rel}`, tone: 'ok' };
}

const TONE_CLASS: Record<'ok' | 'warn' | 'bad' | 'muted', string> = {
  ok: 'text-muted-foreground',
  warn: 'text-amber-500',
  bad: 'text-destructive',
  muted: 'text-muted-foreground/70',
};

interface ConnectionCardProps {
  cfg: ProviderConfig;
  isActive: boolean;
  deleting: boolean;
  /** composerTarget flag: the global default is only a default for NEW chats. */
  defaultForNewChatsWording?: boolean;
  onUse: () => void;
  onEdit: () => void;
  onOpenDetail: () => void;
  onDeleteRequest: () => void;
  onDeleteConfirm: () => void;
  onDeleteCancel: () => void;
}

/**
 * Capability-aware connection card (plan §4.1). Family identity lives on the glyph
 * tile; line 2 is always family · surface · model. The chip strip shows headline
 * capabilities from the backend report only — with no report, capabilities read as
 * a single honest "Unverified" chip, never as implied support.
 */
const ConnectionCard: React.FC<ConnectionCardProps> = ({
  cfg, isActive, deleting, defaultForNewChatsWording,
  onUse, onEdit, onOpenDetail, onDeleteRequest, onDeleteConfirm, onDeleteCancel,
}) => {
  const def = getProviderDef(cfg.providerId);
  const surface = describeSurface(cfg.providerId, cfg.apiType);
  const { status, report } = useCapabilityReportSeam(cfg, true, { savedConnection: true });
  const health = healthLine(cfg);
  const name = cfg.displayName || def.name;

  const headline = useMemo(() => {
    if (status !== 'ready' || !report) return null;
    const rows = Object.entries(report.features)
      .filter(([, res]) => res?.meta?.headline)
      .sort((a, b) => (a[1].meta?.order ?? 0) - (b[1].meta?.order ?? 0))
      .slice(0, 4);
    const unverified = Object.values(report.features).filter((res) => res?.state === 'unknown').length;
    return { rows, unverified };
  }, [report, status]);

  return (
    <div
      data-connection-card={cfg.id}
      className="rounded-lg border bg-card/50 hover:bg-card/80 transition-colors p-3"
      style={{ borderColor: isActive ? `${def.color}50` : undefined }}
    >
      <div className="flex items-start justify-between gap-2 flex-wrap">
        <div className="flex items-start gap-3 min-w-0 flex-1">
          <div
            className="w-8 h-8 rounded-md border flex items-center justify-center text-sm font-bold shrink-0"
            aria-hidden
            style={{ color: def.color, backgroundColor: `${def.color}14`, borderColor: `${def.color}30` }}
          >
            {def.icon}
          </div>
          <div className="min-w-0">
            <div className="text-sm font-medium truncate flex items-center gap-2">
              {name}
              {isActive && (
                <span className="inline-flex items-center gap-1 text-[10px] font-semibold text-primary bg-primary/10 px-1.5 py-0.5 rounded">
                  <Star size={10} aria-hidden /> {defaultForNewChatsWording ? 'Default for new chats' : 'Active'}
                </span>
              )}
              {isLegacyAuthConfig(cfg.providerId, cfg.authType) && (
                <span
                  data-legacy-auth-badge
                  title="This connection uses a legacy authentication method. It keeps working; new connections use an API key."
                  className="inline-flex items-center text-[10px] font-semibold text-amber-500 bg-amber-500/10 px-1.5 py-0.5 rounded"
                >
                  Legacy auth
                </span>
              )}
            </div>
            <div data-connection-identity className="text-[11px] text-muted-foreground truncate">
              {surface.familyLabel} · {surface.surfaceLabel}
              {cfg.model?.trim() ? <span className="font-mono"> · {cfg.model.trim()}</span> : null}
            </div>

            <button
              type="button"
              data-connection-chip-strip
              className="mt-1 flex flex-wrap items-center gap-x-2.5 gap-y-0.5 rounded-md px-1 -mx-1 py-0.5 text-left hover:bg-card focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
              aria-label={`Show capabilities and evidence for ${name}`}
              onClick={onOpenDetail}
            >
              {headline ? (
                <>
                  {headline.rows.map(([key, res]) => (
                    <CapabilityChip key={key} viewState={capabilityViewState(res)} label={res.meta?.label || key} />
                  ))}
                  {headline.unverified > 0 && (
                    <CapabilityChip viewState="unverified" label={`${headline.unverified}`} />
                  )}
                </>
              ) : (
                <CapabilityChip viewState="unverified" label="Capabilities" />
              )}
              <ChevronRight size={11} className="text-muted-foreground" aria-hidden />
            </button>

            <div className={`text-[10px] ${TONE_CLASS[health.tone]}`}>{health.text}</div>
          </div>
        </div>

        <div className="flex items-center gap-1 shrink-0">
          {!isActive && (
            <Button variant="outline" size="sm" className="h-7 px-2 text-[11px]" onClick={onUse}>
              {defaultForNewChatsWording ? 'Make default' : 'Use'}
            </Button>
          )}
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7 text-muted-foreground"
            aria-label={`Edit ${name}`}
            onClick={onEdit}
          >
            <Pencil size={13} aria-hidden />
          </Button>
          {deleting ? (
            <div className="flex items-center gap-1">
              <Button size="sm" variant="destructive" className="h-6 px-2 text-[11px]" onClick={onDeleteConfirm}>
                Delete
              </Button>
              <Button size="sm" variant="outline" className="h-6 px-2 text-[11px]" onClick={onDeleteCancel}>
                Cancel
              </Button>
            </div>
          ) : (
            <Button
              variant="ghost"
              size="icon"
              className="h-7 w-7 text-muted-foreground"
              aria-label={`Delete ${name}`}
              onClick={onDeleteRequest}
            >
              <Trash2 size={13} aria-hidden />
            </Button>
          )}
        </div>
      </div>
    </div>
  );
};

export default ConnectionCard;
