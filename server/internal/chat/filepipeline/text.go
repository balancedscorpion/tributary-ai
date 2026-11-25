package filepipeline

import (
	"context"
	"path/filepath"
	"strings"

	"ai-analyst-local/internal/chat/fileingest"
	"ai-analyst-local/internal/chat/textextract"
)

type TextExtractionRequest struct {
	SourcePath    string
	OutputDir     string
	OutputRelPath string
	FileName      string
	Evaluation    fileingest.Evaluation
}

func ShouldExtractText(eval fileingest.Evaluation) bool {
	switch eval.Kind {
	case fileingest.KindText, fileingest.KindJSON, fileingest.KindHTML:
		return true
	default:
		return false
	}
}

func ExtractText(ctx context.Context, req TextExtractionRequest) map[string]any {
	if !ShouldExtractText(req.Evaluation) {
		return nil
	}
	sourcePath := strings.TrimSpace(req.SourcePath)
	outputDir := strings.TrimSpace(req.OutputDir)
	pipeline := textPipeline(req.Evaluation)
	if sourcePath == "" || outputDir == "" {
		return textextract.FailedMetadata(pipeline, errString("missing source path or output dir"))
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = req.Evaluation.FileName
	}
	if fileName == "" {
		fileName = filepath.Base(sourcePath)
	}
	result, err := textextract.Extract(ctx, textextract.Request{
		SourcePath:   sourcePath,
		FileName:     fileName,
		OutputDir:    outputDir,
		SourceSHA256: req.Evaluation.SHA256Hex,
		Pipeline:     pipeline,
	})
	if err != nil {
		return textextract.FailedMetadata(pipeline, err)
	}
	return result.Metadata(req.OutputRelPath)
}

func textPipeline(eval fileingest.Evaluation) string {
	ext := strings.ToLower(strings.TrimSpace(eval.Extension))
	mimeType := strings.ToLower(strings.TrimSpace(eval.NormalizedMIME))
	switch {
	case eval.Kind == fileingest.KindJSON && ext == "jsonl":
		return "jsonl"
	case eval.Kind == fileingest.KindJSON:
		return "json"
	case eval.Kind == fileingest.KindHTML:
		return "html"
	case ext == "md", ext == "markdown", mimeType == "text/markdown":
		return "markdown"
	case ext == "xml", ext == "svg", strings.Contains(mimeType, "xml"):
		return "xml"
	default:
		return "text"
	}
}
