package validate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/grafana/alloy/syntax/ast"

	"shepherd/internal/schema"
)

// Strict tenant mode accepts only classified components (#421). The tenant
// lint checks, attribute by attribute, every place it knows a component can
// set a tenant; that is sound only for components whose tenant-relevant
// behaviour has been reviewed. So every component of every shipped schema
// carries a strict classification in the schema overlay
// (internal/schema/artifacts/overlay.json: components.<name>.strict, and
// strict_retired for a component only an older shipped schema has):
//
//   - allowed: it cannot introduce or alter a tenant label or header;
//   - checked: it can, but only through attributes the lint already checks
//     (Checks names each such check, StrictCheckCovers);
//   - refused: it can produce tenant-affecting output the lint cannot prove.
//
// In strict mode a component whose classification is refused, or that has
// none, is refused wherever the lint walks (the top level, a declare, a
// foreach template, a literal or pinned module), unless an application
// administrator allowed it for the org (TenantPolicy.AllowedComponents,
// org_strict_components): that lifts this refusal only, and every other
// check still applies. Custom components (declare and module instances)
// are judged by their bodies. strictcomponents_test.go fails when a shipped
// schema has a component without a classification, or a checked entry names
// a check that does not exist or does not cover the component.

// ComponentClass is a component's strict tenant classification.
type ComponentClass string

// The classes.
const (
	ComponentAllowed ComponentClass = "allowed"
	ComponentChecked ComponentClass = "checked"
	ComponentRefused ComponentClass = "refused"
)

// ComponentClassification is one component's entry in the overlay.
type ComponentClassification struct {
	Class ComponentClass `json:"class"`
	// Checks are the lint checks that cover a checked component's
	// tenant-relevant attributes (StrictCheckNames).
	Checks []string `json:"checks,omitempty"`
	// Reason is one line saying why.
	Reason string `json:"reason"`
}

// StrictComponentRefusal is the phrase every refusal of an unclassified or
// refused component carries.
const StrictComponentRefusal = "strict tenant mode accepts only classified components"

// strictCheck is one lint check a checked classification may name: what it
// reads, and whether it covers a component (reads that component's
// tenant-relevant attributes).
type strictCheck struct {
	doc    string
	covers func(component string) bool
}

// strictChecks are the checks a checked classification may name.
var strictChecks = map[string]strictCheck{
	"tenant_attributes": {
		doc:    "the tenant attributes (loki.write tenant_id, the rules components' tenant_id, stage.tenant) and the tenant taken from the data (stage.tenant label/source)",
		covers: isTenantComponent,
	},
	"headers": {
		doc:    "X-Scope-OrgID in every headers map (headers, http_headers, proxy_connect_header)",
		covers: func(c string) bool { return hasMapAttrKind(c, MapAttrHeaders) },
	},
	"label_maps": {
		doc:    "__tenant_id__ in every label map, which must be literal (ClassifyMapAttribute)",
		covers: func(c string) bool { return hasMapAttrKind(c, MapAttrLabels) },
	},
	"targets": {
		doc:    "the targets a component takes, classified by what it does with their labels (targetConsumers)",
		covers: func(c string) bool { return hasMapAttrKind(c, MapAttrTargets) },
	},
	"relabel_rules": {
		doc:    "relabel rule blocks writing __tenant_id__, and relabel rules attributes (RelabelRuleBlocks, RelabelRuleAttributes)",
		covers: hasRelabelRules,
	},
	"group_labels": {
		doc:    "stage.regex and stage.pattern labels_from_groups naming a label after a capture group",
		covers: func(c string) bool { return c == "loki.process" },
	},
	"secretfilter_origin": {
		doc:    "loki.secretfilter's origin_label",
		covers: func(c string) bool { return c == "loki.secretfilter" },
	},
	"enrich_labels": {
		doc: "loki.enrich's labels_to_copy",
		covers: func(c string) bool {
			tc, ok := lookupFamily(targetConsumers, c)
			return ok && tc.copiesLabels
		},
	},
	"push_source": {
		doc:    "every path from a source of client-chosen tenants to a loki.write overwrites or drops __tenant_id__ (pushSources)",
		covers: func(c string) bool { _, ok := pushSources[c]; return ok },
	},
	"auth_headers": {
		doc:    "otelcol.auth.headers' X-Scope-OrgID header",
		covers: func(c string) bool { return c == "otelcol.auth.headers" },
	},
	"auth_bearer": {
		doc:    "otelcol.auth.bearer sending its token as X-Scope-OrgID",
		covers: func(c string) bool { return c == "otelcol.auth.bearer" },
	},
	"gateway_endpoints": {
		doc:    "every endpoint on the tenant-route gateway's hosts is under one of the org's own routes, and an endpoint it cannot read is refused (gatewayendpoints.go)",
		covers: hasEndpointAttribute,
	},
	"blackbox_config": {
		doc:    "prometheus.exporter.blackbox's modules: a literal config naming no X-Scope-OrgID header and no tcp query_response",
		covers: func(c string) bool { return c == blackboxExporter },
	},
}

