import type { Artifact } from '@/types';

interface CodeFenceMatch {
  index: number;
  end: number;
  fence: string;
  info: string;
  content: string;
}

export interface GeneratedFileArtifactItem {
  key: string;
  artifact: Artifact;
}

export interface GeneratedFileArtifactProjection {
  markdown: string;
  items: GeneratedFileArtifactItem[];
}

const FILE_LANGUAGE_EXTENSIONS: Record<string, string> = {
  bash: 'sh',
  css: 'css',
  csv: 'csv',
  go: 'go',
  htm: 'html',
  html: 'html',
  javascript: 'js',
  js: 'js',
  json: 'json',
  jsx: 'jsx',
  markdown: 'md',
  md: 'md',
  py: 'py',
  python: 'py',
  sh: 'sh',
  sql: 'sql',
  svg: 'svg',
  text: 'txt',
  ts: 'ts',
  tsx: 'tsx',
  txt: 'txt',
  typescript: 'ts',
  xml: 'xml',
  yaml: 'yaml',
  yml: 'yml',
};

const MIME_BY_EXTENSION: Record<string, string> = {
  css: 'text/css',
  csv: 'text/csv',
  go: 'text/x-go',
  htm: 'text/html',
  html: 'text/html',
  js: 'text/javascript',
  json: 'application/json',
  jsx: 'text/jsx',
  md: 'text/markdown',
  py: 'text/x-python',
  sh: 'application/x-sh',
  sql: 'application/sql',
  svg: 'image/svg+xml',
  ts: 'text/typescript',
  tsx: 'text/tsx',
  txt: 'text/plain',
  xml: 'application/xml',
  yaml: 'application/yaml',
  yml: 'application/yaml',
};

const FILE_KIND_BY_EXTENSION: Record<string, string> = {
  csv: 'csv',
  htm: 'html',
  html: 'html',
  json: 'json',
  md: 'markdown',
  svg: 'image',
  txt: 'text',
  xml: 'text',
  yaml: 'config',
  yml: 'config',
};

function stableHash(value: string): string {
  let hash = 2166136261;
  for (let i = 0; i < value.length; i += 1) {
    hash ^= value.charCodeAt(i);
    hash = Math.imul(hash, 16777619);
  }
  return (hash >>> 0).toString(36);
}

function sanitizeFileName(value: string): string {
  return value
    .replace(/[/\\?%*:|"<>]/g, '-')
    .replace(/[\u0000-\u001f\u007f]/g, '')
    .replace(/\s+/g, ' ')
    .trim();
}

function titleFromHeading(content: string): string {
  const heading = content.match(/^\s*#\s+(.+?)\s*$/m)?.[1];
  return sanitizeFileName(heading || '');
}

function splitInfo(info: string): string[] {
  return String(info || '')
    .trim()
    .split(/\s+/)
    .map((part) => part.trim())
    .filter(Boolean);
}

function fileNameFromInfo(info: string): string {
  const parts = splitInfo(info);
  for (const part of parts) {
    const direct = part.match(/^(?:file|filename|path|title)=["']?([^"']+)["']?$/i)?.[1];
    if (direct) return sanitizeFileName(direct);
    if (/^[\w .@()[\]-]+\.[a-z0-9]{1,12}$/i.test(part)) return sanitizeFileName(part);
  }
  return '';
}

function primaryLanguage(info: string): string {
  const token = splitInfo(info)[0] || 'text';
  return token.toLowerCase().replace(/^\./, '');
}

function extensionForLanguage(language: string): string {
  const normalized = language.toLowerCase();
  return FILE_LANGUAGE_EXTENSIONS[normalized] || normalized;
}

function fileKindForExtension(ext: string): string {
  return FILE_KIND_BY_EXTENSION[ext] || (ext === 'js' || ext === 'jsx' || ext === 'ts' || ext === 'tsx' || ext === 'py' || ext === 'go' || ext === 'css' || ext === 'sql' || ext === 'sh' ? 'code' : 'text');
}

function countWords(content: string): number {
  const matches = content.trim().match(/\b[\p{L}\p{N}'-]+\b/gu);
  return matches ? matches.length : 0;
}

function extractCodeFences(markdown: string): CodeFenceMatch[] {
  const matches: CodeFenceMatch[] = [];
  const re = /(^|\n)(`{3,}|~{3,})([^\n]*)\n([\s\S]*?)(?:\n\2[ \t]*(?=\n|$))/g;
  let match: RegExpExecArray | null;
  while ((match = re.exec(markdown)) !== null) {
    const leading = match[1] || '';
    const fence = match[2] || '';
    const info = match[3] || '';
    const content = match[4] || '';
    const index = match.index + leading.length;
    const end = re.lastIndex;
    matches.push({ index, end, fence, info, content });
  }
  return matches;
}

function shouldPromoteFence(match: CodeFenceMatch): boolean {
  const language = primaryLanguage(match.info);
  if (!language || language === 'text') return match.content.trim().length >= 80;
  if (FILE_LANGUAGE_EXTENSIONS[language]) return match.content.trim().length >= 40;
  return /^[a-z0-9][a-z0-9_+.-]{1,24}$/i.test(language) && match.content.trim().length >= 120;
}

function generatedTitle(match: CodeFenceMatch, index: number, ext: string): string {
  const explicit = fileNameFromInfo(match.info);
  if (explicit) return explicit;
  const heading = titleFromHeading(match.content);
  if (heading) return heading.includes('.') ? heading : `${heading}.${ext}`;
  return `generated-${index + 1}.${ext || 'txt'}`;
}

function generatedArtifact(seed: string, match: CodeFenceMatch, index: number): Artifact {
  const language = primaryLanguage(match.info);
  const ext = extensionForLanguage(language);
  const title = generatedTitle(match, index, ext);
  const content = match.content.replace(/\s+$/, '');
  const mimeType = MIME_BY_EXTENSION[ext] || 'text/plain';
  const fileKind = fileKindForExtension(ext);
  const id = `generated-file-${stableHash(`${seed}:${index}:${title}:${content}`)}`;
  const lineCount = content ? content.split(/\r\n|\r|\n/).length : 0;
  return {
    id,
    type: 'file',
    title,
    content,
    language,
    metadata: {
      source: 'assistant_generated',
      generatedFrom: 'assistant_code_fence',
      mimeType,
      normalizedMimeType: mimeType,
      fileKind,
      fileName: title,
      file_name: title,
      downloadName: title,
      download_name: title,
      isText: true,
      previewAvailable: true,
      sizeBytes: new Blob([content]).size,
      lineCount,
      wordCount: fileKind === 'markdown' || fileKind === 'text' ? countWords(content) : undefined,
    },
  };
}

export function projectGeneratedFileArtifactsFromMarkdown(
  markdown: string,
  seed: string,
): GeneratedFileArtifactProjection {
  const source = typeof markdown === 'string' ? markdown : '';
  if (!source.includes('```') && !source.includes('~~~')) {
    return { markdown: source, items: [] };
  }

  const matches = extractCodeFences(source).filter(shouldPromoteFence);
  if (matches.length === 0) return { markdown: source, items: [] };

  const items = matches.map((match, index) => ({
    key: `${seed}:generated-file:${index}:${match.index}`,
    artifact: generatedArtifact(seed, match, index),
  }));

  let stripped = '';
  let cursor = 0;
  for (const match of matches) {
    stripped += source.slice(cursor, match.index);
    cursor = match.end;
  }
  stripped += source.slice(cursor);
  stripped = stripped.replace(/\n{3,}/g, '\n\n').trim();

  return { markdown: stripped, items };
}
