import React, { useEffect, useMemo, useRef, useState } from 'react';
import {
  ArrowDown,
  ArrowUp,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Columns3,
  Loader2,
  RefreshCw,
  Search,
  X,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import type { Row } from './useQueryExplorer';

export interface SchemaField {
  name: string;
  type?: string;
  mode?: string;
  description?: string;
}

interface ColumnInfo {
  key: string;
  type: string | null;        // raw type string e.g. INT64 / TIMESTAMP / STRING
  numeric: boolean;           // for right-align + numeric sort
  description: string | null;
}

interface ExplorerProps {
  rows: Row[];
  schema?: SchemaField[];
  totalRows: number | null;
  isLive: boolean;
  hasMore: boolean;
  loading: boolean;
  error: Error | null;
  onLoadMore: () => void;
  onRefresh: () => void;
  /** Optional connector label for the "Live · BigQuery" indicator. */
  providerLabel?: string | null;
}

const PAGE_SIZES = [10, 25, 50, 100];

const NUMERIC_TYPE_PATTERN = /^(int|integer|bigint|smallint|tinyint|long|short|byte|float|double|real|numeric|decimal|number|int64|float64|currency)/i;

function inferTypeFromSample(samples: unknown[]): string | null {
  for (const s of samples) {
    if (s === null || s === undefined) continue;
    if (typeof s === 'number') return 'NUMBER';
    if (typeof s === 'boolean') return 'BOOLEAN';
    if (typeof s === 'string') {
      // Try to detect a numeric-looking string and a timestamp-looking string.
      if (/^-?\d+(\.\d+)?$/.test(s)) return 'NUMBER';
      if (/^\d{4}-\d{2}-\d{2}([T ]\d{2}:\d{2})?/.test(s)) return 'TIMESTAMP';
      return 'STRING';
    }
    return 'OBJECT';
  }
  return null;
}

function buildColumns(rows: Row[], schema?: SchemaField[]): ColumnInfo[] {
  const seen = new Set<string>();
  const out: ColumnInfo[] = [];
  const schemaByName = new Map<string, SchemaField>();
  if (schema) schema.forEach((s) => schemaByName.set(s.name, s));

  const orderSource = schema && schema.length > 0 ? schema.map((s) => s.name) : [];
  // Always include keys that exist in rows but not in schema (graceful upgrade).
  for (const r of rows.slice(0, 50)) {
    for (const k of Object.keys(r)) {
      if (!orderSource.includes(k)) orderSource.push(k);
    }
  }

  for (const key of orderSource) {
    if (seen.has(key)) continue;
    seen.add(key);
    const sField = schemaByName.get(key);
    const rawType = sField?.type ? String(sField.type).toUpperCase() : null;
    const sample = rows.slice(0, 12).map((r) => r?.[key]);
    const inferred = rawType ?? inferTypeFromSample(sample);
    const numeric =
      (rawType && NUMERIC_TYPE_PATTERN.test(rawType)) ||
      (!rawType && inferred === 'NUMBER');
    out.push({
      key,
      type: inferred,
      numeric: !!numeric,
      description: sField?.description ?? null,
    });
  }
  return out;
}

function compareCells(a: unknown, b: unknown, numeric: boolean): number {
  const aNull = a === null || a === undefined || a === '';
  const bNull = b === null || b === undefined || b === '';
  if (aNull && bNull) return 0;
  if (aNull) return 1;
  if (bNull) return -1;
  if (numeric) {
    const an = typeof a === 'number' ? a : Number(a);
    const bn = typeof b === 'number' ? b : Number(b);
    if (Number.isNaN(an) || Number.isNaN(bn)) {
      return String(a).localeCompare(String(b));
    }
    return an - bn;
  }
  return String(a).localeCompare(String(b), undefined, { numeric: true, sensitivity: 'base' });
}

function formatCell(value: unknown, numeric: boolean): string {
  if (value === null || value === undefined) return '';
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) return String(value);
    return numeric ? value.toLocaleString(undefined, { maximumFractionDigits: 6 }) : String(value);
  }
  if (typeof value === 'boolean') return value ? 'true' : 'false';
  if (typeof value === 'string') return value;
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

function typeLabel(type: string | null): string {
  if (!type) return '';
  return type.toUpperCase();
}

