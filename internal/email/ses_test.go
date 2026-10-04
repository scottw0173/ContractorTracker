package email

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

type fakeSES struct {
	send func(context.Context, *sesv2.SendEmailInput) (*sesv2.SendEmailOutput, error)
}

func (f fakeSES) SendEmail(ctx context.Context, in *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	return f.send(ctx, in)
}

func TestSend(t *testing.T) {
	failure := errors.New("SES rejected request")
	for _, tc := range []struct {
		name string
		err  error
	}{{"accepted", nil}, {"failed", failure}} {
		t.Run(tc.name, func(t *testing.T) {
			message := Message{Subject: "Contractor status — October 5, 2026", TextBody: "Full Day: https://example.com/respond?token=test", HTMLBody: "<p>Full Day</p>"}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			expected := &sesv2.SendEmailInput{
				FromEmailAddress: aws.String("from@example.com"),
				Destination:      &types.Destination{ToAddresses: []string{"to@example.com"}},
				Content: &types.EmailContent{Simple: &types.Message{
					Subject: &types.Content{Data: aws.String(message.Subject), Charset: aws.String("UTF-8")},
					Body:    &types.Body{Text: &types.Content{Data: aws.String(message.TextBody), Charset: aws.String("UTF-8")}, Html: &types.Content{Data: aws.String(message.HTMLBody), Charset: aws.String("UTF-8")}},
				}},
			}
			client := fakeSES{send: func(gotCtx context.Context, in *sesv2.SendEmailInput) (*sesv2.SendEmailOutput, error) {
				calls++
				if gotCtx != ctx {
					t.Fatal("context not forwarded")
				}
				if !reflect.DeepEqual(in, expected) {
					t.Fatalf("incorrect SES request: %+v", in)
				}
				return &sesv2.SendEmailOutput{MessageId: aws.String("message-id")}, tc.err
			}}
			sender, err := NewSender(client, "Contractor Tracker <from@example.com>", "Contractor <to@example.com>")
			if err != nil {
				t.Fatal(err)
			}
			err = sender.Send(ctx, message)
			if !errors.Is(err, tc.err) {
				t.Fatalf("SES error lost: %v", err)
			}
			if tc.err != nil && !strings.Contains(err.Error(), "send email via SES") {
				t.Fatal("missing send context")
			}
			if calls != 1 {
				t.Fatal("sender retried manually")
			}
		})
	}
}

func TestSenderValidation(t *testing.T) {
	client := fakeSES{}
	if sender, err := NewSender(nil, "from@example.com", "to@example.com"); err == nil || sender != nil {
		t.Fatal("nil client accepted")
	}
	for _, tc := range []struct{ name, from, to string }{
		{"empty sender", "", "to@example.com"},
		{"empty recipient", "from@example.com", ""},
		{"malformed sender", "not an address", "to@example.com"},
		{"malformed recipient", "from@example.com", "not an address"},
		{"multiple recipients", "from@example.com", "a@example.com, b@example.com"},
		{"newline injection", "from@example.com\r\nBcc: other@example.com", "to@example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if sender, err := NewSender(client, tc.from, tc.to); err == nil || sender != nil {
				t.Fatal("invalid addressing accepted")
			}
		})
	}
}
