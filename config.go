package main

// Config holds autodi configuration, derived purely from go.mod + directory
// conventions (no central generate.go).
type Config struct {
	Module   string
	Scan     []string
	Exclude  []string
	Output   string
	Bindings map[string][]string    // concrete type → interface list (auto-detected)
	Groups   map[string]GroupConfig // legacy explicit-group support; unpopulated in normal use ([]iface fan-in is handled by AutoCollect)
}

// GroupConfig defines a collection of providers implementing an interface.
type GroupConfig struct {
	Interface string
	Paths     []string
}
