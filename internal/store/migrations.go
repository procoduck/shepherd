package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/golang-migrate/migrate/v4"
	pgxv5 "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/stdlib"

	"shepherd/internal/migrations"
)

// newMigrate creates a migrate.Migrate instance backed by embedded SQL files.
//
// The URL is parsed once, by parseDatabaseURL (the same parser New uses), and
// golang-migrate is handed an open database handle rather than the URL: its
// own URL parsing accepts fewer DSNs than the server does (no keyword/value
// form) and its parse errors quote the URL, password included.
//
// Closing the returned migrator closes the database handle: the pgx/v5
// driver's Close closes its dedicated connection and then the *sql.DB.
func newMigrate(databaseURL string) (*migrate.Migrate, error) {
	poolCfg, err := parseDatabaseURL(databaseURL)
	if err != nil {
		return nil, err
	}
	src, err := iofs.New(migrations.FS, "sql")
	if err != nil {
		return nil, fmt.Errorf("creating migration source: %w", err)
	}
	db := stdlib.OpenDB(*poolCfg.ConnConfig)
	driver, err := pgxv5.WithInstance(db, migrationDriverConfig(poolCfg.ConnConfig.Database))
	if err != nil {
		// WithInstance keeps no reference on failure, so the handle is ours
		// to close (its idle connections go with it).
		return nil, errors.Join(fmt.Errorf("creating migrator: %w", err), db.Close(), src.Close())
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		// driver.Close closes the driver's connection and the handle.
		return nil, errors.Join(fmt.Errorf("creating migrator: %w", err), driver.Close(), src.Close())
	}
	return m, nil
}

// migrationDriverConfig is the driver configuration golang-migrate derived
// from a pgx5:// URL with no x-* options, which is what this package passed
// before: the default schema_migrations table in the connection's current
// schema, no statement timeout, single-statement files. DatabaseName only
// keys the advisory lock that serialises concurrent migrators; the URL form
// set it to the URL path ("/<database>"), so it is kept in that form, and
// left empty (resolved with CURRENT_DATABASE(), as before) when the DSN
// names no database.
func migrationDriverConfig(database string) *pgxv5.Config {
	cfg := &pgxv5.Config{
		MigrationsTable:       pgxv5.DefaultMigrationsTable,
		MultiStatementMaxSize: pgxv5.DefaultMultiStatementMaxSize,
	}
	if database != "" {
		cfg.DatabaseName = "/" + database
	}
	return cfg
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
