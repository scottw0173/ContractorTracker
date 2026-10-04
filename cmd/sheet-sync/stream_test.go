package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

func streamNotification(name, year, date string) events.DynamoDBEventRecord {
	return events.DynamoDBEventRecord{
		EventSource: "aws:dynamodb", EventName: name,
		Change: events.DynamoDBStreamRecord{
			Keys: map[string]events.DynamoDBAttributeValue{
				"year": events.NewNumberAttribute(year), "date": events.NewStringAttribute(date),
			},
			NewImage: map[string]events.DynamoDBAttributeValue{"status": events.NewStringAttribute("PENDING")},
			OldImage: map[string]events.DynamoDBAttributeValue{"status": events.NewStringAttribute("NO_RESPONSE")},
		},
	}
}
func streamPayload(t *testing.T, records ...events.DynamoDBEventRecord) events.DynamoDBEvent {
	t.Helper()
	return events.DynamoDBEvent{Records: records}
}

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

type sequenceDays struct {
	keys    []dayKey
	records []tracker.DayRecord
	calls   int
	ctx     context.Context
}

func (s *sequenceDays) GetDay(ctx context.Context, year int, date string) (tracker.DayRecord, bool, error) {
	s.ctx = ctx
	s.keys = append(s.keys, dayKey{Year: year, Date: date})
	record := s.records[s.calls]
	s.calls++
	return record, true, nil
}

func TestStreamDeduplicatesInFirstSeenOrder(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "stream")
	current := []tracker.DayRecord{
		{Year: 2026, Date: "2026-10-03", Status: tracker.StatusPTO, PTOFraction: 1, HasBeenChanged: true},
		{Year: 2027, Date: "2027-01-01", Status: tracker.StatusFullDay},
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
	wantKeys := []dayKey{{Year: 2026, Date: "2026-10-03"}, {Year: 2027, Date: "2027-01-01"}}
	if err != nil || db.calls != 2 || db.ctx != ctx || !reflect.DeepEqual(db.keys, wantKeys) || !reflect.DeepEqual(projected, current) {
		t.Fatalf("error=%v keys=%v projected=%v", err, db.keys, projected)
	}
}

func TestIgnoredStreamRecords(t *testing.T) {
	for _, batch := range []events.DynamoDBEvent{
		{}, {Records: []events.DynamoDBEventRecord{}},
		{Records: []events.DynamoDBEventRecord{{EventSource: "aws:dynamodb", EventName: "REMOVE"}}},
	} {
		db := &fakeDays{}
		err := handleEvent(context.Background(), batch, db, func(context.Context, tracker.DayRecord) error { t.Fatal("unexpected projection"); return nil })
		if err != nil || db.calls != 0 {
			t.Fatalf("error=%v reads=%d", err, db.calls)
		}
	}
}

