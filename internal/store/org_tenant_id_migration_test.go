package store_test

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0013_org_tenant_id: tenant identity belongs to the org and only an
// app admin sets it. The schema-level controls here are the second half of
// that — internal/gateway.ValidateTenantID produces the good error message at
// the API boundary, and these constraints make a bad or duplicated value
// unstorable even if some future write path forgets to call it.
var _ = Describe("Migration: 0013_org_tenant_id", Label("integration"), func() {
	var db *pgxpool.Pool

	BeforeEach(func(ctx context.Context) {
		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
	})

	insertOrg := func(ctx context.Context, name, tenant string) error {
		if tenant == "" {
			_, err := db.Exec(ctx,
				`INSERT INTO orgs (name, display_name, admin_group_id) VALUES ($1, $1, 'g')`, name)
			return err
		}
		_, err := db.Exec(ctx,
			`INSERT INTO orgs (name, display_name, admin_group_id, tenant_id) VALUES ($1, $1, 'g', $2)`,
			name, tenant)
		return err
	}

	It("accepts the characters Grafana Mimir documents as legal", func(ctx context.Context) {
		for i, tenant := range []string{"acme", "AcmeCorp", "acme-42", "a!-_.*'()"} {
			Expect(insertOrg(ctx, "ok-org-"+string(rune('a'+i)), tenant)).To(Succeed(),
				"tenant %q is legal per internal/gateway.ValidateTenantID; the CHECK has drifted from it", tenant)
		}
	})

	It("refuses a tenant id with a slash or whitespace", func(ctx context.Context) {
		Expect(insertOrg(ctx, "slash-org", "acme/evil")).To(HaveOccurred(),
			"a slash would break both the injected header value and the route slug derived from it")
		Expect(insertOrg(ctx, "space-org", "acme corp")).To(HaveOccurred())
	})

	It("refuses the reserved tenant ids", func(ctx context.Context) {
		for _, tenant := range []string{".", "..", "__mimir_cluster"} {
			Expect(insertOrg(ctx, "reserved-"+strings.Trim(tenant, "._"), tenant)).To(HaveOccurred(),
				"tenant %q is refused by Mimir itself; storing it would fail at ingest instead of here", tenant)
		}
	})

	It("refuses a tenant id past the documented length limit", func(ctx context.Context) {
		Expect(insertOrg(ctx, "long-org", strings.Repeat("a", 151))).To(HaveOccurred())
		Expect(insertOrg(ctx, "at-limit-org", strings.Repeat("a", 150))).To(Succeed())
	})

	// The property the whole change rests on: one tenant identity, one org.
	// Without it, two orgs could be given the same tenant and the gateway
	// would stamp both orgs' traffic alike — the cross-tenant merge this
	// change exists to prevent, reached by a slower route.
	It("refuses two orgs claiming the same tenant identity", func(ctx context.Context) {
		Expect(insertOrg(ctx, "first-org", "shared-tenant")).To(Succeed())
		Expect(insertOrg(ctx, "second-org", "shared-tenant")).To(HaveOccurred(),
			"two orgs share one tenant identity — the gateway would inject the same X-Scope-OrgID "+
				"for both, merging their telemetry")
	})

	// Orgs predating this migration have no tenant, and there may be many of
	// them; the uniqueness index must not treat those NULLs as collisions.
	It("allows many orgs with no tenant identity yet", func(ctx context.Context) {
		Expect(insertOrg(ctx, "untenanted-a", "")).To(Succeed())
		Expect(insertOrg(ctx, "untenanted-b", "")).To(Succeed())
	})
})
