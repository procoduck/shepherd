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
	"shepherd/internal/version"
)

var _ = Describe("shepherd.mgmt.v1.VisualService", Label("integration"), func() {
	var (
		ctx          context.Context
		cancel       context.CancelFunc
		st           *store.Store
		server       *httptest.Server
		orgID        string
		adminCookie  *http.Cookie
		readerCookie *http.Cookie
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "visual-rpc-org", DisplayName: "Visual RPC Org",
			AdminGroupID: "visual-admin-grp", ReaderGroupID: pgtype.Text{String: "visual-reader-grp", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		adminCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "visual-admin-grp")}
		readerCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "visual-reader-grp")}

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	// A minimal graph document renderable without a live schema.Registry: one
	// prometheus.exporter.unix node with no schema-dependent attributes, so
	// Render succeeds against the embedded schema without a destination.
	minimalGraph := func() map[string]any {
		return map[string]any{
			"kind":           "alloy-graph/v1",
			"schema_version": version.AlloySchemaVersion,
			"nodes": []map[string]any{
				{
					"id": "n1", "component": "prometheus.exporter.unix", "label": "unix",
					"position": map[string]any{"x": 0, "y": 0},
					"props":    map[string]any{},
				},
			},
			"edges":    []any{},
			"bindings": []any{},
		}
	}

	It("renders a graph over the Connect handler (happy path)", func() {
		body := map[string]any{"org_id": orgID, "graph": minimalGraph()}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/Render", adminCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var result map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
		Expect(result["content"]).To(ContainSubstring(`prometheus.exporter.unix "unix"`))
	})

	It("denies Render for a session without org-admin access", func() {
		body := map[string]any{"org_id": orgID, "graph": minimalGraph()}
		// GraphView is org-reader, but Render requires org-admin.
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/Render", readerCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		Expect(connectErrorCode(resp)).To(Equal("permission_denied"))
	})

	It("maps an unresolvable schema version to invalid_argument", func() {
		graph := minimalGraph()
		graph["schema_version"] = "does-not-exist"
		body := map[string]any{"org_id": orgID, "graph": graph}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/Render", adminCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(connectErrorCode(resp)).To(Equal("invalid_argument"))
	})

	// B3(b) (docs/reviews/README.md): GraphView called visual.ParseAlloy
	// without the schema argument, so its fallback text re-parse used the
	// legacy referenced->referencing edge orientation — backwards for a
	// receiver export (D1) like prometheus.remote_write's `receiver`, which
	// prometheus.scrape's `forward_to` argument references. The edge the
	// graph view returns must run produces->accepts: scrape.forward_to (an
	// argument, role "produces" under D1) -> remote_write.receiver (an
	// export, role "accepts").
	It("orients a receiver-kind reference produces->accepts when re-parsing text (D1)", func() {
		contents := `prometheus.scrape "scrape" {
  targets    = []
  forward_to = [prometheus.remote_write.sink.receiver]
}

prometheus.remote_write "sink" {
  endpoint {
    url = "http://example.com/api/v1/push"
  }
}
`
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "graph-view-direction-pipe", Contents: contents,
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "ui", CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		body := map[string]any{"org_id": orgID, "id": p.ID.String()}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/GraphView", readerCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var result map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
		graph, ok := result["graph"].(map[string]any)
		Expect(ok).To(BeTrue(), "expected a graph in the GraphView response, got %#v", result)
		nodes, ok := graph["nodes"].([]any)
		Expect(ok).To(BeTrue())
		Expect(nodes).To(HaveLen(2))

		var scrapeID, remoteWriteID string
		for _, raw := range nodes {
			n, ok := raw.(map[string]any)
			Expect(ok).To(BeTrue())
			id, idOK := n["id"].(string)
			Expect(idOK).To(BeTrue())
			switch n["component"] {
			case "prometheus.scrape":
				scrapeID = id
			case "prometheus.remote_write":
				remoteWriteID = id
			}
		}
		Expect(scrapeID).NotTo(BeEmpty())
		Expect(remoteWriteID).NotTo(BeEmpty())

		edges, ok := graph["edges"].([]any)
		Expect(ok).To(BeTrue())
		Expect(edges).To(HaveLen(1))
		edge, ok := edges[0].(map[string]any)
		Expect(ok).To(BeTrue())
		from, ok := edge["from"].(map[string]any)
		Expect(ok).To(BeTrue())
		to, ok := edge["to"].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(from["node"]).To(Equal(scrapeID))
		Expect(from["port"]).To(Equal("forward_to"))
		Expect(to["node"]).To(Equal(remoteWriteID))
		Expect(to["port"]).To(Equal("receiver"))
	})

	It("allows GraphView for an org-reader session", func() {
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "graph-view-pipe", Contents: "// empty pipeline\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "ui", CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		body := map[string]any{"org_id": orgID, "id": p.ID.String()}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/GraphView", readerCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})
})
