import React, { useEffect, useMemo, useState } from 'react';
import { Loader2, RefreshCw } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { useTokenUsage } from '@/hooks/useTokenUsage';
import {
  browserTimezone,
  formatCalendarDate,
  formatCount,
  formatTimestamp,
  formatTokenFloor,
  presetRange,
  rangeValidationMessage,
  tokenValueSrText,
  type DateRange,
  type RangePreset,
} from './usage/usageFormat';
import {
  applyFilters,
  buildTableRows,
  computeTotals,
  deriveFilterOptions,
  emptyFilters,
  isAnyFilterActive,
  reconcileFilters,
  type GroupByDimension,
  type UsageFilterDimension,
} from './usage/usageViewModel';
import { LoadingState, StateMessage, TrackingNote } from './usage/UsageStates';
import { UsageQualityBanner } from './usage/UsageQualityBanner';
import { UsageSummary } from './usage/UsageSummary';
import { UsageFilterBar, UsageRangePicker } from './usage/UsageToolbar';
import { UsageChart } from './usage/UsageChart';
import { UsageTable } from './usage/UsageTable';

interface UsageTabProps {
  active: boolean;
}

const UsageTab: React.FC<UsageTabProps> = ({ active }) => {
  const timezone = useMemo(browserTimezone, []);
  const initialRange = useMemo(() => presetRange(30), []);
  const [preset, setPreset] = useState<RangePreset>('30');
  const [range, setRange] = useState<DateRange>(initialRange);
  const [customRange, setCustomRange] = useState<DateRange>(initialRange);
  const [filters, setFilters] = useState(emptyFilters);
  const [groupBy, setGroupBy] = useState<GroupByDimension>('model');

  const usage = useTokenUsage(active, { ...range, timezone });
  const report = usage.data;

  const filterOptions = useMemo(
    () => deriveFilterOptions(report, filters.provider),
    [filters.provider, report],
  );

  useEffect(() => {
    if (!report) return;
    setFilters((current) => reconcileFilters(current, filterOptions));
  }, [filterOptions, report]);

  const visibleBuckets = useMemo(
    () => applyFilters(report?.buckets || [], filters),
    [filters, report],
  );
  const totals = useMemo(
    () => computeTotals(report, filters, visibleBuckets),
    [filters, report, visibleBuckets],
  );
  const tableRows = useMemo(() => buildTableRows(visibleBuckets), [visibleBuckets]);

  const handleFilterChange = (dimension: UsageFilterDimension, value: string | null) => {
    setFilters((current) => ({ ...current, [dimension]: value }));
  };

  const selectPreset = (next: Exclude<RangePreset, 'custom'>) => {
    const nextRange = presetRange(Number(next));
    setPreset(next);
    setRange(nextRange);
    setCustomRange(nextRange);
  };

  const applyCustomRange = () => {
    if (rangeValidationMessage(customRange)) return;
    setRange(customRange);
  };

  const generatedAt = formatTimestamp(report?.generatedAt || null, timezone);
  const trackingStartedAt = formatTimestamp(report?.trackingStartedAt || null, timezone);
  const hasNoUsage = Boolean(report && report.totals.invocationCount === 0 && report.buckets.length === 0);
  const filteredEmpty = Boolean(report && !hasNoUsage && visibleBuckets.length === 0);
  const customRangeError = rangeValidationMessage(customRange);
  const rangeLabel = report
    ? `${formatCalendarDate(report.range.from)} – ${formatCalendarDate(report.range.to)}`
    : null;
  const healthIssue = Boolean(report && (report.health.status !== 'healthy' || !report.reportComplete));
  const totalAnnouncement = (() => {
    if (!report) return '';
    const callsPhrase = `${formatCount(totals.invocationCount)} model calls`;
    const totalDisplay = formatTokenFloor(totals.totalTokens, totals.lowerBoundTotalTokens, totals.totalMeasuredInvocationCount, totals.lowerBoundTotalInvocationCount, totals.invocationCount, report.reportComplete);
    if (totalDisplay === '—') return `Showing ${callsPhrase} with no recorded token total.`;
    return `Showing ${tokenValueSrText(totalDisplay) ?? totalDisplay} tokens across ${callsPhrase}.`;
  })();

  return (
    <section className="space-y-5 pb-1" aria-labelledby="usage-heading" data-testid="settings-usage-tab">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 id="usage-heading" className="text-base font-semibold">Token usage</h2>
          <p className="mt-1 text-sm text-muted-foreground">
            Recorded token counts by day, model, operation, connection, and API surface.
          </p>
        </div>
        <div className="flex items-center gap-2">
          {generatedAt && <span className="hidden text-xs text-muted-foreground sm:inline">Updated {generatedAt}</span>}
          <Button
            variant="outline"
            size="sm"
            onClick={usage.refresh}
            disabled={!active || usage.status === 'loading' || usage.isRefreshing}
            aria-label="Refresh token usage"
          >
            {usage.isRefreshing ? <Loader2 className="animate-spin" aria-hidden /> : <RefreshCw aria-hidden />}
            Refresh
          </Button>
        </div>
      </div>

      {/* A deliberately slim, boxless range row: the hero total must stay the
          first substantial thing on the page, but the range control has to
          remain reachable in every state (loading, error, empty). */}
      <UsageRangePicker
        preset={preset}
        onSelectPreset={selectPreset}
        onSelectCustom={() => setPreset('custom')}
        customRange={customRange}
        onCustomRangeChange={setCustomRange}
        onApplyCustomRange={applyCustomRange}
        customRangeError={preset === 'custom' ? customRangeError : null}
        timezone={timezone}
      />

      {(usage.status === 'idle' || usage.status === 'loading') && !report ? (
        <LoadingState />
      ) : usage.status === 'unavailable' ? (
        <StateMessage
          kind="unavailable"
          title="Usage reporting is unavailable"
          message={usage.error || 'This server build does not expose token usage reports yet.'}
        />
      ) : usage.status === 'error' ? (
        <StateMessage
          kind="error"
          title="Token usage could not be loaded"
          message={usage.error || 'Check the server connection and try again.'}
          onRetry={usage.refresh}
        />
      ) : hasNoUsage && report ? (
        <>
          <StateMessage
            kind="empty"
            title="No token usage in this period"
            message="Model calls will appear here after the server records usage for this date range."
          />
          {/* A quiet "no usage" period only needs the banner when tracking
              itself was unhealthy — that is the one caveat worth surfacing. */}
          {healthIssue && (
            <UsageQualityBanner
              health={report.health}
              reportComplete={report.reportComplete}
              totals={totals}
              filtersActive={isAnyFilterActive(filters)}
            />
          )}
          <TrackingNote trackingStartedAt={trackingStartedAt} />
        </>
      ) : report ? (
        <>
          {/* One persistent, always-polite result announcement so keyboard and
              screen-reader users hear the new headline when a filter or range
              change mutates totals rendered below the focused control. */}
          <p role="status" className="sr-only">
            {filteredEmpty
              ? 'No recorded usage matches the current filters.'
              : totalAnnouncement}
          </p>

          {!filteredEmpty && (
            <UsageSummary totals={totals} reportComplete={report.reportComplete} rangeLabel={rangeLabel} />
          )}

          <div className="rounded-lg border bg-muted/20 p-3" data-testid="usage-controls">
            <UsageFilterBar
              filters={filters}
              options={filterOptions}
              onFilterChange={handleFilterChange}
              onClearAll={() => setFilters(emptyFilters())}
            />
          </div>

          <UsageQualityBanner
            health={report.health}
            reportComplete={report.reportComplete}
            totals={totals}
            filtersActive={isAnyFilterActive(filters)}
          />

          {filteredEmpty ? (
            <StateMessage
              kind="empty"
              title="No usage matches these filters"
              message="Clear or change the connection, provider, model, operation, or API surface filters to see recorded usage."
            />
          ) : (
            <>
              <UsageChart
                buckets={visibleBuckets}
                groupBy={groupBy}
                onGroupByChange={setGroupBy}
                reportComplete={report.reportComplete}
              />
              <UsageTable rows={tableRows} reportComplete={report.reportComplete} />
            </>
          )}

          <TrackingNote trackingStartedAt={trackingStartedAt} />
        </>
      ) : null}
    </section>
  );
};

export default UsageTab;
