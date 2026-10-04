package sheets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/scottw0173/ContractorTracker/internal/tracker"
	"google.golang.org/api/option"
	googlesheets "google.golang.org/api/sheets/v4"
)

// sheetFixture handles the real SDK's requests without network access. Rows
// include physical gaps so date matching must preserve actual row numbers.
type sheetFixture struct {
	t                                       *testing.T
	titles                                  []string
	header                                  []interface{}
	rows                                    [][]interface{}
	creates, headerWrites, updates, appends int
	updatedRange                            string
	failStage                               string
	failure                                 error
}

func (f *sheetFixture) client() *Client {
	f.t.Helper()
	service, err := googlesheets.NewService(context.Background(), option.WithHTTPClient(&http.Client{Transport: roundTripFunc(f.request)}))
	if err != nil {
		f.t.Fatal(err)
	}
	return &Client{Service: service, SpreadsheetID: "test-id"}
}

func (f *sheetFixture) request(r *http.Request) (*http.Response, error) {
	path := r.URL.Path
	var body interface{} = map[string]interface{}{}
	stage := ""
	switch {
	case r.Method == "GET" && path == "/v4/spreadsheets/test-id":
		stage = "metadata"
		tabs := []interface{}{}
		for _, title := range f.titles {
			tabs = append(tabs, map[string]interface{}{"properties": map[string]interface{}{"title": title}})
		}
		body = map[string]interface{}{"sheets": tabs}
	case r.Method == "POST" && path == "/v4/spreadsheets/test-id:batchUpdate":
		stage = "create"
		var in googlesheets.BatchUpdateSpreadsheetRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			f.t.Fatal(err)
		}
		if len(in.Requests) != 1 || in.Requests[0].AddSheet == nil {
			f.t.Fatal("only AddSheet is permitted")
		}
		f.creates++
		f.titles = append(f.titles, in.Requests[0].AddSheet.Properties.Title)
	case r.Method == "GET" && strings.HasSuffix(path, "!1:1"):
		stage = "header"
		if r.URL.Query().Get("valueRenderOption") != "FORMULA" {
			f.t.Fatal("headers must check actual contents, including formulas")
		}
		body = map[string]interface{}{"values": [][]interface{}{f.header}}
	case r.Method == "GET" && strings.HasSuffix(path, "!A2:A"):
		stage = "dates"
		dates := [][]interface{}{}
		for _, row := range f.rows {
			if len(row) == 0 {
				dates = append(dates, []interface{}{})
			} else {
				dates = append(dates, []interface{}{row[0]})
			}
		}
		body = map[string]interface{}{"values": dates}
	case r.Method == "PUT" || (r.Method == "POST" && strings.HasSuffix(path, ":append")):
		if r.URL.Query().Get("valueInputOption") != "RAW" {
			f.t.Fatal("writes must use RAW values")
		}
		var in googlesheets.ValueRange
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			f.t.Fatal(err)
		}
		if len(in.Values) != 1 || len(in.Values[0]) != 11 {
			f.t.Fatal("writes must contain exactly one complete 11-column row")
		}
		switch {
		case strings.HasSuffix(path, "!A1:K1"):
			stage = "initialize"
			f.headerWrites++
			f.header = in.Values[0]
		case strings.HasSuffix(path, ":append"):
			stage = "append"
			if r.URL.Query().Get("insertDataOption") != "INSERT_ROWS" {
				f.t.Fatal("append must insert rather than overwrite")
			}
			f.appends++
			f.rows = append(f.rows, in.Values[0])
		default:
			stage = "update"
			f.updates++
			f.updatedRange = strings.Split(path, "/values/")[1]
			var row, end int
			if _, err := fmt.Sscanf(strings.Split(f.updatedRange, "!")[1], "A%d:K%d", &row, &end); err != nil || row != end || row < 2 || row-2 >= len(f.rows) {
				f.t.Fatalf("bad update range: %s", path)
			}
			f.rows[row-2] = in.Values[0]
		}
	default:
		f.t.Fatalf("unexpected SDK request: %s %s", r.Method, path)
	}
	if stage == f.failStage {
		return nil, f.failure
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(string(encoded))), Request: r}, nil
}

func TestYearlyWorksheet(t *testing.T) {
	for _, year := range []int{2026, 2027, 2028} {
		if got := yearlyWorksheet(year); got != fmt.Sprintf("Daily Log %d", year) {
			t.Fatal(got)
		}
	}
}

