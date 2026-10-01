package validate_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/validate"
	"shepherd/internal/wizard/wizardtest"
)

// listOfLists is issue #233's reproduction verbatim: `alloy validate` (v1.20.1)
// exits 0 on it, and Alloy refuses it at load with
// "target::ConvertFrom: conversion from '[]discovery.Target' is not supported".
const listOfLists = `discovery.kubernetes "pods" {
  role = "pod"
}

prometheus.scrape "demo" {
  targets    = [discovery.kubernetes.pods.targets]
  forward_to = [prometheus.remote_write.demo.receiver]
}

prometheus.remote_write "demo" {
  endpoint {
    url = "https://prom.example.com/api/v1/push"
  }
}
`

// noBinary is a Validator with Stage 2's `alloy validate` exec skipped — the
// port-shape check is in-process and must run anyway.
func noBinary() *validate.Validator {
	return validate.New(&config.ValidateConfig{AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10 * time.Second})
}

var _ = Describe("Stage 2 port-shape check (#233)", func() {
	It("refuses a targets reference wrapped in a list literal, with line/col and a fix", func() {
		r := noBinary().Stage2(context.Background(), listOfLists)
		Expect(r.Valid).To(BeFalse())
		Expect(r.Diagnostics).To(HaveLen(1))
		d := r.Diagnostics[0]
		Expect(d.Stage).To(Equal(2))
		Expect(d.Line).To(Equal(6))
		Expect(d.Col).To(Equal(17)) // the element, inside the brackets
		Expect(d.Message).To(ContainSubstring("targets expects a list of targets"))
		Expect(d.Message).To(ContainSubstring("[discovery.kubernetes.pods.targets] is a list of lists"))
		Expect(d.Message).To(ContainSubstring("drop the brackets"))
	})

	It("refuses it through Stages12 on declare-wrapped content, the shape every save validates", func() {
		r := noBinary().Stages12(context.Background(), validate.WrapForValidation("demo", listOfLists))
		Expect(r.Valid).To(BeFalse())
		Expect(r.Diagnostics).To(HaveLen(1))
		Expect(r.Diagnostics[0].Stage).To(Equal(2))
		Expect(r.Diagnostics[0].Line).To(Equal(7)) // one line down: the declare header
	})

	It("refuses a single receiver where forward_to wants a list", func() {
		r := noBinary().Stage2(context.Background(), `prometheus.scrape "demo" {
  targets    = []
  forward_to = prometheus.remote_write.demo.receiver
}

prometheus.remote_write "demo" {
  endpoint {
    url = "https://prom.example.com/api/v1/push"
  }
}
`)
		Expect(r.Valid).To(BeFalse())
		Expect(r.Diagnostics).To(HaveLen(1))
		Expect(r.Diagnostics[0].Line).To(Equal(3))
		Expect(r.Diagnostics[0].Message).To(ContainSubstring("wrap it in brackets: [prometheus.remote_write.demo.receiver]"))
	})

	It("accepts the corrected config", func() {
		fixed := `discovery.kubernetes "pods" {
  role = "pod"
}

prometheus.scrape "demo" {
  targets    = discovery.kubernetes.pods.targets
  forward_to = [prometheus.remote_write.demo.receiver]
}

prometheus.remote_write "demo" {
  endpoint {
    url = "https://prom.example.com/api/v1/push"
  }
}
`
		Expect(noBinary().Stage2(context.Background(), fixed).Valid).To(BeTrue())
	})

	// The gap this closes, proven against the real pinned binary rather than
	// asserted: Stage 2 with `alloy validate` running still refuses, and the
	// only diagnostic is the port-shape one — alloy validate itself had
	// nothing to say about the config. Self-skips without Docker/alloy; the
	// no-binary specs above carry the refusal either way.
	It("is what refuses the config when the real alloy validate accepts it", func() {
		bin := wizardtest.AlloyBinary()
		if bin == "" {
			Skip("no alloy binary and no docker")
		}
		v := validate.New(&config.ValidateConfig{AlloyBinary: bin, StabilityLevel: "experimental", Timeout: 2 * time.Minute})
		r := v.Stages12(context.Background(), validate.WrapForValidation("demo", listOfLists))
		Expect(r.Valid).To(BeFalse())
		Expect(r.Diagnostics).To(HaveLen(1))
		Expect(r.Diagnostics[0].Message).To(ContainSubstring("is a list of lists"))
	})
})
