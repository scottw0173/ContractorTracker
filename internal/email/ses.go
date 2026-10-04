package email

import (
	"context"
	"fmt"
	"net/mail"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

// SESClient contains only the SES operation used by Sender.
type SESClient interface {
	SendEmail(context.Context, *sesv2.SendEmailInput, ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

var _ SESClient = (*sesv2.Client)(nil)

type Sender struct {
	client SESClient
	from   string
	to     string
}

// NewSender validates one sender and one recipient without checking SES identity
// authorization. Display names are accepted; the parsed mailbox is sent to SES.
func NewSender(client SESClient, from, to string) (*Sender, error) {
	if client == nil {
		return nil, fmt.Errorf("email sender requires an SES client")
	}
	sender, err := mail.ParseAddress(from)
	if err != nil {
		return nil, fmt.Errorf("invalid sender address: %w", err)
	}
	recipient, err := mail.ParseAddress(to)
	if err != nil {
		return nil, fmt.Errorf("invalid recipient address: %w", err)
	}
	return &Sender{client: client, from: sender.Address, to: recipient.Address}, nil
}

// Send submits a simple email to SES. Success means SES accepted the request;
// this method neither retries nor updates any day record.
func (s *Sender) Send(ctx context.Context, message Message) error {
	_, err := s.client.SendEmail(ctx, &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(s.from),
		Destination:      &types.Destination{ToAddresses: []string{s.to}},
		Content: &types.EmailContent{Simple: &types.Message{
			Subject: &types.Content{Data: aws.String(message.Subject), Charset: aws.String("UTF-8")},
			Body: &types.Body{
				Text: &types.Content{Data: aws.String(message.TextBody), Charset: aws.String("UTF-8")},
				Html: &types.Content{Data: aws.String(message.HTMLBody), Charset: aws.String("UTF-8")},
			},
		}},
	})
	if err != nil {
		return fmt.Errorf("send email via SES: %w", err)
	}
	return nil
}
