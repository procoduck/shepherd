package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/crypto"
	"shepherd/internal/gitrepo"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// GitOpsService implements mgmtv1connect.GitOpsServiceHandler. Business
// logic moved here from RepoLinksHandler (repolinks.go), which is now a
// thin REST shim delegating to these methods in-process.
type GitOpsService struct {
	store      *store.Store
	crypto     *crypto.Encryptor
	tokenCache *gitrepo.TokenCache
	logger     *slog.Logger
}

// NewGitOpsService constructs a GitOpsService with the deps RepoLinksHandler uses today.
func NewGitOpsService(st *store.Store, enc *crypto.Encryptor, logger *slog.Logger) *GitOpsService {
	return &GitOpsService{store: st, crypto: enc, tokenCache: gitrepo.NewTokenCache(), logger: logger}
}

var _ mgmtv1connect.GitOpsServiceHandler = (*GitOpsService)(nil)

// errEncryptionUnavailable is returned (as connect.CodeUnavailable) for
// mutating GitOps calls when the server was booted without an encryption
// key. Mirrors the router.go nil-encryptor guard that previously wrapped
// RepoLinksHandler: reads degrade to empty lists, writes are unavailable.
var errEncryptionUnavailable = errors.New("encryption not configured")

// validCredentialKinds are the six auth strategies from
// docs/git-provider-design.md §3.2.
var validCredentialKinds = map[string]bool{ //nolint:gochecknoglobals // static validation table, read-only after init
	"none": true, "basic": true, "pat": true, "ssh": true, "ado_sp": true, "github_app": true,
}

// tokenExchangeKinds mint a short-lived token from a long-lived secret
// before git is ever contacted (docs/git-provider-design.md §3.2): a
// failure in that step is a different operator problem than git itself
// being unreachable, which is what TestCredential distinguishes.
func isTokenExchangeKind(kind string) bool {
	return kind == "ado_sp" || kind == "github_app"
}

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

// toGitCredentialProto converts a stored credential row to its proto
// representation. Every secret column (client_secret_enc, secret2_enc) is
// deliberately left out — secrets are write-only, see GitCredential's doc
// comment in gitops.proto.
func toGitCredentialProto(c sqlc.GitCredential) *mgmtv1.GitCredential {
	pc, err := structFromJSON(c.ProviderConfig)
	if err != nil {
		// provider_config is written exclusively by CreateCredential via
		// protojson (see createCredentialProviderConfigJSON), so a parse
		// failure here would mean corrupted data, not a client mistake.
		// Degrade to an empty Struct rather than failing the whole read.
		pc = nil
	}
	return &mgmtv1.GitCredential{
		Id:             c.ID.String(),
		Name:           c.Name,
		Kind:           c.Kind,
		Username:       c.Username.String,
		AdoOrgUrl:      c.AdoOrgUrl.String,
		EntraTenantId:  c.EntraTenantID.String,
		ClientId:       c.ClientID.String,
		ProviderConfig: pc,
		CreatedAt:      protoTimestamp(c.CreatedAt),
	}
}

func toRepoLinkProto(l sqlc.RepoLink) *mgmtv1.RepoLink {
	return &mgmtv1.RepoLink{
		Id:           l.ID.String(),
		RepoUrl:      l.RepoUrl,
		Branch:       l.Branch,
		Path:         l.Path,
		SyncStatus:   l.SyncStatus.String,
		LastSyncedAt: protoTimestamp(l.LastSyncedAt),
		CollectorId:  l.CollectorID.String(),
		CredentialId: l.CredentialID.String(),
	}
}

