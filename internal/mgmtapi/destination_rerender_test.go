package mgmtapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// sha256Hex is the render fingerprint of contents (0030).
func sha256Hex(contents string) string {
	sum := sha256.Sum256([]byte(contents))
	return hex.EncodeToString(sum[:])
}

// #262: a destination's URL and auth are rendered into a wizard pipeline's
// stored contents at commit time, so before this an UpdateDestination reached
// no existing pipeline, and DeleteDestination's in-use check matched a
// `destination_id` key no wizard ever stores — an in-use destination could be
// deleted. Red run (on #260's code): the re-render specs fail on their first
// contents assertion (the old URL is still stored), the rollback specs get a
// 200, and the delete-guard spec deletes the in-use destination.
var _ = Describe("Destination changes reach wizard pipelines (#262)", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		server      *httptest.Server
		org, other  pgtype.UUID
		adminCookie *http.Cookie
	)

	const (
		oldURL = "https://mimir-old.example.com/api/v1/push"
		newURL = "https://mimir-new.example.com/api/v1/push"
	)

	createDest := func(o pgtype.UUID, name, typ, url string) sqlc.Destination {
		d, err := st.Queries.CreateDestination(ctx, sqlc.CreateDestinationParams{
			OrgID: o, Name: name, Type: typ, Url: url, AuthMode: "none", Extra: json.RawMessage(`{}`),
		})
		Expect(err).NotTo(HaveOccurred())
		return d
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 8}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "rerender-org", DisplayName: "Rerender", AdminGroupID: "rerender-admin"})
		Expect(err).NotTo(HaveOccurred())
		org = o.ID
		p, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "rerender-other", DisplayName: "Other", AdminGroupID: "rerender-other-admin"})
		Expect(err).NotTo(HaveOccurred())
		other = p.ID

		// An app admin: the specs act in both orgs.
		adminCookie = newAppAdminSession(ctx, st)
		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		server = httptest.NewServer(newRPCWiringRouter(st, auth.NewLocalAdmin(cfg, st, slog.Default()), cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	call := func(procedure string, body map[string]any) (int, map[string]any) {
		resp := postConnectJSON(server, "/shepherd.mgmt.v1."+procedure, adminCookie, body)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		b, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		var out map[string]any
		Expect(json.Unmarshal(b, &out)).To(Succeed(), string(b))
		return resp.StatusCode, out
	}

	// commitSelfMonitoring commits a self-monitoring wizard pipeline (role
	// singleton) shipping metrics to dest, through the real CommitWizard.
	commitSelfMonitoring := func(o pgtype.UUID, name, dest string) sqlc.Pipeline {
		code, out := call("WizardService/CommitWizard", map[string]any{
			"org_id": o.String(), "kind": "self-monitoring", "name": name,
			"state": map[string]any{"metrics_dest_name": dest, "logs_enabled": false},
		})
		Expect(code).To(Equal(http.StatusOK), "%v", out)
		p, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: o, Name: name})
		Expect(err).NotTo(HaveOccurred())
		return p
	}

	pipeline := func(id pgtype.UUID) sqlc.Pipeline {
		p, err := st.Queries.GetPipelineByID(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return p
	}

	updateDest := func(d sqlc.Destination, fields map[string]any) (int, map[string]any) {
		body := map[string]any{
			"orgId": d.OrgID.String(), "id": d.ID.String(), "name": d.Name, "type": d.Type,
			"url": d.Url, "authMode": d.AuthMode,
		}
		for k, v := range fields {
			body[k] = v
		}
		return call("DestinationService/UpdateDestination", body)
	}

	auditRows := func(o pgtype.UUID, action string) []sqlc.AuditLog {
		rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: o, Column3: action, Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		return rows
	}

	revisions := func(id pgtype.UUID) []sqlc.PipelineRevision {
		revs, err := st.Queries.ListPipelineRevisions(ctx, id)
		Expect(err).NotTo(HaveOccurred())
		return revs
	}

	It("re-renders every wizard pipeline that names the destination: revision, audit row, serve cache — and nothing else", func() {
		mimir := createDest(org, "mimir", "prometheus", oldURL)
		createDest(org, "mimir-dr", "prometheus", "https://dr.example.com/api/v1/push")
		otherMimir := createDest(other, "mimir", "prometheus", oldURL) // same name, other org

		used := commitSelfMonitoring(org, "self-mon", "mimir")
		bystander := commitSelfMonitoring(org, "self-mon-dr", "mimir-dr")
		foreign := commitSelfMonitoring(other, "self-mon-foreign", "mimir")
		Expect(otherMimir.ID).NotTo(Equal(mimir.ID))

		// A singleton collector the enabled pipeline is served to.
		cluster, err := st.Queries.UpsertCluster(ctx, "rerender-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org})).To(Succeed())
		collector, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "singleton"})
		Expect(err).NotTo(HaveOccurred())
		code, out := call("PipelineService/EnablePipeline", map[string]any{"orgId": org.String(), "id": used.ID.String()})
		Expect(code).To(Equal(http.StatusOK), "%v", out)
		Eventually(func() string {
			c, _ := st.Queries.GetServeCache(ctx, collector.ID) //nolint:errcheck // polled
			return c.Content
		}).WithTimeout(10 * time.Second).Should(ContainSubstring(oldURL))
		seqBefore, err := st.Queries.GetServeCache(ctx, collector.ID)
		Expect(err).NotTo(HaveOccurred())

		code, out = updateDest(mimir, map[string]any{
			"url": newURL, "authMode": "basic_secret", "secretNamespace": "monitoring", "secretName": "mimir-creds",
		})
		Expect(code).To(Equal(http.StatusOK), "%v", out)

		got := pipeline(used.ID)
		Expect(got.Contents).To(ContainSubstring(`url  = "` + newURL + `"`))
		Expect(got.Contents).NotTo(ContainSubstring(oldURL))
		Expect(got.Contents).To(ContainSubstring(`remote.kubernetes.secret "metrics_auth"`))
		Expect(got.Contents).To(ContainSubstring(`password = remote.kubernetes.secret.metrics_auth.data["password"]`))
		Expect(got.Enabled).To(BeTrue())

		revs := revisions(used.ID)
		Expect(revs).To(HaveLen(2), "created + re-rendered")
		Expect(revs[0].ChangeNote).To(ContainSubstring("re-rendered"))
		Expect(revs[0].Contents).To(Equal(got.Contents))

		rerenders := auditRows(org, "pipeline.rerender")
		Expect(rerenders).To(HaveLen(1))
		Expect(rerenders[0].ResourceID).To(Equal(used.ID.String()))
		var detail map[string]any
		Expect(json.Unmarshal(rerenders[0].Detail, &detail)).To(Succeed())
		Expect(detail).To(HaveKeyWithValue("destination_name", "mimir"))
		Expect(detail).To(HaveKeyWithValue("destination_id", mimir.ID.String()))
		Expect(detail).To(HaveKeyWithValue("revision", 2.0))
		destUpdates := auditRows(org, "destination.update")
		Expect(destUpdates).To(HaveLen(1))
		Expect(rerenders[0].Actor).To(Equal(destUpdates[0].Actor), "the re-render is attributed to the user who changed the destination")
		Expect(string(destUpdates[0].Detail)).To(ContainSubstring("self-mon"))

		// The consumed layer: what the collector is served.
		Eventually(func() string {
			c, _ := st.Queries.GetServeCache(ctx, collector.ID) //nolint:errcheck // polled
			return c.Content
		}).WithTimeout(10 * time.Second).Should(ContainSubstring(newURL))
		seqAfter, err := st.Queries.GetServeCache(ctx, collector.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(seqAfter.DirtySeq).To(BeNumerically(">", seqBefore.DirtySeq), "the update marked the serve cache dirty")

		// Untouched: another destination's pipeline, and another org's
		// pipeline naming a destination of the same name.
		Expect(pipeline(bystander.ID).Contents).To(Equal(bystander.Contents))
		Expect(revisions(bystander.ID)).To(HaveLen(1))
		Expect(pipeline(foreign.ID).Contents).To(Equal(foreign.Contents))
		Expect(revisions(foreign.ID)).To(HaveLen(1))
		Expect(auditRows(other, "pipeline.rerender")).To(BeEmpty())
	})

	It("writes no revision when the update does not change what the wizard renders", func() {
		mimir := createDest(org, "mimir", "prometheus", oldURL)
		used := commitSelfMonitoring(org, "self-mon", "mimir")
		code, out := updateDest(mimir, map[string]any{"tenantId": "tenant-a"})
		Expect(code).To(Equal(http.StatusOK), "%v", out)
		Expect(revisions(used.ID)).To(HaveLen(1))
		Expect(auditRows(org, "pipeline.rerender")).To(BeEmpty())
	})

	It("renames: rewrites the name in each pipeline's wizard state as well as its contents", func() {
		mimir := createDest(org, "mimir", "prometheus", oldURL)
		used := commitSelfMonitoring(org, "self-mon", "mimir")
		code, out := updateDest(mimir, map[string]any{"name": "mimir-eu"})
		Expect(code).To(Equal(http.StatusOK), "%v", out)

		got := pipeline(used.ID)
		var state map[string]any
		Expect(json.Unmarshal(got.WizardState, &state)).To(Succeed())
		Expect(state["metrics_dest_name"]).To(Equal("mimir-eu"))
		Expect(got.Contents).To(ContainSubstring(`name = "mimir-eu"`))
		Expect(revisions(used.ID)[0].WizardState).To(MatchJSON(got.WizardState))

		// The renamed destination is still recognised as in use.
		code, out = call("DestinationService/DeleteDestination", map[string]any{"orgId": org.String(), "id": mimir.ID.String()})
		Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
		Expect(out["message"]).To(ContainSubstring("self-mon"))
	})

	Describe("a re-render the gate refuses rolls the whole update back", func() {
		It("refuses a type change the wizard cannot render, naming the pipeline", func() {
			mimir := createDest(org, "mimir", "prometheus", oldURL)
			used := commitSelfMonitoring(org, "self-mon", "mimir")

			code, out := updateDest(mimir, map[string]any{"type": "loki", "url": newURL})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("failed_precondition"))
			Expect(out["message"]).To(ContainSubstring(`"self-mon"`))
			Expect(out["message"]).To(ContainSubstring("needs a prometheus destination"))

			d, err := st.Queries.GetDestinationByID(ctx, mimir.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(d.Type).To(Equal("prometheus"))
			Expect(d.Url).To(Equal(oldURL))
			Expect(pipeline(used.ID).Contents).To(Equal(used.Contents))
			Expect(revisions(used.ID)).To(HaveLen(1))
			Expect(auditRows(org, "pipeline.rerender")).To(BeEmpty())
			Expect(auditRows(org, "destination.update")).To(BeEmpty())
		})

		It("refuses when one pipeline's re-render fails Stage 1, leaving every pipeline as it was", func() {
			mimir := createDest(org, "mimir", "prometheus", oldURL)
			good := commitSelfMonitoring(org, "self-mon", "mimir")
			// A wizard pipeline whose stored state no longer renders to valid
			// Alloy (app-observability puts job_name inside a string
			// literal unquoted; CommitWizard's Stage 1 gate would refuse it
			// today, a row from before that gate need not have been).
			brokenState := map[string]any{
				"scrape_url": "app:8080", "job_name": `app"broken`, "metrics_dest_name": "mimir", "logs_enabled": false,
			}
			state, err := json.Marshal(brokenState)
			Expect(err).NotTo(HaveOccurred())
			// Stored exactly as the wizard renders it (RenderWizard returns
			// the contents even when they fail the gate), so it is not
			// mistaken for a hand edit.
			code, render := call("WizardService/RenderWizard", map[string]any{
				"org_id": org.String(), "kind": "app-observability", "name": "broken-app", "state": brokenState,
			})
			Expect(code).To(Equal(http.StatusOK), "%v", render)
			Expect(render["valid"]).NotTo(Equal(true), "the stored state must render to text that fails the gate")
			storedBroken, _ := render["contents"].(string) //nolint:errcheck // asserted non-empty below
			Expect(storedBroken).NotTo(BeEmpty())
			broken, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: org, Name: "broken-app", Contents: storedBroken, Matchers: json.RawMessage(`[]`),
				Source: "wizard", WizardKind: pgtype.Text{String: "app-observability", Valid: true}, WizardState: state,
			})
			Expect(err).NotTo(HaveOccurred())

			code, out := updateDest(mimir, map[string]any{"url": newURL})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("failed_precondition"))
			Expect(out["message"]).To(ContainSubstring(`"broken-app"`))
			Expect(out["message"]).To(ContainSubstring("stage 1"))
			Expect(out["message"]).NotTo(ContainSubstring("edited by hand"))

			d, err := st.Queries.GetDestinationByID(ctx, mimir.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(d.Url).To(Equal(oldURL))
			Expect(pipeline(good.ID).Contents).To(Equal(good.Contents), "no partial state: the pipeline that did render is not written either")
			Expect(revisions(good.ID)).To(HaveLen(1))
			Expect(pipeline(broken.ID).Contents).To(Equal(storedBroken))
		})
	})

	// Maintainer decision 2026-10-01: a re-render must never overwrite a
	// wizard pipeline whose stored text was edited by hand. Detected by
	// rendering it from its stored state against the destinations as they
	// were BEFORE the update: a difference is a hand edit. Red run: on the
	// first #262 build the update succeeded and replaced the edit.
	// The render fingerprint (0030): sha256 of the exact contents a wizard
	// last wrote. The hand-edit check reads it rather than re-rendering, so a
	// later change to a wizard's template does not make every older pipeline
	// look hand-edited. Red run (on #264): CommitWizard set no fingerprint,
	// and the template-change pipeline was refused as hand-edited.
	Describe("render fingerprint", func() {
		It("is the sha256 of what CommitWizard and a re-render stored", func() {
			mimir := createDest(org, "mimir", "prometheus", oldURL)
			used := commitSelfMonitoring(org, "self-mon", "mimir")
			Expect(used.WizardRenderSha256.Valid).To(BeTrue())
			Expect(used.WizardRenderSha256.String).To(Equal(sha256Hex(used.Contents)))

			code, out := updateDest(mimir, map[string]any{"url": newURL})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			got := pipeline(used.ID)
			Expect(got.WizardRenderSha256.String).To(Equal(sha256Hex(got.Contents)))
		})

		It("re-renders a pipeline an older wizard template produced: its text matches its fingerprint, not a fresh render", func() {
			mimir := createDest(org, "mimir", "prometheus", oldURL)
			used := commitSelfMonitoring(org, "self-mon", "mimir")
			older := used.Contents + "\n// as an older template rendered it\n"
			_, err := st.Pool().Exec(ctx, `UPDATE pipelines SET contents = $2, wizard_render_sha256 = $3 WHERE id = $1`,
				used.ID, older, sha256Hex(older))
			Expect(err).NotTo(HaveOccurred())

			code, out := updateDest(mimir, map[string]any{"url": newURL})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			got := pipeline(used.ID)
			Expect(got.Contents).To(ContainSubstring(newURL))
			Expect(got.Contents).NotTo(ContainSubstring("older template"))
		})

		It("with no fingerprint (written before 0030), falls back to a fresh render and records one", func() {
			mimir := createDest(org, "mimir", "prometheus", oldURL)
			used := commitSelfMonitoring(org, "self-mon", "mimir")
			_, err := st.Pool().Exec(ctx, `UPDATE pipelines SET wizard_render_sha256 = NULL WHERE id = $1`, used.ID)
			Expect(err).NotTo(HaveOccurred())

			code, out := updateDest(mimir, map[string]any{"url": newURL})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			got := pipeline(used.ID)
			Expect(got.Contents).To(ContainSubstring(newURL))
			Expect(got.WizardRenderSha256.String).To(Equal(sha256Hex(got.Contents)))
		})

		It("with no fingerprint and text that differs from a fresh render, refuses it as hand-edited", func() {
			mimir := createDest(org, "mimir", "prometheus", oldURL)
			used := commitSelfMonitoring(org, "self-mon", "mimir")
			_, err := st.Pool().Exec(ctx, `UPDATE pipelines SET contents = contents || '// edit', wizard_render_sha256 = NULL WHERE id = $1`, used.ID)
			Expect(err).NotTo(HaveOccurred())

			code, out := updateDest(mimir, map[string]any{"url": newURL})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["message"]).To(ContainSubstring("edited by hand"))
		})

		It("survives an editor save that leaves the contents as the wizard wrote them", func() {
			createDest(org, "mimir", "prometheus", oldURL)
			used := commitSelfMonitoring(org, "self-mon", "mimir")
			code, out := call("PipelineService/UpdatePipeline", map[string]any{
				"orgId": org.String(), "id": used.ID.String(), "name": used.Name,
				"contents": used.Contents, "matchers": []string{`role="singleton"`, `cluster=~"prod-.*"`},
			})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			Expect(pipeline(used.ID).WizardRenderSha256).To(Equal(used.WizardRenderSha256),
				"a matchers-only save is not a hand edit of the text")
		})
	})

	Describe("hand-edited wizard pipelines", func() {
		It("refuses the update, naming the pipeline and what to do, and changes nothing", func() {
			mimir := createDest(org, "mimir", "prometheus", oldURL)
			used := commitSelfMonitoring(org, "self-mon", "mimir")
			edited := used.Contents + "\n// tuned by hand\n"
			code, out := call("PipelineService/UpdatePipeline", map[string]any{
				"orgId": org.String(), "id": used.ID.String(), "name": used.Name,
				"contents": edited, "matchers": []string{`role="singleton"`},
			})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			Expect(pipeline(used.ID).Source).To(Equal("wizard"), "an editor save keeps the pipeline a wizard pipeline")
			Expect(pipeline(used.ID).WizardRenderSha256.Valid).To(BeFalse(), "an editor edit clears the render fingerprint")

			code, out = updateDest(mimir, map[string]any{"url": newURL})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("failed_precondition"))
			Expect(out["message"]).To(ContainSubstring(`"self-mon"`))
			Expect(out["message"]).To(ContainSubstring("edited by hand"))
			Expect(out["message"]).To(ContainSubstring("re-run its wizard"))
			Expect(out["message"]).To(ContainSubstring(`"Detach from wizard"`))

			d, err := st.Queries.GetDestinationByID(ctx, mimir.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(d.Url).To(Equal(oldURL))
			Expect(pipeline(used.ID).Contents).To(Equal(edited))
			Expect(revisions(used.ID)).To(HaveLen(2), "created + the hand edit, nothing more")
			Expect(auditRows(org, "pipeline.rerender")).To(BeEmpty())
			Expect(auditRows(org, "destination.update")).To(BeEmpty())
		})

		It("still converts a pre-#260 sys.env pipeline: its text came from the old renderer, not a hand", func() {
			mimir := createDest(org, "mimir", "prometheus", oldURL)
			state, err := json.Marshal(map[string]any{"metrics_dest_name": "mimir", "logs_enabled": false})
			Expect(err).NotTo(HaveOccurred())
			legacy, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: org, Name: "legacy-self-mon", Contents: legacySelfMonitoringContents,
				Matchers: json.RawMessage(`["role=\"singleton\""]`), Source: "wizard",
				WizardKind: pgtype.Text{String: "self-monitoring", Valid: true}, WizardState: state,
			})
			Expect(err).NotTo(HaveOccurred())

			code, out := updateDest(mimir, map[string]any{"url": newURL})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			got := pipeline(legacy.ID)
			Expect(got.Contents).NotTo(ContainSubstring("sys.env"))
			Expect(got.Contents).To(ContainSubstring(`url  = "` + newURL + `"`))
		})
	})

	Describe("DeleteDestination's in-use guard", func() {
		It("refuses a destination a wizard pipeline names, listing the pipelines", func() {
			mimir := createDest(org, "mimir", "prometheus", oldURL)
			commitSelfMonitoring(org, "self-mon-a", "mimir")
			commitSelfMonitoring(org, "self-mon-b", "mimir")

			code, out := call("DestinationService/DeleteDestination", map[string]any{"orgId": org.String(), "id": mimir.ID.String()})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("failed_precondition"))
			Expect(out["message"]).To(ContainSubstring("self-mon-a, self-mon-b"))
			_, err := st.Queries.GetDestinationByID(ctx, mimir.ID)
			Expect(err).NotTo(HaveOccurred(), "the destination must still exist")
		})

		It("matches a logs destination too", func() {
			createDest(org, "mimir", "prometheus", oldURL)
			loki := createDest(org, "loki", "loki", "https://loki.example.com/loki/api/v1/push")
			code, out := call("WizardService/CommitWizard", map[string]any{
				"org_id": org.String(), "kind": "self-monitoring", "name": "with-logs",
				"state": map[string]any{"metrics_dest_name": "mimir", "logs_enabled": true, "log_path": "/var/log/alloy/*.log", "logs_dest_name": "loki"},
			})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			code, out = call("DestinationService/DeleteDestination", map[string]any{"orgId": org.String(), "id": loki.ID.String()})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["message"]).To(ContainSubstring("with-logs"))
		})

		It("deletes a destination no pipeline in its org names, even if another org's pipeline names one of the same name", func() {
			mimir := createDest(org, "mimir", "prometheus", oldURL)
			createDest(other, "mimir", "prometheus", oldURL)
			commitSelfMonitoring(other, "foreign", "mimir")

			code, out := call("DestinationService/DeleteDestination", map[string]any{"orgId": org.String(), "id": mimir.ID.String()})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
		})
	})
})
