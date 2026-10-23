package mgmtapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/gateway"
	"shepherd/internal/onboarding"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// TenantRouteService implements mgmtv1connect.TenantRouteServiceHandler:
// create/list/rotate/revoke for tenant route records (W4,
// docs/gateway-tier-plan.md §4), plus the read-only "connect an app"
// rendering (W7). It owns the generation POLICY wiring
// (internal/gateway.GenerateSegment, D9) and persistence
// (0009_tenant_routes, D8's two gateway_mode values); it does not touch
// Kubernetes — internal/routeapply's reconciler applies the HTTPRoutes.
type TenantRouteService struct {
	store  *store.Store
	logger *slog.Logger
	// publicBaseURL is the gateway's public https URL, the default for
	// RenderConnectApp (gateway.routes.public_base_url). May be empty.
	publicBaseURL string
	// applyRunning reports whether this server started the tenant-route
	// reconciler (internal/routeapply). Nil means it did not. A route the
	// reconciler never picked up is reported "not_applicable" with the reason
	// rather than "pending", which would read as "about to be applied" (#304).
	applyRunning func() bool
}

// TenantRouteOption configures a TenantRouteService.
type TenantRouteOption func(*TenantRouteService)

// WithGatewayPublicBaseURL sets the default gateway URL RenderConnectApp uses.
func WithGatewayPublicBaseURL(u string) TenantRouteOption {
	return func(s *TenantRouteService) { s.publicBaseURL = u }
}

// WithTenantRouteApplyRunning tells the service whether the tenant-route reconciler
// is running in this process; see TenantRouteService.applyRunning.
func WithTenantRouteApplyRunning(running func() bool) TenantRouteOption {
	return func(s *TenantRouteService) { s.applyRunning = running }
}

// NewTenantRouteService constructs a TenantRouteService.
func NewTenantRouteService(st *store.Store, logger *slog.Logger, opts ...TenantRouteOption) *TenantRouteService {
	s := &TenantRouteService{store: st, logger: logger}
	for _, o := range opts {
		o(s)
	}
	return s
}

var _ mgmtv1connect.TenantRouteServiceHandler = (*TenantRouteService)(nil)

// gateway_mode values, matching 0009_tenant_routes.up.sql's CHECK
// constraint and D8's two ownership modes.
const (
	gatewayModeManaged  = "managed"
	gatewayModeOperator = "operator"
)

// defaultRotationOverlap is how long a deprecated segment keeps routing
// traffic after RotateTenantRoute, when the caller does not specify one
// (D9: "issue new, run both briefly, revoke old").
const defaultRotationOverlap = 24 * time.Hour

var errTenantRouteNotFound = errors.New("tenant route not found")

// applyNotRunningMessage is the apply_message of a route reported
// "not_applicable" because no reconciler runs (#304).
const applyNotRunningMessage = "not applied: tenant-route apply is not running on this server " +
	"(gateway.routes.apply.enabled is off, or Shepherd is not running in a Kubernetes cluster)"

// routeProto converts a row for a response, reporting a "pending" route as
// "not_applicable" when no reconciler is running to apply it — the contract
// TenantRoute.apply_status documents ("route apply disabled"). The stored row
// keeps "pending", so a reconciler that starts later still applies it.
func (s *TenantRouteService) routeProto(r sqlc.TenantRoute) *mgmtv1.TenantRoute {
	out := toTenantRouteProto(r)
	if out.GetApplyStatus() == mgmtv1.TenantRouteApplyStatus_TENANT_ROUTE_APPLY_STATUS_PENDING && (s.applyRunning == nil || !s.applyRunning()) {
		out.ApplyStatus = mgmtv1.TenantRouteApplyStatus_TENANT_ROUTE_APPLY_STATUS_NOT_APPLICABLE
		out.ApplyMessage = applyNotRunningMessage
	}
	return out
}

// routeStatusToProto is the one stored tenant_routes.status -> wire
// conversion. A stored value that is not a status reads UNSPECIFIED.
func routeStatusToProto(stored string) mgmtv1.TenantRouteStatus {
	st, err := gateway.ParseRouteStatus(stored)
	if err != nil {
		return mgmtv1.TenantRouteStatus_TENANT_ROUTE_STATUS_UNSPECIFIED
	}
	switch st {
	case gateway.RouteActive:
		return mgmtv1.TenantRouteStatus_TENANT_ROUTE_STATUS_ACTIVE
	case gateway.RouteDeprecated:
		return mgmtv1.TenantRouteStatus_TENANT_ROUTE_STATUS_DEPRECATED
	case gateway.RouteRevoked:
		return mgmtv1.TenantRouteStatus_TENANT_ROUTE_STATUS_REVOKED
	}
	return mgmtv1.TenantRouteStatus_TENANT_ROUTE_STATUS_UNSPECIFIED
}

