package tenancy_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/tenancy"
	"shepherd/internal/testutil"
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
