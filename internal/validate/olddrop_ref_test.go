package validate

// The InsertTenantDrops of 3ec5c9c (before the served-form budget), verbatim but
// for the names (ref...): what served_budget_diff_test.go compares against.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/grafana/alloy/syntax/ast"

	"shepherd/internal/alloysyntax"
)

func refInsertTenantDrops(text string) (string, error) {
	return refInsertTenantDropsAt(text, 0)
}

func refInsertTenantDropsAt(text string, depth int) (string, error) {
	if depth > maxDepth {
		return "", fmt.Errorf("%w: modules nest more than %d deep", ErrTenantDrop, maxDepth)
	}
	file, err := alloysyntax.ParseFile("<served>", []byte(text))
	if err != nil {
		return "", fmt.Errorf("%w: the text does not parse: %w", ErrTenantDrop, err)
	}
	d := &refDropper{text: text, depth: depth, newline: "\n"}
	if strings.Contains(text, "\r\n") {
		d.newline = "\r\n"
	}
	if err := d.body(file.Body, "", "", 0); err != nil {
		return "", err
	}
	if len(d.splices) == 0 {
		return text, nil
	}
	// From the last offset back, so earlier offsets stay valid.
	slices.SortStableFunc(d.splices, func(a, b refDropSplice) int { return b.start - a.start })
	out := text
	for _, sp := range d.splices {
		out = out[:sp.start] + sp.text + out[sp.end:]
	}
	if _, err := alloysyntax.ParseFile("<served>", []byte(out)); err != nil {
		return "", fmt.Errorf("%w: the text with the drops inserted does not parse: %w", ErrTenantDrop, err)
	}
	return out, nil
}

type refDropSplice struct {
	start, end int
	text       string
}

type refDropper struct {
	text    string
	depth   int
	newline string
	splices []refDropSplice
}

// body walks stmts. component is the relabel-rule component enclosing them
// ("" outside one), path their block path inside it.
func (d *refDropper) body(stmts ast.Body, component, path string, nesting int) error {
	if nesting > alloysyntax.MaxNesting {
		return fmt.Errorf("%w: blocks nest too deeply", ErrTenantDrop)
	}
	for i, st := range stmts {
		b, ok := st.(*ast.BlockStmt)
		if !ok {
			continue
		}
		name := strings.Join(b.Name, ".")
		switch {
		case component == "" && name == "import.string":
			if err := d.importString(b); err != nil {
				return err
			}
			continue
		case component == "" && name == exporterLoki:
			if err := d.exporterLoki(b, stmts); err != nil {
				return err
			}
			continue
		case component == "":
			rules, err := RelabelRuleBlocks()
			if err != nil {
				return fmt.Errorf("%w: %w", ErrTenantDrop, err)
			}
			if _, ok := rules[name]; ok {
				if err := d.body(b.Body, name, "", nesting+1); err != nil {
					return err
				}
				continue
			}
			if err := d.body(b.Body, "", "", nesting+1); err != nil {
				return err
			}
			continue
		}
		p := name
		if path != "" {
			p = path + "/" + name
		}
		rules, err := RelabelRuleBlocks()
		if err != nil {
			return fmt.Errorf("%w: %w", ErrTenantDrop, err)
		}
		if name == "rule" && rules[component][p] && isLiteralLabelmap(b) && needsTenantDrop(b) &&
			!isTenantDrop(nextRule(stmts, i)) {
			d.insertAfter(b)
		}
		if err := d.body(b.Body, component, p, nesting+1); err != nil {
			return err
		}
	}
	return nil
}

// insertAfter splices tenantDropRule in right after b's closing brace, on
// lines of its own, indented with the leading whitespace (spaces and tabs,
// byte for byte) of the line b starts on — never a column count, which a tab
// or a multi-byte character before b would throw off.
func (d *refDropper) insertAfter(b *ast.BlockStmt) {
	start := ast.StartPos(b).Position().Offset
	end := ast.EndPos(b).Position().Offset + 1
	lineStart := strings.LastIndexByte(d.text[:start], '\n') + 1
	line := d.text[lineStart:start]
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	var sb strings.Builder
	for _, line := range tenantDropRule {
		sb.WriteString(d.newline)
		sb.WriteString(indent)
		sb.WriteString(line)
	}
	d.splices = append(d.splices, refDropSplice{start: end, end: end, text: sb.String()})
}

