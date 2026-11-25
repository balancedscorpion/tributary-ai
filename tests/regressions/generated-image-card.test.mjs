import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('CollapsedToolsSummary dispatches GeneratedImageCard before the generic tool row', () => {
  const source = sourceAt('components/chat/CollapsedToolsSummary.tsx');

  assert.ok(
    source.includes("import GeneratedImageCard from \"./GeneratedImageCard\""),
    'must import the purpose-built card'
  );
  assert.ok(
    source.includes('isImageGenerationToolName') &&
      source.includes('from "@/lib/generatedImageArtifacts"'),
    'must classify generated-image tools through the shared helper'
  );

  const dispatchIdx = source.indexOf('if (isImageGenerationToolName(tc.name)) {');
  const genericIdx = source.indexOf('// Non-SQL tool card');
  assert.ok(dispatchIdx !== -1, 'generated-image dispatch branch must exist');
  assert.ok(genericIdx !== -1, 'generic tool card branch must still exist');
  assert.ok(
    dispatchIdx < genericIdx,
    'generated-image dispatch must run before the generic non-SQL branch'
  );
  assert.ok(
    source.includes('<GeneratedImageCard'),
    'the branch must render GeneratedImageCard'
  );
  assert.ok(
    source.includes('surfacedGeneratedImageCards') &&
      source.includes('isGeneratedImageItem(item) ? null : renderAnchoredToolCard'),
    'completed image cards must remain visible outside the collapsed tool-audit details'
  );
  assert.ok(
    source.includes('(isLastMessage === true || Boolean(imgProgressEvent))') &&
      !source.includes('isLastMessage === true &&\n                  imgHasStartedAt'),
    'live image progress must remain authoritative even when persisted assistant data drops UI-only timing fields'
  );
  assert.ok(
    source.includes('findToolProgressForCall') &&
      source.includes('String(progress?.tool_call_id || "").trim() !== callId'),
    'progress lookup must survive persisted assistant messages replacing client-only run IDs'
  );

  // The existing card-click / preview strings that sibling regression tests
  // assert on must remain untouched by this edit.
  assert.ok(
    source.includes(
      'onClick={bodyOpensAudit ? handleOpenAudit : () => togglePreview(previewKey)}'
    ),
    'must not disturb the existing generic card body click contract'
  );
});

test('file-backed image envelopes hydrate through the artifact endpoint', () => {
  const source = sourceAt('lib/toolResultArtifact.ts');
  assert.ok(
    source.includes("presentationNativeViewer === 'image'") &&
      source.includes("presentationResultKind === 'image'"),
    'declared image results must be shaped as image artifacts'
  );
  assert.ok(
    source.includes("source: 'generated_image'") &&
      source.includes('placeholder: true'),
    'file-backed generated images must remain hydratable placeholders until artifact metadata loads'
  );
});

test('GeneratedImageCard honours the shared test-id and a11y contract', () => {
  const source = sourceAt('components/chat/GeneratedImageCard.tsx');

  for (const id of [
    'data-testid="generated-image-card"',
    'data-testid="generated-image-gallery"',
    'data-testid="generated-image-tile"',
    'data-testid="generated-image-broken-preview"',
  ]) {
    assert.ok(source.includes(id), `missing test-id: ${id}`);
  }

  for (const state of [
    '"queued"',
    '"generating"',
    '"completed"',
    '"moderation_blocked"',
    '"cancelled"',
    '"interrupted"',
    '"failed"',
  ]) {
    assert.ok(source.includes(state), `missing data-state value: ${state}`);
  }

  assert.ok(
    source.includes('data-error-category'),
    'failed cards must expose data-error-category'
  );
  assert.ok(
    source.includes('classifyGeneratedImageError'),
    'error category must come from the shared classifier'
  );
});

test('GeneratedImageCard never infers cancellation from a missing result', () => {
  const source = sourceAt('components/chat/GeneratedImageCard.tsx');
  assert.ok(
    !source.includes('if (!isRunning) return { kind: "cancelled" }'),
    'a result-delivery race must not be labelled cancelled'
  );
  assert.ok(
    source.includes('if (!isRunning) return { kind: "interrupted" }'),
    'missing terminal evidence must use an honest unknown/interrupted state'
  );
  assert.ok(
    source.includes('firstString(args.prompt)'),
    'live and interrupted cards must retain the prompt directly from tool args'
  );
  assert.ok(
    source.includes('if (progressEvent) return { kind: "generating" }'),
    'requesting and retrying progress must keep the visible card in its generating state'
  );
});

test('GeneratedImageCard uses prompt-derived alt text and stable-aspect sizing', () => {
  const source = sourceAt('components/chat/GeneratedImageCard.tsx');

  assert.ok(
    source.includes('generatedImageAltText('),
    'inline <img> must use prompt-derived alt text'
  );
  assert.ok(
    source.includes('max-h-[280px]'),
    'the reserved image frame must cap at ~280px (no 40x40 thumbnail)'
  );
  assert.ok(
    source.includes('Math.round(280 * safeRatio)'),
    'the frame width must be capped from its ratio so portrait/square states do not stretch card-wide'
  );
  assert.ok(
    source.includes('aspectRatio'),
    'a reserved aspect box must prevent layout shift'
  );
  assert.ok(
    source.includes('generatedImageAspectRatio('),
    'the reserved box must derive its ratio from the request args'
  );
});

test('GeneratedImageCard gates animation behind motion-safe', () => {
  const source = sourceAt('components/chat/GeneratedImageCard.tsx');
  assert.ok(source.includes('motion-safe:'), 'all animation must be motion-safe');
  assert.ok(
    source.includes('motion-safe:animate-[shimmer_1.5s_ease-in-out_infinite]'),
    'the generating state must shimmer like QueryExecutionCard'
  );
  assert.ok(
    source.includes('role="status"') && source.includes('aria-live="polite"'),
    'boundary announcements must use a polite status region'
  );
});

test('moderation-blocked state is non-retryable: no Retry, only Edit prompt', () => {
  const source = sourceAt('components/chat/GeneratedImageCard.tsx');

  // The whole file must never offer a "Retry" affordance — transient failures
  // draft a new generation and are labelled "Regenerate" instead.
  assert.ok(
    !/\bRetry\b/.test(source),
    'the card must never render a "Retry" action (moderation is non-retryable; transient failures use Regenerate)'
  );
  assert.ok(
    source.includes('Blocked by content policy'),
    'moderation state must carry generic non-judgmental copy'
  );
  assert.ok(
    source.includes('Edit prompt'),
    'moderation state must offer Edit prompt as the primary action'
  );
  assert.ok(
    source.includes('Regenerate'),
    'transient failures must offer Regenerate (drafts, never auto-runs)'
  );
});

test('GeneratedImageCard exposes the exact accessible action names', () => {
  const source = sourceAt('components/chat/GeneratedImageCard.tsx');
  for (const name of [
    'Open image',
    'Use in chat',
    'Download image',
    'Copy prompt',
    'Edit prompt',
    'Regenerate image',
    'Image details',
  ]) {
    assert.ok(source.includes(name), `missing accessible action name: ${name}`);
  }
});
