package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	appconfig "github.com/scottw0173/ContractorTracker/internal/config"
	"github.com/scottw0173/ContractorTracker/internal/token"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

type fakeParameters struct {
	calls int
	err   error
	ctx   context.Context
}

const fixtureSecret = " \tfixture-only-existing-HMAC-secret\n"

func (f *fakeParameters) GetParameter(ctx context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.calls++
	f.ctx = ctx
	if aws.ToString(in.Name) != "configured-token-secret" || !aws.ToBool(in.WithDecryption) {
		return nil, errors.New("wrong SSM request")
	}
	return &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: aws.String(fixtureSecret)}}, f.err
}

type fakeDays struct {
	record tracker.DayRecord
	exists bool
}

func (f *fakeDays) GetDay(_ context.Context, year int, date string) (tracker.DayRecord, bool, error) {
	return f.record, f.exists && f.record.Year == year && f.record.Date == date, nil
}
func (f *fakeDays) CreateDay(_ context.Context, record tracker.DayRecord) (bool, error) {
	if f.exists {
		return false, nil
	}
	f.record = record
	f.exists = true
	return true, nil
}
func (f *fakeDays) PutDayIfStatus(context.Context, tracker.DayRecord, tracker.Status) (bool, error) {
	return false, errors.New("unexpected finalization")
}
func (f *fakeDays) MarkEmailSent(_ context.Context, _ int, _ string, at time.Time) (bool, error) {
	f.record.EmailSentAt = at
	return true, nil
}

type fakeSES struct {
	calls   int
	message *sesv2.SendEmailInput
}

func (f *fakeSES) SendEmail(_ context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	f.calls++
	f.message = in
	return &sesv2.SendEmailOutput{}, nil
}
func dailySettings() appconfig.Daily {
	return appconfig.Daily{TableName: "days", TokenSecretParameter: "configured-token-secret", Location: time.UTC, ResponseBaseURL: "https://example.com/respond", EmailFrom: "from@example.com", EmailTo: "to@example.com"}
}

func TestInitializationUsesSSMSecretOnce(t *testing.T) {
	parameters, db, ses := &fakeParameters{}, &fakeDays{}, &fakeSES{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner, err := buildRunner(ctx, dailySettings(), db, ses, parameters)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if err := runner.Run(ctx, now); err != nil {
			t.Fatal(err)
		}
	}
	if parameters.calls != 1 || parameters.ctx != ctx || ses.calls != 1 {
		t.Fatal("secret retrieval was not cold-start-only")
	}
	signer, err := token.New([]byte(fixtureSecret))
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[tracker.Status]bool)
	for _, line := range strings.Split(aws.ToString(ses.message.Content.Simple.Body.Text.Data), "\n") {
		start := strings.Index(line, "https://example.com/respond?")
		if start < 0 {
			continue
		}
		link, err := url.Parse(strings.TrimSpace(line[start:]))
		if err != nil {
			t.Fatal(err)
		}
		claims, err := signer.Verify(link.Query().Get("token"))
		if err != nil || claims.Date != "2026-10-04" {
			t.Fatal("email links did not use exact retrieved secret")
		}
		seen[claims.Status] = true
	}
	for _, status := range []tracker.Status{tracker.StatusFullDay, tracker.StatusHalfDay, tracker.StatusPTO, tracker.StatusTimeOff} {
		if !seen[status] {
			t.Fatal("missing signed status link")
		}
	}
}

func TestInitializationSSMFailureSafe(t *testing.T) {
	cause := errors.New(fixtureSecret)
	parameters := &fakeParameters{err: cause}
	ses := &fakeSES{}
	runner, err := buildRunner(context.Background(), dailySettings(), &fakeDays{}, ses, parameters)
	if runner != nil || !errors.Is(err, cause) || strings.Contains(err.Error(), fixtureSecret) || ses.calls != 0 {
		t.Fatal("unsafe or partial initialization")
	}
}
