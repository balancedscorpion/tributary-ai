export const TOKEN_USAGE_SCHEMA_VERSION = 2;

export interface TokenUsageRange {
  from: string;
  to: string;
  timezone: string;
}

export interface TokenUsageCounts {
  inputTokens: number | null;
  outputTokens: number | null;
  providerTotalTokens: number | null;
  derivedTotalTokens: number | null;
  totalTokens: number | null;
  cachedInputTokens: number | null;
  cacheWriteTokens: number | null;
  reasoningTokens: number | null;
  lowerBoundInputTokens: number | null;
  lowerBoundOutputTokens: number | null;
  lowerBoundProviderTotalTokens: number | null;
  lowerBoundDerivedTotalTokens: number | null;
  lowerBoundTotalTokens: number | null;
  lowerBoundCachedInputTokens: number | null;
  lowerBoundCacheWriteTokens: number | null;
  lowerBoundReasoningTokens: number | null;
  invocationCount: number;
  upstreamRequestCount: number;
  measuredInvocationCount: number;
  inputMeasuredInvocationCount: number;
  outputMeasuredInvocationCount: number;
  providerTotalMeasuredInvocationCount: number;
  derivedTotalMeasuredInvocationCount: number;
  totalMeasuredInvocationCount: number;
  cachedInputMeasuredInvocationCount: number;
  cacheWriteMeasuredInvocationCount: number;
  reasoningMeasuredInvocationCount: number;
  lowerBoundInvocationCount: number;
  lowerBoundInputInvocationCount: number;
  lowerBoundOutputInvocationCount: number;
  lowerBoundProviderTotalInvocationCount: number;
  lowerBoundDerivedTotalInvocationCount: number;
  lowerBoundTotalInvocationCount: number;
  lowerBoundCachedInputInvocationCount: number;
  lowerBoundCacheWriteInvocationCount: number;
  lowerBoundReasoningInvocationCount: number;
  missingUsageCount: number;
  failedCount: number;
  cancelledCount: number;
  interruptedCount: number;
  providerReportedCount: number;
  derivedUsageCount: number;
  legacyPartialCount: number;
}

export interface TokenUsageBucket extends TokenUsageCounts {
  date: string;
  providerId: string;
  connectionId: string | null;
  profileId: string | null;
  model: string;
  operation: string;
  plannedApiSurface: string | null;
  effectiveApiSurface: string | null;
}

export type TokenUsageHealthStatus = 'healthy' | 'degraded' | 'unavailable';

export interface TokenUsageHealth {
  status: TokenUsageHealthStatus;
  message: string | null;
  droppedInvocationCount: number;
  runningInvocationCount: number;
  staleInvocationCount: number;
  pendingInvocationCount: number;
  terminalPendingCount: number;
  quarantinedRecordCount: number;
  oldestPendingAt: string | null;
}

export interface TokenUsageReport {
  schemaVersion: number;
  reportComplete: boolean;
  health: TokenUsageHealth;
  retention: {
    mode: 'indefinite';
    deleteWithThread: false;
  };
  range: TokenUsageRange;
  totals: TokenUsageCounts;
  buckets: TokenUsageBucket[];
  trackingStartedAt: string | null;
  generatedAt: string;
}

export interface TokenUsageQuery {
  from: string;
  to: string;
  timezone: string;
}

export class TokenUsageApiError extends Error {
  readonly status: number;
  readonly unavailable: boolean;

  constructor(message: string, status: number, unavailable = false) {
    super(message);
    this.name = 'TokenUsageApiError';
    this.status = status;
    this.unavailable = unavailable;
  }
}

