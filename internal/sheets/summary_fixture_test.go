package sheets

import (
	"strings"

	googlesheets "google.golang.org/api/sheets/v4"
)

func (f *sheetFixture) summaryCell(row, col int64) *googlesheets.CellData {
	if f.summary == nil {
		f.summary = make(map[summaryCoordinate]*googlesheets.CellData)
	}
	key := summaryCoordinate{row, col}
	if f.summary[key] == nil {
		f.summary[key] = &googlesheets.CellData{}
	}
	return f.summary[key]
}

func (f *sheetFixture) summaryRows() []*googlesheets.RowData {
	rows := make([]*googlesheets.RowData, 43)
	for row := int64(0); row < 43; row++ {
		values := make([]*googlesheets.CellData, 7)
		for col := int64(0); col < 7; col++ {
			values[col] = f.summaryCell(row, col)
		}
		rows[row] = &googlesheets.RowData{Values: values}
	}
	return rows
}

func (f *sheetFixture) applySummary(requests []*googlesheets.Request) {
	f.summaryBatches = append(f.summaryBatches, requests)
	id := f.sheet("Summary").Properties.SheetId
	owned := make(map[summaryCoordinate]bool)
	for _, spec := range summaryLayout() {
		owned[summaryCoordinate{spec.row, spec.col}] = true
	}
	owned[summaryCoordinate{2, 1}] = true
	for _, request := range requests {
		switch {
		case request.UpdateCells != nil:
			u := request.UpdateCells
			if u.Start == nil || u.Start.SheetId != id || len(u.Rows) != 1 || len(u.Rows[0].Values) != 1 || !owned[summaryCoordinate{u.Start.RowIndex, u.Start.ColumnIndex}] {
				f.t.Fatal("Summary update must target one owned cell")
			}
			cell := f.summaryCell(u.Start.RowIndex, u.Start.ColumnIndex)
			data := u.Rows[0].Values[0]
			if u.Fields == "userEnteredValue" {
				cell.UserEnteredValue = data.UserEnteredValue
				f.summaryValueWrites++
				continue
			}
			if cell.UserEnteredFormat == nil {
				cell.UserEnteredFormat = &googlesheets.CellFormat{}
			}
			actual, desired := cell.UserEnteredFormat, data.UserEnteredFormat
			for _, field := range strings.Split(u.Fields, ",") {
				switch field {
				case "userEnteredFormat.textFormat.bold":
					if actual.TextFormat == nil {
						actual.TextFormat = &googlesheets.TextFormat{}
					}
					actual.TextFormat.Bold = desired.TextFormat.Bold
				case "userEnteredFormat.textFormat.fontSize":
					if actual.TextFormat == nil {
						actual.TextFormat = &googlesheets.TextFormat{}
					}
					actual.TextFormat.FontSize = desired.TextFormat.FontSize
				case "userEnteredFormat.backgroundColorStyle":
					actual.BackgroundColorStyle = desired.BackgroundColorStyle
				case "userEnteredFormat.numberFormat":
					actual.NumberFormat = desired.NumberFormat
				case "userEnteredFormat.wrapStrategy":
					actual.WrapStrategy = desired.WrapStrategy
				default:
					f.t.Fatalf("unexpected Summary field mask %s", field)
				}
			}
			f.summaryFormatWrites++
		case request.SetDataValidation != nil:
			v := request.SetDataValidation
			if v.Range.SheetId != id || v.Range.StartRowIndex != 2 || v.Range.EndRowIndex != 3 || v.Range.StartColumnIndex != 1 || v.Range.EndColumnIndex != 2 {
				f.t.Fatal("wrong validation cell")
			}
			f.summaryCell(2, 1).DataValidation = v.Rule
			f.summaryValidationWrites++
		case request.AutoResizeDimensions != nil:
			d := request.AutoResizeDimensions.Dimensions
			if d.SheetId != id || d.Dimension != "COLUMNS" || d.StartIndex != 0 || d.EndIndex != 7 {
				f.t.Fatal("wrong Summary sizing")
			}
			f.summaryResizes++
		default:
			f.t.Fatal("unexpected Summary operation")
		}
	}
}
