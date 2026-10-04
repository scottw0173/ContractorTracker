package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

var testNow = time.Date(2026, 10, 4, 12, 30, 45, 123456789, time.FixedZone("operator", -7*3600))

func validOptions(status tracker.Status) options {
	return options{table: "test-table", year: 2026, date: "2026-10-01", status: status}
}

type fakeStore struct {
	record             tracker.DayRecord
	exists             bool
	reads, writes      int
	readErr, createErr error
	concurrent         *tracker.DayRecord
	readCtx, createCtx context.Context
	year               int
	date               string
}

func (f *fakeStore) GetDay(ctx context.Context, year int, date string) (tracker.DayRecord, bool, error) {
	f.reads++
	f.readCtx = ctx
	f.year, f.date = year, date
	return f.record, f.exists, f.readErr
}
func (f *fakeStore) CreateDay(ctx context.Context, record tracker.DayRecord) (bool, error) {
	f.writes++
	f.createCtx = ctx
	if f.createErr != nil {
		return false, f.createErr
	}
	if f.concurrent != nil {
		f.record = *f.concurrent
		f.exists = true
	}
	if f.exists {
		return false, nil
	}
	f.record = record
	f.exists = true
	return true, nil
}

func TestBackfillConfirmed(t *testing.T) {
	for _, tt := range []struct {
		status    tracker.Status
		work, pto float64
	}{
		{tracker.StatusFullDay, 1, 0}, {tracker.StatusHalfDay, .5, 0}, {tracker.StatusPTO, 0, 1}, {tracker.StatusTimeOff, 0, 0},
	} {
		t.Run(string(tt.status), func(t *testing.T) {
			f := &fakeStore{}
			var output bytes.Buffer
			type ctxKey struct{}
			ctx := context.WithValue(context.Background(), ctxKey{}, "offline")
			err := backfillDay(ctx, f, validOptions(tt.status), strings.NewReader("YeS\n"), &output, testNow)
			got := f.record
			if err != nil || f.reads != 1 || f.writes != 1 || f.year != 2026 || f.date != "2026-10-01" || f.readCtx != ctx || f.createCtx != ctx || got.Status != tt.status || got.WorkFraction == nil || *got.WorkFraction != tt.work || got.PTOFraction != tt.pto || got.IsWeekend || got.ResponseSource != tracker.ResponseSourceAdminBackfill || got.HasBeenChanged || !got.EmailSentAt.IsZero() || !got.FinalizedAt.IsZero() || !got.RespondedAt.Equal(testNow) || got.RespondedAt.Location() != time.UTC {
				t.Fatalf("err=%v record=%+v store=%+v", err, got, f)
			}
			for _, text := range []string{"Date: 2026-10-01", "Status: " + string(tt.status), fmt.Sprintf("Work Fraction: %g", tt.work), fmt.Sprintf("PTO Fraction: %g", tt.pto), "Weekend: false", "Response Source: ADMIN_BACKFILL", "Responded At: " + testNow.UTC().Format(time.RFC3339Nano), "Create this historical record? [y/N]", "Created 2026-10-01"} {
				if !strings.Contains(output.String(), text) {
					t.Fatalf("missing %q in %q", text, output.String())
				}
			}
		})
	}
}

func TestConfirmationConservative(t *testing.T) {
	for _, answer := range []string{"y\n", "Y\n", "yes\n", "YES\n", "n\n", "no\n", "\n", "", "yeah\n", "true\n", " y \n", "yes please\n"} {
		t.Run(fmt.Sprintf("%q", answer), func(t *testing.T) {
			f := &fakeStore{}
			var output bytes.Buffer
			err := backfillDay(context.Background(), f, validOptions(tracker.StatusFullDay), strings.NewReader(answer), &output, testNow)
			confirm := answer == "y\n" || answer == "Y\n" || answer == "yes\n" || answer == "YES\n"
			wantWrites := 0
			if confirm {
				wantWrites = 1
			}
			if err != nil || f.reads != 1 || f.writes != wantWrites || f.exists != confirm {
				t.Fatalf("answer=%q err=%v writes=%d", answer, err, f.writes)
			}
			if !confirm && !strings.Contains(output.String(), "cancelled; no write performed") {
				t.Fatal(output.String())
			}
		})
	}
}

