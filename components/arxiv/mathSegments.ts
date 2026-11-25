/**
 * Source-aware math segmentation for UNTRUSTED arXiv text.
 *
 * The arXiv viewer anchors highlights and selections to byte offsets in the
 * extracted source text, so any renderer that replaces raw text with KaTeX
 * output must be able to map every rendered node back to the exact source
 * span it came from. This tokenizer is the single source of truth for that
 * mapping: it splits a string into `text` and `math` segments that tile the
 * input exactly — `segments[0].start === 0`, each `end === next.start`, and
 * the last `end === text.length` (all JS string indices; UTF-8 byte
 * conversion stays in utf8Offsets.ts).
 *
 * Delimiter rules match MathText exactly:
 * - `$$...$$` is display math when the body is non-empty after trim.
 * - `\(...\)` and `\[...\]` are inline/display math.
 * - Common display environments are rendered as display math.
 * - `$...$` is inline math only when both delimiters touch a non-space
 *   character, so prices ("$5 and $10") stay plain text.
 * - `\$` is a literal dollar and never opens or closes math.
 * - Anything unclosed stays plain text.
 */

export type SourceMathSegment =
  | { kind: 'text'; start: number; end: number }
  | { kind: 'math'; start: number; end: number; src: string; display: boolean };

const DISPLAY_ENV_NAMES = new Set([
  'equation',
  'align',
  'gather',
  'multline',
  'aligned',
  'gathered',
]);

function isNonSpace(ch: string | undefined): boolean {
  return typeof ch === 'string' && ch.length > 0 && !/\s/.test(ch);
}

function findClosingToken(text: string, start: number, token: string): number {
  let j = start;
  while (j < text.length) {
    if (text[j] === '\\') {
      if (text.startsWith(token, j)) return j;
      j += 2;
      continue;
    }
    if (text.startsWith(token, j)) return j;
    j += 1;
  }
  return -1;
}

function beginEnvironmentAt(text: string, index: number): { env: string; end: number } | null {
  const match = /^\\begin\{([A-Za-z*]+)\}/.exec(text.slice(index));
  if (!match) return null;
  return { env: match[1], end: index + match[0].length };
}

function normalizeEnvironmentSource(env: string, body: string, raw: string): string {
  const base = env.replace(/\*$/, '');
  switch (base) {
    case 'equation':
      return body;
    case 'align':
    case 'multline':
      return `\\begin{aligned}${body}\\end{aligned}`;
    case 'gather':
      return `\\begin{gathered}${body}\\end{gathered}`;
    case 'aligned':
    case 'gathered':
      return raw;
    default:
      return raw;
  }
}

function findInlineDollarClose(text: string, start: number): number {
  let j = start;
  while (j < text.length) {
    if (text[j] === '\n' || text[j] === '\r') return -1;
    if (text[j] === '\\') {
      j += 2;
      continue;
    }
    if (text[j] === '$') return j;
    j += 1;
  }
  return -1;
}

export function parseMathSegmentsWithOffsets(text: string): SourceMathSegment[] {
  const segments: SourceMathSegment[] = [];
  // Start of the current pending plain-text run.
  let plainStart = 0;
  const flushPlain = (until: number) => {
    if (until > plainStart) {
      segments.push({ kind: 'text', start: plainStart, end: until });
    }
  };

  let i = 0;
  while (i < text.length) {
    const ch = text[i];
    if (ch === '\\' && text[i + 1] === '$') {
      // Escaped dollar: stays inside the plain run.
      i += 2;
      continue;
    }
    if (ch === '\\' && (text[i + 1] === '(' || text[i + 1] === '[')) {
      const display = text[i + 1] === '[';
      const closeToken = display ? '\\]' : '\\)';
      const close = findClosingToken(text, i + 2, closeToken);
      const src = close !== -1 ? text.slice(i + 2, close) : '';
      if (close !== -1 && src.trim()) {
        flushPlain(i);
        segments.push({ kind: 'math', start: i, end: close + 2, src, display });
        i = close + 2;
        plainStart = i;
        continue;
      }
    }
    if (ch === '\\') {
      const begin = beginEnvironmentAt(text, i);
      const baseEnv = begin?.env.replace(/\*$/, '') || '';
      if (begin && DISPLAY_ENV_NAMES.has(baseEnv)) {
        const closeToken = `\\end{${begin.env}}`;
        const close = findClosingToken(text, begin.end, closeToken);
        const body = close !== -1 ? text.slice(begin.end, close) : '';
        if (close !== -1 && body.trim()) {
          const raw = text.slice(i, close + closeToken.length);
          flushPlain(i);
          segments.push({
            kind: 'math',
            start: i,
            end: close + closeToken.length,
            src: normalizeEnvironmentSource(begin.env, body, raw),
            display: true,
          });
          i = close + closeToken.length;
          plainStart = i;
          continue;
        }
      }
    }
    if (ch === '$') {
      if (text[i + 1] === '$') {
        // Display math $$...$$ — skip escaped \$ while scanning for the closer.
        let j = i + 2;
        let close = -1;
        while (j < text.length) {
          if (text[j] === '\\') {
            j += 2;
            continue;
          }
          if (text[j] === '$' && text[j + 1] === '$') {
            close = j;
            break;
          }
          j += 1;
        }
        const src = close !== -1 ? text.slice(i + 2, close) : '';
        if (close !== -1 && src.trim()) {
          flushPlain(i);
          segments.push({ kind: 'math', start: i, end: close + 2, src, display: true });
          i = close + 2;
          plainStart = i;
          continue;
        }
        i += 2;
        continue;
      }
      // Inline math $...$ — require non-space adjacent to both delimiters.
      if (isNonSpace(text[i + 1])) {
        const close = findInlineDollarClose(text, i + 1);
        if (close > i + 1 && close - i <= 1000 && isNonSpace(text[close - 1])) {
          flushPlain(i);
          segments.push({
            kind: 'math',
            start: i,
            end: close + 1,
            src: text.slice(i + 1, close),
            display: false,
          });
          i = close + 1;
          plainStart = i;
          continue;
        }
      }
      i += 1;
      continue;
    }
    i += 1;
  }
  flushPlain(text.length);
  return segments;
}

/**
 * Plain-text value of a text segment as the user should read it: the raw
 * source slice with escaped dollars unescaped. Only valid for `text` segments.
 */
export function textSegmentValue(text: string, seg: Extract<SourceMathSegment, { kind: 'text' }>): string {
  return text.slice(seg.start, seg.end).replace(/\\\$/g, '$');
}

/** Raw source slice covered by any segment (math delimiters included). */
export function segmentRaw(text: string, seg: SourceMathSegment): string {
  return text.slice(seg.start, seg.end);
}
