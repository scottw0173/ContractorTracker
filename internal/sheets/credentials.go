// Package sheets loads service-account credentials and constructs Google Sheets
// clients. It does not synchronize or mutate spreadsheets.
package sheets

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

// SSMClient is the single Parameter Store operation required for credentials.
type SSMClient interface {
	GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

var _ SSMClient = (*ssm.Client)(nil)

// LoadCredentials fetches one configured parameter with decryption enabled.
// Returned bytes remain in memory; no credential contents are included in errors.
func LoadCredentials(ctx context.Context, client SSMClient, parameterName string) ([]byte, error) {
	if client == nil {
		return nil, fmt.Errorf("Google credentials require an SSM client")
	}
	if strings.TrimSpace(parameterName) == "" {
		return nil, fmt.Errorf("Google credentials parameter must not be blank")
	}
	out, err := client.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(parameterName), WithDecryption: aws.Bool(true)})
	if err != nil {
		return nil, fmt.Errorf("get Google credentials parameter: %w", err)
	}
	if out == nil || out.Parameter == nil || out.Parameter.Value == nil || strings.TrimSpace(*out.Parameter.Value) == "" {
		return nil, fmt.Errorf("Google credentials parameter has no non-blank value")
	}
	return []byte(*out.Parameter.Value), nil
}
