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

// fakeClient implements only the operations the store needs.
type fakeClient struct {
	update func(context.Context, *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error)
	get    func(context.Context, *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error)
	put    func(context.Context, *dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error)
	query  func(context.Context, *dynamodb.QueryInput) (*dynamodb.QueryOutput, error)
}

func (f fakeClient) GetItem(ctx context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return f.get(ctx, in)
}
func (f fakeClient) PutItem(ctx context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return f.put(ctx, in)
}
func (f fakeClient) Query(ctx context.Context, in *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return f.query(ctx, in)
}

func testDay(t *testing.T, date string) (tracker.DayRecord, map[string]types.AttributeValue) {
	t.Helper()
	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		t.Fatal(err)
	}
	record := tracker.NewPendingDay(day)
	item, err := marshalDay(record)
	if err != nil {
		t.Fatal(err)
	}
	return record, item
}

func TestCreateDay(t *testing.T) {
	failure := errors.New("service unavailable")
	for _, tc := range []struct {
		name    string
		err     error
		created bool
	}{
		{"created", nil, true},
		{"already exists", &types.ConditionalCheckFailedException{}, false},
		{"wrapped condition failure", fmt.Errorf("SDK: %w", &types.ConditionalCheckFailedException{}), false},
		{"other failure", failure, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record, item := testDay(t, "2026-10-03")
			ctx := context.WithValue(context.Background(), struct{}{}, "request")
			client := fakeClient{put: func(gotCtx context.Context, in *dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error) {
				if gotCtx != ctx {
					t.Fatal("context not passed through")
				}
				if aws.ToString(in.TableName) != "days" || !reflect.DeepEqual(in.Item, item) {
					t.Fatal("incorrect write")
				}
				if aws.ToString(in.ConditionExpression) != "attribute_not_exists(#year) AND attribute_not_exists(#date)" || !reflect.DeepEqual(in.ExpressionAttributeNames, map[string]string{"#year": "year", "#date": "date"}) {
					t.Fatal("missing exact-key create condition")
				}
				return &dynamodb.PutItemOutput{}, tc.err
			}}
			created, err := New(client, "days").CreateDay(ctx, record)
			if created != tc.created {
				t.Fatalf("created = %v", created)
			}
			if tc.err == failure {
				if !errors.Is(err, failure) {
					t.Fatalf("error lost: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPutDay(t *testing.T) {
	record, item := testDay(t, "2026-10-03")
	failure := errors.New("write failed")
	for _, responseError := range []error{nil, failure} {
		client := fakeClient{put: func(_ context.Context, in *dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error) {
			if aws.ToString(in.TableName) != "days" || !reflect.DeepEqual(in.Item, item) || in.ConditionExpression != nil {
				t.Fatal("expected unconditional complete item write")
			}
			return &dynamodb.PutItemOutput{}, responseError
		}}
		if err := New(client, "days").PutDay(context.Background(), record); !errors.Is(err, responseError) {
			t.Fatalf("unexpected error %v", err)
		}
	}
}

func TestGetDay(t *testing.T) {
	record, item := testDay(t, "2026-10-03")
	malformed := map[string]types.AttributeValue{"responded_at": &types.AttributeValueMemberS{Value: "invalid"}}
	failure := errors.New("read failed")
	for _, tc := range []struct {
		name        string
		item        map[string]types.AttributeValue
		err         error
		exists      bool
		decodeError bool
	}{
		{"found", item, nil, true, false},
		{"absent", nil, nil, false, false},
		{"service error", nil, failure, false, false},
		{"malformed", malformed, nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fakeClient{get: func(_ context.Context, in *dynamodb.GetItemInput) (*dynamodb.GetItemOutput, error) {
				if aws.ToString(in.TableName) != "days" || !aws.ToBool(in.ConsistentRead) || !reflect.DeepEqual(in.Key, dayKey(2026, "2026-10-03")) {
					t.Fatal("incorrect key or consistency")
				}
				return &dynamodb.GetItemOutput{Item: tc.item}, tc.err
			}}
			got, exists, err := New(client, "days").GetDay(context.Background(), 2026, "2026-10-03")
			if exists != tc.exists {
				t.Fatalf("exists = %v", exists)
			}
			if tc.decodeError {
				if err == nil {
					t.Fatal("malformed data accepted")
				}
			} else if !errors.Is(err, tc.err) {
				t.Fatalf("unexpected error %v", err)
			}
			if exists && !reflect.DeepEqual(got, record) {
				t.Fatal("incorrect decoded record")
			}
		})
	}
}

func TestListYearPagination(t *testing.T) {
	first, firstItem := testDay(t, "2026-01-01")
	second, secondItem := testDay(t, "2026-10-03")
	key := dayKey(2026, first.Date)
	calls := 0
	client := fakeClient{query: func(_ context.Context, in *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) {
		calls++
		if aws.ToString(in.TableName) != "days" || !aws.ToBool(in.ConsistentRead) || !aws.ToBool(in.ScanIndexForward) {
			t.Fatal("incorrect table, consistency, or ordering")
		}
		if aws.ToString(in.KeyConditionExpression) != "#year = :year" || !reflect.DeepEqual(in.ExpressionAttributeNames, map[string]string{"#year": "year"}) || !reflect.DeepEqual(in.ExpressionAttributeValues, map[string]types.AttributeValue{":year": &types.AttributeValueMemberN{Value: "2026"}}) {
			t.Fatal("incorrect partition query")
		}
		switch calls {
		case 1:
			if len(in.ExclusiveStartKey) != 0 {
				t.Fatal("unexpected initial cursor")
			}
			// DynamoDB may return an empty page with a continuation key.
			return &dynamodb.QueryOutput{LastEvaluatedKey: key}, nil
		case 2:
			if !reflect.DeepEqual(in.ExclusiveStartKey, key) {
				t.Fatal("pagination cursor lost")
			}
			return &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{firstItem, secondItem}}, nil
		default:
			t.Fatal("extra query")
			return nil, nil
		}
	}}
	got, err := New(client, "days").ListYear(context.Background(), 2026)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !reflect.DeepEqual(got, []tracker.DayRecord{first, second}) {
		t.Fatalf("incorrect paginated results: %+v", got)
	}
}

func TestListYearErrorsAndEmpty(t *testing.T) {
	failure := errors.New("query failed")
	for _, tc := range []struct {
		name        string
		out         *dynamodb.QueryOutput
		err         error
		decodeError bool
	}{
		{"empty", &dynamodb.QueryOutput{}, nil, false},
		{"service error", nil, failure, false},
		{"malformed", &dynamodb.QueryOutput{Items: []map[string]types.AttributeValue{{"finalized_at": &types.AttributeValueMemberS{Value: "invalid"}}}}, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fakeClient{query: func(context.Context, *dynamodb.QueryInput) (*dynamodb.QueryOutput, error) { return tc.out, tc.err }}
			got, err := New(client, "days").ListYear(context.Background(), 2026)
			if tc.decodeError {
				if err == nil {
					t.Fatal("malformed data accepted")
				}
			} else if !errors.Is(err, tc.err) {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != 0 {
				t.Fatal("unexpected records")
			}
		})
	}
}

func TestPutDayIfStatus(t *testing.T) {
	failure := errors.New("write failed")
	for _, expected := range []tracker.Status{tracker.StatusPending, tracker.StatusPTO} {
		for _, tc := range []struct {
			name    string
			err     error
			updated bool
		}{
			{"updated", nil, true},
			{"conflict", &types.ConditionalCheckFailedException{}, false},
			{"wrapped conflict", fmt.Errorf("SDK: %w", &types.ConditionalCheckFailedException{}), false},
			{"other error", failure, false},
		} {
			t.Run(string(expected)+"/"+tc.name, func(t *testing.T) {
				record, _ := testDay(t, "2026-10-03")
				record, err := tracker.FinalizePending(record, time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC))
				if err != nil {
					t.Fatal(err)
				}
				item, err := marshalDay(record)
				if err != nil {
					t.Fatal(err)
				}
				ctx := context.WithValue(context.Background(), struct{}{}, "request")
				client := fakeClient{put: func(gotCtx context.Context, in *dynamodb.PutItemInput) (*dynamodb.PutItemOutput, error) {
					if gotCtx != ctx || aws.ToString(in.TableName) != "days" || !reflect.DeepEqual(in.Item, item) {
						t.Fatal("incorrect complete record write")
					}
					if aws.ToString(in.ConditionExpression) != "#status = :expected_status" || !reflect.DeepEqual(in.ExpressionAttributeNames, map[string]string{"#status": "status"}) || !reflect.DeepEqual(in.ExpressionAttributeValues, map[string]types.AttributeValue{":expected_status": &types.AttributeValueMemberS{Value: string(expected)}}) {
						t.Fatal("incorrect expected-status condition")
					}
					return &dynamodb.PutItemOutput{}, tc.err
				}}
				updated, err := New(client, "days").PutDayIfStatus(ctx, record, expected)
				if updated != tc.updated {
					t.Fatalf("updated = %v", updated)
				}
				if tc.err == failure {
					if !errors.Is(err, failure) {
						t.Fatalf("error lost: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func (f fakeClient) UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return f.update(ctx, in)
}

func TestMarkEmailSent(t *testing.T) {
	failure := errors.New("update failed")
	at := time.Date(2026, 10, 5, 12, 30, 45, 123456789, time.FixedZone("app", -7*3600))
	for _, tc := range []struct {
		name    string
		err     error
		updated bool
	}{
		{"updated", nil, true}, {"conflict", &types.ConditionalCheckFailedException{}, false},
		{"wrapped conflict", fmt.Errorf("SDK: %w", &types.ConditionalCheckFailedException{}), false}, {"failure", failure, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := fakeClient{update: func(gotCtx context.Context, in *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
				want := &dynamodb.UpdateItemInput{
					TableName: aws.String("days"), Key: dayKey(2026, "2026-10-05"),
					UpdateExpression:          aws.String("SET #sent = :sent_at"),
					ConditionExpression:       aws.String("attribute_exists(#year) AND attribute_exists(#date) AND attribute_not_exists(#sent)"),
					ExpressionAttributeNames:  map[string]string{"#year": "year", "#date": "date", "#sent": "email_sent_at"},
					ExpressionAttributeValues: map[string]types.AttributeValue{":sent_at": &types.AttributeValueMemberS{Value: at.Format(time.RFC3339Nano)}},
				}
				if gotCtx != ctx || !reflect.DeepEqual(in, want) {
					t.Fatal("incorrect timestamp-only conditional update or context")
				}
				return &dynamodb.UpdateItemOutput{}, tc.err
			}}
			updated, err := New(client, "days").MarkEmailSent(ctx, 2026, "2026-10-05", at)
			if updated != tc.updated {
				t.Fatalf("updated = %v", updated)
			}
			if tc.err == failure {
				if !errors.Is(err, failure) {
					t.Fatalf("error lost: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	client := fakeClient{update: func(context.Context, *dynamodb.UpdateItemInput) (*dynamodb.UpdateItemOutput, error) {
		t.Fatal("zero time reached client")
		return nil, nil
	}}
	if updated, err := New(client, "days").MarkEmailSent(context.Background(), 2026, "2026-10-05", time.Time{}); updated || err == nil {
		t.Fatal("zero timestamp accepted")
	}
}
