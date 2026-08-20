// Package visual renders visual-builder graphs into deterministic Alloy.
package visual

import (
	"fmt"
	"math"
	"regexp"
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

// PortSchema is one wireable port. Role is the D1 dataflow role: "produces"
// means data leaves the node here (rendered on the node's right), "accepts"
// means data enters here (rendered on the left). It is independent of whether
// the port is an argument (Inputs) or an export (Outputs): a receiver export
// such as prometheus.remote_write's `receiver` is an export with role
// "accepts", and a forward_to argument is an argument with role "produces".
//
// Path is the location of the port inside the component body: ["forward_to"]
// for a top-level attribute, ["output", "metrics"] for an attribute nested in
// an `output` block. When empty the port name is used as a single-segment path
// (which is what pre-role schema payloads expect).
type PortSchema struct {
	Prop        string   `json:"prop"`
	Export      string   `json:"export"`
	Type        string   `json:"type"`
	Cardinality string   `json:"cardinality"`
	Role        string   `json:"role"`
	Path        []string `json:"path"`
}

type BlockSchema struct { //nolint:revive
	Name       string            `json:"name"`
	Repeatable bool              `json:"repeatable"`
	Required   bool              `json:"required"`
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

// RenderDiagnostic is one problem found while rendering. Severity is "error"
// for a condition that makes the generated config wrong ("warning" for one
// that merely loses information); callers that gate on diagnostics should gate
// on severity == "error".
type RenderDiagnostic struct {
	Layer    string `json:"layer"`
	Severity string `json:"severity,omitempty"`
	Code     string `json:"code"`
	NodeID   string `json:"node_id,omitempty"`
	NodeID2  string `json:"node_id2,omitempty"`
	Message  string `json:"message"`
}

const (
	sevError   = "error"
	sevWarning = "warning"

	roleProduces = "produces"
	roleAccepts  = "accepts"

	// exprKey marks a props value as a raw Alloy expression rather than a
	// literal: {"$expr": "env(\"TOKEN\")"} renders as env("TOKEN"). It is the
	// only way to reach a secret-typed attribute nested inside a block, and
	// the shape ParseAlloy uses to round-trip non-literal expressions.
	exprKey = "$expr"
)

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

// SanitizeLabel exposes the label mapping the renderer applies when it emits a
// component's block header. Callers that must reconstruct the reference token a
// node will be addressed by — the S3 simulation transform's containment
// self-check (§6.4) and its tests — have to derive it from the same function
// the renderer uses, or the check silently stops matching the emitted text.
func SanitizeLabel(s string) string { return sanitizeLabel(s) }

// quote renders an Alloy string literal. It is deliberately not
// strconv.Quote: the escape set below is the one the TypeScript renderer can
// reproduce exactly, which is what keeps the two implementations byte-equal.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
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

// renderer carries the per-render state that every emission step appends to.
type renderer struct {
	schema SchemaPayload
	diags  []RenderDiagnostic
}

func (rd *renderer) diag(code, severity, nodeID, msg string) {
	rd.diags = append(rd.diags, RenderDiagnostic{Layer: "L1", Severity: severity, Code: code, NodeID: nodeID, Message: msg})
}

// portPath is where a port lives inside the component body. Payloads that
// predate the path field fall back to the port's own name.
func portPath(p PortSchema) []string {
	if len(p.Path) > 0 {
		return p.Path
	}
	name := p.Prop
	if name == "" {
		name = p.Export
	}
	return []string{name}
}

func inputRole(p PortSchema) string {
	if p.Role == "" {
		return roleAccepts
	}
	return p.Role
}

func outputRole(p PortSchema) string {
	if p.Role == "" {
		return roleProduces
	}
	return p.Role
}

func findInput(c ComponentSchema, name string) *PortSchema {
	for i := range c.Inputs {
		if c.Inputs[i].Prop == name {
			return &c.Inputs[i]
		}
	}
	return nil
}

func findOutput(c ComponentSchema, name string) *PortSchema {
	for i := range c.Outputs {
		if c.Outputs[i].Export == name {
			return &c.Outputs[i]
		}
	}
	return nil
}

// levelRef is a resolved wire reference ready to be written into a body: the
// path of the argument that holds it and the already-serialized value.
type levelRef struct {
	path  []string
	value string
}

// wire is one edge resolved onto an argument port.
type wire struct {
	text  string
	order int
	seq   int
}

// resolveEdges implements the D1 rule. For an edge A --> B (dataflow, source
// on the left) exactly one endpoint is an argument that holds the reference:
//
//   - B's port is an accepts-role argument and A's port is a produces-role
//     export (a DATA export such as discovery.*.targets) — the reference is
//     emitted in B: B { targets = A.targets }.
//   - A's port is a produces-role argument (forward_to) and B's port is an
//     accepts-role export (a RECEIVER export such as remote_write.receiver) —
//     the reference is emitted in A: A { forward_to = [B.receiver] }.
//
// Anything else is reported instead of silently dropped. Returns wires keyed
// by node id then by argument port name.
func (rd *renderer) resolveEdges(doc GraphDocument, active map[string]GraphNode, labels map[string]string) map[string]map[string][]wire {
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

// nodeRefs turns the wires landing on a node into ordered, path-addressed
// values, following the component's declared input order.
func nodeRefs(c ComponentSchema, wires map[string][]wire) []levelRef {
	refs := []levelRef{}
	for _, in := range c.Inputs {
		ws := wires[in.Prop]
		if len(ws) == 0 {
			continue
		}
		sorted := make([]wire, len(ws))
		copy(sorted, ws)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].order < sorted[j].order })
		texts := make([]string, len(sorted))
		for i, w := range sorted {
			texts[i] = w.text
		}
		value := texts[0]
		if in.Cardinality == "list" {
			value = "[" + strings.Join(texts, ", ") + "]"
		}
		refs = append(refs, levelRef{path: portPath(in), value: value})
	}
	return refs
}

