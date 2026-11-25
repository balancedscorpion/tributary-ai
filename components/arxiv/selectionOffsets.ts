/**
 * Maps DOM selection points inside the arXiv viewer's rendered text back to
 * JS string indices in the source text.
 *
 * Rendering contract (kept by ArxivPaperViewer):
 * - Rendered source pieces carry `data-s` and `data-e` source indices.
 * - Plain-text pieces usually render the raw source slice as their text node,
 *   so a DOM offset inside that text node maps linearly: `s + offset`.
 * - Escaped-literal text pieces can render shorter than their raw source span;
 *   selection points at their edges snap to the covered source span.
 * - Math pieces carry `data-kind="math"`. Their
 *   DOM content is KaTeX output whose textContent does NOT correspond to the
 *   source (KaTeX duplicates content across MathML and HTML layers), so a
 *   point anywhere inside one snaps outward: to `s` for a range start, to
 *   `e` for a range end. Selections therefore always cover whole formulas.
 *
 * This means selection capture never trusts `range.toString()` or DOM
 * textContent lengths — only source offsets — which is what keeps stored
 * highlight byte offsets exact even though the DOM shows rendered math.
 */

export type SourceBias = 'start' | 'end';

function pieceElementFor(container: HTMLElement, node: Node): HTMLElement | null {
  let el: Element | null = node instanceof Element ? node : node.parentElement;
  while (el && el !== container) {
    if (el instanceof HTMLElement && el.dataset.s !== undefined && el.dataset.e !== undefined) return el;
    el = el.parentElement;
  }
  return null;
}

function firstPieceIn(node: Node | null): HTMLElement | null {
  if (!node || node.nodeType !== Node.ELEMENT_NODE) return null;
  const el = node as HTMLElement;
  if (el.dataset.s !== undefined && el.dataset.e !== undefined) return el;
  return el.querySelector<HTMLElement>('[data-s][data-e]');
}

function lastPieceIn(node: Node | null): HTMLElement | null {
  if (!node || node.nodeType !== Node.ELEMENT_NODE) return null;
  const el = node as HTMLElement;
  const matches = Array.from(el.querySelectorAll<HTMLElement>('[data-s][data-e]'));
  if (el.dataset.s !== undefined && el.dataset.e !== undefined) matches.unshift(el);
  return matches.length ? matches[matches.length - 1] : null;
}

function pieceFromElementBoundary(node: Element, offset: number, bias: SourceBias): HTMLElement | null {
  const children = Array.from(node.childNodes);
  if (bias === 'start') {
    for (let i = Math.max(0, offset); i < children.length; i += 1) {
      const found = firstPieceIn(children[i]);
      if (found) return found;
    }
    for (let i = Math.min(offset - 1, children.length - 1); i >= 0; i -= 1) {
      const found = lastPieceIn(children[i]);
      if (found) return found;
    }
    return null;
  }
  for (let i = Math.min(offset - 1, children.length - 1); i >= 0; i -= 1) {
    const found = lastPieceIn(children[i]);
    if (found) return found;
  }
  for (let i = Math.max(0, offset); i < children.length; i += 1) {
    const found = firstPieceIn(children[i]);
    if (found) return found;
  }
  return null;
}

function pieceStart(el: HTMLElement): number | null {
  const s = Number(el.dataset.s);
  return Number.isInteger(s) && s >= 0 ? s : null;
}

function pieceEnd(el: HTMLElement): number | null {
  const start = pieceStart(el);
  if (start === null) return null;
  const e = Number(el.dataset.e);
  return Number.isInteger(e) && e >= start ? e : null;
}

/**
 * Source index for one DOM point. Returns null when the point cannot be
 * attributed to a rendered piece (caller should abandon the selection).
 */
export function domPointToSourceOffset(
  container: HTMLElement,
  node: Node,
  offset: number,
  bias: SourceBias,
): number | null {
  // Point expressed on the container itself: offset counts child pieces.
  if (node === container) {
    const children = Array.from(container.childNodes);
    if (children.length === 0) return 0;
    if (bias === 'start') {
      for (let i = Math.max(0, offset); i < children.length; i += 1) {
        const found = firstPieceIn(children[i]);
        if (found) return pieceStart(found);
      }
      for (let i = Math.min(offset - 1, children.length - 1); i >= 0; i -= 1) {
        const found = lastPieceIn(children[i]);
        if (found) return pieceEnd(found);
      }
      return null;
    }
    for (let i = Math.min(offset - 1, children.length - 1); i >= 0; i -= 1) {
      const found = lastPieceIn(children[i]);
      if (found) return pieceEnd(found);
    }
    for (let i = Math.max(0, offset); i < children.length; i += 1) {
      const found = firstPieceIn(children[i]);
      if (found) return pieceStart(found);
    }
    return null;
  }

  const piece = pieceElementFor(container, node);
  if (!piece && node.nodeType === Node.ELEMENT_NODE) {
    const boundaryPiece = pieceFromElementBoundary(node as Element, offset, bias);
    if (!boundaryPiece) return null;
    return bias === 'start' ? pieceStart(boundaryPiece) : pieceEnd(boundaryPiece);
  }
  if (!piece) return null;
  const start = pieceStart(piece);
  const end = pieceEnd(piece);
  if (start === null || end === null) return null;

  // Math piece: snap outward so whole formulas are selected.
  if (piece.dataset.kind === 'math') {
    return bias === 'start' ? start : end;
  }

  // Plain piece with the point inside its text node: linear mapping.
  if (node.nodeType === Node.TEXT_NODE) {
    const len = (node as Text).data.length;
    const sourceLength = end - start;
    if (sourceLength === len) {
      return start + Math.max(0, Math.min(offset, len));
    }
    if (offset <= 0) return start;
    if (offset >= len) return end;
    return bias === 'start' ? start : end;
  }
  // Point on the piece element itself: child index 0 = before the text.
  return offset <= 0 ? start : end;
}

/**
 * Maps a DOM Range to a half-open source range. Returns null when either
 * endpoint is unattributable or the result is empty/inverted.
 */
export function sourceRangeFromDomRange(
  container: HTMLElement,
  range: AbstractRange,
): { start: number; end: number } | null {
  const start = domPointToSourceOffset(container, range.startContainer, range.startOffset, 'start');
  const end = domPointToSourceOffset(container, range.endContainer, range.endOffset, 'end');
  if (start === null || end === null) return null;
  if (end <= start) return null;
  return { start, end };
}
