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
			g.pick(alloysyntax.QuoteString(inner), alloysyntax.QuoteString(inner), "local.file.f.content") + "\n" + ind + "}\n"
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
	return sb.String()
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
// hundred bytes of the cap either side.
func TestInlineMatchesReferenceNearCap(t *testing.T) {
	g := &inlineGen{r: rand.New(rand.NewSource(3900))}
	repos := budgetRepos(t)
	const n = 400
	under, over := 0, 0
	// The served size is linear in the padding; its two coefficients
	// depend only on the shape.
	type shape struct {
		copies, mix int
		tabs        bool
	}
	linear := map[shape][2]float64{}
	for i := range n {
		copies := 1 + g.r.Intn(4)
		tabs := i%2 == 1
		build := func(pad int) (fakeModules, string) {
			filler := strings.Repeat("a", pad)
			if tabs {
				filler = strings.Repeat("\t", pad)
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
		sh := shape{copies: copies, mix: i % 3, tabs: tabs}
		if _, ok := linear[sh]; !ok {
			l0, l1 := size(0), size(1000)
			linear[sh] = [2]float64{float64(l0), float64(l1-l0) / 1000}
		}
		l0, perPad := linear[sh][0], linear[sh][1]
		target := validate.MaxContentBytes + g.r.Intn(601) - 300
		pad := max(int((float64(target)-l0)/perPad), 0)
		mods, contents := build(pad)
		if checkInlineMatches(t, mods, repos, contents, false, false) {
			under++
		} else {
			over++
		}
	}
	t.Logf("%d near-cap pipelines: %d served identically, %d refused alike", n, under, over)
	if under < n/5 || over < n/5 {
		t.Fatalf("the near-cap corpus does not straddle the cap: %d under, %d over", under, over)
	}
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
