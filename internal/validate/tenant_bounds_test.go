package validate_test

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"shepherd/internal/validate"
)

// pathological is a pipeline whose tenant multiplies by fan at each of
// levels declare hops — `t = argument.t.value + … ×fan` — the
// shape that built a 200 MB string from 4 KB of text before the lint had
// caps (third review of #388).
func pathological(levels, fan int) string {
	var b strings.Builder
	b.WriteString(`declare "l0" {
  argument "t" { }
  prometheus.remote_write "o" {
    endpoint {
      url     = "https://m.example/push"
      headers = { "X-Scope-OrgID" = argument.t.value }
    }
  }
}
`)
	sum := strings.TrimSuffix(strings.Repeat("argument.t.value + ", fan), " + ")
	for i := 1; i < levels; i++ {
		fmt.Fprintf(&b, "declare \"l%d\" {\n  argument \"t\" { }\n  l%d \"x\" { t = %s }\n}\n", i, i-1, sum)
	}
	fmt.Fprintf(&b, "l%d \"top\" { t = \"acme\" }\n", levels-1)
	return b.String()
}

// boundedRun runs f and fails when it takes longer than d or allocates more
// than maxBytes.
func boundedRun(t *testing.T, d time.Duration, maxBytes uint64, f func()) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	f()
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	if took > d {
		t.Errorf("took %s; the bound is %s", took, d)
	}
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > maxBytes {
		t.Errorf("allocated %d bytes; the bound is %d", alloc, maxBytes)
	}
}

func strictAcme() validate.TenantPolicy {
	return validate.TenantPolicy{Strict: true, Owns: func(t string) bool { return t == "acme" }}
}

// TestTenantLintIsBounded pins the lint's caps: a value that folds past them
// is unreadable — refused in strict mode, "incomplete" for TenantLiterals —
// in bounded time and memory, never built.
func TestTenantLintIsBounded(t *testing.T) {
	for _, c := range []struct{ levels, fan int }{{5, 40}, {7, 40}, {8, 200}} {
		src := pathological(c.levels, c.fan)
		t.Run(fmt.Sprintf("%d levels x%d (%d bytes)", c.levels, c.fan, len(src)), func(t *testing.T) {
			var diags []validate.Diagnostic
			boundedRun(t, 2*time.Second, 64<<20, func() { diags = validate.TenantLint(src, strictAcme()) })
			if len(diags) == 0 {
				t.Errorf("a tenant past the caps was not refused")
			}
			var complete bool
			boundedRun(t, 2*time.Second, 64<<20, func() { _, complete = validate.TenantLiterals(src) })
			if complete {
				t.Errorf("TenantLiterals reported a tenant past the caps as complete")
			}
		})
	}
}

func BenchmarkTenantLintPathological(b *testing.B) {
	src := pathological(5, 40)
	for b.Loop() {
		validate.TenantLint(src, strictAcme())
	}
}

// FuzzTenantLint: any text, however nested, finishes within the bounds and
// never panics.
func FuzzTenantLint(f *testing.F) {
	f.Add(pathological(3, 3))
	f.Add(`loki.source.api "a" { forward_to = [loki.write.w.receiver] }` + "\n" + `loki.write "w" { endpoint { url = "x" } }`)
	f.Add(`import.string "m" { content = "declare \"d\" { }" }`)
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 8<<10 {
			return
		}
		boundedRun(t, 2*time.Second, 256<<20, func() {
			validate.TenantLint(src, strictAcme())
			validate.TenantLiterals(src)
		})
	})
}
