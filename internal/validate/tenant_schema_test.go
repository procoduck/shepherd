package validate

import (
	"strings"
	"testing"

	"shepherd/internal/visual"
)

// TestTenantLintNamesSchemaAttributes pins every place TenantLint reads a
// tenant from to the embedded component schema: a renamed component, block
// or attribute in a schema bump would otherwise leave the lint silently
// checking nothing there.
func TestTenantLintNamesSchemaAttributes(t *testing.T) {
	payload, err := loadPortSchema()
	if err != nil {
		t.Fatalf("loading the schema: %v", err)
	}
	check := func(component, path, attr string) {
		t.Helper()
		comp, ok := payload.Components[component]
		if !ok {
			t.Errorf("component %q is not in the schema", component)
			return
		}
		attrs, blocks := comp.Attributes, comp.Blocks
		if path != "" {
			for _, name := range strings.Split(path, "/") {
				var found *visual.BlockSchema
				for i := range blocks {
					if blocks[i].Name == name {
						found = &blocks[i]
					}
				}
				if found == nil {
					t.Errorf("%s has no block %q (path %q)", component, name, path)
					return
				}
				attrs, blocks = found.Attributes, found.Blocks
			}
		}
		for _, a := range attrs {
			if a.Name == attr {
				return
			}
		}
		t.Errorf("%s %s has no attribute %q", component, path, attr)
	}
	for component, paths := range tenantAttrs {
		for path, attr := range paths {
			check(component, path, attr)
		}
	}
	for component, paths := range tenantDynamic {
		for path, attrs := range paths {
			for _, attr := range attrs {
				check(component, path, attr)
			}
		}
	}
	check("otelcol.auth.headers", "header", "key")
	check("otelcol.auth.headers", "header", "value")
	// The headers maps the lint checks wherever they appear.
	for _, c := range []struct{ component, path string }{
		{"prometheus.remote_write", "endpoint"},
		{"loki.write", "endpoint"},
		{"pyroscope.write", "endpoint"},
		{"otelcol.exporter.otlp", "client"},
		{"otelcol.exporter.otlphttp", "client"},
	} {
		check(c.component, c.path, "headers")
	}
}
