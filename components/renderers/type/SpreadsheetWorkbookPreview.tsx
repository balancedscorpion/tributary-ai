import React from 'react';
import { AlertTriangle, ChevronDown, ExternalLink, EyeOff, Loader2, MessageSquare, Sigma, Table } from 'lucide-react';
import MarkdownRenderer from '@/components/chat/MarkdownRenderer';
import { cn } from '@/lib/utils';
import type { RendererProps } from '../types';

type Artifact = RendererProps['artifact'];

interface SpreadsheetWorkbookPreviewProps {
  artifact: Artifact;
  workbookUrl?: string;
  sheetUrls: string[];
  markdownUrls: string[];
}

interface WorkbookPreviewJson {
  version?: number;
  workbook?: Record<string, unknown>;
  metadata?: Record<string, unknown>;
  sheets?: unknown[];
  [key: string]: unknown;
}

interface SheetInfo {
  id?: string;
  name?: string;
  index?: number;
  state?: string;
  dimension?: string;
  rowCount?: number;
  columnCount?: number;
  cellCount?: number;
  formulaCount?: number;
  commentCount?: number;
  mergedRangeCount?: number;
  tableIds?: string[];
  chartIds?: string[];
  hasHiddenRowsOrColumns?: boolean;
}

interface SheetMetadata {
  previewRowCount?: number;
  previewColumnCount?: number;
  totalRowCount?: number;
  totalColumnCount?: number;
  totalCellCount?: number;
  formulaCount?: number;
  commentCount?: number;
  mergedRangeCount?: number;
  hiddenRowsOrColumns?: boolean;
  truncated?: boolean;
  nextRowStart?: number;
  tables?: unknown[];
  charts?: unknown[];
}

interface SheetColumn {
  index: number;
  name: string;
}

interface SheetCellStyle {
  bold?: boolean;
  italic?: boolean;
  fontColor?: string;
  fillColor?: string;
  horizontalAlign?: string;
  verticalAlign?: string;
  wrapText?: boolean;
}

interface SheetCell {
  address?: string;
  row?: number;
  col?: number;
  display?: string | number | boolean | null;
  kind?: string;
  formula?: string;
  hyperlink?: string;
  comment?: string;
  numberFormat?: string;
  mergedParent?: string;
  header?: boolean;
  style?: SheetCellStyle;
}

interface SheetRow {
  index: number;
  cells: SheetCell[];
}

interface SheetPreviewJson {
  version?: number;
  sheet?: SheetInfo;
  metadata?: SheetMetadata;
  columns?: SheetColumn[];
  rows?: SheetRow[];
  headerRows?: number[];
  mergedRanges?: string[];
}

interface SheetCellsPageResponse {
  rows?: SheetRow[];
  columns?: SheetColumn[];
  total_row_count?: number;
  total_column_count?: number;
  has_more_rows?: boolean;
  next_row_start?: number;
  response_size?: number;
}

interface MarkdownSheetPreview {
  url: string;
  content: string;
}

type PreviewState =
  | { status: 'loading' }
  | { status: 'json'; workbook: WorkbookPreviewJson | null; sheets: SheetPreviewJson[] }
  | { status: 'markdown'; sheets: MarkdownSheetPreview[] }
  | { status: 'error'; message: string };

interface Metric {
  label: string;
  value: React.ReactNode;
  tone?: 'default' | 'warning';
}

const COLUMN_HEADER_HEIGHT = 28;
const SHEET_HEADER_HEIGHT = 32;
const ROW_NUMBER_WIDTH = 56;
const CELL_WIDTH = 132;

function asRecord(value: unknown): Record<string, any> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, any>
    : {};
}

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return '';
}

function firstNumber(...values: unknown[]): number | undefined {
  for (const value of values) {
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value === 'string' && value.trim()) {
      const parsed = Number(value.trim());
      if (Number.isFinite(parsed)) return parsed;
    }
  }
  return undefined;
}

function firstBool(...values: unknown[]): boolean | undefined {
  for (const value of values) {
    if (typeof value === 'boolean') return value;
  }
  return undefined;
}

function numericListCount(value: unknown): number | undefined {
  if (Array.isArray(value)) return value.length;
  return firstNumber(value);
}

function formatNumber(value: unknown): string {
  const number = firstNumber(value);
  return typeof number === 'number' ? number.toLocaleString() : '-';
}

function formatDimension(rows?: number, columns?: number, dimension?: string): string {
  if (dimension) return dimension;
  if (typeof rows === 'number' && typeof columns === 'number') return `${rows.toLocaleString()} x ${columns.toLocaleString()}`;
  return '-';
}

function columnName(index: number): string {
  let next = Math.max(1, Math.floor(index));
  let name = '';
  while (next > 0) {
    const rem = (next - 1) % 26;
    name = String.fromCharCode(65 + rem) + name;
    next = Math.floor((next - rem - 1) / 26);
  }
  return name;
}

