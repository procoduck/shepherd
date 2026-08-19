// Package gitsync implements the git GitOps reconciliation loop. It polls
// repo_links that are due for sync, downloads .alloy files over real git
// (via internal/gitrepo) using whichever auth strategy the linked
// credential's kind selects, validates each file, and upserts them as
// git-sourced pipelines.
package gitsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/gitrepo"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// Reconciler polls due repo_links and syncs them.
type Reconciler struct {
	store      *store.Store
	crypto     *crypto.Encryptor
	validator  *validate.Validator
	cfg        *config.GitSyncConfig
	limits     gitrepo.Limits
	tokenCache *gitrepo.TokenCache
	logger     *slog.Logger
}

// New creates a Reconciler.
func New(st *store.Store, enc *crypto.Encryptor, v *validate.Validator, cfg *config.Config, logger *slog.Logger) *Reconciler {
	return &Reconciler{
		store:     st,
		crypto:    enc,
		validator: v,
		cfg:       &cfg.GitSync,
		limits: gitrepo.Limits{
			MaxRepoBytes: cfg.GitSync.MaxRepoBytes,
			MaxFileBytes: cfg.GitSync.MaxFileBytes,
			MaxFiles:     cfg.GitSync.MaxFiles,
			FetchTimeout: cfg.GitSync.FetchTimeout,
		},
		tokenCache: gitrepo.NewTokenCache(),
		logger:     logger.With("component", "gitsync"),
	}
}

// Start runs the reconciliation loop until ctx is cancelled.
func (r *Reconciler) Start(ctx context.Context) {
	go r.run(ctx)
}

func (r *Reconciler) run(ctx context.Context) {
	tick := r.cfg.Tick
	if tick == 0 {
		tick = 3 * time.Minute
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.reconcileAll(ctx)
		}
	}
}

func (r *Reconciler) reconcileAll(ctx context.Context) {
	links, err := r.store.Queries.ListDueRepoLinks(ctx)
	if err != nil {
		r.logger.Error("gitsync: listing due repo links", "err", err)
		return
	}
	for i := range links {
		if err := r.reconcileLink(ctx, links[i]); err != nil {
			r.logger.Error("gitsync: reconciling repo link",
				"link_id", links[i].ID, "err", err)
		}
	}
}

func (r *Reconciler) reconcileLink(ctx context.Context, link sqlc.RepoLink) error {
	// Load credential.
	cred, err := r.store.Queries.GetGitCredentialByID(ctx, link.CredentialID)
	if err != nil {
		return r.markError(ctx, link.ID, fmt.Errorf("loading credential: %w", err))
	}

	auth, err := r.buildAuth(cred)
	if err != nil {
		return r.markError(ctx, link.ID, fmt.Errorf("building auth for credential %q: %w", cred.Name, err))
	}

	repo := gitrepo.Repo{
		URL:    link.RepoUrl,
		Branch: link.Branch,
		Path:   link.Path,
		Auth:   auth,
		TLS: gitrepo.TLSOptions{
			CABundle:           []byte(cred.CaCert.String),
			InsecureSkipVerify: cred.TlsInsecureSkipVerify,
		},
		Limits: r.limits,
	}

	// Check latest commit — skip if unchanged.
	latestCommit, err := repo.LatestCommit(ctx)
	if err != nil {
		return r.markError(ctx, link.ID, fmt.Errorf("getting latest commit: %w", err))
	}
	if link.LastCommit.Valid && link.LastCommit.String == latestCommit {
		if syncErr := r.store.Queries.UpdateRepoLinkSync(ctx, sqlc.UpdateRepoLinkSyncParams{
			ID: link.ID, LastCommit: pgtype.Text{String: latestCommit, Valid: true},
			SyncStatus: pgtype.Text{String: "ok", Valid: true}, SyncError: pgtype.Text{},
		}); syncErr != nil {
			r.logger.Error("gitsync: recording sync status", "link_id", link.ID, "err", syncErr)
		}
		return nil
	}

	// Fetch .alloy files.
	files, err := repo.Files(ctx)
	if err != nil {
		return r.markError(ctx, link.ID, fmt.Errorf("fetching files: %w", err))
	}

	var syncErrs []error
	for _, file := range files {
		if err := r.syncFile(ctx, link, file, latestCommit); err != nil {
			r.logger.Warn("gitsync: syncing file", "path", file.Path, "err", err)
			syncErrs = append(syncErrs, fmt.Errorf("%s: %w", file.Path, err))
		}
	}
	if len(syncErrs) > 0 {
		return r.markError(ctx, link.ID, errors.Join(syncErrs...))
	}

	// Mark sync success.
	if syncErr := r.store.Queries.UpdateRepoLinkSync(ctx, sqlc.UpdateRepoLinkSyncParams{
		ID:         link.ID,
		LastCommit: pgtype.Text{String: latestCommit, Valid: true},
		SyncStatus: pgtype.Text{String: "ok", Valid: true},
		SyncError:  pgtype.Text{},
	}); syncErr != nil {
		r.logger.Error("gitsync: recording sync status", "link_id", link.ID, "err", syncErr)
	}
	return nil
}

