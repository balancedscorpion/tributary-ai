package tabularextract

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	Version              = 1
	DefaultPreviewRows   = 50
	DefaultSampleValues  = 8
	DefaultMaxFieldBytes = 4 << 20
)

type Status string

const (
	StatusComplete Status = "complete"
	StatusPartial  Status = "partial"
	StatusFailed   Status = "failed"
)

type Request struct {
	SourcePath    string
	FileName      string
	OutputDir     string
	SourceSHA256  string
	Pipeline      string
	Delimiter     rune
	PreviewRows   int
	SampleValues  int
	MaxFieldBytes int
}

type Result struct {
	Manifest  Manifest
	Table     Table
	OutputDir string
}

type Manifest struct {
	Version               int             `json:"version"`
	Pipeline              string          `json:"pipeline"`
	Status                Status          `json:"status"`
	FileName              string          `json:"fileName"`
	SourceSHA256          string          `json:"sourceSha256,omitempty"`
	CreatedAt             time.Time       `json:"createdAt"`
	Delimiter             string          `json:"delimiter"`
	HasHeader             bool            `json:"hasHeader"`
	RowCount              int             `json:"rowCount"`
	DataRowCount          int             `json:"dataRowCount"`
	ColumnCount           int             `json:"columnCount"`
	RaggedRowCount        int             `json:"raggedRowCount"`
	FormulaRiskCellCount  int             `json:"formulaRiskCellCount"`
	Encoding              string          `json:"encoding"`
	Outputs               map[string]any  `json:"outputs"`
	Capabilities          map[string]bool `json:"capabilities"`
	Warnings              []string        `json:"warnings,omitempty"`
	PreviewTruncated      bool            `json:"previewTruncated"`
	RecommendedNextAction string          `json:"recommendedNextAction,omitempty"`
}

type Table struct {
	Version     int              `json:"version"`
	FileName    string           `json:"fileName"`
	Pipeline    string           `json:"pipeline"`
	Delimiter   string           `json:"delimiter"`
	HasHeader   bool             `json:"hasHeader"`
	RowCount    int              `json:"rowCount"`
	DataRows    int              `json:"dataRowCount"`
	ColumnCount int              `json:"columnCount"`
	Columns     []ColumnSummary  `json:"columns"`
	PreviewRows []map[string]any `json:"previewRows,omitempty"`
	Warnings    []string         `json:"warnings,omitempty"`
	Text        string           `json:"text,omitempty"`
}

type ColumnSummary struct {
	Index         int      `json:"index"`
	Name          string   `json:"name"`
	SourceHeader  string   `json:"sourceHeader,omitempty"`
	InferredType  string   `json:"inferredType"`
	NonEmptyCount int      `json:"nonEmptyCount"`
	NullCount     int      `json:"nullCount"`
	NumericCount  int      `json:"numericCount"`
	BooleanCount  int      `json:"booleanCount"`
	DateCount     int      `json:"dateCount"`
	StringCount   int      `json:"stringCount"`
	SampleValues  []string `json:"sampleValues,omitempty"`
}

type columnProfile struct {
	summary ColumnSummary
	samples map[string]struct{}
}

