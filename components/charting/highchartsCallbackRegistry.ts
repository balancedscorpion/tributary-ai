import type Highcharts from 'highcharts';

export type TooltipFormatter = Highcharts.TooltipFormatterCallbackFunction;

const compactNumber = new Intl.NumberFormat(undefined, {
  notation: 'compact',
  maximumFractionDigits: 1,
});

const usdNumber = new Intl.NumberFormat(undefined, {
  style: 'currency',
  currency: 'USD',
  maximumFractionDigits: 2,
});

const percentNumber = new Intl.NumberFormat(undefined, {
  style: 'percent',
  maximumFractionDigits: 1,
});

function valueToNumber(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  if (typeof value === 'string') {
    const trimmed = value.trim();
    if (!trimmed) return null;
    const n = Number(trimmed);
    if (Number.isFinite(n)) return n;
  }
  return null;
}

function formatHeader(ctx: Highcharts.Point): string {
  if (ctx.x === undefined || ctx.x === null) return '';
  if (typeof ctx.x === 'number') {
    const dt = new Date(ctx.x);
    if (!Number.isNaN(dt.getTime())) {
      return `<b>${dt.toLocaleString()}</b><br/>`;
    }
  }
  return `<b>${String(ctx.x)}</b><br/>`;
}

const tooltipFormatters: Record<string, TooltipFormatter> = {
  tooltip_number_compact: function numberCompactFormatter(this: Highcharts.Point): string {
    const n = valueToNumber(this.y);
    const label = this.series?.name ? `${this.series.name}: ` : '';
    return `${formatHeader(this)}${label}<b>${n === null ? '-' : compactNumber.format(n)}</b>`;
  },
  tooltip_currency_usd: function currencyUSDFormatter(this: Highcharts.Point): string {
    const n = valueToNumber(this.y);
    const label = this.series?.name ? `${this.series.name}: ` : '';
    return `${formatHeader(this)}${label}<b>${n === null ? '-' : usdNumber.format(n)}</b>`;
  },
  tooltip_percent_1dp: function percentFormatter(this: Highcharts.Point): string {
    const n = valueToNumber(this.y);
    const label = this.series?.name ? `${this.series.name}: ` : '';
    const rendered = n === null ? '-' : percentNumber.format(n);
    return `${formatHeader(this)}${label}<b>${rendered}</b>`;
  },
  tooltip_datetime_short: function datetimeShortFormatter(this: Highcharts.Point): string {
    const n = valueToNumber(this.y);
    const label = this.series?.name ? `${this.series.name}: ` : '';
    return `${formatHeader(this)}${label}<b>${n === null ? '-' : String(n)}</b>`;
  },
};

export function getTooltipFormatterByID(id: string): TooltipFormatter | undefined {
  const key = String(id || '').trim();
  if (!key) return undefined;
  return tooltipFormatters[key];
}

export function listHighchartsCallbackIDs(): string[] {
  return Object.keys(tooltipFormatters).sort();
}
