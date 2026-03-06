package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// ── Color palette (mirrors Mermaid classDef palette) ─────────────────────────

const (
	sgColorLeaf      = "#27ae60"
	sgColorProvider  = "#2980b9"
	sgColorInvoke    = "#8e44ad"
	sgColorCommand   = "#4a6fa5"
	sgColorIface     = "#e67e22"
	sgColorDecorator = "#c0392b"
	sgColorEdgeDep   = "#6c757d" // dependency arrow
	sgColorEdgeImpl  = "#e67e22" // implements arrow (orange)
	sgColorEdgeSlice = "#5dade2" // slice/auto-collect arrow (blue)
)

// ── Graphology-compatible JSON types ─────────────────────────────────────────

type sigmaNode struct {
	Key        string         `json:"key"`
	Attributes map[string]any `json:"attributes"`
}

type sigmaEdge struct {
	Key        string         `json:"key"`
	Source     string         `json:"source"`
	Target     string         `json:"target"`
	Attributes map[string]any `json:"attributes"`
}

type sigmaGraph struct {
	Nodes []sigmaNode `json:"nodes"`
	Edges []sigmaEdge `json:"edges"`
}

// ── DI Dependency Graph ───────────────────────────────────────────────────────

// renderDIHTML generates a self-contained Sigma.js HTML file for the DI dependency graph.
// It reuses the same mermaidGen data logic (collectIfaceTypes, computeStats, etc.)
// so node colours and edge routing are identical to the Mermaid output.
func renderDIHTML(graph *Graph, commands []*DiscoveredCommand, cfg *Config) []byte {
	mg := &mermaidGen{
		graph:    graph,
		commands: commands,
		cfg:      cfg,
		ids:      make(map[string]string),
		usedIDs:  make(map[string]bool),
	}
	ifaceSet := mg.collectIfaceTypes()
	providerCounts, ifaceStats := mg.computeStats(ifaceSet)

	var nodes []sigmaNode
	var edges []sigmaEdge
	edgeIdx := 0

	addEdge := func(src, tgt, color, label string) {
		edges = append(edges, sigmaEdge{
			Key:    fmt.Sprintf("e%d", edgeIdx),
			Source: src,
			Target: tgt,
			Attributes: map[string]any{
				"color": color,
				"label": label,
				"size":  1.5,
			},
		})
		edgeIdx++
	}

	// ── Provider nodes ────────────────────────────────────────────────────────
	for _, p := range graph.Providers {
		id := mg.nodeID(p)
		depCount := providerCounts[id]

		color, nodeType := sgColorProvider, "provider"
		switch {
		case p.IsInvoke:
			color, nodeType = sgColorInvoke, "invoke"
		case mg.isDecorator(p):
			color, nodeType = sgColorDecorator, "decorator"
		case len(p.Params) == 0:
			color, nodeType = sgColorLeaf, "leaf"
		}

		size := clampInt(8+depCount*3, 8, 40)

		typeName := ""
		if !p.IsInvoke {
			for _, ret := range p.Returns {
				if ret.TypeStr != "error" {
					typeName = briefTypeName(ret.TypeStr)
					break
				}
			}
		}
		label := p.FuncName
		if typeName != "" {
			label = typeName + "\n" + p.FuncName
		}

		nodes = append(nodes, sigmaNode{
			Key: id,
			Attributes: map[string]any{
				"label":    label,
				"color":    color,
				"size":     size,
				"nodeType": nodeType,
				"pkg":      mermaidRelPkg(p.PkgPath, cfg.Module),
				"depCount": depCount,
			},
		})
	}

	// ── Interface nodes ───────────────────────────────────────────────────────
	for ifaceTypeStr := range ifaceSet {
		id := mg.ifaceNodeID(ifaceTypeStr)
		stat := ifaceStats[ifaceTypeStr]
		implCount, useCount := stat[0], stat[1]
		size := clampInt(10+implCount*2+useCount*2, 10, 40)

		nodes = append(nodes, sigmaNode{
			Key: id,
			Attributes: map[string]any{
				"label":     "«iface»\n" + ifaceShortName(ifaceTypeStr),
				"color":     sgColorIface,
				"size":      size,
				"nodeType":  "iface",
				"implCount": implCount,
				"useCount":  useCount,
			},
		})
	}

	// ── Command nodes ─────────────────────────────────────────────────────────
	for _, cmd := range commands {
		cmdID := "C_" + sanitizeMermaidID(cmd.Name)
		label := cmd.Name
		if len(cmd.Handlers) > 0 && len(cmd.Handlers) <= 4 {
			var ms []string
			for _, h := range cmd.Handlers {
				ms = append(ms, h.MethodName)
			}
			label = cmd.Name + "\n" + strings.Join(ms, " | ")
		}
		nodes = append(nodes, sigmaNode{
			Key: cmdID,
			Attributes: map[string]any{
				"label":    label,
				"color":    sgColorCommand,
				"size":     14,
				"nodeType": "command",
			},
		})
	}

	// ── Implements edges ──────────────────────────────────────────────────────
	renderedImpl := make(map[string]bool)
	for ifaceTypeStr := range ifaceSet {
		ifaceID := mg.ifaceNodeID(ifaceTypeStr)
		emitImpl := func(dep *Provider) {
			key := mg.nodeID(dep) + "|" + ifaceID
			if renderedImpl[key] {
				return
			}
			renderedImpl[key] = true
			addEdge(mg.nodeID(dep), ifaceID, sgColorEdgeImpl, "implements")
		}
		if concrete, ok := graph.Bindings[ifaceTypeStr]; ok {
			if dep := graph.ProviderMap[concrete]; dep != nil {
				emitImpl(dep)
			}
		}
		for _, p := range graph.AutoCollect(ifaceTypeStr) {
			emitImpl(p)
		}
		for groupName, gc := range cfg.Groups {
			if ifaceTypeStr == graph.resolveConfigType(gc.Interface) {
				for _, gp := range graph.Groups[groupName] {
					emitImpl(gp)
				}
			}
		}
	}

	// ── Dependency edges ──────────────────────────────────────────────────────
	addParamEdges := func(consumerID string, params []TypeRef) {
		for _, param := range params {
			if strings.HasPrefix(param.TypeStr, "[]") {
				elemType := param.TypeStr[2:]
				sliceLabel := "[]" + briefTypeName(elemType)
				if ifaceSet[elemType] {
					addEdge(mg.ifaceNodeID(elemType), consumerID, sgColorEdgeSlice, sliceLabel)
				} else if groupName := mg.matchGroupByElem(elemType); groupName != "" {
					for _, gp := range graph.Groups[groupName] {
						addEdge(mg.nodeID(gp), consumerID, sgColorEdgeSlice, sliceLabel)
					}
				} else {
					for _, ap := range graph.AutoCollect(elemType) {
						addEdge(mg.nodeID(ap), consumerID, sgColorEdgeSlice, sliceLabel)
					}
				}
				continue
			}

			resolved := graph.resolveType(param.TypeStr)
			dep := graph.ProviderMap[resolved]
			if dep == nil {
				dep = graph.ProviderMap[param.TypeStr]
			}
			if dep == nil {
				continue
			}
			if param.IsIface && ifaceSet[param.TypeStr] {
				addEdge(mg.ifaceNodeID(param.TypeStr), consumerID, sgColorEdgeDep, "")
			} else {
				addEdge(mg.nodeID(dep), consumerID, sgColorEdgeDep, "")
			}
		}
	}
	for _, p := range graph.Providers {
		addParamEdges(mg.nodeID(p), p.Params)
	}
	for _, cmd := range commands {
		addParamEdges("C_"+sanitizeMermaidID(cmd.Name), cmd.Params)
	}

	assignDIPositions(nodes, edges)

	data := sigmaGraph{Nodes: nodes, Edges: edges}
	jsonBytes, _ := json.Marshal(data)
	title := cfg.AppName + " — DI Graph"
	return buildSigmaHTML(title, diLegend(), string(jsonBytes), diTooltipFn())
}

