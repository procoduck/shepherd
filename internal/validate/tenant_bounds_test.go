package validate_test

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"strings"
	"testing"
	"time"

	"shepherd/internal/validate"
)

// The tenant lint runs on every pipeline write of every org (the pipeline
// tenant index) and at every boot, so no text may make it slow or large.
// These are the shapes reviews of #388 found, each at its reported size and
// ten times it, and a generator of large structured inputs; every one must
// finish within lintTimeBound and lintAllocBound.
const (
	lintTimeBound  = 250 * time.Millisecond // the lint's own guard is far lower; this is CI slack
	lintAllocBound = 32 << 20
)

// pathological multiplies the tenant by fan at each of levels declare hops:
// `t = argument.t.value + … ×fan` (third review).
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

// manyPieces is one declare with headers headers that each send
// argument.t.value, instantiated insts times with a value of pieces
// "|"-joined tenants (fourth review: 2000 × 64 × 500 ≈ 277 KB built 64M
// diagnostics).
func manyPieces(headers, insts, pieces int) string {
	var b strings.Builder
	b.WriteString("declare \"w\" {\n  argument \"t\" { }\n")
	for i := range headers {
		fmt.Fprintf(&b, "  prometheus.remote_write \"o%d\" {\n    endpoint {\n      url = \"x\"\n      headers = { \"X-Scope-OrgID\" = argument.t.value }\n    }\n  }\n", i)
	}
	b.WriteString("}\n")
	val := strings.TrimSuffix(strings.Repeat("a|", pieces), "|")
	for i := range insts {
		fmt.Fprintf(&b, "w \"i%d\" { t = %q }\n", i, val)
	}
	return b.String()
}

// deepMatch nests depth stage.match blocks (fourth review: 40 000 levels,
// 625 KB, was quadratic in the path).
func deepMatch(depth int) string {
	var b strings.Builder
	b.WriteString("loki.process \"p\" {\n  forward_to = []\n")
	for range depth {
		b.WriteString("stage.match {\n")
	}
	b.WriteString("stage.tenant { value = \"acme\" }\n")
	for range depth {
		b.WriteString("}\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// structured builds a large random pipeline from the shapes above and more:
// nested declares passing arguments, stage.match nesting, |-strings, labels
// maps, relabel rules, push sources and import.string modules.
func structured(r *rand.Rand, size int) string {
	var b strings.Builder
	for b.Len() < size {
		switch r.IntN(7) {
		case 0:
			b.WriteString(pathological(2+r.IntN(6), 1+r.IntN(60)))
		case 1:
			b.WriteString(manyPieces(1+r.IntN(200), 1+r.IntN(30), 1+r.IntN(300)))
		case 2:
			b.WriteString(deepMatch(1 + r.IntN(2000)))
		case 3:
			fmt.Fprintf(&b, "loki.process \"s%d\" {\n  forward_to = []\n  stage.static_labels {\n    values = { \"__tenant_id__\" = %q }\n  }\n}\n",
				r.IntN(1e6), strings.Repeat("t|", 1+r.IntN(2000)))
		case 4:
			fmt.Fprintf(&b, "loki.relabel \"r%d\" {\n  forward_to = []\n  rule {\n    target_label = \"__tenant\" + \"_id__\"\n    replacement = %q\n  }\n}\n",
				r.IntN(1e6), strings.Repeat("x", r.IntN(9000)))
		case 5:
			n := r.IntN(1e6)
			fmt.Fprintf(&b, "loki.source.api \"a%d\" {\n  http { }\n  forward_to = [loki.process.p%d.receiver]\n}\nloki.process \"p%d\" {\n  forward_to = [loki.process.p%d.receiver, loki.write.w%d.receiver]\n}\nloki.write \"w%d\" {\n  endpoint { url = \"x\" }\n}\n", n, n, n, n, n, n)
		case 6:
			fmt.Fprintf(&b, "import.string \"m%d\" {\n  content = %q\n}\n", r.IntN(1e6), manyPieces(1+r.IntN(20), 1+r.IntN(5), 1+r.IntN(50)))
		}
	}
	return b.String()
}

// measure runs f once and returns its wall time and bytes allocated.
func measure(f func()) (time.Duration, uint64) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	f()
	took := time.Since(start)
	runtime.ReadMemStats(&after)
	return took, after.TotalAlloc - before.TotalAlloc
}

func assertBounded(t *testing.T, name, src string) {
	t.Helper()
	took, alloc := measure(func() { validate.TenantLint(src, strictAcme()) })
	took2, alloc2 := measure(func() { validate.TenantLiterals(src) })
	t.Logf("%s (%d bytes): TenantLint %s / %d B, TenantLiterals %s / %d B", name, len(src), took, alloc, took2, alloc2)
	for _, m := range []struct {
		took  time.Duration
		alloc uint64
	}{{took, alloc}, {took2, alloc2}} {
		if m.took > lintTimeBound {
			t.Errorf("%s took %s; the bound is %s", name, m.took, lintTimeBound)
		}
		if m.alloc > lintAllocBound+uint64(4*len(src)) {
			t.Errorf("%s allocated %d bytes; the bound is %d plus 4x the text", name, m.alloc, lintAllocBound)
		}
	}
}

func strictAcme() validate.TenantPolicy {
	return validate.TenantPolicy{Strict: true, Owns: func(t string) bool { return t == "acme" }}
}

// TestTenantLintIsBounded pins the lint's single budget over every shape a
// review found, at its size and ten times it: time and allocation stay
// bounded, and what it cannot finish is refused (strict) or incomplete.
func TestTenantLintIsBounded(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"declare levels 5x40", pathological(5, 40)},
		{"declare levels 7x40", pathological(7, 40)},
		{"declare levels 8x200", pathological(8, 200)},
		{"| pieces 2000x64x500", manyPieces(2000, 64, 500)},
		{"| pieces 10x (20000x64x500, past the contents cap)", manyPieces(20000, 64, 500)},
		{"stage.match 40000 deep", deepMatch(40_000)},
		{"stage.match 400000 deep (10x, past the contents cap)", deepMatch(400_000)},
		{"stage.match 65000 deep (just under the contents cap)", deepMatch(65_000)},
		{"| pieces 6000x64x500 (just under the contents cap)", manyPieces(6000, 64, 500)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertBounded(t, c.name, c.src)
			if diags := validate.TenantLint(c.src, strictAcme()); len(diags) == 0 || len(diags) > 101 {
				t.Errorf("want between 1 and 101 refusals, got %d", len(diags))
			}
			if lits, complete := validate.TenantLiterals(c.src); complete || len(lits) > 256 {
				t.Errorf("want an incomplete result of at most 256 tenants, got complete=%v with %d", complete, len(lits))
			}
		})
	}
}

