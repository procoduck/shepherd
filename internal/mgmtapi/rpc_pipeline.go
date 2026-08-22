package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/auth"
	"shepherd/internal/beacon"
	"shepherd/internal/merge"
	"shepherd/internal/schema"
	"shepherd/internal/signals"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
	"shepherd/internal/visual"
)

// PipelineService implements mgmtv1connect.PipelineServiceHandler. Business
// logic moved here from PipelinesHandler (pipelines.go), which is now a
// thin REST shim delegating to these methods in-process.
type PipelineService struct {
	store     *store.Store
	validator *validate.Validator
	schema    *schema.Registry
	logger    *slog.Logger
	// beaconBaseline configures D6's baseline pipeline, appended to every
	// collector's served config by recomputeOrgCaches — the eager
	// write-time recompute counterpart to internal/agentapi's lazy
	// recomputeServeCache. docs/gateway-tier-plan.md §10 recorded, for W1,
	// that "two paths produce the same served config; enforcing one of them
	// is not enforcement" — the same is true for D6's baseline, hence this
	// field exists here too rather than only in agentapi.Service. See
	// beacon.AppendBaseline, the single function both paths call.
	beaconBaseline beacon.BaselineConfig
}

// PipelineServiceOption configures optional PipelineService behavior. See
// WithBeaconRemoteWrite.
type PipelineServiceOption func(*PipelineService)

// WithBeaconRemoteWrite enables D6's baseline pipeline on the eager
// recompute path — see internal/agentapi.WithBeaconRemoteWrite, which does
// the identical thing for the lazy path. baseURL is
// config.ServerConfig.BaseURL; "" is equivalent to omitting this option.
func WithBeaconRemoteWrite(baseURL string) PipelineServiceOption {
	return func(s *PipelineService) {
		remoteWriteURL := ""
		if baseURL != "" {
			remoteWriteURL = strings.TrimSuffix(baseURL, "/") + beacon.WritePath
		}
		s.beaconBaseline = beacon.NewBaselineConfig(remoteWriteURL)
	}
}

