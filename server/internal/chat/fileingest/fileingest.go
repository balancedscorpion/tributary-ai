package fileingest

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	DefaultPreviewBytes = 32 << 10
)

type Scope string

const (
	ScopeThread    Scope = "thread"
	ScopeProject   Scope = "project"
	ScopeConnector Scope = "connector"
	ScopeGenerated Scope = "generated"
	ScopeImport    Scope = "import"
)

type Source string

const (
	SourceUpload      Source = "upload"
	SourcePaste       Source = "paste"
	SourceDrop        Source = "drop"
	SourceExternalAPI Source = "external_api"
	SourceGit         Source = "git"
	SourceLocalDir    Source = "local_dir"
	SourceConnector   Source = "connector"
	SourceGenerated   Source = "generated"
)

type Kind string

const (
	KindImage        Kind = "image"
	KindPDF          Kind = "pdf"
	KindSpreadsheet  Kind = "spreadsheet"
	KindPresentation Kind = "presentation"
	KindDocument     Kind = "document"
	KindCSV          Kind = "csv"
	KindText         Kind = "text"
	KindJSON         Kind = "json"
	KindHTML         Kind = "html"
	KindArchive      Kind = "archive"
	KindBinary       Kind = "binary"
	KindUnknown      Kind = "unknown"
)

type Capabilities struct {
	CanPreviewText           bool `json:"canPreviewText"`
	CanSearchText            bool `json:"canSearchText"`
	CanExtractStructuredData bool `json:"canExtractStructuredData"`
	CanUseNativeImage        bool `json:"canUseNativeImage"`
	CanUseNativeDocument     bool `json:"canUseNativeDocument"`
	SafeToInline             bool `json:"safeToInline"`
}

type Evaluation struct {
	FileName           string       `json:"fileName"`
	Extension          string       `json:"extension,omitempty"`
	DeclaredMIME       string       `json:"declaredMimeType,omitempty"`
	ExtensionMIME      string       `json:"extensionMimeType,omitempty"`
	SniffedMIME        string       `json:"sniffedMimeType,omitempty"`
	NormalizedMIME     string       `json:"normalizedMimeType"`
	Kind               Kind         `json:"fileKind"`
	ContainerFormat    string       `json:"containerFormat,omitempty"`
	SizeBytes          int64        `json:"sizeBytes"`
	SHA256             []byte       `json:"-"`
	SHA256Hex          string       `json:"sha256"`
	IsText             bool         `json:"isText"`
	PreviewAvailable   bool         `json:"previewAvailable"`
	PreviewTruncated   bool         `json:"previewTruncated"`
	PreviewText        string       `json:"previewText,omitempty"`
	ContentNote        string       `json:"contentNote,omitempty"`
	Language           string       `json:"language,omitempty"`
	Capabilities       Capabilities `json:"capabilities"`
	OOXMLContainer     bool         `json:"ooxmlContainer,omitempty"`
	UnsupportedPreview bool         `json:"unsupportedPreview,omitempty"`
}

type IngestRequest struct {
	Scope        Scope
	Source       Source
	FileName     string
	DeclaredMIME string
	Reader       io.Reader
	TargetPath   string
	MaxBytes     int64
	PreviewBytes int
}

type Result struct {
	Evaluation Evaluation
	TargetPath string
	ByteSize   int64
	SHA256     []byte
	SHA256Hex  string
}

var ErrTooLarge = errors.New("fileingest: file too large")

type captureWriter struct {
	limit int
	buf   bytes.Buffer
}

func (w *captureWriter) Write(p []byte) (int, error) {
	if remaining := w.limit - w.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			_, _ = w.buf.Write(p[:remaining])
		} else {
			_, _ = w.buf.Write(p)
		}
	}
	return len(p), nil
}

