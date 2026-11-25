const TOOL_NAME_SANITIZER = /[^a-zA-Z0-9_-]/g;

export class ToolNameMapper {
  private readonly providerToCanonical = new Map<string, string>();
  private readonly canonicalToProvider = new Map<string, string>();
  private readonly maxLength?: number;

  constructor(opts?: { maxLength?: number }) {
    this.maxLength = opts?.maxLength;
  }

  providerName(canonicalName: string): string {
    const canonical = String(canonicalName);
    const existing = this.canonicalToProvider.get(canonical);
    if (existing) return existing;

    const base = this.sanitize(canonical);
    let suffix = '';
    let candidate = this.applyMaxLength(base, suffix);
    let attempt = 1;
    while (true) {
      const current = this.providerToCanonical.get(candidate);
      if (!current || current === canonical) break;
      suffix = `_${attempt}`;
      candidate = this.applyMaxLength(base, suffix);
      attempt += 1;
    }

    this.providerToCanonical.set(candidate, canonical);
    this.canonicalToProvider.set(canonical, candidate);
    return candidate;
  }

  canonicalName(providerName: string): string {
    const provider = String(providerName);
    return this.providerToCanonical.get(provider) ?? provider;
  }

  getProviderToCanonical(): Record<string, string> {
    return Object.fromEntries(this.providerToCanonical.entries());
  }

  getCanonicalToProvider(): Record<string, string> {
    return Object.fromEntries(this.canonicalToProvider.entries());
  }

  private sanitize(name: string): string {
    const sanitized = name.replace(TOOL_NAME_SANITIZER, '_').replace(/^_+|_+$/g, '');
    return sanitized || 'tool';
  }

  private applyMaxLength(base: string, suffix: string): string {
    const max = this.maxLength;
    if (!max || max <= 0) return `${base}${suffix}`;

    if (base.length + suffix.length <= max) return `${base}${suffix}`;
    if (suffix.length >= max) return suffix.slice(-max);

    const available = max - suffix.length;
    const trimmedBase = (base.slice(0, available) || 'tool'.slice(0, available));
    return `${trimmedBase}${suffix}`;
  }
}

