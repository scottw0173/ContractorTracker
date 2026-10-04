package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

func TestUpdateUserResponse(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 30, 45, 123456789, time.FixedZone("app", -7*3600))
	for _, tc := range []struct {
		status    tracker.Status
		work, pto string
	}{
		{tracker.StatusFullDay, "1", "0"}, {tracker.StatusHalfDay, "0.5", "0"},
		{tracker.StatusPTO, "0", "1"}, {tracker.StatusTimeOff, "0", "0"},
	} {
		for _, changed := range []bool{false, true} {
			for _, source := range []tracker.ResponseSource{tracker.ResponseSourceUser, tracker.ResponseSourceLateUser} {
				t.Run(fmt.Sprintf("%s/%v/%s", tc.status, changed, source), func(t *testing.T) {
					record, err := tracker.ApplyUserStatus(tracker.NewPendingDay(at), tc.status, at)
					if err != nil {
						t.Fatal(err)
					}
					record.HasBeenChanged, record.ResponseSource = changed, source
					record.EmailSentAt, record.FinalizedAt = at, at
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					client := fakeClient{update: func(gotCtx context.Context, in *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
						want := &dynamodb.UpdateItemInput{
							TableName: aws.String("days"), Key: dayKey(2026, "2026-10-05"),
							UpdateExpression:         aws.String("SET #status = :status, #work = :work, #pto = :pto, #responded = :responded, #source = :source, #changed = :changed"),
							ConditionExpression:      aws.String("attribute_exists(#year) AND attribute_exists(#date) AND #status = :expected_status"),
							ExpressionAttributeNames: map[string]string{"#year": "year", "#date": "date", "#status": "status", "#work": "work_fraction", "#pto": "pto_fraction", "#responded": "responded_at", "#source": "response_source", "#changed": "has_been_changed"},
							ExpressionAttributeValues: map[string]types.AttributeValue{
								":status": &types.AttributeValueMemberS{Value: string(tc.status)}, ":expected_status": &types.AttributeValueMemberS{Value: "PENDING"},
								":work": &types.AttributeValueMemberN{Value: tc.work}, ":pto": &types.AttributeValueMemberN{Value: tc.pto},
								":responded": &types.AttributeValueMemberS{Value: at.Format(time.RFC3339Nano)},
								":source":    &types.AttributeValueMemberS{Value: string(source)}, ":changed": &types.AttributeValueMemberBOOL{Value: changed},
							},
						}
						if gotCtx != ctx || !reflect.DeepEqual(in, want) {
							t.Fatal("incorrect six-attribute response update or context")
						}
						return &dynamodb.UpdateItemOutput{}, nil
					}}
					if updated, err := New(client, "days").UpdateUserResponseIfCurrent(ctx, record, tracker.NewPendingDay(at)); !updated || err != nil {
						t.Fatalf("got %v, %v", updated, err)
					}
				})
			}
		}
	}
}

