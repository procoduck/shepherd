package mgmtapi

import (
	"errors"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"shepherd/internal/auth"
)

// errInternal is the message returned to the client for mapError's default
// (unrecognized) case. The real error is never sent over the wire for that
// case — only a caller that has already logged err server-side (the
// existing convention across this package, e.g. s.logger.Warn("...", "err",
// err) before returning) has any record of what actually failed, matching
// how every other CodeInternal site in mgmtapi already behaves.
var errInternal = errors.New("internal error")

// The client-facing texts for mapError's Postgres cases. The store's own
// error text (constraint and table names, the SQLSTATE) is never sent: a
// caller wanting a domain message ("an org with that name already exists")
// checks isUniqueViolation itself first, as several handlers do.
var (
	errAlreadyExists     = errors.New("already exists")
	errConflictsRelated  = errors.New("conflicts with a related resource (it no longer exists, or is still in use)")
	errRolloutInProgress = errors.New("server temporarily unavailable (schema mismatch, likely an upgrade in progress); retry")
)

// isSchemaMismatch reports a Postgres error meaning this binary and the
// database schema disagree — a pod on the other side of a migration during a
// rolling upgrade. Retrying succeeds once the rollout settles: Unavailable.
// internal/agentapi applies the same rule to collector RPCs.
func isSchemaMismatch(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "42P10", "42703", "42P01", "42883":
		return true
	default:
		return false
	}
}

// mapError maps the store and auth packages' sentinel errors, plus common
// pgconn failure codes, onto the connect.Code an RPC handler should return.
// It exists because the mapping was previously duplicated ad hoc at ~274
// call sites across mgmtapi (rpc_destination.go alone had ~39), and several
// of them collapsed every possible store error onto one fixed code — e.g. a
// *ByID lookup helper reporting "not found" even when the real failure was
// a connection error, not a missing row. Call mapError on a raw store/auth
// error to get the right code; a caller that wants a nicer, domain-specific
// message for the "this row doesn't exist" case should still check
// errors.Is(err, pgx.ErrNoRows) itself before falling back to mapError for
// everything else (see loadOwnedDestination for the pattern).
func mapError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, pgx.ErrNoRows):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, auth.ErrUnauthenticated):
		return connect.NewError(connect.CodeUnauthenticated, err)
	case errors.Is(err, auth.ErrOrgNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, auth.ErrInvalidOrgID):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, auth.ErrHelmManaged),
		errors.Is(err, auth.ErrEncryptionUnavailable),
		errors.Is(err, auth.ErrNoSettings):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case isUniqueViolation(err):
		return connect.NewError(connect.CodeAlreadyExists, errAlreadyExists)
	case isFKViolation(err):
		return connect.NewError(connect.CodeAlreadyExists, errConflictsRelated)
	case isSchemaMismatch(err):
		return connect.NewError(connect.CodeUnavailable, errRolloutInProgress)
	default:
		return connect.NewError(connect.CodeInternal, errInternal)
	}
}
