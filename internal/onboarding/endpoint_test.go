package onboarding_test

import (
	"net/url"
	"strings"
	"testing"

	"shepherd/internal/gateway"
	"shepherd/internal/onboarding"
)

// fullRouteSpec is a fully populated gateway.RouteSpec — everything
// gateway.RenderHTTPRoute itself requires (GatewayName, BackendName, etc.),
// not just the Kind/RouteSegment onboarding.BaseEndpoint reads. Using the
// same value against both functions is what makes
// TestBaseEndpointPathMatchesRenderedRoute a real cross-check rather than
// two call sites that merely happen to agree.
func fullRouteSpec() gateway.RouteSpec {
	return gateway.RouteSpec{
		Name:             "acme-otlp",
		Namespace:        "shepherd-receiver",
		TenantID:         "acme",
		Kind:             gateway.KindOTLP,
		RouteSegment:     "acme-a1b2c3d4",
		GatewayName:      "shepherd-gateway",
		GatewayNamespace: "shepherd-gateway-system",
		BackendName:      "receiver-acme",
		BackendPort:      4318,
	}
}

// TestBaseEndpointPathMatchesRenderedRoute is the W7 must-not proof (plan
// §5: "must not emit an endpoint that no route serves"). It renders a REAL
// gatewayv1.HTTPRoute via gateway.RenderHTTPRoute and reads the actual path
// match value out of that object — the same object
// internal/gateway/apply.go applies to a cluster — and checks BaseEndpoint's
// URL carries exactly that path, rather than trusting that BaseEndpoint and
// RenderHTTPRoute merely call the same helper.
//
// RED-RUN PROOF: temporarily changing BaseEndpoint (endpoint.go) to restate
// the path as `"/" + string(route.Kind) + "-" + route.RouteSegment` instead
// of calling route.PathPrefix() — i.e. the exact "format string that
// restates it" plan §5 forbids, with a deliberately wrong separator to make
// the divergence concrete — makes this test fail with: `endpoint path
// "/otlp-acme-a1b2c3d4" does not match the route's actual rendered path
// "/otlp/acme-a1b2c3d4"`. Confirmed 2026-08-22 (go test
// ./internal/onboarding/ -run TestBaseEndpointPathMatchesRenderedRoute -v);
// reverting the edit returns it to green. This is the guarantee a
// hardcoded-format-string implementation of BaseEndpoint could never offer:
// because the "want" side is read from the actually-rendered Kubernetes
// object rather than from calling route.PathPrefix() a second time, ANY
// drift between what BaseEndpoint computes and what the gateway truly
// routes is caught here — including a future drift inside route.go itself,
// since PathPrefix() is the one function both RenderHTTPRoute and
// BaseEndpoint depend on and this test never assumes they agree, it checks.
func TestBaseEndpointPathMatchesRenderedRoute(t *testing.T) {
	spec := fullRouteSpec()
	route, err := gateway.RenderHTTPRoute(spec)
	if err != nil {
		t.Fatalf("gateway.RenderHTTPRoute: %v", err)
	}
	rules := route.Spec.Rules
	if len(rules) != 1 || len(rules[0].Matches) != 1 || rules[0].Matches[0].Path == nil || rules[0].Matches[0].Path.Value == nil {
		t.Fatalf("rendered route has an unexpected shape: %+v", route.Spec)
	}
	wantPath := *rules[0].Matches[0].Path.Value

	got, err := onboarding.BaseEndpoint("https://telemetry.example.com", spec)
	if err != nil {
		t.Fatalf("BaseEndpoint: %v", err)
	}
	gotURL, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parsing BaseEndpoint result %q: %v", got, err)
	}
	if gotURL.Path != wantPath {
		t.Fatalf("endpoint path %q does not match the route's actual rendered path %q", gotURL.Path, wantPath)
	}
}

