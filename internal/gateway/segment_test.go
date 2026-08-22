package gateway

import (
	"context"
	"strings"
	"testing"
)

func TestValidateSegment_AcceptsWellFormed(t *testing.T) {
	for _, s := range []string{
		"abc",
		"acme-a1b2c3d4",
		"k7f3n9qp3mx8",
		"a-b-c-d-e-f",
		strings.Repeat("a", maxSegmentLength),
	} {
		if err := ValidateSegment(s); err != nil {
			t.Errorf("ValidateSegment(%q) = %v, want nil", s, err)
		}
	}
}

// TestValidateSegment_RejectsCharsetViolations pins the exact set of things
// D9 requires a segment's charset to exclude: path separators, control
// characters, percent-encoding, uppercase, and leading/trailing/doubled
// hyphens. Each case here is also this control's red-run target — see the
// package-level comment below for the one-line revert.
func TestValidateSegment_RejectsCharsetViolations(t *testing.T) {
	cases := map[string]string{
		"path separator":     "acme/prod",
		"parent traversal":   "..",
		"embedded traversal": "acme-../etc",
		"control character":  "acme-\x00abc",
		"percent-encoding":   "acme%2fprod",
		"uppercase":          "ACME-prod",
		"leading hyphen":     "-acme-prod",
		"trailing hyphen":    "acme-prod-",
		"doubled hyphen":     "acme--prod",
		"space":              "acme prod",
		"unicode confusable": "аcme-prod", // Cyrillic 'а' (U+0430), not ASCII 'a'
		"too short":          "ab",
		"too long":           strings.Repeat("a", maxSegmentLength+1),
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateSegment(s); err == nil {
				t.Errorf("ValidateSegment(%q) = nil, want an error (%s)", s, name)
			}
		})
	}
}

// TestGenerateSegment_SlugSuffixSanitizesTraversalTenantName pins the
// requirement stated explicitly in the W4 task: "a tenant named ../../etc
// must not produce a traversal segment." A generated segment must both
// pass ValidateSegment (proving no "/" or ".." byte survived) and must not
// contain "/" or ".." as substrings, checked directly here rather than only
// through ValidateSegment, so this test fails on its own even if
// ValidateSegment's charset check were independently broken.
func TestGenerateSegment_SlugSuffixSanitizesTraversalTenantName(t *testing.T) {
	for _, tenantName := range []string{
		"../../etc",
		"../../../etc/passwd",
		"..",
		"////",
		"a/../b",
	} {
		segment, err := GenerateSegment(context.Background(), FormatSlugSuffix, KindOTLP, tenantName, nil)
		if err != nil {
			t.Fatalf("GenerateSegment(%q) returned error: %v", tenantName, err)
		}
		if strings.Contains(segment, "/") || strings.Contains(segment, "..") {
			t.Fatalf("GenerateSegment(%q) = %q, contains a traversal fragment", tenantName, segment)
		}
		if err := ValidateSegment(segment); err != nil {
			t.Fatalf("GenerateSegment(%q) = %q, which fails ValidateSegment: %v", tenantName, segment, err)
		}
	}
}

func TestGenerateSegment_SlugSuffixKeepsReadableTenantName(t *testing.T) {
	segment, err := GenerateSegment(context.Background(), FormatSlugSuffix, KindOTLP, "Acme Corp", nil)
	if err != nil {
		t.Fatalf("GenerateSegment returned error: %v", err)
	}
	if !strings.HasPrefix(segment, "acme-corp-") {
		t.Errorf("GenerateSegment(%q) = %q, want prefix %q", "Acme Corp", segment, "acme-corp-")
	}
}

