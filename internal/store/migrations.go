package store

import (
	"context"
	"errors"
	"fmt"
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

// MigrateStatus prints migration status to stdout and closes the migrator.
func MigrateStatus(_ context.Context, databaseURL string) error {
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
	fmt.Printf("version=%d dirty=%v\n", v, dirty)
	return nil
}
