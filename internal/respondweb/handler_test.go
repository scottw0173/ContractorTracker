package respondweb

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/scottw0173/ContractorTracker/internal/respond"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

type fakeService struct {
	previews, submits int
	token             string
	now               time.Time
	ctx               context.Context
	confirmation      respond.Confirmation
	record            tracker.DayRecord
	err               error
}

func (s *fakeService) Preview(ctx context.Context, raw string) (respond.Confirmation, error) {
	s.previews++
	s.token, s.ctx = raw, ctx
	return s.confirmation, s.err
}
func (s *fakeService) Submit(ctx context.Context, raw string, now time.Time) (tracker.DayRecord, error) {
	s.submits++
	s.token, s.now, s.ctx = raw, now, ctx
	return s.record, s.err
}
func fixture(t *testing.T) (*Handler, *fakeService) {
	t.Helper()
	s := &fakeService{confirmation: respond.Confirmation{Token: "signed-token", Date: "2026-10-05", FriendlyDate: "October 5, 2026", CurrentStatus: tracker.StatusPending, CurrentLabel: "Pending", RequestedStatus: tracker.StatusFullDay, RequestedLabel: "Full Day"}, record: tracker.DayRecord{Date: "2026-10-05", Status: tracker.StatusPTO}}
	h, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	return h, s
}
func request(method string) events.LambdaFunctionURLRequest {
	return events.LambdaFunctionURLRequest{Version: "2.0", RawPath: "/custom/respond", RawQueryString: "token=signed-token", QueryStringParameters: map[string]string{"token": "signed-token"}, Headers: map[string]string{"content-type": "application/x-www-form-urlencoded; charset=utf-8"}, Body: "token=signed-token", RequestContext: events.LambdaFunctionURLRequestContext{HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: method}}}
}
func assertHeaders(t *testing.T, r events.LambdaFunctionURLResponse) {
	t.Helper()
	for k, v := range map[string]string{"Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff", "Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"} {
		if r.Headers[k] != v {
			t.Fatalf("header %s=%q", k, r.Headers[k])
		}
	}
	if r.IsBase64Encoded {
		t.Fatal("HTML should be plain text")
	}
}

