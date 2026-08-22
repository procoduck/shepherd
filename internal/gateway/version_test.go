package gateway

import (
	"errors"
	"strings"
	"testing"
)

// The values below are the real annotations the Gateway API project stamps on
// its released CRDs, read from the v1.4.0 and v1.6.1 install bundles on
// 2026-08-21. Using real strings rather than invented ones is the point: this
// test fails if the contract drifts from what upstream actually ships.
func TestCheckSupport(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		channel string
		wantErr bool
		// wantIn is a substring the refusal must contain, so a rejection stays
		// actionable rather than degenerating into a bare "unsupported".
		wantIn string
	}{
		{name: "pinned floor, standard channel", version: "v1.4.0", channel: "standard"},
		{name: "floor patch release", version: "v1.4.1", channel: "standard"},
		{name: "newer minor", version: "v1.6.1", channel: "standard"},
		{name: "newer major", version: "v2.0.0", channel: "standard"},
		{name: "no channel annotation is tolerated", version: "v1.4.0", channel: ""},

		{
			name: "one minor below the floor", version: "v1.3.0", channel: "standard",
			wantErr: true, wantIn: "requires 1.4 or newer",
		},
		{
			name: "much older", version: "v1.0.0", channel: "standard",
			wantErr: true, wantIn: "Upgrade the Gateway API CRDs",
		},
		{
			// Experimental is a superset and would technically work; refusing it
			// is deliberate (see the comment in CheckSupport).
			name: "experimental channel refused", version: "v1.6.1", channel: "experimental",
			wantErr: true, wantIn: "experimental",
		},
		{
			name: "unstamped CRD is not assumed modern", version: "", channel: "standard",
			wantErr: true, wantIn: "carries no",
		},
		{
			name: "unparseable version", version: "latest", channel: "standard",
			wantErr: true, wantIn: "not a recognizable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckSupport(tc.version, tc.channel)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("CheckSupport(%q, %q) = nil, want a refusal", tc.version, tc.channel)
				}
				var unsup *ErrUnsupported
				if !errors.As(err, &unsup) {
					t.Fatalf("error is %T, want *ErrUnsupported so callers can tell refusal from transport failure", err)
				}
				if tc.wantIn != "" && !strings.Contains(err.Error(), tc.wantIn) {
					t.Fatalf("refusal %q does not contain %q — a rejection must say what to do about it",
						err.Error(), tc.wantIn)
				}
				return
			}
			if err != nil {
				t.Fatalf("CheckSupport(%q, %q) = %v, want nil", tc.version, tc.channel, err)
			}
		})
	}
}

// The floor is a claim Shepherd makes to operators and proves in the kind
// suite. If it moves, deploy/versions.env, the kind suite's installed CRDs and
// this constant must move together — `make check-gateway-pin` enforces the
// first two, this pins the third so a silent edit here fails loudly.
func TestContractConstantsAreTheDocumentedOnes(t *testing.T) {
	if MinBundleVersion != "1.4" {
		t.Fatalf("MinBundleVersion = %q, want \"1.4\" — if the floor really moved, update "+
			"deploy/versions.env, the kind suite's CRD install and docs/gateway-tier-plan.md D1/D2 "+
			"in the same change, including re-verifying which filters are in the Standard channel "+
			"at the new floor", MinBundleVersion)
	}
	if RequiredChannel != "standard" {
		t.Fatalf("RequiredChannel = %q, want \"standard\" — Shepherd deliberately depends on no "+
			"Experimental-channel feature (D2)", RequiredChannel)
	}
}