function invalidResponse(message: string): never {
  throw new TokenUsageApiError(`The usage service returned ${message}.`, 502);
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function hasOwn(record: Record<string, unknown>, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(record, key);
}

function requiredRecord(value: unknown, label: string): Record<string, unknown> {
  if (!isRecord(value)) invalidResponse(`an invalid ${label}`);
  return value;
}

function requiredString(record: Record<string, unknown>, key: string, label: string): string {
  const value = record[key];
  if (typeof value !== 'string' || !value.trim()) invalidResponse(`an invalid ${label}`);
  return value.trim();
}

function requiredNullableString(record: Record<string, unknown>, key: string, label: string): string | null {
  if (!hasOwn(record, key)) invalidResponse(`a missing ${label}`);
  const value = record[key];
  if (value === null) return null;
  if (typeof value !== 'string' || !value.trim()) invalidResponse(`an invalid ${label}`);
  return value.trim();
}

function requiredBoolean(record: Record<string, unknown>, key: string, label: string): boolean {
  const value = record[key];
  if (typeof value !== 'boolean') invalidResponse(`an invalid ${label}`);
  return value;
}

function requiredSafeInteger(record: Record<string, unknown>, key: string, label: string): number {
  if (!hasOwn(record, key)) invalidResponse(`a missing ${label}`);
  const value = record[key];
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) {
    invalidResponse(`an invalid ${label}`);
  }
  return value;
}

function requiredNullableSafeInteger(
  record: Record<string, unknown>,
  key: string,
  label: string,
): number | null {
  if (!hasOwn(record, key)) invalidResponse(`a missing ${label}`);
  const value = record[key];
  if (value === null) return null;
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) {
    invalidResponse(`an invalid ${label}`);
  }
  return value;
}

function validCalendarDate(value: string): boolean {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(value)) return false;
  const [year, month, day] = value.split('-').map(Number);
  const date = new Date(Date.UTC(year, month - 1, day));
  return date.getUTCFullYear() === year
    && date.getUTCMonth() === month - 1
    && date.getUTCDate() === day;
}

