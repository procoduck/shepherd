package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/auth"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/wizard"
	_ "shepherd/internal/wizard/appobservability" // register wizard
)

// WizardService implements mgmtv1connect.WizardServiceHandler — the business
// logic for /api/orgs/{org}/wizards/*, moved here from WizardHandler
// (wizards.go, now a thin REST shim over this service).
type WizardService struct {
	store    *store.Store
	registry *wizard.Registry
	logger   *slog.Logger
}

// NewWizardService constructs a WizardService with the deps WizardHandler uses today.
func NewWizardService(st *store.Store, logger *slog.Logger) *WizardService {
	return &WizardService{store: st, registry: wizard.Default(), logger: logger}
}

var _ mgmtv1connect.WizardServiceHandler = (*WizardService)(nil)

// ListWizards returns all registered wizard kinds.
func (s *WizardService) ListWizards(_ context.Context, _ *connect.Request[mgmtv1.ListWizardsRequest]) (*connect.Response[mgmtv1.ListWizardsResponse], error) {
	kinds := s.registry.ListKinds() // ListKinds never fails
	items := make([]*mgmtv1.WizardSchema, 0, len(kinds))
	for _, k := range kinds {
		wiz, err := s.registry.Get(k)
		if err != nil {
			continue
		}
		items = append(items, wizardSchemaToProto(wiz.Schema()))
	}
	return connect.NewResponse(&mgmtv1.ListWizardsResponse{Items: items, Total: int32(len(items))}), nil //nolint:gosec // G115: registered wizard kinds, a handful at most
}

// GetWizardSchema returns the schema for a specific wizard kind.
func (s *WizardService) GetWizardSchema(_ context.Context, req *connect.Request[mgmtv1.GetWizardSchemaRequest]) (*connect.Response[mgmtv1.WizardSchema], error) {
	wiz, err := s.registry.Get(req.Msg.GetKind())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewResponse(wizardSchemaToProto(wiz.Schema())), nil
}

// CommitWizard generates a pipeline from wizard state and creates it.
func (s *WizardService) CommitWizard(ctx context.Context, req *connect.Request[mgmtv1.CommitWizardRequest]) (*connect.Response[mgmtv1.Pipeline], error) {
	var orgID pgtype.UUID
	if err := orgID.Scan(req.Msg.GetOrgId()); err != nil {
		orgID.Valid = false
	}

	wiz, err := s.registry.Get(req.Msg.GetKind())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	state := map[string]any{}
	if req.Msg.GetState() != nil {
		state = req.Msg.GetState().AsMap()
	}

	result, err := wiz.Commit(state)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	stateJSON, err := wizard.MarshalState(state)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	matchersJSON, err := json.Marshal(result.Matchers)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("marshal error"))
	}

	actor := auth.ActorFromCtx(ctx)
	p, err := s.store.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
		OrgID:       orgID,
		Name:        req.Msg.GetName(),
		Contents:    result.Contents,
		Matchers:    matchersJSON,
		Enabled:     false,
		Source:      "wizard",
		WizardKind:  pgtype.Text{String: req.Msg.GetKind(), Valid: true},
		WizardState: stateJSON,
		CreatedBy:   actor,
		UpdatedBy:   actor,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("pipeline name already exists"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create pipeline"))
	}

	return connect.NewResponse(wizardPipelineToProto(p)), nil
}

// -- proto conversions --

func wizardSchemaToProto(sc wizard.Schema) *mgmtv1.WizardSchema {
	steps := make([]*mgmtv1.Step, 0, len(sc.Steps))
	for _, st := range sc.Steps {
		fields := make([]*mgmtv1.StepField, 0, len(st.Fields))
		for i := range st.Fields {
			f := &st.Fields[i]
			var def *structpb.Value
			if f.Default != nil {
				if v, err := structpb.NewValue(f.Default); err == nil {
					def = v
				}
			}
			fields = append(fields, &mgmtv1.StepField{
				Name: f.Name, Label: f.Label, Type: f.Type, Required: f.Required,
				Options: f.Options, Default: def, Placeholder: f.Placeholder, Description: f.Description,
			})
		}
		steps = append(steps, &mgmtv1.Step{Id: st.ID, Title: st.Title, Fields: fields})
	}
	return &mgmtv1.WizardSchema{Kind: sc.Kind, Title: sc.Title, Steps: steps}
}

// wizardPipelineToProto converts a freshly created pipeline row to the
// mgmt.v1 Pipeline shape, mirroring pipelines.go's pipelineToResponse (which
// PipelineService will also need — see notes for the proto-change /
// dedup request). Timestamps are truncated to whole seconds so protojson's
// RFC3339 rendering matches the legacy "2006-01-02T15:04:05Z" format exactly.
func wizardPipelineToProto(p sqlc.Pipeline) *mgmtv1.Pipeline {
	var matchers []string
	if err := json.Unmarshal(p.Matchers, &matchers); err != nil {
		matchers = []string{}
	}
	if matchers == nil {
		matchers = []string{}
	}
	createdAt := p.CreatedAt.Time.UTC().Truncate(time.Second)
	updatedAt := p.UpdatedAt.Time.UTC().Truncate(time.Second)
	return &mgmtv1.Pipeline{
		Id:        p.ID.String(),
		OrgId:     p.OrgID.String(),
		Name:      p.Name,
		Contents:  p.Contents,
		Matchers:  matchers,
		Enabled:   p.Enabled,
		Source:    p.Source,
		CreatedBy: p.CreatedBy,
		UpdatedBy: p.UpdatedBy,
		CreatedAt: timestamppb.New(createdAt),
		UpdatedAt: timestamppb.New(updatedAt),
	}
}
