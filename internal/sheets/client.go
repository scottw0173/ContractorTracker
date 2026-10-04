package sheets

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	googlesheets "google.golang.org/api/sheets/v4"
)

// Client pairs an authenticated SDK service with the configured existing target.
// SpreadsheetID is not an OAuth restriction: Google sharing controls access.
type Client struct {
	Service       *googlesheets.Service
	SpreadsheetID string
}

// NewClient accepts service-account JSON only, without default credential
// discovery. Construction makes no Google requests; tokens are acquired lazily
// on API use. The context must remain valid for the client's lifetime.
func NewClient(ctx context.Context, credentialsJSON []byte, spreadsheetID string) (*Client, error) {
	if strings.TrimSpace(spreadsheetID) == "" {
		return nil, errors.New("Google spreadsheet ID must not be blank")
	}
	auth, err := google.JWTConfigFromJSON(credentialsJSON, googlesheets.SpreadsheetsScope)
	// Do not expose credential parser errors, which may contain JSON field values.
	if err != nil || auth.Email == "" || len(auth.PrivateKey) == 0 || auth.TokenURL == "" {
		return nil, errors.New("invalid Google service-account credentials")
	}
	service, err := googlesheets.NewService(ctx, option.WithHTTPClient(auth.Client(ctx)))
	if err != nil {
		return nil, fmt.Errorf("create Google Sheets service: %w", err)
	}
	return &Client{Service: service, SpreadsheetID: spreadsheetID}, nil
}
