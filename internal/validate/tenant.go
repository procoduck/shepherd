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

// tenantLabel is Loki's reserved label that overrides loki.write's tenant_id
// for an entry (Alloy v1.20.1 internal/component/common/loki/client:
// ReservedLabelTenantID, read by getTenantID). Label names are
// case-sensitive. prometheus.remote_write has no such label: Mimir takes the
// tenant from the X-Scope-OrgID header only.
const tenantLabel = "__tenant_id__"

// tenantAttrs are the places, besides an X-Scope-OrgID header and the
// __tenant_id__ label, where an Alloy component is given a tenant to write
// (or sync rules) under: component name -> the LAST block of the path inside
// the component ("" for the component's own body; any nesting above it, such
// as loki.process's stage.match, is allowed) -> attribute. Each is in the
// component schema (tenant_schema_test.go pins that). Azure AD's tenant_id
// attributes (discovery.azure, prometheus.remote_write's azuread block) name
// an identity tenant, not a data tenant, and are deliberately absent.
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

// corsHeaderLists are attributes ending in "headers" that list header NAMES
// a receiver allows (CORS), not headers sent: they carry no tenant.
var corsHeaderLists = map[string]bool{"allowed_headers": true, "exposed_headers": true}

// isHeadersMapAttr reports whether an attribute called name is a map of
// request headers the lint reads X-Scope-OrgID from: any attribute whose
// name ends in "headers" (headers, http_headers, …) except the CORS lists.
// tenant_schema_test.go enumerates every such map in the pinned schema.
func isHeadersMapAttr(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, "headers") && !corsHeaderLists[n]
}

// errNonLiteralTenant is the warning for a tenant the lint cannot read.
const errNonLiteralTenant = "tenant set from a non-literal expression — cannot be verified in strict mode"

// maxFolded bounds how many values one expression may fold to (a
// concatenation of several many-valued declare arguments).
const maxFolded = 64

// TenantLint is strict tenant mode's pipeline check (spec §8): with p.Strict,
// it refuses every literal tenant in content the org does not own, where
// Alloy sets a tenant —
//
//   - an X-Scope-OrgID key (any case) of any headers map (`headers`,
//     `http_headers`, …: prometheus.remote_write, loki.write,
//     pyroscope.write, the rules components, the otelcol.exporter.*
//     clients); a list value is checked element by element;
//   - an otelcol.auth.headers `header` block whose key is X-Scope-OrgID;
//   - loki.write's endpoint tenant_id, loki.rules.kubernetes' and
//     mimir.rules.kubernetes' tenant_id, and loki.process stage.tenant's
//     value at any nesting (stage.match);
//   - Loki's reserved __tenant_id__ label wherever a labels map sets it
//     (stage.static_labels, a loki.source.* labels map, …) and as a relabel
//     rule's target_label.
//
// A value is read when it is a string literal, a concatenation of readable
// values, or a declare argument whose default and every value it is given
// in the text are readable. Anything else — sys.env(…), a reference, a
// headers map that is not an object literal, stage.tenant's label/source,
// stage.labels, a relabel replacement with a regex reference — is not
// refused but reported as a warning (Diagnostic.Warning). It is best-effort,
// not containment: see spec §8 for what text analysis cannot see. Content
// that does not parse yields nothing — Stage 1 reports it. Positions are
// content's own. Stage is 1.
func TenantLint(content string, p TenantPolicy) []Diagnostic {
	if !p.Strict {
		return nil
	}
	l := newTenantLinter(content, p)
	if l == nil {
		return nil
	}
	return l.diags
}

// TenantLiterals lists every tenant content writes to that the lint can
// read, whatever the org's policy: what an organisation being given a tenant
// must not already have another organisation's enabled pipeline writing to.
// Content that does not parse lists nothing.
func TenantLiterals(content string) []string {
	l := newTenantLinter(content, TenantPolicy{Strict: true, Owns: func(string) bool { return true }})
	if l == nil {
		return nil
	}
	return l.literals
}

