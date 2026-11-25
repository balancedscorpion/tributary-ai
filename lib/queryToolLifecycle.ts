import type {
  Artifact,
  QueryJobStatus,
  ToolResultEnvelope,
} from '@/types';
import { normalizeQueryJobStatus } from '@/lib/queryProgress';

interface QueryLifecycleInput {
  envelope?: ToolResultEnvelope;
  artifact?: Artifact;
  liveStatus?: QueryJobStatus | null;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function normalizeStatusAlias(value: unknown): QueryJobStatus | null {
  if (typeof value !== 'string') return null;
  const status = value.trim().toLowerCase();
  const canonical = normalizeQueryJobStatus(status);
  if (canonical) return canonical;
  if (
    status === 'pending'
    || status === 'queued'
    || status === 'in_progress'
    || status === 'processing'
  ) {
    return 'running';
  }
  if (
    status === 'complete'
    || status === 'success'
    || status === 'succeeded'
    || status === 'done'
  ) {
    return 'completed';
  }
  if (status === 'error' || status === 'errored') return 'failed';
  if (status === 'canceled') return 'cancelled';
  return null;
}

function statusValues(input: QueryLifecycleInput): unknown[] {
  const result = isRecord(input.envelope?.result) ? input.envelope.result : null;
  const resultMetadata = isRecord(input.envelope?.result_metadata)
    ? input.envelope.result_metadata
    : null;
  const artifactMetadata = input.artifact?.metadata || {};
  const artifactQueryMetadata = isRecord(artifactMetadata.query_metadata)
    ? artifactMetadata.query_metadata
    : null;
  const artifactResultMetadata = isRecord(artifactMetadata.result_metadata)
    ? artifactMetadata.result_metadata
    : null;
  const artifactRawResult = isRecord(artifactMetadata.raw_result)
    ? artifactMetadata.raw_result
    : null;

  return [
    input.liveStatus,
    result?.status,
    result?.execution_status,
    result?.state,
    input.envelope?.query_metadata?.status,
    input.envelope?.query_metadata?.execution_status,
    resultMetadata?.status,
    resultMetadata?.execution_status,
    resultMetadata?.state,
    artifactMetadata.status,
    artifactMetadata.execution_status,
    artifactQueryMetadata?.status,
    artifactQueryMetadata?.execution_status,
    artifactResultMetadata?.status,
    artifactResultMetadata?.execution_status,
    artifactResultMetadata?.state,
    artifactRawResult?.status,
    artifactRawResult?.execution_status,
    artifactRawResult?.state,
  ];
}

function hasNonNegativeNumber(value: unknown): boolean {
  if (typeof value === 'number') return Number.isFinite(value) && value >= 0;
  if (typeof value !== 'string' || !value.trim()) return false;
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 0;
}

/**
 * Returns true only when the payload contains evidence that query results
 * materialized. A successful launch acknowledgement is deliberately not
 * completion evidence.
 */
export function hasMaterializedQueryResults(input: QueryLifecycleInput): boolean {
  const result = input.envelope?.result;
  if (Array.isArray(result)) return true;
  if (isRecord(result)) {
    if (Array.isArray(result.rows) || Array.isArray(result.data)) return true;
    if (
      hasNonNegativeNumber(result.row_count)
      || hasNonNegativeNumber(result.rows_returned)
      || hasNonNegativeNumber(result.affected_rows)
    ) {
      return true;
    }
  }

  const artifact = input.artifact;
  if (!artifact) return false;
  const metadata = artifact.metadata || {};
  if (metadata.placeholder === true || metadata.async_query === true || metadata.ok === false) {
    return false;
  }
  const queryMetadata = isRecord(metadata.query_metadata)
    ? metadata.query_metadata
    : null;
  const resultMetadata = isRecord(metadata.result_metadata)
    ? metadata.result_metadata
    : null;
  const toolName = String(metadata.tool_name || metadata.generated_by || '').trim();
  const contentKind = String(
    metadata.content_kind || resultMetadata?.content_kind || '',
  ).trim();
  const hasRowsContent =
    artifact.type === 'data'
    && typeof artifact.content === 'string'
    && artifact.content.trim().startsWith('[');

  return (
    toolName === 'get_query_results'
    || metadata.contains_full_content === true
    || contentKind === 'json_rows'
    || hasNonNegativeNumber(metadata.row_count_total)
    || hasNonNegativeNumber(metadata.row_count)
    || hasNonNegativeNumber(queryMetadata?.row_count_total)
    || hasNonNegativeNumber(queryMetadata?.row_count)
    || hasNonNegativeNumber(queryMetadata?.rows_returned)
    || hasNonNegativeNumber(resultMetadata?.row_count_total)
    || hasRowsContent
  );
}

/**
 * Resolves the remote query lifecycle independently from whether invoking the
 * tool itself succeeded. Green success is returned only for terminal
 * completion or materialized results.
 */
export function resolveQueryToolLifecycleStatus(
  input: QueryLifecycleInput,
): QueryJobStatus {
  const statuses = statusValues(input)
    .map(normalizeStatusAlias)
    .filter((status): status is QueryJobStatus => status !== null);
  const terminal = statuses.find((status) =>
    status === 'completed'
    || status === 'failed'
    || status === 'cancelled'
    || status === 'expired'
    || status === 'orphaned'
  );
  if (terminal) return terminal;

  if (input.envelope?.ok === false || input.artifact?.metadata?.ok === false) {
    return 'failed';
  }

  const active = statuses.find((status) =>
    status === 'reserved'
    || status === 'profiling'
    || status === 'submission_unknown'
    || status === 'submitted'
    || status === 'running'
    || status === 'cancel_requested'
    || status === 'cancelling'
    || status === 'unknown'
  );
  if (active) return active;

  if (hasMaterializedQueryResults(input)) {
    return 'completed';
  }

  if (
    input.artifact?.metadata?.placeholder === true
    || input.artifact?.metadata?.async_query === true
  ) {
    return 'running';
  }

  // No terminal proof: keep the query neutral/live instead of claiming it
  // succeeded merely because the launch tool returned ok=true.
  return 'unknown';
}