// NewPipelineService constructs a PipelineService with the deps PipelinesHandler uses today.
func NewPipelineService(st *store.Store, v *validate.Validator, reg *schema.Registry, logger *slog.Logger, opts ...PipelineServiceOption) *PipelineService {
	s := &PipelineService{store: st, validator: v, schema: reg, logger: logger}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

var _ mgmtv1connect.PipelineServiceHandler = (*PipelineService)(nil)

// Sentinel errors for PipelineService. Message text mirrors the legacy
// pipelines.go handler strings exactly so the REST shim's rendered
// {"error":{"message":...}} stays byte-compatible even though the "code"
// value now reflects the connect.Code rather than the old ad-hoc string
// (see shim.go's WriteConnectError / docs/archive/api-contract-design.md's Errors
// mapping table).
var (
	errOrgIDInvalid         = errors.New("invalid org id")
	errPipelineIDInvalid    = errors.New("invalid pipeline id")
	errPipelineNotFound     = errors.New("pipeline not found")
	errPipelineNameRequired = errors.New("name is required")
	errMatchersInvalid      = errors.New("invalid matchers")
	errRenderMismatch       = errors.New("submitted content does not match server render of the graph; use POST /visual/render to get the canonical content")
	errGitSourceReadOnly    = errors.New("git-sourced pipelines are read-only")
	errNameExists           = errors.New("pipeline name already exists in this org")
	errNameCollision        = errors.New("pipeline sanitized name collides with an existing pipeline")
	errCreatePipelineFailed = errors.New("failed to create pipeline")
	errUpdatePipelineFailed = errors.New("failed to update pipeline")
	errDeletePipelineFailed = errors.New("failed to delete pipeline")
	errListPipelinesFailed  = errors.New("failed to list pipelines")
	errListRevisionsFailed  = errors.New("failed to load revisions")
	errWizardStateInvalid   = errors.New("invalid wizard_state")
)

// pipelineValidationError carries Stage1/2 diagnostics for a CreatePipeline
// or UpdatePipeline call that failed the validation gate. The REST shim
// (pipelines.go's writePipelineSaveError) type-asserts on this via errors.As
// to reproduce the legacy {"error":...,"diagnostics":[...]} envelope;
// ValidatePipeline (which never fails — see its doc comment) carries the
// same diagnostics directly on its response instead.
type pipelineValidationError struct {
	Diagnostics []validate.Diagnostic
}

func (e *pipelineValidationError) Error() string { return "pipeline failed validation" }

// diagnosticsToProto converts internal/validate diagnostics to their
// shepherd.mgmt.v1 proto representation (field-for-field identical shape).
func diagnosticsToProto(diags []validate.Diagnostic) []*mgmtv1.Diagnostic {
	out := make([]*mgmtv1.Diagnostic, len(diags))
	for i, d := range diags {
		out[i] = &mgmtv1.Diagnostic{
			Line:    int32(d.Line), //nolint:gosec // validate diagnostics are bounded by file size, not attacker-controlled
			Col:     int32(d.Col),  //nolint:gosec // same
			Message: d.Message,
			Stage:   int32(d.Stage), //nolint:gosec // stage is 1..3
		}
	}
	return out
}

// pipelineToProto mirrors pipelineToResponse: it never populates Revision
// (always 0) and only populates Revisions when the caller (GetPipeline)
// explicitly attaches them.
func pipelineToProto(p sqlc.Pipeline) *mgmtv1.Pipeline {
	var matchers []string
	if err := json.Unmarshal(p.Matchers, &matchers); err != nil {
		matchers = []string{}
	}
	if matchers == nil {
		matchers = []string{}
	}
	pb := &mgmtv1.Pipeline{
		Id:        p.ID.String(),
		OrgId:     p.OrgID.String(),
		Name:      p.Name,
		Contents:  p.Contents,
		Matchers:  matchers,
		Enabled:   p.Enabled,
		Source:    p.Source,
		CreatedBy: p.CreatedBy,
		UpdatedBy: p.UpdatedBy,
		CreatedAt: protoTimestamp(p.CreatedAt),
		UpdatedAt: protoTimestamp(p.UpdatedAt),
	}
	// B3(a): GetPipeline previously had no way to return the stored graph, so
	// the visual builder could never load wizard_state (D3) — it always fell
	// back to the lossy text re-parse. Best-effort: a malformed stored blob
	// (should not happen — it's only ever written by this same server) is
	// dropped rather than failing the whole response.
	if len(p.WizardState) > 0 {
		ws := &structpb.Struct{}
		if err := protojson.Unmarshal(p.WizardState, ws); err == nil {
			pb.WizardState = ws
		}
	}
	if p.OwnerTeamID.Valid {
		pb.OwnerTeamId = p.OwnerTeamID.String()
	}
	return pb
}

func revisionToProto(rv sqlc.PipelineRevision) *mgmtv1.PipelineRevision {
	return &mgmtv1.PipelineRevision{
		Revision:   rv.Revision,
		ChangedBy:  rv.ChangedBy,
		ChangedAt:  protoTimestamp(rv.ChangedAt),
		ChangeNote: rv.ChangeNote,
	}
}

// visualNeedsUpgrade reports whether the wizard_state JSON contains a schema_version
// that differs from currentVersion.
func visualNeedsUpgrade(wizardState []byte, currentVersion string) bool {
	if len(wizardState) == 0 {
		return false
	}
	var doc struct {
		SchemaVersion string `json:"schema_version"`
	}
	if err := json.Unmarshal(wizardState, &doc); err != nil {
		return false
	}
	return doc.SchemaVersion != "" && doc.SchemaVersion != currentVersion
}

// checkVisualRenderMatch re-renders wizard_state and compares it with clientContent.
func (s *PipelineService) checkVisualRenderMatch(_ context.Context, clientContent string, wizardState json.RawMessage) (bool, error) {
	if s.schema == nil {
		return false, nil
	}
	var doc visual.GraphDocument
	if err := json.Unmarshal(wizardState, &doc); err != nil {
		return false, fmt.Errorf("unmarshal wizard_state: %w", err)
	}
	version := doc.SchemaVersion
	if version == "" {
		version = s.schema.CurrentVersion()
	}
	payload, _, err := s.schema.Get(version)
	if err != nil {
		return false, fmt.Errorf("load schema %q: %w", version, err)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return false, fmt.Errorf("marshal schema: %w", err)
	}
	var schemaPayload visual.SchemaPayload
	if err := json.Unmarshal(b, &schemaPayload); err != nil {
		return false, fmt.Errorf("unmarshal schema payload: %w", err)
	}
	result := visual.Render(doc, schemaPayload)
	if len(result.Diagnostics) > 0 {
		return false, nil
	}
	return result.Content != clientContent, nil
}

// loadPipeline fetches a pipeline by id, mapping an unparsable id to
// InvalidArgument (400) and a missing row to NotFound (404) — the same two
// distinct statuses PipelinesHandler.loadPipeline produced.
//
// It also enforces that the pipeline belongs to orgIDStr. The authz interceptor
// only proves the caller may act on the org NAMED IN THE REQUEST; without this
// check an org admin could read or mutate another org's pipeline by pairing their
// own org id with its pipeline id. A mismatch is reported as NotFound rather than
// PermissionDenied so the response does not confirm the pipeline exists.
func (s *PipelineService) loadPipeline(ctx context.Context, orgIDStr, idStr string) (sqlc.Pipeline, error) {
	id, err := scanUUID(idStr)
	if err != nil {
		return sqlc.Pipeline{}, connect.NewError(connect.CodeInvalidArgument, errPipelineIDInvalid)
	}
	orgID, err := scanUUID(orgIDStr)
	if err != nil {
		return sqlc.Pipeline{}, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	p, err := s.store.Queries.GetPipelineByID(ctx, id)
	if err != nil {
		return sqlc.Pipeline{}, connect.NewError(connect.CodeNotFound, errPipelineNotFound)
	}
	if p.OrgID != orgID {
		return sqlc.Pipeline{}, connect.NewError(connect.CodeNotFound, errPipelineNotFound)
	}
	return p, nil
}

// authorizeOwnership enforces G11 (docs/gateway-tier-plan.md): the caller
// must be an org admin, or a member of the team identified by
// ownerTeamID. Called individually inside every PipelineService write
// handler (Create/Update/Delete/Enable/Disable) — per write path, not from
// the interceptor, which only proved "has some access to this org" (see
// procedureRequirements' comment). ownerTeamID is "" for an unowned
// pipeline, which auth.AuthorizeOwnership treats as org-admin-only.
func (s *PipelineService) authorizeOwnership(ctx context.Context, orgIDStr, ownerTeamID string) error {
	sess := auth.SessionFromCtx(ctx)
	if sess == nil {
		// A machine (service-account) caller has no team membership to check:
		// G11's ownership model is keyed on IdP groups carried by a human
		// session, and a service account has none. requireWriteAuthorized has
		// already gated this call on capability AND on the on-behalf-of claim
		// matching the human the credential was issued to, so the write is
		// attributable — but attribution is not authorization.
		//
		// State this plainly because it is the real limit of G11: an
		// apply-capability service account can write ANY pipeline in its org,
		// including one owned by a team its delegating human is not in. Team
		// scoping for machine credentials means resolving that human's group
		// membership without a session, which is new scope (an allow-list on
		// the service account, or an IdP lookup), not a missing check here.
		// Until then, an apply credential is an org-level power and should be
		// issued as one.
		if _, ok := serviceAccountFromCtx(ctx); ok {
			return nil
		}
		return connect.NewError(connect.CodeUnauthenticated, auth.ErrUnauthenticated)
	}
	switch err := auth.AuthorizeOwnership(ctx, s.store, sess, orgIDStr, ownerTeamID); {
	case err == nil:
		return nil
	case errors.Is(err, auth.ErrUnauthenticated):
		return connect.NewError(connect.CodeUnauthenticated, err)
	case errors.Is(err, auth.ErrOrgNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, auth.ErrInvalidOrgID):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodePermissionDenied, err)
	}
}

// looseUUID parses s into a pgtype.UUID, leaving it as the invalid zero
// value on parse failure instead of returning an error — mirrors
// orgIDFromParam's "id.Valid = false on error" behavior for the endpoints
// (Update/Delete/Enable/Disable) that never validated the org id and simply
// passed the zero-value UUID through to cache-dirtying/audit calls.
func looseUUID(s string) pgtype.UUID {
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		id.Valid = false
	}
	return id
}

