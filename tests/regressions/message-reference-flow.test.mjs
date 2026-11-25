import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

test('ChatInput renders removable composer chips for pinned message references', () => {
  const source = sourceAt('components/ChatInput.tsx');

  assert.ok(
    source.includes('pinnedMessageReferences.map((reference) => {'),
    'ChatInput should render pinned message reference chips'
  );
  assert.ok(
    source.includes('onRemovePinnedMessageReference?.(reference.id)'),
    'ChatInput message reference chips should remove by reference id'
  );
  assert.ok(
    source.includes('<MessageSquarePlus size={12} className="text-sky-400 shrink-0" />'),
    'ChatInput pinned message reference chips should render MessageSquarePlus icon'
  );
  assert.ok(
    source.includes('title={preview || reference.title}'),
    'ChatInput pinned message reference chips should expose preview/content snapshot tooltip'
  );
  assert.ok(
    source.includes('&ldquo;{displayText || reference.label || reference.title}&rdquo;'),
    'ChatInput pinned message reference chips should show quoted preview/label/title text'
  );
});

test('ChatInput renders removable composer chips for pinned thread references', () => {
  const source = sourceAt('components/ChatInput.tsx');

  assert.ok(
    source.includes('pinnedThreadReferences.map((reference) => {'),
    'ChatInput should render pinned thread reference chips'
  );
  assert.ok(
    source.includes('onRemovePinnedThreadReference?.(reference.id)'),
    'ChatInput thread reference chips should remove by thread id'
  );
  assert.ok(
    source.includes('TooltipContent>Reference thread</TooltipContent>'),
    'ChatInput should expose a thread reference picker action'
  );
});

test('buildMessageReferenceContext keeps heading and message-id output token', () => {
  const source = sourceAt('lib/messageReferencePreview.ts');

  assert.ok(
    source.includes("'Referenced Message Blocks'"),
    'message reference context should include stable section heading'
  );
  assert.ok(
    source.includes('ref.messageId ? `Message ID: ${ref.messageId}` :'),
    'message reference context should emit Message ID output token when available'
  );
});

test('App forwards referencedMessageIds derived from pinned reference messageId', () => {
  const source = sourceAt('App.tsx');

  assert.ok(
    source.includes('const targetReferencedMessageIds = targetPinnedMessageReferences.reduce<string[]>((out, reference) => {'),
    'App should derive referenced message ids for run payload'
  );
  assert.ok(
    source.includes("const id = String(reference?.messageId || '').trim();"),
    'App should derive referenced ids from reference.messageId'
  );
  assert.ok(
    source.includes('if (!id || out.includes(id)) return out;'),
    'App should dedupe referenced message ids before sending payload'
  );
  assert.ok(
    source.includes('referencedMessageIds: targetReferencedMessageIds,'),
    'App should forward derived referenced message ids to sendMessage payload'
  );
});

test('App forwards referencedThreadIds and referencedThreads from pinned thread references', () => {
  const source = sourceAt('App.tsx');

  assert.ok(
    source.includes('referencedThreadIds: targetPinnedThreadReferences.map((reference) => reference.id),'),
    'App should forward referenced thread ids to sendMessage payload'
  );
  assert.ok(
    source.includes('referencedThreads: targetPinnedThreadReferences.map((reference) => ({ ...reference })),'),
    'App should forward thread reference snapshots to the optimistic UI message'
  );
  assert.ok(
    source.includes('toast.error(\'You can reference up to 5 threads per message.\');'),
    'App should guard against selecting more than five referenced threads'
  );
});

test('MessageList resolves referenced message chips for all visible message rows', () => {
  const source = sourceAt('components/chat/MessageList.tsx');

  assert.ok(
    source.includes('const referencedMessageReferencesByIndex = React.useMemo(() => {'),
    'MessageList should resolve referenced messages without a user-only index map'
  );
  assert.ok(
    source.includes('referencedMessageReferencesByIndex.get(i) || []'),
    'MessageList should derive referenced messages for every rendered message row'
  );
  assert.ok(
    source.includes('referencedMessages={referencedMessages}') &&
      source.includes('referencedMessages:\n                      referencedMessageReferencesByIndex.get(i) || [],'),
    'MessageList should pass referenced message chips into steer, user, assistant, and intermediate step renderers'
  );
});

test('MessageList resolves referenced thread chips for visible message rows', () => {
  const source = sourceAt('components/chat/MessageList.tsx');

  assert.ok(
    source.includes('const referencedThreadReferencesByIndex = React.useMemo(() => {'),
    'MessageList should resolve referenced thread ids into renderable thread references'
  );
  assert.ok(
    source.includes('threadReferencesById?.[referencedThreadId]'),
    'MessageList should fall back to known thread metadata when the message lacks explicit thread DTOs'
  );
  assert.ok(
    source.includes('referencedThreads={referencedThreads}') &&
      source.includes('referencedThreads:\n                      referencedThreadReferencesByIndex.get(i) || [],'),
    'MessageList should pass referenced thread chips into rendered message rows and intermediate steps'
  );
});

