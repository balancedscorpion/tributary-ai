import Highcharts from 'highcharts';
import type { HighchartsModule } from '@/types';

export type HighchartsConstructorType = 'chart' | 'stockChart' | 'mapChart' | 'ganttChart';

export const STAGE2_EXTENSION_MODULES = [
  'highcharts-more',
  'drilldown',
  'heatmap',
  'tilemap',
  'treemap',
  'sunburst',
  'sankey',
  'organization',
  'dependency-wheel',
  'networkgraph',
  'wordcloud',
  'venn',
  'timeline',
  'xrange',
  'streamgraph',
  'item-series',
  'pareto',
  'dumbbell',
  'lollipop',
  'funnel',
  'histogram-bellcurve',
  'geoheatmap',
  'flowmap',
  'tiledwebmap',
] as const;

export type Stage2ExtensionModule = typeof STAGE2_EXTENSION_MODULES[number];

const MODULE_DEPENDENCIES: Record<Stage2ExtensionModule, Stage2ExtensionModule[]> = {
  'highcharts-more': [],
  'drilldown': [],
  'heatmap': [],
  'tilemap': ['heatmap'],
  'treemap': [],
  'sunburst': ['treemap'],
  'sankey': [],
  'organization': ['sankey'],
  'dependency-wheel': ['sankey'],
  'networkgraph': [],
  'wordcloud': [],
  'venn': [],
  'timeline': [],
  'xrange': [],
  'streamgraph': ['highcharts-more'],
  'item-series': [],
  'pareto': ['histogram-bellcurve'],
  'dumbbell': ['highcharts-more'],
  'lollipop': ['dumbbell'],
  'funnel': [],
  'histogram-bellcurve': [],
  'geoheatmap': ['heatmap'],
  'flowmap': [],
  'tiledwebmap': [],
};

const extensionImporters: Record<Stage2ExtensionModule, () => Promise<any>> = {
  'highcharts-more': () => import('highcharts/highcharts-more'),
  'drilldown': () => import('highcharts/modules/drilldown'),
  'heatmap': () => import('highcharts/modules/heatmap'),
  'tilemap': () => import('highcharts/modules/tilemap'),
  'treemap': () => import('highcharts/modules/treemap'),
  'sunburst': () => import('highcharts/modules/sunburst'),
  'sankey': () => import('highcharts/modules/sankey'),
  'organization': () => import('highcharts/modules/organization'),
  'dependency-wheel': () => import('highcharts/modules/dependency-wheel'),
  'networkgraph': () => import('highcharts/modules/networkgraph'),
  'wordcloud': () => import('highcharts/modules/wordcloud'),
  'venn': () => import('highcharts/modules/venn'),
  'timeline': () => import('highcharts/modules/timeline'),
  'xrange': () => import('highcharts/modules/xrange'),
  'streamgraph': () => import('highcharts/modules/streamgraph'),
  'item-series': () => import('highcharts/modules/item-series'),
  'pareto': () => import('highcharts/modules/pareto'),
  'dumbbell': () => import('highcharts/modules/dumbbell'),
  'lollipop': () => import('highcharts/modules/lollipop'),
  'funnel': () => import('highcharts/modules/funnel'),
  'histogram-bellcurve': () => import('highcharts/modules/histogram-bellcurve'),
  'geoheatmap': () => import('highcharts/modules/geoheatmap'),
  'flowmap': () => import('highcharts/modules/flowmap'),
  'tiledwebmap': () => import('highcharts/modules/tiledwebmap'),
};

const constructorByModule: Record<HighchartsModule, HighchartsConstructorType> = {
  core: 'chart',
  stock: 'stockChart',
  maps: 'mapChart',
  gantt: 'ganttChart',
};

const baseModuleImporters: Record<Exclude<HighchartsModule, 'core'>, () => Promise<any>> = {
  stock: () => import('highcharts/modules/stock'),
  maps: () => import('highcharts/modules/map'),
  gantt: () => import('highcharts/modules/gantt'),
};

