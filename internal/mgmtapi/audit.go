package mgmtapi

import (
	"log/slog"
	"math"
	"net/http"

	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// AuditHandler handles /api/orgs/{org}/audit.
type AuditHandler struct {
	store  *store.Store
	logger *slog.Logger
}

// NewAuditHandler creates an audit handler.
func NewAuditHandler(st *store.Store, logger *slog.Logger) *AuditHandler {
	return &AuditHandler{store: st, logger: logger}
}

type auditResponse struct {
	ID           int64  `json:"id"`
	At           string `json:"at"`
	Actor        string `json:"actor"`
	ActorType    string `json:"actor_type"`
	OrgID        string `json:"org_id,omitempty"`
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
}

// List returns audit log entries.
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := orgIDFromParam(r)
	limit, offset := paginationParams(r)
	if limit > math.MaxInt32 {
		limit = math.MaxInt32
	}
	if offset > math.MaxInt32 {
		offset = math.MaxInt32
	}

	actor := r.URL.Query().Get("actor")
	action := r.URL.Query().Get("action")

	rows, err := h.store.Queries.ListAuditLog(r.Context(), sqlc.ListAuditLogParams{
		Column1: orgID,
		Column2: actor,
		Column3: action,
		Limit:   int32(limit),
		Offset:  int32(offset),
	})
	if err != nil {
		h.logger.Error("list audit log", "err", err)
		respondError(w, http.StatusInternalServerError, "internal", "failed to list audit log")
		return
	}

	total, _ := h.store.Queries.CountAuditLog(r.Context(), sqlc.CountAuditLogParams{ //nolint:errcheck // informational; 0 is safe fallback
		Column1: orgID,
		Column2: actor,
		Column3: action,
	})

	items := make([]auditResponse, len(rows))
	for i := range rows {
		row := rows[i]
		orgIDStr := row.OrgID.String()
		items[i] = auditResponse{
			ID:           row.ID,
			At:           row.At.Time.UTC().Format("2006-01-02T15:04:05Z"),
			Actor:        row.Actor,
			ActorType:    row.ActorType,
			OrgID:        orgIDStr,
			Action:       row.Action,
			ResourceType: row.ResourceType,
			ResourceID:   row.ResourceID,
		}
	}
	respondJSON(w, http.StatusOK, listResponse[auditResponse]{Items: items, Total: int(total)})
}
