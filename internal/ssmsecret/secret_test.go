package ssmsecret

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

type fakeClient struct {
	calls int
	get   func(context.Context, *ssm.GetParameterInput) (*ssm.GetParameterOutput, error)
}

func (f *fakeClient) GetParameter(ctx context.Context, in *ssm.GetParameterInput, _ ...func(*ssm.Options)) (*ssm.GetParameterOutput, error) {
	f.calls++
	return f.get(ctx, in)
}

func TestLoadPreservesBytes(t *testing.T) {
	for _, kind := range []types.ParameterType{types.ParameterTypeSecureString, types.ParameterTypeString} {
		t.Run(string(kind), func(t *testing.T) {
			original := " \tfixture-only-secret\n"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &fakeClient{get: func(got context.Context, in *ssm.GetParameterInput) (*ssm.GetParameterOutput, error) {
				if got != ctx || aws.ToString(in.Name) != "configured-key" || !aws.ToBool(in.WithDecryption) {
					t.Fatal("wrong name, context, or decryption")
				}
				return &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: &original, Type: kind}}, nil
			}}
			got, err := Load(ctx, client, "configured-key")
			if err != nil || string(got) != original || client.calls != 1 {
				t.Fatal("value bytes or retrieval count changed")
			}
			got[0] = 'x'
			if original != " \tfixture-only-secret\n" {
				t.Fatal("returned bytes share caller state")
			}
		})
	}
}

func TestMissingValues(t *testing.T) {
	for _, out := range []*ssm.GetParameterOutput{nil, {}, {Parameter: &types.Parameter{}}, {Parameter: &types.Parameter{Value: aws.String("")}}, {Parameter: &types.Parameter{Value: aws.String(" \n\t")}}} {
		client := &fakeClient{get: func(context.Context, *ssm.GetParameterInput) (*ssm.GetParameterOutput, error) { return out, nil }}
		if got, err := Load(context.Background(), client, "configured-key"); err == nil || got != nil {
			t.Fatal("missing or blank value accepted")
		}
	}
}

func TestInvalidDependencies(t *testing.T) {
	client := &fakeClient{get: func(context.Context, *ssm.GetParameterInput) (*ssm.GetParameterOutput, error) {
		t.Fatal("invalid input reached SSM")
		return nil, nil
	}}
	for _, name := range []string{"", " \t\n"} {
		if _, err := Load(context.Background(), client, name); err == nil {
			t.Fatal("blank name accepted")
		}
	}
	var typedNil *fakeClient
	for _, invalid := range []Client{nil, typedNil} {
		if _, err := Load(context.Background(), invalid, "configured-key"); err == nil {
			t.Fatal("nil client accepted")
		}
	}
}

func TestRetrievalErrorDoesNotLeak(t *testing.T) {
	confidential := "fixture-confidential-error-content"
	cause := &types.InvalidKeyId{Message: aws.String(confidential)}
	client := &fakeClient{get: func(context.Context, *ssm.GetParameterInput) (*ssm.GetParameterOutput, error) {
		return &ssm.GetParameterOutput{Parameter: &types.Parameter{Value: aws.String(confidential)}}, cause
	}}
	got, err := Load(context.Background(), client, "configured-key")
	if err == nil || got != nil || !errors.Is(err, cause) {
		t.Fatal("error or cause lost")
	}
	var target *types.InvalidKeyId
	if !errors.As(err, &target) {
		t.Fatal("typed SDK cause lost")
	}
	for _, text := range []string{err.Error(), fmt.Sprintf("%v", err), fmt.Sprintf("%+v", fmt.Errorf("startup: %w", err))} {
		if strings.Contains(text, confidential) {
			t.Fatal("secret material leaked through error formatting")
		}
	}
}
