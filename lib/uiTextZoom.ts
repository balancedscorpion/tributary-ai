export const UI_TEXT_ZOOM_STORAGE_KEY = 'ai_analyst_ui_text_zoom_v1';
export const UI_TEXT_ZOOM_MIN = 80;
export const UI_TEXT_ZOOM_MAX = 140;
export const UI_TEXT_ZOOM_STEP = 10;
export const UI_TEXT_ZOOM_DEFAULT = 100;

export type UiTextZoomDirection = -1 | 0 | 1;

export function normalizeUiTextZoom(value: unknown): number {
  if (value === null || value === undefined) return UI_TEXT_ZOOM_DEFAULT;
  if (typeof value === 'string' && !value.trim()) return UI_TEXT_ZOOM_DEFAULT;
  const parsed = typeof value === 'number' ? value : Number(String(value).trim());
  if (!Number.isFinite(parsed)) return UI_TEXT_ZOOM_DEFAULT;
  const stepped = Math.round(parsed / UI_TEXT_ZOOM_STEP) * UI_TEXT_ZOOM_STEP;
  return Math.min(UI_TEXT_ZOOM_MAX, Math.max(UI_TEXT_ZOOM_MIN, stepped));
}

export function readStoredUiTextZoom(): number {
  try {
    return normalizeUiTextZoom(window.localStorage.getItem(UI_TEXT_ZOOM_STORAGE_KEY));
  } catch {
    return UI_TEXT_ZOOM_DEFAULT;
  }
}

export function applyUiTextZoom(percent: number): void {
  const normalized = normalizeUiTextZoom(percent);
  document.documentElement.style.fontSize = `${normalized}%`;
  document.documentElement.dataset.uiTextZoom = String(normalized);
}

export function persistUiTextZoom(percent: number): void {
  try {
    window.localStorage.setItem(UI_TEXT_ZOOM_STORAGE_KEY, String(normalizeUiTextZoom(percent)));
  } catch {
    // Storage is optional; text zoom remains available for this session.
  }
}

export function stepUiTextZoom(percent: number, direction: Exclude<UiTextZoomDirection, 0>): number {
  return normalizeUiTextZoom(percent + direction * UI_TEXT_ZOOM_STEP);
}

export function getUiTextZoomShortcutDirection(
  event: Pick<KeyboardEvent, 'altKey' | 'code' | 'ctrlKey' | 'isComposing' | 'key' | 'metaKey' | 'shiftKey'>,
): UiTextZoomDirection {
  if (
    event.isComposing
    || event.altKey
    || (!event.metaKey && !event.ctrlKey)
  ) {
    return 0;
  }

  if (event.code === 'Equal' || event.key === '+' || event.key === '=') return 1;
  if (event.code === 'Minus' || event.key === '-' || event.key === '_') return -1;
  return 0;
}
