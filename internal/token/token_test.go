package token_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/scottw0173/ContractorTracker/internal/token"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

func mustSigner(t *testing.T, secret []byte) *token.Signer {
	t.Helper()
	signer, err := token.New(secret)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func mustSign(t *testing.T, signer *token.Signer, claims token.Claims) string {
	t.Helper()
	signed, err := signer.Sign(claims)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// Sign arbitrary payloads independently to exercise validation after a valid HMAC.
func signPayload(raw string, secret []byte) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(raw))
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestRoundTrip(t *testing.T) {
	secret := []byte("test secret")
	signer := mustSigner(t, secret)
	for _, status := range []tracker.Status{tracker.StatusFullDay, tracker.StatusHalfDay, tracker.StatusPTO, tracker.StatusTimeOff} {
		t.Run(string(status), func(t *testing.T) {
			claims := token.Claims{Date: "2026-10-03", Status: status}
			signed := mustSign(t, signer, claims)
			verified, err := signer.Verify(signed)
			if err != nil || verified != claims {
				t.Fatalf("got %+v, %v; want %+v", verified, err, claims)
			}
			if repeat := mustSign(t, signer, claims); repeat != signed {
				t.Fatal("identical claims produced different tokens")
			}
			if strings.ContainsAny(signed, "=+/") {
				t.Fatal("token is not unpadded URL-safe base64")
			}
			expected := signPayload(`{"v":1,"date":"2026-10-03","status":"`+string(status)+`"}`, secret)
			if signed != expected {
				t.Fatal("wire format or signing input differs from documented format")
			}
		})
	}
	for _, date := range []string{"2027-01-01", "2024-02-29"} {
		claims := token.Claims{Date: date, Status: tracker.StatusPTO}
		got, err := signer.Verify(mustSign(t, signer, claims))
		if err != nil || got != claims {
			t.Fatalf("valid calendar date rejected: %v", err)
		}
	}
}

func TestInvalidClaims(t *testing.T) {
	signer := mustSigner(t, []byte("test secret"))
	for _, tc := range []struct {
		name   string
		claims token.Claims
	}{
		{"pending", token.Claims{Date: "2026-10-03", Status: tracker.StatusPending}},
		{"no response", token.Claims{Date: "2026-10-03", Status: tracker.StatusNoResponse}},
		{"unknown status", token.Claims{Date: "2026-10-03", Status: "unknown"}},
		{"empty status", token.Claims{Date: "2026-10-03"}},
		{"empty date", token.Claims{Status: tracker.StatusFullDay}},
		{"unpadded date", token.Claims{Date: "2026-1-3", Status: tracker.StatusFullDay}},
		{"slashes", token.Claims{Date: "2026/10/03", Status: tracker.StatusFullDay}},
		{"long date", token.Claims{Date: "October 3, 2026", Status: tracker.StatusFullDay}},
		{"not a date", token.Claims{Date: "not-a-date", Status: tracker.StatusFullDay}},
		{"invalid leap day", token.Claims{Date: "2026-02-29", Status: tracker.StatusFullDay}},
		{"timestamp", token.Claims{Date: "2026-10-03T00:00:00Z", Status: tracker.StatusFullDay}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if signed, err := signer.Sign(tc.claims); err == nil || signed != "" {
				t.Fatal("invalid claims signed")
			}
		})
	}
}

func TestTamperingAndWrongSecret(t *testing.T) {
	signer := mustSigner(t, []byte("test secret"))
	signed := mustSign(t, signer, token.Claims{Date: "2026-10-03", Status: tracker.StatusFullDay})
	parts := strings.Split(signed, ".")
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	alteredStatus := strings.Replace(string(raw), "FULL_DAY", "PTO", 1)
	alteredDate := strings.Replace(string(raw), "2026-10-03", "2026-10-04", 1)
	for _, tc := range []struct {
		name, value string
		verifier    *token.Signer
	}{
		{"payload character", flipFirst(parts[0]) + "." + parts[1], signer},
		{"signature character", parts[0] + "." + flipFirst(parts[1]), signer},
		{"full day changed to PTO", base64.RawURLEncoding.EncodeToString([]byte(alteredStatus)) + "." + parts[1], signer},
		{"changed date", base64.RawURLEncoding.EncodeToString([]byte(alteredDate)) + "." + parts[1], signer},
		{"wrong secret", signed, mustSigner(t, []byte("different secret"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.verifier.Verify(tc.value)
			if err == nil || got != (token.Claims{}) {
				t.Fatal("tampered token or wrong secret accepted")
			}
		})
	}
}

func flipFirst(value string) string {
	if value[0] == 'A' {
		return "B" + value[1:]
	}
	return "A" + value[1:]
}

func TestRejectedTokens(t *testing.T) {
	secret := []byte("test secret")
	signer := mustSigner(t, secret)
	valid := mustSign(t, signer, token.Claims{Date: "2026-10-03", Status: tracker.StatusFullDay})
	parts := strings.Split(valid, ".")
	for _, tc := range []struct{ name, value string }{
		{"empty", ""},
		{"missing separator", parts[0]},
		{"extra sections", valid + ".extra"},
		{"empty payload", "." + parts[1]},
		{"empty signature", parts[0] + "."},
		{"invalid payload base64", "!" + parts[0] + "." + parts[1]},
		{"invalid signature base64", parts[0] + ".!"},
		{"padded payload", parts[0] + "=." + parts[1]},
		{"newline in payload", parts[0] + "\n." + parts[1]},
		{"invalid JSON", signPayload("not JSON", secret)},
		{"trailing JSON", signPayload(`{"v":1,"date":"2026-10-03","status":"PTO"} {}`, secret)},
		{"unsupported version", signPayload(`{"v":2,"date":"2026-10-03","status":"PTO"}`, secret)},
		{"missing version", signPayload(`{"date":"2026-10-03","status":"PTO"}`, secret)},
		{"invalid signed date", signPayload(`{"v":1,"date":"2026-1-3","status":"PTO"}`, secret)},
		{"empty signed date", signPayload(`{"v":1,"date":"","status":"PTO"}`, secret)},
		{"signed pending", signPayload(`{"v":1,"date":"2026-10-03","status":"PENDING"}`, secret)},
		{"signed no response", signPayload(`{"v":1,"date":"2026-10-03","status":"NO_RESPONSE"}`, secret)},
		{"signed unknown status", signPayload(`{"v":1,"date":"2026-10-03","status":"unknown"}`, secret)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := signer.Verify(tc.value)
			if err == nil || got != (token.Claims{}) {
				t.Fatal("invalid token accepted")
			}
			if err.Error() != "invalid token" {
				t.Fatal("verification leaked failure details")
			}
		})
	}
}

func TestSecretRequirements(t *testing.T) {
	for _, secret := range [][]byte{nil, {}} {
		if signer, err := token.New(secret); err == nil || signer != nil {
			t.Fatal("empty secret accepted")
		}
	}
	secret := []byte("x")
	signer := mustSigner(t, secret)
	claims := token.Claims{Date: "2026-10-03", Status: tracker.StatusPTO}
	original := mustSign(t, signer, claims)
	secret[0] = 'y'
	if signed := mustSign(t, signer, claims); signed != original {
		t.Fatal("signer retained caller-owned secret slice")
	}
	var zero token.Signer
	if _, err := zero.Sign(claims); err == nil {
		t.Fatal("uninitialized signer signed claims")
	}
	if _, err := zero.Verify(original); err == nil {
		t.Fatal("uninitialized signer verified token")
	}
}
