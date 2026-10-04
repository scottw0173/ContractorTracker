package sheets

import (
	"context"
	"fmt"
	googlesheets "google.golang.org/api/sheets/v4"
)

// VerifyWorksheets checks the required existing Summary worksheet. Yearly Daily
// Log worksheets are created on demand by UpsertDay; an unyearly tab is unused.
func (c *Client) VerifyWorksheets(ctx context.Context) error {
	titles, err := c.worksheetTitles(ctx)
	if err != nil {
		return err
	}
	if !titles["Summary"] {
		return fmt.Errorf("spreadsheet missing required worksheet: Summary")
	}
	return nil
}

func (c *Client) worksheetTitles(ctx context.Context) (map[string]bool, error) {
	worksheets, err := c.worksheetMetadata(ctx)
	if err != nil {
		return nil, err
	}
	titles := make(map[string]bool)
	for title := range worksheets {
		titles[title] = true
	}
	return titles, nil
}

const worksheetMetadataFields = "sheets(properties(sheetId,title,gridProperties(frozenRowCount)),bandedRanges(range))"

func (c *Client) worksheetMetadata(ctx context.Context) (map[string]*googlesheets.Sheet, error) {
	metadata, err := c.Service.Spreadsheets.Get(c.SpreadsheetID).
		IncludeGridData(false).Fields(worksheetMetadataFields).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("read spreadsheet metadata: %w", err)
	}
	worksheets := make(map[string]*googlesheets.Sheet)
	for _, sheet := range metadata.Sheets {
		if sheet != nil && sheet.Properties != nil {
			worksheets[sheet.Properties.Title] = sheet
		}
	}
	return worksheets, nil
}
