// Package pinnedimport is how strict tenant mode reads import.git modules
// (#390): a strict org may use import.git only when its revision is a full
// commit SHA and its repository is on the org's allowlist
// (org_import_repositories, 0043, kept by an app admin). The tenant lint
// (validate.TenantLintChecked) asks a validate.GitModules from here whether
// a repository is allowed and for each module's text, and lints that text
// with the same rules.
//
// Modules are fetched at save time, in the validation path, with git sync's
// limits (gitsync.max_repo_bytes, max_file_bytes and fetch_timeout) and a
// budget for the whole save (SaveBudget), and cached in the database by
// (org, repository, commit, path): a commit cannot change, so a cached
// module is never fetched again. The cache is serving data: a strict org is
// served each pinned import.git inlined as an import.string of the cached
// text (Inline), so collectors never fetch the module themselves and run
// exactly what the lint checked — a tag or branch later pushed under the
// SHA's name changes nothing. A module an enabled pipeline references is
// never evicted; each org keeps at most MaxCachedModules others.
//
// Shepherd fetches only a repository whose normal form
// (NormalizeRepository) is on the org's allowlist, over https, http or ssh
// only, with redirects to another host refused, using the entry's stored
// git credential (if any). Credential material never reaches a log line or
// a message: errors are scrubbed of the credential's secrets (gitcred).
package pinnedimport

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/gitcred"
	"shepherd/internal/gitrepo"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// MaxCachedModules is how many fetched modules no enabled pipeline references
// each org keeps cached, the least recently used going first. Referenced ones
// are never evicted: they are what the org is served. Each is at most
// gitsync.max_file_bytes (1 MiB by default).
const MaxCachedModules = 128

// MaxConcurrentFetches is how many module fetches run at once in the
// process, across every save; a fetch waiting for a slot spends its save's
// budget (SaveBudget) while it waits.
const MaxConcurrentFetches = 4

var fetchSlots = make(chan struct{}, MaxConcurrentFetches)

// evictionGrace is how recently used an unreferenced module may be and still
// not be evicted: a save fetches its modules before it commits the pipeline
// that references them, so a module fetched moments ago is not "unused".
var evictionGrace = time.Hour

// SaveBudget bounds the wall-clock time one validation spends fetching
// modules, whatever their number; each fetch is also bounded by
// gitsync.fetch_timeout. A slow or huge repository makes the save fail
// (that import is refused), never hang.
const SaveBudget = 30 * time.Second

// Mode is when the modules are read.
type Mode int

const (
	// ModeSave is a save or a validation: a cached module is used (and
	// marked used); a module not cached is fetched.
	ModeSave Mode = iota
	// ModePreflight is a check of what is stored (tenant preflight, boot,
	// enabling a pipeline): a cached module is used as it is, without
	// touching the network; a module not cached is fetched.
	ModePreflight
	// ModeCached never touches the network: a module not cached is refused.
	// For checks inside a write transaction under the ownership lock, which
	// the save's own validation (ModeSave) has just filled the cache for.
	ModeCached
)

// Source makes the validate.GitModules one lint of an org's pipeline reads.
type Source interface {
	Modules(q *sqlc.Queries, orgID pgtype.UUID, repos []sqlc.OrgImportRepository) validate.GitModules
}

// fetcher is the git side, gitrepo in production.
type fetcher interface {
	FileAtCommit(ctx context.Context, repo gitrepo.Repo, commit, path string) ([]byte, error)
}

type gitFetcher struct{}

func (gitFetcher) FileAtCommit(ctx context.Context, repo gitrepo.Repo, commit, path string) ([]byte, error) {
	return repo.FileAtCommit(ctx, commit, path)
}

// Resolver fetches and caches pinned modules. A nil *Resolver reads the
// cache only (every mode behaves as ModeCached).
type Resolver struct {
	enc    *crypto.Encryptor
	tokens *gitrepo.TokenCache
	limits gitrepo.Limits
	budget time.Duration
	git    fetcher
	logger *slog.Logger
}

// New builds a Resolver with git sync's limits from cfg. enc may be nil:
// then only public repositories (no credential) can be fetched.
func New(cfg *config.Config, enc *crypto.Encryptor, logger *slog.Logger) *Resolver {
	return &Resolver{
		enc:    enc,
		tokens: gitrepo.NewTokenCache(),
		limits: gitrepo.Limits{
			MaxRepoBytes: cfg.GitSync.MaxRepoBytes,
			MaxFileBytes: cfg.GitSync.MaxFileBytes,
			MaxFiles:     cfg.GitSync.MaxFiles,
			FetchTimeout: cfg.GitSync.FetchTimeout,
		},
		budget: SaveBudget,
		git:    gitFetcher{},
		logger: logger.With("component", "pinnedimport"),
	}
}

type modeSource struct {
	r     *Resolver
	mode  Mode
	clock *budgetClock
}

