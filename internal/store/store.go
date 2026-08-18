// Package store contains sqlc-generated queries and thin repository wrappers.
package store

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/tracelog"

	"shepherd/internal/config"
	"shepherd/internal/store/sqlc"
)

// Store wraps the sqlc Queries with a pgxpool connection pool.
type Store struct {
	pool    *pgxpool.Pool
	Queries *sqlc.Queries
}

// New creates a Store and opens a connection pool to the database.
func New(ctx context.Context, cfg *config.DatabaseConfig, loggers ...*slog.Logger) (*Store, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parsing database URL: %w", err)
	}
	poolCfg.MaxConns = cfg.MaxConns
	logger := slog.New(slog.DiscardHandler)
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	poolCfg.ConnConfig.Tracer = &tracelog.TraceLog{Logger: tracelog.LoggerFunc(func(ctx context.Context, level tracelog.LogLevel, msg string, data map[string]any) {
		args := make([]any, 0, len(data)*2)
		for key, value := range data {
			args = append(args, key, value)
		}
		switch level {
		case tracelog.LogLevelError:
			logger.ErrorContext(ctx, msg, args...)
		case tracelog.LogLevelWarn:
			logger.WarnContext(ctx, msg, args...)
		default:
			logger.DebugContext(ctx, msg, args...)
		}
	}), LogLevel: tracelog.LogLevelWarn}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("creating connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return &Store{
		pool:    pool,
		Queries: sqlc.New(pool),
	}, nil
}

// Close closes the connection pool.
func (s *Store) Close() {
	s.pool.Close()
}

// Pool returns the underlying pgxpool for transactions.
func (s *Store) Pool() *pgxpool.Pool {
	return s.pool
}
