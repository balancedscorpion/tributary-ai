package arxiv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBaseURL  = "https://export.arxiv.org"
	baseUserAgent   = "ai-analyst-local (arxiv-connection)"
	metadataTimeout = 30 * time.Second
	pdfTimeout      = 5 * time.Minute
	// maxMetadataBytes caps Atom response bodies.
	maxMetadataBytes = 8 << 20
	// DefaultMaxPDFBytes caps stored PDFs.
	DefaultMaxPDFBytes = 100 << 20
	maxRetryAfter      = 60 * time.Second
	maxSearchResults   = 100
)

// pdfHosts is the production allowlist for PDF downloads (https only).
var pdfHosts = map[string]bool{
	"arxiv.org":        true,
	"www.arxiv.org":    true,
	"export.arxiv.org": true,
}

// ClientOptions configures a Client.
type ClientOptions struct {
	// BaseURL defaults to https://export.arxiv.org.
	BaseURL string
	// ContactEmail, when set, is appended to the User-Agent per arXiv's
	// politeness guidance.
	ContactEmail string
	// Limiter enforces global request spacing; a default 3s limiter is
	// created when nil.
	Limiter *Limiter
	// HTTPClient defaults to a plain http.Client.
	HTTPClient *http.Client
}

// Client talks to the arXiv export API.
type Client struct {
	baseURL   string
	httpc     *http.Client
	limiter   *Limiter
	userAgent string

	// allowedHosts is the PDF download allowlist. The configured base URL's
	// host/scheme is additionally trusted so tests can point at a local
	// httptest server.
	allowedHosts map[string]bool
	baseHost     string
	baseScheme   string
}

// NewClient builds a Client from opts, applying defaults.
func NewClient(opts ClientOptions) *Client {
	base := strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	limiter := opts.Limiter
	if limiter == nil {
		limiter = NewLimiter(DefaultMinInterval, nil, nil)
	}
	httpc := opts.HTTPClient
	if httpc == nil {
		httpc = &http.Client{}
	}
	c := &Client{
		baseURL:      base,
		httpc:        httpc,
		limiter:      limiter,
		userAgent:    userAgentFor(opts.ContactEmail),
		allowedHosts: pdfHosts,
	}
	if u, err := url.Parse(base); err == nil {
		c.baseHost = strings.ToLower(u.Host)
		c.baseScheme = strings.ToLower(u.Scheme)
	}
	return c
}

func userAgentFor(contactEmail string) string {
	ua := baseUserAgent
	if email := strings.TrimSpace(contactEmail); email != "" {
		ua += " (mailto:" + email + ")"
	}
	return ua
}

// WithContactEmail returns a shallow copy of the client whose User-Agent
// reflects email. The limiter and HTTP client are shared.
func (c *Client) WithContactEmail(email string) *Client {
	out := *c
	out.userAgent = userAgentFor(email)
	return &out
}

// SearchSpec describes an arXiv query. When IDs is non-empty an id_list
// lookup is performed; otherwise the other fields are combined into a
// search_query (groups ANDed, members ORed).
type SearchSpec struct {
	Query      string
	Categories []string
	Authors    []string
	IDs        []string
	Start      int
	MaxResults int
}

// Entry is one Atom feed entry from the export API.
type Entry struct {
	ArxivID         string
	Version         int
	Title           string
	Summary         string
	Authors         []string
	Categories      []string
	PrimaryCategory string
	Published       time.Time
	Updated         time.Time
	PDFURL          string
	AbsURL          string
	DOI             string
	Comment         string
}

// SearchResult carries entries plus opensearch pagination metadata.
type SearchResult struct {
	TotalResults int
	StartIndex   int
	ItemsPerPage int
	Entries      []Entry
}

// Search executes a query against {base}/api/query.
func (c *Client) Search(ctx context.Context, spec SearchSpec) (SearchResult, error) {
	params := url.Values{}
	if len(spec.IDs) > 0 {
		ids := make([]string, 0, len(spec.IDs))
		for _, raw := range spec.IDs {
			id, version, err := CanonicalID(raw)
			if err != nil {
				return SearchResult{}, err
			}
			if version > 0 {
				id = fmt.Sprintf("%sv%d", id, version)
			}
			ids = append(ids, id)
		}
		params.Set("id_list", strings.Join(ids, ","))
	} else {
		query := buildSearchQuery(spec)
		if query == "" {
			return SearchResult{}, errors.New("search requires a query, categories, authors, or ids")
		}
		params.Set("search_query", query)
	}
	start := spec.Start
	if start < 0 {
		start = 0
	}
	maxResults := spec.MaxResults
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > maxSearchResults {
		maxResults = maxSearchResults
	}
	params.Set("start", strconv.Itoa(start))
	params.Set("max_results", strconv.Itoa(maxResults))

	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	target := c.baseURL + "/api/query?" + params.Encode()
	resp, err := c.do(ctx, c.httpc, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/atom+xml")
		return req, nil
	})
	if err != nil {
		return SearchResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SearchResult{}, fmt.Errorf("arxiv api error (HTTP %d)", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes+1))
	if err != nil {
		return SearchResult{}, err
	}
	if int64(len(body)) > maxMetadataBytes {
		return SearchResult{}, errors.New("arxiv api response exceeds 8 MiB limit")
	}
	return parseAtomFeed(body)
}

