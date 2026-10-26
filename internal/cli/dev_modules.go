package cli

// Dev-only fixtures for the 0.20 walkthrough: a Gitea repository of Alloy
// modules pinned by commit SHA (#390/#408), the allowlist row a strict org
// needs to import from it, the pipelines that import it, and the
// k8s-monitoring chart-shaped pipelines (#378).

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
	xhttp "github.com/go-git/go-git/v6/plumbing/transport/http"
	"github.com/go-git/go-git/v6/storage/memory"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/pinnedimport"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// The module repository. Its commits are built object by object with a fixed
// author, committer and timestamp (seedModuleCommits), so their SHAs are the
// same on every run and every machine — the pipelines below pin them as
// constants, and the walkthrough can quote them.
const (
	seedModuleRepoName = "shepherd-modules"
	// seedModuleRepoURL is the repository as Shepherd and the Alloy agents
	// reach it: in-cluster on kind (the gitea Service in shepherd-dev), on
	// the compose network on `make dev`. The allowlist row holds this URL.
	seedModuleRepoURL = seedGiteaBaseURL + "/" + seedGiteaAdminUser + "/" + seedModuleRepoName + ".git"

	seedModuleCleanPath   = "platform-metrics.alloy"
	seedModuleForeignPath = "foreign-tenant.alloy"

	// seedModuleCleanSHA is commit 1: platform-metrics.alloy only.
	seedModuleCleanSHA = "b42fd3c75d02ff44e7dbd89f83bc22f8d15eb410"
	// seedModuleForeignSHA is commit 2: adds foreign-tenant.alloy.
	seedModuleForeignSHA = "5c48c5d49ac7f34d79fd55b36c648687a4307350"

	// seedOwnedTenant is platform-org's own default tenant (upsertSeedOrg);
	// seedForeignTenant is data-eng's, which platform-org does not own.
	seedOwnedTenant   = "platform-org"
	seedForeignTenant = "data-eng"
)

// seedModuleClean is a module whose metrics pipeline writes under a tenant
// platform-org owns: strict mode accepts a pipeline importing it.
var seedModuleClean = `// Seeded by 'shepherd dev seed' (internal/cli/dev_modules.go). A module whose
// remote_write uses a tenant platform-org owns, so strict tenant mode
// accepts a pipeline importing it at this commit.
declare "platform_metrics" {
  prometheus.exporter.self "module" { }

  prometheus.scrape "module" {
    targets    = prometheus.exporter.self.module.targets
    forward_to = [prometheus.remote_write.module.receiver]
  }

  prometheus.remote_write "module" {
    endpoint {
      url     = "` + seedDestProdPromURL + `"
      headers = {
        "X-Scope-OrgID" = "` + seedOwnedTenant + `",
      }
    }
  }
}
`

// seedModuleForeign writes under data-eng's tenant: strict mode refuses a
// platform-org pipeline importing it.
var seedModuleForeign = `// Seeded by 'shepherd dev seed' (internal/cli/dev_modules.go). A module whose
// remote_write uses data-eng's tenant, which platform-org does not own, so
// strict tenant mode refuses a platform-org pipeline importing it.
declare "foreign_tenant_metrics" {
  prometheus.exporter.self "module" { }

  prometheus.scrape "module" {
    targets    = prometheus.exporter.self.module.targets
    forward_to = [prometheus.remote_write.module.receiver]
  }

  prometheus.remote_write "module" {
    endpoint {
      url     = "` + seedDestProdPromURL + `"
      headers = {
        "X-Scope-OrgID" = "` + seedForeignTenant + `",
      }
    }
  }
}
`

// seedModuleCommit is one commit of the module repository: the files its
// tree holds (the whole tree, not a diff) and its fixed time.
type seedModuleCommit struct {
	message string
	when    time.Time
	files   map[string]string
}

// seedModuleCommits is the module repository's history, oldest first.
func seedModuleCommits() []seedModuleCommit {
	base := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	return []seedModuleCommit{
		{
			message: "seed: add " + seedModuleCleanPath + "\n",
			when:    base,
			files:   map[string]string{seedModuleCleanPath: seedModuleClean},
		},
		{
			message: "seed: add " + seedModuleForeignPath + "\n",
			when:    base.Add(time.Minute),
			files: map[string]string{
				seedModuleCleanPath:   seedModuleClean,
				seedModuleForeignPath: seedModuleForeign,
			},
		},
	}
}

