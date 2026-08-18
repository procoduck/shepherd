// Package visual renders visual-builder graphs into deterministic Alloy.
package visual

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type GraphDocument struct { //nolint:revive
	Kind          string         `json:"kind"`
	SchemaVersion string         `json:"schema_version"`
	Nodes         []GraphNode    `json:"nodes"`
	Edges         []GraphEdge    `json:"edges"`
	Bindings      []GraphBinding `json:"bindings"`
	Viewport      Viewport       `json:"viewport"`
	Meta          Meta           `json:"meta"`
}
type GraphNode struct { //nolint:revive
	ID        string                 `json:"id"`
	Component string                 `json:"component"`
	Label     string                 `json:"label"`
	Position  Position               `json:"position"`
	Props     map[string]interface{} `json:"props"`
	Disabled  bool                   `json:"disabled"`
	Notes     string                 `json:"notes"`
}
type GraphEdge struct { //nolint:revive
	ID    string  `json:"id"`
	From  PortRef `json:"from"`
	To    PortRef `json:"to"`
	Order *int    `json:"order,omitempty"`
}
type GraphBinding struct { //nolint:revive
	Node string     `json:"node"`
	Prop string     `json:"prop"`
	Ref  BindingRef `json:"ref"`
}
type PortRef struct { //nolint:revive
	Node string `json:"node"`
	Port string `json:"port"`
}
type BindingRef struct { //nolint:revive
	Node   string `json:"node"`
	Export string `json:"export"`
	Expr   string `json:"expr"`
}
type Position struct { //nolint:revive
	X float64 `json:"x"`
	Y float64 `json:"y"`
}
type Viewport struct { //nolint:revive
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Zoom float64 `json:"zoom"`
}
type Meta struct { //nolint:revive
	CreatedWith string `json:"created_with"`
}

type SchemaPayload struct { //nolint:revive
	Meta       SchemaMeta                 `json:"_meta"`
	Components map[string]ComponentSchema `json:"components"`
}
type SchemaMeta struct { //nolint:revive
	AlloyVersion string `json:"alloy_version"`
}
type ComponentSchema struct { //nolint:revive
	Category   string            `json:"category"`
	Attributes []AttributeSchema `json:"attributes"`
	Blocks     []BlockSchema     `json:"blocks"`
	Inputs     []PortSchema      `json:"inputs"`
	Outputs    []PortSchema      `json:"outputs"`
}
type AttributeSchema struct { //nolint:revive
	Name string `json:"name"`
	Type string `json:"type"`
}
type PortSchema struct { //nolint:revive
	Prop        string `json:"prop"`
	Export      string `json:"export"`
	Type        string `json:"type"`
	Cardinality string `json:"cardinality"`
}
type BlockSchema struct { //nolint:revive
	Name       string            `json:"name"`
	Attributes []AttributeSchema `json:"attributes"`
	Blocks     []BlockSchema     `json:"blocks"`
}
type NodeRange struct { //nolint:revive
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
}
type RenderResult struct { //nolint:revive
	Content     string
	NodeMap     map[string]NodeRange `json:"node_map"`
	Diagnostics []RenderDiagnostic   `json:"diagnostics"`
}
type RenderDiagnostic struct { //nolint:revive
	Layer   string `json:"layer"`
	Code    string `json:"code"`
	NodeID  string `json:"node_id,omitempty"`
	NodeID2 string `json:"node_id2,omitempty"`
	Message string `json:"message"`
}

func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || unicode.IsDigit(rune(out[0])) {
		out = "_" + out
	}
	return out
}
func categoryOrder(s string) int {
	switch s {
	case "config":
		return 0
	case "sources":
		return 1
	case "transform":
		return 2
	case "destinations":
		return 3
	}
	return 4
}

// lineWriter wraps a strings.Builder and maintains a running line count.
type lineWriter struct {
	b     strings.Builder
	lines int
}

func (lw *lineWriter) writef(format string, args ...any) {
	lw.writes(fmt.Sprintf(format, args...))
}

func (lw *lineWriter) writes(s string) {
	lw.b.WriteString(s)
	lw.lines += strings.Count(s, "\n")
}

func (lw *lineWriter) currentLine() int { return lw.lines + 1 }

