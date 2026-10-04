package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

var testNow = time.Date(2026, 10, 4, 12, 0, 0, 123, time.FixedZone("test", -7*3600))

func validOptions(status tracker.Status) options {
	return options{table: "test-table", year: 2026, date: "2026-10-04", status: status}
}
func pending() tracker.DayRecord {
	return tracker.NewPendingDay(time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC))
}

type fakeStore struct {
	record            tracker.DayRecord
	exists            bool
	reads, writes     int
	readErr, writeErr error
	conflicts         int
	conflict          func(*fakeStore)
	expected          []tracker.DayRecord
	ctx               context.Context
}

func (f *fakeStore) GetDay(ctx context.Context, year int, date string) (tracker.DayRecord, bool, error) {
	f.reads++
	f.ctx = ctx
	return f.record, f.exists, f.readErr
}
func (f *fakeStore) UpdateUserResponseIfCurrent(ctx context.Context, updated, expected tracker.DayRecord) (bool, error) {
	f.writes++
	f.ctx = ctx
	f.expected = append(f.expected, expected)
	if f.writeErr != nil {
		return false, f.writeErr
	}
	if f.conflicts > 0 {
		f.conflicts--
		if f.conflict != nil {
			f.conflict(f)
		}
		return false, nil
	}
	if f.record.Status != expected.Status || (tracker.IsUserStatus(expected.Status) && !f.record.RespondedAt.Equal(expected.RespondedAt)) {
		return false, nil
	}
	f.record = updated
	return true, nil
}

func TestCorrections(t *testing.T) {
	for _, tt := range []struct {
		status    tracker.Status
		work, pto float64
	}{
		{tracker.StatusFullDay, 1, 0}, {tracker.StatusHalfDay, 0.5, 0}, {tracker.StatusPTO, 0, 1}, {tracker.StatusTimeOff, 0, 0},
	} {
		t.Run(string(tt.status), func(t *testing.T) {
			f := &fakeStore{record: pending(), exists: true}
			var output bytes.Buffer
			type contextKey struct{}
			ctx := context.WithValue(context.Background(), contextKey{}, "test")
			err := correctDay(ctx, f, validOptions(tt.status), strings.NewReader("YeS\n"), &output, testNow)
			got := f.record
			if err != nil || f.reads != 1 || f.writes != 1 || f.ctx != ctx || got.Status != tt.status || got.WorkFraction == nil || *got.WorkFraction != tt.work || got.PTOFraction != tt.pto || got.HasBeenChanged || got.ResponseSource != tracker.ResponseSourceUser || !got.RespondedAt.Equal(testNow) || got.RespondedAt.Location() != time.UTC {
				t.Fatalf("error=%v record=%+v reads=%d writes=%d", err, got, f.reads, f.writes)
			}
			for _, text := range []string{"Date: 2026-10-04", "Current Status: PENDING", "Requested Status: " + string(tt.status), "Current HasBeenChanged: false", "Resulting HasBeenChanged: false", "Resulting ResponseSource: USER", "Apply this correction? [y/N]", "Saved"} {
				if !strings.Contains(output.String(), text) {
					t.Fatalf("missing %q in %q", text, output.String())
				}
			}
		})
	}
}

func TestTransitionMetadata(t *testing.T) {
	for _, tt := range []struct {
		name    string
		current tracker.Status
		changed bool
		source  tracker.ResponseSource
	}{
		{"late first response", tracker.StatusNoResponse, false, tracker.ResponseSourceLateUser},
		{"late changed flag retained", tracker.StatusNoResponse, true, tracker.ResponseSourceLateUser},
		{"correction", tracker.StatusFullDay, true, tracker.ResponseSourceUser},
	} {
		t.Run(tt.name, func(t *testing.T) {
			record := pending()
			at := testNow.Add(-time.Hour)
			target := tracker.StatusFullDay
			if tt.current == tracker.StatusNoResponse {
				record, _ = tracker.FinalizePending(record, at)
				record.HasBeenChanged = tt.changed
			} else {
				record, _ = tracker.ApplyUserStatus(record, tt.current, at)
				target = tracker.StatusHalfDay
			}
			f := &fakeStore{record: record, exists: true}
			err := correctDay(context.Background(), f, validOptions(target), strings.NewReader("y\n"), io.Discard, testNow)
			if err != nil || f.record.HasBeenChanged != tt.changed || f.record.ResponseSource != tt.source || !f.record.FinalizedAt.Equal(record.FinalizedAt) {
				t.Fatalf("error=%v record=%+v", err, f.record)
			}
		})
	}
}

