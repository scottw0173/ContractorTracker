package store

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

func TestDayRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 3, 12, 30, 45, 123456789, time.FixedZone("app", -7*60*60))
	for _, status := range []tracker.Status{tracker.StatusPending, tracker.StatusFullDay, tracker.StatusHalfDay, tracker.StatusTimeOff, tracker.StatusPTO, tracker.StatusNoResponse} {
		for _, populated := range []bool{false, true} {
			name := string(status)
			if populated {
				name += "/timestamps_and_true_booleans"
			}
			t.Run(name, func(t *testing.T) {
				record := tracker.NewPendingDay(at)
				record.IsWeekend = populated
				if status == tracker.StatusNoResponse {
					record, _ = tracker.FinalizePending(record, at)
				} else if status != tracker.StatusPending {
					record, _ = tracker.ApplyUserStatus(record, status, at)
				}
				if populated {
					record.EmailSentAt, record.RespondedAt, record.FinalizedAt = at, at.Add(time.Minute), at.Add(time.Hour)
					record.HasBeenChanged = true
				} else {
					record.EmailSentAt, record.RespondedAt, record.FinalizedAt = time.Time{}, time.Time{}, time.Time{}
				}
				item, err := marshalDay(record)
				if err != nil {
					t.Fatal(err)
				}
				if year, ok := item["year"].(*types.AttributeValueMemberN); !ok || year.Value != "2026" {
					t.Fatal("year must be numeric")
				}
				if date, ok := item["date"].(*types.AttributeValueMemberS); !ok || date.Value != record.Date {
					t.Fatal("incorrect date key")
				}
				for key, want := range map[string]bool{"is_weekend": record.IsWeekend, "has_been_changed": record.HasBeenChanged} {
					value, ok := item[key].(*types.AttributeValueMemberBOOL)
					if !ok || value.Value != want {
						t.Fatalf("%s missing or incorrect", key)
					}
				}
				work, exists := item["work_fraction"]
				if record.WorkFraction == nil {
					if exists {
						t.Fatal("nil work fraction was persisted")
					}
				} else {
					if _, ok := work.(*types.AttributeValueMemberN); !ok {
						t.Fatal("work fraction must be numeric, including zero")
					}
				}
				if _, ok := item["pto_fraction"].(*types.AttributeValueMemberN); !ok {
					t.Fatal("PTO fraction missing")
				}
				for _, key := range []string{"email_sent_at", "responded_at", "finalized_at"} {
					value, exists := item[key]
					if exists != populated {
						t.Fatalf("unexpected timestamp presence for %s", key)
					}
					if populated {
						text, ok := value.(*types.AttributeValueMemberS)
						if !ok {
							t.Fatal("timestamp must be a string")
						}
						if _, err := time.Parse(time.RFC3339, text.Value); err != nil {
							t.Fatal(err)
						}
					}
				}
				got, err := unmarshalDay(item)
				if err != nil {
					t.Fatal(err)
				}
				if !got.EmailSentAt.Equal(record.EmailSentAt) || !got.RespondedAt.Equal(record.RespondedAt) || !got.FinalizedAt.Equal(record.FinalizedAt) {
					t.Fatal("timestamp instant or precision changed")
				}
				// Timezone names and monotonic clock readings are not part of persistence.
				got.EmailSentAt, got.RespondedAt, got.FinalizedAt = record.EmailSentAt, record.RespondedAt, record.FinalizedAt
				if !reflect.DeepEqual(got, record) {
					t.Fatalf("got %+v, want %+v", got, record)
				}
			})
		}
	}
}

func TestMalformedTimestamps(t *testing.T) {
	for _, key := range []string{"email_sent_at", "responded_at", "finalized_at"} {
		for _, bad := range []types.AttributeValue{
			&types.AttributeValueMemberS{Value: "not-a-time"},
			&types.AttributeValueMemberS{Value: ""},
			&types.AttributeValueMemberN{Value: "123"},
		} {
			t.Run(key, func(t *testing.T) {
				item, err := marshalDay(tracker.DayRecord{Year: 2026, Date: "2026-10-03", Status: tracker.StatusPending})
				if err != nil {
					t.Fatal(err)
				}
				item[key] = bad
				if _, err := unmarshalDay(item); err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("expected field-specific timestamp error, got %v", err)
				}
			})
		}
	}
}

func TestAdminBackfillRoundTrip(t *testing.T) {
	day := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	at := day.Add(12 * time.Hour)
	for _, status := range []tracker.Status{tracker.StatusFullDay, tracker.StatusHalfDay, tracker.StatusPTO, tracker.StatusTimeOff} {
		t.Run(string(status), func(t *testing.T) {
			record, err := tracker.NewAdminBackfillDay(day, status, at)
			if err != nil {
				t.Fatal(err)
			}
			item, err := marshalDay(record)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"email_sent_at", "finalized_at"} {
				if _, exists := item[key]; exists {
					t.Fatalf("fabricated %s", key)
				}
			}
			source, ok := item["response_source"].(*types.AttributeValueMemberS)
			if !ok || source.Value != "ADMIN_BACKFILL" {
				t.Fatal("backfill provenance not persisted")
			}
			got, err := unmarshalDay(item)
			if err != nil || !reflect.DeepEqual(got, record) {
				t.Fatalf("round trip: %+v %v", got, err)
			}
		})
	}
}
