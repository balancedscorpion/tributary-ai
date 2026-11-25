#!/usr/bin/env node
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const args = parseArgs(process.argv.slice(2));

const goos = requiredArg('goos');
const goarch = requiredArg('goarch');
const binary = path.resolve(requiredArg('binary'));
const outDir = path.resolve(requiredArg('out'));
const nodeVersion = String(args['node-version'] || '20.19.5').replace(/^v/, '');
const skipBrowserDownload = Boolean(args['skip-browser-download']);
const nativeToolsDir = args['native-tools-dir'] ? path.resolve(String(args['native-tools-dir'])) : '';
const allowMissingNativeTools = Boolean(args['allow-missing-native-tools']);

const target = targetConfig(goos, goarch);
const playwrightVersion = readPlaywrightVersion();
const nativeToolRequirements = {
  poppler: ['pdftoppm', 'pdfinfo', 'pdftotext'],
  tesseract: ['tesseract'],
  search: ['rg', 'fd'],
};

fs.rmSync(outDir, { recursive: true, force: true });
fs.mkdirSync(outDir, { recursive: true });

const appBinary = path.join(outDir, target.executable);
fs.copyFileSync(binary, appBinary);
if (goos !== 'windows') fs.chmodSync(appBinary, 0o755);

const runtimeDir = path.join(outDir, 'runtime');
const nodeDir = path.join(runtimeDir, 'node');
const atlassianDir = path.join(runtimeDir, 'atlassian');
const browserDir = path.join(runtimeDir, 'ms-playwright');
fs.mkdirSync(runtimeDir, { recursive: true });

installNodeRuntime(nodeDir, target, nodeVersion);
installPlaywrightPackage(atlassianDir, playwrightVersion);
if (!skipBrowserDownload) installPlaywrightBrowser(atlassianDir, browserDir, target.playwrightHost);
const nativeRuntime = installNativeToolRuntime(runtimeDir, target);

writeBundleReadme(outDir, target, nodeVersion, playwrightVersion, skipBrowserDownload, nativeRuntime);

console.log(`Packaged ${goos}/${goarch} release bundle at ${outDir}`);

function requiredArg(name) {
  const value = args[name];
  if (!value) throw new Error(`Missing required --${name}`);
  return value;
}

function parseArgs(argv) {
  const parsed = {};
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (!arg.startsWith('--')) continue;
    const key = arg.slice(2);
    const next = argv[i + 1];
    if (!next || next.startsWith('--')) {
      parsed[key] = true;
    } else {
      parsed[key] = next;
      i += 1;
    }
  }
  return parsed;
}

function targetConfig(targetOS, targetArch) {
  const key = `${targetOS}/${targetArch}`;
  const targets = {
    'linux/amd64': {
      executable: 'ai-analyst',
      nodePlatform: 'linux',
      nodeArch: 'x64',
      nodeArchiveExt: 'tar.gz',
      playwrightHost: 'ubuntu24.04-x64',
    },
    'linux/arm64': {
      executable: 'ai-analyst',
      nodePlatform: 'linux',
      nodeArch: 'arm64',
      nodeArchiveExt: 'tar.gz',
      playwrightHost: 'ubuntu24.04-arm64',
    },
    'darwin/amd64': {
      executable: 'ai-analyst',
      nodePlatform: 'darwin',
      nodeArch: 'x64',
      nodeArchiveExt: 'tar.gz',
      playwrightHost: 'mac15',
    },
    'darwin/arm64': {
      executable: 'ai-analyst',
      nodePlatform: 'darwin',
      nodeArch: 'arm64',
      nodeArchiveExt: 'tar.gz',
      playwrightHost: 'mac15-arm64',
    },
    'windows/amd64': {
      executable: 'ai-analyst.exe',
      nodePlatform: 'win',
      nodeArch: 'x64',
      nodeArchiveExt: 'zip',
      playwrightHost: 'win64',
    },
  };
  const target = targets[key];
  if (!target) throw new Error(`Unsupported release target ${key}`);
  return target;
}

function readPlaywrightVersion() {
  const lockPath = path.join(repoRoot, 'package-lock.json');
  const lock = JSON.parse(fs.readFileSync(lockPath, 'utf8'));
  const version = lock?.packages?.['node_modules/playwright']?.version;
  if (!version) throw new Error('Could not find Playwright version in package-lock.json');
  return version;
}

