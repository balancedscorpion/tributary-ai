export async function openExternalUrl(rawUrl: string): Promise<boolean> {
  const url = rawUrl.trim();
  if (!/^https?:\/\//i.test(url)) return false;

  const isTauri = typeof window !== 'undefined' && Boolean((window as any).__TAURI_INTERNALS__);
  if (isTauri) {
    try {
      const shell = await import('@tauri-apps/plugin-shell');
      await shell.open(url);
      return true;
    } catch {
      // Fall through to the browser path; the UI still shows a manual link.
    }
  }

  if (isLoopbackAppOrigin()) {
    const opened = await openExternalViaBackend(url);
    if (opened) return true;
  }

  try {
    const popup = window.open(url, '_blank', 'noopener,noreferrer');
    return Boolean(popup);
  } catch {
    return false;
  }
}

function isLoopbackAppOrigin(): boolean {
  if (typeof window === 'undefined') return false;
  const host = window.location.hostname;
  return host === 'localhost' || host === '127.0.0.1' || host === '::1' || host === '[::1]';
}

async function openExternalViaBackend(url: string): Promise<boolean> {
  try {
    const response = await fetch('/api/system/open-external', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url }),
    });
    return response.ok;
  } catch {
    return false;
  }
}
