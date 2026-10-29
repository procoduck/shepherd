package validate

import (
	"strconv"
	"strings"
	"testing"
)

// The tenant lint parses a module imported many times once, but keeps at
// most maxParsedBytes of module text parsed (#450 review): a tree is several
// times its text, and keeping every module's for the whole call let 16
// large modules hold well over 100 MiB. The pipeline's own text is never
// kept (it is parsed once per run, as before).
func TestTenantLintKeepsFewParseTrees(t *testing.T) {
	g := &gitState{}
	module := func(i int) string {
		return "// " + strconv.Itoa(i) + "\n" + strings.Repeat("x = 1\n", (MaxContentBytes/2)/6)
	}
	for i := range maxGitModules {
		for range 3 {
			if g.parse(module(i), 1) == nil {
				t.Fatalf("module %d does not parse", i)
			}
		}
	}
	if g.parsedBytes > maxParsedBytes {
		t.Fatalf("keeps %d bytes of text parsed, more than %d", g.parsedBytes, maxParsedBytes)
	}
	if len(g.parsed) != 1 {
		t.Fatalf("keeps %d trees, want the one that fits", len(g.parsed))
	}
	first := g.parse(module(0), 1)
	if again := g.parse(module(0), 1); again != first {
		t.Fatal("a kept module is parsed again")
	}
	if g.parse(strings.Repeat("y = 2\n", 10), 0); len(g.parsed) != 1 {
		t.Fatal("the pipeline's own text is kept")
	}
}
