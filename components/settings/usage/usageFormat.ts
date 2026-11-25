import type { TokenUsageBucket } from '@/services/tokenUsageApi';

export type RangePreset = '7' | '30' | '90' | 'custom';

export interface DateRange {
  from: string;
  to: string;
}

export const MAX_USAGE_RANGE_DAYS = 366;
const DAY_MS = 86_400_000;

const integerFormatter = new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 });
const compactNumber = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });

export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

function localDateString(date: Date): string {
  const year = date.getFullYear();
  const month = String(date.getMonth() + 1).padStart(2, '0');
  const day = String(date.getDate()).padStart(2, '0');
  return `${year}-${month}-${day}`;
}

export function presetRange(days: number): DateRange {
  const to = new Date();
  const from = new Date(to);
  from.setDate(from.getDate() - Math.max(0, days - 1));
  return { from: localDateString(from), to: localDateString(to) };
}

function calendarDateValue(value: string): number | null {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return null;
  const [year, month, day] = value.split('-').map(Number);
  const timestamp = Date.UTC(year, month - 1, day);
  const date = new Date(timestamp);
  if (date.getUTCFullYear() !== year || date.getUTCMonth() !== month - 1 || date.getUTCDate() !== day) return null;
  return timestamp;
}

export function rangeValidationMessage(range: DateRange): string | null {
  const from = calendarDateValue(range.from);
  const to = calendarDateValue(range.to);
  if (from === null || to === null || from > to) {
    return 'Choose a valid start date on or before the end date.';
  }
  const inclusiveDays = Math.floor((to - from) / DAY_MS) + 1;
  if (inclusiveDays > MAX_USAGE_RANGE_DAYS) {
    return `Choose a date range of ${MAX_USAGE_RANGE_DAYS} days or fewer.`;
  }
  return null;
}

export function operationLabel(value: string): string {
  return value.replace(/[_-]+/g, ' ').replace(/\b\w/g, (letter) => letter.toUpperCase());
}

export function surfacePairKey(bucket: TokenUsageBucket): string {
  return JSON.stringify([bucket.plannedApiSurface, bucket.effectiveApiSurface]);
}

export function surfacePairLabel(planned: string | null, effective: string | null): string {
  if (planned && effective && planned !== effective) return `${planned} → ${effective}`;
  return effective || planned || 'Unreported surface';
}

export function connectionFilterKey(bucket: TokenUsageBucket): string {
  if (bucket.connectionId) return `connection:${bucket.connectionId}`;
  if (bucket.profileId) return `legacy:${bucket.profileId}`;
  return 'unbound';
}

export function connectionFilterLabel(bucket: TokenUsageBucket): string {
  if (bucket.connectionId) return bucket.connectionId;
  if (bucket.profileId) return `Legacy: ${bucket.profileId}`;
  return 'No connection identity';
}

export function formatCount(value: number): string {
  return integerFormatter.format(Math.max(0, value));
}

export function formatCompactCount(value: number): string {
  return compactNumber.format(Math.max(0, value));
}

export function tokenFloor(exact: number | null, lowerBound: number | null): number | null {
  if (exact === null && lowerBound === null) return null;
  return (exact || 0) + (lowerBound || 0);
}

export function formatTokenFloor(
  exact: number | null,
  lowerBound: number | null,
  exactCalls: number,
  lowerBoundCalls: number,
  invocationCount: number,
  reportComplete = true,
): string {
  const value = tokenFloor(exact, lowerBound);
  if (value === null) return '—';
  const coveredCalls = exactCalls + lowerBoundCalls;
  const partial = !reportComplete || lowerBoundCalls > 0 || coveredCalls < invocationCount;
  return partial ? `≥${formatCount(value)}` : formatCount(value);
}

/**
 * Display for a largest-remainder allocated integer share. Allocations that
 * would read untruthfully at the extremes are pinned: a nonzero part never
 * shows 0%, and a part never shows 100% while other nonzero parts exist.
 */
export function formatAllocatedShare(
  allocated: number,
  value: number,
  hasOtherNonzeroParts: boolean,
): string {
  if (allocated <= 0) return value > 0 ? '<1%' : '0%';
  if (allocated >= 100 && hasOtherNonzeroParts) return '>99%';
  return `${allocated}%`;
}

/**
 * Screen-reader expansion for a formatted token value: NVDA's default
 * punctuation level drops both '≥' (U+2265) and '—' (U+2014), so a floor
 * would sound exact and an unknown would read as silence. Returns null when
 * the plain value already reads correctly.
 */
export function tokenValueSrText(display: string): string | null {
  if (display === '—') return 'not recorded';
  if (display.startsWith('≥')) return `at least ${display.slice(1)}`;
  return null;
}

export function formatCalendarDate(value: string): string {
  try {
    return new Intl.DateTimeFormat(undefined, {
      day: 'numeric',
      month: 'short',
      year: 'numeric',
      // The server has already assigned this YYYY-MM-DD to the requested
      // calendar timezone. Format the label in UTC to avoid shifting it again.
      timeZone: 'UTC',
    }).format(new Date(`${value}T00:00:00Z`));
  } catch {
    return value;
  }
}

export function formatTimestamp(value: string | null, timezone: string): string | null {
  if (!value) return null;
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return null;
  try {
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: 'medium',
      timeStyle: 'short',
      timeZone: timezone,
    }).format(date);
  } catch {
    return date.toLocaleString();
  }
}