func newTenantLinter(content string, p TenantPolicy) *tenantLinter {
	file, err := parser.ParseFile("<pipeline>", []byte(content))
	if err != nil {
		return nil
	}
	l := &tenantLinter{policy: p, declares: map[string]*declareInfo{}}
	l.collectDeclares(file.Body)
	l.body(file.Body, scope{})
	return l
}

type tenantLinter struct {
	policy   TenantPolicy
	diags    []Diagnostic
	literals []string
	declares map[string]*declareInfo
}

// declareInfo is one declare block's arguments: each one's default, and
// every value an instantiation of the declare in the text gives it.
type declareInfo struct {
	defaults map[string]ast.Expr
	given    map[string][]ast.Expr
}

// scope is where the walk is: the nearest enclosing component the lint has
// rules for and the block path inside it, and the nearest enclosing declare.
type scope struct {
	component string
	path      []string
	declare   string
}

func (s scope) last() string {
	if len(s.path) == 0 {
		return ""
	}
	return s.path[len(s.path)-1]
}

func (s scope) child(name string) scope {
	path := make([]string, len(s.path), len(s.path)+1)
	copy(path, s.path)
	return scope{component: s.component, path: append(path, name), declare: s.declare}
}

// collectDeclares records every declare block's arguments, then every
// instantiation of a declare (a block named by its label) and the attributes
// it sets.
func (l *tenantLinter) collectDeclares(stmts ast.Body) {
	var find func(ast.Body)
	find = func(b ast.Body) {
		for _, st := range b {
			blk, ok := st.(*ast.BlockStmt)
			if !ok {
				continue
			}
			if strings.Join(blk.Name, ".") == "declare" && blk.Label != "" {
				info := &declareInfo{defaults: map[string]ast.Expr{}, given: map[string][]ast.Expr{}}
				for _, inner := range blk.Body {
					arg, ok := inner.(*ast.BlockStmt)
					if !ok || strings.Join(arg.Name, ".") != "argument" {
						continue
					}
					var def ast.Expr
					for _, a := range arg.Body {
						if at, ok := a.(*ast.AttributeStmt); ok && at.Name.Name == "default" {
							def = at.Value
						}
					}
					info.defaults[arg.Label] = def
				}
				l.declares[blk.Label] = info
			}
			find(blk.Body)
		}
	}
	find(stmts)
	var inst func(ast.Body)
	inst = func(b ast.Body) {
		for _, st := range b {
			blk, ok := st.(*ast.BlockStmt)
			if !ok {
				continue
			}
			if info, ok := l.declares[strings.Join(blk.Name, ".")]; ok {
				for _, a := range blk.Body {
					if at, ok := a.(*ast.AttributeStmt); ok {
						info.given[at.Name.Name] = append(info.given[at.Name.Name], at.Value)
					}
				}
			}
			inst(blk.Body)
		}
	}
	inst(stmts)
}

func (l *tenantLinter) body(stmts ast.Body, sc scope) {
	for _, st := range stmts {
		switch n := st.(type) {
		case *ast.BlockStmt:
			name := strings.Join(n.Name, ".")
			switch {
			case name == "declare":
				l.body(n.Body, scope{declare: n.Label})
			case sc.component == "" && len(sc.path) == 0 && (isTenantComponent(name) || name == "otelcol.auth.headers"):
				l.body(n.Body, scope{component: name, declare: sc.declare})
			default:
				if sc.component == "otelcol.auth.headers" && len(sc.path) == 0 && name == "header" {
					l.authHeader(n, sc)
				}
				if name == "rule" {
					l.relabelRule(n, sc)
				}
				l.body(n.Body, sc.child(name))
			}
		case *ast.AttributeStmt:
			l.attribute(n, sc)
		}
	}
}

func isTenantComponent(name string) bool {
	_, a := tenantAttrs[name]
	_, b := tenantDynamic[name]
	return a || b
}