func Extract(ctx context.Context, req Request) (Result, error) {
	sourcePath := strings.TrimSpace(req.SourcePath)
	if sourcePath == "" {
		return Result{}, errors.New("tabularextract: source path is required")
	}
	outputDir := strings.TrimSpace(req.OutputDir)
	if outputDir == "" {
		return Result{}, errors.New("tabularextract: output dir is required")
	}
	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = filepath.Base(sourcePath)
	}
	pipeline := normalizePipeline(req.Pipeline, fileName, req.Delimiter)
	previewRows := req.PreviewRows
	if previewRows <= 0 {
		previewRows = DefaultPreviewRows
	}
	sampleValues := req.SampleValues
	if sampleValues <= 0 {
		sampleValues = DefaultSampleValues
	}
	maxFieldBytes := req.MaxFieldBytes
	if maxFieldBytes <= 0 {
		maxFieldBytes = DefaultMaxFieldBytes
	}

	if err := os.RemoveAll(outputDir); err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return Result{}, err
	}

	f, err := os.Open(sourcePath)
	if err != nil {
		manifest := failedManifest(fileName, pipeline, req.SourceSHA256, err)
		_ = writeJSON(filepath.Join(outputDir, "manifest.json"), manifest)
		return Result{Manifest: manifest, OutputDir: outputDir}, err
	}
	defer f.Close()

	sample, err := readSample(f, 64<<10)
	if err != nil {
		manifest := failedManifest(fileName, pipeline, req.SourceSHA256, err)
		_ = writeJSON(filepath.Join(outputDir, "manifest.json"), manifest)
		return Result{Manifest: manifest, OutputDir: outputDir}, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Result{}, err
	}

	delimiter := req.Delimiter
	if delimiter == 0 {
		delimiter = delimiterForPipeline(pipeline)
	}
	if delimiter == 0 {
		delimiter = sniffDelimiter(sample)
	}
	reader := csv.NewReader(stripUTF8BOMReader(f))
	reader.Comma = delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.ReuseRecord = false

	rowsPath := filepath.Join(outputDir, "rows-0001.jsonl")
	rowsFile, err := os.Create(rowsPath)
	if err != nil {
		return Result{}, err
	}
	defer rowsFile.Close()
	rowsWriter := bufio.NewWriter(rowsFile)
	defer rowsWriter.Flush()

	var warnings []string
	var headers []string
	var sourceHeaders []string
	var profiles []columnProfile
	var previewRowsData []map[string]any
	var expectedColumns int
	rowCount := 0
	dataRowCount := 0
	raggedRows := 0
	formulaRiskCells := 0

	for {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			default:
			}
		}
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("row %d parse warning: %v", rowCount+1, err))
			if record == nil {
				continue
			}
		}
		rowCount++
		if rowCount == 1 {
			sourceHeaders = append([]string(nil), record...)
			headers = normalizeHeaders(record)
			expectedColumns = len(headers)
			profiles = makeColumnProfiles(headers, sourceHeaders)
			continue
		}

		if len(record) != expectedColumns {
			raggedRows++
			record = normalizeRecordWidth(record, expectedColumns)
		}
		dataRowCount++
		rowObject := map[string]any{}
		for idx, header := range headers {
			value := ""
			if idx < len(record) {
				value = record[idx]
			}
			if len(value) > maxFieldBytes {
				warnings = append(warnings, fmt.Sprintf("row %d column %s exceeded max field bytes and was truncated", rowCount, header))
				value = value[:maxFieldBytes]
			}
			if formulaInjectionRisk(value) {
				formulaRiskCells++
			}
			rowObject[header] = value
			updateColumnProfile(&profiles[idx], value, sampleValues)
		}
		if len(previewRowsData) < previewRows {
			previewRowsData = append(previewRowsData, cloneRow(rowObject))
		}
		raw, err := json.Marshal(rowObject)
		if err != nil {
			return Result{}, err
		}
		if _, err := rowsWriter.Write(raw); err != nil {
			return Result{}, err
		}
		if err := rowsWriter.WriteByte('\n'); err != nil {
			return Result{}, err
		}
	}
	if rowCount == 0 {
		headers = nil
		profiles = nil
	}
	if err := rowsWriter.Flush(); err != nil {
		return Result{}, err
	}

	columns := finalizeProfiles(profiles, dataRowCount)
	table := Table{
		Version:     Version,
		FileName:    fileName,
		Pipeline:    pipeline,
		Delimiter:   delimiterName(delimiter),
		HasHeader:   rowCount > 0,
		RowCount:    rowCount,
		DataRows:    dataRowCount,
		ColumnCount: len(columns),
		Columns:     columns,
		PreviewRows: previewRowsData,
		Warnings:    warnings,
	}
	table.Text = tableMarkdown(table)

	if err := writeJSON(filepath.Join(outputDir, "table.json"), table); err != nil {
		return Result{}, err
	}
	if err := writeFile(filepath.Join(outputDir, "preview.md"), []byte(table.Text)); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "schema.json"), columns); err != nil {
		return Result{}, err
	}

	status := StatusComplete
	if len(warnings) > 0 {
		status = StatusPartial
	}
	manifest := Manifest{
		Version:              Version,
		Pipeline:             pipeline,
		Status:               status,
		FileName:             fileName,
		SourceSHA256:         strings.TrimSpace(req.SourceSHA256),
		CreatedAt:            time.Now().UTC(),
		Delimiter:            delimiterName(delimiter),
		HasHeader:            rowCount > 0,
		RowCount:             rowCount,
		DataRowCount:         dataRowCount,
		ColumnCount:          len(columns),
		RaggedRowCount:       raggedRows,
		FormulaRiskCellCount: formulaRiskCells,
		Encoding:             "utf-8_or_ascii_preserved",
		Outputs: map[string]any{
			"tablePath":   "table.json",
			"rowsPath":    "rows-0001.jsonl",
			"schemaPath":  "schema.json",
			"previewPath": "preview.md",
		},
		Capabilities: map[string]bool{
			"canPreview":            len(previewRowsData) > 0,
			"canSearchRows":         dataRowCount > 0,
			"canPageRows":           dataRowCount > 0,
			"canInferSchema":        len(columns) > 0,
			"formulaInjectionRisk":  formulaRiskCells > 0,
			"hasStructuredRowStore": true,
		},
		Warnings:              warnings,
		PreviewTruncated:      dataRowCount > len(previewRowsData),
		RecommendedNextAction: "Use rows-0001.jsonl for focused row access and schema.json for column planning.",
	}
	if err := writeJSON(filepath.Join(outputDir, "manifest.json"), manifest); err != nil {
		return Result{}, err
	}
	return Result{Manifest: manifest, Table: table, OutputDir: outputDir}, nil
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
		"kind":                   "spreadsheet",
		"pipeline":               r.Manifest.Pipeline,
		"version":                r.Manifest.Version,
		"status":                 string(r.Manifest.Status),
		"rootPath":               rootRelPath,
		"manifestPath":           rel("manifest.json"),
		"dataPath":               rel("table.json"),
		"rowsPath":               rel("rows-0001.jsonl"),
		"schemaPath":             rel("schema.json"),
		"markdownPath":           rel("preview.md"),
		"sheetCount":             1,
		"rowCount":               r.Manifest.RowCount,
		"dataRowCount":           r.Manifest.DataRowCount,
		"columnCount":            r.Manifest.ColumnCount,
		"raggedRowCount":         r.Manifest.RaggedRowCount,
		"formulaRiskCellCount":   r.Manifest.FormulaRiskCellCount,
		"delimiter":              r.Manifest.Delimiter,
		"hasHeader":              r.Manifest.HasHeader,
		"previewTruncated":       r.Manifest.PreviewTruncated,
		"capabilities":           r.Manifest.Capabilities,
		"outputs":                r.Manifest.Outputs,
		"warnings":               r.Manifest.Warnings,
		"cachedValuePolicy":      "not_applicable",
		"hasExternalLinks":       false,
		"hasMacrosOrOle":         false,
		"hasHiddenSheets":        false,
		"hasHiddenRowsOrColumns": false,
	}
	if r.Manifest.SourceSHA256 != "" {
		meta["sourceSha256"] = r.Manifest.SourceSHA256
	}
	return meta
}

