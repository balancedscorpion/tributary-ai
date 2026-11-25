import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('App renders artifacts through the shared modal overlay instead of a side split panel', () => {
  const source = sourceAt('App.tsx');
  const overlay = sourceAt('components/InspectorOverlay.tsx');

  assert.match(
    source,
    /<div className="flex-1 min-w-0">\s*\{chatPanel\}\s*<\/div>/,
    'App should keep the chat panel mounted at full layout width'
  );
  assert.ok(
    source.includes('<InspectorOverlay onClose={() => setIsPanelOpen(false)}>'),
    'App should mount artifact viewing through InspectorOverlay'
  );
  assert.ok(
    overlay.includes('data-testid="inspector-backdrop"') &&
      overlay.includes('max-w-screen-2xl') &&
      overlay.includes('rounded-xl') &&
      overlay.includes('shadow-2xl'),
    'InspectorOverlay should render a bounded modal surface over a backdrop'
  );
  assert.ok(
    !source.includes('ResizablePanelGroup') &&
      !source.includes('ResizablePanel') &&
      !source.includes('ResizableHandle'),
    'App should not render the artifact viewer as a resizable side panel'
  );
});
