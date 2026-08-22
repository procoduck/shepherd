package clustermetrics_test

import (
	"testing"

	"shepherd/internal/wizard/wizardtest"
)

// renderedAttrs is every attribute wizard.go's Commit sets, one entry per
// line it emits — see wizardtest.AssertSchemaConformance's doc for what this
// proves and internal/receiver/schema_conformance_test.go for the pattern
// this mirrors.
var renderedAttrs = []wizardtest.AttrPath{
	{Component: "prometheus.exporter.cadvisor", Path: []string{"docker_only"}},

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