function normalizeCounts(value: unknown, label: string): TokenUsageCounts {
  const raw = requiredRecord(value, label);
  const counts: TokenUsageCounts = {
    inputTokens: requiredNullableSafeInteger(raw, 'input_tokens', `${label} input token count`),
    outputTokens: requiredNullableSafeInteger(raw, 'output_tokens', `${label} output token count`),
    providerTotalTokens: requiredNullableSafeInteger(raw, 'provider_total_tokens', `${label} provider total tokens`),
    derivedTotalTokens: requiredNullableSafeInteger(raw, 'derived_total_tokens', `${label} derived total tokens`),
    totalTokens: requiredNullableSafeInteger(raw, 'total_tokens', `${label} total token count`),
    cachedInputTokens: requiredNullableSafeInteger(raw, 'cached_input_tokens', `${label} cached input token count`),
    cacheWriteTokens: requiredNullableSafeInteger(raw, 'cache_write_tokens', `${label} cache write token count`),
    reasoningTokens: requiredNullableSafeInteger(raw, 'reasoning_tokens', `${label} reasoning token count`),
    lowerBoundInputTokens: requiredNullableSafeInteger(raw, 'lower_bound_input_tokens', `${label} lower-bound input token count`),
    lowerBoundOutputTokens: requiredNullableSafeInteger(raw, 'lower_bound_output_tokens', `${label} lower-bound output token count`),
    lowerBoundProviderTotalTokens: requiredNullableSafeInteger(raw, 'lower_bound_provider_total_tokens', `${label} lower-bound provider total tokens`),
    lowerBoundDerivedTotalTokens: requiredNullableSafeInteger(raw, 'lower_bound_derived_total_tokens', `${label} lower-bound derived total tokens`),
    lowerBoundTotalTokens: requiredNullableSafeInteger(raw, 'lower_bound_total_tokens', `${label} lower-bound total token count`),
    lowerBoundCachedInputTokens: requiredNullableSafeInteger(raw, 'lower_bound_cached_input_tokens', `${label} lower-bound cached input token count`),
    lowerBoundCacheWriteTokens: requiredNullableSafeInteger(raw, 'lower_bound_cache_write_tokens', `${label} lower-bound cache write token count`),
    lowerBoundReasoningTokens: requiredNullableSafeInteger(raw, 'lower_bound_reasoning_tokens', `${label} lower-bound reasoning token count`),
    invocationCount: requiredSafeInteger(raw, 'invocation_count', `${label} invocation count`),
    upstreamRequestCount: requiredSafeInteger(raw, 'upstream_request_count', `${label} upstream request count`),
    measuredInvocationCount: requiredSafeInteger(raw, 'measured_invocation_count', `${label} measured invocation count`),
    inputMeasuredInvocationCount: requiredSafeInteger(raw, 'input_measured_invocation_count', `${label} input coverage count`),
    outputMeasuredInvocationCount: requiredSafeInteger(raw, 'output_measured_invocation_count', `${label} output coverage count`),
    providerTotalMeasuredInvocationCount: requiredSafeInteger(raw, 'provider_total_measured_invocation_count', `${label} provider total coverage count`),
    derivedTotalMeasuredInvocationCount: requiredSafeInteger(raw, 'derived_total_measured_invocation_count', `${label} derived total coverage count`),
    totalMeasuredInvocationCount: requiredSafeInteger(raw, 'total_measured_invocation_count', `${label} total coverage count`),
    cachedInputMeasuredInvocationCount: requiredSafeInteger(raw, 'cached_input_measured_invocation_count', `${label} cached input coverage count`),
    cacheWriteMeasuredInvocationCount: requiredSafeInteger(raw, 'cache_write_measured_invocation_count', `${label} cache write coverage count`),
    reasoningMeasuredInvocationCount: requiredSafeInteger(raw, 'reasoning_measured_invocation_count', `${label} reasoning coverage count`),
    lowerBoundInvocationCount: requiredSafeInteger(raw, 'lower_bound_invocation_count', `${label} lower-bound invocation count`),
    lowerBoundInputInvocationCount: requiredSafeInteger(raw, 'lower_bound_input_invocation_count', `${label} lower-bound input coverage count`),
    lowerBoundOutputInvocationCount: requiredSafeInteger(raw, 'lower_bound_output_invocation_count', `${label} lower-bound output coverage count`),
    lowerBoundProviderTotalInvocationCount: requiredSafeInteger(raw, 'lower_bound_provider_total_invocation_count', `${label} lower-bound provider total coverage count`),
    lowerBoundDerivedTotalInvocationCount: requiredSafeInteger(raw, 'lower_bound_derived_total_invocation_count', `${label} lower-bound derived total coverage count`),
    lowerBoundTotalInvocationCount: requiredSafeInteger(raw, 'lower_bound_total_invocation_count', `${label} lower-bound total coverage count`),
    lowerBoundCachedInputInvocationCount: requiredSafeInteger(raw, 'lower_bound_cached_input_invocation_count', `${label} lower-bound cached input coverage count`),
    lowerBoundCacheWriteInvocationCount: requiredSafeInteger(raw, 'lower_bound_cache_write_invocation_count', `${label} lower-bound cache write coverage count`),
    lowerBoundReasoningInvocationCount: requiredSafeInteger(raw, 'lower_bound_reasoning_invocation_count', `${label} lower-bound reasoning coverage count`),
    missingUsageCount: requiredSafeInteger(raw, 'missing_usage_count', `${label} missing usage count`),
    failedCount: requiredSafeInteger(raw, 'failed_count', `${label} failed count`),
    cancelledCount: requiredSafeInteger(raw, 'cancelled_count', `${label} cancelled count`),
    interruptedCount: requiredSafeInteger(raw, 'interrupted_count', `${label} interrupted count`),
    providerReportedCount: requiredSafeInteger(raw, 'provider_reported_count', `${label} provider-reported count`),
    derivedUsageCount: requiredSafeInteger(raw, 'derived_usage_count', `${label} derived usage count`),
    legacyPartialCount: requiredSafeInteger(raw, 'legacy_partial_count', `${label} legacy partial count`),
  };

  const coverageCounts = [
    counts.measuredInvocationCount,
    counts.inputMeasuredInvocationCount,
    counts.outputMeasuredInvocationCount,
    counts.providerTotalMeasuredInvocationCount,
    counts.derivedTotalMeasuredInvocationCount,
    counts.totalMeasuredInvocationCount,
    counts.cachedInputMeasuredInvocationCount,
    counts.cacheWriteMeasuredInvocationCount,
    counts.reasoningMeasuredInvocationCount,
    counts.lowerBoundInvocationCount,
    counts.lowerBoundInputInvocationCount,
    counts.lowerBoundOutputInvocationCount,
    counts.lowerBoundProviderTotalInvocationCount,
    counts.lowerBoundDerivedTotalInvocationCount,
    counts.lowerBoundTotalInvocationCount,
    counts.lowerBoundCachedInputInvocationCount,
    counts.lowerBoundCacheWriteInvocationCount,
    counts.lowerBoundReasoningInvocationCount,
    counts.missingUsageCount,
    counts.failedCount,
    counts.cancelledCount,
    counts.interruptedCount,
    counts.providerReportedCount,
    counts.derivedUsageCount,
    counts.legacyPartialCount,
  ];
  if (coverageCounts.some((count) => count > counts.invocationCount)) {
    invalidResponse(`${label} coverage counts that exceed its invocation count`);
  }
  if (counts.measuredInvocationCount !== counts.totalMeasuredInvocationCount) {
    invalidResponse(`${label} measured and total coverage counts that disagree`);
  }
  if (counts.totalMeasuredInvocationCount + counts.lowerBoundTotalInvocationCount + counts.missingUsageCount !== counts.invocationCount) {
    invalidResponse(`${label} total coverage that does not account for every invocation`);
  }
  if (counts.failedCount + counts.cancelledCount + counts.interruptedCount > counts.invocationCount) {
    invalidResponse(`${label} terminal status counts that exceed its invocation count`);
  }
  if (counts.providerReportedCount + counts.derivedUsageCount + counts.legacyPartialCount > counts.invocationCount) {
    invalidResponse(`${label} usage quality counts that exceed its invocation count`);
  }

  const nullableCoverage: Array<[number | null, number, string]> = [
    [counts.inputTokens, counts.inputMeasuredInvocationCount, 'input'],
    [counts.outputTokens, counts.outputMeasuredInvocationCount, 'output'],
    [counts.providerTotalTokens, counts.providerTotalMeasuredInvocationCount, 'provider total'],
    [counts.derivedTotalTokens, counts.derivedTotalMeasuredInvocationCount, 'derived total'],
    [counts.totalTokens, counts.totalMeasuredInvocationCount, 'total'],
    [counts.cachedInputTokens, counts.cachedInputMeasuredInvocationCount, 'cached input'],
    [counts.cacheWriteTokens, counts.cacheWriteMeasuredInvocationCount, 'cache write'],
    [counts.reasoningTokens, counts.reasoningMeasuredInvocationCount, 'reasoning'],
    [counts.lowerBoundInputTokens, counts.lowerBoundInputInvocationCount, 'lower-bound input'],
    [counts.lowerBoundOutputTokens, counts.lowerBoundOutputInvocationCount, 'lower-bound output'],
    [counts.lowerBoundProviderTotalTokens, counts.lowerBoundProviderTotalInvocationCount, 'lower-bound provider total'],
    [counts.lowerBoundDerivedTotalTokens, counts.lowerBoundDerivedTotalInvocationCount, 'lower-bound derived total'],
    [counts.lowerBoundTotalTokens, counts.lowerBoundTotalInvocationCount, 'lower-bound total'],
    [counts.lowerBoundCachedInputTokens, counts.lowerBoundCachedInputInvocationCount, 'lower-bound cached input'],
    [counts.lowerBoundCacheWriteTokens, counts.lowerBoundCacheWriteInvocationCount, 'lower-bound cache write'],
    [counts.lowerBoundReasoningTokens, counts.lowerBoundReasoningInvocationCount, 'lower-bound reasoning'],
  ];
  for (const [tokenCount, coverage, counter] of nullableCoverage) {
    if ((tokenCount === null) !== (coverage === 0)) {
      invalidResponse(`${label} ${counter} value and coverage that disagree`);
    }
  }
  return counts;
}

