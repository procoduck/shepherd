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
	// Revision is the pipeline's current revision (its newest
	// pipeline_revisions row), printed in the served header as "(rev N)".
	// It is part of the served bytes and so of the hash: every serve path
	// must fill it the same way — internal/serve.PipelinesFromRows does — or
	// the hash moves whenever a poll switches path (#296). A legacy pipeline
	// with no revision row has 0 and shows "(rev 0)".
	Revision int
	// RepoLinkCollectorID is set (non-empty) when Source == "git".
	// Git pipelines match only their target collector, not via matchers.
	RepoLinkCollectorID string
	// TenantProblem is set (non-empty) for a strict org's pipeline that
	// strict tenant mode's check refuses as served (internal/serve sets it
	// when it loads the org's pipelines): Assemble leaves it out of every
	// collector it matches and lists it, with this reason, under "Excluded -
	// strict tenant check" in the header. Empty for every other pipeline,
	// which keeps every other served config byte-identical.
	TenantProblem string
}

// CollectorLabels represents the label set used to match pipelines to a
// collector. It includes the built-in labels (cluster, role) plus whatever
// adminLabels and localAttrs BuildCollectorLabels was given — this type
// itself has no opinion on how a caller computed localAttrs (e.g. whether it
// reflects one instance or several); that's BuildCollectorLabels' caller's
// responsibility, not this merge step's.
type CollectorLabels struct {
	CollectorID string
	Labels      map[string]string
}

// BuildCollectorLabels merges the built-in cluster/role labels with a
// collector's admin-set "Manage labels" (adminLabels) and agent-reported
// local_attributes (localAttrs), for callers gated on the org's
// allow_label_matching / allow_local_attribute_matching flags respectively
// (procoduck/shepherd#139). A caller not opted into one of the two sources
// must pass nil/empty for it — this function applies no gating itself, so it
// always merges whatever it is given.
//
// Precedence, low to high: localAttrs < adminLabels < {cluster, role}.
// localAttrs is agent-reported — i.e. reachable via a compromised agent
// token — and must never be allowed to shadow an admin-set label, mirroring
// why Fleet Management makes remote-reported data lose to admin-set data.
// localAttrs keys are lowercased before merging: admin labels are already
// forced lowercase at write time (SetCollectorLabel), so an unnormalized
// agent-reported "Team" would otherwise silently fail to match a
// `team="..."` matcher written in the admin-label convention.
//
// Any localAttrs or adminLabels key IsReserved rejects is dropped here even
// though the write paths already refuse one (defense in depth against a row
// that predates a key becoming reserved — see docs' "retroactive collision"
// known issue), and cluster/role are set after both filters so they can
// never be shadowed regardless of iteration order.
func BuildCollectorLabels(collectorID, cluster, role string, adminLabels, localAttrs map[string]string) CollectorLabels {
	labels := make(map[string]string, len(adminLabels)+len(localAttrs)+2)
	for k, v := range localAttrs {
		k = strings.ToLower(k)
		if IsReserved(k) {
			continue
		}
		labels[k] = v
	}
	for k, v := range adminLabels {
		if IsReserved(k) {
			continue
		}
		labels[k] = v
	}
	labels["cluster"] = cluster
	labels["role"] = role
	return CollectorLabels{CollectorID: collectorID, Labels: labels}
}

// sanitizeRe matches characters outside [a-z0-9_].
var sanitizeRe = regexp.MustCompile(`[^a-z0-9_]`)

// DeclareLabel is the declare block label Assemble wraps a pipeline in,
// "pipe_" + SanitizeName(name) — what Spans and Locate report as BlockName,
// and unique within one merged config (Assemble refuses a collision).
func DeclareLabel(name string) string { return "pipe_" + SanitizeName(name) }

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
	// Spans is where each included pipeline's declare block sits in Content,
	// in Content's order (#297). Metadata only — it never changes Content or
	// Hash — and Spans(Content) recovers exactly the same map from the bytes.
	Spans []BlockSpan
}

