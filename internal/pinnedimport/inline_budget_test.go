package pinnedimport

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"shepherd/internal/alloysyntax"
	"shepherd/internal/store/sqlc"
	"shepherd/internal/validate"
)

// The served-form budget must not change what a strict org is served:
// Inline returns what the pre-budget one (refInline, oldinline_ref_test.go)
// did whenever that fits, refuses whenever it did, and References lists the
// same modules in the same order.

const (
	budgetRepo = "https://git.example.com/team/modules.git"
	budgetSHA  = "0123456789abcdef0123456789abcdef01234567"
)

// fakeModules is a module cache: path -> content, under budgetRepo at
// budgetSHA.
type fakeModules map[string]string

func (f fakeModules) GetPinnedImportModule(_ context.Context, a sqlc.GetPinnedImportModuleParams) (sqlc.PinnedImportModule, error) {
	key, err := NormalizeRepository(budgetRepo)
	if err != nil {
		return sqlc.PinnedImportModule{}, err
	}
	c, ok := f[a.Path]
	if !ok || a.RepositoryKey != key || a.Revision != budgetSHA {
		return sqlc.PinnedImportModule{}, pgx.ErrNoRows
	}
	return sqlc.PinnedImportModule{Content: c}, nil
}

func budgetRepos(t *testing.T) []sqlc.OrgImportRepository {
	t.Helper()
	key, err := NormalizeRepository(budgetRepo)
	if err != nil {
		t.Fatal(err)
	}
	return []sqlc.OrgImportRepository{{RepositoryKey: key}}
}

func importGit(ind, label, path string) string {
	return ind + "import.git " + strconv.Quote(label) + " {\n" + ind + "  repository = \"" + budgetRepo + "\"\n" + ind +
		"  revision   = \"" + budgetSHA + "\"\n" + ind + "  path       = " + strconv.Quote(path) + "\n" + ind + "}\n"
}

// inlineGen writes random pipelines and modules: pinned import.git blocks
// (cached, not cached, refused for their revision, repository or a
// computed path), import.string contents holding them, declares, labelmaps
// and exporters the drop pass rewrites, odd columns and bytes.
type inlineGen struct {
	r *rand.Rand
}

func (g *inlineGen) pick(xs ...string) string { return xs[g.r.Intn(len(xs))] }

func (g *inlineGen) stmt(depth, imports int, ind string) string {
	label := g.pick("a", "b", "m", "é", "with space")
	switch g.r.Intn(11) {
	case 0, 1, 2:
		if imports <= 0 {
			return ""
		}
		path := g.pick("m0.alloy", "m1.alloy", "m2.alloy", "m3.alloy", "dir/m4.alloy", "missing.alloy")
		switch g.r.Intn(12) {
		case 0:
			return strings.Replace(importGit(ind, label, path), budgetSHA, "main", 1)
		case 1:
			return strings.Replace(importGit(ind, label, path), budgetRepo, "https://elsewhere.example.com/x.git", 1)
		case 2:
			return strings.Replace(importGit(ind, label, path), strconv.Quote(path), `"m" + "0.alloy"`, 1)
		case 3: // not at column 1
			return ind + "/* " + g.pick("x", "\t", "é") + " */ " + importGit("", label, path)
		case 4:
			return strings.Replace(importGit(ind, label, path), ind+"}\n",
				ind+"  basic_auth {\n"+ind+"    username = \"u\"\n"+ind+"  }\n"+ind+"}\n", 1)
		default:
			return importGit(ind, label, path)
		}
	case 3:
		if depth > 2 {
			return ""
		}
		inner := g.text(depth+1, imports-1)
		return ind + "import.string " + strconv.Quote(label) + " {\n" + ind + "  content = " +
			g.pick(alloysyntax.QuoteString(inner), noncanonicalQuote(g.r, inner), noncanonicalQuote(g.r, inner), rawLiteral(inner),
				"local.file.f.content") + "\n" + ind + "}\n"
	case 4:
		if depth > 2 {
			return ""
		}
		return ind + "declare " + strconv.Quote(label) + " {\n" + g.stmt(depth+1, imports, ind+"  ") + ind + "}\n"
	case 5:
		return ind + "prometheus.relabel " + strconv.Quote(label) + " {\n" + ind + "  forward_to = []\n" + ind +
			"  rule {\n" + ind + "    action = \"labelmap\"\n" + ind + "  }\n" + ind + "}\n"
	case 6:
		return ind + "otelcol.exporter.loki " + strconv.Quote(label) + " {\n" + ind +
			"  forward_to = [loki.write.a.receiver]\n" + ind + "}" + g.pick("", " // c") + "\n"
	case 7:
		return ind + "// " + g.pick("\t\x01", "é", "import.git \"x\" {}", "") + "\n"
	case 8:
		return ind + "x = " + strconv.Quote(g.pick("\t", "\x01", "a\"b", "😀")) + "\n"
	case 9:
		if g.r.Intn(10) == 0 {
			return ind + g.pick("{", "import.git {", "\"") + "\n"
		}
		return ""
	default:
		return ""
	}
}

