package mgmtapi

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/types/known/timestamppb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/crypto"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// GitOpsService implements mgmtv1connect.GitOpsServiceHandler. Business
// logic moved here from RepoLinksHandler (repolinks.go), which is now a
// thin REST shim delegating to these methods in-process.
type GitOpsService struct {
	store  *store.Store
	crypto *crypto.Encryptor
	logger *slog.Logger
}

// NewGitOpsService constructs a GitOpsService with the deps RepoLinksHandler uses today.
func NewGitOpsService(st *store.Store, enc *crypto.Encryptor, logger *slog.Logger) *GitOpsService {
	return &GitOpsService{store: st, crypto: enc, logger: logger}
}

var _ mgmtv1connect.GitOpsServiceHandler = (*GitOpsService)(nil)

// errEncryptionUnavailable is returned (as connect.CodeUnavailable) for
// mutating GitOps calls when the server was booted without an encryption
// key. Mirrors the router.go nil-encryptor guard that previously wrapped
// RepoLinksHandler: reads degrade to empty lists, writes are unavailable.
var errEncryptionUnavailable = errors.New("encryption not configured")

// scanUUID parses s (expected to be a UUID string) into a pgtype.UUID,
// returning connect.CodeInvalidArgument on failure.
func scanUUID(s string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		return id, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid id"))
	}
	return id, nil
}

// protoTimestamp converts a nullable Postgres timestamptz to a proto
// Timestamp, truncated to whole seconds to match the legacy REST responses'
// fixed "2006-01-02T15:04:05Z" formatting (which always dropped
// sub-second precision) so protojson's RFC3339 rendering stays
// byte-compatible with the pre-migration wire format.
func protoTimestamp(t pgtype.Timestamptz) *timestamppb.Timestamp {
	if !t.Valid {
		return nil
	}
	return timestamppb.New(t.Time.UTC().Truncate(time.Second))
}

func toAdoCredentialProto(c sqlc.AdoCredential) *mgmtv1.AdoCredential {
	return &mgmtv1.AdoCredential{
		Id:            c.ID.String(),
		Name:          c.Name,
		AdoOrgUrl:     c.AdoOrgUrl,
		EntraTenantId: c.EntraTenantID,
		ClientId:      c.ClientID,
		CreatedAt:     protoTimestamp(c.CreatedAt),
	}
}

func toRepoLinkProto(l sqlc.RepoLink) *mgmtv1.RepoLink {
	return &mgmtv1.RepoLink{
		Id:           l.ID.String(),
		Project:      l.Project,
		Repository:   l.Repository,
		Branch:       l.Branch,
		Path:         l.Path,
		SyncStatus:   l.SyncStatus.String,
		LastSyncedAt: protoTimestamp(l.LastSyncedAt),
	}
}

