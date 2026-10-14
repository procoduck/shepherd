package visual

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/parser"
	"github.com/grafana/alloy/syntax/printer"
	"github.com/grafana/alloy/syntax/token"
)

// ParseResult is the output of ParseAlloy.
type ParseResult struct {
	Doc     GraphDocument
	Opaque  bool   // true if the file couldn't be fully mapped to a graph
	Warning string // human-readable note when parsing is partial
	// Bindings are the references to another node's non-port export (a
	// remote.kubernetes.secret's `data`, say), wherever they sit in a body —
	// nested blocks and function calls included. They are a PROJECTION for the
	// read-only graph view: the expression itself stays in Props as
	// {"$expr": ...} so Render reproduces it, which is also why they are kept
	// out of Doc.Bindings — that channel renders `prop = expr` at the node's
	// top level, wrong for a nested prop and a duplicate for a top-level one.
	Bindings []GraphBinding
}

// configBlocks are the top-level Alloy blocks that are not components. They
// have no place in the graph model, so a file containing one is only partially
// mappable. Used when no schema is supplied to say which names are components.
var configBlocks = map[string]bool{
	"logging": true, "tracing": true, "http": true, "declare": true,
	"argument": true, "export": true, "foreach": true, "remotecfg": true,
	"livedebugging": true, "import.file": true, "import.git": true,
	"import.http": true, "import.string": true,
}

// ParseAlloy converts raw Alloy pipeline text into a best-effort GraphDocument.
//
// Top-level component blocks become nodes; their bodies become props, with
// nested blocks kept as nested values (D2) and non-literal expressions kept as
// {"$expr": "..."} so nothing is quietly rewritten. References between
// components become edges in dataflow direction (D1) when a schema is supplied
// — the schema is what says whether an export carries data (the consumer holds
// the reference) or is a receiver (the producer does). Without one, edges are
// oriented referenced → referencing, which is right for data exports only.
//
// Anything that cannot be mapped is reported: the statement is either kept as
// an "opaque.*" node or named in Warning, and Opaque is set. This is one-way
// and lossy by design — it backs the read-only graph view of a hand-written
// pipeline (§4.3). A pipeline built in the builder is reloaded from its
// wizard_state instead (D3).
func ParseAlloy(content, schemaVersion string, schema ...SchemaPayload) ParseResult {
	gp := &graphParser{}
	if len(schema) > 0 && len(schema[0].Components) > 0 {
		gp.schema, gp.hasSchema = schema[0], true
	}
	f, err := parser.ParseFile("<pipeline>", []byte(content))
	if err != nil {
		return ParseResult{
			Doc: GraphDocument{
				Kind:          "alloy-graph/v1",
				SchemaVersion: schemaVersion,
				Meta:          Meta{CreatedWith: "shepherd-parser"},
			},
			Opaque:  true,
			Warning: fmt.Sprintf("syntax error: %v", err),
		}
	}
	doc := GraphDocument{
		Kind:          "alloy-graph/v1",
		SchemaVersion: schemaVersion,
		Viewport:      Viewport{Zoom: 1},
		Meta:          Meta{CreatedWith: "shepherd-parser"},
	}

	// nodeByRef maps "component.label" → node id, for edge resolution.
	nodeByRef := map[string]string{}
	blocks := map[string]*ast.BlockStmt{}

	x, y := 0.0, 0.0
	for _, stmt := range f.Body {
		block, ok := stmt.(*ast.BlockStmt)
		if !ok {
			if attr, isAttr := stmt.(*ast.AttributeStmt); isAttr {
				gp.drop("top-level attribute %q has no place in the graph", attr.Name.Name)
			} else {
				gp.drop("a top-level statement could not be mapped to a node")
			}
			continue
		}
		name := strings.Join(block.Name, ".")
		component := name
		if !gp.isComponent(name) {
			// Kept, not dropped: the caller can badge it, and the "opaque."
			// prefix is what tells VisualBuilderPage to refuse to edit.
			component = "opaque." + name
			gp.drop("%q is not a component and cannot be edited as a node", name)
		}
		label := block.Label
		if label == "" {
			label = "default"
		}
		id := fmt.Sprintf("n_%s_%s", sanitizeLabel(component), sanitizeLabel(label))
		for _, existing := range doc.Nodes {
			if existing.ID == id {
				id += "_2"
				break
			}
		}

		props := gp.props(block.Body, name)
		if component != name {
			// An opaque block's props are a best-effort reading of a body the
			// graph model does not describe (a `declare`'s nested components
			// are dropped entirely), so the graph view shows the block itself,
			// printed back from its parse (comments are not kept). Both
			// renderers skip this key.
			props[rawKey] = gp.print(block)
		}
		doc.Nodes = append(doc.Nodes, GraphNode{
			ID:        id,
			Component: component,
			Label:     label,
			Position:  Position{X: x, Y: y},
			Props:     props,
			// Captured here or lost for good: Props is a map, so parsing an
			// existing config into a graph and rendering it back would otherwise
			// silently re-sequence the author's blocks into schema order. That
			// round trip runs on every "recreate as visual pipeline".
			BlockOrder: topLevelBlockOrder(block.Body),
			Disabled:   false,
		})
		nodeByRef[name+"."+label] = id
		blocks[id] = block
		x += 280
		if x > 1120 {
			x = 0
			y += 200
		}
	}

	// Second pass: turn references into edges, removing the props they came
	// from. Bodies are walked depth-first so a reference inside a block (an
	// OTel `output` block, say) is addressed by its dotted path.
	for i := range doc.Nodes {
		gp.edges(&doc, i, blocks[doc.Nodes[i].ID].Body, nil, nodeByRef, doc.Nodes[i].Props)
	}

	return ParseResult{Doc: doc, Opaque: gp.opaque, Warning: gp.warning(), Bindings: gp.bindings}
}

