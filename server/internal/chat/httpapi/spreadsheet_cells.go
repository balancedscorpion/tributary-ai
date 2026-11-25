package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"ai-analyst-local/internal/chat/fileartifact"
	"ai-analyst-local/internal/chat/store"
)

const (
	defaultSpreadsheetCellPageRows  = 200
	maxSpreadsheetCellPageRows      = 500
	defaultSpreadsheetCellPageCols  = 100
	maxSpreadsheetCellPageCols      = 200
	maxSpreadsheetCellPageCells     = 20000
	maxSpreadsheetCellLineBytes     = 1 << 20
	maxSpreadsheetCellTextRunes     = 8192
	maxSpreadsheetCellReplyBytes    = 2 << 20
	maxSpreadsheetWorkbookJSONBytes = 16 << 20
)

type spreadsheetWorkbookFile struct {
	Sheets []spreadsheetSheetFile `json:"sheets"`
}

type spreadsheetSheetFile struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Index        int    `json:"index"`
	State        string `json:"state,omitempty"`
	Dimension    string `json:"dimension,omitempty"`
	RowCount     int    `json:"rowCount,omitempty"`
	ColumnCount  int    `json:"columnCount,omitempty"`
	CellCount    int    `json:"cellCount,omitempty"`
	FormulaCount int    `json:"formulaCount,omitempty"`
	CellsPath    string `json:"cellsPath,omitempty"`
}

type spreadsheetCellJSONLRecord struct {
	SheetID      string         `json:"sheet_id,omitempty"`
	Address      string         `json:"address,omitempty"`
	Row          int            `json:"row"`
	Col          int            `json:"col"`
	Kind         string         `json:"kind,omitempty"`
	Display      string         `json:"display,omitempty"`
	Formula      string         `json:"formula,omitempty"`
	Hyperlink    string         `json:"hyperlink,omitempty"`
	Comment      string         `json:"comment,omitempty"`
	NumberFormat string         `json:"number_format,omitempty"`
	MergedParent string         `json:"merged_parent,omitempty"`
	Hidden       bool           `json:"hidden,omitempty"`
	Style        map[string]any `json:"style,omitempty"`
}

type spreadsheetCellPageCell struct {
	Address      string         `json:"address,omitempty"`
	Row          int            `json:"row"`
	Col          int            `json:"col"`
	Kind         string         `json:"kind,omitempty"`
	Display      string         `json:"display,omitempty"`
	Formula      string         `json:"formula,omitempty"`
	Hyperlink    string         `json:"hyperlink,omitempty"`
	Comment      string         `json:"comment,omitempty"`
	NumberFormat string         `json:"numberFormat,omitempty"`
	MergedParent string         `json:"mergedParent,omitempty"`
	Hidden       bool           `json:"hidden,omitempty"`
	Style        map[string]any `json:"style,omitempty"`
}

type spreadsheetCellPageRow struct {
	Index int                       `json:"index"`
	Cells []spreadsheetCellPageCell `json:"cells"`
}

type spreadsheetCellPageColumn struct {
	Index int    `json:"index"`
	Name  string `json:"name"`
}

