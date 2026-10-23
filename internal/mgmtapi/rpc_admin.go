package mgmtapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/auth"
	"shepherd/internal/gateway"
	"shepherd/internal/signals"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// AdminService implements mgmtv1connect.AdminServiceHandler.
type AdminService struct {
	store  *store.Store
	logger *slog.Logger

	// oidc owns the single sign-on configuration (see rpc_admin_oidc.go). It
	// is the live auth handler rather than a store, because saving settings
	// and ACTIVATING them are one operation from the admin's point of view —
	// the whole point of configuring a provider through the UI is not having
	// to restart to use it. nil in test wirings that mount the RPC surface
	// without a server; those procedures then answer CodeUnavailable.
	oidc *auth.Handler

	// tokenChanged runs after RevokeAgentToken or BindAgentToken commits,
	// with the token's canonical id: internal/server passes the collector
	// gate's outage-only verdict cache's Evict, so this replica stops acting
	// on the old verdict (and binding) at once, even while the database is
	// down (#374). Nil = nothing to evict.
	tokenChanged func(tokenID string)

	// identityChanged runs after CreateAgentIdentity or DeleteAgentIdentity
	// commits: internal/server passes agentapi.Service.ForgetAgentIdentity,
	// so this replica stops answering that OIDC caller from the outage
	// cache under the old binding (#385). Nil = nothing to forget.
	identityChanged func(issuer, appID string)
}

// AdminServiceOption configures optional AdminService dependencies. The
// options exist so the several tests that construct this service directly
// keep compiling unchanged as capabilities that need more than a store are
// added.
type AdminServiceOption func(*AdminService)

// WithOIDCHandler supplies the auth handler backing the OIDC settings
// procedures.
func WithOIDCHandler(h *auth.Handler) AdminServiceOption {
	return func(s *AdminService) { s.oidc = h }
}

// WithTokenChangedHook supplies the hook RevokeAgentToken and
// BindAgentToken call after their change commits.
func WithTokenChangedHook(f func(tokenID string)) AdminServiceOption {
	return func(s *AdminService) { s.tokenChanged = f }
}

// WithIdentityChangedHook supplies the hook CreateAgentIdentity and
// DeleteAgentIdentity call after their change commits.
func WithIdentityChangedHook(f func(issuer, appID string)) AdminServiceOption {
	return func(s *AdminService) { s.identityChanged = f }
}

