import React, { useEffect, useMemo, useState } from 'react';
import {
  AlertTriangle,
  Download,
  Eye,
  FileArchive,
  FileCode,
  FileImage,
  FileJson,
  FileQuestion,
  FileSpreadsheet,
  FileText,
  Globe2,
  Loader2,
  Presentation,
  RotateCcw,
  X,
} from 'lucide-react';
import { toast } from 'sonner';
import { Card } from '@/components/ui/card';
import { artifactRawUrl, downloadArtifactRawBytes } from '@/lib/artifactDownload';
import { formatBytes } from '@/lib/formatters';
import { stripInternalCitationHandles } from '@/lib/markdownSanitizers';
import { cn } from '@/lib/utils';
import { SANDBOXED_HTML_SANDBOX_POLICY } from '@/types';
import MarkdownRenderer from './MarkdownRenderer';

interface FileAttachmentCardProps {
  title: string;
  mimeType?: string;
  fileKind?: string;
  sizeBytes?: number;
  previewText?: string;
  contentNote?: string;
  previewAvailable?: boolean;
  previewTruncated?: boolean;
  metadata?: Record<string, any>;
  /** Upload lifecycle; absent means uploaded (persisted attachments). */
  status?: 'uploading' | 'uploaded' | 'error';
  /** Upload progress in [0, 1] while status is "uploading". */
  progress?: number;
  errorMessage?: string;
  /** Image preview source, typically an object URL while upload is pending. */
  thumbnailUrl?: string;
  /** Same-origin API URL fetched through authenticated fetch before rendering. */
  thumbnailFetchUrl?: string;
  /** Raw artifact id to fetch through authenticated fetch before rendering. */
  thumbnailArtifactId?: string;
  /** Same-origin raw PDF URL to render inline instead of extracted PDF text. */
  pdfPreviewUrl?: string;
  /** Same-origin raw HTML URL fetched and rendered in a sandboxed iframe. */
  htmlPreviewUrl?: string;
  /** Inline HTML content for generated/file-like HTML artifacts. */
  htmlPreviewContent?: string;
  /** Same-origin extracted text/markdown preview URL, used for sheet previews. */
  textPreviewFetchUrl?: string;
  /**
   * compact: dense composer chip, still with bounded preview.
   * full: transcript/tool card with the same anatomy and a taller body.
   */
  variant?: 'compact' | 'full';
  /**
   * Artifact whose original bytes are served by /api/artifacts/{id}/raw.
   * Downloads fetch bytes and save them locally; never navigate to the raw URL.
   */
  downloadArtifactId?: string;
  downloadFileName?: string;
  onDownload?: () => void | Promise<void>;
  onRetry?: () => void;
  onClick?: () => void;
  onRemove?: () => void;
  className?: string;
}

const MATH_PREVIEW_RE = /(\$\$[\s\S]+?\$\$|\$[^$\n]{1,240}\$|\\\(|\\\[|\\begin\{(?:equation|align|gather|multline)\*?\})/;
const CARD_TEXT_PREVIEW_CHAR_LIMIT = 6000;
const TABLE_PREVIEW_ROWS_FULL = 6;
const TABLE_PREVIEW_ROWS_COMPACT = 4;
const TABLE_PREVIEW_COLUMNS = 6;
const CODE_PREVIEW_LINES_FULL = 10;
const CODE_PREVIEW_LINES_COMPACT = 6;

type CardKind =
  | 'pdf'
  | 'document'
  | 'spreadsheet'
  | 'presentation'
  | 'html'
  | 'code'
  | 'data'
  | 'image'
  | 'archive'
  | 'text'
  | 'unknown';

interface KindConfig {
  label: string;
  noun: string;
  action: string;
  fallback: string;
  Icon: React.ComponentType<{ size?: number | string; className?: string }>;
  tileClass: string;
}

const KIND_CONFIG: Record<CardKind, KindConfig> = {
  pdf: {
    label: 'PDF',
    noun: 'PDF',
    action: 'View PDF',
    fallback: 'Preview unavailable. The PDF may be encrypted, scanned, or too large for inline preview.',
    Icon: FileText,
    tileClass: 'bg-red-500/10 text-red-400',
  },
  document: {
    label: 'Document',
    noun: 'document',
    action: 'Read document',
    fallback: 'Preview unavailable. The document may be protected or require its native editor.',
    Icon: FileText,
    tileClass: 'bg-sky-500/10 text-sky-400',
  },
  spreadsheet: {
    label: 'Spreadsheet',
    noun: 'sheet',
    action: 'Review sheet',
    fallback: 'Preview unavailable. The workbook may be protected or exceed preview limits.',
    Icon: FileSpreadsheet,
    tileClass: 'bg-emerald-500/10 text-emerald-400',
  },
  presentation: {
    label: 'Presentation',
    noun: 'slides',
    action: 'View slides',
    fallback: 'Preview unavailable. The presentation may be protected or need its native renderer.',
    Icon: Presentation,
    tileClass: 'bg-orange-500/10 text-orange-400',
  },
  html: {
    label: 'HTML',
    noun: 'HTML',
    action: 'Preview HTML',
    fallback: 'Preview unavailable. The HTML file could not be rendered safely.',
    Icon: Globe2,
    tileClass: 'bg-cyan-500/10 text-cyan-400',
  },
  code: {
    label: 'Code',
    noun: 'source',
    action: 'View source',
    fallback: 'Preview unavailable. The source file could not be decoded safely.',
    Icon: FileCode,
    tileClass: 'bg-zinc-500/10 text-zinc-300',
  },
  data: {
    label: 'Data',
    noun: 'data',
    action: 'Inspect data',
    fallback: 'Preview unavailable. The structured data could not be decoded safely.',
    Icon: FileJson,
    tileClass: 'bg-violet-500/10 text-violet-400',
  },
  image: {
    label: 'Image',
    noun: 'image',
    action: 'Preview image',
    fallback: 'Preview unavailable. The image may be unsupported or failed to load.',
    Icon: FileImage,
    tileClass: 'bg-fuchsia-500/10 text-fuchsia-400',
  },
  archive: {
    label: 'Archive',
    noun: 'archive',
    action: 'Inspect archive',
    fallback: 'Preview unavailable. The archive inventory could not be read safely.',
    Icon: FileArchive,
    tileClass: 'bg-amber-500/10 text-amber-400',
  },
  text: {
    label: 'Text',
    noun: 'text',
    action: 'Read text',
    fallback: 'Preview unavailable. The text could not be decoded safely.',
    Icon: FileText,
    tileClass: 'bg-stone-500/10 text-stone-300',
  },
  unknown: {
    label: 'File',
    noun: 'file',
    action: 'Inspect file',
    fallback: 'Preview unavailable. This file type is not supported for inline preview.',
    Icon: FileQuestion,
    tileClass: 'bg-muted text-muted-foreground',
  },
};

const CODE_EXTENSIONS = new Set([
  'py', 'go', 'js', 'jsx', 'ts', 'tsx', 'rb', 'rs', 'java', 'kt', 'swift', 'c', 'h',
  'cpp', 'hpp', 'cs', 'php', 'sh', 'bash', 'zsh', 'sql', 'css', 'scss', 'less',
  'xml', 'svg', 'yaml', 'yml', 'toml', 'ini', 'cfg', 'conf',
  'env', 'properties', 'lock', 'proto', 'graphql', 'gql', 'vue', 'svelte',
]);

const MIME_LABELS: Record<string, string> = {
  'application/pdf': 'PDF',
  'text/plain': 'Text',
  'text/markdown': 'Markdown',
  'text/html': 'HTML',
  'text/csv': 'CSV',
  'application/csv': 'CSV',
  'text/tab-separated-values': 'TSV',
  'application/json': 'JSON',
  'application/x-ndjson': 'JSONL',
  'application/jsonl': 'JSONL',
  'application/msword': 'DOC',
  'application/rtf': 'RTF',
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document': 'DOCX',
  'application/vnd.oasis.opendocument.text': 'ODT',
  'application/vnd.apple.pages': 'PAGES',
  'application/x-iwork-pages-sffpages': 'PAGES',
  'application/vnd.ms-excel': 'XLS',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet': 'XLSX',
  'application/vnd.oasis.opendocument.spreadsheet': 'ODS',
  'application/vnd.apple.numbers': 'NUMBERS',
  'application/x-iwork-numbers-sffnumbers': 'NUMBERS',
  'application/vnd.ms-powerpoint': 'PPT',
  'application/vnd.openxmlformats-officedocument.presentationml.presentation': 'PPTX',
  'application/vnd.oasis.opendocument.presentation': 'ODP',
  'application/vnd.apple.keynote': 'KEY',
  'application/x-iwork-keynote-sffkey': 'KEY',
  'application/zip': 'ZIP',
  'application/x-zip-compressed': 'ZIP',
  'application/x-tar': 'TAR',
  'application/gzip': 'GZIP',
  'application/x-gzip': 'GZIP',
  'application/x-7z-compressed': '7Z',
  'application/vnd.rar': 'RAR',
  'application/x-rar-compressed': 'RAR',
  'application/x-bzip2': 'BZIP2',
  'application/x-xz': 'XZ',
  'application/octet-stream': 'Binary',
};

interface TablePreview {
  headers: string[];
  rows: string[][];
  shownRows: number;
}

interface JsonTreePreview {
  rootLabel: string;
  entries: Array<{ key: string; value: string; depth: number }>;
  keyCount: number;
}

interface ArchivePreview {
  entries: Array<{ name: string; detail?: string }>;
  shownEntries: number;
}

type FooterActionKind = 'open' | 'download' | 'retry' | 'remove';

interface FooterAction {
  kind: FooterActionKind;
  label: string;
  Icon?: React.ComponentType<{ size?: number | string; className?: string }>;
}

function fileExtension(title: string): string {
  const clean = String(title || '').trim().toLowerCase();
  const dot = clean.lastIndexOf('.');
  if (dot <= 0 || dot === clean.length - 1) return '';
  return clean.slice(dot + 1);
}

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return '';
}

function firstNumber(...values: unknown[]): number | undefined {
  for (const value of values) {
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value === 'string' && value.trim()) {
      const parsed = Number(value.trim());
      if (Number.isFinite(parsed)) return parsed;
    }
  }
  return undefined;
}

