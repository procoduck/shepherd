package simulate_test

import (
	"errors"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/simulate"
	"shepherd/internal/visual"
)

// #393 review: every diagnostic list visual and simulate produce is bounded
// like Render's — a refusal lists at most visual.MaxDiagnostics problems,
// then counts the rest.
var _ = Describe("Transform: bounded refusals", func() {
	It("lists at most MaxDiagnostics problems and counts the rest", func() {
		schema, policy := shippedSchema()
		doc := visual.GraphDocument{Kind: "alloy-graph/v1"}
		for i := range 500 {
			id := fmt.Sprintf("n%d", i)
			doc.Nodes = append(doc.Nodes, visual.GraphNode{ID: id, Component: "not.a.component", Label: id})
		}
		_, err := simulate.Transform(simulate.TransformRequest{Graph: doc, Schema: schema, Policy: policy})
		var errs simulate.TransformErrors
		Expect(errors.As(err, &errs)).To(BeTrue())
		Expect(errs).To(HaveLen(visual.MaxDiagnostics + 1))
		last := errs[len(errs)-1]
		Expect(last.Code).To(Equal(simulate.CodeUnknownComponent))
		Expect(last.Message).To(Equal("300 more problems are not listed"))
	})
})