type graphParser struct {
	schema    SchemaPayload
	hasSchema bool
	dropped   []string
	opaque    bool
	bindings  []GraphBinding
}

func (gp *graphParser) drop(format string, args ...any) {
	gp.opaque = true
	msg := fmt.Sprintf(format, args...)
	for _, existing := range gp.dropped {
		if existing == msg {
			return
		}
	}
	gp.dropped = append(gp.dropped, msg)
}

func (gp *graphParser) warning() string {
	if len(gp.dropped) == 0 {
		return ""
	}
	return "this pipeline could not be fully mapped to a graph: " + strings.Join(gp.dropped, "; ")
}

func (gp *graphParser) isComponent(name string) bool {
	if gp.hasSchema {
		_, ok := gp.schema.Components[name]
		return ok
	}
	return !configBlocks[name]
}

// topLevelBlockOrder lists a body's block names in first-appearance order, once
// each. Repeats of the same name keep their relative order inside the props
// list already, so only the sequence *between* differently-named blocks needs
// recording — that is the part a map cannot hold.
func topLevelBlockOrder(body ast.Body) []string {
	var order []string
	seen := map[string]bool{}
	for _, stmt := range body {
		block, ok := stmt.(*ast.BlockStmt)
		if !ok {
			continue
		}
		name := strings.Join(block.Name, ".")
		if seen[name] {
			continue
		}
		seen[name] = true
		order = append(order, name)
	}
	return order
}

// props converts a block body into a props map: attributes by value, nested
// blocks as nested objects (or arrays when the block repeats).
func (gp *graphParser) props(body ast.Body, owner string) map[string]interface{} {
	out := map[string]interface{}{}
	counts := map[string]int{}
	for _, stmt := range body {
		if block, ok := stmt.(*ast.BlockStmt); ok {
			counts[strings.Join(block.Name, ".")]++
		}
	}
	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ast.AttributeStmt:
			out[s.Name.Name] = gp.value(s.Value)
		case *ast.BlockStmt:
			name := strings.Join(s.Name, ".")
			if s.Label != "" {
				gp.drop("block %q in %s has a label, which the graph model cannot represent", name, owner)
				continue
			}
			nested := gp.props(s.Body, owner+"."+name)
			if counts[name] > 1 {
				list, isList := out[name].([]interface{})
				if !isList {
					list = nil
				}
				out[name] = append(list, nested)
				continue
			}
			out[name] = nested
		}
	}
	return out
}