function asRecord(value: unknown): Record<string, any> {
  return value && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, any>
    : {};
}

function extractionRecord(metadata?: Record<string, any>): Record<string, any> {
  return asRecord(metadata?.extraction);
}

function metadataNumber(metadata: Record<string, any> | undefined, ...keys: string[]): number | undefined {
  const extraction = extractionRecord(metadata);
  const values: unknown[] = [];
  for (const key of keys) {
    values.push(metadata?.[key], extraction[key]);
  }
  return firstNumber(...values);
}

function metadataString(metadata: Record<string, any> | undefined, ...keys: string[]): string {
  const extraction = extractionRecord(metadata);
  const values: unknown[] = [];
  for (const key of keys) {
    values.push(metadata?.[key], extraction[key]);
  }
  return firstString(...values);
}

function formatCount(value: number): string {
  return Math.trunc(value).toLocaleString();
}

function plural(value: number, singular: string, pluralLabel = `${singular}s`): string {
  return `${formatCount(value)} ${Math.trunc(value) === 1 ? singular : pluralLabel}`;
}

function lineCount(text: string): number {
  if (!text) return 0;
  return text.split(/\r\n|\r|\n/).length;
}

function resolveCardKind(fileKind?: string, mimeType?: string, title?: string): CardKind {
  const kind = String(fileKind || '').trim().toLowerCase();
  const ext = fileExtension(title || '');
  const name = String(title || '').trim().toLowerCase().split(/[\\/]/).pop() || '';
  const mime = String(mimeType || '').trim().toLowerCase();

  if (
    kind === 'html' ||
    kind === 'web' ||
    kind === 'webdoc' ||
    kind === 'web_document' ||
    mime === 'text/html' ||
    mime === 'application/xhtml+xml' ||
    ext === 'html' ||
    ext === 'htm'
  ) {
    return 'html';
  }

  if (CODE_EXTENSIONS.has(ext) || ['dockerfile', 'makefile', 'rakefile', 'gemfile', 'procfile'].includes(name) || name.startsWith('.env')) {
    return 'code';
  }

  switch (kind) {
    case 'pdf': return 'pdf';
    case 'document': return 'document';
    case 'spreadsheet':
    case 'csv':
    case 'tabular':
      return 'spreadsheet';
    case 'presentation': return 'presentation';
    case 'image': return 'image';
    case 'archive': return 'archive';
    case 'json': return 'data';
    case 'data': return 'data';
    case 'config':
    case 'xml':
    case 'code':
      return 'code';
    case 'text':
    case 'markdown':
      return 'text';
  }

  if (mime === 'application/pdf') return 'pdf';
  if (mime.startsWith('image/')) return 'image';
  if (
    mime.includes('wordprocessingml') ||
    mime === 'application/msword' ||
    mime === 'application/rtf' ||
    mime === 'text/rtf' ||
    mime === 'application/vnd.oasis.opendocument.text' ||
    mime.includes('pages')
  ) return 'document';
  if (
    mime.includes('spreadsheetml') ||
    mime === 'application/vnd.ms-excel' ||
    mime === 'application/vnd.oasis.opendocument.spreadsheet' ||
    mime.includes('numbers') ||
    mime.includes('csv') ||
    mime === 'text/tab-separated-values'
  ) return 'spreadsheet';
  if (
    mime.includes('presentationml') ||
    mime === 'application/vnd.ms-powerpoint' ||
    mime === 'application/vnd.oasis.opendocument.presentation' ||
    mime.includes('keynote')
  ) return 'presentation';
  if (
    mime.includes('zip') ||
    mime.includes('tar') ||
    mime.includes('gzip') ||
    mime.includes('x-7z') ||
    mime.includes('rar') ||
    mime.includes('bzip') ||
    mime.includes('x-xz')
  ) return 'archive';
  if (mime.includes('json') || mime === 'application/x-ndjson') return 'data';
  if (
    mime.includes('xml') ||
    mime === 'application/javascript' ||
    mime === 'application/x-javascript' ||
    mime === 'text/javascript' ||
    mime === 'application/typescript' ||
    mime === 'application/x-typescript' ||
    mime === 'text/typescript' ||
    mime === 'application/x-sh' ||
    mime === 'application/x-yaml' ||
    mime === 'application/yaml' ||
    mime === 'application/toml'
  ) return 'code';

  if (ext === 'pdf') return 'pdf';
  if (['doc', 'docx', 'rtf', 'odt', 'pages'].includes(ext)) return 'document';
  if (['xls', 'xlsx', 'csv', 'tsv', 'tab', 'ods', 'numbers'].includes(ext)) return 'spreadsheet';
  if (['ppt', 'pptx', 'key', 'odp'].includes(ext)) return 'presentation';
  if (['png', 'jpg', 'jpeg', 'gif', 'webp'].includes(ext)) return 'image';
  if (['zip', 'tar', 'gz', 'gzip', 'tgz', '7z', 'rar', 'bz2', 'tbz', 'tbz2', 'xz', 'txz'].includes(ext)) return 'archive';
  if (ext === 'html' || ext === 'htm') return 'html';
  if (ext === 'json' || ext === 'jsonl' || ext === 'ndjson') return 'data';
  if (ext === 'md' || ext === 'markdown' || ext === 'txt' || ext === 'log') return 'text';
  if (mime.startsWith('text/')) return 'text';
  return 'unknown';
}

