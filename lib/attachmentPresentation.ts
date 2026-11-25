import { fileArtifactFileKind, fileArtifactMimeType } from '@/lib/fileProvenance';
import type { Artifact, ChatFileAttachment } from '@/types';

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return '';
}

function firstBool(...values: unknown[]): boolean | undefined {
  for (const value of values) {
    if (typeof value === 'boolean') return value;
  }
  return undefined;
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

function stringList(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value
    .map((item) => (typeof item === 'string' ? item.trim() : ''))
    .filter(Boolean);
}

function fileExtension(title: string): string {
  const clean = String(title || '').trim().toLowerCase();
  const dot = clean.lastIndexOf('.');
  if (dot <= 0 || dot === clean.length - 1) return '';
  return clean.slice(dot + 1);
}

function imagePreviewPath(path: string): boolean {
  return /\.(?:png|jpe?g|gif|webp)$/i.test(path);
}

function spreadsheetPreviewPath(path: string): boolean {
  const clean = String(path || '').trim().toLowerCase().replaceAll('\\', '/');
  return clean.includes('/worksheets/') && clean.endsWith('/preview.md');
}

function normalizedExtractedPath(path: string): string {
  return String(path || '').trim().replaceAll('\\', '/').replace(/^\/+/, '');
}

function spreadsheetSheetPreviewJsonPath(path: string): string {
  const clean = normalizedExtractedPath(path);
  const lower = clean.toLowerCase();
  if (!lower.includes('/worksheets/')) return '';
  if (lower.endsWith('/preview.json')) return clean;
  if (lower.endsWith('/preview.md')) return clean.replace(/\/preview\.md$/i, '/preview.json');
  if (/\/cells-\d+\.jsonl$/i.test(clean)) return clean.replace(/\/cells-\d+\.jsonl$/i, '/preview.json');
  return '';
}

function spreadsheetWorkbookPreviewJsonPath(path: string): string {
  const clean = normalizedExtractedPath(path);
  const lower = clean.toLowerCase();
  if (!clean) return '';
  if (lower === 'workbook.json' || lower.endsWith('/workbook.json')) return clean;
  if (lower.endsWith('/index/workbook.md')) return clean.replace(/\/index\/workbook\.md$/i, '/workbook.json');
  if (lower.endsWith('/workbook.md')) return clean.replace(/\/workbook\.md$/i, '/workbook.json');
  return '';
}

function imageFileExtension(ext: string): boolean {
  return ['png', 'jpg', 'jpeg', 'gif', 'webp'].includes(ext);
}

function textPreviewFileExtension(ext: string): boolean {
  return [
    'txt', 'md', 'markdown', 'log',
    'csv', 'tsv', 'tab',
    'json', 'jsonl', 'ndjson',
    'html', 'htm', 'xml', 'svg',
    'yaml', 'yml', 'toml', 'ini', 'cfg', 'conf', 'env', 'properties',
    'sql', 'go', 'js', 'jsx', 'ts', 'tsx', 'py', 'rb', 'rs', 'java',
    'css', 'scss', 'sh', 'bash', 'zsh',
  ].includes(ext);
}

function shouldUseContentAsTextPreview(mimeType: string, fileKind: string, title: string, metadata: Record<string, any>): boolean {
  const mime = String(mimeType || '').trim().toLowerCase();
  const kind = String(fileKind || '').trim().toLowerCase();
  const ext = fileExtension(title);
  const name = String(title || '').trim().toLowerCase().split(/[\\/]/).pop() || '';

  if (metadata.isText === true || metadata.is_text === true) return true;
  if (['text', 'markdown', 'csv', 'json', 'html', 'code', 'config'].includes(kind)) return true;
  if (
    mime.startsWith('text/') ||
    mime === 'application/json' ||
    mime === 'application/x-ndjson' ||
    mime === 'application/jsonl' ||
    mime === 'application/xml' ||
    mime === 'application/xhtml+xml' ||
    mime === 'application/x-yaml' ||
    mime === 'application/yaml' ||
    mime === 'application/toml' ||
    mime === 'application/javascript' ||
    mime === 'application/x-javascript' ||
    mime === 'application/x-sh' ||
    mime.endsWith('+json') ||
    mime.endsWith('+xml')
  ) return true;
  if (textPreviewFileExtension(ext)) return true;
  return ['dockerfile', 'makefile', 'rakefile', 'gemfile', 'procfile'].includes(name) || name.startsWith('.env');
}

function shouldHideTextPreviewFallback(mimeType: string, fileKind: string, title: string): boolean {
  const mime = String(mimeType || '').trim().toLowerCase();
  const kind = String(fileKind || '').trim().toLowerCase();
  const ext = fileExtension(title);

  if (kind === 'pdf' || mime === 'application/pdf' || ext === 'pdf') return true;
  if (kind === 'image' || mime.startsWith('image/') || imageFileExtension(ext)) return true;
  return false;
}

function extractedPreviewUrl(artifactId: string, relPath: string): string | undefined {
  const id = String(artifactId || '').trim();
  const path = String(relPath || '').trim();
  if (!id || !path || !imagePreviewPath(path)) return undefined;
  return `/api/artifacts/${encodeURIComponent(id)}/extracted?path=${encodeURIComponent(path)}`;
}

function extractedSpreadsheetPreviewUrl(artifactId: string, relPath: string): string | undefined {
  const id = String(artifactId || '').trim();
  const path = String(relPath || '').trim();
  if (!id || !path || !spreadsheetPreviewPath(path)) return undefined;
  return `/api/artifacts/${encodeURIComponent(id)}/extracted?path=${encodeURIComponent(path)}`;
}

function extractedSpreadsheetJsonPreviewUrl(artifactId: string, relPath: string): string | undefined {
  const id = String(artifactId || '').trim();
  const path = normalizedExtractedPath(relPath);
  const lower = path.toLowerCase();
  if (!id || !path || (!lower.endsWith('/preview.json') && lower !== 'workbook.json' && !lower.endsWith('/workbook.json'))) {
    return undefined;
  }
  return `/api/artifacts/${encodeURIComponent(id)}/extracted?path=${encodeURIComponent(path)}`;
}

function extractedPreviewPathFromUrl(artifactId: string, rawUrl?: string): string {
  const id = String(artifactId || '').trim();
  const url = String(rawUrl || '').trim();
  if (!id || !url) return '';
  try {
    const parsed = new URL(url, 'http://local');
    const match = parsed.pathname.match(/^\/api\/artifacts\/([^/]+)\/extracted$/);
    if (!match || decodeURIComponent(match[1]) !== id) return '';
    const path = parsed.searchParams.get('path') || '';
    return imagePreviewPath(path) ? path : '';
  } catch {
    return '';
  }
}

function artifactExtraction(artifact: Artifact): Record<string, any> {
  const extraction = (artifact.metadata || {}).extraction;
  return extraction && typeof extraction === 'object' && !Array.isArray(extraction)
    ? extraction as Record<string, any>
    : {};
}

/**
 * Thumbnail source for image-kind uploaded files. The raw endpoint streams the
 * original bytes; non-image attachments have no thumbnail.
 */
export function fileArtifactThumbnailUrl(artifact: Artifact): string | undefined {
  const kind = fileArtifactFileKind(artifact).toLowerCase();
  const mime = fileArtifactMimeType(artifact).toLowerCase();
  const ext = fileExtension(artifact.title || '');
  if (kind !== 'image' && !mime.startsWith('image/') && !imageFileExtension(ext)) return undefined;
  return `/api/artifacts/${encodeURIComponent(artifact.id)}/raw`;
}

export function fileArtifactRenderedPreviewUrls(artifact: Artifact): string[] {
  const extraction = artifactExtraction(artifact);
  const paths = stringList(extraction.renderedSlides || extraction.rendered_slides);
  const previewImagePath = firstString(extraction.previewImagePath, extraction.preview_image_path);
  const ordered = previewImagePath ? [previewImagePath, ...paths] : paths;
  const seen = new Set<string>();
  const urls: string[] = [];
  for (const path of ordered) {
    if (seen.has(path)) continue;
    seen.add(path);
    const url = extractedPreviewUrl(artifact.id, path);
    if (url) urls.push(url);
  }
  return urls;
}

export function fileArtifactRenderedPreviewUrl(artifact: Artifact): string | undefined {
  return fileArtifactRenderedPreviewUrls(artifact)[0];
}

function extractionRootPath(extraction: Record<string, any>): string {
  return firstString(extraction.rootPath, extraction.root_path).replace(/^\/+|\/+$/g, '');
}

function joinExtractionRoot(root: string, relPath: string): string {
  const clean = String(relPath || '').trim().replace(/^\/+|\/+$/g, '');
  if (!clean) return '';
  const normalizedRoot = String(root || '').trim().replace(/^\/+|\/+$/g, '');
  return normalizedRoot && !clean.startsWith(`${normalizedRoot}/`)
    ? `${normalizedRoot}/${clean}`
    : clean;
}

export function fileArtifactSpreadsheetPreviewUrls(artifact: Artifact): string[] {
  const extraction = artifactExtraction(artifact);
  const root = extractionRootPath(extraction);
  const directPaths = stringList(extraction.sheetOutputs || extraction.sheet_outputs);
  const outputPaths = extraction.outputs && typeof extraction.outputs === 'object' && !Array.isArray(extraction.outputs)
    ? stringList((extraction.outputs as Record<string, any>).sheetOutputs || (extraction.outputs as Record<string, any>).sheet_outputs)
    : [];
  const seen = new Set<string>();
  const urls: string[] = [];
  for (const rawPath of [...directPaths, ...outputPaths.map((p) => joinExtractionRoot(root, p))]) {
    const path = String(rawPath || '').trim();
    if (!path || seen.has(path)) continue;
    seen.add(path);
    const url = extractedSpreadsheetPreviewUrl(artifact.id, path);
    if (url) urls.push(url);
  }
  return urls;
}

export interface FileArtifactSpreadsheetWorkbookPreviewUrls {
  workbookUrl?: string;
  sheetUrls: string[];
}

export function fileArtifactSpreadsheetWorkbookPreviewUrls(artifact: Artifact): FileArtifactSpreadsheetWorkbookPreviewUrls {
  const extraction = artifactExtraction(artifact);
  const root = extractionRootPath(extraction);
  const outputs = extraction.outputs && typeof extraction.outputs === 'object' && !Array.isArray(extraction.outputs)
    ? extraction.outputs as Record<string, any>
    : {};
  const directPreviewPaths = stringList(extraction.sheetPreviewOutputs || extraction.sheet_preview_outputs);
  const outputPreviewPaths = stringList(outputs.sheetPreviewOutputs || outputs.sheet_preview_outputs);
  const legacyDirectSheetPaths = stringList(extraction.sheetOutputs || extraction.sheet_outputs);
  const legacyOutputSheetPaths = stringList(outputs.sheetOutputs || outputs.sheet_outputs);
  const workbookPath = firstString(extraction.workbookPath, extraction.workbook_path);
  const outputWorkbookPath = firstString(outputs.workbookPath, outputs.workbook_path);

  const seenSheets = new Set<string>();
  const sheetUrls: string[] = [];
  const addSheetPath = (candidate: string) => {
    const path = spreadsheetSheetPreviewJsonPath(candidate);
    if (!path || seenSheets.has(path)) return;
    seenSheets.add(path);
    const url = extractedSpreadsheetJsonPreviewUrl(artifact.id, path);
    if (url) sheetUrls.push(url);
  };

  for (const rawPath of directPreviewPaths) {
    addSheetPath(rawPath);
  }
  for (const rawPath of outputPreviewPaths) {
    addSheetPath(joinExtractionRoot(root, rawPath));
  }
  for (const rawPath of legacyDirectSheetPaths) {
    addSheetPath(rawPath);
  }
  for (const rawPath of legacyOutputSheetPaths) {
    addSheetPath(joinExtractionRoot(root, rawPath));
  }

  const workbookCandidates = [
    workbookPath,
    joinExtractionRoot(root, outputWorkbookPath),
  ];
  let workbookUrl: string | undefined;
  const seenWorkbookPaths = new Set<string>();
  for (const candidate of workbookCandidates) {
    const path = spreadsheetWorkbookPreviewJsonPath(candidate);
    if (!path || seenWorkbookPaths.has(path)) continue;
    seenWorkbookPaths.add(path);
    workbookUrl = extractedSpreadsheetJsonPreviewUrl(artifact.id, path);
    if (workbookUrl) break;
  }

  return { workbookUrl, sheetUrls };
}

export interface FileArtifactCardPresentation {
  mimeType?: string;
  fileKind?: string;
  sizeBytes?: number;
  previewText?: string;
  contentNote?: string;
  previewAvailable?: boolean;
  previewTruncated?: boolean;
  metadata?: Record<string, any>;
  thumbnailUrl?: string;
  thumbnailFetchUrl?: string;
  thumbnailArtifactId?: string;
  pdfPreviewUrl?: string;
  htmlPreviewUrl?: string;
  htmlPreviewContent?: string;
  textPreviewFetchUrl?: string;
}

function artifactRawUrl(artifactId: string): string {
  return `/api/artifacts/${encodeURIComponent(artifactId)}/raw`;
}

function isPdfArtifact(mimeType: string, fileKind: string, title: string): boolean {
  const mime = String(mimeType || '').trim().toLowerCase();
  const kind = String(fileKind || '').trim().toLowerCase();
  const ext = fileExtension(title);
  return kind === 'pdf' || mime === 'application/pdf' || ext === 'pdf';
}

function isHtmlArtifact(mimeType: string, fileKind: string, title: string): boolean {
  const mime = String(mimeType || '').trim().toLowerCase();
  const kind = String(fileKind || '').trim().toLowerCase();
  const ext = fileExtension(title);
  return kind === 'html' ||
    mime === 'text/html' ||
    mime === 'application/xhtml+xml' ||
    ext === 'html' ||
    ext === 'htm';
}

function isSpreadsheetArtifact(mimeType: string, fileKind: string, title: string): boolean {
  const mime = String(mimeType || '').trim().toLowerCase();
  const kind = String(fileKind || '').trim().toLowerCase();
  const ext = fileExtension(title);
  return kind === 'spreadsheet' ||
    kind === 'csv' ||
    mime.includes('spreadsheet') ||
    mime === 'application/vnd.ms-excel' ||
    ['xls', 'xlsx', 'ods', 'numbers'].includes(ext);
}

function isNativeSpreadsheetContainer(mimeType: string, fileKind: string, title: string): boolean {
  const mime = String(mimeType || '').trim().toLowerCase();
  const kind = String(fileKind || '').trim().toLowerCase();
  const ext = fileExtension(title);
  return (
    kind === 'spreadsheet' ||
    mime.includes('spreadsheet') ||
    mime === 'application/vnd.ms-excel' ||
    mime === 'application/vnd.oasis.opendocument.spreadsheet' ||
    ['xls', 'xlsx', 'ods', 'numbers'].includes(ext)
  );
}

function isPresentationArtifact(mimeType: string, fileKind: string, title: string): boolean {
  const mime = String(mimeType || '').trim().toLowerCase();
  const kind = String(fileKind || '').trim().toLowerCase();
  const ext = fileExtension(title);
  return (
    kind === 'presentation' ||
    mime.includes('presentation') ||
    mime.includes('powerpoint') ||
    mime.includes('keynote') ||
    mime === 'application/vnd.oasis.opendocument.presentation' ||
    ['ppt', 'pptx', 'key', 'odp'].includes(ext)
  );
}

function isDocumentArtifact(mimeType: string, fileKind: string, title: string): boolean {
  const mime = String(mimeType || '').trim().toLowerCase();
  const kind = String(fileKind || '').trim().toLowerCase();
  const ext = fileExtension(title);
  return (
    kind === 'document' ||
    mime === 'application/msword' ||
    mime.includes('wordprocessingml.document') ||
    mime === 'application/vnd.oasis.opendocument.text' ||
    mime.includes('iwork-pages') ||
    ['doc', 'docx', 'odt', 'pages'].includes(ext)
  );
}

function shouldSuppressExtractedTextPreview(mimeType: string, fileKind: string, title: string): boolean {
  if (shouldHideTextPreviewFallback(mimeType, fileKind, title)) return true;
  if (isHtmlArtifact(mimeType, fileKind, title)) return true;
  if (isNativeSpreadsheetContainer(mimeType, fileKind, title)) return true;
  if (isPresentationArtifact(mimeType, fileKind, title)) return true;
  if (isDocumentArtifact(mimeType, fileKind, title)) return true;
  return false;
}

export function fileArtifactCardPresentation(artifact: Artifact): FileArtifactCardPresentation {
  const metadata = (artifact.metadata || {}) as Record<string, any>;
  const mimeType = fileArtifactMimeType(artifact);
  const fileKind = fileArtifactFileKind(artifact);
  const sizeBytes = firstNumber(metadata.sizeBytes, metadata.size_bytes);
  const previewAvailableRaw = firstBool(metadata.previewAvailable, metadata.preview_available);
  const previewTruncated = firstBool(metadata.previewTruncated, metadata.preview_truncated);
  const explicitPreviewText = firstString(metadata.previewText, metadata.preview_text);
  const explicitContentNote = firstString(metadata.contentNote, metadata.content_note);
  const contentFallback = typeof artifact.content === 'string' ? artifact.content.trim() : '';
  const hideTextPreviewFallback = shouldHideTextPreviewFallback(mimeType, fileKind, artifact.title || '');
  const rawThumbnailUrl = fileArtifactThumbnailUrl(artifact);
  const renderedPreviewUrl = fileArtifactRenderedPreviewUrl(artifact);
  const pdfPreviewUrl = isPdfArtifact(mimeType, fileKind, artifact.title || '')
    ? artifactRawUrl(artifact.id)
    : '';
  const htmlPreviewUrl = isHtmlArtifact(mimeType, fileKind, artifact.title || '')
    ? artifactRawUrl(artifact.id)
    : '';
  const htmlPreviewContent = isHtmlArtifact(mimeType, fileKind, artifact.title || '') && typeof artifact.content === 'string'
    ? artifact.content.trim()
    : '';
  const spreadsheetPreviewUrl = isSpreadsheetArtifact(mimeType, fileKind, artifact.title || '')
    ? fileArtifactSpreadsheetPreviewUrls(artifact)[0] || ''
    : '';
  const suppressExtractedTextPreview = shouldSuppressExtractedTextPreview(mimeType, fileKind, artifact.title || '');
  const suppressInlineTextPreview = Boolean(
    rawThumbnailUrl ||
      renderedPreviewUrl ||
      pdfPreviewUrl ||
      htmlPreviewUrl ||
      htmlPreviewContent ||
      spreadsheetPreviewUrl ||
      suppressExtractedTextPreview,
  );
  const canUseContentFallback = !hideTextPreviewFallback &&
    previewAvailableRaw !== false &&
    shouldUseContentAsTextPreview(mimeType, fileKind, artifact.title || '', metadata);
  const previewText = !hideTextPreviewFallback
    ? suppressInlineTextPreview
      ? ''
      : explicitPreviewText || (canUseContentFallback ? contentFallback : '')
    : '';
  const contentNote = explicitContentNote || (
    !hideTextPreviewFallback && !suppressInlineTextPreview && !previewText && contentFallback
      ? contentFallback
      : ''
  );

  const previewAvailable = previewAvailableRaw === true ||
    Boolean(rawThumbnailUrl) ||
    Boolean(renderedPreviewUrl) ||
    Boolean(pdfPreviewUrl) ||
    Boolean(htmlPreviewUrl) ||
    Boolean(htmlPreviewContent) ||
    Boolean(spreadsheetPreviewUrl) ||
    Boolean(previewText);

  return {
    mimeType: mimeType || undefined,
    fileKind: fileKind || undefined,
    sizeBytes,
    previewText: previewText || undefined,
    contentNote: contentNote || undefined,
    previewAvailable: previewAvailable ? true : previewAvailableRaw,
    previewTruncated,
    metadata,
    thumbnailUrl: rawThumbnailUrl,
    thumbnailFetchUrl: rawThumbnailUrl ? undefined : renderedPreviewUrl,
    thumbnailArtifactId: rawThumbnailUrl ? artifact.id : undefined,
    pdfPreviewUrl: pdfPreviewUrl || undefined,
    htmlPreviewUrl: htmlPreviewUrl || undefined,
    htmlPreviewContent: htmlPreviewContent || undefined,
    textPreviewFetchUrl: spreadsheetPreviewUrl || undefined,
  };
}

export function fileAttachmentSnapshotArtifact(attachment: ChatFileAttachment): Artifact | null {
  const artifactId = String(attachment.artifactId || '').trim();
  if (!artifactId) return null;

  const title = String(attachment.fileName || '').trim() || 'Uploaded file';
  const previewText = firstString(attachment.previewText);
  const contentNote = firstString(attachment.contentNote);
  const renderedPreviewPath = extractedPreviewPathFromUrl(artifactId, attachment.thumbnailFetchUrl);
  const metadata: Record<string, any> = {
    tool_name: 'thread_file_attachment',
    source: attachment.source || 'chat_upload',
    partial: true,
    fileName: title,
    file_name: title,
    mimeType: attachment.mimeType,
    detectedMimeType: attachment.detectedMimeType,
    normalizedMimeType: attachment.normalizedMimeType,
    declaredMimeType: attachment.declaredMimeType,
    extensionMimeType: attachment.extensionMimeType,
    sniffedMimeType: attachment.sniffedMimeType,
    fileKind: attachment.fileKind,
    sha256: attachment.sha256,
    providerFileId: attachment.providerFileId,
    providerFileType: attachment.providerFileType,
    capabilities: attachment.capabilities,
    sizeBytes: attachment.sizeBytes,
    isText: attachment.isText,
    previewAvailable: attachment.previewAvailable,
    previewTruncated: attachment.previewTruncated,
    previewText: previewText || undefined,
    contentNote: contentNote || undefined,
  };
  if (renderedPreviewPath) {
    metadata.extraction = {
      previewImagePath: renderedPreviewPath,
      renderedSlides: [renderedPreviewPath],
    };
    if (metadata.previewAvailable !== false) metadata.previewAvailable = true;
  }

  return {
    id: artifactId,
    type: 'file',
    title,
    content: previewText || contentNote || '',
    language: attachment.language,
    metadata,
  };
}

export interface SplitAttachmentArtifacts {
  imageArtifacts: Artifact[];
  fileArtifacts: Artifact[];
  /** Message-level attachment snapshots not yet covered by a hydrated artifact. */
  snapshotAttachments: ChatFileAttachment[];
  otherArtifacts: Artifact[];
}

/**
 * Transcript rendering rule for user/steer messages: hydrated file artifacts
 * win, message snapshots fill the gap until hydration completes (or when it
 * fails), and placeholder chips never appear for known file attachments.
 */
export function splitAttachmentArtifacts(
  referencedArtifacts: Artifact[],
  fileAttachments: ChatFileAttachment[],
): SplitAttachmentArtifacts {
  const imageArtifacts = referencedArtifacts.filter((a) => a.type === 'image');
  const fileArtifacts = referencedArtifacts.filter(
    (a) => a.type === 'file' && a.metadata?.placeholder !== true,
  );
  const hydratedIds = new Set(fileArtifacts.map((a) => a.id));
  const snapshotAttachments = fileAttachments.filter(
    (s) => s.artifactId && !hydratedIds.has(s.artifactId),
  );
  const snapshotIds = new Set(
    fileAttachments.map((s) => s.artifactId).filter(Boolean),
  );
  const otherArtifacts = referencedArtifacts.filter(
    (a) => a.type !== 'image' && a.type !== 'file' && !snapshotIds.has(a.id),
  );
  return { imageArtifacts, fileArtifacts, snapshotAttachments, otherArtifacts };
}
