import React from 'react';
import * as pdfjs from 'pdfjs-dist/legacy/build/pdf.mjs';
import pdfWorkerUrl from 'pdfjs-dist/legacy/build/pdf.worker.mjs?url';

// Same-origin, version-matched module worker: Vite serves the file directly in
// dev and emits it as a hashed .mjs asset in builds. pdf.js 4.x creates the
// worker with { type: "module" }, supported by WebKit/WKWebView since Safari 15.
pdfjs.GlobalWorkerOptions.workerSrc = pdfWorkerUrl;

interface PdfCanvasViewerProps {
  data: Uint8Array;
  title: string;
}

// Failures that only happen because we tore the document or render task down
// mid-flight (StrictMode double-invoke, data swaps, unmount). Never worth
// surfacing: the replacement attempt is already underway or nothing is shown.
function isTeardownError(err: unknown): boolean {
  if (!(err instanceof Error)) return false;
  if (err.name === 'RenderingCancelledException' || err.name === 'AbortException') return true;
  return /Transport destroyed|Worker was destroyed|Loading aborted/i.test(err.message);
}

// Maps common pdf.js failures to short human messages, keeping the raw detail
// in parentheses (the full error object is also console.warn'd at catch sites).
function pdfErrorMessage(err: unknown): string {
  const name = err instanceof Error ? err.name : '';
  const raw = err instanceof Error && err.message ? err.message : '';
  const detail = raw ? ` (${raw})` : '';
  if (name === 'InvalidPDFException') return `This file does not look like a readable PDF.${detail}`;
  if (name === 'PasswordException') return 'This PDF is password-protected, so it cannot be previewed here.';
  if (/fake worker failed/i.test(raw)) return `The PDF rendering engine failed to start.${detail}`;
  return raw || 'Failed to render the PDF preview.';
}

interface PdfPageCanvasProps {
  documentProxy: any;
  pageNumber: number;
  pageCount: number;
  containerWidth: number;
  zoom: number;
  title: string;
}

const PdfPageCanvas: React.FC<PdfPageCanvasProps> = ({
  documentProxy,
  pageNumber,
  pageCount,
  containerWidth,
  zoom,
  title,
}) => {
  const canvasRef = React.useRef<HTMLCanvasElement>(null);
  const [rendering, setRendering] = React.useState(false);
  const [renderError, setRenderError] = React.useState<string | null>(null);
  const [localRetryNonce, setLocalRetryNonce] = React.useState(0);

  React.useEffect(() => {
    if (!documentProxy || !canvasRef.current) return undefined;

    let cancelled = false;
    let renderTask: any | null = null;
    const canvas = canvasRef.current;

    setRendering(true);
    setRenderError(null);

    (async () => {
      try {
        const page = await documentProxy.getPage(pageNumber);
        if (cancelled) return;

        const baseViewport = page.getViewport({ scale: 1 });
        const availableWidth = Math.max(320, (containerWidth || 760) - 44);
        const fitScale = availableWidth / baseViewport.width;
        const scale = Math.max(0.6, Math.min(3, fitScale * zoom));
        const viewport = page.getViewport({ scale });
        const outputScale = Math.min(window.devicePixelRatio || 1, 2);
        const context = canvas.getContext('2d');

        if (!context) {
          throw new Error('Canvas rendering is unavailable in this browser.');
        }

        canvas.width = Math.floor(viewport.width * outputScale);
        canvas.height = Math.floor(viewport.height * outputScale);
        canvas.style.width = `${Math.floor(viewport.width)}px`;
        canvas.style.height = `${Math.floor(viewport.height)}px`;
        context.setTransform(outputScale, 0, 0, outputScale, 0, 0);
        context.clearRect(0, 0, viewport.width, viewport.height);

        renderTask = page.render({ canvasContext: context, viewport });
        await renderTask.promise;
      } catch (err) {
        if (!cancelled && !isTeardownError(err)) {
          console.warn(`arXiv PDF page ${pageNumber} render failed`, err);
          setRenderError(pdfErrorMessage(err));
        }
      } finally {
        if (!cancelled) {
          setRendering(false);
        }
      }
    })();

    return () => {
      cancelled = true;
      renderTask?.cancel?.();
    };
  }, [containerWidth, documentProxy, pageNumber, zoom, localRetryNonce]);

  return (
    <section className="axv-pdf-page-frame" aria-label={`Page ${pageNumber} of ${pageCount}`}>
      <div className="axv-pdf-page-label">Page {pageNumber}</div>
      <div className="axv-pdf-page-canvas-wrap">
        <canvas
          ref={canvasRef}
          className="axv-pdf-canvas"
          aria-label={`PDF page ${pageNumber} preview for ${title}`}
        />
        {rendering ? (
          <div className="axv-pdf-rendering"><span className="axv-spin" />Rendering page...</div>
        ) : null}
        {renderError && !rendering ? (
          <div className="axv-error axv-pdf-page-error" role="status">
            <span>{renderError}</span>
            <button
              type="button"
              className="axv-btn-secondary"
              onClick={() => setLocalRetryNonce((n) => n + 1)}
            >
              Try again
            </button>
          </div>
        ) : null}
      </div>
    </section>
  );
};

