package mgmtapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/auth"
	"shepherd/internal/store"
)

// MeService implements mgmtv1connect.MeServiceHandler.
type MeService struct {
	store  *store.Store
	logger *slog.Logger
	// requireOwned is the server-wide tenants.require_owned setting
	// (OrgMembership.tenants_required).
	requireOwned bool
}

// MeServiceOption configures optional MeService behaviour.
type MeServiceOption func(*MeService)

// WithMeRequireOwnedTenants passes the server-wide tenants.require_owned
// setting, which OrgMembership.tenants_required reports.
func WithMeRequireOwnedTenants(on bool) MeServiceOption {
	return func(s *MeService) { s.requireOwned = on }
}

// NewMeService constructs a MeService.
func NewMeService(st *store.Store, logger *slog.Logger, opts ...MeServiceOption) *MeService {
	s := &MeService{store: st, logger: logger}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

var _ mgmtv1connect.MeServiceHandler = (*MeService)(nil)

// noStoreCacheControl is the Cache-Control value GetMe has always returned
// (the pre-Connect REST handler did the same): authentication state must never be cached.
// MountRPC's procedureRequirements grants GetMe to any authenticated
// session (auth.RequireAuthenticated()), so the authz interceptor already enforces
// authentication for direct Connect callers; this method sets the same
// header on the Connect response (connect.Response.Header()) so that wire
// dialect also conveys the no-caching intent. The nil-session guard below
// is defence in depth for any caller that reaches this method without the
// interceptor (the removed /api REST shim was one).
const noStoreCacheControl = "no-store, no-cache, must-revalidate"

// GetMe returns the caller's identity and org memberships.
func (s *MeService) GetMe(ctx context.Context, _ *connect.Request[mgmtv1.GetMeRequest]) (*connect.Response[mgmtv1.GetMeResponse], error) {
	sess := auth.SessionFromCtx(ctx)
	if sess == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not authenticated"))
	}
	authMethod := "oidc"
	if sess.Source == "local" || sess.Source == "dev" {
		authMethod = sess.Source
	}
	allOrgs, err := s.store.Queries.ListOrgs(ctx)
	if err != nil {
		s.logger.Error("me: listing orgs", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list orgs"))
	}
	allTenants, err := s.store.Queries.ListAllOrgTenants(ctx)
	if err != nil {
		s.logger.Error("me: listing org tenants", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list orgs"))
	}
	tenantsByOrg := groupOrgTenants(allTenants)
	orgEntries := make([]*mgmtv1.OrgMembership, 0, len(allOrgs))
	for i := range allOrgs {
		org := &allOrgs[i]
		if sess.IsAppAdmin {
			orgEntries = append(orgEntries, &mgmtv1.OrgMembership{Id: org.ID.String(), Name: org.Name, DisplayName: org.DisplayName, Role: mgmtv1.Role_ROLE_ADMIN, AllowExperimentalComponents: org.AllowExperimentalComponents, AllowLabelMatching: org.AllowLabelMatching, AllowLocalAttributeMatching: org.AllowLocalAttributeMatching, Tenants: toOrgTenantProtos(tenantsByOrg[org.ID]), TenantsRequired: org.StrictTenants || s.requireOwned})
			continue
		}
		// Both sign-in paths resolve to the SAME Role here, because this
		// value drives what the UI offers. Reporting viewer to a local editor
		// would hide the pipeline editor from someone who can in fact use it
		// — the server would allow the write the UI never offered.
		if role, ok := auth.ResolveOrgRole(ctx, s.store, sess, *org); ok {
			orgEntries = append(orgEntries, &mgmtv1.OrgMembership{Id: org.ID.String(), Name: org.Name, DisplayName: org.DisplayName, Role: roleToProto(role), AllowExperimentalComponents: org.AllowExperimentalComponents, AllowLabelMatching: org.AllowLabelMatching, AllowLocalAttributeMatching: org.AllowLocalAttributeMatching, Tenants: toOrgTenantProtos(tenantsByOrg[org.ID]), TenantsRequired: org.StrictTenants || s.requireOwned})
		}
	}

	resp := connect.NewResponse(&mgmtv1.GetMeResponse{
		UserOid:     sess.UserOID,
		Email:       sess.Email,
		DisplayName: sess.DisplayName,
		IsAppAdmin:  sess.IsAppAdmin,
		AuthMethod:  authMethod,
		Orgs:        orgEntries,
	})
	resp.Header().Set("Cache-Control", noStoreCacheControl)
	return resp, nil
}
