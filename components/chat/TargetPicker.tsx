import React, { useRef } from 'react';
import { Check, Settings2 } from 'lucide-react';
import type { ProviderConfig } from '@/types';
import type { ThreadTarget } from '@/lib/capability/types';
import { getProviderDef } from '@/providers/catalog/providerDefs';
import { deriveProviderSurface } from '@/providers/catalog/providerSurfaces';

interface TargetPickerProps {
  connections: ProviderConfig[];
  currentTarget: ThreadTarget | null;
  onSelect: (target: ThreadTarget) => void;
  /** Open Settings ▸ Providers. Omit to hide the manage footer (embedded use). */
  onManageConnections?: () => void;
  disabled?: boolean;
}

/**
 * Lightweight target list (plan §4.5): configured provider connections and
 * their configured model only — no browsing, no creation. Used inside the
 * TargetPill popover and embedded in the preflight sheet's switch-model fix.
 */
const TargetPicker: React.FC<TargetPickerProps> = ({
  connections, currentTarget, onSelect, onManageConnections, disabled,
}) => {
  const listRef = useRef<HTMLDivElement>(null);

  // Listbox keyboard contract: Arrow/Home/End move a roving focus across
  // enabled options (single tab stop); Enter activates the native button;
  // Escape bubbles to the popover/dialog that hosts the picker.
  const handleListKeyDown = (event: React.KeyboardEvent) => {
    if (!['ArrowDown', 'ArrowUp', 'Home', 'End'].includes(event.key)) return;
    const options = Array.from(
      listRef.current?.querySelectorAll<HTMLButtonElement>('[role="option"]:not(:disabled)') || [],
    );
    if (options.length === 0) return;
    event.preventDefault();
    const current = options.indexOf(document.activeElement as HTMLButtonElement);
    const next = event.key === 'Home'
      ? 0
      : event.key === 'End'
        ? options.length - 1
        : event.key === 'ArrowDown'
          ? (current < 0 ? 0 : Math.min(options.length - 1, current + 1))
          : (current < 0 ? options.length - 1 : Math.max(0, current - 1));
    options[next].focus();
  };

  const selectableIds = connections
    .filter((cfg) => !disabled && Boolean(cfg.model?.trim()))
    .map((cfg) => cfg.id);
  const selectedId = connections.find((cfg) => {
    const model = cfg.model?.trim() || '';
    return currentTarget?.connectionId === cfg.id
      && (!currentTarget.model || currentTarget.model === model)
      && Boolean(model);
  })?.id;
  const rovingFocusId = selectedId && selectableIds.includes(selectedId)
    ? selectedId
    : selectableIds[0];

  return (
    <div data-target-picker className="min-w-[260px]">
      <div
        ref={listRef}
        role="listbox"
        aria-label="Provider connection and model"
        className="space-y-0.5 p-1"
        onKeyDown={handleListKeyDown}
      >
        {connections.length === 0 && (
          <p className="px-2 py-3 text-[11px] text-muted-foreground">
            No provider connections configured yet.
          </p>
        )}
        {connections.map((cfg) => {
          const def = getProviderDef(cfg.providerId);
          const surface = deriveProviderSurface(cfg);
          const model = cfg.model?.trim() || '';
          const selected = currentTarget?.connectionId === cfg.id
            && (!currentTarget.model || currentTarget.model === model);
          return (
            <button
              key={cfg.id}
              type="button"
              role="option"
              aria-selected={selected}
              disabled={disabled || !model}
              tabIndex={cfg.id === rovingFocusId ? 0 : -1}
              data-target-option={cfg.id}
              className="w-full flex items-center gap-2 rounded-md px-2 py-1.5 min-h-[38px] text-left transition-colors hover:bg-card/80 disabled:opacity-50 disabled:cursor-not-allowed focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
              onClick={() => {
                if (!model) return;
                onSelect({ connectionId: cfg.id, model });
              }}
            >
              <span
                className="w-5 h-5 rounded border flex items-center justify-center text-[10px] font-bold shrink-0"
                aria-hidden
                style={{ color: def.color, backgroundColor: `${def.color}14`, borderColor: `${def.color}30` }}
              >
                {def.icon}
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-xs font-medium">
                  {cfg.displayName || def.name}
                </span>
                <span className="block truncate text-[10px] text-muted-foreground">
                  {surface.familyLabel} · {surface.label}
                  {model ? <span className="font-mono"> · {model}</span> : ' · no model configured'}
                </span>
              </span>
              {selected && <Check size={13} className="shrink-0 text-emerald-400" aria-hidden />}
            </button>
          );
        })}
      </div>
      {onManageConnections && (
        <div className="border-t border-border p-1">
          <button
            type="button"
            className="w-full flex items-center gap-1.5 rounded-md px-2 py-1.5 text-[11px] text-muted-foreground hover:text-foreground hover:bg-card/80 focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
            onClick={onManageConnections}
          >
            <Settings2 size={12} aria-hidden /> Manage provider connections…
          </button>
        </div>
      )}
    </div>
  );
};

export default TargetPicker;
