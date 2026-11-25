package documentextract

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	Version                    = 1
	DefaultMaxEntries          = 4000
	DefaultMaxUncompressedSize = 512 << 20
)

type Status string

const (
	StatusComplete Status = "complete"
	StatusPartial  Status = "partial"
	StatusFailed   Status = "failed"
)

type Request struct {
	SourcePath          string
	FileName            string
	OutputDir           string
	SourceSHA256        string
	Pipeline            string
	IncludeRawXML       bool
	MaxEntries          int
	MaxUncompressedSize int64
}

type Result struct {
	Manifest  Manifest
	Document  Document
	OutputDir string
}

type Manifest struct {
	Version               int             `json:"version"`
	Pipeline              string          `json:"pipeline"`
	Status                Status          `json:"status"`
	FileName              string          `json:"fileName"`
	SourceSHA256          string          `json:"sourceSha256,omitempty"`
	CreatedAt             time.Time       `json:"createdAt"`
	ParagraphCount        int             `json:"paragraphCount"`
	TableCount            int             `json:"tableCount"`
	WordCount             int             `json:"wordCount"`
	CharacterCount        int             `json:"characterCount"`
	HeaderFooterCount     int             `json:"headerFooterCount"`
	CommentCount          int             `json:"commentCount"`
	RelationshipCount     int             `json:"relationshipCount"`
	MediaCount            int             `json:"mediaCount"`
	HasMacrosOrOle        bool            `json:"hasMacrosOrOle"`
	HasExternalTargets    bool            `json:"hasExternalTargets"`
	Outputs               map[string]any  `json:"outputs"`
	Capabilities          map[string]bool `json:"capabilities"`
	Warnings              []string        `json:"warnings,omitempty"`
	CachedValuePolicy     string          `json:"cachedValuePolicy"`
	RecommendedNextAction string          `json:"recommendedNextAction,omitempty"`
}

type Document struct {
	Version       int               `json:"version"`
	FileName      string            `json:"fileName"`
	Pipeline      string            `json:"pipeline"`
	Paragraphs    []Paragraph       `json:"paragraphs,omitempty"`
	Tables        []Table           `json:"tables,omitempty"`
	Relationships []RelationshipRef `json:"relationships,omitempty"`
	Media         []MediaSummary    `json:"media,omitempty"`
	Warnings      []string          `json:"warnings,omitempty"`
	Text          string            `json:"text,omitempty"`
}

type Paragraph struct {
	Index   int    `json:"index"`
	Part    string `json:"part"`
	Text    string `json:"text"`
	InTable bool   `json:"inTable,omitempty"`
}

type Table struct {
	Index int        `json:"index"`
	Part  string     `json:"part"`
	Rows  [][]string `json:"rows"`
}

type RelationshipRef struct {
	ID         string `json:"id"`
	Type       string `json:"type,omitempty"`
	Target     string `json:"target,omitempty"`
	TargetMode string `json:"targetMode,omitempty"`
	Part       string `json:"part,omitempty"`
}

type MediaSummary struct {
	Path        string `json:"path"`
	ContentType string `json:"contentType,omitempty"`
	ByteSize    int64  `json:"byteSize"`
	SHA256      string `json:"sha256,omitempty"`
}

type packageReader struct {
	reader       *zip.ReadCloser
	files        map[string]*zip.File
	contentTypes map[string]string
}

type relationshipXML struct {
	ID         string `xml:"Id,attr"`
	Type       string `xml:"Type,attr"`
	Target     string `xml:"Target,attr"`
	TargetMode string `xml:"TargetMode,attr"`
}

type relationshipFile struct {
	Relationships []relationshipXML `xml:"Relationship"`
}