func Render(doc GraphDocument, schema SchemaPayload) RenderResult { //nolint:revive
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

	wires := rd.resolveEdges(doc, active, labels)

	version := schema.Meta.AlloyVersion
	if version == "" {
		version = doc.SchemaVersion
	}
	var lw lineWriter
	lw.writef("// generated by shepherd visual builder — do not edit by hand (edits will be overwritten); graph revision %d, schema %s\n", len(doc.Nodes), version)
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
			rd.writeBody(&lw, "  ", id, c.Attributes, c.Blocks, n.Props, nodeRefs(c, wires[id]))
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

// writeBody emits one block body: schema-ordered attributes (with wired
// references taking the place of the attribute they satisfy), then any
// remaining top-level references, then nested blocks, then a diagnostic for
// every prop the schema does not know.
func (rd *renderer) writeBody(lw *lineWriter, indent, nodeID string, attrs []AttributeSchema, blocks []BlockSchema, props map[string]interface{}, refs []levelRef) {
	consumed := map[string]bool{}
	here := map[string]string{}       // single-segment refs at this level
	order := []string{}               // and their order
	nested := map[string][]levelRef{} // refs that live inside a block at this level
	for _, ref := range refs {
		switch {
		case len(ref.path) == 1:
			if _, dup := here[ref.path[0]]; !dup {
				order = append(order, ref.path[0])
			}
			here[ref.path[0]] = ref.value
		case len(ref.path) > 1:
			head := ref.path[0]
			nested[head] = append(nested[head], levelRef{path: ref.path[1:], value: ref.value})
		}
	}

	for _, a := range attrs {
		if value, wired := here[a.Name]; wired {
			if _, alsoProp := props[a.Name]; alsoProp {
				rd.diag("prop_wire_conflict", sevError, nodeID,
					fmt.Sprintf("%q is both wired and set as a value; the wire was used", a.Name))
			}
			lw.writef("%s%s = %s\n", indent, a.Name, value)
			consumed[a.Name] = true
			delete(here, a.Name)
			continue
		}
		raw, ok := props[a.Name]
		if !ok {
			continue
		}
		consumed[a.Name] = true
		if text, emit := rd.serializeTyped(nodeID, a.Name, raw, a.Type); emit {
			lw.writef("%s%s = %s\n", indent, a.Name, text)
		}
	}
	for _, name := range order {
		value, still := here[name]
		if !still {
			continue
		}
		if _, alsoProp := props[name]; alsoProp {
			rd.diag("prop_wire_conflict", sevError, nodeID,
				fmt.Sprintf("%q is both wired and set as a value; the wire was used", name))
			consumed[name] = true
		}
		lw.writef("%s%s = %s\n", indent, name, value)
	}

	for _, b := range blocks {
		instances, ok := blockInstances(props[b.Name])
		if _, present := props[b.Name]; present {
			consumed[b.Name] = true
			if !ok {
				rd.diag("prop_type_mismatch", sevError, nodeID,
					fmt.Sprintf("%q is a block: expected an object or a list of objects", b.Name))
				instances = nil
			}
			if len(instances) > 1 && !b.Repeatable {
				rd.diag("block_not_repeatable", sevError, nodeID,
					fmt.Sprintf("block %q may only appear once", b.Name))
			}
		}
		childRefs := nested[b.Name]
		if len(instances) == 0 && len(childRefs) == 0 {
			continue
		}
		if len(instances) == 0 {
			instances = []map[string]interface{}{{}}
		}
		for i, inst := range instances {
			var mine []levelRef
			if i == 0 {
				mine = childRefs
			}
			lw.writef("%s%s {\n", indent, b.Name)
			rd.writeBody(lw, indent+"  ", nodeID, b.Attributes, b.Blocks, inst, mine)
			lw.writef("%s}\n", indent)
		}
	}

	unknown := []string{}
	for k := range props {
		if !consumed[k] {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		rd.diag("unknown_prop", sevWarning, nodeID,
			fmt.Sprintf("%q is not an attribute or block of this component in this schema; it was not emitted", k))
	}
}

// writeUntypedProps emits props for a component the schema does not describe.
// Nothing is silently dropped, but no type information is available so values
// are emitted by their JSON shape.
func (rd *renderer) writeUntypedProps(lw *lineWriter, indent, nodeID string, props map[string]interface{}) {
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if props[k] == nil {
			continue
		}
		if text, emit := rd.serializeTyped(nodeID, k, props[k], ""); emit {
			lw.writef("%s%s = %s\n", indent, k, text)
		}
	}
}

// blockInstances normalizes a props value into block instances. A single
// object is one block; an array of objects is one block per element (D2).
func blockInstances(v interface{}) ([]map[string]interface{}, bool) {
	switch x := v.(type) {
	case nil:
		return nil, true
	case map[string]interface{}:
		if _, raw := rawExpr(x); raw {
			return nil, false
		}
		return []map[string]interface{}{x}, true
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(x))
		for _, el := range x {
			m, ok := el.(map[string]interface{})
			if !ok {
				return nil, false
			}
			out = append(out, m)
		}
		return out, true
	}
	return nil, false
}

