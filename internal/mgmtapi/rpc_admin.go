package mgmtapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/gateway"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// AdminService implements mgmtv1connect.AdminServiceHandler. Business logic
// moved here from AdminHandler (admin.go), which is now a thin REST shim
// delegating to these methods in-process.
type AdminService struct {
	store  *store.Store
	logger *slog.Logger
}

// NewAdminService constructs an AdminService with the deps AdminHandler uses today.
func NewAdminService(st *store.Store, logger *slog.Logger) *AdminService {
	return &AdminService{store: st, logger: logger}
}

var _ mgmtv1connect.AdminServiceHandler = (*AdminService)(nil)

func toOrgProto(o sqlc.Org) *mgmtv1.Org {
	return &mgmtv1.Org{
		Id:            o.ID.String(),
		Name:          o.Name,
		DisplayName:   o.DisplayName,
		AdminGroupId:  o.AdminGroupID,
		ReaderGroupId: o.ReaderGroupID.String,
		CreatedAt:     protoTimestamp(o.CreatedAt),
		UpdatedAt:     protoTimestamp(o.UpdatedAt),
		TenantId:      o.TenantID.String,
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

func toAgentTokenProto(t sqlc.AgentToken) *mgmtv1.AgentToken {
	status := "active"
	if t.RevokedAt.Valid {
		status = "revoked"
	}
	return &mgmtv1.AgentToken{
		Id:        t.ID.String(),
		Name:      t.Name,
		CreatedBy: t.CreatedBy,
		Status:    status,
		CreatedAt: protoTimestamp(t.CreatedAt),
	}
}

// ListOrgs lists all orgs.
func (s *AdminService) ListOrgs(ctx context.Context, _ *connect.Request[mgmtv1.ListOrgsRequest]) (*connect.Response[mgmtv1.ListOrgsResponse], error) {
	orgs, err := s.store.Queries.ListOrgs(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list orgs"))
	}
	items := make([]*mgmtv1.Org, len(orgs))
	for i := range orgs {
		items[i] = toOrgProto(orgs[i])
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
	// Tenant identity is optional at creation but validated when present:
	// an org may exist before its destination tenancy is decided, but a bad
	// value must never be storable. gateway.ValidateTenantID is the one
	// definition of that rule (Mimir's own), and 0013's CHECK mirrors it.
	tenantID := strings.TrimSpace(msg.GetTenantId())
	if tenantID != "" {
		if err := gateway.ValidateTenantID(tenantID); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	o, err := s.store.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
		Name:          msg.GetName(),
		DisplayName:   msg.GetDisplayName(),
		AdminGroupID:  msg.GetAdminGroupId(),
		ReaderGroupID: pgtype.Text{String: msg.GetReaderGroupId(), Valid: msg.GetReaderGroupId() != ""},
		TenantID:      pgtype.Text{String: tenantID, Valid: tenantID != ""},
	})
	if err != nil {
		if isUniqueViolation(err) {
			// Two unique constraints can fire here, and telling an admin the
			// wrong one sends them to rename an org when the real collision
			// is a tenant identity already claimed by a different org.
			if tenantID != "" && strings.Contains(err.Error(), "idx_orgs_tenant_id") {
				return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf(
					"tenant id %q is already assigned to another org; one tenant identity belongs to "+
						"exactly one org, or the gateway would stamp both orgs' traffic alike", tenantID))
			}
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("org name already exists"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create org"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), o.ID, "org.create", "org", o.ID.String())
	return connect.NewResponse(toOrgProto(o)), nil
}

// SetOrgTenantID assigns tenant identity to an org created without one.
//
// Set-once, and the SQL is what enforces it (SetOrgTenantID's WHERE clause
// matches only a NULL tenant_id), so a concurrent second caller loses the
// race cleanly instead of both believing they won. Changing an org's tenant
// after routes exist would leave every already-applied HTTPRoute injecting
// the previous value: the routes keep working and keep being wrong, which is
// harder to notice than an outage.
func (s *AdminService) SetOrgTenantID(ctx context.Context, req *connect.Request[mgmtv1.SetOrgTenantIDRequest]) (*connect.Response[mgmtv1.Org], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	id, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	tenantID := strings.TrimSpace(req.Msg.GetTenantId())
	if err := gateway.ValidateTenantID(tenantID); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	o, err := s.store.Queries.SetOrgTenantID(ctx, sqlc.SetOrgTenantIDParams{
		ID:       id,
		TenantID: pgtype.Text{String: tenantID, Valid: true},
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists,
				fmt.Errorf("tenant id %q is already assigned to another org; one tenant identity "+
					"belongs to exactly one org, or the gateway would stamp both orgs' traffic alike", tenantID))
		}
		// No row updated means either the org does not exist or its tenant is
		// already set. Distinguish them: "already set" is the interesting one
		// and deserves to say so rather than reading as "not found".
		if errors.Is(err, pgx.ErrNoRows) {
			existing, getErr := s.store.Queries.GetOrgByID(ctx, id)
			if getErr != nil {
				return nil, connect.NewError(connect.CodeNotFound, errors.New("org not found"))
			}
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
				"org %q already has tenant identity %q, and it cannot be changed: every tenant route "+
					"already minted for this org injects that value, and they would keep routing while "+
					"naming the wrong tenant", existing.Name, existing.TenantID.String))
		}
		s.logger.Warn("set org tenant id", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to set org tenant id"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), o.ID, "org.set_tenant_id", "org", o.ID.String())
	return connect.NewResponse(toOrgProto(o)), nil
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
	o, err := s.store.Queries.UpdateOrg(ctx, sqlc.UpdateOrgParams{
		ID:            id,
		DisplayName:   msg.GetDisplayName(),
		AdminGroupID:  msg.GetAdminGroupId(),
		ReaderGroupID: pgtype.Text{String: msg.GetReaderGroupId(), Valid: msg.GetReaderGroupId() != ""},
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update org"))
	}
	return connect.NewResponse(toOrgProto(o)), nil
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

	var clusterCount, pipelineCount int
	// RAW-SQL-OK: cross-table count with two columns — no sqlc equivalent
	if err := s.store.Pool().QueryRow(ctx,
		`SELECT
			(SELECT count(*) FROM clusters WHERE org_id = $1)::int,
			(SELECT count(*) FROM pipelines WHERE org_id = $1)::int`,
		id).Scan(&clusterCount, &pipelineCount); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to check org content"))
	}
	if clusterCount > 0 || pipelineCount > 0 {
		msg := fmt.Sprintf("org has %d clusters, %d pipelines", clusterCount, pipelineCount)
		return nil, connect.NewError(connect.CodeAlreadyExists, &orgNotEmptyError{message: msg})
	}

	if err := s.store.Queries.DeleteOrg(ctx, id); err != nil {
		if isFKViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, &orgNotEmptyError{message: "org still has references"})
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete org"))
	}
	return connect.NewResponse(&mgmtv1.DeleteOrgResponse{}), nil
}

// orgNotEmptyError indicates an org cannot be deleted because it still has
// clusters, pipelines, or other references. The REST shim (admin.go's
// DeleteOrg) detects this via errors.As and renders the legacy
// {"error":{"code":"not_empty",...}} envelope exactly — the Ginkgo REST
// suite keys on that literal code string, so it cannot be replaced by
// connect.CodeAlreadyExists's default rendering ("already_exists"). Mirrors
// destinationInUseError in rpc_destination.go.
type orgNotEmptyError struct {
	message string
}

func (e *orgNotEmptyError) Error() string { return e.message }

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
		return nil, connect.NewError(connect.CodeNotFound, errors.New("cluster not found"))
	}
	orgID, err := scanUUID(msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	if err := s.store.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID}); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to claim cluster"))
	}
	s.logger.Info("cluster claimed", "cluster_id", cluster.ID, "org_id", orgID)
	return connect.NewResponse(&mgmtv1.ClaimClusterResponse{Status: "claimed"}), nil
}

