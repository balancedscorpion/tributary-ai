package pdfextract

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const Version = 1

type Status string

const (
	StatusComplete Status = "complete"
	StatusPartial  Status = "partial"
	StatusFailed   Status = "failed"
)

type Request struct {
	SourcePath   string
	FileName     string
	OutputDir    string
	SourceSHA256 string
}

type Result struct {
	Manifest  Manifest
	Document  PDFDocument
	OutputDir string
}

type Manifest struct {
	Version               int             `json:"version"`
	Pipeline              string          `json:"pipeline"`
	Status                Status          `json:"status"`
	FileName              string          `json:"fileName"`
	SourceSHA256          string          `json:"sourceSha256,omitempty"`
	CreatedAt             time.Time       `json:"createdAt"`
	PageCount             int             `json:"pageCount"`
	Encrypted             bool            `json:"encrypted"`
	Linearized            bool            `json:"linearized"`
	Title                 string          `json:"title,omitempty"`
	Author                string          `json:"author,omitempty"`
	WordCount             int             `json:"wordCount"`
	CharacterCount        int             `json:"characterCount"`
	TextExtractionQuality string          `json:"textExtractionQuality"`
	NativePDFEligible     bool            `json:"nativePdfEligible"`
	Outputs               map[string]any  `json:"outputs"`
	Capabilities          map[string]bool `json:"capabilities"`
	Warnings              []string        `json:"warnings,omitempty"`
}

type PDFDocument struct {
	Version               int      `json:"version"`
	FileName              string   `json:"fileName"`
	PageCount             int      `json:"pageCount"`
	Encrypted             bool     `json:"encrypted"`
	Linearized            bool     `json:"linearized"`
	Title                 string   `json:"title,omitempty"`
	Author                string   `json:"author,omitempty"`
	Text                  string   `json:"text,omitempty"`
	TextExtractionQuality string   `json:"textExtractionQuality"`
	Warnings              []string `json:"warnings,omitempty"`
}

type Page struct {
	PageNumber int    `json:"pageNumber"`
	Text       string `json:"text,omitempty"`
	Method     string `json:"method"`
}

type pythonPDFDocument struct {
	PageCount int      `json:"pageCount"`
	Encrypted bool     `json:"encrypted"`
	Title     string   `json:"title"`
	Author    string   `json:"author"`
	Text      string   `json:"text"`
	Pages     []Page   `json:"pages"`
	Warnings  []string `json:"warnings"`
}

type PageTextResult struct {
	PageCount int
	Encrypted bool
	Text      string
	Method    string
	Warnings  []string
}

