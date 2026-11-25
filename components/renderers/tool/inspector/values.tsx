import React from 'react';
import { Check, ExternalLink, X } from 'lucide-react';
import { cn } from '@/lib/utils';
import { Disclosure } from '../query/atoms';

/**
 * values.tsx — the structured rendering engine shared by ToolInspector.
 *
 * Turns an arbitrary JSON tool result into scannable UI instead of a raw blob:
 *   array-of-objects → AutoTable
 *   array-of-scalars → chip list
 *   object           → KeyValueGrid
 *   scalar/string    → typed cell (link / timestamp / boolean / number / text)
 *
 * It is deliberately defensive — connector read tools return opaque,
 * backend-shaped maps, so this never assumes a schema and never throws on an
 * unexpected shape. Depth is capped so deeply nested values collapse into a
 * Disclosure rather than nesting cards-in-cards or overflowing narrow panels.
 */

const MAX_DEPTH = 2;
const MAX_TABLE_ROWS = 50;
const MAX_TABLE_COLS = 12;
const MAX_CHIPS = 60;

export function isPlainObject(v: unknown): v is Record<string, any> {
  return v !== null && typeof v === 'object' && !Array.isArray(v);
}

function isArrayOfObjects(v: unknown): v is Record<string, any>[] {
  return (
    Array.isArray(v) &&
    v.length > 0 &&
    v.filter((x) => isPlainObject(x)).length >= Math.ceil(v.length / 2)
  );
}

const ISO_RE = /^\d{4}-\d{2}-\d{2}([T ]\d{2}:\d{2}(:\d{2})?(\.\d+)?(Z|[+-]\d{2}:?\d{2})?)?$/;
const URL_RE = /^https?:\/\/\S+$/i;
const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
// Slack/Unix-ish epoch seconds as string, e.g. "1717000000.001200".
const EPOCH_RE = /^\d{9,10}(\.\d+)?$/;

const numberFmt = new Intl.NumberFormat();

export function formatTimestamp(raw: string): string | null {
  let d: Date | null = null;
  if (ISO_RE.test(raw)) d = new Date(raw);
  else if (EPOCH_RE.test(raw)) d = new Date(Math.floor(parseFloat(raw) * 1000));
  if (!d || Number.isNaN(d.getTime())) return null;
  try {
    return d.toLocaleString(undefined, {
      year: 'numeric',
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit',
    });
  } catch {
    return d.toISOString();
  }
}

/** Compact one-line JSON used for nested cells and the raw cell fallback. */
function compactJson(value: unknown, max = 80): string {
  let s: string;
  try {
    s = JSON.stringify(value);
  } catch {
    s = String(value);
  }
  if (s == null) return '';
  return s.length > max ? `${s.slice(0, max - 1)}…` : s;
}

function BooleanPill({ value }: { value: boolean }) {
  return value ? (
    <span className="inline-flex items-center gap-1 rounded bg-emerald-500/10 px-1.5 py-0.5 text-[11px] font-medium text-emerald-600">
      <Check size={10} aria-hidden /> true
    </span>
  ) : (
    <span className="inline-flex items-center gap-1 rounded bg-muted px-1.5 py-0.5 text-[11px] font-medium text-muted-foreground">
      <X size={10} aria-hidden /> false
    </span>
  );
}

/** A single inline value — typed and link/timestamp aware. */
export function ValueCell({ value }: { value: unknown }) {
  if (value === null || value === undefined) {
    return <span className="text-muted-foreground/60">—</span>;
  }
  if (typeof value === 'boolean') return <BooleanPill value={value} />;
  if (typeof value === 'number') {
    return <span className="font-mono tabular-nums">{numberFmt.format(value)}</span>;
  }
  if (typeof value === 'string') {
    const s = value.trim();
    if (!s) return <span className="text-muted-foreground/60">""</span>;
    if (URL_RE.test(s)) {
      return (
        <a
          href={s}
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 rounded text-primary hover:underline [overflow-wrap:anywhere] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
        >
          {s}
          <ExternalLink size={11} aria-hidden className="shrink-0" />
        </a>
      );
    }
    if (EMAIL_RE.test(s)) {
      return <span className="[overflow-wrap:anywhere]">{s}</span>;
    }
    const ts = formatTimestamp(s);
    if (ts) {
      return (
        <span title={s} className="text-foreground">
          {ts}
        </span>
      );
    }
    return <span className="[overflow-wrap:anywhere] whitespace-pre-wrap">{s}</span>;
  }
  // object/array fallback (used inside table cells)
  return <span className="font-mono text-[11px] text-muted-foreground">{compactJson(value)}</span>;
}

function ChipList({ values }: { values: unknown[] }) {
  const shown = values.slice(0, MAX_CHIPS);
  const extra = values.length - shown.length;
  return (
    <div className="flex flex-wrap gap-1">
      {shown.map((v, i) => (
        <span
          key={i}
          className="rounded bg-muted px-1.5 py-0.5 font-mono text-[11px] text-foreground [overflow-wrap:anywhere]"
        >
          {typeof v === 'object' && v !== null ? compactJson(v, 48) : String(v)}
        </span>
      ))}
      {extra > 0 && (
        <span className="px-1 py-0.5 text-[11px] text-muted-foreground">+{extra} more</span>
      )}
    </div>
  );
}

function cellAlignRight(value: unknown): boolean {
  return typeof value === 'number';
}

