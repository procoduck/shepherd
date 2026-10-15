// Package gitsync implements the git GitOps reconciliation loop. It polls
// repo_links that are due for sync, downloads .alloy files over real git
// (via internal/gitrepo) using whichever auth strategy the linked
// credential's kind selects, validates each file, and upserts them as
// git-sourced pipelines. A successful sync also deletes the link's
// pipelines whose file has left the repository (docs/spec.md §10).
package gitsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/gitrepo"
	"shepherd/internal/merge"
	"shepherd/internal/metrics"
	"shepherd/internal/serve"
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
	// locker, when set, limits passes to one replica at a time (see Locker).
	locker Locker
	// recompute, when set, refreshes an org's served configs right after a
	// sync changed its git pipelines (see WithRecompute).
	recompute func(ctx context.Context, orgID pgtype.UUID)
	// planFileFunc, when set, replaces planFile. Test seam only: specs use it
	// to make one planning pass fail transiently (a validator timeout).
	planFileFunc func(ctx context.Context, link sqlc.RepoLink, file gitrepo.File, removing map[string]bool) (*filePlan, error)
}

// Locker lets exactly one Shepherd replica run a sync pass at a time. The
// chart runs two replicas by default; without it each replica fetched every
// due repo, and a new commit seen by both at once was synced twice —
// duplicate revisions and audit rows (#210).
type Locker interface {
	// TryLock returns ok=false, without error, when another replica holds
	// the lock. On ok=true the caller must call unlock when the pass ends.
	TryLock(ctx context.Context) (unlock func(), ok bool, err error)
}

// Option configures a Reconciler.
type Option func(*Reconciler)

// WithLocker makes the Reconciler take l before every pass.
func WithLocker(l Locker) Option {
	return func(r *Reconciler) { r.locker = l }
}

// WithRecompute makes the Reconciler call recompute for the link's org after
// a sync that created, updated or deleted a pipeline committed — the eager
// served-config refresh the API's own deletes run (mgmtapi's
// PipelineService.RecomputeOrgCaches), so the served config shown for the
// collector, an offline one included, drops a removed file's pipeline
// without waiting for a poll. Without it the collector is still marked dirty
// and recomputes on its next poll.
func WithRecompute(recompute func(ctx context.Context, orgID pgtype.UUID)) Option {
	return func(r *Reconciler) { r.recompute = recompute }
}

