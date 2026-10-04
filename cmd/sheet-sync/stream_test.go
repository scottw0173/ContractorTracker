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

func streamNotification(name, year, date string) map[string]interface{} {
	return map[string]interface{}{
		"eventSource": "aws:dynamodb", "eventName": name,
		"dynamodb": map[string]interface{}{
			"Keys": map[string]interface{}{"year": map[string]string{"N": year}, "date": map[string]string{"S": date}},
			// Deliberately stale images must never become projected business state.
			"NewImage": map[string]interface{}{"status": map[string]string{"S": "PENDING"}},
			"OldImage": map[string]interface{}{"status": map[string]string{"S": "NO_RESPONSE"}},
		},
	}
}

func streamPayload(t *testing.T, records ...map[string]interface{}) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]interface{}{"Records": records})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type sequenceDays struct {
	keys    []Event
	records []tracker.DayRecord
	calls   int
	ctx     context.Context
}

func (s *sequenceDays) GetDay(ctx context.Context, year int, date string) (tracker.DayRecord, bool, error) {
	s.ctx = ctx
	s.keys = append(s.keys, Event{Year: year, Date: date})
	record := s.records[s.calls]
	s.calls++
	return record, true, nil
}

func TestStreamRereadsEveryAffectedKey(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "stream")
	current := []tracker.DayRecord{
		{Year: 2026, Date: "2026-10-03", Status: tracker.StatusPTO, PTOFraction: 1, HasBeenChanged: true},
		{Year: 2027, Date: "2027-01-01", Status: tracker.StatusFullDay},
		{Year: 2026, Date: "2026-10-03", Status: tracker.StatusHalfDay, HasBeenChanged: true},
	}
	db := &sequenceDays{records: current}
	raw := streamPayload(t, streamNotification("INSERT", "2026", "2026-10-03"), streamNotification("MODIFY", "2027", "2027-01-01"), streamNotification("MODIFY", "2026", "2026-10-03"))
	var projected []tracker.DayRecord
	err := handleEvent(ctx, raw, db, func(got context.Context, record tracker.DayRecord) error {
		if got != ctx {
			t.Fatal("project context lost")
		}
		projected = append(projected, record)
		return nil
	})
	wantKeys := []Event{{Year: 2026, Date: "2026-10-03"}, {Year: 2027, Date: "2027-01-01"}, {Year: 2026, Date: "2026-10-03"}}
	if err != nil || db.calls != 3 || db.ctx != ctx || !reflect.DeepEqual(db.keys, wantKeys) || !reflect.DeepEqual(projected, current) {
		t.Fatalf("error=%v keys=%v projected=%v", err, db.keys, projected)
	}
}

func TestManualEventDispatch(t *testing.T) {
	db := &fakeDays{exists: true, record: tracker.DayRecord{Year: 2026, Date: "2026-10-03", Status: tracker.StatusTimeOff}}
	calls := 0
	err := handleEvent(context.Background(), json.RawMessage(`{"year":2026,"date":"2026-10-03"}`), db, func(_ context.Context, record tracker.DayRecord) error {
		calls++
		if record.Status != tracker.StatusTimeOff {
			t.Fatal("wrong record")
		}
		return nil
	})
	if err != nil || calls != 1 || db.calls != 1 {
		t.Fatalf("error=%v calls=%d reads=%d", err, calls, db.calls)
	}
}

