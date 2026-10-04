package daily

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/scottw0173/ContractorTracker/internal/store"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

var _ DayStore = (*store.Store)(nil)

type memoryStore struct {
	records                   map[string]tracker.DayRecord
	getErr, putErr, createErr error
	beforePut                 func(*memoryStore, tracker.DayRecord)
	reads                     []tracker.DayRecord
	writes                    []tracker.DayRecord
	creates                   []tracker.DayRecord
}

func newMemoryStore() *memoryStore { return &memoryStore{records: make(map[string]tracker.DayRecord)} }
func (s *memoryStore) GetDay(_ context.Context, year int, date string) (tracker.DayRecord, bool, error) {
	s.reads = append(s.reads, tracker.DayRecord{Year: year, Date: date})
	if s.getErr != nil {
		return tracker.DayRecord{}, false, s.getErr
	}
	record, ok := s.records[date]
	return record, ok && record.Year == year, nil
}
func (s *memoryStore) CreateDay(_ context.Context, record tracker.DayRecord) (bool, error) {
	s.creates = append(s.creates, record)
	if s.createErr != nil {
		return false, s.createErr
	}
	if _, exists := s.records[record.Date]; exists {
		return false, nil
	}
	s.records[record.Date] = record
	return true, nil
}
func (s *memoryStore) PutDayIfStatus(_ context.Context, record tracker.DayRecord, expected tracker.Status) (bool, error) {
	s.writes = append(s.writes, record)
	if s.putErr != nil {
		return false, s.putErr
	}
	if s.beforePut != nil {
		s.beforePut(s, record)
	}
	current, exists := s.records[record.Date]
	if !exists || current.Status != expected {
		return false, nil
	}
	s.records[record.Date] = record
	return true, nil
}
func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return at
}
func mustRunner(t *testing.T, s DayStore, location *time.Location) *Runner {
	t.Helper()
	runner, err := New(s, location)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}
