type EchartsTooltipFormatter = (params: any) => string;
type EchartsSeriesRenderItem = (params: any, api: any) => any;

const formatCompact = (value: unknown): string => {
  const numeric = typeof value === 'number' ? value : Number(value);
  if (!Number.isFinite(numeric)) return String(value ?? '');
  return new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 }).format(numeric);
};

const formatCurrency = (value: unknown): string => {
  const numeric = typeof value === 'number' ? value : Number(value);
  if (!Number.isFinite(numeric)) return String(value ?? '');
  return new Intl.NumberFormat(undefined, {
    style: 'currency',
    currency: 'USD',
    maximumFractionDigits: 2,
  }).format(numeric);
};

const formatPercent = (value: unknown): string => {
  const numeric = typeof value === 'number' ? value : Number(value);
  if (!Number.isFinite(numeric)) return String(value ?? '');
  return `${(numeric * 100).toFixed(1)}%`;
};

const normalizeParams = (params: any): any[] => {
  return Array.isArray(params) ? params : [params];
};

const tooltipFormatters: Record<string, EchartsTooltipFormatter> = {
  echarts_tooltip_compact: (params) => {
    const rows = normalizeParams(params);
    return rows
      .map((entry) => `${entry.marker || ''}${entry.seriesName || ''}: ${formatCompact((entry as any).value)}`)
      .join('<br/>');
  },
  echarts_tooltip_currency: (params) => {
    const rows = normalizeParams(params);
    return rows
      .map((entry) => `${entry.marker || ''}${entry.seriesName || ''}: ${formatCurrency((entry as any).value)}`)
      .join('<br/>');
  },
  echarts_tooltip_percent_1dp: (params) => {
    const rows = normalizeParams(params);
    return rows
      .map((entry) => `${entry.marker || ''}${entry.seriesName || ''}: ${formatPercent((entry as any).value)}`)
      .join('<br/>');
  },
};

const customRenderRect: EchartsSeriesRenderItem = (_params, api) => {
  const x = Number(api.value(0) ?? 0);
  const y = Number(api.value(1) ?? 0);
  const point = api.coord([x, y]);
  const size = api.size([1, 1]);
  const width = Math.max(2, Number.isFinite(size?.[0]) ? size[0] * 0.8 : 10);
  const height = Math.max(2, Number.isFinite(size?.[1]) ? size[1] * 0.8 : 10);

  return {
    type: 'rect',
    shape: {
      x: point[0] - (width / 2),
      y: point[1] - height,
      width,
      height,
    },
    style: api.style(),
  };
};

const customSeriesRenderers: Record<string, EchartsSeriesRenderItem> = {
  custom_render_rect: customRenderRect,
};

export const getEchartsTooltipFormatterByID = (id: string): EchartsTooltipFormatter | null => {
  const key = String(id || '').trim();
  if (!key) return null;
  return tooltipFormatters[key] || null;
};

export const getEchartsSeriesRenderItemByID = (id: string): EchartsSeriesRenderItem | null => {
  const key = String(id || '').trim();
  if (!key) return null;
  return customSeriesRenderers[key] || null;
};
