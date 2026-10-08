package database_test

import (
	"testing"

	"shepherd/internal/wizard/wizardtest"
)

// renderedAttrs is every attribute wizard.go's Commit sets, one entry per
// line it emits across all three selectable engines — see
// wizardtest.AssertSchemaConformance's doc.
var renderedAttrs = []wizardtest.AttrPath{
	{Component: "remote.kubernetes.secret", Path: []string{"namespace"}},
	{Component: "remote.kubernetes.secret", Path: []string{"name"}},

	{Component: "prometheus.exporter.postgres", Path: []string{"data_source_names"}},
	{Component: "prometheus.exporter.mysql", Path: []string{"data_source_name"}},
	{Component: "prometheus.exporter.redis", Path: []string{"redis_addr"}},
	{Component: "prometheus.exporter.redis", Path: []string{"redis_password"}},

	{Component: "prometheus.scrape", Path: []string{"targets"}},
	{Component: "prometheus.scrape", Path: []string{"forward_to"}},
	{Component: "prometheus.scrape", Path: []string{"scrape_interval"}},
	{Component: "prometheus.scrape", Path: []string{"job_name"}},

	{Component: "prometheus.remote_write", Path: []string{"endpoint", "name"}},
	{Component: "prometheus.remote_write", Path: []string{"endpoint", "url"}},
}

func TestSchemaConformance(t *testing.T) {
	wizardtest.AssertSchemaConformance(t, renderedAttrs, "testdata")
}

// TestSecretValueLandsInSecretTypedAttrs pins why wizard.go passes the
// Secret's value straight into the exporter rather than through
// convert.nonsensitive: the single-value attributes it lands in are
// secret-typed in the pinned schema. postgres' data_source_names is a list
// (the artifact records no element type; Alloy declares it
// []alloytypes.Secret), so its acceptance of a secret value is proven by
// TestGoldensLoadInRealAlloy loading the postgres goldens instead.
func TestSecretValueLandsInSecretTypedAttrs(t *testing.T) {
	comps := wizardtest.ShippedSchemaComponents(t)
	for _, c := range []struct{ component, attr, want string }{
		{"prometheus.exporter.mysql", "data_source_name", "secret"},
		{"prometheus.exporter.redis", "redis_password", "secret"},
		{"prometheus.exporter.postgres", "data_source_names", "list"},
	} {
		comp, ok := comps[c.component].(map[string]any)
		if !ok {
			t.Fatalf("component %s not in the pinned schema", c.component)
		}
		attrs, _ := comp["attributes"].([]any) //nolint:errcheck // a missing list fails below
		found := false
		for _, a := range attrs {
			m, _ := a.(map[string]any) //nolint:errcheck // shape checked by the comparison
			if m["name"] == c.attr {
				found = true
				if m["type"] != c.want {
					t.Errorf("%s.%s is type %v in the pinned schema, want %s", c.component, c.attr, m["type"], c.want)
				}
			}
		}
		if !found {
			t.Errorf("%s.%s is not in the pinned schema", c.component, c.attr)
		}
	}
}
