package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"

	arxivapi "ai-analyst-local/internal/arxiv"
	"ai-analyst-local/internal/chat/store"
	chattypes "ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

func TestArxivToolNamesAndLookup(t *testing.T) {
	names := ArxivToolNames()
	if len(names) != 10 {
		t.Fatalf("len(ArxivToolNames())=%d, want 10", len(names))
	}

	seen := map[string]bool{}
	for _, name := range names {
		if name == "" {
			t.Fatal("ArxivToolNames contains empty name")
		}
		if seen[name] {
			t.Fatalf("ArxivToolNames contains duplicate %q", name)
		}
		seen[name] = true
		if !IsArxivToolName(name) {
			t.Fatalf("IsArxivToolName(%q)=false, want true", name)
		}
	}
	if IsArxivToolName("arxiv_delete_paper") {
		t.Fatal("IsArxivToolName accepted a destructive tool")
	}
	if !IsArxivToolName(ArxivToolScreeningReport) {
		t.Fatal("IsArxivToolName rejected arxiv_screening_report")
	}
	if !IsArxivToolName(ArxivToolRecommendations) {
		t.Fatal("IsArxivToolName rejected arxiv_recommendations")
	}

	names[0] = "mutated"
	if ArxivToolNames()[0] == "mutated" {
		t.Fatal("ArxivToolNames should return a defensive copy")
	}
}

func TestArxivToolDefinitionsAreGated(t *testing.T) {
	registry := NewRegistry(nil)
	if defs := registry.ArxivToolDefinitions(false); len(defs) != 0 {
		t.Fatalf("disabled arXiv definitions=%d, want 0", len(defs))
	}
	if defs := registry.ArxivToolDefinitions(true); len(defs) != 0 {
		t.Fatalf("arXiv definitions without manager=%d, want 0", len(defs))
	}

	manager := arxivapi.NewManager(arxivapi.Options{CfgDir: t.TempDir()})
	registry = NewRegistry(&RegistryOptions{Arxiv: manager})
	if defs := registry.ArxivToolDefinitions(true); len(defs) != len(ArxivToolNames()) {
		t.Fatalf("enabled arXiv definitions=%d, want %d", len(defs), len(ArxivToolNames()))
	}
	if defs := registry.Definitions(); toolDefsContain(defs, ArxivToolStatus) {
		t.Fatal("arXiv tools should not be included in default definitions")
	}
}

func TestArxivToolsUseFirstClassResultMetadata(t *testing.T) {
	raw, err := os.ReadFile("arxiv_tools.go")
	if err != nil {
		t.Fatalf("read arxiv_tools.go: %v", err)
	}
	if strings.Contains(string(raw), `"result_metadata"`) {
		t.Fatal("arXiv tools should set Output.ResultMetadata directly instead of nesting result_metadata in result maps")
	}

	out := arxivOutput(ArxivToolSearch, map[string]any{"ok": true}, "search_results")
	if out.ResultMetadata["kind"] != "search_results" ||
		out.ResultMetadata["integration"] != "arxiv" ||
		out.ResultMetadata["readonly"] != true ||
		out.ResultMetadata["tool_name"] != ArxivToolSearch ||
		out.ResultMetadata["tool_set_id"] != "arxiv" ||
		out.ResultMetadata["category"] != "read" ||
		out.ResultMetadata["origin"] != "connected" ||
		out.ResultMetadata["renderer_strategy"] != "family" ||
		out.ResultMetadata["renderer_key"] != "arxiv" {
		t.Fatalf("ResultMetadata=%#v, want arXiv search metadata", out.ResultMetadata)
	}
	if _, ok := out.ResultMetadata["tool_presentation"].(map[string]any); !ok {
		t.Fatalf("ResultMetadata missing tool_presentation: %#v", out.ResultMetadata)
	}
	if result, ok := out.Result.(map[string]any); ok {
		if _, nested := result["result_metadata"]; nested {
			t.Fatalf("result contains nested result_metadata: %#v", result)
		}
	}
}

func TestArxivResultMetadataUsesCatalogPresentation(t *testing.T) {
	cases := []struct {
		toolName string
		kind     string
	}{
		{ArxivToolStatus, "status"},
		{ArxivToolCheck, "connection_status"},
		{ArxivToolSearch, "search_results"},
		{ArxivToolScreeningReport, "screening_report"},
		{ArxivToolRecommendations, "recommendation_set"},
		{ArxivToolDownloadPaper, "downloaded_paper"},
		{ArxivToolListPapers, "paper_list"},
		{ArxivToolGetPaper, "paper"},
		{ArxivToolGetPaperText, "paper_text"},
		{ArxivToolReadPaper, "paper_pages"},
	}
	for _, tc := range cases {
		t.Run(tc.toolName, func(t *testing.T) {
			meta := arxivResultMetadata(tc.toolName, tc.kind)
			toolMeta, ok := ToolMetadataByName(tc.toolName)
			if !ok {
				t.Fatalf("missing catalog metadata for %s", tc.toolName)
			}
			expected := map[string]any{
				"provider":      "arxiv",
				"integration":   "arxiv",
				"kind":          tc.kind,
				"readonly":      true,
				"tool_name":     tc.toolName,
				"tool_set_id":   "arxiv",
				"tool_set_name": "arXiv",
				"connector_id":  "arxiv",
				"category":      string(ToolCategoryRead),
				"origin":        string(ToolOriginConnected),
			}
			for key, want := range expected {
				if got := meta[key]; got != want {
					t.Fatalf("%s metadata[%s]=%#v, want %#v; metadata=%#v", tc.toolName, key, got, want, meta)
				}
			}
			presentation, ok := meta["tool_presentation"].(map[string]any)
			if !ok {
				t.Fatalf("%s tool_presentation=%T, want map[string]any; metadata=%#v", tc.toolName, meta["tool_presentation"], meta)
			}
			for key, want := range map[string]any{
				"family":            toolMeta.Presentation.Family,
				"result_kind":       toolMeta.Presentation.ResultKind,
				"native_viewer":     toolMeta.Presentation.NativeViewer,
				"renderer_strategy": toolMeta.Presentation.RendererStrategy,
				"renderer_key":      toolMeta.Presentation.RendererKey,
			} {
				if got := presentation[key]; got != want {
					t.Fatalf("%s tool_presentation[%s]=%#v, want %#v; presentation=%#v", tc.toolName, key, got, want, presentation)
				}
			}
			if want := toolMeta.Presentation.FileCapability; want != "" && presentation["file_capability"] != want {
				t.Fatalf("%s tool_presentation.file_capability=%#v, want %#v; presentation=%#v", tc.toolName, presentation["file_capability"], want, presentation)
			}
		})
	}
}

