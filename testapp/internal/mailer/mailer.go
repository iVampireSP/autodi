package mailer

import "example.com/testapp/internal/notify"

// Mailer dispatches a notification to every registered Notifier.
// autodi will auto-collect all providers that implement notify.Notifier
// and inject them as the slice argument.
type Mailer struct {
	notifiers []notify.Notifier
}

// NewMailer creates a Mailer with all auto-discovered Notifier implementations.
func NewMailer(notifiers []notify.Notifier) *Mailer {
	return &Mailer{notifiers: notifiers}
}

// Notify sends to all registered notifiers.
func (m *Mailer) Notify(to, subject, body string) {
	for _, n := range m.notifiers {
		_ = n.Send(to, subject, body)
	}
}
