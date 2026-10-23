package mgmtapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// An organisation can own several tenants and tenant prefixes (org_tenants,
// 0039). Only an app admin changes them; a tenant still belongs to at most
// one org (D11), now across exact tenants AND prefixes; a tenant route names
// one the org owns (or gets the default); removal waits until nothing uses
// it; destinations and destination bindings may not send another org's.
var _ = Describe("Org tenants", Label("integration"), func() {
	var (
		ctx      context.Context
		cancel   context.CancelFunc
		st       *store.Store
		server   *httptest.Server
		orgA     pgtype.UUID
		orgB     pgtype.UUID
		appAdmin *http.Cookie
		adminA   *http.Cookie
	)

	session := func(isAppAdmin bool, groups ...string) *http.Cookie {
		if groups == nil {
			groups = []string{}
		}
		groupsJSON, err := json.Marshal(groups)
		Expect(err).NotTo(HaveOccurred())
		id := fmt.Sprintf("org-tenants-session-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: id, UserOid: "user-" + id, Email: id + "@example.com", DisplayName: "Tenants",
			GroupIds: groupsJSON, IsAppAdmin: isAppAdmin,
			ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, Source: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		return &http.Cookie{Name: "shepherd_session", Value: id}
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 12}, slog.Default())
		Expect(err).NotTo(HaveOccurred())
		a, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "tenants-a", DisplayName: "A", AdminGroupID: "tenants-a-admins"})
		Expect(err).NotTo(HaveOccurred())
		b, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "tenants-b", DisplayName: "B", AdminGroupID: "tenants-b-admins"})
		Expect(err).NotTo(HaveOccurred())
		orgA, orgB = a.ID, b.ID

		cfg := &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		cfg.Gateway.Routes.PublicBaseURL = "https://telemetry.example.com"
		cfg.Gateway.Routes.Apply.Enabled = true
		server = httptest.NewServer(newRPCWiringRouter(st, auth.NewLocalAdmin(cfg, st, slog.Default()), cfg))
		appAdmin = session(true)
		adminA = session(false, "tenants-a-admins")
	})

	AfterEach(func() {
		server.Close()
		st.Close()
		cancel()
	})

	call := func(who *http.Cookie, procedure string, body map[string]any) (int, map[string]any) {
		resp := postJSON(server, "/shepherd.mgmt.v1."+procedure, body, who)
		defer resp.Body.Close() //nolint:errcheck // test cleanup
		raw, err := io.ReadAll(resp.Body)
		Expect(err).NotTo(HaveOccurred())
		out := map[string]any{}
		Expect(json.Unmarshal(raw, &out)).To(Succeed(), string(raw))
		return resp.StatusCode, out
	}
	const exact, prefix = "ORG_TENANT_KIND_EXACT", "ORG_TENANT_KIND_PREFIX"
	add := func(org pgtype.UUID, tenant, kind string, makeDefault bool) (int, map[string]any) {
		return call(appAdmin, "AdminService/AddOrgTenant", map[string]any{
			"orgId": org.String(), "tenant": tenant, "kind": kind, "makeDefault": makeDefault,
		})
	}
	mustAdd := func(org pgtype.UUID, tenant, kind string, makeDefault bool) {
		GinkgoHelper()
		code, out := add(org, tenant, kind, makeDefault)
		Expect(code).To(Equal(http.StatusOK), "%v", out)
	}
	remove := func(org pgtype.UUID, tenant, kind string) (int, map[string]any) {
		return call(appAdmin, "AdminService/RemoveOrgTenant", map[string]any{"orgId": org.String(), "tenant": tenant, "kind": kind})
	}
	tenantsOf := func(org pgtype.UUID) []sqlc.OrgTenant {
		rows, err := st.Queries.ListOrgTenants(ctx, org)
		Expect(err).NotTo(HaveOccurred())
		return rows
	}

	Describe("AddOrgTenant, SetDefaultOrgTenant", func() {
		It("gives an org exact tenants and prefixes, switches its default, and audits each change", func() {
			mustAdd(orgA, "acme", exact, true)
			mustAdd(orgA, "acme-eu", exact, false)
			code, out := add(orgA, "acme-team-", prefix, false)
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			Expect(out["tenants"]).To(ConsistOf(
				And(HaveKeyWithValue("tenant", "acme"), HaveKeyWithValue("kind", exact), HaveKeyWithValue("isDefault", true)),
				And(HaveKeyWithValue("tenant", "acme-eu"), HaveKeyWithValue("kind", exact), Not(HaveKey("isDefault"))),
				And(HaveKeyWithValue("tenant", "acme-team-"), HaveKeyWithValue("kind", prefix), Not(HaveKey("isDefault"))),
			))

			code, out = call(appAdmin, "AdminService/SetDefaultOrgTenant", map[string]any{"orgId": orgA.String(), "tenant": "acme-eu"})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			for _, t := range tenantsOf(orgA) {
				Expect(t.IsDefault).To(Equal(t.Tenant == "acme-eu"), "exactly one default, the new one")
			}

			rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: orgA, Limit: 50})
			Expect(err).NotTo(HaveOccurred())
			var actions []string
			for _, r := range rows {
				actions = append(actions, r.Action)
			}
			Expect(actions).To(ContainElements("org.tenant.add", "org.tenant.set_default"))
		})

		It("refuses a prefix as the default, and a default that is not one of the org's exact tenants", func() {
			code, out := add(orgA, "acme-", prefix, true)
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("invalid_argument"))

			mustAdd(orgA, "acme-", prefix, false)
			for _, tenant := range []string{"acme-", "elsewhere"} {
				code, out = call(appAdmin, "AdminService/SetDefaultOrgTenant", map[string]any{"orgId": orgA.String(), "tenant": tenant})
				Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
				Expect(out["code"]).To(Equal("failed_precondition"))
			}
		})

		It("refuses an invalid tenant or prefix", func() {
			for _, req := range [][2]string{{"a/b", exact}, {"__mimir_cluster", exact}, {"a b", prefix}, {"", prefix}, {"acme", "ORG_TENANT_KIND_UNSPECIFIED"}} {
				code, out := add(orgA, req[0], req[1], false)
				Expect(code).To(Equal(http.StatusBadRequest), "%v %v", req, out)
				Expect(out["code"]).To(Equal("invalid_argument"))
			}
		})

		It("creates an org with an initial default tenant", func() {
			code, out := call(appAdmin, "AdminService/CreateOrg", map[string]any{
				"name": "tenants-c", "displayName": "C", "adminGroupId": "c-admins", "tenantId": "initech",
			})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			Expect(out["tenants"]).To(ConsistOf(And(HaveKeyWithValue("tenant", "initech"), HaveKeyWithValue("isDefault", true))))

			code, out = call(appAdmin, "AdminService/CreateOrg", map[string]any{
				"name": "tenants-d", "displayName": "D", "adminGroupId": "d-admins", "tenantId": "initech",
			})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("failed_precondition"))
			_, err := st.Queries.GetOrgByName(ctx, "tenants-d")
			Expect(err).To(HaveOccurred(), "a refused initial tenant rolls the org back too")
		})
	})

	// D11: a tenant belongs to at most one org. With prefixes that is a
	// relation over many tenants, checked both ways.
	DescribeTable("refuses a tenant or prefix that overlaps another org's",
		func(heldTenant, heldKind, newTenant, newKind string) {
			mustAdd(orgA, heldTenant, heldKind, false)
			code, out := add(orgB, newTenant, newKind, false)
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("failed_precondition"))
			Expect(out["message"]).To(ContainSubstring("tenants-a"), "the app admin is told which org holds it")
			Expect(tenantsOf(orgB)).To(BeEmpty())
		},
		Entry("the same exact tenant", "acme", exact, "acme", exact),
		Entry("the same prefix", "acme-", prefix, "acme-", prefix),
		Entry("a prefix under another org's prefix", "acme-", prefix, "acme-team-", prefix),
		Entry("a prefix over another org's prefix", "acme-team-", prefix, "acme-", prefix),
		Entry("a prefix over another org's exact tenant", "globex-1", exact, "globex-", prefix),
		Entry("a prefix equal to another org's exact tenant", "globex-", exact, "globex-", prefix),
		Entry("an exact tenant under another org's prefix", "initech-", prefix, "initech-x", exact),
	)

	It("allows disjoint prefixes and tenants across orgs", func() {
		mustAdd(orgA, "acme-a-", prefix, false)
		mustAdd(orgB, "acme-b-", prefix, false)
		mustAdd(orgB, "acme", exact, false)
	})

	// The overlap rule is checked in Go, so the race between two app admins
	// assigning overlapping prefixes to two orgs at once is closed by the
	// exclusive ownership lock, not by an index. Without it both pass their
	// checks and both commit.
	It("lets only one of two concurrent overlapping assignments through", func() {
		for round := range 5 {
			p := fmt.Sprintf("race%d-", round)
			var wg sync.WaitGroup
			codes := make([]int, 2)
			for i, req := range []struct {
				org    pgtype.UUID
				tenant string
			}{{orgA, p}, {orgB, p + "x-"}} {
				wg.Go(func() {
					defer GinkgoRecover()
					codes[i], _ = add(req.org, req.tenant, prefix, false)
				})
			}
			wg.Wait()
			Expect(codes).To(ConsistOf(http.StatusOK, http.StatusBadRequest), "round %d", round)
		}
	})

	It("keeps tenant changes app-admin only", func() {
		mustAdd(orgA, "acme", exact, true)
		for proc, body := range map[string]map[string]any{
			"AdminService/AddOrgTenant":        {"orgId": orgA.String(), "tenant": "acme-2", "kind": exact},
			"AdminService/RemoveOrgTenant":     {"orgId": orgA.String(), "tenant": "acme", "kind": exact},
			"AdminService/SetDefaultOrgTenant": {"orgId": orgA.String(), "tenant": "acme"},
		} {
			code, out := call(adminA, proc, body)
			Expect(code).To(Equal(http.StatusForbidden), "%s: %v", proc, out)
			Expect(out["code"]).To(Equal("permission_denied"))
		}
		Expect(tenantsOf(orgA)).To(HaveLen(1))
	})

	Describe("tenant routes", func() {
		createRoute := func(tenant string) (int, map[string]any) {
			body := map[string]any{"orgId": orgA.String(), "kind": "otlp", "gatewayMode": "managed", "gatewayName": "shepherd-receiver-gw"}
			if tenant != "" {
				body["tenant"] = tenant
			}
			return call(adminA, "TenantRouteService/CreateTenantRoute", body)
		}

		BeforeEach(func() {
			mustAdd(orgA, "acme", exact, true)
			mustAdd(orgA, "acme-eu", exact, false)
			mustAdd(orgA, "acme-team-", prefix, false)
			mustAdd(orgB, "globex", exact, true)
		})

		DescribeTable("mints a route for a tenant the org owns",
			func(requested, want string) {
				code, out := createRoute(requested)
				Expect(code).To(Equal(http.StatusOK), "%v", out)
				Expect(out["tenantId"]).To(Equal(want))
			},
			Entry("empty: the default", "", "acme"),
			Entry("an exact tenant", "acme-eu", "acme-eu"),
			Entry("a tenant under a prefix", "acme-team-payments", "acme-team-payments"),
		)

		DescribeTable("refuses a tenant the org does not own",
			func(requested string) {
				code, out := createRoute(requested)
				Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
				Expect(out["code"]).To(Equal("invalid_argument"))
				Expect(out["message"]).To(ContainSubstring("not one org"))
			},
			Entry("another org's tenant", "globex"),
			Entry("near the prefix but not under it", "acme-tea"),
			Entry("under the prefix but not a valid tenant", "acme-team-a/b"),
		)
	})

	Describe("RemoveOrgTenant", func() {
		BeforeEach(func() {
			mustAdd(orgA, "acme", exact, true)
			mustAdd(orgA, "acme-eu", exact, false)
			mustAdd(orgA, "acme-team-", prefix, false)
		})

		It("removes an unused tenant or prefix, and audits it", func() {
			code, out := remove(orgA, "acme-eu", exact)
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			code, out = remove(orgA, "acme-team-", prefix)
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			Expect(tenantsOf(orgA)).To(HaveLen(1))
			rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: orgA, Column3: "org.tenant.remove", Limit: 10})
			Expect(err).NotTo(HaveOccurred())
			Expect(rows).To(HaveLen(2))
		})

		It("refuses while a tenant route, a destination or a binding uses it, naming each", func() {
			code, out := call(adminA, "TenantRouteService/CreateTenantRoute", map[string]any{
				"orgId": orgA.String(), "tenant": "acme-team-pay", "kind": "faro", "gatewayMode": "managed", "gatewayName": "gw",
			})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			code, out = call(adminA, "DestinationService/CreateDestination", map[string]any{
				"orgId": orgA.String(), "name": "mimir-eu", "type": "prometheus", "url": "https://mimir.example.com/api/v1/push",
				"authMode": "none", "tenantId": "acme-eu",
			})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			destID, _ := out["id"].(string) //nolint:errcheck // asserted by the binding call
			code, out = call(adminA, "DestinationService/CreateDestinationBinding", map[string]any{
				"orgId": orgA.String(), "destinationId": destID, "name": "team-ops", "tenantId": "acme-team-ops",
			})
			Expect(code).To(Equal(http.StatusOK), "%v", out)

			code, out = remove(orgA, "acme-team-", prefix)
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("failed_precondition"))
			Expect(out["message"]).To(And(ContainSubstring("tenant route"), ContainSubstring("acme-team-pay"),
				ContainSubstring(`destination binding "team-ops"`)))
			code, out = remove(orgA, "acme-eu", exact)
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["message"]).To(ContainSubstring(`destination "mimir-eu"`))
			Expect(tenantsOf(orgA)).To(HaveLen(3), "nothing was removed")
		})

		It("refuses removing the default while the org has live routes, until another is the default", func() {
			code, out := call(adminA, "TenantRouteService/CreateTenantRoute", map[string]any{
				"orgId": orgA.String(), "tenant": "acme-eu", "kind": "otlp", "gatewayMode": "managed", "gatewayName": "gw",
			})
			Expect(code).To(Equal(http.StatusOK), "%v", out)

			code, out = remove(orgA, "acme", exact)
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["message"]).To(ContainSubstring("default"))

			code, out = call(appAdmin, "AdminService/SetDefaultOrgTenant", map[string]any{"orgId": orgA.String(), "tenant": "acme-eu"})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			code, out = remove(orgA, "acme", exact)
			Expect(code).To(Equal(http.StatusOK), "%v", out)
		})

		It("answers not_found for a tenant the org does not have", func() {
			code, out := remove(orgA, "acme-team-", exact)
			Expect(code).To(Equal(http.StatusNotFound), "%v", out)
		})
	})

	// Today's destination rule, now over another org's exact tenants AND
	// prefixes, and the same rule for destination bindings — which until this
	// change checked only the charset.
	Describe("destinations and bindings", func() {
		var destID string

		BeforeEach(func() {
			mustAdd(orgB, "globex", exact, true)
			mustAdd(orgB, "globex-team-", prefix, false)
			code, out := call(adminA, "DestinationService/CreateDestination", map[string]any{
				"orgId": orgA.String(), "name": "mimir", "type": "prometheus", "url": "https://mimir.example.com/api/v1/push", "authMode": "none",
			})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			destID, _ = out["id"].(string) //nolint:errcheck // used by the binding calls
		})

		DescribeTable("refuses a destination tenant another org owns",
			func(tenant string) {
				code, out := call(adminA, "DestinationService/CreateDestination", map[string]any{
					"orgId": orgA.String(), "name": "other", "type": "prometheus", "url": "https://mimir.example.com/api/v1/push",
					"authMode": "none", "tenantId": tenant,
				})
				Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
				Expect(out["message"]).To(Equal("tenant_id is not available to this org"))
			},
			Entry("an exact tenant", "globex"),
			Entry("a tenant under a prefix", "globex-team-a"),
		)

		DescribeTable("refuses a binding tenant another org owns, on create and on update",
			func(tenant string) {
				code, out := call(adminA, "DestinationService/CreateDestinationBinding", map[string]any{
					"orgId": orgA.String(), "destinationId": destID, "name": "team-x", "tenantId": tenant,
				})
				Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
				Expect(out["message"]).To(Equal("tenant_id is not available to this org"))

				code, out = call(adminA, "DestinationService/CreateDestinationBinding", map[string]any{
					"orgId": orgA.String(), "destinationId": destID, "name": "team-y", "tenantId": "free-tenant",
				})
				Expect(code).To(Equal(http.StatusOK), "%v", out)
				code, out = call(adminA, "DestinationService/UpdateDestinationBinding", map[string]any{
					"orgId": orgA.String(), "id": out["id"], "name": "team-y", "tenantId": tenant,
				})
				Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
				Expect(out["message"]).To(Equal("tenant_id is not available to this org"))
			},
			Entry("an exact tenant", "globex"),
			Entry("a tenant under a prefix", "globex-team-a"),
		)

		It("refuses giving an org a prefix covering another org's binding tenant", func() {
			code, out := call(adminA, "DestinationService/CreateDestinationBinding", map[string]any{
				"orgId": orgA.String(), "destinationId": destID, "name": "team-x", "tenantId": "shared-x",
			})
			Expect(code).To(Equal(http.StatusOK), "%v", out)
			code, out = add(orgB, "shared-", prefix, false)
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["message"]).To(ContainSubstring("destination binding"))
		})
	})
})
