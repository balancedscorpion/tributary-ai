import React, { useMemo, useState } from 'react';
import { ChevronRight, ShieldAlert } from 'lucide-react';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { Button } from '@/components/ui/button';
import { sortedCapabilityEntries } from '@/lib/capability/format';
import type { CapabilityControlSetting, FeatureKey } from '@/lib/capability/types';
import type { CapabilityReport, CapabilityReportStatus, FeatureResolution } from './capabilityContract';
import CapabilityChip, { capabilityViewState, modeModifierText } from './CapabilityChip';
import EvidenceInspector from './EvidenceInspector';

export interface CapabilityMatrixControls {
  /** Persist a control change; resolves when saved. */
  onChange: (feature: FeatureKey, setting: CapabilityControlSetting) => void | Promise<void>;
  /** Deep link to the Permissions surface, when the host can provide one. */
  onOpenPermissions?: () => void;
  /** Features managed on another settings surface remain visible but read-only here. */
  readOnlyFeatures?: FeatureKey[];
}

/**
 * Read-only grouped capability summary (plan §4.3).
 *
 * Registry-driven by design: rows come from the report's feature entries and their
 * server-provided `meta` (label/group/order). This component must never derive a
 * row, state, or availability from providerId/apiType (plan P1) — unknown feature
 * keys render generically from their metadata.
 */

const GROUP_LABELS: Record<string, string> = {
  core: 'Core',
  tools: 'Tools',
  inputs: 'Attachments',
  hosted: 'Hosted tools',
  reasoning: 'Reasoning',
  output: 'Output',
  state: 'State & continuity',
  ops: 'Limits & usage',
  safety: 'Safety',
};

function groupLabel(group: string): string {
  return GROUP_LABELS[group] || group.charAt(0).toUpperCase() + group.slice(1);
}

const RISK_TAG_TEXT: Record<string, string> = {
  billable: 'billed usage',
  external_egress: 'sends data outside the provider',
  code_execution: 'runs code',
};

function consentReasonText(riskTags: string[]): string {
  const parts = riskTags.map((t) => RISK_TAG_TEXT[t] || t.replace(/_/g, ' '));
  const why = parts.length > 0 ? parts.join(', ') : 'sensitive capability';
  return `Requires explicit enable and permission — ${why}. Never turned on automatically.`;
}

/** Row-level secondary text: conditions, reasons, mode modifier, set-but-unavailable. */
function rowDetailText(res: FeatureResolution): string | null {
  const view = capabilityViewState(res);
  const bits: string[] = [];
  if (res.control?.setting === 'on' && view === 'not_available') bits.push('On (set) — not available here');
  const mod = modeModifierText(res.mode);
  if (mod) bits.push(mod);
  if (res.conditions.length > 0) bits.push(res.conditions.map((c) => c.text).join(' · '));
  if ((view === 'not_available' || view === 'blocked') && res.reasons[0]?.text) bits.push(res.reasons[0].text);
  return bits.length > 0 ? bits.join(' · ') : null;
}

interface CapabilityMatrixRow {
  key: string;
  resolution: FeatureResolution;
}

interface CapabilityMatrixProps {
  status: CapabilityReportStatus;
  report: CapabilityReport | null;
  errorMessage?: string | null;
  /** Compact mode: headline features only (card popovers / summaries). */
  compact?: boolean;
  /** Unique prefix so multiple matrices can coexist in one document. */
  idPrefix: string;
  /** When present, controllable rows render interactive Auto/On/Off (Class S) or Off/On (consent) selects. */
  controls?: CapabilityMatrixControls;
}

