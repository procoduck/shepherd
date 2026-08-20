package simulate_test

import (
	"context"
	"os"
	"os/exec"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/simulate"
	"shepherd/internal/validate"
	"shepherd/internal/visual"
)

// §7.4 item: the transformed config must itself pass stages 1–2 (§6.4 step 2).
// This is the only test that can catch a stub which renders but does not run —
// most concretely, anyone who "corrects" the static stub back to §6.4's literal
// discovery.static, which Alloy v1.18.1 has never shipped. Nothing else in the
// suite executes the real binary against renderer output.
var _ = Describe("Transform: the transformed config validates", func() {
	var (
		payload visual.SchemaPayload
		policy  simulate.Policy
	)

	BeforeEach(func() { payload, policy = shippedSchema() })

	// Stage 1 is the real Alloy syntax parser compiled in, so it needs no
	// binary and runs everywhere, on every corpus entry, always.
	It("parses: every transformed corpus graph passes stage 1", func() {
		for _, name := range corpusNames() {
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: corpusGraph(name), Schema: payload, Policy: policy, Harness: testHarness(),
			})
			Expect(err).NotTo(HaveOccurred(), "corpus entry %q", name)

			content := visual.Render(result.Graph, payload).Content
			stage1 := validate.Stage1(content)
			Expect(stage1.Valid).To(BeTrue(),
				"corpus entry %q failed stage 1: %+v\n%s", name, stage1.Diagnostics, content)
		}
	})

	// Stage 2 shells out to the pinned Alloy binary. It is labelled so CI can
	// require it; it must not be dropped from the required set, because it is
	// the only guard against a stub component the binary does not have.
	It("runs: every transformed corpus graph passes stages 1-2 against the real binary", Label("needs-alloy-binary"), func() {
		binary := alloyBinary()
		if binary == "" {
			Skip("no Alloy binary: set SHEPHERD_VALIDATE_ALLOY_BINARY or put `alloy` on PATH")
		}
		validator := validate.New(&config.ValidateConfig{
			AlloyBinary: binary, StabilityLevel: "experimental", Timeout: 90 * time.Second,
		})

		for _, name := range corpusNames() {
			result, err := simulate.Transform(simulate.TransformRequest{
				Graph: corpusGraph(name), Schema: payload, Policy: policy, Harness: testHarness(),
			})
			Expect(err).NotTo(HaveOccurred(), "corpus entry %q", name)

			content := visual.Render(result.Graph, payload).Content
			res := validator.Stages12(context.Background(), validate.WrapForValidation("sim_"+name, content))
			Expect(res.Valid).To(BeTrue(),
				"corpus entry %q failed stages 1-2: %+v\n%s", name, res.Diagnostics, content)
		}
	})
})

func alloyBinary() string {
	if path := os.Getenv("SHEPHERD_VALIDATE_ALLOY_BINARY"); path != "" {
		return path
	}
	path, err := exec.LookPath("alloy")
	if err != nil {
		return ""
	}
	return path
}
