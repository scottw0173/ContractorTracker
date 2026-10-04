package sheets

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/scottw0173/ContractorTracker/internal/tracker"
	googlesheets "google.golang.org/api/sheets/v4"
)

var dailyHeaders = []interface{}{"Date", "Day", "Status", "Work Fraction", "PTO Fraction", "Weekend", "Email Sent At", "Responded At", "Response Source", "Finalized At", "Changed"}

func yearlyWorksheet(year int) string { return fmt.Sprintf("Daily Log %d", year) }

func projectionValues(record tracker.DayRecord) ([]interface{}, error) {
	date, err := time.Parse(time.DateOnly, record.Date)
	if err != nil || date.Format(time.DateOnly) != record.Date || record.Year <= 0 || date.Year() != record.Year {
		return nil, fmt.Errorf("invalid record year/date: %d/%q", record.Year, record.Date)
	}
	var work interface{} = ""
	if record.WorkFraction != nil {
		work = *record.WorkFraction
	}
	return []interface{}{record.Date, date.Weekday().String(), string(record.Status), work,
		record.PTOFraction, record.IsWeekend, projectedTimestamp(record.EmailSentAt),
		projectedTimestamp(record.RespondedAt), string(record.ResponseSource),
		projectedTimestamp(record.FinalizedAt), record.HasBeenChanged}, nil
}

func projectedTimestamp(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
}

// UpsertDay projects one authoritative record into Daily Log YYYY. Summary must
// already exist. Header mismatches and duplicate date rows fail without replacing
// data. Read-then-write is not atomic across concurrent writers; no spreadsheet
// data becomes business state.
func (c *Client) UpsertDay(ctx context.Context, record tracker.DayRecord) error {
	values, err := projectionValues(record)
	if err != nil {
		return err
	}
	title := yearlyWorksheet(record.Year)
	if err := c.ensureYearlyWorksheet(ctx, title); err != nil {
		return err
	}
	if err := c.ensureHeaders(ctx, title); err != nil {
		return err
	}
	dates, err := c.Service.Spreadsheets.Values.Get(c.SpreadsheetID, "'"+title+"'!A2:A").
		ValueRenderOption("UNFORMATTED_VALUE").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("read %s dates: %w", title, err)
	}
	row := 0
	for i, cells := range dates.Values {
		if len(cells) > 0 && cells[0] == record.Date {
			if row != 0 {
				return fmt.Errorf("duplicate date %s in %s", record.Date, title)
			}
			row = i + 2
		}
	}
	body := &googlesheets.ValueRange{MajorDimension: "ROWS", Values: [][]interface{}{values}}
	if row != 0 {
		target := fmt.Sprintf("'%s'!A%d:K%d", title, row, row)
		_, err = c.Service.Spreadsheets.Values.Update(c.SpreadsheetID, target, body).
			ValueInputOption("RAW").Context(ctx).Do()
	} else {
		_, err = c.Service.Spreadsheets.Values.Append(c.SpreadsheetID, "'"+title+"'!A:K", body).
			ValueInputOption("RAW").InsertDataOption("INSERT_ROWS").Context(ctx).Do()
	}
	if err != nil {
		return fmt.Errorf("upsert %s in %s: %w", record.Date, title, err)
	}
	return nil
}

func (c *Client) ensureYearlyWorksheet(ctx context.Context, title string) error {
	titles, err := c.worksheetTitles(ctx)
	if err != nil {
		return err
	}
	if !titles["Summary"] {
		return fmt.Errorf("spreadsheet missing required worksheet: Summary")
	}
	if titles[title] {
		return nil
	}
	_, err = c.Service.Spreadsheets.BatchUpdate(c.SpreadsheetID, &googlesheets.BatchUpdateSpreadsheetRequest{
		Requests: []*googlesheets.Request{{AddSheet: &googlesheets.AddSheetRequest{Properties: &googlesheets.SheetProperties{Title: title}}}},
	}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("create %s: %w", title, err)
	}
	return nil
}

func (c *Client) ensureHeaders(ctx context.Context, title string) error {
	// Read the entire first row so unexpected nonblank columns beyond K are caught.
	target := "'" + title + "'!1:1"
	header, err := c.Service.Spreadsheets.Values.Get(c.SpreadsheetID, target).
		ValueRenderOption("FORMULA").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("read %s headers: %w", title, err)
	}
	var cells []interface{}
	if len(header.Values) > 0 {
		cells = header.Values[0]
	}
	blank := true
	for _, cell := range cells {
		if cell != "" && cell != nil {
			blank = false
		}
	}
	if !blank {
		// Sheets normally omits trailing blank cells, but accept explicit blanks too.
		for len(cells) > 0 && (cells[len(cells)-1] == "" || cells[len(cells)-1] == nil) {
			cells = cells[:len(cells)-1]
		}
		if !reflect.DeepEqual(cells, dailyHeaders) {
			return fmt.Errorf("%s header schema does not match expected Daily Log columns", title)
		}
		return nil
	}
	_, err = c.Service.Spreadsheets.Values.Update(c.SpreadsheetID, "'"+title+"'!A1:K1",
		&googlesheets.ValueRange{MajorDimension: "ROWS", Values: [][]interface{}{dailyHeaders}}).
		ValueInputOption("RAW").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("initialize %s headers: %w", title, err)
	}
	return nil
}
