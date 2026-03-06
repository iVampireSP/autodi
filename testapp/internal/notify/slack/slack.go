package slack

import (
	"fmt"

	"example.com/testapp/internal/config"
)

// Slack sends notifications via a Slack incoming webhook.
type Slack struct {
	url string
}

// NewSlack creates a Slack notifier.
func NewSlack(cfg *config.Config) *Slack {
	return &Slack{url: cfg.SlackURL}
}

// Name implements notify.Notifier.
func (s *Slack) Name() string { return "slack" }

// Send implements notify.Notifier.
func (s *Slack) Send(to, subject, body string) error {
	fmt.Printf("[slack] subject=%q webhook=%s\n", subject, s.url)
	return nil
}
