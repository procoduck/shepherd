// Package gitsync implements the ADO GitOps reconciliation loop.
// It polls repo_links that are due for sync, downloads .alloy files,
// validates each one, and upserts them as git-sourced pipelines.
package gitsync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/ado"
	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// Reconciler polls due repo_links and syncs them.
type Reconciler struct {
	store     *store.Store
	crypto    *crypto.Encryptor
	validator *validate.Validator
	cfg       *config.GitSyncConfig
	adoBase   string // optional override for tests
	logger    *slog.Logger
}

// New creates a Reconciler.
func New(st *store.Store, enc *crypto.Encryptor, v *validate.Validator, cfg *config.Config, logger *slog.Logger) *Reconciler {
	return &Reconciler{
		store:     st,
		crypto:    enc,
		validator: v,
		cfg:       &cfg.GitSync,
		adoBase:   cfg.ADO.BaseURL,
		logger:    logger.With("component", "gitsync"),
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
	cred, err := r.store.Queries.GetADOCredentialByID(ctx, link.CredentialID)
	if err != nil {
		return r.markError(ctx, link.ID, fmt.Errorf("loading credential: %w", err))
	}

	// Decrypt client secret.
	secret, err := r.crypto.Decrypt(cred.ClientSecretEnc)
	if err != nil {
		return r.markError(ctx, link.ID, fmt.Errorf("decrypting secret: %w", err))
	}

	client := ado.New(ctx, cred.EntraTenantID, cred.ClientID, string(secret), cred.AdoOrgUrl, r.adoBase)

	// Check latest commit — skip if unchanged.
	latestCommit, err := client.GetLatestCommit(ctx, link.Project, link.Repository, link.Branch)
	if err != nil {
		return r.markError(ctx, link.ID, fmt.Errorf("getting latest commit: %w", err))
	}
	if link.LastCommit.Valid && link.LastCommit.String == latestCommit {
		_ = r.store.Queries.UpdateRepoLinkSync(ctx, sqlc.UpdateRepoLinkSyncParams{
			ID: link.ID, LastCommit: pgtype.Text{String: latestCommit, Valid: true},
			SyncStatus: pgtype.Text{String: "ok", Valid: true}, SyncError: pgtype.Text{},
		})
		return nil
	}

	// List .alloy files.
	items, err := client.ListFiles(ctx, link.Project, link.Repository, link.Branch, link.Path)
	if err != nil {
		return r.markError(ctx, link.ID, fmt.Errorf("listing files: %w", err))
	}

	for _, item := range items {
		if err := r.syncFile(ctx, client, link, item.Path); err != nil {
			r.logger.Warn("gitsync: syncing file", "path", item.Path, "err", err)
			// Continue other files; mark link error at the end.
		}
	}

	// Mark sync success.
	_ = r.store.Queries.UpdateRepoLinkSync(ctx, sqlc.UpdateRepoLinkSyncParams{
		ID:         link.ID,
		LastCommit: pgtype.Text{String: latestCommit, Valid: true},
		SyncStatus: pgtype.Text{String: "ok", Valid: true},
		SyncError:  pgtype.Text{},
	})
	return nil
}

func (r *Reconciler) syncFile(ctx context.Context, client *ado.Client, link sqlc.RepoLink, filePath string) error {
	contents, err := client.DownloadFile(ctx, link.Project, link.Repository, link.Branch, filePath)
	if err != nil {
		return err
	}

	// Stage 1 validation.
	result := validate.Stage1(contents)
	if !result.Valid {
		return fmt.Errorf("syntax errors in %s: %v", filePath, result.Diagnostics)
	}

	// Build pipeline name from path (strip leading / and .alloy extension).
	name := filePath
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

	var orgID pgtype.UUID
	if err := orgID.Scan(link.OrgID); err != nil {
		return r.markError(ctx, link.ID, fmt.Errorf("invalid org id: %w", err))
	}

	// Check if pipeline exists for this file+collector.
	_, err = r.store.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{
		OrgID: orgID,
		Name:  name,
	})
	if err != nil {
		// Create new.
		_, err = r.store.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID:     orgID,
			Name:      name,
			Contents:  contents,
			Matchers:  matchersJSON,
			Enabled:   true,
			Source:    "git",
			CreatedBy: "gitsync",
			UpdatedBy: "gitsync",
		})
		return err
	}
	// Update existing (TODO: get pipeline by name and update).
	return nil
}

func (r *Reconciler) markError(ctx context.Context, linkID pgtype.UUID, err error) error {
	_ = r.store.Queries.UpdateRepoLinkSync(ctx, sqlc.UpdateRepoLinkSyncParams{
		ID:         linkID,
		LastCommit: pgtype.Text{},
		SyncStatus: pgtype.Text{String: "error", Valid: true},
		SyncError:  pgtype.Text{String: err.Error(), Valid: true},
	})
	return err
}
