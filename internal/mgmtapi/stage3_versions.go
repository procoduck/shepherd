package mgmtapi

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/merge"
	"shepherd/internal/serve"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// Stage 3's version check (#375): each affected collector's merged config is
// checked against the schema each Alloy version among its active instances
// resolves to (validate.CheckAlloyVersions). An item missing from one of
// them refuses the save, naming the collector, the schema, the item and the
// instances reporting it; resolution notes and components below general
// availability are warnings. Stage 2's `alloy validate` stays the single
// pinned binary.
//
// A failure the collector is ALREADY served — the same item, in the same
// pipeline at the same line of its text, in the collector's merged config
// as it stands — refuses only when it is inside a pipeline the change
// writes; on a disable or a destination re-render
// (stage3OnlyNewVersionFailures) it never does. Git sync's "only new errors
// refuse" rule: an existing mismatch, or an agent reporting an old
// collector.version, must not freeze every unrelated change.

type stage3Options struct {
	onlyNewVersionFailures bool
}

type stage3Option func(*stage3Options)

// stage3OnlyNewVersionFailures makes the version check refuse only failures
// the collector's current merged config does not already have, even inside
// the changed pipelines: for writes that do not author content (a
// destination re-render regenerates what a wizard wrote).
func stage3OnlyNewVersionFailures(o *stage3Options) { o.onlyNewVersionFailures = true }

// maxVersionsPerCollector caps the distinct versions read per collector
// (in SQL, oldest release versions first): instance ids and versions are
// agent-chosen. A collector past it is also checked against the oldest
// shipped schema, with a warning (validate.WithTruncatedVersions) — never
// against the pin alone.
const maxVersionsPerCollector = validate.MaxReportedVersions

// maxNamedInstances is how many instances a refusal names per version.
const maxNamedInstances = 3

// instanceVersion is one reported version of a collector, the (at most 3)
// instances reporting it most recently and how many report it; or, with
// truncated set, how many versions past the cap were not read.
type instanceVersion struct {
	version   string
	instances []string
	count     int64
	truncated int
}

// activeVersionsByCollector returns, per collector id, the Alloy versions its
// active instances report — instances registered and seen within
// agent.inactive_after (all registered ones when it is unset): the rule the
// Collectors page uses for "inactive". A collector with no active instance
// is absent, and is checked against the pinned schema with a warning.
func (s *PipelineService) activeVersionsByCollector(ctx context.Context, orgID pgtype.UUID) (map[string][]instanceVersion, error) {
	rows, err := s.store.Queries.ListActiveInstanceVersionsByOrg(ctx, sqlc.ListActiveInstanceVersionsByOrgParams{
		OrgID:          orgID,
		InactiveBefore: activeCutoff(s.inactiveAfter, time.Now()),
		PerCollector:   maxVersionsPerCollector,
	})
	if err != nil {
		return nil, fmt.Errorf("loading collector instance versions: %w", err)
	}
	out := make(map[string][]instanceVersion)
	total := map[string]int64{}
	for _, r := range rows {
		id := r.CollectorID.String()
		out[id] = append(out[id], instanceVersion{version: r.AlloyVersion, instances: r.Instances, count: r.InstanceCount})
		total[id] = r.VersionsTotal
	}
	for id, n := range total {
		if dropped := int(n) - len(out[id]); dropped > 0 {
			out[id] = append(out[id], instanceVersion{truncated: dropped})
		}
	}
	return out, nil
}

// versionsOf is the version list validate.CheckAlloyVersions takes, marked
// as truncated when the collector's versions were capped.
func versionsOf(ivs []instanceVersion) []string {
	out := make([]string, 0, len(ivs))
	dropped := 0
	for _, iv := range ivs {
		if iv.truncated > 0 {
			dropped += iv.truncated
			continue
		}
		out = append(out, iv.version)
	}
	return validate.WithTruncatedVersions(out, dropped)
}

// activeCutoff is now minus agent.inactive_after, or NULL (every registered
// instance) when it is unset.
func activeCutoff(inactiveAfter time.Duration, now time.Time) pgtype.Timestamptz {
	if inactiveAfter <= 0 {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: now.Add(-inactiveAfter), Valid: true}
}

