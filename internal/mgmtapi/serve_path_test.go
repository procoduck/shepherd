package mgmtapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"

	"shepherd/internal/auth"
	"shepherd/internal/beacon"
	"shepherd/internal/config"
	"shepherd/internal/metrics"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/serve"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// R4 (#358), the eager half: a recompute that fails on the eager path is
// logged at Error, counted in shepherd_serve_recompute_failures_total (it was
// not before) and written to audit_log once per (collector, dirty_seq) — the
// same serve.RecordFailure the lazy path uses.
//
// Red run, executed: before the fix recomputeOrgCaches only logged a Warn, so
// the metric assertion failed (+0, want +2) and no audit row existed.
var _ = Describe("eager recompute failure (R4)", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		orgID       pgtype.UUID
		collectorID pgtype.UUID
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: sharedPG.IsolatedDB(ctx, GinkgoTB()), MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "fail-org", DisplayName: "Fail Org", AdminGroupID: "admin-group"})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID
		cluster, err := st.Queries.UpsertCluster(ctx, "fail-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: o.ID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		collectorID = collector.ID

		// Two enabled pipelines whose names sanitize to the same declare
		// block: merge.Assemble refuses the pair, so serve.ComputeServed
		// genuinely fails for this collector (an unparsable pipeline would
		// not — role enforcement excludes it fail-safe instead). The
		// database refuses that pair (unique index pipelines_org_sanitized_name_key), so
		// this spec's isolated database drops the index to reach
		// merge.Assemble's own refusal behind it.
		_, err = st.Pool().Exec(ctx, `DROP INDEX pipelines_org_sanitized_name_key`)
		Expect(err).NotTo(HaveOccurred())
		for _, name := range []string{"collide-a", "collide a"} {
			_, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: o.ID, Name: name, Contents: "// " + name,
				Matchers: json.RawMessage(`["cluster=\"fail-cluster\""]`), Enabled: true, Source: "ui",
				WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(st.Queries.MarkServeCacheDirtyByOrg(ctx, o.ID)).To(Succeed())
	})

	AfterEach(func() {
		st.Close()
		cancel()
	})

	type auditRow struct {
		Actor, ActorType, OrgID, ResourceType, ResourceID string
		Detail                                            map[string]any
	}
	auditRows := func() []auditRow {
		rows, err := st.Pool().Query(ctx, `SELECT actor, actor_type, org_id::text, resource_type, resource_id, detail
			FROM audit_log WHERE action = $1 ORDER BY id`, serve.AuditActionRecomputeFailed)
		Expect(err).NotTo(HaveOccurred())
		defer rows.Close()
		var out []auditRow
		for rows.Next() {
			var r auditRow
			var raw []byte
			Expect(rows.Scan(&r.Actor, &r.ActorType, &r.OrgID, &r.ResourceType, &r.ResourceID, &raw)).To(Succeed())
			Expect(json.Unmarshal(raw, &r.Detail)).To(Succeed())
			out = append(out, r)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return out
	}

	It("counts every failure, keeps the previous entry, and audits once per dirty generation", func() {
		v := validate.New(&config.ValidateConfig{StabilityLevel: "experimental", Timeout: 10e9})
		ps := mgmtapi.NewPipelineService(st, v, embeddedSchema(), slog.Default())
		before := promtestutil.ToFloat64(metrics.ServeRecomputeFailuresTotal)

		ps.RecomputeOrgCaches(ctx, orgID)
		ps.RecomputeOrgCaches(ctx, orgID)

		Expect(promtestutil.ToFloat64(metrics.ServeRecomputeFailuresTotal)).To(Equal(before+2),
			"the eager path must count each failed recompute")
		row, err := st.Queries.GetServeCache(ctx, collectorID)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.Dirty).To(BeTrue(), "a failed recompute writes nothing: the previous entry stays")

		got := auditRows()
		Expect(got).To(HaveLen(1), "two failures of one generation are one audit row")
		Expect(got[0].Actor).To(Equal("serve"))
		Expect(got[0].ActorType).To(Equal("system"))
		Expect(got[0].OrgID).To(Equal(orgID.String()))
		Expect(got[0].ResourceType).To(Equal("collector"))
		Expect(got[0].ResourceID).To(Equal(collectorID.String()))
		Expect(got[0].Detail).To(HaveKeyWithValue("path", "eager"))
		Expect(got[0].Detail).To(HaveKeyWithValue("dirty_seq", BeNumerically("==", row.DirtySeq)))
		Expect(got[0].Detail).To(HaveKeyWithValue("error", ContainSubstring("declare-name collision")))

		// A new mark is a new generation: its failure is reported again.
		Expect(st.Queries.MarkServeCacheDirty(ctx, collectorID)).To(Succeed())
		ps.RecomputeOrgCaches(ctx, orgID)
		got = auditRows()
		Expect(got).To(HaveLen(2))
		Expect(got[1].Detail).To(HaveKeyWithValue("dirty_seq", BeNumerically("==", row.DirtySeq+1)))
	})
})