// buildSeedModuleRepo builds the module repository in memory with main at
// the last commit, and returns each commit's hash, oldest first. It writes
// the blob, tree and commit objects itself rather than committing through a
// worktree: go-git's worktree commit reads the system git config, and a
// commit.gpgSign there would sign (or refuse) the commit and change its SHA.
func buildSeedModuleRepo() (*git.Repository, []plumbing.Hash, error) {
	st := memory.NewStorage()
	repo, err := git.Init(st, git.WithDefaultBranch(plumbing.NewBranchReferenceName("main")))
	if err != nil {
		return nil, nil, fmt.Errorf("init: %w", err)
	}
	sig := func(when time.Time) object.Signature {
		return object.Signature{Name: "shepherd dev seed", Email: "dev-seed@shepherd.local", When: when}
	}
	var hashes []plumbing.Hash
	for _, c := range seedModuleCommits() {
		names := make([]string, 0, len(c.files))
		for name := range c.files {
			names = append(names, name)
		}
		sort.Strings(names) // a git tree lists its entries by name
		tree := &object.Tree{}
		for _, name := range names {
			blob := st.NewEncodedObject()
			blob.SetType(plumbing.BlobObject)
			w, err := blob.Writer()
			if err != nil {
				return nil, nil, fmt.Errorf("blob %s: %w", name, err)
			}
			if _, err := w.Write([]byte(c.files[name])); err != nil {
				return nil, nil, fmt.Errorf("blob %s: %w", name, err)
			}
			if err := w.Close(); err != nil {
				return nil, nil, fmt.Errorf("blob %s: %w", name, err)
			}
			h, err := st.SetEncodedObject(blob)
			if err != nil {
				return nil, nil, fmt.Errorf("storing blob %s: %w", name, err)
			}
			tree.Entries = append(tree.Entries, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: h})
		}
		treeObj := st.NewEncodedObject()
		if err := tree.Encode(treeObj); err != nil {
			return nil, nil, fmt.Errorf("encoding tree: %w", err)
		}
		treeHash, err := st.SetEncodedObject(treeObj)
		if err != nil {
			return nil, nil, fmt.Errorf("storing tree: %w", err)
		}
		var parents []plumbing.Hash
		if len(hashes) > 0 {
			parents = []plumbing.Hash{hashes[len(hashes)-1]}
		}
		commit := &object.Commit{
			Author: sig(c.when), Committer: sig(c.when), Message: c.message,
			TreeHash: treeHash, ParentHashes: parents,
		}
		commitObj := st.NewEncodedObject()
		if err := commit.Encode(commitObj); err != nil {
			return nil, nil, fmt.Errorf("encoding commit: %w", err)
		}
		h, err := st.SetEncodedObject(commitObj)
		if err != nil {
			return nil, nil, fmt.Errorf("storing commit: %w", err)
		}
		hashes = append(hashes, h)
	}
	mainRef := plumbing.NewHashReference(plumbing.NewBranchReferenceName("main"), hashes[len(hashes)-1])
	if err := st.SetReference(mainRef); err != nil {
		return nil, nil, fmt.Errorf("setting main: %w", err)
	}
	return repo, hashes, nil
}

// pushSeedModuleRepo pushes the module repository's main to url. A remote
// already at the same commit is fine: the commits are deterministic, so a
// re-seed pushes nothing.
func pushSeedModuleRepo(ctx context.Context, repo *git.Repository, url string, opts ...client.Option) error {
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "seed", URLs: []string{url}}); err != nil {
		return fmt.Errorf("adding remote: %w", err)
	}
	err := repo.PushContext(ctx, &git.PushOptions{
		RemoteName:    "seed",
		RefSpecs:      []config.RefSpec{"refs/heads/main:refs/heads/main"},
		ClientOptions: opts,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("push: %w", err)
	}
	return nil
}

