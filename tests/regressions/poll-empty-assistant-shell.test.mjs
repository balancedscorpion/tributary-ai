import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('useChat suppresses empty assistant shells for poll-only query checks', () => {
  const source = sourceAt('hooks/useChat.ts');

  assert.ok(
    source.includes('function sanitizeAssistantMessageForUI(message: UIMessage, suppressedCallIds?: Set<string>): UIMessage | null {')
      && source.includes('if (!hasNarrative && !hasVisibleToolCalls) {')
      && source.includes('return null;'),
    'useChat should drop assistant messages that contain no visible text, thinking, or visible tool calls'
  );

  assert.ok(
    source.includes('if (visibleToolCalls.length === 0) {')
      && source.includes('const sanitizedLast = sanitizeAssistantMessageForUI(last);')
      && source.includes('if (!sanitizedLast) return prev.slice(0, -1);'),
    'tool_calls events with only hidden polling checks should remove empty assistant placeholders'
  );

  assert.ok(
    source.includes("import {\n  isHiddenQueryToolCallName,\n  shouldSuppressQueryToolResult,\n} from '@/lib/queryToolVisibility';"),
    'useChat should consume centralized query tool visibility rules'
  );

  assert.ok(
    source.includes('if (shouldSuppressQueryToolResult({')
      && source.includes('envelopeUiSuppress: envelope?.ui_suppress === true,'),
    'tool-result suppression should route through shared query visibility helper'
  );

  assert.ok(
    source.includes('if (msg.role === Role.Assistant) {')
      && source.includes('const sanitizedAssistant = sanitizeAssistantMessageForUI(msg);')
      && source.includes('if (!sanitizedAssistant) {')
      && source.includes('pruneTrailingEmptyAssistant(threadId);'),
    'message events should skip appending empty assistant shells and prune any trailing placeholder'
  );

  assert.ok(
    source.includes('const finalMessage = rawFinalMessage?.role === Role.Assistant')
      && source.includes('? sanitizeAssistantMessageForUI(rawFinalMessage)')
      && source.includes('const sanitizedMerged = sanitizeAssistantMessageForUI(mergedAssistant);'),
    'assistant_final handling should sanitize and avoid merging empty poll-only assistant payloads'
  );

  assert.ok(
    source.includes('pruneTrailingEmptyAssistant(threadId);') && source.includes('// Refresh thread list'),
    'run teardown should prune any lingering empty assistant shell before ending the stream loop'
  );
});
