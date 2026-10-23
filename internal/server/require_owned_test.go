package server_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/server"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/tenancy"
	"shepherd/internal/testutil"
)

// tenants.require_owned fails closed at boot: server.New refuses to start
// while anything stored uses a tenant its org does not own.
var _ = Describe("tenants.require_owned at boot", Ordered, Label("integration"), func() {
	var dbURL string

	BeforeAll(func() {
		ctx := context.Background()
		pg, err := testutil.StartSharedPostgres(ctx)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(pg.Terminate(context.Background())).To(Succeed()) })
		Expect(store.MigrateUp(ctx, pg.RootURL)).To(Succeed())
		dbURL = pg.RootURL

		st, err := store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 2})
		Expect(err).NotTo(HaveOccurred())
		defer st.Close()
		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "boot-org", DisplayName: "Boot", AdminGroupID: "boot-admins"})
		Expect(err).NotTo(HaveOccurred())
		_, err = st.Queries.CreateDestination(ctx, sqlc.CreateDestinationParams{
			OrgID: org.ID, Name: "legacy", Type: "prometheus", Url: "https://m.example.com", AuthMode: "none",
			TenantID: "nobodys", Extra: json.RawMessage(`{}`),
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses to start, listing what breaks the rule", func() {
		cfg := &config.Config{Database: config.DatabaseConfig{URL: dbURL, MaxConns: 2}}
		cfg.Tenants.RequireOwned = true
		srv, err := server.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		Expect(srv).To(BeNil())
		Expect(err).To(MatchError(tenancy.ErrRequireOwnedViolations))
		Expect(err.Error()).To(And(ContainSubstring("refusing to start"),
			ContainSubstring(`destination "legacy" uses tenant "nobodys"`)))
	})
})
