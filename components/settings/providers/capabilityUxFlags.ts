import { FEATURE_FLAGS } from '@/featureFlags';
import type { AppSettings } from '@/types';

/**
 * Gates for the capability-negotiated provider UX (plan §5.7). Each phase has
 * its own flag; all default OFF so legacy behavior/DOM stays byte-identical:
 *
 * - report:         capability report, chips, evidence surfaces
 * - editorV2:       surface-first editor + model discovery
 * - controls:       capability control mutations, checks, detail management
 * - preflight:      preflight send protocol and plan rows
 * - composerTarget: composer target pill/picker + header badge retirement
 *
 * Activation channels, in priority order:
 * 1. `?capabilityUx=…` URL param — dev/e2e override. `1`/`true` enables all,
 *    `0`/`false` disables all, and a comma list is the COMPLETE effective set
 *    (e.g. `?capabilityUx=report,preflight` — persisted flags are ignored).
 * 2. `FEATURE_FLAGS.capabilityUx.*` build-time defaults, OR-ed with
 * 3. `settings.featureFlags.capabilityUx.*` server-persisted flags.
 *
 * Dependencies are enforced centrally after channel resolution:
 * composerTarget requires preflight. A composer target switch without the
 * preflight/plan transport would persist target B in the pill while the
 * backend dispatches the global default — fail closed instead.
 */
export type CapabilityUxFlagKey = 'report' | 'editorV2' | 'controls' | 'preflight' | 'composerTarget';

export interface CapabilityUxFlags {
  report: boolean;
  editorV2: boolean;
  controls: boolean;
  preflight: boolean;
  composerTarget: boolean;
}

const FLAG_KEYS: CapabilityUxFlagKey[] = ['report', 'editorV2', 'controls', 'preflight', 'composerTarget'];

function readOverrideParam(): string | null {
  if (typeof window === 'undefined') return null;
  try {
    return new URLSearchParams(window.location.search).get('capabilityUx');
  } catch {
    return null;
  }
}

/** Cross-flag dependencies, applied to every channel's result (fail closed). */
export function enforceCapabilityFlagDependencies(flags: CapabilityUxFlags): CapabilityUxFlags {
  return { ...flags, composerTarget: flags.composerTarget && flags.preflight };
}

export function getCapabilityUxFlags(settings?: AppSettings | null): CapabilityUxFlags {
  const param = readOverrideParam();
  if (param === '1' || param === 'true') {
    return enforceCapabilityFlagDependencies(
      { report: true, editorV2: true, controls: true, preflight: true, composerTarget: true },
    );
  }
  if (param === '0' || param === 'false') {
    return { report: false, editorV2: false, controls: false, preflight: false, composerTarget: false };
  }
  const overrides = param
    ? new Set(param.split(',').map((s) => s.trim()).filter(Boolean))
    : null;

  const moduleFlags = (FEATURE_FLAGS as Record<string, any>).capabilityUx || {};
  const settingsFlags = (settings?.featureFlags as Record<string, any> | undefined)?.capabilityUx || {};

  const out = {} as CapabilityUxFlags;
  for (const key of FLAG_KEYS) {
    // A URL list is the complete highest-priority set; persisted flags apply
    // only when no list is present.
    out[key] = overrides
      ? overrides.has(key)
      : Boolean(moduleFlags[key] === true || settingsFlags[key] === true);
  }
  return enforceCapabilityFlagDependencies(out);
}

/** Back-compat helper for the first slice: true when the report surface is on. */
export function isCapabilityUxEnabled(settings?: AppSettings | null): boolean {
  return getCapabilityUxFlags(settings).report;
}
