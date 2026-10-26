package serve_test

import (
	"context"
	"strconv"
	"testing"

	"shepherd/internal/merge"
	"shepherd/internal/serve"
)

// The serve-path bound (#390, second review): whatever the inliner returns,
// ServedForm — which every serve path and Stage 3 dry run goes through —
// refuses a strict org's served text with an import.git block left in it,
// however spelt and wherever (inside an import.string too). A no-op inliner
// stands in for a broken one.
func TestServedFormRefusesAnyImportGitLeftBehind(t *testing.T) {
	noop := func(_ context.Context, c string) (string, error) { return c, nil }
	block := func(name string) string { return name + " \"m\" { }\n" }
	for _, name := range []string{"import.git", "import . git", "import/**/.git", "import.\ngit"} {
		for _, src := range []string{
			block(name),
			"declare \"d\" {\n" + block(name) + "}\n",
			"import.string \"s\" {\n  content = " + strconv.Quote(block(name)) + "\n}\n",
		} {
			p := []merge.Pipeline{{ID: "1", Name: "p", Contents: src, Matchers: []string{}, Source: "ui"}}
			if _, err := serve.ServedForm(context.Background(), noop, p); err == nil {
				t.Errorf("served a strict org's import.git:\n%s", src)
			}
		}
	}
	clean := []merge.Pipeline{{ID: "1", Name: "p", Contents: "logging { level = \"info\" }\n", Matchers: []string{}, Source: "ui"}}
	if _, err := serve.ServedForm(context.Background(), noop, clean); err != nil {
		t.Errorf("clean text refused: %v", err)
	}
}
