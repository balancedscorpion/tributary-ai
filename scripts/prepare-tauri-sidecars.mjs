#!/usr/bin/env node
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const args = parseArgs(process.argv.slice(2));

const goos = String(args.goos || defaultGoOS()).trim();
const goarch = String(args.goarch || defaultGoArch()).trim();
const targetTriple = String(args['target-triple'] || defaultTargetTriple(goos, goarch)).trim();
const showAllProviders = parseBool(args['show-all-providers'], true);
const skipUIBuild = Boolean(args['skip-ui-build']);
const skipBrowserDownload = Boolean(args['skip-browser-download']);
const cacheArg = args.cache ? ['--cache', path.resolve(String(args.cache))] : [];
const nativeToolsArg = args['native-tools-dir'] ? ['--native-tools-dir', path.resolve(String(args['native-tools-dir']))] : [];

if (!goos || !goarch || !targetTriple) {
  throw new Error(`Unsupported desktop target: goos=${goos} goarch=${goarch} targetTriple=${targetTriple}`);
}

const exeExt = goos === 'windows' ? '.exe' : '';
const binaryName = `ai-analyst-server-${targetTriple}${exeExt}`;
const binariesDir = path.join(repoRoot, 'src-tauri', 'binaries');
const resourcesDir = path.join(repoRoot, 'src-tauri', 'resources');
const runtimeResourceDir = path.join(resourcesDir, 'runtime');
const desktopRuntimePackage = path.join(repoRoot, 'dist', 'desktop-runtime', `${goos}-${goarch}`);
const sidecarPath = path.join(binariesDir, binaryName);

fs.mkdirSync(binariesDir, { recursive: true });
fs.mkdirSync(resourcesDir, { recursive: true });
cleanGeneratedSidecars(binariesDir);
fs.rmSync(runtimeResourceDir, { recursive: true, force: true });
fs.rmSync(desktopRuntimePackage, { recursive: true, force: true });

if (!skipUIBuild) {
  runNpm(['run', 'build'], {
    cwd: repoRoot,
    env: {
      ...process.env,
      VITE_SHOW_ALL_PROVIDERS: showAllProviders ? 'true' : 'false',
    },
  });
}

run('go', [
  'build',
  '-trimpath',
  '-tags',
  showAllProviders ? 'embedui,allproviders' : 'embedui',
  '-ldflags',
  '-s -w',
  '-o',
  sidecarPath,
  './cmd/ai-analyst',
], {
  cwd: path.join(repoRoot, 'server'),
  env: {
    ...process.env,
    CGO_ENABLED: '0',
    GOOS: goos,
    GOARCH: goarch,
    AIA_SHOW_ALL_PROVIDERS: showAllProviders ? 'true' : 'false',
  },
});
if (goos !== 'windows') fs.chmodSync(sidecarPath, 0o755);

run('node', [
  path.join(repoRoot, 'scripts', 'package-release-runtime.mjs'),
  '--goos',
  goos,
  '--goarch',
  goarch,
  '--binary',
  sidecarPath,
  '--out',
  desktopRuntimePackage,
  ...cacheArg,
  ...nativeToolsArg,
  ...(skipBrowserDownload ? ['--skip-browser-download'] : []),
], { cwd: repoRoot });

const packagedRuntime = path.join(desktopRuntimePackage, 'runtime');
if (!fs.existsSync(packagedRuntime)) {
  throw new Error(`Runtime package was not generated at ${packagedRuntime}`);
}
// Preserve the macOS browser framework's relative symlinks while staging the
// runtime. Dereferencing here is both unnecessary (Node, npm, and native-tool
// links were already materialized by package-release-runtime.mjs) and
// Node-version-dependent: Node 20 recursively expanded Chrome's Versions/Current
// plus its top-level framework aliases into three physical copies. Tauri can
// bundle the original framework links without that release-size inflation.
fs.cpSync(packagedRuntime, runtimeResourceDir, {
  recursive: true,
  dereference: false,
  verbatimSymlinks: true,
});

console.log(`Prepared Tauri sidecar: ${path.relative(repoRoot, sidecarPath)}`);
console.log(`Prepared Tauri runtime resource: ${path.relative(repoRoot, runtimeResourceDir)}`);

function parseArgs(argv) {
  const out = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (!arg.startsWith('--')) continue;
    const key = arg.slice(2);
    const next = argv[i + 1];
    if (!next || next.startsWith('--')) {
      out[key] = true;
    } else {
      out[key] = next;
      i += 1;
    }
  }
  return out;
}

function parseBool(value, fallback) {
  if (value === undefined) return fallback;
  if (value === true) return true;
  const normalized = String(value).trim().toLowerCase();
  if (['1', 'true', 'yes', 'on'].includes(normalized)) return true;
  if (['0', 'false', 'no', 'off'].includes(normalized)) return false;
  return fallback;
}

function run(command, commandArgs, options = {}) {
  console.log(`$ ${[command, ...commandArgs].join(' ')}`);
  execFileSync(command, commandArgs, { stdio: 'inherit', ...options });
}

function runNpm(commandArgs, options = {}) {
  const npmExecPath = process.env.npm_execpath;
  if (process.platform === 'win32' && npmExecPath) {
    console.log(`$ ${['npm', ...commandArgs].join(' ')}`);
    execFileSync(process.execPath, [npmExecPath, ...commandArgs], {
      stdio: 'inherit',
      ...options,
    });
    return;
  }
  run(npmCommand(), commandArgs, options);
}

function npmCommand() {
  return process.platform === 'win32' ? 'npm.cmd' : 'npm';
}

function cleanGeneratedSidecars(dir) {
  for (const entry of fs.readdirSync(dir)) {
    if (/^ai-analyst-server-/.test(entry)) {
      fs.rmSync(path.join(dir, entry), { force: true });
    }
  }
}

function defaultGoOS() {
  switch (process.platform) {
    case 'darwin':
      return 'darwin';
    case 'win32':
      return 'windows';
    case 'linux':
      return 'linux';
    default:
      return process.platform;
  }
}

function defaultGoArch() {
  switch (process.arch) {
    case 'x64':
      return 'amd64';
    case 'arm64':
      return 'arm64';
    default:
      return process.arch;
  }
}

function defaultTargetTriple(targetOS, targetArch) {
  const key = `${targetOS}/${targetArch}`;
  const triples = {
    'darwin/amd64': 'x86_64-apple-darwin',
    'darwin/arm64': 'aarch64-apple-darwin',
    'windows/amd64': 'x86_64-pc-windows-msvc',
  };
  return triples[key] || '';
}