// Check probes the export API with a minimal query.
func (c *Client) Check(ctx context.Context) error {
	_, err := c.Search(ctx, SearchSpec{Query: "electron", MaxResults: 1})
	return err
}

// DownloadPDF fetches pdfURL through the limiter, validating the host
// allowlist on the initial URL and on every redirect hop, sniffing the
// %PDF- magic before writing, enforcing maxBytes while streaming, and
// returning the byte count and SHA-256 of the written content.
func (c *Client) DownloadPDF(ctx context.Context, pdfURL string, dst io.Writer, maxBytes int64) (int64, string, error) {
	if dst == nil {
		return 0, "", errors.New("download destination is required")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxPDFBytes
	}
	u, err := c.normalizeDownloadURL(pdfURL)
	if err != nil {
		return 0, "", err
	}

	ctx, cancel := context.WithTimeout(ctx, pdfTimeout)
	defer cancel()

	// Shallow copy so the shared client's redirect policy is untouched.
	dlClient := *c.httpc
	dlClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 4 {
			return errors.New("stopped after 3 redirects")
		}
		return c.validateDownloadURL(req.URL)
	}
	target := u.String()
	resp, err := c.do(ctx, &dlClient, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/pdf")
		return req, nil
	})
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, "", fmt.Errorf("arxiv pdf download failed (HTTP %d)", resp.StatusCode)
	}

	head := make([]byte, 5)
	if _, err := io.ReadFull(resp.Body, head); err != nil {
		return 0, "", errors.New("arxiv pdf response is too short to be a PDF")
	}
	if string(head) != "%PDF-" {
		return 0, "", errors.New("arxiv pdf response does not start with %PDF-")
	}
	if int64(len(head)) > maxBytes {
		return 0, "", fmt.Errorf("arxiv pdf exceeds the %d byte limit", maxBytes)
	}

	hash := sha256.New()
	w := io.MultiWriter(dst, hash)
	if _, err := w.Write(head); err != nil {
		return 0, "", err
	}
	written := int64(len(head))
	remaining := maxBytes - written
	n, err := io.Copy(w, io.LimitReader(resp.Body, remaining+1))
	written += n
	if err != nil {
		return written, "", err
	}
	if written > maxBytes {
		return written, "", fmt.Errorf("arxiv pdf exceeds the %d byte limit", maxBytes)
	}
	return written, hex.EncodeToString(hash.Sum(nil)), nil
}

// do issues a request after Limiter.Wait, honoring Retry-After on a 429 with
// exactly one retry.
func (c *Client) do(ctx context.Context, httpc *http.Client, build func() (*http.Request, error)) (*http.Response, error) {
	resp, err := c.doOnce(ctx, httpc, build)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		return resp, nil
	}
	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
	if err := sleepCtx(ctx, retryAfter); err != nil {
		return nil, err
	}
	resp, err = c.doOnce(ctx, httpc, build)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		return nil, errors.New("arxiv api rate limited (HTTP 429)")
	}
	return resp, nil
}

func (c *Client) doOnce(ctx context.Context, httpc *http.Client, build func() (*http.Request, error)) (*http.Response, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	req, err := build()
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	return httpc.Do(req)
}

func (c *Client) normalizeDownloadURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid pdf url: %v", err)
	}
	// arXiv Atom feeds historically link http://arxiv.org/pdf/...; upgrade
	// known hosts to https before validating.
	if strings.ToLower(u.Scheme) == "http" && c.allowedHosts[strings.ToLower(u.Host)] {
		u.Scheme = "https"
	}
	if err := c.validateDownloadURL(u); err != nil {
		return nil, err
	}
	return u, nil
}

