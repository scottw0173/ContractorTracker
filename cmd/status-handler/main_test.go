package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	appconfig "github.com/scottw0173/ContractorTracker/internal/config"
	"github.com/scottw0173/ContractorTracker/internal/token"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

const fixtureSecret = " \tfixture-only-existing-HMAC-secret\n"

type fakeParameters struct {
	calls int
	err   error
	ctx   context.Context
}

func (f *fakeParameters) GetParameter(ctx context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.calls++
	f.ctx = ctx
	if aws.ToString(in.Name) != "configured-token-secret" || !aws.ToBool(in.WithDecryption) {
		return nil, errors.New("wrong SSM request")
	}
	return &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: aws.String(fixtureSecret)}}, f.err
}

type fakeDays struct{ reads int }

func (f *fakeDays) GetDay(_ context.Context, year int, date string) (tracker.DayRecord, bool, error) {
	f.reads++
	return tracker.DayRecord{Year: year, Date: date, Status: tracker.StatusPending}, true, nil
}
func (f *fakeDays) UpdateUserResponseIfCurrent(context.Context, tracker.DayRecord, tracker.DayRecord) (bool, error) {
	return false, errors.New("GET attempted mutation")
}
func statusSettings() appconfig.Status {
	return appconfig.Status{TableName: "days", TokenSecretParameter: "configured-token-secret"}
}

func TestInitializationVerifiesOldLinksWithLoadedSecret(t *testing.T) {
	// Simulate a link generated before migration with exactly the same secret.
	signer, err := token.New([]byte(fixtureSecret))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signer.Sign(token.Claims{Date: "2026-10-03", Status: tracker.StatusPTO})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	parameters, db := &fakeParameters{}, &fakeDays{}
	handler, err := buildHandler(ctx, statusSettings(), db, parameters)
	if err != nil {
		t.Fatal(err)
	}
	request := events.LambdaFunctionURLRequest{RawPath: "/respond", QueryStringParameters: map[string]string{"token": raw}}
	request.RequestContext.HTTP.Method = "GET"
	for i := 0; i < 2; i++ {
		response, err := handler.Handle(ctx, request, time.Time{})
		if err != nil || response.StatusCode != 200 || !strings.Contains(response.Body, "PTO") {
			t.Fatal("old token was not verified by loaded secret")
		}
	}
	if parameters.calls != 1 || parameters.ctx != ctx || db.reads != 2 {
		t.Fatal("SSM was not cold-start-only")
	}
}
func TestInitializationSSMFailureSafe(t *testing.T) {
	cause := errors.New(fixtureSecret)
	db := &fakeDays{}
	handler, err := buildHandler(context.Background(), statusSettings(), db, &fakeParameters{err: cause})
	if handler != nil || !errors.Is(err, cause) || strings.Contains(err.Error(), fixtureSecret) || db.reads != 0 {
		t.Fatal("unsafe or partial initialization")
	}
}
