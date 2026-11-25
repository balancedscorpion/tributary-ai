package archiveextract

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	Pipeline     string
}

type Result struct {
	Manifest  Manifest
	Entries   []Entry
	OutputDir string
}

type Manifest struct {
	Version                int             `json:"version"`
	Pipeline               string          `json:"pipeline"`
	Status                 Status          `json:"status"`
	FileName               string          `json:"fileName"`
	SourceSHA256           string          `json:"sourceSha256,omitempty"`
	CreatedAt              time.Time       `json:"createdAt"`
	EntryCount             int             `json:"entryCount"`
	FileCount              int             `json:"fileCount"`
	DirectoryCount         int             `json:"directoryCount"`
	SymlinkCount           int             `json:"symlinkCount"`
	UnsafeEntryCount       int             `json:"unsafeEntryCount"`
	TotalUncompressedBytes int64           `json:"totalUncompressedBytes"`
	TotalCompressedBytes   int64           `json:"totalCompressedBytes,omitempty"`
	MaxDepth               int             `json:"maxDepth"`
	Outputs                map[string]any  `json:"outputs"`
	Capabilities           map[string]bool `json:"capabilities"`
	Warnings               []string        `json:"warnings,omitempty"`
}

type Entry struct {
	Index            int     `json:"index"`
	Path             string  `json:"path"`
	Type             string  `json:"type"`
	Mode             string  `json:"mode,omitempty"`
	ByteSize         int64   `json:"byteSize"`
	CompressedSize   int64   `json:"compressedSize,omitempty"`
	CompressionRatio float64 `json:"compressionRatio,omitempty"`
	ModTime          string  `json:"modTime,omitempty"`
	Unsafe           bool    `json:"unsafe"`
	UnsafeReason     string  `json:"unsafeReason,omitempty"`
	LinkTarget       string  `json:"linkTarget,omitempty"`
}

func Extract(req Request) (Result, error) {
	sourcePath := strings.TrimSpace(req.SourcePath)
	if sourcePath == "" {
		return Result{}, errors.New("archiveextract: source path is required")
	}
	outputDir := strings.TrimSpace(req.OutputDir)
	if outputDir == "" {
		return Result{}, errors.New("archiveextract: output dir is required")
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = filepath.Base(sourcePath)
	}
	pipeline := normalizePipeline(req.Pipeline, fileName)
	if err := os.RemoveAll(outputDir); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return Result{}, err
	}
	var entries []Entry
	var err error
	switch pipeline {
	case "zip":
		entries, err = readZip(sourcePath)
	case "tar", "tgz":
		entries, err = readTar(sourcePath, pipeline == "tgz")
	case "gz":
		entries, err = readGzip(sourcePath)
	default:
		err = fmt.Errorf("%s archive extraction is not supported", pipeline)
	}
	if err != nil {
		manifest := failedManifest(req, pipeline, err)
		_ = writeJSON(filepath.Join(outputDir, "manifest.json"), manifest)
		return Result{Manifest: manifest, OutputDir: outputDir}, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	for i := range entries {
		entries[i].Index = i + 1
	}
	manifest := buildManifest(req, pipeline, fileName, entries)
	if err := writeJSONLines(filepath.Join(outputDir, "entries.jsonl"), entriesToAny(entries)); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "archive.json"), map[string]any{"entries": entries}); err != nil {
		return Result{}, err
	}
	if err := writeFile(filepath.Join(outputDir, "index.md"), []byte(archiveMarkdown(fileName, pipeline, manifest, entries))); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "manifest.json"), manifest); err != nil {
		return Result{}, err
	}
	return Result{Manifest: manifest, Entries: entries, OutputDir: outputDir}, nil
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
		"kind":                   "archive",
		"pipeline":               r.Manifest.Pipeline,
		"version":                r.Manifest.Version,
		"status":                 string(r.Manifest.Status),
		"rootPath":               rootRelPath,
		"manifestPath":           rel("manifest.json"),
		"dataPath":               rel("archive.json"),
		"entriesPath":            rel("entries.jsonl"),
		"markdownPath":           rel("index.md"),
		"entryCount":             r.Manifest.EntryCount,
		"fileCount":              r.Manifest.FileCount,
		"directoryCount":         r.Manifest.DirectoryCount,
		"symlinkCount":           r.Manifest.SymlinkCount,
		"unsafeEntryCount":       r.Manifest.UnsafeEntryCount,
		"totalUncompressedBytes": r.Manifest.TotalUncompressedBytes,
		"totalCompressedBytes":   r.Manifest.TotalCompressedBytes,
		"maxDepth":               r.Manifest.MaxDepth,
		"capabilities":           r.Manifest.Capabilities,
		"warnings":               r.Manifest.Warnings,
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
		"kind":     "archive",
		"pipeline": normalizePipeline(pipeline, ""),
		"version":  Version,
		"status":   string(StatusFailed),
		"error":    msg,
	}
}