// R8 (#360): internal/gitsync's eager recompute (server.go wires it to
// RecomputeCollectorCache) refreshes the synced link's collector only. It
// used to recompute the whole org, writing every other dirty collector's
// served config as a side effect of a sync that could not have changed it.
var _ = Describe("RecomputeCollectorCache (the git sync hook)", Label("integration"), func() {
	It("writes the named collector's served config and leaves a dirty bystander in the same org alone", func() {
		ctx := context.Background()
		st, err := store.New(ctx, &config.DatabaseConfig{URL: sharedPG.IsolatedDB(ctx, GinkgoTB()), MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		defer st.Close()

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "hook-org", DisplayName: "Hook Org", AdminGroupID: "admin-group"})
		Expect(err).NotTo(HaveOccurred())
		collector := func(clusterName string) pgtype.UUID {
			cluster, err := st.Queries.UpsertCluster(ctx, clusterName)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: o.ID})).To(Succeed())
			c, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
			Expect(err).NotTo(HaveOccurred())
			_, err = st.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
				CollectorID: c.ID, Content: "// old " + clusterName, Hash: "old-" + clusterName, Format: serve.Format,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.MarkServeCacheDirty(ctx, c.ID)).To(Succeed())
			return c.ID
		}
		// The bystander's cluster sorts first: an org-wide recompute reaches it first.
		bystander := collector("hook-a-bystander")
		target := collector("hook-b-target")
		_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: o.ID, Name: "everywhere", Contents: "// everywhere",
			Matchers: json.RawMessage(`["role=\"metrics\""]`), Enabled: true, Source: "ui",
			WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		v := validate.New(&config.ValidateConfig{StabilityLevel: "experimental", Timeout: 10e9})
		ps := mgmtapi.NewPipelineService(st, v, embeddedSchema(), slog.Default())
		ps.RecomputeCollectorCache(ctx, o.ID, target)

		got, err := st.Queries.GetServeCache(ctx, target)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Dirty).To(BeFalse())
		Expect(got.Content).To(ContainSubstring("everywhere"))

		other, err := st.Queries.GetServeCache(ctx, bystander)
		Expect(err).NotTo(HaveOccurred())
		Expect(other.Dirty).To(BeTrue(), "the bystander still waits for its own poll")
		Expect(other.Hash).To(Equal("old-hook-a-bystander"))
	})
})

// R4 (#358), the Stage 2 half: Stage 3's dry run must hand Stage 2 (PortShapes
// and `alloy validate`) the bytes a collector is actually served — D6's
// baseline pipeline included — not merge.Assemble's output alone. A stand-in
// alloy binary records every file it is asked to validate.
//
// Red run, executed: with the baseline left out of stage3CheckSet's
// serve.Deps — what validating merge.Assemble's output amounted to — the
// merged config Stage 2 saw carried the "Generated by Shepherd" header but no
// beacon remote_write, and the final assertion failed.
var _ = Describe("Stage 3 validates the served bytes (R4)", Label("integration"), func() {
	const baseURL = "https://shepherd.example.test"

	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		server      *httptest.Server
		orgID       string
		adminCookie *http.Cookie
		captureDir  string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: sharedPG.IsolatedDB(ctx, GinkgoTB()), MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "stage2-org", DisplayName: "Stage2 Org", AdminGroupID: "admin-group"})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()
		cluster, err := st.Queries.UpsertCluster(ctx, "stage2-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: o.ID})).To(Succeed())
		_, err = st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		captureDir = GinkgoT().TempDir()
		fakeAlloy := filepath.Join(GinkgoT().TempDir(), "alloy")
		script := "#!/bin/sh\nfor a; do f=$a; done\ncp \"$f\" \"$(mktemp " + captureDir + "/validated.XXXXXX)\"\nexit 0\n"
		Expect(os.WriteFile(fakeAlloy, []byte(script), 0o700)).To(Succeed())

		cfg := &config.Config{
			Auth:     config.AuthConfig{InsecureCookies: true},
			Server:   config.ServerConfig{BaseURL: baseURL},
			Validate: config.ValidateConfig{AlloyBinary: fakeAlloy, StabilityLevel: "experimental", Timeout: 10e9},
		}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
		adminCookie = newAppAdminSession(ctx, st)
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	It("runs Stage 2 on the merged config with the D6 baseline appended", func() {
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/CreatePipeline", adminCookie, map[string]any{
			"orgId": orgID, "name": "stage2-probe", "contents": "// stage2-probe",
			"matchers": []string{`cluster="stage2-cluster"`},
		})
		var created map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&created)).To(Succeed())
		Expect(resp.Body.Close()).To(Succeed())
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		enable := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/EnablePipeline", adminCookie, map[string]any{
			"orgId": orgID, "id": created["id"],
		})
		Expect(enable.Body.Close()).To(Succeed())
		Expect(enable.StatusCode).To(Equal(http.StatusOK))

		files, err := filepath.Glob(filepath.Join(captureDir, "validated.*"))
		Expect(err).NotTo(HaveOccurred())
		var merged []string
		for _, f := range files {
			b, err := os.ReadFile(f)
			Expect(err).NotTo(HaveOccurred())
			if strings.Contains(string(b), "// Generated by Shepherd") {
				merged = append(merged, string(b))
			}
		}
		Expect(merged).NotTo(BeEmpty(), "Stage 3 never ran Stage 2 on a merged config")
		for _, m := range merged {
			Expect(m).To(ContainSubstring("stage2-probe"))
			Expect(m).To(ContainSubstring(baseURL+beacon.WritePath),
				"Stage 2 validated a merged config without the D6 baseline the collector is served:\n%s", m)
		}
	})
})
