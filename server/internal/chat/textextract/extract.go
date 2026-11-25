package textextract

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
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Version          = 1
	DefaultChunkSize = 16 << 10
)

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
	Pipeline     string
	ChunkSize    int
}

type Result struct {
	Manifest  Manifest
	Content   Content
	OutputDir string
}

type Manifest struct {
	Version        int             `json:"version"`
	Pipeline       string          `json:"pipeline"`
	Status         Status          `json:"status"`
	FileName       string          `json:"fileName"`
	SourceSHA256   string          `json:"sourceSha256,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	LineCount      int             `json:"lineCount"`
	WordCount      int             `json:"wordCount"`
	CharacterCount int             `json:"characterCount"`
	ChunkCount     int             `json:"chunkCount"`
	ValidJSON      bool            `json:"validJson,omitempty"`
	JSONRootType   string          `json:"jsonRootType,omitempty"`
	HTMLTitle      string          `json:"htmlTitle,omitempty"`
	Encoding       string          `json:"encoding"`
	Outputs        map[string]any  `json:"outputs"`
	Capabilities   map[string]bool `json:"capabilities"`
	Warnings       []string        `json:"warnings,omitempty"`
}

type Content struct {
	Version  int            `json:"version"`
	FileName string         `json:"fileName"`
	Pipeline string         `json:"pipeline"`
	Text     string         `json:"text,omitempty"`
	Summary  map[string]any `json:"summary,omitempty"`
	Warnings []string       `json:"warnings,omitempty"`
}

type Chunk struct {
	Index     int    `json:"index"`
	StartByte int    `json:"startByte"`
	EndByte   int    `json:"endByte"`
	Text      string `json:"text"`
}

func Extract(ctx context.Context, req Request) (Result, error) {
	sourcePath := strings.TrimSpace(req.SourcePath)
	if sourcePath == "" {
		return Result{}, errors.New("textextract: source path is required")
	}
	outputDir := strings.TrimSpace(req.OutputDir)
	if outputDir == "" {
		return Result{}, errors.New("textextract: output dir is required")
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = filepath.Base(sourcePath)
	}
	pipeline := normalizePipeline(req.Pipeline, fileName)
	chunkSize := req.ChunkSize
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	if err := os.RemoveAll(outputDir); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return Result{}, err
	}
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		manifest := failedManifest(req, pipeline, err)
		_ = writeJSON(filepath.Join(outputDir, "manifest.json"), manifest)
		return Result{Manifest: manifest, OutputDir: outputDir}, err
	}
	if ctx != nil {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		default:
		}
	}
	text := string(bytes.ToValidUTF8(stripUTF8BOM(raw), []byte("?")))
	var warnings []string
	summary := map[string]any{}
	status := StatusComplete
	switch pipeline {
	case "json", "jsonl":
		jsonSummary, err := summarizeJSON(text, pipeline)
		if err != nil {
			status = StatusPartial
			warnings = append(warnings, err.Error())
		}
		for key, value := range jsonSummary {
			summary[key] = value
		}
	case "html":
		title, bodyText := htmlToText(text)
		text = bodyText
		if title != "" {
			summary["title"] = title
		}
	case "xml":
		summary["looksLikeXML"] = strings.Contains(text, "<") && strings.Contains(text, ">")
	}
	content := Content{Version: Version, FileName: fileName, Pipeline: pipeline, Text: text, Summary: summary, Warnings: warnings}
	chunks := chunkText(text, chunkSize)
	if err := writeJSON(filepath.Join(outputDir, "content.json"), content); err != nil {
		return Result{}, err
	}
	if err := writeFile(filepath.Join(outputDir, "text.txt"), []byte(text)); err != nil {
		return Result{}, err
	}
	if err := writeFile(filepath.Join(outputDir, "content.md"), []byte(markdownContent(fileName, pipeline, text, summary))); err != nil {
		return Result{}, err
	}
	if err := writeJSONLines(filepath.Join(outputDir, "chunks.jsonl"), chunksToAny(chunks)); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "structure.json"), summary); err != nil {
		return Result{}, err
	}
	manifest := Manifest{
		Version:        Version,
		Pipeline:       pipeline,
		Status:         status,
		FileName:       fileName,
		SourceSHA256:   strings.TrimSpace(req.SourceSHA256),
		CreatedAt:      time.Now().UTC(),
		LineCount:      lineCount(text),
		WordCount:      len(strings.Fields(text)),
		CharacterCount: len([]rune(text)),
		ChunkCount:     len(chunks),
		Encoding:       "utf-8_or_ascii_preserved",
		Outputs: map[string]any{
			"manifestPath":  "manifest.json",
			"contentPath":   "content.json",
			"textPath":      "text.txt",
			"markdownPath":  "content.md",
			"chunksPath":    "chunks.jsonl",
			"structurePath": "structure.json",
		},
		Capabilities: map[string]bool{
			"canReadText":   true,
			"canSearchText": true,
			"canPageChunks": len(chunks) > 0,
			"hasStructure":  len(summary) > 0,
		},
		Warnings: warnings,
	}
	if valid, _ := summary["validJson"].(bool); valid {
		manifest.ValidJSON = true
	}
	if rootType, _ := summary["rootType"].(string); rootType != "" {
		manifest.JSONRootType = rootType
	}
	if title, _ := summary["title"].(string); title != "" {
		manifest.HTMLTitle = title
	}
	if err := writeJSON(filepath.Join(outputDir, "manifest.json"), manifest); err != nil {
		return Result{}, err
	}
	return Result{Manifest: manifest, Content: content, OutputDir: outputDir}, nil
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
		"kind":           "text",
		"pipeline":       r.Manifest.Pipeline,
		"version":        r.Manifest.Version,
		"status":         string(r.Manifest.Status),
		"rootPath":       rootRelPath,
		"manifestPath":   rel("manifest.json"),
		"contentPath":    rel("content.json"),
		"dataPath":       rel("content.json"),
		"markdownPath":   rel("content.md"),
		"textPath":       rel("text.txt"),
		"chunksPath":     rel("chunks.jsonl"),
		"structurePath":  rel("structure.json"),
		"lineCount":      r.Manifest.LineCount,
		"wordCount":      r.Manifest.WordCount,
		"characterCount": r.Manifest.CharacterCount,
		"chunkCount":     r.Manifest.ChunkCount,
		"capabilities":   r.Manifest.Capabilities,
		"warnings":       r.Manifest.Warnings,
	}
	if r.Manifest.ValidJSON {
		meta["validJson"] = true
	}
	if r.Manifest.JSONRootType != "" {
		meta["jsonRootType"] = r.Manifest.JSONRootType
	}
	if r.Manifest.HTMLTitle != "" {
		meta["htmlTitle"] = r.Manifest.HTMLTitle
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
		"kind":     "text",
		"pipeline": normalizePipeline(pipeline, ""),
		"version":  Version,
		"status":   string(StatusFailed),
		"error":    msg,
	}
}

func normalizePipeline(pipeline, fileName string) string {
	p := strings.ToLower(strings.TrimSpace(pipeline))
	switch p {
	case "text", "markdown", "json", "jsonl", "html", "xml":
		return p
	}
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".md", ".markdown":
		return "markdown"
	case ".json":
		return "json"
	case ".jsonl":
		return "jsonl"
	case ".html", ".htm":
		return "html"
	case ".xml", ".svg":
		return "xml"
	default:
		return "text"
	}
}

func summarizeJSON(text string, pipeline string) (map[string]any, error) {
	if pipeline == "jsonl" {
		lines := strings.Split(text, "\n")
		count := 0
		for idx, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var v any
			if err := json.Unmarshal([]byte(line), &v); err != nil {
				return map[string]any{"validJson": false, "rootType": "jsonl", "recordCount": count}, fmt.Errorf("invalid JSONL at line %d: %w", idx+1, err)
			}
			count++
		}
		return map[string]any{"validJson": true, "rootType": "jsonl", "recordCount": count}, nil
	}
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return map[string]any{"validJson": false}, fmt.Errorf("invalid JSON: %w", err)
	}
	out := map[string]any{"validJson": true, "rootType": jsonType(v)}
	switch typed := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sortStrings(keys)
		out["topLevelKeyCount"] = len(keys)
		if len(keys) > 50 {
			keys = keys[:50]
		}
		out["topLevelKeys"] = keys
	case []any:
		out["arrayLength"] = len(typed)
	}
	return out, nil
}

func jsonType(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}

var (
	scriptRE = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	styleRE  = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	tagRE    = regexp.MustCompile(`(?is)<[^>]+>`)
	spaceRE  = regexp.MustCompile(`[ \t\r\f]+`)
	titleRE  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
)

func htmlToText(raw string) (string, string) {
	title := ""
	if m := titleRE.FindStringSubmatch(raw); len(m) > 1 {
		title = htmlEntityCleanup(m[1])
	}
	cleaned := scriptRE.ReplaceAllString(raw, " ")
	cleaned = styleRE.ReplaceAllString(cleaned, " ")
	cleaned = strings.ReplaceAll(cleaned, "<br>", "\n")
	cleaned = strings.ReplaceAll(cleaned, "<br/>", "\n")
	cleaned = strings.ReplaceAll(cleaned, "<br />", "\n")
	cleaned = tagRE.ReplaceAllString(cleaned, " ")
	cleaned = htmlEntityCleanup(cleaned)
	lines := strings.Split(cleaned, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(spaceRE.ReplaceAllString(line, " "))
	}
	return title, strings.TrimSpace(strings.Join(lines, "\n"))
}

func htmlEntityCleanup(s string) string {
	replacer := strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'")
	return strings.TrimSpace(replacer.Replace(s))
}

func chunkText(text string, chunkSize int) []Chunk {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	raw := []byte(text)
	var chunks []Chunk
	for start := 0; start < len(raw); {
		end := start + chunkSize
		if end > len(raw) {
			end = len(raw)
		}
		for end < len(raw) && !utf8.Valid(raw[start:end]) {
			end--
		}
		if end <= start {
			end = min(len(raw), start+chunkSize)
		}
		chunks = append(chunks, Chunk{Index: len(chunks) + 1, StartByte: start, EndByte: end, Text: string(raw[start:end])})
		start = end
	}
	if len(chunks) == 0 {
		return []Chunk{}
	}
	return chunks
}

func chunksToAny(chunks []Chunk) []any {
	out := make([]any, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, chunk)
	}
	return out
}

func markdownContent(fileName string, pipeline string, text string, summary map[string]any) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(firstNonEmpty(fileName, "Text File"))
	b.WriteString("\n\n")
	b.WriteString("- Pipeline: ")
	b.WriteString(pipeline)
	b.WriteString("\n")
	if len(summary) > 0 {
		if raw, err := json.MarshalIndent(summary, "", "  "); err == nil {
			b.WriteString("- Structure:\n\n```json\n")
			b.Write(raw)
			b.WriteString("\n```\n\n")
		}
	}
	b.WriteString("## Content\n\n")
	b.WriteString(text)
	if !strings.HasSuffix(text, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

func stripUTF8BOM(raw []byte) []byte {
	return bytes.TrimPrefix(raw, []byte{0xef, 0xbb, 0xbf})
}

func lineCount(text string) int {
	if text == "" {
		return 0
	}
	text = strings.TrimRight(text, "\r\n")
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
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

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
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