func (l *tenantLinter) attribute(a *ast.AttributeStmt, sc scope) {
	name := a.Name.Name
	if isHeadersMapAttr(name) {
		l.headers(a.Value, sc)
		return
	}
	l.labelMaps(a.Value, sc)
	if sc.component == "" {
		return
	}
	where := strings.Join(append(append([]string{sc.component}, sc.path...), name), " ")
	if attr, ok := tenantAttrs[sc.component][sc.last()]; ok && attr == name {
		l.tenantValue(a.Value, where, sc)
		return
	}
	for _, dyn := range tenantDynamic[sc.component][sc.last()] {
		if dyn == name {
			l.warn(a.Value, where+" picks the tenant from the data at run time — cannot be verified in strict mode")
		}
	}
}

// headers checks a headers attribute's value. A string or list literal is
// not a header map (beyla's policy action, kafka's header list) and carries
// no tenant; an object literal is checked key by key, a list value element
// by element (http_headers); anything else might carry the header and cannot
// be read.
func (l *tenantLinter) headers(v ast.Expr, sc scope) {
	switch e := unparen(v).(type) {
	case *ast.LiteralExpr, *ast.ArrayExpr:
		return
	case *ast.ObjectExpr:
		for _, f := range e.Fields {
			if !strings.EqualFold(f.Name.Name, tenantHeader) {
				continue
			}
			if arr, ok := unparen(f.Value).(*ast.ArrayExpr); ok {
				for _, el := range arr.Elements {
					l.tenantValue(el, tenantHeader+" header", sc)
				}
				continue
			}
			l.tenantValue(f.Value, tenantHeader+" header", sc)
		}
	default:
		l.warn(v, "headers set from a non-literal expression may carry "+tenantHeader+
			" — cannot be verified in strict mode")
	}
}

// labelMaps finds every object literal in v (labels, values, external_labels,
// targets, …) that sets Loki's __tenant_id__ label. stage.labels' values map
// names an extracted field, not a value, so there it is a warning.
func (l *tenantLinter) labelMaps(v ast.Expr, sc scope) {
	switch e := unparen(v).(type) {
	case *ast.ObjectExpr:
		for _, f := range e.Fields {
			if f.Name.Name == tenantLabel {
				if sc.last() == "stage.labels" {
					l.warn(f.Value, "stage.labels sets "+tenantLabel+" from an extracted field at run time — cannot be "+
						"verified in strict mode")
				} else {
					l.tenantValue(f.Value, tenantLabel+" label", sc)
				}
			}
			l.labelMaps(f.Value, sc)
		}
	case *ast.ArrayExpr:
		for _, el := range e.Elements {
			l.labelMaps(el, sc)
		}
	}
}

// relabelRule checks a relabel `rule` block (loki.relabel,
// discovery.relabel, prometheus.relabel, a source's rule block) that writes
// the __tenant_id__ label: a readable replacement is a tenant; a regex
// reference ($1), the default replacement or an action other than replace
// comes from the data.
func (l *tenantLinter) relabelRule(b *ast.BlockStmt, sc scope) {
	var target, replacement, action ast.Expr
	for _, st := range b.Body {
		a, ok := st.(*ast.AttributeStmt)
		if !ok {
			continue
		}
		switch a.Name.Name {
		case "target_label":
			target = a.Value
		case "replacement":
			replacement = a.Value
		case "action":
			action = a.Value
		}
	}
	if target == nil {
		return
	}
	if t, ok := stringLiteral(target); !ok || t != tenantLabel {
		return
	}
	fromData := "a relabel rule sets " + tenantLabel + " from its source labels at run time — cannot be " +
		"verified in strict mode"
	if action != nil {
		if act, ok := stringLiteral(action); !ok || act != "replace" {
			l.warn(action, fromData)
			return
		}
	}
	if replacement == nil {
		l.warn(target, fromData)
		return
	}
	if values, ok := l.resolve(replacement, sc); ok {
		for _, s := range values {
			if strings.Contains(s, "$") {
				l.warn(replacement, fromData)
				return
			}
		}
	}
	l.tenantValue(replacement, "relabel rule "+tenantLabel, sc)
}

