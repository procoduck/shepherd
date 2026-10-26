package validate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/token"
)

// Label names taken from the pipeline's own text. Most loki.process stages
// read and write the extracted map, which becomes a label only through
// stage.labels (whose keys are checked). Two stages name labels after
// capture groups instead: stage.regex and stage.pattern with
// labels_from_groups = true set a label for each named group (Alloy v1.21.2
// loki/process/stages/regex.go and pattern.go). The group names come from
// the expression, so strict tenant mode reads it: a group named
// __tenant_id__ would set Loki's tenant label from the log line.
//
// What else turns data or text into a label name was checked against the
// v1.21.2 sources: the other stages write the extracted map (json, logfmt,
// geoip, eventlogmessage, windowsevent, template, replace) or structured
// metadata (structured_metadata, its regex included), or set fixed names
// (cri and docker's "stream"); stage.pack only removes labels;
// loki.secretfilter's origin_label only reads one (checked below);
// loki.source.syslog's label_structured_data names labels
// __syslog_message_sd_*; loki.source.podlogs and loki.source.kubernetes drop
// every reserved label but __meta_* ones (kubetail.NewTarget), whatever
// preserve_discovered_labels says.

// groupLabelStages are the stages whose labels_from_groups names labels
// after capture groups, with the attribute holding the expression and how
// to read its group names.
var groupLabelStages = map[string]struct {
	attr   string
	groups func(string) ([]string, error)
}{
	"stage.regex":   {attr: "expression", groups: regexGroupNames},
	"stage.pattern": {attr: "pattern", groups: patternCaptureNames},
}

// regexGroupNames is a Go regular expression's named groups (Alloy compiles
// stage.regex's expression with regexp.Compile).
func regexGroupNames(expr string) ([]string, error) {
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range re.SubexpNames() {
		if n != "" {
			out = append(out, n)
		}
	}
	return out, nil
}

// patternIdent is a capture name in Loki's pattern syntax: <name>, where
// <_> captures nothing.
var patternIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// patternCaptureNames is a Loki pattern's named captures (`<name>`). Text
// between < and > that is not an identifier is literal in Loki's lexer; it
// is reported as a capture too when it names __tenant_id__ with spaces
// around, so no spelling of that name passes. An unclosed < is refused.
func patternCaptureNames(p string) ([]string, error) {
	var out []string
	for rest := p; ; {
		i := strings.IndexByte(rest, '<')
		if i < 0 {
			return out, nil
		}
		j := strings.IndexByte(rest[i+1:], '>')
		if j < 0 {
			return nil, fmt.Errorf("an unclosed < in the pattern")
		}
		inner := rest[i+1 : i+1+j]
		switch name := strings.TrimSpace(inner); {
		case name == "_":
		case patternIdent.MatchString(name):
			out = append(out, name)
		}
		rest = rest[i+1+j+1:]
	}
}

// groupLabels checks a stage.regex or stage.pattern block b (named name).
func (l *tenantLinter) groupLabels(b *ast.BlockStmt, name string, sc scope) {
	st, ok := groupLabelStages[name]
	if !ok {
		return
	}
	attrs := attrsOf(b)
	flag := attrs["labels_from_groups"]
	if flag == nil {
		return
	}
	lit, ok := unparen(flag).(*ast.LiteralExpr)
	if !ok || lit.Kind != token.BOOL {
		l.refuse(flag, name+" labels_from_groups cannot be verified and may name labels after capture groups — "+
			"strict tenant mode refuses it; write true or false")
		return
	}
	if lit.Value != "true" {
		return
	}
	expr := attrs[st.attr]
	if expr == nil {
		return
	}
	texts, ok := l.resolve(expr, sc, 0)
	if !ok {
		l.refuse(expr, fmt.Sprintf("%s with labels_from_groups names labels after the groups of its %s, which "+
			"cannot be verified — strict tenant mode refuses it; write the %s as a literal", name, st.attr, st.attr))
		return
	}
	for _, t := range texts {
		if !l.budget.charge(1 + len(t)/64) {
			return
		}
		groups, err := st.groups(t)
		if err != nil {
			l.refuse(expr, fmt.Sprintf("%s with labels_from_groups: the %s does not parse (%v), so its group names "+
				"cannot be verified — strict tenant mode refuses it", name, st.attr, err))
			return
		}
		if hasTenantLabel(groups) {
			l.refuse(expr, fmt.Sprintf("%s with labels_from_groups sets %s from the log line (a group of that name) — "+
				"strict tenant mode refuses it", name, tenantLabel))
			return
		}
	}
}

// secretfilterOrigin checks loki.secretfilter's origin_label: the label it
// reads to count redactions by origin. It never writes it, but strict tenant
// mode accepts only a literal other than __tenant_id__.
func (l *tenantLinter) secretfilterOrigin(b *ast.BlockStmt, name string, sc scope) {
	if name != "loki.secretfilter" {
		return
	}
	v := attrsOf(b)["origin_label"]
	if v == nil {
		return
	}
	labels, ok := l.resolve(v, sc, 0)
	if !ok {
		l.refuse(v, "loki.secretfilter origin_label cannot be verified — strict tenant mode refuses it; write a literal label name")
		return
	}
	if hasTenantLabel(labels) {
		l.refuse(v, "loki.secretfilter origin_label is "+tenantLabel+" — strict tenant mode refuses it")
	}
}
