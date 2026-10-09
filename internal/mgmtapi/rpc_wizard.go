package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/merge"
	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
	"shepherd/internal/wizard"
	// Blank imports are what put a wizard in the registry: each package
	// registers itself in init(), so a wizard the binary does not import
	// cannot exist at runtime no matter how well it is tested. W8's five
	// catalog wizards shipped with passing suites and were absent from the
	// running product for exactly this reason — their tests import their own
	// package, so the packages proved themselves in isolation while nothing
	// reached them from cmd/shepherd. Found by walking the Wizards page in a
	// real deployment and seeing one wizard where there should have been six.
	//
	// TestEveryWizardPackageIsRegistered (wizard_registration_test.go) now
	// fails if a package under internal/wizard/ is missing from this list.
	_ "shepherd/internal/wizard/appobservability" // register wizard
	_ "shepherd/internal/wizard/blackbox"         // register wizard
	_ "shepherd/internal/wizard/clustermetrics"   // register wizard
	_ "shepherd/internal/wizard/database"         // register wizard
	_ "shepherd/internal/wizard/podlogs"          // register wizard
	_ "shepherd/internal/wizard/selfmonitoring"   // register wizard
)

// WizardService implements mgmtv1connect.WizardServiceHandler.
type WizardService struct {
	store     *store.Store
	registry  *wizard.Registry
	validator *validate.Validator
	logger    *slog.Logger
	// schema drives the role-exclusion warning RenderWizard adds for a matched
	// collector whose role refuses the pipeline's signals (M2). Nil means no
	// such warning, never a guessed one.
	schema *schema.Registry
}

// WizardServiceOption configures optional WizardService dependencies.
type WizardServiceOption func(*WizardService)

// WithWizardSchema supplies the schema registry RenderWizard needs to warn
// about matched collectors that role enforcement would exclude the pipeline
// from.
func WithWizardSchema(reg *schema.Registry) WizardServiceOption {
	return func(s *WizardService) { s.schema = reg }
}

