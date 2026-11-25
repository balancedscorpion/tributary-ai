package arxiv

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/atomicfile"
	"ai-analyst-local/internal/chat/pdfextract"
	"ai-analyst-local/internal/config"
)

// minExtractedChars is the minimum amount of extracted PDF text considered
// useful; below it the paper degrades to metadata_only with a fallback text
// file built from the Atom metadata.
const minExtractedChars = 200

var (
	lowercaseLineHyphenRE = regexp.MustCompile(`(\p{Ll})-\n(\p{Ll})`)
	letterLineHyphenRE    = regexp.MustCompile(`(\p{L})-\n(\p{L})`)
)

// Options configures a Manager.
type Options struct {
	// CfgDir is the app config directory; papers live under CfgDir/arxiv.
	CfgDir string
	// Settings provides enablement, contact email, and health stamps.
	Settings *config.Store
	// BaseURL overrides the arXiv export API base URL (tests).
	BaseURL string
	// HTTPClient overrides the HTTP client (tests).
	HTTPClient *http.Client
	// Limiter overrides the politeness limiter (tests).
	Limiter *Limiter
}

// Manager owns the arXiv integration: settings-gated search, single-flight
// paper downloads, local storage, and the HTTP API.
type Manager struct {
	settings *config.Store
	store    *Store
	client   *Client
	limiter  *Limiter

	mu        sync.Mutex
	downloads map[string]bool

	mux *http.ServeMux
}

type PDFPreview struct {
	ArxivID   string
	Version   int
	SizeBytes int64
}

// NewManager builds a Manager rooted at opts.CfgDir.
func NewManager(opts Options) *Manager {
	limiter := opts.Limiter
	if limiter == nil {
		limiter = NewLimiter(DefaultMinInterval, nil, nil)
	}
	m := &Manager{
		settings: opts.Settings,
		store:    NewStore(opts.CfgDir),
		limiter:  limiter,
		client: NewClient(ClientOptions{
			BaseURL:    opts.BaseURL,
			Limiter:    limiter,
			HTTPClient: opts.HTTPClient,
		}),
		downloads: map[string]bool{},
	}
	m.mux = m.routes()
	return m
}

// arxivSettings returns the current sanitized arXiv settings (never nil).
func (m *Manager) arxivSettings() *config.ArxivSettings {
	if m != nil && m.settings != nil {
		s := m.settings.Get()
		if s.Arxiv != nil {
			return s.Arxiv
		}
	}
	off := false
	return &config.ArxivSettings{Enabled: &off}
}

func (m *Manager) enabled() bool {
	s := m.arxivSettings()
	return s.Enabled != nil && *s.Enabled
}

func (m *Manager) contactEmail() string {
	s := m.arxivSettings()
	if s.ContactEmail == nil {
		return ""
	}
	return strings.TrimSpace(*s.ContactEmail)
}

// Status returns local arXiv integration status and library metrics.
func (m *Manager) Status() Status {
	if m == nil {
		return Status{OK: false}
	}
	return m.statusPayload()
}

// requestClient returns the client carrying the contact email configured at
// request time.
func (m *Manager) requestClient() *Client {
	return m.client.WithContactEmail(m.contactEmail())
}

// persistHealth stamps check results into settings, mirroring the Slack
// manager's persistSlackHealth.
func (m *Manager) persistHealth(ok bool, message string) (int64, bool, string) {
	checkedAt := time.Now().UnixMilli()
	checkedMessage := strings.TrimSpace(message)
	if checkedMessage == "" {
		if ok {
			checkedMessage = "arXiv connection verified."
		} else {
			checkedMessage = "arXiv connection check failed."
		}
	}
	if m == nil || m.settings == nil {
		return checkedAt, ok, checkedMessage
	}
	next := m.settings.Get()
	if next.Arxiv == nil {
		next.Arxiv = config.DefaultSettings().Arxiv
	}
	if next.Arxiv == nil {
		next.Arxiv = &config.ArxivSettings{}
	}
	next.Arxiv.LastCheckedAt = &checkedAt
	next.Arxiv.LastCheckOk = &ok
	next.Arxiv.LastCheckMessage = &checkedMessage
	_ = m.settings.Save(next)
	return checkedAt, ok, checkedMessage
}

