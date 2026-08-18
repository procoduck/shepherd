package mgmtapi

import (
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// AuditService implements mgmtv1connect.AuditServiceHandler. Business logic
// moved here from AuditHandler (audit.go), which is now a thin REST shim
// delegating to this service in-process. See docs/api-contract-design.md,
// "Server wiring".
type AuditService struct {
	store  *store.Store
	logger *slog.Logger
}

// NewAuditService constructs an AuditService with the deps AuditHandler uses today.
func NewAuditService(st *store.Store, logger *slog.Logger) *AuditService {
	return &AuditService{store: st, logger: logger}
}

var _ mgmtv1connect.AuditServiceHandler = (*AuditService)(nil)

// defaultAuditLimit/maxAuditLimit mirror the legacy paginationParams
// defaults documented on ListAuditRequest in audit.proto.
const (
	defaultAuditLimit int32 = 25
	maxAuditLimit     int32 = 200
)

// ListAudit lists audit log entries, optionally scoped to an org and
// filtered by actor/action substring match. A malformed/empty org_id
// resolves to SQL NULL (matching legacy orgIDFromParam, which never
// rejected it), returning entries across all orgs. limit outside (0, 200]
// resets to the default of 25 (not clamped to 200 — mirroring
// AuditHandler.List's paginationParams exactly); offset below 0 resets to 0.
func (s *AuditService) ListAudit(ctx context.Context, req *connect.Request[mgmtv1.ListAuditRequest]) (*connect.Response[mgmtv1.ListAuditResponse], error) {
	orgID, _ := parseUUID(req.Msg.GetOrgId()) // invalid/empty org id resolves to NULL, matching legacy orgIDFromParam

	limit := req.Msg.GetLimit()
	if limit <= 0 || limit > maxAuditLimit {
		limit = defaultAuditLimit
	}
	offset := req.Msg.GetOffset()
	if offset < 0 {
		offset = 0
	}

	actor := req.Msg.GetActor()
	action := req.Msg.GetAction()

	rows, err := s.store.Queries.ListAuditLog(ctx, sqlc.ListAuditLogParams{
		Column1: orgID,
		Column2: actor,
		Column3: action,
		Limit:   limit,
		Offset:  offset,
	})
	if err != nil {
		s.logger.Error("list audit log", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list audit log"))
	}

	total, _ := s.store.Queries.CountAuditLog(ctx, sqlc.CountAuditLogParams{ //nolint:errcheck // informational; 0 is safe fallback
		Column1: orgID,
		Column2: actor,
		Column3: action,
	})

	items := make([]*mgmtv1.AuditEntry, len(rows))
	for i := range rows {
		row := rows[i]
		items[i] = &mgmtv1.AuditEntry{
			Id:           row.ID,
			At:           protoTimestamp(row.At),
			Actor:        row.Actor,
			ActorType:    row.ActorType,
			OrgId:        row.OrgID.String(),
			Action:       row.Action,
			ResourceType: row.ResourceType,
			ResourceId:   row.ResourceID,
		}
	}
	return connect.NewResponse(&mgmtv1.ListAuditResponse{Items: items, Total: total}), nil
}
