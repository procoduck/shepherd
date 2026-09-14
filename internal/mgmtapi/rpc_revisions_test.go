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
// contents/matchers/enabled/wizard_state, through the same validation gate
// and authorization as UpdatePipeline). See docs/plans/2026-09-11-f-revisions.md
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
		readerSess   string
		editorCookie *http.Cookie
		readerCookie *http.Cookie
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
			ReaderGroupID: pgtype.Text{String: "revisions-reader-grp", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		o2, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "revisions-org-2", DisplayName: "Revisions Org 2", AdminGroupID: "revisions-admin-grp-2",
		})
		Expect(err).NotTo(HaveOccurred())
		orgID2 = o2.ID.String()

		editorSess = newTestSession(ctx, st, "revisions-editor-grp")
		readerSess = newTestSession(ctx, st, "revisions-reader-grp")
		editorCookie = &http.Cookie{Name: "shepherd_session", Value: editorSess}
		readerCookie = &http.Cookie{Name: "shepherd_session", Value: readerSess}

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
	// Connect endpoint, which also writes its first revision (createRevision
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

	// 3. A reader cannot restore; an editor can — the same authorization
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
		Entry("reader is refused permission_denied", func() *http.Cookie { return readerCookie }, http.StatusForbidden, "permission_denied"),
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

		// An app-admin session clears the authz interceptor's org-reader
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

	// 10. Fix-up (backend review, F-REVISIONS): a restore that flips an
	// ENABLED pipeline back to disabled must dirty and recompute the serve
	// cache exactly as EnablePipeline/DisablePipeline/DeletePipeline do —
	// otherwise every collector the pipeline matched keeps being served its
	// config indefinitely, since nothing else would ever mark the cache
	// dirty again. Only the enabled bit changes across the restore here (the
	// matcher and content are identical before and after), isolating the
	// assertion to the dirty/recompute wiring rather than any content
	// difference.
	It("dirties and recomputes the serve cache when a restore flips an enabled pipeline back to disabled", func() {
		cluster, err := st.Queries.UpsertCluster(ctx, "restore-dirty-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		createResp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/CreatePipeline", editorCookie, map[string]any{
			"orgId": orgID, "name": "restore-dirty-pipe", "contents": "// RESTORE-DIRTY-MARKER\n",
			"matchers": []string{`role="metrics"`},
		})
		Expect(createResp.StatusCode).To(Equal(http.StatusOK))
		var p map[string]any
		Expect(json.NewDecoder(createResp.Body).Decode(&p)).To(Succeed())
		Expect(createResp.Body.Close()).To(Succeed())
		id := pipelineID(p)

		// Enable it (still rev1's content — EnablePipeline never writes a
		// new revision) and wait for the resulting eager recompute to
		// actually serve this collector the marker, proving the setup is
		// live before the restore under test.
		enableResp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/EnablePipeline", editorCookie, map[string]any{
			"orgId": orgID, "id": id,
		})
		Expect(enableResp.StatusCode).To(Equal(http.StatusOK))
		Expect(enableResp.Body.Close()).To(Succeed())

		Eventually(func() string {
			cache, cacheErr := st.Queries.GetServeCache(ctx, collector.ID)
			if cacheErr != nil {
				return ""
			}
			return cache.Content
		}, "5s", "20ms").Should(ContainSubstring("RESTORE-DIRTY-MARKER"),
			"collector must be served the enabled pipeline's content before the restore under test")

		// Restore revision 1 — created disabled, so this is an
		// enabled(true) -> disabled(false) TRANSITION.
		resp := restoreRevision(editorCookie, id, 1, "")
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Body.Close()).To(Succeed())

		var pid pgtype.UUID
		Expect(pid.Scan(id)).To(Succeed())
		reloaded, err := st.Queries.GetPipelineByID(ctx, pid)
		Expect(err).NotTo(HaveOccurred())
		Expect(reloaded.Enabled).To(BeFalse(), "restoring revision 1 must flip the pipeline back to disabled")

		Eventually(func() string {
			cache, cacheErr := st.Queries.GetServeCache(ctx, collector.ID)
			if cacheErr != nil {
				return "sentinel: no serve_cache row"
			}
			return cache.Content
		}, "5s", "20ms").ShouldNot(ContainSubstring("RESTORE-DIRTY-MARKER"),
			"restoring a pipeline from enabled to disabled must dirty+recompute the serve cache so "+
				"the collector stops being served its config")
	})

	// 10b. Re-check follow-up: the other direction of the enabled flip. A
	// successful restore of an enabled=true revision onto a currently
	// DISABLED pipeline must flip pipelines.enabled back on AND dirty +
	// recompute the serve cache so the collector is served the restored
	// content — spec 11 only exercises this transition on its forced-failure
	// path. Red run: skipping the SetPipelineEnabled write (or the dirty +
	// recompute block) in RestoreRevision fails the Enabled assertion (or
	// the Eventually on the serve cache) below.
	It("flips a disabled pipeline back to enabled and serves the restored content when the revision was enabled", func() {
		cluster, err := st.Queries.UpsertCluster(ctx, "restore-reenable-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: orgUUID(orgID)})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())

		createResp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/CreatePipeline", editorCookie, map[string]any{
			"orgId": orgID, "name": "restore-reenable-pipe", "contents": "// REENABLE-MARKER-A\n",
			"matchers": []string{`role="metrics"`},
		})
		Expect(createResp.StatusCode).To(Equal(http.StatusOK))
		var p map[string]any
		Expect(json.NewDecoder(createResp.Body).Decode(&p)).To(Succeed())
		Expect(createResp.Body.Close()).To(Succeed())
		id := pipelineID(p)

		// Enable (no new revision), then save new contents so revision 2 is
		// written with enabled=true, then disable again (no new revision):
		// the pipeline is now disabled while revision 2 says enabled.
		enableResp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/EnablePipeline", editorCookie, map[string]any{
			"orgId": orgID, "id": id,
		})
		Expect(enableResp.StatusCode).To(Equal(http.StatusOK))
		Expect(enableResp.Body.Close()).To(Succeed())
		updateResp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", editorCookie, map[string]any{
			"orgId": orgID, "id": id, "name": "restore-reenable-pipe", "contents": "// REENABLE-MARKER-B\n",
			"matchers": []string{`role="metrics"`},
		})
		Expect(updateResp.StatusCode).To(Equal(http.StatusOK))
		Expect(updateResp.Body.Close()).To(Succeed())
		disableResp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/DisablePipeline", editorCookie, map[string]any{
			"orgId": orgID, "id": id,
		})
		Expect(disableResp.StatusCode).To(Equal(http.StatusOK))
		Expect(disableResp.Body.Close()).To(Succeed())
		Eventually(func() string {
			cache, cacheErr := st.Queries.GetServeCache(ctx, collector.ID)
			if cacheErr != nil {
				return "sentinel: no serve_cache row"
			}
			return cache.Content
		}, "5s", "20ms").ShouldNot(ContainSubstring("REENABLE-MARKER"),
			"the disabled pipeline must be absent from the served config before the restore under test")

		// Restore revision 2 (contents B, enabled=true): a disabled -> enabled
		// transition through the restore path.
		resp := restoreRevision(editorCookie, id, 2, "")
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		Expect(resp.Body.Close()).To(Succeed())

		var pid pgtype.UUID
		Expect(pid.Scan(id)).To(Succeed())
		reloaded, err := st.Queries.GetPipelineByID(ctx, pid)
		Expect(err).NotTo(HaveOccurred())
		Expect(reloaded.Enabled).To(BeTrue(), "restoring an enabled=true revision must flip the pipeline back to enabled")
		Expect(reloaded.Contents).To(Equal("// REENABLE-MARKER-B\n"))

		Eventually(func() string {
			cache, cacheErr := st.Queries.GetServeCache(ctx, collector.ID)
			if cacheErr != nil {
				return ""
			}
			return cache.Content
		}, "10s", "20ms").Should(ContainSubstring("REENABLE-MARKER-B"),
			"restoring a pipeline from disabled to enabled must dirty+recompute the serve cache so "+
				"the collector is served the restored content")
	})

	// 11. Fix-up (backend review, F-REVISIONS): when the enabled-transition
	// Stage 3 check fails, the restore must leave the pipeline row, its
	// revision history, and the audit log completely untouched — no
	// half-restore where contents/matchers/wizard_state were already
	// committed before the transition gate ran. Stage 3 is forced to fail
	// deterministically via an effectively-zero Stage3Timeout on a
	// dedicated server for this spec, rather than a genuine merge content
	// conflict: this harness runs with no alloy binary (Stage 2 is always
	// skipped, matching every other spec in this file) and Stage 1's bare
	// syntax parser does not detect duplicate/colliding component labels
	// across pipelines (that only surfaces once Alloy actually evaluates
	// the merged component graph); a two-pipeline NAME collision, which
	// would trip merge.Assemble's own declare-name-collision check without
	// needing Stage 2 at all, is independently blocked by the
	// pipelines_org_sanitized_name_key DB constraint. What matters for this
	// fix is the WRITE ORDERING — Stage 3 must run, and be allowed to fail,
	// strictly before UpdatePipeline touches the row — and a timeout
	// reaches that same stage3Check failure branch exactly as a real merge
	// conflict would, deterministically and content-independently.
	It("leaves the pipeline row, revision history and audit log untouched when the Stage 3 transition check fails", func() {
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

		createResp := postConnectJSON(tinyServer, "/shepherd.mgmt.v1.PipelineService/CreatePipeline", editorCookie, map[string]any{
			"orgId": orgID, "name": "restore-stage3-timeout", "contents": "// v1\n", "matchers": []string{},
		})
		Expect(createResp.StatusCode).To(Equal(http.StatusOK))
		var p map[string]any
		Expect(json.NewDecoder(createResp.Body).Decode(&p)).To(Succeed())
		Expect(createResp.Body.Close()).To(Succeed())
		id := pipelineID(p)
		var pid pgtype.UUID
		Expect(pid.Scan(id)).To(Succeed())

		// Seed rev2 directly with enabled=true so restoring it is a
		// disabled(false) -> enabled(true) TRANSITION — the path the fix
		// gates on `p.Enabled || targetEnabled`, exercised here via
		// targetEnabled alone (the pipeline itself stays disabled).
		_, err := st.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
			PipelineID: pid, Revision: 2, Contents: "// v2 would-be-restored\n",
			Matchers: json.RawMessage(`[]`), Enabled: true, ChangedBy: "test", ChangeNote: "seeded enabled",
		})
		Expect(err).NotTo(HaveOccurred())

		resp := postConnectJSON(tinyServer, "/shepherd.mgmt.v1.PipelineService/RestoreRevision", editorCookie, map[string]any{
			"orgId": orgID, "id": id, "revision": 2,
		})
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(connectErrorCode(resp)).To(Equal("failed_precondition"))
		Expect(resp.Body.Close()).To(Succeed())

		reloaded, err := st.Queries.GetPipelineByID(ctx, pid)
		Expect(err).NotTo(HaveOccurred())
		Expect(reloaded.Contents).To(Equal("// v1\n"), "a failed Stage 3 transition check must not touch the pipeline's stored contents")
		Expect(reloaded.Enabled).To(BeFalse(), "a failed Stage 3 transition check must not flip the enabled bit")

		revs, err := st.Queries.ListPipelineRevisions(ctx, pid)
		Expect(err).NotTo(HaveOccurred())
		Expect(revs).To(HaveLen(2), "a failed restore must not write a new revision")

		rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: orgUUID(orgID), Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		for i := range rows {
			if rows[i].Action == "pipeline.restore" {
				Expect(rows[i].ResourceID).NotTo(Equal(id), "a failed restore must not write a pipeline.restore audit row")
			}
		}
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

