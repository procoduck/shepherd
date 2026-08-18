package visual_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/visual"
)

var _ = Describe("ParseAlloy", func() {
	It("extracts component blocks as nodes", func() {
		content := `
prometheus.scrape "app" {
  job_name = "myapp"
}

prometheus.remote_write "sink" {
  receiver = [prometheus.scrape.app.metrics]
}
`
		result := visual.ParseAlloy(content, "alloy-v1.18.1")
		Expect(result.Doc.Nodes).To(HaveLen(2))

		names := map[string]string{}
		for _, n := range result.Doc.Nodes {
			names[n.Component] = n.Label
		}
		Expect(names).To(HaveKey("prometheus.scrape"))
		Expect(names).To(HaveKey("prometheus.remote_write"))
		Expect(names["prometheus.scrape"]).To(Equal("app"))
	})

	It("extracts edges from list reference attributes", func() {
		content := `
prometheus.scrape "app" {}

prometheus.remote_write "sink" {
  receiver = [prometheus.scrape.app.metrics]
}
`
		result := visual.ParseAlloy(content, "alloy-v1.18.1")
		Expect(result.Doc.Edges).To(HaveLen(1))
		e := result.Doc.Edges[0]
		Expect(e.From.Port).To(Equal("metrics"))
		Expect(e.To.Port).To(Equal("receiver"))
	})

	It("returns opaque=true and a warning for invalid syntax", func() {
		result := visual.ParseAlloy("this is { not valid alloy }", "alloy-v1.18.1")
		Expect(result.Opaque).To(BeTrue())
		Expect(result.Warning).NotTo(BeEmpty())
	})

	It("sets schema_version from the argument", func() {
		result := visual.ParseAlloy(`foo "bar" {}`, "alloy-v1.18.1")
		Expect(result.Doc.SchemaVersion).To(Equal("alloy-v1.18.1"))
	})

	It("handles pipeline with no blocks gracefully", func() {
		result := visual.ParseAlloy("// just a comment\n", "alloy-v1.18.1")
		Expect(result.Doc.Nodes).To(BeEmpty())
		Expect(result.Opaque).To(BeFalse())
	})
})
