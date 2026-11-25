type FenceChar = '`' | '~';

export interface StreamingMarkdownSegmenterOptions {
  /**
   * Bytes retained in the uncertain tail while outside code-fence mode.
   * This allows small reparse windows when new chunks arrive.
   */
  tailReparseBytes?: number;
  /**
   * Maximum bytes retained while waiting for a closing code fence.
   * When exceeded, we degrade to plain tail parsing to avoid unbounded memory.
   */
  maxUnclosedCodeBytes?: number;
  /**
   * Guardrail for pathological never-ending lines.
   */
  maxLineBytes?: number;
}

export interface StreamingMarkdownSnapshot {
  fullText: string;
  stablePrefix: string;
  unstableSuffix: string;
  inCodeBlock: boolean;
  degraded: boolean;
}

const DEFAULT_TAIL_REPARSE_BYTES = 1536;
const DEFAULT_MAX_UNCLOSED_CODE_BYTES = 256 * 1024;
const DEFAULT_MAX_LINE_BYTES = 64 * 1024;

function stripLineEnding(line: string): string {
  if (line.endsWith('\r\n')) return line.slice(0, -2);
  if (line.endsWith('\n')) return line.slice(0, -1);
  if (line.endsWith('\r')) return line.slice(0, -1);
  return line;
}

function parseOpeningFenceLine(line: string): { fenceChar: FenceChar; fenceLen: number } | null {
  const trimmedLine = stripLineEnding(line);
  const match = trimmedLine.match(/^( {0,3})(`{3,}|~{3,})([^\n]*)$/);
  if (!match) return null;

  const fence = match[2];
  const fenceChar = fence[0] as FenceChar;
  const info = (match[3] || '').trim();

  // CommonMark: opening backtick fence info string must not contain backticks.
  if (fenceChar === '`' && info.includes('`')) return null;

  return { fenceChar, fenceLen: fence.length };
}

function isClosingFenceLine(line: string, fenceChar: FenceChar, minFenceLen: number): boolean {
  const trimmedLine = stripLineEnding(line);
  if (!new RegExp(`^ {0,3}\\${fenceChar}{${minFenceLen},}[ \\t]*$`).test(trimmedLine)) {
    return false;
  }
  return true;
}

export class StreamingMarkdownSegmenter {
  private readonly tailReparseBytes: number;

  private readonly maxUnclosedCodeBytes: number;

  private readonly maxLineBytes: number;

  private stablePrefix = '';

  private unstableTail = '';

  private lineBuffer = '';

  private inCodeBlock = false;

  private fenceChar: FenceChar | null = null;

  private fenceLen = 0;

  private degraded = false;

  constructor(opts?: StreamingMarkdownSegmenterOptions) {
    this.tailReparseBytes = Math.max(0, opts?.tailReparseBytes ?? DEFAULT_TAIL_REPARSE_BYTES);
    this.maxUnclosedCodeBytes = Math.max(1024, opts?.maxUnclosedCodeBytes ?? DEFAULT_MAX_UNCLOSED_CODE_BYTES);
    this.maxLineBytes = Math.max(1024, opts?.maxLineBytes ?? DEFAULT_MAX_LINE_BYTES);
  }

  reset(): void {
    this.stablePrefix = '';
    this.unstableTail = '';
    this.lineBuffer = '';
    this.inCodeBlock = false;
    this.fenceChar = null;
    this.fenceLen = 0;
    this.degraded = false;
  }

  push(delta: string): StreamingMarkdownSnapshot {
    if (!delta) return this.snapshot();

    for (let i = 0; i < delta.length; i += 1) {
      const ch = delta[i];
      this.lineBuffer += ch;

      if (this.lineBuffer.length > this.maxLineBytes) {
        // Degrade long unbroken lines into tail text to avoid scanner blowups.
        this.unstableTail += this.lineBuffer;
        this.lineBuffer = '';
        this.degraded = true;
      }

      if (ch !== '\n') continue;

      const completeLine = this.lineBuffer;
      this.lineBuffer = '';
      this.consumeCompleteLine(completeLine);
    }

    this.applyUnclosedFenceGuardrail();
    return this.snapshot();
  }

  snapshot(): StreamingMarkdownSnapshot {
    const unstableSuffix = this.unstableTail + this.lineBuffer;
    return {
      fullText: this.stablePrefix + unstableSuffix,
      stablePrefix: this.stablePrefix,
      unstableSuffix,
      inCodeBlock: this.inCodeBlock,
      degraded: this.degraded,
    };
  }

  private consumeCompleteLine(line: string): void {
    this.unstableTail += line;

    if (!this.inCodeBlock) {
      const opening = parseOpeningFenceLine(line);
      if (opening) {
        this.inCodeBlock = true;
        this.fenceChar = opening.fenceChar;
        this.fenceLen = opening.fenceLen;
      }
    } else if (
      this.fenceChar
      && this.fenceLen > 0
      && isClosingFenceLine(line, this.fenceChar, this.fenceLen)
    ) {
      this.inCodeBlock = false;
      this.fenceChar = null;
      this.fenceLen = 0;
    }

    if (!this.inCodeBlock) {
      this.shiftStablePrefix();
    }
  }

  private shiftStablePrefix(): void {
    if (this.unstableTail.length <= this.tailReparseBytes) return;
    let shiftBy = this.unstableTail.length - this.tailReparseBytes;
    const newlineBeforeShift = this.unstableTail.lastIndexOf('\n', Math.max(0, shiftBy - 1));
    if (newlineBeforeShift < 0) {
      // Avoid cutting at arbitrary positions when no line boundary exists yet.
      return;
    }
    shiftBy = newlineBeforeShift + 1;
    this.stablePrefix += this.unstableTail.slice(0, shiftBy);
    this.unstableTail = this.unstableTail.slice(shiftBy);
  }

  private applyUnclosedFenceGuardrail(): void {
    if (!this.inCodeBlock) return;
    const pendingSize = this.unstableTail.length + this.lineBuffer.length;
    if (pendingSize <= this.maxUnclosedCodeBytes) return;

    // Soft recovery: stop treating the remainder as fenced code to keep memory bounded.
    this.inCodeBlock = false;
    this.fenceChar = null;
    this.fenceLen = 0;
    this.degraded = true;
    this.shiftStablePrefix();
  }
}

export function createStreamingMarkdownSegmenter(
  opts?: StreamingMarkdownSegmenterOptions,
): StreamingMarkdownSegmenter {
  return new StreamingMarkdownSegmenter(opts);
}