// seedModuleRepo creates the shepherd-modules Gitea repository (empty, no
// auto_init, so the history is exactly seedModuleCommits), pushes the two
// commits, allows the repository for platform-org's pinned imports and seeds
// the pipelines that import it. Strict mode stays off: turning it on is a
// walkthrough step. Best-effort like seedGitOps — the caller only reports an
// error.
func seedModuleRepo(ctx context.Context, st *store.Store, imports *pinnedimport.Resolver, orgID pgtype.UUID) error {
	repo, hashes, err := buildSeedModuleRepo()
	if err != nil {
		return fmt.Errorf("building the module commits: %w", err)
	}
	if got := []string{hashes[0].String(), hashes[1].String()}; got[0] != seedModuleCleanSHA || got[1] != seedModuleForeignSHA {
		return fmt.Errorf("module commits are %v, the pinned SHAs say [%s %s]: update the constants with the module text",
			got, seedModuleCleanSHA, seedModuleForeignSHA)
	}
	if _, _, err := seedGiteaCreateRepo(ctx, seedModuleRepoName, false); err != nil && !isGiteaConflict(err) {
		return fmt.Errorf("creating gitea repo: %w", err)
	}
	auth := &xhttp.BasicAuth{Username: seedGiteaAdminUser, Password: seedGiteaAdminPass}
	if err := pushSeedModuleRepo(ctx, repo, seedModuleRepoURL, client.WithHTTPAuth(auth)); err != nil {
		return fmt.Errorf("pushing %s: %w", seedModuleRepoName, err)
	}
	fmt.Printf("modules: %s at %s — %s=%s (owned tenant %q), %s=%s (foreign tenant %q)\n",
		seedModuleRepoName, seedModuleRepoURL,
		seedModuleCleanPath, seedModuleCleanSHA, seedOwnedTenant,
		seedModuleForeignPath, seedModuleForeignSHA, seedForeignTenant)

	if err := seedImportAllowlist(ctx, st, orgID); err != nil {
		return err
	}
	return seedModulePipelines(ctx, st, orgID, imports.Preflight())
}

// seedModulePipelines seeds modulePipelineItems once the modules the enabled
// ones pin are in the pinned-import cache. Recording a pipeline's tenants
// (tenancy.RecordPipelineTenants, which seedPipelines calls as every API
// write does) reads that cache only; a save fills it first through its
// validation, and the seed writes no save. So the modules are fetched here
// through the same reader — the tenant lint over the pipeline text, with src
// (pinnedimport.Resolver.Preflight(): cached, or fetched and cached) — before
// the pipelines exist. Without it the server logged "the pipeline sets a
// tenant the lint cannot read; recorded as incomplete" until the first save.
func seedModulePipelines(ctx context.Context, st *store.Store, orgID pgtype.UUID, src pinnedimport.Source) error {
	items := modulePipelineItems()
	repos, err := st.Queries.ListOrgImportRepositories(ctx, orgID)
	if err != nil {
		return fmt.Errorf("loading the import allowlist: %w", err)
	}
	for _, item := range items {
		if !item.enabled {
			continue // recorded with no tenants, so nothing reads its module
		}
		_, complete, err := validate.TenantLiteralsWith(ctx, item.contents, src.Modules(st.Queries, orgID, repos))
		// Best effort, like the rest of the module seed: a module that could
		// not be fetched leaves the pipeline recorded incomplete until its
		// first save, as before — never a pipeline left unseeded.
		if err != nil || !complete {
			fmt.Printf("modules: %s's module is not cached (err=%v); its tenants are recorded incomplete until it is saved\n", item.name, err)
		}
	}
	if err := seedPipelines(ctx, st, orgID, items); err != nil {
		return fmt.Errorf("seeding module pipelines: %w", err)
	}
	return nil
}

// seedImportAllowlist puts the module repository on platform-org's import
// allowlist (0043), with no credential: Gitea serves public repositories
// anonymously. Idempotent.
func seedImportAllowlist(ctx context.Context, st *store.Store, orgID pgtype.UUID) error {
	key, err := pinnedimport.NormalizeRepository(seedModuleRepoURL)
	if err != nil {
		return fmt.Errorf("normalising %s: %w", seedModuleRepoURL, err)
	}
	rows, err := st.Queries.ListOrgImportRepositories(ctx, orgID)
	if err != nil {
		return fmt.Errorf("listing the import allowlist: %w", err)
	}
	for i := range rows {
		if rows[i].RepositoryKey == key {
			fmt.Printf("import allowlist: %s(exists)\n", seedModuleRepoURL)
			return nil
		}
	}
	if _, err := st.Queries.AddOrgImportRepository(ctx, sqlc.AddOrgImportRepositoryParams{
		OrgID: orgID, Repository: seedModuleRepoURL, RepositoryKey: key, CreatedBy: "dev-seed",
	}); err != nil && !isUnique(err) {
		return fmt.Errorf("adding %s to the import allowlist: %w", seedModuleRepoURL, err)
	}
	detail, err := json.Marshal(map[string]string{"repository": seedModuleRepoURL, "normalized": key})
	if err != nil {
		return err
	}
	if err := st.Queries.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{
		Actor: "seed", ActorType: "user", OrgID: orgID, Action: "org.import_repository.add",
		ResourceType: "org", ResourceID: orgID.String(), Detail: detail,
	}); err != nil {
		return fmt.Errorf("writing audit log for the import allowlist: %w", err)
	}
	fmt.Printf("import allowlist: %s (platform-org; strict mode left off)\n", seedModuleRepoURL)
	return nil
}

