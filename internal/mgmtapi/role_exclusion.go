package mgmtapi

import (
	"fmt"
	"sort"
	"strings"

	"shepherd/internal/merge"
	"shepherd/internal/schema"
	"shepherd/internal/signals"
)

// roleExclusionWarnings reports, as human-readable warnings, which of the
// collectors p's matchers currently select would leave p out of their served
// config because role enforcement (gate G6) refuses its signals — finding M2
// of the 2026-10-08 walkthrough: such a pipeline used to save and enable
// silently and then appear only as a comment in each collector's served
// config.
//
// Non-blocking by design: matchers are label-based and collectors come and
// go, so refusing the save would be wrong. matched is previewMatchedCollectors'
// output (cluster/role/id per collector). Each role is checked once, with
// merge.RoleExclusion — the per-pipeline check merge.Assemble runs — so the
// warning and the served config can never disagree. One warning per excluding
// role, in role order:
//
//	Excluded from 2 collector(s): prod/metrics, dev/metrics — its signals
//	(logs) are not allowed on role metrics.
//
// A nil reg (a service wired without the schema) reports nothing rather than
// guessing.
func roleExclusionWarnings(reg *schema.Registry, p merge.Pipeline, matched []map[string]string) []string {
	if reg == nil || len(matched) == 0 {
		return nil
	}
	type roleGroup struct {
		ex         merge.Exclusion
		collectors []string
	}
	byRole := map[string]*roleGroup{}
	checked := map[string]bool{}
	for _, m := range matched {
		role := m["role"]
		if !checked[role] {
			checked[role] = true
			if ex, excluded := merge.RoleExclusion(p, role, reg); excluded {
				byRole[role] = &roleGroup{ex: ex}
			}
		}
		if g, ok := byRole[role]; ok {
			g.collectors = append(g.collectors, m["cluster"]+"/"+role)
		}
	}
	roles := make([]string, 0, len(byRole))
	for r := range byRole {
		roles = append(roles, r)
	}
	sort.Strings(roles)

	warnings := make([]string, 0, len(roles))
	for _, r := range roles {
		g := byRole[r]
		why := g.ex.Reason
		if !g.ex.Disallowed.Empty() {
			why = fmt.Sprintf("its signals (%s) are not allowed on role %s", signalProse(g.ex.Disallowed), r)
		}
		warnings = append(warnings, fmt.Sprintf("Excluded from %d collector(s): %s — %s.",
			len(g.collectors), strings.Join(g.collectors, ", "), why))
	}
	return warnings
}

// signalProse renders a signal set as "logs, traces".
func signalProse(set signals.Set) string {
	return strings.Join(signalStrings(set), ", ")
}
