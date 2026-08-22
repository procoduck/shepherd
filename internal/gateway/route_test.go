package gateway_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"

	"shepherd/internal/gateway"
)

func otlpSpec() gateway.RouteSpec {
	return gateway.RouteSpec{
		Name:             "acme-otlp",
		Namespace:        "shepherd-receiver",
		TenantID:         "acme",
		Kind:             gateway.KindOTLP,
		RouteSegment:     "acme-a1b2c3",
		GatewayName:      "shepherd-gateway",
		GatewayNamespace: "shepherd-gateway-system",
		BackendName:      "receiver-acme",
		BackendPort:      4318,
	}
}

func faroSpec() gateway.RouteSpec {
	return gateway.RouteSpec{
		Name:         "acme-faro",
		Namespace:    "shepherd-receiver",
		TenantID:     "acme",
		Kind:         gateway.KindFaro,
		RouteSegment: "acme-webapp",
		GatewayName:  "shepherd-gateway",
		// GatewayNamespace deliberately empty: same-namespace case.
		BackendName: "receiver-acme",
		BackendPort: 12347,
	}
}

// TestRenderHTTPRouteGolden renders both route kinds to the same YAML shape a
// human would review or `kubectl apply`, and byte-compares against committed
// goldens. Regenerate with:
//
//	GEN_GOLDENS=1 go test ./internal/gateway/ -run TestGenGoldens -v
func TestRenderHTTPRouteGolden(t *testing.T) {
	for _, tc := range []struct {
		name   string
		spec   gateway.RouteSpec
		golden string
	}{
		{"otlp", otlpSpec(), "testdata/otlp-route.golden.yaml"},
		{"faro", faroSpec(), "testdata/faro-route.golden.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route, err := gateway.RenderHTTPRoute(tc.spec)
			if err != nil {
				t.Fatalf("RenderHTTPRoute: %v", err)
			}
			got, err := sigsyaml.Marshal(route)
			if err != nil {
				t.Fatalf("marshalling rendered route to YAML: %v", err)
			}
			want, err := os.ReadFile(tc.golden)
			if err != nil {
				t.Fatalf("reading golden %s: %v (run with GEN_GOLDENS=1 via TestGenGoldens to create it)", tc.golden, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("rendered route does not match %s.\n--- got ---\n%s\n--- want ---\n%s",
					tc.golden, got, want)
			}
		})
	}
}

