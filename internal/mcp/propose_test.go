package mcp

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/gen/shepherd/mgmt/v1/mgmtv1connect"
)

// fakePipelineClient answers the two calls propose_pipeline_revision makes;
// any other method panics through the nil embedded interface.
type fakePipelineClient struct {
	mgmtv1connect.PipelineServiceClient
	collectors []*mgmtv1.MatchedCollector
}

func (f fakePipelineClient) ValidatePipeline(context.Context, *connect.Request[mgmtv1.ValidatePipelineRequest]) (*connect.Response[mgmtv1.ValidatePipelineResponse], error) {
	return connect.NewResponse(&mgmtv1.ValidatePipelineResponse{Valid: true}), nil
}

func (f fakePipelineClient) PreviewMatches(context.Context, *connect.Request[mgmtv1.PreviewMatchesRequest]) (*connect.Response[mgmtv1.PreviewMatchesResponse], error) {
	return connect.NewResponse(&mgmtv1.PreviewMatchesResponse{Collectors: f.collectors}), nil
}

// PreviewMatches checks role exclusion against the STORED pipeline's
// contents. A proposal carries different contents, so the stored verdict
// would be about the wrong pipeline text: it must not reach the blast radius
// as if it described the proposal, and the note must say why it is absent.
func TestProposePipelineRevision_BlastRadiusDropsStoredExclusions(t *testing.T) {
	b := &Backend{Pipeline: fakePipelineClient{collectors: []*mgmtv1.MatchedCollector{
		{Cluster: "prod", Role: "metrics", Id: "a", ExcludedReason: "its signals (logs) are not allowed on role metrics"},
		{Cluster: "prod", Role: "logs", Id: "b"},
	}}}

	for name, in := range map[string]proposePipelineRevisionIn{
		"contents only":         {OrgID: "org", PipelineID: "pip", Name: "p", Contents: "// new"},
		"contents and matchers": {OrgID: "org", PipelineID: "pip", Name: "p", Contents: "// new", Matchers: []string{`cluster="prod"`}},
	} {
		t.Run(name, func(t *testing.T) {
			_, out, err := b.proposePipelineRevision(context.Background(), nil, in)
			if err != nil {
				t.Fatalf("proposePipelineRevision: %v", err)
			}
			if len(out.BlastRadius) != 2 {
				t.Fatalf("blast radius = %+v, want the 2 matched collectors", out.BlastRadius)
			}
			for _, c := range out.BlastRadius {
				if c.ExcludedReason != "" {
					t.Errorf("collector %s carries the stored pipeline's excluded_reason %q", c.ID, c.ExcludedReason)
				}
			}
			if !strings.Contains(out.BlastRadiusNote, "excluded_reason") {
				t.Errorf("note %q does not explain why excluded_reason is absent", out.BlastRadiusNote)
			}
		})
	}
}