// New creates a Reconciler.
func New(st *store.Store, enc *crypto.Encryptor, v *validate.Validator, cfg *config.Config, logger *slog.Logger, opts ...Option) *Reconciler {
	r := &Reconciler{
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
	for _, o := range opts {
		o(r)
	}
	return r
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

// syncOutcome is how one link's reconciliation ended; it is the
// shepherd_gitsync_total result label.
type syncOutcome string

const (
	outcomeOK      syncOutcome = "ok"
	outcomeWarning syncOutcome = "warning"
	outcomeError   syncOutcome = "error"
	// outcomeLinkGone: the link was deleted while its sync ran, so the sync
	// wrote nothing — neither a success nor a failure of the sync itself.
	outcomeLinkGone syncOutcome = "link_gone"
)

func (r *Reconciler) reconcileAll(ctx context.Context) {
	if r.locker != nil {
		unlock, ok, err := r.locker.TryLock(ctx)
		if err != nil {
			r.logger.Error("gitsync: taking the sync lock", "err", err)
			return
		}
		if !ok {
			// Another replica is syncing; it records the outcomes.
			return
		}
		defer unlock()
	}
	links, err := r.store.Queries.ListDueRepoLinks(ctx)
	if err != nil {
		r.logger.Error("gitsync: listing due repo links", "err", err)
		return
	}
	for i := range links {
		// Counted here rather than inside syncLink: this is the one place
		// every reconciliation outcome passes through, and syncLink has a
		// dozen error returns that would each have to remember.
		outcome, err := r.syncLink(ctx, links[i])
		if err != nil {
			r.logger.Error("gitsync: reconciling repo link",
				"link_id", links[i].ID, "err", err)
		}
		metrics.SyncTotal.WithLabelValues(string(outcome)).Inc()
	}
}

// reconcileLink syncs one link and reports only its error.
func (r *Reconciler) reconcileLink(ctx context.Context, link sqlc.RepoLink) error {
	_, err := r.syncLink(ctx, link)
	return err
}

// syncLink fetches link's repository and brings the link's git pipelines in
// line with it: files are validated one at a time (planFile), then every
// write — the deletes of pipelines whose file left the repository, the
// creates and updates, their revisions and audit rows, the collector's dirty
// mark and the link's sync status — lands in ONE transaction (apply).
//
// Deletes happen only on a fully successful sync: a failed fetch returns
// before anything is compared, and a file that fails validation turns the
// prune off for that sync (the files that passed still sync, as before).
// Two guards hold a suspicious prune back while the rest of the commit is
// still applied (pruneGuard).
func (r *Reconciler) syncLink(ctx context.Context, link sqlc.RepoLink) (syncOutcome, error) {
	fail := func(err error) (syncOutcome, error) {
		return outcomeError, r.markError(ctx, link.ID, err)
	}

	// Load credential.
	cred, err := r.store.Queries.GetGitCredentialByID(ctx, link.CredentialID)
	if err != nil {
		return fail(fmt.Errorf("loading credential: %w", err))
	}

	auth, err := r.buildAuth(cred)
	if err != nil {
		return fail(fmt.Errorf("building auth for credential %q: %w", cred.Name, err))
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
		return fail(scrub(fmt.Errorf("getting latest commit: %w", err)))
	}
	if link.LastCommit.Valid && link.LastCommit.String == latestCommit {
		if syncErr := r.store.Queries.UpdateRepoLinkSync(ctx, sqlc.UpdateRepoLinkSyncParams{
			ID: link.ID, LastCommit: pgtype.Text{String: latestCommit, Valid: true},
			SyncStatus: pgtype.Text{String: "ok", Valid: true}, SyncError: pgtype.Text{},
		}); syncErr != nil {
			r.logger.Error("gitsync: recording sync status", "link_id", link.ID, "err", syncErr)
		}
		return outcomeOK, nil
	}

	// Fetch .alloy files.
	tree, err := repo.Fetch(ctx)
	if err != nil {
		return fail(scrub(fmt.Errorf("fetching files: %w", err)))
	}
	// The commit the clone checked out is the one every write below
	// describes; the branch may have moved since LatestCommit.
	commit := tree.Commit
	if commit == "" {
		commit = latestCommit
	}

	// Which of the link's pipelines lost their file. A pipeline's identity is
	// its name, derived from the file path the same way for every sync
	// (pipelineName) — not the stored git_path text, so a change in how a
	// path is spelled can never read as a removal. A file skipped as
	// oversized is still in the repository.
	owned, err := r.store.Queries.ListGitPipelinesByRepoLink(ctx, link.ID)
	if err != nil {
		return fail(fmt.Errorf("listing the link's pipelines: %w", err))
	}
	present := make(map[string]bool, len(tree.Files)+len(tree.Oversized))
	for _, f := range tree.Files {
		present[pipelineName(f.Path)] = true
	}
	for _, p := range tree.Oversized {
		present[pipelineName(p)] = true
	}
	var removed []sqlc.ListGitPipelinesByRepoLinkRow
	removedNames := map[string]bool{}
	for _, p := range owned {
		if !present[p.Name] {
			removed = append(removed, p)
			removedNames[p.Name] = true
		}
	}

	// A guard holds the deletes back, never the rest of the commit: a
	// backlog of files removed before an upgrade, or a moved directory, must
	// not freeze every other change to the link.
	baseline := guardBaseline(link, owned)
	warning := pruneGuard(len(present), len(owned), baseline, len(removed))
	if warning != "" {
		r.logger.Warn("gitsync: holding back deletes that would remove too many pipelines",
			"link_id", link.ID, "commit", commit, "pipelines", len(owned), "baseline", baseline,
			"removing", len(removed), "branch", link.Branch, "path", link.Path)
		removed, removedNames = nil, nil
	}

	plans, syncErrs, err := r.planAll(ctx, link, tree.Files, removed, removedNames, scrub)
	if err != nil {
		return fail(err)
	}
	if len(syncErrs) > 0 && len(removed) > 0 {
		// A failed sync never prunes, so the removed pipelines stay. Plan
		// again with them in place: what is written must be exactly what
		// was validated, and a file that replaces one of them under the
		// same sanitised name now fails as a clash and waits for a sync
		// that can prune. The first pass's failures stand even if the
		// second pass clears them (a validator timeout, a store blip): the
		// sync stays "error" with last_commit unset, so the next poll
		// fetches this commit again and retries the deletes, instead of
		// recording it as synced with the deletes skipped.
		firstErrs := syncErrs
		removed = nil
		plans, syncErrs, err = r.planAll(ctx, link, tree.Files, nil, nil, scrub)
		if err != nil {
			return fail(err)
		}
		if len(syncErrs) == 0 {
			syncErrs = firstErrs
		}
	}

	// apply has rolled its transaction back by the time it returns an
	// error, so markError (a write to the same link row on another
	// connection) never waits on the row lock apply took.
	changed, err := r.apply(ctx, link, commit, plans, removed, len(syncErrs) == 0 && warning == "")
	if errors.Is(err, errLinkGone) {
		r.logger.Info("gitsync: repo link deleted during its sync; nothing written", "link_id", link.ID)
		return outcomeLinkGone, nil
	}
	if err != nil {
		return fail(err)
	}
	if changed && r.recompute != nil {
		r.recompute(ctx, link.OrgID)
	}
	if len(syncErrs) > 0 {
		if warning != "" {
			syncErrs = append(syncErrs, errors.New(warning))
		}
		return fail(errors.Join(syncErrs...))
	}
	if warning != "" {
		r.markWarning(ctx, link.ID, warning)
		return outcomeWarning, nil
	}
	return outcomeOK, nil
}

// planAll plans every fetched file (planFile), then fails — that file only,
// with its own error — every planned write whose sanitised name another
// pipeline of the org already holds, or another file of this sync claims.
// The unique index pipelines_org_sanitized_name_key would otherwise refuse
// it at write time and roll back the whole sync, and the Stage-3 dry run
// cannot catch it: it sees only the pipelines this collector is served.
// removed/removing are the link's pipelines this sync deletes: left out of
// each dry run, and their names are free to take. The returned error is a
// store failure that ends the sync; per-file problems are in syncErrs.
func (r *Reconciler) planAll(ctx context.Context, link sqlc.RepoLink, files []gitrepo.File, removed []sqlc.ListGitPipelinesByRepoLinkRow, removing map[string]bool, scrub func(error) error) ([]filePlan, []error, error) {
	var plans []filePlan
	var syncErrs []error
	for _, file := range files {
		planFile := r.planFile
		if r.planFileFunc != nil {
			planFile = r.planFileFunc
		}
		plan, err := planFile(ctx, link, file, removing)
		if err != nil {
			err = scrub(err)
			r.logger.Warn("gitsync: syncing file", "path", file.Path, "err", err)
			syncErrs = append(syncErrs, fmt.Errorf("%s: %w", file.Path, err))
			continue
		}
		if plan != nil {
			plans = append(plans, *plan)
		}
	}

	existing, err := r.store.Queries.ListPipelineSanitizedNamesByOrg(ctx, link.OrgID)
	if err != nil {
		return nil, nil, fmt.Errorf("listing the org's pipeline names: %w", err)
	}
	deleting := make(map[pgtype.UUID]bool, len(removed))
	for _, p := range removed {
		deleting[p.ID] = true
	}
	taken := map[string][]sqlc.ListPipelineSanitizedNamesByOrgRow{}
	for _, p := range existing {
		if !deleting[p.ID] {
			taken[p.SanitizedName.String] = append(taken[p.SanitizedName.String], p)
		}
	}
	claims := map[string][]filePlan{}
	for _, p := range plans {
		s := merge.SanitizeName(p.name)
		claims[s] = append(claims[s], p)
	}

	kept := plans[:0]
	for _, p := range plans {
		s := merge.SanitizeName(p.name)
		var clash error
		for _, other := range claims[s] {
			if other.path != p.path {
				clash = fmt.Errorf("%s: pipeline %q has the same sanitised name as %s; rename one of the files", p.path, p.name, other.path)
				break
			}
		}
		if clash == nil {
			for _, holder := range taken[s] {
				if p.existing != nil && holder.ID == p.existing.ID {
					continue // the pipeline this file updates
				}
				clash = fmt.Errorf("%s: pipeline %q has the same sanitised name as pipeline %q (source %s) in this org; rename the file or that pipeline",
					p.path, p.name, holder.Name, holder.Source)
				break
			}
		}
		if clash != nil {
			r.logger.Warn("gitsync: syncing file", "path", p.path, "err", clash)
			syncErrs = append(syncErrs, clash)
			continue
		}
		kept = append(kept, p)
	}
	return kept, syncErrs, nil
}

// maxUnguardedPrune is the largest number of pipelines one sync may delete
// without the half-of-the-link check applying (pruneGuard).
const maxUnguardedPrune = 3

// emptyTreeWarning is recorded in sync_error (sync_status=warning) when a
// sync finds no .alloy files but the link still has pipelines.
const emptyTreeWarning = "the repository has no .alloy files under this link's path on this branch, " +
	"but the link still has synced pipelines; none of them was deleted. " +
	"Check the link's path and branch. To remove every pipeline deliberately, delete the repository link."

// guardBaseline is the denominator of the more-than-half guard: how many of
// the link's pipelines existed when its last sync finished "ok"
// (repo_links.last_ok_at, stamped by a trigger, migration 0034; every link
// that existed at that upgrade starts from the upgrade's moment). A guarded
// sync still applies its creates, so counting every current pipeline would
// let the next poll weigh the same deletes against a larger set and release
// the guard on its own. Measured against this baseline, the guard holds
// until the removal itself shrinks (files restored) or the link is
// re-created. A link that has never finished "ok" has a baseline of 0: it
// may delete at most maxUnguardedPrune pipelines per sync until it does.
func guardBaseline(link sqlc.RepoLink, owned []sqlc.ListGitPipelinesByRepoLinkRow) int {
	if !link.LastOkAt.Valid {
		return 0
	}
	n := 0
	for _, p := range owned {
		if !p.CreatedAt.Time.After(link.LastOkAt.Time) {
			n++
		}
	}
	return n
}

// pruneGuard returns the warning to record instead of deleting, or "" when
// the deletes may go ahead. present is the number of pipeline files in the
// synced tree, owned the link's pipelines, baseline how many of those
// existed at the link's last ok sync (guardBaseline), removing how many of
// the link's pipelines have no file any more.
//
//   - No .alloy files at all while the link has pipelines: far more often a
//     wrong path or branch (a missing path reads as an empty tree) than a
//     deliberate wipe.
//   - More than half of the baseline AND more than maxUnguardedPrune: a
//     mass deletion in one commit is more likely a mistake (a moved
//     directory, a bad merge) than intent.
//
// Either way no pipeline is deleted, but the commit's creates and updates
// still apply; the link shows sync_status=warning with the reason, and
// last_commit stays unset so every poll checks again — a full clone, and
// Stages 1-2 plus the Stage-3 dry run for every file — until the repository
// or the link changes.
func pruneGuard(present, owned, baseline, removing int) string {
	if present == 0 && owned > 0 {
		return emptyTreeWarning
	}
	if removing > maxUnguardedPrune && 2*removing > baseline {
		return fmt.Sprintf("this commit would delete %d of %d pipelines (more than half, and more than %d) in one sync, "+
			"so none of them was deleted; the commit's other changes were applied. "+
			"To delete them deliberately, delete and re-create the repository link, "+
			"or restore some of the removed files so that one sync deletes no more than half, "+
			"and remove them again once it has completed.",
			removing, baseline, maxUnguardedPrune)
	}
	return ""
}

// errLinkGone reports that the link row disappeared (DeleteRepoLink) while
// its sync was running.
var errLinkGone = errors.New("gitsync: repo link no longer exists")

// fileRemovedReason is the machine-readable reason on the pipeline.delete
// audit row of a pipeline whose file left the repository.
const fileRemovedReason = "gitsync.file_removed"

// apply writes one sync's outcome in a single transaction. It locks the link
// row first — the order GitOpsService.DeleteRepoLink uses (link, then its
// pipelines, then the link row) — so a concurrent link delete either
// finishes first (errLinkGone: nothing is written) or waits for this sync.
// Then it deletes the removed pipelines (before the creates, so a rename
// onto the same sanitised name never collides), writes each planned create
// or update with its next revision, audits every change, dirties the link's
// collector — the only one a git pipeline is served to (§6.1) — and, when
// markOK, records sync_status=ok at commit. It reports whether a pipeline
// was created, updated or deleted. Its deferred rollback has run by the time
// it returns an error, so the caller's markError never waits on the link row
// lock taken here.
func (r *Reconciler) apply(ctx context.Context, link sqlc.RepoLink, commit string, plans []filePlan, removed []sqlc.ListGitPipelinesByRepoLinkRow, markOK bool) (bool, error) {
	tx, err := r.store.Pool().Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("beginning transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op once committed
	q := r.store.Queries.WithTx(tx)

	if _, err := q.GetRepoLinkForUpdate(ctx, link.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, errLinkGone
		}
		return false, fmt.Errorf("locking the repo link: %w", err)
	}

	changed := false
	if len(removed) > 0 {
		ids := make([]pgtype.UUID, len(removed))
		for i, p := range removed {
			ids[i] = p.ID
		}
		gone, err := q.DeleteGitPipelinesOfRepoLink(ctx, sqlc.DeleteGitPipelinesOfRepoLinkParams{RepoLinkID: link.ID, Ids: ids})
		if err != nil {
			return false, fmt.Errorf("deleting the pipelines of removed files: %w", err)
		}
		for _, p := range gone {
			if err := audit(ctx, q, link.OrgID, "pipeline.delete", p.ID, map[string]string{
				"name":         p.Name,
				"reason":       fileRemovedReason,
				"description":  "file removed from repository",
				"repo_link_id": link.ID.String(),
				"git_path":     p.GitPath.String,
				"commit":       commit,
			}); err != nil {
				return false, err
			}
			r.logger.Info("gitsync: deleting the pipeline of a removed file",
				"link_id", link.ID, "pipeline", p.Name, "path", p.GitPath.String, "commit", commit)
		}
		changed = changed || len(gone) > 0
	}

	for _, plan := range plans {
		var p sqlc.Pipeline
		action := "update"
		if plan.existing == nil {
			action = "create"
			// repo_link_id is what ties a git pipeline to the collector it
			// serves: the merge engine matches source='git' by that
			// collector, never by matchers.
			p, err = q.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID:      link.OrgID,
				Name:       plan.name,
				Contents:   plan.contents,
				Matchers:   json.RawMessage("[]"),
				Enabled:    true,
				Source:     "git",
				CreatedBy:  "gitsync",
				UpdatedBy:  "gitsync",
				RepoLinkID: link.ID,
				GitPath:    pgtype.Text{String: plan.path, Valid: true},
			})
		} else {
			p, err = q.UpdatePipeline(ctx, sqlc.UpdatePipelineParams{
				ID:        plan.existing.ID,
				Name:      plan.existing.Name,
				Contents:  plan.contents,
				Matchers:  plan.existing.Matchers,
				UpdatedBy: "gitsync",
				// WizardRenderSha256 left NULL: only a wizard write sets it,
				// and planFile only ever plans writes to git pipelines.
			})
		}
		if err != nil {
			// planAll checks sanitised names up front; a clash that still
			// reaches the index (a pipeline created by the API meanwhile)
			// gets the same plain-language reading.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "pipelines_org_sanitized_name_key" {
				return false, fmt.Errorf("%s: pipeline %q has the same sanitised name as another pipeline in this org; rename the file", plan.path, plan.name)
			}
			return false, fmt.Errorf("%s pipeline %s: %w", action, plan.name, err)
		}
		// The revision goes in with the row, so no reader ever sees the new
		// contents beside the old revision: the served header prints the
		// revision next to the contents it describes ("(rev N)", #296).
		maxRev, err := q.GetMaxPipelineRevision(ctx, p.ID)
		if err != nil {
			return false, fmt.Errorf("reading current revision of %s: %w", plan.name, err)
		}
		changeNote := "git sync"
		if commit != "" {
			changeNote = "git sync " + commit
		}
		if _, err := q.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
			PipelineID: p.ID,
			Revision:   maxRev + 1,
			Contents:   p.Contents,
			Matchers:   p.Matchers,
			Enabled:    p.Enabled,
			ChangedBy:  "gitsync",
			ChangeNote: changeNote,
			// WizardState left nil (-> NULL): git-sourced pipelines have no wizard_state to carry.
		}); err != nil {
			return false, fmt.Errorf("recording revision of %s: %w", plan.name, err)
		}
		// action is the audit verb, matching the imperative names the
		// management API writes (pipeline.create/pipeline.update).
		if err := audit(ctx, q, link.OrgID, "pipeline."+action, p.ID, map[string]string{}); err != nil {
			return false, err
		}
		changed = true
	}

	// A changed pipeline set changes what the collector must be served;
	// without the mark it keeps receiving the previously cached config.
	// The mark bumps dirty_seq, so the collector's next poll recomputes.
	if changed {
		if err := q.MarkServeCacheDirty(ctx, link.CollectorID); err != nil {
			return false, fmt.Errorf("marking serve cache dirty: %w", err)
		}
	}
	if markOK {
		if err := q.UpdateRepoLinkSync(ctx, sqlc.UpdateRepoLinkSyncParams{
			ID:         link.ID,
			LastCommit: pgtype.Text{String: commit, Valid: true},
			SyncStatus: pgtype.Text{String: "ok", Valid: true},
			SyncError:  pgtype.Text{},
		}); err != nil {
			return false, fmt.Errorf("recording sync status: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("committing: %w", err)
	}
	return changed, nil
}

// audit writes one gitsync audit row on q.
func audit(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, action string, pipelineID pgtype.UUID, detail map[string]string) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	if err := q.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		Actor:        "gitsync",
		ActorType:    "system",
		OrgID:        orgID,
		Action:       action,
		ResourceType: "pipeline",
		ResourceID:   pipelineID.String(),
		Detail:       raw,
	}); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
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

