package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BuildConfig builds a Config from go.mod + generate.go conventions.
func BuildConfig(moduleRoot string) (*Config, error) {
	module, err := parseModulePath(moduleRoot)
	if err != nil {
		return nil, err
	}

	appName, appShort, appLong, groups, err := parseGenerateFile(moduleRoot)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Module: module,
		Scan: []string{
			"internal/...",
			"pkg/...",
		},
		Exclude:  []string{},
		Output:   ".",
		Bindings: make(map[string][]string),
		Groups:   groups,
		AppName:  appName,
		AppShort: appShort,
		AppLong:  appLong,
	}
	return cfg, nil
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
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", fmt.Errorf("module directive not found in go.mod")
}

func parseGenerateFile(root string) (appName, appShort, appLong string, groups map[string]GroupConfig, err error) {
	path := filepath.Join(root, "generate.go")
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		err = fmt.Errorf("read generate.go: %w", readErr)
		return
	}

	groups = make(map[string]GroupConfig)

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "//autodi:") {
			continue
		}
		directive := strings.TrimPrefix(line, "//autodi:")
		parts := strings.Fields(directive)
		if len(parts) == 0 {
			continue
		}

		switch parts[0] {
		case "app":
			// //autodi:app leaflow "Leaflow Cloud" "Leaflow Cloud Management CLI Tool"
			if len(parts) >= 2 {
				appName = parts[1]
			}
			rest := strings.TrimSpace(strings.TrimPrefix(directive, "app "+appName))
			quoted := parseQuotedStrings(rest)
			if len(quoted) >= 1 {
				appShort = quoted[0]
			}
			if len(quoted) >= 2 {
				appLong = quoted[1]
			}

		case "group":
			// //autodi:group user_controllers []apis.Controller internal/apis/user/controllers
			if len(parts) >= 4 {
				groupName := parts[1]
				ifaceType := strings.TrimPrefix(parts[2], "[]")
				groupPath := parts[3]
				groups[groupName] = GroupConfig{
					Interface: ifaceType,
					Paths:     []string{groupPath},
				}
			}
		}
	}

	return
}

// parseQuotedStrings extracts "quoted strings" from text.
func parseQuotedStrings(s string) []string {
	var result []string
	for {
		start := strings.Index(s, `"`)
		if start < 0 {
			break
		}
		end := strings.Index(s[start+1:], `"`)
		if end < 0 {
			break
		}
		result = append(result, s[start+1:start+1+end])
		s = s[start+1+end+1:]
	}
	return result
}
