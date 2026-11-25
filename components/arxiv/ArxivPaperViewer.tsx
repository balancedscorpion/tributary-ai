import React from 'react';
import { toast } from 'sonner';
import type { ArxivPaper } from '@/services/arxivApi';
import {
  ArxivApiError,
  DESKTOP_AUTH_REQUIRED_MESSAGE,
  fetchArxivPaperPdfBlob,
  fetchArxivPaperPdfFile,
  fetchArxivPaperTextFile,
  getArxivPaper,
  getArxivPaperText,
  isDesktopAuthRequiredError,
  preloadArxivPaperPdfBlob,
} from '@/services/arxivApi';
import { openExternalUrl } from '@/services/openExternal';
import { useChatActions } from '@/components/chat/ChatActionsContext';
import { useArxivHighlights } from './useArxivHighlights';
import { formatHighlightForChat, safeArxivUrl } from './arxivContentSafety';
import { buildUtf8OffsetMap } from './utf8Offsets';
import MathText, { truncateMathText } from './MathText';
import SourceMathText from './SourceMathText';
import { sourceRangeFromDomRange } from './selectionOffsets';
import PdfCanvasViewer from './PdfCanvasViewer';
import './arxivViewer.css';

// Window size for the text API's max_chars parameter, which the server
// measures in UTF-8 bytes (windows are rune-aligned, so never mid-character).
const TEXT_WINDOW_CHARS = 200000;

export interface ArxivPaperViewerProps {
  paperKey?: string;
  /** Metadata for an unsaved paper preview. Preview mode never persists paper state. */
  previewPaper?: ArxivPaper;
  /** Fetches temporary PDF bytes for preview mode. */
  previewPdfLoader?: () => Promise<Blob>;
  initialMode?: 'text' | 'pdf';
  /** Open the text view at this server text offset (deep link from excerpt cards). */
  initialOffset?: number;
  /** Validated evidence spans to show transiently without saving user highlights. */
  transientEvidence?: ArxivTransientEvidence[];
  /** Overrides ChatActionsContext.attachFile when provided. */
  onAttachFile?: (file: File) => void | Promise<void>;
  /** Overrides ChatActionsContext.insertDraftText when provided. */
  onInsertDraftText?: (text: string) => void;
  /** Renders a back/close affordance when provided. */
  onRequestClose?: () => void;
  /** Explicit save action shown in preview mode. */
  onSavePreview?: () => void | Promise<void>;
  savePreviewBusy?: boolean;
  className?: string;
}

export interface ArxivTransientEvidence {
  sourceField: 'title' | 'summary' | 'paper_text';
  start: number;
  end: number;
  quote?: string;
  label?: string;
}

const EMPTY_TRANSIENT_EVIDENCE: ArxivTransientEvidence[] = [];

