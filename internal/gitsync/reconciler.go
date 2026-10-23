// Package gitsync implements the git GitOps reconciliation loop. It polls
// repo_links that are due for sync, downloads .alloy files over real git
// (via internal/gitrepo) using whichever auth strategy the linked
// credential's kind selects, validates each file and then the commit as a
// whole, and upserts them as git-sourced pipelines. A successful sync also
// deletes the link's pipelines whose file has left the repository. A commit
// applies whole or not at all (docs/spec.md §8, §10).
package gitsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/beacon"
	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/gitrepo"
	"shepherd/internal/merge"
	"shepherd/internal/metrics"
	"shepherd/internal/reconcile"
	"shepherd/internal/schema"
	"shepherd/internal/serve"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/tenancy"
	"shepherd/internal/validate"
)

// Reconciler polls due repo_links and syncs them.
type Reconciler struct {
	store     *store.Store
	crypto    *crypto.Encryptor
	validator *validate.Validator
	// schema drives the commit dry run's role enforcement (stage3DryRun),
	// the merge.WithRoleEnforcement every serve path applies.
	schema *schema.Registry
	// beaconBaseline is D6's baseline pipeline, appended by every serve path
	// and so by the dry run too (serve.Assemble).
	beaconBaseline beacon.BaselineConfig
	cfg            *config.GitSyncConfig
	// requireOwned is the server-wide tenants.require_owned setting: strict
	// tenant mode's pipeline lint for every org.
	requireOwned bool
	limits       gitrepo.Limits
	tokenCache   *gitrepo.TokenCache
	logger       *slog.Logger
	// locker, when set, limits passes to one replica at a time (see Locker).
	locker Locker
	// recompute, when set, refreshes the link's collector's served config
	// right after a sync changed its git pipelines (see WithRecompute).
	recompute func(ctx context.Context, orgID, collectorID pgtype.UUID)
	// planFileFunc, when set, replaces planFile. Test seam only: specs use it
	// to make one planning pass fail transiently (a validator timeout).
	planFileFunc func(ctx context.Context, link sqlc.RepoLink, file gitrepo.File) (*filePlan, error)
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

// WithRecompute makes the Reconciler call recompute for the link's org and
// collector after a sync that created, updated or deleted a pipeline
// committed — the eager served-config refresh the API's own writes run
// (mgmtapi's PipelineService.RecomputeCollectorCache), so the served config
// shown for the collector, an offline one included, follows the commit
// without waiting for a poll. A git pipeline is served to its link's
// collector only (§6.1), so no other collector is recomputed. Without it the
// collector is still marked dirty and recomputes on its next poll.
func WithRecompute(recompute func(ctx context.Context, orgID, collectorID pgtype.UUID)) Option {
	return func(r *Reconciler) { r.recompute = recompute }
}

// New creates a Reconciler. reg is the schema registry every serve path
// enforces roles with; a nil reg refuses every commit (stage3DryRun) rather
// than syncing one unenforced.
func New(st *store.Store, enc *crypto.Encryptor, v *validate.Validator, reg *schema.Registry, cfg *config.Config, logger *slog.Logger, opts ...Option) *Reconciler {
	r := &Reconciler{
		store:          st,
		crypto:         enc,
		validator:      v,
		schema:         reg,
		beaconBaseline: serve.BeaconBaseline(cfg),
		cfg:            &cfg.GitSync,
		requireOwned:   cfg.Tenants.RequireOwned,
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
// line with it, all or nothing (docs/spec.md §8: "never partially apply a
// commit"). Each file is checked on its own first (planFile: Stages 1-2 and
// ownership; planAll: sanitised-name clashes), then the commit as a whole
// (stage3DryRun: the link's collector's merged config with every create,
// update and delete of the commit in it, role enforcement included). Only
// when every check passes does apply write the commit — the deletes of
// pipelines whose file left the repository, the creates and updates, their
// revisions and audit rows, the collector's dirty mark and the link's sync
// status — in ONE transaction.
//
// When any file fails, or a prune guard trips (pruneGuard), nothing from the
// commit is written: the collector keeps serving the previous commit's
// config, and last_commit stays unset so the next poll tries again.
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
		// A commit applied with a warning (pre-existing validation failures
		// elsewhere in the collector's merged config, stage3DryRun) keeps
		// its warning until the next commit; anything else reads "ok".
		status, syncError, outcome := pgtype.Text{String: "ok", Valid: true}, pgtype.Text{}, outcomeOK
		if link.SyncStatus.String == "warning" {
			status, syncError, outcome = link.SyncStatus, link.SyncError, outcomeWarning
		}
		if syncErr := r.store.Queries.UpdateRepoLinkSync(ctx, sqlc.UpdateRepoLinkSyncParams{
			ID: link.ID, LastCommit: pgtype.Text{String: latestCommit, Valid: true},
			SyncStatus: status, SyncError: syncError,
		}); syncErr != nil {
			r.logger.Error("gitsync: recording sync status", "link_id", link.ID, "err", syncErr)
		}
		return outcome, nil
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
	for _, p := range owned {
		if !present[p.Name] {
			removed = append(removed, p)
		}
	}

	// A tripped guard holds back the whole commit, not just its deletes:
	// applying the rest would serve a state the repository never held — a
	// moved directory's new copies beside the old ones (duplicate scrapes
	// and remote_write, two receivers on one port), or updates that only
	// make sense with the deletes they came with.
	baseline := guardBaseline(link, owned)
	if guard := pruneGuard(len(present), len(owned), baseline, len(removed), link.LastOkAt.Valid); guard != guardNone {
		r.logger.Warn("gitsync: commit not applied: it would remove too many pipelines",
			"link_id", link.ID, "commit", commit, "pipelines", len(owned), "baseline", baseline,
			"removing", len(removed), "branch", link.Branch, "path", link.Path)
		r.markWarning(ctx, link.ID, guardMessage(guard, len(removed), baseline))
		return outcomeWarning, nil
	}

	plans, fileErrs, err := r.planAll(ctx, link, tree.Files, removed, scrub)
	if err != nil {
		return fail(scrub(err))
	}
	warning := ""
	if len(fileErrs) == 0 && (len(plans) > 0 || len(removed) > 0) {
		verdict, err := r.stage3DryRun(ctx, link, plans, owned, removed)
		if err != nil {
			return fail(scrub(fmt.Errorf("stage-3 merge dry-run: %w", err)))
		}
		fileErrs = verdict.refusals
		if len(verdict.preexisting) > 0 {
			warning = preexistingWarning(verdict.preexisting)
		}
	}
	if len(fileErrs) > 0 {
		for _, e := range fileErrs {
			r.logger.Warn("gitsync: commit not applied", "link_id", link.ID, "commit", commit, "err", e)
		}
		return fail(commitRefused(fileErrs))
	}

	// apply has rolled its transaction back by the time it returns an
	// error, so markError (a write to the same link row on another
	// connection) never waits on the row lock apply took.
	changed, err := r.apply(ctx, link, commit, plans, removed, warning)
	if errors.Is(err, errLinkGone) {
		r.logger.Info("gitsync: repo link deleted during its sync; nothing written", "link_id", link.ID)
		return outcomeLinkGone, nil
	}
	if err != nil {
		return fail(err)
	}
	if changed && r.recompute != nil {
		r.recompute(ctx, link.OrgID, link.CollectorID)
	}
	if warning != "" {
		r.logger.Warn("gitsync: commit applied; the collector's merged config was already invalid",
			"link_id", link.ID, "commit", commit, "warning", warning)
		return outcomeWarning, nil
	}
	return outcomeOK, nil
}

// maxNamedFailures is how many failing files a refused commit's sync_error
// lists before summarising the rest as "and K more".
const maxNamedFailures = 5

// commitRefused is the sync_error of a commit refused because of fileErrs
// (one per failing file, each starting with the file's path): the first
// maxNamedFailures of them, then how many more failed.
func commitRefused(fileErrs []error) error {
	lines := make([]string, 0, maxNamedFailures+1)
	for i, e := range fileErrs {
		if i == maxNamedFailures {
			lines = append(lines, fmt.Sprintf("and %d more", len(fileErrs)-i))
			break
		}
		lines = append(lines, e.Error())
	}
	return fmt.Errorf("commit not synced, nothing from it was applied:\n%s", strings.Join(lines, "\n"))
}

// planAll plans every fetched file (planFile), then fails — that file, with
// its own error — every planned write whose sanitised name another pipeline
// of the org already holds, or another file of this sync claims. The unique
// index pipelines_org_sanitized_name_key would otherwise refuse it at write
// time, and the Stage-3 dry run cannot catch it: it sees only the pipelines
// this collector is served. removed are the link's pipelines this sync
// deletes: their names are free to take. The returned error is a store
// failure that ends the sync; per-file problems are in fileErrs, and any one
// of them refuses the whole commit (syncLink).
func (r *Reconciler) planAll(ctx context.Context, link sqlc.RepoLink, files []gitrepo.File, removed []sqlc.ListGitPipelinesByRepoLinkRow, scrub func(error) error) ([]filePlan, []error, error) {
	var plans []filePlan
	var fileErrs []error
	for _, file := range files {
		planFile := r.planFile
		if r.planFileFunc != nil {
			planFile = r.planFileFunc
		}
		plan, err := planFile(ctx, link, file)
		if err != nil {
			fileErrs = append(fileErrs, fmt.Errorf("%s: %w", file.Path, scrub(err)))
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
			fileErrs = append(fileErrs, clash)
			continue
		}
		kept = append(kept, p)
	}
	return kept, fileErrs, nil
}

// maxUnguardedPrune is the largest number of pipelines one sync may delete
// without the half-of-the-link check applying (pruneGuard).
const maxUnguardedPrune = 3

// guardBaseline is the denominator of the more-than-half guard: how many of
// the link's pipelines existed when its last sync finished "ok"
// (repo_links.last_ok_at, stamped by a trigger, migration 0034; every link
// that existed at that upgrade starts from the upgrade's moment). Pipelines
// created since do not count — a sync writes only when it finishes "ok", so
// none should exist, but a write from an older replica during a rolling
// update could add some — so nothing the link gains while the deletes wait
// can weigh the same deletes against a larger set and release the guard on
// its own. Measured against this baseline, the guard holds
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

// pruneGuardKind is which guard, if any, holds a sync's deletes back.
type pruneGuardKind int

const (
	guardNone pruneGuardKind = iota
	// guardEmptyTree: no .alloy files at all while the link has pipelines.
	guardEmptyTree
	// guardThreshold: more than maxUnguardedPrune deletes and more than half
	// of the baseline.
	guardThreshold
	// guardNeverOK: more than maxUnguardedPrune deletes on a link that has
	// never finished a sync "ok" (no baseline).
	guardNeverOK
)

// pruneGuard decides whether a sync's deletes may go ahead. present is the
// number of pipeline files in the synced tree, owned the link's pipelines,
// baseline how many of those existed at the link's last ok sync
// (guardBaseline), removing how many of the link's pipelines have no file
// any more, everOK whether the link has ever finished a sync "ok".
//
//   - No .alloy files at all while the link has pipelines: far more often a
//     wrong path or branch (a missing path reads as an empty tree) than a
//     deliberate wipe.
//   - More than half of the baseline AND more than maxUnguardedPrune: a
//     mass deletion in one commit is more likely a mistake (a moved
//     directory, a bad merge) than intent. With no baseline (never ok),
//     more than maxUnguardedPrune.
//
// When a guard holds, nothing from the commit is applied: no delete, no
// create, no update. The link shows sync_status=warning with guardMessage,
// and last_commit stays unset so every poll checks again — a full clone —
// until the repository or the link changes.
func pruneGuard(present, owned, baseline, removing int, everOK bool) pruneGuardKind {
	if present == 0 && owned > 0 {
		return guardEmptyTree
	}
	if removing > maxUnguardedPrune && 2*removing > baseline {
		if !everOK {
			return guardNeverOK
		}
		return guardThreshold
	}
	return guardNone
}

// guardRemedy is the advice every mass-delete warning ends with: what
// actually clears it, given the guard compares the branch head with the
// database (removing fewer files per commit cannot help once they are gone).
const guardRemedy = "To apply it, delete and re-create the repository link; " +
	"or restore some of the removed files, then remove them again a few at a time " +
	"(at most 3, or no more than half of what remains, per sync), letting each sync complete."

// guardMessage is the sync_error a held-back sync records: which guard held
// the commit, how many pipelines it would remove, and how to resolve it.
func guardMessage(kind pruneGuardKind, removing, baseline int) string {
	switch kind {
	case guardNone:
		return ""
	case guardEmptyTree:
		return fmt.Sprintf("the repository has no .alloy files under this link's path on this branch, "+
			"so this commit would delete all %d of the link's synced pipelines; nothing from it was applied. "+
			"Check the link's path and branch. To remove every pipeline deliberately, delete the repository link.",
			removing)
	case guardNeverOK:
		return fmt.Sprintf("this link has not completed a sync yet; until it does, one sync may delete at most %d pipelines, "+
			"and this commit would delete %d, so nothing from it was applied. "+
			"To apply it, delete and re-create the repository link; or restore some of the removed files, "+
			"then remove them again at most %d per sync, letting each sync complete.",
			maxUnguardedPrune, removing, maxUnguardedPrune)
	case guardThreshold:
		return fmt.Sprintf("this commit would delete %d of %d pipelines (more than half, and more than %d) in one sync, "+
			"so nothing from it was applied. %s",
			removing, baseline, maxUnguardedPrune, guardRemedy)
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
// collector — the only one a git pipeline is served to (§6.1) — and records
// sync_status=ok at commit (then "warning", when warning is set). It is only ever handed a whole commit that passed
// every check (syncLink), so it never writes part of one. It reports whether a pipeline
// was created, updated or deleted. Its deferred rollback has run by the time
// it returns an error, so the caller's markError never waits on the link row
// lock taken here.
func (r *Reconciler) apply(ctx context.Context, link sqlc.RepoLink, commit string, plans []filePlan, removed []sqlc.ListGitPipelinesByRepoLinkRow, warning string) (bool, error) {
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
	// Strict tenant mode, again under the shared ownership lock: the
	// per-file lint ran before this transaction, and the org may have turned
	// strict (or lost a tenant) since.
	lint := make([]tenancy.Pipeline, len(plans))
	for i := range plans {
		lint[i] = tenancy.Pipeline{Name: plans[i].path, Contents: plans[i].contents}
	}
	if err := tenancy.LintLocked(ctx, q, link.OrgID, r.requireOwned, lint...); err != nil {
		return false, err
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
		// management API writes (pipeline.create/pipeline.update). The
		// detail says where the change came from, as pipeline.delete's does.
		if err := audit(ctx, q, link.OrgID, "pipeline."+action, p.ID, map[string]string{
			"repo_link_id": link.ID.String(),
			"git_path":     plan.path,
			"commit":       commit,
		}); err != nil {
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
	if err := q.UpdateRepoLinkSync(ctx, sqlc.UpdateRepoLinkSyncParams{
		ID:         link.ID,
		LastCommit: pgtype.Text{String: commit, Valid: true},
		SyncStatus: pgtype.Text{String: "ok", Valid: true},
		SyncError:  pgtype.Text{},
	}); err != nil {
		return false, fmt.Errorf("recording sync status: %w", err)
	}
	// Applied with a warning: the commit is recorded "ok" first, which
	// stamps last_ok_at (migration 0034's trigger) — the commit WAS applied,
	// and the prune guard's baseline must count it — then the warning goes
	// over it with last_commit kept, which tells it apart from a guard's
	// held commit (last_commit unset).
	if warning != "" {
		if err := q.UpdateRepoLinkSync(ctx, sqlc.UpdateRepoLinkSyncParams{
			ID:         link.ID,
			LastCommit: pgtype.Text{String: commit, Valid: true},
			SyncStatus: pgtype.Text{String: "warning", Valid: true},
			SyncError:  pgtype.Text{String: warning, Valid: true},
		}); err != nil {
			return false, fmt.Errorf("recording sync warning: %w", err)
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

// planFile validates one file on its own (Stages 1-2, ownership) and decides
// what it changes. It returns nil, nil when the file's pipeline already holds
// exactly this content. The merged config is checked once for the whole
// commit afterwards (stage3DryRun); these per-file checks exist for an error
// that names the file and its own diagnostics.
func (r *Reconciler) planFile(ctx context.Context, link sqlc.RepoLink, file gitrepo.File) (*filePlan, error) {
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
	//
	// In strict tenant mode the gate's tenant lint runs too, against the
	// org's tenants as they stand now: one file setting a tenant the org
	// does not own refuses the whole commit, like any other invalid file.
	policy, err := tenancy.Load(ctx, r.store.Queries, link.OrgID, r.requireOwned)
	if err != nil {
		return nil, fmt.Errorf("loading the org's tenant policy: %w", err)
	}
	if result := r.validator.ValidatePipeline(ctx, name, contents, policy.Validation()); !result.Valid {
		return nil, fmt.Errorf("validation errors in %s: %v", file.Path, result.Diagnostics)
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

// dryRunResult is stage3DryRun's verdict on a commit.
type dryRunResult struct {
	// refusals are the reasons the commit is refused, each naming its file
	// where one is to blame. Empty means the commit may be applied.
	refusals []error
	// preexisting names what already failed validation in the collector's
	// merged config BEFORE the commit (a pipeline by its file path or name,
	// or the merged config itself), when the commit's merged config fails
	// only on those. The commit is applied, and the link warns.
	preexisting []string
}

// stage3DryRun is the commit's gate: it assembles the merged config the
// link's collector would be served once the WHOLE commit is applied — every
// create and update in plans, every pipeline in removed gone — exactly as the
// serve paths build it (serve.Assemble: role enforcement and D6's baseline
// included), and validates that one config with Stages 1 and 2. Checking the
// files one at a time against the rows they replace would validate configs
// no collector is ever served.
//
// The commit is refused for a created or updated pipeline that role
// enforcement would exclude from the collector (a git pipeline is served to
// that one collector only, so an excluded file would sync "ok" and do
// nothing), for a declare-name collision, and for a Stage 1/2 diagnostic the
// commit is responsible for. When the commit's merged config fails, the
// collector's CURRENT merged config (same inputs, without the commit) is
// validated too, and only new diagnostics — or any inside the commit's own
// files' blocks — refuse it (newDiagnostics). A pipeline the commit does not
// touch, broken by something else (an Alloy upgrade, say), must not freeze
// every future commit on the link. That second validation runs only when the
// first fails, so a passing commit costs one.
//
// owned are the link's pipelines before the commit, so a diagnostic in one
// of its unchanged files is named by path. err is a failure that is no
// verdict on the commit (a store error, a missing validator or schema) — it
// refuses the commit too.
func (r *Reconciler) stage3DryRun(ctx context.Context, link sqlc.RepoLink, plans []filePlan, owned, removed []sqlc.ListGitPipelinesByRepoLinkRow) (dryRunResult, error) {
	if r.validator == nil {
		return dryRunResult{}, errors.New("gitsync: validator not configured")
	}
	if r.schema == nil {
		// Fail closed, as for the validator: a dry run without role
		// enforcement would accept a file every serve path then excludes.
		return dryRunResult{}, errors.New("gitsync: schema registry not configured")
	}
	if timeout := r.validator.Stage3Timeout(); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	rows, err := r.store.Queries.ListEnabledPipelinesForMerge(ctx, link.OrgID)
	if err != nil {
		return dryRunResult{}, fmt.Errorf("loading pipelines: %w", err)
	}
	// The serve paths' own row conversion (internal/serve), so the dry run
	// models what they serve. current is the collector's input as it stands;
	// next is it with the commit applied.
	current := serve.PipelinesFromRows(rows)
	gone := make(map[string]bool, len(removed))
	for _, p := range removed {
		gone[p.ID.String()] = true
	}
	next := slices.DeleteFunc(slices.Clone(current), func(mp merge.Pipeline) bool { return gone[mp.ID] })

	// paths names a pipeline of this link by its file: the commit's files,
	// then the link's unchanged ones. changed is the commit's own.
	paths := make(map[string]string, len(owned)+len(plans))
	for _, p := range owned {
		if p.GitPath.Valid {
			paths[p.Name] = p.GitPath.String
		}
	}
	changed := make(map[string]bool, len(plans))
	for _, p := range plans {
		paths[p.name] = p.path
		changed[p.name] = true
		cand := merge.Pipeline{
			ID:                  "gitsync-candidate:" + p.path,
			Name:                p.name,
			Contents:            p.contents,
			Source:              "git",
			RepoLinkCollectorID: link.CollectorID.String(),
		}
		if p.existing == nil {
			next = append(next, cand)
			continue
		}
		// An update replaces its row and keeps the row's revision for the
		// header ("(rev N)" is a comment line; the dry run validates, it
		// never serves). A disabled pipeline is not served, so its update
		// is not in the merged config; its own Stages 1-2 ran in planFile,
		// and enabling it runs the API's Stage 3.
		id := p.existing.ID.String()
		if i := slices.IndexFunc(next, func(mp merge.Pipeline) bool { return mp.ID == id }); i >= 0 {
			cand.ID, cand.Revision = id, next[i].Revision
			next[i] = cand
		}
	}

	coll, err := r.store.Queries.GetCollectorByID(ctx, link.CollectorID)
	if err != nil {
		return dryRunResult{}, fmt.Errorf("loading collector: %w", err)
	}
	cluster, err := r.store.Queries.GetClusterByID(ctx, coll.ClusterID)
	if err != nil {
		return dryRunResult{}, fmt.Errorf("loading cluster: %w", err)
	}
	collectorKey := cluster.Name + "/" + coll.Role

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
	assemble := func(pipelines []merge.Pipeline) (serve.Result, error) {
		return serve.Assemble(
			serve.Deps{Schema: r.schema, BeaconBaseline: r.beaconBaseline},
			serve.Collector{ID: link.CollectorID.String(), Cluster: cluster.Name, Role: coll.Role, AdminLabels: adminLabels, LocalAttrs: localAttrs},
			pipelines,
		)
	}

	served, err := assemble(next)
	if err != nil {
		// With the schema present, merge.Assemble refuses only a
		// declare-name collision, which names both pipelines.
		return dryRunResult{refusals: []error{fmt.Errorf("the merged config for collector %s: %w", collectorKey, err)}}, nil
	}

	// Role enforcement (gate G6), the rule every serve path assembles with.
	// Only the commit's own files refuse it: a pipeline the commit does not
	// touch is already served (or excluded) as it is.
	var refusals []error
	for _, ex := range served.Exclusions {
		if !changed[ex.PipelineName] {
			continue
		}
		why := reconcile.ExcludedPipeline{
			Name: ex.PipelineName, Reason: ex.Reason, Disallowed: ex.Disallowed, Unproven: ex.Unproven,
		}.Why(coll.Role)
		refusals = append(refusals, fmt.Errorf("%s: collector %s would not serve it: %s", paths[ex.PipelineName], collectorKey, why))
	}
	if len(refusals) > 0 {
		return dryRunResult{refusals: refusals}, nil
	}

	result := r.validator.Stages12(ctx, served.Content)
	if result.Valid {
		return dryRunResult{}, nil
	}
	if err := inconclusive(ctx, result); err != nil {
		return dryRunResult{}, err
	}
	after := locateDiagnostics(served.Content, result.Diagnostics)

	// The baseline: the collector's merged config without the commit. If it
	// cannot be assembled, nothing counts as pre-existing. If its validation
	// gives no verdict (the budget ran out, the binary failed), the commit
	// is refused: an unanswered check is never an error "already there".
	var before []locatedDiag
	if baseServed, baseErr := assemble(current); baseErr == nil {
		base := r.validator.Stages12(ctx, baseServed.Content)
		if !base.Valid {
			if err := inconclusive(ctx, base); err != nil {
				return dryRunResult{}, err
			}
			before = locateDiagnostics(baseServed.Content, base.Diagnostics)
		}
	}
	return judge(after, before, changed, paths, collectorKey), nil
}

// inconclusive reports a Stages 1-2 run that gave no verdict on the content
// — the dry run's budget ran out, or alloy validate itself could not answer
// (validate.Result.Inconclusive) — as an error that refuses the commit.
func inconclusive(ctx context.Context, res validate.Result) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("validation could not complete: %w", err)
	}
	if res.Inconclusive {
		msg := "alloy validate gave no verdict"
		if len(res.Diagnostics) > 0 {
			msg = res.Diagnostics[0].Message
		}
		return fmt.Errorf("validation could not complete: %s", msg)
	}
	return nil
}

// judge decides a commit whose merged config failed Stages 1-2 (after),
// given the diagnostics of the collector's merged config before it
// (before). A Stage 1 failure before the commit refuses it: Stage 2 never
// runs on a config that does not parse, so nothing the commit does could be
// checked — the broken pipeline must be fixed first. Otherwise the commit
// is refused for what it brings (newDiagnostics), and what was already
// there is returned as preexisting, to apply the commit with a warning.
func judge(after, before []locatedDiag, changed map[string]bool, paths map[string]string, collectorKey string) dryRunResult {
	var syntax []string
	for _, d := range before {
		if d.stage == 1 {
			syntax = appendOnce(syntax, d.label(paths, collectorKey))
		}
	}
	if len(syntax) > 0 {
		return dryRunResult{refusals: []error{fmt.Errorf(
			"the collector's merged config already fails its syntax check before this commit, in: %s; "+
				"fix that first — until it parses, no commit for this collector can be validated",
			strings.Join(syntax, "; "))}}
	}
	fresh, old := newDiagnostics(after, before, changed)
	if len(fresh) > 0 {
		return dryRunResult{refusals: attributeDiagnostics(fresh, paths, collectorKey)}
	}
	var preexisting []string
	for _, d := range old {
		preexisting = appendOnce(preexisting, d.label(paths, collectorKey))
	}
	return dryRunResult{preexisting: preexisting}
}

func appendOnce(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// locatedDiag is a merged-config diagnostic attributed to the pipeline whose
// declare block contains it (merge.Locate). pipeline is "" for a line no
// block contains (the header, D6's baseline). line/col are the position in
// the pipeline's own text when pipelineLine is set, else in the merged
// config.
type locatedDiag struct {
	pipeline     string
	pipelineLine bool
	line, col    int
	message      string
	stage        int
}

// declareIndent is how far merge.Assemble indents a pipeline's text inside
// its declare block.
const declareIndent = 2

func locateDiagnostics(content string, diags []validate.Diagnostic) []locatedDiag {
	out := make([]locatedDiag, 0, len(diags))
	for _, d := range diags {
		ld := locatedDiag{line: d.Line, col: d.Col, message: d.Message, stage: d.Stage}
		if loc, ok := merge.Locate(content, d.Line); ok {
			ld.pipeline = loc.PipelineName
			if loc.PipelineLine > 0 {
				ld.pipelineLine = true
				ld.line = loc.PipelineLine
				if ld.col > declareIndent {
					ld.col -= declareIndent
				}
			}
		}
		out = append(out, ld)
	}
	return out
}

// alloyLocRE matches what differs between two validations of the same
// mistake in a message: the temp file alloy validate was handed, with the
// "line:col" Alloy writes after it when it quotes a position. Nothing else
// is rewritten — a "host:port" in a message is part of the mistake.
var alloyLocRE = regexp.MustCompile(`\S*\.alloy(?::\d+:\d+)?`)

// key identifies a diagnostic across the two merged configs stage3DryRun
// validates. Merged-config line numbers are no use: a block the commit adds
// or removes shifts every line after it. A pipeline the commit does not touch
// has the same text in both configs, so (pipeline, line and column in its own
// text, message) is stable for it. A line outside every block (the header,
// the baseline) has no stable position, so only the message counts there.
// Messages are normalised (alloyLocRE) because a message can quote the temp
// file alloy validate read, and a position in it.
func (d locatedDiag) key() string {
	msg := alloyLocRE.ReplaceAllString(d.message, "<file>")
	if !d.pipelineLine {
		return d.pipeline + "\x00" + msg
	}
	return fmt.Sprintf("%s\x00%d:%d\x00%s", d.pipeline, d.line, d.col, msg)
}

// label names where d is: a pipeline of the link by its file path, another
// pipeline by name, anything else as the collector's merged config.
func (d locatedDiag) label(paths map[string]string, collectorKey string) string {
	switch {
	case d.pipeline == "":
		return "the merged config for collector " + collectorKey
	case paths[d.pipeline] != "":
		return paths[d.pipeline]
	default:
		return fmt.Sprintf("pipeline %q in the merged config for collector %s", d.pipeline, collectorKey)
	}
}

// newDiagnostics splits the commit's merged-config diagnostics into the ones
// the commit is responsible for — any inside one of its own files' blocks
// (changed), and any other whose key the config before the commit did not
// have — and the pre-existing rest.
func newDiagnostics(after, before []locatedDiag, changed map[string]bool) (fresh, old []locatedDiag) {
	had := make(map[string]bool, len(before))
	for _, d := range before {
		had[d.key()] = true
	}
	for _, d := range after {
		if changed[d.pipeline] || !had[d.key()] {
			fresh = append(fresh, d)
			continue
		}
		old = append(old, d)
	}
	return fresh, old
}

// attributeDiagnostics turns located diagnostics into refusals, one per place
// they fall in (locatedDiag.label), with positions in the pipeline's own
// text where there is one.
func attributeDiagnostics(diags []locatedDiag, paths map[string]string, collectorKey string) []error {
	var order []string
	msgs := map[string][]string{}
	for _, d := range diags {
		owner := d.label(paths, collectorKey)
		if _, seen := msgs[owner]; !seen {
			order = append(order, owner)
		}
		msgs[owner] = append(msgs[owner], fmt.Sprintf("%d:%d %s", d.line, d.col, d.message))
	}
	out := make([]error, 0, len(order))
	for _, owner := range order {
		out = append(out, fmt.Errorf("%s: merged config invalid: %s", owner, strings.Join(msgs[owner], "; ")))
	}
	return out
}

// preexistingWarning is the sync_error of a commit applied although the
// collector's merged config already failed validation before it, in places
// the commit did not touch.
func preexistingWarning(places []string) string {
	named := places
	more := ""
	if len(named) > maxNamedFailures {
		named, more = places[:maxNamedFailures], fmt.Sprintf(" and %d more", len(places)-maxNamedFailures)
	}
	return fmt.Sprintf("commit applied, but the collector's merged config already failed validation before it, in: %s%s. "+
		"The collector will likely reject the merged config and keep running its previous one until those are fixed "+
		"where they are defined.", strings.Join(named, "; "), more)
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

// markWarning records a commit held back on purpose (pruneGuard): nothing
// from it was applied. last_commit stays unset so the next poll re-checks
// instead of taking the unchanged-commit shortcut. (A commit applied with a
// warning is recorded by apply instead, with last_commit set.)
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