// Email-security scanners may follow GET links; GET must never submit.
func TestGETScannerSafety(t *testing.T) {
	h, s := fixture(t)
	ctx := context.WithValue(context.Background(), struct{}{}, "request")
	r, err := h.Handle(ctx, request("GET"), time.Time{})
	if err != nil || r.StatusCode != 200 || s.previews != 1 || s.submits != 0 || s.token != "signed-token" || s.ctx != ctx {
		t.Fatalf("GET not a read-only preview: %+v %v", r, err)
	}
	for _, text := range []string{"October 5, 2026", "Current status: Pending", "Selected status: Full Day", "Confirm Full Day", `<form method="post" action="/custom/respond">`, `<input type="hidden" name="token" value="signed-token">`} {
		if !strings.Contains(r.Body, text) {
			t.Fatalf("missing %q", text)
		}
	}
	if strings.Count(r.Body, "signed-token") != 1 {
		t.Fatal("token must appear only in hidden field")
	}
	assertHeaders(t, r)
}
func TestPreviewCorrection(t *testing.T) {
	for _, correction := range []bool{false, true} {
		h, s := fixture(t)
		s.confirmation.IsCorrection = correction
		s.confirmation.CurrentLabel, s.confirmation.RequestedLabel = "Full Day", "PTO"
		if !correction {
			s.confirmation.CurrentLabel = "No Response"
		}
		r, err := h.Handle(context.Background(), request("GET"), time.Time{})
		if err != nil || strings.Contains(r.Body, "Change status from") != correction {
			t.Fatal("incorrect correction wording")
		}
		if correction && !strings.Contains(r.Body, "Change status from Full Day to PTO?") {
			t.Fatal("missing correction detail")
		}
	}
}
func TestSubmitSuccess(t *testing.T) {
	for _, status := range []tracker.Status{tracker.StatusFullDay, tracker.StatusHalfDay, tracker.StatusPTO, tracker.StatusTimeOff} {
		for _, encoded := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/base64=%v", status, encoded), func(t *testing.T) {
				h, s := fixture(t)
				s.record.Status = status
				req := request("POST")
				raw := "secret+token&value"
				req.Body = url.Values{"token": {raw}, "ignored": {"field"}}.Encode()
				if encoded {
					req.Body = base64.StdEncoding.EncodeToString([]byte(req.Body))
					req.IsBase64Encoded = true
				}
				now := time.Date(2026, 10, 5, 12, 0, 0, 123456789, time.FixedZone("app", -7*3600))
				ctx := context.WithValue(context.Background(), struct{}{}, "submit")
				r, err := h.Handle(ctx, req, now)
				labels := map[tracker.Status]string{tracker.StatusFullDay: "Full Day", tracker.StatusHalfDay: "Half Day", tracker.StatusPTO: "PTO", tracker.StatusTimeOff: "Time Off"}
				if err != nil || r.StatusCode != 200 || s.submits != 1 || s.previews != 0 || s.token != raw || s.now != now || s.ctx != ctx {
					t.Fatalf("incorrect submission %v %+v", err, s)
				}
				for _, text := range []string{"Status recorded", "October 5, 2026", labels[status], "Your response has been saved."} {
					if !strings.Contains(r.Body, text) {
						t.Fatalf("missing %q", text)
					}
				}
				if strings.Contains(r.Body, raw) || strings.Contains(r.Body, "signed-token") {
					t.Fatal("success leaked token")
				}
				assertHeaders(t, r)
			})
		}
	}
}
func TestInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		change       func(*events.LambdaFunctionURLRequest)
		status       int
	}{
		{"missing query", "GET", func(r *events.LambdaFunctionURLRequest) { r.QueryStringParameters = nil }, 400},
		{"empty query", "GET", func(r *events.LambdaFunctionURLRequest) { r.QueryStringParameters["token"] = "" }, 400},
		{"missing body token", "POST", func(r *events.LambdaFunctionURLRequest) { r.Body = "other=value" }, 400},
		{"empty body token", "POST", func(r *events.LambdaFunctionURLRequest) { r.Body = "token=" }, 400},
		{"duplicate token", "POST", func(r *events.LambdaFunctionURLRequest) { r.Body = "token=a&token=b" }, 400},
		{"empty duplicate token", "POST", func(r *events.LambdaFunctionURLRequest) { r.Body = "token=a&token=" }, 400},
		{"bad escape", "POST", func(r *events.LambdaFunctionURLRequest) { r.Body = "token=%ZZ" }, 400},
		{"bad separator", "POST", func(r *events.LambdaFunctionURLRequest) { r.Body = "token=a;b" }, 400},
		{"bad base64", "POST", func(r *events.LambdaFunctionURLRequest) { r.Body = "!bad!"; r.IsBase64Encoded = true }, 400},
		{"large form", "POST", func(r *events.LambdaFunctionURLRequest) { r.Body = "token=" + strings.Repeat("x", maxFormBody) }, 400},
		{"large base64 form", "POST", func(r *events.LambdaFunctionURLRequest) {
			r.Body = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", maxFormBody+1)))
			r.IsBase64Encoded = true
		}, 400},
		{"JSON", "POST", func(r *events.LambdaFunctionURLRequest) { r.Headers["content-type"] = "application/json" }, 415},
		{"missing media type", "POST", func(r *events.LambdaFunctionURLRequest) { r.Headers = nil }, 415},
		{"bad media type", "POST", func(r *events.LambdaFunctionURLRequest) { r.Headers["content-type"] = ";bad" }, 415},
		{"external action", "GET", func(r *events.LambdaFunctionURLRequest) { r.RawPath = "//evil.example/respond" }, 400},
		{"query action", "GET", func(r *events.LambdaFunctionURLRequest) { r.RawPath = "/respond?token=secret" }, 400},
		{"backslash action", "GET", func(r *events.LambdaFunctionURLRequest) { r.RawPath = "/\\evil.example" }, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, s := fixture(t)
			req := request(tc.method)
			tc.change(&req)
			r, err := h.Handle(context.Background(), req, time.Time{})
			if err != nil || r.StatusCode != tc.status || s.previews != 0 || s.submits != 0 {
				t.Fatalf("invalid request called service or wrong status: %d %v", r.StatusCode, err)
			}
			if strings.Contains(r.Body, "signed-token") {
				t.Fatal("error leaked token")
			}
			assertHeaders(t, r)
		})
	}
}
func TestServiceErrorMapping(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, tc := range []struct {
			err    error
			status int
		}{
			{fmt.Errorf("secret signature detail: %w", respond.ErrInvalidToken), 400},
			{fmt.Errorf("private key: %w", respond.ErrDayNotFound), 404},
			{fmt.Errorf("private conflict: %w", respond.ErrConflict), 409},
			{errors.New("private AWS error"), 500},
		} {
			t.Run(fmt.Sprintf("%s/%d", method, tc.status), func(t *testing.T) {
				h, s := fixture(t)
				s.err = tc.err
				r, err := h.Handle(context.Background(), request(method), time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC))
				if err != nil || r.StatusCode != tc.status {
					t.Fatalf("wrong mapping %d %v", r.StatusCode, err)
				}
				if strings.Contains(r.Body, "private") || strings.Contains(r.Body, "secret") || strings.Contains(r.Body, "signed-token") {
					t.Fatal("service error leaked")
				}
				if method == "GET" && (s.previews != 1 || s.submits != 0) || method == "POST" && (s.previews != 0 || s.submits != 1) {
					t.Fatal("wrong service operation")
				}
				assertHeaders(t, r)
			})
		}
	}
}
func TestMethodsAndConstructor(t *testing.T) {
	for _, method := range []string{"PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"} {
		h, s := fixture(t)
		r, err := h.Handle(context.Background(), request(method), time.Time{})
		if err != nil || r.StatusCode != 405 || r.Headers["Allow"] != "GET, POST" || s.previews != 0 || s.submits != 0 {
			t.Fatalf("wrong method handling %s", method)
		}
		assertHeaders(t, r)
	}
	if h, err := New(nil); h != nil || err == nil {
		t.Fatal("nil service accepted")
	}
}
func TestHTMLEscaping(t *testing.T) {
	h, s := fixture(t)
	hostile := `<script>alert("x")</script>'&`
	s.confirmation.Token = hostile
	s.confirmation.FriendlyDate = hostile
	s.confirmation.CurrentLabel = hostile
	s.confirmation.RequestedLabel = hostile
	req := request("GET")
	req.RawPath = `/respond/"<>&'`
	r, err := h.Handle(context.Background(), req, time.Time{})
	if err != nil || r.StatusCode != 200 {
		t.Fatalf("unexpected escaping response %d %v", r.StatusCode, err)
	}
	if strings.Contains(r.Body, "<script>") || strings.Contains(r.Body, hostile) || strings.Contains(r.Body, `action="/respond/"`) {
		t.Fatal("unescaped dynamic content")
	}
	if !strings.Contains(r.Body, `name="token" value="`+html.EscapeString(hostile)+`"`) {
		t.Fatal("hidden token not escaped")
	}
	if !strings.Contains(r.Body, html.EscapeString(hostile)) {
		t.Fatal("display content not escaped")
	}
}
