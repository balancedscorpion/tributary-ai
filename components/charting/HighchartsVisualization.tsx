import React from 'react';
import Highcharts from 'highcharts';
import HighchartsReact from 'highcharts-react-official';
import { AlertTriangle } from 'lucide-react';
import { sanitizeHighchartsPlotSpec, type SanitizedHighchartsSpec } from './highchartsSpecSanitizer';
import {
  loadHighchartsModules,
  resolveHighchartsConstructor,
  type HighchartsConstructorType,
} from './HighchartsModuleLoader';
import { attachHighchartsDrilldownController } from './highchartsDrilldownController';

interface HighchartsVisualizationProps {
  plotSpec: unknown;
  className?: string;
  title?: string;
}

const HIGHCHARTS_DARK_BASE_OPTIONS: Highcharts.Options = {
  chart: {
    backgroundColor: '#0b1220',
    style: {
      color: '#e2e8f0',
    },
  },
  title: {
    style: {
      color: '#f8fafc',
    },
  },
  subtitle: {
    style: {
      color: '#cbd5e1',
    },
  },
  xAxis: {
    gridLineColor: 'rgba(148, 163, 184, 0.2)',
    lineColor: 'rgba(148, 163, 184, 0.35)',
    tickColor: 'rgba(148, 163, 184, 0.35)',
    labels: {
      style: {
        color: '#cbd5e1',
      },
    },
    title: {
      style: {
        color: '#e2e8f0',
      },
    },
  },
  yAxis: {
    gridLineColor: 'rgba(148, 163, 184, 0.2)',
    lineColor: 'rgba(148, 163, 184, 0.35)',
    tickColor: 'rgba(148, 163, 184, 0.35)',
    labels: {
      style: {
        color: '#cbd5e1',
      },
    },
    title: {
      style: {
        color: '#e2e8f0',
      },
    },
  },
  legend: {
    layout: 'vertical',
    align: 'right',
    verticalAlign: 'middle',
    itemDistance: 10,
    itemMarginTop: 2,
    itemMarginBottom: 2,
    itemWidth: 220,
    navigation: {
      enabled: true,
    },
    itemStyle: {
      color: '#e2e8f0',
      textOverflow: 'ellipsis',
      whiteSpace: 'nowrap',
    },
    itemHoverStyle: {
      color: '#f8fafc',
    },
    itemHiddenStyle: {
      color: '#64748b',
    },
  },
  tooltip: {
    backgroundColor: 'rgba(15, 23, 42, 0.95)',
    borderColor: 'rgba(148, 163, 184, 0.35)',
    style: {
      color: '#f8fafc',
    },
    useHTML: false,
    outside: false,
  },
  credits: {
    enabled: false,
  },
  rangeSelector: {
    buttonTheme: {
      fill: '#1e293b',
      stroke: '#334155',
      style: {
        color: '#e2e8f0',
      },
      states: {
        hover: {
          fill: '#334155',
          style: {
            color: '#f8fafc',
          },
        },
        select: {
          fill: '#0ea5e9',
          style: {
            color: '#001018',
          },
        },
      },
    },
    inputBoxBorderColor: '#334155',
    inputStyle: {
      backgroundColor: '#0f172a',
      color: '#e2e8f0',
    },
    labelStyle: {
      color: '#94a3b8',
    },
  },
  navigator: {
    maskFill: 'rgba(14, 165, 233, 0.2)',
    outlineColor: '#334155',
    xAxis: {
      gridLineColor: 'rgba(148, 163, 184, 0.2)',
      lineColor: 'rgba(148, 163, 184, 0.35)',
      tickColor: 'rgba(148, 163, 184, 0.35)',
      labels: {
        style: {
          color: '#94a3b8',
        },
      },
    },
  },
  scrollbar: {
    barBackgroundColor: '#334155',
    barBorderColor: '#334155',
    buttonArrowColor: '#e2e8f0',
    buttonBackgroundColor: '#1e293b',
    buttonBorderColor: '#334155',
    rifleColor: '#94a3b8',
    trackBackgroundColor: '#0f172a',
    trackBorderColor: '#334155',
  },
};