// NewAdminService constructs an AdminService.
func NewAdminService(st *store.Store, logger *slog.Logger, opts ...AdminServiceOption) *AdminService {
	s := &AdminService{store: st, logger: logger}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

var _ mgmtv1connect.AdminServiceHandler = (*AdminService)(nil)

func toOrgProto(o sqlc.Org, tenants []sqlc.OrgTenant) *mgmtv1.Org {
	return &mgmtv1.Org{
		Id:            o.ID.String(),
		Name:          o.Name,
		DisplayName:   o.DisplayName,
		AdminGroupId:  o.AdminGroupID,
		ViewerGroupId: o.ViewerGroupID.String,
		EditorGroupId: o.EditorGroupID.String,
		CreatedAt:     protoTimestamp(o.CreatedAt),
		UpdatedAt:     protoTimestamp(o.UpdatedAt),
		Tenants:       toOrgTenantProtos(tenants),

		AllowExperimentalComponents: o.AllowExperimentalComponents,
		AllowLabelMatching:          o.AllowLabelMatching,
		AllowLocalAttributeMatching: o.AllowLocalAttributeMatching,
	}
}

func toClusterProto(c sqlc.Cluster) *mgmtv1.Cluster {
	return &mgmtv1.Cluster{
		Id:        c.ID.String(),
		Name:      c.Name,
		OrgId:     c.OrgID.String(),
		CreatedAt: protoTimestamp(c.CreatedAt),
	}
}

func toAgentTokenProto(t sqlc.AgentToken, orgName string) *mgmtv1.AgentToken {
	status := "active"
	if t.RevokedAt.Valid {
		status = "revoked"
	}
	orgID := ""
	if t.OrgID.Valid {
		orgID = t.OrgID.String()
	}
	return &mgmtv1.AgentToken{
		Id:        t.ID.String(),
		Name:      t.Name,
		CreatedBy: t.CreatedBy,
		Status:    status,
		CreatedAt: protoTimestamp(t.CreatedAt),
		OrgId:     orgID,
		OrgName:   orgName,
		Clusters:  decodeStringArray(t.Clusters),
		Roles:     decodeStringArray(t.Roles),
		MayClaim:  t.MayClaim,
	}
}

// ListOrgs lists all orgs.
func (s *AdminService) ListOrgs(ctx context.Context, _ *connect.Request[mgmtv1.ListOrgsRequest]) (*connect.Response[mgmtv1.ListOrgsResponse], error) {
	orgs, err := s.store.Queries.ListOrgs(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list orgs"))
	}
	allTenants, err := s.store.Queries.ListAllOrgTenants(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list org tenants"))
	}
	byOrg := groupOrgTenants(allTenants)
	items := make([]*mgmtv1.Org, len(orgs))
	for i := range orgs {
		items[i] = toOrgProto(orgs[i], byOrg[orgs[i].ID])
	}
	return connect.NewResponse(&mgmtv1.ListOrgsResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // org counts never approach int32 overflow
}

// CreateOrg creates an org.
func (s *AdminService) CreateOrg(ctx context.Context, req *connect.Request[mgmtv1.CreateOrgRequest]) (*connect.Response[mgmtv1.Org], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	msg := req.Msg
	if msg.GetName() == "" || msg.GetDisplayName() == "" || msg.GetAdminGroupId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name, display_name, admin_group_id required"))
	}
	// The initial tenant is optional; when given it becomes the org's
	// default exact tenant, under AddOrgTenant's rules (assignOrgTenant).
	var initial gateway.OrgTenant
	if t := strings.TrimSpace(msg.GetTenantId()); t != "" {
		initial = gateway.OrgTenant{Tenant: t, Kind: gateway.TenantExact}
		if err := gateway.ValidateOrgTenant(initial); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create org"))
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op once committed
	txQ := s.store.Queries.WithTx(tx)
	if initial.Tenant != "" {
		if err := lockOrgTenantsExclusive(ctx, txQ); err != nil {
			return nil, err
		}
	}
	o, err := txQ.CreateOrg(ctx, sqlc.CreateOrgParams{
		Name:          msg.GetName(),
		DisplayName:   msg.GetDisplayName(),
		AdminGroupID:  msg.GetAdminGroupId(),
		ViewerGroupID: pgtype.Text{String: msg.GetViewerGroupId(), Valid: msg.GetViewerGroupId() != ""},
		EditorGroupID: pgtype.Text{String: msg.GetEditorGroupId(), Valid: msg.GetEditorGroupId() != ""},
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("org name already exists"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create org"))
	}
	if initial.Tenant != "" {
		if err := assignOrgTenant(ctx, txQ, o.ID, initial, true, actorFromCtx(ctx)); err != nil {
			return nil, err
		}
		if err := auditOrgTenantChange(ctx, txQ, o.ID, "org.tenant.add",
			orgTenantDetail{Tenant: initial.Tenant, Kind: string(initial.Kind), IsDefault: true}); err != nil {
			return nil, err
		}
	}
	out, err := orgProtoWithTenants(ctx, txQ, o)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create org"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), o.ID, "org.create", "org", o.ID.String())
	return connect.NewResponse(out), nil
}

// UpdateOrg updates an org.
func (s *AdminService) UpdateOrg(ctx context.Context, req *connect.Request[mgmtv1.UpdateOrgRequest]) (*connect.Response[mgmtv1.Org], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	msg := req.Msg
	id, err := scanUUID(msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	// CreateOrg refuses an empty admin group; UpdateOrg did not, so an org
	// could be edited into admin_group_id "". That matters because the admin
	// check is a plain slices.Contains against the session's group ids, and a
	// group id of "" would then match any session carrying one.
	if strings.TrimSpace(msg.GetAdminGroupId()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("admin_group_id required"))
	}
	// Every check above runs before anything is written. The update and its
	// org.update audit row then commit together (#302): the row used to be a
	// best-effort insert after the update had committed, so an edit could land
	// without its audit row. An audit insert that fails now rolls the edit back.
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to start org update"))
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op once committed; rollback error on the success path is expected and harmless
	txQ := s.store.Queries.WithTx(tx)

	o, err := txQ.UpdateOrg(ctx, sqlc.UpdateOrgParams{
		ID:                          id,
		DisplayName:                 msg.GetDisplayName(),
		AdminGroupID:                msg.GetAdminGroupId(),
		ViewerGroupID:               pgtype.Text{String: msg.GetViewerGroupId(), Valid: msg.GetViewerGroupId() != ""},
		EditorGroupID:               pgtype.Text{String: msg.GetEditorGroupId(), Valid: msg.GetEditorGroupId() != ""},
		AllowExperimentalComponents: msg.GetAllowExperimentalComponents(),
		AllowLabelMatching:          msg.GetAllowLabelMatching(),
		AllowLocalAttributeMatching: msg.GetAllowLocalAttributeMatching(),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("org not found"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update org"))
	}
	if err := auditInsert(ctx, txQ, actorFromCtx(ctx), "user", o.ID, "org.update", "org", o.ID.String(), nil); err != nil {
		s.logger.Warn("update org: audit insert failed, rolling back", "org_id", o.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to record org update"))
	}
	out, err := orgProtoWithTenants(ctx, txQ, o)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to commit org update"))
	}
	return connect.NewResponse(out), nil
}

// DeleteOrg deletes an org.
func (s *AdminService) DeleteOrg(ctx context.Context, req *connect.Request[mgmtv1.DeleteOrgRequest]) (*connect.Response[mgmtv1.DeleteOrgResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	id, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	content, err := s.store.Queries.CountOrgContent(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to check org content"))
	}
	if content.ClusterCount > 0 || content.PipelineCount > 0 {
		msg := fmt.Sprintf("org has %d clusters, %d pipelines", content.ClusterCount, content.PipelineCount)
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New(msg))
	}

	if err := s.store.Queries.DeleteOrg(ctx, id); err != nil {
		if isFKViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("org still has references"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete org"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), id, "org.delete", "org", id.String())
	return connect.NewResponse(&mgmtv1.DeleteOrgResponse{}), nil
}

// errClusterNotFound is returned by ClaimCluster/UnclaimCluster when the
// named cluster genuinely does not exist (GetClusterByName's error was
// pgx.ErrNoRows specifically) — any other lookup failure is mapError(err)
// instead, so a connection error or similar is never misreported to the
// caller as "this cluster does not exist".
var errClusterNotFound = errors.New("cluster not found")

// ListClusters lists all clusters, or only unclaimed clusters when
// unclaimed=true (matching the legacy ?unclaimed=true query param).
func (s *AdminService) ListClusters(ctx context.Context, req *connect.Request[mgmtv1.ListClustersRequest]) (*connect.Response[mgmtv1.ListClustersResponse], error) {
	if req.Msg.GetUnclaimed() {
		clusters, _ := s.store.Queries.ListUnclaimedClusters(ctx) //nolint:errcheck // empty list is safe fallback
		items := make([]*mgmtv1.Cluster, len(clusters))
		for i := range clusters {
			items[i] = toClusterProto(clusters[i])
		}
		return connect.NewResponse(&mgmtv1.ListClustersResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // cluster counts never approach int32 overflow
	}
	clusters, err := s.store.Queries.ListAllClusters(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list clusters"))
	}
	items := make([]*mgmtv1.Cluster, len(clusters))
	for i := range clusters {
		items[i] = toClusterProto(clusters[i])
	}
	return connect.NewResponse(&mgmtv1.ListClustersResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // cluster counts never approach int32 overflow
}

// ClaimCluster assigns a cluster to an org.
func (s *AdminService) ClaimCluster(ctx context.Context, req *connect.Request[mgmtv1.ClaimClusterRequest]) (*connect.Response[mgmtv1.ClaimClusterResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	msg := req.Msg
	cluster, err := s.store.Queries.GetClusterByName(ctx, msg.GetCluster())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errClusterNotFound)
		}
		return nil, mapError(err)
	}
	orgID, err := scanUUID(msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	if err := s.store.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to claim cluster"))
	}
	s.logger.Info("cluster claimed", "cluster_id", cluster.ID, "org_id", orgID)
	auditLog(ctx, s.store, actorFromCtx(ctx), orgID, "cluster.claim", "cluster", cluster.ID.String())
	return connect.NewResponse(&mgmtv1.ClaimClusterResponse{Status: "claimed"}), nil
}

// UnclaimCluster removes a cluster's org assignment and marks its
// collectors' serve cache dirty. Both changes happen in one transaction: if
// the dirty-marking half fails, an unclaimed-but-not-dirtied cluster would
// leave its collectors serving the previous org's config indefinitely with
// no visible error, so the whole unclaim rolls back and the caller sees the
// failure instead of a false "unclaimed" success.
func (s *AdminService) UnclaimCluster(ctx context.Context, req *connect.Request[mgmtv1.UnclaimClusterRequest]) (*connect.Response[mgmtv1.UnclaimClusterResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	cluster, err := s.store.Queries.GetClusterByName(ctx, req.Msg.GetCluster())
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errClusterNotFound)
		}
		return nil, mapError(err)
	}

	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to start unclaim"))
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op once committed; rollback error on the success path is expected and harmless
	txQueries := s.store.Queries.WithTx(tx)

	if err := txQueries.UnclaimCluster(ctx, cluster.ID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to unclaim cluster"))
	}
	if err := txQueries.MarkServeCacheDirtyByCluster(ctx, cluster.ID); err != nil {
		s.logger.Warn("unclaim: failed to mark serve_cache dirty, rolling back", "cluster_id", cluster.ID, "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to mark serve cache dirty"))
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to commit unclaim"))
	}

	s.logger.Info("cluster unclaimed", "cluster_id", cluster.ID)
	// Audited against the org that just lost the cluster, so it shows in that org's log.
	auditLog(ctx, s.store, actorFromCtx(ctx), cluster.OrgID, "cluster.unclaim", "cluster", cluster.ID.String())
	return connect.NewResponse(&mgmtv1.UnclaimClusterResponse{Status: "unclaimed"}), nil
}

// ListAgentTokens lists agent tokens. With org_id set (an org admin's call,
// or an app admin's filtered view) only the tokens bound to that org; empty
// (app admin only — the interceptor refuses anyone else) every token,
// unbound ones first.
func (s *AdminService) ListAgentTokens(ctx context.Context, req *connect.Request[mgmtv1.ListAgentTokensRequest]) (*connect.Response[mgmtv1.ListAgentTokensResponse], error) {
	orgID, err := optionalOrgID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	rows, err := s.store.Queries.ListAgentTokens(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list tokens"))
	}
	items := make([]*mgmtv1.AgentToken, len(rows))
	for i := range rows {
		items[i] = toAgentTokenProto(rows[i].AgentToken, rows[i].OrgName.String)
	}
	return connect.NewResponse(&mgmtv1.ListAgentTokensResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // token counts never approach int32 overflow
}

// optionalOrgID parses an optional org id: "" is the NULL uuid (no org
// scope), anything else must be a valid uuid.
func optionalOrgID(raw string) (pgtype.UUID, error) {
	if raw == "" {
		return pgtype.UUID{}, nil
	}
	return scanUUID(raw)
}

// validateRoleAllowlist checks a binding's role allowlist with
// signals.ValidateRole, the same check internal/agentapi's requireClusterRole
// applies when a collector registers: a role outside signals.Policies can
// never register, so an allowlist naming "bogusrole" would be stored and then
// admit no collector on it, silently (M6). Cluster names are free-form — any
// string a collector reports — so they are not checked.
func validateRoleAllowlist(roles []string) error {
	for _, role := range roles {
		if role = strings.TrimSpace(role); role == "" {
			continue
		}
		if err := signals.ValidateRole(role); err != nil {
			return connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	return nil
}

// tokenBindingOrg resolves and checks the org a token is being bound to: it
// is required, and must exist.
func (s *AdminService) tokenBindingOrg(ctx context.Context, raw string) (sqlc.Org, error) {
	if raw == "" {
		return sqlc.Org{}, connect.NewError(connect.CodeInvalidArgument, errors.New("org_id required: every agent token is bound to an organisation"))
	}
	id, err := scanUUID(raw)
	if err != nil {
		return sqlc.Org{}, err
	}
	org, err := s.store.Queries.GetOrgByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlc.Org{}, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown organisation"))
	}
	if err != nil {
		return sqlc.Org{}, mapError(err)
	}
	return org, nil
}

// CreateAgentToken creates an agent token bound to org_id, with optional
// cluster/role allowlists. The plaintext secret is returned exactly once in
// the response and is never logged or persisted — only its SHA-256 hash is
// stored. The interceptor has already checked the caller is an app admin or
// an admin of org_id, so an org admin can only mint tokens for their own org.
func (s *AdminService) CreateAgentToken(ctx context.Context, req *connect.Request[mgmtv1.CreateAgentTokenRequest]) (*connect.Response[mgmtv1.CreateAgentTokenResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name required"))
	}
	if err := validateRoleAllowlist(req.Msg.GetRoles()); err != nil {
		return nil, err
	}
	org, err := s.tokenBindingOrg(ctx, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	actor := actorFromCtx(ctx)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to generate secret"))
	}
	secret := base64.URLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(secret))
	tok, err := s.store.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
		Name: name, TokenHash: hash[:], CreatedBy: actor, OrgID: org.ID,
		Clusters: encodeStringArray(req.Msg.GetClusters()),
		Roles:    encodeStringArray(req.Msg.GetRoles()),
		MayClaim: mintedByAppAdmin(ctx),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create token"))
	}
	auditLog(ctx, s.store, actor, org.ID, "agent_token.create", "agent_token", tok.ID.String())
	return connect.NewResponse(&mgmtv1.CreateAgentTokenResponse{
		Id:     tok.ID.String(),
		Name:   tok.Name,
		Secret: secret,
	}), nil
}

// mintedByAppAdmin reports whether the caller minting a token is an app
// admin — the one caller whose tokens may claim an unclaimed cluster for
// their org (agent_tokens.may_claim). Claiming a cluster into an org is an
// app-admin decision (spec §7.2), so a token an org admin or a service
// account (never app admin) mints only serves clusters its org already
// claimed.
func mintedByAppAdmin(ctx context.Context) bool {
	if _, ok := serviceAccountFromCtx(ctx); ok {
		return false
	}
	sess := auth.SessionFromCtx(ctx)
	return sess != nil && sess.IsAppAdmin
}

// RevokeAgentToken revokes an agent token. With org_id set only a token bound
// to that org matches — another org's token, or an unbound one, is NotFound,
// indistinguishable from an id that does not exist.
func (s *AdminService) RevokeAgentToken(ctx context.Context, req *connect.Request[mgmtv1.RevokeAgentTokenRequest]) (*connect.Response[mgmtv1.RevokeAgentTokenResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	id, err := scanUUID(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	orgID, err := optionalOrgID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	tok, err := s.store.Queries.RevokeAgentToken(ctx, sqlc.RevokeAgentTokenParams{ID: id, OrgID: orgID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such agent token"))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to revoke token"))
	}
	if s.tokenChanged != nil {
		s.tokenChanged(id.String())
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), tok.OrgID, "agent_token.revoke", "agent_token", id.String())
	return connect.NewResponse(&mgmtv1.RevokeAgentTokenResponse{}), nil
}

// BindAgentToken binds a live token to an organisation, or rebinds it, and
// replaces its allowlists — in place, so the secret (and every collector
// using it) keeps working. App admin only, so a bound token may claim
// clusters (may_claim, set by the query). Audited against the new org, with
// the previous org in the detail.
func (s *AdminService) BindAgentToken(ctx context.Context, req *connect.Request[mgmtv1.BindAgentTokenRequest]) (*connect.Response[mgmtv1.AgentToken], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	id, err := scanUUID(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := validateRoleAllowlist(req.Msg.GetRoles()); err != nil {
		return nil, err
	}
	org, err := s.tokenBindingOrg(ctx, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	prev, err := s.store.Queries.GetAgentTokenByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such live agent token"))
	}
	if err != nil {
		return nil, mapError(err)
	}
	tok, err := s.store.Queries.BindAgentToken(ctx, sqlc.BindAgentTokenParams{
		ID:       id,
		OrgID:    org.ID,
		Clusters: encodeStringArray(req.Msg.GetClusters()),
		Roles:    encodeStringArray(req.Msg.GetRoles()),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such live agent token"))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to bind token"))
	}
	if s.tokenChanged != nil {
		s.tokenChanged(id.String())
	}
	previousOrg := ""
	if prev.OrgID.Valid {
		previousOrg = prev.OrgID.String()
	}
	auditLogDetail(ctx, s.store, actorFromCtx(ctx), "user", org.ID, "agent_token.bind", "agent_token", id.String(), map[string]any{
		"org_id":          org.ID.String(),
		"previous_org_id": previousOrg,
		"clusters":        decodeStringArray(tok.Clusters),
		"roles":           decodeStringArray(tok.Roles),
	})
	return connect.NewResponse(toAgentTokenProto(tok, org.Name)), nil
}

// --- Collector OIDC identity bindings (docs/archive/plans/2026-09-16-agent-oidc-auth.md) ---

func decodeStringArray(raw json.RawMessage) []string {
	var out []string
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out) //nolint:errcheck // a malformed allowlist reads as empty ("any")
	}
	return out
}

func encodeStringArray(in []string) json.RawMessage {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	b, _ := json.Marshal(out) //nolint:errcheck // marshaling a []string cannot fail
	return b
}

// ListAgentIdentities lists the collector-OIDC bindings, with the org slug.
func (s *AdminService) ListAgentIdentities(ctx context.Context, _ *connect.Request[mgmtv1.ListAgentIdentitiesRequest]) (*connect.Response[mgmtv1.ListAgentIdentitiesResponse], error) {
	rows, _ := s.store.Queries.ListAgentIdentities(ctx) //nolint:errcheck // empty list is a safe fallback
	items := make([]*mgmtv1.AgentIdentity, len(rows))
	for i := range rows {
		items[i] = &mgmtv1.AgentIdentity{
			Issuer:    rows[i].Issuer,
			AppId:     rows[i].AppID,
			OrgId:     rows[i].OrgID.String(),
			OrgName:   rows[i].OrgName,
			Clusters:  decodeStringArray(rows[i].Clusters),
			Roles:     decodeStringArray(rows[i].Roles),
			CreatedBy: rows[i].CreatedBy,
			CreatedAt: protoTimestamp(rows[i].CreatedAt),
		}
	}
	return connect.NewResponse(&mgmtv1.ListAgentIdentitiesResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // binding counts never approach int32 overflow
}

// CreateAgentIdentity binds an OIDC identity (issuer + app_id) to an org.
func (s *AdminService) CreateAgentIdentity(ctx context.Context, req *connect.Request[mgmtv1.CreateAgentIdentityRequest]) (*connect.Response[mgmtv1.AgentIdentity], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	issuer := strings.TrimSpace(req.Msg.GetIssuer())
	appID := strings.TrimSpace(req.Msg.GetAppId())
	if issuer == "" || appID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("issuer and app_id are required"))
	}
	if err := validateRoleAllowlist(req.Msg.GetRoles()); err != nil {
		return nil, err
	}
	org, err := s.store.Queries.GetOrgByName(ctx, strings.TrimSpace(req.Msg.GetOrg()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown organisation %q", req.Msg.GetOrg()))
	}
	created, err := s.store.Queries.CreateAgentIdentity(ctx, sqlc.CreateAgentIdentityParams{
		Issuer:    issuer,
		AppID:     appID,
		OrgID:     org.ID,
		Clusters:  encodeStringArray(req.Msg.GetClusters()),
		Roles:     encodeStringArray(req.Msg.GetRoles()),
		CreatedBy: actorFromCtx(ctx),
	})
	if err != nil {
		return nil, mapError(err)
	}
	if s.identityChanged != nil {
		s.identityChanged(created.Issuer, created.AppID)
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), org.ID, "agent_identity.create", "agent_identity", issuer+"|"+appID)
	return connect.NewResponse(&mgmtv1.AgentIdentity{
		Issuer:    created.Issuer,
		AppId:     created.AppID,
		OrgId:     created.OrgID.String(),
		OrgName:   org.Name,
		Clusters:  decodeStringArray(created.Clusters),
		Roles:     decodeStringArray(created.Roles),
		CreatedBy: created.CreatedBy,
		CreatedAt: protoTimestamp(created.CreatedAt),
	}), nil
}

// DeleteAgentIdentity removes a binding by (issuer, app_id).
func (s *AdminService) DeleteAgentIdentity(ctx context.Context, req *connect.Request[mgmtv1.DeleteAgentIdentityRequest]) (*connect.Response[mgmtv1.DeleteAgentIdentityResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	issuer := strings.TrimSpace(req.Msg.GetIssuer())
	appID := strings.TrimSpace(req.Msg.GetAppId())
	n, err := s.store.Queries.DeleteAgentIdentity(ctx, sqlc.DeleteAgentIdentityParams{Issuer: issuer, AppID: appID})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete binding"))
	}
	if n == 0 {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such binding"))
	}
	if s.identityChanged != nil {
		s.identityChanged(issuer, appID)
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), pgtype.UUID{}, "agent_identity.delete", "agent_identity", issuer+"|"+appID)
	return connect.NewResponse(&mgmtv1.DeleteAgentIdentityResponse{}), nil
}

// SearchGroups searches Entra groups by display-name prefix. Stubbed to
// match the pre-Connect REST handler exactly: no Graph client is threaded through
// the server anywhere today, so this always returns an empty list rather
// than guessing at behavior. Implementing it needs both the wiring change
// and an app-mode search call on internal/graph's Client (the unused cached
// implementation that once lived there has been deleted as dead code).
func (s *AdminService) SearchGroups(_ context.Context, _ *connect.Request[mgmtv1.SearchGroupsRequest]) (*connect.Response[mgmtv1.SearchGroupsResponse], error) {
	return connect.NewResponse(&mgmtv1.SearchGroupsResponse{Items: []*mgmtv1.GroupSearchResult{}, Total: 0}), nil
}
