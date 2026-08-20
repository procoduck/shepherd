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

		doc.Nodes = append(doc.Nodes, GraphNode{
			ID:        id,
			Component: component,
			Label:     label,
			Position:  Position{X: x, Y: y},
			Props:     gp.props(block.Body, name),
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

	return ParseResult{Doc: doc, Opaque: gp.opaque, Warning: gp.warning()}
}

type graphParser struct {
	schema    SchemaPayload
	hasSchema bool
	dropped   []string
	opaque    bool
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
		default:
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
// into an edge, deleting the prop it came from. path is the dotted location of
// the current body inside the component.
func (gp *graphParser) edges(doc *GraphDocument, idx int, body ast.Body, path []string, nodeByRef map[string]string, props map[string]interface{}) {
	node := doc.Nodes[idx]
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
			refs := extractRefs(s.Value)
			if len(refs) == 0 {
				continue
			}
			prop := strings.Join(append(append([]string{}, path...), s.Name.Name), ".")
			consumed := 0
			for _, ref := range refs {
				if gp.edge(doc, idx, prop, ref, nodeByRef) {
					consumed++
				}
			}
			if consumed == len(refs) {
				delete(props, s.Name.Name)
			} else if consumed > 0 {
				gp.drop("attribute %q of %s mixes wires with other values", prop, node.Component)
			}
		}
	}
}

// edge resolves one "component.label.export" reference into a graph edge in
// dataflow direction.
func (gp *graphParser) edge(doc *GraphDocument, idx int, prop, ref string, nodeByRef map[string]string) bool {
	node := doc.Nodes[idx]
	parts := strings.Split(ref, ".")
	if len(parts) < 3 {
		return false
	}
	// Try progressively shorter prefixes to find a matching node.
	for split := len(parts) - 1; split >= 2; split-- {
		otherID, exists := nodeByRef[strings.Join(parts[:split], ".")]
		if !exists {
			continue
		}
		export := strings.Join(parts[split:], ".")
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
		return true
	}
	return false
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

// extractRefs returns all dot-joined access expression strings in an expression.
func extractRefs(expr ast.Expr) []string {
	var refs []string
	switch e := expr.(type) {
	case *ast.AccessExpr:
		// Build the full dot-path
		refs = append(refs, accessPath(e))
	case *ast.ArrayExpr:
		for _, elem := range e.Elements {
			refs = append(refs, extractRefs(elem)...)
		}
	case *ast.IdentifierExpr:
		// single identifier — not a cross-component reference
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
