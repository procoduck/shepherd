package visual

import (
	"fmt"
	"sort"
	"strings"
)

// LegacyRender is Render exactly as it stood before #391 — the per-push queue
// re-sort, the per-node binding scan, no edge de-duplication, no label or
// output cap — kept so render_bounds_test.go can prove the bounded renderer
// emits byte-identical content, node maps and diagnostics for every graph the
// old one accepted. Test-only: nothing in the product can reach it.
func LegacyRender(doc GraphDocument, schema SchemaPayload) RenderResult {
	return legacyRender(doc, schema)
}

func (rd *renderer) legacyResolveEdges(doc GraphDocument, active map[string]GraphNode, labels map[string]string) map[string]map[string][]wire {
	out := map[string]map[string][]wire{}
	add := func(node, port string, w wire) {
		if out[node] == nil {
			out[node] = map[string][]wire{}
		}
		out[node][port] = append(out[node][port], w)
	}
	for i, e := range doc.Edges {
		fromNode, fromActive := active[e.From.Node]
		toNode, toActive := active[e.To.Node]
		if !fromActive || !toActive {
			// Wires to disabled or absent nodes are dropped along with the
			// node itself; that is the documented meaning of "disabled".
			continue
		}
		fromComp := rd.schema.Components[fromNode.Component]
		toComp := rd.schema.Components[toNode.Component]
		toIn, fromOut := findInput(toComp, e.To.Port), findOutput(fromComp, e.From.Port)
		fromIn, toOut := findInput(fromComp, e.From.Port), findOutput(toComp, e.To.Port)

		order := 0
		if e.Order != nil {
			order = *e.Order
		}
		switch {
		case toIn != nil && inputRole(*toIn) == roleAccepts && fromOut != nil && outputRole(*fromOut) == roleProduces:
			add(e.To.Node, e.To.Port, wire{text: reference(e.From.Node, e.From.Port, active, labels), order: order, seq: i})
		case fromIn != nil && inputRole(*fromIn) == roleProduces && toOut != nil && outputRole(*toOut) == roleAccepts:
			add(e.From.Node, e.From.Port, wire{text: reference(e.To.Node, e.To.Port, active, labels), order: order, seq: i})
		default:
			rd.diags = append(rd.diags, RenderDiagnostic{
				Layer: "L1", Severity: sevError, Code: "edge_unresolved",
				NodeID: e.From.Node, NodeID2: e.To.Node,
				Message: fmt.Sprintf("edge %s: %s.%s → %s.%s does not connect an argument to a matching export; the reference was not emitted",
					e.ID, fromNode.Component, e.From.Port, toNode.Component, e.To.Port),
			})
		}
	}
	return out
}

