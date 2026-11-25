import React from 'react';
import { FileText, Loader2 } from 'lucide-react';
import PdfCanvasViewer from '@/components/arxiv/PdfCanvasViewer';
import '@/components/arxiv/arxivViewer.css';
import FileAttachmentCard from '@/components/chat/FileAttachmentCard';
import MarkdownRenderer from '@/components/chat/MarkdownRenderer';
import CopyButton from '@/components/renderers/shared/CopyButton';
import RawViewer from '@/components/renderers/shared/RawViewer';
import {
  fileArtifactCardPresentation,
  fileArtifactRenderedPreviewUrls,
  fileArtifactSpreadsheetPreviewUrls,
  fileArtifactSpreadsheetWorkbookPreviewUrls,
} from '@/lib/attachmentPresentation';
import {
  artifactRawUrl,
  canDownloadArtifact,
  downloadArtifact,
  getArtifactDownloadFileName,
  isRawDownloadArtifact,
} from '@/lib/artifactDownload';
import { fileArtifactFileKind, fileArtifactMimeType } from '@/lib/fileProvenance';
import { SANDBOXED_HTML_SANDBOX_POLICY } from '@/types';
import { registerTypeRenderer } from '../registry';
import type { RendererProps } from '../types';
import SpreadsheetWorkbookPreview from './SpreadsheetWorkbookPreview';

type Artifact = RendererProps['artifact'];

function firstString(...values: unknown[]): string {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return '';
}

function fileExtension(title: string): string {
  const clean = String(title || '').trim().toLowerCase();
  const dot = clean.lastIndexOf('.');
  if (dot <= 0 || dot === clean.length - 1) return '';
  return clean.slice(dot + 1);
}

function fileMimeType(artifact: Artifact): string {
  return fileArtifactMimeType(artifact).toLowerCase();
}

function fileKind(artifact: Artifact): string {
  return fileArtifactFileKind(artifact).toLowerCase();
}

function isImageFile(artifact: Artifact): boolean {
  const mime = fileMimeType(artifact);
  const ext = fileExtension(artifact.title || '');
  return fileKind(artifact) === 'image' ||
    mime.startsWith('image/') ||
    artifact.type === 'image' ||
    ['png', 'jpg', 'jpeg', 'gif', 'webp'].includes(ext);
}

function isPdfFile(artifact: Artifact): boolean {
  const mime = fileMimeType(artifact);
  return fileKind(artifact) === 'pdf' ||
    mime === 'application/pdf' ||
    fileExtension(artifact.title || '') === 'pdf';
}

function isSpreadsheetFile(artifact: Artifact): boolean {
  const mime = fileMimeType(artifact);
  const kind = fileKind(artifact);
  const ext = fileExtension(artifact.title || '');
  return (
    kind === 'spreadsheet' ||
    kind === 'csv' ||
    kind === 'tabular' ||
    mime.includes('spreadsheet') ||
    mime === 'application/vnd.ms-excel' ||
    mime === 'application/vnd.oasis.opendocument.spreadsheet' ||
    mime.includes('numbers') ||
    mime === 'text/csv' ||
    mime === 'application/csv' ||
    mime === 'text/tab-separated-values' ||
    ['xls', 'xlsx', 'csv', 'tsv', 'tab', 'ods', 'numbers'].includes(ext)
  );
}

function isPresentationFile(artifact: Artifact): boolean {
  const mime = fileMimeType(artifact);
  const kind = fileKind(artifact);
  const ext = fileExtension(artifact.title || '');
  return (
    kind === 'presentation' ||
    mime.includes('presentation') ||
    mime.includes('powerpoint') ||
    mime.includes('keynote') ||
    ['ppt', 'pptx', 'key', 'odp'].includes(ext)
  );
}

function isHtmlFile(artifact: Artifact): boolean {
  const mime = fileMimeType(artifact);
  const kind = fileKind(artifact);
  const ext = fileExtension(artifact.title || '');
  return kind === 'html' || mime === 'text/html' || mime === 'application/xhtml+xml' || ext === 'html' || ext === 'htm';
}

function isMarkdownFile(artifact: Artifact): boolean {
  const mime = fileMimeType(artifact);
  const kind = fileKind(artifact);
  const ext = fileExtension(artifact.title || '');
  return kind === 'markdown' || mime === 'text/markdown' || ext === 'md' || ext === 'markdown';
}