// Assemble builds the merged Alloy configuration for a collector.
//
// It selects matching pipelines (git first, then ui/wizard sorted by name),
// optionally drops any whose signals the collector's role does not allow
// (WithRoleEnforcement — gate G6), wraps each survivor in a declare block,
// and prepends a header comment naming both what was included and what was
// excluded and why. Returns (content, hash, exclusions) ready to be stored
// in serve_cache.
func Assemble(collectorID, collectorDisplayName string, cl CollectorLabels, pipelines []Pipeline, opts ...AssembleOption) (AssembleResult, error) {
	var cfg assembleConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	// Select pipelines.
	var selected []Pipeline
	// Pipelines whose matchers could not be parsed. Reported like role
	// exclusions rather than aborting the assembly -- see below.
	var unmatchable []Exclusion
	for i := range pipelines {
		p := &pipelines[i]
		if p.Source == "git" {
			if p.RepoLinkCollectorID == cl.CollectorID {
				selected = append(selected, *p)
			}
			continue
		}
		matched, err := MatchesPipeline(*p, cl)
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
			selected = append(selected, *p)
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
	// Both exclusion sources reach the header: unparsable matchers were
	// collected above, role mismatches come from enforceRoles. Append, never
	// replace — a broken matcher must stay visible in the served header even
	// when role enforcement is on (which is every production deployment).
	exclusions := unmatchable
	// Strict tenant mode's serve-time check: a matched pipeline it refuses is
	// not served, and says so in the header (before role enforcement, which
	// then has nothing of it to judge).
	kept := selected[:0:0]
	for i := range selected {
		p := &selected[i]
		if p.TenantProblem != "" {
			exclusions = append(exclusions, Exclusion{PipelineName: p.Name, Reason: p.TenantProblem, TenantCheck: true})
			continue
		}
		kept = append(kept, *p)
	}
	selected = kept
	if cfg.registry != nil {
		var roleExclusions []Exclusion
		selected, roleExclusions = enforceRoles(selected, cl, cfg.registry)
		exclusions = append(exclusions, roleExclusions...)
	}

	if len(selected) == 0 {
		// Empty config (nothing matched, or role enforcement excluded everything that did).
		content := buildHeader(collectorID, collectorDisplayName, nil, exclusions)
		return AssembleResult{Content: content, Hash: HashContent(content), Exclusions: exclusions}, nil
	}

	var sb strings.Builder

	// Header comment.
	sb.WriteString(buildHeader(collectorID, collectorDisplayName, selected, exclusions))

	// Check for sanitized-name collisions before assembling.
	seen := make(map[string]string, len(selected)) // blockName → pipeline name
	for i := range selected {
		p := &selected[i]
		blockName := DeclareLabel(p.Name)
		if existing, ok := seen[blockName]; ok {
			return AssembleResult{}, fmt.Errorf(
				"declare-name collision: pipelines %q and %q both sanitize to block name %q",
				existing, p.Name, blockName,
			)
		}
		seen[blockName] = p.Name
	}

	// Declare-wrapped blocks. linesWritten tracks the 1-based line numbers
	// each block lands on, for its BlockSpan (#297) — metadata only, the
	// bytes written are unchanged by it.
	linesWritten := strings.Count(sb.String(), "\n")
	spans := make([]BlockSpan, 0, len(selected))
	for i := range selected {
		p := &selected[i]
		if i > 0 {
			sb.WriteString("\n")
			linesWritten++
		}
		blockName := DeclareLabel(p.Name)
		fmt.Fprintf(&sb, "declare %q {\n", blockName)
		declareLine := linesWritten + 1
		// Indent pipeline contents by one level. The text's own final newline
		// is the line break before the closing brace, not an empty last line:
		// splitting it as one put a blank line before every block's "}"
		// (#308). validate.WrapForValidation lays text out the same way.
		contentLines := strings.Split(strings.TrimSuffix(p.Contents, "\n"), "\n")
		for _, line := range contentLines {
			if line == "" {
				sb.WriteString("\n")
			} else {
				sb.WriteString("  " + line + "\n")
			}
		}
		sb.WriteString("}\n")
		fmt.Fprintf(&sb, "%s \"default\" { }\n", blockName)
		linesWritten = declareLine + len(contentLines) + 2
		spans = append(spans, BlockSpan{
			PipelineName: p.Name, BlockName: blockName,
			DeclareLine: declareLine, EndLine: linesWritten,
		})
	}

	content := sb.String()
	return AssembleResult{Content: content, Hash: HashContent(content), Exclusions: exclusions, Spans: spans}, nil
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
func buildHeader(collectorID, displayName string, pipelines []Pipeline, exclusions []Exclusion) string {
	var sb strings.Builder
	// No generation timestamp: served bytes and their hash must depend only
	// on the merged configuration, or every recompute of an unchanged config
	// moves the hash and the fleet re-fetches and reloads (procoduck/shepherd#213).
	// Nor an environment label (#308): the serve path hard-coded "prod" for
	// every deployment, so it named nothing. Changing this line changes every
	// collector's served bytes — bump serve.Format with it.
	sb.WriteString("// Generated by Shepherd\n")
	// Every interpolated string goes through commentSafe: a line break in a
	// name would end the comment and break the assembled config (see the
	// "header comment hardening" spec).
	fmt.Fprintf(&sb, "// Collector: %s (%s)\n", commentSafe(displayName), commentSafe(collectorID))
	if len(pipelines) == 0 {
		sb.WriteString("// No pipelines matched\n")
	} else {
		fmt.Fprintf(&sb, "// Pipelines (%d):\n", len(pipelines))
		for i := range pipelines {
			p := &pipelines[i]
			fmt.Fprintf(&sb, "//   - %s (rev %d)\n", commentSafe(p.Name), p.Revision)
		}
	}
	// Each section only when it lists something: a config with no strict
	// tenant exclusion is byte-identical to one served before the check.
	var roleExcl, tenantExcl []Exclusion
	for _, e := range exclusions {
		if e.TenantCheck {
			tenantExcl = append(tenantExcl, e)
		} else {
			roleExcl = append(roleExcl, e)
		}
	}
	if len(roleExcl) > 0 {
		fmt.Fprintf(&sb, "// Excluded (%d) - signal/role mismatch (docs/gateway-tier-plan.md W1, gate G6):\n", len(roleExcl))
		for _, e := range roleExcl {
			fmt.Fprintf(&sb, "//   - %s: %s\n", commentSafe(e.PipelineName), commentSafe(e.Reason))
		}
	}
	if len(tenantExcl) > 0 {
		fmt.Fprintf(&sb, "// Excluded (%d) - strict tenant check:\n", len(tenantExcl))
		for _, e := range tenantExcl {
			fmt.Fprintf(&sb, "//   - %s: %s\n", commentSafe(e.PipelineName), commentSafe(e.Reason))
		}
	}
	sb.WriteString("\n")
	return sb.String()
}