// value converts an expression into a props value. Literals become Go values;
// everything else is preserved verbatim as {"$expr": "..."} so a round trip
// through the renderer reproduces it.
func (gp *graphParser) value(expr ast.Expr) interface{} {
	switch e := expr.(type) {
	case *ast.LiteralExpr:
		switch e.Kind {
		case token.STRING:
			if s, err := strconv.Unquote(e.Value); err == nil {
				return s
			}
			return e.Value
		case token.NUMBER, token.FLOAT:
			if f, err := strconv.ParseFloat(e.Value, 64); err == nil {
				return f
			}
		case token.BOOL:
			return e.Value == "true"
		case token.NULL:
			return nil
		case token.ILLEGAL, token.LITERAL, token.EOF, token.COMMENT, token.IDENT,
			token.OR, token.AND, token.NOT, token.ASSIGN, token.EQ, token.NEQ, token.LT, token.LTE, token.GT, token.GTE,
			token.ADD, token.SUB, token.MUL, token.DIV, token.MOD, token.POW,
			token.LCURLY, token.RCURLY, token.LPAREN, token.RPAREN, token.LBRACK, token.RBRACK, token.COMMA, token.DOT,
			token.TERMINATOR:
			// Not a literal shape we can model; fall through to the
			// expression form below.
		}
	case *ast.ArrayExpr:
		out := make([]interface{}, 0, len(e.Elements))
		for _, el := range e.Elements {
			out = append(out, gp.value(el))
		}
		return out
	case *ast.ObjectExpr:
		out := map[string]interface{}{}
		for _, field := range e.Fields {
			out[field.Name.Name] = gp.value(field.Value)
		}
		return out
	}
	return map[string]interface{}{exprKey: gp.print(expr)}
}

// print renders an expression back to source text.
func (gp *graphParser) print(node ast.Node) string {
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, node); err != nil {
		return ""
	}
	return strings.TrimSpace(buf.String())
}

// edges walks one node's body and converts every reference to another node
// into an edge (deleting the prop it came from when the prop was nothing but
// wiring) or a binding. path is the dotted location of the current body inside
// the component.
func (gp *graphParser) edges(doc *GraphDocument, idx int, body ast.Body, path []string, nodeByRef map[string]string, props map[string]interface{}) {
	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ast.BlockStmt:
			name := strings.Join(s.Name, ".")
			if s.Label != "" {
				continue
			}
			child, ok := props[name].(map[string]interface{})
			if !ok {
				// A repeated block is an array; references inside one cannot
				// be addressed by a port path, so they stay as props.
				continue
			}
			gp.edges(doc, idx, s.Body, append(append([]string{}, path...), name), nodeByRef, child)
		case *ast.AttributeStmt:
			prop := strings.Join(append(append([]string{}, path...), s.Name.Name), ".")
			if refs, isWire := wireRefs(s.Value); isWire {
				gp.wire(doc, idx, prop, s, refs, nodeByRef, props)
				continue
			}
			gp.expression(doc, idx, prop, s, nodeByRef)
		}
	}
}

