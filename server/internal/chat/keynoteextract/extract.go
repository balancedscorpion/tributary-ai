package keynoteextract

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	Version                    = 1
	DefaultMaxEntries          = 4000
	DefaultMaxUncompressedSize = 768 << 20
	DefaultExternalTimeout     = 90 * time.Second
)

type StageStatus string

const (
	StageCompleted StageStatus = "completed"
	StageSkipped   StageStatus = "skipped"
	StageFailed    StageStatus = "failed"
)

type Status string

const (
	StatusComplete Status = "complete"
	StatusPartial  Status = "partial"
	StatusFailed   Status = "failed"
)

type Request struct {
	SourcePath             string
	FileName               string
	OutputDir              string
	SourceSHA256           string
	EnableRendering        bool
	EnableOCR              bool
	ExternalCommandTimeout time.Duration
	MaxEntries             int
	MaxUncompressedSize    int64
}

type Result struct {
	Manifest  Manifest
	OutputDir string
}

type Manifest struct {
	Version      int             `json:"version"`
	Pipeline     string          `json:"pipeline"`
	Status       Status          `json:"status"`
	FileName     string          `json:"fileName"`
	SourceSHA256 string          `json:"sourceSha256,omitempty"`
	CreatedAt    time.Time       `json:"createdAt"`
	SlideCount   int             `json:"slideCount"`
	Outputs      map[string]any  `json:"outputs"`
	Capabilities map[string]bool `json:"capabilities"`
	Stages       []Stage         `json:"stages"`
	Warnings     []string        `json:"warnings,omitempty"`
}

