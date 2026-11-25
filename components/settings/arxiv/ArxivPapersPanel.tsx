import React from 'react';
import { toast } from 'sonner';
import type {
  ArxivPaper,
  ArxivSearchEntry,
} from '@/services/arxivApi';
import {
  ArxivApiError,
  deleteArxivPaper,
  downloadArxivPaper,
  fetchArxivPaperPdfFile,
  fetchArxivPaperTextFile,
  listArxivPapers,
  searchArxiv,
} from '@/services/arxivApi';
import { openExternalUrl } from '@/services/openExternal';
import ArxivPaperViewer from '@/components/arxiv/ArxivPaperViewer';
import { safeArxivUrl } from '@/components/arxiv/arxivContentSafety';
import MathText from '@/components/arxiv/MathText';
import { clearArxivHighlights } from '@/components/arxiv/useArxivHighlights';

// Common arXiv categories for the datalist autocomplete. Local data by design —
// the taxonomy changes rarely and must not require a network call to render.
const ARXIV_CATEGORY_SUGGESTIONS = [
  'cs.AI', 'cs.CL', 'cs.CV', 'cs.LG', 'cs.NE', 'cs.IR', 'cs.RO', 'cs.SE', 'cs.DB', 'cs.DC', 'cs.CR', 'cs.HC',
  'stat.ML', 'stat.ME', 'math.OC', 'math.ST', 'math.PR', 'math.NA',
  'eess.AS', 'eess.IV', 'eess.SP',
  'physics.data-an', 'quant-ph', 'cond-mat.dis-nn',
  'q-bio.NC', 'q-bio.QM', 'q-fin.ST', 'econ.EM',
];

function formatDate(value: string | null): string {
  if (!value) return '';
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}

