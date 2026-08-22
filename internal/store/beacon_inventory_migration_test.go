package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0010_beacon_inventory (docs/gateway-tier-plan.md §4 W5, D6):
// beacon inventory storage. These specs exercise the schema-level controls
// that are the migration-level half of G5's "stores no raw samples" and
// "inventory rows keyed by collector instance" obligations:
//
//  1. The length CHECK constraints on instance_label/component_name — a
//     second, independent enforcement point mirroring 0009_tenant_routes'
//     documented precedent.
//  2. The FK to agent_tokens(id) — a row cannot exist for a token Shepherd
//     never issued.
//  3. The UNIQUE(token_id, instance_label, component_name) constraint that
//     makes UpsertBeaconComponent an upsert rather than a grower.
var _ = Describe("Migration: 0010_beacon_inventory", Label("integration"), func() {
	var (
		db      *pgxpool.Pool
		tokenID string
	)

	BeforeEach(func(ctx context.Context) {
		url := sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateUp(ctx, url)).To(Succeed())

		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		Expect(db.QueryRow(ctx,
			`INSERT INTO agent_tokens (name, token_hash, created_by) VALUES ('beacon-migration-test', '\x00', 'test') RETURNING id`,
		).Scan(&tokenID)).To(Succeed())
	})

	insertRow := func(ctx context.Context, token, instanceLabel, componentName string, healthy bool) error {
		_, err := db.Exec(ctx,
			`INSERT INTO beacon_inventory (token_id, instance_label, component_name, healthy) VALUES ($1, $2, $3, $4)`,
			token, instanceLabel, componentName, healthy)
		return err
	}

	It("accepts a well-formed row", func(ctx context.Context) {
		Expect(insertRow(ctx, tokenID, "10.0.0.1:12345", "pipe_x", true)).To(Succeed())
	})

	// Red run: delete the `CHECK (length(instance_label) BETWEEN 1 AND 256)`
	// clause from 0010_beacon_inventory.up.sql. This spec then fails because
	// the empty-string insert below succeeds instead of failing with
	// SQLSTATE 23514 (check_violation).
	It("rejects an empty instance_label", func(ctx context.Context) {
		err := insertRow(ctx, tokenID, "", "pipe_x", true)
		Expect(err).To(HaveOccurred())
		Expect(pgErrCode(err)).To(Equal("23514"))
	})

	It("rejects an empty component_name", func(ctx context.Context) {
		err := insertRow(ctx, tokenID, "10.0.0.1:12345", "", true)
		Expect(err).To(HaveOccurred())
		Expect(pgErrCode(err)).To(Equal("23514"))
	})

	// Red run: delete the FK clause (`REFERENCES agent_tokens(id)`) from
	// 0010_beacon_inventory.up.sql. This spec then fails because the insert
	// against a nonexistent token succeeds instead of failing with SQLSTATE
	// 23503 (foreign_key_violation).
	It("rejects a row for a token that does not exist", func(ctx context.Context) {
		err := insertRow(ctx, "00000000-0000-0000-0000-000000000000", "10.0.0.1:12345", "pipe_x", true)
		Expect(err).To(HaveOccurred())
		Expect(pgErrCode(err)).To(Equal("23503"))
	})

	// Red run: delete the UNIQUE(token_id, instance_label, component_name)
	// constraint. This spec then fails because the second insert succeeds
	// (creating a duplicate row UpsertBeaconComponent's ON CONFLICT clause
	// could never have targeted) instead of failing with SQLSTATE 23505.
	It("enforces uniqueness on (token_id, instance_label, component_name)", func(ctx context.Context) {
		Expect(insertRow(ctx, tokenID, "10.0.0.1:12345", "pipe_x", true)).To(Succeed())
		err := insertRow(ctx, tokenID, "10.0.0.1:12345", "pipe_x", false)
		Expect(err).To(HaveOccurred())
		Expect(pgErrCode(err)).To(Equal("23505"))
	})

	It("allows the same instance_label/component_name pair under a different token", func(ctx context.Context) {
		var otherToken string
		Expect(db.QueryRow(ctx,
			`INSERT INTO agent_tokens (name, token_hash, created_by) VALUES ('beacon-migration-test-2', '\x00', 'test') RETURNING id`,
		).Scan(&otherToken)).To(Succeed())

		Expect(insertRow(ctx, tokenID, "10.0.0.1:12345", "pipe_x", true)).To(Succeed())
		Expect(insertRow(ctx, otherToken, "10.0.0.1:12345", "pipe_x", true)).To(Succeed())
	})
})
