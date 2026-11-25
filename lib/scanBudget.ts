// BigQuery scan-budget policy helpers.
//
// Two byte thresholds live on the BigQuery connection slice
// (dataSources.bigquery): estimated scans at or below `automaticScanBytes`
// (default 100 GiB) run without asking; estimates above it pause for a
// one-time user approval. `approvedScanBytes`, when set, is a hard ceiling —
// estimates above it are blocked without asking. When it is missing there is
// no ceiling: every oversized estimate still asks first.
//
// Pure module (no runtime imports) so regression tests can compile and
// execute it standalone — keep it dependency-free.

export const GIBIBYTE = 1024 * 1024 * 1024;

/**
 * The effective automatic limit when none is configured. Must stay equal to
 * DEFAULT_DATABASE_QUERY_POLICY.bigQueryMaximumBytesBilled (constants.ts),
 * which imports this constant.
 */
export const DEFAULT_BIGQUERY_AUTOMATIC_SCAN_BYTES = 100 * GIBIBYTE;

export interface ScanBudget {
  automaticScanBytes?: number;
  approvedScanBytes?: number;
}

/** Positive finite byte count, else undefined. Accepts numeric strings (JSON int64s). */
export function sanitizeScanBytes(value: unknown): number | undefined {
  const n = typeof value === 'string' && value.trim() !== '' ? Number(value) : value;
  if (typeof n !== 'number' || !Number.isFinite(n) || n <= 0) return undefined;
  return Math.round(n);
}

/**
 * Enforce the invariant persisted settings always satisfy: the approved
 * ceiling is never below the (effective) automatic limit. An approved ceiling
 * set while the automatic limit is blank clamps against the 100 GiB default,
 * because that is the limit actually in force.
 */
export function normalizeScanBudget(automatic: unknown, approved: unknown): ScanBudget {
  const automaticScanBytes = sanitizeScanBytes(automatic);
  let approvedScanBytes = sanitizeScanBytes(approved);
  if (approvedScanBytes !== undefined) {
    const floor = automaticScanBytes ?? DEFAULT_BIGQUERY_AUTOMATIC_SCAN_BYTES;
    if (approvedScanBytes < floor) approvedScanBytes = floor;
  }
  return {
    ...(automaticScanBytes !== undefined ? { automaticScanBytes } : {}),
    ...(approvedScanBytes !== undefined ? { approvedScanBytes } : {}),
  };
}

/** The automatic limit actually in force (configured value or the 100 GiB default). */
export function effectiveAutomaticScanBytes(budget?: ScanBudget | null): number {
  return sanitizeScanBytes(budget?.automaticScanBytes) ?? DEFAULT_BIGQUERY_AUTOMATIC_SCAN_BYTES;
}

/** Bytes → GiB string for a policy input value: '' when unset, else '100', '1.5', '0.25'. */
export function bytesToGibInput(bytes?: number): string {
  const n = sanitizeScanBytes(bytes);
  if (n === undefined) return '';
  return String(Math.round((n / GIBIBYTE) * 100) / 100);
}

/**
 * Parse a user-typed GiB amount into bytes.
 * Blank → undefined (use the default); unparseable or non-positive → null.
 */
export function parseGibInput(text: string): number | undefined | null {
  const trimmed = (text ?? '').trim().replace(',', '.');
  if (!trimmed) return undefined;
  if (!/^\d+(\.\d+)?$/.test(trimmed)) return null;
  const gib = Number(trimmed);
  if (!Number.isFinite(gib) || gib <= 0) return null;
  return Math.round(gib * GIBIBYTE);
}

/**
 * Human scan size for cost copy: binary units with honest suffixes
 * ("132.5 GiB", "820 MiB", "1.2 TiB"). Unlike formatBytes (lib/formatters),
 * which pairs binary math with decimal-looking labels, cost authorization
 * copy must name the exact unit BigQuery bills against.
 */
export function formatScanBytes(bytes: unknown): string {
  const n = sanitizeScanBytes(bytes);
  if (n === undefined) return '';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
  let value = n;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  const rounded = unit === 0 ? Math.round(value) : Math.round(value * 10) / 10;
  return `${rounded} ${units[unit]}`;
}
