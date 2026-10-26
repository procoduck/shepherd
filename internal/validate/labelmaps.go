package validate

import (
	"fmt"
	"io/fs"
	"strings"
	"sync"

	"github.com/grafana/alloy/syntax/ast"
)

// Strict tenant mode reads every label map a pipeline sets: a value that
// becomes labels on the data (loki.write's external_labels, a source's
// labels, stage.static_labels' values) may set Loki's __tenant_id__, and a
// map of request headers may set X-Scope-OrgID. The lint checks those keys in
// an object literal; a value it cannot read as one — a function call such as
// encoding.from_json, a component export, a concatenation — may carry either
// key, so strict mode refuses it unless the lint can fold it to literal maps
// (declare arguments, followed to every value they are given).
//
// Which attributes are label maps is read from every shipped component
// schema, not a hand list: every map-typed attribute, and every list
// attribute named targets, is classified by ClassifyMapAttribute, and
// tenant_labelmaps_test.go fails when a schema brings one no rule covers.

// MapAttrKind is how strict tenant mode treats one map-typed (or targets)
// attribute of a component schema.
type MapAttrKind int

const (
	// MapAttrUnclassified is an attribute no rule covers. The lint treats it
	// as MapAttrLabels (fail closed); the classification spec fails on it.
	MapAttrUnclassified MapAttrKind = iota
	// MapAttrLabels is a map whose entries become labels (or structured
	// metadata) on the data: in strict mode it must be an object literal,
	// or fold to literal maps, so the lint can read every key.
	MapAttrLabels
	// MapAttrHeaders is a map of request headers (isHeadersMapAttr): read
	// the same way, for X-Scope-OrgID.
	MapAttrHeaders
	// MapAttrTargets is a targets list; what it may carry is decided by the
	// component that takes it (targetConsumers).
	MapAttrTargets
	// MapAttrExcluded is a map that labels no data and sends no header the
	// backend reads a tenant from; the reason says why.
	MapAttrExcluded
)

// labelMapFamilies are the component families whose map attributes are
// label maps unless excluded below: their entries label logs, metrics,
// profiles or rule groups. (otelcol.* maps are OTLP attributes, not labels,
// and are each listed in mapAttrExclusions.)
var labelMapFamilies = []string{"loki.", "prometheus.", "pyroscope.", "faro.", "mimir."}

// otlpAttributes is why a map of OTLP attributes is not a label map: a Loki
// label is made from an OTLP attribute only by otelcol.exporter.loki (its
// loki.*.labels hints), and the strict served form routes that exporter
// through a __tenant_id__ drop (InsertTenantDrops).
const otlpAttributes = "OTLP attributes, not labels: a Loki label is made from them only by otelcol.exporter.loki, " +
	"which the strict served form routes through a " + tenantLabel + " drop"

// headerTenantOnly is why metrics and profiles labels carry no tenant.
const headerTenantOnly = "Mimir and Pyroscope read the tenant only from the X-Scope-OrgID header, never from a label"

// mapAttrNameExclusions are map attributes, by name in any component, that
// label no data and send no header a backend reads a tenant from.
var mapAttrNameExclusions = map[string]string{
	"endpoint_params": "OAuth2 token request parameters, sent to the token endpoint only",
	"match_labels":    "a Kubernetes label selector picking objects to watch; it sets no label on data",
	"params":          "query parameters of the scrape request",
	"pod_labels":      "a Beyla selector picking the pods to instrument",
	"pod_annotations": "a Beyla selector picking the pods to instrument",
}

