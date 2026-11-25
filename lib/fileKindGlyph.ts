import { FileCode, FileImage, FileSpreadsheet, FileText, type LucideIcon } from 'lucide-react';

function titleExtension(title: string): string {
  const clean = String(title || '').trim().toLowerCase();
  const dot = clean.lastIndexOf('.');
  if (dot <= 0 || dot === clean.length - 1) return '';
  return clean.slice(dot + 1);
}

/**
 * Deliberately coarse: enough shape variety to scan a long list (image vs
 * table vs code vs document) without maintaining a per-format icon set.
 * Shared by the file-audit rows and the artifact viewer header so the glyph
 * a user clicks in the list is the glyph the viewer confirms.
 */
export function fileKindGlyph(fileKind?: string, mimeType?: string, title?: string): LucideIcon {
  const kind = (fileKind || '').toLowerCase();
  const mime = (mimeType || '').toLowerCase();
  const ext = titleExtension(title || '');
  if (kind === 'image' || mime.startsWith('image/') || ['png', 'jpg', 'jpeg', 'gif', 'webp'].includes(ext)) {
    return FileImage;
  }
  if (
    kind === 'spreadsheet' || kind === 'csv' || kind === 'tabular' ||
    mime.includes('spreadsheet') || mime.includes('csv') ||
    ['xls', 'xlsx', 'csv', 'tsv', 'ods', 'numbers'].includes(ext)
  ) {
    return FileSpreadsheet;
  }
  if (
    kind === 'code' || kind === 'json' || kind === 'html' ||
    // Exact/suffix XML only: OOXML container MIMEs (docx/pptx,
    // "application/vnd.openxmlformats-...") contain "xml" but are documents,
    // not code — same trap FileRenderer's isTextLikeFile already dodges.
    mime.includes('json') || mime === 'application/xml' || mime.endsWith('+xml') ||
    mime.includes('javascript') || mime.includes('yaml') ||
    ['json', 'jsonl', 'xml', 'svg', 'html', 'htm', 'js', 'jsx', 'ts', 'tsx', 'py', 'go', 'rb', 'rs', 'java', 'css', 'scss', 'sh', 'sql', 'yaml', 'yml', 'toml'].includes(ext)
  ) {
    return FileCode;
  }
  return FileText;
}