// ListPipelines lists pipelines in an org.
func (s *PipelineService) ListPipelines(ctx context.Context, req *connect.Request[mgmtv1.ListPipelinesRequest]) (*connect.Response[mgmtv1.ListPipelinesResponse], error) {
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errOrgIDInvalid)
	}
	pipelines, err := s.store.Queries.ListPipelinesByOrg(ctx, orgID)
	if err != nil {
		s.logger.Error("list pipelines", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errListPipelinesFailed)
	}

	// ?needs_upgrade=true — filter to visual pipelines whose schema_version < current.
	if req.Msg.GetNeedsUpgrade() && s.schema != nil {
		current := s.schema.CurrentVersion()
		filtered := pipelines[:0]
		for i := range pipelines {
			if pipelines[i].Source == "visual" && visualNeedsUpgrade(pipelines[i].WizardState, current) {
				filtered = append(filtered, pipelines[i])
			}
		}
		pipelines = filtered
	}

	items := make([]*mgmtv1.Pipeline, len(pipelines))
	for i := range pipelines {
		items[i] = pipelineToProto(pipelines[i])
	}
	return connect.NewResponse(&mgmtv1.ListPipelinesResponse{
		Items: items,
		Total: int32(len(items)), //nolint:gosec // len(items) bounded by DB rows for one org
	}), nil
}

// GetPipeline returns one pipeline, including its revision history.
func (s *PipelineService) GetPipeline(ctx context.Context, req *connect.Request[mgmtv1.GetPipelineRequest]) (*connect.Response[mgmtv1.Pipeline], error) {
	p, err := s.loadPipeline(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	pp := pipelineToProto(p)

	revs, revErr := s.store.Queries.ListPipelineRevisions(ctx, p.ID)
	if revErr != nil {
		s.logger.Warn("failed to load pipeline revisions", "err", revErr)
	}
	for i := range revs {
		pp.Revisions = append(pp.Revisions, revisionToProto(revs[i]))
	}
	return connect.NewResponse(pp), nil
}

// pipelineSaveInput holds the fields shared by CreatePipeline and
// UpdatePipeline's validation/persistence pipeline.
type pipelineSaveInput struct {
	Name        string
	Contents    string
	Matchers    []string
	Source      string
	WizardState []byte
}

// validateSaveInput runs the shared create/update validation sequence: name
// required, visual render-match check, matchers marshal, then the Stage1+2
// validation gate. It returns the marshaled matchers JSON on success.
func (s *PipelineService) validateSaveInput(ctx context.Context, in pipelineSaveInput) (json.RawMessage, error) {
	if in.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errPipelineNameRequired)
	}
	if in.Source == "visual" && len(in.WizardState) > 0 {
		mismatch, checkErr := s.checkVisualRenderMatch(ctx, in.Contents, in.WizardState)
		if checkErr != nil {
			s.logger.Warn("visual render-equality check failed", "err", checkErr)
		} else if mismatch {
			return nil, connect.NewError(connect.CodeAlreadyExists, errRenderMismatch)
		}
	}

	matchersJSON, err := json.Marshal(in.Matchers)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errMatchersInvalid)
	}

	wrapped := validate.WrapForValidation(in.Name, in.Contents)
	if result := s.validator.Stages12(ctx, wrapped); !result.Valid {
		return nil, connect.NewError(connect.CodeFailedPrecondition, &pipelineValidationError{Diagnostics: result.Diagnostics})
	}
	return matchersJSON, nil
}

