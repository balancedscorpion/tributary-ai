import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

// ---------------------------------------------------------------------------
// lib/scanBudget.ts — behavioral tests. The module is pure (no runtime
// imports), so esbuild can compile it standalone for real execution.
// ---------------------------------------------------------------------------

let scanBudgetPromise = null;
async function loadScanBudget() {
  if (!scanBudgetPromise) {
    scanBudgetPromise = (async () => {
      const { transformSync } = await import('esbuild');
      const { code } = transformSync(sourceAt('lib/scanBudget.ts'), { loader: 'ts', format: 'esm' });
      const file = path.join(os.tmpdir(), `scan-budget-${process.pid}-${Date.now()}.mjs`);
      fs.writeFileSync(file, code);
      try {
        return await import(pathToFileURL(file).href);
      } finally {
        fs.unlinkSync(file);
      }
    })();
  }
  return scanBudgetPromise;
}

const GIB = 1024 * 1024 * 1024;

test('parseGibInput: blank means default, invalid means rejected, values are bytes', async () => {
  const { parseGibInput } = await loadScanBudget();
  assert.equal(parseGibInput(''), undefined);
  assert.equal(parseGibInput('   '), undefined);
  assert.equal(parseGibInput('250'), 250 * GIB);
  assert.equal(parseGibInput('1.5'), Math.round(1.5 * GIB));
  assert.equal(parseGibInput('1,5'), Math.round(1.5 * GIB), 'comma decimals are accepted');
  assert.equal(parseGibInput('abc'), null);
  assert.equal(parseGibInput('-4'), null);
  assert.equal(parseGibInput('0'), null);
  assert.equal(parseGibInput('1e3'), null, 'exponent notation is rejected as unclear input');
});

test('normalizeScanBudget: approved ceiling is never below the effective automatic limit', async () => {
  const { normalizeScanBudget, DEFAULT_BIGQUERY_AUTOMATIC_SCAN_BYTES } = await loadScanBudget();

  // Approved below configured automatic clamps up.
  assert.deepEqual(
    normalizeScanBudget(200 * GIB, 50 * GIB),
    { automaticScanBytes: 200 * GIB, approvedScanBytes: 200 * GIB },
  );
  // Valid band is preserved.
  assert.deepEqual(
    normalizeScanBudget(100 * GIB, 500 * GIB),
    { automaticScanBytes: 100 * GIB, approvedScanBytes: 500 * GIB },
  );
  // Approved with automatic unset clamps against the 100 GiB default in force.
  assert.deepEqual(
    normalizeScanBudget(undefined, 50 * GIB),
    { approvedScanBytes: DEFAULT_BIGQUERY_AUTOMATIC_SCAN_BYTES },
  );
  // Garbage and non-positive values drop out instead of persisting.
  assert.deepEqual(normalizeScanBudget('nope', -3), {});
  assert.deepEqual(normalizeScanBudget(null, undefined), {});
  // Numeric strings (JSON int64s) are accepted.
  assert.deepEqual(
    normalizeScanBudget(String(150 * GIB), String(300 * GIB)),
    { automaticScanBytes: 150 * GIB, approvedScanBytes: 300 * GIB },
  );
});

test('default automatic limit matches the legacy 100 GiB policy', async () => {
  const { DEFAULT_BIGQUERY_AUTOMATIC_SCAN_BYTES, effectiveAutomaticScanBytes } = await loadScanBudget();
  assert.equal(DEFAULT_BIGQUERY_AUTOMATIC_SCAN_BYTES, 100 * GIB);
  assert.equal(effectiveAutomaticScanBytes(undefined), 100 * GIB);
  assert.equal(effectiveAutomaticScanBytes({ automaticScanBytes: 25 * GIB }), 25 * GIB);
  // constants.ts must derive the billing default from this constant, not restate it.
  const constants = sourceAt('constants.ts');
  assert.ok(
    constants.includes('bigQueryMaximumBytesBilled: DEFAULT_BIGQUERY_AUTOMATIC_SCAN_BYTES'),
    'DEFAULT_DATABASE_QUERY_POLICY must reuse the scan-budget default'
  );
});

test('GiB input round-trip and cost formatting stay honest about binary units', async () => {
  const { bytesToGibInput, parseGibInput, formatScanBytes } = await loadScanBudget();
  assert.equal(bytesToGibInput(undefined), '');
  assert.equal(bytesToGibInput(100 * GIB), '100');
  assert.equal(bytesToGibInput(Math.round(1.5 * GIB)), '1.5');
  assert.equal(parseGibInput(bytesToGibInput(250 * GIB)), 250 * GIB);
  assert.equal(formatScanBytes(100 * GIB), '100 GiB');
  assert.equal(formatScanBytes(142_318_642_339), '132.5 GiB');
  assert.equal(formatScanBytes(1024 * GIB), '1 TiB');
  assert.equal(formatScanBytes('107374182400'), '100 GiB', 'string int64s format');
  assert.equal(formatScanBytes(undefined), '');
});

