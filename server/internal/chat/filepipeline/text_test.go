package filepipeline

import (
	"testing"

	"ai-analyst-local/internal/chat/fileingest"
)

func TestExtractFileDispatchesJSONText(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"name":"north","values":[1,2,3]}`)
	eval := fileingest.EvaluateBytes("payload.json", "application/json", raw)
	meta, cfgDir := extractTestFile(t, raw, "payload.json", eval)

	assertStringMeta(t, meta, "kind", "text")
	assertStringMeta(t, meta, "pipeline", "json")
	assertStringMeta(t, meta, "status", "complete")
	if got, _ := meta["validJson"].(bool); !got {
		t.Fatalf("validJson=%v, want true; meta=%#v", meta["validJson"], meta)
	}
	assertStringMeta(t, meta, "jsonRootType", "object")
	assertOutputExists(t, cfgDir, meta, "manifestPath")
	assertOutputExists(t, cfgDir, meta, "textPath")
	assertOutputExists(t, cfgDir, meta, "chunksPath")
	assertOutputExists(t, cfgDir, meta, "structurePath")
}

func TestExtractFileDispatchesHTMLText(t *testing.T) {
	t.Parallel()

	raw := []byte(`<html><head><title>Quarterly Note</title><script>ignore()</script></head><body><h1>Revenue</h1><p>North grew.</p></body></html>`)
	eval := fileingest.EvaluateBytes("note.html", "text/html", raw)
	meta, cfgDir := extractTestFile(t, raw, "note.html", eval)

	assertStringMeta(t, meta, "pipeline", "html")
	assertStringMeta(t, meta, "status", "complete")
	assertStringMeta(t, meta, "htmlTitle", "Quarterly Note")
	assertOutputExists(t, cfgDir, meta, "markdownPath")
	assertOutputExists(t, cfgDir, meta, "textPath")
}

func TestExtractFileDispatchesMarkdownText(t *testing.T) {
	t.Parallel()

	raw := []byte("# Plan\n\n- Extract files\n- Verify sidecars\n")
	eval := fileingest.EvaluateBytes("plan.md", "text/markdown", raw)
	meta, cfgDir := extractTestFile(t, raw, "plan.md", eval)

	assertStringMeta(t, meta, "pipeline", "markdown")
	assertStringMeta(t, meta, "status", "complete")
	assertIntMeta(t, meta, "lineCount", 4)
	assertOutputExists(t, cfgDir, meta, "chunksPath")
}
