const DESKTOP_TOKEN_HASH_KEY = 'desktop_token';
const DESKTOP_TOKEN_STORAGE_KEY = 'ai_analyst_desktop_token_v1';
const DESKTOP_TOKEN_HEADER = 'X-AI-Desktop-Token';

let desktopToken: string | null = null;
let installed = false;

/**
 * Patches window.fetch so same-origin `/api/` requests carry the per-launch
 * desktop token as X-AI-Desktop-Token. The wrapper installs unconditionally
 * (even when no token is known yet) and resolves the token per request, so a
 * token that only shows up after boot is honored without a reload.
 */
export function installDesktopFetchAuth(): void {
  if (typeof window === 'undefined') return;
  refreshDesktopToken();
  if (installed) return;
  installed = true;

  const originalFetch = window.fetch.bind(window);
  window.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    if (!isSameOriginApiRequest(input)) {
      return originalFetch(input, init);
    }

    // Resolve lazily per request so a token that becomes available after boot
    // (hash navigation, sessionStorage written later in this tab) starts
    // working without a reload. Tokenless windows fall through unmodified and
    // surface the backend guard's 401.
    const token = resolveDesktopToken();
    if (!token) {
      return originalFetch(input, init);
    }

    const request = input instanceof Request ? input : null;
    const headers = new Headers(init?.headers || request?.headers || undefined);
    if (!headers.has(DESKTOP_TOKEN_HEADER)) {
      headers.set(DESKTOP_TOKEN_HEADER, token);
    }

    if (request) {
      return originalFetch(new Request(request, { ...init, headers }));
    }
    return originalFetch(input, { ...init, headers });
  }) as typeof window.fetch;

  window.addEventListener('hashchange', refreshDesktopToken);
}

export function getDesktopToken(): string | null {
  if (typeof window === 'undefined') return desktopToken;
  return resolveDesktopToken();
}

/**
 * Cache-first token lookup: serve the in-memory token when present and only
 * fall back to re-reading the URL hash / sessionStorage while the cache is
 * empty. The hashchange listener handles tokens replaced after boot.
 */
function resolveDesktopToken(): string | null {
  if (!desktopToken) refreshDesktopToken();
  return desktopToken;
}

function loadDesktopToken(): string | null {
  try {
    const stored = window.sessionStorage.getItem(DESKTOP_TOKEN_STORAGE_KEY);
    return sanitizeToken(stored);
  } catch {
    return null;
  }
}

function refreshDesktopToken(): void {
  const nextToken = extractDesktopToken() || loadDesktopToken();
  if (!nextToken || nextToken === desktopToken) return;
  desktopToken = nextToken;
  try {
    window.sessionStorage.setItem(DESKTOP_TOKEN_STORAGE_KEY, nextToken);
  } catch {
    // Ignore storage failures; the in-memory token still covers this load.
  }
}

function extractDesktopToken(): string | null {
  const hash = window.location.hash || '';
  if (!hash.includes(DESKTOP_TOKEN_HASH_KEY)) return null;

  const raw = hash.startsWith('#') ? hash.slice(1) : hash;
  const params = new URLSearchParams(raw);
  const token = sanitizeToken(params.get(DESKTOP_TOKEN_HASH_KEY));
  if (!token) return null;

  params.delete(DESKTOP_TOKEN_HASH_KEY);
  const nextHash = params.toString();
  const nextUrl =
    window.location.pathname +
    window.location.search +
    (nextHash ? `#${nextHash}` : '');
  try {
    window.history.replaceState(window.history.state, document.title, nextUrl);
  } catch {
    // Leave the URL alone if the history API is unavailable.
  }
  return token;
}

function sanitizeToken(raw: string | null | undefined): string | null {
  const token = String(raw || '').trim();
  if (!/^[a-f0-9]{64}$/i.test(token)) return null;
  return token;
}

function isSameOriginApiRequest(input: RequestInfo | URL): boolean {
  const raw =
    typeof input === 'string'
      ? input
      : input instanceof URL
        ? input.toString()
        : input.url;

  try {
    const url = new URL(raw, window.location.origin);
    return url.origin === window.location.origin && url.pathname.startsWith('/api/');
  } catch {
    return typeof raw === 'string' && raw.startsWith('/api/');
  }
}
