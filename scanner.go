package main

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Provider represents a discovered New* constructor function.
type Provider struct {
	FuncName    string         // e.g., "NewIAM"
	PkgPath     string         // e.g., "github.com/LeaflowNET/cloud/internal/services/iam"
	PkgName     string         // e.g., "iam"
	Params      []TypeRef      // input parameters (dependencies)
	Returns     []TypeRef      // return values (provided types)
	HasError    bool           // last return is error
	IsInvoke    bool           // call-only, no stored result
	Annotations []Annotation   // parsed //autodi: directives
	Position    token.Position // source location for errors

	// Resolved during graph building
	Groups []string // group memberships
}

// TypeRef describes a single type in a provider's signature.
type TypeRef struct {
	Type     types.Type
	TypeStr  string // qualified string like "*ent.Client", "iam.AuthN"
	PkgPath  string // package path for this type
	IsIface  bool   // whether this is an interface type
	Optional bool   // from //autodi:optional
}

// Scanner discovers providers by loading and analyzing Go packages.
type Scanner struct {
	cfg        *Config
	moduleRoot string
	gitignore  []GitignorePattern
	fset       *token.FileSet

	// PkgIndex maps package short name → full package path for all loaded packages.
	PkgIndex map[string]string

	// IfaceTypes maps full type string → *types.Interface for all exported interface
	// types discovered in loaded packages. Used by AutoCollect to find interface types
	// that aren't directly referenced in any provider's params/returns.
	IfaceTypes map[string]*types.Interface
}

// NewScanner creates a scanner.
func NewScanner(cfg *Config, moduleRoot string, gitignore []GitignorePattern) *Scanner {
	return &Scanner{
		cfg:        cfg,
		moduleRoot: moduleRoot,
		gitignore:  gitignore,
	}
}

// Scan loads packages and extracts providers.
func (s *Scanner) Scan() ([]*Provider, error) {
	// Build package patterns from scan config
	patterns := s.buildPatterns()

	// Load packages with full type info
	cfg := &packages.Config{
		Mode: packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedName |
			packages.NeedFiles | packages.NeedImports,
		Dir: s.moduleRoot,
	}

	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}

	// Check for package loading errors
	var loadErrs []string
	for _, pkg := range pkgs {
		for _, e := range pkg.Errors {
			loadErrs = append(loadErrs, e.Error())
		}
	}
	if len(loadErrs) > 0 {
		return nil, fmt.Errorf("package errors:\n  %s", strings.Join(loadErrs, "\n  "))
	}

	s.fset = pkgs[0].Fset

	// Build package index from all loaded packages and their imports
	s.PkgIndex = make(map[string]string)
	for _, pkg := range pkgs {
		s.PkgIndex[pkg.Name] = pkg.PkgPath
		for _, imp := range pkg.Imports {
			s.PkgIndex[imp.Name] = imp.PkgPath
		}
	}

	// Extract interface types from all loaded packages (and their in-module imports)
	s.buildIfaceTypes(pkgs)

	// Extract providers from each package
	var providers []*Provider
	for _, pkg := range pkgs {
		if s.shouldExclude(pkg.PkgPath) {
			continue
		}
		found := s.extractProviders(pkg)
		providers = append(providers, found...)
	}

	return providers, nil
}

// buildIfaceTypes extracts all exported interface types from loaded packages
// and their in-module imports. This allows AutoCollect to find interface types
// that aren't directly used in any provider's signature.
func (s *Scanner) buildIfaceTypes(pkgs []*packages.Package) {
	s.IfaceTypes = make(map[string]*types.Interface)
	visited := make(map[string]bool)

	var extract func(pkg *packages.Package)
	extract = func(pkg *packages.Package) {
		if pkg.Types == nil || visited[pkg.PkgPath] {
			return
		}
		visited[pkg.PkgPath] = true

		scope := pkg.Types.Scope()
		for _, name := range scope.Names() {
			obj := scope.Lookup(name)
			if !obj.Exported() {
				continue
			}
			if iface, ok := obj.Type().Underlying().(*types.Interface); ok {
				typeStr := types.TypeString(obj.Type(), nil)
				s.IfaceTypes[typeStr] = iface
			}
		}
		// Also process imports within the same module
		for _, imp := range pkg.Imports {
			if strings.HasPrefix(imp.PkgPath, s.cfg.Module) {
				extract(imp)
			}
		}
	}

	for _, pkg := range pkgs {
		extract(pkg)
	}
}

