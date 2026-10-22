package gitsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"

	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/gitrepo"
	"shepherd/internal/merge"
	"shepherd/internal/metrics"
	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
	"shepherd/internal/version"
	"shepherd/internal/wizard/wizardtest"
)

// giteaFixtureCounter makes repo/token names unique across every spec that
// shares the one Gitea container started in gitsync_suite_test.go.
var giteaFixtureCounter atomic.Int64

func nextGiteaFixtureName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, giteaFixtureCounter.Add(1))
}

var _ = Describe("Reconciler.reconcileLink", Label("integration"), func() {
	const pipelineName = "myservice"

	var (
		ctx           context.Context
		cancel        context.CancelFunc
		st            *store.Store
		enc           *crypto.Encryptor
		r             *Reconciler
		link          sqlc.RepoLink
		collectorID   pgtype.UUID
		repoCloneURL  string
		defaultBranch string
	)

	BeforeEach(func() {
		skipWithoutGitea()

		ctx, cancel = context.WithCancel(context.Background())
		dbURL := reconcilerSharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		key := make([]byte, 32)
		for i := range key {
			key[i] = byte(i)
		}
		enc, err = crypto.NewEncryptor(base64.StdEncoding.EncodeToString(key))
		Expect(err).NotTo(HaveOccurred())

		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "gitsync-org", DisplayName: "Gitsync Org", AdminGroupID: "admin-group",
		})
		Expect(err).NotTo(HaveOccurred())

		cluster, err := st.Queries.UpsertCluster(ctx, "gitsync-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org.ID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		collectorID = collector.ID

		// Seed a serve_cache row so specs can assert it gets flipped dirty.
		_, err = st.Pool().Exec(ctx, `INSERT INTO serve_cache (collector_id, dirty) VALUES ($1, false)`, collector.ID)
		Expect(err).NotTo(HaveOccurred())

		// Real Gitea repo + PAT credential, per docs/git-provider-design.md §4:
		// the reconciler talks real git, authenticated the same way
		// production would for the `pat` kind (Gitea, GitHub, GitLab,
		// Bitbucket).
		repoName := nextGiteaFixtureName("gitsync-repo")
		defaultBranch, repoCloneURL, err = giteaCreateRepo(ctx, repoName)
		Expect(err).NotTo(HaveOccurred())

		token, err := giteaCreateToken(ctx, nextGiteaFixtureName("gitsync-token"))
		Expect(err).NotTo(HaveOccurred())

		tokenEnc, err := enc.Encrypt([]byte(token))
		Expect(err).NotTo(HaveOccurred())

		cred, err := st.Queries.CreateGitCredential(ctx, sqlc.CreateGitCredentialParams{
			OrgID: org.ID, Name: "gitsync-cred", Kind: "pat",
			Username:        pgtype.Text{String: giteaAdminUser, Valid: true},
			ClientSecretEnc: tokenEnc,
			ProviderConfig:  []byte("{}"),
		})
		Expect(err).NotTo(HaveOccurred())

		link, err = st.Queries.CreateRepoLink(ctx, sqlc.CreateRepoLinkParams{
			OrgID: org.ID, CollectorID: collector.ID, CredentialID: cred.ID,
			RepoUrl: repoCloneURL, Branch: defaultBranch, Path: "",
			PollIntervalSeconds: 300,
		})
		Expect(err).NotTo(HaveOccurred())

		// A real validator, same shape production wires via validate.New
		// (server.go): without it, planFile now fails closed (W2-S1) rather
		// than syncing on Stage 1 syntax alone. bin resolves to a real alloy
		// binary, an operator override, or a docker shim around the pinned
		// grafana/alloy image — see wizardtest.AlloyBinary's doc comment for
		// why "" must Fail here rather than silently skip: skipWithoutGitea
		// above already gates the Docker-unavailable case, so reaching this
		// point with no usable binary is a real environment problem, not an
		// expected local-dev gap.
		bin := wizardtest.AlloyBinary()
		if bin == "" {
			Fail("gitsync: no alloy binary and no usable docker image to run Stage 2/3 validation")
		}
		v := validate.New(&config.ValidateConfig{
			AlloyBinary:    bin,
			StabilityLevel: "experimental",
			Timeout:        30 * time.Second,
			Stage3Timeout:  30 * time.Second,
		})

		r = &Reconciler{
			store:      st,
			crypto:     enc,
			validator:  v,
			schema:     embeddedSchema(),
			limits:     gitrepo.Limits{},
			tokenCache: gitrepo.NewTokenCache(),
			logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		}
	})

	AfterEach(func() {
		st.Close()
		cancel()
	})

	dirtyFlag := func() bool {
		var dirty bool
		Expect(st.Pool().QueryRow(ctx, `SELECT dirty FROM serve_cache WHERE collector_id = $1`, collectorID).Scan(&dirty)).To(Succeed())
		return dirty
	}

	push := func(content string) {
		p, err := newPusher(ctx, repoCloneURL, giteaAdminUser, giteaAdminPass)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.commitAndPush(ctx, map[string]string{pipelineName + ".alloy": content}, "sync fixture")).To(Succeed())
	}

	It("syncs a due link from reconcileAll while holding the sync lock, then releases it", func() {
		push("// via the pass")
		l := &fakeLocker{ok: true}
		r.locker = l

		r.reconcileAll(ctx)

		pipeline, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(err).NotTo(HaveOccurred())
		Expect(pipeline.Contents).To(Equal("// via the pass"))
		Expect(l.unlocked).To(Equal(1))
	})

	It("first sync creates the pipeline", func() {
		push("// original content")

		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		pipeline, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(err).NotTo(HaveOccurred())
		Expect(pipeline.Contents).To(Equal("// original content"))
		Expect(pipeline.Source).To(Equal("git"))
		Expect(pipeline.CreatedBy).To(Equal("gitsync"))

		updatedLink, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedLink.SyncStatus.String).To(Equal("ok"))
	})

	It("updates the pipeline, records revision 2, audits, and dirties the collector's serve cache when content changed", func() {
		push("// original content")
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		pipeline, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(err).NotTo(HaveOccurred())
		// The reconciler's create branch records revision 1 itself, so nothing is
		// seeded here — revision 2 below must come from the update path.

		// Re-fetch the link: reconcileLink above already recorded last_commit.
		link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(err).NotTo(HaveOccurred())

		push("// updated content")
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		updated, err := st.Queries.GetPipelineByID(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Contents).To(Equal("// updated content"))
		Expect(updated.UpdatedBy).To(Equal("gitsync"))

		revs, err := st.Queries.ListPipelineRevisions(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revs).To(HaveLen(2))
		Expect(revs[0].Revision).To(Equal(int32(2)))
		Expect(revs[0].Contents).To(Equal("// updated content"))
		Expect(revs[0].ChangedBy).To(Equal("gitsync"))

		Expect(dirtyFlag()).To(BeTrue())

		var auditCount int
		Expect(st.Pool().QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE action = 'pipeline.update' AND actor = 'gitsync' AND resource_id = $1`,
			pipeline.ID.String(),
		).Scan(&auditCount)).To(Succeed())
		Expect(auditCount).To(Equal(1))

		updatedLink, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedLink.SyncStatus.String).To(Equal("ok"))
	})

	It("is a no-op when the fetched commit tip is unchanged", func() {
		push("// original content")
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		pipeline, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(err).NotTo(HaveOccurred())

		link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(link.LastCommit.Valid).To(BeTrue())

		// Second reconcile against the same, unchanged tip.
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		revs, err := st.Queries.ListPipelineRevisions(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		// The first sync creates the pipeline and its revision 1 (mirroring the API's
		// create path); the point of this spec is that an unchanged tip adds NOTHING on
		// top of that, so the count must still be exactly 1.
		Expect(revs).To(HaveLen(1), "an unchanged commit tip must not create another revision")
	})

	It("is a no-op when the fetched content is unchanged despite a new commit", func() {
		push("// original content")
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		pipeline, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(err).NotTo(HaveOccurred())
		// The create branch already recorded revision 1; nothing is seeded here.
		// Re-baseline the serve cache too: creating the pipeline legitimately dirtied
		// it, and this spec is about the unchanged-content sync adding nothing.
		_, err = st.Pool().Exec(ctx, `UPDATE serve_cache SET dirty = false WHERE collector_id = $1`, link.CollectorID)
		Expect(err).NotTo(HaveOccurred())

		link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(err).NotTo(HaveOccurred())

		// A new commit that re-pushes byte-identical content: the tip
		// changes, but the pipeline content does not.
		push("// original content")
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		updated, err := st.Queries.GetPipelineByID(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Contents).To(Equal("// original content"))

		revs, err := st.Queries.ListPipelineRevisions(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revs).To(HaveLen(1), "no new revision should be created for unchanged content")

		Expect(dirtyFlag()).To(BeFalse())

		var auditCount int
		Expect(st.Pool().QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE action = 'pipeline.update' AND resource_id = $1`,
			pipeline.ID.String(),
		).Scan(&auditCount)).To(Succeed())
		Expect(auditCount).To(Equal(0))
	})

	It("marks sync_status=error and leaves the last good pipeline when a file fails validation", func() {
		push("// original content")
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		pipeline, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(err).NotTo(HaveOccurred())

		link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(err).NotTo(HaveOccurred())

		// Re-baseline the serve cache: the initial successful sync legitimately dirties
		// it (a new pipeline changes what the collector must be served). This spec is
		// about the FAILED sync not dirtying it, so start from a clean flag.
		_, err = st.Pool().Exec(ctx, `UPDATE serve_cache SET dirty = false WHERE collector_id = $1`, link.CollectorID)
		Expect(err).NotTo(HaveOccurred())
		Expect(dirtyFlag()).To(BeFalse(), "precondition: cache starts clean")

		push("this is not valid alloy syntax {{{")
		err = r.reconcileLink(ctx, link)
		Expect(err).To(HaveOccurred())

		updatedLink, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(updatedLink.SyncStatus.String).To(Equal("error"))
		Expect(updatedLink.SyncError.String).NotTo(BeEmpty())

		// The last good pipeline content is untouched.
		unchanged, err := st.Queries.GetPipelineByID(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(unchanged.Contents).To(Equal("// original content"))

		Expect(dirtyFlag()).To(BeFalse())
	})

	// W2-S1: r.validator was injected at construction but never referenced
	// inside the per-file sync (now planFile), so every synced file passed on Stage 1 (syntax)
	// alone. This file parses fine (a component block is syntactically
	// valid) but references a component that does not exist, which only
	// Stage 2's real `alloy validate` catches.
	It("rejects a file that parses but fails alloy validate (stage 2)", func() {
		push(`nonexistent.component "x" {
  forward_to = []
}`)

		err := r.reconcileLink(ctx, link)
		Expect(err).To(HaveOccurred())

		_, lookupErr := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(lookupErr).To(HaveOccurred(), "a file that fails stage 2 must never be synced as a pipeline")

		updatedLink, linkErr := st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(linkErr).NotTo(HaveOccurred())
		Expect(updatedLink.SyncStatus.String).To(Equal("error"))
		Expect(updatedLink.SyncError.String).To(ContainSubstring("validation errors"))
	})

	// #233: the real `alloy validate` (this suite runs the pinned binary)
	// accepts a list-of-lists targets wire that Alloy refuses at load. A sync
	// must refuse it the same way a UI save does, through Stage 2's port-shape
	// check, and leave nothing synced.
	It("rejects a file whose targets wire is a list of lists (stage 2 port shape)", func() {
		push(`// #233 reproduction: a list of lists
discovery.kubernetes "pods" {
  role = "pod"
}

prometheus.scrape "demo" {
  targets    = [discovery.kubernetes.pods.targets]
  forward_to = [prometheus.remote_write.demo.receiver]
}

prometheus.remote_write "demo" {
  endpoint {
    url = "https://prom.example.com/api/v1/push"
  }
}
`)

		err := r.reconcileLink(ctx, link)
		Expect(err).To(HaveOccurred())

		_, lookupErr := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(lookupErr).To(HaveOccurred(), "a list-of-lists file must never be synced as a pipeline")

		updatedLink, linkErr := st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(linkErr).NotTo(HaveOccurred())
		Expect(updatedLink.SyncStatus.String).To(Equal("error"))
		Expect(updatedLink.SyncError.String).To(ContainSubstring("validation errors"))
	})

	// W2-S1's stage-3 dry-run: this file is individually valid, but merging
	// it against the linked collector's other enabled pipelines produces a
	// declare-block-name collision — the same failure mode
	// internal/mgmtapi's stage3Check refuses to enable on. gitsync
	// previously never ran this check at all, so a file like this synced
	// "ok" and only surfaced as a merge failure later, on the serve path.
	It("rejects a file that merges into a duplicate block label", func() {
		// "my_service" and "my-service" both sanitize to declare block
		// "pipe_my_service" (merge.SanitizeName lowercases and folds "-" to
		// "_") — pre-seed the former as an already-enabled UI pipeline
		// matching this collector's labels.
		_, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: link.OrgID, Name: "my_service", Contents: "// pre-existing",
			Matchers: json.RawMessage(`["cluster=\"gitsync-cluster\""]`),
			Enabled:  true, Source: "ui",
			WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		p, err := newPusher(ctx, repoCloneURL, giteaAdminUser, giteaAdminPass)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.commitAndPush(ctx, map[string]string{
			"my-service.alloy": "// duplicate label candidate",
		}, "dup label fixture")).To(Succeed())

		err = r.reconcileLink(ctx, link)
		Expect(err).To(HaveOccurred())
		// Caught by planAll's sanitised-name check, before the merged
		// dry run; stage3DryRun's own collision check is covered below.
		Expect(err.Error()).To(ContainSubstring(`my-service.alloy: pipeline "my-service" has the same sanitised name as pipeline "my_service"`))

		_, lookupErr := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: "my-service"})
		Expect(lookupErr).To(HaveOccurred(), "a file whose stage-3 merge dry-run fails must never be synced as a pipeline")

		updatedLink, linkErr := st.Queries.GetRepoLinkByID(ctx, link.ID)
		Expect(linkErr).NotTo(HaveOccurred())
		Expect(updatedLink.SyncStatus.String).To(Equal("error"))
	})

	// B-GITSYNC-PRUNE: docs/spec.md §10's reconciler is to "delete git
	// pipelines of this link whose file disappeared", but reconcileLink only
	// upserted what it fetched, so a removed file's pipeline stayed served
	// to the link's collector for as long as the link existed.
	Describe("a file removed from the repository", func() {
		commit := func(add map[string]string, remove ...string) {
			GinkgoHelper()
			p, err := newPusher(ctx, repoCloneURL, giteaAdminUser, giteaAdminPass)
			Expect(err).NotTo(HaveOccurred())
			Expect(p.commitChangesAndPush(ctx, add, remove, "prune fixture")).To(Succeed())
		}
		// syncOK runs one successful sync and returns the link as it now
		// stands (last_commit recorded), with the serve cache re-baselined
		// clean so a spec can assert what the NEXT sync dirties.
		syncOK := func() {
			GinkgoHelper()
			Expect(r.reconcileLink(ctx, link)).To(Succeed())
			var err error
			link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Pool().Exec(ctx, `UPDATE serve_cache SET dirty = false WHERE collector_id = $1`, collectorID)
			Expect(err).NotTo(HaveOccurred())
		}
		pipelineNamed := func(name string) (sqlc.Pipeline, error) {
			return st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: name})
		}
		deleteAudits := func() []map[string]string {
			GinkgoHelper()
			rows, err := st.Pool().Query(ctx,
				`SELECT resource_id, actor, actor_type, detail FROM audit_log WHERE action = 'pipeline.delete' ORDER BY id`)
			Expect(err).NotTo(HaveOccurred())
			defer rows.Close()
			var out []map[string]string
			for rows.Next() {
				var resourceID, actor, actorType string
				var detail []byte
				Expect(rows.Scan(&resourceID, &actor, &actorType, &detail)).To(Succeed())
				m := map[string]string{}
				Expect(json.Unmarshal(detail, &m)).To(Succeed())
				m["resource_id"], m["actor"], m["actor_type"] = resourceID, actor, actorType
				out = append(out, m)
			}
			Expect(rows.Err()).NotTo(HaveOccurred())
			return out
		}

		// R8 (#360): spec §8 — "never partially apply a commit". Every file
		// of a commit passes, or nothing from it is written: no create, no
		// update, no delete, and the collector keeps its served config.
		Describe("a commit applies atomically (R8)", func() {
			auditsFor := func(action string) []map[string]string {
				GinkgoHelper()
				rows, err := st.Pool().Query(ctx,
					`SELECT resource_id, detail FROM audit_log WHERE action = $1 ORDER BY id`, action)
				Expect(err).NotTo(HaveOccurred())
				defer rows.Close()
				var out []map[string]string
				for rows.Next() {
					var resourceID string
					var detail []byte
					Expect(rows.Scan(&resourceID, &detail)).To(Succeed())
					m := map[string]string{}
					Expect(json.Unmarshal(detail, &m)).To(Succeed())
					m["resource_id"] = resourceID
					out = append(out, m)
				}
				Expect(rows.Err()).NotTo(HaveOccurred())
				return out
			}
			servedContent := func() string {
				GinkgoHelper()
				var content string
				Expect(st.Pool().QueryRow(ctx, `SELECT content FROM serve_cache WHERE collector_id = $1`, collectorID).Scan(&content)).To(Succeed())
				return content
			}
			// expectNothingApplied asserts the state after a refused commit:
			// keep still holds "// keep" at revision 1, gone still exists,
			// none of the commit's new files exists, no revision or audit
			// row was added, and the collector keeps its served config.
			expectNothingApplied := func(newFiles ...string) {
				GinkgoHelper()
				keep, err := pipelineNamed("keep")
				Expect(err).NotTo(HaveOccurred())
				Expect(keep.Contents).To(Equal("// keep"), "an update from a refused commit must not apply")
				revs, err := st.Queries.ListPipelineRevisions(ctx, keep.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(revs).To(HaveLen(1))
				_, err = pipelineNamed("gone")
				Expect(err).NotTo(HaveOccurred(), "a delete from a refused commit must not apply")
				for _, n := range newFiles {
					_, err := pipelineNamed(n)
					Expect(err).To(HaveOccurred(), "a create from a refused commit must not apply: %s", n)
				}
				Expect(auditsFor("pipeline.update")).To(BeEmpty())
				Expect(auditsFor("pipeline.delete")).To(BeEmpty())
				Expect(auditsFor("pipeline.create")).To(HaveLen(2), "only the two setup creates")
				Expect(dirtyFlag()).To(BeFalse())
				Expect(servedContent()).To(Equal("// previously served"), "the collector keeps serving the previous commit")

				l, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(l.SyncStatus.String).To(Equal("error"))
				Expect(l.LastCommit.Valid).To(BeFalse(), "the next poll retries the commit")
				Expect(l.SyncError.String).To(ContainSubstring("nothing from it was applied"))
			}

			BeforeEach(func() {
				commit(map[string]string{"keep.alloy": "// keep", "gone.alloy": "// gone"})
				syncOK()
				_, err := st.Pool().Exec(ctx, `UPDATE serve_cache SET content = '// previously served' WHERE collector_id = $1`, collectorID)
				Expect(err).NotTo(HaveOccurred())
			})

			It("applies nothing from a commit with one invalid file, and names that file", func() {
				commit(map[string]string{
					"keep.alloy": "// keep, changed",
					"new.alloy":  "// new",
					"bad.alloy":  "this is not valid alloy syntax {{{",
				}, "gone.alloy")

				err := r.reconcileLink(ctx, link)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("bad.alloy"))
				Expect(err.Error()).NotTo(ContainSubstring("keep.alloy"), "only the failing file is named")
				expectNothingApplied("new")
				l, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(l.SyncError.String).To(ContainSubstring("bad.alloy: validation errors"))
			})

			It("names at most five failing files, then how many more", func() {
				files := map[string]string{"keep.alloy": "// keep, changed"}
				for i := 1; i <= 7; i++ {
					files[fmt.Sprintf("bad%d.alloy", i)] = "this is not valid alloy syntax {{{"
				}
				commit(files)

				err := r.reconcileLink(ctx, link)
				Expect(err).To(HaveOccurred())
				Expect(strings.Count(err.Error(), "validation errors")).To(Equal(5))
				Expect(err.Error()).To(ContainSubstring("and 2 more"))
				expectNothingApplied("bad1")
			})

			// The collector's role (metrics) does not allow logs. Served, the
			// pipeline would be excluded fail-safe from the only collector a
			// git pipeline targets — synced "ok" and silently doing nothing.
			It("refuses the whole commit when a file's signals are not allowed on the collector's role", func() {
				commit(map[string]string{
					"keep.alloy": "// keep, changed",
					"logs.alloy": `loki.write "out" {
  endpoint {
    url = "http://loki.example.com/loki/api/v1/push"
  }
}
`,
				}, "gone.alloy")

				err := r.reconcileLink(ctx, link)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(MatchRegexp(`logs\.alloy: .*not allowed on role metrics`))
				expectNothingApplied("logs")
			})

			// The dry run models the collector after the WHOLE commit. In a
			// declare-wrapped merge the pipelines meet at their block names,
			// so that is where checking files one at a time goes wrong in
			// both directions.
			It("dry-runs the commit as one merged config, not file by file", func() {
				a := filePlan{name: "x-y", path: "x-y.alloy", contents: "// a"}
				b := filePlan{name: "x_y", path: "x_y.alloy", contents: "// b"}
				for _, alone := range []filePlan{a, b} {
					refusals, err := r.stage3DryRun(ctx, link, []filePlan{alone}, nil)
					Expect(err).NotTo(HaveOccurred())
					Expect(refusals).To(BeEmpty(), "%s passes on its own", alone.path)
				}
				refusals, err := r.stage3DryRun(ctx, link, []filePlan{a, b}, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(refusals).To(HaveLen(1), "together they collide")
				Expect(refusals[0].Error()).To(ContainSubstring("declare-name collision"))

				// The other way round: "GONE" takes the declare label of the
				// "gone" pipeline the same commit removes. Checked against the
				// current rows it collides; with the commit's removals it fits.
				owned, err := st.Queries.ListGitPipelinesByRepoLink(ctx, link.ID)
				Expect(err).NotTo(HaveOccurred())
				var removed []sqlc.ListGitPipelinesByRepoLinkRow
				for _, p := range owned {
					if p.Name == "gone" {
						removed = append(removed, p)
					}
				}
				Expect(removed).To(HaveLen(1))
				rename := filePlan{name: "GONE", path: "GONE.alloy", contents: "// gone"}
				refusals, err = r.stage3DryRun(ctx, link, []filePlan{rename}, nil)
				Expect(err).NotTo(HaveOccurred())
				Expect(refusals).To(HaveLen(1))
				refusals, err = r.stage3DryRun(ctx, link, []filePlan{rename}, removed)
				Expect(err).NotTo(HaveOccurred())
				Expect(refusals).To(BeEmpty())
			})

			It("refuses every commit when no schema registry is configured, rather than dry-running it unenforced", func() {
				r.schema = nil
				commit(map[string]string{"keep.alloy": "// keep, changed"})
				err := r.reconcileLink(ctx, link)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("schema registry not configured"))
				keep, err := pipelineNamed("keep")
				Expect(err).NotTo(HaveOccurred())
				Expect(keep.Contents).To(Equal("// keep"))
			})

			It("applies a commit's creates, updates and deletes together, auditing each with its commit, path and link", func() {
				gone, err := pipelineNamed("gone")
				Expect(err).NotTo(HaveOccurred())
				commit(map[string]string{"keep.alloy": "// keep, changed", "new.alloy": "// new"}, "gone.alloy")

				Expect(r.reconcileLink(ctx, link)).To(Succeed())

				l, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(l.SyncStatus.String).To(Equal("ok"))
				head := l.LastCommit.String
				Expect(head).NotTo(BeEmpty())

				keep, err := pipelineNamed("keep")
				Expect(err).NotTo(HaveOccurred())
				Expect(keep.Contents).To(Equal("// keep, changed"))
				created, err := pipelineNamed("new")
				Expect(err).NotTo(HaveOccurred())
				_, err = st.Queries.GetPipelineByID(ctx, gone.ID)
				Expect(err).To(HaveOccurred())
				Expect(dirtyFlag()).To(BeTrue())

				updates := auditsFor("pipeline.update")
				Expect(updates).To(HaveLen(1))
				Expect(updates[0]).To(Equal(map[string]string{
					"resource_id": keep.ID.String(), "commit": head, "git_path": "keep.alloy", "repo_link_id": link.ID.String(),
				}))
				creates := auditsFor("pipeline.create")
				Expect(creates).To(HaveLen(3), "two from the setup sync, one from this one")
				Expect(creates[2]).To(Equal(map[string]string{
					"resource_id": created.ID.String(), "commit": head, "git_path": "new.alloy", "repo_link_id": link.ID.String(),
				}))
				deletes := auditsFor("pipeline.delete")
				Expect(deletes).To(HaveLen(1))
				Expect(deletes[0]["commit"]).To(Equal(head))
			})
		})

		It("deletes its pipeline, audits the delete, dirties the collector and leaves the kept file's pipeline alone", func() {
			commit(map[string]string{"gone.alloy": "// gone", "kept.alloy": "// kept"})
			syncOK()
			gone, err := pipelineNamed("gone")
			Expect(err).NotTo(HaveOccurred())
			kept, err := pipelineNamed("kept")
			Expect(err).NotTo(HaveOccurred())

			commit(nil, "gone.alloy")
			var recomputed [][2]pgtype.UUID
			r.recompute = func(_ context.Context, orgID, collector pgtype.UUID) {
				recomputed = append(recomputed, [2]pgtype.UUID{orgID, collector})
			}
			Expect(r.reconcileLink(ctx, link)).To(Succeed())

			_, err = st.Queries.GetPipelineByID(ctx, gone.ID)
			Expect(err).To(HaveOccurred(), "the removed file's pipeline must be deleted")
			Expect(recomputed).To(Equal([][2]pgtype.UUID{{link.OrgID, collectorID}}),
				"the link's collector, and only it, is recomputed right after the delete commits")

			stillKept, err := st.Queries.GetPipelineByID(ctx, kept.ID)
			Expect(err).NotTo(HaveOccurred(), "a file still in the repository keeps its pipeline")
			Expect(stillKept.Contents).To(Equal("// kept"))
			revs, err := st.Queries.ListPipelineRevisions(ctx, kept.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(revs).To(HaveLen(1), "an untouched file gains no revision")

			Expect(dirtyFlag()).To(BeTrue(), "the collector that served the pipeline must recompute without it")

			updatedLink, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(updatedLink.SyncStatus.String).To(Equal("ok"))

			audits := deleteAudits()
			Expect(audits).To(HaveLen(1))
			Expect(audits[0]).To(Equal(map[string]string{
				"resource_id":  gone.ID.String(),
				"actor":        "gitsync",
				"actor_type":   "system",
				"name":         "gone",
				"reason":       "gitsync.file_removed",
				"description":  "file removed from repository",
				"repo_link_id": link.ID.String(),
				"git_path":     "gone.alloy",
				"commit":       updatedLink.LastCommit.String,
			}))
		})

		It("deletes nothing when the fetch fails", func() {
			commit(map[string]string{"gone.alloy": "// gone", "kept.alloy": "// kept"})
			syncOK()
			gone, err := pipelineNamed("gone")
			Expect(err).NotTo(HaveOccurred())

			// gone.alloy is removed, but the tree now holds three files and
			// the fetch is capped at two: Files fails, so the reconciler
			// never sees a complete listing and must not act on a partial one.
			commit(map[string]string{"c.alloy": "// c", "d.alloy": "// d"}, "gone.alloy")
			r.limits = gitrepo.Limits{MaxFiles: 2}
			Expect(r.reconcileLink(ctx, link)).NotTo(Succeed())

			_, err = st.Queries.GetPipelineByID(ctx, gone.ID)
			Expect(err).NotTo(HaveOccurred(), "a failed fetch must not delete anything")
			Expect(deleteAudits()).To(BeEmpty())
			Expect(dirtyFlag()).To(BeFalse())
		})

		It("deletes nothing when another file in the same commit fails validation", func() {
			commit(map[string]string{"gone.alloy": "// gone", "kept.alloy": "// kept"})
			syncOK()
			gone, err := pipelineNamed("gone")
			Expect(err).NotTo(HaveOccurred())

			commit(map[string]string{"kept.alloy": "this is not valid alloy syntax {{{"}, "gone.alloy")
			Expect(r.reconcileLink(ctx, link)).NotTo(Succeed())

			_, err = st.Queries.GetPipelineByID(ctx, gone.ID)
			Expect(err).NotTo(HaveOccurred(), "a sync that failed must not prune")
			Expect(deleteAudits()).To(BeEmpty())
		})

		It("refuses to prune every pipeline when the synced tree has no .alloy files, and records a warning", func() {
			commit(map[string]string{"only.alloy": "// only"})
			syncOK()
			only, err := pipelineNamed("only")
			Expect(err).NotTo(HaveOccurred())

			commit(nil, "only.alloy")
			Expect(r.reconcileLink(ctx, link)).To(Succeed())

			_, err = st.Queries.GetPipelineByID(ctx, only.ID)
			Expect(err).NotTo(HaveOccurred(), "an empty tree is more likely a wrong path or branch than a deliberate wipe")
			Expect(deleteAudits()).To(BeEmpty())
			Expect(dirtyFlag()).To(BeFalse())

			updatedLink, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(updatedLink.SyncStatus.String).To(Equal("warning"))
			Expect(updatedLink.SyncError.String).To(ContainSubstring("no .alloy files"))
			Expect(updatedLink.LastCommit.Valid).To(BeFalse(),
				"the warning must be re-evaluated on the next poll, not cleared by the unchanged-commit shortcut")
		})

		It("keeps the pipeline of a file that is still there but now skipped as oversized", func() {
			commit(map[string]string{"big.alloy": "// small for now", "kept.alloy": "// kept"})
			syncOK()
			big, err := pipelineNamed("big")
			Expect(err).NotTo(HaveOccurred())

			// gitrepo skips (does not fail on) a file over max_file_bytes,
			// so it is missing from the files the sync read — but not from
			// the repository.
			commit(map[string]string{"big.alloy": "// " + strings.Repeat("x", 512)})
			r.limits = gitrepo.Limits{MaxFileBytes: 256}
			Expect(r.reconcileLink(ctx, link)).To(Succeed())

			stillThere, err := st.Queries.GetPipelineByID(ctx, big.ID)
			Expect(err).NotTo(HaveOccurred(), "an oversized file has not been removed")
			Expect(stillThere.Contents).To(Equal("// small for now"))
			Expect(deleteAudits()).To(BeEmpty())
		})

		It("treats a rename as a delete of the old pipeline and a create of the new one", func() {
			commit(map[string]string{"before.alloy": "// renamed"})
			syncOK()
			before, err := pipelineNamed("before")
			Expect(err).NotTo(HaveOccurred())

			commit(map[string]string{"after.alloy": "// renamed"}, "before.alloy")
			Expect(r.reconcileLink(ctx, link)).To(Succeed())

			_, err = st.Queries.GetPipelineByID(ctx, before.ID)
			Expect(err).To(HaveOccurred(), "the old name's pipeline must go")
			after, err := pipelineNamed("after")
			Expect(err).NotTo(HaveOccurred())
			Expect(after.Source).To(Equal("git"))
			Expect(after.GitPath.String).To(Equal("after.alloy"))
			Expect(after.Contents).To(Equal("// renamed"))

			audits := deleteAudits()
			Expect(audits).To(HaveLen(1))
			Expect(audits[0]["resource_id"]).To(Equal(before.ID.String()))
			Expect(audits[0]["git_path"]).To(Equal("before.alloy"))
			Expect(dirtyFlag()).To(BeTrue())
		})

		// A rename between two names that sanitise to the same declare label
		// (merge.SanitizeName) used to be stuck for good: the Stage-3 dry run
		// merged the new file beside the old, still-present pipeline, failed
		// on the duplicate label, and a failed sync never prunes.
		It("completes a rename whose old and new names sanitise to the same label", func() {
			commit(map[string]string{"My-App.alloy": "// app"})
			syncOK()
			old, err := pipelineNamed("My-App")
			Expect(err).NotTo(HaveOccurred())

			commit(map[string]string{"my_app.alloy": "// app"}, "My-App.alloy")
			Expect(r.reconcileLink(ctx, link)).To(Succeed())

			_, err = st.Queries.GetPipelineByID(ctx, old.ID)
			Expect(err).To(HaveOccurred(), "the old name's pipeline must go")
			renamed, err := pipelineNamed("my_app")
			Expect(err).NotTo(HaveOccurred())
			Expect(renamed.GitPath.String).To(Equal("my_app.alloy"))

			updatedLink, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(updatedLink.SyncStatus.String).To(Equal("ok"))
		})

		It("holds such a rename back, with the rest of the commit, while another file fails validation", func() {
			commit(map[string]string{"My-App.alloy": "// app", "fine.alloy": "// fine"})
			syncOK()
			old, err := pipelineNamed("My-App")
			Expect(err).NotTo(HaveOccurred())

			commit(map[string]string{
				"my_app.alloy": "// app",
				"fine.alloy":   "// fine, changed",
				"bad.alloy":    "this is not valid alloy syntax {{{",
			}, "My-App.alloy")
			err = r.reconcileLink(ctx, link)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("bad.alloy: validation errors"))
			Expect(err.Error()).NotTo(ContainSubstring("my_app.alloy"), "the rename itself is fine")

			_, err = st.Queries.GetPipelineByID(ctx, old.ID)
			Expect(err).NotTo(HaveOccurred(), "a refused commit never prunes")
			_, err = pipelineNamed("my_app")
			Expect(err).To(HaveOccurred(), "nor creates")
			fine, err := pipelineNamed("fine")
			Expect(err).NotTo(HaveOccurred())
			Expect(fine.Contents).To(Equal("// fine"), "nor updates")
		})

		// Two names that sanitise alike cannot both exist in an org (unique
		// index pipelines_org_sanitized_name_key). Caught before anything is
		// written, the clash names its own files and refuses the commit
		// (R8: every file passes or nothing applies).
		Describe("a sanitised-name clash", func() {
			It("names both new files that clash with each other, and applies nothing", func() {
				commit(map[string]string{"fine.alloy": "// fine", "gone.alloy": "// gone"})
				syncOK()
				gone, err := pipelineNamed("gone")
				Expect(err).NotTo(HaveOccurred())

				commit(map[string]string{
					"fine.alloy": "// fine, changed",
					"a-b.alloy":  "// a-b",
					"a_b.alloy":  "// a_b",
				}, "gone.alloy")
				err = r.reconcileLink(ctx, link)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(`a-b.alloy: pipeline "a-b" has the same sanitised name as a_b.alloy`))
				Expect(err.Error()).To(ContainSubstring(`a_b.alloy: pipeline "a_b" has the same sanitised name as a-b.alloy`))

				Expect(err.Error()).NotTo(ContainSubstring("fine.alloy"))

				fine, err := pipelineNamed("fine")
				Expect(err).NotTo(HaveOccurred())
				Expect(fine.Contents).To(Equal("// fine"), "nothing from a refused commit syncs")
				_, err = pipelineNamed("a-b")
				Expect(err).To(HaveOccurred())
				_, err = pipelineNamed("a_b")
				Expect(err).To(HaveOccurred())
				_, err = st.Queries.GetPipelineByID(ctx, gone.ID)
				Expect(err).NotTo(HaveOccurred(), "a refused commit does not prune")

				updatedLink, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(updatedLink.SyncStatus.String).To(Equal("error"))
			})

			It("refuses a commit whose new file clashes with a pipeline this collector is not served", func() {
				// A UI pipeline matching another cluster: the Stage-3 dry run
				// for this collector never sees it.
				_, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
					OrgID: link.OrgID, Name: "ui-thing", Contents: "// ui",
					Matchers: json.RawMessage(`["cluster=\"somewhere-else\""]`),
					Enabled:  true, Source: "ui",
					WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
				})
				Expect(err).NotTo(HaveOccurred())
				commit(map[string]string{"fine.alloy": "// fine"})
				syncOK()

				commit(map[string]string{"fine.alloy": "// fine, changed", "ui_thing.alloy": "// git"})
				err = r.reconcileLink(ctx, link)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(`ui_thing.alloy: pipeline "ui_thing" has the same sanitised name as pipeline "ui-thing"`))

				fine, err := pipelineNamed("fine")
				Expect(err).NotTo(HaveOccurred())
				Expect(fine.Contents).To(Equal("// fine"))
				_, err = pipelineNamed("ui_thing")
				Expect(err).To(HaveOccurred())
			})
		})

		// A file that fails transiently (a validator timeout, a store blip)
		// refuses the whole commit — the update with the delete — and leaves
		// last_commit unset, so the next poll fetches the commit again and
		// applies all of it.
		It("refuses a commit whose file failed transiently, and applies all of it on the next poll", func() {
			commit(map[string]string{"fine.alloy": "// fine", "gone.alloy": "// gone"})
			syncOK()
			gone, err := pipelineNamed("gone")
			Expect(err).NotTo(HaveOccurred())

			commit(map[string]string{"fine.alloy": "// fine, changed"}, "gone.alloy")
			r.planFileFunc = func(context.Context, sqlc.RepoLink, gitrepo.File) (*filePlan, error) {
				return nil, errors.New("alloy validate: timed out")
			}
			err = r.reconcileLink(ctx, link)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("fine.alloy: alloy validate: timed out"))

			link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(link.SyncStatus.String).To(Equal("error"))
			Expect(link.LastCommit.Valid).To(BeFalse(), "the next poll must fetch this commit again")
			_, err = st.Queries.GetPipelineByID(ctx, gone.ID)
			Expect(err).NotTo(HaveOccurred())
			fine, err := pipelineNamed("fine")
			Expect(err).NotTo(HaveOccurred())
			Expect(fine.Contents).To(Equal("// fine"))

			r.planFileFunc = nil
			Expect(r.reconcileLink(ctx, link)).To(Succeed())
			_, err = st.Queries.GetPipelineByID(ctx, gone.ID)
			Expect(err).To(HaveOccurred(), "the retry prunes")
			fine, err = pipelineNamed("fine")
			Expect(err).NotTo(HaveOccurred())
			Expect(fine.Contents).To(Equal("// fine, changed"), "and updates, in the same sync")
		})

		Describe("the mass-deletion threshold", func() {
			sixFiles := func() map[string]string {
				m := map[string]string{}
				for _, n := range []string{"p1", "p2", "p3", "p4", "p5", "p6"} {
					m[n+".alloy"] = "// " + n
				}
				return m
			}

			It("deletes nothing and warns when one sync would remove more than half of the link's pipelines and more than three", func() {
				commit(sixFiles())
				syncOK()

				commit(nil, "p1.alloy", "p2.alloy", "p3.alloy", "p4.alloy")
				recomputes := 0
				r.recompute = func(context.Context, pgtype.UUID, pgtype.UUID) { recomputes++ }
				Expect(r.reconcileLink(ctx, link)).To(Succeed())
				Expect(recomputes).To(BeZero(), "a sync that applied nothing recomputes nothing")

				for _, n := range []string{"p1", "p2", "p3", "p4", "p5", "p6"} {
					_, err := pipelineNamed(n)
					Expect(err).NotTo(HaveOccurred(), "%s must survive a suspicious mass delete", n)
				}
				Expect(deleteAudits()).To(BeEmpty())
				Expect(dirtyFlag()).To(BeFalse())

				updatedLink, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(updatedLink.SyncStatus.String).To(Equal("warning"))
				Expect(updatedLink.SyncError.String).To(ContainSubstring("would delete 4 of 6 pipelines"))
				Expect(updatedLink.SyncError.String).To(ContainSubstring("delete and re-create the repository link"))
				Expect(updatedLink.SyncError.String).To(ContainSubstring("restore some of the removed files"))
				Expect(updatedLink.LastCommit.Valid).To(BeFalse())
			})

			// R8 (#360): a tripped guard holds back the whole commit — its
			// updates and creates as well as its deletes. Applying the
			// updates alone would serve a commit nobody wrote.
			It("applies nothing from a guarded commit: no update, create or delete", func() {
				commit(sixFiles())
				syncOK()
				p5, err := pipelineNamed("p5")
				Expect(err).NotTo(HaveOccurred())

				commit(map[string]string{"p5.alloy": "// p5, changed", "p7.alloy": "// p7"},
					"p1.alloy", "p2.alloy", "p3.alloy", "p4.alloy")
				Expect(r.reconcileLink(ctx, link)).To(Succeed())

				unchanged, err := st.Queries.GetPipelineByID(ctx, p5.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(unchanged.Contents).To(Equal("// p5"), "an update waits with the held-back deletes")
				_, err = pipelineNamed("p7")
				Expect(err).To(HaveOccurred(), "a new file waits with the held-back deletes")
				for _, n := range []string{"p1", "p2", "p3", "p4"} {
					_, err := pipelineNamed(n)
					Expect(err).NotTo(HaveOccurred(), "%s's delete is held back", n)
				}
				Expect(deleteAudits()).To(BeEmpty())
				Expect(dirtyFlag()).To(BeFalse())

				updatedLink, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(updatedLink.SyncStatus.String).To(Equal("warning"))
				Expect(updatedLink.SyncError.String).To(ContainSubstring("would delete 4 of 6 pipelines"))
				Expect(updatedLink.SyncError.String).To(ContainSubstring("nothing from it was applied"))
				Expect(updatedLink.LastCommit.Valid).To(BeFalse())
			})

			It("keeps serving a moved directory's old pipelines and creates none of the new ones", func() {
				moved := map[string]string{}
				var old []string
				for _, n := range []string{"p1", "p2", "p3", "p4", "p5", "p6"} {
					moved["b/"+n+".alloy"] = "// " + n
					old = append(old, "a/"+n+".alloy")
				}
				original := map[string]string{}
				for _, p := range old {
					original[p] = "// " + p[2:len(p)-6]
				}
				commit(original)
				syncOK()

				commit(moved, old...)
				Expect(r.reconcileLink(ctx, link)).To(Succeed())

				for _, n := range []string{"p1", "p2", "p3", "p4", "p5", "p6"} {
					_, err := pipelineNamed("a/" + n)
					Expect(err).NotTo(HaveOccurred(), "a/%s keeps serving", n)
					_, err = pipelineNamed("b/" + n)
					Expect(err).To(HaveOccurred(), "b/%s must not be served beside a/%s", n, n)
				}
				Expect(deleteAudits()).To(BeEmpty())
				Expect(dirtyFlag()).To(BeFalse())

				updatedLink, err := st.Queries.GetRepoLinkByID(ctx, link.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(updatedLink.SyncStatus.String).To(Equal("warning"))
				Expect(updatedLink.SyncError.String).To(ContainSubstring("would delete 6 of 6 pipelines"))
				Expect(updatedLink.SyncError.String).To(ContainSubstring("nothing from it was applied"))
			})

			// A link that has never finished "ok" has no baseline: it may
			// delete at most three per sync, and says why in its own words.
			// A sync writes only when it finishes "ok" (R8), so a link owns
			// pipelines without ever having finished "ok" only when something
			// else wrote them (an older replica during a rolling update); the
			// spec clears last_ok_at to stand for that.
			It("tells a link that has never completed a sync why it may delete at most three", func() {
				commit(map[string]string{"q1.alloy": "// q1", "q2.alloy": "// q2", "q3.alloy": "// q3", "q4.alloy": "// q4", "keep.alloy": "// keep"})
				syncOK()
				_, err := st.Pool().Exec(ctx, `UPDATE repo_links SET last_ok_at = NULL, sync_status = 'error', last_commit = NULL WHERE id = $1`, link.ID)
				Expect(err).NotTo(HaveOccurred())
				link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(link.LastOkAt.Valid).To(BeFalse())

				commit(nil, "q1.alloy", "q2.alloy", "q3.alloy", "q4.alloy")
				Expect(r.reconcileLink(ctx, link)).To(Succeed())

				for _, n := range []string{"q1", "q2", "q3", "q4"} {
					_, err := pipelineNamed(n)
					Expect(err).NotTo(HaveOccurred())
				}
				link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(link.SyncStatus.String).To(Equal("warning"))
				Expect(link.SyncError.String).To(ContainSubstring("this link has not completed a sync yet"))
				Expect(link.SyncError.String).To(ContainSubstring("at most 3"))
				Expect(link.SyncError.String).To(ContainSubstring("delete and re-create the repository link"))
			})

			// The guard's denominator must not grow from the guarded commit's
			// own creates: compared with 8 pipelines (6 + the 2 it added) on
			// the next poll, 4 removals are no longer "more than half", and
			// the guard released itself after one poll.
			It("keeps holding on later polls when the guarded commit also adds files, until files are restored", func() {
				commit(sixFiles())
				syncOK()

				commit(map[string]string{"p7.alloy": "// p7", "p8.alloy": "// p8"},
					"p1.alloy", "p2.alloy", "p3.alloy", "p4.alloy")
				for poll := 1; poll <= 2; poll++ {
					Expect(r.reconcileLink(ctx, link)).To(Succeed())
					var err error
					link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
					Expect(err).NotTo(HaveOccurred())
					Expect(link.SyncStatus.String).To(Equal("warning"), "poll %d", poll)
					Expect(link.SyncError.String).To(ContainSubstring("would delete 4 of 6 pipelines"), "poll %d", poll)
					Expect(deleteAudits()).To(BeEmpty(), "poll %d: the deletes stay held back", poll)
				}
				_, err := pipelineNamed("p7")
				Expect(err).To(HaveOccurred(), "the guarded commit's creates are held back too")

				// Restoring two files brings the removal under the threshold.
				commit(map[string]string{"p1.alloy": "// p1", "p2.alloy": "// p2"})
				Expect(r.reconcileLink(ctx, link)).To(Succeed())
				link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(link.SyncStatus.String).To(Equal("ok"))
				for _, n := range []string{"p3", "p4"} {
					_, err := pipelineNamed(n)
					Expect(err).To(HaveOccurred(), "%s is pruned once under the threshold", n)
				}
				for _, n := range []string{"p1", "p2", "p5", "p6", "p7", "p8"} {
					_, err := pipelineNamed(n)
					Expect(err).NotTo(HaveOccurred(), n)
				}
				Expect(deleteAudits()).To(HaveLen(2))
			})

			It("deletes one of six", func() {
				commit(sixFiles())
				syncOK()

				commit(nil, "p1.alloy")
				Expect(r.reconcileLink(ctx, link)).To(Succeed())

				_, err := pipelineNamed("p1")
				Expect(err).To(HaveOccurred())
				Expect(deleteAudits()).To(HaveLen(1))
			})

			It("deletes three of four: at most three is never a mass deletion", func() {
				commit(map[string]string{"q1.alloy": "// q1", "q2.alloy": "// q2", "q3.alloy": "// q3", "q4.alloy": "// q4"})
				syncOK()

				commit(nil, "q1.alloy", "q2.alloy", "q3.alloy")
				Expect(r.reconcileLink(ctx, link)).To(Succeed())

				for _, n := range []string{"q1", "q2", "q3"} {
					_, err := pipelineNamed(n)
					Expect(err).To(HaveOccurred(), "%s must be deleted", n)
				}
				_, err := pipelineNamed("q4")
				Expect(err).NotTo(HaveOccurred())
				Expect(deleteAudits()).To(HaveLen(3))
			})
		})

		// The prune matches on the pipeline name derived from the file path
		// (the identity the unique sanitised-name index enforces), not on
		// the stored git_path text: if the stored path format ever drifted,
		// a git_path match would read every file as removed.
		It("keeps a pipeline whose stored git_path no longer matches the path text but whose file is still there", func() {
			commit(map[string]string{"kept.alloy": "// kept", "other.alloy": "// other"})
			syncOK()
			kept, err := pipelineNamed("kept")
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Pool().Exec(ctx, `UPDATE pipelines SET git_path = './kept.alloy' WHERE id = $1`, kept.ID)
			Expect(err).NotTo(HaveOccurred())

			commit(map[string]string{"other.alloy": "// other, changed"})
			Expect(r.reconcileLink(ctx, link)).To(Succeed())

			_, err = st.Queries.GetPipelineByID(ctx, kept.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(deleteAudits()).To(BeEmpty())
		})

		It("ignores files that are not .alloy files", func() {
			commit(map[string]string{"kept.alloy": "// kept", "notes.txt": "notes", "docs/README.md": "# readme"})
			syncOK()

			commit(map[string]string{"CHANGES.md": "changes"}, "notes.txt", "docs/README.md")
			Expect(r.reconcileLink(ctx, link)).To(Succeed())

			_, err := pipelineNamed("kept")
			Expect(err).NotTo(HaveOccurred())
			Expect(deleteAudits()).To(BeEmpty())
		})

		DescribeTable("prunes under the link's path",
			func(linkPath string, files map[string]string, remove, gone, kept string) {
				_, err := st.Pool().Exec(ctx, `UPDATE repo_links SET path = $1 WHERE id = $2`, linkPath, link.ID)
				Expect(err).NotTo(HaveOccurred())
				link, err = st.Queries.GetRepoLinkByID(ctx, link.ID)
				Expect(err).NotTo(HaveOccurred())

				commit(files)
				syncOK()
				gonePipe, err := pipelineNamed(gone)
				Expect(err).NotTo(HaveOccurred())
				keptPipe, err := pipelineNamed(kept)
				Expect(err).NotTo(HaveOccurred())

				commit(nil, remove)
				Expect(r.reconcileLink(ctx, link)).To(Succeed())

				_, err = st.Queries.GetPipelineByID(ctx, gonePipe.ID)
				Expect(err).To(HaveOccurred())
				_, err = st.Queries.GetPipelineByID(ctx, keptPipe.ID)
				Expect(err).NotTo(HaveOccurred())
				Expect(deleteAudits()).To(HaveLen(1))
			},
			Entry("path \"/\"", "/",
				map[string]string{"a.alloy": "// a", "b.alloy": "// b"}, "a.alloy", "a", "b"),
			Entry("a subdirectory, with a file outside it", "configs",
				map[string]string{"configs/a.alloy": "// a", "configs/b.alloy": "// b", "elsewhere/c.alloy": "// c"},
				"configs/a.alloy", "configs/a", "configs/b"),
		)

		It("ends cleanly, writing nothing, when the link is deleted while its sync runs", func() {
			commit(map[string]string{"orphan.alloy": "// orphan"})
			// The link row goes after reconcileAll listed it: reconcileLink
			// still holds the stale row.
			_, err := st.Pool().Exec(ctx, `DELETE FROM repo_links WHERE id = $1`, link.ID)
			Expect(err).NotTo(HaveOccurred())

			outcome, err := r.syncLink(ctx, link)
			Expect(err).NotTo(HaveOccurred())
			Expect(outcome).To(Equal(syncOutcome("link_gone")), "counted apart from a real ok sync")

			_, err = pipelineNamed("orphan")
			Expect(err).To(HaveOccurred())
		})

		It("counts a guarded sync as a warning, not ok or error", func() {
			commit(map[string]string{"only.alloy": "// only"})
			syncOK()
			commit(nil, "only.alloy")
			_, err := st.Pool().Exec(ctx, `UPDATE repo_links SET last_synced_at = NULL WHERE id = $1`, link.ID)
			Expect(err).NotTo(HaveOccurred())

			before := promtestutil.ToFloat64(metrics.SyncTotal.WithLabelValues("warning"))
			r.reconcileAll(ctx)
			Expect(promtestutil.ToFloat64(metrics.SyncTotal.WithLabelValues("warning"))).To(Equal(before + 1))
		})
	})
})

