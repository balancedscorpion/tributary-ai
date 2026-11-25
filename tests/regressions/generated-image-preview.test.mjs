import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('generate_image registers an inline preview that never dumps base64', () => {
  const registry = sourceAt('components/chat/toolActivity/inlinePreviewRegistry.tsx');

  assert.ok(
    registry.includes('match: (toolCall) => isImageGenerationToolName(toolCall.name)') &&
      registry.includes('component: GeneratedImageInlinePreview'),
    'the registry must match generate_image by tool name so failures never hit the generic base64 dump',
  );
  assert.ok(
    registry.includes('/^data:image\\//i.test(content)'),
    'the preview must gate the thumbnail on a data:image URI',
  );
  assert.ok(
    registry.includes('onError={() => setThumbFailed(true)}'),
    'a broken thumbnail must fall back to text, never a broken-image glyph',
  );
  assert.ok(
    registry.includes('return GenericInlinePreview;'),
    'the generic fallback must remain for every other tool',
  );
});

test('resultSummary owns generate_image before any generic string branch', () => {
  const summary = sourceAt('components/chat/toolActivity/resultSummary.ts');

  assert.ok(
    summary.includes('function getGenerateImageSummary(') &&
      summary.includes('isImageGenerationToolName(toolCall.name)'),
    'a dedicated generate_image summary branch must exist',
  );
  const branchIndex = summary.indexOf('return getGenerateImageSummary(envelope);');
  const stringDumpIndex = summary.indexOf("if (typeof result === 'string')");
  assert.ok(branchIndex > 0, 'the generate_image branch must be present');
  assert.ok(stringDumpIndex > 0, 'the generic string branch must still exist');
  assert.ok(
    branchIndex < stringDumpIndex,
    'the generate_image branch must run before the generic string dump so no data URI reaches compactText',
  );
  assert.ok(
    summary.includes('`Generated ${width}×${height} ${format}`'),
    'success summary must read "Generated W×H FORMAT"',
  );
});

test('ImageRenderer ships zoom controls and a broken-image fallback', () => {
  const renderer = sourceAt('components/renderers/type/ImageRenderer.tsx');

  assert.ok(
    renderer.includes('role="toolbar"') && renderer.includes('aria-label="Image controls"'),
    'the image canvas must expose a labelled controls toolbar',
  );
  assert.ok(
    renderer.includes('aria-label="Zoom in"') && renderer.includes('aria-label="Zoom out"'),
    'zoom buttons must carry accessible labels',
  );
  assert.ok(
    renderer.includes('onError={() => setErrored(true)}') &&
      renderer.includes('Preview unavailable') &&
      renderer.includes('role="status"'),
    'a load error must render the accessible "Preview unavailable" fallback, not a broken glyph',
  );
  assert.ok(
    renderer.includes('generatedImageAltText(artifact)'),
    'generated images must use prompt-derived alt text',
  );
});
