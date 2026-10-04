package config

import (
	"os"
	"strings"
	"testing"
)

func TestLoadSheets(t *testing.T) {
	t.Setenv("TABLE_NAME", "test-table")
	t.Setenv("GOOGLE_CREDENTIALS_PARAMETER", "test-google-key")
	t.Setenv("GOOGLE_SPREADSHEET_ID", "test-spreadsheet-id")
	for _, key := range []string{"TOKEN_SECRET_PARAMETER", "TOKEN_SECRET", "APP_TIMEZONE", "RESPONSE_BASE_URL", "EMAIL_FROM", "EMAIL_TO"} {
		t.Setenv(key, "")
	}
	got, err := LoadSheets()
	want := Sheets{TableName: "test-table", CredentialsParameter: "test-google-key", SpreadsheetID: "test-spreadsheet-id"}
	if err != nil || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
}

func TestLoadSheetsRequiredSettings(t *testing.T) {
	for _, key := range []string{"TABLE_NAME", "GOOGLE_CREDENTIALS_PARAMETER", "GOOGLE_SPREADSHEET_ID"} {
		for _, mode := range []string{"missing", "empty", "blank"} {
			t.Run(key+"/"+mode, func(t *testing.T) {
				t.Setenv("TABLE_NAME", "test-table")
				t.Setenv("GOOGLE_CREDENTIALS_PARAMETER", "test-google-key")
				t.Setenv("GOOGLE_SPREADSHEET_ID", "test-spreadsheet-id")
				switch mode {
				case "missing":
					if err := os.Unsetenv(key); err != nil {
						t.Fatal(err)
					}
				case "empty":
					t.Setenv(key, "")
				case "blank":
					t.Setenv(key, " \t\n")
				}
				got, err := LoadSheets()
				if err == nil || !strings.Contains(err.Error(), key) || got != (Sheets{}) {
					t.Fatalf("got %+v, %v", got, err)
				}
			})
		}
	}
}