func Ingest(req IngestRequest) (Result, error) {
	if req.Reader == nil {
		return Result{}, errors.New("fileingest: reader is required")
	}
	target := strings.TrimSpace(req.TargetPath)
	if target == "" {
		return Result{}, errors.New("fileingest: target path is required")
	}
	previewLimit := req.PreviewBytes
	if previewLimit <= 0 {
		previewLimit = DefaultPreviewBytes
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return Result{}, err
	}

	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".tmp-*")
	if err != nil {
		return Result{}, err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		_ = tmp.Close()
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return Result{}, err
	}

	hasher := sha256.New()
	capture := &captureWriter{limit: previewLimit}
	writer := io.MultiWriter(tmp, hasher, capture)

	var size int64
	if req.MaxBytes > 0 {
		size, err = io.Copy(writer, io.LimitReader(req.Reader, req.MaxBytes+1))
		if err != nil {
			return Result{}, err
		}
		if size > req.MaxBytes {
			return Result{}, fmt.Errorf("%w (max %d bytes)", ErrTooLarge, req.MaxBytes)
		}
	} else {
		size, err = io.Copy(writer, req.Reader)
		if err != nil {
			return Result{}, err
		}
	}
	if err := tmp.Sync(); err != nil {
		return Result{}, err
	}
	if err := tmp.Close(); err != nil {
		return Result{}, err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		return Result{}, err
	}
	keep = true

	sum := hasher.Sum(nil)
	eval := Evaluate(EvaluateRequest{
		FileName:     req.FileName,
		DeclaredMIME: req.DeclaredMIME,
		SizeBytes:    size,
		SHA256:       sum,
		Preview:      capture.buf.Bytes(),
		PreviewLimit: previewLimit,
		ReaderAt:     fileReaderAt{path: target},
	})
	return Result{
		Evaluation: eval,
		TargetPath: target,
		ByteSize:   size,
		SHA256:     append([]byte(nil), sum...),
		SHA256Hex:  hex.EncodeToString(sum),
	}, nil
}

type EvaluateRequest struct {
	FileName     string
	DeclaredMIME string
	SizeBytes    int64
	SHA256       []byte
	Preview      []byte
	PreviewLimit int
	ReaderAt     io.ReaderAt
}

func Evaluate(req EvaluateRequest) Evaluation {
	fileName, _ := SafeFileName(req.FileName)
	if fileName == "" {
		fileName = "attachment"
	}
	previewLimit := req.PreviewLimit
	if previewLimit <= 0 {
		previewLimit = DefaultPreviewBytes
	}
	preview := append([]byte(nil), req.Preview...)
	declared := NormalizeMIME(req.DeclaredMIME)
	ext := strings.ToLower(strings.TrimSpace(path.Ext(fileName)))
	extMIME := MIMEByExtension(ext)
	sniffed := ""
	if len(preview) > 0 {
		sniffed = NormalizeMIME(http.DetectContentType(preview))
	}
	container := ""
	containerMIME := ""
	if req.ReaderAt != nil && req.SizeBytes > 0 {
		container, containerMIME = detectOOXML(req.ReaderAt, req.SizeBytes)
	}

	normalized := chooseMIME(declared, extMIME, sniffed, containerMIME)
	kind := classifyKind(normalized, ext, container)
	isText := isTextLike(fileName, normalized, declared, extMIME, sniffed, preview)
	previewText, previewAvailable := previewText(preview, isText)
	previewTruncated := req.SizeBytes > int64(len(preview))
	contentNote := ""
	unsupportedPreview := false
	if previewText == "" {
		unsupportedPreview = true
		contentNote = "Binary or unsupported text preview omitted. File type was evaluated before further processing."
	}
	if kind == KindUnknown && !isText {
		kind = KindBinary
	}
	sha := append([]byte(nil), req.SHA256...)
	shaHex := ""
	if len(sha) > 0 {
		shaHex = hex.EncodeToString(sha)
	}
	e := Evaluation{
		FileName:           fileName,
		Extension:          strings.TrimPrefix(ext, "."),
		DeclaredMIME:       declared,
		ExtensionMIME:      extMIME,
		SniffedMIME:        sniffed,
		NormalizedMIME:     normalized,
		Kind:               kind,
		ContainerFormat:    container,
		SizeBytes:          req.SizeBytes,
		SHA256:             sha,
		SHA256Hex:          shaHex,
		IsText:             isText,
		PreviewAvailable:   previewAvailable,
		PreviewTruncated:   previewTruncated,
		PreviewText:        previewText,
		ContentNote:        contentNote,
		Language:           LanguageForFileName(fileName),
		OOXMLContainer:     container != "",
		UnsupportedPreview: unsupportedPreview,
	}
	e.Capabilities = capabilitiesFor(e)
	return e
}