function formatDate(value: string | null): string {
  if (!value) return '';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

function authorsPreview(authors: string[]): string {
  if (authors.length === 0) return 'Unknown authors';
  if (authors.length <= 3) return authors.join(', ');
  return `${authors.slice(0, 3).join(', ')} +${authors.length - 3}`;
}

function findReadableBodyStart(text: string, hasMetadataAbstract: boolean): number {
  if (!hasMetadataAbstract) return 0;
  // arXiv metadata provides a cleaner abstract than many PDF text streams.
  // Keep the selectable extracted text focused on the body when a recognizable
  // introduction heading appears near the start of the paper.
  const head = text.slice(0, Math.min(text.length, 20000));
  const match = /(?:^|\n)(?:\s*(?:1|I)\.?\s+)?Introduction\b/i.exec(head);
  return match ? match.index + (match[0].startsWith('\n') ? 1 : 0) : 0;
}

function errorMessage(err: unknown, fallback: string): string {
  // Backend desktop-auth guard (401 DESKTOP_TOKEN_REQUIRED): this window was
  // opened without the launcher's token, so tell the user how to recover.
  if (isDesktopAuthRequiredError(err)) {
    return DESKTOP_AUTH_REQUIRED_MESSAGE;
  }
  if (err instanceof ArxivApiError) {
    if (err.status === 429 || err.status === 503) {
      return 'arXiv allows about one request every 3 seconds — please retry in a moment.';
    }
    return err.message || fallback;
  }
  if (err instanceof Error && err.message) return err.message;
  return fallback;
}

function renderMathTextWithEvidence(
  text: string,
  evidence: ArxivTransientEvidence[],
  inlineOnly = false,
): React.ReactNode {
  if (!text || evidence.length === 0) return <MathText text={text} inlineOnly={inlineOnly} />;
  const offsets = buildUtf8OffsetMap(text);
  const ranges = evidence
    .filter((item) => item.end > item.start)
    .map((item) => ({
      start: offsets.byteToJs(Math.max(0, Math.min(item.start, offsets.byteLength))),
      end: offsets.byteToJs(Math.max(0, Math.min(item.end, offsets.byteLength))),
    }))
    .filter((item) => item.end > item.start)
    .sort((a, b) => a.start - b.start);
  if (ranges.length === 0) return <MathText text={text} inlineOnly={inlineOnly} />;

  const pieces: React.ReactNode[] = [];
  let cursor = 0;
  ranges.forEach((range, index) => {
    const start = Math.max(cursor, range.start);
    if (start >= range.end) return;
    if (start > cursor) {
      pieces.push(
        <MathText key={`t-${cursor}`} text={text.slice(cursor, start)} inlineOnly={inlineOnly} />,
      );
    }
    pieces.push(
      <span key={`e-${index}-${start}`} className="axv-mark axv-mark-evidence">
        <MathText text={text.slice(start, range.end)} inlineOnly={inlineOnly} />
      </span>,
    );
    cursor = range.end;
  });
  if (cursor < text.length) {
    pieces.push(<MathText key={`t-${cursor}`} text={text.slice(cursor)} inlineOnly={inlineOnly} />);
  }
  return <>{pieces}</>;
}

/**
 * Shared arXiv paper viewer: extracted-text mode with selection capture,
 * persistent highlights and "send to chat", plus an inline PDF mode for
 * papers whose PDF is stored locally. Self-fetches paper metadata and text by
 * paperKey, so any host (settings sheet, inspector overlay, tool renderers)
 * can mount it with just a key.
 *
 * Chat actions resolve from props first, then from ChatActionsContext; an
 * action is hidden when neither source provides a handler. All paper content
 * is untrusted external data: it renders as plain React text (titles through
 * MathText, which only emits KaTeX-rendered math beyond text nodes), and the
 * only external link goes through safeArxivUrl.
 */
const ArxivPaperViewer: React.FC<ArxivPaperViewerProps> = ({
  paperKey,
  previewPaper,
  previewPdfLoader,
  initialMode = 'pdf',
  initialOffset = 0,
  transientEvidence = EMPTY_TRANSIENT_EVIDENCE,
  onAttachFile,
  onInsertDraftText,
  onRequestClose,
  onSavePreview,
  savePreviewBusy,
  className,
}) => {
  const chatActions = useChatActions();
  const isPreview = Boolean(previewPaper && !paperKey);
  const attachFile = isPreview ? undefined : (onAttachFile ?? chatActions.attachFile);
  const insertDraftText = onInsertDraftText ?? chatActions.insertDraftText;

  const [paper, setPaper] = React.useState<ArxivPaper | null>(null);
  const [paperError, setPaperError] = React.useState<string | null>(null);
  const [mode, setMode] = React.useState<'text' | 'pdf'>(initialMode);

  const [paperText, setPaperText] = React.useState('');
  // Server UTF-8 byte offset at which the loaded text window starts (0 unless
  // deep-linked via initialOffset). Highlight offsets are absolute byte
  // offsets into the extracted text, so rendering and selection capture shift
  // by this base and convert bytes ⇄ JS string indices via textOffsets below.
  const [windowStart, setWindowStart] = React.useState(0);
  // Total size of the extracted text in UTF-8 bytes (the API's total_chars).
  const [textTotalBytes, setTextTotalBytes] = React.useState(0);
  const [textHasMore, setTextHasMore] = React.useState(false);
  // Server-provided continuation offset (UTF-8 bytes); never derived from JS
  // string length, which counts UTF-16 units and would drift on non-ASCII text.
  const [textNextOffset, setTextNextOffset] = React.useState(0);
  const [textLoading, setTextLoading] = React.useState(false);
  const [textError, setTextError] = React.useState<string | null>(null);

  const [selection, setSelection] = React.useState<{ start: number; end: number; quote: string } | null>(null);
  const [attachBusy, setAttachBusy] = React.useState(false);
  // PDF preview cache: fetched lazily through fetch() so desktop auth headers
  // are attached, then rendered by PDF.js instead of a native iframe/plugin.
  const [pdfData, setPdfData] = React.useState<Uint8Array | null>(null);
  const [pdfLoading, setPdfLoading] = React.useState(false);
  const [pdfError, setPdfError] = React.useState<string | null>(null);
  // Bumped by the Retry button to re-run the PDF fetch effect after a failure.
  const [pdfRetryNonce, setPdfRetryNonce] = React.useState(0);
  const previewPdfLoaderRef = React.useRef(previewPdfLoader);

  const textContainerRef = React.useRef<HTMLDivElement>(null);
  const headingRef = React.useRef<HTMLHeadingElement>(null);

  React.useEffect(() => {
    previewPdfLoaderRef.current = previewPdfLoader;
  }, [previewPdfLoader]);

  const { highlights, addHighlight, removeHighlight } = useArxivHighlights(isPreview ? null : paperKey || null);
  const transientTextHighlights = React.useMemo(
    () => transientEvidence
      .filter((item) => item.sourceField === 'paper_text' && item.end > item.start)
      .map((item, index) => ({
        id: `evidence-${index}-${item.start}-${item.end}`,
        start: item.start,
        end: item.end,
        quote: item.quote || '',
      })),
    [transientEvidence],
  );
  const allTextHighlights = React.useMemo(
    () => [...transientTextHighlights, ...highlights],
    [highlights, transientTextHighlights],
  );
  const titleEvidence = React.useMemo(
    () => transientEvidence.filter((item) => item.sourceField === 'title'),
    [transientEvidence],
  );
  const summaryEvidence = React.useMemo(
    () => transientEvidence.filter((item) => item.sourceField === 'summary'),
    [transientEvidence],
  );

  // ---------------------------------------------------------------- loading

  React.useEffect(() => {
    let cancelled = false;
    setPaper(null);
    setPaperError(null);
    setMode(isPreview ? 'pdf' : initialMode);
    setPaperText('');
    setWindowStart(0);
    setTextTotalBytes(0);
    setTextHasMore(false);
    setTextNextOffset(0);
    setTextError(null);
    setSelection(null);
    setPdfData(null);
    setPdfLoading(false);
    setPdfError(null);
    setTextLoading(!isPreview);

    if (isPreview) {
      if (!previewPaper) {
        setPaperError('Failed to load this paper preview.');
        setTextLoading(false);
        return () => {
          cancelled = true;
        };
      }
      setPaper(previewPaper);
      setPaperText('');
      setTextLoading(false);
      window.setTimeout(() => headingRef.current?.focus(), 60);
      return () => {
        cancelled = true;
      };
    }

    if (!paperKey) {
      setPaperError('Paper key is required.');
      setTextLoading(false);
      return () => {
        cancelled = true;
      };
    }
    const storedPaperKey = paperKey;

    (async () => {
      let loaded: ArxivPaper | null = null;
      try {
        loaded = await getArxivPaper(storedPaperKey);
        if (cancelled) return;
        setPaper(loaded);
        if (initialMode === 'pdf' && loaded.pdfState !== 'stored') setMode('text');
      } catch (err) {
        if (cancelled) return;
        setPaperError(errorMessage(err, 'Failed to load this paper.'));
        setTextLoading(false);
        return;
      }
      try {
        const page = await getArxivPaperText(storedPaperKey, {
          offset: Math.max(0, initialOffset),
          maxChars: TEXT_WINDOW_CHARS,
        });
        if (cancelled) return;
        setPaperText(page.text);
        setWindowStart(page.offset);
        setTextTotalBytes(page.totalChars);
        setTextHasMore(page.hasMore);
        setTextNextOffset(page.nextOffset);
      } catch (err) {
        if (cancelled) return;
        setTextError(errorMessage(err, 'Failed to load extracted text for this paper.'));
      } finally {
        if (!cancelled) {
          setTextLoading(false);
          window.setTimeout(() => headingRef.current?.focus(), 60);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
    // initialMode/initialOffset are mount-time hints; only a paper change reloads.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paperKey, isPreview, previewPaper]);

  const loadMoreText = React.useCallback(async () => {
    if (!paperKey || isPreview || !textHasMore || textLoading) return;
    setTextLoading(true);
    try {
      const page = await getArxivPaperText(paperKey, { offset: textNextOffset, maxChars: TEXT_WINDOW_CHARS });
      setPaperText((prev) => prev + page.text);
      setTextHasMore(page.hasMore);
      setTextNextOffset(page.nextOffset);
    } catch (err) {
      toast.error(errorMessage(err, 'Failed to load more text.'));
    } finally {
      setTextLoading(false);
    }
  }, [paperKey, isPreview, textHasMore, textLoading, textNextOffset]);

  // Desktop auth contract: /api/* routes require X-AI-Desktop-Token. That
  // token is attached by the app's fetch wrapper, but native iframe requests
  // bypass fetch and hit the guard unauthenticated. Fetch the PDF bytes here
  // and render them with PDF.js instead of pointing any frame at /api.
  React.useEffect(() => {
    if (mode !== 'pdf') return;
    if (!paper) return;
    const canLoadStoredPDF = Boolean(!isPreview && paperKey && paper.pdfState === 'stored' && paper.paperKey === paperKey);
    const canLoadPreviewPDF = Boolean(isPreview && previewPdfLoader);
    if (!canLoadStoredPDF && !canLoadPreviewPDF) return;
    if (pdfData) return;

    let cancelled = false;
    setPdfLoading(true);
    setPdfError(null);

    (async () => {
      try {
        const loader = previewPdfLoaderRef.current;
        const blob = canLoadPreviewPDF && loader
          ? await loader()
          : await fetchArxivPaperPdfBlob(paperKey || '', { priority: 'interactive' });
        if (cancelled) return;
        const bytes = new Uint8Array(await blob.arrayBuffer());
        if (!cancelled) {
          setPdfData(bytes);
        }
      } catch (err) {
        if (cancelled) return;
        setPdfError(errorMessage(err, 'Failed to load the PDF preview.'));
      } finally {
        if (!cancelled) {
          setPdfLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [
    mode,
    isPreview,
    paperKey,
    paper?.paperKey,
    paper?.pdfState,
    paper?.arxivId,
    paper?.pdfUrl,
    pdfData,
    pdfRetryNonce,
  ]);

  React.useEffect(() => {
    if (isPreview || !paperKey || paper?.pdfState !== 'stored') return;
    preloadArxivPaperPdfBlob(paperKey);
  }, [isPreview, paper?.pdfState, paperKey]);

  const retryPdfLoad = React.useCallback(() => {
    setPdfError(null);
    setPdfData(null);
    setPdfRetryNonce((n) => n + 1);
  }, []);

  // ---------------------------------------------------------------- selection

  // Map between the loaded window's UTF-8 byte offsets (server model) and JS
  // string indices (UTF-16). Identity for ASCII text.
  const textOffsets = React.useMemo(() => buildUtf8OffsetMap(paperText), [paperText]);
  const bodyStart = React.useMemo(
    () => findReadableBodyStart(paperText, Boolean(paper?.summary?.trim())),
    [paper?.summary, paperText],
  );

  // Capture the user's text selection as UTF-8 byte offsets into the extracted
  // text (the server's offset model). The body may render TeX through KaTeX,
  // whose DOM textContent does not match the raw paper source, so selection
  // boundaries are mapped through data-axv-source-* spans emitted by
  // SourceMathText. Math selections snap to the full raw TeX segment.
  const captureSelection = React.useCallback(() => {
    const container = textContainerRef.current;
    const sel = window.getSelection();
    if (!container || !sel || sel.rangeCount === 0 || sel.isCollapsed) {
      setSelection(null);
      return;
    }
    const range = sel.getRangeAt(0);
    if (!container.contains(range.commonAncestorContainer)) {
      setSelection(null);
      return;
    }
    const sourceRange = sourceRangeFromDomRange(container, range);
    if (!sourceRange) {
      setSelection(null);
      return;
    }
    const { start: rawStart, end: rawEnd } = sourceRange;
    const jsStart = Math.max(0, Math.min(rawStart, paperText.length));
    const jsEnd = Math.max(jsStart, Math.min(rawEnd, paperText.length));
    const quote = paperText.slice(jsStart, jsEnd);
    if (!quote.trim()) {
      setSelection(null);
      return;
    }
    const start = windowStart + textOffsets.jsToByte(jsStart);
    const end = windowStart + textOffsets.jsToByte(jsEnd);
    setSelection({ start, end, quote });
  }, [paperText, windowStart, textOffsets]);

  const addHighlightFromSelection = React.useCallback(() => {
    if (!selection || isPreview) return;
    addHighlight({ start: selection.start, end: selection.end, quote: selection.quote });
    setSelection(null);
    window.getSelection()?.removeAllRanges();
    toast.success('Highlight saved.');
  }, [selection, isPreview, addHighlight]);

  // ---------------------------------------------------------------- actions

  const sendQuoteToChat = React.useCallback(
    (quote: string) => {
      if (!paper || !insertDraftText) return;
      insertDraftText(formatHighlightForChat({ title: paper.title, arxivId: paper.arxivId, quote }));
      toast.success('Added to the chat composer.');
    },
    [paper, insertDraftText],
  );

  const attachPaperToChat = React.useCallback(async () => {
    if (!paper || !attachFile) return;
    setAttachBusy(true);
    try {
      // First-class file attachment: the stored PDF (or extracted text when no
      // PDF could be stored) goes through the exact same upload path as a
      // user-picked file, so the model receives it as a normal file attachment.
      const file = paper.pdfState === 'stored'
        ? await fetchArxivPaperPdfFile(paper)
        : await fetchArxivPaperTextFile(paper);
      await Promise.resolve(attachFile(file));
      toast.success(`Attached ${file.name} to the chat composer.`);
    } catch (err) {
      toast.error(errorMessage(err, 'Failed to attach the paper to chat.'));
    } finally {
      setAttachBusy(false);
    }
  }, [paper, attachFile]);

  // ---------------------------------------------------------------- rendering

  // Render the text split at highlight boundaries so highlighted ranges show
  // as <mark> while keeping textContent identical to the fetched text.
  // Highlight offsets are UTF-8 bytes (server model); convert them to JS
  // string indices before slicing so spans stay accurate on non-ASCII text.
  const textSegments = React.useMemo(() => {
    if (!paperText) return [];
    const windowEndByte = windowStart + textOffsets.byteLength;
    const bodyStartByte = windowStart + textOffsets.jsToByte(bodyStart);
    const bounds = allTextHighlights
      .filter((h) => h.end > bodyStartByte && h.start < windowEndByte)
      .map((h) => ({
        ...h,
        start: textOffsets.byteToJs(Math.max(h.start - windowStart, bodyStartByte - windowStart)),
        end: textOffsets.byteToJs(Math.min(h.end - windowStart, textOffsets.byteLength)),
      }))
      .sort((a, b) => a.start - b.start);
    const segments: Array<{ key: string; text: string; highlighted: boolean; start: number; end: number }> = [];
    let cursor = bodyStart;
    for (const h of bounds) {
      const start = Math.max(h.start, cursor);
      if (start >= h.end) continue; // overlapping highlight already covered
      if (start > cursor) {
        segments.push({
          key: `t${cursor}`,
          text: paperText.slice(cursor, start),
          highlighted: false,
          start: cursor,
          end: start,
        });
      }
      segments.push({
        key: h.id,
        text: paperText.slice(start, h.end),
        highlighted: true,
        start,
        end: h.end,
      });
      cursor = h.end;
    }
    if (cursor < paperText.length) {
      segments.push({
        key: `t${cursor}`,
        text: paperText.slice(cursor),
        highlighted: false,
        start: cursor,
        end: paperText.length,
      });
    }
    return segments;
  }, [paperText, allTextHighlights, windowStart, textOffsets, bodyStart]);

  const absUrl = paper ? safeArxivUrl(paper.absUrl) : null;
  const pdfAvailable = isPreview ? Boolean(previewPdfLoader) : paper?.pdfState === 'stored';

  if (paperError) {
    return (
      <div className={`axv${className ? ` ${className}` : ''}`}>
        {onRequestClose ? (
          <div className="axv-head">
            <button type="button" className="axv-link" onClick={onRequestClose}>
              ← Back
            </button>
          </div>
        ) : null}
        <div className="axv-error" role="status">{paperError}</div>
      </div>
    );
  }

  if (!paper) {
    return (
      <div className={`axv${className ? ` ${className}` : ''}`}>
        <div className="axv-loading"><span className="axv-spin" />Loading paper…</div>
      </div>
    );
  }

  return (
    <div className={`axv${className ? ` ${className}` : ''}`}>
      <div className="axv-head">
        {onRequestClose ? (
          <button type="button" className="axv-link" onClick={onRequestClose}>
            ← Back
          </button>
        ) : null}
        <div>
          <h3 ref={headingRef} tabIndex={-1} className="axv-title">
            {renderMathTextWithEvidence(paper.title, titleEvidence)}
          </h3>
          <div className="axv-meta">
            <span className="axv-id">arXiv:{paper.arxivId}{paper.version ? `v${paper.version}` : ''}</span>
            <span>{authorsPreview(paper.authors)}</span>
            {paper.published ? <span>{formatDate(paper.published)}</span> : null}
            {isPreview ? (
              <span className="axv-state-warn">Preview — not saved</span>
            ) : null}
            {paper.extractState === 'metadata_only' ? (
              <span className="axv-state-warn">Metadata only — abstract text shown</span>
            ) : null}
          </div>
        </div>
      </div>

      <div className="axv-toolbar" role="toolbar" aria-label="Paper viewer actions">
        <div className="axv-seg">
          <button
            type="button"
            className={`axv-seg-btn${mode === 'text' ? ' axv-on' : ''}`}
            aria-pressed={mode === 'text'}
            disabled={isPreview}
            onClick={() => setMode('text')}
          >
            Text
          </button>
          <button
            type="button"
            className={`axv-seg-btn${mode === 'pdf' ? ' axv-on' : ''}`}
            aria-pressed={mode === 'pdf'}
            disabled={!pdfAvailable}
            onClick={() => setMode('pdf')}
          >
            PDF
          </button>
        </div>
        <div className="axv-toolbar-actions">
          {isPreview && onSavePreview ? (
            <button
              type="button"
              className="axv-btn-primary"
              disabled={savePreviewBusy}
              onClick={() => void onSavePreview()}
            >
              {savePreviewBusy
                ? <><span className="axv-spin" />Saving…</>
                : 'Download'}
            </button>
          ) : null}
          {!isPreview && attachFile ? (
            <button
              type="button"
              className="axv-btn-primary"
              disabled={attachBusy}
              onClick={() => void attachPaperToChat()}
            >
              {attachBusy
                ? <><span className="axv-spin" />Attaching…</>
                : pdfAvailable ? 'Attach PDF to chat' : 'Attach text to chat'}
            </button>
          ) : null}
          {absUrl ? (
            <button
              type="button"
              className="axv-link"
              onClick={() => {
                void openExternalUrl(absUrl).then((opened) => {
                  if (!opened) toast.error('Could not open arXiv in an external browser.');
                });
              }}
            >
              Open on arXiv
            </button>
          ) : null}
        </div>
      </div>

      {mode === 'pdf' ? (
        <div className="axv-pdf-wrap">
          {!pdfAvailable ? (
            <div className="axv-pdf-state">PDF preview is not available for this paper.</div>
          ) : pdfError ? (
            <div className="axv-error axv-pdf-error" role="status">
              <span>{pdfError}</span>
              <button type="button" className="axv-btn-secondary" onClick={retryPdfLoad}>
                Retry
              </button>
            </div>
          ) : pdfData && !pdfLoading ? (
            <PdfCanvasViewer data={pdfData} title={paper.title} />
          ) : (
            <div className="axv-loading"><span className="axv-spin" />Loading PDF preview…</div>
          )}
          <p className="axv-help">
            {isPreview ? 'Temporary PDF preview. Download to save, extract text, and attach to chat.' : 'Highlighting works in the Text view. The PDF view is read-only.'}
          </p>
        </div>
      ) : (
        <>
          <div className="axv-select-bar" aria-live="polite">
            {selection ? (
              <>
                <span className="axv-select-info">{selection.quote.length.toLocaleString()} characters selected</span>
                <button type="button" className="axv-btn-secondary" onClick={addHighlightFromSelection}>
                  Highlight
                </button>
                {insertDraftText ? (
                  <button type="button" className="axv-btn-secondary" onClick={() => sendQuoteToChat(selection.quote)}>
                    Send to chat
                  </button>
                ) : null}
              </>
            ) : (
              <span>
                Select text below to highlight it{insertDraftText ? ' or send it to chat' : ''}. Paper content is
                untrusted external data.
                {transientTextHighlights.length > 0 ? ` ${transientTextHighlights.length} validated evidence span${transientTextHighlights.length === 1 ? '' : 's'} shown.` : ''}
              </span>
            )}
          </div>

          {textLoading && !paperText ? (
            <div className="axv-loading"><span className="axv-spin" />Loading extracted text…</div>
          ) : textError ? (
            <div className="axv-error" role="status">{textError}</div>
          ) : (
            <>
              {paper.summary?.trim() ? (
                <section className="axv-abstract" aria-label="Paper abstract">
                  <div className="axv-sec">Abstract</div>
                  <p>{renderMathTextWithEvidence(paper.summary.trim(), summaryEvidence)}</p>
                </section>
              ) : null}
              <div
                ref={textContainerRef}
                className="axv-text"
                onMouseUp={captureSelection}
                onKeyUp={captureSelection}
              >
                {textSegments.map((seg) => (
                  <SourceMathText
                    key={seg.key}
                    text={seg.text}
                    baseIndex={seg.start}
                    highlighted={seg.highlighted}
                  />
                ))}
              </div>
            </>
          )}
          {textHasMore ? (
            <button
              type="button"
              className="axv-btn-secondary axv-loadmore"
              disabled={textLoading}
              onClick={() => void loadMoreText()}
            >
              {textLoading
                ? <><span className="axv-spin" />Loading…</>
                : `Load more (${textOffsets.byteLength.toLocaleString()} of ${textTotalBytes.toLocaleString()} bytes shown)`}
            </button>
          ) : null}

          <div className="axv-sec">Highlights{highlights.length ? ` · ${highlights.length}` : ''}</div>
          {highlights.length === 0 ? (
            <p className="axv-help">No highlights yet. Select a passage above, then press Highlight.</p>
          ) : (
            <ul className="axv-highlights">
              {highlights.map((h) => (
                <li key={h.id} className="axv-highlight-row">
                  {/* Display-only: math-aware truncation never cuts inside a
                      formula, and TeX renders instead of showing raw source.
                      Stored quotes and send-to-chat keep h.quote verbatim. */}
                  <blockquote className="axv-quote"><MathText text={truncateMathText(h.quote, 280)} inlineOnly /></blockquote>
                  <div className="axv-row-actions">
                    {insertDraftText ? (
                      <button type="button" className="axv-btn-secondary" onClick={() => sendQuoteToChat(h.quote)}>
                        Send to chat
                      </button>
                    ) : null}
                    <button
                      type="button"
                      className="axv-link axv-link-warn"
                      aria-label="Remove this highlight"
                      onClick={() => removeHighlight(h.id)}
                    >
                      Remove
                    </button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  );
};

export default ArxivPaperViewer;
