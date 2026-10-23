package validate

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/grafana/alloy/syntax/ast"
	"github.com/grafana/alloy/syntax/parser"
	"github.com/grafana/alloy/syntax/token"
)

// TenantPolicy is strict tenant mode as the validation gate sees it. The zero
// value is "not strict": TenantLint finds nothing and ValidatePipeline is
// exactly Stages 1-2.
type TenantPolicy struct {
	// Strict turns the tenant lint on (the org's strict_tenants flag, or the
	// server-wide tenants.require_owned setting).
	Strict bool
	// Owns reports whether the org owns tenant (gateway.OrgOwnsTenant over
	// the org's tenants). Required when Strict is set; a nil Owns owns
	// nothing.
	Owns func(tenant string) bool
}

func (p TenantPolicy) owns(tenant string) bool {
	return p.Owns != nil && p.Owns(tenant)
}

// tenantHeader is the header Grafana Mimir, Loki, Tempo and Pyroscope read a
// tenant from. Header names are case-insensitive.
const tenantHeader = "X-Scope-OrgID"

// tenantAttrs are the places, besides an X-Scope-OrgID header, where an Alloy
// component is given a tenant to write (or sync rules) under: component name
// -> block path inside the component ("" for the component's own body) ->
// attribute. Each is in the component schema (internal/schema; the
// tenant_lint spec pins that). Azure AD's tenant_id attributes
// (discovery.azure, prometheus.remote_write's azuread block) name an identity
// tenant, not a data tenant, and are deliberately absent.
var tenantAttrs = map[string]map[string]string{
	"loki.write":             {"endpoint": "tenant_id"},
	"loki.rules.kubernetes":  {"": "tenant_id"},
	"mimir.rules.kubernetes": {"": "tenant_id"},
	"loki.process":           {"stage.tenant": "value"},
}

// tenantDynamic are attributes that pick a tenant at run time from the data
// itself — nothing the text says can be checked, so each one is a warning in
// strict mode.
var tenantDynamic = map[string]map[string][]string{
	"loki.process": {"stage.tenant": {"label", "source"}},
}

// errNonLiteralTenant is the warning for a tenant the lint cannot read.
const errNonLiteralTenant = "tenant set from a non-literal expression — cannot be verified in strict mode"

// TenantLint is strict tenant mode's pipeline check (spec §8): with p.Strict,
// it refuses every literal tenant in content the org does not own, where
// Alloy sets a tenant —
//
//   - an X-Scope-OrgID key (any case) of any `headers = { … }` object, which
//     is how prometheus.remote_write, loki.write, pyroscope.write and the
//     otelcol.exporter.* clients send one;
//   - an otelcol.auth.headers `header` block whose key is X-Scope-OrgID;
//   - loki.write's endpoint tenant_id, loki.rules.kubernetes' and
//     mimir.rules.kubernetes' tenant_id, and loki.process stage.tenant's
//     value.
//
// A value it cannot read — sys.env(…), a reference, a concatenation, a
// headers map that is not an object literal, stage.tenant's label/source —
// is not refused but reported as a warning (Diagnostic.Warning). It is
// best-effort, not containment: anything that computes the tenant at run
// time (relabelling a `__tenant_id__` label, a module imported from
// outside the pipeline, a header a proxy adds) is outside what text analysis
// can see; the strict rule on destinations, bindings and routes is the part
// that holds. Content that does not parse yields nothing — Stage 1 reports
// it. Positions are content's own. Stage is 1.
func TenantLint(content string, p TenantPolicy) []Diagnostic {
	if !p.Strict {
		return nil
	}
	file, err := parser.ParseFile("<pipeline>", []byte(content))
	if err != nil {
		return nil
	}
	l := &tenantLinter{policy: p}
	l.body(file.Body, "", "")
	return l.diags
}

type tenantLinter struct {
	policy TenantPolicy
	diags  []Diagnostic
}

// body walks stmts. component is the nearest enclosing block that is a
// component with a tenant attribute ("" outside one), path the block path
// inside it.
func (l *tenantLinter) body(stmts ast.Body, component, path string) {
	for _, st := range stmts {
		switch n := st.(type) {
		case *ast.BlockStmt:
			name := strings.Join(n.Name, ".")
			switch {
			case isTenantComponent(name):
				l.body(n.Body, name, "")
			case name == "otelcol.auth.headers":
				l.body(n.Body, name, "")
			case component != "":
				if component == "otelcol.auth.headers" && path == "" && name == "header" {
					l.authHeader(n)
				}
				l.body(n.Body, component, joinPath(path, name))
			default:
				l.body(n.Body, "", "")
			}
		case *ast.AttributeStmt:
			l.attribute(n, component, path)
		}
	}
}