// wire handles an attribute whose whole value is wiring — a reference, a list
// of them, or the array.concat the renderer emits for a targets fan-in — the
// shape both the builder and hand-written configs use for a wire. Every port
// reference becomes an edge and, when all of them do, the prop goes (the
// renderer writes it back from the edges). A reference to a non-port export is
// a binding instead; the prop then stays, so nothing is lost.
func (gp *graphParser) wire(doc *GraphDocument, idx int, prop string, s *ast.AttributeStmt, refs []string, nodeByRef map[string]string, props map[string]interface{}) {
	if len(refs) == 0 {
		return
	}
	node := doc.Nodes[idx]
	edges := 0
	for _, ref := range refs {
		otherID, export, ok := resolveRef(ref, nodeByRef)
		if !ok {
			continue
		}
		if gp.isPort(doc, otherID, export) {
			gp.edge(doc, idx, prop, otherID, export)
			edges++
			continue
		}
		gp.binding(node.ID, prop, otherID, export, s.Value)
	}
	if edges == len(refs) {
		delete(props, s.Name.Name)
	} else if edges > 0 {
		gp.drop("attribute %q of %s mixes wires with other values", prop, node.Component)
	}
}

// expression handles an attribute whose value is a computation that happens to
// contain references — convert.nonsensitive(secret.data["k"]), a concat mixing
// wires and literals, an index into an export. The prop keeps the expression
// verbatim ({"$expr": ...}, set by props()), so rendering it back is exact. A
// reference to a port is still drawn as an edge, because that IS where the
// node's data comes from, and reported, because rebuilding the attribute from
// that edge would lose the rest of the expression. Any other reference is a
// binding.
func (gp *graphParser) expression(doc *GraphDocument, idx int, prop string, s *ast.AttributeStmt, nodeByRef map[string]string) {
	node := doc.Nodes[idx]
	for _, ref := range exprRefs(s.Value) {
		otherID, export, ok := resolveRef(ref, nodeByRef)
		if !ok {
			continue
		}
		if gp.isPort(doc, otherID, export) {
			gp.edge(doc, idx, prop, otherID, export)
			gp.drop("attribute %q of %s wires %s inside an expression; it is drawn as a wire and kept as written",
				prop, node.Component, ref)
			continue
		}
		gp.binding(node.ID, prop, otherID, export, s.Value)
	}
}

// binding records a reference to a non-port export (see ParseResult.Bindings).
// Expr is the whole attribute value: that is what the prop holds.
func (gp *graphParser) binding(nodeID, prop, otherID, export string, value ast.Expr) {
	gp.bindings = append(gp.bindings, GraphBinding{
		Node: nodeID,
		Prop: prop,
		Ref:  BindingRef{Node: otherID, Export: export, Expr: gp.print(value)},
	})
}

// resolveRef splits a "component.label.export" reference into the node it
// names and the export, trying progressively shorter prefixes so a dotted
// export (output.metrics) and a dotted component name both resolve.
func resolveRef(ref string, nodeByRef map[string]string) (nodeID, export string, ok bool) {
	parts := strings.Split(ref, ".")
	if len(parts) < 3 {
		return "", "", false
	}
	for split := len(parts) - 1; split >= 2; split-- {
		if id, exists := nodeByRef[strings.Join(parts[:split], ".")]; exists {
			return id, strings.Join(parts[split:], "."), true
		}
	}
	return "", "", false
}

// isPort reports whether export is a wireable port of the node. Without a
// schema every reference is treated as one (the legacy behaviour); with one, a
// mapped component's port list decides, and an opaque node — whose exports no
// schema describes — has none.
func (gp *graphParser) isPort(doc *GraphDocument, nodeID, export string) bool {
	if !gp.hasSchema {
		return true
	}
	for _, n := range doc.Nodes {
		if n.ID != nodeID {
			continue
		}
		c, known := gp.schema.Components[n.Component]
		return known && findOutput(c, export) != nil
	}
	return false
}

// edge adds the edge for one resolved reference, in dataflow direction.
func (gp *graphParser) edge(doc *GraphDocument, idx int, prop, otherID, export string) {
	node := doc.Nodes[idx]
	edge := GraphEdge{
		ID:   fmt.Sprintf("e_%s_%s_%s", otherID, node.ID, prop),
		From: PortRef{Node: otherID, Port: export},
		To:   PortRef{Node: node.ID, Port: prop},
	}
	if gp.isReceiverExport(doc, otherID, export) {
		// A receiver export means data flows this node → the referenced
		// one, so the canvas edge points the other way (D1(b)).
		edge.ID = fmt.Sprintf("e_%s_%s_%s", node.ID, otherID, prop)
		edge.From = PortRef{Node: node.ID, Port: prop}
		edge.To = PortRef{Node: otherID, Port: export}
	}
	doc.Edges = append(doc.Edges, edge)
}

