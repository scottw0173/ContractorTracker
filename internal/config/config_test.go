package config

import (
	"os"
	"strings"
	"testing"
)

func setDailyEnv(t *testing.T) {
	t.Helper()
	for key, value := range map[string]string{
		"TABLE_NAME": "days", "TOKEN_SECRET": "test-only secret", "APP_TIMEZONE": "America/Mazatlan",
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
	if got.TableName != "days" || string(got.TokenSecret) != "test-only secret" || got.Location == nil || got.Location.String() != "America/Mazatlan" || got.ResponseBaseURL != "https://example.com/respond" || got.EmailFrom != "from@example.com" || got.EmailTo != "success@simulator.amazonses.com" {
		t.Fatalf("incorrect config %+v", got)
	}
}
func TestLoadStatus(t *testing.T) {
	t.Setenv("TABLE_NAME", "days")
	t.Setenv("TOKEN_SECRET", "test-only secret")
	// Status startup does not require any daily-only setting.
	for _, key := range []string{"APP_TIMEZONE", "RESPONSE_BASE_URL", "EMAIL_FROM", "EMAIL_TO"} {
		t.Setenv(key, "")
	}
	got, err := LoadStatus()
	if err != nil || got.TableName != "days" || string(got.TokenSecret) != "test-only secret" {
		t.Fatalf("incorrect status config: %+v %v", got, err)
	}
}
func TestRequiredSettings(t *testing.T) {
	for _, daily := range []bool{false, true} {
		keys := []string{"TABLE_NAME", "TOKEN_SECRET"}
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
					if strings.Contains(err.Error(), "test-only secret") {
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
func TestSecretIndependence(t *testing.T) {
	setDailyEnv(t)
	t.Setenv("TOKEN_SECRET", " test-only secret ")
	first, err := LoadDaily()
	if err != nil {
		t.Fatal(err)
	}
	original := string(first.TokenSecret)
	first.TokenSecret[0] = 'x'
	second, err := LoadStatus()
	if err != nil {
		t.Fatal(err)
	}
	if string(second.TokenSecret) != original || os.Getenv("TOKEN_SECRET") != original {
		t.Fatal("secret slice mutation affected later loads or environment")
	}
	second.TokenSecret[0] = 'y'
	if first.TokenSecret[0] != 'x' {
		t.Fatal("loads share a mutable secret slice")
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
