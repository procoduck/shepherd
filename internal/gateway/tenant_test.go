package gateway

import (
	"strings"
	"testing"
)

// The values below are Grafana Mimir's own documented rules, read from
// grafana.com/docs/mimir/latest/configure/about-tenant-ids/ on 2026-08-22.
// Testing against the real published rule rather than an invented one is the
// point: this fails if Shepherd's idea of a valid tenant drifts from what the
// destination will actually accept.
func TestValidateTenantID(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     string
		wantOK bool
		wantIn string
	}{
		{name: "ordinary slug", in: "acme", wantOK: true},
		{name: "mixed case is allowed", in: "AcmeCorp", wantOK: true},
		{name: "digits and dashes", in: "acme-42", wantOK: true},
		{name: "the full documented punctuation set", in: "a!-_.*'()", wantOK: true},
		{name: "at the length limit", in: strings.Repeat("a", MaxTenantIDLen), wantOK: true},

		{name: "empty", in: "", wantIn: "must not be empty"},
		{name: "one byte over the limit", in: strings.Repeat("a", MaxTenantIDLen+1), wantIn: "the limit is 150"},
		{name: "a slash would break the header and the slug", in: "acme/evil", wantIn: "outside the allowed set"},
		{name: "whitespace is not permitted", in: "acme corp", wantIn: "outside the allowed set"},
		{name: "a newline is not permitted", in: "acme\nX-Other: 1", wantIn: "outside the allowed set"},
		{name: "dot is reserved", in: ".", wantIn: "reserved"},
		{name: "dot-dot is reserved", in: "..", wantIn: "reserved"},
		{name: "mimir's internal tenant is reserved", in: "__mimir_cluster", wantIn: "reserved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTenantID(tc.in)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("ValidateTenantID(%q) = %v, want nil", tc.in, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateTenantID(%q) = nil, want a refusal", tc.in)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Fatalf("refusal %q does not contain %q — a rejection at org-creation time must say "+
					"what is allowed, not just that it said no", err.Error(), tc.wantIn)
			}
		})
	}
}

// A tenant id reaches the wire as a header value. Anything ValidateTenantID
// accepts must also survive RenderHTTPRoute, or the two checks disagree about
// what a legal tenant is and the disagreement surfaces at apply time instead
// of at creation time.
func TestEveryValidTenantIDIsRenderable(t *testing.T) {
	for _, id := range []string{"acme", "AcmeCorp", "acme-42", "a!-_.*'()", strings.Repeat("a", MaxTenantIDLen)} {
		if err := ValidateTenantID(id); err != nil {
			t.Fatalf("fixture %q is not valid, so this test proves nothing: %v", id, err)
		}
		_, err := RenderHTTPRoute(RouteSpec{
			Name: "r", Namespace: "ns", TenantID: id, Kind: KindOTLP,
			RouteSegment: "acme-a1b2c3", GatewayName: "gw",
			BackendName: "echo", BackendPort: 8080,
		})
		if err != nil {
			t.Fatalf("RenderHTTPRoute refused tenant id %q that ValidateTenantID accepts: %v", id, err)
		}
	}
}
