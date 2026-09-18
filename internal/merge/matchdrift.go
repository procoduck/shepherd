package merge

// DiffEntry describes one pipeline whose match status against a collector
// flipped between an old and a new CollectorLabels snapshot.
type DiffEntry struct {
	PipelineID   string
	PipelineName string
	// Direction is "added" (didn't match before, matches now) or "removed"
	// (matched before, doesn't match now).
	Direction string
}

// DiffMatches compares which of pipelines match a single collector under
// before and after, returning one DiffEntry per pipeline whose match status
// flipped. Pipelines whose match status is unchanged (including one that
// didn't match either time) are omitted.
//
// A matcher-parse error from MatchesPipeline is treated as "did not match"
// on that side, consistent with Assemble excluding an unparsable matcher
// rather than failing the whole computation.
func DiffMatches(pipelines []Pipeline, before, after CollectorLabels) []DiffEntry {
	var out []DiffEntry
	for _, p := range pipelines {
		wasMatched, err := MatchesPipeline(p, before)
		if err != nil {
			wasMatched = false
		}
		isMatched, err := MatchesPipeline(p, after)
		if err != nil {
			isMatched = false
		}
		if wasMatched == isMatched {
			continue
		}
		direction := "added"
		if wasMatched {
			direction = "removed"
		}
		out = append(out, DiffEntry{PipelineID: p.ID, PipelineName: p.Name, Direction: direction})
	}
	return out
}
