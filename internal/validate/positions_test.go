package validate_test

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/validate"
	"shepherd/internal/wizard/wizardtest"
)

// positions renders each diagnostic as "line:col message" so a failure shows
// every position at once.
func positions(ds []validate.Diagnostic) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = fmt.Sprintf("%d:%d %s", d.Line, d.Col, d.Message)
	}
	return out
}

// Walkthrough F2: every pipeline-editor diagnostic was off by +1 line and +2
// columns, because the gate validates the contents inside WrapForValidation's
// one-line `declare "pipe_…" {` header and two-space indent, and no caller
// mapped the positions back to the text the user wrote.
var _ = Describe("Pipeline diagnostic positions (F2)", func() {
	noBinary := func() *validate.Validator {
		return validate.New(&config.ValidateConfig{AlloyBinary: "", StabilityLevel: "experimental", Timeout: 10 * time.Second})
	}

	It("puts a stage-1 error on line 3 of a nested multi-line pipeline at its own line:col", func() {
		contents := "prometheus.remote_write \"a\" {\n" +
			"  endpoint {\n" +
			"    url = = \"x\"\n" +
			"  }\n" +
			"}\n"
		r := noBinary().ValidatePipeline(context.Background(), "demo", contents)
		Expect(r.Valid).To(BeFalse())
		Expect(r.Diagnostics).NotTo(BeEmpty())
		Expect(r.Diagnostics[0].Line).To(Equal(3))
		Expect(r.Diagnostics[0].Col).To(Equal(11), "the second '=' is column 11 of `    url = = \"x\"`")
	})

	It("keeps every diagnostic of a one-line document inside that line", func() {
		// The walkthrough's "x": four errors, at 2:4, 3:1, 4:1 and 5:1 of the
		// wrapped text. The first is the user's own text (1:2); the rest are
		// the wrapper's closing brace and instantiation, clamped to the end
		// of the user's text with their messages kept.
		r := noBinary().ValidatePipeline(context.Background(), "preview", "x")
		Expect(r.Valid).To(BeFalse())
		Expect(r.Diagnostics).NotTo(BeEmpty())
		for _, d := range r.Diagnostics {
			Expect(d.Line).To(Equal(1), "%v", positions(r.Diagnostics))
			Expect(d.Col).To(Equal(2), "%v", positions(r.Diagnostics))
			Expect(d.Message).NotTo(BeEmpty())
		}
	})

	It("clamps an unterminated block's error to the end of the user's text, message kept", func() {
		contents := "prometheus.exporter.self \"a\" {\n  bogus = 1\n"
		r := noBinary().ValidatePipeline(context.Background(), "demo", contents)
		Expect(r.Valid).To(BeFalse())
		Expect(r.Diagnostics).NotTo(BeEmpty())
		// The text ends with a newline, so its end is the empty line 3, column 1.
		for _, d := range r.Diagnostics {
			Expect([]int{d.Line, d.Col}).To(Equal([]int{3, 1}), "%v", positions(r.Diagnostics))
		}
		Expect(r.Diagnostics[0].Message).To(ContainSubstring("}"))
	})

	It("reports the in-process port-shape check in the user's coordinates", func() {
		r := noBinary().ValidatePipeline(context.Background(), "demo", listOfLists)
		Expect(r.Valid).To(BeFalse())
		Expect(r.Diagnostics).To(HaveLen(1))
		// `  targets    = [discovery.kubernetes.pods.targets]` is line 6; the diagnostic points at the reference, column 17.
		Expect(r.Diagnostics[0].Line).To(Equal(6), "%v", positions(r.Diagnostics))
		Expect(r.Diagnostics[0].Col).To(Equal(17), "%v", positions(r.Diagnostics))
	})

	// The walkthrough's exact case, through the real pinned `alloy validate`
	// (Stage 2). Self-skips without Docker/alloy.
	It("puts the real alloy validate's unrecognized attribute at 1:32, not 2:34", func() {
		bin := wizardtest.AlloyBinary()
		if bin == "" {
			Skip("no alloy binary and no docker")
		}
		v := validate.New(&config.ValidateConfig{AlloyBinary: bin, StabilityLevel: "experimental", Timeout: 2 * time.Minute})
		r := v.ValidatePipeline(context.Background(), "preview", `prometheus.exporter.self "a" { bogus = 1 }`)
		Expect(r.Valid).To(BeFalse())
		Expect(positions(r.Diagnostics)).To(ConsistOf(`1:32 unrecognized attribute name "bogus"`))
	})
})

var _ = Describe("UnwrapDiagnostics", func() {
	// contents → WrapForValidation("p", contents):
	//
	//	1 declare "pipe_p" {
	//	2   a = 1          ← user line 1
	//	3                  ← user line 2 (empty: not indented)
	//	4   b = 2          ← user line 3
	//	5 }
	//	6 pipe_p "default" { }
	const contents = "a = 1\n\nb = 2"

	It("is the inverse of WrapForValidation's layout", func() {
		Expect(validate.WrapForValidation("p", contents)).To(Equal(
			"declare \"pipe_p\" {\n  a = 1\n\n  b = 2\n}\npipe_p \"default\" { }\n"))
	})

	DescribeTable("maps a wrapped position to the user's text",
		func(line, col, wantLine, wantCol int) {
			got := validate.UnwrapDiagnostics(contents, []validate.Diagnostic{{Line: line, Col: col, Message: "m", Stage: 2}})
			Expect(got).To(Equal([]validate.Diagnostic{{Line: wantLine, Col: wantCol, Message: "m", Stage: 2}}))
		},
		Entry("indented line: -1 line, -2 cols", 2, 5, 1, 3),
		Entry("first column of the user's text", 2, 3, 1, 1),
		Entry("a column inside the indent clamps to 1", 2, 1, 1, 1),
		Entry("empty line keeps its column", 3, 1, 2, 1),
		Entry("last user line", 4, 7, 3, 5),
		Entry("closing brace clamps to the end of the text", 5, 1, 3, 6),
		Entry("instantiation line clamps to the end of the text", 6, 20, 3, 6),
		Entry("past the document clamps to the end of the text", 9, 1, 3, 6),
		Entry("header line → start of the text", 1, 9, 1, 1),
		Entry("unknown line stays unknown", 0, 0, 0, 0),
		Entry("unknown column stays unknown", 2, 0, 1, 0),
	)

	It("ends a trailing-newline text on its empty last line", func() {
		got := validate.UnwrapDiagnostics("a = 1\n", []validate.Diagnostic{{Line: 4, Col: 1}})
		Expect(got).To(Equal([]validate.Diagnostic{{Line: 2, Col: 1}}))
	})

	It("returns nil for no diagnostics and does not touch its input", func() {
		Expect(validate.UnwrapDiagnostics(contents, nil)).To(BeNil())
		in := []validate.Diagnostic{{Line: 2, Col: 5}}
		_ = validate.UnwrapDiagnostics(contents, in)
		Expect(in[0]).To(Equal(validate.Diagnostic{Line: 2, Col: 5}))
	})
})