// StrictCheckNames lists the checks a checked classification may name.
func StrictCheckNames() []string {
	out := make([]string, 0, len(strictChecks))
	for k := range strictChecks {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// StrictCheckCovers reports whether check exists and whether it covers
// component (reads that component's tenant-relevant attributes).
func StrictCheckCovers(check, component string) (known, covers bool) {
	c, ok := strictChecks[check]
	if !ok {
		return false, false
	}
	if _, err := loadedSchemaIndex(); err != nil {
		return true, false
	}
	return true, c.covers(component)
}

// hasMapAttrKind reports whether a shipped schema gives component a map (or
// targets) attribute of kind.
func hasMapAttrKind(component string, kind MapAttrKind) bool {
	m, err := labelMapAttributes()
	if err != nil {
		return false
	}
	for _, attrs := range m[component] {
		for _, k := range attrs {
			if k == kind {
				return true
			}
		}
	}
	return false
}

// hasRelabelRules reports whether component takes relabel rule blocks or a
// relabel rules attribute.
func hasRelabelRules(component string) bool {
	blocks, err := RelabelRuleBlocks()
	if err != nil {
		return false
	}
	attrs, err := RelabelRuleAttributes()
	if err != nil {
		return false
	}
	return len(blocks[component]) > 0 || len(attrs[component]) > 0
}

var (
	classesOnce sync.Once
	classes     map[string]ComponentClassification
	classesErr  error
)

// ComponentClassifications is every component's strict classification, read
// from the embedded schema overlay once per process.
func ComponentClassifications() (map[string]ComponentClassification, error) {
	classesOnce.Do(func() {
		classes, classesErr = readClassifications(schema.Embedded)
	})
	return classes, classesErr
}

// readClassifications reads the classifications of the overlay in fsys:
// components.<name>.strict, and strict_retired.<name> for components only an
// older shipped schema has. A component in both is an error, as is an entry
// whose class is unknown.
func readClassifications(fsys fs.FS) (map[string]ComponentClassification, error) {
	data, err := fs.ReadFile(fsys, "artifacts/overlay.json")
	if err != nil {
		return nil, fmt.Errorf("reading the schema overlay: %w", err)
	}
	var ov struct {
		Components map[string]struct {
			Strict *ComponentClassification `json:"strict"`
		} `json:"components"`
		Retired map[string]ComponentClassification `json:"strict_retired"`
	}
	if err := json.Unmarshal(data, &ov); err != nil {
		return nil, fmt.Errorf("parsing the schema overlay: %w", err)
	}
	out := map[string]ComponentClassification{}
	for name, c := range ov.Components {
		if c.Strict != nil {
			out[name] = *c.Strict
		}
	}
	for name, c := range ov.Retired {
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("the schema overlay classifies %s in components and strict_retired", name)
		}
		out[name] = c
	}
	for name, c := range out {
		switch c.Class {
		case ComponentAllowed, ComponentChecked, ComponentRefused:
		default:
			return nil, fmt.Errorf("the schema overlay gives %s the unknown strict class %q", name, c.Class)
		}
	}
	return out, nil
}

// ClassifyComponent is component's strict classification, ok=false when it
// has none (or the overlay cannot be read).
func ClassifyComponent(component string) (ComponentClassification, bool) {
	m, err := ComponentClassifications()
	if err != nil {
		return ComponentClassification{}, false
	}
	c, ok := m[component]
	return c, ok
}

// ErrComponentNotAllowable is the refusal of allowing a component for an org
// that strict tenant mode already accepts, that no shipped schema has, or
// whose name is not a component name.
var ErrComponentNotAllowable = errors.New("not a component strict tenant mode refuses")

// CheckAllowableComponent reports whether an application administrator may
// allow component for an org: a component a shipped schema has (the pinned
// one, or an older one: strict_retired) whose classification is refused.
// One classified allowed or checked is already accepted; any other dotted
// name is no component Alloy runs, so allowing it would mean nothing.
func CheckAllowableComponent(component string) error {
	if !isComponentName(component) {
		return fmt.Errorf("%w: %q is not a component name (such as otelcol.auth.google)", ErrComponentNotAllowable, component)
	}
	c, ok := ClassifyComponent(component)
	if !ok {
		return fmt.Errorf("%w: no shipped Alloy schema has a component %s", ErrComponentNotAllowable, component)
	}
	if c.Class != ComponentRefused {
		return fmt.Errorf("%w: %s is classified %s, so strict tenant mode already accepts it", ErrComponentNotAllowable, component, c.Class)
	}
	return nil
}

// isComponentName reports whether s is a built-in component's name: two or
// more dot-separated parts of lower-case letters, digits and underscores,
// each starting with a letter, at most 128 bytes.
func isComponentName(s string) bool {
	if len(s) > 128 {
		return false
	}
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p[0] < 'a' || p[0] > 'z' {
			return false
		}
		for i := 0; i < len(p); i++ {
			c := p[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
				return false
			}
		}
	}
	return true
}