function formatMimeLabel(mimeType?: string, title?: string, kind?: CardKind): string {
  const raw = String(mimeType || '').trim().toLowerCase();
  if (raw && MIME_LABELS[raw]) return MIME_LABELS[raw];
  if (raw.startsWith('image/')) return raw.slice('image/'.length).toUpperCase();
  const ext = fileExtension(title || '');
  if (ext && ext.length <= 5) return ext.toUpperCase();
  return kind ? KIND_CONFIG[kind].label : raw;
}

function shouldRenderRichPreview(previewText: string, mimeType?: string): boolean {
  const mime = String(mimeType || '').toLowerCase();
  return mime === 'text/markdown' || MATH_PREVIEW_RE.test(previewText);
}

function isLowValuePreviewText(text: string): boolean {
  const clean = text.trim().toLowerCase();
  if (!clean) return true;
  return (
    clean === '(empty)' ||
    clean === 'binary preview omitted.' ||
    clean.includes('binary or unsupported text preview omitted') ||
    clean.includes('binary preview omitted') ||
    clean.includes('unsupported text preview omitted') ||
    clean.includes('preview omitted. file type was evaluated')
  );
}

function clippedPreviewText(text: string): { text: string; clipped: boolean } {
  if (text.length <= CARD_TEXT_PREVIEW_CHAR_LIMIT) return { text, clipped: false };
  return {
    text: `${text.slice(0, CARD_TEXT_PREVIEW_CHAR_LIMIT).trimEnd()}\n...`,
    clipped: true,
  };
}

function splitDelimitedLine(line: string, delimiter: string): string[] {
  if (delimiter !== ',') return line.split(delimiter).map((value) => value.trim());
  const values: string[] = [];
  let current = '';
  let quoted = false;
  for (let i = 0; i < line.length; i += 1) {
    const ch = line[i];
    if (ch === '"') {
      if (quoted && line[i + 1] === '"') {
        current += '"';
        i += 1;
      } else {
        quoted = !quoted;
      }
      continue;
    }
    if (ch === delimiter && !quoted) {
      values.push(current.trim());
      current = '';
      continue;
    }
    current += ch;
  }
  values.push(current.trim());
  return values;
}

function parseMarkdownRow(line: string): string[] {
  let clean = line.trim();
  if (clean.startsWith('|')) clean = clean.slice(1);
  if (clean.endsWith('|')) clean = clean.slice(0, -1);
  return clean.split('|').map((cell) => cell.trim().replace(/\\\|/g, '|'));
}

function parseMarkdownTable(text: string, rowLimit: number): TablePreview | null {
  const lines = text.split(/\r\n|\r|\n/);
  for (let index = 0; index < lines.length - 1; index += 1) {
    const headerLine = lines[index];
    const separatorLine = lines[index + 1];
    if (!headerLine.includes('|') || !/^\s*\|?\s*:?-{3,}/.test(separatorLine)) continue;
    const headers = parseMarkdownRow(headerLine).filter(Boolean).slice(0, TABLE_PREVIEW_COLUMNS);
    if (headers.length === 0) continue;
    const rows: string[][] = [];
    for (let rowIndex = index + 2; rowIndex < lines.length && rows.length < rowLimit; rowIndex += 1) {
      const line = lines[rowIndex];
      if (!line.includes('|') || !line.trim()) break;
      rows.push(parseMarkdownRow(line).slice(0, headers.length));
    }
    if (rows.length > 0) {
      return { headers, rows, shownRows: rows.length };
    }
  }
  return null;
}

function parseDelimitedTable(text: string, title: string, mimeType: string | undefined, rowLimit: number): TablePreview | null {
  const ext = fileExtension(title);
  const mime = String(mimeType || '').toLowerCase();
  const delimiter = ext === 'tsv' || ext === 'tab' || mime === 'text/tab-separated-values'
    ? '\t'
    : ',';
  const lines = text.split(/\r\n|\r|\n/).filter((line) => line.trim()).slice(0, rowLimit + 1);
  if (lines.length < 2) return null;
  const parsed = lines.map((line) => splitDelimitedLine(line, delimiter));
  const maxCols = Math.max(...parsed.map((row) => row.length));
  if (maxCols < 2) return null;
  const headers = parsed[0].slice(0, TABLE_PREVIEW_COLUMNS).map((header, index) => header || `Column ${index + 1}`);
  const rows = parsed.slice(1, rowLimit + 1).map((row) => row.slice(0, headers.length));
  return { headers, rows, shownRows: rows.length };
}

function buildTablePreview(text: string, title: string, mimeType: string | undefined, rowLimit: number): TablePreview | null {
  if (!text) return null;
  return parseMarkdownTable(text, rowLimit) || parseDelimitedTable(text, title, mimeType, rowLimit);
}

function compactValue(value: unknown, maxLength = 60): string {
  if (value === null) return 'null';
  if (value === undefined) return '';
  if (Array.isArray(value)) return `Array(${value.length})`;
  if (typeof value === 'object') return `Object(${Object.keys(value as Record<string, unknown>).length})`;
  const text = String(value).replace(/\s+/g, ' ').trim();
  return text.length > maxLength ? `${text.slice(0, maxLength - 3)}...` : text;
}

function collectJsonEntries(value: unknown, depth = 0, limit = 12): JsonTreePreview['entries'] {
  if (!value || typeof value !== 'object' || limit <= 0) return [];
  const object = Array.isArray(value)
    ? Object.fromEntries(value.slice(0, 6).map((item, index) => [String(index), item]))
    : value as Record<string, unknown>;
  const entries: JsonTreePreview['entries'] = [];
  for (const [key, child] of Object.entries(object)) {
    entries.push({ key, value: compactValue(child), depth });
    if (entries.length >= limit) break;
    if (child && typeof child === 'object' && depth < 1) {
      const nested = collectJsonEntries(child, depth + 1, limit - entries.length);
      entries.push(...nested);
    }
    if (entries.length >= limit) break;
  }
  return entries;
}