function sheetName(sheet: SheetPreviewJson, fallbackIndex: number): string {
  return firstString(sheet.sheet?.name, sheet.sheet?.id) || `Sheet ${fallbackIndex + 1}`;
}

function sheetState(sheet: SheetPreviewJson): string {
  return firstString(sheet.sheet?.state).toLowerCase() || 'visible';
}

function isHiddenSheet(sheet: SheetPreviewJson): boolean {
  const state = sheetState(sheet);
  return state !== '' && state !== 'visible';
}

function normalizeColumns(sheet: SheetPreviewJson): SheetColumn[] {
  const declared = Array.isArray(sheet.columns)
    ? sheet.columns
      .map((column) => ({
        index: firstNumber(column?.index) || 0,
        name: firstString(column?.name),
      }))
      .filter((column) => column.index > 0)
    : [];
  if (declared.length > 0) {
    return declared
      .sort((a, b) => a.index - b.index)
      .map((column) => ({ ...column, name: column.name || columnName(column.index) }));
  }

  const maxFromCells = (sheet.rows || []).reduce((max, row) => {
    return Math.max(max, ...(row.cells || []).map((cell) => firstNumber(cell.col) || 0));
  }, 0);
  const maxColumn = Math.max(1, firstNumber(sheet.metadata?.previewColumnCount, sheet.sheet?.columnCount) || maxFromCells || 1);
  return Array.from({ length: maxColumn }, (_, index) => ({
    index: index + 1,
    name: columnName(index + 1),
  }));
}

function normalizeRows(sheet: SheetPreviewJson): SheetRow[] {
  return (sheet.rows || [])
    .map((row) => ({
      index: firstNumber(row?.index) || 0,
      cells: Array.isArray(row?.cells) ? row.cells : [],
    }))
    .filter((row) => row.index > 0)
    .sort((a, b) => a.index - b.index);
}

function cellForColumn(row: SheetRow, column: SheetColumn): SheetCell {
  return row.cells.find((cell) => firstNumber(cell.col) === column.index) || {
    address: `${column.name}${row.index}`,
    row: row.index,
    col: column.index,
    display: '',
  };
}

function displayValue(cell: SheetCell): string {
  if (cell.display === null || cell.display === undefined) return '';
  return String(cell.display);
}

