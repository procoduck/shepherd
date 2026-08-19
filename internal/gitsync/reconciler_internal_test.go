package gitsync

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/gitrepo"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
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

		r = &Reconciler{
			store:      st,
			crypto:     enc,
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
		// Seed revision 1, standing in for the outcome of a prior "create"
		// sync (mgmtapi's create path records revision 1 the same way; the
		// reconciler's create branch mirrors CreatePipeline only, so it is
		// seeded directly here).
		_, err = st.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
			PipelineID: pipeline.ID, Revision: 1, Contents: pipeline.Contents, Matchers: pipeline.Matchers,
			Enabled: true, ChangedBy: "gitsync", ChangeNote: "git sync initial",
		})
		Expect(err).NotTo(HaveOccurred())

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
		Expect(revs).To(BeEmpty(), "no revision should be created when the commit tip hasn't changed")

		Expect(dirtyFlag()).To(BeFalse())
	})

	It("is a no-op when the fetched content is unchanged despite a new commit", func() {
		push("// original content")
		Expect(r.reconcileLink(ctx, link)).To(Succeed())

		pipeline, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: link.OrgID, Name: pipelineName})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
			PipelineID: pipeline.ID, Revision: 1, Contents: pipeline.Contents, Matchers: pipeline.Matchers,
			Enabled: true, ChangedBy: "gitsync", ChangeNote: "git sync initial",
		})
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
})