// pipelineName derives a git pipeline's name from its file path: the path
// relative to the repository root, minus a leading "/" and the ".alloy"
// extension. It is a git pipeline's identity across syncs.
func pipelineName(path string) string {
	name := strings.TrimPrefix(path, "/")
	if len(name) > 6 && strings.HasSuffix(name, ".alloy") {
		name = name[:len(name)-6]
	}
	return name
}

// filePlan is one validated file's pending write: a create when existing is
// nil, otherwise an update of existing.
type filePlan struct {
	name, path, contents string
	existing             *sqlc.Pipeline
}

// planFile validates one file and decides what it changes. It returns nil,
// nil when the file's pipeline already holds exactly this content. removing
// names the link's pipelines this sync deletes: the Stage-3 dry run leaves
// them out, as the collector will be served without them.
func (r *Reconciler) planFile(ctx context.Context, link sqlc.RepoLink, file gitrepo.File, removing map[string]bool) (*filePlan, error) {
	if r.validator == nil {
		// Fail closed: syncing a file into a pipeline on Stage 1 (syntax)
		// alone was the bug this gate closes. A Reconciler built without a
		// validator (see New) must refuse to sync rather than silently fall
		// back to the weaker check.
		return nil, errors.New("gitsync: validator not configured")
	}

	contents := string(file.Content)

	// Derived before validation because Stage 2 validates the content
	// declare-wrapped exactly as it will be served, and the wrapper needs
	// this name for its block label (validate.WrapForValidation).
	name := pipelineName(file.Path)

	// Stages 1 and 2: syntax, then `alloy validate` on the content wrapped
	// the same way the merge engine wraps it. r.validator was injected at
	// construction (New) but never referenced here before this change —
	// every synced file passed on Stage 1 syntax alone, regardless of
	// whether it would actually validate.
	if result := r.validator.ValidatePipeline(ctx, name, contents); !result.Valid {
		return nil, fmt.Errorf("validation errors in %s: %v", file.Path, result.Diagnostics)
	}

	// Stage 3 dry-run: merge this file's content against the linked
	// collector's other enabled pipelines and validate the merged result.
	// gitsync has no schema registry (see this package's doc comment and
	// New), so this validates the UNENFORCED superset of what production
	// would actually serve for that collector — strictly stricter, never
	// looser: production's own Stage 3 (internal/mgmtapi's stage3Check)
	// additionally drops role/signal-mismatched pipelines, so anything this
	// dry-run accepts, production either accepts too or safely excludes.
	if err := r.stage3DryRun(ctx, link, name, contents, removing); err != nil {
		return nil, fmt.Errorf("stage-3 merge dry-run for %s: %w", file.Path, err)
	}

	// Check if pipeline exists for this file+collector.
	existing, err := r.store.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{
		OrgID: link.OrgID,
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
		return nil, fmt.Errorf(
			"pipeline %q already exists in this org from a different source (%s); "+
				"rename the file or the existing pipeline", name, existing.Source)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return &filePlan{name: name, path: file.Path, contents: contents}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("looking up pipeline %s: %w", name, err)
	}

	// Update only when the fetched content actually changed — avoids
	// churning revisions/audit log/cache on every poll.
	if existing.Contents == contents {
		return nil, nil //nolint:nilnil // nil plan: nothing to write for this file
	}
	return &filePlan{name: name, path: file.Path, contents: contents, existing: &existing}, nil
}

