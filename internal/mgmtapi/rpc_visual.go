package mgmtapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgtype"
	"google.golang.org/protobuf/types/known/structpb"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
	"shepherd/internal/schema"
	"shepherd/internal/store"
	"shepherd/internal/validate"
	"shepherd/internal/visual"
)

// VisualService implements mgmtv1connect.VisualServiceHandler — the business
// logic for /api/orgs/{org}/visual/* and the pipeline graph view, moved here
// from VisualHandler (visual.go, now a thin REST shim over this service).
type VisualService struct {
	store     *store.Store
	validator *validate.Validator
	schema    *schema.Registry
	logger    *slog.Logger
}

// NewVisualService constructs a VisualService with the deps VisualHandler uses today.
func NewVisualService(st *store.Store, v *validate.Validator, reg *schema.Registry, logger *slog.Logger) *VisualService {
	return &VisualService{store: st, validator: v, schema: reg, logger: logger}
}

var _ mgmtv1connect.VisualServiceHandler = (*VisualService)(nil)

// errSchemaUnavailable mirrors the legacy schemaError sentinel: the schema
// registry failed to initialize at startup.
var errSchemaUnavailable = &schemaError{}

type schemaError struct{}

func (*schemaError) Error() string { return "schema registry not initialized" }

// loadSchemaPayload loads the schema artifact for `version` and re-marshals
// it into visual.SchemaPayload — the shape both the renderer and the text
// re-parse (B3(b): a schema-aware ParseAlloy call is required for correct D1
// edge orientation) need. Shared so the two call sites (renderGraph, GraphView)
// can't drift on how the payload is decoded.
func (s *VisualService) loadSchemaPayload(version string) (visual.SchemaPayload, error) {
	payload, _, err := s.schema.Get(version)
	if err != nil {
		return visual.SchemaPayload{}, err
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return visual.SchemaPayload{}, err
	}
	var schemaPayload visual.SchemaPayload
	if err := json.Unmarshal(b, &schemaPayload); err != nil {
		return visual.SchemaPayload{}, err
	}
	return schemaPayload, nil
}

// renderGraph mirrors VisualHandler.render: decodes the graph's schema
// version, loads and re-marshals the schema payload into visual.SchemaPayload,
// and renders. Every error here mapped to CodeInvalidArgument by callers
// (Render/Validate never distinguished error causes — see visual.go's
// original render() callers, both of which reduced any error to 400).
func (s *VisualService) renderGraph(g *mgmtv1.GraphDocument) (visual.RenderResult, error) {
	if s.schema == nil {
		return visual.RenderResult{}, errSchemaUnavailable
	}
	doc := graphDocFromProto(g)
	schemaPayload, err := s.loadSchemaPayload(doc.SchemaVersion)
	if err != nil {
		return visual.RenderResult{}, err
	}
	return visual.Render(doc, schemaPayload), nil
}

// Render renders a visual graph document to Alloy config. When the render
// step produces L1 diagnostics (e.g. a label collision), the response still
// carries them in Diagnostics rather than as a connect error — the REST shim
// (visual.go) inspects that field to reproduce the legacy 422 shape; the
// Connect JSON path always answers 200 with the diagnostics embedded, which
// is the "ride in a Diagnostics message" contract from the design doc.
func (s *VisualService) Render(_ context.Context, req *connect.Request[mgmtv1.RenderRequest]) (*connect.Response[mgmtv1.RenderResponse], error) {
	result, err := s.renderGraph(req.Msg.GetGraph())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&mgmtv1.RenderResponse{
		Content:     result.Content,
		NodeMap:     nodeRangesToProto(result.NodeMap),
		Diagnostics: renderDiagnosticsToProto(result.Diagnostics),
	}), nil
}

