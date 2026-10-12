package mgmtapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/merge"
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

	findingsFor := func(id pgtype.UUID) []map[string]any {
		body := map[string]any{"org_id": orgID.String(), "id": id.String()}
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
	findings := func() []map[string]any { return findingsFor(collID) }

	// M2 (2026-10-08 walkthrough): a pipeline whose matchers select a collector
	// but whose signals the collector's role does not allow is excluded from
	// its served config. That used to leave only a comment in the served
	// config — this tab said "in sync". It must name the pipeline and why.
	It("reports a pipeline excluded by role enforcement, naming its signals and the role", func() {
		cluster, err := st.Queries.UpsertCluster(ctx, "recon-cluster")
		Expect(err).NotTo(HaveOccurred())
		metricsColl, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		// keep-me (loki.write — logs) matches cluster="recon-cluster", so it
		// matches the metrics collector too, and role metrics refuses logs.
		fs := findingsFor(metricsColl.ID)
		Expect(fs).To(HaveLen(1))
		Expect(fs[0]["kind"]).To(Equal("role_signal_excluded"))
		Expect(fs[0]["pipelineName"]).To(Equal(pipeName))
		sources, ok := fs[0]["sources"].([]any)
		Expect(ok).To(BeTrue())
		Expect(sources).To(Equal([]any{"declared", "served"}))
		Expect(fs[0]["summary"]).To(And(
			ContainSubstring(`"keep-me"`),
			ContainSubstring("excluded"),
			ContainSubstring("(logs)"),
			ContainSubstring("role metrics"),
		))

		// The singleton collector it IS served to has nothing to report.
		Expect(findings()).To(BeEmpty())
	})

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

	// #139/#144: with allow_label_matching on, a pipeline can reach a collector
	// through an admin label rather than cluster/role. The desired served set
	// must be built with the same gated label set the serve paths use, or a
	// label-matched pipeline the collector is correctly running reads as
	// "no longer served" drift.
	Context("with a pipeline matched by an admin label", func() {
		BeforeEach(func() {
			_, err := st.Queries.SetCollectorLabel(ctx, sqlc.SetCollectorLabelParams{
				ID: collID, LabelKey: "team", LabelValue: "payments",
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: orgID, Name: "by-label",
				Contents: "loki.write \"dest\" {\n  endpoint {\n    url = \"http://example.com/loki/api/v1/push\"\n  }\n}\n",
				Matchers: json.RawMessage(`["team=\"payments\""]`),
				Enabled:  true, Source: "ui", CreatedBy: "test", UpdatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())
		})

		setLabelMatching := func(on bool) {
			o, err := st.Queries.GetOrgByID(ctx, orgID)
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Queries.UpdateOrg(ctx, sqlc.UpdateOrgParams{
				ID: orgID, DisplayName: o.DisplayName, AdminGroupID: o.AdminGroupID,
				ReaderGroupID: o.ReaderGroupID, EditorGroupID: o.EditorGroupID,
				AllowExperimentalComponents: o.AllowExperimentalComponents,
				AllowLabelMatching:          on,
				AllowLocalAttributeMatching: o.AllowLocalAttributeMatching,
			})
			Expect(err).NotTo(HaveOccurred())
		}

		It("does not report it as drift when the org has label matching on", func() {
			setLabelMatching(true)
			observe("pipe_keep_me", true)
			observe("pipe_by_label", true)
			Expect(findings()).To(BeEmpty())
		})

		It("reports it as drift when the org has label matching off", func() {
			setLabelMatching(false)
			observe("pipe_keep_me", true)
			observe("pipe_by_label", true)
			fs := findings()
			Expect(fs).To(HaveLen(1))
			Expect(fs[0]["controllerPath"]).To(Equal("pipe_by_label"))
		})
	})

	// #297: a collector that rejected its served config reads FAILED, and its
	// error points at a line of the MERGED config. The tab used to say "the
	// served and observed state below cannot agree" and list nothing; it now
	// carries a served_config_rejected finding naming the pipeline whose
	// declare block holds that line.
	Describe("a collector that rejected its served config (#297)", func() {
		const instanceID = "recon-inst-1"
		pipeContents := "loki.write \"dest\" {\n  endpoint {\n    url = \"http://example.com/loki/api/v1/push\"\n  }\n}\n"

		// serve stores an assembled config for the collector, the way the
		// serve path would, and returns it.
		serve := func() merge.AssembleResult {
			cl := merge.CollectorLabels{CollectorID: collID.String(), Labels: map[string]string{"cluster": "recon-cluster", "role": "singleton"}}
			res, err := merge.Assemble(collID.String(), "recon-cluster/singleton", cl, []merge.Pipeline{{
				ID: "p", Name: pipeName, Contents: pipeContents, Source: "ui", Revision: 1,
				Matchers: []string{`cluster="recon-cluster"`},
			}}, "test")
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
				CollectorID: collID, Content: res.Content, Hash: res.Hash, DirtySeq: 0,
			})
			Expect(err).NotTo(HaveOccurred())
			return res
		}
		report := func(status, errMsg, hash string) {
			_, err := st.Queries.UpsertCollectorInstance(ctx, sqlc.UpsertCollectorInstanceParams{
				ID: instanceID, CollectorID: collID, Name: "recon-node", LocalAttributes: json.RawMessage(`{}`),
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.UpdateInstanceStatus(ctx, sqlc.UpdateInstanceStatusParams{
				ID:                 instanceID,
				RemoteConfigStatus: pgtype.Text{String: status, Valid: true},
				RemoteConfigError:  pgtype.Text{String: errMsg, Valid: errMsg != ""},
				StatusHash:         pgtype.Text{String: hash, Valid: hash != ""},
			})).To(Succeed())
		}
		rejections := func() []map[string]any {
			var out []map[string]any
			for _, f := range findings() {
				if f["kind"] == "served_config_rejected" {
					out = append(out, f)
				}
			}
			return out
		}

		It("names the pipeline and the line of its own text the error points at", func() {
			res := serve()
			// The url attribute is line 3 of the pipeline's own text.
			merged := res.Spans[0].DeclareLine + 3
			report("FAILED", fmt.Sprintf("%d:5: Failed to build component: decoding configuration", merged), res.Hash)

			fs := rejections()
			Expect(fs).To(HaveLen(1))
			Expect(fs[0]["pipelineName"]).To(Equal(pipeName))
			Expect(fs[0]["sources"]).To(Equal([]any{"served", "observed"}))
			Expect(fs[0]["summary"]).To(ContainSubstring(
				fmt.Sprintf(`rejected in pipeline %q (line 3 of its text, line %d of the merged config)`, pipeName, merged)))
		})

		It("does not attribute a rejection of an earlier config to the current pipelines", func() {
			res := serve()
			report("FAILED", fmt.Sprintf("%d:5: Failed to build component", res.Spans[0].DeclareLine+3), "an-older-hash")
			fs := rejections()
			Expect(fs).To(HaveLen(1))
			Expect(fs[0]).NotTo(HaveKey("pipelineName"))
			Expect(fs[0]["summary"]).To(ContainSubstring("earlier served config"))
		})

		// Review of #327: Alloy re-sends a status only when (status, message)
		// changes, so after an unrelated served change a collector still
		// failing the same way never re-reports. The finding stays, says it is
		// still failing, and does not guess at the moved line.
		It("keeps reporting a collector still failing after an unrelated served change, without attributing it", func() {
			res := serve()
			report("FAILED", fmt.Sprintf("%d:5: Failed to build component", res.Spans[0].DeclareLine+3), res.Hash)
			Expect(rejections()[0]["pipelineName"]).To(Equal(pipeName))

			// An unrelated change re-serves different bytes; no new status arrives.
			Expect(st.Queries.MarkServeCacheDirty(ctx, collID)).To(Succeed())
			cache, err := st.Queries.GetServeCache(ctx, collID)
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
				CollectorID: collID, Content: "// header moved\n" + res.Content, Hash: "h-after-unrelated-change",
				DirtySeq: cache.DirtySeq,
			})
			Expect(err).NotTo(HaveOccurred())

			fs := rejections()
			Expect(fs).To(HaveLen(1))
			Expect(fs[0]).NotTo(HaveKey("pipelineName"))
			Expect(fs[0]["summary"]).To(And(
				ContainSubstring("still failing"),
				ContainSubstring("recorded against an earlier served config"),
				Not(ContainSubstring("has not reported")),
			))
		})

		It("reports nothing once the collector loads the config", func() {
			res := serve()
			report("APPLIED", "", res.Hash)
			Expect(rejections()).To(BeEmpty())
		})
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
