package config

// Config holds all application configuration.
type Config struct {
	DBPath    string
	CacheAddr string
	SMTPHost  string
	SlackURL  string
}

// NewConfig returns a Config populated from defaults (real apps would use env vars / flags).
func NewConfig() *Config {
	return &Config{
		DBPath:    "data.db",
		CacheAddr: "localhost:6379",
		SMTPHost:  "smtp.example.com",
		SlackURL:  "https://hooks.slack.com/services/xxx",
	}
}