// UnclaimCluster removes a cluster's org assignment and marks its
// collectors' serve cache dirty.
func (s *AdminService) UnclaimCluster(ctx context.Context, req *connect.Request[mgmtv1.UnclaimClusterRequest]) (*connect.Response[mgmtv1.UnclaimClusterResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	cluster, err := s.store.Queries.GetClusterByName(ctx, req.Msg.GetCluster())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("cluster not found"))
	}
	if err := s.store.Queries.UnclaimCluster(ctx, cluster.ID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to unclaim cluster"))
	}
	// RAW-SQL-OK: cluster-scoped dirty marking — no sqlc query covers this join shape
	if _, err := s.store.Pool().Exec(ctx,
		`UPDATE serve_cache sc SET dirty = true
		 FROM collectors c WHERE sc.collector_id = c.id AND c.cluster_id = $1`,
		cluster.ID); err != nil {
		s.logger.Warn("unclaim: failed to mark serve_cache dirty", "cluster_id", cluster.ID, "err", err)
	}
	return connect.NewResponse(&mgmtv1.UnclaimClusterResponse{Status: "unclaimed"}), nil
}

// ListAgentTokens lists agent tokens.
func (s *AdminService) ListAgentTokens(ctx context.Context, _ *connect.Request[mgmtv1.ListAgentTokensRequest]) (*connect.Response[mgmtv1.ListAgentTokensResponse], error) {
	tokens, _ := s.store.Queries.ListAgentTokens(ctx) //nolint:errcheck // empty list is safe fallback
	items := make([]*mgmtv1.AgentToken, len(tokens))
	for i := range tokens {
		items[i] = toAgentTokenProto(tokens[i])
	}
	return connect.NewResponse(&mgmtv1.ListAgentTokensResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // token counts never approach int32 overflow
}

// CreateAgentToken creates an agent token. The plaintext secret is returned
// exactly once in the response and is never logged or persisted — only its
// SHA-256 hash is stored.
func (s *AdminService) CreateAgentToken(ctx context.Context, req *connect.Request[mgmtv1.CreateAgentTokenRequest]) (*connect.Response[mgmtv1.CreateAgentTokenResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	name := req.Msg.GetName()
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name required"))
	}
	actor := actorFromCtx(ctx)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to generate secret"))
	}
	secret := base64.URLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(secret))
	tok, err := s.store.Queries.CreateAgentToken(ctx, sqlc.CreateAgentTokenParams{
		Name: name, TokenHash: hash[:], CreatedBy: actor,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create token"))
	}
	return connect.NewResponse(&mgmtv1.CreateAgentTokenResponse{
		Id:     tok.ID.String(),
		Name:   tok.Name,
		Secret: secret,
	}), nil
}

// RevokeAgentToken revokes an agent token.
func (s *AdminService) RevokeAgentToken(ctx context.Context, req *connect.Request[mgmtv1.RevokeAgentTokenRequest]) (*connect.Response[mgmtv1.RevokeAgentTokenResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	id, err := scanUUID(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.store.Queries.RevokeAgentToken(ctx, id); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to revoke token"))
	}
	return connect.NewResponse(&mgmtv1.RevokeAgentTokenResponse{}), nil
}

// SearchGroups searches Entra groups by display-name prefix. Stubbed to
// match the pre-migration legacy handler exactly (see admin.go's original
// AdminHandler.SearchGroups comment): no Graph client is threaded through
// the server anywhere today, so this always returns an empty list rather
// than guessing at behavior. Implementing it needs both the wiring change
// and an app-mode search call on internal/graph's Client (the unused cached
// implementation that once lived there has been deleted as dead code).
func (s *AdminService) SearchGroups(_ context.Context, _ *connect.Request[mgmtv1.SearchGroupsRequest]) (*connect.Response[mgmtv1.SearchGroupsResponse], error) {
	return connect.NewResponse(&mgmtv1.SearchGroupsResponse{Items: []*mgmtv1.GroupSearchResult{}, Total: 0}), nil
}