func (h *Handler) handleGetSpreadsheetCells(w http.ResponseWriter, r *http.Request) {
	artifactID := strings.TrimSpace(r.PathValue("artifactId"))
	if artifactID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing artifactId"})
		return
	}
	sheetKey := strings.TrimSpace(r.URL.Query().Get("sheet_id"))
	if sheetKey == "" {
		sheetKey = strings.TrimSpace(r.URL.Query().Get("sheet"))
	}
	if sheetKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing sheet_id"})
		return
	}

	rowStart := parseInt(r.URL.Query().Get("row_start"), 1, 1, int(^uint(0)>>1))
	rowCount := parseInt(r.URL.Query().Get("row_count"), defaultSpreadsheetCellPageRows, 1, maxSpreadsheetCellPageRows)
	colStart := parseInt(r.URL.Query().Get("col_start"), 1, 1, int(^uint(0)>>1))
	colCount := parseInt(r.URL.Query().Get("col_count"), defaultSpreadsheetCellPageCols, 1, maxSpreadsheetCellPageCols)
	if rowCount*colCount > maxSpreadsheetCellPageCells {
		colCount = maxSpreadsheetCellPageCells / rowCount
		if colCount < 1 {
			colCount = 1
		}
	}

	artifact, err := h.store.GetArtifact(r.Context(), artifactID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	extraction, ok := spreadsheetExtractionForArtifact(artifact)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	rootRel := strings.Trim(strings.TrimSpace(firstStringMapValue(extraction, "rootPath", "root_path")), "/")
	workbookRel := spreadsheetExtractionPath(rootRel, firstStringMapValue(extraction, "workbookPath", "workbook_path", "dataPath", "data_path"))
	if workbookRel == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	workbook, err := readSpreadsheetWorkbookFile(h.cfgDir, workbookRel)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	sheet, ok := findSpreadsheetSheet(workbook.Sheets, sheetKey)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "sheet not found"})
		return
	}
	if colCount == defaultSpreadsheetCellPageCols && sheet.ColumnCount > 0 && sheet.ColumnCount < colCount {
		colCount = sheet.ColumnCount
	}
	cellsRel := spreadsheetExtractionPath(rootRel, sheet.CellsPath)
	if !spreadsheetCellSidecarPathAllowed(rootRel, cellsRel, sheet.ID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}

	fullCellsPath, ok := safeConfigRelativePath(h.cfgDir, cellsRel)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	rows, truncatedByCellCap, err := readSpreadsheetCellPage(fullCellsPath, rowStart, rowCount, colStart, colCount)
	if err != nil {
		h.writeErr(w, err)
		return
	}

	rowEndExclusive := rowStart + rowCount
	colEndExclusive := colStart + colCount
	columns := make([]spreadsheetCellPageColumn, 0, colCount)
	for col := colStart; col < colEndExclusive; col++ {
		columns = append(columns, spreadsheetCellPageColumn{Index: col, Name: spreadsheetColumnName(col)})
	}

	hasMoreRows := sheet.RowCount <= 0 || rowEndExclusive <= sheet.RowCount
	hasMoreColumns := sheet.ColumnCount > 0 && colEndExclusive <= sheet.ColumnCount
	payload := map[string]any{
		"artifact_id":                   artifact.ID,
		"sheet_id":                      sheet.ID,
		"sheet_name":                    sheet.Name,
		"row_start":                     rowStart,
		"row_count":                     rowCount,
		"col_start":                     colStart,
		"col_count":                     colCount,
		"rows":                          rows,
		"columns":                       columns,
		"rows_returned":                 len(rows),
		"total_row_count":               sheet.RowCount,
		"total_column_count":            sheet.ColumnCount,
		"has_more_rows":                 hasMoreRows || truncatedByCellCap,
		"has_more_columns":              hasMoreColumns,
		"next_row_start":                rowEndExclusive,
		"truncated_by_cell_count_limit": truncatedByCellCap,
	}
	payload, truncatedByResponseSize, err := clampSpreadsheetCellPayload(payload, rows)
	if err != nil {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error": "requested cells exceed response size limit; reduce row_count or col_count",
		})
		return
	}
	if truncatedByResponseSize {
		payload["has_more_rows"] = true
		payload["truncated_by_response_size"] = true
		if clampedRows, ok := payload["rows"].([]spreadsheetCellPageRow); ok && len(clampedRows) > 0 {
			payload["next_row_start"] = clampedRows[len(clampedRows)-1].Index + 1
		}
	}

	writeJSON(w, http.StatusOK, payload)
}

func spreadsheetExtractionForArtifact(artifact store.Artifact) (map[string]any, bool) {
	if !fileartifact.IsFileAttachmentArtifact(artifact) {
		return nil, false
	}
	extraction, ok := artifact.Metadata["extraction"].(map[string]any)
	if !ok || len(extraction) == 0 {
		return nil, false
	}
	kind := strings.ToLower(strings.TrimSpace(firstStringMapValue(extraction, "kind", "pipeline")))
	if kind != "spreadsheet" && kind != "xlsx" && kind != "xls" && kind != "csv" && kind != "tsv" {
		return nil, false
	}
	return extraction, true
}

func spreadsheetExtractionPath(rootRel string, rel string) string {
	rel = strings.Trim(strings.TrimSpace(filepath.ToSlash(rel)), "/")
	rootRel = strings.Trim(strings.TrimSpace(filepath.ToSlash(rootRel)), "/")
	if rel == "" {
		return ""
	}
	if rootRel != "" && rel != rootRel && !strings.HasPrefix(rel, rootRel+"/") {
		rel = path.Join(rootRel, rel)
	}
	clean, err := cleanRelPath(rel)
	if err != nil {
		return ""
	}
	return clean
}

func readSpreadsheetWorkbookFile(cfgDir string, rel string) (spreadsheetWorkbookFile, error) {
	full, ok := safeConfigRelativePath(cfgDir, rel)
	if !ok {
		return spreadsheetWorkbookFile{}, errors.New("invalid workbook path")
	}
	raw, err := osReadFileLimit(full, maxSpreadsheetWorkbookJSONBytes)
	if err != nil {
		return spreadsheetWorkbookFile{}, err
	}
	var workbook spreadsheetWorkbookFile
	if err := json.Unmarshal(raw, &workbook); err != nil {
		return spreadsheetWorkbookFile{}, err
	}
	return workbook, nil
}

func findSpreadsheetSheet(sheets []spreadsheetSheetFile, key string) (spreadsheetSheetFile, bool) {
	cleanKey := strings.TrimSpace(key)
	for _, sheet := range sheets {
		if strings.EqualFold(strings.TrimSpace(sheet.ID), cleanKey) ||
			strings.EqualFold(strings.TrimSpace(sheet.Name), cleanKey) ||
			(sheet.Index > 0 && strconv.Itoa(sheet.Index) == cleanKey) {
			return sheet, true
		}
	}
	return spreadsheetSheetFile{}, false
}