function isTextLikeFile(artifact: Artifact): boolean {
  const mime = fileMimeType(artifact);
  const kind = fileKind(artifact);
  const ext = fileExtension(artifact.title || '');
  return (
    kind === 'text' ||
    kind === 'json' ||
    kind === 'csv' ||
    kind === 'code' ||
    mime.startsWith('text/') ||
    mime.includes('json') ||
    // Exact/suffix XML only: OOXML container MIMEs (docx/xlsx/pptx,
    // "application/vnd.openxmlformats-...") contain "xml" but are zip
    // binaries; a substring match would render them as garbage text and
    // starve the presentation/fallback branches below.
    mime === 'application/xml' ||
    mime.endsWith('+xml') ||
    mime === 'application/x-yaml' ||
    mime === 'application/yaml' ||
    mime === 'application/toml' ||
    mime === 'application/javascript' ||
    mime === 'application/x-javascript' ||
    mime === 'application/x-sh' ||
    [
      'txt', 'log', 'csv', 'tsv', 'tab', 'json', 'jsonl', 'ndjson', 'xml', 'svg',
      'yaml', 'yml', 'toml', 'ini', 'cfg', 'conf', 'env', 'properties', 'sql',
      'go', 'js', 'jsx', 'ts', 'tsx', 'py', 'rb', 'rs', 'java', 'kt', 'swift',
      'c', 'h', 'cpp', 'hpp', 'cs', 'php', 'css', 'scss', 'less', 'sh', 'bash',
      'zsh', 'proto', 'graphql', 'gql', 'vue', 'svelte',
    ].includes(ext)
  );
}

function languageForTextFile(artifact: Artifact, mode: 'markdown' | 'text'): string {
  if (mode === 'markdown') return 'markdown';
  const meta = artifact.metadata || {};
  const hinted = firstString(artifact.language, meta.language);
  if (hinted) return hinted;
  const ext = fileExtension(artifact.title || '');
  switch (ext) {
    case 'json':
    case 'jsonl':
    case 'ndjson':
      return 'json';
    case 'xml':
    case 'svg':
      return 'xml';
    case 'yaml':
    case 'yml':
      return 'yaml';
    case 'toml':
      return 'toml';
    case 'csv':
      return 'csv';
    case 'tsv':
    case 'tab':
      return 'tsv';
    default:
      return ext || 'text';
  }
}

