package orm

import (
	"fmt"

	"example.com/testapp/ent"
	"example.com/testapp/internal/config"
)

// NewORM creates a new ent client.
// In a real app this would connect to MySQL/PostgreSQL via ent's SQL driver.
func NewORM(cfg *config.Config) *ent.Client {
	fmt.Printf("[orm] connecting ent client to %s\n", cfg.DBPath)
	// In production: sql.Open → entsql.OpenDB → ent.NewClient(ent.Driver(drv))
	// Here we use an in-memory client for the demo.
	client := ent.NewClient()
	return client
}

// CloseEntClient closes the ent client connection.
func CloseEntClient(client *ent.Client) error {
	return client.Close()
}
