package merge

import (
	"strings"
	"testing"
)

func TestIsReserved(t *testing.T) {
	cases := []struct {
		key      string
		reserved bool
	}{
		// Exact reserved keys.
		{"cluster", true},
		{"role", true},
		{"id", true},
		{"os", true},
		{"alloy_version", true},
		// Prefix reserved keys.
		{"collector.foo", true},
		{"collector.", true},
		{"shepherd.pipeline_count", true},
		{"shepherd.", true},
		// Case: callers are expected to lowercase first (mirrors
		// validCollectorLabelKey's contract in mgmtapi/rpc_fleet.go); confirm
		// the post-lowercase form of a mixed-case key is caught.
		{strings.ToLower("Collector.Foo"), true},
		{strings.ToLower("ROLE"), true},
		// Non-reserved keys, including near-misses that must NOT match.
		{"team", false},
		{"environment", false},
		{"clusters", false},
		{"role_group", false},
		{"collectorfoo", false},
		{"shepherds", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsReserved(tc.key); got != tc.reserved {
			t.Errorf("IsReserved(%q) = %v, want %v", tc.key, got, tc.reserved)
		}
	}
}
