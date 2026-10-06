package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/gateway"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/wizard"
)

// DestinationService implements mgmtv1connect.DestinationServiceHandler.
// See docs/archive/api-contract-design.md, "Server wiring".
type DestinationService struct {
	store *store.Store
	// pipelines is the pipeline write path UpdateDestination re-renders a
	// destination's wizard pipelines through (#262): its validation gate,
	// Stage 3 and eager serve-cache recompute. Required, not optional — a
	// DestinationService that could be built without it would be a second
	// update path on which a destination change silently reaches nothing.
	pipelines *PipelineService
	logger    *slog.Logger
}

// NewDestinationService constructs a DestinationService. pipelines is the
// PipelineService whose gate re-rendered wizard pipelines pass.
func NewDestinationService(st *store.Store, pipelines *PipelineService, logger *slog.Logger) *DestinationService {
	return &DestinationService{store: st, pipelines: pipelines, logger: logger}
}

var _ mgmtv1connect.DestinationServiceHandler = (*DestinationService)(nil)

// destinationExtraJSON converts a CreateDestination/UpdateDestination
// request's extra Struct to the jsonb bytes stored in the destinations
// table. A nil/absent Struct (extra not provided) stores "{}", matching
// the pre-Connect REST handler's default.
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
// swallowed to an empty list, matching the pre-Connect REST
// handler's behavior.
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
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.Destination{}, connect.NewError(connect.CodeNotFound, errDestinationNotFound)
		}
		return sqlc.Destination{}, mapError(err)
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

// validDestinationTypes mirrors the destinations.type CHECK constraint in
// 0001_init.up.sql. Validating here turns a wrong type into invalid_argument
// with the accepted values named, instead of letting the INSERT fail and
// surfacing as an internal error.
var validDestinationTypes = []string{"prometheus", "loki", "otlp"}

func validateDestinationType(t string) error {
	if slices.Contains(validDestinationTypes, t) {
		return nil
	}
	return connect.NewError(connect.CodeInvalidArgument,
		fmt.Errorf("invalid destination type %q: expected one of %s", t, strings.Join(validDestinationTypes, ", ")))
}