func TestIgnoredStreamRecords(t *testing.T) {
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"Records":[]}`),
		// REMOVE needs neither a key lookup nor a spreadsheet deletion.
		json.RawMessage(`{"Records":[{"eventSource":"aws:dynamodb","eventName":"REMOVE"}]}`),
	} {
		db := &fakeDays{}
		err := handleEvent(context.Background(), raw, db, func(context.Context, tracker.DayRecord) error { t.Fatal("unexpected projection"); return nil })
		if err != nil || db.calls != 0 {
			t.Fatalf("error=%v reads=%d", err, db.calls)
		}
	}
}

func TestMalformedProjectionEvents(t *testing.T) {
	invalid := []string{
		"", "{", "null", "[]", `{}`, `{"year":2026,"date":"2027-01-01"}`,
		`{"year":"2026","date":"2026-10-03"}`,
		`{"Records":null}`, `{"Records":{}}`, `{"Records":"bad"}`,
		`{"Records":[],"year":2026}`, `{"Records":[],"date":"2026-10-03"}`,
		`{"Records":[{"eventSource":"aws:sqs","eventName":"INSERT"}]}`,
		`{"Records":[{"eventSource":"aws:dynamodb","eventName":"UNKNOWN"}]}`,
	}
	for _, name := range []string{"missing year", "wrong year type", "missing date", "wrong date type", "noninteger year", "zero year", "mismatched date", "invalid date"} {
		notification := streamNotification("INSERT", "2026", "2026-10-03")
		keys := notification["dynamodb"].(map[string]interface{})["Keys"].(map[string]interface{})
		switch name {
		case "missing year":
			delete(keys, "year")
		case "wrong year type":
			keys["year"] = map[string]string{"S": "2026"}
		case "missing date":
			delete(keys, "date")
		case "wrong date type":
			keys["date"] = map[string]string{"N": "20261003"}
		case "noninteger year":
			keys["year"] = map[string]string{"N": "2026.5"}
		case "zero year":
			keys["year"] = map[string]string{"N": "0"}
		case "mismatched date":
			keys["date"] = map[string]string{"S": "2027-01-01"}
		case "invalid date":
			keys["date"] = map[string]string{"S": "2026-02-30"}
		}
		invalid = append(invalid, string(streamPayload(t, notification)))
	}
	for _, raw := range invalid {
		db := &fakeDays{}
		err := handleEvent(context.Background(), json.RawMessage(raw), db, func(context.Context, tracker.DayRecord) error { t.Fatal("invalid event projected"); return nil })
		if err == nil || db.calls != 0 {
			t.Fatalf("accepted %s: err=%v reads=%d", raw, err, db.calls)
		}
	}
}

func TestStreamBatchFailures(t *testing.T) {
	failure := errors.New("offline failure")
	raw := streamPayload(t, streamNotification("INSERT", "2026", "2026-10-03"), streamNotification("MODIFY", "2026", "2026-10-03"))
	for _, tt := range []struct {
		name               string
		exists             bool
		getErr, projectErr error
		want               string
		calls              int
	}{
		{name: "missing day", want: "not found"},
		{name: "storage failure", getErr: failure, want: "load projection day"},
		{name: "projection failure", exists: true, projectErr: failure, want: "project day", calls: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeDays{record: tracker.DayRecord{Year: 2026, Date: "2026-10-03"}, exists: tt.exists, err: tt.getErr}
			calls := 0
			err := handleEvent(context.Background(), raw, db, func(context.Context, tracker.DayRecord) error { calls++; return tt.projectErr })
			if err == nil || !strings.Contains(err.Error(), "stream record 0") || !strings.Contains(err.Error(), tt.want) || db.calls != 1 || calls != tt.calls {
				t.Fatalf("error=%v reads=%d projects=%d", err, db.calls, calls)
			}
			if (tt.getErr != nil || tt.projectErr != nil) && !errors.Is(err, failure) {
				t.Fatalf("cause lost: %v", err)
			}
		})
	}
}

func TestRepeatedStreamDelivery(t *testing.T) {
	db := &fakeDays{exists: true, record: tracker.DayRecord{Year: 2026, Date: "2026-10-03", Status: tracker.StatusPTO, PTOFraction: 1}}
	raw := streamPayload(t, streamNotification("MODIFY", "2026", "2026-10-03"))
	var got []tracker.DayRecord
	project := func(_ context.Context, record tracker.DayRecord) error { got = append(got, record); return nil }
	for i := 0; i < 2; i++ {
		if err := handleEvent(context.Background(), raw, db, project); err != nil {
			t.Fatal(err)
		}
	}
	if db.calls != 2 || len(got) != 2 || !reflect.DeepEqual(got[0], got[1]) {
		t.Fatal("redelivery must reread and project current state")
	}
}

func TestStreamFailureAfterSuccessfulRecord(t *testing.T) {
	records := []tracker.DayRecord{
		{Year: 2026, Date: "2026-10-03", Status: tracker.StatusPTO},
		{Year: 2026, Date: "2026-10-04", Status: tracker.StatusFullDay},
	}
	db := &sequenceDays{records: records}
	failure := errors.New("second projection failed")
	raw := streamPayload(t,
		streamNotification("INSERT", "2026", "2026-10-03"),
		streamNotification("MODIFY", "2026", "2026-10-04"),
		streamNotification("INSERT", "2026", "2026-10-05"))
	calls := 0
	err := handleEvent(context.Background(), raw, db, func(_ context.Context, record tracker.DayRecord) error {
		calls++
		if record.Date == "2026-10-04" {
			return failure
		}
		return nil
	})
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "stream record 1") || db.calls != 2 || calls != 2 {
		t.Fatalf("error=%v reads=%d projections=%d", err, db.calls, calls)
	}
}