// mapAttrExclusions are map attributes excluded at one component and block
// (the last block of the path inside the component, "" for its body).
var mapAttrExclusions = map[string]map[string]map[string]string{
	"loki.process": {
		"stage.json":   {"expressions": extractedFields},
		"stage.logfmt": {"mapping": extractedFields},
		"stage.geoip":  {"custom_lookups": extractedFields},
	},
	"otelcol.auth.oauth2":         {"": {"claims": "OAuth2 token request settings, sent to the token endpoint only"}},
	"otelcol.exporter.datadog":    {"traces": {"span_name_remappings": "span names for Datadog; no label or tenant header"}},
	"otelcol.exporter.splunkhec":  {"telemetry": {"extra_attributes": splunkTelemetry, "override_metrics_names": splunkTelemetry}},
	"otelcol.receiver.filelog":    {"": {"attributes": otlpAttributes, "resource": otlpAttributes}},
	"otelcol.receiver.cloudflare": {"": {"attributes": otlpAttributes}},
	"beyla.ebpf": {
		"kubernetes": {"resource_labels": otlpAttributes},
		"resources":  {"attributes": otlpAttributes},
	},
	"prometheus.exporter.cloudwatch": {
		"":          {"discovery_exported_tags": cloudwatchSelect},
		"discovery": {"search_tags": cloudwatchSelect},
		"static":    {"dimensions": cloudwatchSelect},
	},
	"prometheus.enrich":       {"": {"target_to_metric_match": "names labels to match targets and metrics on; it sets none"}},
	"mimir.alerts.kubernetes": {"": {"template_files": "Alertmanager template text, not labels"}},
	"remote.vault":            {"auth.custom": {"data": "the Vault login request's data"}},
}

const (
	extractedFields = "names fields extracted from the log line, not labels: a label is made from them only by " +
		"stage.labels, whose map is checked"
	splunkTelemetry  = "the exporter's own telemetry for Splunk; no label or tenant header"
	cloudwatchSelect = "selects the AWS resources and metrics to export; " + headerTenantOnly
)

// mapAttrFamilyExclusions are component families none of whose (non-header)
// map attributes is a label map.
var mapAttrFamilyExclusions = map[string]string{
	"discovery.": "a discovery component's labels reach the data only as targets, checked where they are taken " +
		"(targetConsumers)",
}

// targetConsumer is what a component taking a targets list does with the
// targets' labels, for strict tenant mode.
type targetConsumer struct {
	// reason says why a targets value it cannot read is still safe there.
	reason string
	// copiesLabels marks a component that copies target labels onto log
	// entries (loki.enrich): checked by enrichLabels.
	copiesLabels bool
}

// stripsReserved is why a Loki source's targets cannot carry __tenant_id__.
const stripsReserved = "the source drops every label starting with __ from a target before it labels an entry " +
	"(Alloy v1.21.2: loki/source/file/resolver.go and discovery.Target.NonReservedLabelSet, " +
	"loki/source/docker/tailer.go, loki/source/kubernetes/kubetail/target.go)"

// targetConsumers are the components that take a targets list, by name or
// family (a name ending in "."). One not listed is unclassified.
var targetConsumers = map[string]targetConsumer{
	"loki.source.file":       {reason: stripsReserved},
	"loki.source.docker":     {reason: stripsReserved},
	"loki.source.kubernetes": {reason: stripsReserved},
	"loki.enrich": {copiesLabels: true, reason: "copies target labels onto log entries; strict tenant mode requires " +
		"labels_to_copy, a literal list without " + tenantLabel},
	"prometheus.":             {reason: "metrics targets; " + headerTenantOnly},
	"pyroscope.":              {reason: "profiling targets; " + headerTenantOnly},
	"database_observability.": {reason: "metrics targets; " + headerTenantOnly},
	"otelcol.processor.discovery": {reason: "target labels become OTLP resource attributes; " +
		otlpAttributes},
	"discovery.": {reason: "a discovery component's input; its output is checked where it is taken"},
}

func lookupFamily[T any](m map[string]T, component string) (T, bool) {
	if v, ok := m[component]; ok {
		return v, true
	}
	for k, v := range m {
		if strings.HasSuffix(k, ".") && strings.HasPrefix(component, k) {
			return v, true
		}
	}
	var zero T
	return zero, false
}

// ClassifyMapAttribute classifies attribute attr of component, directly in
// block (the last block of the path inside the component, "" for its body),
// whose schema type is typ ("map", or "list" for targets), and says why when
// it is excluded or a targets list.
func ClassifyMapAttribute(component, block, attr, typ string) (MapAttrKind, string) {
	if typ != "map" {
		if attr != "targets" {
			return MapAttrUnclassified, ""
		}
		if c, ok := lookupFamily(targetConsumers, component); ok {
			return MapAttrTargets, c.reason
		}
		return MapAttrUnclassified, ""
	}
	if isHeadersMapAttr(attr) {
		return MapAttrHeaders, ""
	}
	if r, ok := mapAttrNameExclusions[attr]; ok {
		return MapAttrExcluded, r
	}
	if r, ok := mapAttrExclusions[component][block][attr]; ok {
		return MapAttrExcluded, r
	}
	if r, ok := lookupFamily(mapAttrFamilyExclusions, component); ok {
		return MapAttrExcluded, r
	}
	for _, f := range labelMapFamilies {
		if strings.HasPrefix(component, f) {
			return MapAttrLabels, ""
		}
	}
	return MapAttrUnclassified, ""
}