// versionGroups groups the collectors sharing one merged content by their
// version set (validate.VersionSetKey): the version check's dedupe key is
// (content hash, version set), so collectors with the same bytes and
// versions are checked once and the same bytes with different versions are
// not.
type versionGroups map[string]*versionGroup

type versionGroup struct {
	versions []string
	members  []versionMember
}

type versionMember struct {
	key      string // "cluster/role"
	coll     serve.Collector
	versions []instanceVersion
}

func (g versionGroups) add(collectorKey string, coll serve.Collector, ivs []instanceVersion) {
	versions := versionsOf(ivs)
	key := validate.VersionSetKey(versions)
	grp, ok := g[key]
	if !ok {
		grp = &versionGroup{versions: versions}
		g[key] = grp
	}
	grp.members = append(grp.members, versionMember{key: collectorKey, coll: coll, versions: ivs})
}

// versionCheckContext is what check needs beyond the content.
type versionCheckContext struct {
	// writes holds the declare labels (merge.DeclareLabel) of the pipelines
	// the change writes (the candidates).
	writes map[string]bool
	// newOnly: refuse only failures not already served
	// (stage3OnlyNewVersionFailures).
	newOnly bool
	// current assembles a collector's merged config as it stands, before
	// the change (normalised as the candidate content is).
	current func(serve.Collector) (string, bool)
}

// versionVerdict is one content's version-check outcome: failures refuse the
// save, warnings do not. Each line names the collectors it is about.
type versionVerdict struct {
	failures []string
	warnings []string
}

// failureKey identifies a version failure across two merged configs: the
// declare block holding it (its label, merge.DeclareLabel — unique within a
// merged config, not the display name the header escapes), its line in that pipeline's own
// text, and the message (which names the item and the schemas). A line
// outside every block (the header, D6's baseline) is keyed by the message.
func failureKey(content string, d validate.Diagnostic) (key, pipeline string) {
	if loc, ok := merge.Locate(content, d.Line); ok {
		return fmt.Sprintf("%s:%d:%d:%s", loc.BlockName, loc.PipelineLine, d.Col, d.Message), loc.BlockName
	}
	return d.Message, ""
}

