// Package mcp is the agent interface (W11, docs/gateway-tier-plan.md): a
// Model Context Protocol server exposing a curated, read-plus-propose slice
// of the existing shepherd.mgmt.v1 Connect API to an AI-agent caller.
//
// This package is deliberately a THIN ADAPTER, not a second permission
// system (docs/gateway-tier-plan.md §4/§5: "the work is interface and
// identity, not new safety machinery"). Every tool handler does argument
// conversion and exactly one Backend call against the real, already-running
// mgmtapi Connect server; every safety property (capability scoping,
// on-behalf-of attribution, org scoping, ownership) is enforced there,
// unchanged, by machinery W10 already built and proved
// (internal/mgmtapi/machine_auth.go, rpc_interceptor.go,
// internal/auth/authz.go). This package adds no authorization decision of
// its own — see registry_test.go, which proves the tool surface never
// reaches a mutating procedure, and g14_g15_test.go, which proves the
// underlying credential cannot apply no matter what is called.
package mcp

import (
	"net/http"

	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
)

// onBehalfOfHeader mirrors internal/mgmtapi/machine_auth.go's unexported
// onBehalfOfHeader constant exactly. Restating the header NAME here is not
// the "new authorization logic" the W11 brief warns against — the
// verification of what this header carries happens entirely in
// machine_auth.go's requireWriteAuthorized and helpers.go's
// auditLogDetail, neither of which this package touches or reimplements.
const onBehalfOfHeader = "Shepherd-On-Behalf-Of"

// Config points a Backend at a running shepherd.mgmt.v1 Connect API and
// supplies the propose-capability service-account credential every call
// authenticates with (W10's existing Basic-auth mechanism,
// internal/mgmtapi/machine_auth.go — reused, not replaced).
//
// OnBehalfOf is fixed for the process lifetime, deliberately NOT a per-call
// tool argument. This is the load-bearing answer to R6's attribution
// question (docs/gateway-tier-plan.md §7): W10 already verifies an
// on-behalf-of claim against service_accounts.created_by for every
// apply-gated write (requireWriteAuthorized) — one credential delegates
// for exactly one human, and an unverifiable claim is worse than none
// (machine_auth.go's own doc comment). That verification does NOT run on
// SimulateService.CreateRun (it is propose-safe, so requireWriteAuthorized
// is never called on it — see capability_enumeration_test.go's
// nonMutatingNameExceptions), which means nothing in mgmtapi today stops an
// unverified on-behalf-of claim from being stamped into that path's audit
// row (helpers.go's auditLogDetail reads sa.OnBehalfOf unconditionally).
// This package closes that gap the only way available to a thin adapter
// that must not add authorization logic of its own: never let a tool
// argument set it. The header this Backend sends is always
// Config.OnBehalfOf, never a value taken from a tool call — so this
// process cannot emit a mismatched claim even though mgmtapi would not
// catch one on this specific path today. See the workstream report for the
// finding recommending mgmtapi close this at the source.
//
// The consequence, stated plainly for R6: an "agent acting autonomously
// with no human in the loop" is not a state this design allows to go
// unattributed. Every write-adjacent action a given MCP server process
// performs is attributed to the ONE human who created its credential
// (service_accounts.created_by), whether or not that human is watching at
// the moment of the call. Serving N humans means provisioning N
// propose-capability service accounts and running N credentialed server
// processes (or N configured sessions) — an operational cost, not a gap.
type Config struct {
	// BaseURL is the shepherd.mgmt.v1 Connect API's base URL (the same
	// server MountRPC mounts in production — internal/mgmtapi/router.go).
	BaseURL string
	// ServiceAccountID/ServiceAccountSecret are the propose-capability
	// service account's Basic-auth id:secret
	// (ServiceAccountService.CreateServiceAccount's response — W10).
	ServiceAccountID     string
	ServiceAccountSecret string
	// OnBehalfOf is the human this credential was issued to
	// (service_accounts.created_by) — see the type doc above.
	OnBehalfOf string
	// HTTPClient, if set, is wrapped with auth header injection instead of
	// http.DefaultClient. Tests point this at an httptest.Server's client.
	HTTPClient *http.Client
}

// Backend holds the typed Connect clients every tool handler calls through.
// Constructing a client per service (rather than one generic Connect
// client) keeps each tool handler's Backend call statically typed against
// the real shepherd.mgmt.v1 proto contracts — a request shape drifting out
// from under a tool handler fails `go build`, not a runtime agent session.
type Backend struct {
	cfg Config

	Fleet    mgmtv1connect.FleetServiceClient
	Pipeline mgmtv1connect.PipelineServiceClient
	Simulate mgmtv1connect.SimulateServiceClient
}

// NewBackend builds a Backend from cfg, wrapping the HTTP transport so
// every outgoing Connect call carries the propose credential and the fixed
// on-behalf-of claim automatically — no tool handler ever touches
// authentication.
func NewBackend(cfg Config) *Backend {
	base := cfg.HTTPClient
	if base == nil {
		base = http.DefaultClient
	}
	rt := base.Transport
	if rt == nil {
		rt = http.DefaultTransport
	}
	authed := &http.Client{
		Transport: &authRoundTripper{
			base:       rt,
			id:         cfg.ServiceAccountID,
			secret:     cfg.ServiceAccountSecret,
			onBehalfOf: cfg.OnBehalfOf,
		},
		CheckRedirect: base.CheckRedirect,
		Jar:           base.Jar,
		Timeout:       base.Timeout,
	}
	return &Backend{
		cfg:      cfg,
		Fleet:    mgmtv1connect.NewFleetServiceClient(authed, cfg.BaseURL),
		Pipeline: mgmtv1connect.NewPipelineServiceClient(authed, cfg.BaseURL),
		Simulate: mgmtv1connect.NewSimulateServiceClient(authed, cfg.BaseURL),
	}
}

// authRoundTripper injects the two headers internal/mgmtapi/machine_auth.go
// reads on every request, identically, regardless of which tool or
// procedure is being called — see Config.OnBehalfOf's doc comment for why
// that uniformity is the point, not a simplification.
type authRoundTripper struct {
	base       http.RoundTripper
	id, secret string
	onBehalfOf string
}

func (rt *authRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.SetBasicAuth(rt.id, rt.secret)
	if rt.onBehalfOf != "" {
		req.Header.Set(onBehalfOfHeader, rt.onBehalfOf)
	}
	// auth.CSRFMiddleware (internal/auth/auth.go) requires this header on
	// every non-GET request unconditionally — including a Basic-auth
	// service-account call, which carries no cookie for CSRF to even be
	// meaningful against. It is not optional for a Connect client talking
	// to mgmtapi's RPC mount, so it belongs here next to the other two
	// headers every call needs, not left for each tool handler to
	// remember.
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	return rt.base.RoundTrip(req)
}
