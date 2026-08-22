package onboarding_test

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"shepherd/internal/gateway"
	"shepherd/internal/onboarding"
)

// fixture builds one named ConnectAppSpec. Table-driven and named so
// TestRenderGolden and TestGenGoldens iterate the exact same set — adding a
// fixture here is the only step needed to golden-test it.
var fixtures = map[string]onboarding.ConnectAppSpec{
	"acme-lambda": {
		GatewayBaseURL: "https://telemetry.example.com",
		Route: gateway.RouteSpec{
			Kind:         gateway.KindOTLP,
			RouteSegment: "acme-a1b2c3d4",
			TenantID:     "acme",
		},
		ServiceName:      "checkout-api",
		Protocol:         onboarding.ProtocolHTTPProtobuf,
		IncludeADOTLayer: true,
	},
	"globex-container": {
		GatewayBaseURL: "https://telemetry.example.com",
		Route: gateway.RouteSpec{
			Kind:         gateway.KindOTLP,
			RouteSegment: "k7f3n9qp3mx8vh2q",
			TenantID:     "globex",
		},
		ServiceName: "billing-worker",
		Protocol:    onboarding.ProtocolHTTPJSON,
		// IncludeADOTLayer left false: this fixture is a container image
		// build, not Lambda — proves the layer wiring is optional and the
		// AWS_LAMBDA_EXEC_WRAPPER var does not leak in when it is unset.
	},
}

func fixtureNames() []string {
	names := make([]string, 0, len(fixtures))
	for name := range fixtures {
		names = append(names, name)
	}
	return names
}

func readGolden(t *testing.T, fixture, artifact string) string {
	t.Helper()
	path := goldenPath(fixture, artifact)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden file %s is missing — it must be committed (regenerate with GEN_GOLDENS=1 go test ./internal/onboarding/ -run TestGenGoldens -v): %v", path, err)
	}
	return string(b)
}

func goldenPath(fixture, artifact string) string {
	return fmt.Sprintf("testdata/%s.%s.golden", fixture, artifact)
}

func TestRenderGolden(t *testing.T) {
	for name, spec := range fixtures {
		t.Run(name, func(t *testing.T) {
			bundle, err := onboarding.Render(spec)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			for artifact, got := range map[string]string{
				"env":       bundle.Env,
				"lambda":    bundle.Lambda,
				"terraform": bundle.Terraform,
				"sam":       bundle.SAM,
				"cdk":       bundle.CDK,
				"k8s":       bundle.K8s,
				"sdk-notes": bundle.SDKNotes,
			} {
				want := readGolden(t, name, artifact)
				if got != want {
					t.Errorf("%s/%s: output does not match testdata/%s.%s.golden\n--- got ---\n%s\n--- want ---\n%s", name, artifact, name, artifact, got, want)
				}
			}
		})
	}
}

// httpsURLRE finds every https:// URL embedded anywhere in rendered
// artifact text — inside quotes, JSON strings, YAML values, HCL strings,
// TS strings, or Markdown prose. Deliberately broad (stops at whitespace or
// a closing quote/paren/bracket/backtick) so it does not need to know which
// artifact format it is scanning.
var httpsURLRE = regexp.MustCompile(`https://[^\s"'` + "`" + `)\]]+`)

