package mgmtapi_test

import (
	"context"
	"crypto/sha256"
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

// W3-1 (docs/gateway-tier-plan.md, D3): a service account now carries a
// role TIER ("editor" or "admin", 0018_service_account_role), checked
// against a procedure's requirement in authorizeServiceAccountProcedure
// (rpc_interceptor.go) the same way a human session's role is. Before this,
// authorizeServiceAccountProcedure only checked org membership, so an
// apply-capability service account — regardless of what it was minted to
// do — reached every RoleOrgAdmin procedure (RotateTenantRoute, DeleteTeam,
// AddTeamMember, DeleteCredential, ListAudit...). capability (G12, propose
// vs apply) and role (the tier axis) are orthogonal: role decides WHICH
// procedures a token may reach at all, capability decides whether it may
// write once there.
//
// This suite is deliberately separate from capability_test.go's G12 suite:
// that one holds capability constant (propose vs apply) and varies the
// procedure; this one holds capability constant (apply, so the write
// clears requireWriteAuthorized) and varies ROLE, to isolate the tier axis
// from the capability axis the same way the two are orthogonal in the
// implementation.
var _ = Describe("W3-1: service-account role tier", Label("integration"), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		server *httptest.Server
		orgID  pgtype.UUID
		org2ID pgtype.UUID
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())

		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())

		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "satier-org", DisplayName: "SA Tier Org", AdminGroupID: "satier-admin-group",
			// A tenant identity is required before CreateTenantRoute will
			// mint anything — see rpc_tenant_route_test.go.
			TenantID: pgtype.Text{String: "satier-tenant", Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID

		o2, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "satier-org-2", DisplayName: "SA Tier Org 2", AdminGroupID: "satier-admin-group-2",
		})
		Expect(err).NotTo(HaveOccurred())
		org2ID = o2.ID

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		authHandler := auth.NewLocalAdmin(cfg, st, slog.Default())
		server = httptest.NewServer(newRPCWiringRouter(st, authHandler, cfg))
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	// The service-account gate decides on the headers before the body is
	// read. A request with a bad credential AND a body that is not valid
	// JSON must therefore be answered "unauthenticated" (401), not "invalid
	// argument" (400): the decode that would have produced the 400 never
	// runs for a caller that could not authenticate. Under the previous
	// interceptor wiring the body was decoded first, so this exact request
	// answered 400 — and a credential-guessing caller got the server to
	// unmarshal every payload it sent.
	It("refuses a bad service-account credential before reading the request body", func() {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			server.URL+"/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute",
			strings.NewReader("this is not json"))
		Expect(err).NotTo(HaveOccurred())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		req.SetBasicAuth(orgID.String(), "not-the-secret")
		resp, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		Expect(resp.StatusCode).To(Equal(http.StatusUnauthorized))
		Expect(g11DecodeBody(resp)["code"]).To(Equal("unauthenticated"))
	})

	createTenantRouteBody := func(org string) map[string]any {
		return map[string]any{
			"orgId": org, "kind": "otlp",
			"gatewayMode": "managed", "gatewayName": "satier-gw",
		}
	}

	// This is the core RED case: an apply-capability service account must
	// NOT reach a RoleOrgAdmin procedure just because its org matches and
	// it has apply capability. Before W3-1's interceptor tier check this
	// passed (200), because authorizeServiceAccountProcedure never looked
	// past the org match — see the workstream report for the captured red
	// run against this exact spec, taken before 0018/the proto role
	// field/the interceptor tier check existed (the test as run then used
	// a service account with no role column at all — the assertion is
	// unchanged, only the fixture gained an explicit role since).
	It("refuses an apply-scoped, editor-tier service account on TenantRouteService.CreateTenantRoute", func() {
		secret, id := satierMakeServiceAccount(ctx, st, orgID, "satier-editor-apply", "apply")
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute",
			createTenantRouteBody(orgID.String()), id, secret, satierDelegatedPrincipal)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden),
			"an editor-tier service account (the default) must not reach an org-admin procedure "+
				"just because its capability is apply and its org matches")
		Expect(g11DecodeBody(resp)["code"]).To(Equal("permission_denied"))
	})

	// Positive half of the same case: an admin-tier service account
	// (created explicitly with role=admin, D3's "admin explicit at
	// creation") DOES reach the same procedure. Without this, the RED
	// case above could pass for the wrong reason (e.g. a bug that refuses
	// every service account, tier notwithstanding).
	It("allows an admin-tier, apply-scoped service account on TenantRouteService.CreateTenantRoute", func() {
		secret, id := satierMakeServiceAccountWithRole(ctx, st, orgID, "satier-admin-apply", "apply", "admin")
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute",
			createTenantRouteBody(orgID.String()), id, secret, satierDelegatedPrincipal)
		Expect(resp.StatusCode).To(Equal(http.StatusOK),
			"an admin-tier service account, created explicitly with role=admin, must clear the same "+
				"org-admin procedure an editor-tier one is refused on")
	})

	// Coverage case, already holding before this workstream (a mutation
	// revert proved it — see the workstream report): no service account,
	// at any tier, is ever app-admin.
	It("refuses any service account, even admin-tier, on an app-admin procedure", func() {
		secret, id := satierMakeServiceAccountWithRole(ctx, st, orgID, "satier-admin-listorgs", "propose", "admin")
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.AdminService/ListOrgs",
			map[string]any{}, id, secret, "")
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden),
			"a service account must never reach an app-admin procedure, regardless of its role tier")
		Expect(g11DecodeBody(resp)["code"]).To(Equal("permission_denied"))
	})

	// Coverage case, already holding before this workstream: a service
	// account's org match is checked before (and independently of) its
	// role tier — an admin-tier token minted for one org must not reach a
	// procedure scoped to a different org.
	It("refuses a service account scoped to a different org, even admin-tier", func() {
		secret, id := satierMakeServiceAccountWithRole(ctx, st, orgID, "satier-admin-cross-org", "apply", "admin")
		resp := g12PostConnect(server, "/shepherd.mgmt.v1.TenantRouteService/CreateTenantRoute",
			createTenantRouteBody(org2ID.String()), id, secret, satierDelegatedPrincipal)
		Expect(resp.StatusCode).To(Equal(http.StatusForbidden),
			"an admin-tier service account minted for one org must not reach a procedure scoped to "+
				"another org just because its role would otherwise clear the requirement")
		Expect(g11DecodeBody(resp)["code"]).To(Equal("permission_denied"))
	})

	// D3: role defaults to "editor" when CreateServiceAccountRequest
	// leaves it unset — proven at the RPC boundary (a human app-admin
	// session mints the account; CreateServiceAccount always refuses a
	// machine caller, so this is necessarily a human-session call), not
	// just by reading the migration's DEFAULT back through a direct DB
	// insert.
	It("defaults CreateServiceAccount's role to editor when the request does not set one", func() {
		admin := newAppAdminSession(ctx, st)
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.ServiceAccountService/CreateServiceAccount", admin,
			map[string]any{"orgId": orgID.String(), "name": "satier-default-role", "capability": "propose"})
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		created := g11DecodeBody(resp)
		Expect(created["role"]).To(Equal("editor"), "role must default to editor when the request leaves it unset")
	})

	// D3: "admin explicit at creation" — the positive half of the same
	// contract, proving the field round-trips rather than always
	// silently defaulting.
	It("creates an admin-tier service account when the request explicitly sets role=admin", func() {
		admin := newAppAdminSession(ctx, st)
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.ServiceAccountService/CreateServiceAccount", admin,
			map[string]any{"orgId": orgID.String(), "name": "satier-explicit-admin-role", "capability": "propose", "role": "admin"})
		Expect(resp.StatusCode).To(Equal(http.StatusOK))
		created := g11DecodeBody(resp)
		Expect(created["role"]).To(Equal("admin"), "an explicit role=admin request must be honored, not silently downgraded")
	})

	// A role outside the "editor"/"admin" pair must be rejected the same
	// way an unrecognized capability already is, not silently coerced.
	It("rejects a role outside the editor/admin pair", func() {
		admin := newAppAdminSession(ctx, st)
		resp := postConnectJSON(server, "/shepherd.mgmt.v1.ServiceAccountService/CreateServiceAccount", admin,
			map[string]any{"orgId": orgID.String(), "name": "satier-bad-role", "capability": "propose", "role": "super-admin"})
		Expect(resp.StatusCode).To(Equal(http.StatusBadRequest))
		Expect(g11DecodeBody(resp)["code"]).To(Equal("invalid_argument"))
	})
})