// Validate validates a visual graph document: first the render step (L1),
// then Stage1+2 Alloy validation on the rendered content (L2). Diagnostics
// from either layer ride in the response rather than as a connect error,
// mirroring VisualHandler.Validate's "always 200 except on an L1 render
// failure" behavior — the REST shim decides the HTTP status from the
// diagnostics' layer tag (see visual.go).
func (s *VisualService) Validate(ctx context.Context, req *connect.Request[mgmtv1.ValidateVisualRequest]) (*connect.Response[mgmtv1.ValidateVisualResponse], error) {
	result, err := s.renderGraph(req.Msg.GetGraph())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if len(result.Diagnostics) > 0 {
		// L1 render diagnostics ride in RenderDiagnostics, not Diagnostics:
		// they are visual.RenderDiagnostic values (layer/code/node_id/
		// node_id2/message), a different shape from the Stage1/2
		// diagnostics Diagnostics carries (layer/node_id/line/col/message —
		// line/col never existed for this kind, and VisualNodeDiagnostic has
		// no slot for code/node_id2). renderDiagnosticsToProto is the same
		// conversion Render uses, so this reproduces the legacy 422 shape
		// exactly via the REST shim's writeValidateL1LegacyShape.
		return connect.NewResponse(&mgmtv1.ValidateVisualResponse{
			RenderDiagnostics: renderDiagnosticsToProto(result.Diagnostics),
		}), nil
	}

	vr := s.validator.Stages12(ctx, result.Content)
	out := make([]*mgmtv1.VisualNodeDiagnostic, 0, len(vr.Diagnostics))
	for _, d := range vr.Diagnostics {
		id := ""
		for node, rg := range result.NodeMap {
			if d.Line >= rg.StartLine && d.Line <= rg.EndLine {
				id = node
				break
			}
		}
		out = append(out, &mgmtv1.VisualNodeDiagnostic{
			Layer: "L2", NodeId: id,
			Line:    int32(d.Line), //nolint:gosec // G115: line/col from a rendered Alloy file, well within int32
			Col:     int32(d.Col),  //nolint:gosec // G115: line/col from a rendered Alloy file, well within int32
			Message: d.Message,
		})
	}
	return connect.NewResponse(&mgmtv1.ValidateVisualResponse{Diagnostics: out}), nil
}

// UpgradeCheck reports whether a visual graph document needs a schema upgrade.
func (s *VisualService) UpgradeCheck(_ context.Context, req *connect.Request[mgmtv1.UpgradeCheckRequest]) (*connect.Response[mgmtv1.UpgradeCheckResponse], error) {
	if s.schema == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errSchemaUnavailable)
	}
	doc := graphDocFromProto(req.Msg.GetGraph())

	oldVersion := doc.SchemaVersion
	if oldVersion == "" {
		oldVersion = s.schema.CurrentVersion()
	}
	newVersion := s.schema.CurrentVersion()

	oldPayload, _, err := s.schema.Get(oldVersion)
	if err != nil {
		if schema.IsNotFound(err) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("schema version not found: %s", oldVersion))
		}
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("schema unavailable"))
	}
	newPayload, _, err := s.schema.Get(newVersion)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("current schema unavailable"))
	}

	// Merge migrations from overlay into the new payload before marshaling.
	if migs := s.schema.Migrations(); len(migs) > 0 {
		if np, ok := newPayload["migrations"]; !ok || np == nil {
			merged := make(map[string]any, len(newPayload)+1)
			for k, v := range newPayload {
				merged[k] = v
			}
			merged["migrations"] = migs
			newPayload = merged
		}
	}

	// Convert map[string]any → UpgradeSchemaPayload via JSON round-trip.
	var oldSchema, newSchema visual.UpgradeSchemaPayload
	if b, err := json.Marshal(oldPayload); err == nil {
		_ = json.Unmarshal(b, &oldSchema) //nolint:errcheck // best-effort round trip, matches legacy
	}
	if b, err := json.Marshal(newPayload); err == nil {
		_ = json.Unmarshal(b, &newSchema) //nolint:errcheck // best-effort round trip, matches legacy
	}

	result := visual.UpgradeCheck(doc, oldSchema, newSchema, oldVersion, newVersion)
	return connect.NewResponse(&mgmtv1.UpgradeCheckResponse{
		OldVersion:   result.OldVersion,
		NewVersion:   result.NewVersion,
		Items:        upgradeItemsToProto(result.Items),
		NeedsUpgrade: result.NeedsUpgrade,
	}), nil
}