func mustStatus(t *testing.T, record tracker.DayRecord, status tracker.Status, at time.Time) tracker.DayRecord {
	t.Helper()
	var err error
	if status == tracker.StatusNoResponse {
		record, err = tracker.FinalizePending(record, at)
	} else if status != tracker.StatusPending {
		record, err = tracker.ApplyUserStatus(record, status, at)
	}
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestYesterdayStatuses(t *testing.T) {
	now := mustTime(t, "2026-10-04T09:00:00-07:00")
	location := time.FixedZone("app", -7*60*60)
	for _, status := range []tracker.Status{tracker.StatusPending, tracker.StatusFullDay, tracker.StatusHalfDay, tracker.StatusPTO, tracker.StatusTimeOff, tracker.StatusNoResponse} {
		t.Run(string(status), func(t *testing.T) {
			s := newMemoryStore()
			original := mustStatus(t, tracker.NewPendingDay(now.AddDate(0, 0, -1)), status, now.Add(-time.Hour))
			original.EmailSentAt = now.Add(-time.Hour)
			s.records[original.Date] = original
			if err := mustRunner(t, s, location).Run(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			want := original
			if status == tracker.StatusPending {
				var err error
				want, err = tracker.FinalizePending(original, now.UTC())
				if err != nil {
					t.Fatal(err)
				}
				if len(s.writes) != 1 || !reflect.DeepEqual(s.writes[0], want) {
					t.Fatal("pending day was not conditionally finalized")
				}
				if s.records[original.Date].FinalizedAt.Location() != time.UTC {
					t.Fatal("finalization timestamp must use UTC")
				}
			} else if len(s.writes) != 0 {
				t.Fatal("non-pending day was written")
			}
			if !reflect.DeepEqual(s.records[original.Date], want) {
				t.Fatalf("yesterday changed unexpectedly: %+v", s.records[original.Date])
			}
			today := tracker.NewPendingDay(now.In(location))
			if !reflect.DeepEqual(s.records[today.Date], today) {
				t.Fatal("today not created as pending")
			}
		})
	}
}

func TestMissingYesterday(t *testing.T) {
	s := newMemoryStore()
	now := mustTime(t, "2026-10-05T12:00:00Z")
	if err := mustRunner(t, s, time.UTC).Run(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(s.records) != 1 || len(s.writes) != 0 {
		t.Fatal("missing yesterday must not create a synthetic record")
	}
	if got, exists := s.records["2026-10-05"]; !exists || got.Status != tracker.StatusPending {
		t.Fatal("today must still be created")
	}
}

func TestConcurrentResponse(t *testing.T) {
	s := newMemoryStore()
	now := mustTime(t, "2026-10-05T12:00:00Z")
	yesterday := tracker.NewPendingDay(now.AddDate(0, 0, -1))
	s.records[yesterday.Date] = yesterday
	response := mustStatus(t, yesterday, tracker.StatusPTO, now)
	s.beforePut = func(s *memoryStore, _ tracker.DayRecord) { s.records[yesterday.Date] = response }
	if err := mustRunner(t, s, time.UTC).Run(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s.records[yesterday.Date], response) {
		t.Fatal("concurrent user response overwritten")
	}
	if len(s.reads) != 1 {
		t.Fatal("conflict should not cause a reread")
	}
	if _, exists := s.records["2026-10-05"]; !exists {
		t.Fatal("conflict prevented today's creation")
	}
}

func TestTodayAndRepeatPreserveData(t *testing.T) {
	now := mustTime(t, "2026-10-05T12:00:00Z")
	for _, preexisting := range []bool{false, true} {
		name := "initially_missing"
		if preexisting {
			name = "already_exists"
		}
		t.Run(name, func(t *testing.T) {
			s := newMemoryStore()
			yesterday := tracker.NewPendingDay(now.AddDate(0, 0, -1))
			s.records[yesterday.Date] = yesterday
			runner := mustRunner(t, s, time.UTC)
			today := tracker.NewPendingDay(now)
			responded := mustStatus(t, today, tracker.StatusFullDay, now)
			responded = mustStatus(t, responded, tracker.StatusPTO, now.Add(time.Minute))
			responded.EmailSentAt = now.Add(-time.Hour)
			if preexisting {
				s.records[today.Date] = responded
			}
			if err := runner.Run(context.Background(), now); err != nil {
				t.Fatal(err)
			}
			if preexisting {
				if !reflect.DeepEqual(s.records[today.Date], responded) {
					t.Fatal("existing today was reset")
				}
			} else {
				if !reflect.DeepEqual(s.records[today.Date], today) {
					t.Fatal("missing today not created")
				}
				s.records[today.Date] = responded
			}
			before := make(map[string]tracker.DayRecord)
			for key, value := range s.records {
				before[key] = value
			}
			if err := runner.Run(context.Background(), now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(s.records, before) {
				t.Fatal("repeat reset persisted data")
			}
			if len(s.writes) != 1 {
				t.Fatal("repeat finalized yesterday again")
			}
		})
	}
}

func TestLocalCalendarDates(t *testing.T) {
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	app := time.FixedZone("app", -7*60*60)
	for _, tc := range []struct {
		name, now, today, yesterday string
		location                    *time.Location
		weekend                     bool
	}{
		{"normal", "2026-10-06T19:00:00Z", "2026-10-06", "2026-10-05", app, false},
		{"month boundary", "2026-11-01T19:00:00Z", "2026-11-01", "2026-10-31", app, true},
		{"year boundary", "2027-01-01T19:00:00Z", "2027-01-01", "2026-12-31", app, false},
		{"UTC ahead of local", "2026-10-04T01:00:00Z", "2026-10-03", "2026-10-02", app, true},
		{"local ahead of UTC", "2026-10-02T23:00:00Z", "2026-10-03", "2026-10-02", time.FixedZone("east", 2*60*60), true},
		{"DST spring forward", "2026-03-09T04:30:00Z", "2026-03-09", "2026-03-08", newYork, false},
		{"DST fall back", "2026-11-02T04:30:00Z", "2026-11-01", "2026-10-31", newYork, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newMemoryStore()
			if err := mustRunner(t, s, tc.location).Run(context.Background(), mustTime(t, tc.now)); err != nil {
				t.Fatal(err)
			}
			previous := mustTime(t, tc.yesterday+"T00:00:00Z")
			if len(s.reads) != 1 || s.reads[0].Year != previous.Year() || s.reads[0].Date != tc.yesterday {
				t.Fatalf("incorrect yesterday lookup: %+v", s.reads)
			}
			today := mustTime(t, tc.today+"T00:00:00Z")
			record, exists := s.records[tc.today]
			if !exists || record.Year != today.Year() || record.IsWeekend != tc.weekend {
				t.Fatalf("incorrect local-day creation: %+v", record)
			}
		})
	}
}

func TestStorageErrors(t *testing.T) {
	failure := errors.New("storage failed")
	now := mustTime(t, "2026-10-05T12:00:00Z")
	for _, tc := range []struct {
		name, context             string
		getErr, putErr, createErr error
		writes, creates           int
	}{
		{"get", "get yesterday 2026-10-04", failure, nil, nil, 0, 0},
		{"conditional write", "save finalized yesterday 2026-10-04", nil, failure, nil, 1, 0},
		{"create", "create today 2026-10-05", nil, nil, failure, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newMemoryStore()
			previous := tracker.NewPendingDay(now.AddDate(0, 0, -1))
			s.records[previous.Date] = previous
			s.getErr, s.putErr, s.createErr = tc.getErr, tc.putErr, tc.createErr
			err := mustRunner(t, s, time.UTC).Run(context.Background(), now)
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), tc.context) {
				t.Fatalf("missing cause or step context: %v", err)
			}
			if len(s.writes) != tc.writes || len(s.creates) != tc.creates {
				t.Fatal("processing continued after storage error")
			}
		})
	}
}

func TestNewRequiresDependencies(t *testing.T) {
	if runner, err := New(newMemoryStore(), nil); err == nil || runner != nil {
		t.Fatal("nil timezone accepted")
	}
	if runner, err := New(nil, time.UTC); err == nil || runner != nil {
		t.Fatal("nil store accepted")
	}
}
