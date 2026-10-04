// Package daily implements the once-daily record workflow.
package daily

import (
	"context"
	"fmt"
	"time"

	"github.com/scottw0173/ContractorTracker/internal/email"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

// DayStore contains only the storage operations needed by the daily workflow.
type DayStore interface {
	MarkEmailSent(context.Context, int, string, time.Time) (bool, error)
	GetDay(context.Context, int, string) (tracker.DayRecord, bool, error)
	CreateDay(context.Context, tracker.DayRecord) (bool, error)
	PutDayIfStatus(context.Context, tracker.DayRecord, tracker.Status) (bool, error)
}

// MessageBuilder composes the prompt without exposing transport details.
type MessageBuilder interface {
	Build(string) (email.Message, error)
}

// MessageSender delivers a composed prompt.
type MessageSender interface {
	Send(context.Context, email.Message) error
}

type Runner struct {
	builder  MessageBuilder
	sender   MessageSender
	store    DayStore
	location *time.Location
}

// New requires a store, message builder, sender, and explicit application timezone.
func New(store DayStore, builder MessageBuilder, sender MessageSender, location *time.Location) (*Runner, error) {
	if location == nil {
		return nil, fmt.Errorf("daily runner requires a timezone")
	}
	if store == nil {
		return nil, fmt.Errorf("daily runner requires a store")
	}
	if builder == nil {
		return nil, fmt.Errorf("daily runner requires a message builder")
	}
	if sender == nil {
		return nil, fmt.Errorf("daily runner requires a message sender")
	}
	return &Runner{store: store, builder: builder, sender: sender, location: location}, nil
}

// Run finalizes an existing pending yesterday, ensures today exists, and sends
// today's prompt only while it is pending and has not been marked as emailed.
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
	todayDate := today.Format("2006-01-02")
	current := tracker.NewPendingDay(today)
	created, err := r.store.CreateDay(ctx, current)
	if err != nil {
		return fmt.Errorf("create today %s: %w", todayDate, err)
	}
	if !created {
		var exists bool
		current, exists, err = r.store.GetDay(ctx, today.Year(), todayDate)
		if err != nil {
			return fmt.Errorf("get today %s: %w", todayDate, err)
		}
		if !exists {
			return fmt.Errorf("get today %s: record missing after CreateDay reported it exists", todayDate)
		}
	}
	if current.Status != tracker.StatusPending || !current.EmailSentAt.IsZero() {
		return nil
	}
	message, err := r.builder.Build(todayDate)
	if err != nil {
		return fmt.Errorf("build today email %s: %w", todayDate, err)
	}
	if err := r.sender.Send(ctx, message); err != nil {
		return fmt.Errorf("send today email %s: %w", todayDate, err)
	}
	if _, err := r.store.MarkEmailSent(ctx, today.Year(), todayDate, now.UTC()); err != nil {
		return fmt.Errorf("mark today email sent %s: %w", todayDate, err)
	}
	return nil
}
