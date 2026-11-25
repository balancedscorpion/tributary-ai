import React from 'react';
import { createRoot } from 'react-dom/client';
import '../../../globals.css';
import AssistantMessage from '../../../components/chat/AssistantMessage';
import UserMessage from '../../../components/chat/UserMessage';
import { Role } from '../../../types';

const longLine = 'abcdefghijklmnopqrstuvwxyz0123456789-!@#$%^&*()_+{}[]|:;,.<>?'.repeat(500);
const longWord = 'LongUnbrokenChatText_'.repeat(500);
const markdownTable = [
  '| Identifier | Value |',
  '| --- | --- |',
  `| ${longWord} | ${longWord} |`,
].join('\n');

const codeMessage = {
  role: Role.Assistant,
  content: `\`\`\`sql\n${longLine}\n\`\`\``,
};

const plainAssistantMessage = {
  role: Role.Assistant,
  content: `A long generated token should wrap inside the assistant message: ${longWord}`,
};

const tableAssistantMessage = {
  role: Role.Assistant,
  content: markdownTable,
};

const root = createRoot(document.getElementById('root')!);

root.render(
  <div
    id="fixture-viewport"
    style={{
      width: '100vw',
      maxWidth: '100vw',
      padding: '16px',
      boxSizing: 'border-box',
      overflow: 'hidden',
      background: 'white',
    }}
  >
    <div id="fixture-message" style={{ width: '100%', overflow: 'hidden' }}>
      <section id="assistant-code" data-overflow-check>
        <AssistantMessage
          message={codeMessage as any}
          onOpenArtifact={() => undefined}
        />
      </section>
      <section id="assistant-plain" data-overflow-check>
        <AssistantMessage
          message={plainAssistantMessage as any}
          onOpenArtifact={() => undefined}
        />
      </section>
      <section id="user-plain" data-overflow-check>
        <UserMessage content={`A long user token should wrap inside the user message: ${longWord}`} />
      </section>
      <section id="assistant-table" data-overflow-check>
        <AssistantMessage
          message={tableAssistantMessage as any}
          onOpenArtifact={() => undefined}
        />
      </section>
    </div>
  </div>
);
