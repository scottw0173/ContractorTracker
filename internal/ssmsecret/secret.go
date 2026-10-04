// Package ssmsecret retrieves confidential bytes from Parameter Store without
// coupling retrieval to any application-specific use of the value.
package ssmsecret

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
)

type Client interface {
	GetParameter(context.Context, *ssm.GetParameterInput, ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
}

var _ Client = (*ssm.Client)(nil)

// retrievalError hides arbitrary SDK error text from startup logs while retaining
// the cause for errors.Is/errors.As. Do not log an explicitly unwrapped cause.
type retrievalError struct{ cause error }

func (e *retrievalError) Error() string { return "retrieve confidential SSM parameter failed" }
func (e *retrievalError) Unwrap() error { return e.cause }

// Load fetches once with decryption enabled and preserves every value byte.
// Production secrets should be SecureString parameters; String is also accepted.
func Load(ctx context.Context, client Client, parameterName string) ([]byte, error) {
	if client == nil || (reflect.ValueOf(client).Kind() == reflect.Ptr && reflect.ValueOf(client).IsNil()) {
		return nil, errors.New("confidential SSM parameter requires a client")
	}
	if strings.TrimSpace(parameterName) == "" {
		return nil, errors.New("confidential SSM parameter name must not be blank")
	}
	out, err := client.GetParameter(ctx, &ssm.GetParameterInput{Name: aws.String(parameterName), WithDecryption: aws.Bool(true)})
	if err != nil {
		return nil, &retrievalError{cause: err}
	}
	if out == nil || out.Parameter == nil || out.Parameter.Value == nil || strings.TrimSpace(*out.Parameter.Value) == "" {
		return nil, errors.New("confidential SSM parameter has no non-blank value")
	}
	return []byte(*out.Parameter.Value), nil
}