function buildJsonTreePreview(text: string): JsonTreePreview | null {
  if (!text.trim()) return null;
  const trimmed = text.trim();
  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    const lines = trimmed.split(/\r\n|\r|\n/).filter(Boolean).slice(0, 5);
    const objects: unknown[] = [];
    for (const line of lines) {
      try {
        objects.push(JSON.parse(line));
      } catch {
        return null;
      }
    }
    if (objects.length === 0) return null;
    parsed = objects;
  }
  const rootLabel = Array.isArray(parsed)
    ? `Array(${parsed.length})`
    : parsed && typeof parsed === 'object'
      ? `Object(${Object.keys(parsed as Record<string, unknown>).length})`
      : typeof parsed;
  const entries = collectJsonEntries(parsed);
  return { rootLabel, entries, keyCount: entries.length };
}

function buildArchivePreview(text: string, rowLimit: number): ArchivePreview | null {
  const table = parseMarkdownTable(text, rowLimit);
  if (table) {
    const pathIndex = table.headers.findIndex((header) => /path|name|file/i.test(header));
    const typeIndex = table.headers.findIndex((header) => /type|kind|size/i.test(header));
    const entries = table.rows.map((row) => ({
      name: row[Math.max(0, pathIndex)] || row[0] || 'Archive entry',
      detail: typeIndex >= 0 ? row[typeIndex] : undefined,
    }));
    return entries.length > 0 ? { entries, shownEntries: entries.length } : null;
  }
  const entries = text
    .split(/\r\n|\r|\n/)
    .map((line) => line.replace(/^[-*]\s*/, '').trim())
    .filter((line) => line && !line.startsWith('#'))
    .slice(0, rowLimit)
    .map((line) => ({ name: line }));
  return entries.length > 0 ? { entries, shownEntries: entries.length } : null;
}

function measureParts(kind: CardKind, metadata: Record<string, any> | undefined, previewText: string): string[] {
  const parts: string[] = [];
  const width = metadataNumber(metadata, 'width');
  const height = metadataNumber(metadata, 'height');
  const rowCount = metadataNumber(metadata, 'rowCount', 'row_count', 'totalRowCount', 'total_row_count');
  const columnCount = metadataNumber(metadata, 'columnCount', 'column_count', 'totalColumnCount', 'total_column_count');
  const sheetCount = metadataNumber(metadata, 'sheetCount', 'sheet_count');
  const pageCount = metadataNumber(metadata, 'pageCount', 'page_count');
  const slideCount = metadataNumber(metadata, 'slideCount', 'slide_count');
  const wordCount = metadataNumber(metadata, 'wordCount', 'word_count');
  const explicitLineCount = metadataNumber(metadata, 'lineCount', 'line_count');
  const entryCount = metadataNumber(metadata, 'entryCount', 'entry_count');
  const fileCount = metadataNumber(metadata, 'fileCount', 'file_count');
  const directoryCount = metadataNumber(metadata, 'directoryCount', 'directory_count');

  if (kind === 'image' && width && height) parts.push(`${formatCount(width)} x ${formatCount(height)}`);
  if (kind === 'pdf' && pageCount) parts.push(plural(pageCount, 'page'));
  if (kind === 'spreadsheet') {
    if (rowCount && columnCount) parts.push(`${formatCount(rowCount)} x ${formatCount(columnCount)}`);
    else if (rowCount) parts.push(plural(rowCount, 'row'));
    else if (columnCount) parts.push(plural(columnCount, 'column'));
    if (sheetCount && sheetCount > 1) parts.push(plural(sheetCount, 'sheet'));
  }
  if (kind === 'presentation' && slideCount) parts.push(plural(slideCount, 'slide'));
  if (kind === 'document' && wordCount) parts.push(plural(wordCount, 'word'));
  if (kind === 'pdf' && wordCount) parts.push(plural(wordCount, 'word'));
  if ((kind === 'text' || kind === 'code' || kind === 'html') && (explicitLineCount || previewText)) {
    const count = explicitLineCount || lineCount(previewText);
    if (count > 0) parts.push(plural(count, 'line'));
  }
  if (kind === 'data') {
    const tree = buildJsonTreePreview(previewText);
    if (tree?.keyCount) parts.push(plural(tree.keyCount, 'key'));
    else if (explicitLineCount) parts.push(plural(explicitLineCount, 'line'));
  }
  if (kind === 'archive') {
    if (entryCount) parts.push(plural(entryCount, 'entry', 'entries'));
    else if (fileCount) parts.push(plural(fileCount, 'file'));
    if (directoryCount) parts.push(plural(directoryCount, 'folder'));
  }
  return parts;
}

function fallbackReason(kind: CardKind, metadata: Record<string, any> | undefined, contentNote: string | undefined, previewAvailable?: boolean): string {
  const explicit = firstString(
    contentNote,
    metadataString(metadata, 'error', 'errorMessage', 'error_message', 'reason'),
  );
  if (explicit) {
    return explicit.includes('Preview unavailable')
      ? explicit
      : `Preview unavailable. ${explicit.replace(/\.$/, '')}.`;
  }
  if (previewAvailable === false) return KIND_CONFIG[kind].fallback;
  return KIND_CONFIG[kind].fallback;
}

const PreviewFrame: React.FC<{ children: React.ReactNode; compact: boolean; className?: string }> = ({ children, compact, className }) => (
  <div
    className={cn(
      'mt-3 max-h-[200px] overflow-hidden rounded-lg border border-border/45 bg-background/55',
      compact && 'max-h-32',
      className,
    )}
  >
    {children}
  </div>
);

const LoadingPreview: React.FC<{ compact: boolean }> = ({ compact }) => (
  <PreviewFrame compact={compact} className="relative h-28 overflow-hidden before:absolute before:inset-y-0 before:left-0 before:w-1/2 before:bg-gradient-to-r before:from-transparent before:via-white/5 before:to-transparent before:content-[''] before:motion-safe:animate-[shimmer_1.6s_ease-in-out_infinite]">
    <div className="space-y-2 p-3">
      <div className="h-3 w-3/4 rounded bg-muted/50" />
      <div className="h-3 w-full rounded bg-muted/35" />
      <div className="h-3 w-2/3 rounded bg-muted/35" />
      {!compact && <div className="h-12 rounded-md bg-muted/25" />}
    </div>
  </PreviewFrame>
);

const ReasonPreview: React.FC<{ reason: string; compact: boolean }> = ({ reason, compact }) => (
  <PreviewFrame compact={compact}>
    <div className="flex min-h-16 items-start gap-2 px-3 py-2.5 text-[11px] leading-relaxed text-muted-foreground">
      <AlertTriangle size={13} className="mt-0.5 shrink-0 text-muted-foreground/75" aria-hidden="true" />
      <span className="chat-text-wrap">{reason}</span>
    </div>
  </PreviewFrame>
);

