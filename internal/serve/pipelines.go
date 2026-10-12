package serve

import (
	"encoding/json"

	"shepherd/internal/merge"
	"shepherd/internal/store/sqlc"
)

// PipelinesFromRows converts the rows of ListEnabledPipelinesForMerge into
// the merge.Pipeline set ComputeServed takes. Every path that computes served
// bytes (internal/agentapi's lazy recompute, internal/mgmtapi's eager one)
// and every Stage 3 dry run that models them builds its set here, so a field
// that reaches the served bytes cannot be filled on one path and forgotten on
// another — the served header's "(rev N)" was exactly that: Revision was set
// by no caller at all, so every pipeline showed rev 0 (#296). A divergence
// would be worse than a wrong number: the bytes feed the hash, so two paths
// disagreeing would move the hash on every poll that switched paths.
//
// Revision is the row's current_revision: its newest pipeline_revisions row,
// or 0 for a legacy pipeline that has none (the same 0 the editor reports as
// its current revision). A row whose matchers do not decode is skipped — it
// could match nothing, and both serve paths always dropped it. A caller that
// leaves some rows out (a Stage 3 dry run replacing a candidate's stored row)
// filters rows before calling this.
func PipelinesFromRows(rows []sqlc.ListEnabledPipelinesForMergeRow) []merge.Pipeline {
	out := make([]merge.Pipeline, 0, len(rows))
	for i := range rows {
		r := &rows[i]
		var matchers []string
		if err := json.Unmarshal(r.Matchers, &matchers); err != nil {
			continue
		}
		repoLinkCollectorID := ""
		if r.RepoLinkCollectorID.Valid {
			repoLinkCollectorID = r.RepoLinkCollectorID.String()
		}
		out = append(out, merge.Pipeline{
			ID:                  r.ID.String(),
			Name:                r.Name,
			Contents:            r.Contents,
			Matchers:            matchers,
			Source:              r.Source,
			Revision:            int(r.CurrentRevision),
			RepoLinkCollectorID: repoLinkCollectorID,
		})
	}
	return out
}
