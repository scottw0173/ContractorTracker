package tracker

import (
	"fmt"
	"time"
)

func isUserStatus(status Status) bool {
	return status == StatusFullDay || status == StatusHalfDay || status == StatusTimeOff || status == StatusPTO
}

// ApplyUserStatus records a response or correction without modifying record.
// Repeating the current status leaves every field unchanged. FinalizedAt is
// retained so corrections to late responses remain identifiable as late.
func ApplyUserStatus(record DayRecord, status Status, at time.Time) (DayRecord, error) {
	if !isUserStatus(status) {
		return record, fmt.Errorf("invalid user status %q", status)
	}
	if record.Status != StatusPending && record.Status != StatusNoResponse && !isUserStatus(record.Status) {
		return record, fmt.Errorf("invalid record status %q", record.Status)
	}
	if record.Status == status {
		return record, nil
	}
	if at.IsZero() {
		return record, fmt.Errorf("response timestamp must not be zero")
	}
	record.HasBeenChanged = record.HasBeenChanged || isUserStatus(record.Status)
	late := record.Status == StatusNoResponse || !record.FinalizedAt.IsZero() || record.ResponseSource == ResponseSourceLateUser
	record.Status = status
	fraction := 0.0
	record.PTOFraction = 0
	switch status {
	case StatusFullDay:
		fraction = 1.0
	case StatusHalfDay:
		fraction = 0.5
	case StatusPTO:
		record.PTOFraction = 1
	}
	record.WorkFraction = &fraction
	record.RespondedAt = at
	record.ResponseSource = ResponseSourceUser
	if late {
		record.ResponseSource = ResponseSourceLateUser
	}
	return record, nil
}

// FinalizePending marks a pending day as unanswered. Other valid statuses are
// unchanged. The caller decides when the day is due for finalization.
func FinalizePending(record DayRecord, at time.Time) (DayRecord, error) {
	if record.Status != StatusPending {
		if record.Status == StatusNoResponse || isUserStatus(record.Status) {
			return record, nil
		}
		return record, fmt.Errorf("invalid record status %q", record.Status)
	}
	if at.IsZero() {
		return record, fmt.Errorf("finalization timestamp must not be zero")
	}
	record.Status = StatusNoResponse
	record.WorkFraction = nil
	record.PTOFraction = 0
	record.FinalizedAt = at
	record.ResponseSource = ResponseSourceAutoFinalize
	return record, nil
}
