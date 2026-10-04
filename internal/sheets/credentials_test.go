package sheets

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

type fakeSSM struct {
	get func(context.Context, *ssm.GetParameterInput) (*ssm.GetParameterOutput, error)
}

func (f fakeSSM) GetParameter(ctx context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	return f.get(ctx, in)
}

func TestLoadCredentials(t *testing.T) {
	failure := errors.New("SSM unavailable")
	for _, tc := range []struct {
		name string
		out  *ssm.GetParameterOutput
		err  error
		want string
	}{
		{"value", &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: aws.String(" {\"test\":true} \n")}}, nil, " {\"test\":true} \n"},
		{"AWS error", nil, failure, ""},
		{"missing output", nil, nil, ""},
		{"missing parameter", &ssm.GetParameterOutput{}, nil, ""},
		{"missing value", &ssm.GetParameterOutput{Parameter: &types.Parameter{}}, nil, ""},
		{"empty value", &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: aws.String("")}}, nil, ""},
		{"blank value", &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: aws.String(" \t\n")}}, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			client := fakeSSM{get: func(gotCtx context.Context, in *ssm.GetParameterInput) (*ssm.GetParameterOutput, error) {
				calls++
				if gotCtx != ctx || !reflect.DeepEqual(in, &ssm.GetParameterInput{Name: aws.String("test-google-key"), WithDecryption: aws.Bool(true)}) {
					t.Fatal("wrong parameter, decryption flag, or context")
				}
				return tc.out, tc.err
			}}
			got, err := LoadCredentials(ctx, client, "test-google-key")
			if calls != 1 {
				t.Fatal("expected one retrieval")
			}
			if tc.want != "" {
				if err != nil || string(got) != tc.want {
					t.Fatal("credential value changed")
				}
				got[0] = 'x'
				if *tc.out.Parameter.Value != tc.want {
					t.Fatal("credentials retained mutable backing state")
				}
			} else if err == nil || got != nil {
				t.Fatal("invalid result accepted")
			}
			if tc.err != nil && !errors.Is(err, tc.err) {
				t.Fatal("AWS cause lost")
			}
		})
	}
}
func TestLoadCredentialsInvalidDependencies(t *testing.T) {
	client := fakeSSM{get: func(context.Context, *ssm.GetParameterInput) (*ssm.GetParameterOutput, error) {
		t.Fatal("invalid input reached SSM")
		return nil, nil
	}}
	for _, name := range []string{"", " \t\n"} {
		if got, err := LoadCredentials(context.Background(), client, name); err == nil || got != nil {
			t.Fatal("blank name accepted")
		}
	}
	if got, err := LoadCredentials(context.Background(), nil, "test-google-key"); err == nil || got != nil {
		t.Fatal("nil client accepted")
	}
}
