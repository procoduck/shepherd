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

// tenantAttrs are the places, besides headers, otelcol.auth.* and the
// __tenant_id__ label, where an Alloy component is given a tenant to write
// (or sync rules) under: component name -> the LAST block of the path inside
// the component ("" for the component's own body; any nesting above it, such
// as loki.process's stage.match, is allowed) -> attribute. Azure AD's
// tenant_id attributes name an identity tenant, not a data tenant
// (tenant_schema_test.go classifies every attribute named like a tenant or a
// header).
var tenantAttrs = map[string]map[string]string{
	"loki.write":             {"endpoint": "tenant_id"},
	"loki.rules.kubernetes":  {"": "tenant_id"},
	"mimir.rules.kubernetes": {"": "tenant_id"},
	"loki.process":           {"stage.tenant": "value"},
}

// tenantFromData are attributes that pick a tenant at run time from the data
// itself (a log line's label or extracted field). In strict mode they are
// refused: data pushed by a client (loki.source.api, a receiver) can carry
// any tenant.
var tenantFromData = map[string]map[string][]string{
	"loki.process": {"stage.tenant": {"label", "source"}},
}

// corsHeaderLists are attributes ending in "headers" that list header NAMES
// a receiver allows (CORS) or a Kafka receiver extracts, not headers sent:
// they carry no tenant.
var corsHeaderLists = map[string]bool{"allowed_headers": true, "exposed_headers": true}

// isHeadersMapAttr reports whether an attribute called name is a map of
// request headers the lint reads X-Scope-OrgID from: any attribute whose
// name ends in "headers" (headers, http_headers, …) except the CORS lists,
// and proxy_connect_header.
func isHeadersMapAttr(name string) bool {
	n := strings.ToLower(name)
	return (strings.HasSuffix(n, "headers") && !corsHeaderLists[n]) || n == "proxy_connect_header"
}

// maxFolded bounds how many values one expression may fold to, maxDepth how
// many declare argument hops and nested import.string modules the lint
// follows. Past either it cannot verify, so strict mode refuses.
const (
	maxFolded = 64
	maxDepth  = 8
)

// TenantLint is strict tenant mode's pipeline check (spec §8). With p.Strict
// it refuses, wherever Alloy sets a tenant, every tenant content can write
// to that the org does not own, and everything there it cannot verify:
//
//   - an X-Scope-OrgID key (any case) of any headers map (headers,
//     http_headers, proxy_connect_header, …), each list element; a headers
//     value that is not an object literal;
//   - an otelcol.auth.headers header whose key is (or may be) X-Scope-OrgID,
//     from_context/from_attribute included; an otelcol.auth.bearer whose
//     header is (or may be) X-Scope-OrgID — its token is the tenant when the
//     scheme is empty, and anything else is refused;
//   - loki.write's endpoint tenant_id, the rules components' tenant_id,
//     loki.process stage.tenant at any nesting (label/source too);
//   - Loki's reserved __tenant_id__ label in any labels map (stage.labels
//     included) and from a relabel rule (a target_label that is or may be
//     __tenant_id__; a labelmap replacement naming it);
//   - every import.* block except import.string with readable content,
//     whose content is linted the same way.
//
// A value is readable when it is a string literal, a concatenation of
// readable values, or a declare argument whose default and every value it is
// given (in the scope it is given in) are readable. A non-strict policy
// finds nothing. Content that does not parse yields nothing — Stage 1
// reports it. Positions are content's own. Stage is 1. The lint is defence
// in depth: the backend's own tenant authentication is the boundary.
func TenantLint(content string, p TenantPolicy) []Diagnostic {
	if !p.Strict {
		return nil
	}
	l := newTenantLinter(content, p, nil, 0)
	if l == nil {
		return nil
	}
	return l.diags
}