// buildAuth decrypts cred's secret(s) and builds the gitrepo.Auth strategy
// matching cred.Kind (docs/git-provider-design.md §3.2). r.tokenCache is
// shared across every ado_sp and github_app credential so their minted
// tokens are cached and refreshed independently, keyed by credential id.
func (r *Reconciler) buildAuth(cred sqlc.GitCredential) (gitrepo.Auth, error) {
	switch cred.Kind {
	case "none":
		return gitrepo.NoneAuth{}, nil

	case "basic":
		secret, err := r.crypto.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting password: %w", err)
		}
		return gitrepo.BasicAuth{Username: cred.Username.String, Password: string(secret)}, nil

	case "pat":
		secret, err := r.crypto.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting token: %w", err)
		}
		return gitrepo.PATAuth{Username: cred.Username.String, Token: string(secret)}, nil

	case "ssh":
		key, err := r.crypto.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting private key: %w", err)
		}
		var passphrase string
		if len(cred.Secret2Enc) > 0 {
			pp, err := r.crypto.Decrypt(cred.Secret2Enc)
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
		secret, err := r.crypto.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting client secret: %w", err)
		}
		return gitrepo.AdoSPAuth{
			CredentialID: cred.ID.String(),
			TenantID:     cred.EntraTenantID.String,
			ClientID:     cred.ClientID.String,
			ClientSecret: string(secret),
			Cache:        r.tokenCache,
		}, nil

	case "github_app":
		key, err := r.crypto.Decrypt(cred.ClientSecretEnc)
		if err != nil {
			return nil, fmt.Errorf("decrypting private key: %w", err)
		}
		var pc struct {
			AppID          string `json:"app_id"`
			InstallationID string `json:"installation_id"`
			APIBase        string `json:"api_base_url"`
		}
		if len(cred.ProviderConfig) > 0 {
			if err := json.Unmarshal(cred.ProviderConfig, &pc); err != nil {
				return nil, fmt.Errorf("parsing provider_config: %w", err)
			}
		}
		return gitrepo.GitHubAppAuth{
			CredentialID:   cred.ID.String(),
			AppID:          pc.AppID,
			InstallationID: pc.InstallationID,
			PrivateKeyPEM:  key,
			APIBase:        pc.APIBase,
			Cache:          r.tokenCache,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported credential kind %q", cred.Kind)
	}
}