func EvaluateBytes(fileName string, declaredMIME string, raw []byte) Evaluation {
	sum := sha256.Sum256(raw)
	preview := raw
	if len(preview) > DefaultPreviewBytes {
		preview = preview[:DefaultPreviewBytes]
	}
	return Evaluate(EvaluateRequest{
		FileName:     fileName,
		DeclaredMIME: declaredMIME,
		SizeBytes:    int64(len(raw)),
		SHA256:       sum[:],
		Preview:      preview,
		PreviewLimit: DefaultPreviewBytes,
		ReaderAt:     bytes.NewReader(raw),
	})
}

func EvaluatePath(fileName string, declaredMIME string, fullPath string, previewBytes int, includeHash bool) (Evaluation, error) {
	f, err := os.Open(strings.TrimSpace(fullPath))
	if err != nil {
		return Evaluation{}, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return Evaluation{}, err
	}
	if info == nil || info.IsDir() {
		return Evaluation{}, errors.New("fileingest: path is not a file")
	}
	previewLimit := previewBytes
	if previewLimit <= 0 {
		previewLimit = DefaultPreviewBytes
	}
	preview, err := io.ReadAll(io.LimitReader(f, int64(previewLimit)))
	if err != nil {
		return Evaluation{}, err
	}
	var sum []byte
	if includeHash {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return Evaluation{}, err
		}
		hasher := sha256.New()
		if _, err := io.Copy(hasher, f); err != nil {
			return Evaluation{}, err
		}
		sum = hasher.Sum(nil)
	}
	return Evaluate(EvaluateRequest{
		FileName:     fileName,
		DeclaredMIME: declaredMIME,
		SizeBytes:    info.Size(),
		SHA256:       sum,
		Preview:      preview,
		PreviewLimit: previewLimit,
		ReaderAt:     f,
	}), nil
}

func (e Evaluation) Metadata(source Source) map[string]any {
	meta := map[string]any{
		"fileName":           e.FileName,
		"mimeType":           e.NormalizedMIME,
		"normalizedMimeType": e.NormalizedMIME,
		"detectedMimeType":   firstNonEmpty(e.SniffedMIME, e.NormalizedMIME),
		"fileKind":           string(e.Kind),
		"sizeBytes":          e.SizeBytes,
		"sha256":             e.SHA256Hex,
		"isText":             e.IsText,
		"previewAvailable":   e.PreviewAvailable,
		"previewTruncated":   e.PreviewTruncated,
		"capabilities":       e.CapabilitiesMap(),
	}
	if e.DeclaredMIME != "" {
		meta["declaredMimeType"] = e.DeclaredMIME
	}
	if e.ExtensionMIME != "" {
		meta["extensionMimeType"] = e.ExtensionMIME
	}
	if e.SniffedMIME != "" {
		meta["sniffedMimeType"] = e.SniffedMIME
	}
	if e.Extension != "" {
		meta["extension"] = e.Extension
	}
	if e.ContainerFormat != "" {
		meta["containerFormat"] = e.ContainerFormat
		meta["ooxmlContainer"] = true
	}
	if e.PreviewText != "" {
		meta["previewText"] = e.PreviewText
	}
	if e.ContentNote != "" {
		meta["contentNote"] = e.ContentNote
	}
	if e.Language != "" {
		meta["language"] = e.Language
	}
	if source != "" {
		meta["source"] = string(source)
	}
	return meta
}