const ThumbnailPreview: React.FC<{ src: string; title: string; compact: boolean; onError: () => void }> = ({
  src,
  title,
  compact,
  onError,
}) => (
  <PreviewFrame compact={compact} className="bg-muted/15">
    <img
      key={src}
      src={src}
      alt={`Preview of ${title}`}
      className={cn('block w-full object-contain', compact ? 'max-h-32' : 'max-h-[200px]')}
      onError={onError}
      loading="lazy"
    />
  </PreviewFrame>
);

const HTML_PREVIEW_CSP = [
  "default-src 'none'",
  "script-src 'unsafe-inline'",
  "style-src 'unsafe-inline'",
  "img-src data: blob:",
  "font-src data:",
  "media-src data: blob:",
  "connect-src 'none'",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
  "frame-src 'none'",
  "worker-src 'none'",
].join('; ');

function htmlPreviewDocument(html: string): string {
  const csp = `<meta http-equiv="Content-Security-Policy" content="${HTML_PREVIEW_CSP}">`;
  const trimmed = html.trim();
  if (!trimmed) {
    return `<!doctype html><html><head>${csp}</head><body></body></html>`;
  }
  if (/<head(\s[^>]*)?>/i.test(trimmed)) {
    return trimmed.replace(/<head(\s[^>]*)?>/i, (match) => `${match}${csp}`);
  }
  if (/<html(\s[^>]*)?>/i.test(trimmed)) {
    return trimmed.replace(/<html(\s[^>]*)?>/i, (match) => `${match}<head>${csp}</head>`);
  }
  const doctypeMatch = trimmed.match(/^\s*<!doctype html>\s*/i);
  if (doctypeMatch) {
    const withoutDoctype = trimmed.slice(doctypeMatch[0].length);
    return `<!doctype html><html><head>${csp}</head><body>${withoutDoctype}</body></html>`;
  }
  return `<!doctype html><html><head>${csp}</head><body>${trimmed}</body></html>`;
}

const PdfPreviewBody: React.FC<{ url: string; title: string; compact: boolean }> = ({ url, title, compact }) => {
  const [objectUrl, setObjectUrl] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    let createdUrl: string | null = null;
    setObjectUrl(null);
    setFailed(false);

    const loadPdf = async () => {
      try {
        const response = await fetch(url, {
          credentials: 'same-origin',
          signal: controller.signal,
        });
        if (!response.ok) {
          throw new Error(`PDF preview failed with HTTP ${response.status}`);
        }
        const blob = await response.blob();
        if (controller.signal.aborted) return;
        const pdfBlob = blob.type ? blob : new Blob([blob], { type: 'application/pdf' });
        createdUrl = URL.createObjectURL(pdfBlob);
        setObjectUrl(createdUrl);
      } catch {
        if (!controller.signal.aborted) {
          setFailed(true);
        }
      }
    };

    void loadPdf();

    return () => {
      controller.abort();
      if (createdUrl) {
        URL.revokeObjectURL(createdUrl);
      }
    };
  }, [url]);

  if (failed) {
    return <ReasonPreview compact={compact} reason="Preview unavailable. The PDF file could not be loaded for rendering." />;
  }
  if (!objectUrl) {
    return <LoadingPreview compact={compact} />;
  }

  const src = `${objectUrl}#toolbar=0&navpanes=0&scrollbar=0&page=1&zoom=page-width`;
  return (
    <PreviewFrame compact={compact} className={cn('bg-muted/15', compact ? 'h-32' : 'h-48')}>
      <iframe
        src={src}
        title={`PDF preview of ${title}`}
        className="h-full w-full border-0 bg-background"
        loading="lazy"
        data-file-preview-kind="pdf"
      />
    </PreviewFrame>
  );
};

const HtmlPreviewBody: React.FC<{ html: string; title: string; compact: boolean }> = ({ html, title, compact }) => {
  const srcDoc = useMemo(() => htmlPreviewDocument(html), [html]);
  return (
    <PreviewFrame compact={compact} className={cn('bg-white', compact ? 'h-32' : 'h-48')}>
      <iframe
        title={`HTML preview of ${title}`}
        srcDoc={srcDoc}
        sandbox={SANDBOXED_HTML_SANDBOX_POLICY}
        referrerPolicy="no-referrer"
        className="h-full w-full border-0 bg-white"
        loading="lazy"
        data-file-preview-kind="html"
      />
    </PreviewFrame>
  );
};