// Check probes the export API and persists health stamps.
func (m *Manager) Check(ctx context.Context) error {
	if m == nil {
		return errors.New("arxiv integration is not configured")
	}
	if !m.enabled() {
		return errors.New("arxiv connection is disabled")
	}
	err := m.requestClient().Check(ctx)
	if err != nil {
		m.persistHealth(false, err.Error())
		return err
	}
	m.persistHealth(true, "arXiv export API is reachable.")
	return nil
}

// Search executes a settings-gated arXiv search.
func (m *Manager) Search(ctx context.Context, spec SearchSpec) (SearchResult, error) {
	if m == nil {
		return SearchResult{}, errors.New("arxiv integration is not configured")
	}
	if !m.enabled() {
		return SearchResult{}, errors.New("arxiv connection is disabled")
	}
	spec.Query = strings.TrimSpace(spec.Query)
	if len(spec.Query) > 1024 {
		return SearchResult{}, errBadRequest("query must be at most 1024 characters")
	}
	if spec.Query == "" && !hasSearchValues(spec.Categories) && !hasSearchValues(spec.Authors) && !hasSearchValues(spec.IDs) {
		return SearchResult{}, errBadRequest("at least one of query, categories, authors, or ids is required")
	}
	if spec.MaxResults <= 0 {
		spec.MaxResults = defaultSearchResults
	}
	if spec.MaxResults > maxHandlerResults {
		spec.MaxResults = maxHandlerResults
	}
	if spec.Start < 0 {
		spec.Start = 0
	}
	return m.requestClient().Search(ctx, spec)
}

// DownloadPaper fetches metadata (and the PDF when possible) for the given
// raw arXiv ID, runs text extraction, and stores the paper record. A failed
// PDF download still yields a stored metadata_only record. The second
// concurrent request for the same paper returns errDownloadInProgress.
func (m *Manager) DownloadPaper(ctx context.Context, rawID string) (*Paper, bool, error) {
	if m == nil {
		return nil, false, errors.New("arxiv integration is not configured")
	}
	if !m.enabled() {
		return nil, false, errors.New("arxiv connection is disabled")
	}
	id, version, err := CanonicalID(rawID)
	if err != nil {
		return nil, false, errBadRequest(err.Error())
	}
	key := PaperKey(id)
	if paper, err := m.store.GetPaper(key); err == nil {
		return paper, true, nil
	}

	m.mu.Lock()
	if m.downloads[key] {
		m.mu.Unlock()
		return nil, false, errDownloadInProgress
	}
	m.downloads[key] = true
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.downloads, key)
		m.mu.Unlock()
	}()

	client := m.requestClient()
	lookup := id
	if version > 0 {
		lookup = fmt.Sprintf("%sv%d", id, version)
	}
	result, err := client.Search(ctx, SearchSpec{IDs: []string{lookup}, MaxResults: 1})
	if err != nil {
		return nil, false, errUpstream(fmt.Sprintf("arxiv metadata lookup failed: %v", err))
	}
	if len(result.Entries) == 0 {
		return nil, false, errNotFound(fmt.Sprintf("arxiv paper %q was not found", id))
	}
	entry := result.Entries[0]

	paper := &Paper{
		PaperKey:        key,
		ArxivID:         id,
		Version:         entry.Version,
		Title:           entry.Title,
		Summary:         entry.Summary,
		Authors:         append([]string(nil), entry.Authors...),
		Categories:      append([]string(nil), entry.Categories...),
		PrimaryCategory: entry.PrimaryCategory,
		Published:       entry.Published,
		Updated:         entry.Updated,
		AddedAt:         time.Now().UnixMilli(),
		PDFState:        PDFStateNone,
		ExtractState:    ExtractStateNone,
		AbsURL:          entry.AbsURL,
		PDFURL:          entry.PDFURL,
	}
	if paper.Version == 0 {
		paper.Version = version
	}

	pdfPath, err := m.downloadPDF(ctx, client, paper)
	if err != nil {
		paper.PDFState = PDFStateFailed
		paper.LastError = err.Error()
	} else {
		paper.PDFState = PDFStateStored
	}

	m.extractText(paper, pdfPath)

	if err := m.store.SavePaper(paper); err != nil {
		// Do not echo err.Error(): store errors can carry filesystem paths.
		return nil, false, errors.New("failed to save paper record")
	}
	return paper, false, nil
}

