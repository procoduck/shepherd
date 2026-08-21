package gateway

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

// This file is the generation POLICY for RouteSpec.RouteSegment (D9): what a
// segment looks like, how one is minted, and how any segment value —
// freshly generated or read back from storage — is validated before it is
// ever handed to RenderHTTPRoute (route.go). RenderHTTPRoute already treats
// RouteSegment as opaque (route.go's routeSegmentRE is a looser superset
// safety net); this file is stricter and is where the actual policy lives.

// SegmentFormat selects which of D9's two supported route-prefix formats a
// newly minted tenant segment uses. The choice is per installation
// (plan §D9); this package does not decide a default for a deployment, it
// only implements both.
type SegmentFormat string

const (
	// FormatOpaque discloses nothing about the tenant: the entire segment
	// is random, e.g. "k7f3n9qp3mx8" (plan §D9 example). Chosen by
	// operators who treat their customer list as sensitive.
	FormatOpaque SegmentFormat = "opaque"

	// FormatSlugSuffix is D9's DEFAULT: a slugified tenant name plus an
	// unguessable random suffix, e.g. "acme-a1b2c3d4f5". Debuggable ("ops
	// reads acme-… in a gateway log and knows whose traffic it is") while
	// the suffix keeps the full segment unguessable.
	FormatSlugSuffix SegmentFormat = "slug-suffix"
)

// segmentAlphabet is the 36-symbol alphabet every random segment component
// (opaque segments, the slug-suffix's suffix) is drawn from: lowercase
// letters and digits only. This is deliberately the SAME alphabet
// ValidateSegment's charset accepts for the whole segment (minus the
// hyphen, which random components never emit themselves — hyphens are
// inserted only as an explicit separator, never sampled), so a freshly
// generated component can never itself violate the charset it will be
// checked against.
const segmentAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// opaqueLength is the length of a FormatOpaque segment: 14 characters from
// segmentAlphabet. Entropy: 14 * log2(36) ≈ 14 * 5.17 ≈ 72.4 bits.
//
// Why 14, not shorter or longer: the segment is an identifier, not an
// authorizer (plan §3) — the real controls are origin allowlists, rate
// limits and rotation, not the segment's secrecy — so this does not need
// cryptographic-secret-grade entropy. But FormatOpaque's whole point is
// that the segment discloses NOTHING (unlike slug-suffix, which trades some
// entropy for a readable prefix), so it needs enough bits that scanning the
// URL space (trying random segments against the gateway) is not a practical
// enumeration attack even at high request volume: at 72 bits, an attacker
// sending 10^6 requests/sec would need on the order of 10^15 years to
// enumerate the space. 14 chars also keeps the emitted URL short, which
// matters because D9 notes these appear in Referer headers and gateway
// logs.
const opaqueLength = 14

// suffixLength is the length of FormatSlugSuffix's random suffix: 8
// characters from segmentAlphabet. Entropy: 8 * log2(36) ≈ 8 * 5.17 ≈ 41.4 bits.
//
// Why 8, shorter than opaqueLength: slug-suffix's own design already trades
// away some unguessability for debuggability (the slug half is the
// tenant's own name, not secret at all) — see D9's rationale table. 41 bits
// is still far beyond what an org's tenant count could ever collide
// against by chance (birthday bound: ~2^20 tenants before a 50% collision
// chance against the SAME slug, and GenerateSegment's uniqueness check/
// storage-layer UNIQUE constraint catches even that), while keeping the
// example from D9's own table ("acme-a1b2c3") close to true length.
const suffixLength = 8

// maxSlugLength bounds the slugified-tenant-name portion of a
// FormatSlugSuffix segment so one long tenant name cannot push the overall
// segment past maxSegmentLength, and so the segment stays a short,
// log-scannable token.
const maxSlugLength = 32

// minSegmentLength / maxSegmentLength bound EVERY segment, regardless of
// format or origin (generated here, or read back from storage before being
// handed to RenderHTTPRoute). maxSegmentLength matches route.go's
// routeSegmentRE bound (63, the DNS-label convention Kubernetes object
// names and path segments both tend to follow).
const (
	minSegmentLength = 3
	maxSegmentLength = 63
)

