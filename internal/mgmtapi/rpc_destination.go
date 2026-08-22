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
	"shepherd/internal/gateway"
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
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
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
	// Destination and binding writes went unaudited until a post-merge review
	// asked why. They decide where an org's telemetry is sent and, for a
	// binding, under which tenant — squarely the "why did this change"
	// question an audit log exists to answer. auditLog derives the actor
	// (and, for a machine caller, the verified on-behalf-of) from ctx.
	auditLog(ctx, s.store, actorFromCtx(ctx), orgID, "destination.create", "destination", d.ID.String())
	return connect.NewResponse(item), nil
}

// UpdateDestination updates a destination. Matches
// OrgsHandler.UpdateDestination's existing behavior: any store error
// (including a unique-name violation) maps to a generic internal error — the
// legacy handler never special-cased conflicts here the way Create does.
func (s *DestinationService) UpdateDestination(ctx context.Context, req *connect.Request[mgmtv1.UpdateDestinationRequest]) (*connect.Response[mgmtv1.Destination], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
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
	auditLog(ctx, s.store, actorFromCtx(ctx), owned.OrgID, "destination.update", "destination", id.String())
	return connect.NewResponse(item), nil
}

// DeleteDestination deletes a destination, refusing (with a
// destinationInUseError) when it is still referenced by a wizard-managed
// pipeline's wizard_state. Mirrors OrgsHandler.DeleteDestination's raw JSONB
// containment query exactly — sqlc has no equivalent.
func (s *DestinationService) DeleteDestination(ctx context.Context, req *connect.Request[mgmtv1.DeleteDestinationRequest]) (*connect.Response[mgmtv1.DeleteDestinationResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
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
		if isFKViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("destination is still referenced by one or more tenant bindings"))
		}
		s.logger.Warn("delete destination", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete destination"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), owned.OrgID, "destination.delete", "destination", owned.ID.String())
	return connect.NewResponse(&mgmtv1.DeleteDestinationResponse{}), nil
}

// --- Destination templates + tenant bindings (W2, docs/gateway-tier-plan.md §4) ---
//
// A Destination row doubles as a "template" the moment a DestinationBinding
// points at it. A binding contributes exactly one thing on top of its
// template: tenant_id. See destination.proto's DestinationService doc
// comment and 0008_destination_bindings.up.sql for the full design
// rationale (why a child table rather than a flag on destinations, and why
// that is what makes "a binding cannot carry a credential" enforceable
// rather than merely conventional).

var errDestinationBindingNotFound = errors.New("destination binding not found")

// errBindingCredentialOverride is returned when a Create/UpdateDestinationBindingRequest
// tries to set a field that belongs to the template, not the binding. This
// is the control plan §4/item 5 asks to be red-run: comment out the
// rejectCredentialOverride call in CreateDestinationBinding (or
// UpdateDestinationBinding) and a binding can silently acquire its own
// url/secret_name/auth_mode — see rpc_destination_test.go for the red run.
var errBindingCredentialOverride = errors.New("a destination binding may only set tenant_id; url/type/secret_name/secret_namespace/auth_mode/extra belong to the template and cannot be overridden by a binding")

// credentialBearingBindingFields is implemented by both
// CreateDestinationBindingRequest and UpdateDestinationBindingRequest: both
// carry the template's credential-bearing fields for the sole purpose of
// letting rejectCredentialOverride detect and refuse an attempt to set them
// (see destination.proto's doc comment on CreateDestinationBindingRequest —
// a well-behaved client never populates these).
type credentialBearingBindingFields interface {
	GetUrl() string
	GetType() string
	GetSecretName() string
	GetSecretNamespace() string
	GetAuthMode() string
	GetExtra() *structpb.Struct
}

// rejectCredentialOverride refuses a binding create/update request that
// tries to set any field a binding may not carry. Every field here is
// absent from the DestinationBinding message itself and from the
// destination_bindings table (0008_destination_bindings.up.sql) — this
// check exists so the attempt fails loudly (CodeInvalidArgument) instead of
// being silently dropped by protobuf's unknown-field handling if the field
// were absent from the request message entirely.
func rejectCredentialOverride(req credentialBearingBindingFields) error {
	if req.GetUrl() != "" || req.GetType() != "" || req.GetSecretName() != "" ||
		req.GetSecretNamespace() != "" || req.GetAuthMode() != "" || req.GetExtra() != nil {
		return connect.NewError(connect.CodeInvalidArgument, errBindingCredentialOverride)
	}
	return nil
}

