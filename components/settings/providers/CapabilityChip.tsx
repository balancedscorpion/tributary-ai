import React from 'react';
import { Ban, Check, CircleDashed, CircleDot, HelpCircle, ShieldAlert } from 'lucide-react';
import {
  CAPABILITY_STATE_PRESENTATION,
  capabilityDisplayState,
  capabilityModeModifier,
  type CapabilityDisplayState,
} from '@/lib/capability/format';
import type { CapabilityMode, FeatureResolution } from '@/lib/capability/types';

/**
 * Visual layer over the shared capability state vocabulary (lib/capability/format):
 * every state renders icon + word + color — never color alone, and Unverified must
 * never look successful. State derivation itself lives in lib/capability/format
 * (`capabilityDisplayState`), re-exported here for this folder's components.
 */
export type CapabilityViewState = CapabilityDisplayState;
export const capabilityViewState: (
  res: Pick<FeatureResolution, 'state' | 'effective'>
) => CapabilityDisplayState = (res) => capabilityDisplayState(res as FeatureResolution);

const STATE_VISUALS: Record<CapabilityDisplayState, {
  className: string;
  Icon: React.ComponentType<{ size?: number | string; className?: string }>;
}> = {
  on: { className: 'text-emerald-400', Icon: Check },
  off: { className: 'text-muted-foreground', Icon: CircleDashed },
  conditional: { className: 'text-amber-500', Icon: CircleDot },
  not_available: { className: 'text-rose-400', Icon: Ban },
  unverified: { className: 'text-muted-foreground', Icon: HelpCircle },
  blocked: { className: 'text-amber-500', Icon: ShieldAlert },
};

/** Word + visual per display state; words come from the shared presentation map. */
export const CAPABILITY_STATE_META = Object.fromEntries(
  (Object.keys(STATE_VISUALS) as CapabilityDisplayState[]).map((state) => [state, {
    word: CAPABILITY_STATE_PRESENTATION[state].label,
    className: STATE_VISUALS[state].className,
    Icon: STATE_VISUALS[state].Icon,
  }]),
) as Record<CapabilityDisplayState, {
  word: string;
  className: string;
  Icon: React.ComponentType<{ size?: number | string; className?: string }>;
}>;

/** Modifier text for delivery mode (plan §2): "≈ emulated", "▽ reduced fidelity". */
export function modeModifierText(mode: CapabilityMode | null): string | null {
  const modifier = capabilityModeModifier(mode);
  return modifier ? `${modifier.symbol} ${modifier.label.toLowerCase()}` : null;
}

interface CapabilityChipProps {
  viewState: CapabilityViewState;
  /** Feature name for chip-strip usage, e.g. "Web search". Omit for bare state chips. */
  label?: string;
  className?: string;
}

/**
 * Compact icon+word chip. Used in matrix rows (state only) and connection-card
 * chip strips (label + state word).
 */
const CapabilityChip: React.FC<CapabilityChipProps> = ({ viewState, label, className }) => {
  const meta = CAPABILITY_STATE_META[viewState] || CAPABILITY_STATE_META.unverified;
  const Icon = meta.Icon;
  return (
    <span
      data-capability-chip={viewState}
      className={`inline-flex items-center gap-1 text-[11px] font-medium whitespace-nowrap ${meta.className} ${className || ''}`}
    >
      <Icon size={11} aria-hidden />
      <span>{label ? `${label}: ${meta.word}` : meta.word}</span>
    </span>
  );
};

export default CapabilityChip;