func (gp *graphParser) isReceiverExport(doc *GraphDocument, nodeID, export string) bool {
	if !gp.hasSchema {
		return false
	}
	for _, n := range doc.Nodes {
		if n.ID != nodeID {
			continue
		}
		if out := findOutput(gp.schema.Components[n.Component], export); out != nil {
			return outputRole(*out) == roleAccepts
		}
	}
	return false
}

// wireRefs reports whether an expression is pure wiring — a reference, a list
// of references (nested lists included), or array.concat over those, which is
// what the renderer writes for a targets fan-in — and returns its references.
func wireRefs(expr ast.Expr) ([]string, bool) {
	switch e := expr.(type) {
	case *ast.AccessExpr:
		return []string{accessPath(e)}, true
	case *ast.ArrayExpr:
		return wireList(e.Elements)
	case *ast.CallExpr:
		if fn, isAccess := e.Value.(*ast.AccessExpr); isAccess && accessPath(fn) == "array.concat" {
			return wireList(e.Args)
		}
	}
	return nil, false
}

func wireList(elems []ast.Expr) ([]string, bool) {
	refs := []string{}
	for _, el := range elems {
		inner, ok := wireRefs(el)
		if !ok {
			return nil, false
		}
		refs = append(refs, inner...)
	}
	return refs, true
}

// exprRefs returns every dotted reference anywhere inside an expression: in
// call arguments, indexed values and indices, operands, object fields and
// list elements. A called function's own name (convert.nonsensitive) is not a
// reference and is skipped.
func exprRefs(expr ast.Expr) []string {
	var refs []string
	switch e := expr.(type) {
	case *ast.AccessExpr:
		root := ast.Expr(e)
		for {
			inner, isAccess := root.(*ast.AccessExpr)
			if !isAccess {
				break
			}
			root = inner.Value
		}
		if _, isIdent := root.(*ast.IdentifierExpr); isIdent {
			refs = append(refs, accessPath(e))
		} else {
			// foo(x).y: the field access is on a computed value, so only
			// the computation can hold a reference.
			refs = append(refs, exprRefs(root)...)
		}
	case *ast.ArrayExpr:
		for _, el := range e.Elements {
			refs = append(refs, exprRefs(el)...)
		}
	case *ast.ObjectExpr:
		for _, field := range e.Fields {
			refs = append(refs, exprRefs(field.Value)...)
		}
	case *ast.CallExpr:
		for _, arg := range e.Args {
			refs = append(refs, exprRefs(arg)...)
		}
	case *ast.IndexExpr:
		refs = append(refs, exprRefs(e.Value)...)
		refs = append(refs, exprRefs(e.Index)...)
	case *ast.BinaryExpr:
		refs = append(refs, exprRefs(e.Left)...)
		refs = append(refs, exprRefs(e.Right)...)
	case *ast.UnaryExpr:
		refs = append(refs, exprRefs(e.Value)...)
	case *ast.ParenExpr:
		refs = append(refs, exprRefs(e.Inner)...)
	case *ast.IdentifierExpr, *ast.LiteralExpr:
		// a bare identifier or a literal is not a cross-component reference
	}
	return refs
}

// accessPath reconstructs the dotted path from an AccessExpr chain.
func accessPath(e *ast.AccessExpr) string {
	parts := []string{e.Name.Name}
	cur := e.Value
	for {
		switch inner := cur.(type) {
		case *ast.AccessExpr:
			parts = append([]string{inner.Name.Name}, parts...)
			cur = inner.Value
		case *ast.IdentifierExpr:
			parts = append([]string{inner.Ident.Name}, parts...)
			return strings.Join(parts, ".")
		default:
			return strings.Join(parts, ".")
		}
	}
}
