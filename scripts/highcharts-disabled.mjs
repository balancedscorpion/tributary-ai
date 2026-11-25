#!/usr/bin/env node

const mode = String(process.argv[2] || 'check').trim() || 'check';
console.log(`Highcharts ${mode} skipped: Highcharts package download is disabled for licensing reasons.`);
