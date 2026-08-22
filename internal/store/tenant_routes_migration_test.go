package store_test

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0009_tenant_routes (docs/gateway-tier-plan.md §4 W4, D8, D9):
// tenant route storage. These specs exercise the two schema-level controls
// the CI red run (see the W4 session report) proves are load-bearing:
//
//  1. The segment CHECK constraint — a second, independent enforcement of
//     the same charset internal/gateway.ValidateSegment checks in Go, so a
//     bug that bypassed application-level validation still could not get a
//     malformed segment into the table.
//  2. The partial UNIQUE index on (org_id, tenant_id, kind) WHERE status =
//     'active' — "at most one active segment per tenant+kind", which is
//     what makes rotation a state transition instead of a race.
//
// pgErrCode extracts a Postgres SQLSTATE from err, or "" if err is not a
// *pgconn.PgError.
func pgErrCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

var _ = Describe("Migration: 0009_tenant_routes", Label("integration"), func() {
	var (
		db    *pgxpool.Pool
		orgID string
	)

	BeforeEach(func(ctx context.Context) {
		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateUp(ctx, url)).To(Succeed())

		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		Expect(db.QueryRow(ctx,
			`INSERT INTO orgs (name, display_name, admin_group_id) VALUES ('tenant-routes-org', 'Tenant Routes Org', 'grp-admin') RETURNING id`,
		).Scan(&orgID)).To(Succeed())
	})

	insertRoute := func(ctx context.Context, tenantID, kind, segment, status string) error {
		_, err := db.Exec(ctx,
			`INSERT INTO tenant_routes (org_id, tenant_id, kind, segment, status, gateway_mode, gateway_name)
			 VALUES ($1, $2, $3, $4, $5, 'managed', 'shepherd-gw')`,
			orgID, tenantID, kind, segment, status)
		return err
	}

	It("accepts a well-formed segment", func(ctx context.Context) {
		Expect(insertRoute(ctx, "acme", "otlp", "acme-a1b2c3d4", "active")).To(Succeed())
	})

	// This is the migration-level half of the charset/traversal red run:
	// reverting 0009's segment CHECK constraint (delete the CHECK clause
	// from 0009_tenant_routes.up.sql) makes this spec fail because the
	// INSERT below succeeds instead of failing with SQLSTATE 23514
	// (check_violation).
	It("rejects a segment that violates the charset CHECK constraint", func(ctx context.Context) {
		err := insertRoute(ctx, "acme", "otlp", "../../etc", "active")
		Expect(err).To(HaveOccurred())
		Expect(pgErrCode(err)).To(Equal("23514"), "expected a check_violation for a path-traversal-shaped segment")
	})

	It("rejects a segment shorter than the length bound", func(ctx context.Context) {
		err := insertRoute(ctx, "acme", "otlp", "ab", "active")
		Expect(err).To(HaveOccurred())
		Expect(pgErrCode(err)).To(Equal("23514"))
	})

	It("rejects an unknown kind", func(ctx context.Context) {
		err := insertRoute(ctx, "acme", "carrier-pigeon", "acme-a1b2c3d4", "active")
		Expect(err).To(HaveOccurred())
		Expect(pgErrCode(err)).To(Equal("23514"))
	})

	// This is the migration-level half of the uniqueness red run: reverting
	// 0009's idx_tenant_routes_one_active partial unique index makes this
	// spec fail because the second INSERT below succeeds instead of failing
	// with SQLSTATE 23505 (unique_violation).
	It("enforces at most one active route per org+tenant+kind", func(ctx context.Context) {
		Expect(insertRoute(ctx, "acme", "otlp", "acme-a1b2c3d4", "active")).To(Succeed())

		err := insertRoute(ctx, "acme", "otlp", "acme-e5f6g7h8", "active")
		Expect(err).To(HaveOccurred())
		Expect(pgErrCode(err)).To(Equal("23505"), "expected a unique_violation for a second active route on the same org+tenant+kind")
	})

	It("allows a deprecated route to coexist with a new active route for the same org+tenant+kind (rotation)", func(ctx context.Context) {
		Expect(insertRoute(ctx, "acme", "otlp", "acme-a1b2c3d4", "deprecated")).To(Succeed())
		Expect(insertRoute(ctx, "acme", "otlp", "acme-e5f6g7h8", "active")).To(Succeed())
	})

	It("allows the same segment string to be reused across kinds but not within the same kind", func(ctx context.Context) {
		Expect(insertRoute(ctx, "acme", "otlp", "acme-a1b2c3d4", "revoked")).To(Succeed())
		Expect(insertRoute(ctx, "acme", "faro", "acme-a1b2c3d4", "revoked")).To(Succeed())

		err := insertRoute(ctx, "globex", "otlp", "acme-a1b2c3d4", "deprecated")
		Expect(err).To(HaveOccurred())
		Expect(pgErrCode(err)).To(Equal("23505"), "expected a unique_violation for a duplicate (kind, segment) pair, even across tenants/orgs")
	})
})
