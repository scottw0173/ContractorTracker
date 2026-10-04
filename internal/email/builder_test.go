package email

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/scottw0173/ContractorTracker/internal/token"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

func testSigner(t *testing.T) *token.Signer {
	t.Helper()
	signer, err := token.New([]byte("test secret"))
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func TestBuild(t *testing.T) {
	for _, base := range []string{
		"https://example.com/respond",
		"https://example.com/nested/respond?campaign=daily&marker=%22%3E%3Cscript%3E",
		"http://localhost:8080/respond",
	} {
		t.Run(base, func(t *testing.T) {
			signer := testSigner(t)
			builder, err := NewBuilder(signer, base)
			if err != nil {
				t.Fatal(err)
			}
			message, err := builder.Build("2026-10-05")
			if err != nil {
				t.Fatal(err)
			}
			if message.Subject == "" || message.TextBody == "" || message.HTMLBody == "" {
				t.Fatal("empty message content")
			}
			for _, body := range []string{message.Subject, message.TextBody, message.HTMLBody} {
				if !strings.Contains(body, "October 5, 2026") {
					t.Fatal("human-friendly date missing")
				}
			}
			labels := map[string]tracker.Status{"Full Day": tracker.StatusFullDay, "Half Day": tracker.StatusHalfDay, "PTO": tracker.StatusPTO, "Time Off": tracker.StatusTimeOff}
			textLinks := make(map[string]string)
			for _, line := range strings.Split(message.TextBody, "\n") {
				label, link, ok := strings.Cut(line, ": ")
				if _, exists := labels[label]; ok && exists && strings.HasPrefix(link, "http") {
					textLinks[label] = link
				}
			}
			if len(textLinks) != 4 {
				t.Fatal("four labeled plain-text links required")
			}
			matches := regexp.MustCompile(`<a href="([^"]+)"[^>]*>([^<]+)</a>`).FindAllStringSubmatch(message.HTMLBody, -1)
			if len(matches) != 4 {
				t.Fatal("four HTML choices required")
			}
			seen := make(map[string]bool)
			parsedBase, err := url.Parse(base)
			if err != nil {
				t.Fatal(err)
			}
			for _, match := range matches {
				label := html.UnescapeString(match[2])
				link := html.UnescapeString(match[1])
				expected, ok := labels[label]
				if !ok || seen[link] {
					t.Fatalf("unexpected or duplicate choice %q", label)
				}
				seen[link] = true
				if textLinks[label] != link {
					t.Fatal("text and HTML links differ")
				}
				parsed, err := url.Parse(link)
				if err != nil {
					t.Fatal(err)
				}
				if parsed.Scheme != parsedBase.Scheme || parsed.Host != parsedBase.Host || parsed.Path != parsedBase.Path {
					t.Fatal("base URL changed")
				}
				query, err := url.ParseQuery(parsed.RawQuery)
				if err != nil {
					t.Fatal(err)
				}
				signed := query.Get("token")
				if len(query["token"]) != 1 {
					t.Fatal("expected exactly one token")
				}
				query.Del("token")
				if query.Encode() != parsedBase.Query().Encode() {
					t.Fatal("unrelated query parameters changed or unsigned claims added")
				}
				claims, err := signer.Verify(signed)
				if err != nil || claims != (token.Claims{Date: "2026-10-05", Status: expected}) {
					t.Fatalf("incorrect signed claims %+v: %v", claims, err)
				}
			}
			for _, body := range []string{message.TextBody, message.HTMLBody} {
				if !strings.Contains(body, "confirmation page before changing the record") || !strings.Contains(body, "annual PTO allowance") || !strings.Contains(body, "does not consume PTO") {
					t.Fatal("confirmation or PTO semantics missing")
				}
				if strings.Contains(body, "FULL_DAY") || strings.Contains(body, "HALF_DAY") || strings.Contains(body, "TIME_OFF") {
					t.Fatal("internal status labels exposed")
				}
			}
			for _, bad := range []string{"<script", "<form", "<img", "<link", "#ZgotmplZ"} {
				if strings.Contains(message.HTMLBody, bad) {
					t.Fatalf("unexpected HTML content %q", bad)
				}
			}
		})
	}
}

func TestBuilderValidation(t *testing.T) {
	signer := testSigner(t)
	if builder, err := NewBuilder(nil, "https://example.com/respond"); err == nil || builder != nil {
		t.Fatal("nil signer accepted")
	}
	for _, base := range []string{"", "not a URL", "/respond", "https:///respond", "https://exa mple.com", "https://example.com/%zz", "javascript:alert(1)", "ftp://example.com/respond", "https://user:pass@example.com/respond", "https://example.com/respond#section", "https://example.com/respond?x=%zz", "https://example.com/respond?token=old", "https://example.com/respond?date=2026-10-05", "https://example.com/respond?status=PTO"} {
		t.Run(base, func(t *testing.T) {
			if builder, err := NewBuilder(signer, base); err == nil || builder != nil {
				t.Fatal("invalid base URL accepted")
			}
		})
	}
	builder, err := NewBuilder(signer, "https://example.com/respond")
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []string{"", "2026-1-5", "2026/10/05", "2026-02-30", "2026-02-29", "2026-10-05T00:00:00Z"} {
		t.Run(date, func(t *testing.T) {
			if message, err := builder.Build(date); err == nil || message != (Message{}) {
				t.Fatal("invalid date accepted")
			}
		})
	}
	for _, date := range []string{"2024-02-29", "2027-01-01"} {
		if _, err := builder.Build(date); err != nil {
			t.Fatalf("valid date rejected: %v", err)
		}
	}
	var zero token.Signer
	builder, err = NewBuilder(&zero, "https://example.com/respond")
	if err != nil {
		t.Fatal(err)
	}
	if message, err := builder.Build("2026-10-05"); err == nil || message != (Message{}) {
		t.Fatal("signing failure was not propagated")
	}
}
