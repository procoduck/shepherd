package mgmtapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// G12 (docs/gateway-tier-plan.md): "A propose-scoped token cannot apply —
// asserted per write path, not at one middleware chokepoint." This suite
// deliberately calls SEVERAL DIFFERENT procedures across several different
// services (PipelineService, DestinationService, TeamService) — not one
// representative RPC — because the whole point of "per write path" is that
// no single call proves the system; see capability_enumeration_test.go for
// the complementary classification-completeness guarantee, and this file's
// red-run case (in the suite's own It, gated behind ginkgo focus during
// development — see the workstream report for the captured red-run
// transcript) for proof requireWriteAuthorized is what actually blocks it,
// not the coarser org-reader gate PipelineService's writes now share with
// its reads.
var _ = Describe("G12: service-account capability scoping (propose vs apply)", Label("integration"), func() {
	var (
		ctx           context.Context
		cancel        context.CancelFunc
		st            *store.Store
		server        *httptest.Server
		orgID         pgtype.UUID
		proposeSecret string
		applySecret   string
		proposeID     string
		applyID       string
		pipelineID    string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "g12-org", DisplayName: "G12 Org", AdminGroupID: "g12-admin-group"})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID

		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: orgID, Name: "g12-pipeline", Source: "ui", Matchers: json.RawMessage("[]"),
		})
		Expect(err).NotTo(HaveOccurred())
		pipelineID = p.ID.String()

		proposeSecret, proposeID = g12MakeServiceAccount(ctx, st, orgID, "g12-propose", "propose")
		applySecret, applyID = g12MakeServiceAccount(ctx, st, orgID, "g12-apply", "apply")

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	// Each case below is a DIFFERENT write path in a DIFFERENT service —
	// PipelineService.UpdatePipeline, DestinationService.CreateDestination,
	// TeamService.CreateTeam — proving requireWriteAuthorized's per-handler
	// calls, not a shared chokepoint that happens to run once.
	It("refuses a propose-scoped token on PipelineService.UpdatePipeline", func() {
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"orgId": orgID.String(), "id": pipelineID, "name": "g12-pipeline", "contents": "// x",
		}, proposeID, proposeSecret, g12DelegatedPrincipal)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		Expect(g11DecodeBody(resp)["code"]).To(Equal("permission_denied"))
	})

	It("refuses a propose-scoped token on DestinationService.CreateDestination", func() {
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.DestinationService/CreateDestination", map[string]any{
			"orgId": orgID.String(), "name": "g12-dest-2", "type": "prometheus", "url": "http://prom2", "authMode": "none",
		}, proposeID, proposeSecret, g12DelegatedPrincipal)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		Expect(g11DecodeBody(resp)["code"]).To(Equal("permission_denied"))
	})

	It("refuses a propose-scoped token on TeamService.CreateTeam", func() {
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.TeamService/CreateTeam", map[string]any{
			"orgId": orgID.String(), "name": "g12-team", "idpGroupId": "g12-group",
		}, proposeID, proposeSecret, g12DelegatedPrincipal)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden))
		Expect(g11DecodeBody(resp)["code"]).To(Equal("permission_denied"))
	})

	It("lets an apply-scoped token (with on-behalf-of) reach the same write paths", func() {
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.PipelineService/UpdatePipeline", map[string]any{
			"orgId": orgID.String(), "id": pipelineID, "name": "g12-pipeline", "contents": "// applied",
		}, applyID, applySecret, g12DelegatedPrincipal)
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})

	It("lets a propose-scoped token reach READ paths freely (capability only gates writes)", func() {
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.PipelineService/ListPipelines", map[string]any{
			"orgId": orgID.String(),
		}, proposeID, proposeSecret, "")
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
	})
})

// g12DelegatedPrincipal is the human every test service account is created
// BY, and therefore the only principal it may act on behalf of. The tests
// originally passed an arbitrary literal here and asserted the write
// succeeded — which is precisely the impersonation the on-behalf-of check
// now prevents, so the fixture has to model a real delegation instead of a
// claimed one.
const g12DelegatedPrincipal = "someone@example.com"

