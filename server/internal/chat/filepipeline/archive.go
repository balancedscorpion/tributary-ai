package filepipeline

import (
	"path/filepath"
	"strings"

	"ai-analyst-local/internal/chat/archiveextract"
	"ai-analyst-local/internal/chat/fileingest"
)

type ArchiveExtractionRequest struct {
	SourcePath    string
	OutputDir     string
	OutputRelPath string
	FileName      string
	Evaluation    fileingest.Evaluation
}

func ShouldExtractArchive(eval fileingest.Evaluation) bool {
	return eval.Kind == fileingest.KindArchive && archivePipeline(eval) != ""
}

func ExtractArchive(req ArchiveExtractionRequest) map[string]any {
	if !ShouldExtractArchive(req.Evaluation) {
		return nil
	}
	sourcePath := strings.TrimSpace(req.SourcePath)
	outputDir := strings.TrimSpace(req.OutputDir)
	pipeline := archivePipeline(req.Evaluation)
	if sourcePath == "" || outputDir == "" {
		return archiveextract.FailedMetadata(pipeline, errString("missing source path or output dir"))
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = req.Evaluation.FileName
	}
	if fileName == "" {
		fileName = filepath.Base(sourcePath)
	}
	result, err := archiveextract.Extract(archiveextract.Request{
		SourcePath:   sourcePath,
		FileName:     fileName,
		OutputDir:    outputDir,
		SourceSHA256: req.Evaluation.SHA256Hex,
		Pipeline:     pipeline,
	})
	if err != nil {
		return archiveextract.FailedMetadata(pipeline, err)
	}
	return result.Metadata(req.OutputRelPath)
}

func archivePipeline(eval fileingest.Evaluation) string {
	ext := strings.ToLower(strings.TrimSpace(eval.Extension))
	switch ext {
	case "zip":
		return "zip"
	case "tar":
		return "tar"
	case "tgz":
		return "tgz"
	case "gz", "gzip":
		return "gz"
	default:
		mimeType := strings.ToLower(strings.TrimSpace(eval.NormalizedMIME))
		switch {
		case mimeType == "application/zip":
			return "zip"
		case strings.Contains(mimeType, "x-tar"):
			return "tar"
		case strings.Contains(mimeType, "gzip"):
			return "gz"
		default:
			return ""
		}
	}
}