// ListCredentials lists git credentials in an org. When the server has no
// encryption key configured, returns an empty list (matching the legacy
// nil-encryptor guard in router.go).
func (s *GitOpsService) ListCredentials(ctx context.Context, req *connect.Request[mgmtv1.ListCredentialsRequest]) (*connect.Response[mgmtv1.ListCredentialsResponse], error) {
	if s.crypto == nil {
		return connect.NewResponse(&mgmtv1.ListCredentialsResponse{Items: []*mgmtv1.GitCredential{}, Total: 0}), nil
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		orgID = pgtype.UUID{}
	}
	rows, _ := s.store.Queries.ListGitCredentialsByOrg(ctx, orgID) //nolint:errcheck // empty is safe
	items := make([]*mgmtv1.GitCredential, len(rows))
	for i := range rows {
		items[i] = toGitCredentialProto(rows[i])
	}
	return connect.NewResponse(&mgmtv1.ListCredentialsResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // org credential counts never approach int32 overflow
}

// CreateCredential creates a git credential for one of the six auth
// strategies (docs/git-provider-design.md §3.2). client_secret is required
// for every kind except "none"; it and secret2 (only ssh's optional key
// passphrase today) are encrypted at rest and never returned.
func (s *GitOpsService) CreateCredential(ctx context.Context, req *connect.Request[mgmtv1.CreateCredentialRequest]) (*connect.Response[mgmtv1.GitCredential], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	if s.crypto == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errEncryptionUnavailable)
	}
	msg := req.Msg

	kind := msg.GetKind()
	if kind == "" {
		// The legacy REST shim's pre-rename shape (repolinks.go's
		// adoCredRequest) never sent kind: every credential it created was
		// an ADO service principal.
		kind = "ado_sp"
	}
	if !validCredentialKinds[kind] {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported credential kind %q", kind))
	}
	if kind != "none" && msg.GetClientSecret() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("client_secret required"))
	}
	if kind == "ssh" && msg.GetSshKnownHosts() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("ssh_known_hosts required for kind ssh (no accept-any-host-key mode)"))
	}

	orgID, err := scanUUID(msg.GetOrgId())
	if err != nil {
		return nil, err
	}

	// client_secret_enc is NOT NULL (it predates provider_config, and the
	// 0006 migration didn't relax it), so kind="none" — the only kind
	// allowed to omit client_secret — still needs a non-nil value here:
	// encrypt the empty string rather than leaving the column nil.
	encSecret, err := s.crypto.Encrypt([]byte(msg.GetClientSecret()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("encryption failed"))
	}
	var encSecret2 []byte
	if msg.GetSecret2() != "" {
		encSecret2, err = s.crypto.Encrypt([]byte(msg.GetSecret2()))
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, errors.New("encryption failed"))
		}
	}

	providerConfigJSON, err := createCredentialProviderConfigJSON(msg)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid provider_config: %w", err))
	}

	c, err := s.store.Queries.CreateGitCredential(ctx, sqlc.CreateGitCredentialParams{
		OrgID:                 orgID,
		Name:                  msg.GetName(),
		Kind:                  kind,
		Username:              pgtype.Text{String: msg.GetUsername(), Valid: msg.GetUsername() != ""},
		AdoOrgUrl:             pgtype.Text{String: msg.GetAdoOrgUrl(), Valid: msg.GetAdoOrgUrl() != ""},
		EntraTenantID:         pgtype.Text{String: msg.GetEntraTenantId(), Valid: msg.GetEntraTenantId() != ""},
		ClientID:              pgtype.Text{String: msg.GetClientId(), Valid: msg.GetClientId() != ""},
		ClientSecretEnc:       encSecret,
		Secret2Enc:            encSecret2,
		ProviderConfig:        providerConfigJSON,
		SshKnownHosts:         pgtype.Text{String: msg.GetSshKnownHosts(), Valid: msg.GetSshKnownHosts() != ""},
		CaCert:                pgtype.Text{String: msg.GetCaCert(), Valid: msg.GetCaCert() != ""},
		TlsInsecureSkipVerify: msg.GetTlsInsecureSkipVerify(),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("credential name already exists"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create credential"))
	}
	return connect.NewResponse(toGitCredentialProto(c)), nil
}

// DeleteCredential deletes a git credential.
func (s *GitOpsService) DeleteCredential(ctx context.Context, req *connect.Request[mgmtv1.DeleteCredentialRequest]) (*connect.Response[mgmtv1.DeleteCredentialResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	if s.crypto == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errEncryptionUnavailable)
	}
	id, err := scanUUID(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	// Ownership recheck, exactly as TestCredential below already does. Without
	// it this deletes by id alone: the interceptor proves a role in the org the
	// caller NAMED, never that the id belongs to it, so an org admin could
	// destroy another tenant's GitOps wiring.
	cred, err := s.store.Queries.GetGitCredentialByID(ctx, id)
	if err != nil || cred.OrgID.String() != req.Msg.GetOrgId() {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("credential not found"))
	}
	// The error was previously discarded, so a delete that removed nothing
	// still reported success.
	if err := s.store.Queries.DeleteGitCredential(ctx, id); err != nil {
		s.logger.Error("delete git credential", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete credential"))
	}
	return connect.NewResponse(&mgmtv1.DeleteCredentialResponse{}), nil
}

// TestCredential resolves the stored credential's auth strategy and runs a
// git ls-remote against req.repo_url, reporting whether the remote is
// reachable and — for the two token-exchange strategies (ado_sp,
// github_app) — whether the token-exchange step itself succeeded. These
// are deliberately distinguished: an operator with a bad ADO client secret
// needs a different fix than one whose repo URL is wrong or whose Gitea
// instance is down (docs/git-provider-design.md §3.4).
func (s *GitOpsService) TestCredential(ctx context.Context, req *connect.Request[mgmtv1.TestCredentialRequest]) (*connect.Response[mgmtv1.TestCredentialResponse], error) {
	if s.crypto == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errEncryptionUnavailable)
	}
	msg := req.Msg
	if msg.GetRepoUrl() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("repo_url required"))
	}
	id, err := scanUUID(msg.GetId())
	if err != nil {
		return nil, err
	}

	cred, err := s.store.Queries.GetGitCredentialByID(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("credential not found"))
	}
	if orgID := msg.GetOrgId(); orgID != "" && cred.OrgID.String() != orgID {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("credential not found"))
	}

	required := isTokenExchangeKind(cred.Kind)

	auth, err := s.buildAuth(cred)
	if err != nil {
		// A credential resolution failure (bad encrypted data, malformed
		// provider_config) is reported in the response body, not as a
		// Connect RPC error: the call itself succeeded, it's the tested
		// credential that's broken — the same "diagnostic in the payload"
		// shape as every other branch below.
		return connect.NewResponse(&mgmtv1.TestCredentialResponse{ //nolint:nilerr // deliberate: see comment above
			Error: err.Error(), TokenExchangeRequired: required,
		}), nil
	}

	// For ado_sp/github_app, resolve the token first so a token-exchange
	// failure is reported as such rather than surfacing as an
	// unauthenticated git error once LatestCommit tries to use it.
	if required {
		if _, _, _, tokenErr := auth.HTTP(ctx); tokenErr != nil {
			return connect.NewResponse(&mgmtv1.TestCredentialResponse{
				Error:                 fmt.Sprintf("token exchange failed: %s", tokenErr.Error()),
				TokenExchangeRequired: true,
				TokenExchangeOk:       false,
			}), nil
		}
	}

	branch := msg.GetBranch()
	if branch == "" {
		branch = "main"
	}
	repo := gitrepo.Repo{
		URL:    msg.GetRepoUrl(),
		Branch: branch,
		Auth:   auth,
		TLS: gitrepo.TLSOptions{
			CABundle:           []byte(cred.CaCert.String),
			InsecureSkipVerify: cred.TlsInsecureSkipVerify,
		},
	}

	_, err = repo.LatestCommit(ctx)
	switch {
	case err == nil:
		return connect.NewResponse(&mgmtv1.TestCredentialResponse{
			Reachable: true, TokenExchangeRequired: required, TokenExchangeOk: required,
		}), nil
	case errors.Is(err, gitrepo.ErrBranchNotFound):
		// The remote was reached and, if applicable, authenticated —
		// only the requested branch doesn't exist there. That's a repo
		// link configuration mismatch, not a reachability problem.
		return connect.NewResponse(&mgmtv1.TestCredentialResponse{
			Reachable: true, Error: err.Error(), TokenExchangeRequired: required, TokenExchangeOk: required,
		}), nil
	default:
		return connect.NewResponse(&mgmtv1.TestCredentialResponse{
			Error: err.Error(), TokenExchangeRequired: required, TokenExchangeOk: required,
		}), nil
	}
}