// CreatePipeline creates a pipeline.
func (s *PipelineService) CreatePipeline(ctx context.Context, req *connect.Request[mgmtv1.CreatePipelineRequest]) (*connect.Response[mgmtv1.Pipeline], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	msg := req.Msg
	orgID, err := scanUUID(msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errOrgIDInvalid)
	}
	// G11: creating a pipeline owned by a team requires membership of that
	// team (or org-admin); creating an unowned pipeline requires org-admin.
	if err := s.authorizeOwnership(ctx, msg.GetOrgId(), msg.GetOwnerTeamId()); err != nil {
		return nil, err
	}

	var wsJSON []byte
	if ws := msg.GetWizardState(); ws != nil {
		if wsJSON, err = protojson.Marshal(ws); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errWizardStateInvalid)
		}
	}

	matchersJSON, err := s.validateSaveInput(ctx, pipelineSaveInput{
		Name: msg.GetName(), Contents: msg.GetContents(), Matchers: msg.GetMatchers(),
		Source: msg.GetSource(), WizardState: wsJSON,
	})
	if err != nil {
		return nil, err
	}

	actor := actorFromCtx(ctx)
	source := msg.GetSource()
	if source == "" {
		source = "ui"
	}
	p, err := s.store.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
		OrgID:       orgID,
		Name:        msg.GetName(),
		Contents:    msg.GetContents(),
		Matchers:    matchersJSON,
		Enabled:     false,
		Source:      source,
		WizardState: wsJSON,
		CreatedBy:   actor,
		UpdatedBy:   actor,
		OwnerTeamID: looseUUID(msg.GetOwnerTeamId()),
	})
	if err != nil {
		if isUniqueViolation(err) {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && strings.Contains(pgErr.ConstraintName, "sanitized_name") {
				return nil, connect.NewError(connect.CodeFailedPrecondition, errNameCollision)
			}
			return nil, connect.NewError(connect.CodeAlreadyExists, errNameExists)
		}
		s.logger.Error("create pipeline", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errCreatePipelineFailed)
	}

	if revErr := s.createRevision(ctx, p, "created", actor); revErr != nil {
		s.logger.Error("create pipeline: create revision", "err", revErr, "pipeline_id", p.ID.String())
	}
	auditLog(ctx, s.store, actor, orgID, "pipeline.create", "pipeline", p.ID.String())
	s.logger.Info("pipeline created", "pipeline_id", p.ID.String(), "org_id", orgID.String(), "name", p.Name, "actor", actor)

	return connect.NewResponse(pipelineToProto(p)), nil
}

// UpdatePipeline updates a pipeline.
func (s *PipelineService) UpdatePipeline(ctx context.Context, req *connect.Request[mgmtv1.UpdatePipelineRequest]) (*connect.Response[mgmtv1.Pipeline], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	msg := req.Msg
	p, err := s.loadPipeline(ctx, msg.GetOrgId(), msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.authorizeOwnership(ctx, msg.GetOrgId(), pipelineOwnerTeamID(p)); err != nil {
		return nil, err
	}
	if p.Source == "git" {
		return nil, connect.NewError(connect.CodePermissionDenied, errGitSourceReadOnly)
	}

	var wsJSON []byte
	if ws := msg.GetWizardState(); ws != nil {
		if wsJSON, err = protojson.Marshal(ws); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errWizardStateInvalid)
		}
	}

	matchersJSON, err := s.validateSaveInput(ctx, pipelineSaveInput{
		Name: msg.GetName(), Contents: msg.GetContents(), Matchers: msg.GetMatchers(),
		Source: msg.GetSource(), WizardState: wsJSON,
	})
	if err != nil {
		return nil, err
	}

	actor := actorFromCtx(ctx)
	// WizardState is nil when the request omits the field (proto3 message
	// fields are nullable), which the query's COALESCE treats as "preserve
	// the stored graph" rather than clearing it — see pipelines.sql.
	updated, err := s.store.Queries.UpdatePipeline(ctx, sqlc.UpdatePipelineParams{
		ID:          p.ID,
		Name:        msg.GetName(),
		Contents:    msg.GetContents(),
		Matchers:    matchersJSON,
		WizardState: wsJSON,
		UpdatedBy:   actor,
	})
	if err != nil {
		s.logger.Error("update pipeline", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errUpdatePipelineFailed)
	}

	if revErr := s.createRevision(ctx, updated, "updated", actor); revErr != nil {
		s.logger.Error("update pipeline: create revision", "err", revErr, "pipeline_id", updated.ID.String())
	}

	orgID := looseUUID(msg.GetOrgId())
	// If enabled, dirty the serve cache for affected collectors.
	if updated.Enabled {
		if dirtyErr := s.store.Queries.MarkServeCacheDirtyByOrg(ctx, orgID); dirtyErr != nil {
			s.logger.Error("update pipeline: mark serve cache dirty", "err", dirtyErr, "org_id", orgID.String())
		}
		go s.recomputeOrgCaches(context.Background(), orgID) //nolint:contextcheck,gosec // G118+contextcheck: intentional detached context
	}

	auditLog(ctx, s.store, actor, orgID, "pipeline.update", "pipeline", p.ID.String())
	return connect.NewResponse(pipelineToProto(updated)), nil
}

