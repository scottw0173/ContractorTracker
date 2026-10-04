// Package daily implements the once-daily record workflow.
package daily

import (
	"context"
	"fmt"
	"time"

	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

// DayStore contains only the storage operations needed by the daily workflow.
type DayStore interface {
	GetDay(context.Context, int, string) (tracker.DayRecord, bool, error)
	CreateDay(context.Context, tracker.DayRecord) (bool, error)
	PutDayIfStatus(context.Context, tracker.DayRecord, tracker.Status) (bool, error)
}

type Runner struct {
	store    DayStore
	location *time.Location
}

// New requires an explicit application timezone and a configured store.
func New(store DayStore, location *time.Location) (*Runner, error) {
	if location == nil {
		return nil, fmt.Errorf("daily runner requires a timezone")
	}
	if store == nil {
		return nil, fmt.Errorf("daily runner requires a store")
	}
	return &Runner{store: store, location: location}, nil
}

// Run finalizes an existing pending yesterday and ensures today's record exists.
// Conditional conflicts are expected; existing records are never reset to pending.
func (r *Runner) Run(ctx context.Context, now time.Time) error {
	today := now.In(r.location)
	yesterday := today.AddDate(0, 0, -1)
	yesterdayDate := yesterday.Format("2006-01-02")
	record, exists, err := r.store.GetDay(ctx, yesterday.Year(), yesterdayDate)
	if err != nil {
		return fmt.Errorf("get yesterday %s: %w", yesterdayDate, err)
	}
	if exists && record.Status == tracker.StatusPending {
		finalized, err := tracker.FinalizePending(record, now.UTC())
		if err != nil {
			return fmt.Errorf("finalize yesterday %s: %w", yesterdayDate, err)
		}
		if _, err := r.store.PutDayIfStatus(ctx, finalized, tracker.StatusPending); err != nil {
			return fmt.Errorf("save finalized yesterday %s: %w", yesterdayDate, err)
		}
	}
	if _, err := r.store.CreateDay(ctx, tracker.NewPendingDay(today)); err != nil {
		return fmt.Errorf("create today %s: %w", today.Format("2006-01-02"), err)
	}
	return nil
}
