export type CompatibleEndpointApiType = 'chat_completions' | 'responses';

export const COMPATIBLE_ENDPOINT_SEGMENT: Record<CompatibleEndpointApiType, string> = {
  chat_completions: '/chat/completions',
  responses: '/responses',
};

function getDefaultUrlBase(): string {
  const origin =
    (typeof globalThis !== 'undefined'
      && (globalThis as any).location
      && typeof (globalThis as any).location.origin === 'string'
      && (globalThis as any).location.origin)
    || 'http://localhost';
  return origin;
}

function isRelativeUrl(raw: string): boolean {
  return raw.trim().startsWith('/');
}

function finalizeUrl(parsed: URL, original: string): string {
  const rendered = parsed.toString().replace(/\/$/, '');
  if (!isRelativeUrl(original)) return rendered;

  const relative = `${parsed.pathname}${parsed.search}${parsed.hash}`;
  if (!relative) return '';
  if (relative === '/') return '';
  return relative.replace(/\/$/, '');
}

function applyQueryParams(url: string, params?: Record<string, string> | null): string {
  if (!params || Object.keys(params).length === 0) return url;
  const parsed = new URL(url, getDefaultUrlBase());
  for (const [key, value] of Object.entries(params)) {
    if (!key.trim()) continue;
    parsed.searchParams.set(key, value);
  }
  return finalizeUrl(parsed, url);
}

function splitCompatibleEndpointPath(path: string): {
  basePath: string;
  apiType: CompatibleEndpointApiType | null;
  endpointSuffix: string;
  includesEndpoint: boolean;
} {
  const lower = path.toLowerCase();
  const chatSegment = COMPATIBLE_ENDPOINT_SEGMENT.chat_completions;
  const responsesSegment = COMPATIBLE_ENDPOINT_SEGMENT.responses;
  const chatIndex = lower.indexOf(chatSegment);
  const responsesIndex = lower.indexOf(responsesSegment);

  let matchedApiType: CompatibleEndpointApiType | null = null;
  let matchedSegment = '';
  let matchedIndex = -1;

  if (chatIndex >= 0 && (responsesIndex === -1 || chatIndex <= responsesIndex)) {
    matchedApiType = 'chat_completions';
    matchedSegment = chatSegment;
    matchedIndex = chatIndex;
  } else if (responsesIndex >= 0) {
    matchedApiType = 'responses';
    matchedSegment = responsesSegment;
    matchedIndex = responsesIndex;
  }

  if (matchedIndex === -1 || !matchedApiType) {
    return {
      basePath: path || '/',
      apiType: null,
      endpointSuffix: '',
      includesEndpoint: false,
    };
  }

  const basePath = path.slice(0, matchedIndex) || '/';
  const endpointSuffix = path.slice(matchedIndex + matchedSegment.length).trim();
  return {
    basePath,
    apiType: matchedApiType,
    endpointSuffix: normalizeCompatibleEndpointSuffix(endpointSuffix),
    includesEndpoint: true,
  };
}

export function getCompatibleEndpointSegment(apiType: CompatibleEndpointApiType): string {
  return COMPATIBLE_ENDPOINT_SEGMENT[apiType];
}

export function normalizeCompatibleEndpointSuffix(value: string | null | undefined): string {
  const trimmed = String(value ?? '').trim();
  if (!trimmed) return '';
  return trimmed.startsWith('/') ? trimmed : `/${trimmed}`;
}

export function splitCompatibleEndpointFromUrl(value: string): {
  baseUrl: string;
  apiType: CompatibleEndpointApiType | null;
  endpointSuffix: string;
  includesEndpoint: boolean;
} {
  const trimmed = String(value ?? '').trim();
  if (!trimmed) {
    return {
      baseUrl: '',
      apiType: null,
      endpointSuffix: '',
      includesEndpoint: false,
    };
  }

  try {
    const parsed = new URL(trimmed, getDefaultUrlBase());
    const pathInfo = splitCompatibleEndpointPath(parsed.pathname || '/');
    if (!pathInfo.includesEndpoint) {
      return {
        baseUrl: finalizeUrl(parsed, trimmed),
        apiType: null,
        endpointSuffix: '',
        includesEndpoint: false,
      };
    }

    parsed.pathname = pathInfo.basePath === '/' ? '' : pathInfo.basePath;
    return {
      baseUrl: finalizeUrl(parsed, trimmed),
      apiType: pathInfo.apiType,
      endpointSuffix: pathInfo.endpointSuffix,
      includesEndpoint: true,
    };
  } catch {
    const pathInfo = splitCompatibleEndpointPath(trimmed);
    return {
      baseUrl: pathInfo.includesEndpoint ? pathInfo.basePath.replace(/\/+$/, '') : trimmed.replace(/\/+$/, ''),
      apiType: pathInfo.apiType,
      endpointSuffix: pathInfo.endpointSuffix,
      includesEndpoint: pathInfo.includesEndpoint,
    };
  }
}

export function buildCompatibleEndpointUrl(input: {
  baseUrl: string;
  apiType: CompatibleEndpointApiType;
  model?: string | null;
  modelInPath?: boolean | null;
  endpointSuffix?: string | null;
  queryParams?: Record<string, string> | null;
}): string {
  const trimmedBase = String(input.baseUrl ?? '').trim();
  if (!trimmedBase) return '';

  const baseWithQuery = applyQueryParams(trimmedBase.replace(/\/+$/, ''), input.queryParams);
  const parsed = new URL(baseWithQuery, getDefaultUrlBase());
  let path = parsed.pathname.replace(/\/+$/, '');
  const endpointSegment = getCompatibleEndpointSegment(input.apiType);

  if (!path.includes(endpointSegment)) {
    const model = String(input.model ?? '').trim();
    if (input.modelInPath && model) {
      path = `${path}/${encodeURIComponent(model)}`;
    }
    path = `${path}${endpointSegment}`;
  }

  const suffix = normalizeCompatibleEndpointSuffix(input.endpointSuffix);
  if (suffix) {
    path = `${path}${suffix}`;
  }

  parsed.pathname = path || '/';
  return finalizeUrl(parsed, trimmedBase);
}
