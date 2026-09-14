package mgmtapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// S4 (docs/plans/2026-09-14-walkthrough-fixes.md): CommitWizard skipped both
// of PipelineService.CreatePipeline's side effects (a revision-1 row, a
// pipeline.create audit row) and its Stage 1/2 validation gate — a wizard
// commit went straight to the CreatePipeline store query with none of the
// checks or bookkeeping the editor's own create path performs. This file
// covers the parity fix; the fixture mirrors rpc_wizard_test.go's.
var _ = Describe("WizardService.CommitWizard editor parity", Label("integration"), func() {
	var (
		ctx         context.Context
		cancel      context.CancelFunc
		st          *store.Store
		server      *httptest.Server
		orgID       string
		adminCookie *http.Cookie
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "wizard-commit-org", DisplayName: "Wizard Commit Org",
			AdminGroupID: "wizard-commit-admin-grp",
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID.String()

		adminCookie = &http.Cookie{Name: "shepherd_session", Value: newTestSession(ctx, st, "wizard-commit-admin-grp")}

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	commitWizard := func(name string, state map[string]any) *http.Response {
		return postConnectJSON(server, "/shepherd.mgmt.v1.WizardService/CommitWizard", adminCookie, map[string]any{
			"org_id": orgID,
			"kind":   "app-observability",
			"name":   name,
			"state":  state,
		})
	}

	validState := map[string]any{
		"scrape_url":        "http://myapp:9090/metrics",
		"metrics_dest_name": "mimir",
	}

	// (a) CommitWizard writes a revision-1 "created" row, exactly as
	// CreatePipeline does (createRevision, rpc_pipeline.go).
	It("writes a revision-1 row when it commits a pipeline", func() {
		resp := commitWizard("wizard-commit-revision", validState)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var pipeline map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&pipeline)).To(Succeed())
		Expect(resp.Body.Close()).To(Succeed())
		pid := pipelineID(pipeline)

		revs, err := st.Queries.ListPipelineRevisions(ctx, orgUUID(pid))
		Expect(err).NotTo(HaveOccurred())
		Expect(revs).To(HaveLen(1))
		Expect(revs[0].Revision).To(Equal(int32(1)))
		Expect(revs[0].ChangeNote).To(Equal("created"))
		Expect(revs[0].Contents).To(Equal(pipeline["contents"]))
	})

	// (b) CommitWizard writes a pipeline.create audit row, exactly as
	// CreatePipeline does (auditLog, rpc_pipeline.go / helpers.go).
	It("writes a pipeline.create audit row when it commits a pipeline", func() {
		resp := commitWizard("wizard-commit-audit", validState)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		var pipeline map[string]any
		Expect(json.NewDecoder(resp.Body).Decode(&pipeline)).To(Succeed())
		Expect(resp.Body.Close()).To(Succeed())
		pid := pipelineID(pipeline)

		rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: orgUUID(orgID), Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		var found *sqlc.AuditLog
		for i := range rows {
			if rows[i].Action == "pipeline.create" && rows[i].ResourceID == pid {
				found = &rows[i]
				break
			}
		}
		Expect(found).NotTo(BeNil(), "expected a pipeline.create audit row for %s", pid)
	})

	// (c) CommitWizard now runs the same Stage 1/2 gate CreatePipeline runs
	// (validateSaveInput's Stages12 call, rpc_pipeline.go) — a wizard state
	// that renders unparsable Alloy must not reach the store at all. The
	// quote-in-scrape_url state is the one rpc_wizard_test.go's
	// "surfaces stage-1 syntax diagnostics from RenderWizard" spec uses to
	// break Stage 1.
	It("refuses to commit a pipeline whose rendered contents fail stage 1/2", func() {
		badState := map[string]any{
			"scrape_url":        `http://myapp:9090/metrics" broken = "x`,
			"metrics_dest_name": "mimir",
		}
		resp := commitWizard("wizard-commit-invalid", badState)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		// connect-go's unary JSON protocol maps every non-OK code (including
		// failed_precondition) to HTTP 400 for this transport — the "code"
		// field in the body, not the HTTP status, is what distinguishes
		// them; see the other failed_precondition specs in this package
		// (e.g. rpc_revisions_test.go), none of which assert a 412 status.
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(connectErrorCode(resp)).To(Equal("failed_precondition"))

		listResp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/ListPipelines", adminCookie,
			map[string]any{"org_id": orgID})
		defer listResp.Body.Close() //nolint:errcheck // test cleanup
		var list map[string]any
		Expect(json.NewDecoder(listResp.Body).Decode(&list)).To(Succeed())
		Expect(list["items"]).To(Or(BeNil(), BeEmpty()))
	})

	// (d) Editor parity: PipelineService/CreatePipeline and
	// WizardService/CommitWizard each add exactly one revision and exactly
	// one pipeline.create audit row for the pipeline they create.
	DescribeTable("adds exactly one revision and one pipeline.create row",
		func(create func() string) {
			pid := create()

			revs, err := st.Queries.ListPipelineRevisions(ctx, orgUUID(pid))
			Expect(err).NotTo(HaveOccurred())
			Expect(revs).To(HaveLen(1))

			rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: orgUUID(orgID), Limit: 100})
			Expect(err).NotTo(HaveOccurred())
			count := 0
			for i := range rows {
				if rows[i].Action == "pipeline.create" && rows[i].ResourceID == pid {
					count++
				}
			}
			Expect(count).To(Equal(1))
		},
		Entry("PipelineService/CreatePipeline", func() string {
			resp := postConnectJSON(server, "/shepherd.mgmt.v1.PipelineService/CreatePipeline", adminCookie, map[string]any{
				"orgId": orgID, "name": "parity-editor", "contents": "// v1\n", "matchers": []string{},
			})
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			var p map[string]any
			Expect(json.NewDecoder(resp.Body).Decode(&p)).To(Succeed())
			return pipelineID(p)
		}),
		Entry("WizardService/CommitWizard", func() string {
			resp := commitWizard("parity-wizard", validState)
			defer resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			var p map[string]any
			Expect(json.NewDecoder(resp.Body).Decode(&p)).To(Succeed())
			return pipelineID(p)
		}),
	)
})
