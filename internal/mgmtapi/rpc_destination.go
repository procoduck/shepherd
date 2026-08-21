package mgmtapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
)

// DestinationService implements mgmtv1connect.DestinationServiceHandler.
// Business logic moved here from OrgsHandler's destination methods
// (orgs.go), which are now thin REST shims delegating to these methods
// in-process. See docs/archive/api-contract-design.md, "Server wiring".
type DestinationService struct {
	store  *store.Store
	logger *slog.Logger
}

// NewDestinationService constructs a DestinationService with the deps OrgsHandler uses today.
func NewDestinationService(st *store.Store, logger *slog.Logger) *DestinationService {
	return &DestinationService{store: st, logger: logger}
}

var _ mgmtv1connect.DestinationServiceHandler = (*DestinationService)(nil)

// destinationInUseError indicates a destination cannot be deleted because it
// is referenced by one or more wizard-managed pipelines. The REST shim
// (orgs.go's DeleteDestination) detects this via errors.As and renders the
// legacy {"error":{"code":"in_use",...}} envelope exactly — the frontend
// (web/src/pages/DestinationsPage.tsx) and the Ginkgo REST suite both key on
// that literal code string, so it cannot be replaced by a generic connect
// code's default rendering (WriteConnectError would render CodeAlreadyExists
// as "already_exists").
type destinationInUseError struct {
	message string
}

func (e *destinationInUseError) Error() string { return e.message }

// destinationExtraJSON converts a CreateDestination/UpdateDestination
// request's extra Struct to the jsonb bytes stored in the destinations
// table. A nil/absent Struct (extra not provided) stores "{}", matching
// OrgsHandler's legacy `if len(extra) == 0 { extra = json.RawMessage("{}") }`
// default.
func destinationExtraJSON(extra *structpb.Struct) ([]byte, error) {
	if extra == nil {
		return []byte("{}"), nil
	}
	b, err := protojson.Marshal(extra)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 {
		return []byte("{}"), nil
	}
	return b, nil
}

// toDestinationProto converts a stored destination row to its proto
// representation. Timestamps are truncated to whole seconds so protojson's
// RFC3339 rendering matches the legacy handlers' fixed
// "2006-01-02T15:04:05Z" formatting exactly (see protoTimestamp in
// rpc_gitops.go).
func toDestinationProto(d sqlc.Destination) (*mgmtv1.Destination, error) {
	extra, err := structFromJSON(d.Extra)
	if err != nil {
		return nil, err
	}
	return &mgmtv1.Destination{
		Id:              d.ID.String(),
		OrgId:           d.OrgID.String(),
		Name:            d.Name,
		Type:            d.Type,
		Url:             d.Url,
		TenantId:        d.TenantID,
		SecretName:      d.SecretName,
		SecretNamespace: d.SecretNamespace,
		AuthMode:        d.AuthMode,
		Extra:           extra,
		CreatedAt:       protoTimestamp(d.CreatedAt),
		UpdatedAt:       protoTimestamp(d.UpdatedAt),
	}, nil
}

// ListDestinations lists destinations in an org. Errors from the store are
// swallowed to an empty list, matching OrgsHandler.ListDestinations's
// existing `//nolint:errcheck // empty is safe fallback` behavior.
func (s *DestinationService) ListDestinations(ctx context.Context, req *connect.Request[mgmtv1.ListDestinationsRequest]) (*connect.Response[mgmtv1.ListDestinationsResponse], error) {
	orgID, _ := parseUUID(req.Msg.GetOrgId())                     // invalid/empty org id resolves to NULL, matching legacy orgIDFromParam
	dests, _ := s.store.Queries.ListDestinationsByOrg(ctx, orgID) //nolint:errcheck // empty is safe fallback
	items := make([]*mgmtv1.Destination, len(dests))
	for i := range dests {
		item, err := toDestinationProto(dests[i])
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, errors.New("failed to decode destination"))
		}
		items[i] = item
	}
	return connect.NewResponse(&mgmtv1.ListDestinationsResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // org destination counts never approach int32 overflow
}

