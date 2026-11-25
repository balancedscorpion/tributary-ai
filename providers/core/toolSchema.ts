export function normalizeToolParameters(parameters?: Record<string, any> | null): Record<string, any> {
  const out: Record<string, any> = { ...(parameters || {}) };
  if (typeof out.type !== 'string' || out.type.trim() === '') {
    out.type = 'object';
  }
  if (!Object.prototype.hasOwnProperty.call(out, 'properties')) {
    out.properties = {};
  }
  return out;
}