func (e Evaluation) CapabilitiesMap() map[string]bool {
	return map[string]bool{
		"canPreviewText":           e.Capabilities.CanPreviewText,
		"canSearchText":            e.Capabilities.CanSearchText,
		"canExtractStructuredData": e.Capabilities.CanExtractStructuredData,
		"canUseNativeImage":        e.Capabilities.CanUseNativeImage,
		"canUseNativeDocument":     e.Capabilities.CanUseNativeDocument,
		"safeToInline":             e.Capabilities.SafeToInline,
	}
}

func (e Evaluation) DisplayContent() string {
	if strings.TrimSpace(e.PreviewText) != "" {
		return strings.TrimSpace(e.PreviewText)
	}
	return strings.TrimSpace(e.ContentNote)
}

func SafeFileName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	name = strings.TrimSpace(name)
	if name == "." || name == "/" || name == "" {
		return "", errors.New("missing attachment file name")
	}
	if strings.ContainsRune(name, '\x00') {
		return "", errors.New("invalid attachment file name")
	}
	return name, nil
}

func NormalizeMIME(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if mimeType, _, err := mime.ParseMediaType(trimmed); err == nil {
		return strings.ToLower(strings.TrimSpace(mimeType))
	}
	if idx := strings.Index(trimmed, ";"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	return strings.ToLower(strings.TrimSpace(trimmed))
}

func MIMEByExtension(ext string) string {
	ext = strings.ToLower(strings.TrimSpace(ext))
	if ext == "" {
		return ""
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	switch ext {
	case ".csv":
		return "text/csv"
	case ".tsv":
		return "text/tab-separated-values"
	case ".json", ".jsonl":
		return "application/json"
	case ".md", ".markdown":
		return "text/markdown"
	case ".yaml", ".yml":
		return "application/yaml"
	case ".toml":
		return "application/toml"
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".key":
		return "application/vnd.apple.keynote"
	case ".doc":
		return "application/msword"
	case ".rtf":
		return "application/rtf"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".ppt":
		return "application/vnd.ms-powerpoint"
	case ".zip":
		return "application/zip"
	case ".tar":
		return "application/x-tar"
	case ".gz", ".gzip", ".tgz":
		return "application/gzip"
	}
	return NormalizeMIME(mime.TypeByExtension(ext))
}

func LanguageForFileName(fileName string) string {
	switch strings.ToLower(path.Ext(fileName)) {
	case ".go":
		return "go"
	case ".js":
		return "javascript"
	case ".jsx":
		return "jsx"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".py":
		return "python"
	case ".rb":
		return "ruby"
	case ".rs":
		return "rust"
	case ".java":
		return "java"
	case ".sql":
		return "sql"
	case ".json", ".jsonl":
		return "json"
	case ".yaml", ".yml":
		return "yaml"
	case ".toml":
		return "toml"
	case ".xml", ".svg":
		return "xml"
	case ".html", ".htm":
		return "html"
	case ".css":
		return "css"
	case ".scss":
		return "scss"
	case ".md", ".markdown":
		return "markdown"
	case ".csv":
		return "csv"
	case ".tsv":
		return "tsv"
	case ".txt", ".log":
		return "text"
	default:
		return ""
	}
}

type fileReaderAt struct {
	path string
}

func (r fileReaderAt) ReadAt(p []byte, off int64) (int, error) {
	f, err := os.Open(r.path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return f.ReadAt(p, off)
}

func detectOOXML(reader io.ReaderAt, size int64) (string, string) {
	zr, err := zip.NewReader(reader, size)
	if err != nil {
		return "", ""
	}
	hasContentTypes := false
	for _, f := range zr.File {
		name := strings.Trim(strings.ToLower(strings.ReplaceAll(f.Name, "\\", "/")), "/")
		switch name {
		case "[content_types].xml":
			hasContentTypes = true
		case "word/document.xml":
			return "docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		case "xl/workbook.xml":
			return "xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
		case "ppt/presentation.xml":
			return "pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation"
		}
	}
	if hasContentTypes {
		return "ooxml", "application/zip"
	}
	return "", ""
}

func chooseMIME(declared, extension, sniffed, container string) string {
	if container != "" && container != "application/zip" {
		return container
	}
	if strongSniff(sniffed) {
		return sniffed
	}
	if extension != "" && extension != "application/octet-stream" {
		return extension
	}
	if declared != "" && declared != "application/octet-stream" {
		return declared
	}
	if container != "" {
		return container
	}
	if sniffed != "" {
		return sniffed
	}
	return "application/octet-stream"
}

func strongSniff(sniffed string) bool {
	switch sniffed {
	case "", "application/octet-stream", "text/plain; charset=utf-8", "text/plain":
		return false
	case "application/zip":
		return false
	default:
		return true
	}
}

func classifyKind(mimeType, ext, container string) Kind {
	switch container {
	case "docx":
		return KindDocument
	case "xlsx":
		return KindSpreadsheet
	case "pptx":
		return KindPresentation
	}
	m := strings.ToLower(strings.TrimSpace(mimeType))
	switch {
	case strings.HasPrefix(m, "image/"):
		return KindImage
	case m == "application/pdf":
		return KindPDF
	case m == "text/csv", m == "application/csv", m == "text/tab-separated-values":
		return KindCSV
	case m == "application/json", strings.HasSuffix(m, "+json"):
		return KindJSON
	case m == "text/html", m == "application/xhtml+xml":
		return KindHTML
	case strings.HasPrefix(m, "text/"):
		return KindText
	case strings.Contains(m, "spreadsheet") || m == "application/vnd.ms-excel":
		return KindSpreadsheet
	case strings.Contains(m, "presentation") || m == "application/vnd.ms-powerpoint" || strings.Contains(m, "keynote"):
		return KindPresentation
	case strings.Contains(m, "wordprocessingml") || m == "application/msword" || m == "application/rtf" || m == "text/rtf":
		return KindDocument
	case m == "application/zip" || strings.Contains(m, "tar") || strings.Contains(m, "gzip"):
		return KindArchive
	}
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "csv", "tsv", "tab":
		return KindCSV
	case "json", "jsonl":
		return KindJSON
	case "html", "htm":
		return KindHTML
	case "txt", "md", "markdown", "log", "yaml", "yml", "toml", "xml", "svg", "sql", "go", "js", "jsx", "ts", "tsx", "py", "rb", "rs", "java", "css", "scss", "sh", "bash", "zsh":
		return KindText
	case "pdf":
		return KindPDF
	case "xls", "xlsx":
		return KindSpreadsheet
	case "ppt", "pptx", "key":
		return KindPresentation
	case "doc", "docx", "rtf":
		return KindDocument
	case "png", "jpg", "jpeg", "gif", "webp":
		return KindImage
	case "zip", "tar", "gz", "tgz":
		return KindArchive
	}
	return KindUnknown
}

func isTextLike(fileName string, mimeValues ...any) bool {
	var preview []byte
	for _, value := range mimeValues {
		if b, ok := value.([]byte); ok {
			preview = b
			break
		}
	}
	if len(preview) == 0 {
		return false
	}
	primaryMIME := firstMIMEString(mimeValues...)
	if isKnownNonTextPreviewMIME(primaryMIME) {
		return false
	}
	if isKnownTextPreviewMIME(primaryMIME) {
		return true
	}
	for _, value := range mimeValues {
		v, ok := value.(string)
		if !ok {
			continue
		}
		if isKnownTextPreviewMIME(v) {
			return true
		}
	}
	switch strings.ToLower(path.Ext(fileName)) {
	case ".txt", ".md", ".markdown", ".json", ".jsonl", ".yaml", ".yml", ".toml", ".ini",
		".cfg", ".conf", ".log", ".csv", ".tsv", ".sql", ".py", ".go", ".js", ".jsx",
		".ts", ".tsx", ".html", ".htm", ".css", ".scss", ".xml", ".svg", ".sh", ".bash",
		".zsh", ".java", ".rb", ".rs", ".c", ".h", ".cpp", ".hpp":
		return true
	}
	if hasKnownNonTextFileType(fileName, mimeValues...) {
		return false
	}
	if bytes.IndexByte(preview, 0) >= 0 {
		return false
	}
	return utf8.Valid(preview)
}

func hasKnownNonTextFileType(fileName string, mimeValues ...any) bool {
	for _, value := range mimeValues {
		v, ok := value.(string)
		if !ok {
			continue
		}
		if isKnownTextPreviewMIME(v) {
			continue
		}
		if isKnownNonTextPreviewMIME(v) {
			return true
		}
	}
	switch strings.ToLower(path.Ext(fileName)) {
	case ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".zip", ".tar", ".gz", ".gzip", ".tgz",
		".7z", ".doc", ".docx", ".rtf", ".xls", ".xlsx", ".ppt", ".pptx", ".key":
		return true
	default:
		return false
	}
}

func firstMIMEString(values ...any) string {
	for _, value := range values {
		if v, ok := value.(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(strings.ToLower(v))
		}
	}
	return ""
}

func isKnownTextPreviewMIME(raw string) bool {
	v := strings.TrimSpace(strings.ToLower(raw))
	return strings.HasPrefix(v, "text/") && v != "text/rtf" ||
		v == "application/json" ||
		v == "application/ld+json" ||
		v == "application/xml" ||
		v == "application/x-yaml" ||
		v == "application/yaml" ||
		v == "application/toml" ||
		v == "application/x-sh" ||
		v == "application/javascript" ||
		v == "application/x-javascript" ||
		v == "image/svg+xml" ||
		strings.HasSuffix(v, "+json") ||
		strings.HasSuffix(v, "+xml")
}

func isKnownNonTextPreviewMIME(raw string) bool {
	v := strings.TrimSpace(strings.ToLower(raw))
	return v == "application/pdf" ||
		strings.HasPrefix(v, "image/") && v != "image/svg+xml" ||
		v == "application/zip" ||
		v == "application/gzip" ||
		v == "application/x-gzip" ||
		v == "application/x-tar" ||
		v == "application/x-7z-compressed" ||
		v == "application/msword" ||
		v == "application/rtf" ||
		v == "text/rtf" ||
		v == "application/vnd.ms-excel" ||
		v == "application/vnd.ms-powerpoint" ||
		v == "application/vnd.apple.keynote" ||
		v == "application/x-iwork-keynote-sffkey" ||
		strings.Contains(v, "wordprocessingml") ||
		strings.Contains(v, "spreadsheetml") ||
		strings.Contains(v, "presentationml") ||
		strings.Contains(v, "keynote")
}

func previewText(preview []byte, isText bool) (string, bool) {
	if !isText || len(preview) == 0 {
		return "", false
	}
	sanitized := string(bytes.ToValidUTF8(preview, []byte("?")))
	sanitized = strings.TrimSpace(sanitized)
	if sanitized == "" {
		return "", false
	}
	return sanitized, true
}

func capabilitiesFor(e Evaluation) Capabilities {
	canNativeImage := e.Kind == KindImage && (e.NormalizedMIME == "image/png" || e.NormalizedMIME == "image/jpeg" || e.NormalizedMIME == "image/webp" || e.NormalizedMIME == "image/gif")
	canNativeDocument := e.IsText || e.Kind == KindPDF || e.Kind == KindText || e.Kind == KindCSV || e.Kind == KindJSON || e.Kind == KindHTML || e.Kind == KindDocument || e.Kind == KindSpreadsheet || e.Kind == KindPresentation
	canExtractStructured := e.Kind == KindCSV || e.Kind == KindSpreadsheet || e.Kind == KindPresentation || e.Kind == KindDocument || e.Kind == KindPDF
	return Capabilities{
		CanPreviewText:           e.PreviewAvailable,
		CanSearchText:            e.IsText,
		CanExtractStructuredData: canExtractStructured,
		CanUseNativeImage:        canNativeImage,
		CanUseNativeDocument:     canNativeDocument,
		SafeToInline:             e.IsText && e.SizeBytes <= int64(DefaultPreviewBytes),
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
