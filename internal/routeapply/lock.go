package routeapply

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
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

// routeApplyAdvisoryLockNamespace is the classid half of the
// pg_try_advisory_lock(classid, objid) key. Fixed and distinct from the
// simulate worker's "SIM1" so the two never contend in the shared keyspace.
const routeApplyAdvisoryLockNamespace = 0x52544131 // "RTA1" packed into an int32

// PGLocker is a Postgres session-level advisory lock held on a dedicated
// pooled connection for the length of one pass (the same pattern
// internal/simulate/worker uses). A replica that dies mid-pass drops its
// connection, and Postgres releases the lock with it.
type PGLocker struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewPGLocker returns a Locker backed by pool.
func NewPGLocker(pool *pgxpool.Pool, logger *slog.Logger) *PGLocker {
	return &PGLocker{pool: pool, logger: logger}
}

// TryLock implements Locker.
func (l *PGLocker) TryLock(ctx context.Context) (func(), bool, error) {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquiring a connection for the route-apply lock: %w", err)
	}
	var got bool
	// RAW-SQL-OK: session-scoped advisory lock primitive, not representable as a sqlc row query
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, 1)", int32(routeApplyAdvisoryLockNamespace)).Scan(&got); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("taking the route-apply lock: %w", err)
	}
	if !got {
		conn.Release()
		return nil, false, nil
	}
	return func() {
		// RAW-SQL-OK: session-scoped advisory lock primitive, not representable as a sqlc row query
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1, 1)", int32(routeApplyAdvisoryLockNamespace)); err != nil {
			// Not fatal: the lock goes with the connection when it closes.
			l.logger.Warn("route-apply advisory unlock failed", "err", err)
		}
		conn.Release()
	}, true, nil
}
