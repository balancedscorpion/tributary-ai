package filepipeline

import (
	"context"
	"time"

	"ai-analyst-local/internal/chat/fileingest"
)

type ExtractionRequest struct {
	SourcePath             string
	OutputDir              string
	OutputRelPath          string
	FileName               string
	Evaluation             fileingest.Evaluation
	IncludeRawXML          bool
	EnableExternalTools    bool
	EnableRendering        bool
	EnableOCR              bool
	ExternalCommandTimeout time.Duration
}

func ExtractFile(ctx context.Context, req ExtractionRequest) map[string]any {
	if extraction := ExtractPresentation(ctx, PresentationExtractionRequest{
		SourcePath:             req.SourcePath,
		OutputDir:              req.OutputDir,
		OutputRelPath:          req.OutputRelPath,
		FileName:               req.FileName,
		Evaluation:             req.Evaluation,
		IncludeRawXML:          req.IncludeRawXML,
		EnableExternalTools:    req.EnableExternalTools,
		EnableRendering:        req.EnableRendering,
		ExternalCommandTimeout: req.ExternalCommandTimeout,
	}); extraction != nil {
		return extraction
	}
	if extraction := ExtractSpreadsheet(ctx, SpreadsheetExtractionRequest{
		SourcePath:          req.SourcePath,
		OutputDir:           req.OutputDir,
		OutputRelPath:       req.OutputRelPath,
		FileName:            req.FileName,
		Evaluation:          req.Evaluation,
		IncludeRawXML:       req.IncludeRawXML,
		EnableExternalTools: req.EnableExternalTools,
		EnableRendering:     req.EnableRendering,
	}); extraction != nil {
		return extraction
	}
	if extraction := ExtractPDF(PDFExtractionRequest{
		SourcePath:    req.SourcePath,
		OutputDir:     req.OutputDir,
		OutputRelPath: req.OutputRelPath,
		FileName:      req.FileName,
		Evaluation:    req.Evaluation,
	}); extraction != nil {
		return extraction
	}
	if extraction := ExtractDocument(ctx, DocumentExtractionRequest{
		SourcePath:    req.SourcePath,
		OutputDir:     req.OutputDir,
		OutputRelPath: req.OutputRelPath,
		FileName:      req.FileName,
		Evaluation:    req.Evaluation,
		IncludeRawXML: req.IncludeRawXML,
	}); extraction != nil {
		return extraction
	}
	if extraction := ExtractImage(ImageExtractionRequest{
		SourcePath:    req.SourcePath,
		OutputDir:     req.OutputDir,
		OutputRelPath: req.OutputRelPath,
		FileName:      req.FileName,
		Evaluation:    req.Evaluation,
		EnableOCR:     req.EnableOCR,
	}); extraction != nil {
		return extraction
	}
	if extraction := ExtractArchive(ArchiveExtractionRequest{
		SourcePath:    req.SourcePath,
		OutputDir:     req.OutputDir,
		OutputRelPath: req.OutputRelPath,
		FileName:      req.FileName,
		Evaluation:    req.Evaluation,
	}); extraction != nil {
		return extraction
	}
	if extraction := ExtractText(ctx, TextExtractionRequest{
		SourcePath:    req.SourcePath,
		OutputDir:     req.OutputDir,
		OutputRelPath: req.OutputRelPath,
		FileName:      req.FileName,
		Evaluation:    req.Evaluation,
	}); extraction != nil {
		return extraction
	}
	return nil
}