function installNodeRuntime(destination, targetInfo, version) {
  const versionTag = `v${version}`;
  const archiveName = `node-${versionTag}-${targetInfo.nodePlatform}-${targetInfo.nodeArch}.${targetInfo.nodeArchiveExt}`;
  const cacheDir = path.resolve(String(args.cache || path.join(repoRoot, '.release-cache')));
  const archivePath = path.join(cacheDir, archiveName);
  fs.mkdirSync(cacheDir, { recursive: true });
  if (!fs.existsSync(archivePath)) {
    const url = `https://nodejs.org/dist/${versionTag}/${archiveName}`;
    console.log(`Downloading ${url}`);
    download(url, archivePath);
  }

  const extractRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'aia-node-runtime-'));
  try {
    extractArchive(archivePath, extractRoot, targetInfo.nodeArchiveExt);
    const extracted = fs.readdirSync(extractRoot)
      .map((entry) => path.join(extractRoot, entry))
      .find((entry) => fs.statSync(entry).isDirectory() && path.basename(entry).startsWith(`node-${versionTag}-`));
    if (!extracted) throw new Error(`Could not find extracted Node runtime in ${extractRoot}`);
    fs.cpSync(extracted, destination, { recursive: true, dereference: true });
    materializeSymlinks(destination);
  } finally {
    fs.rmSync(extractRoot, { recursive: true, force: true });
  }
}

function installPlaywrightPackage(destination, version) {
  const packageName = 'ai-analyst-atlassian-runtime';
  fs.mkdirSync(destination, { recursive: true });
  fs.writeFileSync(
    path.join(destination, 'package.json'),
    JSON.stringify({
      name: packageName,
      private: true,
      type: 'module',
      dependencies: { playwright: version },
    }, null, 2) + '\n',
  );
  runNpm(
    ['install', '--prefix', destination, '--omit=dev', '--ignore-scripts', '--package-lock=false'],
    {
      cwd: destination,
      env: { ...process.env, PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD: '1' },
    },
  );
  removePackageSelfLink(destination, packageName);
  materializeSymlinks(destination);
}

function installPlaywrightBrowser(atlassianRuntime, browsersPath, hostPlatform) {
  const cli = path.join(atlassianRuntime, 'node_modules', 'playwright', 'cli.js');
  fs.mkdirSync(browsersPath, { recursive: true });
  execFileSync(
    process.execPath,
    [cli, 'install', 'chromium'],
    {
      cwd: atlassianRuntime,
      stdio: 'inherit',
      env: {
        ...process.env,
        PLAYWRIGHT_BROWSERS_PATH: browsersPath,
        PLAYWRIGHT_HOST_PLATFORM_OVERRIDE: hostPlatform,
        PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS: '1',
      },
    },
  );
}

function installNativeToolRuntime(runtimeRoot, targetInfo) {
  const installed = [];
  const missing = [];
  const requiredTools = Object.keys(nativeToolRequirements);
  const sharedSource = findSharedNativeToolRuntime(targetInfo);
  if (sharedSource) {
    const provided = requiredTools.filter((name) => nativeRuntimeProvides(sharedSource, name, targetInfo));
    if (provided.length > 0) {
      const destination = path.join(runtimeRoot, 'native', 'tools');
      installNativeRuntimeDirectory(sharedSource, destination);
      for (const name of provided) {
        validateNativeToolRuntime(destination, name, targetInfo);
        installed.push(name);
      }
      console.log(`Bundled shared native tools (${provided.join(', ')}) from ${sharedSource}`);
    }
  }

  for (const name of requiredTools) {
    if (installed.includes(name)) continue;
    const source = findNativeToolRuntime(name, targetInfo);
    if (!source) {
      missing.push(name);
      continue;
    }
    const destination = path.join(runtimeRoot, 'native', name);
    installNativeRuntimeDirectory(source, destination);
    validateNativeToolRuntime(destination, name, targetInfo);
    installed.push(name);
    console.log(`Bundled native ${name} runtime from ${source}`);
  }
  if (missing.length > 0) {
    const message = [
      `Missing required native runtime for ${goos}/${goarch}: ${missing.join(', ')}.`,
      'Local PDF rendering needs Poppler and Tesseract; local file search needs ripgrep and fd.',
      'Provide a conda/native tools prefix with --native-tools-dir, pre-populate .release-cache/native/<goos>-<goarch>/{tools,poppler,tesseract,search}, or pass --allow-missing-native-tools only for a deliberately degraded dev build.',
    ].join(' ');
    if (!allowMissingNativeTools) {
      throw new Error(message);
    }
    console.warn(`${message} Continuing because --allow-missing-native-tools was set.`);
  }
  return installed;
}

function installNativeRuntimeDirectory(source, destination) {
  fs.rmSync(destination, { recursive: true, force: true });
  fs.mkdirSync(path.dirname(destination), { recursive: true });
  fs.cpSync(source, destination, { recursive: true, dereference: true });
  materializeSymlinks(destination);
}

