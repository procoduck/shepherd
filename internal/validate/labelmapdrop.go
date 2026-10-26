package validate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"sync"

	"github.com/grafana/alloy/syntax/ast"

	"shepherd/internal/alloysyntax"
	"shepherd/internal/schema"
)

// Strict tenant mode's served form drops __tenant_id__ after every labelmap
// that may write it (#410). A labelmap names each label it writes by its
// replacement — "$1" by default, a capture of an existing label name — so
// one whose replacement is not a plain literal free of "$" may write Loki's
// tenant label from the data. Rather than refuse such a rule (the
// grafana/k8s-monitoring chart's output has them), the served form of a
// strict org's pipeline carries, directly after it, the rule tenantDropRule,
// and the tenant lint accepts the labelmap only when that rule follows it in
// the text it lints. The stored text is not changed.

// tenantDropRule is the rule InsertTenantDrops inserts, as it is written
// (indented to the labelmap's own column, the text's line endings).
var tenantDropRule = []string{
	"rule {",
	`  action = "labeldrop"`,
	`  regex  = "` + tenantLabel + `"`,
	"}",
}

// ErrTenantDrop is a served form InsertTenantDrops cannot build.
var ErrTenantDrop = errors.New("strict tenant mode cannot drop " + tenantLabel + " after every labelmap")

// relabelRuleSignature is the attribute set that makes a schema block named
// `rule` a relabel rule (Alloy's common/relabel Config): the rule blocks of
// discovery.relabel, prometheus.relabel, loki.relabel, pyroscope.relabel,
// the prometheus.operator.* components and database_observability.*'s
// database_instance. Other `rule` blocks (loki.process stage.truncate,
// beyla's payload enrichment) lack it.
var relabelRuleSignature = []string{"action", "regex", "replacement", "source_labels", "target_label"}

var (
	relabelRulesOnce sync.Once
	relabelRules     map[string]map[string]bool
	relabelRulesErr  error
)

// RelabelRuleBlocks is where Alloy takes relabel rule blocks, read from
// every shipped component schema (not a hand list, so a new component with
// relabel rules is covered when its schema ships): component name -> the
// block path inside the component ("rule", "database_instance/rule").
func RelabelRuleBlocks() (map[string]map[string]bool, error) {
	relabelRulesOnce.Do(func() {
		relabelRules, relabelRulesErr = loadRelabelRuleBlocks(schema.Embedded)
	})
	return relabelRules, relabelRulesErr
}

type schemaBlock struct {
	Name       string        `json:"name"`
	Attributes []schemaAttr  `json:"attributes"`
	Blocks     []schemaBlock `json:"blocks"`
}

type schemaAttr struct {
	Name string `json:"name"`
}

func loadRelabelRuleBlocks(fsys fs.FS) (map[string]map[string]bool, error) {
	files, err := fs.Glob(fsys, "artifacts/alloy-v*.json")
	if err != nil {
		return nil, fmt.Errorf("listing the component schemas: %w", err)
	}
	if len(files) == 0 {
		return nil, errors.New("no component schema is shipped")
	}
	out := map[string]map[string]bool{}
	for _, f := range files {
		data, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", f, err)
		}
		var art struct {
			Components map[string]struct {
				Blocks []schemaBlock `json:"blocks"`
			} `json:"components"`
		}
		if err := json.Unmarshal(data, &art); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", f, err)
		}
		for name, c := range art.Components {
			collectRelabelRules(name, "", c.Blocks, out)
		}
	}
	return out, nil
}

func collectRelabelRules(component, prefix string, blocks []schemaBlock, out map[string]map[string]bool) {
	for _, b := range blocks {
		path := b.Name
		if prefix != "" {
			path = prefix + "/" + b.Name
		}
		if b.Name == "rule" && isRelabelRuleSchema(b) {
			if out[component] == nil {
				out[component] = map[string]bool{}
			}
			out[component][path] = true
		}
		collectRelabelRules(component, path, b.Blocks, out)
	}
}

func isRelabelRuleSchema(b schemaBlock) bool {
	for _, want := range relabelRuleSignature {
		if !slices.ContainsFunc(b.Attributes, func(a schemaAttr) bool { return a.Name == want }) {
			return false
		}
	}
	return true
}

// InsertTenantDrops is text with tenantDropRule inserted directly after
// every relabel rule block (RelabelRuleBlocks) whose action is the literal
// "labelmap" and that needsTenantDrop — at the top level, in a declare, in
// a foreach template or any other block, and inside the content of every
// literal import.string (re-quoted), to the lint's module depth. A labelmap
// already followed by that rule gets no second one. Text without such a
// labelmap is returned unchanged, byte for byte; otherwise only the
// inserted ranges change. The result must parse. It is part of the served
// form of a strict org's pipeline (pinnedimport.Inline).
func InsertTenantDrops(text string) (string, error) {
	return insertTenantDrops(text, 0)
}

