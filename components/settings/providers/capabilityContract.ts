/**
 * Thin adapter between the capability infrastructure (lib/capability, services,
 * hooks/useCapabilityReport) and the visual provider-settings components in this
 * folder. Types are re-exported from the authoritative module — nothing here
 * duplicates the §5 contracts; this file only maps the hook result onto the
 * view-model status the visual components render ('unavailable' = the capability
 * backend is not connected yet, an expected condition that must stay quiet).
 */
import { useCapabilityReport } from '@/hooks/useCapabilityReport';
import { CapabilityApiError } from '@/lib/capability/http';
import type { CapabilityReport } from '@/lib/capability/types';
import type { ProviderConfig } from '@/types';

export type {
  CapabilityReport,
  FeatureResolution,
  FeatureMeta,
  FeatureKey,
  CapabilityClass,
  RiskTag,
  CapabilityEvidence,
  CapabilityLimits,
} from '@/lib/capability/types';

export type CapabilityReportStatus = 'idle' | 'loading' | 'ready' | 'unavailable' | 'error';

export interface CapabilityReportState {
  status: CapabilityReportStatus;
  report: CapabilityReport | null;
  /** Human-readable failure detail for status 'error'. */
  errorMessage: string | null;
  /** Force a fresh fetch (e.g. after a conclusive capability check). */
  refetch: () => void;
}

/** Backend-missing responses are a normal shared-worktree/rollout condition. */
function isServiceMissing(error: Error): boolean {
  return error instanceof CapabilityApiError
    && (error.status === 404 || error.status === 405 || error.status === 501 || error.status === 0);
}

/**
 * Resolve a capability report for a saved connection or an editor draft.
 * Saved connections (a persisted id) use the connection endpoint; drafts use
 * the resolve endpoint — both via services/capabilityApi through the shared hook.
 */
export function useCapabilityReportSeam(
  config: ProviderConfig | null,
  enabled: boolean,
  opts?: { savedConnection?: boolean }
): CapabilityReportState {
  const { report, error, isLoading, refetch } = useCapabilityReport({
    connectionId: opts?.savedConnection ? config?.id ?? null : null,
    config,
    model: config?.model ?? null,
    enabled: enabled && Boolean(config),
  });

  if (!enabled || !config) return { status: 'idle', report: null, errorMessage: null, refetch };
  if (report) return { status: 'ready', report, errorMessage: null, refetch };
  if (isLoading) return { status: 'loading', report: null, errorMessage: null, refetch };
  if (error) {
    if (isServiceMissing(error)) return { status: 'unavailable', report: null, errorMessage: null, refetch };
    return { status: 'error', report: null, errorMessage: error.message, refetch };
  }
  return { status: 'loading', report: null, errorMessage: null, refetch };
}
