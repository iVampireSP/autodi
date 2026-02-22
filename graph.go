package main

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
)

// Graph holds the resolved dependency graph.
type Graph struct {
	Providers   []*Provider
	ProviderMap map[string]*Provider   // typeStr → provider
	Bindings    map[string]string      // interface typeStr → concrete typeStr
	Groups      map[string][]*Provider // group name → providers
	TypeToField map[string]string      // typeStr → Container field name

	cfg           *Config
	shortToFull   map[string]string           // short type name → full type string (e.g., "iam.AuthN" → "github.com/.../iam.AuthN")
	pkgNameToPath map[string]string           // pkg short name → full pkg path (e.g., "iam" → "github.com/.../iam")
	ifaceTypes    map[string]*types.Interface // full typeStr → interface type from loaded packages
}

// BuildGraph constructs the dependency graph from discovered providers.
// ifaceTypes is an optional map of all exported interface types from loaded packages,
// used as a fallback by AutoCollect when interface types aren't found in provider signatures.
func BuildGraph(providers []*Provider, cfg *Config, pkgIndex map[string]string, ifaceTypes map[string]*types.Interface) (*Graph, []error) {
	g := &Graph{
		Providers:     providers,
		ProviderMap:   make(map[string]*Provider),
		Bindings:      make(map[string]string),
		Groups:        make(map[string][]*Provider),
		TypeToField:   make(map[string]string),
		cfg:           cfg,
		shortToFull:   make(map[string]string),
		pkgNameToPath: make(map[string]string),
		ifaceTypes:    ifaceTypes,
	}

	// Seed pkgNameToPath with the full package index from scanner
	for name, path := range pkgIndex {
		g.pkgNameToPath[name] = path
	}

	// Build short-to-full type name mapping from all discovered types
	g.buildTypeIndex(providers)

	var errs []error

	// Phase 1: Classify providers into groups
	for _, p := range providers {
		rel := p.RelPath(cfg.Module)
		for groupName, groupCfg := range cfg.Groups {
			for _, gpath := range groupCfg.Paths {
				if strings.HasPrefix(rel, gpath) {
					p.Groups = append(p.Groups, groupName)
				}
			}
		}
	}

	// Phase 2: Register each provider's return types in the provider map
	for _, p := range providers {
		if p.IsInvoke {
			continue // invoke-only providers don't go in the map
		}

		for _, ret := range p.Returns {
			typeStr := ret.TypeStr

			// Skip grouped providers from the singleton map — they're collected, not single-provider
			if len(p.Groups) > 0 {
				// But still add to group slices
				for _, g := range p.Groups {
					g2 := g
					_ = g2
				}
				continue
			}

			if existing, ok := g.ProviderMap[typeStr]; ok {
				errs = append(errs, fmt.Errorf(
					"类型 %s 有多个 provider:\n  1. %s.%s (%s)\n  2. %s.%s (%s)\n  提示: 使用 //autodi:ignore 标记其中一个",
					typeStr,
					existing.PkgName, existing.FuncName, existing.Position,
					p.PkgName, p.FuncName, p.Position,
				))
				continue
			}
			g.ProviderMap[typeStr] = p
			g.TypeToField[typeStr] = FieldName(typeStr)
		}
	}

	// Add grouped providers
	for _, p := range providers {
		for _, groupName := range p.Groups {
			g.Groups[groupName] = append(g.Groups[groupName], p)
		}
	}

	// Phase 3: Resolve interface bindings
	bindErrs := g.resolveBindings(providers)
	errs = append(errs, bindErrs...)

	if len(errs) > 0 {
		return nil, errs
	}
	return g, nil
}

// buildTypeIndex builds lookup maps from all discovered types.
func (g *Graph) buildTypeIndex(providers []*Provider) {
	for _, p := range providers {
		// Index package name → path
		g.pkgNameToPath[p.PkgName] = p.PkgPath

		// Index all return types
		for _, ret := range p.Returns {
			short := toShortTypeName(ret.TypeStr)
			if short != ret.TypeStr {
				g.shortToFull[short] = ret.TypeStr
			}
			if ret.PkgPath != "" {
				parts := strings.Split(ret.PkgPath, "/")
				g.pkgNameToPath[parts[len(parts)-1]] = ret.PkgPath
			}
		}

		// Index all param types
		for _, param := range p.Params {
			short := toShortTypeName(param.TypeStr)
			if short != param.TypeStr {
				g.shortToFull[short] = param.TypeStr
			}
			if param.PkgPath != "" {
				parts := strings.Split(param.PkgPath, "/")
				g.pkgNameToPath[parts[len(parts)-1]] = param.PkgPath
			}
		}
	}
}