// destinationScopes reads an oauth2_secret destination's scopes from its
// extra JSON (wizard.ExtraKeyOAuth2Scopes): absent means none, anything but
// a list of non-empty strings is an error.
func destinationScopes(extra []byte) ([]string, error) {
	if len(extra) == 0 {
		return nil, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(extra, &m); err != nil {
		return nil, fmt.Errorf("extra is not a JSON object: %w", err)
	}
	raw, ok := m[wizard.ExtraKeyOAuth2Scopes]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	var scopes []string
	if err := json.Unmarshal(raw, &scopes); err != nil {
		return nil, fmt.Errorf("extra.%s must be a list of strings", wizard.ExtraKeyOAuth2Scopes)
	}
	for _, sc := range scopes {
		if strings.TrimSpace(sc) == "" {
			return nil, fmt.Errorf("extra.%s must not contain an empty scope", wizard.ExtraKeyOAuth2Scopes)
		}
	}
	return scopes, nil
}

// validateDestinationAuth refuses an auth_mode the destinations table does
// not admit, a Secret mode without a valid Secret reference, and malformed
// OAuth2 scopes — at the API, as invalid_argument, rather than as a CHECK
// violation (an internal error) or a wizard render failure much later. The
// Secret's key contract itself is wizard.SecretKeys.
func validateDestinationAuth(mode, secretNamespace, secretName string, extra []byte) error {
	if err := wizard.ValidateSecretRef(wizard.AuthMode(mode), secretNamespace, secretName); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if _, err := destinationScopes(extra); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}

// validateDestinationTLS refuses, as invalid_argument, an extra.tls that
// does not decode strictly or validate (wizard.ParseTLS: unknown keys such
// as insecure_skip_verify, bad references, an illegal CA key), and TLS
// options on a URL that is not https:// (#261).
func validateDestinationTLS(url string, extra []byte) error {
	tls, err := wizard.ParseTLS(extra)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := wizard.ValidateTLSForURL(tls, url); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return nil
}

// wizardDestinations loads an org's destinations as the set a wizard's
// `*_dest_name` fields resolve against (wizard.Destinations). Only the
// non-sensitive columns are carried: a Secret mode names the Secret, the
// collector reads its values at runtime.
//
// A row whose extra does not decode is still loaded, carrying the error in
// LoadErr, and RenderWriter refuses it only when a wizard names it. Failing
// the whole load instead made one bad row (written before the API validated
// extra, or by a path that skipped it) break every wizard render and every
// destination update in the org (#261).
func wizardDestinations(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID) (wizard.Destinations, error) {
	rows, err := q.ListDestinationsByOrg(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("listing destinations: %w", err)
	}
	out := make(wizard.Destinations, len(rows))
	for i := range rows {
		d := rows[i]
		dest := wizard.Destination{
			Name: d.Name, Type: d.Type, URL: d.Url, AuthMode: wizard.AuthMode(d.AuthMode),
			SecretNamespace: d.SecretNamespace, SecretName: d.SecretName, TenantID: d.TenantID,
		}
		if scopes, err := destinationScopes(d.Extra); err != nil {
			dest.LoadErr = err
		} else {
			dest.OAuth2Scopes = scopes
		}
		if tls, err := wizard.ParseTLS(d.Extra); err != nil {
			if dest.LoadErr == nil {
				dest.LoadErr = err
			}
		} else {
			dest.TLS = tls
		}
		// The cross-org tenant guard holds at render time too, not only at
		// save: an app admin can give another org this tenant after the
		// destination was saved (SetOrgTenantID), and from then on rendering
		// it would stamp that org's tenant on this org's data.
		if dest.LoadErr == nil && d.TenantID != "" {
			held, err := q.TenantIDHeldByOtherOrg(ctx, sqlc.TenantIDHeldByOtherOrgParams{TenantID: d.TenantID, OrgID: orgID})
			if err != nil {
				return nil, fmt.Errorf("checking destination %q's tenant: %w", d.Name, err)
			}
			if held {
				dest.LoadErr = errTenantNotAvailable
			}
		}
		out[d.Name] = dest
	}
	return out, nil
}

// errTenantNotAvailable is the cross-org tenant refusal (#261, maintainer
// decision Q2): a destination may send any valid tenant except one another
// org holds as its orgs.tenant_id. Deliberately generic — it does not say
// that another org holds it.
var errTenantNotAvailable = errors.New("tenant_id is not available to this org")

// validateDestinationTenant refuses, as invalid_argument, a tenant_id
// outside Mimir's charset (wizard.ValidateTenant) or one another org holds.
// Empty means no tenant is sent.
func validateDestinationTenant(ctx context.Context, q *sqlc.Queries, orgID pgtype.UUID, tenant string) error {
	if err := wizard.ValidateTenant(tenant); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if tenant == "" {
		return nil
	}
	held, err := q.TenantIDHeldByOtherOrg(ctx, sqlc.TenantIDHeldByOtherOrgParams{TenantID: tenant, OrgID: orgID})
	if err != nil {
		return connect.NewError(connect.CodeInternal, errors.New("failed to check tenant_id"))
	}
	if held {
		return connect.NewError(connect.CodeInvalidArgument, errTenantNotAvailable)
	}
	return nil
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
	if err := validateDestinationType(req.Msg.GetType()); err != nil {
		return nil, err
	}
	extraJSON, err := destinationExtraJSON(req.Msg.GetExtra())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid extra"))
	}
	if err := validateDestinationAuth(req.Msg.GetAuthMode(), req.Msg.GetSecretNamespace(), req.Msg.GetSecretName(), extraJSON); err != nil {
		return nil, err
	}
	if err := validateDestinationTenant(ctx, s.store.Queries, orgID, req.Msg.GetTenantId()); err != nil {
		return nil, err
	}
	if err := validateDestinationTLS(req.Msg.GetUrl(), extraJSON); err != nil {
		return nil, err
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

// UpdateDestination updates a destination and, in the same transaction,
// re-renders every wizard pipeline in the org that names it (#262; see
// rerenderForUpdate). A rename onto a name already in use is already_exists.
func (s *DestinationService) UpdateDestination(ctx context.Context, req *connect.Request[mgmtv1.UpdateDestinationRequest]) (*connect.Response[mgmtv1.Destination], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	owned, err := s.loadOwnedDestination(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	id := owned.ID
	if err := validateDestinationType(req.Msg.GetType()); err != nil {
		return nil, err
	}
	extraJSON, err := destinationExtraJSON(req.Msg.GetExtra())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid extra"))
	}
	if err := validateDestinationAuth(req.Msg.GetAuthMode(), req.Msg.GetSecretNamespace(), req.Msg.GetSecretName(), extraJSON); err != nil {
		return nil, err
	}
	if err := validateDestinationTenant(ctx, s.store.Queries, owned.OrgID, req.Msg.GetTenantId()); err != nil {
		return nil, err
	}
	if err := validateDestinationTLS(req.Msg.GetUrl(), extraJSON); err != nil {
		return nil, err
	}

	// The destination row and every wizard pipeline rendered from it change
	// in ONE transaction (#262): a re-render the gate refuses rolls the
	// destination update back with it, so no pipeline is ever left rendered
	// from a destination that no longer looks like that.
	tx, err := s.store.Pool().Begin(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update destination"))
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // no-op once committed; rollback error on the success path is expected and harmless
	txQ := s.store.Queries.WithTx(tx)

	// The destinations as they stand BEFORE this update — what every
	// affected wizard pipeline's stored text was rendered from, so the
	// hand-edit check (handEdited) compares against the right render.
	before, err := wizardDestinations(ctx, txQ, owned.OrgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	d, err := txQ.UpdateDestination(ctx, sqlc.UpdateDestinationParams{
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
		if isUniqueViolation(err) {
			// A rename onto a name the org already uses. Reported now that a
			// rename is a supported, pipeline-rewriting operation (#262).
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("destination name already exists"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update destination"))
	}

	rerendered, dirtied, err := s.rerenderForUpdate(ctx, txQ, owned, d, before)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		s.logger.Error("update destination: commit", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update destination"))
	}
	if dirtied {
		go s.pipelines.recomputeOrgCaches(context.Background(), owned.OrgID) //nolint:contextcheck,gosec // G118+contextcheck: intentional detached context, as UpdatePipeline
	}

	item, err := toDestinationProto(d)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to decode destination"))
	}
	var detail any
	if len(rerendered) > 0 {
		detail = map[string]any{"rerendered_pipelines": rerendered}
	}
	auditLogDetail(ctx, s.store, actorFromCtx(ctx), "user", owned.OrgID, "destination.update", "destination", id.String(), detail)
	return connect.NewResponse(item), nil
}

// rerenderForUpdate re-renders, inside txQ, every wizard pipeline in the org
// that names the destination before (the stored name, `owned`) — through
// planWizardRerenders' Stage 1-3 gate — against the org's destinations as
// they stand after the update (`updated`, already written to txQ). A rename
// rewrites the name in each pipeline's wizard state too, so the pipeline
// keeps resolving to this destination rather than to nothing. It returns
// the names of the pipelines it rewrote and whether it marked the serve
// cache dirty; any refusal is failed_precondition naming every pipeline and
// why, and the caller's rollback undoes the destination update with it.
//
// before is the org's destinations as they stood before the update (read in
// the same transaction, ahead of the destination write): a pipeline whose
// stored contents differ from its render against them was edited by hand,
// and is refused rather than overwritten (handEdited).
func (s *DestinationService) rerenderForUpdate(ctx context.Context, txQ *sqlc.Queries, owned, updated sqlc.Destination, before wizard.Destinations) (names []string, dirtied bool, err error) {
	pipelines, err := txQ.ListWizardPipelinesReferencingDestination(ctx, sqlc.ListWizardPipelinesReferencingDestinationParams{
		OrgID: owned.OrgID, DestinationName: owned.Name,
	})
	if err != nil {
		s.logger.Error("update destination: list wizard pipelines", "err", err)
		return nil, false, connect.NewError(connect.CodeInternal, errors.New("failed to load the pipelines using this destination"))
	}
	if len(pipelines) == 0 {
		return nil, false, nil
	}
	dests, err := wizardDestinations(ctx, txQ, owned.OrgID)
	if err != nil {
		return nil, false, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	var rename *destinationRename
	if updated.Name != owned.Name {
		rename = &destinationRename{from: owned.Name, to: updated.Name}
	}
	changes, failures := s.pipelines.planWizardRerenders(ctx, owned.OrgID, pipelines, before, dests, rename)
	if len(failures) > 0 {
		return nil, false, withPipelinesDetail(connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("destination %q was not updated: %w", owned.Name, failures)), failures.pipelineRefs())
	}
	detail := rerenderAudit{Reason: "destination.update", DestinationID: owned.ID.String(), DestinationName: updated.Name}
	if rename != nil {
		detail.RenamedFrom = rename.from
	}
	note := fmt.Sprintf("re-rendered: destination %q updated", updated.Name)
	dirtied, err = applyWizardRerenders(ctx, txQ, owned.OrgID, changes, actorFromCtx(ctx), "user", note, detail)
	if err != nil {
		s.logger.Error("update destination: write re-rendered pipelines", "err", err)
		return nil, false, connect.NewError(connect.CodeInternal, errors.New("failed to update destination"))
	}
	for i := range changes {
		names = append(names, changes[i].row.Name)
	}
	return names, dirtied, nil
}

// DeleteDestination deletes a destination, refusing with failed_precondition
// (naming the pipelines) while a wizard pipeline in the org still names it
// in its wizard state (ListWizardPipelinesReferencingDestination) — deleting
// it would leave that pipeline unable to re-render (#262: this used to match
// a `destination_id` key no wizard ever stored, so it never refused).
func (s *DestinationService) DeleteDestination(ctx context.Context, req *connect.Request[mgmtv1.DeleteDestinationRequest]) (*connect.Response[mgmtv1.DeleteDestinationResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	owned, err := s.loadOwnedDestination(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	id := owned.ID

	refs, err := s.store.Queries.ListWizardPipelinesReferencingDestination(ctx, sqlc.ListWizardPipelinesReferencingDestinationParams{
		OrgID: owned.OrgID, DestinationName: owned.Name,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to check destination references"))
	}
	if len(refs) > 0 {
		// The remedies are the two the pipeline page offers — a wizard
		// pipeline's destination is an answer stored with it, which nothing
		// in the UI edits in place. The pipelines also ride along as a
		// detail, which the UI links to their pages (M4).
		names := make([]string, len(refs))
		pRefs := make([]pipelineRef, len(refs))
		for i := range refs {
			names[i] = fmt.Sprintf("%q", refs[i].Name)
			pRefs[i] = pipelineRef{id: refs[i].ID.String(), name: refs[i].Name}
		}
		return nil, withPipelinesDetail(connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
			"destination %q is used by %d wizard pipeline(s): %s — detach them from the wizard or delete them first, each on its pipeline's page",
			owned.Name, len(names), strings.Join(names, ", "))), pRefs)
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
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.DestinationBinding{}, connect.NewError(connect.CodeNotFound, errDestinationBindingNotFound)
		}
		return sqlc.DestinationBinding{}, mapError(err)
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
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errDestinationNotFound)
		}
		return nil, mapError(err)
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
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errDestinationBindingNotFound)
		}
		return nil, mapError(err)
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