// segmentCharsetRE is the single source of truth for "a bounded charset:
// lowercase alnum plus single hyphens — no path separators, no control
// characters, no percent-encoding, no leading/trailing/doubled hyphens"
// (plan §D9). Because the character class is exactly [a-z0-9] and hyphens
// may only separate two non-empty such runs, this regex alone rules out
// every one of those cases: a "/" or ".." never matches [a-z0-9-], a
// leading/trailing hyphen can never start/end a "[a-z0-9]+" run, and two
// consecutive hyphens can never appear because the hyphen is required to be
// immediately followed by another "[a-z0-9]+" run, not another hyphen.
var segmentCharsetRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// nonSlugRunRE matches every run of characters that a slug must not carry:
// anything other than a lowercase ASCII letter or digit. slugify collapses
// each run to a single hyphen, which is what defensively neutralizes a
// tenant name like "../../etc" — the "/" and "." characters are exactly
// what this class excludes, so they can never survive into a segment.
var nonSlugRunRE = regexp.MustCompile(`[^a-z0-9]+`)

// ValidateSegment enforces D9's generation contract on ANY segment value —
// whether just generated by GenerateSegment, or read back from storage
// before being handed to RenderHTTPRoute, or (if a future caller ever
// accepts one) supplied externally. It does not check uniqueness: that is
// a storage-layer property (internal/migrations' tenant_routes table
// UNIQUE(kind, segment) constraint plus its partial "one active row" index)
// evaluated against all other segments, not a property of one value in
// isolation.
func ValidateSegment(segment string) error {
	if len(segment) < minSegmentLength || len(segment) > maxSegmentLength {
		return fmt.Errorf("gateway: segment %q must be between %d and %d characters, got %d",
			segment, minSegmentLength, maxSegmentLength, len(segment))
	}
	if !segmentCharsetRE.MatchString(segment) {
		return fmt.Errorf("gateway: segment %q must be lowercase alphanumerics separated by single "+
			"hyphens only, no leading/trailing/doubled hyphens, no path separators, no control "+
			"characters, no percent-encoding (must match %s)", segment, segmentCharsetRE.String())
	}
	return nil
}

// ValidFormat reports whether f is a SegmentFormat GenerateSegment
// understands.
func ValidFormat(f SegmentFormat) bool {
	return f == FormatOpaque || f == FormatSlugSuffix
}

// ExistsFunc reports whether segment is already in use for kind. Callers
// wire this to a storage lookup (see internal/mgmtapi's use of the
// TenantRouteSegmentExists query); this package has no dependency on
// internal/store, so uniqueness checking is injected rather than imported —
// the storage layer's UNIQUE(kind, segment) constraint (0009_tenant_routes)
// is the final backstop against a TOCTOU race between this check and the
// INSERT.
type ExistsFunc func(ctx context.Context, kind RouteKind, segment string) (bool, error)

// maxGenerateAttempts bounds GenerateSegment's collision-retry loop. Chosen
// generously relative to the entropy above: a collision on the first
// attempt is already astronomically unlikely for either format, so
// repeated collisions up to this bound indicate ExistsFunc itself is
// misbehaving (e.g. always returning true) rather than bad luck, and
// GenerateSegment reports that as an error instead of looping forever.
const maxGenerateAttempts = 20