// A credential value must never reach repo_links.sync_error (shown to every
// org reader) or a log line, whatever a transport error chooses to echo
// (CodeQL go/clear-text-logging). Red run: making redactSecrets return err
// unchanged fails the first two specs.
type fakeLocker struct {
	ok       bool
	err      error
	unlocked int
}

func (l *fakeLocker) TryLock(context.Context) (func(), bool, error) {
	if l.err != nil || !l.ok {
		return nil, false, l.err
	}
	return func() { l.unlocked++ }, true, nil
}

// #210: the chart runs two replicas, and each ran every sync pass. A replica
// that does not hold the lock must not touch the store at all — the nil store
// here panics on the first query.
var _ = Describe("Reconciler.reconcileAll with a Locker", func() {
	newRec := func(l Locker) *Reconciler {
		return &Reconciler{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), locker: l}
	}

	It("skips the pass while another replica holds the lock", func() {
		Expect(func() { newRec(&fakeLocker{ok: false}).reconcileAll(context.Background()) }).NotTo(Panic())
	})

	It("skips the pass when the lock cannot be taken", func() {
		Expect(func() {
			newRec(&fakeLocker{err: errors.New("db down")}).reconcileAll(context.Background())
		}).NotTo(Panic())
	})
})

// embeddedSchema is the registry production wires (server.go loads the same
// embedded, pinned artifact).
func embeddedSchema() *schema.Registry {
	GinkgoHelper()
	reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
	Expect(err).NotTo(HaveOccurred())
	return reg
}

