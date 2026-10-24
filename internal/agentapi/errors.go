package agentapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgconn"
)

// errInternal and errRolloutInProgress are the only texts an unexpected
// failure reaches a collector with. The 0.22 -> 0.23 upgrade walkthrough had
// collectors polling an old pod log "internal: upserting instance: ERROR:
// there is no unique or exclusion constraint matching the ON CONFLICT
// specification (SQLSTATE 42P10)" — the store's own text, schema details
// included, handed to every agent.
var (
	errInternal          = errors.New("internal error")
	errRolloutInProgress = errors.New("server temporarily unavailable (schema mismatch, likely an upgrade in progress); retry")
)

// isSchemaMismatch reports whether err is a Postgres error that means the
// running binary and the database schema disagree — what a rolling upgrade
// looks like from a pod on the other side of a migration. Retrying (on
// another pod, or after the rollout) succeeds, so it is Unavailable, not
// Internal.
func isSchemaMismatch(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "42P10", // invalid_column_reference: ON CONFLICT names a constraint that is not there
		"42703", // undefined_column
		"42P01", // undefined_table
		"42883": // undefined_function
		return true
	default:
		return false
	}
}

// sanitizeErrors is the collector.v1 handler's innermost interceptor: an
// error a handler did not deliberately shape for the agent — CodeInternal,
// or a plain error Connect would send as CodeUnknown — is logged here with
// its detail and replaced by a generic one. A schema mismatch becomes
// Unavailable (retryable); everything else Internal "internal error". Every
// other code (PermissionDenied refusals, InvalidArgument, Unauthenticated,
// the outage path's Unavailable) is the handler's chosen answer and passes
// unchanged.
func sanitizeErrors(logger *slog.Logger) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			resp, err := next(ctx, req)
			if err == nil {
				return resp, nil
			}
			var cerr *connect.Error
			if errors.As(err, &cerr) && cerr.Code() != connect.CodeInternal && cerr.Code() != connect.CodeUnknown {
				return resp, err
			}
			if isSchemaMismatch(err) {
				logger.Warn("collector request failed on a schema mismatch; answered Unavailable",
					"rpc", req.Spec().Procedure, "err", err)
				return nil, connect.NewError(connect.CodeUnavailable, errRolloutInProgress)
			}
			logger.Error("collector request failed", "rpc", req.Spec().Procedure, "err", err)
			return nil, connect.NewError(connect.CodeInternal, errInternal)
		}
	}
}
