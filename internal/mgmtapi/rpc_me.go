package mgmtapi

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"connectrpc.com/connect"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/auth"
	"shepherd/internal/store"
)

// MeService implements mgmtv1connect.MeServiceHandler. Business logic moved
// here from OrgsHandler.Me (orgs.go), which is now a thin REST shim
// delegating to this method in-process.
type MeService struct {
	store  *store.Store
	logger *slog.Logger
}

// NewMeService constructs a MeService with the deps OrgsHandler.Me uses today.
func NewMeService(st *store.Store, logger *slog.Logger) *MeService {
	return &MeService{store: st, logger: logger}
}

var _ mgmtv1connect.MeServiceHandler = (*MeService)(nil)

// noStoreCacheControl is the Cache-Control value GetMe has always returned
// (see legacy OrgsHandler.Me): authentication state must never be cached.
// MountRPC's procedureRequirements grants GetMe to any authenticated
// session (auth.RoleAny), so the authz interceptor already enforces
// authentication for direct Connect callers; this method sets the same
// header on the Connect response (connect.Response.Header()) so that wire
// dialect also conveys the no-caching intent, and repeats the nil-session
// guard below so the REST shim — which calls this method directly,
// bypassing the interceptor — keeps its own 401 behavior unchanged.
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
	orgEntries := make([]*mgmtv1.OrgMembership, 0, len(allOrgs))
	for i := range allOrgs {
		org := &allOrgs[i]
		if sess.IsAppAdmin {
			orgEntries = append(orgEntries, &mgmtv1.OrgMembership{Id: org.ID.String(), Name: org.Name, DisplayName: org.DisplayName, Role: "admin"})
			continue
		}
		role := ""
		if slices.Contains(sess.GroupIDs, org.AdminGroupID) {
			role = "admin"
		} else if org.ReaderGroupID.Valid && slices.Contains(sess.GroupIDs, org.ReaderGroupID.String) {
			role = "reader"
		}
		if role != "" {
			orgEntries = append(orgEntries, &mgmtv1.OrgMembership{Id: org.ID.String(), Name: org.Name, DisplayName: org.DisplayName, Role: role})
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
