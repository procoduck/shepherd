package mgmtapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// remoteWriteTo is a pipeline that writes metrics under tenant (an Alloy
// expression, so a spec can pass a literal or sys.env(...)).
func remoteWriteTo(tenant string) string {
	return `prometheus.remote_write "out" {
  endpoint {
    url     = "https://mimir.example.com/api/v1/push"
    headers = { "X-Scope-OrgID" = ` + tenant + ` }
  }
}
`
}

// Strict tenant mode: an org's destinations, destination bindings, tenant
// routes and pipelines may only use tenants it owns — per org (an app
// admin's SetOrgStrictTenants, refused until nothing breaks the rule) or for
// every org (tenants.require_owned).
var _ = Describe("Strict tenant mode", Label("integration"), func() {
	var (
		ctx      context.Context
		cancel   context.CancelFunc
		st       *store.Store
		server   *httptest.Server
		orgA     pgtype.UUID // owns acme (default) and the prefix acme-
		orgB     pgtype.UUID // owns globex; never strict
		orgC     pgtype.UUID // owns nothing
		appAdmin *http.Cookie
		adminA   *http.Cookie
		adminB   *http.Cookie
		adminC   *http.Cookie
		cfg      *config.Config
	)

	session := func(isAppAdmin bool, groups ...string) *http.Cookie {
		if groups == nil {
			groups = []string{}
		}
		groupsJSON, err := json.Marshal(groups)
		Expect(err).NotTo(HaveOccurred())
		id := fmt.Sprintf("strict-tenants-session-%d", time.Now().UnixNano())
		_, err = st.Queries.CreateSession(ctx, sqlc.CreateSessionParams{
			ID: id, UserOid: "user-" + id, Email: id + "@example.com", DisplayName: "Strict",
			GroupIds: groupsJSON, IsAppAdmin: isAppAdmin,
			ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}, Source: "test",
		})
		Expect(err).NotTo(HaveOccurred())
		return &http.Cookie{Name: "shepherd_session", Value: id}
	}

	start := func() {
		server = httptest.NewServer(newRPCWiringRouter(st, auth.NewLocalAdmin(cfg, st, slog.Default()), cfg))
	}

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 12}, slog.Default())
		Expect(err).NotTo(HaveOccurred())
		mk := func(name string) pgtype.UUID {
			o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: name, DisplayName: name, AdminGroupID: name + "-admins"})
			Expect(err).NotTo(HaveOccurred())
			return o.ID
		}
		orgA, orgB, orgC = mk("strict-a"), mk("strict-b"), mk("strict-c")
		for _, t := range []sqlc.AddOrgTenantParams{
			{OrgID: orgA, Tenant: "acme", Kind: "exact", IsDefault: true, CreatedBy: "test"},
			{OrgID: orgA, Tenant: "acme-", Kind: "prefix", CreatedBy: "test"},
			{OrgID: orgB, Tenant: "globex", Kind: "exact", IsDefault: true, CreatedBy: "test"},
		} {
			_, err := st.Queries.AddOrgTenant(ctx, t)
			Expect(err).NotTo(HaveOccurred())
		}
		cfg = &config.Config{Auth: config.AuthConfig{InsecureCookies: true}}
		cfg.Gateway.Routes.PublicBaseURL = "https://telemetry.example.com"
		appAdmin = session(true)
		adminA = session(false, "strict-a-admins")
		adminB = session(false, "strict-b-admins")
		adminC = session(false, "strict-c-admins")
	})

	AfterEach(func() {
		if server != nil {
			server.Close()
			server = nil
		}
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
	ok := func(code int, out map[string]any) map[string]any {
		GinkgoHelper()
		Expect(code).To(Equal(http.StatusOK), "%v", out)
		return out
	}
	setStrict := func(org pgtype.UUID, on bool) (int, map[string]any) {
		return call(appAdmin, "AdminService/SetOrgStrictTenants", map[string]any{"orgId": org.String(), "strictTenants": on})
	}
	makeStrict := func(org pgtype.UUID) {
		GinkgoHelper()
		_, err := st.Queries.SetOrgStrictTenants(ctx, sqlc.SetOrgStrictTenantsParams{ID: org, StrictTenants: true})
		Expect(err).NotTo(HaveOccurred())
	}
	createDest := func(who *http.Cookie, org pgtype.UUID, name, tenant string) (int, map[string]any) {
		return call(who, "DestinationService/CreateDestination", map[string]any{
			"orgId": org.String(), "name": name, "type": "prometheus", "url": "https://mimir.example.com/api/v1/push",
			"authMode": "none", "tenantId": tenant,
		})
	}
	createPipeline := func(who *http.Cookie, org pgtype.UUID, name, contents string) (int, map[string]any) {
		return call(who, "PipelineService/CreatePipeline", map[string]any{
			"orgId": org.String(), "name": name, "contents": contents, "matchers": []string{},
		})
	}

	Describe("turning it on (SetOrgStrictTenants)", func() {
		BeforeEach(start)

		It("is refused for an org that owns no tenant", func() {
			code, out := setStrict(orgC, true)
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("failed_precondition"))
			Expect(out["message"]).To(ContainSubstring("owns no tenant"))
		})

		It("is an app admin's call, not the org admin's", func() {
			code, out := call(adminA, "AdminService/SetOrgStrictTenants", map[string]any{"orgId": orgA.String(), "strictTenants": true})
			Expect(code).To(Equal(http.StatusForbidden), "%v", out)
			code, out = call(adminA, "AdminService/TenantPreflight", map[string]any{"orgId": orgA.String()})
			Expect(code).To(Equal(http.StatusForbidden), "%v", out)
		})

		It("is refused while a destination, binding or enabled pipeline uses an unowned tenant, listing each, until they are fixed", func() {
			dest := ok(createDest(adminA, orgA, "mimir-free", "free-dest"))
			binding := ok(call(adminA, "DestinationService/CreateDestinationBinding", map[string]any{
				"orgId": orgA.String(), "destinationId": dest["id"], "name": "team-free", "tenantId": "free-binding",
			}))
			pipe := ok(createPipeline(adminA, orgA, "writer", remoteWriteTo(`"free-pipeline"`)))
			ok(call(adminA, "PipelineService/EnablePipeline", map[string]any{"orgId": orgA.String(), "id": pipe["id"]}))
			// A DISABLED pipeline does not block: it is linted when enabled.
			ok(createPipeline(adminA, orgA, "parked", remoteWriteTo(`"free-parked"`)))

			code, out := setStrict(orgA, true)
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("failed_precondition"))
			Expect(out["message"]).To(And(
				ContainSubstring(`destination "mimir-free" uses tenant "free-dest"`),
				ContainSubstring(`destination binding "team-free" uses tenant "free-binding"`),
				ContainSubstring(`enabled pipeline "writer"`), ContainSubstring("free-pipeline"),
				Not(ContainSubstring("parked")),
			))
			org, err := st.Queries.GetOrgByID(ctx, orgA)
			Expect(err).NotTo(HaveOccurred())
			Expect(org.StrictTenants).To(BeFalse())

			pre := ok(call(appAdmin, "AdminService/TenantPreflight", map[string]any{"orgId": orgA.String()}))
			Expect(pre["violations"]).To(ConsistOf(
				And(HaveKeyWithValue("kind", "destination"), HaveKeyWithValue("name", "mimir-free"), HaveKeyWithValue("tenant", "free-dest")),
				And(HaveKeyWithValue("kind", "destination binding"), HaveKeyWithValue("name", "team-free")),
				And(HaveKeyWithValue("kind", "pipeline"), HaveKeyWithValue("name", "writer")),
			))

			ok(call(adminA, "DestinationService/UpdateDestination", map[string]any{
				"orgId": orgA.String(), "id": dest["id"], "name": "mimir-free", "type": "prometheus",
				"url": "https://mimir.example.com/api/v1/push", "authMode": "none", "tenantId": "acme-metrics",
			}))
			ok(call(adminA, "DestinationService/UpdateDestinationBinding", map[string]any{
				"orgId": orgA.String(), "id": binding["id"], "name": "team-free", "tenantId": "acme",
			}))
			ok(call(adminA, "PipelineService/DisablePipeline", map[string]any{"orgId": orgA.String(), "id": pipe["id"]}))

			out = ok(setStrict(orgA, true))
			Expect(out).To(And(HaveKeyWithValue("strictTenants", true), HaveKeyWithValue("tenantsRequired", true)))
			rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: orgA, Column3: "org.strict_tenants", Limit: 10})
			Expect(err).NotTo(HaveOccurred())
			Expect(rows).To(HaveLen(1))

			// Enabling a parked violating pipeline is now refused by Stage 3.
			code, out = call(adminA, "PipelineService/EnablePipeline", map[string]any{"orgId": orgA.String(), "id": pipe["id"]})
			Expect(code).NotTo(Equal(http.StatusOK), "%v", out)
			Expect(out["message"]).To(ContainSubstring("strict tenant mode refuses pipeline(s): writer"))

			// And it can always be turned off.
			out = ok(setStrict(orgA, false))
			Expect(out).NotTo(HaveKey("strictTenants"))
		})

		It("reports org members' strict state in GetMe", func() {
			makeStrict(orgA)
			out := ok(call(adminA, "MeService/GetMe", map[string]any{}))
			Expect(out["orgs"]).To(ContainElement(And(HaveKeyWithValue("name", "strict-a"), HaveKeyWithValue("tenantsRequired", true))))
		})
	})

	Describe("in a strict org", func() {
		BeforeEach(func() {
			makeStrict(orgA)
			start()
		})

		DescribeTable("a destination's tenant",
			func(tenant string, allowed bool) {
				code, out := createDest(adminA, orgA, "d", tenant)
				if allowed {
					ok(code, out)
					return
				}
				Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
				Expect(out["code"]).To(Equal("invalid_argument"))
				// Another org's tenant is refused by the cross-org rule first.
				Expect(out["message"]).To(Or(ContainSubstring("strict tenant mode"), Equal("tenant_id is not available to this org")))
			},
			Entry("an owned exact tenant", "acme", true),
			Entry("a tenant under an owned prefix", "acme-eu", true),
			Entry("no tenant at all", "", true),
			Entry("a tenant nobody owns", "free", false),
			Entry("another org's tenant", "globex", false),
		)

		It("refuses a destination update and a binding (create and update) onto an unowned tenant", func() {
			dest := ok(createDest(adminA, orgA, "d", "acme"))
			code, out := call(adminA, "DestinationService/UpdateDestination", map[string]any{
				"orgId": orgA.String(), "id": dest["id"], "name": "d", "type": "prometheus",
				"url": "https://mimir.example.com/api/v1/push", "authMode": "none", "tenantId": "free",
			})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["message"]).To(ContainSubstring("strict tenant mode"))

			code, out = call(adminA, "DestinationService/CreateDestinationBinding", map[string]any{
				"orgId": orgA.String(), "destinationId": dest["id"], "name": "b", "tenantId": "free",
			})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["message"]).To(ContainSubstring("strict tenant mode"))
			b := ok(call(adminA, "DestinationService/CreateDestinationBinding", map[string]any{
				"orgId": orgA.String(), "destinationId": dest["id"], "name": "b", "tenantId": "acme-team",
			}))
			code, out = call(adminA, "DestinationService/UpdateDestinationBinding", map[string]any{
				"orgId": orgA.String(), "id": b["id"], "name": "b", "tenantId": "free",
			})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["message"]).To(ContainSubstring("strict tenant mode"))
		})

		It("gives a tenant route only an owned tenant", func() {
			code, out := call(adminA, "TenantRouteService/CreateTenantRoute", map[string]any{
				"orgId": orgA.String(), "tenant": "free", "kind": "otlp", "gatewayMode": "managed", "gatewayName": "gw",
			})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			ok(call(adminA, "TenantRouteService/CreateTenantRoute", map[string]any{
				"orgId": orgA.String(), "tenant": "acme-eu", "kind": "otlp", "gatewayMode": "managed", "gatewayName": "gw",
			}))
		})

		It("refuses saving a pipeline with a literal unowned X-Scope-OrgID, and accepts an owned one", func() {
			code, out := createPipeline(adminA, orgA, "bad", remoteWriteTo(`"free"`))
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("failed_precondition"))
			Expect(out["message"]).To(Equal("pipeline failed validation"))
			ok(createPipeline(adminA, orgA, "good", remoteWriteTo(`"acme-prod"`)))

			// UpdatePipeline goes through the same gate.
			good, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: orgA, Name: "good"})
			Expect(err).NotTo(HaveOccurred())
			code, out = call(adminA, "PipelineService/UpdatePipeline", map[string]any{
				"orgId": orgA.String(), "id": good.ID.String(), "name": "good", "contents": remoteWriteTo(`"globex"`), "matchers": []string{},
			})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
		})

		It("reports the lint in ValidatePipeline: an error for an unowned literal and for sys.env", func() {
			out := ok(call(adminA, "PipelineService/ValidatePipeline", map[string]any{"orgId": orgA.String(), "contents": remoteWriteTo(`"free"`)}))
			Expect(out).NotTo(HaveKey("valid"))
			Expect(out["diagnostics"]).To(ContainElement(And(HaveKeyWithValue("line", BeNumerically("==", 4)),
				HaveKeyWithValue("message", ContainSubstring(`"free"`)), Not(HaveKey("warning")))))

			out = ok(call(adminA, "PipelineService/ValidatePipeline", map[string]any{"orgId": orgA.String(), "contents": remoteWriteTo(`sys.env("TENANT")`)}))
			Expect(out).NotTo(HaveKey("valid"))
			Expect(out["diagnostics"]).To(ConsistOf(HaveKeyWithValue("message", ContainSubstring("cannot be verified"))))
			code, out := createPipeline(adminA, orgA, "env", remoteWriteTo(`sys.env("TENANT")`))
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
		})

		// The render-time re-check: removing a tenant is guarded while a
		// destination uses it, so this takes it away directly to prove the
		// render refuses on its own.
		It("refuses rendering, committing and re-rendering a wizard destination whose tenant the org no longer owns", func() {
			ok(createDest(adminA, orgA, "mimir", "acme-eu"))
			commit := func() (int, map[string]any) {
				return call(adminA, "WizardService/CommitWizard", map[string]any{
					"orgId": orgA.String(), "kind": "self-monitoring", "name": fmt.Sprintf("self-%d", time.Now().UnixNano()),
					"state": map[string]any{"metrics_dest_name": "mimir", "logs_enabled": false, "role": "singleton"},
				})
			}
			ok(commit())

			_, err := st.Queries.DeleteOrgTenant(ctx, sqlc.DeleteOrgTenantParams{OrgID: orgA, Tenant: "acme-", Kind: "prefix"})
			Expect(err).NotTo(HaveOccurred())

			code, out := commit()
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["message"]).To(ContainSubstring("strict tenant mode"))
			code, out = call(adminA, "WizardService/RenderWizard", map[string]any{
				"orgId": orgA.String(), "kind": "self-monitoring",
				"state": map[string]any{"metrics_dest_name": "mimir", "logs_enabled": false, "role": "singleton"},
			})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["message"]).To(ContainSubstring("strict tenant mode"))

			// `shepherd admin rerender-destinations --all` reports the
			// pipeline instead of regenerating it.
			results, err := mgmtapi.RerenderAllWizardPipelines(ctx, st, validate.New(&cfg.Validate), embeddedSchema(), slog.Default(), true)
			Expect(err).NotTo(HaveOccurred())
			Expect(fmt.Sprint(results)).To(ContainSubstring("strict tenant mode"))
		})
	})

	// Review fixes (PR #388): ownership changes see enabled pipelines.
	Describe("an ownership change and enabled pipelines", func() {
		BeforeEach(start)

		It("refuses removing the tenant a strict org's enabled pipeline writes to, naming it", func() {
			p := ok(createPipeline(adminA, orgA, "writer", remoteWriteTo(`"acme-logs"`)))
			ok(call(adminA, "PipelineService/EnablePipeline", map[string]any{"orgId": orgA.String(), "id": p["id"]}))
			makeStrict(orgA)
			code, out := call(appAdmin, "AdminService/RemoveOrgTenant", map[string]any{
				"orgId": orgA.String(), "tenant": "acme-", "kind": "ORG_TENANT_KIND_PREFIX",
			})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("failed_precondition"))
			Expect(out["message"]).To(ContainSubstring(`pipeline "writer"`))
		})

		It("refuses giving an org a tenant another org's enabled pipeline writes to", func() {
			p := ok(createPipeline(adminB, orgB, "w", remoteWriteTo(`"shared-x"`)))
			ok(call(adminB, "PipelineService/EnablePipeline", map[string]any{"orgId": orgB.String(), "id": p["id"]}))
			code, out := call(appAdmin, "AdminService/AddOrgTenant", map[string]any{
				"orgId": orgA.String(), "tenant": "shared-", "kind": "ORG_TENANT_KIND_PREFIX",
			})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("failed_precondition"))
			Expect(out["message"]).To(ContainSubstring("pipeline"))
			ok(call(appAdmin, "AdminService/AddOrgTenant", map[string]any{
				"orgId": orgA.String(), "tenant": "unused-", "kind": "ORG_TENANT_KIND_PREFIX",
			}))

			// Disabling the pipeline clears its recorded tenants (pipeline_tenants).
			ok(call(adminB, "PipelineService/DisablePipeline", map[string]any{"orgId": orgB.String(), "id": p["id"]}))
			ok(call(appAdmin, "AdminService/AddOrgTenant", map[string]any{
				"orgId": orgA.String(), "tenant": "shared-", "kind": "ORG_TENANT_KIND_PREFIX",
			}))
		})
	})

	// Fourth review (PR #388): request and contents caps, and AddOrgTenant
	// warns about what its check cannot see.
	Describe("limits", func() {
		BeforeEach(start)

		It("refuses pipeline contents over the cap", func() {
			code, out := createPipeline(adminA, orgA, "huge", "// "+strings.Repeat("x", mgmtapi.MaxPipelineContentsBytes))
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("invalid_argument"))
			Expect(out["message"]).To(ContainSubstring("bytes"))
		})

		// Fifth review: deep nesting overflowed the Alloy parser's stack — a
		// fatal crash — on every save path; it is now a Stage 1 refusal for
		// every org.
		It("refuses deeply nested contents at Stage 1, for any org", func() {
			deep := "x = " + strings.Repeat("[", 487_000) + strings.Repeat("]", 487_000) + "\n"
			code, out := createPipeline(adminB, orgB, "deep", deep)
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["message"]).To(Equal("pipeline failed validation"))
			resp := ok(call(adminB, "PipelineService/ValidatePipeline", map[string]any{"orgId": orgB.String(), "contents": deep}))
			Expect(resp["diagnostics"]).To(ConsistOf(HaveKeyWithValue("message", ContainSubstring("nesting deeper than"))))
		})

		It("answers a tenant check that ran out of time as busy (retryable), not a refusal", func() {
			makeStrict(orgA)
			restore := validate.SetTenantLintDeadline(0)
			defer restore()
			code, out := createPipeline(adminA, orgA, "timed", remoteWriteTo(`"acme"`))
			Expect(code).To(Equal(http.StatusServiceUnavailable), "%v", out)
			Expect(out["code"]).To(Equal("unavailable"))
			Expect(out["message"]).To(ContainSubstring("busy"))
			restore()
			ok(createPipeline(adminA, orgA, "timed", remoteWriteTo(`"acme"`)))
		})

		It("refuses a request body over the management API's read cap", func() {
			code, out := createPipeline(adminA, orgA, "huger", "// "+strings.Repeat("x", mgmtapi.MaxRequestBytes))
			Expect(code).NotTo(Equal(http.StatusOK), "%v", out)
			Expect(out["code"]).To(Equal("resource_exhausted"))
		})

		It("warns the app admin about other orgs' pipelines the assignment check cannot read", func() {
			p := ok(createPipeline(adminB, orgB, "computed", remoteWriteTo(`sys.env("T")`)))
			ok(call(adminB, "PipelineService/EnablePipeline", map[string]any{"orgId": orgB.String(), "id": p["id"]}))
			out := ok(call(appAdmin, "AdminService/AddOrgTenant", map[string]any{
				"orgId": orgA.String(), "tenant": "new-", "kind": "ORG_TENANT_KIND_PREFIX",
			}))
			Expect(out["warnings"]).To(ConsistOf(And(ContainSubstring("strict-b"), ContainSubstring("1"))))
		})
	})

	// The toggle's preflight and a pipeline write must serialise on the
	// ownership lock: otherwise the write reads a non-strict policy, the toggle
	// reads the old text, and both commit — an enabled violating pipeline in a
	// strict org.
	It("never ends with a strict org serving a pipeline that writes to an unowned tenant (race)", func() {
		start()
		p := ok(createPipeline(adminA, orgA, "racer", remoteWriteTo(`"acme"`)))
		id := p["id"]
		ok(call(adminA, "PipelineService/EnablePipeline", map[string]any{"orgId": orgA.String(), "id": id}))
		for round := range 25 {
			_, err := st.Queries.SetOrgStrictTenants(ctx, sqlc.SetOrgStrictTenantsParams{ID: orgA, StrictTenants: false})
			Expect(err).NotTo(HaveOccurred())
			ok(call(adminA, "PipelineService/UpdatePipeline", map[string]any{
				"orgId": orgA.String(), "id": id, "name": "racer", "contents": remoteWriteTo(`"acme"`), "matchers": []string{},
			}))
			var wg sync.WaitGroup
			wg.Go(func() {
				defer GinkgoRecover()
				setStrict(orgA, true)
			})
			wg.Go(func() {
				defer GinkgoRecover()
				call(adminA, "PipelineService/UpdatePipeline", map[string]any{
					"orgId": orgA.String(), "id": id, "name": "racer",
					"contents": remoteWriteTo(fmt.Sprintf(`"free-%d"`, round)), "matchers": []string{},
				})
			})
			wg.Wait()
			org, err := st.Queries.GetOrgByID(ctx, orgA)
			Expect(err).NotTo(HaveOccurred())
			pl, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: orgA, Name: "racer"})
			Expect(err).NotTo(HaveOccurred())
			Expect(org.StrictTenants && pl.Enabled && strings.Contains(pl.Contents, "free-")).To(BeFalse(),
				"round %d: strict org with an enabled pipeline writing to an unowned tenant", round)
		}
	})

	Describe("an org not in strict mode (unchanged)", func() {
		BeforeEach(start)

		It("still sends any tenant no other org owns, and saves pipelines that write to one", func() {
			ok(createDest(adminB, orgB, "d", "free"))
			ok(createPipeline(adminB, orgB, "w", remoteWriteTo(`"free"`)))
			out := ok(call(adminB, "PipelineService/ValidatePipeline", map[string]any{"orgId": orgB.String(), "contents": remoteWriteTo(`sys.env("T")`)}))
			Expect(out).To(HaveKeyWithValue("valid", true))
			Expect(out).NotTo(HaveKey("diagnostics"))
		})
	})

	Describe("the server-wide tenants.require_owned setting", func() {
		BeforeEach(func() {
			cfg.Tenants.RequireOwned = true
			start()
		})

		It("applies the rule to every org: an org with no tenants cannot use one, but needs none", func() {
			code, out := createDest(adminC, orgC, "d", "free")
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["message"]).To(ContainSubstring("strict tenant mode"))
			ok(createDest(adminC, orgC, "plain", ""))
			code, out = createPipeline(adminC, orgC, "w", remoteWriteTo(`"free"`))
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)

			code, out = createDest(adminB, orgB, "d", "free")
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			ok(createDest(adminB, orgB, "own", "globex"))

			orgs := ok(call(appAdmin, "AdminService/ListOrgs", map[string]any{}))
			Expect(orgs["items"]).To(ContainElement(And(HaveKeyWithValue("name", "strict-b"),
				HaveKeyWithValue("tenantsRequired", true), Not(HaveKey("strictTenants")))))
		})

		It("lists what would block it across every org in TenantPreflight", func() {
			_, err := st.Queries.CreateDestination(ctx, sqlc.CreateDestinationParams{
				OrgID: orgC, Name: "legacy", Type: "prometheus", Url: "https://m.example.com", AuthMode: "none",
				TenantID: "free", Extra: json.RawMessage(`{}`),
			})
			Expect(err).NotTo(HaveOccurred())
			out := ok(call(appAdmin, "AdminService/TenantPreflight", map[string]any{}))
			Expect(out["violations"]).To(ConsistOf(And(HaveKeyWithValue("orgName", "strict-c"),
				HaveKeyWithValue("kind", "destination"), HaveKeyWithValue("name", "legacy"))))
		})
	})
})