// ── Package Diagram ───────────────────────────────────────────────────────────

// renderPkgHTML generates a self-contained Sigma.js HTML for the package/type diagram.
func renderPkgHTML(pkgInfos []*pdPkgInfo, rels []pdRelation, refByNode, implByNode map[string]int) []byte {
	var nodes []sigmaNode
	var edges []sigmaEdge
	edgeIdx := 0

	// Helper to get a pdPkgInfo reference for each pdType
	typeToInfo := make(map[*pdType]*pdPkgInfo)
	for _, pkg := range pkgInfos {
		for _, t := range pkg.Structs {
			typeToInfo[t] = pkg
		}
		for _, t := range pkg.Ifaces {
			typeToInfo[t] = pkg
		}
	}

	// ── Nodes ──────────────────────────────────────────────────────────────
	addedNodes := make(map[string]bool)
	addTypeNode := func(pkg *pdPkgInfo, t *pdType, nodeType string) {
		id := pkgTypeNodeID(pkg, t.Name)
		if addedNodes[id] {
			return // skip duplicate (safety net)
		}
		addedNodes[id] = true
		usedBy := refByNode[id]
		impl := implByNode[id]
		use := refByNode[id]

		var color string
		var size int
		switch nodeType {
		case "interface":
			color = sgColorIface
			size = clampInt(10+(impl+use)*2, 10, 40)
		case "decorator":
			color = sgColorDecorator
			size = clampInt(8+usedBy*2, 8, 35)
		default: // struct
			color = sgColorProvider
			size = clampInt(8+usedBy*2, 8, 35)
		}

		// Collect up to 6 methods for tooltip
		methods := make([]string, 0, 6)
		for i, m := range t.Methods {
			if i >= 6 {
				break
			}
			retStr := ""
			if len(m.Returns) > 0 {
				retStr = " " + strings.Join(m.Returns, ", ")
			}
			methods = append(methods, fmt.Sprintf("%s(%s)%s", m.Name, strings.Join(m.Params, ", "), retStr))
		}

		attrs := map[string]any{
			"label":    t.Name,
			"color":    color,
			"size":     size,
			"nodeType": nodeType,
			"pkg":      pkg.RelPath,
			"methods":  methods,
			"usedBy":   usedBy,
		}
		if nodeType == "interface" {
			attrs["implCount"] = impl
			attrs["useCount"] = use
		}
		nodes = append(nodes, sigmaNode{Key: id, Attributes: attrs})
	}

	for _, pkg := range pkgInfos {
		for _, iface := range pkg.Ifaces {
			addTypeNode(pkg, iface, "interface")
		}
		for _, st := range pkg.Structs {
			nt := "struct"
			if isDecoratorPkg(pkg, st) {
				nt = "decorator"
			}
			addTypeNode(pkg, st, nt)
		}
	}

	// ── Edges ──────────────────────────────────────────────────────────────
	edgeColors := map[string]string{
		"implements": sgColorEdgeImpl,
		"depends":    sgColorEdgeDep,
		"field":      "#95a5a6",
	}
	for _, r := range rels {
		color := edgeColors[r.Kind]
		if color == "" {
			color = sgColorEdgeDep
		}
		edges = append(edges, sigmaEdge{
			Key:    fmt.Sprintf("e%d", edgeIdx),
			Source: r.FromID,
			Target: r.ToID,
			Attributes: map[string]any{
				"color": color,
				"label": r.Kind,
				"size":  1.5,
			},
		})
		edgeIdx++
	}

	assignPkgPositions(nodes)

	data := sigmaGraph{Nodes: nodes, Edges: edges}
	jsonBytes, _ := json.Marshal(data)
	return buildSigmaHTML("Package Diagram", pkgLegend(), string(jsonBytes), pkgTooltipFn())
}