// ListCredentials lists ADO credentials in an org. When the server has no
// encryption key configured, returns an empty list (matching the legacy
// nil-encryptor guard in router.go).
func (s *GitOpsService) ListCredentials(ctx context.Context, req *connect.Request[mgmtv1.ListCredentialsRequest]) (*connect.Response[mgmtv1.ListCredentialsResponse], error) {
	if s.crypto == nil {
		return connect.NewResponse(&mgmtv1.ListCredentialsResponse{Items: []*mgmtv1.AdoCredential{}, Total: 0}), nil
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		orgID = pgtype.UUID{}
	}
	rows, _ := s.store.Queries.ListADOCredentialsByOrg(ctx, orgID) //nolint:errcheck // empty is safe
	items := make([]*mgmtv1.AdoCredential, len(rows))
	for i := range rows {
		items[i] = toAdoCredentialProto(rows[i])
	}
	return connect.NewResponse(&mgmtv1.ListCredentialsResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // org credential counts never approach int32 overflow
}

// CreateCredential creates an ADO credential. client_secret is required and
// is encrypted at rest; it is never returned in the response.
func (s *GitOpsService) CreateCredential(ctx context.Context, req *connect.Request[mgmtv1.CreateCredentialRequest]) (*connect.Response[mgmtv1.AdoCredential], error) {
	if s.crypto == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errEncryptionUnavailable)
	}
	msg := req.Msg
	if msg.GetClientSecret() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("client_secret required"))
	}
	orgID, err := scanUUID(msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	encSecret, err := s.crypto.Encrypt([]byte(msg.GetClientSecret()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("encryption failed"))
	}
	c, err := s.store.Queries.CreateADOCredential(ctx, sqlc.CreateADOCredentialParams{
		OrgID: orgID, Name: msg.GetName(), AdoOrgUrl: msg.GetAdoOrgUrl(),
		EntraTenantID: msg.GetEntraTenantId(), ClientID: msg.GetClientId(), ClientSecretEnc: encSecret,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("credential name already exists"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create credential"))
	}
	return connect.NewResponse(toAdoCredentialProto(c)), nil
}

// DeleteCredential deletes an ADO credential.
func (s *GitOpsService) DeleteCredential(ctx context.Context, req *connect.Request[mgmtv1.DeleteCredentialRequest]) (*connect.Response[mgmtv1.DeleteCredentialResponse], error) {
	if s.crypto == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errEncryptionUnavailable)
	}
	id, err := scanUUID(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	_ = s.store.Queries.DeleteADOCredential(ctx, id) //nolint:errcheck // empty list is safe fallback
	return connect.NewResponse(&mgmtv1.DeleteCredentialResponse{}), nil
}

// ListRepoLinks lists repo links in an org. When the server has no
// encryption key configured, returns an empty list (matching the legacy
// nil-encryptor guard in router.go).
func (s *GitOpsService) ListRepoLinks(ctx context.Context, req *connect.Request[mgmtv1.ListRepoLinksRequest]) (*connect.Response[mgmtv1.ListRepoLinksResponse], error) {
	if s.crypto == nil {
		return connect.NewResponse(&mgmtv1.ListRepoLinksResponse{Items: []*mgmtv1.RepoLink{}, Total: 0}), nil
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		orgID = pgtype.UUID{}
	}
	rows, _ := s.store.Queries.ListRepoLinksByOrg(ctx, orgID) //nolint:errcheck // empty is safe
	items := make([]*mgmtv1.RepoLink, len(rows))
	for i := range rows {
		items[i] = toRepoLinkProto(rows[i])
	}
	return connect.NewResponse(&mgmtv1.ListRepoLinksResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // org repo-link counts never approach int32 overflow
}

// CreateRepoLink creates a repo link. Defaults branch="main", path="/",
// poll_interval_seconds=180 when unset, matching the legacy handler.
func (s *GitOpsService) CreateRepoLink(ctx context.Context, req *connect.Request[mgmtv1.CreateRepoLinkRequest]) (*connect.Response[mgmtv1.RepoLink], error) {
	if s.crypto == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errEncryptionUnavailable)
	}
	msg := req.Msg
	orgID, err := scanUUID(msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	collID, err := scanUUID(msg.GetCollectorId())
	if err != nil {
		return nil, err
	}
	credID, err := scanUUID(msg.GetCredentialId())
	if err != nil {
		return nil, err
	}
	branch := msg.GetBranch()
	if branch == "" {
		branch = "main"
	}
	path := msg.GetPath()
	if path == "" {
		path = "/"
	}
	poll := msg.GetPollIntervalSeconds()
	if poll == 0 {
		poll = 180
	}
	l, err := s.store.Queries.CreateRepoLink(ctx, sqlc.CreateRepoLinkParams{
		OrgID: orgID, CollectorID: collID, CredentialID: credID,
		Project: msg.GetProject(), Repository: msg.GetRepository(), Branch: branch, Path: path, PollIntervalSeconds: poll,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create repo link"))
	}
	return connect.NewResponse(toRepoLinkProto(l)), nil
}

// DeleteRepoLink deletes a repo link.
func (s *GitOpsService) DeleteRepoLink(ctx context.Context, req *connect.Request[mgmtv1.DeleteRepoLinkRequest]) (*connect.Response[mgmtv1.DeleteRepoLinkResponse], error) {
	if s.crypto == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errEncryptionUnavailable)
	}
	id, err := scanUUID(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	_ = s.store.Queries.DeleteRepoLink(ctx, id) //nolint:errcheck // empty list is safe fallback
	return connect.NewResponse(&mgmtv1.DeleteRepoLinkResponse{}), nil
}
