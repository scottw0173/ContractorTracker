package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	appconfig "github.com/scottw0173/ContractorTracker/internal/config"
	"github.com/scottw0173/ContractorTracker/internal/respond"
	"github.com/scottw0173/ContractorTracker/internal/respondweb"
	"github.com/scottw0173/ContractorTracker/internal/ssmsecret"
	"github.com/scottw0173/ContractorTracker/internal/store"
	"github.com/scottw0173/ContractorTracker/internal/token"
)

func newHandler(ctx context.Context) (*respondweb.Handler, error) {
	settings, err := appconfig.LoadStatus()
	if err != nil {
		return nil, fmt.Errorf("load status settings: %w", err)
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS configuration: %w", err)
	}
	return buildHandler(ctx, settings, store.New(dynamodb.NewFromConfig(cfg), settings.TableName), ssm.NewFromConfig(cfg))
}

func buildHandler(ctx context.Context, settings appconfig.Status, db respond.DayStore, parameters ssmsecret.Client) (*respondweb.Handler, error) {
	secret, err := ssmsecret.Load(ctx, parameters, settings.TokenSecretParameter)
	if err != nil {
		return nil, fmt.Errorf("load token secret: %w", err)
	}
	signer, err := token.New(secret)
	if err != nil {
		return nil, fmt.Errorf("create signer: %w", err)
	}
	service, err := respond.New(db, signer)
	if err != nil {
		return nil, fmt.Errorf("create response service: %w", err)
	}
	return respondweb.New(service)
}

func main() {
	handler, err := newHandler(context.Background())
	if err != nil {
		log.Fatalf("initialize status handler: %v", err)
	}
	lambda.Start(func(ctx context.Context, request events.LambdaFunctionURLRequest) (events.LambdaFunctionURLResponse, error) {
		return handler.Handle(ctx, request, time.Now())
	})
}
