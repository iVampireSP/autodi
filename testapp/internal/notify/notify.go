package notify

// Notifier can deliver a notification through some channel.
type Notifier interface {
	// Name returns a human-readable label for this notifier (e.g. "email", "slack").
	Name() string
	// Send delivers a message.
	Send(to, subject, body string) error
}