// toShortTypeName converts a full type string to its short form.
// "*github.com/.../iam.IAM" → "*iam.IAM"
func toShortTypeName(typeStr string) string {
	prefix := ""
	s := typeStr
	if strings.HasPrefix(s, "*") {
		prefix = "*"
		s = s[1:]
	}

	dotIdx := strings.LastIndex(s, ".")
	if dotIdx < 0 {
		return typeStr
	}

	pkgPath := s[:dotIdx]
	typeName := s[dotIdx+1:]

	parts := strings.Split(pkgPath, "/")
	pkgName := parts[len(parts)-1]

	// Handle versioned paths (v2, v9)
	if len(pkgName) >= 2 && pkgName[0] == 'v' && pkgName[1] >= '0' && pkgName[1] <= '9' && len(parts) >= 2 {
		candidate := parts[len(parts)-2]
		if idx := strings.LastIndex(candidate, "-"); idx >= 0 {
			pkgName = candidate[idx+1:]
		} else {
			pkgName = candidate
		}
	}

	return prefix + pkgName + "." + typeName
}

// resolveConfigType resolves a short config type name to its full type string.
// e.g., "*iam.IAM" → "*github.com/LeaflowNET/cloud/internal/services/iam.IAM"
func (g *Graph) resolveConfigType(shortName string) string {
	// Already a full path?
	if strings.Contains(shortName, "/") {
		return shortName
	}

	// Try direct lookup
	if full, ok := g.shortToFull[shortName]; ok {
		return full
	}

	// Try resolving via package name
	prefix := ""
	s := shortName
	if strings.HasPrefix(s, "*") {
		prefix = "*"
		s = s[1:]
	}
	dotIdx := strings.Index(s, ".")
	if dotIdx > 0 {
		pkgName := s[:dotIdx]
		typeName := s[dotIdx+1:]
		if pkgPath, ok := g.pkgNameToPath[pkgName]; ok {
			return prefix + pkgPath + "." + typeName
		}

		// Heuristic: infer package path from group paths in config.
		// e.g., group path "internal/apis/user/controllers" + interface "apis.Controller"
		// → "apis" is a parent segment → full path is module/internal/apis
		for _, group := range g.cfg.Groups {
			for _, gpath := range group.Paths {
				parts := strings.Split(gpath, "/")
				for i, part := range parts {
					if part == pkgName {
						fullPkg := g.cfg.Module + "/" + strings.Join(parts[:i+1], "/")
						g.pkgNameToPath[pkgName] = fullPkg // cache for future lookups
						return prefix + fullPkg + "." + typeName
					}
				}
			}
		}
	}

	return shortName // not found, keep as-is
}

// resolveBindings sets up interface → concrete type mappings.
func (g *Graph) resolveBindings(providers []*Provider) []error {
	var errs []error

	// 1. Explicit bindings from config (resolve short names to full type strings)
	for concreteShort, ifaces := range g.cfg.Bindings {
		concreteFull := g.resolveConfigType(concreteShort)
		for _, ifaceShort := range ifaces {
			ifaceFull := g.resolveConfigType(ifaceShort)
			if _, ok := g.Bindings[ifaceFull]; ok {
				errs = append(errs, fmt.Errorf("接口 %s 有重复绑定配置", ifaceFull))
				continue
			}
			g.Bindings[ifaceFull] = concreteFull
			// Register in provider map so it can be looked up
			if provider, ok := g.ProviderMap[concreteFull]; ok {
				g.ProviderMap[ifaceFull] = provider
				g.TypeToField[ifaceFull] = FieldName(ifaceFull)
			}
		}
	}

	// 2. Explicit bindings from annotations
	for _, p := range providers {
		bindTargets := GetAnnotationValues(p.Annotations, AnnotBind)
		for _, target := range bindTargets {
			if _, ok := g.Bindings[target]; ok {
				continue // already configured via yaml
			}
			if len(p.Returns) > 0 {
				concreteStr := p.Returns[0].TypeStr
				g.Bindings[target] = concreteStr
				g.ProviderMap[target] = p
			}
		}
	}

	// 3. Auto-detect: for each param that is an interface type, find a concrete provider
	// that implements it (if not already bound)
	g.autoDetectBindings(providers)

	return errs
}

