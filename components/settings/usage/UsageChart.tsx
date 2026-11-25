import React, { useMemo } from 'react';
import {
  Bar,
  BarChart,
  CartesianGrid,
  Legend,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Label } from '@/components/ui/label';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import type { TokenUsageBucket } from '@/services/tokenUsageApi';
import { formatCalendarDate, formatCompactCount } from './usageFormat';
import {
  GROUP_BY_DIMENSIONS,
  buildChart,
  formatChartPoint,
  groupByLabel,
  type GroupByDimension,
} from './usageViewModel';

/**
 * Usage-scoped categorical palette (the global --chart-1..5 tokens are five
 * near-identical oranges). Validated for the dark card surface: all slots
 * >= 3:1 contrast, worst adjacent CVD delta-E 101. Slots are assigned in
 * fixed order to the ranked series; grey is reserved for "Other" and never
 * doubles as a series color.
 */
const USAGE_CHART_COLORS = [
  '#ea580c', // orange (primary family)
  '#3b82f6', // blue (artifact-data family)
  '#16a34a', // green (artifact-chart family)
  '#7c3aed', // violet (artifact-code family)
  '#d97706', // amber (artifact-file family)
];
const USAGE_CHART_OTHER_COLOR = '#6b7280';

/**
 * Recharts' default legend draws svg swatch icons that expose their own
 * `img` roles and aria-labels; the chart already has a single labelled
 * `figure` wrapper, so the legend stays presentational text plus a hidden
 * color swatch.
 */
function UsageLegendContent({ payload }: { payload?: ReadonlyArray<{ value?: unknown; color?: string }> }) {
  if (!payload || payload.length === 0) return null;
  return (
    <ul className="flex flex-wrap justify-center gap-x-4 gap-y-1 pt-2 text-[11px] text-muted-foreground">
      {payload.map((entry, index) => (
        <li key={`${String(entry.value ?? '')}-${index}`} className="flex items-center gap-1.5">
          <span aria-hidden className="h-2.5 w-2.5 shrink-0 rounded-sm" style={{ backgroundColor: entry.color }} />
          <span>{String(entry.value ?? '')}</span>
        </li>
      ))}
    </ul>
  );
}

export function UsageChart({
  buckets,
  groupBy,
  onGroupByChange,
  reportComplete,
}: {
  buckets: TokenUsageBucket[];
  groupBy: GroupByDimension;
  onGroupByChange: (dimension: GroupByDimension) => void;
  reportComplete: boolean;
}) {
  const chart = useMemo(() => buildChart(buckets, groupBy, reportComplete), [buckets, groupBy, reportComplete]);
  const dimensionLabel = groupByLabel(groupBy);
  const qualifierId = React.useId();
  const hasQualifier = chart.hasPartial || chart.hasUnknown;

  return (
    <Card className="shadow-sm" data-testid="usage-chart">
      <CardHeader className="flex-row flex-wrap items-center justify-between gap-2 space-y-0 p-4 pb-2">
        <CardTitle className="text-sm">Daily recorded tokens</CardTitle>
        <div className="flex items-center gap-2">
          <Label htmlFor="usage-chart-group-by" className="text-xs text-muted-foreground">Group by</Label>
          <Select value={groupBy} onValueChange={(value) => onGroupByChange(value as GroupByDimension)}>
            <SelectTrigger
              id="usage-chart-group-by"
              data-testid="usage-chart-group-by"
              aria-label="Group usage by"
              className="h-8 w-36 text-xs"
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {GROUP_BY_DIMENSIONS.map((dimension) => (
                <SelectItem key={dimension.value} value={dimension.value}>{dimension.label}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </CardHeader>
      <CardContent className="p-2 pt-0 sm:p-4 sm:pt-0">
        {/* A labelled figure (not role="img") keeps recharts' accessibilityLayer
            keyboard navigation and its accessible subtree reachable. */}
        <figure
          className="m-0 h-72 w-full"
          aria-label={`Stacked bar chart of daily recorded total tokens by ${dimensionLabel.toLowerCase()}`}
          aria-describedby={hasQualifier ? qualifierId : undefined}
        >
          <ResponsiveContainer width="100%" height="100%">
            <BarChart data={chart.rows} margin={{ top: 12, right: 10, bottom: 4, left: 0 }} accessibilityLayer>
              <CartesianGrid strokeDasharray="3 3" vertical={false} stroke="hsl(var(--border))" />
              <XAxis
                dataKey="date"
                tickFormatter={(value) => formatCalendarDate(String(value)).replace(/,? \d{4}/, '')}
                tick={{ fontSize: 11, fill: 'hsl(var(--muted-foreground))' }}
                axisLine={false}
                tickLine={false}
              />
              <YAxis
                tickFormatter={(value) => formatCompactCount(Number(value))}
                tick={{ fontSize: 11, fill: 'hsl(var(--muted-foreground))' }}
                axisLine={false}
                tickLine={false}
                width={44}
              />
              <Tooltip
                labelFormatter={(value) => formatCalendarDate(String(value))}
                formatter={(value, name, item) => [
                  formatChartPoint(
                    chart,
                    Number(value),
                    String((item as { payload?: { date?: string } })?.payload?.date ?? ''),
                    String((item as { dataKey?: string | number })?.dataKey ?? ''),
                  ),
                  String(name),
                ]}
                cursor={{ fill: 'hsl(var(--muted))', fillOpacity: 0.35 }}
                contentStyle={{
                  background: 'hsl(var(--popover))',
                  border: '1px solid hsl(var(--border))',
                  borderRadius: 'var(--radius)',
                  color: 'hsl(var(--popover-foreground))',
                  fontSize: 12,
                }}
                itemStyle={{ color: 'hsl(var(--popover-foreground))' }}
              />
              {chart.series.length > 1 && <Legend content={<UsageLegendContent />} />}
              {chart.series.map((series, index) => (
                <Bar
                  key={series.key}
                  dataKey={series.key}
                  name={series.label}
                  stackId="tokens"
                  fill={series.isOther ? USAGE_CHART_OTHER_COLOR : USAGE_CHART_COLORS[index]}
                  stroke="hsl(var(--card))"
                  strokeWidth={1}
                  maxBarSize={48}
                />
              ))}
            </BarChart>
          </ResponsiveContainer>
        </figure>
        {hasQualifier && (
          <p id={qualifierId} className="mt-1 px-2 text-xs text-muted-foreground" data-testid="usage-chart-qualifier">
            Bars show recorded floors: ≥ marks values where some calls lack exact totals; — means no total was reported for that series on that day.
          </p>
        )}
      </CardContent>
    </Card>
  );
}
