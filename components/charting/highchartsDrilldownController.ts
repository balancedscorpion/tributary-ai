import Highcharts from 'highcharts';

export interface HighchartsAsyncDrilldownConfig {
  loader_id: string;
  series_by_id: Record<string, Record<string, any>>;
  max_depth?: number;
}

interface DrilldownControllerConfig {
  drilldownAsync?: HighchartsAsyncDrilldownConfig;
  onWarning?: (message: string) => void;
}

function asSeriesOptionsType(value: Record<string, any>): Highcharts.SeriesOptionsType {
  return value as unknown as Highcharts.SeriesOptionsType;
}

function clampDepth(value: unknown): number {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) return 4;
  if (parsed < 1) return 1;
  if (parsed > 8) return 8;
  return Math.trunc(parsed);
}

export function attachHighchartsDrilldownController(
  chart: Highcharts.Chart,
  config: DrilldownControllerConfig,
): () => void {
  const drilldownAsync = config.drilldownAsync;
  if (!drilldownAsync || typeof drilldownAsync !== 'object') {
    return () => {};
  }

  const seriesByID = drilldownAsync.series_by_id || {};
  const maxDepth = clampDepth(drilldownAsync.max_depth);
  const emittedWarnings = new Set<string>();

  const warn = (message: string) => {
    if (!message) return;
    if (emittedWarnings.has(message)) return;
    emittedWarnings.add(message);
    config.onWarning?.(message);
  };

  const removeDrilldown = (Highcharts as any).addEvent(chart, 'drilldown', (event: any) => {
    if (!event || event.seriesOptions) {
      return;
    }

    const drilldownID = String(event.point?.drilldown || '').trim();
    if (!drilldownID) {
      return;
    }

    const depth = Array.isArray((chart as any).drilldownLevels) ? (chart as any).drilldownLevels.length : 0;
    if (depth >= maxDepth) {
      warn(`Maximum drilldown depth (${maxDepth}) reached.`);
      return;
    }

    const nextSeries = seriesByID[drilldownID];
    if (!nextSeries || typeof nextSeries !== 'object') {
      warn(`No async drilldown series available for id "${drilldownID}".`);
      return;
    }

    if (typeof (chart as any).addSeriesAsDrilldown !== 'function') {
      warn('Highcharts drilldown API is unavailable for async drilldown.');
      return;
    }

    try {
      (chart as any).addSeriesAsDrilldown(event.point, asSeriesOptionsType(nextSeries));
      if (typeof (chart as any).applyDrilldown === 'function') {
        (chart as any).applyDrilldown();
      }
    } catch (error) {
      warn(`Failed to apply async drilldown for "${drilldownID}": ${String(error)}`);
    }
  });

  return () => {
    if (typeof removeDrilldown === 'function') {
      removeDrilldown();
    }
  };
}
