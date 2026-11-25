import type { ProviderId } from './types';

export function proxyFetch(targetUrl: string, init: RequestInit, meta: { providerId: ProviderId; profileId?: string | null }): Promise<Response> {
  const headers = new Headers(init.headers || {});
  headers.set('X-AI-Proxy-Target', targetUrl);
  headers.set('X-AI-Proxy-Provider', meta.providerId);
  if (meta.providerId === 'compatible' && meta.profileId) {
    headers.set('X-AI-Proxy-Profile', meta.profileId);
  }

  // Same-origin call to the local Go server (or Vite dev proxy).
  return fetch('/api/proxy', { ...init, headers });
}