// TenantLiterals lists every tenant content writes to that the lint can
// read, whatever the org's policy: what pipeline_tenants records for an
// enabled pipeline, and what an organisation being given a tenant must not
// already have another organisation writing to. Content that does not parse
// lists nothing.
func TenantLiterals(content string) []string {
	l := newTenantLinter(content, TenantPolicy{Strict: true, Owns: func(string) bool { return true }}, nil, 0)
	if l == nil {
		return nil
	}
	return l.literals
}

// resolved is a value as the lint read it: its possible strings, or ok=false.
type resolved struct {
	values []string
	ok     bool
}

func newTenantLinter(content string, p TenantPolicy, external map[string]map[string][]resolved, depth int) *tenantLinter {
	file, err := parser.ParseFile("<pipeline>", []byte(content))
	if err != nil {
		return nil
	}
	l := &tenantLinter{policy: p, declares: map[string]*declareInfo{}, depth: depth}
	l.collectDeclares(file.Body, external)
	l.body(file.Body, scope{})
	return l
}

type tenantLinter struct {
	policy   TenantPolicy
	diags    []Diagnostic
	literals []string
	declares map[string]*declareInfo
	depth    int
}

// declareInfo is one declare block's arguments: each one's default, and
// every value an instantiation gives it, with the declare it is given in.
type declareInfo struct {
	defaults map[string]ast.Expr
	given    map[string][]givenValue
	// external are values an instantiation outside this text gives (an
	// import.string module's declare, instantiated by the importing text).
	external map[string][]resolved
}

