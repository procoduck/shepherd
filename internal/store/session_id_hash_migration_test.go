package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0052_session_id_hash: sessions.id switches from the raw cookie
// value to its SHA-256 hex digest. Neither direction can translate existing
// rows (a digest cannot be reversed, and rehashing on the way up was ruled
// out), so both empty the table and everyone signs in again.
var _ = Describe("Migration: 0052_session_id_hash", Label("integration"), func() {
	var (
		db  *pgxpool.Pool
		url string
	)

	reopen := func(ctx context.Context) {
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
	}

	insertSession := func(ctx context.Context, id string) {
		// RAW-SQL-OK: migration specs drive the schema directly.
		_, err := db.Exec(ctx,
			`INSERT INTO sessions (id, user_oid, email, display_name, group_ids, expires_at, source)
			 VALUES ($1, 'oid', 'u@example.com', 'U', '[]'::jsonb, now() + interval '1 hour', 'test')`, id)
		Expect(err).NotTo(HaveOccurred())
	}

	count := func(ctx context.Context) int {
		var n int
		// RAW-SQL-OK: migration specs drive the schema directly.
		Expect(db.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&n)).To(Succeed())
		return n
	}

	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateTo(ctx, url, 51)).To(Succeed())
		reopen(ctx)
		DeferCleanup(func() { db.Close() })
	})

	It("up removes every session written before it", func(ctx context.Context) {
		insertSession(ctx, "test-session-a")
		insertSession(ctx, "test-session-b")
		Expect(count(ctx)).To(Equal(2))

		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		reopen(ctx)
		Expect(count(ctx)).To(BeZero())
	})

	It("down removes every session written after it", func(ctx context.Context) {
		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		reopen(ctx)
		insertSession(ctx, "test-session-c")
		Expect(count(ctx)).To(Equal(1))

		db.Close()
		Expect(store.MigrateTo(ctx, url, 51)).To(Succeed())
		reopen(ctx)
		Expect(count(ctx)).To(BeZero())
	})
})
