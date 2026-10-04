package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/aws/aws-lambda-go/events"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

// handleEvent accepts either the operational manual key or the AWS stream event.
// Stream records are notifications only; images are never projected.
func handleEvent(ctx context.Context, raw json.RawMessage, db dayReader, project func(context.Context, tracker.DayRecord) error) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("decode projection event: %w", err)
	}
	if envelope == nil {
		return fmt.Errorf("projection event must be an object")
	}
	records, isStream := envelope["Records"]
	if !isStream {
		var event Event
		if err := json.Unmarshal(raw, &event); err != nil {
			return fmt.Errorf("decode manual projection event: %w", err)
		}
		return syncDay(ctx, event, db, project)
	}
	if _, ok := envelope["year"]; ok {
		return fmt.Errorf("projection event mixes manual and stream fields")
	}
	if _, ok := envelope["date"]; ok {
		return fmt.Errorf("projection event mixes manual and stream fields")
	}
	var batch events.DynamoDBEvent
	if err := json.Unmarshal(raw, &batch); err != nil {
		return fmt.Errorf("decode DynamoDB stream event: %w", err)
	}
	if string(records) == "null" || batch.Records == nil {
		return fmt.Errorf("DynamoDB stream Records must be an array")
	}
	// Validate the complete batch before synchronization. Deduplication is local
	// to this invocation; the slice retains deterministic first-seen order.
	seen := make(map[Event]bool)
	var keys []Event
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

func streamKey(record events.DynamoDBEventRecord) (Event, error) {
	year, ok := record.Change.Keys["year"]
	if !ok || year.DataType() != events.DataTypeNumber {
		return Event{}, fmt.Errorf("year key must be a DynamoDB number")
	}
	date, ok := record.Change.Keys["date"]
	if !ok || date.DataType() != events.DataTypeString {
		return Event{}, fmt.Errorf("date key must be a DynamoDB string")
	}
	parsed, err := strconv.Atoi(year.Number())
	if err != nil {
		return Event{}, fmt.Errorf("year key must be an integer: %w", err)
	}
	return Event{Year: parsed, Date: date.String()}, nil
}