// exporterLoki routes otelcol.exporter.loki b through a __tenant_id__ drop:
// its forward_to becomes [loki.process.<exporterDropPrefix><label>.receiver],
// and that loki.process — forwarding to the original forward_to, verbatim,
// with a stage.label_drop of __tenant_id__ — is inserted on lines of its own
// right after b (and after a comment trailing b's closing brace on that line,
// afterTrailingComment), indented as b's line is. A forward_to that is absent, empty
// or not a plain list of receivers (plainReceiverList) is left alone: the
// lint refuses the last. A component of that name already in scope (stmts)
// is an error: the served form cannot be built.
func (d *refDropper) exporterLoki(b *ast.BlockStmt, stmts ast.Body) error {
	var fwd *ast.AttributeStmt
	for _, st := range b.Body {
		if a, ok := st.(*ast.AttributeStmt); ok && a.Name.Name == "forward_to" {
			fwd = a
		}
	}
	if fwd == nil || !plainReceiverList(fwd.Value) {
		return nil
	}
	if arr, ok := unparen(fwd.Value).(*ast.ArrayExpr); !ok || len(arr.Elements) == 0 {
		return nil
	}
	label := exporterDropPrefix + b.Label
	for _, st := range stmts {
		if o, ok := st.(*ast.BlockStmt); ok && strings.Join(o.Name, ".") == "loki.process" && o.Label == label {
			return fmt.Errorf("%w: loki.process %q already exists beside %s %q, so the drop cannot be inserted; "+
				"rename that component", ErrTenantDrop, label, exporterLoki, b.Label)
		}
	}
	vStart := ast.StartPos(fwd.Value).Position().Offset
	vEnd := ast.EndPos(fwd.Value).Position().Offset + 1
	original := d.text[vStart:vEnd]
	d.splices = append(d.splices, refDropSplice{start: vStart, end: vEnd, text: "[loki.process." + label + ".receiver]"})

	start := ast.StartPos(b).Position().Offset
	end := ast.EndPos(b).Position().Offset + 1
	lineStart := strings.LastIndexByte(d.text[:start], '\n') + 1
	line := d.text[lineStart:start]
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	lines := []string{
		"",
		"loki.process " + strconv.Quote(label) + " {",
		"  forward_to = " + original,
		"",
		"  stage.label_drop {",
		`    values = ["` + tenantLabel + `"]`,
		"  }",
		"}",
	}
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(d.newline)
		if l != "" {
			sb.WriteString(indent)
			sb.WriteString(l)
		}
	}
	at := afterTrailingComment(d.text, end)
	d.splices = append(d.splices, refDropSplice{start: at, end: at, text: sb.String()})
	return nil
}

// importString inserts the drops into a literal import.string's module
// text, re-quoted. Content that is not a plain literal is left alone
// (ServedFormProblem refuses it in a strict org).
func (d *refDropper) importString(b *ast.BlockStmt) error {
	for _, st := range b.Body {
		a, ok := st.(*ast.AttributeStmt)
		if !ok || a.Name.Name != "content" {
			continue
		}
		content, ok := PlainStringLiteral(a.Value)
		if !ok {
			return nil
		}
		inner, err := refInsertTenantDropsAt(content, d.depth+1)
		if err != nil {
			return fmt.Errorf("import.string %q: %w", b.Label, err)
		}
		if inner != content {
			d.splices = append(d.splices, refDropSplice{
				start: ast.StartPos(a.Value).Position().Offset,
				end:   ast.EndPos(a.Value).Position().Offset + 1,
				text:  alloysyntax.QuoteString(inner),
			})
		}
	}
	return nil
}
