package mcp

import (
	"context"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
)

// propose_pipeline_revision is W11's answer to
// docs/gateway-tier-plan.md §11 open question 10 ("does 'propose' create a
// revision, a git PR, or a distinct proposal object with its own
// lifecycle?"). It deliberately does NOT create anything server-side.
//
// Why not, given G15 asks for a proposal "visible as a revision/PR": every
// existing write-shaped path that could persist a not-yet-applied change
// (PipelineService.CreatePipeline/UpdatePipeline) is, by G12/G14 design,
// unreachable with a propose-scoped credential — that is the control
// working as intended, not an obstacle to route around. Inventing a THIRD
// state ("proposed but not applied") on the pipelines/pipeline_revisions
// tables would mean new schema, a new status column meaningful to every
// existing reader of those tables (merge assembly, the UI, GitOps sync),
// and new authorization logic deciding who may promote a proposal to
// live — exactly the "new safety machinery" the W11 brief says to stop and
// report instead of building. So this tool produces a fully-formed
// proposal PACKAGE (validated content + derived signals + blast radius)
// and hands it back to the caller; turning it into a real pipeline is an
// ORDINARY human write via CreatePipeline/UpdatePipeline, made through the
// human's own session, exactly as docs/gateway-tier-plan.md's G15 section
// describes ("The human applying it is an ordinary human write").
//
// "Visible as a revision" is satisfied at the point the human applies it:
// PipelineService.CreatePipeline/UpdatePipeline already writes a
// pipeline_revisions row for every save (rpc_pipeline.go's createRevision)
// — the existing revision history IS the record, with no new concept
// bolted on. What this tool adds is the PRE-apply half: proof the content
// is valid and where it would land, so the human is applying something
// already vetted rather than raw text.
//
// Evidence of work, and a real limit stated rather than hidden: this
// package's evidence is ValidatePipeline (Stage1/2 syntax+component
// checks, and derived signals) plus PreviewMatches (blast radius, when
// updating an existing pipeline). It does NOT include an S3 sandbox
// simulation (create_sandbox_run) here, because CreateRun's input is an
// alloy-graph/v1 GraphDocument — the visual builder's node graph — and
// there is no function anywhere in this codebase that derives a graph from
// arbitrary Alloy TEXT (the direction only runs the other way: the visual
// builder renders a graph TO text). Fabricating one to force a "simulation
// result" into every proposal would describe a different pipeline than the
// one being proposed, which is worse than omitting it — this repo's own
// standard is "assert the observable consequence", not "assert something".
// An agent proposing a change to an existing SOURCE="VISUAL" pipeline can
// still get real sandbox evidence: read its wizard_state via get_pipeline
// and call create_sandbox_run directly.
//
// SECOND LIMIT, and the one that matters for G15/R6: this tool WRITES NO
// AUDIT ROW. It composes ValidatePipeline and PreviewMatches, both of which
// are reads and neither of which audits, so an agent proposing a change to
// raw Alloy text leaves no record that a proposal was ever made. The
// round-trip G15 proves end to end uses create_sandbox_run, which does
// audit; the text path does not.
//
// Not fixed here on purpose: making these two endpoints audit would also log
// every keystroke-level validation the UI performs, and an audit trail
// nobody can read is its own failure. The right shape is probably a distinct
// "proposal recorded" event rather than auditing the reads it is built from
// — an R6 decision, not a silent one. Stated here because the ledger points
// at this comment for it, and a pointer to a disclosure that is not written
// is exactly the rot this repo keeps finding.
type proposePipelineRevisionIn struct {
	OrgID string `json:"org_id" jsonschema:"the Shepherd org UUID"`
	// PipelineID, when set, proposes an update to an existing pipeline and
	// unlocks blast-radius computation (against that pipeline's CURRENTLY
	// stored matchers — see BlastRadiusNote on the response when this
	// proposal also changes matchers). Empty proposes a brand-new pipeline,
	// for which blast radius cannot be computed pre-creation by any
	// existing propose-safe procedure.
	PipelineID string   `json:"pipeline_id,omitempty" jsonschema:"existing pipeline UUID to propose an update to; omit to propose a new pipeline"`
	Name       string   `json:"name" jsonschema:"the pipeline's display name"`
	Contents   string   `json:"contents" jsonschema:"the candidate Alloy pipeline text"`
	Matchers   []string `json:"matchers,omitempty" jsonschema:"Alertmanager-style matchers this pipeline would use; informational only — see BlastRadiusNote"`
}

