package validate

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"shepherd/internal/alloysyntax"
)

// The served-form budget (ServedBudget) must not change what is served:
// for every text, InsertTenantDrops returns what the pre-budget one
// (refInsertTenantDrops, olddrop_ref_test.go) did whenever that fits
// MaxContentBytes, refuses whenever it did, and refuses a changed result
// over the cap.

// dropGen writes random pipeline text in the shapes the drop pass reads:
// relabel components with labelmap rules (and drops already there),
// otelcol.exporter.loki forward_to lists with trailing comments, declares,
// nested literal import.string contents, the name a drop would collide
// with, raw tabs and control bytes, CRLF and a little text that does not
// parse.
type dropGen struct {
	r *rand.Rand
}

func (g *dropGen) pick(xs ...string) string { return xs[g.r.Intn(len(xs))] }

func (g *dropGen) indent() string { return g.pick("", "", "  ", "\t", "    ", " \t ") }

func (g *dropGen) junk() string {
	var sb strings.Builder
	for range g.r.Intn(12) {
		sb.WriteString(g.pick("a", "z", " ", "\t", "\x01", "é", "\u2028", "\u00ad", "😀", "\"", "\\", "$1", "{", "}"))
	}
	return sb.String()
}

func (g *dropGen) rule(ind string) string {
	switch g.r.Intn(9) {
	case 0, 1:
		return ind + "rule {\n" + ind + "  action = \"labelmap\"\n" + ind + "  regex  = \"(.*)\"\n" + ind + "}\n"
	case 2:
		return ind + "rule {\n" + ind + "  action      = \"labelmap\"\n" + ind + "  replacement = " +
			g.pick(`"$1"`, `"x"`, `"__tenant_id__"`, `env("X")`, `"a$2b"`) + "\n" + ind + "}\n"
	case 3:
		return ind + "rule {\n" + ind + "  action = \"labeldrop\"\n" + ind + "  regex  = \"__tenant_id__\"\n" + ind + "}\n"
	case 4:
		return ind + "rule { action = \"labelmap\" }" + g.pick("", " rule { action = \"labelmap\" }") + "\n"
	case 5:
		return ind + "rule {\n" + ind + "  action = " + g.pick(`"replace"`, `"keep"`, `"label" + "map"`) + "\n" + ind + "}\n"
	case 6:
		return ind + "rule {\n" + ind + "  action = \"labelmap\"\n" + ind + "}\n" + ind + "rule {\n" + ind +
			"  action = \"labeldrop\"\n" + ind + "  regex  = \"__tenant_id__\"\n" + ind + "}\n"
	case 7:
		return ind + "// " + g.junk() + "\n"
	default:
		return ind + "rule {\n" + ind + "  action = \"labelmap\"\n" + ind + "  regex = \"" + strings.ReplaceAll(g.junk(), "\"", "") + "\"\n" + ind + "}\n"
	}
}

func (g *dropGen) stmt(depth int, ind string) string {
	label := g.pick("a", "b", "x", "k8s", "é")
	switch g.r.Intn(12) {
	case 0, 1, 2:
		comp := g.pick("prometheus.relabel", "discovery.relabel", "loki.relabel", "pyroscope.relabel", "loki.process")
		var sb strings.Builder
		sb.WriteString(ind + comp + " " + strconv.Quote(label) + " {\n" + ind + "  forward_to = []\n")
		for range 1 + g.r.Intn(4) {
			sb.WriteString(g.rule(ind + g.pick("  ", "\t", "  \t")))
		}
		sb.WriteString(ind + "}" + g.pick("", " // "+g.junk()) + "\n")
		return sb.String()
	case 3:
		return ind + "otelcol.exporter.loki " + strconv.Quote(label) + " {\n" + ind + "  forward_to = " +
			g.pick("[loki.write.a.receiver]", "[loki.write.a.receiver, loki.write.b.receiver]", "[]",
				"argument.f.value", "[argument.x.receiver]") + "\n" + ind + "}" +
			g.pick("", " // otelcol.exporter.loki", " /* c */", " /* c */ x", "\t// "+g.junk()) + "\n"
	case 4:
		if depth > 3 {
			return ""
		}
		var sb strings.Builder
		sb.WriteString(ind + "declare " + strconv.Quote(label) + " {\n")
		for range g.r.Intn(3) {
			sb.WriteString(g.stmt(depth+1, ind+"  "))
		}
		sb.WriteString(ind + "}\n")
		return sb.String()
	case 5, 6:
		if depth > 3 {
			return ""
		}
		inner := g.text(depth + 1)
		var lit string
		switch g.r.Intn(6) {
		case 5: // a raw string, raw CR LF and all
			lit = rawLiteral(inner)
		case 4: // escapes the encoder does not write: re-quoted shorter
			lit = noncanonicalQuote(g.r, inner)
		case 0: // raw tabs and control bytes kept as written
			lit = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(inner) + `"`
		case 1:
			lit = g.pick("local.file.x.content", "`raw`", `"" + "x"`)
		default:
			lit = alloysyntax.QuoteString(inner)
		}
		return ind + "import.string " + strconv.Quote(label) + " {\n" + ind + "  content = " + lit + "\n" + ind + "}\n"
	case 7:
		return ind + "loki.process \"shepherd_tenant_drop_" + label + "\" {\n" + ind + "  forward_to = []\n" + ind + "}\n"
	case 8:
		return ind + "// " + g.junk() + "\n"
	case 9:
		return ind + "x = " + strconv.Quote(g.junk()) + "\n"
	case 10:
		if g.r.Intn(8) == 0 {
			return ind + g.pick("{", "rule {", "\"", "import.string \"q\" {") + "\n"
		}
		return ind + "foreach \"f\" {\n" + ind + "  collection = []\n" + ind + "  var = \"v\"\n" + ind + "  template {\n" +
			g.stmt(depth+1, ind+"    ") + ind + "  }\n" + ind + "}\n"
	default:
		return ""
	}
}

