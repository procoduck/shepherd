// Package merge implements the pipeline matching and declare-wrap merge engine.
//
// Each enabled pipeline's contents are wrapped in a declare block and instantiated,
// namespacing its components to prevent collisions across pipelines.
package merge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/prometheus/alertmanager/pkg/labels"
)

// Pipeline is the minimal representation of a pipeline needed by the merge engine.
type Pipeline struct {
	// ID is the pipeline UUID string.
	ID string
	// Name is the human-readable name used for block naming.
	Name string
	// Contents is the raw Alloy syntax content.
	Contents string
	// Matchers is the list of Alertmanager matcher strings (all must match — AND).
	// Empty means match nothing (safety default).
	Matchers []string
	// Source is "ui", "wizard", or "git".
	Source string
	// Revision is the current revision number.
	Revision int
	// RepoLinkCollectorID is set (non-empty) when Source == "git".
	// Git pipelines match only their target collector, not via matchers.
	RepoLinkCollectorID string
}

// CollectorLabels represents the label set used to match pipelines to a collector.
// It includes built-in labels (cluster, role) plus all key/value pairs from the
// union of live instance local_attributes (last-seen instance wins per key).
type CollectorLabels struct {
	CollectorID string
	Labels      map[string]string
}

// sanitizeRe matches characters outside [a-z0-9_].
var sanitizeRe = regexp.MustCompile(`[^a-z0-9_]`)

// SanitizeName converts a pipeline name to a valid Alloy identifier.
// Result is lowercase, replaces non-[a-z0-9_] with underscores, and
// prepends "p" if the first character would be a digit.
func SanitizeName(name string) string {
	r := sanitizeRe.ReplaceAllString(strings.ToLower(name), "_")
	if len(r) == 0 || (r[0] >= '0' && r[0] <= '9') {
		r = "p" + r
	}
	return r
}

// MatchesPipeline reports whether the pipeline matches the collector label set.
// Git pipelines are matched by collector ID, not by label matchers.
// UI/wizard pipelines with zero matchers match nothing.
func MatchesPipeline(p Pipeline, cl CollectorLabels) (bool, error) {
	if p.Source == "git" {
		return p.RepoLinkCollectorID == cl.CollectorID, nil
	}
	if len(p.Matchers) == 0 {
		return false, nil
	}
	for _, ms := range p.Matchers {
		m, err := labels.ParseMatcher(ms)
		if err != nil {
			return false, fmt.Errorf("parsing matcher %q: %w", ms, err)
		}
		v := cl.Labels[m.Name]
		if !m.Matches(v) {
			return false, nil
		}
	}
	return true, nil
}

// AssembleResult is the output of the merge engine for a single collector.
type AssembleResult struct {
	Content string
	Hash    string
	// Exclusions lists every pipeline that matched cl's labels but was left
	// out of Content because of role enforcement (see WithRoleEnforcement).
	// Empty when enforcement was not requested or excluded nothing.
	Exclusions []Exclusion
}

