import React from 'react';
import { MessageSquareQuote } from 'lucide-react';
import { CopyIcon } from './atoms';
import { SqlBlock } from './sql';

/**
 * QueryRequestZone — the "Query" zone shared across running / completed /
 * error / cancelled inspector states. Agent description headline above,
 * syntax-highlighted SQL block below. The SQL block is always copyable; in
 * error mode the highlighted line is the offending row.
 *
 * If neither a description nor SQL is available, falls back to a muted
 * "No query text available." panel — the zone is always present so the
 * inspector's three-zone rhythm stays consistent.
 */
export function QueryRequestZone({
  description,
  sql,
  highlightLine,
}: {
  description: string;
  sql: string;
  highlightLine?: number | null;
}) {
  return (
    <div>
      <div className="mb-1.5 text-xs text-muted-foreground/60">Query</div>
      {description && (
        <div className="mb-2 flex items-start gap-2">
          <MessageSquareQuote
            size={14}
            className="mt-0.5 shrink-0 text-muted-foreground/60"
            aria-hidden
          />
          <p className="text-[13px] leading-[1.55] text-foreground/90 [overflow-wrap:anywhere]">
            {description}
          </p>
        </div>
      )}
      {sql && (
        <div className="relative">
          <div className="absolute right-2 top-2 z-10">
            <CopyIcon value={sql} label="Copy SQL" />
          </div>
          <SqlBlock sql={sql} highlightLine={highlightLine ?? undefined} />
        </div>
      )}
      {!description && !sql && (
        <div className="rounded-md border border-border bg-muted/40 px-3 py-2.5 text-[12px] text-muted-foreground">
          No query text available.
        </div>
      )}
    </div>
  );
}
