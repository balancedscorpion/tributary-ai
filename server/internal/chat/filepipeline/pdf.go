package filepipeline

import (
	"path/filepath"
	"strings"

	"ai-analyst-local/internal/chat/fileingest"
	"ai-analyst-local/internal/chat/pdfextract"
)

type PDFExtractionRequest struct {
	SourcePath    string
	OutputDir     string
	OutputRelPath string
	FileName      string
	Evaluation    fileingest.Evaluation
}

func ShouldExtractPDF(eval fileingest.Evaluation) bool {
	return eval.Kind == fileingest.KindPDF || strings.EqualFold(eval.NormalizedMIME, "application/pdf")
}

func ExtractPDF(req PDFExtractionRequest) map[string]any {
	if !ShouldExtractPDF(req.Evaluation) {
		return nil
	}
	sourcePath := strings.TrimSpace(req.SourcePath)
	outputDir := strings.TrimSpace(req.OutputDir)
	if sourcePath == "" || outputDir == "" {
		return pdfextract.FailedMetadata(errString("missing source path or output dir"))
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = req.Evaluation.FileName
	}
	if fileName == "" {
		fileName = filepath.Base(sourcePath)
	}
	result, err := pdfextract.Extract(pdfextract.Request{
		SourcePath:   sourcePath,
		FileName:     fileName,
		OutputDir:    outputDir,
		SourceSHA256: req.Evaluation.SHA256Hex,
	})
	if err != nil {
		return pdfextract.FailedMetadata(err)
	}
	return result.Metadata(req.OutputRelPath)
}