// budgetClock is one fetch budget, shared by every lint a Source serves: it
// starts at the first network call.
type budgetClock struct {
	mu       sync.Mutex
	deadline time.Time
}

// Saving is the Source for a save or validation (ModeSave). Every lint it
// serves shares one SaveBudget: one Source per save, or per git sync pass.
func (r *Resolver) Saving() Source { return modeSource{r: r, mode: ModeSave, clock: &budgetClock{}} }

// Preflight is the Source for a check of stored pipelines (ModePreflight),
// with one SaveBudget across it.
func (r *Resolver) Preflight() Source {
	return modeSource{r: r, mode: ModePreflight, clock: &budgetClock{}}
}

// Cached is the Source that reads only the cache (ModeCached), through the
// q it is given: the write transaction's.
var Cached Source = modeSource{mode: ModeCached}

func (s modeSource) Modules(q *sqlc.Queries, orgID pgtype.UUID, repos []sqlc.OrgImportRepository) validate.GitModules {
	m := &modules{r: s.r, mode: s.mode, q: q, orgID: orgID, repos: map[string]sqlc.OrgImportRepository{}, clock: s.clock, list: repos}
	if s.r == nil {
		m.mode = ModeCached
	}
	for i := range repos {
		m.repos[repos[i].RepositoryKey] = repos[i]
	}
	return m
}

// modules is the validate.GitModules of one lint.
type modules struct {
	r     *Resolver
	mode  Mode
	q     *sqlc.Queries
	orgID pgtype.UUID
	repos map[string]sqlc.OrgImportRepository
	list  []sqlc.OrgImportRepository
	clock *budgetClock
}

func (m *modules) Allowed(repository string) error {
	key, err := NormalizeRepository(repository)
	if err != nil {
		return fmt.Errorf("repository %q cannot be matched to the allowlist: %w", repository, err)
	}
	if _, ok := m.repos[key]; !ok {
		return fmt.Errorf("repository %q %w", repository, errNotAllowed)
	}
	return nil
}

func (m *modules) Fetch(ctx context.Context, gm validate.GitModule) (string, string, error) {
	key, err := NormalizeRepository(gm.Repository)
	if err != nil {
		return "", "", err
	}
	entry, ok := m.repos[key]
	if !ok {
		return "", "", fmt.Errorf("repository %q %w", gm.Repository, errNotAllowed)
	}
	if !gitrepo.IsFullCommitSHA(gm.Revision) {
		return "", "", gitrepo.ErrNotACommit
	}
	row, err := m.q.GetPinnedImportModule(ctx, sqlc.GetPinnedImportModuleParams{
		OrgID: m.orgID, RepositoryKey: key, Revision: gm.Revision, Path: gm.Path,
	})
	switch {
	case err == nil:
		return m.cached(ctx, row)
	case !errors.Is(err, pgx.ErrNoRows):
		return "", "", errors.New("reading the module cache failed")
	}
	if m.mode == ModeCached {
		return "", "", errors.New("the module has not been fetched yet; validate or save the pipeline again to fetch it")
	}
	ctx, cancel := m.budgetCtx(ctx)
	defer cancel()
	repo, scrub, err := m.repo(ctx, entry, gm.Repository)
	if err != nil {
		return "", "", err
	}
	select {
	case fetchSlots <- struct{}{}:
	case <-ctx.Done():
		return "", "", describe(ctx.Err())
	}
	if ctx.Err() != nil { // a slot and the deadline arrived together
		<-fetchSlots
		return "", "", describe(ctx.Err())
	}
	data, err := m.r.git.FileAtCommit(ctx, repo, gm.Revision, gm.Path)
	<-fetchSlots
	if err != nil {
		err = scrub(err)
		m.r.logger.WarnContext(ctx, "pinned import: fetch failed",
			"org_id", m.orgID.String(), "repository", key, "revision", gm.Revision, "path", gm.Path, "err", err)
		return "", "", describe(err)
	}
	if !utf8.Valid(data) {
		return "", "", errors.New("the module is not UTF-8 text")
	}
	content := string(data)
	if err := m.q.PutPinnedImportModule(ctx, sqlc.PutPinnedImportModuleParams{
		OrgID: m.orgID, RepositoryKey: key, Revision: gm.Revision, Path: gm.Path, Content: content,
	}); err != nil {
		return "", "", errors.New("caching the fetched module failed")
	}
	if err := m.evict(ctx); err != nil {
		m.r.logger.WarnContext(ctx, "pinned import: evicting old cached modules", "org_id", m.orgID.String(), "err", err)
	}
	return content, "", nil
}