// strictComponent refuses component block b (named name, at a component
// position in sc) when strict tenant mode does not accept it: a built-in
// component whose classification is refused or missing, unless the org's
// application administrator allowed it. Only a dotted name can be a built-in
// component (namespace.name); a name without a dot is a declare instance or
// one of Alloy's own blocks (argument, export, logging, …), which Stage 2
// judges.
//
// A name a shipped schema has is always judged as that built-in, whatever
// import labels are in scope: an import label equal to a component
// namespace (import.string "otelcol") does not make otelcol.auth.google a
// custom component — Alloy runs the built-in. Any other dotted name is a
// module instance only when it is exactly "<label>.<declare>" for an import
// in scope whose module the lint read and which declares <declare>
// (resolveModuleInstances, once the walk has read every module); such an
// instance is judged by the module's body. Anything else is refused as
// unclassified.
func (l *tenantLinter) strictComponent(b *ast.BlockStmt, name string, sc scope) {
	if l.policy.literalsOnly || !strings.Contains(name, ".") || strings.HasPrefix(name, "import.") {
		return
	}
	if !l.budget.charge(1 + len(sc.declare)/64) {
		return
	}
	c, classified := ClassifyComponent(name)
	if !classified {
		if key, ok := l.moduleImport(sc.declare, name); ok {
			l.pendingInstances = append(l.pendingInstances, pendingInstance{block: b, name: name, importKey: key})
			return
		}
	}
	l.judgeComponent(b, name, c, classified)
}

// pendingInstance is a possible module instance strictComponent left for
// resolveModuleInstances.
type pendingInstance struct {
	block     *ast.BlockStmt
	name      string
	importKey string
}