// DeletePipeline deletes a pipeline.
func (s *PipelineService) DeletePipeline(ctx context.Context, req *connect.Request[mgmtv1.DeletePipelineRequest]) (*connect.Response[mgmtv1.DeletePipelineResponse], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	p, err := s.loadPipeline(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	if err := s.authorizeOwnership(ctx, req.Msg.GetOrgId(), pipelineOwnerTeamID(p)); err != nil {
		return nil, err
	}
	if p.Source == "git" {
		return nil, connect.NewError(connect.CodePermissionDenied, errGitSourceReadOnly)
	}
	actor := actorFromCtx(ctx)
	orgID := looseUUID(req.Msg.GetOrgId())
	if err := s.store.Queries.DeletePipeline(ctx, p.ID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errDeletePipelineFailed)
	}
	if dirtyErr := s.store.Queries.MarkServeCacheDirtyByOrg(ctx, orgID); dirtyErr != nil {
		s.logger.Error("delete pipeline: mark serve cache dirty", "err", dirtyErr, "org_id", orgID.String())
	}
	go s.recomputeOrgCaches(context.Background(), orgID) //nolint:contextcheck,gosec // G118+contextcheck: intentional detached context
	auditLog(ctx, s.store, actor, orgID, "pipeline.delete", "pipeline", p.ID.String())
	s.logger.Info("pipeline deleted", "pipeline_id", p.ID.String(), "org_id", orgID.String(), "actor", actor)
	return connect.NewResponse(&mgmtv1.DeletePipelineResponse{}), nil
}

// setEnabled implements both EnablePipeline and DisablePipeline: it runs a
// Stage3 dry-run merge validation (with or without the candidate pipeline
// included), then persists the new enabled state and dirties/recomputes the
// serve cache for the org.
func (s *PipelineService) setEnabled(ctx context.Context, orgIDStr, idStr string, enable bool) (*connect.Response[mgmtv1.Pipeline], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	p, err := s.loadPipeline(ctx, orgIDStr, idStr)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeOwnership(ctx, orgIDStr, pipelineOwnerTeamID(p)); err != nil {
		return nil, err
	}
	orgID := looseUUID(orgIDStr)
	actor := actorFromCtx(ctx)

	// includeCandidate=true on enable (dry-run merge with the candidate
	// added); false on disable (validates the remaining pipelines, covering
	// the OLD matcher set per spec §P1.2).
	if mergeErr := s.stage3Check(ctx, p, orgID, enable); mergeErr != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, mergeErr)
	}

	updated, err := s.store.Queries.SetPipelineEnabled(ctx, sqlc.SetPipelineEnabledParams{
		ID: p.ID, Enabled: enable, UpdatedBy: actor,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errUpdatePipelineFailed)
	}

	if dirtyErr := s.store.Queries.MarkServeCacheDirtyByOrg(ctx, orgID); dirtyErr != nil {
		s.logger.Error("set pipeline enabled: mark serve cache dirty", "err", dirtyErr, "org_id", orgID.String())
	}
	// Recompute serve-cache eagerly for all collectors in this org so the
	// next GetConfig poll (and the REST served-config endpoint) reflects the
	// updated pipeline state immediately.
	go s.recomputeOrgCaches(context.Background(), orgID) //nolint:contextcheck,gosec // G118+contextcheck: intentional detached context — recompute runs after request completes

	action := "pipeline.disable"
	if enable {
		action = "pipeline.enable"
	}
	auditLog(ctx, s.store, actor, orgID, action, "pipeline", p.ID.String())
	s.logger.Info("pipeline enabled/disabled", "pipeline_id", p.ID.String(), "org_id", orgID.String(), "enabled", enable, "actor", actor)
	return connect.NewResponse(pipelineToProto(updated)), nil
}

// EnablePipeline enables a pipeline.
func (s *PipelineService) EnablePipeline(ctx context.Context, req *connect.Request[mgmtv1.EnablePipelineRequest]) (*connect.Response[mgmtv1.Pipeline], error) {
	return s.setEnabled(ctx, req.Msg.GetOrgId(), req.Msg.GetId(), true)
}

// DisablePipeline disables a pipeline.
func (s *PipelineService) DisablePipeline(ctx context.Context, req *connect.Request[mgmtv1.DisablePipelineRequest]) (*connect.Response[mgmtv1.Pipeline], error) {
	return s.setEnabled(ctx, req.Msg.GetOrgId(), req.Msg.GetId(), false)
}

// ValidatePipeline validates pipeline contents against the schema registry.
// It never returns a connect error for invalid content: valid=false and its
// diagnostics ride on a normal (connect-success) response, matching
// docs/archive/api-contract-design.md's "validate endpoints return 200 + diagnostics
// today — keep that shape". The REST shim (pipelines.go) maps valid=false to
// HTTP 422 to preserve the legacy status code.
func (s *PipelineService) ValidatePipeline(ctx context.Context, req *connect.Request[mgmtv1.ValidatePipelineRequest]) (*connect.Response[mgmtv1.ValidatePipelineResponse], error) {
	name := req.Msg.GetName()
	if name == "" {
		name = "preview"
	}
	wrapped := validate.WrapForValidation(name, req.Msg.GetContents())
	result := s.validator.Stages12(ctx, wrapped)

	resp := &mgmtv1.ValidatePipelineResponse{
		Valid:       result.Valid,
		Diagnostics: diagnosticsToProto(result.Diagnostics),
	}

	// Surface derived signals at authoring time (docs/gateway-tier-plan.md
	// W1 bullet 5): a user should learn "this carries metrics and logs"
	// here, not discover a role mismatch only when merge assembly quietly
	// excludes the pipeline later. This does not block the save — matchers
	// are label-based and which collector(s) this lands on is dynamic; see
	// PreviewMatches for that, and merge.WithRoleEnforcement for the actual
	// enforcement point.
	//
	// Best-effort: contents that fails to parse as Alloy syntax already has
	// that failure reported via valid/diagnostics above (signals.Derive
	// would just fail the same parse), so a Derive error here is not
	// surfaced as a second, redundant error.
	if sig, sigErr := signals.Derive(req.Msg.GetContents(), s.schema); sigErr == nil {
		resp.Signals = signalStrings(sig.Combined)
		resp.SignalsProven = sig.Proven()
		for _, u := range sig.Unknown {
			resp.UnknownComponents = append(resp.UnknownComponents, u.Component)
		}
	}

	// A machine calling this is proposing; a human calling it is typing.
	//
	// R6 asked how an agent's proposal is attributed, and the honest answer
	// was that it was not: internal/mcp's propose_pipeline_revision composes
	// this call and PreviewMatches, both reads, so an agent proposing a
	// change to raw Alloy text left no record that it had. Auditing this
	// endpoint unconditionally would instead log every keystroke-level
	// validation the authoring UI performs, and an audit trail nobody can
	// read is its own failure — so the record is written only when the caller
	// is a machine identity, which is exactly the case R6 cares about.
	//
	// Best-effort by design: a proposal is a read, and failing to record it
	// must not fail it. auditLogDetail already derives actor and the verified
	// on_behalf_of from ctx, so both halves of the delegated action land here
	// the same way they do for a write (G13).
	if sa, ok := serviceAccountFromCtx(ctx); ok {
		orgID, orgErr := scanUUID(req.Msg.GetOrgId())
		if orgErr == nil {
			auditLogDetail(ctx, s.store, actorFromCtx(ctx), "user", orgID,
				"pipeline.propose", "pipeline", "", map[string]any{
					"service_account": sa.Name,
					"pipeline_name":   name,
					"valid":           result.Valid,
					"signals":         resp.Signals,
				})
		}
	}

	return connect.NewResponse(resp), nil
}

