// Package mgmtapi implements the JSON REST management API (/api).
package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

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

// listResponse wraps a list result in the standard envelope.
type listResponse[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
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

// paginationParams extracts limit/offset from query params with defaults.
func paginationParams(r *http.Request) (limit, offset int) {
	limit = 25
	offset = 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
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

// orgIDFromParam extracts the {org} URL param as a pgtype.UUID.
// Defined in pipelines.go but referenced from multiple files; declared here
// so the compiler sees it once regardless of build order.
// (actual definition is in pipelines.go to avoid duplicate declarations — kept here as doc.)

// auditLog writes a single audit row. Errors are logged at debug level and not
// returned to the caller — audit writes are best-effort side effects.
func auditLog(ctx context.Context, st *store.Store, actor string, orgID pgtype.UUID, action, resType, resID string) {
	_ = st.Queries.InsertAuditLog(ctx, sqlc.InsertAuditLogParams{ //nolint:errcheck // best-effort side effect
		Actor:        actor,
		ActorType:    "user",
		OrgID:        orgID,
		Action:       action,
		ResourceType: resType,
		ResourceID:   resID,
		Detail:       json.RawMessage("{}"),
	})
}
