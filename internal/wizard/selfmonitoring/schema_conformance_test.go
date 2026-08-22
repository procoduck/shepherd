package selfmonitoring_test

import (
	"testing"

	"shepherd/internal/wizard/wizardtest"
)

// renderedAttrs is every attribute wizard.go's Commit sets, one entry per
// line it emits — see wizardtest.AssertSchemaConformance's doc.
var renderedAttrs = []wizardtest.AttrPath{
	{Component: "prometheus.scrape", Path: []string{"targets"}},
	{Component: "prometheus.scrape", Path: []string{"forward_to"}},
	{Component: "prometheus.scrape", Path: []string{"scrape_interval"}},
	{Component: "prometheus.scrape", Path: []string{"job_name"}},

	{Component: "prometheus.remote_write", Path: []string{"endpoint", "name"}},
	{Component: "prometheus.remote_write", Path: []string{"endpoint", "url"}},

	{Component: "loki.source.file", Path: []string{"targets"}},
	{Component: "loki.source.file", Path: []string{"forward_to"}},

	{Component: "loki.write", Path: []string{"endpoint", "name"}},
	{Component: "loki.write", Path: []string{"endpoint", "url"}},
}

func TestSchemaConformance(t *testing.T) {
	wizardtest.AssertSchemaConformance(t, renderedAttrs, "testdata")
}

// TestExporterSelfComponentExists covers prometheus.exporter.self
// separately: wizard.go emits it with an empty body (`prometheus.exporter.self
// "alloy" {}`, no attributes set), so there is no attribute leaf for
// renderedAttrs above to anchor on — this proves the component NAME itself
// is real, the same claim AssertSchemaConformance's "declares every
// component" half makes for components it has an attribute entry for.
func TestExporterSelfComponentExists(t *testing.T) {
	components := wizardtest.ShippedSchemaComponents(t)
	if _, ok := components["prometheus.exporter.self"]; !ok {
		t.Fatal(`component "prometheus.exporter.self" is not in the pinned schema artifact`)
	}
}
