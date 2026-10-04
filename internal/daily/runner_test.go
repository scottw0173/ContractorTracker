package daily

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/scottw0173/ContractorTracker/internal/email"
	"github.com/scottw0173/ContractorTracker/internal/store"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

var _ DayStore = (*store.Store)(nil)
var _ MessageBuilder = (*email.Builder)(nil)
var _ MessageSender = (*email.Sender)(nil)

type memoryStore struct {
	markErr                   error
	markConflict              bool
	missingToday              bool
	todayGetErr               error
	marks                     []tracker.DayRecord
	beforeMark                func(*memoryStore)
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
	if len(s.creates) > 0 && s.todayGetErr != nil {
		return tracker.DayRecord{}, false, s.todayGetErr
	}
	if len(s.creates) > 0 && s.missingToday {
		return tracker.DayRecord{}, false, nil
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
	runner, err := New(s, &fakeBuilder{}, &fakeSender{}, location)
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
			today.EmailSentAt = now.UTC()
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
				today.EmailSentAt = now.UTC()
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
	if runner, err := New(newMemoryStore(), nil, &fakeSender{}, time.UTC); err == nil || runner != nil {
		t.Fatal("nil builder accepted")
	}
	if runner, err := New(newMemoryStore(), &fakeBuilder{}, nil, time.UTC); err == nil || runner != nil {
		t.Fatal("nil sender accepted")
	}
	if runner, err := New(newMemoryStore(), &fakeBuilder{}, &fakeSender{}, nil); err == nil || runner != nil {
		t.Fatal("nil timezone accepted")
	}
	if runner, err := New(nil, &fakeBuilder{}, &fakeSender{}, time.UTC); err == nil || runner != nil {
		t.Fatal("nil store accepted")
	}
}

func (s *memoryStore) MarkEmailSent(_ context.Context, year int, date string, at time.Time) (bool, error) {
	s.marks = append(s.marks, tracker.DayRecord{Year: year, Date: date, EmailSentAt: at})
	if s.markErr != nil {
		return false, s.markErr
	}
	if s.beforeMark != nil {
		s.beforeMark(s)
	}
	current, exists := s.records[date]
	if s.markConflict || !exists || current.Year != year || !current.EmailSentAt.IsZero() {
		return false, nil
	}
	current.EmailSentAt = at
	s.records[date] = current
	return true, nil
}

type fakeBuilder struct {
	dates   []string
	message email.Message
	err     error
}

func (b *fakeBuilder) Build(date string) (email.Message, error) {
	b.dates = append(b.dates, date)
	return b.message, b.err
}

type fakeSender struct {
	messages []email.Message
	err      error
	ctx      context.Context
}

func (s *fakeSender) Send(ctx context.Context, message email.Message) error {
	s.ctx = ctx
	s.messages = append(s.messages, message)
	return s.err
}

func TestPromptDecisions(t *testing.T) {
	now := mustTime(t, "2026-10-05T12:00:00-07:00")
	for _, tc := range []struct {
		name          string
		exists        bool
		status        tracker.Status
		emailed, send bool
	}{
		{"new day", false, tracker.StatusPending, false, true},
		{"existing pending", true, tracker.StatusPending, false, true},
		{"already emailed", true, tracker.StatusPending, true, false},
		{"full day", true, tracker.StatusFullDay, false, false},
		{"half day", true, tracker.StatusHalfDay, false, false},
		{"PTO", true, tracker.StatusPTO, false, false},
		{"time off", true, tracker.StatusTimeOff, false, false},
		{"no response", true, tracker.StatusNoResponse, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newMemoryStore()
			original := mustStatus(t, tracker.NewPendingDay(now), tc.status, now)
			if tc.emailed {
				original.EmailSentAt = now.Add(-time.Hour)
			}
			if tc.exists {
				s.records[original.Date] = original
			}
			message := email.Message{Subject: "subject", TextBody: "text", HTMLBody: "<p>HTML</p>"}
			b, send := &fakeBuilder{message: message}, &fakeSender{}
			runner, err := New(s, b, send, now.Location())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := runner.Run(ctx, now); err != nil {
				t.Fatal(err)
			}
			reads := 1
			if tc.exists {
				reads = 2
			}
			if len(s.reads) != reads || (tc.exists && (s.reads[1].Year != 2026 || s.reads[1].Date != "2026-10-05")) {
				t.Fatal("existing day not reread")
			}
			if tc.send {
				if !reflect.DeepEqual(b.dates, []string{"2026-10-05"}) || !reflect.DeepEqual(send.messages, []email.Message{message}) || send.ctx != ctx {
					t.Fatal("incorrect build/send")
				}
				want := tracker.DayRecord{Year: 2026, Date: "2026-10-05", EmailSentAt: now.UTC()}
				if !reflect.DeepEqual(s.marks, []tracker.DayRecord{want}) {
					t.Fatalf("incorrect mark: %+v", s.marks)
				}
			} else {
				if len(b.dates)+len(send.messages)+len(s.marks) != 0 {
					t.Fatal("suppressed day prompted or fabricated mark")
				}
				if !reflect.DeepEqual(s.records[original.Date], original) {
					t.Fatal("suppressed record changed")
				}
			}
		})
	}
}

