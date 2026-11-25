package filepipeline

import (
	"path/filepath"
	"strings"

	"ai-analyst-local/internal/chat/fileingest"
	"ai-analyst-local/internal/chat/imageextract"
)

type ImageExtractionRequest struct {
	SourcePath    string
	OutputDir     string
	OutputRelPath string
	FileName      string
	Evaluation    fileingest.Evaluation
	EnableOCR     bool
}

func ShouldExtractImage(eval fileingest.Evaluation) bool {
	if eval.Kind != fileingest.KindImage {
		return false
	}
	mimeType := strings.ToLower(strings.TrimSpace(eval.NormalizedMIME))
	ext := strings.ToLower(strings.TrimSpace(eval.Extension))
	switch {
	case mimeType == "image/png", mimeType == "image/jpeg", mimeType == "image/webp", mimeType == "image/gif":
		return true
	case ext == "png", ext == "jpg", ext == "jpeg", ext == "webp", ext == "gif":
		return true
	default:
		return false
	}
}

func ExtractImage(req ImageExtractionRequest) map[string]any {
	if !ShouldExtractImage(req.Evaluation) {
		return nil
	}
	sourcePath := strings.TrimSpace(req.SourcePath)
	outputDir := strings.TrimSpace(req.OutputDir)
	if sourcePath == "" || outputDir == "" {
		return imageextract.FailedMetadata(errString("missing source path or output dir"))
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = req.Evaluation.FileName
	}
	if fileName == "" {
		fileName = filepath.Base(sourcePath)
	}
	result, err := imageextract.Extract(imageextract.Request{
		SourcePath:   sourcePath,
		FileName:     fileName,
		OutputDir:    outputDir,
		SourceSHA256: req.Evaluation.SHA256Hex,
		MIMEType:     req.Evaluation.NormalizedMIME,
		EnableOCR:    req.EnableOCR,
	})
	if err != nil {
		return imageextract.FailedMetadata(err)
	}
	return result.Metadata(req.OutputRelPath)
}
