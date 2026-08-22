// Package mgmtapi implements the JSON REST management API (/api).
package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// respondJSON writes v as a JSON response with the given status code.
// Encode errors are logged at debug level and otherwise ignored — the response
// header has already been sent, so there is nothing else to do.
func respondJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return // response headers have already been sent
	}
}

// respondError writes a standard error envelope.
func respondError(w http.ResponseWriter, status int, code, message string) {
	respondJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

// decodeJSON decodes the request body into v, responding with 400 on error.
// Returns false if the decode failed (handler should return immediately).
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		respondError(w, http.StatusBadRequest, "bad_request", "invalid JSON: "+err.Error())
		return false
	}
	return true
}

// isUniqueViolation returns true for PostgreSQL unique-constraint violations (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// isFKViolation reports whether err is a PostgreSQL foreign key violation (SQLSTATE 23503).
func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23503"
	}
	return false
}

// auditLog writes a single audit row. Errors are logged at debug level and not
// returned to the caller — audit writes are best-effort side effects.
func auditLog(ctx context.Context, st *store.Store, actor string, orgID pgtype.UUID, action, resType, resID string) {
	auditLogDetail(ctx, st, actor, "user", orgID, action, resType, resID, nil)
}

// auditLogDetail is auditLog's richer counterpart, for the rare row that
// benefits from more than "{}" (e.g. simulate.run.create's node/edge
// counts). actorType lets a background component audit as "system" — see
// gitsync/reconciler.go's direct InsertAuditLog call for the same need.
//
// G13 (docs/gateway-tier-plan.md, two-part attribution) is enforced HERE,
// not at each of auditLog/auditLogDetail's ~15 call sites: when ctx carries
// a machine (service account) identity, actorType and on_behalf_of are
// overridden from it regardless of what the caller passed, so every
// existing and future audit call site gets G13 for free — the same "one
// function every path calls" shape beacon.AppendBaseline used for D6 (see
// docs/gateway-tier-plan.md §10's 2026-08-22 entry). By the time this runs,
// requireWriteAuthorized has already rejected any machine write with no
// on-behalf-of (machine_auth.go) — a "service_account" actor_type row here
// is therefore never missing on_behalf_of on any path that reaches this
// function after that guard, which is every write path in this package.
func auditLogDetail(ctx context.Context, st *store.Store, actor, actorType string, orgID pgtype.UUID, action, resType, resID string, detail any) {
	var onBehalfOf pgtype.Text
	if sa, ok := serviceAccountFromCtx(ctx); ok {
		actorType = "service_account"
		actor = "svcacct:" + sa.Name
		if sa.OnBehalfOf != "" {
			onBehalfOf = pgtype.Text{String: sa.OnBehalfOf, Valid: true}
		}
	}
	detailJSON, err := json.Marshal(detail)
	if err != nil || detail == nil {
		detailJSON = json.RawMessage("{}")
	}
	_ = st.Queries.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{ //nolint:errcheck // best-effort side effect
		Actor:        actor,
		ActorType:    actorType,
		OrgID:        orgID,
		Action:       action,
		ResourceType: resType,
		ResourceID:   resID,
		Detail:       detailJSON,
		OnBehalfOf:   onBehalfOf,
	})
}
