package podlogs_test

import (
	"testing"

	"shepherd/internal/wizard/wizardtest"
)

// renderedAttrs is every attribute wizard.go's Commit sets, one entry per
// line it emits — see wizardtest.AssertSchemaConformance's doc.
var renderedAttrs = []wizardtest.AttrPath{
	{Component: "discovery.kubernetes", Path: []string{"role"}},

	{Component: "discovery.relabel", Path: []string{"targets"}},
	{Component: "discovery.relabel", Path: []string{"rule", "source_labels"}},
	{Component: "discovery.relabel", Path: []string{"rule", "regex"}},
	{Component: "discovery.relabel", Path: []string{"rule", "action"}},

	{Component: "loki.source.kubernetes", Path: []string{"targets"}},
	{Component: "loki.source.kubernetes", Path: []string{"forward_to"}},

	{Component: "loki.process", Path: []string{"forward_to"}},

	{Component: "loki.write", Path: []string{"endpoint", "name"}},
	{Component: "loki.write", Path: []string{"endpoint", "url"}},
}

func TestSchemaConformance(t *testing.T) {
	wizardtest.AssertSchemaConformance(t, renderedAttrs, "testdata")
}

// TestLogFormatStagesExist proves every entry in wizard.go's logFormats
// allow-list is a real block loki.process declares, checked against the
// pinned schema artifact rather than assumed. logFormats deliberately
// excludes several real stage.* names (drop, geoip, ...) that don't apply
// here — this only proves the ones Commit CAN emit are real, not that the
// list is exhaustive over the schema.
func TestLogFormatStagesExist(t *testing.T) {
	components := wizardtest.ShippedSchemaComponents(t)
	for _, format := range []string{"logfmt", "json", "cri", "docker"} {
		blockName := "stage." + format
		if !wizardtest.HasBlock(components, "loki.process", blockName) {
			t.Errorf("loki.process has no %q block in the pinned schema artifact — wizard.go's logFormats claims it does", blockName)
		}
	}
}
