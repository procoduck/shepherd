package mgmtapi_test

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// The serve-cache write is a compare-and-swap on the dirty generation. This
// is the interleaving it exists for, played out explicitly: a recompute
// reads generation N, a newer mark bumps it to N+1 while that recompute is
// still computing, and the recompute's write must then be REFUSED — the
// old query (WHERE dirty = true only) accepted it, cleared the flag, and left
// collectors on stale content until the next change; the restore-flips-
// enabled spec in rpc_revisions_test.go caught exactly that under CI load.
// Red run: dropping `AND serve_cache.dirty_seq = $4` from
// UpsertServeCacheConditional turns "refuses the stale write" red.
var _ = Describe("serve cache: compare-and-swap on the dirty generation", Label("integration"), func() {
	var (
		ctx    context.Context
		cancel context.CancelFunc
		st     *store.Store
		coll   sqlc.Collector
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		dbURL := sharedPG.IsolatedDB(ctx, GinkgoTB())
		var err error
		st, err = store.New(ctx, &config.DatabaseConfig{URL: dbURL, MaxConns: 5}, slog.Default())
		Expect(err).NotTo(HaveOccurred())
		org, err := st.Queries.CreateOrg(ctx, sqlc.CreateOrgParams{Name: "gen-org", DisplayName: "Gen", AdminGroupID: "g"})
		Expect(err).NotTo(HaveOccurred())
		cluster, err := st.Queries.UpsertCluster(ctx, "gen-cluster")
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.ClaimCluster(ctx, sqlc.ClaimClusterParams{ID: cluster.ID, OrgID: org.ID})).To(Succeed())
		coll, err = st.Queries.UpsertCollector(ctx, sqlc.UpsertCollectorParams{ClusterID: cluster.ID, Role: "metrics"})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		st.Close()
		cancel()
	})

	write := func(content string, seq int64) (sqlc.ServeCache, error) {
		return st.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
			CollectorID: coll.ID, Content: content, Hash: "h-" + content, DirtySeq: seq,
		})
	}

	It("inserts a first row at generation 0, and every mark bumps the generation", func() {
		row, err := write("v1", 0)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.DirtySeq).To(BeEquivalentTo(0))
		Expect(row.Dirty).To(BeFalse())

		Expect(st.Queries.MarkServeCacheDirty(ctx, coll.ID)).To(Succeed())
		Expect(st.Queries.MarkServeCacheDirty(ctx, coll.ID)).To(Succeed())
		row, err = st.Queries.GetServeCache(ctx, coll.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.Dirty).To(BeTrue())
		Expect(row.DirtySeq).To(BeEquivalentTo(2))
	})

	It("refuses the stale write and accepts the one that observed the newer mark", func() {
		_, err := write("v1", 0)
		Expect(err).NotTo(HaveOccurred())

		// Recompute A reads generation 1 ...
		Expect(st.Queries.MarkServeCacheDirty(ctx, coll.ID)).To(Succeed())
		seqSeenByA := int64(1)
		// ... a newer change marks the row again (generation 2) while A is
		// still computing against the state it read ...
		Expect(st.Queries.MarkServeCacheDirty(ctx, coll.ID)).To(Succeed())

		// ... so A's write must be refused, leaving the row dirty for the
		// recompute that saw generation 2.
		_, err = write("stale-from-A", seqSeenByA)
		Expect(errors.Is(err, pgx.ErrNoRows)).To(BeTrue(), "a write against a superseded generation must be refused, got: %v", err)
		row, err := st.Queries.GetServeCache(ctx, coll.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.Dirty).To(BeTrue(), "the stale write must not clear the newer dirty flag")
		Expect(row.Content).To(Equal("v1"))

		row, err = write("fresh-from-B", 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.Dirty).To(BeFalse())
		Expect(row.Content).To(Equal("fresh-from-B"))
	})

	It("marks by org and by cluster bump the generation like the per-collector mark", func() {
		_, err := write("v1", 0)
		Expect(err).NotTo(HaveOccurred())
		orgID, err := st.Queries.GetCollectorOrgID(ctx, coll.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(st.Queries.MarkServeCacheDirtyByOrg(ctx, orgID)).To(Succeed())
		Expect(st.Queries.MarkServeCacheDirtyByCluster(ctx, coll.ClusterID)).To(Succeed())
		row, err := st.Queries.GetServeCache(ctx, coll.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(row.DirtySeq).To(BeEquivalentTo(2))
		var _ pgtype.UUID = orgID
	})
})
