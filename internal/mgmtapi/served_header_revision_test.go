package mgmtapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	collectorv1 "shepherd/gen/collector/v1"
	"shepherd/internal/agentapi"
	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/schema"
	"shepherd/internal/serve"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/version"
)

// #296: the served header listed every pipeline as "(rev 0)" because no
// caller set merge.Pipeline.Revision. It now carries the pipeline's current
// revision — which puts the revision into the served BYTES, and therefore
// into the hash every collector compares on each poll. Two paths compute
// those bytes: this package's eager recompute after a pipeline write, and
// internal/agentapi's lazy recompute when a poll finds serve_cache dirty. If
// the two ever disagreed on the revision, every poll that landed on the
// other path would move the hash and the fleet would reload for nothing
// (#213's failure mode, through a different line of the header).
//
// So this spec drives both REAL paths over one database: the pipeline is
// written through the PipelineService RPCs (the eager path runs as a side
// effect), then the same collector is recomputed by an agentapi.Service wired
// as internal/server wires it, and the two serve_cache results must be
// byte-identical.
//
// Red runs, executed: before the fix the eager path served
// "//   - rev-probe (rev 0)" and the first Eventually below timed out; with the
// fix in place but the lazy path's revisions zeroed, the byte-identical
// assertion fails on "(rev 0)" vs "(rev 4)".
var _ = Describe("served header revision (#296)", Label("integration"), func() {
	const (
		baseURL     = "https://shepherd.example.test"
		clusterName = "rev-cluster"
		contents    = `prometheus.scrape "a" {
  targets    = []
  forward_to = []
}`
	)

	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		cfg         *config.Config
		server      *httptest.Server
		orgID       string
		adminCookie *http.Cookie
		collectorID pgtype.UUID
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "rev-org", DisplayName: "Rev Org", AdminGroupID: "admin-group",
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		cluster, err := st.Queries.UpsertCluster(ctx, clusterName)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: o.ID})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		collectorID = collector.ID

		// BaseURL set so both paths also append the D6 baseline: parity is
		// asserted over the whole served config, not merely the header.
		cfg = &config.Config{
			Auth:     config.AuthConfig{InsecureCookies: true},
			Server:   config.ServerConfig{BaseURL: baseURL},
			Validate: config.ValidateConfig{AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10e9},
		}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
		adminCookie = newAppAdminSession(ctx, st)
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
		}
		if st != nil {
			st.Close()
		}
		cancel()
	})

	call := func(procedure string, body map[string]any) map[string]any {
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/"+procedure, adminCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK), "%s should succeed", procedure)
		var out map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&out)).To(Succeed())
		return out
	}

	served := func() string {
		var got string
		if err := st.Pool().QueryRow(ctx,
			`SELECT content FROM serve_cache WHERE collector_id = $1 AND NOT dirty`, collectorID).Scan(&got); err != nil {
			return ""
		}
		return got
	}

	It("serves the current revision on the eager path, byte-identical to the lazy agentapi path", func() {
		matchers := []string{`cluster="` + clusterName + `"`}
		created := call("CreatePipeline", map[string]any{
			"orgId": orgID, "name": "rev-probe", "contents": contents, "matchers": matchers,
		})
		pipelineID, _ := created["id"].(string) //nolint:errcheck // asserted non-empty below
		Expect(pipelineID).NotTo(BeEmpty())
		// Two edits while disabled: revisions 2 and 3.
		for _, c := range []string{contents + "\n// edit 2", contents + "\n// edit 3"} {
			call("UpdatePipeline", map[string]any{
				"orgId": orgID, "id": pipelineID, "name": "rev-probe", "contents": c, "matchers": matchers,
			})
		}
		call("EnablePipeline", map[string]any{"orgId": orgID, "id": pipelineID})

		Eventually(served, "20s", "250ms").Should(ContainSubstring("//   - rev-probe (rev 3)\n"),
			"the eager recompute after enable must print the pipeline's current revision (3)")

		// An edit while enabled writes revision 4 and recomputes again.
		call("UpdatePipeline", map[string]any{
			"orgId": orgID, "id": pipelineID, "name": "rev-probe", "contents": contents + "\n// edit 4", "matchers": matchers,
		})
		Eventually(served, "20s", "250ms").Should(ContainSubstring("//   - rev-probe (rev 4)\n"))
		eager, err := st.Queries.GetServeCache(ctx, collectorID)
		Expect(err).NotTo(HaveOccurred())

		// The lazy path, wired exactly as internal/server wires it.
		reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
		Expect(err).NotTo(HaveOccurred())
		lazy := agentapi.New(st, nil, slog.Default(), reg,
			agentapi.WithBeaconBaseline(serve.BeaconBaseline(cfg)))
		Expect(st.Queries.MarkServeCacheDirty(ctx, collectorID)).To(Succeed())
		resp, err := lazy.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: "rev-instance", LocalAttributes: map[string]string{"cluster": clusterName, "role": "metrics"},
		}))
		Expect(err).NotTo(HaveOccurred())
		after, err := st.Queries.GetServeCache(ctx, collectorID)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.Dirty).To(BeFalse(), "the lazy path must have recomputed and written the row")

		Expect(resp.Msg.GetContent()).To(Equal(eager.Content),
			"the lazy (agentapi) and eager (mgmtapi) paths must serve byte-identical config for the same fleet")
		Expect(resp.Msg.GetHash()).To(Equal(eager.Hash))
		Expect(after.Hash).To(Equal(eager.Hash))
	})

	// #369: with the beacon disabled, MountRPC's eager recompute appended the
	// D6 baseline (it passed cfg.Server.BaseURL unconditionally) while the
	// lazy path internal/server wires passed "" and appended nothing, so a
	// beacon-disabled collector's bytes and hash flipped with whichever path
	// recomputed last and Alloy reloaded on every flip. Every path now takes
	// its baseline from serve.BeaconBaseline(cfg).
	// Red run, executed: before the fix the eager content carried the
	// prometheus.remote_write "beacon" block and this spec failed.
	It("serves byte-identical config on both paths with the beacon disabled, and no baseline", func() {
		server.Close()
		cfg.Server.BeaconDisabled = true
		server = httptest.NewServer(newRPCWiringRouter(st, auth.NewLocalAdmin(cfg, st, slog.Default()), cfg))

		matchers := []string{`cluster="` + clusterName + `"`}
		created := call("CreatePipeline", map[string]any{
			"orgId": orgID, "name": "rev-probe", "contents": contents, "matchers": matchers,
		})
		pipelineID, _ := created["id"].(string) //nolint:errcheck // asserted non-empty below
		Expect(pipelineID).NotTo(BeEmpty())
		call("EnablePipeline", map[string]any{"orgId": orgID, "id": pipelineID})
		Eventually(served, "20s", "250ms").Should(ContainSubstring("//   - rev-probe (rev 1)\n"))
		eager, err := st.Queries.GetServeCache(ctx, collectorID)
		Expect(err).NotTo(HaveOccurred())
		Expect(eager.Content).NotTo(ContainSubstring(`prometheus.remote_write "beacon"`),
			"a disabled beacon must not reach the eager path's served config")

		reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
		Expect(err).NotTo(HaveOccurred())
		lazy := agentapi.New(st, nil, slog.Default(), reg,
			agentapi.WithBeaconBaseline(serve.BeaconBaseline(cfg)))
		Expect(st.Queries.MarkServeCacheDirty(ctx, collectorID)).To(Succeed())
		resp, err := lazy.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: "rev-instance", LocalAttributes: map[string]string{"cluster": clusterName, "role": "metrics"},
		}))
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Msg.GetContent()).To(Equal(eager.Content),
			"the lazy and eager paths must serve byte-identical config with the beacon disabled")
		Expect(resp.Msg.GetHash()).To(Equal(eager.Hash))
	})

	// The v0.16 rolling upgrade, replayed over one database: the eager path
	// (new binary) has served the current bytes; then migration 0032's mark
	// lands and a v0.15 pod -- still serving during the rollout -- recomputes
	// first, writing "(rev 0)" bytes with the v0.15 statement (it names its
	// columns, so serve_cache.format stays where the mark reset it: 0) and
	// clearing dirty. A poll on a new pod must recompute and serve exactly
	// the bytes the eager path produced -- the bytes #323 serves, no new
	// header text -- and write them back in the current format.
	// Red run, executed: without the format check in agentapi's GetConfig
	// the poll answered not-modified for the v0.15 hash.
	It("replaces a v0.15 pod's mid-rollout rewrite with the eager path's bytes on the next poll", func() {
		matchers := []string{`cluster="` + clusterName + `"`}
		created := call("CreatePipeline", map[string]any{
			"orgId": orgID, "name": "rev-probe", "contents": contents, "matchers": matchers,
		})
		pipelineID, _ := created["id"].(string) //nolint:errcheck // asserted non-empty below
		Expect(pipelineID).NotTo(BeEmpty())
		call("UpdatePipeline", map[string]any{
			"orgId": orgID, "id": pipelineID, "name": "rev-probe", "contents": contents + "\n// edit 2", "matchers": matchers,
		})
		call("EnablePipeline", map[string]any{"orgId": orgID, "id": pipelineID})
		Eventually(served, "20s", "250ms").Should(ContainSubstring("//   - rev-probe (rev 2)\n"))
		eager, err := st.Queries.GetServeCache(ctx, collectorID)
		Expect(err).NotTo(HaveOccurred())
		Expect(eager.Format).To(Equal(serve.Format), "the eager path stamps the current format")

		// Migration 0032's statement, then the v0.15 recompute write.
		_, err = st.Pool().Exec(ctx, `UPDATE serve_cache SET dirty = true, dirty_seq = dirty_seq + 1`)
		Expect(err).NotTo(HaveOccurred())
		oldBytes := strings.Replace(eager.Content, "(rev 2)", "(rev 0)", 1)
		tag, err := st.Pool().Exec(ctx, `INSERT INTO serve_cache (collector_id, content, hash, computed_at, dirty, dirty_seq)
VALUES ($1, $2, $3, now(), false, 0)
ON CONFLICT (collector_id) DO UPDATE SET
    content     = EXCLUDED.content,
    hash        = EXCLUDED.hash,
    computed_at = now(),
    dirty       = false
WHERE serve_cache.dirty = true AND serve_cache.dirty_seq = $4`, collectorID, oldBytes, "v015-hash", eager.DirtySeq+1)
		Expect(err).NotTo(HaveOccurred())
		Expect(tag.RowsAffected()).To(BeEquivalentTo(1))

		reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
		Expect(err).NotTo(HaveOccurred())
		lazy := agentapi.New(st, nil, slog.Default(), reg,
			agentapi.WithBeaconBaseline(serve.BeaconBaseline(cfg)))
		resp, err := lazy.GetConfig(ctx, connect.NewRequest(&collectorv1.GetConfigRequest{
			Id: "rev-instance", Hash: "v015-hash", LocalAttributes: map[string]string{"cluster": clusterName, "role": "metrics"},
		}))
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.Msg.GetNotModified()).To(BeFalse())
		Expect(resp.Msg.GetContent()).To(Equal(eager.Content))
		Expect(resp.Msg.GetHash()).To(Equal(eager.Hash))
		after, err := st.Queries.GetServeCache(ctx, collectorID)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.Content).To(Equal(eager.Content))
		Expect(after.Format).To(Equal(serve.Format))
		Expect(after.Dirty).To(BeFalse())
	})
})