// buildAuth decrypts cred's secret(s) and builds the gitrepo.Auth strategy
// matching cred.Kind (docs/git-provider-design.md §3.2).
//
// This mirrors internal/gitsync.Reconciler.buildAuth field-for-field: both
// resolve the same (kind, encrypted columns) shape of sqlc.GitCredential
// into the same gitrepo.Auth values, so TestCredential proves exactly what
// the reconciler will do with the same row. It is duplicated rather than
// shared because the reconciler's version is unexported and lives in a
// package this task does not own; rpc_gitops_test.go's kind-by-kind
// coverage keeps the two from silently drifting apart.
func (s *GitOpsService) buildAuth(cred sqlc.GitCredential) (gitrepo.Auth, error) {
	switch cred.Kind {
	case "none":
		return gitrepo.NoneAuth{}, nil

	case "basic":
		secret, err := s.crypto.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting password: %w", err)
		}
		return gitrepo.BasicAuth{Username: cred.Username.String, Password: string(secret)}, nil

	case "pat":
		secret, err := s.crypto.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting token: %w", err)
		}
		return gitrepo.PATAuth{Username: cred.Username.String, Token: string(secret)}, nil

	case "ssh":
		key, err := s.crypto.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting private key: %w", err)
		}
		var passphrase string
		if len(cred.Secret2Enc) > 0 {
			pp, err := s.crypto.Decrypt(cred.Secret2Enc)
			if err != nil {
				return nil, fmt.Errorf("decrypting private key passphrase: %w", err)
			}
			passphrase = string(pp)
		}
		return gitrepo.SSHAuth{
			User:          cred.Username.String,
			PrivateKeyPEM: key,
			Passphrase:    passphrase,
			KnownHosts:    []byte(cred.SshKnownHosts.String),
		}, nil

	case "ado_sp":
		secret, err := s.crypto.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting client secret: %w", err)
		}
		return gitrepo.AdoSPAuth{
			CredentialID: cred.ID.String(),
			TenantID:     cred.EntraTenantID.String,
			ClientID:     cred.ClientID.String,
			ClientSecret: string(secret),
			Cache:        s.tokenCache,
		}, nil

	case "github_app":
		key, err := s.crypto.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting private key: %w", err)
		}
		pc, err := githubAppProviderConfig(cred.ProviderConfig)
		if err != nil {
			return nil, fmt.Errorf("parsing provider_config: %w", err)
		}
		return gitrepo.GitHubAppAuth{
			CredentialID:   cred.ID.String(),
			AppID:          pc.AppID,
			InstallationID: pc.InstallationID,
			PrivateKeyPEM:  key,
			APIBase:        pc.APIBase,
			Cache:          s.tokenCache,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported credential kind %q", cred.Kind)
	}
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
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	if s.crypto == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errEncryptionUnavailable)
	}
	msg := req.Msg
	if msg.GetRepoUrl() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("repo_url required"))
	}
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
	// Both references must belong to the same org as the link. The foreign keys
	// enforce existence, not ownership, so without this a link in org A can name
	// org B's credential -- and the gitsync reconciler would then decrypt and
	// use B's credential to drive A's sync.
	collOrg, err := s.store.Queries.GetCollectorOrgID(ctx, collID)
	if err != nil || !collOrg.Valid || collOrg != orgID {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("collector not found"))
	}
	cred, err := s.store.Queries.GetGitCredentialByID(ctx, credID)
	if err != nil || cred.OrgID != orgID {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("credential not found"))
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
		RepoUrl: msg.GetRepoUrl(), Branch: branch, Path: path, PollIntervalSeconds: poll,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create repo link"))
	}
	return connect.NewResponse(toRepoLinkProto(l)), nil
}

