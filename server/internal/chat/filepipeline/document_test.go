package filepipeline

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/fileingest"
)

const docxMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

func TestExtractFileDispatchesDOCXDocument(t *testing.T) {
	t.Parallel()

	raw := testDOCX(t)
	eval := fileingest.EvaluateBytes("brief.docx", docxMIME, raw)
	meta, cfgDir := extractDocumentTestFile(t, raw, "brief.docx", eval)

	assertStringMeta(t, meta, "kind", "document")
	assertStringMeta(t, meta, "pipeline", "docx")
	assertStringMeta(t, meta, "status", "complete")
	assertIntMeta(t, meta, "paragraphCount", 6)
	assertIntMeta(t, meta, "tableCount", 1)
	assertOutputExists(t, cfgDir, meta, "manifestPath")
	assertOutputExists(t, cfgDir, meta, "documentPath")
	assertOutputExists(t, cfgDir, meta, "markdownPath")
	assertOutputExists(t, cfgDir, meta, "textPath")
	assertOutputExists(t, cfgDir, meta, "paragraphsPath")
}

func TestExtractFileDispatchesRTFDocument(t *testing.T) {
	t.Parallel()

	raw := []byte(`{\rtf1\ansi This is {\b bold} text.\par Second line.}`)
	eval := fileingest.EvaluateBytes("brief.rtf", "application/rtf", raw)
	meta, cfgDir := extractDocumentTestFile(t, raw, "brief.rtf", eval)

	assertStringMeta(t, meta, "kind", "document")
	assertStringMeta(t, meta, "pipeline", "rtf")
	assertStringMeta(t, meta, "status", "complete")
	assertIntMeta(t, meta, "paragraphCount", 1)
	assertOutputExists(t, cfgDir, meta, "textPath")
}

func TestExtractFileReportsLegacyDOCUnsupported(t *testing.T) {
	t.Parallel()

	raw := []byte("not a real OLE document")
	eval := fileingest.EvaluateBytes("legacy.doc", "application/msword", raw)
	meta, _ := extractDocumentTestFile(t, raw, "legacy.doc", eval)

	assertStringMeta(t, meta, "kind", "document")
	assertStringMeta(t, meta, "pipeline", "doc")
	assertStringMeta(t, meta, "status", "failed")
	if got, _ := meta["error"].(string); !strings.Contains(got, "doc document extraction is not supported") {
		t.Fatalf("error=%q, want unsupported doc message", got)
	}
}

func extractDocumentTestFile(t *testing.T, raw []byte, fileName string, eval fileingest.Evaluation) (map[string]any, string) {
	t.Helper()

	cfgDir := t.TempDir()
	sourcePath := filepath.Join(cfgDir, fileName)
	if err := os.WriteFile(sourcePath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile source: %v", err)
	}
	meta := ExtractFile(context.Background(), ExtractionRequest{
		SourcePath:    sourcePath,
		OutputDir:     filepath.Join(cfgDir, "extract"),
		OutputRelPath: "extract",
		FileName:      fileName,
		Evaluation:    eval,
		IncludeRawXML: true,
	})
	if meta == nil {
		t.Fatalf("ExtractFile returned nil for %s, eval=%#v", fileName, eval)
	}
	return meta, cfgDir
}

func testDOCX(t *testing.T) []byte {
	t.Helper()

	entries := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`,
		"word/document.xml": `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Executive summary</w:t></w:r></w:p>
    <w:p><w:r><w:t>Revenue increased in Q2.</w:t></w:r></w:p>
    <w:tbl>
      <w:tr><w:tc><w:p><w:r><w:t>Region</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Revenue</w:t></w:r></w:p></w:tc></w:tr>
      <w:tr><w:tc><w:p><w:r><w:t>North</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>12</w:t></w:r></w:p></w:tc></w:tr>
    </w:tbl>
  </w:body>
</w:document>`,
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
		if _, err := w.Write([]byte(entries[name])); err != nil {
			t.Fatalf("Write(%s): %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close zip: %v", err)
	}
	return buf.Bytes()
}
