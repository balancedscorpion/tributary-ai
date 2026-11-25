package fileingest

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluateBytesTextCSVAndJSON(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		fileName string
		body     string
		wantKind Kind
		wantMIME string
	}{
		{name: "text", fileName: "notes.txt", body: "hello\nworld\n", wantKind: KindText, wantMIME: "text/plain"},
		{name: "csv", fileName: "data.csv", body: "a,b\n1,2\n", wantKind: KindCSV, wantMIME: "text/csv"},
		{name: "tsv", fileName: "data.tsv", body: "a\tb\n1\t2\n", wantKind: KindCSV, wantMIME: "text/tab-separated-values"},
		{name: "json", fileName: "payload.json", body: `{"ok":true}`, wantKind: KindJSON, wantMIME: "application/json"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eval := EvaluateBytes(tc.fileName, "application/octet-stream", []byte(tc.body))
			if eval.Kind != tc.wantKind {
				t.Fatalf("Kind=%q, want %q", eval.Kind, tc.wantKind)
			}
			if eval.NormalizedMIME != tc.wantMIME {
				t.Fatalf("NormalizedMIME=%q, want %q", eval.NormalizedMIME, tc.wantMIME)
			}
			if !eval.IsText || !eval.PreviewAvailable {
				t.Fatalf("text preview flags = isText:%v preview:%v, want true/true", eval.IsText, eval.PreviewAvailable)
			}
			if !strings.Contains(eval.PreviewText, strings.Split(tc.body, "\n")[0]) {
				t.Fatalf("PreviewText=%q does not contain body", eval.PreviewText)
			}
		})
	}
}

func TestEvaluateBytesTextLikeUnknownCanUseNativeDocument(t *testing.T) {
	t.Parallel()

	eval := EvaluateBytes("settings.ini", "application/octet-stream", []byte("name=value\n"))
	if !eval.IsText {
		t.Fatalf("IsText=false, want true")
	}
	if !eval.Capabilities.CanUseNativeDocument {
		t.Fatalf("CanUseNativeDocument=false, want true for text-like files")
	}
}

func TestEvaluateBytesMagicBeatsMisleadingDeclaredMIME(t *testing.T) {
	t.Parallel()

	eval := EvaluateBytes("report.bin", "text/plain", []byte("%PDF-1.7\n%test\n"))
	if eval.Kind != KindPDF {
		t.Fatalf("Kind=%q, want %q", eval.Kind, KindPDF)
	}
	if eval.NormalizedMIME != "application/pdf" {
		t.Fatalf("NormalizedMIME=%q, want application/pdf", eval.NormalizedMIME)
	}
	if eval.DeclaredMIME != "text/plain" {
		t.Fatalf("DeclaredMIME=%q, want text/plain", eval.DeclaredMIME)
	}
	if eval.IsText || eval.PreviewAvailable || strings.TrimSpace(eval.PreviewText) != "" {
		t.Fatalf("PDF raw bytes treated as text preview: isText=%v previewAvailable=%v preview=%q", eval.IsText, eval.PreviewAvailable, eval.PreviewText)
	}
	if strings.TrimSpace(eval.ContentNote) == "" {
		t.Fatalf("ContentNote empty, want unsupported preview note")
	}
}

func TestEvaluateBytesRecognizedNonTextDoesNotUseASCIIHeaderPreview(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		fileName string
		body     []byte
		wantKind Kind
	}{
		{name: "pdf", fileName: "report.pdf", body: []byte("%PDF-1.7\n1 0 obj <<>> endobj\n"), wantKind: KindPDF},
		{name: "rtf", fileName: "notes.rtf", body: []byte("{\\rtf1\\ansi Raw RTF control text}"), wantKind: KindDocument},
		{name: "keynote", fileName: "deck.key", body: makeZip(t, map[string]string{"QuickLook/Preview.txt": "not rendered"}), wantKind: KindPresentation},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eval := EvaluateBytes(tc.fileName, "application/octet-stream", tc.body)
			if eval.Kind != tc.wantKind {
				t.Fatalf("Kind=%q, want %q", eval.Kind, tc.wantKind)
			}
			if eval.IsText || eval.PreviewAvailable || strings.TrimSpace(eval.PreviewText) != "" {
				t.Fatalf("raw bytes treated as preview: isText=%v previewAvailable=%v preview=%q", eval.IsText, eval.PreviewAvailable, eval.PreviewText)
			}
			if strings.TrimSpace(eval.ContentNote) == "" {
				t.Fatalf("ContentNote empty, want unsupported preview note")
			}
		})
	}
}

