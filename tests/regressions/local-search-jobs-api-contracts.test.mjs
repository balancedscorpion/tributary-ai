import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

// Local search job polling client (services/localSearchJobsApi.ts).
// Contract under test:
// - one-job reads target /api/local-files/search-jobs/{jobId} with thread/run scope;
// - list calls must be scoped by thread or run so the UI never asks for a
//   global local-search job dump;
// - snake_case server payloads normalize into camelCase UI objects;
// - server JSON errors surface directly to future reconnect/polling UI.

let modulePromise = null;
async function loadModule() {
  if (!modulePromise) {
    modulePromise = (async () => {
      const esbuild = await import('esbuild');
      const result = await esbuild.build({
        stdin: {
          contents: "export * from './services/localSearchJobsApi';",
          resolveDir: process.cwd(),
          loader: 'ts',
        },
        bundle: true,
        format: 'esm',
        write: false,
        platform: 'browser',
        alias: { '@': '.' },
        logLevel: 'silent',
      });
      const code = result.outputFiles[0].text;
      const file = path.join(os.tmpdir(), `local-search-jobs-api-${process.pid}-${Date.now()}.mjs`);
      fs.writeFileSync(file, code);
      try {
        return await import(pathToFileURL(file).href);
      } finally {
        fs.unlinkSync(file);
      }
    })();
  }
  return modulePromise;
}

function jsonResponse(payload, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

function withFetch(handler, run) {
  const original = globalThis.fetch;
  globalThis.fetch = handler;
  return run().finally(() => {
    globalThis.fetch = original;
  });
}

const JOB = {
  id: 'local_search_123',
  thread_id: 'thread-1',
  run_id: 'run-1',
  tool_call_id: 'call-1',
  status: 'completed',
  query: 'forecast',
  grant_id: 'grant-1',
  local_root: '/Users/example/Reports',
  local_kind: 'folder',
  created_at: '2026-07-07T10:00:00Z',
  updated_at: '2026-07-07T10:00:02Z',
  completed_at: '2026-07-07T10:00:02Z',
  files_count: 2,
  result: { status: 'completed', files: [{ file_ref: 'notes.md' }] },
};

test('getLocalSearchJob normalizes one job from the status endpoint', async () => {
  const api = await loadModule();
  const calls = [];
  await withFetch(
    async (input) => {
      const url = new URL(String(input), 'http://fixture.local');
      calls.push(`${url.pathname}?${url.searchParams.toString()}`);
      return jsonResponse({ ok: true, job: JOB });
    },
    async () => {
      const job = await api.getLocalSearchJob('local_search_123', { threadId: 'thread-1', runId: 'run-1' });
      assert.deepEqual(calls, ['/api/local-files/search-jobs/local_search_123?thread_id=thread-1&run_id=run-1']);
      assert.equal(job.id, 'local_search_123');
      assert.equal(job.threadId, 'thread-1');
      assert.equal(job.runId, 'run-1');
      assert.equal(job.toolCallId, 'call-1');
      assert.equal(job.grantId, 'grant-1');
      assert.equal(job.localRoot, '/Users/example/Reports');
      assert.equal(job.localKind, 'folder');
      assert.equal(job.filesCount, 2);
      assert.equal(job.result.status, 'completed');
      assert.equal(job.completedAt, Date.parse('2026-07-07T10:00:02Z'));
    },
  );
});

test('listLocalSearchJobs requires a scope and sends thread/run query params', async () => {
  const api = await loadModule();
  await assert.rejects(
    api.listLocalSearchJobs({}),
    /threadId or runId is required/,
  );

  const calls = [];
  await withFetch(
    async (input) => {
      const url = new URL(String(input), 'http://fixture.local');
      calls.push(`${url.pathname}?${url.searchParams.toString()}`);
      return jsonResponse({
        ok: true,
        jobs: [JOB],
        scope: { thread_id: 'thread-1', run_id: 'run-1', returned: 1, limit: 25 },
      });
    },
    async () => {
      const result = await api.listLocalSearchJobs({ threadId: 'thread-1', runId: 'run-1', limit: 25 });
      assert.deepEqual(calls, ['/api/local-files/search-jobs?thread_id=thread-1&run_id=run-1&limit=25']);
      assert.equal(result.jobs.length, 1);
      assert.equal(result.scope.threadId, 'thread-1');
      assert.equal(result.scope.runId, 'run-1');
      assert.equal(result.scope.returned, 1);
      assert.equal(result.scope.limit, 25);
    },
  );
});

test('server JSON errors surface for local search job polling', async () => {
  const api = await loadModule();
  await assert.rejects(
    api.getLocalSearchJob('missing'),
    /threadId or runId is required/,
  );
  await withFetch(
    async () => jsonResponse({ ok: false, error: 'local search job was not found' }, 404),
    async () => {
      await assert.rejects(
        api.getLocalSearchJob('missing', { threadId: 'thread-1' }),
        /local search job was not found/,
      );
    },
  );
});
