package filepipeline

import (
	"encoding/base64"
	"testing"

	"ai-analyst-local/internal/chat/fileingest"
)

func TestExtractFileDispatchesPNGImage(t *testing.T) {
	t.Parallel()

	raw, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	eval := fileingest.EvaluateBytes("pixel.png", "image/png", raw)
	meta, cfgDir := extractTestFile(t, raw, "pixel.png", eval)

	assertStringMeta(t, meta, "kind", "image")
	assertStringMeta(t, meta, "pipeline", "image")
	assertStringMeta(t, meta, "status", "complete")
	assertIntMeta(t, meta, "width", 1)
	assertIntMeta(t, meta, "height", 1)
	if got, _ := meta["nativeVisionEligible"].(bool); !got {
		t.Fatalf("nativeVisionEligible=%v, want true; meta=%#v", meta["nativeVisionEligible"], meta)
	}
	assertStringMeta(t, meta, "ocrStatus", "not_run")
	assertOutputExists(t, cfgDir, meta, "manifestPath")
	assertOutputExists(t, cfgDir, meta, "imagePath")
	assertOutputExists(t, cfgDir, meta, "markdownPath")
}