const initializedBaseModules = new Set<Exclude<HighchartsModule, 'core'>>();
const initializedExtensionModules = new Set<Stage2ExtensionModule>();
const inFlightLoads = new Map<string, Promise<void>>();

function normalizeExtensionModuleName(value: unknown): Stage2ExtensionModule | null {
  if (typeof value !== 'string') return null;
  const normalized = value.trim().toLowerCase() as Stage2ExtensionModule;
  if (!STAGE2_EXTENSION_MODULES.includes(normalized)) return null;
  return normalized;
}

function resolveExtensionModuleOrder(inputModules: readonly Stage2ExtensionModule[]): Stage2ExtensionModule[] {
  const ordered: Stage2ExtensionModule[] = [];
  const visited = new Set<Stage2ExtensionModule>();
  const visiting = new Set<Stage2ExtensionModule>();

  const visit = (moduleName: Stage2ExtensionModule) => {
    if (visited.has(moduleName)) return;
    if (visiting.has(moduleName)) {
      throw new Error(`Cyclic Highcharts module dependency detected at ${moduleName}`);
    }
    visiting.add(moduleName);
    for (const dep of MODULE_DEPENDENCIES[moduleName] || []) {
      visit(dep);
    }
    visiting.delete(moduleName);
    visited.add(moduleName);
    ordered.push(moduleName);
  };

  for (const moduleName of inputModules) {
    visit(moduleName);
  }

  return ordered;
}

async function initializeImportedModule(loader: () => Promise<any>): Promise<void> {
  const moduleFactory = await loader();
  const init = (moduleFactory as any).default || moduleFactory;
  if (typeof init === 'function') {
    init(Highcharts);
  }
}

async function ensureBaseModule(moduleName: HighchartsModule): Promise<void> {
  if (moduleName === 'core') {
    return;
  }
  if (initializedBaseModules.has(moduleName)) {
    return;
  }

  const key = `base:${moduleName}`;
  if (!inFlightLoads.has(key)) {
    inFlightLoads.set(
      key,
      initializeImportedModule(baseModuleImporters[moduleName]).then(() => {
        initializedBaseModules.add(moduleName);
      }),
    );
  }

  try {
    await inFlightLoads.get(key);
  } finally {
    inFlightLoads.delete(key);
  }
}

async function ensureExtensionModule(moduleName: Stage2ExtensionModule): Promise<void> {
  if (initializedExtensionModules.has(moduleName)) {
    return;
  }

  const key = `ext:${moduleName}`;
  if (!inFlightLoads.has(key)) {
    inFlightLoads.set(
      key,
      initializeImportedModule(extensionImporters[moduleName]).then(() => {
        initializedExtensionModules.add(moduleName);
      }),
    );
  }

  try {
    await inFlightLoads.get(key);
  } finally {
    inFlightLoads.delete(key);
  }
}

export function resolveHighchartsConstructor(moduleName: HighchartsModule): HighchartsConstructorType {
  return constructorByModule[moduleName] || 'chart';
}

export function normalizeHighchartsExtensionModules(rawModules: unknown): Stage2ExtensionModule[] {
  if (!Array.isArray(rawModules)) return [];
  const deduped = new Set<Stage2ExtensionModule>();
  for (const rawModule of rawModules) {
    const normalized = normalizeExtensionModuleName(rawModule);
    if (!normalized) continue;
    deduped.add(normalized);
  }
  return [...deduped];
}

export async function loadHighchartsModules(moduleName: HighchartsModule, rawModules: unknown): Promise<Stage2ExtensionModule[]> {
  await ensureBaseModule(moduleName);

  const requestedModules = normalizeHighchartsExtensionModules(rawModules);
  if (requestedModules.length === 0) {
    return [];
  }

  const orderedModules = resolveExtensionModuleOrder(requestedModules);
  for (const extensionModule of orderedModules) {
    await ensureExtensionModule(extensionModule);
  }

  return orderedModules;
}
