package sheets

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/scottw0173/ContractorTracker/internal/tracker"
	googlesheets "google.golang.org/api/sheets/v4"
)

func TestYearlyPresentation(t *testing.T) {
	for _, tt := range []struct {
		name                 string
		frozen               int64
		band                 *googlesheets.GridRange
		wantFreeze, wantBand int
		wantErr              bool
	}{
		{name: "missing formatting", wantFreeze: 1, wantBand: 1},
		{name: "incorrect freeze", frozen: 2, wantFreeze: 1, wantBand: 1},
		{name: "already frozen", frozen: 1, wantBand: 1},
		{name: "exact band", frozen: 1, band: &googlesheets.GridRange{EndRowIndex: 370, EndColumnIndex: 11}},
		{name: "larger band", frozen: 1, band: &googlesheets.GridRange{EndRowIndex: 1000, EndColumnIndex: 15}},
		{name: "unbounded band", frozen: 1, band: &googlesheets.GridRange{}},
		{name: "unrelated band", frozen: 1, band: &googlesheets.GridRange{StartRowIndex: 400, EndRowIndex: 500, EndColumnIndex: 11}, wantBand: 1},
		{name: "partial overlapping band", frozen: 1, band: &googlesheets.GridRange{EndRowIndex: 100, EndColumnIndex: 11}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &sheetFixture{t: t, titles: []string{"Daily Log 2026", "Summary"}, header: dailyHeaders}
			target := f.sheet("Daily Log 2026") // ID zero also exercises zero-valued SDK fields.
			target.Properties.GridProperties.FrozenRowCount = tt.frozen
			summary := f.sheet("Summary")
			summary.Properties.GridProperties.FrozenRowCount = 3
			var original []byte
			if tt.band != nil {
				tt.band.SheetId = target.Properties.SheetId
				target.BandedRanges = []*googlesheets.BandedRange{{Range: tt.band, RowProperties: &googlesheets.BandingProperties{HeaderColor: &googlesheets.Color{Red: .7}, FirstBandColor: &googlesheets.Color{Green: .4}}}}
				original, _ = target.BandedRanges[0].MarshalJSON()
			}
			err := f.client().UpsertDay(context.Background(), tracker.DayRecord{Year: 2026, Date: "2026-10-03"})
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "overlapping") || f.bandAdds+f.freezeUpdates+f.appends+f.resizeCalls != 0 {
					t.Fatalf("unexpected partial-band handling: %v %+v", err, f)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if f.freezeUpdates != tt.wantFreeze || f.bandAdds != tt.wantBand || f.resizeCalls != 1 || target.Properties.GridProperties.FrozenRowCount != 1 {
				t.Fatalf("unexpected formatting: %+v", f)
			}
			if tt.band != nil {
				after, _ := target.BandedRanges[0].MarshalJSON()
				if !reflect.DeepEqual(original, after) {
					t.Fatal("existing band or colors changed")
				}
			}
			if summary.Properties.GridProperties.FrozenRowCount != 3 || len(summary.BandedRanges) != 0 {
				t.Fatal("Summary formatting changed")
			}
		})
	}
}

func TestPresentationRetry(t *testing.T) {
	for _, stage := range []string{"create", "initialize", "presentation", "append", "resize"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New("accepted request but lost response")
			f := &sheetFixture{t: t, titles: []string{"Summary"}, failStage: stage, failure: failure}
			c := f.client()
			record := tracker.DayRecord{Year: 2027, Date: "2027-01-01"}
			if err := c.UpsertDay(context.Background(), record); !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			f.failStage = ""
			if err := c.UpsertDay(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			target := f.sheet("Daily Log 2027")
			if f.creates != 1 || f.headerWrites != 1 || f.freezeUpdates != 1 || f.bandAdds != 1 || len(target.BandedRanges) != 1 || f.appends != 1 || len(f.rows) != 1 || f.resizeCalls < 1 {
				t.Fatalf("retry not safe: %+v", f)
			}
		})
	}
}

func TestAutoResizeAfterUpdateAndAppend(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "append", true: "update"}[existing], func(t *testing.T) {
			f := &sheetFixture{t: t, titles: []string{"Summary", "Daily Log 2026"}, header: dailyHeaders}
			if existing {
				f.rows = [][]interface{}{{"2026-10-03"}}
			}
			if err := f.client().UpsertDay(context.Background(), tracker.DayRecord{Year: 2026, Date: "2026-10-03"}); err != nil {
				t.Fatal(err)
			}
			if f.resizeCalls != 1 || f.updates+f.appends != 1 {
				t.Fatalf("missing resize: %+v", f)
			}
		})
	}
}
