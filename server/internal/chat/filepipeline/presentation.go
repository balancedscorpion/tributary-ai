package filepipeline

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/fileingest"
	"ai-analyst-local/internal/chat/keynoteextract"
	"ai-analyst-local/internal/chat/pptxextract"
)

type PresentationExtractionRequest struct {
	SourcePath             string
	OutputDir              string
	OutputRelPath          string
	FileName               string
	Evaluation             fileingest.Evaluation
	IncludeRawXML          bool
	EnableExternalTools    bool
	EnableRendering        bool
	ExternalCommandTimeout time.Duration
}

func ShouldExtractPresentation(eval fileingest.Evaluation) bool {
	if eval.Kind != fileingest.KindPresentation {
		return false
	}
	if isKeynotePresentation(eval) {
		return true
	}
	if strings.EqualFold(eval.ContainerFormat, "pptx") {
		return true
	}
	return strings.EqualFold(eval.NormalizedMIME, "application/vnd.openxmlformats-officedocument.presentationml.presentation")
}

func ProjectExtractionKey(relPath string) string {
	return pptxextract.ProjectExtractionKey(relPath)
}

func ExtractPresentation(ctx context.Context, req PresentationExtractionRequest) map[string]any {
	if !ShouldExtractPresentation(req.Evaluation) {
		return nil
	}
	sourcePath := strings.TrimSpace(req.SourcePath)
	outputDir := strings.TrimSpace(req.OutputDir)
	if sourcePath == "" || outputDir == "" {
		return pptxextract.FailedMetadata(errString("missing source path or output dir"))
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = req.Evaluation.FileName
	}
	if fileName == "" {
		fileName = filepath.Base(sourcePath)
	}
	if isKeynotePresentation(req.Evaluation) {
		result, err := keynoteextract.Extract(ctx, keynoteextract.Request{
			SourcePath:             sourcePath,
			FileName:               fileName,
			OutputDir:              outputDir,
			SourceSHA256:           req.Evaluation.SHA256Hex,
			EnableRendering:        req.EnableExternalTools || req.EnableRendering,
			EnableOCR:              req.EnableExternalTools || req.EnableRendering,
			ExternalCommandTimeout: req.ExternalCommandTimeout,
		})
		if err != nil {
			return keynoteextract.FailedMetadata(err)
		}
		return result.Metadata(req.OutputRelPath)
	}
	result, err := pptxextract.Extract(ctx, pptxextract.Request{
		SourcePath:             sourcePath,
		FileName:               fileName,
		OutputDir:              outputDir,
		SourceSHA256:           req.Evaluation.SHA256Hex,
		IncludeRawXML:          req.IncludeRawXML,
		EnableMarkItDown:       req.EnableExternalTools,
		EnableRendering:        req.EnableExternalTools || req.EnableRendering,
		EnableOCR:              req.EnableExternalTools,
		ExternalCommandTimeout: req.ExternalCommandTimeout,
	})
	if err != nil {
		return pptxextract.FailedMetadata(err)
	}
	return result.Metadata(req.OutputRelPath)
}

type errString string

func (e errString) Error() string { return string(e) }

func isKeynotePresentation(eval fileingest.Evaluation) bool {
	ext := strings.ToLower(strings.TrimSpace(eval.Extension))
	mimeType := strings.ToLower(strings.TrimSpace(eval.NormalizedMIME))
	return ext == "key" ||
		mimeType == "application/vnd.apple.keynote" ||
		mimeType == "application/x-iwork-keynote-sffkey" ||
		strings.Contains(mimeType, "keynote")
}