function withDarkThemeDefaults(options: Highcharts.Options): Highcharts.Options {
  return Highcharts.merge({}, HIGHCHARTS_DARK_BASE_OPTIONS, options) as Highcharts.Options;
}

function looksPlaceholderTitle(value: string): boolean {
  const normalized = value.trim().toLowerCase();
  return normalized === 'chart title' || normalized === 'title' || normalized === 'untitled chart';
}

function resolveChartTitle(options: Highcharts.Options, preferredTitle?: string): string {
  const preferred = String(preferredTitle || '').trim();
  const optionTitle = String((options.title as any)?.text || '').trim();
  if (optionTitle && !looksPlaceholderTitle(optionTitle)) {
    return optionTitle;
  }
  if (preferred) {
    return preferred;
  }
  const series = Array.isArray(options.series) ? options.series : [];
  for (const entry of series) {
    const name = String((entry as any)?.name || '').trim();
    if (name) return `${name} Overview`;
  }
  return 'Data Chart';
}

function stableSerialize(value: unknown): string {
  const seen = new WeakSet<object>();
  const normalize = (input: unknown): unknown => {
    if (Array.isArray(input)) {
      return input.map(normalize);
    }
    if (!input || typeof input !== 'object') {
      return input;
    }
    const typed = input as Record<string, unknown>;
    if (seen.has(typed)) {
      return '[circular]';
    }
    seen.add(typed);
    const sortedEntries = Object.keys(typed)
      .sort()
      .map((key) => [key, normalize(typed[key])] as const);
    return Object.fromEntries(sortedEntries);
  };

  try {
    return JSON.stringify(normalize(value));
  } catch {
    return '';
  }
}

function readPositiveSize(value: unknown): number | undefined {
  if (typeof value !== 'number' || !Number.isFinite(value)) return undefined;
  if (value <= 0) return undefined;
  return Math.round(value);
}

function getRequestedChartSize(options: Highcharts.Options | undefined): { width?: number; height?: number } {
  const chartOptions = (options?.chart || {}) as Highcharts.ChartOptions;
  return {
    width: readPositiveSize(chartOptions.width),
    height: readPositiveSize(chartOptions.height),
  };
}