export function AutoTable({ rows }: { rows: Record<string, any>[] }) {
  const cols: string[] = [];
  const seen = new Set<string>();
  for (const row of rows.slice(0, MAX_TABLE_ROWS)) {
    if (!isPlainObject(row)) continue;
    for (const k of Object.keys(row)) {
      if (!seen.has(k)) {
        seen.add(k);
        cols.push(k);
      }
    }
  }
  const shownCols = cols.slice(0, MAX_TABLE_COLS);
  const extraCols = cols.length - shownCols.length;
  const shownRows = rows.slice(0, MAX_TABLE_ROWS);
  const extraRows = rows.length - shownRows.length;

  return (
    <div className="flex flex-col gap-1">
      <div className="overflow-x-auto rounded-md border border-border">
        <table className="w-full border-collapse text-[12px]">
          <thead>
            <tr className="border-b border-border bg-muted/40">
              {shownCols.map((c) => (
                <th
                  key={c}
                  scope="col"
                  className="px-2.5 py-1.5 text-left font-medium text-muted-foreground whitespace-nowrap"
                >
                  {c}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {shownRows.map((row, i) => (
              <tr key={i} className="border-b border-border/50 last:border-b-0 hover:bg-muted/30">
                {shownCols.map((c) => {
                  const v = isPlainObject(row) ? row[c] : undefined;
                  return (
                    <td
                      key={c}
                      className={cn(
                        'max-w-[280px] px-2.5 py-1.5 align-top [overflow-wrap:anywhere]',
                        cellAlignRight(v) && 'text-right tabular-nums',
                      )}
                      title={typeof v === 'string' ? v : compactJson(v, 240)}
                    >
                      <ValueCell value={v} />
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <div className="flex items-center gap-2 text-[11px] text-muted-foreground/70">
        <span>
          {rows.length} {rows.length === 1 ? 'row' : 'rows'}
        </span>
        {extraRows > 0 && <span>· showing first {MAX_TABLE_ROWS}</span>}
        {extraCols > 0 && <span>· +{extraCols} more columns (see Raw)</span>}
      </div>
    </div>
  );
}

export function KeyValueGrid({
  data,
  depth = 0,
}: {
  data: Record<string, any>;
  depth?: number;
}) {
  const entries = Object.entries(data);
  if (entries.length === 0) {
    return <span className="text-[12px] text-muted-foreground/60">Empty object</span>;
  }
  return (
    <div className="grid grid-cols-[minmax(0,9rem)_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-[12px]">
      {entries.map(([k, v]) => (
        <React.Fragment key={k}>
          <div className="truncate font-mono text-muted-foreground" title={k}>
            {k}
          </div>
          <div className="min-w-0">
            <NestedValue value={v} depth={depth} />
          </div>
        </React.Fragment>
      ))}
    </div>
  );
}

/** Value rendering inside a KeyValueGrid cell — recurses with a depth cap. */
function NestedValue({ value, depth }: { value: unknown; depth: number }) {
  if (Array.isArray(value)) {
    if (value.length === 0) return <span className="text-muted-foreground/60">Empty list</span>;
    if (isArrayOfObjects(value)) {
      if (depth >= MAX_DEPTH) {
        return (
          <Disclosure label={`${value.length} rows`}>
            <AutoTable rows={value as Record<string, any>[]} />
          </Disclosure>
        );
      }
      return <AutoTable rows={value as Record<string, any>[]} />;
    }
    return <ChipList values={value} />;
  }
  if (isPlainObject(value)) {
    if (Object.keys(value).length === 0)
      return <span className="text-muted-foreground/60">Empty object</span>;
    if (depth >= MAX_DEPTH) {
      return (
        <Disclosure label="object">
          <pre className="mt-1 overflow-x-auto rounded border border-border bg-muted/30 p-2 font-mono text-[11px] text-foreground">
            {safeJson(value)}
          </pre>
        </Disclosure>
      );
    }
    return (
      <div className="rounded-md border border-border/60 bg-muted/20 p-2">
        <KeyValueGrid data={value} depth={depth + 1} />
      </div>
    );
  }
  return <ValueCell value={value} />;
}

export function safeJson(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

/**
 * ValueBlock — top-level dispatcher for a tool result payload.
 */
export function ValueBlock({ value, depth = 0 }: { value: unknown; depth?: number }) {
  if (value === null || value === undefined) {
    return <span className="text-[12px] text-muted-foreground/60">No content</span>;
  }
  if (typeof value === 'string') {
    const s = value.trim();
    if (!s) return <span className="text-[12px] text-muted-foreground/60">Empty</span>;
    return (
      <div className="whitespace-pre-wrap [overflow-wrap:anywhere] rounded-md border border-border bg-muted/30 px-3 py-2.5 text-[13px] text-foreground">
        {s}
      </div>
    );
  }
  if (typeof value === 'number' || typeof value === 'boolean') {
    return (
      <div className="text-[13px]">
        <ValueCell value={value} />
      </div>
    );
  }
  if (Array.isArray(value)) {
    if (value.length === 0)
      return <span className="text-[12px] text-muted-foreground/60">Empty list</span>;
    if (isArrayOfObjects(value)) return <AutoTable rows={value as Record<string, any>[]} />;
    return <ChipList values={value} />;
  }
  if (isPlainObject(value)) {
    if (Object.keys(value).length === 0)
      return <span className="text-[12px] text-muted-foreground/60">Empty object</span>;
    return <KeyValueGrid data={value} depth={depth} />;
  }
  return <span className="font-mono text-[12px]">{compactJson(value)}</span>;
}