const CapabilityMatrix: React.FC<CapabilityMatrixProps> = ({ status, report, errorMessage, compact, idPrefix, controls }) => {
  const [openEvidenceKey, setOpenEvidenceKey] = useState<string | null>(null);
  const [controlAnnouncement, setControlAnnouncement] = useState('');
  const [savingFeature, setSavingFeature] = useState<string | null>(null);

  const handleControlChange = async (key: string, label: string, setting: CapabilityControlSetting) => {
    if (!controls) return;
    setSavingFeature(key);
    try {
      await Promise.resolve(controls.onChange(key, setting));
      const word = setting === 'auto' ? 'Auto' : setting === 'on' ? 'On' : 'Off';
      setControlAnnouncement(`${label} set to ${word}.`);
    } catch (e: any) {
      setControlAnnouncement(`Could not update ${label}${e?.message ? `: ${e.message}` : '.'}`);
    } finally {
      setSavingFeature(null);
    }
  };

  const groups = useMemo(() => {
    if (!report) return [] as Array<{ group: string; rows: CapabilityMatrixRow[] }>;
    // Shared registry-driven ordering (lib/capability/format), grouped for display.
    const entries = sortedCapabilityEntries(report.features)
      .filter(([, resolution]) => resolution && typeof resolution === 'object')
      .filter(([, resolution]) => !compact || resolution.meta?.headline);
    const out: Array<{ group: string; rows: CapabilityMatrixRow[] }> = [];
    for (const [key, resolution] of entries) {
      const group = String(resolution.meta?.group || 'other');
      const last = out[out.length - 1];
      if (last && last.group === group) last.rows.push({ key, resolution });
      else out.push({ group, rows: [{ key, resolution }] });
    }
    return out;
  }, [compact, report]);

  const liveText = status === 'loading'
    ? 'Loading capability report…'
    : status === 'ready' && report
      ? `Capability report loaded for ${report.target.model || 'this target'}.`
      : '';

  return (
    <div data-capability-matrix className="space-y-2">
      {/* One logical live region per async workflow: load status + control saves. */}
      <span className="sr-only" role="status" aria-live="polite">
        {[liveText, controlAnnouncement].filter(Boolean).join(' ')}
      </span>

      {status === 'loading' && (
        <div className="space-y-1.5" aria-hidden>
          <Skeleton className="h-5 w-2/5" />
          <Skeleton className="h-5 w-3/5" />
          <Skeleton className="h-5 w-1/2" />
        </div>
      )}

      {status === 'unavailable' && (
        <p data-capability-matrix-state="unavailable" className="text-[11px] text-muted-foreground">
          Capability report isn’t available yet — the capability service hasn’t been connected.
          Features stay unverified until it is; nothing is assumed.
        </p>
      )}

      {status === 'error' && (
        <p data-capability-matrix-state="error" className="text-[11px] text-destructive">
          Couldn’t load the capability report{errorMessage ? `: ${errorMessage}` : '.'} Capabilities are
          shown as unverified rather than guessed.
        </p>
      )}

      {status === 'ready' && report && groups.length === 0 && (
        <p className="text-[11px] text-muted-foreground">The capability report contained no features.</p>
      )}

      {status === 'ready' && report && groups.map(({ group, rows }) => (
        <div key={group} role="group" aria-label={`${groupLabel(group)} capabilities`} className="space-y-0.5">
          <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground pt-1">
            {groupLabel(group)}
          </div>
          {rows.map(({ key, resolution }) => {
            const view = capabilityViewState(resolution);
            const detail = rowDetailText(resolution);
            const label = resolution.meta?.label || key.replace(/_/g, ' ');
            const evidenceId = `${idPrefix}-evidence-${key}`;
            const isOpen = openEvidenceKey === key;
            const consent = resolution.class === 'explicit_consent';
            return (
              <div key={key} data-capability-row={key} className="rounded-md">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-0.5 py-0.5">
                  <button
                    type="button"
                    className="group flex min-w-0 flex-1 basis-52 items-center gap-1.5 rounded-md px-1 py-1 text-left hover:bg-card/70 focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
                    aria-expanded={isOpen}
                    aria-controls={evidenceId}
                    aria-label={`Show evidence for ${label}`}
                    onClick={() => setOpenEvidenceKey(isOpen ? null : key)}
                  >
                    <ChevronRight
                      size={12}
                      aria-hidden
                      className="shrink-0 text-muted-foreground transition-transform motion-reduce:transition-none"
                      style={{ transform: isOpen ? 'rotate(90deg)' : undefined }}
                    />
                    <span className="truncate text-xs">{label}</span>
                    {consent && (
                      <span
                        className="inline-flex items-center text-amber-500"
                        title={consentReasonText(resolution.riskTags)}
                      >
                        <ShieldAlert size={11} aria-hidden />
                        <span className="sr-only">{consentReasonText(resolution.riskTags)}</span>
                      </span>
                    )}
                  </button>
                  {controls &&
                    resolution.control?.controllable &&
                    !controls.readOnlyFeatures?.includes(key) && (
                    <Select
                      value={resolution.control.setting}
                      disabled={savingFeature === key || resolution.effective === 'blocked'}
                      onValueChange={(v) => void handleControlChange(key, label, v as CapabilityControlSetting)}
                    >
                      <SelectTrigger
                        data-capability-control={key}
                        aria-label={`${label} setting`}
                        className="h-6 w-[84px] shrink-0 px-2 text-[11px]"
                      >
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {/* Consent-class features never offer Auto (D-G2). */}
                        {!consent && <SelectItem value="auto">Auto</SelectItem>}
                        <SelectItem value="on">On</SelectItem>
                        <SelectItem value="off">Off</SelectItem>
                      </SelectContent>
                    </Select>
                  )}
                  <CapabilityChip viewState={view} />
                  {view === 'blocked' && controls?.onOpenPermissions && (
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-6 px-2 text-[10px]"
                      onClick={controls.onOpenPermissions}
                    >
                      Open Permissions
                    </Button>
                  )}
                </div>
                {detail && (
                  <div className="pl-6 pr-1 pb-0.5 text-[11px] leading-snug text-muted-foreground">{detail}</div>
                )}
                {isOpen && (
                  <div className="pl-6 pb-1.5">
                    <EvidenceInspector featureLabel={label} resolution={resolution} id={evidenceId} />
                  </div>
                )}
              </div>
            );
          })}
        </div>
      ))}
    </div>
  );
};

export default CapabilityMatrix;
