package tenancy_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/gateway"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/tenancy"
	"shepherd/internal/testutil"
	"shepherd/internal/validate"
)

func TestTenancy(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Tenancy Suite")
}

var sharedPG *testutil.SharedPostgres

var _ = SynchronizedBeforeSuite(func() []byte {
	var err error
	sharedPG, err = testutil.StartSharedPostgres(context.Background())
	Expect(err).NotTo(HaveOccurred())
	Expect(store.MigrateUp(context.Background(), sharedPG.RootURL)).To(Succeed())
	return nil
}, func(_ []byte) {})

var _ = SynchronizedAfterSuite(func() {}, func() {
	if sharedPG != nil {
		Expect(sharedPG.Terminate(context.Background())).To(Succeed())
	}
})

var _ = Describe("Policy", func() {
	It("checks a tenant only when strict, and never refuses no tenant", func() {
		p := tenancy.Policy{Owned: tenancy.Rules([]sqlc.OrgTenant{{Tenant: "acme", Kind: "exact"}, {Tenant: "acme-", Kind: "prefix"}})}
		Expect(p.Check("free")).To(Succeed(), "not strict")
		p.Strict = true
		Expect(p.Check("acme")).To(Succeed())
		Expect(p.Check("acme-eu")).To(Succeed())
		Expect(p.Check("")).To(Succeed())
		Expect(p.Check("free")).To(MatchError(tenancy.ErrNotOwned))
		Expect(p.Validation().Strict).To(BeTrue())
		Expect(tenancy.Policy{}.Validation().Strict).To(BeFalse())
	})
})