// GraphView returns a pipeline's contents as a visual graph document.
// [reader] — accessible to org readers (graph view is read-only); enforced by
// the authz interceptor on the Connect path and by RequireOrgAccess(...,
// "reader") on the REST path (router.go), not by this method.
func (s *VisualService) GraphView(ctx context.Context, req *connect.Request[mgmtv1.GraphViewRequest]) (*connect.Response[mgmtv1.GraphViewResponse], error) {
	var id pgtype.UUID
	if err := id.Scan(req.Msg.GetId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid pipeline id"))
	}
	p, err := s.store.Queries.GetPipelineByID(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("pipeline not found"))
	}
	var orgID pgtype.UUID
	if scanErr := orgID.Scan(req.Msg.GetOrgId()); scanErr != nil {
		orgID.Valid = false
	}
	if !orgID.Valid {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid org id"))
	}
	if p.OrgID != orgID {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("pipeline not found"))
	}

	schemaVersion := "alloy-v" + strings.TrimPrefix(s.schema.CurrentVersion(), "alloy-v")
	if schemaVersion == "alloy-" {
		schemaVersion = "alloy-v1.18.1"
	}

	// B3(b): ParseAlloy needs the schema to know which exports are
	// receiver-kind (D1) — without it, edges fall back to the legacy
	// referenced->referencing orientation, which is backwards for every
	// receiver export (prometheus.remote_write.receiver, loki.write.receiver,
	// otelcol.*.input, pyroscope.*.receiver). Best-effort: if the schema
	// can't be loaded, still parse without it rather than fail GraphView.
	schemaPayload, schemaErr := s.loadSchemaPayload(schemaVersion)
	if schemaErr != nil {
		s.logger.Warn("graph view: schema unavailable for text re-parse, edges may be misoriented", "err", schemaErr, "schema_version", schemaVersion)
	}

	var result visual.ParseResult
	if schemaErr == nil {
		result = visual.ParseAlloy(p.Contents, schemaVersion, schemaPayload)
	} else {
		result = visual.ParseAlloy(p.Contents, schemaVersion)
	}
	return connect.NewResponse(&mgmtv1.GraphViewResponse{
		Graph:   graphDocToProto(result.Doc),
		Opaque:  result.Opaque,
		Warning: result.Warning,
	}), nil
}

// -- proto <-> internal/visual conversions --

func graphDocFromProto(g *mgmtv1.GraphDocument) visual.GraphDocument {
	doc := visual.GraphDocument{}
	if g == nil {
		return doc
	}
	doc.Kind = g.GetKind()
	doc.SchemaVersion = g.GetSchemaVersion()
	if vp := g.GetViewport(); vp != nil {
		doc.Viewport = visual.Viewport{X: vp.GetX(), Y: vp.GetY(), Zoom: vp.GetZoom()}
	}
	if m := g.GetMeta(); m != nil {
		doc.Meta = visual.Meta{CreatedWith: m.GetCreatedWith()}
	}
	for _, n := range g.GetNodes() {
		var props map[string]any
		if n.GetProps() != nil {
			props = n.GetProps().AsMap()
		}
		var pos visual.Position
		if p := n.GetPosition(); p != nil {
			pos = visual.Position{X: p.GetX(), Y: p.GetY()}
		}
		doc.Nodes = append(doc.Nodes, visual.GraphNode{
			ID: n.GetId(), Component: n.GetComponent(), Label: n.GetLabel(),
			Position: pos, Props: props, Disabled: n.GetDisabled(), Notes: n.GetNotes(),
			// Props is a Struct — an unordered map — so dropping this here would
			// re-sequence the author's blocks into schema order on every save.
			BlockOrder: n.GetBlockOrder(),
		})
	}
	for _, e := range g.GetEdges() {
		var order *int
		if e.Order != nil {
			o := int(e.GetOrder())
			order = &o
		}
		var from, to visual.PortRef
		if f := e.GetFrom(); f != nil {
			from = visual.PortRef{Node: f.GetNode(), Port: f.GetPort()}
		}
		if t := e.GetTo(); t != nil {
			to = visual.PortRef{Node: t.GetNode(), Port: t.GetPort()}
		}
		doc.Edges = append(doc.Edges, visual.GraphEdge{ID: e.GetId(), From: from, To: to, Order: order})
	}
	for _, b := range g.GetBindings() {
		var ref visual.BindingRef
		if r := b.GetRef(); r != nil {
			ref = visual.BindingRef{Node: r.GetNode(), Export: r.GetExport(), Expr: r.GetExpr()}
		}
		doc.Bindings = append(doc.Bindings, visual.GraphBinding{Node: b.GetNode(), Prop: b.GetProp(), Ref: ref})
	}
	return doc
}

