import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('useChat extracts and stores plot_spec metadata from tool result envelopes', () => {
  const source = sourceAt('hooks/useChat.ts');

  assert.ok(
    source.includes('const envelopePlotSpec = isPlainObject((envelope as any).plot_spec)'),
    'useChat should detect plot_spec directly on the envelope'
  );
  assert.ok(
    source.includes('isPlainObject(result) && isPlainObject((result as any).plot_spec)'),
    'useChat should detect plot_spec nested under envelope result payloads'
  );
  assert.ok(
    source.includes("...(envelopePlotSpec ? { plot_spec: envelopePlotSpec } : {})"),
    'buildArtifactFromEnvelope metadata should include plot_spec when present'
  );
  assert.ok(
    source.includes("...(chartSpecVersion ? { chart_spec_version: chartSpecVersion } : {})"),
    'buildArtifactFromEnvelope metadata should include chart_spec_version when present'
  );
  assert.ok(
    source.includes("...(chartModules ? { chart_modules: chartModules } : {})"),
    'buildArtifactFromEnvelope metadata should include chart_modules when present'
  );
});