// PreviewPDF fetches a paper PDF for immediate viewing without writing any
// paper metadata, PDF bytes, or extracted text to the local library.
func (m *Manager) PreviewPDF(ctx context.Context, rawID string, rawPDFURL string, dst io.Writer) (PDFPreview, error) {
	if m == nil {
		return PDFPreview{}, errors.New("arxiv integration is not configured")
	}
	if !m.enabled() {
		return PDFPreview{}, errors.New("arxiv connection is disabled")
	}
	if dst == nil {
		return PDFPreview{}, errBadRequest("download destination is required")
	}
	id, version, err := CanonicalID(rawID)
	if err != nil {
		return PDFPreview{}, errBadRequest(err.Error())
	}
	client := m.requestClient()
	pdfURL := strings.TrimSpace(rawPDFURL)
	if pdfURL == "" {
		lookup := id
		if version > 0 {
			lookup = fmt.Sprintf("%sv%d", id, version)
		}
		result, err := client.Search(ctx, SearchSpec{IDs: []string{lookup}, MaxResults: 1})
		if err != nil {
			return PDFPreview{}, errUpstream(fmt.Sprintf("arxiv metadata lookup failed: %v", err))
		}
		if len(result.Entries) == 0 {
			return PDFPreview{}, errNotFound(fmt.Sprintf("arxiv paper %q was not found", id))
		}
		entry := result.Entries[0]
		pdfURL = entry.PDFURL
		if version == 0 {
			version = entry.Version
		}
	} else if pdfID, pdfVersion, err := CanonicalID(pdfURL); err == nil {
		if pdfID != id {
			return PDFPreview{}, errBadRequest("pdf_url does not match arxiv_id")
		}
		if version == 0 {
			version = pdfVersion
		}
	}
	if strings.TrimSpace(pdfURL) == "" {
		return PDFPreview{}, errUpstream("arxiv entry does not expose a PDF link")
	}
	written, _, err := client.DownloadPDF(ctx, pdfURL, dst, DefaultMaxPDFBytes)
	if err != nil {
		return PDFPreview{}, errUpstream(fmt.Sprintf("arxiv pdf preview failed: %v", err))
	}
	return PDFPreview{ArxivID: id, Version: version, SizeBytes: written}, nil
}

// ListPapers returns a bounded page from the local arXiv paper library.
func (m *Manager) ListPapers(q string, limit int, offset int) ([]*Paper, int, *int, error) {
	if m == nil {
		return nil, 0, nil, errors.New("arxiv integration is not configured")
	}
	papers, err := m.store.ListPapers()
	if err != nil {
		// Do not echo err.Error(): store errors can carry filesystem paths.
		return nil, 0, nil, errors.New("failed to list papers")
	}
	query := strings.ToLower(strings.TrimSpace(q))
	if query != "" {
		filtered := papers[:0]
		for _, paper := range papers {
			if paperMatchesQuery(paper, query) {
				filtered = append(filtered, paper)
			}
		}
		papers = filtered
	}
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	if offset < 0 {
		offset = 0
	}
	total := len(papers)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	var next *int
	if end < total {
		v := end
		next = &v
	}
	return papers[offset:end], total, next, nil
}

// NormalizePaperKey accepts a stored paper key, raw arXiv ID, or arXiv abs/pdf
// URL and returns the local paper key.
func NormalizePaperKey(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", errors.New("paper key or arxiv id is required")
	}
	if ValidPaperKey(value) {
		if _, err := KeyToID(value); err == nil {
			return value, nil
		}
	}
	id, _, err := CanonicalID(value)
	if err != nil {
		return "", err
	}
	return PaperKey(id), nil
}

