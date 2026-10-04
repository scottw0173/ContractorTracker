package sheets

import (
	"context"
	"fmt"

	googlesheets "google.golang.org/api/sheets/v4"
)

// ensurePresentation preserves existing qualifying bands, including their colors.
// A partial overlap requires operator attention rather than destructive repair.
func (c *Client) ensurePresentation(ctx context.Context, sheet *googlesheets.Sheet) error {
	id := sheet.Properties.SheetId
	covered, overlap := false, false
	for _, band := range sheet.BandedRanges {
		if band == nil || band.Range == nil || band.Range.SheetId != id {
			continue
		}
		r := band.Range
		if r.StartRowIndex == 0 && r.StartColumnIndex == 0 && (r.EndRowIndex == 0 || r.EndRowIndex >= 370) && (r.EndColumnIndex == 0 || r.EndColumnIndex >= 11) {
			covered = true
		}
		if r.StartRowIndex < 370 && r.StartColumnIndex < 11 {
			overlap = true
		}
	}
	if !covered && overlap {
		return fmt.Errorf("%s has existing banding overlapping but not covering A1:K370", sheet.Properties.Title)
	}
	var requests []*googlesheets.Request
	if sheet.Properties.GridProperties == nil || sheet.Properties.GridProperties.FrozenRowCount != 1 {
		requests = append(requests, &googlesheets.Request{UpdateSheetProperties: &googlesheets.UpdateSheetPropertiesRequest{
			Properties: &googlesheets.SheetProperties{SheetId: id, ForceSendFields: []string{"SheetId"}, GridProperties: &googlesheets.GridProperties{FrozenRowCount: 1}},
			Fields:     "gridProperties.frozenRowCount",
		}})
	}
	if !covered {
		color := func(red, green, blue float64) *googlesheets.ColorStyle {
			return &googlesheets.ColorStyle{RgbColor: &googlesheets.Color{Red: red, Green: green, Blue: blue}}
		}
		requests = append(requests, &googlesheets.Request{AddBanding: &googlesheets.AddBandingRequest{BandedRange: &googlesheets.BandedRange{
			Range:         &googlesheets.GridRange{SheetId: id, EndRowIndex: 370, EndColumnIndex: 11, ForceSendFields: []string{"SheetId", "StartRowIndex", "StartColumnIndex"}},
			RowProperties: &googlesheets.BandingProperties{HeaderColorStyle: color(.85, .89, .93), FirstBandColorStyle: color(1, 1, 1), SecondBandColorStyle: color(.95, .97, .99)},
		}}})
	}
	if len(requests) == 0 {
		return nil
	}
	_, err := c.Service.Spreadsheets.BatchUpdate(c.SpreadsheetID, &googlesheets.BatchUpdateSpreadsheetRequest{Requests: requests}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("ensure %s presentation: %w", sheet.Properties.Title, err)
	}
	return nil
}

func (c *Client) autoResizeColumns(ctx context.Context, id int64) error {
	_, err := c.Service.Spreadsheets.BatchUpdate(c.SpreadsheetID, &googlesheets.BatchUpdateSpreadsheetRequest{Requests: []*googlesheets.Request{{AutoResizeDimensions: &googlesheets.AutoResizeDimensionsRequest{
		Dimensions: &googlesheets.DimensionRange{SheetId: id, Dimension: "COLUMNS", EndIndex: 11, ForceSendFields: []string{"SheetId", "StartIndex"}},
	}}}}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("auto-resize Daily Log columns A:K: %w", err)
	}
	return nil
}
