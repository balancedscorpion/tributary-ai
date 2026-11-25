package filepipeline

import (
	"context"
	"path/filepath"
	"strings"

	"ai-analyst-local/internal/chat/documentextract"
	"ai-analyst-local/internal/chat/fileingest"
)

type DocumentExtractionRequest struct {
	SourcePath    string
	OutputDir     string
	OutputRelPath string
	FileName      string
	Evaluation    fileingest.Evaluation
	IncludeRawXML bool
}

func ShouldExtractDocument(eval fileingest.Evaluation) bool {
	if eval.Kind != fileingest.KindDocument {
		return false
	}
	return documentPipeline(eval) != ""
}

func ExtractDocument(ctx context.Context, req DocumentExtractionRequest) map[string]any {
	if !ShouldExtractDocument(req.Evaluation) {
		return nil
	}
	sourcePath := strings.TrimSpace(req.SourcePath)
	outputDir := strings.TrimSpace(req.OutputDir)
	pipeline := documentPipeline(req.Evaluation)
	if sourcePath == "" || outputDir == "" {
		return documentextract.FailedMetadata(pipeline, errString("missing source path or output dir"))
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = req.Evaluation.FileName
	}
	if fileName == "" {
		fileName = filepath.Base(sourcePath)
	}
	result, err := documentextract.Extract(ctx, documentextract.Request{
		SourcePath:    sourcePath,
		FileName:      fileName,
		OutputDir:     outputDir,
		SourceSHA256:  req.Evaluation.SHA256Hex,
		Pipeline:      pipeline,
		IncludeRawXML: req.IncludeRawXML,
	})
	if err != nil {
		return documentextract.FailedMetadata(pipeline, err)
	}
	return result.Metadata(req.OutputRelPath)
}

func documentPipeline(eval fileingest.Evaluation) string {
	ext := strings.ToLower(strings.TrimSpace(eval.Extension))
	mimeType := strings.ToLower(strings.TrimSpace(eval.NormalizedMIME))
	switch {
	case strings.EqualFold(eval.ContainerFormat, "docx"),
		ext == "docx",
		mimeType == "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return "docx"
	case ext == "rtf", mimeType == "application/rtf", mimeType == "text/rtf":
		return "rtf"
	case ext == "doc", mimeType == "application/msword":
		return "doc"
	default:
		return ""
	}
}