function findSharedNativeToolRuntime(targetInfo) {
  const cacheRoot = path.resolve(String(args.cache || path.join(repoRoot, '.release-cache')));
  const candidates = [
    nativeToolsDir,
    path.join(cacheRoot, 'native', `${goos}-${goarch}`, 'tools'),
  ];
  if (os.homedir()) {
    candidates.push(path.join(os.homedir(), '.cache', 'codex-runtimes', 'codex-primary-runtime', 'dependencies'));
  }
  for (const candidate of candidates) {
    if (!candidate || !fs.existsSync(candidate) || !fs.statSync(candidate).isDirectory()) continue;
    if (!nativeRuntimeMatches(candidate, targetInfo)) continue;
    if (Object.keys(nativeToolRequirements).some((name) => nativeRuntimeProvides(candidate, name, targetInfo))) {
      return candidate;
    }
  }
  return '';
}

function findNativeToolRuntime(name, targetInfo) {
  const cacheRoot = path.resolve(String(args.cache || path.join(repoRoot, '.release-cache')));
  const candidates = [
    args[`${name}-runtime-dir`] ? path.resolve(String(args[`${name}-runtime-dir`])) : '',
    path.join(cacheRoot, 'native', `${goos}-${goarch}`, name),
  ];
  if (os.homedir()) {
    candidates.push(path.join(os.homedir(), '.cache', 'codex-runtimes', 'codex-primary-runtime', 'dependencies', 'native', name));
  }
  for (const candidate of candidates) {
    if (!candidate || !fs.existsSync(candidate) || !fs.statSync(candidate).isDirectory()) continue;
    if (!nativeRuntimeMatches(candidate, targetInfo)) continue;
    if (!nativeRuntimeProvides(candidate, name, targetInfo)) continue;
    return candidate;
  }
  return '';
}

function validateNativeToolRuntime(candidate, name, targetInfo) {
  const missing = nativeToolRequirements[name].filter((command) => !nativeRuntimeCommandPath(candidate, name, command, targetInfo));
  if (missing.length > 0) {
    throw new Error(`Native ${name} runtime at ${candidate} is missing required command(s): ${missing.join(', ')}`);
  }
}

function nativeRuntimeProvides(candidate, name, targetInfo) {
  return (nativeToolRequirements[name] || []).every((command) => nativeRuntimeCommandPath(candidate, name, command, targetInfo));
}

function nativeRuntimeCommandPath(candidate, requirementName, command, targetInfo) {
  for (const dir of nativeRuntimeCommandDirs(candidate, targetInfo)) {
    for (const executableName of nativeExecutableNames(command, targetInfo, requirementName === 'search')) {
      const toolPath = path.join(dir, executableName);
      if (executableFile(toolPath, targetInfo)) return toolPath;
    }
  }
  return '';
}

function nativeRuntimeCommandDirs(candidate, targetInfo) {
  const dirs = [
    path.join(candidate, 'bin'),
    path.join(candidate, 'poppler', 'bin'),
    path.join(candidate, 'tesseract', 'bin'),
    path.join(candidate, 'search', 'bin'),
  ];
  if (targetInfo.nodePlatform === 'win') {
    dirs.push(
      path.join(candidate, 'Library', 'bin'),
      path.join(candidate, 'poppler', 'Library', 'bin'),
      path.join(candidate, 'tesseract', 'Library', 'bin'),
      path.join(candidate, 'search', 'Library', 'bin'),
    );
  }
  return dirs;
}

function nativeExecutableNames(command, targetInfo, requireNativeExecutable = false) {
  if (targetInfo.nodePlatform !== 'win' || path.extname(command)) return [command];
  if (requireNativeExecutable) return [`${command}.exe`];
  return [`${command}.exe`, `${command}.cmd`, `${command}.bat`, command];
}

function executableFile(toolPath, targetInfo) {
  let stat;
  try {
    stat = fs.statSync(toolPath);
  } catch {
    return false;
  }
  if (!stat.isFile()) return false;
  if (targetInfo.nodePlatform === 'win') return true;
  return (stat.mode & 0o111) !== 0;
}

function nativeRuntimeMatches(candidate, targetInfo) {
  const manifestPath = path.join(candidate, 'manifest.json');
  if (!fs.existsSync(manifestPath)) return true;
  let manifest;
  try {
    manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
  } catch {
    return false;
  }
  const platform = String(manifest.targetPlatform || '').trim().toLowerCase();
  const arch = String(manifest.targetArch || '').trim().toLowerCase();
  if (platform && platform !== goos) return false;
  if (arch && !nativeArchMatches(arch, targetInfo)) return false;
  return true;
}