// signalStrings renders a signals.Set as strings in signals.All's fixed
// display order, for a proto repeated-string field.
func signalStrings(set signals.Set) []string {
	sorted := set.Sorted()
	out := make([]string, len(sorted))
	for i, sig := range sorted {
		out[i] = string(sig)
	}
	return out
}

// PreviewMatches previews which collectors a pipeline's matchers would select.
func (s *PipelineService) PreviewMatches(ctx context.Context, req *connect.Request[mgmtv1.PreviewMatchesRequest]) (*connect.Response[mgmtv1.PreviewMatchesResponse], error) {
	p, err := s.loadPipeline(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		orgID = pgtype.UUID{}
	}

	var matchers []string
	if err := json.Unmarshal(p.Matchers, &matchers); err != nil {
		s.logger.Debug("unmarshal matchers", "err", err)
	}

	mp := merge.Pipeline{
		ID:       p.ID.String(),
		Name:     p.Name,
		Matchers: matchers,
		Source:   p.Source,
	}

	matched, matchErr := s.previewMatchedCollectors(ctx, mp, orgID)
	if matchErr != nil {
		return nil, connect.NewError(connect.CodeInternal, matchErr)
	}
	items := make([]*mgmtv1.MatchedCollector, len(matched))
	for i, m := range matched {
		items[i] = &mgmtv1.MatchedCollector{Cluster: m["cluster"], Role: m["role"], Id: m["id"]}
	}
	return connect.NewResponse(&mgmtv1.PreviewMatchesResponse{Collectors: items}), nil
}

// pipelineOwnerTeamID returns p's owner_team_id as a string, or "" for an
// unowned pipeline — the shape authorizeOwnership/auth.AuthorizeOwnership
// expect.
func pipelineOwnerTeamID(p sqlc.Pipeline) string {
	if !p.OwnerTeamID.Valid {
		return ""
	}
	return p.OwnerTeamID.String()
}

