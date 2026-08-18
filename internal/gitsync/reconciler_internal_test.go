package gitsync

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/ado"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/testutil"
)

// reconcilerSharedPG is a Postgres container shared across specs in this file.
// It is independent from the (external) gitsync_test package's suite, which
// has no database-backed specs of its own.
var reconcilerSharedPG *testutil.SharedPostgres

var _ = SynchronizedBeforeSuite(func() []byte {
	var err error
	reconcilerSharedPG, err = testutil.StartSharedPostgres(context.Background())
	Expect(err).NotTo(HaveOccurred())
	Expect(store.MigrateUp(context.Background(), reconcilerSharedPG.RootURL)).To(Succeed())
	return nil
}, func(_ []byte) {})

var _ = SynchronizedAfterSuite(func() {}, func() {
	if reconcilerSharedPG != nil {
		Expect(reconcilerSharedPG.Terminate(context.Background())).To(Succeed())
	}
})

var _ = Describe("Reconciler.syncFile", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		r           *Reconciler
		mockServer  *httptest.Server
		adoClient   *ado.Client
		link        sqlc.RepoLink
		pipeline    sqlc.Pipeline
		fileContent string
	)

	// syncFile derives the pipeline name by stripping the leading slash and
	// the .alloy extension, so this must resolve to "myservice" to match the
	// pipeline seeded below.
	const filePath = "/myservice.alloy"

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := reconcilerSharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "gitsync-org", DisplayName: "Gitsync Org", AdminGroupID: "admin-group",
		})
		Expect(err).NotTo(HaveOccurred())
		orgID := org.ID

		cluster, err := st.Queries.UpsertCluster(ctx, "gitsync-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		// Seed a serve_cache row so we can assert it gets flipped dirty.
		_, err = st.Pool().Exec(ctx, `INSERT INTO serve_cache (collector_id, dirty) VALUES ($1, false)`, collector.ID)
		Expect(err).NotTo(HaveOccurred())

		cred, err := st.Queries.CreateADOCredential(ctx, sqlc.CreateADOCredentialParams{
			OrgID: orgID, Name: "gitsync-cred", AdoOrgUrl: "https://dev.azure.com/example",
			EntraTenantID: "tenant", ClientID: "client", ClientSecretEnc: []byte("enc"),
		})
		Expect(err).NotTo(HaveOccurred())

		link, err = st.Queries.CreateRepoLink(ctx, sqlc.CreateRepoLinkParams{
			OrgID: orgID, CollectorID: collector.ID, CredentialID: cred.ID,
			Project: "proj", Repository: "repo", Branch: "main", Path: "/pipelines",
			PollIntervalSeconds: 300,
		})
		Expect(err).NotTo(HaveOccurred())

		fileContent = "// original content"
		mux := http.NewServeMux()
		mux.HandleFunc("/proj/_apis/git/repositories/repo/items", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(fileContent)) //nolint:errcheck // test server
		})
		mockServer = httptest.NewServer(mux)
		adoClient = ado.NewWithHTTPClient(mockServer.URL, mockServer.URL, mockServer.Client())

		r = &Reconciler{store: st, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

		// Seed the pipeline + revision 1, standing in for the outcome of a
		// prior "create" sync (mgmtapi's create path records revision 1 the
		// same way; the reconciler's create branch mirrors CreatePipeline
		// only, so we seed both rows directly here).
		pipeline, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgID, Name: "myservice", Contents: fileContent, Matchers: []byte("[]"),
			Enabled: true, Source: "git", CreatedBy: "gitsync", UpdatedBy: "gitsync",
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
			PipelineID: pipeline.ID, Revision: 1, Contents: fileContent, Matchers: []byte("[]"),
			Enabled: true, ChangedBy: "gitsync", ChangeNote: "git sync initial",
		})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		mockServer.Close()
		st.Close()
		cancel()
	})

	dirtyFlag := func() bool {
		var dirty bool
		Expect(st.Pool().QueryRow(ctx, `SELECT dirty FROM serve_cache WHERE collector_id = $1`, link.CollectorID).Scan(&dirty)).To(Succeed())
		return dirty
	}

	It("updates the pipeline, records revision 2, audits, and dirties the collector's serve cache when content changed", func() {
		fileContent = "// updated content"

		Expect(r.syncFile(ctx, adoClient, link, filePath, "commit-xyz")).To(Succeed())

		updated, err := st.Queries.GetPipelineByID(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Contents).To(Equal(fileContent))
		Expect(updated.UpdatedBy).To(Equal("gitsync"))

		revs, err := st.Queries.ListPipelineRevisions(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revs).To(HaveLen(2))
		Expect(revs[0].Revision).To(Equal(int32(2)))
		Expect(revs[0].Contents).To(Equal(fileContent))
		Expect(revs[0].ChangedBy).To(Equal("gitsync"))
		Expect(revs[0].ChangeNote).To(ContainSubstring("commit-xyz"))

		Expect(dirtyFlag()).To(BeTrue())

		var auditCount int
		Expect(st.Pool().QueryRow(ctx,
			`SELECT count(*) FROM audit_log WHERE action = 'pipeline.update' AND actor = 'gitsync' AND resource_id = $1`,
			pipeline.ID.String(),
		).Scan(&auditCount)).To(Succeed())
		Expect(auditCount).To(Equal(1))
	})

	It("is a no-op when the fetched content is unchanged", func() {
		Expect(r.syncFile(ctx, adoClient, link, filePath, "commit-abc")).To(Succeed())

		updated, err := st.Queries.GetPipelineByID(ctx, pipeline.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Contents).To(Equal(fileContent))

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
})
