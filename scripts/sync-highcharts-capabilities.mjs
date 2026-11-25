#!/usr/bin/env node
import { buildHighchartsCapabilitiesSnapshot, OUTPUT_PATHS, writeSnapshot } from './highcharts-capabilities-shared.mjs';

const snapshot = buildHighchartsCapabilitiesSnapshot();
writeSnapshot(snapshot, OUTPUT_PATHS);

console.log('Wrote Highcharts capabilities snapshot:');
for (const outputPath of OUTPUT_PATHS) {
  console.log(` - ${outputPath}`);
}
console.log(`Capability version: ${snapshot.capability_version}`);
