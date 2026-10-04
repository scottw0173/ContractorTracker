package main

import (
	"context"
	"fmt"
	"log"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	appconfig "github.com/scottw0173/ContractorTracker/internal/config"
	"github.com/scottw0173/ContractorTracker/internal/sheets"
	"github.com/scottw0173/ContractorTracker/internal/store"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

func newHandler(ctx context.Context) (func(context.Context, events.DynamoDBEvent) error, error) {
	settings, err := appconfig.LoadSheets()
	if err != nil {
		return nil, fmt.Errorf("load Sheets settings: %w", err)
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	client := ssm.NewFromConfig(cfg)
	db := store.New(dynamodb.NewFromConfig(cfg), settings.TableName)
	return streamHandler(db, func(ctx context.Context) (dayProjector, error) {
		credentials, err := sheets.LoadCredentials(ctx, client, settings.CredentialsParameter)
		if err != nil {
			return nil, fmt.Errorf("load Sheets credentials: %w", err)
		}
		target, err := sheets.NewClient(ctx, credentials, settings.SpreadsheetID)
		if err != nil {
			return nil, fmt.Errorf("initialize Sheets client: %w", err)
		}
		return target, nil
	}), nil
}

type dayProjector interface {
	UpsertDay(context.Context, tracker.DayRecord) error
}

// Keep the Google client scoped to this invocation, including token acquisition.
func streamHandler(db dayReader, newProjector func(context.Context) (dayProjector, error)) func(context.Context, events.DynamoDBEvent) error {
	return func(ctx context.Context, event events.DynamoDBEvent) error {
		var target dayProjector
		return handleEvent(ctx, event, db, func(ctx context.Context, record tracker.DayRecord) error {
			if target == nil {
				var err error
				target, err = newProjector(ctx)
				if err != nil {
					return err
				}
			}
			return target.UpsertDay(ctx, record)
		})
	}
}

func main() {
	handler, err := newHandler(context.Background())
	if err != nil {
		log.Fatalf("initialize sheet sync: %v", err)
	}
	lambda.Start(handler)
}
