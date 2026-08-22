package database_test

import (
	"testing"

	"shepherd/internal/wizard/wizardtest"
)

// renderedAttrs is every attribute wizard.go's Commit sets, one entry per
// line it emits across all three selectable engines — see
// wizardtest.AssertSchemaConformance's doc.
var renderedAttrs = []wizardtest.AttrPath{
	{Component: "prometheus.exporter.postgres", Path: []string{"data_source_names"}},
	{Component: "prometheus.exporter.mysql", Path: []string{"data_source_name"}},
	{Component: "prometheus.exporter.redis", Path: []string{"redis_addr"}},

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
