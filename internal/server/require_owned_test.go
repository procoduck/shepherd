package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

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

	// The boot's pipeline_tenants rebuild parses every enabled pipeline; a
	// pathological one (each declare level multiplying the tenant ×40) must
	// neither slow it down nor stop it.
	It("starts with a pathological pipeline saved", func() {
		ctx := context.Background()
		st, err := store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 2})
		Expect(err).NotTo(HaveOccurred())
		defer st.Close()
		org, err := st.Queries.GetOrgByName(ctx, "boot-org")
		Expect(err).NotTo(HaveOccurred())
		var b strings.Builder
		b.WriteString("declare \"l0\" {\n  argument \"t\" { }\n  loki.write \"w\" {\n    endpoint {\n      url = \"x\"\n      tenant_id = argument.t.value\n    }\n  }\n}\n")
		sum := strings.TrimSuffix(strings.Repeat("argument.t.value + ", 40), " + ")
		for i := 1; i < 7; i++ {
			fmt.Fprintf(&b, "declare \"l%d\" {\n  argument \"t\" { }\n  l%d \"x\" { t = %s }\n}\n", i, i-1, sum)
		}
		b.WriteString("l6 \"top\" { t = \"acme\" }\n")
		_, err = st.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
			OrgID: org.ID, Name: "pathological", Contents: b.String(), Matchers: json.RawMessage(`[]`), Source: "ui", Enabled: true,
		})
		Expect(err).NotTo(HaveOccurred())

		cfg := &config.Config{Database: config.DatabaseConfig{URL: dbURL, MaxConns: 2}}
		start := time.Now()
		// The server is not run, so its pool is left open; the suite's
		// container goes with the test process.
		srv, err := server.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
		Expect(err).NotTo(HaveOccurred())
		Expect(srv).NotTo(BeNil())
		Expect(time.Since(start)).To(BeNumerically("<", 10*time.Second))
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