// GetPaper returns a stored paper by local key, arXiv ID, or arXiv URL.
func (m *Manager) GetPaper(rawKeyOrID string) (*Paper, error) {
	if m == nil {
		return nil, errors.New("arxiv integration is not configured")
	}
	key, err := NormalizePaperKey(rawKeyOrID)
	if err != nil {
		return nil, errBadRequest(err.Error())
	}
	paper, err := m.store.GetPaper(key)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errNotFound("paper not found")
		}
		// Do not echo err.Error(): store errors can carry filesystem paths.
		return nil, errors.New("failed to load paper record")
	}
	return paper, nil
}

// TextWindow is a bounded excerpt from a stored paper's extracted text.
type TextWindow struct {
	OK             bool   `json:"ok"`
	PaperKey       string `json:"paper_key"`
	ArxivID        string `json:"arxiv_id"`
	Title          string `json:"title"`
	TotalChars     int    `json:"total_chars"`
	Offset         int    `json:"offset"`
	Text           string `json:"text"`
	HasMore        bool   `json:"has_more"`
	NextOffset     *int   `json:"next_offset,omitempty"`
	ExtractState   string `json:"extract_state"`
	ExtractQuality string `json:"extract_quality"`
	UntrustedNote  string `json:"untrusted_note"`
}

// PageExcerpt is one extracted PDF page exposed to the agent.
type PageExcerpt struct {
	PageNumber int    `json:"page_number"`
	Text       string `json:"text"`
	Method     string `json:"method"`
	CharCount  int    `json:"char_count"`
}

// PageWindow is a bounded page range from a stored arXiv paper.
type PageWindow struct {
	OK             bool          `json:"ok"`
	PaperKey       string        `json:"paper_key"`
	ArxivID        string        `json:"arxiv_id"`
	Title          string        `json:"title"`
	PageCount      int           `json:"page_count"`
	StartPage      int           `json:"start_page"`
	EndPage        int           `json:"end_page"`
	Pages          []PageExcerpt `json:"pages"`
	HasMore        bool          `json:"has_more"`
	NextPage       *int          `json:"next_page,omitempty"`
	ExtractState   string        `json:"extract_state"`
	ExtractQuality string        `json:"extract_quality"`
	SourceKind     string        `json:"source_kind"`
	UntrustedNote  string        `json:"untrusted_note"`
}

// PageReadProgress is emitted while a PageWindow is being assembled.
type PageReadProgress struct {
	PaperKey       string
	ArxivID        string
	Title          string
	Page           PageExcerpt
	PagesRead      int
	PageCount      int
	ExtractState   string
	ExtractQuality string
	SourceKind     string
}

// GetTextWindow returns a UTF-8-safe bounded window of extracted paper text.
func (m *Manager) GetTextWindow(rawKeyOrID string, offset int, maxChars int) (TextWindow, error) {
	paper, err := m.GetPaper(rawKeyOrID)
	if err != nil {
		return TextWindow{}, err
	}
	textPath, err := m.store.PaperTextPath(paper.PaperKey)
	if err != nil {
		return TextWindow{}, errBadRequest(err.Error())
	}
	data, err := os.ReadFile(textPath)
	if err != nil {
		return TextWindow{}, errNotFound("paper has no extracted text")
	}
	data = []byte(normalizeExtractedPaperText(string(data)))
	start, end := textWindowBounds(data, offset, maxChars)
	out := TextWindow{
		OK:             true,
		PaperKey:       paper.PaperKey,
		ArxivID:        paper.ArxivID,
		Title:          paper.Title,
		TotalChars:     len(data),
		Offset:         start,
		Text:           string(data[start:end]),
		HasMore:        end < len(data),
		ExtractState:   paper.ExtractState,
		ExtractQuality: paper.ExtractQuality,
		UntrustedNote:  untrustedNote,
	}
	if end < len(data) {
		v := end
		out.NextOffset = &v
	}
	return out, nil
}

