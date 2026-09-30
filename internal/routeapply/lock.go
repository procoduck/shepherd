package routeapply

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"shepherd/internal/advisorylock"
)

// Locker lets exactly one Shepherd replica run a reconcile pass at a time.
// The chart runs two replicas by default; without this both reconciled the
// same HTTPRoutes concurrently, the loser of each get-then-update recorded a
// conflict as `error`, and a route's status flapped between applied and error.
type Locker interface {
	// TryLock returns ok=false, without error, when another replica holds
	// the lock. On ok=true the caller must call unlock when the pass ends.
	TryLock(ctx context.Context) (unlock func(), ok bool, err error)
}

// PGLocker is the production Locker: a Postgres advisory lock (see
// internal/advisorylock).
type PGLocker = advisorylock.Lock

// NewPGLocker returns a Locker backed by pool.
func NewPGLocker(pool *pgxpool.Pool, logger *slog.Logger) *PGLocker {
	return advisorylock.New(pool, advisorylock.RouteApply, "route-apply", logger)
}
