package sheets

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/scottw0173/ContractorTracker/internal/tracker"
	googlesheets "google.golang.org/api/sheets/v4"
)

func TestReportingYears(t *testing.T) {
	for _, tt := range []struct {
		name         string
		titles, want []string
	}{
		{"one", []string{"Summary", "Daily Log 2026"}, []string{"2026"}},
		{"sorted exact matches", []string{"Daily Log 2028", "Daily Log 2026", "Daily Log 2027", "Daily Log", "Daily Log 0000", "Daily Log 26", "Daily Log 2026 extra", " Daily Log 2025", "Daily Log 2025 ", "Daily Log -001", "Daily Log abcd", "Summary"}, []string{"2026", "2027", "2028"}},
		{"positive four digit", []string{"Daily Log 0001", "Daily Log 9999"}, []string{"0001", "9999"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sheets := make(map[string]*googlesheets.Sheet)
			for _, title := range tt.titles {
				sheets[title] = &googlesheets.Sheet{}
			}
			if got := reportingYears(sheets); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("years = %v; want %v", got, tt.want)
			}
		})
	}
}

func setSummaryString(f *sheetFixture, row, col int64, value string) {
	f.summaryCell(row-1, col-1).UserEnteredValue = &googlesheets.ExtendedValue{StringValue: &value}
}
func summaryText(f *sheetFixture, row, col int64) string {
	v := f.summaryCell(row-1, col-1).UserEnteredValue
	if v == nil {
		return ""
	}
	if v.FormulaValue != nil {
		return *v.FormulaValue
	}
	return selectedYear(v)
}

