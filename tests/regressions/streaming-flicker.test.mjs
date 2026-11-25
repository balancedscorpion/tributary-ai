import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function read(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('MessageList prefers _uiStableId before content hash fallback', () => {
  const source = read('components/chat/MessageList.tsx');
  const uiStableIdIndex = source.indexOf('const uiStableId = typeof message._uiStableId');
  const contentFallbackIndex = source.indexOf('const source = buildContentFallbackIdentitySource(message);');
  assert.ok(uiStableIdIndex > -1, 'Expected MessageList to read message._uiStableId');
  assert.ok(contentFallbackIndex > -1, 'Expected MessageList to keep content fallback keying');
  assert.ok(uiStableIdIndex < contentFallbackIndex, 'Expected _uiStableId keying to run before content fallback');
});

test('useChat batches streaming deltas and assigns stable UI ids', () => {
  const source = read('hooks/useChat.ts');
  assert.ok(source.includes('function ensureUiStableIds('), 'Expected ensureUiStableIds helper in useChat');
  assert.ok(source.includes('let pendingTextDelta = \'\';'), 'Expected pending text batching state');
  assert.ok(source.includes('let pendingThinkingDelta = \'\';'), 'Expected pending thinking batching state');
  assert.ok(source.includes('requestAnimationFrame(() => {'), 'Expected animation-frame batching for streamed deltas');
  assert.ok(source.includes("if (name !== 'text_delta' && name !== 'thinking_delta')"), 'Expected flush before non-delta events');
});