// A merged config's Stage 1/2 diagnostics name the file whose declare block
// they fall in, so a refused commit's sync_error points at a file.
var _ = Describe("attributeDiagnostics", func() {
	It("names a commit file by path, another pipeline by name, and the rest as the merged config", func() {
		spans := []merge.BlockSpan{
			{PipelineName: "mine", DeclareLine: 5, EndLine: 9},
			{PipelineName: "theirs", DeclareLine: 11, EndLine: 14},
		}
		got := attributeDiagnostics([]validate.Diagnostic{
			{Line: 6, Col: 3, Message: "bad attr"},
			{Line: 12, Col: 1, Message: "unknown component"},
			{Line: 7, Col: 2, Message: "another"},
			{Line: 30, Col: 1, Message: "in the baseline"},
		}, spans, map[string]string{"mine": "dir/mine.alloy"}, "c1/metrics")
		msgs := make([]string, len(got))
		for i, e := range got {
			msgs[i] = e.Error()
		}
		Expect(msgs).To(Equal([]string{
			"dir/mine.alloy: merged config invalid: 6:3 bad attr; 7:2 another",
			`pipeline "theirs" in the merged config for collector c1/metrics: merged config invalid: 12:1 unknown component`,
			"the merged config for collector c1/metrics: merged config invalid: 30:1 in the baseline",
		}))
	})
})