func readZip(sourcePath string) ([]Entry, error) {
	zr, err := zip.OpenReader(sourcePath)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	entries := make([]Entry, 0, len(zr.File))
	for _, f := range zr.File {
		entry := Entry{Path: filepath.ToSlash(f.Name), ByteSize: int64(f.UncompressedSize64), CompressedSize: int64(f.CompressedSize64)}
		entry.Type = zipEntryType(f)
		entry.Mode = f.Mode().String()
		if !f.Modified.IsZero() {
			entry.ModTime = f.Modified.UTC().Format(time.RFC3339)
		}
		entry.Unsafe, entry.UnsafeReason = unsafeArchivePath(entry.Path, entry.Type, "")
		if entry.ByteSize > 0 && entry.CompressedSize > 0 {
			entry.CompressionRatio = float64(entry.ByteSize) / float64(entry.CompressedSize)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func zipEntryType(f *zip.File) string {
	mode := f.FileInfo().Mode()
	switch {
	case mode.IsDir():
		return "directory"
	case mode&os.ModeSymlink != 0:
		return "symlink"
	default:
		return "file"
	}
}

func readTar(sourcePath string, compressed bool) ([]Entry, error) {
	f, err := os.Open(sourcePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var reader io.Reader = f
	if compressed {
		gr, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer gr.Close()
		reader = gr
	}
	tr := tar.NewReader(reader)
	var entries []Entry
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return entries, err
		}
		entry := Entry{Path: filepath.ToSlash(hdr.Name), ByteSize: hdr.Size, Mode: os.FileMode(hdr.Mode).String()}
		if !hdr.ModTime.IsZero() {
			entry.ModTime = hdr.ModTime.UTC().Format(time.RFC3339)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			entry.Type = "directory"
		case tar.TypeSymlink, tar.TypeLink:
			entry.Type = "symlink"
			entry.LinkTarget = hdr.Linkname
		default:
			entry.Type = "file"
		}
		entry.Unsafe, entry.UnsafeReason = unsafeArchivePath(entry.Path, entry.Type, entry.LinkTarget)
		entries = append(entries, entry)
	}
	return entries, nil
}

func readGzip(sourcePath string) ([]Entry, error) {
	f, err := os.Open(sourcePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	name := strings.TrimSpace(gr.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(sourcePath), ".gz")
	}
	entry := Entry{Path: filepath.ToSlash(name), Type: "file"}
	entry.Unsafe, entry.UnsafeReason = unsafeArchivePath(entry.Path, entry.Type, "")
	return []Entry{entry}, nil
}

func unsafeArchivePath(entryPath string, entryType string, linkTarget string) (bool, string) {
	raw := strings.TrimSpace(strings.ReplaceAll(entryPath, "\\", "/"))
	if raw == "" || strings.ContainsRune(raw, '\x00') {
		return true, "empty or invalid path"
	}
	clean := path.Clean(strings.TrimPrefix(raw, "/"))
	if strings.HasPrefix(raw, "/") || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return true, "path traversal"
	}
	if entryType == "symlink" {
		target := strings.TrimSpace(strings.ReplaceAll(linkTarget, "\\", "/"))
		if target == "" || strings.HasPrefix(target, "/") || strings.HasPrefix(path.Clean(target), "../") {
			return true, "unsafe symlink target"
		}
	}
	return false, ""
}

func buildManifest(req Request, pipeline string, fileName string, entries []Entry) Manifest {
	manifest := Manifest{
		Version:      Version,
		Pipeline:     pipeline,
		Status:       StatusComplete,
		FileName:     fileName,
		SourceSHA256: strings.TrimSpace(req.SourceSHA256),
		CreatedAt:    time.Now().UTC(),
		Outputs: map[string]any{
			"manifestPath": "manifest.json",
			"archivePath":  "archive.json",
			"entriesPath":  "entries.jsonl",
			"markdownPath": "index.md",
		},
		Capabilities: map[string]bool{"canInspectEntries": true, "canSearchEntries": true},
	}
	for _, entry := range entries {
		manifest.EntryCount++
		if depth := archiveDepth(entry.Path); depth > manifest.MaxDepth {
			manifest.MaxDepth = depth
		}
		manifest.TotalUncompressedBytes += entry.ByteSize
		manifest.TotalCompressedBytes += entry.CompressedSize
		switch entry.Type {
		case "directory":
			manifest.DirectoryCount++
		case "symlink":
			manifest.SymlinkCount++
		default:
			manifest.FileCount++
		}
		if entry.Unsafe {
			manifest.UnsafeEntryCount++
			manifest.Warnings = append(manifest.Warnings, fmt.Sprintf("%s: %s", entry.Path, entry.UnsafeReason))
		}
	}
	if manifest.UnsafeEntryCount > 0 {
		manifest.Status = StatusPartial
	}
	return manifest
}

func archiveDepth(p string) int {
	p = strings.Trim(strings.TrimSpace(filepath.ToSlash(p)), "/")
	if p == "" {
		return 0
	}
	return strings.Count(p, "/") + 1
}

func archiveMarkdown(fileName string, pipeline string, manifest Manifest, entries []Entry) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(firstNonEmpty(fileName, "Archive"))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("- Pipeline: %s\n", pipeline))
	b.WriteString(fmt.Sprintf("- Entries: %d\n", manifest.EntryCount))
	b.WriteString(fmt.Sprintf("- Files: %d\n", manifest.FileCount))
	b.WriteString(fmt.Sprintf("- Directories: %d\n", manifest.DirectoryCount))
	b.WriteString(fmt.Sprintf("- Unsafe entries: %d\n\n", manifest.UnsafeEntryCount))
	b.WriteString("| Path | Type | Size | Unsafe |\n")
	b.WriteString("| --- | --- | ---: | --- |\n")
	limit := len(entries)
	if limit > 500 {
		limit = 500
	}
	for _, entry := range entries[:limit] {
		b.WriteString("| ")
		b.WriteString(escapeMarkdown(entry.Path))
		b.WriteString(" | ")
		b.WriteString(entry.Type)
		b.WriteString(" | ")
		b.WriteString(fmt.Sprintf("%d", entry.ByteSize))
		b.WriteString(" | ")
		if entry.Unsafe {
			b.WriteString(escapeMarkdown(entry.UnsafeReason))
		}
		b.WriteString(" |\n")
	}
	return b.String()
}

func entriesToAny(entries []Entry) []any {
	out := make([]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	return out
}

func normalizePipeline(pipeline, fileName string) string {
	p := strings.ToLower(strings.TrimSpace(pipeline))
	switch p {
	case "zip", "tar", "tgz", "gz":
		return p
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	switch ext {
	case ".zip":
		return "zip"
	case ".tar":
		return "tar"
	case ".tgz":
		return "tgz"
	case ".gz", ".gzip":
		return "gz"
	default:
		return "archive"
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

func escapeMarkdown(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
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
