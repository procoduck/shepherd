package store_test

import (
	"context"
	"encoding/json"
	"log/slog"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// W2 (docs/gateway-tier-plan.md §4): 0008_destination_bindings.up.sql adds a
// new destination_bindings table and does not ALTER destinations at all.
// This pins the load-bearing claim in that migration's own comment: a
// destination row seeded before this migration existed (same shape the dev
// seed's seedDestinations and the e2e fixtures use — SecretName/
// SecretNamespace empty, AuthMode "none") keeps resolving standalone after
// the migration, and a binding can be layered on top of it without the
// template ever needing to change.
var _ = Describe("Migration: destination_bindings does not orphan existing destinations", Label("integration"), func() {
	It("lets a pre-existing (no-secret) destination keep resolving after the migration, and a binding added on top resolves correctly", func(ctx context.Context) {
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateUp(ctx, dbURL)).To(Succeed())

		st, err := store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(st.Close)

		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{
			Name: "seed-shape-org", DisplayName: "Seed Shape Org", AdminGroupID: "seed-shape-admin",
		})
		Expect(err).NotTo(HaveOccurred())

		// Exactly the shape internal/cli/dev.go's seedDestinations creates.
		seeded, err := st.Queries.CreateDestination(ctx, sqlc.CreateDestinationParams{
			OrgID: org.ID, Name: "prom-prod", Type: "prometheus", Url: "http://prometheus.dev.svc:9090",
			SecretName: "", SecretNamespace: "", AuthMode: "none", Extra: json.RawMessage(`{}`),
		})
		Expect(err).NotTo(HaveOccurred())

		// Untouched by the migration: still fetchable by id and by org list,
		// with every pre-existing field intact.
		fetched, err := st.Queries.GetDestinationByID(ctx, seeded.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(fetched).To(Equal(seeded))

		listed, err := st.Queries.ListDestinationsByOrg(ctx, org.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(listed).To(HaveLen(1))
		Expect(listed[0].Name).To(Equal("prom-prod"))

		// A binding layered on top resolves against the seeded (empty-secret,
		// auth_mode "none") template without error, and without requiring
		// any change to the seeded row.
		binding, err := st.Queries.CreateDestinationBinding(ctx, sqlc.CreateDestinationBindingParams{
			DestinationID: seeded.ID, OrgID: org.ID, Name: "team-x", TenantID: "team-x",
		})
		Expect(err).NotTo(HaveOccurred())

		resolved, err := st.Queries.GetResolvedDestinationBinding(ctx, binding.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.TenantID).To(Equal("team-x"), "tenant_id comes from the binding")
		Expect(resolved.Url).To(Equal("http://prometheus.dev.svc:9090"), "url comes from the untouched template")
		Expect(resolved.SecretName).To(Equal(""))
		Expect(resolved.AuthMode).To(Equal("none"))
		Expect(resolved.DestinationID).To(Equal(seeded.ID))

		// Deleting the template while the binding still references it must
		// fail (FK, no CASCADE) -- silently orphaning the binding would leave
		// a team pointed at nothing.
		err = st.Queries.DeleteDestination(ctx, seeded.ID)
		Expect(err).To(HaveOccurred())
	})
})