// TestGenGoldens regenerates the golden files above from current renderer
// output. Not a regular test: skipped unless GEN_GOLDENS=1 is explicitly set,
// matching internal/visual's convention. Review the diff before committing —
// a golden change must correspond to a verified, intentional rendering
// change, never a shortcut to make TestRenderHTTPRouteGolden pass.
func TestGenGoldens(t *testing.T) {
	if os.Getenv("GEN_GOLDENS") == "" {
		t.Skip("set GEN_GOLDENS=1 to regenerate")
	}
	for _, tc := range []struct {
		spec   gateway.RouteSpec
		golden string
	}{
		{otlpSpec(), "testdata/otlp-route.golden.yaml"},
		{faroSpec(), "testdata/faro-route.golden.yaml"},
	} {
		route, err := gateway.RenderHTTPRoute(tc.spec)
		if err != nil {
			t.Fatalf("RenderHTTPRoute: %v", err)
		}
		data, err := sigsyaml.Marshal(route)
		if err != nil {
			t.Fatalf("marshalling rendered route to YAML: %v", err)
		}
		if err := os.MkdirAll(filepath.Dir(tc.golden), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(tc.golden, data, 0o644); err != nil {
			t.Fatalf("writing golden %s: %v", tc.golden, err)
		}
		t.Logf("wrote %s (%d bytes)", tc.golden, len(data))
	}
}

// TestRenderHTTPRoute_TenantHeaderIsSetNotAdded is the security-property test
// plan §5/W3 calls for: the tenant header must be SET (overwrite), never
// ADD (append), because Add would leave a client-supplied X-Scope-OrgID
// sitting alongside the injected one rather than replaced by it, and which
// value a backend then honors becomes implementation-defined — the exact
// spoofing gap this renderer exists to close.
func TestRenderHTTPRoute_TenantHeaderIsSetNotAdded(t *testing.T) {
	route, err := gateway.RenderHTTPRoute(otlpSpec())
	if err != nil {
		t.Fatalf("RenderHTTPRoute: %v", err)
	}
	if len(route.Spec.Rules) != 1 {
		t.Fatalf("expected exactly 1 rule, got %d", len(route.Spec.Rules))
	}
	var found bool
	for _, f := range route.Spec.Rules[0].Filters {
		if f.Type != "RequestHeaderModifier" {
			continue
		}
		found = true
		mod := f.RequestHeaderModifier
		if mod == nil {
			t.Fatalf("filter has type RequestHeaderModifier but no RequestHeaderModifier payload")
		}
		if len(mod.Add) != 0 {
			t.Fatalf("RequestHeaderModifier.Add = %v, want empty — tenant identity must never be "+
				"injected via Add, which appends to (rather than overwrites) any client-supplied value",
				mod.Add)
		}
		if len(mod.Set) != 1 {
			t.Fatalf("RequestHeaderModifier.Set has %d entries, want exactly 1 (the tenant header)", len(mod.Set))
		}
		if got := string(mod.Set[0].Name); !strings.EqualFold(got, gateway.TenantHeader) {
			t.Fatalf("RequestHeaderModifier.Set[0].Name = %q, want %q", got, gateway.TenantHeader)
		}
		if mod.Set[0].Value != "acme" {
			t.Fatalf("RequestHeaderModifier.Set[0].Value = %q, want tenant id %q", mod.Set[0].Value, "acme")
		}
	}
	if !found {
		t.Fatal("no RequestHeaderModifier filter found on the rendered route")
	}
}

// TestRenderHTTPRoute_PathPrefixAndRewrite pins the path-matching and
// path-rewriting halves of the contract: the match is a PathPrefix on
// "/{kind}/{route}", and the URLRewrite strips exactly that prefix (empty
// ReplacePrefixMatch) so the backend sees the path it would see with no
// gateway in front of it.
func TestRenderHTTPRoute_PathPrefixAndRewrite(t *testing.T) {
	spec := otlpSpec()
	route, err := gateway.RenderHTTPRoute(spec)
	if err != nil {
		t.Fatalf("RenderHTTPRoute: %v", err)
	}
	rule := route.Spec.Rules[0]

	if len(rule.Matches) != 1 || rule.Matches[0].Path == nil {
		t.Fatalf("expected exactly 1 path match, got %+v", rule.Matches)
	}
	wantPrefix := "/otlp/acme-a1b2c3"
	if got := spec.PathPrefix(); got != wantPrefix {
		t.Fatalf("PathPrefix() = %q, want %q", got, wantPrefix)
	}
	if got := string(*rule.Matches[0].Path.Type); got != "PathPrefix" {
		t.Fatalf("match path type = %q, want PathPrefix", got)
	}
	if got := *rule.Matches[0].Path.Value; got != wantPrefix {
		t.Fatalf("match path value = %q, want %q", got, wantPrefix)
	}

	var rewrite *string
	for _, f := range rule.Filters {
		if f.Type == "URLRewrite" {
			if f.URLRewrite == nil || f.URLRewrite.Path == nil {
				t.Fatalf("URLRewrite filter missing its Path modifier")
			}
			if got := string(f.URLRewrite.Path.Type); got != "ReplacePrefixMatch" {
				t.Fatalf("URLRewrite path modifier type = %q, want ReplacePrefixMatch", got)
			}
			rewrite = f.URLRewrite.Path.ReplacePrefixMatch
		}
	}
	if rewrite == nil {
		t.Fatal("no URLRewrite filter found on the rendered route")
	}
	if *rewrite != "" {
		t.Fatalf("ReplacePrefixMatch = %q, want empty string (strip the prefix entirely)", *rewrite)
	}
}

// TestRenderHTTPRoute_Validation exercises inputs RenderHTTPRoute must refuse
// rather than render into a broken or unsafe route.
func TestRenderHTTPRoute_Validation(t *testing.T) {
	base := otlpSpec()

	for _, tc := range []struct {
		name   string
		mutate func(gateway.RouteSpec) gateway.RouteSpec
	}{
		{"unknown kind", func(s gateway.RouteSpec) gateway.RouteSpec { s.Kind = "grpc"; return s }},
		{"empty name", func(s gateway.RouteSpec) gateway.RouteSpec { s.Name = ""; return s }},
		{"empty namespace", func(s gateway.RouteSpec) gateway.RouteSpec { s.Namespace = ""; return s }},
		{"empty tenant id", func(s gateway.RouteSpec) gateway.RouteSpec { s.TenantID = ""; return s }},
		{"empty gateway name", func(s gateway.RouteSpec) gateway.RouteSpec { s.GatewayName = ""; return s }},
		{"empty backend name", func(s gateway.RouteSpec) gateway.RouteSpec { s.BackendName = ""; return s }},
		{"route segment with slash", func(s gateway.RouteSpec) gateway.RouteSpec {
			s.RouteSegment = "acme/../other"
			return s
		}},
		{"empty route segment", func(s gateway.RouteSpec) gateway.RouteSpec { s.RouteSegment = ""; return s }},
		{"tenant id with CRLF", func(s gateway.RouteSpec) gateway.RouteSpec {
			s.TenantID = "acme\r\nX-Injected: evil"
			return s
		}},
		{"backend port zero", func(s gateway.RouteSpec) gateway.RouteSpec { s.BackendPort = 0; return s }},
		{"backend port too large", func(s gateway.RouteSpec) gateway.RouteSpec { s.BackendPort = 70000; return s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gateway.RenderHTTPRoute(tc.mutate(base))
			if err == nil {
				t.Fatalf("RenderHTTPRoute(%s) = nil error, want a refusal", tc.name)
			}
		})
	}

	// The unmutated base spec must itself be valid, or every case above would
	// pass for the wrong reason (failing regardless of which field was hit).
	if _, err := gateway.RenderHTTPRoute(base); err != nil {
		t.Fatalf("baseline valid spec was rejected: %v", err)
	}
}
