package mgmtapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/serve"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// servedProvenance is GetServedConfigResponse's F-CONTRIB fields as JSON.
type servedProvenance struct {
	Content            string `json:"content"`
	Hash               string `json:"hash"`
	ProvenanceRecorded bool   `json:"provenanceRecorded"`
	Contributing       []struct {
		PipelineID string `json:"pipelineId"`
		Name       string `json:"name"`
		Revision   int    `json:"revision"`
		Source     string `json:"source"`
		BlockName  string `json:"blockName"`
		Matchers   []struct {
			Matcher string `json:"matcher"`
			Label   string `json:"label"`
			Value   string `json:"value"`
			Present bool   `json:"present"`
		} `json:"matchers"`
		Lines struct {
			First int `json:"first"`
			Last  int `json:"last"`
		} `json:"lines"`
	} `json:"contributing"`
	Excluded []struct {
		PipelineID        string   `json:"pipelineId"`
		Name              string   `json:"name"`
		Reason            string   `json:"reason"`
		Message           string   `json:"message"`
		DisallowedSignals []string `json:"disallowedSignals"`
	} `json:"excluded"`
}

// F-CONTRIB (#423): GetServedConfig carries how the served config was built —
// recorded by the recompute that wrote it, read back from serve_cache, never
// re-evaluated per view — behind the same read gate as the content.
var _ = Describe("GetServedConfig provenance (F-CONTRIB)", Label("integration"), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		server *httptest.Server
		orgA   pgtype.UUID
		orgB   pgtype.UUID
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())
		a, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "prov-a", DisplayName: "Prov A", AdminGroupID: "pa-admin",
			ViewerGroupID: pgtype.Text{String: "pa-viewer", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgA = a.ID
		b, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "prov-b", DisplayName: "Prov B", AdminGroupID: "pb-admin",
			ViewerGroupID: pgtype.Text{String: "pb-viewer", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgB = b.ID
		cfg := &config.Config{
			Auth:     config.AuthConfig{InsecureCookies: true},
			Validate: config.ValidateConfig{StabilityLevel: "experimental", Timeout: 10e9},
		}
		server = httptest.NewServer(newRPCWiringRouter(st, auth.NewLocalAdmin(cfg, st, slog.Default()), cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	sessionIn := func(groups ...string) *http.Cookie {
		groupsJSON, err := json.Marshal(groups)
		Expect(err).NotTo(HaveOccurred())
		id := fmt.Sprintf("prov-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: id, UserOid: "oid", Email: "u@example.com", DisplayName: "U",
			GroupIds:  groupsJSON,
			ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			Source:    "test",
		})
		Expect(err).NotTo(HaveOccurred())
		return &http.Cookie{Name: "shepherd_session", Value: id}
	}

	post := func(procedure string, body map[string]any, cookie *http.Cookie) (int, string) {
		buf, err := json.Marshal(body)
		Expect(err).NotTo(HaveOccurred())
		req, err := http.NewRequest(http.MethodPost, server.URL+procedure, strings.NewReader(string(buf)))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.AddCookie(cookie)
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		raw, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		return resp.StatusCode, string(raw)
	}

	collectorIn := func(org pgtype.UUID, cluster string) pgtype.UUID {
		c, err := st.Queries.UpsertCluster(ctx, cluster)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: c.ID, OrgID: org})).To(Succeed())
		coll, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: c.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		return coll.ID
	}

	enabledPipeline := func(org pgtype.UUID, admin *http.Cookie, name, contents string, matchers ...string) string {
		code, body := post("/shepherd.mgmt.v1.PipelineService/CreatePipeline", map[string]any{
			"org_id": org.String(), "name": name, "contents": contents, "matchers": matchers,
		}, admin)
		Expect(code).To(Equal(http.StatusOK), body)
		var created struct {
			ID string `json:"id"`
		}
		Expect(json.Unmarshal([]byte(body), &created)).To(Succeed())
		code, body = post("/shepherd.mgmt.v1.PipelineService/EnablePipeline", map[string]any{
			"org_id": org.String(), "id": created.ID,
		}, admin)
		Expect(code).To(Equal(http.StatusOK), body)
		return created.ID
	}

	served := func(org, collector pgtype.UUID, cookie *http.Cookie) (servedProvenance, string) {
		code, body := post("/shepherd.mgmt.v1.FleetService/GetServedConfig",
			map[string]any{"orgId": org.String(), "id": collector.String()}, cookie)
		Expect(code).To(Equal(http.StatusOK), body)
		var out servedProvenance
		Expect(json.Unmarshal([]byte(body), &out)).To(Succeed())
		return out, body
	}

	const metricsContents = "prometheus.scrape \"app\" {\n  targets    = []\n  forward_to = []\n}\n"
	const logsContents = "loki.source.file \"pods\" {\n  targets    = []\n  forward_to = []\n}\n"

	It("lists the org's contributing and excluded pipelines, with line spans into the served bytes, and nothing of another org", func() {
		collA := collectorIn(orgA, "prov-cluster-a")
		collectorIn(orgB, "prov-cluster-b")
		adminA, adminB := sessionIn("pa-admin"), sessionIn("pb-admin")
		metricsID := enabledPipeline(orgA, adminA, "a-metrics", metricsContents, `cluster="prov-cluster-a"`)
		logsID := enabledPipeline(orgA, adminA, "a-logs", logsContents, `cluster="prov-cluster-a"`)
		// Org B's pipeline matches every metrics collector by its matchers;
		// org A's collector must still never be served — or shown — it.
		enabledPipeline(orgB, adminB, "b-secret-metrics", metricsContents, `role="metrics"`)

		viewerA := sessionIn("pa-viewer")
		var got servedProvenance
		var body string
		Eventually(func() int {
			got, body = served(orgA, collA, viewerA)
			if !got.ProvenanceRecorded {
				return -1
			}
			return len(got.Contributing) + len(got.Excluded)
		}, "10s", "50ms").Should(Equal(2), "the eager recompute records both pipelines")

		Expect(body).NotTo(ContainSubstring("b-secret-metrics"))
		Expect(got.Contributing).To(HaveLen(1))
		c := got.Contributing[0]
		Expect(c.PipelineID).To(Equal(metricsID))
		Expect(c.Name).To(Equal("a-metrics"))
		Expect(c.Revision).To(BeNumerically(">=", 1))
		Expect(c.Matchers).To(HaveLen(1))
		Expect(c.Matchers[0].Label).To(Equal("cluster"))
		Expect(c.Matchers[0].Value).To(Equal("prov-cluster-a"))
		Expect(c.Matchers[0].Present).To(BeTrue())
		// The span slices the very bytes the response serves.
		lines := strings.Split(got.Content, "\n")
		Expect(c.Lines.Last).To(BeNumerically("<=", len(lines)))
		block := lines[c.Lines.First-1 : c.Lines.Last]
		Expect(block[0]).To(Equal(`declare "pipe_a_metrics" {`))
		Expect(block[len(block)-1]).To(Equal(`pipe_a_metrics "default" { }`))
		Expect(c.BlockName).To(Equal("pipe_a_metrics"))

		Expect(got.Excluded).To(HaveLen(1))
		ex := got.Excluded[0]
		Expect(ex.PipelineID).To(Equal(logsID))
		Expect(ex.Reason).To(Equal("role"))
		Expect(ex.Message).To(Equal("its signals (logs) are not allowed on role metrics"))
		Expect(ex.DisallowedSignals).To(Equal([]string{"logs"}))

		// Org B's members, naming their own org, cannot reach it at all.
		code, _ := post("/shepherd.mgmt.v1.FleetService/GetServedConfig",
			map[string]any{"orgId": orgB.String(), "id": collA.String()}, sessionIn("pb-viewer"))
		Expect(code).NotTo(Equal(http.StatusOK))
	})

	// Mid-rollout: a replica predating migration 0049 recomputes a dirty row
	// to the same bytes (so the same hash) with its own upsert, which leaves
	// provenance as the newer replica wrote it. The label evidence in that
	// record may be stale (a label changed and marked the row), so it must not
	// be shown just because the hash still matches.
	It("does not show provenance a pre-0049 replica's rewrite of the row left behind", func() {
		coll := collectorIn(orgA, "prov-cluster-rollout")
		adminA := sessionIn("pa-admin")
		enabledPipeline(orgA, adminA, "rollout-metrics", metricsContents, `cluster="prov-cluster-rollout"`)
		viewerA := sessionIn("pa-viewer")
		Eventually(func() bool {
			got, _ := served(orgA, coll, viewerA)
			return got.ProvenanceRecorded
		}, "10s", "50ms").Should(BeTrue())
		before, err := st.Queries.GetServeCache(ctx, coll)
		Expect(err).NotTo(HaveOccurred())

		// A mark, then the v0.20 binary's UpsertServeCacheConditional: same
		// content and hash, provenance untouched.
		Expect(st.Queries.MarkServeCacheDirty(ctx, coll)).To(Succeed())
		marked, err := st.Queries.GetServeCache(ctx, coll)
		Expect(err).NotTo(HaveOccurred())
		const v020Upsert = `INSERT INTO serve_cache (collector_id, content, hash, computed_at, dirty, dirty_seq, format)
VALUES ($1, $2, $3, now(), false, 0, $5)
ON CONFLICT (collector_id) DO UPDATE SET
    content     = EXCLUDED.content,
    hash        = EXCLUDED.hash,
    computed_at = now(),
    dirty       = false,
    format      = EXCLUDED.format
WHERE (serve_cache.dirty = true OR serve_cache.format < EXCLUDED.format)
  AND serve_cache.dirty_seq = $4`
		tag, err := st.Pool().Exec(ctx, v020Upsert, coll, before.Content, before.Hash, marked.DirtySeq, int16(4))
		Expect(err).NotTo(HaveOccurred())
		Expect(tag.RowsAffected()).To(BeEquivalentTo(1), "the old replica's write must land, as it does mid-rollout")
		after, err := st.Queries.GetServeCache(ctx, coll)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.Hash).To(Equal(before.Hash))
		Expect(after.Provenance).To(MatchJSON(before.Provenance), "the old upsert leaves provenance alone")

		got, _ := served(orgA, coll, viewerA)
		Expect(got.ProvenanceRecorded).To(BeFalse(), "provenance from before the old replica's write must not be shown")
		Expect(got.Contributing).To(BeEmpty())
	})

	It("reports a row computed before provenance was recorded as not recorded, and never re-evaluates it per view", func() {
		coll := collectorIn(orgA, "prov-cluster-legacy")
		// A row as a pre-0049 binary wrote it: content, no provenance.
		_, err := st.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
			CollectorID: coll, Content: "// legacy\n", Hash: "legacy-hash", DirtySeq: 0, Format: serve.Format,
		})
		Expect(err).NotTo(HaveOccurred())
		got, _ := served(orgA, coll, sessionIn("pa-viewer"))
		Expect(got.Content).To(Equal("// legacy\n"))
		Expect(got.ProvenanceRecorded).To(BeFalse())
		Expect(got.Contributing).To(BeEmpty())
		Expect(got.Excluded).To(BeEmpty())
	})
})
