/**
 * The arXiv text API addresses extracted text by UTF-8 byte offsets:
 * `offset`, `next_offset`, and `total_chars` are byte positions/sizes in the
 * stored text file, and the server slices windows on (rune-aligned) byte
 * boundaries. JS strings and DOM selections, however, use UTF-16 code-unit
 * indices. This map converts between the two models for one loaded text
 * window so highlight offsets stay byte-accurate on non-ASCII text.
 *
 * For ASCII text both models coincide (1 byte per code unit), so all
 * conversions are identity there.
 */
export interface Utf8OffsetMap {
  /** Total UTF-8 byte length of the mapped string. */
  byteLength: number;
  /** UTF-8 byte offset of the given JS string index (clamped to the string). */
  jsToByte(jsIndex: number): number;
  /**
   * JS string index for the given UTF-8 byte offset (clamped). Offsets that
   * land inside a multi-byte character snap forward to the next character
   * boundary; exact boundaries map exactly.
   */
  byteToJs(byteOffset: number): number;
}

export function buildUtf8OffsetMap(text: string): Utf8OffsetMap {
  // byteAt[i] = UTF-8 byte offset of JS index i (length text.length + 1).
  const byteAt = new Array<number>(text.length + 1);
  let bytes = 0;
  let i = 0;
  while (i < text.length) {
    byteAt[i] = bytes;
    const cp = text.codePointAt(i) as number;
    if (cp <= 0x7f) bytes += 1;
    else if (cp <= 0x7ff) bytes += 2;
    else if (cp <= 0xffff) bytes += 3;
    else bytes += 4;
    if (cp > 0xffff) {
      // Astral code point: the trailing surrogate has no byte position of its
      // own; give it the character's start so the array stays monotonic and
      // byteToJs (lower bound = first occurrence) can never split the pair.
      byteAt[i + 1] = byteAt[i];
      i += 2;
    } else {
      i += 1;
    }
  }
  byteAt[text.length] = bytes;

  const jsToByte = (jsIndex: number): number => {
    if (jsIndex <= 0) return 0;
    if (jsIndex >= text.length) return bytes;
    return byteAt[jsIndex];
  };

  const byteToJs = (byteOffset: number): number => {
    if (byteOffset <= 0) return 0;
    if (byteOffset >= bytes) return text.length;
    // Lower bound: first JS index whose byte offset is >= byteOffset.
    let lo = 0;
    let hi = text.length;
    while (lo < hi) {
      const mid = (lo + hi) >>> 1;
      if (byteAt[mid] < byteOffset) lo = mid + 1;
      else hi = mid;
    }
    return lo;
  };

  return { byteLength: bytes, jsToByte, byteToJs };
}
