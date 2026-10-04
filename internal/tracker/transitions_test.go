package tracker

import (
	"reflect"
	"testing"
	"time"
)

func TestNewPendingDay(t *testing.T) {
	location := time.FixedZone("application", -7*60*60)
	for offset := 0; offset < 7; offset++ {
		day := time.Date(2026, 10, 3+offset, 23, 30, 0, 0, location)
		t.Run(day.Weekday().String(), func(t *testing.T) {
			want := DayRecord{Year: 2026, Date: day.Format("2006-01-02"), Status: StatusPending, IsWeekend: offset < 2}
			if got := NewPendingDay(day); !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v, want %+v", got, want)
			}
			if IsWeekend(day) != want.IsWeekend {
				t.Fatal("incorrect weekend detection")
			}
		})
	}
}

func TestApplyUserStatus(t *testing.T) {
	first := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	next := first.Add(time.Hour)
	statuses := []Status{StatusPending, StatusNoResponse, StatusFullDay, StatusHalfDay, StatusTimeOff, StatusPTO}
	for _, from := range statuses {
		for _, late := range []bool{false, true} {
			for _, to := range []Status{StatusFullDay, StatusHalfDay, StatusTimeOff, StatusPTO} {
				t.Run(fmtName(from, to, late), func(t *testing.T) {
					record := NewPendingDay(first)
					record.EmailSentAt = first
					if from == StatusNoResponse || late {
						record, _ = FinalizePending(record, first)
					}
					if isUserStatus(from) {
						record, _ = ApplyUserStatus(record, from, first)
					} else {
						record.Status = from
					}
					before := record
					got, err := ApplyUserStatus(record, to, next)
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(record, before) {
						t.Fatal("input mutated")
					}
					if from == to {
						if !reflect.DeepEqual(got, before) {
							t.Fatal("repeat changed record")
						}
						return
					}
					fraction := map[Status]float64{StatusFullDay: 1, StatusHalfDay: 0.5, StatusTimeOff: 0, StatusPTO: 0}[to]
					want := before
					want.Status, want.WorkFraction, want.RespondedAt = to, &fraction, next
					want.PTOFraction = 0
					if to == StatusPTO {
						want.PTOFraction = 1
					}
					want.HasBeenChanged = isUserStatus(from)
					want.ResponseSource = ResponseSourceUser
					if late || from == StatusNoResponse {
						want.ResponseSource = ResponseSourceLateUser
					}
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("got %+v, want %+v", got, want)
					}
				})
			}
		}
	}
}

func fmtName(from, to Status, late bool) string {
	name := string(from) + "_to_" + string(to)
	if late {
		name += "_after_finalization"
	}
	return name
}

func TestCorrectionFlagStaysTrue(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	record := NewPendingDay(at)
	for i, status := range []Status{StatusFullDay, StatusPTO, StatusPTO, StatusHalfDay, StatusFullDay, StatusFullDay, StatusTimeOff, StatusPTO} {
		var err error
		record, err = ApplyUserStatus(record, status, at.Add(time.Duration(i)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if record.HasBeenChanged != (i > 0) {
			t.Fatalf("step %d: incorrect correction flag", i)
		}
	}
}

func TestFinalizePending(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for _, status := range []Status{StatusPending, StatusNoResponse, StatusFullDay, StatusHalfDay, StatusTimeOff, StatusPTO} {
		t.Run(string(status), func(t *testing.T) {
			record := NewPendingDay(at)
			if status == StatusNoResponse {
				record, _ = FinalizePending(record, at)
			}
			if isUserStatus(status) {
				record, _ = ApplyUserStatus(record, status, at)
			}
			if status == StatusPending {
				// Finalization must explicitly clear any stale fractions.
				fraction := 0.5
				record.WorkFraction = &fraction
				record.PTOFraction = 1
			}
			before := record
			got, err := FinalizePending(record, at.Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			want := before
			if status == StatusPending {
				want.Status, want.ResponseSource = StatusNoResponse, ResponseSourceAutoFinalize
				want.WorkFraction, want.PTOFraction = nil, 0
				want.FinalizedAt = at.Add(time.Hour)
			}
			if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(record, before) {
				t.Fatalf("unexpected finalization: %+v", got)
			}
		})
	}
}

func TestInvalidTransitions(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for _, status := range []Status{StatusPending, StatusNoResponse, "HALF_DAY_PTO", "invalid", ""} {
		t.Run(string(status), func(t *testing.T) {
			record := NewPendingDay(at)
			got, err := ApplyUserStatus(record, status, at)
			if err == nil || !reflect.DeepEqual(got, record) {
				t.Fatal("invalid selection accepted or record changed")
			}
		})
	}
	for _, tc := range []struct {
		name     string
		record   DayRecord
		at       time.Time
		finalize bool
	}{
		{"zero response time", NewPendingDay(at), time.Time{}, false},
		{"zero finalization time", NewPendingDay(at), time.Time{}, true},
		{"unknown response state", DayRecord{Status: "invalid"}, at, false},
		{"unknown finalization state", DayRecord{Status: "invalid"}, at, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got DayRecord
			var err error
			if tc.finalize {
				got, err = FinalizePending(tc.record, tc.at)
			} else {
				got, err = ApplyUserStatus(tc.record, StatusFullDay, tc.at)
			}
			if err == nil || !reflect.DeepEqual(got, tc.record) {
				t.Fatal("invalid transition accepted or record changed")
			}
		})
	}
}