// autoDetectBindings automatically binds interfaces to concrete types.
func (g *Graph) autoDetectBindings(providers []*Provider) {
	// Collect all interface types needed as parameters
	neededIfaces := make(map[string]types.Type) // typeStr → type
	for _, p := range providers {
		for _, param := range p.Params {
			if param.IsIface {
				if _, bound := g.Bindings[param.TypeStr]; !bound {
					if _, provided := g.ProviderMap[param.TypeStr]; !provided {
						neededIfaces[param.TypeStr] = param.Type
					}
				}
			}
		}
	}

	// For each needed interface, find concrete providers that implement it
	for ifaceStr, ifaceType := range neededIfaces {
		ifaceUnderlying, ok := ifaceType.Underlying().(*types.Interface)
		if !ok {
			continue
		}

		var candidates []*Provider
		var candidateTypes []string
		for typeStr, provider := range g.ProviderMap {
			for _, ret := range provider.Returns {
				if types.Implements(ret.Type, ifaceUnderlying) ||
					(isPointer(ret.Type) && types.Implements(ret.Type, ifaceUnderlying)) {
					candidates = append(candidates, provider)
					candidateTypes = append(candidateTypes, typeStr)
					break
				}
			}
		}

		if len(candidates) == 1 {
			g.Bindings[ifaceStr] = candidateTypes[0]
			g.ProviderMap[ifaceStr] = candidates[0]
		}
		// Multiple candidates or zero: leave unresolved, will be caught as missing dep
	}
}

// BindCommandInterfaces resolves interface bindings for command parameters
// that weren't covered by provider-only scanning.
// Since command types come from a different packages.Load universe,
// we find the matching interface type from our provider universe by TypeStr.
func (g *Graph) BindCommandInterfaces(commands []*DiscoveredCommand) {
	// Build lookup: typeStr → types.Type from provider universe
	providerTypes := make(map[string]types.Type)
	for _, p := range g.Providers {
		for _, param := range p.Params {
			providerTypes[param.TypeStr] = param.Type
		}
		for _, ret := range p.Returns {
			providerTypes[ret.TypeStr] = ret.Type
		}
	}

	for _, cmd := range commands {
		for _, param := range cmd.Params {
			if !param.IsIface {
				continue
			}
			if _, bound := g.Bindings[param.TypeStr]; bound {
				continue
			}
			// Find the same interface type from the provider universe
			provType, ok := providerTypes[param.TypeStr]
			if !ok {
				continue
			}
			ifaceUnderlying, ok := provType.Underlying().(*types.Interface)
			if !ok {
				continue
			}
			var candidateTypes []string
			for _, provider := range g.Providers {
				for _, ret := range provider.Returns {
					if types.Implements(ret.Type, ifaceUnderlying) {
						candidateTypes = append(candidateTypes, ret.TypeStr)
						break
					}
				}
			}
			if len(candidateTypes) == 1 {
				g.Bindings[param.TypeStr] = candidateTypes[0]
				if p, ok := g.ProviderMap[candidateTypes[0]]; ok {
					g.ProviderMap[param.TypeStr] = p
				}
			}
		}
	}
}

func isPointer(t types.Type) bool {
	_, ok := t.(*types.Pointer)
	return ok
}

// AllSingletonProviders returns all non-group, non-invoke providers in dependency order.
// Used to generate the Container struct with the full set of fields.
func (g *Graph) AllSingletonProviders() ([]*Provider, error) {
	var targets []string
	for typeStr := range g.ProviderMap {
		targets = append(targets, typeStr)
	}
	sort.Strings(targets)
	return g.TopologicalSort(targets)
}