// satierDelegatedPrincipal is the human every fixture service account in
// this suite is created BY, mirroring g12DelegatedPrincipal
// (capability_test.go) — the on-behalf-of header must name this principal
// or requireWriteAuthorized refuses the write before the tier check would
// even matter.
const satierDelegatedPrincipal = "satier-owner@example.com"

// satierMakeServiceAccount creates an editor-tier service account (W3-1's
// default — 0018_service_account_role), mirroring g12MakeServiceAccount
// (capability_test.go) exactly but under this suite's own delegated
// principal.
func satierMakeServiceAccount(ctx context.Context, st *store.Store, orgID pgtype.UUID, name, capability string) (secret, id string) {
	return satierMakeServiceAccountWithRole(ctx, st, orgID, name, capability, "editor")
}

// satierMakeServiceAccountWithRole is satierMakeServiceAccount with an
// explicit role tier, for the admin-tier cases this suite exists to cover.
func satierMakeServiceAccountWithRole(ctx context.Context, st *store.Store, orgID pgtype.UUID, name, capability, role string) (secret, id string) {
	secret = "satier-secret-" + name
	hash := sha256.Sum256([]byte(secret))
	sa, err := st.Queries.CreateServiceAccount(ctx, sqlc.CreateServiceAccountParams{
		OrgID: orgID, Name: name, Capability: capability, Role: role, TokenHash: hash[:], CreatedBy: satierDelegatedPrincipal,
	})
	Expect(err).NotTo(HaveOccurred())
	return secret, sa.ID.String()
}
