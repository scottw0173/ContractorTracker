package sheets

import (
	"fmt"
	"time"

	googlesheets "google.golang.org/api/sheets/v4"
)

const summaryRange = "'Summary'!A1:G43"

// Coordinates are zero-based. B3 is the sole user-controlled input; the other
// listed cells are application-owned labels or formulas. Unlisted cells are left alone.
type summaryCell struct {
	row, col     int64
	value        *googlesheets.ExtendedValue
	format       *googlesheets.CellFormat
	formatFields string
}

func summaryReference(columns string) string {
	return `INDIRECT("'Daily Log "&$B$3&"'!` + columns + `")`
}

func summaryLayout() []summaryCell {
	var cells []summaryCell
	label := func(row, col int64, text string) {
		cells = append(cells, summaryCell{row: row - 1, col: col - 1, value: &googlesheets.ExtendedValue{StringValue: &text}})
	}
	formula := func(row, col int64, text string) {
		cells = append(cells, summaryCell{row: row - 1, col: col - 1, value: &googlesheets.ExtendedValue{FormulaValue: &text}})
	}
	dates, statuses := summaryReference("A2:A370"), summaryReference("C2:C370")
	work, pto := summaryReference("D2:D370"), summaryReference("E2:E370")
	label(1, 1, "Contractor Summary")
	label(3, 1, "Reporting Year:")
	label(3, 4, "Records Through:")
	formula(3, 5, `=LET(dates,`+dates+`,IF(COUNTA(dates)=0,"—",TEXT(MAX(ARRAYFORMULA(IFERROR(DATE(VALUE(LEFT(dates,4)),VALUE(MID(dates,6,2)),VALUE(RIGHT(dates,2))),0))),"mmm d, yyyy")))`)
	label(4, 1, "Today's Status:")
	formula(4, 2, `=IF(VALUE($B$3)<>YEAR(TODAY()),"—",IFERROR(SWITCH(INDEX(`+statuses+`,MATCH(TEXT(TODAY(),"yyyy-mm-dd"),`+dates+`,0)),"PENDING","Pending","FULL_DAY","Full Day","HALF_DAY","Half Day","PTO","PTO","TIME_OFF","Time Off","NO_RESPONSE","No Response","Unknown"),"No Record"))`)
	headlines := []string{"Workday Equivalent", "Full Days", "Half Days", "PTO Used", "No Responses", "Corrections"}
	metrics := []string{`=SUM(` + work + `)`, `=COUNTIF(` + statuses + `,"FULL_DAY")`, `=COUNTIF(` + statuses + `,"HALF_DAY")`, `=SUM(` + pto + `)`, `=COUNTIF(` + statuses + `,"NO_RESPONSE")`, `=COUNTIF(` + summaryReference("K2:K370") + `,TRUE)`}
	for i, labelText := range headlines {
		label(6, int64(i+1), labelText)
		formula(7, int64(i+1), metrics[i])
	}
	label(10, 1, "MONTHLY BREAKDOWN")
	for i, text := range []string{"Month", "Work Eq.", "Full", "Half", "PTO", "Time Off", "No Response"} {
		label(11, int64(i+1), text)
	}
	for month := 1; month <= 12; month++ {
		row := int64(month + 11)
		label(row, 1, time.Month(month).String())
		// ISO month-prefix wildcards operate directly on RAW strings, including
		// backfilled rows; no native date coercion or physical row ordering is needed.
		monthPattern := fmt.Sprintf(`$B$3&"-"&TEXT(%d,"00")&"-*"`, month)
		formula(row, 2, `=SUMIF(`+dates+`,`+monthPattern+`,`+work+`)`)
		formula(row, 3, `=COUNTIFS(`+statuses+`,"FULL_DAY",`+dates+`,`+monthPattern+`)`)
		formula(row, 4, `=COUNTIFS(`+statuses+`,"HALF_DAY",`+dates+`,`+monthPattern+`)`)
		formula(row, 5, `=SUMIF(`+dates+`,`+monthPattern+`,`+pto+`)`)
		formula(row, 6, `=COUNTIFS(`+statuses+`,"TIME_OFF",`+dates+`,`+monthPattern+`)`)
		formula(row, 7, `=COUNTIFS(`+statuses+`,"NO_RESPONSE",`+dates+`,`+monthPattern+`)`)
	}
	label(24, 1, "TOTAL")
	for col := 'B'; col <= 'G'; col++ {
		formula(24, int64(col-'A'+1), fmt.Sprintf("=SUM(%c12:%c23)", col, col))
	}
	label(27, 1, "PTO DAYS")
	formula(27, 4, `=IF(COUNTIF(`+statuses+`,"PTO")>15,"Overflow: "&COUNTIF(`+statuses+`,"PTO")&" PTO records. First 15 shown; see selected Daily Log for all dates.","")`)
	label(28, 1, "Date")
	label(28, 2, "Day")
	for row := int64(29); row <= 43; row++ {
		for col := int64(1); col <= 2; col++ {
			formula(row, col, fmt.Sprintf(`=IFERROR(INDEX(SORT(FILTER(%s,%s="PTO"),1,TRUE),%d,%d),"")`, summaryReference("A2:B370"), statuses, row-28, col))
		}
	}
	// Styles are attached only to owned cells. Masks preserve unrelated styles.
	for i := range cells {
		cell := &cells[i]
		row, col := cell.row+1, cell.col+1
		switch {
		case row == 1:
			cell.format = &googlesheets.CellFormat{TextFormat: &googlesheets.TextFormat{Bold: true, FontSize: 18}}
			cell.formatFields = "userEnteredFormat.textFormat.bold,userEnteredFormat.textFormat.fontSize"
		case row == 3 || row == 4 || row == 6 || row == 10 || row == 11 || row == 24 || row == 27 || row == 28:
			cell.format = &googlesheets.CellFormat{TextFormat: &googlesheets.TextFormat{Bold: true}, BackgroundColorStyle: &googlesheets.ColorStyle{RgbColor: &googlesheets.Color{Red: .91, Green: .94, Blue: .97}}}
			cell.formatFields = "userEnteredFormat.textFormat.bold,userEnteredFormat.backgroundColorStyle"
			if row == 27 && col == 4 {
				cell.format.WrapStrategy = "WRAP"
				cell.formatFields += ",userEnteredFormat.wrapStrategy"
			}
		}
		if row == 7 || (row >= 12 && row <= 24 && col >= 2) {
			if cell.format == nil {
				cell.format = &googlesheets.CellFormat{}
			}
			cell.format.NumberFormat = &googlesheets.NumberFormat{Type: "NUMBER", Pattern: "0.##"}
			if cell.formatFields != "" {
				cell.formatFields += ","
			}
			cell.formatFields += "userEnteredFormat.numberFormat"
		}
	}
	return cells
}