export function Explorer({
  rows,
  schema,
  totalRows,
  isLive,
  hasMore,
  loading,
  error,
  onLoadMore,
  onRefresh,
  providerLabel,
}: ExplorerProps) {
  const columns = useMemo(() => buildColumns(rows, schema), [rows, schema]);

  // Column order + visibility state. Initialise from columns; resync on new columns.
  const [columnOrder, setColumnOrder] = useState<string[]>(() => columns.map((c) => c.key));
  const [hidden, setHidden] = useState<Set<string>>(new Set());
  useEffect(() => {
    setColumnOrder((prev) => {
      const present = new Set(columns.map((c) => c.key));
      const kept = prev.filter((k) => present.has(k));
      const added = columns.map((c) => c.key).filter((k) => !kept.includes(k));
      return [...kept, ...added];
    });
    setHidden((prev) => {
      const next = new Set<string>();
      const present = new Set(columns.map((c) => c.key));
      prev.forEach((k) => {
        if (present.has(k)) next.add(k);
      });
      return next;
    });
  }, [columns]);

  const columnMap = useMemo(() => {
    const m = new Map<string, ColumnInfo>();
    columns.forEach((c) => m.set(c.key, c));
    return m;
  }, [columns]);

  const orderedVisibleColumns = useMemo(
    () => columnOrder.map((k) => columnMap.get(k)!).filter((c) => c && !hidden.has(c.key)),
    [columnOrder, columnMap, hidden],
  );

  const [search, setSearch] = useState('');
  const [sort, setSort] = useState<{ key: string; dir: 'asc' | 'desc' } | null>(null);
  const [pageIndex, setPageIndex] = useState(0);
  const [pageSize, setPageSize] = useState<number>(PAGE_SIZES[1]);
  const [showColumnsPanel, setShowColumnsPanel] = useState(false);
  const columnsPanelRef = useRef<HTMLDivElement | null>(null);

  // Close the columns panel on outside click.
  useEffect(() => {
    if (!showColumnsPanel) return;
    const onDown = (e: MouseEvent) => {
      if (!columnsPanelRef.current?.contains(e.target as Node)) {
        setShowColumnsPanel(false);
      }
    };
    window.addEventListener('mousedown', onDown);
    return () => window.removeEventListener('mousedown', onDown);
  }, [showColumnsPanel]);

  // Reset paging when filters change.
  useEffect(() => {
    setPageIndex(0);
  }, [search, sort?.key, sort?.dir, pageSize]);

  const filteredRows = useMemo(() => {
    const needle = search.trim().toLowerCase();
    if (!needle) return rows;
    return rows.filter((r) =>
      Object.values(r).some((v) => String(v ?? '').toLowerCase().includes(needle)),
    );
  }, [rows, search]);

  const sortedRows = useMemo(() => {
    if (!sort) return filteredRows;
    const col = columnMap.get(sort.key);
    if (!col) return filteredRows;
    const arr = [...filteredRows];
    arr.sort((a, b) => {
      const cmp = compareCells(a?.[sort.key], b?.[sort.key], col.numeric);
      return sort.dir === 'asc' ? cmp : -cmp;
    });
    return arr;
  }, [filteredRows, sort, columnMap]);

  const pageCount = Math.max(1, Math.ceil(sortedRows.length / pageSize));
  const pageRows = useMemo(
    () => sortedRows.slice(pageIndex * pageSize, (pageIndex + 1) * pageSize),
    [sortedRows, pageIndex, pageSize],
  );

  const cycleSort = (key: string) => {
    setSort((prev) => {
      if (!prev || prev.key !== key) return { key, dir: 'asc' };
      if (prev.dir === 'asc') return { key, dir: 'desc' };
      return null;
    });
  };

  // Drag reorder via native HTML5 drag/drop.
  const dragKeyRef = useRef<string | null>(null);
  const [dragOverKey, setDragOverKey] = useState<string | null>(null);
  const onDragStart = (key: string) => (e: React.DragEvent) => {
    dragKeyRef.current = key;
    e.dataTransfer.effectAllowed = 'move';
    try {
      // some browsers need data set on the transfer
      e.dataTransfer.setData('text/plain', key);
    } catch {
      // ignore
    }
  };
  const onDragOver = (key: string) => (e: React.DragEvent) => {
    if (!dragKeyRef.current || dragKeyRef.current === key) return;
    e.preventDefault();
    e.dataTransfer.dropEffect = 'move';
    if (dragOverKey !== key) setDragOverKey(key);
  };
  const onDrop = (target: string) => (e: React.DragEvent) => {
    e.preventDefault();
    const from = dragKeyRef.current;
    dragKeyRef.current = null;
    setDragOverKey(null);
    if (!from || from === target) return;
    setColumnOrder((prev) => {
      const next = prev.filter((k) => k !== from);
      const idx = next.indexOf(target);
      next.splice(idx, 0, from);
      return next;
    });
  };
  const onDragEnd = () => {
    dragKeyRef.current = null;
    setDragOverKey(null);
  };

  const liveLabel = providerLabel ? `Live · ${providerLabel}` : 'Live';

  const startRow = sortedRows.length === 0 ? 0 : pageIndex * pageSize + 1;
  const endRow = Math.min(sortedRows.length, (pageIndex + 1) * pageSize);

  return (
    <div className="flex flex-col gap-3">
      {/* Toolbar */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative min-w-0 flex-1 sm:max-w-xs">
          <Search
            size={12}
            className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-muted-foreground/60"
            aria-hidden
          />
          <input
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search results"
            aria-label="Search results"
            className="w-full rounded-md border border-border bg-background py-1.5 pl-7 pr-7 text-[12px] text-foreground placeholder:text-muted-foreground/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
          />
          {search && (
            <button
              type="button"
              aria-label="Clear search"
              onClick={() => setSearch('')}
              className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded p-0.5 text-muted-foreground/60 hover:text-foreground"
            >
              <X size={12} aria-hidden />
            </button>
          )}
        </div>

        {isLive && (
          <span className="inline-flex items-center gap-1.5 rounded-md border border-border bg-muted/40 px-2 py-1 text-[11px] font-medium text-muted-foreground">
            <span className="relative inline-flex h-1.5 w-1.5">
              <span className="absolute inset-0 rounded-full bg-emerald-500/70 animate-ping" aria-hidden />
              <span className="relative h-1.5 w-1.5 rounded-full bg-emerald-500" aria-hidden />
            </span>
            {liveLabel}
          </span>
        )}

        <button
          type="button"
          onClick={onRefresh}
          disabled={loading}
          aria-label="Refresh from source"
          title={isLive ? 'Refresh from source (no re-run)' : 'Re-parse loaded rows'}
          className="inline-flex h-7 items-center justify-center rounded-md border border-border bg-background px-2 text-[11px] text-muted-foreground hover:bg-muted hover:text-foreground disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
        >
          <RefreshCw size={11} className={cn('mr-1', loading && 'animate-spin')} aria-hidden />
          Refresh
        </button>

        <div ref={columnsPanelRef} className="relative">
          <button
            type="button"
            onClick={() => setShowColumnsPanel((v) => !v)}
            aria-expanded={showColumnsPanel}
            className="inline-flex h-7 items-center gap-1 rounded-md border border-border bg-background px-2 text-[11px] text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
          >
            <Columns3 size={11} aria-hidden />
            Columns
            <ChevronDown size={11} className={cn('transition-transform', showColumnsPanel && 'rotate-180')} aria-hidden />
          </button>
          {showColumnsPanel && (
            <div className="absolute right-0 top-full z-20 mt-1 max-h-72 w-56 overflow-auto rounded-md border border-border bg-background p-1.5 shadow-lg">
              <div className="px-1.5 py-1 text-[10px] uppercase tracking-wide text-muted-foreground/60">
                Show columns
              </div>
              <ul className="flex flex-col">
                {columnOrder.map((key) => {
                  const col = columnMap.get(key);
                  if (!col) return null;
                  const visible = !hidden.has(key);
                  return (
                    <li key={key}>
                      <label className="flex cursor-pointer items-center gap-2 rounded px-1.5 py-1 text-[12px] text-foreground hover:bg-muted">
                        <input
                          type="checkbox"
                          checked={visible}
                          onChange={() => {
                            setHidden((prev) => {
                              const next = new Set(prev);
                              if (next.has(key)) next.delete(key);
                              else next.add(key);
                              return next;
                            });
                          }}
                          className="h-3.5 w-3.5"
                          aria-label={`Toggle column ${key}`}
                        />
                        <span className="min-w-0 flex-1 truncate font-mono text-[12px]">{key}</span>
                        {col.type && (
                          <span className="text-[10px] text-muted-foreground/60">{typeLabel(col.type)}</span>
                        )}
                      </label>
                    </li>
                  );
                })}
              </ul>
            </div>
          )}
        </div>
      </div>

      {/* Grid */}
      <div className="overflow-hidden rounded-md border border-border">
        <div className="max-h-[420px] overflow-auto">
          <table className="w-full border-collapse text-[12.5px]">
            <thead className="sticky top-0 z-10 bg-muted/60 backdrop-blur">
              <tr>
                {orderedVisibleColumns.map((col) => {
                  const active = sort?.key === col.key;
                  const isDragOver = dragOverKey === col.key;
                  return (
                    <th
                      key={col.key}
                      scope="col"
                      draggable
                      onDragStart={onDragStart(col.key)}
                      onDragOver={onDragOver(col.key)}
                      onDrop={onDrop(col.key)}
                      onDragEnd={onDragEnd}
                      className={cn(
                        'group border-b border-border px-3 py-2 align-bottom font-medium text-foreground/90',
                        col.numeric ? 'text-right' : 'text-left',
                        isDragOver && 'ring-2 ring-inset ring-blue-500/40',
                      )}
                    >
                      <button
                        type="button"
                        onClick={() => cycleSort(col.key)}
                        aria-label={`Sort by ${col.key}`}
                        className={cn(
                          'inline-flex items-baseline gap-1.5 cursor-pointer select-none rounded text-[12px] font-medium hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1',
                          col.numeric ? 'ml-auto' : '',
                          active ? 'text-foreground' : 'text-foreground/80',
                        )}
                      >
                        <span className="cursor-grab">{col.key}</span>
                        {active && (
                          sort!.dir === 'asc'
                            ? <ArrowUp size={11} className="shrink-0 text-foreground" aria-hidden />
                            : <ArrowDown size={11} className="shrink-0 text-foreground" aria-hidden />
                        )}
                      </button>
                      {col.type && (
                        <div
                          className={cn(
                            'mt-0.5 font-mono text-[10px] uppercase tracking-wide text-muted-foreground/60',
                            col.numeric && 'text-right',
                          )}
                        >
                          {typeLabel(col.type)}
                        </div>
                      )}
                    </th>
                  );
                })}
              </tr>
            </thead>
            <tbody>
              {pageRows.length === 0 ? (
                <tr>
                  <td
                    colSpan={Math.max(1, orderedVisibleColumns.length)}
                    className="px-3 py-6 text-center text-[12px] text-muted-foreground"
                  >
                    {rows.length === 0
                      ? 'No rows returned.'
                      : 'No rows match the search.'}
                  </td>
                </tr>
              ) : (
                pageRows.map((row, ri) => (
                  <tr key={ri} className="border-b border-border/60 last:border-b-0 hover:bg-muted/30">
                    {orderedVisibleColumns.map((col) => {
                      const value = row?.[col.key];
                      const text = formatCell(value, col.numeric);
                      const dim = value === null || value === undefined;
                      return (
                        <td
                          key={col.key}
                          className={cn(
                            'px-3 py-1.5 align-top',
                            col.numeric ? 'text-right font-mono tabular-nums' : 'text-left',
                            '[overflow-wrap:anywhere]',
                          )}
                          title={dim ? '(null)' : text}
                        >
                          {dim ? (
                            <span className="text-muted-foreground/40">∅</span>
                          ) : (
                            text
                          )}
                        </td>
                      );
                    })}
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>

      {error && (
        <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-[12px] text-destructive">
          Failed to fetch more rows: {error.message}
        </div>
      )}

      {/* Footer */}
      <div className="flex flex-wrap items-center justify-between gap-2 text-[11px] text-muted-foreground">
        <div className="flex items-center gap-2">
          <span>
            {sortedRows.length === 0
              ? 'No rows'
              : `Showing ${startRow.toLocaleString()}–${endRow.toLocaleString()} of ${sortedRows.length.toLocaleString()}`}
          </span>
          {totalRows != null && totalRows > sortedRows.length && (
            <span className="text-muted-foreground/60">
              · {totalRows.toLocaleString()} total
            </span>
          )}
          {hasMore && (
            <button
              type="button"
              onClick={onLoadMore}
              disabled={loading}
              className="inline-flex items-center gap-1 rounded text-[11px] text-foreground/80 underline-offset-2 hover:underline disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
            >
              {loading ? (
                <>
                  <Loader2 size={10} className="animate-spin" aria-hidden /> Loading more…
                </>
              ) : (
                'Load more from source'
              )}
            </button>
          )}
        </div>

        <div className="flex items-center gap-1.5">
          <label className="inline-flex items-center gap-1.5">
            <span>Rows</span>
            <select
              value={pageSize}
              onChange={(e) => setPageSize(Number(e.target.value))}
              aria-label="Page size"
              className="rounded-md border border-border bg-background px-1.5 py-0.5 text-[11px] text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
            >
              {PAGE_SIZES.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
          </label>
          <button
            type="button"
            onClick={() => setPageIndex((i) => Math.max(0, i - 1))}
            disabled={pageIndex === 0}
            aria-label="Previous page"
            className="inline-flex h-6 w-6 items-center justify-center rounded-md border border-border bg-background text-muted-foreground disabled:opacity-40 hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
          >
            <ChevronLeft size={12} aria-hidden />
          </button>
          <span className="font-mono text-[11px] tabular-nums">
            {pageIndex + 1} / {pageCount}
          </span>
          <button
            type="button"
            onClick={() => setPageIndex((i) => Math.min(pageCount - 1, i + 1))}
            disabled={pageIndex >= pageCount - 1}
            aria-label="Next page"
            className="inline-flex h-6 w-6 items-center justify-center rounded-md border border-border bg-background text-muted-foreground disabled:opacity-40 hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-1"
          >
            <ChevronRight size={12} aria-hidden />
          </button>
        </div>
      </div>
    </div>
  );
}