func TestInvalidUserResponse(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	valid, err := tracker.ApplyUserStatus(tracker.NewPendingDay(at), tracker.StatusPTO, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*tracker.DayRecord)
	}{
		{"pending", func(r *tracker.DayRecord) { r.Status = tracker.StatusPending }},
		{"no response", func(r *tracker.DayRecord) { r.Status = tracker.StatusNoResponse }},
		{"unknown", func(r *tracker.DayRecord) { r.Status = "unknown" }},
		{"nil work", func(r *tracker.DayRecord) { r.WorkFraction = nil }},
		{"zero time", func(r *tracker.DayRecord) { r.RespondedAt = time.Time{} }},
		{"auto source", func(r *tracker.DayRecord) { r.ResponseSource = tracker.ResponseSourceAutoFinalize }},
		{"unknown source", func(r *tracker.DayRecord) { r.ResponseSource = "unknown" }},
		{"empty source", func(r *tracker.DayRecord) { r.ResponseSource = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := valid
			tc.mutate(&record)
			client := fakeClient{update: func(context.Context, *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
				t.Fatal("invalid record reached DynamoDB")
				return nil, nil
			}}
			if updated, err := New(client, "days").UpdateUserResponseIfCurrent(context.Background(), record, tracker.NewPendingDay(at)); updated || err == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}
}

func TestResponseUpdateErrorsAndExpectedStatus(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	record, err := tracker.ApplyUserStatus(tracker.NewPendingDay(at), tracker.StatusFullDay, at)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("service failure")
	for _, expected := range []tracker.Status{tracker.StatusNoResponse, tracker.StatusFullDay, tracker.StatusPTO} {
		for _, responseErr := range []error{&types.ConditionalCheckFailedException{}, fmt.Errorf("wrapped: %w", &types.ConditionalCheckFailedException{}), failure} {
			client := fakeClient{update: func(_ context.Context, in *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
				value := in.ExpressionAttributeValues[":expected_status"].(*types.AttributeValueMemberS)
				if value.Value != string(expected) {
					t.Fatal("expected status lost")
				}
				return nil, responseErr
			}}
			updated, err := New(client, "days").UpdateUserResponseIfCurrent(context.Background(), record, tracker.DayRecord{Year: record.Year, Date: record.Date, Status: expected, RespondedAt: at})
			if updated {
				t.Fatal("failed update reported success")
			}
			if responseErr == failure {
				if !errors.Is(err, failure) {
					t.Fatal("storage error lost")
				}
			} else if err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestResponseExpectedSnapshotCondition(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 30, 45, 123456789, time.FixedZone("app", -7*3600))
	record, err := tracker.ApplyUserStatus(tracker.NewPendingDay(at), tracker.StatusPTO, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []tracker.Status{tracker.StatusPending, tracker.StatusNoResponse, tracker.StatusFullDay, tracker.StatusHalfDay, tracker.StatusPTO, tracker.StatusTimeOff} {
		t.Run(string(status), func(t *testing.T) {
			expected := record
			expected.Status, expected.RespondedAt = status, at.Add(-time.Minute)
			client := fakeClient{update: func(_ context.Context, in *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
				want := "attribute_exists(#year) AND attribute_exists(#date) AND #status = :expected_status"
				value, present := in.ExpressionAttributeValues[":expected_responded"]
				if tracker.IsUserStatus(status) {
					want += " AND #responded = :expected_responded"
					if !reflect.DeepEqual(value, &types.AttributeValueMemberS{Value: expected.RespondedAt.Format(time.RFC3339Nano)}) {
						t.Fatalf("expected timestamp lost precision: %v", value)
					}
				} else if present {
					t.Fatal("non-user state must not condition on responded_at")
				}
				if aws.ToString(in.ConditionExpression) != want || in.ExpressionAttributeNames["#responded"] != "responded_at" || !reflect.DeepEqual(in.ExpressionAttributeValues[":expected_status"], &types.AttributeValueMemberS{Value: string(status)}) {
					t.Fatal("incorrect snapshot condition")
				}
				return &dynamodb.UpdateItemOutput{}, nil
			}}
			if updated, err := New(client, "days").UpdateUserResponseIfCurrent(context.Background(), record, expected); !updated || err != nil {
				t.Fatalf("got %v, %v", updated, err)
			}
		})
	}
}

func TestInvalidExpectedResponseSnapshot(t *testing.T) {
	at := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	record, err := tracker.ApplyUserStatus(tracker.NewPendingDay(at), tracker.StatusFullDay, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*tracker.DayRecord)
	}{
		{"year mismatch", func(r *tracker.DayRecord) { r.Year++ }},
		{"date mismatch", func(r *tracker.DayRecord) { r.Date = "2026-10-06" }},
		{"zero user timestamp", func(r *tracker.DayRecord) { r.RespondedAt = time.Time{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := record
			tc.mutate(&expected)
			client := fakeClient{update: func(context.Context, *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
				t.Fatal("invalid expected snapshot reached DynamoDB")
				return nil, nil
			}}
			if updated, err := New(client, "days").UpdateUserResponseIfCurrent(context.Background(), record, expected); updated || err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
}