// TestGenerateSegment_OpaqueFormatDisclosesNoTenantSubstring pins D9's
// promise for the opaque format: no fragment of the tenant name appears in
// the emitted segment, unlike slug-suffix.
func TestGenerateSegment_OpaqueFormatDisclosesNoTenantSubstring(t *testing.T) {
	segment, err := GenerateSegment(context.Background(), FormatOpaque, KindOTLP, "Acme Corp", nil)
	if err != nil {
		t.Fatalf("GenerateSegment returned error: %v", err)
	}
	if strings.Contains(segment, "acme") {
		t.Errorf("GenerateSegment(FormatOpaque, %q) = %q, discloses the tenant name", "Acme Corp", segment)
	}
	if len(segment) != opaqueLength {
		t.Errorf("GenerateSegment(FormatOpaque) = %q, want length %d, got %d", segment, opaqueLength, len(segment))
	}
	if err := ValidateSegment(segment); err != nil {
		t.Errorf("GenerateSegment(FormatOpaque) = %q fails ValidateSegment: %v", segment, err)
	}
}

// TestGenerateSegment_UsesCryptoRandNotMathRand does not (and cannot from a
// black-box test) prove which package supplies the randomness; what it CAN
// prove is that repeated generation is not deterministic/collision-prone,
// which a math/rand source seeded predictably (e.g. from time.Now(), or not
// seeded at all before Go 1.20) would risk. Combined with the source
// reading (randomSegmentString calls crypto/rand.Reader directly, never
// math/rand), this is the practical check available at this layer.
func TestGenerateSegment_UsesCryptoRandNotMathRand(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		segment, err := GenerateSegment(context.Background(), FormatOpaque, KindOTLP, "", nil)
		if err != nil {
			t.Fatalf("GenerateSegment returned error: %v", err)
		}
		if seen[segment] {
			t.Fatalf("GenerateSegment produced a duplicate opaque segment %q within 50 draws (72 bits of entropy makes this astronomically unlikely; suspect a broken/non-random generator)", segment)
		}
		seen[segment] = true
	}
}

// TestGenerateSegment_RetriesOnCollision pins the "uniqueness" half of
// D9's generation contract at the application layer: GenerateSegment must
// retry when ExistsFunc reports a collision, not return the colliding
// value.
func TestGenerateSegment_RetriesOnCollision(t *testing.T) {
	calls := 0
	exists := func(_ context.Context, _ RouteKind, _ string) (bool, error) {
		calls++
		return calls <= 3, nil // first 3 candidates "collide", 4th is free
	}
	segment, err := GenerateSegment(context.Background(), FormatOpaque, KindOTLP, "", exists)
	if err != nil {
		t.Fatalf("GenerateSegment returned error: %v", err)
	}
	if calls != 4 {
		t.Errorf("GenerateSegment called exists %d times, want exactly 4 (3 collisions + 1 success)", calls)
	}
	if err := ValidateSegment(segment); err != nil {
		t.Errorf("GenerateSegment returned invalid segment %q: %v", segment, err)
	}
}

// TestGenerateSegment_ExhaustsAttemptsOnPersistentCollision proves
// GenerateSegment fails loudly rather than looping forever or silently
// returning a colliding value when ExistsFunc always reports a collision.
func TestGenerateSegment_ExhaustsAttemptsOnPersistentCollision(t *testing.T) {
	always := func(_ context.Context, _ RouteKind, _ string) (bool, error) { return true, nil }
	_, err := GenerateSegment(context.Background(), FormatOpaque, KindOTLP, "", always)
	if err == nil {
		t.Fatal("GenerateSegment with an always-colliding ExistsFunc returned nil error, want an error")
	}
}

func TestGenerateSegment_RejectsUnknownFormat(t *testing.T) {
	_, err := GenerateSegment(context.Background(), SegmentFormat("subdomain"), KindOTLP, "acme", nil)
	if err == nil {
		t.Fatal("GenerateSegment with an unknown format returned nil error, want an error")
	}
}

func TestValidFormat(t *testing.T) {
	if !ValidFormat(FormatOpaque) {
		t.Error("ValidFormat(FormatOpaque) = false, want true")
	}
	if !ValidFormat(FormatSlugSuffix) {
		t.Error("ValidFormat(FormatSlugSuffix) = false, want true")
	}
	if ValidFormat(SegmentFormat("subdomain")) {
		t.Error("ValidFormat(\"subdomain\") = true, want false")
	}
}