// check runs the version check once per group, in a stable order.
func (g versionGroups) check(content string, vc versionCheckContext) versionVerdict {
	keys := make([]string, 0, len(g))
	for k := range g {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var v versionVerdict
	for _, k := range keys {
		grp := g[k]
		var errs []validate.Diagnostic
		var warns []string
		for _, d := range validate.CheckAlloyVersions(content, grp.versions) {
			if d.Warning {
				warns = append(warns, d.Message)
				continue
			}
			errs = append(errs, d)
		}
		who := make([]string, 0, len(grp.members))
		for _, m := range grp.members {
			who = append(who, m.key)
		}
		for _, w := range warns {
			v.warnings = append(v.warnings, fmt.Sprintf("%s: %s", strings.Join(who, ", "), w))
		}
		if len(errs) == 0 {
			continue
		}
		// Collectors of the group refusing the same failures share a line,
		// which names the instances (of all of them) behind each failure.
		type line struct {
			diags   []validate.Diagnostic
			members []versionMember
		}
		lines := map[string]*line{}
		var order []string
		for _, m := range grp.members {
			refusing := vc.refusing(content, errs, grp.versions, m)
			if len(refusing) == 0 {
				continue
			}
			keyParts := make([]string, 0, len(refusing))
			for _, d := range refusing {
				keyParts = append(keyParts, fmt.Sprintf("%d:%d:%s", d.Line, d.Col, d.Message))
			}
			lk := strings.Join(keyParts, "\x00")
			if _, ok := lines[lk]; !ok {
				lines[lk] = &line{diags: refusing}
				order = append(order, lk)
			}
			lines[lk].members = append(lines[lk].members, m)
		}
		for _, lk := range order {
			l := lines[lk]
			keys := make([]string, 0, len(l.members))
			var ivs []instanceVersion
			for _, m := range l.members {
				keys = append(keys, m.key)
				ivs = append(ivs, m.versions...)
			}
			msgs := make([]string, 0, len(l.diags))
			for _, d := range l.diags {
				msgs = append(msgs, fmt.Sprintf("version:%d:%d %s%s", d.Line, d.Col, d.Message, reportedBy(ivs, d.AlloySchemas)))
			}
			v.failures = append(v.failures, fmt.Sprintf("%s: %s", strings.Join(keys, ", "), strings.Join(msgs, "; ")))
		}
	}
	return v
}

// refusing returns the failures of errs that refuse for member m: every one
// inside a pipeline the change writes (unless newOnly), and otherwise those
// m's merged config as it stands does not already have. A current config
// that cannot be assembled has nothing already served, so all refuse.
func (vc versionCheckContext) refusing(content string, errs []validate.Diagnostic, versions []string, m versionMember) []validate.Diagnostic {
	var existing map[string]bool
	load := func() {
		if existing != nil {
			return
		}
		existing = map[string]bool{}
		if vc.current == nil {
			return
		}
		cur, ok := vc.current(m.coll)
		if !ok {
			return
		}
		for _, d := range validate.AlloyVersionErrors(cur, versions) {
			k, _ := failureKey(cur, d)
			existing[k] = true
		}
	}
	var out []validate.Diagnostic
	for _, d := range errs {
		k, pipeline := failureKey(content, d)
		if !vc.newOnly && pipeline != "" && vc.writes[pipeline] {
			out = append(out, d)
			continue
		}
		load()
		if !existing[k] {
			out = append(out, d)
		}
	}
	return out
}

// reportedBy names, for a failure, the instances whose reported version
// resolves to one of the schemas lacking the item — so an operator can find
// a stale or misreporting agent. Names are agent-chosen: sanitised and
// capped.
func reportedBy(ivs []instanceVersion, schemas []string) string {
	var parts []string
	var total int64
	for _, iv := range ivs {
		if iv.truncated > 0 || !slices.Contains(schemas, validate.DisplayVersion(validate.ResolveVersion(iv.version).Artifact)) {
			continue
		}
		total += max(iv.count, int64(len(iv.instances)))
		for _, name := range iv.instances {
			if len(parts) < maxNamedInstances {
				parts = append(parts, fmt.Sprintf("%s (%s)", validate.SanitizeName(name), versionLabel(iv.version)))
			}
		}
	}
	if total == 0 {
		return ""
	}
	sort.Strings(parts)
	s := " [reported by " + strings.Join(parts, ", ")
	if n := total - int64(len(parts)); n > 0 {
		s += fmt.Sprintf(" and %d more", n)
	}
	return s + "]"
}

func versionLabel(v string) string {
	if v == "" {
		return "no version"
	}
	return validate.SanitizeVersion(v)
}

// hasStage reports whether diags holds an error from stage.
func hasStage(diags []validate.Diagnostic, stage int) bool {
	return slices.ContainsFunc(diags, func(d validate.Diagnostic) bool { return d.Stage == stage && !d.Warning })
}

// orgVersionWarnings is the version check ValidatePipeline adds for the
// author (#375): contents against every Alloy version any active instance in
// the org reports (validate.MaxReportedVersions of them at most). The
// request carries no matchers, so which collectors the pipeline will reach
// is unknown here, and every finding is a warning — Stage 3 refuses, at
// save, what is missing on a collector it reaches. Nil when the org has no
// active instance.
func (s *PipelineService) orgVersionWarnings(ctx context.Context, orgID pgtype.UUID, contents string) []validate.Diagnostic {
	byCollector, err := s.activeVersionsByCollector(ctx, orgID)
	if err != nil {
		s.logger.Warn("validate: loading collector versions", "org_id", orgID.String(), "err", err)
		return nil
	}
	var versions []string
	for _, ivs := range byCollector {
		versions = append(versions, versionsOf(ivs)...)
	}
	if len(versions) == 0 {
		return nil
	}
	slices.Sort(versions)
	versions = slices.Compact(versions)
	diags := validate.CheckAlloyVersions(contents, versions)
	for i := range diags {
		switch {
		case !diags[i].Warning:
			diags[i].Message = "an active collector in this org runs this version — Stage 3 refuses the save if the pipeline reaches it: " + diags[i].Message
		case strings.HasPrefix(diags[i].Message, "runs "), strings.HasPrefix(diags[i].Message, "reports "), strings.HasPrefix(diags[i].Message, "has "):
			diags[i].Message = "a collector in this org " + diags[i].Message
		}
		diags[i].Warning = true
	}
	return diags
}
