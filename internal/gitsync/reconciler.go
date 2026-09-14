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
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/gitrepo"
	"shepherd/internal/merge"
	"shepherd/internal/metrics"
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
		// Counted here rather than inside reconcileLink: this is the one place
		// every reconciliation outcome passes through, and reconcileLink has a
		// dozen error returns that would each have to remember.
		if err := r.reconcileLink(ctx, links[i]); err != nil {
			metrics.SyncTotal.WithLabelValues("error").Inc()
			r.logger.Error("gitsync: reconciling repo link",
				"link_id", links[i].ID, "err", err)
		} else {
			metrics.SyncTotal.WithLabelValues("ok").Inc()
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
	// Everything below runs with the decrypted credential in hand, and its
	// errors end up in a log line AND in repo_links.sync_error, which the UI
	// shows to every org reader. A transport or URL error that echoes what
	// it was given would carry the secret with it, so every error from here
	// on is scrubbed of the credential's values before it is recorded
	// (CodeQL go/clear-text-logging). markError still receives a plain
	// error; only the text changes.
	scrub := newSecretScrubber(auth)

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
		return r.markError(ctx, link.ID, scrub(fmt.Errorf("getting latest commit: %w", err)))
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
		return r.markError(ctx, link.ID, scrub(fmt.Errorf("fetching files: %w", err)))
	}

	var syncErrs []error
	for _, file := range files {
		if err := r.syncFile(ctx, link, file, latestCommit); err != nil {
			err = scrub(err)
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

// newSecretScrubber returns a function that rewrites an error so none of
// auth's secret values appear in its text. Empty values are skipped (a
// blank passphrase would otherwise match everywhere). The result is a plain
// error — wrapping is deliberately dropped, because the whole point is that
// the original text never reaches a sink.
func newSecretScrubber(auth gitrepo.Auth) func(error) error {
	var secrets []string
	switch a := auth.(type) {
	case gitrepo.BasicAuth:
		secrets = append(secrets, a.Password)
	case gitrepo.PATAuth:
		secrets = append(secrets, a.Token)
	case gitrepo.SSHAuth:
		secrets = append(secrets, a.Passphrase, string(a.PrivateKeyPEM))
	case gitrepo.AdoSPAuth:
		secrets = append(secrets, a.ClientSecret)
	case gitrepo.GitHubAppAuth:
		secrets = append(secrets, string(a.PrivateKeyPEM))
	}
	return func(err error) error { return redactSecrets(err, secrets) }
}

// redactSecrets replaces every non-empty secret in err's text with
// "[REDACTED]". A nil error stays nil; an error containing none of the
// secrets is returned unchanged, wrapping intact.
func redactSecrets(err error, secrets []string) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	changed := false
	for _, s := range secrets {
		if s == "" || !strings.Contains(text, s) {
			continue
		}
		text = strings.ReplaceAll(text, s, "[REDACTED]")
		changed = true
	}
	if !changed {
		return err
	}
	return errors.New(text)
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
	if r.validator == nil {
		// Fail closed: syncing a file into a pipeline on Stage 1 (syntax)
		// alone was the bug this gate closes. A Reconciler built without a
		// validator (see New) must refuse to sync rather than silently fall
		// back to the weaker check.
		return errors.New("gitsync: validator not configured")
	}

	contents := string(file.Content)

	// Build pipeline name from path (strip leading / and .alloy extension).
	// Derived before validation because Stage 2 validates the content
	// declare-wrapped exactly as it will be served, and the wrapper needs
	// this name for its block label (validate.WrapForValidation).
	name := file.Path
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}
	if len(name) > 6 && name[len(name)-6:] == ".alloy" {
		name = name[:len(name)-6]
	}

	// Stages 1 and 2: syntax, then `alloy validate` on the content wrapped
	// the same way the merge engine wraps it. r.validator was injected at
	// construction (New) but never referenced here before this change —
	// every synced file passed on Stage 1 syntax alone, regardless of
	// whether it would actually validate.
	wrapped := validate.WrapForValidation(name, contents)
	if result := r.validator.Stages12(ctx, wrapped); !result.Valid {
		return fmt.Errorf("validation errors in %s: %v", file.Path, result.Diagnostics)
	}

	// Stage 3 dry-run: merge this file's content against the linked
	// collector's other enabled pipelines and validate the merged result.
	// gitsync has no schema registry (see this package's doc comment and
	// New), so this validates the UNENFORCED superset of what production
	// would actually serve for that collector — strictly stricter, never
	// looser: production's own Stage 3 (internal/mgmtapi's stage3Check)
	// additionally drops role/signal-mismatched pipelines, so anything this
	// dry-run accepts, production either accepts too or safely excludes.
	if err := r.stage3DryRun(ctx, link, name, contents); err != nil {
		return fmt.Errorf("stage-3 merge dry-run for %s: %w", file.Path, err)
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
	// A name collision with a pipeline this repo does not own is an error, not
	// an update.
	//
	// The lookup is by (org, name) alone, so a repo file named the same as a
	// UI or wizard pipeline used to take it over: contents replaced, matchers
	// KEPT. That deploys repo content to every collector the UI pipeline's
	// matchers select -- escaping the "a git pipeline targets only its linked
	// collector" rule entirely -- on a Stage-1-only check, and attributes it to
	// "gitsync". The API blocks the reverse direction explicitly
	// (errGitSourceReadOnly), so this direction being open was an oversight.
	if err == nil && (existing.Source != "git" || existing.RepoLinkID != link.ID) {
		return fmt.Errorf(
			"pipeline %q already exists in this org from a different source (%s); "+
				"rename the file or the existing pipeline", name, existing.Source)
	}
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

// stage3DryRun assembles the merged config the linked collector would be
// served if this file's content (candidateName, candidateContents) were
// synced, and validates that merged result with Stages 1 and 2. It never
// applies role/signal enforcement (merge.WithRoleEnforcement) — gitsync has
// no schema registry — so this always validates a superset of what
// production's own Stage 3 (internal/mgmtapi's stage3Check) would actually
// serve for the same collector: strictly stricter, never looser.
func (r *Reconciler) stage3DryRun(ctx context.Context, link sqlc.RepoLink, candidateName, candidateContents string) error {
	enabledPipelines, err := r.store.Queries.ListEnabledPipelinesForMerge(ctx, link.OrgID)
	if err != nil {
		return fmt.Errorf("loading pipelines: %w", err)
	}

	// Every other enabled pipeline, minus any existing row for this same
	// name — it is superseded by the candidate below, which represents the
	// file as it would be AFTER this sync.
	mergePipelines := make([]merge.Pipeline, 0, len(enabledPipelines)+1)
	for i := range enabledPipelines {
		ep := enabledPipelines[i]
		if ep.Name == candidateName {
			continue
		}
		var m []string
		if jsonErr := json.Unmarshal(ep.Matchers, &m); jsonErr != nil {
			continue
		}
		repoLinkCollectorID := ""
		if ep.RepoLinkCollectorID.Valid {
			repoLinkCollectorID = ep.RepoLinkCollectorID.String()
		}
		mergePipelines = append(mergePipelines, merge.Pipeline{
			ID:                  ep.ID.String(),
			Name:                ep.Name,
			Contents:            ep.Contents,
			Matchers:            m,
			Source:              ep.Source,
			RepoLinkCollectorID: repoLinkCollectorID,
		})
	}
	mergePipelines = append(mergePipelines, merge.Pipeline{
		ID:                  "dry-run-candidate",
		Name:                candidateName,
		Contents:            candidateContents,
		Source:              "git",
		RepoLinkCollectorID: link.CollectorID.String(),
	})

	coll, err := r.store.Queries.GetCollectorByID(ctx, link.CollectorID)
	if err != nil {
		return fmt.Errorf("loading collector: %w", err)
	}
	cluster, err := r.store.Queries.GetClusterByID(ctx, coll.ClusterID)
	if err != nil {
		return fmt.Errorf("loading cluster: %w", err)
	}

	cl := merge.CollectorLabels{
		CollectorID: link.CollectorID.String(),
		Labels:      map[string]string{"role": coll.Role, "cluster": cluster.Name},
	}
	// No WithRoleEnforcement option: gitsync has no schema registry, so this
	// deliberately validates the unenforced superset (see doc comment above).
	assembled, err := merge.Assemble(link.CollectorID.String(), cluster.Name+"/"+coll.Role, cl, mergePipelines, "dev", "")
	if err != nil {
		return fmt.Errorf("merge: %w", err)
	}

	if result := r.validator.Stages12(ctx, assembled.Content); !result.Valid {
		return fmt.Errorf("merged config invalid: %v", result.Diagnostics)
	}
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
		// WizardState left nil (-> NULL): git-sourced pipelines have no wizard_state to carry.
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
