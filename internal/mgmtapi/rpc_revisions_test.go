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

// pipelineID extracts "id" from a decoded Connect JSON response body,
// failing the spec immediately (via Gomega, not a panicking assertion) if
// it is missing or not a string.
func pipelineID(p map[string]any) string {
	id, ok := p["id"].(string)
	Expect(ok).To(BeTrue(), "expected a string \"id\" in the response, got %#v", p["id"])
	return id
}

// F-REVISIONS backend package: GetRevision (full-field read of one
// revision) and RestoreRevision (create a new revision from an old one's
// contents/matchers/wizard_state, keeping the current enabled state (#377), through the same validation gate
// and authorization as UpdatePipeline). See docs/archive/plans/2026-09-11-f-revisions.md
// §3 for the settled shape; this file is B-6's Ginkgo coverage.
var _ = Describe("PipelineService GetRevision / RestoreRevision", Label("integration"), func() {
	var (
		ctx          context.Context
		cancel       context.CancelFunc
		st           *store.Store
		server       *httptest.Server
		orgID        string
		orgID2       string
		editorSess   string
		viewerSess   string
		editorCookie *http.Cookie
		viewerCookie *http.Cookie
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "revisions-org", DisplayName: "Revisions Org",
			AdminGroupID:  "revisions-admin-grp",
			EditorGroupID: pgtype.Text{String: "revisions-editor-grp", Valid: true},
			ViewerGroupID: pgtype.Text{String: "revisions-viewer-grp", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		o2, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "revisions-org-2", DisplayName: "Revisions Org 2", AdminGroupID: "revisions-admin-grp-2",
		})
		Expect(err).NotTo(HaveOccurred())
		orgID2 = o2.ID.String()

		editorSess = newTestSession(ctx, st, "revisions-editor-grp")
		viewerSess = newTestSession(ctx, st, "revisions-viewer-grp")
		editorCookie = &http.Cookie{Name: "shepherd_session", Value: editorSess}
		viewerCookie = &http.Cookie{Name: "shepherd_session", Value: viewerSess}

		cfg := &config.Config{
			Auth: config.AuthConfig{InsecureCookies: true},
			Validate: config.ValidateConfig{
				AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10e9,
			},
		}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	// editorEmail mirrors newTestSession's Email: id + "@example.com" (see
	// rpc_wizard_test.go) — the actor a session's writes are attributed to.
	editorEmail := func() string { return editorSess + "@example.com" }

	// createPipeline creates an (unowned, editor-writable) pipeline over the
	// Connect endpoint, which also writes its first revision (createPipelineRevisionQ
	// inside CreatePipeline) — this is rev1 for every spec below unless
	// noted otherwise.
	createPipeline := func(name, contents string) map[string]any {
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/CreatePipeline", editorCookie, map[string]any{
			"orgId": orgID, "name": name, "contents": contents, "matchers": []string{},
		})
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var p map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&p)).To(Succeed())
		return p
	}

	updatePipeline := func(id, name, contents string) *http.Response {
		return postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", editorCookie, map[string]any{
			"orgId": orgID, "id": id, "name": name, "contents": contents, "matchers": []string{},
		})
	}

	restoreRevision := func(cookie *http.Cookie, id string, revision int, changeNote string) *http.Response {
		body := map[string]any{"orgId": orgID, "id": id, "revision": revision}
		if changeNote != "" {
			body["changeNote"] = changeNote
		}
		return postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/RestoreRevision", cookie, body)
	}

	getRevision := func(cookie *http.Cookie, org, id string, revision int) *http.Response {
		return postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/GetRevision", cookie, map[string]any{
			"orgId": org, "id": id, "revision": revision,
		})
	}

	listRevisions := func(id string) []map[string]any {
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/ListRevisions", editorCookie, map[string]any{
			"orgId": orgID, "id": id,
		})
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var out struct {
			Items []map[string]any `json:"items"`
		}
		Expect(json.NewDecoder(resp.Body).Decode(&out)).To(Succeed())
		return out.Items
	}

	// 1. Restore creates revision N+1 with the old contents; the old
	// revision is never mutated.
	It("restores a pipeline's contents from an old revision as a brand-new revision", func() {
		p := createPipeline("restore-basic", "// v1\n") // rev 1
		id := pipelineID(p)

		resp := updatePipeline(id, "restore-basic", "// v2\n") // rev 2
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Body.Close()).To(Succeed())

		resp = restoreRevision(editorCookie, id, 1, "")
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var restored map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&restored)).To(Succeed())
		Expect(resp.Body.Close()).To(Succeed())
		Expect(restored["contents"]).To(Equal("// v1\n"))

		items := listRevisions(id)
		Expect(items).To(HaveLen(3))
		Expect(items[0]["revision"]).To(BeNumerically("==", 3))
		Expect(items[0]["changeNote"]).To(Equal("Restored from revision 1"))

		revResp := getRevision(editorCookie, orgID, id, 1)
		Expect(revResp.StatusCode).To(Equal(http.StatusOK))
		var rev1 map[string]any
		Expect(json.NewDecoder(revResp.Body).Decode(&rev1)).To(Succeed())
		Expect(revResp.Body.Close()).To(Succeed())
		Expect(rev1["contents"]).To(Equal("// v1\n"), "restoring must never mutate the old revision row")
	})

	// 2. Restore goes through the same validation gate as UpdatePipeline.
	It("refuses to restore a revision whose contents fail the validation gate", func() {
		p := createPipeline("restore-invalid-gate", "// v1\n") // rev 1
		id := pipelineID(p)
		var pid pgtype.UUID
		Expect(pid.Scan(id)).To(Succeed())

		// Seed a revision row directly whose contents fail Stage 1 (unbalanced
		// block) — something RestoreRevision must reject before ever writing.
		_, err := st.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
			PipelineID: pid, Revision: 2, Contents: `prometheus.exporter.self "x" {`,
			Matchers: json.RawMessage(`[]`), Enabled: false, ChangedBy: "test", ChangeNote: "seeded invalid",
		})
		Expect(err).NotTo(HaveOccurred())

		resp := restoreRevision(editorCookie, id, 2, "")
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(connectErrorCode(resp)).To(Equal("failed_precondition"))
		Expect(resp.Body.Close()).To(Succeed())

		items := listRevisions(id)
		Expect(items).To(HaveLen(2), "a failed restore must not write a new revision")

		getResp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/GetPipeline", editorCookie, map[string]any{
			"orgId": orgID, "id": id,
		})
		defer getResp.Body.Close() //nolint:errcheck // test cleanup
		var got map[string]any
		Expect(json.NewDecoder(getResp.Body).Decode(&got)).To(Succeed())
		Expect(got["contents"]).To(Equal("// v1\n"), "a failed restore must not touch the pipeline's stored contents")
	})

	// 3. A viewer cannot restore; an editor can — the same authorization
	// UpdatePipeline uses.
	DescribeTable("restore authorization matches UpdatePipeline's",
		func(cookieFn func() *http.Cookie, wantStatus int, wantCode string) {
			p := createPipeline("restore-authz", "// v1\n")
			id := pipelineID(p)
			resp := updatePipeline(id, "restore-authz", "// v2\n")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			Expect(resp.Body.Close()).To(Succeed())

			resp = restoreRevision(cookieFn(), id, 1, "")
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(wantStatus))
			if wantCode != "" {
				Expect(connectErrorCode(resp)).To(Equal(wantCode))
			}
		},
		Entry("viewer is refused permission_denied", func() *http.Cookie { return viewerCookie }, http.StatusForbidden, "permission_denied"),
		Entry("editor succeeds", func() *http.Cookie { return editorCookie }, http.StatusOK, ""),
	)

	// 4. Restoring a visual pipeline restores its wizard_state graph
	// alongside the text (S4).
	It("restores a visual pipeline's wizard_state alongside its contents", func() {
		graphA := json.RawMessage(`{"kind":"alloy-graph/v1","schema_version":"v1","nodes":[{"id":"a"}]}`)
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "restore-visual", Contents: "// graph a\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "visual",
			WizardState: graphA, CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
			PipelineID: p.ID, Revision: 1, Contents: "// graph a\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, ChangedBy: "test", ChangeNote: "created",
			WizardState: graphA,
		})
		Expect(err).NotTo(HaveOccurred())

		graphB := map[string]any{"kind": "alloy-graph/v1", "schema_version": "v1", "nodes": []map[string]any{{"id": "b"}}}
		// source="wizard" routes around the visual render-equality gate the
		// same way rpc_pipeline_test.go's "replaces a visual pipeline's
		// stored wizard_state" spec does — UpdatePipeline's SQL never
		// touches the source column, so the row stays "visual".
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", editorCookie, map[string]any{
			"orgId": orgID, "id": p.ID.String(), "name": "restore-visual",
			"contents": "// graph b\n", "matchers": []string{}, "source": "wizard", "wizardState": graphB,
		})
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Body.Close()).To(Succeed())

		resp = restoreRevision(editorCookie, p.ID.String(), 1, "")
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Body.Close()).To(Succeed())

		stored, err := st.Queries.GetPipelineByID(ctx, p.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Contents).To(Equal("// graph a\n"))
		Expect(string(stored.WizardState)).To(MatchJSON(graphA))

		revs, err := st.Queries.ListPipelineRevisions(ctx, p.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revs).To(HaveLen(3))
		Expect(revs[0].Revision).To(Equal(int32(3)))
		Expect(string(revs[0].WizardState)).To(MatchJSON(graphA))
	})

	// 5. Restore writes a pipeline.restore audit row.
	It("writes a pipeline.restore audit row", func() {
		p := createPipeline("restore-audit", "// v1\n")
		id := pipelineID(p)
		resp := updatePipeline(id, "restore-audit", "// v2\n")
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Body.Close()).To(Succeed())

		resp = restoreRevision(editorCookie, id, 1, "")
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Body.Close()).To(Succeed())

		rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: orgUUID(orgID), Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		var found *sqlc.AuditLog
		for i := range rows {
			if rows[i].Action == "pipeline.restore" && rows[i].ResourceID == id {
				found = &rows[i]
				break
			}
		}
		Expect(found).NotTo(BeNil(), "expected a pipeline.restore audit row for %s", id)
		Expect(found.Actor).To(Equal(editorEmail()))
	})

	// 6. Git-sourced restore is allowed even though UpdatePipeline stays
	// read-only for git (S5) — errGitSourceReadOnly must not apply here.
	It("allows restoring a git-sourced pipeline even though UpdatePipeline stays read-only for it", func() {
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "restore-git", Contents: "// git v1\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "git",
			CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
			PipelineID: p.ID, Revision: 1, Contents: "// git v1\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, ChangedBy: "test", ChangeNote: "created",
		})
		Expect(err).NotTo(HaveOccurred())

		resp := restoreRevision(editorCookie, p.ID.String(), 1, "")
		Expect(resp.StatusCode).To(Equal(http.StatusOK), "restoring a git-sourced pipeline must be allowed")
		Expect(resp.Body.Close()).To(Succeed())

		updResp := updatePipeline(p.ID.String(), "restore-git", "// direct edit\n")
		defer updResp.Body.Close() //nolint:errcheck // test cleanup
		Expect(updResp.StatusCode).To(Equal(http.StatusForbidden), "UpdatePipeline must stay read-only for git-sourced pipelines")
		Expect(connectErrorCode(updResp)).To(Equal("permission_denied"))
	})

	// 7. GetRevision returns the full fields; ListRevisions/GetPipeline stay
	// metadata-only (S1); unknown revision and cross-org pipeline ids are
	// both not_found.
	It("returns full fields from GetRevision while ListRevisions stays metadata-only", func() {
		// A non-empty matcher so the field survives protojson's
		// omit-unpopulated default marshaling — proving the connection
		// end-to-end (revisionToProtoFull -> wire), not just "the key is
		// technically declared".
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/CreatePipeline", editorCookie, map[string]any{
			"orgId": orgID, "name": "get-revision-full", "contents": "// v1\n", "matchers": []string{`cluster="prod"`},
		})
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var p map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&p)).To(Succeed())
		Expect(resp.Body.Close()).To(Succeed())
		id := pipelineID(p)

		resp = getRevision(editorCookie, orgID, id, 1)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var rev map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&rev)).To(Succeed())
		Expect(resp.Body.Close()).To(Succeed())
		Expect(rev["contents"]).To(Equal("// v1\n"))
		matchers, hasMatchers := rev["matchers"].([]any)
		Expect(hasMatchers).To(BeTrue(), "GetRevision must carry matchers")
		Expect(matchers).To(ConsistOf(`cluster="prod"`))

		items := listRevisions(id)
		Expect(items).To(HaveLen(1))
		_, hasContents := items[0]["contents"]
		Expect(hasContents).To(BeFalse(), "ListRevisions must stay metadata-only and never carry contents")

		notFound := getRevision(editorCookie, orgID, id, 99)
		defer notFound.Body.Close() //nolint:errcheck // test cleanup
		Expect(notFound.StatusCode).To(Equal(http.StatusNotFound))
		Expect(connectErrorCode(notFound)).To(Equal("not_found"))

		// An app-admin session clears the authz interceptor's org viewer
		// check for ANY org (see auth.AuthorizeOwnership's IsAppAdmin
		// bypass), so this actually reaches loadPipeline's org-scoping
		// check — an editor-group session for orgID2 alone would be refused
		// permission_denied by the interceptor before ever reaching the
		// handler, which is a different control than the one this proves
		// (mirrors rpc_pipeline_test.go's "refuses to read another org's
		// pipeline through a caller-supplied org id").
		adminCookie := newAppAdminSession(ctx, st)
		crossOrg := getRevision(adminCookie, orgID2, id, 1)
		defer crossOrg.Body.Close() //nolint:errcheck // test cleanup
		Expect(crossOrg.StatusCode).To(Equal(http.StatusNotFound))
		Expect(connectErrorCode(crossOrg)).To(Equal("not_found"))
	})

	// 9. Machine callers: a propose-capability service account is refused;
	// an apply account without Shepherd-On-Behalf-Of is invalid_argument
	// (mirrors service_account_tier_test.go / capability_test.go's shape).
	It("refuses a propose-capability service account and demands on-behalf-of from an apply one", func() {
		p := createPipeline("restore-machine-caller", "// v1\n")
		id := pipelineID(p)
		resp := updatePipeline(id, "restore-machine-caller", "// v2\n")
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Body.Close()).To(Succeed())

		proposeSecret, proposeID := g12MakeServiceAccount(ctx, st, orgUUID(orgID), "revisions-propose", "propose")
		proposeResp := g12PostConnect(server, "/shepherd.mgmt.v1.PipelineService/RestoreRevision",
			map[string]any{"orgId": orgID, "id": id, "revision": 1}, proposeID, proposeSecret, g12DelegatedPrincipal)
		defer proposeResp.Body.Close() //nolint:errcheck // test cleanup
		Expect(proposeResp.StatusCode).To(Equal(http.StatusForbidden))
		Expect(g11DecodeBody(proposeResp)["code"]).To(Equal("permission_denied"))

		applySecret, applyID := g12MakeServiceAccount(ctx, st, orgUUID(orgID), "revisions-apply", "apply")
		applyResp := g12PostConnect(server, "/shepherd.mgmt.v1.PipelineService/RestoreRevision",
			map[string]any{"orgId": orgID, "id": id, "revision": 1}, applyID, applySecret, "")
		defer applyResp.Body.Close() //nolint:errcheck // test cleanup
		Expect(applyResp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(g11DecodeBody(applyResp)["code"]).To(Equal("invalid_argument"))
	})

	// Shared setup for the enabled-state specs below (#377): a claimed
	// cluster with one metrics collector the pipeline's matcher selects, so
	// the serve cache shows what is actually served.
	metricsCollector := func(clusterName string) pgtype.UUID {
		cluster, err := st.Queries.UpsertCluster(ctx, clusterName)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
		return collector.ID
	}
	servedContent := func(collectorID pgtype.UUID) func() string {
		return func() string {
			cache, cacheErr := st.Queries.GetServeCache(ctx, collectorID)
			if cacheErr != nil {
				return "sentinel: no serve_cache row"
			}
			return cache.Content
		}
	}
	callOK := func(srv *httptest.Server, procedure string, body map[string]any) map[string]any {
		resp := postConnectJSON(srv, "/shepherd.mgmt.v1.PipelineService/"+procedure, editorCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK), procedure)
		var out map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&out)).To(Succeed())
		return out
	}
	createMetricsPipeline := func(srv *httptest.Server, name, contents string) (string, pgtype.UUID) {
		p := callOK(srv, "CreatePipeline", map[string]any{
			"orgId": orgID, "name": name, "contents": contents, "matchers": []string{`role="metrics"`},
		})
		id := pipelineID(p)
		var pid pgtype.UUID
		Expect(pid.Scan(id)).To(Succeed())
		return id, pid
	}
	auditActions := func(id string) []string {
		rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: orgUUID(orgID), Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		var actions []string
		for i := range rows {
			if rows[i].ResourceID == id {
				actions = append(actions, rows[i].Action)
			}
		}
		return actions
	}

	// 10. #377: restore brings back contents and matchers but keeps the
	// pipeline's CURRENT enabled state. Revision 1 is always written
	// disabled (create stores enabled=false; Enable/Disable write no
	// revision), so before #377 "revert to revision 1" switched an enabled
	// pipeline off and its data stopped. Restoring it onto an enabled
	// pipeline must leave the pipeline enabled, dirty + recompute the serve
	// cache, and serve the restored content. Red before #377: Enabled is
	// false and the collector stops being served the pipeline.
	It("keeps an enabled pipeline enabled and serves the restored content when the revision was written disabled", func() {
		collectorID := metricsCollector("restore-keep-enabled-cluster")
		id, pid := createMetricsPipeline(server, "restore-keep-enabled", "// KEEP-ENABLED-MARKER-A\n") // rev 1, enabled=false
		callOK(server, "EnablePipeline", map[string]any{"orgId": orgID, "id": id})
		callOK(server, "UpdatePipeline", map[string]any{
			"orgId": orgID, "id": id, "name": "restore-keep-enabled",
			"contents": "// KEEP-ENABLED-MARKER-B\n", "matchers": []string{`role="metrics"`},
		}) // rev 2, enabled=true
		Eventually(servedContent(collectorID), "5s", "20ms").Should(ContainSubstring("KEEP-ENABLED-MARKER-B"),
			"collector must be served revision 2 before the restore under test")

		restored := callOK(server, "RestoreRevision", map[string]any{"orgId": orgID, "id": id, "revision": 1})
		Expect(restored).To(HaveKeyWithValue("enabled", true), "the RestoreRevision response must report the pipeline still enabled")

		reloaded, err := st.Queries.GetPipelineByID(ctx, pid)
		Expect(err).NotTo(HaveOccurred())
		Expect(reloaded.Enabled).To(BeTrue(), "restoring a disabled revision must not switch an enabled pipeline off")
		Expect(reloaded.Contents).To(Equal("// KEEP-ENABLED-MARKER-A\n"))

		revs, err := st.Queries.ListPipelineRevisions(ctx, pid)
		Expect(err).NotTo(HaveOccurred())
		Expect(revs[0].Revision).To(BeNumerically("==", 3))
		Expect(revs[0].Enabled).To(BeTrue(), "the new revision records the state the pipeline is in after the restore")

		Eventually(servedContent(collectorID), "10s", "20ms").Should(SatisfyAll(
			ContainSubstring("KEEP-ENABLED-MARKER-A"), Not(ContainSubstring("KEEP-ENABLED-MARKER-B")),
		), "restoring an enabled pipeline must dirty+recompute the serve cache so the collector is served the restored content")

		actions := auditActions(id)
		Expect(actions).To(ContainElement("pipeline.restore"))
		Expect(actions).NotTo(ContainElement("pipeline.disable"), "a restore no longer toggles the pipeline")
	})

	// 10b. #377, the other direction: restoring a revision written while the
	// pipeline was enabled onto a pipeline that is now DISABLED restores
	// its content but leaves it disabled — nothing becomes served. Red
	// before #377: the restore flipped the pipeline back on.
	It("keeps a disabled pipeline disabled when the restored revision was written enabled", func() {
		collectorID := metricsCollector("restore-keep-disabled-cluster")
		id, pid := createMetricsPipeline(server, "restore-keep-disabled", "// KEEP-DISABLED-MARKER-A\n") // rev 1
		callOK(server, "EnablePipeline", map[string]any{"orgId": orgID, "id": id})
		callOK(server, "UpdatePipeline", map[string]any{
			"orgId": orgID, "id": id, "name": "restore-keep-disabled",
			"contents": "// KEEP-DISABLED-MARKER-B\n", "matchers": []string{`role="metrics"`},
		}) // rev 2, enabled=true
		callOK(server, "UpdatePipeline", map[string]any{
			"orgId": orgID, "id": id, "name": "restore-keep-disabled",
			"contents": "// KEEP-DISABLED-MARKER-C\n", "matchers": []string{`role="metrics"`},
		}) // rev 3, enabled=true
		callOK(server, "DisablePipeline", map[string]any{"orgId": orgID, "id": id})
		Eventually(servedContent(collectorID), "5s", "20ms").ShouldNot(ContainSubstring("KEEP-DISABLED-MARKER"),
			"the disabled pipeline must be absent from the served config before the restore under test")

		restored := callOK(server, "RestoreRevision", map[string]any{"orgId": orgID, "id": id, "revision": 2})
		Expect(restored).NotTo(HaveKeyWithValue("enabled", true), "the RestoreRevision response must report the pipeline still disabled (proto JSON omits false)")

		reloaded, err := st.Queries.GetPipelineByID(ctx, pid)
		Expect(err).NotTo(HaveOccurred())
		Expect(reloaded.Enabled).To(BeFalse(), "restoring an enabled revision must not switch a disabled pipeline on")
		Expect(reloaded.Contents).To(Equal("// KEEP-DISABLED-MARKER-B\n"))

		Consistently(servedContent(collectorID), "300ms", "20ms").ShouldNot(ContainSubstring("KEEP-DISABLED-MARKER"),
			"a disabled pipeline stays out of the served config after a restore")

		actions := auditActions(id)
		Expect(actions).To(ContainElement("pipeline.restore"))
		// One pipeline.enable from the EnablePipeline call above; the restore adds none.
		enables := 0
		for _, a := range actions {
			if a == "pipeline.enable" {
				enables++
			}
		}
		Expect(enables).To(Equal(1), "a restore no longer toggles the pipeline")
	})

	// 11. The validation gate is unchanged by #377: a restore onto an
	// ENABLED pipeline runs Stage 3 (the merged-config dry run) with the
	// restored content as enabled, before any write, and a failure leaves
	// the pipeline row, its revision history and the audit log untouched —
	// and in particular keeping enabled=true is never a way past Stage 3.
	// Stage 3 is forced to fail deterministically via an effectively-zero
	// Stage3Timeout on a dedicated server for this spec, rather than a
	// genuine merge content conflict: this harness runs with no alloy
	// binary (Stage 2 is always skipped, matching every other spec in this
	// file) and Stage 1's bare syntax parser does not detect
	// duplicate/colliding component labels across pipelines; a two-pipeline
	// NAME collision is independently blocked by the
	// pipelines_org_sanitized_name_key DB constraint. A timeout reaches the
	// same stage3Check failure branch a real merge conflict would. The
	// pipeline is enabled directly in the store because EnablePipeline
	// itself runs Stage 3 and would fail on this server.
	It("refuses a restore onto an enabled pipeline when the restored content fails the Stage 3 merge dry run, leaving everything untouched", func() {
		tinyStage3Cfg := &config.Config{
			Auth: config.AuthConfig{InsecureCookies: true},
			Validate: config.ValidateConfig{
				AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10e9,
				Stage3Timeout: 1, // effectively zero: context.WithTimeout's deadline is already past at creation, so every Stage3Check call fails deterministically.
			},
		}
		tinyAuthHandler := auth.NewLocalAdmin(tinyStage3Cfg, st, slog.Default())
		tinyServer := httptest.NewServer(newRPCWiringRouter(st, tinyAuthHandler, tinyStage3Cfg))
		defer tinyServer.Close()

		id, pid := createMetricsPipeline(tinyServer, "restore-stage3-timeout", "// v1\n") // rev 1
		_, err := st.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
			PipelineID: pid, Revision: 2, Contents: "// v2 would-be-restored\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, ChangedBy: "test", ChangeNote: "seeded disabled",
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.SetPipelineEnabled(ctx, sqlc.SetPipelineEnabledParams{ID: pid, Enabled: true, UpdatedBy: "test"})
		Expect(err).NotTo(HaveOccurred())

		resp := postConnectJSON(tinyServer, "/shepherd.mgmt.v1.PipelineService/RestoreRevision", editorCookie, map[string]any{
			"orgId": orgID, "id": id, "revision": 2,
		})
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(connectErrorCode(resp)).To(Equal("failed_precondition"))
		Expect(resp.Body.Close()).To(Succeed())

		reloaded, err := st.Queries.GetPipelineByID(ctx, pid)
		Expect(err).NotTo(HaveOccurred())
		Expect(reloaded.Contents).To(Equal("// v1\n"), "a failed Stage 3 check must not touch the pipeline's stored contents")
		Expect(reloaded.Enabled).To(BeTrue(), "a failed restore leaves the enabled bit as it was")

		revs, err := st.Queries.ListPipelineRevisions(ctx, pid)
		Expect(err).NotTo(HaveOccurred())
		Expect(revs).To(HaveLen(2), "a failed restore must not write a new revision")
		Expect(auditActions(id)).NotTo(ContainElement("pipeline.restore"), "a failed restore must not write a pipeline.restore audit row")
	})

	// 11b. The other half of the unchanged gate: a DISABLED pipeline's
	// restore validates as a disabled save does (Stages 1-2 only, nothing
	// is served), even when the restored revision was written enabled —
	// the revision's old enabled bit no longer drives Stage 3. On the
	// Stage-3-always-fails server this restore therefore succeeds and the
	// pipeline stays disabled.
	It("validates a disabled pipeline's restore as a disabled save, whatever the revision's enabled bit", func() {
		tinyStage3Cfg := &config.Config{
			Auth: config.AuthConfig{InsecureCookies: true},
			Validate: config.ValidateConfig{
				AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10e9,
				Stage3Timeout: 1,
			},
		}
		tinyAuthHandler := auth.NewLocalAdmin(tinyStage3Cfg, st, slog.Default())
		tinyServer := httptest.NewServer(newRPCWiringRouter(st, tinyAuthHandler, tinyStage3Cfg))
		defer tinyServer.Close()

		id, pid := createMetricsPipeline(tinyServer, "restore-disabled-gate", "// v1\n") // rev 1
		_, err := st.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
			PipelineID: pid, Revision: 2, Contents: "// v2 restored\n",
			Matchers: json.RawMessage(`[]`), Enabled: true, ChangedBy: "test", ChangeNote: "seeded enabled",
		})
		Expect(err).NotTo(HaveOccurred())

		callOK(tinyServer, "RestoreRevision", map[string]any{"orgId": orgID, "id": id, "revision": 2})

		reloaded, err := st.Queries.GetPipelineByID(ctx, pid)
		Expect(err).NotTo(HaveOccurred())
		Expect(reloaded.Contents).To(Equal("// v2 restored\n"))
		Expect(reloaded.Enabled).To(BeFalse())
	})

	// 12. Fix-up (backend review, F-REVISIONS): RestoreRevision must not run
	// the client-submission visual render-equality gate against
	// server-stored content. Unlike rpc_revisions_test.go's existing visual
	// spec (whose graph's schema_version "v1" does not resolve, so
	// checkVisualRenderMatch errors and the comparison never actually
	// runs), this graph uses the real embedded schema version so the
	// render-match check genuinely engages — and the stored contents are
	// deliberately NOT what re-rendering the graph produces, simulating a
	// renderer/schema change since the revision was written. Before the
	// fix this restore is refused already_exists/409; after it, restoring
	// server-stored content is never subject to that gate at all.
	It("restores a visual pipeline even when its stored contents no longer match re-rendering its graph", func() {
		graph := map[string]any{
			"kind": "alloy-graph/v1", "schema_version": version.AlloySchemaVersion,
			"nodes": []map[string]any{
				{
					"id": "n1", "component": "prometheus.exporter.unix", "label": "unix",
					"position": map[string]any{"x": 0, "y": 0}, "props": map[string]any{},
				},
			},
			"edges": []any{}, "bindings": []any{},
		}
		graphJSON, err := json.Marshal(graph)
		Expect(err).NotTo(HaveOccurred())

		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgUUID(orgID), Name: "restore-visual-render-mismatch", Contents: "// stale content predating a renderer change\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, Source: "visual",
			WizardState: graphJSON, CreatedBy: "test", UpdatedBy: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
			PipelineID: p.ID, Revision: 1, Contents: "// stale content predating a renderer change\n",
			Matchers: json.RawMessage(`[]`), Enabled: false, ChangedBy: "test", ChangeNote: "created",
			WizardState: graphJSON,
		})
		Expect(err).NotTo(HaveOccurred())

		resp := restoreRevision(editorCookie, p.ID.String(), 1, "")
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusOK),
			"restoring server-stored content must not be refused by the client-submission render-equality gate")
	})
})
