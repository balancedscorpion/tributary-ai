import type { Artifact, ToolResultEnvelope } from '@/types';

export type ErrorKind =
  | 'auth'
  | 'notfound'
  | 'permission'
  | 'ratelimit'
  | 'timeout'
  | 'validation'
  | 'unavailable'
  | 'generic';

export interface ToolErrorDiagnostic {
  title: string;
  code: string | null;
  kind: ErrorKind;
  message: string;
  why: string | null;
  tryThis: string | null;
  failedRequest: string | null;
  source: 'metadata' | 'classifier' | 'fallback';
}

export type ConnectorClassifier = (
  message: string,
) => Partial<Pick<ToolErrorDiagnostic, 'title' | 'code' | 'kind' | 'why' | 'tryThis'>>;

function isObj(v: unknown): v is Record<string, any> {
  return v !== null && typeof v === 'object' && !Array.isArray(v);
}

function pickString(
  sources: Array<Record<string, any> | null | undefined>,
  keys: readonly string[],
): string | null {
  for (const s of sources) {
    if (!s) continue;
    for (const k of keys) {
      const v = s[k];
      if (typeof v === 'string' && v.trim()) return v.trim();
      if (typeof v === 'number' && Number.isFinite(v)) return String(v);
    }
  }
  return null;
}

const DIAG_OBJECT_KEYS = ['diagnostic', 'error_diagnostic', 'diagnostics'] as const;

const TITLE_KEYS = ['title', 'summary', 'short'] as const;
const CODE_KEYS = ['code', 'status_code', 'http_status', 'error_code'] as const;
const KIND_KEYS = ['kind'] as const;
const WHY_KEYS = [
  'why',
  'reason',
  'likely_cause',
  'cause',
  'model_commentary',
  'commentary',
  'explanation',
] as const;
const TRY_KEYS = ['try_this', 'tryThis', 'suggested_fix', 'suggestion', 'remediation'] as const;
const REQ_KEYS = ['failed_request', 'failedRequest', 'command', 'request', 'endpoint'] as const;
const MSG_KEYS = ['message', 'error', 'description', 'detail', 'reported'] as const;

function classifyGenericToolError(
  message: string,
): Partial<Pick<ToolErrorDiagnostic, 'title' | 'code' | 'kind' | 'why' | 'tryThis'>> {
  const lower = message.toLowerCase();
  const code = lower.match(/\b([1-5][0-9]{2})\b/)?.[1] || null;
  if (
    lower.includes('401') ||
    lower.includes('unauthorized') ||
    lower.includes('unauthenticated') ||
    lower.includes('bad credentials') ||
    lower.includes('invalid token') ||
    lower.includes('missing token')
  ) {
    return {
      title: 'Authentication required',
      code,
      kind: 'auth',
      why: "The tool's connection credentials were missing, expired, or rejected by the remote service.",
      tryThis: 'Reconnect or reconfigure the integration, then run the request again.',
    };
  }
  if (
    lower.includes('403') ||
    lower.includes('forbidden') ||
    lower.includes('permission denied') ||
    lower.includes('access denied') ||
    lower.includes('not authorized')
  ) {
    return {
      title: 'Permission denied',
      code,
      kind: 'permission',
      why: 'The connected account authenticated, but it does not have permission for this resource or operation.',
      tryThis: 'Verify the account, scopes, repository, project, dataset, or workspace permissions.',
    };
  }
  if (
    lower.includes('404') ||
    lower.includes('not found') ||
    lower.includes('does not exist') ||
    lower.includes('no such file') ||
    lower.includes('not visible')
  ) {
    return {
      title: 'Not found',
      code,
      kind: 'notfound',
      why: 'The requested resource could not be found, or it is not visible to the connected account.',
      tryThis: 'Check the resource name/path and confirm the connected account has access.',
    };
  }
  if (lower.includes('429') || lower.includes('rate limit') || lower.includes('too many requests') || lower.includes('quota')) {
    return {
      title: 'Rate limited',
      code,
      kind: 'ratelimit',
      why: 'The service temporarily rejected the request because a quota or rate limit was reached.',
      tryThis: 'Wait briefly, reduce request volume, or retry with a narrower request.',
    };
  }
  if (lower.includes('timeout') || lower.includes('timed out') || lower.includes('deadline exceeded')) {
    return {
      title: 'Timed out',
      code,
      kind: 'timeout',
      why: 'The tool did not receive a response before its timeout.',
      tryThis: 'Retry the request or narrow the operation so it completes faster.',
    };
  }
  if (
    lower.includes('400') ||
    lower.includes('invalid json') ||
    lower.includes('invalid argument') ||
    lower.includes('missing ') ||
    lower.includes('required') ||
    lower.includes('syntax') ||
    lower.includes('validation')
  ) {
    return {
      title: 'Invalid request',
      code,
      kind: 'validation',
      why: 'The tool rejected the request because one or more arguments or query fields were invalid.',
      tryThis: 'Correct the request arguments and run the tool again.',
    };
  }
  if (
    lower.includes('500') ||
    lower.includes('502') ||
    lower.includes('503') ||
    lower.includes('504') ||
    lower.includes('unavailable') ||
    lower.includes('connection refused') ||
    lower.includes('failed to fetch') ||
    lower.includes('network')
  ) {
    return {
      title: 'Service unavailable',
      code,
      kind: 'unavailable',
      why: 'The service or network path failed before the tool could complete.',
      tryThis: 'Retry after the service recovers, or check the connector/network configuration.',
    };
  }
  return {
    title: 'Tool request failed',
    code,
    kind: 'generic',
    why: 'The tool returned an error before completing the requested operation.',
    tryThis: 'Review the reported error and adjust the request or connection before retrying.',
  };
}

