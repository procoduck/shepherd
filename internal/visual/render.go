// Package visual renders visual-builder graphs into deterministic Alloy.
package visual

import (
	"container/heap"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
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
	// BlockOrder records the author's ordering of this node's top-level blocks,
	// by block name. Props is a map, so without this the order in which a user
	// arranged differently-named blocks is never stored — not merely lost at
	// render time.
	//
	// Alloy block order is semantic: loki.process runs stages in document order,
	// so stage.json before stage.labels extracts fields and then promotes them,
	// while the reverse leaves stage.labels nothing to promote. Both render,
	// both validate, both run — one silently drops every label.
	//
	// Empty means "no recorded order" and the renderer falls back to schema
	// declaration order, which is what every graph saved before this field
	// existed gets, byte for byte.
	BlockOrder []string `json:"block_order,omitempty"`
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
	// Stability is the component's maturity: "ga", "public-preview", or
	// "experimental" (#114). The visual builder hides experimental components
	// unless the org opts in, and the server refuses to render a graph that uses
	// one without that opt-in. Empty is treated as GA (not experimental).
	Stability string `json:"stability"`
}

type AttributeSchema struct { //nolint:revive
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
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

	// rawKey holds an opaque node's block as printed from its parse (ParseAlloy sets
	// it for the read-only graph view). It is display-only: neither renderer
	// ever emits it, whatever component it sits on.
	rawKey = "$raw"
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

// MaxRenderBytes is the most Alloy text Render writes for one graph: the
// pipeline contents limit (config.MaxPipelineContentsBytes, which a test pins
// this to), since nothing larger could be saved anyway. Render refuses a graph
// whose text would pass it as soon as the text does — while writing, never by
// building the whole string first (#391: a 910 KB graph rendered 6.0 GB).
const MaxRenderBytes = 1 << 20

// MaxLabelBytes is the longest node label Render accepts. Alloy itself sets
// no limit on a block label, but the label is the component's identity
// everywhere Alloy reports it (its UI, its metrics' component_id label, its
// logs), and every wire to the node writes it again, so an unbounded one is
// what let a single node expand by the edge count (#391). 256 covers any
// name a person types and any Kubernetes object name (DNS subdomains top out
// at 253), with room for a prefix.
const MaxLabelBytes = 256

// ErrOutputTooLarge is RenderResult.Err for a graph refused because its text
// would pass MaxRenderBytes.
var ErrOutputTooLarge = errors.New("rendered pipeline exceeds 1 MiB")

const codeOutputTooLarge = "output_too_large"

// MaxComponentBytes is the longest node component Render accepts. Component
// names are schema identifiers — the longest in any shipped artifact is 40
// bytes (otelcol.extension.jaeger_remote_sampling) — so 256 leaves room for
// any future or custom name, and nothing longer is a component. Unbounded,
// it was copied into every per-edge diagnostic (#393 review: 12.8 GB of
// messages from a 1 MiB graph).
const MaxComponentBytes = 256

// MaxDiagnostics and MaxDiagnosticBytes bound one render's diagnostic list
// (Layer, Severity, Code, both node ids and Message, summed). Past either,
// the rest are counted into one too_many_diagnostics entry, which is an error
// when any dropped one was, so a refusal is never lost to the cap.
const (
	MaxDiagnostics     = 200
	MaxDiagnosticBytes = 64 << 10
)

const codeTooManyDiagnostics = "too_many_diagnostics"

// maxCycleNames is how many of a cycle's components its diagnostic names.
const maxCycleNames = 10

// maxMessageField is how much of one client-supplied string (an edge id, a
// port, a prop name, a value) a diagnostic message quotes.
const maxMessageField = 64

// clip shortens a client-supplied string for a diagnostic message.
func clip(s string) string { return clipTo(s, maxMessageField) }

// clipTo shortens s to at most n bytes, ending in an ellipsis when cut.
func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func diagSize(d RenderDiagnostic) int {
	return len(d.Layer) + len(d.Severity) + len(d.Code) + len(d.NodeID) + len(d.NodeID2) + len(d.Message)
}

// diagSink accumulates diagnostics up to MaxDiagnostics / MaxDiagnosticBytes
// and counts the rest.
type diagSink struct {
	out        []RenderDiagnostic
	bytes      int
	dropped    int
	droppedErr bool
}

func (s *diagSink) add(d RenderDiagnostic) {
	if d.Code == codeTooManyDiagnostics {
		// A list capped earlier: fold its count in rather than keep it.
		var n int
		if _, err := fmt.Sscanf(d.Message, "%d more", &n); err == nil {
			s.dropped += n
		}
		s.droppedErr = s.droppedErr || d.Severity == sevError
		return
	}
	if s.dropped > 0 || len(s.out) >= MaxDiagnostics || s.bytes+diagSize(d) > MaxDiagnosticBytes {
		s.dropped++
		s.droppedErr = s.droppedErr || d.Severity == sevError
		return
	}
	s.out = append(s.out, d)
	s.bytes += diagSize(d)
}

func (s *diagSink) list() []RenderDiagnostic {
	if s.dropped == 0 {
		return s.out
	}
	sev := sevWarning
	if s.droppedErr {
		sev = sevError
	}
	return append(s.out, RenderDiagnostic{
		Layer: "L1", Severity: sev, Code: codeTooManyDiagnostics,
		Message: fmt.Sprintf("%d more diagnostics were not reported (at most %d, %d bytes, are)", s.dropped, MaxDiagnostics, MaxDiagnosticBytes),
	})
}

// CapDiagnostics applies Render's diagnostic cap to a list a caller has added
// to (VisualService's experimental gate). An entry Render already folded
// into too_many_diagnostics stays counted.
func CapDiagnostics(diags []RenderDiagnostic) []RenderDiagnostic {
	var s diagSink
	for _, d := range diags {
		s.add(d)
	}
	if s.dropped == 0 {
		return diags
	}
	return s.list()
}

// Err reports a render that was refused for size (ErrOutputTooLarge). Every
// other problem is a diagnostic; this one also has an error so a caller can
// refuse the request (InvalidArgument) rather than return empty content.
func (r RenderResult) Err() error {
	for _, d := range r.Diagnostics {
		if d.Code == codeOutputTooLarge {
			return ErrOutputTooLarge
		}
	}
	return nil
}

// lineWriter wraps a strings.Builder and maintains a running line count. With
// a non-zero limit it stops writing at the first write that would take the
// text past it and reports that in over; nothing after that is written.
type lineWriter struct {
	b     strings.Builder
	lines int
	limit int
	over  bool
}

func (lw *lineWriter) writef(format string, args ...any) {
	if lw.over {
		return
	}
	lw.writes(fmt.Sprintf(format, args...))
}

func (lw *lineWriter) writes(s string) {
	if lw.over {
		return
	}
	if lw.limit > 0 && lw.b.Len()+len(s) > lw.limit {
		lw.over = true
		return
	}
	lw.b.WriteString(s)
	lw.lines += strings.Count(s, "\n")
}

func (lw *lineWriter) currentLine() int { return lw.lines + 1 }

// natural serializes a value by its JSON shape within the room the writer has
// left; a value that cannot fit marks the writer over and is not emitted.
func (lw *lineWriter) natural(v interface{}) (string, bool) {
	if lw.limit == 0 {
		return serializeNatural(v), true
	}
	text, ok := serializeNaturalWithin(v, lw.limit-lw.b.Len())
	if !ok {
		lw.over = true
		return "", false
	}
	return text, true
}

// renderer carries the per-render state that every emission step appends to.
type renderer struct {
	schema SchemaPayload
	sink   diagSink
}

func (rd *renderer) add(d RenderDiagnostic) { rd.sink.add(d) }

func (rd *renderer) diag(code, severity, nodeID, msg string) {
	rd.add(RenderDiagnostic{Layer: "L1", Severity: severity, Code: code, NodeID: nodeID, Message: msg})
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
//
// A second edge with the same endpoints and ports is refused (an error
// diagnostic, once per pair) and not wired again. It is not harmless to merge:
// Alloy fans out to every element of forward_to, so [B.receiver, B.receiver]
// delivers each sample twice, and array.concat(A.targets, A.targets) scrapes
// each target twice. The builder never creates one (its store refuses a
// duplicate connection), so only a hand-built request carries one — #391's
// probe sent 15,000.
//
// Every resolved wire's text lands in the output, so once their total passes
// MaxRenderBytes the render cannot fit: tooLarge reports that before the
// joined lists are built.
func (rd *renderer) resolveEdges(doc GraphDocument, active map[string]GraphNode, labels map[string]string) (wires map[string]map[string][]wire, tooLarge bool) {
	out := map[string]map[string][]wire{}
	total := 0
	add := func(node, port string, w wire) {
		if out[node] == nil {
			out[node] = map[string][]wire{}
		}
		out[node][port] = append(out[node][port], w)
		total += len(w.text)
	}
	seen := map[[4]string]bool{}
	reported := map[[4]string]bool{}
	for i, e := range doc.Edges {
		if total > MaxRenderBytes {
			return nil, true
		}
		fromNode, fromActive := active[e.From.Node]
		toNode, toActive := active[e.To.Node]
		if !fromActive || !toActive {
			// Wires to disabled or absent nodes are dropped along with the
			// node itself; that is the documented meaning of "disabled".
			continue
		}
		key := [4]string{e.From.Node, e.From.Port, e.To.Node, e.To.Port}
		if seen[key] {
			if !reported[key] {
				reported[key] = true
				rd.add(RenderDiagnostic{
					Layer: "L1", Severity: sevError, Code: "duplicate_edge",
					NodeID: e.From.Node, NodeID2: e.To.Node,
					Message: fmt.Sprintf("edge %s repeats %s.%s → %s.%s; Alloy would deliver the data once per copy, so the copies were not emitted",
						clip(e.ID), fromNode.Component, clip(e.From.Port), toNode.Component, clip(e.To.Port)),
				})
			}
			continue
		}
		seen[key] = true
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
			rd.add(RenderDiagnostic{
				Layer: "L1", Severity: sevError, Code: "edge_unresolved",
				NodeID: e.From.Node, NodeID2: e.To.Node,
				Message: fmt.Sprintf("edge %s: %s.%s → %s.%s does not connect an argument to a matching export; the reference was not emitted",
					clip(e.ID), fromNode.Component, clip(e.From.Port), toNode.Component, clip(e.To.Port)),
			})
		}
	}
	if total > MaxRenderBytes {
		return nil, true
	}
	return out, false
}

// targetsPortType is the schema type of every input port that carries Alloy's
// []discovery.Target — 17 ports on 17 components in the shipped artifact,
// `prometheus.scrape.targets` and `loki.source.file.targets` among them. It is
// the one port type whose Alloy-side value is ALREADY a list, which is what
// refValue below turns on.
const targetsPortType = "targets"

// refValue renders the wires landing on one input port as an Alloy value.
//
// Every input in the shipped artifact has cardinality "list", so the obvious
// rendering is a `[a, b]` list literal — and for the receiver-shaped port
// types (otel.*, loki.logs, prom.metrics, pyroscope.profiles) that is exactly
// right: each export is one receiver and the argument wants a list of them.
//
// It is WRONG for the "targets" port type, and wrongly in a way only a real
// run can see. A discovery export is already a `[]discovery.Target`, so
// wrapping it produces a list-of-lists, and Alloy v1.18.1 refuses it at
// evaluation time with:
//
//	discovery.relabel.k8s.output target::ConvertFrom: conversion from
//	'[]discovery.Target' is not supported
//
// `alloy validate` accepts that config — only `alloy run` evaluates the
// dataflow graph — so the corpus golden tests, the transform's Stages12
// validate gate and the ordinary fleet-delivery path all passed it through.
// It made every wired discovery→scrape pipeline (minimal-scrape included, and
// its committed golden) unrunnable on a real agent. Evidence:
// docs/proofs/sandbox-sim-e2e.md §1.
//
// So: bare reference for a single targets wire, `array.concat(...)` for
// several — both confirmed against real grafana/alloy:v1.18.1 with `alloy
// run`, not just `validate`. CheckPortShapes (portshape.go) enforces the same
// two facts on hand-written config in the validation gate's Stage 2 (#233).
func refValue(in PortSchema, texts []string) string {
	if in.Type == targetsPortType {
		if len(texts) == 1 {
			return texts[0]
		}
		return "array.concat(" + strings.Join(texts, ", ") + ")"
	}
	if in.Cardinality == "list" {
		return "[" + strings.Join(texts, ", ") + "]"
	}
	return texts[0]
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
		value := refValue(in, texts)
		refs = append(refs, levelRef{path: portPath(in), value: value})
	}
	return refs
}