func TestEvaluateBytesKeynotePresentation(t *testing.T) {
	t.Parallel()

	raw := makeZip(t, map[string]string{
		"QuickLook/Preview.txt": "KEYNOTE_SENTINEL",
	})
	eval := EvaluateBytes("quarterly.key", "application/octet-stream", raw)
	if eval.Kind != KindPresentation {
		t.Fatalf("Kind=%q, want %q", eval.Kind, KindPresentation)
	}
	if eval.NormalizedMIME != "application/vnd.apple.keynote" {
		t.Fatalf("NormalizedMIME=%q, want application/vnd.apple.keynote", eval.NormalizedMIME)
	}
	if eval.IsText || eval.PreviewAvailable || strings.TrimSpace(eval.PreviewText) != "" {
		t.Fatalf("keynote bytes treated as text preview: isText=%v previewAvailable=%v preview=%q", eval.IsText, eval.PreviewAvailable, eval.PreviewText)
	}
	if !eval.Capabilities.CanUseNativeDocument {
		t.Fatalf("CanUseNativeDocument=false, want true")
	}
}

func TestEvaluateBytesImages(t *testing.T) {
	t.Parallel()

	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xdb, 0, 0x43, 0, 1, 2, 3, 4}

	for _, tc := range []struct {
		name string
		file string
		body []byte
		mime string
	}{
		{name: "png", file: "image.dat", body: png, mime: "image/png"},
		{name: "jpeg", file: "photo.dat", body: jpeg, mime: "image/jpeg"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eval := EvaluateBytes(tc.file, "application/octet-stream", tc.body)
			if eval.Kind != KindImage {
				t.Fatalf("Kind=%q, want image", eval.Kind)
			}
			if eval.NormalizedMIME != tc.mime {
				t.Fatalf("NormalizedMIME=%q, want %q", eval.NormalizedMIME, tc.mime)
			}
			if !eval.Capabilities.CanUseNativeImage {
				t.Fatalf("CanUseNativeImage=false, want true")
			}
		})
	}
}

func TestEvaluateBytesExtensionlessOOXML(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		entryName string
		wantKind  Kind
		wantMIME  string
		wantFmt   string
	}{
		{name: "docx", entryName: "word/document.xml", wantKind: KindDocument, wantMIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", wantFmt: "docx"},
		{name: "xlsx", entryName: "xl/workbook.xml", wantKind: KindSpreadsheet, wantMIME: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", wantFmt: "xlsx"},
		{name: "pptx", entryName: "ppt/presentation.xml", wantKind: KindPresentation, wantMIME: "application/vnd.openxmlformats-officedocument.presentationml.presentation", wantFmt: "pptx"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := makeZip(t, map[string]string{
				"[Content_Types].xml": "<Types></Types>",
				tc.entryName:          "<xml/>",
			})
			eval := EvaluateBytes("upload", "application/octet-stream", raw)
			if eval.Kind != tc.wantKind {
				t.Fatalf("Kind=%q, want %q", eval.Kind, tc.wantKind)
			}
			if eval.NormalizedMIME != tc.wantMIME {
				t.Fatalf("NormalizedMIME=%q, want %q", eval.NormalizedMIME, tc.wantMIME)
			}
			if eval.ContainerFormat != tc.wantFmt || !eval.OOXMLContainer {
				t.Fatalf("container=%q ooxml=%v, want %q/true", eval.ContainerFormat, eval.OOXMLContainer, tc.wantFmt)
			}
		})
	}
}

func TestEvaluateBytesBinaryUnknown(t *testing.T) {
	t.Parallel()

	eval := EvaluateBytes("blob", "application/octet-stream", []byte{0, 1, 2, 3, 4, 5})
	if eval.Kind != KindBinary {
		t.Fatalf("Kind=%q, want binary", eval.Kind)
	}
	if eval.IsText || eval.PreviewAvailable {
		t.Fatalf("text flags = %v/%v, want false/false", eval.IsText, eval.PreviewAvailable)
	}
	if eval.ContentNote == "" {
		t.Fatalf("ContentNote empty, want unsupported preview note")
	}
}

func TestEvaluatePath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fullPath := filepath.Join(dir, "slides")
	raw := makeZip(t, map[string]string{
		"[Content_Types].xml":  "<Types></Types>",
		"ppt/presentation.xml": "<presentation/>",
	})
	if err := os.WriteFile(fullPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	eval, err := EvaluatePath("slides", "application/octet-stream", fullPath, DefaultPreviewBytes, true)
	if err != nil {
		t.Fatalf("EvaluatePath: %v", err)
	}
	if eval.Kind != KindPresentation {
		t.Fatalf("Kind=%q, want presentation", eval.Kind)
	}
	if eval.ContainerFormat != "pptx" {
		t.Fatalf("ContainerFormat=%q, want pptx", eval.ContainerFormat)
	}
	if eval.SHA256Hex == "" {
		t.Fatalf("SHA256Hex missing")
	}
}

func makeZip(t *testing.T, entries map[string]string) []byte {
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
