import React from 'react';
import katex from 'katex';
import {
  parseMathSegmentsWithOffsets,
  segmentRaw,
  textSegmentValue,
} from './mathSegments';

/**
 * Renders UNTRUSTED text (paper titles/abstracts) as plain React text nodes,
 * except for inline ($...$) and display ($$...$$) math segments, which are
 * rendered through KaTeX. KaTeX is invoked with `trust: false`, so its HTML
 * output is safe-by-construction; everything outside math delimiters is
 * emitted as React text and can never become markup.
 *
 * `katex/dist/katex.min.css` is imported globally in index.tsx.
 */

export type MathSegment =
  | { kind: 'text'; value: string }
  | { kind: 'math'; src: string; raw: string; display: boolean };

/**
 * Split on common TeX math delimiters. Single-dollar math requires a non-space
 * character immediately inside both delimiters so prices ("$5 and $10") are
 * not misread as math. Escaped \$ never opens or closes a segment.
 */
export function parseMathSegments(text: string): MathSegment[] {
  return parseMathSegmentsWithOffsets(text).map((segment) => {
    if (segment.kind === 'text') {
      return { kind: 'text', value: textSegmentValue(text, segment) };
    }
    return {
      kind: 'math',
      src: segment.src,
      raw: segmentRaw(text, segment),
      display: segment.display,
    };
  });
}

function KatexNode({
  segment,
  inlineOnly,
}: {
  segment: Extract<MathSegment, { kind: 'math' }>;
  inlineOnly?: boolean;
}) {
  const ref = React.useRef<HTMLSpanElement>(null);
  const [fallback, setFallback] = React.useState(false);

  React.useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    try {
      katex.render(segment.src, el, {
        throwOnError: false,
        trust: false,
        strict: 'ignore',
        output: 'htmlAndMathml',
        displayMode: segment.display && !inlineOnly,
        maxExpand: 256,
        maxSize: 500,
      });
      setFallback(false);
    } catch {
      setFallback(true);
    }
  }, [segment, inlineOnly]);

  if (fallback) {
    return <React.Fragment>{segment.raw}</React.Fragment>;
  }
  return <span ref={ref} />;
}

function renderSegment(seg: MathSegment, key: number, inlineOnly?: boolean): React.ReactNode {
  if (seg.kind === 'text') {
    return <React.Fragment key={key}>{seg.value}</React.Fragment>;
  }
  // KatexNode handles KaTeX failures itself, falling back to the raw segment.
  return <KatexNode key={key} segment={seg} inlineOnly={inlineOnly} />;
}

/**
 * Truncate to `limit` characters (after whitespace normalization) without ever
 * cutting inside a math segment: a math segment that would cross the limit is
 * dropped entirely. Literal dollars in kept plain text are re-escaped so the
 * result re-parses identically through MathText. Plain text behaves exactly
 * like a blind slice-plus-ellipsis.
 */
export function truncateMathText(value: string, limit: number): string {
  const compact = value.replace(/\s+/g, ' ').trim();
  if (compact.length <= limit) return compact;
  const budget = limit - 1;
  let out = '';
  let used = 0;
  for (const seg of parseMathSegments(compact)) {
    if (seg.kind === 'math') {
      if (used + seg.raw.length > budget) break;
      out += seg.raw;
      used += seg.raw.length;
      continue;
    }
    const keep = Math.min(seg.value.length, budget - used);
    out += seg.value.slice(0, keep).replace(/\$/g, '\\$');
    used += keep;
    if (keep < seg.value.length) break;
  }
  return `${out.trim()}...`;
}

export const MathText: React.FC<{ text: string; className?: string; inlineOnly?: boolean }> = ({
  text,
  className,
  inlineOnly,
}) => {
  const rendered = React.useMemo(
    () => parseMathSegments(text).map((seg, idx) => renderSegment(seg, idx, inlineOnly)),
    [text, inlineOnly],
  );
  return <span className={className}>{rendered}</span>;
};

export default MathText;