// The boot preflight behind tenants.require_owned (internal/server.New runs
// CheckRequireOwned and refuses to start on its error).
var _ = Describe("CheckRequireOwned", Label("integration"), func() {
	var (
		ctx context.Context
		st  *store.Store
		org sqlc.Org
	)

	BeforeEach(func() {
		ctx = context.Background()
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: sharedPG.IsolatedDB(ctx, GinkgoTB()), MaxConns: 4})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)
		org, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "preflight-org", DisplayName: "P", AdminGroupID: "p-admins"})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.AddOrgTenant(ctx, sqlc.AddOrgTenantParams{OrgID: org.ID, Tenant: "acme", Kind: "exact", IsDefault: true, CreatedBy: "t"})
		Expect(err).NotTo(HaveOccurred())
		// An org with no tenants and nothing that uses one passes too.
		_, err = st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "preflight-empty", DisplayName: "E", AdminGroupID: "e-admins"})
		Expect(err).NotTo(HaveOccurred())
	})

	destination := func(name, tenant string) {
		GinkgoHelper()
		_, err := st.Queries.CreateDestination(ctx, sqlc.CreateDestinationParams{
			OrgID: org.ID, Name: name, Type: "prometheus", Url: "https://m.example.com", AuthMode: "none",
			TenantID: tenant, Extra: json.RawMessage(`{}`),
		})
		Expect(err).NotTo(HaveOccurred())
	}
	pipeline := func(name, tenant string, enabled bool) {
		GinkgoHelper()
		_, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: org.ID, Name: name, Contents: `loki.write "w" {
  endpoint {
    url       = "https://loki.example.com/loki/api/v1/push"
    tenant_id = "` + tenant + `"
  }
}
`, Matchers: json.RawMessage(`[]`), Source: "ui", Enabled: enabled,
		})
		Expect(err).NotTo(HaveOccurred())
	}

	// Third review (PR #388): the index is bounded and fails soft, and never
	// trusts rows written for other contents.
	It("records a pathological pipeline as incomplete quickly, and the rebuild never fails", func() {
		var b strings.Builder
		b.WriteString("declare \"l0\" {\n  argument \"t\" { }\n  loki.write \"w\" {\n    endpoint {\n      url = \"x\"\n      tenant_id = argument.t.value\n    }\n  }\n}\n")
		sum := strings.TrimSuffix(strings.Repeat("argument.t.value + ", 40), " + ")
		for i := 1; i < 7; i++ {
			fmt.Fprintf(&b, "declare \"l%d\" {\n  argument \"t\" { }\n  l%d \"x\" { t = %s }\n}\n", i, i-1, sum)
		}
		b.WriteString("l6 \"top\" { t = \"acme\" }\n")
		p, err := st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: org.ID, Name: "pathological", Contents: b.String(), Matchers: json.RawMessage(`[]`), Source: "ui", Enabled: true,
		})
		Expect(err).NotTo(HaveOccurred())

		start := time.Now()
		tenancy.RebuildPipelineTenants(ctx, st)
		Expect(time.Since(start)).To(BeNumerically("<", 5*time.Second))
		idx, err := st.Queries.GetPipelineTenantIndex(ctx, p.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(idx.Complete).To(BeFalse())
	})

	It("records at most 256 tenants of one pipeline, and marks it incomplete past that", func() {
		pieces := make([]string, 2000)
		for i := range pieces {
			pieces[i] = fmt.Sprintf("t%d", i)
		}
		pipeline("many", strings.Join(pieces, "|"), true)
		tenancy.RebuildPipelineTenants(ctx, st)
		p, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: org.ID, Name: "many"})
		Expect(err).NotTo(HaveOccurred())
		idx, err := st.Queries.GetPipelineTenantIndex(ctx, p.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(idx.Complete).To(BeFalse())
		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "preflight-other3", DisplayName: "O", AdminGroupID: "o3-admins"})
		Expect(err).NotTo(HaveOccurred())
		covered, warnings, err := tenancy.OtherOrgPipelinesCovered(ctx, st.Queries, other.ID, gateway.OrgTenant{Tenant: "t", Kind: gateway.TenantPrefix})
		Expect(err).NotTo(HaveOccurred())
		Expect(covered).To(HaveLen(256))
		Expect(warnings).To(ConsistOf(ContainSubstring(`"preflight-org"`)))
	})

	It("warns about another org's pipeline it could not read in time, rather than leaving it out", func() {
		pipeline("w", "first", true) // never recorded: stale
		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "preflight-other4", DisplayName: "O", AdminGroupID: "o4-admins"})
		Expect(err).NotTo(HaveOccurred())
		restore := validate.SetTenantLintDeadline(0)
		defer restore()
		covered, warnings, err := tenancy.OtherOrgPipelinesCovered(ctx, st.Queries, other.ID, gateway.OrgTenant{Tenant: "first", Kind: gateway.TenantExact})
		Expect(err).NotTo(HaveOccurred())
		Expect(covered).To(BeEmpty())
		Expect(warnings).To(ConsistOf(And(ContainSubstring(`"preflight-org"`), ContainSubstring("busy"))))
	})

	It("keeps a pipeline's previous index row when the tenant check is busy (no verdict)", func() {
		pipeline("w", "first", true)
		tenancy.RebuildPipelineTenants(ctx, st)
		p, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: org.ID, Name: "w"})
		Expect(err).NotTo(HaveOccurred())
		before, err := st.Queries.GetPipelineTenantIndex(ctx, p.ID)
		Expect(err).NotTo(HaveOccurred())

		restore := validate.SetTenantLintDeadline(0)
		defer restore()
		p.Contents = strings.Replace(p.Contents, "first", "second", 1)
		Expect(tenancy.RecordPipelineTenants(ctx, st.Queries, p)).To(Succeed(), "busy is not a failed write")
		after, err := st.Queries.GetPipelineTenantIndex(ctx, p.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(after).To(Equal(before), "neither recorded as incomplete nor rewritten")
	})

	It("re-records a pipeline whose contents changed without the index (a rolling upgrade)", func() {
		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "preflight-other2", DisplayName: "O", AdminGroupID: "o2-admins"})
		Expect(err).NotTo(HaveOccurred())
		pipeline("w", "first", true)
		tenancy.RebuildPipelineTenants(ctx, st)
		p, err := st.Queries.GetPipelineByOrgAndName(ctx, sqlc.GetPipelineByOrgAndNameParams{OrgID: org.ID, Name: "w"})
		Expect(err).NotTo(HaveOccurred())
		// An old pod's write: contents change, the index does not.
		_, err = st.Queries.UpdatePipeline(ctx, sqlc.UpdatePipelineParams{
			ID: p.ID, Name: p.Name, Contents: strings.Replace(p.Contents, "first", "second", 1), Matchers: p.Matchers, UpdatedBy: "old-pod",
		})
		Expect(err).NotTo(HaveOccurred())
		got, _, err := tenancy.OtherOrgPipelinesCovered(ctx, st.Queries, other.ID, gateway.OrgTenant{Tenant: "second", Kind: gateway.TenantExact})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(HaveLen(1))
		got, _, err = tenancy.OtherOrgPipelinesCovered(ctx, st.Queries, other.ID, gateway.OrgTenant{Tenant: "first", Kind: gateway.TenantExact})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(BeEmpty())
	})

	It("passes when everything uses an owned tenant or none", func() {
		destination("owned", "acme")
		destination("none", "")
		pipeline("w", "acme", true)
		pipeline("parked", "free", false)
		Expect(tenancy.CheckRequireOwned(ctx, st.Queries)).To(Succeed())
	})

	It("fails closed, listing every violation and how to fix it", func() {
		destination("legacy", "free-d")
		pipeline("w", "free-p", true)
		err := tenancy.CheckRequireOwned(ctx, st.Queries)
		Expect(err).To(MatchError(tenancy.ErrRequireOwnedViolations))
		Expect(err.Error()).To(And(
			ContainSubstring(`destination "legacy" uses tenant "free-d"`),
			ContainSubstring(`enabled pipeline "w"`), ContainSubstring(`"free-p"`),
			ContainSubstring("shepherd admin tenant-preflight"),
		))
		vs, err := tenancy.Preflight(ctx, st.Queries, org, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(vs).To(HaveLen(2))
	})

	It("needs a tenant for per-org strict mode only (requireTenant)", func() {
		empty, err := st.Queries.GetOrgByName(ctx, "preflight-empty")
		Expect(err).NotTo(HaveOccurred())
		vs, err := tenancy.Preflight(ctx, st.Queries, empty, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(vs).To(ConsistOf(HaveField("Kind", tenancy.KindOrg)))
		vs, err = tenancy.Preflight(ctx, st.Queries, empty, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(vs).To(BeEmpty())
	})

	// pipeline_tenants (0040): the boot rebuild is its backfill, and the
	// assignment check is one indexed query over it.
	It("rebuilds pipeline_tenants and answers which rule would cover another org's tenant", func() {
		pipeline("w", "x_tenant", true)
		pipeline("parked", "parked-t", false)
		other, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "preflight-other", DisplayName: "O", AdminGroupID: "o-admins"})
		Expect(err).NotTo(HaveOccurred())

		covered := func(tenant string, prefix bool) []string {
			GinkgoHelper()
			kind := gateway.TenantExact
			if prefix {
				kind = gateway.TenantPrefix
			}
			out, _, err := tenancy.OtherOrgPipelinesCovered(ctx, st.Queries, other.ID, gateway.OrgTenant{Tenant: tenant, Kind: kind})
			Expect(err).NotTo(HaveOccurred())
			return out
		}
		// Written without the index (as a pod that predates it would): the
		// check re-records it before it asks.
		Expect(covered("x_tenant", false)).To(ConsistOf(ContainSubstring(`pipeline "w"`)), "a stale row is re-recorded")
		tenancy.RebuildPipelineTenants(ctx, st)

		Expect(covered("x_tenant", false)).To(ConsistOf(ContainSubstring(`pipeline "w"`)))
		Expect(covered("x_", true)).To(HaveLen(1))
		Expect(covered("xa", true)).To(BeEmpty(), "an underscore is not a wildcard")
		Expect(covered("parked-t", false)).To(BeEmpty(), "a disabled pipeline is not recorded")
		Expect(tenancy.OtherOrgPipelinesCovered(ctx, st.Queries, org.ID,
			gateway.OrgTenant{Tenant: "x_tenant", Kind: gateway.TenantExact})).To(BeEmpty(), "the org's own pipeline")
	})

	It("loads an org's policy: its flag, or the server-wide setting", func() {
		p, err := tenancy.Load(ctx, st.Queries, org.ID, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Strict).To(BeFalse())
		p, err = tenancy.Load(ctx, st.Queries, org.ID, true)
		Expect(err).NotTo(HaveOccurred())
		Expect(p).To(And(HaveField("Strict", true), HaveField("OrgStrict", false)))
		_, err = st.Queries.SetOrgStrictTenants(ctx, sqlc.SetOrgStrictTenantsParams{ID: org.ID, StrictTenants: true})
		Expect(err).NotTo(HaveOccurred())
		p, err = tenancy.Load(ctx, st.Queries, org.ID, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(p).To(And(HaveField("Strict", true), HaveField("OrgStrict", true)))
		_, err = tenancy.Load(ctx, st.Queries, pgtype.UUID{}, false)
		Expect(err).To(HaveOccurred())
	})
})
