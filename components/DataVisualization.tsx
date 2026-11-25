import React, { useState, useMemo, useEffect, useRef, useId, useCallback } from "react";
import {
  BarChart,
  Bar,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  Legend,
  AreaChart,
  Area,
} from "recharts";
import {
  BarChart2,
  Table as TableIcon,
  ChevronLeft,
  ChevronRight,
  Search,
  ArrowUpDown,
  ArrowUp,
  ArrowDown,
  Download,
  Maximize2,
  Minimize2,
  PanelBottomOpen,
  PanelBottomClose,
  AlertTriangle,
} from "lucide-react";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { ScrollArea, ScrollBar } from "@/components/ui/scroll-area";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { ChartBudgetReport } from "@/types";
import EchartsVisualization from "@/components/charting/EchartsVisualization";
import HighchartsVisualization from "@/components/charting/HighchartsVisualization";
import ChartRenderErrorBoundary from "@/components/charting/ChartRenderErrorBoundary";

interface DataVisualizationProps {
  dataString: string;
  title?: string;
  className?: string;
  plotSpec?: unknown;
  chartEngine?: string;
  artifactId?: string;
  partial?: boolean;
  fullArtifactId?: string;
  rowCountTotal?: number;
  downsampleRatio?: number;
  chartBudget?: ChartBudgetReport;
  dimensionFiltersEnabled?: boolean;
}

type SortDirection = "asc" | "desc";
interface SortConfig {
  key: string;
  direction: SortDirection;
}

type SeriesDef = { key: string; color: string };

const NUMERIC_LIKE_RE = /^[+-]?(?:\d+\.?\d*|\d*\.?\d+)(?:[eE][+-]?\d+)?$/;

function toFiniteNumber(value: unknown): number | null {
  if (typeof value === "number") return Number.isFinite(value) ? value : null;
  if (typeof value === "string") {
    const s = value.trim();
    if (!s) return null;
    if (!NUMERIC_LIKE_RE.test(s)) return null;
    const n = Number(s);
    return Number.isFinite(n) ? n : null;
  }
  return null;
}

