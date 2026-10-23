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
	for component, paths := range tenantFromData {
		for path, attrs := range paths {
			for _, attr := range attrs {
				check(component, path, attr)
			}
		}
	}
	check("otelcol.auth.headers", "header", "key")
	check("otelcol.auth.headers", "header", "value")
	check("otelcol.auth.bearer", "", "header")
	check("otelcol.auth.bearer", "", "token")
	check("otelcol.auth.bearer", "", "scheme")
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

// headerOrTenantNotTenant are the attributes named like a header or a tenant
// that set no data tenant, each with why. Anything else whose name contains
// "header" or "tenant" (any case) must be one the lint reads — a schema bump
// that adds one fails TestTenantLintClassifiesEveryHeaderAndTenantAttribute
// until it is classified here or taught to the lint.
var headerOrTenantNotTenant = map[string]string{
	"allowed_headers":               "CORS: header names a receiver accepts",
	"exposed_headers":               "CORS: header names a receiver exposes",
	"extract_headers":               "kafka receiver: bool, copy Kafka headers into attributes",
	"allow_skip_pri_header":         "syslog receiver: bool, parsing option",
	"max_decoder_header_table_size": "HTTP/2 tuning number",
	"max_encoder_header_table_size": "HTTP/2 tuning number",
	"read_header_timeout":           "server timeout",
	"signal_header":                 "kafka exporter: bool, adds a signal-type header",
	"track_request_headers":         "beyla: bool, instrumentation option",
	"all_tenants":                   "openstack discovery: bool, OpenStack projects",
	"iam_server_id_header":          "vault AWS auth: Vault's server-id header value",
	"token_header":                  "otelcol.auth.google: the header its OAuth token goes in, never a tenant",
}

// azureTenantIDs are the tenant_id attributes that name an Azure AD identity
// tenant, not a data tenant (component and block path).
var azureTenantIDs = map[string]bool{
	"discovery.azure/oauth":                                      true,
	"discovery.azure/sdk_auth":                                   true,
	"prometheus.remote_write/endpoint/azuread/workload_identity": true,
	"prometheus.remote_write/endpoint/azuread/oauth":             true,
	"prometheus.remote_write/endpoint/azuread/sdk":               true,
}

// TestTenantLintClassifiesEveryHeaderAndTenantAttribute walks every
// attribute of the pinned schema whose name contains "header" or "tenant"
// and requires each to be one the lint reads (a headers map, a tenant
// attribute, otelcol.auth.bearer's header) or one listed above as carrying no
// data tenant. A new header-ish attribute fails until it is classified.
func TestTenantLintClassifiesEveryHeaderAndTenantAttribute(t *testing.T) {
	payload, err := loadPortSchema()
	if err != nil {
		t.Fatalf("loading the schema: %v", err)
	}
	found := 0
	var walk func(component string, path []string, attrs []visual.AttributeSchema, blocks []visual.BlockSchema)
	walk = func(component string, path []string, attrs []visual.AttributeSchema, blocks []visual.BlockSchema) {
		for _, a := range attrs {
			n := strings.ToLower(a.Name)
			if !strings.Contains(n, "header") && !strings.Contains(n, "tenant") {
				continue
			}
			found++
			last := ""
			if len(path) > 0 {
				last = path[len(path)-1]
			}
			where := strings.Join(append([]string{component}, path...), "/")
			switch {
			case isHeadersMapAttr(a.Name) && (a.Type == "map" || a.Type == "list" || a.Type == "string"):
				// read as a headers map (a list or string literal is skipped
				// as not a map, and anything computed is refused)
			case tenantAttrs[component][last] == a.Name:
			case component == "otelcol.auth.bearer" && a.Name == "header":
			case a.Name == "tenant_id" && azureTenantIDs[where]:
			case headerOrTenantNotTenant[a.Name] != "":
			default:
				t.Errorf("%s %s (%s) is named like a header or tenant but the tenant lint does not classify it", where, a.Name, a.Type)
			}
		}
		for _, b := range blocks {
			walk(component, append(append([]string{}, path...), b.Name), b.Attributes, b.Blocks)
		}
	}
	for name, c := range payload.Components {
		walk(name, nil, c.Attributes, c.Blocks)
	}
	if found < 100 {
		t.Fatalf("found only %d header/tenant attributes; the schema walk is broken", found)
	}
}
