import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('RunningQueryCard includes progress-bar and native-link rendering hooks', () => {
  const source = sourceAt('components/chat/RunningQueryCard.tsx');

  assert.ok(
    source.includes('data-testid="running-query-progress-bar"'),
    'RunningQueryCard progress bar hook is missing'
  );
  assert.ok(
    source.includes('data-testid="running-query-native-link"'),
    'RunningQueryCard native provider link hook is missing'
  );
  assert.ok(
    source.includes('provider_job_id') && source.includes('provider_job_url'),
    'RunningQueryCard should prefer provider_job_id/provider_job_url fields'
  );
});

test('ToolResultBlock marks async running card completed when get_query_results artifact is ready', () => {
  const source = sourceAt('components/chat/ToolResultBlock.tsx');
  assert.ok(
    source.includes("artifact?.metadata?.tool_name === 'get_query_results'"),
    'ToolResultBlock should detect get_query_results artifact completion'
  );
  assert.ok(
    source.includes('initialStatus={derivedInitialStatus}'),
    'ToolResultBlock should pass derived terminal status to RunningQueryCard'
  );
});