function isPlainObject(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function filterPlotSpecByDimension(
  spec: unknown,
  filters: Record<string, string>,
): unknown {
  if (!spec || typeof spec !== "object") return spec;
  const activeFilters = Object.entries(filters).filter(
    ([, v]) => v !== "" && v !== undefined && v !== "__all__",
  );
  if (activeFilters.length === 0) return spec;
  const root = spec as Record<string, any>;
  const options = root.options;
  if (!options || typeof options !== "object") return spec;
  const series = (options as Record<string, any>).series;
  if (!Array.isArray(series)) return spec;
  // Check if series are keyed by a dimension value (color-coded series)
  // If any active filter column matches series names, filter series by name
  const activeValues = new Set(activeFilters.map(([, v]) => v));
  const seriesNames = series
    .map((s: any) => String(s?.name ?? "").trim())
    .filter(Boolean);
  const hasMatchingNames = seriesNames.some((n: string) => activeValues.has(n));
  if (hasMatchingNames) {
    const filteredSeries = series.filter((s: any) => {
      const name = String(s?.name ?? "").trim();
      if (!name) return true;
      return activeValues.has(name);
    });
    return {
      ...root,
      options: { ...(options as Record<string, any>), series: filteredSeries },
    };
  }
  // Per-point data filtering: filter data arrays inside each series
  const filteredSeries = series.map((s: any) => {
    if (!s || !Array.isArray(s.data)) return s;
    const filteredPoints = s.data.filter((point: any) => {
      if (!point || typeof point !== "object" || Array.isArray(point))
        return true;
      return activeFilters.every(
        ([col, val]) => String(point[col] ?? "").trim() === val,
      );
    });
    return { ...s, data: filteredPoints };
  });
  return {
    ...root,
    options: { ...(options as Record<string, any>), series: filteredSeries },
  };
}

type FilterChartRebuildInput = {
  kind: "scatter" | "line" | "bar";
  engine: "echarts" | "highcharts";
  x: string;
  y: string;
  color?: string;
  size?: string;
};

function normalizeChartKind(raw: unknown): "scatter" | "line" | "bar" | null {
  const kind = String(raw ?? "")
    .trim()
    .toLowerCase();
  if (kind === "scatter" || kind === "line" || kind === "bar") return kind;
  if (kind === "column") return "bar";
  if (kind === "bubble") return "scatter";
  return null;
}

function axisNameFromSpec(axisRaw: unknown): string {
  if (isPlainObject(axisRaw)) return String(axisRaw.name ?? "").trim();
  if (Array.isArray(axisRaw) && isPlainObject(axisRaw[0])) {
    return String((axisRaw[0] as Record<string, any>).name ?? "").trim();
  }
  return "";
}

function axisCategoryLabelsFromSpec(axisRaw: unknown): string[] {
  let axis: Record<string, any> | null = null;
  if (isPlainObject(axisRaw)) axis = axisRaw;
  if (Array.isArray(axisRaw) && isPlainObject(axisRaw[0])) {
    axis = axisRaw[0] as Record<string, any>;
  }
  if (!axis) return [];
  const data = Array.isArray(axis.data)
    ? axis.data
    : Array.isArray(axis.categories)
      ? axis.categories
      : [];
  return data
    .map((item: unknown) => String(item ?? "").trim())
    .filter(Boolean);
}

function inferFilterChartRebuildInput(
  spec: unknown,
  rows: Array<Record<string, any>>,
  engine: "echarts" | "highcharts",
): FilterChartRebuildInput | null {
  if (!isPlainObject(spec) || !Array.isArray(rows) || rows.length === 0) return null;
  const options = spec.options;
  if (!isPlainObject(options)) return null;
  const seriesRaw = Array.isArray(options.series)
    ? options.series.filter(isPlainObject)
    : isPlainObject(options.series)
      ? [options.series]
      : [];
  if (seriesRaw.length === 0) return null;

  const kind = normalizeChartKind(seriesRaw[0]?.type);
  if (!kind) return null;

  const keys = Object.keys(rows[0] ?? {});
  if (keys.length === 0) return null;

  const sampleSize = Math.min(rows.length, 200);
  const numericKeys = keys.filter((key) => {
    let numeric = 0;
    let nonEmpty = 0;
    for (let i = 0; i < sampleSize; i++) {
      const value = rows[i]?.[key];
      if (value === null || value === undefined) continue;
      if (String(value).trim() === "") continue;
      nonEmpty += 1;
      if (toFiniteNumber(value) !== null) numeric += 1;
    }
    return nonEmpty > 0 && numeric / nonEmpty >= 0.6;
  });

  const matchKeyByName = (target: string): string => {
    const lower = target.trim().toLowerCase();
    if (!lower) return "";
    const exact = keys.find((k) => k.toLowerCase() === lower);
    if (exact) return exact;
    return (
      keys.find(
        (k) =>
          k.toLowerCase().includes(lower) || lower.includes(k.toLowerCase()),
      ) || ""
    );
  };

  const matchKeyByCoverage = (labels: string[]): string => {
    if (labels.length === 0) return "";
    const labelSet = new Set(labels.map((v) => v.trim()));
    let bestKey = "";
    let bestMatches = 0;
    for (const key of keys) {
      let matches = 0;
      for (let i = 0; i < sampleSize; i++) {
        const value = String(rows[i]?.[key] ?? "").trim();
        if (value && labelSet.has(value)) matches += 1;
      }
      if (matches > bestMatches) {
        bestMatches = matches;
        bestKey = key;
      }
    }
    return bestMatches >= 2 ? bestKey : "";
  };

  let xKey = matchKeyByName(axisNameFromSpec(options.xAxis));
  if (!xKey) xKey = matchKeyByCoverage(axisCategoryLabelsFromSpec(options.xAxis));
  if (!xKey) {
    if (kind === "scatter") {
      xKey = numericKeys[0] || "";
    } else {
      xKey = keys.find((k) => !numericKeys.includes(k)) || keys[0] || "";
    }
  }

  let yKey = matchKeyByName(axisNameFromSpec(options.yAxis));
  if (!yKey) {
    const seriesName = String(seriesRaw[0]?.name ?? "").trim();
    if (seriesName) yKey = matchKeyByName(seriesName);
  }
  if (!yKey) {
    yKey = numericKeys.find((k) => k !== xKey) || "";
  }
  if (!xKey || !yKey) return null;

  let color = "";
  if (seriesRaw.length > 1) {
    const seriesNames = seriesRaw
      .map((s) => String(s?.name ?? "").trim())
      .filter(Boolean);
    if (seriesNames.length > 1) {
      let bestKey = "";
      let bestCoverage = 0;
      for (const key of keys) {
        if (key === xKey || key === yKey || numericKeys.includes(key)) continue;
        const distinct = new Set<string>();
        for (let i = 0; i < sampleSize; i++) {
          const value = String(rows[i]?.[key] ?? "").trim();
          if (value) distinct.add(value);
        }
        let coverage = 0;
        for (const name of seriesNames) {
          if (distinct.has(name)) coverage += 1;
        }
        if (coverage > bestCoverage) {
          bestCoverage = coverage;
          bestKey = key;
        }
      }
      if (bestCoverage > 0) color = bestKey;
    }
  }

  return {
    kind,
    engine,
    x: xKey,
    y: yKey,
    ...(color ? { color } : {}),
  };
}

type CategoricalSeriesGroup = {
  name: string;
  data: Array<number | null>;
};

function buildCategoricalSeriesByColorRows(
  rows: Array<Record<string, any>>,
  xKey: string,
  yKey: string,
  colorKey: string,
): { categories: any[]; groups: CategoricalSeriesGroup[] } {
  const xOrder: string[] = [];
  const xValues = new Map<string, unknown>();
  const grouped = new Map<string, Map<string, number>>();

  for (const row of rows) {
    const rawX = row[xKey];
    const xLabel = String(rawX ?? "").trim();
    if (!xLabel) continue;
    if (!xValues.has(xLabel)) {
      xValues.set(xLabel, rawX);
      xOrder.push(xLabel);
    }

    let group = String(row[colorKey] ?? "").trim();
    if (!group) group = "(empty)";
    if (!grouped.has(group)) grouped.set(group, new Map<string, number>());
    const yValue = toFiniteNumber(row[yKey]);
    if (yValue === null) continue;
    const target = grouped.get(group)!;
    target.set(xLabel, (target.get(xLabel) ?? 0) + yValue);
  }

  const categories = xOrder.map((label) => xValues.get(label));
  const groupNames = Array.from(grouped.keys()).sort();
  const groups = groupNames.map((name) => {
    const sums = grouped.get(name)!;
    const data = xOrder.map((label) =>
      sums.has(label) ? (sums.get(label) as number) : null,
    );
    return { name, data };
  });

  return { categories, groups };
}

function buildEchartsPlotSpecFromRows(
  baseSpec: Record<string, any>,
  input: FilterChartRebuildInput,
  rows: Array<Record<string, any>>,
): Record<string, any> {
  const xKey = input.x;
  const yKey = input.y;
  const colorKey = String(input.color || "").trim();
  const sizeKey = String(input.size || "").trim();
  const kind = input.kind;

  const baseOptions = isPlainObject(baseSpec.options)
    ? { ...(baseSpec.options as Record<string, any>) }
    : {};
  const nextOptions: Record<string, any> = { ...baseOptions };

  if (kind === "scatter") {
    nextOptions.xAxis = { type: "value", name: xKey };
    nextOptions.yAxis = { type: "value", name: yKey };

    if (colorKey) {
      const grouped = new Map<string, any[]>();
      for (const row of rows) {
        const xv = toFiniteNumber(row[xKey]);
        const yv = toFiniteNumber(row[yKey]);
        if (xv === null || yv === null) continue;
        let group = String(row[colorKey] ?? "").trim();
        if (!group) group = "(empty)";
        if (!grouped.has(group)) grouped.set(group, []);
        if (sizeKey) {
          const sv = toFiniteNumber(row[sizeKey]);
          if (sv !== null) {
            grouped.get(group)!.push({
              value: [xv, yv],
              symbolSize: sv,
            });
            continue;
          }
        }
        grouped.get(group)!.push([xv, yv]);
      }
      const names = Array.from(grouped.keys()).sort();
      nextOptions.series = names.map((name) => ({
        type: "scatter",
        name,
        data: grouped.get(name) || [],
      }));
    } else {
      const points: any[] = [];
      for (const row of rows) {
        const xv = toFiniteNumber(row[xKey]);
        const yv = toFiniteNumber(row[yKey]);
        if (xv === null || yv === null) continue;
        if (sizeKey) {
          const sv = toFiniteNumber(row[sizeKey]);
          if (sv !== null) {
            points.push({ value: [xv, yv], symbolSize: sv });
            continue;
          }
        }
        points.push([xv, yv]);
      }
      nextOptions.series = [{ type: "scatter", name: yKey, data: points }];
    }
  } else {
    if (colorKey) {
      const { categories, groups } = buildCategoricalSeriesByColorRows(
        rows,
        xKey,
        yKey,
        colorKey,
      );
      nextOptions.xAxis = { type: "category", name: xKey, data: categories };
      nextOptions.yAxis = { type: "value", name: yKey };
      nextOptions.series = groups.map((group) => ({
        type: kind,
        name: group.name,
        data: group.data,
      }));
    } else {
      const categories = rows.map((row) => row[xKey]);
      const values = rows.map((row) => {
        const numeric = toFiniteNumber(row[yKey]);
        return numeric === null ? row[yKey] : numeric;
      });
      nextOptions.xAxis = { type: "category", name: xKey, data: categories };
      nextOptions.yAxis = { type: "value", name: yKey };
      nextOptions.series = [{ type: kind, name: yKey, data: values }];
    }
  }

  const series = Array.isArray(nextOptions.series) ? nextOptions.series : [];
  if (series.length > 1) {
    const legend = isPlainObject(nextOptions.legend)
      ? { ...(nextOptions.legend as Record<string, any>) }
      : {};
    legend.show = true;
    nextOptions.legend = legend;
  } else if (isPlainObject(nextOptions.legend)) {
    nextOptions.legend = {
      ...(nextOptions.legend as Record<string, any>),
      show: false,
    };
  }

  return {
    ...baseSpec,
    schema_version: baseSpec.schema_version || "echarts-plot-spec.v1",
    module: baseSpec.module || "all",
    options: nextOptions,
  };
}

function buildHighchartsPlotSpecFromRows(
  baseSpec: Record<string, any>,
  input: FilterChartRebuildInput,
  rows: Array<Record<string, any>>,
): Record<string, any> {
  const xKey = input.x;
  const yKey = input.y;
  const colorKey = String(input.color || "").trim();
  const sizeKey = String(input.size || "").trim();
  const kind = input.kind;

  const baseOptions = isPlainObject(baseSpec.options)
    ? { ...(baseSpec.options as Record<string, any>) }
    : {};
  const nextOptions: Record<string, any> = { ...baseOptions };

  nextOptions.chart = {
    ...(isPlainObject(nextOptions.chart)
      ? (nextOptions.chart as Record<string, any>)
      : {}),
    type: kind,
  };

  if (kind === "scatter") {
    nextOptions.xAxis = { title: { text: xKey } };
    nextOptions.yAxis = { title: { text: yKey } };

    if (colorKey) {
      const grouped = new Map<string, any[]>();
      for (const row of rows) {
        const xv = toFiniteNumber(row[xKey]);
        const yv = toFiniteNumber(row[yKey]);
        if (xv === null || yv === null) continue;
        let group = String(row[colorKey] ?? "").trim();
        if (!group) group = "(empty)";
        if (!grouped.has(group)) grouped.set(group, []);
        if (sizeKey) {
          const zv = toFiniteNumber(row[sizeKey]);
          if (zv !== null) {
            grouped.get(group)!.push({ x: xv, y: yv, z: zv });
            continue;
          }
        }
        grouped.get(group)!.push([xv, yv]);
      }
      const names = Array.from(grouped.keys()).sort();
      nextOptions.series = names.map((name) => ({
        type: sizeKey ? "bubble" : "scatter",
        name,
        data: grouped.get(name) || [],
      }));
    } else {
      const points: any[] = [];
      for (const row of rows) {
        const xv = toFiniteNumber(row[xKey]);
        const yv = toFiniteNumber(row[yKey]);
        if (xv === null || yv === null) continue;
        if (sizeKey) {
          const zv = toFiniteNumber(row[sizeKey]);
          if (zv !== null) {
            points.push({ x: xv, y: yv, z: zv });
            continue;
          }
        }
        points.push([xv, yv]);
      }
      nextOptions.series = [
        { type: sizeKey ? "bubble" : "scatter", name: yKey, data: points },
      ];
    }
  } else {
    if (colorKey) {
      const { categories, groups } = buildCategoricalSeriesByColorRows(
        rows,
        xKey,
        yKey,
        colorKey,
      );
      nextOptions.xAxis = { categories, title: { text: xKey } };
      nextOptions.yAxis = { title: { text: yKey } };
      nextOptions.series = groups.map((group) => ({
        type: kind,
        name: group.name,
        data: group.data,
      }));
    } else {
      const categories = rows.map((row) => row[xKey]);
      const values = rows.map((row) => {
        const numeric = toFiniteNumber(row[yKey]);
        return numeric === null ? row[yKey] : numeric;
      });
      nextOptions.xAxis = { categories, title: { text: xKey } };
      nextOptions.yAxis = { title: { text: yKey } };
      nextOptions.series = [{ type: kind, name: yKey, data: values }];
    }
  }

  return {
    ...baseSpec,
    schema_version: baseSpec.schema_version || "highcharts-plot-spec.v2",
    module: baseSpec.module || "core",
    options: nextOptions,
  };
}

function rebuildPlotSpecFromRows(
  spec: unknown,
  input: FilterChartRebuildInput | null,
  rows: Array<Record<string, any>>,
): unknown {
  if (!isPlainObject(spec) || !input || !Array.isArray(rows)) {
    return spec;
  }
  const kind = normalizeChartKind(input.kind) || "scatter";
  const engine = String(input.engine || "").trim().toLowerCase();
  const x = String(input.x || "").trim();
  const y = String(input.y || "").trim();
  if (!x || !y) return spec;

  const normalizedInput: FilterChartRebuildInput = {
    ...input,
    kind,
    engine: engine === "highcharts" ? "highcharts" : "echarts",
    x,
    y,
  };

  if (normalizedInput.engine === "highcharts") {
    return buildHighchartsPlotSpecFromRows(
      spec as Record<string, any>,
      normalizedInput,
      rows,
    );
  }
  return buildEchartsPlotSpecFromRows(
    spec as Record<string, any>,
    normalizedInput,
    rows,
  );
}

function formatCompactInt(value: number): string {
  if (!Number.isFinite(value)) return "0";
  return Math.max(0, Math.trunc(value)).toLocaleString();
}

const InteractiveLegend: React.FC<{
  series: SeriesDef[];
  hiddenSeries: Record<string, boolean>;
  onToggle: (key: string) => void;
  onShowAll: () => void;
}> = ({ series, hiddenSeries, onToggle, onShowAll }) => {
  if (series.length === 0) return null;

  const anyHidden = series.some((s) => hiddenSeries[s.key]);

  return (
    <div className="flex flex-wrap items-center justify-center gap-x-4 gap-y-2 pt-2 text-xs">
      {series.map((s) => {
        const isHidden = !!hiddenSeries[s.key];
        return (
          <button
            key={s.key}
            type="button"
            onClick={() => onToggle(s.key)}
            aria-pressed={!isHidden}
            title={isHidden ? "Click to show series" : "Click to hide series"}
            className={[
              "inline-flex items-center gap-2 select-none",
              "rounded-md px-1.5 py-1",
              "cursor-pointer",
              "hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
              isHidden ? "opacity-50" : "",
            ].join(" ")}
          >
            <span
              className="h-2.5 w-2.5 rounded-sm"
              style={{ backgroundColor: s.color }}
            />
            <span className={isHidden ? "line-through" : ""}>{s.key}</span>
          </button>
        );
      })}

      {anyHidden && (
        <button
          type="button"
          onClick={onShowAll}
          className="text-muted-foreground hover:text-foreground underline underline-offset-4"
          title="Show all series"
        >
          Show all
        </button>
      )}
    </div>
  );
};

function useContainerSize(ref: React.RefObject<HTMLElement | null>) {
  const [size, setSize] = useState<{ width: number; height: number }>({
    width: 0,
    height: 0,
  });
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => {
      const { width, height } = el.getBoundingClientRect();
      setSize((prev) =>
        prev.width === Math.round(width) && prev.height === Math.round(height)
          ? prev
          : { width: Math.round(width), height: Math.round(height) },
      );
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [ref]);
  return size;
}

const DataVisualization: React.FC<DataVisualizationProps> = ({
  dataString,
  title,
  className,
  plotSpec,
  chartEngine,
  artifactId,
  partial,
  fullArtifactId,
  rowCountTotal,
  downsampleRatio,
  chartBudget,
  dimensionFiltersEnabled,
}) => {
  const [currentPage, setCurrentPage] = useState(1);
  const [pageSize, setPageSize] = useState(10);
  const [searchTerm, setSearchTerm] = useState("");
  const [sortConfig, setSortConfig] = useState<SortConfig | null>(null);
  const [hiddenSeries, setHiddenSeries] = useState<Record<string, boolean>>({});
  const [dimensionFilters, setDimensionFilters] = useState<Record<string, string>>({});
  const [isChartExpanded, setIsChartExpanded] = useState(false);
  const [isTableOpen, setIsTableOpen] = useState(true);
  const [stablePlotSpec, setStablePlotSpec] = useState<unknown>(
    () => plotSpec ?? null,
  );
  const [remoteRows, setRemoteRows] = useState<Array<Record<string, any>>>([]);
  const [remoteRowCountTotal, setRemoteRowCountTotal] = useState<number | null>(
    null,
  );
  const [remoteHasMore, setRemoteHasMore] = useState(false);
  const [remoteLoading, setRemoteLoading] = useState(false);
  const [remoteError, setRemoteError] = useState("");
  const [isChartTransitioning, setIsChartTransitioning] = useState(false);
  const rawChartId = useId();
  const chartId = useMemo(
    () => `artifact-chart-${rawChartId.replace(/:/g, "")}`,
    [rawChartId],
  );
  const chartContainerRef = useRef<HTMLDivElement>(null);
  const chartSize = useContainerSize(chartContainerRef);
  const activePlotSpec = stablePlotSpec ?? plotSpec;
  const hasPlotSpec = activePlotSpec !== null && activePlotSpec !== undefined;
  const resolvedChartEngine = useMemo<"highcharts" | "echarts">(() => {
    const engine = String(chartEngine || "")
      .trim()
      .toLowerCase();
    if (engine === "echarts") return "echarts";
    if (engine === "highcharts") return "highcharts";
    if (isPlainObject(activePlotSpec)) {
      const schemaVersion = String(
        (activePlotSpec as Record<string, any>).schema_version || "",
      )
        .trim()
        .toLowerCase();
      if (schemaVersion.startsWith("echarts-")) return "echarts";
      if (schemaVersion.startsWith("highcharts-")) return "highcharts";
    }
    return "highcharts";
  }, [chartEngine, activePlotSpec]);
  const fullDataArtifactId = String(fullArtifactId || artifactId || "").trim();
  const isPartialDataset = Boolean(partial && fullDataArtifactId);
  const isDimensionFiltersEnabled = dimensionFiltersEnabled === true;
  const hasActiveDimensionFilters = useMemo(
    () => {
      if (!isDimensionFiltersEnabled) return false;
      return Object.values(dimensionFilters).some(
        (value) => value && value !== "__all__",
      );
    },
    [dimensionFilters, isDimensionFiltersEnabled],
  );
  const requestedRemotePageSize = useMemo(() => {
    if (pageSize <= 0) return 5000;
    return Math.min(pageSize, 5000);
  }, [pageSize]);

  const rawData = useMemo(() => {
    try {
      let data = JSON.parse(dataString);
      if (!Array.isArray(data) && data && typeof data === "object") {
        const maybeRows = (data as any).rows;
        if (Array.isArray(maybeRows)) {
          data = maybeRows;
        } else {
          const keys = Object.keys(data);
          if (keys.length === 1 && Array.isArray(data[keys[0]])) {
            data = data[keys[0]];
          }
        }
      }
      if (!Array.isArray(data)) return null;
      if (data.length === 0) return [];
      if (typeof data[0] !== "object") return null;
      return data;
    } catch {
      return null;
    }
  }, [dataString]);
  const chartInferenceRows = useMemo<Array<Record<string, any>>>(() => {
    if (Array.isArray(rawData) && rawData.length > 0) return rawData as Array<Record<string, any>>;
    if (remoteRows.length > 0) return remoteRows;
    return [];
  }, [rawData, remoteRows]);
  const inferredFilterChartInput = useMemo<FilterChartRebuildInput | null>(
    () =>
      inferFilterChartRebuildInput(
        activePlotSpec,
        chartInferenceRows,
        resolvedChartEngine,
      ),
    [activePlotSpec, chartInferenceRows, resolvedChartEngine],
  );

  useEffect(() => {
    // When dimension filters are active on a partial dataset, the filter-chart effect handles fetching
    if (isPartialDataset && hasActiveDimensionFilters) {
      return;
    }

    if (!isPartialDataset) {
      setRemoteRows([]);
      setRemoteError("");
      setRemoteHasMore(false);
      setRemoteRowCountTotal(null);
      setIsChartTransitioning(false);
      return;
    }
    let cancelled = false;
    setRemoteLoading(true);
    setRemoteError("");

    const offset = Math.max(0, (currentPage - 1) * requestedRemotePageSize);
    const params = new URLSearchParams({
      offset: String(offset),
      limit: String(requestedRemotePageSize),
    });

    void fetch(
      `/api/artifacts/${encodeURIComponent(fullDataArtifactId)}/rows?${params.toString()}`,
      {
        method: "GET",
        headers: { Accept: "application/json" },
      },
    )
      .then(async (response) => {
        const payload = await response.json().catch(() => ({}));
        if (!response.ok) {
          const message = String(
            (payload as any)?.error || `HTTP ${response.status}`,
          ).trim();
          throw new Error(message || `HTTP ${response.status}`);
        }
        return payload as any;
      })
      .then((payload) => {
        if (cancelled) return;
        const rowsRaw = Array.isArray(payload?.rows) ? payload.rows : [];
        const rows = rowsRaw.filter(
          (row) => row && typeof row === "object" && !Array.isArray(row),
        );
        setRemoteRows(rows);
        const totalRaw = Number(
          payload?.row_count_total ??
            payload?.total_rows ??
            rowCountTotal ??
            rows.length,
        );
        setRemoteRowCountTotal(
          Number.isFinite(totalRaw) && totalRaw >= 0 ? totalRaw : rows.length,
        );
        setRemoteHasMore(Boolean(payload?.has_more));
      })
      .catch((error: any) => {
        if (cancelled) return;
        setRemoteRows([]);
        setRemoteHasMore(false);
        setRemoteError(
          String(error?.message || error || "Failed to load rows."),
        );
      })
      .finally(() => {
        if (!cancelled) setRemoteLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [
    currentPage,
    fullDataArtifactId,
    hasActiveDimensionFilters,
    isPartialDataset,
    requestedRemotePageSize,
    rowCountTotal,
  ]);

  const fetchFilteredChart = useCallback(
    async (
      filters: Record<string, string>,
      page: number,
      chart: FilterChartRebuildInput | null,
      signal?: AbortSignal,
    ) => {
      const filterParams = Object.entries(filters)
        .filter(([, v]) => v && v !== "__all__")
        .map(([col, val]) => ({
          column: col,
          operator: "eq",
          value: val,
        }));
      if (filterParams.length === 0) return null;

      const offset = Math.max(0, (page - 1) * requestedRemotePageSize);
      const resp = await fetch(
        `/api/artifacts/${encodeURIComponent(fullDataArtifactId)}/filter-chart`,
        {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            Accept: "application/json",
          },
          body: JSON.stringify({
            filters: filterParams,
            offset,
            limit: requestedRemotePageSize,
            ...(chart ? { chart } : {}),
          }),
          signal,
        },
      );
      const payload = await resp.json().catch(() => ({}));
      if (!resp.ok) {
        throw new Error(
          String((payload as any)?.error || `HTTP ${resp.status}`).trim() ||
            `HTTP ${resp.status}`,
        );
      }
      return payload as any;
    },
    [fullDataArtifactId, requestedRemotePageSize],
  );

  useEffect(() => {
    if (!isPartialDataset || !hasActiveDimensionFilters) {
      // No active dimension filters on partial dataset — fall back to existing page-based fetch
      setIsChartTransitioning(false);
      return;
    }

    let cancelled = false;
    const controller = new AbortController();
    let transitionResetTimer: ReturnType<typeof setTimeout> | null = null;
    const timer = setTimeout(() => {
      setRemoteLoading(true);
      setRemoteError("");
      setIsChartTransitioning(true);
      fetchFilteredChart(
        dimensionFilters,
        currentPage,
        inferredFilterChartInput,
        controller.signal,
      )
        .then((payload) => {
          if (cancelled || !payload) return;
          const rowsRaw = Array.isArray(payload?.rows) ? payload.rows : [];
          const rows = rowsRaw.filter(
            (row: any) => row && typeof row === "object" && !Array.isArray(row),
          );
          setRemoteRows(rows);
          const totalRaw = Number(payload?.row_count_total ?? rows.length);
          setRemoteRowCountTotal(
            Number.isFinite(totalRaw) && totalRaw >= 0 ? totalRaw : rows.length,
          );
          setRemoteHasMore(Boolean(payload?.has_more));
          if (payload?.plot_spec != null) {
            setStablePlotSpec(payload.plot_spec);
          } else if (inferredFilterChartInput) {
            if (plotSpec != null) {
              const rebuiltSpec = rebuildPlotSpecFromRows(
                plotSpec,
                inferredFilterChartInput,
                rows,
              );
              if (rebuiltSpec != null) {
                setStablePlotSpec(rebuiltSpec);
              }
            }
          }
        })
        .catch((error: any) => {
          if (cancelled) return;
          setRemoteRows([]);
          setRemoteHasMore(false);
          setRemoteError(
            String(error?.message || error || "Failed to load filtered rows."),
          );
        })
        .finally(() => {
          if (!cancelled) {
            setRemoteLoading(false);
            transitionResetTimer = setTimeout(
              () => setIsChartTransitioning(false),
              180,
            );
          }
        });
    }, 300);

    return () => {
      cancelled = true;
      controller.abort();
      clearTimeout(timer);
      if (transitionResetTimer) clearTimeout(transitionResetTimer);
      setIsChartTransitioning(false);
    };
  }, [
    isPartialDataset,
    currentPage,
    dimensionFilters,
    fetchFilteredChart,
    hasActiveDimensionFilters,
    inferredFilterChartInput,
    plotSpec,
  ]);

  useEffect(() => {
    setCurrentPage(1);
  }, [searchTerm, pageSize]);
  useEffect(() => {
    setHiddenSeries({});
    setDimensionFilters({});
    setIsChartTransitioning(false);
  }, [dataString]);
  useEffect(() => {
    if (isDimensionFiltersEnabled) return;
    if (Object.keys(dimensionFilters).length === 0) return;
    setDimensionFilters({});
  }, [dimensionFilters, isDimensionFiltersEnabled]);
  useEffect(() => {
    setCurrentPage(1);
  }, [dimensionFilters]);
  useEffect(() => {
    if (isPartialDataset) return;
    if (!hasActiveDimensionFilters) {
      setIsChartTransitioning(false);
      return;
    }
    setIsChartTransitioning(true);
    const timer = setTimeout(() => setIsChartTransitioning(false), 220);
    return () => clearTimeout(timer);
  }, [dimensionFilters, hasActiveDimensionFilters, isPartialDataset]);
  useEffect(() => {
    if (plotSpec === null || plotSpec === undefined) return;
    setStablePlotSpec(plotSpec);
  }, [plotSpec]);
  useEffect(() => {
    if (hasActiveDimensionFilters) return;
    if (plotSpec === null || plotSpec === undefined) return;
    setStablePlotSpec(plotSpec);
  }, [hasActiveDimensionFilters, plotSpec]);

  const keys = useMemo(() => Object.keys(rawData?.[0] ?? {}), [rawData]);

  const { numberKeys, xAxisKey, isTimeSeries } = useMemo(() => {
    if (!rawData || keys.length === 0) {
      return { numberKeys: [], xAxisKey: "", isTimeSeries: false };
    }

    const sampleSize = Math.min(rawData.length, 100);
    const stats = keys.reduce(
      (acc, k) => {
        acc[k] = { numeric: 0, nonNumeric: 0, nonNumericString: 0, string: 0 };
        return acc;
      },
      {} as Record<
        string,
        {
          numeric: number;
          nonNumeric: number;
          nonNumericString: number;
          string: number;
        }
      >,
    );

    for (let i = 0; i < sampleSize; i++) {
      const row: any = rawData[i] || {};
      for (const k of keys) {
        const v = row[k];
        if (v === null || v === undefined) continue;
        if (typeof v === "string" && v.trim() === "") continue;
        const isStr = typeof v === "string";
        if (isStr) stats[k].string += 1;

        const n = toFiniteNumber(v);
        if (n !== null) {
          stats[k].numeric += 1;
          continue;
        }
        stats[k].nonNumeric += 1;
        if (isStr) stats[k].nonNumericString += 1;
      }
    }

    const nonNumericStringKeys = keys.filter(
      (k) => stats[k].nonNumericString > 0,
    );
    const anyStringKeys = keys.filter((k) => stats[k].string > 0);
    const axisCandidates =
      nonNumericStringKeys.length > 0 ? nonNumericStringKeys : anyStringKeys;

    // Prefer a dimension-like key for the X axis. Keep existing behavior:
    // pick from string-ish keys first, then fall back to any key by name.
    const axisNameRe =
      /date|time|timestamp|datetime|year|month|day|category|name|label/i;
    const chosenXAxis =
      axisCandidates.find((k) => axisNameRe.test(k)) ||
      axisCandidates[0] ||
      keys.find((k) => axisNameRe.test(k)) ||
      keys[0];

    const numericKeys = keys.filter((k) => {
      if (k === chosenXAxis) return false;
      const s = stats[k];
      const considered = s.numeric + s.nonNumeric;
      if (considered === 0) return false;
      // If the column is mostly numeric-like, treat it as a series.
      return s.numeric / considered >= 0.6;
    });

    const timeNameRe = /date|time|timestamp|datetime/i;
    const timeSeries = !!(chosenXAxis && timeNameRe.test(chosenXAxis));

    return {
      numberKeys: numericKeys,
      xAxisKey: chosenXAxis,
      isTimeSeries: timeSeries,
    };
  }, [rawData, keys]);

  const seriesPalette = useMemo(() => {
    return numberKeys.map((key, idx) => {
      const areaColor = `hsl(${25 + idx * 15}, ${85 - idx * 5}%, ${55 + idx * 3}%)`;
      const barColor = `hsl(${20 + idx * 15}, ${90 - idx * 5}%, ${48 + idx * 3}%)`;
      return {
        key,
        idx,
        areaColor,
        barColor,
        gradientId: `${chartId}-series-fill-${idx}`,
        areaId: `${chartId}-area-${idx}`,
        barId: `${chartId}-bar-${idx}`,
      };
    });
  }, [numberKeys, chartId]);

  type CategoricalColumn = { key: string; values: string[] };
  const categoricalColumns = useMemo<CategoricalColumn[]>(() => {
    if (!isDimensionFiltersEnabled) return [];
    if (!rawData || rawData.length === 0) return [];
    const sampleSize = Math.min(rawData.length, 200);
    const sample = rawData.slice(0, sampleSize);
    const skipSet = new Set<string>([xAxisKey, ...numberKeys]);
    const result: CategoricalColumn[] = [];
    for (const col of keys) {
      if (skipSet.has(col)) continue;
      const distinctValues = new Set<string>();
      let nonEmpty = 0;
      for (let i = 0; i < sample.length; i++) {
        const v = sample[i][col];
        if (v === null || v === undefined) continue;
        const s = String(v).trim();
        if (s === "") continue;
        nonEmpty++;
        distinctValues.add(s);
      }
      const cardinality = distinctValues.size;
      if (cardinality < 2 || cardinality > 50) continue;
      if (nonEmpty / sampleSize < 0.8) continue;
      const sorted = Array.from(distinctValues).sort();
      result.push({ key: col, values: sorted });
    }
    return result;
  }, [isDimensionFiltersEnabled, rawData, keys, numberKeys, xAxisKey]);

  const legendSeries = useMemo<SeriesDef[]>(
    () =>
      seriesPalette.map((s) => ({
        key: s.key,
        color: isTimeSeries ? s.areaColor : s.barColor,
      })),
    [seriesPalette, isTimeSeries],
  );

  const dimensionFilteredData = useMemo(() => {
    if (!rawData) return [];
    if (!isDimensionFiltersEnabled) return rawData;
    const activeFilters = Object.entries(dimensionFilters).filter(
      ([, v]) => v !== "" && v !== undefined && v !== "__all__",
    );
    if (activeFilters.length === 0) return rawData;
    return rawData.filter((row: any) =>
      activeFilters.every(
        ([col, val]) => String(row[col] ?? "").trim() === val,
      ),
    );
  }, [dimensionFilters, isDimensionFiltersEnabled, rawData]);

  const chartRebuildRows = useMemo<Array<Record<string, any>>>(() => {
    if (!hasActiveDimensionFilters) return [];
    if (isPartialDataset) return [];
    return dimensionFilteredData as Array<Record<string, any>>;
  }, [
    dimensionFilteredData,
    hasActiveDimensionFilters,
    isPartialDataset,
  ]);

  const filteredPlotSpec = useMemo(() => {
    if (!isDimensionFiltersEnabled) return activePlotSpec;
    if (!hasActiveDimensionFilters) return activePlotSpec;
    if (!isPartialDataset && inferredFilterChartInput) {
      return rebuildPlotSpecFromRows(
        activePlotSpec,
        inferredFilterChartInput,
        chartRebuildRows,
      );
    }
    return filterPlotSpecByDimension(activePlotSpec, dimensionFilters);
  }, [
    activePlotSpec,
    chartRebuildRows,
    dimensionFilters,
    isDimensionFiltersEnabled,
    hasActiveDimensionFilters,
    isPartialDataset,
    inferredFilterChartInput,
  ]);

  const filteredData = useMemo(() => {
    if (!searchTerm) return dimensionFilteredData;
    const lowerTerm = searchTerm.toLowerCase();
    return dimensionFilteredData.filter((row: any) =>
      keys.some((key) => String(row[key]).toLowerCase().includes(lowerTerm)),
    );
  }, [dimensionFilteredData, searchTerm, keys]);

  const sortedData = useMemo(() => {
    if (!sortConfig) return filteredData;
    return [...filteredData].sort((a: any, b: any) => {
      const aVal = a[sortConfig.key];
      const bVal = b[sortConfig.key];
      const aNum = toFiniteNumber(aVal);
      const bNum = toFiniteNumber(bVal);
      if (aNum !== null && bNum !== null) {
        if (aNum < bNum) return sortConfig.direction === "asc" ? -1 : 1;
        if (aNum > bNum) return sortConfig.direction === "asc" ? 1 : -1;
        return 0;
      }
      if (aVal < bVal) return sortConfig.direction === "asc" ? -1 : 1;
      if (aVal > bVal) return sortConfig.direction === "asc" ? 1 : -1;
      return 0;
    });
  }, [filteredData, sortConfig]);

  const totalPages = Math.ceil(sortedData.length / pageSize);
  const paginatedData =
    pageSize === -1
      ? sortedData
      : sortedData.slice((currentPage - 1) * pageSize, currentPage * pageSize);
  const hasAnyChart =
    hasPlotSpec || (numberKeys.length > 0 && filteredData.length > 0);
  const remoteKeys = useMemo(
    () => Object.keys(remoteRows?.[0] ?? {}),
    [remoteRows],
  );
  const remoteFilteredData = useMemo(() => {
    if (!isPartialDataset) return [];
    if (!searchTerm) return remoteRows;
    const lowerTerm = searchTerm.toLowerCase();
    return remoteRows.filter((row: any) =>
      remoteKeys.some((key) =>
        String(row[key]).toLowerCase().includes(lowerTerm),
      ),
    );
  }, [isPartialDataset, remoteRows, remoteKeys, searchTerm]);
  const remoteSortedData = useMemo(() => {
    if (!isPartialDataset) return [];
    if (!sortConfig) return remoteFilteredData;
    return [...remoteFilteredData].sort((a: any, b: any) => {
      const aVal = a[sortConfig.key];
      const bVal = b[sortConfig.key];
      const aNum = toFiniteNumber(aVal);
      const bNum = toFiniteNumber(bVal);
      if (aNum !== null && bNum !== null) {
        if (aNum < bNum) return sortConfig.direction === "asc" ? -1 : 1;
        if (aNum > bNum) return sortConfig.direction === "asc" ? 1 : -1;
        return 0;
      }
      if (aVal < bVal) return sortConfig.direction === "asc" ? -1 : 1;
      if (aVal > bVal) return sortConfig.direction === "asc" ? 1 : -1;
      return 0;
    });
  }, [isPartialDataset, remoteFilteredData, sortConfig]);
  const tableKeys = useMemo(() => {
    if (isPartialDataset) {
      if (remoteKeys.length > 0) return remoteKeys;
      return keys;
    }
    return keys;
  }, [isPartialDataset, keys, remoteKeys]);
  const tableRows = isPartialDataset ? remoteSortedData : paginatedData;
  const totalRecordCount = useMemo(() => {
    if (!isPartialDataset) return filteredData.length;
    const fromRemote = remoteRowCountTotal;
    if (fromRemote !== null && Number.isFinite(fromRemote) && fromRemote >= 0)
      return fromRemote;
    if (Number.isFinite(rowCountTotal as number) && Number(rowCountTotal) >= 0)
      return Number(rowCountTotal);
    return remoteSortedData.length;
  }, [
    filteredData.length,
    isPartialDataset,
    remoteRowCountTotal,
    remoteSortedData.length,
    rowCountTotal,
  ]);
  const effectiveTotalPages = useMemo(() => {
    if (!isPartialDataset) return Math.max(totalPages, 1);
    if (requestedRemotePageSize <= 0) return 1;
    return Math.max(1, Math.ceil(totalRecordCount / requestedRemotePageSize));
  }, [isPartialDataset, requestedRemotePageSize, totalPages, totalRecordCount]);
  const pageStart = useMemo(() => {
    if (totalRecordCount <= 0) return 0;
    if (isPartialDataset)
      return (currentPage - 1) * requestedRemotePageSize + 1;
    if (pageSize === -1) return 1;
    return Math.min((currentPage - 1) * pageSize + 1, filteredData.length);
  }, [
    currentPage,
    filteredData.length,
    isPartialDataset,
    pageSize,
    requestedRemotePageSize,
    totalRecordCount,
  ]);
  const pageEnd = useMemo(() => {
    if (totalRecordCount <= 0) return 0;
    if (isPartialDataset)
      return Math.min(
        (currentPage - 1) * requestedRemotePageSize + tableRows.length,
        totalRecordCount,
      );
    if (pageSize === -1) return filteredData.length;
    return Math.min(currentPage * pageSize, filteredData.length);
  }, [
    currentPage,
    filteredData.length,
    isPartialDataset,
    pageSize,
    requestedRemotePageSize,
    tableRows.length,
    totalRecordCount,
  ]);

  const chartHeightClass = useMemo(() => {
    if (isChartExpanded && !isTableOpen) {
      return "h-[72vh] min-h-[620px] max-h-[960px]";
    }
    if (isChartExpanded) {
      return "h-[64vh] min-h-[520px] max-h-[860px]";
    }
    if (!isTableOpen) {
      return "h-[56vh] min-h-[460px] max-h-[780px]";
    }
    return "h-[420px] md:h-[520px]";
  }, [isChartExpanded, isTableOpen]);
  const chartTransitionClass = isChartTransitioning
    ? "opacity-75"
    : "opacity-100";

  const chartData = useMemo(() => {
    const top = filteredData.slice(0, 20);
    if (numberKeys.length === 0) return top;
    // Coerce numeric-like strings to numbers so Recharts consistently renders series values.
    return top.map((row: any) => {
      const out: any = { ...row };
      for (const k of numberKeys) {
        const n = toFiniteNumber(out[k]);
        if (n !== null) out[k] = n;
      }
      return out;
    });
  }, [filteredData, numberKeys]);
  const sampledPointCount = useMemo(() => {
    const total = Number.isFinite(Number(rowCountTotal))
      ? Number(rowCountTotal)
      : null;
    const ratio = Number.isFinite(Number(downsampleRatio))
      ? Number(downsampleRatio)
      : null;
    if (total === null || ratio === null || ratio <= 1) return null;
    return Math.max(1, Math.round(total / ratio));
  }, [downsampleRatio, rowCountTotal]);
  const chartBudgetActions = useMemo(() => {
    if (!Array.isArray(chartBudget?.budget_actions)) return [];
    return chartBudget.budget_actions
      .map((entry) => String(entry || "").trim())
      .filter(Boolean);
  }, [chartBudget?.budget_actions]);
  const chartBudgetSummary = useMemo(() => {
    const requested = Number(chartBudget?.points_requested ?? NaN);
    const returned = Number(chartBudget?.points_returned ?? NaN);
    const totalRows = Number(
      chartBudget?.row_count_total ?? rowCountTotal ?? NaN,
    );

    let summary = "";
    if (Number.isFinite(returned) && returned >= 0) {
      const source =
        Number.isFinite(requested) && requested > 0
          ? requested
          : Number.isFinite(totalRows) && totalRows > 0
            ? totalRows
            : NaN;
      if (Number.isFinite(source) && source > returned) {
        summary = `Showing ${formatCompactInt(returned)} points from ${formatCompactInt(source)} rows.`;
      } else {
        summary = `Showing ${formatCompactInt(returned)} points.`;
      }
    } else if (
      sampledPointCount !== null &&
      Number.isFinite(Number(rowCountTotal))
    ) {
      summary = `Showing sampled points: ${formatCompactInt(sampledPointCount)} of ${formatCompactInt(Number(rowCountTotal))}.`;
    }

    if (chartBudgetActions.length > 0) {
      const actions = chartBudgetActions.join(", ");
      if (summary) return `${summary} Policy actions: ${actions}.`;
      return `Policy actions: ${actions}.`;
    }
    return summary || null;
  }, [
    chartBudget?.points_requested,
    chartBudget?.points_returned,
    chartBudget?.row_count_total,
    chartBudgetActions,
    rowCountTotal,
    sampledPointCount,
  ]);
  const chartBudgetFailureText = useMemo(() => {
    const stage = String(chartBudget?.failure_stage || "").trim();
    const code = String(chartBudget?.failure_code || "").trim();
    const message = String(chartBudget?.failure_message || "").trim();
    if (!stage && !code && !message) return null;
    const prefix = [stage || "unknown_stage", code].filter(Boolean).join(" / ");
    if (message) return `${prefix}: ${message}`;
    return prefix;
  }, [
    chartBudget?.failure_code,
    chartBudget?.failure_message,
    chartBudget?.failure_stage,
  ]);
  const chartRenderResetKey = useMemo(() => {
    const keyParts = [
      artifactId || "artifact",
      resolvedChartEngine || "chart",
      String(chartBudget?.plot_spec_bytes ?? ""),
      String(chartBudget?.points_returned ?? ""),
      String(chartBudget?.failure_stage ?? ""),
    ];
    return keyParts.join(":");
  }, [
    artifactId,
    chartBudget?.failure_stage,
    chartBudget?.plot_spec_bytes,
    chartBudget?.points_returned,
    resolvedChartEngine,
  ]);

  if (!rawData && !isPartialDataset) {
    return (
      <div className="p-4 text-destructive bg-destructive/10 rounded-lg border border-destructive/30">
        Invalid JSON Data
      </div>
    );
  }

  const handleSort = (key: string) => {
    setSortConfig((current) => {
      if (current?.key === key) {
        return current.direction === "asc" ? { key, direction: "desc" } : null;
      }
      return { key, direction: "asc" };
    });
  };

  const toggleSeries = (key: string) => {
    setHiddenSeries((prev) => ({ ...prev, [key]: !prev[key] }));
  };

  const showAllSeries = () => setHiddenSeries({});

  const downloadCSV = () => {
    const escapeCsvCell = (value: unknown): string => {
      if (value == null) return "";
      const raw =
        typeof value === "object" ? JSON.stringify(value) : String(value);
      const escaped = raw.replace(/"/g, '""');
      return /[",\n\r]/.test(escaped) ? `"${escaped}"` : escaped;
    };

    const exportKeys = isPartialDataset ? tableKeys : keys;
    const exportRows = isPartialDataset ? tableRows : sortedData;
    const rows = [
      exportKeys.map(escapeCsvCell).join(","),
      ...exportRows.map((row: any) =>
        exportKeys.map((k) => escapeCsvCell(row[k])).join(","),
      ),
    ];

    const blob = new Blob([rows.join("\n")], {
      type: "text/csv;charset=utf-8;",
    });
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.setAttribute("download", `${title || "data_export"}.csv`);
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
    URL.revokeObjectURL(url);
  };

  return (
    <div className={`flex flex-col h-full ${className || ""}`}>
      {/* Controls Bar */}
      <div className="flex flex-col gap-3 mb-4 p-1">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="relative flex-1 min-w-[200px]">
            <Search
              size={14}
              className="absolute left-3 top-2.5 text-muted-foreground"
            />
            <Input
              placeholder={
                isPartialDataset ? "Search current page..." : "Search data..."
              }
              value={searchTerm}
              onChange={(e) => setSearchTerm(e.target.value)}
              className="pl-9"
            />
          </div>
          <div className="flex items-center gap-2">
            <Select
              value={String(pageSize)}
              onValueChange={(v) => setPageSize(Number(v))}
            >
              <SelectTrigger className="w-[100px] h-9 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="5">5 rows</SelectItem>
                <SelectItem value="10">10 rows</SelectItem>
                <SelectItem value="20">20 rows</SelectItem>
                <SelectItem value="50">50 rows</SelectItem>
                {isPartialDataset ? (
                  <SelectItem value="200">200 rows</SelectItem>
                ) : (
                  <SelectItem value="-1">All rows</SelectItem>
                )}
              </SelectContent>
            </Select>
            <Button
              variant="outline"
              size="sm"
              onClick={downloadCSV}
              className="gap-2"
            >
              <Download size={14} />
              <span className="hidden sm:inline">CSV</span>
            </Button>
          </div>
        </div>
        {isDimensionFiltersEnabled && categoricalColumns.length > 0 && (
          <div className="flex flex-wrap items-center gap-2">
            {categoricalColumns.map((col) => (
              <Select
                key={col.key}
                value={dimensionFilters[col.key] || "__all__"}
                onValueChange={(v) =>
                  setDimensionFilters((prev) => {
                    const next = { ...prev };
                    if (v === "__all__") {
                      delete next[col.key];
                    } else {
                      next[col.key] = v;
                    }
                    return next;
                  })
                }
              >
                <SelectTrigger className="h-8 w-auto min-w-[120px] max-w-[200px] text-xs">
                  <SelectValue placeholder={`All ${col.key}`} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="__all__">All {col.key}</SelectItem>
                  {col.values.map((val) => (
                    <SelectItem key={val} value={val}>
                      {val}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ))}
            {Object.values(dimensionFilters).some((v) => v && v !== "__all__") && (
              <Button
                variant="ghost"
                size="sm"
                className="h-8 text-xs text-muted-foreground"
                onClick={() => setDimensionFilters({})}
              >
                Clear filters
              </Button>
            )}
          </div>
        )}
        {(chartBudgetSummary ||
          sampledPointCount !== null ||
          isPartialDataset) && (
          <div className="px-1 text-[11px] text-muted-foreground">
            {chartBudgetSummary || null}
            {!chartBudgetSummary &&
            sampledPointCount !== null &&
            Number.isFinite(Number(rowCountTotal))
              ? `Showing sampled points: ${sampledPointCount.toLocaleString()} of ${Number(rowCountTotal).toLocaleString()}.`
              : null}
            {isPartialDataset
              ? `${chartBudgetSummary || sampledPointCount !== null ? " " : ""}Table rows are loaded in pages from the full artifact.`
              : null}
          </div>
        )}
        {chartBudgetFailureText && (
          <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-[11px] text-destructive">
            <div className="inline-flex items-center gap-1.5 font-semibold uppercase tracking-wide">
              <AlertTriangle size={12} />
              Chart Failure
            </div>
            <div className="mt-1">{chartBudgetFailureText}</div>
          </div>
        )}
      </div>

      {/* Scrollable Content Area */}
      <div className="flex-1 overflow-y-auto space-y-6 pr-1 pb-4">
        {/* Chart Section */}
        {hasPlotSpec && (
          <Card className="p-4">
            <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
              <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                <BarChart2 size={14} />{" "}
                {resolvedChartEngine === "echarts"
                  ? "ECharts Visualization"
                  : "Highcharts Visualization"}
              </div>
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-8 gap-1.5 text-[11px]"
                  onClick={() => setIsTableOpen((open) => !open)}
                >
                  {isTableOpen ? (
                    <PanelBottomClose size={13} />
                  ) : (
                    <PanelBottomOpen size={13} />
                  )}
                  {isTableOpen ? "Hide Table" : "Show Table"}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-8 gap-1.5 text-[11px]"
                  onClick={() => setIsChartExpanded((expanded) => !expanded)}
                >
                  {isChartExpanded ? (
                    <Minimize2 size={13} />
                  ) : (
                    <Maximize2 size={13} />
                  )}
                  {isChartExpanded ? "Normal Size" : "Expand Chart"}
                </Button>
              </div>
            </div>
            <div
              className={`relative transition-opacity duration-300 ease-out ${chartTransitionClass}`}
            >
              {isChartTransitioning && (
                <div className="pointer-events-none absolute inset-0 z-10 bg-gradient-to-r from-transparent via-background/20 to-transparent" />
              )}
              <ChartRenderErrorBoundary
                artifactId={artifactId}
                engine={resolvedChartEngine}
                chartBudget={chartBudget}
                resetKey={chartRenderResetKey}
                className={`${chartHeightClass} w-full`}
              >
                {resolvedChartEngine === "echarts" ? (
                  <EchartsVisualization
                    plotSpec={filteredPlotSpec}
                    title={title}
                    className={`${chartHeightClass} w-full`}
                  />
                ) : (
                  <HighchartsVisualization
                    plotSpec={filteredPlotSpec}
                    title={title}
                    className={`${chartHeightClass} w-full`}
                  />
                )}
              </ChartRenderErrorBoundary>
            </div>
          </Card>
        )}
        {!hasPlotSpec && numberKeys.length > 0 && filteredData.length > 0 && (
          <Card className="p-4">
            <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
              <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
                <BarChart2 size={14} /> Visualization (Top 20)
              </div>
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-8 gap-1.5 text-[11px]"
                  onClick={() => setIsTableOpen((open) => !open)}
                >
                  {isTableOpen ? (
                    <PanelBottomClose size={13} />
                  ) : (
                    <PanelBottomOpen size={13} />
                  )}
                  {isTableOpen ? "Hide Table" : "Show Table"}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="h-8 gap-1.5 text-[11px]"
                  onClick={() => setIsChartExpanded((expanded) => !expanded)}
                >
                  {isChartExpanded ? (
                    <Minimize2 size={13} />
                  ) : (
                    <Maximize2 size={13} />
                  )}
                  {isChartExpanded ? "Normal Size" : "Expand Chart"}
                </Button>
              </div>
            </div>
            <div
              className={`relative transition-opacity duration-300 ease-out ${chartTransitionClass}`}
            >
              {isChartTransitioning && (
                <div className="pointer-events-none absolute inset-0 z-10 bg-gradient-to-r from-transparent via-background/20 to-transparent" />
              )}
              <ChartRenderErrorBoundary
                artifactId={artifactId}
                engine="recharts"
                chartBudget={chartBudget}
                resetKey={`${chartRenderResetKey}:recharts`}
                className={`${chartHeightClass} w-full`}
              >
                <div
                  ref={chartContainerRef}
                  className={`${chartHeightClass} w-full`}
                >
                {chartSize.width > 0 &&
                  chartSize.height > 0 &&
                  (isTimeSeries ? (
                    <AreaChart
                      id={`${chartId}-area-chart`}
                      width={chartSize.width}
                      height={chartSize.height}
                      data={chartData}
                    >
                      <defs>
                        {seriesPalette.map((s) => (
                          <linearGradient
                            key={s.key}
                            id={s.gradientId}
                            x1="0"
                            y1="0"
                            x2="0"
                            y2="1"
                          >
                            <stop
                              offset="5%"
                              stopColor={s.areaColor}
                              stopOpacity={0.3}
                            />
                            <stop
                              offset="95%"
                              stopColor={s.areaColor}
                              stopOpacity={0}
                            />
                          </linearGradient>
                        ))}
                      </defs>
                      <CartesianGrid
                        strokeDasharray="3 3"
                        stroke="hsl(var(--border))"
                        vertical={false}
                      />
                      <XAxis
                        dataKey={xAxisKey}
                        stroke="hsl(var(--muted-foreground))"
                        fontSize={11}
                        tickLine={false}
                        axisLine={false}
                      />
                      <YAxis
                        stroke="hsl(var(--muted-foreground))"
                        fontSize={11}
                        tickLine={false}
                        axisLine={false}
                      />
                      <Tooltip
                        contentStyle={{
                          backgroundColor: "hsl(var(--card))",
                          borderColor: "hsl(var(--border))",
                          borderRadius: "8px",
                          color: "hsl(var(--foreground))",
                          fontSize: "12px",
                        }}
                        itemStyle={{ color: "hsl(var(--card-foreground))" }}
                      />
                      <Legend
                        wrapperStyle={{ zIndex: 10, pointerEvents: "auto" }}
                        content={() => (
                          <InteractiveLegend
                            series={legendSeries}
                            hiddenSeries={hiddenSeries}
                            onToggle={toggleSeries}
                            onShowAll={showAllSeries}
                          />
                        )}
                      />
                      {seriesPalette.map((s) => (
                        <Area
                          id={s.areaId}
                          key={s.key}
                          type="monotone"
                          dataKey={s.key}
                          stroke={s.areaColor}
                          fill={`url(#${s.gradientId})`}
                          strokeWidth={2}
                          isAnimationActive={false}
                          hide={!!hiddenSeries[s.key]}
                        />
                      ))}
                    </AreaChart>
                  ) : (
                    <BarChart
                      id={`${chartId}-bar-chart`}
                      width={chartSize.width}
                      height={chartSize.height}
                      data={chartData}
                    >
                      <CartesianGrid
                        strokeDasharray="3 3"
                        stroke="hsl(var(--border))"
                        vertical={false}
                      />
                      <XAxis
                        dataKey={xAxisKey}
                        stroke="hsl(var(--muted-foreground))"
                        fontSize={11}
                        tickLine={false}
                        axisLine={false}
                      />
                      <YAxis
                        stroke="hsl(var(--muted-foreground))"
                        fontSize={11}
                        tickLine={false}
                        axisLine={false}
                      />
                      <Tooltip
                        contentStyle={{
                          backgroundColor: "hsl(var(--card))",
                          borderColor: "hsl(var(--border))",
                          borderRadius: "8px",
                          color: "hsl(var(--foreground))",
                          fontSize: "12px",
                        }}
                        itemStyle={{ color: "hsl(var(--card-foreground))" }}
                        cursor={{ fill: "hsl(var(--muted))", opacity: 0.2 }}
                      />
                      <Legend
                        wrapperStyle={{ zIndex: 10, pointerEvents: "auto" }}
                        content={() => (
                          <InteractiveLegend
                            series={legendSeries}
                            hiddenSeries={hiddenSeries}
                            onToggle={toggleSeries}
                            onShowAll={showAllSeries}
                          />
                        )}
                      />
                      {seriesPalette.map((s) => (
                        <Bar
                          id={s.barId}
                          key={s.key}
                          dataKey={s.key}
                          fill={s.barColor}
                          radius={[4, 4, 0, 0]}
                          minPointSize={2}
                          isAnimationActive={false}
                          hide={!!hiddenSeries[s.key]}
                        />
                      ))}
                    </BarChart>
                  ))}
                </div>
              </ChartRenderErrorBoundary>
            </div>
          </Card>
        )}

        {/* Table Section */}
        <Card className="overflow-hidden">
          <div className="flex items-center justify-between gap-2 px-4 py-2 bg-muted/40 border-b border-border">
            <div className="flex items-center gap-2 text-xs font-semibold text-muted-foreground uppercase tracking-wider">
              <TableIcon size={14} /> Dataset
            </div>
            <div className="flex items-center gap-2">
              <Badge variant="secondary" className="text-[10px]">
                {totalRecordCount} records
              </Badge>
              {hasAnyChart && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-7 gap-1 text-[11px]"
                  onClick={() => setIsTableOpen((open) => !open)}
                >
                  {isTableOpen ? (
                    <PanelBottomClose size={13} />
                  ) : (
                    <PanelBottomOpen size={13} />
                  )}
                  {isTableOpen ? "Collapse" : "Expand"}
                </Button>
              )}
            </div>
          </div>

          {isTableOpen ? (
            <>
              <ScrollArea className="max-h-[400px]">
                <div className="overflow-x-auto">
                  <table className="w-full text-left text-xs border-collapse">
                    <thead className="sticky top-0 z-10">
                      <tr className="border-b border-border bg-muted shadow-sm">
                        {tableKeys.map((k) => (
                          <th
                            key={k}
                            className="p-3 font-mono text-muted-foreground font-semibold cursor-pointer hover:bg-muted/80 transition-colors select-none whitespace-nowrap group"
                            onClick={() => handleSort(k)}
                          >
                            <div className="flex items-center gap-1.5">
                              {k}
                              <span className="text-muted-foreground/40 group-hover:text-primary transition-colors">
                                {sortConfig?.key === k ? (
                                  sortConfig.direction === "asc" ? (
                                    <ArrowUp size={12} />
                                  ) : (
                                    <ArrowDown size={12} />
                                  )
                                ) : (
                                  <ArrowUpDown
                                    size={12}
                                    className="opacity-0 group-hover:opacity-50"
                                  />
                                )}
                              </span>
                            </div>
                          </th>
                        ))}
                      </tr>
                    </thead>
                    <tbody className="font-mono text-card-foreground divide-y divide-border/50">
                      {remoteLoading ? (
                        <tr>
                          <td
                            colSpan={Math.max(1, tableKeys.length)}
                            className="p-6 text-center text-muted-foreground italic"
                          >
                            Loading rows...
                          </td>
                        </tr>
                      ) : null}
                      {!remoteLoading && remoteError ? (
                        <tr>
                          <td
                            colSpan={Math.max(1, tableKeys.length)}
                            className="p-6 text-center text-destructive"
                          >
                            {remoteError}
                          </td>
                        </tr>
                      ) : null}
                      {!remoteLoading &&
                        !remoteError &&
                        tableRows.map((row: any, i: number) => (
                          <tr
                            key={i}
                            className="hover:bg-muted/30 transition-colors group"
                          >
                            {tableKeys.map((k) => (
                              <td
                                key={k}
                                className="p-3 whitespace-nowrap max-w-[200px] overflow-hidden text-ellipsis group-hover:text-foreground"
                                title={String(row[k])}
                              >
                                {String(row[k])}
                              </td>
                            ))}
                          </tr>
                        ))}
                      {!remoteLoading &&
                        !remoteError &&
                        tableRows.length === 0 && (
                          <tr>
                            <td
                              colSpan={Math.max(1, tableKeys.length)}
                              className="p-8 text-center text-muted-foreground italic"
                            >
                              No matching records found.
                            </td>
                          </tr>
                        )}
                    </tbody>
                  </table>
                </div>
                <ScrollBar orientation="horizontal" />
              </ScrollArea>

              {/* Pagination */}
              <div className="flex items-center justify-between px-4 py-3 bg-muted/40 border-t border-border text-xs">
                <span className="text-muted-foreground">
                  Showing {pageStart} - {pageEnd} of {totalRecordCount}
                </span>
                {((!isPartialDataset && pageSize !== -1) || isPartialDataset) &&
                  effectiveTotalPages > 1 && (
                    <div className="flex items-center gap-1">
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7"
                        onClick={() =>
                          setCurrentPage((p) => Math.max(1, p - 1))
                        }
                        disabled={currentPage === 1 || remoteLoading}
                      >
                        <ChevronLeft size={16} />
                      </Button>
                      <span className="px-2 text-muted-foreground font-medium">
                        {currentPage} / {effectiveTotalPages}
                      </span>
                      <Button
                        variant="ghost"
                        size="icon"
                        className="h-7 w-7"
                        onClick={() =>
                          setCurrentPage((p) =>
                            Math.min(effectiveTotalPages, p + 1),
                          )
                        }
                        disabled={
                          currentPage === effectiveTotalPages ||
                          remoteLoading ||
                          (isPartialDataset &&
                            !remoteHasMore &&
                            currentPage >= effectiveTotalPages)
                        }
                      >
                        <ChevronRight size={16} />
                      </Button>
                    </div>
                  )}
              </div>
            </>
          ) : (
            <div className="border-t border-border bg-muted/20 px-4 py-3 text-xs text-muted-foreground">
              Table drawer is collapsed. Expand it to inspect rows, sorting, and
              pagination.
            </div>
          )}
        </Card>
      </div>
    </div>
  );
};

export default DataVisualization;
