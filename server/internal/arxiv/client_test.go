package arxiv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// atomFixture returns a realistic two-entry Atom feed whose PDF links point
// at host (the httptest server).
func atomFixture(host string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/" xmlns:arxiv="http://arxiv.org/schemas/atom">
  <link href="http://arxiv.org/api/query" rel="self" type="application/atom+xml"/>
  <title type="html">ArXiv Query: search_query=test</title>
  <id>http://arxiv.org/api/cHxbiOdZaP56ODnBPIenZhzg5f8</id>
  <updated>2026-06-11T00:00:00-04:00</updated>
  <opensearch:totalResults xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/">42</opensearch:totalResults>
  <opensearch:startIndex xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/">5</opensearch:startIndex>
  <opensearch:itemsPerPage xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/">2</opensearch:itemsPerPage>
  <entry>
    <id>http://arxiv.org/abs/2301.12345v2</id>
    <updated>2023-02-01T10:00:00Z</updated>
    <published>2023-01-28T18:00:00Z</published>
    <title>Sparse  Attention
      Models</title>
    <summary>  We study   sparse attention.
      It works well.  </summary>
    <author>
      <name>Jane Doe</name>
    </author>
    <author>
      <name>John Q. Public</name>
    </author>
    <arxiv:doi xmlns:arxiv="http://arxiv.org/schemas/atom">10.1000/test.doi</arxiv:doi>
    <arxiv:comment xmlns:arxiv="http://arxiv.org/schemas/atom">12 pages, 3 figures</arxiv:comment>
    <link href="http://arxiv.org/abs/2301.12345v2" rel="alternate" type="text/html"/>
    <link title="pdf" href="` + host + `/pdf/2301.12345v2" rel="related" type="application/pdf"/>
    <arxiv:primary_category xmlns:arxiv="http://arxiv.org/schemas/atom" term="cs.LG" scheme="http://arxiv.org/schemas/atom"/>
    <category term="cs.LG" scheme="http://arxiv.org/schemas/atom"/>
    <category term="stat.ML" scheme="http://arxiv.org/schemas/atom"/>
  </entry>
  <entry>
    <id>http://arxiv.org/abs/math/0309136v1</id>
    <updated>2003-09-08T12:00:00Z</updated>
    <published>2003-09-08T12:00:00Z</published>
    <title>An Old Style Paper</title>
    <summary>Classic geometry result.</summary>
    <author>
      <name>Grigori Author</name>
    </author>
    <link href="http://arxiv.org/abs/math/0309136v1" rel="alternate" type="text/html"/>
    <link title="pdf" href="` + host + `/pdf/math/0309136v1" rel="related" type="application/pdf"/>
    <arxiv:primary_category xmlns:arxiv="http://arxiv.org/schemas/atom" term="math.GT" scheme="http://arxiv.org/schemas/atom"/>
    <category term="math.GT" scheme="http://arxiv.org/schemas/atom"/>
  </entry>
</feed>`
}

func newTestClient(t *testing.T, handler http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := NewClient(ClientOptions{
		BaseURL:      srv.URL,
		ContactEmail: "research@example.com",
		Limiter:      NewLimiter(0, nil, nil),
	})
	return client, srv
}

func TestBuildSearchQuery(t *testing.T) {
	got := buildSearchQuery(SearchSpec{
		Query:      `sparse "attention"`,
		Categories: []string{"cs.LG", " stat.ML ", ""},
		Authors:    []string{"Doe, Jane", ""},
	})
	want := `(cat:cs.LG OR cat:stat.ML) AND (au:"Doe, Jane") AND (ti:"sparse attention" OR abs:"sparse attention")`
	if got != want {
		t.Fatalf("buildSearchQuery = %q, want %q", got, want)
	}
	if q := buildSearchQuery(SearchSpec{}); q != "" {
		t.Fatalf("empty spec query = %q, want empty", q)
	}
}

func TestSearchBuildsQueryAndParsesAtom(t *testing.T) {
	var captured url.Values
	var capturedUA string
	var capturedPath string
	var client *Client
	mux := http.NewServeMux()
	mux.HandleFunc("/api/query", func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query()
		capturedUA = r.Header.Get("User-Agent")
		capturedPath = r.URL.Path
		fmt.Fprint(w, atomFixture("http://"+r.Host))
	})
	client, srv := newTestClient(t, mux)

	result, err := client.Search(context.Background(), SearchSpec{
		Query:      "sparse attention",
		Categories: []string{"cs.LG", "stat.ML"},
		Authors:    []string{"Doe, Jane"},
		Start:      5,
		MaxResults: 2,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if capturedPath != "/api/query" {
		t.Fatalf("path = %q, want /api/query", capturedPath)
	}
	wantQuery := `(cat:cs.LG OR cat:stat.ML) AND (au:"Doe, Jane") AND (ti:"sparse attention" OR abs:"sparse attention")`
	if got := captured.Get("search_query"); got != wantQuery {
		t.Fatalf("search_query = %q, want %q", got, wantQuery)
	}
	if captured.Get("start") != "5" || captured.Get("max_results") != "2" {
		t.Fatalf("pagination params = start=%q max_results=%q, want 5/2", captured.Get("start"), captured.Get("max_results"))
	}
	if capturedUA != "ai-analyst-local (arxiv-connection) (mailto:research@example.com)" {
		t.Fatalf("User-Agent = %q", capturedUA)
	}

	if result.TotalResults != 42 || result.StartIndex != 5 || result.ItemsPerPage != 2 {
		t.Fatalf("pagination = %+v, want totalResults=42 startIndex=5 itemsPerPage=2", result)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(result.Entries))
	}
	first := result.Entries[0]
	if first.ArxivID != "2301.12345" || first.Version != 2 {
		t.Fatalf("first id = %q v%d, want 2301.12345 v2", first.ArxivID, first.Version)
	}
	if first.Title != "Sparse Attention Models" {
		t.Fatalf("first title = %q, want collapsed whitespace", first.Title)
	}
	if first.Summary != "We study sparse attention. It works well." {
		t.Fatalf("first summary = %q, want collapsed whitespace", first.Summary)
	}
	if len(first.Authors) != 2 || first.Authors[0] != "Jane Doe" || first.Authors[1] != "John Q. Public" {
		t.Fatalf("first authors = %#v", first.Authors)
	}
	if first.PrimaryCategory != "cs.LG" || len(first.Categories) != 2 || first.Categories[1] != "stat.ML" {
		t.Fatalf("first categories = %q %#v", first.PrimaryCategory, first.Categories)
	}
	if first.DOI != "10.1000/test.doi" || first.Comment != "12 pages, 3 figures" {
		t.Fatalf("first doi/comment = %q / %q", first.DOI, first.Comment)
	}
	if !strings.HasSuffix(first.PDFURL, "/pdf/2301.12345v2") || !strings.Contains(first.PDFURL, srv.Listener.Addr().String()) {
		t.Fatalf("first pdf url = %q, want test server pdf link", first.PDFURL)
	}
	if first.AbsURL != "http://arxiv.org/abs/2301.12345v2" {
		t.Fatalf("first abs url = %q", first.AbsURL)
	}
	wantPublished := time.Date(2023, 1, 28, 18, 0, 0, 0, time.UTC)
	if !first.Published.Equal(wantPublished) {
		t.Fatalf("first published = %v, want %v", first.Published, wantPublished)
	}
	second := result.Entries[1]
	if second.ArxivID != "math/0309136" || second.Version != 1 || second.PrimaryCategory != "math.GT" {
		t.Fatalf("second entry = %+v, want old-style math/0309136 v1", second)
	}
}

func TestSearchIDList(t *testing.T) {
	var captured url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/api/query", func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query()
		fmt.Fprint(w, atomFixture("http://"+r.Host))
	})
	client, _ := newTestClient(t, mux)

	_, err := client.Search(context.Background(), SearchSpec{
		IDs:        []string{"arXiv:2301.12345v2", "https://arxiv.org/abs/math/0309136"},
		MaxResults: 2,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got := captured.Get("id_list"); got != "2301.12345v2,math/0309136" {
		t.Fatalf("id_list = %q, want canonical ids", got)
	}
	if captured.Get("search_query") != "" {
		t.Fatalf("search_query = %q, want empty for id_list lookups", captured.Get("search_query"))
	}

	if _, err := client.Search(context.Background(), SearchSpec{IDs: []string{"../etc"}}); err == nil {
		t.Fatalf("Search with hostile id succeeded, want error")
	}
}

func TestSearch429RetryHonored(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/query", func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, atomFixture("http://"+r.Host))
	})
	client, _ := newTestClient(t, mux)

	result, err := client.Search(context.Background(), SearchSpec{Query: "electron", MaxResults: 1})
	if err != nil {
		t.Fatalf("Search after 429: %v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("upstream hits = %d, want exactly one retry", hits.Load())
	}
	if result.TotalResults != 42 {
		t.Fatalf("retry result totalResults = %d, want 42", result.TotalResults)
	}
}

func TestSearchPersistent429Fails(t *testing.T) {
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/query", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	client, _ := newTestClient(t, mux)

	if _, err := client.Search(context.Background(), SearchSpec{Query: "electron"}); err == nil {
		t.Fatalf("Search with persistent 429 succeeded, want error")
	}
	if hits.Load() != 2 {
		t.Fatalf("upstream hits = %d, want exactly 2 (one retry)", hits.Load())
	}
}

func TestCheckUsesSingleResultQuery(t *testing.T) {
	var captured url.Values
	mux := http.NewServeMux()
	mux.HandleFunc("/api/query", func(w http.ResponseWriter, r *http.Request) {
		captured = r.URL.Query()
		fmt.Fprint(w, atomFixture("http://"+r.Host))
	})
	client, _ := newTestClient(t, mux)
	if err := client.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if captured.Get("max_results") != "1" {
		t.Fatalf("max_results = %q, want 1", captured.Get("max_results"))
	}
}

func TestDownloadPDFHappyPath(t *testing.T) {
	pdfBody := []byte("%PDF-1.4\n1 0 obj << >> endobj\ntrailer\n%%EOF\n")
	var capturedUA string
	mux := http.NewServeMux()
	mux.HandleFunc("/pdf/2301.12345v2", func(w http.ResponseWriter, r *http.Request) {
		capturedUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(pdfBody)
	})
	client, srv := newTestClient(t, mux)

	var buf bytes.Buffer
	written, sum, err := client.DownloadPDF(context.Background(), srv.URL+"/pdf/2301.12345v2", &buf, 1<<20)
	if err != nil {
		t.Fatalf("DownloadPDF: %v", err)
	}
	if written != int64(len(pdfBody)) || !bytes.Equal(buf.Bytes(), pdfBody) {
		t.Fatalf("written = %d bytes, want %d byte body", written, len(pdfBody))
	}
	wantSum := sha256.Sum256(pdfBody)
	if sum != hex.EncodeToString(wantSum[:]) {
		t.Fatalf("sha256 = %q, want %q", sum, hex.EncodeToString(wantSum[:]))
	}
	if !strings.Contains(capturedUA, "arxiv-connection") {
		t.Fatalf("User-Agent = %q, want arxiv-connection UA", capturedUA)
	}
}

func TestDownloadPDFRejectsNonPDFMagic(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/pdf/bad", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<html>not a pdf</html>")
	})
	client, srv := newTestClient(t, mux)

	var buf bytes.Buffer
	if _, _, err := client.DownloadPDF(context.Background(), srv.URL+"/pdf/bad", &buf, 1<<20); err == nil || !strings.Contains(err.Error(), "%PDF-") {
		t.Fatalf("DownloadPDF with html body = %v, want magic error", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("destination received %d bytes before sniff rejection, want 0", buf.Len())
	}
}

func TestDownloadPDFEnforcesMaxBytes(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/pdf/huge", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("%PDF-1.4\n"))
		_, _ = w.Write(bytes.Repeat([]byte("a"), 4096))
	})
	client, srv := newTestClient(t, mux)

	var buf bytes.Buffer
	if _, _, err := client.DownloadPDF(context.Background(), srv.URL+"/pdf/huge", &buf, 64); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("DownloadPDF over limit = %v, want size error", err)
	}
}

func TestDownloadPDFRedirectValidation(t *testing.T) {
	pdfBody := []byte("%PDF-1.4\nok\n%%EOF\n")
	mux := http.NewServeMux()
	mux.HandleFunc("/pdf/redirect-ok", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/pdf/final", http.StatusFound)
	})
	mux.HandleFunc("/pdf/final", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(pdfBody)
	})
	mux.HandleFunc("/pdf/redirect-evil", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.com/paper.pdf", http.StatusFound)
	})
	loop := func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/pdf/loop", http.StatusFound)
	}
	mux.HandleFunc("/pdf/loop", loop)
	client, srv := newTestClient(t, mux)

	var buf bytes.Buffer
	if _, _, err := client.DownloadPDF(context.Background(), srv.URL+"/pdf/redirect-ok", &buf, 1<<20); err != nil {
		t.Fatalf("same-host redirect: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), pdfBody) {
		t.Fatalf("redirected body = %q, want pdf body", buf.Bytes())
	}

	if _, _, err := client.DownloadPDF(context.Background(), srv.URL+"/pdf/redirect-evil", &buf, 1<<20); err == nil || !strings.Contains(err.Error(), "not an allowed arxiv host") {
		t.Fatalf("cross-host redirect = %v, want host allowlist error", err)
	}

	if _, _, err := client.DownloadPDF(context.Background(), srv.URL+"/pdf/loop", &buf, 1<<20); err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("redirect loop = %v, want max redirect error", err)
	}
}

func TestDownloadPDFURLValidation(t *testing.T) {
	mux := http.NewServeMux()
	var hit atomic.Bool
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { hit.Store(true) })
	client, _ := newTestClient(t, mux)

	var buf bytes.Buffer
	cases := []string{
		"https://evil.example.com/paper.pdf",
		"http://evil.example.com/paper.pdf",
		"ftp://arxiv.org/paper.pdf",
		"://bad",
	}
	for _, raw := range cases {
		if _, _, err := client.DownloadPDF(context.Background(), raw, &buf, 1<<20); err == nil {
			t.Fatalf("DownloadPDF(%q) succeeded, want validation error", raw)
		}
	}
	if hit.Load() {
		t.Fatalf("validation failures must not reach the network")
	}

	u, err := client.normalizeDownloadURL("http://arxiv.org/pdf/2301.12345v2")
	if err != nil {
		t.Fatalf("normalizeDownloadURL: %v", err)
	}
	if u.Scheme != "https" || u.Host != "arxiv.org" {
		t.Fatalf("normalized url = %q, want https upgrade for arxiv.org", u.String())
	}
	for _, host := range []string{"www.arxiv.org", "export.arxiv.org"} {
		if _, err := client.normalizeDownloadURL("https://" + host + "/pdf/x"); err != nil {
			t.Fatalf("normalizeDownloadURL(%s): %v", host, err)
		}
	}
}

func TestSearchRejectsOversizedBody(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/query", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/atom+xml")
		_, _ = w.Write([]byte("<feed>"))
		_, _ = w.Write(bytes.Repeat([]byte(" "), maxMetadataBytes))
	})
	client, _ := newTestClient(t, mux)

	if _, err := client.Search(context.Background(), SearchSpec{Query: "x"}); err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("oversized atom body = %v, want size error", err)
	}
}