// rawExpr reports whether a props value is the {"$expr": "..."} escape.
func rawExpr(v interface{}) (string, bool) {
	m, ok := v.(map[string]interface{})
	if !ok || len(m) != 1 {
		return "", false
	}
	s, ok := m[exprKey].(string)
	if !ok || strings.TrimSpace(s) == "" {
		return "", false
	}
	return strings.TrimSpace(s), true
}

func reference(id, port string, nodes map[string]GraphNode, labels map[string]string) string {
	return nodes[id].Component + "." + labels[id] + "." + port
}

var (
	durationRe = regexp.MustCompile(`^[+-]?(0|([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+)$`)
	identRe    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// serializeTyped renders one value according to its declared schema type. It
// returns false when nothing should be emitted; any reason for that is
// reported as a diagnostic first.
func (rd *renderer) serializeTyped(nodeID, name string, v interface{}, typ string) (string, bool) {
	if v == nil {
		// A null prop means "not set"; emitting "null"/"<nil>" as a string was
		// the historical behaviour and produced a config that silently lied.
		return "", false
	}
	if expr, ok := rawExpr(v); ok {
		return expr, true
	}
	mismatch := func(want string) (string, bool) {
		rd.diag("prop_type_mismatch", sevError, nodeID,
			fmt.Sprintf("%q is declared %s in the schema but the value is %s; it was not emitted", name, want, jsonKind(v)))
		return "", false
	}
	switch typ {
	case "secret":
		rd.diag("secret_by_value", sevError, nodeID,
			fmt.Sprintf("%q is a secret: supply it as an expression (a binding or {\"$expr\": ...}), never as a literal", name))
		return "", false
	case "duration":
		s, isString := v.(string)
		if !isString {
			f, isNum := v.(float64)
			if !isNum {
				return mismatch("a duration")
			}
			s = formatNumber(f)
		}
		if !durationRe.MatchString(s) {
			rd.diag("invalid_duration", sevError, nodeID,
				fmt.Sprintf("%q = %q is not a duration (expected a unit such as 30s, 5m, 1h)", name, s))
		}
		return quote(s), true
	case "string":
		switch x := v.(type) {
		case string:
			return quote(x), true
		case bool:
			return quote(strconv.FormatBool(x)), true
		case float64:
			return quote(formatNumber(x)), true
		}
		return mismatch("a string")
	case "number":
		switch x := v.(type) {
		case float64:
			if math.IsNaN(x) || math.IsInf(x, 0) {
				return mismatch("a number")
			}
			return formatNumber(x), true
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
			if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
				return mismatch("a number")
			}
			return formatNumber(f), true
		}
		return mismatch("a number")
	case "bool":
		switch x := v.(type) {
		case bool:
			return strconv.FormatBool(x), true
		case string:
			if x == "true" || x == "false" {
				return x, true
			}
		}
		return mismatch("a bool")
	case "list":
		if x, ok := v.([]interface{}); ok {
			return serializeNatural(x), true
		}
		return mismatch("a list")
	case "map":
		if x, ok := v.(map[string]interface{}); ok {
			return serializeNatural(x), true
		}
		return mismatch("a map")
	case "capsule":
		// A capsule can only come from another component's export, so a string
		// is an expression, not a literal.
		if s, ok := v.(string); ok {
			if strings.TrimSpace(s) == "" {
				return "", false
			}
			return strings.TrimSpace(s), true
		}
		return mismatch("an expression")
	}
	return serializeNatural(v), true
}

// serializeNatural renders a value by its JSON shape, for list elements, map
// values and attributes whose type the schema does not state.
func serializeNatural(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return quote(x)
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return formatNumber(x)
	case []interface{}:
		p := make([]string, len(x))
		for i, z := range x {
			p[i] = serializeNatural(z)
		}
		return "[" + strings.Join(p, ", ") + "]"
	case map[string]interface{}:
		if expr, ok := rawExpr(x); ok {
			return expr
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		p := make([]string, len(keys))
		for i, k := range keys {
			key := k
			if !identRe.MatchString(k) {
				key = quote(k)
			}
			p[i] = key + " = " + serializeNatural(x[k])
		}
		return "{" + strings.Join(p, ", ") + "}"
	}
	return quote(fmt.Sprint(v))
}

// formatNumber prints a float in plain decimal notation. The TS renderer
// expands JavaScript's exponent form to match this exactly.
func formatNumber(f float64) string {
	if f == 0 {
		return "0" // never "-0": JSON round-trips -0 but the two runtimes print it differently
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func jsonKind(v interface{}) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "a string"
	case bool:
		return "a bool"
	case float64:
		return "a number"
	case []interface{}:
		return "a list"
	case map[string]interface{}:
		return "an object"
	}
	return "unsupported"
}
