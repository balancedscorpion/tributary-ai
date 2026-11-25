import React, { useEffect, useRef, useState } from 'react';
import { highlightCode } from '@/lib/highlighter';
import { normalizeLanguageLabel } from '@/lib/markdownLanguage';

interface SyntaxHighlighterProps {
  code: string;
  language?: string;
  showLineNumbers?: boolean;
  className?: string;
}

const SyntaxHighlighter: React.FC<SyntaxHighlighterProps> = ({ code, language = 'text', showLineNumbers = false, className }) => {
  const [html, setHtml] = useState<string | null>(null);
  // Track which code+lang pair the current html corresponds to
  const pendingRef = useRef(0);
  const normalizedLanguage = normalizeLanguageLabel(language) || 'text';

  useEffect(() => {
    // Reset html immediately so stale highlighted content from a previous
    // artifact is never shown while the new highlight loads.
    setHtml(null);

    const id = ++pendingRef.current;

    highlightCode(code, normalizedLanguage).then((result) => {
      // Only apply if this is still the latest request
      if (pendingRef.current === id) setHtml(result);
    }).catch(() => {
      // Highlighting failed (unknown lang, WASM issue, etc.) — stay on plain fallback
    });
  }, [code, normalizedLanguage]);

  if (html) {
    return (
      <div
        className={`shiki-wrapper overflow-auto text-xs font-mono [&_pre]:p-4 [&_pre]:m-0 [&_code]:text-xs ${className ?? ''}`}
        dangerouslySetInnerHTML={{ __html: html }}
      />
    );
  }

  // Plain fallback while shiki loads (or if it fails)
  const lines = showLineNumbers ? code.split('\n') : null;
  return (
    <div className={`overflow-auto ${className ?? ''}`}>
      <pre className="p-4 text-xs font-mono text-foreground whitespace-pre w-max min-w-full">
        {lines
          ? lines.map((line, i) => (
              <div key={i} className="flex">
                <span className="text-muted-foreground/40 select-none pr-4 text-right w-10 shrink-0">{i + 1}</span>
                <span>{line}</span>
              </div>
            ))
          : code}
      </pre>
    </div>
  );
};

export default SyntaxHighlighter;
