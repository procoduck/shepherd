package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0022_beacon_principal (docs/plans/2026-09-16-agent-oidc-auth.md,
// Phase 2): generalises the beacon inventory's identity from an agent-token
// UUID (0010's token_id, FK to agent_tokens) to a free-form text principal, so
// an OIDC collector — which has no agent_tokens row — can report too. Pin the
// three things enforcement leans on: the FK is gone (an "oidc:..." principal
// with no token row inserts), the identity unique constraint moved to
// principal, and an existing token_id row backfills to its text.
var _ = Describe("Migration: 0022_beacon_principal", Label("integration"), func() {
	var db *pgxpool.Pool

	BeforeEach(func(ctx context.Context) {
		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
	})

	It("accepts a principal with no agent_tokens row (the OIDC case)", func(ctx context.Context) {
		_, err := db.Exec(ctx,
			`INSERT INTO beacon_inventory (principal, instance_label, component_name, healthy)
			 VALUES ('oidc:https://idp/|alloy-prod', '10.0.0.1:12345', 'pipe_x', true)`)
		Expect(err).NotTo(HaveOccurred(), "the agent_tokens FK must be gone so an OIDC principal can report")
	})

	It("still enforces identity uniqueness on (principal, instance_label, component_name)", func(ctx context.Context) {
		ins := func() error {
			_, err := db.Exec(ctx,
				`INSERT INTO beacon_inventory (principal, instance_label, component_name, healthy)
				 VALUES ('p1', 'i1', 'c1', true)`)
			return err
		}
		Expect(ins()).To(Succeed())
		Expect(ins()).To(HaveOccurred(), "a re-report of the same component must conflict, not duplicate")
	})

	It("rejects a principal longer than the length CHECK", func(ctx context.Context) {
		long := make([]byte, 513)
		for i := range long {
			long[i] = 'a'
		}
		_, err := db.Exec(ctx,
			`INSERT INTO beacon_inventory (principal, instance_label, component_name, healthy) VALUES ($1, 'i', 'c', true)`,
			string(long))
		Expect(err).To(HaveOccurred())
	})

	It("has no token_id column any more", func(ctx context.Context) {
		var exists bool
		Expect(db.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			                WHERE table_name = 'beacon_inventory' AND column_name = 'token_id')`,
		).Scan(&exists)).To(Succeed())
		Expect(exists).To(BeFalse())
	})

	It("backfills an existing token_id row to its text on up, and restores it on down", func(ctx context.Context) {
		// Land on 0021 (before 0022), insert a token-keyed row the 0010 way,
		// then migrate up to 0022 and confirm the principal is the UUID text.
		db.Close()
		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateTo(ctx, url, 21)).To(Succeed())
		at21, err := pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())

		var tokenID string
		Expect(at21.QueryRow(ctx,
			`INSERT INTO agent_tokens (name, token_hash, created_by) VALUES ('m', '\x00', 't') RETURNING id`,
		).Scan(&tokenID)).To(Succeed())
		_, err = at21.Exec(ctx,
			`INSERT INTO beacon_inventory (token_id, instance_label, component_name, healthy) VALUES ($1, 'i', 'c', true)`, tokenID)
		Expect(err).NotTo(HaveOccurred())
		at21.Close()

		Expect(store.MigrateTo(ctx, url, 22)).To(Succeed())
		at22, err := pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		defer at22.Close()
		var principal string
		Expect(at22.QueryRow(ctx, `SELECT principal FROM beacon_inventory WHERE instance_label = 'i'`).Scan(&principal)).To(Succeed())
		Expect(principal).To(Equal(tokenID), "an existing token_id must backfill to its text form")
	})

	It("drops the principal column on the down migration", func(ctx context.Context) {
		db.Close()
		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		Expect(store.MigrateTo(ctx, url, 21)).To(Succeed())

		probe, err := pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		defer probe.Close()
		var exists bool
		Expect(probe.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.columns
			                WHERE table_name = 'beacon_inventory' AND column_name = 'principal')`,
		).Scan(&exists)).To(Succeed())
		Expect(exists).To(BeFalse(), "down migration should drop principal and restore token_id")
	})
})
