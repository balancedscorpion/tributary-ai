package filepipeline

import (
	"archive/zip"
	"bytes"
	"testing"

	"ai-analyst-local/internal/chat/fileingest"
)

func TestExtractFileDispatchesZIPArchive(t *testing.T) {
	t.Parallel()

	raw := testZIP(t, map[string]string{
		"docs/readme.txt": "hello",
		"../escape.txt":   "bad",
	})
	eval := fileingest.EvaluateBytes("bundle.zip", "application/zip", raw)
	meta, cfgDir := extractTestFile(t, raw, "bundle.zip", eval)

	assertStringMeta(t, meta, "kind", "archive")
	assertStringMeta(t, meta, "pipeline", "zip")
	assertStringMeta(t, meta, "status", "partial")
	assertIntMeta(t, meta, "entryCount", 2)
	assertIntMeta(t, meta, "fileCount", 2)
	assertIntMeta(t, meta, "unsafeEntryCount", 1)
	assertOutputExists(t, cfgDir, meta, "manifestPath")
	assertOutputExists(t, cfgDir, meta, "entriesPath")
	assertOutputExists(t, cfgDir, meta, "markdownPath")
}

func testZIP(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("Write(%s): %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close zip: %v", err)
	}
	return buf.Bytes()
}
