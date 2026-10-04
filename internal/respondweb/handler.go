// Package respondweb adapts signed response confirmation to Function URL HTTP events.
package respondweb

import (
	"context"
	"encoding/base64"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/events"
	"github.com/scottw0173/ContractorTracker/internal/respond"
	"github.com/scottw0173/ContractorTracker/internal/tracker"
)

// ResponseService owns token verification and all record mutation rules.
type ResponseService interface {
	Preview(context.Context, string) (respond.Confirmation, error)
	Submit(context.Context, string, time.Time) (tracker.DayRecord, error)
}

var _ ResponseService = (*respond.Service)(nil)

type Handler struct{ service ResponseService }

func New(service ResponseService) (*Handler, error) {
	if service == nil {
		return nil, errors.New("response web handler requires a service")
	}
	return &Handler{service: service}, nil
}

// Handle returns rendered pages with nil errors, including service failures,
// so Function URLs deliver the intended HTTP response rather than a gateway error.
func (h *Handler) Handle(ctx context.Context, request events.LambdaFunctionURLRequest, now time.Time) (events.LambdaFunctionURLResponse, error) {
	switch request.RequestContext.HTTP.Method {
	case http.MethodGet:
		raw := request.QueryStringParameters["token"]
		if raw == "" {
			return renderError(http.StatusBadRequest, "This response link is invalid."), nil
		}
		action, err := formAction(request.RawPath)
		if err != nil {
			return renderError(http.StatusBadRequest, "This request is invalid."), nil
		}
		confirmation, err := h.service.Preview(ctx, raw)
		if err != nil {
			return serviceError(err), nil
		}
		return render(http.StatusOK, page{Title: "Contractor status", Confirmation: &confirmation, Action: action}), nil
	case http.MethodPost:
		contentType := ""
		for key, value := range request.Headers {
			if strings.EqualFold(key, "Content-Type") {
				contentType = value
				break
			}
		}
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil || mediaType != "application/x-www-form-urlencoded" {
			return renderError(http.StatusUnsupportedMediaType, "Please submit the confirmation form."), nil
		}
		raw, err := formToken(request)
		if err != nil {
			return renderError(http.StatusBadRequest, "This confirmation is invalid."), nil
		}
		record, err := h.service.Submit(ctx, raw, now)
		if err != nil {
			return serviceError(err), nil
		}
		date, err := time.Parse("2006-01-02", record.Date)
		if err != nil || date.Format("2006-01-02") != record.Date || !tracker.IsUserStatus(record.Status) {
			return renderError(http.StatusInternalServerError, "Your response could not be displayed. Please try again."), nil
		}
		return render(http.StatusOK, page{Title: "Status recorded", Date: date.Format("January 2, 2006"), Label: userLabel(record.Status), Message: "Your response has been saved."}), nil
	default:
		response := renderError(http.StatusMethodNotAllowed, "Please use GET or POST for this page.")
		response.Headers["Allow"] = "GET, POST"
		return response, nil
	}
}

// Only a local absolute path can receive the bearer token. RawPath normally has
// no query; reject malformed event paths rather than posting to another origin.
func formAction(path string) (string, error) {
	if path == "" {
		path = "/"
	}
	parsed, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "\\") || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || strings.ContainsAny(path, "?#\r\n") {
		return "", errors.New("invalid form path")
	}
	return path, nil
}

const maxFormBody = 16 * 1024

func formToken(request events.LambdaFunctionURLRequest) (string, error) {
	body := request.Body
	if request.IsBase64Encoded {
		if len(body) > base64.StdEncoding.EncodedLen(maxFormBody) {
			return "", errors.New("form too large")
		}
		decoded, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return "", err
		}
		body = string(decoded)
	}
	if len(body) > maxFormBody {
		return "", errors.New("form too large")
	}
	values, err := url.ParseQuery(body)
	if err != nil {
		return "", err
	}
	tokens := values["token"]
	if len(tokens) != 1 || tokens[0] == "" {
		return "", errors.New("one non-empty token required")
	}
	return tokens[0], nil
}

func serviceError(err error) events.LambdaFunctionURLResponse {
	switch {
	case errors.Is(err, respond.ErrInvalidToken):
		return renderError(http.StatusBadRequest, "This response link is invalid.")
	case errors.Is(err, respond.ErrDayNotFound):
		return renderError(http.StatusNotFound, "The day record for this response link was not found.")
	case errors.Is(err, respond.ErrConflict):
		return renderError(http.StatusConflict, "The record changed while your response was being saved. Please reopen the link and try again.")
	default:
		return renderError(http.StatusInternalServerError, "Your request could not be completed. Please try again.")
	}
}

func userLabel(status tracker.Status) string {
	switch status {
	case tracker.StatusFullDay:
		return "Full Day"
	case tracker.StatusHalfDay:
		return "Half Day"
	case tracker.StatusPTO:
		return "PTO"
	case tracker.StatusTimeOff:
		return "Time Off"
	default:
		return "Unknown"
	}
}