// buildPatterns converts scan config paths to Go package patterns.
// Skips cmd/ paths — those are handled by EntryDetector with AST-only loading.
func (s *Scanner) buildPatterns() []string {
	var patterns []string
	for _, scan := range s.cfg.Scan {
		p := strings.TrimPrefix(scan, "./")
		// Skip cmd/ packages — they don't have providers, only entry points
		if strings.HasPrefix(p, "cmd/") || p == "cmd/..." || p == "cmd" {
			continue
		}
		patterns = append(patterns, s.cfg.Module+"/"+p)
	}
	return patterns
}

// shouldExclude checks if a package path should be excluded.
func (s *Scanner) shouldExclude(pkgPath string) bool {
	// Check explicit excludes
	for _, exc := range s.cfg.Exclude {
		excPath := strings.TrimPrefix(exc, "./")
		excPath = strings.TrimSuffix(excPath, "/...")
		full := s.cfg.Module + "/" + excPath
		if strings.HasPrefix(pkgPath, full) {
			return true
		}
	}

	// Check gitignore
	rel := strings.TrimPrefix(pkgPath, s.cfg.Module+"/")
	return IsGitignored(rel, s.gitignore)
}

// extractProviders finds the PRIMARY exported New* function in a package.
// Following the project convention: one exported New per package.
// Selection priority:
//  1. Functions with //autodi:bind or //autodi:invoke annotations (always included)
//  2. "New" + PkgName (e.g., NewIAM in package iam) — canonical form
//  3. "New" + exported struct name matching package (e.g., NewService in user pkg)
//  4. Bare "New" function (e.g., redisx.New)
//
// Functions with "WithConfig", "WithXxx" suffixes are skipped as variants.
func (s *Scanner) extractProviders(pkg *packages.Package) []*Provider {
	type candidate struct {
		fn          *ast.FuncDecl
		annotations []Annotation
		priority    int // lower is better
	}
	var candidates []candidate
	var alwaysInclude []*Provider // annotated functions always included

	for _, f := range pkg.Syntax {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if !fn.Name.IsExported() || !strings.HasPrefix(fn.Name.Name, "New") {
				continue
			}

			annotations := ParseAnnotations(fn)
			if HasAnnotation(annotations, AnnotIgnore) {
				continue
			}

			// Skip variant constructors (NewXxxWithConfig, NewXxxFromYyy, etc.)
			name := fn.Name.Name
			if strings.Contains(name, "With") || strings.Contains(name, "From") {
				continue
			}

			obj := pkg.TypesInfo.Defs[fn.Name]
			if obj == nil {
				continue
			}
			funcObj, ok := obj.(*types.Func)
			if !ok {
				continue
			}
			sig := funcObj.Type().(*types.Signature)

			returns, hasError := s.extractReturns(sig)
			if len(returns) == 0 {
				continue
			}

			params := s.extractParams(sig, annotations)

			provider := &Provider{
				FuncName:    fn.Name.Name,
				PkgPath:     pkg.PkgPath,
				PkgName:     pkg.Name,
				Params:      params,
				Returns:     returns,
				HasError:    hasError,
				IsInvoke:    HasAnnotation(annotations, AnnotInvoke),
				Annotations: annotations,
				Position:    s.fset.Position(fn.Pos()),
			}

			// Annotated functions are always included (they opted in explicitly)
			if HasAnnotation(annotations, AnnotBind) || HasAnnotation(annotations, AnnotInvoke) {
				alwaysInclude = append(alwaysInclude, provider)
				continue
			}

			// Determine priority for "one New per package" selection
			priority := s.funcPriority(pkg.Name, name)
			candidates = append(candidates, candidate{fn: fn, annotations: annotations, priority: priority})
		}
	}

	// Select providers, deduplicating by return type.
	// Sort candidates by priority (best first), then include each candidate
	// only if it provides types not already covered by a better candidate.
	// This handles:
	//   - Multi-return functions (redisx.New → UniversalClient + Locker)
	//   - Packages with multiple service constructors (mq: Queue, Router, Consumer, etc.)
	//   - Deduplication (NewLocker skipped when New already provides *Locker)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].priority < candidates[j].priority
	})

	var providers []*Provider
	providers = append(providers, alwaysInclude...)

	providedTypes := make(map[string]bool)
	// Mark types from always-included providers
	for _, p := range alwaysInclude {
		for _, ret := range p.Returns {
			providedTypes[ret.TypeStr] = true
		}
	}

	for _, c := range candidates {
		p := s.buildProvider(pkg, c.fn, c.annotations)
		if p == nil {
			continue
		}

		// Check if any return type is already provided
		overlap := false
		for _, ret := range p.Returns {
			if providedTypes[ret.TypeStr] {
				overlap = true
				break
			}
		}
		if overlap {
			continue
		}

		// Include this provider and mark its return types
		providers = append(providers, p)
		for _, ret := range p.Returns {
			providedTypes[ret.TypeStr] = true
		}
	}

	return providers
}