// evict removes the org's cached modules beyond MaxCachedModules that no
// enabled pipeline of the org references (References, through the cache)
// and that were not used within evictionGrace. A referenced module is what
// the org is served, so it is never evicted.
func (m *modules) evict(ctx context.Context) error {
	rows, err := m.q.ListPinnedImportModuleKeys(ctx, m.orgID)
	if err != nil || len(rows) <= MaxCachedModules {
		return err
	}
	pipelines, err := m.q.ListEnabledPipelinesByOrg(ctx, m.orgID)
	if err != nil {
		return err
	}
	used := map[Ref]bool{}
	for i := range pipelines {
		refs, err := References(ctx, m.q, m.orgID, m.list, pipelines[i].Contents)
		if err != nil {
			return err
		}
		for _, r := range refs {
			used[r] = true
		}
	}
	kept := 0
	cutoff := time.Now().Add(-evictionGrace)
	for _, row := range rows {
		if used[Ref{Key: row.RepositoryKey, Revision: row.Revision, Path: row.Path}] {
			continue
		}
		kept++
		if kept <= MaxCachedModules || row.UsedAt.Time.After(cutoff) {
			continue
		}
		if err := m.q.DeletePinnedImportModule(ctx, sqlc.DeletePinnedImportModuleParams{
			OrgID: m.orgID, RepositoryKey: row.RepositoryKey, Revision: row.Revision, Path: row.Path,
		}); err != nil {
			return err
		}
	}
	return nil
}

// cached answers from a cache row. It never contacts the source: the org
// is served this text, not an import collectors fetch.
func (m *modules) cached(ctx context.Context, row sqlc.PinnedImportModule) (string, string, error) {
	if m.mode == ModeSave {
		if err := m.q.TouchPinnedImportModule(ctx, sqlc.TouchPinnedImportModuleParams{
			OrgID: m.orgID, RepositoryKey: row.RepositoryKey, Revision: row.Revision, Path: row.Path,
		}); err != nil {
			m.r.logger.WarnContext(ctx, "pinned import: touching a cached module", "org_id", m.orgID.String(), "err", err)
		}
	}
	return row.Content, "", nil
}

// budgetCtx bounds ctx by the Source's fetch budget, which starts at its
// first network call.
func (m *modules) budgetCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	c := m.clock
	if c == nil {
		c = &budgetClock{}
		m.clock = c
	}
	c.mu.Lock()
	if c.deadline.IsZero() {
		c.deadline = time.Now().Add(m.r.budget)
	}
	d := c.deadline
	c.mu.Unlock()
	return context.WithDeadline(ctx, d)
}

// repo is the gitrepo.Repo for an allowlisted entry, fetched at url (the
// pipeline's own spelling, which normalises to the entry: what Alloy
// fetches), with the entry's credential and a scrubber of its secrets.
func (m *modules) repo(ctx context.Context, entry sqlc.OrgImportRepository, url string) (gitrepo.Repo, func(error) error, error) {
	repo := gitrepo.Repo{URL: url, Limits: m.r.limits, SameHostRedirects: true, Auth: gitrepo.NoneAuth{}}
	if !entry.CredentialID.Valid {
		return repo, func(err error) error { return err }, nil
	}
	cred, err := m.q.GetGitCredentialByID(ctx, entry.CredentialID)
	if err != nil || cred.OrgID != m.orgID {
		return repo, nil, errors.New("the allowlist entry's git credential could not be read")
	}
	auth, err := gitcred.BuildAuth(m.r.enc, m.r.tokens, cred)
	if err != nil {
		m.r.logger.WarnContext(ctx, "pinned import: building the credential", "org_id", m.orgID.String(),
			"credential_id", cred.ID.String(), "err", err)
		return repo, nil, fmt.Errorf("the allowlist entry's git credential %q cannot be used", cred.Name)
	}
	repo.Auth = auth
	repo.TLS = gitrepo.TLSOptions{CABundle: []byte(cred.CaCert.String), InsecureSkipVerify: cred.TlsInsecureSkipVerify}
	return repo, gitcred.Scrubber(auth), nil
}

// describe turns a fetch error into the refusal a person reads.
func describe(err error) error {
	switch {
	case errors.Is(err, gitrepo.ErrCommitNotFound):
		return errors.New("the commit is not in the repository")
	case errors.Is(err, gitrepo.ErrPathNotFound):
		return errors.New("the path does not exist at that commit")
	case errors.Is(err, gitrepo.ErrNotAFile):
		return errors.New("the path is not a regular file at that commit; name one .alloy file")
	case errors.Is(err, gitrepo.ErrFileTooLarge):
		return errors.New("the module is larger than gitsync.max_file_bytes")
	case errors.Is(err, gitrepo.ErrRevisionShadowed):
		return errors.New("a branch or tag in the repository is named like the commit SHA, and Alloy would check " +
			"that out instead")
	case errors.Is(err, gitrepo.ErrRepoTooLarge):
		return errors.New("the repository is larger than gitsync.max_repo_bytes")
	case errors.Is(err, gitrepo.ErrFetchTimeout), errors.Is(err, context.DeadlineExceeded):
		return errors.New("fetching the module timed out")
	case errors.Is(err, gitrepo.ErrRedirectRefused):
		return errors.New("the server redirected to another host, which Shepherd does not follow")
	default:
		return fmt.Errorf("fetching the module failed: %w", err)
	}
}