func Extract(ctx context.Context, req Request) (Result, error) {
	pipeline := normalizePipeline(req.Pipeline, req.FileName)
	if pipeline == "docx" {
		return extractDOCX(ctx, req)
	}
	if pipeline == "rtf" {
		return extractRTF(req)
	}
	err := fmt.Errorf("%s document extraction is not supported by the local file pipeline", pipeline)
	manifest := failedManifest(req, pipeline, err)
	if strings.TrimSpace(req.OutputDir) != "" {
		_ = os.MkdirAll(req.OutputDir, 0o700)
		_ = writeJSON(filepath.Join(req.OutputDir, "manifest.json"), manifest)
	}
	return Result{Manifest: manifest, OutputDir: strings.TrimSpace(req.OutputDir)}, err
}

func extractDOCX(ctx context.Context, req Request) (Result, error) {
	sourcePath := strings.TrimSpace(req.SourcePath)
	if sourcePath == "" {
		return Result{}, errors.New("documentextract: source path is required")
	}
	outputDir := strings.TrimSpace(req.OutputDir)
	if outputDir == "" {
		return Result{}, errors.New("documentextract: output dir is required")
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = filepath.Base(sourcePath)
	}
	if err := os.RemoveAll(outputDir); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return Result{}, err
	}
	pkg, err := openPackage(sourcePath, req.MaxEntries, req.MaxUncompressedSize)
	if err != nil {
		manifest := failedManifest(req, "docx", err)
		_ = writeJSON(filepath.Join(outputDir, "manifest.json"), manifest)
		return Result{Manifest: manifest, OutputDir: outputDir}, err
	}
	defer pkg.close()
	if _, ok := pkg.files["word/document.xml"]; !ok {
		err := errors.New("docx missing word/document.xml")
		manifest := failedManifest(req, "docx", err)
		_ = writeJSON(filepath.Join(outputDir, "manifest.json"), manifest)
		return Result{Manifest: manifest, OutputDir: outputDir}, err
	}

	doc := Document{Version: Version, FileName: fileName, Pipeline: "docx"}
	parts := pkg.documentTextParts()
	for _, part := range parts {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			default:
			}
		}
		raw, err := pkg.read(part)
		if err != nil {
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("could not read %s: %v", part, err))
			continue
		}
		paragraphs, tables, err := parseWordprocessingPart(part, raw, len(doc.Paragraphs), len(doc.Tables))
		if err != nil {
			doc.Warnings = append(doc.Warnings, fmt.Sprintf("could not fully parse %s: %v", part, err))
			continue
		}
		doc.Paragraphs = append(doc.Paragraphs, paragraphs...)
		doc.Tables = append(doc.Tables, tables...)
	}
	doc.Relationships = pkg.allRelationships()
	doc.Media = pkg.media()
	doc.Text = documentMarkdown(doc)

	if err := writeJSON(filepath.Join(outputDir, "document.json"), doc); err != nil {
		return Result{}, err
	}
	if err := writeJSONLines(filepath.Join(outputDir, "paragraphs.jsonl"), paragraphsToAny(doc.Paragraphs)); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "tables.json"), doc.Tables); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "relationships.json"), doc.Relationships); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "media_manifest.json"), map[string]any{"media": doc.Media}); err != nil {
		return Result{}, err
	}
	if err := writeFile(filepath.Join(outputDir, "document.md"), []byte(doc.Text)); err != nil {
		return Result{}, err
	}
	if err := writeFile(filepath.Join(outputDir, "text.txt"), []byte(documentPlainText(doc))); err != nil {
		return Result{}, err
	}
	if req.IncludeRawXML {
		if _, err := pkg.writeRawXML(filepath.Join(outputDir, "raw", "ooxml")); err != nil {
			doc.Warnings = append(doc.Warnings, "raw OOXML copy failed: "+err.Error())
		}
	}

	manifest := Manifest{
		Version:            Version,
		Pipeline:           "docx",
		Status:             StatusComplete,
		FileName:           fileName,
		SourceSHA256:       strings.TrimSpace(req.SourceSHA256),
		CreatedAt:          time.Now().UTC(),
		ParagraphCount:     len(doc.Paragraphs),
		TableCount:         len(doc.Tables),
		WordCount:          wordCount(documentPlainText(doc)),
		CharacterCount:     len([]rune(documentPlainText(doc))),
		HeaderFooterCount:  countPartPrefix(parts, "word/header") + countPartPrefix(parts, "word/footer"),
		CommentCount:       countPartPrefix(parts, "word/comments"),
		RelationshipCount:  len(doc.Relationships),
		MediaCount:         len(doc.Media),
		HasMacrosOrOle:     pkg.hasMacrosOrOle(),
		HasExternalTargets: hasExternalRelationships(doc.Relationships),
		Outputs: map[string]any{
			"manifestPath":      "manifest.json",
			"documentPath":      "document.json",
			"markdownPath":      "document.md",
			"textPath":          "text.txt",
			"paragraphsPath":    "paragraphs.jsonl",
			"tablesPath":        "tables.json",
			"relationshipsPath": "relationships.json",
			"mediaPath":         "media_manifest.json",
		},
		Capabilities: map[string]bool{
			"canReadText":       true,
			"canSearchDocument": true,
			"canInspectTables":  len(doc.Tables) > 0,
			"canInspectMedia":   len(doc.Media) > 0,
		},
		Warnings:              doc.Warnings,
		CachedValuePolicy:     "not_applicable",
		RecommendedNextAction: "Use document.md/text.txt for prose inspection, paragraphs.jsonl for citation-style chunks, and tables.json for tables.",
	}
	if len(doc.Warnings) > 0 {
		manifest.Status = StatusPartial
	}
	if err := writeJSON(filepath.Join(outputDir, "manifest.json"), manifest); err != nil {
		return Result{}, err
	}
	return Result{Manifest: manifest, Document: doc, OutputDir: outputDir}, nil
}

