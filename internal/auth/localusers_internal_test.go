package auth

import (
	"testing"
	"time"
)

// dummyHash exists so the no-such-user login path costs the same as a
// wrong-password one. That only works if it is a REAL argon2id hash: replace it
// with a placeholder string and VerifyPassword returns immediately, handing
// back exactly the username-enumeration timing oracle it was added to close.
//
// This test fails if that happens, which a reader tidying up a "weird looking
// constant" would otherwise never discover.
func TestDummyHashIsRealAndComparablyExpensive(t *testing.T) {
	real, err := HashPassword("correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("hashing: %v", err)
	}

	start := time.Now()
	ok, err := VerifyPassword(dummyHash, "any-guess")
	dummyCost := time.Since(start)
	if err != nil {
		t.Fatalf("dummyHash does not parse as argon2id (%v) — the timing equalisation is doing nothing", err)
	}
	if ok {
		t.Fatal("dummyHash accepted a password; it must never match anything")
	}

	start = time.Now()
	_, _ = VerifyPassword(real, "any-guess") //nolint:errcheck // cost measurement only
	realCost := time.Since(start)

	// Generous bound: this is asserting the same order of magnitude, not a
	// stopwatch. A placeholder string returns in nanoseconds and fails loudly.
	if dummyCost < realCost/4 {
		t.Errorf("dummy path %v vs real %v — not comparable, still a timing oracle", dummyCost, realCost)
	}
}