function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '';
  if (n < 1024 * 1024) return `${Math.max(1, Math.round(n / 1024))} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

function authorsPreview(authors: string[]): string {
  if (authors.length === 0) return 'Unknown authors';
  if (authors.length <= 3) return authors.join(', ');
  return `${authors.slice(0, 3).join(', ')} +${authors.length - 3}`;
}

function errorMessage(err: unknown, fallback: string): string {
  if (err instanceof ArxivApiError) {
    if (err.status === 429 || err.status === 503) {
      return 'arXiv allows about one request every 3 seconds — please retry in a moment.';
    }
    return err.message || fallback;
  }
  if (err instanceof Error && err.message) return err.message;
  return fallback;
}

function looksLikeArxivIdentifier(value: string): boolean {
  const raw = value.trim().toLowerCase().replace(/^arxiv:/, '');
  if (!raw) return false;
  if (/^https?:\/\/(www\.|export\.)?arxiv\.org\/(abs|pdf)\/\S+$/.test(raw)) return true;
  return /^\d{4}\.\d{4,5}(v\d+)?$/.test(raw) || /^[a-z-]+(\.[a-z]{2})?\/\d{7}(v\d+)?$/.test(raw);
}

export interface ArxivPapersPanelProps {
  enabled: boolean;
  onAttachFile?: (file: File) => void | Promise<void>;
  onInsertDraftText?: (text: string) => void;
  onCloseDialog?: () => void;
}

type PanelTab = 'search' | 'library';

const ArxivPapersPanel: React.FC<ArxivPapersPanelProps> = ({
  enabled,
  onAttachFile,
  onInsertDraftText,
  onCloseDialog,
}) => {
  const [tab, setTab] = React.useState<PanelTab>('search');

  // --- Search state ---
  const [query, setQuery] = React.useState('');
  const [category, setCategory] = React.useState('');
  const [author, setAuthor] = React.useState('');
  const [searching, setSearching] = React.useState(false);
  const [searchError, setSearchError] = React.useState<string | null>(null);
  const [results, setResults] = React.useState<ArxivSearchEntry[]>([]);
  const [totalResults, setTotalResults] = React.useState(0);
  const [downloadingIds, setDownloadingIds] = React.useState<Set<string>>(new Set());

  // --- Library state ---
  const [papers, setPapers] = React.useState<ArxivPaper[]>([]);
  const [libraryFilter, setLibraryFilter] = React.useState('');
  const [libraryLoading, setLibraryLoading] = React.useState(false);
  const [libraryError, setLibraryError] = React.useState<string | null>(null);

  // --- Viewer state ---
  // The shared viewer self-fetches metadata and text; the panel only tracks
  // which paper is open.
  const [openPaperKey, setOpenPaperKey] = React.useState<string | null>(null);
  const [busyAction, setBusyAction] = React.useState<string | null>(null);

  const refreshLibrary = React.useCallback(async (filter?: string) => {
    setLibraryLoading(true);
    setLibraryError(null);
    try {
      const res = await listArxivPapers({ q: filter?.trim() || undefined, limit: 200 });
      setPapers(res.papers);
    } catch (err) {
      setLibraryError(errorMessage(err, 'Failed to load the paper library.'));
    } finally {
      setLibraryLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void refreshLibrary();
  }, [refreshLibrary]);

  const downloadedKeys = React.useMemo(() => new Set(papers.map((p) => p.arxivId)), [papers]);

  const handleSearch = React.useCallback(async () => {
    const trimmedQuery = query.trim();
    const trimmedCategory = category.trim();
    const trimmedAuthor = author.trim();
    if (!trimmedQuery && !trimmedCategory && !trimmedAuthor) {
      setSearchError('Enter keywords, a category, or an author to search arXiv.');
      return;
    }
    setSearching(true);
    setSearchError(null);
    try {
      const exactIDLookup = Boolean(trimmedQuery && !trimmedCategory && !trimmedAuthor && looksLikeArxivIdentifier(trimmedQuery));
      const res = await searchArxiv({
        query: exactIDLookup ? undefined : trimmedQuery || undefined,
        ids: exactIDLookup ? [trimmedQuery] : undefined,
        categories: trimmedCategory ? [trimmedCategory] : undefined,
        authors: trimmedAuthor ? [trimmedAuthor] : undefined,
        maxResults: 25,
      });
      setResults(res.entries);
      setTotalResults(res.totalResults);
      if (res.entries.length === 0) {
        setSearchError('No papers matched. Try broader keywords or remove the category filter.');
      }
    } catch (err) {
      setResults([]);
      setTotalResults(0);
      setSearchError(errorMessage(err, 'Search failed — check that the backend is running.'));
    } finally {
      setSearching(false);
    }
  }, [query, category, author]);

  const handleDownload = React.useCallback(async (entry: ArxivSearchEntry) => {
    setDownloadingIds((prev) => new Set(prev).add(entry.arxivId));
    try {
      const { paper, alreadyExisted } = await downloadArxivPaper(entry.arxivId);
      toast.success(alreadyExisted ? 'Already in your library.' : `Downloaded "${paper.title.slice(0, 60)}"`);
      await refreshLibrary(libraryFilter);
    } catch (err) {
      toast.error(errorMessage(err, `Failed to download arXiv:${entry.arxivId}`));
    } finally {
      setDownloadingIds((prev) => {
        const next = new Set(prev);
        next.delete(entry.arxivId);
        return next;
      });
    }
  }, [refreshLibrary, libraryFilter]);

  const attachPaperToChat = React.useCallback(async (paper: ArxivPaper) => {
    if (!onAttachFile) {
      toast.error('Chat attachments are not available from here.');
      return;
    }
    setBusyAction(`attach:${paper.paperKey}`);
    try {
      // First-class file attachment: the stored PDF (or extracted text when no
      // PDF could be stored) goes through the exact same upload path as a
      // user-picked file, so the model receives it as a normal file attachment.
      const file = paper.pdfState === 'stored'
        ? await fetchArxivPaperPdfFile(paper)
        : await fetchArxivPaperTextFile(paper);
      await Promise.resolve(onAttachFile(file));
      toast.success(`Attached ${file.name} to the chat composer.`);
      onCloseDialog?.();
    } catch (err) {
      toast.error(errorMessage(err, 'Failed to attach the paper to chat.'));
    } finally {
      setBusyAction(null);
    }
  }, [onAttachFile, onCloseDialog]);

  const handleDeletePaper = React.useCallback(async (paper: ArxivPaper) => {
    setBusyAction(`delete:${paper.paperKey}`);
    try {
      await deleteArxivPaper(paper.paperKey);
      clearArxivHighlights(paper.paperKey);
      setOpenPaperKey((prev) => (prev === paper.paperKey ? null : prev));
      toast.success('Removed from library.');
      await refreshLibrary(libraryFilter);
    } catch (err) {
      toast.error(errorMessage(err, 'Failed to remove the paper.'));
    } finally {
      setBusyAction(null);
    }
  }, [refreshLibrary, libraryFilter]);

  // Wrapped chat actions for the shared viewer: same close-dialog-after-action
  // behavior as before extraction. Kept undefined when the underlying handler
  // is absent so the viewer hides the corresponding affordance.
  const viewerAttachFile = React.useMemo(() => {
    if (!onAttachFile) return undefined;
    return async (file: File) => {
      await Promise.resolve(onAttachFile(file));
      onCloseDialog?.();
    };
  }, [onAttachFile, onCloseDialog]);

  const viewerInsertDraftText = React.useMemo(() => {
    if (!onInsertDraftText) return undefined;
    return (text: string) => {
      onInsertDraftText(text);
      onCloseDialog?.();
    };
  }, [onInsertDraftText, onCloseDialog]);

  const filteredPapers = papers; // server-side filter via refreshLibrary(q)

  // ---------------------------------------------------------------- rendering

  const renderEntryRow = (entry: ArxivSearchEntry) => {
    const stored = downloadedKeys.has(entry.arxivId) || Boolean(entry.paperKey);
    const downloading = downloadingIds.has(entry.arxivId);
    // Prefer the backend-provided paper_key for downloaded entries; fall back
    // to matching the loaded library list.
    const paperKey = entry.paperKey || papers.find((p) => p.arxivId === entry.arxivId)?.paperKey || null;
    const absUrl = safeArxivUrl(entry.absUrl);
    return (
      <li key={`${entry.arxivId}v${entry.version}`} className="cx-ax-row">
        <div className="cx-ax-row-main">
          <div className="cx-ax-title"><MathText text={entry.title} inlineOnly /></div>
          <div className="cx-ax-meta">
            <span className="cx-ax-id">arXiv:{entry.arxivId}</span>
            {entry.primaryCategory ? <span className="cx-ax-chip">{entry.primaryCategory}</span> : null}
            <span>{authorsPreview(entry.authors)}</span>
            {entry.published ? <span>{formatDate(entry.published)}</span> : null}
          </div>
          {entry.summary ? <div className="cx-ax-abstract"><MathText text={entry.summary} inlineOnly /></div> : null}
        </div>
        <div className="cx-ax-row-actions">
          {absUrl ? (
            <button type="button" className="cx-link" onClick={() => void openExternalUrl(absUrl)}>
              Abstract
            </button>
          ) : null}
          {stored && paperKey ? (
            <button type="button" className="cx-btn-secondary" onClick={() => setOpenPaperKey(paperKey)}>
              Open
            </button>
          ) : (
            <button
              type="button"
              className="cx-btn-primary"
              disabled={downloading || !enabled}
              onClick={() => void handleDownload(entry)}
            >
              {downloading ? <><span className="cx-spin" />Downloading…</> : 'Download'}
            </button>
          )}
        </div>
      </li>
    );
  };

  const renderPaperRow = (paper: ArxivPaper) => (
    <li key={paper.paperKey} className="cx-ax-row">
      <div className="cx-ax-row-main">
        <div className="cx-ax-title"><MathText text={paper.title} inlineOnly /></div>
        <div className="cx-ax-meta">
          <span className="cx-ax-id">arXiv:{paper.arxivId}{paper.version ? `v${paper.version}` : ''}</span>
          {paper.primaryCategory ? <span className="cx-ax-chip">{paper.primaryCategory}</span> : null}
          <span>{authorsPreview(paper.authors)}</span>
          <span className={`cx-ax-state cx-ax-state-${paper.pdfState === 'stored' ? 'ok' : paper.extractState === 'metadata_only' ? 'warn' : 'muted'}`}>
            {paper.pdfState === 'stored'
              ? `PDF stored${paper.pdfSizeBytes ? ` · ${formatBytes(paper.pdfSizeBytes)}` : ''}`
              : paper.extractState === 'metadata_only'
                ? 'Metadata only'
                : paper.pdfState === 'failed'
                  ? 'PDF failed'
                  : 'Pending'}
          </span>
        </div>
      </div>
      <div className="cx-ax-row-actions">
        <button type="button" className="cx-btn-secondary" onClick={() => setOpenPaperKey(paper.paperKey)}>
          Open
        </button>
        <button
          type="button"
          className="cx-btn-secondary"
          disabled={busyAction === `attach:${paper.paperKey}` || !onAttachFile}
          onClick={() => void attachPaperToChat(paper)}
        >
          {busyAction === `attach:${paper.paperKey}` ? <><span className="cx-spin" />Attaching…</> : 'Attach to chat'}
        </button>
        <button
          type="button"
          className="cx-link cx-link-warn"
          aria-label={`Remove "${paper.title}" from library`}
          disabled={busyAction === `delete:${paper.paperKey}`}
          onClick={() => void handleDeletePaper(paper)}
        >
          Remove
        </button>
      </div>
    </li>
  );

  if (openPaperKey) {
    // The .cx-ax-viewer wrapper keeps the wide-sheet sizing rule
    // (.cx-sheet:has(.cx-ax-viewer)) working; the viewer itself is standalone.
    return (
      <div className="cx-ax-viewer">
        <ArxivPaperViewer
          paperKey={openPaperKey}
          onAttachFile={viewerAttachFile}
          onInsertDraftText={viewerInsertDraftText}
          onRequestClose={() => setOpenPaperKey(null)}
        />
      </div>
    );
  }

  return (
    <div className="cx-ax-panel">
      <div className="cx-seg cx-ax-seg" role="tablist" aria-label="arXiv papers">
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'search'}
          className={`cx-seg-btn${tab === 'search' ? ' cx-on' : ''}`}
          onClick={() => setTab('search')}
        >
          Search arXiv
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'library'}
          className={`cx-seg-btn${tab === 'library' ? ' cx-on' : ''}`}
          onClick={() => { setTab('library'); void refreshLibrary(libraryFilter); }}
        >
          Library{papers.length ? ` · ${papers.length}` : ''}
        </button>
      </div>

      {tab === 'search' ? (
        <>
          <form
            className="cx-ax-search-form"
            onSubmit={(e) => { e.preventDefault(); void handleSearch(); }}
          >
            <div className="cx-field-row">
              <div className="cx-ax-grow">
                <span className="cx-field-label">Keywords</span>
                <CxLikeInput
                  placeholder="e.g. retrieval augmented generation"
                  value={query}
                  onChange={setQuery}
                />
              </div>
            </div>
            <div className="cx-field-row">
              <div>
                <span className="cx-field-label">Category · optional</span>
                <CxLikeInput placeholder="cs.AI" value={category} onChange={setCategory} listId="cx-ax-categories" mono />
                <datalist id="cx-ax-categories">
                  {ARXIV_CATEGORY_SUGGESTIONS.map((c) => <option key={c} value={c} />)}
                </datalist>
              </div>
              <div>
                <span className="cx-field-label">Author · optional</span>
                <CxLikeInput placeholder="Hinton" value={author} onChange={setAuthor} />
              </div>
              <div className="cx-ax-search-btn-wrap">
                <button type="submit" className="cx-btn-primary" disabled={searching}>
                  {searching ? <><span className="cx-spin" />Searching…</> : 'Search'}
                </button>
              </div>
            </div>
            <p className="cx-help">
              Searches arxiv.org directly (about one request every 3 seconds is allowed). You can also paste an
              arXiv ID like <code>2301.12345</code> into Keywords.
            </p>
          </form>
          {searchError ? <div className="cx-feedback" role="status">{searchError}</div> : null}
          {results.length > 0 ? (
            <>
              <div className="cx-ax-count" aria-live="polite">
                {totalResults.toLocaleString()} matches · showing first {results.length}
              </div>
              <ul className="cx-ax-list">{results.map(renderEntryRow)}</ul>
            </>
          ) : null}
        </>
      ) : (
        <>
          <form
            className="cx-ax-search-form"
            onSubmit={(e) => { e.preventDefault(); void refreshLibrary(libraryFilter); }}
          >
            <div className="cx-field-row">
              <div className="cx-ax-grow">
                <span className="cx-field-label">Filter library</span>
                <CxLikeInput placeholder="Filter by title, author or ID" value={libraryFilter} onChange={setLibraryFilter} />
              </div>
              <div className="cx-ax-search-btn-wrap">
                <button type="submit" className="cx-btn-secondary" disabled={libraryLoading}>
                  {libraryLoading ? <><span className="cx-spin" />Loading…</> : 'Filter'}
                </button>
              </div>
            </div>
          </form>
          {libraryError ? <div className="cx-feedback cx-feedback-err" role="status">{libraryError}</div> : null}
          {libraryLoading && papers.length === 0 ? (
            <div className="cx-ax-loading"><span className="cx-spin" />Loading library…</div>
          ) : filteredPapers.length === 0 ? (
            <p className="cx-help">No papers yet. Search arXiv and download a paper to start your library.</p>
          ) : (
            <ul className="cx-ax-list">{filteredPapers.map(renderPaperRow)}</ul>
          )}
        </>
      )}
    </div>
  );
};

// Minimal input matching the connections console styling without importing the
// (non-exported) CxInput from McpIntegrationsTab.
const CxLikeInput: React.FC<{
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
  mono?: boolean;
  listId?: string;
}> = ({ value, onChange, placeholder, mono, listId }) => (
  <input
    className={`cx-input${mono ? ' cx-mono' : ''}`}
    placeholder={placeholder}
    value={value}
    list={listId}
    onChange={(e) => onChange(e.target.value)}
  />
);

export default ArxivPapersPanel;
