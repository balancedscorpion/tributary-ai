import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('suppressed query poll results finalize the current assistant thinking step', () => {
  const source = sourceAt('hooks/useChat.ts');

  assert.ok(
    source.includes('const finalizeLatestAssistantStepIfIdle = useCallback(')
      && source.includes('if (visibleToolCalls.length > 0) return prev;')
      && source.includes('const needsFinalize =')
      && source.includes('_uiStreaming: false,'),
    'useChat should include helper logic that finalizes the latest idle assistant step'
  );

  assert.ok(
    source.includes('} else {')
      && source.includes('removeToolCallFromAssistantMessages(threadId, callId);')
      && source.includes('finalizeLatestAssistantStepIfIdle(threadId);'),
    'tool_result suppression should remove the suppressed call and finalize the current thinking step'
  );

  assert.ok(
    source.includes('if (suppressUi) {')
      && source.includes('if (callId)')
      && source.includes('removeToolCallFromAssistantMessages(threadId, callId);')
      && source.includes('finalizeLatestAssistantStepIfIdle(threadId);')
      && source.includes('continue;'),
    'message-event suppression should finalize the current thinking step before continuing'
  );
});

