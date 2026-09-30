package routeapply_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/routeapply"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/testutil"
)

func activeRow() sqlc.TenantRoute {
	return sqlc.TenantRoute{
		ID: pgtype.UUID{Bytes: [16]byte{15: 0xaa}, Valid: true}, TenantID: "acme", Kind: "otlp", Segment: "acme-lock",
		Status: "active", GatewayMode: "operator", GatewayName: "edge", GatewayNamespace: "gateways",
		ApplyStatus: routeapply.StatusPending,
	}
}

type fakeLocker struct {
	ok       bool
	err      error
	unlocked int
}

func (l *fakeLocker) TryLock(context.Context) (func(), bool, error) {
	if l.err != nil || !l.ok {
		return nil, false, l.err
	}
	return func() { l.unlocked++ }, true, nil
}

var _ = Describe("Reconciler with a Locker", func() {
	var (
		ctx context.Context
		st  *fakeStore
		cl  *fakeCluster
		cfg config.RouteApplyConfig
	)
	BeforeEach(func() {
		ctx = context.Background()
		st = &fakeStore{}
		cl = newFakeCluster()
		cfg = config.RouteApplyConfig{
			Enabled: true, Namespace: "shepherd", BackendService: "shepherd-receiver", BackendPort: 4318,
			Interval: time.Minute, AttachTimeout: time.Second,
		}
		st.rows = append(st.rows, activeRow())
	})
	newRec := func(l routeapply.Locker) *routeapply.Reconciler {
		return routeapply.New(st, cl, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), routeapply.WithLocker(l))
	}

	It("runs the pass and releases the lock when it holds it", func() {
		l := &fakeLocker{ok: true}
		newRec(l).Reconcile(ctx)
		Expect(cl.applies).To(Equal(1))
		Expect(l.unlocked).To(Equal(1))
	})

	It("does nothing while another replica holds the lock", func() {
		newRec(&fakeLocker{ok: false}).Reconcile(ctx)
		Expect(cl.applies).To(BeZero())
		Expect(st.writes).To(BeZero(), "the lock holder records outcomes; a waiting replica must not")
	})

	It("does nothing when the lock cannot be taken", func() {
		newRec(&fakeLocker{err: errors.New("db down")}).Reconcile(ctx)
		Expect(cl.applies).To(BeZero())
		Expect(st.writes).To(BeZero())
	})
})

var _ = Describe("PGLocker", Ordered, Label("integration"), func() {
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

	It("lets one replica hold the pass at a time, and the next take it once released", func() {
		ctx := context.Background()
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		// Two lockers on one pool stand in for two replicas: each takes its
		// own session, which is what the advisory lock is scoped to.
		a, b := routeapply.NewPGLocker(pool, logger), routeapply.NewPGLocker(pool, logger)

		// Every lock taken is released when this spec returns, pass or fail
		// (Expect panics, and deferred calls still run). A held session keeps
		// its pooled connection checked out, and AfterAll's pool.Close() waits
		// for it — and in an Ordered container the last spec's DeferCleanup
		// runs AFTER AfterAll, so it would hang there instead of failing.
		var releases []func()
		defer func() {
			for _, r := range releases {
				r()
			}
		}()
		take := func(l *routeapply.PGLocker) (func(), bool) {
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

		unlockA, ok := take(a)
		Expect(ok).To(BeTrue())

		_, ok = take(b)
		Expect(ok).To(BeFalse(), "a second replica must not reconcile while the first does")

		unlockA()
		_, ok = take(b)
		Expect(ok).To(BeTrue(), "the lock is free again once the first pass ends")
	})
})