const HighchartsVisualizationComponent: React.FC<HighchartsVisualizationProps> = ({ plotSpec, className, title }) => {
  const incomingSanitized = React.useMemo(() => sanitizeHighchartsPlotSpec(plotSpec), [plotSpec]);
  const [stableSanitized, setStableSanitized] = React.useState<SanitizedHighchartsSpec | null>(
    incomingSanitized.ok ? incomingSanitized : null,
  );
  const effectiveSanitized = incomingSanitized.ok ? incomingSanitized : stableSanitized;

  const chartHostRef = React.useRef<HTMLDivElement | null>(null);
  const highchartsRef = React.useRef<any>(null);
  const [containerSize, setContainerSize] = React.useState({ width: 0, height: 0 });
  const [moduleReady, setModuleReady] = React.useState(false);
  const [moduleError, setModuleError] = React.useState<string | null>(null);
  const [loadedModules, setLoadedModules] = React.useState<string[]>([]);
  const [runtimeWarnings, setRuntimeWarnings] = React.useState<string[]>([]);
  const requestedChartSize = React.useMemo(
    () => getRequestedChartSize(effectiveSanitized?.options),
    [effectiveSanitized?.options],
  );
  const resolvedChartSize = React.useMemo(() => ({
    width: Math.max(320, containerSize.width, requestedChartSize.width ?? 0),
    height: Math.max(220, containerSize.height, requestedChartSize.height ?? 0),
  }), [containerSize.height, containerSize.width, requestedChartSize.height, requestedChartSize.width]);

  React.useEffect(() => {
    if (!incomingSanitized.ok) return;
    setStableSanitized((current) => {
      if (!current) return incomingSanitized;
      const currentSignature = stableSerialize({
        module: current.module,
        modules: current.modules,
        options: current.options,
        extensions: current.extensions,
      });
      const nextSignature = stableSerialize({
        module: incomingSanitized.module,
        modules: incomingSanitized.modules,
        options: incomingSanitized.options,
        extensions: incomingSanitized.extensions,
      });
      if (current.schemaVersion === incomingSanitized.schemaVersion && currentSignature === nextSignature) {
        return current;
      }
      return incomingSanitized;
    });
  }, [incomingSanitized]);

  React.useEffect(() => {
    const el = chartHostRef.current;
    if (!el) return;
    const update = () => {
      const rect = el.getBoundingClientRect();
      const next = {
        width: Math.max(1, Math.round(rect.width)),
        height: Math.max(1, Math.round(rect.height)),
      };
      setContainerSize((prev) => (prev.width === next.width && prev.height === next.height ? prev : next));
    };
    update();
    const observer = new ResizeObserver(update);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  React.useEffect(() => {
    let cancelled = false;

    if (!effectiveSanitized?.ok) {
      setModuleReady(false);
      setModuleError(null);
      setLoadedModules([]);
      return () => {
        cancelled = true;
      };
    }

    setModuleReady(false);
    setModuleError(null);

    void loadHighchartsModules(effectiveSanitized.module, effectiveSanitized.modules)
      .then((resolvedModules) => {
        if (cancelled) return;
        setLoadedModules(resolvedModules);
        setModuleReady(true);
      })
      .catch((error) => {
        if (cancelled) return;
        setModuleError(`Failed to load Highcharts modules: ${String(error)}`);
      });

    return () => {
      cancelled = true;
    };
  }, [effectiveSanitized?.ok, effectiveSanitized?.module, stableSerialize(effectiveSanitized?.modules)]);

  React.useEffect(() => {
    setRuntimeWarnings([]);
  }, [effectiveSanitized?.schemaVersion, effectiveSanitized?.module, stableSerialize(effectiveSanitized?.extensions)]);

  React.useEffect(() => {
    if (!moduleReady || !effectiveSanitized?.ok) return;
    const chart = highchartsRef.current?.chart as Highcharts.Chart | undefined;
    if (!chart) return;

    return attachHighchartsDrilldownController(chart, {
      drilldownAsync: effectiveSanitized.extensions?.drilldown_async,
      onWarning: (message) => {
        setRuntimeWarnings((prev) => (prev.includes(message) ? prev : [...prev, message]));
      },
    });
  }, [moduleReady, effectiveSanitized, highchartsRef]);

  React.useEffect(() => {
    const chart = highchartsRef.current?.chart as Highcharts.Chart | undefined;
    if (!chart) return;
    if (resolvedChartSize.width <= 0 || resolvedChartSize.height <= 0) return;
    chart.setSize(resolvedChartSize.width, resolvedChartSize.height, false);
  }, [resolvedChartSize.height, resolvedChartSize.width]);

  const themedOptions = React.useMemo(() => {
    if (!effectiveSanitized?.ok) return {};
    const base = withDarkThemeDefaults(effectiveSanitized.options);
    const resolvedTitle = resolveChartTitle(base, title);

    const legendMaxHeight = containerSize.height > 0 ? Math.max(96, Math.floor(containerSize.height * 0.24)) : undefined;
    const verticalLegendWidth = containerSize.width > 0 ? Math.max(150, Math.min(250, Math.floor(containerSize.width * 0.24))) : 220;
    const compactLegendWidth = containerSize.width > 0 ? Math.max(100, Math.min(180, Math.floor(containerSize.width * 0.2))) : 140;

    const isCompact = containerSize.width > 0 && containerSize.width < 980;

    return Highcharts.merge({}, base, {
      title: {
        text: resolvedTitle,
      },
      chart: {
        animation: false,
        reflow: true,
        width: resolvedChartSize.width,
        height: resolvedChartSize.height,
        spacingBottom: isCompact ? 24 : 12,
        spacingLeft: 12,
        spacingRight: 12,
        spacingTop: 12,
      },
      legend: {
        layout: isCompact ? 'horizontal' : 'vertical',
        align: isCompact ? 'center' : 'right',
        verticalAlign: isCompact ? 'bottom' : 'middle',
        itemWidth: isCompact ? compactLegendWidth : verticalLegendWidth,
        maxHeight: legendMaxHeight,
        navigation: {
          enabled: true,
        },
      },
      responsive: {
        rules: [
          {
            condition: {
              maxWidth: 720,
            },
            chartOptions: {
              legend: {
                layout: 'horizontal',
                align: 'center',
                verticalAlign: 'bottom',
                itemWidth: 110,
              },
            },
          },
        ],
      },
    }) as Highcharts.Options;
  }, [containerSize.height, containerSize.width, effectiveSanitized, resolvedChartSize.height, resolvedChartSize.width, title]);

  if (!effectiveSanitized?.ok) {
    return (
      <div className={`rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-xs text-destructive ${className || ''}`}>
        <div className="flex items-center gap-2 font-medium">
          <AlertTriangle size={14} />
          Invalid chart specification
        </div>
        <div className="mt-2">{incomingSanitized.error || 'Unable to render chart spec.'}</div>
      </div>
    );
  }

  if (moduleError) {
    return (
      <div className={`rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-xs text-destructive ${className || ''}`}>
        <div className="flex items-center gap-2 font-medium">
          <AlertTriangle size={14} />
          Chart module load failed
        </div>
        <div className="mt-2">{moduleError}</div>
      </div>
    );
  }

  if (!moduleReady) {
    return (
      <div className={`rounded-lg border border-border bg-muted/20 p-3 text-xs text-muted-foreground ${className || ''}`}>
        Loading chart module...
      </div>
    );
  }

  const warnings = [...effectiveSanitized.warnings, ...runtimeWarnings];
  const constructorType = resolveHighchartsConstructor(effectiveSanitized.module) as HighchartsConstructorType;

  return (
    <div className={`flex min-h-0 flex-col overflow-hidden ${className || ''}`} data-testid="highcharts-visualization">
      {warnings.length > 0 && (
        <div className="mb-2 rounded-md border border-amber-300/50 bg-amber-100/40 px-2 py-1 text-[11px] text-amber-900">
          {warnings.join(' ')}
        </div>
      )}
      <div ref={chartHostRef} className="relative w-full min-h-0 flex-1 overflow-auto rounded-md border border-border/40 bg-background/40">
        <div
          style={{
            width: `${resolvedChartSize.width}px`,
            height: `${resolvedChartSize.height}px`,
            minWidth: '100%',
            minHeight: '100%',
          }}
        >
          <HighchartsReact
            ref={highchartsRef}
            highcharts={Highcharts}
            constructorType={constructorType}
            options={themedOptions}
            containerProps={{ style: { width: '100%', maxWidth: '100%', height: '100%', maxHeight: '100%', overflow: 'hidden' } }}
            immutable={false}
            updateArgs={[true, true, false]}
          />
        </div>
      </div>
      {loadedModules.length > 0 && (
        <div className="mt-2 text-[10px] text-muted-foreground" data-testid="highcharts-loaded-modules">
          Modules: {loadedModules.join(', ')}
        </div>
      )}
    </div>
  );
};

const HighchartsVisualization = React.memo(
  HighchartsVisualizationComponent,
  (prev, next) =>
    String(prev.className || '') === String(next.className || '')
    && String(prev.title || '') === String(next.title || '')
    && stableSerialize(prev.plotSpec) === stableSerialize(next.plotSpec),
);

export default HighchartsVisualization;