export function extractToolErrorDiagnosticFromParts(input: {
  metadata?: Record<string, any> | null;
  rawResult?: unknown;
  title?: string | null;
  content?: string | null;
  classifier?: ConnectorClassifier;
}): ToolErrorDiagnostic {
  const meta = (input.metadata || {}) as Record<string, any>;
  const raw = input.rawResult;
  const resultMeta = isObj(meta.result_metadata) ? meta.result_metadata : {};

  const nested: Array<Record<string, any>> = [];
  for (const k of DIAG_OBJECT_KEYS) {
    if (isObj(resultMeta[k])) nested.push(resultMeta[k]);
    if (isObj(raw) && isObj((raw as Record<string, any>)[k])) {
      nested.push((raw as Record<string, any>)[k]);
    }
  }

  const direct: Array<Record<string, any>> = [resultMeta];
  if (isObj(raw)) direct.push(raw as Record<string, any>);

  const allSources = [...nested, ...direct];

  const message =
    pickString(allSources, MSG_KEYS) ||
    input.title ||
    (typeof input.content === 'string' ? input.content.trim().slice(0, 240) : '') ||
    'An unknown error occurred.';

  let title = pickString(allSources, TITLE_KEYS);
  let code = pickString(allSources, CODE_KEYS);
  let why = pickString(allSources, WHY_KEYS);
  let tryThis = pickString(allSources, TRY_KEYS);
  let failedRequest = pickString(allSources, REQ_KEYS);
  let kind: ErrorKind | null =
    (pickString(allSources, KIND_KEYS) as ErrorKind | null) || null;

  const filledFromMetadata = !!(title || code || why || tryThis || failedRequest || kind);
  let source: ToolErrorDiagnostic['source'] = filledFromMetadata ? 'metadata' : 'fallback';

  if (input.classifier) {
    const c = input.classifier(message);
    if (!title && c.title) title = c.title;
    if (!code && c.code) code = c.code;
    if (!why && c.why) why = c.why;
    if (!tryThis && c.tryThis) tryThis = c.tryThis;
    if (!kind && c.kind) kind = c.kind;
    if (!filledFromMetadata && (c.title || c.code || c.why || c.tryThis || c.kind)) {
      source = 'classifier';
    }
  }

  const generic = classifyGenericToolError(message);
  if (!title && generic.title) title = generic.title;
  if (!code && generic.code) code = generic.code;
  if (!why && generic.why) why = generic.why;
  if (!tryThis && generic.tryThis) tryThis = generic.tryThis;
  if (!kind && generic.kind) kind = generic.kind;
  if (!filledFromMetadata && source === 'fallback') {
    source = 'classifier';
  }

  return {
    title: title || 'Request failed',
    code,
    kind: kind || 'generic',
    message,
    why,
    tryThis,
    failedRequest,
    source,
  };
}

export function extractToolErrorDiagnosticFromArtifact(
  artifact: Artifact,
  opts?: { classifier?: ConnectorClassifier },
): ToolErrorDiagnostic {
  const meta = (artifact.metadata || {}) as Record<string, any>;
  return extractToolErrorDiagnosticFromParts({
    metadata: meta,
    rawResult: meta.raw_result,
    title: artifact.title,
    content: artifact.content,
    classifier: opts?.classifier,
  });
}

export function extractToolErrorDiagnosticFromEnvelope(
  envelope: ToolResultEnvelope | null | undefined,
  opts?: { classifier?: ConnectorClassifier; fallbackTitle?: string | null; fallbackContent?: string | null },
): ToolErrorDiagnostic | null {
  if (!envelope) return null;
  return extractToolErrorDiagnosticFromParts({
    metadata: { result_metadata: envelope.result_metadata },
    rawResult: envelope.result,
    title: opts?.fallbackTitle || envelope.tool_name || 'Tool error',
    content: opts?.fallbackContent || null,
    classifier: opts?.classifier,
  });
}

export function formatToolErrorCardText(
  diagnostic: ToolErrorDiagnostic | null | undefined,
): string | null {
  if (!diagnostic) return null;
  if (diagnostic.why) return `Why: ${diagnostic.why}`;
  if (diagnostic.message) return diagnostic.message;
  return null;
}
