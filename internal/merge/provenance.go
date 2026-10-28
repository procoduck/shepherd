package merge

import (
	"fmt"

	"github.com/prometheus/alertmanager/pkg/labels"
)

// Contributor is one pipeline Assemble put into a collector's served config,
// with the evidence that selected it and where its block sits (F-CONTRIB,
// #423). Assemble records it while choosing and writing the pipeline, so a
// reader of it sees the decision the served bytes were built from rather
// than a second evaluation of the pipeline set. Metadata only: it never
// changes Content or Hash.
type Contributor struct {
	PipelineID   string
	PipelineName string
	Source       string
	Revision     int
	// RepoLink is set for a git pipeline, which matches the one collector its
	// repo link names instead of by matchers (Matchers is then empty).
	RepoLink bool
	// Matchers is the evidence for a matcher-selected pipeline: every one of
	// its matchers (all must match) with the collector label it read.
	Matchers []MatcherEvidence
	// Span is where the pipeline's declare block sits in Content.
	Span BlockSpan
}

// MatcherEvidence is one matcher of a selected pipeline and the collector
// label value it was evaluated against.
type MatcherEvidence struct {
	// Matcher is the matcher as the pipeline stores it.
	Matcher string
	// Label is the label name the matcher reads.
	Label string
	// Value is the collector's value for Label; "" when it has none.
	Value string
	// Present is whether the collector has Label at all (a negative matcher
	// such as env!="dev" matches a collector without it).
	Present bool
}

// ExclusionKind is why Assemble left a matched pipeline out of a served
// config. Every Exclusion carries one; switches over it are exhaustive.
type ExclusionKind string

const (
	// ExclusionUnparsableMatcher means one of the pipeline's matchers does not
	// parse, so it cannot be evaluated (Assemble excludes it fail-safe).
	ExclusionUnparsableMatcher ExclusionKind = "unparsable_matcher"
	// ExclusionTenantCheck means strict tenant mode's serve-time check refused it
	// (Pipeline.TenantProblem).
	ExclusionTenantCheck ExclusionKind = "strict_tenant"
	// ExclusionRole means role/signal enforcement (gate G6) refused it — its
	// signals are ones the collector's role does not allow, or could not be
	// derived.
	ExclusionRole ExclusionKind = "role"
)

// matchEvidence evaluates p's matchers against cl — the one evaluation
// MatchesPipeline and Assemble both use — and returns whether all match and,
// when they do, the evidence for each. Git pipelines are matched by collector
// id; a ui/wizard pipeline with no matchers matches nothing.
func matchEvidence(p Pipeline, cl CollectorLabels) (bool, []MatcherEvidence, error) {
	if p.Source == "git" {
		return p.RepoLinkCollectorID == cl.CollectorID, nil, nil
	}
	if len(p.Matchers) == 0 {
		return false, nil, nil
	}
	evidence := make([]MatcherEvidence, 0, len(p.Matchers))
	for _, ms := range p.Matchers {
		m, err := labels.ParseMatcher(ms)
		if err != nil {
			return false, nil, fmt.Errorf("parsing matcher %q: %w", ms, err)
		}
		v, present := cl.Labels[m.Name]
		if !m.Matches(v) {
			return false, nil, nil
		}
		evidence = append(evidence, MatcherEvidence{Matcher: ms, Label: m.Name, Value: v, Present: present})
	}
	return true, evidence, nil
}
