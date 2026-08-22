package mgmtapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/gateway"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// TenantRouteService implements mgmtv1connect.TenantRouteServiceHandler:
// create/list/rotate/revoke for tenant route records (W4,
// docs/gateway-tier-plan.md §4). It owns the generation POLICY wiring
// (internal/gateway.GenerateSegment, D9) and persistence
// (0009_tenant_routes, D8's two gateway_mode values); it does not touch
// Kubernetes at all — rendering (internal/gateway.RenderHTTPRoute already
// exists) and the actual apply are explicitly a later slice (plan §5's W4
// boundary).
type TenantRouteService struct {
	store  *store.Store
	logger *slog.Logger
}

// NewTenantRouteService constructs a TenantRouteService.
func NewTenantRouteService(st *store.Store, logger *slog.Logger) *TenantRouteService {
	return &TenantRouteService{store: st, logger: logger}
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

// toTenantRouteProto converts a stored tenant_routes row to its proto
// representation.
func toTenantRouteProto(r sqlc.TenantRoute) *mgmtv1.TenantRoute {
	out := &mgmtv1.TenantRoute{
		Id:               r.ID.String(),
		OrgId:            r.OrgID.String(),
		TenantId:         r.TenantID,
		Kind:             r.Kind,
		Segment:          r.Segment,
		Status:           r.Status,
		GatewayMode:      r.GatewayMode,
		GatewayName:      r.GatewayName,
		GatewayNamespace: r.GatewayNamespace,
		ValidUntil:       protoTimestamp(r.ValidUntil),
		CreatedAt:        protoTimestamp(r.CreatedAt),
		UpdatedAt:        protoTimestamp(r.UpdatedAt),
		RevokedAt:        protoTimestamp(r.RevokedAt),
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
		items[i] = toTenantRouteProto(routes[i])
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
	// Tenant identity comes from the ORG, never from the request. An org
	// admin may create routes for their own org; letting them name the tenant
	// would let them name someone else's, and a route that injects another
	// org's X-Scope-OrgID is a cross-tenant write the gateway would happily
	// authorize. See 0013_org_tenant_id.
	org, err := s.store.Queries.GetOrgByID(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("org not found"))
	}
	if !org.TenantID.Valid || org.TenantID.String == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"org %q has no tenant identity yet, so a route for it would have no %s value to inject. "+
				"An application administrator sets this once, on the org (SetOrgTenantID)",
			org.Name, gateway.TenantHeader))
	}
	tenantID := org.TenantID.String
	if err := validateKind(req.Msg.GetKind()); err != nil {
		return nil, err
	}
	if err := validateGatewayMode(req.Msg.GetGatewayMode()); err != nil {
		return nil, err
	}
	if req.Msg.GetGatewayName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("gateway_name must not be empty"))
	}
	format, err := segmentFormatOrDefault(req.Msg.GetFormat())
	if err != nil {
		return nil, err
	}
	kind := gateway.RouteKind(req.Msg.GetKind())

	segment, err := gateway.GenerateSegment(ctx, format, kind, tenantID, s.segmentExists)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("minting tenant route segment: %w", err))
	}

	r, err := s.store.Queries.CreateTenantRoute(ctx, sqlc.CreateTenantRouteParams{
		OrgID:            orgID,
		TenantID:         tenantID,
		Kind:             string(kind),
		Segment:          segment,
		GatewayMode:      req.Msg.GetGatewayMode(),
		GatewayName:      req.Msg.GetGatewayName(),
		GatewayNamespace: req.Msg.GetGatewayNamespace(),
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
	return connect.NewResponse(toTenantRouteProto(r)), nil
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
	if current.Status != "active" {
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

	return connect.NewResponse(&mgmtv1.RotateTenantRouteResponse{
		Active:     toTenantRouteProto(active),
		Deprecated: toTenantRouteProto(deprecated),
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
	return connect.NewResponse(toTenantRouteProto(r)), nil
}