// REST shim coverage for GetRevision/RestoreRevision (B-6.8): separate
// Describe so it runs on newRESTRouter (the legacy /api surface), not the
// Connect wiring the rest of this file uses.
var _ = Describe("REST shim: pipeline revisions", Label("integration"), func() {
	var (
		ctx          context.Context
		cancel       context.CancelFunc
		st           *store.Store
		server       *httptest.Server
		orgID        string
		editorCookie *http.Cookie
		readerCookie *http.Cookie
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "revisions-rest-org", DisplayName: "Revisions REST Org",
			AdminGroupID:  "revisions-rest-admin-grp",
			EditorGroupID: pgtype.Text{String: "revisions-rest-editor-grp", Valid: true},
			ReaderGroupID: pgtype.Text{String: "revisions-rest-reader-grp", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		editorCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "revisions-rest-editor-grp")}
		readerCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "revisions-rest-reader-grp")}

		cfg := &config.Config{
			Auth: config.AuthConfig{InsecureCookies: true},
			Validate: config.ValidateConfig{
				AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10e9,
			},
		}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRESTRouter(st, authHandler, cfg, nil))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	It("serves GetRevision snake_case with contents and gates RestoreRevision by role", func() {
		createResp := postJSON(server, "/orgs/"+orgID+"/pipelines", map[string]any{
			"name": "rest-revisions-pipe", "contents": "// v1\n", "matchers": []string{},
		}, editorCookie)
		Expect(createResp.StatusCode).To(Equal(http.StatusCreated))
		var created map[string]any
		Expect(json.NewDecoder(createResp.Body).Decode(&created)).To(Succeed())
		Expect(createResp.Body.Close()).To(Succeed())
		id := pipelineID(created)

		getResp := getRequest(server, "/orgs/"+orgID+"/pipelines/"+id+"/revisions/1", editorCookie)
		Expect(getResp.StatusCode).To(Equal(http.StatusOK))
		var rev map[string]any
		Expect(json.NewDecoder(getResp.Body).Decode(&rev)).To(Succeed())
		Expect(getResp.Body.Close()).To(Succeed())
		Expect(rev["contents"]).To(Equal("// v1\n"))

		// Malformed {rev} -> 400 bad_request, for every shape the bounded
		// parser refuses: non-numeric, zero/negative, and a value that does
		// not fit int32 (CodeQL go/incorrect-integer-conversion — the shim
		// parses with ParseInt bitSize 32, never Atoi + narrowing).
		for _, bad := range []string{"not-a-number", "0", "-1", "99999999999"} {
			badResp := getRequest(server, "/orgs/"+orgID+"/pipelines/"+id+"/revisions/"+bad, editorCookie)
			Expect(badResp.Body.Close()).To(Succeed())
			Expect(badResp.StatusCode).To(Equal(http.StatusBadRequest), "rev=%q must be refused as 400", bad)
		}

		// reader -> 403 on restore.
		readerResp := postJSON(server, "/orgs/"+orgID+"/pipelines/"+id+"/revisions/1/restore", nil, readerCookie)
		defer readerResp.Body.Close() //nolint:errcheck // test cleanup
		Expect(readerResp.StatusCode).To(Equal(http.StatusForbidden))

		// editor -> 200, and the revision count grows by one.
		beforeResp := getRequest(server, "/orgs/"+orgID+"/pipelines/"+id+"/revisions", editorCookie)
		var before struct {
			Items []map[string]any `json:"items"`
		}
		Expect(json.NewDecoder(beforeResp.Body).Decode(&before)).To(Succeed())
		Expect(beforeResp.Body.Close()).To(Succeed())

		editorResp := postJSON(server, "/orgs/"+orgID+"/pipelines/"+id+"/revisions/1/restore", nil, editorCookie)
		Expect(editorResp.StatusCode).To(Equal(http.StatusOK))
		Expect(editorResp.Body.Close()).To(Succeed())

		afterResp := getRequest(server, "/orgs/"+orgID+"/pipelines/"+id+"/revisions", editorCookie)
		var after struct {
			Items []map[string]any `json:"items"`
		}
		Expect(json.NewDecoder(afterResp.Body).Decode(&after)).To(Succeed())
		Expect(afterResp.Body.Close()).To(Succeed())
		Expect(after.Items).To(HaveLen(len(before.Items) + 1))
	})

	// Fix-up (backend review, F-REVISIONS): S1 ("ListRevisions stays
	// metadata-only") must hold on the REST shim too, not just the Connect
	// endpoint — writeProtoJSON's EmitUnpopulated marshals every list item's
	// contents/matchers/enabled/wizard_state as its zero value unless the
	// shim strips them the same way pipelineOmitFields already does for
	// Pipeline. GetRevision (singular) must still carry them in full.
	It("keeps REST ListRevisions metadata-only while GET .../revisions/{rev} stays full", func() {
		createResp := postJSON(server, "/orgs/"+orgID+"/pipelines", map[string]any{
			"name": "rest-revisions-omit", "contents": "// v1\n", "matchers": []string{`cluster="prod"`},
		}, editorCookie)
		Expect(createResp.StatusCode).To(Equal(http.StatusCreated))
		var created map[string]any
		Expect(json.NewDecoder(createResp.Body).Decode(&created)).To(Succeed())
		Expect(createResp.Body.Close()).To(Succeed())
		id := pipelineID(created)

		listResp := getRequest(server, "/orgs/"+orgID+"/pipelines/"+id+"/revisions", editorCookie)
		Expect(listResp.StatusCode).To(Equal(http.StatusOK))
		var list struct {
			Items []map[string]any `json:"items"`
		}
		Expect(json.NewDecoder(listResp.Body).Decode(&list)).To(Succeed())
		Expect(listResp.Body.Close()).To(Succeed())
		Expect(list.Items).To(HaveLen(1))
		for _, key := range []string{"contents", "matchers", "enabled", "wizard_state"} {
			_, has := list.Items[0][key]
			Expect(has).To(BeFalse(), "REST ListRevisions must stay metadata-only and never carry %q", key)
		}

		revResp := getRequest(server, "/orgs/"+orgID+"/pipelines/"+id+"/revisions/1", editorCookie)
		Expect(revResp.StatusCode).To(Equal(http.StatusOK))
		var rev map[string]any
		Expect(json.NewDecoder(revResp.Body).Decode(&rev)).To(Succeed())
		Expect(revResp.Body.Close()).To(Succeed())
		Expect(rev["contents"]).To(Equal("// v1\n"))
		matchers, hasMatchers := rev["matchers"].([]any)
		Expect(hasMatchers).To(BeTrue(), "GetRevision must carry matchers")
		Expect(matchers).To(ConsistOf(`cluster="prod"`))

		// The pipeline-detail route the SPA loads attaches the revision list
		// as a NESTED object array ("revisions", not "items"), which the
		// zero-value stripper used to walk past — every nested revision then
		// shipped enabled:false and contents:"" to the client (backend
		// re-check finding). Red run: dropping the "revisions" case from
		// stripZeroEntries fails this with `has "contents"` = true.
		detailResp := getRequest(server, "/orgs/"+orgID+"/pipelines/"+id, editorCookie)
		Expect(detailResp.StatusCode).To(Equal(http.StatusOK))
		var detail struct {
			Revisions []map[string]any `json:"revisions"`
		}
		Expect(json.NewDecoder(detailResp.Body).Decode(&detail)).To(Succeed())
		Expect(detailResp.Body.Close()).To(Succeed())
		Expect(detail.Revisions).To(HaveLen(1))
		Expect(detail.Revisions[0]["revision"]).To(BeEquivalentTo(1))
		for _, key := range []string{"contents", "matchers", "enabled", "wizard_state"} {
			_, has := detail.Revisions[0][key]
			Expect(has).To(BeFalse(), "GET .../pipelines/{id} must not carry zero-valued %q on nested revisions", key)
		}
	})
})
