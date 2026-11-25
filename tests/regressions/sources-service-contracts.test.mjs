import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

// Unified sources service layer (services/sourcesApi.ts + githubReposApi.ts).
// Contract under test:
// - listSources prefers GET /api/sources and normalizes registry rows
//   (github_clone is canonical for app-managed clones, visibility -> hidden);
// - on 404 it composes local grants + repo store + project sources, with
//   per-section error isolation (one failing section never blanks the rest,
//   and an *absent* optional service is not an error);
// - semantics are honest: grants/repo-store rows are references, project
//   sources are copies, the whole-computer grant is not stop-referenceable
//   from the dialog;
// - the GitHub client falls back from /api/github/repositories to
//   /api/github/repos before declaring the service unavailable.

let modulePromise = null;
async function loadModule() {
  if (!modulePromise) {
    modulePromise = (async () => {
      const esbuild = await import('esbuild');
      const result = await esbuild.build({
        stdin: {
          contents: [
            "export * from './services/sourcesApi';",
            "export * as gh from './services/githubReposApi';",
          ].join('\n'),
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
      const file = path.join(os.tmpdir(), `sources-service-${process.pid}-${Date.now()}.mjs`);
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

/** Installs a route-table fetch stub; returns the list of requested paths. */
function stubFetch(routes) {
  const calls = [];
  globalThis.fetch = async (input, init) => {
    const url = new URL(String(input), 'http://fixture.local');
    const method = String(init?.method || 'GET').toUpperCase();
    const key = `${method} ${url.pathname}`;
    calls.push(key);
    const handler = routes[key];
    if (!handler) return jsonResponse({ error: `no route: ${key}` }, 404);
    return typeof handler === 'function' ? handler(url, init) : handler();
  };
  return calls;
}

const GRANT_RULES = {
  rules: [
    {
      id: 'rule-folder',
      category: 'read',
      effect: 'allow',
      scope: 'global',
      subject: { kind: 'local_location', local_root: '/Users/u/Reports', local_kind: 'folder' },
    },
    {
      id: 'rule-whole',
      category: 'read',
      effect: 'allow',
      scope: 'global',
      subject: { kind: 'local_location', local_root: '/', local_kind: 'folder' },
    },
    {
      // A file grant is not a folder reference and must not appear.
      id: 'rule-file',
      category: 'read',
      effect: 'allow',
      scope: 'global',
      subject: { kind: 'local_location', local_root: '/Users/u/doc.pdf', local_kind: 'file' },
    },
  ],
};

test('legacy composition: grants + repo store + project sources with honest semantics', async () => {
  const mod = await loadModule();
  mod.gh.resetGitHubReposApiCache();
  stubFetch({
    'GET /api/permissions/rules': () => jsonResponse(GRANT_RULES),
    'GET /api/github/repositories': () => jsonResponse({
      repositories: [{ id: 'repo-1', full_name: 'acme/analytics', default_ref: 'main', remote_url: 'https://github.com/acme/analytics' }],
    }),
    'GET /api/projects/proj-1/sources': () => jsonResponse({
      sources: [
        { name: 'uploads', kind: 'directory', hidden: false, file_count: 3, total_bytes: 999 },
        { name: 'old-clone', kind: 'git', hidden: true, file_count: 8, total_bytes: 1234 },
      ],
    }),
  });

  const result = await mod.listSources({ projectId: 'proj-1' });
  assert.equal(result.unified, false);
  assert.deepEqual(result.errors, []);

  const byId = new Map(result.sources.map((s) => [s.id, s]));
  const folder = byId.get('grant:rule-folder');
  assert.equal(folder.kind, 'local_reference');
  assert.equal(folder.semantics, 'reference');
  assert.equal(folder.capabilities.stopReference, true);

  const whole = byId.get('grant:rule-whole');
  assert.equal(whole.wholeComputer, true);
  assert.equal(whole.title, 'Whole computer');
  // The access mode is owned by Settings; the dialog must not offer revoke.
  assert.equal(whole.capabilities.stopReference, false);

  assert.equal(byId.has('grant:rule-file'), false, 'file grants are not folder sources');

  const repo = byId.get('repo:repo-1');
  assert.equal(repo.kind, 'github_clone');
  assert.equal(repo.semantics, 'reference');
  assert.equal(repo.capabilities.browse, true);

  const copy = byId.get('project:proj-1:uploads');
  assert.equal(copy.kind, 'project_snapshot');
  assert.equal(copy.semantics, 'copy');
  assert.equal(copy.capabilities.deleteCopy, true);
  assert.equal(copy.capabilities.stopReference, undefined);

  const legacyClone = byId.get('project:proj-1:old-clone');
  assert.equal(legacyClone.kind, 'project_snapshot');
  assert.equal(legacyClone.semantics, 'copy', 'a legacy clone is a snapshot, not a reference');
  assert.equal(legacyClone.hidden, true);
  assert.equal(legacyClone.capabilities.refresh, undefined);
});

test('legacy composition: absent optional services are silent, real failures are scoped', async () => {
  const mod = await loadModule();
  mod.gh.resetGitHubReposApiCache();
  stubFetch({
    // Permissions service missing entirely: silence, not an error banner.
    'GET /api/permissions/rules': () => jsonResponse({ error: 'nope' }, 404),
    // Repo store present but broken: a scoped, reported failure.
    'GET /api/github/repositories': () => jsonResponse({ error: 'exploded' }, 500),
    'GET /api/github/repos': () => jsonResponse({ error: 'exploded' }, 500),
    'GET /api/projects/proj-1/sources': () => jsonResponse({
      sources: [{ name: 'uploads', kind: 'directory', hidden: false, file_count: 1, total_bytes: 10 }],
    }),
  });

  const result = await mod.listSources({ projectId: 'proj-1' });
  assert.equal(result.unified, false);
  assert.equal(result.sources.length, 1, 'surviving sections still load');
  assert.equal(result.sources[0].kind, 'project_snapshot');
  assert.deepEqual(result.errors.map((e) => e.section), ['github']);
});

test('unified registry rows normalize kinds, visibility, and locators', async () => {
  const mod = await loadModule();
  stubFetch({
    'GET /api/sources': () => jsonResponse({
      sources: [
        { id: 's1', kind: 'local_reference', title: 'Reports', local_root: '/Users/u/Reports', grant_id: 'rule-1' },
        { id: 's2', kind: 'github_clone', title: 'acme/analytics', remote_url: 'https://github.com/acme/analytics', ref: 'main' },
        { id: 's3', kind: 'project_snapshot', title: 'uploads', visibility: 'hidden', byte_size: 55 },
        { id: 's4', kind: 'local_reference', title: 'Whole computer', local_path: '/' },
        { id: 's5', kind: 'something_unknown', title: 'mystery' },
      ],
    }),
  });

  const result = await mod.listSources({});
  assert.equal(result.unified, true);
  assert.equal(result.sources.length, 4, 'unknown kinds are dropped, not misfiled');

  const byId = new Map(result.sources.map((s) => [s.id, s]));
  const local = byId.get('source:s1');
  const repo = byId.get('source:s2');
  const copy = byId.get('source:s3');
  const whole = byId.get('source:s4');
  assert.equal(local.kind, 'local_reference');
  assert.equal(local.grantRuleId, 'rule-1');
  assert.equal(repo.kind, 'github_clone', 'github_clone registry rows keep the clone kind');
  assert.equal(repo.semantics, 'reference');
  assert.equal(repo.repo.id, 's2');
  assert.equal(repo.repo.fullName, 'acme/analytics');
  assert.equal(repo.repo.defaultRef, 'main');
  assert.equal(repo.capabilities.browse, true);
  assert.equal(copy.hidden, true);
  assert.equal(copy.semantics, 'copy');
  assert.equal(whole.path, '/');
  assert.equal(whole.wholeComputer, true);
  assert.equal(whole.capabilities.stopReference, false);
});

test('github client falls back to the /repos alias before giving up', async () => {
  const mod = await loadModule();
  mod.gh.resetGitHubReposApiCache();
  const calls = stubFetch({
    'GET /api/github/repositories': () => jsonResponse({ error: 'nope' }, 404),
    'GET /api/github/repos': () => jsonResponse({ repos: [{ id: 'r1', name: 'acme/x' }] }),
  });

  const repos = await mod.gh.listGitHubRepositories();
  assert.equal(repos.length, 1);
  assert.equal(repos[0].id, 'r1');
  assert.ok(calls.includes('GET /api/github/repositories'));
  assert.ok(calls.includes('GET /api/github/repos'));

  // The resolved mount point is remembered: the next call skips the probe.
  calls.length = 0;
  await mod.gh.listGitHubRepositories();
  assert.deepEqual(calls, ['GET /api/github/repos']);
});

test('github clone posts the canonical repository field', async () => {
  const mod = await loadModule();
  mod.gh.resetGitHubReposApiCache();
  const bodies = [];
  stubFetch({
    'POST /api/github/repositories/clone': (_url, init) => {
      bodies.push(JSON.parse(String(init.body || '{}')));
      return jsonResponse({
        repository: {
          source_id: 'github-abc',
          repository: 'acme/widget',
          url: 'https://github.com/acme/widget.git',
        },
      });
    },
  });

  const result = await mod.gh.cloneGitHubRepository({ url: 'https://github.com/acme/widget.git' });
  assert.equal(result.via, 'repo-store');
  assert.deepEqual(bodies, [{ repository: 'https://github.com/acme/widget.git' }]);
  assert.equal(result.repository.id, 'github-abc');
  assert.equal(result.repository.fullName, 'acme/widget');
});

test('github clone does not fall back to imported project snapshots', async () => {
  const mod = await loadModule();
  mod.gh.resetGitHubReposApiCache();
  const calls = stubFetch({
    'POST /api/github/repositories/clone': () => jsonResponse({ error: 'missing' }, 404),
    'POST /api/github/repos/clone': () => jsonResponse({ error: 'missing' }, 404),
    'POST /api/projects/git': () => jsonResponse({ project: { id: 'legacy' } }),
  });

  await assert.rejects(
    () => mod.gh.cloneGitHubRepository({ url: 'https://github.com/acme/widget.git' }),
    (error) => error?.name === 'GithubReposApiUnavailableError',
  );
  assert.deepEqual(calls, [
    'POST /api/github/repositories/clone',
    'POST /api/github/repos/clone',
  ]);
});

test('github raw file URLs request raw bytes, not the JSON envelope', async () => {
  const mod = await loadModule();
  mod.gh.resetGitHubReposApiCache();
  const url = mod.gh.gitHubRepoRawUrl('github-abc', { ref: 'main', path: 'src/app.ts' });
  const parsed = new URL(url, 'http://fixture.local');
  assert.equal(parsed.pathname, '/api/github/repositories/github-abc/raw');
  assert.equal(parsed.searchParams.get('path'), 'src/app.ts');
  assert.equal(parsed.searchParams.get('ref'), 'main');
  assert.equal(parsed.searchParams.get('format'), 'raw');
});

test('github tree client accepts the backend nested tree envelope', async () => {
  const mod = await loadModule();
  mod.gh.resetGitHubReposApiCache();
  stubFetch({
    'GET /api/github/repositories/github-abc/tree': () => jsonResponse({
      tree: {
        ref: 'main',
        entries: [
          { name: 'app.ts', path: 'src/app.ts', type: 'file', byte_size: 12 },
          { name: 'src', path: 'src', type: 'directory' },
        ],
      },
    }),
  });

  const result = await mod.gh.getGitHubRepoTree('github-abc', { path: 'src' });
  assert.equal(result.ref, 'main');
  assert.deepEqual(result.entries.map((entry) => `${entry.type}:${entry.path}`), ['dir:src', 'file:src/app.ts']);
  assert.equal(result.entries[1].byteSize, 12);
});

test('github client reports unavailable only when every mount point 404s', async () => {
  const mod = await loadModule();
  mod.gh.resetGitHubReposApiCache();
  stubFetch({
    'GET /api/github/repositories': () => jsonResponse({ error: 'nope' }, 404),
    'GET /api/github/repos': () => jsonResponse({ error: 'nope' }, 501),
  });
  await assert.rejects(
    () => mod.gh.listGitHubRepositories(),
    (error) => mod.gh.isGithubReposUnavailable(error),
  );
});
