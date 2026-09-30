// Package advisorylock lets exactly one Shepherd replica run a periodic pass
// at a time. The chart runs two replicas by default, and every background
// loop that writes shared state (tenant-route apply, git sync) would
// otherwise run once per replica, racing itself.
package advisorylock

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Namespaces are the classid half of the pg_try_advisory_lock(classid, objid)
// key, fixed and distinct so no two loops contend in the shared keyspace. The
// simulate worker packs "SIM1" (internal/simulate/worker) the same way.
const (
	RouteApply int32 = 0x52544131 // "RTA1"
	GitSync    int32 = 0x47495431 // "GIT1"
)

// Lock is a Postgres session-level advisory lock held on a dedicated pooled
// connection for the length of one pass. A replica that dies mid-pass drops
// its connection, and Postgres releases the lock with it.
type Lock struct {
	pool      *pgxpool.Pool
	namespace int32
	name      string
	logger    *slog.Logger
}

// New returns a Lock on namespace, backed by pool. name appears in errors and
// logs ("route-apply", "git-sync").
func New(pool *pgxpool.Pool, namespace int32, name string, logger *slog.Logger) *Lock {
	return &Lock{pool: pool, namespace: namespace, name: name, logger: logger}
}

// TryLock returns ok=false, without error, when another replica holds the
// lock. On ok=true the caller must call unlock when the pass ends.
func (l *Lock) TryLock(ctx context.Context) (unlock func(), ok bool, err error) {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquiring a connection for the %s lock: %w", l.name, err)
	}
	var got bool
	// RAW-SQL-OK: session-scoped advisory lock primitive, not representable as a sqlc row query
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1, 1)", l.namespace).Scan(&got); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("taking the %s lock: %w", l.name, err)
	}
	if !got {
		conn.Release()
		return nil, false, nil
	}
	return func() {
		// RAW-SQL-OK: session-scoped advisory lock primitive, not representable as a sqlc row query
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1, 1)", l.namespace); err != nil {
			// Not fatal: the lock goes with the connection when it closes.
			l.logger.Warn("advisory unlock failed", "lock", l.name, "err", err)
		}
		conn.Release()
	}, true, nil
}
