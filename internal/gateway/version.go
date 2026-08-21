// Package gateway holds Shepherd's Gateway API support: the version contract it
// requires of a cluster, and (later) the HTTPRoute rendering built on top of it.
//
// Shepherd speaks Gateway API and nothing else — no Ingress fallback, no
// annotation-based abstraction (docs/gateway-tier-plan.md D1). That makes the
// installed CRD version a hard dependency rather than a detail, because a
// cluster whose CRDs are older or on the wrong channel will accept an
// HTTPRoute and then quietly route nothing. A route that renders but does not
// route is the silent-by-construction failure this repo keeps removing, so the
// contract is checked explicitly and refused with a sentence a human can act on.
package gateway

import (
	"fmt"
	"strconv"
	"strings"
)

// MinBundleVersion is the minimum Gateway API bundle version Shepherd supports,
// as major.minor. Kept in lockstep with GATEWAY_API_VERSION in
// deploy/versions.env by `make check-gateway-pin` — the kind suite installs
// that version, so testing conformance against one version while the code
// required another would make the claim meaningless.
//
// The 1.4 line is the floor because it is what most controllers ship. Patch
// level is deliberately not part of the contract: a controller on v1.4.0 and
// one on v1.4.1 expose the same API surface to us.
const MinBundleVersion = "1.4"

// RequiredChannel is the Gateway API release channel Shepherd requires.
//
// This matters more than the version number. Experimental-channel features can
// change shape between releases, and Shepherd deliberately depends on nothing
// outside Standard: RequestHeaderModifier (Core) for tenant injection and
// URLRewrite/ReplacePrefixMatch (Extended) for prefix routing, both present in
// Standard at MinBundleVersion. The CORS filter is NOT in Standard at 1.4 —
// Alloy's faro.receiver answers CORS instead (D2).
const RequiredChannel = "standard"

// Annotations the Gateway API project stamps on its own CRDs. These are the
// canonical discovery mechanism — they are set by the released install YAML
// itself, so they describe what is actually installed rather than what someone
// believes they installed.
const (
	BundleVersionAnnotation = "gateway.networking.k8s.io/bundle-version"
	ChannelAnnotation       = "gateway.networking.k8s.io/channel"
)

// ErrUnsupported is returned by CheckSupport for every rejection, so callers
// can distinguish "this cluster cannot serve routes" from a transport failure.
type ErrUnsupported struct{ Reason string }

func (e *ErrUnsupported) Error() string { return e.Reason }

// CheckSupport reports whether a cluster carrying the given CRD annotations can
// serve the routes Shepherd renders.
//
// bundleVersion and channel come from the HTTPRoute CRD's own annotations; an
// empty bundleVersion means the CRD was found but carries no version stamp,
// which is treated as unsupported rather than assumed-modern — an unstamped CRD
// is more likely a hand-rolled or vendored copy than a current release, and
// guessing in the permissive direction is how routes end up silently ignored.
//
// Every rejection names what was found, what is required, and what to do.
func CheckSupport(bundleVersion, channel string) error {
	if bundleVersion == "" {
		return &ErrUnsupported{Reason: fmt.Sprintf(
			"the Gateway API HTTPRoute CRD carries no %s annotation, so its version cannot be "+
				"determined. Shepherd requires Gateway API %s or newer on the %s channel. Install it with "+
				"the released standard-install.yaml rather than a vendored copy.",
			BundleVersionAnnotation, MinBundleVersion, RequiredChannel)}
	}

	gotMajor, gotMinor, err := parseMajorMinor(bundleVersion)
	if err != nil {
		return &ErrUnsupported{Reason: fmt.Sprintf(
			"the Gateway API HTTPRoute CRD reports bundle version %q, which is not a recognizable "+
				"vMAJOR.MINOR version. Shepherd requires %s or newer on the %s channel.",
			bundleVersion, MinBundleVersion, RequiredChannel)}
	}
	wantMajor, wantMinor, err := parseMajorMinor(MinBundleVersion)
	if err != nil {
		// Unreachable unless MinBundleVersion itself is malformed, which
		// check-gateway-pin would have caught first.
		return fmt.Errorf("gateway: malformed MinBundleVersion %q: %w", MinBundleVersion, err)
	}

	if gotMajor < wantMajor || (gotMajor == wantMajor && gotMinor < wantMinor) {
		return &ErrUnsupported{Reason: fmt.Sprintf(
			"this cluster has Gateway API %s but Shepherd requires %s or newer. Older CRDs accept the "+
				"HTTPRoutes Shepherd renders and then ignore the filters they depend on, so tenant routing "+
				"would silently not work. Upgrade the Gateway API CRDs.",
			bundleVersion, MinBundleVersion)}
	}

	// Channel: anything other than the required channel is refused, including
	// experimental. Experimental is a SUPERSET of standard, so it would in fact
	// work — but accepting it would mean Shepherd runs untested against a
	// channel whose feature shapes can change between releases, and the failure
	// would surface as mysterious routing behaviour rather than a clear refusal.
	if channel != "" && channel != RequiredChannel {
		return &ErrUnsupported{Reason: fmt.Sprintf(
			"this cluster's Gateway API CRDs are on the %q channel; Shepherd is built and tested "+
				"against %q. Install the standard-install.yaml bundle for Gateway API %s or newer.",
			channel, RequiredChannel, MinBundleVersion)}
	}

	return nil
}

// parseMajorMinor accepts "v1.4.1", "1.4.1", "v1.4" and "1.4".
func parseMajorMinor(v string) (major, minor int, err error) {
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("not a MAJOR.MINOR version: %q", v)
	}
	if major, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, fmt.Errorf("major in %q: %w", v, err)
	}
	if minor, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, fmt.Errorf("minor in %q: %w", v, err)
	}
	return major, minor, nil
}
