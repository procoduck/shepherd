package mgmtapi_test

import (
	"context"
	"encoding/json"
	"io"
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
		viewerCookie *http.Cookie
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "visual-rpc-org", DisplayName: "Visual RPC Org",
			AdminGroupID: "visual-admin-grp", ViewerGroupID: pgtype.Text{String: "visual-viewer-grp", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		adminCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "visual-admin-grp")}
		viewerCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "visual-viewer-grp")}

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

	// #114: a graph using an experimental component (otelcol.exporter.debug is
	// experimental in the embedded schema and needs no required attributes).
	experimentalGraph := func() map[string]any {
		return map[string]any{
			"kind":           "alloy-graph/v1",
			"schema_version": version.AlloySchemaVersion,
			"nodes": []map[string]any{
				{
					"id": "n1", "component": "otelcol.exporter.debug", "label": "debug",
					"position": map[string]any{"x": 0, "y": 0},
					"props":    map[string]any{},
				},
			},
			"edges":    []any{},
			"bindings": []any{},
		}
	}

	hasExperimentalGated := func(result map[string]any) bool {
		diags, ok := result["diagnostics"].([]any)
		if !ok {
			return false
		}
		for _, raw := range diags {
			if d, ok := raw.(map[string]any); ok && d["code"] == "experimental_gated" {
				return true
			}
		}
		return false
	}

	It("gates an experimental component unless the org opts in (#114)", func() {
		// The org defaults to allow_experimental_components = false.
		body := map[string]any{"org_id": orgID, "graph": experimentalGraph()}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/Render", adminCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var gated map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&gated)).To(Succeed())
		Expect(hasExperimentalGated(gated)).To(BeTrue(), "expected an experimental_gated diagnostic when the org has not opted in")

		// Opt the org in, then the same graph renders without the gate.
		_, err := st.Queries.UpdateOrg(ctx, sqlc.UpdateOrgParams{
			ID: orgUUID(orgID), DisplayName: "Visual RPC Org", AdminGroupID: "visual-admin-grp",
			AllowExperimentalComponents: true,
		})
		Expect(err).NotTo(HaveOccurred())

		resp2 := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/Render", adminCookie, body)
		defer resp2.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp2.StatusCode).To(Equal(http.StatusOK))
		var allowed map[string]any
		Expect(json.NewDecoder(resp2.Body).Decode(&allowed)).To(Succeed())
		Expect(hasExperimentalGated(allowed)).To(BeFalse(), "expected no gate once the org opts in")
		Expect(allowed["content"]).To(ContainSubstring(`otelcol.exporter.debug "debug"`))
	})

	It("denies Render of a new graph for a session without org editor access", func() {
		body := map[string]any{"org_id": orgID, "graph": minimalGraph()}
		// GraphView is org viewer, but Render of a new graph (no pipeline_id)
		// requires org editor (#319: decided in the handler, not the interceptor).
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/Render", viewerCookie, body)
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
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/GraphView", viewerCookie, body)
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

	// The read-only graph view of a hand-written / chart-generated pipeline:
	// the opaque block's source text and the secret references reach the
	// client, through the existing GraphDocument message (no proto change).
	It("returns opaque blocks' source as $raw and secret references as bindings", func() {
		contents := `declare "feature" {
  argument "dest" { }
}

feature "x" {
  dest = [prometheus.remote_write.sink.receiver]
}

remote.kubernetes.secret "creds" {
  name      = "creds"
  namespace = "default"
}

prometheus.remote_write "sink" {
  endpoint {
    url = "http://example.com/api/v1/push"
    basic_auth {
      password = remote.kubernetes.secret.creds.data["password"]
    }
  }
}
`
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "graph-view-opaque-pipe", Contents: contents,
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "git", CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/GraphView", viewerCookie,
			map[string]any{"org_id": orgID, "id": p.ID.String()})
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var result struct {
			Graph struct {
				Nodes []struct {
					ID        string         `json:"id"`
					Component string         `json:"component"`
					Props     map[string]any `json:"props"`
				} `json:"nodes"`
				Edges    []map[string]any `json:"edges"`
				Bindings []struct {
					Node string `json:"node"`
					Prop string `json:"prop"`
					Ref  struct {
						Node   string `json:"node"`
						Export string `json:"export"`
						Expr   string `json:"expr"`
					} `json:"ref"`
				} `json:"bindings"`
			} `json:"graph"`
			Opaque  bool   `json:"opaque"`
			Warning string `json:"warning"`
		}
		Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
		Expect(result.Opaque).To(BeTrue())
		Expect(result.Warning).To(ContainSubstring(`"feature" is not a component`))

		raws := map[string]any{}
		ids := map[string]string{}
		for _, n := range result.Graph.Nodes {
			ids[n.Component] = n.ID
			if raw, ok := n.Props["$raw"]; ok {
				raws[n.Component] = raw
			}
		}
		Expect(raws).To(HaveLen(2))
		Expect(raws["opaque.feature"]).To(HavePrefix(`feature "x" {`))
		Expect(result.Graph.Edges).To(HaveLen(1))

		Expect(result.Graph.Bindings).To(HaveLen(1))
		b := result.Graph.Bindings[0]
		Expect(b.Node).To(Equal(ids["prometheus.remote_write"]))
		Expect(b.Prop).To(Equal("endpoint.basic_auth.password"))
		Expect(b.Ref.Node).To(Equal(ids["remote.kubernetes.secret"]))
		Expect(b.Ref.Export).To(Equal("data"))
		Expect(b.Ref.Expr).To(Equal(`remote.kubernetes.secret.creds.data["password"]`))
	})

	// A chart-shaped module: one component inside a `declare`, reaching the
	// destination only through argument.dest.value and the instance.
	moduleContents := `declare "feature" {
  argument "dest" { }

  prometheus.exporter.unix "host" { }

  prometheus.scrape "host" {
    targets    = prometheus.exporter.unix.host.targets
    forward_to = argument.dest.value
  }
}

feature "x" {
  dest = [prometheus.remote_write.sink.receiver]
}

prometheus.remote_write "sink" {
  endpoint {
    url = "http://example.com/api/v1/push"
  }
}
`

	It("expands modules in GraphView: children carry the group's id as parent", func() {
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "graph-view-module-pipe", Contents: moduleContents,
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "git", CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/GraphView", viewerCookie,
			map[string]any{"org_id": orgID, "id": p.ID.String()})
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var result struct {
			Graph struct {
				Nodes []struct {
					ID        string `json:"id"`
					Component string `json:"component"`
					Label     string `json:"label"`
					Parent    string `json:"parent"`
				} `json:"nodes"`
				Edges []struct {
					From struct{ Node, Port string } `json:"from"`
					To   struct{ Node, Port string } `json:"to"`
				} `json:"edges"`
			} `json:"graph"`
			Opaque bool `json:"opaque"`
		}
		Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
		Expect(result.Opaque).To(BeTrue(), "a module cannot be edited in the builder")

		parents := map[string]string{}
		ids := map[string]string{}
		for _, n := range result.Graph.Nodes {
			parents[n.Component+"/"+n.Label] = n.Parent
			ids[n.Component+"/"+n.Label] = n.ID
		}
		group := ids["declare/feature"]
		Expect(group).NotTo(BeEmpty())
		Expect(parents).To(Equal(map[string]string{
			"declare/feature":               "",
			"prometheus.exporter.unix/host": group,
			"prometheus.scrape/host":        group,
			"opaque.feature/x":              "",
			"prometheus.remote_write/sink":  "",
		}))
		type hop struct{ from, to string }
		var hops []hop
		for _, e := range result.Graph.Edges {
			hops = append(hops, hop{e.From.Node, e.To.Node})
		}
		Expect(hops).To(ConsistOf(
			hop{ids["prometheus.exporter.unix/host"], ids["prometheus.scrape/host"]},
			hop{ids["prometheus.scrape/host"], group},
			hop{group, ids["opaque.feature/x"]},
			hop{ids["opaque.feature/x"], ids["prometheus.remote_write/sink"]},
		))
	})

	It("never expands modules for DiffRevisions", func() {
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "diff-module-pipe", Contents: moduleContents,
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "git", CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
			PipelineID: p.ID, Revision: 1, Contents: "// r1\n", Matchers: json.RawMessage(`[]`),
			Enabled: false, ChangedBy: "test", ChangeNote: "first",
		})
		Expect(err).NotTo(HaveOccurred())

		body := map[string]any{"org_id": orgID, "id": p.ID.String(), "from_revision": 1, "to_revision": 0}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/DiffRevisions", viewerCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var result struct {
			Diff struct {
				NodeChanges []struct {
					ID        string `json:"id"`
					Component string `json:"component"`
				} `json:"nodeChanges"`
			} `json:"diff"`
		}
		Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
		var components []string
		for _, nc := range result.Diff.NodeChanges {
			components = append(components, nc.Component)
		}
		Expect(components).To(ConsistOf("opaque.declare", "opaque.feature", "prometheus.remote_write"),
			"the module stays one opaque node: no group, none of its components")
	})

	It("refuses to Render or Validate a graph whose node carries a parent", func() {
		graph := minimalGraph()
		nodes, ok := graph["nodes"].([]map[string]any)
		Expect(ok).To(BeTrue())
		nodes[0]["parent"] = "n_declare_feature"
		for _, procedure := range []string{"Render", "Validate"} {
			resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/"+procedure, adminCookie,
				map[string]any{"org_id": orgID, "graph": graph})
			Expect(resp.StatusCode).To(Equal(http.StatusBadRequest), procedure)
			raw, err := io.ReadAll(resp.Body)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp.Body.Close()).To(Succeed())
			Expect(string(raw)).To(ContainSubstring("invalid_argument"))
			Expect(string(raw)).To(ContainSubstring("read-only graph view"), procedure)
		}
	})

	It("allows GraphView for an org viewer session", func() {
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "graph-view-pipe", Contents: "// empty pipeline\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "ui", CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		body := map[string]any{"org_id": orgID, "id": p.ID.String()}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/GraphView", viewerCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	// A visual graph carrying its own wizard_state, so DiffRevisions reads the
	// saved graph directly rather than re-parsing Alloy. `scrape` is the
	// scrape_interval on the single node, so callers can vary it per revision.
	visualStateGraph := func(scrape string, extraNode bool) json.RawMessage {
		nodes := []map[string]any{{
			"id": "n1", "component": "prometheus.scrape", "label": "web",
			"position": map[string]any{"x": 0, "y": 0},
			"props":    map[string]any{"scrape_interval": scrape},
		}}
		if extraNode {
			nodes = append(nodes, map[string]any{
				"id": "n2", "component": "loki.write", "label": "central",
				"position": map[string]any{"x": 200, "y": 0},
				"props":    map[string]any{},
			})
		}
		b, err := json.Marshal(map[string]any{
			"kind": "alloy-graph/v1", "schema_version": version.AlloySchemaVersion,
			"nodes": nodes, "edges": []any{}, "bindings": []any{},
		})
		Expect(err).NotTo(HaveOccurred())
		return b
	}

	It("diffs a revision against the current saved graph (to_revision 0)", func() {
		// Current pipeline: two nodes, scrape_interval 30s.
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "diff-pipe", Contents: "// current\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "visual",
			WizardState: visualStateGraph("30s", true),
			CreatedBy:   "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		// Revision 1: one node, scrape_interval 15s.
		_, err = st.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
			PipelineID: p.ID, Revision: 1, Contents: "// r1\n", Matchers: json.RawMessage(`[]`),
			Enabled: false, ChangedBy: "test", ChangeNote: "first",
			WizardState: visualStateGraph("15s", false),
		})
		Expect(err).NotTo(HaveOccurred())

		body := map[string]any{"org_id": orgID, "id": p.ID.String(), "from_revision": 1, "to_revision": 0}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/DiffRevisions", viewerCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))

		var result map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&result)).To(Succeed())
		diff, ok := result["diff"].(map[string]any)
		Expect(ok).To(BeTrue(), "expected a diff, got %#v", result)
		nodeChanges, ok := diff["nodeChanges"].([]any)
		Expect(ok).To(BeTrue())
		Expect(nodeChanges).To(HaveLen(2))

		byID := map[string]map[string]any{}
		for _, raw := range nodeChanges {
			nc, ok := raw.(map[string]any)
			Expect(ok).To(BeTrue())
			id, ok := nc["id"].(string)
			Expect(ok).To(BeTrue())
			byID[id] = nc
		}
		Expect(byID["n1"]["kind"]).To(Equal("changed"))
		fcs, ok := byID["n1"]["fieldChanges"].([]any)
		Expect(ok).To(BeTrue())
		Expect(fcs).To(HaveLen(1))
		fc, ok := fcs[0].(map[string]any)
		Expect(ok).To(BeTrue())
		Expect(fc["field"]).To(Equal("prop:scrape_interval"))
		Expect(fc["oldValue"]).To(Equal("15s"))
		Expect(fc["newValue"]).To(Equal("30s"))
		Expect(byID["n2"]["kind"]).To(Equal("added"))
	})

	It("returns not_found for a from_revision that does not exist", func() {
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "diff-missing-rev", Contents: "// c\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "visual",
			WizardState: visualStateGraph("30s", false), CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())

		body := map[string]any{"org_id": orgID, "id": p.ID.String(), "from_revision": 99, "to_revision": 0}
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.VisualService/DiffRevisions", viewerCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusNotFound))
		Expect(connectErrorCode(resp)).To(Equal("not_found"))
	})
})
