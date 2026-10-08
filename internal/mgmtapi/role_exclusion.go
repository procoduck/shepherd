package mgmtapi

import (
	"fmt"
	"sort"
	"strings"

	mgmtv1 "shepherd/gen/shepherd/mgmt/v1"
	"shepherd/internal/merge"
	"shepherd/internal/reconcile"
	"shepherd/internal/schema"
)

// roleExclusionChecker answers, once per role, whether role enforcement
// (gate G6) leaves p out of a collector of that role's served config, and
// why. Every surface that tells an operator about an exclusion — the
// matched-collector preview (PreviewMatches, RenderWizard's
// matched_collectors) and RenderWizard's warnings — goes through it, and it
// asks merge.RoleExclusion, the per-pipeline check merge.Assemble runs, so the
// served config, Reconciliation's role_signal_excluded finding and the
// preview cannot disagree (walkthrough finding M2).
//
// A nil reg (a service wired without the schema) reports nothing rather than
// guessing.
type roleExclusionChecker struct {
	reg     *schema.Registry
	p       merge.Pipeline
	reasons map[string]string
}

func newRoleExclusionChecker(reg *schema.Registry, p merge.Pipeline) *roleExclusionChecker {
	return &roleExclusionChecker{reg: reg, p: p, reasons: map[string]string{}}
}

// reason returns why p is excluded from a collector of role — prose like
// "its signals (logs) are not allowed on role metrics" — or "" when it is
// served there.
func (c *roleExclusionChecker) reason(role string) string {
	if c.reg == nil {
		return ""
	}
	if r, ok := c.reasons[role]; ok {
		return r
	}
	var r string
	if ex, excluded := merge.RoleExclusion(c.p, role, c.reg); excluded {
		// Phrased exactly as Reconciliation's role_signal_excluded finding,
		// unproven-signal-set note included.
		r = reconcile.ExcludedPipeline{
			Name: ex.PipelineName, Reason: ex.Reason, Disallowed: ex.Disallowed, Unproven: ex.Unproven,
		}.Why(role)
	}
	c.reasons[role] = r
	return r
}

// matchedCollectorsProto converts previewMatchedCollectors' output
// (cluster/role/id per collector) to the wire shape, filling excluded_reason
// for every collector whose role refuses p's signals.
func matchedCollectorsProto(reg *schema.Registry, p merge.Pipeline, matched []map[string]string) []*mgmtv1.MatchedCollector {
	check := newRoleExclusionChecker(reg, p)
	items := make([]*mgmtv1.MatchedCollector, len(matched))
	for i, m := range matched {
		items[i] = &mgmtv1.MatchedCollector{
			Cluster:        m["cluster"],
			Role:           m["role"],
			Id:             m["id"],
			ExcludedReason: check.reason(m["role"]),
		}
	}
	return items
}

// roleExclusionWarnings reports, as human-readable warnings, which of the
// collectors p's matchers currently select would leave p out of their served
// config because role enforcement (gate G6) refuses its signals — finding M2
// of the 2026-10-08 walkthrough: such a pipeline used to save and enable
// silently and then appear only as a comment in each collector's served
// config.
//
// Non-blocking by design: matchers are label-based and collectors come and
// go, so refusing the save would be wrong. matched is previewMatchedCollectors'
// output (cluster/role/id per collector). Each role is checked once, through
// roleExclusionChecker, so the warning, the preview's excluded_reason and the
// served config can never disagree. One warning per excluding role, in role
// order, with collectors that share a cluster/role counted rather than
// repeated:
//
//	Excluded from 4 collector(s): prod/metrics ×3, dev/metrics — its signals
//	(logs) are not allowed on role metrics.
//
// A nil reg (a service wired without the schema) reports nothing rather than
// guessing.
func roleExclusionWarnings(reg *schema.Registry, p merge.Pipeline, matched []map[string]string) []string {
	if reg == nil || len(matched) == 0 {
		return nil
	}
	check := newRoleExclusionChecker(reg, p)
	// Per excluding role: each cluster/role name in first-seen order, and how
	// many matched collectors share it.
	type roleGroup struct {
		total  int
		names  []string
		counts map[string]int
	}
	byRole := map[string]*roleGroup{}
	for _, m := range matched {
		role := m["role"]
		if check.reason(role) == "" {
			continue
		}
		g, ok := byRole[role]
		if !ok {
			g = &roleGroup{counts: map[string]int{}}
			byRole[role] = g
		}
		name := m["cluster"] + "/" + role
		if g.counts[name] == 0 {
			g.names = append(g.names, name)
		}
		g.counts[name]++
		g.total++
	}
	roles := make([]string, 0, len(byRole))
	for r := range byRole {
		roles = append(roles, r)
	}
	sort.Strings(roles)

	warnings := make([]string, 0, len(roles))
	for _, r := range roles {
		g := byRole[r]
		labels := make([]string, len(g.names))
		for i, n := range g.names {
			labels[i] = n
			if c := g.counts[n]; c > 1 {
				labels[i] = fmt.Sprintf("%s ×%d", n, c)
			}
		}
		warnings = append(warnings, fmt.Sprintf("Excluded from %d collector(s): %s — %s.",
			g.total, strings.Join(labels, ", "), check.reason(r)))
	}
	return warnings
}
