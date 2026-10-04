package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

// UpdateUserResponseIfCurrent changes only response attributes, preserving date
// metadata and independently written email and finalization timestamps.
// The stored status must match expected; user states must also match its
// RespondedAt. Expected is the read snapshot used to calculate record.
func (s *Store) UpdateUserResponseIfCurrent(ctx context.Context, record tracker.DayRecord, expected tracker.DayRecord) (bool, error) {
	if !tracker.IsUserStatus(record.Status) || record.WorkFraction == nil || record.RespondedAt.IsZero() ||
		(record.ResponseSource != tracker.ResponseSourceUser && record.ResponseSource != tracker.ResponseSourceLateUser) {
		return false, fmt.Errorf("update user response %s: invalid persisted user response", record.Date)
	}
	if record.Year != expected.Year || record.Date != expected.Date {
		return false, fmt.Errorf("update user response %s: expected record key does not match", record.Date)
	}
	if tracker.IsUserStatus(expected.Status) && expected.RespondedAt.IsZero() {
		return false, fmt.Errorf("update user response %s: expected user response timestamp must not be zero", record.Date)
	}
	input := &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName), Key: dayKey(record.Year, record.Date),
		UpdateExpression:    aws.String("SET #status = :status, #work = :work, #pto = :pto, #responded = :responded, #source = :source, #changed = :changed"),
		ConditionExpression: aws.String("attribute_exists(#year) AND attribute_exists(#date) AND #status = :expected_status"),
		ExpressionAttributeNames: map[string]string{
			"#year": "year", "#date": "date", "#status": "status", "#work": "work_fraction", "#pto": "pto_fraction",
			"#responded": "responded_at", "#source": "response_source", "#changed": "has_been_changed",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":status":          &types.AttributeValueMemberS{Value: string(record.Status)},
			":expected_status": &types.AttributeValueMemberS{Value: string(expected.Status)},
			":work":            &types.AttributeValueMemberN{Value: strconv.FormatFloat(*record.WorkFraction, 'f', -1, 64)},
			":pto":             &types.AttributeValueMemberN{Value: strconv.FormatFloat(record.PTOFraction, 'f', -1, 64)},
			":responded":       &types.AttributeValueMemberS{Value: *timestampString(record.RespondedAt)},
			":source":          &types.AttributeValueMemberS{Value: string(record.ResponseSource)},
			":changed":         &types.AttributeValueMemberBOOL{Value: record.HasBeenChanged},
		},
	}
	if tracker.IsUserStatus(expected.Status) {
		input.ConditionExpression = aws.String(*input.ConditionExpression + " AND #responded = :expected_responded")
		input.ExpressionAttributeValues[":expected_responded"] = &types.AttributeValueMemberS{Value: *timestampString(expected.RespondedAt)}
	}
	_, err := s.client.UpdateItem(ctx, input)
	var conflict *types.ConditionalCheckFailedException
	if errors.As(err, &conflict) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("update user response %s: %w", record.Date, err)
	}
	return true, nil
}