func insertTenantDrops(text string, depth int) (string, error) {
	if depth > maxDepth {
		return "", fmt.Errorf("%w: modules nest more than %d deep", ErrTenantDrop, maxDepth)
	}
	file, err := alloysyntax.ParseFile("<served>", []byte(text))
	if err != nil {
		return "", fmt.Errorf("%w: the text does not parse: %w", ErrTenantDrop, err)
	}
	d := &dropper{text: text, depth: depth, newline: "\n"}
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
	slices.SortStableFunc(d.splices, func(a, b dropSplice) int { return b.start - a.start })
	out := text
	for _, sp := range d.splices {
		out = out[:sp.start] + sp.text + out[sp.end:]
	}
	if _, err := alloysyntax.ParseFile("<served>", []byte(out)); err != nil {
		return "", fmt.Errorf("%w: the text with the drops inserted does not parse: %w", ErrTenantDrop, err)
	}
	return out, nil
}

type dropSplice struct {
	start, end int
	text       string
}

type dropper struct {
	text    string
	depth   int
	newline string
	splices []dropSplice
}

// body walks stmts. component is the relabel-rule component enclosing them
// ("" outside one), path their block path inside it.
func (d *dropper) body(stmts ast.Body, component, path string, nesting int) error {
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
// lines of its own at b's indentation.
func (d *dropper) insertAfter(b *ast.BlockStmt) {
	start := ast.StartPos(b).Position().Offset
	end := ast.EndPos(b).Position().Offset + 1
	lineStart := strings.LastIndexByte(d.text[:start], '\n') + 1
	indent := d.text[lineStart:start]
	if strings.TrimLeft(indent, " \t") != "" {
		indent = strings.Repeat(" ", ast.StartPos(b).Position().Column-1)
	}
	var sb strings.Builder
	for _, line := range tenantDropRule {
		sb.WriteString(d.newline)
		sb.WriteString(indent)
		sb.WriteString(line)
	}
	d.splices = append(d.splices, dropSplice{start: end, end: end, text: sb.String()})
}

// importString inserts the drops into a literal import.string's module
// text, re-quoted. Content that is not a plain literal is left alone
// (ServedFormProblem refuses it in a strict org).
func (d *dropper) importString(b *ast.BlockStmt) error {
	for _, st := range b.Body {
		a, ok := st.(*ast.AttributeStmt)
		if !ok || a.Name.Name != "content" {
			continue
		}
		content, ok := PlainStringLiteral(a.Value)
		if !ok {
			return nil
		}
		inner, err := insertTenantDrops(content, d.depth+1)
		if err != nil {
			return fmt.Errorf("import.string %q: %w", b.Label, err)
		}
		if inner != content {
			d.splices = append(d.splices, dropSplice{
				start: ast.StartPos(a.Value).Position().Offset,
				end:   ast.EndPos(a.Value).Position().Offset + 1,
				text:  alloysyntax.QuoteString(inner),
			})
		}
	}
	return nil
}

// isLiteralLabelmap reports whether rule b's action is the string literal
// "labelmap". A computed action that resolves to labelmap gets no drop, so
// the lint refuses it.
func isLiteralLabelmap(b *ast.BlockStmt) bool {
	act, ok := stringLiteral(attrsOf(b)["action"])
	return ok && act == "labelmap"
}

// needsTenantDrop reports whether labelmap rule b may write __tenant_id__
// under a name it does not spell out: its replacement is the default "$1",
// is not a plain string literal, or holds a "$" reference. A literal
// replacement without "$" names the one label it writes: no drop, since
// one would only discard a tenant an earlier rule set (and a literal
// __tenant_id__ replacement is refused by the lint whatever follows it).
func needsTenantDrop(b *ast.BlockStmt) bool {
	rep := attrsOf(b)["replacement"]
	if rep == nil {
		return true
	}
	s, ok := PlainStringLiteral(rep)
	return !ok || strings.Contains(s, "$")
}

// nextRule is the first block named `rule` after stmts[i], or nil.
func nextRule(stmts ast.Body, i int) *ast.BlockStmt {
	for _, st := range stmts[i+1:] {
		if b, ok := st.(*ast.BlockStmt); ok && strings.Join(b.Name, ".") == "rule" {
			return b
		}
	}
	return nil
}

// isTenantDrop reports whether b is exactly tenantDropRule: a rule with
// two attributes, the literals action = "labeldrop" and regex =
// "__tenant_id__" (Alloy anchors the regex, so it drops that label and no
// other), and nothing else.
func isTenantDrop(b *ast.BlockStmt) bool {
	if b == nil || len(b.Body) != 2 {
		return false
	}
	got := map[string]string{}
	for _, st := range b.Body {
		a, ok := st.(*ast.AttributeStmt)
		if !ok {
			return false
		}
		s, ok := PlainStringLiteral(a.Value)
		if !ok {
			return false
		}
		got[a.Name.Name] = s
	}
	return got["action"] == "labeldrop" && got["regex"] == tenantLabel
}
