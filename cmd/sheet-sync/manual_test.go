package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

type fakeDays struct {
	record tracker.DayRecord
	exists bool
	err    error
	calls  int
	year   int
	date   string
	ctx    context.Context
}

func (f *fakeDays) GetDay(ctx context.Context, year int, date string) (tracker.DayRecord, bool, error) {
	f.calls++
	f.ctx, f.year, f.date = ctx, year, date
	return f.record, f.exists, f.err
}

func TestManualProjection(t *testing.T) {
	var event Event
	if err := json.Unmarshal([]byte(`{"year":2026,"date":"2026-10-03"}`), &event); err != nil {
		t.Fatal(err)
	}
	record := tracker.DayRecord{Year: 2026, Date: "2026-10-03", Status: tracker.StatusPTO, PTOFraction: 1, HasBeenChanged: true}
	db := &fakeDays{record: record, exists: true}
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "test")
	calls := 0
	err := syncDay(ctx, event, db, func(gotCtx context.Context, got tracker.DayRecord) error {
		calls++
		if gotCtx != ctx || !reflect.DeepEqual(got, record) {
			t.Fatal("projector did not receive the exact authoritative record and context")
		}
		return nil
	})
	if err != nil || calls != 1 || db.calls != 1 || db.year != 2026 || db.date != "2026-10-03" || db.ctx != ctx {
		t.Fatalf("error=%v db=%+v project calls=%d", err, db, calls)
	}
}

func TestInvalidManualEvents(t *testing.T) {
	for _, event := range []Event{
		{}, {Year: -1, Date: "2026-10-03"}, {Year: 0, Date: "2026-10-03"},
		{Year: 2026, Date: ""}, {Year: 2026, Date: "2026-1-3"},
		{Year: 2026, Date: "2026/10/03"}, {Year: 2026, Date: "2026-02-30"},
		{Year: 2026, Date: "2027-01-01"},
	} {
		db := &fakeDays{}
		err := syncDay(context.Background(), event, db, func(context.Context, tracker.DayRecord) error { t.Fatal("invalid event projected"); return nil })
		if err == nil || db.calls != 0 {
			t.Fatalf("accepted invalid event %+v", event)
		}
	}
}

func TestManualProjectionErrors(t *testing.T) {
	failure := errors.New("storage failed")
	projectionFailure := errors.New("projection failed")
	event := Event{Year: 2026, Date: "2026-10-03"}
	for _, tt := range []struct {
		name         string
		db           fakeDays
		projectErr   error
		want         string
		cause        error
		projectCalls int
	}{
		{name: "missing day", want: "not found"},
		{name: "lookup failure", db: fakeDays{err: failure}, want: "load projection day", cause: failure},
		{name: "key mismatch", db: fakeDays{exists: true, record: tracker.DayRecord{Year: 2027, Date: event.Date}}, want: "key differs"},
		{name: "projection failure", db: fakeDays{exists: true, record: tracker.DayRecord{Year: event.Year, Date: event.Date}}, projectErr: projectionFailure, want: "project day", cause: projectionFailure, projectCalls: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			err := syncDay(context.Background(), event, &tt.db, func(context.Context, tracker.DayRecord) error { calls++; return tt.projectErr })
			if err == nil || !strings.Contains(err.Error(), tt.want) || (tt.cause != nil && !errors.Is(err, tt.cause)) || calls != tt.projectCalls {
				t.Fatalf("error=%v project calls=%d", err, calls)
			}
		})
	}
}