// EntryProviders returns the singleton providers needed for an entry point, in dependency order.
// fieldNames are Container field names accessed by the entry point code (from AST analysis).
func (g *Graph) EntryProviders(fieldNames []string) ([]*Provider, error) {
	// Build reverse map: fieldName → typeStr
	fieldToType := make(map[string]string)
	for typeStr, fieldName := range g.TypeToField {
		fieldToType[fieldName] = typeStr
	}

	needed := make(map[string]bool)

	for _, fieldName := range fieldNames {
		// Check if it's a group field
		groupName := g.fieldNameToGroup(fieldName)
		if groupName != "" {
			// Include all group providers' dependencies
			for _, p := range g.Groups[groupName] {
				for _, param := range p.Params {
					needed[param.TypeStr] = true
				}
			}
			continue
		}

		// Singleton field — find its type and include it
		if typeStr, ok := fieldToType[fieldName]; ok {
			needed[typeStr] = true
		}
	}

	// Transitive expansion
	expanded := make(map[string]bool)
	var expand func(string)
	expand = func(typeStr string) {
		resolved := g.resolveType(typeStr)
		if expanded[resolved] {
			return
		}
		expanded[resolved] = true

		provider := g.ProviderMap[resolved]
		if provider == nil {
			return
		}
		for _, param := range provider.Params {
			expand(param.TypeStr)
		}
	}

	for t := range needed {
		expand(t)
	}

	// Include invoke providers whose dependencies are all satisfied
	for _, p := range g.Providers {
		if !p.IsInvoke {
			continue
		}
		allSatisfied := true
		for _, param := range p.Params {
			resolved := g.resolveType(param.TypeStr)
			if !expanded[resolved] {
				allSatisfied = false
				break
			}
		}
		if allSatisfied {
			for _, ret := range p.Returns {
				expanded[ret.TypeStr] = true
			}
		}
	}

	// Topological sort
	var targets []string
	for t := range expanded {
		targets = append(targets, t)
	}
	sort.Strings(targets)

	return g.TopologicalSort(targets)
}

// ValidateEntry checks that all providers for an entry have their dependencies satisfied.
func (g *Graph) ValidateEntry(name string, providers []*Provider) []error {
	provided := make(map[string]bool)
	for _, p := range providers {
		for _, ret := range p.Returns {
			provided[ret.TypeStr] = true
		}
	}
	for iface, concrete := range g.Bindings {
		if provided[concrete] {
			provided[iface] = true
		}
	}

	var errs []error
	for _, p := range providers {
		for _, param := range p.Params {
			if param.Optional {
				continue
			}
			resolved := g.resolveType(param.TypeStr)
			if !provided[resolved] {
				// Skip []Interface params that can be auto-collected
				if strings.HasPrefix(param.TypeStr, "[]") {
					elemType := param.TypeStr[2:]
					if autoProviders := g.AutoCollect(elemType); len(autoProviders) > 0 {
						continue
					}
				}
				errs = append(errs, fmt.Errorf(
					"entry %q: %s.%s 缺少依赖 %s",
					name, p.PkgName, p.FuncName, toShortTypeName(param.TypeStr),
				))
			}
		}
	}
	return errs
}

// fieldNameToGroup returns the group name for a Container field name, or "" if not a group.
func (g *Graph) fieldNameToGroup(fieldName string) string {
	for name := range g.Groups {
		if GroupFieldName(name) == fieldName {
			return name
		}
	}
	return ""
}

// GroupFieldName converts a group config name to a Container field name.
// "admin_controllers" → "AdminControllers"
// "listeners" → "Listeners"
func GroupFieldName(name string) string {
	parts := strings.Split(name, "_")
	var result string
	for _, p := range parts {
		result += exportName(p)
	}
	return result
}

// VerifyAcyclic checks for circular dependencies using DFS with trail tracking.
func (g *Graph) VerifyAcyclic() []error {
	visited := make(map[string]bool)
	var errs []error

	for typeStr := range g.ProviderMap {
		if visited[typeStr] {
			continue
		}

		// DFS with trail
		type frame struct {
			typeStr string
			trail   []string
		}
		stack := []frame{{typeStr: typeStr, trail: []string{typeStr}}}

		for len(stack) > 0 {
			curr := stack[len(stack)-1]
			stack = stack[:len(stack)-1]

			if visited[curr.typeStr] {
				continue
			}

			provider := g.ProviderMap[curr.typeStr]
			if provider == nil {
				continue
			}

			for _, param := range provider.Params {
				depType := g.resolveType(param.TypeStr)

				// Check for cycle
				for i, t := range curr.trail {
					if t == depType {
						// Format cycle
						cycle := append(curr.trail[i:], depType)
						errs = append(errs, fmt.Errorf(
							"检测到循环依赖:\n  %s\n涉及的 provider:\n%s",
							strings.Join(cycle, " → "),
							g.formatCycleProviders(cycle),
						))
						break
					}
				}

				if _, ok := g.ProviderMap[depType]; ok && !visited[depType] {
					newTrail := make([]string, len(curr.trail))
					copy(newTrail, curr.trail)
					newTrail = append(newTrail, depType)
					stack = append(stack, frame{typeStr: depType, trail: newTrail})
				}
			}

			visited[curr.typeStr] = true
		}
	}

	return errs
}