// GetPageWindow returns a bounded range of extracted PDF pages. It reads the
// deterministic pdfextract pages.jsonl output when available and falls back to
// a single metadata/text page for metadata-only records.
func (m *Manager) GetPageWindow(rawKeyOrID string, startPage int, maxPages int) (PageWindow, error) {
	return m.GetPageWindowWithProgress(rawKeyOrID, startPage, maxPages, nil)
}

// GetPageWindowWithProgress returns a bounded range of extracted PDF pages and
// calls onPage as each returned page is selected. This lets callers surface
// progress while the page window is being assembled instead of after the full
// window has been read.
func (m *Manager) GetPageWindowWithProgress(rawKeyOrID string, startPage int, maxPages int, onPage func(PageReadProgress)) (PageWindow, error) {
	paper, err := m.GetPaper(rawKeyOrID)
	if err != nil {
		return PageWindow{}, err
	}
	if startPage <= 0 {
		startPage = 1
	}
	if maxPages <= 0 {
		maxPages = 5
	}
	if maxPages > 20 {
		maxPages = 20
	}

	pages := []PageExcerpt(nil)
	manifestPageCount := 0
	maxSeenPage := 0
	if paper.ExtractState != ExtractStateMetadataOnly {
		var err error
		pages, manifestPageCount, maxSeenPage, err = m.readExtractedPageWindow(paper, startPage, maxPages, onPage)
		if err != nil {
			return PageWindow{}, err
		}
	}
	if len(pages) == 0 && manifestPageCount > 0 && startPage > manifestPageCount {
		return PageWindow{}, errBadRequest("start_page is beyond the extracted page count")
	}
	if len(pages) == 0 {
		textPath, textErr := m.store.PaperTextPath(paper.PaperKey)
		if textErr != nil {
			return PageWindow{}, errBadRequest(textErr.Error())
		}
		data, readErr := os.ReadFile(textPath)
		if readErr != nil {
			return PageWindow{}, errNotFound("paper has no extracted text")
		}
		text := normalizeExtractedPaperText(string(data))
		pages = []PageExcerpt{{
			PageNumber: 1,
			Text:       text,
			Method:     "metadata_only",
			CharCount:  len([]rune(text)),
		}}
		maxSeenPage = 1
		if manifestPageCount <= 0 {
			manifestPageCount = 1
		}
		emitPageReadProgress(onPage, paper, pages[0], len(pages), manifestPageCount, "metadata_only")
	}

	pageCount := manifestPageCount
	if pageCount < len(pages) {
		pageCount = len(pages)
	}
	if pageCount < maxSeenPage {
		pageCount = maxSeenPage
	}
	if startPage > pageCount {
		return PageWindow{}, errBadRequest("start_page is beyond the extracted page count")
	}

	if len(pages) == 0 {
		return PageWindow{}, errBadRequest("start_page is beyond the extracted page count")
	}
	sourceKind := "pdf_extract"
	if paper.ExtractState == ExtractStateMetadataOnly {
		sourceKind = "metadata_only"
	}
	if sourceKind != "metadata_only" {
		allMetadataOnly := true
		for _, page := range pages {
			if page.Method != "metadata_only" {
				allMetadataOnly = false
				break
			}
		}
		if allMetadataOnly {
			sourceKind = "metadata_only"
		}
	}
	out := PageWindow{
		OK:             true,
		PaperKey:       paper.PaperKey,
		ArxivID:        paper.ArxivID,
		Title:          paper.Title,
		PageCount:      pageCount,
		StartPage:      pages[0].PageNumber,
		EndPage:        pages[len(pages)-1].PageNumber,
		Pages:          pages,
		HasMore:        pages[len(pages)-1].PageNumber < pageCount,
		ExtractState:   paper.ExtractState,
		ExtractQuality: paper.ExtractQuality,
		SourceKind:     sourceKind,
		UntrustedNote:  untrustedNote,
	}
	if out.HasMore {
		next := out.EndPage + 1
		out.NextPage = &next
	}
	return out, nil
}