func (g *dropGen) text(depth int) string {
	var sb strings.Builder
	for range g.r.Intn(6) {
		sb.WriteString(g.stmt(depth, g.indent()))
	}
	return lineEndings(g.r, sb.String())
}

// checkDropsMatch fails t unless InsertTenantDrops agrees with the
// reference on src.
func checkDropsMatch(t *testing.T, src string, sameErrors bool) (accepted bool) {
	t.Helper()
	want, wantErr := refInsertTenantDrops(src)
	got, gotErr := InsertTenantDrops(src)
	switch {
	case wantErr != nil:
		if gotErr == nil {
			t.Fatalf("the reference refuses (%v), the budgeted pass serves %d bytes for %q", wantErr, len(got), trunc(src))
		}
		if sameErrors && gotErr.Error() != wantErr.Error() {
			t.Fatalf("refusals differ:\n got  %v\n want %v\nfor %q", gotErr, wantErr, trunc(src))
		}
		return false
	case len(want) > MaxContentBytes && want != src:
		if !errors.Is(gotErr, ErrServedFormTooLarge) {
			t.Fatalf("the reference builds %d bytes, the budgeted pass: %v (%d bytes)", len(want), gotErr, len(got))
		}
		return false
	default:
		if gotErr != nil {
			t.Fatalf("the reference serves %d bytes, the budgeted pass refuses: %v\nfor %q", len(want), gotErr, trunc(src))
		}
		if got != want {
			t.Fatalf("served bytes differ for %q:\n got  %q\n want %q", trunc(src), trunc(got), trunc(want))
		}
		return true
	}
}

func trunc(s string) string {
	if len(s) > 2000 {
		return s[:2000] + "…"
	}
	return s
}

func TestInsertTenantDropsMatchesReference(t *testing.T) {
	g := &dropGen{r: rand.New(rand.NewSource(410))}
	const n = 60000
	accepted, changed := 0, 0
	for i := range n {
		src := g.text(0)
		if checkDropsMatch(t, src, true) {
			accepted++
			if out, err := InsertTenantDrops(src); err == nil && out != src {
				changed++
			}
		}
		if t.Failed() {
			t.Fatalf("case %d", i)
		}
	}
	t.Logf("%d random texts: %d served (%d with drops inserted), %d refused alike", n, accepted, changed, n-accepted)
	if changed < n/10 || accepted < n/4 {
		t.Fatalf("the corpus is too thin: %d served, %d changed", accepted, changed)
	}
}

