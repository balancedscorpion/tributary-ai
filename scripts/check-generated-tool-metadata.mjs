#!/usr/bin/env node
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';

const repoRoot = process.cwd();
const generatedPath = path.join(repoRoot, 'lib', 'generatedToolMetadata.ts');

function firstDifferenceLine(actual, expected) {
  const actualLines = actual.split(/\r?\n/);
  const expectedLines = expected.split(/\r?\n/);
  const max = Math.max(actualLines.length, expectedLines.length);
  for (let index = 0; index < max; index += 1) {
    if (actualLines[index] !== expectedLines[index]) {
      return {
        line: index + 1,
        actual: actualLines[index] ?? '<missing>',
        expected: expectedLines[index] ?? '<missing>',
      };
    }
  }
  return null;
}

let expected;
try {
  expected = execFileSync('go', ['run', './cmd/toolinventorygen'], {
    cwd: path.join(repoRoot, 'server'),
    encoding: 'utf8',
    stdio: ['ignore', 'pipe', 'pipe'],
  });
} catch (error) {
  console.error('Failed to generate tool metadata from backend manifest.');
  if (error.stderr) console.error(String(error.stderr).trim());
  process.exit(1);
}

let actual;
try {
  actual = fs.readFileSync(generatedPath, 'utf8');
} catch (error) {
  console.error(`Missing generated tool metadata: ${generatedPath}`);
  console.error('Run: (cd server && go generate ./internal/chat/tools)');
  process.exit(1);
}

if (actual !== expected) {
  const diff = firstDifferenceLine(actual, expected);
  console.error('Generated tool metadata is stale.');
  if (diff) {
    console.error(`First difference at line ${diff.line}:`);
    console.error(`  actual:   ${diff.actual}`);
    console.error(`  expected: ${diff.expected}`);
  }
  console.error('Run: (cd server && go generate ./internal/chat/tools)');
  process.exit(1);
}

console.log('Generated tool metadata is up to date.');
