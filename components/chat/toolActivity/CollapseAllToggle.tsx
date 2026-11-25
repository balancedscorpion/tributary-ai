import React from 'react';
import { ListCollapse } from 'lucide-react';
import { useCollapseAllToolActivity } from '@/lib/toolActivityPrefs';

/**
 * Thread-level "Collapse all tool activity" toggle — the single affordance for
 * the persisted per-thread preference (lib/toolActivityPrefs.ts). Lives in the
 * thread header, not inside individual tool groups, so it is discoverable
 * without first expanding a group. Turning it on collapses every mounted
 * group now (useToolActivityDisclosure reacts to the rising edge) and makes
 * future completed runs minimize immediately; live pinned groups and pending
 * approvals stay open.
 */
const LABEL = 'Collapse all tool activity';

const CollapseAllToggle: React.FC<{ showLabel?: boolean }> = ({ showLabel = true }) => {
  const { threadId, collapseAll, setCollapseAll } = useCollapseAllToolActivity();
  if (!threadId) return null;
  return (
    <button
      type="button"
      onClick={() => setCollapseAll(!collapseAll)}
      aria-pressed={collapseAll}
      // The visible text is the accessible name on the labelled variant;
      // aria-label/title only backfill the icon-only variant (identical
      // name + title-description makes some screen readers announce twice).
      aria-label={showLabel ? undefined : LABEL}
      title={showLabel ? undefined : LABEL}
      className={`flex h-7 shrink-0 items-center gap-1.5 rounded-md text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring ${
        showLabel ? 'px-2' : 'w-7 justify-center'
      } ${
        collapseAll
          ? 'bg-muted text-foreground'
          : 'text-muted-foreground hover:bg-muted/60 hover:text-foreground'
      }`}
    >
      <ListCollapse size={14} aria-hidden />
      {showLabel && <span className="whitespace-nowrap">{LABEL}</span>}
    </button>
  );
};

export default CollapseAllToggle;