func TestArxivToolSchemasExposeReadOnlyInputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool Tool
	}{
		{ArxivToolStatus, newArxivStatusTool(nil)},
		{ArxivToolCheck, newArxivCheckConnectionTool(nil)},
		{ArxivToolSearch, newArxivSearchTool(nil)},
		{ArxivToolDownloadPaper, newArxivDownloadPaperTool(nil)},
		{ArxivToolListPapers, newArxivListPapersTool(nil)},
		{ArxivToolGetPaper, newArxivGetPaperTool(nil)},
		{ArxivToolGetPaperText, newArxivGetPaperTextTool(nil)},
		{ArxivToolReadPaper, newArxivReadPaperTool(nil)},
		{ArxivToolScreeningReport, newArxivScreeningReportTool(nil)},
		{ArxivToolRecommendations, newArxivRecommendationsTool(nil)},
	} {
		t.Run(tc.name+"_closed_schema", func(t *testing.T) {
			requireArxivSchemaObjectsClosed(t, tc.tool.Definition().Function.Parameters, tc.name)
		})
	}

	searchDef := newArxivSearchTool(nil).Definition()
	properties := toolDefinitionProperties(t, searchDef.Function.Parameters)
	for _, key := range []string{"query", "categories", "authors", "ids", "start", "max_results"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("arxiv_search schema missing %s", key)
		}
	}
	for _, key := range []string{"token", "api_key", "access_token", "pdf_bytes"} {
		if _, ok := properties[key]; ok {
			t.Fatalf("arXiv search schema should not expose %s", key)
		}
	}

	downloadDef := newArxivDownloadPaperTool(nil).Definition()
	properties = toolDefinitionProperties(t, downloadDef.Function.Parameters)
	if _, ok := properties["arxiv_id"]; !ok {
		t.Fatal("arxiv_download_paper schema missing arxiv_id")
	}
	required := toolDefinitionRequired(t, downloadDef.Function.Parameters)
	if !stringSliceContains(required, "arxiv_id") {
		t.Fatalf("arxiv_download_paper required=%v, want arxiv_id", required)
	}

	textDef := newArxivGetPaperTextTool(nil).Definition()
	properties = toolDefinitionProperties(t, textDef.Function.Parameters)
	for _, key := range []string{"paper", "offset", "max_chars"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("arxiv_get_paper_text schema missing %s", key)
		}
	}

	readDef := newArxivReadPaperTool(nil).Definition()
	properties = toolDefinitionProperties(t, readDef.Function.Parameters)
	for _, key := range []string{"paper", "start_page", "max_pages"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("arxiv_read_paper schema missing %s", key)
		}
	}
	required = toolDefinitionRequired(t, readDef.Function.Parameters)
	if !stringSliceContains(required, "paper") {
		t.Fatalf("arxiv_read_paper required=%v, want paper", required)
	}

	reportDef := newArxivScreeningReportTool(nil).Definition()
	properties = toolDefinitionProperties(t, reportDef.Function.Parameters)
	for _, key := range []string{"interpreted_request", "concepts", "corpus_gaps", "query_rationales", "verdicts"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("arxiv_screening_report schema missing %s", key)
		}
	}
	for _, key := range []string{"token", "api_key", "access_token", "pdf_bytes"} {
		if _, ok := properties[key]; ok {
			t.Fatalf("arxiv_screening_report schema should not expose %s", key)
		}
	}

	recommendationsDef := newArxivRecommendationsTool(nil).Definition()
	properties = toolDefinitionProperties(t, recommendationsDef.Function.Parameters)
	for _, key := range []string{"screening_report_tool_call_id", "selection_basis", "recommendations"} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("arxiv_recommendations schema missing %s", key)
		}
	}
	required = toolDefinitionRequired(t, recommendationsDef.Function.Parameters)
	if !stringSliceContains(required, "recommendations") {
		t.Fatalf("arxiv_recommendations required=%v, want recommendations", required)
	}
	for _, key := range []string{"title", "summary", "authors", "token", "api_key", "access_token", "pdf_bytes"} {
		if _, ok := properties[key]; ok {
			t.Fatalf("arxiv_recommendations schema should not expose %s", key)
		}
	}
}

func requireArxivSchemaObjectsClosed(t *testing.T, value any, path string) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if schemaType, _ := typed["type"].(string); schemaType == "object" || typed["properties"] != nil {
			if got, ok := typed["additionalProperties"].(bool); !ok || got {
				t.Fatalf("%s additionalProperties=%#v, want false", path, typed["additionalProperties"])
			}
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			requireArxivSchemaObjectsClosed(t, typed[key], path+"."+key)
		}
	case []any:
		for idx, entry := range typed {
			requireArxivSchemaObjectsClosed(t, entry, fmt.Sprintf("%s[%d]", path, idx))
		}
	}
}

