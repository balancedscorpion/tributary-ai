package imageextract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const Version = 1
const ocrTimeout = 20 * time.Second

var errTesseractUnavailable = errors.New("tesseract unavailable")

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
	MIMEType     string
	EnableOCR    bool
}

type Result struct {
	Manifest  Manifest
	Image     ImageSummary
	OutputDir string
}

type Manifest struct {
	Version                   int             `json:"version"`
	Pipeline                  string          `json:"pipeline"`
	Status                    Status          `json:"status"`
	FileName                  string          `json:"fileName"`
	SourceSHA256              string          `json:"sourceSha256,omitempty"`
	CreatedAt                 time.Time       `json:"createdAt"`
	MIMEType                  string          `json:"mimeType,omitempty"`
	Format                    string          `json:"format,omitempty"`
	Width                     int             `json:"width"`
	Height                    int             `json:"height"`
	PixelCount                int64           `json:"pixelCount"`
	Animated                  bool            `json:"animated"`
	FrameCount                int             `json:"frameCount"`
	HasAlpha                  bool            `json:"hasAlpha"`
	NativeVisionEligible      bool            `json:"nativeVisionEligible"`
	NativeVisionBlockedReason string          `json:"nativeVisionBlockedReason,omitempty"`
	OCRStatus                 string          `json:"ocrStatus"`
	Outputs                   map[string]any  `json:"outputs"`
	Capabilities              map[string]bool `json:"capabilities"`
	Warnings                  []string        `json:"warnings,omitempty"`
}

type ImageSummary struct {
	Version                   int      `json:"version"`
	FileName                  string   `json:"fileName"`
	MIMEType                  string   `json:"mimeType,omitempty"`
	Format                    string   `json:"format,omitempty"`
	Width                     int      `json:"width"`
	Height                    int      `json:"height"`
	PixelCount                int64    `json:"pixelCount"`
	Animated                  bool     `json:"animated"`
	FrameCount                int      `json:"frameCount"`
	HasAlpha                  bool     `json:"hasAlpha"`
	NativeVisionEligible      bool     `json:"nativeVisionEligible"`
	NativeVisionBlockedReason string   `json:"nativeVisionBlockedReason,omitempty"`
	OCRStatus                 string   `json:"ocrStatus"`
	OCRText                   string   `json:"ocrText,omitempty"`
	Warnings                  []string `json:"warnings,omitempty"`
}