func legacyRender(doc GraphDocument, schema SchemaPayload) RenderResult {
	rd := &renderer{schema: schema}
	r := RenderResult{NodeMap: map[string]NodeRange{}}
	labels := map[string]string{}
	seen := map[string]string{}
	collision := false
	for _, n := range doc.Nodes {
		labels[n.ID] = sanitizeLabel(n.Label)
		if n.Disabled {
			continue
		}
		key := n.Component + "\x00" + labels[n.ID]
		if old, ok := seen[key]; ok {
			collision = true
			rd.diags = append(rd.diags, RenderDiagnostic{
				Layer: "L1", Severity: sevError, Code: "label_collision", NodeID: old, NodeID2: n.ID,
				Message: fmt.Sprintf("nodes %s and %s have the same label", old, n.ID),
			})
			continue
		}
		seen[key] = n.ID
	}
	if collision {
		// A collision makes every reference ambiguous, so no content is safe
		// to emit; every colliding pair is reported, not just the first.
		r.Diagnostics = rd.diags
		return r
	}

	active := map[string]GraphNode{}
	indegree := map[string]int{}
	adj := map[string][]string{}
	for _, n := range doc.Nodes {
		if !n.Disabled {
			active[n.ID] = n
			indegree[n.ID] = 0
		}
	}
	for _, e := range doc.Edges {
		if _, ok := active[e.From.Node]; !ok {
			continue
		}
		if _, ok := active[e.To.Node]; !ok {
			continue
		}
		adj[e.From.Node] = append(adj[e.From.Node], e.To.Node)
		indegree[e.To.Node]++
	}
	less := func(a, b string) bool {
		na, nb := active[a], active[b]
		ca, cb := categoryOrder(schema.Components[na.Component].Category), categoryOrder(schema.Components[nb.Component].Category)
		if ca != cb {
			return ca < cb
		}
		if na.Component != nb.Component {
			return na.Component < nb.Component
		}
		return labels[a] < labels[b]
	}
	queue := []string{}
	for id, d := range indegree {
		if d == 0 {
			queue = append(queue, id)
		}
	}
	sort.Slice(queue, func(i, j int) bool { return less(queue[i], queue[j]) })
	order := []string{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		order = append(order, id)
		for _, to := range adj[id] {
			indegree[to]--
			if indegree[to] == 0 {
				queue = append(queue, to)
				sort.Slice(queue, func(i, j int) bool { return less(queue[i], queue[j]) })
			}
		}
	}
	if len(order) < len(active) {
		placed := map[string]bool{}
		for _, id := range order {
			placed[id] = true
		}
		rest := []string{}
		for id := range active {
			if !placed[id] {
				rest = append(rest, id)
			}
		}
		sort.Slice(rest, func(i, j int) bool { return less(rest[i], rest[j]) })
		names := make([]string, len(rest))
		for i, id := range rest {
			names[i] = active[id].Component + " " + quote(labels[id])
		}
		rd.diags = append(rd.diags, RenderDiagnostic{
			Layer: "L1", Severity: sevError, Code: "cycle", NodeID: rest[0],
			Message: fmt.Sprintf("graph has a cycle through %s; the emission order of those components is arbitrary", strings.Join(names, ", ")),
		})
		order = append(order, rest...)
	}

	wires := rd.legacyResolveEdges(doc, active, labels)

	version := schema.Meta.AlloyVersion
	if version == "" {
		version = doc.SchemaVersion
	}
	var lw lineWriter
	// The count is the graph's nodes, disabled ones included. Until #308 it
	// was labelled "graph revision", which it never was; nothing parses this
	// line (it is a comment), so text stored with the old label still loads.
	// renderTS.ts writes the same line.
	lw.writef("// generated by shepherd visual builder — do not edit by hand (edits will be overwritten); %s, schema %s\n", nodeCount(len(doc.Nodes)), version)
	// Disabled markers are intentionally retained, while disabled blocks and wires are omitted.
	for _, n := range doc.Nodes {
		if n.Disabled {
			lw.writef("// node %s disabled\n", quote(n.Label))
		}
	}
	for _, id := range order {
		n := active[id]
		c, known := schema.Components[n.Component]
		if n.Component != "" && !known {
			rd.diags = append(rd.diags, RenderDiagnostic{
				Layer: "L1", Severity: sevError, Code: "unknown_component", NodeID: id,
				Message: fmt.Sprintf("component %q is not in the schema for this graph", n.Component),
			})
		}
		lw.writes("\n")
		start := lw.currentLine()
		lw.writef("%s %s {\n", n.Component, quote(labels[id]))
		if known {
			rd.writeBody(&lw, "  ", id, c.Attributes, orderBlocks(c.Blocks, n.BlockOrder), n.Props,
				nodeRefs(c, wires[id]))
		} else {
			// Without a schema there is no type or block information, so props
			// are emitted by their JSON shape rather than dropped.
			rd.writeUntypedProps(&lw, "  ", id, n.Props)
		}
		for _, bind := range doc.Bindings {
			if bind.Node != id {
				continue
			}
			expr := strings.TrimSpace(bind.Ref.Expr)
			if expr == "" {
				rd.diag("empty_binding_expr", sevError, id, fmt.Sprintf("binding for %q has an empty expression", bind.Prop))
				continue
			}
			if bind.Prop == "" {
				rd.diag("empty_binding_prop", sevError, id, "binding has an empty property name")
				continue
			}
			lw.writef("  %s = %s\n", bind.Prop, expr)
		}
		lw.writes("}\n")
		end := lw.currentLine() - 1
		r.NodeMap[id] = NodeRange{StartLine: start, EndLine: end}
	}
	r.Content = lw.b.String()
	r.Diagnostics = rd.diags
	return r
}