type givenValue struct {
	expr    ast.Expr
	declare string
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
// it sets, with the declare the instantiation sits in.
func (l *tenantLinter) collectDeclares(stmts ast.Body, external map[string]map[string][]resolved) {
	var find func(ast.Body)
	find = func(b ast.Body) {
		for _, st := range b {
			blk, ok := st.(*ast.BlockStmt)
			if !ok {
				continue
			}
			if strings.Join(blk.Name, ".") == "declare" && blk.Label != "" {
				info := &declareInfo{defaults: map[string]ast.Expr{}, given: map[string][]givenValue{}, external: external[blk.Label]}
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
	var inst func(ast.Body, string)
	inst = func(b ast.Body, declare string) {
		for _, st := range b {
			blk, ok := st.(*ast.BlockStmt)
			if !ok {
				continue
			}
			name := strings.Join(blk.Name, ".")
			if info, ok := l.declares[name]; ok {
				for _, a := range blk.Body {
					if at, ok := a.(*ast.AttributeStmt); ok {
						info.given[at.Name.Name] = append(info.given[at.Name.Name], givenValue{expr: at.Value, declare: declare})
					}
				}
			}
			if name == "declare" {
				inst(blk.Body, blk.Label)
			} else {
				inst(blk.Body, declare)
			}
		}
	}
	inst(stmts, "")
}

func (l *tenantLinter) body(stmts ast.Body, sc scope) {
	for _, st := range stmts {
		switch n := st.(type) {
		case *ast.BlockStmt:
			name := strings.Join(n.Name, ".")
			atTop := sc.component == "" && len(sc.path) == 0
			switch {
			case name == "declare":
				l.body(n.Body, scope{declare: n.Label})
			case atTop && strings.HasPrefix(name, "import."):
				l.importBlock(n, name, sc, stmts)
			case atTop && (isTenantComponent(name) || name == "otelcol.auth.headers"):
				l.body(n.Body, scope{component: name, declare: sc.declare})
			case atTop && name == "otelcol.auth.bearer":
				l.bearer(n, sc)
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
	_, b := tenantFromData[name]
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
	for _, dyn := range tenantFromData[sc.component][sc.last()] {
		if dyn == name {
			l.refuse(a.Value, where+" picks the tenant from the data at run time, which a client can set — "+
				"strict tenant mode refuses it")
		}
	}
}

// headers checks a headers attribute's value. A string or list literal is
// not a header map (beyla's policy action, kafka's header list); an object
// literal is checked key by key, a list value element by element
// (http_headers); anything else may carry the header and cannot be read.
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
		l.refuse(v, "headers set from an expression may carry "+tenantHeader+" and cannot be verified — "+
			"strict tenant mode refuses it; write the headers as a literal map")
	}
}

// labelMaps finds every object literal in v (labels, values, external_labels,
// targets, …) that sets Loki's __tenant_id__ label. stage.labels' values map
// names an extracted field, so the tenant comes from the data: refused.
func (l *tenantLinter) labelMaps(v ast.Expr, sc scope) {
	switch e := unparen(v).(type) {
	case *ast.ObjectExpr:
		for _, f := range e.Fields {
			if f.Name.Name == tenantLabel {
				if sc.last() == "stage.labels" {
					l.refuse(f.Value, "stage.labels sets "+tenantLabel+" from the log data, which a client can set — "+
						"strict tenant mode refuses it")
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
// discovery.relabel, prometheus.relabel, a source's rule block). Alloy
// v1.20.1 matches the action exactly (common/relabel Action.UnmarshalText,
// lowercase names, default "replace"). A rule that writes __tenant_id__ —
// its target_label is or may be __tenant_id__ for replace, lowercase,
// uppercase or hashmod, or a labelmap whose replacement names it — is
// checked: a readable replacement without a regex reference is a tenant;
// anything else comes from the data and is refused.
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
	act := "replace"
	if action != nil {
		acts, ok := l.resolve(action, sc, 0)
		if !ok || len(acts) != 1 {
			if target != nil || replacement != nil {
				l.refuse(action, "a relabel rule's action cannot be verified — strict tenant mode refuses it")
			}
			return
		}
		act = acts[0]
	}
	fromData := "a relabel rule sets " + tenantLabel + " from its source labels at run time, which a client " +
		"can set — strict tenant mode refuses it"
	switch act {
	case "labelmap":
		if replacement == nil {
			return // "$1": the captured part of an existing label name
		}
		reps, ok := l.resolve(replacement, sc, 0)
		if !ok {
			l.refuse(replacement, "a labelmap replacement cannot be verified — strict tenant mode refuses it")
			return
		}
		for _, r := range reps {
			if strings.Contains(r, tenantLabel) {
				l.refuse(replacement, fromData)
				return
			}
		}
		return
	case "replace", "lowercase", "uppercase", "hashmod":
	default:
		return // keep, drop, labeldrop, labelkeep, keepequal, dropequal write no label value
	}
	if target == nil {
		return
	}
	targets, ok := l.resolve(target, sc, 0)
	if !ok {
		l.refuse(target, "a relabel rule's target_label cannot be verified and may be "+tenantLabel+
			" — strict tenant mode refuses it")
		return
	}
	writes := false
	for _, t := range targets {
		writes = writes || t == tenantLabel
	}
	if !writes {
		return
	}
	if act != "replace" || replacement == nil {
		l.refuse(target, fromData)
		return
	}
	if reps, ok := l.resolve(replacement, sc, 0); ok {
		for _, s := range reps {
			if strings.Contains(s, "$") {
				l.refuse(replacement, fromData)
				return
			}
		}
	}
	l.tenantValue(replacement, "relabel rule "+tenantLabel, sc)
}

// authHeader checks one otelcol.auth.headers `header` block: when its key
// is (or may be) X-Scope-OrgID, its value is a tenant, and from_context /
// from_attribute (read from the request) are refused.
func (l *tenantLinter) authHeader(b *ast.BlockStmt, sc scope) {
	attrs := attrsOf(b)
	key := attrs["key"]
	if key == nil {
		return
	}
	if !l.mayBeTenantHeader(key, sc, "otelcol.auth.headers header key") {
		return
	}
	if v := attrs["value"]; v != nil {
		l.tenantValue(v, "otelcol.auth.headers "+tenantHeader+" header", sc)
	}
	for _, k := range []string{"from_context", "from_attribute"} {
		if v := attrs[k]; v != nil {
			l.refuse(v, "otelcol.auth.headers takes "+tenantHeader+" from the request at run time — strict "+
				"tenant mode refuses it")
		}
	}
}

// bearer checks an otelcol.auth.bearer component: with header set to (or
// possibly) X-Scope-OrgID it sends "<scheme> <token>" there, so with an
// empty scheme the token is the tenant; any other scheme is refused.
func (l *tenantLinter) bearer(b *ast.BlockStmt, sc scope) {
	attrs := attrsOf(b)
	header := attrs["header"]
	if header == nil {
		return // Authorization
	}
	if !l.mayBeTenantHeader(header, sc, "otelcol.auth.bearer header") {
		return
	}
	scheme := attrs["scheme"]
	if scheme == nil {
		l.refuse(header, "otelcol.auth.bearer sends \"Bearer <token>\" as "+tenantHeader+" — strict tenant mode "+
			"refuses it; use otelcol.auth.headers")
		return
	}
	schemes, ok := l.resolve(scheme, sc, 0)
	if !ok || len(schemes) != 1 || schemes[0] != "" {
		l.refuse(scheme, "otelcol.auth.bearer sends a scheme with its token as "+tenantHeader+" — strict tenant "+
			"mode refuses it; use otelcol.auth.headers")
		return
	}
	token := attrs["token"]
	if token == nil {
		l.refuse(header, "otelcol.auth.bearer sends an empty "+tenantHeader+" — strict tenant mode refuses it")
		return
	}
	l.tenantValue(token, "otelcol.auth.bearer "+tenantHeader+" token", sc)
}

// mayBeTenantHeader reports whether a header-name expression is
// X-Scope-OrgID, refusing (and reporting true) one it cannot read.
func (l *tenantLinter) mayBeTenantHeader(v ast.Expr, sc scope, what string) bool {
	names, ok := l.resolve(v, sc, 0)
	if !ok {
		l.refuse(v, what+" cannot be verified and may be "+tenantHeader+" — strict tenant mode refuses it")
		return false
	}
	for _, n := range names {
		if strings.EqualFold(n, tenantHeader) {
			return true
		}
	}
	return false
}

// importBlock handles an import.* block. import.string with readable content
// is parsed and linted with the same rules (its declares' arguments include
// what the importing text gives them); every other import, and content it
// cannot read, is refused: what it loads cannot be verified.
func (l *tenantLinter) importBlock(b *ast.BlockStmt, name string, sc scope, siblings ast.Body) {
	where := fmt.Sprintf("%s %q", name, b.Label)
	if name != "import.string" {
		l.refuse(b, where+" loads configuration from outside the pipeline, which cannot be verified — strict "+
			"tenant mode refuses it; inline the module (declare, or import.string with literal content)")
		return
	}
	content := attrsOf(b)["content"]
	if content == nil {
		return
	}
	texts, ok := l.resolve(content, sc, 0)
	if !ok || len(texts) != 1 {
		l.refuse(content, where+" content cannot be verified — strict tenant mode refuses it")
		return
	}
	if l.depth >= maxDepth {
		l.refuse(content, where+" nests modules too deeply to verify — strict tenant mode refuses it")
		return
	}
	inner := newTenantLinter(texts[0], l.policy, l.moduleArguments(b.Label, siblings), l.depth+1)
	if inner == nil {
		return // Stage 2 / Alloy reports content that does not parse
	}
	l.literals = append(l.literals, inner.literals...)
	for _, d := range inner.diags {
		l.refuse(content, fmt.Sprintf("%s line %d: %s", where, d.Line, d.Message))
	}
}

// moduleArguments resolves, in this text, every value an instantiation of
// one of module's declares (a block named "<module>.<declare>") gives.
func (l *tenantLinter) moduleArguments(module string, body ast.Body) map[string]map[string][]resolved {
	out := map[string]map[string][]resolved{}
	var walk func(ast.Body, string)
	walk = func(b ast.Body, declare string) {
		for _, st := range b {
			blk, ok := st.(*ast.BlockStmt)
			if !ok {
				continue
			}
			name := strings.Join(blk.Name, ".")
			if decl, ok := strings.CutPrefix(name, module+"."); ok && decl != "" {
				if out[decl] == nil {
					out[decl] = map[string][]resolved{}
				}
				for _, a := range blk.Body {
					if at, ok := a.(*ast.AttributeStmt); ok {
						vals, ok := l.resolve(at.Value, scope{declare: declare}, 0)
						out[decl][at.Name.Name] = append(out[decl][at.Name.Name], resolved{values: vals, ok: ok})
					}
				}
			}
			if name == "declare" {
				walk(blk.Body, blk.Label)
			} else {
				walk(blk.Body, declare)
			}
		}
	}
	walk(body, "")
	return out
}

// tenantValue refuses each tenant v can be that the org does not own, and v
// when it cannot read it. X-Scope-OrgID may name several tenants joined by
// "|" (a federated read); each must be owned.
func (l *tenantLinter) tenantValue(v ast.Expr, where string, sc scope) {
	values, ok := l.resolve(v, sc, 0)
	if !ok {
		l.refuse(v, where+": the tenant is set from an expression that cannot be verified — strict tenant mode "+
			"refuses it; write the tenant as a literal")
		return
	}
	for _, s := range values {
		for _, t := range strings.Split(s, "|") {
			l.literals = append(l.literals, t)
			if !l.policy.owns(t) {
				l.refuse(v, fmt.Sprintf("%s: tenant %q is not one this organisation owns; in strict tenant "+
					"mode a pipeline may only write to the organisation's own tenants", where, t))
			}
		}
	}
}

// resolve returns every string v can be, when the lint can read it: a
// string literal; a concatenation of readable values; or
// `argument.<name>.value` of the enclosing declare (resolveArgument).
func (l *tenantLinter) resolve(v ast.Expr, sc scope, depth int) ([]string, bool) {
	if depth > maxDepth {
		return nil, false
	}
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
		left, ok := l.resolve(e.Left, sc, depth)
		if !ok {
			return nil, false
		}
		right, ok := l.resolve(e.Right, sc, depth)
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
		return l.resolveArgument(e, sc, depth)
	}
	return nil, false
}

// resolveArgument resolves `argument.<name>.value` of the enclosing
// declare: its default (read inside the declare) and every value an
// instantiation gives it (read in the declare that instantiation sits in,
// so a value passed down several declare levels is followed). It is
// readable only when every one of those is and there is at least one.
func (l *tenantLinter) resolveArgument(e *ast.AccessExpr, sc scope, depth int) ([]string, bool) {
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
	add := func(vals []string) bool {
		if len(out)+len(vals) > maxFolded {
			return false
		}
		out = append(out, vals...)
		read++
		return true
	}
	if def := info.defaults[name]; def != nil {
		vals, ok := l.resolve(def, scope{declare: sc.declare}, depth+1)
		if !ok || !add(vals) {
			return nil, false
		}
	}
	for _, g := range info.given[name] {
		vals, ok := l.resolve(g.expr, scope{declare: g.declare}, depth+1)
		if !ok || !add(vals) {
			return nil, false
		}
	}
	for _, r := range info.external[name] {
		if !r.ok || !add(r.values) {
			return nil, false
		}
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

// attrsOf maps a block's own attributes by name.
func attrsOf(b *ast.BlockStmt) map[string]ast.Expr {
	out := map[string]ast.Expr{}
	for _, st := range b.Body {
		if a, ok := st.(*ast.AttributeStmt); ok {
			out[a.Name.Name] = a.Value
		}
	}
	return out
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

func (l *tenantLinter) refuse(at ast.Node, msg string) {
	pos := ast.StartPos(at).Position()
	l.diags = append(l.diags, Diagnostic{Line: pos.Line, Col: pos.Column, Message: msg, Stage: 1})
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