// GenerateSegment mints a new, validated, unique RouteSpec.RouteSegment
// value for kind, in the given format. tenantName seeds FormatSlugSuffix's
// slug (ignored for FormatOpaque); it is treated as fully untrusted input
// and defensively sanitized by slugify — see
// TestGenerateSegment_SlugSuffixSanitizesTraversalTenantName, which pins
// that a tenant named "../../etc" cannot produce a traversal-shaped
// segment. exists may be nil (skips the application-level uniqueness
// check, relying solely on the storage layer's constraint); pass it
// whenever a storage lookup is available, since it turns most collisions
// into a cheap retry instead of a failed INSERT.
func GenerateSegment(ctx context.Context, format SegmentFormat, kind RouteKind, tenantName string, exists ExistsFunc) (string, error) {
	if !ValidFormat(format) {
		return "", fmt.Errorf("gateway: unknown segment format %q (want %q or %q)", format, FormatOpaque, FormatSlugSuffix)
	}
	for attempt := 0; attempt < maxGenerateAttempts; attempt++ {
		candidate, err := newCandidateSegment(format, tenantName)
		if err != nil {
			return "", err
		}
		// Defense in depth: a generated value failing its own validator is
		// a defect in newCandidateSegment, not a retryable collision, so
		// this is a hard error rather than a `continue`.
		if err := ValidateSegment(candidate); err != nil {
			return "", fmt.Errorf("gateway: generated segment %q failed validation: %w", candidate, err)
		}
		if exists != nil {
			used, err := exists(ctx, kind, candidate)
			if err != nil {
				return "", fmt.Errorf("gateway: checking segment %q uniqueness: %w", candidate, err)
			}
			if used {
				continue
			}
		}
		return candidate, nil
	}
	return "", fmt.Errorf("gateway: could not mint a unique %s segment for kind %q after %d attempts",
		format, kind, maxGenerateAttempts)
}

// newCandidateSegment builds one (unchecked-for-uniqueness) candidate
// segment for format.
func newCandidateSegment(format SegmentFormat, tenantName string) (string, error) {
	switch format {
	case FormatOpaque:
		return randomSegmentString(opaqueLength)
	case FormatSlugSuffix:
		suffix, err := randomSegmentString(suffixLength)
		if err != nil {
			return "", err
		}
		slug := slugify(tenantName, maxSlugLength)
		if slug == "" {
			// tenantName sanitized to nothing at all (e.g. a name made
			// entirely of path-traversal punctuation like "../.."). Rather
			// than emit a leading hyphen (which segmentCharsetRE — and
			// therefore ValidateSegment — would reject), fall back to the
			// suffix alone: still unique, still valid, just indistinguishable
			// from an opaque segment for this one pathological tenant name.
			return suffix, nil
		}
		return slug + "-" + suffix, nil
	default:
		return "", fmt.Errorf("gateway: unknown segment format %q", format)
	}
}

// slugify defensively sanitizes tenantName into a segment-safe slug: lower­
// case, every run of one-or-more non-[a-z0-9] characters collapsed to a
// single hyphen, leading/trailing hyphens trimmed, truncated to maxLen. A
// tenant name is externally-supplied and untrusted (an org admin sets it),
// so this treats it exactly like the traversal-hostile input it might be —
// "../../etc" becomes "etc", "//" becomes "" (see GenerateSegment's
// empty-slug fallback above), and no byte of the original path separators,
// dots, control characters or percent-encoding survives, because none of
// them are in [a-z0-9].
func slugify(tenantName string, maxLen int) string {
	lower := strings.ToLower(tenantName)
	collapsed := nonSlugRunRE.ReplaceAllString(lower, "-")
	trimmed := strings.Trim(collapsed, "-")
	if len(trimmed) <= maxLen {
		return trimmed
	}
	// Truncating mid-run could leave a trailing hyphen (or, at a rune
	// boundary, split a multi-byte character already excluded above — moot
	// since only ASCII survives collapsing) — trim again after cutting.
	return strings.Trim(trimmed[:maxLen], "-")
}

// randomSegmentString returns n characters drawn uniformly from
// segmentAlphabet using crypto/rand — never math/rand. math/rand (even
// seeded from crypto/rand) is a deterministic PRNG whose internal state
// can be reconstructed from a handful of observed outputs; since these
// suffixes/opaque segments are the one thing standing between "debuggable"
// and "guessable" for FormatOpaque (and contribute the only real
// unguessability FormatSlugSuffix has), the generator itself must be a
// CSPRNG. rand.Int's rejection-sampling against segmentAlphabet's length
// keeps the distribution uniform (no modulo bias).
func randomSegmentString(n int) (string, error) {
	alphabetLen := big.NewInt(int64(len(segmentAlphabet)))
	out := make([]byte, n)
	for i := range out {
		idx, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			return "", fmt.Errorf("gateway: reading crypto/rand: %w", err)
		}
		out[i] = segmentAlphabet[idx.Int64()]
	}
	return string(out), nil
}
