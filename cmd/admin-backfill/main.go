package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/scottw0173/ContractorTracker/internal/store"
)

func main() {
	options, err := parseOptions(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err == nil {
		ctx := context.Background()
		cfg, loadErr := awsconfig.LoadDefaultConfig(ctx)
		if loadErr != nil {
			err = fmt.Errorf("load AWS configuration: %w", loadErr)
		} else {
			db := store.New(dynamodb.NewFromConfig(cfg), options.table)
			err = backfillDay(ctx, db, options, os.Stdin, os.Stdout, time.Now().UTC())
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
