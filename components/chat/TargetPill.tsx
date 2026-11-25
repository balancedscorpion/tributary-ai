import React, { useMemo, useRef, useState } from 'react';
import { ChevronDown } from 'lucide-react';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip';
import type { ProviderConfig } from '@/types';
import type { ThreadTarget } from '@/lib/capability/types';
import { getProviderDef } from '@/providers/catalog/providerDefs';
import { deriveProviderSurface } from '@/providers/catalog/providerSurfaces';
import TargetPicker from './TargetPicker';

function healthDotClass(cfg: ProviderConfig | null): string {
  if (!cfg) return 'bg-muted-foreground/50';
  if (cfg.lastTestStatus === 'failure') return 'bg-destructive';
  if (!cfg.lastTestedAt) return 'bg-muted-foreground/50';
  if (Date.now() - cfg.lastTestedAt > 30 * 86_400_000) return 'bg-amber-500';
  return 'bg-emerald-400';
}

interface TargetPillProps {
  connections: ProviderConfig[];
  target: ThreadTarget | null;
  /** Thread has an active run: switching is disabled with an accessible reason. */
  runActive: boolean;
  /** A send is mid-negotiation (auth/preflight): switching is locked. */
  negotiating?: boolean;
  /** Persist the switch (PUT /target). Resolve true on success. */
  onSwitchTarget: (target: ThreadTarget) => Promise<boolean>;
  onManageConnections: () => void;
}

/**
 * Compact active-target pill for the composer (plan §4.5): family glyph,
 * model, health dot. Opens a lightweight picker of configured targets only.
 * Switching never mutates the global default connection.
 */
const TargetPill: React.FC<TargetPillProps> = ({
  connections, target, runActive, negotiating, onSwitchTarget, onManageConnections,
}) => {
  const [open, setOpen] = useState(false);
  const [continuityNote, setContinuityNote] = useState<string | null>(null);
  const [switching, setSwitching] = useState(false);
  const popoverContentRef = useRef<HTMLDivElement>(null);
  const blockedReason = runActive
    ? 'Wait for the current response to finish before switching models.'
    : negotiating
      ? 'Hold on — the current message is still being checked before sending.'
      : null;

  const activeCfg = useMemo(
    () => connections.find((c) => c.id === target?.connectionId) || null,
    [connections, target?.connectionId],
  );
  const def = activeCfg ? getProviderDef(activeCfg.providerId) : null;
  const model = target?.model?.trim() || activeCfg?.model?.trim() || '';
  const surfaceLabel = activeCfg ? deriveProviderSurface(activeCfg).label : '';

  const pillLabel = activeCfg
    ? `Model: ${model || 'not configured'} on ${activeCfg.displayName || def?.name}. Change model.`
    : 'No provider connection configured. Open settings to add one.';

  const handleSelect = async (next: ThreadTarget) => {
    if (next.connectionId === target?.connectionId && next.model === target?.model) {
      setOpen(false);
      return;
    }
    setSwitching(true);
    try {
      const ok = await onSwitchTarget(next);
      if (ok) {
        setOpen(false);
        // The server plan stays authoritative; this is a courtesy heads-up only.
        setContinuityNote(
          'Continuing portably. Native session state from the previous model won’t transfer; the next reply starts from the conversation text.',
        );
      }
    } finally {
      setSwitching(false);
    }
  };

  const pillButton = (
    <button
      type="button"
      data-target-pill
      aria-label={pillLabel}
      disabled={Boolean(blockedReason) || switching}
      className="flex items-center gap-1.5 rounded-xl border border-border/60 bg-muted/40 px-2 py-1 min-h-[28px] max-w-[240px] text-[11px] text-muted-foreground hover:text-foreground hover:border-border transition-colors disabled:opacity-60 disabled:cursor-not-allowed focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
    >
      {def ? (
        <span
          className="w-4 h-4 rounded border flex items-center justify-center text-[9px] font-bold shrink-0"
          aria-hidden
          style={{ color: def.color, backgroundColor: `${def.color}14`, borderColor: `${def.color}30` }}
        >
          {def.icon}
        </span>
      ) : null}
      <span className="truncate font-mono">{model || 'no model'}</span>
      <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${healthDotClass(activeCfg)}`} aria-hidden />
      <ChevronDown size={11} className="shrink-0" aria-hidden />
    </button>
  );

  return (
    <div className="flex flex-col items-start gap-1 min-w-0">
      <Popover open={open && !blockedReason} onOpenChange={(next) => { if (!blockedReason) setOpen(next); }}>
        {blockedReason ? (
          <Tooltip>
            <TooltipTrigger asChild>
              {/* Disabled buttons swallow hover; wrap so the reason stays reachable. */}
              <span tabIndex={0} className="inline-flex rounded-xl focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring">
                {pillButton}
              </span>
            </TooltipTrigger>
            <TooltipContent>{blockedReason}</TooltipContent>
          </Tooltip>
        ) : (
          <PopoverTrigger asChild>{pillButton}</PopoverTrigger>
        )}
        <PopoverContent
          ref={popoverContentRef}
          align="start"
          side="top"
          className="w-auto p-0"
          onOpenAutoFocus={(event) => {
            // Land initial focus on the roving option (the content wrapper is
            // not the listbox, so arrow keys would otherwise go nowhere).
            event.preventDefault();
            popoverContentRef.current
              ?.querySelector<HTMLElement>('[role="option"][tabindex="0"]')
              ?.focus();
          }}
        >
          <TargetPicker
            connections={connections}
            currentTarget={target}
            disabled={switching}
            onSelect={(next) => void handleSelect(next)}
            onManageConnections={() => {
              setOpen(false);
              onManageConnections();
            }}
          />
        </PopoverContent>
      </Popover>
      {continuityNote && (
        <p
          data-target-continuity-note
          role="status"
          className="text-[10px] text-muted-foreground max-w-[420px]"
        >
          {continuityNote}
          <button
            type="button"
            className="ml-1.5 underline underline-offset-2 hover:text-foreground"
            onClick={() => setContinuityNote(null)}
          >
            Dismiss
          </button>
        </p>
      )}
    </div>
  );
};

export default TargetPill;