// resolveModuleInstances judges every pending module instance once the walk
// has read every module of the text: "<label>.<declare>" whose module
// declares <declare> is judged by that body (already linted); any other is
// refused as unclassified (a module the lint could not read, a declare it
// does not have, a longer name).
func (l *tenantLinter) resolveModuleInstances() {
	for _, p := range l.pendingInstances {
		label := p.importKey[strings.LastIndex(p.importKey, "/")+1:]
		decl := strings.TrimPrefix(p.name, label+".")
		if l.moduleDeclares[p.importKey][decl] {
			continue
		}
		// Not a built-in (no shipped schema has the name) and not a declare
		// of the module the import loads: nothing an application
		// administrator could allow.
		l.refuse(p.block, fmt.Sprintf("%s %q: %s, and %s is not a built-in component; the module or declare it names "+
			"was not found (import %q has no declare %q the tenant check could read) — strict tenant mode refuses it",
			p.name, p.block.Label, StrictComponentRefusal, p.name, label, decl))
	}
	l.pendingInstances = nil
}

// judgeComponent refuses built-in component b (named name, classified c when
// classified) unless strict mode accepts it or the org allowed it.
func (l *tenantLinter) judgeComponent(b *ast.BlockStmt, name string, c ComponentClassification, classified bool) {
	if l.policy.AllowedComponents[name] {
		return
	}
	ok := classified
	switch {
	case !ok:
		// Every component a shipped schema has is classified (the repo test),
		// so this name is no component Alloy ships, and none an application
		// administrator can allow (CheckAllowableComponent).
		l.refuse(b, fmt.Sprintf("%s %q: %s, and %s has none: no shipped Alloy schema has it — strict tenant mode "+
			"refuses it", name, b.Label, StrictComponentRefusal, name))
	case c.Class == ComponentRefused:
		l.refuse(b, fmt.Sprintf("%s %q: %s, and %s is classified refused (%s) — strict tenant mode refuses it; an "+
			"application administrator can allow %s for this organisation", name, b.Label, StrictComponentRefusal,
			name, c.Reason, name))
	}
}

// moduleImport finds the import a name "<label>.<…>" may instantiate a
// module declare of: the innermost import block "<label>" visible in scope,
// returned by its key.
func (l *tenantLinter) moduleImport(scopeKey, name string) (string, bool) {
	label, _, ok := strings.Cut(name, ".")
	if !ok {
		return "", false
	}
	for k := scopeKey; ; {
		if key := declareKey(k, label); l.imports[key] {
			return key, true
		}
		if k == "" {
			return "", false
		}
		if i := strings.LastIndex(k, "/"); i >= 0 {
			k = k[:i]
		} else {
			k = ""
		}
	}
}

// collectImports records every import block's label by scope (as declares
// are keyed), for isModuleInstance.
func (l *tenantLinter) collectImports(stmts ast.Body, scopeKey string, nesting int) {
	if nesting > maxNesting {
		return
	}
	for _, st := range stmts {
		if !l.budget.take() {
			return
		}
		blk, ok := st.(*ast.BlockStmt)
		if !ok {
			continue
		}
		name := strings.Join(blk.Name, ".")
		switch {
		case strings.HasPrefix(name, "import.") && blk.Label != "":
			l.imports[declareKey(scopeKey, blk.Label)] = true
		case name == "declare" && blk.Label != "":
			if !l.budget.charge(1 + (len(scopeKey)+len(blk.Label))/64) {
				return
			}
			l.collectImports(blk.Body, declareKey(scopeKey, blk.Label), nesting+1)
		}
	}
}

// RefusesComponent reports whether msg, a refusal of the tenant lint (as
// tenancy.LintPipeline reports it, a module's included), is the refusal of
// component as unclassified or refused.
func RefusesComponent(msg, component string) bool {
	return strings.Contains(msg, ": "+StrictComponentRefusal+", and "+component+" ")
}

// StrictComponentRefusals is the refusals of components the tenant lint
// makes in content (refused or unclassified, none allowed for the org,
// every tenant owned): for tests and tooling. Content that does not parse
// has none.
func StrictComponentRefusals(content string) []string {
	diags := TenantLint(content, TenantPolicy{Strict: true, Owns: func(string) bool { return true }})
	var out []string
	for _, d := range diags {
		if strings.Contains(d.Message, StrictComponentRefusal) {
			out = append(out, d.Message)
		}
	}
	return slices.Compact(out)
}
