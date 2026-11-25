package filepipeline

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/fileingest"
	"ai-analyst-local/internal/chat/spreadsheetextract"
	"ai-analyst-local/internal/chat/tabularextract"
)

const legacyExcelConversionTimeout = 90 * time.Second

type SpreadsheetExtractionRequest struct {
	SourcePath          string
	OutputDir           string
	OutputRelPath       string
	FileName            string
	Evaluation          fileingest.Evaluation
	IncludeRawXML       bool
	EnableExternalTools bool
	EnableRendering     bool
}

func ShouldExtractSpreadsheet(eval fileingest.Evaluation) bool {
	switch eval.Kind {
	case fileingest.KindSpreadsheet, fileingest.KindCSV:
		return true
	default:
		return isDelimitedSpreadsheet(eval)
	}
}

func ExtractSpreadsheet(ctx context.Context, req SpreadsheetExtractionRequest) map[string]any {
	if !ShouldExtractSpreadsheet(req.Evaluation) {
		return nil
	}
	sourcePath := strings.TrimSpace(req.SourcePath)
	outputDir := strings.TrimSpace(req.OutputDir)
	if sourcePath == "" || outputDir == "" {
		return tabularextract.FailedMetadata(spreadsheetPipeline(req.Evaluation), errString("missing source path or output dir"))
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = req.Evaluation.FileName
	}
	if fileName == "" {
		fileName = filepath.Base(sourcePath)
	}

	if isXLSXSpreadsheet(req.Evaluation) {
		result, err := spreadsheetextract.Extract(ctx, spreadsheetextract.Request{
			SourcePath:    sourcePath,
			FileName:      fileName,
			OutputDir:     outputDir,
			SourceSHA256:  req.Evaluation.SHA256Hex,
			IncludeRawXML: req.IncludeRawXML,
		})
		if err != nil {
			return spreadsheetextract.FailedMetadata(err)
		}
		return result.Metadata(req.OutputRelPath)
	}
	if isLegacyExcel(req.Evaluation) {
		if req.EnableExternalTools || req.EnableRendering {
			return extractLegacyExcel(ctx, req, sourcePath, outputDir, fileName)
		}
		return tabularextract.FailedMetadata("xls", errString("legacy .xls binary workbook extraction requires LibreOffice conversion, but rendering/external processing is disabled"))
	}

	pipeline := spreadsheetPipeline(req.Evaluation)
	delimiter := rune(0)
	if pipeline == "tsv" {
		delimiter = '\t'
	}
	result, err := tabularextract.Extract(ctx, tabularextract.Request{
		SourcePath:   sourcePath,
		FileName:     fileName,
		OutputDir:    outputDir,
		SourceSHA256: req.Evaluation.SHA256Hex,
		Pipeline:     pipeline,
		Delimiter:    delimiter,
	})
	if err != nil {
		return tabularextract.FailedMetadata(pipeline, err)
	}
	return result.Metadata(req.OutputRelPath)
}

func extractLegacyExcel(ctx context.Context, req SpreadsheetExtractionRequest, sourcePath string, outputDir string, fileName string) map[string]any {
	tmpDir, err := os.MkdirTemp(filepath.Dir(outputDir), "xls-convert-*")
	if err != nil {
		return tabularextract.FailedMetadata("xls", err)
	}
	defer os.RemoveAll(tmpDir)

	convertedPath, err := convertLegacyExcelToXLSX(ctx, sourcePath, tmpDir)
	if err != nil {
		return tabularextract.FailedMetadata("xls", err)
	}
	result, err := spreadsheetextract.Extract(ctx, spreadsheetextract.Request{
		SourcePath:    convertedPath,
		FileName:      strings.TrimSuffix(fileName, filepath.Ext(fileName)) + ".xlsx",
		OutputDir:     outputDir,
		SourceSHA256:  req.Evaluation.SHA256Hex,
		IncludeRawXML: req.IncludeRawXML,
	})
	if err != nil {
		return spreadsheetextract.FailedMetadata(err)
	}
	meta := result.Metadata(req.OutputRelPath)
	meta["pipeline"] = "xls"
	meta["convertedPipeline"] = "xlsx"
	meta["converted_pipeline"] = "xlsx"
	meta["conversion"] = map[string]any{
		"from": "xls",
		"to":   "xlsx",
		"tool": "libreoffice",
	}
	return meta
}

func convertLegacyExcelToXLSX(ctx context.Context, sourcePath string, outputDir string) (string, error) {
	soffice, ok := findSpreadsheetExecutable("soffice", "libreoffice")
	if !ok {
		return "", errString("legacy .xls conversion requires LibreOffice/soffice, but it was not found")
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return "", err
	}
	runCtx, cancel := context.WithTimeout(ctxOrBackground(ctx), legacyExcelConversionTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, soffice, "--headless", "--convert-to", "xlsx", "--outdir", outputDir, sourcePath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("legacy .xls conversion failed: %s", trimSpreadsheetCommandOutput(err, out))
	}
	matches, _ := filepath.Glob(filepath.Join(outputDir, "*.xlsx"))
	sort.Strings(matches)
	if len(matches) == 0 {
		return "", errString("legacy .xls conversion failed: LibreOffice did not produce an .xlsx workbook")
	}
	stablePath := filepath.Join(outputDir, "converted.xlsx")
	if matches[0] != stablePath {
		raw, err := os.ReadFile(matches[0])
		if err != nil {
			return "", err
		}
		if err := os.WriteFile(stablePath, raw, 0o600); err != nil {
			return "", err
		}
	}
	return stablePath, nil
}

func isXLSXSpreadsheet(eval fileingest.Evaluation) bool {
	if strings.EqualFold(eval.ContainerFormat, "xlsx") {
		return true
	}
	if strings.EqualFold(eval.Extension, "xlsx") {
		return true
	}
	return strings.EqualFold(eval.NormalizedMIME, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
}

func isLegacyExcel(eval fileingest.Evaluation) bool {
	if strings.EqualFold(eval.Extension, "xls") {
		return true
	}
	return strings.EqualFold(eval.NormalizedMIME, "application/vnd.ms-excel")
}

func isDelimitedSpreadsheet(eval fileingest.Evaluation) bool {
	ext := strings.ToLower(strings.TrimSpace(eval.Extension))
	mimeType := strings.ToLower(strings.TrimSpace(eval.NormalizedMIME))
	return ext == "tsv" ||
		ext == "tab" ||
		mimeType == "text/tab-separated-values" ||
		mimeType == "text/csv" ||
		mimeType == "application/csv"
}

func spreadsheetPipeline(eval fileingest.Evaluation) string {
	ext := strings.ToLower(strings.TrimSpace(eval.Extension))
	mimeType := strings.ToLower(strings.TrimSpace(eval.NormalizedMIME))
	switch {
	case ext == "tsv", ext == "tab", mimeType == "text/tab-separated-values":
		return "tsv"
	case isXLSXSpreadsheet(eval):
		return "xlsx"
	case isLegacyExcel(eval):
		return "xls"
	default:
		return "csv"
	}
}

func findSpreadsheetExecutable(names ...string) (string, bool) {
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil && p != "" {
			return p, true
		}
	}
	return "", false
}

func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func trimSpreadsheetCommandOutput(err error, output []byte) string {
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
