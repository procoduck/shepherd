package gateway

import (
	"strings"
	"testing"
)

// ValidateGatewayRef applies Kubernetes' own name rules (DNS-1123 subdomain
// for the Gateway's name, DNS-1123 label for its namespace).
func TestValidateGatewayRef(t *testing.T) {
	for _, tc := range []struct {
		name, gw, ns string
		wantIn       string // empty: valid
	}{
		{name: "ordinary name, no namespace", gw: "shepherd-receiver-gw"},
		{name: "dotted name", gw: "edge.gw-1", ns: "gateway-system"},
		{name: "name at the length limit", gw: strings.Repeat("a", 253)},
		{name: "namespace at the length limit", gw: "edge", ns: strings.Repeat("a", 63)},

		{name: "empty name", gw: "", wantIn: "gateway name is required"},
		{name: "spaces and punctuation", gw: "Bad Name!", wantIn: `gateway name "Bad Name!" is not a valid Kubernetes object name`},
		{name: "upper case", gw: "Edge", wantIn: "not a valid Kubernetes object name"},
		{name: "leading dash", gw: "-edge", wantIn: "not a valid Kubernetes object name"},
		{name: "name one over the limit", gw: strings.Repeat("a", 254), wantIn: "at most 253 characters"},
		{name: "dotted namespace", gw: "edge", ns: "gw.system", wantIn: `gateway namespace "gw.system" is not a valid Kubernetes namespace`},
		{name: "underscore in namespace", gw: "edge", ns: "gw_system", wantIn: "not a valid Kubernetes namespace"},
		{name: "namespace one over the limit", gw: "edge", ns: strings.Repeat("a", 64), wantIn: "at most 63 characters"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateGatewayRef(tc.gw, tc.ns)
			if tc.wantIn == "" {
				if err != nil {
					t.Fatalf("ValidateGatewayRef(%q, %q) = %v, want nil", tc.gw, tc.ns, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("ValidateGatewayRef(%q, %q) = %v, want an error containing %q", tc.gw, tc.ns, err, tc.wantIn)
			}
		})
	}
}