// pkgTypeNodeID builds a stable sigma node ID for a package type.
// Uses the full RelPath to avoid collisions between packages that share a suffix
// (e.g. "internal/ring" vs "pkg/ring" would both become "ring_Ring" with prefix stripping).
func pkgTypeNodeID(pkg *pdPkgInfo, typeName string) string {
	return sanitizeMermaidID(pkg.RelPath + "_" + typeName)
}

// isDecoratorPkg detects decorator pattern: struct implements an interface AND
// its New* constructor takes that same interface as a parameter.
// It is a lightweight replication of pkgDiagramGen.isDecorator used here
// since we don't have a pkgDiagramGen receiver in this context.
func isDecoratorPkg(pkg *pdPkgInfo, st *pdType) bool {
	// No named types available here — delegate decorator detection via method
	// presence heuristic (if the struct has no Named, skip).
	if st.Named == nil {
		return false
	}
	// Reuse the logic via a temporary pkgDiagramGen — but we don't have one here.
	// Instead, use a simpler approach: check if any New* func in the same pkg
	// returns this struct AND takes an interface as a param that the struct also
	// appears to implement (we can't do full types.Implements here without the
	// original pkgDiagramGen context).
	// For simplicity, return false; the full version is in pkgDiagramGen.isDecorator.
	return false
}