const TablePreviewBody: React.FC<{ table: TablePreview; compact: boolean }> = ({ table, compact }) => (
  <PreviewFrame compact={compact} className="overflow-auto">
    <table className="min-w-full border-separate border-spacing-0 text-left text-[11px]" data-file-preview-kind="spreadsheet-table">
      <thead>
        <tr>
          {table.headers.map((header, index) => (
            <th
              key={`${header}-${index}`}
              className="border-b border-border/50 bg-muted/30 px-2 py-1.5 font-medium text-muted-foreground"
            >
              <span className="line-clamp-1">{header || `Column ${index + 1}`}</span>
            </th>
          ))}
        </tr>
      </thead>
      <tbody>
        {table.rows.map((row, rowIndex) => (
          <tr key={rowIndex} className="odd:bg-muted/10">
            {table.headers.map((_, cellIndex) => (
              <td key={cellIndex} className="max-w-[9rem] border-b border-border/25 px-2 py-1.5 text-muted-foreground">
                <span className="line-clamp-1">{row[cellIndex] ?? ''}</span>
              </td>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  </PreviewFrame>
);

const JsonTreePreviewBody: React.FC<{ tree: JsonTreePreview; compact: boolean }> = ({ tree, compact }) => (
  <PreviewFrame compact={compact}>
    <div className="px-3 py-2 font-mono text-[11px] leading-relaxed">
      <div className="mb-1 text-muted-foreground/70">{tree.rootLabel}</div>
      <ul className="space-y-0.5">
        {tree.entries.slice(0, compact ? 7 : 12).map((entry, index) => (
          <li
            key={`${entry.depth}-${entry.key}-${index}`}
            className="flex min-w-0 gap-2 text-muted-foreground"
            style={{ paddingLeft: `${entry.depth * 14}px` }}
          >
            <span className="shrink-0 text-violet-300/80">{entry.key}</span>
            <span className="min-w-0 truncate text-muted-foreground/65">{entry.value}</span>
          </li>
        ))}
      </ul>
    </div>
  </PreviewFrame>
);

const ArchivePreviewBody: React.FC<{ archive: ArchivePreview; compact: boolean }> = ({ archive, compact }) => (
  <PreviewFrame compact={compact}>
    <ul className="divide-y divide-border/25 text-[11px]">
      {archive.entries.slice(0, compact ? 5 : 8).map((entry, index) => (
        <li key={`${entry.name}-${index}`} className="flex min-w-0 items-center gap-2 px-3 py-1.5 text-muted-foreground">
          <FileText size={12} className="shrink-0 text-muted-foreground/60" aria-hidden="true" />
          <span className="min-w-0 flex-1 truncate">{entry.name}</span>
          {entry.detail && <span className="shrink-0 text-muted-foreground/55">{entry.detail}</span>}
        </li>
      ))}
    </ul>
  </PreviewFrame>
);

const CodePreviewBody: React.FC<{ text: string; compact: boolean }> = ({ text, compact }) => {
  const limit = compact ? CODE_PREVIEW_LINES_COMPACT : CODE_PREVIEW_LINES_FULL;
  const lines = text.split(/\r\n|\r|\n/).slice(0, limit);
  return (
    <PreviewFrame compact={compact} className="overflow-auto">
      <pre className="m-0 min-w-0 p-0 font-mono text-[11px] leading-relaxed">
        {lines.map((line, index) => (
          <div key={index} className="grid min-w-0 grid-cols-[2.5rem_minmax(0,1fr)] border-b border-border/20 last:border-b-0">
            <span className="select-none bg-muted/20 px-2 py-0.5 text-right text-muted-foreground/45">{index + 1}</span>
            <span className="chat-text-wrap px-2 py-0.5 text-muted-foreground">{line || ' '}</span>
          </div>
        ))}
      </pre>
    </PreviewFrame>
  );
};

const ExcerptPreviewBody: React.FC<{
  text: string;
  compact: boolean;
  rich: boolean;
}> = ({ text, compact, rich }) => {
  const displayText = stripInternalCitationHandles(text);
  return (
    <PreviewFrame compact={compact}>
      {rich && !compact ? (
        <div className="max-h-[200px] overflow-hidden px-3 py-2.5 text-[11px] leading-relaxed text-muted-foreground [&_.chat-markdown]:prose-sm [&_.chat-markdown]:text-[11px] [&_.chat-markdown]:leading-relaxed [&_.chat-markdown_p]:my-1 [&_.chat-markdown_.katex-display]:my-1.5">
          <MarkdownRenderer content={displayText} />
        </div>
      ) : (
        <pre className="chat-text-wrap m-0 max-h-[200px] overflow-hidden whitespace-pre-wrap px-3 py-2.5 text-[11px] leading-relaxed text-muted-foreground">
          {displayText}
        </pre>
      )}
    </PreviewFrame>
  );
};

function footerActionFor(args: {
  isUploading: boolean;
  isError: boolean;
  onRetry?: () => void;
  onRemove?: () => void;
  onClick?: () => void;
  canDownload: boolean;
  config: KindConfig;
}): FooterAction | null {
  if (args.isError && args.onRetry) return { kind: 'retry', label: 'Retry upload', Icon: RotateCcw };
  if (args.isUploading && args.onRemove) return { kind: 'remove', label: 'Cancel upload', Icon: X };
  if (!args.isUploading && !args.isError && args.onClick) return { kind: 'open', label: args.config.action, Icon: Eye };
  if (!args.isUploading && !args.isError && args.canDownload) return { kind: 'download', label: `Download ${args.config.noun}`, Icon: Download };
  if (!args.isUploading && args.onRemove) return { kind: 'remove', label: 'Remove file', Icon: X };
  return null;
}

const FileAttachmentCard: React.FC<FileAttachmentCardProps> = ({
  title,
  mimeType,
  fileKind,
  sizeBytes,
  previewText,
  contentNote,
  previewAvailable,
  previewTruncated,
  metadata,
  status,
  progress,
  errorMessage,
  thumbnailUrl,
  thumbnailFetchUrl,
  thumbnailArtifactId,
  pdfPreviewUrl,
  htmlPreviewUrl,
  htmlPreviewContent,
  textPreviewFetchUrl,
  variant = 'full',
  downloadArtifactId,
  downloadFileName,
  onDownload,
  onRetry,
  onClick,
  onRemove,
  className,
}) => {
  const [thumbnailFailed, setThumbnailFailed] = useState(false);
  const [thumbnailLoading, setThumbnailLoading] = useState(false);
  const [rawThumbnailUrl, setRawThumbnailUrl] = useState<string | null>(null);
  const [fetchedTextPreview, setFetchedTextPreview] = useState('');
  const [textPreviewLoading, setTextPreviewLoading] = useState(false);
  const [textPreviewFailed, setTextPreviewFailed] = useState(false);
  const [fetchedHtmlPreview, setFetchedHtmlPreview] = useState('');
  const [htmlPreviewLoading, setHtmlPreviewLoading] = useState(false);
  const [htmlPreviewFailed, setHtmlPreviewFailed] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const rawThumbnailObjectUrlRef = React.useRef<string | null>(null);
  const removeButtonRef = React.useRef<HTMLButtonElement>(null);

  useEffect(() => () => {
    if (rawThumbnailObjectUrlRef.current) {
      URL.revokeObjectURL(rawThumbnailObjectUrlRef.current);
      rawThumbnailObjectUrlRef.current = null;
    }
  }, []);

  useEffect(() => {
    setThumbnailFailed(false);
  }, [thumbnailUrl, thumbnailFetchUrl, thumbnailArtifactId]);

  const compact = variant === 'compact';
  const cardKind = resolveCardKind(fileKind, mimeType, title);
  const config = KIND_CONFIG[cardKind];
  const KindIcon = config.Icon;
  const isUploading = status === 'uploading';
  const isError = status === 'error';
  const progressPct = Math.round(Math.min(1, Math.max(0, progress ?? 0)) * 100);
  const canDownload = Boolean(downloadArtifactId || onDownload) && !isUploading && !isError;
  const interactive = typeof onClick === 'function' && !isUploading && !isError;

  useEffect(() => {
    setFetchedTextPreview('');
    setTextPreviewFailed(false);

    if (!textPreviewFetchUrl || isUploading || isError) {
      setTextPreviewLoading(false);
      return;
    }

    let cancelled = false;
    const controller = new AbortController();
    setTextPreviewLoading(true);
    fetch(textPreviewFetchUrl, {
      credentials: 'same-origin',
      signal: controller.signal,
    })
      .then((response) => {
        if (!response.ok) throw new Error(`Text preview failed (HTTP ${response.status})`);
        return response.text();
      })
      .then((text) => {
        if (cancelled) return;
        setFetchedTextPreview(text);
        setTextPreviewLoading(false);
      })
      .catch((error) => {
        if (!cancelled && error?.name !== 'AbortError') {
          setTextPreviewFailed(true);
          setTextPreviewLoading(false);
        }
      });

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [textPreviewFetchUrl, isUploading, isError]);

  useEffect(() => {
    setFetchedHtmlPreview('');
    setHtmlPreviewFailed(false);

    if (!htmlPreviewUrl || htmlPreviewContent || isUploading || isError) {
      setHtmlPreviewLoading(false);
      return;
    }

    let cancelled = false;
    const controller = new AbortController();
    setHtmlPreviewLoading(true);
    fetch(htmlPreviewUrl, {
      credentials: 'same-origin',
      signal: controller.signal,
    })
      .then((response) => {
        if (!response.ok) throw new Error(`HTML preview failed (HTTP ${response.status})`);
        return response.text();
      })
      .then((text) => {
        if (cancelled) return;
        setFetchedHtmlPreview(text);
        setHtmlPreviewLoading(false);
      })
      .catch((error) => {
        if (!cancelled && error?.name !== 'AbortError') {
          setHtmlPreviewFailed(true);
          setHtmlPreviewLoading(false);
        }
      });

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [htmlPreviewUrl, htmlPreviewContent, isUploading, isError]);

  const htmlPreviewSource = (htmlPreviewContent || fetchedHtmlPreview).trim();
  const previewTextSource = textPreviewFetchUrl ? fetchedTextPreview : previewText;
  const cleanPreview = typeof previewTextSource === 'string' ? previewTextSource.trim() : '';
  const usablePreview = !isLowValuePreviewText(cleanPreview);
  const clipped = useMemo(() => clippedPreviewText(cleanPreview), [cleanPreview]);
  const displayPreview = usablePreview ? clipped.text : '';
  const richPreview = displayPreview ? shouldRenderRichPreview(displayPreview, mimeType) : false;
  const metaParts = useMemo(() => {
    const label = formatMimeLabel(mimeType, title, cardKind);
    const size = formatBytes(sizeBytes);
    return [
      label,
      size,
      ...measureParts(cardKind, metadata, displayPreview),
    ].filter(Boolean);
  }, [cardKind, displayPreview, metadata, mimeType, sizeBytes, title]);
  const metaLine = isUploading
    ? [...metaParts, `Uploading ${progressPct}%`].join(' · ')
    : metaParts.join(' · ');

  useEffect(() => {
    if (rawThumbnailObjectUrlRef.current) {
      URL.revokeObjectURL(rawThumbnailObjectUrlRef.current);
      rawThumbnailObjectUrlRef.current = null;
    }
    setRawThumbnailUrl(null);

    const authenticatedThumbnailUrl = thumbnailArtifactId
      ? artifactRawUrl(thumbnailArtifactId)
      : thumbnailFetchUrl;
    if (!authenticatedThumbnailUrl || isUploading || isError) {
      setThumbnailLoading(false);
      return;
    }

    let cancelled = false;
    const controller = new AbortController();
    setThumbnailLoading(true);
    fetch(authenticatedThumbnailUrl, {
      credentials: 'same-origin',
      signal: controller.signal,
    })
      .then((response) => {
        if (!response.ok) throw new Error(`Thumbnail failed (HTTP ${response.status})`);
        return response.blob();
      })
      .then((blob) => {
        if (cancelled) return;
        const objectUrl = URL.createObjectURL(blob);
        rawThumbnailObjectUrlRef.current = objectUrl;
        setRawThumbnailUrl(objectUrl);
        setThumbnailLoading(false);
      })
      .catch((error) => {
        if (!cancelled && error?.name !== 'AbortError') {
          setThumbnailFailed(true);
          setThumbnailLoading(false);
        }
      });

    return () => {
      cancelled = true;
      controller.abort();
      if (rawThumbnailObjectUrlRef.current) {
        URL.revokeObjectURL(rawThumbnailObjectUrlRef.current);
        rawThumbnailObjectUrlRef.current = null;
      }
    };
  }, [thumbnailArtifactId, thumbnailFetchUrl, isUploading, isError]);

  const handleDownload = async (event: React.MouseEvent<HTMLElement>) => {
    event.stopPropagation();
    if ((!downloadArtifactId && !onDownload) || downloading) return;
    setDownloading(true);
    try {
      if (downloadArtifactId) {
        await downloadArtifactRawBytes(downloadArtifactId, downloadFileName || title);
      } else {
        await onDownload?.();
      }
    } catch {
      toast.error(`Couldn't download ${title}.`);
    } finally {
      setDownloading(false);
    }
  };

  const effectiveThumbnailUrl = rawThumbnailUrl || (!thumbnailArtifactId && !thumbnailFetchUrl ? thumbnailUrl : undefined);
  const showThumbnail = Boolean(effectiveThumbnailUrl) && !thumbnailFailed && !isError;
  const isFetchingThumbnail = Boolean((thumbnailArtifactId || thumbnailFetchUrl) && !rawThumbnailUrl && !thumbnailFailed && !isUploading && !isError);
  const tablePreview = useMemo(
    () => cardKind === 'spreadsheet'
      ? buildTablePreview(displayPreview, title, mimeType, compact ? TABLE_PREVIEW_ROWS_COMPACT : TABLE_PREVIEW_ROWS_FULL)
      : null,
    [cardKind, compact, displayPreview, mimeType, title],
  );
  const jsonPreview = useMemo(
    () => cardKind === 'data' ? buildJsonTreePreview(displayPreview) : null,
    [cardKind, displayPreview],
  );
  const archivePreview = useMemo(
    () => cardKind === 'archive' ? buildArchivePreview(displayPreview, compact ? 5 : 8) : null,
    [cardKind, compact, displayPreview],
  );
  const fallback = fallbackReason(cardKind, metadata, contentNote, previewAvailable);

  const previewBody = (() => {
    if (isUploading) return <LoadingPreview compact={compact} />;
    if (isError) {
      return <ReasonPreview compact={compact} reason={errorMessage || 'Upload failed.'} />;
    }
    if (pdfPreviewUrl && cardKind === 'pdf') {
      return <PdfPreviewBody url={pdfPreviewUrl} title={title} compact={compact} />;
    }
    if (htmlPreviewLoading || textPreviewLoading) return <LoadingPreview compact={compact} />;
    if (htmlPreviewSource && cardKind === 'html') {
      return <HtmlPreviewBody html={htmlPreviewSource} title={title} compact={compact} />;
    }
    if (htmlPreviewFailed && cardKind === 'html') {
      return <ReasonPreview compact={compact} reason="Preview unavailable. The HTML file could not be loaded for rendering." />;
    }
    if (textPreviewFailed && textPreviewFetchUrl) {
      return <ReasonPreview compact={compact} reason="Preview unavailable. The sheet preview could not be loaded." />;
    }
    if (thumbnailLoading || isFetchingThumbnail) return <LoadingPreview compact={compact} />;
    if (showThumbnail && effectiveThumbnailUrl) {
      return (
        <ThumbnailPreview
          src={effectiveThumbnailUrl}
          title={title}
          compact={compact}
          onError={() => setThumbnailFailed(true)}
        />
      );
    }
    if (tablePreview) return <TablePreviewBody table={tablePreview} compact={compact} />;
    if (jsonPreview) return <JsonTreePreviewBody tree={jsonPreview} compact={compact} />;
    if (archivePreview) return <ArchivePreviewBody archive={archivePreview} compact={compact} />;
    if (displayPreview && cardKind === 'code') return <CodePreviewBody text={displayPreview} compact={compact} />;
    if (displayPreview) return <ExcerptPreviewBody text={displayPreview} compact={compact} rich={richPreview} />;
    return <ReasonPreview compact={compact} reason={fallback} />;
  })();

  const footerNote = (() => {
    if (isUploading) return `Uploading ${progressPct}%`;
    if (isError) return 'Upload failed';
    if (thumbnailLoading || isFetchingThumbnail || htmlPreviewLoading || textPreviewLoading) return 'Loading preview';
    if (pdfPreviewUrl && cardKind === 'pdf') return 'PDF preview';
    if (htmlPreviewSource && cardKind === 'html') return 'HTML preview';
    if (tablePreview) {
      const totalRows = metadataNumber(metadata, 'rowCount', 'row_count', 'totalRowCount', 'total_row_count');
      return totalRows && totalRows > tablePreview.shownRows
        ? `Showing ${formatCount(tablePreview.shownRows)} of ${formatCount(totalRows)} rows`
        : `${plural(tablePreview.shownRows, 'row')} previewed`;
    }
    if (jsonPreview) return `${plural(jsonPreview.keyCount, 'key')} previewed`;
    if (archivePreview) {
      const totalEntries = metadataNumber(metadata, 'entryCount', 'entry_count');
      return totalEntries && totalEntries > archivePreview.shownEntries
        ? `Showing ${formatCount(archivePreview.shownEntries)} of ${formatCount(totalEntries)} entries`
        : `${plural(archivePreview.shownEntries, 'entry', 'entries')} previewed`;
    }
    if (cardKind === 'code' && displayPreview) {
      const totalLines = metadataNumber(metadata, 'lineCount', 'line_count') || lineCount(cleanPreview);
      const shownLines = Math.min(totalLines, compact ? CODE_PREVIEW_LINES_COMPACT : CODE_PREVIEW_LINES_FULL);
      return totalLines > shownLines
        ? `Showing ${formatCount(shownLines)} of ${formatCount(totalLines)} lines`
        : `${plural(totalLines, 'line')} previewed`;
    }
    if (previewTruncated || clipped.clipped) return 'Preview truncated';
    if (!displayPreview && !showThumbnail && !pdfPreviewUrl && !htmlPreviewSource) return 'Preview unavailable';
    return `${config.label} preview`;
  })();

  const footerAction = footerActionFor({
    isUploading,
    isError,
    onRetry,
    onRemove,
    onClick,
    canDownload,
    config,
  });

  const handleFooterAction = async (event: React.MouseEvent<HTMLButtonElement>) => {
    event.stopPropagation();
    if (!footerAction) return;
    if (footerAction.kind === 'open') {
      onClick?.();
      return;
    }
    if (footerAction.kind === 'download') {
      await handleDownload(event);
      return;
    }
    if (footerAction.kind === 'retry') {
      onRetry?.();
      removeButtonRef.current?.focus();
      return;
    }
    if (footerAction.kind === 'remove') {
      onRemove?.();
    }
  };

  return (
    <Card
      role={isError ? 'alert' : undefined}
      aria-busy={isUploading || thumbnailLoading || isFetchingThumbnail || htmlPreviewLoading || textPreviewLoading || undefined}
      className={cn(
        'group relative max-w-full min-w-0 overflow-hidden rounded-xl border bg-card/65 shadow-sm transition-all',
        isError ? 'border-destructive/45' : 'border-border/55',
        interactive && 'cursor-pointer hover:border-border hover:bg-card hover:shadow-md hover:shadow-black/10',
        compact ? 'w-full' : 'w-full',
        className,
      )}
    >
      {interactive && (
        <button
          type="button"
          onClick={onClick}
          className="absolute inset-0 z-0 cursor-pointer appearance-none rounded-[inherit] border-0 bg-transparent p-0 text-inherit focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-card"
          aria-label={`${config.action} ${title}`}
          title={`${config.action} ${title}`}
        />
      )}

      <div className={cn('relative z-10 min-w-0', interactive && 'pointer-events-none', compact ? 'p-2.5' : 'p-3')}>
        <div className="flex min-w-0 items-start gap-3">
          <div
            className={cn(
              'flex h-9 w-9 shrink-0 items-center justify-center rounded-lg',
              isError ? 'bg-destructive/10 text-destructive' : config.tileClass,
            )}
            aria-hidden="true"
          >
            {isUploading
              ? <Loader2 size={18} className="motion-safe:animate-spin" />
              : isError
                ? <AlertTriangle size={18} />
                : <KindIcon size={18} />}
          </div>
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium leading-5 text-card-foreground" title={title}>
              {title}
            </div>
            {metaLine && (
              <div className="mt-0.5 truncate text-xs leading-4 text-muted-foreground" title={metaLine}>
                {metaLine}
              </div>
            )}
            {metadata?.sendFidelity === 'reduced' && (
              <div
                data-attachment-send-fidelity="reduced"
                className="mt-0.5 inline-flex items-center gap-1 text-[10px] leading-4 text-amber-500"
                title="Sent with reduced fidelity for this run (e.g. extracted text). The stored file is unchanged."
              >
                ▽ Reduced fidelity
              </div>
            )}
          </div>
          <div className="pointer-events-auto flex shrink-0 items-center gap-1">
            {canDownload && (
              <button
                type="button"
                onClick={handleDownload}
                aria-disabled={downloading || undefined}
                aria-busy={downloading || undefined}
                className="inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground/65 transition-colors hover:bg-background/80 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                aria-label={`Download ${title}`}
                title={`Download ${title}`}
              >
                {downloading
                  ? <Loader2 size={14} className="motion-safe:animate-spin" aria-hidden="true" />
                  : <Download size={14} aria-hidden="true" />}
              </button>
            )}
            {onRemove && footerAction?.kind !== 'remove' && (
              <button
                ref={removeButtonRef}
                type="button"
                onClick={(event) => {
                  event.stopPropagation();
                  onRemove();
                }}
                className="inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground/60 transition-colors hover:bg-background/80 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                aria-label={isUploading ? `Cancel upload of ${title}` : `Remove ${title}`}
                title={isUploading ? 'Cancel upload' : 'Remove file'}
              >
                <X size={14} />
              </button>
            )}
          </div>
        </div>

        {previewBody}

        <div className="mt-3 flex min-w-0 items-center justify-between gap-3 border-t border-border/35 pt-2.5">
          <span className="min-w-0 truncate text-[11px] leading-5 text-muted-foreground/80" title={footerNote}>
            {footerNote}
          </span>
          {footerAction && (
            <button
              type="button"
              onClick={handleFooterAction}
              aria-label={footerAction.kind === 'retry' ? `Retry uploading ${title}` : undefined}
              className={cn(
                'pointer-events-auto inline-flex h-7 shrink-0 items-center gap-1.5 rounded-md border px-2.5 text-[11px] font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
                footerAction.kind === 'retry'
                  ? 'border-destructive/35 bg-destructive/10 text-destructive hover:bg-destructive/15'
                  : 'border-border/60 bg-background/70 text-muted-foreground hover:bg-background hover:text-foreground',
              )}
            >
              {downloading && footerAction.kind === 'download' ? (
                <Loader2 size={12} className="motion-safe:animate-spin" aria-hidden="true" />
              ) : footerAction.Icon ? (
                <footerAction.Icon size={12} aria-hidden="true" />
              ) : null}
              <span>{footerAction.label}</span>
            </button>
          )}
        </div>
      </div>

      {isUploading && (
        <div
          className="absolute inset-x-0 bottom-0 h-0.5 bg-border/40"
          role="progressbar"
          aria-label={`Uploading ${title}`}
          aria-valuemin={0}
          aria-valuemax={100}
          aria-valuenow={typeof progress === 'number' ? progressPct : undefined}
        >
          <div
            className="h-full bg-primary transition-[width] duration-200"
            style={{ width: `${Math.max(4, progressPct)}%` }}
          />
        </div>
      )}
    </Card>
  );
};

export default React.memo(FileAttachmentCard);