// g12MakeServiceAccount always creates an editor-tier account (W3-1's
// default — see 0018_service_account_role) because this suite's whole
// point is to isolate the CAPABILITY axis (propose vs apply): every
// procedure this file calls (PipelineService.UpdatePipeline,
// DestinationService.CreateDestination, TeamService.CreateTeam,
// PipelineService.ListPipelines) is RoleOrgReader, which editor tier
// clears trivially, so varying role here would only add noise to a suite
// about the orthogonal axis. See service_account_tier_test.go for the
// tier-specific coverage TeamService.CreateTeam's propose-vs-apply case
// does NOT — TeamService writes are RoleOrgAdmin, so an editor-tier token
// used to be refused there for the WRONG reason before W3-1 (capability
// only; org-admin procedures were unchecked for tier at all) and is
// refused for the RIGHT reason now (capability AND tier both gate it) —
// the capability-scoped "propose" cases below stay 403 either way, so
// nothing here needed to change to keep asserting that.
func g12MakeServiceAccount(ctx context.Context, st *store.Store, orgID pgtype.UUID, name, capability string) (secret, id string) {
	secret = "g12-secret-" + name
	hash := sha256.Sum256([]byte(secret))
	sa, err := st.Queries.CreateServiceAccount(ctx, sqlc.CreateServiceAccountParams{
		OrgID: orgID, Name: name, Capability: capability, Role: "editor", TokenHash: hash[:], CreatedBy: g12DelegatedPrincipal,
	})
	Expect(err).NotTo(HaveOccurred())
	return secret, sa.ID.String()
}

func g12PostConnect(server *httptest.Server, procedure string, body map[string]any, id, secret, onBehalfOf string) *http.Response {
	buf, err := json.Marshal(body)
	Expect(err).NotTo(HaveOccurred())
	req, err := http.NewRequest(http.MethodPost, server.URL+procedure, strings.NewReader(string(buf)))
	Expect(err).NotTo(HaveOccurred())
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.SetBasicAuth(id, secret)
	if onBehalfOf != "" {
		req.Header.Set("Shepherd-On-Behalf-Of", onBehalfOf)
	}
	resp, err := http.DefaultClient.Do(req)
	Expect(err).NotTo(HaveOccurred())
	return resp
}

// R6 (docs/gateway-tier-plan.md §7): a per-service-account request rate limit,
// keyed on the credential id, enforced in the auth gate. Its own server so the
// limit can be set low; the rest of the suite runs with the limiter disabled
// (a directly-built config leaves the rate at 0), so this is the one place the
// limiter is exercised end to end.
var _ = Describe("Service-account rate limit (R6)", Label("integration"), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		server *httptest.Server
		orgID  pgtype.UUID
		id     string
		secret string
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "rl-org", DisplayName: "RL Org", AdminGroupID: "rl-admin"})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID
		secret, id = g12MakeServiceAccount(ctx, st, orgID, "rl-propose", "propose")

		// Rate 1/s, burst 2: two immediate requests pass, the third is refused.
		cfg := &config.Config{Auth: config.AuthConfig{
			InsecureCookies:         true,
			ServiceAccountRateLimit: 1,
			ServiceAccountRateBurst: 2,
		}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	list := func() int {
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.PipelineService/ListPipelines", map[string]any{
			"orgId": orgID.String(),
		}, id, secret, "")
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		return resp.StatusCode
	}

	It("refuses a credential that exceeds its burst with 429/ResourceExhausted", func() {
		Expect(list()).To(Equal(http.StatusOK))
		Expect(list()).To(Equal(http.StatusOK))
		// The third immediate call has drained the bucket.
		Expect(list()).To(Equal(http.StatusTooManyRequests))
	})

	It("does not limit a human session (only Basic-auth machine callers are keyed)", func() {
		// A cookie session never presents Basic auth, so the gate returns early
		// without keying it — a burst of session reads past the SA burst is
		// never refused. Posted to the same Connect procedure the limited SA
		// hit above, but with a session cookie instead of Basic credentials.
		cookie := newAppAdminSession(ctx, st)
		for range 5 {
			buf, _ := json.Marshal(map[string]any{"orgId": orgID.String()}) //nolint:errcheck // test fixture
			req, err := http.NewRequest(http.MethodPost, server.URL+"/shepherd.mgmt.v1.PipelineService/ListPipelines", strings.NewReader(string(buf)))
			Expect(err).NotTo(HaveOccurred())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Requested-With", "XMLHttpRequest")
			req.AddCookie(cookie)
			resp, err := http.DefaultClient.Do(req)
			Expect(err).NotTo(HaveOccurred())
			resp.Body.Close() //nolint:errcheck // test cleanup
			Expect(resp.StatusCode).NotTo(Equal(http.StatusTooManyRequests))
		}
	})
})