// ── HTML Assembly ─────────────────────────────────────────────────────────────

// buildSigmaHTML assembles the self-contained HTML string.
// graphJSON must be valid JSON; tooltipFn is a raw JS function body.
func buildSigmaHTML(title, legend, graphJSON, tooltipFn string) []byte {
	html := strings.NewReplacer(
		"{{TITLE}}", title,
		"{{LEGEND}}", legend,
		"{{GRAPH_JSON}}", graphJSON,
		"{{TOOLTIP_FN}}", tooltipFn,
	).Replace(sigmaHTMLTemplate)
	return []byte(html)
}

// ── Legend HTML ───────────────────────────────────────────────────────────────

func diLegend() string {
	items := []struct{ color, label string }{
		{sgColorLeaf, "Leaf provider"},
		{sgColorProvider, "Provider"},
		{sgColorInvoke, "Invoke"},
		{sgColorDecorator, "Decorator"},
		{sgColorIface, "Interface"},
		{sgColorCommand, "Command"},
	}
	return legendHTML(items)
}

func pkgLegend() string {
	items := []struct{ color, label string }{
		{sgColorProvider, "Struct"},
		{sgColorIface, "Interface"},
		{sgColorDecorator, "Decorator"},
		{sgColorEdgeImpl, "→ implements"},
		{sgColorEdgeDep, "→ depends"},
	}
	return legendHTML(items)
}

func legendHTML(items []struct{ color, label string }) string {
	var sb strings.Builder
	for _, it := range items {
		fmt.Fprintf(&sb,
			`<div class="li"><span class="dot" style="background:%s"></span>%s</div>`,
			it.color, it.label)
	}
	return sb.String()
}

// ── Tooltip JS functions ──────────────────────────────────────────────────────

func diTooltipFn() string {
	return `function buildTooltip(node, a) {
  const typeLabel = {leaf:'Leaf',provider:'Provider',invoke:'Invoke',decorator:'Decorator',iface:'Interface',command:'Command'};
  let h = '<div class="tt-title">' + a.label.replace(/\n/g,'<br>') + '</div>';
  if (a.pkg) h += '<div class="tt-sub">' + a.pkg + '</div>';
  h += '<span class="tt-badge" style="background:' + a.color + '">' + (typeLabel[a.nodeType]||a.nodeType) + '</span>';
  if (a.depCount > 0)   h += '<div class="tt-stat">→ used by <b>' + a.depCount + '</b></div>';
  if (a.implCount > 0 || a.useCount > 0)
    h += '<div class="tt-stat">' + a.implCount + ' impl · ' + a.useCount + ' use</div>';
  return h;
}`
}

