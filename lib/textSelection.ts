// Utilities to make "clickable cards" and auto-scroll behavior play nicely with text selection.
// If the user is selecting text inside an element, we should generally avoid triggering clicks
// that would navigate/toggle and destroy the selection.

export function hasNonCollapsedSelectionWithin(container: HTMLElement | null): boolean {
  if (!container) return false;
  const selection = window.getSelection?.();
  if (!selection || selection.isCollapsed) return false;

  const anchor = selection.anchorNode;
  const focus = selection.focusNode;

  return (!!anchor && container.contains(anchor)) || (!!focus && container.contains(focus));
}

