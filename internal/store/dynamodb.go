// Package store persists tracker records in DynamoDB.
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

// DynamoDBClient is the subset of the SDK client used by Store.
type DynamoDBClient interface {
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	Query(context.Context, *dynamodb.QueryInput, ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

var _ DynamoDBClient = (*dynamodb.Client)(nil)

type Store struct {
	client    DynamoDBClient
	tableName string
}

// New receives an already configured client and the target table name.
func New(client DynamoDBClient, tableName string) *Store {
	return &Store{client: client, tableName: tableName}
}

func dayKey(year int, date string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"year": &types.AttributeValueMemberN{Value: strconv.Itoa(year)},
		"date": &types.AttributeValueMemberS{Value: date},
	}
}

// GetDay returns a zero record and exists=false when the item is absent.
func (s *Store) GetDay(ctx context.Context, year int, date string) (tracker.DayRecord, bool, error) {
	out, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName), Key: dayKey(year, date), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return tracker.DayRecord{}, false, fmt.Errorf("get day: %w", err)
	}
	if len(out.Item) == 0 {
		return tracker.DayRecord{}, false, nil
	}
	record, err := unmarshalDay(out.Item)
	if err != nil {
		return tracker.DayRecord{}, false, err
	}
	return record, true, nil
}

// CreateDay conditionally creates the exact year/date item without overwriting it.
func (s *Store) CreateDay(ctx context.Context, record tracker.DayRecord) (bool, error) {
	item, err := marshalDay(record)
	if err != nil {
		return false, fmt.Errorf("encode day: %w", err)
	}
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName), Item: item,
		ConditionExpression:      aws.String("attribute_not_exists(#year) AND attribute_not_exists(#date)"),
		ExpressionAttributeNames: map[string]string{"#year": "year", "#date": "date"},
	})
	var conflict *types.ConditionalCheckFailedException
	if errors.As(err, &conflict) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("create day: %w", err)
	}
	return true, nil
}

// PutDay replaces the complete item unconditionally. Callers supply an existing
// day's current representation; existence and concurrency are not checked here.
func (s *Store) PutDay(ctx context.Context, record tracker.DayRecord) error {
	item, err := marshalDay(record)
	if err != nil {
		return fmt.Errorf("encode day: %w", err)
	}
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.tableName), Item: item})
	if err != nil {
		return fmt.Errorf("put day: %w", err)
	}
	return nil
}

// PutDayIfStatus replaces the complete item only while its stored status matches
// expectedStatus. A missing item or a changed status returns updated=false.
func (s *Store) PutDayIfStatus(ctx context.Context, record tracker.DayRecord, expectedStatus tracker.Status) (bool, error) {
	item, err := marshalDay(record)
	if err != nil {
		return false, fmt.Errorf("encode day: %w", err)
	}
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName), Item: item,
		ConditionExpression:      aws.String("#status = :expected_status"),
		ExpressionAttributeNames: map[string]string{"#status": "status"},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":expected_status": &types.AttributeValueMemberS{Value: string(expectedStatus)},
		},
	})
	var conflict *types.ConditionalCheckFailedException
	if errors.As(err, &conflict) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("put day if status: %w", err)
	}
	return true, nil
}

// ListYear queries every page of the year partition in ascending date order.
// Dates must use ISO YYYY-MM-DD so sort-key order is chronological.
func (s *Store) ListYear(ctx context.Context, year int) ([]tracker.DayRecord, error) {
	input := &dynamodb.QueryInput{
		TableName: aws.String(s.tableName), ConsistentRead: aws.Bool(true), ScanIndexForward: aws.Bool(true),
		KeyConditionExpression:    aws.String("#year = :year"),
		ExpressionAttributeNames:  map[string]string{"#year": "year"},
		ExpressionAttributeValues: map[string]types.AttributeValue{":year": &types.AttributeValueMemberN{Value: strconv.Itoa(year)}},
	}
	records := make([]tracker.DayRecord, 0)
	for {
		out, err := s.client.Query(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("list year: %w", err)
		}
		for _, item := range out.Items {
			record, err := unmarshalDay(item)
			if err != nil {
				return nil, err
			}
			records = append(records, record)
		}
		if len(out.LastEvaluatedKey) == 0 {
			return records, nil
		}
		input.ExclusiveStartKey = out.LastEvaluatedKey
	}
}
