package mgmtapi

import "testing"

func TestReservedCollectorLabelKey(t *testing.T) {
	reserved := []string{
		"cluster", "role", "id", "os", "alloy_version",
		"collector.", "collector.team", "collector.env.tier",
		"shepherd.", "shepherd.managed",
	}
	for _, k := range reserved {
		if !reservedCollectorLabelKey(k) {
			t.Errorf("reservedCollectorLabelKey(%q) = false, want true", k)
		}
	}

	allowed := []string{
		"team", "environment", "tier", "cost-center",
		// A reserved word as a substring or suffix, not the exact key or a
		// reserved-prefix, is fine.
		"my-cluster", "role-name", "os-family", "app.collector", "shepherd-adjacent",
	}
	for _, k := range allowed {
		if reservedCollectorLabelKey(k) {
			t.Errorf("reservedCollectorLabelKey(%q) = true, want false", k)
		}
	}
}