func TestPromptFailures(t *testing.T) {
	now := mustTime(t, "2026-10-05T12:00:00Z")
	failure := errors.New("operation failed")
	for _, tc := range []struct {
		name, step           string
		builds, sends, marks int
	}{
		{"build", "build today email", 1, 0, 0}, {"send", "send today email", 1, 1, 0},
		{"mark", "mark today email sent", 1, 1, 1}, {"reread", "get today", 0, 0, 0},
		{"missing reread", "record missing after CreateDay", 0, 0, 0},
		{"yesterday get", "get yesterday", 0, 0, 0}, {"yesterday finalize", "save finalized yesterday", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newMemoryStore()
			b, send := &fakeBuilder{}, &fakeSender{}
			switch tc.name {
			case "build":
				b.err = failure
			case "send":
				send.err = failure
			case "mark":
				s.markErr = failure
			case "reread", "missing reread":
				current := tracker.NewPendingDay(now)
				s.records[current.Date] = current
				s.todayGetErr = failure
				if tc.name == "missing reread" {
					s.todayGetErr = nil
					s.missingToday = true
				}
			case "yesterday get":
				s.getErr = failure
			case "yesterday finalize":
				previous := tracker.NewPendingDay(now.AddDate(0, 0, -1))
				s.records[previous.Date] = previous
				s.putErr = failure
			}
			runner, err := New(s, b, send, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			err = runner.Run(context.Background(), now)
			if err == nil || !strings.Contains(err.Error(), tc.step) || !strings.Contains(err.Error(), "2026-10-") {
				t.Fatalf("missing step/date: %v", err)
			}
			if tc.name != "missing reread" && !errors.Is(err, failure) {
				t.Fatal("error cause lost")
			}
			if len(b.dates) != tc.builds || len(send.messages) != tc.sends || len(s.marks) != tc.marks {
				t.Fatal("continued after failure")
			}
			if tc.name == "mark" && !s.records["2026-10-05"].EmailSentAt.IsZero() {
				t.Fatal("failed mark persisted")
			}
		})
	}
}

func TestEmailRetries(t *testing.T) {
	now := mustTime(t, "2026-10-05T12:00:00Z")
	for _, mode := range []string{"successful mark", "failed mark", "response before retry", "mark conflict"} {
		t.Run(mode, func(t *testing.T) {
			s := newMemoryStore()
			b, send := &fakeBuilder{}, &fakeSender{}
			failure := errors.New("mark failed")
			if mode == "failed mark" || mode == "response before retry" {
				s.markErr = failure
			}
			if mode == "mark conflict" {
				s.markConflict = true
			}
			runner, err := New(s, b, send, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			err = runner.Run(context.Background(), now)
			if !errors.Is(err, s.markErr) {
				t.Fatalf("unexpected first result: %v", err)
			}
			if mode == "mark conflict" {
				if len(send.messages) != 1 || len(s.marks) != 1 {
					t.Fatal("conflict retried within run")
				}
				return
			}
			s.markErr = nil
			if mode == "response before retry" {
				s.records["2026-10-05"] = mustStatus(t, s.records["2026-10-05"], tracker.StatusFullDay, now.Add(time.Minute))
			}
			if err := runner.Run(context.Background(), now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			count := 1
			if mode == "failed mark" {
				count = 2
			}
			if len(b.dates) != count || len(send.messages) != count || len(s.marks) != count {
				t.Fatal("incorrect retry send behavior")
			}
			if mode == "response before retry" && !s.records["2026-10-05"].EmailSentAt.IsZero() {
				t.Fatal("response fabricated timestamp")
			}
		})
	}
}

func TestMarkPreservesRapidResponse(t *testing.T) {
	s := newMemoryStore()
	now := mustTime(t, "2026-10-05T12:00:00Z")
	var response tracker.DayRecord
	s.beforeMark = func(s *memoryStore) {
		response = mustStatus(t, s.records["2026-10-05"], tracker.StatusPTO, now)
		s.records[response.Date] = response
	}
	if err := mustRunner(t, s, time.UTC).Run(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	response.EmailSentAt = now.UTC()
	if !reflect.DeepEqual(s.records[response.Date], response) {
		t.Fatal("mark overwrote rapid response")
	}
}
