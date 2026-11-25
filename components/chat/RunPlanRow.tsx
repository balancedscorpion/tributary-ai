import React, { useState } from 'react';
import { Ban, ChevronRight, ClipboardList, Info, TriangleAlert } from 'lucide-react';
import { getRunCapabilityPlan } from '@/services/capabilityApi';
import type { RunPlanRecord } from '@/lib/capability/types';
import type { UICapabilityPlanSummary } from '@/types';
import PlanUpdateRow from './PlanUpdateRow';

function summaryLine(plan: UICapabilityPlanSummary): string {
  const parts: string[] = [];
  if (plan.targetLabel) parts.push(plan.targetLabel);
  const counts: string[] = [];
  const notices = plan.items.filter((i) => i.kind === 'notice').length;
  const choices = plan.items.filter((i) => i.kind === 'choice').length;
  const updates = plan.items.filter((i) => i.kind === 'update').length;
  if (choices > 0) counts.push(`${choices} ${choices === 1 ? 'choice' : 'choices'}`);
  if (notices > 0) counts.push(`${notices} ${notices === 1 ? 'notice' : 'notices'}`);
  if (updates > 0) counts.push(`${updates} ${updates === 1 ? 'change' : 'changes'}`);
  if (plan.targetChanged) counts.push('model changed');
  if (counts.length > 0) parts.push(counts.join(' · '));
  return parts.join(' — ') || 'Run plan';
}

interface RunPlanRowProps {
  plan: UICapabilityPlanSummary;
}

/**
 * Thread-differential run-plan row (plan §4.6): one quiet, expandable line per
 * run that deviated from the previous plan. Expanding a run-backed row fetches
 * the finalized plan on demand; absence (older backends, unfinalized runs) is
 * tolerated gracefully.
 */
const RunPlanRow: React.FC<RunPlanRowProps> = ({ plan }) => {
  const [open, setOpen] = useState(false);
  const [finalized, setFinalized] = useState<RunPlanRecord | null>(null);
  const [finalizedState, setFinalizedState] = useState<'idle' | 'loading' | 'ready' | 'unavailable'>('idle');

  const toggle = () => {
    const next = !open;
    setOpen(next);
    if (next && plan.runId && finalizedState === 'idle') {
      setFinalizedState('loading');
      getRunCapabilityPlan(plan.runId)
        .then((record) => {
          setFinalized(record);
          setFinalizedState('ready');
        })
        .catch(() => setFinalizedState('unavailable'));
    }
  };

  return (
    <div data-run-plan-row={plan.runId || plan.planId || 'pending'} className="my-1">
      <button
        type="button"
        aria-expanded={open}
        className="flex w-full items-center gap-1.5 rounded-md px-2 py-1 min-h-7 text-left text-[11px] text-muted-foreground hover:bg-card/60 hover:text-foreground transition-colors focus-visible:outline focus-visible:outline-2 focus-visible:outline-ring"
        onClick={toggle}
      >
        <ChevronRight
          size={12}
          aria-hidden
          className="shrink-0 transition-transform motion-reduce:transition-none"
          style={{ transform: open ? 'rotate(90deg)' : undefined }}
        />
        <ClipboardList size={12} className="shrink-0" aria-hidden />
        <span className="min-w-0 truncate">Plan · {summaryLine(plan)}</span>
      </button>

      {open && (
        <div className="ml-6 mt-1 space-y-1.5 rounded-md border border-border bg-card/40 p-2.5">
          {plan.items.length === 0 && !plan.targetChanged && (
            <p className="text-[11px] text-muted-foreground">No deviations — this run followed the resolved plan.</p>
          )}
          {plan.targetChanged && plan.targetLabel && (
            <div className="flex items-start gap-1.5 text-[11px] text-muted-foreground">
              <Info size={12} className="mt-0.5 shrink-0" aria-hidden />
              <span>This run used a different model: <span className="text-foreground/80">{plan.targetLabel}</span>.</span>
            </div>
          )}
          {plan.items.map((item, i) => {
            if (item.kind === 'update') return <PlanUpdateRow key={i} item={item} />;
            const Icon = item.kind === 'blocker' ? Ban : item.kind === 'choice' ? TriangleAlert : Info;
            const tone = item.kind === 'blocker' ? 'text-destructive' : item.kind === 'choice' ? 'text-amber-500' : 'text-muted-foreground';
            return (
              <div key={i} className="flex items-start gap-1.5 text-[11px] text-muted-foreground">
                <Icon size={12} className={`mt-0.5 shrink-0 ${tone}`} aria-hidden />
                <span className="min-w-0">{item.text}</span>
              </div>
            );
          })}

          {finalizedState === 'loading' && (
            <p className="text-[10px] text-muted-foreground/70">Loading finalized plan…</p>
          )}
          {finalizedState === 'unavailable' && plan.runId && (
            <p className="text-[10px] text-muted-foreground/70">
              The finalized plan isn’t available for this run.
            </p>
          )}
          {finalizedState === 'ready' && finalized && (
            <div className="border-t border-border/60 pt-1.5 space-y-1">
              <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                Finalized plan
              </div>
              <p className="text-[10px] font-mono text-muted-foreground/80 break-all">
                plan {finalized.planId}
                {finalized.plan?.target?.apiSurface ? ` · ${finalized.plan.target.apiSurface}` : ''}
                {finalized.plan?.target?.model || finalized.target?.model
                  ? ` · ${finalized.plan?.target?.model || finalized.target?.model}`
                  : ''}
              </p>
              {(finalized.choiceSelections || []).length > 0 && (
                <ul className="space-y-0.5">
                  {(finalized.choiceSelections || []).map((sel) => {
                    const issue = (finalized.choices || []).find((c) => c.issueId === sel.issueId);
                    const option = issue?.options?.find((o) => o.id === sel.optionId);
                    return issue ? (
                      <li key={sel.issueId} className="text-[10px] text-muted-foreground">
                        {issue.text} → {option?.label || sel.optionId} ({sel.scope})
                      </li>
                    ) : (
                      <li key={sel.issueId} className="text-[10px] text-muted-foreground font-mono">
                        {sel.issueId} → {sel.optionId} ({sel.scope})
                      </li>
                    );
                  })}
                </ul>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
};

export default RunPlanRow;