func (m *Manager) readExtractedPageWindow(paper *Paper, startPage int, maxPages int, onPage func(PageReadProgress)) ([]PageExcerpt, int, int, error) {
	pagesPath, err := m.store.PaperPagesPath(paper.PaperKey)
	if err != nil {
		return nil, 0, 0, errBadRequest(err.Error())
	}
	manifestPageCount := m.readExtractedPageCount(paper.PaperKey)
	if manifestPageCount > 0 && startPage > manifestPageCount {
		return nil, manifestPageCount, manifestPageCount, nil
	}
	file, err := os.Open(pagesPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, manifestPageCount, 0, nil
		}
		return nil, manifestPageCount, 0, errors.New("failed to read extracted paper pages")
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	pages := []PageExcerpt{}
	maxSeenPage := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var page pdfextract.Page
		if err := json.Unmarshal([]byte(line), &page); err != nil {
			return nil, manifestPageCount, maxSeenPage, errors.New("extracted paper pages are corrupt")
		}
		if page.PageNumber > maxSeenPage {
			maxSeenPage = page.PageNumber
		}
		if page.PageNumber < startPage {
			continue
		}
		if len(pages) >= maxPages {
			if manifestPageCount > 0 {
				break
			}
			continue
		}
		text := normalizeExtractedPaperText(page.Text)
		method := strings.TrimSpace(page.Method)
		if method == "" {
			method = "unknown"
		}
		selected := PageExcerpt{
			PageNumber: page.PageNumber,
			Text:       text,
			Method:     method,
			CharCount:  len([]rune(text)),
		}
		pages = append(pages, selected)
		emitPageReadProgress(onPage, paper, selected, len(pages), manifestPageCount, sourceKindForPageMethod(method))
	}
	if err := scanner.Err(); err != nil {
		return nil, manifestPageCount, maxSeenPage, errors.New("failed to read extracted paper pages")
	}
	return pages, manifestPageCount, maxSeenPage, nil
}

func sourceKindForPageMethod(method string) string {
	if strings.TrimSpace(method) == "metadata_only" {
		return "metadata_only"
	}
	return "pdf_extract"
}

func emitPageReadProgress(onPage func(PageReadProgress), paper *Paper, page PageExcerpt, pagesRead int, pageCount int, sourceKind string) {
	if onPage == nil || paper == nil {
		return
	}
	onPage(PageReadProgress{
		PaperKey:       paper.PaperKey,
		ArxivID:        paper.ArxivID,
		Title:          paper.Title,
		Page:           page,
		PagesRead:      pagesRead,
		PageCount:      pageCount,
		ExtractState:   paper.ExtractState,
		ExtractQuality: paper.ExtractQuality,
		SourceKind:     sourceKind,
	})
}

func (m *Manager) readExtractedPageCount(paperKey string) int {
	manifestPath, err := m.store.PaperManifestPath(paperKey)
	if err != nil {
		return 0
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return 0
	}
	var manifest pdfextract.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return 0
	}
	return manifest.PageCount
}

func normalizeExtractedPaperText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	text = strings.Join(lines, "\n")
	text = lowercaseLineHyphenRE.ReplaceAllString(text, "$1$2")
	text = letterLineHyphenRE.ReplaceAllString(text, "$1-$2")
	return strings.TrimSpace(text)
}

// textWindowBounds clamps offset and maxChars and aligns the [start, end)
// byte window onto UTF-8 rune boundaries (continuation bytes are 0b10xxxxxx).
// When maxChars is smaller than the rune at start, the window widens to that
// one full rune so paging always makes progress: whenever end < len(data),
// end > start holds and a caller resuming at end advances.
func textWindowBounds(data []byte, offset int, maxChars int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(data) {
		offset = len(data)
	}
	if maxChars <= 0 {
		maxChars = defaultTextChars
	}
	if maxChars > maxTextChars {
		maxChars = maxTextChars
	}
	start := offset
	for start < len(data) && data[start]&0xC0 == 0x80 {
		start++
	}
	end := start + maxChars
	if end > len(data) {
		end = len(data)
	}
	for end > start && end < len(data) && data[end]&0xC0 == 0x80 {
		end--
	}
	if end == start && start < len(data) {
		end = start + 1
		for end < len(data) && data[end]&0xC0 == 0x80 {
			end++
		}
	}
	return start, end
}

