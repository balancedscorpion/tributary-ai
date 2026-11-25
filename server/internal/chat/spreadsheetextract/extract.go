package spreadsheetextract

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	Version                    = 1
	DefaultMaxEntries          = 6000
	DefaultMaxUncompressedSize = 768 << 20
	DefaultCellChunkSize       = 5000
	DefaultPreviewRows         = 50
)

type Status string

const (
	StatusComplete Status = "complete"
	StatusPartial  Status = "partial"
	StatusFailed   Status = "failed"
)

type StageStatus string

const (
	StageCompleted StageStatus = "completed"
	StageSkipped   StageStatus = "skipped"
	StageFailed    StageStatus = "failed"
)

type Request struct {
	SourcePath          string
	FileName            string
	OutputDir           string
	SourceSHA256        string
	IncludeRawXML       bool
	MaxEntries          int
	MaxUncompressedSize int64
	CellChunkSize       int
	PreviewRows         int
}

type Result struct {
	Manifest  Manifest
	Workbook  Workbook
	OutputDir string
}

type Manifest struct {
	Version             int             `json:"version"`
	Pipeline            string          `json:"pipeline"`
	Status              Status          `json:"status"`
	FileName            string          `json:"fileName"`
	SourceSHA256        string          `json:"sourceSha256,omitempty"`
	CreatedAt           time.Time       `json:"createdAt"`
	SheetCount          int             `json:"sheetCount"`
	RowCount            int             `json:"rowCount"`
	ColumnCount         int             `json:"columnCount"`
	TableCount          int             `json:"tableCount"`
	FormulaCount        int             `json:"formulaCount"`
	NamedRangeCount     int             `json:"namedRangeCount"`
	PivotCount          int             `json:"pivotCount"`
	ChartCount          int             `json:"chartCount"`
	CommentCount        int             `json:"commentCount"`
	MergedRangeCount    int             `json:"mergedRangeCount"`
	MediaCount          int             `json:"mediaCount"`
	HasHiddenSheets     bool            `json:"hasHiddenSheets"`
	HasHiddenRowsOrCols bool            `json:"hasHiddenRowsOrColumns"`
	HasExternalLinks    bool            `json:"hasExternalLinks"`
	HasMacrosOrOle      bool            `json:"hasMacrosOrOle"`
	DateSystem          string          `json:"dateSystem"`
	CalculationMode     string          `json:"calculationMode,omitempty"`
	CachedValuePolicy   string          `json:"cachedValuePolicy"`
	Outputs             map[string]any  `json:"outputs"`
	Capabilities        map[string]bool `json:"capabilities"`
	Warnings            []string        `json:"warnings,omitempty"`
	Stages              []Stage         `json:"stages"`
}

type Stage struct {
	Name    string      `json:"name"`
	Status  StageStatus `json:"status"`
	Detail  string      `json:"detail,omitempty"`
	Outputs []string    `json:"outputs,omitempty"`
	Error   string      `json:"error,omitempty"`
}

type Workbook struct {
	Version         int             `json:"version"`
	FileName        string          `json:"fileName"`
	DateSystem      string          `json:"dateSystem"`
	CalculationMode string          `json:"calculationMode,omitempty"`
	Sheets          []SheetSummary  `json:"sheets"`
	DefinedNames    []DefinedName   `json:"definedNames,omitempty"`
	ExternalLinks   []ExternalLink  `json:"externalLinks,omitempty"`
	Security        SecuritySummary `json:"security"`
	Text            string          `json:"text,omitempty"`
}

type WorkbookGraph struct {
	Version       int               `json:"version"`
	FileName      string            `json:"fileName"`
	WorkbookPath  string            `json:"workbookPath"`
	Relationships []RelationshipRef `json:"relationships,omitempty"`
	Sheets        []SheetGraph      `json:"sheets"`
}

type SheetGraph struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Index          int               `json:"index"`
	State          string            `json:"state,omitempty"`
	RelationshipID string            `json:"relationshipId,omitempty"`
	XMLPath        string            `json:"xmlPath"`
	Relationships  []RelationshipRef `json:"relationships,omitempty"`
}

type SheetSummary struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Index               int      `json:"index"`
	SheetID             string   `json:"sheetId,omitempty"`
	RelationshipID      string   `json:"relationshipId,omitempty"`
	State               string   `json:"state,omitempty"`
	XMLPath             string   `json:"xmlPath"`
	Dimension           string   `json:"dimension,omitempty"`
	RowCount            int      `json:"rowCount"`
	ColumnCount         int      `json:"columnCount"`
	CellCount           int      `json:"cellCount"`
	FormulaCount        int      `json:"formulaCount"`
	CommentCount        int      `json:"commentCount"`
	MergedRangeCount    int      `json:"mergedRangeCount"`
	TableIDs            []string `json:"tableIds,omitempty"`
	ChartIDs            []string `json:"chartIds,omitempty"`
	HasHiddenRowsOrCols bool     `json:"hasHiddenRowsOrColumns,omitempty"`
	CellsPath           string   `json:"cellsPath,omitempty"`
	PreviewPath         string   `json:"previewPath,omitempty"`
	PreviewJSONPath     string   `json:"previewJsonPath,omitempty"`
	MergedCellsPath     string   `json:"mergedCellsPath,omitempty"`
	CommentsPath        string   `json:"commentsPath,omitempty"`
}

type CellStylePreview struct {
	Bold            bool   `json:"bold,omitempty"`
	Italic          bool   `json:"italic,omitempty"`
	FontColor       string `json:"fontColor,omitempty"`
	FillColor       string `json:"fillColor,omitempty"`
	HorizontalAlign string `json:"horizontalAlign,omitempty"`
	VerticalAlign   string `json:"verticalAlign,omitempty"`
	WrapText        bool   `json:"wrapText,omitempty"`
}

type CellRecord struct {
	SheetID      string            `json:"sheet_id"`
	Address      string            `json:"address"`
	Row          int               `json:"row"`
	Col          int               `json:"col"`
	Kind         string            `json:"kind"`
	Raw          string            `json:"raw,omitempty"`
	Display      string            `json:"display,omitempty"`
	Formula      string            `json:"formula,omitempty"`
	CachedValue  string            `json:"cached_value,omitempty"`
	CacheStatus  string            `json:"cache_status,omitempty"`
	StyleID      int               `json:"style_id,omitempty"`
	NumberFormat string            `json:"number_format,omitempty"`
	Style        *CellStylePreview `json:"style,omitempty"`
	MergedParent string            `json:"merged_parent,omitempty"`
	Hidden       bool              `json:"hidden,omitempty"`
	Comment      string            `json:"comment,omitempty"`
	Hyperlink    string            `json:"hyperlink,omitempty"`
}

type TableSummary struct {
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	SheetID     string   `json:"sheetId"`
	XMLPath     string   `json:"xmlPath"`
	Ref         string   `json:"ref,omitempty"`
	HeaderNames []string `json:"headerNames,omitempty"`
	RowCount    int      `json:"rowCount"`
	ColumnCount int      `json:"columnCount"`
	RowsPath    string   `json:"rowsPath,omitempty"`
}

type DefinedName struct {
	Name         string `json:"name"`
	Text         string `json:"text"`
	LocalSheetID string `json:"localSheetId,omitempty"`
	Hidden       bool   `json:"hidden,omitempty"`
}

type ExternalLink struct {
	ID      string `json:"id,omitempty"`
	XMLPath string `json:"xmlPath,omitempty"`
	Target  string `json:"target,omitempty"`
	Type    string `json:"type,omitempty"`
}

type ChartSummary struct {
	ID      string `json:"id"`
	SheetID string `json:"sheetId,omitempty"`
	XMLPath string `json:"xmlPath"`
	Title   string `json:"title,omitempty"`
	Type    string `json:"type,omitempty"`
}