func TestMalformedStreamRecords(t *testing.T) {
	for _, name := range []string{"source", "event", "missing year", "wrong year type", "missing date", "wrong date type", "noninteger year", "zero year", "negative year", "mismatched date", "invalid date", "empty date", "inexact date"} {
		t.Run(name, func(t *testing.T) {
			record := streamNotification("INSERT", "2026", "2026-10-03")
			keys := record.Change.Keys
			switch name {
			case "source":
				record.EventSource = "aws:sqs"
			case "event":
				record.EventName = "UNKNOWN"
			case "missing year":
				delete(keys, "year")
			case "wrong year type":
				keys["year"] = events.NewStringAttribute("2026")
			case "missing date":
				delete(keys, "date")
			case "wrong date type":
				keys["date"] = events.NewNumberAttribute("20261003")
			case "noninteger year":
				keys["year"] = events.NewNumberAttribute("2026.5")
			case "zero year":
				keys["year"] = events.NewNumberAttribute("0")
			case "negative year":
				keys["year"] = events.NewNumberAttribute("-1")
			case "mismatched date":
				keys["date"] = events.NewStringAttribute("2027-01-01")
			case "invalid date":
				keys["date"] = events.NewStringAttribute("2026-02-30")
			case "empty date":
				keys["date"] = events.NewStringAttribute("")
			case "inexact date":
				keys["date"] = events.NewStringAttribute("2026-1-3")
			}
			db := &fakeDays{}
			err := handleEvent(context.Background(), streamPayload(t, record), db, func(context.Context, tracker.DayRecord) error { t.Fatal("invalid record projected"); return nil })
			if err == nil || db.calls != 0 {
				t.Fatalf("error=%v reads=%d", err, db.calls)
			}
		})
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
		mismatch           bool
	}{
		{name: "missing day", want: "not found"},
		{name: "key mismatch", exists: true, mismatch: true, want: "key differs"},
		{name: "storage failure", getErr: failure, want: "load projection day"},
		{name: "projection failure", exists: true, projectErr: failure, want: "project day", calls: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := &fakeDays{record: tracker.DayRecord{Year: 2026, Date: "2026-10-03"}, exists: tt.exists, err: tt.getErr}
			if tt.mismatch {
				db.record.Year = 2027
			}
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
		if i == 1 {
			db.record.Status = tracker.StatusFullDay
			db.record.PTOFraction = 0
		}
		if err := handleEvent(context.Background(), raw, db, project); err != nil {
			t.Fatal(err)
		}
	}
	if db.calls != 2 || len(got) != 2 || got[0].Status != tracker.StatusPTO || got[1].Status != tracker.StatusFullDay {
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

func TestStreamRepeatedKeysSyncOncePerInvocation(t *testing.T) {
	for _, names := range [][]string{
		{"INSERT", "INSERT", "INSERT"},
		{"MODIFY", "MODIFY", "MODIFY"},
		{"INSERT", "MODIFY", "MODIFY"},
		{"REMOVE", "INSERT", "REMOVE", "MODIFY", "REMOVE"},
	} {
		t.Run(strings.Join(names, "-"), func(t *testing.T) {
			notifications := make([]events.DynamoDBEventRecord, 0, len(names))
			for _, name := range names {
				notifications = append(notifications, streamNotification(name, "2026", "2026-10-03"))
			}
			db := &fakeDays{exists: true, record: tracker.DayRecord{Year: 2026, Date: "2026-10-03", Status: tracker.StatusPTO}}
			projects := 0
			raw := streamPayload(t, notifications...)
			for invocation := 1; invocation <= 2; invocation++ {
				err := handleEvent(context.Background(), raw, db, func(context.Context, tracker.DayRecord) error { projects++; return nil })
				if err != nil || db.calls != invocation || projects != invocation {
					t.Fatalf("invocation=%d error=%v reads=%d projects=%d", invocation, err, db.calls, projects)
				}
			}
		})
	}
}

func TestStreamValidatesWholeBatchBeforeSync(t *testing.T) {
	raw := streamPayload(t,
		streamNotification("INSERT", "2026", "2026-10-03"),
		streamNotification("MODIFY", "2026", "2026-10-03"),
		streamNotification("MODIFY", "2026", "2027-01-01"))
	db := &fakeDays{}
	err := handleEvent(context.Background(), raw, db, func(context.Context, tracker.DayRecord) error {
		t.Fatal("invalid batch must not project any key")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "stream record 2 keys") || db.calls != 0 {
		t.Fatalf("error=%v reads=%d", err, db.calls)
	}
}

type fakeProjector struct {
	calls int
	err   error
}

func (p *fakeProjector) UpsertDay(context.Context, tracker.DayRecord) error {
	p.calls++
	return p.err
}

func TestTypedHandlerLazyInvocationClient(t *testing.T) {
	db := &fakeDays{exists: true, record: tracker.DayRecord{Year: 2026, Date: "2026-10-03"}}
	var clients []*fakeProjector
	handler := streamHandler(db, func(context.Context) (dayProjector, error) {
		client := &fakeProjector{}
		clients = append(clients, client)
		return client, nil
	})
	// A typed handler is directly suitable for Lambda's runtime deserialization.
	var typed func(context.Context, events.DynamoDBEvent) error = handler
	for _, batch := range []events.DynamoDBEvent{{}, streamPayload(t, streamNotification("REMOVE", "", ""))} {
		if err := typed(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
	}
	if len(clients) != 0 || db.calls != 0 {
		t.Fatal("empty/REMOVE-only batches initialized Google or read DynamoDB")
	}
	batch := streamPayload(t, streamNotification("INSERT", "2026", "2026-10-03"), streamNotification("MODIFY", "2026", "2026-10-04"))
	// The reader returns the requested authoritative key for each projection.
	reader := &sequenceDays{records: []tracker.DayRecord{
		{Year: 2026, Date: "2026-10-03"}, {Year: 2026, Date: "2026-10-04"},
		{Year: 2026, Date: "2026-10-03"}, {Year: 2026, Date: "2026-10-04"},
	}}
	handler = streamHandler(reader, func(context.Context) (dayProjector, error) {
		client := &fakeProjector{}
		clients = append(clients, client)
		return client, nil
	})
	for i := 0; i < 2; i++ {
		if err := handler(context.Background(), batch); err != nil {
			t.Fatal(err)
		}
	}
	if len(clients) != 2 || clients[0].calls != 2 || clients[1].calls != 2 {
		t.Fatal("client must be reused within, but not across, invocations")
	}
}

func TestWholeBatchRetryRepeatsEarlierSuccess(t *testing.T) {
	a := tracker.DayRecord{Year: 2026, Date: "2026-10-03"}
	b := tracker.DayRecord{Year: 2026, Date: "2026-10-04"}
	db := &sequenceDays{records: []tracker.DayRecord{a, b, a, b}}
	batch := streamPayload(t, streamNotification("INSERT", "2026", a.Date), streamNotification("MODIFY", "2026", b.Date))
	failure := errors.New("temporary Google failure")
	var dates []string
	project := func(_ context.Context, record tracker.DayRecord) error {
		dates = append(dates, record.Date)
		if len(dates) == 2 {
			return failure
		}
		return nil
	}
	if err := handleEvent(context.Background(), batch, db, project); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := handleEvent(context.Background(), batch, db, project); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dates, []string{a.Date, b.Date, a.Date, b.Date}) || db.calls != 4 {
		t.Fatalf("reads=%d dates=%v", db.calls, dates)
	}
}

func TestLazyClientFailurePropagates(t *testing.T) {
	failure := errors.New("credential initialization failed")
	db := &fakeDays{exists: true, record: tracker.DayRecord{Year: 2026, Date: "2026-10-03"}}
	handler := streamHandler(db, func(context.Context) (dayProjector, error) { return nil, failure })
	err := handler(context.Background(), streamPayload(t, streamNotification("INSERT", "2026", "2026-10-03")))
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "project day") {
		t.Fatal(err)
	}
}
