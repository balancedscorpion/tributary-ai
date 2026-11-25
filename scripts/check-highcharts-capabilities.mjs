#!/usr/bin/env node
import {
  buildHighchartsCapabilitiesSnapshot,
  OUTPUT_PATHS,
  readSnapshot,
  snapshotDigest,
} from './highcharts-capabilities-shared.mjs';

const expected = buildHighchartsCapabilitiesSnapshot();
const expectedDigest = snapshotDigest(expected);

let failed = false;
for (const outputPath of OUTPUT_PATHS) {
  try {
    const actual = readSnapshot(outputPath);
    const actualDigest = snapshotDigest(actual);
    if (actualDigest !== expectedDigest) {
      failed = true;
      console.error(`Stale Highcharts capabilities snapshot: ${outputPath}`);
    }
  } catch (error) {
    failed = true;
    console.error(`Missing or invalid Highcharts capabilities snapshot: ${outputPath}`);
    console.error(String(error?.message || error));
  }
}

if (failed) {
  console.error('Run: npm run highcharts:sync');
  process.exit(1);
}

console.log('Highcharts capabilities snapshot is up to date.');
