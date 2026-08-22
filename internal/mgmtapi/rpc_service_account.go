package mgmtapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// ServiceAccountService implements mgmtv1connect.ServiceAccountServiceHandler:
// org-scoped, capability-scoped machine-caller credentials (W10, G12). See
// service_account.proto for the authorization model.
type ServiceAccountService struct {
	store  *store.Store
	logger *slog.Logger
}

// NewServiceAccountService constructs a ServiceAccountService.
func NewServiceAccountService(st *store.Store, logger *slog.Logger) *ServiceAccountService {
	return &ServiceAccountService{store: st, logger: logger}
}

var _ mgmtv1connect.ServiceAccountServiceHandler = (*ServiceAccountService)(nil)

var (
	errServiceAccountNameRequired = errors.New("name is required")
	errServiceAccountCapability   = errors.New(`capability must be "propose" or "apply"`)
	errServiceAccountNotFound     = errors.New("service account not found")
	errServiceAccountNameExists   = errors.New("service account name already exists in this org")
)

func toServiceAccountProto(sa sqlc.ServiceAccount) *mgmtv1.ServiceAccount {
	status := "active"
	if sa.RevokedAt.Valid {
		status = "revoked"
	}
	return &mgmtv1.ServiceAccount{
		Id:         sa.ID.String(),
		OrgId:      sa.OrgID.String(),
		Name:       sa.Name,
		Capability: sa.Capability,
		CreatedBy:  sa.CreatedBy,
		Status:     status,
		CreatedAt:  protoTimestamp(sa.CreatedAt),
	}
}

// ListServiceAccounts lists service accounts in an org (active and revoked
// — the caller sees Status, mirroring ListAgentTokens' shape).
func (s *ServiceAccountService) ListServiceAccounts(ctx context.Context, req *connect.Request[mgmtv1.ListServiceAccountsRequest]) (*connect.Response[mgmtv1.ListServiceAccountsResponse], error) {
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errOrgIDInvalid)
	}
	accounts, err := s.store.Queries.ListServiceAccountsByOrg(ctx, orgID)
	if err != nil {
		s.logger.Error("list service accounts", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list service accounts"))
	}
	items := make([]*mgmtv1.ServiceAccount, len(accounts))
	for i := range accounts {
		items[i] = toServiceAccountProto(accounts[i])
	}
	return connect.NewResponse(&mgmtv1.ListServiceAccountsResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // service account counts never approach int32 overflow
}

// CreateServiceAccount mints a service account. The plaintext secret is
// returned exactly once, mirroring AdminService.CreateAgentToken exactly
// (rpc_admin.go) — only sha256(secret) is ever stored.
func (s *ServiceAccountService) CreateServiceAccount(ctx context.Context, req *connect.Request[mgmtv1.CreateServiceAccountRequest]) (*connect.Response[mgmtv1.CreateServiceAccountResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	// Issuing a credential is not an ordinary write: it grants identity. A
	// machine may not do it, even with apply capability.
	//
	// The reason is what the credential's created_by means. Every machine
	// write is checked against that field (machine_auth.go's verifyOnBehalfOf)
	// precisely because a human put their name on it. If a service account
	// could mint another, the new credential's created_by would be
	// "svcacct:<parent>" and the delegation chain would no longer bottom out
	// at anyone accountable — while still passing every check, because each
	// link is individually valid. Revoking the parent would not revoke the
	// child either. Found by review as an attenuation rather than a spoof,
	// and closed here so the chain is one link deep by construction.
	if sa, ok := serviceAccountFromCtx(ctx); ok {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"service account %q may not create service accounts: a credential must be issued by a "+
				"human, so that the on-behalf-of claims made with it can be checked against one",
			sa.Name))
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errOrgIDInvalid)
	}
	if req.Msg.GetName() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errServiceAccountNameRequired)
	}
	capability := req.Msg.GetCapability()
	if capability != capabilityPropose && capability != capabilityApply {
		return nil, connect.NewError(connect.CodeInvalidArgument, errServiceAccountCapability)
	}

	actor := actorFromCtx(ctx)
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to generate secret"))
	}
	secret := base64.URLEncoding.EncodeToString(raw)
	hash := sha256.Sum256([]byte(secret))

	sa, err := s.store.Queries.CreateServiceAccount(ctx, sqlc.CreateServiceAccountParams{
		OrgID: orgID, Name: req.Msg.GetName(), Capability: capability, TokenHash: hash[:], CreatedBy: actor,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errServiceAccountNameExists)
		}
		s.logger.Error("create service account", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create service account"))
	}
	auditLog(ctx, s.store, actor, orgID, "service_account.create", "service_account", sa.ID.String())
	return connect.NewResponse(&mgmtv1.CreateServiceAccountResponse{
		Id: sa.ID.String(), Name: sa.Name, Capability: sa.Capability, Secret: secret,
	}), nil
}

// RevokeServiceAccount revokes a service account. A revoked account
// authenticates nowhere (GetServiceAccountByID's query filters
// revoked_at IS NULL) — this is immediate, not eventual.
func (s *ServiceAccountService) RevokeServiceAccount(ctx context.Context, req *connect.Request[mgmtv1.RevokeServiceAccountRequest]) (*connect.Response[mgmtv1.RevokeServiceAccountResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errOrgIDInvalid)
	}
	id, err := scanUUID(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid service account id"))
	}
	// GetServiceAccountByID's revoked_at IS NULL filter means an
	// already-revoked or cross-org id is indistinguishable from a missing
	// one here — matches loadOwnedDestination's "a mismatch is NotFound,
	// not PermissionDenied" reasoning.
	accounts, err := s.store.Queries.ListServiceAccountsByOrg(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to load service accounts"))
	}
	found := false
	for i := range accounts {
		if accounts[i].ID == id {
			found = true
			break
		}
	}
	if !found {
		return nil, connect.NewError(connect.CodeNotFound, errServiceAccountNotFound)
	}
	if err := s.store.Queries.RevokeServiceAccount(ctx, id); err != nil {
		s.logger.Error("revoke service account", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to revoke service account"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), orgID, "service_account.revoke", "service_account", id.String())
	return connect.NewResponse(&mgmtv1.RevokeServiceAccountResponse{}), nil
}