// NewWizardService constructs a WizardService.
func NewWizardService(st *store.Store, v *validate.Validator, logger *slog.Logger, opts ...WizardServiceOption) *WizardService {
	s := &WizardService{store: st, registry: wizard.Default(), validator: v, logger: logger}
	for _, opt := range opts {
		opt(s)
	}
	return s
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

// RenderWizard generates pipeline contents + matchers from wizard state,
// exactly as CommitWizard does, then runs the same stage 1/2 validation gate
// and a match preview against the org's collectors — WITHOUT persisting
// anything (spec §12: "input -> rendered configs + diagnostics + match
// preview").
func (s *WizardService) RenderWizard(ctx context.Context, req *connect.Request[mgmtv1.RenderWizardRequest]) (*connect.Response[mgmtv1.RenderWizardResponse], error) {
	wiz, err := s.registry.Get(req.Msg.GetKind())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	state := map[string]any{}
	if req.Msg.GetState() != nil {
		state = req.Msg.GetState().AsMap()
	}

	orgID, err := scanUUID(req.Msg.GetOrgId())
	if err != nil {
		orgID = pgtype.UUID{}
	}
	// The org's destinations are what the wizard's `*_dest_name` fields
	// resolve against — the same load CommitWizard does, so the preview
	// shows exactly the writer, auth block included, a commit would store.
	dests, err := wizardDestinations(ctx, s.store.Queries, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	result, err := wiz.Commit(state, dests)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	name := req.Msg.GetName()
	if name == "" {
		name = "preview"
	}
	valResult := s.validator.ValidatePipeline(ctx, name, result.Contents)

	candidate := merge.Pipeline{
		ID:       name,
		Name:     name,
		Contents: result.Contents,
		Matchers: result.Matchers,
		Source:   "wizard",
	}
	matched, orgCollectors, matchErr := s.previewMatchedCollectors(ctx, candidate, orgID)
	if matchErr != nil {
		s.logger.Debug("wizard render: match preview failed", "err", matchErr)
		matched = nil
	}
	return connect.NewResponse(&mgmtv1.RenderWizardResponse{
		Contents:    result.Contents,
		Matchers:    result.Matchers,
		Valid:       valResult.Valid,
		Diagnostics: diagnosticsToProto(valResult.Diagnostics),
		// excluded_reason per collector comes from the same check as the
		// warnings below (and as the served config) — see roleExclusionChecker.
		MatchedCollectors: matchedCollectorsProto(s.schema, candidate, matched),
		// The wizard's own notes, then any matched collector whose role would
		// exclude this pipeline from its served config (M2) — non-blocking.
		Warnings: append(append(append(result.Warnings, roleExclusionWarnings(s.schema, candidate, matched)...),
			zeroMatchWarnings(candidate.Matchers, matched, orgCollectors, matchErr)...),
			neverConnectedWarnings(matched)...),
	}), nil
}

// matchConnected is the previewMatchedCollectors key recording whether a
// matched collector has ever connected: "true", "false", or absent when that
// could not be read (an unknown is never reported as "never connected").
const matchConnected = "connected"

// neverConnectedWarnings names a preview whose every match is a collector
// that has never connected (B5, 2026-10-09 walkthrough). Such a collector
// still counts as a match — it receives the pipeline the moment it connects,
// so "Matches 1 collector" stays true — but a collector row nothing ever
// connected as (seeded, or created for a cluster/role no Alloy runs yet)
// used to suppress the zero-match warning on its own, so a pipeline that
// serves nothing today read as one that does. One connected match is enough
// to say nothing: the pipeline is served somewhere.
func neverConnectedWarnings(matched []map[string]string) []string {
	if len(matched) == 0 {
		return nil
	}
	names := make([]string, 0, len(matched))
	for _, m := range matched {
		if m[matchConnected] != "false" {
			return nil
		}
		names = append(names, m["cluster"]+" / "+m["role"])
	}
	return []string{fmt.Sprintf("Matches only collectors that have never connected: %s. "+
		"The pipeline can still be saved, but it serves nothing until one of them connects — "+
		"check that an Alloy collector runs with that cluster and role.", strings.Join(names, ", "))}
}

// zeroMatchWarnings names a preview that matches no collector in the org
// (S2). A save is still allowed — the collector may simply not have
// connected yet — but "Matches 0 collectors" alone read as a detail, and a
// pipeline declared for a role the fleet does not run (App Observability's
// singleton default for metrics+logs, in a metrics/logs split fleet) was
// saved and served nowhere. Nothing is said when the preview itself failed:
// an unknown match count is not a zero one. An org with no collectors at all
// gets its own message: there is no pattern to check yet.
func zeroMatchWarnings(matchers []string, matched []map[string]string, orgCollectors int, matchErr error) []string {
	if matchErr != nil || len(matched) > 0 {
		return nil
	}
	if orgCollectors == 0 {
		return []string{"Matches no collector yet: this org has no collectors yet. The pipeline can " +
			"still be saved, and is served once a collector it matches connects."}
	}
	target := "its matchers"
	if len(matchers) > 0 {
		target = strings.Join(matchers, ", ")
	}
	return []string{fmt.Sprintf("Matches no collector yet: no collector in this org matches %s. "+
		"The pipeline can still be saved, but it serves nothing until a collector with those labels "+
		"connects — check the cluster pattern and the collector role.", target)}
}

// previewMatchedCollectors mirrors PipelineService.previewMatchedCollectors
// (rpc_pipeline.go) — kept as its own copy here since WizardService renders
// a candidate pipeline that has no row in the pipelines table to load. It
// also returns how many collectors the org has at all, so a zero match can
// tell "none match" from "there are none" (zeroMatchWarnings), and marks
// each match with whether it has ever connected (matchConnected,
// neverConnectedWarnings).
func (s *WizardService) previewMatchedCollectors(ctx context.Context, p merge.Pipeline, orgID pgtype.UUID) ([]map[string]string, int, error) {
	collectors, err := s.store.Queries.ListCollectorsByOrg(ctx, orgID)
	if err != nil {
		return nil, 0, err
	}
	org, _ := s.store.Queries.GetOrgByID(ctx, orgID) //nolint:errcheck // an org lookup failure degrades to no admin labels below
	localAttrs := localAttrsByOrg(ctx, s.store.Queries, orgID, org.AllowLocalAttributeMatching)
	connectedIDs, connErr := s.store.Queries.ListConnectedCollectorIDsByOrg(ctx, orgID)
	if connErr != nil {
		s.logger.Debug("wizard render: listing connected collectors failed", "err", connErr)
	}
	connected := make(map[pgtype.UUID]bool, len(connectedIDs))
	for _, id := range connectedIDs {
		connected[id] = true
	}
	var matched []map[string]string
	for i := range collectors {
		c := collectors[i]
		cluster, _ := s.store.Queries.GetClusterByID(ctx, c.ClusterID) //nolint:errcheck // empty cluster name is safe in merge
		cl := merge.BuildCollectorLabels(c.ID.String(), cluster.Name, c.Role, adminLabelsIfAllowed(org.AllowLabelMatching, c.Labels), localAttrs[c.ID.String()])

		ok, matchErr := merge.MatchesPipeline(p, cl)
		if matchErr != nil || !ok {
			continue
		}
		m := map[string]string{"cluster": cluster.Name, "role": c.Role, "id": c.ID.String()}
		if connErr == nil {
			m[matchConnected] = strconv.FormatBool(connected[c.ID])
		}
		matched = append(matched, m)
	}
	return matched, len(collectors), nil
}

// CommitWizard generates a pipeline from wizard state and creates it.
func (s *WizardService) CommitWizard(ctx context.Context, req *connect.Request[mgmtv1.CommitWizardRequest]) (*connect.Response[mgmtv1.Pipeline], error) {
	if err := requireWriteAuthorized(ctx); err != nil {
		return nil, err
	}
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

	// The org's destinations are what the wizard's `*_dest_name` fields
	// resolve against — the same load RenderWizard does, so the preview and
	// the stored pipeline render the same writer, auth block included.
	dests, err := wizardDestinations(ctx, s.store.Queries, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	result, err := wiz.Commit(state, dests)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	// Same Stage 1/2 gate PipelineService.CreatePipeline runs
	// (validateSaveInput, rpc_pipeline.go) before it ever writes a row — a
	// wizard's generated contents were previously never checked outside the
	// preview-only RenderWizard call, so an unvalidated pipeline (e.g. a
	// field value that breaks Alloy syntax, like a quote in a scrape URL)
	// could be committed straight through. The wizard stays disabled-on-create
	// (Enabled: false below), so Stage 3 is EnablePipeline's job, same as today.
	if valResult := s.validator.ValidatePipeline(ctx, req.Msg.GetName(), result.Contents); !valResult.Valid {
		return nil, connect.NewError(connect.CodeFailedPrecondition, &pipelineValidationError{Diagnostics: valResult.Diagnostics})
	}

	stateJSON, err := wizard.MarshalState(state)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	matchersJSON, err := json.Marshal(result.Matchers)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("marshal error"))
	}

	actor := actorFromCtx(ctx)
	p, err := s.store.Queries.CreatePipeline(ctx, sqlc.CreatePipelineParams{
		OrgID:       orgID,
		Name:        req.Msg.GetName(),
		Contents:    result.Contents,
		Matchers:    matchersJSON,
		Enabled:     false,
		Source:      "wizard",
		WizardKind:  pgtype.Text{String: req.Msg.GetKind(), Valid: true},
		WizardState: stateJSON,
		// The render fingerprint (0030): what this wizard wrote, so a later
		// destination update can tell its output from a hand edit.
		WizardRenderSha256: renderFingerprint(result.Contents),
		CreatedBy:          actor,
		UpdatedBy:          actor,
	})
	if err != nil {
		if isUniqueViolation(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("pipeline name already exists"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create pipeline"))
	}

	// Same two side effects PipelineService.CreatePipeline performs after
	// its insert (rpc_pipeline.go) — a revision-1 "created" row and a
	// pipeline.create audit row — so a wizard-created pipeline has the same
	// history and audit trail an editor-created one does. Log-only on
	// revision failure, matching CreatePipeline's own handling.
	if revErr := createPipelineRevision(ctx, s.store, p, "created", actor); revErr != nil {
		s.logger.Error("commit wizard: create revision", "err", revErr, "pipeline_id", p.ID.String())
	}
	auditLog(ctx, s.store, actor, orgID, "pipeline.create", "pipeline", p.ID.String())

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
	return &mgmtv1.WizardSchema{Kind: sc.Kind, Title: sc.Title, Description: sc.Description, Steps: steps}
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
