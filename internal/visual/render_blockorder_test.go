package visual_test

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/version"
	"shepherd/internal/visual"
)

// Alloy block order is semantic, and loki.process is where it bites: stages run
// in document order. A user who authors "parse the JSON, then drop on a field
// the parse extracted" gets silence if the two are emitted the other way round —
// stage.drop runs against an empty extracted map, drops nothing, and the config
// is still valid and still runs.
//
// Before BlockOrder existed the authored order was not merely lost at render
// time, it was never stored: GraphNode.Props is a map keyed by block name.
// loki.process declares stage.drop at schema index 3 and stage.json at 6, so
// every such pipeline rendered inverted — including the text served to real
// fleet agents, not just the sandbox.
var _ = Describe("Render: loki.process stage order", func() {
	var sc visual.SchemaPayload

	BeforeEach(func() {
		// The shipped artifact, not a fixture: the whole point is the real
		// schema's declaration order (stage.drop 3, stage.json 6).
		var err error
		sc, err = loadShippedSchema()
		Expect(err).NotTo(HaveOccurred())
	})

	// parse-then-drop, the order the user wants and the inverse of the schema's.
	newDoc := func(order []string) visual.GraphDocument {
		return visual.GraphDocument{
			Kind: "alloy-graph/v1",
			Nodes: []visual.GraphNode{{
				ID:        "n1",
				Component: "loki.process",
				Label:     "proc",
				Props: map[string]interface{}{
					"stage.json": map[string]interface{}{
						"expressions": map[string]interface{}{"level": "level"},
					},
					"stage.drop": map[string]interface{}{
						"source": "level",
						"value":  "debug",
					},
				},
				BlockOrder: order,
			}},
		}
	}

	indexOf := strings.Index

	It("emits the author's order when BlockOrder records it", func() {
		res := visual.Render(newDoc([]string{"stage.json", "stage.drop"}), sc)

		json, drop := indexOf(res.Content, "stage.json"), indexOf(res.Content, "stage.drop")
		Expect(json).To(BeNumerically(">=", 0), "stage.json must render:\n%s", res.Content)
		Expect(drop).To(BeNumerically(">=", 0), "stage.drop must render:\n%s", res.Content)
		Expect(json).To(BeNumerically("<", drop),
			"stage.json must precede stage.drop — the author's order:\n%s", res.Content)
	})

	It("emits the reverse when the author reversed it", func() {
		// The mirror case matters: a renderer that happened to hardcode
		// json-before-drop would pass the spec above and still be broken.
		res := visual.Render(newDoc([]string{"stage.drop", "stage.json"}), sc)

		Expect(indexOf(res.Content, "stage.drop")).To(BeNumerically("<", indexOf(res.Content, "stage.json")),
			"stage.drop must precede stage.json here:\n%s", res.Content)
	})

	It("falls back to schema order when BlockOrder is absent, as pre-existing graphs do", func() {
		// Not an endorsement of the old behaviour — a guarantee that graphs saved
		// before the field existed still render byte-identically rather than
		// changing under their authors.
		res := visual.Render(newDoc(nil), sc)

		Expect(indexOf(res.Content, "stage.drop")).To(BeNumerically("<", indexOf(res.Content, "stage.json")),
			"with no recorded order the schema's own sequence (drop=3, json=6) applies:\n%s", res.Content)
	})

	It("ignores names it does not declare and still renders every block present", func() {
		res := visual.Render(newDoc([]string{"stage.nonexistent", "stage.json", "stage.drop"}), sc)

		Expect(res.Content).To(ContainSubstring("stage.json"))
		Expect(res.Content).To(ContainSubstring("stage.drop"))
		Expect(res.Content).NotTo(ContainSubstring("stage.nonexistent"))
		Expect(indexOf(res.Content, "stage.json")).To(BeNumerically("<", indexOf(res.Content, "stage.drop")))
	})

	It("renders blocks omitted from BlockOrder after the ones it names", func() {
		// A partial order must not drop the blocks it does not mention.
		res := visual.Render(newDoc([]string{"stage.drop"}), sc)

		Expect(res.Content).To(ContainSubstring("stage.json"))
		Expect(indexOf(res.Content, "stage.drop")).To(BeNumerically("<", indexOf(res.Content, "stage.json")))
	})
})

// A round trip is where the loss used to become permanent: "recreate as visual
// pipeline" parses existing Alloy into a graph, and if the parser does not
// record block order the re-render silently re-sequences the user's stages into
// schema order. The config stays valid, so nothing downstream objects.
var _ = Describe("Parse then render: block order survives the round trip", func() {
	It("recovers the authored order from text and reproduces it", func() {
		sc, err := loadShippedSchema()
		Expect(err).NotTo(HaveOccurred())

		// Deliberately the inverse of the schema's declaration order.
		const src = `loki.process "proc" {
  forward_to = []

  stage.json {
    expressions = {
      level = "level",
    }
  }

  stage.drop {
    source = "level"
    value  = "debug"
  }
}
`
		res := visual.ParseAlloy(src, version.AlloySchemaVersion, sc)
		doc := res.Doc
		Expect(doc.Nodes).To(HaveLen(1))
		Expect(doc.Nodes[0].BlockOrder).To(Equal([]string{"stage.json", "stage.drop"}),
			"the parser must record the order it read")

		out := visual.Render(doc, sc)
		Expect(strings.Index(out.Content, "stage.json")).
			To(BeNumerically("<", strings.Index(out.Content, "stage.drop")),
				"the re-render must not re-sequence the author's stages:\n%s", out.Content)
	})
})
