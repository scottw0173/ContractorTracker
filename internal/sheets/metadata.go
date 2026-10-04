package sheets

import (
	"context"
	"fmt"
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
	metadata, err := c.Service.Spreadsheets.Get(c.SpreadsheetID).
		IncludeGridData(false).Fields("sheets.properties.title").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("read spreadsheet metadata: %w", err)
	}
	titles := make(map[string]bool)
	for _, sheet := range metadata.Sheets {
		if sheet != nil && sheet.Properties != nil {
			titles[sheet.Properties.Title] = true
		}
	}
	return titles, nil
}