func (c *Client) validateDownloadURL(u *url.URL) error {
	if u == nil {
		return errors.New("pdf url is required")
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	if c.baseHost != "" && host == c.baseHost && scheme == c.baseScheme {
		return nil
	}
	if scheme != "https" {
		return fmt.Errorf("pdf url must use https, got %q", u.Scheme)
	}
	if !c.allowedHosts[host] {
		return fmt.Errorf("pdf url host %q is not an allowed arxiv host", u.Host)
	}
	return nil
}

func buildSearchQuery(spec SearchSpec) string {
	groups := []string{}
	if parts := categoryTerms(spec.Categories); len(parts) > 0 {
		groups = append(groups, orGroup(parts))
	}
	if parts := authorTerms(spec.Authors); len(parts) > 0 {
		groups = append(groups, orGroup(parts))
	}
	if phrase := sanitizePhrase(spec.Query); phrase != "" {
		groups = append(groups, orGroup([]string{
			`ti:"` + phrase + `"`,
			`abs:"` + phrase + `"`,
		}))
	}
	return strings.Join(groups, " AND ")
}

func categoryTerms(categories []string) []string {
	out := []string{}
	for _, raw := range categories {
		cat := sanitizeCategory(raw)
		if cat == "" {
			continue
		}
		out = append(out, "cat:"+cat)
	}
	return out
}

func authorTerms(authors []string) []string {
	out := []string{}
	for _, raw := range authors {
		author := sanitizePhrase(raw)
		if author == "" {
			continue
		}
		out = append(out, `au:"`+author+`"`)
	}
	return out
}

func orGroup(parts []string) string {
	return "(" + strings.Join(parts, " OR ") + ")"
}

// sanitizePhrase collapses whitespace and strips quote characters so the
// value can be embedded inside a quoted field query.
func sanitizePhrase(raw string) string {
	raw = strings.ReplaceAll(raw, `"`, " ")
	return strings.Join(strings.Fields(raw), " ")
}

// sanitizeCategory keeps the characters valid in arXiv category names.
func sanitizeCategory(raw string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func parseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	secs, err := strconv.Atoi(header)
	if err != nil || secs < 0 {
		return 0
	}
	d := time.Duration(secs) * time.Second
	if d > maxRetryAfter {
		d = maxRetryAfter
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type atomFeed struct {
	XMLName      xml.Name    `xml:"feed"`
	TotalResults int         `xml:"http://a9.com/-/spec/opensearch/1.1/ totalResults"`
	StartIndex   int         `xml:"http://a9.com/-/spec/opensearch/1.1/ startIndex"`
	ItemsPerPage int         `xml:"http://a9.com/-/spec/opensearch/1.1/ itemsPerPage"`
	Entries      []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID         string       `xml:"id"`
	Title      string       `xml:"title"`
	Summary    string       `xml:"summary"`
	Published  string       `xml:"published"`
	Updated    string       `xml:"updated"`
	Authors    []atomAuthor `xml:"author"`
	Links      []atomLink   `xml:"link"`
	Categories []atomTerm   `xml:"category"`
	Primary    atomTerm     `xml:"http://arxiv.org/schemas/atom primary_category"`
	DOI        string       `xml:"http://arxiv.org/schemas/atom doi"`
	Comment    string       `xml:"http://arxiv.org/schemas/atom comment"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomLink struct {
	Href  string `xml:"href,attr"`
	Rel   string `xml:"rel,attr"`
	Type  string `xml:"type,attr"`
	Title string `xml:"title,attr"`
}

type atomTerm struct {
	Term string `xml:"term,attr"`
}

func parseAtomFeed(body []byte) (SearchResult, error) {
	var feed atomFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return SearchResult{}, fmt.Errorf("arxiv api returned invalid atom xml: %v", err)
	}
	out := SearchResult{
		TotalResults: feed.TotalResults,
		StartIndex:   feed.StartIndex,
		ItemsPerPage: feed.ItemsPerPage,
		Entries:      make([]Entry, 0, len(feed.Entries)),
	}
	for _, raw := range feed.Entries {
		entry := Entry{
			Title:           collapseWhitespace(raw.Title),
			Summary:         collapseWhitespace(raw.Summary),
			PrimaryCategory: strings.TrimSpace(raw.Primary.Term),
			DOI:             strings.TrimSpace(raw.DOI),
			Comment:         collapseWhitespace(raw.Comment),
		}
		if id, version, err := CanonicalID(raw.ID); err == nil {
			entry.ArxivID = id
			entry.Version = version
		} else {
			// Skip entries whose ID cannot be canonicalized (e.g. the API's
			// error placeholder entries).
			continue
		}
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw.Published)); err == nil {
			entry.Published = t
		}
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw.Updated)); err == nil {
			entry.Updated = t
		}
		for _, author := range raw.Authors {
			if name := collapseWhitespace(author.Name); name != "" {
				entry.Authors = append(entry.Authors, name)
			}
		}
		for _, cat := range raw.Categories {
			if term := strings.TrimSpace(cat.Term); term != "" {
				entry.Categories = append(entry.Categories, term)
			}
		}
		for _, link := range raw.Links {
			href := strings.TrimSpace(link.Href)
			if href == "" {
				continue
			}
			switch {
			case strings.EqualFold(link.Title, "pdf") || strings.EqualFold(link.Type, "application/pdf"):
				entry.PDFURL = href
			case strings.EqualFold(link.Rel, "alternate"):
				entry.AbsURL = href
			}
		}
		if entry.AbsURL == "" {
			entry.AbsURL = strings.TrimSpace(raw.ID)
		}
		out.Entries = append(out.Entries, entry)
	}
	return out, nil
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
