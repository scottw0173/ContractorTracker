// Package tracker contains the work-status domain model and pure transitions.
package tracker

import "time"

type Status string

const (
	StatusPending    Status = "PENDING"
	StatusFullDay    Status = "FULL_DAY"
	StatusHalfDay    Status = "HALF_DAY"
	StatusTimeOff    Status = "TIME_OFF"
	StatusPTO        Status = "PTO"
	StatusNoResponse Status = "NO_RESPONSE"
)

type ResponseSource string

const (
	ResponseSourceUser          ResponseSource = "USER"
	ResponseSourceLateUser      ResponseSource = "LATE_USER"
	ResponseSourceAutoFinalize  ResponseSource = "AUTO_FINALIZE"
	ResponseSourceAdminBackfill ResponseSource = "ADMIN_BACKFILL"
)

// DayRecord describes one calendar day. Zero timestamps mean the event has
// not occurred; a nil WorkFraction means the amount of work is unknown.
// PTOFraction records days of PTO consumed independently of work performed.
type DayRecord struct {
	Year           int
	Date           string
	Status         Status
	WorkFraction   *float64
	PTOFraction    float64
	IsWeekend      bool
	EmailSentAt    time.Time
	RespondedAt    time.Time
	FinalizedAt    time.Time
	ResponseSource ResponseSource
	HasBeenChanged bool
}

// NewPendingDay uses the calendar date in day's location. Callers must convert
// day to the application timezone before calling it.
func NewPendingDay(day time.Time) DayRecord {
	return DayRecord{
		Year: day.Year(), Date: day.Format("2006-01-02"),
		Status: StatusPending, IsWeekend: IsWeekend(day),
	}
}

// IsWeekend uses the weekday in day's location.
func IsWeekend(day time.Time) bool {
	return day.Weekday() == time.Saturday || day.Weekday() == time.Sunday
}
