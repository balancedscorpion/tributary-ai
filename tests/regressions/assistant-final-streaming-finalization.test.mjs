import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('assistant_final with empty payload still finalizes streamed assistant step', () => {
  const source = sourceAt('hooks/useChat.ts');

  assert.ok(
    source.includes('if (!hasFinalText && !finalMessage) {')
      && source.includes('finalizeLatestAssistantStepIfIdle(threadId);')
      && source.includes('pruneTrailingEmptyAssistant(threadId);'),
    'empty assistant_final branch should finalize latest assistant step and then prune empties'
  );
});