func TestSummaryYearSelection(t *testing.T) {
	for _, tt := range []struct {
		name, selected, want string
	}{
		{name: "blank newest", want: "2028"},
		{name: "invalid newest", selected: "2019", want: "2028"},
		{name: "valid past retained", selected: "2026", want: "2026"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &sheetFixture{t: t, titles: []string{"Daily Log 2028", "Summary", "Daily Log 2026", "Daily Log 2027"}}
			setSummaryString(f, 3, 2, tt.selected)
			if err := f.client().ensureSummary(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := summaryText(f, 3, 2); got != tt.want {
				t.Fatalf("selection = %s; want %s", got, tt.want)
			}
			rule := f.summaryCell(2, 1).DataValidation
			if !rule.Strict || !rule.ShowCustomUi || rule.Condition.Type != "ONE_OF_LIST" {
				t.Fatal("missing explicit strict dropdown")
			}
			var years []string
			for _, v := range rule.Condition.Values {
				years = append(years, v.UserEnteredValue)
			}
			if !reflect.DeepEqual(years, []string{"2026", "2027", "2028"}) {
				t.Fatal(years)
			}
		})
	}
	t.Run("numeric valid selection preserved", func(t *testing.T) {
		f := &sheetFixture{t: t, titles: []string{"Summary", "Daily Log 2026", "Daily Log 2027"}}
		n := float64(2026)
		f.summaryCell(2, 1).UserEnteredValue = &googlesheets.ExtendedValue{NumberValue: &n}
		if err := f.client().ensureSummary(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.summaryCell(2, 1).UserEnteredValue.NumberValue == nil || summaryText(f, 3, 2) != "2026" {
			t.Fatal("numeric past year replaced")
		}
	})
}

func TestSummaryNewAnnualTabAndLaterUpsert(t *testing.T) {
	f := &sheetFixture{t: t, titles: []string{"Summary", "Daily Log 2026", "Daily Log 2028"}, header: dailyHeaders}
	setSummaryString(f, 3, 2, "2026")
	c := f.client()
	record := tracker.DayRecord{Year: 2027, Date: "2027-01-01"}
	if err := c.UpsertDay(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 || summaryText(f, 3, 2) != "2027" {
		t.Fatal("new year not selected")
	}
	setSummaryString(f, 3, 2, "2026")
	if err := c.UpsertDay(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if summaryText(f, 3, 2) != "2026" || f.creates != 1 || f.appends != 1 || f.updates != 1 {
		t.Fatal("ordinary upsert reset selection or projection")
	}
}

func TestSummaryFormulaContract(t *testing.T) {
	f := &sheetFixture{t: t, titles: []string{"Summary", "Daily Log 2026"}}
	if err := f.client().ensureSummary(context.Background()); err != nil {
		t.Fatal(err)
	}
	records := summaryText(f, 3, 5)
	for _, part := range []string{"MAX(", "ARRAYFORMULA(", "LEFT(dates,4)", "MID(dates,6,2)", "RIGHT(dates,2)", "COUNTA(dates)=0", `"mmm d, yyyy"`} {
		if !strings.Contains(records, part) {
			t.Fatalf("Records Through missing %s: %s", part, records)
		}
	}
	today := summaryText(f, 4, 2)
	for _, part := range []string{"$B$3", "YEAR(TODAY())", "MATCH(TEXT(TODAY()", `"No Record"`, `"—"`} {
		if !strings.Contains(today, part) {
			t.Fatal(today)
		}
	}
	for _, tt := range []struct {
		col                              int64
		label, operation, column, status string
	}{
		{1, "Workday Equivalent", "SUM(", "D2:D", ""},
		{2, "Full Days", "COUNTIF(", "C2:C", "FULL_DAY"},
		{3, "Half Days", "COUNTIF(", "C2:C", "HALF_DAY"},
		{4, "PTO Used", "SUM(", "E2:E", ""},
		{5, "No Responses", "COUNTIF(", "C2:C", "NO_RESPONSE"},
		{6, "Corrections", "COUNTIF(", "K2:K", "TRUE"},
	} {
		if summaryText(f, 6, tt.col) != tt.label {
			t.Fatal("wrong headline")
		}
		formula := summaryText(f, 7, tt.col)
		for _, part := range []string{tt.operation, tt.column, tt.status, "$B$3"} {
			if !strings.Contains(formula, part) {
				t.Fatal(formula)
			}
		}
		for _, forbidden := range []string{"F2:F", "I2:I", "J2:J", "AUTO_FINALIZE", "LATE_USER", "PENDING"} {
			if strings.Contains(formula, forbidden) {
				t.Fatalf("historical/weekend filtering in %s", formula)
			}
		}
	}
	headers := []string{"Month", "Work Eq.", "Full", "Half", "PTO", "Time Off", "No Response"}
	for i, label := range headers {
		if summaryText(f, 11, int64(i+1)) != label {
			t.Fatal("wrong monthly columns")
		}
	}
	months := []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
	for i, month := range months {
		row := int64(12 + i)
		if summaryText(f, row, 1) != month {
			t.Fatal("missing month")
		}
		for col := int64(2); col <= 7; col++ {
			formula := summaryText(f, row, col)
			for _, part := range []string{fmt.Sprintf(`TEXT(%d,"00")`, i+1), `"-*"`, "$B$3", "A2:A"} {
				if !strings.Contains(formula, part) {
					t.Fatal(formula)
				}
			}
			if strings.Contains(formula, "PENDING") || strings.Contains(formula, "F2:F") {
				t.Fatal("unexpected monthly classification")
			}
		}
		if !strings.Contains(summaryText(f, row, 2), "D2:D") || !strings.Contains(summaryText(f, row, 5), "E2:E") || !strings.Contains(summaryText(f, row, 6), "TIME_OFF") || !strings.Contains(summaryText(f, row, 7), "NO_RESPONSE") {
			t.Fatal("wrong monthly metric")
		}
	}
	if summaryText(f, 24, 1) != "TOTAL" {
		t.Fatal("missing total")
	}
	for col := 'B'; col <= 'G'; col++ {
		if got := summaryText(f, 24, int64(col-'A'+1)); got != fmt.Sprintf("=SUM(%c12:%c23)", col, col) {
			t.Fatal(got)
		}
	}
	if summaryText(f, 28, 1) != "Date" || summaryText(f, 28, 2) != "Day" {
		t.Fatal("wrong PTO headers")
	}
	for row := int64(29); row <= 43; row++ {
		for col := int64(1); col <= 2; col++ {
			formula := summaryText(f, row, col)
			for _, part := range []string{"SORT(FILTER(", "A2:B", "C2:C", `="PTO"`, "1,TRUE", fmt.Sprintf(",%d,%d)", row-28, col), `IFERROR(`} {
				if !strings.Contains(formula, part) {
					t.Fatal(formula)
				}
			}
		}
	}
	if f.summaryCell(43, 0).UserEnteredValue != nil {
		t.Fatal("PTO data extends past 15 rows")
	}
	overflow := summaryText(f, 27, 4)
	if !strings.Contains(overflow, ">15") || !strings.Contains(overflow, "Overflow:") || !strings.Contains(overflow, "see selected Daily Log for all dates") {
		t.Fatal("missing visible overflow")
	}
}

func TestSummaryInitializationSafety(t *testing.T) {
	t.Run("idempotent and unrelated cells retained", func(t *testing.T) {
		f := &sheetFixture{t: t, titles: []string{"Summary", "Daily Log 2026"}}
		setSummaryString(f, 40, 7, "unrelated within inspected range")
		setSummaryString(f, 50, 8, "unrelated outside layout")
		c := f.client()
		if err := c.ensureSummary(context.Background()); err != nil {
			t.Fatal(err)
		}
		before := []int{f.summaryValueWrites, f.summaryFormatWrites, f.summaryValidationWrites}
		if err := c.ensureSummary(context.Background()); err != nil {
			t.Fatal(err)
		}
		after := []int{f.summaryValueWrites, f.summaryFormatWrites, f.summaryValidationWrites}
		if !reflect.DeepEqual(before, after) || f.summaryResizes != 2 {
			t.Fatalf("unnecessary initialization writes: %v -> %v", before, after)
		}
		if summaryText(f, 40, 7) != "unrelated within inspected range" || summaryText(f, 50, 8) != "unrelated outside layout" {
			t.Fatal("unrelated content overwritten")
		}
		if f.summaryCell(10, 0).UserEnteredFormat.TextFormat.Bold != true || f.summaryCell(27, 0).UserEnteredFormat.TextFormat.Bold != true || f.summaryCell(0, 0).UserEnteredFormat.TextFormat.FontSize != 18 {
			t.Fatal("missing presentation")
		}
	})
	for _, tt := range []struct {
		name     string
		row, col int64
		value    string
	}{
		{"label", 1, 1, "Unexpected title"},
		{"formula", 7, 1, "123"},
		{"PTO list", 43, 2, "Unexpected note"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &sheetFixture{t: t, titles: []string{"Summary", "Daily Log 2026"}}
			setSummaryString(f, tt.row, tt.col, tt.value)
			err := f.client().ensureSummary(context.Background())
			if err == nil || !strings.Contains(err.Error(), "incompatible Summary structure") || f.summaryValueWrites+f.summaryFormatWrites+f.summaryValidationWrites+f.summaryResizes != 0 {
				t.Fatalf("unsafe initialization: %v", err)
			}
		})
	}
	t.Run("partial compatible initialization completes", func(t *testing.T) {
		f := &sheetFixture{t: t, titles: []string{"Summary", "Daily Log 2026"}}
		setSummaryString(f, 1, 1, "Contractor Summary")
		if err := f.client().ensureSummary(context.Background()); err != nil {
			t.Fatal(err)
		}
		if summaryText(f, 6, 1) != "Workday Equivalent" {
			t.Fatal("missing remaining layout")
		}
	})
}

func TestSummaryRetryAfterAmbiguousWrite(t *testing.T) {
	failure := errors.New("lost Summary response")
	f := &sheetFixture{t: t, titles: []string{"Summary", "Daily Log 2026"}, header: dailyHeaders, failStage: "summaryWrite", failure: failure}
	c := f.client()
	record := tracker.DayRecord{Year: 2026, Date: "2026-10-03"}
	if err := c.UpsertDay(context.Background(), record); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	writes := f.summaryValueWrites
	f.failStage = ""
	if err := c.UpsertDay(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if f.summaryValueWrites != writes || f.appends != 1 || f.updates != 1 {
		t.Fatal("retry recreated layout or row")
	}
}

func TestNewYearSelectionSurvivesFailedProjection(t *testing.T) {
	for _, stage := range []string{"create", "initialize", "presentation", "append", "resize", "summaryRead"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New("failed invocation")
			f := &sheetFixture{t: t, titles: []string{"Summary", "Daily Log 2026"}}
			c := f.client()
			if err := c.ensureSummary(context.Background()); err != nil {
				t.Fatal(err)
			}
			f.failStage, f.failure = stage, failure
			record := tracker.DayRecord{Year: 2027, Date: "2027-01-01"}
			if err := c.UpsertDay(context.Background(), record); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			// Fake failures happen after acceptance, including the atomic create batch.
			if summaryText(f, 3, 2) != "2027" {
				t.Fatal("tab created without year selection")
			}
			f.failStage = ""
			if err := c.UpsertDay(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			if f.creates != 1 || summaryText(f, 3, 2) != "2027" {
				t.Fatal("retry lost new-year selection")
			}
			setSummaryString(f, 3, 2, "2026")
			if err := c.UpsertDay(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			if summaryText(f, 3, 2) != "2026" {
				t.Fatal("later invocation overrides past year")
			}
		})
	}
}
