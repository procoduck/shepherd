package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0041_collector_last_refusal: collectors.last_refused_at /
// last_refused_reason record the agent API's latest refusal of a poll for the
// collector. The reason is enum-like (the refusals a poll can get), the two
// columns are set and cleared together, an existing collector starts with
// none, and an older binary's collector upsert (which names neither) still
// works. Down drops both.
var _ = Describe("Migration: 0041_collector_last_refusal", Label("integration"), func() {
	var (
		db        *pgxpool.Pool
		url       string
		collector string
	)

	reopen := func(ctx context.Context) {
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
	}

	// RAW-SQL-OK: migration specs drive the schema directly.
	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateTo(ctx, url, 40)).To(Succeed())
		reopen(ctx)
		var cluster string
		Expect(db.QueryRow(ctx, `INSERT INTO clusters (name) VALUES ('m41') RETURNING id`).Scan(&cluster)).To(Succeed())
		Expect(db.QueryRow(ctx,
			`INSERT INTO collectors (cluster_id, role) VALUES ($1, 'metrics') RETURNING id`, cluster,
		).Scan(&collector)).To(Succeed())
		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		reopen(ctx)
		DeferCleanup(func() { db.Close() })
	})

	setRefusal := func(ctx context.Context, reason any, at string) error {
		_, err := db.Exec(ctx,
			`UPDATE collectors SET last_refused_reason = $2, last_refused_at = `+at+` WHERE id = $1`,
			collector, reason)
		return err
	}

	It("starts an existing collector with no refusal", func(ctx context.Context) {
		var n int
		Expect(db.QueryRow(ctx,
			`SELECT count(*) FROM collectors WHERE id = $1 AND last_refused_at IS NULL AND last_refused_reason IS NULL`,
			collector).Scan(&n)).To(Succeed())
		Expect(n).To(Equal(1))
	})

	It("accepts the three refusal reasons and nothing else", func(ctx context.Context) {
		for _, r := range []string{"token_unbound", "claim_denied", "role_denied"} {
			Expect(setRefusal(ctx, r, "now()")).To(Succeed(), r)
		}
		Expect(setRefusal(ctx, "unregister_denied", "now()")).To(MatchError(ContainSubstring("violates check constraint")))
	})

	It("sets and clears the two columns together", func(ctx context.Context) {
		Expect(setRefusal(ctx, "token_unbound", "NULL")).To(MatchError(ContainSubstring("collectors_last_refusal_pair")))
		Expect(setRefusal(ctx, nil, "now()")).To(MatchError(ContainSubstring("collectors_last_refusal_pair")))
		Expect(setRefusal(ctx, nil, "NULL")).To(Succeed())
	})

	It("keeps an older binary's collector upsert working", func(ctx context.Context) {
		// v0.18.0's UpsertCollector, verbatim.
		var cluster string
		Expect(db.QueryRow(ctx, `INSERT INTO clusters (name) VALUES ('m41-old') RETURNING id`).Scan(&cluster)).To(Succeed())
		_, err := db.Exec(ctx, `INSERT INTO collectors (cluster_id, role)
VALUES ($1, $2)
ON CONFLICT (cluster_id, role) DO UPDATE SET updated_at = now()
RETURNING id, cluster_id, role, created_at, updated_at, labels`, cluster, "logs")
		Expect(err).NotTo(HaveOccurred())
	})

	It("drops both columns on down", func(ctx context.Context) {
		db.Close()
		Expect(store.MigrateTo(ctx, url, 40)).To(Succeed())
		reopen(ctx)
		var n int
		Expect(db.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.columns WHERE table_name = 'collectors' AND column_name LIKE 'last_refused%'`,
		).Scan(&n)).To(Succeed())
		Expect(n).To(BeZero())
	})
})
