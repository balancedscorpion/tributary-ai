import React from 'react';

/**
 * Highlights are anchored to UTF-8 byte offsets in the extracted text — the
 * same offset model the backend text endpoint uses for offset/next_offset —
 * with the quote retained as a fallback. Stored per paper in localStorage so
 * they survive reloads on this device.
 */
export interface ArxivHighlight {
  id: string;
  /** UTF-8 byte offset into the extracted text (inclusive start). */
  start: number;
  /** UTF-8 byte offset into the extracted text (exclusive end). */
  end: number;
  quote: string;
  createdAt: number;
}

// Keep this prefix stable: highlights saved by the original settings-side
// viewer must remain readable by the shared viewer.
const HIGHLIGHT_STORE_PREFIX = 'arxiv_highlights_v1:';

function loadHighlights(paperKey: string): ArxivHighlight[] {
  try {
    const raw = window.localStorage.getItem(HIGHLIGHT_STORE_PREFIX + paperKey);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter(
      (h) => h && typeof h.start === 'number' && typeof h.end === 'number' && typeof h.quote === 'string',
    );
  } catch {
    return [];
  }
}

function saveHighlights(paperKey: string, highlights: ArxivHighlight[]) {
  try {
    if (highlights.length === 0) {
      window.localStorage.removeItem(HIGHLIGHT_STORE_PREFIX + paperKey);
    } else {
      window.localStorage.setItem(HIGHLIGHT_STORE_PREFIX + paperKey, JSON.stringify(highlights));
    }
  } catch {
    // localStorage may be unavailable; highlights degrade to session-only state.
  }
}

/** Remove all stored highlights for a paper (e.g. when it is deleted). */
export function clearArxivHighlights(paperKey: string) {
  saveHighlights(paperKey, []);
}

/**
 * Per-paper highlight store backed by localStorage. Pass null while no paper
 * is open; the hook returns an empty list and ignores mutations.
 */
export function useArxivHighlights(paperKey: string | null): {
  highlights: ArxivHighlight[];
  addHighlight: (highlight: Omit<ArxivHighlight, 'id' | 'createdAt'> & Partial<Pick<ArxivHighlight, 'id' | 'createdAt'>>) => ArxivHighlight | null;
  removeHighlight: (id: string) => void;
} {
  const [highlights, setHighlights] = React.useState<ArxivHighlight[]>(() =>
    paperKey ? loadHighlights(paperKey) : [],
  );

  // Reload when the paper changes. Staleness guard: state writes are keyed to
  // the paperKey captured at effect time, so a fast paper switch cannot leave
  // the previous paper's highlights behind.
  React.useEffect(() => {
    setHighlights(paperKey ? loadHighlights(paperKey) : []);
  }, [paperKey]);

  const addHighlight = React.useCallback(
    (
      highlight: Omit<ArxivHighlight, 'id' | 'createdAt'> & Partial<Pick<ArxivHighlight, 'id' | 'createdAt'>>,
    ): ArxivHighlight | null => {
      if (!paperKey) return null;
      const next: ArxivHighlight = {
        id: highlight.id || `hl_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 7)}`,
        start: highlight.start,
        end: highlight.end,
        quote: highlight.quote,
        createdAt: highlight.createdAt ?? Date.now(),
      };
      setHighlights((prev) => {
        const merged = [...prev, next].sort((a, b) => a.start - b.start);
        saveHighlights(paperKey, merged);
        return merged;
      });
      return next;
    },
    [paperKey],
  );

  const removeHighlight = React.useCallback(
    (id: string) => {
      if (!paperKey) return;
      setHighlights((prev) => {
        const next = prev.filter((h) => h.id !== id);
        saveHighlights(paperKey, next);
        return next;
      });
    },
    [paperKey],
  );

  return { highlights, addHighlight, removeHighlight };
}
