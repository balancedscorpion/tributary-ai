import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('QueryInfoPanel resolves and renders provider-native job links', () => {
  const source = sourceAt('components/QueryInfoPanel.tsx');

  assert.ok(
    source.includes('resolveQueryNativeJobLink('),
    'QueryInfoPanel should resolve native query job links from query metadata'
  );
  assert.ok(
    source.includes('data-testid="query-info-native-link"') && source.includes('target="_blank"'),
    'QueryInfoPanel should render a clickable external job link'
  );
  assert.ok(
    source.includes('provider_job_id') && source.includes('provider_job_url'),
    'QueryInfoPanel should consider provider_job_id/provider_job_url metadata fields'
  );
});
