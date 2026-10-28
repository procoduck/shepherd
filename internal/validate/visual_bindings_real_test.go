package validate_test

import (
	"context"
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/validate"
	"shepherd/internal/wizard/wizardtest"
)

// The visual builder's binding goldens through Stage 1+2 with the real pinned
// `alloy validate`: a doc.bindings[] entry at a nested path (#426) — a
// repeated block's instance, deeper nesting, a dotted block name — must render
// to Alloy the real binary accepts, not merely to text that looks right.
var _ = Describe("visual-builder binding goldens under the real alloy validate", func() {
	for _, name := range []string{"bindings-secret", "bindings-nested"} {
		It("accepts "+name, func() {
			bin := wizardtest.AlloyBinary()
			if bin == "" {
				Skip("no alloy binary and no docker")
			}
			text, err := os.ReadFile(filepath.Join("..", "visual", "testdata", "corpus", name+".golden.alloy"))
			Expect(err).NotTo(HaveOccurred())
			real := validate.New(&config.ValidateConfig{AlloyBinary: bin, StabilityLevel: "experimental", Timeout: 2 * time.Minute})
			r := real.Stages12(context.Background(), validate.WrapForValidation(name, string(text)))
			Expect(r.Valid).To(BeTrue(), "%v", r.Diagnostics)
			Expect(r.Skipped).To(BeEmpty())
		})
	}
})