func Extract(req Request) (Result, error) {
	sourcePath := strings.TrimSpace(req.SourcePath)
	if sourcePath == "" {
		return Result{}, errors.New("imageextract: source path is required")
	}
	outputDir := strings.TrimSpace(req.OutputDir)
	if outputDir == "" {
		return Result{}, errors.New("imageextract: output dir is required")
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
	mimeType := strings.ToLower(strings.TrimSpace(req.MIMEType))
	summary := ImageSummary{Version: Version, FileName: fileName, MIMEType: mimeType, OCRStatus: "not_run"}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err == nil {
		summary.Format = format
		summary.Width = cfg.Width
		summary.Height = cfg.Height
		summary.PixelCount = int64(cfg.Width) * int64(cfg.Height)
	} else if strings.Contains(mimeType, "webp") || strings.EqualFold(filepath.Ext(fileName), ".webp") {
		width, height, parseErr := parseWebPDimensions(raw)
		if parseErr != nil {
			summary.Warnings = append(summary.Warnings, "could not decode image dimensions: "+err.Error())
		} else {
			summary.Format = "webp"
			summary.Width = width
			summary.Height = height
			summary.PixelCount = int64(width) * int64(height)
		}
	} else {
		summary.Warnings = append(summary.Warnings, "could not decode image dimensions: "+err.Error())
	}
	if summary.Format == "gif" {
		if decoded, err := gif.DecodeAll(bytes.NewReader(raw)); err == nil {
			summary.FrameCount = len(decoded.Image)
			summary.Animated = summary.FrameCount > 1
		}
	}
	if summary.FrameCount == 0 && summary.Width > 0 {
		summary.FrameCount = 1
	}
	summary.HasAlpha = hasAlpha(raw, summary.Format)
	summary.NativeVisionEligible, summary.NativeVisionBlockedReason = nativeVisionEligibility(summary)
	if req.EnableOCR {
		ocrText, err := runOCR(sourcePath)
		switch {
		case err == nil && strings.TrimSpace(ocrText) != "":
			summary.OCRStatus = "completed"
			summary.OCRText = strings.TrimSpace(ocrText)
		case err == nil:
			summary.OCRStatus = "no_text"
		case errors.Is(err, errTesseractUnavailable):
			summary.OCRStatus = "skipped"
			summary.Warnings = append(summary.Warnings, err.Error())
		default:
			summary.OCRStatus = "failed"
			summary.Warnings = append(summary.Warnings, "OCR failed: "+err.Error())
		}
	}
	outputs := map[string]any{
		"manifestPath": "manifest.json",
		"imagePath":    "image.json",
		"markdownPath": "image.md",
	}
	canReadOCR := strings.TrimSpace(summary.OCRText) != ""
	if canReadOCR {
		outputs["ocrTextPath"] = "ocr.txt"
	}
	manifest := Manifest{
		Version:                   Version,
		Pipeline:                  "image",
		Status:                    StatusComplete,
		FileName:                  fileName,
		SourceSHA256:              strings.TrimSpace(req.SourceSHA256),
		CreatedAt:                 time.Now().UTC(),
		MIMEType:                  summary.MIMEType,
		Format:                    summary.Format,
		Width:                     summary.Width,
		Height:                    summary.Height,
		PixelCount:                summary.PixelCount,
		Animated:                  summary.Animated,
		FrameCount:                summary.FrameCount,
		HasAlpha:                  summary.HasAlpha,
		NativeVisionEligible:      summary.NativeVisionEligible,
		NativeVisionBlockedReason: summary.NativeVisionBlockedReason,
		OCRStatus:                 summary.OCRStatus,
		Outputs:                   outputs,
		Capabilities: map[string]bool{
			"canUseNativeImage": summary.NativeVisionEligible,
			"canInspectImage":   true,
			"canReadOCR":        canReadOCR,
			"canSearchOCR":      canReadOCR,
		},
		Warnings: summary.Warnings,
	}
	if len(summary.Warnings) > 0 {
		manifest.Status = StatusPartial
	}
	if err := writeJSON(filepath.Join(outputDir, "image.json"), summary); err != nil {
		return Result{}, err
	}
	if err := writeFile(filepath.Join(outputDir, "image.md"), []byte(imageMarkdown(summary))); err != nil {
		return Result{}, err
	}
	if canReadOCR {
		if err := writeFile(filepath.Join(outputDir, "ocr.txt"), []byte(summary.OCRText+"\n")); err != nil {
			return Result{}, err
		}
	}
	if err := writeJSON(filepath.Join(outputDir, "manifest.json"), manifest); err != nil {
		return Result{}, err
	}
	return Result{Manifest: manifest, Image: summary, OutputDir: outputDir}, nil
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
		"kind":                      "image",
		"pipeline":                  "image",
		"version":                   r.Manifest.Version,
		"status":                    string(r.Manifest.Status),
		"rootPath":                  rootRelPath,
		"manifestPath":              rel("manifest.json"),
		"imagePath":                 rel("image.json"),
		"dataPath":                  rel("image.json"),
		"markdownPath":              rel("image.md"),
		"width":                     r.Manifest.Width,
		"height":                    r.Manifest.Height,
		"pixelCount":                r.Manifest.PixelCount,
		"animated":                  r.Manifest.Animated,
		"frameCount":                r.Manifest.FrameCount,
		"hasAlpha":                  r.Manifest.HasAlpha,
		"nativeVisionEligible":      r.Manifest.NativeVisionEligible,
		"nativeVisionBlockedReason": r.Manifest.NativeVisionBlockedReason,
		"ocrStatus":                 r.Manifest.OCRStatus,
		"capabilities":              r.Manifest.Capabilities,
		"warnings":                  r.Manifest.Warnings,
	}
	if r.Manifest.Format != "" {
		meta["format"] = r.Manifest.Format
	}
	if r.Manifest.MIMEType != "" {
		meta["mimeType"] = r.Manifest.MIMEType
	}
	if r.Manifest.SourceSHA256 != "" {
		meta["sourceSha256"] = r.Manifest.SourceSHA256
	}
	if relPath, ok := r.Manifest.Outputs["ocrTextPath"].(string); ok && strings.TrimSpace(relPath) != "" {
		meta["ocrTextPath"] = rel(relPath)
	}
	return meta
}

func FailedMetadata(err error) map[string]any {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return map[string]any{
		"kind":     "image",
		"pipeline": "image",
		"version":  Version,
		"status":   string(StatusFailed),
		"error":    msg,
	}
}

func failedManifest(req Request, err error) Manifest {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return Manifest{
		Version:      Version,
		Pipeline:     "image",
		Status:       StatusFailed,
		FileName:     firstNonEmpty(strings.TrimSpace(req.FileName), filepath.Base(strings.TrimSpace(req.SourcePath))),
		SourceSHA256: strings.TrimSpace(req.SourceSHA256),
		CreatedAt:    time.Now().UTC(),
		Outputs:      map[string]any{},
		Warnings:     []string{msg},
	}
}