func Extract(req Request) (Result, error) {
	sourcePath := strings.TrimSpace(req.SourcePath)
	if sourcePath == "" {
		return Result{}, errors.New("pdfextract: source path is required")
	}
	outputDir := strings.TrimSpace(req.OutputDir)
	if outputDir == "" {
		return Result{}, errors.New("pdfextract: output dir is required")
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
		manifest := failedManifest(req, err)
		_ = writeJSON(filepath.Join(outputDir, "manifest.json"), manifest)
		return Result{Manifest: manifest, OutputDir: outputDir}, err
	}
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("%PDF-")) {
		err := errors.New("file does not start with a PDF header")
		manifest := failedManifest(req, err)
		_ = writeJSON(filepath.Join(outputDir, "manifest.json"), manifest)
		return Result{Manifest: manifest, OutputDir: outputDir}, err
	}
	pageCount := countPDFPages(raw)
	encrypted := bytes.Contains(raw, []byte("/Encrypt"))
	linearized := bytes.Contains(raw[:min(len(raw), 2048)], []byte("/Linearized"))
	title := pdfInfoString(raw, "Title")
	author := pdfInfoString(raw, "Author")
	text := ""
	textExtractionQuality := "basic_literal_strings"
	pages := []any(nil)
	warnings := []string(nil)
	if pyDoc, err := extractWithPyPDF(sourcePath); err == nil && looksLikeUsefulExtractedText(pyDoc.Text) {
		if pyDoc.PageCount > 0 {
			pageCount = pyDoc.PageCount
		}
		encrypted = pyDoc.Encrypted
		title = firstNonEmpty(pyDoc.Title, title)
		author = firstNonEmpty(pyDoc.Author, author)
		text = strings.TrimSpace(pyDoc.Text)
		textExtractionQuality = "pypdf"
		warnings = append(warnings, pyDoc.Warnings...)
		for _, page := range pyDoc.Pages {
			pages = append(pages, page)
		}
	} else {
		text = bestEffortPDFText(raw)
		warnings = append(warnings, "PDF text extraction is best-effort and does not preserve layout, OCR scanned pages, or decode all compressed content streams.")
	}
	status := StatusComplete
	if text == "" {
		status = StatusPartial
		warnings = append(warnings, "no extractable literal text found; use native provider PDF support or a PDF rendering/OCR stage for visual/scanned content")
	}
	if encrypted {
		status = StatusPartial
		warnings = append(warnings, "PDF declares encryption; native provider and local extraction may fail without a password")
	}
	doc := PDFDocument{
		Version:               Version,
		FileName:              fileName,
		PageCount:             pageCount,
		Encrypted:             encrypted,
		Linearized:            linearized,
		Title:                 title,
		Author:                author,
		Text:                  text,
		TextExtractionQuality: textExtractionQuality,
		Warnings:              warnings,
	}
	if len(pages) == 0 {
		pages = make([]any, 0, max(1, pageCount))
		for i := 1; i <= max(1, pageCount); i++ {
			page := Page{PageNumber: i, Method: "metadata_only"}
			if i == 1 && text != "" {
				page.Text = text
				page.Method = "best_effort_literal_strings"
			}
			pages = append(pages, page)
		}
	}
	if err := writeJSON(filepath.Join(outputDir, "document.json"), doc); err != nil {
		return Result{}, err
	}
	if err := writeFile(filepath.Join(outputDir, "text.txt"), []byte(text)); err != nil {
		return Result{}, err
	}
	if err := writeFile(filepath.Join(outputDir, "document.md"), []byte(pdfMarkdown(doc))); err != nil {
		return Result{}, err
	}
	if err := writeJSONLines(filepath.Join(outputDir, "pages.jsonl"), pages); err != nil {
		return Result{}, err
	}
	manifest := Manifest{
		Version:               Version,
		Pipeline:              "pdf",
		Status:                status,
		FileName:              fileName,
		SourceSHA256:          strings.TrimSpace(req.SourceSHA256),
		CreatedAt:             time.Now().UTC(),
		PageCount:             pageCount,
		Encrypted:             encrypted,
		Linearized:            linearized,
		Title:                 title,
		Author:                author,
		WordCount:             len(strings.Fields(text)),
		CharacterCount:        len([]rune(text)),
		TextExtractionQuality: textExtractionQuality,
		NativePDFEligible:     !encrypted,
		Outputs: map[string]any{
			"manifestPath": "manifest.json",
			"documentPath": "document.json",
			"markdownPath": "document.md",
			"textPath":     "text.txt",
			"pagesPath":    "pages.jsonl",
		},
		Capabilities: map[string]bool{
			"canUseNativePDF":  !encrypted,
			"canReadText":      text != "",
			"canSearchText":    text != "",
			"needsPDFParser":   true,
			"needsOCRForScans": true,
		},
		Warnings: warnings,
	}
	if err := writeJSON(filepath.Join(outputDir, "manifest.json"), manifest); err != nil {
		return Result{}, err
	}
	return Result{Manifest: manifest, Document: doc, OutputDir: outputDir}, nil
}

