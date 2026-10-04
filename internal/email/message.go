// Package email composes daily prompts and provides an SES transport.
package email

// Message contains rendered content independently of transport addressing.
type Message struct {
	Subject  string
	TextBody string
	HTMLBody string
}
