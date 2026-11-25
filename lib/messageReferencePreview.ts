import type { MessageBlockReference } from '@/types';

const MAX_PREVIEW_CHARS = 220;
const MAX_SNAPSHOT_CHARS = 6000;

function normalizeMultiline(value: string): string {
  return String(value || '')
    .replace(/\r\n/g, '\n')
    .replace(/\r/g, '\n')
    .trim();
}

function toOneLine(value: string): string {
  return normalizeMultiline(value).replace(/\s+/g, ' ').trim();
}

function truncateOneLine(value: string, maxChars: number): string {
  if (value.length <= maxChars) return value;
  return `${value.slice(0, Math.max(0, maxChars - 3)).trimEnd()}...`;
}

function truncateSnapshot(value: string, maxChars: number): string {
  if (value.length <= maxChars) return value;
  const omitted = value.length - maxChars;
  return `${value.slice(0, maxChars).trimEnd()}\n... (${omitted} chars omitted)`;
}

function sortDeterministically(refs: MessageBlockReference[]): MessageBlockReference[] {
  return refs.slice().sort((a, b) => {
    const aSeq = Number.isFinite(a.messageSeq) ? Number(a.messageSeq) : Number.MAX_SAFE_INTEGER;
    const bSeq = Number.isFinite(b.messageSeq) ? Number(b.messageSeq) : Number.MAX_SAFE_INTEGER;
    if (aSeq !== bSeq) return aSeq - bSeq;

    const aId = String(a.messageId || '');
    const bId = String(b.messageId || '');
    if (aId !== bId) return aId.localeCompare(bId);

    return String(a.key || a.id).localeCompare(String(b.key || b.id));
  });
}

export function buildMessageReferenceContext(references: MessageBlockReference[]): string {
  const refs = Array.isArray(references)
    ? references.filter((ref) => ref && typeof ref === 'object' && String(ref.id || '').trim())
    : [];
  if (refs.length === 0) return '';

  const sections = sortDeterministically(refs).map((ref, index) => {
    const title = String(ref.label || ref.title || 'Message block').trim() || 'Message block';
    const role = String(ref.role || '').trim() || 'unknown';
    const key = String(ref.key || ref.id).trim() || ref.id;
    const previewSource = String(ref.preview || ref.contentSnapshot || '').trim();
    const preview = truncateOneLine(toOneLine(previewSource), MAX_PREVIEW_CHARS) || '(empty)';
    const snapshotSource = normalizeMultiline(String(ref.contentSnapshot || ref.preview || ''));
    const snapshot = truncateSnapshot(snapshotSource || '(empty)', MAX_SNAPSHOT_CHARS);

    return [
      `[${index + 1}] ${title}`,
      `Reference ID: ${ref.id}`,
      `Block Key: ${key}`,
      `Kind: ${ref.kind}`,
      `Role: ${role}`,
      ref.messageId ? `Message ID: ${ref.messageId}` : '',
      Number.isFinite(ref.messageSeq) ? `Message Seq: ${ref.messageSeq}` : '',
      `Preview: ${preview}`,
      '',
      'Snapshot:',
      '```text',
      snapshot,
      '```',
    ].filter(Boolean).join('\n');
  });

  return ['---', 'Referenced Message Blocks', '---', '', ...sections].join('\n');
}
