#!/usr/bin/env node
import {
  buildEchartsCapabilitiesSnapshot,
  OUTPUT_PATHS,
  readSnapshot,
  snapshotDigest,
} from './echarts-capabilities-shared.mjs';

const expected = buildEchartsCapabilitiesSnapshot();
const expectedDigest = snapshotDigest(expected);

let failed = false;
for (const outputPath of OUTPUT_PATHS) {
  try {
    const actual = readSnapshot(outputPath);
    const actualDigest = snapshotDigest(actual);
    if (actualDigest !== expectedDigest) {
      failed = true;
      console.error(`Stale ECharts capabilities snapshot: ${outputPath}`);
    }
  } catch (error) {
    failed = true;
    console.error(`Missing or invalid ECharts capabilities snapshot: ${outputPath}`);
    console.error(String(error?.message || error));
  }
}

if (failed) {
  console.error('Run: npm run echarts:sync');
  process.exit(1);
}

console.log('ECharts capabilities snapshot is up to date.');