// funcPriority determines how well a function name matches the "primary New" convention.
func (s *Scanner) funcPriority(pkgName, funcName string) int {
	suffix := strings.TrimPrefix(funcName, "New")

	// "New" + exact PkgName (case-insensitive) → highest priority
	if strings.EqualFold(suffix, pkgName) {
		return 0
	}
	// Bare "New" → second
	if suffix == "" {
		return 1
	}
	// "NewService" → common pattern
	if suffix == "Service" {
		return 2
	}
	// Any other New* → lowest
	return 3
}

// isGroupPackage checks if this package path falls under a group definition.
func (s *Scanner) isGroupPackage(pkgPath string) bool {
	rel := strings.TrimPrefix(pkgPath, s.cfg.Module+"/")
	for _, group := range s.cfg.Groups {
		for _, gpath := range group.Paths {
			if strings.HasPrefix(rel, gpath) {
				return true
			}
		}
	}
	return false
}

// buildProvider creates a Provider from a function declaration.
func (s *Scanner) buildProvider(pkg *packages.Package, fn *ast.FuncDecl, annotations []Annotation) *Provider {
	obj := pkg.TypesInfo.Defs[fn.Name]
	if obj == nil {
		return nil
	}
	funcObj, ok := obj.(*types.Func)
	if !ok {
		return nil
	}
	sig := funcObj.Type().(*types.Signature)
	returns, hasError := s.extractReturns(sig)
	if len(returns) == 0 {
		return nil
	}
	params := s.extractParams(sig, annotations)

	return &Provider{
		FuncName:    fn.Name.Name,
		PkgPath:     pkg.PkgPath,
		PkgName:     pkg.Name,
		Params:      params,
		Returns:     returns,
		HasError:    hasError,
		IsInvoke:    HasAnnotation(annotations, AnnotInvoke),
		Annotations: annotations,
		Position:    s.fset.Position(fn.Pos()),
	}
}

// extractReturns parses return types, separating error from provided types.
func (s *Scanner) extractReturns(sig *types.Signature) ([]TypeRef, bool) {
	results := sig.Results()
	if results.Len() == 0 {
		return nil, false
	}

	var refs []TypeRef
	hasError := false

	for i := 0; i < results.Len(); i++ {
		t := results.At(i).Type()

		// Check if this is the error type (only valid as last return)
		if i == results.Len()-1 && isErrorType(t) {
			hasError = true
			continue
		}

		refs = append(refs, TypeRef{
			Type:    t,
			TypeStr: types.TypeString(t, nil),
			PkgPath: typePkgPath(t),
			IsIface: isInterface(t),
		})
	}

	return refs, hasError
}