// resolveType follows interface bindings to find the concrete type.
func (g *Graph) resolveType(typeStr string) string {
	if concrete, ok := g.Bindings[typeStr]; ok {
		return concrete
	}
	return typeStr
}

// formatCycleProviders formats providers involved in a cycle for error output.
func (g *Graph) formatCycleProviders(cycle []string) string {
	var lines []string
	seen := make(map[string]bool)
	for _, typeStr := range cycle {
		if seen[typeStr] {
			continue
		}
		seen[typeStr] = true
		if p, ok := g.ProviderMap[typeStr]; ok {
			lines = append(lines, fmt.Sprintf("  %s.%s (%s)", p.PkgName, p.FuncName, p.Position))
		}
	}
	return strings.Join(lines, "\n")
}

// TopologicalSort returns providers in dependency order for the given target types.
func (g *Graph) TopologicalSort(targetTypes []string) ([]*Provider, error) {
	return g.TopologicalSortWithExtraEdges(targetTypes, nil)
}

// TopologicalSortWithExtraEdges sorts providers with additional synthetic dependency edges.
// extraEdges maps a provider's return type to extra dependency type strings that must be
// visited before it. This is used for deep auto-collected slice parameters whose
// item-provider dependencies must precede the consuming provider.
func (g *Graph) TopologicalSortWithExtraEdges(targetTypes []string, extraEdges map[string][]string) ([]*Provider, error) {
	visited := make(map[string]bool)
	var order []*Provider
	visiting := make(map[string]bool) // for cycle detection during sort

	var visit func(typeStr string) error
	visit = func(typeStr string) error {
		resolved := g.resolveType(typeStr)
		if visited[resolved] {
			return nil
		}
		if visiting[resolved] {
			return fmt.Errorf("unexpected cycle at %s", resolved)
		}
		visiting[resolved] = true

		provider := g.ProviderMap[resolved]
		if provider == nil {
			// Not in provider map — might be from a group or external
			visited[resolved] = true
			return nil
		}

		// Visit dependencies first
		for _, param := range provider.Params {
			depType := g.resolveType(param.TypeStr)
			if err := visit(depType); err != nil {
				return err
			}
		}

		// Visit extra edges (synthetic dependencies from auto-collected slice items)
		if extraEdges != nil {
			for _, ret := range provider.Returns {
				if extras, ok := extraEdges[ret.TypeStr]; ok {
					for _, extra := range extras {
						if err := visit(extra); err != nil {
							return err
						}
					}
				}
			}
		}

		visited[resolved] = true
		delete(visiting, resolved)

		// Only add if not already added (multi-return providers added once)
		if !isProviderInList(provider, order) {
			order = append(order, provider)
		}

		// Mark all return types as visited
		for _, ret := range provider.Returns {
			visited[ret.TypeStr] = true
		}

		return nil
	}

	for _, target := range targetTypes {
		if err := visit(target); err != nil {
			return nil, err
		}
	}

	return order, nil
}

// ProvidersForTypes returns singleton providers needed for the given type strings, in dependency order.
// Used by the new codegen to trace transitive dependencies from NewCommand parameter types.
func (g *Graph) ProvidersForTypes(typeStrs []string) ([]*Provider, error) {
	// Transitive expansion
	expanded := make(map[string]bool)
	var expand func(string)
	expand = func(typeStr string) {
		resolved := g.resolveType(typeStr)
		if expanded[resolved] {
			return
		}
		expanded[resolved] = true

		provider := g.ProviderMap[resolved]
		if provider == nil {
			return
		}
		for _, param := range provider.Params {
			expand(param.TypeStr)
		}
	}

	for _, t := range typeStrs {
		expand(t)
	}

	// Include invoke providers whose dependencies are all satisfied
	for _, p := range g.Providers {
		if !p.IsInvoke {
			continue
		}
		allSatisfied := true
		for _, param := range p.Params {
			resolved := g.resolveType(param.TypeStr)
			if !expanded[resolved] {
				allSatisfied = false
				break
			}
		}
		if allSatisfied {
			for _, ret := range p.Returns {
				expanded[ret.TypeStr] = true
			}
		}
	}

	// Topological sort
	var targets []string
	for t := range expanded {
		targets = append(targets, t)
	}
	sort.Strings(targets)

	return g.TopologicalSort(targets)
}