func extractRTF(req Request) (Result, error) {
	sourcePath := strings.TrimSpace(req.SourcePath)
	if sourcePath == "" {
		return Result{}, errors.New("documentextract: source path is required")
	}
	outputDir := strings.TrimSpace(req.OutputDir)
	if outputDir == "" {
		return Result{}, errors.New("documentextract: output dir is required")
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = filepath.Base(sourcePath)
	}
	if err := os.RemoveAll(outputDir); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return Result{}, err
	}
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		manifest := failedManifest(req, "rtf", err)
		_ = writeJSON(filepath.Join(outputDir, "manifest.json"), manifest)
		return Result{Manifest: manifest, OutputDir: outputDir}, err
	}
	text := rtfToText(string(raw))
	doc := Document{
		Version:  Version,
		FileName: fileName,
		Pipeline: "rtf",
		Text:     text,
	}
	if strings.TrimSpace(text) != "" {
		doc.Paragraphs = []Paragraph{{Index: 1, Part: "document", Text: text}}
	}
	if err := writeJSON(filepath.Join(outputDir, "document.json"), doc); err != nil {
		return Result{}, err
	}
	if err := writeJSONLines(filepath.Join(outputDir, "paragraphs.jsonl"), paragraphsToAny(doc.Paragraphs)); err != nil {
		return Result{}, err
	}
	if err := writeFile(filepath.Join(outputDir, "document.md"), []byte("# "+fileName+"\n\n"+text+"\n")); err != nil {
		return Result{}, err
	}
	if err := writeFile(filepath.Join(outputDir, "text.txt"), []byte(text)); err != nil {
		return Result{}, err
	}
	manifest := Manifest{
		Version:        Version,
		Pipeline:       "rtf",
		Status:         StatusComplete,
		FileName:       fileName,
		SourceSHA256:   strings.TrimSpace(req.SourceSHA256),
		CreatedAt:      time.Now().UTC(),
		ParagraphCount: len(doc.Paragraphs),
		WordCount:      wordCount(text),
		CharacterCount: len([]rune(text)),
		Outputs: map[string]any{
			"manifestPath":   "manifest.json",
			"documentPath":   "document.json",
			"markdownPath":   "document.md",
			"textPath":       "text.txt",
			"paragraphsPath": "paragraphs.jsonl",
		},
		Capabilities:          map[string]bool{"canReadText": true, "canSearchDocument": true},
		CachedValuePolicy:     "not_applicable",
		RecommendedNextAction: "Use text.txt for searchable extracted text. RTF styling is intentionally discarded.",
	}
	if err := writeJSON(filepath.Join(outputDir, "manifest.json"), manifest); err != nil {
		return Result{}, err
	}
	return Result{Manifest: manifest, Document: doc, OutputDir: outputDir}, nil
}

