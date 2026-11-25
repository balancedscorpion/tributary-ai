export interface EchartsAsyncDrilldownConfig {
  loader_id: string;
  series_by_id: Record<string, Record<string, any>>;
  max_depth?: number;
}

export interface EchartsDrilldownState {
  stack: Array<Array<Record<string, any>>>;
  currentSeries: Array<Record<string, any>> | null;
}

export interface EchartsDrilldownStep {
  state: EchartsDrilldownState;
  warning?: string;
}

const DEFAULT_MAX_DEPTH = 4;

export function createEchartsDrilldownState(): EchartsDrilldownState {
  return {
    stack: [],
    currentSeries: null,
  };
}

function clampDepth(value: unknown): number {
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) return DEFAULT_MAX_DEPTH;
  if (parsed < 1) return 1;
  if (parsed > 8) return 8;
  return Math.trunc(parsed);
}

function cloneSeriesList(series: Array<Record<string, any>>): Array<Record<string, any>> {
  return series.map((entry) => ({ ...entry }));
}

function normalizeSeriesPayload(raw: unknown): Array<Record<string, any>> | null {
  if (Array.isArray(raw)) {
    const out = raw.filter((entry): entry is Record<string, any> => !!entry && typeof entry === 'object' && !Array.isArray(entry));
    return out.length > 0 ? cloneSeriesList(out) : null;
  }
  if (raw && typeof raw === 'object' && !Array.isArray(raw)) {
    return [raw as Record<string, any>];
  }
  return null;
}

function resolveDrilldownID(params: any): string {
  const candidates: unknown[] = [
    params?.data?.drilldown,
    params?.data?.drilldown_id,
    params?.data?.drilldownId,
    params?.value?.drilldown,
    params?.value?.drilldown_id,
    params?.value?.drilldownId,
    params?.drilldown,
  ];
  for (const candidate of candidates) {
    const id = String(candidate || '').trim();
    if (id) return id;
  }
  return '';
}

export function resolveActiveSeries(
  state: EchartsDrilldownState,
  baseSeries: Array<Record<string, any>>,
): Array<Record<string, any>> {
  if (state.currentSeries && state.currentSeries.length > 0) {
    return state.currentSeries;
  }
  return baseSeries;
}

export function canStepBackDrilldown(state: EchartsDrilldownState): boolean {
  return state.stack.length > 0;
}

export function applyEchartsDrilldownClick(
  state: EchartsDrilldownState,
  params: any,
  config: EchartsAsyncDrilldownConfig | undefined,
  baseSeries: Array<Record<string, any>>,
): EchartsDrilldownStep {
  if (!config) {
    return { state };
  }
  if (String(config.loader_id || '').trim() !== 'drilldown_embedded_series') {
    return {
      state,
      warning: `Unsupported drilldown loader_id: ${String(config.loader_id || '').trim() || '(missing)'}.`,
    };
  }
  const drilldownID = resolveDrilldownID(params);
  if (!drilldownID) {
    return { state };
  }

  const maxDepth = clampDepth(config.max_depth);
  if (state.stack.length >= maxDepth) {
    return {
      state,
      warning: `Maximum drilldown depth (${maxDepth}) reached.`,
    };
  }

  const nextRaw = config.series_by_id?.[drilldownID];
  const nextSeries = normalizeSeriesPayload(nextRaw);
  if (!nextSeries) {
    return {
      state,
      warning: `No drilldown series found for id "${drilldownID}".`,
    };
  }

  const currentSeries = resolveActiveSeries(state, baseSeries);
  const nextState: EchartsDrilldownState = {
    stack: [...state.stack, cloneSeriesList(currentSeries)],
    currentSeries: nextSeries,
  };
  return { state: nextState };
}

export function stepBackEchartsDrilldown(state: EchartsDrilldownState): EchartsDrilldownStep {
  if (state.stack.length === 0) {
    return { state };
  }

  const nextStack = [...state.stack];
  const previousSeries = nextStack.pop() || null;
  return {
    state: {
      stack: nextStack,
      currentSeries: previousSeries,
    },
  };
}