// pinnedImportContents is a pipeline importing one module of the seed
// repository at a pinned commit and instantiating its declare.
func pinnedImportContents(namespace, revision, path, declare string) string {
	return fmt.Sprintf(`import.git %q {
  repository = %q
  revision   = %q
  path       = %q
}

%s.%s "singleton" { }
`, namespace, seedModuleRepoURL, revision, path, namespace, declare)
}

// modulePipelineItems are the platform-org pipelines importing the module
// repository, matched to the prod-eu-1 singleton agent (alloy-singleton in
// dev/kind/alloy.yaml). The singleton role is signal-unrestricted, so a
// non-strict org's import.git — whose module the serve path cannot classify —
// is served there rather than excluded; once strict mode is on, the module
// is served inlined and classified (#378/#408).
func modulePipelineItems() []seedPipelineItem {
	singleton := []string{fmt.Sprintf("cluster=%q", seedClusterPlatformName), `role="singleton"`}
	return []seedPipelineItem{
		{
			name:     "pinned-module",
			contents: pinnedImportContents("platform", seedModuleCleanSHA, seedModuleCleanPath, "platform_metrics"),
			source:   "ui",
			matchers: singleton,
			enabled:  true,
		},
		{
			// Disabled: the walkthrough enables it with strict mode on and
			// expects the foreign tenant to be refused.
			name:     "pinned-module-foreign",
			contents: pinnedImportContents("foreign", seedModuleForeignSHA, seedModuleForeignPath, "foreign_tenant_metrics"),
			source:   "ui",
			matchers: singleton,
			enabled:  false,
		},
	}
}

// seedFixtures holds byte-for-byte copies of internal/signals/testdata's
// k8s-monitoring fixtures (a spec keeps them equal): go:embed cannot reach
// outside this package's directory.
//
//go:embed seedfixtures/k8smon-*.alloy
var seedFixtures embed.FS

// k8smonPipelineItems are the chart-shaped k8s-monitoring pipelines (a
// feature module inlined as a declare plus its instance, then the
// destination), with the fixtures' destination URLs rewritten to the seed's.
// Seeded disabled: they need discovery.kubernetes, so enable them on the
// kind stack.
func k8smonPipelineItems() ([]seedPipelineItem, error) {
	items := []struct{ name, file, role, fromURL, toURL string }{
		{"k8smon-metrics", "k8smon-metrics.alloy", "metrics", "http://prom.example:9090/api/v1/write", seedDestProdPromURL},
		{"k8smon-logs", "k8smon-logs.alloy", "logs", "http://loki.example:3100/loki/api/v1/push", seedDestProdLokiURL},
	}
	out := make([]seedPipelineItem, 0, len(items))
	for _, it := range items {
		b, err := seedFixtures.ReadFile("seedfixtures/" + it.file)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", it.file, err)
		}
		quoted := fmt.Sprintf("%q", it.fromURL)
		if n := strings.Count(string(b), quoted); n != 1 {
			return nil, fmt.Errorf("%s: expected its destination URL %s once, found %d", it.file, quoted, n)
		}
		out = append(out, seedPipelineItem{
			name:     it.name,
			contents: strings.Replace(string(b), quoted, fmt.Sprintf("%q", it.toURL), 1),
			source:   "ui",
			matchers: []string{fmt.Sprintf("cluster=%q", seedClusterPlatformName), fmt.Sprintf("role=%q", it.role)},
			enabled:  false,
		})
	}
	return out, nil
}

// giteaStatusError is a non-2xx Gitea API answer.
type giteaStatusError struct {
	method, path string
	status       int
	body         string
}

func (e *giteaStatusError) Error() string {
	return fmt.Sprintf("%s %s: status %d: %s", e.method, e.path, e.status, e.body)
}

// isGiteaConflict reports a 409, which repository creation answers when the
// repository already exists.
func isGiteaConflict(err error) bool {
	var se *giteaStatusError
	return errors.As(err, &se) && se.status == http.StatusConflict
}