func TestExistingAndConcurrentDay(t *testing.T) {
	day, _ := time.Parse(time.DateOnly, "2026-10-01")
	existing, err := tracker.NewAdminBackfillDay(day, tracker.StatusPTO, testNow.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprint(concurrent), func(t *testing.T) {
			f := &fakeStore{}
			if concurrent {
				f.concurrent = &existing
			} else {
				f.record = existing
				f.exists = true
			}
			var output bytes.Buffer
			err := backfillDay(context.Background(), f, validOptions(tracker.StatusFullDay), strings.NewReader("y\n"), &output, testNow)
			wantWrites := 0
			if concurrent {
				wantWrites = 1
			}
			if err == nil || !strings.Contains(err.Error(), "exists") || !strings.Contains(err.Error(), "admin-correct") || f.writes != wantWrites || !reflect.DeepEqual(f.record, existing) {
				t.Fatalf("err=%v writes=%d record=%+v", err, f.writes, f.record)
			}
			if !concurrent && strings.Contains(output.String(), "Create this historical record?") {
				t.Fatal("existing record offered for creation")
			}
			if strings.Contains(output.String(), "Created ") {
				t.Fatal("conflict reported success")
			}
		})
	}
}

func TestOptionsValidation(t *testing.T) {
	base := []string{"--table", "test-table", "--year", "2026", "--date", "2026-10-01", "--status", "FULL_DAY"}
	for _, status := range []tracker.Status{tracker.StatusFullDay, tracker.StatusHalfDay, tracker.StatusPTO, tracker.StatusTimeOff} {
		args := append([]string{}, base...)
		args[7] = string(status)
		got, err := parseOptions(args, io.Discard)
		if err != nil || got != validOptions(status) {
			t.Fatalf("got=%+v err=%v", got, err)
		}
	}
	for _, tt := range []struct {
		index int
		value string
	}{
		{1, ""}, {1, " \t"}, {3, "0"}, {3, "-1"}, {3, "2027"}, {3, "abc"}, {5, ""}, {5, "2026-1-1"}, {5, "2026/10/01"}, {5, "2026-02-30"}, {5, "2026-10-01T00:00:00Z"}, {7, ""}, {7, "PENDING"}, {7, "NO_RESPONSE"}, {7, "UNKNOWN"}, {7, "full_day"},
	} {
		args := append([]string{}, base...)
		args[tt.index] = tt.value
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, args := range [][]string{nil, {"--unknown"}, append(append([]string{}, base...), "extra"), {"extra", "--table", "test-table"}} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	for _, change := range []func(*options){func(o *options) { o.table = "" }, func(o *options) { o.year = 2027 }, func(o *options) { o.date = "2026-1-1" }, func(o *options) { o.status = tracker.StatusPending }, func(o *options) { o.status = tracker.StatusNoResponse }} {
		opts := validOptions(tracker.StatusFullDay)
		change(&opts)
		f := &fakeStore{}
		if err := backfillDay(context.Background(), f, opts, strings.NewReader("y\n"), io.Discard, testNow); err == nil || f.reads+f.writes != 0 {
			t.Fatal("invalid input reached store")
		}
	}
}

type failingIO struct{ err error }

func (f failingIO) Read([]byte) (int, error)  { return 0, f.err }
func (f failingIO) Write([]byte) (int, error) { return 0, f.err }

func TestBackfillErrors(t *testing.T) {
	failure := errors.New("offline failure")
	for _, tt := range []struct {
		name               string
		readErr, createErr error
		input              io.Reader
		output             io.Writer
		want               string
		writes             int
	}{
		{name: "read", readErr: failure, want: "read backfill day"},
		{name: "create", createErr: failure, want: "create backfill day", writes: 1},
		{name: "confirmation", input: failingIO{failure}, want: "read confirmation"},
		{name: "preview", output: failingIO{failure}, want: "print backfill preview"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeStore{readErr: tt.readErr, createErr: tt.createErr}
			input := tt.input
			if input == nil {
				input = strings.NewReader("y\n")
			}
			output := tt.output
			if output == nil {
				output = io.Discard
			}
			err := backfillDay(context.Background(), f, validOptions(tracker.StatusFullDay), input, output, testNow)
			if !errors.Is(err, failure) || !strings.Contains(err.Error(), tt.want) || f.writes != tt.writes {
				t.Fatalf("error=%v writes=%d", err, f.writes)
			}
		})
	}
	f := &fakeStore{}
	if err := backfillDay(context.Background(), f, validOptions(tracker.StatusFullDay), strings.NewReader("y\n"), io.Discard, time.Time{}); err == nil || f.reads+f.writes != 0 {
		t.Fatal("zero timestamp accepted")
	}
}
