package advisorylock_test

import (
	"context"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/advisorylock"
	"shepherd/internal/testutil"
)

var _ = Describe("Lock", Ordered, Label("integration"), func() {
	var (
		pg   *testutil.SharedPostgres
		pool *pgxpool.Pool
	)
	BeforeAll(func() {
		var err error
		pg, err = testutil.StartSharedPostgres(context.Background())
		Expect(err).NotTo(HaveOccurred())
		pool, err = pgxpool.New(context.Background(), pg.IsolatedDB(context.Background(), GinkgoTB()))
		Expect(err).NotTo(HaveOccurred())
	})
	AfterAll(func() {
		if pool != nil {
			pool.Close()
		}
		if pg != nil {
			Expect(pg.Terminate(context.Background())).To(Succeed())
		}
	})

	It("lets one holder per namespace at a time, and namespaces never contend", func() {
		ctx := context.Background()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))

		// Released when the spec returns, pass or fail: a held session keeps
		// its pooled connection checked out, and AfterAll's pool.Close()
		// would wait on it (an Ordered container's last DeferCleanup runs
		// after AfterAll, so it cannot do this).
		var releases []func()
		defer func() {
			for _, r := range releases {
				r()
			}
		}()
		take := func(l *advisorylock.Lock) (func(), bool) {
			unlock, ok, err := l.TryLock(ctx)
			Expect(err).NotTo(HaveOccurred())
			if !ok {
				return func() {}, false
			}
			released := false
			release := func() {
				if !released {
					released = true
					unlock()
				}
			}
			releases = append(releases, release)
			return release, true
		}

		// Two locks on one pool stand in for two replicas: each takes its own
		// session, which is what the advisory lock is scoped to.
		gitA := advisorylock.New(pool, advisorylock.GitSync, "git-sync", logger)
		gitB := advisorylock.New(pool, advisorylock.GitSync, "git-sync", logger)
		route := advisorylock.New(pool, advisorylock.RouteApply, "route-apply", logger)

		unlockA, ok := take(gitA)
		Expect(ok).To(BeTrue())
		_, ok = take(gitB)
		Expect(ok).To(BeFalse(), "a second replica must not sync while the first does")
		_, ok = take(route)
		Expect(ok).To(BeTrue(), "route apply must not wait on git sync")

		unlockA()
		_, ok = take(gitB)
		Expect(ok).To(BeTrue(), "the lock is free again once the first pass ends")
	})
})