func (r Result) Metadata(rootRelPath string) map[string]any {
	rootRelPath = strings.Trim(strings.TrimSpace(filepath.ToSlash(rootRelPath)), "/")
	rel := func(p string) string {
		p = strings.Trim(strings.TrimSpace(filepath.ToSlash(p)), "/")
		if rootRelPath == "" || p == "" {
			return p
		}
		return path.Join(rootRelPath, p)
	}
	meta := map[string]any{
		"kind":               "document",
		"pipeline":           r.Manifest.Pipeline,
		"version":            r.Manifest.Version,
		"status":             string(r.Manifest.Status),
		"rootPath":           rootRelPath,
		"manifestPath":       rel("manifest.json"),
		"documentPath":       rel("document.json"),
		"dataPath":           rel("document.json"),
		"markdownPath":       rel("document.md"),
		"textPath":           rel("text.txt"),
		"paragraphsPath":     rel("paragraphs.jsonl"),
		"tablesPath":         rel("tables.json"),
		"paragraphCount":     r.Manifest.ParagraphCount,
		"tableCount":         r.Manifest.TableCount,
		"wordCount":          r.Manifest.WordCount,
		"characterCount":     r.Manifest.CharacterCount,
		"headerFooterCount":  r.Manifest.HeaderFooterCount,
		"commentCount":       r.Manifest.CommentCount,
		"relationshipCount":  r.Manifest.RelationshipCount,
		"mediaCount":         r.Manifest.MediaCount,
		"hasMacrosOrOle":     r.Manifest.HasMacrosOrOle,
		"hasExternalTargets": r.Manifest.HasExternalTargets,
		"cachedValuePolicy":  r.Manifest.CachedValuePolicy,
		"capabilities":       r.Manifest.Capabilities,
		"outputs":            r.Manifest.Outputs,
		"warnings":           r.Manifest.Warnings,
	}
	if r.Manifest.SourceSHA256 != "" {
		meta["sourceSha256"] = r.Manifest.SourceSHA256
	}
	return meta
}

func FailedMetadata(pipeline string, err error) map[string]any {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return map[string]any{
		"kind":     "document",
		"pipeline": normalizePipeline(pipeline, ""),
		"version":  Version,
		"status":   string(StatusFailed),
		"error":    msg,
	}
}

func openPackage(sourcePath string, maxEntries int, maxUncompressedSize int64) (*packageReader, error) {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	if maxUncompressedSize <= 0 {
		maxUncompressedSize = DefaultMaxUncompressedSize
	}
	r, err := zip.OpenReader(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open docx zip: %w", err)
	}
	p := &packageReader{reader: r, files: map[string]*zip.File{}, contentTypes: map[string]string{}}
	var total uint64
	for _, f := range r.File {
		name := cleanZipName(f.Name)
		if name == "" {
			_ = r.Close()
			return nil, fmt.Errorf("unsafe docx entry path %q", f.Name)
		}
		total += f.UncompressedSize64
		if len(p.files)+1 > maxEntries {
			_ = r.Close()
			return nil, fmt.Errorf("docx has too many entries: %d > %d", len(p.files)+1, maxEntries)
		}
		if total > uint64(maxUncompressedSize) {
			_ = r.Close()
			return nil, fmt.Errorf("docx uncompressed size exceeds limit: %d > %d", total, maxUncompressedSize)
		}
		p.files[name] = f
	}
	if _, ok := p.files["[Content_Types].xml"]; !ok {
		_ = r.Close()
		return nil, errors.New("docx missing [Content_Types].xml")
	}
	p.contentTypes = p.parseContentTypes()
	return p, nil
}

