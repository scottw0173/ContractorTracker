package sheets

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/api/option"
	googlesheets "google.golang.org/api/sheets/v4"
)

func TestVerifyWorksheets(t *testing.T) {
	for _, tt := range []struct {
		name, body, missing string
	}{
		{"both exist", `{"sheets":[{"properties":{"title":"Summary"}},{"properties":{"title":"Other"}},{"properties":{"title":"Daily Log"}}]}`, ""},
		{"Summary alone", `{"sheets":[{"properties":{"title":"Summary"}}]}`, ""},
		{"missing summary", `{"sheets":[{"properties":{"title":"Daily Log"}}]}`, "Summary"},
		{"missing both", `{}`, "Summary"},
		{"exact names", `{"sheets":[{"properties":{"title":"daily log"}},{"properties":{"title":"Summary "}}]}`, "Summary"},
		{"incomplete metadata", `{"sheets":[null,{}]}`, "Summary"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			type contextKey struct{}
			ctx := context.WithValue(context.Background(), contextKey{}, "invocation")
			calls := 0
			service, err := googlesheets.NewService(ctx, option.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/v4/spreadsheets/test-id" {
					t.Fatal("expected read-only metadata request to configured spreadsheet")
				}
				if r.URL.Query().Get("includeGridData") != "false" || r.URL.Query().Get("fields") != worksheetMetadataFields {
					t.Fatal("request should fetch only worksheet titles, without grid data")
				}
				if r.Context().Value(contextKey{}) != "invocation" {
					t.Fatal("context not passed through")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tt.body)), Request: r}, nil
			})}))
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{Service: service, SpreadsheetID: "test-id"}
			err = client.VerifyWorksheets(ctx)
			if tt.missing == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.missing) {
				t.Fatalf("missing worksheet error = %v", err)
			}
			if calls != 1 {
				t.Fatalf("requests = %d, want 1", calls)
			}
		})
	}
}

func TestVerifyWorksheetsRequestFailure(t *testing.T) {
	failure := errors.New("offline transport failure")
	service, err := googlesheets.NewService(context.Background(), option.WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, failure
	})}))
	if err != nil {
		t.Fatal(err)
	}
	err = (&Client{Service: service, SpreadsheetID: "test-id"}).VerifyWorksheets(context.Background())
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), "read spreadsheet metadata") {
		t.Fatalf("error = %v", err)
	}
}
