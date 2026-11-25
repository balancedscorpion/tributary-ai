package filepipeline

import (
	"testing"

	"ai-analyst-local/internal/chat/fileingest"
)

func TestExtractFileDispatchesPDF(t *testing.T) {
	t.Parallel()

	raw := []byte(`%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>
endobj
4 0 obj
<< /Length 44 >>
stream
BT /F1 12 Tf 72 720 Td (Hello PDF world) Tj ET
endstream
endobj
trailer << /Root 1 0 R /Title (Sample PDF) >>
%%EOF`)
	eval := fileingest.EvaluateBytes("sample.pdf", "application/pdf", raw)
	meta, cfgDir := extractTestFile(t, raw, "sample.pdf", eval)

	assertStringMeta(t, meta, "kind", "pdf")
	assertStringMeta(t, meta, "pipeline", "pdf")
	assertStringMeta(t, meta, "status", "complete")
	assertIntMeta(t, meta, "pageCount", 1)
	assertStringMeta(t, meta, "textExtractionQuality", "basic_literal_strings")
	if got, _ := meta["nativePdfEligible"].(bool); !got {
		t.Fatalf("nativePdfEligible=%v, want true; meta=%#v", meta["nativePdfEligible"], meta)
	}
	assertOutputExists(t, cfgDir, meta, "manifestPath")
	assertOutputExists(t, cfgDir, meta, "documentPath")
	assertOutputExists(t, cfgDir, meta, "markdownPath")
	assertOutputExists(t, cfgDir, meta, "textPath")
	assertOutputExists(t, cfgDir, meta, "pagesPath")
}
