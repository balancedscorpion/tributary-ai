package arxiv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/config"
)

// tinyPDF is a ~300 byte hand-written single-page PDF. It is valid enough to
// pass the %PDF- sniff; text extraction is expected to degrade to the
// metadata_only fallback path.
var tinyPDF = []byte(`%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>
endobj
trailer
<< /Size 4 /Root 1 0 R >>
%%EOF
`)

func singleEntryFixture(host string, badPDF bool) string {
	entryID := "http://arxiv.org/abs/2301.12345v2"
	pdfLink := host + "/pdf/2301.12345v2"
	if badPDF {
		entryID = "http://arxiv.org/abs/math/0309136v1"
		pdfLink = host + "/pdf/missing"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/" xmlns:arxiv="http://arxiv.org/schemas/atom">
  <opensearch:totalResults xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/">1</opensearch:totalResults>
  <opensearch:startIndex xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/">0</opensearch:startIndex>
  <opensearch:itemsPerPage xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/">1</opensearch:itemsPerPage>
  <entry>
    <id>` + entryID + `</id>
    <updated>2023-02-01T10:00:00Z</updated>
    <published>2023-01-28T18:00:00Z</published>
    <title>Sparse Attention Models</title>
    <summary>We study sparse attention. It works well.</summary>
    <author><name>Jane Doe</name></author>
    <author><name>John Q. Public</name></author>
    <link href="` + entryID + `" rel="alternate" type="text/html"/>
    <link title="pdf" href="` + pdfLink + `" rel="related" type="application/pdf"/>
    <arxiv:primary_category xmlns:arxiv="http://arxiv.org/schemas/atom" term="cs.LG" scheme="http://arxiv.org/schemas/atom"/>
    <category term="cs.LG" scheme="http://arxiv.org/schemas/atom"/>
  </entry>
</feed>`
}

func newFakeArxivUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/query", func(w http.ResponseWriter, r *http.Request) {
		idList := r.URL.Query().Get("id_list")
		switch {
		case strings.HasPrefix(idList, "2301.12345"):
			fmt.Fprint(w, singleEntryFixture(srv.URL, false))
		case strings.HasPrefix(idList, "math/0309136"):
			fmt.Fprint(w, singleEntryFixture(srv.URL, true))
		default:
			fmt.Fprint(w, atomFixture(srv.URL))
		}
	})
	mux.HandleFunc("/pdf/2301.12345v2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(tinyPDF)
	})
	mux.HandleFunc("/pdf/missing", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newArxivTestSettings(t *testing.T, enabled bool, email string) *config.Store {
	t.Helper()
	store, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	s := store.Get()
	on := enabled
	arx := &config.ArxivSettings{Enabled: &on}
	if email != "" {
		arx.ContactEmail = &email
	}
	s.Arxiv = arx
	if err := store.Save(s); err != nil {
		t.Fatalf("Save settings: %v", err)
	}
	return store
}

func newTestManager(t *testing.T, enabled bool) (*Manager, *config.Store) {
	t.Helper()
	upstream := newFakeArxivUpstream(t)
	settings := newArxivTestSettings(t, enabled, "research@example.com")
	m := NewManager(Options{
		CfgDir:   t.TempDir(),
		Settings: settings,
		BaseURL:  upstream.URL,
		Limiter:  NewLimiter(0, nil, nil),
	})
	return m, settings
}

func doRequest(t *testing.T, m *Manager, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	m.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (body %q)", err, rec.Body.String())
	}
	return out
}

func TestHandlerDisabledGating(t *testing.T) {
	m, _ := newTestManager(t, false)

	rec := doRequest(t, m, http.MethodGet, "/api/integrations/arxiv/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200 while disabled", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["ok"] != true || body["enabled"] != false {
		t.Fatalf("status body = %#v, want ok with enabled=false", body)
	}

	for _, probe := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/integrations/arxiv/search", `{"query":"x"}`},
		{http.MethodPost, "/api/integrations/arxiv/preview/pdf", `{"arxiv_id":"2301.12345"}`},
		{http.MethodPost, "/api/integrations/arxiv/papers", `{"arxiv_id":"2301.12345"}`},
		{http.MethodPost, "/api/integrations/arxiv/check", ""},
	} {
		rec := doRequest(t, m, probe.method, probe.path, probe.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s %s = %d, want 403 while disabled", probe.method, probe.path, rec.Code)
		}
		body := decodeBody(t, rec)
		if body["ok"] != false || body["error"] != "arxiv connection is disabled" {
			t.Fatalf("%s body = %#v, want disabled error", probe.path, body)
		}
	}

	// Local data management stays available while disabled.
	rec = doRequest(t, m, http.MethodGet, "/api/integrations/arxiv/papers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list while disabled = %d, want 200", rec.Code)
	}
	rec = doRequest(t, m, http.MethodDelete, "/api/integrations/arxiv/papers/2301.12345", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing paper while disabled = %d, want 404 (not 403)", rec.Code)
	}
}

func TestHandlerPreviewPDFDoesNotPersist(t *testing.T) {
	m, _ := newTestManager(t, true)

	rec := doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/preview/pdf", `{"arxiv_id":"2301.12345"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview pdf = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("preview content-type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `inline; filename="2301.12345.pdf"` {
		t.Fatalf("preview content-disposition = %q", cd)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("preview cache-control = %q, want no-store", rec.Header().Get("Cache-Control"))
	}
	if !strings.HasPrefix(rec.Body.String(), "%PDF-") {
		t.Fatalf("preview body prefix = %q", rec.Body.String()[:8])
	}

	rec = doRequest(t, m, http.MethodGet, "/api/integrations/arxiv/papers", "")
	body := decodeBody(t, rec)
	if rec.Code != http.StatusOK || body["total"] != float64(0) {
		t.Fatalf("list after preview = %d %#v, want empty library", rec.Code, body)
	}

	rec = doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/search", `{"ids":["2301.12345"]}`)
	body = decodeBody(t, rec)
	entries, _ := body["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("id search entries after preview = %#v", body["entries"])
	}
	first, _ := entries[0].(map[string]any)
	if first["downloaded"] != false {
		t.Fatalf("entry after preview = %#v, want downloaded=false", first)
	}
	if _, ok := first["paper_key"]; ok {
		t.Fatalf("entry after preview has paper_key = %#v, want omitted", first["paper_key"])
	}
}

func TestHandlerSearchHappyPath(t *testing.T) {
	m, _ := newTestManager(t, true)

	rec := doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/search", `{"query":"sparse attention","max_results":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["ok"] != true || body["total_results"] != float64(42) || body["items_per_page"] != float64(2) {
		t.Fatalf("search body = %#v", body)
	}
	entries, ok := body["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("entries = %#v, want 2", body["entries"])
	}
	first, _ := entries[0].(map[string]any)
	if first["arxiv_id"] != "2301.12345" || first["version"] != float64(2) || first["downloaded"] != false {
		t.Fatalf("first entry = %#v", first)
	}
	if _, ok := first["paper_key"]; ok {
		t.Fatalf("paper_key = %#v, want omitted for non-downloaded entry", first["paper_key"])
	}
	if first["title"] != "Sparse Attention Models" || first["primary_category"] != "cs.LG" {
		t.Fatalf("first entry metadata = %#v", first)
	}
}

func TestHandlerSearchPaperKey(t *testing.T) {
	m, _ := newTestManager(t, true)

	// Store the old-style paper locally so one search entry is downloaded; its
	// PDF link 404s, which still stores a metadata_only record.
	rec := doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/papers", `{"arxiv_id":"math/0309136"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d (%s), want 200", rec.Code, rec.Body.String())
	}

	rec = doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/search", `{"query":"sparse attention"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	entries, ok := body["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("entries = %#v, want 2", body["entries"])
	}
	byID := map[string]map[string]any{}
	for _, raw := range entries {
		entry, _ := raw.(map[string]any)
		id, _ := entry["arxiv_id"].(string)
		byID[id] = entry
	}

	tests := []struct {
		name           string
		arxivID        string
		wantDownloaded bool
		wantPaperKey   string
	}{
		{name: "downloaded entry carries its paper key", arxivID: "math/0309136", wantDownloaded: true, wantPaperKey: "math_0309136"},
		{name: "non-downloaded entry omits paper_key", arxivID: "2301.12345", wantDownloaded: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry, ok := byID[tc.arxivID]
			if !ok {
				t.Fatalf("entry %q missing from search results: %#v", tc.arxivID, byID)
			}
			if entry["downloaded"] != tc.wantDownloaded {
				t.Fatalf("downloaded = %#v, want %v", entry["downloaded"], tc.wantDownloaded)
			}
			key, present := entry["paper_key"]
			if tc.wantPaperKey == "" {
				if present {
					t.Fatalf("paper_key = %#v, want omitted", key)
				}
				return
			}
			if key != tc.wantPaperKey {
				t.Fatalf("paper_key = %#v, want %q", key, tc.wantPaperKey)
			}
		})
	}
}