func (p *packageReader) close() {
	if p != nil && p.reader != nil {
		_ = p.reader.Close()
	}
}

func cleanZipName(name string) string {
	name = strings.Trim(strings.ReplaceAll(name, "\\", "/"), "/")
	if name == "" || strings.HasPrefix(name, "../") || name == ".." || strings.Contains(name, "/../") || strings.ContainsRune(name, '\x00') {
		return ""
	}
	return name
}

func (p *packageReader) read(name string) ([]byte, error) {
	name = cleanZipName(name)
	if name == "" {
		return nil, errors.New("unsafe path")
	}
	f := p.files[name]
	if f == nil {
		return nil, fmt.Errorf("docx entry %s not found", name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func (p *packageReader) parseContentTypes() map[string]string {
	raw, err := p.read("[Content_Types].xml")
	if err != nil {
		return map[string]string{}
	}
	type override struct {
		PartName    string `xml:"PartName,attr"`
		ContentType string `xml:"ContentType,attr"`
	}
	type typesXML struct {
		Overrides []override `xml:"Override"`
	}
	var doc typesXML
	_ = xml.Unmarshal(raw, &doc)
	out := map[string]string{}
	for _, item := range doc.Overrides {
		key := strings.Trim(strings.TrimSpace(item.PartName), "/")
		if key != "" {
			out[key] = strings.TrimSpace(item.ContentType)
		}
	}
	return out
}

func (p *packageReader) documentTextParts() []string {
	var parts []string
	preferred := []string{"word/document.xml"}
	for _, name := range preferred {
		if p.files[name] != nil {
			parts = append(parts, name)
		}
	}
	var extra []string
	for name := range p.files {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "word/header") ||
			strings.HasPrefix(lower, "word/footer") ||
			strings.HasPrefix(lower, "word/footnotes") ||
			strings.HasPrefix(lower, "word/endnotes") ||
			strings.HasPrefix(lower, "word/comments") {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	return append(parts, extra...)
}

func parseWordprocessingPart(part string, raw []byte, paragraphOffset int, tableOffset int) ([]Paragraph, []Table, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var paragraphs []Paragraph
	var tables []Table
	var currentParagraph strings.Builder
	var currentCell strings.Builder
	var currentRow []string
	var currentTable *Table
	inParagraph := false
	inTable := false
	inCell := false
	captureText := false
	paragraphInTable := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return paragraphs, tables, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tbl":
				inTable = true
				currentTable = &Table{Index: tableOffset + len(tables) + 1, Part: part}
			case "tr":
				if inTable {
					currentRow = nil
				}
			case "tc":
				if inTable {
					inCell = true
					currentCell.Reset()
				}
			case "p":
				inParagraph = true
				paragraphInTable = inTable
				currentParagraph.Reset()
			case "t":
				captureText = true
			case "tab":
				appendText(&currentParagraph, "\t")
				if inCell {
					appendText(&currentCell, "\t")
				}
			case "br", "cr":
				appendText(&currentParagraph, "\n")
				if inCell {
					appendText(&currentCell, "\n")
				}
			}
		case xml.CharData:
			if captureText {
				text := string(t)
				appendText(&currentParagraph, text)
				if inCell {
					appendText(&currentCell, text)
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				captureText = false
			case "p":
				text := normalizeParagraph(currentParagraph.String())
				if text != "" {
					paragraphs = append(paragraphs, Paragraph{Index: paragraphOffset + len(paragraphs) + 1, Part: part, Text: text, InTable: paragraphInTable})
				}
				if inCell {
					appendText(&currentCell, "\n")
				}
				inParagraph = false
			case "tc":
				if inTable {
					currentRow = append(currentRow, normalizeParagraph(currentCell.String()))
				}
				inCell = false
			case "tr":
				if currentTable != nil && len(currentRow) > 0 {
					currentTable.Rows = append(currentTable.Rows, append([]string(nil), currentRow...))
				}
			case "tbl":
				if currentTable != nil {
					tables = append(tables, *currentTable)
				}
				currentTable = nil
				inTable = false
			}
			if !inParagraph {
				currentParagraph.Reset()
			}
		}
	}
	return paragraphs, tables, nil
}

func appendText(b *strings.Builder, text string) {
	if text == "" {
		return
	}
	b.WriteString(text)
}

func normalizeParagraph(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func documentMarkdown(doc Document) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(firstNonEmpty(doc.FileName, "Document"))
	b.WriteString("\n\n")
	if len(doc.Paragraphs) > 0 {
		b.WriteString("## Text\n\n")
		for _, paragraph := range doc.Paragraphs {
			if paragraph.InTable {
				continue
			}
			b.WriteString(paragraph.Text)
			b.WriteString("\n\n")
		}
	}
	if len(doc.Tables) > 0 {
		b.WriteString("## Tables\n\n")
		for _, table := range doc.Tables {
			b.WriteString(fmt.Sprintf("### Table %d\n\n", table.Index))
			writeMarkdownTable(&b, table.Rows)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func writeMarkdownTable(b *strings.Builder, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	maxCols := 0
	for _, row := range rows {
		if len(row) > maxCols {
			maxCols = len(row)
		}
	}
	if maxCols == 0 {
		return
	}
	header := rows[0]
	b.WriteString("|")
	for c := 0; c < maxCols; c++ {
		value := ""
		if c < len(header) {
			value = header[c]
		}
		b.WriteString(" ")
		b.WriteString(escapeMarkdownCell(value))
		b.WriteString(" |")
	}
	b.WriteString("\n|")
	for c := 0; c < maxCols; c++ {
		b.WriteString(" --- |")
	}
	b.WriteString("\n")
	for _, row := range rows[1:] {
		b.WriteString("|")
		for c := 0; c < maxCols; c++ {
			value := ""
			if c < len(row) {
				value = row[c]
			}
			b.WriteString(" ")
			b.WriteString(escapeMarkdownCell(value))
			b.WriteString(" |")
		}
		b.WriteString("\n")
	}
}

func documentPlainText(doc Document) string {
	var lines []string
	for _, paragraph := range doc.Paragraphs {
		if strings.TrimSpace(paragraph.Text) != "" {
			lines = append(lines, paragraph.Text)
		}
	}
	for _, table := range doc.Tables {
		for _, row := range table.Rows {
			lines = append(lines, strings.Join(row, "\t"))
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func paragraphsToAny(paragraphs []Paragraph) []any {
	out := make([]any, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		out = append(out, paragraph)
	}
	return out
}

func (p *packageReader) relationshipsFor(part string) []relationshipXML {
	relsPath := relationshipPath(part)
	raw, err := p.read(relsPath)
	if err != nil {
		return nil
	}
	var rels relationshipFile
	if err := xml.Unmarshal(raw, &rels); err != nil {
		return nil
	}
	return rels.Relationships
}

func relationshipPath(part string) string {
	dir, base := path.Split(strings.Trim(part, "/"))
	return path.Join(dir, "_rels", base+".rels")
}

func (p *packageReader) allRelationships() []RelationshipRef {
	var out []RelationshipRef
	for name := range p.files {
		if !strings.HasSuffix(strings.ToLower(name), ".rels") {
			continue
		}
		raw, err := p.read(name)
		if err != nil {
			continue
		}
		var rels relationshipFile
		if err := xml.Unmarshal(raw, &rels); err != nil {
			continue
		}
		for _, rel := range rels.Relationships {
			out = append(out, RelationshipRef{ID: rel.ID, Type: rel.Type, Target: rel.Target, TargetMode: rel.TargetMode, Part: name})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Part == out[j].Part {
			return out[i].ID < out[j].ID
		}
		return out[i].Part < out[j].Part
	})
	return out
}

func (p *packageReader) media() []MediaSummary {
	var out []MediaSummary
	for name, f := range p.files {
		if !strings.HasPrefix(strings.ToLower(name), "word/media/") {
			continue
		}
		raw, err := p.read(name)
		sum := ""
		if err == nil {
			h := sha256.Sum256(raw)
			sum = hex.EncodeToString(h[:])
		}
		out = append(out, MediaSummary{Path: name, ContentType: p.contentTypes[name], ByteSize: int64(f.UncompressedSize64), SHA256: sum})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func (p *packageReader) hasMacrosOrOle() bool {
	for name, contentType := range p.contentTypes {
		lowerName := strings.ToLower(name)
		lowerType := strings.ToLower(contentType)
		if strings.Contains(lowerName, "vbaproject") || strings.Contains(lowerName, "embeddings/") ||
			strings.Contains(lowerType, "vba") || strings.Contains(lowerType, "oleobject") {
			return true
		}
	}
	return false
}

func hasExternalRelationships(rels []RelationshipRef) bool {
	for _, rel := range rels {
		if strings.EqualFold(rel.TargetMode, "External") {
			return true
		}
	}
	return false
}

func (p *packageReader) writeRawXML(root string) ([]string, error) {
	names := make([]string, 0, len(p.files))
	for name := range p.files {
		if strings.HasSuffix(strings.ToLower(name), ".xml") || strings.HasSuffix(strings.ToLower(name), ".rels") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		raw, err := p.read(name)
		if err != nil {
			return out, err
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := writeFile(target, raw); err != nil {
			return out, err
		}
		out = append(out, path.Join("raw", "ooxml", name))
	}
	return out, nil
}

func countPartPrefix(parts []string, prefix string) int {
	count := 0
	for _, part := range parts {
		if strings.HasPrefix(strings.ToLower(part), strings.ToLower(prefix)) {
			count++
		}
	}
	return count
}

func wordCount(s string) int {
	return len(strings.Fields(s))
}

var rtfControlWord = regexp.MustCompile(`\\[a-zA-Z]+\d* ?|\\'([0-9a-fA-F]{2})|[{}]`)

func rtfToText(raw string) string {
	raw = strings.ReplaceAll(raw, `\par`, "\n")
	raw = strings.ReplaceAll(raw, `\line`, "\n")
	raw = strings.ReplaceAll(raw, `\tab`, "\t")
	out := rtfControlWord.ReplaceAllStringFunc(raw, func(match string) string {
		if strings.HasPrefix(match, `\'`) && len(match) == 4 {
			return ""
		}
		return ""
	})
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func normalizePipeline(pipeline string, fileName string) string {
	p := strings.ToLower(strings.TrimSpace(pipeline))
	switch p {
	case "docx", "doc", "rtf":
		return p
	}
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".rtf":
		return "rtf"
	case ".doc":
		return "doc"
	default:
		return "docx"
	}
}

func failedManifest(req Request, pipeline string, err error) Manifest {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return Manifest{
		Version:      Version,
		Pipeline:     normalizePipeline(pipeline, req.FileName),
		Status:       StatusFailed,
		FileName:     firstNonEmpty(strings.TrimSpace(req.FileName), filepath.Base(strings.TrimSpace(req.SourcePath))),
		SourceSHA256: strings.TrimSpace(req.SourceSHA256),
		CreatedAt:    time.Now().UTC(),
		Outputs:      map[string]any{},
		Warnings:     []string{msg},
	}
}

func escapeMarkdownCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(raw, '\n'))
}

func writeJSONLines(path string, values []any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if _, err := w.Write(raw); err != nil {
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			return err
		}
	}
	return w.Flush()
}

func writeFile(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func ProjectExtractionKey(relPath string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(filepath.ToSlash(relPath))))
	return hex.EncodeToString(sum[:])[:24]
}
