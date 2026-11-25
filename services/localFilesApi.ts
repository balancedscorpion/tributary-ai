// API client for grant-scoped local file preview streaming.
//
// Phase 2 of the local-files grants plan adds a native preview endpoint that
// re-checks the grant server-side and streams the file without copying it
// into the app:
//   GET /api/local-files/grants/{grantId}/file?ref=...
//
// Capability is reported via `LocalFilesApiUnavailableError` (the
// services/githubReposApi.ts pattern) so renderers can keep the preview
// affordance honest. A plain 404 is ambiguous here — it is both "this build
// has no stream endpoint" (bare mux text response) and "this file/grant no
// longer exists" (the live endpoint's JSON error) — so the client tells them
// apart by body shape and by whether the endpoint has answered before.

export class LocalFilesApiUnavailableError extends Error {
  constructor(message = 'Local file preview streaming is not available in this build.') {
    super(message);
    this.name = 'LocalFilesApiUnavailableError';
  }
}

export function isLocalFilesApiUnavailable(error: unknown): boolean {
  return error instanceof LocalFilesApiUnavailableError;
}

// Set once any request reaches the real endpoint; after that a 404 always
// means the file or grant, never the build.
let streamEndpointSeen = false;

/** Test hook: forget endpoint capability between mocked scenarios. */
export function resetLocalFilesApiCache(): void {
  streamEndpointSeen = false;
}

export function localGrantFileRawUrl(grantId: string, fileRef: string): string {
  const params = new URLSearchParams({ ref: fileRef });
  return `/api/local-files/grants/${encodeURIComponent(grantId)}/file?${params.toString()}`;
}

export interface LocalGrantFilePreview {
  /** Extracted/streamed text when the payload is text-like; undefined otherwise. */
  text?: string;
  mimeType?: string;
  byteSize?: number;
  /** True when the body was not inlined; `reason` says why. */
  binary: boolean;
  reason?: 'binary' | 'too-large';
}

const TEXT_MIME_HINTS = [
  'text/',
  'application/json',
  'application/xml',
  'application/javascript',
  'application/x-sh',
  'application/x-yaml',
  'application/toml',
  'image/svg',
];
const MAX_INLINE_PREVIEW_BYTES = 1_048_576; // 1 MiB guard for inline text previews.

/**
 * Fetch a bounded preview of a granted local file. Throws
 * `LocalFilesApiUnavailableError` when the build lacks the stream endpoint,
 * and a plain Error (with honest copy) when the endpoint exists but the file
 * or grant does not. The endpoint is read-only and re-validates the grant on
 * every call, so the client sends only the grant id and the relative file
 * ref — never an absolute path.
 */
export async function fetchLocalGrantFilePreview(
  grantId: string,
  fileRef: string,
  options: { signal?: AbortSignal } = {},
): Promise<LocalGrantFilePreview> {
  let res: Response;
  try {
    res = await fetch(localGrantFileRawUrl(grantId, fileRef), { signal: options.signal });
  } catch (e: any) {
    throw new Error(e?.message || 'Network error');
  }
  if (res.status === 501) {
    throw new LocalFilesApiUnavailableError();
  }
  if (res.status === 404) {
    const contentType = (res.headers.get('Content-Type') || '').toLowerCase();
    if (streamEndpointSeen || contentType.includes('application/json')) {
      const data = await res.json().catch(() => ({}));
      throw new Error(
        (data && (data.error || data.message)) ||
          'This file is no longer at that path. Ask the assistant to search again.',
      );
    }
    // Bare mux 404 (no JSON body): the route does not exist in this build.
    throw new LocalFilesApiUnavailableError();
  }
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error((data && (data.error || data.message)) || `HTTP ${res.status}`);
  }
  streamEndpointSeen = true;
  const mimeType = (res.headers.get('Content-Type') || '').split(';')[0].trim() || undefined;
  const lengthHeader = Number(res.headers.get('Content-Length') || '');
  const byteSize = Number.isFinite(lengthHeader) && lengthHeader > 0 ? lengthHeader : undefined;
  const textLike = !mimeType || TEXT_MIME_HINTS.some((hint) => mimeType.startsWith(hint));
  if (!textLike) {
    return { mimeType, byteSize, binary: true, reason: 'binary' };
  }
  if (byteSize !== undefined && byteSize > MAX_INLINE_PREVIEW_BYTES) {
    return { mimeType, byteSize, binary: true, reason: 'too-large' };
  }
  const text = await res.text();
  return { text, mimeType, byteSize: byteSize ?? text.length, binary: false };
}
