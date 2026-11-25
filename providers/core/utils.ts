export const MAX_CHAR_LIMIT = 100000; // ~25k tokens rough guardrail.

export function safeTruncate(str: string): string {
  if (str.length <= MAX_CHAR_LIMIT) return str;
  return `${str.slice(0, MAX_CHAR_LIMIT)}... [Truncated by client: ${str.length} chars]`;
}

export function tryParseJsonObject(value: string): Record<string, unknown> | null {
  try {
    const parsed = JSON.parse(value);
    if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
      return parsed as Record<string, unknown>;
    }
    return null;
  } catch {
    return null;
  }
}

export function uuid(prefix = 'id'): string {
  // Prefer crypto.randomUUID when available (modern browsers).
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) {
    return `${prefix}_${crypto.randomUUID().replace(/-/g, '')}`;
  }
  // Fallback: not cryptographically strong, but stable enough for call IDs.
  return `${prefix}_${Math.random().toString(16).slice(2)}${Date.now().toString(16)}`;
}

