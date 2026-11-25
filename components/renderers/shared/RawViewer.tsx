import React from 'react';
import SyntaxHighlighter from './SyntaxHighlighter';
import CopyButton from './CopyButton';

interface RawViewerProps {
  content: string;
  language?: string;
  className?: string;
}

function detectLanguage(content: string, hint?: string): string {
  if (hint && hint !== 'text') return hint;
  const trimmed = content.trimStart();
  if (trimmed.startsWith('{') || trimmed.startsWith('[')) return 'json';
  return 'text';
}

const RawViewer: React.FC<RawViewerProps> = ({ content, language, className }) => {
  const lang = detectLanguage(content, language);

  return (
    <div className={`h-full flex flex-col bg-background rounded-lg border border-border overflow-hidden ${className ?? ''}`}>
      <div className="flex items-center justify-between px-3 py-1.5 bg-muted/50 border-b border-border">
        <span className="text-[10px] font-mono text-muted-foreground uppercase">{lang}</span>
        <CopyButton text={content} />
      </div>
      <div className="flex-1 overflow-auto">
        <SyntaxHighlighter code={content} language={lang} />
      </div>
    </div>
  );
};

export default RawViewer;
