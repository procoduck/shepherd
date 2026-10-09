package mgmtapi_test

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
	"shepherd/internal/version"
	"shepherd/internal/wizard"
)

// legacySelfMonitoringContents is a self-monitoring pipeline as wizards
// stored it before #260: the writer's URL read from an environment variable
// nothing sets, and no auth.
const legacySelfMonitoringContents = `prometheus.exporter.self "alloy" {}

prometheus.scrape "alloy_self" {
  targets         = prometheus.exporter.self.alloy.targets
  forward_to      = [prometheus.remote_write.metrics.receiver]
  scrape_interval = "60s"
  job_name        = "alloy-self"
}

prometheus.remote_write "metrics" {
  endpoint {
    url = sys.env("SHEPHERD_DEST_MIMIR_URL")
    // auth injected by Shepherd at serve time
  }
}
`

// #262's one-time upgrade path: `shepherd admin rerender-destinations`
// (RerenderLegacyDestinationWriters) converts every wizard pipeline still
// carrying the pre-#260 writer. Red run: the function did not exist.
var _ = Describe("RerenderLegacyDestinationWriters (#262 upgrade path)", Label("integration"), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		org    pgtype.UUID
		reg    *schema.Registry
		v      *validate.Validator
	)

	const url = "https://mimir.example.com/api/v1/push"

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())
		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "legacy-org", DisplayName: "Legacy", AdminGroupID: "legacy-admin"})
		Expect(err).NotTo(HaveOccurred())
		org = o.ID
		_, err = st.Queries.CreateDestination(ctx, sqlc.CreateDestinationParams{
			OrgID: org, Name: "mimir", Type: "prometheus", Url: url, AuthMode: "basic_secret",
			SecretNamespace: "monitoring", SecretName: "mimir-creds", Extra: json.RawMessage(`{}`),
		})
		Expect(err).NotTo(HaveOccurred())
		reg, err = schema.New(schema.Embedded, version.AlloySchemaVersion)
		Expect(err).NotTo(HaveOccurred())
		v = validate.New(&config.ValidateConfig{StabilityLevel: "experimental", Timeout: 10e9})
	})

	AfterEach(func() {
		st.Close()
		cancel()
	})

	legacyPipeline := func(name, dest string, enabled bool) sqlc.Pipeline {
		state, err := json.Marshal(map[string]any{"metrics_dest_name": dest, "logs_enabled": false})
		Expect(err).NotTo(HaveOccurred())
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: org, Name: name, Contents: legacySelfMonitoringContents, Matchers: json.RawMessage(`["role=\"singleton\""]`),
			Enabled: enabled, Source: "wizard", WizardKind: pgtype.Text{String: "self-monitoring", Valid: true},
			WizardState: state, CreatedBy: "seed", UpdatedBy: "seed",
		})
		Expect(err).NotTo(HaveOccurred())
		return p
	}

	It("re-renders a sys.env writer pipeline with the destination's URL and auth, once", func() {
		legacy := legacyPipeline("legacy-self-mon", "mimir", true)
		orphan := legacyPipeline("legacy-orphan", "deleted-long-ago", false)
		cluster, err := st.Queries.UpsertCluster(ctx, "legacy-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "singleton"})
		Expect(err).NotTo(HaveOccurred())

		By("a dry run reports and writes nothing")
		results, err := mgmtapi.RerenderLegacyDestinationWriters(ctx, st, v, reg, slog.Default(), true)
		Expect(err).NotTo(HaveOccurred())
		Expect(results).To(HaveLen(1))
		Expect(results[0].Rerendered).To(ConsistOf("legacy-self-mon"))
		Expect(results[0].Failed).To(ConsistOf(ContainSubstring("legacy-orphan")))
		p, err := st.Queries.GetPipelineByID(ctx, legacy.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Contents).To(Equal(legacySelfMonitoringContents))

		By("the real run converts it through the gate")
		results, err = mgmtapi.RerenderLegacyDestinationWriters(ctx, st, v, reg, slog.Default(), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(results).To(HaveLen(1))
		Expect(results[0].Rerendered).To(ConsistOf("legacy-self-mon"))
		Expect(results[0].Failed).To(ConsistOf(ContainSubstring(`"deleted-long-ago" does not exist`)))
		// M4: each failure says what to do about it, in actions that exist.
		Expect(results[0].Failed).To(ConsistOf(And(
			ContainSubstring(`"legacy-orphan"`),
			ContainSubstring("detach it from the wizard or delete it"),
			ContainSubstring("run this command again"),
		)))

		p, err = st.Queries.GetPipelineByID(ctx, legacy.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Contents).NotTo(ContainSubstring("sys.env"))
		Expect(p.Contents).To(ContainSubstring(`url  = "` + url + `"`))
		Expect(p.Contents).To(ContainSubstring(`remote.kubernetes.secret "metrics_auth"`))
		Expect(p.Enabled).To(BeTrue())
		Expect(p.WizardRenderSha256.String).To(Equal(sha256Hex(p.Contents)), "a wizard write records its render fingerprint")
		revs, err := st.Queries.ListPipelineRevisions(ctx, legacy.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revs).To(HaveLen(1))
		Expect(revs[0].Contents).To(Equal(p.Contents))
		audits, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: org, Column3: "pipeline.rerender", Limit: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(audits).To(HaveLen(1))
		Expect(audits[0].Actor).To(Equal(mgmtapi.LegacyRerenderActor))
		Expect(audits[0].ActorType).To(Equal("system"))
		cache, err := st.Queries.GetServeCache(ctx, collector.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(cache.Dirty).To(BeTrue(), "collectors recompute on their next poll")

		o, err := st.Queries.GetPipelineByID(ctx, orphan.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(o.Contents).To(Equal(legacySelfMonitoringContents), "a pipeline that cannot render is left as it was")

		By("a second run finds only the pipeline it could not render (legacy run)")
		results, err = mgmtapi.RerenderLegacyDestinationWriters(ctx, st, v, reg, slog.Default(), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(results).To(HaveLen(1))
		Expect(results[0].Rerendered).To(BeEmpty())
		revs, err = st.Queries.ListPipelineRevisions(ctx, legacy.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revs).To(HaveLen(1))
	})

	// #261 (maintainer decision Q1): a tenant stored before #261 was never
	// sent. `rerender-destinations --all` is the operator-triggered rollout:
	// it lists the destinations with a tenant and regenerates every wizard
	// pipeline whose render differs, never overwriting a hand edit. Red run:
	// RerenderAllWizardPipelines did not exist.
	It("--all regenerates pre-#261 renders with the stored tenant, lists the tenants, and keeps hand edits", func() {
		state := map[string]any{"metrics_dest_name": "mimir", "logs_enabled": false}
		wiz, err := wizard.Default().Get("self-monitoring")
		Expect(err).NotTo(HaveOccurred())
		pre261, err := wiz.Commit(state, wizard.Destinations{"mimir": {
			Name: "mimir", Type: "prometheus", URL: url, AuthMode: wizard.AuthBasicSecret,
			SecretNamespace: "monitoring", SecretName: "mimir-creds",
		}})
		Expect(err).NotTo(HaveOccurred())
		stateJSON, err := json.Marshal(state)
		Expect(err).NotTo(HaveOccurred())
		create := func(name, contents string, fingerprint pgtype.Text) sqlc.Pipeline {
			p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: org, Name: name, Contents: contents, Matchers: json.RawMessage(`["role=\"singleton\""]`),
				Source: "wizard", WizardKind: pgtype.Text{String: "self-monitoring", Valid: true},
				WizardState: stateJSON, CreatedBy: "seed", UpdatedBy: "seed",
			})
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Pool().Exec(ctx, `UPDATE pipelines SET wizard_render_sha256 = $2 WHERE id = $1`, p.ID, fingerprint)
			Expect(err).NotTo(HaveOccurred())
			return p
		}
		fp := func(s string) pgtype.Text { return pgtype.Text{String: sha256Hex(s), Valid: true} }
		fingerprinted := create("fingerprinted", pre261.Contents, fp(pre261.Contents))
		unfingerprinted := create("unfingerprinted", pre261.Contents, pgtype.Text{})
		edited := create("hand-edited", pre261.Contents+"// my edit\n", fp(pre261.Contents))
		// Stored and, before #261, ignored.
		_, err = st.Pool().Exec(ctx, `UPDATE destinations SET tenant_id = 'acme' WHERE org_id = $1 AND name = 'mimir'`, org)
		Expect(err).NotTo(HaveOccurred())

		By("the dry run lists the tenant and the pipelines, and writes nothing")
		results, err := mgmtapi.RerenderAllWizardPipelines(ctx, st, v, reg, slog.Default(), true)
		Expect(err).NotTo(HaveOccurred())
		Expect(results).To(HaveLen(1))
		Expect(results[0].TenantDestinations).To(ConsistOf("mimir: acme"))
		Expect(results[0].Rerendered).To(ConsistOf("fingerprinted", "unfingerprinted"))
		Expect(results[0].Failed).To(ConsistOf(And(ContainSubstring("hand-edited"), ContainSubstring("edited by hand"))))
		p, err := st.Queries.GetPipelineByID(ctx, fingerprinted.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Contents).To(Equal(pre261.Contents))

		By("--apply sends the tenant from the next poll on")
		results, err = mgmtapi.RerenderAllWizardPipelines(ctx, st, v, reg, slog.Default(), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(results[0].Rerendered).To(ConsistOf("fingerprinted", "unfingerprinted"))
		for _, id := range []pgtype.UUID{fingerprinted.ID, unfingerprinted.ID} {
			p, err := st.Queries.GetPipelineByID(ctx, id)
			Expect(err).NotTo(HaveOccurred())
			Expect(p.Contents).To(ContainSubstring(`"X-Scope-OrgID" = "acme",`))
			Expect(p.WizardRenderSha256.String).To(Equal(sha256Hex(p.Contents)))
		}
		e, err := st.Queries.GetPipelineByID(ctx, edited.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(e.Contents).To(Equal(pre261.Contents+"// my edit\n"), "a hand edit is never overwritten")
		audits, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: org, Column3: "pipeline.rerender", Limit: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(audits).To(HaveLen(2))
		Expect(audits[0].Actor).To(Equal(mgmtapi.LegacyRerenderActor))

		By("a second run has nothing left to regenerate")
		results, err = mgmtapi.RerenderAllWizardPipelines(ctx, st, v, reg, slog.Default(), false)
		Expect(err).NotTo(HaveOccurred())
		Expect(results[0].Rerendered).To(BeEmpty())
	})
})
