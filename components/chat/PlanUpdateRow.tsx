import React from 'react';
import { ArrowLeftRight, FileWarning, Link2Off, TriangleAlert } from 'lucide-react';
import type { UICapabilityPlanItem } from '@/types';

/**
 * One runtime plan change (SSE `plan_update`), rendered as a thin system row —
 * never assistant prose. The concise kind label frames the server's own text,
 * which stays authoritative and is never contradicted by local copy.
 */
const CONTINUATION_META = { label: 'Continuation not carried over', Icon: Link2Off };

const KIND_META: Record<string, {
  label: string;
  Icon: React.ComponentType<{ size?: number | string; className?: string }>;
}> = {
  surface_switch: { label: 'API surface changed', Icon: ArrowLeftRight },
  fidelity_reduction: { label: '▽ Reduced fidelity', Icon: FileWarning },
  feature_unavailable: { label: 'Feature unavailable', Icon: TriangleAlert },
  // `continuation_loss` is the canonical backend code; `continuation_lost`
  // stays accepted for legacy emitters and forward compatibility.
  continuation_loss: CONTINUATION_META,
  continuation_lost: CONTINUATION_META,
};

/** Pure vocabulary lookup, exported for contract tests. Unknown codes fall back honestly. */
export function planUpdateKindLabel(code: string | null | undefined): string {
  return (code && KIND_META[code]?.label) || 'Plan update';
}

interface PlanUpdateRowProps {
  item: UICapabilityPlanItem;
}

const PlanUpdateRow: React.FC<PlanUpdateRowProps> = ({ item }) => {
  const meta = (item.code && KIND_META[item.code]) || { label: 'Plan update', Icon: TriangleAlert };
  const Icon = meta.Icon;
  return (
    <div
      data-plan-update-row={item.code || 'update'}
      className="flex items-start gap-1.5 text-[11px] text-muted-foreground"
    >
      <Icon size={12} className="mt-0.5 shrink-0 text-amber-500" aria-hidden />
      <span className="min-w-0">
        <span className="text-foreground/80">{meta.label}</span>
        {item.text ? <> — {item.text}</> : null}
      </span>
    </div>
  );
};

export default PlanUpdateRow;