type Stage struct {
	Name    string      `json:"name"`
	Status  StageStatus `json:"status"`
	Detail  string      `json:"detail,omitempty"`
	Outputs []string    `json:"outputs,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type previewCandidate struct {
	Name     string
	Ext      string
	Priority int
}

type ocrResult struct {
	Slide int
	Path  string
	Text  string
}

func Extract(ctx context.Context, req Request) (Result, error) {
	sourcePath := strings.TrimSpace(req.SourcePath)
	if sourcePath == "" {
		return Result{}, errors.New("keynoteextract: source path is required")
	}
	outputDir := strings.TrimSpace(req.OutputDir)
	if outputDir == "" {
		return Result{}, errors.New("keynoteextract: output dir is required")
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = filepath.Base(sourcePath)
	}
	timeout := req.ExternalCommandTimeout
	if timeout <= 0 {
		timeout = DefaultExternalTimeout
	}
	if err := os.RemoveAll(outputDir); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return Result{}, err
	}

	manifest := Manifest{
		Version:      Version,
		Pipeline:     "keynote",
		Status:       StatusComplete,
		FileName:     fileName,
		SourceSHA256: strings.TrimSpace(req.SourceSHA256),
		CreatedAt:    time.Now().UTC(),
		Outputs:      map[string]any{},
		Capabilities: map[string]bool{},
	}

	renderedSlides, stages := extractEmbeddedPreviews(ctx, req, timeout)
	manifest.Stages = append(manifest.Stages, stages...)
	if len(renderedSlides) == 0 && req.EnableRendering {
		qlSlides, st := renderQuickLookThumbnail(ctx, sourcePath, outputDir, timeout)
		manifest.Stages = append(manifest.Stages, st)
		renderedSlides = append(renderedSlides, qlSlides...)
	} else if !req.EnableRendering {
		manifest.Stages = append(manifest.Stages, Stage{Name: "quicklook_render", Status: StageSkipped, Detail: "External rendering stage disabled"})
	}

	var ocr []ocrResult
	if req.EnableOCR && len(renderedSlides) > 0 {
		var st Stage
		ocr, st = runOCR(ctx, outputDir, renderedSlides, timeout)
		manifest.Stages = append(manifest.Stages, st)
		if len(ocr) > 0 {
			paths := make([]string, 0, len(ocr))
			for _, item := range ocr {
				paths = append(paths, item.Path)
			}
			manifest.Outputs["ocrText"] = paths
		}
	} else if req.EnableOCR {
		manifest.Stages = append(manifest.Stages, Stage{Name: "ocr", Status: StageSkipped, Detail: "No rendered Keynote preview images available for OCR"})
	} else {
		manifest.Stages = append(manifest.Stages, Stage{Name: "ocr", Status: StageSkipped, Detail: "External OCR stage disabled"})
	}

	manifest.SlideCount = len(renderedSlides)
	if len(renderedSlides) > 0 {
		manifest.Outputs["renderedSlides"] = renderedSlides
	}
	manifest.Outputs["markdownPath"] = "deck.md"
	manifest.Outputs["manifestPath"] = "manifest.json"
	manifest.Capabilities = map[string]bool{
		"canPreviewSlides": len(renderedSlides) > 0,
		"canReadOCR":       len(ocr) > 0,
		"canUseNativeFile": true,
	}
	if len(renderedSlides) == 0 {
		manifest.Status = StatusPartial
		manifest.Warnings = append(manifest.Warnings, "No embedded or Quick Look-rendered Keynote preview images were available.")
	}
	if err := writeFile(filepath.Join(outputDir, "deck.md"), []byte(renderMarkdown(fileName, renderedSlides, ocr))); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "manifest.json"), manifest); err != nil {
		return Result{}, err
	}
	return Result{Manifest: manifest, OutputDir: outputDir}, nil
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
		"kind":         "presentation",
		"pipeline":     "keynote",
		"version":      r.Manifest.Version,
		"status":       string(r.Manifest.Status),
		"slideCount":   r.Manifest.SlideCount,
		"rootPath":     rootRelPath,
		"manifestPath": rel("manifest.json"),
		"markdownPath": rel("deck.md"),
		"textPath":     rel("deck.md"),
		"capabilities": r.Manifest.Capabilities,
		"outputs":      r.Manifest.Outputs,
		"stages":       r.Manifest.Stages,
		"warnings":     r.Manifest.Warnings,
	}
	if rendered, ok := r.Manifest.Outputs["renderedSlides"].([]string); ok && len(rendered) > 0 {
		slides := make([]string, 0, len(rendered))
		for _, slide := range rendered {
			if clean := rel(slide); clean != "" {
				slides = append(slides, clean)
			}
		}
		if len(slides) > 0 {
			meta["renderedSlides"] = slides
			meta["previewImagePath"] = slides[0]
		}
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
		"kind":     "presentation",
		"pipeline": "keynote",
		"version":  Version,
		"status":   string(StatusFailed),
		"error":    msg,
	}
}

func extractEmbeddedPreviews(ctx context.Context, req Request, timeout time.Duration) ([]string, []Stage) {
	sourcePath := strings.TrimSpace(req.SourcePath)
	outputDir := strings.TrimSpace(req.OutputDir)
	zr, err := zip.OpenReader(sourcePath)
	if err != nil {
		return nil, []Stage{{Name: "keynote_package", Status: StageFailed, Error: "open keynote package: " + err.Error()}}
	}
	defer zr.Close()

	maxEntries := req.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	maxUncompressed := req.MaxUncompressedSize
	if maxUncompressed <= 0 {
		maxUncompressed = DefaultMaxUncompressedSize
	}
	var total uint64
	candidates := make([]previewCandidate, 0)
	for _, f := range zr.File {
		clean, ok := cleanZipName(f.Name)
		if !ok {
			return nil, []Stage{{Name: "keynote_package", Status: StageFailed, Error: fmt.Sprintf("unsafe keynote entry path %q", f.Name)}}
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if len(candidates) > maxEntries {
			break
		}
		total += f.UncompressedSize64
		if total > uint64(maxUncompressed) {
			return nil, []Stage{{Name: "keynote_package", Status: StageFailed, Error: "keynote package exceeds extraction safety limit"}}
		}
		if ext := previewImageExtension(clean); ext != "" && previewAssetName(clean) {
			candidates = append(candidates, previewCandidate{Name: clean, Ext: ext, Priority: previewPriority(clean, ext)})
		}
		if strings.EqualFold(path.Ext(clean), ".pdf") && previewAssetName(clean) {
			candidates = append(candidates, previewCandidate{Name: clean, Ext: ".pdf", Priority: previewPriority(clean, ".pdf")})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Priority != candidates[j].Priority {
			return candidates[i].Priority < candidates[j].Priority
		}
		return candidates[i].Name < candidates[j].Name
	})
	if len(candidates) == 0 {
		return nil, []Stage{
			{Name: "keynote_package", Status: StageCompleted, Detail: "Valid ZIP package, but no embedded Quick Look preview assets found"},
			{Name: "embedded_previews", Status: StageSkipped, Detail: "No embedded preview images or PDFs found"},
		}
	}

	var rendered []string
	var stages []Stage
	stages = append(stages, Stage{Name: "keynote_package", Status: StageCompleted, Detail: "Valid ZIP-based Keynote package"})
	for _, candidate := range candidates {
		if len(rendered) >= 60 {
			break
		}
		f := zipFileByName(zr.File, candidate.Name)
		if f == nil {
			continue
		}
		raw, err := readZipFile(f, maxUncompressed)
		if err != nil {
			stages = append(stages, Stage{Name: "embedded_previews", Status: StageFailed, Error: err.Error(), Outputs: rendered})
			return rendered, stages
		}
		if candidate.Ext == ".pdf" {
			tmpPDF := filepath.Join(outputDir, "quicklook-preview.pdf")
			if err := writeFile(tmpPDF, raw); err != nil {
				stages = append(stages, Stage{Name: "embedded_previews", Status: StageFailed, Error: err.Error(), Outputs: rendered})
				return rendered, stages
			}
			converted, st := renderPDFPreview(ctx, tmpPDF, outputDir, len(rendered)+1, timeout)
			stages = append(stages, st)
			rendered = append(rendered, converted...)
			continue
		}
		rel := path.Join("rendered", fmt.Sprintf("slide-%03d%s", len(rendered)+1, candidate.Ext))
		if err := writeFile(filepath.Join(outputDir, filepath.FromSlash(rel)), raw); err != nil {
			stages = append(stages, Stage{Name: "embedded_previews", Status: StageFailed, Error: err.Error(), Outputs: rendered})
			return rendered, stages
		}
		rendered = append(rendered, rel)
	}
	if len(rendered) == 0 {
		stages = append(stages, Stage{Name: "embedded_previews", Status: StageSkipped, Detail: "Embedded preview assets were present but could not be rendered"})
	} else {
		stages = append(stages, Stage{Name: "embedded_previews", Status: StageCompleted, Detail: fmt.Sprintf("Extracted %d Keynote preview image(s)", len(rendered)), Outputs: rendered})
	}
	return rendered, stages
}

func renderQuickLookThumbnail(ctx context.Context, sourcePath string, outputDir string, timeout time.Duration) ([]string, Stage) {
	qlmanage, ok := findExecutable("qlmanage")
	if !ok {
		return nil, Stage{Name: "quicklook_render", Status: StageSkipped, Detail: "qlmanage not found"}
	}
	tmpDir, err := os.MkdirTemp(outputDir, "quicklook-*")
	if err != nil {
		return nil, Stage{Name: "quicklook_render", Status: StageFailed, Error: err.Error()}
	}
	defer os.RemoveAll(tmpDir)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, qlmanage, "-t", "-s", "1600", "-o", tmpDir, sourcePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, Stage{Name: "quicklook_render", Status: StageFailed, Error: trimCommandOutput(err, out)}
	}
	matches, _ := filepath.Glob(filepath.Join(tmpDir, "*.png"))
	sort.Strings(matches)
	if len(matches) == 0 {
		return nil, Stage{Name: "quicklook_render", Status: StageSkipped, Detail: "Quick Look did not produce a thumbnail"}
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		return nil, Stage{Name: "quicklook_render", Status: StageFailed, Error: err.Error()}
	}
	rel := path.Join("rendered", "slide-001.png")
	if err := writeFile(filepath.Join(outputDir, filepath.FromSlash(rel)), raw); err != nil {
		return nil, Stage{Name: "quicklook_render", Status: StageFailed, Error: err.Error()}
	}
	return []string{rel}, Stage{Name: "quicklook_render", Status: StageCompleted, Detail: "Rendered Keynote thumbnail with Quick Look", Outputs: []string{rel}}
}

func renderPDFPreview(ctx context.Context, pdfPath string, outputDir string, startIndex int, timeout time.Duration) ([]string, Stage) {
	pdftoppm, ok := findExecutable("pdftoppm")
	if !ok {
		return nil, Stage{Name: "embedded_preview_pdf", Status: StageSkipped, Detail: "Poppler pdftoppm not found"}
	}
	tmpDir, err := os.MkdirTemp(outputDir, "preview-pdf-*")
	if err != nil {
		return nil, Stage{Name: "embedded_preview_pdf", Status: StageFailed, Error: err.Error()}
	}
	defer os.RemoveAll(tmpDir)
	prefix := filepath.Join(tmpDir, "page")
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, pdftoppm, "-png", "-r", "150", pdfPath, prefix)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, Stage{Name: "embedded_preview_pdf", Status: StageFailed, Error: trimCommandOutput(err, out)}
	}
	pages, _ := filepath.Glob(prefix + "-*.png")
	sort.Strings(pages)
	if len(pages) == 0 {
		return nil, Stage{Name: "embedded_preview_pdf", Status: StageFailed, Error: "pdftoppm did not produce preview images"}
	}
	rendered := make([]string, 0, len(pages))
	for i, pagePath := range pages {
		raw, err := os.ReadFile(pagePath)
		if err != nil {
			return rendered, Stage{Name: "embedded_preview_pdf", Status: StageFailed, Error: err.Error(), Outputs: rendered}
		}
		rel := path.Join("rendered", fmt.Sprintf("slide-%03d.png", startIndex+i))
		if err := writeFile(filepath.Join(outputDir, filepath.FromSlash(rel)), raw); err != nil {
			return rendered, Stage{Name: "embedded_preview_pdf", Status: StageFailed, Error: err.Error(), Outputs: rendered}
		}
		rendered = append(rendered, rel)
	}
	return rendered, Stage{Name: "embedded_preview_pdf", Status: StageCompleted, Detail: fmt.Sprintf("Rendered %d embedded preview PDF page(s)", len(rendered)), Outputs: rendered}
}

func runOCR(ctx context.Context, outputDir string, renderedSlides []string, timeout time.Duration) ([]ocrResult, Stage) {
	tesseract, ok := findExecutable("tesseract")
	if !ok {
		return nil, Stage{Name: "ocr", Status: StageSkipped, Detail: "Tesseract OCR not found"}
	}
	var results []ocrResult
	for i, rel := range renderedSlides {
		imagePath := filepath.Join(outputDir, filepath.FromSlash(rel))
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(runCtx, tesseract, filepath.Base(imagePath), "stdout", "--psm", "6")
		cmd.Dir = filepath.Dir(imagePath)
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			return results, Stage{Name: "ocr", Status: StageFailed, Error: trimCommandOutput(err, out), Outputs: ocrPaths(results)}
		}
		text := strings.TrimSpace(string(bytes.ToValidUTF8(out, []byte("?"))))
		if text == "" {
			continue
		}
		ocrRel := path.Join("ocr", fmt.Sprintf("slide-%03d.txt", i+1))
		if err := writeFile(filepath.Join(outputDir, filepath.FromSlash(ocrRel)), []byte(text+"\n")); err != nil {
			return results, Stage{Name: "ocr", Status: StageFailed, Error: err.Error(), Outputs: ocrPaths(results)}
		}
		results = append(results, ocrResult{Slide: i + 1, Path: ocrRel, Text: text})
	}
	if len(results) == 0 {
		return nil, Stage{Name: "ocr", Status: StageSkipped, Detail: "OCR produced no text"}
	}
	return results, Stage{Name: "ocr", Status: StageCompleted, Detail: fmt.Sprintf("OCR extracted text from %d Keynote preview image(s)", len(results)), Outputs: ocrPaths(results)}
}

func renderMarkdown(fileName string, renderedSlides []string, ocr []ocrResult) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(strings.TrimSpace(fileName))
	b.WriteString("\n\n")
	b.WriteString("- Format: Keynote\n")
	b.WriteString(fmt.Sprintf("- Rendered preview images: %d\n", len(renderedSlides)))
	if len(ocr) == 0 {
		b.WriteString("- OCR text: unavailable\n")
		return b.String()
	}
	b.WriteString("- OCR text: available\n\n")
	b.WriteString("## OCR Text\n\n")
	for _, item := range ocr {
		b.WriteString(fmt.Sprintf("### Slide %d\n\n", item.Slide))
		b.WriteString(strings.TrimSpace(item.Text))
		b.WriteString("\n\n")
	}
	return b.String()
}

func cleanZipName(raw string) (string, bool) {
	clean := path.Clean(strings.Trim(strings.ReplaceAll(raw, "\\", "/"), "/"))
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", false
	}
	return clean, true
}

func previewAssetName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(lower, "quicklook/") ||
		strings.Contains(lower, "preview") ||
		strings.Contains(lower, "thumbnail") ||
		strings.Contains(lower, "thumb")
}

func previewImageExtension(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		return ".png"
	case ".jpg", ".jpeg":
		return ".jpg"
	case ".webp":
		return ".webp"
	default:
		return ""
	}
}

func previewPriority(name string, ext string) int {
	lower := strings.ToLower(name)
	score := 100
	if strings.Contains(lower, "quicklook/preview") {
		score -= 60
	}
	if strings.Contains(lower, "preview") {
		score -= 30
	}
	if strings.Contains(lower, "thumbnail") || strings.Contains(lower, "thumb") {
		score -= 10
	}
	if ext == ".pdf" {
		score -= 20
	}
	return score
}

func zipFileByName(files []*zip.File, name string) *zip.File {
	for _, f := range files {
		clean, ok := cleanZipName(f.Name)
		if ok && clean == name {
			return f
		}
	}
	return nil
}

func readZipFile(f *zip.File, maxUncompressed int64) ([]byte, error) {
	if maxUncompressed <= 0 {
		maxUncompressed = DefaultMaxUncompressedSize
	}
	if f.UncompressedSize64 > uint64(maxUncompressed) {
		return nil, fmt.Errorf("keynote preview entry %q exceeds size limit", f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(rc, maxUncompressed+1)); err != nil {
		return nil, err
	}
	if int64(buf.Len()) > maxUncompressed {
		return nil, fmt.Errorf("keynote preview entry %q exceeds size limit", f.Name)
	}
	return buf.Bytes(), nil
}

func ocrPaths(results []ocrResult) []string {
	out := make([]string, 0, len(results))
	for _, item := range results {
		out = append(out, item.Path)
	}
	return out
}

func writeFile(fullPath string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(fullPath, data, 0o600)
}

func writeJSON(fullPath string, value any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		return err
	}
	return writeFile(fullPath, buf.Bytes())
}

func findExecutable(names ...string) (string, bool) {
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, true
		}
	}
	return "", false
}

func trimCommandOutput(err error, output []byte) string {
	msg := strings.TrimSpace(string(bytes.ToValidUTF8(output, []byte("?"))))
	if msg == "" && err != nil {
		msg = err.Error()
	}
	if len([]rune(msg)) > 2000 {
		r := []rune(msg)
		msg = string(r[:2000]) + "..."
	}
	return msg
}
