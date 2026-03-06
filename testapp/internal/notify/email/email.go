package email

import (
	"fmt"

	"example.com/testapp/internal/config"
)

// Email sends notifications via SMTP.
type Email struct {
	host string
}

// NewEmail creates an Email notifier.
func NewEmail(cfg *config.Config) *Email {
	return &Email{host: cfg.SMTPHost}
}

// Name implements notify.Notifier.
func (e *Email) Name() string { return "email" }

// Send implements notify.Notifier.
func (e *Email) Send(to, subject, body string) error {
	fmt.Printf("[email] to=%s subject=%q via=%s\n", to, subject, e.host)
	return nil
}