func TestHandlerSearchValidation(t *testing.T) {
	m, _ := newTestManager(t, true)

	rec := doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/search", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty search = %d, want 400", rec.Code)
	}

	long := strings.Repeat("a", 1025)
	rec = doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/search", `{"query":"`+long+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized query = %d, want 400", rec.Code)
	}

	rec = doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/search", `not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid json = %d, want 400", rec.Code)
	}
}

func TestHandlerPaperLifecycle(t *testing.T) {
	m, _ := newTestManager(t, true)

	// Single-flight: a concurrent download of the same key returns 409.
	m.mu.Lock()
	m.downloads["2301.12345"] = true
	m.mu.Unlock()
	rec := doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/papers", `{"arxiv_id":"2301.12345"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("concurrent download = %d, want 409", rec.Code)
	}
	if body := decodeBody(t, rec); body["error"] != "download already in progress" {
		t.Fatalf("conflict body = %#v", body)
	}
	m.mu.Lock()
	delete(m.downloads, "2301.12345")
	m.mu.Unlock()

	// Download.
	rec = doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/papers", `{"arxiv_id":"arXiv:2301.12345v2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("download = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["ok"] != true || body["already_existed"] != nil {
		t.Fatalf("download body = %#v", body)
	}
	paper, _ := body["paper"].(map[string]any)
	if paper["paper_key"] != "2301.12345" || paper["arxiv_id"] != "2301.12345" || paper["version"] != float64(2) {
		t.Fatalf("paper identity = %#v", paper)
	}
	if paper["pdf_state"] != PDFStateStored {
		t.Fatalf("pdf_state = %v, want stored", paper["pdf_state"])
	}
	if paper["extract_state"] != ExtractStateMetadataOnly && paper["extract_state"] != ExtractStateExtracted {
		t.Fatalf("extract_state = %v", paper["extract_state"])
	}
	if size, _ := paper["pdf_size_bytes"].(float64); int64(size) != int64(len(tinyPDF)) {
		t.Fatalf("pdf_size_bytes = %v, want %d", paper["pdf_size_bytes"], len(tinyPDF))
	}
	if sha, _ := paper["pdf_sha256"].(string); len(sha) != 64 {
		t.Fatalf("pdf_sha256 = %q, want 64 hex chars", paper["pdf_sha256"])
	}

	// Re-download returns the stored record.
	rec = doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/papers", `{"arxiv_id":"2301.12345"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("re-download = %d, want 200", rec.Code)
	}
	if body := decodeBody(t, rec); body["already_existed"] != true {
		t.Fatalf("re-download body = %#v, want already_existed", body)
	}

	// List.
	rec = doRequest(t, m, http.MethodGet, "/api/integrations/arxiv/papers", "")
	body = decodeBody(t, rec)
	if rec.Code != http.StatusOK || body["total"] != float64(1) {
		t.Fatalf("list = %d %#v, want total 1", rec.Code, body)
	}
	if body["next_offset"] != nil {
		t.Fatalf("list next_offset = %#v, want absent", body["next_offset"])
	}

	// Filtered list.
	rec = doRequest(t, m, http.MethodGet, "/api/integrations/arxiv/papers?q=doe", "")
	if body := decodeBody(t, rec); body["total"] != float64(1) {
		t.Fatalf("author filter = %#v, want match", body)
	}
	rec = doRequest(t, m, http.MethodGet, "/api/integrations/arxiv/papers?q=zzz-no-match", "")
	if body := decodeBody(t, rec); body["total"] != float64(0) {
		t.Fatalf("no-match filter = %#v, want empty", body)
	}

	// Get.
	rec = doRequest(t, m, http.MethodGet, "/api/integrations/arxiv/papers/2301.12345", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d, want 200", rec.Code)
	}

	// Search marks it as downloaded.
	rec = doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/search", `{"ids":["2301.12345"]}`)
	body = decodeBody(t, rec)
	entries, _ := body["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("id search entries = %#v", body["entries"])
	}
	if first, _ := entries[0].(map[string]any); first["downloaded"] != true || first["paper_key"] != "2301.12345" {
		t.Fatalf("downloaded entry = %#v, want downloaded=true with paper_key", entries[0])
	}

	// PDF streaming.
	rec = doRequest(t, m, http.MethodGet, "/api/integrations/arxiv/papers/2301.12345/pdf", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("pdf = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("pdf content-type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `inline; filename="2301.12345.pdf"` {
		t.Fatalf("pdf content-disposition = %q", cd)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("pdf nosniff header missing")
	}
	if !strings.HasPrefix(rec.Body.String(), "%PDF-") {
		t.Fatalf("pdf body prefix = %q", rec.Body.String()[:8])
	}

	// Text with windowing.
	rec = doRequest(t, m, http.MethodGet, "/api/integrations/arxiv/papers/2301.12345/text?max_chars=10", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("text = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	textBody := decodeBody(t, rec)
	if textBody["ok"] != true || textBody["untrusted_note"] == "" || textBody["untrusted_note"] == nil {
		t.Fatalf("text body = %#v", textBody)
	}
	if textBody["has_more"] != true {
		t.Fatalf("text has_more = %#v, want true", textBody["has_more"])
	}
	window, _ := textBody["text"].(string)
	if len(window) == 0 || len(window) > 10 {
		t.Fatalf("text window = %q, want at most 10 bytes", window)
	}
	next, _ := textBody["next_offset"].(float64)
	if int(next) <= 0 {
		t.Fatalf("next_offset = %v, want positive", textBody["next_offset"])
	}
	totalChars, _ := textBody["total_chars"].(float64)

	// Read the remainder from next_offset and confirm the windows tile the file.
	rec = doRequest(t, m, http.MethodGet, fmt.Sprintf("/api/integrations/arxiv/papers/2301.12345/text?offset=%d&max_chars=200000", int(next)), "")
	rest := decodeBody(t, rec)
	if rest["has_more"] != false {
		t.Fatalf("rest has_more = %#v, want false", rest["has_more"])
	}
	restText, _ := rest["text"].(string)
	if len(window)+len(restText) != int(totalChars) {
		t.Fatalf("windowing mismatch: %d + %d != %d", len(window), len(restText), int(totalChars))
	}
	full := window + restText
	if !strings.Contains(full, "Sparse Attention Models") || !strings.Contains(full, "Jane Doe") {
		t.Fatalf("text content = %q, want metadata fallback containing title and authors", full)
	}

	// Status reflects stored data.
	rec = doRequest(t, m, http.MethodGet, "/api/integrations/arxiv/status", "")
	status := decodeBody(t, rec)
	if status["paper_count"] != float64(1) || status["contact_email_set"] != true {
		t.Fatalf("status = %#v", status)
	}
	if sb, _ := status["storage_bytes"].(float64); sb <= 0 {
		t.Fatalf("storage_bytes = %v, want > 0", status["storage_bytes"])
	}

	// Delete.
	rec = doRequest(t, m, http.MethodDelete, "/api/integrations/arxiv/papers/2301.12345", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d, want 200", rec.Code)
	}
	rec = doRequest(t, m, http.MethodGet, "/api/integrations/arxiv/papers/2301.12345", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete = %d, want 404", rec.Code)
	}
}

func TestHandlerPDFFailureStoresMetadataOnly(t *testing.T) {
	m, _ := newTestManager(t, true)

	rec := doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/papers", `{"arxiv_id":"math/0309136"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("download with failing pdf = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["ok"] != true {
		t.Fatalf("body = %#v, want ok despite pdf failure", body)
	}
	paper, _ := body["paper"].(map[string]any)
	if paper["pdf_state"] != PDFStateFailed || paper["extract_state"] != ExtractStateMetadataOnly {
		t.Fatalf("paper states = %#v, want failed pdf with metadata_only extract", paper)
	}
	if lastErr, _ := paper["last_error"].(string); lastErr == "" {
		t.Fatalf("last_error empty, want recorded failure")
	}

	// PDF endpoint refuses, text endpoint serves the metadata fallback.
	rec = doRequest(t, m, http.MethodGet, "/api/integrations/arxiv/papers/math_0309136/pdf", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("pdf for metadata_only paper = %d, want 404", rec.Code)
	}
	rec = doRequest(t, m, http.MethodGet, "/api/integrations/arxiv/papers/math_0309136/text", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("text for metadata_only paper = %d, want 200", rec.Code)
	}
	if textBody := decodeBody(t, rec); textBody["extract_state"] != ExtractStateMetadataOnly {
		t.Fatalf("text extract_state = %#v", textBody["extract_state"])
	}
}

func TestHandlerHostilePaperKeys(t *testing.T) {
	m, _ := newTestManager(t, true)

	for _, path := range []string{
		"/api/integrations/arxiv/papers/has..dots",
		"/api/integrations/arxiv/papers/UPPER",
		"/api/integrations/arxiv/papers/has..dots/text",
		"/api/integrations/arxiv/papers/has..dots/pdf",
	} {
		rec := doRequest(t, m, http.MethodGet, path, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("GET %s = %d, want 400", path, rec.Code)
		}
		if body := decodeBody(t, rec); body["error"] != "invalid paper key" {
			t.Fatalf("GET %s body = %#v", path, body)
		}
	}
	rec := doRequest(t, m, http.MethodDelete, "/api/integrations/arxiv/papers/has..dots", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("DELETE hostile key = %d, want 400", rec.Code)
	}

	rec = doRequest(t, m, http.MethodGet, "/api/integrations/arxiv/papers/2301.99999", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown valid key = %d, want 404", rec.Code)
	}

	rec = doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/papers", `{"arxiv_id":"../etc"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("download hostile id = %d, want 400", rec.Code)
	}
}

func TestHandlerCheckPersistsHealth(t *testing.T) {
	m, settings := newTestManager(t, true)

	rec := doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/check", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("check = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["ok"] != true || body["last_check_ok"] != true {
		t.Fatalf("check body = %#v", body)
	}
	persisted := settings.Get()
	if persisted.Arxiv == nil || persisted.Arxiv.LastCheckOk == nil || !*persisted.Arxiv.LastCheckOk || persisted.Arxiv.LastCheckedAt == nil {
		t.Fatalf("health stamps not persisted: %#v", persisted.Arxiv)
	}
}

func TestHandlerCheckFailurePersistsHealth(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)
	settings := newArxivTestSettings(t, true, "")
	m := NewManager(Options{
		CfgDir:   t.TempDir(),
		Settings: settings,
		BaseURL:  broken.URL,
		Limiter:  NewLimiter(0, nil, nil),
	})

	rec := doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/check", "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("failed check = %d, want 502", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["ok"] != false || body["error"] == nil {
		t.Fatalf("failed check body = %#v", body)
	}
	persisted := settings.Get()
	if persisted.Arxiv == nil || persisted.Arxiv.LastCheckOk == nil || *persisted.Arxiv.LastCheckOk {
		t.Fatalf("failure stamp not persisted: %#v", persisted.Arxiv)
	}
}

func TestHandlerSearchUpstreamFailure(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(broken.Close)
	m := NewManager(Options{
		CfgDir:   t.TempDir(),
		Settings: newArxivTestSettings(t, true, ""),
		BaseURL:  broken.URL,
		Limiter:  NewLimiter(0, nil, nil),
	})

	rec := doRequest(t, m, http.MethodPost, "/api/integrations/arxiv/search", `{"query":"x"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("search with broken upstream = %d, want 502", rec.Code)
	}
	if body := decodeBody(t, rec); body["ok"] != false {
		t.Fatalf("body = %#v, want ok=false", body)
	}
}

// seedPaperWithText stores a paper record plus an extracted-text file so text
// windowing can be exercised without a download.
func seedPaperWithText(t *testing.T, m *Manager, key string, text string) {
	t.Helper()
	if err := m.store.SavePaper(testPaper(key, 100)); err != nil {
		t.Fatalf("SavePaper: %v", err)
	}
	textPath, err := m.store.PaperTextPath(key)
	if err != nil {
		t.Fatalf("PaperTextPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(textPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(textPath, []byte(text), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestTextWindowMultibyteForwardProgress(t *testing.T) {
	m, _ := newTestManager(t, true)
	const key = "2301.12345"
	// Mixes 1-, 2-, 3-, and 4-byte runes so tiny windows land mid-rune.
	full := "héllo 世界 🚀 — done"
	seedPaperWithText(t, m, key, full)

	for _, maxChars := range []int{1, 3} {
		var got strings.Builder
		offset := 0
		for steps := 0; ; steps++ {
			if steps > 2*len(full)+4 {
				t.Fatalf("max_chars=%d: pagination did not terminate", maxChars)
			}
			w, err := m.GetTextWindow(key, offset, maxChars)
			if err != nil {
				t.Fatalf("GetTextWindow(offset=%d): %v", offset, err)
			}
			got.WriteString(w.Text)
			if !w.HasMore {
				if w.NextOffset != nil {
					t.Fatalf("next_offset = %d with has_more=false", *w.NextOffset)
				}
				break
			}
			if w.NextOffset == nil || *w.NextOffset <= offset {
				t.Fatalf("max_chars=%d: no forward progress at offset %d (text %q, next_offset %v)", maxChars, offset, w.Text, w.NextOffset)
			}
			offset = *w.NextOffset
		}
		if got.String() != full {
			t.Fatalf("max_chars=%d: reassembled %q, want %q", maxChars, got.String(), full)
		}
	}

	// The HTTP text handler shares the same forward-progress guarantee.
	var got strings.Builder
	offset := 0
	for steps := 0; ; steps++ {
		if steps > 2*len(full)+4 {
			t.Fatal("http pagination did not terminate")
		}
		rec := doRequest(t, m, http.MethodGet, fmt.Sprintf("/api/integrations/arxiv/papers/%s/text?offset=%d&max_chars=1", key, offset), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("text = %d (%s), want 200", rec.Code, rec.Body.String())
		}
		body := decodeBody(t, rec)
		text, _ := body["text"].(string)
		got.WriteString(text)
		if body["has_more"] != true {
			break
		}
		next, ok := body["next_offset"].(float64)
		if !ok || int(next) <= offset {
			t.Fatalf("http: no forward progress at offset %d (body %#v)", offset, body)
		}
		offset = int(next)
	}
	if got.String() != full {
		t.Fatalf("http reassembled %q, want %q", got.String(), full)
	}
}

func TestTextWindowNormalizesWrappedHyphenation(t *testing.T) {
	m, _ := newTestManager(t, true)
	const key = "2301.12345"
	seedPaperWithText(t, m, key, "A time-consuming pro-\ncess.\nDirichlet-\nCategorical stays hyphenated.\n")

	w, err := m.GetTextWindow(key, 0, 1000)
	if err != nil {
		t.Fatalf("GetTextWindow: %v", err)
	}
	want := "A time-consuming process.\nDirichlet-Categorical stays hyphenated."
	if w.Text != want {
		t.Fatalf("text = %q, want %q", w.Text, want)
	}
	if w.TotalChars != len([]byte(w.Text)) {
		t.Fatalf("total_chars = %d, want normalized byte length %d", w.TotalChars, len([]byte(w.Text)))
	}
}

func TestPageWindowReadsExtractedPages(t *testing.T) {
	m, _ := newTestManager(t, true)
	const key = "2301.12345"
	if err := m.store.SavePaper(testPaper(key, 100)); err != nil {
		t.Fatalf("SavePaper: %v", err)
	}
	pagesPath, err := m.store.PaperPagesPath(key)
	if err != nil {
		t.Fatalf("PaperPagesPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(pagesPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	manifestPath, err := m.store.PaperManifestPath(key)
	if err != nil {
		t.Fatalf("PaperManifestPath: %v", err)
	}
	if err := os.WriteFile(manifestPath, []byte(`{"pageCount":3}`), 0o600); err != nil {
		t.Fatalf("WriteFile manifest: %v", err)
	}
	pagesJSONL := strings.Join([]string{
		`{"pageNumber":1,"text":"First page.","method":"pypdf"}`,
		`{"pageNumber":2,"text":"Second page with wrapped pro-\ncess.","method":"pypdf"}`,
		`{"pageNumber":3,"text":"Third page.","method":"pypdf"}`,
	}, "\n")
	if err := os.WriteFile(pagesPath, []byte(pagesJSONL), 0o600); err != nil {
		t.Fatalf("WriteFile pages: %v", err)
	}

	progress := []PageReadProgress{}
	window, err := m.GetPageWindowWithProgress(key, 2, 1, func(event PageReadProgress) {
		progress = append(progress, event)
	})
	if err != nil {
		t.Fatalf("GetPageWindow: %v", err)
	}
	if window.PageCount != 3 || window.StartPage != 2 || window.EndPage != 2 {
		t.Fatalf("window bounds = page_count:%d start:%d end:%d", window.PageCount, window.StartPage, window.EndPage)
	}
	if !window.HasMore || window.NextPage == nil || *window.NextPage != 3 {
		t.Fatalf("next page = %#v hasMore=%v, want 3/true", window.NextPage, window.HasMore)
	}
	if len(window.Pages) != 1 || window.Pages[0].PageNumber != 2 {
		t.Fatalf("pages = %#v, want page 2 only", window.Pages)
	}
	if window.Pages[0].Text != "Second page with wrapped process." {
		t.Fatalf("normalized page text = %q", window.Pages[0].Text)
	}
	if len(progress) != 1 {
		t.Fatalf("progress events = %#v, want one selected page", progress)
	}
	if progress[0].Page.PageNumber != 2 || progress[0].PagesRead != 1 || progress[0].PageCount != 3 {
		t.Fatalf("progress page details = %#v", progress[0])
	}
	if progress[0].SourceKind != "pdf_extract" || progress[0].Title != "A Title for 2301.12345" {
		t.Fatalf("progress paper details = %#v", progress[0])
	}
}

func TestTextWindowBoundsClampsMaxChars(t *testing.T) {
	data := bytes.Repeat([]byte("a"), maxTextChars+10)
	start, end := textWindowBounds(data, 0, maxTextChars+5)
	if start != 0 || end != maxTextChars {
		t.Fatalf("window = [%d, %d), want [0, %d)", start, end, maxTextChars)
	}
}
