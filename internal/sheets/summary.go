package sheets

import (
	"context"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	googlesheets "google.golang.org/api/sheets/v4"
)

var yearlyTitlePattern = regexp.MustCompile(`^Daily Log ([0-9]{4})$`)

func reportingYears(worksheets map[string]*googlesheets.Sheet) []string {
	var years []string
	for title := range worksheets {
		match := yearlyTitlePattern.FindStringSubmatch(title)
		if match != nil && match[1] != "0000" {
			years = append(years, match[1])
		}
	}
	sort.Strings(years)
	return years
}

const summaryMetadataFields = "sheets(properties(sheetId,title),data(startRow,startColumn,rowData(values(userEnteredValue,dataValidation,userEnteredFormat))))"

type summaryCoordinate struct{ row, col int64 }

func summaryGrid(sheet *googlesheets.Sheet) map[summaryCoordinate]*googlesheets.CellData {
	cells := make(map[summaryCoordinate]*googlesheets.CellData)
	for _, grid := range sheet.Data {
		if grid == nil {
			continue
		}
		for r, row := range grid.RowData {
			if row == nil {
				continue
			}
			for col, cell := range row.Values {
				if cell != nil {
					cells[summaryCoordinate{grid.StartRow + int64(r), grid.StartColumn + int64(col)}] = cell
				}
			}
		}
	}
	return cells
}

func blankSummaryValue(value *googlesheets.ExtendedValue) bool {
	return value == nil || (value.StringValue != nil && *value.StringValue == "" && value.FormulaValue == nil && value.NumberValue == nil && value.BoolValue == nil && value.ErrorValue == nil)
}

func selectedYear(value *googlesheets.ExtendedValue) string {
	if value == nil {
		return ""
	}
	if value.StringValue != nil {
		return *value.StringValue
	}
	if value.NumberValue != nil {
		return strconv.FormatFloat(*value.NumberValue, 'f', -1, 64)
	}
	return ""
}

func summaryCellUpdate(id int64, spec summaryCell, fields string, cell *googlesheets.CellData) *googlesheets.Request {
	return &googlesheets.Request{UpdateCells: &googlesheets.UpdateCellsRequest{
		Start: &googlesheets.GridCoordinate{SheetId: id, RowIndex: spec.row, ColumnIndex: spec.col, ForceSendFields: []string{"SheetId", "RowIndex", "ColumnIndex"}},
		Rows:  []*googlesheets.RowData{{Values: []*googlesheets.CellData{cell}}}, Fields: fields,
	}}
}

func matchingSummaryFormat(actual, desired *googlesheets.CellFormat, fields string) bool {
	if actual == nil {
		return false
	}
	for _, field := range strings.Split(fields, ",") {
		switch field {
		case "userEnteredFormat.textFormat.bold":
			if actual.TextFormat == nil || actual.TextFormat.Bold != desired.TextFormat.Bold {
				return false
			}
		case "userEnteredFormat.textFormat.fontSize":
			if actual.TextFormat == nil || actual.TextFormat.FontSize != desired.TextFormat.FontSize {
				return false
			}
		case "userEnteredFormat.backgroundColorStyle":
			if !reflect.DeepEqual(actual.BackgroundColorStyle, desired.BackgroundColorStyle) {
				return false
			}
		case "userEnteredFormat.numberFormat":
			if !reflect.DeepEqual(actual.NumberFormat, desired.NumberFormat) {
				return false
			}
		case "userEnteredFormat.wrapStrategy":
			if actual.WrapStrategy != desired.WrapStrategy {
				return false
			}
		}
	}
	return true
}