// extractParams parses parameter types as dependencies.
func (s *Scanner) extractParams(sig *types.Signature, annotations []Annotation) []TypeRef {
	params := sig.Params()
	optionalTypes := GetAnnotationValues(annotations, AnnotOptional)

	var refs []TypeRef
	for i := 0; i < params.Len(); i++ {
		t := params.At(i).Type()
		typeStr := types.TypeString(t, nil)

		optional := false
		for _, opt := range optionalTypes {
			if strings.HasSuffix(typeStr, opt) {
				optional = true
				break
			}
		}

		refs = append(refs, TypeRef{
			Type:     t,
			TypeStr:  typeStr,
			PkgPath:  typePkgPath(t),
			IsIface:  isInterface(t),
			Optional: optional,
		})
	}
	return refs
}

// isErrorType checks if a type is the built-in error interface.
func isErrorType(t types.Type) bool {
	return types.Identical(t, types.Universe.Lookup("error").Type())
}

// isInterface checks if a type is an interface (not including error).
func isInterface(t types.Type) bool {
	// Dereference pointer
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	_, ok := t.Underlying().(*types.Interface)
	return ok && !isErrorType(t)
}

// typePkgPath extracts the package path from a type.
func typePkgPath(t types.Type) string {
	switch t := t.(type) {
	case *types.Named:
		if t.Obj().Pkg() != nil {
			return t.Obj().Pkg().Path()
		}
	case *types.Pointer:
		return typePkgPath(t.Elem())
	}
	return ""
}

// RelPath returns the relative package path within the module.
func (p *Provider) RelPath(module string) string {
	return strings.TrimPrefix(p.PkgPath, module+"/")
}

// FieldName generates a Container field name for this provider's return type.
// Uses the package short name + type name to produce unique, readable names.
func FieldName(typeStr string) string {
	s := typeStr
	s = strings.TrimPrefix(s, "*")

	// Split into package path and type name at the last dot
	dotIdx := strings.LastIndex(s, ".")
	if dotIdx < 0 {
		return exportName(s)
	}

	pkgPath := s[:dotIdx]
	typeName := s[dotIdx+1:]

	// Get short package name
	pkg := pkgPath
	if idx := strings.LastIndex(pkg, "/"); idx >= 0 {
		pkg = pkg[idx+1:]
	}
	// Handle versioned paths (v2, v9)
	if len(pkg) >= 2 && pkg[0] == 'v' && pkg[1] >= '0' && pkg[1] <= '9' {
		parts := strings.Split(pkgPath, "/")
		if len(parts) >= 2 {
			pkg = parts[len(parts)-2]
			if idx := strings.LastIndex(pkg, "-"); idx >= 0 {
				pkg = pkg[idx+1:]
			}
		}
	}

	// If type name already incorporates the package name, skip prefix
	// e.g., pkg="iam", name="IAM" → just "IAM"
	// e.g., pkg="redisx", name="Locker" → "RedisxLocker"
	// e.g., pkg="ent", name="Client" → "EntClient"
	if strings.EqualFold(pkg, typeName) {
		return exportName(typeName)
	}
	if len(typeName) > len(pkg) && strings.EqualFold(typeName[:len(pkg)], pkg) {
		return exportName(typeName)
	}
	return exportName(pkg) + exportName(typeName)
}

// exportName ensures first letter is uppercase.
func exportName(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ImportAlias returns the import alias needed for a package, or empty if default is fine.
func ImportAlias(pkgPath, pkgName string, used map[string]string) string {
	// Check if this package name is already used by a different path
	if existingPath, ok := used[pkgName]; ok && existingPath != pkgPath {
		// Need alias — use parent dir + pkg name
		parts := strings.Split(pkgPath, "/")
		if len(parts) >= 2 {
			parent := parts[len(parts)-2]
			alias := parent + pkgName
			// Check this alias isn't also taken
			if _, exists := used[alias]; !exists {
				return alias
			}
			// Fallback to more segments
			if len(parts) >= 3 {
				return parts[len(parts)-3] + parent + pkgName
			}
		}
		return pkgName + "2"
	}
	return ""
}
