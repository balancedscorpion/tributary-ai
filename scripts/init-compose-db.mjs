import { randomBytes } from 'node:crypto';
import { mkdtempSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const directory = fileURLToPath(new URL('../.local-db', import.meta.url));
const staging = mkdtempSync(directory + '.tmp-');

try {
  const password = randomBytes(32).toString('hex');
  const rootPassword = randomBytes(32).toString('hex');
  // The host directory is owner-only. The MySQL container drops privileges
  // and needs to read the individual files mounted as Compose secrets.
  writeFileSync(join(staging, 'mysql-password'), password, { mode: 0o444 });
  writeFileSync(join(staging, 'mysql-root-password'), rootPassword, { mode: 0o444 });
  const mysql = {
    user: 'ai_analyst', password, addr: 'mysql:3306', database: 'ai_analyst',
    params: { multiStatements: 'true' },
  };
  writeFileSync(join(staging, 'db.json'), JSON.stringify({ mysql }) + '\n', { mode: 0o400 });
  renameSync(staging, directory);
  console.log('Created local MySQL credentials in .local-db/.');
} catch (error) {
  if (!['ENOTEMPTY', 'EEXIST'].includes(error.code)) throw error;
  console.error('Local database configuration already exists. Its credentials have been preserved.');
  process.exitCode = 1;
} finally {
  rmSync(staging, { recursive: true, force: true });
}