// Assemble builds the merged Alloy configuration for a collector.
//
// It selects matching pipelines (git first, then ui/wizard sorted by name),
// optionally drops any whose signals the collector's role does not allow
// (WithRoleEnforcement — gate G6), wraps each survivor in a declare block,
// and prepends a header comment naming both what was included and what was
// excluded and why. Returns (content, hash, exclusions) ready to be stored
// in serve_cache.
func Assemble(collectorID, collectorDisplayName string, cl CollectorLabels, pipelines []Pipeline, version, generatedAt string, opts ...AssembleOption) (AssembleResult, error) {
	var cfg assembleConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	// Select pipelines.
	var selected []Pipeline
	// Pipelines whose matchers could not be parsed. Reported like role
	// exclusions rather than aborting the assembly -- see below.
	var unmatchable []Exclusion
	for _, p := range pipelines {
		if p.Source == "git" {
			if p.RepoLinkCollectorID == cl.CollectorID {
				selected = append(selected, p)
			}
			continue
		}
		matched, err := MatchesPipeline(p, cl)
		if err != nil {
			// One unparsable matcher used to abort the whole assembly, which
			// meant a single bad pipeline froze config serving for every
			// collector in the org -- and, for a collector with no cache row
			// yet, produced an EMPTY served config that wiped what it was
			// already running. Exclude just the offender and say so in the
			// header, the same way role enforcement reports its exclusions.
			// Matchers are also parsed at save now, so reaching this means the
			// row predates that check or was written outside the API.
			unmatchable = append(unmatchable, Exclusion{
				PipelineName: p.Name,
				Reason:       fmt.Sprintf("unparsable matcher, excluded: %v", err),
			})
			continue
		}
		if matched {
			selected = append(selected, p)
		}
	}

	// Stable sort: git pipelines first (already filtered above into selected in
	// pipeline order), then ui/wizard pipelines by name.
	// Re-sort all by name for full determinism.
	slices.SortStableFunc(selected, func(a, b Pipeline) int {
		return strings.Compare(a.Name, b.Name)
	})

	if cfg.enforcementRequested && cfg.registry == nil {
		return AssembleResult{}, errors.New(
			"merge: role enforcement was requested but the schema registry is nil — " +
				"refusing to serve unenforced config that would look enforced")
	}
	exclusions := unmatchable
	if cfg.registry != nil {
		selected, exclusions = enforceRoles(selected, cl, cfg.registry)
	}

	if len(selected) == 0 {
		// Empty config (nothing matched, or role enforcement excluded everything that did).
		content := buildHeader(collectorID, collectorDisplayName, version, generatedAt, nil, exclusions)
		return AssembleResult{Content: content, Hash: HashContent(content), Exclusions: exclusions}, nil
	}

	var sb strings.Builder

	// Header comment.
	sb.WriteString(buildHeader(collectorID, collectorDisplayName, version, generatedAt, selected, exclusions))

	// Check for sanitized-name collisions before assembling.
	seen := make(map[string]string, len(selected)) // blockName → pipeline name
	for _, p := range selected {
		blockName := "pipe_" + SanitizeName(p.Name)
		if existing, ok := seen[blockName]; ok {
			return AssembleResult{}, fmt.Errorf(
				"declare-name collision: pipelines %q and %q both sanitize to block name %q",
				existing, p.Name, blockName,
			)
		}
		seen[blockName] = p.Name
	}

	// Declare-wrapped blocks.
	for i, p := range selected {
		if i > 0 {
			sb.WriteString("\n")
		}
		blockName := "pipe_" + SanitizeName(p.Name)
		fmt.Fprintf(&sb, "declare %q {\n", blockName)
		// Indent pipeline contents by one level.
		for _, line := range strings.Split(p.Contents, "\n") {
			if line == "" {
				sb.WriteString("\n")
			} else {
				sb.WriteString("  " + line + "\n")
			}
		}
		sb.WriteString("}\n")
		fmt.Fprintf(&sb, "%s \"default\" { }\n", blockName)
	}

	content := sb.String()
	return AssembleResult{Content: content, Hash: HashContent(content), Exclusions: exclusions}, nil
}

// HashContent computes hex(sha256(content)).
func HashContent(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:])
}

// buildHeader returns the header comment for a merged config. exclusions
// (from role enforcement — see WithRoleEnforcement) are always listed when
// present, even when pipelines is otherwise empty, so an excluded pipeline
// is never invisible just because nothing else matched.
func buildHeader(collectorID, displayName, version, generatedAt string, pipelines []Pipeline, exclusions []Exclusion) string {
	if generatedAt == "" {
		generatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "// Generated by Shepherd %s at %s\n", version, generatedAt)
	fmt.Fprintf(&sb, "// Collector: %s (%s)\n", displayName, collectorID)
	if len(pipelines) == 0 {
		sb.WriteString("// No pipelines matched\n")
	} else {
		fmt.Fprintf(&sb, "// Pipelines (%d):\n", len(pipelines))
		for _, p := range pipelines {
			fmt.Fprintf(&sb, "//   - %s (rev %d)\n", p.Name, p.Revision)
		}
	}
	if len(exclusions) > 0 {
		fmt.Fprintf(&sb, "// Excluded (%d) - signal/role mismatch (docs/gateway-tier-plan.md W1, gate G6):\n", len(exclusions))
		for _, e := range exclusions {
			fmt.Fprintf(&sb, "//   - %s: %s\n", e.PipelineName, e.Reason)
		}
	}
	sb.WriteString("\n")
	return sb.String()
}