function normalizeHealth(value: unknown): TokenUsageHealth {
  const raw = requiredRecord(value, 'health object');
  const status = requiredString(raw, 'status', 'health status');
  if (status !== 'healthy' && status !== 'degraded' && status !== 'unavailable') {
    invalidResponse('an unknown health status');
  }
  const oldestPendingAt = requiredNullableString(raw, 'oldest_pending_at', 'oldest pending timestamp');
  if (oldestPendingAt && !Number.isFinite(Date.parse(oldestPendingAt))) {
    invalidResponse('an invalid oldest pending timestamp');
  }
  return {
    status,
    message: requiredNullableString(raw, 'message', 'health message'),
    droppedInvocationCount: requiredSafeInteger(raw, 'dropped_invocation_count', 'dropped invocation count'),
    runningInvocationCount: requiredSafeInteger(raw, 'running_invocation_count', 'running invocation count'),
    staleInvocationCount: requiredSafeInteger(raw, 'stale_invocation_count', 'stale invocation count'),
    pendingInvocationCount: requiredSafeInteger(raw, 'pending_invocation_count', 'pending invocation count'),
    terminalPendingCount: requiredSafeInteger(raw, 'terminal_pending_count', 'terminal pending count'),
    quarantinedRecordCount: requiredSafeInteger(raw, 'quarantined_record_count', 'quarantined record count'),
    oldestPendingAt,
  };
}