function nativeArchMatches(arch, targetInfo) {
  const accepted = new Set([goarch, targetInfo.nodeArch]);
  if (goarch === 'amd64') accepted.add('x64');
  if (goarch === '386') accepted.add('x86');
  if (goarch === 'arm64') accepted.add('aarch64');
  return accepted.has(arch);
}

function writeBundleReadme(destination, targetInfo, version, pwVersion, skippedBrowser, nativeRuntime) {
  const lines = [
    'AI Analyst release bundle',
    '',
    `Target: ${goos}/${goarch}`,
    `Bundled Node.js: ${version}`,
    `Bundled Playwright: ${pwVersion}`,
    skippedBrowser ? 'Bundled Chromium: skipped for this package command' : `Bundled Chromium host: ${targetInfo.playwrightHost}`,
    nativeRuntime.length > 0 ? `Bundled native tools: ${nativeRuntime.join(', ')}` : 'Bundled native tools: none',
    '',
    'Run the ai-analyst executable from this directory. Keep the runtime directory next to the executable; it contains the Node.js, Playwright, Chromium, and native tool sidecars required by integrations and local document rendering.',
    '',
  ];
  fs.writeFileSync(path.join(destination, 'README.txt'), lines.join('\n'));
}

function npmCommand() {
  return process.platform === 'win32' ? 'npm.cmd' : 'npm';
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
  console.log(`$ ${[npmCommand(), ...commandArgs].join(' ')}`);
  execFileSync(npmCommand(), commandArgs, { stdio: 'inherit', ...options });
}

function removePackageSelfLink(packageRoot, packageName) {
  const linkPath = path.join(packageRoot, 'node_modules', packageName);
  let linkStat;
  try {
    linkStat = fs.lstatSync(linkPath);
  } catch (err) {
    if (err.code === 'ENOENT') return;
    throw err;
  }
  if (!linkStat.isSymbolicLink()) return;

  const packagePath = normalizeFsPath(fs.realpathSync(packageRoot));
  const targetPath = normalizeFsPath(fs.realpathSync(linkPath));
  if (targetPath === packagePath) fs.rmSync(linkPath, { recursive: true, force: true });
}

function normalizeFsPath(value) {
  const normalized = path.resolve(value);
  return process.platform === 'win32' ? normalized.toLowerCase() : normalized;
}

function download(url, destination) {
  const tmp = `${destination}.tmp`;
  fs.rmSync(tmp, { force: true });
  execFileSync('curl', ['-fL', '--retry', '3', '-o', tmp, url], { stdio: 'inherit' });
  fs.renameSync(tmp, destination);
}

function extractArchive(archivePath, destination, archiveExt) {
  if (archiveExt !== 'zip') {
    execFileSync('tar', ['-xzf', archivePath, '-C', destination], { stdio: 'inherit' });
    return;
  }
  if (process.platform === 'win32') {
    execFileSync(
      'powershell',
      ['-NoProfile', '-Command', 'Expand-Archive', '-LiteralPath', archivePath, '-DestinationPath', destination, '-Force'],
      { stdio: 'inherit' },
    );
    return;
  }
  execFileSync('unzip', ['-q', archivePath, '-d', destination], { stdio: 'inherit' });
}

function materializeSymlinks(root) {
  for (let pass = 0; pass < 5; pass += 1) {
    const links = collectSymlinks(root);
    if (links.length === 0) return;
    for (const linkPath of links) {
      const targetPath = path.resolve(path.dirname(linkPath), fs.readlinkSync(linkPath));
      let targetStat;
      try {
        targetStat = fs.statSync(linkPath);
      } catch (err) {
        throw new Error(`Bundled runtime symlink ${linkPath} points to unavailable target ${targetPath}: ${err.message}`);
      }

      fs.rmSync(linkPath, { recursive: true, force: true });
      if (targetStat.isDirectory()) {
        fs.cpSync(targetPath, linkPath, { recursive: true, dereference: true });
      } else if (targetStat.isFile()) {
        fs.copyFileSync(targetPath, linkPath);
        fs.chmodSync(linkPath, targetStat.mode & 0o777);
      } else {
        throw new Error(`Bundled runtime symlink ${linkPath} points to unsupported file type ${targetPath}`);
      }
    }
  }
  throw new Error(`Bundled runtime still contains symlinks after normalization: ${collectSymlinks(root).join(', ')}`);
}

function collectSymlinks(root) {
  const links = [];
  walk(root, (entry) => {
    if (fs.lstatSync(entry).isSymbolicLink()) links.push(entry);
  });
  return links;
}

function walk(root, visit) {
  if (!fs.existsSync(root)) return;
  visit(root);
  if (!fs.lstatSync(root).isDirectory()) return;
  for (const entry of fs.readdirSync(root)) {
    walk(path.join(root, entry), visit);
  }
}
