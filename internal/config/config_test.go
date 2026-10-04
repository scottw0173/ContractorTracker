package config

import (
	"os"
	"strings"
	"testing"
)

func setDailyEnv(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"TABLE_NAME": "days", "TOKEN_SECRET_PARAMETER": "test-token-parameter", "APP_TIMEZONE": "America/Mazatlan",
		"RESPONSE_BASE_URL": "https://example.com/respond", "EMAIL_FROM": "from@example.com", "EMAIL_TO": "success@simulator.amazonses.com",
	} {
		t.Setenv(key, value)
	}
}
func TestLoadDaily(t *testing.T) {
	setDailyEnv(t)
	got, err := LoadDaily()
	if err != nil {
		t.Fatal(err)
	}
	if got.TableName != "days" || got.TokenSecretParameter != "test-token-parameter" || got.Location == nil || got.Location.String() != "America/Mazatlan" || got.ResponseBaseURL != "https://example.com/respond" || got.EmailFrom != "from@example.com" || got.EmailTo != "success@simulator.amazonses.com" {
		t.Fatalf("incorrect config %+v", got)
	}
}
func TestLoadStatus(t *testing.T) {
	t.Setenv("TABLE_NAME", "days")
	t.Setenv("TOKEN_SECRET_PARAMETER", "test-token-parameter")
	// Status startup does not require any daily-only setting.
	for _, key := range []string{"APP_TIMEZONE", "RESPONSE_BASE_URL", "EMAIL_FROM", "EMAIL_TO"} {
		t.Setenv(key, "")
	}
	got, err := LoadStatus()
	if err != nil || got.TableName != "days" || got.TokenSecretParameter != "test-token-parameter" {
		t.Fatalf("incorrect status config: %+v %v", got, err)
	}
}
func TestRequiredSettings(t *testing.T) {
	for _, daily := range []bool{false, true} {
		keys := []string{"TABLE_NAME", "TOKEN_SECRET_PARAMETER"}
		if daily {
			keys = append(keys, "APP_TIMEZONE", "RESPONSE_BASE_URL", "EMAIL_FROM", "EMAIL_TO")
		}
		for _, key := range keys {
			for _, mode := range []string{"missing", "empty", "blank"} {
				name := "status/"
				if daily {
					name = "daily/"
				}
				t.Run(name+key+"/"+mode, func(t *testing.T) {
					setDailyEnv(t)
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
					var err error
					if daily {
						_, err = LoadDaily()
					} else {
						_, err = LoadStatus()
					}
					if err == nil || !strings.Contains(err.Error(), key) {
						t.Fatalf("missing setting name: %v", err)
					}
					if strings.Contains(err.Error(), "test-token-parameter") {
						t.Fatal("secret leaked in error")
					}
				})
			}
		}
	}
}
func TestInvalidTimezone(t *testing.T) {
	setDailyEnv(t)
	t.Setenv("APP_TIMEZONE", "not/a/timezone")
	if _, err := LoadDaily(); err == nil || !strings.Contains(err.Error(), "APP_TIMEZONE") {
		t.Fatalf("invalid timezone accepted: %v", err)
	}
}
func TestLegacySecretIsNotConsumed(t *testing.T) {
	for _, legacy := range []string{"", "legacy-secret-must-be-ignored"} {
		t.Run(legacy, func(t *testing.T) {
			setDailyEnv(t)
			t.Setenv("TOKEN_SECRET", legacy)
			daily, err := LoadDaily()
			if err != nil || daily.TokenSecretParameter != "test-token-parameter" {
				t.Fatal("daily config consumed legacy secret")
			}
			status, err := LoadStatus()
			if err != nil || status.TokenSecretParameter != "test-token-parameter" {
				t.Fatal("status config consumed legacy secret")
			}
			t.Setenv("TOKEN_SECRET_PARAMETER", "")
			if _, err := LoadDaily(); err == nil {
				t.Fatal("legacy secret replaced required parameter name")
			}
			if _, err := LoadStatus(); err == nil {
				t.Fatal("legacy secret replaced required parameter name")
			}
		})
	}
}
func TestEmailOwnsURLAndMailboxValidation(t *testing.T) {
	setDailyEnv(t)
	t.Setenv("RESPONSE_BASE_URL", "invalid URL")
	t.Setenv("EMAIL_FROM", "invalid mailbox")
	t.Setenv("EMAIL_TO", "invalid mailbox")
	if _, err := LoadDaily(); err != nil {
		t.Fatalf("config duplicated email validation: %v", err)
	}
}
