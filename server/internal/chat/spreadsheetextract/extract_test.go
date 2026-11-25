package spreadsheetextract

import "testing"

func TestSheetPreviewDataIncludesHeadersMetadataAndStyles(t *testing.T) {
	t.Parallel()

	bold := &CellStylePreview{Bold: true, FillColor: "#002060", FontColor: "#ffffff"}
	regular := &CellStylePreview{HorizontalAlign: "right"}
	extracted := sheetExtraction{
		Summary: SheetSummary{
			ID:           "sheet1",
			Name:         "Cap Table",
			Index:        2,
			RowCount:     10,
			ColumnCount:  3,
			CellCount:    4,
			FormulaCount: 1,
		},
		Cells: []CellRecord{
			{SheetID: "sheet1", Address: "A1", Row: 1, Col: 1, Kind: "string", Display: "Investor", Style: bold},
			{SheetID: "sheet1", Address: "B1", Row: 1, Col: 2, Kind: "string", Display: "Shares", Style: bold},
			{SheetID: "sheet1", Address: "A2", Row: 2, Col: 1, Kind: "string", Display: "Example Fund"},
			{SheetID: "sheet1", Address: "B2", Row: 2, Col: 2, Kind: "number", Display: "1000", Formula: "SUM(B3:B4)", NumberFormat: "#,##0", Style: regular},
		},
		MergedRanges: []string{"A1:B1"},
	}

	preview := sheetPreviewData(extracted, 50)
	if preview.Metadata.TotalCellCount != 4 || preview.Metadata.FormulaCount != 1 {
		t.Fatalf("metadata = %#v", preview.Metadata)
	}
	if len(preview.Columns) != 2 || preview.Columns[0].Name != "A" || preview.Columns[1].Name != "B" {
		t.Fatalf("columns = %#v", preview.Columns)
	}
	if len(preview.HeaderRows) != 1 || preview.HeaderRows[0] != 1 {
		t.Fatalf("header rows = %#v", preview.HeaderRows)
	}
	if len(preview.Rows) != 2 || len(preview.Rows[0].Cells) != 2 {
		t.Fatalf("rows = %#v", preview.Rows)
	}
	header := preview.Rows[0].Cells[0]
	if !header.Header || header.Style == nil || header.Style.FillColor != "#002060" || header.Style.FontColor != "#ffffff" {
		t.Fatalf("header cell = %#v", header)
	}
	value := preview.Rows[1].Cells[1]
	if value.Formula != "SUM(B3:B4)" || value.NumberFormat != "#,##0" || value.Style == nil || value.Style.HorizontalAlign != "right" {
		t.Fatalf("value cell = %#v", value)
	}
	if len(preview.MergedRanges) != 1 || preview.MergedRanges[0] != "A1:B1" {
		t.Fatalf("merged ranges = %#v", preview.MergedRanges)
	}
}