// downloadPDF streams the entry PDF into a temp file inside the paper dir,
// renaming it to paper.pdf on success. It returns the final PDF path.
func (m *Manager) downloadPDF(ctx context.Context, client *Client, paper *Paper) (string, error) {
	if strings.TrimSpace(paper.PDFURL) == "" {
		return "", errors.New("arxiv entry does not expose a PDF link")
	}
	pdfPath, err := m.store.PaperPDFPath(paper.PaperKey)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(pdfPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, "paper.pdf.tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()
	written, sha, err := client.DownloadPDF(ctx, paper.PDFURL, tmp, DefaultMaxPDFBytes)
	if err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, pdfPath); err != nil {
		return "", err
	}
	keep = true
	paper.PDFSizeBytes = written
	paper.PDFSHA256 = sha
	return pdfPath, nil
}

// extractText runs pdfextract over the stored PDF and falls back to a
// metadata-derived text file when extraction fails or yields too little
// text, so the text endpoint always has content.
func (m *Manager) extractText(paper *Paper, pdfPath string) {
	extractDir, err := m.store.PaperExtractDir(paper.PaperKey)
	if err != nil {
		paper.ExtractState = ExtractStateFailed
		paper.LastError = err.Error()
		return
	}
	if paper.PDFState == PDFStateStored && pdfPath != "" {
		result, err := pdfextract.Extract(pdfextract.Request{
			SourcePath:   pdfPath,
			FileName:     paper.PaperKey + ".pdf",
			OutputDir:    extractDir,
			SourceSHA256: paper.PDFSHA256,
		})
		if err == nil {
			paper.ExtractQuality = result.Manifest.TextExtractionQuality
			if result.Manifest.CharacterCount >= minExtractedChars {
				paper.ExtractState = ExtractStateExtracted
				paper.TextChars = result.Manifest.CharacterCount
				return
			}
		}
	}
	paper.ExtractState = ExtractStateMetadataOnly
	fallback := m.metadataFallbackText(paper)
	if err := os.MkdirAll(extractDir, 0o700); err != nil {
		paper.ExtractState = ExtractStateFailed
		paper.LastError = err.Error()
		return
	}
	if err := atomicfile.WriteFile(filepath.Join(extractDir, "text.txt"), []byte(fallback), 0o600); err != nil {
		paper.ExtractState = ExtractStateFailed
		paper.LastError = err.Error()
		return
	}
	paper.TextChars = len(fallback)
}

func (m *Manager) metadataFallbackText(paper *Paper) string {
	return fmt.Sprintf("%s\n\nAuthors: %s\n\nAbstract:\n%s",
		strings.TrimSpace(paper.Title),
		strings.Join(paper.Authors, ", "),
		strings.TrimSpace(paper.Summary),
	)
}

func hasSearchValues(values []string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}

// Typed handler errors so HTTP code mapping stays in one place.

var errDownloadInProgress = httpError{status: http.StatusConflict, msg: "download already in progress"}

type httpError struct {
	status int
	msg    string
}

func (e httpError) Error() string { return e.msg }

func errBadRequest(msg string) error { return httpError{status: http.StatusBadRequest, msg: msg} }
func errNotFound(msg string) error   { return httpError{status: http.StatusNotFound, msg: msg} }
func errUpstream(msg string) error   { return httpError{status: http.StatusBadGateway, msg: msg} }

func statusForError(err error) int {
	var kind httpError
	if errors.As(err, &kind) && kind.status != 0 {
		return kind.status
	}
	return http.StatusInternalServerError
}