func pkgTooltipFn() string {
	return `function buildTooltip(node, a) {
  const typeLabel = {struct:'Struct',interface:'Interface',decorator:'Decorator'};
  let h = '<div class="tt-title">' + a.label + '</div>';
  if (a.pkg) h += '<div class="tt-sub">' + a.pkg + '</div>';
  h += '<span class="tt-badge" style="background:' + a.color + '">' + (typeLabel[a.nodeType]||a.nodeType) + '</span>';
  if (a.nodeType === 'interface' && (a.implCount > 0 || a.useCount > 0))
    h += '<div class="tt-stat">' + a.implCount + ' impl · ' + a.useCount + ' use</div>';
  else if (a.usedBy > 0)
    h += '<div class="tt-stat">used by <b>' + a.usedBy + '</b></div>';
  if (a.methods && a.methods.length > 0) {
    h += '<div class="tt-divider"></div>';
    a.methods.forEach(m => { h += '<div class="tt-method">+' + m + '</div>'; });
  }
  return h;
}`
}

// ── HTML Template ─────────────────────────────────────────────────────────────

// clampInt returns v clamped to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// assignDIPositions computes hierarchical x/y positions for the DI dependency graph.
// Edges flow from dependency (source) to consumer (target).
// Leaf providers with no incoming edges are placed at the top (layer 0);
// each subsequent layer is placed further down.
func assignDIPositions(nodes []sigmaNode, edges []sigmaEdge) {
	if len(nodes) == 0 {
		return
	}

	nodeIdx := make(map[string]int, len(nodes))
	for i, n := range nodes {
		nodeIdx[n.Key] = i
	}

	// Build adjacency: inDegree and outgoing neighbours.
	outAdj := make(map[string][]string, len(nodes))
	inDegree := make(map[string]int, len(nodes))
	for _, n := range nodes {
		outAdj[n.Key] = nil
		inDegree[n.Key] = 0
	}
	for _, e := range edges {
		if _, ok := nodeIdx[e.Source]; !ok {
			continue
		}
		if _, ok := nodeIdx[e.Target]; !ok {
			continue
		}
		outAdj[e.Source] = append(outAdj[e.Source], e.Target)
		inDegree[e.Target]++
	}

	// Kahn's BFS with longest-path layer assignment.
	layer := make(map[string]int, len(nodes))
	queue := []string{}
	for _, n := range nodes {
		if inDegree[n.Key] == 0 {
			layer[n.Key] = 0
			queue = append(queue, n.Key)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range outAdj[cur] {
			if layer[cur]+1 > layer[next] {
				layer[next] = layer[cur] + 1
			}
			inDegree[next]--
			if inDegree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	// Nodes in cycles not yet assigned — place them one past the max layer.
	maxLayer := 0
	for _, l := range layer {
		if l > maxLayer {
			maxLayer = l
		}
	}
	for _, n := range nodes {
		if _, ok := layer[n.Key]; !ok {
			layer[n.Key] = maxLayer + 1
		}
	}

	// Group nodes by layer; sort within each layer for stable output.
	layerNodes := make(map[int][]string)
	for key, l := range layer {
		layerNodes[l] = append(layerNodes[l], key)
	}
	for l := range layerNodes {
		sort.Strings(layerNodes[l])
	}

	const layerH = 200.0
	const nodeSpacing = 220.0

	for l := 0; l <= maxLayer+1; l++ {
		keys := layerNodes[l]
		if len(keys) == 0 {
			continue
		}
		totalW := float64(len(keys)-1) * nodeSpacing
		for i, key := range keys {
			idx := nodeIdx[key]
			nodes[idx].Attributes["x"] = float64(i)*nodeSpacing - totalW/2
			nodes[idx].Attributes["y"] = float64(l) * layerH
		}
	}
}

// assignPkgPositions assigns x/y to package-diagram nodes by clustering them
// by package. Packages are arranged in a square grid; nodes within each
// package are placed in a compact 2-column sub-grid.
func assignPkgPositions(nodes []sigmaNode) {
	if len(nodes) == 0 {
		return
	}

	// Group node indices by package path.
	pkgOrder := []string{}
	pkgNodes := make(map[string][]int)
	for i, n := range nodes {
		pkg, _ := n.Attributes["pkg"].(string)
		if _, seen := pkgNodes[pkg]; !seen {
			pkgOrder = append(pkgOrder, pkg)
		}
		pkgNodes[pkg] = append(pkgNodes[pkg], i)
	}
	sort.Strings(pkgOrder)

	cols := int(math.Ceil(math.Sqrt(float64(len(pkgOrder)))))
	const pkgW = 340.0 // horizontal space per package cluster
	const pkgH = 280.0 // vertical space per package cluster
	const nodeH = 80.0 // vertical spacing within a cluster
	const nodeColW = 150.0

	for pi, pkg := range pkgOrder {
		col := pi % cols
		row := pi / cols
		baseX := float64(col) * pkgW
		baseY := float64(row) * pkgH

		for ni, idx := range pkgNodes[pkg] {
			c := ni % 2
			r := ni / 2
			nodes[idx].Attributes["x"] = baseX + float64(c)*nodeColW
			nodes[idx].Attributes["y"] = baseY + float64(r)*nodeH
		}
	}
}

const sigmaHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{TITLE}}</title>
<script src="https://cdn.jsdelivr.net/npm/graphology@0.25.4/dist/graphology.umd.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/sigma@2.4.0/build/sigma.min.js"></script>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:system-ui,-apple-system,sans-serif;background:#0f1117;color:#ddd;overflow:hidden}
#toolbar{position:fixed;top:0;left:0;right:0;height:48px;background:#1a1d27;border-bottom:1px solid #2a2d3a;z-index:10;display:flex;align-items:center;padding:0 16px;gap:12px}
#toolbar h1{font-size:14px;font-weight:600;white-space:nowrap}
#search{flex:1;max-width:280px;padding:5px 10px;background:#252836;border:1px solid #3a3d4a;border-radius:6px;color:#ddd;font-size:13px;outline:none}
#search::placeholder{color:#666}
#node-count{font-size:11px;color:#666;white-space:nowrap}
#hint{font-size:11px;color:#555;white-space:nowrap}
#sigma-container{position:absolute;top:48px;left:0;right:0;bottom:0}
#tooltip{position:fixed;background:#1c1f2eee;border:1px solid #3a3d4a;border-radius:8px;padding:10px 13px;font-size:12px;line-height:1.5;max-width:260px;z-index:20;pointer-events:none;display:none;box-shadow:0 4px 12px #0006}
.tt-title{font-weight:600;font-size:13px;margin-bottom:3px}
.tt-sub{color:#888;font-size:11px;margin-bottom:4px}
.tt-badge{display:inline-block;padding:1px 7px;border-radius:10px;font-size:10px;font-weight:600;color:#fff;margin-bottom:5px}
.tt-stat{color:#aaa;margin-top:2px}
.tt-divider{border-top:1px solid #2a2d3a;margin:6px 0}
.tt-method{color:#8ab;font-size:11px}
#legend{position:fixed;bottom:16px;right:16px;background:#1a1d27cc;border:1px solid #2a2d3a;border-radius:8px;padding:10px 13px;z-index:10;font-size:12px;backdrop-filter:blur(4px)}
.li{display:flex;align-items:center;gap:7px;line-height:2}
.dot{width:11px;height:11px;border-radius:50%;flex-shrink:0}
</style>
</head>
<body>
<div id="toolbar">
  <h1>{{TITLE}}</h1>
  <input id="search" type="search" placeholder="Search nodes…">
  <span id="node-count"></span>
  <span id="hint">click node to highlight · click background to reset</span>
</div>
<div id="sigma-container"></div>
<div id="tooltip"></div>
<div id="legend">{{LEGEND}}</div>
<script>
(function(){
'use strict';

const DATA = {{GRAPH_JSON}};

{{TOOLTIP_FN}}

// ── Build graphology graph ────────────────────────────────────────────────────
const graph = new graphology.Graph({multi: true, type: 'directed'});
DATA.nodes.forEach(n => graph.addNode(n.key, n.attributes));
DATA.edges.forEach(e => {
  try { graph.addEdge(e.source, e.target, e.attributes); } catch(_) {}
});

// Node x/y positions are pre-computed in Go (hierarchical for DI, package-clustered
// for the package diagram) and embedded in DATA.nodes[*].attributes.

// ── Sigma renderer ────────────────────────────────────────────────────────────
const container = document.getElementById('sigma-container');
const renderer = new Sigma(graph, container, {
  defaultEdgeType: 'arrow',
  renderEdgeLabels: true,
  labelFont: 'system-ui, sans-serif',
  labelSize: 12,
  labelWeight: '500',
  labelColor: {color: '#c8c8c8'},
  edgeLabelFont: 'system-ui, sans-serif',
  edgeLabelSize: 9,
  edgeLabelColor: {color: '#666'},
  minCameraRatio: 0.05,
  maxCameraRatio: 20,
  labelThreshold: 0.5,
});

// ── Node count ────────────────────────────────────────────────────────────────
document.getElementById('node-count').textContent =
  DATA.nodes.length + ' nodes · ' + DATA.edges.length + ' edges';

// ── Tooltip ───────────────────────────────────────────────────────────────────
const tooltip = document.getElementById('tooltip');
let mouseX = 0, mouseY = 0;
document.addEventListener('mousemove', evt => {
  mouseX = evt.clientX; mouseY = evt.clientY;
  if (tooltip.style.display !== 'none') {
    tooltip.style.left = (mouseX + 16) + 'px';
    tooltip.style.top  = Math.min(mouseY + 8, window.innerHeight - 20) + 'px';
  }
});
renderer.on('enterNode', ({node}) => {
  const attrs = graph.getNodeAttributes(node);
  tooltip.innerHTML = buildTooltip(node, attrs);
  tooltip.style.left = (mouseX + 16) + 'px';
  tooltip.style.top  = (mouseY + 8) + 'px';
  tooltip.style.display = 'block';
});
renderer.on('leaveNode', () => { tooltip.style.display = 'none'; });

// ── Click to highlight neighbours ─────────────────────────────────────────────
const origColors = {};
graph.forEachNode((n, attrs) => { origColors[n] = attrs.color; });
let selected = null;

renderer.on('clickNode', ({node}) => {
  if (selected === node) {
    selected = null;
    graph.forEachNode(n => graph.setNodeAttribute(n, 'color', origColors[n]));
  } else {
    selected = node;
    const nbrs = new Set(graph.neighbors(node));
    nbrs.add(node);
    graph.forEachNode(n => {
      graph.setNodeAttribute(n, 'color', nbrs.has(n) ? origColors[n] : '#222232');
    });
  }
  renderer.refresh();
});
renderer.on('clickStage', () => {
  if (selected) {
    selected = null;
    graph.forEachNode(n => graph.setNodeAttribute(n, 'color', origColors[n]));
    renderer.refresh();
  }
});

// ── Search ─────────────────────────────────────────────────────────────────────
document.getElementById('search').addEventListener('input', evt => {
  const q = evt.target.value.toLowerCase().trim();
  if (!q) {
    graph.forEachNode(n => graph.setNodeAttribute(n, 'color', origColors[n]));
  } else {
    graph.forEachNode((n, attrs) => {
      const match = attrs.label.toLowerCase().includes(q);
      graph.setNodeAttribute(n, 'color', match ? origColors[n] : '#222232');
    });
  }
  renderer.refresh();
});

})();
</script>
</body>
</html>
`
