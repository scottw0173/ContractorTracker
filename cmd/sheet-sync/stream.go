package main

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

// handleEvent treats stream records as notifications only; images are never projected.
func handleEvent(ctx context.Context, batch events.DynamoDBEvent, db dayReader, project func(context.Context, tracker.DayRecord) error) error {
	// Validate the complete batch before synchronization. Deduplication is local
	// to this invocation; the slice retains deterministic first-seen order.
	seen := make(map[dayKey]bool)
	var keys []dayKey
	var firstRecords []int
	for i, record := range batch.Records {
		if record.EventSource != "aws:dynamodb" {
			return fmt.Errorf("stream record %d has unsupported event source %q", i, record.EventSource)
		}
		switch record.EventName {
		case "REMOVE":
			continue // Worksheet deletion is outside the projection contract.
		case "INSERT", "MODIFY":
		default:
			return fmt.Errorf("stream record %d has unsupported event name %q", i, record.EventName)
		}
		key, err := streamKey(record)
		if err != nil {
			return fmt.Errorf("stream record %d keys: %w", i, err)
		}
		if err := key.validate(); err != nil {
			return fmt.Errorf("stream record %d keys: %w", i, err)
		}
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
			firstRecords = append(firstRecords, i)
		}
	}
	for i, key := range keys {
		if err := syncDay(ctx, key, db, project); err != nil {
			return fmt.Errorf("stream record %d: %w", firstRecords[i], err)
		}
	}
	return nil
}

func streamKey(record events.DynamoDBEventRecord) (dayKey, error) {
	year, ok := record.Change.Keys["year"]
	if !ok || year.DataType() != events.DataTypeNumber {
		return dayKey{}, fmt.Errorf("year key must be a DynamoDB number")
	}
	date, ok := record.Change.Keys["date"]
	if !ok || date.DataType() != events.DataTypeString {
		return dayKey{}, fmt.Errorf("date key must be a DynamoDB string")
	}
	parsed, err := strconv.Atoi(year.Number())
	if err != nil {
		return dayKey{}, fmt.Errorf("year key must be an integer: %w", err)
	}
	return dayKey{Year: parsed, Date: date.String()}, nil
}

// dayKey is a validated stream key, not an external invocation contract.
type dayKey struct {
	Year int
	Date string
}

type dayReader interface {
	GetDay(context.Context, int, string) (tracker.DayRecord, bool, error)
}

func syncDay(ctx context.Context, event dayKey, db dayReader, project func(context.Context, tracker.DayRecord) error) error {
	if err := event.validate(); err != nil {
		return err
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

func (event dayKey) validate() error {
	date, err := time.Parse(time.DateOnly, event.Date)
	if err != nil || date.Format(time.DateOnly) != event.Date || event.Year <= 0 || date.Year() != event.Year {
		return fmt.Errorf("invalid stream key year/date: %d/%q", event.Year, event.Date)
	}
	return nil
}