type proposePipelineRevisionOut struct {
	Valid             bool                   `json:"valid"`
	Diagnostics       []DiagnosticView       `json:"diagnostics,omitempty"`
	Signals           []string               `json:"signals,omitempty"`
	SignalsProven     bool                   `json:"signals_proven"`
	UnknownComponents []string               `json:"unknown_components,omitempty"`
	BlastRadius       []MatchedCollectorView `json:"blast_radius,omitempty"`
	BlastRadiusNote   string                 `json:"blast_radius_note"`
	Instructions      string                 `json:"instructions"`
}

const applyInstructions = "This is a proposal, not a change: nothing has been written to Shepherd. " +
	"To apply it, a human with Shepherd access must review this package and call " +
	"PipelineService.UpdatePipeline (existing pipeline_id) or CreatePipeline (new pipeline) " +
	"with this exact contents/matchers, through their own authenticated session — the " +
	"credential this MCP server runs as has propose capability only and cannot make that call."

func (b *Backend) proposePipelineRevision(ctx context.Context, _ *mcp.CallToolRequest, in proposePipelineRevisionIn) (*mcp.CallToolResult, proposePipelineRevisionOut, error) {
	vresp, err := b.Pipeline.ValidatePipeline(ctx, connect.NewRequest(&mgmtv1.ValidatePipelineRequest{
		OrgId: in.OrgID, Name: in.Name, Contents: in.Contents,
	}))
	if err != nil {
		return nil, proposePipelineRevisionOut{}, err
	}

	out := proposePipelineRevisionOut{
		Valid:             vresp.Msg.GetValid(),
		Diagnostics:       toDiagnosticViews(vresp.Msg.GetDiagnostics()),
		Signals:           vresp.Msg.GetSignals(),
		SignalsProven:     vresp.Msg.GetSignalsProven(),
		UnknownComponents: vresp.Msg.GetUnknownComponents(),
		Instructions:      applyInstructions,
	}

	switch {
	case in.PipelineID == "":
		out.BlastRadiusNote = "no pipeline_id given (proposing a new pipeline): blast radius cannot be " +
			"computed before the pipeline exists. A human can run preview_matches after applying this proposal."
	case len(in.Matchers) > 0:
		out.BlastRadiusNote = "blast radius below reflects the EXISTING pipeline's currently-stored matchers, " +
			"not the matchers in this proposal — PreviewMatches has no propose-safe way to preview candidate " +
			"matchers that are not yet saved. Re-run preview_matches after a human applies this proposal to " +
			"confirm the new matchers select what you expect."
		if pr, prErr := b.previewMatchesFor(ctx, in.OrgID, in.PipelineID); prErr == nil {
			out.BlastRadius = pr
		}
	default:
		mresp, mErr := b.Pipeline.PreviewMatches(ctx, connect.NewRequest(&mgmtv1.PreviewMatchesRequest{OrgId: in.OrgID, Id: in.PipelineID}))
		if mErr != nil {
			return nil, proposePipelineRevisionOut{}, mErr
		}
		out.BlastRadius = toMatchedCollectorViews(mresp.Msg.GetCollectors())
		out.BlastRadiusNote = "reflects the pipeline's currently-stored matchers (unchanged by this proposal)."
	}

	return nil, out, nil
}

// previewMatchesFor is a best-effort helper for the "matchers also changing"
// branch above: still show today's blast radius even though it is a known
// approximation, rather than showing none. Errors are swallowed on purpose
// (BlastRadiusNote already told the caller this number is a proxy; failing
// the whole proposal over an approximation the response already flags
// would be worse).
func (b *Backend) previewMatchesFor(ctx context.Context, orgID, pipelineID string) ([]MatchedCollectorView, error) {
	resp, err := b.Pipeline.PreviewMatches(ctx, connect.NewRequest(&mgmtv1.PreviewMatchesRequest{OrgId: orgID, Id: pipelineID}))
	if err != nil {
		return nil, err
	}
	return toMatchedCollectorViews(resp.Msg.GetCollectors()), nil
}
