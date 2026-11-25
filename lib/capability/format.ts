import type { CapabilityMode, FeatureResolution, FeatureKey } from './types';

export type CapabilityDisplayState =
  | 'on'
  | 'off'
  | 'not_available'
  | 'conditional'
  | 'unverified'
  | 'blocked';

export type CapabilityTone = 'positive' | 'neutral' | 'warning' | 'danger' | 'muted';

export interface CapabilityStatePresentation {
  label: string;
  symbol: string;
  tone: CapabilityTone;
}

export const CAPABILITY_STATE_PRESENTATION: Record<CapabilityDisplayState, CapabilityStatePresentation> = {
  on: { label: 'On', symbol: '✓', tone: 'positive' },
  off: { label: 'Off', symbol: '○', tone: 'neutral' },
  not_available: { label: 'Not available', symbol: '✕', tone: 'danger' },
  conditional: { label: 'Conditional', symbol: '◐', tone: 'warning' },
  unverified: { label: 'Unverified', symbol: '?', tone: 'muted' },
  blocked: { label: 'Blocked', symbol: '⛔', tone: 'danger' },
};

export const CAPABILITY_GROUP_ORDER = [
  'core',
  'tools',
  'inputs',
  'hosted',
  'reasoning',
  'output',
  'state',
  'ops',
  'safety',
] as const;

export function capabilityDisplayState(feature: FeatureResolution): CapabilityDisplayState {
  if (feature.state === 'unknown') return 'unverified';
  if (feature.state === 'unsupported' || feature.effective === 'unavailable') return 'not_available';
  if (feature.effective === 'blocked') return 'blocked';
  if (feature.effective === 'off') return 'off';
  if (feature.state === 'conditional') return 'conditional';
  if (feature.effective === 'on' && feature.state === 'supported') return 'on';

  // A newer semantic value must fail closed in the UI, never appear green.
  return 'unverified';
}

export function capabilityPresentation(feature: FeatureResolution): CapabilityStatePresentation {
  return CAPABILITY_STATE_PRESENTATION[capabilityDisplayState(feature)];
}

export function capabilityModeModifier(mode: CapabilityMode | null): { label: string; symbol: string } | null {
  if (mode === 'emulated') return { label: 'Emulated', symbol: '≈' };
  if (mode === 'reduced') return { label: 'Reduced fidelity', symbol: '▽' };
  return null;
}

export function sortedCapabilityEntries(
  features: Record<FeatureKey, FeatureResolution>,
): Array<[FeatureKey, FeatureResolution]> {
  return Object.entries(features).sort(([keyA, a], [keyB, b]) => {
    const groupA = String(a.meta?.group || '');
    const groupB = String(b.meta?.group || '');
    const rankA = (CAPABILITY_GROUP_ORDER as readonly string[]).indexOf(groupA);
    const rankB = (CAPABILITY_GROUP_ORDER as readonly string[]).indexOf(groupB);
    const normalizedRankA = rankA < 0 ? Number.MAX_SAFE_INTEGER : rankA;
    const normalizedRankB = rankB < 0 ? Number.MAX_SAFE_INTEGER : rankB;
    const knownGroupOrder = normalizedRankA - normalizedRankB;
    if (knownGroupOrder !== 0) return knownGroupOrder;
    const group = groupA.localeCompare(groupB);
    if (group !== 0) return group;
    const order = Number(a.meta?.order ?? 0) - Number(b.meta?.order ?? 0);
    if (order !== 0) return order;
    const label = String(a.meta?.label || keyA).localeCompare(String(b.meta?.label || keyB));
    return label !== 0 ? label : keyA.localeCompare(keyB);
  });
}

export function formatCapabilityTimestamp(value: string | number | Date, now = Date.now()): {
  relative: string;
  absolute: string;
} {
  const timestamp = value instanceof Date
    ? value.getTime()
    : typeof value === 'number'
      ? value
      : Date.parse(value);
  if (!Number.isFinite(timestamp)) return { relative: 'Unknown time', absolute: 'Unknown time' };

  const absolute = new Intl.DateTimeFormat(undefined, {
    dateStyle: 'medium',
    timeStyle: 'short',
  }).format(timestamp);
  const elapsed = Math.max(0, now - timestamp);
  const minute = 60_000;
  const hour = 60 * minute;
  const day = 24 * hour;
  if (elapsed < minute) return { relative: 'just now', absolute };
  if (elapsed < hour) return { relative: `${Math.floor(elapsed / minute)}m ago`, absolute };
  if (elapsed < day) return { relative: `${Math.floor(elapsed / hour)}h ago`, absolute };
  return { relative: `${Math.floor(elapsed / day)}d ago`, absolute };
}
