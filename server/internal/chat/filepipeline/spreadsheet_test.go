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

const xlsxMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

func TestExtractFileDispatchesXLSXSpreadsheet(t *testing.T) {
	t.Parallel()

	raw := testXLSX(t)
	eval := fileingest.EvaluateBytes("revenue.xlsx", xlsxMIME, raw)
	meta, cfgDir := extractTestFile(t, raw, "revenue.xlsx", eval)

	assertStringMeta(t, meta, "pipeline", "xlsx")
	assertStringMeta(t, meta, "status", "complete")
	assertIntMeta(t, meta, "sheetCount", 1)
	assertIntMeta(t, meta, "rowCount", 3)
	assertIntMeta(t, meta, "columnCount", 3)
	assertOutputExists(t, cfgDir, meta, "manifestPath")
	assertOutputExists(t, cfgDir, meta, "dataPath")
	assertOutputExists(t, cfgDir, meta, "markdownPath")
}

func TestExtractFileDispatchesCSV(t *testing.T) {
	t.Parallel()

	raw := []byte("region,q1,q2\nNorth,10,12\nSouth,8,9\n")
	eval := fileingest.EvaluateBytes("revenue.csv", "text/csv", raw)
	meta, cfgDir := extractTestFile(t, raw, "revenue.csv", eval)

	assertStringMeta(t, meta, "pipeline", "csv")
	assertStringMeta(t, meta, "status", "complete")
	assertIntMeta(t, meta, "sheetCount", 1)
	assertIntMeta(t, meta, "rowCount", 3)
	assertIntMeta(t, meta, "dataRowCount", 2)
	assertIntMeta(t, meta, "columnCount", 3)
	assertOutputExists(t, cfgDir, meta, "manifestPath")
	assertOutputExists(t, cfgDir, meta, "dataPath")
	assertOutputExists(t, cfgDir, meta, "rowsPath")
	assertOutputExists(t, cfgDir, meta, "markdownPath")
}

func TestExtractFileSniffsDelimitedCSV(t *testing.T) {
	t.Parallel()

	raw := []byte("region;q1;q2\nNorth;10;12\nSouth;8;9\n")
	eval := fileingest.EvaluateBytes("revenue.csv", "text/csv", raw)
	meta, _ := extractTestFile(t, raw, "revenue.csv", eval)

	assertStringMeta(t, meta, "pipeline", "csv")
	assertStringMeta(t, meta, "status", "complete")
	assertStringMeta(t, meta, "delimiter", "semicolon")
	assertIntMeta(t, meta, "rowCount", 3)
	assertIntMeta(t, meta, "columnCount", 3)
}

func TestExtractFileDispatchesTSV(t *testing.T) {
	t.Parallel()

	raw := []byte("region\tq1\tq2\nNorth\t10\t12\n")
	eval := fileingest.EvaluateBytes("revenue.tsv", "text/tab-separated-values", raw)
	meta, cfgDir := extractTestFile(t, raw, "revenue.tsv", eval)

	assertStringMeta(t, meta, "pipeline", "tsv")
	assertStringMeta(t, meta, "status", "complete")
	assertStringMeta(t, meta, "delimiter", "tab")
	assertIntMeta(t, meta, "rowCount", 2)
	assertIntMeta(t, meta, "dataRowCount", 1)
	assertIntMeta(t, meta, "columnCount", 3)
	assertOutputExists(t, cfgDir, meta, "manifestPath")
	assertOutputExists(t, cfgDir, meta, "dataPath")
	assertOutputExists(t, cfgDir, meta, "rowsPath")
}

func TestExtractFileReportsLegacyXLSUnsupported(t *testing.T) {
	t.Parallel()

	raw := []byte("not a real BIFF stream")
	eval := fileingest.EvaluateBytes("legacy.xls", "application/vnd.ms-excel", raw)
	meta, _ := extractTestFile(t, raw, "legacy.xls", eval)

	assertStringMeta(t, meta, "pipeline", "xls")
	assertStringMeta(t, meta, "status", "failed")
	if got, _ := meta["error"].(string); !strings.Contains(got, "legacy .xls") {
		t.Fatalf("error=%q, want legacy .xls unsupported message", got)
	}
}

func extractTestFile(t *testing.T, raw []byte, fileName string, eval fileingest.Evaluation) (map[string]any, string) {
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

func assertStringMeta(t *testing.T, meta map[string]any, key string, want string) {
	t.Helper()
	got, _ := meta[key].(string)
	if got != want {
		t.Fatalf("%s=%q, want %q; meta=%#v", key, got, want, meta)
	}
}

func assertIntMeta(t *testing.T, meta map[string]any, key string, want int) {
	t.Helper()
	got, ok := meta[key].(int)
	if !ok || got != want {
		t.Fatalf("%s=%v, want %d; meta=%#v", key, meta[key], want, meta)
	}
}

func assertOutputExists(t *testing.T, cfgDir string, meta map[string]any, key string) {
	t.Helper()
	rel, _ := meta[key].(string)
	if rel == "" {
		t.Fatalf("%s missing from metadata: %#v", key, meta)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("%s output %s: %v", key, rel, err)
	}
}

func testXLSX(t *testing.T) []byte {
	t.Helper()

	entries := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
  <Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
  <Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>
  <Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`,
		"xl/workbook.xml": `<?xml version="1.0" encoding="UTF-8"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets>
    <sheet name="Revenue" sheetId="1" r:id="rId1"/>
  </sheets>
</workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <dimension ref="A1:C3"/>
  <sheetData>
    <row r="1">
      <c r="A1" t="s"><v>0</v></c>
      <c r="B1" t="s"><v>1</v></c>
      <c r="C1" t="s"><v>2</v></c>
    </row>
    <row r="2">
      <c r="A2" t="s"><v>3</v></c>
      <c r="B2"><v>10</v></c>
      <c r="C2"><v>12</v></c>
    </row>
    <row r="3">
      <c r="A3" t="s"><v>4</v></c>
      <c r="B3"><v>8</v></c>
      <c r="C3"><v>9</v></c>
    </row>
  </sheetData>
</worksheet>`,
		"xl/sharedStrings.xml": `<?xml version="1.0" encoding="UTF-8"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="5" uniqueCount="5">
  <si><t>region</t></si>
  <si><t>q1</t></si>
  <si><t>q2</t></si>
  <si><t>North</t></si>
  <si><t>South</t></si>
</sst>`,
		"xl/styles.xml": `<?xml version="1.0" encoding="UTF-8"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts>
  <fills count="1"><fill><patternFill patternType="none"/></fill></fills>
  <borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>
  <cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
  <cellXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/></cellXfs>
</styleSheet>`,
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
