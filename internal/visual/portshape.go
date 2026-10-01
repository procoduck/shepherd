package visual

import (
	"fmt"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/parser"
)

// PortShapeDiagnostic is one wire whose shape Alloy is certain to refuse when
// it loads the config. Line and Col are 1-based, in the coordinates of the
// content handed to CheckPortShapes.
type PortShapeDiagnostic struct {
	Line    int
	Col     int
	Message string
}

// CheckPortShapes reports component references written with the wrong list
// shape for the port they land on — the mistake `alloy validate` cannot see
// (it never evaluates the dataflow graph) and Alloy refuses at load (#233).
// It enforces the same two facts refValue writes into every rendered pipeline:
//
//  1. A "targets" export is ALREADY a list ([]discovery.Target), and a
//     targets argument wants exactly that list. Wrapping the reference in a
//     list literal — `targets = [discovery.kubernetes.pods.targets]` — makes a
//     list of lists, and Alloy fails every element with
//     "target::ConvertFrom: conversion from '[]discovery.Target' is not
//     supported". This fails whatever the discovery finds, even nothing: the
//     element is a slice, and Target.ConvertFrom accepts only maps.
//  2. A receiver export (role "accepts": prometheus.remote_write's
//     `receiver`, otelcol.*'s `input`, ...) is ONE capsule, and a
//     forward_to-style argument (role "produces", cardinality "list") wants a
//     list of them. A bare reference — `forward_to = loki.write.x.receiver` —
//     is a capsule where a list is required; no receiver type implements a
//     capsule conversion, so Alloy fails it with "expected list, got capsule".
//
// FALSE POSITIVES BLOCK SAVES AND GIT SYNCS, so everything here is deliberately
// narrow. A diagnostic needs ALL of:
//
//   - the attribute sits in a block whose name is a schema component (or in a
//     single nested block on the port's schema path, e.g. `output { metrics }`)
//     and whose first name segment is not an import namespace in scope;
//   - the schema declares that attribute as an input port with cardinality
//     "list";
//   - the value is a bare reference — an identifier followed only by `.name`
//     accesses, no call, index or operator — or a list literal whose element is
//     one;
//   - that reference names a component block declared in the same or an
//     enclosing scope, and an export the schema declares on it with the role
//     and type the rule needs.
//
// Anything else — function calls (array.concat), argument.*/each.*
// references, unknown components, untyped exports, cross-signal OTel wiring
// (otel.any is polymorphic, so that is not a type error) — yields no
// diagnostic. A content that does not parse yields none either: Stage 1 owns
// syntax errors.
func CheckPortShapes(content string, schema SchemaPayload) []PortShapeDiagnostic {
	if len(schema.Components) == 0 {
		return nil
	}
	f, err := parser.ParseFile("<pipeline>", []byte(content))
	if err != nil {
		return nil
	}
	pc := &portShapeChecker{schema: schema}
	pc.walkScope(f.Body, nil)
	return pc.diags
}

type portShapeChecker struct {
	schema SchemaPayload
	diags  []PortShapeDiagnostic
}

// shapeScope is one Alloy module body: the component blocks it declares
// ("component.label") and the import namespaces it binds. Scopes chain
// outwards so a foreach template can see the module around it.
type shapeScope struct {
	parent   *shapeScope
	blocks   map[string]bool
	imported map[string]bool
}

func (s *shapeScope) declares(ref string) bool {
	for c := s; c != nil; c = c.parent {
		if c.blocks[ref] {
			return true
		}
	}
	return false
}

func (s *shapeScope) allImports() map[string]bool {
	out := map[string]bool{}
	for c := s; c != nil; c = c.parent {
		for ns := range c.imported {
			out[ns] = true
		}
	}
	return out
}

func (s *shapeScope) isImport(namespace string) bool {
	for c := s; c != nil; c = c.parent {
		if c.imported[namespace] {
			return true
		}
	}
	return false
}

// walkScope checks every component block in one module body, then descends
// into the bodies that open a new scope: a declare (a module of its own) and a
// foreach template (which also sees the enclosing module).
func (pc *portShapeChecker) walkScope(body ast.Body, parent *shapeScope) {
	scope := &shapeScope{parent: parent, blocks: map[string]bool{}, imported: map[string]bool{}}
	for _, stmt := range body {
		block, ok := stmt.(*ast.BlockStmt)
		if !ok {
			continue
		}
		name := strings.Join(block.Name, ".")
		if strings.HasPrefix(name, "import.") {
			scope.imported[block.Label] = true
			continue
		}
		if block.Label != "" {
			scope.blocks[name+"."+block.Label] = true
		}
	}

	for _, stmt := range body {
		block, ok := stmt.(*ast.BlockStmt)
		if !ok {
			continue
		}
		name := strings.Join(block.Name, ".")
		switch name {
		case "declare":
			// A declare body is its own module: it cannot see the components
			// around it, so it starts a fresh scope chain. It keeps the
			// enclosing import namespaces, though — over-skipping an import
			// can only lose a diagnostic, never invent one.
			pc.walkScope(block.Body, &shapeScope{imported: scope.allImports()})
			continue
		case "foreach":
			for _, inner := range block.Body {
				if tmpl, ok := inner.(*ast.BlockStmt); ok && strings.Join(tmpl.Name, ".") == "template" {
					pc.walkScope(tmpl.Body, scope)
				}
			}
			continue
		}
		if scope.isImport(block.Name[0]) {
			continue
		}
		comp, known := pc.schema.Components[name]
		if !known {
			continue
		}
		pc.checkComponent(comp, block.Body, scope)
	}
}