type MediaSummary struct {
	ID          string `json:"id"`
	SheetID     string `json:"sheetId,omitempty"`
	XMLPath     string `json:"xmlPath"`
	ContentType string `json:"contentType,omitempty"`
	ByteSize    int64  `json:"byteSize,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	Quarantined bool   `json:"quarantined"`
}

type Comment struct {
	SheetID string `json:"sheetId"`
	Address string `json:"address,omitempty"`
	Text    string `json:"text"`
	Author  string `json:"author,omitempty"`
	XMLPath string `json:"xmlPath,omitempty"`
}

type SecuritySummary struct {
	EncryptedPackage    bool     `json:"encryptedPackage"`
	HasMacros           bool     `json:"hasMacros"`
	HasOleObjects       bool     `json:"hasOleObjects"`
	HasExternalLinks    bool     `json:"hasExternalLinks"`
	HasRemoteHyperlinks bool     `json:"hasRemoteHyperlinks"`
	SuspiciousParts     []string `json:"suspiciousParts,omitempty"`
	Warnings            []string `json:"warnings,omitempty"`
}

type RelationshipRef struct {
	ID         string `json:"id"`
	Type       string `json:"type,omitempty"`
	Target     string `json:"target,omitempty"`
	TargetMode string `json:"targetMode,omitempty"`
	Resolved   string `json:"resolved,omitempty"`
}

type packageReader struct {
	reader       *zip.ReadCloser
	files        map[string]*zip.File
	contentTypes contentTypeIndex
}

type contentTypeIndex struct {
	defaults  map[string]string
	overrides map[string]string
}

type relationshipXML struct {
	ID         string `xml:"Id,attr"`
	Type       string `xml:"Type,attr"`
	Target     string `xml:"Target,attr"`
	TargetMode string `xml:"TargetMode,attr"`
}

type relationshipFile struct {
	Relationships []relationshipXML `xml:"Relationship"`
}

type workbookSheet struct {
	Name           string
	SheetID        string
	State          string
	RelationshipID string
	XMLPath        string
}

type workbookInfo struct {
	DateSystem      string
	CalculationMode string
	Sheets          []workbookSheet
	DefinedNames    []DefinedName
}

type styleIndex struct {
	CellFormats   map[int]string           `json:"cellFormats"`
	CellStyles    map[int]CellStylePreview `json:"cellStyles,omitempty"`
	CustomFormats map[string]string        `json:"customFormats"`
}

type styleSheetXML struct {
	NumFmts struct {
		Items []styleNumFmtXML `xml:"numFmt"`
	} `xml:"numFmts"`
	Fonts struct {
		Items []styleFontXML `xml:"font"`
	} `xml:"fonts"`
	Fills struct {
		Items []styleFillXML `xml:"fill"`
	} `xml:"fills"`
	CellXfs struct {
		Items []styleXFXML `xml:"xf"`
	} `xml:"cellXfs"`
}

type styleNumFmtXML struct {
	NumFmtID   string `xml:"numFmtId,attr"`
	FormatCode string `xml:"formatCode,attr"`
}

type styleFontXML struct {
	Bold   *struct{}      `xml:"b"`
	Italic *struct{}      `xml:"i"`
	Color  *styleColorXML `xml:"color"`
}

type styleFillXML struct {
	PatternFill *stylePatternFillXML `xml:"patternFill"`
}

type stylePatternFillXML struct {
	PatternType string         `xml:"patternType,attr"`
	FgColor     *styleColorXML `xml:"fgColor"`
	BgColor     *styleColorXML `xml:"bgColor"`
}

type styleXFXML struct {
	NumFmtID  string             `xml:"numFmtId,attr"`
	FontID    string             `xml:"fontId,attr"`
	FillID    string             `xml:"fillId,attr"`
	Alignment *styleAlignmentXML `xml:"alignment"`
}

type styleAlignmentXML struct {
	Horizontal string `xml:"horizontal,attr"`
	Vertical   string `xml:"vertical,attr"`
	WrapText   string `xml:"wrapText,attr"`
}

type styleColorXML struct {
	RGB     string `xml:"rgb,attr"`
	Theme   string `xml:"theme,attr"`
	Indexed string `xml:"indexed,attr"`
	Tint    string `xml:"tint,attr"`
	Auto    string `xml:"auto,attr"`
}

type SheetPreview struct {
	Version      int                  `json:"version"`
	Sheet        SheetSummary         `json:"sheet"`
	Metadata     SheetPreviewMetadata `json:"metadata"`
	Columns      []SheetPreviewColumn `json:"columns"`
	Rows         []SheetPreviewRow    `json:"rows"`
	HeaderRows   []int                `json:"headerRows,omitempty"`
	MergedRanges []string             `json:"mergedRanges,omitempty"`
}

type SheetPreviewMetadata struct {
	PreviewRowCount    int            `json:"previewRowCount"`
	PreviewColumnCount int            `json:"previewColumnCount"`
	TotalRowCount      int            `json:"totalRowCount"`
	TotalColumnCount   int            `json:"totalColumnCount"`
	TotalCellCount     int            `json:"totalCellCount"`
	FormulaCount       int            `json:"formulaCount"`
	CommentCount       int            `json:"commentCount"`
	MergedRangeCount   int            `json:"mergedRangeCount"`
	HiddenRowsOrCols   bool           `json:"hiddenRowsOrColumns,omitempty"`
	Truncated          bool           `json:"truncated,omitempty"`
	Tables             []TableSummary `json:"tables,omitempty"`
	Charts             []ChartSummary `json:"charts,omitempty"`
}

type SheetPreviewColumn struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
}

type SheetPreviewRow struct {
	Index int                `json:"index"`
	Cells []SheetPreviewCell `json:"cells"`
}

type SheetPreviewCell struct {
	Address      string            `json:"address"`
	Row          int               `json:"row"`
	Col          int               `json:"col"`
	Kind         string            `json:"kind"`
	Display      string            `json:"display,omitempty"`
	Formula      string            `json:"formula,omitempty"`
	Hyperlink    string            `json:"hyperlink,omitempty"`
	Comment      string            `json:"comment,omitempty"`
	NumberFormat string            `json:"numberFormat,omitempty"`
	MergedParent string            `json:"mergedParent,omitempty"`
	Header       bool              `json:"header,omitempty"`
	Hidden       bool              `json:"hidden,omitempty"`
	Style        *CellStylePreview `json:"style,omitempty"`
}

type sheetExtraction struct {
	Summary       SheetSummary
	Cells         []CellRecord
	Formulas      []CellRecord
	Comments      []Comment
	MergedRanges  []string
	Tables        []TableSummary
	Charts        []ChartSummary
	Media         []MediaSummary
	Relationships []RelationshipRef
	Warnings      []string
}

func Extract(ctx context.Context, req Request) (Result, error) {
	sourcePath := strings.TrimSpace(req.SourcePath)
	if sourcePath == "" {
		return Result{}, errors.New("spreadsheetextract: source path is required")
	}
	outputDir := strings.TrimSpace(req.OutputDir)
	if outputDir == "" {
		return Result{}, errors.New("spreadsheetextract: output dir is required")
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
	builder := &manifestBuilder{manifest: Manifest{
		Version:           Version,
		Pipeline:          "xlsx",
		Status:            StatusComplete,
		FileName:          fileName,
		SourceSHA256:      strings.TrimSpace(req.SourceSHA256),
		CreatedAt:         time.Now().UTC(),
		CachedValuePolicy: "preserved_not_recalculated",
		Outputs:           map[string]any{},
		Capabilities:      map[string]bool{},
	}}
	pkg, err := openPackage(sourcePath, req.MaxEntries, req.MaxUncompressedSize)
	if err != nil {
		builder.stage(Stage{Name: "intake_validation", Status: StageFailed, Error: err.Error()})
		manifest := builder.finishFailed()
		_ = writeJSON(filepath.Join(outputDir, "manifest.json"), manifest)
		return Result{Manifest: manifest, OutputDir: outputDir}, err
	}
	defer pkg.close()
	builder.stage(Stage{Name: "intake_validation", Status: StageCompleted, Detail: "Valid XLSX OOXML package"})

	security := pkg.securitySummary()
	workbook, workbookPath, err := pkg.parseWorkbook(fileName)
	if err != nil {
		builder.stage(Stage{Name: "workbook_graph", Status: StageFailed, Error: err.Error()})
		manifest := builder.finishFailed()
		_ = writeJSON(filepath.Join(outputDir, "manifest.json"), manifest)
		return Result{Manifest: manifest, OutputDir: outputDir}, err
	}
	workbook.Security = security
	builder.stage(Stage{Name: "workbook_graph", Status: StageCompleted, Detail: fmt.Sprintf("Found %d workbook sheets", len(workbook.Sheets))})

	sharedStrings, sharedSummary, err := pkg.parseSharedStrings()
	if err != nil {
		builder.warn("shared strings could not be fully parsed: " + err.Error())
		builder.stage(Stage{Name: "shared_strings", Status: StageFailed, Error: err.Error()})
	} else {
		builder.stage(Stage{Name: "shared_strings", Status: StageCompleted, Detail: fmt.Sprintf("Loaded %d shared strings", len(sharedStrings))})
	}
	styles := pkg.parseStyles()
	if err := writeJSON(filepath.Join(outputDir, "styles.json"), styles); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "shared_strings.summary.json"), sharedSummary); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "defined_names.json"), workbook.DefinedNames); err != nil {
		return Result{}, err
	}
	builder.output("stylesPath", "styles.json")
	builder.output("sharedStringsSummaryPath", "shared_strings.summary.json")
	builder.output("definedNamesPath", "defined_names.json")

	graph := WorkbookGraph{Version: Version, FileName: fileName, WorkbookPath: workbookPath}
	if rels, err := pkg.relationshipsFor(workbookPath); err == nil {
		graph.Relationships = relationshipRefs(rels, workbookPath)
	}
	var allFormulas []CellRecord
	var allTables []TableSummary
	var allCharts []ChartSummary
	var allMedia []MediaSummary
	var sheetOutputs []string
	var sheetPreviewOutputs []string
	for i := range workbook.Sheets {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return Result{}, ctx.Err()
			default:
			}
		}
		sheet := workbook.Sheets[i]
		extracted, err := pkg.extractSheet(sheet, sharedStrings, styles, req)
		if err != nil {
			builder.warn(fmt.Sprintf("sheet %s could not be fully parsed: %v", sheet.Name, err))
			builder.stage(Stage{Name: "worksheet_" + sheet.ID, Status: StageFailed, Error: err.Error()})
			continue
		}
		workbook.Sheets[i] = extracted.Summary
		graph.Sheets = append(graph.Sheets, SheetGraph{
			ID:             extracted.Summary.ID,
			Name:           extracted.Summary.Name,
			Index:          extracted.Summary.Index,
			State:          extracted.Summary.State,
			RelationshipID: sheet.RelationshipID,
			XMLPath:        extracted.Summary.XMLPath,
			Relationships:  extracted.Relationships,
		})
		if err := writeSheetOutputs(outputDir, extracted, req); err != nil {
			return Result{}, err
		}
		sheetOutputs = append(sheetOutputs, extracted.Summary.PreviewPath, extracted.Summary.CellsPath)
		sheetPreviewOutputs = append(sheetPreviewOutputs, extracted.Summary.PreviewJSONPath)
		allFormulas = append(allFormulas, extracted.Formulas...)
		allTables = append(allTables, extracted.Tables...)
		allCharts = append(allCharts, extracted.Charts...)
		allMedia = append(allMedia, extracted.Media...)
		builder.stage(Stage{Name: "worksheet_" + sheet.ID, Status: StageCompleted, Detail: fmt.Sprintf("Extracted %d cells", extracted.Summary.CellCount)})
		for _, warning := range extracted.Warnings {
			builder.warn(warning)
		}
	}

	if err := writeTables(outputDir, allTables); err != nil {
		return Result{}, err
	}
	if err := writeJSONLines(filepath.Join(outputDir, "formulas", "formulas.jsonl"), cellsToAny(allFormulas)); err != nil {
		return Result{}, err
	}
	if err := writeCharts(outputDir, allCharts); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "media", "media_manifest.json"), map[string]any{"media": allMedia}); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "security.json"), workbook.Security); err != nil {
		return Result{}, err
	}
	externalLinks := pkg.externalLinks()
	workbook.ExternalLinks = externalLinks
	if len(externalLinks) > 0 {
		workbook.Security.HasExternalLinks = true
	}
	if err := writeJSON(filepath.Join(outputDir, "external_links.json"), externalLinks); err != nil {
		return Result{}, err
	}
	workbook.Text = workbookMarkdown(workbook, allTables, allCharts)
	if err := writeFile(filepath.Join(outputDir, "index", "workbook.md"), []byte(workbook.Text)); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "workbook.json"), workbook); err != nil {
		return Result{}, err
	}
	if err := writeJSON(filepath.Join(outputDir, "workbook_graph.json"), graph); err != nil {
		return Result{}, err
	}
	if req.IncludeRawXML {
		rawOutputs, err := pkg.writeRawXML(filepath.Join(outputDir, "raw", "ooxml"))
		if err != nil {
			builder.stage(Stage{Name: "raw_ooxml", Status: StageFailed, Error: err.Error()})
		} else {
			builder.stage(Stage{Name: "raw_ooxml", Status: StageCompleted, Outputs: rawOutputs})
			builder.output("rawOOXMLRoot", "raw/ooxml")
		}
	} else {
		builder.stage(Stage{Name: "raw_ooxml", Status: StageSkipped, Detail: "Raw OOXML copy disabled"})
	}

	manifest := builder.manifest
	manifest.DateSystem = workbook.DateSystem
	manifest.CalculationMode = workbook.CalculationMode
	manifest.SheetCount = len(workbook.Sheets)
	manifest.NamedRangeCount = len(workbook.DefinedNames)
	manifest.HasExternalLinks = len(externalLinks) > 0 || workbook.Security.HasExternalLinks
	manifest.HasMacrosOrOle = workbook.Security.HasMacros || workbook.Security.HasOleObjects
	for _, sheet := range workbook.Sheets {
		if sheet.State == "hidden" || sheet.State == "veryHidden" {
			manifest.HasHiddenSheets = true
		}
		manifest.RowCount += sheet.RowCount
		if sheet.ColumnCount > manifest.ColumnCount {
			manifest.ColumnCount = sheet.ColumnCount
		}
		manifest.FormulaCount += sheet.FormulaCount
		manifest.CommentCount += sheet.CommentCount
		manifest.MergedRangeCount += sheet.MergedRangeCount
		if sheet.HasHiddenRowsOrCols {
			manifest.HasHiddenRowsOrCols = true
		}
	}
	manifest.TableCount = len(allTables)
	manifest.ChartCount = len(allCharts)
	manifest.MediaCount = len(allMedia)
	manifest.Outputs = builder.manifest.Outputs
	manifest.Outputs["manifestPath"] = "manifest.json"
	manifest.Outputs["workbookPath"] = "workbook.json"
	manifest.Outputs["graphPath"] = "workbook_graph.json"
	manifest.Outputs["markdownPath"] = "index/workbook.md"
	manifest.Outputs["dataPath"] = "workbook.json"
	manifest.Outputs["sheetOutputs"] = compactStrings(sheetOutputs)
	manifest.Outputs["sheetPreviewOutputs"] = compactStrings(sheetPreviewOutputs)
	manifest.Outputs["formulasPath"] = "formulas/formulas.jsonl"
	manifest.Outputs["securityPath"] = "security.json"
	manifest.Outputs["externalLinksPath"] = "external_links.json"
	manifest.Capabilities = map[string]bool{
		"canReadSheets":      len(workbook.Sheets) > 0,
		"canReadTables":      len(allTables) > 0,
		"canSearchWorkbook":  true,
		"canInspectFormulas": manifest.FormulaCount > 0,
		"canInspectCharts":   len(allCharts) > 0,
	}
	manifest.Stages = builder.manifest.Stages
	manifest.Warnings = builder.manifest.Warnings
	if manifest.Status == "" {
		manifest.Status = StatusComplete
	}
	if err := writeJSON(filepath.Join(outputDir, "manifest.json"), manifest); err != nil {
		return Result{}, err
	}
	return Result{Manifest: manifest, Workbook: workbook, OutputDir: outputDir}, nil
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
		"pipeline":               "xlsx",
		"version":                r.Manifest.Version,
		"status":                 string(r.Manifest.Status),
		"rootPath":               rootRelPath,
		"manifestPath":           rel("manifest.json"),
		"workbookPath":           rel("workbook.json"),
		"graphPath":              rel("workbook_graph.json"),
		"markdownPath":           rel("index/workbook.md"),
		"dataPath":               rel("workbook.json"),
		"sheetCount":             r.Manifest.SheetCount,
		"rowCount":               r.Manifest.RowCount,
		"columnCount":            r.Manifest.ColumnCount,
		"tableCount":             r.Manifest.TableCount,
		"formulaCount":           r.Manifest.FormulaCount,
		"namedRangeCount":        r.Manifest.NamedRangeCount,
		"pivotCount":             r.Manifest.PivotCount,
		"chartCount":             r.Manifest.ChartCount,
		"commentCount":           r.Manifest.CommentCount,
		"mergedRangeCount":       r.Manifest.MergedRangeCount,
		"mediaCount":             r.Manifest.MediaCount,
		"hasHiddenSheets":        r.Manifest.HasHiddenSheets,
		"hasHiddenRowsOrColumns": r.Manifest.HasHiddenRowsOrCols,
		"hasExternalLinks":       r.Manifest.HasExternalLinks,
		"hasMacrosOrOle":         r.Manifest.HasMacrosOrOle,
		"dateSystem":             r.Manifest.DateSystem,
		"calculationMode":        r.Manifest.CalculationMode,
		"cachedValuePolicy":      r.Manifest.CachedValuePolicy,
		"capabilities":           r.Manifest.Capabilities,
		"outputs":                r.Manifest.Outputs,
		"warnings":               r.Manifest.Warnings,
		"stages":                 r.Manifest.Stages,
	}
	if sheetOutputs, ok := r.Manifest.Outputs["sheetOutputs"].([]string); ok && len(sheetOutputs) > 0 {
		out := make([]string, 0, len(sheetOutputs))
		for _, sheetOutput := range sheetOutputs {
			if relPath := rel(sheetOutput); relPath != "" {
				out = append(out, relPath)
			}
		}
		if len(out) > 0 {
			meta["sheetOutputs"] = out
		}
	} else if raw, ok := r.Manifest.Outputs["sheetOutputs"].([]any); ok && len(raw) > 0 {
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if s, ok := item.(string); ok {
				if relPath := rel(s); relPath != "" {
					out = append(out, relPath)
				}
			}
		}
		if len(out) > 0 {
			meta["sheetOutputs"] = out
		}
	}
	if sheetPreviewOutputs, ok := r.Manifest.Outputs["sheetPreviewOutputs"].([]string); ok && len(sheetPreviewOutputs) > 0 {
		out := make([]string, 0, len(sheetPreviewOutputs))
		for _, sheetOutput := range sheetPreviewOutputs {
			if relPath := rel(sheetOutput); relPath != "" {
				out = append(out, relPath)
			}
		}
		if len(out) > 0 {
			meta["sheetPreviewOutputs"] = out
		}
	} else if raw, ok := r.Manifest.Outputs["sheetPreviewOutputs"].([]any); ok && len(raw) > 0 {
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if s, ok := item.(string); ok {
				if relPath := rel(s); relPath != "" {
					out = append(out, relPath)
				}
			}
		}
		if len(out) > 0 {
			meta["sheetPreviewOutputs"] = out
		}
	}
	if r.Manifest.SourceSHA256 != "" {
		meta["sourceSha256"] = r.Manifest.SourceSHA256
	}
	return meta
}

func FailedMetadata(err error) map[string]any {
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return map[string]any{
		"kind":     "spreadsheet",
		"pipeline": "xlsx",
		"version":  Version,
		"status":   string(StatusFailed),
		"error":    msg,
	}
}

func openPackage(sourcePath string, maxEntries int, maxUncompressedSize int64) (*packageReader, error) {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	if maxUncompressedSize <= 0 {
		maxUncompressedSize = DefaultMaxUncompressedSize
	}
	zr, err := zip.OpenReader(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open xlsx zip: %w", err)
	}
	p := &packageReader{reader: zr, files: map[string]*zip.File{}}
	var total uint64
	for _, f := range zr.File {
		clean, ok := cleanZipName(f.Name)
		if !ok {
			_ = zr.Close()
			return nil, fmt.Errorf("unsafe xlsx entry path %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		total += f.UncompressedSize64
		if len(p.files)+1 > maxEntries {
			_ = zr.Close()
			return nil, fmt.Errorf("xlsx has too many entries: %d > %d", len(p.files)+1, maxEntries)
		}
		if total > uint64(maxUncompressedSize) {
			_ = zr.Close()
			return nil, fmt.Errorf("xlsx uncompressed size exceeds limit: %d > %d", total, maxUncompressedSize)
		}
		if _, exists := p.files[clean]; !exists {
			p.files[clean] = f
		}
	}
	if !p.has("[Content_Types].xml") {
		_ = zr.Close()
		return nil, errors.New("xlsx missing [Content_Types].xml")
	}
	if !p.has("xl/workbook.xml") {
		_ = zr.Close()
		return nil, errors.New("xlsx missing xl/workbook.xml")
	}
	ct, err := p.parseContentTypes()
	if err != nil {
		_ = zr.Close()
		return nil, err
	}
	p.contentTypes = ct
	return p, nil
}

func (p *packageReader) close() {
	if p != nil && p.reader != nil {
		_ = p.reader.Close()
	}
}

func (p *packageReader) has(name string) bool {
	_, ok := p.files[strings.Trim(strings.TrimSpace(filepath.ToSlash(name)), "/")]
	return ok
}

func (p *packageReader) read(name string) ([]byte, error) {
	clean, ok := cleanZipName(name)
	if !ok {
		return nil, fmt.Errorf("unsafe xlsx entry path %q", name)
	}
	f, ok := p.files[clean]
	if !ok {
		return nil, os.ErrNotExist
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func cleanZipName(name string) (string, bool) {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || strings.ContainsRune(name, '\x00') || strings.HasPrefix(name, "/") {
		return "", false
	}
	if unescaped, err := url.PathUnescape(name); err == nil {
		name = unescaped
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, ":") {
		return "", false
	}
	return clean, true
}

func (p *packageReader) parseContentTypes() (contentTypeIndex, error) {
	raw, err := p.read("[Content_Types].xml")
	if err != nil {
		return contentTypeIndex{}, err
	}
	type defaultXML struct {
		Extension   string `xml:"Extension,attr"`
		ContentType string `xml:"ContentType,attr"`
	}
	type overrideXML struct {
		PartName    string `xml:"PartName,attr"`
		ContentType string `xml:"ContentType,attr"`
	}
	type typesXML struct {
		Defaults  []defaultXML  `xml:"Default"`
		Overrides []overrideXML `xml:"Override"`
	}
	var parsed typesXML
	if err := xml.Unmarshal(raw, &parsed); err != nil {
		return contentTypeIndex{}, fmt.Errorf("parse [Content_Types].xml: %w", err)
	}
	out := contentTypeIndex{defaults: map[string]string{}, overrides: map[string]string{}}
	for _, d := range parsed.Defaults {
		ext := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d.Extension), "."))
		if ext != "" && strings.TrimSpace(d.ContentType) != "" {
			out.defaults[ext] = strings.TrimSpace(d.ContentType)
		}
	}
	for _, o := range parsed.Overrides {
		part := strings.Trim(strings.TrimSpace(o.PartName), "/")
		if part != "" && strings.TrimSpace(o.ContentType) != "" {
			out.overrides[part] = strings.TrimSpace(o.ContentType)
		}
	}
	return out, nil
}

func (c contentTypeIndex) contentType(part string) string {
	part = strings.Trim(strings.TrimSpace(filepath.ToSlash(part)), "/")
	if ct := c.overrides[part]; ct != "" {
		return ct
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(part), "."))
	return c.defaults[ext]
}

func (p *packageReader) relationshipsFor(part string) (map[string]relationshipXML, error) {
	raw, err := p.read(relationshipsPath(part))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]relationshipXML{}, nil
	}
	if err != nil {
		return nil, err
	}
	var parsed relationshipFile
	if err := xml.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	out := make(map[string]relationshipXML, len(parsed.Relationships))
	for _, rel := range parsed.Relationships {
		if strings.TrimSpace(rel.ID) != "" {
			out[rel.ID] = rel
		}
	}
	return out, nil
}

func relationshipsPath(part string) string {
	part = strings.Trim(strings.TrimSpace(filepath.ToSlash(part)), "/")
	dir, base := path.Split(part)
	return path.Join(dir, "_rels", base+".rels")
}

func resolveRelationshipTarget(sourcePart string, target string) string {
	target = strings.TrimSpace(strings.ReplaceAll(target, "\\", "/"))
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "/") {
		return strings.Trim(path.Clean(target), "/")
	}
	base := path.Dir(strings.Trim(strings.TrimSpace(filepath.ToSlash(sourcePart)), "/"))
	return strings.Trim(path.Clean(path.Join(base, target)), "/")
}

func relationshipRefs(rels map[string]relationshipXML, sourcePart string) []RelationshipRef {
	ids := make([]string, 0, len(rels))
	for id := range rels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]RelationshipRef, 0, len(ids))
	for _, id := range ids {
		rel := rels[id]
		out = append(out, RelationshipRef{
			ID: rel.ID, Type: rel.Type, Target: rel.Target, TargetMode: rel.TargetMode, Resolved: resolveRelationshipTarget(sourcePart, rel.Target),
		})
	}
	return out
}

func (p *packageReader) parseWorkbook(fileName string) (Workbook, string, error) {
	workbookPath := "xl/workbook.xml"
	raw, err := p.read(workbookPath)
	if err != nil {
		return Workbook{}, "", err
	}
	rels, err := p.relationshipsFor(workbookPath)
	if err != nil {
		return Workbook{}, "", err
	}
	info, err := parseWorkbookInfo(raw, rels, workbookPath)
	if err != nil {
		return Workbook{}, "", err
	}
	wb := Workbook{
		Version:         Version,
		FileName:        fileName,
		DateSystem:      info.DateSystem,
		CalculationMode: info.CalculationMode,
		DefinedNames:    info.DefinedNames,
	}
	for idx, sh := range info.Sheets {
		id := fmt.Sprintf("sheet%d", idx+1)
		state := sh.State
		if state == "" {
			state = "visible"
		}
		wb.Sheets = append(wb.Sheets, SheetSummary{
			ID: id, Name: sh.Name, Index: idx + 1, SheetID: sh.SheetID, State: state, XMLPath: sh.XMLPath,
		})
	}
	return wb, workbookPath, nil
}

func parseWorkbookInfo(raw []byte, rels map[string]relationshipXML, workbookPath string) (workbookInfo, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	info := workbookInfo{DateSystem: "1900", CalculationMode: "auto"}
	var inDefinedNames bool
	var currentDefined *DefinedName
	var definedText strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return info, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "workbookPr":
				if attrValue(t, "date1904") == "1" || strings.EqualFold(attrValue(t, "date1904"), "true") {
					info.DateSystem = "1904"
				}
			case "calcPr":
				info.CalculationMode = firstNonEmpty(attrValue(t, "calcMode"), "auto")
			case "sheet":
				rid := ""
				for _, attr := range t.Attr {
					if attr.Name.Local == "id" && attr.Name.Space != "" {
						rid = attr.Value
					}
				}
				rel := rels[rid]
				info.Sheets = append(info.Sheets, workbookSheet{
					Name: attrValue(t, "name"), SheetID: attrValue(t, "sheetId"), State: attrValue(t, "state"),
					RelationshipID: rid, XMLPath: resolveRelationshipTarget(workbookPath, rel.Target),
				})
			case "definedNames":
				inDefinedNames = true
			case "definedName":
				if inDefinedNames {
					currentDefined = &DefinedName{Name: attrValue(t, "name"), LocalSheetID: attrValue(t, "localSheetId")}
					currentDefined.Hidden = attrValue(t, "hidden") == "1" || strings.EqualFold(attrValue(t, "hidden"), "true")
					definedText.Reset()
				}
			}
		case xml.CharData:
			if currentDefined != nil {
				definedText.WriteString(string(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "definedNames":
				inDefinedNames = false
			case "definedName":
				if currentDefined != nil {
					currentDefined.Text = strings.TrimSpace(definedText.String())
					if currentDefined.Name != "" || currentDefined.Text != "" {
						info.DefinedNames = append(info.DefinedNames, *currentDefined)
					}
				}
				currentDefined = nil
			}
		}
	}
	return info, nil
}

func (p *packageReader) parseSharedStrings() ([]string, map[string]any, error) {
	raw, err := p.read("xl/sharedStrings.xml")
	if errors.Is(err, os.ErrNotExist) {
		return nil, map[string]any{"count": 0, "uniqueCount": 0, "path": ""}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var stringsOut []string
	var inSI bool
	var inT bool
	var current strings.Builder
	count := ""
	uniqueCount := ""
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return stringsOut, nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "sst":
				count = attrValue(t, "count")
				uniqueCount = attrValue(t, "uniqueCount")
			case "si":
				inSI = true
				current.Reset()
			case "t":
				if inSI {
					inT = true
				}
			}
		case xml.CharData:
			if inT {
				current.WriteString(string(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inT = false
			case "si":
				stringsOut = append(stringsOut, current.String())
				inSI = false
			}
		}
	}
	sample := stringsOut
	if len(sample) > 20 {
		sample = sample[:20]
	}
	return stringsOut, map[string]any{
		"path": "xl/sharedStrings.xml", "count": parseInt(count), "uniqueCount": parseInt(uniqueCount), "loadedCount": len(stringsOut), "sample": sample,
	}, nil
}

func (p *packageReader) parseStyles() styleIndex {
	raw, err := p.read("xl/styles.xml")
	if err != nil {
		return styleIndex{CellFormats: map[int]string{}, CellStyles: map[int]CellStylePreview{}, CustomFormats: map[string]string{}}
	}

	var sheet styleSheetXML
	if err := xml.Unmarshal(raw, &sheet); err != nil {
		return styleIndex{CellFormats: map[int]string{}, CellStyles: map[int]CellStylePreview{}, CustomFormats: map[string]string{}}
	}

	custom := map[string]string{}
	for _, item := range sheet.NumFmts.Items {
		id := strings.TrimSpace(item.NumFmtID)
		code := strings.TrimSpace(item.FormatCode)
		if id != "" && code != "" {
			custom[id] = code
		}
	}

	themeColors := p.parseThemeColors()
	fonts := make([]CellStylePreview, len(sheet.Fonts.Items))
	for i, font := range sheet.Fonts.Items {
		style := CellStylePreview{
			Bold:   font.Bold != nil,
			Italic: font.Italic != nil,
		}
		if color := colorCSS(font.Color, themeColors); color != "" && color != "#000000" {
			style.FontColor = color
		}
		fonts[i] = style
	}

	fills := make([]string, len(sheet.Fills.Items))
	for i, fill := range sheet.Fills.Items {
		if fill.PatternFill == nil {
			continue
		}
		pattern := strings.TrimSpace(strings.ToLower(fill.PatternFill.PatternType))
		if pattern != "" && pattern != "solid" {
			continue
		}
		color := colorCSS(fill.PatternFill.FgColor, themeColors)
		if color == "" {
			color = colorCSS(fill.PatternFill.BgColor, themeColors)
		}
		if color != "" && color != "#ffffff" {
			fills[i] = color
		}
	}

	cellFormats := map[int]string{}
	cellStyles := map[int]CellStylePreview{}
	for styleID, xf := range sheet.CellXfs.Items {
		numFmtID := strings.TrimSpace(xf.NumFmtID)
		cellFormats[styleID] = firstNonEmpty(custom[numFmtID], builtinNumFmt(numFmtID), numFmtID)
		style := CellStylePreview{}
		if fontID := parseInt(xf.FontID); fontID >= 0 && fontID < len(fonts) {
			style = mergeCellStylePreview(style, fonts[fontID])
		}
		if fillID := parseInt(xf.FillID); fillID >= 0 && fillID < len(fills) && fills[fillID] != "" {
			style.FillColor = fills[fillID]
		}
		if xf.Alignment != nil {
			style.HorizontalAlign = cleanSpreadsheetAlign(xf.Alignment.Horizontal)
			style.VerticalAlign = cleanSpreadsheetAlign(xf.Alignment.Vertical)
			style.WrapText = boolAttr(xf.Alignment.WrapText)
		}
		if !style.empty() {
			cellStyles[styleID] = style
		}
	}
	return styleIndex{CellFormats: cellFormats, CellStyles: cellStyles, CustomFormats: custom}
}

func (p *packageReader) parseThemeColors() map[string]string {
	raw, err := p.read("xl/theme/theme1.xml")
	if err != nil {
		return defaultThemeColors()
	}
	colors := defaultThemeColors()
	slots := map[string]string{
		"lt1": "0", "dk1": "1", "lt2": "2", "dk2": "3",
		"accent1": "4", "accent2": "5", "accent3": "6", "accent4": "7", "accent5": "8", "accent6": "9",
		"hlink": "10", "folHlink": "11",
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	current := ""
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if slot, ok := slots[t.Name.Local]; ok {
				current = slot
				continue
			}
			if current == "" {
				continue
			}
			switch t.Name.Local {
			case "srgbClr":
				if color := normalizeHexColor(attrValue(t, "val")); color != "" {
					colors[current] = color
				}
			case "sysClr":
				if color := normalizeHexColor(attrValue(t, "lastClr")); color != "" {
					colors[current] = color
				}
			}
		case xml.EndElement:
			if _, ok := slots[t.Name.Local]; ok {
				current = ""
			}
		}
	}
	return colors
}

func defaultThemeColors() map[string]string {
	return map[string]string{
		"0": "#ffffff", "1": "#000000", "2": "#eeece1", "3": "#1f497d",
		"4": "#4f81bd", "5": "#c0504d", "6": "#9bbb59", "7": "#8064a2", "8": "#4bacc6", "9": "#f79646",
		"10": "#0000ff", "11": "#800080",
	}
}

func mergeCellStylePreview(base CellStylePreview, next CellStylePreview) CellStylePreview {
	if next.Bold {
		base.Bold = true
	}
	if next.Italic {
		base.Italic = true
	}
	if next.FontColor != "" {
		base.FontColor = next.FontColor
	}
	if next.FillColor != "" {
		base.FillColor = next.FillColor
	}
	if next.HorizontalAlign != "" {
		base.HorizontalAlign = next.HorizontalAlign
	}
	if next.VerticalAlign != "" {
		base.VerticalAlign = next.VerticalAlign
	}
	if next.WrapText {
		base.WrapText = true
	}
	return base
}

func (s CellStylePreview) empty() bool {
	return !s.Bold && !s.Italic && s.FontColor == "" && s.FillColor == "" && s.HorizontalAlign == "" && s.VerticalAlign == "" && !s.WrapText
}

func cleanSpreadsheetAlign(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "left", "center", "right", "justify", "distributed":
		return strings.TrimSpace(strings.ToLower(value))
	case "top", "middle", "bottom":
		return strings.TrimSpace(strings.ToLower(value))
	default:
		return ""
	}
}

func boolAttr(value string) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	return value == "1" || value == "true"
}

func colorCSS(color *styleColorXML, themeColors map[string]string) string {
	if color == nil {
		return ""
	}
	if boolAttr(color.Auto) {
		return ""
	}
	if rgb := normalizeHexColor(color.RGB); rgb != "" {
		return applyTint(rgb, color.Tint)
	}
	if theme := strings.TrimSpace(color.Theme); theme != "" {
		if base := themeColors[theme]; base != "" {
			return applyTint(base, color.Tint)
		}
	}
	if indexed := strings.TrimSpace(color.Indexed); indexed != "" {
		return applyTint(indexedColor(indexed), color.Tint)
	}
	return ""
}

func normalizeHexColor(raw string) string {
	clean := strings.TrimPrefix(strings.TrimSpace(raw), "#")
	if len(clean) == 8 {
		clean = clean[2:]
	}
	if len(clean) != 6 {
		return ""
	}
	for _, r := range clean {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return ""
		}
	}
	return "#" + strings.ToLower(clean)
}

func indexedColor(index string) string {
	switch strings.TrimSpace(index) {
	case "0", "8":
		return "#000000"
	case "1", "9":
		return "#ffffff"
	case "2", "10":
		return "#ff0000"
	case "3", "11":
		return "#00ff00"
	case "4", "12":
		return "#0000ff"
	case "5", "13":
		return "#ffff00"
	case "6", "14":
		return "#ff00ff"
	case "7", "15":
		return "#00ffff"
	case "16":
		return "#800000"
	case "17":
		return "#008000"
	case "18":
		return "#000080"
	case "19":
		return "#808000"
	case "20":
		return "#800080"
	case "21":
		return "#008080"
	case "22":
		return "#c0c0c0"
	case "23":
		return "#808080"
	default:
		return ""
	}
}

func applyTint(hexColor string, rawTint string) string {
	hexColor = normalizeHexColor(hexColor)
	if hexColor == "" {
		return ""
	}
	tint, err := strconv.ParseFloat(strings.TrimSpace(rawTint), 64)
	if err != nil || tint == 0 {
		return hexColor
	}
	r := tintedChannel(hexColor[1:3], tint)
	g := tintedChannel(hexColor[3:5], tint)
	b := tintedChannel(hexColor[5:7], tint)
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

func tintedChannel(hex string, tint float64) int {
	v64, err := strconv.ParseInt(hex, 16, 0)
	if err != nil {
		return 0
	}
	v := float64(v64)
	if tint < 0 {
		v = v * (1 + tint)
	} else {
		v = v*(1-tint) + 255*tint
	}
	out := int(v + 0.5)
	if out < 0 {
		return 0
	}
	if out > 255 {
		return 255
	}
	return out
}

func builtinNumFmt(id string) string {
	switch strings.TrimSpace(id) {
	case "0":
		return "General"
	case "1":
		return "0"
	case "2":
		return "0.00"
	case "9":
		return "0%"
	case "10":
		return "0.00%"
	case "14":
		return "m/d/yy"
	case "15":
		return "d-mmm-yy"
	case "16":
		return "d-mmm"
	case "17":
		return "mmm-yy"
	case "22":
		return "m/d/yy h:mm"
	default:
		return ""
	}
}

func (p *packageReader) extractSheet(summary SheetSummary, sharedStrings []string, styles styleIndex, req Request) (sheetExtraction, error) {
	raw, err := p.read(summary.XMLPath)
	if err != nil {
		return sheetExtraction{}, err
	}
	sheetID := summary.ID
	mergedRanges, mergedParents := parseMergedCells(raw)
	comments := p.extractSheetComments(sheetID, summary.XMLPath)
	commentsByCell := map[string]string{}
	for _, comment := range comments {
		if comment.Address != "" {
			commentsByCell[comment.Address] = comment.Text
		}
	}
	rels, _ := p.relationshipsFor(summary.XMLPath)
	tables := p.extractSheetTables(sheetID, summary.XMLPath, rels)
	charts, media := p.extractSheetDrawingInventory(sheetID, summary.XMLPath, rels)
	hyperlinks := parseSheetHyperlinks(raw, rels, summary.XMLPath)
	hiddenCols := parseHiddenCols(raw)
	rows, cols, dimension := parseDimension(raw)
	extracted := sheetExtraction{
		Summary:       summary,
		Comments:      comments,
		MergedRanges:  mergedRanges,
		Tables:        tables,
		Charts:        charts,
		Media:         media,
		Relationships: relationshipRefs(rels, summary.XMLPath),
	}
	extracted.Summary.Dimension = dimension
	extracted.Summary.RowCount = rows
	extracted.Summary.ColumnCount = cols
	extracted.Summary.MergedRangeCount = len(mergedRanges)
	extracted.Summary.CommentCount = len(comments)
	for _, table := range tables {
		extracted.Summary.TableIDs = append(extracted.Summary.TableIDs, table.ID)
	}
	for _, chart := range charts {
		extracted.Summary.ChartIDs = append(extracted.Summary.ChartIDs, chart.ID)
	}
	var cells []CellRecord
	dec := xml.NewDecoder(bytes.NewReader(raw))
	currentRow := 0
	currentRowHidden := false
	var current *CellRecord
	var capture string
	var buf strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return extracted, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				currentRow = parseInt(attrValue(t, "r"))
				currentRowHidden = attrValue(t, "hidden") == "1" || strings.EqualFold(attrValue(t, "hidden"), "true")
				if currentRowHidden {
					extracted.Summary.HasHiddenRowsOrCols = true
				}
			case "c":
				addr := attrValue(t, "r")
				row, col := splitCellRef(addr)
				if row == 0 {
					row = currentRow
				}
				styleID := parseInt(attrValue(t, "s"))
				hidden := currentRowHidden || hiddenCols[col]
				if hidden {
					extracted.Summary.HasHiddenRowsOrCols = true
				}
				var stylePreview *CellStylePreview
				if style, ok := styles.CellStyles[styleID]; ok && !style.empty() {
					copied := style
					stylePreview = &copied
				}
				current = &CellRecord{
					SheetID: sheetID, Address: addr, Row: row, Col: col, Kind: "blank", StyleID: styleID,
					NumberFormat: styles.CellFormats[styleID], Style: stylePreview, MergedParent: mergedParents[addr], Hidden: hidden,
					Comment: commentsByCell[addr], Hyperlink: hyperlinks[addr],
				}
				current.Raw = attrValue(t, "t")
			case "f", "v", "t":
				if current != nil {
					capture = t.Name.Local
					buf.Reset()
				}
			}
		case xml.CharData:
			if current != nil && capture != "" {
				buf.WriteString(string(t))
			}
		case xml.EndElement:
			if current != nil {
				switch t.Name.Local {
				case "f":
					current.Formula = strings.TrimSpace(buf.String())
					current.CacheStatus = formulaCacheStatus(current.Formula, current.CachedValue)
					capture = ""
				case "v":
					current.CachedValue = strings.TrimSpace(buf.String())
					current.Display = displayValue(*current, sharedStrings)
					current.Kind = cellKind(*current)
					if current.Formula != "" {
						current.CacheStatus = formulaCacheStatus(current.Formula, current.CachedValue)
					}
					capture = ""
				case "t":
					if current.Raw == "inlineStr" || current.Display == "" {
						current.Display = strings.TrimSpace(buf.String())
						current.Kind = "string"
					}
					capture = ""
				case "c":
					if current.Display == "" && current.CachedValue == "" && current.Formula == "" {
						current.Kind = "blank"
					}
					if current.Formula != "" {
						extracted.Summary.FormulaCount++
						extracted.Formulas = append(extracted.Formulas, *current)
					}
					cells = append(cells, *current)
					current = nil
				}
			}
		}
	}
	extracted.Cells = cells
	extracted.Summary.CellCount = len(cells)
	if extracted.Summary.RowCount == 0 || extracted.Summary.ColumnCount == 0 {
		extracted.Summary.RowCount, extracted.Summary.ColumnCount = inferDimensions(cells)
	}
	extracted.Summary.CellsPath = path.Join("worksheets", sheetID, "cells-0001.jsonl")
	extracted.Summary.PreviewPath = path.Join("worksheets", sheetID, "preview.md")
	extracted.Summary.PreviewJSONPath = path.Join("worksheets", sheetID, "preview.json")
	extracted.Summary.MergedCellsPath = path.Join("worksheets", sheetID, "merged_cells.json")
	if len(comments) > 0 {
		extracted.Summary.CommentsPath = path.Join("worksheets", sheetID, "comments.jsonl")
	}
	for i := range extracted.Tables {
		extracted.Tables[i].RowCount, extracted.Tables[i].ColumnCount = tableDimensionsFromRef(extracted.Tables[i].Ref)
	}
	return extracted, nil
}

func displayValue(cell CellRecord, sharedStrings []string) string {
	switch cell.Raw {
	case "s":
		idx := parseInt(cell.CachedValue)
		if idx >= 0 && idx < len(sharedStrings) {
			return sharedStrings[idx]
		}
		return cell.CachedValue
	case "b":
		if cell.CachedValue == "1" {
			return "TRUE"
		}
		return "FALSE"
	case "str", "inlineStr":
		return cell.CachedValue
	default:
		return cell.CachedValue
	}
}

func cellKind(cell CellRecord) string {
	if cell.Formula != "" && cell.CachedValue == "" {
		return "blank"
	}
	switch cell.Raw {
	case "s", "str", "inlineStr":
		return "string"
	case "b":
		return "bool"
	case "e":
		return "error"
	default:
		if strings.TrimSpace(cell.CachedValue) == "" {
			return "blank"
		}
		if _, err := strconv.ParseFloat(cell.CachedValue, 64); err == nil {
			return "number"
		}
		return "string"
	}
}

func formulaCacheStatus(formula string, cached string) string {
	if strings.TrimSpace(formula) == "" {
		return ""
	}
	upper := strings.ToUpper(formula)
	if strings.Contains(upper, "[") || strings.Contains(upper, "WEBSERVICE(") || strings.Contains(upper, "HYPERLINK(") {
		return "external_dependency"
	}
	if strings.TrimSpace(cached) == "" {
		return "missing"
	}
	if strings.Contains(upper, "NOW(") || strings.Contains(upper, "TODAY(") || strings.Contains(upper, "RAND(") || strings.Contains(upper, "RANDBETWEEN(") {
		return "stale_possible"
	}
	return "trusted"
}

func writeSheetOutputs(outputDir string, extracted sheetExtraction, req Request) error {
	root := filepath.Join(outputDir, "worksheets", extracted.Summary.ID)
	if err := writeJSON(filepath.Join(root, "sheet.json"), extracted.Summary); err != nil {
		return err
	}
	if err := writeJSONLines(filepath.Join(root, "cells-0001.jsonl"), cellsToAny(extracted.Cells)); err != nil {
		return err
	}
	if err := writeFile(filepath.Join(root, "preview.md"), []byte(sheetPreviewMarkdown(extracted, req.PreviewRows))); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(root, "preview.json"), sheetPreviewData(extracted, req.PreviewRows)); err != nil {
		return err
	}
	merged := map[string]any{"ranges": append([]string(nil), extracted.MergedRanges...)}
	if err := writeJSON(filepath.Join(root, "merged_cells.json"), merged); err != nil {
		return err
	}
	if len(extracted.Comments) > 0 {
		values := make([]any, 0, len(extracted.Comments))
		for _, c := range extracted.Comments {
			values = append(values, c)
		}
		if err := writeJSONLines(filepath.Join(root, "comments.jsonl"), values); err != nil {
			return err
		}
	}
	return nil
}

func sheetPreviewData(extracted sheetExtraction, limit int) SheetPreview {
	if limit <= 0 {
		limit = DefaultPreviewRows
	}
	rowOrder, byRow, totalRows := previewRowsFromCells(extracted.Cells, limit)
	maxCol := 0
	for _, row := range byRow {
		for _, cell := range row {
			if cell.Col > maxCol {
				maxCol = cell.Col
			}
		}
	}
	columns := make([]SheetPreviewColumn, 0, maxCol)
	for col := 1; col <= maxCol; col++ {
		columns = append(columns, SheetPreviewColumn{Index: col, Name: columnName(col)})
	}

	headerRows := inferPreviewHeaderRows(extracted, rowOrder, byRow)
	headerSet := intSet(headerRows)
	rows := make([]SheetPreviewRow, 0, len(rowOrder))
	for _, rowIndex := range rowOrder {
		cells := byRow[rowIndex]
		sort.Slice(cells, func(i, j int) bool { return cells[i].Col < cells[j].Col })
		previewCells := make([]SheetPreviewCell, 0, len(cells))
		for _, cell := range cells {
			display := cell.Display
			if display == "" && cell.Formula != "" {
				display = "=" + cell.Formula
			}
			previewCells = append(previewCells, SheetPreviewCell{
				Address:      cell.Address,
				Row:          cell.Row,
				Col:          cell.Col,
				Kind:         cell.Kind,
				Display:      display,
				Formula:      cell.Formula,
				Hyperlink:    cell.Hyperlink,
				Comment:      cell.Comment,
				NumberFormat: cell.NumberFormat,
				MergedParent: cell.MergedParent,
				Header:       headerSet[cell.Row] || previewCellLooksLikeHeader(cell),
				Hidden:       cell.Hidden,
				Style:        cell.Style,
			})
		}
		rows = append(rows, SheetPreviewRow{Index: rowIndex, Cells: previewCells})
	}

	return SheetPreview{
		Version: Version,
		Sheet:   extracted.Summary,
		Metadata: SheetPreviewMetadata{
			PreviewRowCount:    len(rows),
			PreviewColumnCount: maxCol,
			TotalRowCount:      extracted.Summary.RowCount,
			TotalColumnCount:   extracted.Summary.ColumnCount,
			TotalCellCount:     extracted.Summary.CellCount,
			FormulaCount:       extracted.Summary.FormulaCount,
			CommentCount:       extracted.Summary.CommentCount,
			MergedRangeCount:   extracted.Summary.MergedRangeCount,
			HiddenRowsOrCols:   extracted.Summary.HasHiddenRowsOrCols,
			Truncated:          totalRows > len(rows),
			Tables:             append([]TableSummary(nil), extracted.Tables...),
			Charts:             append([]ChartSummary(nil), extracted.Charts...),
		},
		Columns:      columns,
		Rows:         rows,
		HeaderRows:   headerRows,
		MergedRanges: append([]string(nil), extracted.MergedRanges...),
	}
}

func previewRowsFromCells(cells []CellRecord, limit int) ([]int, map[int][]CellRecord, int) {
	byRow := map[int][]CellRecord{}
	rowSeen := map[int]bool{}
	rowOrder := make([]int, 0, limit)
	totalSeen := map[int]bool{}
	for _, cell := range cells {
		if cell.Row <= 0 || cell.Col <= 0 {
			continue
		}
		totalSeen[cell.Row] = true
		if !rowSeen[cell.Row] {
			if len(rowOrder) >= limit {
				continue
			}
			rowSeen[cell.Row] = true
			rowOrder = append(rowOrder, cell.Row)
		}
		byRow[cell.Row] = append(byRow[cell.Row], cell)
	}
	sort.Ints(rowOrder)
	return rowOrder, byRow, len(totalSeen)
}

func inferPreviewHeaderRows(extracted sheetExtraction, rowOrder []int, byRow map[int][]CellRecord) []int {
	if len(rowOrder) == 0 {
		return nil
	}
	tableHeaders := map[int]bool{}
	for _, table := range extracted.Tables {
		if row := firstRowFromRange(table.Ref); row > 0 {
			tableHeaders[row] = true
		}
	}
	firstContent := 0
	for _, rowIndex := range rowOrder {
		if previewRowHasVisibleText(byRow[rowIndex]) {
			firstContent = rowIndex
			break
		}
	}
	if firstContent == 0 {
		return nil
	}
	headers := make([]int, 0, 3)
	started := false
	for _, rowIndex := range rowOrder {
		if rowIndex < firstContent {
			continue
		}
		if rowIndex > firstContent+4 {
			break
		}
		isHeader := tableHeaders[rowIndex] || previewRowLooksLikeHeader(byRow[rowIndex])
		if !started && rowIndex == firstContent && !isHeader && len(rowOrder) > 1 {
			isHeader = true
		}
		if isHeader {
			headers = append(headers, rowIndex)
			started = true
			continue
		}
		if started {
			break
		}
	}
	return headers
}

func previewRowHasVisibleText(cells []CellRecord) bool {
	for _, cell := range cells {
		if strings.TrimSpace(firstNonEmpty(cell.Display, cell.Formula)) != "" {
			return true
		}
	}
	return false
}

func previewRowLooksLikeHeader(cells []CellRecord) bool {
	visible := 0
	headerish := 0
	for _, cell := range cells {
		if strings.TrimSpace(firstNonEmpty(cell.Display, cell.Formula)) == "" {
			continue
		}
		visible++
		if previewCellLooksLikeHeader(cell) {
			headerish++
		}
	}
	return visible > 0 && (headerish >= 2 || headerish == visible)
}

func previewCellLooksLikeHeader(cell CellRecord) bool {
	if strings.TrimSpace(firstNonEmpty(cell.Display, cell.Formula)) == "" || cell.Style == nil {
		return false
	}
	style := *cell.Style
	return style.Bold || style.FillColor != ""
}

func firstRowFromRange(ref string) int {
	start := strings.TrimSpace(strings.Split(ref, ":")[0])
	row, _ := splitCellRef(start)
	return row
}

func intSet(values []int) map[int]bool {
	out := make(map[int]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

func sheetPreviewMarkdown(extracted sheetExtraction, limit int) string {
	if limit <= 0 {
		limit = DefaultPreviewRows
	}
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(extracted.Summary.Name)
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("- Rows: %d\n- Columns: %d\n- Cells: %d\n- Formulas: %d\n\n", extracted.Summary.RowCount, extracted.Summary.ColumnCount, extracted.Summary.CellCount, extracted.Summary.FormulaCount))
	rows := rowsFromCells(extracted.Cells, limit)
	if len(rows) == 0 {
		return b.String()
	}
	maxCol := 0
	for _, row := range rows {
		for col := range row {
			if col > maxCol {
				maxCol = col
			}
		}
	}
	b.WriteString("| Row |")
	for c := 1; c <= maxCol; c++ {
		b.WriteString(" ")
		b.WriteString(columnName(c))
		b.WriteString(" |")
	}
	b.WriteString("\n| --- |")
	for c := 1; c <= maxCol; c++ {
		b.WriteString(" --- |")
	}
	b.WriteString("\n")
	rowNums := make([]int, 0, len(rows))
	for r := range rows {
		rowNums = append(rowNums, r)
	}
	sort.Ints(rowNums)
	for _, r := range rowNums {
		b.WriteString(fmt.Sprintf("| %d |", r))
		for c := 1; c <= maxCol; c++ {
			b.WriteString(" ")
			b.WriteString(escapeMarkdownCell(rows[r][c]))
			b.WriteString(" |")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func rowsFromCells(cells []CellRecord, limit int) map[int]map[int]string {
	rows := map[int]map[int]string{}
	for _, cell := range cells {
		if len(rows) >= limit {
			if _, ok := rows[cell.Row]; !ok {
				continue
			}
		}
		if rows[cell.Row] == nil {
			rows[cell.Row] = map[int]string{}
		}
		value := cell.Display
		if value == "" && cell.Formula != "" {
			value = "=" + cell.Formula
		}
		rows[cell.Row][cell.Col] = value
	}
	return rows
}

func writeTables(outputDir string, tables []TableSummary) error {
	for _, table := range tables {
		if err := writeJSON(filepath.Join(outputDir, "tables", table.ID, "table.json"), table); err != nil {
			return err
		}
		if err := writeJSONLines(filepath.Join(outputDir, "tables", table.ID, "rows-0001.jsonl"), nil); err != nil {
			return err
		}
	}
	return nil
}

func writeCharts(outputDir string, charts []ChartSummary) error {
	for _, chart := range charts {
		if err := writeJSON(filepath.Join(outputDir, "charts", chart.ID+".json"), chart); err != nil {
			return err
		}
	}
	return nil
}

func cellsToAny(cells []CellRecord) []any {
	out := make([]any, 0, len(cells))
	for _, c := range cells {
		out = append(out, c)
	}
	return out
}

func (p *packageReader) extractSheetTables(sheetID string, sheetPath string, rels map[string]relationshipXML) []TableSummary {
	var out []TableSummary
	for _, rel := range sortedRelationships(rels) {
		if !strings.Contains(strings.ToLower(rel.Type), "/table") {
			continue
		}
		xmlPath := resolveRelationshipTarget(sheetPath, rel.Target)
		raw, err := p.read(xmlPath)
		if err != nil {
			continue
		}
		table := parseTable(sheetID, xmlPath, raw)
		if table.ID == "" {
			table.ID = fmt.Sprintf("table%d", len(out)+1)
		}
		out = append(out, table)
	}
	return out
}

func parseTable(sheetID string, xmlPath string, raw []byte) TableSummary {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	table := TableSummary{SheetID: sheetID, XMLPath: xmlPath}
	var headers []string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if start, ok := tok.(xml.StartElement); ok {
			switch start.Name.Local {
			case "table":
				table.ID = "table" + firstNonEmpty(attrValue(start, "id"), "1")
				table.Name = attrValue(start, "name")
				table.DisplayName = attrValue(start, "displayName")
				table.Ref = attrValue(start, "ref")
			case "tableColumn":
				if name := attrValue(start, "name"); name != "" {
					headers = append(headers, name)
				}
			}
		}
	}
	table.HeaderNames = headers
	table.RowCount, table.ColumnCount = tableDimensionsFromRef(table.Ref)
	table.RowsPath = path.Join("tables", table.ID, "rows-0001.jsonl")
	return table
}

func tableDimensionsFromRef(ref string) (int, int) {
	parts := strings.Split(strings.TrimSpace(ref), ":")
	if len(parts) != 2 {
		return 0, 0
	}
	r1, c1 := splitCellRef(parts[0])
	r2, c2 := splitCellRef(parts[1])
	if r1 == 0 || r2 == 0 || c1 == 0 || c2 == 0 {
		return 0, 0
	}
	return abs(r2-r1) + 1, abs(c2-c1) + 1
}

func (p *packageReader) extractSheetDrawingInventory(sheetID string, sheetPath string, rels map[string]relationshipXML) ([]ChartSummary, []MediaSummary) {
	var charts []ChartSummary
	var media []MediaSummary
	for _, rel := range sortedRelationships(rels) {
		typ := strings.ToLower(rel.Type)
		resolved := resolveRelationshipTarget(sheetPath, rel.Target)
		switch {
		case strings.Contains(typ, "/drawing"):
			drawRels, err := p.relationshipsFor(resolved)
			if err != nil {
				continue
			}
			for _, drawRel := range sortedRelationships(drawRels) {
				drawResolved := resolveRelationshipTarget(resolved, drawRel.Target)
				drawTyp := strings.ToLower(drawRel.Type)
				if strings.Contains(drawTyp, "/chart") {
					charts = append(charts, p.chartSummary(sheetID, fmt.Sprintf("chart%d", len(charts)+1), drawResolved))
				} else if strings.Contains(drawTyp, "/image") {
					media = append(media, p.mediaSummary(sheetID, fmt.Sprintf("media%d", len(media)+1), drawResolved))
				}
			}
		case strings.Contains(typ, "/chart"):
			charts = append(charts, p.chartSummary(sheetID, fmt.Sprintf("chart%d", len(charts)+1), resolved))
		case strings.Contains(typ, "/image"):
			media = append(media, p.mediaSummary(sheetID, fmt.Sprintf("media%d", len(media)+1), resolved))
		}
	}
	return charts, media
}

func (p *packageReader) chartSummary(sheetID string, id string, xmlPath string) ChartSummary {
	raw, _ := p.read(xmlPath)
	return ChartSummary{ID: id, SheetID: sheetID, XMLPath: xmlPath, Title: firstXMLText(raw, "t", "v"), Type: chartType(raw)}
}

func chartType(raw []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if start, ok := tok.(xml.StartElement); ok && strings.HasSuffix(start.Name.Local, "Chart") && start.Name.Local != "chart" {
			return start.Name.Local
		}
	}
	return ""
}

func (p *packageReader) mediaSummary(sheetID string, id string, xmlPath string) MediaSummary {
	s := MediaSummary{ID: id, SheetID: sheetID, XMLPath: xmlPath, ContentType: p.contentTypes.contentType(xmlPath), Quarantined: true}
	if f, ok := p.files[xmlPath]; ok {
		s.ByteSize = int64(f.UncompressedSize64)
		if raw, err := p.read(xmlPath); err == nil {
			sum := sha256.Sum256(raw)
			s.SHA256 = hex.EncodeToString(sum[:])
		}
	}
	return s
}

func (p *packageReader) extractSheetComments(sheetID string, sheetPath string) []Comment {
	rels, err := p.relationshipsFor(sheetPath)
	if err != nil {
		return nil
	}
	var out []Comment
	for _, rel := range sortedRelationships(rels) {
		if !strings.Contains(strings.ToLower(rel.Type), "/comments") {
			continue
		}
		xmlPath := resolveRelationshipTarget(sheetPath, rel.Target)
		raw, err := p.read(xmlPath)
		if err != nil {
			continue
		}
		out = append(out, parseComments(sheetID, xmlPath, raw)...)
	}
	return out
}

func parseComments(sheetID string, xmlPath string, raw []byte) []Comment {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var comments []Comment
	var current *Comment
	var capture bool
	var buf strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "comment":
				current = &Comment{SheetID: sheetID, Address: attrValue(t, "ref"), XMLPath: xmlPath}
			case "t":
				if current != nil {
					capture = true
				}
			}
		case xml.CharData:
			if capture {
				buf.WriteString(string(t))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				capture = false
			case "comment":
				if current != nil {
					current.Text = normalizeWhitespace(buf.String())
					if current.Text != "" {
						comments = append(comments, *current)
					}
				}
				current = nil
				buf.Reset()
			}
		}
	}
	return comments
}

func parseHiddenCols(raw []byte) map[int]bool {
	out := map[int]bool{}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if start, ok := tok.(xml.StartElement); ok && start.Name.Local == "col" {
			if attrValue(start, "hidden") == "1" || strings.EqualFold(attrValue(start, "hidden"), "true") {
				min := parseInt(attrValue(start, "min"))
				max := parseInt(attrValue(start, "max"))
				if max < min {
					max = min
				}
				for c := min; c <= max; c++ {
					out[c] = true
				}
			}
		}
	}
	return out
}

func parseSheetHyperlinks(raw []byte, rels map[string]relationshipXML, sheetPath string) map[string]string {
	out := map[string]string{}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if start, ok := tok.(xml.StartElement); ok && start.Name.Local == "hyperlink" {
			ref := attrValue(start, "ref")
			target := attrValue(start, "location")
			for _, attr := range start.Attr {
				if attr.Name.Local == "id" && attr.Name.Space != "" {
					if rel, ok := rels[attr.Value]; ok {
						target = rel.Target
						if !strings.EqualFold(rel.TargetMode, "External") {
							target = resolveRelationshipTarget(sheetPath, rel.Target)
						}
					}
				}
			}
			if ref != "" && target != "" {
				out[ref] = target
			}
		}
	}
	return out
}

func parseDimension(raw []byte) (int, int, string) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if start, ok := tok.(xml.StartElement); ok && start.Name.Local == "dimension" {
			ref := attrValue(start, "ref")
			if strings.Contains(ref, ":") {
				rows, cols := tableDimensionsFromRef(ref)
				return rows, cols, ref
			}
			r, c := splitCellRef(ref)
			return r, c, ref
		}
	}
	return 0, 0, ""
}

func parseMergedCells(raw []byte) ([]string, map[string]string) {
	var ranges []string
	parents := map[string]string{}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		if start, ok := tok.(xml.StartElement); ok && start.Name.Local == "mergeCell" {
			ref := attrValue(start, "ref")
			if ref == "" {
				continue
			}
			ranges = append(ranges, ref)
			parts := strings.Split(ref, ":")
			if len(parts) != 2 {
				continue
			}
			r1, c1 := splitCellRef(parts[0])
			r2, c2 := splitCellRef(parts[1])
			parent := parts[0]
			for r := min(r1, r2); r <= max(r1, r2); r++ {
				for c := min(c1, c2); c <= max(c1, c2); c++ {
					parents[fmt.Sprintf("%s%d", columnName(c), r)] = parent
				}
			}
		}
	}
	return ranges, parents
}

func (p *packageReader) externalLinks() []ExternalLink {
	var out []ExternalLink
	for name := range p.files {
		if strings.HasPrefix(name, "xl/externalLinks/") && strings.HasSuffix(name, ".xml") {
			out = append(out, ExternalLink{XMLPath: name})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].XMLPath < out[j].XMLPath })
	return out
}

func (p *packageReader) securitySummary() SecuritySummary {
	var s SecuritySummary
	for name, f := range p.files {
		lower := strings.ToLower(name)
		switch {
		case strings.Contains(lower, "encryptedpackage"):
			s.EncryptedPackage = true
			s.Warnings = append(s.Warnings, "encrypted package marker found")
		case strings.HasSuffix(lower, "vbaproject.bin"):
			s.HasMacros = true
			s.SuspiciousParts = append(s.SuspiciousParts, name)
		case strings.Contains(lower, "/embeddings/") || strings.Contains(lower, "oleobject"):
			s.HasOleObjects = true
			s.SuspiciousParts = append(s.SuspiciousParts, name)
		case strings.Contains(lower, "externallink"):
			s.HasExternalLinks = true
		}
		if f.UncompressedSize64 > 64<<20 && (strings.Contains(lower, "/media/") || strings.Contains(lower, "/embeddings/")) {
			s.Warnings = append(s.Warnings, "large embedded object: "+name)
		}
	}
	return s
}

func (p *packageReader) writeRawXML(root string) ([]string, error) {
	names := make([]string, 0, len(p.files))
	for name := range p.files {
		if strings.HasSuffix(strings.ToLower(name), ".xml") || strings.HasSuffix(strings.ToLower(name), ".rels") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, name := range names {
		raw, err := p.read(name)
		if err != nil {
			return out, err
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if err := writeFile(target, raw); err != nil {
			return out, err
		}
		out = append(out, path.Join("raw", "ooxml", name))
	}
	return out, nil
}

func sortedRelationships(rels map[string]relationshipXML) []relationshipXML {
	ids := make([]string, 0, len(rels))
	for id := range rels {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]relationshipXML, 0, len(ids))
	for _, id := range ids {
		out = append(out, rels[id])
	}
	return out
}

func workbookMarkdown(wb Workbook, tables []TableSummary, charts []ChartSummary) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(firstNonEmpty(wb.FileName, "Workbook"))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("- Date system: %s\n", wb.DateSystem))
	if wb.CalculationMode != "" {
		b.WriteString(fmt.Sprintf("- Calculation mode: %s\n", wb.CalculationMode))
	}
	b.WriteString(fmt.Sprintf("- Sheets: %d\n", len(wb.Sheets)))
	b.WriteString(fmt.Sprintf("- Tables: %d\n", len(tables)))
	b.WriteString(fmt.Sprintf("- Charts: %d\n\n", len(charts)))
	for _, sheet := range wb.Sheets {
		b.WriteString(fmt.Sprintf("## %s\n\n", sheet.Name))
		b.WriteString(fmt.Sprintf("- ID: %s\n- State: %s\n- Dimensions: %s\n- Rows: %d\n- Columns: %d\n- Cells: %d\n- Formulas: %d\n\n", sheet.ID, sheet.State, sheet.Dimension, sheet.RowCount, sheet.ColumnCount, sheet.CellCount, sheet.FormulaCount))
		if sheet.PreviewPath != "" {
			b.WriteString(fmt.Sprintf("Preview: `%s`\n\n", sheet.PreviewPath))
		}
	}
	if len(wb.DefinedNames) > 0 {
		b.WriteString("## Defined Names\n\n")
		for _, dn := range wb.DefinedNames {
			b.WriteString("- ")
			b.WriteString(dn.Name)
			b.WriteString(": `")
			b.WriteString(dn.Text)
			b.WriteString("`\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func firstXMLText(raw []byte, names ...string) string {
	want := map[string]bool{}
	for _, name := range names {
		want[name] = true
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var capture bool
	var buf strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if want[t.Name.Local] {
				capture = true
				buf.Reset()
			}
		case xml.CharData:
			if capture {
				buf.WriteString(string(t))
			}
		case xml.EndElement:
			if want[t.Name.Local] {
				text := normalizeWhitespace(buf.String())
				if text != "" {
					return text
				}
				capture = false
			}
		}
	}
	return ""
}