// ProvidersForTypesWithExtraEdges is like ProvidersForTypes but accepts extra synthetic
// dependency edges for the topological sort.
func (g *Graph) ProvidersForTypesWithExtraEdges(typeStrs []string, extraEdges map[string][]string) ([]*Provider, error) {
	expanded := make(map[string]bool)
	var expand func(string)
	expand = func(typeStr string) {
		resolved := g.resolveType(typeStr)
		if expanded[resolved] {
			return
		}
		expanded[resolved] = true

		provider := g.ProviderMap[resolved]
		if provider == nil {
			return
		}
		for _, param := range provider.Params {
			expand(param.TypeStr)
		}
	}

	for _, t := range typeStrs {
		expand(t)
	}

	for _, p := range g.Providers {
		if !p.IsInvoke {
			continue
		}
		allSatisfied := true
		for _, param := range p.Params {
			resolved := g.resolveType(param.TypeStr)
			if !expanded[resolved] {
				allSatisfied = false
				break
			}
		}
		if allSatisfied {
			for _, ret := range p.Returns {
				expanded[ret.TypeStr] = true
			}
		}
	}

	var targets []string
	for t := range expanded {
		targets = append(targets, t)
	}
	sort.Strings(targets)

	return g.TopologicalSortWithExtraEdges(targets, extraEdges)
}

// AutoCollect scans all providers and returns those whose return type implements
// the given interface type string. Used for automatic slice injection when no
// explicit group is configured.
func (g *Graph) AutoCollect(elemTypeStr string) []*Provider {
	// Find the interface type from known types
	ifaceType := g.findIfaceType(elemTypeStr)
	if ifaceType == nil {
		return nil
	}

	var matches []*Provider
	for _, p := range g.Providers {
		if p.IsInvoke {
			continue
		}
		for _, ret := range p.Returns {
			// Check both T and *T implementing the interface
			if types.Implements(ret.Type, ifaceType) {
				matches = append(matches, p)
				break
			}
			if ptr, ok := ret.Type.(*types.Pointer); ok {
				if types.Implements(ptr, ifaceType) {
					matches = append(matches, p)
					break
				}
			} else {
				// ret.Type is not a pointer, check if *ret.Type implements
				ptrType := types.NewPointer(ret.Type)
				if types.Implements(ptrType, ifaceType) {
					matches = append(matches, p)
					break
				}
			}
		}
	}

	// Sort by package path for deterministic output
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].PkgPath < matches[j].PkgPath
	})
	return matches
}

// findIfaceType finds the *types.Interface underlying type for a given type string.
func (g *Graph) findIfaceType(typeStr string) *types.Interface {
	// Search all providers' params and returns for a matching interface type
	for _, p := range g.Providers {
		for _, param := range p.Params {
			if param.TypeStr == typeStr && param.IsIface {
				if iface, ok := param.Type.Underlying().(*types.Interface); ok {
					return iface
				}
			}
			// Check slice element: if param is []Interface, extract the element type
			if strings.HasPrefix(param.TypeStr, "[]") && param.TypeStr[2:] == typeStr {
				// The param.Type is a *types.Slice, get elem
				if sliceType, ok := param.Type.Underlying().(*types.Slice); ok {
					if iface, ok := sliceType.Elem().Underlying().(*types.Interface); ok {
						return iface
					}
				}
			}
		}
		for _, ret := range p.Returns {
			if ret.TypeStr == typeStr && ret.IsIface {
				if iface, ok := ret.Type.Underlying().(*types.Interface); ok {
					return iface
				}
			}
		}
	}

	// Fallback: look up from package-level interface types extracted by the scanner.
	// This covers interfaces that are defined in loaded packages but not directly
	// referenced in any provider's params/returns (e.g., jobs.Handler, mq.Listener).
	if g.ifaceTypes != nil {
		if iface, ok := g.ifaceTypes[typeStr]; ok {
			return iface
		}
	}

	return nil
}

func isProviderInList(p *Provider, list []*Provider) bool {
	for _, existing := range list {
		if existing.PkgPath == p.PkgPath && existing.FuncName == p.FuncName {
			return true
		}
	}
	return false
}

func sanitizeName(s string) string {
	s = strings.ReplaceAll(s, ".", "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "*", "")
	return s
}