// checkComponent checks every list-cardinality input port of one component
// block, finding each port's attribute by its schema path.
func (pc *portShapeChecker) checkComponent(comp ComponentSchema, body ast.Body, scope *shapeScope) {
	for _, in := range comp.Inputs {
		if in.Cardinality != "list" {
			continue
		}
		path := portPath(in)
		for _, attr := range attributesAt(body, path) {
			pc.checkPort(in, attr, scope)
		}
	}
}

// attributesAt returns the attribute statements at path inside body, walking
// one unlabelled nested block per leading path segment. A port path names
// only non-repeatable blocks (the extractor skips ports in repeatable ones),
// so more than one matching block is already an Alloy error; every match is
// checked anyway.
func attributesAt(body ast.Body, path []string) []*ast.AttributeStmt {
	if len(path) == 0 {
		return nil
	}
	var out []*ast.AttributeStmt
	for _, stmt := range body {
		switch s := stmt.(type) {
		case *ast.AttributeStmt:
			if len(path) == 1 && s.Name.Name == path[0] {
				out = append(out, s)
			}
		case *ast.BlockStmt:
			if len(path) > 1 && s.Label == "" && strings.Join(s.Name, ".") == path[0] {
				out = append(out, attributesAt(s.Body, path[1:])...)
			}
		}
	}
	return out
}

func (pc *portShapeChecker) checkPort(in PortSchema, attr *ast.AttributeStmt, scope *shapeScope) {
	attrName := strings.Join(portPath(in), ".")
	switch {
	case in.Type == targetsPortType && inputRole(in) == roleAccepts:
		// Rule 1: a targets export inside a list literal is a list of lists.
		arr, ok := attr.Value.(*ast.ArrayExpr)
		if !ok {
			return
		}
		for _, el := range arr.Elements {
			ref, ok := pc.resolveExport(el, scope)
			if !ok || ref.out.Type != targetsPortType || outputRole(ref.out) != roleProduces {
				continue
			}
			pos := ast.StartPos(el).Position()
			pc.diags = append(pc.diags, PortShapeDiagnostic{
				Line: pos.Line,
				Col:  pos.Column,
				Message: fmt.Sprintf(
					"%s expects a list of targets; [%s] is a list of lists, because %s is already a list of targets — drop the brackets (use array.concat(a, b) to combine several)",
					attrName, ref.text, ref.text),
			})
		}
	case inputRole(in) == roleProduces:
		// Rule 2: a single receiver where a list of receivers is required.
		ref, ok := pc.resolveExport(attr.Value, scope)
		if !ok || outputRole(ref.out) != roleAccepts {
			return
		}
		pos := ast.StartPos(attr.Value).Position()
		pc.diags = append(pc.diags, PortShapeDiagnostic{
			Line: pos.Line,
			Col:  pos.Column,
			Message: fmt.Sprintf(
				"%s expects a list of receivers; %s is a single receiver — wrap it in brackets: [%s]",
				attrName, ref.text, ref.text),
		})
	}
}

// resolvedExport is a reference resolved to a declared component block's
// schema export.
type resolvedExport struct {
	text string
	out  PortSchema
}

// resolveExport resolves expr to a schema export when, and only when, it is a
// bare `<component>.<label>.<export>` reference to a component block declared
// in scope. Exactly one way of splitting the dotted name may resolve;
// anything ambiguous is treated as unknown.
func (pc *portShapeChecker) resolveExport(expr ast.Expr, scope *shapeScope) (resolvedExport, bool) {
	parts, ok := referenceParts(expr)
	if !ok || len(parts) < 3 || scope.isImport(parts[0]) {
		return resolvedExport{}, false
	}
	var found []resolvedExport
	// The label is one segment (Alloy labels cannot contain a dot), so each
	// split is: component = parts[:i], label = parts[i], export = the rest.
	for i := 1; i <= len(parts)-2; i++ {
		component := strings.Join(parts[:i], ".")
		comp, known := pc.schema.Components[component]
		if !known || !scope.declares(component+"."+parts[i]) {
			continue
		}
		out := findOutput(comp, strings.Join(parts[i+1:], "."))
		if out == nil {
			continue
		}
		found = append(found, resolvedExport{text: strings.Join(parts, "."), out: *out})
	}
	if len(found) != 1 {
		return resolvedExport{}, false
	}
	return found[0], true
}

// referenceParts returns the dotted segments of a pure reference expression:
// an identifier followed only by field accesses. Any other shape — a call, an
// index, an operator, a literal — is not a reference this check can type.
func referenceParts(expr ast.Expr) ([]string, bool) {
	var rev []string
	cur := expr
	for {
		switch e := cur.(type) {
		case *ast.AccessExpr:
			rev = append(rev, e.Name.Name)
			cur = e.Value
		case *ast.IdentifierExpr:
			rev = append(rev, e.Ident.Name)
			parts := make([]string, len(rev))
			for i, p := range rev {
				parts[len(rev)-1-i] = p
			}
			return parts, true
		default:
			return nil, false
		}
	}
}