test('Intermediate thinking steps expose message-level reference buttons', () => {
  const source = sourceAt('components/chat/IntermediateThinkingBlock.tsx');

  assert.ok(
    source.includes('buildMessageTextReference('),
    'IntermediateThinkingBlock should build a message-level reference for each visible step'
  );
  assert.ok(
    source.includes('<MessageReferenceButton') &&
      source.includes('onToggle={() => onToggleMessageReference(messageReference)}'),
    'IntermediateThinkingBlock step rows should render a reference button for the assistant step message'
  );
  assert.ok(
    source.includes('<ReferencedMessageChips') &&
      source.includes('references={referencedMessages}'),
    'IntermediateThinkingBlock should display referenced-message chips on step details when present'
  );
});

test('Assistant and steer messages render referenced-message chips', () => {
  const assistantSource = sourceAt('components/chat/AssistantMessage.tsx');
  const steerSource = sourceAt('components/chat/SteerMessage.tsx');

  assert.ok(
    assistantSource.includes('<ReferencedMessageChips') &&
      assistantSource.includes('references={referencedMessages}'),
    'AssistantMessage should display referenced-message chips for assistant outputs'
  );
  assert.ok(
    steerSource.includes('<ReferencedMessageChips') &&
      steerSource.includes('<MessageReferenceButton'),
    'SteerMessage should support both referenced-message chips and message-level reference toggles'
  );
});

test('Chat renderers display referenced-thread chips', () => {
  const userSource = sourceAt('components/chat/UserMessage.tsx');
  const assistantSource = sourceAt('components/chat/AssistantMessage.tsx');
  const steerSource = sourceAt('components/chat/SteerMessage.tsx');
  const thinkingSource = sourceAt('components/chat/IntermediateThinkingBlock.tsx');

  for (const source of [userSource, assistantSource, steerSource, thinkingSource]) {
    assert.ok(
      source.includes('<ReferencedThreadChips'),
      'visible chat renderers should surface referenced-thread chips'
    );
  }
});

test('Thread reference picker scopes suggest search to thread docs', () => {
  const source = sourceAt('components/chat/ThreadReferencePickerDialog.tsx');

  assert.ok(
    source.includes("doc_kind: 'thread'"),
    'thread reference picker should scope search suggestions to thread docs'
  );
  assert.ok(
    source.includes('Reference Other Threads'),
    'thread reference picker should explain the cross-thread grounding workflow'
  );
});

test('shared message reference builder falls back to tool-only assistant steps', () => {
  const source = sourceAt('lib/messageReferences.ts');

  assert.ok(
    source.includes('function buildToolCallSummarySnapshot(message: UIMessage): string {'),
    'messageReferences helper should summarize tool-only assistant rows'
  );
  assert.ok(
    source.includes('if (toolCallSummary) return toolCallSummary;'),
    'messageReferences helper should use tool-call summaries when assistant content is empty'
  );
});

test('useChat assistant_final merges normalized payload.message metadata onto assistant row', () => {
  const source = sourceAt('hooks/useChat.ts');

  assert.ok(
    source.includes('const finalMessage =') && source.includes('normalizeMessage((payload as any).message)'),
    'useChat assistant_final should normalize payload.message'
  );
  assert.ok(
    source.includes('if (!hasFinalText && !finalMessage) {'),
    'useChat assistant_final should only prune empty assistant rows when no final text and no normalized payload message'
  );
  assert.ok(
    source.includes('const messageContent = finalMessage?.content;'),
    'useChat assistant_final should inspect normalized payload.message content for merge behavior'
  );
  assert.ok(
    source.includes('...(finalMessage || {}),') && source.includes('content: resolveFinalAssistantContent(last.content),'),
    'useChat assistant_final should merge normalized payload message metadata onto the assistant row'
  );
  assert.ok(
    source.includes('return typeof fallback === "string" ? fallback : "";'),
    'useChat assistant_final should preserve streamed assistant content fallback when final payload content is empty'
  );
});

test('useChat assistant_final can split final narrative into standalone assistant row', () => {
  const source = sourceAt('hooks/useChat.ts');

  assert.ok(
    source.includes('const resolvedFinalContent = resolveFinalAssistantContent(')
    && source.includes('const shouldSplitStandaloneFinal =')
    && source.includes('hasFinalNarrative &&'),
    'useChat assistant_final should detect when final narrative must be split from tool/thinking turn'
  );
  assert.ok(
    source.includes('const hasExplicitFinalNarrative =')
      && source.includes('const shouldStripLastNarrative =')
      && source.includes('finalizedLastCandidate.content = null;'),
    'useChat assistant_final split path should strip duplicate narrative from the intermediate thinking row'
  );
  assert.ok(
    source.includes('const buildStandaloneFinalAssistant = (')
    && source.includes('fallback: UIMessage["content"]')
    && source.includes('delete (standalone as any).tool_calls;')
    && source.includes('delete (standalone as any).thinking;'),
    'useChat assistant_final standalone message should strip tool/thinking fields'
  );
  assert.ok(
    source.includes('buildStandaloneFinalAssistant(last.content)'),
    'useChat assistant_final should build standalone final rows using streamed fallback content when explicit payload text is absent'
  );
  assert.ok(
    source.includes('const baseMessages = finalizedLast')
    && source.includes('return baseMessages.concat([finalAssistant]);'),
    'useChat assistant_final split path should preserve finalized thinking row then append final assistant row'
  );
  assert.ok(
    source.includes('if (thinkingText.length > 0) {')
      && source.includes('(cleared as any).thinkingComplete = true;'),
    'useChat assistant_final finalization should mark thinking traces complete once the run is finalized'
  );
});