func spreadsheetCellSidecarPathAllowed(rootRel string, cellsRel string, sheetID string) bool {
	rootRel = strings.Trim(strings.TrimSpace(filepath.ToSlash(rootRel)), "/")
	cellsRel = strings.Trim(strings.TrimSpace(filepath.ToSlash(cellsRel)), "/")
	if rootRel == "" || cellsRel == "" {
		return false
	}
	if cellsRel != rootRel && !strings.HasPrefix(cellsRel, rootRel+"/") {
		return false
	}
	clean := strings.ToLower(filepath.ToSlash(cellsRel))
	if !strings.Contains(clean, "/worksheets/") {
		return false
	}
	base := path.Base(clean)
	if !strings.HasPrefix(base, "cells-") || !strings.HasSuffix(base, ".jsonl") {
		return false
	}
	if sheetID != "" {
		expected := "/worksheets/" + strings.ToLower(strings.Trim(sheetID, "/")) + "/"
		if !strings.Contains(clean, expected) {
			return false
		}
	}
	return true
}

func readSpreadsheetCellPage(fullPath string, rowStart int, rowCount int, colStart int, colCount int) ([]spreadsheetCellPageRow, bool, error) {
	f, err := os.Open(fullPath)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()

	rowEnd := rowStart + rowCount
	colEnd := colStart + colCount
	rowsByIndex := map[int][]spreadsheetCellPageCell{}
	seenRows := map[int]bool{}
	truncatedByCellCap := false
	cellCount := 0
	reader := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > maxSpreadsheetCellLineBytes {
			line = line[:maxSpreadsheetCellLineBytes]
		}
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			var record spreadsheetCellJSONLRecord
			if decodeErr := json.Unmarshal(line, &record); decodeErr == nil {
				if record.Row >= rowStart && record.Row < rowEnd && record.Col >= colStart && record.Col < colEnd {
					if cellCount >= maxSpreadsheetCellPageCells {
						truncatedByCellCap = true
						break
					}
					rowsByIndex[record.Row] = append(rowsByIndex[record.Row], spreadsheetCellPageCellFromRecord(record))
					seenRows[record.Row] = true
					cellCount++
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, false, err
		}
	}

	rowIndexes := make([]int, 0, len(seenRows))
	for row := range seenRows {
		rowIndexes = append(rowIndexes, row)
	}
	sort.Ints(rowIndexes)
	out := make([]spreadsheetCellPageRow, 0, len(rowIndexes))
	for _, row := range rowIndexes {
		cells := rowsByIndex[row]
		sort.Slice(cells, func(i, j int) bool { return cells[i].Col < cells[j].Col })
		out = append(out, spreadsheetCellPageRow{Index: row, Cells: cells})
	}
	return out, truncatedByCellCap, nil
}

func spreadsheetCellPageCellFromRecord(record spreadsheetCellJSONLRecord) spreadsheetCellPageCell {
	display := record.Display
	if display == "" && record.Formula != "" {
		display = "=" + record.Formula
	}
	return spreadsheetCellPageCell{
		Address:      strings.TrimSpace(record.Address),
		Row:          record.Row,
		Col:          record.Col,
		Kind:         strings.TrimSpace(record.Kind),
		Display:      truncateSpreadsheetCellText(display),
		Formula:      truncateSpreadsheetCellText(record.Formula),
		Hyperlink:    truncateSpreadsheetCellText(record.Hyperlink),
		Comment:      truncateSpreadsheetCellText(record.Comment),
		NumberFormat: truncateSpreadsheetCellText(record.NumberFormat),
		MergedParent: truncateSpreadsheetCellText(record.MergedParent),
		Hidden:       record.Hidden,
		Style:        record.Style,
	}
}

func truncateSpreadsheetCellText(value string) string {
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxSpreadsheetCellTextRunes {
		return value
	}
	return string(runes[:maxSpreadsheetCellTextRunes]) + "..."
}

func clampSpreadsheetCellPayload(payload map[string]any, rows []spreadsheetCellPageRow) (map[string]any, bool, error) {
	currentRows := append([]spreadsheetCellPageRow(nil), rows...)
	truncated := false
	for {
		payload["rows"] = currentRows
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, false, err
		}
		if len(raw) <= maxSpreadsheetCellReplyBytes {
			payload["response_size"] = len(raw)
			payload["rows_returned"] = len(currentRows)
			return payload, truncated, nil
		}
		if len(currentRows) == 0 {
			return nil, false, fmt.Errorf("response too large")
		}
		truncated = true
		currentRows = currentRows[:len(currentRows)-1]
	}
}

func spreadsheetColumnName(index int) string {
	if index <= 0 {
		return ""
	}
	var name string
	for index > 0 {
		index--
		name = string(rune('A'+(index%26))) + name
		index /= 26
	}
	return name
}
