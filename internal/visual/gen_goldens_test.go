package visual_test

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"shepherd/internal/visual"
)

// gen_goldens_test.go — regenerates golden corpus files from current renderer output.
//
// This is NOT a regular test. It is skipped unless GEN_GOLDENS=1 is explicitly set.
// Run with:
//
//	GEN_GOLDENS=1 go test ./internal/visual/ -run TestGenGoldens -v
//
// Run after changing the renderer or corpus graphs. Review the diff carefully;
// golden changes must correspond to verified render behavior changes. Do not run
// this to make failing corpus tests pass — fix the renderer instead.
// CI always skips this test because GEN_GOLDENS is not set.
func TestGenGoldens(t *testing.T) {
	if os.Getenv("GEN_GOLDENS") == "" {
		t.Skip("set GEN_GOLDENS=1 to regenerate")
	}
	schema := corpusSchema()
	entries := []string{
		"minimal-scrape", "fanin-fanout", "nested-blocks", "bindings-secret",
		"logs-chain", "disabled-node", "label-edgecases", "otel-three-signals", "kitchen-sink",
	}
	for _, name := range entries {
		path := "testdata/corpus/" + name + ".graph.json"
		data, err := os.ReadFile(path)
		if err != nil {
			t.Logf("SKIP %s: %v", name, err)
			continue
		}
		var doc visual.GraphDocument
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: unmarshal: %v", name, err)
		}
		result := visual.Render(doc, schema)
		if len(result.Diagnostics) > 0 {
			t.Logf("WARNING %s has diagnostics: %v", name, result.Diagnostics)
		}
		golden := "testdata/corpus/" + name + ".golden.alloy"
		if err := os.WriteFile(golden, []byte(result.Content), 0o644); err != nil {
			t.Fatalf("%s: write golden: %v", name, err)
		}
		fmt.Printf("wrote %s (%d bytes)\n", golden, len(result.Content))
	}
}
