package pptxextract

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestExtractBuildsCanonicalDeckBundle(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := filepath.Join(dir, "quarterly-review.pptx")
	if err := os.WriteFile(source, minimalPPTX(t), 0o600); err != nil {
		t.Fatalf("WriteFile source: %v", err)
	}
	outputDir := filepath.Join(dir, "extract")

	result, err := Extract(context.Background(), Request{
		SourcePath:       source,
		FileName:         "quarterly-review.pptx",
		OutputDir:        outputDir,
		SourceSHA256:     "abc123",
		IncludeRawXML:    true,
		EnableMarkItDown: false,
		EnableRendering:  false,
		EnableOCR:        false,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if result.Manifest.Status != StatusComplete {
		t.Fatalf("manifest status=%q, want complete; manifest=%#v", result.Manifest.Status, result.Manifest)
	}
	if result.Manifest.SlideCount != 1 {
		t.Fatalf("slide count=%d, want 1", result.Manifest.SlideCount)
	}
	if !result.Manifest.Capabilities["hasStructuredText"] || !result.Manifest.Capabilities["hasCharts"] || !result.Manifest.Capabilities["hasNotes"] {
		t.Fatalf("capabilities missing expected true values: %#v", result.Manifest.Capabilities)
	}
	if len(result.Deck.Slides) != 1 {
		t.Fatalf("deck slides=%d, want 1", len(result.Deck.Slides))
	}
	slide := result.Deck.Slides[0]
	if slide.Title != "Quarterly Review" {
		t.Fatalf("slide title=%q, want Quarterly Review", slide.Title)
	}
	if !strings.Contains(slide.RawText, "Revenue increased") {
		t.Fatalf("raw text=%q, want body text", slide.RawText)
	}
	if len(slide.Notes) != 1 || !strings.Contains(slide.Notes[0].Text, "Open with context") {
		t.Fatalf("notes=%#v, want speaker note", slide.Notes)
	}
	if len(slide.Charts) != 1 || slide.Charts[0].EmbeddedWorkbookPath != "ppt/embeddings/Microsoft_Excel_Worksheet1.xlsx" {
		t.Fatalf("charts=%#v, want chart with embedded workbook", slide.Charts)
	}
	if len(slide.Charts[0].Series) != 1 || slide.Charts[0].Series[0].Name != "Revenue" || slide.Charts[0].Series[0].Values[0] != "42" {
		t.Fatalf("chart series=%#v, want Revenue/Q1/42", slide.Charts[0].Series)
	}
	if len(slide.Media) != 1 || slide.Media[0].XMLPath != "ppt/media/image1.png" {
		t.Fatalf("media=%#v, want image relationship", slide.Media)
	}
	if len(slide.Hyperlinks) != 1 || slide.Hyperlinks[0].Target != "https://example.com" {
		t.Fatalf("hyperlinks=%#v, want external link", slide.Hyperlinks)
	}

	for _, rel := range []string{
		"manifest.json",
		"deck.json",
		"deck_graph.json",
		"deck.md",
		"slides/slide-001.json",
		"raw/ooxml/ppt/slides/slide1.xml",
	} {
		if _, err := os.Stat(filepath.Join(outputDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("expected output %s: %v", rel, err)
		}
	}

	rawManifest, err := os.ReadFile(filepath.Join(outputDir, "manifest.json"))
	if err != nil {
		t.Fatalf("ReadFile manifest: %v", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if manifest.Outputs["manifestPath"] != "manifest.json" {
		t.Fatalf("manifest outputs=%#v, want manifestPath", manifest.Outputs)
	}
}

func TestExtractRejectsUnsafeZipEntry(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	source := filepath.Join(dir, "bad.pptx")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("../evil.xml")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.Write([]byte("<evil/>")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := os.WriteFile(source, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile source: %v", err)
	}

	_, err = Extract(context.Background(), Request{
		SourcePath: source,
		FileName:   "bad.pptx",
		OutputDir:  filepath.Join(dir, "extract"),
	})
	if err == nil || !strings.Contains(err.Error(), "unsafe pptx entry") {
		t.Fatalf("Extract err=%v, want unsafe pptx entry", err)
	}
}

func minimalPPTX(t *testing.T) []byte {
	t.Helper()
	entries := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Default Extension="png" ContentType="image/png"/>
  <Override PartName="/ppt/presentation.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.presentation.main+xml"/>
  <Override PartName="/ppt/slides/slide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.slide+xml"/>
  <Override PartName="/ppt/notesSlides/notesSlide1.xml" ContentType="application/vnd.openxmlformats-officedocument.presentationml.notesSlide+xml"/>
  <Override PartName="/ppt/charts/chart1.xml" ContentType="application/vnd.openxmlformats-officedocument.drawingml.chart+xml"/>
</Types>`,
		"ppt/presentation.xml": `<?xml version="1.0" encoding="UTF-8"?>
<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:sldIdLst>
    <p:sldId id="256" r:id="rId1"/>
  </p:sldIdLst>
</p:presentation>`,
		"ppt/_rels/presentation.xml.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/slide" Target="slides/slide1.xml"/>
</Relationships>`,
		"ppt/slides/slide1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<p:sld xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <p:cSld>
    <p:spTree>
      <p:sp>
        <p:nvSpPr>
          <p:cNvPr id="2" name="Title 1"/>
          <p:cNvSpPr/>
          <p:nvPr><p:ph type="title"/></p:nvPr>
        </p:nvSpPr>
        <p:spPr><a:xfrm><a:off x="1000" y="2000"/><a:ext cx="3000" cy="4000"/></a:xfrm></p:spPr>
        <p:txBody><a:bodyPr/><a:p><a:r><a:t>Quarterly Review</a:t></a:r></a:p></p:txBody>
      </p:sp>
      <p:sp>
        <p:nvSpPr><p:cNvPr id="3" name="Body 1"/><p:cNvSpPr/><p:nvPr/></p:nvSpPr>
        <p:txBody>
          <a:bodyPr/>
          <a:p><a:r><a:t>Revenue increased 18%</a:t></a:r></a:p>
          <a:p><a:r><a:t>Margin expanded</a:t></a:r></a:p>
        </p:txBody>
      </p:sp>
      <p:pic>
        <p:nvPicPr><p:cNvPr id="4" name="Logo" descr="Company logo"/></p:nvPicPr>
      </p:pic>
    </p:spTree>
  </p:cSld>
</p:sld>`,
		"ppt/slides/_rels/slide1.xml.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/notesSlide" Target="../notesSlides/notesSlide1.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/chart" Target="../charts/chart1.xml"/>
  <Relationship Id="rId4" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="../media/image1.png"/>
  <Relationship Id="rId5" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/hyperlink" Target="https://example.com" TargetMode="External"/>
</Relationships>`,
		"ppt/notesSlides/notesSlide1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<p:notes xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>Open with context from April close.</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld>
</p:notes>`,
		"ppt/charts/chart1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<c:chartSpace xmlns:c="http://schemas.openxmlformats.org/drawingml/2006/chart" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <c:chart>
    <c:title><c:tx><c:rich><a:p><a:r><a:t>Revenue Chart</a:t></a:r></a:p></c:rich></c:tx></c:title>
    <c:plotArea><c:barChart><c:ser>
      <c:tx><c:v>Revenue</c:v></c:tx>
      <c:cat><c:strLit><c:pt idx="0"><c:v>Q1</c:v></c:pt></c:strLit></c:cat>
      <c:val><c:numLit><c:pt idx="0"><c:v>42</c:v></c:pt></c:numLit></c:val>
    </c:ser></c:barChart></c:plotArea>
  </c:chart>
</c:chartSpace>`,
		"ppt/charts/_rels/chart1.xml.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/package" Target="../embeddings/Microsoft_Excel_Worksheet1.xlsx"/>
</Relationships>`,
		"ppt/media/image1.png": "not really png",
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
