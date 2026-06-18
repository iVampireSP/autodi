// Command autodi is a compile-time dependency-injection code generator.
//
// autodi scans a Go module for exported New* constructors (the providers),
// builds a dependency graph by type analysis, and — for each cmd/<face> package
// that declares an //autodi:inject root assembler — emits cmd/<face>/gen.go with
// a single func Inject() (T, func(), error) that constructs the whole graph in
// topological order and calls the assembler. No runtime reflection, no DI
// container.
//
// Each cmd/<face> is an independent program. Shared code (internal/, pkg/, …)
// stays completely autodi-agnostic: fan-in slices ([]Iface) are auto-collected,
// single-implementation interfaces are auto-bound, and constructors that take
// runtime data (string/int/…) rather than injectable dependencies are skipped by
// the satisfiability filter — so no //autodi annotations are ever needed outside
// cmd/.
//
// Usage (from a cmd dir, or repo root):
//
//	//go:generate go run github.com/iVampireSP/autodi
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	verbose := flag.Bool("verbose", false, "enable verbose logging")
	dryRun := flag.Bool("dry-run", false, "print generated code without writing")
	flag.Parse()

	moduleRoot, err := findModuleRoot()
	if err != nil {
		log.Fatalf("autodi: %v", err)
	}

	cfg, err := BuildConfig(moduleRoot)
	if err != nil {
		log.Fatalf("autodi: %v", err)
	}
	if *verbose {
		fmt.Fprintf(os.Stderr, "autodi: module=%s root=%s\n", cfg.Module, moduleRoot)
	}

	totalStart := time.Now()
	gitignore := LoadGitignore(moduleRoot)

	// Scan shared trees (internal/, pkg/, …) for provider candidates.
	scanner := NewScanner(cfg, moduleRoot, gitignore)
	shared, err := scanner.Scan()
	if err != nil {
		log.Fatalf("autodi: scan: %v", err)
	}
	if *verbose {
		fmt.Fprintf(os.Stderr, "autodi: scanned %d shared providers\n", len(shared))
	}

	// Discover per-cmd entry points (//autodi:inject assemblers).
	detector := NewCommandDetector(cfg, moduleRoot, gitignore)
	entries, err := detector.Detect()
	if err != nil {
		log.Fatalf("autodi: detect entries: %v", err)
	}
	if len(entries) == 0 {
		log.Fatalf("autodi: no //autodi:inject entry points found under cmd/")
	}

	var generated int
	for _, entry := range entries {
		if *verbose {
			fmt.Fprintf(os.Stderr, "autodi: entry %s → %s (%d roots, %d local providers)\n",
				entry.RelDir, entry.FuncName, len(entry.Params), len(entry.Local))
		}

		file, err := generateForEntry(cfg, scanner, shared, entry, *verbose)
		if err != nil {
			fmt.Fprintf(os.Stderr, "autodi: %s: %v\n", entry.RelDir, err)
			os.Exit(1)
		}

		if *dryRun {
			fmt.Fprintf(os.Stdout, "// === %s ===\n%s\n", file.Name, file.Content)
			continue
		}
		path := filepath.Join(moduleRoot, file.Name)
		if err := os.WriteFile(path, file.Content, 0644); err != nil {
			log.Fatalf("autodi: write %s: %v", path, err)
		}
		if *verbose {
			fmt.Fprintf(os.Stderr, "autodi: wrote %s\n", path)
		}
		generated++
	}

	if !*dryRun {
		fmt.Fprintf(os.Stderr, "autodi: generated %d files in %s\n", generated, time.Since(totalStart))
	}
}

// generateForEntry builds the per-cmd dependency graph and emits its gen.go.
func generateForEntry(cfg *Config, scanner *Scanner, shared []*Provider, entry *DiscoveredCommand, verbose bool) (GeneratedFile, error) {
	// This binary's private subtree (cmd/<face>/internal/...) holds its handlers,
	// sub-routers, etc. — scan them as candidates for this entry only.
	subtree, err := scanner.ScanSubtree(entry.RelDir)
	if err != nil {
		return GeneratedFile{}, fmt.Errorf("scan subtree: %w", err)
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "autodi: scanned %d providers under %s\n", len(subtree), entry.RelDir)
	}

	// Candidates = shared + this binary's subtree + cmd-local glue, minus cmd-side ignores.
	candidates := make([]*Provider, 0, len(shared)+len(subtree)+len(entry.Local))
	candidates = append(candidates, shared...)
	candidates = append(candidates, subtree...)
	candidates = append(candidates, entry.Local...)
	if len(entry.Ignore) > 0 {
		candidates = applyIgnores(candidates, entry.Ignore)
	}

	// Reachable from the assembler's params, then drop unwireable constructors.
	reachable := FilterReachable(candidates, []*DiscoveredCommand{entry}, cfg, scanner.IfaceTypes, verbose)
	live := FilterSatisfiable(reachable, scanner.IfaceTypes, verbose)

	graph, errs := BuildGraph(live, cfg, scanner.PkgIndex, scanner.IfaceTypes)
	if len(errs) > 0 {
		return GeneratedFile{}, fmt.Errorf("build graph:\n  %s", joinErrors(errs))
	}
	if errs := graph.VerifyAcyclic(); len(errs) > 0 {
		return GeneratedFile{}, fmt.Errorf("dependency cycle:\n  %s", joinErrors(errs))
	}
	graph.BindCommandInterfaces([]*DiscoveredCommand{entry})

	// Validate the assembler's own dependencies resolve.
	var neededTypes []string
	for _, param := range entry.Params {
		neededTypes = append(neededTypes, param.TypeStr)
	}
	pp, err := graph.ProvidersForTypes(neededTypes)
	if err != nil {
		return GeneratedFile{}, fmt.Errorf("resolve roots: %w", err)
	}
	if errs := graph.ValidateEntry(entry.RelDir, pp); len(errs) > 0 {
		return GeneratedFile{}, fmt.Errorf("unsatisfied dependencies:\n  %s", joinErrors(errs))
	}

	gen := NewCodeGen(cfg, graph, []*DiscoveredCommand{entry}, scanner.moduleRoot)
	files, err := gen.Generate()
	if err != nil {
		return GeneratedFile{}, err
	}
	if len(files) != 1 {
		return GeneratedFile{}, fmt.Errorf("expected 1 generated file, got %d", len(files))
	}
	return files[0], nil
}

// applyIgnores drops candidates whose "<importpath>.<Func>" matches a cmd-side
// //autodi:ignore directive (suffix match, so "internal/infra/cache.NewLocker"
// or just "cache.NewLocker" both work).
func applyIgnores(candidates []*Provider, ignores []string) []*Provider {
	out := candidates[:0:0]
	for _, p := range candidates {
		full := p.PkgPath + "." + p.FuncName
		drop := false
		for _, ig := range ignores {
			if full == ig || strings.HasSuffix(full, "/"+ig) || strings.HasSuffix(full, "."+ig) ||
				p.PkgName+"."+p.FuncName == ig {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, p)
		}
	}
	return out
}

func joinErrors(errs []error) string {
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Error()
	}
	return strings.Join(parts, "\n  ")
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
