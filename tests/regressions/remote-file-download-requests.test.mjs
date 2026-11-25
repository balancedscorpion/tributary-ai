import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

// Session ledger for "ask agent to download" draft requests
// (lib/remoteFileDownloadRequests.ts). Contract under test:
// - marking is keyed by artifactId||id and re-marking refreshes requestedAt;
// - clearing matches by key OR any remote locator, because a fulfilled
//   request produces a cached record under a DIFFERENT id — locators are the
//   only way to tie "now cached" / "cached copy deleted" back to the entry;
// - the run-completed channel only fires for threads with pending entries.

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

let modulePromise = null;
async function loadStore() {
  if (!modulePromise) {
    modulePromise = (async () => {
      const { transformSync } = await import('esbuild');
      const { code } = transformSync(sourceAt('lib/remoteFileDownloadRequests.ts'), { loader: 'ts', format: 'esm' });
      const file = path.join(os.tmpdir(), `remote-file-download-requests-${process.pid}-${Date.now()}.mjs`);
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

const remoteRecord = {
  id: 'remote:sp-1',
  artifactId: 'remote:sp-1',
  title: 'Vendor SOW.docx',
  cached: false,
  remoteUrl: 'https://contoso.sharepoint.com/sites/s/Vendor%20SOW.docx',
  remotePath: '/sites/s/Vendor SOW.docx',
  remoteRef: 'SPDOC-8842',
};

test('mark stores the request by record key and re-marking refreshes requestedAt', async () => {
  const store = await loadStore();
  store.resetRemoteFileDownloadRequestsForTests();

  assert.equal(store.getRemoteFileDownloadRequest(remoteRecord), null);
  store.markRemoteFileDownloadRequested(remoteRecord, { threadId: 't1', threadTitle: 'Thread one' });
  const first = store.getRemoteFileDownloadRequest(remoteRecord);
  assert.ok(first);
  assert.equal(first.threadId, 't1');
  assert.equal(first.threadTitle, 'Thread one');
  assert.equal(first.remoteRef, 'SPDOC-8842');
  assert.ok(Number.isFinite(first.requestedAt));

  await new Promise((resolve) => setTimeout(resolve, 5));
  store.markRemoteFileDownloadRequested(remoteRecord, { threadId: 't2', threadTitle: null });
  const second = store.getRemoteFileDownloadRequest(remoteRecord);
  assert.ok(second.requestedAt >= first.requestedAt);
  assert.equal(second.threadId, 't2');
  assert.equal(second.threadTitle, null);

  // Lookup falls back from artifactId to id.
  assert.ok(store.getRemoteFileDownloadRequest({ id: 'remote:sp-1' }));
  assert.equal(store.getRemoteFileDownloadRequest({ id: 'other' }), null);
});

test('clearing matches by key or by any remote locator (differently-keyed cached record)', async () => {
  const store = await loadStore();

  for (const [field, value] of [
    ['remoteUrl', remoteRecord.remoteUrl],
    ['remotePath', remoteRecord.remotePath],
    ['remoteRef', remoteRecord.remoteRef],
  ]) {
    store.resetRemoteFileDownloadRequestsForTests();
    store.markRemoteFileDownloadRequested(remoteRecord, { threadId: 't1' });
    // The cached record the fulfilled download produced: new id, one shared locator.
    store.clearRemoteFileDownloadRequestsFor({ artifactId: 'cached-999', cached: true, [field]: value });
    assert.equal(store.getRemoteFileDownloadRequest(remoteRecord), null, `should clear via ${field}`);
  }

  // Key match without locators (deleting the remote reference row itself).
  store.resetRemoteFileDownloadRequestsForTests();
  store.markRemoteFileDownloadRequested(remoteRecord, { threadId: 't1' });
  store.clearRemoteFileDownloadRequestsFor({ artifactId: 'remote:sp-1' });
  assert.equal(store.getRemoteFileDownloadRequest(remoteRecord), null);

  // Empty locators on the clearing record must not wildcard-match entries
  // whose own locator fields are empty strings.
  store.resetRemoteFileDownloadRequestsForTests();
  store.markRemoteFileDownloadRequested({ artifactId: 'remote:bare', title: 'bare' }, { threadId: 't1' });
  store.clearRemoteFileDownloadRequestsFor({ artifactId: 'unrelated' });
  assert.ok(store.getRemoteFileDownloadRequest({ artifactId: 'remote:bare' }), 'unrelated clear must not remove entry');
});

test('subscribers and version change on mark and on clear', async () => {
  const store = await loadStore();
  store.resetRemoteFileDownloadRequestsForTests();

  let notified = 0;
  const unsubscribe = store.subscribeRemoteFileDownloadRequests(() => { notified += 1; });
  const v0 = store.remoteFileDownloadRequestsSnapshot();

  store.markRemoteFileDownloadRequested(remoteRecord, { threadId: 't1' });
  assert.equal(notified, 1);
  assert.ok(store.remoteFileDownloadRequestsSnapshot() > v0);

  // A clear with no matching entry must not churn the version.
  const v1 = store.remoteFileDownloadRequestsSnapshot();
  store.clearRemoteFileDownloadRequestsFor({ artifactId: 'unrelated' });
  assert.equal(notified, 1);
  assert.equal(store.remoteFileDownloadRequestsSnapshot(), v1);

  store.clearRemoteFileDownloadRequestsFor(remoteRecord);
  assert.equal(notified, 2);
  assert.ok(store.remoteFileDownloadRequestsSnapshot() > v1);
  unsubscribe();
});

test('run-completed channel fires only for threads with pending requests', async () => {
  const store = await loadStore();
  store.resetRemoteFileDownloadRequestsForTests();

  const completions = [];
  store.subscribeRemoteFileDownloadRunCompleted((threadId) => completions.push(threadId));

  store.notifyRemoteFileDownloadRunCompleted('t1');
  assert.deepEqual(completions, [], 'no pending requests → no notification');

  store.markRemoteFileDownloadRequested(remoteRecord, { threadId: 't1' });
  assert.equal(store.threadHasRemoteFileDownloadRequests('t1'), true);
  assert.equal(store.threadHasRemoteFileDownloadRequests('t2'), false);

  store.notifyRemoteFileDownloadRunCompleted('t2');
  assert.deepEqual(completions, [], 'other thread completing must not notify');
  store.notifyRemoteFileDownloadRunCompleted('t1');
  assert.deepEqual(completions, ['t1']);

  // Once fulfilled/cleared, further completions are silent again.
  store.clearRemoteFileDownloadRequestsFor(remoteRecord);
  store.notifyRemoteFileDownloadRunCompleted('t1');
  assert.deepEqual(completions, ['t1']);
});
