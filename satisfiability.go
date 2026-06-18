package main

import (
	"fmt"
	"go/types"
	"os"
	"strings"
)

// FilterSatisfiable drops providers whose parameters cannot be supplied by the
// provider set, via a fixpoint. This is what lets shared code (internal/, pkg/)
// stay completely annotation-free: a constructor that takes runtime data rather
// than injectable dependencies — e.g. registry.NewClient(baseURL, user, pass
// string) or applier.NewApplier(..., namespace string, ...) — has a parameter no
// provider returns, so it is silently not treated as a provider. No //autodi:ignore
// needed.
//
// A parameter is satisfiable if it is optional, or some live provider returns it
// (concrete), implements it (interface), or implements its element ([]interface,
// the AutoCollect fan-in). Everything else (string, int, unprovided named types)
// makes the constructor non-live; the fixpoint then drops anything that depended
// on it. A genuinely-needed-but-unsatisfiable type surfaces later as a clear
// "missing dependency" error from ValidateEntry, not as broken generated code.
func FilterSatisfiable(providers []*Provider, ifaceTypes map[string]*types.Interface, verbose bool) []*Provider {
	live := make(map[*Provider]bool, len(providers))
	for _, p := range providers {
		live[p] = true
	}

	for {
		providable := make(map[string]bool)
		liveList := make([]*Provider, 0, len(providers))
		for _, p := range providers {
			if !live[p] {
				continue
			}
			liveList = append(liveList, p)
			for _, ret := range p.Returns {
				providable[ret.TypeStr] = true
			}
		}

		changed := false
		for _, p := range liveList {
			if p.IsInvoke {
				continue
			}
			for _, param := range p.Params {
				if param.Optional {
					continue
				}
				if satisfiableParam(param, providable, liveList, ifaceTypes) {
					continue
				}
				live[p] = false
				changed = true
				if verbose {
					fmt.Fprintf(os.Stderr, "autodi: skip %s.%s (param %s not providable — treated as non-DI constructor)\n",
						p.PkgName, p.FuncName, toShortTypeName(param.TypeStr))
				}
				break
			}
		}
		if !changed {
			break
		}
	}

	result := make([]*Provider, 0, len(providers))
	for _, p := range providers {
		if live[p] {
			result = append(result, p)
		}
	}
	return result
}

func satisfiableParam(param TypeRef, providable map[string]bool, live []*Provider, ifaceTypes map[string]*types.Interface) bool {
	if providable[param.TypeStr] {
		return true
	}
	if iface := asInterfaceType(param.Type, param.TypeStr, ifaceTypes); iface != nil && implementedByAny(iface, live) {
		return true
	}
	if strings.HasPrefix(param.TypeStr, "[]") {
		elemStr := param.TypeStr[2:]
		if providable[elemStr] {
			return true
		}
		var elem types.Type
		if param.Type != nil {
			if sl, ok := param.Type.Underlying().(*types.Slice); ok {
				elem = sl.Elem()
			}
		}
		if iface := asInterfaceType(elem, elemStr, ifaceTypes); iface != nil && implementedByAny(iface, live) {
			return true
		}
	}
	return false
}

func asInterfaceType(t types.Type, typeStr string, ifaceTypes map[string]*types.Interface) *types.Interface {
	if t != nil {
		if iface, ok := t.Underlying().(*types.Interface); ok {
			return iface
		}
	}
	if iface, ok := ifaceTypes[typeStr]; ok {
		return iface
	}
	return nil
}

func implementedByAny(iface *types.Interface, providers []*Provider) bool {
	for _, p := range providers {
		for _, ret := range p.Returns {
			if ret.Type == nil {
				continue
			}
			if types.Implements(ret.Type, iface) {
				return true
			}
			if _, isPtr := ret.Type.(*types.Pointer); !isPtr {
				if types.Implements(types.NewPointer(ret.Type), iface) {
					return true
				}
			}
		}
	}
	return false
}
