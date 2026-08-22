package onboarding

import (
	"fmt"

	"shepherd/internal/gateway"
)

// Protocol selects which OTLP/HTTP wire format the rendered artifacts
// configure via OTEL_EXPORTER_OTLP_PROTOCOL. There is no gRPC option — see
// doc.go for why gRPC cannot be routed by this gateway's path-prefix tenant
// scheme (D9) at all, not merely why it is undocumented here.
type Protocol string

const (
	// ProtocolHTTPProtobuf renders OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf.
	// This is the value most OTel SDKs' own docs recommend for OTLP/HTTP and
	// the one RenderSDKNotes calls out as the default choice.
	ProtocolHTTPProtobuf Protocol = "http/protobuf"
	// ProtocolHTTPJSON renders OTEL_EXPORTER_OTLP_PROTOCOL=http/json —
	// heavier on the wire, but human-readable, which is useful while
	// verifying a new integration with a plain HTTP capture tool.
	ProtocolHTTPJSON Protocol = "http/json"
)

func validProtocol(p Protocol) bool {
	return p == ProtocolHTTPProtobuf || p == ProtocolHTTPJSON
}

// ConnectAppSpec is everything Render needs to produce every "connect an
// app" artifact for one tenant's OTLP route.
type ConnectAppSpec struct {
	// GatewayBaseURL is the externally reachable base URL of the gateway,
	// e.g. "https://telemetry.example.com" (no path required; BaseEndpoint
	// normalizes a trailing slash). Required, must be https.
	GatewayBaseURL string

	// Route identifies the tenant route these artifacts connect to.
	// Route.Kind must be gateway.KindOTLP (see doc.go). This is the SAME
	// gateway.RouteSpec type gateway.RenderHTTPRoute consumes — not a
	// lookalike local type — specifically so BaseEndpoint's derived path can
	// never drift from what the gateway actually routes.
	Route gateway.RouteSpec

	// ServiceName seeds OTEL_SERVICE_NAME in every rendered artifact.
	// Required: an unnamed service is a debugging dead end the moment two
	// services' telemetry lands in the same tenant destination.
	ServiceName string

	// Protocol selects the OTLP/HTTP wire format. Required.
	Protocol Protocol

	// IncludeADOTLayer, when true, adds the AWS Distro for OpenTelemetry
	// Lambda layer wiring (AWS_LAMBDA_EXEC_WRAPPER, and a layers/Layers
	// entry) to every Lambda-facing artifact. The ARN itself is NEVER a
	// literal this package fills in — D5's corollary: ADOT layer ARNs are
	// region/arch-specific and not pinned/freshness-checked the way
	// ALLOY_VERSION is. Each Lambda renderer instead defines its own
	// idiomatic placeholder (a Terraform variable, a SAM template
	// Parameter, a CDK construct prop) with no default, so the tool refuses
	// to apply until a human fills in the real ARN looked up from AWS's
	// published table (adotLambdaDocsURL). Leave false to omit the layer
	// entirely (e.g. a container image build that already bakes in
	// instrumentation).
	IncludeADOTLayer bool
}

func (s ConnectAppSpec) validate() error {
	if s.Route.Kind != gateway.KindOTLP {
		return fmt.Errorf("onboarding: ConnectAppSpec.Route.Kind %q is not %q", s.Route.Kind, gateway.KindOTLP)
	}
	if s.ServiceName == "" {
		return fmt.Errorf("onboarding: ConnectAppSpec.ServiceName must not be empty")
	}
	if !validProtocol(s.Protocol) {
		return fmt.Errorf("onboarding: ConnectAppSpec.Protocol %q is neither %q nor %q — OTLP/gRPC is not "+
			"supported by this gateway's path-prefix tenant routing, see package doc", s.Protocol, ProtocolHTTPProtobuf, ProtocolHTTPJSON)
	}
	return nil
}
