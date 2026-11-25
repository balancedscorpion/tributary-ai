import React, { useMemo, useState } from 'react';
import { Ban, Check, ChevronRight, CircleDashed, FlaskConical, X } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Checkbox } from '@/components/ui/checkbox';
import { runProviderCheck } from '@/services/providerCheckApi';
import type { FeatureKey, ProviderCheckProbeResult } from '@/lib/capability/types';
import type { ProviderConfig } from '@/types';

/**
 * The safe capability probe bundle (plan D-G7). Consent-class hosted probes
 * (web search, code execution) are intentionally absent from this panel: they
 * bill/egress/execute and require their own explicit consent flow.
 */
export const SAFE_CAPABILITY_PROBES: Array<{ key: FeatureKey; label: string; note: string }> = [
  { key: 'text_streaming', label: 'Streaming text', note: '1 small request' },
  { key: 'system_instructions', label: 'System instructions', note: '1 small request' },
  { key: 'custom_tools', label: 'Custom tools', note: '2 small requests' },
  { key: 'images', label: 'Image input', note: '1 request' },
  { key: 'documents', label: 'Document input', note: '1 request' },
  { key: 'structured_output', label: 'Structured output', note: '1 request' },
];

const VERDICT_META: Record<string, {
  word: string;
  className: string;
  Icon: React.ComponentType<{ size?: number | string; className?: string }>;
}> = {
  verified: { word: 'Verified', className: 'text-emerald-400', Icon: Check },
  unsupported: { word: 'Unsupported', className: 'text-rose-400', Icon: Ban },
  failed: { word: 'Failed', className: 'text-destructive', Icon: X },
  skipped: { word: 'Skipped', className: 'text-muted-foreground', Icon: CircleDashed },
};

interface CapabilityCheckPanelProps {
  /** Saved connection id, when checking a persisted connection. */
  connectionId?: string | null;
  /** Draft config, when checking from the editor before save. */
  config?: ProviderConfig | null;
  model?: string | null;
  /** Called after a check completes with at least one conclusive verdict. */
  onConclusiveResult?: () => void;
}

/**
 * Opt-in capability checks (plan §4.2 step 4 / §4.7). A quick check proves
 * transport only; these probes verify specific features. Verdicts render
 * honestly: only "Verified" is green — skipped/unsupported never are.
 */
const CapabilityCheckPanel: React.FC<CapabilityCheckPanelProps> = ({
  connectionId, config, model, onConclusiveResult,
}) => {
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState<Set<FeatureKey>>(
    () => new Set(SAFE_CAPABILITY_PROBES.map((p) => p.key)),
  );
  const [running, setRunning] = useState(false);
  const [results, setResults] = useState<ProviderCheckProbeResult[] | null>(null);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [announcement, setAnnouncement] = useState('');

  const canRun = useMemo(
    () => selected.size > 0 && !running && (Boolean(connectionId) || Boolean(config)),
    [config, connectionId, running, selected.size],
  );

  const toggleProbe = (key: FeatureKey) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  };

  const run = async () => {
    if (!canRun) return;
    setRunning(true);
    setErrorMessage(null);
    setResults(null);
    setAnnouncement('Running capability checks…');
    try {
      const result = await runProviderCheck({
        ...(connectionId ? { connectionId } : {}),
        ...(config ? { config } : {}),
        ...(model?.trim() ? { model: model.trim() } : {}),
        tier: 'capability',
        probes: [...selected],
      });
      setResults(result.results);
      const conclusive = result.results.filter((r) => r.verdict === 'verified' || r.verdict === 'unsupported');
      setAnnouncement(`Capability checks finished: ${conclusive.length} conclusive of ${result.results.length}.`);
      if (conclusive.length > 0) onConclusiveResult?.();
    } catch (e: any) {
      setErrorMessage(e?.message || 'Capability check failed.');
      setAnnouncement('Capability checks failed.');
    } finally {
      setRunning(false);
    }
  };

  return (
    <div data-capability-check-panel className="space-y-2">
      <span className="sr-only" role="status" aria-live="polite">{announcement}</span>
      <button
        type="button"
        className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors rounded-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        <ChevronRight
          size={14}
          aria-hidden
          className="transition-transform motion-reduce:transition-none"
          style={{ transform: open ? 'rotate(90deg)' : undefined }}
        />
        <FlaskConical size={12} aria-hidden />
        Run capability checks
      </button>

      {open && (
        <div className="rounded-lg border border-border bg-card/30 p-3 space-y-2.5">
          <p className="text-[11px] text-muted-foreground">
            A quick check proves connectivity only. These optional checks verify specific
            features against this exact connection and model.
          </p>
          <div className="space-y-1.5">
            {SAFE_CAPABILITY_PROBES.map((probe) => (
              <label key={probe.key} className="flex items-center gap-2 cursor-pointer min-h-6">
                <Checkbox
                  checked={selected.has(probe.key)}
                  disabled={running}
                  onCheckedChange={() => toggleProbe(probe.key)}
                  aria-label={`${probe.label} probe`}
                />
                <span className="text-xs">{probe.label}</span>
                <span className="text-[10px] text-muted-foreground">{probe.note}</span>
              </label>
            ))}
          </div>
          <Button
            variant="outline"
            size="sm"
            className="h-7 gap-1.5 px-2.5 text-[11px]"
            disabled={!canRun}
            onClick={() => void run()}
          >
            <FlaskConical size={11} aria-hidden />
            {running ? 'Running checks…' : `Run ${selected.size} ${selected.size === 1 ? 'check' : 'checks'}`}
          </Button>

          {errorMessage && (
            <p className="text-[11px] text-destructive">{errorMessage}</p>
          )}

          {results && (
            <ul data-capability-check-results className="space-y-1 pt-1 border-t border-border/60">
              {results.map((r, i) => {
                const meta = VERDICT_META[r.verdict] || VERDICT_META.skipped;
                const Icon = meta.Icon;
                const probeLabel = SAFE_CAPABILITY_PROBES.find((p) => p.key === r.probe)?.label
                  || (r.probe === 'transport' ? 'Transport' : String(r.probe).replace(/_/g, ' '));
                return (
                  <li
                    key={`${r.probe}-${i}`}
                    data-capability-check-result={`${r.probe}:${r.verdict}`}
                    className="flex items-start gap-1.5 text-[11px]"
                  >
                    <span className={`inline-flex items-center gap-1 font-medium shrink-0 ${meta.className}`}>
                      <Icon size={11} aria-hidden /> {meta.word}
                    </span>
                    <span className="min-w-0 text-muted-foreground">
                      <span className="text-foreground/80">{probeLabel}</span>
                      {r.message ? ` — ${r.message}` : ''}
                      {r.verdict === 'skipped' && r.reason ? ` (${r.reason.replace(/_/g, ' ')})` : ''}
                    </span>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}
    </div>
  );
};

export default CapabilityCheckPanel;