// Near the cap: a text whose served form is within a few hundred bytes of
// MaxContentBytes either side, the growth at the top level (a comment),
// inside nested import.string contents (raw tabs, re-escaped once per
// level), or in a content literal written with escapes the encoder does not
// use, which the drops make it re-quote shorter. 45 texts by default;
// SERVED_BUDGET_FULL=1 runs 400 (the same seed: the first 45 are the same).
func TestInsertTenantDropsMatchesReferenceNearCap(t *testing.T) {
	g := &dropGen{r: rand.New(rand.NewSource(4100))}
	n := 45
	if os.Getenv("SERVED_BUDGET_FULL") == "1" {
		n = 400
	}
	under, over := 0, 0
	for i := range n {
		var base string
		for {
			base = g.stmt(0, "") + "prometheus.relabel \"r\" {\n  forward_to = []\n" + g.rule("  ") + g.rule("  ") + "}\n"
			if _, err := refInsertTenantDrops(base); err == nil {
				break
			}
		}
		nested := 0
		if i%3 == 1 {
			nested = 1 + g.r.Intn(3)
		}
		shrink := i%3 == 2
		build := func(pad int) string {
			filler := strings.Repeat("a", pad)
			if shrink {
				inner := `loki.relabel \"n\" {\n  forward_to = []\n  rule {\n    action = \"labelmap\"\n  }\n}\n` +
					`x = \"` + strings.Repeat(`\u0061`, pad) + `\"\n`
				return base + "import.string \"s\" {\n  content = \"" + inner + "\"\n}\n"
			}
			if nested == 0 {
				return base + "// " + filler + "\n"
			}
			inner := "loki.relabel \"n\" {\n  forward_to = []\n  rule {\n    action = \"labelmap\"\n  }\n}\n" +
				"x = \"" + strings.Repeat("\t", pad) + "\"\n"
			for range nested {
				inner = "import.string \"m\" {\n  content = \"" + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(inner) + "\"\n}\n"
			}
			return base + inner
		}
		size := func(pad int) int {
			out, err := refInsertTenantDrops(build(pad))
			if err != nil {
				t.Fatalf("near-cap base refused: %v", err)
			}
			return len(out)
		}
		l0, l1 := size(0), size(1000)
		perPad := float64(l1-l0) / 1000
		target := MaxContentBytes + g.r.Intn(601) - 300
		pad := max(int(float64(target-l0)/perPad), 0)
		if checkDropsMatch(t, build(pad), false) {
			under++
		} else {
			over++
		}
	}
	t.Logf("%d near-cap texts: %d served identically, %d refused alike", n, under, over)
	if under < n/5 || over < n/5 {
		t.Fatalf("the near-cap corpus does not straddle the cap: %d under, %d over", under, over)
	}
}

func TestQuotedLenIsTheQuotedLength(t *testing.T) {
	// Every rune, in chunks (the length is a sum over runes).
	var sb strings.Builder
	for r := rune(0); r <= utf8.MaxRune; r++ {
		if utf8.ValidRune(r) {
			sb.WriteRune(r)
		}
		if sb.Len() > 4000 || r == utf8.MaxRune {
			s := sb.String()
			if got, want := QuotedLen(s), len(alloysyntax.QuoteString(s)); got != want {
				t.Fatalf("QuotedLen = %d, quoted is %d, for runes up to %U", got, want, r)
			}
			sb.Reset()
		}
	}
	// Random bytes: invalid UTF-8, lone continuation bytes, surrogates.
	rnd := rand.New(rand.NewSource(7))
	for range 20000 {
		b := make([]byte, rnd.Intn(40))
		for i := range b {
			b[i] = byte(rnd.Intn(256))
		}
		s := string(b)
		if got, want := QuotedLen(s), len(alloysyntax.QuoteString(s)); got != want {
			t.Fatalf("QuotedLen(%q) = %d, quoted is %d", s, got, want)
		}
	}
}

// The shapes that made the drop pass build far more than the cap from a
// text within it. Each must be refused (or served) having allocated, beyond
// what parsing every module level costs (ServedFormProblem parses the same
// texts), at most twice the cap. SERVED_BUDGET_REFERENCE=1 runs the same
// checks on the pre-budget pass, which fails them.
func TestInsertTenantDropsStaysWithinTheBudget(t *testing.T) {
	drops := InsertTenantDrops
	if os.Getenv("SERVED_BUDGET_REFERENCE") == "1" {
		drops = refInsertTenantDrops
	}
	labelmap := "prometheus.relabel \"r\" {\n  forward_to = []\n  rule {\n    action = \"labelmap\"\n  }\n}\n"
	for _, tc := range []struct {
		name  string
		src   string
		serve bool
	}{
		{
			// 8 nested import.string, a labelmap innermost beside ~1 MiB of
			// raw tabs and 0x01 bytes: each level re-escapes the level
			// inside it, doubling its backslashes.
			name: "nested quoting",
			src:  nestRaw(labelmap+"x = \""+strings.Repeat("\t\x01", (MaxContentBytes-8000)/2)+"\"\n", 8),
		},
		{
			// ~1 MiB of labelmaps, each needing a drop of ~70 bytes.
			name: "many drops",
			src:  strings.Repeat("loki.relabel \"r\" {\n forward_to = []\n rule { action = \"labelmap\" }\n}\n", MaxContentBytes/70),
		},
		{
			// Rules indented ~25 KiB: each drop repeats the indent on its
			// four lines.
			name: "indented drops",
			src: "loki.relabel \"r\" {\n forward_to = []\n" +
				strings.Repeat(strings.Repeat(" ", 25<<10)+"rule { action = \"labelmap\" }\n", 40) + "}\n",
		},
		{
			// Within the cap: many drops served, built once.
			name:  "many drops served",
			src:   strings.Repeat("loki.relabel \"r\" {\n forward_to = []\n rule { action = \"labelmap\" }\n}\n", 6000),
			serve: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.src) > MaxContentBytes {
				t.Fatalf("the input is %d bytes, over the cap", len(tc.src))
			}
			var out string
			var err error
			start := time.Now()
			build := allocated(func() { out, err = drops(tc.src) })
			elapsed := time.Since(start)
			// The pass parses every level of its input, and parses what it
			// built once more: a result within the cap when served, at most
			// the input's size when refused (it stops at the first level
			// that would pass the cap).
			parse := allocated(func() { errSink = ServedFormProblem(tc.src) })
			if err == nil && out != tc.src {
				parse += allocated(func() { errSink = ServedFormProblem(out) })
			} else {
				parse *= 2
			}
			beyond := int64(build) - int64(parse)
			t.Logf("%s: the pass allocated %.1f MiB, parsing %.1f MiB of it: %.1f MiB beyond, in %v; served %d bytes, err %v",
				tc.name, mib(build), mib(parse), float64(beyond)/(1<<20), elapsed.Round(time.Millisecond), len(out), err)
			if tc.serve {
				if err != nil || len(out) > MaxContentBytes {
					t.Fatalf("not served: %d bytes, %v", len(out), err)
				}
			} else if err == nil && len(out) <= MaxContentBytes {
				t.Fatalf("served %d bytes, want a refusal", len(out))
			}
			if beyond > 2*MaxContentBytes {
				t.Fatalf("allocated %.1f MiB beyond parsing, more than twice the cap", float64(beyond)/(1<<20))
			}
			if elapsed > 5*time.Second {
				t.Fatalf("took %v", elapsed)
			}
			if err == nil && len(out) > MaxContentBytes {
				t.Fatalf("built a %d-byte served form", len(out))
			}
		})
	}
}

