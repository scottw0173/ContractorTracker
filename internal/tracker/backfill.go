package tracker

import (
	"fmt"
	"time"
)

// NewAdminBackfillDay reconstructs a missing calendar day's final status. The
// administrative entry time is recorded in UTC, without inventing email or
// finalization events. Callers are responsible for conditional creation only.
func NewAdminBackfillDay(day time.Time, status Status, at time.Time) (DayRecord, error) {
	if at.IsZero() {
		return DayRecord{}, fmt.Errorf("administrative backfill timestamp must not be zero")
	}
	record, err := ApplyUserStatus(NewPendingDay(day), status, at.UTC())
	if err != nil {
		return DayRecord{}, fmt.Errorf("construct administrative backfill: %w", err)
	}
	record.ResponseSource = ResponseSourceAdminBackfill
	return record, nil
}