func TestSameStatusNoWrite(t *testing.T) {
	for _, changed := range []bool{false, true} {
		record, _ := tracker.ApplyUserStatus(pending(), tracker.StatusFullDay, testNow.Add(-time.Hour))
		record.HasBeenChanged = changed
		f := &fakeStore{record: record, exists: true}
		var output bytes.Buffer
		err := correctDay(context.Background(), f, validOptions(tracker.StatusFullDay), strings.NewReader(""), &output, testNow)
		if err != nil || f.writes != 0 || !reflect.DeepEqual(f.record, record) || !strings.Contains(output.String(), "no effective state change") || strings.Contains(output.String(), "Apply this correction?") {
			t.Fatalf("error=%v writes=%d output=%q", err, f.writes, output.String())
		}
	}
}

func TestDeclinedConfirmation(t *testing.T) {
	for _, answer := range []string{"", "\n", "n\n", "no\n", "true\n", "yeah\n", " y \n"} {
		f := &fakeStore{record: pending(), exists: true}
		err := correctDay(context.Background(), f, validOptions(tracker.StatusFullDay), strings.NewReader(answer), io.Discard, testNow)
		if err != nil || f.writes != 0 {
			t.Fatalf("answer=%q error=%v writes=%d", answer, err, f.writes)
		}
	}
}

func TestConflicts(t *testing.T) {
	t.Run("reread and reconfirm", func(t *testing.T) {
		f := &fakeStore{record: pending(), exists: true, conflicts: 1}
		f.conflict = func(f *fakeStore) { f.record, _ = tracker.FinalizePending(f.record, testNow.Add(-time.Minute)) }
		var output bytes.Buffer
		err := correctDay(context.Background(), f, validOptions(tracker.StatusFullDay), strings.NewReader("y\nyes\n"), &output, testNow)
		if err != nil || f.reads != 2 || f.writes != 2 || f.record.ResponseSource != tracker.ResponseSourceLateUser || f.record.HasBeenChanged || f.record.FinalizedAt.IsZero() || f.expected[1].Status != tracker.StatusNoResponse || strings.Count(output.String(), "Apply this correction?") != 2 {
			t.Fatalf("error=%v store=%+v output=%q", err, f, output.String())
		}
	})
	t.Run("exhaustion", func(t *testing.T) {
		f := &fakeStore{record: pending(), exists: true, conflicts: 3}
		err := correctDay(context.Background(), f, validOptions(tracker.StatusPTO), strings.NewReader("y\ny\ny\n"), io.Discard, testNow)
		if err == nil || !strings.Contains(err.Error(), "after 3 attempts") || f.reads != 3 || f.writes != 3 {
			t.Fatalf("error=%v store=%+v", err, f)
		}
	})
	t.Run("decline after conflict", func(t *testing.T) {
		f := &fakeStore{record: pending(), exists: true, conflicts: 1}
		err := correctDay(context.Background(), f, validOptions(tracker.StatusPTO), strings.NewReader("y\nn\n"), io.Discard, testNow)
		if err != nil || f.reads != 2 || f.writes != 1 || f.record.Status != tracker.StatusPending {
			t.Fatalf("error=%v store=%+v", err, f)
		}
	})
	t.Run("concurrent actor already selected target", func(t *testing.T) {
		f := &fakeStore{record: pending(), exists: true, conflicts: 1}
		f.conflict = func(f *fakeStore) {
			f.record, _ = tracker.ApplyUserStatus(f.record, tracker.StatusPTO, testNow.Add(-time.Minute))
		}
		err := correctDay(context.Background(), f, validOptions(tracker.StatusPTO), strings.NewReader("y\n"), io.Discard, testNow)
		if err != nil || f.reads != 2 || f.writes != 1 || !f.record.RespondedAt.Equal(testNow.Add(-time.Minute)) {
			t.Fatalf("error=%v store=%+v", err, f)
		}
	})
}

