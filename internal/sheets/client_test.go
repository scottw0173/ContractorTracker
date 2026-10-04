package sheets

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"
	googlesheets "google.golang.org/api/sheets/v4"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testAccount(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{
		"type": "service_account", "client_email": "test@example.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})),
		"token_uri":   "https://oauth2.googleapis.com/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestClientAuthenticationOffline(t *testing.T) {
	calls := 0
	// All HTTP requests are intercepted. No sockets, AWS, or Google calls occur.
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		body := "{}"
		if calls == 1 {
			if r.Method != http.MethodPost || r.URL.String() != "https://oauth2.googleapis.com/token" {
				t.Fatal("unexpected token exchange")
			}
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			values, err := url.ParseQuery(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(values.Get("assertion"), ".")
			if len(parts) != 3 {
				t.Fatal("not a service-account JWT assertion")
			}
			payload, err := base64.RawURLEncoding.DecodeString(parts[1])
			if err != nil {
				t.Fatal(err)
			}
			var claims struct {
				Scope  string `json:"scope"`
				Issuer string `json:"iss"`
			}
			if err := json.Unmarshal(payload, &claims); err != nil {
				t.Fatal(err)
			}
			if claims.Scope != googlesheets.SpreadsheetsScope || claims.Issuer != "test@example.iam.gserviceaccount.com" {
				t.Fatal("wrong service-account scope or identity")
			}
			body = `{"access_token":"test-only-access-token","token_type":"Bearer","expires_in":3600}`
		} else if calls == 2 {
			if r.Method != http.MethodGet || r.URL.Path != "/v4/spreadsheets/test-spreadsheet-id" || r.Header.Get("Authorization") != "Bearer test-only-access-token" {
				t.Fatal("SDK request not authenticated or wrong target")
			}
		} else {
			t.Fatal("unexpected request")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Transport: transport})
	client, err := NewClient(ctx, testAccount(t), "test-spreadsheet-id")
	if err != nil || client == nil || client.Service == nil || client.SpreadsheetID != "test-spreadsheet-id" {
		t.Fatalf("client construction failed: %v", err)
	}
	if calls != 0 {
		t.Fatal("constructor must not make requests")
	}
	// Exercise only authentication against a fake response, not synchronization.
	if _, err := client.Service.Spreadsheets.Get(client.SpreadsheetID).Context(ctx).Do(); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("expected lazy token exchange and authenticated request")
	}
}

func TestInvalidClientCredentials(t *testing.T) {
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid credentials caused a request")
		return nil, nil
	})})
	for _, data := range []string{"", "not JSON", `{"type":"authorized_user"}`, `{"type":"service_account"}`, `{"type":"service_account","client_email":"test@example.com"}`} {
		if client, err := NewClient(ctx, []byte(data), "test-spreadsheet-id"); err == nil || client != nil {
			t.Fatal("invalid service-account data accepted")
		}
	}
	for _, id := range []string{"", " \t\n"} {
		if client, err := NewClient(ctx, []byte("not JSON"), id); err == nil || client != nil {
			t.Fatal("blank ID accepted")
		}
	}
}
