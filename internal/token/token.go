// Package token signs date/status bearer capabilities. Tokens protect integrity,
// not confidentiality or human identity. Possession permits use of the signed
// claims; record mutation rules belong to the future status handler.
package token

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

const version = 1

var errInvalidToken = errors.New("invalid token")

// Claims binds a calendar date to one user-selectable status.
type Claims struct {
	Date   string
	Status tracker.Status
}

// Signer must be constructed with New. Tokens have no expiration so older
// emails can support late responses and corrections.
type Signer struct{ secret []byte }

type payload struct {
	Version int            `json:"v"`
	Date    string         `json:"date"`
	Status  tracker.Status `json:"status"`
}

// New copies the caller's secret and rejects an empty secret. Provisioning a
// strong random secret is the responsibility of the integration layer.
func New(secret []byte) (*Signer, error) {
	if len(secret) == 0 {
		return nil, errors.New("token secret must not be empty")
	}
	return &Signer{secret: append([]byte(nil), secret...)}, nil
}

func validateClaims(claims Claims) error {
	date, err := time.Parse("2006-01-02", claims.Date)
	if err != nil || date.Format("2006-01-02") != claims.Date {
		return fmt.Errorf("date must be an exact YYYY-MM-DD calendar date")
	}
	if !tracker.IsUserStatus(claims.Status) {
		return fmt.Errorf("invalid user status %q", claims.Status)
	}
	return nil
}

func (s *Signer) signature(encodedPayload string) []byte {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(encodedPayload))
	return mac.Sum(nil)
}

// Sign returns base64url(JSON).base64url(HMAC-SHA256(encoded JSON)), without
// padding. Identical claims and secrets produce identical tokens.
func (s *Signer) Sign(claims Claims) (string, error) {
	if s == nil || len(s.secret) == 0 {
		return "", errors.New("token signer is not initialized")
	}
	if err := validateClaims(claims); err != nil {
		return "", err
	}
	raw, err := json.Marshal(payload{Version: version, Date: claims.Date, Status: claims.Status})
	if err != nil {
		return "", fmt.Errorf("encode token claims: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(s.signature(encoded)), nil
}

// Verify authenticates the exact encoded payload before interpreting claims.
// All rejected tokens return the same error and no claims.
func (s *Signer) Verify(token string) (Claims, error) {
	if s == nil || len(s.secret) == 0 {
		return Claims{}, errInvalidToken
	}
	encoded, signature, ok := strings.Cut(token, ".")
	if !ok || encoded == "" || signature == "" || strings.Contains(signature, ".") {
		return Claims{}, errInvalidToken
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return Claims{}, errInvalidToken
	}
	received, err := base64.RawURLEncoding.Strict().DecodeString(signature)
	if err != nil || base64.RawURLEncoding.EncodeToString(received) != signature {
		return Claims{}, errInvalidToken
	}
	if !hmac.Equal(received, s.signature(encoded)) {
		return Claims{}, errInvalidToken
	}
	var decoded payload
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return Claims{}, errInvalidToken
	}
	if decoded.Version != version {
		return Claims{}, errInvalidToken
	}
	claims := Claims{Date: decoded.Date, Status: decoded.Status}
	if err := validateClaims(claims); err != nil {
		return Claims{}, errInvalidToken
	}
	return claims, nil
}
