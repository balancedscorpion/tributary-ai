import React from 'react';
import { Layers } from 'lucide-react';
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from '@/components/ui/accordion';
import type { CompactionStats } from '@/types';
import MarkdownRenderer from './MarkdownRenderer';

interface CompactionBlockProps {
  summary: string;
  isComplete: boolean;
  stats?: CompactionStats;
  jiraBaseUrl?: string;
}

function formatTokenCount(value: number | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return '-';
  const rounded = Math.round(value);
  if (rounded >= 1_000_000) {
    return `${(rounded / 1_000_000).toFixed(1).replace(/\.0$/, '')}M`;
  }
  if (rounded >= 1_000) {
    return `${(rounded / 1_000).toFixed(1).replace(/\.0$/, '')}k`;
  }
  return rounded.toLocaleString();
}

function formatTokenDelta(stats: CompactionStats | undefined): string {
  const before = stats?.tokensBefore;
  const after = stats?.tokensAfter;
  if (
    typeof before !== 'number' ||
    !Number.isFinite(before) ||
    before <= 0 ||
    typeof after !== 'number' ||
    !Number.isFinite(after) ||
    after < 0
  ) {
    return '-';
  }
  const saved = Math.max(0, before - after);
  const percent = Math.round((saved / before) * 100);
  return `${formatTokenCount(saved)} (${percent}%)`;
}

function hasCompactionStats(stats: CompactionStats | undefined): boolean {
  return Boolean(
    stats &&
      (typeof stats.tokensBefore === 'number' ||
        typeof stats.tokensAfter === 'number' ||
        typeof stats.tokenBudget === 'number' ||
        typeof stats.compacted === 'boolean'),
  );
}

const CompactionBlock: React.FC<CompactionBlockProps> = ({ summary, isComplete, stats, jiraBaseUrl }) => {
  const showStats = hasCompactionStats(stats);
  const status = String(stats?.status || '').trim().toLowerCase();
  const title = status === 'failed'
    ? 'Context compaction failed'
    : isComplete
      ? 'Context compacted'
      : 'Compacting context...';

  return (
    <Accordion type="single" collapsible defaultValue={isComplete ? undefined : "compaction"} className="my-2 min-w-0 max-w-full">
      <AccordionItem
        value="compaction"
        className={`border rounded-lg overflow-hidden min-w-0 max-w-full ${
          isComplete ? 'border-blue-500/20' : 'border-blue-500/40 animate-compaction-pulse'
        }`}
      >
        <AccordionTrigger className="px-3 py-2 text-xs hover:no-underline hover:bg-muted/50">
          <div className="flex min-w-0 items-center gap-2">
            <Layers size={14} className={`text-blue-500 ${!isComplete ? 'animate-pulse' : ''}`} />
            <span className="text-blue-400/80 font-medium">
              {title}
            </span>
          </div>
        </AccordionTrigger>
        <AccordionContent className="px-3 pb-3 space-y-3">
          {showStats && (
            <div className="grid grid-cols-2 gap-2 text-xs sm:grid-cols-4">
              <div className="min-w-0 rounded-md border border-border/60 bg-muted/20 px-2.5 py-2">
                <div className="text-[10px] uppercase tracking-normal text-muted-foreground">Before</div>
                <div className="mt-1 font-medium text-foreground">{formatTokenCount(stats?.tokensBefore)}</div>
              </div>
              <div className="min-w-0 rounded-md border border-border/60 bg-muted/20 px-2.5 py-2">
                <div className="text-[10px] uppercase tracking-normal text-muted-foreground">After</div>
                <div className="mt-1 font-medium text-foreground">{formatTokenCount(stats?.tokensAfter)}</div>
              </div>
              <div className="min-w-0 rounded-md border border-border/60 bg-muted/20 px-2.5 py-2">
                <div className="text-[10px] uppercase tracking-normal text-muted-foreground">Saved</div>
                <div className="mt-1 font-medium text-foreground">{formatTokenDelta(stats)}</div>
              </div>
              <div className="min-w-0 rounded-md border border-border/60 bg-muted/20 px-2.5 py-2">
                <div className="text-[10px] uppercase tracking-normal text-muted-foreground">Budget</div>
                <div className="mt-1 font-medium text-foreground">{formatTokenCount(stats?.tokenBudget)}</div>
              </div>
            </div>
          )}
          {summary ? (
            <div className="text-xs text-muted-foreground">
              <MarkdownRenderer content={summary} jiraBaseUrl={jiraBaseUrl} />
            </div>
          ) : isComplete ? (
            <p className="text-xs text-muted-foreground italic">
              No compaction summary was emitted.
            </p>
          ) : (
            <p className="text-xs text-muted-foreground italic animate-pulse">
              Generating summary...
            </p>
          )}
        </AccordionContent>
      </AccordionItem>
    </Accordion>
  );
};

export default CompactionBlock;
