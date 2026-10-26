package mgmtapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/auth"
	"shepherd/internal/config"
	"shepherd/internal/crypto"
	"shepherd/internal/mgmtapi"
	"shepherd/internal/pinnedimport"
	"shepherd/internal/serve"
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

		// The serve-time check on the eager path: a pipeline stored before
		// the check it now fails (inserted straight through sqlc, as an
		// older version saved it) is excluded from every collector it
		// matches, named in the header, audited once, shown on the pipeline
		// page and the collector's reconciliation, listed by the preflight —
		// and served again once a fix is saved.
		It("clears the serve-time exclusions, audited once each, when strict mode is turned off by the toggle or at boot", func() {
			computed := "loki.write \"w\" {\n  endpoint {\n    url       = \"https://l.example/push\"\n    tenant_id = \"acme\"\n  }\n" +
				"  external_labels = encoding.from_json(\"{}\")\n}\n"
			old, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: orgA, Name: "old", Contents: computed, Matchers: json.RawMessage(`["cluster=\"off-cluster\""]`),
				Enabled: true, Source: "ui", WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())
			cleared := func() int {
				rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: orgA, Limit: 100})
				Expect(err).NotTo(HaveOccurred())
				n := 0
				for _, r := range rows {
					if r.Action == serve.AuditActionTenantCheckCleared && r.ResourceID == old.ID.String() {
						n++
					}
				}
				return n
			}
			exclude := func() {
				GinkgoHelper()
				_, err := serve.LoadPipelines(ctx, st, orgA, false)
				Expect(err).NotTo(HaveOccurred())
				_, err = st.Queries.GetPipelineTenantExclusion(ctx, old.ID)
				Expect(err).NotTo(HaveOccurred())
			}

			exclude()
			ok(setStrict(orgA, false))
			_, err = st.Queries.GetPipelineTenantExclusion(ctx, old.ID)
			Expect(err).To(HaveOccurred(), "the toggle clears the org's exclusions")
			Expect(cleared()).To(Equal(1))

			// At boot: the flag (or tenants.require_owned) went off without the toggle.
			makeStrict(orgA)
			exclude()
			_, err = st.Queries.SetOrgStrictTenants(ctx, sqlc.SetOrgStrictTenantsParams{ID: orgA, StrictTenants: false})
			Expect(err).NotTo(HaveOccurred())
			Expect(serve.ClearNonStrictTenantExclusions(ctx, st, true)).To(Succeed())
			_, err = st.Queries.GetPipelineTenantExclusion(ctx, old.ID)
			Expect(err).NotTo(HaveOccurred(), "tenants.require_owned still applies the rule")
			Expect(serve.ClearNonStrictTenantExclusions(ctx, st, false)).To(Succeed())
			_, err = st.Queries.GetPipelineTenantExclusion(ctx, old.ID)
			Expect(err).To(HaveOccurred())
			Expect(cleared()).To(Equal(2))
		})

		It("excludes a stored pipeline the serve-time check refuses until a fix is saved", func() {
			computed := "loki.write \"w\" {\n  endpoint {\n    url       = \"https://l.example/push\"\n    tenant_id = \"acme\"\n  }\n" +
				"  external_labels = encoding.from_json(\"{\\\"__tenant_id__\\\":\\\"globex\\\"}\")\n}\n"
			cl, err := st.Queries.UpsertCluster(ctx, "check-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cl.ID, OrgID: orgA})).To(Succeed())
			coll, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cl.ID, Role: "singleton"})
			Expect(err).NotTo(HaveOccurred())
			old, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: orgA, Name: "old", Contents: computed, Matchers: json.RawMessage(`["cluster=\"check-cluster\""]`),
				Enabled: true, Source: "ui", WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())

			ps := mgmtapi.NewPipelineService(st, validate.New(&cfg.Validate), embeddedSchema(), slog.Default())
			ps.RecomputeOrgCaches(ctx, orgA)
			row, err := st.Queries.GetServeCache(ctx, coll.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(row.Content).To(ContainSubstring("// Excluded (1) - strict tenant check:\n//   - old: "))
			Expect(row.Content).To(ContainSubstring("not a literal map"))
			Expect(row.Content).NotTo(ContainSubstring(`declare "pipe_old"`))
			Expect(st.Queries.MarkServeCacheDirtyByOrg(ctx, orgA)).To(Succeed())
			ps.RecomputeOrgCaches(ctx, orgA)
			again, err := st.Queries.GetServeCache(ctx, coll.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(again.Content).To(Equal(row.Content))
			auditCount := func(action string) int {
				rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: orgA, Limit: 100})
				Expect(err).NotTo(HaveOccurred())
				n := 0
				for _, r := range rows {
					if r.Action == action && r.ResourceID == old.ID.String() {
						n++
					}
				}
				return n
			}
			Expect(auditCount(serve.AuditActionTenantCheckExcluded)).To(Equal(1))

			// The pipeline page: every matched collector reads excluded.
			pm := ok(call(adminA, "PipelineService/PreviewMatches", map[string]any{"orgId": orgA.String(), "id": old.ID.String()}))
			Expect(pm["collectors"]).To(ConsistOf(HaveKeyWithValue("excludedReason", ContainSubstring("strict tenant check"))))
			// The collector's reconciliation names it.
			rec := ok(call(adminA, "FleetService/GetReconciliation", map[string]any{"orgId": orgA.String(), "id": coll.ID.String()}))
			Expect(rec["findings"]).To(ContainElement(And(
				HaveKeyWithValue("kind", "strict_tenant_excluded"), HaveKeyWithValue("pipelineName", "old"))))
			// The preflight predicts it.
			pre := ok(call(appAdmin, "AdminService/TenantPreflight", map[string]any{"orgId": orgA.String()}))
			Expect(pre["violations"]).To(ContainElement(And(HaveKeyWithValue("kind", "pipeline"), HaveKeyWithValue("name", "old"))))

			// Saving it unchanged is refused at save, as before; a fix is
			// served.
			code, out := call(adminA, "PipelineService/UpdatePipeline", map[string]any{
				"orgId": orgA.String(), "id": old.ID.String(), "name": "old", "contents": computed, "matchers": []string{`cluster="check-cluster"`},
			})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			fixed := strings.Replace(computed, "encoding.from_json(\"{\\\"__tenant_id__\\\":\\\"globex\\\"}\")", `{ team = "a" }`, 1)
			ok(call(adminA, "PipelineService/UpdatePipeline", map[string]any{
				"orgId": orgA.String(), "id": old.ID.String(), "name": "old", "contents": fixed, "matchers": []string{`cluster="check-cluster"`},
			}))
			Eventually(func() string {
				r, err := st.Queries.GetServeCache(ctx, coll.ID)
				Expect(err).NotTo(HaveOccurred())
				return r.Content
			}).WithTimeout(10 * time.Second).Should(And(ContainSubstring(`declare "pipe_old"`), Not(ContainSubstring("strict tenant check"))))
			Eventually(func() int { return auditCount(serve.AuditActionTenantCheckCleared) }).WithTimeout(10 * time.Second).Should(Equal(1))

			// The same text in an org not in strict mode is served as is.
			_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
				OrgID: orgB, Name: "old", Contents: strings.ReplaceAll(computed, "acme", "globex"), Matchers: json.RawMessage(`["cluster=\"b-cluster\""]`),
				Enabled: true, Source: "ui", WizardState: json.RawMessage(`{}`), CreatedBy: "test", UpdatedBy: "test",
			})
			Expect(err).NotTo(HaveOccurred())
			clB, err := st.Queries.UpsertCluster(ctx, "b-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: clB.ID, OrgID: orgB})).To(Succeed())
			collB, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: clB.ID, Role: "singleton"})
			Expect(err).NotTo(HaveOccurred())
			ps.RecomputeOrgCaches(ctx, orgB)
			rowB, err := st.Queries.GetServeCache(ctx, collB.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(rowB.Content).To(ContainSubstring(`declare "pipe_old"`))
			Expect(rowB.Content).NotTo(ContainSubstring("strict tenant check"))
		})

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
		It("answers a destination update whose re-render's tenant check ran out of time as busy (retryable)", func() {
			dest := ok(createDest(adminA, orgA, "mimir", "acme"))
			ok(call(adminA, "WizardService/CommitWizard", map[string]any{
				"orgId": orgA.String(), "kind": "self-monitoring", "name": "self",
				"state": map[string]any{"metrics_dest_name": "mimir", "logs_enabled": false, "role": "singleton"},
			}))
			restore := validate.SetTenantLintDeadline(0)
			defer restore()
			code, out := call(adminA, "DestinationService/UpdateDestination", map[string]any{
				"orgId": orgA.String(), "id": dest["id"], "name": "mimir", "type": "prometheus",
				"url": "https://mimir2.example.com/api/v1/push", "authMode": "none", "tenantId": "acme",
			})
			Expect(code).To(Equal(http.StatusServiceUnavailable), "%v", out)
			Expect(out["code"]).To(Equal("unavailable"))
		})

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

	// #390: a strict org may import.git a module pinned to a full commit SHA
	// of a repository an app admin allowlisted; the module is fetched and
	// linted. Port 1 on loopback refuses every connection, so the source is
	// "unreachable" and only what is cached can be verified.
	Describe("pinned import.git", func() {
		const (
			repo = "http://127.0.0.1:1/team/modules.git"
			sha  = "0123456789abcdef0123456789abcdef01234567"
		)
		importing := func(rev, path string) string {
			return "import.git \"m\" {\n  repository = \"" + repo + "\"\n  revision   = \"" + rev + "\"\n  path       = \"" + path + "\"\n}\n"
		}
		module := func(tenant string) string {
			return "declare \"out\" {\n" + remoteWriteTo(`"`+tenant+`"`) + "}\n"
		}
		cache := func(path, content string) {
			GinkgoHelper()
			key, err := pinnedimport.NormalizeRepository(repo)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.PutPinnedImportModule(ctx, sqlc.PutPinnedImportModuleParams{
				OrgID: orgA, RepositoryKey: key, Revision: sha, Path: path, Content: content,
			})).To(Succeed())
		}
		allow := func(repository string) (int, map[string]any) {
			return call(appAdmin, "AdminService/AddOrgImportRepository", map[string]any{"orgId": orgA.String(), "repository": repository})
		}

		BeforeEach(func() {
			makeStrict(orgA)
			start()
		})

		It("lets only an app admin keep the allowlist, normalised, audited and refusing ambiguous spellings", func() {
			code, out := call(adminA, "AdminService/AddOrgImportRepository", map[string]any{"orgId": orgA.String(), "repository": repo})
			Expect(code).To(Equal(http.StatusForbidden), "%v", out)

			added := ok(allow(repo))
			Expect(added).To(And(HaveKeyWithValue("repository", repo), HaveKeyWithValue("normalized", "http://127.0.0.1:1/team/modules")))
			code, out = allow("HTTP://127.0.0.1:1/team/modules/")
			Expect(code).To(Equal(http.StatusConflict), "%v", out)
			Expect(out["code"]).To(Equal("already_exists"))
			for _, bad := range []string{"http://127.0.0.1:1/team/mod%75les", "file:///srv/modules.git", "https://u:p@h.example/x/y"} {
				code, out = allow(bad)
				Expect(code).To(Equal(http.StatusBadRequest), "%s: %v", bad, out)
			}
			other, err := st.Queries.CreateGitCredential(ctx, sqlc.CreateGitCredentialParams{
				OrgID: orgB, Name: "b-cred", Kind: "none", ClientSecretEnc: []byte("-"), ProviderConfig: []byte(`{}`),
			})
			Expect(err).NotTo(HaveOccurred())
			code, out = call(appAdmin, "AdminService/AddOrgImportRepository", map[string]any{
				"orgId": orgA.String(), "repository": "https://git.example.com/x/y.git", "credentialId": other.ID.String(),
			})
			Expect(code).To(Equal(http.StatusBadRequest), "another org's credential: %v", out)

			list := ok(call(appAdmin, "AdminService/ListOrgImportRepositories", map[string]any{"orgId": orgA.String()}))
			Expect(list["items"]).To(HaveLen(1))
			ok(call(appAdmin, "AdminService/RemoveOrgImportRepository", map[string]any{"orgId": orgA.String(), "id": added["id"]}))

			rows, err := st.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{Column1: orgA, Limit: 50})
			Expect(err).NotTo(HaveOccurred())
			var actions []string
			for _, r := range rows {
				actions = append(actions, r.Action)
			}
			Expect(actions).To(ContainElements("org.import_repository.add", "org.import_repository.remove"))
		})

		// served is what orgA's collector is served once the eager recompute
		// has written it with the given substring.
		served := func(collector pgtype.UUID, want string) string {
			GinkgoHelper()
			var content string
			Eventually(func() string {
				row, err := st.Queries.GetServeCache(ctx, collector)
				if err != nil {
					return ""
				}
				content = row.Content
				return content
			}).WithTimeout(10 * time.Second).Should(ContainSubstring(want))
			return content
		}
		// matched creates a pipeline selecting the pinned-cluster collector.
		matched := func(name, contents string) map[string]any {
			GinkgoHelper()
			return ok(call(adminA, "PipelineService/CreatePipeline", map[string]any{
				"orgId": orgA.String(), "name": name, "contents": contents, "matchers": []string{`cluster="pinned-cluster"`},
			}))
		}
		newCollector := func() pgtype.UUID {
			GinkgoHelper()
			cl, err := st.Queries.UpsertCluster(ctx, "pinned-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cl.ID, OrgID: orgA})).To(Succeed())
			c, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cl.ID, Role: "singleton"})
			Expect(err).NotTo(HaveOccurred())
			return c.ID
		}

		It("serves a strict org the module it linted, inlined, and switches with the strict toggle", func() {
			collector := newCollector()
			ok(allow(repo))
			cache("owned.alloy", module("acme"))
			src := importing(sha, "owned.alloy")

			v := ok(call(adminA, "PipelineService/ValidatePipeline", map[string]any{"orgId": orgA.String(), "name": "p", "contents": src}))
			Expect(v).To(HaveKeyWithValue("valid", true))
			Expect(v).NotTo(HaveKey("diagnostics"), "collectors no longer fetch the module, so there is nothing to warn about")
			p := matched("p", src)
			ok(call(adminA, "PipelineService/EnablePipeline", map[string]any{"orgId": orgA.String(), "id": p["id"]}))
			stored, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: orgA, Name: "p"})
			Expect(err).NotTo(HaveOccurred())
			Expect(stored.Contents).To(Equal(src), "the stored text keeps import.git as written")

			content := served(collector, `import.string "m"`)
			Expect(content).NotTo(ContainSubstring("import.git"))
			Expect(content).To(ContainSubstring(`\"X-Scope-OrgID\" = \"acme\"`), "the exact module text, quoted")

			// Every other place strict mode is evaluated agrees with the save.
			pre := ok(call(appAdmin, "AdminService/TenantPreflight", map[string]any{"orgId": orgA.String()}))
			Expect(pre).NotTo(HaveKey("violations"))
			ok(setStrict(orgA, false))
			served(collector, `import.git "m"`)
			ok(setStrict(orgA, true))
			served(collector, `import.string "m"`)
		})

		// #410: the grafana/k8s-monitoring chart's labelmaps may write
		// __tenant_id__; a strict org saves the chart output verbatim and is
		// served it with a __tenant_id__ drop after each one.
		It("saves the k8s-monitoring chart output verbatim and serves it with __tenant_id__ dropped after each labelmap", func() {
			cl, err := st.Queries.UpsertCluster(ctx, "pinned-cluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cl.ID, OrgID: orgA})).To(Succeed())
			drop := regexp.MustCompile(`\n\s*rule \{\n\s*action = "labeldrop"\n\s*regex  = "__tenant_id__"\n\s*\}\n`)
			for role, drops := range map[string]int{"metrics": 2, "logs": 1} {
				raw, err := os.ReadFile("../signals/testdata/k8smon-" + role + ".alloy")
				Expect(err).NotTo(HaveOccurred())
				src := string(raw)
				coll, err := st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cl.ID, Role: role})
				Expect(err).NotTo(HaveOccurred())

				v := ok(call(adminA, "PipelineService/ValidatePipeline", map[string]any{"orgId": orgA.String(), "name": "k8smon-" + role, "contents": src}))
				Expect(v).To(HaveKeyWithValue("valid", true), "%v", v)
				p := ok(call(adminA, "PipelineService/CreatePipeline", map[string]any{
					"orgId": orgA.String(), "name": "k8smon-" + role, "contents": src,
					"matchers": []string{`cluster="pinned-cluster"`, `role="` + role + `"`},
				}))
				ok(call(adminA, "PipelineService/EnablePipeline", map[string]any{"orgId": orgA.String(), "id": p["id"]}))
				stored, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: orgA, Name: "k8smon-" + role})
				Expect(err).NotTo(HaveOccurred())
				Expect(stored.Contents).To(Equal(src), "the stored text is the chart output as written")

				content := served(coll.ID, `declare "pipe_k8smon_`+role+`"`)
				Expect(drop.FindAllString(content, -1)).To(HaveLen(drops), role)
				if role == "logs" {
					// The chart's otelcol.exporter.loki is served through a
					// loki.process dropping __tenant_id__.
					Expect(content).To(ContainSubstring("forward_to = [loki.process.shepherd_tenant_drop_loki.receiver]"))
					Expect(content).To(MatchRegexp(`loki\.process "shepherd_tenant_drop_loki" \{\s*forward_to = \[loki\.write\.loki\.receiver\]\s*stage\.label_drop \{\s*values = \["__tenant_id__"\]`))
				}
			}
			pre := ok(call(appAdmin, "AdminService/TenantPreflight", map[string]any{"orgId": orgA.String()}))
			Expect(pre).NotTo(HaveKey("violations"))
		})

		It("refuses deleting a git credential an allowlist entry uses, naming the entry", func() {
			enc, err := crypto.NewEncryptor(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))))
			Expect(err).NotTo(HaveOccurred())
			rpcWiringEncryptor = enc
			DeferCleanup(func() { rpcWiringEncryptor = nil })
			server.Close()
			start()
			cred, err := st.Queries.CreateGitCredential(ctx, sqlc.CreateGitCredentialParams{
				OrgID: orgA, Name: "modules-reader", Kind: "none", ClientSecretEnc: []byte("-"), ProviderConfig: []byte(`{}`),
			})
			Expect(err).NotTo(HaveOccurred())
			ok(call(appAdmin, "AdminService/AddOrgImportRepository", map[string]any{
				"orgId": orgA.String(), "repository": repo, "credentialId": cred.ID.String(),
			}))
			code, out := call(appAdmin, "GitOpsService/DeleteCredential", map[string]any{"orgId": orgA.String(), "id": cred.ID.String()})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("failed_precondition"))
			Expect(out["message"]).To(ContainSubstring(repo))
		})

		It("derives the editor's signals from the served form, so a pinned metrics module is proven metrics", func() {
			ok(allow(repo))
			cache("owned.alloy", module("acme"))
			src := importing(sha, "owned.alloy") + "m.out \"x\" { }\n"
			v := ok(call(adminA, "PipelineService/ValidatePipeline", map[string]any{"orgId": orgA.String(), "name": "p", "contents": src}))
			Expect(v).To(HaveKeyWithValue("valid", true))
			Expect(v).To(HaveKeyWithValue("signals", ConsistOf("metrics")))
			Expect(v).To(HaveKeyWithValue("signalsProven", true))
		})

		// #375 on the served form: the version check sees the inlined
		// module, so a component only the module uses is checked too.
		It("refuses a pinned module using a component a v1.18.1 instance lacks", func() {
			collector := newCollector()
			_, err := st.Queries.UpsertCollectorInstance(ctx, sqlc.UpsertCollectorInstanceParams{
				ID: "old-pod", CollectorID: collector, Name: "old-pod", LocalAttributes: json.RawMessage(`{}`),
				AlloyVersion: pgtype.Text{String: "v1.18.1", Valid: true},
			})
			Expect(err).NotTo(HaveOccurred())
			ok(allow(repo))
			cache("redact.alloy", "declare \"out\" {\n  otelcol.processor.redaction \"r\" {\n    output {}\n  }\n}\n")
			src := importing(sha, "redact.alloy") + "m.out \"x\" { }\n"

			v := ok(call(adminA, "PipelineService/ValidatePipeline", map[string]any{"orgId": orgA.String(), "name": "p", "contents": src}))
			Expect(fmt.Sprint(v["diagnostics"])).To(ContainSubstring("otelcol.processor.redaction is not available in Alloy v1.18.1"))

			p := matched("p", src)
			code, out := call(adminA, "PipelineService/EnablePipeline", map[string]any{"orgId": orgA.String(), "id": p["id"]})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["message"]).To(ContainSubstring("component otelcol.processor.redaction is not available in Alloy v1.18.1"))
			Expect(out["message"]).To(ContainSubstring("old-pod"))
		})

		It("keeps the last good config, and records the failure, when a module cannot be inlined at serve time", func() {
			collector := newCollector()
			ok(allow(repo))
			cache("owned.alloy", module("acme"))
			p := matched("p", importing(sha, "owned.alloy"))
			ok(call(adminA, "PipelineService/EnablePipeline", map[string]any{"orgId": orgA.String(), "id": p["id"]}))
			good := served(collector, `import.string "m"`)

			key, err := pinnedimport.NormalizeRepository(repo)
			Expect(err).NotTo(HaveOccurred())
			Expect(st.Queries.DeletePinnedImportModule(ctx, sqlc.DeletePinnedImportModuleParams{
				OrgID: orgA, RepositoryKey: key, Revision: sha, Path: "owned.alloy",
			})).To(Succeed())
			// Stage 3 refuses any change while the org's served form cannot
			// be built, so the recompute is run directly.
			code, out := call(adminA, "PipelineService/EnablePipeline", map[string]any{"orgId": orgA.String(), "id": matched("other", remoteWriteTo(`"acme"`))["id"]})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["message"]).To(ContainSubstring("cannot be served"))
			ps := mgmtapi.NewPipelineService(st, validate.New(&cfg.Validate), embeddedSchema(), slog.Default())
			ps.RecomputeOrgCaches(ctx, orgA)
			Eventually(func() int64 {
				row, err := st.Queries.GetServeCache(ctx, collector)
				Expect(err).NotTo(HaveOccurred())
				return row.LastFailedSeq
			}).WithTimeout(10 * time.Second).Should(BeNumerically(">", 0))
			row, err := st.Queries.GetServeCache(ctx, collector)
			Expect(err).NotTo(HaveOccurred())
			Expect(row.Content).To(Equal(good), "never the raw import.git, never a partial config")
		})

		It("refuses a module writing to a foreign tenant, a branch revision, a repository off the allowlist and an unfetchable module", func() {
			ok(allow(repo))
			cache("foreign.alloy", module("victim"))
			for name, src := range map[string]string{
				"foreign":   importing(sha, "foreign.alloy"),
				"branch":    importing("main", "foreign.alloy"),
				"short":     importing(sha[:12], "foreign.alloy"),
				"elsewhere": strings.Replace(importing(sha, "foreign.alloy"), "team/modules", "evil/modules", 1),
				"variant":   strings.Replace(importing(sha, "foreign.alloy"), "team/modules", "Team/modules", 1),
				"uncached":  importing(sha, "none.alloy"),
			} {
				code, out := createPipeline(adminA, orgA, name, src)
				Expect(code).To(Equal(http.StatusBadRequest), "%s: %v", name, out)
				Expect(out["code"]).To(Equal("failed_precondition"), name)
			}
		})

		It("keeps an entry on the allowlist while an enabled pipeline imports from it", func() {
			added := ok(allow(repo))
			cache("owned.alloy", module("acme"))
			p := ok(createPipeline(adminA, orgA, "p", importing(sha, "owned.alloy")))
			ok(call(adminA, "PipelineService/EnablePipeline", map[string]any{"orgId": orgA.String(), "id": p["id"]}))
			code, out := call(appAdmin, "AdminService/RemoveOrgImportRepository", map[string]any{"orgId": orgA.String(), "id": added["id"]})
			Expect(code).To(Equal(http.StatusBadRequest), "%v", out)
			Expect(out["code"]).To(Equal("failed_precondition"))
			Expect(out["message"]).To(ContainSubstring(`pipeline "p"`))

			ok(call(adminA, "PipelineService/DisablePipeline", map[string]any{"orgId": orgA.String(), "id": p["id"]}))
			ok(call(appAdmin, "AdminService/RemoveOrgImportRepository", map[string]any{"orgId": orgA.String(), "id": added["id"]}))
		})
	})
})
