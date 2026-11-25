import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('origin navigation forwards the tool call id into a reveal request', () => {
  const app = sourceAt('App.tsx');
  assert.ok(
    app.includes("requestToolCallReveal(callId, {"),
    'App.handleOpenOriginThread must request a tool-call reveal when a call id is present'
  );
  assert.ok(
    !app.includes('_toolCallId'),
    'the origin tool call id must not be discarded with an underscore parameter'
  );
  assert.ok(
    app.match(/handleOpenOriginThread[\s\S]{0,700}onMiss/),
    'an unclaimed reveal must surface the miss to the user instead of failing silently'
  );
});

test('reveal requests expire instead of lingering forever', () => {
  const lib = sourceAt('lib/toolCallReveal.ts');
  assert.ok(
    lib.includes('DEFAULT_REVEAL_TIMEOUT_MS'),
    'pending reveals need a timeout so stale requests cannot flash a future thread'
  );
  assert.ok(
    lib.includes('missed?.onMiss?.()'),
    'the timeout path must invoke onMiss for the fallback message'
  );
  assert.ok(
    lib.match(/function claimToolCallReveal[\s\S]*?return false;[\s\S]*?clearPending\(\);[\s\S]*?return true;/),
    'claims must be consume-once so only the owning group reveals'
  );
});

test('tool activity groups claim reveals, expand, and anchor every card', () => {
  const summary = sourceAt('components/chat/CollapsedToolsSummary.tsx');
  assert.ok(
    summary.includes('claimToolCallReveal(pendingId)'),
    'the owning group must claim the pending reveal'
  );
  assert.ok(
    summary.match(/claimToolCallReveal\(pendingId\)\)[\s\S]{0,80}disclosure\.setOpen\(true\)/),
    'a claimed reveal must expand the group from the minimized pill'
  );
  assert.ok(
    summary.includes('data-tool-call-anchor'),
    'every tool card needs a per-call DOM anchor for provenance deep links'
  );
  assert.ok(
    summary.includes('subscribeToolCallReveal(tryClaim)'),
    'already-mounted groups must react to reveals requested after mount'
  );
  assert.ok(
    summary.includes('prefers-reduced-motion: reduce'),
    'reveal scrolling must respect reduced-motion (auto instead of smooth)'
  );
});
