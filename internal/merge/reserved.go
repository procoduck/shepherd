package merge

import "strings"

// reservedExact are attribute/label keys that already carry a specific,
// built-in meaning in the merge engine or elsewhere in Shepherd: cluster and
// role are existing match labels (role also carries signals.Policies
// enforcement), id mirrors Grafana Cloud Fleet Management's own reservation
// of the same name, os and alloy_version mirror real collector_instances
// columns that are likely future derived built-ins.
var reservedExact = map[string]bool{
	"cluster":       true,
	"role":          true,
	"id":            true,
	"os":            true,
	"alloy_version": true,
}

// reservedPrefixes namespace future built-ins so adding one is never a
// breaking change for an admin/agent-set key that happens to collide.
// collector.* mirrors Grafana Cloud Fleet Management's own convention;
// shepherd.* is Shepherd-native, kept separate so a future Shepherd-only
// derived concept (e.g. shepherd.pipeline_count) doesn't have to colonize
// collector.*.
var reservedPrefixes = []string{"collector.", "shepherd."}

// IsReserved reports whether key is reserved for built-in use and must not
// be settable as an admin label or usable as agent-reported local_attributes.
// Callers must lowercase key first — reserved keys are always lowercase, and
// this function does not normalize case itself.
func IsReserved(key string) bool {
	if reservedExact[key] {
		return true
	}
	for _, prefix := range reservedPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}
