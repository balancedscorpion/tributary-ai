function normalizeToolName(toolName: string): string {
  return String(toolName || '').trim().toLowerCase();
}

export function isSuppressedQueryStatusToolName(toolName: string): boolean {
  const normalized = normalizeToolName(toolName);
  return (
    normalized === 'check_query_status'
    || normalized === 'get_query_status'
    || normalized === 'query_status'
    || normalized === 'poll_query_status'
    || normalized === 'get_query_job_status'
  );
}

export function isSuppressedArtifactRetrievalToolName(toolName: string): boolean {
  const normalized = normalizeToolName(toolName);
  return (
    normalized === 'get_artifact_chunk'
    || normalized === 'search_artifact_content'
    || normalized === 'get_json_artifact'
  );
}

export function isHiddenQueryToolCallName(toolName: string): boolean {
  const normalized = normalizeToolName(toolName);
  return (
    normalized === 'get_query_results'
    || isSuppressedQueryStatusToolName(normalized)
    || isSuppressedArtifactRetrievalToolName(normalized)
  );
}

function isPlainObject(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function isNonTerminalQueryStatus(status: unknown): boolean {
  const normalized = String(status || '').trim().toLowerCase();
  switch (normalized) {
    case 'reserved':
    case 'profiling':
    case 'submission_unknown':
    case 'running':
    case 'submitted':
    case 'cancel_requested':
    case 'cancelling':
    case 'pending':
    case 'queued':
    case 'unknown':
    case 'in_progress':
    case 'processing':
      return true;
    default:
      return false;
  }
}

export function shouldSuppressQueryToolResult(params: {
  toolName: string;
  resultFormat: 'json' | 'text';
  result: unknown;
  envelopeUiSuppress?: boolean;
}): boolean {
  const toolName = normalizeToolName(params.toolName);
  if (params.envelopeUiSuppress === true) return true;
  if (isSuppressedQueryStatusToolName(toolName)) return true;
  if (isSuppressedArtifactRetrievalToolName(toolName)) return true;

  if (params.resultFormat !== 'json' || !isPlainObject(params.result)) return false;
  const result = params.result as Record<string, any>;

  if (result.ui_suppress === true || result.suppress_ui === true || result.hide_from_ui === true) {
    return true;
  }

  if (toolName !== 'get_query_results') return false;
  if (result.retryable === true) return true;
  if (isNonTerminalQueryStatus(result.status ?? result.state ?? result.phase)) return true;
  return false;
}