func (r *Reconciler) syncFile(ctx context.Context, link sqlc.RepoLink, file gitrepo.File, commit string) error {
	contents := string(file.Content)

	// Stage 1 validation.
	result := validate.Stage1(contents)
	if !result.Valid {
		return fmt.Errorf("syntax errors in %s: %v", file.Path, result.Diagnostics)
	}

	// Build pipeline name from path (strip leading / and .alloy extension).
	name := file.Path
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}
	if len(name) > 6 && name[len(name)-6:] == ".alloy" {
		name = name[:len(name)-6]
	}

	matchersJSON, err := json.Marshal([]string{})
	if err != nil {
		matchersJSON = []byte("[]")
	}

	// link.OrgID is already a pgtype.UUID; no conversion needed.
	orgID := link.OrgID

	// Check if pipeline exists for this file+collector.
	existing, err := r.store.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{
		OrgID: orgID,
		Name:  name,
	})
	if err != nil {
		// Create new.
		// repo_link_id is what ties a git pipeline to the collector it serves: the merge
		// engine matches source='git' by that collector, never by matchers.
		created, err := r.store.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID:      orgID,
			Name:       name,
			Contents:   contents,
			Matchers:   matchersJSON,
			Enabled:    true,
			Source:     "git",
			CreatedBy:  "gitsync",
			UpdatedBy:  "gitsync",
			RepoLinkID: link.ID,
			GitPath:    pgtype.Text{String: file.Path, Valid: true},
		})
		if err != nil {
			return fmt.Errorf("creating pipeline %s: %w", name, err)
		}
		// A newly synced pipeline changes what this collector must be served, so the
		// serve cache has to be invalidated — otherwise the collector keeps receiving
		// the previously cached config indefinitely. Revision 1 and the audit row
		// mirror the update path and the API's create path.
		r.recordPipelineChange(ctx, created, orgID, link.CollectorID, commit, "create")
		return nil
	}

	// Update existing pipeline, but only when the fetched content actually changed —
	// avoids churning revisions/audit log/cache on every poll.
	if existing.Contents == contents {
		return nil
	}

	updated, err := r.store.Queries.UpdatePipeline(ctx, sqlc.UpdatePipelineParams{
		ID:        existing.ID,
		Name:      existing.Name,
		Contents:  contents,
		Matchers:  existing.Matchers,
		UpdatedBy: "gitsync",
	})
	if err != nil {
		return fmt.Errorf("updating pipeline %s: %w", name, err)
	}

	r.recordPipelineChange(ctx, updated, orgID, link.CollectorID, commit, "update")
	return nil
}

// recordPipelineChange writes the revision + audit trail for a git-sourced pipeline change
// and invalidates the target collector's serve cache. Both the create and update paths need
// all three: without the cache invalidation the collector keeps being served the previously
// cached config, which is how a newly synced pipeline could sync "ok" yet never reach an agent.
// Every step is best-effort and logged — a bookkeeping failure must not abort the sync.
func (r *Reconciler) recordPipelineChange(
	ctx context.Context,
	p sqlc.Pipeline,
	orgID, collectorID pgtype.UUID,
	commit, action string, // action is the audit verb: "create" or "update", matching
	// the imperative names the management API writes (pipeline.create/pipeline.update).
) {
	maxRev, err := r.store.Queries.GetMaxPipelineRevision(ctx, p.ID)
	if err != nil {
		r.logger.Warn("gitsync: getting max pipeline revision", "pipeline_id", p.ID, "err", err)
	}
	changeNote := "git sync"
	if commit != "" {
		changeNote = "git sync " + commit
	}
	if _, revErr := r.store.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
		PipelineID: p.ID,
		Revision:   maxRev + 1,
		Contents:   p.Contents,
		Matchers:   p.Matchers,
		Enabled:    p.Enabled,
		ChangedBy:  "gitsync",
		ChangeNote: changeNote,
	}); revErr != nil {
		r.logger.Error("gitsync: creating pipeline revision", "pipeline_id", p.ID, "err", revErr)
	}

	if auditErr := r.store.Queries.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		Actor:        "gitsync",
		ActorType:    "system",
		OrgID:        orgID,
		Action:       "pipeline." + action,
		ResourceType: "pipeline",
		ResourceID:   p.ID.String(),
		Detail:       json.RawMessage("{}"),
	}); auditErr != nil {
		r.logger.Error("gitsync: writing audit log", "pipeline_id", p.ID, "err", auditErr)
	}

	if cacheErr := r.store.Queries.MarkServeCacheDirty(ctx, collectorID); cacheErr != nil {
		r.logger.Error("gitsync: marking serve cache dirty", "collector_id", collectorID, "err", cacheErr)
	}
}

func (r *Reconciler) markError(ctx context.Context, linkID pgtype.UUID, err error) error {
	if syncErr := r.store.Queries.UpdateRepoLinkSync(ctx, sqlc.UpdateRepoLinkSyncParams{
		ID:         linkID,
		LastCommit: pgtype.Text{},
		SyncStatus: pgtype.Text{String: "error", Valid: true},
		SyncError:  pgtype.Text{String: err.Error(), Valid: true},
	}); syncErr != nil {
		r.logger.Error("gitsync: recording sync error", "link_id", linkID, "err", syncErr)
	}
	return err
}