// ---------------------------------------------------------------------------
// Wiring contracts — the fields must survive every layer they pass through.
// ---------------------------------------------------------------------------

test('DataSourceState carries the two scan-budget policy levels', () => {
  const types = sourceAt('types.ts');
  assert.ok(types.includes('automaticScanBytes?: number;'), 'DataSourceState must expose automaticScanBytes');
  assert.ok(types.includes('approvedScanBytes?: number;'), 'DataSourceState must expose approvedScanBytes');
});

test('settings migration preserves and normalizes scan-budget fields', () => {
  const migration = sourceAt('services/settingsMigration.ts');
  assert.ok(
    migration.includes("import { normalizeScanBudget } from '../lib/scanBudget';"),
    'migration must reuse the shared normalizer'
  );
  assert.ok(
    migration.includes('...normalizeScanBudget((raw as any).automaticScanBytes, (raw as any).approvedScanBytes)'),
    'ensureDataSourceState must keep scan-budget fields instead of stripping them'
  );
});

test('BigQuery settings sheet wires GiB inputs through the connection slice', () => {
  const tab = sourceAt('components/settings/McpIntegrationsTab.tsx');
  // dataSourceState re-materializes a fixed field set; forgetting these lines
  // silently drops saved budgets on the next connection-state write.
  assert.ok(tab.includes('automaticScanBytes: raw?.automaticScanBytes'), 'dataSourceState must carry automaticScanBytes');
  assert.ok(tab.includes('approvedScanBytes: raw?.approvedScanBytes'), 'dataSourceState must carry approvedScanBytes');
  assert.ok(tab.includes("applyBigQueryScanBudget('automatic', parsed)"), 'automatic input must write through the normalizer');
  assert.ok(tab.includes("applyBigQueryScanBudget('approved', parsed)"), 'approved input must write through the normalizer');
  assert.ok(tab.includes('...normalizeScanBudget(automatic, approved)'), 'writes must enforce approved >= automatic');
  assert.ok(tab.includes('Automatic scan limit (GiB)'), 'automatic field keeps its plain-language label');
  assert.ok(tab.includes('One-time approval ceiling (GiB)'), 'approval field keeps its plain-language label');
  // A blank ceiling means "no ceiling — oversized scans ask first", never
  // "block everything over the automatic limit". The help copy and the
  // placeholder must both tell that truth.
  assert.ok(
    tab.includes('Leave blank for no ceiling'),
    'ceiling help must explain that blank keeps the approval path open'
  );
  assert.ok(
    !tab.includes('Leave blank to block'),
    'ceiling help must not promise blanket blocking on blank'
  );
  assert.ok(tab.includes('placeholder="No ceiling"'), 'ceiling placeholder must not suggest a numeric default');
  assert.ok(
    tab.includes('can&apos;t be below the automatic limit'),
    'clamping must be explained to the user, not silent'
  );
  assert.ok(
    tab.includes('setBqScanDrafts({ automatic: null, approved: null });\n  }, [sheetOpenId]);'),
    'sheet open/close must reset in-progress drafts so discard cannot be contradicted'
  );
});

test('scan-budget approvals present as one-time cost authorization', () => {
  const presentation = sourceAt('lib/approvalPresentation.ts');
  const card = sourceAt('components/approvals/ApprovalCard.tsx');

  assert.ok(
    presentation.includes("approval.operation === 'query_scan_budget'") &&
      presentation.includes("approval.subject?.operation === 'query_scan_budget'"),
    'isScanBudgetApproval must recognize the backend operation on both fields'
  );
  assert.ok(
    presentation.includes('/^run_bigquery_query$/'),
    'run_bigquery_query needs a phrase rule (never a humanized tool name)'
  );
  assert.ok(
    presentation.includes('Run a BigQuery query scanning about'),
    'the phrase must lead with the estimated scan size'
  );
  assert.ok(
    card.includes("isScanBudgetApproval(approval) ? ['once'] : approval.options"),
    'scan-budget cards must never offer thread/always scopes'
  );
  assert.ok(card.includes('<ScanBudgetPreview approval={approval} />'), 'card must claim the scan-budget preview');
  for (const key of ['estimated_bytes', 'automatic_scan_bytes', 'authorized_scan_bytes', 'approved_scan_bytes', 'project_id', 'sql_preview']) {
    assert.ok(card.includes(key), `scan-budget preview must read preview.${key}`);
  }
  assert.ok(
    card.includes('This approval allows') && card.includes('Connection hard ceiling'),
    'the one-query execution cap must be distinct from the connection hard ceiling'
  );
  assert.ok(
    card.includes('scanBudgetMeaning()'),
    'the meaning line must use cost framing instead of categoryMeaning'
  );
});