func (g *inlineGen) text(depth, imports int) string {
	var sb strings.Builder
	for range g.r.Intn(5) {
		sb.WriteString(g.stmt(depth, imports, g.pick("", "", "  ", "\t")))
	}
	return lineEndings(g.r, sb.String())
}

func (g *inlineGen) modules() fakeModules {
	m := fakeModules{}
	for _, p := range []string{"m0.alloy", "m1.alloy", "m2.alloy", "m3.alloy", "dir/m4.alloy"} {
		if g.r.Intn(8) != 0 {
			m[p] = g.text(1, 2)
		}
	}
	return m
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// checkInlineMatches fails t unless Inline and References agree with the
// reference on contents.
func checkInlineMatches(t *testing.T, mods fakeModules, repos []sqlc.OrgImportRepository, contents string, sameErrors, withRefs bool) bool {
	t.Helper()
	ctx := context.Background()
	org := pgtype.UUID{}
	want, wantErr := refInline(ctx, mods, org, repos, contents)
	got, gotErr := inline(ctx, mods, org, repos, contents)
	if withRefs {
		wantRefs, wantRefsErr := refReferences(ctx, mods, org, repos, contents)
		gotRefs, gotRefsErr := references(ctx, mods, org, repos, contents)
		if !reflect.DeepEqual(gotRefs, wantRefs) || errText(gotRefsErr) != errText(wantRefsErr) {
			t.Fatalf("References differ:\n got  %v %v\n want %v %v\nfor %q", gotRefs, gotRefsErr, wantRefs, wantRefsErr, contents)
		}
	}
	if wantErr != nil {
		if gotErr == nil {
			t.Fatalf("the reference refuses (%v), the budgeted Inline serves %d bytes for %q", wantErr, len(got), contents)
		}
		if sameErrors && gotErr.Error() != wantErr.Error() {
			t.Fatalf("refusals differ:\n got  %v\n want %v\nfor %q", gotErr, wantErr, contents)
		}
		return false
	}
	if gotErr != nil {
		t.Fatalf("the reference serves %d bytes, the budgeted Inline refuses: %v\nfor %q", len(want), gotErr, contents)
	}
	if got != want {
		t.Fatalf("served bytes differ for %q:\n got  %q\n want %q", contents, got, want)
	}
	return true
}

func TestInlineMatchesReference(t *testing.T) {
	g := &inlineGen{r: rand.New(rand.NewSource(390))}
	repos := budgetRepos(t)
	const n = 60000
	var mods fakeModules
	served, inlined := 0, 0
	for i := range n {
		if i%50 == 0 {
			mods = g.modules()
		}
		contents := g.text(0, 3)
		if checkInlineMatches(t, mods, repos, contents, true, true) {
			served++
			if out, err := refInline(context.Background(), mods, pgtype.UUID{}, repos, contents); err == nil && out != contents {
				inlined++
			}
		}
	}
	t.Logf("%d random pipelines: %d served (%d changed as served), %d refused alike", n, served, inlined, n-served)
	if served < n/4 || inlined < n/10 {
		t.Fatalf("the corpus is too thin: %d served, %d changed", served, inlined)
	}
}

// Near the cap: a module imported one to four times (directly, through an
// import.string, or from another module) whose served form is within a few
// hundred bytes of the cap either side; or, beside it, an import.string
// whose content is written with a for "a" and holds a labelmap, so
// the drops make it re-quote shorter: the text inlined before the drops are
// inserted passes the cap while the served form may fit. 36 pipelines by
// default; SERVED_BUDGET_FULL=1 runs 400 (the same seed: the first 36 are
// the same).
func TestInlineMatchesReferenceNearCap(t *testing.T) {
	g := &inlineGen{r: rand.New(rand.NewSource(3900))}
	repos := budgetRepos(t)
	n := 36
	if os.Getenv("SERVED_BUDGET_FULL") == "1" {
		n = 400
	}
	under, over, shrunk := 0, 0, 0
	// The served size is linear in the padding; its two coefficients
	// depend only on the shape.
	type shape struct {
		copies, mix, kind int
	}
	linear := map[shape][2]float64{}
	for i := range n {
		copies := 1 + g.r.Intn(4)
		kind := i % 3 // 0: "a" in the module, 1: tabs in it, 2: a literal that shrinks
		build := func(pad int) (fakeModules, string) {
			filler := strings.Repeat("a", pad)
			switch kind {
			case 1:
				filler = strings.Repeat("\t", pad)
			case 2:
				filler = strings.Repeat("a", 600000/copies)
			}
			mods := fakeModules{
				"m0.alloy": "prometheus.relabel \"r\" {\n  forward_to = []\n  rule {\n    action = \"labelmap\"\n  }\n}\n" +
					"x = \"" + filler + "\"\n",
				"m1.alloy": importGit("", "inner", "m0.alloy"),
			}
			var sb strings.Builder
			for c := range copies {
				label := "m" + strconv.Itoa(c)
				switch (i + c) % 3 {
				case 0:
					sb.WriteString(importGit("", label, "m0.alloy"))
				case 1:
					sb.WriteString(importGit("  ", label, "m1.alloy"))
				default:
					sb.WriteString("import.string " + strconv.Quote(label) + " {\n  content = " +
						alloysyntax.QuoteString(importGit("", "x", "m0.alloy")) + "\n}\n")
				}
			}
			if kind == 2 {
				sb.WriteString(shrinkingImport(pad))
			}
			return mods, sb.String()
		}
		size := func(pad int) int {
			mods, contents := build(pad)
			out, err := refInline(context.Background(), mods, pgtype.UUID{}, repos, contents)
			if err != nil {
				t.Fatalf("near-cap base refused: %v", err)
			}
			return len(out)
		}
		sh := shape{copies: copies, mix: i % 3, kind: kind}
		if _, ok := linear[sh]; !ok {
			l0, l1 := size(0), size(1000)
			linear[sh] = [2]float64{float64(l0), float64(l1-l0) / 1000}
		}
		l0, perPad := linear[sh][0], linear[sh][1]
		target := validate.MaxContentBytes + g.r.Intn(601) - 300
		pad := max(int((float64(target)-l0)/perPad), 0)
		mods, contents := build(pad)
		served := checkInlineMatches(t, mods, repos, contents, false, false)
		if served && kind == 2 && inlinedLen(t, mods, repos, contents) > validate.MaxContentBytes {
			shrunk++
		}
		if served {
			under++
		} else {
			over++
		}
	}
	t.Logf("%d near-cap pipelines: %d served identically (%d inlined past the cap before the drops shrank them), "+
		"%d refused alike", n, under, shrunk, over)
	if under < n/5 || over < n/5 || shrunk == 0 {
		t.Fatalf("the near-cap corpus does not straddle the cap: %d under (%d shrunk), %d over", under, shrunk, over)
	}
}

// shrinkingImport is an import.string holding a labelmap and a pad-long
// string of "a", each written a: the drops make the content re-quote
// five bytes shorter per "a".
func shrinkingImport(pad int) string {
	return "import.string \"s\" {\n  content = \"" +
		`prometheus.relabel \"r\" {\n  forward_to = []\n  rule {\n    action = \"labelmap\"\n  }\n}\n` +
		`y = \"` + strings.Repeat(`\u0061`, pad) + `\"\n` + "\"\n}\n"
}

// inlinedLen is how long the text the old Inline built before inserting
// the drops was.
func inlinedLen(t *testing.T, mods fakeModules, repos []sqlc.OrgImportRepository, contents string) int {
	t.Helper()
	out, err := newRefWalker(context.Background(), mods, pgtype.UUID{}, repos).inline(contents, 0, 0)
	if err != nil {
		t.Fatalf("inlining: %v", err)
	}
	return len(out)
}

// A pipeline the drops shrink back under the cap is served as before
// (#450 review): the old Inline inlined it past the cap (the module plus
// the import.string as written) and inserted the drops, which re-quote the
// content's a escapes as "a"; the served form fits.
func TestInlineServesWhatTheDropsShrinkUnderTheCap(t *testing.T) {
	repos := budgetRepos(t)
	mods := fakeModules{"m.alloy": "x = \"" + strings.Repeat("a", 600_000) + "\"\n"}
	contents := importGit("", "a", "m.alloy") + shrinkingImport(90_000)
	if inlinedLen(t, mods, repos, contents) <= validate.MaxContentBytes {
		t.Fatal("the inlined text fits: not the shape under test")
	}
	if !checkInlineMatches(t, mods, repos, contents, true, true) {
		t.Fatal("the old Inline refuses it: not the shape under test")
	}
}

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

// The import-instance cap counts every import expanded, repeats and nested
// ones included.
func TestInlineCapsImportInstances(t *testing.T) {
	repos := budgetRepos(t)
	ctx := context.Background()
	mods := fakeModules{"leaf.alloy": "declare \"d\" {\n}\n"}
	repeated := func(n int, path string) string {
		var sb strings.Builder
		for i := range n {
			sb.WriteString(importGit("", "m"+strconv.Itoa(i), path))
		}
		return sb.String()
	}
	if !checkInlineMatches(t, mods, repos, repeated(MaxImportInstances, "leaf.alloy"), true, true) {
		t.Fatalf("%d imports of one module are refused", MaxImportInstances)
	}
	_, err := inline(ctx, mods, pgtype.UUID{}, repos, repeated(MaxImportInstances+1, "leaf.alloy"))
	if !errors.Is(err, ErrNotInlinable) || !strings.Contains(err.Error(), fmt.Sprintf("more than %d imports", MaxImportInstances)) {
		t.Fatalf("%d imports: %v", MaxImportInstances+1, err)
	}
	// Fan-out 4, four levels deep: 4+16+64+256 instances of tiny modules.
	mods = fakeModules{"l3.alloy": "declare \"d\" {\n}\n"}
	for _, l := range []string{"l2", "l1", "l0"} {
		next := map[string]string{"l2": "l3", "l1": "l2", "l0": "l1"}[l]
		mods[l+".alloy"] = repeated(4, next+".alloy")
	}
	_, err = inline(ctx, mods, pgtype.UUID{}, repos, repeated(4, "l0.alloy"))
	if !errors.Is(err, ErrNotInlinable) || !strings.Contains(err.Error(), "imports") {
		t.Fatalf("nested fan-out: %v", err)
	}
}

// The shapes that made Inline build far more than the cap from a pipeline
// within it. Each must be refused having allocated, beyond what parsing its
// texts costs, at most twice the cap. SERVED_BUDGET_REFERENCE=1 runs the
// same checks on the pre-budget Inline, which fails them.
func TestInlineStaysWithinTheBudget(t *testing.T) {
	fn := inline
	if os.Getenv("SERVED_BUDGET_REFERENCE") == "1" {
		fn = refInline
	}
	repos := budgetRepos(t)
	labelmap := "prometheus.relabel \"r\" {\n  forward_to = []\n  rule {\n    action = \"labelmap\"\n  }\n}\n"
	repeated := func(n int, path string) string {
		var sb strings.Builder
		for i := range n {
			sb.WriteString(importGit("", "m"+strconv.Itoa(i), path))
		}
		return sb.String()
	}
	fanOut := fakeModules{"d.alloy": labelmap + "x = \"" + strings.Repeat("\t", 20<<10) + "\"\n"}
	for _, l := range []string{"c", "b", "a"} {
		next := map[string]string{"c": "d", "b": "c", "a": "b"}[l]
		fanOut[l+".alloy"] = repeated(3, next+".alloy")
	}
	for _, tc := range []struct {
		name     string
		mods     fakeModules
		contents string
	}{
		{
			// One 50 KiB module imported 200 times.
			name:     "one module many times",
			mods:     fakeModules{"big.alloy": labelmap + "// " + strings.Repeat("a", 50<<10) + "\n"},
			contents: repeated(200, "big.alloy"),
		},
		{
			// Fan-out 3 through four levels of modules: 81 copies of a
			// 20 KiB leaf of tabs, re-escaped at every level.
			name:     "nested fan-out",
			mods:     fanOut,
			contents: repeated(3, "a.alloy"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var out string
			var err error
			start := time.Now()
			build := allocated(func() { out, err = fn(ctx, tc.mods, pgtype.UUID{}, repos, tc.contents) })
			elapsed := time.Since(start)
			// Parsing: the pipeline, each module once and the served form
			// once more per level (the drop pass's check), generously.
			parse := allocated(func() {
				errSink = validate.ServedFormProblem(tc.contents)
				for _, m := range tc.mods {
					errSink = validate.ServedFormProblem(m)
				}
			}) * 3
			// The Alloy encoder (alloysyntax.QuoteString) works in about a
			// dozen times the memory it returns. Each level quotes its module
			// once (memoized), and the levels' quotes sum to under twice the
			// largest, which fits the cap: two cap-sized quotes' worth.
			encoder := 2 * allocated(func() { _ = alloysyntax.QuoteString(strings.Repeat("\t", validate.MaxContentBytes/2)) })
			beyond := int64(build) - int64(parse) - int64(encoder)
			t.Logf("%s: Inline allocated %.1f MiB, parsing %.1f MiB and two cap-sized quotes %.1f MiB of it: "+
				"%.1f MiB beyond, in %v; served %d bytes, err %v", tc.name, mib(build), mib(parse), mib(encoder),
				float64(beyond)/(1<<20), elapsed.Round(time.Millisecond), len(out), err)
			if err == nil {
				t.Fatalf("served %d bytes, want a refusal", len(out))
			}
			if !errors.Is(err, ErrNotInlinable) {
				t.Fatalf("refused with %v, want ErrNotInlinable", err)
			}
			if beyond > 2*validate.MaxContentBytes {
				t.Fatalf("allocated %.1f MiB beyond parsing, more than twice the cap", float64(beyond)/(1<<20))
			}
			if elapsed > 5*time.Second {
				t.Fatalf("took %v", elapsed)
			}
		})
	}
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

// The drop pass reads a level's line ending from the text the old inliner
// left (#450 review): without its import.git blocks and the import.string
// contents it re-quoted. A raw (backtick) content may hold raw CR LF; when
// it holds an import.git, directly or in a module or content it follows,
// the old inliner re-quoted it and its CR LF no longer counted.
func TestInlineReadsTheLineEndingTheOldInlinerLeft(t *testing.T) {
	repos := budgetRepos(t)
	relabel := "prometheus.relabel \"r\" {\n  forward_to = []\n  rule {\n    action = \"labelmap\"\n  }\n}\n"
	crlf := func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
	g := strings.TrimSuffix(importGit("", "g", "m0.alloy"), "\n")
	rawMod := "x = 1\n" + relabel + "import.string \"s\" {\n  content = `" + crlf(importGit("", "g", "m0.alloy")) + "`\n}\n"
	mods := fakeModules{
		"m0.alloy": "x = 1\n", "crlf.alloy": "x = 1\r\n" + relabel, "raw.alloy": rawMod, "lm.alloy": relabel,
	}
	for name, contents := range map[string]string{
		"raw content with CR LF holding an import.git": relabel + "import.string \"s\" {\n  content = `" +
			crlf(importGit("", "g", "m0.alloy")) + "`\n}\n",
		"the same in a cached module": importGit("", "r", "raw.alloy"),
		"raw content importing a labelmap, LF outside": "import.string \"s\" {\n  content = `" +
			crlf(importGit("", "g", "lm.alloy")) + "`\n}\n" + relabel,
		"raw content holding an import.git one level down": relabel + "import.string \"s\" {\n  content = `" +
			"import.string \"t\" {\r\n  content = " + alloysyntax.QuoteString(importGit("", "g", "m0.alloy")) + "\r\n}\r\n`\n}\n",
		"raw content with CR LF and no import.git": relabel + "import.string \"s\" {\n  content = `y = 1\r\n`\n}\n",
		"CR LF inside the import.git block only":   relabel + crlf(importGit("", "g", "m0.alloy")),
		"a module written with CR LF":              relabel + importGit("", "g", "crlf.alloy"),
		"a raw second content":                     relabel + "import.string \"s\" {\n  content = \"x = 1\"\n  content = `" + crlf(importGit("", "g", "m0.alloy")) + "`\n}\n",
		"CR LF in a declare":                       "declare \"d\"\r\n{\r\n" + crlf(importGit("  ", "g", "lm.alloy")) + "}\n" + relabel,
		"an import.git after an exporter's brace":  "otelcol.exporter.loki \"a\" {\n  forward_to = [loki.write.a.receiver]\n}" + g + "\n",
	} {
		t.Run(name, func(t *testing.T) { checkInlineMatches(t, mods, repos, contents, true, true) })
	}
}
