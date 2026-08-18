package visual

import (
	"fmt"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/parser"
)

// ParseResult is the output of ParseAlloy.
type ParseResult struct {
	Doc     GraphDocument
	Opaque  bool   // true if the file couldn't be fully mapped to a graph
	Warning string // human-readable note when parsing is partial
}

// ParseAlloy converts raw Alloy pipeline text into a best-effort GraphDocument.
//
// It extracts top-level BlockStmts as nodes and infers edges from list-typed
// attributes whose values are AccessExprs (component.label.export references).
// Anything that can't be mapped is left in the graph with an "opaque" component
// name so the caller can badge it in the UI.
//
// This is intentionally one-way and lossy: it is used only for the read-only
// graph view (§4.3). The resulting graph is never used to regenerate Alloy text.
func ParseAlloy(content, schemaVersion string) ParseResult {
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

	// First pass: extract all top-level blocks as nodes.
	x, y := 0.0, 0.0
	for _, stmt := range f.Body {
		block, ok := stmt.(*ast.BlockStmt)
		if !ok {
			continue
		}
		component := strings.Join(block.Name, ".")
		label := block.Label
		if label == "" {
			label = "default"
		}
		id := fmt.Sprintf("n_%s_%s", sanitizeLabel(component), sanitizeLabel(label))
		// deduplicate ids in case of name collision
		for _, existing := range doc.Nodes {
			if existing.ID == id {
				id += "_2"
				break
			}
		}

		node := GraphNode{
			ID:        id,
			Component: component,
			Label:     label,
			Position:  Position{X: x, Y: y},
			Props:     extractProps(block.Body),
			Disabled:  false,
		}
		doc.Nodes = append(doc.Nodes, node)
		nodeByRef[component+"."+label] = id
		x += 280
		if x > 1120 {
			x = 0
			y += 200
		}
	}

	// Second pass: extract edges from list-typed reference attributes.
	for i, node := range doc.Nodes {
		block := findBlock(f.Body, node.Component, node.Label)
		if block == nil {
			continue
		}
		for _, stmt := range block.Body {
			attr, ok := stmt.(*ast.AttributeStmt)
			if !ok {
				continue
			}
			prop := attr.Name.Name
			// Look for array expressions containing access expressions (references).
			refs := extractRefs(attr.Value)
			for _, ref := range refs {
				// ref is a dot-separated access like "prometheus.scrape.app.metrics"
				// The node ref is "component.label" (all but the last segment = export).
				parts := strings.Split(ref, ".")
				if len(parts) < 3 {
					continue
				}
				// Try progressively shorter prefixes to find a matching node.
				for split := len(parts) - 1; split >= 2; split-- {
					nodeRef := strings.Join(parts[:split], ".")
					export := strings.Join(parts[split:], ".")
					if fromID, exists := nodeByRef[nodeRef]; exists {
						edgeID := fmt.Sprintf("e_%s_%s_%s", fromID, node.ID, prop)
						doc.Edges = append(doc.Edges, GraphEdge{
							ID:   edgeID,
							From: PortRef{Node: fromID, Port: export},
							To:   PortRef{Node: node.ID, Port: prop},
						})
						// Remove from Props so the canvas doesn't show it as an attr
						delete(doc.Nodes[i].Props, prop)
						break
					}
				}
			}
		}
	}

	opaque := false
	for _, n := range doc.Nodes {
		if strings.HasPrefix(n.Component, "opaque.") {
			opaque = true
		}
	}

	return ParseResult{Doc: doc, Opaque: opaque}
}

// extractProps converts a block body into a props map (string attrs only).
func extractProps(body ast.Body) map[string]interface{} {
	props := map[string]interface{}{}
	for _, stmt := range body {
		attr, ok := stmt.(*ast.AttributeStmt)
		if !ok {
			continue
		}
		if lit, ok := attr.Value.(*ast.LiteralExpr); ok {
			props[attr.Name.Name] = lit.Value
		}
	}
	return props
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

// findBlock returns the BlockStmt for the given component name and label.
func findBlock(body ast.Body, component, label string) *ast.BlockStmt {
	name := strings.Split(component, ".")
	for _, stmt := range body {
		block, ok := stmt.(*ast.BlockStmt)
		if !ok {
			continue
		}
		if strings.Join(block.Name, ".") == strings.Join(name, ".") && block.Label == label {
			return block
		}
	}
	return nil
}
