import assert from 'node:assert/strict';
import { readFileSync, unlinkSync, writeFileSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { pathToFileURL } from 'node:url';

const broker = readFileSync('server/internal/chat/tools/local_search_command_tool.go', 'utf8');
const agent = readFileSync('server/internal/chat/optionc/local_search_agent.go', 'utf8');
const manager = readFileSync('server/internal/chat/optionc/manager.go', 'utf8');
const grants = readFileSync('lib/localComputerGrants.ts', 'utf8');
const windowsRoots = readFileSync('server/internal/localroots/roots_windows.go', 'utf8');
const windowsCommand = readFileSync('server/internal/localexec/command_windows.go', 'utf8');
const releaseWorkflow = readFileSync('.github/workflows/release-binaries.yml', 'utf8');

async function loadLocalComputerGrants() {
  const esbuild = await import('esbuild');
  const result = await esbuild.build({
    stdin: {
      contents: "export * from './lib/localComputerGrants';",
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
  const file = path.join(os.tmpdir(), `local-computer-grants-${process.pid}-${Date.now()}.mjs`);
  writeFileSync(file, result.outputFiles[0].text);
  try {
    return await import(pathToFileURL(file).href);
  } finally {
    unlinkSync(file);
  }
}

test('local search command broker exposes a closed no-shell command surface', () => {
  assert.match(broker, /\[\]string\{"auto", "rg", "grep", "fd", "find"\}/);
  assert.match(broker, /localexec\.CommandContext/);
  assert.match(broker, /shell syntax is not allowed/);
  assert.match(broker, /--exec/);
  assert.match(broker, /--pre/);
  assert.doesNotMatch(broker, /bash\s+-lc|sh\s+-c|exec\.CommandContext\([^\n]+"bash"/);
});

test('local search agent uses the configured provider and retains a portable fallback', () => {
  assert.match(manager, /localSearchAgent:\s*&localSearchAgentService/);
  // Nested model calls must inherit the immutable run dispatch snapshot; the
  // context-aware factory still falls back to the configured provider when no
  // snapshot is present.
  assert.match(manager, /CreateProviderForContext/);
  assert.match(manager, /WithDispatchSnapshot/);
  assert.match(agent, /local_search_command/);
  assert.match(agent, /runConfiguredLocalFileSearch/);
  assert.match(agent, /portable_search/);
  assert.match(agent, /localSearchAgentRunTimeout/);
});

test('Local Computer setup provisions and preserves the bounded execute policy', () => {
  assert.match(grants, /LOCAL_COMPUTER_SEARCH_COMMAND_TOOL = 'local_search_command'/);
  assert.match(grants, /ensureLocalComputerSearchCommandPermission/);
  assert.match(grants, /isLocalComputerSearchCommandPolicyRule/);
  assert.match(grants, /kind === 'tool_set'/);
  assert.match(grants, /kind === 'connector'/);
  assert.match(grants, /connector: 'local-computer'/);
  assert.match(grants, /tool_set_id: 'local-computer'/);
  assert.match(grants, /category: 'execute'/);
  assert.match(grants, /effect: 'allow'/);
  assert.doesNotMatch(grants, /deleteLocalComputerSearchCommandPermissions/);
});

test('Windows Local Computer roots and paths are normalized without assuming one drive', async () => {
  const api = await loadLocalComputerGrants();
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({
    ok: true,
    os: 'windows',
    primary_root: 'D:\\',
    roots: ['D:\\', 'C:\\'],
  }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  try {
    assert.deepEqual(await api.localComputerWholeRoots(), ['D:\\', 'C:\\']);
  } finally {
    globalThis.fetch = originalFetch;
  }
  assert.equal(
    api.normalizeLocalPathKey('c:\\Users\\Alex\\Reports\\'),
    api.normalizeLocalPathKey('C:/users/alex/reports'),
  );
  assert.equal(api.dedupeLocalFolders(['C:\\Users\\Alex', 'c:/users/alex']).length, 1);
  assert.equal(api.isLocalFilesystemRoot('D:\\'), true);
  assert.equal(api.isLocalFilesystemRoot('D:\\Reports'), false);
});

test('Windows runtime enumerates local drives, hides process windows, and smoke-tests native search', () => {
  assert.match(windowsRoots, /GetLogicalDrives/);
  assert.match(windowsRoots, /DRIVE_FIXED/);
  assert.match(windowsRoots, /DRIVE_REMOVABLE/);
  assert.doesNotMatch(windowsRoots, /DRIVE_REMOTE/);
  assert.match(windowsCommand, /HideWindow:\s+true/);
  assert.match(windowsCommand, /CREATE_NO_WINDOW/);
  assert.match(releaseWorkflow, /Verify Windows local search runtime/);
  assert.match(releaseWorkflow, /rg --version/);
  assert.match(releaseWorkflow, /fd --version/);
  assert.match(releaseWorkflow, /TestCommandContextHidesWindowsConsole/);
});
