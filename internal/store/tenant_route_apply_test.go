package store_test

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// The store half of tenant-route apply (docs/archive/plans/2026-09-29-tenant-route-apply.md
// PR 1, migration 0027): the reconciler's reads and its status writes.
var _ = Describe("tenant route apply status (0027)", Label("integration"), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		orgID  pgtype.UUID
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateUp(ctx, dbURL)).To(Succeed())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5})
		Expect(err).NotTo(HaveOccurred())
		o, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "routes-org", DisplayName: "Routes", AdminGroupID: "admin"})
		Expect(err).NotTo(HaveOccurred())
		orgID = o.ID
	})

	AfterEach(func() {
		st.Close()
		cancel()
	})

	route := func(tenant, kind, segment string) sqlc.TenantRoute {
		r, err := st.Queries.CreateTenantRoute(ctx, sqlc.CreateTenantRouteParams{
			OrgID: orgID, TenantID: tenant, Kind: kind, Segment: segment,
			GatewayMode: "operator", GatewayName: "edge", GatewayNamespace: "gateways",
		})
		Expect(err).NotTo(HaveOccurred())
		return r
	}

	It("starts every route pending, with no message and no applied_at", func() {
		r := route("acme", "otlp", "acme-a1")
		Expect(r.ApplyStatus).To(Equal("pending"))
		Expect(r.ApplyMessage).To(BeEmpty())
		Expect(r.AppliedAt.Valid).To(BeFalse())
	})

	It("moves applied_at only on a verified apply", func() {
		r := route("acme", "otlp", "acme-a2")
		applied, err := st.Queries.SetTenantRouteApplyStatus(ctx, sqlc.SetTenantRouteApplyStatusParams{
			ID: r.ID, ApplyStatus: "applied", ApplyMessage: "",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(applied.AppliedAt.Valid).To(BeTrue())

		failed, err := st.Queries.SetTenantRouteApplyStatus(ctx, sqlc.SetTenantRouteApplyStatusParams{
			ID: r.ID, ApplyStatus: "error", ApplyMessage: "connection refused",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(failed.ApplyStatus).To(Equal("error"))
		Expect(failed.ApplyMessage).To(Equal("connection refused"))
		Expect(failed.AppliedAt.Time).To(BeTemporally("==", applied.AppliedAt.Time),
			"a failed pass must not move the last verified-apply time")
	})

	It("refuses an apply status outside the documented set", func() {
		r := route("acme", "otlp", "acme-a3")
		_, err := st.Queries.SetTenantRouteApplyStatus(ctx, sqlc.SetTenantRouteApplyStatusParams{
			ID: r.ID, ApplyStatus: "maybe", ApplyMessage: "",
		})
		Expect(err).To(HaveOccurred())
	})

	It("revokes exactly the deprecated routes whose overlap has ended", func() {
		expired := route("acme", "otlp", "acme-old")
		_, err := st.Queries.DeprecateTenantRoute(ctx, sqlc.DeprecateTenantRouteParams{
			ID: expired.ID, ValidUntil: pgtype.Timestamptz{Time: time.Now().Add(-time.Minute), Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		overlapping := route("globex", "otlp", "globex-old")
		_, err = st.Queries.DeprecateTenantRoute(ctx, sqlc.DeprecateTenantRouteParams{
			ID: overlapping.ID, ValidUntil: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		})
		Expect(err).NotTo(HaveOccurred())
		active := route("initech", "otlp", "initech-new")

		revoked, err := st.Queries.RevokeExpiredTenantRoutes(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(revoked).To(HaveLen(1))
		Expect(revoked[0].ID).To(Equal(expired.ID))
		Expect(revoked[0].Status).To(Equal("revoked"))
		Expect(revoked[0].RevokedAt.Valid).To(BeTrue())

		for _, id := range []pgtype.UUID{overlapping.ID, active.ID} {
			r, err := st.Queries.GetTenantRouteByID(ctx, id)
			Expect(err).NotTo(HaveOccurred())
			Expect(r.Status).NotTo(Equal("revoked"))
		}
	})

	It("lists every route for the reconciler, revoked ones included", func() {
		keep := route("acme", "otlp", "acme-k1")
		gone := route("globex", "faro", "globex-f1")
		_, err := st.Queries.RevokeTenantRoute(ctx, gone.ID)
		Expect(err).NotTo(HaveOccurred())

		all, err := st.Queries.ListTenantRoutesForReconcile(ctx)
		Expect(err).NotTo(HaveOccurred())
		ids := []pgtype.UUID{}
		for _, r := range all {
			ids = append(ids, r.ID)
		}
		Expect(ids).To(ContainElements(keep.ID, gone.ID))
	})
})
