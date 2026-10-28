package mgmtapi

import (
	"time"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/merge"
	"shepherd/internal/reconcile"
	"shepherd/internal/serve"
	"shepherd/internal/signals"
)

// provenanceToProto fills resp's F-CONTRIB fields (#423) from the provenance
// the recompute stored with the row's content. Nothing is re-evaluated: a row
// with no provenance for its hash (written before migration 0049, or by a
// binary predating it) is reported as not recorded, and the next recompute —
// serve.Format 5 makes the collector's next poll one — records it.
//
// Every name and message here comes from the collector's own org: both serve
// paths load only that org's pipelines (serve.LoadPipelines), and the caller
// has already proved the collector is the org's (loadOwnedCollector).
func provenanceToProto(resp *mgmtv1.GetServedConfigResponse, raw []byte, hash string, computedAt time.Time) {
	p, ok := serve.DecodeProvenance(raw, hash, computedAt)
	if !ok {
		return
	}
	resp.ProvenanceRecorded = true
	for i := range p.Contributors {
		c := &p.Contributors[i]
		pc := &mgmtv1.ContributingPipeline{
			PipelineId: c.PipelineID,
			Name:       c.Name,
			Revision:   int32(c.Revision), //nolint:gosec // a pipeline's revision count never approaches int32 overflow
			Source:     c.Source,
			RepoLink:   c.RepoLink,
			Lines:      lineSpanToProto(c.Lines),
			BlockName:  c.Block,
		}
		for _, m := range c.Matchers {
			pc.Matchers = append(pc.Matchers, &mgmtv1.MatcherEvidence{
				Matcher: m.Matcher, Label: m.Label, Value: m.Value, Present: m.Present,
			})
		}
		resp.Contributing = append(resp.Contributing, pc)
	}
	for i := range p.Excluded {
		resp.Excluded = append(resp.Excluded, exclusionToProto(&p.Excluded[i]))
	}
	if p.Baseline != nil {
		resp.BaselineLines = lineSpanToProto(*p.Baseline)
	}
}

func lineSpanToProto(s serve.LineSpan) *mgmtv1.LineSpan {
	return &mgmtv1.LineSpan{
		First: int32(s.First), //nolint:gosec // a served config's line count never approaches int32 overflow
		Last:  int32(s.Last),  //nolint:gosec // as above
	}
}

// exclusionToProto names an exclusion's kind on the wire and phrases it for
// an operator: a role exclusion as the reconciliation tab and match preview
// phrase it (reconcile.ExcludedPipeline.Why), the others as the served header
// lists them.
func exclusionToProto(e *serve.ExclusionRecord) *mgmtv1.ExcludedPipeline {
	out := &mgmtv1.ExcludedPipeline{PipelineId: e.PipelineID, Name: e.Name, Message: e.Reason}
	switch e.Kind {
	case merge.ExclusionRole:
		out.Reason = "role"
		disallowed := signals.Set{}
		for _, s := range e.Disallowed {
			disallowed = disallowed.Add(signals.Signal(s))
		}
		out.Message = reconcile.ExcludedPipeline{
			Name: e.Name, Reason: e.Reason, Disallowed: disallowed, Unproven: e.Unproven,
		}.Why(e.Role)
		out.DisallowedSignals = e.Disallowed
	case merge.ExclusionTenantCheck:
		out.Reason = "strict_tenant"
	case merge.ExclusionUnparsableMatcher:
		out.Reason = "unparsable_matcher"
	}
	return out
}