func ExtractPageText(sourcePath string, pageNumber int) (PageTextResult, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return PageTextResult{}, errors.New("source path is required")
	}
	if pageNumber <= 0 {
		return PageTextResult{}, errors.New("page number must be positive")
	}
	doc, err := extractWithPyPDF(sourcePath)
	if err != nil {
		return PageTextResult{}, err
	}
	if doc.PageCount > 0 && pageNumber > doc.PageCount {
		return PageTextResult{}, fmt.Errorf("page_number %d is outside this PDF's %d pages", pageNumber, doc.PageCount)
	}
	for _, page := range doc.Pages {
		if page.PageNumber != pageNumber {
			continue
		}
		text := strings.TrimSpace(page.Text)
		if text == "" || (!looksLikeUsefulExtractedText(text) && !looksLikeHumanText(text)) {
			return PageTextResult{}, errors.New("page has no useful embedded text")
		}
		method := strings.TrimSpace(page.Method)
		if method == "" {
			method = "pypdf"
		}
		return PageTextResult{
			PageCount: doc.PageCount,
			Encrypted: doc.Encrypted,
			Text:      text,
			Method:    method,
			Warnings:  append([]string(nil), doc.Warnings...),
		}, nil
	}
	return PageTextResult{}, errors.New("page was not found in embedded PDF text extraction")
}

