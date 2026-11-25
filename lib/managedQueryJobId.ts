export const MANAGED_QUERY_JOB_ID_PREFIX = 'qj-';
export const MAX_MANAGED_QUERY_JOB_ID_LENGTH = 96;

/**
 * Returns true for legacy placeholder values that can never identify a real
 * app-managed query job.
 */
export function isQueryJobIdSentinel(value: unknown): boolean {
  if (typeof value !== 'string') return false;
  let normalized = value.trim().toLowerCase();
  if (!normalized) return false;
  if (normalized.startsWith(MANAGED_QUERY_JOB_ID_PREFIX)) {
    normalized = normalized.slice(MANAGED_QUERY_JOB_ID_PREFIX.length);
  }
  if (
    normalized === 'nil'
    || normalized === 'null'
    || normalized === '<nil>'
    || normalized === 'none'
    || normalized === 'undefined'
  ) {
    return true;
  }
  return /0/.test(normalized) && /^[0_-]+$/.test(normalized);
}

/**
 * Normalizes app-owned durable query identities. Provider-native ids and
 * historical nil/all-zero placeholders return undefined and must not be
 * polled, displayed as managed ids, or sent to the cancellation endpoint.
 */
export function normalizeManagedQueryJobId(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined;
  const id = value.trim();
  if (
    id.length <= MANAGED_QUERY_JOB_ID_PREFIX.length
    || id.length > MAX_MANAGED_QUERY_JOB_ID_LENGTH
    || !id.startsWith(MANAGED_QUERY_JOB_ID_PREFIX)
    || isQueryJobIdSentinel(id)
  ) {
    return undefined;
  }
  const payload = id.slice(MANAGED_QUERY_JOB_ID_PREFIX.length);
  if (!/^[A-Za-z0-9][A-Za-z0-9_-]*$/.test(payload)) return undefined;
  if (!/[A-Za-z1-9]/.test(payload)) return undefined;
  return id;
}

export function isValidManagedQueryJobId(value: unknown): boolean {
  return normalizeManagedQueryJobId(value) !== undefined;
}