function errorMessage(payload: unknown, fallback: string): string {
  if (isRecord(payload)) {
    for (const key of ['error', 'message']) {
      const value = payload[key];
      if (typeof value === 'string' && value.trim()) return value.trim();
    }
    if (isRecord(payload.health) && typeof payload.health.message === 'string' && payload.health.message.trim()) {
      return payload.health.message.trim();
    }
  }
  return fallback;
}

function normalizeReport(payload: unknown, query: TokenUsageQuery): TokenUsageReport {
  const raw = requiredRecord(payload, 'response');
  if (raw.schema_version !== TOKEN_USAGE_SCHEMA_VERSION) {
    invalidResponse(`unsupported schema version ${String(raw.schema_version)}`);
  }
  if (!Array.isArray(raw.buckets)) invalidResponse('an invalid bucket list');

  const rawRange = requiredRecord(raw.range, 'range');
  const from = requiredString(rawRange, 'from', 'range start date');
  const to = requiredString(rawRange, 'to', 'range end date');
  const timezone = requiredString(rawRange, 'timezone', 'range timezone');
  if (!validCalendarDate(from) || !validCalendarDate(to) || from > to) {
    invalidResponse('an invalid date range');
  }
  if (from !== query.from || to !== query.to || timezone !== query.timezone) {
    invalidResponse('a range that does not match the request');
  }

  const buckets = raw.buckets.map((value, index): TokenUsageBucket => {
    const bucket = requiredRecord(value, `daily bucket ${index + 1}`);
    const date = requiredString(bucket, 'date', `daily bucket ${index + 1} date`);
    if (!validCalendarDate(date) || date < from || date > to) {
      invalidResponse(`a daily bucket with an out-of-range date`);
    }
    const connectionId = requiredNullableString(bucket, 'connection_id', `daily bucket ${index + 1} connection id`);
    const profileId = requiredNullableString(bucket, 'profile_id', `daily bucket ${index + 1} legacy profile id`);
    if (connectionId && profileId) {
      invalidResponse(`a daily bucket with both a connection id and legacy profile id`);
    }
    return {
      date,
      providerId: requiredString(bucket, 'provider_id', `daily bucket ${index + 1} provider id`),
      connectionId,
      profileId,
      model: requiredString(bucket, 'model', `daily bucket ${index + 1} model`),
      operation: requiredString(bucket, 'operation', `daily bucket ${index + 1} operation`),
      plannedApiSurface: requiredNullableString(bucket, 'planned_api_surface', `daily bucket ${index + 1} planned API surface`),
      effectiveApiSurface: requiredNullableString(bucket, 'effective_api_surface', `daily bucket ${index + 1} effective API surface`),
      ...normalizeCounts(bucket, `daily bucket ${index + 1}`),
    };
  });

  const trackingStartedAt = requiredNullableString(raw, 'tracking_started_at', 'tracking start timestamp');
  const generatedAt = requiredString(raw, 'generated_at', 'generation timestamp');
  if ((trackingStartedAt && !Number.isFinite(Date.parse(trackingStartedAt))) || !Number.isFinite(Date.parse(generatedAt))) {
    invalidResponse('an invalid report timestamp');
  }

  const totals = normalizeCounts(raw.totals, 'report totals');
  const rawRetention = requiredRecord(raw.retention, 'retention policy');
  const retentionMode = requiredString(rawRetention, 'mode', 'retention mode');
  const deleteWithThread = requiredBoolean(rawRetention, 'delete_with_thread', 'delete-with-thread retention flag');
  if (retentionMode !== 'indefinite' || deleteWithThread) {
    invalidResponse('an unsupported retention policy');
  }
  const nullableAdditiveKeys = [
    'inputTokens',
    'outputTokens',
    'providerTotalTokens',
    'derivedTotalTokens',
    'totalTokens',
    'cachedInputTokens',
    'cacheWriteTokens',
    'reasoningTokens',
    'lowerBoundInputTokens',
    'lowerBoundOutputTokens',
    'lowerBoundProviderTotalTokens',
    'lowerBoundDerivedTotalTokens',
    'lowerBoundTotalTokens',
    'lowerBoundCachedInputTokens',
    'lowerBoundCacheWriteTokens',
    'lowerBoundReasoningTokens',
  ] as const;
  for (const key of nullableAdditiveKeys) {
    let bucketSum = 0;
    let hasValue = false;
    for (const bucket of buckets) {
      const value = bucket[key];
      if (value !== null) {
        bucketSum += value;
        hasValue = true;
      }
    }
    const normalizedSum = hasValue ? bucketSum : null;
    if ((normalizedSum !== null && !Number.isSafeInteger(normalizedSum)) || normalizedSum !== totals[key]) {
      invalidResponse(`report totals that do not match its daily buckets for ${key}`);
    }
  }

  const additiveKeys = [
    'invocationCount',
    'upstreamRequestCount',
    'measuredInvocationCount',
    'inputMeasuredInvocationCount',
    'outputMeasuredInvocationCount',
    'providerTotalMeasuredInvocationCount',
    'derivedTotalMeasuredInvocationCount',
    'totalMeasuredInvocationCount',
    'cachedInputMeasuredInvocationCount',
    'cacheWriteMeasuredInvocationCount',
    'reasoningMeasuredInvocationCount',
    'lowerBoundInvocationCount',
    'lowerBoundInputInvocationCount',
    'lowerBoundOutputInvocationCount',
    'lowerBoundProviderTotalInvocationCount',
    'lowerBoundDerivedTotalInvocationCount',
    'lowerBoundTotalInvocationCount',
    'lowerBoundCachedInputInvocationCount',
    'lowerBoundCacheWriteInvocationCount',
    'lowerBoundReasoningInvocationCount',
    'missingUsageCount',
    'failedCount',
    'cancelledCount',
    'interruptedCount',
    'providerReportedCount',
    'derivedUsageCount',
    'legacyPartialCount',
  ] as const;
  for (const key of additiveKeys) {
    const bucketSum = buckets.reduce((sum, bucket) => sum + bucket[key], 0);
    if (!Number.isSafeInteger(bucketSum) || bucketSum !== totals[key]) {
      invalidResponse(`report totals that do not match its daily buckets for ${key}`);
    }
  }

  return {
    schemaVersion: TOKEN_USAGE_SCHEMA_VERSION,
    reportComplete: requiredBoolean(raw, 'report_complete', 'report completeness flag'),
    health: normalizeHealth(raw.health),
    retention: { mode: 'indefinite', deleteWithThread: false },
    range: { from, to, timezone },
    totals,
    buckets,
    trackingStartedAt,
    generatedAt,
  };
}

async function readPayload(response: Response): Promise<unknown> {
  const text = await response.text().catch(() => '');
  if (!text.trim()) return null;
  try {
    return JSON.parse(text);
  } catch {
    return { error: text };
  }
}

/** Fetch a server-aggregated, calendar-day token usage report. */
export async function getDailyTokenUsage(
  query: TokenUsageQuery,
  signal?: AbortSignal,
): Promise<TokenUsageReport> {
  const params = new URLSearchParams({
    from: query.from,
    to: query.to,
    timezone: query.timezone,
  });
  const response = await fetch(`/api/usage/tokens/daily?${params.toString()}`, {
    method: 'GET',
    headers: { Accept: 'application/json' },
    signal,
  });
  const payload = await readPayload(response);
  if (!response.ok) {
    const unavailable = response.status === 404 || response.status === 501;
    throw new TokenUsageApiError(
      errorMessage(
        payload,
        unavailable
          ? 'Usage reporting is not available in this build.'
          : `Could not load token usage (HTTP ${response.status}).`,
      ),
      response.status,
      unavailable,
    );
  }
  return normalizeReport(payload, query);
}