// authHeader checks one otelcol.auth.headers `header` block: when its key
// is X-Scope-OrgID, its value is a tenant. from_context/from_attribute
// read it from the request at run time.
func (l *tenantLinter) authHeader(b *ast.BlockStmt, sc scope) {
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
	keys, ok := l.resolve(key, sc)
	if !ok {
		l.warn(key, "otelcol.auth.headers header key set from a non-literal expression may be "+
			tenantHeader+" — cannot be verified in strict mode")
		return
	}
	isTenant := false
	for _, k := range keys {
		isTenant = isTenant || strings.EqualFold(k, tenantHeader)
	}
	if !isTenant {
		return
	}
	if value != nil {
		l.tenantValue(value, "otelcol.auth.headers "+tenantHeader+" header", sc)
	}
	if dynamic != nil {
		l.warn(dynamic, "otelcol.auth.headers takes "+tenantHeader+" from the request at run time — "+
			"cannot be verified in strict mode")
	}
}

// tenantValue refuses each tenant v can be that the org does not own, and
// warns when it cannot read v. X-Scope-OrgID may name several tenants joined
// by "|" (a federated read); each must be owned.
func (l *tenantLinter) tenantValue(v ast.Expr, where string, sc scope) {
	values, ok := l.resolve(v, sc)
	if !ok {
		l.warn(v, errNonLiteralTenant)
		return
	}
	for _, s := range values {
		for _, t := range strings.Split(s, "|") {
			l.literals = append(l.literals, t)
			if !l.policy.owns(t) {
				l.add(v, false, fmt.Sprintf("%s: tenant %q is not one this organisation owns; in strict tenant "+
					"mode a pipeline may only write to the organisation's own tenants", where, t))
			}
		}
	}
}

// resolve returns every string v can be, when the lint can read it: a
// string literal; a concatenation of readable values; or
// `argument.<name>.value` of the enclosing declare, whose default (if any)
// and every value an instantiation gives it are readable, and of which there
// is at least one.
func (l *tenantLinter) resolve(v ast.Expr, sc scope) ([]string, bool) {
	switch e := unparen(v).(type) {
	case *ast.LiteralExpr:
		s, ok := stringLiteral(e)
		if !ok {
			return nil, false
		}
		return []string{s}, true
	case *ast.BinaryExpr:
		if e.Kind != token.ADD {
			return nil, false
		}
		left, ok := l.resolve(e.Left, sc)
		if !ok {
			return nil, false
		}
		right, ok := l.resolve(e.Right, sc)
		if !ok || len(left)*len(right) > maxFolded {
			return nil, false
		}
		out := make([]string, 0, len(left)*len(right))
		for _, a := range left {
			for _, b := range right {
				out = append(out, a+b)
			}
		}
		return out, true
	case *ast.AccessExpr:
		return l.resolveArgument(e, sc)
	}
	return nil, false
}

// resolveArgument resolves `argument.<name>.value` of the enclosing
// declare. A default is read inside the declare; a value an instantiation
// gives is read where it is written, outside it.
func (l *tenantLinter) resolveArgument(e *ast.AccessExpr, sc scope) ([]string, bool) {
	name, ok := argumentRef(e)
	if !ok || sc.declare == "" {
		return nil, false
	}
	info := l.declares[sc.declare]
	if info == nil {
		return nil, false
	}
	var out []string
	read := 0
	if def := info.defaults[name]; def != nil {
		vals, ok := l.resolve(def, scope{declare: sc.declare})
		if !ok {
			return nil, false
		}
		out = append(out, vals...)
		read++
	}
	for _, x := range info.given[name] {
		vals, ok := l.resolve(x, scope{})
		if !ok || len(out)+len(vals) > maxFolded {
			return nil, false
		}
		out = append(out, vals...)
		read++
	}
	if read == 0 {
		return nil, false
	}
	return out, true
}

// argumentRef reports the argument name of `argument.<name>.value`.
func argumentRef(e *ast.AccessExpr) (string, bool) {
	if e.Name.Name != "value" {
		return "", false
	}
	mid, ok := e.Value.(*ast.AccessExpr)
	if !ok {
		return "", false
	}
	root, ok := mid.Value.(*ast.IdentifierExpr)
	if !ok || root.Ident.Name != "argument" {
		return "", false
	}
	return mid.Name.Name, true
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