func TestInvalidOptions(t *testing.T) {
	for _, change := range []func(*options){
		func(o *options) { o.table = " \t" }, func(o *options) { o.year = 0 }, func(o *options) { o.year = -1 }, func(o *options) { o.year = 2027 },
		func(o *options) { o.date = "" }, func(o *options) { o.date = "2026-1-3" }, func(o *options) { o.date = "2026-02-30" },
		func(o *options) { o.status = tracker.StatusPending }, func(o *options) { o.status = tracker.StatusNoResponse }, func(o *options) { o.status = "UNKNOWN" },
	} {
		opts := validOptions(tracker.StatusFullDay)
		change(&opts)
		f := &fakeStore{}
		err := correctDay(context.Background(), f, opts, strings.NewReader("y\n"), io.Discard, testNow)
		if err == nil || f.reads != 0 || f.writes != 0 {
			t.Fatalf("accepted %+v", opts)
		}
	}
}

func TestParseOptions(t *testing.T) {
	args := []string{"--table", "test-table", "--year", "2026", "--date", "2026-10-04", "--status", "FULL_DAY"}
	got, err := parseOptions(args, io.Discard)
	if err != nil || got != validOptions(tracker.StatusFullDay) {
		t.Fatalf("got=%+v error=%v", got, err)
	}
	for _, args := range [][]string{nil, {"--year", "abc"}, {"--unknown"}, append(append([]string{}, args...), "extra")} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestStorageErrors(t *testing.T) {
	failure := errors.New("offline failure")
	for _, tt := range []struct {
		name              string
		exists            bool
		readErr, writeErr error
		want              string
	}{
		{name: "missing", want: "not found"}, {name: "read", readErr: failure, want: "read correction day"},
		{name: "write", exists: true, writeErr: failure, want: "save correction"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStore{record: pending(), exists: tt.exists, readErr: tt.readErr, writeErr: tt.writeErr}
			err := correctDay(context.Background(), f, validOptions(tracker.StatusFullDay), strings.NewReader("y\n"), io.Discard, testNow)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error=%v", err)
			}
			if (tt.readErr != nil || tt.writeErr != nil) && !errors.Is(err, failure) {
				t.Fatalf("lost cause: %v", err)
			}
		})
	}
	f := &fakeStore{record: pending(), exists: true}
	if err := correctDay(context.Background(), f, validOptions(tracker.StatusFullDay), strings.NewReader("y\n"), io.Discard, time.Time{}); err == nil || f.reads != 0 {
		t.Fatal("zero timestamp accepted")
	}
}

func TestCorrectionsBetweenUserStatuses(t *testing.T) {
	for _, target := range []tracker.Status{tracker.StatusFullDay, tracker.StatusHalfDay, tracker.StatusPTO, tracker.StatusTimeOff} {
		t.Run(string(target), func(t *testing.T) {
			initial := tracker.StatusFullDay
			if target == initial {
				initial = tracker.StatusHalfDay
			}
			record, _ := tracker.ApplyUserStatus(pending(), initial, testNow.Add(-time.Hour))
			f := &fakeStore{record: record, exists: true}
			err := correctDay(context.Background(), f, validOptions(target), strings.NewReader("y\n"), io.Discard, testNow)
			if err != nil || f.writes != 1 || f.record.Status != target || !f.record.HasBeenChanged || f.record.ResponseSource != tracker.ResponseSourceUser {
				t.Fatalf("error=%v record=%+v", err, f.record)
			}
		})
	}
	t.Run("late correction remains late", func(t *testing.T) {
		record, _ := tracker.FinalizePending(pending(), testNow.Add(-2*time.Hour))
		record, _ = tracker.ApplyUserStatus(record, tracker.StatusPTO, testNow.Add(-time.Hour))
		f := &fakeStore{record: record, exists: true}
		err := correctDay(context.Background(), f, validOptions(tracker.StatusFullDay), strings.NewReader("y\n"), io.Discard, testNow)
		if err != nil || !f.record.HasBeenChanged || f.record.ResponseSource != tracker.ResponseSourceLateUser || !f.record.FinalizedAt.Equal(record.FinalizedAt) {
			t.Fatalf("error=%v record=%+v", err, f.record)
		}
	})
}