// applyStatusToProto is the one stored tenant_routes.apply_status -> wire
// conversion. A stored value that is not a status reads UNSPECIFIED.
func applyStatusToProto(stored string) mgmtv1.TenantRouteApplyStatus {
	st, err := gateway.ParseApplyStatus(stored)
	if err != nil {
		return mgmtv1.TenantRouteApplyStatus_TENANT_ROUTE_APPLY_STATUS_UNSPECIFIED
	}
	switch st {
	case gateway.ApplyPending:
		return mgmtv1.TenantRouteApplyStatus_TENANT_ROUTE_APPLY_STATUS_PENDING
	case gateway.ApplyApplied:
		return mgmtv1.TenantRouteApplyStatus_TENANT_ROUTE_APPLY_STATUS_APPLIED
	case gateway.ApplyRefused:
		return mgmtv1.TenantRouteApplyStatus_TENANT_ROUTE_APPLY_STATUS_REFUSED
	case gateway.ApplyError:
		return mgmtv1.TenantRouteApplyStatus_TENANT_ROUTE_APPLY_STATUS_ERROR
	case gateway.ApplyRemoved:
		return mgmtv1.TenantRouteApplyStatus_TENANT_ROUTE_APPLY_STATUS_REMOVED
	case gateway.ApplyNotApplicable:
		return mgmtv1.TenantRouteApplyStatus_TENANT_ROUTE_APPLY_STATUS_NOT_APPLICABLE
	}
	return mgmtv1.TenantRouteApplyStatus_TENANT_ROUTE_APPLY_STATUS_UNSPECIFIED
}

// toTenantRouteProto converts a stored tenant_routes row to its proto
// representation.
func toTenantRouteProto(r sqlc.TenantRoute) *mgmtv1.TenantRoute {
	out := &mgmtv1.TenantRoute{
		Id:               r.ID.String(),
		OrgId:            r.OrgID.String(),
		TenantId:         r.TenantID,
		Kind:             r.Kind,
		Segment:          r.Segment,
		Status:           routeStatusToProto(r.Status),
		GatewayMode:      r.GatewayMode,
		GatewayName:      r.GatewayName,
		GatewayNamespace: r.GatewayNamespace,
		ValidUntil:       protoTimestamp(r.ValidUntil),
		CreatedAt:        protoTimestamp(r.CreatedAt),
		UpdatedAt:        protoTimestamp(r.UpdatedAt),
		RevokedAt:        protoTimestamp(r.RevokedAt),
		ApplyStatus:      applyStatusToProto(r.ApplyStatus),
		ApplyMessage:     r.ApplyMessage,
		AppliedAt:        protoTimestamp(r.AppliedAt),
	}
	if r.RotatedFromID.Valid {
		out.RotatedFromId = r.RotatedFromID.String()
	}
	return out
}

// validateKind reports whether kind is one of internal/gateway's two
// RouteKind values.
func validateKind(kind string) error {
	switch gateway.RouteKind(kind) {
	case gateway.KindOTLP, gateway.KindFaro:
		return nil
	default:
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("kind %q is neither %q nor %q", kind, gateway.KindOTLP, gateway.KindFaro))
	}
}

// validateGatewayMode reports whether mode is one of D8's two gateway
// ownership modes.
func validateGatewayMode(mode string) error {
	switch mode {
	case gatewayModeManaged, gatewayModeOperator:
		return nil
	default:
		return connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("gateway_mode %q is neither %q nor %q", mode, gatewayModeManaged, gatewayModeOperator))
	}
}

// segmentFormatOrDefault maps a request's format string to a
// gateway.SegmentFormat, defaulting empty to D9's documented default
// (slug-suffix).
func segmentFormatOrDefault(format string) (gateway.SegmentFormat, error) {
	if format == "" {
		return gateway.FormatSlugSuffix, nil
	}
	f := gateway.SegmentFormat(format)
	if !gateway.ValidFormat(f) {
		return "", connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("format %q is neither %q nor %q", format, gateway.FormatOpaque, gateway.FormatSlugSuffix))
	}
	return f, nil
}

