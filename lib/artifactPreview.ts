import type { Artifact } from '@/types';

function compactText(value: unknown, maxLength = 160): string {
  if (typeof value !== 'string') return '';
  const text = value.replace(/\s+/g, ' ').trim();
  if (!text) return '';
  return text.length > maxLength ? `${text.slice(0, Math.max(0, maxLength - 3))}...` : text;
}

function formatSizeBytes(value: unknown): string {
  const sizeBytes = Number(value);
  if (!Number.isFinite(sizeBytes) || sizeBytes < 0) return '';
  if (sizeBytes < 1024) return `${sizeBytes} B`;
  if (sizeBytes < 1024 * 1024) return `${(sizeBytes / 1024).toFixed(sizeBytes >= 10 * 1024 ? 0 : 1)} KB`;
  return `${(sizeBytes / (1024 * 1024)).toFixed(sizeBytes >= 10 * 1024 * 1024 ? 0 : 1)} MB`;
}

function firstDefined(...values: unknown[]): unknown {
  for (const value of values) {
    if (value !== undefined && value !== null && value !== '') return value;
  }
  return undefined;
}

function buildArtifactReference(artifact: Artifact): string {
  const meta = artifact.metadata || {};
  const title = compactText(artifact.title, 120) || 'Artifact';
  const type = compactText(artifact.type, 40) || 'artifact';
  const lang = compactText(artifact.language, 40);
  const sourceTool = compactText(firstDefined(meta.tool_name, meta.toolName), 80);
  const fullArtifactId = compactText(firstDefined(meta.full_artifact_id, meta.fullArtifactId), 120);
  const rowCount = Number(firstDefined(meta.row_count, meta.rowCount, meta.rows));
  const columnCount = Number(firstDefined(meta.column_count, meta.columnCount));
  const columns = Array.isArray(meta.columns)
    ? meta.columns.map((c: unknown) => compactText(c, 40)).filter(Boolean).slice(0, 12)
    : [];
  const size = formatSizeBytes(firstDefined(meta.sizeBytes, meta.size_bytes));
  const mimeType = compactText(firstDefined(meta.detectedMimeType, meta.detected_mime_type, meta.mimeType, meta.mime_type), 80);
  const parts = [
    `"${title}" (${[type, lang].filter(Boolean).join(' · ')})`,
    `Artifact ID: ${artifact.id}`,
  ];

  if (fullArtifactId && fullArtifactId !== artifact.id) {
    parts.push(`Full artifact ID: ${fullArtifactId}`);
  }
  if (sourceTool) parts.push(`Source tool: ${sourceTool}`);
  if (Number.isFinite(rowCount) && rowCount >= 0) {
    const shape = Number.isFinite(columnCount) && columnCount >= 0
      ? `${rowCount} rows x ${columnCount} columns`
      : `${rowCount} rows`;
    parts.push(`Shape: ${shape}`);
  } else if (Number.isFinite(columnCount) && columnCount >= 0) {
    parts.push(`Columns: ${columnCount}`);
  }
  if (columns.length > 0) parts.push(`Column names: ${columns.join(', ')}`);
  if (mimeType) parts.push(`MIME type: ${mimeType}`);
  if (size) parts.push(`Size: ${size}`);
  parts.push('Content: not inlined. Use the artifact ID with retrieval or paging tools if details are needed.');
  return parts.join('\n');
}

function buildDataPreview(artifact: Artifact): string {
  let rows: Record<string, any>[] = [];
  try {
    const parsed = JSON.parse(artifact.content);
    if (Array.isArray(parsed)) {
      rows = parsed;
    } else if (parsed && typeof parsed === 'object') {
      const keys = Object.keys(parsed);
      if (keys.length === 1 && Array.isArray(parsed[keys[0]])) {
        rows = parsed[keys[0]];
      }
    }
  } catch {
    return artifact.content.slice(0, 500);
  }

  if (rows.length === 0) return '(empty dataset)';

  const columns = Object.keys(rows[0]);
  const colHeader = `| ${columns.join(' | ')} |`;
  const separator = `|${columns.map(() => '---').join('|')}|`;

  const maxRows = rows.length <= 50 ? rows.length : 10;
  const tableRows = rows.slice(0, maxRows).map(
    (row) => `| ${columns.map((c) => String(row[c] ?? '')).join(' | ')} |`
  );

  const header = `${artifact.title ? `"${artifact.title}"` : 'Data'} (data · ${rows.length} rows × ${columns.length} columns: ${columns.join(', ')})`;
  const meta = artifact.metadata?.tool_name ? `Source tool: ${artifact.metadata.tool_name}` : '';

  let table = [colHeader, separator, ...tableRows].join('\n');
  if (rows.length > 50) {
    table += `\n... (${rows.length - 10} more rows in artifact ${artifact.id})`;
  }

  return [header, `Artifact ID: ${artifact.id}`, meta, '', table].filter(Boolean).join('\n');
}