// loadOwnedDestination fetches a destination by id and enforces that it belongs to
// orgIDStr. The authz interceptor only proves the caller may act on the org NAMED IN
// THE REQUEST, so without this an org admin could read, modify or delete another
// org's destination by pairing their own org id with its destination id. A mismatch
// is NotFound rather than PermissionDenied so the response does not confirm the
// destination exists.
func (s *DestinationService) loadOwnedDestination(ctx context.Context, orgIDStr, idStr string) (sqlc.Destination, error) {
	id, err := scanUUID(idStr)
	if err != nil {
		return sqlc.Destination{}, err
	}
	orgID, err := scanUUID(orgIDStr)
	if err != nil {
		return sqlc.Destination{}, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	d, err := s.store.Queries.GetDestinationByID(ctx, id)
	if err != nil {
		return sqlc.Destination{}, connect.NewError(connect.CodeNotFound, errDestinationNotFound)
	}
	if d.OrgID != orgID {
		return sqlc.Destination{}, connect.NewError(connect.CodeNotFound, errDestinationNotFound)
	}
	return d, nil
}

var errDestinationNotFound = errors.New("destination not found")

// GetDestination returns a destination by id, scoped to the requested org.
func (s *DestinationService) GetDestination(ctx context.Context, req *connect.Request[mgmtv1.GetDestinationRequest]) (*connect.Response[mgmtv1.Destination], error) {
	d, err := s.loadOwnedDestination(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	item, err := toDestinationProto(d)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to decode destination"))
	}
	return connect.NewResponse(item), nil
}

// CreateDestination creates a destination.
func (s *DestinationService) CreateDestination(ctx context.Context, req *connect.Request[mgmtv1.CreateDestinationRequest]) (*connect.Response[mgmtv1.Destination], error) {
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	extraJSON, err := destinationExtraJSON(req.Msg.GetExtra())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid extra"))
	}
	d, err := s.store.Queries.CreateDestination(ctx, sqlc.CreateDestinationParams{
		OrgID:           orgID,
		Name:            req.Msg.GetName(),
		Type:            req.Msg.GetType(),
		Url:             req.Msg.GetUrl(),
		TenantID:        req.Msg.GetTenantId(),
		SecretName:      req.Msg.GetSecretName(),
		SecretNamespace: req.Msg.GetSecretNamespace(),
		AuthMode:        req.Msg.GetAuthMode(),
		Extra:           extraJSON,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("destination name already exists"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create destination"))
	}
	item, err := toDestinationProto(d)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to decode destination"))
	}
	return connect.NewResponse(item), nil
}

// UpdateDestination updates a destination. Matches
// OrgsHandler.UpdateDestination's existing behavior: any store error
// (including a unique-name violation) maps to a generic internal error — the
// legacy handler never special-cased conflicts here the way Create does.
func (s *DestinationService) UpdateDestination(ctx context.Context, req *connect.Request[mgmtv1.UpdateDestinationRequest]) (*connect.Response[mgmtv1.Destination], error) {
	owned, err := s.loadOwnedDestination(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	id := owned.ID
	extraJSON, err := destinationExtraJSON(req.Msg.GetExtra())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid extra"))
	}
	d, err := s.store.Queries.UpdateDestination(ctx, sqlc.UpdateDestinationParams{
		ID:              id,
		Name:            req.Msg.GetName(),
		Type:            req.Msg.GetType(),
		Url:             req.Msg.GetUrl(),
		TenantID:        req.Msg.GetTenantId(),
		SecretName:      req.Msg.GetSecretName(),
		SecretNamespace: req.Msg.GetSecretNamespace(),
		AuthMode:        req.Msg.GetAuthMode(),
		Extra:           extraJSON,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update destination"))
	}
	item, err := toDestinationProto(d)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to decode destination"))
	}
	return connect.NewResponse(item), nil
}

// DeleteDestination deletes a destination, refusing (with a
// destinationInUseError) when it is still referenced by a wizard-managed
// pipeline's wizard_state. Mirrors OrgsHandler.DeleteDestination's raw JSONB
// containment query exactly — sqlc has no equivalent.
func (s *DestinationService) DeleteDestination(ctx context.Context, req *connect.Request[mgmtv1.DeleteDestinationRequest]) (*connect.Response[mgmtv1.DeleteDestinationResponse], error) {
	owned, err := s.loadOwnedDestination(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	id := owned.ID

	// RAW-SQL-OK: JSONB containment check on wizard_state — no sqlc equivalent
	rows, err := s.store.Pool().Query(ctx,
		`SELECT name FROM pipelines
		 WHERE wizard_state IS NOT NULL
		 AND wizard_state @> jsonb_build_object('destination_id', $1::text)
		 ORDER BY name`,
		id.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to check destination references"))
	}
	var refNames []string
	for rows.Next() {
		var name string
		if scanErr := rows.Scan(&name); scanErr == nil {
			refNames = append(refNames, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to scan references"))
	}
	if len(refNames) > 0 {
		msg := fmt.Sprintf("referenced by %d wizard pipeline(s): %s", len(refNames), strings.Join(refNames, ", "))
		return nil, connect.NewError(connect.CodeAlreadyExists, &destinationInUseError{message: msg})
	}

	if err := s.store.Queries.DeleteDestination(ctx, id); err != nil {
		s.logger.Warn("delete destination", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete destination"))
	}
	return connect.NewResponse(&mgmtv1.DeleteDestinationResponse{}), nil
}