func (c *Client) ensureSummary(ctx context.Context) error {
	worksheets, err := c.worksheetMetadata(ctx)
	if err != nil {
		return err
	}
	years := reportingYears(worksheets)
	if len(years) == 0 {
		return fmt.Errorf("Summary has no available Daily Log YYYY worksheets")
	}
	// Only Summary entered cells are inspected. Effective formula results are
	// deliberately excluded: they are presentation, not owned source content.
	metadata, err := c.Service.Spreadsheets.Get(c.SpreadsheetID).Ranges(summaryRange).
		Fields(summaryMetadataFields).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("read Summary structure: %w", err)
	}
	var sheet *googlesheets.Sheet
	for _, candidate := range metadata.Sheets {
		if candidate != nil && candidate.Properties != nil && candidate.Properties.Title == "Summary" {
			sheet = candidate
			break
		}
	}
	if sheet == nil {
		return fmt.Errorf("spreadsheet missing required worksheet: Summary")
	}
	id := sheet.Properties.SheetId
	grid := summaryGrid(sheet)
	var requests []*googlesheets.Request
	for _, spec := range summaryLayout() {
		actual := grid[summaryCoordinate{spec.row, spec.col}]
		if actual == nil {
			actual = &googlesheets.CellData{}
		}
		if blankSummaryValue(actual.UserEnteredValue) {
			requests = append(requests, summaryCellUpdate(id, spec, "userEnteredValue", &googlesheets.CellData{UserEnteredValue: spec.value}))
		} else if !reflect.DeepEqual(actual.UserEnteredValue, spec.value) {
			return fmt.Errorf("incompatible Summary structure at %c%d: expected application label/formula", 'A'+spec.col, spec.row+1)
		}
		if spec.format != nil && !matchingSummaryFormat(actual.UserEnteredFormat, spec.format, spec.formatFields) {
			requests = append(requests, summaryCellUpdate(id, spec, spec.formatFields, &googlesheets.CellData{UserEnteredFormat: spec.format}))
		}
	}
	yearCell := grid[summaryCoordinate{2, 1}]
	if yearCell == nil {
		yearCell = &googlesheets.CellData{}
	}
	selected := selectedYear(yearCell.UserEnteredValue)
	valid := false
	for _, year := range years {
		if selected == year {
			valid = true
			break
		}
	}
	desired := selected
	if !valid {
		desired = years[len(years)-1]
	}
	if desired != selected {
		requests = append(requests, summaryCellUpdate(id, summaryCell{row: 2, col: 1}, "userEnteredValue", &googlesheets.CellData{UserEnteredValue: &googlesheets.ExtendedValue{StringValue: &desired}}))
	}
	validation := reportingYearValidation(years)
	if !reflect.DeepEqual(yearCell.DataValidation, validation) {
		requests = append(requests, reportingYearValidationUpdate(id, validation))
	}
	yearFormat := &googlesheets.CellFormat{TextFormat: &googlesheets.TextFormat{Bold: true}, BackgroundColorStyle: &googlesheets.ColorStyle{RgbColor: &googlesheets.Color{Red: .91, Green: .94, Blue: .97}}, NumberFormat: &googlesheets.NumberFormat{Type: "NUMBER", Pattern: "0"}}
	yearFields := "userEnteredFormat.textFormat.bold,userEnteredFormat.backgroundColorStyle,userEnteredFormat.numberFormat"
	if !matchingSummaryFormat(yearCell.UserEnteredFormat, yearFormat, yearFields) {
		requests = append(requests, summaryCellUpdate(id, summaryCell{row: 2, col: 1}, yearFields, &googlesheets.CellData{UserEnteredFormat: yearFormat}))
	}
	// Resizing is harmless on repeats and also finishes initialization after
	// failures. Every other request is emitted only for a missing/different field.
	requests = append(requests, &googlesheets.Request{AutoResizeDimensions: &googlesheets.AutoResizeDimensionsRequest{Dimensions: &googlesheets.DimensionRange{SheetId: id, Dimension: "COLUMNS", EndIndex: 7, ForceSendFields: []string{"SheetId", "StartIndex"}}}})
	_, err = c.Service.Spreadsheets.BatchUpdate(c.SpreadsheetID, &googlesheets.BatchUpdateSpreadsheetRequest{Requests: requests}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("initialize/maintain Summary: %w", err)
	}
	return nil
}

func reportingYearValidation(years []string) *googlesheets.DataValidationRule {
	validation := &googlesheets.DataValidationRule{Condition: &googlesheets.BooleanCondition{Type: "ONE_OF_LIST"}, Strict: true, ShowCustomUi: true}
	for _, year := range years {
		validation.Condition.Values = append(validation.Condition.Values, &googlesheets.ConditionValue{UserEnteredValue: year})
	}
	return validation
}

func reportingYearValidationUpdate(id int64, validation *googlesheets.DataValidationRule) *googlesheets.Request {
	return &googlesheets.Request{SetDataValidation: &googlesheets.SetDataValidationRequest{
		Range: &googlesheets.GridRange{SheetId: id, StartRowIndex: 2, EndRowIndex: 3, StartColumnIndex: 1, EndColumnIndex: 2, ForceSendFields: []string{"SheetId"}}, Rule: validation,
	}}
}
