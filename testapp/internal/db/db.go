package db

import (
	"fmt"

	"example.com/testapp/internal/config"
)

// DB is a simple database client stub.
type DB struct {
	path string
}

// NewDB opens a database connection.
func NewDB(cfg *config.Config) *DB {
	fmt.Printf("[db] connecting to %s\n", cfg.DBPath)
	return &DB{path: cfg.DBPath}
}

// Close releases the database connection.
func (db *DB) Close() {
	fmt.Printf("[db] closing %s\n", db.path)
}

// Query executes a query and returns rows (stub).
func (db *DB) Query(q string) []string {
	return nil
}