// stage3DryRun assembles the merged config the linked collector would be
// served if this file's content (candidateName, candidateContents) were
// synced, and validates that merged result with Stages 1 and 2. It never
// applies role/signal enforcement (merge.WithRoleEnforcement) — gitsync has
// no schema registry — so this always validates a superset of what
// production's own Stage 3 (internal/mgmtapi's stage3Check) would actually
// serve for the same collector: strictly stricter, never looser. Pipelines
// named in removing are left out: this sync deletes them.
func (r *Reconciler) stage3DryRun(ctx context.Context, link sqlc.RepoLink, candidateName, candidateContents string, removing map[string]bool) error {
	enabledPipelines, err := r.store.Queries.ListEnabledPipelinesForMerge(ctx, link.OrgID)
	if err != nil {
		return fmt.Errorf("loading pipelines: %w", err)
	}

	// Every other enabled pipeline, minus any existing row for this same
	// name — it is superseded by the candidate below, which represents the
	// file as it would be AFTER this sync — and minus the link's pipelines
	// this sync deletes (their files left the repository; a rename onto the
	// same sanitised name would otherwise collide with its own old self).
	// The candidate keeps the superseded row's revision for the header ("(rev
	// N)" is a comment line; the dry run validates, it never serves).
	candidateRevision := 0
	others := slices.DeleteFunc(enabledPipelines, func(ep sqlc.ListEnabledPipelinesForMergeRow) bool {
		if ep.Name == candidateName {
			candidateRevision = int(ep.CurrentRevision)
			return true
		}
		return ep.RepoLinkID == link.ID && removing[ep.Name]
	})
	// The serve paths' own row conversion (internal/serve), so the dry run
	// models what they serve.
	mergePipelines := append(serve.PipelinesFromRows(others), merge.Pipeline{
		ID:                  "dry-run-candidate",
		Name:                candidateName,
		Contents:            candidateContents,
		Source:              "git",
		Revision:            candidateRevision,
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

	// Admin labels and local_attributes only participate in matching once the
	// org has opted into each independently (procoduck/shepherd#139) — an org
	// with both flags off must reproduce exactly the pre-#139 {cluster,
	// role}-only behavior.
	var adminLabels, localAttrs map[string]string
	org, orgErr := r.store.Queries.GetOrgByID(ctx, link.OrgID)
	if orgErr == nil && org.AllowLabelMatching {
		if jsonErr := json.Unmarshal(coll.Labels, &adminLabels); jsonErr != nil {
			r.logger.Warn("gitsync: decoding collector labels", "collector_id", link.CollectorID.String(), "err", jsonErr)
			adminLabels = nil
		}
	}
	// Single collector, not an org-wide loop — GetLatestCollectorInstanceSummary
	// (already used for the collector-list endpoint) rather than PR-7's bulk
	// ListLatestLocalAttributesByOrg, which exists specifically to avoid N+1
	// across an org-wide Assemble loop that this dry-run isn't.
	if orgErr == nil && org.AllowLocalAttributeMatching {
		if summary, sumErr := r.store.Queries.GetLatestCollectorInstanceSummary(ctx, link.CollectorID); sumErr == nil {
			if jsonErr := json.Unmarshal(summary.LocalAttributes, &localAttrs); jsonErr != nil {
				r.logger.Warn("gitsync: decoding local_attributes", "collector_id", link.CollectorID.String(), "err", jsonErr)
				localAttrs = nil
			}
		}
	}
	cl := merge.BuildCollectorLabels(link.CollectorID.String(), cluster.Name, coll.Role, adminLabels, localAttrs)
	// No WithRoleEnforcement option: gitsync has no schema registry, so this
	// deliberately validates the unenforced superset (see doc comment above).
	assembled, err := merge.Assemble(link.CollectorID.String(), cluster.Name+"/"+coll.Role, cl, mergePipelines)
	if err != nil {
		return fmt.Errorf("merge: %w", err)
	}

	if result := r.validator.Stages12(ctx, assembled.Content); !result.Valid {
		return fmt.Errorf("merged config invalid: %v", result.Diagnostics)
	}
	return nil
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

// markWarning records a sync that held its deletes back on purpose
// (pruneGuard) after applying the commit's creates and updates.
// last_commit stays unset so the next poll re-checks instead of taking the
// unchanged-commit shortcut back to "ok".
func (r *Reconciler) markWarning(ctx context.Context, linkID pgtype.UUID, warning string) {
	if err := r.store.Queries.UpdateRepoLinkSync(ctx, sqlc.UpdateRepoLinkSyncParams{
		ID:         linkID,
		LastCommit: pgtype.Text{},
		SyncStatus: pgtype.Text{String: "warning", Valid: true},
		SyncError:  pgtype.Text{String: warning, Valid: true},
	}); err != nil {
		r.logger.Error("gitsync: recording sync warning", "link_id", linkID, "err", err)
	}
}
