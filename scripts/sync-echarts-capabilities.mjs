#!/usr/bin/env node
import { buildEchartsCapabilitiesSnapshot, OUTPUT_PATHS, writeSnapshot } from './echarts-capabilities-shared.mjs';

const snapshot = buildEchartsCapabilitiesSnapshot();
writeSnapshot(snapshot, OUTPUT_PATHS);

console.log('Wrote ECharts capabilities snapshot:');
for (const outputPath of OUTPUT_PATHS) {
  console.log(` - ${outputPath}`);
}
console.log(`Capability version: ${snapshot.capability_version}`);
