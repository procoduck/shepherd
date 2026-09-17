package mgmtapi

import "testing"

func TestSARateLimiter(t *testing.T) {
	t.Run("burst is allowed then the bucket empties", func(t *testing.T) {
		// Rate 1/s, burst 2: two immediate requests pass, the third does not
		// (no time has elapsed to refill the bucket).
		l := newSARateLimiter(1, 2)
		if !l.allow("sa-1") {
			t.Fatal("the first request within burst must be allowed")
		}
		if !l.allow("sa-1") {
			t.Fatal("the second request within burst must be allowed")
		}
		if l.allow("sa-1") {
			t.Fatal("the third immediate request must be refused")
		}
	})

	t.Run("limits are per service account", func(t *testing.T) {
		l := newSARateLimiter(1, 1)
		if !l.allow("sa-a") {
			t.Fatal("sa-a's first request must pass")
		}
		if l.allow("sa-a") {
			t.Fatal("sa-a's second immediate request must be refused")
		}
		// A different credential has its own bucket and is unaffected.
		if !l.allow("sa-b") {
			t.Fatal("sa-b must not be limited by sa-a's traffic")
		}
	})

	t.Run("rate 0 disables the limiter", func(t *testing.T) {
		l := newSARateLimiter(0, 0)
		for i := 0; i < 1000; i++ {
			if !l.allow("sa-1") {
				t.Fatalf("a disabled limiter must always allow (failed at %d)", i)
			}
		}
	})
}