function normalizeHexColor(value: unknown): string | undefined {
  const raw = firstString(value);
  if (!raw) return undefined;
  const clean = raw.startsWith('#') ? raw : `#${raw}`;
  if (/^#[0-9a-f]{3}$/i.test(clean) || /^#[0-9a-f]{6}$/i.test(clean)) return clean;
  if (/^#[0-9a-f]{8}$/i.test(clean)) return `#${clean.slice(3)}`;
  return undefined;
}

function normalizeHorizontalAlign(value: unknown): React.CSSProperties['textAlign'] | undefined {
  const align = firstString(value).toLowerCase();
  if (align === 'left' || align === 'center' || align === 'right' || align === 'justify') return align;
  return undefined;
}

function normalizeVerticalAlign(value: unknown): React.CSSProperties['verticalAlign'] | undefined {
  const align = firstString(value).toLowerCase();
  if (align === 'top') return 'top';
  if (align === 'center' || align === 'middle') return 'middle';
  if (align === 'bottom') return 'bottom';
  return undefined;
}

function cellInlineStyle(cell: SheetCell): React.CSSProperties {
  const style = cell.style || {};
  const css: React.CSSProperties = {};
  const fontColor = normalizeHexColor(style.fontColor);
  const fillColor = normalizeHexColor(style.fillColor);
  const textAlign = normalizeHorizontalAlign(style.horizontalAlign);
  const verticalAlign = normalizeVerticalAlign(style.verticalAlign);
  if (fontColor) css.color = fontColor;
  if (fillColor) css.backgroundColor = fillColor;
  if (textAlign) css.textAlign = textAlign;
  if (verticalAlign) css.verticalAlign = verticalAlign;
  if (style.bold === true) css.fontWeight = 700;
  if (style.italic === true) css.fontStyle = 'italic';
  if (style.wrapText === true) css.whiteSpace = 'pre-wrap';
  if (style.wrapText === false) css.whiteSpace = 'nowrap';
  return css;
}

function defaultCellAlignment(cell: SheetCell): string {
  if (cell.style?.horizontalAlign) return '';
  const kind = firstString(cell.kind).toLowerCase();
  if (kind === 'number' || kind === 'currency' || kind === 'percentage' || kind === 'date') {
    return 'text-right tabular-nums';
  }
  return '';
}

function safeHref(value: unknown): string | undefined {
  const href = firstString(value);
  if (/^(https?:|mailto:)/i.test(href)) return href;
  return undefined;
}

function cellTitle(cell: SheetCell): string {
  const details = [
    firstString(cell.address),
    cell.formula ? `Formula: ${cell.formula}` : '',
    cell.comment ? `Comment: ${cell.comment}` : '',
    cell.hyperlink ? `Link: ${cell.hyperlink}` : '',
    cell.numberFormat ? `Format: ${cell.numberFormat}` : '',
    cell.mergedParent ? `Merged parent: ${cell.mergedParent}` : '',
  ].filter(Boolean);
  return details.join('\n');
}

function tabNameFromUrl(url: string, index: number): string {
  try {
    const parsed = new URL(url, 'http://local');
    const path = parsed.searchParams.get('path') || parsed.pathname;
    const match = path.match(/\/worksheets\/([^/]+)\//i);
    if (match?.[1]) return decodeURIComponent(match[1]).replace(/[_-]+/g, ' ');
  } catch {
    // Fall through to the stable label.
  }
  return `Sheet ${index + 1}`;
}

async function fetchJson<T>(url: string, signal: AbortSignal): Promise<T> {
  const response = await fetch(url, { credentials: 'same-origin', signal });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return await response.json() as T;
}

async function fetchMarkdown(url: string, signal: AbortSignal): Promise<MarkdownSheetPreview> {
  const response = await fetch(url, { credentials: 'same-origin', signal });
  if (!response.ok) throw new Error(`HTTP ${response.status}`);
  return { url, content: await response.text() };
}

function isUsableSheetPreview(value: unknown): value is SheetPreviewJson {
  const record = asRecord(value);
  return Array.isArray(record.rows) && Array.isArray(record.columns);
}

function spreadsheetCellsUrl(artifactId: string, sheet: SheetPreviewJson, rowStart: number, rowCount: number, colStart: number, colCount: number): string {
  const params = new URLSearchParams({
    sheet_id: firstString(sheet.sheet?.id, sheet.sheet?.name),
    row_start: String(rowStart),
    row_count: String(rowCount),
    col_start: String(colStart),
    col_count: String(colCount),
  });
  return `/api/artifacts/${encodeURIComponent(artifactId)}/spreadsheet/cells?${params.toString()}`;
}

function maxLoadedRowIndex(sheet: SheetPreviewJson): number {
  return normalizeRows(sheet).reduce((max, row) => Math.max(max, row.index), 0);
}

function nextRowStart(sheet: SheetPreviewJson): number {
  return firstNumber(sheet.metadata?.nextRowStart) || maxLoadedRowIndex(sheet) + 1 || 1;
}

function loadedThroughRow(sheet: SheetPreviewJson): number {
  const next = firstNumber(sheet.metadata?.nextRowStart);
  if (next && next > 1) return next - 1;
  return maxLoadedRowIndex(sheet);
}

function loadMoreColumnCount(sheet: SheetPreviewJson): number {
  const totalColumns = firstNumber(sheet.metadata?.totalColumnCount, sheet.sheet?.columnCount, sheet.columns?.length) || 1;
  return Math.max(1, Math.min(200, totalColumns));
}

function mergeSheetCellsPage(sheet: SheetPreviewJson, page: SheetCellsPageResponse): SheetPreviewJson {
  const rowMap = new Map<number, SheetRow>();
  for (const row of normalizeRows(sheet)) {
    rowMap.set(row.index, row);
  }
  for (const row of page.rows || []) {
    const index = firstNumber(row?.index) || 0;
    if (index <= 0) continue;
    rowMap.set(index, {
      index,
      cells: Array.isArray(row?.cells) ? row.cells : [],
    });
  }
  const rows = [...rowMap.values()].sort((a, b) => a.index - b.index);

  const columnMap = new Map<number, SheetColumn>();
  for (const column of normalizeColumns(sheet)) {
    columnMap.set(column.index, column);
  }
  for (const column of page.columns || []) {
    const index = firstNumber(column?.index) || 0;
    if (index <= 0) continue;
    columnMap.set(index, {
      index,
      name: firstString(column?.name) || columnName(index),
    });
  }
  const columns = [...columnMap.values()].sort((a, b) => a.index - b.index);

  return {
    ...sheet,
    columns,
    rows,
    metadata: {
      ...sheet.metadata,
      previewRowCount: rows.length,
      previewColumnCount: columns.length,
      totalRowCount: firstNumber(page.total_row_count, sheet.metadata?.totalRowCount, sheet.sheet?.rowCount),
      totalColumnCount: firstNumber(page.total_column_count, sheet.metadata?.totalColumnCount, sheet.sheet?.columnCount),
      truncated: Boolean(page.has_more_rows),
      nextRowStart: firstNumber(page.next_row_start) || nextRowStart(sheet),
    },
  };
}

const LoadingState: React.FC = () => (
  <div className="flex h-full items-center justify-center rounded-lg border border-border bg-muted/20 text-sm text-muted-foreground">
    <Loader2 size={16} className="mr-2 motion-safe:animate-spin" aria-hidden="true" />
    Loading spreadsheet preview...
  </div>
);

const ErrorState: React.FC<{ message: string }> = ({ message }) => (
  <div className="flex h-full items-center justify-center rounded-lg border border-dashed border-border bg-muted/20 p-6">
    <div className="max-w-md text-center">
      <div className="mx-auto mb-3 flex h-12 w-12 items-center justify-center rounded-lg bg-destructive/10 text-destructive">
        <AlertTriangle size={22} aria-hidden="true" />
      </div>
      <div className="text-sm font-medium text-foreground">Spreadsheet preview unavailable</div>
      <p className="mt-2 text-xs leading-relaxed text-muted-foreground">{message}</p>
    </div>
  </div>
);

const MetricStrip: React.FC<{ title: string; icon?: React.ReactNode; metrics: Metric[]; badges?: React.ReactNode }> = ({
  title,
  icon,
  metrics,
  badges,
}) => (
  <section className="min-w-0 px-3 py-2.5">
    <div className="mb-2 flex min-w-0 items-center gap-2">
      {icon}
      <div className="truncate text-[11px] font-semibold uppercase tracking-wide text-muted-foreground">{title}</div>
      {badges}
    </div>
    <div className="flex flex-wrap gap-x-5 gap-y-2">
      {metrics.map((metric) => (
        <div key={metric.label} className="min-w-[4.5rem]">
          <div className="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">{metric.label}</div>
          <div className={cn(
            "mt-0.5 truncate text-xs font-semibold text-foreground",
            metric.tone === 'warning' && "text-accent",
          )}>
            {metric.value}
          </div>
        </div>
      ))}
    </div>
  </section>
);

const SheetTabs: React.FC<{
  tabs: Array<{ id: string; label: string; detail?: string; hidden?: boolean }>;
  activeIndex: number;
  onChange: (index: number) => void;
  baseId: string;
}> = ({ tabs, activeIndex, onChange, baseId }) => {
  const focusTab = React.useCallback((index: number) => {
    window.requestAnimationFrame(() => {
      document.getElementById(`${baseId}-tab-${index}`)?.focus();
    });
  }, [baseId]);

  const onKeyDown = React.useCallback((event: React.KeyboardEvent<HTMLButtonElement>, index: number) => {
    let nextIndex = index;
    if (event.key === 'ArrowRight') nextIndex = (index + 1) % tabs.length;
    else if (event.key === 'ArrowLeft') nextIndex = (index - 1 + tabs.length) % tabs.length;
    else if (event.key === 'Home') nextIndex = 0;
    else if (event.key === 'End') nextIndex = tabs.length - 1;
    else return;
    event.preventDefault();
    onChange(nextIndex);
    focusTab(nextIndex);
  }, [focusTab, onChange, tabs.length]);

  return (
    <div className="shrink-0 border-b border-border bg-background">
      <div
        role="tablist"
        aria-label="Workbook sheets"
        className="flex min-h-10 items-end gap-1 overflow-x-auto px-2 pt-2"
      >
        {tabs.map((tab, index) => {
          const active = index === activeIndex;
          return (
            <button
              key={tab.id}
              id={`${baseId}-tab-${index}`}
              type="button"
              role="tab"
              aria-selected={active}
              aria-controls={`${baseId}-panel`}
              tabIndex={active ? 0 : -1}
              onClick={() => onChange(index)}
              onKeyDown={(event) => onKeyDown(event, index)}
              className={cn(
                "flex h-8 max-w-[14rem] shrink-0 items-center gap-1.5 rounded-t-md border border-b-0 px-2.5 text-left text-xs transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring",
                active
                  ? "border-border bg-card text-foreground"
                  : "border-transparent bg-muted/30 text-muted-foreground hover:border-border/70 hover:bg-muted/50 hover:text-foreground",
              )}
            >
              {tab.hidden && <EyeOff size={12} className="shrink-0 text-muted-foreground" aria-hidden="true" />}
              <span className="truncate font-medium">{tab.label}</span>
              {tab.detail && <span className="shrink-0 font-mono text-[10px] text-muted-foreground">{tab.detail}</span>}
            </button>
          );
        })}
      </div>
    </div>
  );
};

const CellAffordances: React.FC<{ cell: SheetCell }> = ({ cell }) => {
  const href = safeHref(cell.hyperlink);
  if (!cell.formula && !cell.comment && !cell.hyperlink) return null;
  return (
    <span className="ml-1 inline-flex shrink-0 items-center gap-0.5 text-muted-foreground">
      {cell.formula && (
        <span title={cell.formula} aria-label="Formula" className="inline-flex h-4 w-4 items-center justify-center rounded-sm bg-muted/70">
          <Sigma size={10} aria-hidden="true" />
        </span>
      )}
      {cell.comment && (
        <span title={cell.comment} aria-label="Comment" className="inline-flex h-4 w-4 items-center justify-center rounded-sm bg-muted/70">
          <MessageSquare size={10} aria-hidden="true" />
        </span>
      )}
      {cell.hyperlink && (
        href ? (
          <a
            href={href}
            title={cell.hyperlink}
            target="_blank"
            rel="noreferrer"
            aria-label="Open hyperlink"
            className="inline-flex h-4 w-4 items-center justify-center rounded-sm bg-muted/70 text-muted-foreground hover:text-primary"
          >
            <ExternalLink size={10} aria-hidden="true" />
          </a>
        ) : (
          <span title={cell.hyperlink} aria-label="Hyperlink" className="inline-flex h-4 w-4 items-center justify-center rounded-sm bg-muted/70">
            <ExternalLink size={10} aria-hidden="true" />
          </span>
        )
      )}
    </span>
  );
};

const CellContent: React.FC<{ cell: SheetCell }> = ({ cell }) => {
  const value = displayValue(cell);
  const href = safeHref(cell.hyperlink);
  const text = (
    <span className={cn("min-w-0 flex-1", cell.style?.wrapText === true ? "whitespace-pre-wrap break-words" : "truncate")}>
      {value || '\u00a0'}
    </span>
  );

  return (
    <div className="flex min-w-0 items-start gap-1">
      {href && value ? (
        <a
          href={href}
          target="_blank"
          rel="noreferrer"
          className="min-w-0 flex-1 truncate underline decoration-dotted underline-offset-2 hover:text-primary"
        >
          {value}
        </a>
      ) : text}
      <CellAffordances cell={cell} />
    </div>
  );
};

const SpreadsheetGrid: React.FC<{ sheet: SheetPreviewJson; panelId: string; labelledBy: string }> = ({
  sheet,
  panelId,
  labelledBy,
}) => {
  const columns = React.useMemo(() => normalizeColumns(sheet), [sheet]);
  const rows = React.useMemo(() => normalizeRows(sheet), [sheet]);
  const rowByIndex = React.useMemo(() => new Map(rows.map((row) => [row.index, row])), [rows]);
  const headerIndexes = React.useMemo(() => {
    return [...new Set((sheet.headerRows || []).map((index) => firstNumber(index) || 0).filter(Boolean))]
      .sort((a, b) => a - b);
  }, [sheet.headerRows]);
  const headerIndexSet = React.useMemo(() => new Set(headerIndexes), [headerIndexes]);
  const headerRows = headerIndexes.map((index) => rowByIndex.get(index) || { index, cells: [] });
  const bodyRows = rows.filter((row) => !headerIndexSet.has(row.index));
  const tableMinWidth = ROW_NUMBER_WIDTH + Math.max(columns.length, 1) * CELL_WIDTH;

  return (
    <div
      id={panelId}
      role="tabpanel"
      aria-labelledby={labelledBy}
      className="min-h-0 flex-1 overflow-auto bg-background"
    >
      <table
        className="border-separate border-spacing-0 text-xs"
        style={{ minWidth: tableMinWidth }}
      >
        <colgroup>
          <col style={{ width: ROW_NUMBER_WIDTH }} />
          {columns.map((column) => (
            <col key={column.index} style={{ width: CELL_WIDTH }} />
          ))}
        </colgroup>
        <thead>
          <tr className="h-7">
            <th
              scope="col"
              aria-label="Rows"
              className="sticky left-0 top-0 z-40 border-b border-r border-border bg-muted px-2 text-center font-mono text-[10px] font-semibold text-muted-foreground"
            />
            {columns.map((column) => (
              <th
                key={column.index}
                scope="col"
                className="sticky top-0 z-30 border-b border-r border-border bg-muted px-2 text-center font-mono text-[10px] font-semibold text-muted-foreground"
                style={{ height: COLUMN_HEADER_HEIGHT }}
              >
                {column.name}
              </th>
            ))}
          </tr>
          {headerRows.map((row, headerOffset) => {
            const top = COLUMN_HEADER_HEIGHT + headerOffset * SHEET_HEADER_HEIGHT;
            return (
              <tr key={`header-${row.index}`} className="h-8">
                <th
                  scope="row"
                  className="sticky left-0 z-40 border-b border-r border-border bg-card px-2 text-right font-mono text-[10px] font-medium text-muted-foreground"
                  style={{ top }}
                >
                  {row.index}
                </th>
                {columns.map((column) => {
                  const cell = cellForColumn(row, column);
                  return (
                    <th
                      key={`${row.index}-${column.index}`}
                      scope="col"
                      title={cellTitle(cell)}
                      className={cn(
                        "sticky z-30 min-w-0 border-b border-r border-border bg-card px-2 py-1.5 text-left font-semibold text-foreground",
                        defaultCellAlignment(cell),
                      )}
                      style={{ ...cellInlineStyle(cell), top }}
                    >
                      <CellContent cell={cell} />
                    </th>
                  );
                })}
              </tr>
            );
          })}
        </thead>
        <tbody>
          {bodyRows.map((row) => (
            <tr key={row.index} className="h-8">
              <th
                scope="row"
                className="sticky left-0 z-20 border-b border-r border-border bg-card px-2 text-right font-mono text-[10px] font-medium text-muted-foreground"
              >
                {row.index}
              </th>
              {columns.map((column) => {
                const cell = cellForColumn(row, column);
                const CellTag = cell.header ? 'th' : 'td';
                return (
                  <CellTag
                    key={`${row.index}-${column.index}`}
                    scope={cell.header ? 'row' : undefined}
                    title={cellTitle(cell)}
                    className={cn(
                      "min-w-0 border-b border-r border-border bg-background px-2 py-1.5 align-middle text-foreground",
                      cell.header && "font-semibold",
                      defaultCellAlignment(cell),
                    )}
                    style={cellInlineStyle(cell)}
                  >
                    <CellContent cell={cell} />
                  </CellTag>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length === 0 && (
        <div className="flex h-32 items-center justify-center border-b border-border text-xs text-muted-foreground">
          No preview rows available.
        </div>
      )}
    </div>
  );
};

function workbookMetrics(workbook: WorkbookPreviewJson | null, sheets: SheetPreviewJson[]): Metric[] {
  const workbookRecord = asRecord(workbook?.workbook);
  const metadata = asRecord(workbook?.metadata);
  const workbookSheets = Array.isArray(workbook?.sheets) ? workbook?.sheets || [] : [];
  const sheetCount = firstNumber(
    workbookRecord.sheetCount,
    workbookRecord.worksheetCount,
    metadata.sheetCount,
    metadata.worksheetCount,
    workbookSheets.length || undefined,
    sheets.length,
  );
  const hiddenSheetCount = firstNumber(
    workbookRecord.hiddenSheetCount,
    metadata.hiddenSheetCount,
    sheets.filter(isHiddenSheet).length,
  );
  const totalCells = firstNumber(
    metadata.totalCellCount,
    metadata.cellCount,
    sheets.reduce((sum, sheet) => sum + (firstNumber(sheet.metadata?.totalCellCount, sheet.sheet?.cellCount) || 0), 0),
  );
  const formulaCount = firstNumber(
    metadata.formulaCount,
    sheets.reduce((sum, sheet) => sum + (firstNumber(sheet.metadata?.formulaCount, sheet.sheet?.formulaCount) || 0), 0),
  );
  const commentCount = firstNumber(
    metadata.commentCount,
    sheets.reduce((sum, sheet) => sum + (firstNumber(sheet.metadata?.commentCount, sheet.sheet?.commentCount) || 0), 0),
  );
  const mergedRangeCount = firstNumber(
    metadata.mergedRangeCount,
    sheets.reduce((sum, sheet) => sum + (firstNumber(sheet.metadata?.mergedRangeCount, sheet.sheet?.mergedRangeCount, sheet.mergedRanges?.length) || 0), 0),
  );

  return [
    { label: 'Sheets', value: formatNumber(sheetCount) },
    { label: 'Hidden', value: formatNumber(hiddenSheetCount), tone: hiddenSheetCount ? 'warning' : 'default' },
    { label: 'Cells', value: formatNumber(totalCells) },
    { label: 'Formulas', value: formatNumber(formulaCount) },
    { label: 'Comments', value: formatNumber(commentCount) },
    { label: 'Merged', value: formatNumber(mergedRangeCount) },
  ];
}

function activeSheetMetrics(sheet: SheetPreviewJson): Metric[] {
  const totalRows = firstNumber(sheet.metadata?.totalRowCount, sheet.sheet?.rowCount);
  const totalColumns = firstNumber(sheet.metadata?.totalColumnCount, sheet.sheet?.columnCount);
  const previewRows = firstNumber(sheet.metadata?.previewRowCount, sheet.rows?.length);
  const previewColumns = firstNumber(sheet.metadata?.previewColumnCount, sheet.columns?.length);
  const tableCount = numericListCount(sheet.metadata?.tables) ?? numericListCount(sheet.sheet?.tableIds);
  const chartCount = numericListCount(sheet.metadata?.charts) ?? numericListCount(sheet.sheet?.chartIds);
  const formulaCount = firstNumber(sheet.metadata?.formulaCount, sheet.sheet?.formulaCount);
  const commentCount = firstNumber(sheet.metadata?.commentCount, sheet.sheet?.commentCount);
  const mergedRangeCount = firstNumber(sheet.metadata?.mergedRangeCount, sheet.sheet?.mergedRangeCount, sheet.mergedRanges?.length);
  const truncated = firstBool(sheet.metadata?.truncated);

  return [
    { label: 'Range', value: formatDimension(totalRows, totalColumns, sheet.sheet?.dimension) },
    { label: 'Preview', value: `${formatNumber(previewRows)} x ${formatNumber(previewColumns)}`, tone: truncated ? 'warning' : 'default' },
    { label: 'Formulas', value: formatNumber(formulaCount) },
    { label: 'Comments', value: formatNumber(commentCount) },
    { label: 'Merged', value: formatNumber(mergedRangeCount) },
    { label: 'Tables', value: formatNumber(tableCount) },
    { label: 'Charts', value: formatNumber(chartCount) },
  ];
}

const JsonWorkbookPreview: React.FC<{
  artifact: Artifact;
  workbook: WorkbookPreviewJson | null;
  sheets: SheetPreviewJson[];
}> = ({ artifact, workbook, sheets }) => {
  const [activeIndex, setActiveIndex] = React.useState(0);
  const [loadedSheets, setLoadedSheets] = React.useState<SheetPreviewJson[]>(sheets);
  const [loadState, setLoadState] = React.useState<{
    status: 'idle' | 'loading' | 'error';
    sheetKey?: string;
    message?: string;
  }>({ status: 'idle' });
  const baseId = React.useId();

  React.useEffect(() => {
    setActiveIndex(0);
    setLoadedSheets(sheets);
    setLoadState({ status: 'idle' });
  }, [artifact.id, sheets.length]);

  React.useEffect(() => {
    setLoadedSheets(sheets);
  }, [sheets]);

  const boundedActiveIndex = Math.min(activeIndex, Math.max(loadedSheets.length - 1, 0));
  const activeSheet = loadedSheets[boundedActiveIndex];
  const hiddenRowsOrColumns = firstBool(activeSheet?.metadata?.hiddenRowsOrColumns, activeSheet?.sheet?.hasHiddenRowsOrColumns);
  const truncated = firstBool(activeSheet?.metadata?.truncated);
  const activeSheetKey = activeSheet ? firstString(activeSheet.sheet?.id, activeSheet.sheet?.name) : '';
  const loadingActiveSheet = loadState.status === 'loading' && loadState.sheetKey === activeSheetKey;
  const loadErrorActiveSheet = loadState.status === 'error' && loadState.sheetKey === activeSheetKey ? loadState.message : '';

  if (!activeSheet) {
    return <ErrorState message="No worksheet preview data was returned for this workbook." />;
  }

  const tabs = loadedSheets.map((sheet, index) => {
    const rows = firstNumber(sheet.metadata?.totalRowCount, sheet.sheet?.rowCount);
    const columns = firstNumber(sheet.metadata?.totalColumnCount, sheet.sheet?.columnCount);
    return {
      id: firstString(sheet.sheet?.id) || `${index}`,
      label: sheetName(sheet, index),
      detail: rows && columns ? `${rows}x${columns}` : undefined,
      hidden: isHiddenSheet(sheet),
    };
  });

  const onLoadMoreRows = async () => {
    if (!activeSheet || !activeSheetKey || loadingActiveSheet) return;
    const rowStart = nextRowStart(activeSheet);
    const colCount = loadMoreColumnCount(activeSheet);
    setLoadState({ status: 'loading', sheetKey: activeSheetKey });
    try {
      const response = await fetch(spreadsheetCellsUrl(artifact.id, activeSheet, rowStart, 200, 1, colCount), {
        credentials: 'same-origin',
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const page = await response.json() as SheetCellsPageResponse;
      setLoadedSheets((current) => current.map((sheet, index) => (
        index === boundedActiveIndex ? mergeSheetCellsPage(sheet, page) : sheet
      )));
      setLoadState({ status: 'idle' });
    } catch {
      setLoadState({
        status: 'error',
        sheetKey: activeSheetKey,
        message: 'Could not load more rows.',
      });
    }
  };

  const sheetBadges = (
    <div className="ml-auto flex shrink-0 items-center gap-1">
      {isHiddenSheet(activeSheet) && (
        <span className="rounded-sm border border-border px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
          Hidden
        </span>
      )}
      {hiddenRowsOrColumns && (
        <span className="rounded-sm border border-border px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
          Hidden rows/cols
        </span>
      )}
      {truncated && (
        <span className="rounded-sm border border-accent/30 bg-accent/10 px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-accent">
          Truncated
        </span>
      )}
    </div>
  );

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-background">
      <div className="grid shrink-0 divide-y divide-border border-b border-border bg-card/60 lg:grid-cols-2 lg:divide-x lg:divide-y-0">
        <MetricStrip
          title={artifact.title || 'Workbook'}
          icon={<Table size={13} className="shrink-0 text-muted-foreground" aria-hidden="true" />}
          metrics={workbookMetrics(workbook, loadedSheets)}
        />
        <MetricStrip
          title={sheetName(activeSheet, boundedActiveIndex)}
          metrics={activeSheetMetrics(activeSheet)}
          badges={sheetBadges}
        />
      </div>
      <SheetTabs
        tabs={tabs}
        activeIndex={boundedActiveIndex}
        onChange={setActiveIndex}
        baseId={baseId}
      />
      <SpreadsheetGrid
        sheet={activeSheet}
        panelId={`${baseId}-panel`}
        labelledBy={`${baseId}-tab-${boundedActiveIndex}`}
      />
      {truncated && activeSheetKey && (
        <div className="flex shrink-0 items-center justify-between gap-3 border-t border-border bg-card/60 px-3 py-2">
          <div className="min-w-0 text-[11px] text-muted-foreground">
            {loadErrorActiveSheet || `Through row ${formatNumber(loadedThroughRow(activeSheet))} of ${formatNumber(firstNumber(activeSheet.metadata?.totalRowCount, activeSheet.sheet?.rowCount))}`}
          </div>
          <button
            type="button"
            onClick={onLoadMoreRows}
            disabled={loadingActiveSheet}
            className="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-md border border-border bg-background px-2.5 text-xs font-medium text-foreground shadow-sm transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-60"
          >
            {loadingActiveSheet ? (
              <Loader2 size={14} className="motion-safe:animate-spin" aria-hidden="true" />
            ) : (
              <ChevronDown size={14} aria-hidden="true" />
            )}
            Load more rows
          </button>
        </div>
      )}
    </div>
  );
};

const MarkdownWorkbookPreview: React.FC<{ artifact: Artifact; sheets: MarkdownSheetPreview[] }> = ({ artifact, sheets }) => {
  const [activeIndex, setActiveIndex] = React.useState(0);
  const baseId = React.useId();

  React.useEffect(() => {
    setActiveIndex(0);
  }, [artifact.id, sheets.length]);

  const boundedActiveIndex = Math.min(activeIndex, Math.max(sheets.length - 1, 0));
  const activeSheet = sheets[boundedActiveIndex];
  const tabs = sheets.map((sheet, index) => ({
    id: sheet.url,
    label: tabNameFromUrl(sheet.url, index),
  }));

  if (!activeSheet) {
    return <ErrorState message="No spreadsheet preview data was returned for this workbook." />;
  }

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden rounded-lg border border-border bg-background">
      <div className="grid shrink-0 divide-y divide-border border-b border-border bg-card/60 lg:grid-cols-2 lg:divide-x lg:divide-y-0">
        <MetricStrip
          title={artifact.title || 'Workbook'}
          icon={<Table size={13} className="shrink-0 text-muted-foreground" aria-hidden="true" />}
          metrics={[
            { label: 'Sheets', value: formatNumber(sheets.length) },
            { label: 'Source', value: 'Markdown' },
          ]}
        />
        <MetricStrip
          title={tabs[boundedActiveIndex]?.label || `Sheet ${boundedActiveIndex + 1}`}
          metrics={[
            { label: 'Preview', value: `${boundedActiveIndex + 1} of ${sheets.length}` },
          ]}
        />
      </div>
      <SheetTabs
        tabs={tabs}
        activeIndex={boundedActiveIndex}
        onChange={setActiveIndex}
        baseId={baseId}
      />
      <div
        id={`${baseId}-panel`}
        role="tabpanel"
        aria-labelledby={`${baseId}-tab-${boundedActiveIndex}`}
        className="min-h-0 flex-1 overflow-auto bg-background p-3 text-sm [&_.chat-markdown]:max-w-none [&_.chat-markdown_table]:w-max [&_.chat-markdown_table]:min-w-full [&_.chat-markdown_th]:whitespace-nowrap [&_.chat-markdown_td]:whitespace-nowrap"
      >
        <MarkdownRenderer content={activeSheet.content} />
      </div>
    </div>
  );
};

const SpreadsheetWorkbookPreview: React.FC<SpreadsheetWorkbookPreviewProps> = ({
  artifact,
  workbookUrl,
  sheetUrls,
  markdownUrls,
}) => {
  const [state, setState] = React.useState<PreviewState>({ status: 'loading' });

  React.useEffect(() => {
    let cancelled = false;
    const controller = new AbortController();

    setState({ status: 'loading' });

    const loadMarkdownFallback = async () => {
      if (markdownUrls.length === 0) {
        throw new Error('No spreadsheet preview sidecars are available.');
      }
      const markdownSheets = await Promise.all(markdownUrls.map((url) => fetchMarkdown(url, controller.signal)));
      if (!cancelled) setState({ status: 'markdown', sheets: markdownSheets });
    };

    (async () => {
      try {
        if (sheetUrls.length === 0) throw new Error('No JSON worksheet previews are available.');
        const [workbook, sheets] = await Promise.all([
          workbookUrl ? fetchJson<WorkbookPreviewJson>(workbookUrl, controller.signal) : Promise.resolve(null),
          Promise.all(sheetUrls.map((url) => fetchJson<SheetPreviewJson>(url, controller.signal))),
        ]);
        const usableSheets = sheets.filter(isUsableSheetPreview);
        if (usableSheets.length === 0) throw new Error('JSON worksheet previews were empty.');
        if (!cancelled) setState({ status: 'json', workbook, sheets: usableSheets });
      } catch (err: any) {
        if (cancelled || err?.name === 'AbortError') return;
        try {
          await loadMarkdownFallback();
        } catch (fallbackErr: any) {
          if (!cancelled && fallbackErr?.name !== 'AbortError') {
            setState({
              status: 'error',
              message: 'Download the original workbook from the header to inspect it in Excel.',
            });
          }
        }
      }
    })();

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [artifact.id, workbookUrl, sheetUrls.join('\n'), markdownUrls.join('\n')]);

  if (state.status === 'loading') return <LoadingState />;
  if (state.status === 'error') return <ErrorState message={state.message} />;
  if (state.status === 'markdown') return <MarkdownWorkbookPreview artifact={artifact} sheets={state.sheets} />;
  return <JsonWorkbookPreview artifact={artifact} workbook={state.workbook} sheets={state.sheets} />;
};

export default SpreadsheetWorkbookPreview;
