export type ParsedRunSSEEvent = {
  name: string;
  payload: any;
};

function tryParseJson(text: string): { ok: true; value: any } | { ok: false } {
  try {
    return { ok: true, value: JSON.parse(text) };
  } catch {
    return { ok: false };
  }
}

function isPlainObject(value: any): value is Record<string, any> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

export function parseRunSseEvent(msg: { event?: string; data: string }): ParsedRunSSEEvent | null {
  const evName = String(msg.event || '').trim();
  const dataText = msg.data ?? '';

  if (dataText.trim() === '[DONE]') return { name: 'done', payload: null };

  const parsed = tryParseJson(dataText);
  if (evName) {
    if (parsed.ok && isPlainObject(parsed.value) && typeof (parsed.value as any).event === 'string') {
      return { name: evName, payload: (parsed.value as any).data };
    }
    return { name: evName, payload: parsed.ok ? parsed.value : dataText };
  }

  if (parsed.ok && isPlainObject(parsed.value) && typeof (parsed.value as any).event === 'string') {
    return { name: String((parsed.value as any).event), payload: (parsed.value as any).data };
  }

  if (parsed.ok && isPlainObject(parsed.value) && typeof (parsed.value as any).type === 'string') {
    return { name: String((parsed.value as any).type), payload: parsed.value };
  }

  return null;
}