func TestUpsertDay(t *testing.T) {
	record := tracker.DayRecord{Year: 2026, Date: "2026-10-03", Status: tracker.StatusPending}
	for _, tt := range []struct {
		name                               string
		missingTab, blankHeader            bool
		rows                               [][]interface{}
		header                             []interface{}
		wantErr                            string
		creates, headers, updates, appends int
	}{
		{name: "existing date with gap", rows: [][]interface{}{{"2026-10-01", "unrelated"}, {}, {"2026-10-03", "old"}}, updates: 1},
		{name: "append missing date", rows: [][]interface{}{{"2026-10-01", "unrelated"}}, appends: 1},
		{name: "create yearly tab", missingTab: true, blankHeader: true, creates: 1, headers: 1, appends: 1},
		{name: "blank existing header", blankHeader: true, headers: 1, appends: 1},
		{name: "explicit blank cells", header: []interface{}{"", "", nil}, headers: 1, appends: 1},
		{name: "mismatched header", header: []interface{}{"Date", "Wrong"}, wantErr: "header schema"},
		{name: "extra header column", header: append(append([]interface{}{}, dailyHeaders...), "Extra"), wantErr: "header schema"},
		{name: "formula header", header: []interface{}{"=\"Date\""}, wantErr: "header schema"},
		{name: "duplicate date", rows: [][]interface{}{{"2026-10-03"}, {}, {"2026-10-03"}}, wantErr: "duplicate date"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			header := append([]interface{}{}, dailyHeaders...)
			if tt.blankHeader {
				header = nil
			}
			if tt.header != nil {
				header = tt.header
			}
			titles := []string{"Summary", "Daily Log 2026"}
			if tt.missingTab {
				titles = []string{"Summary"}
			}
			fixture := &sheetFixture{t: t, titles: titles, header: header, rows: tt.rows}
			err := fixture.client().UpsertDay(context.Background(), record)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if fixture.creates != tt.creates || fixture.headerWrites != tt.headers || fixture.updates != tt.updates || fixture.appends != tt.appends {
				t.Fatalf("unexpected writes: %+v", fixture)
			}
			if tt.name == "existing date with gap" {
				if fixture.updatedRange != "'Daily Log 2026'!A4:K4" {
					t.Fatal(fixture.updatedRange)
				}
				if !reflect.DeepEqual(fixture.rows[0], []interface{}{"2026-10-01", "unrelated"}) {
					t.Fatal("unrelated row modified")
				}
				if fixture.rows[2][3] != "" || fixture.rows[2][10] != false {
					t.Fatal("nil work and false flags not preserved")
				}
			}
		})
	}
}

func TestRepeatedUpsert(t *testing.T) {
	f := &sheetFixture{t: t, titles: []string{"Summary"}}
	c := f.client()
	record := tracker.DayRecord{Year: 2027, Date: "2027-01-01", Status: tracker.StatusPending}
	if err := c.UpsertDay(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	zero := 0.0
	record.Status, record.WorkFraction, record.PTOFraction = tracker.StatusPTO, &zero, 1
	if err := c.UpsertDay(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if err := c.UpsertDay(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	if f.creates != 1 || f.headerWrites != 1 || f.appends != 1 || f.updates != 2 || len(f.rows) != 1 {
		t.Fatalf("not idempotent: %+v", f)
	}
	if f.rows[0][2] != "PTO" || f.rows[0][3] != float64(0) || f.rows[0][4] != float64(1) {
		t.Fatalf("wrong persisted values: %v", f.rows)
	}
}

func TestProjectionValues(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 13, 14, 123456789, time.FixedZone("test", -7*3600))
	half := 0.5
	record := tracker.DayRecord{Year: 2026, Date: "2026-10-03", Status: tracker.StatusHalfDay, WorkFraction: &half, PTOFraction: 0, IsWeekend: true, EmailSentAt: at, RespondedAt: at, FinalizedAt: at, ResponseSource: tracker.ResponseSourceUser, HasBeenChanged: true}
	values, err := projectionValues(record)
	if err != nil {
		t.Fatal(err)
	}
	stamp := "2026-10-03T19:13:14.123456789Z"
	want := []interface{}{"2026-10-03", "Saturday", "HALF_DAY", 0.5, float64(0), true, stamp, stamp, "USER", stamp, true}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("got %v; want %v", values, want)
	}
	record.WorkFraction = nil
	record.EmailSentAt, record.RespondedAt, record.FinalizedAt = time.Time{}, time.Time{}, time.Time{}
	record.ResponseSource = ""
	values, err = projectionValues(record)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range []int{3, 6, 7, 8, 9} {
		if values[i] != "" {
			t.Fatalf("column %d should be blank", i)
		}
	}
	for _, date := range []string{"", "2026-1-3", "2026-02-30", "2027-10-03"} {
		record.Date = date
		if _, err := projectionValues(record); err == nil {
			t.Fatalf("accepted invalid date %q", date)
		}
	}
}

func TestProjectionFailures(t *testing.T) {
	for _, stage := range []string{"metadata", "create", "header", "initialize", "dates", "append", "update"} {
		t.Run(stage, func(t *testing.T) {
			failure := errors.New("offline failure")
			f := &sheetFixture{t: t, titles: []string{"Summary", "Daily Log 2026"}, header: dailyHeaders, failStage: stage, failure: failure}
			if stage == "create" {
				f.titles = []string{"Summary"}
			}
			if stage == "initialize" {
				f.header = nil
			}
			if stage == "update" {
				f.rows = [][]interface{}{{"2026-10-03"}}
			}
			err := f.client().UpsertDay(context.Background(), tracker.DayRecord{Year: 2026, Date: "2026-10-03"})
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	t.Run("Summary required before writes", func(t *testing.T) {
		f := &sheetFixture{t: t, titles: []string{"Daily Log 2026"}}
		err := f.client().UpsertDay(context.Background(), tracker.DayRecord{Year: 2026, Date: "2026-10-03"})
		if err == nil || !strings.Contains(err.Error(), "Summary") || f.creates+f.headerWrites+f.updates+f.appends != 0 {
			t.Fatalf("error = %v", err)
		}
	})
}
