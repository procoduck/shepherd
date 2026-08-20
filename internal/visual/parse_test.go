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
		// Without a schema the parser cannot tell a data export from a
		// receiver export, so it keeps the referenced → referencing
		// orientation, which is correct for data exports.
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

	It("unquotes and types literal values", func() {
		result := visual.ParseAlloy(`prometheus.scrape "app" {
  job_name = "myapp"
  sample_limit = 12
  honor_labels = true
  scrape_protocols = ["a", "b"]
  params = { a = "1" }
}`, "v1.18.1")
		props := result.Doc.Nodes[0].Props
		Expect(props["job_name"]).To(Equal("myapp"))
		Expect(props["sample_limit"]).To(Equal(float64(12)))
		Expect(props["honor_labels"]).To(Equal(true))
		Expect(props["scrape_protocols"]).To(Equal([]interface{}{"a", "b"}))
		Expect(props["params"]).To(Equal(map[string]interface{}{"a": "1"}))
	})

	It("keeps nested and repeated blocks as nested props", func() {
		result := visual.ParseAlloy(`prometheus.remote_write "sink" {
  endpoint {
    url = "https://a/write"
    basic_auth {
      username = "u"
    }
  }
  endpoint {
    url = "https://b/write"
  }
}`, "v1.18.1")
		Expect(result.Opaque).To(BeFalse())
		endpoints, ok := result.Doc.Nodes[0].Props["endpoint"].([]interface{})
		Expect(ok).To(BeTrue(), "repeated blocks become a list of objects")
		Expect(endpoints).To(HaveLen(2))
		first, isObject := endpoints[0].(map[string]interface{})
		Expect(isObject).To(BeTrue())
		Expect(first["url"]).To(Equal("https://a/write"))
		Expect(first["basic_auth"]).To(Equal(map[string]interface{}{"username": "u"}))
	})

	It("preserves a non-literal expression instead of dropping it", func() {
		result := visual.ParseAlloy(`prometheus.remote_write "sink" {
  endpoint {
    bearer_token = convert.nonsensitive(remote.kubernetes.secret.s.data["token"])
  }
}`, "v1.18.1")
		endpoint, isObject := result.Doc.Nodes[0].Props["endpoint"].(map[string]interface{})
		Expect(isObject).To(BeTrue())
		Expect(endpoint["bearer_token"]).To(Equal(map[string]interface{}{
			"$expr": `convert.nonsensitive(remote.kubernetes.secret.s.data["token"])`,
		}))
	})

	It("reports a non-component block instead of pretending it is a node", func() {
		result := visual.ParseAlloy("logging {\n  level = \"debug\"\n}\n", "v1.18.1")
		Expect(result.Opaque).To(BeTrue())
		Expect(result.Warning).To(ContainSubstring("logging"))
		Expect(result.Doc.Nodes[0].Component).To(Equal("opaque.logging"))
		Expect(result.Doc.Nodes[0].Props["level"]).To(Equal("debug"))
	})

	It("reports a labelled nested block rather than losing the label", func() {
		result := visual.ParseAlloy("loki.process \"p\" {\n  stage.json \"x\" {\n  }\n}\n", "v1.18.1")
		Expect(result.Opaque).To(BeTrue())
		Expect(result.Warning).To(ContainSubstring("label"))
	})

	Context("with the shipped schema", func() {
		It("orients both reference kinds as dataflow (D1)", func() {
			schema := corpusSchema()
			result := visual.ParseAlloy(`
discovery.kubernetes "k8s" {
  role = "pod"
}

prometheus.scrape "app" {
  targets = [discovery.kubernetes.k8s.targets]
  forward_to = [prometheus.remote_write.sink.receiver]
}

prometheus.remote_write "sink" {
  endpoint {
    url = "https://prom/api/v1/write"
  }
}
`, "v1.18.1", schema)
			Expect(result.Opaque).To(BeFalse())
			Expect(result.Warning).To(BeEmpty())
			Expect(result.Doc.Edges).To(HaveLen(2))
			byID := map[string]visual.GraphEdge{}
			for _, e := range result.Doc.Edges {
				byID[e.From.Node+"->"+e.To.Node] = e
			}
			// data export: discovery → scrape
			Expect(byID).To(HaveKey("n_discovery_kubernetes_k8s->n_prometheus_scrape_app"))
			// receiver export: scrape → remote_write, the same way data flows
			Expect(byID).To(HaveKey("n_prometheus_scrape_app->n_prometheus_remote_write_sink"))
			Expect(byID["n_prometheus_scrape_app->n_prometheus_remote_write_sink"].From.Port).To(Equal("forward_to"))
			Expect(byID["n_prometheus_scrape_app->n_prometheus_remote_write_sink"].To.Port).To(Equal("receiver"))
			// consumed references are not left behind as props
			for _, n := range result.Doc.Nodes {
				Expect(n.Props).NotTo(HaveKey("forward_to"))
				Expect(n.Props).NotTo(HaveKey("targets"))
			}
		})

		It("addresses a reference nested in a block by its port path", func() {
			result := visual.ParseAlloy(`
otelcol.receiver.otlp "ingest" {
  output {
    metrics = [otelcol.exporter.otlp.remote.input]
  }
}

otelcol.exporter.otlp "remote" {
  client {
    endpoint = "otel:4317"
  }
}
`, "v1.18.1", corpusSchema())
			Expect(result.Doc.Edges).To(HaveLen(1))
			Expect(result.Doc.Edges[0].From.Port).To(Equal("output.metrics"))
			Expect(result.Doc.Edges[0].To.Port).To(Equal("input"))
			output, isObject := result.Doc.Nodes[0].Props["output"].(map[string]interface{})
			Expect(isObject).To(BeTrue())
			Expect(output).NotTo(HaveKey("metrics"))
		})

		It("round-trips a pipeline through the renderer", func() {
			schema := corpusSchema()
			original := `discovery.kubernetes "k8s" {
  role = "pod"
}

prometheus.scrape "app" {
  targets = discovery.kubernetes.k8s.targets
  forward_to = [prometheus.remote_write.sink.receiver]
  job_name = "app"
  scrape_interval = "30s"
}

prometheus.remote_write "sink" {
  endpoint {
    url = "https://prom/api/v1/write"
  }
}
`
			parsed := visual.ParseAlloy(original, "v1.18.1", schema)
			Expect(parsed.Opaque).To(BeFalse())
			rendered := visual.Render(parsed.Doc, schema)
			Expect(rendered.Diagnostics).To(BeEmpty())
			Expect(rendered.Content).To(Equal(
				"// generated by shepherd visual builder — do not edit by hand (edits will be overwritten); graph revision 3, schema v1.18.1\n\n" +
					original[:len(original)-1] + "\n"))
		})

		// Every config Shepherd generated BEFORE refValue (render.go) stopped
		// bracket-wrapping []discovery.Target references — and every hand-written
		// one copied from those — spells the wire `targets = [x.targets]`, which
		// real Alloy refuses at run time. Importing one must still recover the
		// edge (otherwise the round trip silently drops the user's pipeline and
		// renders an orphan scrape), and re-rendering it must emit the runnable
		// bare form, which is what makes the import the migration path.
		It("imports the legacy bracket-wrapped targets wire and re-renders it runnable", func() {
			schema := corpusSchema()
			legacy := `discovery.kubernetes "k8s" {
  role = "pod"
}

prometheus.scrape "app" {
  targets = [discovery.kubernetes.k8s.targets]
}
`
			parsed := visual.ParseAlloy(legacy, "v1.18.1", schema)
			Expect(parsed.Opaque).To(BeFalse())
			Expect(parsed.Doc.Edges).To(HaveLen(1), "the wire must survive the import, not be dropped as an opaque value")

			rendered := visual.Render(parsed.Doc, schema)
			Expect(rendered.Diagnostics).To(BeEmpty())
			Expect(rendered.Content).To(ContainSubstring("targets = discovery.kubernetes.k8s.targets"))
			Expect(rendered.Content).NotTo(ContainSubstring("targets = [discovery"))
		})
	})
})
