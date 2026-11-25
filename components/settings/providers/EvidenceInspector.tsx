import React, { useMemo, useState } from 'react';
import { BookOpen, ClipboardCopy, Download, FileQuestion, FlaskConical, History } from 'lucide-react';
import { toast } from 'sonner';
import { Button } from '@/components/ui/button';
import { formatCapabilityTimestamp } from '@/lib/capability/format';
import type { FeatureResolution } from './capabilityContract';
import { CAPABILITY_STATE_META, capabilityViewState, modeModifierText } from './CapabilityChip';

const EVIDENCE_KIND_META: Record<string, { label: string; Icon: React.ComponentType<{ size?: number | string; className?: string }> }> = {
  catalog: { label: 'Curated catalog', Icon: BookOpen },
  probe: { label: 'Capability check', Icon: FlaskConical },
  discovery: { label: 'Model discovery', Icon: Download },
  docs: { label: 'Provider documentation', Icon: BookOpen },
  imported: { label: 'Imported from legacy settings', Icon: History },
};

function formatVerifiedAt(iso: string | null): string {
  if (!iso) return 'no timestamp';
  const { relative, absolute } = formatCapabilityTimestamp(iso);
  if (relative === 'Unknown time') return iso;
  return `${relative} · ${absolute}`;
}

/** One-sentence verdict combining state, conditions, and mode (plan §4.3 zone 1). */
export function verdictSentence(label: string, res: FeatureResolution): string {
  const view = capabilityViewState(res);
  const word = CAPABILITY_STATE_META[view].word;
  const bits: string[] = [`${label}: ${word}`];
  const mod = modeModifierText(res.mode);
  if (mod) bits.push(mod);
  if (res.conditions.length > 0) bits.push(res.conditions.map((c) => c.text).join('; '));
  if (view === 'not_available' || view === 'blocked') {
    const reason = res.reasons[0]?.text;
    if (reason) bits.push(reason);
  }
  if (view === 'unverified') bits.push('No evidence for this target yet.');
  return bits.join(' — ');
}

interface EvidenceInspectorProps {
  featureLabel: string;
  resolution: FeatureResolution;
  /** DOM id so the row toggle can reference it via aria-controls. */
  id: string;
}

/**
 * Inline three-zone evidence drawer (plan §4.3): verdict sentence, evidence entries
 * with provenance and staleness, raw resolver JSON. Rendered inside the matrix row's
 * collapsible region — intentionally not a nested dialog, so keyboard focus stays in
 * the settings dialog's natural order.
 */
const EvidenceInspector: React.FC<EvidenceInspectorProps> = ({ featureLabel, resolution, id }) => {
  const [rawOpen, setRawOpen] = useState(false);
  const rawJson = useMemo(() => JSON.stringify(resolution, null, 2), [resolution]);

  const copyRaw = async () => {
    try {
      await navigator.clipboard.writeText(rawJson);
      toast.success('Resolver JSON copied');
    } catch {
      toast.error('Could not copy to clipboard');
    }
  };

  return (
    <div
      id={id}
      data-capability-evidence
      className="rounded-md border border-border bg-muted/20 px-3 py-2.5 space-y-2.5 text-left"
    >
      <p className="text-xs text-foreground/90">{verdictSentence(featureLabel, resolution)}</p>

      <div className="space-y-1.5">
        <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">Evidence</div>
        {resolution.evidence.length === 0 ? (
          <p className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
            <FileQuestion size={12} aria-hidden />
            No evidence recorded — this feature has not been checked or catalogued for this exact target.
          </p>
        ) : (
          <ul className="space-y-1">
            {resolution.evidence.map((ev, i) => {
              const meta = EVIDENCE_KIND_META[ev.kind] || { label: ev.kind, Icon: BookOpen };
              const Icon = meta.Icon;
              return (
                <li key={i} className="flex items-start gap-1.5 text-[11px] text-muted-foreground">
                  <Icon size={12} className="mt-0.5 shrink-0" aria-hidden />
                  <span className="min-w-0">
                    <span className="text-foreground/80">{meta.label}</span>
                    {' · '}
                    <span title={ev.verifiedAt || undefined}>{formatVerifiedAt(ev.verifiedAt)}</span>
                    {ev.ref ? (
                      /^https?:\/\//.test(ev.ref) ? (
                        <>
                          {' · '}
                          <a
                            href={ev.ref}
                            target="_blank"
                            rel="noreferrer"
                            className="underline underline-offset-2 hover:text-foreground"
                          >
                            source
                          </a>
                        </>
                      ) : (
                        <span className="font-mono break-all"> · {ev.ref}</span>
                      )
                    ) : null}
                    {ev.stale && (
                      <span className="ml-1 text-amber-500">
                        stale{ev.staleReason === 'fingerprint' ? ' — config changed since this check' : ev.staleReason === 'age' ? ' — check is old' : ''}
                      </span>
                    )}
                  </span>
                </li>
              );
            })}
          </ul>
        )}
      </div>

      <div>
        <button
          type="button"
          className="text-[11px] text-muted-foreground underline underline-offset-2 hover:text-foreground rounded-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
          aria-expanded={rawOpen}
          onClick={() => setRawOpen((v) => !v)}
        >
          {rawOpen ? 'Hide raw resolver output' : 'Show raw resolver output'}
        </button>
        {rawOpen && (
          <div className="mt-1.5 space-y-1.5">
            <pre className="max-h-48 overflow-auto rounded-md border border-border bg-background/70 p-2 font-mono text-[10px] leading-relaxed">
              {rawJson}
            </pre>
            <Button variant="outline" size="sm" className="h-6 gap-1 px-2 text-[10px]" onClick={() => void copyRaw()}>
              <ClipboardCopy size={10} aria-hidden /> Copy JSON
            </Button>
          </div>
        )}
      </div>
    </div>
  );
};

export default EvidenceInspector;