// toDestinationBindingProto converts a stored binding row to its proto
// representation.
func toDestinationBindingProto(b sqlc.DestinationBinding) *mgmtv1.DestinationBinding {
	return &mgmtv1.DestinationBinding{
		Id:            b.ID.String(),
		DestinationId: b.DestinationID.String(),
		OrgId:         b.OrgID.String(),
		Name:          b.Name,
		TenantId:      b.TenantID,
		CreatedAt:     protoTimestamp(b.CreatedAt),
		UpdatedAt:     protoTimestamp(b.UpdatedAt),
	}
}

// loadOwnedDestinationBinding fetches a binding by id and enforces that it
// belongs to orgIDStr, mirroring loadOwnedDestination's rationale exactly:
// the authz interceptor only proves the caller may act on the org named in
// the request, so without this an org admin could read/modify/delete
// another org's binding by pairing their own org id with its binding id.
func (s *DestinationService) loadOwnedDestinationBinding(ctx context.Context, orgIDStr, idStr string) (sqlc.DestinationBinding, error) {
	id, err := scanUUID(idStr)
	if err != nil {
		return sqlc.DestinationBinding{}, err
	}
	orgID, err := scanUUID(orgIDStr)
	if err != nil {
		return sqlc.DestinationBinding{}, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	b, err := s.store.Queries.GetDestinationBindingByID(ctx, id)
	if err != nil {
		return sqlc.DestinationBinding{}, connect.NewError(connect.CodeNotFound, errDestinationBindingNotFound)
	}
	if b.OrgID != orgID {
		return sqlc.DestinationBinding{}, connect.NewError(connect.CodeNotFound, errDestinationBindingNotFound)
	}
	return b, nil
}

// ListDestinationBindings lists bindings in an org, optionally filtered to
// one template's bindings.
func (s *DestinationService) ListDestinationBindings(ctx context.Context, req *connect.Request[mgmtv1.ListDestinationBindingsRequest]) (*connect.Response[mgmtv1.ListDestinationBindingsResponse], error) {
	orgID, _ := parseUUID(req.Msg.GetOrgId()) // invalid/empty org id resolves to NULL, matching ListDestinations

	var (
		bindings []sqlc.DestinationBinding
		err      error
	)
	if destIDStr := req.Msg.GetDestinationId(); destIDStr != "" {
		destID, ok := parseUUID(destIDStr)
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid destination id"))
		}
		bindings, err = s.store.Queries.ListDestinationBindingsByOrgAndDestination(ctx, sqlc.ListDestinationBindingsByOrgAndDestinationParams{
			OrgID: orgID, DestinationID: destID,
		})
	} else {
		bindings, err = s.store.Queries.ListDestinationBindingsByOrg(ctx, orgID)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list destination bindings"))
	}
	items := make([]*mgmtv1.DestinationBinding, len(bindings))
	for i := range bindings {
		items[i] = toDestinationBindingProto(bindings[i])
	}
	return connect.NewResponse(&mgmtv1.ListDestinationBindingsResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // org binding counts never approach int32 overflow
}

// GetDestinationBinding returns a binding by id, scoped to the requested org.
func (s *DestinationService) GetDestinationBinding(ctx context.Context, req *connect.Request[mgmtv1.GetDestinationBindingRequest]) (*connect.Response[mgmtv1.DestinationBinding], error) {
	b, err := s.loadOwnedDestinationBinding(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(toDestinationBindingProto(b)), nil
}

// CreateDestinationBinding creates a tenant binding pointing at an existing
// destination (template) in the same org. Refuses (CodeInvalidArgument) any
// attempt to set a credential-bearing field — see rejectCredentialOverride —
// and refuses (CodeNotFound) a destination_id belonging to a different org,
// which would otherwise let an org admin bind to, and later resolve, another
// org's template and read its url/secret reference back out.
func (s *DestinationService) CreateDestinationBinding(ctx context.Context, req *connect.Request[mgmtv1.CreateDestinationBindingRequest]) (*connect.Response[mgmtv1.DestinationBinding], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	if err := rejectCredentialOverride(req.Msg); err != nil {
		return nil, err
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	destID, err := scanUUID(req.Msg.GetDestinationId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid destination id"))
	}

	dest, err := s.store.Queries.GetDestinationByID(ctx, destID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errDestinationNotFound)
	}
	if dest.OrgID != orgID {
		return nil, connect.NewError(connect.CodeNotFound, errDestinationNotFound)
	}

	// A binding's whole purpose is to vary the tenant, so this field decides
	// which tenant a team's telemetry ships under — the same decision
	// orgs.tenant_id makes for routes, and it deserves the same rule rather
	// than a second, looser one. gateway.ValidateTenantID is Grafana Mimir's
	// documented charset; an id Shepherd accepts but the destination rejects
	// fails at ingest, far from the screen where it was typed.
	//
	// Today a binding can only reference a template in the caller's own org,
	// so a bad value is contained. That containment is a property of W2's
	// current shape, not of this field — if platform-owned templates are ever
	// shared across orgs, this check is what stops it becoming D11's hole.
	if err := gateway.ValidateTenantID(req.Msg.GetTenantId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	b, err := s.store.Queries.CreateDestinationBinding(ctx, sqlc.CreateDestinationBindingParams{
		DestinationID: destID,
		OrgID:         orgID,
		Name:          req.Msg.GetName(),
		TenantID:      req.Msg.GetTenantId(),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("destination binding name already exists"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create destination binding"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), orgID, "destination_binding.create", "destination_binding", b.ID.String())
	return connect.NewResponse(toDestinationBindingProto(b)), nil
}

// UpdateDestinationBinding updates a binding's name/tenant_id. Refuses
// (CodeInvalidArgument) any attempt to also set a credential-bearing field —
// see rejectCredentialOverride. destination_id is immutable by design: this
// procedure has no way to change which template a binding resolves against.
func (s *DestinationService) UpdateDestinationBinding(ctx context.Context, req *connect.Request[mgmtv1.UpdateDestinationBindingRequest]) (*connect.Response[mgmtv1.DestinationBinding], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	if err := rejectCredentialOverride(req.Msg); err != nil {
		return nil, err
	}
	owned, err := s.loadOwnedDestinationBinding(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	// A binding's whole purpose is to vary the tenant, so this field decides
	// which tenant a team's telemetry ships under — the same decision
	// orgs.tenant_id makes for routes, and it deserves the same rule rather
	// than a second, looser one. gateway.ValidateTenantID is Grafana Mimir's
	// documented charset; an id Shepherd accepts but the destination rejects
	// fails at ingest, far from the screen where it was typed.
	//
	// Today a binding can only reference a template in the caller's own org,
	// so a bad value is contained. That containment is a property of W2's
	// current shape, not of this field — if platform-owned templates are ever
	// shared across orgs, this check is what stops it becoming D11's hole.
	if err := gateway.ValidateTenantID(req.Msg.GetTenantId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	b, err := s.store.Queries.UpdateDestinationBinding(ctx, sqlc.UpdateDestinationBindingParams{
		ID:       owned.ID,
		Name:     req.Msg.GetName(),
		TenantID: req.Msg.GetTenantId(),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("destination binding name already exists"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update destination binding"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), owned.OrgID, "destination_binding.update", "destination_binding", owned.ID.String())
	return connect.NewResponse(toDestinationBindingProto(b)), nil
}

// DeleteDestinationBinding deletes a tenant binding. Unlike DeleteDestination,
// nothing else references a binding, so there is no in-use check.
func (s *DestinationService) DeleteDestinationBinding(ctx context.Context, req *connect.Request[mgmtv1.DeleteDestinationBindingRequest]) (*connect.Response[mgmtv1.DeleteDestinationBindingResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	owned, err := s.loadOwnedDestinationBinding(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.store.Queries.DeleteDestinationBinding(ctx, owned.ID); err != nil {
		s.logger.Warn("delete destination binding", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete destination binding"))
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), owned.OrgID, "destination_binding.delete", "destination_binding", owned.ID.String())
	return connect.NewResponse(&mgmtv1.DeleteDestinationBindingResponse{}), nil
}

// ResolveDestinationBinding returns a binding merged with its template: the
// template's url/type/secret_name/secret_namespace/auth_mode/extra plus the
// binding's own tenant_id. This is the one query/procedure a serving-time
// consumer should use (see GetResolvedDestinationBinding's doc comment) —
// never a GetDestinationBinding + GetDestination pair assembled by hand,
// which is exactly the seam where a half-resolved row could leak.
func (s *DestinationService) ResolveDestinationBinding(ctx context.Context, req *connect.Request[mgmtv1.ResolveDestinationBindingRequest]) (*connect.Response[mgmtv1.ResolvedDestination], error) {
	id, err := scanUUID(req.Msg.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid id"))
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	row, err := s.store.Queries.GetResolvedDestinationBinding(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errDestinationBindingNotFound)
	}
	if row.OrgID != orgID {
		return nil, connect.NewError(connect.CodeNotFound, errDestinationBindingNotFound)
	}
	extra, err := structFromJSON(row.Extra)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to decode destination extra"))
	}
	return connect.NewResponse(&mgmtv1.ResolvedDestination{
		BindingId:       row.BindingID.String(),
		DestinationId:   row.DestinationID.String(),
		OrgId:           row.OrgID.String(),
		BindingName:     row.BindingName,
		DestinationName: row.DestinationName,
		Type:            row.Type,
		Url:             row.Url,
		TenantId:        row.TenantID,
		SecretName:      row.SecretName,
		SecretNamespace: row.SecretNamespace,
		AuthMode:        row.AuthMode,
		Extra:           extra,
	}), nil
}