func inferDimensions(cells []CellRecord) (int, int) {
	rows, cols := 0, 0
	for _, cell := range cells {
		if cell.Row > rows {
			rows = cell.Row
		}
		if cell.Col > cols {
			cols = cell.Col
		}
	}
	return rows, cols
}

var cellRefRe = regexp.MustCompile(`^([A-Za-z]+)([0-9]+)$`)

func splitCellRef(ref string) (int, int) {
	m := cellRefRe.FindStringSubmatch(strings.TrimSpace(ref))
	if len(m) != 3 {
		return 0, 0
	}
	row := parseInt(m[2])
	col := 0
	for _, r := range strings.ToUpper(m[1]) {
		if r < 'A' || r > 'Z' {
			return row, 0
		}
		col = col*26 + int(r-'A'+1)
	}
	return row, col
}

func columnName(n int) string {
	if n <= 0 {
		return ""
	}
	var out []byte
	for n > 0 {
		n--
		out = append([]byte{byte('A' + n%26)}, out...)
		n /= 26
	}
	return string(out)
}

func attrValue(start xml.StartElement, local string) string {
	for _, attr := range start.Attr {
		if attr.Name.Local == local {
			return strings.TrimSpace(attr.Value)
		}
	}
	return ""
}

func parseInt(raw string) int {
	if strings.TrimSpace(raw) == "" {
		return 0
	}
	v, _ := strconv.Atoi(strings.TrimSpace(raw))
	return v
}

