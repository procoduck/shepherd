package store_test

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/store"
)

// Migration 0032_serve_cache_stale_for_revision_header (#296): the served
// header now prints each pipeline's real revision, so every cached served
// config is stale on upgrade. The migration marks every row dirty AND bumps
// its generation (dirty_seq, 0020) so a recompute that read the old
// generation cannot land pre-upgrade bytes over the mark; each collector's
// next poll then recomputes and reloads once. Down is a no-op.
var _ = Describe("Migration: 0032_serve_cache_stale_for_revision_header", Label("integration"), func() {
	var (
		db  *pgxpool.Pool
		url string
	)

	BeforeEach(func(ctx context.Context) {
		url = sharedPG.IsolatedDB(ctx, GinkgoTB())
		Expect(store.MigrateTo(ctx, url, 31)).To(Succeed())
		var err error
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { db.Close() })
	})

	It("marks every cached served config dirty and bumps its generation", func(ctx context.Context) {
		var clusterID string
		Expect(db.QueryRow(ctx, `INSERT INTO clusters (name) VALUES ('m32') RETURNING id`).Scan(&clusterID)).To(Succeed())
		collector := func(role string) string {
			var id string
			Expect(db.QueryRow(ctx,
				`INSERT INTO collectors (cluster_id, role) VALUES ($1, $2) RETURNING id`, clusterID, role,
			).Scan(&id)).To(Succeed())
			return id
		}
		clean, alreadyDirty := collector("metrics"), collector("logs")
		_, err := db.Exec(ctx,
			`INSERT INTO serve_cache (collector_id, content, hash, dirty, dirty_seq) VALUES
			 ($1, '// rev 0 bytes', 'h1', false, 4),
			 ($2, '', '', true, 7)`, clean, alreadyDirty)
		Expect(err).NotTo(HaveOccurred())

		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		db, err = pgxpool.New(ctx, url)
		Expect(err).NotTo(HaveOccurred())

		row := func(id string) (bool, int64, string) {
			var dirty bool
			var seq int64
			var content string
			Expect(db.QueryRow(ctx,
				`SELECT dirty, dirty_seq, content FROM serve_cache WHERE collector_id = $1`, id,
			).Scan(&dirty, &seq, &content)).To(Succeed())
			return dirty, seq, content
		}
		dirty, seq, content := row(clean)
		Expect(dirty).To(BeTrue(), "a clean cached config must be marked stale")
		Expect(seq).To(BeEquivalentTo(5), "the mark must bump the generation, like MarkServeCacheDirty*")
		Expect(content).To(Equal("// rev 0 bytes"), "content stays until the next poll recomputes it")
		dirty, seq, _ = row(alreadyDirty)
		Expect(dirty).To(BeTrue())
		Expect(seq).To(BeEquivalentTo(8))
	})

	It("rolls back to 31 as a no-op", func(ctx context.Context) {
		db.Close()
		Expect(store.MigrateUp(ctx, url)).To(Succeed())
		Expect(store.MigrateTo(ctx, url, 31)).To(Succeed())
	})
})
