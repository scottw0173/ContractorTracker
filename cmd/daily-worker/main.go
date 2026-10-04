package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	appconfig "github.com/scottw0173/ContractorTracker/internal/config"
	"github.com/scottw0173/ContractorTracker/internal/daily"
	"github.com/scottw0173/ContractorTracker/internal/email"
	"github.com/scottw0173/ContractorTracker/internal/store"
	"github.com/scottw0173/ContractorTracker/internal/token"
)

func newRunner(ctx context.Context) (*daily.Runner, error) {
	settings, err := appconfig.LoadDaily()
	if err != nil {
		return nil, fmt.Errorf("load daily settings: %w", err)
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	db := store.New(dynamodb.NewFromConfig(cfg), settings.TableName)
	signer, err := token.New(settings.TokenSecret)
	if err != nil {
		return nil, fmt.Errorf("create signer: %w", err)
	}
	builder, err := email.NewBuilder(signer, settings.ResponseBaseURL)
	if err != nil {
		return nil, fmt.Errorf("create email builder: %w", err)
	}
	sender, err := email.NewSender(sesv2.NewFromConfig(cfg), settings.EmailFrom, settings.EmailTo)
	if err != nil {
		return nil, fmt.Errorf("create email sender: %w", err)
	}
	return daily.New(db, builder, sender, settings.Location)
}

func main() {
	runner, err := newRunner(context.Background())
	if err != nil {
		log.Fatalf("initialize daily worker: %v", err)
	}
	lambda.Start(func(ctx context.Context) error {
		return runner.Run(ctx, time.Now())
	})
}
