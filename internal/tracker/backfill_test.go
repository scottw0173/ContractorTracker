package tracker

import (
	"reflect"
	"testing"
	"time"
)

func TestNewAdminBackfillDay(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 30, 45, 123456789, time.FixedZone("operator", -7*3600))
	for _, tt := range []struct {
		status    Status
		work, pto float64
	}{
		{StatusFullDay, 1, 0}, {StatusHalfDay, .5, 0}, {StatusPTO, 0, 1}, {StatusTimeOff, 0, 0},
	} {
		for _, date := range []string{"2026-10-01", "2026-10-03", "2026-10-04"} {
			t.Run(string(tt.status)+"/"+date, func(t *testing.T) {
				day, err := time.Parse(time.DateOnly, date)
				if err != nil {
					t.Fatal(err)
				}
				got, err := NewAdminBackfillDay(day, tt.status, at)
				if err != nil {
					t.Fatal(err)
				}
				want := DayRecord{Year: 2026, Date: date, Status: tt.status, WorkFraction: &tt.work, PTOFraction: tt.pto, IsWeekend: date != "2026-10-01", RespondedAt: at.UTC(), ResponseSource: ResponseSourceAdminBackfill}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("got %+v; want %+v", got, want)
				}
			})
		}
	}
}

func TestInvalidAdminBackfill(t *testing.T) {
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for _, status := range []Status{StatusPending, StatusNoResponse, "UNKNOWN", ""} {
		if got, err := NewAdminBackfillDay(at, status, at); err == nil || !reflect.DeepEqual(got, DayRecord{}) {
			t.Fatalf("accepted %q: %+v %v", status, got, err)
		}
	}
	if _, err := NewAdminBackfillDay(at, StatusFullDay, time.Time{}); err == nil {
		t.Fatal("zero timestamp accepted")
	}
}

func TestBackfillNormalCorrectionSemantics(t *testing.T) {
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	at := day.AddDate(0, 0, 3)
	for _, status := range []Status{StatusFullDay, StatusHalfDay, StatusPTO, StatusTimeOff} {
		t.Run(string(status), func(t *testing.T) {
			record, err := NewAdminBackfillDay(day, status, at)
			if err != nil {
				t.Fatal(err)
			}
			repeat, err := ApplyUserStatus(record, status, at.Add(time.Hour))
			if err != nil || !reflect.DeepEqual(repeat, record) {
				t.Fatal("same response changed backfill")
			}
			target := StatusFullDay
			if status == target {
				target = StatusHalfDay
			}
			corrected, err := ApplyUserStatus(record, target, at.Add(time.Hour))
			if err != nil || !corrected.HasBeenChanged || corrected.ResponseSource != ResponseSourceUser || !corrected.RespondedAt.Equal(at.Add(time.Hour)) || !corrected.EmailSentAt.IsZero() || !corrected.FinalizedAt.IsZero() {
				t.Fatalf("correction = %+v, %v", corrected, err)
			}
			if record.HasBeenChanged || record.ResponseSource != ResponseSourceAdminBackfill {
				t.Fatal("input mutated")
			}
		})
	}
}
