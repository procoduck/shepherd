package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"shepherd/internal/migrations"
)

// pgx5DSN rewrites a standard postgres://... or postgresql://... DSN to use
// the "pgx5" URL scheme, which is how golang-migrate's pgx-based database
// driver (database/pgx/v5) is registered. Callers of this package pass
// ordinary postgres DSNs; this keeps that contract while selecting the pgx
// driver instead of the lib/pq-based one.
func pgx5DSN(databaseURL string) string {
	switch {
	case strings.HasPrefix(databaseURL, "postgres://"):
		return "pgx5://" + strings.TrimPrefix(databaseURL, "postgres://")
	case strings.HasPrefix(databaseURL, "postgresql://"):
		return "pgx5://" + strings.TrimPrefix(databaseURL, "postgresql://")
	default:
		return databaseURL
	}
}

// newMigrate creates a migrate.Migrate instance backed by embedded SQL files.
func newMigrate(databaseURL string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrations.FS, "sql")
	if err != nil {
		return nil, fmt.Errorf("creating migration source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, pgx5DSN(databaseURL))
	if err != nil {
		return nil, fmt.Errorf("creating migrator: %w", err)
	}
	return m, nil
}

// MigrateUp applies all pending migrations and closes the migrator connection.
func MigrateUp(_ context.Context, databaseURL string) error {
	m, err := newMigrate(databaseURL)
	if err != nil {
		return err
	}
	defer func() {
		srcErr, dbErr := m.Close()
		_, _ = srcErr, dbErr
	}()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("running migrations up: %w", err)
	}
	return nil
}

// MigrateDown rolls back one migration and closes the migrator connection.
func MigrateDown(_ context.Context, databaseURL string) error {
	m, err := newMigrate(databaseURL)
	if err != nil {
		return err
	}
	defer func() {
		srcErr, dbErr := m.Close()
		_, _ = srcErr, dbErr
	}()
	if err := m.Steps(-1); err != nil {
		return fmt.Errorf("rolling back migration: %w", err)
	}
	return nil
}

// MigrateTo migrates (up or down) to exactly the given schema version and
// closes the migrator connection. Unlike MigrateDown (always exactly one
// step back from wherever the schema currently is), this lets a caller name
// an absolute target — needed by tests pinned to a specific migration
// (e.g. "the schema as it stood right before 0019") that would otherwise
// silently start asserting on the wrong version the moment a later
// migration is added ahead of them.
func MigrateTo(_ context.Context, databaseURL string, version uint) error {
	m, err := newMigrate(databaseURL)
	if err != nil {
		return err
	}
	defer func() {
		srcErr, dbErr := m.Close()
		_, _ = srcErr, dbErr
	}()
	if err := m.Migrate(version); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrating to version %d: %w", version, err)
	}
	return nil
}

// MigrateStatus prints migration status to stdout and closes the migrator.
// A thin wrapper over MigrateStatusTo so the CLI's existing
// func(context.Context, string) error call shape keeps compiling unchanged.
func MigrateStatus(ctx context.Context, databaseURL string) error {
	return MigrateStatusTo(ctx, os.Stdout, databaseURL)
}

// MigrateStatusTo writes migration status to w and closes the migrator.
// store is a library package: it must not assume its caller wants status on
// stdout (a caller capturing output, e.g. into a CLI's own out-of-band
// writer, or a test asserting on the exact text, could not otherwise get at
// it), so the write goes to an explicit io.Writer instead of fmt.Printf.
func MigrateStatusTo(_ context.Context, w io.Writer, databaseURL string) error {
	m, err := newMigrate(databaseURL)
	if err != nil {
		return err
	}
	defer func() {
		srcErr, dbErr := m.Close()
		_, _ = srcErr, dbErr
	}()
	v, dirty, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("getting migration version: %w", err)
	}
	if _, err := fmt.Fprintf(w, "version=%d dirty=%v\n", v, dirty); err != nil {
		return fmt.Errorf("writing migration status: %w", err)
	}
	return nil
}