func FailedMetadata(pipeline string, err error) map[string]any {
	pipeline = normalizePipeline(pipeline, "", 0)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return map[string]any{
		"kind":     "spreadsheet",
		"pipeline": pipeline,
		"version":  Version,
		"status":   string(StatusFailed),
		"error":    msg,
	}
}

func failedManifest(fileName, pipeline, sha string, err error) Manifest {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return Manifest{
		Version:      Version,
		Pipeline:     pipeline,
		Status:       StatusFailed,
		FileName:     fileName,
		SourceSHA256: strings.TrimSpace(sha),
		CreatedAt:    time.Now().UTC(),
		Outputs:      map[string]any{},
		Warnings:     []string{msg},
	}
}

func normalizePipeline(pipeline, fileName string, delimiter rune) string {
	p := strings.ToLower(strings.TrimSpace(pipeline))
	switch p {
	case "csv", "tsv", "xls":
		return p
	}
	if delimiter == '\t' {
		return "tsv"
	}
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext == ".tsv" || ext == ".tab" {
		return "tsv"
	}
	return "csv"
}

func delimiterForPipeline(pipeline string) rune {
	if strings.EqualFold(pipeline, "tsv") {
		return '\t'
	}
	return 0
}

func delimiterName(delimiter rune) string {
	switch delimiter {
	case '\t':
		return "tab"
	case ',':
		return "comma"
	case ';':
		return "semicolon"
	case '|':
		return "pipe"
	default:
		if delimiter == 0 {
			return ""
		}
		return string(delimiter)
	}
}

