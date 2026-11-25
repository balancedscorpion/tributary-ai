import React from 'react';
import MathText from './MathText';
import {
  parseMathSegmentsWithOffsets,
  segmentRaw,
} from './mathSegments';

export const SOURCE_SEGMENT_SELECTOR = '[data-s][data-e]';

interface SourceMathTextProps {
  text: string;
  baseIndex?: number;
  className?: string;
  inlineOnly?: boolean;
  highlighted?: boolean;
}

/**
 * Renders untrusted paper text with KaTeX while keeping source offsets on each
 * rendered segment. Selection code in ArxivPaperViewer uses these attributes
 * instead of DOM textContent, because KaTeX's DOM no longer matches the raw
 * extracted-paper source.
 */
const SourceMathText: React.FC<SourceMathTextProps> = ({
  text,
  baseIndex = 0,
  className,
  inlineOnly,
  highlighted = false,
}) => {
  const rendered = React.useMemo(() => {
    const sourceAttrs = (kind: 'text' | 'math', start: number, end: number) => ({
      'data-s': start,
      'data-e': end,
      'data-kind': kind,
      'data-axv-source-kind': kind,
      'data-axv-source-start': start,
      'data-axv-source-end': end,
    });
    const pieceClass = (kind: 'text' | 'math', displayMath = false) => {
      const classes = ['axv-source-piece'];
      if (kind === 'math') classes.push('axv-source-math');
      if (displayMath) classes.push('axv-source-display-math');
      if (highlighted) classes.push('axv-source-highlight');
      return classes.join(' ');
    };
    const renderTextPieces = (raw: string, absoluteStart: number, index: number) => {
      const pieces: React.ReactNode[] = [];
      let cursor = 0;
      let key = 0;
      for (let match = raw.indexOf('\\$'); match !== -1; match = raw.indexOf('\\$', cursor)) {
        if (match > cursor) {
          const start = absoluteStart + cursor;
          const value = raw.slice(cursor, match);
          pieces.push(
            <span
              key={`${index}:t${key}`}
              className={pieceClass('text')}
              {...sourceAttrs('text', start, start + value.length)}
            >
              {value}
            </span>,
          );
          key += 1;
        }
        const start = absoluteStart + match;
        pieces.push(
          <span
            key={`${index}:d${key}`}
            className={pieceClass('text')}
            {...sourceAttrs('text', start, start + 2)}
          >
            $
          </span>,
        );
        key += 1;
        cursor = match + 2;
      }
      if (cursor < raw.length) {
        const start = absoluteStart + cursor;
        const value = raw.slice(cursor);
        pieces.push(
          <span
            key={`${index}:t${key}`}
            className={pieceClass('text')}
            {...sourceAttrs('text', start, start + value.length)}
          >
            {value}
          </span>,
        );
      }
      return pieces;
    };

    return parseMathSegmentsWithOffsets(text).map((segment, index) => {
      const start = baseIndex + segment.start;
      const end = baseIndex + segment.end;
      if (segment.kind === 'text') {
        return renderTextPieces(segmentRaw(text, segment), start, index);
      }

      return (
        <span
          key={`${start}:${index}`}
          className={pieceClass('math', segment.display && !inlineOnly)}
          {...sourceAttrs('math', start, end)}
        >
          <MathText text={segmentRaw(text, segment)} inlineOnly={inlineOnly} />
        </span>
      );
    });
  }, [baseIndex, highlighted, inlineOnly, text]);

  return <span className={className}>{rendered}</span>;
};

export default SourceMathText;