var _ = Describe("secret scrubbing of recorded sync errors", func() {
	It("replaces every occurrence of a secret and drops the original wrapping", func() {
		err := fmt.Errorf("fetching files: %w", errors.New("https://svc:hunter2@git.example.com: 401 (hunter2)"))
		got := redactSecrets(err, []string{"hunter2"})
		Expect(got.Error()).To(Equal("fetching files: https://svc:[REDACTED]@git.example.com: 401 ([REDACTED])"))
		Expect(errors.Unwrap(got)).To(BeNil(), "a scrubbed error must not still wrap the unscrubbed one")
	})

	It("scrubs the values of every auth kind the reconciler builds", func() {
		for _, tc := range []struct {
			auth   gitrepo.Auth
			secret string
		}{
			{gitrepo.BasicAuth{Username: "u", Password: "pw-basic"}, "pw-basic"},
			{gitrepo.PATAuth{Username: "u", Token: "tok-pat"}, "tok-pat"},
			{gitrepo.SSHAuth{PrivateKeyPEM: []byte("KEYMATERIAL"), Passphrase: "pp-ssh"}, "pp-ssh"},
			{gitrepo.SSHAuth{PrivateKeyPEM: []byte("KEYMATERIAL"), Passphrase: "pp-ssh"}, "KEYMATERIAL"},
			{gitrepo.AdoSPAuth{ClientSecret: "cs-ado"}, "cs-ado"},
		} {
			scrub := newSecretScrubber(tc.auth)
			got := scrub(errors.New("boom " + tc.secret + " end"))
			Expect(got.Error()).To(Equal("boom [REDACTED] end"), "%T must scrub %q", tc.auth, tc.secret)
		}
	})

	It("leaves an error alone when no secret appears in it, wrapping intact", func() {
		inner := errors.New("dial tcp: connection refused")
		err := fmt.Errorf("getting latest commit: %w", inner)
		got := redactSecrets(err, []string{"hunter2", ""})
		Expect(got).To(BeIdenticalTo(err))
		Expect(errors.Is(got, inner)).To(BeTrue())
	})

	It("never treats an empty secret as a match", func() {
		err := errors.New("plain text")
		Expect(redactSecrets(err, []string{""}).Error()).To(Equal("plain text"))
		Expect(redactSecrets(nil, []string{"x"})).To(BeNil())
	})
})
