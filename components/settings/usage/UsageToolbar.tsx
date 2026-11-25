import React, { useState } from 'react';
import { SlidersHorizontal, X } from 'lucide-react';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import type { DateRange, RangePreset } from './usageFormat';
import {
  ALL_FILTER,
  type UsageFilterDimension,
  type UsageFilterOptions,
  type UsageFilters,
} from './usageViewModel';

const RANGE_ERROR_ID = 'usage-range-error';
const SELECT_ALL_FILTER = 'all';
const SELECT_VALUE_PREFIX = 'value:';

function selectFilterValue(value: string | null): string {
  return value === ALL_FILTER ? SELECT_ALL_FILTER : `${SELECT_VALUE_PREFIX}${value}`;
}

function parseSelectFilterValue(value: string): string | null {
  return value === SELECT_ALL_FILTER ? ALL_FILTER : value.slice(SELECT_VALUE_PREFIX.length);
}

export function UsageRangePicker({
  preset,
  onSelectPreset,
  onSelectCustom,
  customRange,
  onCustomRangeChange,
  onApplyCustomRange,
  customRangeError,
  timezone,
}: {
  preset: RangePreset;
  onSelectPreset: (preset: Exclude<RangePreset, 'custom'>) => void;
  onSelectCustom: () => void;
  customRange: DateRange;
  onCustomRangeChange: (range: DateRange) => void;
  onApplyCustomRange: () => void;
  customRangeError: string | null;
  timezone: string;
}) {
  return (
    <div>
      <div className="flex flex-wrap items-end gap-3">
        <div className="space-y-1.5">
          <Label className="text-xs">Date range</Label>
          <div className="flex rounded-md border bg-background p-0.5" role="group" aria-label="Usage date range">
            {(['7', '30', '90'] as const).map((value) => (
              <Button
                key={value}
                type="button"
                size="sm"
                variant={preset === value ? 'secondary' : 'ghost'}
                className="h-7 px-2.5"
                aria-pressed={preset === value}
                onClick={() => onSelectPreset(value)}
              >
                {value} days
              </Button>
            ))}
            <Button
              type="button"
              size="sm"
              variant={preset === 'custom' ? 'secondary' : 'ghost'}
              className="h-7 px-2.5"
              aria-pressed={preset === 'custom'}
              onClick={onSelectCustom}
            >
              Custom
            </Button>
          </div>
        </div>

        {preset === 'custom' && (
          <>
            <div className="space-y-1.5">
              <Label htmlFor="usage-from" className="text-xs">From</Label>
              <Input
                id="usage-from"
                type="date"
                value={customRange.from}
                max={customRange.to || undefined}
                onChange={(event) => onCustomRangeChange({ ...customRange, from: event.target.value })}
                className="w-36"
                aria-invalid={Boolean(customRangeError) || undefined}
                aria-describedby={customRangeError ? RANGE_ERROR_ID : undefined}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="usage-to" className="text-xs">To</Label>
              <Input
                id="usage-to"
                type="date"
                value={customRange.to}
                min={customRange.from || undefined}
                onChange={(event) => onCustomRangeChange({ ...customRange, to: event.target.value })}
                className="w-36"
                aria-invalid={Boolean(customRangeError) || undefined}
                aria-describedby={customRangeError ? RANGE_ERROR_ID : undefined}
              />
            </div>
            <Button type="button" variant="secondary" size="sm" disabled={Boolean(customRangeError)} onClick={onApplyCustomRange}>
              Apply
            </Button>
          </>
        )}

        <p className="ml-auto pb-1 text-xs text-muted-foreground" title={timezone}>
          Calendar days in <span className="font-medium text-foreground">{timezone}</span>
        </p>
      </div>
      {preset === 'custom' && customRangeError && (
        <p id={RANGE_ERROR_ID} className="mt-2 text-xs text-destructive" role="alert">{customRangeError}</p>
      )}
    </div>
  );
}

const DIMENSION_LABELS: Record<UsageFilterDimension, string> = {
  provider: 'Provider',
  model: 'Model',
  connection: 'Connection',
  operation: 'Operation',
  surface: 'API surface',
};

function lookupLabel(pairs: Array<[string, string]>, key: string): string {
  return pairs.find(([value]) => value === key)?.[1] || key;
}

function chipLabel(dimension: UsageFilterDimension, value: string, options: UsageFilterOptions): string {
  switch (dimension) {
    case 'connection':
      return lookupLabel(options.connections, value);
    case 'surface':
      return lookupLabel(options.surfaces, value);
    case 'operation':
      return lookupLabel(options.operations, value);
    default:
      return value;
  }
}

/**
 * Provider and Model stay readily visible; Connection, Operation, and API
 * surface live behind a "More filters" popover. Every active filter renders
 * as a dismissible chip.
 */
