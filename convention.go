package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuildConfig builds a Config purely from go.mod + directory conventions.
//
// There is no central generate.go / //autodi:app config: each cmd/<face> is an
// independent program that declares its own root via //autodi:inject, and shared
// trees (internal/, pkg/, …) stay completely autodi-agnostic. We only need the
// module path and the set of top-level dirs to scan for provider candidates.
func BuildConfig(moduleRoot string) (*Config, error) {
	module, err := parseModulePath(moduleRoot)
	if err != nil {
		return nil, err
	}

	gitignore := LoadGitignore(moduleRoot)
	scan, err := discoverScanPaths(moduleRoot, gitignore)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Module:   module,
		Scan:     scan,
		Output:   ".",
		Bindings: make(map[string][]string),
		Groups:   make(map[string]GroupConfig),
	}
	return cfg, nil
}

// discoverScanPaths enumerates top-level directories in the module root and
// returns them as scan patterns (e.g. "internal/..."), excluding:
//   - cmd/       — entry-point packages, handled by EntryDetector
//   - dot dirs   — hidden (.git, .claude, …)
//   - gitignored directories
func discoverScanPaths(root string, gitignore []GitignorePattern) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read module root: %w", err)
	}

	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if name == "cmd" {
			continue
		}
		if IsGitignored(name, gitignore) {
			continue
		}
		paths = append(paths, name+"/...")
	}
	return paths, nil
}

func parseModulePath(root string) (string, error) {
	f, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("open go.mod: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if after, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(after), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan go.mod: %w", err)
	}
	return "", fmt.Errorf("module directive not found in go.mod")
}