func Render(doc GraphDocument, schema SchemaPayload) RenderResult { //nolint:revive
	r := RenderResult{NodeMap: map[string]NodeRange{}}
	labels := map[string]string{}
	seen := map[string]string{}
	for _, n := range doc.Nodes {
		labels[n.ID] = sanitizeLabel(n.Label)
		if n.Disabled {
			continue
		}
		key := n.Component + "\x00" + labels[n.ID]
		if old, ok := seen[key]; ok {
			r.Diagnostics = []RenderDiagnostic{{Layer: "L1", Code: "label_collision", NodeID: old, NodeID2: n.ID, Message: fmt.Sprintf("nodes %s and %s have the same label", old, n.ID)}}
			return r
		}
		seen[key] = n.ID
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
		rest := []string{}
		for id := range active {
			found := false
			for _, x := range order {
				if x == id {
					found = true
				}
			}
			if !found {
				rest = append(rest, id)
			}
		}
		sort.Slice(rest, func(i, j int) bool { return less(rest[i], rest[j]) })
		order = append(order, rest...)
	}
	version := schema.Meta.AlloyVersion
	if version == "" {
		version = doc.SchemaVersion
	}
	var lw lineWriter
	lw.writef("// generated by shepherd visual builder — do not edit by hand (edits will be overwritten); graph revision %d, schema %s\n", len(doc.Nodes), version)
	// Disabled markers are intentionally retained, while disabled blocks and wires are omitted.
	for _, n := range doc.Nodes {
		if n.Disabled {
			lw.writef("// node %q disabled\n", n.Label)
		}
	}
	for _, id := range order {
		n := active[id]
		c := schema.Components[n.Component]
		if n.Component != "" {
			if _, known := schema.Components[n.Component]; !known {
				r.Diagnostics = append(r.Diagnostics, RenderDiagnostic{Layer: "L1", Code: "unknown_component", NodeID: id, Message: fmt.Sprintf("component %q is not in the schema for this graph", n.Component)})
			}
		}
		lw.writes("\n")
		start := lw.currentLine()
		lw.writef("%s %q {\n", n.Component, labels[id])
		for _, a := range c.Attributes {
			if val, ok := n.Props[a.Name]; ok {
				if a.Type == "secret" {
					if _, literal := val.(string); literal {
						r.Diagnostics = []RenderDiagnostic{{Layer: "L1", Code: "secret_by_value", NodeID: id, Message: "secret value supplied as a literal"}}
						return RenderResult{Diagnostics: r.Diagnostics, NodeMap: map[string]NodeRange{}}
					}
					continue
				}
				lw.writef("  %s = %s\n", a.Name, serialize(val, a.Type))
			}
		}
		for _, in := range c.Inputs {
			type wire struct {
				ref   string
				order int
			}
			wires := []wire{}
			for _, e := range doc.Edges {
				if e.To.Node == id && e.To.Port == in.Prop {
					if _, ok := active[e.From.Node]; ok {
						o := 0
						if e.Order != nil {
							o = *e.Order
						}
						wires = append(wires, wire{reference(e.From.Node, e.From.Port, active, labels), o})
					}
				}
			}
			sort.SliceStable(wires, func(i, j int) bool { return wires[i].order < wires[j].order })
			refs := make([]string, len(wires))
			for i, w := range wires {
				refs[i] = w.ref
			}
			if len(refs) > 0 {
				if in.Cardinality == "list" {
					lw.writef("  %s = [%s]\n", in.Prop, strings.Join(refs, ", "))
				} else {
					lw.writef("  %s = %s\n", in.Prop, refs[0])
				}
			}
		}
		for _, bind := range doc.Bindings {
			if bind.Node == id {
				expr := strings.TrimSpace(bind.Ref.Expr)
				if expr == "" {
					r.Diagnostics = append(r.Diagnostics, RenderDiagnostic{Layer: "L1", Code: "empty_binding_expr", NodeID: id, Message: fmt.Sprintf("binding for %q has an empty expression", bind.Prop)})
					continue
				}
				if bind.Prop == "" {
					r.Diagnostics = append(r.Diagnostics, RenderDiagnostic{Layer: "L1", Code: "empty_binding_prop", NodeID: id, Message: "binding has an empty property name"})
					continue
				}
				lw.writef("  %s = %s\n", bind.Prop, expr)
			}
		}
		lw.writes("}\n")
		end := lw.currentLine() - 1
		r.NodeMap[id] = NodeRange{StartLine: start, EndLine: end}
	}
	r.Content = lw.b.String()
	return r
}
func reference(id, port string, nodes map[string]GraphNode, labels map[string]string) string {
	return nodes[id].Component + "." + labels[id] + "." + port
}
func serialize(v interface{}, typ string) string {
	if typ == "duration" {
		return fmt.Sprintf("%q", fmt.Sprint(v))
	}
	switch x := v.(type) {
	case string:
		return strconv.Quote(x)
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case []interface{}:
		p := make([]string, len(x))
		for i, z := range x {
			p[i] = serialize(z, "string")
		}
		return "[" + strings.Join(p, ", ") + "]"
	case map[string]interface{}:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		p := make([]string, len(keys))
		for i, k := range keys {
			p[i] = k + " = " + serialize(x[k], "string")
		}
		return "{" + strings.Join(p, ", ") + "}"
	}
	return strconv.Quote(fmt.Sprint(v))
}