// arxivSearchFeed is a minimal two-entry Atom feed. The old-style entry comes
// first so an id_list download for math/0309136 resolves to the right entry.
// PDF links point at unhandled paths so downloads degrade to metadata_only.
func arxivSearchFeed(host string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom" xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/" xmlns:arxiv="http://arxiv.org/schemas/atom">
  <opensearch:totalResults xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/">2</opensearch:totalResults>
  <opensearch:startIndex xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/">0</opensearch:startIndex>
  <opensearch:itemsPerPage xmlns:opensearch="http://a9.com/-/spec/opensearch/1.1/">2</opensearch:itemsPerPage>
  <entry>
    <id>http://arxiv.org/abs/math/0309136v1</id>
    <updated>2003-09-08T12:00:00Z</updated>
    <published>2003-09-08T12:00:00Z</published>
    <title>An Old Style Paper</title>
    <summary>Classic geometry result.</summary>
    <author><name>Grigori Author</name></author>
    <link href="http://arxiv.org/abs/math/0309136v1" rel="alternate" type="text/html"/>
    <link title="pdf" href="` + host + `/pdf/math/0309136v1" rel="related" type="application/pdf"/>
    <arxiv:primary_category xmlns:arxiv="http://arxiv.org/schemas/atom" term="math.GT" scheme="http://arxiv.org/schemas/atom"/>
    <category term="math.GT" scheme="http://arxiv.org/schemas/atom"/>
  </entry>
  <entry>
    <id>http://arxiv.org/abs/2301.12345v2</id>
    <updated>2023-02-01T10:00:00Z</updated>
    <published>2023-01-28T18:00:00Z</published>
    <title>Sparse Attention Models</title>
    <summary>We study sparse attention.</summary>
    <author><name>Jane Doe</name></author>
    <link href="http://arxiv.org/abs/2301.12345v2" rel="alternate" type="text/html"/>
    <link title="pdf" href="` + host + `/pdf/2301.12345v2" rel="related" type="application/pdf"/>
    <arxiv:primary_category xmlns:arxiv="http://arxiv.org/schemas/atom" term="cs.LG" scheme="http://arxiv.org/schemas/atom"/>
    <category term="cs.LG" scheme="http://arxiv.org/schemas/atom"/>
  </entry>
</feed>`
}

// newArxivSearchToolManager builds an enabled manager backed by a fake local
// arXiv upstream so tool tests stay fully offline.
func newArxivSearchToolManager(t *testing.T) *arxivapi.Manager {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/query", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, arxivSearchFeed(srv.URL))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	settings, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	s := settings.Get()
	enabled := true
	s.Arxiv = &config.ArxivSettings{Enabled: &enabled}
	if err := settings.Save(s); err != nil {
		t.Fatalf("Save settings: %v", err)
	}
	return arxivapi.NewManager(arxivapi.Options{
		CfgDir:   t.TempDir(),
		Settings: settings,
		BaseURL:  srv.URL,
		Limiter:  arxivapi.NewLimiter(0, nil, nil),
	})
}

func newArxivSearchToolManagerWithPDF(t *testing.T) *arxivapi.Manager {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/api/query", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, arxivSearchFeed(srv.URL))
	})
	mux.HandleFunc("/pdf/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<<>>\n%%EOF\n"))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	settings, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	s := settings.Get()
	enabled := true
	s.Arxiv = &config.ArxivSettings{Enabled: &enabled}
	if err := settings.Save(s); err != nil {
		t.Fatalf("Save settings: %v", err)
	}
	return arxivapi.NewManager(arxivapi.Options{
		CfgDir:   t.TempDir(),
		Settings: settings,
		BaseURL:  srv.URL,
		Limiter:  arxivapi.NewLimiter(0, nil, nil),
	})
}

func TestArxivSearchToolPaperKey(t *testing.T) {
	manager := newArxivSearchToolManager(t)
	// Store the old-style paper locally; its PDF link 404s, which still stores
	// a metadata_only record.
	if _, _, err := manager.DownloadPaper(context.Background(), "math/0309136"); err != nil {
		t.Fatalf("DownloadPaper: %v", err)
	}

	out, err := newArxivSearchTool(manager).Execute(context.Background(), ExecContext{}, json.RawMessage(`{"query":"attention"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("search returned error: %#v", out.Result)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want map", out.Result)
	}
	if note, _ := result["untrusted_note"].(string); note == "" {
		t.Fatalf("search result missing untrusted_note: %#v", result)
	}
	entries, ok := result["entries"].([]map[string]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("entries = %#v, want 2", result["entries"])
	}
	byID := map[string]map[string]any{}
	for _, entry := range entries {
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

func TestArxivDownloadPaperToolReturnsFirstClassPDFFileRef(t *testing.T) {
	manager := newArxivSearchToolManagerWithPDF(t)
	out, err := newArxivDownloadPaperTool(manager).Execute(context.Background(), ExecContext{}, json.RawMessage(`{"arxiv_id":"2301.12345"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("download returned error: %#v", out.Result)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want map", out.Result)
	}
	pdfFile, ok := result["pdf_file"].(map[string]any)
	if !ok {
		t.Fatalf("pdf_file = %#v, want map", result["pdf_file"])
	}
	if got := pdfFile["local_path"]; got != "arxiv/papers/2301.12345/paper.pdf" {
		t.Fatalf("pdf_file.local_path=%#v", got)
	}
	if got := pdfFile["mime_type"]; got != "application/pdf" {
		t.Fatalf("pdf_file.mime_type=%#v", got)
	}
	if got := pdfFile["model_file_input"]; got != true {
		t.Fatalf("pdf_file.model_file_input=%#v, want true", got)
	}
}

func TestArxivGetAndListToolsCarryUntrustedNote(t *testing.T) {
	manager := newArxivSearchToolManager(t)
	if _, _, err := manager.DownloadPaper(context.Background(), "math/0309136"); err != nil {
		t.Fatalf("DownloadPaper: %v", err)
	}

	checkNote := func(name string, tool Tool, args string) {
		t.Helper()
		out, err := tool.Execute(context.Background(), ExecContext{}, json.RawMessage(args))
		if err != nil {
			t.Fatalf("%s Execute: %v", name, err)
		}
		if !out.OK {
			t.Fatalf("%s returned error: %#v", name, out.Result)
		}
		result, ok := out.Result.(map[string]any)
		if !ok {
			t.Fatalf("%s result = %#v, want map", name, out.Result)
		}
		if note, _ := result["untrusted_note"].(string); note == "" {
			t.Fatalf("%s result missing untrusted_note: %#v", name, result)
		}
	}

	checkNote(ArxivToolGetPaper, newArxivGetPaperTool(manager), `{"paper":"math_0309136"}`)
	checkNote(ArxivToolListPapers, newArxivListPapersTool(manager), `{}`)
}

func TestArxivReadPaperToolReturnsPagesAndProgress(t *testing.T) {
	manager := newArxivSearchToolManager(t)
	if _, _, err := manager.DownloadPaper(context.Background(), "math/0309136"); err != nil {
		t.Fatalf("DownloadPaper: %v", err)
	}

	progressEvents := []map[string]any{}
	out, err := newArxivReadPaperTool(manager).Execute(context.Background(), ExecContext{
		OnProgress: func(progress map[string]any) {
			copied := map[string]any{}
			for k, v := range progress {
				copied[k] = v
			}
			progressEvents = append(progressEvents, copied)
		},
	}, json.RawMessage(`{"paper":"math_0309136","start_page":1,"max_pages":1}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("read paper returned error: %#v", out.Result)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want map", out.Result)
	}
	window, ok := result["paper_pages"].(arxivapi.PageWindow)
	if !ok {
		t.Fatalf("paper_pages = %#v, want arxiv.PageWindow", result["paper_pages"])
	}
	if window.PaperKey != "math_0309136" || len(window.Pages) != 1 {
		t.Fatalf("window = %#v, want one page for math_0309136", window)
	}
	if window.SourceKind != arxivapi.ExtractStateMetadataOnly {
		t.Fatalf("source_kind = %q, want metadata_only fallback", window.SourceKind)
	}
	if len(progressEvents) < 3 {
		t.Fatalf("progress events = %#v, want opening, page, and complete events", progressEvents)
	}
	opening := progressEvents[0]
	if opening["phase"] != "arxiv_text_window_reading" || opening["label"] != "Opening stored paper" || opening["percent"] != 5 {
		t.Fatalf("opening progress event = %#v", opening)
	}
	var sawPageProgress, sawComplete bool
	lastPercent := 0
	for _, event := range progressEvents {
		if percent, ok := event["percent"].(int); ok {
			if percent < lastPercent {
				t.Fatalf("progress percent went backwards: events=%#v", progressEvents)
			}
			lastPercent = percent
		}
		switch event["phase"] {
		case "arxiv_pdf_page_reading":
			sawPageProgress = true
			if event["provider"] != "arxiv" || event["paper_key"] != "math_0309136" {
				t.Fatalf("page progress event = %#v", event)
			}
			if event["label"] != "Reading paper" || event["message"] != "Reading page 1 of 1" {
				t.Fatalf("page progress label/message = %#v", event)
			}
			if event["page_current"] != 1 || event["page_count"] != 1 || event["pages_read"] != 1 {
				t.Fatalf("page progress page details = %#v", event)
			}
			if event["title"] != "An Old Style Paper" || event["source_kind"] != "metadata_only" {
				t.Fatalf("page progress paper details = %#v", event)
			}
		case "complete":
			sawComplete = true
			if event["label"] != "Paper pages read" || event["message"] != "Read pages 1-1" || event["percent"] != 100 {
				t.Fatalf("complete progress event = %#v", event)
			}
		}
	}
	if !sawPageProgress || !sawComplete {
		t.Fatalf("progress events = %#v, want page progress and complete", progressEvents)
	}
}

func TestArxivStatusToolDoesNotRequireNetwork(t *testing.T) {
	manager := arxivapi.NewManager(arxivapi.Options{CfgDir: t.TempDir()})
	out, err := newArxivStatusTool(manager).Execute(context.Background(), ExecContext{}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("status returned error: %#v", out.Result)
	}
}

func TestArxivScreeningReportValidatesCurrentRunAndEvidence(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, threadID, runID := seedArxivSearchRun(t, cfgDir, map[string]any{
		"entries": []map[string]any{
			{
				"arxiv_id":         "2301.12345",
				"version":          2,
				"title":            "Sparse Attention Models",
				"summary":          "We study sparse attention for efficient transformers.",
				"authors":          []any{"Jane Doe"},
				"categories":       []any{"cs.LG"},
				"primary_category": "cs.LG",
				"downloaded":       false,
			},
			{
				"arxiv_id":         "math/0309136",
				"version":          1,
				"title":            "An Old Style Paper",
				"summary":          "Classic geometry result.",
				"authors":          []any{"Grigori Author"},
				"categories":       []any{"math.GT"},
				"primary_category": "math.GT",
				"downloaded":       false,
			},
		},
		"total_results":  2,
		"start":          0,
		"items_per_page": 2,
	})

	args := json.RawMessage(`{
		"interpreted_request":"Find sparse attention papers.",
		"concepts":["sparse attention"],
		"query_rationales":[{"tool_call_id":"call-search-1","phase":"sweep","rationale":"Start broad."}],
		"verdicts":[
			{
				"arxiv_id":"https://arxiv.org/abs/2301.12345v2",
				"status":"kept",
				"reason":"Directly relevant.",
				"evidence":[{"source_field":"summary","quote":"sparse attention"}]
			}
		]
	}`)
	out, err := newArxivScreeningReportTool(nil).Execute(ctx, ExecContext{Store: st, ThreadID: threadID, RunID: runID, CfgDir: cfgDir}, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("report returned error: %#v", out.Result)
	}
	result := out.Result.(map[string]any)
	if result["schema"] != arxivAuditSchema {
		t.Fatalf("schema = %#v", result["schema"])
	}
	if _, nested := result["result_metadata"]; nested {
		t.Fatalf("screening report result contains nested result_metadata: %#v", result)
	}
	if out.ResultMetadata["kind"] != "screening_report" || out.ResultMetadata["schema"] != arxivAuditSchema {
		t.Fatalf("ResultMetadata=%#v, want screening report metadata", out.ResultMetadata)
	}
	funnel := result["funnel"].(map[string]any)
	if funnel["query_count"] != 1 || funnel["returned_count"] != 2 || funnel["unique_count"] != 2 || funnel["agent_kept_count"] != 1 || funnel["not_screened_count"] != 1 {
		t.Fatalf("unexpected funnel: %#v", funnel)
	}
	papers := result["papers"].([]map[string]any)
	kept := findArxivAuditPaper(t, papers, "2301.12345")
	evidence := kept["evidence"].([]map[string]any)
	if len(evidence) != 1 {
		t.Fatalf("evidence = %#v, want 1", evidence)
	}
	if evidence[0]["source_kind"] != "validated" || evidence[0]["source_field"] != "summary" {
		t.Fatalf("evidence not validated summary: %#v", evidence[0])
	}
	if evidence[0]["start_byte"] != 9 || evidence[0]["end_byte"] != 25 {
		t.Fatalf("evidence offsets = %#v..%#v, want 9..25", evidence[0]["start_byte"], evidence[0]["end_byte"])
	}
	unscreened := findArxivAuditPaper(t, papers, "math/0309136")
	if unscreened["status"] != "not_screened" {
		t.Fatalf("unscreened status = %#v", unscreened["status"])
	}
	if key, ok := unscreened["paper_key"]; ok {
		t.Fatalf("unscreened non-downloaded paper_key = %#v, want omitted", key)
	}
}

func TestArxivScreeningReportRejectsEmptyVerdicts(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, threadID, runID := seedArxivSearchRun(t, cfgDir, map[string]any{
		"entries": []map[string]any{{
			"arxiv_id":   "2301.12345",
			"version":    2,
			"title":      "Sparse Attention Models",
			"summary":    "We study sparse attention.",
			"downloaded": false,
		}},
		"total_results":  1,
		"start":          0,
		"items_per_page": 1,
	})
	out, err := newArxivScreeningReportTool(nil).Execute(ctx, ExecContext{Store: st, ThreadID: threadID, RunID: runID, CfgDir: cfgDir}, json.RawMessage(`{"verdicts":[]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.OK {
		t.Fatalf("empty verdict report unexpectedly succeeded: %#v", out.Result)
	}
}

func TestArxivScreeningReportWarnsForInvalidClaims(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, threadID, runID := seedArxivSearchRun(t, cfgDir, map[string]any{
		"entries": []map[string]any{
			{
				"arxiv_id":   "2301.12345",
				"version":    2,
				"title":      "Sparse Attention Models",
				"summary":    "Repeated phrase. Repeated phrase.",
				"downloaded": false,
			},
		},
		"total_results":  1,
		"start":          0,
		"items_per_page": 1,
	})
	args := json.RawMessage(`{
		"query_rationales":[{"tool_call_id":"missing-call","rationale":"Bad call."}],
		"verdicts":[
			{"arxiv_id":"2301.12345v3","status":"kept","evidence":[{"source_field":"summary","quote":"Repeated phrase."}]},
			{"arxiv_id":"9999.99999","status":"kept"}
		]
	}`)
	out, err := newArxivScreeningReportTool(nil).Execute(ctx, ExecContext{Store: st, ThreadID: threadID, RunID: runID, CfgDir: cfgDir}, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("report returned error: %#v", out.Result)
	}
	result := out.Result.(map[string]any)
	warnings := result["validation_warnings"].([]map[string]any)
	for _, code := range []string{"version_mismatch", "unknown_verdict_id", "unknown_query_rationale"} {
		if !arxivWarningsContain(warnings, code) {
			t.Fatalf("warnings missing %s: %#v", code, warnings)
		}
	}
	funnel := result["funnel"].(map[string]any)
	if funnel["agent_kept_count"] != 0 || funnel["not_screened_count"] != 1 {
		t.Fatalf("invalid verdicts should not count: %#v", funnel)
	}
}

func TestArxivScreeningReportIgnoresOccurrenceIndexForUniqueEvidence(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, threadID, runID := seedArxivSearchRun(t, cfgDir, map[string]any{
		"entries": []map[string]any{
			{
				"arxiv_id":   "2301.12345",
				"version":    1,
				"title":      "Sparse Attention Models",
				"summary":    "This exact support sentence appears once.",
				"downloaded": false,
			},
		},
		"total_results":  1,
		"start":          0,
		"items_per_page": 1,
	})
	args := json.RawMessage(`{
		"verdicts":[
			{
				"arxiv_id":"2301.12345",
				"status":"kept",
				"evidence":[{"source_field":"summary","quote":"This exact support sentence appears once.","occurrence_index":1}]
			}
		]
	}`)
	out, err := newArxivScreeningReportTool(nil).Execute(ctx, ExecContext{Store: st, ThreadID: threadID, RunID: runID, CfgDir: cfgDir}, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("report returned error: %#v", out.Result)
	}
	result := out.Result.(map[string]any)
	warnings := result["validation_warnings"].([]map[string]any)
	if arxivWarningsContain(warnings, "evidence_occurrence_out_of_range") {
		t.Fatalf("unique exact quote should not warn for advisory occurrence_index: %#v", warnings)
	}
	papers := result["papers"].([]map[string]any)
	evidence := papers[0]["evidence"].([]map[string]any)
	if len(evidence) != 1 {
		t.Fatalf("evidence rows = %#v", evidence)
	}
	if evidence[0]["occurrence_index"] != 0 {
		t.Fatalf("occurrence_index should be normalized to the selected exact match: %#v", evidence[0])
	}
}

func TestArxivScreeningReportIncludesReadCoverage(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, threadID, runID := seedArxivSearchRun(t, cfgDir, map[string]any{
		"entries": []map[string]any{
			{
				"arxiv_id":   "2301.12345",
				"version":    1,
				"title":      "Sparse Attention Models",
				"summary":    "This exact support sentence appears once.",
				"downloaded": true,
				"paper_key":  "2301_12345",
			},
		},
		"total_results":  1,
		"start":          0,
		"items_per_page": 1,
	})
	appendArxivReadPaperResult(t, st, threadID, runID, "call-read-1", map[string]any{
		"ok":              true,
		"paper_key":       "2301_12345",
		"arxiv_id":        "2301.12345",
		"title":           "Sparse Attention Models",
		"page_count":      8,
		"start_page":      2,
		"end_page":        3,
		"pages":           []map[string]any{{"page_number": 2, "text": "page two"}, {"page_number": 3, "text": "page three"}},
		"extract_state":   "stored",
		"extract_quality": "text",
		"source_kind":     "pdf_extract",
	})
	args := json.RawMessage(`{
		"verdicts":[
			{
				"arxiv_id":"2301.12345",
				"status":"kept",
				"evidence":[{"source_field":"summary","quote":"This exact support sentence appears once."}]
			}
		]
	}`)
	out, err := newArxivScreeningReportTool(nil).Execute(ctx, ExecContext{Store: st, ThreadID: threadID, RunID: runID, CfgDir: cfgDir}, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("report returned error: %#v", out.Result)
	}
	result := out.Result.(map[string]any)
	warnings := result["validation_warnings"].([]map[string]any)
	if arxivWarningsContain(warnings, "positive_verdict_without_read_trace") {
		t.Fatalf("read paper trace should satisfy positive verdict read coverage: %#v", warnings)
	}
	funnel := result["funnel"].(map[string]any)
	if funnel["read_paper_count"] != 1 {
		t.Fatalf("read_paper_count = %#v, want 1", funnel["read_paper_count"])
	}
	papers := result["papers"].([]map[string]any)
	paper := findArxivAuditPaper(t, papers, "2301.12345")
	if paper["read_state"] != "read" {
		t.Fatalf("read_state = %#v, want read", paper["read_state"])
	}
	coverage := paper["read_coverage"].(map[string]any)
	if coverage["pages_read_count"] != 2 || coverage["page_count"] != 8 || coverage["content_source_kind"] != "pdf_extract" {
		t.Fatalf("read_coverage = %#v, want pages 2/8 from pdf_extract", coverage)
	}
}

func TestArxivScreeningReportWarnsPositiveVerdictWithoutReadTrace(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, threadID, runID := seedArxivSearchRun(t, cfgDir, map[string]any{
		"entries": []map[string]any{
			{
				"arxiv_id":   "2301.12345",
				"version":    1,
				"title":      "Sparse Attention Models",
				"summary":    "This exact support sentence appears once.",
				"downloaded": false,
			},
		},
		"total_results":  1,
		"start":          0,
		"items_per_page": 1,
	})
	args := json.RawMessage(`{
		"verdicts":[{"arxiv_id":"2301.12345","status":"possible"}]
	}`)
	out, err := newArxivScreeningReportTool(nil).Execute(ctx, ExecContext{Store: st, ThreadID: threadID, RunID: runID, CfgDir: cfgDir}, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("report returned error: %#v", out.Result)
	}
	result := out.Result.(map[string]any)
	warnings := result["validation_warnings"].([]map[string]any)
	if !arxivWarningsContain(warnings, "positive_verdict_without_read_trace") {
		t.Fatalf("warnings missing read trace warning: %#v", warnings)
	}
}

func TestArxivScreeningReportRecoversCompactedSearchArtifact(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "prompt", 12)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	searchResult := `{"entries":[{"arxiv_id":"2301.12345","version":2,"title":"Sparse Attention Models","summary":"We study sparse attention.","downloaded":false}],"total_results":1,"start":0,"items_per_page":1}`
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    thread.ID,
		Type:        "code",
		Title:       "arxiv_search result",
		Storage:     "mysql",
		ContentText: &searchResult,
		ByteSize:    int64(len(searchResult)),
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	_, err = st.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
		Role: chattypes.RoleAssistant,
		ToolCalls: []chattypes.ToolCall{{
			ID:   "call-search-1",
			Name: ArxivToolSearch,
			Args: map[string]any{"query": "sparse attention"},
		}},
	})
	if err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	env := map[string]any{
		"artifact_id":     artifact.ID,
		"tool_name":       ArxivToolSearch,
		"tool_call_id":    "call-search-1",
		"ok":              true,
		"result_format":   "json",
		"result":          map[string]any{"model_context_compacted": true},
		"result_metadata": map[string]any{"provider": "arxiv", "integration": "arxiv", "kind": "search_results"},
	}
	contentBytes, _ := json.Marshal(env)
	content := string(contentBytes)
	name := ArxivToolSearch
	callID := "call-search-1"
	_, err = st.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
		Role:       chattypes.RoleTool,
		Content:    &content,
		ToolCallID: &callID,
		Name:       &name,
	})
	if err != nil {
		t.Fatalf("Append tool: %v", err)
	}

	out, err := newArxivScreeningReportTool(nil).Execute(ctx, ExecContext{Store: st, ThreadID: thread.ID, RunID: run.ID, CfgDir: cfgDir}, json.RawMessage(`{"verdicts":[{"arxiv_id":"2301.12345","status":"kept"}]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("report returned error: %#v", out.Result)
	}
	funnel := out.Result.(map[string]any)["funnel"].(map[string]any)
	if funnel["unique_count"] != 1 || funnel["agent_kept_count"] != 1 {
		t.Fatalf("artifact recovery funnel = %#v", funnel)
	}
}

func TestArxivScreeningReportRequiresCurrentRunSearch(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "prompt", 12)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	out, err := newArxivScreeningReportTool(nil).Execute(ctx, ExecContext{Store: st, ThreadID: thread.ID, RunID: run.ID, CfgDir: cfgDir}, json.RawMessage(`{"verdicts":[]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.OK {
		t.Fatalf("report unexpectedly succeeded without arxiv_search: %#v", out.Result)
	}
}

func TestArxivRecommendationsLinkToScreeningReport(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, threadID, runID := seedArxivSearchRun(t, cfgDir, map[string]any{
		"entries": []map[string]any{
			{
				"arxiv_id":   "2301.12345",
				"version":    2,
				"title":      "Sparse Attention Models",
				"summary":    "We study sparse attention for efficient transformers.",
				"downloaded": true,
				"paper_key":  "2301_12345",
			},
		},
		"total_results":  1,
		"start":          0,
		"items_per_page": 1,
	})
	appendArxivReadPaperResult(t, st, threadID, runID, "call-read-1", map[string]any{
		"ok":              true,
		"paper_key":       "2301_12345",
		"arxiv_id":        "2301.12345",
		"title":           "Sparse Attention Models",
		"page_count":      4,
		"start_page":      1,
		"end_page":        2,
		"pages":           []map[string]any{{"page_number": 1, "text": "page one"}, {"page_number": 2, "text": "page two"}},
		"extract_state":   "stored",
		"extract_quality": "text",
		"source_kind":     "pdf_extract",
	})
	report, err := buildArxivScreeningReport(ctx, nil, ExecContext{Store: st, ThreadID: threadID, RunID: runID, CfgDir: cfgDir}, arxivScreeningReportArgs{
		Verdicts: []arxivVerdictInput{{
			ArxivID: "2301.12345v2",
			Status:  "kept",
			Reason:  "Directly relevant.",
			Evidence: []arxivEvidenceInput{{
				SourceField: "summary",
				Quote:       "sparse attention",
			}},
		}},
	})
	if err != nil {
		t.Fatalf("buildArxivScreeningReport: %v", err)
	}
	appendArxivScreeningReportResult(t, st, threadID, runID, "call-report-1", "artifact-report-1", report)

	args := json.RawMessage(`{
		"screening_report_tool_call_id":"call-report-1",
		"selection_basis":"Prioritize the strongest match.",
		"recommendations":[
			{
				"arxiv_id":"https://arxiv.org/abs/2301.12345v2",
				"rank":1,
				"tier":"primary",
				"reason":"Best direct fit.",
				"use_for":"Efficient transformer background.",
				"evidence_refs":[{"evidence_index":0,"source_field":"summary"}]
			}
		]
	}`)
	out, err := newArxivRecommendationsTool(nil).Execute(ctx, ExecContext{Store: st, ThreadID: threadID, RunID: runID, CfgDir: cfgDir}, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("recommendations returned error: %#v", out.Result)
	}
	result := out.Result.(map[string]any)
	if result["schema"] != arxivRecommendationsSchema {
		t.Fatalf("schema = %#v", result["schema"])
	}
	if _, nested := result["result_metadata"]; nested {
		t.Fatalf("recommendations result contains nested result_metadata: %#v", result)
	}
	meta := out.ResultMetadata
	if meta["kind"] != "recommendation_set" || meta["screening_report_tool_call_id"] != "call-report-1" {
		t.Fatalf("unexpected ResultMetadata: %#v", meta)
	}
	rows := result["recommendations"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("recommendations = %#v, want 1", rows)
	}
	row := rows[0]
	if row["arxiv_id"] != "2301.12345" || row["screening_status"] != "kept" || row["screening_report_artifact_id"] != "artifact-report-1" {
		t.Fatalf("recommendation row missing linked refs: %#v", row)
	}
	if row["read_state"] != "read" {
		t.Fatalf("recommendation row read_state = %#v, want read", row["read_state"])
	}
	coverage := row["read_coverage"].(map[string]any)
	if arxivAuditIntFromAny(coverage["pages_read_count"]) != 2 || arxivAuditIntFromAny(coverage["page_count"]) != 4 {
		t.Fatalf("recommendation read_coverage = %#v, want 2/4 pages", coverage)
	}
	if _, ok := row["title"]; ok {
		t.Fatalf("recommendation row duplicated title metadata: %#v", row)
	}
	refs := row["evidence_refs"].([]map[string]any)
	if len(refs) != 1 || refs[0]["evidence_index"] != 0 || refs[0]["source_field"] != "summary" {
		t.Fatalf("evidence refs = %#v", refs)
	}
	if out.Artifact == nil || out.Artifact.Title != "arXiv Recommendations" {
		t.Fatalf("artifact payload = %#v", out.Artifact)
	}
	artifactMeta := out.Artifact.Metadata["result_metadata"].(map[string]any)
	if artifactMeta["kind"] != "recommendation_set" || artifactMeta["screening_report_tool_call_id"] != "call-report-1" {
		t.Fatalf("artifact result_metadata = %#v, want recommendation metadata", artifactMeta)
	}
	toolArgs, ok := out.Artifact.Metadata["tool_args"].(map[string]any)
	if !ok {
		t.Fatalf("artifact tool_args = %T, want object: %#v", out.Artifact.Metadata["tool_args"], out.Artifact.Metadata)
	}
	if toolArgs["screening_report_tool_call_id"] != "call-report-1" || toolArgs["selection_basis"] != "Prioritize the strongest match." {
		t.Fatalf("artifact tool_args missing decoded args: %#v", toolArgs)
	}
	artifactPresentation, ok := out.Artifact.Metadata["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("artifact tool_presentation = %T, want object: %#v", out.Artifact.Metadata["tool_presentation"], out.Artifact.Metadata)
	}
	if artifactPresentation["family"] != "arxiv" || artifactPresentation["result_kind"] != "data" || artifactPresentation["renderer_key"] != "arxiv" {
		t.Fatalf("artifact tool_presentation = %#v, want arXiv data presentation", artifactPresentation)
	}
}

func TestArxivRecommendationsArtifactMetadataIncludesToolContext(t *testing.T) {
	result := map[string]any{
		"screening_report": map[string]any{
			"tool_call_id": "call-report-1",
			"artifact_id":  "artifact-report-1",
		},
	}
	args := arxivRecommendationsArgs{
		ScreeningReportToolCallID: "call-report-1",
		SelectionBasis:            "Rank kept papers first.",
		Recommendations: []arxivRecommendationInput{{
			ArxivID: "2301.12345",
			Rank:    1,
			Tier:    "primary",
			Reason:  "Best fit.",
			UseFor:  "Background.",
			Caveats: []string{"Needs follow-up"},
			EvidenceRefs: []arxivRecommendationEvidenceInput{{
				EvidenceIndex: 0,
				SourceField:   "summary",
			}},
		}},
	}
	resultMetadata := arxivRecommendationsResultMetadata(result)
	meta := arxivRecommendationsArtifactMetadata(result, args, resultMetadata)
	toolArgs, ok := meta["tool_args"].(map[string]any)
	if !ok {
		t.Fatalf("tool_args=%T, want object; metadata=%#v", meta["tool_args"], meta)
	}
	if toolArgs["screening_report_tool_call_id"] != "call-report-1" || toolArgs["selection_basis"] != "Rank kept papers first." {
		t.Fatalf("tool_args=%#v, want decoded recommendation args", toolArgs)
	}
	resultMeta, ok := meta["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata=%T, want object; metadata=%#v", meta["result_metadata"], meta)
	}
	if resultMeta["tool_name"] != ArxivToolRecommendations || resultMeta["kind"] != "recommendation_set" || resultMeta["integration"] != "arxiv" {
		t.Fatalf("result_metadata=%#v, want arXiv recommendation metadata", resultMeta)
	}
	presentation, ok := meta["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("tool_presentation=%T, want object; metadata=%#v", meta["tool_presentation"], meta)
	}
	if presentation["family"] != "arxiv" || presentation["result_kind"] != "data" || presentation["native_viewer"] != "connector_inspector" {
		t.Fatalf("tool_presentation=%#v, want arXiv data presentation", presentation)
	}
}

func TestArxivRecommendationsWarnAndSkipInvalidRefs(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, threadID, runID := seedArxivSearchRun(t, cfgDir, map[string]any{
		"entries": []map[string]any{
			{
				"arxiv_id":   "2301.12345",
				"version":    2,
				"title":      "Sparse Attention Models",
				"summary":    "We study sparse attention.",
				"downloaded": false,
			},
			{
				"arxiv_id":   "2302.00001",
				"version":    1,
				"title":      "Unrelated Paper",
				"summary":    "Unrelated material.",
				"downloaded": false,
			},
		},
		"total_results":  2,
		"start":          0,
		"items_per_page": 2,
	})
	report, err := buildArxivScreeningReport(ctx, nil, ExecContext{Store: st, ThreadID: threadID, RunID: runID, CfgDir: cfgDir}, arxivScreeningReportArgs{
		Verdicts: []arxivVerdictInput{
			{ArxivID: "2301.12345", Status: "kept"},
			{ArxivID: "2302.00001", Status: "screened_out"},
		},
	})
	if err != nil {
		t.Fatalf("buildArxivScreeningReport: %v", err)
	}
	appendArxivScreeningReportResult(t, st, threadID, runID, "call-report-1", "artifact-report-1", report)

	args := json.RawMessage(`{
		"recommendations":[
			{"arxiv_id":"2301.12345","tier":"primary"},
			{"arxiv_id":"2302.00001","tier":"primary"},
			{"arxiv_id":"9999.99999","tier":"primary"}
		]
	}`)
	out, err := newArxivRecommendationsTool(nil).Execute(ctx, ExecContext{Store: st, ThreadID: threadID, RunID: runID, CfgDir: cfgDir}, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("recommendations returned error: %#v", out.Result)
	}
	result := out.Result.(map[string]any)
	rows := result["recommendations"].([]map[string]any)
	if len(rows) != 1 || rows[0]["arxiv_id"] != "2301.12345" {
		t.Fatalf("recommendations rows = %#v", rows)
	}
	warnings := result["validation_warnings"].([]map[string]any)
	for _, code := range []string{"not_recommendable_status", "unknown_recommendation_id"} {
		if !arxivWarningsContain(warnings, code) {
			t.Fatalf("warnings missing %s: %#v", code, warnings)
		}
	}
}

func TestArxivRecommendationsRequireScreeningReport(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, threadID, runID := seedArxivSearchRun(t, cfgDir, map[string]any{
		"entries": []map[string]any{{
			"arxiv_id":   "2301.12345",
			"version":    2,
			"title":      "Sparse Attention Models",
			"summary":    "We study sparse attention.",
			"downloaded": false,
		}},
		"total_results":  1,
		"start":          0,
		"items_per_page": 1,
	})
	out, err := newArxivRecommendationsTool(nil).Execute(ctx, ExecContext{Store: st, ThreadID: threadID, RunID: runID, CfgDir: cfgDir}, json.RawMessage(`{"recommendations":[{"arxiv_id":"2301.12345"}]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.OK {
		t.Fatalf("recommendations unexpectedly succeeded without screening report: %#v", out.Result)
	}
}

func seedArxivSearchRun(t *testing.T, cfgDir string, result map[string]any) (store.Store, string, string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "prompt", 12)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	_, err = st.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
		Role: chattypes.RoleAssistant,
		ToolCalls: []chattypes.ToolCall{{
			ID:   "call-search-1",
			Name: ArxivToolSearch,
			Args: map[string]any{"query": "sparse attention"},
		}},
	})
	if err != nil {
		t.Fatalf("Append assistant: %v", err)
	}
	env := map[string]any{
		"artifact_id":     "artifact-search-1",
		"tool_name":       ArxivToolSearch,
		"tool_call_id":    "call-search-1",
		"ok":              true,
		"result_format":   "json",
		"result":          result,
		"result_metadata": map[string]any{"provider": "arxiv", "integration": "arxiv", "kind": "search_results"},
	}
	contentBytes, _ := json.Marshal(env)
	content := string(contentBytes)
	name := ArxivToolSearch
	callID := "call-search-1"
	_, err = st.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
		Role:       chattypes.RoleTool,
		Content:    &content,
		ToolCallID: &callID,
		Name:       &name,
	})
	if err != nil {
		t.Fatalf("Append tool: %v", err)
	}
	return st, thread.ID, run.ID
}

func appendArxivScreeningReportResult(t *testing.T, st store.Store, threadID string, runID string, toolCallID string, artifactID string, report map[string]any) {
	t.Helper()
	ctx := context.Background()
	env := map[string]any{
		"artifact_id":     artifactID,
		"tool_name":       ArxivToolScreeningReport,
		"tool_call_id":    toolCallID,
		"ok":              true,
		"result_format":   "json",
		"result":          report,
		"result_metadata": map[string]any{"provider": "arxiv", "integration": "arxiv", "kind": "screening_report", "schema": arxivAuditSchema},
	}
	contentBytes, _ := json.Marshal(env)
	content := string(contentBytes)
	name := ArxivToolScreeningReport
	callID := toolCallID
	_, err := st.AppendMessage(ctx, threadID, &runID, store.Message{
		Role:       chattypes.RoleTool,
		Content:    &content,
		ToolCallID: &callID,
		Name:       &name,
	})
	if err != nil {
		t.Fatalf("Append screening report tool: %v", err)
	}
}

func appendArxivReadPaperResult(t *testing.T, st store.Store, threadID string, runID string, toolCallID string, pageWindow map[string]any) {
	t.Helper()
	ctx := context.Background()
	env := map[string]any{
		"artifact_id":   "artifact-" + toolCallID,
		"tool_name":     ArxivToolReadPaper,
		"tool_call_id":  toolCallID,
		"ok":            true,
		"result_format": "json",
		"result": map[string]any{
			"paper_pages": pageWindow,
		},
		"result_metadata": map[string]any{"provider": "arxiv", "integration": "arxiv", "kind": "paper_pages"},
	}
	contentBytes, _ := json.Marshal(env)
	content := string(contentBytes)
	name := ArxivToolReadPaper
	callID := toolCallID
	_, err := st.AppendMessage(ctx, threadID, &runID, store.Message{
		Role:       chattypes.RoleTool,
		Content:    &content,
		ToolCallID: &callID,
		Name:       &name,
	})
	if err != nil {
		t.Fatalf("Append read paper tool: %v", err)
	}
}

func findArxivAuditPaper(t *testing.T, papers []map[string]any, id string) map[string]any {
	t.Helper()
	for _, paper := range papers {
		if paper["arxiv_id"] == id {
			return paper
		}
	}
	t.Fatalf("paper %s not found in %#v", id, papers)
	return nil
}

func arxivWarningsContain(warnings []map[string]any, code string) bool {
	for _, warning := range warnings {
		if warning["code"] == code {
			return true
		}
	}
	return false
}
