package gateway

import (
	"fmt"
	"regexp"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// This file renders the one Kubernetes object Shepherd is allowed to emit for
// tenant routing (D1): a Gateway API v1 HTTPRoute. Two implementation choices
// are worth recording, because both were live options:
//
//  1. Typed Go structs from sigs.k8s.io/gateway-api, vs. rendering YAML by
//     hand (text/template or a map[string]any marshalled to YAML). Typed
//     structs win: they encode the actual API surface, so a field rename or a
//     filter that stops existing between Gateway API releases is a compile
//     error here, not a cluster-side "route accepted, does nothing" the way
//     D3 already refuses to ship. The cost is a dependency on
//     sigs.k8s.io/gateway-api — accepted deliberately, and pinned at the SAME
//     v1.4.1 as GATEWAY_API_VERSION in deploy/versions.env, so a future pin
//     bump is also a go.mod bump reviewed in the same diff, not a silent
//     drift between "the version we render for" and "the version we require
//     clusters to run" (D3's whole point).
//  2. Where to compute the two conformance-required filters (D2). They live
//     here, next to CheckSupport, rather than duplicated at every call site —
//     RenderHTTPRoute is the one place that knows both what Shepherd depends
//     on and how to express it in the typed API.

// RouteKind is the ingest path a tenant route serves. Two values, both wired
// through the receiver tier's Alloy config (W4): OTLP for otelcol.receiver.otlp,
// Faro for faro.receiver.
type RouteKind string

// The two RouteKind values Shepherd renders routes for.
const (
	// KindOTLP is the OTLP ingest path, wired to otelcol.receiver.otlp.
	KindOTLP RouteKind = "otlp"
	// KindFaro is the Grafana Faro (browser RUM) ingest path, wired to
	// faro.receiver.
	KindFaro RouteKind = "faro"
)

// TenantHeader is the header the gateway injects to carry tenant identity to
// the receiver tier. Downstream Alloy pipelines read it to route/label by
// tenant; nothing downstream may trust a value it did not itself set — see
// RenderHTTPRoute's use of Set (never Add).
const TenantHeader = "X-Scope-OrgID"

// routeSegmentRE constrains RouteSpec.RouteSegment to a single safe path
// element: no "/", no "..", nothing that could smuggle extra path structure
// into what is supposed to be one prefix segment. The prefix is an
// identifier, not an authorizer (plan §3) — this is about routing hygiene,
// not access control.
var routeSegmentRE = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9_-]{0,61}[a-zA-Z0-9])?$`)

// RouteSpec is everything RenderHTTPRoute needs to produce one tenant's
// HTTPRoute.
type RouteSpec struct {
	// Name is the HTTPRoute object's own name.
	Name string
	// Namespace is the namespace the HTTPRoute is created in — the receiver
	// tier's namespace (W4), which is infrastructure Shepherd manages
	// directly per D4's exception.
	Namespace string

	// TenantID is the value injected into TenantHeader downstream. Must not
	// contain control characters (defensive against header-injection via a
	// value that, however it originated, ends up in an HTTP header).
	TenantID string

	// Kind selects the ingest path segment: "otlp" or "faro".
	Kind RouteKind
	// RouteSegment is the tenant-route path segment after Kind, e.g.
	// "/{Kind}/{RouteSegment}/...". An opaque identifier (plan §11 open
	// decision 2 leaves the exact minting scheme open), constrained here to a
	// single safe path element.
	RouteSegment string

	// GatewayName/GatewayNamespace identify the Gateway API `Gateway` this
	// route attaches to. GatewayNamespace may be left empty to mean "same
	// namespace as the HTTPRoute" — the typed API represents that as a nil
	// pointer, not an empty string.
	GatewayName      string
	GatewayNamespace string

	// BackendName/BackendNamespace/BackendPort identify the Kubernetes
	// Service that receives the rewritten request. BackendNamespace may be
	// left empty for "same namespace as the HTTPRoute".
	BackendName      string
	BackendNamespace string
	BackendPort      int32
}

// PathPrefix is the literal path prefix this spec matches on:
// "/{Kind}/{RouteSegment}".
func (s RouteSpec) PathPrefix() string {
	return "/" + string(s.Kind) + "/" + s.RouteSegment
}

func (s RouteSpec) validate() error {
	switch s.Kind {
	case KindOTLP, KindFaro:
	default:
		return fmt.Errorf("gateway: route kind %q is neither %q nor %q", s.Kind, KindOTLP, KindFaro)
	}
	for field, v := range map[string]string{
		"Name": s.Name, "Namespace": s.Namespace, "TenantID": s.TenantID,
		"GatewayName": s.GatewayName, "BackendName": s.BackendName,
	} {
		if v == "" {
			return fmt.Errorf("gateway: RouteSpec.%s must not be empty", field)
		}
	}
	if !routeSegmentRE.MatchString(s.RouteSegment) {
		return fmt.Errorf("gateway: RouteSpec.RouteSegment %q is not a single safe path segment "+
			"(want %s)", s.RouteSegment, routeSegmentRE.String())
	}
	if containsControlByte(s.TenantID) {
		return fmt.Errorf("gateway: RouteSpec.TenantID contains a control character, refusing to "+
			"put it in the %s header value", TenantHeader)
	}
	if s.BackendPort < 1 || s.BackendPort > 65535 {
		return fmt.Errorf("gateway: RouteSpec.BackendPort %d is out of range 1-65535", s.BackendPort)
	}
	return nil
}

func containsControlByte(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// RenderHTTPRoute renders the Gateway API v1 HTTPRoute for one tenant route.
//
// Exactly the two filters D2 depends on, in the order the plan's model
// diagram lists them (§3):
//
//  1. URLRewrite / ReplacePrefixMatch strips the "/{kind}/{route}" prefix
//     with an empty replacement, so "/otlp/acme/v1/traces" becomes
//     "/v1/traces" at the backend — the receiver tier's pipeline sees the
//     path it would see with no gateway in front of it at all.
//  2. RequestHeaderModifier SETS (never adds) TenantHeader to spec.TenantID.
//     This is the entire security property this function exists to enforce:
//     Set overwrites any value the request already carried, so a client
//     cannot supply its own X-Scope-OrgID and have it survive — Add would
//     instead produce a header with two values and leave it to the backend
//     to guess which one is real. TestRenderHTTPRoute_TenantHeaderIsSetNotAdded
//     pins this.
func RenderHTTPRoute(spec RouteSpec) (*gatewayv1.HTTPRoute, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}

	return &gatewayv1.HTTPRoute{
		TypeMeta: metav1.TypeMeta{
			APIVersion: gatewayv1.GroupVersion.String(),
			Kind:       "HTTPRoute",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.Name,
			Namespace: spec.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "shepherd",
				"shepherd.io/route-kind":       string(spec.Kind),
			},
		},
		Spec: gatewayv1.HTTPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name:      gatewayv1.ObjectName(spec.GatewayName),
					Namespace: optionalNamespace(spec.GatewayNamespace),
				}},
			},
			Rules: []gatewayv1.HTTPRouteRule{{
				Matches: []gatewayv1.HTTPRouteMatch{{
					Path: &gatewayv1.HTTPPathMatch{
						Type:  ptr.To(gatewayv1.PathMatchPathPrefix),
						Value: ptr.To(spec.PathPrefix()),
					},
				}},
				Filters: []gatewayv1.HTTPRouteFilter{
					{
						Type: gatewayv1.HTTPRouteFilterURLRewrite,
						URLRewrite: &gatewayv1.HTTPURLRewriteFilter{
							Path: &gatewayv1.HTTPPathModifier{
								Type: gatewayv1.PrefixMatchHTTPPathModifier,
								// Empty replacement: "/{prefix}/rest" -> "/rest",
								// "/{prefix}" -> "/", per the ReplacePrefixMatch
								// semantics table in gateway-api's own httproute_types.go.
								ReplacePrefixMatch: ptr.To(""),
							},
						},
					},
					{
						Type: gatewayv1.HTTPRouteFilterRequestHeaderModifier,
						RequestHeaderModifier: &gatewayv1.HTTPHeaderFilter{
							Set: []gatewayv1.HTTPHeader{{
								Name:  gatewayv1.HTTPHeaderName(TenantHeader),
								Value: spec.TenantID,
							}},
						},
					},
				},
				BackendRefs: []gatewayv1.HTTPBackendRef{{
					BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name:      gatewayv1.ObjectName(spec.BackendName),
							Namespace: optionalNamespace(spec.BackendNamespace),
							Port:      ptr.To(spec.BackendPort),
						},
					},
				}},
			}},
		},
	}, nil
}

// optionalNamespace returns nil for "" (meaning "same namespace as the
// referring object", the typed API's own convention) and a pointer otherwise.
func optionalNamespace(ns string) *gatewayv1.Namespace {
	if ns == "" {
		return nil
	}
	return ptr.To(gatewayv1.Namespace(ns))
}
