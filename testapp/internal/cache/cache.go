package cache

import (
	"fmt"

	"example.com/testapp/internal/config"
)

// Cache is an in-memory cache client stub.
type Cache struct {
	addr string
}

// NewCache creates a cache client.
func NewCache(cfg *config.Config) *Cache {
	fmt.Printf("[cache] connecting to %s\n", cfg.CacheAddr)
	return &Cache{addr: cfg.CacheAddr}
}

// Close releases cache resources.
func (c *Cache) Close() {
	fmt.Printf("[cache] closing %s\n", c.addr)
}

// Get retrieves a cached value.
func (c *Cache) Get(key string) string { return "" }

// Set stores a value.
func (c *Cache) Set(key, val string) {}