// TestAllEmittedEndpointsResolveToTheRenderedRoute is G7's second half (plan
// §5: "must not emit an endpoint that no route serves") checked across
// EVERY artifact in Bundle at once, not just the ones covered by an
// eyeballed golden diff. It renders a REAL gatewayv1.HTTPRoute via
// gateway.RenderHTTPRoute for the fixture's route, extracts the path that
// route actually matches on, then scans every string field of Bundle for an
// https:// URL and asserts each one's path is either exactly that route
// path or that path plus "/v1/<signal>" (the only two forms this package's
// renderers ever produce — BaseEndpoint and SignalEndpoint).
//
// RED-RUN PROOF: temporarily changing envvars.go's coreOTLPEnv to emit
// base+"/extra" instead of base for OTEL_EXPORTER_OTLP_ENDPOINT — simulating
// exactly the class of bug this test exists to catch, a renderer that stops
// deriving its endpoint from BaseEndpoint's result — makes this test fail on
// every fixture, e.g.: "acme-lambda: emitted endpoint
// \"https://telemetry.example.com/otlp/acme-a1b2c3d4/extra\" has path
// \"/otlp/acme-a1b2c3d4/extra\" which is not the route's path
// (\"/otlp/acme-a1b2c3d4\") or one of its /v1/<signal> children — this
// endpoint would 404 at the gateway". Confirmed 2026-08-22 (go test
// ./internal/onboarding/ -run TestAllEmittedEndpointsResolveToTheRenderedRoute -v);
// reverting the edit returns the suite to green. TestRenderGolden fails on
// the same edit too (env/lambda/terraform/sam/cdk/k8s all diverge from
// their goldens), but only this test's failure message names the actual
// product defect — a golden diff alone just says "text changed".
func TestAllEmittedEndpointsResolveToTheRenderedRoute(t *testing.T) {
	for name, spec := range fixtures {
		t.Run(name, func(t *testing.T) {
			fullSpec := gateway.RouteSpec{
				Name: "route-under-test", Namespace: "shepherd-receiver",
				TenantID: spec.Route.TenantID, Kind: spec.Route.Kind, RouteSegment: spec.Route.RouteSegment,
				GatewayName: "shepherd-gateway", BackendName: "receiver", BackendPort: 4318,
			}
			route, err := gateway.RenderHTTPRoute(fullSpec)
			if err != nil {
				t.Fatalf("gateway.RenderHTTPRoute: %v", err)
			}
			wantPath := *route.Spec.Rules[0].Matches[0].Path.Value

			bundle, err := onboarding.Render(spec)
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			all := strings.Join([]string{
				bundle.Env, bundle.Lambda, bundle.Terraform, bundle.SAM, bundle.CDK, bundle.K8s, bundle.SDKNotes,
			}, "\n")

			gatewayHost, err := url.Parse(spec.GatewayBaseURL)
			if err != nil {
				t.Fatalf("parsing fixture GatewayBaseURL %q: %v", spec.GatewayBaseURL, err)
			}

			urls := httpsURLRE.FindAllString(all, -1)
			if len(urls) == 0 {
				t.Fatalf("expected at least one https:// endpoint URL across the rendered bundle, found none")
			}
			seen := map[string]bool{}
			for _, raw := range urls {
				seen[raw] = true
			}
			gatewayURLCount := 0
			for raw := range seen {
				u, err := url.Parse(raw)
				if err != nil {
					t.Errorf("emitted endpoint %q does not parse as a URL: %v", raw, err)
					continue
				}
				if u.Host != gatewayHost.Host {
					// Not an endpoint this package derived from the tenant
					// route at all — e.g. adotLambdaDocsURL, a reference
					// link, not something an SDK is ever configured with.
					// Only URLs at the gateway's own host are this
					// property's business.
					continue
				}
				gatewayURLCount++
				ok := u.Path == wantPath
				for _, sig := range []string{"traces", "metrics", "logs"} {
					if u.Path == wantPath+"/v1/"+sig {
						ok = true
					}
				}
				if !ok {
					t.Errorf("%s: emitted endpoint %q has path %q which is not the route's path (%q) "+
						"or one of its /v1/<signal> children — this endpoint would 404 at the gateway",
						name, raw, u.Path, wantPath)
				}
			}
			if gatewayURLCount == 0 {
				t.Fatalf("%s: found https:// URLs in the bundle but none at the gateway host %q — expected at least one actual endpoint", name, gatewayHost.Host)
			}
		})
	}
}

func TestRenderRefusesFaroRoute(t *testing.T) {
	spec := fixtures["acme-lambda"]
	spec.Route.Kind = gateway.KindFaro
	spec.Route.RouteSegment = "acme-webapp"
	_, err := onboarding.Render(spec)
	if err == nil {
		t.Fatal("expected Render to refuse a Faro-kind route, got nil error")
	}
}

func TestRenderRefusesEmptyServiceName(t *testing.T) {
	spec := fixtures["acme-lambda"]
	spec.ServiceName = ""
	_, err := onboarding.Render(spec)
	if err == nil {
		t.Fatal("expected Render to refuse an empty ServiceName, got nil error")
	}
}

func TestRenderRefusesUnknownProtocol(t *testing.T) {
	spec := fixtures["acme-lambda"]
	spec.Protocol = "grpc"
	_, err := onboarding.Render(spec)
	if err == nil {
		t.Fatal("expected Render to refuse protocol \"grpc\", got nil error")
	}
	if !strings.Contains(err.Error(), "gRPC") {
		t.Fatalf("error %q does not explain why gRPC is refused", err)
	}
}

// TestNoADOTArnHardcoded is D5's corollary, checked as a property: no
// rendered artifact — across any fixture, regardless of IncludeADOTLayer —
// ever contains a literal AWS ARN string. IncludeADOTLayer only ever adds a
// reference to a variable/parameter the artifact itself declares.
//
// RED-RUN PROOF: temporarily hardcoding
// `arn:aws:lambda:us-east-1:901920570463:layer:aws-otel-python-amd64-ver-1-25-0:1`
// in render_terraform.go's layers line makes this test fail with "testdata
// artifact terraform for fixture acme-lambda contains a literal ARN"; reverting
// returns it to green. Confirmed 2026-08-22.
func TestNoADOTArnHardcoded(t *testing.T) {
	arnRE := regexp.MustCompile(`arn:aws:lambda:[a-z0-9-]+:\d+:layer:`)
	for name, spec := range fixtures {
		bundle, err := onboarding.Render(spec)
		if err != nil {
			t.Fatalf("%s: Render: %v", name, err)
		}
		for artifact, text := range map[string]string{
			"lambda": bundle.Lambda, "terraform": bundle.Terraform, "sam": bundle.SAM, "cdk": bundle.CDK,
		} {
			if arnRE.MatchString(text) {
				t.Errorf("testdata artifact %s for fixture %s contains a literal ARN — D5 forbids hardcoding ADOT layer ARNs", artifact, name)
			}
		}
	}
}
