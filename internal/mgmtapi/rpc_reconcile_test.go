package mgmtapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// Connect-path coverage for FleetService.GetReconciliation (#110): the
// served<->observed drift the reconciliation surface reports, over the real
// wiring. reconcileServed replicates merge match+enforcement; the observed set
// comes from beacon_inventory attributed by the collector id the baseline
// stamps (see internal/reconcile for the pure comparison's own tests).
var _ = Describe("shepherd.mgmt.v1.FleetService/GetReconciliation", Label("integration"), func() {
	var (
		ctx      context.Context
		cancel   context.CancelFunc
		st       *store.Store
		server   *httptest.Server
		orgID    pgtype.UUID
		reader   *http.Cookie
		collID   pgtype.UUID
		pipeName = "keep-me"
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "recon-org", DisplayName: "Recon Org",
			AdminGroupID: "recon-admin", ReaderGroupID: pgtype.Text{String: "recon-reader", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID

		cluster, err := st.Queries.UpsertCluster(ctx, "recon-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgID})).To(Succeed())
		// singleton is Unrestricted, so a matched pipeline is served regardless of
		// its signals — keeps this test about the observed<->served join, not
		// signal classification.
		coll, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "singleton"})
		Expect(err).NotTo(HaveOccurred())
		collID = coll.ID

		// An enabled pipeline that matches the collector's cluster → served as
		// pipe_keep_me.
		_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgID, Name: pipeName,
			Contents: "loki.write \"dest\" {\n  endpoint {\n    url = \"http://example.com/loki/api/v1/push\"\n  }\n}\n",
			Matchers: json.RawMessage(`["cluster=\"recon-cluster\""]`),
			Enabled:  true, Source: "ui", CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
		reader = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "recon-reader")}
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	observe := func(component string, healthy bool) {
		_, err := st.Queries.UpsertBeaconComponent(ctx, sqlc.UpsertBeaconComponentParams{
			Principal: "recon-token", InstanceLabel: "10.0.0.1:12345",
			ComponentName: component, Healthy: healthy, CollectorID: collID,
		})
		Expect(err).NotTo(HaveOccurred())
	}

	findings := func() []map[string]any {
		body := map[string]any{"org_id": orgID.String(), "id": collID.String()}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.FleetService/GetReconciliation", reader, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var out map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&out)).To(Succeed())
		raw, ok := out["findings"].([]any)
		if !ok {
			raw = nil
		}
		fs := make([]map[string]any, 0, len(raw))
		for _, r := range raw {
			if m, ok := r.(map[string]any); ok {
				fs = append(fs, m)
			}
		}
		return fs
	}

	It("reports no findings when the observed pipeline is the served one", func() {
		observe("pipe_keep_me", true)
		Expect(findings()).To(BeEmpty())
	})

	It("flags a managed pipeline running that is no longer served", func() {
		observe("pipe_keep_me", true) // served — no finding
		observe("pipe_ghost", true)   // not served — finding
		fs := findings()
		Expect(fs).To(HaveLen(1))
		Expect(fs[0]["kind"]).To(Equal("unserved_component_observed"))
		Expect(fs[0]["controllerPath"]).To(Equal("pipe_ghost"))
		sources, ok := fs[0]["sources"].([]any)
		Expect(ok).To(BeTrue())
		Expect(sources).To(ConsistOf("served", "observed"))
	})

	It("ignores root-level (non-managed) observed components", func() {
		// A BYO / baseline component at the root controller path is out of scope
		// and must not produce a finding.
		observe("prometheus.scrape.self", true)
		Expect(findings()).To(BeEmpty())
	})

	It("does not attribute another collector's beacon rows", func() {
		// A row with no collector id (a pre-#110 baseline) must not appear as
		// observed for this collector.
		_, err := st.Queries.UpsertBeaconComponent(ctx, sqlc.UpsertBeaconComponentParams{
			Principal: "recon-token", InstanceLabel: "10.0.0.9:12345",
			ComponentName: "pipe_ghost", Healthy: true, CollectorID: pgtype.UUID{}, // NULL
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(findings()).To(BeEmpty())
	})
})
