package main

import (
	"context"
	"fmt"
	"time"

	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

// Event identifies one authoritative DynamoDB day for manual projection.
type Event struct {
	Year int    `json:"year"`
	Date string `json:"date"`
}

type dayReader interface {
	GetDay(context.Context, int, string) (tracker.DayRecord, bool, error)
}

func syncDay(ctx context.Context, event Event, db dayReader, project func(context.Context, tracker.DayRecord) error) error {
	date, err := time.Parse(time.DateOnly, event.Date)
	if err != nil || date.Format(time.DateOnly) != event.Date || event.Year <= 0 || date.Year() != event.Year {
		return fmt.Errorf("invalid projection event year/date: %d/%q", event.Year, event.Date)
	}
	record, exists, err := db.GetDay(ctx, event.Year, event.Date)
	if err != nil {
		return fmt.Errorf("load projection day %s: %w", event.Date, err)
	}
	if !exists {
		return fmt.Errorf("projection day %s not found", event.Date)
	}
	if record.Year != event.Year || record.Date != event.Date {
		return fmt.Errorf("projection day key differs from requested year/date")
	}
	if err := project(ctx, record); err != nil {
		return fmt.Errorf("project day %s: %w", event.Date, err)
	}
	return nil
}
