import React, { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { Copy, Check } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { highlightCode } from '@/lib/highlighter';
import { normalizeLanguageLabel } from '@/lib/markdownLanguage';

interface CodeBlockProps {
  language: string | undefined;
  children: React.ReactNode;
}

const CodeBlock: React.FC<CodeBlockProps> = ({ language, children }) => {
  const [copied, setCopied] = useState(false);
  const [html, setHtml] = useState<string | null>(null);
  const scrollerRef = useRef<HTMLDivElement | null>(null);
  const scrollStateRef = useRef({ top: 0, left: 0, atBottom: true });
  const pendingHighlightRef = useRef(0);

  const codeText = useMemo(() => {
    if (Array.isArray(children)) return children.join('');
    if (typeof children === 'string') return children;
    return String(children ?? '');
  }, [children]);
  const normalizedLanguage = normalizeLanguageLabel(language) || 'text';

  useEffect(() => {
    const el = scrollerRef.current;
    if (!el) return;

    const update = () => {
      scrollStateRef.current = {
        top: el.scrollTop,
        left: el.scrollLeft,
        atBottom: el.scrollHeight - (el.scrollTop + el.clientHeight) < 8,
      };
    };

    update();
    el.addEventListener('scroll', update, { passive: true });
    return () => el.removeEventListener('scroll', update);
  }, []);

  useEffect(() => {
    setHtml(null);
    const id = ++pendingHighlightRef.current;
    highlightCode(codeText, normalizedLanguage)
      .then((result) => {
        if (pendingHighlightRef.current === id) setHtml(result);
      })
      .catch(() => {
        // Highlighting failure falls back to plain <pre><code>.
      });
  }, [codeText, normalizedLanguage]);

  // Preserve the user's scroll position as streaming updates append content.
  useLayoutEffect(() => {
    const el = scrollerRef.current;
    if (!el) return;
    const { top, left, atBottom } = scrollStateRef.current;
    if (atBottom) el.scrollTop = el.scrollHeight;
    else el.scrollTop = top;
    el.scrollLeft = left;
  }, [codeText, html]);

  const handleCopy = async () => {
    try {
      await navigator.clipboard.writeText(codeText);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch (err) {
      console.error('Failed to copy!', err);
    }
  };

  return (
    <div className="not-prose relative group my-3 w-full max-w-full min-w-0 border border-border rounded-lg overflow-hidden bg-background/50">
      <div className="flex min-w-0 items-center justify-between gap-2 px-3 py-1.5 bg-muted/50 border-b border-border">
        <Badge variant="secondary" className="min-w-0 truncate text-[10px] font-mono">
          {normalizedLanguage || 'code'}
        </Badge>
        <Button
          variant="ghost"
          size="sm"
          className="h-6 shrink-0 px-2 text-[10px] gap-1.5"
          onClick={handleCopy}
        >
          {copied ? <Check size={12} className="text-accent" /> : <Copy size={12} />}
          {copied ? 'Copied!' : 'Copy'}
        </Button>
      </div>
      <div
        ref={scrollerRef}
        className="max-h-[400px] w-full max-w-full min-w-0 overflow-auto"
      >
        {html ? (
          <div
            className="shiki-wrapper chat-text-wrap w-full max-w-full min-w-0 overflow-x-auto text-sm font-mono [&_pre]:m-0 [&_pre]:max-w-full [&_pre]:min-w-0 [&_pre]:p-3 [&_pre]:whitespace-pre-wrap [&_pre]:break-all [&_code]:whitespace-pre-wrap [&_code]:break-all [&_code]:text-sm"
            dangerouslySetInnerHTML={{ __html: html }}
          />
        ) : (
          <pre className="p-3 m-0 w-full max-w-full min-w-0 whitespace-pre-wrap break-all">
            <code className={`language-${normalizedLanguage} chat-text-wrap block font-mono text-sm`}>
              {codeText}
            </code>
          </pre>
        )}
      </div>
    </div>
  );
};

export default CodeBlock;