func nativeVisionEligibility(summary ImageSummary) (bool, string) {
	if summary.Width <= 0 || summary.Height <= 0 {
		return false, "dimensions unavailable"
	}
	if summary.Animated {
		return false, "animated image"
	}
	switch strings.ToLower(firstNonEmpty(summary.Format, strings.TrimPrefix(filepath.Ext(summary.FileName), "."))) {
	case "png", "jpeg", "jpg", "webp", "gif":
		return true, ""
	default:
		return false, "unsupported image format"
	}
}

func hasAlpha(raw []byte, format string) bool {
	if format == "png" && len(raw) > 25 && bytes.Equal(raw[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		colorType := raw[25]
		return colorType == 4 || colorType == 6
	}
	return false
}

func parseWebPDimensions(raw []byte) (int, int, error) {
	if len(raw) < 30 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WEBP" {
		return 0, 0, errors.New("not a WebP RIFF file")
	}
	chunk := string(raw[12:16])
	switch chunk {
	case "VP8X":
		if len(raw) < 30 {
			return 0, 0, errors.New("short VP8X header")
		}
		width := 1 + int(raw[24]) + int(raw[25])<<8 + int(raw[26])<<16
		height := 1 + int(raw[27]) + int(raw[28])<<8 + int(raw[29])<<16
		return width, height, nil
	case "VP8 ":
		if len(raw) < 30 {
			return 0, 0, errors.New("short VP8 header")
		}
		width := int(binary.LittleEndian.Uint16(raw[26:28]) & 0x3fff)
		height := int(binary.LittleEndian.Uint16(raw[28:30]) & 0x3fff)
		return width, height, nil
	case "VP8L":
		if len(raw) < 25 {
			return 0, 0, errors.New("short VP8L header")
		}
		b0, b1, b2, b3 := uint32(raw[21]), uint32(raw[22]), uint32(raw[23]), uint32(raw[24])
		width := int(1 + (((b1 & 0x3f) << 8) | b0))
		height := int(1 + (((b3 & 0x0f) << 10) | (b2 << 2) | ((b1 & 0xc0) >> 6)))
		return width, height, nil
	default:
		return 0, 0, fmt.Errorf("unsupported WebP chunk %q", chunk)
	}
}

func runOCR(sourcePath string) (string, error) {
	bin, err := exec.LookPath("tesseract")
	if err != nil {
		return "", fmt.Errorf("%w: %v", errTesseractUnavailable, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), ocrTimeout)
	defer cancel()

	dir := filepath.Dir(sourcePath)
	name := filepath.Base(sourcePath)
	cmd := exec.CommandContext(ctx, bin, name, "stdout", "--psm", "6")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	text := strings.TrimSpace(string(bytes.ToValidUTF8(out, []byte("?"))))
	if err != nil {
		errText := strings.TrimSpace(string(bytes.ToValidUTF8(stderr.Bytes(), []byte("?"))))
		if errText != "" {
			return "", fmt.Errorf("%v: %s", err, errText)
		}
		return "", err
	}
	return text, nil
}

func imageMarkdown(summary ImageSummary) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(firstNonEmpty(summary.FileName, "Image"))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("- Format: %s\n", firstNonEmpty(summary.Format, "unknown")))
	if summary.MIMEType != "" {
		b.WriteString(fmt.Sprintf("- MIME type: %s\n", summary.MIMEType))
	}
	b.WriteString(fmt.Sprintf("- Dimensions: %d x %d\n", summary.Width, summary.Height))
	b.WriteString(fmt.Sprintf("- Pixels: %d\n", summary.PixelCount))
	b.WriteString(fmt.Sprintf("- Animated: %t\n", summary.Animated))
	b.WriteString(fmt.Sprintf("- Frames: %d\n", summary.FrameCount))
	b.WriteString(fmt.Sprintf("- Alpha: %t\n", summary.HasAlpha))
	b.WriteString(fmt.Sprintf("- Native vision eligible: %t\n", summary.NativeVisionEligible))
	if summary.NativeVisionBlockedReason != "" {
		b.WriteString(fmt.Sprintf("- Native vision blocked reason: %s\n", summary.NativeVisionBlockedReason))
	}
	b.WriteString(fmt.Sprintf("- OCR status: %s\n", summary.OCRStatus))
	if ocrText := strings.TrimSpace(summary.OCRText); ocrText != "" {
		b.WriteString("\n## OCR Text\n\n")
		b.WriteString(ocrText)
		b.WriteString("\n")
	}
	if len(summary.Warnings) > 0 {
		b.WriteString("\n## Warnings\n\n")
		for _, warning := range summary.Warnings {
			b.WriteString("- ")
			b.WriteString(warning)
			b.WriteString("\n")
		}
	}
	return b.String()
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
