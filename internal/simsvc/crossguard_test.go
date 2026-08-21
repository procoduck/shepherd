package simsvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/schema"
	"shepherd/internal/simulate"
	"shepherd/internal/version"
	"shepherd/internal/visual"
)

// crossGuardSchema loads the shipped Alloy schema artifact — the same one
// internal/visual's own corpus tests load (internal/visual/render_test.go's
// corpusSchema) — so that visual.Render below runs with the port model the
// product actually serves, not a hand-trimmed fixture. Loaded once: parsing
// the embedded artifact is not free and every spec in this file needs it.
var (
	crossGuardSchemaOnce sync.Once
	crossGuardSchemaVal  visual.SchemaPayload
	crossGuardSchemaErr  error
)

func crossGuardSchema() (visual.SchemaPayload, error) {
	crossGuardSchemaOnce.Do(func() {
		reg, err := schema.New(schema.Embedded, version.AlloySchemaVersion)
		if err != nil {
			crossGuardSchemaErr = err
			return
		}
		merged, _, err := reg.Get(reg.CurrentVersion())
		if err != nil {
			crossGuardSchemaErr = err
			return
		}
		raw, err := json.Marshal(merged)
		if err != nil {
			crossGuardSchemaErr = err
			return
		}
		crossGuardSchemaErr = json.Unmarshal(raw, &crossGuardSchemaVal)
	})
	return crossGuardSchemaVal, crossGuardSchemaErr
}

// This is the cross-guard test the ledger (B-CONCAT) demands: it pins
// internal/visual's renderer against internal/simsvc's CheckEndpoints so
// that the NEXT renderer change to emit a new expression form for a
// multi-wire "targets" input — array.concat is not the only shape a language
// change could introduce — fails HERE, loudly, rather than only showing up
// as a silently-refused sandbox run days later.
//
// It deliberately does not hand-write an `array.concat(...)` string: it
// loads internal/visual/testdata/corpus/fanin-fanout.graph.json — the
// committed fixture that exists specifically because it is the fan-in shape
// B-CONCAT is about (two discovery.relabel nodes wired onto one
// prometheus.scrape "targets" input) — and renders it with the SAME
// visual.Render entry point rpc_visual.go calls in production. The only
// change from the committed graph is swapping its remote_write destination
// for one inside the sandbox harness allowlist, because the corpus fixture's
// own destination (prometheus.example.com) is deliberately outside any
// allowlist and would fail CheckEndpoints for a reason unrelated to the
// array.concat carve-out under test.
var _ = Describe("Renderer/guard cross-check (B-CONCAT)", func() {
	It("accepts real fan-in renderer output through CheckEndpoints", func() {
		schemaPayload, err := crossGuardSchema()
		Expect(err).NotTo(HaveOccurred())

		path := filepath.Join("..", "visual", "testdata", "corpus", "fanin-fanout.graph.json")
		raw, err := os.ReadFile(path)
		Expect(err).NotTo(HaveOccurred(), "corpus fixture %s must exist for this test to mean anything", path)

		const allowedHost = "shepherd-simulator"
		allowedURL := "http://" + allowedHost + ":9110" + simulate.CapturePathPrometheus
		// The committed fixture points at prometheus.example.com by design —
		// see the doc comment above — so this is the one substitution made
		// before rendering; everything else in the fixture, including the
		// two-source fan-in that produces array.concat, is untouched.
		Expect(string(raw)).To(ContainSubstring("https://prometheus.example.com/api/v1/write"),
			"fixture's destination literal changed; update the substitution below")
		raw = []byte(strings.Replace(string(raw), "https://prometheus.example.com/api/v1/write", allowedURL, 1))

		var doc visual.GraphDocument
		Expect(json.Unmarshal(raw, &doc)).To(Succeed())

		result := visual.Render(doc, schemaPayload)
		Expect(result.Diagnostics).To(BeEmpty(), "real renderer output must be clean for this test to be evidence")
		Expect(result.Content).To(ContainSubstring("array.concat("),
			"fixture stopped exercising the fan-in shape this test pins — B-CONCAT is untested if this fails")

		Expect(CheckEndpoints(result.Content, []string{allowedHost})).To(Succeed())
	})
})
