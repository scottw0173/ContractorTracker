package respondweb

import (
	"bytes"
	"html/template"
	"net/http"

	"github.com/aws/aws-lambda-go/events"
	"github.com/scottw0173/ContractorTracker/internal/respond"
)

type page struct {
	Title, Date, Label, Message, Action string
	Confirmation                        *respond.Confirmation
}

var pageTemplate = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title><style>body{font-family:system-ui,sans-serif;line-height:1.5;margin:2rem auto;padding:0 1rem;max-width:36rem}button{font:inherit;padding:.7rem 1rem;cursor:pointer}</style></head>
<body><main><h1>{{.Title}}</h1>
{{if .Confirmation}}
<p>Date: {{.Confirmation.FriendlyDate}}</p>
<p>Current status: {{.Confirmation.CurrentLabel}}</p>
<p>Selected status: {{.Confirmation.RequestedLabel}}</p>
{{if .Confirmation.IsCorrection}}<p>Change status from {{.Confirmation.CurrentLabel}} to {{.Confirmation.RequestedLabel}}?</p>{{end}}
<form method="post" action="{{.Action}}">
<input type="hidden" name="token" value="{{.Confirmation.Token}}">
<button type="submit">Confirm {{.Confirmation.RequestedLabel}}</button></form>
{{else}}
{{if .Date}}<p>{{.Date}}</p>{{end}}{{if .Label}}<p>{{.Label}}</p>{{end}}<p>{{.Message}}</p>
{{end}}</main></body></html>`))

func render(status int, data page) events.LambdaFunctionURLResponse {
	var body bytes.Buffer
	if err := pageTemplate.Execute(&body, data); err != nil {
		status = http.StatusInternalServerError
		body.Reset()
		body.WriteString("<!doctype html><html lang=\"en\"><title>Request failed</title><p>Your request could not be completed. Please try again.</p></html>")
	}
	return events.LambdaFunctionURLResponse{StatusCode: status, Body: body.String(), Headers: map[string]string{
		"Content-Type":            "text/html; charset=utf-8",
		"Cache-Control":           "no-store",
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'",
	}}
}

func renderError(status int, message string) events.LambdaFunctionURLResponse {
	return render(status, page{Title: http.StatusText(status), Message: message})
}
