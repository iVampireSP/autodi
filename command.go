package main

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// DiscoveredCommand represents a per-binary entry point: a package main under
// cmd/<face> with exactly one //autodi:inject-annotated root assembler. autodi
// generates cmd/<face>/gen.go with func Inject() that wires the assembler's
// parameters from the provider graph and calls it.
type DiscoveredCommand struct {
	Name       string      // dir name with '/'→'_' (for log messages)
	RelDir     string      // module-relative dir, e.g. "cmd/admin-api"
	PkgPath    string      // full import path of the cmd package
	PkgName    string      // Go package name (always "main")
	FuncName   string      // the //autodi:inject assembler, e.g. "newAdminServer"
	Params     []TypeRef   // assembler parameters — the roots to resolve
	PrimaryRet TypeRef     // primary returned value, e.g. *httpserver.Server
	HasCleanup bool        // assembler returns a func() cleanup
	HasError   bool        // assembler returns error (last result)
	Local      []*Provider // cmd-local glue providers (New*/new* in this package, excluding the assembler)
	Ignore     []string    // cmd-side //autodi:ignore "<importpath>.<Func>" exclusions
}

// HasDeps reports whether the assembler takes any parameters to wire.
func (dc *DiscoveredCommand) HasDeps() bool {
	return len(dc.Params) > 0
}

// CommandDetector scans cmd/ packages for //autodi:inject entry points.
type CommandDetector struct {
	cfg        *Config
	moduleRoot string
	gitignore  []GitignorePattern
}

// NewCommandDetector creates an entry-point detector.
func NewCommandDetector(cfg *Config, moduleRoot string, gitignore []GitignorePattern) *CommandDetector {
	return &CommandDetector{cfg: cfg, moduleRoot: moduleRoot, gitignore: gitignore}
}

// Detect loads cmd/... packages and returns one DiscoveredCommand per package
// that declares an //autodi:inject assembler. Packages without one are skipped.
func (d *CommandDetector) Detect() ([]*DiscoveredCommand, error) {
	pattern := d.cfg.Module + "/cmd/..."
	pkgCfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedFiles | packages.NeedImports,
		Dir: d.moduleRoot,
	}
	pkgs, err := packages.Load(pkgCfg, pattern)
	if err != nil {
		return nil, fmt.Errorf("load cmd packages: %w", err)
	}

	// A scanner instance lets us reuse buildProvider for cmd-local providers.
	sc := NewScanner(d.cfg, d.moduleRoot, d.gitignore)
	if len(pkgs) > 0 {
		sc.fset = pkgs[0].Fset
	}

	var commands []*DiscoveredCommand
	for _, pkg := range pkgs {
		rel := strings.TrimPrefix(pkg.PkgPath, d.cfg.Module+"/")
		if rel == "cmd" || !strings.HasPrefix(rel, "cmd/") {
			continue
		}
		cmd := d.analyzePackage(pkg, rel, sc)
		if cmd != nil {
			commands = append(commands, cmd)
		}
	}

	sort.Slice(commands, func(i, j int) bool { return commands[i].RelDir < commands[j].RelDir })
	return commands, nil
}

// analyzePackage finds the //autodi:inject assembler in a cmd package and
// collects its cmd-local glue providers.
func (d *CommandDetector) analyzePackage(pkg *packages.Package, rel string, sc *Scanner) *DiscoveredCommand {
	var assembler *DiscoveredCommand
	var locals []*Provider
	var ignores []string

	for _, f := range pkg.Syntax {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			annotations := ParseAnnotations(fn)
			for _, v := range GetAnnotationValues(annotations, AnnotIgnore) {
				ignores = append(ignores, v)
			}

			obj := pkg.TypesInfo.Defs[fn.Name]
			funcObj, ok := obj.(*types.Func)
			if !ok {
				continue
			}
			sig, ok := funcObj.Type().(*types.Signature)
			if !ok {
				continue
			}

			if HasAnnotation(annotations, AnnotInject) {
				if sig.Results().Len() == 0 {
					continue
				}
				primary := sig.Results().At(0).Type()
				hasCleanup, hasError := false, false
				for i := 1; i < sig.Results().Len(); i++ {
					t := sig.Results().At(i).Type()
					switch {
					case isErrorType(t):
						hasError = true
					case isCleanupFunc(t):
						hasCleanup = true
					}
				}
				assembler = &DiscoveredCommand{
					Name:     strings.ReplaceAll(strings.TrimPrefix(rel, "cmd/"), "/", "_"),
					RelDir:   rel,
					PkgPath:  pkg.PkgPath,
					PkgName:  pkg.Name,
					FuncName: fn.Name.Name,
					Params:   extractParams(sig, annotations),
					PrimaryRet: TypeRef{
						Type:    primary,
						TypeStr: types.TypeString(primary, nil),
						PkgPath: typePkgPath(primary),
						IsIface: isInterface(primary),
					},
					HasCleanup: hasCleanup,
					HasError:   hasError,
				}
				continue
			}

			// cmd-local glue providers: New*/new* funcs that return something.
			if HasAnnotation(annotations, AnnotIgnore) {
				continue
			}
			name := fn.Name.Name
			if !strings.HasPrefix(name, "New") && !strings.HasPrefix(name, "new") {
				continue
			}
			if strings.Contains(name, "With") || strings.Contains(name, "From") {
				continue
			}
			if p := sc.buildProvider(pkg, fn, annotations); p != nil {
				locals = append(locals, p)
			}
		}
	}

	if assembler == nil {
		return nil
	}
	// Drop the assembler itself from locals (it is the entry, not a dependency).
	for _, p := range locals {
		if p.FuncName == assembler.FuncName {
			continue
		}
		assembler.Local = append(assembler.Local, p)
	}
	assembler.Ignore = ignores
	return assembler
}

// isCleanupFunc reports whether t is a func() (no params, no results) — the
// conventional cleanup return.
func isCleanupFunc(t types.Type) bool {
	sig, ok := t.Underlying().(*types.Signature)
	return ok && sig.Params().Len() == 0 && sig.Results().Len() == 0
}
