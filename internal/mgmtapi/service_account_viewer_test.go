package mgmtapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// #362 (review step 7b, R14): a viewer service account gets exactly what an
// org viewer gets. Enforcement reuses the interceptor's generic tier check;
// these specs pin that the two callers stay equal over the whole table, that
// a viewer service account never writes, and that the RPC surface creates
// and lists viewers with no capability.
var _ = Describe("#362: viewer-tier service accounts", Label("integration"), func() {
	const viewerGroup = "saviewer-viewer-group"

	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		server *httptest.Server
		orgID  pgtype.UUID
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "saviewer-org", DisplayName: "SA Viewer Org", AdminGroupID: "saviewer-admin-group",
			ViewerGroupID: pgtype.Text{String: viewerGroup, Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	// The drift guard: every procedure in the interceptor table, decided for
	// an org viewer (OIDC session in the org's viewer group) and for a
	// viewer service account of the same org. A new procedure, or a change to
	// either branch, that lets them differ fails here.
	It("allows a viewer service account exactly the procedures it allows an org viewer", func() {
		org := orgID.String()
		human := &auth.Session{ID: "saviewer-human", UserOID: "saviewer-oid", GroupIDs: []string{viewerGroup}, Source: "oidc"}
		saCtx := mgmtapi.WithServiceAccountForTest(ctx, org, "viewer", "")

		allowed := 0
		for _, proc := range mgmtapi.ProcedureNamesForTest() {
			humanErr := mgmtapi.AuthorizeProcedureForTest(ctx, st, human, org, proc)
			saErr := mgmtapi.AuthorizeProcedureForTest(saCtx, st, nil, org, proc)
			Expect(saErr == nil).To(Equal(humanErr == nil),
				fmt.Sprintf("%s: org viewer err=%v, viewer service account err=%v", proc, humanErr, saErr))
			if saErr == nil {
				allowed++
			}
		}
		// Sanity: the comparison is not vacuous in either direction.
		Expect(allowed).To(BeNumerically(">", 10))
		Expect(allowed).To(BeNumerically("<", len(mgmtapi.ProcedureNamesForTest())))
		Expect(mgmtapi.AuthorizeProcedureForTest(saCtx, st, nil, org,
			"/shepherd.mgmt.v1.ServiceAccountService/ListServiceAccounts")).To(HaveOccurred())
		Expect(mgmtapi.AuthorizeProcedureForTest(saCtx, st, nil, org,
			"/shepherd.mgmt.v1.PipelineService/ListPipelines")).To(Succeed())
	})

	Context("over the wire", func() {
		var (
			secret, id string
			pipelineID string
		)

		BeforeEach(func() {
			p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: orgID, Name: "saviewer-pipeline", Source: "ui", Matchers: json.RawMessage("[]"),
			})
			Expect(err).NotTo(HaveOccurred())
			pipelineID = p.ID.String()

			secret = "saviewer-secret"
			hash := sha256.Sum256([]byte(secret))
			sa, err := st.Queries.CreateServiceAccount(ctx, sqlc.CreateServiceAccountParams{
				OrgID: orgID, Name: "saviewer", Role: "viewer", TokenHash: hash[:], CreatedBy: satierDelegatedPrincipal,
			})
			Expect(err).NotTo(HaveOccurred())
			id = sa.ID.String()
		})

		It("authenticates and reads", func() {
			for _, proc := range []string{"PipelineService/ListPipelines", "PipelineService/GetPipeline"} {
				resp := g12PostConnect(server, "/shepherd.mgmt.v1."+proc,
					map[string]any{"orgId": orgID.String(), "id": pipelineID}, id, secret, "")
				Expect(resp.StatusCode).To(Equal(http.StatusOK), proc)
				g11DecodeBody(resp)
			}
		})

		// The pipeline writes are reqOrgViewer at the interceptor (a team
		// member who is only an org viewer may write a pipeline its team
		// owns), so requireWriteAuthorized is what stops a viewer service
		// account — with a valid on-behalf-of, so capability is the reason.
		It("never writes, even on a procedure its tier reaches", func() {
			for proc, body := range map[string]map[string]any{
				"PipelineService/UpdatePipeline": {"orgId": orgID.String(), "id": pipelineID, "name": "saviewer-pipeline", "contents": "// x"},
				"PipelineService/CreatePipeline": {"orgId": orgID.String(), "name": "saviewer-new", "contents": "// x"},
				"PipelineService/DeletePipeline": {"orgId": orgID.String(), "id": pipelineID},
			} {
				resp := g12PostConnect(server, "/shepherd.mgmt.v1."+proc, body, id, secret, satierDelegatedPrincipal)
				Expect(resp.StatusCode).To(Equal(http.StatusForbidden), proc)
				Expect(g11DecodeBody(resp)["code"]).To(Equal("permission_denied"), proc)
			}
			_, err := st.Queries.GetPipelineByID(ctx, mustUUID(pipelineID))
			Expect(err).NotTo(HaveOccurred(), "the pipeline must survive the refused delete")
		})

		It("reports can_edit=false on a pipeline it reads", func() {
			resp := g12PostConnect(server, "/shepherd.mgmt.v1.PipelineService/GetPipeline",
				map[string]any{"orgId": orgID.String(), "id": pipelineID}, id, secret, "")
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			body := g11DecodeBody(resp)
			Expect(body["canEdit"]).To(BeNil(), "can_edit is false (omitted on the wire) for a viewer")
		})
	})

	Context("CreateServiceAccount and ListServiceAccounts", func() {
		create := func(body map[string]any) *http.Response {
			admin := newAppAdminSession(ctx, st)
			body["orgId"] = orgID.String()
			return postConnectJSON(server, "/shepherd.mgmt.v1.ServiceAccountService/CreateServiceAccount", admin, body)
		}

		It("creates a viewer with no capability and lists it with none", func() {
			resp := create(map[string]any{"name": "saviewer-created", "role": "ROLE_VIEWER"})
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			created := g11DecodeBody(resp)
			Expect(created["role"]).To(Equal("ROLE_VIEWER"))
			Expect(created).NotTo(HaveKey("capability"))
			Expect(created["secret"]).NotTo(BeEmpty())

			admin := newAppAdminSession(ctx, st)
			resp = postConnectJSON(server, "/shepherd.mgmt.v1.ServiceAccountService/ListServiceAccounts", admin,
				map[string]any{"orgId": orgID.String()})
			Expect(resp.StatusCode).To(Equal(http.StatusOK))
			items, ok := g11DecodeBody(resp)["items"].([]any)
			Expect(ok).To(BeTrue())
			Expect(items).To(HaveLen(1))
			item, ok := items[0].(map[string]any)
			Expect(ok).To(BeTrue())
			Expect(item["role"]).To(Equal("ROLE_VIEWER"))
			Expect(item).NotTo(HaveKey("capability"))
			Expect(item["status"]).To(Equal("active"))
		})

		It("refuses a viewer with a capability", func() {
			for _, c := range []string{"propose", "apply", "bogus"} {
				resp := create(map[string]any{"name": "saviewer-cap-" + c, "role": "ROLE_VIEWER", "capability": c})
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest), c)
				Expect(g11DecodeBody(resp)["code"]).To(Equal("invalid_argument"), c)
			}
		})

		It("still refuses an editor or admin without a capability", func() {
			for _, r := range []string{"ROLE_EDITOR", "ROLE_ADMIN"} {
				resp := create(map[string]any{"name": "saviewer-nocap", "role": r})
				Expect(resp.StatusCode).To(Equal(http.StatusBadRequest), r)
				Expect(g11DecodeBody(resp)["code"]).To(Equal("invalid_argument"), r)
			}
		})
	})
})
