import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { test } from 'node:test';

const packageRuntime = readFileSync('scripts/package-release-runtime.mjs', 'utf8');
const prepareSidecars = readFileSync('scripts/prepare-tauri-sidecars.mjs', 'utf8');
const releaseWorkflow = readFileSync('.github/workflows/release-binaries.yml', 'utf8');
const localExecResolver = readFileSync('server/internal/localexec/resolve.go', 'utf8');

test('desktop packaging requires native document and local-search tools instead of silently omitting them', () => {
  assert.match(packageRuntime, /native-tools-dir/);
  assert.match(packageRuntime, /allow-missing-native-tools/);
  assert.match(packageRuntime, /Missing required native runtime/);
  assert.match(packageRuntime, /pdftoppm/);
  assert.match(packageRuntime, /pdfinfo/);
  assert.match(packageRuntime, /pdftotext/);
  assert.match(packageRuntime, /tesseract/);
  assert.match(packageRuntime, /search: \['rg', 'fd'\]/);
  assert.match(packageRuntime, /requireNativeExecutable/);
  assert.match(packageRuntime, /requirementName === 'search'/);
});

test('desktop sidecar preparation forwards native tool runtime location', () => {
  assert.match(prepareSidecars, /native-tools-dir/);
  assert.match(prepareSidecars, /nativeToolsArg/);
  assert.match(prepareSidecars, /package-release-runtime\.mjs/);
});

test('desktop runtime staging preserves browser framework symlinks', () => {
  assert.match(prepareSidecars, /fs\.cpSync\(packagedRuntime, runtimeResourceDir/);
  assert.match(prepareSidecars, /dereference: false/);
  assert.match(prepareSidecars, /verbatimSymlinks: true/);
  assert.doesNotMatch(
    prepareSidecars,
    /fs\.cpSync\(packagedRuntime, runtimeResourceDir, \{ recursive: true, dereference: true \}\)/,
  );
});

test('desktop release workflow provisions and packages native document and local-search tools', () => {
  assert.match(releaseWorkflow, /mamba-org\/setup-micromamba@v2/);
  assert.match(releaseWorkflow, /poppler/);
  assert.match(releaseWorkflow, /tesseract/);
  assert.match(releaseWorkflow, /ripgrep/);
  assert.match(releaseWorkflow, /fd-find/);
  assert.match(releaseWorkflow, /Verify Windows local search runtime/);
  assert.match(releaseWorkflow, /AIA_NATIVE_TOOLS_DIR/);
  assert.match(releaseWorkflow, /--native-tools-dir/);
});

test('backend native command resolver searches shared desktop native tool runtime', () => {
  assert.match(localExecResolver, /native["'], ["']tools["'], ["']bin/);
  assert.match(localExecResolver, /native["'], ["']tools["'], ["']Library["'], ["']bin/);
  assert.match(localExecResolver, /native["'], ["']search["'], ["']bin/);
  assert.match(localExecResolver, /native["'], ["']search["'], ["']Library["'], ["']bin/);
});
