// Package main implements autodi, a compile-time dependency injection code generator.
//
// autodi scans Go packages for exported New* constructor functions, builds a
// dependency graph via type analysis, performs topological sorting with cycle
// detection, and generates a complete main.go with two-phase DI — replacing
// runtime DI frameworks like uber/fx with zero-reflection, compile-time safe code.
//
// Generation flow:
//
//  1. Read go.mod → module path
//  2. Read generate.go → //autodi:app/embed/group annotations
//  3. Scan internal/ + pkg/ → provider discovery (New* constructors)
//  4. Build dependency graph + resolve bindings + detect Close/Shutdown/Stop
//  5. Scan cmd/ → discover commands:
//     - New*(deps...) returns *T with Command() method + handler methods
//     - New*() zero-dep returns *T with same pattern
//  6. For each DI command:
//     Analyze New* params → trace transitive deps → generate init function
//  7. Generate main.go with two-phase DI
//
// Usage:
//
//	//go:generate go run github.com/iVampireSP/autodi@latest
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

func main() {
	verbose := flag.Bool("verbose", false, "enable verbose logging")
	dryRun := flag.Bool("dry-run", false, "print generated code without writing")
	flag.Parse()

	// Resolve module root: walk up from cwd to find go.mod
	moduleRoot, err := findModuleRoot()
	if err != nil {
		log.Fatalf("autodi: %v", err)
	}

	// Build config from conventions (go.mod + generate.go)
	cfg, err := BuildConfig(moduleRoot)
	if err != nil {
		log.Fatalf("autodi: %v", err)
	}

	if *verbose {
		fmt.Fprintf(os.Stderr, "autodi: module=%s root=%s\n", cfg.Module, moduleRoot)
		fmt.Fprintf(os.Stderr, "autodi: app=%s\n", cfg.AppName)
	}

	// Load gitignore patterns
	gitignorePatterns := LoadGitignore(moduleRoot)

	// ── Pass 1: Scan providers and build dependency graph ──

	scanner := NewScanner(cfg, moduleRoot, gitignorePatterns)
	providers, err := scanner.Scan()
	if err != nil {
		log.Fatalf("autodi: scan: %v", err)
	}

	if *verbose {
		fmt.Fprintf(os.Stderr, "autodi: discovered %d providers\n", len(providers))
	}

	graph, errs := BuildGraph(providers, cfg, scanner.PkgIndex, scanner.IfaceTypes)
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "autodi: %v\n", e)
		}
		os.Exit(1)
	}

	if errs := graph.VerifyAcyclic(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "autodi: %v\n", e)
		}
		os.Exit(1)
	}

	// ── Pass 2: Discover commands from cmd/ packages ──

	detector := NewCommandDetector(cfg, moduleRoot)
	commands, err := detector.Detect()
	if err != nil {
		log.Fatalf("autodi: detect commands: %v", err)
	}

	// Resolve interface bindings for command parameters
	graph.BindCommandInterfaces(commands)

	if *verbose {
		fmt.Fprintf(os.Stderr, "autodi: discovered %d commands\n", len(commands))
		for _, cmd := range commands {
			var paramTypes []string
			for _, p := range cmd.Params {
				paramTypes = append(paramTypes, toShortTypeName(p.TypeStr))
			}
			kind := "multi"
			if cmd.IsSingle {
				kind = "single"
			}
			if !cmd.HasDeps() {
				kind += "/zero-dep"
			}
			var handlers []string
			for _, h := range cmd.Handlers {
				handlers = append(handlers, h.MethodName)
			}
			fmt.Fprintf(os.Stderr, "  [%s] %s: %s.%s(%s) → [%s]\n",
				kind, cmd.Name, cmd.StructName, cmd.FuncName,
				joinStrings(paramTypes, ", "), joinStrings(handlers, ", "))
		}
	}

	// Validate per-command dependencies
	hasValidationErr := false
	for _, cmd := range commands {
		if !cmd.HasDeps() {
			continue
		}
		var neededTypes []string
		for _, param := range cmd.Params {
			neededTypes = append(neededTypes, param.TypeStr)
		}
		pp, err := graph.ProvidersForTypes(neededTypes)
		if err != nil {
			fmt.Fprintf(os.Stderr, "autodi: command %s: %v\n", cmd.Name, err)
			hasValidationErr = true
			continue
		}
		if errs := graph.ValidateEntry(cmd.Name, pp); len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "autodi: %v\n", e)
			}
			hasValidationErr = true
		}
		if *verbose {
			fmt.Fprintf(os.Stderr, "autodi: command %s: %d providers\n", cmd.Name, len(pp))
		}
	}
	if hasValidationErr {
		os.Exit(1)
	}

	// ── Generate code ──

	gen := NewCodeGen(cfg, graph, commands, moduleRoot)
	files, err := gen.Generate()
	if err != nil {
		log.Fatalf("autodi: generate: %v", err)
	}

	// Write or print generated files
	for _, f := range files {
		if *dryRun {
			fmt.Fprintf(os.Stdout, "// === %s ===\n%s\n", f.Name, f.Content)
			continue
		}
		path := filepath.Join(moduleRoot, f.Name)
		if *verbose {
			fmt.Fprintf(os.Stderr, "autodi: writing %s\n", path)
		}
		if err := os.WriteFile(path, f.Content, 0644); err != nil {
			log.Fatalf("autodi: write %s: %v", path, err)
		}
	}

	if !*dryRun {
		fmt.Fprintf(os.Stderr, "autodi: generated %d files\n", len(files))
	}
}

// findModuleRoot walks up from cwd to find the directory containing go.mod.
func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("go.mod not found in any parent directory")
}

func joinStrings(ss []string, sep string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}
