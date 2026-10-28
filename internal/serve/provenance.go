package serve

import (
	"encoding/json"
	"strings"
	"time"

	"shepherd/internal/merge"
)

// provenanceVersion is the version of the stored Provenance JSON
// (serve_cache.provenance, migration 0049). DecodeProvenance treats any
// other version as not recorded, so a shape change bumps it and old rows read
// as "recorded on the next recompute" rather than as wrong data.
const provenanceVersion = 1

// Provenance is how one served config was built (F-CONTRIB, #423): the
// pipelines in it with the evidence that selected each and the lines of its
// block, the pipelines that matched but were left out and why, and where the
// D6 baseline sits. ComputeServed produces it in the same assembly that
// writes the bytes, and serve.Store writes it in the same row write as them,
// so the collector page never shows a decision other than the one served.
// Line numbers are 1-based and inclusive, and refer to Content as served —
// the baseline appended.
type Provenance struct {
	Version int `json:"v"`
	// Hash is the hash of the content this provenance describes.
	// DecodeProvenance refuses a record whose Hash is not the row's: a binary
	// predating migration 0049 rewrites content without it.
	Hash string `json:"hash"`
	// ComputedAt is the computed_at of the row write that stored this
	// record, stamped by the database in that same statement
	// (UpsertServeCacheConditional); empty until stored. A pre-0049 binary's
	// upsert moves the row's computed_at and leaves the record alone — the
	// same bytes, so the same hash, but label evidence that may be stale —
	// so DecodeProvenance refuses a record whose stamp is not the row's.
	ComputedAt   string              `json:"computed_at,omitempty"`
	Contributors []ContributorRecord `json:"contributors"`
	Excluded     []ExclusionRecord   `json:"excluded"`
	// Baseline is the D6 baseline pipeline's lines; nil when none is served.
	Baseline *LineSpan `json:"baseline,omitempty"`
}

// LineSpan is a 1-based inclusive line range of the served text.
type LineSpan struct {
	First int `json:"first"`
	Last  int `json:"last"`
}

// ContributorRecord is one pipeline in the served config.
type ContributorRecord struct {
	PipelineID string `json:"pipeline_id"`
	Name       string `json:"name"`
	Source     string `json:"source"`
	Revision   int    `json:"revision"`
	// RepoLink: a git pipeline, matched by its repo link (Matchers empty).
	RepoLink bool            `json:"repo_link,omitempty"`
	Matchers []MatcherRecord `json:"matchers,omitempty"`
	// Lines is the declare block: `declare "<Block>" {` through
	// `<Block> "default" { }`.
	Lines LineSpan `json:"lines"`
	Block string   `json:"block"`
}

// MatcherRecord is merge.MatcherEvidence as stored.
type MatcherRecord struct {
	Matcher string `json:"matcher"`
	Label   string `json:"label"`
	Value   string `json:"value"`
	Present bool   `json:"present"`
}

// ExclusionRecord is merge.Exclusion as stored.
type ExclusionRecord struct {
	PipelineID string              `json:"pipeline_id"`
	Name       string              `json:"name"`
	Kind       merge.ExclusionKind `json:"kind"`
	// Reason is the text the served header lists the exclusion with.
	Reason string `json:"reason"`
	// Role, Disallowed and Unproven are set for a role exclusion
	// (merge.Exclusion's fields of the same names).
	Role       string   `json:"role,omitempty"`
	Disallowed []string `json:"disallowed,omitempty"`
	Unproven   string   `json:"unproven,omitempty"`
}

// newProvenance records an assembly's contributors and exclusions, and the
// baseline's lines when appending it turned merged into content.
func newProvenance(assembled merge.AssembleResult, content, hash string) Provenance {
	p := Provenance{
		Version:      provenanceVersion,
		Hash:         hash,
		Contributors: make([]ContributorRecord, 0, len(assembled.Contributors)),
		Excluded:     make([]ExclusionRecord, 0, len(assembled.Exclusions)),
	}
	for i := range assembled.Contributors {
		c := &assembled.Contributors[i]
		var matchers []MatcherRecord
		for _, m := range c.Matchers {
			matchers = append(matchers, MatcherRecord{Matcher: m.Matcher, Label: m.Label, Value: m.Value, Present: m.Present})
		}
		p.Contributors = append(p.Contributors, ContributorRecord{
			PipelineID: c.PipelineID, Name: c.PipelineName, Source: c.Source, Revision: c.Revision,
			RepoLink: c.RepoLink, Matchers: matchers,
			Lines: LineSpan{First: c.Span.DeclareLine, Last: c.Span.EndLine},
			Block: c.Span.BlockName,
		})
	}
	for i := range assembled.Exclusions {
		e := &assembled.Exclusions[i]
		var disallowed []string
		for _, s := range e.Disallowed.Sorted() {
			disallowed = append(disallowed, string(s))
		}
		p.Excluded = append(p.Excluded, ExclusionRecord{
			PipelineID: e.PipelineID, Name: e.PipelineName, Kind: e.Kind, Reason: e.Reason,
			Role: e.Role, Disallowed: disallowed, Unproven: e.Unproven,
		})
	}
	if content != assembled.Content {
		p.Baseline = appendedSpan(assembled.Content, content)
	}
	return p
}

// appendedSpan is the lines content holds after merged's last non-blank line
// that are not blank at either end: what beacon.AppendBaseline appended. Nil
// when there are none.
func appendedSpan(merged, content string) *LineSpan {
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	first := strings.Count(strings.TrimRight(merged, "\n"), "\n") + 1 // merged's last non-blank line
	for first < len(lines) && lines[first] == "" {
		first++
	}
	if first >= len(lines) {
		return nil
	}
	return &LineSpan{First: first + 1, Last: len(lines)}
}

// DecodeProvenance reads a stored serve_cache.provenance value for the row
// whose hash is hash and computed_at is computedAt. ok is false for a row
// with none (computed before migration 0049, or a failure placeholder), in
// another version, recorded for other content than the row now holds, or
// left behind by a later write that did not record its own (a pre-0049
// replica's, mid-rollout): the caller then shows that provenance is recorded
// on the next recompute.
func DecodeProvenance(raw []byte, hash string, computedAt time.Time) (Provenance, bool) {
	if len(raw) == 0 {
		return Provenance{}, false
	}
	var p Provenance
	if err := json.Unmarshal(raw, &p); err != nil || p.Version != provenanceVersion || p.Hash != hash {
		return Provenance{}, false
	}
	stamped, err := time.Parse(time.RFC3339Nano, p.ComputedAt)
	if err != nil || !stamped.Equal(computedAt) {
		return Provenance{}, false
	}
	return p, true
}
