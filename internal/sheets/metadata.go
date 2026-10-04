package sheets

import (
	"context"
	"fmt"
	"strings"
)

// VerifyWorksheets reads only spreadsheet metadata and requires the existing
// Daily Log and Summary worksheets. It never creates or changes worksheets.
func (c *Client) VerifyWorksheets(ctx context.Context) error {
	metadata, err := c.Service.Spreadsheets.Get(c.SpreadsheetID).
		IncludeGridData(false).Fields("sheets.properties.title").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("read spreadsheet metadata: %w", err)
	}
	titles := make(map[string]bool)
	for _, sheet := range metadata.Sheets {
		if sheet != nil && sheet.Properties != nil {
			titles[sheet.Properties.Title] = true
		}
	}
	var missing []string
	for _, title := range []string{"Daily Log", "Summary"} {
		if !titles[title] {
			missing = append(missing, title)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("spreadsheet missing required worksheets: %s", strings.Join(missing, ", "))
	}
	return nil
}