func isTenantComponent(name string) bool {
	_, a := tenantAttrs[name]
	_, b := tenantDynamic[name]
	return a || b
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "/" + name
}

func (l *tenantLinter) attribute(a *ast.AttributeStmt, component, path string) {
	name := a.Name.Name
	if name == "headers" {
		l.headers(a.Value)
		return
	}
	if component == "" {
		return
	}
	if attr, ok := tenantAttrs[component][path]; ok && attr == name {
		l.tenantValue(a.Value, fmt.Sprintf("%s %s", component, qualified(path, name)))
		return
	}
	for _, dyn := range tenantDynamic[component][path] {
		if dyn == name {
			l.warn(a.Value, fmt.Sprintf("%s %s picks the tenant from the data at run time — cannot be "+
				"verified in strict mode", component, qualified(path, name)))
		}
	}
}

func qualified(path, name string) string {
	if path == "" {
		return name
	}
	return strings.ReplaceAll(path, "/", " ") + " " + name
}

// headers checks a `headers` attribute's value. A string literal is some
// other attribute called headers (beyla's policy action, say) and carries no
// tenant; an object literal is checked key by key; anything else might carry
// the header and cannot be read.
func (l *tenantLinter) headers(v ast.Expr) {
	switch e := unparen(v).(type) {
	case *ast.LiteralExpr:
		return
	case *ast.ObjectExpr:
		for _, f := range e.Fields {
			if strings.EqualFold(f.Name.Name, tenantHeader) {
				l.tenantValue(f.Value, tenantHeader+" header")
			}
		}
	default:
		l.warn(v, "headers set from a non-literal expression may carry "+tenantHeader+
			" — cannot be verified in strict mode")
	}
}

// authHeader checks one otelcol.auth.headers `header` block: when its key
// is X-Scope-OrgID, its value is a tenant. from_context/from_attribute
// read it from the request at run time.
func (l *tenantLinter) authHeader(b *ast.BlockStmt) {
	var key, value ast.Expr
	var dynamic ast.Expr
	for _, st := range b.Body {
		a, ok := st.(*ast.AttributeStmt)
		if !ok {
			continue
		}
		switch a.Name.Name {
		case "key":
			key = a.Value
		case "value":
			value = a.Value
		case "from_context", "from_attribute":
			dynamic = a.Value
		}
	}
	if key == nil {
		return
	}
	k, ok := stringLiteral(key)
	if !ok {
		l.warn(key, "otelcol.auth.headers header key set from a non-literal expression may be "+
			tenantHeader+" — cannot be verified in strict mode")
		return
	}
	if !strings.EqualFold(k, tenantHeader) {
		return
	}
	if value != nil {
		l.tenantValue(value, "otelcol.auth.headers "+tenantHeader+" header")
	}
	if dynamic != nil {
		l.warn(dynamic, "otelcol.auth.headers takes "+tenantHeader+" from the request at run time — "+
			"cannot be verified in strict mode")
	}
}

// tenantValue refuses a literal tenant the org does not own, and warns on
// one it cannot read. X-Scope-OrgID may name several tenants joined by "|"
// (a federated read); each must be owned.
func (l *tenantLinter) tenantValue(v ast.Expr, where string) {
	s, ok := stringLiteral(v)
	if !ok {
		l.warn(v, errNonLiteralTenant)
		return
	}
	for _, t := range strings.Split(s, "|") {
		if !l.policy.owns(t) {
			l.add(v, false, fmt.Sprintf("%s: tenant %q is not one this organisation owns; in strict tenant "+
				"mode a pipeline may only write to the organisation's own tenants", where, t))
		}
	}
}

// stringLiteral returns v's value when it is a plain string literal.
func stringLiteral(v ast.Expr) (string, bool) {
	lit, ok := unparen(v).(*ast.LiteralExpr)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return s, true
}

func unparen(v ast.Expr) ast.Expr {
	for {
		p, ok := v.(*ast.ParenExpr)
		if !ok {
			return v
		}
		v = p.Inner
	}
}

func (l *tenantLinter) warn(at ast.Node, msg string) { l.add(at, true, msg) }

func (l *tenantLinter) add(at ast.Node, warning bool, msg string) {
	pos := ast.StartPos(at).Position()
	l.diags = append(l.diags, Diagnostic{Line: pos.Line, Col: pos.Column, Message: msg, Stage: 1, Warning: warning})
}

// hasErrors reports whether diags holds a refusal (a non-warning
// diagnostic).
func hasErrors(diags []Diagnostic) bool {
	for _, d := range diags {
		if !d.Warning {
			return true
		}
	}
	return false
}
