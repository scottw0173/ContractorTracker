package store

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

// dayItem keeps storage names and optional attributes out of the domain model.
type dayItem struct {
	Year           int                    `dynamodbav:"year"`
	Date           string                 `dynamodbav:"date"`
	Status         tracker.Status         `dynamodbav:"status"`
	WorkFraction   *float64               `dynamodbav:"work_fraction,omitempty"`
	PTOFraction    float64                `dynamodbav:"pto_fraction"`
	IsWeekend      bool                   `dynamodbav:"is_weekend"`
	EmailSentAt    *string                `dynamodbav:"email_sent_at,omitempty"`
	RespondedAt    *string                `dynamodbav:"responded_at,omitempty"`
	FinalizedAt    *string                `dynamodbav:"finalized_at,omitempty"`
	ResponseSource tracker.ResponseSource `dynamodbav:"response_source"`
	HasBeenChanged bool                   `dynamodbav:"has_been_changed"`
}

func timestampString(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.Format(time.RFC3339Nano)
	return &s
}

func marshalDay(record tracker.DayRecord) (map[string]types.AttributeValue, error) {
	return attributevalue.MarshalMap(dayItem{
		Year: record.Year, Date: record.Date, Status: record.Status,
		WorkFraction: record.WorkFraction, PTOFraction: record.PTOFraction,
		IsWeekend: record.IsWeekend, HasBeenChanged: record.HasBeenChanged,
		EmailSentAt: timestampString(record.EmailSentAt), RespondedAt: timestampString(record.RespondedAt),
		FinalizedAt: timestampString(record.FinalizedAt), ResponseSource: record.ResponseSource,
	})
}

func unmarshalDay(item map[string]types.AttributeValue) (tracker.DayRecord, error) {
	var stored dayItem
	if err := attributevalue.UnmarshalMap(item, &stored); err != nil {
		return tracker.DayRecord{}, fmt.Errorf("decode day item: %w", err)
	}
	record := tracker.DayRecord{
		Year: stored.Year, Date: stored.Date, Status: stored.Status,
		WorkFraction: stored.WorkFraction, PTOFraction: stored.PTOFraction,
		IsWeekend: stored.IsWeekend, HasBeenChanged: stored.HasBeenChanged,
		ResponseSource: stored.ResponseSource,
	}
	for _, field := range []struct {
		name   string
		value  *string
		target *time.Time
	}{
		{"email_sent_at", stored.EmailSentAt, &record.EmailSentAt},
		{"responded_at", stored.RespondedAt, &record.RespondedAt},
		{"finalized_at", stored.FinalizedAt, &record.FinalizedAt},
	} {
		if field.value == nil {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, *field.value)
		if err != nil {
			return tracker.DayRecord{}, fmt.Errorf("decode %s: %w", field.name, err)
		}
		*field.target = parsed
	}
	return record, nil
}