function buildCodePreview(artifact: Artifact): string {
  const lines = artifact.content.split('\n');
  const lang = artifact.language || '';
  const header = `"${artifact.title}" (code${lang ? ` · ${lang}` : ''} · ${lines.length} lines)`;
  const meta = artifact.metadata?.tool_name ? `Source tool: ${artifact.metadata.tool_name}` : '';

  let body: string;
  if (lines.length <= 150) {
    body = '```' + lang + '\n' + artifact.content + '\n```';
  } else {
    const head = lines.slice(0, 50).join('\n');
    const tail = lines.slice(-20).join('\n');
    body =
      '```' + lang + '\n' + head + '\n... (' + (lines.length - 70) + ' lines omitted) ...\n' + tail + '\n```';
  }

  return [header, `Artifact ID: ${artifact.id}`, meta, '', body].filter(Boolean).join('\n');
}

function buildImagePreview(artifact: Artifact): string {
  const meta = artifact.metadata || {};
  const dims = meta.width && meta.height ? `${meta.width} x ${meta.height}` : 'unknown dimensions';
  const format = meta.mimeType?.split('/')[1]?.toUpperCase() || 'unknown format';
  const size = meta.sizeBytes ? `${(meta.sizeBytes / 1024).toFixed(0)} KB` : 'unknown size';
  const header = `"${artifact.title}" (image · ${format} · ${dims} · ${size})`;
  return [header, `Artifact ID: ${artifact.id}`, '', '[Image attached — visible in the conversation UI]'].join('\n');
}

function fileExtension(title: string): string {
  const clean = String(title || '').trim().toLowerCase();
  const dot = clean.lastIndexOf('.');
  if (dot <= 0 || dot === clean.length - 1) return '';
  return clean.slice(dot + 1);
}

function shouldInlineExtractedFilePreview(artifact: Artifact, mimeType: string): boolean {
  const meta = artifact.metadata || {};
  const kind = compactText(firstDefined(meta.fileKind, meta.file_kind), 40).toLowerCase();
  const ext = fileExtension(artifact.title || '');
  const mime = String(mimeType || '').toLowerCase();

  if (kind === 'image' || mime.startsWith('image/')) return false;
  if (kind === 'pdf' || mime === 'application/pdf' || ext === 'pdf') return false;
  if (kind === 'presentation' || kind === 'document') return false;
  if (
    mime.includes('officedocument') ||
    mime === 'application/msword' ||
    mime === 'application/vnd.ms-excel' ||
    mime === 'application/vnd.ms-powerpoint' ||
    ['doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx'].includes(ext)
  ) {
    return false;
  }
  return true;
}

function buildFilePreview(artifact: Artifact): string {
  const meta = artifact.metadata || {};
  const mimeType = String(meta.detectedMimeType || meta.mimeType || 'unknown').trim() || 'unknown';
  const sizeBytes = Number(meta.sizeBytes);
  const size = Number.isFinite(sizeBytes) && sizeBytes >= 0
    ? `${(sizeBytes / 1024).toFixed(sizeBytes >= 1024 ? 0 : 1)} KB`
    : 'unknown size';
  const isText = meta.isText === true ? 'text-like' : 'binary/opaque';
  const preview = String(meta.previewText || artifact.content || '').trim();
  const contentNote = String(meta.contentNote || '').trim();
  const header = `"${artifact.title}" (file · ${mimeType} · ${size} · ${isText})`;

  if (preview && shouldInlineExtractedFilePreview(artifact, mimeType)) {
    return [
      header,
      `Artifact ID: ${artifact.id}`,
      '',
      '```',
      preview,
      meta.previewTruncated === true ? '... (preview truncated)' : '',
      '```',
    ].filter(Boolean).join('\n');
  }

  return [
    header,
    `Artifact ID: ${artifact.id}`,
    '',
    contentNote || '[Original file content not inlined; use native file input or artifact retrieval when available.]',
  ].join('\n');
}

export function buildArtifactContext(artifacts: Artifact[]): string {
  if (artifacts.length === 0) return '';

  const sections = artifacts.map((artifact, i) => {
    const index = `[${i + 1}]`;
    let preview: string;
    if (artifact.type === 'image') {
      preview = buildImagePreview(artifact);
    } else if (artifact.type === 'file') {
      preview = buildFilePreview(artifact);
    } else if (artifact.type === 'data') {
      preview = buildDataPreview(artifact);
    } else {
      preview = buildCodePreview(artifact);
    }
    return `${index} ${preview}`;
  });

  return ['---', '📎 Referenced Artifacts', '---', '', ...sections].join('\n');
}

export function buildArtifactReferenceContext(artifacts: Artifact[]): string {
  if (artifacts.length === 0) return '';

  const sections = artifacts.map((artifact, i) => {
    const index = `[${i + 1}]`;
    return `${index} ${buildArtifactReference(artifact)}`;
  });

  return [
    '---',
    '📎 Referenced Artifacts',
    '---',
    '',
    'Compact references only; artifact content is intentionally not included in this prompt.',
    '',
    ...sections,
  ].join('\n');
}
