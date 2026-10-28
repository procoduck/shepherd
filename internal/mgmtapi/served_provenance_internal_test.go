package mgmtapi

import (
	"encoding/json"
	"testing"
	"time"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/merge"
	"shepherd/internal/serve"
)

// TestProvenanceToProto pins each exclusion kind's wire name and message, and
// that a record for other content than the row holds is not shown.
func TestProvenanceToProto(t *testing.T) {
	p := serve.Provenance{
		Version: 1, Hash: "h1", ComputedAt: "2026-10-28T02:40:17.5+00:00",
		Contributors: []serve.ContributorRecord{{
			PipelineID: "p1", Name: "alpha", Source: "git", Revision: 2, RepoLink: true,
			Lines: serve.LineSpan{First: 6, Last: 12}, Block: "pipe_alpha",
		}},
		Excluded: []serve.ExclusionRecord{
			{PipelineID: "p2", Name: "logs", Kind: merge.ExclusionRole, Reason: "raw", Role: "metrics", Disallowed: []string{"logs"}},
			{PipelineID: "p3", Name: "strict", Kind: merge.ExclusionTenantCheck, Reason: `2:3 tenant "x" is not one this org owns`},
			{PipelineID: "p4", Name: "bad", Kind: merge.ExclusionUnparsableMatcher, Reason: "unparsable matcher, excluded: oops"},
		},
		Baseline: &serve.LineSpan{First: 14, Last: 40},
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}

	var stale mgmtv1.GetServedConfigResponse
	at := time.Date(2026, 10, 28, 2, 40, 17, 500000000, time.UTC)
	provenanceToProto(&stale, raw, "h2", at)
	if stale.GetProvenanceRecorded() || len(stale.GetContributing()) != 0 {
		t.Fatalf("a record for another hash was shown: %v", &stale)
	}
	var rewritten mgmtv1.GetServedConfigResponse
	provenanceToProto(&rewritten, raw, "h1", at.Add(time.Millisecond))
	if rewritten.GetProvenanceRecorded() {
		t.Fatalf("a record an earlier write stamped was shown: %v", &rewritten)
	}

	var resp mgmtv1.GetServedConfigResponse
	provenanceToProto(&resp, raw, "h1", at)
	if !resp.GetProvenanceRecorded() {
		t.Fatal("provenance not reported as recorded")
	}
	c := resp.GetContributing()[0]
	if c.GetPipelineId() != "p1" || !c.GetRepoLink() || c.GetLines().GetFirst() != 6 || c.GetLines().GetLast() != 12 || c.GetRevision() != 2 {
		t.Fatalf("contributor = %v", c)
	}
	if b := resp.GetBaselineLines(); b.GetFirst() != 14 || b.GetLast() != 40 {
		t.Fatalf("baseline = %v", b)
	}
	want := []struct{ reason, message string }{
		{"role", "its signals (logs) are not allowed on role metrics"},
		{"strict_tenant", `2:3 tenant "x" is not one this org owns`},
		{"unparsable_matcher", "unparsable matcher, excluded: oops"},
	}
	for i, w := range want {
		ex := resp.GetExcluded()[i]
		if ex.GetReason() != w.reason || ex.GetMessage() != w.message {
			t.Fatalf("excluded[%d] = %v, want %s / %s", i, ex, w.reason, w.message)
		}
	}
}