func TestBaseEndpointRefusesNonOTLPKind(t *testing.T) {
	spec := fullRouteSpec()
	spec.Kind = gateway.KindFaro
	spec.RouteSegment = "acme-webapp"
	_, err := onboarding.BaseEndpoint("https://telemetry.example.com", spec)
	if err == nil {
		t.Fatal("expected an error for a Faro-kind route, got nil")
	}
	if !strings.Contains(err.Error(), "otlp") {
		t.Fatalf("error %q does not name the expected kind", err)
	}
}

func TestBaseEndpointRefusesInvalidSegment(t *testing.T) {
	spec := fullRouteSpec()
	spec.RouteSegment = "../etc/passwd"
	_, err := onboarding.BaseEndpoint("https://telemetry.example.com", spec)
	if err == nil {
		t.Fatal("expected an error for an invalid route segment, got nil")
	}
}

func TestBaseEndpointRefusesNonHTTPS(t *testing.T) {
	_, err := onboarding.BaseEndpoint("http://telemetry.example.com", fullRouteSpec())
	if err == nil {
		t.Fatal("expected an error for a plaintext gateway URL, got nil")
	}
	if !strings.Contains(err.Error(), "https") {
		t.Fatalf("error %q does not mention https", err)
	}
}

func TestBaseEndpointRefusesEmptyGatewayURL(t *testing.T) {
	_, err := onboarding.BaseEndpoint("", fullRouteSpec())
	if err == nil {
		t.Fatal("expected an error for an empty gateway URL, got nil")
	}
}

func TestBaseEndpointNormalizesTrailingSlash(t *testing.T) {
	withSlash, err := onboarding.BaseEndpoint("https://telemetry.example.com/", fullRouteSpec())
	if err != nil {
		t.Fatalf("BaseEndpoint: %v", err)
	}
	withoutSlash, err := onboarding.BaseEndpoint("https://telemetry.example.com", fullRouteSpec())
	if err != nil {
		t.Fatalf("BaseEndpoint: %v", err)
	}
	if withSlash != withoutSlash {
		t.Fatalf("a trailing slash on the gateway base URL produced a different endpoint: %q vs %q", withSlash, withoutSlash)
	}
	if strings.Contains(withSlash, "//otlp") {
		t.Fatalf("endpoint %q has a doubled slash before the route prefix", withSlash)
	}
}

// TestSignalEndpointAppendsExactlyOneSuffix pins the OpenTelemetry spec
// citation in endpoint.go: verified 2026-08-22 against
// https://opentelemetry.io/docs/specs/otel/protocol/exporter/, which states
// the per-signal env vars ("OTEL_EXPORTER_OTLP_<SIGNAL>_ENDPOINT") are used
// "as-is without any modification" — so SignalEndpoint (the function that
// builds the value for those vars) must itself append the suffix, exactly
// once, since nothing downstream ever will.
func TestSignalEndpointAppendsExactlyOneSuffix(t *testing.T) {
	base, err := onboarding.BaseEndpoint("https://telemetry.example.com", fullRouteSpec())
	if err != nil {
		t.Fatalf("BaseEndpoint: %v", err)
	}
	for _, tc := range []struct {
		signal onboarding.Signal
		want   string
	}{
		{onboarding.SignalTraces, base + "/v1/traces"},
		{onboarding.SignalMetrics, base + "/v1/metrics"},
		{onboarding.SignalLogs, base + "/v1/logs"},
	} {
		got, err := onboarding.SignalEndpoint(base, tc.signal)
		if err != nil {
			t.Fatalf("SignalEndpoint(%q): %v", tc.signal, err)
		}
		if got != tc.want {
			t.Errorf("SignalEndpoint(%q) = %q, want %q", tc.signal, got, tc.want)
		}
	}
}

func TestSignalEndpointRefusesUnknownSignal(t *testing.T) {
	_, err := onboarding.SignalEndpoint("https://telemetry.example.com/otlp/x", onboarding.Signal("bogus"))
	if err == nil {
		t.Fatal("expected an error for an unknown signal, got nil")
	}
}
