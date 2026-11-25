const MARKDOWN_CODE_SPAN_RE = /(```[\s\S]*?```|~~~[\s\S]*?~~~|`[^`\n]*`)/g;
const INTERNAL_CITATION_HANDLE_RE = /[ \t]*\uE200cite(?:\uE202[^\uE201]*)?\uE201/g;

export function stripInternalCitationHandles(value: string): string {
  if (!value || !value.includes('\uE200cite')) return value;
  const parts = value.split(MARKDOWN_CODE_SPAN_RE);
  return parts.map((part) => {
    if (!part || part.startsWith('```') || part.startsWith('~~~') || part.startsWith('`')) return part;
    return part.replace(INTERNAL_CITATION_HANDLE_RE, '');
  }).join('');
}
