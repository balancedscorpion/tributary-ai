import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

let fileAuditApiPromise = null;
async function loadFileAuditApi() {
  if (!fileAuditApiPromise) {
    fileAuditApiPromise = (async () => {
      const { transformSync } = await import('esbuild');
      const { code } = transformSync(sourceAt('services/fileAuditApi.ts'), { loader: 'ts', format: 'esm' });
      const file = path.join(os.tmpdir(), `file-audit-api-${process.pid}-${Date.now()}.mjs`);
      fs.writeFileSync(file, code);
      try {
        return await import(pathToFileURL(file).href);
      } finally {
        fs.unlinkSync(file);
      }
    })();
  }
  return fileAuditApiPromise;
}

function scopeMeta(overrides = {}) {
  return {
    limit: 3,
    offset: 0,
    returned: 3,
    hasMore: true,
    truncated: true,
    cachedFileLimitHit: false,
    remoteScanLimitHit: false,
    ...overrides,
  };
}

test('listFileAuditRecords parses scope.candidate_count from the backend payload', async () => {
  const { listFileAuditRecords } = await loadFileAuditApi();
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({
    files: [{ id: 'file-1', thread_id: 't1', title: 'a.md' }],
    scope: {
      thread_id: '',
      limit: 3,
      offset: 0,
      returned: 1,
      has_more: true,
      truncated: true,
      next_offset: 1,
      cached_file_limit_hit: true,
      remote_scan_limit: 400,
      remote_scan_limit_hit: false,
      candidate_count: 42,
    },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  try {
    const result = await listFileAuditRecords({ limit: 3 });
    assert.equal(result.scope.candidateCount, 42);
    assert.equal(result.scope.cachedFileLimitHit, true);
    assert.equal(result.scope.hasMore, true);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('listFileAuditRecords sends a trimmed q param and normalizes the scope query echo', async () => {
  const { listFileAuditRecords } = await loadFileAuditApi();
  const originalFetch = globalThis.fetch;
  let requestedUrl = '';
  globalThis.fetch = async (url) => {
    requestedUrl = String(url);
    return new Response(JSON.stringify({
      files: [],
      scope: { limit: 3, offset: 0, returned: 0, has_more: false, truncated: false, query: 'vendor sow' },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  };
  try {
    const result = await listFileAuditRecords({ limit: 3, query: '  vendor sow  ' });
    assert.match(requestedUrl, /[?&]q=vendor\+sow(&|$)/);
    assert.equal(result.scope.query, 'vendor sow');
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('listFileAuditRecords omits q without a query and leaves scope.query undefined', async () => {
  const { listFileAuditRecords } = await loadFileAuditApi();
  const originalFetch = globalThis.fetch;
  let requestedUrl = '';
  globalThis.fetch = async (url) => {
    requestedUrl = String(url);
    return new Response(JSON.stringify({
      files: [],
      // Backends echo query: "" when no search was applied; legacy backends
      // omit the field entirely. Both must normalize to undefined.
      scope: { limit: 3, offset: 0, returned: 0, has_more: false, truncated: false, query: '' },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  };
  try {
    const result = await listFileAuditRecords({ limit: 3, query: '   ' });
    assert.doesNotMatch(requestedUrl, /[?&]q=/);
    assert.equal(result.scope.query, undefined);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('listFileAuditRecords sends source and remote_only params and normalizes their echoes', async () => {
  const { listFileAuditRecords } = await loadFileAuditApi();
  const originalFetch = globalThis.fetch;
  let requestedUrl = '';
  globalThis.fetch = async (url) => {
    requestedUrl = String(url);
    return new Response(JSON.stringify({
      files: [],
      scope: {
        limit: 3,
        offset: 0,
        returned: 0,
        has_more: false,
        truncated: false,
        source: 'SharePoint',
        source_label: 'SharePoint',
        remote_only: true,
        // The backend's facet shapes: source buckets as a list, status
        // buckets as a map. Malformed entries are dropped, not propagated.
        source_counts: [
          { key: 'SharePoint', label: 'SharePoint', count: 3 },
          { key: 'Jira', label: 'Jira', count: 1 },
          { label: 'bogus', count: 'not-a-number' },
        ],
        status_counts: { total: 4, cached: 2, remote_only: 2 },
      },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  };
  try {
    const result = await listFileAuditRecords({ limit: 3, source: ' SharePoint ', remoteOnly: true });
    assert.match(requestedUrl, /[?&]source=SharePoint(&|$)/);
    assert.match(requestedUrl, /[?&]remote_only=1(&|$)/);
    assert.equal(result.scope.source, 'SharePoint');
    assert.equal(result.scope.remoteOnly, true);
    assert.deepEqual(result.scope.sourceCounts, { SharePoint: 3, Jira: 1 });
    assert.equal(result.scope.remoteOnlyCount, 2);
    assert.equal(result.scope.cachedCount, 2);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('listFileAuditRecords also accepts map-shaped source counts defensively', async () => {
  const { listFileAuditRecords } = await loadFileAuditApi();
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({
    files: [],
    scope: {
      limit: 3,
      offset: 0,
      returned: 0,
      has_more: false,
      truncated: false,
      source_counts: { SharePoint: 3, bogus: 'not-a-number' },
      remote_only_count: 1,
    },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  try {
    const result = await listFileAuditRecords({ limit: 3 });
    assert.deepEqual(result.scope.sourceCounts, { SharePoint: 3 });
    assert.equal(result.scope.remoteOnlyCount, 1);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('listFileAuditRecords omits inactive filters and leaves their scope fields undefined', async () => {
  const { listFileAuditRecords } = await loadFileAuditApi();
  const originalFetch = globalThis.fetch;
  let requestedUrl = '';
  globalThis.fetch = async (url) => {
    requestedUrl = String(url);
    return new Response(JSON.stringify({
      files: [],
      // Legacy backends omit the filter echoes and facet counts entirely;
      // modern backends echo source: "" when no filter was applied.
      scope: { limit: 3, offset: 0, returned: 0, has_more: false, truncated: false, source: '' },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  };
  try {
    const result = await listFileAuditRecords({ limit: 3, source: '   ', remoteOnly: false });
    assert.doesNotMatch(requestedUrl, /[?&]source=/);
    assert.doesNotMatch(requestedUrl, /[?&]remote_only=/);
    assert.equal(result.scope.source, undefined);
    assert.equal(result.scope.remoteOnly, undefined);
    assert.equal(result.scope.sourceCounts, undefined);
    assert.equal(result.scope.remoteOnlyCount, undefined);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('legacy scope payloads without candidate_count leave candidateCount undefined', async () => {
  const { listFileAuditRecords } = await loadFileAuditApi();
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({
    files: [],
    scope: { limit: 3, offset: 0, returned: 0, has_more: false, truncated: false },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  try {
    const result = await listFileAuditRecords({ limit: 3 });
    assert.equal(result.scope.candidateCount, undefined);
  } finally {
    globalThis.fetch = originalFetch;
  }
});

test('fileAuditCountSummary reports an exact available count when no scan limit was hit', async () => {
  const { fileAuditCountSummary } = await loadFileAuditApi();
  const summary = fileAuditCountSummary(scopeMeta({ candidateCount: 7 }), 3);
  assert.deepEqual(summary, { hasMore: true, available: 7, availableIsLowerBound: false });
});

test('fileAuditCountSummary flags the available count as a lower bound when a scan limit truncated enumeration', async () => {
  const { fileAuditCountSummary } = await loadFileAuditApi();
  const cachedBounded = fileAuditCountSummary(scopeMeta({ candidateCount: 9, cachedFileLimitHit: true }), 3);
  assert.deepEqual(cachedBounded, { hasMore: true, available: 9, availableIsLowerBound: true });
  const remoteBounded = fileAuditCountSummary(scopeMeta({ candidateCount: 9, remoteScanLimitHit: true }), 3);
  assert.deepEqual(remoteBounded, { hasMore: true, available: 9, availableIsLowerBound: true });
});

test('fileAuditCountSummary withholds the available count when it does not exceed the loaded count', async () => {
  const { fileAuditCountSummary } = await loadFileAuditApi();
  // More records exist but the candidate count is stale/equal: the UI can only
  // honestly say "N+", never "N of M" with M <= N.
  const summary = fileAuditCountSummary(scopeMeta({ candidateCount: 3, cachedFileLimitHit: true }), 3);
  assert.deepEqual(summary, { hasMore: true, available: null, availableIsLowerBound: true });
});

test('fileAuditCountSummary reports nothing extra once everything is loaded', async () => {
  const { fileAuditCountSummary } = await loadFileAuditApi();
  // A stale candidate count (e.g. after a local delete) must not resurrect an
  // "of M" claim when the backend says there is nothing more to load.
  const summary = fileAuditCountSummary(
    scopeMeta({ hasMore: false, truncated: false, nextOffset: 0, candidateCount: 7 }),
    6,
  );
  assert.deepEqual(summary, { hasMore: false, available: null, availableIsLowerBound: false });
  assert.deepEqual(
    fileAuditCountSummary(null, 5),
    { hasMore: false, available: null, availableIsLowerBound: false },
  );
});

test('fileAuditCountSummary handles legacy scopes with no candidate count but more available', async () => {
  const { fileAuditCountSummary } = await loadFileAuditApi();
  const summary = fileAuditCountSummary(scopeMeta(), 3);
  assert.deepEqual(summary, { hasMore: true, available: null, availableIsLowerBound: false });
});
