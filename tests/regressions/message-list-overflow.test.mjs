import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';

function sourceAt(relativePath) {
  return fs.readFileSync(path.join(process.cwd(), relativePath), 'utf8');
}

function expectClass(classSet, token, label) {
  assert.ok(classSet.has(token), `Expected ${label} to include class "${token}". Found: ${[...classSet].join(' ')}`);
}

function firstDivClassSet(source, label) {
  const match = source.match(/<div className="([^"]+)"/);
  if (!match) throw new Error(`${label} has no top-level div className`);
  return new Set(match[1].split(/\s+/).filter(Boolean));
}

test('MessageList content and scroll area constrain width', () => {
  const source = sourceAt('components/chat/MessageList.tsx');
  const scrollAreaMatch = source.match(/<ScrollArea[\s\S]*?className="([^"]+)"/);
  const viewportClassMatch = source.match(/viewportClassName="([^"]+)"/);
  const contentMatch = source.match(/<div className="([^"]+)"/);
  if (!scrollAreaMatch) throw new Error('MessageList ScrollArea className not found');
  if (!viewportClassMatch) throw new Error('MessageList viewport className not found');
  if (!contentMatch) throw new Error('MessageList inner content div className not found');

  const scrollAreaClassSet = new Set(scrollAreaMatch[1].split(/\s+/).filter(Boolean));
  const viewportClassSet = new Set(viewportClassMatch[1].split(/\s+/).filter(Boolean));
  const contentClassSet = new Set(contentMatch[1].split(/\s+/).filter(Boolean));

  expectClass(scrollAreaClassSet, 'w-full', 'MessageList ScrollArea');
  expectClass(scrollAreaClassSet, 'min-w-0', 'MessageList ScrollArea');
  expectClass(scrollAreaClassSet, 'max-w-full', 'MessageList ScrollArea');
  expectClass(viewportClassSet, 'overflow-x-hidden', 'MessageList viewport');
  expectClass(viewportClassSet, 'overscroll-contain', 'MessageList viewport');
  expectClass(contentClassSet, 'overflow-x-hidden', 'MessageList content wrapper');
  expectClass(contentClassSet, 'max-w-full', 'MessageList content wrapper');
  expectClass(contentClassSet, 'min-w-0', 'MessageList content wrapper');
  assert.ok(
    source.includes("style={{ display: 'flex', flexDirection: 'column', gap: '6px' }}"),
    'MessageList content wrapper should keep stable vertical spacing'
  );
  assert.equal(
    source.includes('bottomRef.current?.scrollIntoView'),
    false,
    'MessageList auto-scroll must target only the transcript viewport'
  );
  assert.ok(
    source.includes('viewportEl.scrollTo({'),
    'MessageList auto-scroll should move the transcript viewport directly'
  );
});

test('MarkdownRenderer table cells wrap instead of forcing horizontal overflow', () => {
  const source = sourceAt('components/chat/MarkdownRenderer.tsx');
  const tdMatch = source.match(/td: \(\{ children \}\) => <td className="([^"]+)"/);
  const thMatch = source.match(/th: \(\{ children \}\) => <th className="([^"]+)"/);
  if (!tdMatch) throw new Error('MarkdownRenderer td className missing');
  if (!thMatch) throw new Error('MarkdownRenderer th className missing');

  const tdClasses = new Set(tdMatch[1].split(/\s+/).filter(Boolean));
  const thClasses = new Set(thMatch[1].split(/\s+/).filter(Boolean));
  expectClass(tdClasses, 'whitespace-normal', 'MarkdownRenderer td');
  expectClass(tdClasses, '[overflow-wrap:anywhere]', 'MarkdownRenderer td');
  expectClass(thClasses, 'whitespace-normal', 'MarkdownRenderer th');
  expectClass(thClasses, '[overflow-wrap:anywhere]', 'MarkdownRenderer th');
  assert.equal(tdClasses.has('whitespace-nowrap'), false, 'MarkdownRenderer td should not force no-wrap table cells');
});

test('message rows in flex layouts are shrinkable', () => {
  const assistantRootClassSet = firstDivClassSet(sourceAt('components/chat/AssistantMessage.tsx'), 'AssistantMessage');
  const userRootClassSet = firstDivClassSet(sourceAt('components/chat/UserMessage.tsx'), 'UserMessage');
  const steerRootClassSet = firstDivClassSet(sourceAt('components/chat/SteerMessage.tsx'), 'SteerMessage');

  expectClass(assistantRootClassSet, 'min-w-0', 'AssistantMessage root');
  expectClass(userRootClassSet, 'min-w-0', 'UserMessage root');
  expectClass(steerRootClassSet, 'min-w-0', 'SteerMessage root');
});

test('tool result and code blocks constrain intrinsic width', () => {
  const toolResultSource = sourceAt('components/chat/ToolResultBlock.tsx');
  const toolResultMatches = [...toolResultSource.matchAll(/<div className="([^"]+)"/g)];
  const toolResultRoots = toolResultMatches.filter(([, classes]) => classes.includes('animate-fade-in') && classes.includes('max-w-[85%]') && classes.includes('pl-12'));
  assert.equal(toolResultRoots.length, 3, `Expected 3 ToolResultBlock root wrappers, found ${toolResultRoots.length}`);

  const first = toolResultRoots.every(([, classes]) => new Set(classes.split(/\s+/).filter(Boolean)).has('min-w-0'));
  assert.ok(first, 'ToolResultBlock root wrappers should include min-w-0');

  const codeSource = sourceAt('components/chat/CodeBlock.tsx');
  const codePre = codeSource.match(/<pre className="([^"]+)"/);
  if (!codePre) throw new Error('CodeBlock pre className missing');
  const preClasses = new Set(codePre[1].split(/\s+/).filter(Boolean));
  expectClass(preClasses, 'w-full', 'CodeBlock pre');
});

test('MarkdownRenderer code renderer uses inline code wrapping', () => {
  const assistantSource = sourceAt('components/chat/MarkdownRenderer.tsx');
  assert.ok(
    assistantSource.includes('code({ inline, className, children, ...props }: any)'),
    'MarkdownRenderer should include inline param in markdown code renderer'
  );

  const inlineCodeClass = 'bg-muted px-1.5 py-0.5 rounded text-orange-300 font-mono text-sm border border-border break-all whitespace-pre-wrap';
  assert.ok(
    assistantSource.includes(inlineCodeClass),
    'AssistantMessage inline markdown code className not found'
  );
  const inlineCodeClasses = new Set(inlineCodeClass.split(/\s+/).filter(Boolean));
  expectClass(inlineCodeClasses, 'break-all', 'Inline markdown code');
  expectClass(inlineCodeClasses, 'whitespace-pre-wrap', 'Inline markdown code');
});

test('MarkdownRenderer markdown code renderer preserves inline backticks', () => {
  const assistantSource = sourceAt('components/chat/MarkdownRenderer.tsx');
  assert.ok(
    assistantSource.includes('typeof inline === \'boolean\'') &&
    assistantSource.includes('? inline') &&
    assistantSource.includes(': !language && !content.includes(\'\\\\n\')'),
    'Inline-code detection should use explicit inline flag with multiline fallback'
  );
  assert.equal(
    assistantSource.includes('const isInline = Boolean(inline);'),
    false,
    'Should not use old bool-coercion fallback that misclassifies inline code as block'
  );
});