// segmentExists wires internal/gateway.ExistsFunc to
// TenantRouteSegmentExists: the application-level uniqueness check
// GenerateSegment uses to turn most collisions into a cheap retry instead
// of a failed INSERT. The storage layer's UNIQUE(kind, segment) constraint
// (0009_tenant_routes) is the final backstop against a TOCTOU race between
// this check and the INSERT.
func (s *TenantRouteService) segmentExists(ctx context.Context, kind gateway.RouteKind, segment string) (bool, error) {
	return s.store.Queries.TenantRouteSegmentExists(ctx, sqlc.TenantRouteSegmentExistsParams{
		Kind:    string(kind),
		Segment: segment,
	})
}

// ListTenantRoutes lists tenant routes in an org.
func (s *TenantRouteService) ListTenantRoutes(ctx context.Context, req *connect.Request[mgmtv1.ListTenantRoutesRequest]) (*connect.Response[mgmtv1.ListTenantRoutesResponse], error) {
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	routes, err := s.store.Queries.ListTenantRoutesByOrg(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list tenant routes"))
	}
	items := make([]*mgmtv1.TenantRoute, len(routes))
	for i := range routes {
		items[i] = s.routeProto(routes[i])
	}
	return connect.NewResponse(&mgmtv1.ListTenantRoutesResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // org route counts never approach int32 overflow
}

// loadOwnedTenantRoute fetches a tenant route by id and enforces that it
// belongs to orgIDStr — same pattern as DestinationService's
// loadOwnedDestination (rpc_destination.go): the authz interceptor only
// proves the caller may act on the org NAMED IN THE REQUEST, so without
// this an org admin could rotate/revoke another org's route by pairing
// their own org id with its route id. A mismatch is NotFound rather than
// PermissionDenied so the response does not confirm the route exists.
func (s *TenantRouteService) loadOwnedTenantRoute(ctx context.Context, orgIDStr, idStr string) (sqlc.TenantRoute, error) {
	id, err := scanUUID(idStr)
	if err != nil {
		return sqlc.TenantRoute{}, err
	}
	orgID, err := scanUUID(orgIDStr)
	if err != nil {
		return sqlc.TenantRoute{}, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	r, err := s.store.Queries.GetTenantRouteByID(ctx, id)
	if err != nil {
		return sqlc.TenantRoute{}, connect.NewError(connect.CodeNotFound, errTenantRouteNotFound)
	}
	if r.OrgID != orgID {
		return sqlc.TenantRoute{}, connect.NewError(connect.CodeNotFound, errTenantRouteNotFound)
	}
	return r, nil
}

// CreateTenantRoute mints a new active tenant route. The segment is always
// generated server-side by internal/gateway.GenerateSegment — this request
// never accepts a caller-supplied segment (see CreateTenantRouteRequest's
// proto doc comment); this method's job is validating the rest of the
// request and persisting the result.
func (s *TenantRouteService) CreateTenantRoute(ctx context.Context, req *connect.Request[mgmtv1.CreateTenantRouteRequest]) (*connect.Response[mgmtv1.TenantRoute], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	if err := validateKind(req.Msg.GetKind()); err != nil {
		return nil, err
	}
	if err := validateGatewayMode(req.Msg.GetGatewayMode()); err != nil {
		return nil, err
	}
	// Kubernetes' own name rules (gateway.ValidateGatewayRef): a name no
	// Gateway object can have renders into an HTTPRoute the API server
	// accepts and that then never attaches (M6).
	gatewayName := strings.TrimSpace(req.Msg.GetGatewayName())
	gatewayNamespace := strings.TrimSpace(req.Msg.GetGatewayNamespace())
	if err := gateway.ValidateGatewayRef(gatewayName, gatewayNamespace); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	format, err := segmentFormatOrDefault(req.Msg.GetFormat())
	if err != nil {
		return nil, err
	}
	kind := gateway.RouteKind(req.Msg.GetKind())

	// The tenant must be one the ORG owns — an exact tenant of it, or a
	// tenant under one of its prefixes (gateway.OrgOwnsTenant) — and only an
	// app admin decides which those are. An org admin may create routes for
	// their own org; letting them name any tenant would let them name someone
	// else's, and a route that injects another org's X-Scope-OrgID is a
	// cross-tenant write the gateway would happily authorize. The ownership
	// check and the insert share one transaction holding the shared ownership
	// lock, so the tenant cannot be taken away in between.
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create tenant route"))
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op once committed
	txQ := s.store.Queries.WithTx(tx)
	tenantID, err := resolveRouteTenant(ctx, txQ, orgID, req.Msg.GetTenant())
	if err != nil {
		return nil, err
	}

	segment, err := gateway.GenerateSegment(ctx, format, kind, tenantID, s.segmentExists)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("minting tenant route segment: %w", err))
	}

	r, err := txQ.CreateTenantRoute(ctx, sqlc.CreateTenantRouteParams{
		OrgID:            orgID,
		TenantID:         tenantID,
		Kind:             string(kind),
		Segment:          segment,
		GatewayMode:      req.Msg.GetGatewayMode(),
		GatewayName:      gatewayName,
		GatewayNamespace: gatewayNamespace,
	})
	if err != nil {
		if isUniqueViolation(err) {
			// Either this tenant+kind already has an active route
			// (idx_tenant_routes_one_active — the caller should rotate, not
			// create again) or the generated segment collided on
			// (kind, segment), which GenerateSegment's own uniqueness check
			// already makes astronomically unlikely (see segment.go's
			// entropy comments). Either way, the caller-facing answer is the
			// same: this tenant/kind is already routed.
			return nil, connect.NewError(connect.CodeAlreadyExists,
				errors.New("tenant already has an active route for this kind — rotate the existing route instead of creating a new one"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create tenant route"))
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create tenant route"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), orgID, "tenant_route.create", "tenant_route", r.ID.String())
	return connect.NewResponse(s.routeProto(r)), nil
}

// resolveRouteTenant picks the tenant a new route injects: the requested
// one, which the org must own, or — when empty — the org's default. q must
// be the transaction the route is then written in; it takes the shared
// ownership lock.
func resolveRouteTenant(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, requested string) (string, error) {
	if err := lockOrgTenantsShared(ctx, q); err != nil {
		return "", err
	}
	org, err := q.GetOrgByID(ctx, orgID)
	if err != nil {
		return "", connect.NewError(connect.CodeNotFound, errors.New("org not found"))
	}
	rows, err := q.ListOrgTenants(ctx, orgID)
	if err != nil {
		return "", connect.NewError(connect.CodeInternal, errors.New("failed to load org tenants"))
	}
	tenant := strings.TrimSpace(requested)
	if tenant == "" {
		tenant = defaultTenant(rows)
		if tenant == "" {
			return "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
				"org %q has no default tenant, so a route that names none would have no %s value to "+
					"inject. Name one of the org's tenants, or ask an application administrator to give "+
					"the org a default tenant (Admin → Organisations)", org.Name, gateway.TenantHeader))
		}
		return tenant, nil
	}
	if !gateway.OrgOwnsTenant(toOrgTenantRules(rows), tenant) {
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(
			"tenant %q is not one org %q owns; a route may inject only one of the org's tenants, or a "+
				"tenant under one of its prefixes, which an application administrator assigns",
			tenant, org.Name))
	}
	return tenant, nil
}

// RotateTenantRoute demotes the currently-active route to "deprecated"
// (with a valid_until overlap deadline) and mints a new active route with
// rotated_from_id pointing back at it, in one transaction — deprecating the
// old route without ever creating its replacement would leave the tenant
// with no active route, so the two writes must succeed or fail together.
func (s *TenantRouteService) RotateTenantRoute(ctx context.Context, req *connect.Request[mgmtv1.RotateTenantRouteRequest]) (*connect.Response[mgmtv1.RotateTenantRouteResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	current, err := s.loadOwnedTenantRoute(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if current.Status != string(gateway.RouteActive) {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("tenant route %s is %q, not active — only an active route can be rotated", current.ID.String(), current.Status))
	}
	format, err := segmentFormatOrDefault(req.Msg.GetFormat())
	if err != nil {
		return nil, err
	}
	overlap := defaultRotationOverlap
	if req.Msg.GetOverlapSeconds() > 0 {
		overlap = time.Duration(req.Msg.GetOverlapSeconds()) * time.Second
	}
	kind := gateway.RouteKind(current.Kind)

	segment, err := gateway.GenerateSegment(ctx, format, kind, current.TenantID, s.segmentExists)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("minting tenant route segment: %w", err))
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to start rotation"))
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op once committed; rollback error on the success path is expected and harmless
	txQueries := s.store.Queries.WithTx(tx)

	// Deprecate first: idx_tenant_routes_one_active (0009_tenant_routes)
	// allows only one 'active' row per org+tenant+kind, so inserting the new
	// active row before demoting the old one would itself violate that
	// constraint.
	deprecated, err := txQueries.DeprecateTenantRoute(ctx, sqlc.DeprecateTenantRouteParams{
		ID:         current.ID,
		ValidUntil: pgtype.Timestamptz{Time: time.Now().Add(overlap), Valid: true},
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to deprecate current tenant route"))
	}

	active, err := txQueries.CreateTenantRoute(ctx, sqlc.CreateTenantRouteParams{
		OrgID:            current.OrgID,
		TenantID:         current.TenantID,
		Kind:             current.Kind,
		Segment:          segment,
		GatewayMode:      current.GatewayMode,
		GatewayName:      current.GatewayName,
		GatewayNamespace: current.GatewayNamespace,
		RotatedFromID:    current.ID,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create rotated tenant route"))
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to commit rotation"))
	}

	auditLog(ctx, s.store, actorFromCtx(ctx), current.OrgID, "tenant_route.rotate", "tenant_route", current.ID.String())
	return connect.NewResponse(&mgmtv1.RotateTenantRouteResponse{
		Active:     s.routeProto(active),
		Deprecated: s.routeProto(deprecated),
	}), nil
}

// RevokeTenantRoute immediately revokes a route (whatever its current
// status), regardless of any valid_until deadline — the "and then revoke
// it" half of a rotation, or an out-of-band incident-response revoke.
func (s *TenantRouteService) RevokeTenantRoute(ctx context.Context, req *connect.Request[mgmtv1.RevokeTenantRouteRequest]) (*connect.Response[mgmtv1.TenantRoute], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	owned, err := s.loadOwnedTenantRoute(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	r, err := s.store.Queries.RevokeTenantRoute(ctx, owned.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to revoke tenant route"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), r.OrgID, "tenant_route.revoke", "tenant_route", r.ID.String())
	return connect.NewResponse(s.routeProto(r)), nil
}

// RenderConnectApp renders internal/onboarding's artifacts for one of the
// org's routes. Only an active OTLP route qualifies: a deprecated one stops
// routing when its overlap ends, a revoked one already has, and Faro has no
// receiver (D10) — handing out any of those endpoints is handing out a 404.
// The endpoint itself comes from onboarding.BaseEndpoint, the same
// RouteSpec.PathPrefix the gateway's HTTPRoute matches on (G7).
func (s *TenantRouteService) RenderConnectApp(ctx context.Context, req *connect.Request[mgmtv1.RenderConnectAppRequest]) (*connect.Response[mgmtv1.RenderConnectAppResponse], error) {
	r, err := s.loadOwnedTenantRoute(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if r.Kind != string(gateway.KindOTLP) {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("only OTLP routes have connect-an-app snippets: Shepherd deploys no Faro receiver"))
	}
	if r.Status != string(gateway.RouteActive) {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("route is %s, so its endpoint will not (or soon will not) route — connect apps to the org's active route", r.Status))
	}
	serviceName := strings.TrimSpace(req.Msg.GetServiceName())
	if serviceName == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("service_name must not be empty"))
	}
	protocol := onboarding.Protocol(req.Msg.GetProtocol())
	if protocol == "" {
		protocol = onboarding.ProtocolHTTPProtobuf
	}
	if protocol != onboarding.ProtocolHTTPProtobuf && protocol != onboarding.ProtocolHTTPJSON {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("protocol %q is not %q or %q (OTLP/gRPC cannot carry the tenant path)", protocol,
				onboarding.ProtocolHTTPProtobuf, onboarding.ProtocolHTTPJSON))
	}
	baseURL := strings.TrimSpace(req.Msg.GetGatewayBaseUrl())
	if baseURL == "" {
		baseURL = s.publicBaseURL
	}
	if baseURL == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("no gateway URL: enter your gateway's public https URL, or have an operator set gateway.routes.public_base_url"))
	}

	spec := onboarding.ConnectAppSpec{
		GatewayBaseURL:   baseURL,
		Route:            gateway.RouteSpec{Kind: gateway.RouteKind(r.Kind), RouteSegment: r.Segment},
		ServiceName:      serviceName,
		Protocol:         protocol,
		IncludeADOTLayer: req.Msg.GetIncludeAdotLayer(),
	}
	b, err := onboarding.Render(spec)
	if err != nil {
		// The inputs above are checked; what remains is the URL's shape
		// (https, a host), which is the caller's to fix.
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	base, err := onboarding.BaseEndpoint(baseURL, spec.Route)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&mgmtv1.RenderConnectAppResponse{
		BaseEndpoint:   base,
		GatewayBaseUrl: baseURL,
		Env:            b.Env,
		Lambda:         b.Lambda,
		Terraform:      b.Terraform,
		Sam:            b.SAM,
		Cdk:            b.CDK,
		K8S:            b.K8s,
		SdkNotes:       b.SDKNotes,
	}), nil
}