// TestTenantLintBoundedOnLargeStructuredInput is the property: random large
// structured pipelines (up to 1 MiB, the pipeline contents cap) stay bounded.
func TestTenantLintBoundedOnLargeStructuredInput(t *testing.T) {
	r := rand.New(rand.NewPCG(388, 4))
	for i := range 12 {
		src := structured(r, 64<<10*(1+i%16))
		assertBounded(t, fmt.Sprintf("structured #%d", i), src)
	}
}

func BenchmarkTenantLintPathological(b *testing.B) {
	for _, c := range []struct {
		name string
		src  string
	}{
		{"declare-levels-5x40", pathological(5, 40)},
		{"pieces-2000x64x500", manyPieces(2000, 64, 500)},
		{"pieces-10x", manyPieces(20000, 64, 500)},
		{"match-40000", deepMatch(40_000)},
		{"match-400000", deepMatch(400_000)},
	} {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				validate.TenantLint(c.src, strictAcme())
			}
		})
	}
}

// FuzzTenantLint drives the structured generator with fuzzed seeds and
// sizes up to 1 MiB, and fuzzes raw text too: nothing exceeds the bounds or
// panics.
func FuzzTenantLint(f *testing.F) {
	f.Add(uint64(1), uint32(64<<10), "")
	f.Add(uint64(2), uint32(1<<20), `loki.source.api "a" { forward_to = [loki.write.w.receiver] }`)
	f.Add(uint64(3), uint32(0), `import.string "m" { content = "declare \"d\" { }" }`)
	f.Fuzz(func(t *testing.T, seed uint64, size uint32, raw string) {
		src := raw
		if n := int(size % (1 << 20)); n > 0 {
			src += "\n" + structured(rand.New(rand.NewPCG(seed, 1)), n)
		}
		assertBounded(t, "fuzz", src)
	})
}
