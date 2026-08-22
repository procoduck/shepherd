package beacon

import "testing"

func TestNewRateLimiter_PanicsOnZeroLimit(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewRateLimiter(zero Limits) did not panic — a zero rate limit must fail loudly at construction, not silently allow everything")
		}
	}()
	NewRateLimiter(Limits{})
}

// TestRateLimiter_EnforcesBurstThenBlocks is the red-run-provable half of
// the per-instance rate cap.
//
// Red run: change RateLimiter.Allow to `return true` unconditionally. This
// test then fails because far more than Burst requests succeed for the same
// key.
func TestRateLimiter_EnforcesBurstThenBlocks(t *testing.T) {
	rl := NewRateLimiter(Limits{RatePerSecond: 1, Burst: 3})

	allowed := 0
	for i := 0; i < 10; i++ {
		if rl.Allow("token-a") {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("allowed = %d immediate requests, want exactly Burst=3 before throttling kicks in", allowed)
	}
}

func TestRateLimiter_KeysAreIndependent(t *testing.T) {
	rl := NewRateLimiter(Limits{RatePerSecond: 1, Burst: 1})

	if !rl.Allow("token-a") {
		t.Fatal("first request for token-a should be allowed")
	}
	if rl.Allow("token-a") {
		t.Fatal("second immediate request for token-a should be throttled")
	}
	if !rl.Allow("token-b") {
		t.Fatal("token-b has its own bucket and should not be affected by token-a's usage")
	}
}