// SchemaMapAttribute is one map-typed or targets attribute of a shipped
// component schema.
type SchemaMapAttribute struct {
	Component string
	// Path is the block path inside the component ("" for its body,
	// "endpoint", "client/oauth2").
	Path string
	Attr string
	Type string
}

// Block is the last block of Path ("" for the component's body).
func (a SchemaMapAttribute) Block() string {
	if i := strings.LastIndex(a.Path, "/"); i >= 0 {
		return a.Path[i+1:]
	}
	return a.Path
}

// SchemaMapAttributes lists every map-typed attribute, and every list
// attribute named targets, of every component in every schema in fsys.
func SchemaMapAttributes(fsys fs.FS) ([]SchemaMapAttribute, error) {
	seen := map[SchemaMapAttribute]bool{}
	var out []SchemaMapAttribute
	err := eachSchemaComponent(fsys, func(name string, c schemaComponent) {
		collectMapAttributes(name, "", c.Attributes, c.Blocks, func(a SchemaMapAttribute) {
			if !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		})
	})
	return out, err
}

// collectMapAttributes calls add for every map-typed and targets attribute
// of component at path and below.
func collectMapAttributes(component, path string, attrs []schemaAttr, blocks []schemaBlock, add func(SchemaMapAttribute)) {
	for _, a := range attrs {
		if a.Type == "map" || (a.Type == "list" && a.Name == "targets") {
			add(SchemaMapAttribute{Component: component, Path: path, Attr: a.Name, Type: a.Type})
		}
	}
	for _, b := range blocks {
		p := b.Name
		if path != "" {
			p = path + "/" + b.Name
		}
		collectMapAttributes(component, p, b.Attributes, b.Blocks, add)
	}
}

var (
	labelMapAttrsOnce sync.Once
	labelMapAttrs     map[string]map[string]map[string]MapAttrKind
	labelMapAttrsErr  error
)

// labelMapAttributes is every shipped schema's map-typed and targets
// attributes as the lint looks them up: component -> last block -> attribute
// -> kind. Unclassified ones are MapAttrLabels (fail closed); were two paths
// with the same last block to disagree, the stricter kind (labels) wins.
func labelMapAttributes() (map[string]map[string]map[string]MapAttrKind, error) {
	labelMapAttrsOnce.Do(func() {
		idx, err := loadedSchemaIndex()
		if err != nil {
			labelMapAttrsErr = err
			return
		}
		attrs := idx.mapAttrs
		out := map[string]map[string]map[string]MapAttrKind{}
		for _, a := range attrs {
			kind, _ := ClassifyMapAttribute(a.Component, a.Block(), a.Attr, a.Type)
			if kind == MapAttrUnclassified {
				kind = MapAttrLabels
			}
			if out[a.Component] == nil {
				out[a.Component] = map[string]map[string]MapAttrKind{}
			}
			if out[a.Component][a.Block()] == nil {
				out[a.Component][a.Block()] = map[string]MapAttrKind{}
			}
			if prev, ok := out[a.Component][a.Block()][a.Attr]; !ok || kind == MapAttrLabels || prev == MapAttrExcluded {
				out[a.Component][a.Block()][a.Attr] = kind
			}
		}
		labelMapAttrs = out
	})
	return labelMapAttrs, labelMapAttrsErr
}

// labelMapKind is attribute attr's kind in block of component, or 0 when the
// schemas have no such map or targets attribute.
func labelMapKind(component, block, attr string) MapAttrKind {
	m, err := labelMapAttributes()
	if err != nil {
		// runLint read the schemas before any lint ran; unreadable, every
		// attribute is a label map (fail closed).
		return MapAttrLabels
	}
	return m[component][block][attr]
}

