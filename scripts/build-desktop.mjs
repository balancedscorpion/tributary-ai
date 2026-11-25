import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const projectRoot = path.resolve(scriptDir, '..');
const bundleRoot = path.join(projectRoot, 'src-tauri', 'target', 'release', 'bundle');
const macosBundleDir = path.join(bundleRoot, 'macos');
const dmgBundleDir = path.join(bundleRoot, 'dmg');

const locale = pickMacLocale();
const buildEnv = {
  ...process.env,
  CI: process.env.CI || 'true',
  LANG: locale,
  LC_ALL: locale,
  LC_CTYPE: locale,
};

try {
  cleanDmgState({ removeFinalDmg: true });
  run('npm', ['run', 'desktop:prepare', '--']);
  cleanDmgState({ removeFinalDmg: false });
  run('tauri', ['build']);
} finally {
  cleanDmgState({ removeFinalDmg: false });
}

function run(command, args) {
  console.log(`$ ${[command, ...args].join(' ')}`);
  const result = spawnSync(command, args, {
    cwd: projectRoot,
    env: buildEnv,
    stdio: 'inherit',
  });
  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }
}

function pickMacLocale() {
  const result = spawnSync('locale', ['-a'], { encoding: 'utf8' });
  const available = new Set((result.stdout || '').split(/\r?\n/).map((line) => line.trim()).filter(Boolean));
  if (available.has('en_US.UTF-8')) return 'en_US.UTF-8';
  if (available.has('en_GB.UTF-8')) return 'en_GB.UTF-8';
  return 'C';
}

function cleanDmgState({ removeFinalDmg }) {
  detachMountedBundleImages();
  detachVolumePaths();
  removeGeneratedDmgFiles(macosBundleDir, removeFinalDmg);
  removeGeneratedDmgFiles(dmgBundleDir, removeFinalDmg);
}

function detachMountedBundleImages() {
  const result = spawnSync('hdiutil', ['info'], { encoding: 'utf8' });
  if (result.status !== 0 || !result.stdout) return;

  const devices = new Set();
  let bundleImage = false;
  for (const line of result.stdout.split(/\r?\n/)) {
    if (line.includes('image-path') || line.includes('image-alias')) {
      bundleImage = line.includes(bundleRoot);
      continue;
    }
    if (!bundleImage || !line.startsWith('/dev/')) continue;

    const fields = line.trim().split(/\s+/);
    const mountPath = fields[fields.length - 1] || '';
    if (mountPath === '/Volumes/AI Analyst' || mountPath.startsWith('/Volumes/dmg.')) {
      devices.add(fields[0]);
    }
  }

  for (const device of devices) {
    detach(device);
  }
}

function detachVolumePaths() {
  if (!fs.existsSync('/Volumes')) return;

  for (const name of fs.readdirSync('/Volumes')) {
    if (name !== 'AI Analyst' && !name.startsWith('dmg.')) continue;
    detach(path.join('/Volumes', name));
  }
}

function detach(target) {
  const result = spawnSync('hdiutil', ['detach', target], { stdio: 'inherit' });
  if (result.status === 0) return;
  spawnSync('hdiutil', ['detach', '-force', target], { stdio: 'inherit' });
}

function removeGeneratedDmgFiles(dir, removeFinalDmg) {
  if (!fs.existsSync(dir)) return;

  for (const name of fs.readdirSync(dir)) {
    const isTempDmg = /^rw\..+\.dmg$/.test(name);
    const isFinalDmg = removeFinalDmg && name.endsWith('.dmg') && name !== 'icon.icns';
    if (!isTempDmg && !isFinalDmg) continue;
    fs.rmSync(path.join(dir, name), { force: true });
  }
}