func sniffDelimiter(sample []byte) rune {
	candidates := []rune{',', '\t', ';', '|'}
	lines := sampleLines(sample, 20)
	best := ','
	bestScore := -1
	for _, delimiter := range candidates {
		score := delimiterScore(lines, delimiter)
		if score > bestScore {
			bestScore = score
			best = delimiter
		}
	}
	return best
}

func sampleLines(sample []byte, limit int) []string {
	sample = bytes.ReplaceAll(sample, []byte{0xef, 0xbb, 0xbf}, nil)
	raw := strings.Split(string(sample), "\n")
	lines := make([]string, 0, min(len(raw), limit))
	for _, line := range raw {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
		if len(lines) >= limit {
			break
		}
	}
	return lines
}

func delimiterScore(lines []string, delimiter rune) int {
	if len(lines) == 0 {
		return 0
	}
	counts := map[int]int{}
	totalFields := 0
	for _, line := range lines {
		reader := csv.NewReader(strings.NewReader(line))
		reader.Comma = delimiter
		reader.FieldsPerRecord = -1
		reader.LazyQuotes = true
		record, err := reader.Read()
		if err != nil {
			continue
		}
		fields := len(record)
		totalFields += fields
		counts[fields]++
	}
	if totalFields == 0 {
		return 0
	}
	modeCount := 0
	modeFields := 0
	for fields, count := range counts {
		if count > modeCount || (count == modeCount && fields > modeFields) {
			modeCount = count
			modeFields = fields
		}
	}
	return (modeFields * 10) + (modeCount * 4) + totalFields
}

func readSample(f *os.File, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, nil
	}
	limited := io.LimitReader(f, limit)
	return io.ReadAll(limited)
}

func stripUTF8BOMReader(r io.Reader) io.Reader {
	br := bufio.NewReader(r)
	first, err := br.Peek(3)
	if err == nil && bytes.Equal(first, []byte{0xef, 0xbb, 0xbf}) {
		_, _ = br.Discard(3)
	}
	return br
}

func normalizeHeaders(record []string) []string {
	if len(record) == 0 {
		return nil
	}
	out := make([]string, len(record))
	seen := map[string]int{}
	for idx, raw := range record {
		base := sanitizeHeader(raw)
		if base == "" {
			base = fmt.Sprintf("column_%d", idx+1)
		}
		name := base
		if count := seen[base]; count > 0 {
			name = fmt.Sprintf("%s_%d", base, count+1)
		}
		seen[base]++
		out[idx] = name
	}
	return out
}

func sanitizeHeader(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.ToLower(raw) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		case r == '_' || r == '-' || unicode.IsSpace(r):
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}

func makeColumnProfiles(headers []string, sourceHeaders []string) []columnProfile {
	out := make([]columnProfile, len(headers))
	for idx, header := range headers {
		source := ""
		if idx < len(sourceHeaders) {
			source = sourceHeaders[idx]
		}
		out[idx] = columnProfile{
			summary: ColumnSummary{Index: idx + 1, Name: header, SourceHeader: source},
			samples: map[string]struct{}{},
		}
	}
	return out
}

func normalizeRecordWidth(record []string, width int) []string {
	if len(record) == width {
		return record
	}
	out := make([]string, width)
	copy(out, record)
	return out
}