// foldedMap is one object literal a label map or headers value folds to,
// with the scope its values are read in. inPlace is set for the value's own
// literal, which labelMaps has already walked where it is written.
type foldedMap struct {
	obj     *ast.ObjectExpr
	sc      scope
	inPlace bool
}

// foldMaps returns every object literal v can be, when the lint can read it
// within its bounds: an object literal, or `argument.<name>.value` of the
// enclosing declare, followed to its default and every value an
// instantiation gives it (each read in the declare it is written in), which
// must fold the same way. Anything else — a function call
// (encoding.from_json), a component export, a value an importing text gives
// a module — is unreadable, as is an argument with no value at all.
func (l *tenantLinter) foldMaps(v ast.Expr, sc scope, depth int) ([]foldedMap, bool) {
	if v == nil || depth > maxDepth || !l.budget.take() {
		return nil, false
	}
	switch e := unparen(v).(type) {
	case *ast.ObjectExpr:
		return []foldedMap{{obj: e, sc: sc, inPlace: depth == 0}}, true
	case *ast.AccessExpr:
		name, ok := argumentRef(e)
		if !ok || sc.declare == "" {
			return nil, false
		}
		info := l.declares[sc.declare]
		if info == nil || len(info.external[name]) > 0 {
			return nil, false
		}
		var out []foldedMap
		add := func(x ast.Expr, s scope) bool {
			ms, ok := l.foldMaps(x, s, depth+1)
			if !ok || len(out)+len(ms) > maxFolded {
				return false
			}
			out = append(out, ms...)
			return true
		}
		if def := info.defaults[name]; def != nil && !add(def, scope{declare: sc.declare}) {
			return nil, false
		}
		for _, g := range info.given[name] {
			if !add(g.expr, scope{declare: g.declare}) {
				return nil, false
			}
		}
		return out, len(out) > 0
	}
	return nil, false
}

// labelMapAttribute checks attribute a when the schemas make it a label map
// of the component the walk is in (labelMapKind): its value must fold to
// object literals (foldMaps), whose keys the lint then reads. A literal
// written in place is checked by labelMaps; one a declare argument is given
// is checked there too, so only what depends on where it is used — a
// __tenant_id__ key in stage.labels, which reads its value from the log
// data — is checked here.
func (l *tenantLinter) labelMapAttribute(a *ast.AttributeStmt, sc scope) {
	owner, block := sc.ownerBlock()
	if owner == "" || labelMapKind(owner, block, a.Name.Name) != MapAttrLabels {
		return
	}
	parts := []string{owner}
	if block != "" {
		parts = append(parts, block)
	}
	where := strings.Join(append(parts, a.Name.Name), " ")
	maps, ok := l.foldMaps(a.Value, sc, 0)
	if !ok {
		l.refuse(a.Value, where+" is not a literal map the tenant check can read, so it may set "+tenantLabel+
			" — strict tenant mode refuses it; write the labels as a literal map ({ name = \"value\", … })")
		return
	}
	for _, m := range maps {
		if m.inPlace || block != "stage.labels" {
			continue
		}
		for _, f := range m.obj.Fields {
			if f.Name.Name == tenantLabel {
				l.refuse(a.Value, "stage.labels sets "+tenantLabel+" from the log data, which a client can set — "+
					"strict tenant mode refuses it")
			}
		}
	}
}

// enrichLabels checks a component that copies its targets' labels onto log
// entries (targetConsumers, loki.enrich): a target may carry any label,
// __tenant_id__ included, so labels_to_copy must be a literal, non-empty
// list that leaves it out (unset or empty, every label is copied).
func (l *tenantLinter) enrichLabels(b *ast.BlockStmt, name string) {
	if c, ok := lookupFamily(targetConsumers, name); !ok || !c.copiesLabels {
		return
	}
	copied := attrsOf(b)["labels_to_copy"]
	names, ok := literalList(copied)
	if ok && len(names) > 0 && !hasTenantLabel(names) {
		return
	}
	var at ast.Node = b
	if copied != nil {
		at = copied
	}
	l.refuse(at, fmt.Sprintf("%s %q copies its targets' labels onto log entries, and a target may carry %s — "+
		"strict tenant mode refuses it unless labels_to_copy is a literal list of the labels to copy, without %s",
		name, b.Label, tenantLabel, tenantLabel))
}