func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func escapeMarkdownCell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, append(raw, '\n'))
}

func writeJSONLines(path string, values []any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, value := range values {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if _, err := w.Write(raw); err != nil {
			return err
		}
		if err := w.WriteByte('\n'); err != nil {
			return err
		}
	}
	return w.Flush()
}

func writeFile(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

type manifestBuilder struct {
	manifest Manifest
}

func (b *manifestBuilder) stage(stage Stage) {
	b.manifest.Stages = append(b.manifest.Stages, stage)
	if stage.Status == StageFailed && b.manifest.Status == StatusComplete {
		b.manifest.Status = StatusPartial
	}
}

func (b *manifestBuilder) warn(msg string) {
	msg = strings.TrimSpace(msg)
	if msg != "" {
		b.manifest.Warnings = append(b.manifest.Warnings, msg)
	}
}

func (b *manifestBuilder) output(key string, value any) {
	if b.manifest.Outputs == nil {
		b.manifest.Outputs = map[string]any{}
	}
	b.manifest.Outputs[key] = value
}

func (b *manifestBuilder) finishFailed() Manifest {
	b.manifest.Status = StatusFailed
	if b.manifest.Outputs == nil {
		b.manifest.Outputs = map[string]any{}
	}
	return b.manifest
}

func ProjectExtractionKey(relPath string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(filepath.ToSlash(relPath))))
	return hex.EncodeToString(sum[:])[:24]
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
