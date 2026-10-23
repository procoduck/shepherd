package cli

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/testutil"
)

// devUsersPG is a Postgres container shared across every spec in this
// Describe block (started once, in SynchronizedBeforeSuite below). It is a
// distinct name from any shared-Postgres var in another _test.go file in this
// package, since Ginkgo runs every *_test.go in "package cli" as one binary
// and one suite (TestDevSeed's RunSpecs in dev_test.go).
var devUsersPG *testutil.SharedPostgres

var _ = SynchronizedBeforeSuite(func() []byte {
	var err error
	devUsersPG, err = testutil.StartSharedPostgres(context.Background())
	Expect(err).NotTo(HaveOccurred())
	Expect(store.MigrateUp(context.Background(), devUsersPG.RootURL)).To(Succeed())
	return nil
}, func(_ []byte) {})

var _ = SynchronizedAfterSuite(func() {}, func() {
	if devUsersPG != nil {
		Expect(devUsersPG.Terminate(context.Background())).To(Succeed())
	}
})

var _ = Describe("seedLocalUsers", Label("integration"), func() {
	var (
		ctx context.Context
		st  *store.Store
	)

	BeforeEach(func() {
		ctx = context.Background()
		dbURL := devUsersPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		st.Close()
	})

	seedPlatformOrg := func() pgtype.UUID {
		org, err := upsertSeedOrg(ctx, st, seedOrgPlatformID, "platform-org", "Platform Engineering",
			seedPlatformAdminGroupID, seedPlatformViewerGroupID, "platform-org")
		Expect(err).NotTo(HaveOccurred())
		return org.ID
	}

	It("creates admin (via bootstrap), editor and viewer with the right platform-org roles, idempotently across two runs", func() {
		orgID := seedPlatformOrg()

		// The users table is empty going in — this is the ordering hazard:
		// seedLocalUsers must create admin itself (deterministically, via
		// BootstrapAdmin) rather than assume something else already has.
		n, err := st.Queries.CountUsers(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(int64(0)))

		Expect(seedLocalUsers(ctx, st, orgID)).To(Succeed())

		admin, err := st.Queries.GetUserByLogin(ctx, "admin")
		Expect(err).NotTo(HaveOccurred(), "seedLocalUsers must create the bootstrap admin, not just editor/viewer")
		Expect(admin.IsAppAdmin).To(BeTrue())

		editor, err := st.Queries.GetUserByLogin(ctx, seedEditorLogin)
		Expect(err).NotTo(HaveOccurred())
		ok, err := auth.VerifyPassword(editor.PasswordHash, seedEditorPassword)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		editorRole, err := st.Queries.GetOrgMemberRole(ctx, sqlc.GetOrgMemberRoleParams{OrgID: orgID, UserID: editor.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(editorRole).To(Equal(auth.RoleEditor.String()))

		viewer, err := st.Queries.GetUserByLogin(ctx, seedViewerLogin)
		Expect(err).NotTo(HaveOccurred())
		ok, err = auth.VerifyPassword(viewer.PasswordHash, seedViewerPassword)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		viewerRole, err := st.Queries.GetOrgMemberRole(ctx, sqlc.GetOrgMemberRoleParams{OrgID: orgID, UserID: viewer.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(viewerRole).To(Equal(auth.RoleViewer.String()))

		// Seeding again must not error, duplicate users, or change roles —
		// `dev seed` is run on every dev stack restart.
		Expect(seedLocalUsers(ctx, st, orgID)).To(Succeed())

		n, err = st.Queries.CountUsers(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(n).To(Equal(int64(3)), "re-seeding must not create duplicate users")

		editorAgain, err := st.Queries.GetUserByLogin(ctx, seedEditorLogin)
		Expect(err).NotTo(HaveOccurred())
		Expect(editorAgain.ID).To(Equal(editor.ID), "re-seeding must reuse the existing editor row, not create a second one")
		editorRoleAgain, err := st.Queries.GetOrgMemberRole(ctx, sqlc.GetOrgMemberRoleParams{OrgID: orgID, UserID: editor.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(editorRoleAgain).To(Equal(auth.RoleEditor.String()))
	})
})

// Re-seeding a running stack: the seed writes pipelines straight to the
// database, so it must mark the org's served configs stale itself, or a
// collector keeps serving the config from before the pipeline existed.
var _ = Describe("seedPipelines", Label("integration"), func() {
	It("marks the org's served configs stale when it creates a pipeline", func() {
		ctx := context.Background()
		st, err := store.New(ctx, &config.DatabaseConfig{URL: devUsersPG.IsolatedDB(ctx, GinkgoTB()), MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		defer st.Close()

		org, err := upsertSeedOrg(ctx, st, seedOrgPlatformID, "platform-org", "Platform Engineering",
			seedPlatformAdminGroupID, seedPlatformViewerGroupID, "platform-org")
		Expect(err).NotTo(HaveOccurred())
		cluster, err := st.Queries.UpsertCluster(ctx, "seed-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org.ID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Pool().Exec(ctx, `INSERT INTO serve_cache (collector_id, dirty) VALUES ($1, false)`, collector.ID)
		Expect(err).NotTo(HaveOccurred())

		Expect(seedPipelines(ctx, st, org.ID, []seedPipelineItem{{
			name: "reseeded", contents: "prometheus.exporter.self \"x\" { }\n", source: "ui",
			matchers: []string{`role="metrics"`}, enabled: true,
		}})).To(Succeed())

		var dirty bool
		Expect(st.Pool().QueryRow(ctx, `SELECT dirty FROM serve_cache WHERE collector_id = $1`, collector.ID).Scan(&dirty)).To(Succeed())
		Expect(dirty).To(BeTrue())
	})
})

// The compose stacks' token bootstrap (e2e/docker-compose.e2e.yaml): the org
// first, then a fixed-secret token bound to it — never an unbound token,
// which the agent API refuses and Alloy exits on.
var _ = Describe("dev ensure-org, then token create --org", Label("integration"), func() {
	var (
		ctx   context.Context
		st    *store.Store
		dbURL string
	)

	BeforeEach(func() {
		ctx = context.Background()
		dbURL = devUsersPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		st.Close()
	})

	It("creates the org once and returns it unchanged on a re-run", func() {
		first, created, err := ensureOrg(ctx, st, "e2e-org", "E2E Org", "g-admins")
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())

		again, created, err := ensureOrg(ctx, st, "e2e-org", "Other", "g-other")
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(again.ID).To(Equal(first.ID))
		Expect(again.DisplayName).To(Equal("E2E Org"), "an existing org is left untouched")
	})

	It("mints the fixed-secret dev token bound to the org, with may_claim and its allowlists", func() {
		_, _, err := ensureOrg(ctx, st, "e2e-org", "E2E Org", "g-admins")
		Expect(err).NotTo(HaveOccurred())

		tokenName, tokenSecret, tokenID = "", "", ""
		resetTokenFlags(tokenCreateCmd)
		GinkgoT().Setenv("SHEPHERD_DEV_ALLOW_STATIC_TOKEN", "true")
		GinkgoT().Setenv("SHEPHERD_DATABASE_URL", dbURL)
		GinkgoT().Setenv("SHEPHERD_SECURITY_ENCRYPTION_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
		rootCmd.SilenceUsage = true
		rootCmd.SilenceErrors = true
		rootCmd.SetArgs([]string{
			"token", "create", "--name", "e2e", "--org", "e2e-org",
			"--clusters", "e2e-cluster", "--roles", "metrics",
			"--id", validFixedUUID, "--secret", "e2e-static-secret-32byteslong1234",
		})
		Expect(rootCmd.Execute()).To(Succeed())

		var id pgtype.UUID
		Expect(id.Scan(validFixedUUID)).To(Succeed())
		tok, err := st.Queries.GetAgentTokenByID(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		org, err := st.Queries.GetOrgByName(ctx, "e2e-org")
		Expect(err).NotTo(HaveOccurred())
		Expect(tok.OrgID).To(Equal(org.ID))
		Expect(tok.MayClaim).To(BeTrue(), "a CLI-minted token is operator-trusted")
		Expect(string(tok.Clusters)).To(MatchJSON(`["e2e-cluster"]`))
		Expect(string(tok.Roles)).To(MatchJSON(`["metrics"]`))
	})
})
