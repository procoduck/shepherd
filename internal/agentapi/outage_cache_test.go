package agentapi_test

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"shepherd/internal/agentapi"
)

// The outage caches (#374) are bounded in entries, bytes and age, and the
// verdict cache compares a presented secret against the stored hash.
func TestTokenVerdictsBounds(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return now }
	hashOf := func(secret string) []byte { h := sha256.Sum256([]byte(secret)); return h[:] }

	t.Run("disabled when ttl is zero", func(t *testing.T) {
		if c := agentapi.NewTokenVerdicts(0, 10); c != nil {
			t.Fatal("NewTokenVerdicts(0, …) should be nil (disabled)")
		}
		var c *agentapi.TokenVerdicts
		agentapi.RememberTokenVerdict(c, "t", hashOf("s"))
		if agentapi.VerifyTokenVerdict(c, "t", "s") || c.Len() != 0 {
			t.Fatal("a nil cache must verify nothing")
		}
	})

	t.Run("verifies only the matching secret", func(t *testing.T) {
		c := agentapi.NewTokenVerdicts(time.Minute, 10)
		agentapi.SetTokenVerdictClock(c, clock)
		agentapi.RememberTokenVerdict(c, "tok", hashOf("right"))
		if !agentapi.VerifyTokenVerdict(c, "tok", "right") {
			t.Fatal("the remembered secret should verify")
		}
		if agentapi.VerifyTokenVerdict(c, "tok", "wrong") {
			t.Fatal("a different secret must not verify")
		}
		if agentapi.VerifyTokenVerdict(c, "other", "right") {
			t.Fatal("an unknown token id must not verify")
		}
	})

	t.Run("expires after the ttl", func(t *testing.T) {
		c := agentapi.NewTokenVerdicts(time.Minute, 10)
		local := now
		agentapi.SetTokenVerdictClock(c, func() time.Time { return local })
		agentapi.RememberTokenVerdict(c, "tok", hashOf("s"))
		local = local.Add(59 * time.Second)
		if !agentapi.VerifyTokenVerdict(c, "tok", "s") {
			t.Fatal("inside the ttl the verdict should hold")
		}
		local = local.Add(time.Second)
		if agentapi.VerifyTokenVerdict(c, "tok", "s") {
			t.Fatal("at the ttl the verdict must be gone")
		}
	})

	t.Run("evict forgets the verdict", func(t *testing.T) {
		c := agentapi.NewTokenVerdicts(time.Minute, 10)
		agentapi.RememberTokenVerdict(c, "tok", hashOf("s"))
		c.Evict("tok")
		if agentapi.VerifyTokenVerdict(c, "tok", "s") || c.Len() != 0 {
			t.Fatal("an evicted verdict must not verify")
		}
	})

	t.Run("holds at most max entries, dropping the least recently verified", func(t *testing.T) {
		c := agentapi.NewTokenVerdicts(time.Hour, 3)
		local := now
		agentapi.SetTokenVerdictClock(c, func() time.Time { return local })
		for i := range 5 {
			local = local.Add(time.Second)
			agentapi.RememberTokenVerdict(c, fmt.Sprintf("tok-%d", i), hashOf("s"))
		}
		if c.Len() != 3 {
			t.Fatalf("Len = %d, want 3", c.Len())
		}
		if agentapi.VerifyTokenVerdict(c, "tok-0", "s") || agentapi.VerifyTokenVerdict(c, "tok-1", "s") {
			t.Fatal("the oldest verdicts should have been evicted")
		}
		if !agentapi.VerifyTokenVerdict(c, "tok-4", "s") {
			t.Fatal("the newest verdict should be kept")
		}
	})
}

func TestLastServedBounds(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	t.Run("caps entries, oldest first", func(t *testing.T) {
		local := now
		c := agentapi.NewLastServedCache(time.Hour, 2, 1<<20, func() time.Time { return local })
		for _, k := range []string{"a", "b", "c"} {
			local = local.Add(time.Second)
			c.Put(k, "content-"+k, "hash-"+k)
		}
		if n, _ := c.Stats(); n != 2 {
			t.Fatalf("entries = %d, want 2", n)
		}
		if _, _, ok := c.Get("a"); ok {
			t.Fatal("the oldest entry should have been evicted")
		}
		if content, hash, ok := c.Get("c"); !ok || content != "content-c" || hash != "hash-c" {
			t.Fatalf("Get(c) = %q %q %v", content, hash, ok)
		}
	})

	t.Run("caps total bytes and skips an entry larger than the cap", func(t *testing.T) {
		local := now
		c := agentapi.NewLastServedCache(time.Hour, 100, 100, func() time.Time { return local })
		c.Put("big", strings.Repeat("x", 200), "h")
		if n, b := c.Stats(); n != 0 || b != 0 {
			t.Fatalf("an over-cap entry was stored: %d entries, %d bytes", n, b)
		}
		for i := range 5 {
			local = local.Add(time.Second)
			c.Put(fmt.Sprintf("k%d", i), strings.Repeat("y", 30), "h")
		}
		if _, b := c.Stats(); b > 100 {
			t.Fatalf("bytes = %d, want <= 100", b)
		}
		if _, _, ok := c.Get("k4"); !ok {
			t.Fatal("the newest entry should be kept")
		}
		if _, _, ok := c.Get("k0"); ok {
			t.Fatal("the oldest entry should have been evicted to make room")
		}
	})

	t.Run("replacing a key does not double count", func(t *testing.T) {
		c := agentapi.NewLastServedCache(time.Hour, 10, 1000, nil)
		c.Put("k", strings.Repeat("a", 50), "h1")
		c.Put("k", strings.Repeat("b", 10), "h2")
		if n, b := c.Stats(); n != 1 || b != len("k")+10+len("h2") {
			t.Fatalf("stats = %d entries, %d bytes", n, b)
		}
	})

	t.Run("expires after the ttl", func(t *testing.T) {
		local := now
		c := agentapi.NewLastServedCache(time.Minute, 10, 1000, func() time.Time { return local })
		c.Put("k", "c", "h")
		local = local.Add(time.Minute)
		if _, _, ok := c.Get("k"); ok {
			t.Fatal("an entry at its ttl must not be served")
		}
	})
}
