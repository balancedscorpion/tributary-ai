package keynoteextract

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractUsesEmbeddedQuickLookPreview(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "deck.key")
	if err := os.WriteFile(sourcePath, testKeynotePackage(t), 0o600); err != nil {
		t.Fatalf("WriteFile source: %v", err)
	}
	outputDir := filepath.Join(dir, "extract")

	result, err := Extract(context.Background(), Request{
		SourcePath:      sourcePath,
		FileName:        "deck.key",
		OutputDir:       outputDir,
		SourceSHA256:    "abc123",
		EnableRendering: false,
		EnableOCR:       false,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Manifest.Pipeline != "keynote" {
		t.Fatalf("pipeline=%q, want keynote", result.Manifest.Pipeline)
	}
	if result.Manifest.Status != StatusComplete {
		t.Fatalf("status=%q, want complete; manifest=%#v", result.Manifest.Status, result.Manifest)
	}
	if result.Manifest.SlideCount != 1 {
		t.Fatalf("slide count=%d, want 1", result.Manifest.SlideCount)
	}
	rendered, ok := result.Manifest.Outputs["renderedSlides"].([]string)
	if !ok || len(rendered) != 1 {
		t.Fatalf("renderedSlides=%#v, want one slide", result.Manifest.Outputs["renderedSlides"])
	}
	if _, err := os.Stat(filepath.Join(outputDir, filepath.FromSlash(rendered[0]))); err != nil {
		t.Fatalf("rendered slide output missing: %v", err)
	}
	meta := result.Metadata("thread_uploads/thread/artifact.extract")
	if got, _ := meta["previewImagePath"].(string); !strings.HasSuffix(got, "/rendered/slide-001.png") {
		t.Fatalf("previewImagePath=%q, want rendered slide", got)
	}
	if got, _ := meta["textPath"].(string); !strings.HasSuffix(got, "/deck.md") {
		t.Fatalf("textPath=%q, want deck.md", got)
	}
}

func testKeynotePackage(t *testing.T) []byte {
	t.Helper()

	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")
	if err != nil {
		t.Fatalf("DecodeString PNG: %v", err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("QuickLook/Preview.png")
	if err != nil {
		t.Fatalf("Create preview: %v", err)
	}
	if _, err := w.Write(png); err != nil {
		t.Fatalf("Write preview: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close zip: %v", err)
	}
	return buf.Bytes()
}