// SetPipelineOwner reassigns (or, with an empty owner_team_id, clears) a
// pipeline's owning team. Org-admin-only regardless of the pipeline's
// current owner (see procedureRequirements' comment and
// SetPipelineOwnerRequest's proto doc) — granting/revoking ownership is a
// platform decision, not a delegated one, so this deliberately does NOT
// call authorizeOwnership.
func (s *PipelineService) SetPipelineOwner(ctx context.Context, req *connect.Request[mgmtv1.SetPipelineOwnerRequest]) (*connect.Response[mgmtv1.Pipeline], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
	p, err := s.loadPipeline(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	updated, err := s.store.Queries.SetPipelineOwnerTeam(ctx, sqlc.SetPipelineOwnerTeamParams{
		ID: p.ID, OwnerTeamID: looseUUID(req.Msg.GetOwnerTeamId()),
	})
	if err != nil {
		s.logger.Error("set pipeline owner", "err", err)
		return nil, connect.NewError(connect.CodeInternal, errUpdatePipelineFailed)
	}
	auditLog(ctx, s.store, actorFromCtx(ctx), looseUUID(req.Msg.GetOrgId()), "pipeline.set_owner", "pipeline", p.ID.String())
	return connect.NewResponse(pipelineToProto(updated)), nil
}

// ListRevisions lists a pipeline's revision history.
func (s *PipelineService) ListRevisions(ctx context.Context, req *connect.Request[mgmtv1.ListRevisionsRequest]) (*connect.Response[mgmtv1.ListRevisionsResponse], error) {
	p, err := s.loadPipeline(ctx, req.Msg.GetOrgId(), req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	revs, err := s.store.Queries.ListPipelineRevisions(ctx, p.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errListRevisionsFailed)
	}
	items := make([]*mgmtv1.PipelineRevision, len(revs))
	for i := range revs {
		items[i] = revisionToProto(revs[i])
	}
	return connect.NewResponse(&mgmtv1.ListRevisionsResponse{
		Items: items,
		Total: int32(len(items)), //nolint:gosec // len(items) bounded by DB rows for one pipeline
	}), nil
}

// --- helpers moved verbatim from PipelinesHandler (pipelines.go) ---

func (s *PipelineService) createRevision(ctx context.Context, p sqlc.Pipeline, note, actor string) error {
	maxRev, _ := s.store.Queries.GetMaxPipelineRevision(ctx, p.ID) //nolint:errcheck // returns 0 on err, which is the safe default
	_, err := s.store.Queries.CreatePipelineRevision(ctx, sqlc.CreatePipelineRevisionParams{
		PipelineID: p.ID,
		Revision:   maxRev + 1,
		Contents:   p.Contents,
		Matchers:   p.Matchers,
		Enabled:    p.Enabled,
		ChangedBy:  actor,
		ChangeNote: note,
	})
	return err
}

// stage3Check runs Stage 3 validation: assembles merged configs for all affected collectors,
// deduplicates by content hash, then runs Stages 1 and 2 on each unique content
// with concurrency=4 and a configurable timeout (validate.stage3_timeout).
func (s *PipelineService) stage3Check(ctx context.Context, p sqlc.Pipeline, orgID pgtype.UUID, includeCandidate bool) error {
	var matchers []string
	if err := json.Unmarshal(p.Matchers, &matchers); err != nil {
		s.logger.Debug("stage3: unmarshal matchers", "err", err)
	}

	timeout := s.validator.Stage3Timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	enabledPipelines, err := s.store.Queries.ListEnabledPipelinesByOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("loading pipelines: %w", err)
	}

	var mergePipelines []merge.Pipeline
	for i := range enabledPipelines {
		ep := enabledPipelines[i]
		var m []string
		if jsonErr := json.Unmarshal(ep.Matchers, &m); jsonErr != nil {
			continue
		}
		mergePipelines = append(mergePipelines, merge.Pipeline{
			ID:       ep.ID.String(),
			Name:     ep.Name,
			Contents: ep.Contents,
			Matchers: m,
			Source:   ep.Source,
		})
	}
	pID := p.ID.String()
	if includeCandidate {
		// On enable: add/replace candidate in the merge set.
		found := false
		for i, mp := range mergePipelines {
			if mp.ID == pID {
				mergePipelines[i] = merge.Pipeline{ID: pID, Name: p.Name, Contents: p.Contents, Matchers: matchers, Source: p.Source}
				found = true
				break
			}
		}
		if !found {
			mergePipelines = append(mergePipelines, merge.Pipeline{
				ID: pID, Name: p.Name, Contents: p.Contents, Matchers: matchers, Source: p.Source,
			})
		}
	} else {
		// On disable: remove candidate from merge set (validate remaining pipelines).
		filtered := mergePipelines[:0]
		for _, mp := range mergePipelines {
			if mp.ID != pID {
				filtered = append(filtered, mp)
			}
		}
		mergePipelines = filtered
	}

	collectors, err := s.store.Queries.ListCollectorsByOrg(ctx, orgID)
	if err != nil {
		return fmt.Errorf("loading collectors: %w", err)
	}

	// Assemble merged content for every collector; deduplicate by sha256 hash.
	type mergedEntry struct {
		content      string
		collectorKey string // "cluster/role" for error messages
	}
	seen := make(map[string]bool) // hash → already validated
	var toValidate []mergedEntry
	var failures []string

	for i := range collectors {
		c := collectors[i]
		cl := merge.CollectorLabels{
			CollectorID: c.ID.String(),
			Labels:      map[string]string{"role": c.Role},
		}
		cluster, _ := s.store.Queries.GetClusterByID(ctx, c.ClusterID) //nolint:errcheck // empty cluster name is safe in merge
		cl.Labels["cluster"] = cluster.Name
		key := cluster.Name + "/" + c.Role

		result, assembleErr := merge.Assemble(c.ID.String(), key, cl, mergePipelines, "dev", "", merge.WithRoleEnforcement(s.schema))
		if assembleErr != nil {
			failures = append(failures, fmt.Sprintf("%s: merge error: %v", key, assembleErr))
			continue
		}
		// A role/signal-mismatched pipeline is excluded from this
		// collector's dry-run content (fail-safe), not a validation
		// failure — enforcement, unlike a syntax error, does not block
		// enable/disable. It is still worth a debug line since stage3Check
		// is exactly where the org first learns which collector this
		// candidate would land on.
		for _, ex := range result.Exclusions {
			s.logger.Debug("stage3Check: pipeline excluded (role/signal mismatch)",
				"collector_id", c.ID.String(), "role", c.Role, "pipeline", ex.PipelineName, "reason", ex.Reason)
		}
		if !seen[result.Hash] {
			seen[result.Hash] = true
			toValidate = append(toValidate, mergedEntry{content: result.Content, collectorKey: key})
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("merge validation failed on collectors: %s", strings.Join(failures, " | "))
	}

	// Validate unique merged contents concurrently (max 4 workers).
	type result struct {
		key  string
		msgs []string
	}
	sem := make(chan struct{}, 4)
	resultCh := make(chan result, len(toValidate))

	for _, entry := range toValidate {
		sem <- struct{}{}
		go func() {
			defer func() { <-sem }()
			var msgs []string

			r1 := validate.Stage1(entry.content)
			if !r1.Valid {
				for _, d := range r1.Diagnostics {
					msgs = append(msgs, fmt.Sprintf("%d:%d %s", d.Line, d.Col, d.Message))
				}
				resultCh <- result{key: entry.collectorKey, msgs: msgs}
				return
			}

			r2 := s.validator.Stage2(ctx, entry.content)
			if !r2.Valid {
				for _, d := range r2.Diagnostics {
					msgs = append(msgs, fmt.Sprintf("stage2:%d:%d %s", d.Line, d.Col, d.Message))
				}
			}
			resultCh <- result{key: entry.collectorKey, msgs: msgs}
		}()
	}

	// Collect results.
	for range toValidate {
		select {
		case <-ctx.Done():
			return fmt.Errorf("stage-3 validation budget exceeded: %w", ctx.Err())
		case r := <-resultCh:
			if len(r.msgs) > 0 {
				failures = append(failures, fmt.Sprintf("%s: %s", r.key, strings.Join(r.msgs, "; ")))
			}
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("merge validation failed on collectors: %s", strings.Join(failures, " | "))
	}
	return nil
}

func (s *PipelineService) previewMatchedCollectors(ctx context.Context, p merge.Pipeline, orgID pgtype.UUID) ([]map[string]string, error) {
	collectors, err := s.store.Queries.ListCollectorsByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}
	var matched []map[string]string
	for i := range collectors {
		c := collectors[i]
		cl := merge.CollectorLabels{
			CollectorID: c.ID.String(),
			Labels:      map[string]string{"role": c.Role},
		}
		cluster, _ := s.store.Queries.GetClusterByID(ctx, c.ClusterID) //nolint:errcheck // empty cluster name is safe in merge
		cl.Labels["cluster"] = cluster.Name

		ok, matchErr := merge.MatchesPipeline(p, cl)
		if matchErr != nil || !ok {
			continue
		}
		matched = append(matched, map[string]string{"cluster": cluster.Name, "role": c.Role, "id": c.ID.String()})
	}
	return matched, nil
}

// recomputeOrgCaches assembles and writes the serve cache for all collectors in an org.
// Called as a background goroutine after pipeline enable/disable/update/delete.
func (s *PipelineService) recomputeOrgCaches(ctx context.Context, orgID pgtype.UUID) {
	// Optional prewarm: use the same conditional cache path as lazy GetConfig.
	// It is panic-safe and caller-invisible; failures are logged and never affect HTTP responses.
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("recomputeOrgCaches panicked", "org_id", orgID.String(), "panic", r)
		}
	}()
	collectors, err := s.store.Queries.ListCollectorsWithClusterByOrg(ctx, orgID)
	if err != nil {
		s.logger.Warn("recomputeOrgCaches: listing collectors failed", "err", err)
		return
	}
	enabledPipelines, err := s.store.Queries.ListEnabledPipelinesByOrg(ctx, orgID)
	if err != nil {
		s.logger.Warn("recomputeOrgCaches: listing pipelines failed", "err", err)
		return
	}
	var mergePipelines []merge.Pipeline
	for i := range enabledPipelines {
		ep := enabledPipelines[i]
		var m []string
		if jsonErr := json.Unmarshal(ep.Matchers, &m); jsonErr != nil {
			continue
		}
		mergePipelines = append(mergePipelines, merge.Pipeline{
			ID: ep.ID.String(), Name: ep.Name, Contents: ep.Contents,
			Matchers: m, Source: ep.Source,
		})
	}
	for i := range collectors {
		c := collectors[i]
		cl := merge.CollectorLabels{
			CollectorID: c.ID.String(),
			Labels:      map[string]string{"role": c.Role, "cluster": c.ClusterName},
		}
		result, assembleErr := merge.Assemble(c.ID.String(), c.ClusterName+"/"+c.Role, cl, mergePipelines, "prod", "", merge.WithRoleEnforcement(s.schema))
		if assembleErr != nil {
			s.logger.Warn("recomputeOrgCaches: merge failed", "collector_id", c.ID.String(), "err", assembleErr)
			continue
		}
		for _, ex := range result.Exclusions {
			// Visible per docs/gateway-tier-plan.md §8 rule 2: a pipeline
			// excluded for a role/signal mismatch is never silent. The
			// generated header (result.Content) already names it too; this
			// log line is what makes it discoverable without opening the
			// merged config.
			s.logger.Warn("recomputeOrgCaches: pipeline excluded (role/signal mismatch)",
				"collector_id", c.ID.String(), "role", c.Role, "pipeline", ex.PipelineName, "reason", ex.Reason)
		}

		// D6: every collector gets the baseline pipeline here too — see
		// internal/agentapi.WithBeaconRemoteWrite's doc comment and
		// beacon.AppendBaseline for why this must be the SAME call the lazy
		// recompute path makes, not a separate re-implementation.
		content, appendErr := beacon.AppendBaseline(result.Content, s.beaconBaseline)
		if appendErr != nil {
			s.logger.Warn("recomputeOrgCaches: appending beacon baseline pipeline failed; serving without it",
				"collector_id", c.ID.String(), "err", appendErr)
			content = result.Content
		}
		hash := result.Hash
		if content != result.Content {
			hash = merge.HashContent(content)
		}

		r1 := validate.Stage1(content)
		if !r1.Valid {
			s.logger.Warn("recomputeOrgCaches: stage-1 invalid on merged output", "collector_id", c.ID.String())
			continue
		}
		if _, upsertErr := s.store.Queries.UpsertServeCacheConditional(ctx, sqlc.UpsertServeCacheConditionalParams{
			CollectorID: c.ID,
			Content:     content,
			Hash:        hash,
		}); upsertErr != nil {
			s.logger.Warn("recomputeOrgCaches: upsert failed", "collector_id", c.ID.String(), "err", upsertErr)
		}
	}
}