// nestRaw wraps text in n import.string levels, each content written with
// its tabs and control bytes raw (only quotes, backslashes and line ends
// escaped).
func nestRaw(text string, n int) string {
	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	for range n {
		text = "import.string \"m\" {\n  content = \"" + esc.Replace(text) + "\"\n}\n"
	}
	return text
}

func mib(n uint64) float64 { return float64(n) / (1 << 20) }

// allocated is how many bytes fn allocates.
func allocated(fn func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// errSink keeps a result only measured for its allocations.
var errSink error

// noncanonicalQuote writes s as a string literal with escapes chosen at
// random — \xHH, octal, \uXXXX, \UXXXXXXXX or the encoder's own — so the
// literal decodes to s but is not what alloysyntax.QuoteString writes.
func noncanonicalQuote(r *rand.Rand, s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for i := 0; i < len(s); {
		c, w := utf8.DecodeRuneInString(s[i:])
		canonical := strconv.Quote(s[i : i+w])
		canonical = canonical[1 : len(canonical)-1]
		switch k := r.Intn(6); {
		case c == utf8.RuneError && w == 1:
			fmt.Fprintf(&sb, `\x%02x`, s[i])
		case k == 0 && w == 1:
			fmt.Fprintf(&sb, `\x%02X`, s[i])
		case k == 1 && w == 1:
			fmt.Fprintf(&sb, `\%03o`, s[i])
		case k == 2 && c < 0x10000:
			fmt.Fprintf(&sb, `\u%04x`, c)
		case k == 3:
			fmt.Fprintf(&sb, `\U%08x`, c)
		default:
			sb.WriteString(canonical)
		}
		i += w
	}
	sb.WriteByte('"')
	return sb.String()
}

func TestNoncanonicalQuoteDecodesBack(t *testing.T) {
	rnd := rand.New(rand.NewSource(11))
	for range 5000 {
		b := make([]byte, rnd.Intn(30))
		for i := range b {
			b[i] = byte(rnd.Intn(256))
		}
		s := string(b) + "é😀\t\"\\"
		lit := noncanonicalQuote(rnd, s)
		if got, err := strconv.Unquote(lit); err != nil || got != s {
			t.Fatalf("%s decodes to %q (%v), want %q", lit, got, err, s)
		}
	}
}

// lineEndings is s with its line endings as written: LF (most), all CR LF,
// or a mix.
func lineEndings(r *rand.Rand, s string) string {
	switch r.Intn(6) {
	case 0:
		return strings.ReplaceAll(s, "\n", "\r\n")
	case 1:
		var sb strings.Builder
		for _, line := range strings.SplitAfter(s, "\n") {
			if strings.HasSuffix(line, "\n") && r.Intn(2) == 0 {
				line = strings.TrimSuffix(line, "\n") + "\r\n"
			}
			sb.WriteString(line)
		}
		return sb.String()
	default:
		return s
	}
}

// rawLiteral writes s as a raw (backtick) string, its CR LF kept raw, or
// quoted when it holds a backtick.
func rawLiteral(s string) string {
	if strings.Contains(s, "`") {
		return alloysyntax.QuoteString(s)
	}
	return "`" + s + "`"
}
