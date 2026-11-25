import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

// Grant-scoped local file preview client (services/localFilesApi.ts).
// Contract under test:
// - the raw URL targets the phase-2 stream endpoint and never carries an
//   absolute local path — only the grant id and the relative file ref;
// - a bare mux 404 (no JSON body) and 501 mean "this build has no stream
//   endpoint" (LocalFilesApiUnavailableError); a JSON 404 — the live
//   endpoint's answer for a missing file or revoked grant — surfaces as an
//   honest file-level error, never as "feature missing";
// - after the endpoint has answered once, every 404 is a file-level error;
// - text-like payloads inline; binary and oversize payloads stay unread
//   (with a reason) — preview must not copy files into the app.

let modulePromise = null;
async function loadModule() {
  if (!modulePromise) {
    modulePromise = (async () => {
      const esbuild = await import('esbuild');
      const result = await esbuild.build({
        stdin: {
          contents: "export * from './services/localFilesApi';",
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
      const file = path.join(os.tmpdir(), `local-files-api-${process.pid}-${Date.now()}.mjs`);
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

function withFetch(handler, run) {
  const original = globalThis.fetch;
  globalThis.fetch = handler;
  return run().finally(() => {
    globalThis.fetch = original;
  });
}

test('raw URL targets the grant stream endpoint with an encoded relative ref', async () => {
  const api = await loadModule();
  const url = api.localGrantFileRawUrl('grant/1', 'notes/summary q3.md');
  assert.equal(url, '/api/local-files/grants/grant%2F1/file?ref=notes%2Fsummary+q3.md');
  assert.ok(!url.includes('/Users/'), 'no absolute local path in the URL');
});

test('bare 404 and 501 mean the build lacks the endpoint', async () => {
  const api = await loadModule();
  api.resetLocalFilesApiCache();
  await withFetch(
    async () => new Response('404 page not found', {
      status: 404,
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
    }),
    async () => {
      await assert.rejects(
        api.fetchLocalGrantFilePreview('grant-1', 'a.txt'),
        (e) => api.isLocalFilesApiUnavailable(e),
        'bare mux 404 should map to LocalFilesApiUnavailableError',
      );
    },
  );
  await withFetch(
    async () => new Response('{}', { status: 501, headers: { 'Content-Type': 'application/json' } }),
    async () => {
      await assert.rejects(
        api.fetchLocalGrantFilePreview('grant-1', 'a.txt'),
        (e) => api.isLocalFilesApiUnavailable(e),
        '501 should map to LocalFilesApiUnavailableError',
      );
    },
  );
});

test('a JSON 404 is a missing file or grant, not a missing feature', async () => {
  const api = await loadModule();
  api.resetLocalFilesApiCache();
  await withFetch(
    async () => new Response(JSON.stringify({ error: 'file not found in grant' }), {
      status: 404,
      headers: { 'Content-Type': 'application/json' },
    }),
    async () => {
      await assert.rejects(
        api.fetchLocalGrantFilePreview('grant-1', 'moved.txt'),
        (e) => !api.isLocalFilesApiUnavailable(e) && /file not found in grant/.test(e.message),
      );
    },
  );
});

test('after the endpoint answers once, every 404 is a file-level error', async () => {
  const api = await loadModule();
  api.resetLocalFilesApiCache();
  await withFetch(
    async () => new Response('hello', {
      status: 200,
      headers: { 'Content-Type': 'text/plain', 'Content-Length': '5' },
    }),
    async () => {
      await api.fetchLocalGrantFilePreview('grant-1', 'a.txt');
    },
  );
  await withFetch(
    async () => new Response('404 page not found', {
      status: 404,
      headers: { 'Content-Type': 'text/plain; charset=utf-8' },
    }),
    async () => {
      await assert.rejects(
        api.fetchLocalGrantFilePreview('grant-1', 'moved.txt'),
        (e) => !api.isLocalFilesApiUnavailable(e) && /no longer at that path/.test(e.message),
      );
    },
  );
  api.resetLocalFilesApiCache();
});

test('text payloads inline; binary and oversize payloads stay unread', async () => {
  const api = await loadModule();
  api.resetLocalFilesApiCache();

  await withFetch(
    async () => new Response('hello preview', {
      status: 200,
      headers: { 'Content-Type': 'text/plain', 'Content-Length': '13' },
    }),
    async () => {
      const preview = await api.fetchLocalGrantFilePreview('grant-1', 'a.txt');
      assert.equal(preview.binary, false);
      assert.equal(preview.text, 'hello preview');
      assert.equal(preview.mimeType, 'text/plain');
    },
  );

  await withFetch(
    async () => new Response(new Uint8Array([1, 2, 3]), {
      status: 200,
      headers: { 'Content-Type': 'application/octet-stream', 'Content-Length': '3' },
    }),
    async () => {
      const preview = await api.fetchLocalGrantFilePreview('grant-1', 'a.bin');
      assert.equal(preview.binary, true);
      assert.equal(preview.reason, 'binary');
      assert.equal(preview.text, undefined);
    },
  );

  await withFetch(
    async () => new Response('x', {
      status: 200,
      headers: { 'Content-Type': 'text/plain', 'Content-Length': String(64 * 1024 * 1024) },
    }),
    async () => {
      const preview = await api.fetchLocalGrantFilePreview('grant-1', 'huge.txt');
      assert.equal(preview.binary, true, 'oversize text is not inlined');
      assert.equal(preview.reason, 'too-large');
    },
  );
  api.resetLocalFilesApiCache();
});

test('non-ok responses raise the server-provided message', async () => {
  const api = await loadModule();
  await withFetch(
    async () => new Response(JSON.stringify({ error: 'grant revoked' }), {
      status: 403,
      headers: { 'Content-Type': 'application/json' },
    }),
    async () => {
      await assert.rejects(
        api.fetchLocalGrantFilePreview('grant-1', 'a.txt'),
        /grant revoked/,
      );
    },
  );
});