const PdfCanvasViewer: React.FC<PdfCanvasViewerProps> = ({ data, title }) => {
  const shellRef = React.useRef<HTMLDivElement>(null);
  const loadMoreRef = React.useRef<HTMLDivElement>(null);
  const [documentProxy, setDocumentProxy] = React.useState<any | null>(null);
  const [pageCount, setPageCount] = React.useState(0);
  const [zoom, setZoom] = React.useState(1);
  const [containerWidth, setContainerWidth] = React.useState(0);
  const [loading, setLoading] = React.useState(false);
  const [visiblePageCount, setVisiblePageCount] = React.useState(0);
  // Document-load errors replace the canvas (there is nothing to draw); the
  // recovery path for bad bytes is the parent's Retry, which refetches them.
  const [error, setError] = React.useState<string | null>(null);

  React.useEffect(() => {
    const shell = shellRef.current;
    if (!shell) return undefined;

    let timer = 0;
    const measure = () => {
      const width = Math.round(shell.clientWidth);
      // Ignore sub-pixel/scrollbar jitter; a real change (including the first
      // 0 -> laid-out transition) always moves by more than a few px.
      setContainerWidth((prev) => (Math.abs(width - prev) < 4 ? prev : width));
    };
    // Trailing debounce: host animations (settings sheet slide-in) emit a
    // resize per frame, and re-rendering on each one keeps cancelling the
    // in-flight page render, so the PDF never appears until things settle.
    const schedule = () => {
      window.clearTimeout(timer);
      timer = window.setTimeout(measure, 120);
    };

    // Initial measurement stays synchronous; a 0 width here is fine because
    // the render effect falls back to a sane default width.
    measure();

    if (typeof ResizeObserver === 'undefined') {
      window.addEventListener('resize', schedule);
      return () => {
        window.clearTimeout(timer);
        window.removeEventListener('resize', schedule);
      };
    }

    const ro = new ResizeObserver(schedule);
    ro.observe(shell);
    return () => {
      window.clearTimeout(timer);
      ro.disconnect();
    };
  }, []);

  React.useEffect(() => {
    let cancelled = false;
    let loadingTask: any | null = null;
    let loadedDocument: any | null = null;

    setLoading(true);
    setError(null);
    setDocumentProxy(null);
    setPageCount(0);
    setVisiblePageCount(0);

    (async () => {
      try {
        loadingTask = pdfjs.getDocument({
          // data.slice(): getDocument transfers the underlying buffer to the
          // worker, which would detach the parent's cached bytes and break
          // StrictMode re-runs / later remounts if we passed `data` directly.
          data: data.slice(),
          // arXiv PDFs are untrusted external content: never let pdf.js
          // compile font/PostScript programs through eval-like paths.
          isEvalSupported: false,
        });
        loadedDocument = await loadingTask.promise;
        if (cancelled) {
          await loadedDocument.destroy?.();
          return;
        }
        setDocumentProxy(loadedDocument);
        setPageCount(loadedDocument.numPages || 0);
        setVisiblePageCount(Math.min(2, loadedDocument.numPages || 0));
      } catch (err) {
        if (!cancelled && !isTeardownError(err)) {
          console.warn('arXiv PDF document load failed', err);
          setError(pdfErrorMessage(err));
        }
      } finally {
        if (!cancelled) {
          setLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
      void loadingTask?.destroy?.();
      void loadedDocument?.destroy?.();
    };
  }, [data]);

  React.useEffect(() => {
    if (!pageCount || visiblePageCount >= pageCount) return undefined;
    const root = shellRef.current;
    const target = loadMoreRef.current;
    if (!root || !target) return undefined;

    const loadNextPages = () => {
      setVisiblePageCount((count) => Math.min(pageCount, count + 2));
    };

    if (typeof IntersectionObserver === 'undefined') {
      let frame = 0;
      const onScroll = () => {
        window.cancelAnimationFrame(frame);
        frame = window.requestAnimationFrame(() => {
          const remaining = root.scrollHeight - root.scrollTop - root.clientHeight;
          if (remaining < 900) loadNextPages();
        });
      };
      root.addEventListener('scroll', onScroll, { passive: true });
      onScroll();
      return () => {
        window.cancelAnimationFrame(frame);
        root.removeEventListener('scroll', onScroll);
      };
    }

    const observer = new IntersectionObserver(
      (entries) => {
        if (!entries.some((entry) => entry.isIntersecting)) return;
        loadNextPages();
      },
      {
        root,
        // Start rendering the next pair of pages before the user reaches the
        // spinner, without continuously appending pages while the document is
        // idle at the top of a long PDF.
        rootMargin: '900px 0px',
      },
    );
    observer.observe(target);
    return () => observer.disconnect();
  }, [pageCount, visiblePageCount]);

  const pageNumbers = React.useMemo(
    () => Array.from({ length: visiblePageCount }, (_, index) => index + 1),
    [visiblePageCount],
  );

  return (
    <div className="axv-pdf-viewer">
      <div className="axv-pdf-toolbar" role="toolbar" aria-label="PDF controls">
        <span className="axv-pdf-page">
          {pageCount
            ? visiblePageCount < pageCount
              ? `${visiblePageCount} of ${pageCount} pages`
              : `${pageCount} pages`
            : 'Loading pages...'}
        </span>
        <span className="axv-pdf-spacer" />
        <button
          type="button"
          className="axv-btn-secondary axv-pdf-tool"
          aria-label="Zoom out"
          disabled={zoom <= 0.7}
          onClick={() => setZoom((z) => Math.max(0.7, Number((z - 0.15).toFixed(2))))}
        >
          -
        </button>
        <span className="axv-pdf-page">{Math.round(zoom * 100)}%</span>
        <button
          type="button"
          className="axv-btn-secondary axv-pdf-tool"
          aria-label="Zoom in"
          disabled={zoom >= 2}
          onClick={() => setZoom((z) => Math.min(2, Number((z + 0.15).toFixed(2))))}
        >
          +
        </button>
      </div>

      <div ref={shellRef} className="axv-pdf-canvas-shell">
        {loading ? (
          <div className="axv-pdf-state" role="status"><span className="axv-spin" />Preparing PDF preview...</div>
        ) : error ? (
          <div className="axv-error" role="status">{error}</div>
        ) : (
          <div className="axv-pdf-pages" aria-label={`Scrollable PDF preview for ${title}`}>
            {pageNumbers.map((pageNumber) => (
              <PdfPageCanvas
                key={pageNumber}
                documentProxy={documentProxy}
                pageNumber={pageNumber}
                pageCount={pageCount}
                containerWidth={containerWidth}
                zoom={zoom}
                title={title}
              />
            ))}
            {visiblePageCount < pageCount ? (
              <div ref={loadMoreRef} className="axv-pdf-state" role="status"><span className="axv-spin" />Loading more pages...</div>
            ) : null}
          </div>
        )}
      </div>
    </div>
  );
};

export default PdfCanvasViewer;