// DeleteRepoLink deletes a repo link.
func (s *GitOpsService) DeleteRepoLink(ctx context.Context, req *connect.Request[mgmtv1.DeleteRepoLinkRequest]) (*connect.Response[mgmtv1.DeleteRepoLinkResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	if s.crypto == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errEncryptionUnavailable)
	}
	id, err := scanUUID(req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	link, err := s.store.Queries.GetRepoLinkByID(ctx, id)
	if err != nil || link.OrgID.String() != req.Msg.GetOrgId() {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("repo link not found"))
	}
	if err := s.store.Queries.DeleteRepoLink(ctx, id); err != nil {
		s.logger.Error("delete repo link", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete repo link"))
	}
	return connect.NewResponse(&mgmtv1.DeleteRepoLinkResponse{}), nil
}

// githubAppProviderConfigFields is the shape of GitCredential.provider_config
// for kind == "github_app": internal/gitsync.Reconciler.buildAuth decodes an
// identical anonymous struct from the same column.
type githubAppProviderConfigFields struct {
	AppID          string `json:"app_id"`
	InstallationID string `json:"installation_id"`
	APIBase        string `json:"api_base_url"`
}

func githubAppProviderConfig(raw []byte) (githubAppProviderConfigFields, error) {
	var pc githubAppProviderConfigFields
	if len(raw) == 0 {
		return pc, nil
	}
	if err := json.Unmarshal(raw, &pc); err != nil {
		return pc, err
	}
	return pc, nil
}

// createCredentialProviderConfigJSON converts CreateCredentialRequest's
// provider_config Struct into the jsonb bytes git_credentials.provider_config
// stores. A nil/absent Struct stores "{}", matching the column's
// NOT NULL DEFAULT '{}'.
func createCredentialProviderConfigJSON(msg *mgmtv1.CreateCredentialRequest) ([]byte, error) {
	return structToJSON(msg.GetProviderConfig())
}

// structToJSON converts a *structpb.Struct (as carried by
// CreateCredentialRequest.provider_config) into the jsonb bytes
// git_credentials.provider_config stores. A nil Struct stores "{}",
// matching the column's NOT NULL DEFAULT '{}'.
func structToJSON(s *structpb.Struct) ([]byte, error) {
	if s == nil {
		return []byte("{}"), nil
	}
	b, err := protojson.Marshal(s)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return []byte("{}"), nil
	}
	return b, nil
}
