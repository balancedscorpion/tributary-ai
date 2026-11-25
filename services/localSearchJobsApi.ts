// API client for local search-agent job status.
//
// The main chat stream remains the primary live surface. These endpoints are a
// recovery/polling contract for app surfaces that need to reconnect to an
// already-started local file search without asking for local permissions again.

export interface LocalSearchJobStatus {
  id: string;
  threadId: string;
  runId: string;
  toolCallId?: string;
  status: string;
  query: string;
  grantId?: string;
  localRoot?: string;
  localKind?: string;
  createdAt?: number;
  updatedAt?: number;
  completedAt?: number;
  filesCount?: number;
  error?: string;
  result?: Record<string, any>;
}

export interface ListLocalSearchJobsOptions {
  threadId?: string;
  runId?: string;
  limit?: number;
  signal?: AbortSignal;
}

export interface ListLocalSearchJobsResult {
  jobs: LocalSearchJobStatus[];
  scope: {
    threadId?: string;
    runId?: string;
    returned: number;
    limit: number;
  };
}

function isRecord(value: unknown): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function stringValue(...values: unknown[]): string | undefined {
  for (const value of values) {
    if (typeof value !== 'string') continue;
    const trimmed = value.trim();
    if (trimmed) return trimmed;
  }
  return undefined;
}

function numberValue(...values: unknown[]): number | undefined {
  for (const value of values) {
    if (typeof value === 'number' && Number.isFinite(value)) return value;
  }
  return undefined;
}

function timestampValue(...values: unknown[]): number | undefined {
  for (const value of values) {
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value === 'string' && value.trim()) {
      const ts = Date.parse(value);
      if (Number.isFinite(ts)) return ts;
    }
  }
  return undefined;
}

async function requestJSON(path: string, init?: RequestInit): Promise<Record<string, any>> {
  let res: Response;
  try {
    res = await fetch(path, {
      ...init,
      headers: { Accept: 'application/json', ...(init?.headers || {}) },
    });
  } catch (e: any) {
    throw new Error(e?.message || 'Network error');
  }
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error((data && (data.error || data.message)) || `HTTP ${res.status}`);
  }
  return isRecord(data) ? data : {};
}

export function normalizeLocalSearchJob(raw: unknown): LocalSearchJobStatus | null {
  if (!isRecord(raw)) return null;
  const id = stringValue(raw.id, raw.job_id, raw.jobId);
  const threadId = stringValue(raw.thread_id, raw.threadId);
  const runId = stringValue(raw.run_id, raw.runId);
  const status = stringValue(raw.status) || '';
  const query = stringValue(raw.query) || '';
  if (!id || !threadId || !runId || !status) return null;
  return {
    id,
    threadId,
    runId,
    toolCallId: stringValue(raw.tool_call_id, raw.toolCallId),
    status,
    query,
    grantId: stringValue(raw.grant_id, raw.grantId),
    localRoot: stringValue(raw.local_root, raw.localRoot),
    localKind: stringValue(raw.local_kind, raw.localKind),
    createdAt: timestampValue(raw.created_at, raw.createdAt),
    updatedAt: timestampValue(raw.updated_at, raw.updatedAt),
    completedAt: timestampValue(raw.completed_at, raw.completedAt),
    filesCount: numberValue(raw.files_count, raw.filesCount),
    error: stringValue(raw.error),
    result: isRecord(raw.result) ? raw.result : undefined,
  };
}

export async function getLocalSearchJob(
  jobId: string,
  options: { threadId?: string; runId?: string; signal?: AbortSignal } = {},
): Promise<LocalSearchJobStatus> {
  const cleanId = jobId.trim();
  if (!cleanId) throw new Error('jobId is required');
  const params = new URLSearchParams();
  if (options.threadId?.trim()) params.set('thread_id', options.threadId.trim());
  if (options.runId?.trim()) params.set('run_id', options.runId.trim());
  if (!params.has('thread_id') && !params.has('run_id')) {
    throw new Error('threadId or runId is required');
  }
  const data = await requestJSON(`/api/local-files/search-jobs/${encodeURIComponent(cleanId)}?${params.toString()}`, {
    signal: options.signal,
  });
  const job = normalizeLocalSearchJob(data.job);
  if (!job) throw new Error('Local search job response was invalid.');
  return job;
}

export async function listLocalSearchJobs(
  options: ListLocalSearchJobsOptions,
): Promise<ListLocalSearchJobsResult> {
  const params = new URLSearchParams();
  if (options.threadId?.trim()) params.set('thread_id', options.threadId.trim());
  if (options.runId?.trim()) params.set('run_id', options.runId.trim());
  if (options.limit && Number.isFinite(options.limit)) params.set('limit', String(Math.trunc(options.limit)));
  if (!params.has('thread_id') && !params.has('run_id')) {
    throw new Error('threadId or runId is required');
  }
  const data = await requestJSON(`/api/local-files/search-jobs?${params.toString()}`, {
    signal: options.signal,
  });
  const jobs = Array.isArray(data.jobs)
    ? data.jobs.map(normalizeLocalSearchJob).filter((job): job is LocalSearchJobStatus => job !== null)
    : [];
  const rawScope = isRecord(data.scope) ? data.scope : {};
  return {
    jobs,
    scope: {
      threadId: stringValue(rawScope.thread_id, rawScope.threadId),
      runId: stringValue(rawScope.run_id, rawScope.runId),
      returned: numberValue(rawScope.returned) ?? jobs.length,
      limit: numberValue(rawScope.limit) ?? jobs.length,
    },
  };
}