func graphDocToProto(doc visual.GraphDocument) *mgmtv1.GraphDocument {
	g := &mgmtv1.GraphDocument{
		Kind:          doc.Kind,
		SchemaVersion: doc.SchemaVersion,
		Viewport:      &mgmtv1.Viewport{X: doc.Viewport.X, Y: doc.Viewport.Y, Zoom: doc.Viewport.Zoom},
		Meta:          &mgmtv1.GraphMeta{CreatedWith: doc.Meta.CreatedWith},
	}
	for _, n := range doc.Nodes {
		var props *structpb.Struct
		if n.Props != nil {
			if p, err := structpb.NewStruct(n.Props); err == nil {
				props = p
			}
		}
		g.Nodes = append(g.Nodes, &mgmtv1.GraphNode{
			Id: n.ID, Component: n.Component, Label: n.Label,
			Position: &mgmtv1.Position{X: n.Position.X, Y: n.Position.Y},
			Props:    props, Disabled: n.Disabled, Notes: n.Notes,
			BlockOrder: n.BlockOrder,
		})
	}
	for _, e := range doc.Edges {
		var order *int32
		if e.Order != nil {
			o := int32(*e.Order) //nolint:gosec // G115: edge order is a small UI-assigned sequence index
			order = &o
		}
		g.Edges = append(g.Edges, &mgmtv1.GraphEdge{
			Id:    e.ID,
			From:  &mgmtv1.PortRef{Node: e.From.Node, Port: e.From.Port},
			To:    &mgmtv1.PortRef{Node: e.To.Node, Port: e.To.Port},
			Order: order,
		})
	}
	for _, b := range doc.Bindings {
		g.Bindings = append(g.Bindings, &mgmtv1.GraphBinding{
			Node: b.Node, Prop: b.Prop,
			Ref: &mgmtv1.BindingRef{Node: b.Ref.Node, Export: b.Ref.Export, Expr: b.Ref.Expr},
		})
	}
	return g
}

func nodeRangesToProto(nm map[string]visual.NodeRange) map[string]*mgmtv1.NodeRange {
	out := make(map[string]*mgmtv1.NodeRange, len(nm))
	for k, v := range nm {
		out[k] = &mgmtv1.NodeRange{
			StartLine: int32(v.StartLine), //nolint:gosec // G115: line numbers from a rendered Alloy file, well within int32
			EndLine:   int32(v.EndLine),   //nolint:gosec // G115: line numbers from a rendered Alloy file, well within int32
		}
	}
	return out
}

func renderDiagnosticsToProto(ds []visual.RenderDiagnostic) []*mgmtv1.VisualDiagnostic {
	out := make([]*mgmtv1.VisualDiagnostic, 0, len(ds))
	for _, d := range ds {
		out = append(out, &mgmtv1.VisualDiagnostic{Layer: d.Layer, Code: d.Code, NodeId: d.NodeID, NodeId2: d.NodeID2, Message: d.Message})
	}
	return out
}

func upgradeItemsToProto(items []visual.UpgradeItem) []*mgmtv1.UpgradeItem {
	out := make([]*mgmtv1.UpgradeItem, 0, len(items))
	for _, it := range items {
		out = append(out, &mgmtv1.UpgradeItem{NodeId: it.NodeID, NodeLabel: it.NodeLabel, Component: it.Component, Class: string(it.Class), Detail: it.Detail})
	}
	return out
}
