package email

import (
	"bytes"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/scottw0173/ContractorTracker/internal/token"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

type Builder struct {
	signer  *token.Signer
	baseURL url.URL
}

// NewBuilder accepts an absolute HTTP(S) URL. It preserves the path and unrelated
// query parameters, but rejects selection parameters, credentials, and fragments.
func NewBuilder(signer *token.Signer, responseBaseURL string) (*Builder, error) {
	if signer == nil {
		return nil, fmt.Errorf("email builder requires a token signer")
	}
	base, err := url.Parse(responseBaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse response base URL: %w", err)
	}
	if (base.Scheme != "https" && base.Scheme != "http") || base.Hostname() == "" || base.User != nil || base.Fragment != "" {
		return nil, fmt.Errorf("response base URL must be absolute HTTP(S) without credentials or a fragment")
	}
	query, err := url.ParseQuery(base.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("parse response base URL query: %w", err)
	}
	for _, key := range []string{"token", "date", "status"} {
		if _, exists := query[key]; exists {
			return nil, fmt.Errorf("response base URL must not contain %s", key)
		}
	}
	return &Builder{signer: signer, baseURL: *base}, nil
}

type choice struct {
	Label string
	URL   string
}

var dailyHTML = template.Must(template.New("daily").Parse(`<!DOCTYPE html>
<html><body style="font-family:Arial,sans-serif;color:#222;">
<h1 style="font-size:22px;">Contractor status — {{.Date}}</h1>
<p>Choose your status for {{.Date}}.</p>
<p>Clicking an option opens a confirmation page before changing the record.</p>
{{range .Choices}}<p><a href="{{.URL}}" style="display:inline-block;padding:12px 20px;background-color:#2457a7;color:#fff;text-decoration:none;border-radius:4px;">{{.Label}}</a></p>
{{end}}<p>PTO: counts against the annual PTO allowance.<br>
Time Off: weekend, holiday, or another normally non-working day; does not consume PTO.</p>
</body></html>`))

// Build uses only the supplied ISO calendar date; token claims retain that date.
func (b *Builder) Build(date string) (Message, error) {
	day, err := time.Parse("2006-01-02", date)
	if err != nil || day.Format("2006-01-02") != date {
		return Message{}, fmt.Errorf("email date must be an exact YYYY-MM-DD calendar date")
	}
	friendly := day.Format("January 2, 2006")
	choices := make([]choice, 0, 4)
	for _, option := range []struct {
		label  string
		status tracker.Status
	}{
		{"Full Day", tracker.StatusFullDay}, {"Half Day", tracker.StatusHalfDay},
		{"PTO", tracker.StatusPTO}, {"Time Off", tracker.StatusTimeOff},
	} {
		signed, err := b.signer.Sign(token.Claims{Date: date, Status: option.status})
		if err != nil {
			return Message{}, fmt.Errorf("sign %s email response: %w", option.label, err)
		}
		link := b.baseURL
		query := link.Query()
		query.Set("token", signed)
		link.RawQuery = query.Encode()
		choices = append(choices, choice{Label: option.label, URL: link.String()})
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Contractor status — %s\n\nChoose your status for %s.\nClicking an option opens a confirmation page before changing the record.\n\n", friendly, friendly)
	for _, option := range choices {
		fmt.Fprintf(&text, "%s: %s\n", option.Label, option.URL)
	}
	text.WriteString("\nPTO: counts against the annual PTO allowance.\nTime Off: weekend, holiday, or another normally non-working day; does not consume PTO.\n")
	var html bytes.Buffer
	if err := dailyHTML.Execute(&html, struct {
		Date    string
		Choices []choice
	}{friendly, choices}); err != nil {
		return Message{}, fmt.Errorf("render email HTML: %w", err)
	}
	return Message{Subject: "Contractor status — " + friendly, TextBody: text.String(), HTMLBody: html.String()}, nil
}
