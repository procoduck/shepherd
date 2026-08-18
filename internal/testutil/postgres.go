// Package testutil provides shared test helpers including a testcontainers Postgres harness.
package testutil

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// PostgresContainer wraps a testcontainers Postgres instance.
type PostgresContainer struct {
	Container *postgres.PostgresContainer
	URL       string
}

// StartPostgres starts a Postgres 16 container and returns its connection URL.
// The container is automatically stopped when the test ends via t.Cleanup.
// For Ginkgo suites, prefer StartPostgresShared + IsolatedDB instead of calling
// this in BeforeEach — one container per suite, one database per spec.
func StartPostgres(ctx context.Context, t testing.TB) *PostgresContainer {
	t.Helper()

	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("shepherd_test"),
		postgres.WithUsername("shepherd"),
		postgres.WithPassword("shepherd"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		),
	)
	if err != nil {
		t.Fatalf("starting postgres container: %v", err)
	}

	t.Cleanup(func() { //nolint:contextcheck // cleanup uses fresh context intentionally
		if termErr := ctr.Terminate(context.Background()); termErr != nil {
			t.Logf("stopping postgres container: %v", termErr)
		}
	})

	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("getting postgres connection string: %v", err)
	}

	return &PostgresContainer{Container: ctr, URL: url}
}

// SharedPostgres manages a single Postgres container shared across an entire
// Ginkgo suite. Create one in SynchronizedBeforeSuite and terminate in
// SynchronizedAfterSuite.
type SharedPostgres struct {
	Container *postgres.PostgresContainer
	RootURL   string // postgres://shepherd:shepherd@host:port/shepherd_test
}

// StartSharedPostgres starts a Postgres container suitable for sharing
// across all specs in a suite. Call Terminate() in AfterSuite.
func StartSharedPostgres(ctx context.Context) (*SharedPostgres, error) {
	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("shepherd_test"),
		postgres.WithUsername("shepherd"),
		postgres.WithPassword("shepherd"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("starting shared postgres: %w", err)
	}

	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = ctr.Terminate(ctx) //nolint:errcheck // best-effort cleanup on an already-erroring path; original err is returned
		return nil, fmt.Errorf("getting connection string: %w", err)
	}

	return &SharedPostgres{Container: ctr, RootURL: url}, nil
}

// Terminate stops the shared container. Call from SynchronizedAfterSuite.
func (s *SharedPostgres) Terminate(ctx context.Context) error {
	return s.Container.Terminate(ctx)
}

// IsolatedDB creates a fresh database inside the shared container for a single
// test spec. Uses shepherd_test as the TEMPLATE so no per-spec migrations are needed.
// Requires that no connections are held to shepherd_test when called —
// the BeforeSuite migration pool must be closed before specs run.
func (s *SharedPostgres) IsolatedDB(ctx context.Context, t testing.TB) string {
	t.Helper()

	dbName := fmt.Sprintf("test_%d", isolatedDBCounter())

	// Connect to the "postgres" system database so shepherd_test has no active connections.
	postgresURL := replaceDatabaseInURL(s.RootURL, "postgres")
	pool, err := pgxpool.New(ctx, postgresURL)
	if err != nil {
		t.Fatalf("connecting to postgres db: %v", err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s TEMPLATE shepherd_test`, dbName)); err != nil {
		t.Fatalf("creating isolated db %q: %v", dbName, err)
	}

	isolatedURL := replaceDatabaseInURL(s.RootURL, dbName)

	t.Cleanup(func() { //nolint:contextcheck // cleanup uses fresh context intentionally
		pool2, err := pgxpool.New(context.Background(), postgresURL)
		if err != nil {
			t.Logf("cleanup: connecting to postgres db: %v", err)
			return
		}
		defer pool2.Close()
		_, _ = pool2.Exec(context.Background(), //nolint:errcheck // best-effort cleanup
			fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '%s'`, dbName))
		if _, err := pool2.Exec(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, dbName)); err != nil {
			t.Logf("cleanup: dropping isolated db %q: %v", dbName, err)
		}
	})

	return isolatedURL
}

// replaceDatabaseInURL swaps the database name in a postgres DSN.
// Handles both URL form (postgres://user:pass@host/dbname?params) and key-value form.
func replaceDatabaseInURL(dsn, newDB string) string {
	// Simple approach: find the last path segment.
	for i := len(dsn) - 1; i >= 0; i-- {
		if dsn[i] == '/' {
			// Check if this is the host/path separator (after @host:port/).
			base := dsn[:i+1]
			rest := dsn[i+1:]
			// Strip any query params from the rest.
			for j, c := range rest {
				if c == '?' {
					return base + newDB + rest[j:]
				}
			}
			return base + newDB
		}
	}
	return dsn
}

var _isolatedCounter int64

func isolatedDBCounter() int64 {
	_isolatedCounter++
	return _isolatedCounter
}