func updateColumnProfile(profile *columnProfile, value string, sampleLimit int) {
	value = strings.TrimSpace(value)
	if value == "" {
		profile.summary.NullCount++
		return
	}
	profile.summary.NonEmptyCount++
	switch {
	case parseBool(value):
		profile.summary.BooleanCount++
	case parseNumber(value):
		profile.summary.NumericCount++
	case parseDate(value):
		profile.summary.DateCount++
	default:
		profile.summary.StringCount++
	}
	if len(profile.samples) < sampleLimit {
		if _, ok := profile.samples[value]; !ok {
			profile.samples[value] = struct{}{}
			profile.summary.SampleValues = append(profile.summary.SampleValues, value)
		}
	}
}

func finalizeProfiles(profiles []columnProfile, dataRows int) []ColumnSummary {
	out := make([]ColumnSummary, len(profiles))
	for idx := range profiles {
		summary := profiles[idx].summary
		if summary.NullCount < 0 {
			summary.NullCount = 0
		}
		if dataRows > summary.NonEmptyCount+summary.NullCount {
			summary.NullCount = dataRows - summary.NonEmptyCount
		}
		summary.InferredType = inferredType(summary)
		sort.Strings(summary.SampleValues)
		out[idx] = summary
	}
	return out
}

func inferredType(summary ColumnSummary) string {
	if summary.NonEmptyCount == 0 {
		return "empty"
	}
	if summary.NumericCount == summary.NonEmptyCount {
		return "number"
	}
	if summary.BooleanCount == summary.NonEmptyCount {
		return "boolean"
	}
	if summary.DateCount == summary.NonEmptyCount {
		return "date"
	}
	if summary.NumericCount+summary.NullCount > summary.StringCount+summary.BooleanCount+summary.DateCount {
		return "mixed_numeric"
	}
	return "string"
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "false", "yes", "no", "y", "n":
		return true
	default:
		return false
	}
}

func parseNumber(value string) bool {
	if strings.TrimSpace(value) == "" {
		return false
	}
	_, err := strconv.ParseFloat(strings.ReplaceAll(value, ",", ""), 64)
	return err == nil
}

func parseDate(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02",
		"02/01/2006",
		"01/02/2006",
		"2006/01/02",
		"02-Jan-2006",
		"Jan 2 2006",
	}
	for _, layout := range layouts {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func cloneRow(row map[string]any) map[string]any {
	out := make(map[string]any, len(row))
	for key, value := range row {
		out[key] = value
	}
	return out
}

func tableMarkdown(table Table) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", table.FileName)
	fmt.Fprintf(&b, "- Pipeline: %s\n", table.Pipeline)
	fmt.Fprintf(&b, "- Rows: %d (%d data rows)\n", table.RowCount, table.DataRows)
	fmt.Fprintf(&b, "- Columns: %d\n", table.ColumnCount)
	if len(table.Warnings) > 0 {
		fmt.Fprintf(&b, "- Warnings: %d\n", len(table.Warnings))
	}
	b.WriteString("\n")
	if len(table.Columns) == 0 || len(table.PreviewRows) == 0 {
		return b.String()
	}
	headers := make([]string, len(table.Columns))
	for idx, column := range table.Columns {
		headers[idx] = column.Name
	}
	b.WriteString("| ")
	for _, header := range headers {
		b.WriteString(escapeMarkdownCell(header))
		b.WriteString(" | ")
	}
	b.WriteString("\n| ")
	for range headers {
		b.WriteString("--- | ")
	}
	b.WriteString("\n")
	for _, row := range table.PreviewRows {
		b.WriteString("| ")
		for _, header := range headers {
			b.WriteString(escapeMarkdownCell(fmt.Sprint(row[header])))
			b.WriteString(" | ")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func formulaInjectionRisk(s string) bool {
	s = strings.TrimLeftFunc(s, unicode.IsSpace)
	if s == "" {
		return false
	}
	switch s[0] {
	case '=', '+', '-', '@':
		return true
	default:
		return false
	}
}

func escapeMarkdownCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