// Render turns a graph into Alloy text. Its output is bounded: a label over
// MaxLabelBytes is refused, a duplicate edge is refused and wired once, and a
// graph whose text would pass MaxRenderBytes is refused (RenderResult.Err)
// as soon as the text does. Every step is n log n in the graph's size, and
// the emission order is the same as it always was, byte for byte (#391).
func Render(doc GraphDocument, schema SchemaPayload) RenderResult {
	rd := &renderer{schema: schema}
	r := RenderResult{NodeMap: map[string]NodeRange{}}
	labels := map[string]string{}
	seen := map[string]string{}
	refuse := false
	for _, n := range doc.Nodes {
		if len(n.Component) > MaxComponentBytes {
			refuse = true
			rd.diag("component_too_long", sevError, n.ID,
				fmt.Sprintf("the component name is %d bytes; the limit is %d", len(n.Component), MaxComponentBytes))
			continue
		}
		if len(n.Label) > MaxLabelBytes {
			// Checked on the raw label: a disabled node's marker comment
			// writes it unsanitized, and every wire writes the sanitized one.
			refuse = true
			rd.diag("label_too_long", sevError, n.ID,
				fmt.Sprintf("the label is %d bytes; the limit is %d", len(n.Label), MaxLabelBytes))
			continue
		}
		labels[n.ID] = sanitizeLabel(n.Label)
		if n.Disabled {
			continue
		}
		key := n.Component + "\x00" + labels[n.ID]
		if old, ok := seen[key]; ok {
			refuse = true
			rd.add(RenderDiagnostic{
				Layer: "L1", Severity: sevError, Code: "label_collision", NodeID: old, NodeID2: n.ID,
				Message: fmt.Sprintf("nodes %s and %s have the same label", clip(old), clip(n.ID)),
			})
			continue
		}
		seen[key] = n.ID
	}
	if refuse {
		// A collision makes every reference ambiguous, so no content is safe
		// to emit; every colliding pair is reported, not just the first. An
		// over-long label is refused the same way.
		r.Diagnostics = rd.sink.list()
		return r
	}
	tooLarge := func() RenderResult {
		rd.diag(codeOutputTooLarge, sevError, "",
			fmt.Sprintf("the rendered pipeline would exceed %d bytes (1 MiB), the most a pipeline may hold", MaxRenderBytes))
		return RenderResult{NodeMap: map[string]NodeRange{}, Diagnostics: rd.sink.list()}
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
	// less is a strict total order over the active nodes: two with the same
	// component and label are a collision, refused above. So "take the least
	// ready node" has exactly one answer, and the heap below yields the same
	// sequence as the sorted queue it replaced (that queue was re-sorted
	// after every push, quadratic in a wide fan-out).
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
	ready := &readyQueue{less: less}
	for id, d := range indegree {
		if d == 0 {
			ready.ids = append(ready.ids, id)
		}
	}
	heap.Init(ready)
	order := []string{}
	for ready.Len() > 0 {
		id := ready.pop()
		order = append(order, id)
		for _, to := range adj[id] {
			indegree[to]--
			if indegree[to] == 0 {
				heap.Push(ready, to)
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
		// At most maxCycleNames are named: the message must not grow with
		// the graph.
		names := make([]string, 0, min(len(rest), maxCycleNames+1))
		for _, id := range rest[:min(len(rest), maxCycleNames)] {
			names = append(names, active[id].Component+" "+quote(labels[id]))
		}
		if len(rest) > maxCycleNames {
			names = append(names, fmt.Sprintf("%d more", len(rest)-maxCycleNames))
		}
		rd.add(RenderDiagnostic{
			Layer: "L1", Severity: sevError, Code: "cycle", NodeID: rest[0],
			Message: fmt.Sprintf("graph has a cycle through %s; the emission order of those components is arbitrary", strings.Join(names, ", ")),
		})
		order = append(order, rest...)
	}

	wires, over := rd.resolveEdges(doc, active, labels)
	if over {
		return tooLarge()
	}
	// Each node's bindings in document order, indexed once rather than
	// scanned once per node.
	bindings := map[string][]GraphBinding{}
	for _, bind := range doc.Bindings {
		if _, ok := active[bind.Node]; ok {
			bindings[bind.Node] = append(bindings[bind.Node], bind)
		}
	}

	version := schema.Meta.AlloyVersion
	if version == "" {
		version = doc.SchemaVersion
	}
	lw := lineWriter{limit: MaxRenderBytes}
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
		if lw.over {
			break
		}
		n := active[id]
		c, known := schema.Components[n.Component]
		if n.Component != "" && !known {
			rd.add(RenderDiagnostic{
				Layer: "L1", Severity: sevError, Code: "unknown_component", NodeID: id,
				Message: fmt.Sprintf("component %q is not in the schema for this graph", clip(n.Component)),
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
		for _, bind := range bindings[id] {
			expr := strings.TrimSpace(bind.Ref.Expr)
			if expr == "" {
				rd.diag("empty_binding_expr", sevError, id, fmt.Sprintf("binding for %q has an empty expression", clip(bind.Prop)))
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
	if lw.over {
		return tooLarge()
	}
	r.Content = lw.b.String()
	r.Diagnostics = rd.sink.list()
	return r
}

// readyQueue is the topological sort's min-heap (container/heap) of nodes
// whose inputs are all placed, ordered by less.
type readyQueue struct {
	ids  []string
	less func(a, b string) bool
}

func (q *readyQueue) Len() int           { return len(q.ids) }
func (q *readyQueue) Less(i, j int) bool { return q.less(q.ids[i], q.ids[j]) }
func (q *readyQueue) Swap(i, j int)      { q.ids[i], q.ids[j] = q.ids[j], q.ids[i] }

// Push and Pop are heap.Interface's; heap.Push only ever passes a string.
func (q *readyQueue) Push(x any) {
	if id, ok := x.(string); ok {
		q.ids = append(q.ids, id)
	}
}

func (q *readyQueue) Pop() any {
	last := q.ids[len(q.ids)-1]
	q.ids = q.ids[:len(q.ids)-1]
	return last
}

// pop removes and returns the least ready node.
func (q *readyQueue) pop() string {
	if id, ok := heap.Pop(q).(string); ok {
		return id
	}
	return ""
}

// writeBody emits one block body: schema-ordered attributes (with wired
// references taking the place of the attribute they satisfy), then any
// remaining top-level references, then nested blocks, then a diagnostic for
// every prop the schema does not know.
// orderBlocks re-sequences a component's block schemas to the author's recorded
// order. Names in `order` come first, in that order; anything the author never
// arranged follows in schema-declaration order, so the result is a permutation
// of `blocks` and never adds or drops one.
//
// An empty `order` returns the schema's own sequence unchanged — that is the
// path every graph saved before BlockOrder existed takes, and it renders exactly
// as it did before.
func orderBlocks(blocks []BlockSchema, order []string) []BlockSchema {
	if len(order) == 0 || len(blocks) == 0 {
		return blocks
	}
	byName := make(map[string]BlockSchema, len(blocks))
	for _, b := range blocks {
		byName[b.Name] = b
	}
	out := make([]BlockSchema, 0, len(blocks))
	placed := make(map[string]bool, len(blocks))
	for _, name := range order {
		// A name the component does not declare is ignored rather than
		// diagnosed: BlockOrder is a rendering hint, and an unknown name is
		// already reported as an unknown prop by the prop walk below.
		b, ok := byName[name]
		if !ok || placed[name] {
			continue
		}
		out = append(out, b)
		placed[name] = true
	}
	for _, b := range blocks {
		if !placed[b.Name] {
			out = append(out, b)
		}
	}
	return out
}

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
		if text, emit := rd.serializeTyped(lw, nodeID, a.Name, raw, a.Type); emit {
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
		if !consumed[k] && k != rawKey {
			unknown = append(unknown, k)
		}
	}
	sort.Strings(unknown)
	for _, k := range unknown {
		rd.diag("unknown_prop", sevWarning, nodeID,
			fmt.Sprintf("%q is not an attribute or block of this component in this schema; it was not emitted", clip(k)))
	}
}

// writeUntypedProps emits props for a component the schema does not describe.
// Nothing is silently dropped, but no type information is available so values
// are emitted by their JSON shape.
func (rd *renderer) writeUntypedProps(lw *lineWriter, indent, nodeID string, props map[string]interface{}) {
	keys := make([]string, 0, len(props))
	for k := range props {
		if k == rawKey {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if props[k] == nil {
			continue
		}
		if text, emit := rd.serializeTyped(lw, nodeID, k, props[k], ""); emit {
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
func (rd *renderer) serializeTyped(lw *lineWriter, nodeID, name string, v interface{}, typ string) (string, bool) {
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
			fmt.Sprintf("%q is declared %s in the schema but the value is %s; it was not emitted", clip(name), want, jsonKind(v)))
		return "", false
	}
	switch typ {
	case "secret":
		rd.diag("secret_by_value", sevError, nodeID,
			fmt.Sprintf("%q is a secret: supply it as an expression (a binding or {\"$expr\": ...}), never as a literal", clip(name)))
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
				fmt.Sprintf("%q = %q is not a duration (expected a unit such as 30s, 5m, 1h)", clip(name), clip(s)))
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
			return lw.natural(x)
		}
		return mismatch("a list")
	case "map":
		if x, ok := v.(map[string]interface{}); ok {
			return lw.natural(x)
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
	return lw.natural(v)
}

// serializeNatural renders a value by its JSON shape, for list elements, map
// values and attributes whose type the schema does not state.
func serializeNatural(v interface{}) string {
	var b boundedBuilder
	writeNatural(&b, v)
	return b.String()
}

// serializeNaturalWithin is serializeNatural that gives up, reporting false,
// as soon as the text passes room bytes — a value is never built whole only
// to be refused (#393 review: 90,000 × -1.79e308 is 9 bytes of JSON and 310
// of Alloy each).
func serializeNaturalWithin(v interface{}, room int) (string, bool) {
	b := boundedBuilder{room: room, bounded: true}
	writeNatural(&b, v)
	if b.over {
		return "", false
	}
	return b.String(), true
}

// boundedBuilder is a strings.Builder that stops at room bytes when bounded.
type boundedBuilder struct {
	strings.Builder
	room    int
	bounded bool
	over    bool
}

func (b *boundedBuilder) put(s string) {
	if b.over {
		return
	}
	if b.bounded && b.Len()+len(s) > b.room {
		b.over = true
		return
	}
	b.WriteString(s)
}

// writeNatural writes exactly what the pre-#393 serializeNatural returned
// (legacySerializeNatural in export_test.go; render_bounds_test.go compares
// them), piece by piece so a bounded builder can stop early.
func writeNatural(b *boundedBuilder, v interface{}) {
	if b.over {
		return
	}
	switch x := v.(type) {
	case nil:
		b.put("null")
	case string:
		b.put(quote(x))
	case bool:
		b.put(strconv.FormatBool(x))
	case float64:
		b.put(formatNumber(x))
	case []interface{}:
		b.put("[")
		for i, z := range x {
			if b.over {
				return
			}
			if i > 0 {
				b.put(", ")
			}
			writeNatural(b, z)
		}
		b.put("]")
	case map[string]interface{}:
		if expr, ok := rawExpr(x); ok {
			b.put(expr)
			return
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.put("{")
		for i, k := range keys {
			if b.over {
				return
			}
			if i > 0 {
				b.put(", ")
			}
			if identRe.MatchString(k) {
				b.put(k)
			} else {
				b.put(quote(k))
			}
			b.put(" = ")
			writeNatural(b, x[k])
		}
		b.put("}")
	default:
		b.put(quote(fmt.Sprint(v)))
	}
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

// nodeCount is the header's "N nodes" ("1 node" for one).
func nodeCount(n int) string {
	if n == 1 {
		return "1 node"
	}
	return fmt.Sprintf("%d nodes", n)
}