export function UsageFilterBar({
  filters,
  options,
  onFilterChange,
  onClearAll,
}: {
  filters: UsageFilters;
  options: UsageFilterOptions;
  onFilterChange: (dimension: UsageFilterDimension, value: string | null) => void;
  onClearAll: () => void;
}) {
  const [moreOpen, setMoreOpen] = useState(false);

  const advancedActiveCount = (['connection', 'operation', 'surface'] as const)
    .filter((dimension) => filters[dimension] !== ALL_FILTER).length;
  const activeChips = (Object.keys(DIMENSION_LABELS) as UsageFilterDimension[])
    .flatMap((dimension) => {
      const value = filters[dimension];
      return value === ALL_FILTER ? [] : [{
        dimension,
        dimensionLabel: DIMENSION_LABELS[dimension],
        valueLabel: chipLabel(dimension, value, options),
      }];
    });

  return (
    <div className="space-y-3" data-testid="usage-filter-bar">
      <div className="flex flex-wrap items-end gap-3">
        <div className="w-full space-y-1.5 sm:w-52">
          <Label htmlFor="usage-provider-filter" className="text-xs">Provider</Label>
          <Select
            value={selectFilterValue(filters.provider)}
            onValueChange={(value) => onFilterChange('provider', parseSelectFilterValue(value))}
          >
            <SelectTrigger id="usage-provider-filter" data-testid="usage-provider-filter">
              <SelectValue placeholder="All providers" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={SELECT_ALL_FILTER}>All providers</SelectItem>
              {options.providers.map((provider) => (
                <SelectItem key={provider} value={selectFilterValue(provider)}>{provider}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="w-full space-y-1.5 sm:w-64">
          <Label htmlFor="usage-model-filter" className="text-xs">Model</Label>
          <Select
            value={selectFilterValue(filters.model)}
            onValueChange={(value) => onFilterChange('model', parseSelectFilterValue(value))}
          >
            <SelectTrigger id="usage-model-filter" data-testid="usage-model-filter">
              <SelectValue placeholder="All models" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={SELECT_ALL_FILTER}>All models</SelectItem>
              {options.models.map((model) => (
                <SelectItem key={model} value={selectFilterValue(model)}>{model}</SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <Popover open={moreOpen} onOpenChange={setMoreOpen}>
          <PopoverTrigger asChild>
            <Button type="button" variant="outline" size="sm" className="h-9" data-testid="usage-more-filters-trigger">
              <SlidersHorizontal aria-hidden />
              More filters
              {advancedActiveCount > 0 && (
                <Badge variant="secondary" className="ml-1 px-1.5 py-0 text-[11px]" data-testid="usage-more-filters-count">
                  {advancedActiveCount}
                </Badge>
              )}
            </Button>
          </PopoverTrigger>
          <PopoverContent align="start" className="w-80 space-y-3">
            <div className="space-y-1.5">
              <Label htmlFor="usage-connection-filter" className="text-xs">Connection</Label>
              <Select
                value={selectFilterValue(filters.connection)}
                onValueChange={(value) => onFilterChange('connection', parseSelectFilterValue(value))}
              >
                <SelectTrigger id="usage-connection-filter" data-testid="usage-connection-filter">
                  <SelectValue placeholder="All connections" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={SELECT_ALL_FILTER}>All connections</SelectItem>
                  {options.connections.map(([value, label]) => (
                    <SelectItem key={value} value={selectFilterValue(value)}>{label}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="usage-operation-filter" className="text-xs">Operation</Label>
              <Select
                value={selectFilterValue(filters.operation)}
                onValueChange={(value) => onFilterChange('operation', parseSelectFilterValue(value))}
              >
                <SelectTrigger id="usage-operation-filter" data-testid="usage-operation-filter">
                  <SelectValue placeholder="All operations" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={SELECT_ALL_FILTER}>All operations</SelectItem>
                  {options.operations.map(([operation, label]) => (
                    <SelectItem key={operation} value={selectFilterValue(operation)}>{label}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="usage-surface-filter" className="text-xs">API surface</Label>
              <Select
                value={selectFilterValue(filters.surface)}
                onValueChange={(value) => onFilterChange('surface', parseSelectFilterValue(value))}
              >
                <SelectTrigger id="usage-surface-filter" data-testid="usage-surface-filter">
                  <SelectValue placeholder="All API surfaces" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={SELECT_ALL_FILTER}>All API surfaces</SelectItem>
                  {options.surfaces.map(([value, label]) => (
                    <SelectItem key={value} value={selectFilterValue(value)}>{label}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </PopoverContent>
        </Popover>
      </div>

      {activeChips.length > 0 && (
        <div className="flex flex-wrap items-center gap-2" data-testid="usage-filter-chips">
          {activeChips.map((chip) => (
            <span
              key={chip.dimension}
              className="inline-flex max-w-full items-center gap-1 rounded-full border bg-muted/30 py-0.5 pl-2.5 pr-1 text-xs"
              data-testid={`usage-filter-chip-${chip.dimension}`}
            >
              <span className="text-muted-foreground">{chip.dimensionLabel}:</span>
              <span className="truncate font-medium" title={chip.valueLabel}>{chip.valueLabel}</span>
              <button
                type="button"
                className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                aria-label={`Remove ${chip.dimensionLabel} filter`}
                onClick={() => onFilterChange(chip.dimension, ALL_FILTER)}
              >
                <X className="h-3.5 w-3.5" aria-hidden />
              </button>
            </span>
          ))}
          <Button type="button" variant="ghost" size="sm" onClick={onClearAll}>
            Clear filters
          </Button>
        </div>
      )}
    </div>
  );
}
