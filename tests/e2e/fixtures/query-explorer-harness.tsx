import React, { useEffect } from 'react';
import { createRoot } from 'react-dom/client';
import { useQueryExplorer } from '@/components/renderers/tool/query/useQueryExplorer';
import type { Artifact } from '@/types';

type Snapshot = {
  rowsLength: number;
  totalRows: number | null;
  hasMore: boolean;
  loading: boolean;
  error: string | null;
};

const allRows = Array.from({ length: 100 }, (_, index) => ({
  n: index,
  label: `row-${index}`,
}));

const previewRows = allRows.slice(0, 10);

function waitFor(predicate: () => boolean, timeoutMs = 3000): Promise<void> {
  const started = Date.now();
  return new Promise((resolve, reject) => {
    const tick = () => {
      if (predicate()) {
        resolve();
        return;
      }
      if (Date.now() - started > timeoutMs) {
        reject(new Error('Timed out waiting for query explorer state'));
        return;
      }
      window.setTimeout(tick, 25);
    };
    tick();
  });
}

export async function runQueryExplorerLivePagingScenario(): Promise<{
  snapshots: Snapshot[];
  calls: string[];
}> {
  const calls: string[] = [];
  const originalFetch = window.fetch;
  window.fetch = (async (input: RequestInfo | URL) => {
    const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    calls.push(raw);
    const url = new URL(raw, window.location.origin);
    const offset = Number(url.searchParams.get('offset') || '0');
    const limit = Number(url.searchParams.get('limit') || '50');
    const rows = allRows.slice(offset, offset + limit);
    return new Response(
      JSON.stringify({
        artifact_id: 'artifact_query_full',
        rows,
        offset,
        next_offset: offset + rows.length,
        limit,
        rows_returned: rows.length,
        row_count_total: allRows.length,
        has_more: offset + rows.length < allRows.length,
      }),
      { headers: { 'content-type': 'application/json' } },
    );
  }) as typeof window.fetch;

  const artifact: Artifact = {
    id: 'artifact_query_preview',
    type: 'data',
    title: 'Query Results',
    content: JSON.stringify(previewRows),
    metadata: {
      tool_name: 'run_bigquery_query',
      full_artifact_id: 'artifact_query_full',
      row_count_total: allRows.length,
      query_metadata: {
        provider: 'bigquery',
        job_id: 'bquxjob_live_paging',
      },
    },
  };

  const snapshots: Snapshot[] = [];
  let latest: Snapshot | null = null;
  let loadMore: (() => void) | null = null;

  function Probe() {
    const data = useQueryExplorer(artifact, 50);
    useEffect(() => {
      latest = {
        rowsLength: data.rows.length,
        totalRows: data.totalRows,
        hasMore: data.hasMore,
        loading: data.loading,
        error: data.error ? data.error.message : null,
      };
      snapshots.push(latest);
      loadMore = () => {
        void data.loadMore();
      };
    }, [data.rows.length, data.totalRows, data.hasMore, data.loading, data.error, data.loadMore]);
    return null;
  }

  const rootNode = document.createElement('div');
  document.body.appendChild(rootNode);
  const root = createRoot(rootNode);
  root.render(<Probe />);

  try {
    await waitFor(() => latest?.rowsLength === 50 && latest.loading === false);
    loadMore?.();
    await waitFor(() => latest?.rowsLength === 100 && latest.loading === false);
    return { snapshots, calls };
  } finally {
    root.unmount();
    rootNode.remove();
    window.fetch = originalFetch;
  }
}