const RAW_HTML_CSP = [
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

function rawHtmlDocument(html: string): string {
  const csp = `<meta http-equiv="Content-Security-Policy" content="${RAW_HTML_CSP}">`;
  const trimmed = html.trim();
  if (!trimmed) return `<!doctype html><html><head>${csp}</head><body></body></html>`;
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

const RawFilePreview: React.FC<{ artifact: Artifact; mode: 'image' | 'pdf' }> = ({ artifact, mode }) => {
  const [objectUrl, setObjectUrl] = React.useState<string | null>(null);
  const [pdfData, setPdfData] = React.useState<Uint8Array | null>(null);
  const [error, setError] = React.useState<string | null>(null);

  React.useEffect(() => {
    let cancelled = false;
    let nextObjectUrl: string | null = null;
    const controller = new AbortController();

    setObjectUrl(null);
    setPdfData(null);
    setError(null);

    (async () => {
      try {
        const response = await fetch(artifactRawUrl(artifact.id), {
          credentials: 'same-origin',
          signal: controller.signal,
        });
        if (!response.ok) throw new Error(`Preview failed (HTTP ${response.status})`);
        if (mode === 'image') {
          const blob = await response.blob();
          if (cancelled) return;
          nextObjectUrl = URL.createObjectURL(blob);
          setObjectUrl(nextObjectUrl);
          return;
        }
        const buffer = await response.arrayBuffer();
        if (cancelled) return;
        setPdfData(new Uint8Array(buffer));
      } catch (err: any) {
        if (!cancelled && err?.name !== 'AbortError') {
          setError('Preview unavailable. Download the original file from the header.');
        }
      }
    })();

    return () => {
      cancelled = true;
      controller.abort();
      if (nextObjectUrl) URL.revokeObjectURL(nextObjectUrl);
    };
  }, [artifact.id, mode]);

  if (error) {
    return <FileMetadataState artifact={artifact} tone="warning" message={error} />;
  }

  if (mode === 'image' && objectUrl) {
    return (
      <div className="flex h-full items-center justify-center overflow-auto rounded-lg border border-border bg-muted/20 p-4">
        <img src={objectUrl} alt={artifact.title} className="max-h-full max-w-full rounded-lg object-contain shadow-md" />
      </div>
    );
  }

  if (mode === 'pdf' && pdfData) {
    return (
      <div className="axv flex h-full flex-col overflow-hidden rounded-lg border border-border bg-background p-3">
        <PdfCanvasViewer data={pdfData} title={artifact.title || 'PDF'} />
      </div>
    );
  }

  return (
    // role="status": the fetch happens after the viewer opens, so without a
    // live region a screen-reader user hears the header and then nothing.
    <div role="status" className="flex h-full items-center justify-center rounded-lg border border-border bg-muted/20 text-sm text-muted-foreground">
      <Loader2 size={16} className="mr-2 motion-safe:animate-spin" aria-hidden="true" />
      Loading preview...
    </div>
  );
};

const RichMarkdownFilePreview: React.FC<{ content: string }> = ({ content }) => (
  <div
    className="flex h-full flex-col overflow-hidden rounded-lg border border-border bg-background"
    data-file-preview-kind="markdown-rendered"
  >
    <div className="flex shrink-0 items-center justify-between border-b border-border bg-muted/50 px-3 py-1.5">
      <span className="font-mono text-[10px] uppercase text-muted-foreground">Markdown</span>
      <CopyButton text={content} />
    </div>
    <div className="min-h-0 flex-1 overflow-auto px-5 py-4 text-sm">
      <MarkdownRenderer content={content} />
    </div>
  </div>
);

const RawTextFilePreview: React.FC<{ artifact: Artifact; mode: 'html' | 'markdown' | 'text' }> = ({ artifact, mode }) => {
  const [content, setContent] = React.useState('');
  const [loaded, setLoaded] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);

  React.useEffect(() => {
    let cancelled = false;
    const controller = new AbortController();

    setContent('');
    setLoaded(false);
    setError(null);

    (async () => {
      try {
        const response = await fetch(artifactRawUrl(artifact.id), {
          credentials: 'same-origin',
          signal: controller.signal,
        });
        if (!response.ok) throw new Error(`Preview failed (HTTP ${response.status})`);
        const text = await response.text();
        if (cancelled) return;
        setContent(text);
        setLoaded(true);
      } catch (err: any) {
        if (!cancelled && err?.name !== 'AbortError') {
          setError('Preview unavailable. Download the original file from the header.');
        }
      }
    })();

    return () => {
      cancelled = true;
      controller.abort();
    };
  }, [artifact.id]);

  if (error) {
    return <FileMetadataState artifact={artifact} tone="warning" message={error} />;
  }

  if (!loaded) {
    return (
      <div role="status" className="flex h-full items-center justify-center rounded-lg border border-border bg-muted/20 text-sm text-muted-foreground">
        <Loader2 size={16} className="mr-2 motion-safe:animate-spin" aria-hidden="true" />
        Loading preview...
      </div>
    );
  }

  if (mode === 'html') {
    const srcDoc = rawHtmlDocument(content);
    return (
      <div className="h-full overflow-hidden rounded-lg border border-border bg-white">
        <iframe
          title={`HTML preview of ${artifact.title || 'HTML file'}`}
          srcDoc={srcDoc}
          sandbox={SANDBOXED_HTML_SANDBOX_POLICY}
          referrerPolicy="no-referrer"
          className="h-full w-full border-0 bg-white"
          data-file-preview-kind="html-full"
        />
      </div>
    );
  }

  if (mode === 'markdown') {
    return <RichMarkdownFilePreview content={content} />;
  }

  return (
    <div className="h-full" data-file-preview-kind={`${mode}-full`}>
      <RawViewer
        content={content}
        language={languageForTextFile(artifact, mode)}
        className="h-full"
      />
    </div>
  );
};

const InlineTextFilePreview: React.FC<{ artifact: Artifact; mode: 'html' | 'markdown' | 'text' }> = ({ artifact, mode }) => {
  const content = typeof artifact.content === 'string' ? artifact.content : '';

  if (mode === 'html') {
    const srcDoc = rawHtmlDocument(content);
    return (
      <div className="h-full overflow-hidden rounded-lg border border-border bg-white">
        <iframe
          title={`HTML preview of ${artifact.title || 'HTML file'}`}
          srcDoc={srcDoc}
          sandbox={SANDBOXED_HTML_SANDBOX_POLICY}
          referrerPolicy="no-referrer"
          className="h-full w-full border-0 bg-white"
          data-file-preview-kind="html-full"
        />
      </div>
    );
  }

  if (mode === 'markdown') {
    return <RichMarkdownFilePreview content={content} />;
  }

  return (
    <div className="h-full" data-file-preview-kind="text-full">
      <RawViewer
        content={content}
        language={languageForTextFile(artifact, mode)}
        className="h-full"
      />
    </div>
  );
};

const FileMetadataState: React.FC<{
  artifact: Artifact;
  tone?: 'default' | 'warning';
  message?: string;
}> = ({ artifact, tone = 'default', message }) => {
  const presentation = fileArtifactCardPresentation(artifact);
  const rawDownload = isRawDownloadArtifact(artifact);
  const contentDownload = !rawDownload && canDownloadArtifact(artifact);
  const fallbackMessage = message || (
    rawDownload
      ? 'A reliable visual preview is not available for this file type. Download the original file to view it in its native app.'
      : 'Preview unavailable. Download the file to view it in its native app.'
  );

  return (
    // Warning tone replaces a loading live region, so it must announce too;
    // the default tone is static fallback content, not a state change.
    <div
      role={tone === 'warning' ? 'status' : undefined}
      className={`flex h-full items-center justify-center overflow-auto rounded-lg bg-muted/20 p-4 ${
        tone === 'warning' ? 'text-destructive' : ''
      }`}
    >
      <FileAttachmentCard
        title={artifact.title || 'Uploaded file'}
        mimeType={presentation.mimeType}
        fileKind={presentation.fileKind}
        sizeBytes={presentation.sizeBytes}
        previewText={presentation.previewText}
        contentNote={presentation.contentNote || fallbackMessage}
        previewAvailable={presentation.previewAvailable}
        previewTruncated={presentation.previewTruncated}
        metadata={presentation.metadata}
        thumbnailUrl={presentation.thumbnailUrl}
        thumbnailFetchUrl={presentation.thumbnailFetchUrl}
        thumbnailArtifactId={presentation.thumbnailArtifactId}
        pdfPreviewUrl={presentation.pdfPreviewUrl}
        htmlPreviewUrl={presentation.htmlPreviewUrl}
        htmlPreviewContent={presentation.htmlPreviewContent}
        textPreviewFetchUrl={presentation.textPreviewFetchUrl}
        downloadArtifactId={rawDownload ? artifact.id : undefined}
        downloadFileName={rawDownload ? getArtifactDownloadFileName(artifact) : undefined}
        onDownload={contentDownload ? () => downloadArtifact(artifact) : undefined}
        className="w-full max-w-xl"
      />
    </div>
  );
};

const RenderedSlideDeckPreview: React.FC<{
  artifact: Artifact;
  urls: string[];
}> = ({ artifact, urls }) => {
  const [objectUrls, setObjectUrls] = React.useState<string[]>([]);
  const [error, setError] = React.useState<string | null>(null);

  React.useEffect(() => {
    let cancelled = false;
    const controller = new AbortController();
    const createdUrls: string[] = [];

    setObjectUrls([]);
    setError(null);

    (async () => {
      try {
        const loaded = await Promise.all(urls.map(async (url) => {
          const response = await fetch(url, {
            credentials: 'same-origin',
            signal: controller.signal,
          });
          if (!response.ok) throw new Error(`Slide preview failed (HTTP ${response.status})`);
          const blob = await response.blob();
          const objectUrl = URL.createObjectURL(blob);
          createdUrls.push(objectUrl);
          return objectUrl;
        }));
        if (!cancelled) setObjectUrls(loaded);
      } catch (err: any) {
        if (!cancelled && err?.name !== 'AbortError') {
          setError('Slide preview unavailable. Download the original presentation from the header.');
        }
      }
    })();

    return () => {
      cancelled = true;
      controller.abort();
      for (const objectUrl of createdUrls) {
        URL.revokeObjectURL(objectUrl);
      }
    };
  }, [urls.join('\n')]);

  if (error) {
    return <FileMetadataState artifact={artifact} tone="warning" message={error} />;
  }

  if (objectUrls.length === 0) {
    return (
      <div role="status" className="flex h-full items-center justify-center rounded-lg border border-border bg-muted/20 text-sm text-muted-foreground">
        <Loader2 size={16} className="mr-2 motion-safe:animate-spin" aria-hidden="true" />
        Loading slide preview...
      </div>
    );
  }

  return (
    <div className="h-full overflow-auto rounded-lg border border-border bg-muted/20 p-4">
      <div className="mx-auto flex max-w-5xl flex-col gap-4">
        {objectUrls.map((url, index) => (
          <figure key={`${url}-${index}`} className="overflow-hidden rounded-lg border border-border bg-background shadow-sm">
            <img
              src={url}
              alt={`${artifact.title || 'Presentation'} slide ${index + 1}`}
              className="block w-full bg-white object-contain"
              loading={index === 0 ? 'eager' : 'lazy'}
            />
            <figcaption className="border-t border-border bg-card px-3 py-1.5 text-[11px] text-muted-foreground">
              Slide {index + 1}
            </figcaption>
          </figure>
        ))}
      </div>
    </div>
  );
};

const FileRenderer: React.FC<RendererProps> = ({ artifact }) => {
  const rawUpload: boolean = isRawDownloadArtifact(artifact);
  const renderedSlideUrls = fileArtifactRenderedPreviewUrls(artifact);
  const markdownSheetUrls = fileArtifactSpreadsheetPreviewUrls(artifact);
  const spreadsheetWorkbookPreviewUrls = fileArtifactSpreadsheetWorkbookPreviewUrls(artifact);

  if (rawUpload && isImageFile(artifact)) {
    return <RawFilePreview artifact={artifact} mode="image" />;
  }

  if (rawUpload && isPdfFile(artifact)) {
    return <RawFilePreview artifact={artifact} mode="pdf" />;
  }

  if (rawUpload && isHtmlFile(artifact)) {
    return <RawTextFilePreview artifact={artifact} mode="html" />;
  }

  if (rawUpload && isMarkdownFile(artifact)) {
    return <RawTextFilePreview artifact={artifact} mode="markdown" />;
  }

  // Before the text branches: CSV/TSV counts as text-like, so a spreadsheet
  // with generated sheet previews must claim the artifact here or it would
  // always render as raw text. Without preview URLs it still falls through.
  if (
    isSpreadsheetFile(artifact) &&
    (spreadsheetWorkbookPreviewUrls.workbookUrl || spreadsheetWorkbookPreviewUrls.sheetUrls.length > 0 || markdownSheetUrls.length > 0)
  ) {
    return (
      <SpreadsheetWorkbookPreview
        artifact={artifact}
        workbookUrl={spreadsheetWorkbookPreviewUrls.workbookUrl}
        sheetUrls={spreadsheetWorkbookPreviewUrls.sheetUrls}
        markdownUrls={markdownSheetUrls}
      />
    );
  }

  if (rawUpload && isTextLikeFile(artifact)) {
    return <RawTextFilePreview artifact={artifact} mode="text" />;
  }

  if (!rawUpload && isHtmlFile(artifact) && typeof artifact.content === 'string') {
    return <InlineTextFilePreview artifact={artifact} mode="html" />;
  }

  if (!rawUpload && isMarkdownFile(artifact) && typeof artifact.content === 'string') {
    return <InlineTextFilePreview artifact={artifact} mode="markdown" />;
  }

  if (!rawUpload && isTextLikeFile(artifact) && typeof artifact.content === 'string') {
    return <InlineTextFilePreview artifact={artifact} mode="text" />;
  }

  if (isPresentationFile(artifact) && renderedSlideUrls.length > 0) {
    return <RenderedSlideDeckPreview artifact={artifact} urls={renderedSlideUrls} />;
  }

  return <FileMetadataState artifact={artifact} />;
};

registerTypeRenderer('file', {
  component: FileRenderer,
  label: 'File',
  icon: FileText,
});

export default FileRenderer;