func extractWithPyPDF(sourcePath string) (pythonPDFDocument, error) {
	python, ok := findPythonWithModule("pypdf")
	if !ok {
		return pythonPDFDocument{}, errors.New("pypdf unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	script := `
import json
import sys
from pypdf import PdfReader

source = sys.argv[1]
reader = PdfReader(source)
warnings = []
pages = []
texts = []
metadata = reader.metadata
for index, page in enumerate(reader.pages, 1):
    try:
        text = page.extract_text() or ""
    except Exception as exc:
        text = ""
        warnings.append(f"pypdf failed to extract text for page {index}: {exc}")
    text = text.strip()
    if text:
        texts.append(text)
    pages.append({"pageNumber": index, "text": text, "method": "pypdf" if text else "pypdf_empty"})

def meta_text(name):
    value = getattr(metadata, name, "") if metadata else ""
    return str(value).strip() if value is not None else ""

payload = {
    "pageCount": len(reader.pages),
    "encrypted": bool(reader.is_encrypted),
    "title": meta_text("title"),
    "author": meta_text("author"),
    "text": "\n\n".join(texts).strip(),
    "pages": pages,
    "warnings": warnings,
}
print(json.dumps(payload, ensure_ascii=False))
`
	cmd := exec.CommandContext(ctx, python, "-c", script, sourcePath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return pythonPDFDocument{}, fmt.Errorf("pypdf extraction failed: %s", detail)
	}
	var doc pythonPDFDocument
	if err := json.Unmarshal(out, &doc); err != nil {
		return pythonPDFDocument{}, fmt.Errorf("pypdf extraction returned invalid JSON: %w", err)
	}
	return doc, nil
}

func findPythonWithModule(module string) (string, bool) {
	for _, python := range pythonCandidates() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cmd := exec.CommandContext(ctx, python, "-c", "import "+module)
		err := cmd.Run()
		cancel()
		if err == nil {
			return python, true
		}
	}
	return "", false
}

func pythonCandidates() []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		if _, ok := seen[candidate]; ok {
			return
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			out = append(out, candidate)
			seen[candidate] = struct{}{}
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		add(filepath.Join(home, ".cache", "codex-runtimes", "codex-primary-runtime", "dependencies", "python", "bin", "python3"))
	}
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			add(path)
		}
	}
	return out
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
		"kind":                  "pdf",
		"pipeline":              "pdf",
		"version":               r.Manifest.Version,
		"status":                string(r.Manifest.Status),
		"rootPath":              rootRelPath,
		"manifestPath":          rel("manifest.json"),
		"documentPath":          rel("document.json"),
		"dataPath":              rel("document.json"),
		"markdownPath":          rel("document.md"),
		"textPath":              rel("text.txt"),
		"pagesPath":             rel("pages.jsonl"),
		"pageCount":             r.Manifest.PageCount,
		"encrypted":             r.Manifest.Encrypted,
		"linearized":            r.Manifest.Linearized,
		"wordCount":             r.Manifest.WordCount,
		"characterCount":        r.Manifest.CharacterCount,
		"textExtractionQuality": r.Manifest.TextExtractionQuality,
		"nativePdfEligible":     r.Manifest.NativePDFEligible,
		"capabilities":          r.Manifest.Capabilities,
		"warnings":              r.Manifest.Warnings,
	}
	if r.Manifest.Title != "" {
		meta["title"] = r.Manifest.Title
	}
	if r.Manifest.Author != "" {
		meta["author"] = r.Manifest.Author
	}
	if r.Manifest.SourceSHA256 != "" {
		meta["sourceSha256"] = r.Manifest.SourceSHA256
	}
	return meta
}

func FailedMetadata(err error) map[string]any {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return map[string]any{
		"kind":     "pdf",
		"pipeline": "pdf",
		"version":  Version,
		"status":   string(StatusFailed),
		"error":    msg,
	}
}

var pageRE = regexp.MustCompile(`/Type\s*/Page\b`)

func countPDFPages(raw []byte) int {
	return len(pageRE.FindAll(raw, -1))
}

func pdfInfoString(raw []byte, key string) string {
	pattern := regexp.MustCompile(`/` + regexp.QuoteMeta(key) + `\s*\(([^)]{0,512})\)`)
	if match := pattern.FindSubmatch(raw); len(match) > 1 {
		return decodePDFLiteralString(string(match[1]))
	}
	return ""
}

var literalStringRE = regexp.MustCompile(`\(([^()]{2,1000})\)`)

func bestEffortPDFText(raw []byte) string {
	matches := literalStringRE.FindAllSubmatch(raw, 2000)
	var lines []string
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		text := decodePDFLiteralString(string(match[1]))
		if looksLikeHumanText(text) {
			lines = append(lines, text)
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func decodePDFLiteralString(s string) string {
	replacer := strings.NewReplacer(`\(`, "(", `\)`, ")", `\\`, `\`, `\n`, "\n", `\r`, "\n", `\t`, "\t")
	s = replacer.Replace(s)
	return strings.TrimSpace(string(bytes.ToValidUTF8([]byte(s), []byte("?"))))
}

func looksLikeHumanText(s string) bool {
	s = strings.TrimSpace(s)
	if len([]rune(s)) < 2 || !utf8.ValidString(s) {
		return false
	}
	letters := 0
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			letters++
		}
	}
	return letters >= 2
}

func looksLikeUsefulExtractedText(s string) bool {
	s = strings.TrimSpace(s)
	if len([]rune(s)) < 20 || !utf8.ValidString(s) {
		return false
	}
	letters := 0
	replacementOrControl := 0
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			letters++
		case r == utf8.RuneError || r == 0 || (r < 32 && r != '\n' && r != '\t' && r != '\r'):
			replacementOrControl++
		}
	}
	if letters < 10 {
		return false
	}
	return replacementOrControl*20 < len([]rune(s))
}

func pdfMarkdown(doc PDFDocument) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(firstNonEmpty(doc.Title, doc.FileName, "PDF"))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("- Pages: %d\n", doc.PageCount))
	b.WriteString(fmt.Sprintf("- Encrypted: %t\n", doc.Encrypted))
	b.WriteString(fmt.Sprintf("- Linearized: %t\n", doc.Linearized))
	b.WriteString(fmt.Sprintf("- Text extraction quality: %s\n", doc.TextExtractionQuality))
	if doc.Author != "" {
		b.WriteString(fmt.Sprintf("- Author: %s\n", doc.Author))
	}
	if len(doc.Warnings) > 0 {
		b.WriteString("\n## Warnings\n\n")
		for _, warning := range doc.Warnings {
			b.WriteString("- ")
			b.WriteString(warning)
			b.WriteString("\n")
		}
	}
	if strings.TrimSpace(doc.Text) != "" {
		b.WriteString("\n## Extracted Text\n\n")
		b.WriteString(doc.Text)
		if !strings.HasSuffix(doc.Text, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func failedManifest(req Request, err error) Manifest {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return Manifest{
		Version:      Version,
		Pipeline:     "pdf",
		Status:       StatusFailed,
		FileName:     firstNonEmpty(strings.TrimSpace(req.FileName), filepath.Base(strings.TrimSpace(req.SourcePath))),
		SourceSHA256: strings.TrimSpace(req.SourceSHA256),
		CreatedAt:    time.Now().UTC(),
		Outputs:      map[string]any{},
		Warnings:     []string{msg},
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
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
